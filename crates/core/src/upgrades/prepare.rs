//! Candidate preparation: the comparison snapshot, member rows and their verification.

use super::*;

const SELECT_UPGRADE_MEMBERS: &str = "SELECT m.media_id,s.variant_id,COALESCE(s.pinned,0),m.resource_generation FROM upgrade_members u JOIN media_items m ON m.media_id=u.media_id LEFT JOIN asset_selections s ON s.media_id=m.media_id WHERE u.candidate_id=?1 ORDER BY m.media_id";

fn members(c: &Connection, id: &str) -> Result<Value, CoreError> {
    Ok(json!(
        c.prepare(SELECT_UPGRADE_MEMBERS)?
            .query_map([id], |r| Ok(json!({
                "media_id": r.get::<_, String>(0)?,
                "variant_id": r.get::<_, Option<String>>(1)?,
                "pinned": r.get::<_, bool>(2)?,
                "generation": r.get::<_, i64>(3)?,
            })))?
            .collect::<Result<Vec<_>, _>>()?
    ))
}
const SELECT_CURRENT_ENTRY_REVISIONS: &str = "SELECT u.revision_id,e.deletion_generation,s.config_version,k.generation,u.credential_id,(SELECT revision_id FROM current_entry_revisions WHERE entry_id=e.entry_id),e.lifecycle,e.media_suppressed,s.state,COALESCE(k.status,'anonymous') FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id LEFT JOIN subscriptions s ON s.subscription_id=u.subscription_id LEFT JOIN credentials_state k ON k.credential_id=u.credential_id WHERE u.candidate_id=?1";

pub(super) fn snapshot(c: &Connection, id: &str) -> Result<Value, CoreError> {
    let mut v = c.query_row(SELECT_CURRENT_ENTRY_REVISIONS, [id], |r| {
        Ok(json!({
            "revision_id": r.get::<_, String>(0)?,
            "deletion_generation": r.get::<_, i64>(1)?,
            "subscription_version": r.get::<_, Option<i64>>(2)?,
            "credential_generation": r.get::<_, Option<i64>>(3)?,
            "credential_id": r.get::<_, Option<String>>(4)?,
            "current_revision_id": r.get::<_, Option<String>>(5)?,
            "lifecycle": r.get::<_, String>(6)?,
            "media_suppressed": r.get::<_, bool>(7)?,
            "subscription_state": r.get::<_, Option<String>>(8)?,
            "credential_status": r.get::<_, String>(9)?,
        }))
    })?;
    let selected = members(c, id)?;
    use sha2::Digest;
    v["selection_digest"] = json!(
        sha2::Sha256::digest(serde_json::to_vec(&selected)?)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    );
    v["resource_count"] = json!(selected.as_array().map_or(0, Vec::len));
    v["policy"] = json!(policy(c)?);
    // A shared lease belongs to the executing consumer. Once its result is
    // complete, later consumers may rotate that lease without revoking it.
    // The originating request and its subscription/credential still must match.
    let (generation, request): (i64, Option<i64>) = c.query_row(
        "SELECT CASE WHEN u.consumer_request IS NULL THEN j.target_generation ELSE u.job_generation END,w.requested FROM upgrade_candidates u JOIN jobs j ON j.job_id=u.job_id LEFT JOIN job_consumers w ON w.job_id=u.job_id AND w.subscription_id=u.subscription_id AND u.consumer_request IS NOT NULL WHERE u.candidate_id=?1",
        [id], |r| Ok((r.get(0)?,r.get(1)?)))?;
    v["work_generation"] = json!(generation);
    // Keep schema-5 nonshared comparison tokens compatible. For a shared
    // candidate the field is present even if its consumer was removed, so
    // removing that consumer cannot accidentally match an older token.
    let shared: bool = c.query_row(
        "SELECT consumer_request IS NOT NULL FROM upgrade_candidates WHERE candidate_id=?1",
        [id],
        |r| r.get(0),
    )?;
    if shared {
        v["consumer_request"] = json!(request);
    }
    // New shared confirmations freeze the originating quality preference.
    // Older tokens keep their exact shape when this optional field is absent.
    let preference: Option<bool> = c.query_row(
        "SELECT json_extract(confirmation,'$.prefer_fps') FROM upgrade_candidates WHERE candidate_id=?1",
        [id],
        |r| r.get(0),
    )?;
    if let Some(prefer_fps) = preference {
        v["prefer_fps"] = json!(prefer_fps);
    }
    Ok(v)
}

