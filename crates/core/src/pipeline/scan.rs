//! Subscription scans (C01/C25/C30/C31).
//!
//! One active job per scan lane: it pages through the adapter within a budget, stores each
//! page together with its cursor, stays queued while pages remain, and at the end of a
//! round schedules the next check by the subscription's interval.
use super::scan_page::{self, ScanPage};
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::{self, ClaimedJob};
use crate::scan_range::{Placement, Range};
use crate::store;
use serde_json::Value;
use std::collections::HashSet;
use trace_contracts::plugin::{EnumerateParams, EnumeratedEntry, EnumeratedPage, methods};
use trace_domain::jobs::{ScanMode, scan_mode_of};

const ENUMERATE_DEADLINE_MS: u64 = 30_000;
const INCREMENTAL_RECENT_DAYS: i64 = 7;
const DEEP_INTERVAL_SECONDS: i64 = 7 * 86400;
/// Pages a history or deep round may take before it yields to other work.
const BACKGROUND_PAGE_BUDGET: u32 = 2;
const PAGE_LOOP_MAX: u32 = 100;
/// Pages one sweep reads of a list that carries no dates at all, for a range that follows the
/// present: nothing in it can say where the recent entries end, and the details place the rest.
const UNDATED_SWEEP_PAGES: u32 = 5;

/// Everything one scan round needs from the subscription and its account.
struct Scan {
    subscription_id: String,
    mode: ScanMode,
    namespace: String,
    platform: String,
    account_key: String,
    content_type: String,
    range: Range,
    recent_days: Option<i64>,
    max_pages: u32,
}

/// Where a round ended.
struct Progress {
    cursor: Option<String>,
    has_more: bool,
    pages: u32,
    new_entries: i64,
    /// New entries the list could not place; their details decide (§5.3).
    unplaced: i64,
}

pub(crate) async fn run_scan(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let subscription_id = job
        .payload
        .get("subscription_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| CoreError::InvalidInput("payload".into()))?
        .to_string();
    let mode = scan_mode_of(&job.payload).map_err(CoreError::InvalidInput)?;
    let id = subscription_id.clone();
    let target = core.db.read(move |c| store::scan::target(c, &id)).await?;
    if target.state != "enabled" {
        return Err(CoreError::Paused("subscription disabled".into()));
    }
    let platform = target.namespace.split(':').next().unwrap_or("").to_string();
    if matches!(platform.as_str(), "bilibili" | "douyin") {
        crate::accounts::schedule(core, job, &subscription_id).await?;
    }
    if target.content_type == "live" {
        return crate::recording::poll(core, job, &target.account_key, &platform).await;
    }
    if core.host.spec_for_platform(&platform).is_none() {
        return wait_for_plugin(core, job).await;
    }
    let range_value: Value = serde_json::from_str(&target.range_json)?;
    let cursor = start_cursor(core, job, &subscription_id, mode, &range_value).await?;
    let scan = Scan {
        range: Range::parse(&range_value, &target.created_at)?,
        recent_days: recent_days(mode, &range_value),
        max_pages: page_budget(core, job, mode, &range_value),
        subscription_id,
        mode,
        namespace: target.namespace,
        platform,
        account_key: target.account_key,
        content_type: target.content_type,
    };
    let progress = fetch_pages(core, job, &scan, cursor).await?;
    if scan.mode == ScanMode::History && !progress.has_more {
        return Ok(());
    }
    schedule_next(core, job, &scan, progress).await
}

/// A missing or disabled plugin parks the job instead of failing it (C19).
async fn wait_for_plugin(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            store::scan::wait_for_plugin(tx, &guard.job_id, &jobs::now())
        })
        .await
}

