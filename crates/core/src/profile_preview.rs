//! Browsing an author's profile before downloading (docs/design/multi-site-contracts.md §6).
//!
//! A preview asks the site's adapter for one page of the profile's list at a time. It stores
//! no media and creates no subscription; what it keeps is the list the user was shown, so a
//! download request can only name entries that were on screen.
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs;
use crate::source_acquisition::{Opened, open_credential};
use crate::store::deliveries as deliveries_repo;
use crate::store::entries;
use crate::store::previews as repo;
use rusqlite::params;
use serde_json::json;
use std::collections::HashSet;
use trace_contracts::catalog::Recognition;
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedEntry, EnumeratedPage, ResolvedAccount, methods,
};
use trace_contracts::preview::{
    MAX_ITEMS, MAX_PAGES, MAX_SELECTED, ProfileDownloadArgs, ProfileDownloadReport, ProfileItem,
    ProfilePage, ProfilePreviewArgs, ProfileRuleChoice, SkippedEntry,
};
use trace_contracts::rules::RuleEntry;
use trace_domain::sites;

const PREVIEW_MINUTES: i64 = 30;
const DEADLINE_MS: u64 = 30_000;
const KEY_MAX: usize = 256;
const TITLE_MAX: usize = 300;
const SUMMARY_MAX: usize = 500;
const EXPIRED: &str = "预览已过期，请重新打开作者主页";

fn cut(text: &str, max: usize) -> String {
    text.chars().take(max).collect()
}

fn expired(at: &str) -> bool {
    time::OffsetDateTime::parse(at, &time::format_description::well_known::Rfc3339)
        .map_or(true, |t| t < time::OffsetDateTime::now_utc())
}

fn later(minutes: i64) -> Result<String, CoreError> {
    (time::OffsetDateTime::now_utc() + time::Duration::minutes(minutes))
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|_| CoreError::Internal("time".into()))
}

/// A page shown to the user, with the state of the preview it belongs to.
struct Loaded {
    page: EnumeratedPage,
}

/// Asks the adapter for the page at `cursor`, refusing a page that cannot be trusted: too many
/// entries, or a claim of more content that does not move on.
async fn fetch_page(
    core: &Core,
    preview: &repo::Preview,
    scope: Option<&str>,
    cursor: Option<String>,
) -> Result<Loaded, CoreError> {
    let params = EnumerateParams {
        content_type: preview.lane.clone().unwrap_or_else(|| "video".into()),
        native_account_key: preview.account_key.clone(),
        mode: "history".into(),
        cursor: cursor.clone(),
        recent_days: None,
        max_pages: 1,
    };
    core.metadata_pacing(&preview.platform).await;
    let value = core
        .host
        .call_scoped(
            &preview.platform,
            scope,
            methods::ENUMERATE,
            serde_json::to_value(&params)?,
            Some(DEADLINE_MS),
        )
        .await?;
    let page: EnumeratedPage = serde_json::from_value(value)
        .map_err(|e| CoreError::InvalidInput(format!("bad page: {e}")))?;
    let stuck = page.has_more && (page.next_cursor.is_none() || page.next_cursor == cursor);
    if page.items.len() > trace_domain::MAX_PAGE_ITEMS as usize || stuck {
        return Err(CoreError::InvalidInput(
            "non-advancing or oversized page".into(),
        ));
    }
    Ok(Loaded { page })
}

/// One shown entry, as the user sees it.
fn item(
    e: EnumeratedEntry,
    entry_id: Option<String>,
    save_progress: Option<String>,
) -> ProfileItem {
    let hints = e.hints.unwrap_or_default();
    ProfileItem {
        native_key: e.native_entry_key,
        native_kind: e.native_kind,
        title: cut(&e.title, TITLE_MAX),
        summary: e
            .summary_body
            .map(|t| cut(t.trim(), SUMMARY_MAX))
            .filter(|t| !t.is_empty()),
        published_at: e.published_at,
        published_precision: if e.published_precision.is_empty() {
            "unknown".into()
        } else {
            e.published_precision
        },
        pinned: e.pinned,
        content_kinds: hints.content_kinds,
        image_count: hints.image_count,
        video_count: hints.video_count,
        duration_ms: hints.duration_ms,
        in_library: entry_id.is_some(),
        entry_id,
        save_progress,
    }
}

