//! Videos a subscription left undownloaded, and the work that fetches or re-checks them.
use crate::error::CoreError;
use rusqlite::{Connection, Transaction, params};

/// A media group left undownloaded by the subscription setting.
#[derive(Debug, Clone)]
pub struct Deferred {
    pub revision_id: String,
    pub group: String,
    pub account_id: String,
}

const GROUP: &str = "COALESCE(mi.group_key,mi.source_attachment_id)";

/// Groups of the current revision that are still left undownloaded and have nothing saved.
pub fn deferred_groups(c: &Connection, entry: &str) -> Result<Vec<Deferred>, CoreError> {
    let sql = format!(
        "SELECT DISTINCT mi.revision_id,{GROUP},e.account_id
         FROM media_items mi JOIN entry_revisions r ON r.revision_id=mi.revision_id
         JOIN entries e ON e.entry_id=r.entry_id
         WHERE r.entry_id=?1 AND mi.fetch_state='deferred'
           AND mi.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)
           AND NOT EXISTS(SELECT 1 FROM media_items other JOIN media_variants v ON v.media_id=other.media_id
                          WHERE other.revision_id=mi.revision_id
                            AND COALESCE(other.group_key,other.source_attachment_id)={GROUP})
         ORDER BY mi.revision_id,{GROUP}"
    );
    let rows = c
        .prepare(&sql)?
        .query_map([entry], |r| {
            Ok(Deferred {
                revision_id: r.get(0)?,
                group: r.get(1)?,
                account_id: r.get(2)?,
            })
        })?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// The group is wanted from now on (an explicit request, unlike discovery, never defers).
pub fn mark_wanted(tx: &Transaction<'_>, revision: &str, group: &str) -> Result<usize, CoreError> {
    Ok(tx.execute(
        "UPDATE media_items SET fetch_state='wanted'
         WHERE revision_id=?1 AND COALESCE(group_key,source_attachment_id)=?2 AND fetch_state='deferred'",
        params![revision, group],
    )?)
}

/// Media jobs of the entry that are waiting or running.
pub fn active_media_jobs(c: &Connection, entry: &str) -> Result<i64, CoreError> {
    Ok(c.query_row(
        "SELECT count(*) FROM jobs WHERE job_type='media' AND subject_id=?1
         AND state IN ('queued','leased','running','waiting_retry','waiting_storage','waiting_auth','waiting_plugin','waiting_user')",
        [entry],
        |r| r.get(0),
    )?)
}

/// Media jobs of the entry that stopped with a failure.
pub fn failed_media_jobs(c: &Connection, entry: &str) -> Result<Vec<String>, CoreError> {
    let rows = c
        .prepare("SELECT job_id FROM jobs WHERE job_type='media' AND subject_id=?1 AND state='failed' ORDER BY job_id")?
        .query_map([entry], |r| r.get(0))?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// Entries published in `[from, to)`, newest first; one more than `limit` is returned when
/// there are more, so the caller can tell.
pub fn entries_in_range(
    c: &Connection,
    account: Option<&str>,
    from: Option<&str>,
    to: Option<&str>,
    limit: i64,
) -> Result<Vec<String>, CoreError> {
    let rows = c
        .prepare(
            "SELECT e.entry_id FROM entries e
             WHERE e.lifecycle!='deleted' AND e.native_kind!='live' AND e.published_at IS NOT NULL
               AND (?1 IS NULL OR e.account_id=?1)
               AND (?2 IS NULL OR julianday(e.published_at)>=julianday(?2))
               AND (?3 IS NULL OR julianday(e.published_at)<julianday(?3))
             ORDER BY e.published_at DESC,e.entry_id DESC LIMIT ?4",
        )?
        .query_map(params![account, from, to, limit + 1], |r| r.get(0))?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// Failed observation and media jobs with the entry each belongs to.
pub fn failed_jobs(
    c: &Connection,
    account: Option<&str>,
    limit: i64,
) -> Result<Vec<(String, String)>, CoreError> {
    let rows = c
        .prepare(
            "SELECT j.job_id,j.subject_id FROM jobs j JOIN entries e ON e.entry_id=j.subject_id
             WHERE j.state='failed' AND j.job_type IN ('media','observe') AND e.lifecycle!='deleted'
               AND (?1 IS NULL OR e.account_id=?1)
             ORDER BY j.updated_at DESC,j.job_id LIMIT ?2",
        )?
        .query_map(params![account, limit + 1], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}
