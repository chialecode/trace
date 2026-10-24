//! Download requests (docs/design/multi-site-contracts.md §6–§7): a request settles when none
//! of its jobs is on its way any more; then the content that was saved is written to the
//! chosen folder and every entry reports what became of it.
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::store::deliveries as repo;
use crate::store::entries;
use std::collections::HashMap;
use trace_contracts::preview::{
    DeliveryItemView, DeliveryList, DeliveryListArgs, DeliveryView, DownloadTarget,
};

const LIST_DEFAULT: u32 = 20;
const LIST_MAX: u32 = 100;

/// Why nothing was saved, from the class the stopped job recorded.
fn stopped_message(class: Option<&str>) -> &'static str {
    match class {
        Some("auth_required") => "需要登录，或登录已失效",
        Some("permission_denied") => "没有权限访问这条内容",
        Some("rate_limited") => "站点限流，稍后再试",
        Some("storage_full") => "磁盘空间不足",
        Some("capability_lost") => "站点暂时不允许获取，需要重新登录",
        Some("source_unavailable") => "内容暂时无法获取",
        Some("transient_network") => "网络问题",
        Some("cancelled") => "已取消",
        _ => "没有取回内容",
    }
}

/// What one entry of the request holds now.
struct Outcome {
    position: i64,
    entry: Option<String>,
    progress: String,
    stopped: Option<String>,
}

fn outcomes(
    c: &rusqlite::Connection,
    id: &str,
    entries_of: &[(i64, Option<String>)],
) -> Result<Vec<Outcome>, CoreError> {
    let mut out = Vec::new();
    for (position, entry) in entries_of {
        let (progress, stopped) = match entry {
            Some(entry) => (
                entries::states(c, entry)?.1,
                repo::stopped_class(c, id, entry)?,
            ),
            None => ("undiscovered".into(), None),
        };
        out.push(Outcome {
            position: *position,
            entry: entry.clone(),
            progress,
            stopped,
        });
    }
    Ok(out)
}

/// The state of an entry before any copy is considered.
fn saved_state(o: &Outcome) -> (&'static str, Option<String>) {
    match o.progress.as_str() {
        "complete" => ("ready", None),
        "partial" | "deferred" => (
            "partial",
            Some(match &o.stopped {
                Some(class) => format!("部分内容没有保存：{}", stopped_message(Some(class))),
                None => "部分内容没有保存".into(),
            }),
        ),
        _ => (
            "failed",
            Some(stopped_message(o.stopped.as_deref()).to_owned()),
        ),
    }
}

struct ItemResult {
    position: i64,
    state: &'static str,
    message: Option<String>,
    files: i64,
}

/// Combines what was saved with what the folder export reported.
fn settle(
    o: &Outcome,
    folder: bool,
    folder_error: Option<&str>,
    exported: &HashMap<String, trace_contracts::download::ExportItem>,
) -> ItemResult {
    let (state, message) = saved_state(o);
    let done = |state, message, files| ItemResult {
        position: o.position,
        state,
        message,
        files,
    };
    if state == "failed" || !folder {
        return done(state, message, 0);
    }
    if let Some(why) = folder_error {
        return done("failed", Some(why.to_owned()), 0);
    }
    match o.entry.as_ref().and_then(|e| exported.get(e)) {
        Some(item) if item.state == "failed" || item.state == "nothing" => {
            done("failed", item.message.clone().or(message), 0)
        }
        Some(item) => done(
            if state == "ready" { "exported" } else { state },
            message,
            item.files.len() as i64,
        ),
        None => done(state, message, 0),
    }
}

/// Writes the copies of a settled request and records what became of each entry.
pub async fn run(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let id = job.payload["deliver"]
        .as_str()
        .ok_or_else(|| CoreError::InvalidInput("payload".into()))?
        .to_owned();
    let read_id = id.clone();
    let (delivery, items) = core
        .db
        .read(move |c| Ok((repo::load(c, &read_id)?, repo::items(c, &read_id)?)))
        .await?;
    let Some(delivery) = delivery.filter(|d| d.state == "finishing") else {
        return Ok(());
    };
    let target: DownloadTarget = serde_json::from_str(&delivery.destination)?;
    let entries_of: Vec<_> = items
        .iter()
        .map(|i| (i.position, i.entry_id.clone()))
        .collect();
    let facts_id = id.clone();
    let found = core
        .db
        .read(move |c| outcomes(c, &facts_id, &entries_of))
        .await?;
    // Anything saved is worth copying, including the part of an entry that did not finish.
    let copyable: Vec<String> = found
        .iter()
        .filter(|o| matches!(o.progress.as_str(), "complete" | "partial" | "deferred"))
        .filter_map(|o| o.entry.clone())
        .collect();
    let folder = target.kind == "folder";
    let mut exported = HashMap::new();
    let mut folder_error = None;
    if folder && !copyable.is_empty() {
        match super::export_entries(core, &copyable, target.dir.as_deref()).await {
            Ok(report) => {
                for item in report.items {
                    exported.insert(item.entry_id.clone(), item);
                }
            }
            Err(e) => folder_error = Some(super::user_message(&e)),
        }
    }
    let results: Vec<ItemResult> = found
        .iter()
        .map(|o| settle(o, folder, folder_error.as_deref(), &exported))
        .collect();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            for r in &results {
                repo::set_item(tx, &id, r.position, r.state, r.message.as_deref(), r.files)?;
            }
            repo::set_done(tx, &id, &crate::jobs::now())
        })
        .await
}

fn shown(source_url: &str) -> String {
    url::Url::parse(source_url)
        .map(|u| format!("{}{}", u.host_str().unwrap_or(""), u.path()))
        .unwrap_or_default()
}

fn view(d: repo::Delivery, items: Vec<repo::Item>) -> DeliveryView {
    DeliveryView {
        source: shown(&d.source_url),
        target: serde_json::from_str(&d.destination).unwrap_or_else(|_| DownloadTarget::library()),
        delivery_id: d.delivery_id,
        kind: d.kind,
        platform: d.platform,
        state: d.state,
        created_at: d.created_at,
        finished_at: d.finished_at,
        items: items
            .into_iter()
            .map(|i| DeliveryItemView {
                native_key: i.native_key,
                native_kind: i.native_kind,
                title: i.title,
                entry_id: i.entry_id,
                state: i.state,
                message: i.message,
                files: i.files,
            })
            .collect(),
    }
}

/// Recent download requests (newest first), or the one asked for.
pub async fn list(core: &Core, args: DeliveryListArgs) -> Result<DeliveryList, CoreError> {
    let limit = i64::from(args.limit.unwrap_or(LIST_DEFAULT).clamp(1, LIST_MAX));
    core.db
        .read(move |c| {
            let deliveries = match &args.delivery_id {
                Some(id) => repo::load(c, id)?.into_iter().collect(),
                None => repo::recent(c, limit)?,
            };
            let mut out = Vec::new();
            for d in deliveries {
                let items = repo::items(c, &d.delivery_id)?;
                out.push(view(d, items));
            }
            Ok(DeliveryList { items: out })
        })
        .await
}

/// Stops a request: unfinished work is cancelled, what is saved stays.
pub async fn cancel(core: &Core, id: String) -> Result<bool, CoreError> {
    core.db.write(move |tx| repo::cancel(tx, &id)).await
}
