//! Detail observation (C02): identity stays stable, revisions are appended, a summary never
//! replaces a full revision, and the work a revision needs is queued in the same transaction.
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::{self, ClaimedJob};
use crate::scan_range::Placement;
use crate::store::{self, entries::Source};
use rusqlite::Transaction;
use serde_json::{Value, json};
use std::collections::BTreeSet;
use trace_contracts::content::Block;
use trace_contracts::plugin::{ObservedDetail, ResourceIdentityDto, methods};
use trace_contracts::unified::StatCounters;
use trace_domain::revision::{
    Completeness, ContentObservation, ResourceIdentity, RevisionDecision, decide_revision_digest,
    semantic_digest,
};
use trace_domain::search::normalize_v1;

use super::standing;

const OBSERVE_DEADLINE_MS: u64 = 30_000;
const DOCUMENT_BLOCKS_MAX: usize = 8192;
const DOCUMENT_BYTES_MAX: usize = 512 * 1024;

pub(crate) async fn run_observe(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let entry_id = job
        .payload
        .get("entry_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| CoreError::InvalidInput("payload".into()))?
        .to_string();
    let e = entry_id.clone();
    let Source {
        namespace,
        native_key,
        account_id,
        native_kind,
    } = core.db.read(move |c| store::entries::source(c, &e)).await?;
    let platform = namespace.split(':').next().unwrap_or("").to_string();
    let context = crate::acquisitions::begin(core, job, &platform, &entry_id).await?;
    crate::acquisitions::require_access(&context)?;
    let job = &with_context(job, &context.context_id);
    core.metadata_pacing(&namespace).await;
    let value = core
        .adapter_call(
            job,
            &platform,
            methods::OBSERVE,
            json!({"native_entry_key": native_key, "native_kind": native_kind}),
            Some(OBSERVE_DEADLINE_MS),
        )
        .await?;
    let detail = checked_detail(value, &native_key, &native_kind)?;
    let context =
        crate::acquisitions::resolution_context(core, job, &entry_id, context, &detail.metadata)
            .await?;
    let resolved = with_context(job, &context.context_id);
    persist_detail(core, &resolved, &entry_id, &account_id, detail).await
}

/// The job as it runs under one acquisition context.
pub(crate) fn with_context(job: &ClaimedJob, context_id: &str) -> ClaimedJob {
    let mut job = job.clone();
    job.payload["acquisition_context_id"] = Value::String(context_id.to_owned());
    job
}

/// Parses what the adapter returned, rejects another identity or an oversized document,
/// and reduces every free-form field to safe metadata.
fn checked_detail(
    value: Value,
    native_key: &str,
    native_kind: &str,
) -> Result<ObservedDetail, CoreError> {
    let mut detail: ObservedDetail = serde_json::from_value(value)
        .map_err(|e| CoreError::InvalidInput(format!("bad detail: {e}")))?;
    if detail.native_entry_key != native_key || detail.native_kind != native_kind {
        return Err(CoreError::InvalidInput(
            "observation identity mismatch".into(),
        ));
    }
    detail.metadata = trace_contracts::content::safe_metadata(&detail.metadata);
    detail.stats = trace_contracts::content::safe_metadata(&detail.stats);
    detail.source_version_evidence =
        trace_contracts::content::safe_metadata(&detail.source_version_evidence);
    detail
        .document
        .validate(&detail.resources)
        .map_err(|e| CoreError::InvalidInput(e.into()))?;
    if detail.document.blocks.len() > DOCUMENT_BLOCKS_MAX
        || serde_json::to_vec(&detail.document)?.len() > DOCUMENT_BYTES_MAX
    {
        return Err(CoreError::InvalidInput("document budget exceeded".into()));
    }
    Ok(detail)
}

/// A revision, its observation and required work are committed together. A crash
/// cannot leave a full revision with no queued resource acquisition.
pub(crate) async fn persist_detail(
    core: &Core,
    job: &ClaimedJob,
    entry_id: &str,
    account_id: &str,
    detail: ObservedDetail,
) -> Result<(), CoreError> {
    let (entry, account, guard) = (entry_id.to_owned(), account_id.to_owned(), job.clone());
    let tools_missing = needs_missing_tool(core, entry_id).await?;
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let (revision, digest) = choose_revision(tx, &entry, &detail)?;
            let now = jobs::now();
            store::entries::apply_published(
                tx,
                &entry,
                detail.published_at.as_deref(),
                &detail.published_precision,
            )?;
            let standing = standing::settle(tx, &guard, &entry)?;
            if let Some(revision) = &revision {
                write_revision(tx, &entry, revision, &digest, &detail, &now)?;
                let queue = Queue {
                    job: &guard,
                    entry: &entry,
                    account: &account,
                    revision,
                    tools_missing,
                    standing,
                };
                queue_media(tx, &queue, &detail, &now)?;
            }
            record_metadata(tx, &entry, revision.as_deref(), &detail, &now)?;
            write_observation(
                tx,
                &guard,
                &entry,
                revision.as_deref(),
                &detail,
                standing,
                &now,
            )
        })
        .await
}

