//! Verified quality selection and recoverable recycling. Source labels alone
//! never release bytes. Unknown, differing tracks and live coverage are retained.
use crate::{core::Core, error::CoreError};
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};
use trace_contracts::control::QualityRequirement;
use trace_domain::entities::MediaSpec;

pub fn validate_requirement(p: &QualityRequirement) -> Result<(), CoreError> {
    if p.min_height.is_some_and(|n| n == 0 || n > 16384)
        || p.min_fps.is_some_and(|n| n == 0 || n > 1000)
        || p.strict && (p.min_height.is_none() && p.min_fps.is_none())
    {
        return Err(CoreError::InvalidInput(
            "invalid quality requirement; strict mode needs an explicit minimum".into(),
        ));
    }
    Ok(())
}
pub fn meets(p: &QualityRequirement, v: &Value) -> bool {
    p.min_height
        .is_none_or(|min| v["height"].as_u64().is_some_and(|n| n >= min as u64))
        && p.min_fps.is_none_or(|min| {
            trace_contracts::content::source_fps(v).is_some_and(|n| n >= min as f64)
        })
}
/// Filter representations independently; a low offer must not reject a high
/// offer for the same required resource. Absence/unknown remains a hard failure.
pub fn filter_required(
    p: &QualityRequirement,
    resources: &[trace_contracts::plugin::ResourceIdentityDto],
    plans: &mut Vec<trace_contracts::plugin::MediaCandidate>,
) -> Result<(), CoreError> {
    if !p.strict {
        return Ok(());
    }
    plans.retain(|c| {
        !matches!(
            c.resource.role.as_str(),
            "video" | "motion" | "video_segment"
        ) || meets(p, &c.claimed_spec)
    });
    if resources.iter().any(|r| {
        r.required
            && matches!(r.role.as_str(), "video" | "motion" | "video_segment")
            && !plans
                .iter()
                .any(|c| c.resource.source_attachment_id == r.source_attachment_id)
    }) {
        return Err(CoreError::Adapter(
            trace_domain::errors::FailureClass::CapabilityLost,
        ));
    }
    Ok(())
}
pub async fn requirement(
    core: &Core,
    job: &crate::jobs::ClaimedJob,
) -> Result<QualityRequirement, CoreError> {
    if let Some(quality) = job
        .payload
        .get("execution_scope")
        .and_then(|s| s.get("quality"))
    {
        return Ok(serde_json::from_value(quality.clone())?);
    }
    let subscription = job.payload["subscription_id"].as_str().map(str::to_owned);
    core.db
        .read(move |c| {
            let raw: Option<String> = c
                .query_row(
                    "SELECT quality_json FROM subscriptions WHERE subscription_id=?1",
                    [subscription],
                    |r| r.get(0),
                )
                .optional()?;
            Ok(raw
                .map(|s| serde_json::from_str(&s))
                .transpose()?
                .unwrap_or_default())
        })
        .await
}
pub async fn enforce_measured(
    core: &Core,
    job: &crate::jobs::ClaimedJob,
    role: &str,
    spec: &Value,
) -> Result<(), CoreError> {
    let p = requirement(core, job).await?;
    if p.strict && matches!(role, "video" | "motion" | "video_segment") && !meets(&p, spec) {
        return Err(CoreError::Adapter(
            trace_domain::errors::FailureClass::CapabilityLost,
        ));
    }
    Ok(())
}

pub async fn policy(core: &Core) -> Result<(bool, i64), CoreError> {
    let default = (
        core.profile.quality_prefer_fps,
        core.profile.recycle_wait_seconds.max(0),
    );
    core.db
        .read(move |c| {
            let value: Option<String> = c
                .query_row(
                    "SELECT value FROM meta WHERE key='quality_policy'",
                    [],
                    |r| r.get(0),
                )
                .optional()?;
            let value = value
                .and_then(|s| serde_json::from_str::<Value>(&s).ok())
                .unwrap_or(Value::Null);
            Ok((
                value["prefer_fps"].as_bool().unwrap_or(default.0),
                value["wait_seconds"].as_i64().unwrap_or(default.1).max(0),
            ))
        })
        .await
}

fn offered_rank(v: &Value, prefer_fps: bool) -> Option<(i64, i64)> {
    trace_contracts::content::source_rank(v, prefer_fps)
}

const UPDATE_MEDIA_ITEMS: &str = "UPDATE media_items SET acquisition_evidence=json_set(COALESCE(acquisition_evidence,'{}'),'$.quality_offers',json(?3),'$.quality_target',json(?4),'$.quality_observed_at',?5,'$.quality_prefer_fps',?6,'$.capability_decreased',?7) WHERE revision_id=?1 AND source_attachment_id=?2";

pub fn record_offers(
    tx: &Transaction<'_>,
    revision: &str,
    resources: &[trace_contracts::plugin::ResourceIdentityDto],
    plans: &[trace_contracts::plugin::MediaCandidate],
    prefer_fps: bool,
) -> Result<(), CoreError> {
    for resource in resources
        .iter()
        .filter(|r| matches!(r.role.as_str(), "video" | "motion"))
    {
        let available = plans
            .iter()
            .filter(|p| p.resource.source_attachment_id == resource.source_attachment_id)
            .map(|p| p.claimed_spec.clone())
            .collect::<Vec<_>>();
        let best = available
            .iter()
            .filter_map(|v| offered_rank(v, prefer_fps).map(|rank| (rank, v)))
            .max_by_key(|(rank, _)| *rank)
            .map(|(_, v)| v.clone());
        let old:Option<String>=tx.query_row("SELECT acquisition_evidence FROM media_items WHERE revision_id=?1 AND source_attachment_id=?2",rusqlite::params![revision,resource.source_attachment_id],|r|r.get(0)).optional()?.flatten();
        let old = old
            .and_then(|v| serde_json::from_str::<Value>(&v).ok())
            .unwrap_or(Value::Null);
        let prior = &old["quality_target"];
        let downgraded = (old["quality_prefer_fps"] == json!(prefer_fps)
            || old["quality_prefer_fps"] == json!(prefer_fps as i64))
            && offered_rank(prior, prefer_fps).is_some()
            && best.as_ref().and_then(|v| offered_rank(v, prefer_fps))
                < offered_rank(prior, prefer_fps);
        let target = if downgraded {
            prior.clone()
        } else {
            json!(best)
        };
        tx.execute(
            UPDATE_MEDIA_ITEMS,
            rusqlite::params![
                revision,
                resource.source_attachment_id,
                json!(available).to_string(),
                target.to_string(),
                crate::jobs::now(),
                prefer_fps,
                downgraded
            ],
        )?;
        if downgraded {
            crate::objects::outbox_emit(
                tx,
                "entry.capability_changed",
                revision,
                &json!({"state":"decreased"}),
            )?;
        }
    }
    Ok(())
}

