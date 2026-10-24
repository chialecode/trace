//! The library side of the explorer: site → account → year → month folders, the entries
//! below a folder and the files of one entry.
use super::node::Node;
use super::{Filters, LIMIT_MAX, Sorting, kind_of_role, sort_rows};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use crate::store::explorer::{EntryQuery, EntryRow, FOLDERS_MAX, FileEntry, Scope};
use trace_contracts::explorer::{ExplorerNode, ExplorerRow, ListArgs, ListView, TreeView};

fn site_title(site: &str) -> String {
    trace_domain::sites::site(site).map_or_else(|| site.to_owned(), |s| s.title.to_owned())
}

fn folder(
    node: Node,
    name: String,
    kind: &str,
    count: Option<i64>,
    expandable: bool,
) -> ExplorerNode {
    ExplorerNode {
        node: node.text(),
        name,
        kind: kind.into(),
        count,
        expandable,
    }
}

pub(super) fn roots() -> Vec<ExplorerNode> {
    vec![
        folder(Node::Library, "资源库".into(), "library", None, true),
        folder(Node::Downloads, "下载位置".into(), "downloads", None, true),
    ]
}

/// The child folders of a library node; entries and months have none.
pub(super) async fn tree(core: &Core, node: Node) -> Result<TreeView, CoreError> {
    let nodes = core
        .db
        .read(move |c| -> Result<(Vec<ExplorerNode>, bool), CoreError> {
            Ok(match &node {
                Node::Library => (
                    store::explorer::sites(c)?
                        .into_iter()
                        .map(|(site, n)| {
                            folder(
                                Node::Site(site.clone()),
                                site_title(&site),
                                "site",
                                Some(n),
                                true,
                            )
                        })
                        .collect(),
                    false,
                ),
                Node::Site(site) => {
                    let mut accounts = store::explorer::accounts(c, site)?;
                    let truncated = accounts.len() as i64 > FOLDERS_MAX;
                    accounts.truncate(FOLDERS_MAX as usize);
                    (
                        accounts
                            .into_iter()
                            .map(|a| {
                                let node = Node::Account {
                                    site: site.clone(),
                                    account: a.account_id,
                                };
                                folder(node, a.name, "account", Some(a.entries), true)
                            })
                            .collect(),
                        truncated,
                    )
                }
                Node::Account { site, account } => (
                    store::explorer::years(c, account)?
                        .into_iter()
                        .map(|(year, n)| {
                            let name = year
                                .as_deref()
                                .map_or_else(|| "未知时间".to_owned(), |y| format!("{y} 年"));
                            let expandable = year.is_some();
                            let node = Node::Year {
                                site: site.clone(),
                                account: account.clone(),
                                year,
                            };
                            folder(node, name, "year", Some(n), expandable)
                        })
                        .collect(),
                    false,
                ),
                Node::Year {
                    site,
                    account,
                    year: Some(year),
                } => (
                    store::explorer::months(c, account, year)?
                        .into_iter()
                        .map(|(month, n)| {
                            let name = format!("{} 月", month.trim_start_matches('0'));
                            let node = Node::Month {
                                site: site.clone(),
                                account: account.clone(),
                                year: year.clone(),
                                month,
                            };
                            folder(node, name, "month", Some(n), false)
                        })
                        .collect(),
                    false,
                ),
                _ => (Vec::new(), false),
            })
        })
        .await?;
    Ok(TreeView {
        nodes: nodes.0,
        truncated: nodes.1,
    })
}

fn scope_of(node: &Node) -> Scope {
    match node {
        Node::Site(site) => Scope {
            site: Some(site.clone()),
            ..Scope::default()
        },
        Node::Account { site, account } => Scope {
            site: Some(site.clone()),
            account: Some(account.clone()),
            ..Scope::default()
        },
        Node::Year {
            site,
            account,
            year,
        } => Scope {
            site: Some(site.clone()),
            account: Some(account.clone()),
            year: Some(year.clone()),
            month: None,
        },
        Node::Month {
            site,
            account,
            year,
            month,
        } => Scope {
            site: Some(site.clone()),
            account: Some(account.clone()),
            year: Some(Some(year.clone())),
            month: Some(month.clone()),
        },
        _ => Scope::default(),
    }
}

