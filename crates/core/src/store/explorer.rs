//! Queries behind the resource explorer: library folders (site → account → year → month),
//! the entries below a folder, an entry's files and the records of a download folder.
//!
//! Ordering and filtering pick their columns from fixed fragments; nothing a caller sends is
//! ever part of the SQL text.
use crate::error::CoreError;
use rusqlite::types::Value as Sql;
use rusqlite::{Connection, OptionalExtension, params, params_from_iter};

/// Entries that count: kept, with at least one stored revision.
const LIVE_ENTRY: &str = "e.lifecycle!='deleted' AND EXISTS(SELECT 1 FROM entry_revisions x WHERE x.entry_id=e.entry_id)";
/// The current revision of `e`.
const CURRENT_REVISION: &str = "JOIN entry_revisions r ON r.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=e.entry_id)";
/// Largest number of accounts, years or months one folder answers with.
pub const FOLDERS_MAX: i64 = 2000;

/// Which entries a library folder holds.
#[derive(Debug, Clone, Default)]
pub struct Scope {
    pub site: Option<String>,
    pub account: Option<String>,
    /// `Some(Some(year))` for a year, `Some(None)` for entries of unknown time.
    pub year: Option<Option<String>>,
    pub month: Option<String>,
}

impl Scope {
    fn clause(&self) -> (String, Vec<Sql>) {
        let mut sql = String::new();
        let mut args = Vec::new();
        let mut push = |text: &str, value: Option<Sql>| {
            sql.push_str(" AND ");
            sql.push_str(text);
            args.extend(value);
        };
        if let Some(site) = &self.site {
            push("e.namespace=?", Some(site.clone().into()));
        }
        if let Some(account) = &self.account {
            push("e.account_id=?", Some(account.clone().into()));
        }
        match &self.year {
            Some(Some(year)) => push("substr(e.published_at,1,4)=?", Some(year.clone().into())),
            Some(None) => push("e.published_at IS NULL", None),
            None => {}
        }
        if let Some(month) = &self.month {
            push("substr(e.published_at,6,2)=?", Some(month.clone().into()));
        }
        (sql, args)
    }
}

