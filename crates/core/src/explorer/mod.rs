//! Resource explorer (docs/design/multi-site-contracts.md §8.1): a folder tree and a file list
//! over the library (site → account → year → month → entry → file) and over the download
//! folders (their real directories, with TRACE's records on the files it wrote).
//!
//! Both sides page, sort and filter on the Core side; nothing loads a whole archive or a whole
//! directory into the interface. Every address and every name is treated as untrusted input.
mod folders;
mod library;
pub mod node;

#[cfg(test)]
#[path = "explorer_tests.rs"]
mod explorer_tests;

pub use folders::{open, reveal};

use crate::core::Core;
use crate::error::CoreError;
use node::Node;
use trace_contracts::explorer::{ExplorerRow, ListArgs, ListView, TreeArgs, TreeView};

pub(crate) const DEFAULT_LIMIT: i64 = 100;
pub(crate) const LIMIT_MAX: i64 = 200;
/// Longest filter text; longer input is refused.
const QUERY_MAX: usize = 200;

const SORTS: [&str; 8] = [
    "name",
    "kind",
    "size",
    "duration",
    "dimensions",
    "published",
    "site",
    "author",
];
const KINDS: [&str; 8] = [
    "folder", "video", "image", "audio", "text", "article", "live", "other",
];
const LIBRARY_STATES: [&str; 9] = [
    "complete", "partial", "pending", "deferred", "unknown", "saved", "missing", "present",
    "changed",
];
const DOWNLOAD_STATES: [&str; 5] = ["present", "changed", "missing", "untracked", "writing"];

/// The column, window and direction a list is read with.
pub(crate) struct Sorting {
    pub column: String,
    pub offset: i64,
    pub limit: i64,
}

/// What a list is narrowed to, already checked.
pub(crate) struct Filters {
    pub name: Option<String>,
    pub kind: Option<String>,
    pub state: Option<String>,
}

impl Filters {
    fn from_args(args: &ListArgs, downloads: bool) -> Result<Filters, CoreError> {
        let name = args
            .query
            .as_deref()
            .map(str::trim)
            .filter(|q| !q.is_empty())
            .map(str::to_lowercase);
        if name.as_ref().is_some_and(|q| q.chars().count() > QUERY_MAX) {
            return Err(CoreError::InvalidInput("explorer filter budget".into()));
        }
        let kind = args.kind.clone().filter(|k| !k.is_empty());
        if kind.as_deref().is_some_and(|k| !KINDS.contains(&k)) {
            return Err(CoreError::InvalidInput("unknown explorer kind".into()));
        }
        let state = args.state.clone().filter(|s| !s.is_empty());
        let allowed: &[&str] = if downloads {
            &DOWNLOAD_STATES
        } else {
            &LIBRARY_STATES
        };
        if state.as_deref().is_some_and(|s| !allowed.contains(&s)) {
            return Err(CoreError::InvalidInput("unknown explorer state".into()));
        }
        Ok(Filters { name, kind, state })
    }

    /// The same filters applied to rows that are already in memory.
    pub(crate) fn accepts(&self, row: &ExplorerRow) -> bool {
        self.name
            .as_deref()
            .is_none_or(|q| row.name.to_lowercase().contains(q))
            && self
                .kind
                .as_deref()
                .is_none_or(|k| row.kind == k || row.kinds.iter().any(|x| x == k))
            && self.state.as_deref().is_none_or(|s| row.state == s)
    }
}

impl Sorting {
    fn from_args(args: &ListArgs, default: &str) -> Result<Sorting, CoreError> {
        let column = args.sort.clone().unwrap_or_else(|| default.to_owned());
        if !SORTS.contains(&column.as_str()) {
            return Err(CoreError::InvalidInput("unknown explorer sort".into()));
        }
        Ok(Sorting {
            column,
            offset: args.offset.unwrap_or(0).max(0),
            limit: args.limit.unwrap_or(DEFAULT_LIMIT).clamp(1, LIMIT_MAX),
        })
    }
}

