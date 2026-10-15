//! Subscription range selection is independent of a site's pagination order.
use crate::error::CoreError;
use serde_json::Value;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
/// The page transaction records next-cursor fingerprints across scheduler turns
/// and restarts. A rejected cycle cannot commit entries or advance coverage.
pub fn record_cursor(
    tx: &rusqlite::Transaction<'_>,
    sub: &str,
    lane: &str,
    next: Option<&str>,
) -> Result<(), CoreError> {
    if let Some(cursor) = next {
        use sha2::Digest;
        let digest: String = sha2::Sha256::digest(cursor.as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        let count: i64 = tx.query_row(
            "SELECT count(*) FROM scan_cursor_visits WHERE subscription_id=?1 AND scan_type=?2",
            rusqlite::params![sub, lane],
            |r| r.get(0),
        )?;
        if count >= 100_000 {
            return Err(CoreError::InvalidInput(
                "scan cursor budget exhausted; coverage retained".into(),
            ));
        }
        if tx.execute(
            "INSERT OR IGNORE INTO scan_cursor_visits VALUES(?1,?2,?3)",
            rusqlite::params![sub, lane, digest],
        )? == 0
        {
            return Err(CoreError::InvalidInput(
                "persistent cursor cycle; coverage retained".into(),
            ));
        }
    } else {
        tx.execute(
            "DELETE FROM scan_cursor_visits WHERE subscription_id=?1 AND scan_type=?2",
            rusqlite::params![sub, lane],
        )?;
    }
    Ok(())
}
#[derive(Debug, Clone, Copy)]
pub struct Range {
    pub start: Option<OffsetDateTime>,
    pub end: Option<OffsetDateTime>,
}
/// Recent scans revisit the head on a bounded schedule; deep/history scans have
/// their own durable cursor. Old catalogs gain that lane without enabling any
/// disabled subscription or resurrecting a cancelled job.
pub async fn ensure_deep_lanes(db: &crate::db::DbHandle) -> Result<(), CoreError> {
    db.write(|tx|{
        tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) SELECT 'deep-'||lower(hex(randomblob(16))),'scan:'||s.subscription_id||':deep','scan',s.subscription_id,json_object('subscription_id',s.subscription_id,'mode','deep'),3,'queued',strftime('%Y-%m-%dT%H:%M:%SZ','now','+7 days'),?1,?1 FROM subscriptions s WHERE s.content_type!='live' AND NOT EXISTS(SELECT 1 FROM jobs p WHERE json_extract(p.last_error,'$.class')='permission_denied' AND json_extract(p.payload,'$.subscription_id')=s.subscription_id) AND NOT EXISTS(SELECT 1 FROM job_consumers w WHERE w.subscription_id=s.subscription_id AND w.requested>w.completed AND w.state='waiting_user' AND json_extract(w.last_error,'$.class')='permission_denied')",[crate::jobs::now()])?;
        Ok(())
    }).await
}

pub fn finish_recent_sweep(
    tx: &rusqlite::Transaction<'_>,
    sub: &str,
    continuation: Option<&str>,
    has_more: bool,
    pages: u32,
) -> Result<(), CoreError> {
    if has_more && continuation.is_none() {
        return Err(CoreError::InvalidInput(
            "recent scan continuation missing".into(),
        ));
    }
    if !has_more {
        record_cursor(tx, sub, "incremental", None)?;
    }
    // A per-turn budget yields to other work; it must not strand every page
    // after the head. Retain the same sweep until the source exhausts it.
    tx.execute("UPDATE scan_checkpoints SET cursor=?6,coverage=json_set(coverage,'$.recent_target_days',7,'$.page_budget_reached',?2,'$.pages',?3,'$.unvisited_continuation',?4,'$.history_complete',?5) WHERE subscription_id=?1 AND scan_type='incremental'",rusqlite::params![sub,has_more,pages,continuation,!has_more,if has_more{continuation}else{None}])?;
    Ok(())
}
impl Range {
    pub fn parse(value: &Value, created: &str) -> Result<Self, CoreError> {
        let bad = || CoreError::InvalidInput("invalid subscription range".into());
        let date = |v: &str| OffsetDateTime::parse(v, &Rfc3339).map_err(|_| bad());
        let (start, end) = match value["kind"].as_str().unwrap_or("all") {
            "from_now" => (Some(date(created)?), None),
            "recent_days" => {
                let n = value["days"]
                    .as_i64()
                    .filter(|n| (1..=36500).contains(n))
                    .ok_or_else(bad)?;
                (Some(date(created)? - time::Duration::days(n)), None)
            }
            "date_range" => (
                value["start"].as_str().map(date).transpose()?,
                value["end"].as_str().map(date).transpose()?,
            ),
            "all" => (None, None),
            "resume_cursor" => {
                if value["cursor"]
                    .as_str()
                    .is_none_or(|s| s.is_empty() || s.len() > 4096)
                {
                    return Err(bad());
                }
                (None, None)
            }
            _ => return Err(bad()),
        };
        if start.zip(end).is_some_and(|(s, e)| s > e) {
            return Err(bad());
        }
        Ok(Self { start, end })
    }
    /// Whether a date the list gave can still lie in the range; an unknown date cannot be
    /// excluded here, since the details place the entry later (§5.3).
    pub fn includes(&self, published: Option<&str>) -> bool {
        self.place(published, 0) != Placement::Outside
    }