// Scope sessions first: the flattened join otherwise scans every recording
// fragment even for a live entry with no recording sessions of its own.
const LIVE_DEGRADED_SQL: &str = "SELECT EXISTS(SELECT 1 FROM live_sessions s WHERE s.entry_id=?1 AND EXISTS(SELECT 1 FROM live_fragments f LEFT JOIN media_items m ON m.media_id=f.media_id WHERE f.session_id=s.session_id AND (f.reason='strict_quality_unrecorded' OR json_extract(m.acquisition_evidence,'$.below_target')=1)))";

const SELECT_REPLICAS: &str = "SELECT m.acquisition_evidence,v.measured_spec,EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=v.blob_id AND r.state='verified') FROM media_items m LEFT JOIN asset_selections s ON s.media_id=m.media_id LEFT JOIN media_variants v ON v.variant_id=s.variant_id WHERE m.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1) AND m.role IN ('video','motion')";

pub fn state(c: &Connection, entry: &str) -> Result<String, CoreError> {
    if crate::upgrades::has_pending(c, entry)? {
        return Ok("pending_upgrade".into());
    }
    let live: bool = c
        .prepare_cached(
            "SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND native_kind='live')",
        )?
        .query_row([entry], |r| r.get(0))?;
    if live {
        let degraded: bool = c
            .prepare_cached(LIVE_DEGRADED_SQL)?
            .query_row([entry], |r| r.get(0))?;
        return Ok(if degraded { "degraded" } else { "unknown" }.into());
    }
    let preference: Option<String> = c
        .prepare_cached("SELECT value FROM meta WHERE key='quality_policy'")?
        .query_row([], |r| r.get(0))
        .optional()?;
    let prefer_fps = preference
        .and_then(|s| serde_json::from_str::<Value>(&s).ok())
        .and_then(|v| v["prefer_fps"].as_bool())
        .unwrap_or(false);
    let rows = c
        .prepare_cached(SELECT_REPLICAS)?
        .query_map([entry], |r| {
            Ok((
                r.get::<_, Option<String>>(0)?,
                r.get::<_, Option<String>>(1)?,
                r.get::<_, bool>(2)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    if rows.is_empty() {
        return Ok("unknown".into());
    }
    let mut unknown:bool=c.prepare_cached("SELECT EXISTS(SELECT 1 FROM media_items m WHERE revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1) AND required=1 AND NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas r ON r.blob_id=v.blob_id AND r.state='verified' WHERE v.media_id=m.media_id))")?.query_row([entry],|r|r.get(0))?;
    for (evidence, spec, verified) in rows {
        let evidence = evidence
            .and_then(|s| serde_json::from_str::<Value>(&s).ok())
            .unwrap_or(Value::Null);
        let prefer_fps = evidence["requested_quality"]["prefer_fps"]
            .as_bool()
            .unwrap_or(prefer_fps);
        if evidence["quality_prefer_fps"] != json!(prefer_fps)
            && evidence["quality_prefer_fps"] != json!(prefer_fps as i64)
        {
            unknown = true;
            continue;
        }
        if evidence["capability_decreased"] == 1 || evidence["capability_decreased"] == true {
            return Ok("degraded".into());
        }
        if let Ok(requested) =
            serde_json::from_value::<QualityRequirement>(evidence["requested_quality"].clone())
            && let Some(raw) = spec
                .as_deref()
                .and_then(|s| serde_json::from_str::<Value>(s).ok())
            && !meets(&requested, &raw)
        {
            return Ok("degraded".into());
        }
        let target = offered_rank(&evidence["quality_target"], prefer_fps);
        let selected = spec
            .as_deref()
            .and_then(measured)
            .and_then(|s| rank(&s, prefer_fps));
        if !verified || target.is_none() || selected.is_none() {
            unknown = true;
            continue;
        }
        // Missing frame-rate claims do not make a higher measured fps a mismatch.
        if selected < target {
            return Ok("degraded".into());
        }
    }
    Ok(if unknown { "unknown" } else { "satisfied" }.into())
}

const SELECT_ASSET_SELECTIONS: &str = "SELECT v.rowid,v.variant_id,v.blob_id,m.role,v.measured_spec,k.reason,COALESCE(k.pinned,0),COALESCE(k.retained,1),k.eligible_at,EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=v.variant_id) FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id LEFT JOIN variant_retention k ON k.variant_id=v.variant_id WHERE r.entry_id=?1 AND v.rowid>?2 ORDER BY v.rowid LIMIT 101";

pub async fn listing(core: &Core, entry: String, after: i64) -> Result<Value, CoreError> {
    let (prefer_fps, wait) = policy(core).await?;
    core.db.read(move|c|{
        let mut values=c.prepare(SELECT_ASSET_SELECTIONS)?.query_map(rusqlite::params![entry,after],|r|Ok(json!({
            "row": r.get::<_, i64>(0)?,
            "variant_id": r.get::<_, String>(1)?,
            "blob_id": r.get::<_, String>(2)?,
            "role": r.get::<_, String>(3)?,
            "measured": r.get::<_, Option<String>>(4)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),
            "reason": r.get::<_, Option<String>>(5)?,
            "pinned": r.get::<_, bool>(6)?,
            "retained": r.get::<_, bool>(7)?,
            "eligible_at": r.get::<_, Option<String>>(8)?,
            "selected": r.get::<_, bool>(9)?,
        })))?.collect::<Result<Vec<_>,_>>()?;
        let more=values.len()>100;values.truncate(100);let next=if more{values.last().and_then(|v|v["row"].as_i64())}else{None};
        Ok(json!({"items":values,"next_after":next,"preference":if prefer_fps{"frame_rate"}else{"resolution"},"recycle_wait_seconds":wait,"state":state(c,&entry)?}))
    }).await
}

/// Every state that applies to one entry, most actionable first. The four filters are derived
/// from the existing tables only; no second state master is stored.
pub fn classify(
    c: &Connection,
    entry: &str,
) -> Result<(String, Vec<String>, Vec<String>), CoreError> {
    let mut states = Vec::new();
    let mut reasons = Vec::new();
    let pending: bool = c.query_row(
        "SELECT EXISTS(SELECT 1 FROM upgrade_candidates WHERE entry_id=?1 AND state='pending')",
        [entry],
        |r| r.get(0),
    )?;
    if pending {
        states.push("pending_upgrade".to_owned());
        reasons.push("a verified higher-quality candidate waits for a decision".to_owned());
    }
    let pinned: bool = c.query_row(
        "SELECT EXISTS(SELECT 1 FROM asset_selections s JOIN media_items m ON m.media_id=s.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 AND s.pinned=1)
             OR EXISTS(SELECT 1 FROM variant_retention k JOIN media_variants v ON v.variant_id=k.variant_id JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 AND k.pinned=1)",
        [entry],
        |r| r.get(0),
    )?;
    if pinned {
        states.push("manually_kept".to_owned());
        reasons.push("a version is pinned and is never recycled automatically".to_owned());
    }
    let replaced: bool = c.query_row(
        "SELECT EXISTS(SELECT 1 FROM variant_retention k JOIN media_variants v ON v.variant_id=k.variant_id JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 AND k.retained=0 AND k.reason='replaced_by_verified_quality')",
        [entry],
        |r| r.get(0),
    )?;
    if replaced {
        states.push("replaced".to_owned());
        reasons.push("a verified variant replaced an earlier selection".to_owned());
    }
    let measured_state = state(c, entry)?;
    if matches!(measured_state.as_str(), "unknown" | "incomparable") {
        states.push("incomparable".to_owned());
        reasons.push("the available representations cannot be compared".to_owned());
    }
    if states.is_empty() {
        states.push("stable".to_owned());
        reasons.push("no pending upgrade or unmeasured comparison".to_owned());
    }
    let primary = states[0].clone();
    Ok((primary, states, reasons))
}

const ENTRIES_AFTER: &str = "SELECT e.entry_id,COALESCE(r.title,''),e.namespace,e.published_at FROM entries e LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id WHERE e.lifecycle!='deleted' AND e.entry_id>?1 ORDER BY e.entry_id LIMIT ?2";

/// A page of entries classified by the four filters; `after` is the previous page's entry id.
pub async fn filtered_listing(
    core: &Core,
    wanted: Option<&str>,
    after: Option<&str>,
    limit: i64,
) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 200);
    let mut cursor = after.unwrap_or("").to_owned();
    let mut items = Vec::new();
    let mut total = 0i64;
    'outer: loop {
        let (batch, cursor2) = {
            let (start, size) = (cursor.clone(), 200i64);
            core.db
                .read(move |c| {
                    let rows = c
                        .prepare(ENTRIES_AFTER)?
                        .query_map(rusqlite::params![start, size], |r| {
                            Ok((
                                r.get::<_, String>(0)?,
                                r.get::<_, String>(1)?,
                                r.get::<_, String>(2)?,
                                r.get::<_, Option<String>>(3)?,
                            ))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    let next = rows.last().map(|r| r.0.clone()).unwrap_or(start);
                    Ok((rows, next))
                })
                .await?
        };
        if batch.is_empty() {
            break;
        }
        total += batch.len() as i64;
        for (entry, title, namespace, published) in batch {
            cursor = entry.clone();
            let (primary, states, reasons) = {
                let entry2 = entry.clone();
                core.db.read(move |c| classify(c, &entry2)).await?
            };
            if let Some(wanted) = wanted
                && !states.iter().any(|s| s == wanted)
            {
                continue;
            }
            items.push(json!({
                "entry_id": entry,
                "title": title,
                "namespace": namespace,
                "published_at": published,
                "state": primary,
                "states": states,
                "reasons": reasons,
                "actions": ["pin","unpin","reevaluate"],
            }));
            if items.len() as i64 >= limit {
                break 'outer;
            }
        }
        let _ = cursor2;
    }
    let more = items.len() as i64 >= limit;
    Ok(json!({
        "items": items,
        "next_after": if more { items.last().and_then(|v| v["entry_id"].as_str()).map(str::to_owned) } else { None },
        "scanned": total,
        "state": wanted,
        "preference": if policy(core).await?.0 { "frame_rate" } else { "resolution" },
    }))
}

pub(crate) fn measured(v: &str) -> Option<MediaSpec> {
    serde_json::from_str(v).ok()
}
pub(crate) fn comparable(a: &MediaSpec, b: &MediaSpec) -> bool {
    a.duration_ms
        .zip(b.duration_ms)
        .is_some_and(|(a, b)| a > 0 && b > 0 && (a - b).abs() <= 250.max(a / 200))
        && a.audio_tracks.is_some()
        && a.audio_tracks == b.audio_tracks
        && a.audio_channels == b.audio_channels
        && a.codec.is_some() == b.codec.is_some()
}
pub(crate) fn rank(v: &MediaSpec, prefer_fps: bool) -> Option<(i64, i64)> {
    let pixels = v.width?.checked_mul(v.height?)?;
    let fps = (v.fps.filter(|n| n.is_finite() && *n > 0.0)? * 1000.0) as i64;
    Some(if prefer_fps {
        (fps, pixels)
    } else {
        (pixels, fps)
    })
}

pub(crate) fn comparison(old: &Value, new: &Value, prefer_fps: bool) -> &'static str {
    let specs = serde_json::from_value::<MediaSpec>(old.clone())
        .ok()
        .zip(serde_json::from_value::<MediaSpec>(new.clone()).ok());
    let Some((old, new)) = specs else {
        return "unknown";
    };
    if !comparable(&old, &new) {
        return "incomparable";
    }
    let ranks = rank(&old, prefer_fps)
        .zip(rank(&new, prefer_fps))
        .or_else(|| {
            old.bitrate_bps
                .zip(new.bitrate_bps)
                .map(|(a, b)| ((a, 0), (b, 0)))
        });
    match ranks.map(|(a, b)| b.cmp(&a)) {
        Some(std::cmp::Ordering::Greater) => "higher",
        Some(std::cmp::Ordering::Less) => "lower",
        Some(std::cmp::Ordering::Equal) => "same_spec",
        None => "unknown",
    }
}

const SELECT_MEDIA_VARIANTS: &str = "SELECT v.variant_id,v.measured_spec,v.source_representation FROM media_variants v WHERE media_id=?1 AND v.approval_state IN ('automatic','accepted','kept') AND (v.source_representation!='playback-mux-v1' OR (SELECT enabled FROM derived_cache_policy WHERE id=1)=1) AND NOT EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=v.variant_id) AND NOT EXISTS(SELECT 1 FROM variant_retention k WHERE k.variant_id=v.variant_id AND k.retained=0 AND k.reason='cache_cleared') AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=v.blob_id AND r.state='verified') ORDER BY created_at,variant_id";

/// All necessary members must be verified before changing any group selection.
/// Same media_id binds source attachment, content revision, track and interval.
pub fn evaluate(
    tx: &Transaction<'_>,
    revision: &str,
    prefer_fps: bool,
    wait_seconds: i64,
) -> Result<(), CoreError> {
    // Repair legacy invalid selections even when the rest of a group is partial.
    tx.execute("DELETE FROM asset_selections WHERE media_id IN (SELECT media_id FROM media_items WHERE revision_id=?1) AND variant_id IN (SELECT variant_id FROM media_variants WHERE approval_state NOT IN ('automatic','accepted','kept'))",[revision])?;
    let complete:bool=tx.query_row("SELECT NOT EXISTS(SELECT 1 FROM media_items m WHERE revision_id=?1 AND required=1 AND NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas r ON r.blob_id=v.blob_id AND r.state='verified' WHERE v.media_id=m.media_id AND v.approval_state IN ('automatic','accepted','kept') AND NOT EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=v.variant_id)))",[revision],|r|r.get(0))?;
    if !complete {
        return Ok(());
    }
    let mids = tx
        .prepare("SELECT media_id,role FROM media_items WHERE revision_id=?1")?
        .query_map([revision], |r| {
            Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let now = crate::jobs::now();
    let eligible = (time::OffsetDateTime::now_utc() + time::Duration::seconds(wait_seconds.max(0)))
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|_| CoreError::Internal("clock".into()))?;
    for (mid, role) in mids {
        let selected: Option<(String, bool)> = tx
            .query_row(
                "SELECT variant_id,pinned FROM asset_selections WHERE media_id=?1",
                [&mid],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .optional()?;
        let Some((original, pinned)) = selected else {
            continue;
        };
        let variants = tx
            .prepare(SELECT_MEDIA_VARIANTS)?
            .query_map([&mid], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, Option<String>>(1)?,
                    r.get::<_, String>(2)?,
                ))
            })?
            .collect::<Result<Vec<_>, _>>()?;
        let mut best = original.clone();
        let Some((_, Some(base), representation)) =
            variants.iter().find(|(id, _, _)| id == &original)
        else {
            continue;
        };
        let Some(base) = measured(base) else {
            continue;
        };
        let mut best_rank = rank(&base, prefer_fps);
        for (id, spec, repr) in &variants {
            let candidate = spec.as_deref().and_then(measured);
            let compatible = candidate.as_ref().is_some_and(|v| comparable(&base, v));
            let candidate_rank = candidate.as_ref().and_then(|v| rank(v, prefer_fps));
            let reason = if pinned {
                "selection_pinned"
            } else if role.starts_with("recording") {
                "unique_live_coverage"
            } else if repr == "playback-mux-v1" || representation == "playback-mux-v1" {
                "derived_copy_preserves_sources"
            } else if !compatible {
                "incomparable_duration_or_tracks"
            } else if candidate_rank.is_none() || best_rank.is_none() {
                "unknown_measured_quality"
            } else {
                "comparable_same_attachment"
            };
            tx.execute("INSERT INTO variant_retention(variant_id,reason,updated_at) VALUES(?1,?2,?3) ON CONFLICT(variant_id) DO UPDATE SET reason=excluded.reason,updated_at=excluded.updated_at,retained=CASE WHEN excluded.reason!='comparable_same_attachment' THEN 1 ELSE retained END,eligible_at=CASE WHEN excluded.reason!='comparable_same_attachment' THEN NULL ELSE eligible_at END",rusqlite::params![id,reason,now])?;
            if reason == "comparable_same_attachment" && candidate_rank > best_rank {
                best = id.clone();
                best_rank = candidate_rank;
            }
        }
        if best != original {
            tx.execute("UPDATE asset_selections SET variant_id=?2,policy_version='m1-v1',reason=?3,updated_at=?4 WHERE media_id=?1 AND pinned=0",rusqlite::params![mid,best,if prefer_fps{"verified_same_attachment_prefer_fps"}else{"verified_same_attachment_prefer_resolution"},now])?;
            tx.execute("UPDATE variant_retention SET retained=1,eligible_at=NULL,reason='selected' WHERE variant_id=?1",[&best])?;
            tx.execute("UPDATE variant_retention SET retained=0,eligible_at=?2,reason='replaced_by_verified_quality' WHERE variant_id=?1 AND pinned=0",rusqlite::params![original,eligible])?;
            crate::objects::outbox_emit(
                tx,
                "entry.quality_changed",
                revision,
                &json!({"media_id":mid,"selected_variant":best}),
            )?;
        }
        tx.execute("UPDATE variant_retention SET retained=1,eligible_at=NULL,reason='selected' WHERE variant_id=?1",[&best])?;
    }
    Ok(())
}

