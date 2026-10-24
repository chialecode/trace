//! A user's decision on pending upgrade candidates: accept, reject or keep both.

use super::*;

const SELECT_UPGRADE_MEMBERS_3: &str = "SELECT media_id,old_variant_id,new_variant_id FROM upgrade_members WHERE candidate_id=?1 AND new_variant_id IS NOT NULL";
const SELECT_MEDIA_VARIANTS_2: &str = "SELECT old.measured_spec,new.measured_spec FROM media_variants old,media_variants new WHERE old.variant_id=?1 AND new.variant_id=?2";
const INSERT_VARIANT_RETENTION: &str = "INSERT INTO variant_retention(variant_id,retained,pinned,reason,eligible_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6) ON CONFLICT(variant_id) DO UPDATE SET retained=CASE WHEN pinned=1 THEN 1 ELSE excluded.retained END,pinned=MAX(pinned,excluded.pinned),reason=excluded.reason,eligible_at=CASE WHEN pinned=1 OR excluded.retained=1 THEN NULL ELSE excluded.eligible_at END";

const SELECT_UPGRADE_MEMBERS_6: &str = "SELECT DISTINCT v.blob_id FROM upgrade_members u JOIN media_variants v ON v.variant_id=u.new_variant_id JOIN upgrade_candidates k ON k.candidate_id=u.candidate_id WHERE u.candidate_id=?1 AND k.state='pending'";
const SELECT_UPGRADE_CANDIDATES_3: &str =
    "SELECT state,confirmation,entry_id FROM upgrade_candidates WHERE candidate_id=?1";
const UPDATE_MEDIA_VARIANTS_2: &str =
    "UPDATE media_variants SET approval_state=?2 WHERE variant_id=?1 AND approval_state='pending'";
const INSERT_VARIANT_RETENTION_2: &str = "INSERT INTO variant_retention(variant_id,retained,reason,eligible_at,updated_at) VALUES(?1,0,'upgrade_rejected',?2,?3) ON CONFLICT(variant_id) DO UPDATE SET retained=0,reason='upgrade_rejected',eligible_at=excluded.eligible_at WHERE pinned=0";
const INSERT_ASSET_SELECTIONS: &str = "INSERT INTO asset_selections(media_id,variant_id,policy_version,reason,updated_at) VALUES(?1,?2,'m1-r2','user_confirmed_upgrade',?3) ON CONFLICT(media_id) DO UPDATE SET variant_id=excluded.variant_id,reason=excluded.reason,policy_version=excluded.policy_version,updated_at=excluded.updated_at";
const INSERT_VARIANT_RETENTION_3: &str = "INSERT INTO variant_retention(variant_id,retained,pinned,reason,updated_at) VALUES(?1,1,?2,'user_confirmed_upgrade',?3) ON CONFLICT(variant_id) DO UPDATE SET retained=1,pinned=MAX(pinned,excluded.pinned),eligible_at=NULL,reason=excluded.reason";
const SELECT_UPGRADE_MEMBERS_7: &str =
    "SELECT old_pinned FROM upgrade_members WHERE candidate_id=?1 AND media_id=?2";
const UPDATE_UPGRADE_CANDIDATES_4: &str =
    "UPDATE upgrade_candidates SET state=?2,finished_at=?3 WHERE candidate_id=?1";

/// What one decision does to every member of its candidate.
struct Verdict<'a> {
    decision: &'a UpgradeDecision,
    target: &'static str,
    /// When the replaced copy becomes eligible for cleanup.
    eligible: String,
    ts: String,
}

impl Verdict<'_> {
    fn keeps_both(&self) -> bool {
        matches!(self.decision.decision, UpgradeAction::KeepBoth)
    }

    fn rejects(&self) -> bool {
        matches!(self.decision.decision, UpgradeAction::Reject)
    }
}

pub async fn decide(core: &Core, decisions: Vec<UpgradeDecision>) -> Result<Value, CoreError> {
    if decisions.is_empty() || decisions.len() > 50 {
        return Err(CoreError::InvalidInput(
            "choose 1..50 explicit candidates".into(),
        ));
    }
    let _gate = core.archive_access.read().await;
    verify_candidate_objects(core, &decisions).await?;
    let (_, wait) = crate::quality::policy(core).await?;
    core.db
        .write(move |tx| {
            let mut results = Vec::new();
            for decision in &decisions {
                results.push(decide_one(tx, decision, wait)?);
            }
            Ok(json!({"items": results}))
        })
        .await
}

/// Revalidate the actual candidate objects before publishing references. A
/// missing or externally corrupted file cannot become the selected resource.
async fn verify_candidate_objects(
    core: &Core,
    decisions: &[UpgradeDecision],
) -> Result<(), CoreError> {
    for decision in decisions {
        if matches!(decision.decision, UpgradeAction::Reject) {
            continue;
        }
        let id = decision.candidate_id.clone();
        let blobs = core
            .db
            .read(move |c| {
                Ok(c.prepare(SELECT_UPGRADE_MEMBERS_6)?
                    .query_map([id], |r| r.get::<_, String>(0))?
                    .collect::<Result<Vec<_>, _>>()?)
            })
            .await?;
        for blob in blobs {
            let sha = crate::paths::blob_sha(&blob)?.to_owned();
            let path = core.objects.object_path(&sha)?;
            let actual = tokio::task::spawn_blocking(move || crate::objects::hash_file(&path))
                .await
                .map_err(|_| CoreError::Internal("candidate verification task".into()))??;
            if actual != sha {
                return Err(CoreError::InvalidInput(
                    "candidate object verification failed".into(),
                ));
            }
        }
    }
    Ok(())
}