const SELECT_UPGRADE_CANDIDATES: &str = "SELECT u.candidate_id FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id WHERE u.job_id=?1 AND u.job_generation=?2 AND u.revision_id=?3 AND u.group_key=?4 AND u.state='collecting' AND u.credential_id IS ?5 AND u.credential_generation IS ?6 AND u.subscription_id IS ?7 AND u.consumer_request IS ?8 AND u.policy IS ?9 AND u.deletion_generation=e.deletion_generation AND NOT EXISTS(SELECT 1 FROM upgrade_members x LEFT JOIN asset_selections s ON s.media_id=x.media_id WHERE x.candidate_id=u.candidate_id AND (s.variant_id IS NOT x.old_variant_id OR COALESCE(s.pinned,0)!=x.old_pinned)) ORDER BY u.created_at DESC LIMIT 1";
const INSERT_UPGRADE_CANDIDATES: &str = "INSERT INTO upgrade_candidates(candidate_id,entry_id,revision_id,group_key,context_id,job_id,job_generation,subscription_id,subscription_version,credential_id,credential_generation,deletion_generation,policy,created_at) SELECT ?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,e.deletion_generation,?12,?13 FROM entries e WHERE e.entry_id=?2";

const SELECT_ACQUISITION_CONTEXTS: &str = "SELECT auth_state,credential_id,credential_generation FROM acquisition_contexts WHERE context_id=?1";
const SELECT_MEDIA_ITEMS: &str = "SELECT EXISTS(SELECT 1 FROM media_items m JOIN asset_selections s ON s.media_id=m.media_id WHERE m.revision_id=?1 AND (m.group_key=?2 OR (m.group_key IS NULL AND m.source_attachment_id=?2)) AND NOT EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=s.variant_id AND x.job_id=?3 AND x.job_generation=?4 AND x.auth_state=?5 AND x.credential_id IS ?6 AND x.credential_generation IS ?7))";
const UPDATE_UPGRADE_CANDIDATES: &str = "UPDATE upgrade_candidates SET state='stale',reason='retry_scope_changed',finished_at=?3 WHERE job_id=?1 AND job_generation=?2 AND state='collecting'";
const INSERT_UPGRADE_MEMBERS: &str = "INSERT INTO upgrade_members(candidate_id,media_id,old_variant_id,required,old_pinned) SELECT ?1,m.media_id,s.variant_id,m.required,COALESCE(s.pinned,0) FROM media_items m LEFT JOIN asset_selections s ON s.media_id=m.media_id WHERE m.revision_id=?2 AND (m.group_key=?3 OR (m.group_key IS NULL AND m.source_attachment_id=?3))";
const UPDATE_UPGRADE_CANDIDATES_2: &str =
    "UPDATE upgrade_candidates SET consumer_request=?2 WHERE candidate_id=?1";