/// Stores a loaded page and answers with the entries that were not shown before.
async fn show_page(
    core: &Core,
    preview: repo::Preview,
    loaded: Loaded,
    alternatives: Vec<ProfileRuleChoice>,
    rule_title: String,
    new_preview: bool,
) -> Result<ProfilePage, CoreError> {
    let Loaded { page } = loaded;
    let entries_in: Vec<_> = page
        .items
        .into_iter()
        .filter(|e| !e.native_entry_key.is_empty() && e.native_entry_key.len() <= KEY_MAX)
        .collect();
    let (id, pages_before, namespace) = (
        preview.preview_id.clone(),
        preview.pages,
        preview.platform.clone(),
    );
    let (next_cursor, has_more_site) = (page.next_cursor.clone(), page.has_more);
    let record = new_preview.then(|| preview.clone());
    let (shown, total, limited, has_more) = core
        .db
        .write(move |tx| {
            let now = jobs::now();
            repo::purge_expired(tx, &now)?;
            if let Some(record) = &record {
                repo::insert(tx, record, &now)?;
            }
            let room = MAX_ITEMS.saturating_sub(repo::shown_count(tx, &id)? as usize);
            let fits = entries_in.len().min(room);
            let items: Vec<_> = entries_in
                .iter()
                .take(fits)
                .map(|e| repo::Shown {
                    native_key: &e.native_entry_key,
                    native_kind: &e.native_kind,
                    title: &e.title,
                })
                .collect();
            let reached = pages_before + 1 >= MAX_PAGES || fits < entries_in.len() || room == fits;
            let more = has_more_site && !reached;
            let fresh =
                repo::add_page(tx, &id, &items, next_cursor.as_deref(), more, pages_before)?;
            let fresh: HashSet<String> = fresh.into_iter().collect();
            let shown: Vec<_> = entries_in
                .into_iter()
                .take(fits)
                .filter(|e| fresh.contains(&e.native_entry_key))
                .collect();
            let keys: Vec<String> = shown.iter().map(|e| e.native_entry_key.clone()).collect();
            let known = repo::known_entries(tx, &namespace, &keys)?;
            let total = repo::shown_count(tx, &id)?;
            let mut out = Vec::new();
            for e in shown {
                let known = known.iter().find(|k| k.0 == e.native_entry_key);
                let progress = match known {
                    Some((_, entry, _)) => Some(entries::states(tx, entry)?.1),
                    None => None,
                };
                out.push((e, known.map(|k| k.1.clone()), progress));
            }
            Ok((out, total, has_more_site && !more, more))
        })
        .await?;
    let items = shown
        .into_iter()
        .map(|(e, entry_id, progress)| item(e, entry_id, progress))
        .collect();
    let site_title = sites::site(&preview.platform)
        .map_or("", |s| s.title)
        .to_owned();
    Ok(ProfilePage {
        preview_id: preview.preview_id,
        platform: preview.platform,
        site_title,
        rule_id: preview.rule_id,
        rule_title,
        alternatives,
        account_key: preview.account_key,
        account_name: preview.account_name,
        authentication: preview.authentication,
        items,
        pages_loaded: pages_before + 1,
        page_limit: MAX_PAGES,
        total_items: total,
        has_more,
        limited,
        expires_at: preview.expires_at,
    })
}

/// The order the lanes of an author are offered in; a lane nobody ranked comes last.
fn lane_rank(lane: Option<&str>) -> usize {
    const ORDER: [&str; 6] = ["video", "post", "short", "image", "dynamic", "article"];
    ORDER
        .iter()
        .position(|known| Some(*known) == lane)
        .unwrap_or(ORDER.len())
}

/// The preview rules a link fits.
fn preview_rules(
    recognition: &Recognition,
) -> Result<Vec<(String, String, Option<String>)>, CoreError> {
    let mut rules: Vec<_> = recognition
        .rules
        .iter()
        .filter(|r| r.entry == RuleEntry::Preview)
        .map(|r| (r.rule_id.clone(), r.title.clone(), r.lane.clone()))
        .collect();
    // The first rule is the one shown until the person picks another: an author's videos before
    // their other lanes, not the alphabet.
    rules.sort_by_key(|r| lane_rank(r.2.as_deref()));
    if rules.is_empty() {
        return Err(CoreError::InvalidInput(
            recognition
                .message
                .clone()
                .unwrap_or_else(|| "这个链接不是可以预览的作者主页".into()),
        ));
    }
    Ok(rules)
}

async fn first_page(core: &Core, args: ProfilePreviewArgs) -> Result<ProfilePage, CoreError> {
    let text = args
        .url
        .clone()
        .ok_or_else(|| CoreError::InvalidInput("需要作者主页链接".into()))?;
    let url = crate::catalog::extract_url(&text)
        .ok_or_else(|| CoreError::InvalidInput("没有找到链接".into()))?;
    let input = text.clone();
    let recognition = core
        .db
        .read(move |c| crate::catalog::recognize(c, &input))
        .await?;
    let site = recognition
        .site
        .clone()
        .ok_or_else(|| CoreError::Unsupported(recognition.message.clone().unwrap_or_default()))?;
    let rules = preview_rules(&recognition)?;
    let (rule_id, rule_title, lane) = match &args.rule_id {
        Some(id) => rules
            .iter()
            .find(|r| &r.0 == id)
            .cloned()
            .ok_or_else(|| CoreError::InvalidInput("这个链接不适用所选规则".into()))?,
        None => rules[0].clone(),
    };
    let alternatives = rules
        .iter()
        .filter(|r| r.0 != rule_id)
        .map(|r| ProfileRuleChoice {
            rule_id: r.0.clone(),
            title: r.1.clone(),
        })
        .collect();
    start(
        core,
        Begin {
            site,
            url,
            rule: (rule_id, rule_title, lane),
            alternatives,
            credential_id: args.credential_id,
        },
    )
    .await
}

