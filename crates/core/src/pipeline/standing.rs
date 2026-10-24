//! Where an observed entry stands against the range of the subscription that found it
//! (multi-site contracts §5.3).
//!
//! A list that gave no date could not be held against the range, so the entry was kept and
//! observed. Its details carry the exact date: an entry outside the range is not downloaded
//! and is no longer one of the subscription's discoveries, and one the details cannot date
//! keeps an unknown publish time and gets no automatic video download. Explicit requests
//! (a link, a preview selection, an authentication completion) are never held to a range.
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::scan_range::{Placement, Range, stored_margin};
use crate::store;
use rusqlite::Transaction;
use serde_json::Value;

/// The subscription this observation works for, unless somebody asked for the content.
fn subscription_of(job: &ClaimedJob) -> Option<&str> {
    let asked = job.payload["source_request_id"].is_string()
        || job.payload["delivery_id"].is_string()
        || job.payload["completion"] == true;
    job.payload["subscription_id"].as_str().filter(|_| !asked)
}

/// The stored date of the entry and how far it may lie from the true time, when it came from
/// the source: an approximate one is a guess and places nothing.
fn exact_date(tx: &Transaction<'_>, entry: &str) -> Option<(String, i64)> {
    let (date, precision): (Option<String>, Option<String>) = tx
        .query_row(
            "SELECT published_at, published_precision FROM entries WHERE entry_id=?1",
            [entry],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .ok()?;
    Some((date?, stored_margin(precision.as_deref())?))
}

/// Places the entry for the subscription this observation works for; `None` when no range
/// applies (an explicit request, a live subscription, or a range that cannot be read).
/// An entry placed outside stops being one of the subscription's discoveries right here.
pub(crate) fn settle(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    entry: &str,
) -> Result<Option<Placement>, CoreError> {
    let Some(subscription) = subscription_of(job) else {
        return Ok(None);
    };
    let Ok(target) = store::scan::target(tx, subscription) else {
        return Ok(None);
    };
    if target.content_type == "live" {
        return Ok(None);
    }
    let Ok(value) = serde_json::from_str::<Value>(&target.range_json) else {
        return Ok(None);
    };
    let Ok(range) = Range::parse(&value, &target.created_at) else {
        return Ok(None);
    };
    let placement = match exact_date(tx, entry) {
        Some((date, margin)) => range.place(Some(&date), margin),
        None => range.place(None, 0),
    };
    if placement == Placement::Outside {
        tx.execute(
            "DELETE FROM entry_subscriptions WHERE entry_id=?1 AND subscription_id=?2",
            rusqlite::params![entry, subscription],
        )?;
    }
    Ok(Some(placement))
}

/// An entry outside the range keeps its media undownloaded when no other subscription found
/// it: nothing is fetched on its own, and the user can still sync it by hand. Media a job
/// already holds, and bytes already saved, are left as they are.
pub(crate) fn leave_undownloaded(
    tx: &Transaction<'_>,
    entry: &str,
    revision: &str,
) -> Result<(), CoreError> {
    let found_elsewhere: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM entry_subscriptions WHERE entry_id=?1)",
        [entry],
        |r| r.get(0),
    )?;
    if found_elsewhere {
        return Ok(());
    }
    tx.execute(
        "UPDATE media_items SET fetch_state='deferred'
         WHERE revision_id=?1 AND fetch_state='wanted'
           AND NOT EXISTS(SELECT 1 FROM media_variants v WHERE v.media_id=media_items.media_id)
           AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.job_key='media:'||media_items.revision_id||':'||COALESCE(media_items.group_key,media_items.source_attachment_id))",
        [revision],
    )?;
    Ok(())
}
