//! Completion of an upgrade job: the candidate becomes pending or is discarded.

use super::*;

const SELECT_UPGRADE_MEMBERS_2: &str = "SELECT old.blob_id,new.blob_id,old.measured_spec,new.measured_spec FROM upgrade_members u LEFT JOIN media_variants old ON old.variant_id=u.old_variant_id LEFT JOIN media_variants new ON new.variant_id=u.new_variant_id WHERE u.candidate_id=?1 AND u.required=1";

const SELECT_UPGRADE_MEMBERS_4: &str = "SELECT EXISTS(SELECT 1 FROM upgrade_members u LEFT JOIN asset_selections s ON s.media_id=u.media_id WHERE u.candidate_id=?1 AND u.old_variant_id IS NOT s.variant_id)";
const SELECT_UPGRADE_MEMBERS_5: &str = "SELECT NOT EXISTS(SELECT 1 FROM upgrade_members u LEFT JOIN media_variants old ON old.variant_id=u.old_variant_id LEFT JOIN media_variants new ON new.variant_id=u.new_variant_id WHERE u.candidate_id=?1 AND u.required=1 AND (old.blob_id IS NULL OR new.blob_id IS NOT old.blob_id))";
const UPDATE_UPGRADE_MEMBERS: &str = "UPDATE upgrade_members SET expected_media_generation=(SELECT resource_generation FROM media_items WHERE media_id=upgrade_members.media_id) WHERE candidate_id=?1";
const UPDATE_UPGRADE_CANDIDATES_3: &str = "UPDATE upgrade_candidates SET state=?2,reason=?3,confirmation=?4,finished_at=?5 WHERE candidate_id=?1 AND state='collecting'";
const UPDATE_MEDIA_VARIANTS: &str = "UPDATE media_variants SET approval_state='rejected' WHERE variant_id IN (SELECT new_variant_id FROM upgrade_members WHERE candidate_id=?1 AND new_variant_id IS NOT old_variant_id) AND approval_state='pending'";

pub async fn finish(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let Some(id) = job.payload["upgrade_candidate_id"]
        .as_str()
        .map(str::to_owned)
    else {
        return Ok(());
    };
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            if !complete(tx, &id)? {
                return Err(CoreError::InvalidInput(
                    "upgrade candidate has incomplete required resources".into(),
                ));
            }
            let stale: bool = tx.query_row(SELECT_UPGRADE_MEMBERS_4, [&id], |r| r.get(0))?;
            let same: bool = tx.query_row(SELECT_UPGRADE_MEMBERS_5, [&id], |r| r.get(0))?;
            tx.execute(UPDATE_UPGRADE_MEMBERS, [&id])?;
            let specs = tx
                .prepare(SELECT_UPGRADE_MEMBERS_2)?
                .query_map([&id], |r| {
                    Ok((
                        r.get::<_, Option<String>>(0)?,
                        r.get::<_, Option<String>>(1)?,
                        r.get::<_, Option<String>>(2)?,
                        r.get::<_, Option<String>>(3)?,
                    ))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            let prefer = guard.payload["execution_scope"]["quality"]["prefer_fps"]
                .as_bool()
                .unwrap_or(false);
            let no_improvement = same
                || (!specs.is_empty()
                    && specs.iter().all(|(old, new, a, b)| {
                        old.is_some()
                            && (old == new
                                || a.as_ref().zip(b.as_ref()).is_some_and(|(a, b)| {
                                    matches!(
                                        crate::quality::comparison(
                                            &serde_json::from_str::<Value>(a).unwrap_or_default(),
                                            &serde_json::from_str::<Value>(b).unwrap_or_default(),
                                            prefer
                                        ),
                                        "lower" | "same_spec"
                                    )
                                }))
                    }));
            let mut expected = snapshot(tx, &id)?;
            if guard.payload["consumer_request"].as_i64().is_some() {
                expected["prefer_fps"] = json!(prefer);
            }
            let state = if stale || expected["revision_id"] != expected["current_revision_id"] {
                "stale"
            } else if no_improvement {
                "no_improvement"
            } else {
                "pending"
            };
            let reason = if expected["revision_id"] != expected["current_revision_id"] {
                "revision_changed"
            } else if stale {
                "selection_changed"
            } else if same {
                "same_verified_bytes"
            } else if no_improvement {
                "no_measured_improvement"
            } else if guard.payload["expected_revision_id"].is_string()
                && guard.payload["expected_revision_id"] != expected["revision_id"]
            {
                "content_edited_requires_decision"
            } else {
                "verified_candidate_requires_decision"
            };
            tx.execute(
                UPDATE_UPGRADE_CANDIDATES_3,
                rusqlite::params![id, state, reason, expected.to_string(), jobs::now()],
            )?;
            if state != "pending" {
                tx.execute(UPDATE_MEDIA_VARIANTS, [&id])?;
            }
            crate::objects::outbox_emit(
                tx,
                "entry.upgrade_changed",
                &guard.subject_id,
                &json!({"candidate_id":id,"state":state}),
            )?;
            Ok(())
        })
        .await
}