/// What a preview needs to begin: the site and rule that were recognized for the link.
pub struct Begin {
    pub site: String,
    pub url: String,
    /// Rule id, title and the list lane it enumerates.
    pub rule: (String, String, Option<String>),
    pub alternatives: Vec<ProfileRuleChoice>,
    pub credential_id: Option<String>,
}

/// Opens a preview of a profile whose site and rule are already known: identifies the owner,
/// then loads and shows the first page.
pub async fn start(core: &Core, begin: Begin) -> Result<ProfilePage, CoreError> {
    let Begin {
        site,
        url,
        rule: (rule_id, rule_title, lane),
        alternatives,
        credential_id,
    } = begin;
    let Opened {
        credential, scope, ..
    } = open_credential(core, &site, credential_id.as_deref(), true).await?;
    core.metadata_pacing(&site).await;
    let value = core
        .host
        .call_scoped(
            &site,
            scope.as_deref(),
            methods::RESOLVE_ACCOUNT,
            json!({"input": url}),
            Some(DEADLINE_MS),
        )
        .await?;
    let account: ResolvedAccount = serde_json::from_value(value)
        .map_err(|e| CoreError::InvalidInput(format!("bad account: {e}")))?;
    if account.native_account_key.is_empty() || account.native_account_key.len() > KEY_MAX {
        return Err(CoreError::InvalidInput("source owner identity".into()));
    }
    let preview = repo::Preview {
        preview_id: format!("profile-{}", uuid::Uuid::new_v4()),
        platform: site,
        rule_id,
        rule_title: rule_title.clone(),
        source_url: url,
        account_key: account.native_account_key,
        account_name: account.display_name.map(|n| cut(&n, TITLE_MAX)),
        lane,
        credential_id: credential.as_ref().map(|c| c.0.clone()),
        credential_generation: credential.as_ref().map(|c| c.1),
        authentication: if credential.is_some() {
            "authenticated"
        } else {
            "anonymous"
        }
        .into(),
        pages: 0,
        next_cursor: None,
        has_more: true,
        expires_at: later(PREVIEW_MINUTES)?,
    };
    let loaded = fetch_page(core, &preview, scope.as_deref(), None).await?;
    show_page(core, preview, loaded, alternatives, rule_title, true).await
}

async fn next_page(core: &Core, id: &str) -> Result<ProfilePage, CoreError> {
    let wanted = id.to_owned();
    let preview = core
        .db
        .read(move |c| repo::load(c, &wanted))
        .await?
        .ok_or_else(|| CoreError::VersionConflict(EXPIRED.into()))?;
    if expired(&preview.expires_at) {
        return Err(CoreError::VersionConflict(EXPIRED.into()));
    }
    if !preview.has_more {
        return Err(CoreError::InvalidInput("没有更多内容可以加载".into()));
    }
    let Opened { scope, .. } = open_credential(
        core,
        &preview.platform,
        preview.credential_id.as_deref(),
        false,
    )
    .await?;
    let cursor = preview.next_cursor.clone();
    let loaded = fetch_page(core, &preview, scope.as_deref(), cursor).await?;
    let title = preview.rule_title.clone();
    show_page(core, preview, loaded, Vec::new(), title, false).await
}

/// One page of a profile: the first (`url`) or the next one (`preview_id`).
pub async fn preview(core: &Core, args: ProfilePreviewArgs) -> Result<ProfilePage, CoreError> {
    match args.preview_id.clone() {
        Some(id) if args.url.is_none() => next_page(core, &id).await,
        Some(_) => Err(CoreError::InvalidInput("继续加载时不需要再给链接".into())),
        None => first_page(core, args).await,
    }
}