/// Whether the entry's site needs a media tool that cannot be used.
async fn needs_missing_tool(core: &Core, entry_id: &str) -> Result<bool, CoreError> {
    let entry = entry_id.to_owned();
    let namespace: String = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT namespace FROM entries WHERE entry_id=?1",
                [entry],
                |r| r.get(0),
            )?)
        })
        .await?;
    let platform = namespace.split(':').next().unwrap_or("");
    Ok(crate::media_tools::tool_for(platform).is_some() && !core.media_tools.ready().await)
}

/// The unified projection of a stored revision. A summary ignored in favor of a full
/// revision still contributes the counters it saw, but never replaces the content fields.
fn record_metadata(
    tx: &Transaction<'_>,
    entry: &str,
    revision: Option<&str>,
    detail: &ObservedDetail,
    now: &str,
) -> Result<(), CoreError> {
    let namespace: String = tx.query_row(
        "SELECT namespace FROM entries WHERE entry_id=?1",
        [entry],
        |r| r.get(0),
    )?;
    let site = namespace.split(':').next().unwrap_or("");
    // An adapter that does not map its own fields leaves them to what the stored record says.
    let mut unified = match &detail.unified {
        Some(unified) => unified.clone().sanitized(site),
        None => crate::legacy_metadata::project(
            site,
            &detail.native_kind,
            &detail.native_entry_key,
            &detail.metadata,
            &detail.stats,
        )
        .unwrap_or_default(),
    };
    if unified.stats.is_empty() {
        unified.stats = StatCounters::from_value(&detail.stats);
    }
    let Some(revision) = revision else {
        return store::meta::apply_stats(tx, entry, &unified.stats, now);
    };
    let has_text = !detail.normalized_body.trim().is_empty();
    store::meta::record_observed(
        tx,
        &store::meta::Observed {
            entry_id: entry,
            site,
            revision_id: revision,
            facts: store::meta::media_facts(&detail.resources, has_text),
            unified,
            now,
        },
    )
}

fn completeness_of(text: &str) -> Completeness {
    if text == "full" {
        Completeness::Full
    } else {
        Completeness::Summary
    }
}