/// A persistent low-priority sweep evaluates bounded batches and resumes after
/// shutdown. It shares the same job generation checks as downloads.
pub fn schedule(tx: &Transaction<'_>, immediate: bool) -> Result<(), CoreError> {
    let next = (time::OffsetDateTime::now_utc()
        + time::Duration::seconds(if immediate { 0 } else { 600 }))
    .format(&time::format_description::well_known::Rfc3339)
    .map_err(|_| CoreError::Internal("clock".into()))?;
    tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) VALUES('quality-maintenance','quality-maintenance','quality_maintenance','archive','{}',3,'queued',?1,?1,?1)",[&next])?;
    if immediate {
        tx.execute("UPDATE jobs SET payload='{}',state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?1,updated_at=?1,attempts=0,last_error=NULL WHERE job_key='quality-maintenance'",[next])?;
    }
    Ok(())
}

const SELECT_MEDIA_VARIANTS_2: &str = "SELECT DISTINCT r.rowid,r.revision_id,r.entry_id FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.rowid>?1 ORDER BY r.rowid LIMIT 16";

const SELECT_MEDIA_ITEMS: &str = "SELECT DISTINCT group_key FROM media_items WHERE revision_id=?1 AND role='video' AND group_key IS NOT NULL";
const UPDATE_JOBS: &str = "UPDATE jobs SET state='queued',payload=?2,next_run_at=?3,lease_owner=NULL,lease_expires_at=NULL,updated_at=?3 WHERE job_id=?1";