/// The kind a media role is listed under.
pub(crate) fn kind_of_role(role: &str) -> &'static str {
    match role {
        "video" | "motion" => "video",
        "image" | "cover" => "image",
        "audio" | "music" => "audio",
        "subtitle" | "text" => "text",
        _ => "other",
    }
}

/// The kind a file name is listed under.
pub(crate) fn kind_of_extension(extension: &str) -> &'static str {
    match extension {
        "mp4" | "m4v" | "mov" | "webm" | "mkv" | "flv" | "ts" | "avi" => "video",
        "jpg" | "jpeg" | "png" | "webp" | "gif" | "bmp" | "heic" => "image",
        "m4a" | "mp3" | "aac" | "flac" | "wav" | "ogg" | "opus" => "audio",
        "txt" | "md" | "json" | "srt" | "vtt" | "ass" => "text",
        _ => "other",
    }
}

fn dimensions(row: &ExplorerRow) -> Option<i64> {
    row.width.zip(row.height).map(|(w, h)| w * h)
}

/// Sorts rows by a column; folders stay first and rows without a value stay last, in either
/// direction. The order of equal rows is the order they came in.
pub(crate) fn sort_rows(rows: &mut [ExplorerRow], column: &str, descending: bool) {
    use std::cmp::Ordering;
    fn by<T: Ord>(a: Option<T>, b: Option<T>, descending: bool) -> Ordering {
        match (a, b) {
            (None, None) => Ordering::Equal,
            (None, Some(_)) => Ordering::Greater,
            (Some(_), None) => Ordering::Less,
            (Some(a), Some(b)) if descending => b.cmp(&a),
            (Some(a), Some(b)) => a.cmp(&b),
        }
    }
    let lower = |s: &Option<String>| s.as_ref().map(|s| s.to_lowercase());
    rows.sort_by(|a, b| {
        let (folder_a, folder_b) = (a.kind == "folder", b.kind == "folder");
        if folder_a != folder_b {
            return folder_b.cmp(&folder_a);
        }
        match column {
            "kind" => by(Some(&a.kind), Some(&b.kind), descending),
            "size" => by(a.size, b.size, descending),
            "duration" => by(a.duration_ms, b.duration_ms, descending),
            "dimensions" => by(dimensions(a), dimensions(b), descending),
            "published" => by(a.published_at.as_ref(), b.published_at.as_ref(), descending),
            "site" => by(lower(&a.site), lower(&b.site), descending),
            "author" => by(lower(&a.author), lower(&b.author), descending),
            _ => by(
                Some(a.name.to_lowercase()),
                Some(b.name.to_lowercase()),
                descending,
            ),
        }
    });
}

/// The child folders of a node; the two roots when no node is given.
pub async fn tree(core: &Core, args: TreeArgs) -> Result<TreeView, CoreError> {
    let Some(text) = args.node else {
        return Ok(TreeView {
            nodes: library::roots(),
            truncated: false,
        });
    };
    match Node::parse(&text)? {
        Node::Downloads => folders::tree_downloads(core).await,
        Node::Folder { location, rel } => folders::tree_folder(core, &location, &rel).await,
        node => library::tree(core, node).await,
    }
}

/// One page of the files of a node: entries below a library folder, the files of an entry,
/// or the contents of a download folder.
pub async fn list(core: &Core, args: ListArgs) -> Result<ListView, CoreError> {
    let node = Node::parse(&args.node)?;
    let downloads = matches!(node, Node::Downloads | Node::Folder { .. });
    let filters = Filters::from_args(&args, downloads)?;
    match &node {
        Node::Downloads => folders::list_downloads(core).await,
        Node::Folder { location, rel } => {
            let sorting = Sorting::from_args(&args, "name")?;
            folders::list_folder(core, location, rel, &args, &sorting, &filters).await
        }
        Node::Entry(entry) => {
            let sorting = Sorting::from_args(&args, "name")?;
            library::files(core, entry, &args, &sorting, &filters).await
        }
        _ => {
            // Newest first until a column is chosen.
            let mut args = args;
            let sorting = Sorting::from_args(&args, "published")?;
            if args.sort.is_none() {
                args.descending = true;
            }
            library::entries(core, &node, &args, &sorting, &filters).await
        }
    }
}