/// The digest of the observed content. A document that is more than plain paragraphs of
/// the normalized body also digests its blocks, so a layout change is a new revision.
fn revision_digest(
    incoming: &ContentObservation,
    detail: &ObservedDetail,
) -> Result<String, CoreError> {
    use sha2::Digest;
    let base = semantic_digest(
        &incoming.title,
        &incoming.normalized_body,
        &incoming.resources,
    );
    let blocks = &detail.document.blocks;
    let paragraphs = blocks
        .iter()
        .map(|b| match b {
            Block::Text { text, style } if style == "paragraph" => Some(text.as_str()),
            _ => None,
        })
        .collect::<Option<Vec<_>>>();
    let plain =
        paragraphs.is_some_and(|text| normalize_v1(&text.join("\n")) == incoming.normalized_body);
    if blocks.is_empty() || plain {
        return Ok(base);
    }
    let mut hash = sha2::Sha256::new();
    hash.update(b"trace-document-v1\0");
    hash.update(base.as_bytes());
    hash.update(serde_json::to_vec(blocks)?);
    Ok(hash.finalize().iter().map(|b| format!("{b:02x}")).collect())
}

/// The revision to write (new or reused) and its digest; `None` keeps a full revision over a summary.
fn choose_revision(
    tx: &Transaction<'_>,
    entry: &str,
    detail: &ObservedDetail,
) -> Result<(Option<String>, String), CoreError> {
    let resources = detail
        .resources
        .iter()
        .map(|r| ResourceIdentity {
            role: r.role.clone(),
            source_attachment_id: r.source_attachment_id.clone(),
            position: r.position,
        })
        .collect::<Vec<_>>();
    let incoming = ContentObservation {
        title: detail.title.clone(),
        normalized_body: normalize_v1(&detail.normalized_body),
        resources,
        completeness: completeness_of(&detail.completeness),
        source_version_evidence: None,
    };
    let existing = tx
        .prepare("SELECT revision_id,completeness,semantic_digest FROM entry_revisions WHERE entry_id=?1")?
        .query_map([entry], |r| {
            Ok((
                r.get::<_, String>(0)?,
                completeness_of(&r.get::<_, String>(1)?),
                r.get::<_, String>(2)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let digest = revision_digest(&incoming, detail)?;
    let revision = match decide_revision_digest(&existing, incoming.completeness, &digest) {
        RevisionDecision::Reuse { revision_id } => Some(revision_id),
        RevisionDecision::CreateNew => Some(format!("rev-{}", uuid::Uuid::new_v4())),
        RevisionDecision::KeepExistingIgnoreSummary { .. } => None,
    };
    Ok((revision, digest))
}

/// A reused summary upgrades to full in place; nothing else about a stored revision changes.
fn write_revision(
    tx: &Transaction<'_>,
    entry: &str,
    revision: &str,
    digest: &str,
    detail: &ObservedDetail,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,source_version_evidence,completeness,observed_at,document_json,metadata_json) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10) ON CONFLICT(revision_id) DO UPDATE SET completeness=excluded.completeness,document_json=excluded.document_json,metadata_json=excluded.metadata_json,source_version_evidence=excluded.source_version_evidence WHERE entry_revisions.completeness!='full' AND excluded.completeness='full'",
        rusqlite::params![
            revision,
            entry,
            digest,
            detail.title,
            detail.normalized_body,
            detail.source_version_evidence.to_string(),
            detail.completeness,
            now,
            serde_json::to_string(&detail.document)?,
            detail.metadata.to_string()
        ],
    )?;
    for r in &detail.resources {
        tx.execute(
            "INSERT OR IGNORE INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,title,required) VALUES(?1,?2,?3,?4,?5,?6,?7,?8)",
            rusqlite::params![
                format!("med-{}", uuid::Uuid::new_v4()),
                revision,
                r.role,
                r.position,
                r.source_attachment_id,
                r.group_key,
                r.title,
                r.required as i64
            ],
        )?;
    }
    Ok(())
}

/// What the media jobs of one observed revision are queued for.
struct Queue<'a> {
    job: &'a ClaimedJob,
    entry: &'a str,
    account: &'a str,
    revision: &'a str,
    /// The tool that answers for this entry's video formats is not available.
    tools_missing: bool,
    /// Where the entry stands against the range of the subscription that found it (§5.3);
    /// `None` when no range applies.
    standing: Option<Placement>,
}