pub async fn prepare(
    core: &Core,
    job: &ClaimedJob,
    revision: &str,
    group: &str,
) -> Result<Option<String>, CoreError> {
    let Some(context) = job.payload["acquisition_context_id"]
        .as_str()
        .map(str::to_owned)
    else {
        return Ok(None);
    };
    let (guard, revision, group) = (job.clone(), revision.to_owned(), group.to_owned());
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let (auth, credential, generation): (String, Option<String>, Option<i64>) = tx
                .query_row(SELECT_ACQUISITION_CONTEXTS, [&context], |r| {
                    Ok((r.get(0)?, r.get(1)?, r.get(2)?))
                })?;
            // A retry completing this same demand may reuse its own committed
            // resources. A previous demand or authentication scope needs a decision.
            let has_old: bool = tx.query_row(
                SELECT_MEDIA_ITEMS,
                rusqlite::params![
                    revision,
                    group,
                    guard.job_id,
                    guard.generation,
                    auth,
                    credential,
                    generation
                ],
                |r| r.get(0),
            )?;
            if guard.payload["completion"] != true
                && !((auth == "authenticated" || guard.payload["source_request_id"].is_string())
                    && has_old)
            {
                return Ok(None);
            }
            let prior: Option<String> = tx
                .query_row(
                    SELECT_UPGRADE_CANDIDATES,
                    rusqlite::params![
                        guard.job_id,
                        guard.generation,
                        revision,
                        group,
                        credential,
                        generation,
                        guard.payload["subscription_id"].as_str(),
                        guard.payload["consumer_request"].as_i64(),
                        policy(tx)?
                    ],
                    |r| r.get(0),
                )
                .optional()?;
            if let Some(id) = prior {
                return Ok(Some(id));
            }
            tx.execute(
                UPDATE_UPGRADE_CANDIDATES,
                rusqlite::params![guard.job_id, guard.generation, jobs::now()],
            )?;
            let id = format!("upgrade-{}", uuid::Uuid::new_v4());
            let entry: String = tx.query_row(
                "SELECT entry_id FROM entry_revisions WHERE revision_id=?1",
                [&revision],
                |r| r.get(0),
            )?;
            tx.execute(
                INSERT_UPGRADE_CANDIDATES,
                rusqlite::params![
                    id,
                    entry,
                    revision,
                    group,
                    context,
                    guard.job_id,
                    guard.generation,
                    guard.payload["subscription_id"].as_str(),
                    guard.payload["execution_scope"]["subscription_version"].as_i64(),
                    credential,
                    generation,
                    policy(tx)?,
                    jobs::now()
                ],
            )?;
            tx.execute(
                INSERT_UPGRADE_MEMBERS,
                rusqlite::params![id, revision, group],
            )?;
            tx.execute(
                UPDATE_UPGRADE_CANDIDATES_2,
                rusqlite::params![id, guard.payload["consumer_request"].as_i64()],
            )?;
            Ok(Some(id))
        })
        .await
}

pub fn variant(
    tx: &Transaction<'_>,
    candidate: &str,
    media: &str,
    variant: &str,
) -> Result<(), CoreError> {
    let n=tx.execute("UPDATE upgrade_members SET new_variant_id=?3 WHERE candidate_id=?1 AND media_id=?2 AND EXISTS(SELECT 1 FROM upgrade_candidates WHERE candidate_id=?1 AND state='collecting')",rusqlite::params![candidate,media,variant])?;
    if n != 1 {
        return Err(CoreError::Cancelled);
    }
    Ok(())
}

const SELECT_MEDIA_VARIANTS: &str = "SELECT v.variant_id,COALESCE(v.measured_spec,'{}') FROM media_variants v WHERE v.media_id=?1 AND v.variant_id=?2 AND (v.approval_state IN ('automatic','accepted','kept') OR (v.approval_state='pending' AND EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id JOIN acquisition_contexts current ON current.context_id=?5 WHERE a.variant_id=v.variant_id AND x.job_id=?3 AND x.job_generation=?4 AND x.auth_state=current.auth_state AND x.credential_id IS current.credential_id AND x.credential_generation IS current.credential_generation))) AND NOT EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=v.variant_id)";

