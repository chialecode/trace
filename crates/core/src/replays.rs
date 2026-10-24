//! Recorded session ↔ replay entry linking (stage plan §11.3, C17).
//!
//! A link is evidence, not an edit: no fragment, media item, selection or object is removed.
//! Coverage is the union of the sealed fragment intervals. When the replay's measured duration
//! is shorter, the tail it does not cover is recorded as a recorded-only range and shown to the
//! user; `replay_primary` is refused while a range is unaccounted for or quality is incomparable.
use crate::core::Core;
use crate::error::CoreError;
use crate::store::{self, replays};
use serde_json::{Value, json};
use std::sync::Arc;

/// A replay shorter than the recording by more than this leaves a visible recorded-only tail.
const DURATION_SLACK_MS: i64 = 1000;

fn merged(ranges: &[(i64, i64)]) -> Vec<(i64, i64)> {
    let mut out: Vec<(i64, i64)> = Vec::new();
    for (start, end) in ranges {
        if let Some(last) = out.last_mut()
            && *start <= last.1
        {
            last.1 = last.1.max(*end);
            continue;
        }
        out.push((*start, *end));
    }
    out
}

/// The union, its gaps and what a replay of `replay_duration` cannot have covered.
pub fn coverage(ranges: &[(i64, i64)], replay_duration: Option<i64>) -> Value {
    let union = merged(ranges);
    if union.is_empty() {
        return json!({
            "recorded_ranges": [],
            "recorded_duration_ms": 0,
            "gaps": [],
            "unique_recorded_ranges": [],
            "complete": false,
            "reason": "no sealed fragment interval",
        });
    }
    let start = union[0].0;
    let end = union.last().map(|r| r.1).unwrap_or(start);
    let mut gaps = Vec::new();
    for pair in union.windows(2) {
        if pair[0].1 < pair[1].0 {
            gaps.push(json!({"start_ms": pair[0].1, "end_ms": pair[1].0}));
        }
    }
    let recorded = end - start;
    let (unique, complete, reason) = match replay_duration {
        Some(duration) if duration >= recorded - DURATION_SLACK_MS => (Vec::new(), true, None),
        Some(duration) => (
            vec![json!({"start_ms": start + duration.max(0), "end_ms": end})],
            false,
            Some("replay is shorter than the recording"),
        ),
        None => (
            Vec::new(),
            false,
            Some("the replay duration was not measured; recorded ranges are kept"),
        ),
    };
    json!({
        "recorded_ranges": union.iter().map(|(s, e)| json!({"start_ms": s, "end_ms": e})).collect::<Vec<_>>(),
        "recorded_duration_ms": recorded,
        "gaps": gaps,
        "replay_duration_ms": replay_duration,
        "unique_recorded_ranges": unique,
        "complete": complete,
        "reason": reason,
    })
}

fn duration_of(spec: &Option<Value>) -> Option<i64> {
    spec.as_ref()
        .and_then(|v| v["duration_ms"].as_i64())
        .filter(|d| *d > 0)
}

#[derive(Debug)]
struct Comparison {
    coverage: Value,
    quality: String,
    replay_spec: Option<Value>,
    recording_spec: Option<Value>,
}

fn compare_values(
    ranges: Vec<(i64, i64)>,
    replay_spec: Option<Value>,
    recording_spec: Option<Value>,
    prefer_fps: bool,
) -> Comparison {
    let coverage = coverage(&ranges, duration_of(&replay_spec));
    let quality = if replay_spec.is_none() || recording_spec.is_none() {
        "incomparable".to_owned()
    } else {
        crate::quality::comparison(
            recording_spec.as_ref().unwrap_or(&Value::Null),
            replay_spec.as_ref().unwrap_or(&Value::Null),
            prefer_fps,
        )
        .to_owned()
    };
    Comparison {
        coverage,
        quality,
        replay_spec,
        recording_spec,
    }
}

async fn comparison(
    core: &Arc<Core>,
    session_id: &str,
    entry_id: &str,
) -> Result<Comparison, CoreError> {
    let (s, e) = (session_id.to_owned(), entry_id.to_owned());
    let (ranges, replay_spec, recording_spec) = core
        .db
        .read(move |c| {
            Ok((
                replays::ranges(c, &s)?,
                replays::replay_spec(c, &e)?,
                replays::recording_spec(c, &s)?,
            ))
        })
        .await?;
    let (prefer_fps, _) = crate::quality::policy(core).await?;
    Ok(compare_values(
        ranges,
        replay_spec,
        recording_spec,
        prefer_fps,
    ))
}

fn comparison_document(comparison: &Comparison) -> Value {
    let complete = comparison.coverage["complete"] == json!(true);
    let quality_known = !matches!(comparison.quality.as_str(), "incomparable" | "unknown");
    let reason = if !complete {
        comparison.coverage["reason"]
            .as_str()
            .filter(|reason| !reason.is_empty())
            .unwrap_or("回放没有完整覆盖录制；只能保留两者")
            .to_owned()
    } else if !quality_known {
        "回放或录制的规格不完整，质量不可比较；只能保留两者".to_owned()
    } else {
        String::new()
    };
    json!({
        "coverage": comparison.coverage,
        "quality": comparison.quality,
        "recording_spec": comparison.recording_spec,
        "replay_spec": comparison.replay_spec,
        "can_replay_primary": complete && quality_known,
        "comparison_reason": reason,
    })
}