/// The job's own cursor first, then the stored checkpoint, then a range that resumes from one.
async fn start_cursor(
    core: &Core,
    job: &ClaimedJob,
    subscription_id: &str,
    mode: ScanMode,
    range: &Value,
) -> Result<Option<String>, CoreError> {
    let mut cursor = job
        .payload
        .get("cursor")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    if cursor.is_none() {
        let (id, scan_type) = (subscription_id.to_owned(), mode.as_str().to_owned());
        cursor = core
            .db
            .read(move |c| Ok(store::scan::checkpoint_cursor(c, &id, &scan_type)))
            .await?;
    }
    if cursor.is_none() && mode == ScanMode::History && range["kind"] == "resume_cursor" {
        let id = subscription_id.to_owned();
        let started = core
            .db
            .read(move |c| store::scan::history_started(c, &id))
            .await?;
        if !started {
            cursor = range["cursor"].as_str().map(str::to_owned);
        }
    }
    Ok(cursor)
}

fn recent_days(mode: ScanMode, range: &Value) -> Option<i64> {
    if mode == ScanMode::Incremental {
        Some(INCREMENTAL_RECENT_DAYS)
    } else {
        range["days"].as_i64()
    }
}

/// Pages one round may fetch: the job's own limit within the profile budget, and less for
/// incremental checks (by the range's setting) and for background rounds.
fn page_budget(core: &Core, job: &ClaimedJob, mode: ScanMode, range: &Value) -> u32 {
    let profile_budget = core.profile.scan_page_budget() as u64;
    let requested = job
        .payload
        .get("max_pages")
        .and_then(|v| v.as_u64())
        .unwrap_or(profile_budget)
        .clamp(1, profile_budget) as u32;
    if mode == ScanMode::Incremental {
        let configured = range["incremental_page_budget"].as_u64().unwrap_or(2);
        requested.min(configured.clamp(1, 10) as u32)
    } else {
        requested.min(BACKGROUND_PAGE_BUDGET)
    }
}

async fn fetch_pages(
    core: &Core,
    job: &ClaimedJob,
    scan: &Scan,
    mut cursor: Option<String>,
) -> Result<Progress, CoreError> {
    let mut seen_cursors = HashSet::new();
    let mut progress = Progress {
        cursor: None,
        has_more: false,
        pages: 0,
        new_entries: 0,
        unplaced: 0,
    };
    for _ in 0..scan.max_pages.clamp(1, PAGE_LOOP_MAX) {
        core.metadata_pacing(&scan.namespace).await;
        if !seen_cursors.insert(cursor.clone()) {
            return Err(CoreError::InvalidInput("cursor cycle".into()));
        }
        let mut page = fetch_page(core, job, scan, cursor.as_ref()).await?;
        let sweep = (
            scan.subscription_id.as_str(),
            scan.mode.as_str(),
            &scan.range,
        );
        if let Some(pages) = undated_cap_reached(core, sweep, &page).await? {
            page.has_more = false;
            page.next_cursor = None;
            page.coverage = capped_coverage(&page.coverage, pages);
        }
        let stored = scan_page::store_page(
            core,
            job,
            ScanPage {
                namespace: &scan.namespace,
                subscription_id: &scan.subscription_id,
                mode: scan.mode.as_str(),
                items: narrowed(&scan.range, &page.items),
                cursor: page.next_cursor.clone(),
                coverage: &page.coverage,
                range: &scan.range,
            },
        )
        .await?;
        progress.new_entries += stored.added;
        progress.unplaced += stored.unplaced;
        cursor = page.next_cursor.clone();
        progress.pages += 1;
        progress.has_more = page.has_more;
        if !page.has_more {
            break;
        }
    }
    progress.cursor = cursor;
    Ok(progress)
}

/// What the range does not rule out: an entry whose date cannot reach the range is not stored
/// at all, one that has no date stays for its details to place.
pub(crate) fn narrowed(range: &Range, items: &[EnumeratedEntry]) -> Vec<EnumeratedEntry> {
    items
        .iter()
        .filter(|i| range.place_listed(i) != Placement::Outside)
        .cloned()
        .collect()
}