/// One media job per resource group, unless the entry's media is suppressed or deleted.
fn queue_media(
    tx: &Transaction<'_>,
    q: &Queue<'_>,
    detail: &ObservedDetail,
    now: &str,
) -> Result<(), CoreError> {
    let suppressed: bool = tx.query_row(
        "SELECT media_suppressed!=0 OR lifecycle='deleted' FROM entries WHERE entry_id=?1",
        [q.entry],
        |r| r.get(0),
    )?;
    if suppressed {
        return Ok(());
    }
    if q.standing == Some(Placement::Outside) {
        return standing::leave_undownloaded(tx, q.entry, q.revision);
    }
    let groups = detail
        .resources
        .iter()
        .map(group_of)
        .collect::<BTreeSet<_>>();
    for group in groups {
        if defers_video(tx, q, &group, detail)? {
            continue;
        }
        if requested(q.job) {
            store::media_sync::mark_wanted(tx, q.revision, &group)?;
        }
        let payload = json!({
            "entry_id": q.entry,
            "revision_id": q.revision,
            "group": group,
            "account_id": q.account,
            "subscription_id": q.job.payload.get("subscription_id"),
        });
        queue_group(tx, q.job, q.entry, q.revision, &group, payload, now)?;
    }
    Ok(())
}

/// A link or a preview selection asked for this content: its videos are fetched whatever the
/// subscription setting says.
fn requested(job: &ClaimedJob) -> bool {
    job.payload["source_request_id"].is_string() || job.payload["delivery_id"].is_string()
}

const MEDIA_IN_GROUP: &str = "revision_id=?1 AND COALESCE(group_key,source_attachment_id)=?2";

/// A video that a subscription without auto-download discovers is kept as metadata and left
/// undownloaded; so is one whose format tool is missing, since there is nothing to fetch it
/// with (the sync command queues it once the tool is back). Only discovery decides: a group
/// already left undownloaded stays so when the switch is turned on later, and a saved one is
/// never touched. Explicit requests (a link, an authentication completion, a recheck without
/// subscription) always run, and report a missing tool as their failure.
fn defers_video(
    tx: &Transaction<'_>,
    q: &Queue<'_>,
    group: &str,
    detail: &ObservedDetail,
) -> Result<bool, CoreError> {
    let (job, revision) = (q.job, q.revision);
    let explicit = requested(job) || job.payload["completion"] == true;
    let Some(subscription) = job.payload["subscription_id"]
        .as_str()
        .filter(|_| !explicit)
    else {
        return Ok(false);
    };
    let video = detail
        .resources
        .iter()
        .any(|r| r.role == "video" && group_of(r) == group);
    if !video {
        return Ok(false);
    }
    let saved: bool = tx.query_row(
        &format!("SELECT EXISTS(SELECT 1 FROM media_items m JOIN media_variants v ON v.media_id=m.media_id WHERE m.{MEDIA_IN_GROUP})"),
        rusqlite::params![revision, group],
        |r| r.get(0),
    )?;
    if saved {
        return Ok(false);
    }
    let left: bool = tx.query_row(
        &format!("SELECT EXISTS(SELECT 1 FROM media_items WHERE {MEDIA_IN_GROUP} AND fetch_state='deferred')"),
        rusqlite::params![revision, group],
        |r| r.get(0),
    )?;
    if left {
        return Ok(true);
    }
    // An entry the details could not date gets no automatic video download (§5.3).
    let dated = q.standing != Some(Placement::Unplaced);
    if dated
        && !q.tools_missing
        && store::subscriptions::auto_download(tx, subscription)? != Some(false)
    {
        return Ok(false);
    }
    tx.execute(
        &format!("UPDATE media_items SET fetch_state='deferred' WHERE {MEDIA_IN_GROUP} AND fetch_state='wanted'"),
        rusqlite::params![revision, group],
    )?;
    Ok(true)
}