pub fn abandon(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    state: &str,
    reason: &str,
) -> Result<(), CoreError> {
    tx.execute("UPDATE upgrade_candidates SET state=?2,reason=?4,finished_at=?3 WHERE job_id=?1 AND state='collecting'",rusqlite::params![job.job_id,state,jobs::now(),reason])?;
    Ok(())
}

pub fn invalidate_stale(tx: &Transaction<'_>) -> Result<(), CoreError> {
    tx.execute("UPDATE upgrade_candidates AS u SET state='stale',reason='comparison_scope_changed',finished_at=?1 WHERE state='pending' AND (EXISTS(SELECT 1 FROM entries e WHERE e.entry_id=u.entry_id AND (e.lifecycle='deleted' OR e.media_suppressed=1 OR e.deletion_generation!=u.deletion_generation)) OR u.revision_id IS NOT (SELECT revision_id FROM current_entry_revisions WHERE entry_id=u.entry_id) OR EXISTS(SELECT 1 FROM subscriptions s WHERE s.subscription_id=u.subscription_id AND (s.state!='enabled' OR s.config_version!=u.subscription_version OR s.credential_id IS NOT u.credential_id)) OR (u.credential_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM credentials_state k WHERE k.credential_id=u.credential_id AND k.generation=u.credential_generation AND k.status IN ('valid','authenticated'))) OR EXISTS(SELECT 1 FROM jobs j WHERE j.job_id=u.job_id AND (j.state='cancelled' OR (u.consumer_request IS NULL AND j.target_generation!=u.job_generation))) OR (u.consumer_request IS NOT NULL AND NOT EXISTS(SELECT 1 FROM job_consumers w WHERE w.job_id=u.job_id AND w.subscription_id=u.subscription_id AND w.requested=u.consumer_request AND w.state!='cancelled')) OR EXISTS(SELECT 1 FROM entries e JOIN plugins_state p ON p.platform=e.namespace WHERE e.entry_id=u.entry_id AND p.enabled=0) OR u.policy IS NOT (SELECT value FROM meta WHERE key='quality_policy') OR EXISTS(SELECT 1 FROM upgrade_members x JOIN media_items m ON m.media_id=x.media_id LEFT JOIN asset_selections s ON s.media_id=m.media_id WHERE x.candidate_id=u.candidate_id AND (m.resource_generation!=x.expected_media_generation OR s.variant_id IS NOT x.old_variant_id OR COALESCE(s.pinned,0)!=x.old_pinned)))",[jobs::now()])?;
    tx.execute("UPDATE upgrade_candidates AS u SET state='cancelled',reason='work_cancelled',finished_at=?1 WHERE state='collecting' AND EXISTS(SELECT 1 FROM jobs j WHERE j.job_id=u.job_id AND (j.state='cancelled' OR j.target_generation!=u.job_generation))",[jobs::now()])?;
    Ok(())
}
