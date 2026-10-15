//! One scanned page in one transaction (§9.1): entries, observations, cursor and coverage
//! commit together, so a crash never leaves a cursor ahead of its entries (C01).
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::{self, ClaimedJob};
use crate::scan_range::{Placement, Range, stored_margin};
use crate::store;
use rusqlite::{Connection, Transaction};
use serde_json::{Value, json};
use trace_contracts::plugin::EnumeratedEntry;
use trace_contracts::unified::StatCounters;
use trace_domain::revision::semantic_digest;
use trace_domain::search::normalize_v1;

/// A page as the adapter returned it, narrowed to the subscription's range.
pub(crate) struct ScanPage<'a> {
    pub namespace: &'a str,
    pub subscription_id: &'a str,
    pub mode: &'a str,
    pub items: Vec<EnumeratedEntry>,
    pub cursor: Option<String>,
    pub coverage: &'a Value,
    /// The subscription's range, to place what is new and to leave out what the details have
    /// already put outside it.
    pub range: &'a Range,
}

/// What a page added: entries the list put inside the range, and entries it could not place.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Stored {
    pub added: i64,
    pub unplaced: i64,
}

/// Stores the page and returns how many entries were new.
pub(crate) async fn store_page(
    core: &Core,
    job: &ClaimedJob,
    page: ScanPage<'_>,
) -> Result<Stored, CoreError> {
    let namespace = page.namespace.to_owned();
    let subscription = page.subscription_id.to_owned();
    let mode = page.mode.to_owned();
    let coverage = trace_contracts::content::safe_metadata(page.coverage);
    let cursor = page.cursor;
    let items = page.items;
    let range = *page.range;
    let now = jobs::now();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::scan_range::record_cursor(tx, &subscription, &mode, cursor.as_deref())?;
            let mut stored = Stored::default();
            for item in items {
                if settled_outside(tx, &namespace, &item, &range)? {
                    continue;
                }
                let placement = range.place_listed(&item);
                let new = store_entry(
                    tx,
                    &namespace,
                    &subscription,
                    &mode,
                    item,
                    &coverage,
                    &range,
                    &now,
                )?;
                match placement {
                    Placement::Inside => stored.added += new,
                    _ => stored.unplaced += new,
                }
            }
            let coverage_json = coverage.to_string();
            store::scan::save_checkpoint(
                tx,
                &subscription,
                &mode,
                cursor.as_deref(),
                &coverage_json,
                &now,
            )?;
            store::scan::remember_cursor(tx, &guard.job_id, cursor.as_deref())?;
            Ok(stored)
        })
        .await
}

/// An entry the list gave no date for, whose details already put it outside the range: it is
/// neither found again nor observed again (§5.3).
fn settled_outside(
    tx: &Transaction<'_>,
    namespace: &str,
    item: &EnumeratedEntry,
    range: &Range,
) -> Result<bool, CoreError> {
    if item.published_at.is_some() {
        return Ok(false);
    }
    let stored: Option<(Option<String>, Option<String>)> = tx
        .query_row(
            "SELECT published_at, published_precision FROM entries WHERE namespace=?1 AND native_kind=?2 AND native_entry_key=?3",
            rusqlite::params![namespace, item.native_kind, item.native_entry_key],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .ok();
    let Some((Some(date), precision)) = stored else {
        return Ok(false);
    };
    Ok(stored_margin(precision.as_deref())
        .is_some_and(|margin| range.place(Some(&date), margin) == Placement::Outside))
}

/// An entry the list gave no date for, whose details already put it inside the range: a repeat
/// incremental check needs the list only, not another detail read.
fn settled_inside(tx: &Transaction<'_>, entry_id: &str, range: &Range) -> bool {
    let stored: Option<(Option<String>, Option<String>)> = tx
        .query_row(
            "SELECT published_at, published_precision FROM entries WHERE entry_id=?1",
            [entry_id],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .ok();
    let Some((Some(date), precision)) = stored else {
        return false;
    };
    stored_margin(precision.as_deref())
        .is_some_and(|margin| range.place(Some(&date), margin) == Placement::Inside)
}

/// Whether this subscription has already completed an observation of the entry. A different
/// subscription still observes it: its own authentication history is a separate record.
fn observed_by(tx: &Transaction<'_>, entry_id: &str, subscription: &str) -> bool {
    tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM job_consumers c JOIN jobs j ON j.job_id=c.job_id \
         WHERE c.subscription_id=?2 AND j.job_type='observe' \
         AND json_extract(j.payload,'$.entry_id')=?1 AND c.completed>0)",
        rusqlite::params![entry_id, subscription],
        |r| r.get::<_, bool>(0),
    )
    .unwrap_or(false)
}

/// Observations carry the adapter's statistics plus the coverage of the scan that saw them.
fn evidence_of(item: &EnumeratedEntry, coverage: &Value) -> Value {
    let mut stats = trace_contracts::content::safe_metadata(&item.stats);
    if !stats.is_object() {
        stats = json!({"stats": stats});
    }
    stats["_trace_enumeration"] = coverage.clone();
    stats
}