fn group_of(r: &ResourceIdentityDto) -> String {
    r.group_key
        .clone()
        .unwrap_or_else(|| r.source_attachment_id.clone())
}

/// Who asked for the media decides how its job is queued: a link request, an authentication
/// completion, a subscription consumer, or an explicit archive recheck without a subscription.
fn queue_group(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    entry: &str,
    revision: &str,
    group: &str,
    mut payload: Value,
    now: &str,
) -> Result<(), CoreError> {
    if let Some(source) = job.payload["source_request_id"].as_str() {
        payload["source_request_id"] = json!(source);
        payload["source_request_generation"] = job.payload["source_request_generation"].clone();
        if let Some(delivery) = job.payload["delivery_id"].as_str() {
            payload["delivery_id"] = json!(delivery);
        }
        tx.execute(
            "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,'queued',1,?5,?5)",
            rusqlite::params![
                format!("job-{}", uuid::Uuid::new_v4()),
                format!("source-media:{source}:{revision}:{group}"),
                entry,
                payload.to_string(),
                now
            ],
        )?;
    } else if let Some(delivery) = job.payload["delivery_id"].as_str() {
        // A download request owns its jobs: they are not shared with any subscription.
        payload["delivery_id"] = json!(delivery);
        tx.execute(
            "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,'queued',1,?5,?5)",
            rusqlite::params![
                format!("job-{}", uuid::Uuid::new_v4()),
                format!("delivery-media:{delivery}:{revision}:{group}"),
                entry,
                payload.to_string(),
                now
            ],
        )?;
    } else if job.payload["completion"] == true {
        crate::upgrades::schedule_media(tx, job, payload, entry, revision, group)?;
    } else if let Some(subscription) = job.payload["subscription_id"].as_str() {
        jobs::consumers::discover(tx, entry, subscription)?;
        let key = format!("media:{revision}:{group}");
        jobs::consumers::request(tx, &key, "media", entry, payload, subscription)?;
    } else {
        tx.execute(
            "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,target_generation,priority,state,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,1,1,'queued',?5,?5) ON CONFLICT(job_key) DO UPDATE SET state='queued',target_generation=jobs.target_generation+1,next_run_at=excluded.updated_at,updated_at=excluded.updated_at WHERE jobs.state='succeeded'",
            rusqlite::params![
                format!("job-{}", uuid::Uuid::new_v4()),
                format!("media:{revision}:{group}"),
                entry,
                payload.to_string(),
                now
            ],
        )?;
    }
    Ok(())
}

fn write_observation(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    entry: &str,
    revision: Option<&str>,
    detail: &ObservedDetail,
    standing: Option<Placement>,
    now: &str,
) -> Result<(), CoreError> {
    let mut evidence = json!({
        "stats": detail.stats,
        "metadata": detail.metadata,
        "source": detail.source_version_evidence,
    });
    if let Some(standing) = standing {
        evidence["range"] = json!(match standing {
            Placement::Inside => "inside",
            Placement::Outside => "outside",
            Placement::Unplaced => "unplaced",
        });
    }
    tx.execute(
        "INSERT INTO source_observations(observation_id,entry_id,revision_id,access_state,observed_at,observation_seq,evidence) VALUES(?1,?2,?3,?4,?5,COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1),?6)",
        rusqlite::params![
            format!("obs-{}", uuid::Uuid::new_v4()),
            entry,
            revision,
            detail.access_state,
            now,
            evidence.to_string()
        ],
    )?;
    if let Some(context) = job.payload["acquisition_context_id"].as_str() {
        tx.execute(
            "UPDATE source_observations SET context_id=?1 WHERE rowid=last_insert_rowid()",
            [context],
        )?;
        crate::acquisitions::record(tx, context, None, None, "observed", &Value::Null)?;
    }
    let payload = json!({"revision_id": revision});
    crate::objects::outbox_emit(tx, "entry.revision.created", entry, &payload)
}