pub async fn maintain(core: &Core, job: &crate::jobs::ClaimedJob) -> Result<(), CoreError> {
    let after = job.payload["after"].as_i64().unwrap_or(0);
    let (prefer, wait) = policy(core).await?;
    let guard = job.clone();
    let revisions = core
        .db
        .write(move |tx| {
            guard.check(tx)?;
            let rows = tx
                .prepare(SELECT_MEDIA_VARIANTS_2)?
                .query_map([after], |r| {
                    Ok((
                        r.get::<_, i64>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                    ))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            for (_, revision, entry) in &rows {
                evaluate(tx, revision, prefer, wait)?;
                crate::objects::outbox_emit(
                    tx,
                    "entry.quality_changed",
                    entry,
                    &json!({"reevaluated":true}),
                )?;
            }
            Ok(rows)
        })
        .await?;
    for (_, revision, _) in &revisions {
        let rev = revision.clone();
        let groups = core
            .db
            .read(move |c| {
                Ok(c.prepare(SELECT_MEDIA_ITEMS)?
                    .query_map([rev], |r| r.get::<_, String>(0))?
                    .collect::<Result<Vec<_>, _>>()?)
            })
            .await?;
        for group in groups {
            crate::pipeline::mux::create_playback_mux(core, job, revision, &group).await?;
        }
    }
    recycle_guarded(core, Some(job.clone())).await?;
    let guard = job.clone();
    let more = revisions.len() == 16;
    let after = if more { revisions.last().unwrap().0 } else { 0 };
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let next = (time::OffsetDateTime::now_utc()
                + time::Duration::seconds(if more { 1 } else { 600 }))
            .format(&time::format_description::well_known::Rfc3339)
            .map_err(|_| CoreError::Internal("clock".into()))?;
            tx.execute(
                UPDATE_JOBS,
                rusqlite::params![guard.job_id, json!({"after":after}).to_string(), next],
            )?;
            Ok(())
        })
        .await
}

