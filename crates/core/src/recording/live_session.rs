//! Registering a live detection as a durable recording session. The first poll may lack
//! an upstream session identity, so identity can be upgraded in place later.
use super::invalid;
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::{self, ClaimedJob};
use rusqlite::{OptionalExtension, Transaction};
use serde_json::json;
use trace_contracts::plugin::LiveSnapshot;

const ROOM_ALREADY_RECORDING: &str = "SELECT EXISTS(SELECT 1 FROM live_sessions l JOIN entries e ON e.entry_id=l.entry_id JOIN subscriptions previous ON previous.subscription_id=l.subscription_id JOIN subscriptions current ON current.subscription_id=?1 JOIN source_accounts a ON a.account_id=current.account_id WHERE l.subscription_id!=?1 AND l.room_key=?2 AND e.namespace=a.namespace AND l.state IN ('waiting','recording','reconnecting','stopping') AND (previous.acquisition_kind='one_shot' OR current.acquisition_kind='one_shot'))";
const LOCAL_SESSION: &str = "SELECT session_id,entry_id,source_session FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND ended_at IS NULL AND source_session LIKE 'local-%' ORDER BY started_at DESC LIMIT 1";
const OPEN_SESSION_SOURCE: &str = "SELECT source_session FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1";
const INSERT_LIVE_ENTRY: &str = "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at,published_precision) SELECT ?1,a.namespace,'live',?2,a.account_id,?3,?3,'discovery' FROM source_accounts a JOIN subscriptions s ON s.account_id=a.account_id WHERE s.subscription_id=?4";
const INSERT_REVISION: &str = "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at,metadata_json) VALUES(?1,?2,?3,?4,'','full',?5,?6)";
const INSERT_SESSION: &str = "INSERT INTO live_sessions(session_id,subscription_id,room_key,source_session,entry_id,state,started_at,evidence) VALUES(?1,?2,?3,?4,?5,'waiting',?6,?7)";
const INSERT_RECORD_JOB: &str = "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'record',?3,?4,0,'queued',?5,?5)";

/// Returns the session that should record, or `None` when nothing should (offline, or the
/// user stopped this session).
pub async fn register(
    core: &Core,
    job: &ClaimedJob,
    snapshot: LiveSnapshot,
) -> Result<Option<String>, CoreError> {
    if !matches!(snapshot.state.as_str(), "live" | "offline" | "unknown")
        || snapshot.room_key.is_empty()
    {
        return Err(invalid("invalid live detection"));
    }
    if snapshot.state == "unknown" {
        return Err(CoreError::Network("live status unknown".into()));
    }
    let subscription = job.payload["subscription_id"]
        .as_str()
        .ok_or_else(|| invalid("live subscription"))?
        .to_owned();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            register_in(tx, &subscription, &snapshot)
        })
        .await
}