/// The folder's address as a person reads it.
async fn path_of(core: &Core, node: &Node) -> Result<String, CoreError> {
    let node = node.clone();
    core.db
        .read(move |c| -> Result<String, CoreError> {
            let mut parts = vec!["资源库".to_owned()];
            match &node {
                Node::Library => {}
                Node::Site(site) => parts.push(site_title(site)),
                Node::Account { site, account } => {
                    parts.push(site_title(site));
                    parts.push(
                        store::explorer::account_of_site(c, site, account)?.unwrap_or_default(),
                    );
                }
                Node::Year {
                    site,
                    account,
                    year,
                } => {
                    parts.push(site_title(site));
                    parts.push(
                        store::explorer::account_of_site(c, site, account)?.unwrap_or_default(),
                    );
                    parts.push(year.clone().unwrap_or_else(|| "未知时间".into()));
                }
                Node::Month {
                    site,
                    account,
                    year,
                    month,
                } => {
                    parts.push(site_title(site));
                    parts.push(
                        store::explorer::account_of_site(c, site, account)?.unwrap_or_default(),
                    );
                    parts.push(year.clone());
                    parts.push(month.clone());
                }
                _ => {}
            }
            Ok(parts.join("\\"))
        })
        .await
}

/// Entries below a folder, one page.
pub(super) async fn entries(
    core: &Core,
    node: &Node,
    args: &ListArgs,
    sorting: &Sorting,
    filters: &Filters,
) -> Result<ListView, CoreError> {
    let path = path_of(core, node).await?;
    let query = EntryQuery {
        scope: scope_of(node),
        sort: sorting.column.clone(),
        descending: args.descending,
        name: filters.name.clone(),
        kind: filters.kind.clone(),
        state: filters.state.clone(),
        offset: sorting.offset,
        limit: sorting.limit,
    };
    let prefix = path.clone();
    let (rows, total) = core
        .db
        .read(move |c| -> Result<(Vec<ExplorerRow>, i64), CoreError> {
            let (ids, total) = store::explorer::entry_page(c, &query)?;
            let mut rows = Vec::with_capacity(ids.len());
            for id in ids {
                if let Some(row) = store::explorer::entry_row(c, &id)? {
                    rows.push(entry_row(&prefix, row));
                }
            }
            Ok((rows, total))
        })
        .await?;
    Ok(ListView {
        path,
        rows,
        total,
        offset: sorting.offset,
        truncated: false,
    })
}

const KIND_ORDER: [&str; 6] = ["video", "image", "audio", "article", "live", "text"];

/// The one kind a row is listed and filtered under.
fn entry_kind(kinds: &[String], native: &str) -> String {
    for kind in KIND_ORDER {
        if kinds
            .iter()
            .any(|k| k == kind || (kind == "image" && k == "live_photo"))
        {
            return kind.to_owned();
        }
    }
    match native {
        "video" | "short" | "reel" | "clip" => "video",
        "image" | "photo" | "opus" => "image",
        "article" => "article",
        "live" => "live",
        "post" | "dynamic" | "tweet" | "note" | "text" => "text",
        _ => "other",
    }
    .to_owned()
}

fn entry_row(parent: &str, e: EntryRow) -> ExplorerRow {
    let name = if e.title.trim().is_empty() {
        "（无标题）".to_owned()
    } else {
        e.title.clone()
    };
    ExplorerRow {
        key: format!("entry:{}", e.entry_id),
        kind: entry_kind(&e.content_kinds, &e.native_kind),
        kinds: e.content_kinds,
        size: (e.size > 0).then_some(e.size),
        duration_ms: e.duration_ms,
        width: e.width,
        height: e.height,
        published_at: e.published_at,
        site: Some(site_title(&e.namespace)),
        author: e.author,
        state: e.state,
        path: format!("{parent}\\{name}"),
        node: Some(Node::Entry(e.entry_id.clone()).text()),
        entry_id: Some(e.entry_id),
        blob_id: e.cover_blob,
        location_id: None,
        rel_path: None,
        name,
        can_open: false,
        can_reveal: false,
        can_sync: false,
        can_forget: false,
    }
}