fn protected(c: &Connection, blob: &str, now: &str) -> Result<bool, CoreError> {
    Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM media_variants v LEFT JOIN variant_retention k ON k.variant_id=v.variant_id WHERE v.blob_id=?1 AND (COALESCE(k.retained,1)=1 OR k.pinned=1 OR k.eligible_at IS NULL OR k.eligible_at>?2 OR EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=v.variant_id))) OR EXISTS(SELECT 1 FROM materializations m WHERE m.stage IN ('RECEIVING','VALIDATING','PREPARED','OBJECT_READY') AND (m.blob_id=?1 OR ('sha256:'||m.received_sha256)=?1))",rusqlite::params![blob,now],|r|r.get(0))?)
}

/// Files remain in .trash after the first pass. A later pass rechecks every
/// reference before unlink. Pending moves recover by examining both locations.
pub async fn recycle(core: &Core) -> Result<Value, CoreError> {
    recycle_guarded(core, None).await
}
async fn recycle_guarded(
    core: &Core,
    guard: Option<crate::jobs::ClaimedJob>,
) -> Result<Value, CoreError> {
    let _gate = core.archive_access.write().await;
    let candidates:Vec<String>=core.db.read(|c|Ok(c.prepare("SELECT v.blob_id FROM media_variants v JOIN variant_retention k ON k.variant_id=v.variant_id WHERE k.retained=0 AND k.eligible_at<=?1 UNION SELECT blob_id FROM object_recycling WHERE phase IN ('prepared','trashed') LIMIT 64")?.query_map([crate::jobs::now()],|r|r.get(0))?.collect::<Result<_,_>>()?)).await?;
    let active = core
        .leases
        .lock()
        .unwrap()
        .values()
        .filter(|l| l.expires_at > std::time::Instant::now())
        .map(|l| format!("sha256:{}", l.blob_sha))
        .collect::<std::collections::BTreeSet<_>>();
    let mut moved = 0;
    let mut removed = 0;
    for blob in candidates {
        if active.contains(&blob) {
            continue;
        }
        let sha = blob
            .strip_prefix("sha256:")
            .ok_or_else(|| CoreError::InvalidInput("blob identity".into()))?;
        let path = core.objects.object_path(sha)?;
        let trash = core.objects.trash_dir().join(format!("recycle-{sha}"));
        crate::paths::no_links(&trash)?;
        let b = blob.clone();
        let check = guard.clone();
        let prepared=core.db.write(move|tx|{
            if let Some(job)=check{job.check(tx)?;}
            let pending:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM object_recycling WHERE blob_id=?1 AND phase IN ('prepared','trashed'))",[&b],|r|r.get(0))?;
            if !pending&&protected(tx,&b,&crate::jobs::now())? {return Ok(false);}
            tx.execute("INSERT OR IGNORE INTO object_recycling VALUES(?1,'prepared',?2)",rusqlite::params![b,crate::jobs::now()])?;Ok(true)
        }).await?;
        if !prepared {
            continue;
        }
        let b = blob.clone();
        let restoring = core
            .db
            .read(move |c| protected(c, &b, &crate::jobs::now()))
            .await?;
        // Hash outside the database transaction. If a new pin arrives after this
        // check, leave its journal for the next pass instead of trusting old flags.
        let restore_valid = if restoring {
            let file = if path.exists() { &path } else { &trash };
            Some(crate::objects::hash_file(file).is_ok_and(|actual| actual == sha))
        } else {
            None
        };
        let check = guard.clone();
        let result=core.db.write(move|tx|{
            if let Some(job)=check{job.check(tx)?;}
            // A newly installed/retained variant can have appeared since prepare.
            if protected(tx,&blob,&crate::jobs::now())?{
                let Some(valid)=restore_valid else{return Ok(0);};
                if !valid{tx.execute("UPDATE replicas SET state='corrupt' WHERE blob_id=?1 AND managed=1",[&blob])?;return Ok(0);}
                if trash.exists()&&!path.exists(){std::fs::create_dir_all(path.parent().unwrap())?;std::fs::rename(&trash,&path)?;}
                if path.exists(){tx.execute("UPDATE replicas SET state='verified' WHERE blob_id=?1 AND state='recycled'",[&blob])?;}
                tx.execute("DELETE FROM object_recycling WHERE blob_id=?1",[&blob])?;return Ok(0);
            }
            let phase:String=tx.query_row("SELECT phase FROM object_recycling WHERE blob_id=?1",[&blob],|r|r.get(0))?;
            if phase=="trashed" {
                if trash.exists(){std::fs::remove_file(&trash)?;}
                tx.execute("UPDATE object_recycling SET phase='removed',updated_at=?2 WHERE blob_id=?1",rusqlite::params![blob,crate::jobs::now()])?;
                return Ok(2);
            }
            if phase=="removed"{return Ok(0);}
            if path.exists(){std::fs::create_dir_all(trash.parent().unwrap())?;std::fs::rename(&path,&trash)?;}
            tx.execute("UPDATE replicas SET state='recycled' WHERE blob_id=?1 AND managed=1",[&blob])?;
            tx.execute("UPDATE object_recycling SET phase='trashed',updated_at=?2 WHERE blob_id=?1",rusqlite::params![blob,crate::jobs::now()])?;Ok(1)
        }).await?;
        if result == 1 {
            moved += 1;
        }
        if result == 2 {
            removed += 1;
        }
    }
    Ok(json!({"moved_to_trash":moved,"removed_from_trash":removed}))
}