fn register_in(
    tx: &Transaction<'_>,
    subscription: &str,
    snapshot: &LiveSnapshot,
) -> Result<Option<String>, CoreError> {
    let now = jobs::now();
    if snapshot.state == "offline" {
        tx.execute(
            "UPDATE live_sessions SET state=CASE WHEN state='recording' THEN 'stopping' ELSE 'sealed' END,ended_at=?2,stop_reason='source_offline' WHERE subscription_id=?1 AND ended_at IS NULL",
            rusqlite::params![subscription, now],
        )?;
        return Ok(None);
    }
    let duplicate: bool = tx.query_row(
        ROOM_ALREADY_RECORDING,
        rusqlite::params![subscription, snapshot.room_key],
        |r| r.get(0),
    )?;
    if duplicate {
        return Err(CoreError::VersionConflict(
            "room already has an active recording".into(),
        ));
    }
    confirm_local_identity(tx, subscription, snapshot)?;
    let source = session_source(tx, subscription, snapshot)?;
    tx.execute(
        "UPDATE live_sessions SET state='stopping',ended_at=?4,stop_reason='source_session_replaced' WHERE subscription_id=?1 AND ended_at IS NULL AND (room_key!=?2 OR source_session!=?3)",
        rusqlite::params![subscription, snapshot.room_key, source, now],
    )?;
    let existing: Option<(String, i64)> = tx
        .query_row(
            "SELECT session_id,manual_stopped FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND source_session=?3",
            rusqlite::params![subscription, snapshot.room_key, source],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .optional()?;
    if let Some((id, stopped)) = existing {
        return Ok((stopped == 0).then_some(id));
    }
    create_session(tx, subscription, snapshot, &source, &now).map(Some)
}

/// The first poll may lack an upstream session identity. Upgrade the durable local
/// session in place; its entry, fragments and stop choice remain the same. A later
/// distinct upstream session closes the old one.
fn confirm_local_identity(
    tx: &Transaction<'_>,
    subscription: &str,
    snapshot: &LiveSnapshot,
) -> Result<(), CoreError> {
    let Some(source) = snapshot.source_session.as_deref() else {
        return Ok(());
    };
    let local: Option<(String, String, String)> = tx
        .query_row(
            LOCAL_SESSION,
            rusqlite::params![subscription, snapshot.room_key],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
        )
        .optional()?;
    let known: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND source_session=?3)",
        rusqlite::params![subscription, snapshot.room_key, source],
        |r| r.get(0),
    )?;
    if let Some((session, entry, prior)) = local.filter(|_| !known) {
        tx.execute(
            "UPDATE live_sessions SET source_session=?2,evidence=json_set(evidence,'$.identity_basis','source_after_local','$.prior_local_identity',?3,'$.source_session',?2) WHERE session_id=?1",
            rusqlite::params![session, source, prior],
        )?;
        tx.execute(
            "UPDATE entries SET native_entry_key=?2 WHERE entry_id=?1",
            rusqlite::params![entry, format!("{}:{source}", snapshot.room_key)],
        )?;
    }
    Ok(())
}

/// The upstream session if it names one, else the one already open, else a local identity.
fn session_source(
    tx: &Transaction<'_>,
    subscription: &str,
    snapshot: &LiveSnapshot,
) -> Result<String, CoreError> {
    if let Some(source) = &snapshot.source_session {
        return Ok(source.clone());
    }
    let open: Option<String> = tx
        .query_row(
            OPEN_SESSION_SOURCE,
            rusqlite::params![subscription, snapshot.room_key],
            |r| r.get(0),
        )
        .optional()
        .ok()
        .flatten();
    Ok(open.unwrap_or_else(|| format!("local-{}", uuid::Uuid::new_v4())))
}

/// A new session with its entry, first revision and the record job that will run it.
fn create_session(
    tx: &Transaction<'_>,
    subscription: &str,
    snapshot: &LiveSnapshot,
    source: &str,
    now: &str,
) -> Result<String, CoreError> {
    let session = format!("live-{}", uuid::Uuid::new_v4());
    let entry = format!("ent-{}", uuid::Uuid::new_v4());
    let revision = format!("rev-{}", uuid::Uuid::new_v4());
    let native_key = format!("{}:{source}", snapshot.room_key);
    tx.execute(
        INSERT_LIVE_ENTRY,
        rusqlite::params![entry, native_key, now, subscription],
    )?;
    let basis = if source.starts_with("local-") {
        "local_until_confirmed_offline"
    } else {
        "source"
    };
    let evidence = trace_contracts::content::safe_metadata(&json!({
        "room": snapshot.metadata,
        "source_session": snapshot.source_session,
        "identity_basis": basis,
    }))
    .to_string();
    tx.execute(
        INSERT_REVISION,
        rusqlite::params![
            revision,
            entry,
            crate::config::short_hash(source),
            snapshot.title,
            now,
            evidence
        ],
    )?;
    tx.execute(
        INSERT_SESSION,
        rusqlite::params![
            session,
            subscription,
            snapshot.room_key,
            source,
            entry,
            now,
            evidence
        ],
    )?;
    let payload = json!({"session_id": session, "subscription_id": subscription}).to_string();
    tx.execute(
        INSERT_RECORD_JOB,
        rusqlite::params![
            format!("job-{}", uuid::Uuid::new_v4()),
            format!("record:{session}"),
            session,
            payload,
            now
        ],
    )?;
    crate::objects::outbox_emit(
        tx,
        "entry.revision.created",
        &entry,
        &json!({"session_id": session}),
    )?;
    Ok(session)
}