fn extension_of(mime: &str) -> Option<&'static str> {
    crate::app::media::external_extension(mime).ok()
}

fn role_label(role: &str) -> &'static str {
    match role {
        "video" => "视频",
        "audio" | "music" => "音频",
        "image" => "图片",
        "cover" => "封面",
        "motion" => "动态片段",
        "subtitle" => "字幕",
        _ => "文件",
    }
}

fn file_row(parent: &str, entry: &EntryRow, f: FileEntry) -> ExplorerRow {
    let extension = f.mime.as_deref().and_then(extension_of);
    let base = f
        .title
        .as_deref()
        .map(str::trim)
        .filter(|t| !t.is_empty())
        .map_or_else(
            || format!("{} {:02}", role_label(&f.role), f.position + 1),
            str::to_owned,
        );
    let name = match extension {
        Some(ext) if !base.to_ascii_lowercase().ends_with(&format!(".{ext}")) => {
            format!("{base}.{ext}")
        }
        _ => base,
    };
    let spec: serde_json::Value = f
        .measured_spec
        .as_deref()
        .and_then(|s| serde_json::from_str(s).ok())
        .unwrap_or_default();
    let state = if f.blob_id.is_some() {
        "saved"
    } else if f.deferred {
        "deferred"
    } else {
        "missing"
    };
    ExplorerRow {
        key: format!("file:{}", f.media_id),
        kind: kind_of_role(&f.role).into(),
        kinds: Vec::new(),
        size: f.size,
        duration_ms: spec["duration_ms"].as_i64(),
        width: spec["width"].as_i64(),
        height: spec["height"].as_i64(),
        published_at: entry.published_at.clone(),
        site: Some(site_title(&entry.namespace)),
        author: entry.author.clone(),
        state: state.into(),
        entry_id: Some(entry.entry_id.clone()),
        can_open: f.blob_id.is_some() && extension.is_some(),
        blob_id: f.blob_id,
        location_id: None,
        rel_path: None,
        path: format!("{parent}\\{name}"),
        node: None,
        name,
        can_reveal: false,
        can_sync: false,
        can_forget: false,
    }
}

/// The files of one entry, in source order or as sorted.
pub(super) async fn files(
    core: &Core,
    entry_id: &str,
    args: &ListArgs,
    sorting: &Sorting,
    filters: &Filters,
) -> Result<ListView, CoreError> {
    let id = entry_id.to_owned();
    let (entry, files) = core
        .db
        .read(
            move |c| -> Result<(Option<EntryRow>, Vec<FileEntry>), CoreError> {
                Ok((
                    store::explorer::entry_row(c, &id)?,
                    store::explorer::entry_files(c, &id)?,
                ))
            },
        )
        .await?;
    let entry = entry.ok_or_else(|| CoreError::NotFound("entry".into()))?;
    let title = if entry.title.trim().is_empty() {
        "（无标题）".to_owned()
    } else {
        entry.title.clone()
    };
    let parent = format!("资源库\\{}\\{}", site_title(&entry.namespace), title);
    let mut rows: Vec<ExplorerRow> = files
        .into_iter()
        .map(|f| file_row(&parent, &entry, f))
        .collect();
    rows.retain(|r| filters.accepts(r));
    // Source order is the default; an explicit column sorts the few rows of one entry.
    if args.sort.is_some() {
        sort_rows(&mut rows, &sorting.column, args.descending);
    }
    let total = rows.len() as i64;
    let rows = rows
        .into_iter()
        .skip(sorting.offset.max(0) as usize)
        .take(sorting.limit.clamp(1, LIMIT_MAX) as usize)
        .collect();
    Ok(ListView {
        path: parent,
        rows,
        total,
        offset: sorting.offset,
        truncated: false,
    })
}