pub fn pin(tx: &Transaction<'_>, variant: &str, enabled: bool) -> Result<(), CoreError> {
    let verified:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM media_variants v JOIN replicas r ON r.blob_id=v.blob_id WHERE variant_id=?1 AND r.state='verified')",[variant],|r|r.get(0))?;
    if !verified {
        return Err(CoreError::InvalidInput(
            "variant has no verified local replica".into(),
        ));
    }
    tx.execute("INSERT INTO variant_retention(variant_id,pinned,reason,updated_at) VALUES(?1,?2,'manual_keep',?3) ON CONFLICT(variant_id) DO UPDATE SET pinned=?2,retained=1,eligible_at=NULL,reason='manual_keep',updated_at=?3",rusqlite::params![variant,enabled,crate::jobs::now()])?;
    Ok(())
}

/// Pins the variant in use, or unpins every variant of an entry for the entry-level action.
pub fn pin_entry(tx: &Transaction<'_>, entry: &str, enabled: bool) -> Result<(), CoreError> {
    if enabled {
        let variant = crate::store::quality::primary_variant(tx, entry)?
            .ok_or_else(|| CoreError::NotFound("a verified variant of the entry".into()))?;
        return pin(tx, &variant, true);
    }
    tx.execute(
        "UPDATE variant_retention SET pinned=0,retained=1,reason='manual_unpin',eligible_at=NULL,updated_at=?2
         WHERE variant_id IN (SELECT v.variant_id FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1)",
        rusqlite::params![entry, crate::jobs::now()],
    )?;
    Ok(())
}

#[cfg(test)]
const INSERT_SOURCE_ACCOUNTS: &str = "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')";
#[cfg(test)]
const INSERT_ENTRIES: &str = "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','bilibili','video','1','a','t')";
#[cfg(test)]
const INSERT_ENTRY_REVISIONS: &str = "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','t','body','t')";
#[cfg(test)]
const INSERT_MEDIA_ITEMS: &str = "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES(?1,'r',?2,0,?1,1)";
#[cfg(test)]
const INSERT_BLOBS: &str = "INSERT INTO blobs(blob_id,size,first_seen_at) VALUES(?1,22,'t')";
#[cfg(test)]
const INSERT_REPLICAS: &str = "INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state) VALUES('low',?1,'local','primary','low',1,'verified')";
#[cfg(test)]
const INSERT_ASSET_SELECTIONS: &str = "INSERT INTO asset_selections(media_id,variant_id,policy_version,updated_at) VALUES(?1,?2,'v1','t')";
#[cfg(test)]
const SELECT_ASSET_SELECTIONS_2: &str =
    "SELECT variant_id FROM asset_selections WHERE media_id='main'";
#[cfg(test)]
const INSERT_REPLICAS_2: &str = "INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES('high','sha256:high','local','primary','high','verified')";
#[cfg(test)]
const INSERT_REPLICAS_3: &str = "INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES('audio','sha256:audio','local','primary','audio','verified')";
#[cfg(test)]
const SELECT_ASSET_SELECTIONS_3: &str =
    "SELECT variant_id FROM asset_selections WHERE media_id='live'";
#[cfg(test)]
const SELECT_VARIANT_RETENTION: &str =
    "SELECT retained=1 AND eligible_at IS NULL FROM variant_retention WHERE variant_id=?1";
#[cfg(test)]
const SELECT_VARIANT_RETENTION_2: &str =
    "SELECT retained=0 AND eligible_at IS NOT NULL FROM variant_retention WHERE variant_id=?1";
#[cfg(test)]
const SELECT_VARIANT_RETENTION_3: &str =
    "SELECT eligible_at>?1 FROM variant_retention WHERE variant_id='low'";