/// A read-only comparison for one candidate. It never inserts a link row.
pub async fn preview(
    core: &Arc<Core>,
    session_id: &str,
    entry_id: &str,
) -> Result<Value, CoreError> {
    let comparison = comparison(core, session_id, entry_id).await?;
    Ok(comparison_document(&comparison))
}

#[allow(clippy::too_many_lines)] // coverage, quality and audit in one transaction
pub async fn link(
    core: &Arc<Core>,
    session_id: &str,
    entry_id: &str,
    decision: &str,
) -> Result<Value, CoreError> {
    if !matches!(decision, "keep_both" | "replay_primary") {
        return Err(CoreError::InvalidInput("replay decision".into()));
    }
    let (session, entry) = {
        let (s, e) = (session_id.to_owned(), entry_id.to_owned());
        core.db
            .read(move |c| Ok((replays::session(c, &s)?, replays::entry(c, &e)?)))
            .await?
    };
    if session.account_id != entry.account_id {
        return Err(CoreError::InvalidInput(
            "the replay belongs to another source account".into(),
        ));
    }
    if session.state != "sealed" {
        return Err(CoreError::VersionConflict(
            "the recording is not sealed yet".into(),
        ));
    }
    if session.entry_id.as_deref() == Some(entry_id) {
        return Err(CoreError::InvalidInput(
            "the session's own live entry cannot be its replay".into(),
        ));
    }
    let (ranges, replay_spec, recording_spec, prefer_fps) = {
        let (s, e) = (session_id.to_owned(), entry_id.to_owned());
        let (ranges, replay_spec, recording_spec) = core
            .db
            .read(move |c| {
                Ok((
                    replays::ranges(c, &s)?,
                    replays::replay_spec(c, &e)?,
                    replays::recording_spec(c, &s)?,
                ))
            })
            .await?;
        let (prefer_fps, _) = crate::quality::policy(core).await?;
        (ranges, replay_spec, recording_spec, prefer_fps)
    };
    if ranges.is_empty() {
        return Err(CoreError::InvalidInput(
            "the recording has no sealed fragment interval".into(),
        ));
    }
    let comparison = compare_values(ranges, replay_spec, recording_spec, prefer_fps);
    let coverage = comparison.coverage;
    let quality = comparison.quality;
    let replay_spec = comparison.replay_spec;
    let recording_spec = comparison.recording_spec;
    if decision == "replay_primary" {
        if coverage["complete"] != json!(true) {
            return Err(CoreError::InvalidInput(
                "the replay does not cover the whole recording; keep both".into(),
            ));
        }
        if matches!(quality.as_str(), "incomparable" | "unknown") {
            return Err(CoreError::InvalidInput(
                "the replay quality is not comparable; keep both".into(),
            ));
        }
    }
    let now = crate::jobs::now();
    let (s, e, d) = (
        session_id.to_owned(),
        entry_id.to_owned(),
        decision.to_owned(),
    );
    let coverage2 = coverage.clone();
    let quality_value = json!({"comparison": quality});
    let evidence = json!({
        "session_started_at": session.started_at,
        "session_ended_at": session.ended_at,
        "room_key": session.room_key,
        "recording_spec": recording_spec,
        "replay_spec": replay_spec,
        "note": "recording fragments and recorded-only ranges are kept",
    });
    let link_id = core
        .db
        .write(move |tx| {
            let id = format!("rpl-{}", uuid::Uuid::new_v4());
            replays::insert_link(
                tx,
                &id,
                &s,
                &e,
                &d,
                &coverage2,
                &quality_value,
                &evidence,
                &now,
            )?;
            store::management::record(
                tx,
                "replay_link",
                &id,
                &json!({"session_id": s, "entry_id": e, "decision": d, "quality": quality_value["comparison"]}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "live.replay_changed", &s, &json!({"entry_id": e}))?;
            Ok(id)
        })
        .await?;
    let (s, e) = (session_id.to_owned(), entry_id.to_owned());
    let link = core
        .db
        .read(move |c| replays::existing(c, &s, &e))
        .await?
        .unwrap_or_else(|| json!({"link_id": link_id}));
    Ok(json!({
        "link": link,
        "coverage": coverage,
        "quality": quality,
        "recording_kept": true,
    }))
}

pub async fn reject(
    core: &Arc<Core>,
    session_id: &str,
    entry_id: &str,
    reason: &str,
) -> Result<Value, CoreError> {
    let now = crate::jobs::now();
    let (s, e, reason) = (
        session_id.to_owned(),
        entry_id.to_owned(),
        reason.chars().take(300).collect::<String>(),
    );
    let changed = core
        .db
        .write(move |tx| {
            let link_id = format!("rpl-{}", uuid::Uuid::new_v4());
            let changed = replays::reject(tx, &link_id, &s, &e, &reason, &now)?;
            if changed {
                store::management::record(
                    tx,
                    "replay_reject",
                    &s,
                    &json!({"entry_id": e, "reason": reason}),
                    &now,
                )?;
                crate::objects::outbox_emit(
                    tx,
                    "live.replay_changed",
                    &s,
                    &json!({"entry_id": e, "state": "rejected"}),
                )?;
            }
            Ok(changed)
        })
        .await?;
    Ok(json!({"rejected": changed}))
}