    /// No bound at all: every entry belongs, dated or not.
    pub fn is_unbounded(&self) -> bool {
        self.start.is_none() && self.end.is_none()
    }

    /// A range that asks for what is recent, with no end: a list that cannot say how old its
    /// entries are is read for a limited number of pages only.
    pub fn follows_the_present(&self) -> bool {
        self.start.is_some() && self.end.is_none()
    }

    /// Where an entry stands: `margin_seconds` is how far the true time may lie from
    /// `published` (zero for an exact date). Only a date that cannot reach the range is
    /// outside; one that straddles an edge, or none, stays unplaced.
    pub fn place(&self, published: Option<&str>, margin_seconds: i64) -> Placement {
        if self.is_unbounded() {
            return Placement::Inside;
        }
        let Some(at) = published.and_then(|s| OffsetDateTime::parse(s, &Rfc3339).ok()) else {
            return Placement::Unplaced;
        };
        let margin = time::Duration::seconds(margin_seconds.max(0));
        let (earliest, latest) = (at - margin, at + margin);
        if self.start.is_some_and(|s| latest < s) || self.end.is_some_and(|e| earliest > e) {
            return Placement::Outside;
        }
        if self.start.is_none_or(|s| earliest >= s) && self.end.is_none_or(|e| latest <= e) {
            return Placement::Inside;
        }
        Placement::Unplaced
    }
}

/// How far a stored date may lie from the true time, by the precision it was stored with; `None`
/// for a date that is only a guess (approximate, discovery, unknown).
pub fn stored_margin(precision: Option<&str>) -> Option<i64> {
    match precision {
        Some("source" | "exact" | "second" | "minute" | "millisecond") => Some(0),
        Some("day") => Some(86_400),
        _ => None,
    }
}

/// How far an approximate date may be off when the adapter did not say: a year either way.
const UNSTATED_MARGIN_SECONDS: i64 = 366 * 86_400;

impl Range {
    /// Places an entry by what its list line gave: an exact date, an approximate one with its
    /// margin, or nothing.
    pub fn place_listed(&self, entry: &trace_contracts::plugin::EnumeratedEntry) -> Placement {
        let margin = if entry.published_precision == "approximate" {
            entry
                .published_margin_seconds
                .unwrap_or(UNSTATED_MARGIN_SECONDS)
        } else {
            0
        };
        self.place(entry.published_at.as_deref(), margin)
    }
}

/// An entry measured against a subscription's range.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Placement {
    Inside,
    Outside,
    /// The date is missing or too coarse to say.
    Unplaced,
}
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn range_is_inclusive_and_does_not_infer_order_or_unknown_dates() {
        let r = Range::parse(
            &json!({"kind":"recent_days","days":2}),
            "2026-10-07T00:00:00Z",
        )
        .unwrap();
        assert!(r.includes(Some("2026-10-05T00:00:00Z")));
        assert!(!r.includes(Some("2026-10-04T23:59:59Z")));
        assert!(r.includes(None));
        assert!(r.includes(Some("2026-10-06T00:00:00Z")));
        assert!(
            Range::parse(
                &json!({"kind":"recent_days","days":0}),
                "2026-10-07T00:00:00Z"
            )
            .is_err()
        );
    }

    #[test]
    fn an_entry_is_placed_by_its_date_and_how_far_that_date_may_be_off() {
        let r = Range::parse(
            &json!({"kind":"recent_days","days":7}),
            "2026-10-18T00:00:00Z",
        )
        .unwrap();
        // The range starts on 2026-10-11.
        assert_eq!(r.place(Some("2026-10-15T00:00:00Z"), 0), Placement::Inside);
        assert_eq!(r.place(Some("2026-10-01T00:00:00Z"), 0), Placement::Outside);
        // A day-sized margin around the edge leaves the entry unplaced, far from it decides.
        assert_eq!(
            r.place(Some("2026-10-11T12:00:00Z"), 86_400),
            Placement::Unplaced
        );
        assert_eq!(
            r.place(Some("2026-10-05T00:00:00Z"), 86_400),
            Placement::Outside
        );
        assert_eq!(
            r.place(Some("2026-10-15T00:00:00Z"), 86_400),
            Placement::Inside
        );
        // No date: unplaced under a bound, inside when nothing is asked of the date.
        assert_eq!(r.place(None, 0), Placement::Unplaced);
        assert_eq!(r.place(Some("not a time"), 0), Placement::Unplaced);
        let all = Range::parse(&json!({"kind":"all"}), "2026-10-18T00:00:00Z").unwrap();
        assert_eq!(all.place(None, 0), Placement::Inside);
        assert!(r.follows_the_present() && !all.follows_the_present());
        let closed = Range::parse(
            &json!({"kind":"date_range","start":"2026-01-01T00:00:00Z","end":"2026-02-01T00:00:00Z"}),
            "2026-10-18T00:00:00Z",
        )
        .unwrap();
        assert!(!closed.follows_the_present());
        assert_eq!(
            closed.place(Some("2026-03-01T00:00:00Z"), 0),
            Placement::Outside
        );
    }
}