#[cfg(test)]
const UPDATE_VARIANT_RETENTION: &str =
    "UPDATE variant_retention SET eligible_at='2000-01-01T00:00:00Z' WHERE variant_id='low'";
#[cfg(test)]
const DELETE_ASSET_SELECTIONS: &str =
    "DELETE FROM asset_selections WHERE media_id IN ('shared','live')";
#[cfg(test)]
const UPDATE_VARIANT_RETENTION_2: &str = "UPDATE variant_retention SET retained=0,pinned=0,eligible_at='2000-01-01T00:00:00Z' WHERE variant_id IN ('low','shared','live-low')";
#[cfg(test)]
const UPDATE_VARIANT_RETENTION_3: &str = "UPDATE variant_retention SET retained=0,pinned=0,eligible_at='2000-01-01T00:00:00Z' WHERE variant_id='low'";

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn live_quality_query_ignores_unrelated_recording_fragments() {
        let mut c = Connection::open_in_memory().unwrap();
        crate::db::schema::configure(&c).unwrap();
        crate::db::schema::run_migrations(&mut c).unwrap();
        let tx = c.transaction().unwrap();
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('account','bilibili','1','t')", []).unwrap();
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,quality_json,created_at,updated_at) VALUES('sub','account','live','{}','{}','t','t')", []).unwrap();
        for entry in ["empty", "target", "unrelated"] {
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,'bilibili','live',?1,'account','t')", [entry]).unwrap();
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?1,?1,'live','','t')", [entry]).unwrap();
        }
        for entry in ["target", "unrelated"] {
            tx.execute("INSERT INTO live_sessions(session_id,subscription_id,room_key,source_session,entry_id,state,started_at) VALUES(?1,'sub',?1,?1,?1,'sealed','t')", [entry]).unwrap();
        }
        for index in 0..10000 {
            tx.prepare_cached("INSERT INTO live_fragments(fragment_id,session_id,sequence,source_sequence,state,reason) VALUES(?1,'unrelated',?2,?1,'gap','strict_quality_unrecorded')").unwrap().execute(rusqlite::params![format!("fragment-{index}"),index]).unwrap();
        }
        tx.commit().unwrap();
        let mut statement = c.prepare(LIVE_DEGRADED_SQL).unwrap();
        let degraded: bool = statement.query_row(["empty"], |r| r.get(0)).unwrap();
        assert!(!degraded);
        let steps = statement.get_status(rusqlite::StatementStatus::VmStep);
        assert!(
            steps < 5000,
            "an entry without recording sessions traversed unrelated fragments: {steps} VM steps"
        );
        assert_eq!(state(&c, "empty").unwrap(), "unknown");
        assert_eq!(state(&c, "target").unwrap(), "unknown");
        assert_eq!(state(&c, "unrelated").unwrap(), "degraded");
        c.execute("INSERT INTO live_fragments(fragment_id,session_id,sequence,source_sequence,state,reason) VALUES('target-fragment','target',0,'target','gap','strict_quality_unrecorded')", []).unwrap();
        assert_eq!(state(&c, "target").unwrap(), "degraded");
        c.execute(
            "UPDATE live_fragments SET reason=NULL WHERE fragment_id='target-fragment'",
            [],
        )
        .unwrap();
        assert_eq!(state(&c, "target").unwrap(), "unknown");
        c.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,acquisition_evidence) VALUES('media','target','recording',0,'media','{\"below_target\":true}')", []).unwrap();
        c.execute(
            "UPDATE live_fragments SET media_id='media' WHERE fragment_id='target-fragment'",
            [],
        )
        .unwrap();
        assert_eq!(state(&c, "target").unwrap(), "degraded");
        c.execute(
            "UPDATE media_items SET acquisition_evidence='{}' WHERE media_id='media'",
            [],
        )
        .unwrap();
        assert_eq!(state(&c, "target").unwrap(), "unknown");
    }
    #[tokio::test]
    async fn quality_failure_shared_blob_pins_unique_coverage_and_gc_windows() {
        let dir = tempfile::tempdir().unwrap();
        let core = Core::open(crate::config::Profile {
            archive_id: "quality-tests".into(),
            data_root: dir.path().join("data"),
            archive_root: dir.path().join("archive"),
            vault_dir: Some(dir.path().join("vault")),
            ..Default::default()
        })
        .await
        .unwrap();
        let bytes = b"synthetic low original";
        use sha2::Digest;
        let sha: String = sha2::Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        let blob = format!("sha256:{sha}");
        let path = core.objects.object_path(&sha).unwrap();
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(&path, bytes).unwrap();
        let b = blob.clone();
        core.db.write(move|tx|{
        tx.execute(INSERT_SOURCE_ACCOUNTS,[])?;
        tx.execute(INSERT_ENTRIES,[])?;
        tx.execute(INSERT_ENTRY_REVISIONS,[])?;
        for (mid,role) in [("main","video"),("audio","audio"),("shared","video"),("live","recording")] {tx.execute(INSERT_MEDIA_ITEMS,rusqlite::params![mid,role])?;}
        for id in [&b,"sha256:high","sha256:audio"]{tx.execute(INSERT_BLOBS,[id])?;}
        tx.execute(INSERT_REPLICAS,[&b])?;
        let spec=json!({"codec":"h264","width":1920,"height":1080,"fps":60,"duration_ms":10000,"audio_tracks":0,"audio_channels":null}).to_string();
        let high=json!({"codec":"h264","width":3840,"height":2160,"fps":30,"duration_ms":10000,"audio_tracks":0,"audio_channels":null}).to_string();
        let variants = [
            ("low", "main", b.as_str(), spec.as_str()),
            ("high", "main", "sha256:high", high.as_str()),
            ("shared", "shared", b.as_str(), spec.as_str()),
            ("live-low", "live", b.as_str(), spec.as_str()),
            ("live-high", "live", "sha256:high", high.as_str()),
            ("audio", "audio", "sha256:audio", spec.as_str()),
        ];
        for (id,mid,blob,spec) in variants {
            tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES(?1,?2,'source',?3,'{}',?4,'t')",rusqlite::params![id,mid,blob,spec])?;
        }
        for (mid,var) in [("main","low"),("shared","shared"),("live","live-low"),("audio","audio")]{tx.execute(INSERT_ASSET_SELECTIONS,rusqlite::params![mid,var])?;}
        evaluate(tx,"r",false,86400)?;
        assert_eq!(tx.query_row(SELECT_ASSET_SELECTIONS_2,[],|r|r.get::<_,String>(0))?,"low","failed/unverified upgrade keeps old selection");
        tx.execute(INSERT_REPLICAS_2,[])?;
        evaluate(tx,"r",false,86400)?;
        assert_eq!(tx.query_row(SELECT_ASSET_SELECTIONS_2,[],|r|r.get::<_,String>(0))?,"low","missing necessary audio blocks group switch");
        tx.execute(INSERT_REPLICAS_3,[])?;
        evaluate(tx,"r",false,86400)?;
        assert_eq!(tx.query_row(SELECT_ASSET_SELECTIONS_2,[],|r|r.get::<_,String>(0))?,"high");
        assert_eq!(tx.query_row(SELECT_ASSET_SELECTIONS_3,[],|r|r.get::<_,String>(0))?,"live-low","resolution alone cannot replace live coverage");
        for (prefer,expected,other) in [(true,"low","high"),(false,"high","low"),(true,"low","high"),(false,"high","low")] {
            evaluate(tx,"r",prefer,86400)?;
            assert_eq!(tx.query_row(SELECT_ASSET_SELECTIONS_2,[],|r|r.get::<_,String>(0))?,expected);
            assert!(tx.query_row(SELECT_VARIANT_RETENTION,[expected],|r|r.get::<_,bool>(0))?);
            assert!(tx.query_row(SELECT_VARIANT_RETENTION_2,[other],|r|r.get::<_,bool>(0))?);
        }
        assert!(protected(tx,&b,&crate::jobs::now())?);
        assert!(tx.query_row(SELECT_VARIANT_RETENTION_3,[crate::jobs::now()],|r|r.get::<_,bool>(0))?,"24 hour grace period is actual evidence");
        tx.execute(UPDATE_VARIANT_RETENTION,[])?;
        pin(tx,"low",true)?;assert!(protected(tx,&b,&crate::jobs::now())?);
        pin_entry(tx,"e",false)?;
        assert!(!tx.query_row("SELECT pinned FROM variant_retention WHERE variant_id='low'",[],|r|r.get::<_,bool>(0))?);
        pin_entry(tx,"e",true)?;
        assert!(tx.query_row("SELECT EXISTS(SELECT 1 FROM variant_retention k JOIN media_variants v ON v.variant_id=k.variant_id JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id='e' AND k.pinned=1)",[],|r|r.get::<_,bool>(0))?);
        Ok(())
    }).await.unwrap();
        assert_eq!(recycle(&core).await.unwrap()["moved_to_trash"], 0);
        assert_eq!(std::fs::read(&path).unwrap(), bytes);
        let b = blob.clone();
        core.db
            .write(move |tx| {
                // Simulate explicit removal of every other retention reason, never blob references.
                tx.execute(DELETE_ASSET_SELECTIONS, [])?;
                tx.execute(UPDATE_VARIANT_RETENTION_2, [])?;
                assert!(!protected(tx, &b, &crate::jobs::now())?);
                Ok(())
            })
            .await
            .unwrap();
        let trash = core.objects.trash_dir().join(format!("recycle-{sha}"));
        // Crash after prepare / file move but before replica update: recovery sees both locations.
        let b = blob.clone();
        core.db
            .write(move |tx| {
                tx.execute(
                    "INSERT INTO object_recycling VALUES(?1,'prepared','t')",
                    [b],
                )?;
                Ok(())
            })
            .await
            .unwrap();
        std::fs::create_dir_all(trash.parent().unwrap()).unwrap();
        std::fs::rename(&path, &trash).unwrap();
        core.db.write(|tx| pin(tx, "low", true)).await.unwrap();
        recycle(&core).await.unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), bytes);
        assert!(!trash.exists());
        core.db
            .write(|tx| {
                tx.execute(UPDATE_VARIANT_RETENTION_3, [])?;
                Ok(())
            })
            .await
            .unwrap();
        assert_eq!(recycle(&core).await.unwrap()["moved_to_trash"], 1);
        assert!(!path.exists());
        assert_eq!(std::fs::read(&trash).unwrap(), bytes);
        // A separate pass, with a fresh reference check, unlinks only the managed trash copy.
        assert_eq!(recycle(&core).await.unwrap()["removed_from_trash"], 1);
        assert!(!trash.exists());
        let rows: i64 = core
            .db
            .read(|c| Ok(c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?))
            .await
            .unwrap();
        assert_eq!(rows, 6, "metadata history survives recycling");
        core.shutdown().await;
    }
}