/// The number of pages read so far in this sweep when a list without any date has reached the
/// cap for a range that follows the present.
pub(crate) async fn undated_cap_reached(
    core: &Core,
    (subscription, mode, range): (&str, &str, &Range),
    page: &EnumeratedPage,
) -> Result<Option<u32>, CoreError> {
    let undated = !page.items.is_empty() && page.items.iter().all(|i| i.published_at.is_none());
    if !page.has_more || !undated || !range.follows_the_present() {
        return Ok(None);
    }
    let (id, mode) = (subscription.to_owned(), mode.to_owned());
    let earlier: i64 = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT count(*) FROM scan_cursor_visits WHERE subscription_id=?1 AND scan_type=?2",
                rusqlite::params![id, mode],
                |r| r.get(0),
            )?)
        })
        .await?;
    let pages = u32::try_from(earlier).unwrap_or(u32::MAX).saturating_add(1);
    Ok((pages >= UNDATED_SWEEP_PAGES).then_some(pages))
}

/// The coverage of a sweep that stopped at the cap says so, instead of looking complete.
fn capped_coverage(coverage: &Value, pages: u32) -> Value {
    let mut out = if coverage.is_object() {
        coverage.clone()
    } else {
        serde_json::json!({})
    };
    out["undated_page_cap"] = serde_json::json!({"pages": pages});
    out
}

async fn fetch_page(
    core: &Core,
    job: &ClaimedJob,
    scan: &Scan,
    cursor: Option<&String>,
) -> Result<EnumeratedPage, CoreError> {
    let params = EnumerateParams {
        content_type: scan.content_type.clone(),
        native_account_key: scan.account_key.clone(),
        mode: scan.mode.as_str().to_owned(),
        cursor: cursor.cloned(),
        recent_days: scan.recent_days,
        max_pages: 1,
    };
    let value = core
        .adapter_call(
            job,
            &scan.platform,
            methods::ENUMERATE,
            serde_json::to_value(&params).unwrap_or_default(),
            Some(ENUMERATE_DEADLINE_MS),
        )
        .await?;
    let page: EnumeratedPage = serde_json::from_value(value)
        .map_err(|e| CoreError::InvalidInput(format!("bad page: {e}")))?;
    let oversized = page.items.len() > trace_domain::MAX_PAGE_ITEMS as usize;
    let stuck =
        page.has_more && (page.next_cursor.is_none() || page.next_cursor.as_ref() == cursor);
    if oversized || stuck {
        return Err(CoreError::InvalidInput(
            "non-advancing or oversized page".into(),
        ));
    }
    Ok(page)
}

/// Single active job: continue with the next page, or schedule the next round by interval.
async fn schedule_next(
    core: &Core,
    job: &ClaimedJob,
    scan: &Scan,
    progress: Progress,
) -> Result<(), CoreError> {
    let Progress {
        cursor,
        has_more,
        pages,
        new_entries,
        unplaced,
    } = progress;
    let mode = scan.mode;
    let interval = if mode == ScanMode::Deep {
        DEEP_INTERVAL_SECONDS
    } else {
        let id = scan.subscription_id.clone();
        core.db
            .read(move |c| Ok(store::scan::check_interval_seconds(c, &id)))
            .await?
    };
    let wait = if has_more {
        1
    } else {
        crate::config::jittered_check_seconds(interval)
    };
    let next_run_at = jobs::plus_secs(&jobs::now(), wait);
    let next_cursor = if mode == ScanMode::Incremental && !has_more {
        None
    } else {
        cursor.clone()
    };
    let (guard, id) = (job.clone(), scan.subscription_id.clone());
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            if mode == ScanMode::Incremental {
                crate::scan_range::finish_recent_sweep(
                    tx,
                    &id,
                    cursor.as_deref(),
                    has_more,
                    pages,
                )?;
            }
            let payload = serde_json::json!({
                "pages": pages,
                "new_entries": new_entries,
                "unplaced_entries": unplaced,
            });
            crate::objects::outbox_emit(tx, "scan.progress", &id, &payload)?;
            store::scan::schedule_next_run(tx, &guard.job_id, &next_run_at, next_cursor.as_deref())
        })
        .await
}