/// Returns 1 for a newly discovered entry, 0 for a known or deleted one.
#[allow(clippy::too_many_arguments)] // one scanned entry with the page's range and coverage
fn store_entry(
    tx: &Transaction<'_>,
    namespace: &str,
    subscription: &str,
    mode: &str,
    item: EnumeratedEntry,
    coverage: &Value,
    range: &Range,
    now: &str,
) -> Result<i64, CoreError> {
    let evidence = evidence_of(&item, coverage).to_string();
    let (entry_id, is_new) = find_or_insert_entry(tx, namespace, subscription, &item, now)?;
    if is_deleted(tx, &entry_id)? {
        return Ok(i64::from(is_new));
    }
    let site = namespace.split(':').next().unwrap_or("");
    let stats = StatCounters::from_value(&item.stats);
    store::meta::record_listing(tx, &entry_id, site, item.pinned, &stats, now)?;
    // An undated entry the details already placed inside the range is known: an incremental
    // check reads the list again but does not fetch the same detail on every pass. A deep
    // sweep still observes it in full, and an entry whose details placed it outside is left
    // out before this point (§5.3).
    if mode == "incremental"
        && item.published_at.is_none()
        && has_full_revision(tx, &entry_id)
        && settled_inside(tx, &entry_id, range)
        && observed_by(tx, &entry_id, subscription)
    {
        return Ok(0);
    }
    // A short summary never overrides a full revision; the observe job completes the detail.
    match item
        .summary_body
        .filter(|_| !has_full_revision(tx, &entry_id))
    {
        Some(body) => {
            let revision = summary_revision(tx, &entry_id, &item.title, &body, now)?;
            insert_observation(tx, &entry_id, Some(&revision), &evidence, now)?;
        }
        None => insert_observation(tx, &entry_id, None, &evidence, now)?,
    }
    jobs::consumers::discover(tx, &entry_id, subscription)?;
    // A stable logical job holds independent pending consumers.
    let key = format!(
        "observe:{namespace}:{}:{}",
        item.native_kind, item.native_entry_key
    );
    let payload = json!({"entry_id": entry_id, "subscription_id": subscription});
    jobs::consumers::request(tx, &key, "observe", &entry_id, payload, subscription)?;
    Ok(i64::from(is_new))
}

fn find_or_insert_entry(
    tx: &Transaction<'_>,
    namespace: &str,
    subscription: &str,
    item: &EnumeratedEntry,
    now: &str,
) -> Result<(String, bool), CoreError> {
    let existing = tx
        .query_row(
            "SELECT entry_id FROM entries WHERE namespace=?1 AND native_kind=?2 AND native_entry_key=?3",
            rusqlite::params![namespace, item.native_kind, item.native_entry_key],
            |r| r.get::<_, String>(0),
        )
        .ok();
    if let Some(id) = existing {
        return Ok((id, false));
    }
    let id = format!("ent-{}", uuid::Uuid::new_v4());
    tx.execute(
        "INSERT INTO entries (entry_id, namespace, native_kind, native_entry_key, account_id,
         first_seen_at, published_at, published_precision, lifecycle)
         SELECT ?1, ?2, ?3, ?4, s.account_id, ?5, ?6, ?7, 'active'
         FROM source_accounts s
         JOIN subscriptions sub ON sub.account_id = s.account_id
         WHERE sub.subscription_id = ?8 LIMIT 1",
        rusqlite::params![
            id,
            namespace,
            item.native_kind,
            item.native_entry_key,
            now,
            item.published_at,
            item.published_precision,
            subscription
        ],
    )?;
    Ok((id, true))
}

fn is_deleted(c: &Connection, entry_id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT lifecycle='deleted' FROM entries WHERE entry_id=?1",
        [entry_id],
        |r| r.get::<_, bool>(0),
    )?)
}

/// A failed lookup counts as "has one": the summary is skipped rather than risk overriding it.
fn has_full_revision(c: &Connection, entry_id: &str) -> bool {
    c.query_row(
        "SELECT EXISTS(SELECT 1 FROM entry_revisions WHERE entry_id=?1 AND completeness='full')",
        [entry_id],
        |r| r.get::<_, bool>(0),
    )
    .unwrap_or(true)
}

/// The summary revision of this title and body, created on first sight.
fn summary_revision(
    tx: &Transaction<'_>,
    entry_id: &str,
    title: &str,
    body: &str,
    now: &str,
) -> Result<String, CoreError> {
    let digest = semantic_digest(title, &normalize_v1(body), &[]);
    let existing: Option<String> = tx
        .query_row(
            "SELECT revision_id FROM entry_revisions WHERE entry_id=?1 AND semantic_digest=?2",
            rusqlite::params![entry_id, digest],
            |r| r.get(0),
        )
        .ok();
    if let Some(id) = existing {
        return Ok(id);
    }
    let id = format!("rev-{}", uuid::Uuid::new_v4());
    tx.execute(
        "INSERT INTO entry_revisions (revision_id, entry_id, semantic_digest, title, normalized_body, completeness, observed_at)
         VALUES (?1,?2,?3,?4,?5,'summary',?6)",
        rusqlite::params![id, entry_id, digest, title, body, now],
    )?;
    Ok(id)
}

fn insert_observation(
    tx: &Transaction<'_>,
    entry_id: &str,
    revision_id: Option<&str>,
    evidence: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO source_observations (observation_id, entry_id, revision_id, access_state, observed_at, observation_seq, evidence)
         VALUES (?1,?2,?3,'visible',?4,
         COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1), ?5)",
        rusqlite::params![
            format!("obs-{}", uuid::Uuid::new_v4()),
            entry_id,
            revision_id,
            now,
            evidence
        ],
    )?;
    Ok(())
}