#[cfg(test)]
mod requirement_tests {
    use super::*;
    #[test]
    fn strict_filters_each_representation_and_requires_all_necessary_media() {
        let resource: trace_contracts::plugin::ResourceIdentityDto = serde_json::from_value(
            json!({"role":"video","source_attachment_id":"v","position":0,"required":true}),
        )
        .unwrap();
        let make = |height| {
            serde_json::from_value(json!({"resource":resource,"representation":format!("{height}"),"kind":"muxed","segments":[],"claimed_spec":{"height":height}})).unwrap()
        };
        let p = QualityRequirement {
            min_height: Some(1080),
            strict: true,
            ..Default::default()
        };
        let mut candidates = vec![make(360), make(1080), make(2160)];
        filter_required(&p, std::slice::from_ref(&resource), &mut candidates).unwrap();
        assert_eq!(candidates.len(), 2);
        assert!(
            filter_required(&p, std::slice::from_ref(&resource), &mut vec![make(360)]).is_err()
        );
        assert!(filter_required(&p, std::slice::from_ref(&resource), &mut vec![]).is_err());
        assert!(
            !meets(&p, &json!({"quality":"origin"})),
            "a label is not measured height"
        );
        let fallback = QualityRequirement { strict: false, ..p };
        let mut low = vec![make(360)];
        filter_required(&fallback, &[resource], &mut low).unwrap();
        assert_eq!(low.len(), 1);
    }
}