fn decide_one(
    tx: &Transaction<'_>,
    decision: &UpgradeDecision,
    wait: i64,
) -> Result<Value, CoreError> {
    let id = &decision.candidate_id;
    let target = match decision.decision {
        UpgradeAction::Accept => "accepted",
        UpgradeAction::Reject => "rejected",
        UpgradeAction::KeepBoth => "kept",
    };
    let (state, expected, entry): (String, Option<String>, String) =
        tx.query_row(SELECT_UPGRADE_CANDIDATES_3, [id], |r| {
            Ok((r.get(0)?, r.get(1)?, r.get(2)?))
        })?;
    if state == target {
        return Ok(json!({"candidate_id": id, "state": state, "duplicate": true}));
    }
    if state != "pending" {
        return Err(CoreError::VersionConflict(
            "candidate is no longer pending".into(),
        ));
    }
    let expected: Value = expected
        .map(|s| serde_json::from_str(&s))
        .transpose()?
        .unwrap_or_default();
    if expected != decision.expected || !confirmation_current(tx, id, &expected)? {
        return Err(CoreError::VersionConflict(
            "upgrade comparison changed; refresh the comparison".into(),
        ));
    }
    let verdict = Verdict {
        decision,
        target,
        eligible: (time::OffsetDateTime::now_utc() + time::Duration::seconds(wait))
            .format(&time::format_description::well_known::Rfc3339)
            .map_err(|_| CoreError::Internal("clock".into()))?,
        ts: jobs::now(),
    };
    let members = tx
        .prepare(SELECT_UPGRADE_MEMBERS_3)?
        .query_map([id], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, Option<String>>(1)?,
                r.get::<_, String>(2)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    for (media, old, new) in members {
        if old.as_deref() != Some(new.as_str()) {
            apply_member(
                tx,
                &verdict,
                (id.as_str(), media.as_str()),
                old.as_deref(),
                &new,
            )?;
        }
    }
    tx.execute(
        UPDATE_UPGRADE_CANDIDATES_4,
        rusqlite::params![id, target, verdict.ts],
    )?;
    crate::objects::outbox_emit(
        tx,
        "entry.upgrade_changed",
        &entry,
        &json!({"candidate_id": id, "state": target}),
    )?;
    Ok(json!({"candidate_id": id, "state": target, "duplicate": false}))
}

/// Publishes (or rejects) the candidate's new variant for one media item and decides what
/// happens to the variant it replaces.
fn apply_member(
    tx: &Transaction<'_>,
    verdict: &Verdict<'_>,
    (candidate, media): (&str, &str),
    old: Option<&str>,
    new: &str,
) -> Result<(), CoreError> {
    let Verdict {
        target,
        eligible,
        ts,
        ..
    } = verdict;
    tx.execute(UPDATE_MEDIA_VARIANTS_2, rusqlite::params![new, target])?;
    if verdict.rejects() {
        tx.execute(
            INSERT_VARIANT_RETENTION_2,
            rusqlite::params![new, eligible, ts],
        )?;
        return Ok(());
    }
    tx.execute(INSERT_ASSET_SELECTIONS, rusqlite::params![media, new, ts])?;
    tx.execute(
        INSERT_VARIANT_RETENTION_3,
        rusqlite::params![new, verdict.keeps_both(), ts],
    )?;
    match old {
        Some(old) => retain_previous(tx, verdict, (candidate, media), (old, new)),
        None => Ok(()),
    }
}

/// Keep prior copies unless their measured tracks and duration prove comparability. Shared
/// and manually pinned bytes remain protected.
fn retain_previous(
    tx: &Transaction<'_>,
    verdict: &Verdict<'_>,
    (candidate, media): (&str, &str),
    (old, new): (&str, &str),
) -> Result<(), CoreError> {
    let (old_spec, new_spec): (Option<String>, Option<String>) =
        tx.query_row(SELECT_MEDIA_VARIANTS_2, rusqlite::params![old, new], |r| {
            Ok((r.get(0)?, r.get(1)?))
        })?;
    let comparable = old_spec.zip(new_spec).is_some_and(|(a, b)| {
        crate::quality::measured(&a)
            .zip(crate::quality::measured(&b))
            .is_some_and(|(a, b)| crate::quality::comparable(&a, &b))
    });
    let pinned: bool = tx.query_row(
        SELECT_UPGRADE_MEMBERS_7,
        rusqlite::params![candidate, media],
        |r| r.get(0),
    )?;
    let keep = pinned || verdict.keeps_both() || !comparable;
    tx.execute(
        INSERT_VARIANT_RETENTION,
        rusqlite::params![
            old,
            keep,
            pinned || verdict.keeps_both(),
            if keep {
                "user_preserved_previous"
            } else {
                "user_confirmed_replacement"
            },
            if keep { None } else { Some(&verdict.eligible) },
            verdict.ts
        ],
    )?;
    Ok(())
}
