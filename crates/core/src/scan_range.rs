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
pub struct Range {
    pub start: Option<OffsetDateTime>,
    pub end: Option<OffsetDateTime>,
}
/// Recent scans revisit the head on a bounded schedule; deep/history scans have
/// their own durable cursor. Old catalogs gain that lane without enabling any
/// disabled subscription or resurrecting a cancelled job.
pub async fn ensure_deep_lanes(db: &crate::db::DbHandle) -> Result<(), CoreError> {
    db.write(|tx|{
        tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) SELECT 'deep-'||lower(hex(randomblob(16))),'scan:'||s.subscription_id||':deep','scan',s.subscription_id,json_object('subscription_id',s.subscription_id,'mode','deep'),3,'queued',strftime('%Y-%m-%dT%H:%M:%SZ','now','+7 days'),?1,?1 FROM subscriptions s WHERE s.content_type!='live'",[crate::jobs::now()])?;
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
    pub fn includes(&self, published: Option<&str>) -> bool {
        // Unknown dates cannot be safely excluded. Coverage retains that uncertainty.
        let Some(t) = published.and_then(|s| OffsetDateTime::parse(s, &Rfc3339).ok()) else {
            return true;
        };
        self.start.is_none_or(|s| t >= s) && self.end.is_none_or(|e| t <= e)
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn c01_range_is_inclusive_and_does_not_infer_order_or_unknown_dates() {
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
}