/// Creates the download request for the entries chosen in a preview.
pub async fn download(
    core: &Core,
    args: ProfileDownloadArgs,
) -> Result<ProfileDownloadReport, CoreError> {
    let mut seen = HashSet::new();
    let keys: Vec<String> = args
        .native_keys
        .into_iter()
        .filter(|k| seen.insert(k.clone()))
        .collect();
    if keys.is_empty() || keys.len() > MAX_SELECTED {
        return Err(CoreError::InvalidInput(format!(
            "一次需要选择 1 到 {MAX_SELECTED} 个内容"
        )));
    }
    let target = crate::downloads::preflight(core, &args.target).await?;
    let id = args.preview_id;
    core.db
        .write(move |tx| {
            let now = jobs::now();
            let preview = repo::load(tx, &id)?
                .filter(|p| !expired(&p.expires_at))
                .ok_or_else(|| CoreError::VersionConflict(EXPIRED.into()))?;
            let current = crate::source_acquisition::credential(
                tx,
                preview.credential_id.as_deref(),
                &preview.platform,
            )?
            .map(|c| c.1);
            if current != preview.credential_generation {
                return Err(CoreError::VersionConflict(EXPIRED.into()));
            }
            create_request(tx, &preview, &keys, &target, &now)
        })
        .await
}

fn create_request(
    tx: &rusqlite::Transaction<'_>,
    preview: &repo::Preview,
    keys: &[String],
    target: &trace_contracts::preview::DownloadTarget,
    now: &str,
) -> Result<ProfileDownloadReport, CoreError> {
    let shown = repo::shown(tx, &preview.preview_id, keys)?;
    let mut skipped: Vec<SkippedEntry> = keys
        .iter()
        .filter(|k| !shown.iter().any(|s| &s.0 == *k))
        .map(|k| SkippedEntry {
            native_key: k.clone(),
            reason: "不在预览列表里".into(),
        })
        .collect();
    tx.execute(
        "INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?2,?3,?4,?5)",
        params![
            format!("acct-{}", uuid::Uuid::new_v4()),
            preview.platform,
            preview.account_key,
            preview.account_name,
            now
        ],
    )?;
    let account: String = tx.query_row(
        "SELECT account_id FROM source_accounts WHERE namespace=?1 AND native_account_key=?2",
        params![preview.platform, preview.account_key],
        |r| r.get(0),
    )?;
    let delivery = format!("delivery-{}", uuid::Uuid::new_v4());
    let mut chosen = Vec::new();
    for (key, kind, title) in &shown {
        tx.execute(
            "INSERT OR IGNORE INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,?2,?3,?4,?5,?6)",
            params![
                format!("ent-{}", uuid::Uuid::new_v4()),
                preview.platform,
                kind,
                key,
                account,
                now
            ],
        )?;
        let (entry, blocked): (String, bool) = tx.query_row(
            "SELECT entry_id,lifecycle='deleted' OR media_suppressed=1 FROM entries WHERE namespace=?1 AND native_kind=?2 AND native_entry_key=?3",
            params![preview.platform, kind, key],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )?;
        if blocked {
            skipped.push(SkippedEntry {
                native_key: key.clone(),
                reason: "这条内容已被删除，不再下载".into(),
            });
            continue;
        }
        let complete = entries::states(tx, &entry)?.1 == "complete";
        chosen.push((key.clone(), kind.clone(), title.clone(), entry, complete));
    }
    if chosen.is_empty() {
        let why = skipped
            .first()
            .map_or("没有可下载的内容", |s| s.reason.as_str());
        return Err(CoreError::InvalidInput(why.into()));
    }
    let scope = format!("once-{delivery}");
    deliveries_repo::create_scope(tx, &scope, &account, preview.credential_id.as_deref(), now)?;
    let items: Vec<_> = chosen
        .iter()
        .map(
            |(key, kind, title, entry, complete)| deliveries_repo::NewItem {
                native_key: key,
                native_kind: kind,
                title,
                entry_id: entry,
                ready: *complete,
            },
        )
        .collect();
    deliveries_repo::insert(
        tx,
        &deliveries_repo::NewDelivery {
            delivery_id: &delivery,
            kind: "profile",
            platform: &preview.platform,
            source_url: &preview.source_url,
            request_id: None,
            scope_id: &scope,
            destination: &serde_json::to_string(target)?,
        },
        &items,
        now,
    )?;
    for (_, _, _, entry, _) in chosen.iter().filter(|c| !c.4) {
        let payload = json!({
            "entry_id": entry,
            "subscription_id": scope,
            "delivery_id": delivery,
        });
        tx.execute(
            "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'observe',?3,?4,'queued',1,?5,?5)",
            params![
                format!("job-{}", uuid::Uuid::new_v4()),
                format!("delivery-observe:{delivery}:{entry}"),
                entry,
                payload.to_string(),
                now
            ],
        )?;
    }
    deliveries_repo::settle(tx, &delivery, now)?;
    Ok(ProfileDownloadReport {
        delivery_id: delivery,
        accepted: chosen.iter().filter(|c| !c.4).count() as u32,
        already_in_library: chosen.iter().filter(|c| c.4).count() as u32,
        skipped,
    })
}

#[cfg(test)]
#[path = "profile_preview_tests.rs"]
mod profile_preview_tests;