pub async fn reused(
    core: &Core,
    job: &ClaimedJob,
    media: &str,
    variant_id: &str,
    same_provenance: bool,
) -> Result<(), CoreError> {
    let (job, media, variant_id) = (job.clone(), media.to_owned(), variant_id.to_owned());
    core.db
        .write(move |tx| {
            job.check(tx)?;
            let (id, spec): (String, String) = tx.query_row(
                SELECT_MEDIA_VARIANTS,
                rusqlite::params![
                    media,
                    variant_id,
                    job.job_id,
                    job.generation,
                    job.payload["acquisition_context_id"].as_str()
                ],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?;
            if let Some(original) = job.payload["acquisition_context_id"].as_str() {
                let context = if same_provenance {
                    original.to_owned()
                } else {
                    let unknown = trace_contracts::acquisition::AcquisitionContext {
                        context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
                        auth_state: trace_contracts::acquisition::AuthenticationState::Unknown,
                        credential_id: None,
                        credential_generation: None,
                        validated_at: None,
                        basis: "legacy_cached_copy_verified".into(),
                        started_at: jobs::now(),
                    };
                    crate::acquisitions::insert_context(tx, &job, &job.subject_id, &unknown)?;
                    unknown.context_id
                };
                crate::acquisitions::record(
                    tx,
                    &context,
                    Some(&media),
                    Some(&id),
                    "cache_verified",
                    &serde_json::from_str(&spec)?,
                )?;
            }
            if let Some(candidate) = job.payload["upgrade_candidate_id"].as_str() {
                variant(tx, candidate, &media, &id)?;
            }
            Ok(())
        })
        .await
}

pub(super) fn complete(c: &Connection, id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM upgrade_members WHERE candidate_id=?1) AND NOT EXISTS(SELECT 1 FROM upgrade_members u WHERE candidate_id=?1 AND required=1 AND (new_variant_id IS NULL OR NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas r ON r.blob_id=v.blob_id WHERE v.variant_id=u.new_variant_id AND r.state='verified') OR EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=u.new_variant_id)))",[id],|r|r.get(0))?)
}

pub(super) fn confirmation_current(
    c: &Connection,
    id: &str,
    expected: &Value,
) -> Result<bool, CoreError> {
    let now = snapshot(c, id)?;
    let stopped:bool=c.query_row("SELECT EXISTS(SELECT 1 FROM upgrade_candidates u JOIN jobs j ON j.job_id=u.job_id LEFT JOIN jobs parent ON parent.job_id=json_extract(j.payload,'$.completion_parent') WHERE u.candidate_id=?1 AND (j.state='cancelled' OR parent.state='cancelled')) OR EXISTS(SELECT 1 FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id JOIN plugins_state p ON p.platform=e.namespace WHERE u.candidate_id=?1 AND p.enabled=0)",[id],|r|r.get(0))?;
    Ok(*expected == now
        && !stopped
        && now["lifecycle"] != "deleted"
        && now["media_suppressed"] != true
        && now["revision_id"] == now["current_revision_id"]
        && now["subscription_state"]
            .as_str()
            .is_none_or(|s| s == "enabled")
        && matches!(
            now["credential_status"].as_str(),
            Some("valid" | "authenticated" | "anonymous")
        )
        && complete(c, id)?)
}

const SELECT_UPGRADE_CANDIDATES_2: &str = "SELECT candidate_id,confirmation FROM upgrade_candidates WHERE entry_id=?1 AND state='pending'";

pub fn has_pending(c: &Connection, entry: &str) -> Result<bool, CoreError> {
    let pending = c
        .prepare_cached(SELECT_UPGRADE_CANDIDATES_2)?
        .query_map([entry], |r| {
            Ok((r.get::<_, String>(0)?, r.get::<_, Option<String>>(1)?))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    for (id, expected) in pending {
        if let Some(expected) = expected.and_then(|s| serde_json::from_str::<Value>(&s).ok())
            && confirmation_current(c, &id, &expected)?
        {
            return Ok(true);
        }
    }
    Ok(false)
}