pub fn sites(c: &Connection) -> Result<Vec<(String, i64)>, CoreError> {
    let sql = format!(
        "SELECT e.namespace,count(*) FROM entries e WHERE {LIVE_ENTRY} GROUP BY e.namespace ORDER BY e.namespace"
    );
    let rows = c
        .prepare(&sql)?
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

pub struct AccountFolder {
    pub account_id: String,
    pub name: String,
    pub entries: i64,
}

pub fn accounts(c: &Connection, site: &str) -> Result<Vec<AccountFolder>, CoreError> {
    let sql = format!(
        "SELECT a.account_id,COALESCE(NULLIF(a.display_name,''),a.native_account_key),count(*)
         FROM entries e JOIN source_accounts a ON a.account_id=e.account_id
         WHERE e.namespace=?1 AND {LIVE_ENTRY}
         GROUP BY a.account_id ORDER BY lower(COALESCE(NULLIF(a.display_name,''),a.native_account_key)),a.account_id LIMIT ?2"
    );
    let rows = c
        .prepare(&sql)?
        .query_map(params![site, FOLDERS_MAX + 1], |r| {
            Ok(AccountFolder {
                account_id: r.get(0)?,
                name: r.get(1)?,
                entries: r.get(2)?,
            })
        })?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// Years of an account, newest first; `None` stands for entries without a publication time.
pub fn years(c: &Connection, account: &str) -> Result<Vec<(Option<String>, i64)>, CoreError> {
    let sql = format!(
        "SELECT substr(e.published_at,1,4),count(*) FROM entries e WHERE e.account_id=?1 AND {LIVE_ENTRY}
         GROUP BY 1 ORDER BY substr(e.published_at,1,4) IS NULL,substr(e.published_at,1,4) DESC"
    );
    let rows = c
        .prepare(&sql)?
        .query_map([account], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

pub fn months(c: &Connection, account: &str, year: &str) -> Result<Vec<(String, i64)>, CoreError> {
    let sql = format!(
        "SELECT substr(e.published_at,6,2),count(*) FROM entries e
         WHERE e.account_id=?1 AND substr(e.published_at,1,4)=?2 AND {LIVE_ENTRY}
         GROUP BY 1 ORDER BY 1 DESC"
    );
    let rows = c
        .prepare(&sql)?
        .query_map(params![account, year], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// What the list of a folder is narrowed and ordered by.
#[derive(Debug, Clone, Default)]
pub struct EntryQuery {
    pub scope: Scope,
    pub sort: String,
    pub descending: bool,
    pub name: Option<String>,
    pub kind: Option<String>,
    pub state: Option<String>,
    pub offset: i64,
    pub limit: i64,
}

/// The ordering expression of a column and whether entries may lack a value for it (those
/// come last in either direction).
fn order_expression(sort: &str) -> Result<(&'static str, bool), CoreError> {
    Ok(match sort {
        "name" => ("lower(r.title)", false),
        "kind" => ("e.native_kind", false),
        "size" => (
            "(SELECT COALESCE(SUM(b.size),0) FROM blobs b WHERE b.blob_id IN (SELECT v.blob_id FROM media_items mi JOIN media_variants v ON v.media_id=mi.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE mi.revision_id=r.revision_id))",
            false,
        ),
        "duration" => ("sm.max_duration_ms", true),
        "dimensions" => ("(sm.max_width*sm.max_height)", true),
        "published" => ("e.published_at", true),
        "site" => ("e.namespace", false),
        "author" => (
            "lower(COALESCE(NULLIF(a.display_name,''),a.native_account_key))",
            false,
        ),
        _ => return Err(CoreError::InvalidInput("unknown explorer sort".into())),
    })
}

/// Entries that count, read from the entries alone: enough for every ordering and filter that
/// does not look at the current revision, and an order of magnitude cheaper on a large library.
const WITHOUT_REVISION: &str = "FROM entries e JOIN source_accounts a ON a.account_id=e.account_id
         LEFT JOIN entry_meta sm ON sm.entry_id=e.entry_id
         WHERE e.lifecycle!='deleted' AND EXISTS(SELECT 1 FROM entry_revisions x WHERE x.entry_id=e.entry_id)";
/// The same entries with their current revision `r`. It starts from the revisions so that the
/// title index can order the rows, and only asks which revision is current for an entry that has
/// more than one.
const WITH_REVISION: &str = "FROM entry_revisions r JOIN entries e ON e.entry_id=r.entry_id
         JOIN source_accounts a ON a.account_id=e.account_id
         LEFT JOIN entry_meta sm ON sm.entry_id=e.entry_id
         WHERE e.lifecycle!='deleted'
           AND (NOT EXISTS(SELECT 1 FROM entry_revisions y WHERE y.entry_id=r.entry_id AND y.revision_id!=r.revision_id)
                OR r.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=r.entry_id))";

/// Entry ids of one page of a folder, and how many entries the folder holds in all.
pub fn entry_page(c: &Connection, q: &EntryQuery) -> Result<(Vec<String>, i64), CoreError> {
    let (order, nullable) = order_expression(&q.sort)?;
    let (scope_sql, mut args) = q.scope.clause();
    let mut filter = String::new();
    let mut revision_filter = false;
    if let Some(name) = &q.name {
        filter.push_str(" AND instr(lower(r.title),?)>0");
        args.push(name.to_lowercase().into());
        revision_filter = true;
    }
    if let Some(kind) = &q.kind {
        filter.push_str(
            " AND (EXISTS(SELECT 1 FROM json_each(COALESCE(sm.content_kinds,'[]')) WHERE value=?) OR e.native_kind=?)",
        );
        args.push(kind.clone().into());
        args.push(kind.clone().into());
    }
    if let Some(state) = &q.state {
        filter.push_str(&format!(
            " AND ({})=?",
            crate::store::entries::SAVE_PROGRESS_SQL
        ));
        args.push(state.clone().into());
        revision_filter = true;
    }
    let revision_order = matches!(q.sort.as_str(), "name" | "size");
    // The count only needs the revision when a filter does; the page also when the ordering does.
    let count_from = if revision_filter {
        WITH_REVISION
    } else {
        WITHOUT_REVISION
    };
    let page_from = if revision_filter || revision_order {
        WITH_REVISION
    } else {
        WITHOUT_REVISION
    };
    let total: i64 = c.query_row(
        &format!("SELECT count(*) {count_from}{scope_sql}{filter}"),
        params_from_iter(args.iter()),
        |r| r.get(0),
    )?;
    // An OFFSET walks every row it skips, so the last page of a large folder would cost a full pass.
    // A page nearer the end than the start is read from the other end, in the opposite order, and
    // turned around; the order is total (entry id breaks every tie), so both reads give the same rows.
    let offset = q.offset.max(0);
    let from_end = offset < total && offset > (total - offset - q.limit).max(0);
    let (page_offset, page_limit, descending) = if from_end {
        let rows = q.limit.min(total - offset);
        (total - offset - rows, rows, !q.descending)
    } else {
        (offset, q.limit, q.descending)
    };
    let direction = if descending { "DESC" } else { "ASC" };
    // Entries that lack a value for the ordering come last in either direction of the folder.
    let unknown_last = match (nullable, from_end) {
        (false, _) => String::new(),
        (true, false) => format!("({order}) IS NULL,"),
        (true, true) => format!("({order}) IS NULL DESC,"),
    };
    let tie = if page_from == WITH_REVISION {
        "r.entry_id"
    } else {
        "e.entry_id"
    };
    let sql = format!(
        "SELECT e.entry_id {page_from}{scope_sql}{filter} ORDER BY {unknown_last}{order} {direction},{tie} {direction} LIMIT ? OFFSET ?"
    );
    args.push(page_limit.into());
    args.push(page_offset.into());
    let mut ids: Vec<String> = c
        .prepare(&sql)?
        .query_map(params_from_iter(args.iter()), |r| r.get(0))?
        .collect::<Result<_, _>>()?;
    if from_end {
        ids.reverse();
    }
    Ok((ids, total))
}

pub struct EntryRow {
    pub entry_id: String,
    pub namespace: String,
    pub native_kind: String,
    pub published_at: Option<String>,
    pub title: String,
    pub author: Option<String>,
    pub content_kinds: Vec<String>,
    pub duration_ms: Option<i64>,
    pub width: Option<i64>,
    pub height: Option<i64>,
    pub size: i64,
    pub state: String,
    pub cover_blob: Option<String>,
}

pub fn entry_row(c: &Connection, entry_id: &str) -> Result<Option<EntryRow>, CoreError> {
    let sql = format!(
        "SELECT e.entry_id,e.namespace,e.native_kind,e.published_at,r.title,
           COALESCE(NULLIF(a.display_name,''),a.native_account_key),
           COALESCE(sm.content_kinds,'[]'),sm.max_duration_ms,sm.max_width,sm.max_height,
           (SELECT COALESCE(SUM(b.size),0) FROM blobs b WHERE b.blob_id IN (SELECT v.blob_id FROM media_items mi JOIN media_variants v ON v.media_id=mi.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE mi.revision_id=r.revision_id)),
           {progress},
           (SELECT v.blob_id FROM media_items mi JOIN media_variants v ON v.media_id=mi.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE mi.revision_id=r.revision_id AND mi.role='cover' LIMIT 1)
         FROM entries e JOIN source_accounts a ON a.account_id=e.account_id {CURRENT_REVISION}
         LEFT JOIN entry_meta sm ON sm.entry_id=e.entry_id
         WHERE e.entry_id=?1 AND e.lifecycle!='deleted'",
        progress = crate::store::entries::SAVE_PROGRESS_SQL
    );
    let row = c
        .query_row(&sql, [entry_id], |r| {
            let kinds: String = r.get(6)?;
            Ok(EntryRow {
                entry_id: r.get(0)?,
                namespace: r.get(1)?,
                native_kind: r.get(2)?,
                published_at: r.get(3)?,
                title: r.get(4)?,
                author: r.get(5)?,
                content_kinds: serde_json::from_str(&kinds).unwrap_or_default(),
                duration_ms: r.get(7)?,
                width: r.get(8)?,
                height: r.get(9)?,
                size: r.get(10)?,
                state: r.get(11)?,
                cover_blob: r.get(12)?,
            })
        })
        .optional()?;
    Ok(row)
}

/// One stored resource of an entry's current revision.
pub struct FileEntry {
    pub media_id: String,
    pub role: String,
    pub position: i64,
    pub title: Option<String>,
    pub deferred: bool,
    pub blob_id: Option<String>,
    pub size: Option<i64>,
    pub mime: Option<String>,
    pub measured_spec: Option<String>,
}

/// The files of an entry in source order, each with its newest verified object.
pub fn entry_files(c: &Connection, entry_id: &str) -> Result<Vec<FileEntry>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT mi.media_id,mi.role,mi.position,mi.title,mi.fetch_state='deferred',
           v.blob_id,b.size,b.mime,v.measured_spec
         FROM media_items mi
         LEFT JOIN media_variants v ON v.variant_id=(SELECT v2.variant_id FROM media_variants v2 JOIN replicas rp ON rp.blob_id=v2.blob_id AND rp.state='verified' WHERE v2.media_id=mi.media_id ORDER BY v2.created_at DESC,v2.variant_id DESC LIMIT 1)
         LEFT JOIN blobs b ON b.blob_id=v.blob_id
         WHERE mi.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)
         ORDER BY mi.position,mi.media_id LIMIT 2000",
    )?;
    let rows = stmt
        .query_map([entry_id], |r| {
            Ok(FileEntry {
                media_id: r.get(0)?,
                role: r.get(1)?,
                position: r.get(2)?,
                title: r.get(3)?,
                deferred: r.get(4)?,
                blob_id: r.get(5)?,
                size: r.get(6)?,
                mime: r.get(7)?,
                measured_spec: r.get(8)?,
            })
        })?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// A recorded download file with what the archive knows about its entry.
pub struct CopyRow {
    pub entry_id: Option<String>,
    pub rel_path: String,
    pub role: String,
    pub size: i64,
    pub state: String,
    pub published_at: Option<String>,
    pub site: Option<String>,
    pub author: Option<String>,
    pub duration_ms: Option<i64>,
    pub width: Option<i64>,
    pub height: Option<i64>,
}

/// Recorded copies that sit directly in the directory `prefix` of a download folder (the
/// directory path followed by `\`, or empty for the folder itself).
pub fn copies_below(
    c: &Connection,
    location_id: &str,
    prefix: &str,
) -> Result<Vec<CopyRow>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT k.entry_id,k.rel_path,k.role,k.size,k.state,e.published_at,e.namespace,
           COALESCE(NULLIF(a.display_name,''),a.native_account_key),
           json_extract(v.measured_spec,'$.duration_ms'),json_extract(v.measured_spec,'$.width'),json_extract(v.measured_spec,'$.height')
         FROM external_copies k
         LEFT JOIN entries e ON e.entry_id=k.entry_id
         LEFT JOIN source_accounts a ON a.account_id=e.account_id
         LEFT JOIN media_variants v ON v.variant_id=(SELECT v2.variant_id FROM media_variants v2 WHERE v2.media_id=k.media_id ORDER BY v2.created_at DESC,v2.variant_id DESC LIMIT 1)
         WHERE k.location_id=?1 AND substr(k.rel_path,1,length(?2))=?2
           AND instr(substr(k.rel_path,length(?2)+1),char(92))=0 LIMIT 100000",
    )?;
    let rows = stmt
        .query_map(params![location_id, prefix], |r| {
            Ok(CopyRow {
                entry_id: r.get(0)?,
                rel_path: r.get(1)?,
                role: r.get(2)?,
                size: r.get(3)?,
                state: r.get(4)?,
                published_at: r.get(5)?,
                site: r.get(6)?,
                author: r.get(7)?,
                duration_ms: r.get(8)?,
                width: r.get(9)?,
                height: r.get(10)?,
            })
        })?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// Whether a recorded copy sits at exactly this path of the folder.
pub fn has_copy(c: &Connection, location_id: &str, rel_path: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM external_copies WHERE location_id=?1 AND rel_path=?2)",
        params![location_id, rel_path],
        |r| r.get(0),
    )?)
}

/// Whether the account exists on the site (a made-up tree address is refused).
pub fn account_of_site(
    c: &Connection,
    site: &str,
    account: &str,
) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT COALESCE(NULLIF(display_name,''),native_account_key) FROM source_accounts WHERE account_id=?1 AND namespace=?2",
        params![account, site],
        |r| r.get(0),
    )
    .optional()?)
}
