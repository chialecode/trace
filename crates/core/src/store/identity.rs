//! Explicit creator relations, profile history and identity candidates (stage plan §11.1/§11.2).
//!
//! Merging, splitting and relinking change only `account_bindings`, `creator_redirects` and the
//! audit rows: entries, revisions, blobs, replicas and file paths are never touched.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};

pub fn creator_exists(c: &Connection, id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM creators WHERE creator_id=?1)",
        [id],
        |r| r.get(0),
    )?)
}

pub fn creator_status(c: &Connection, id: &str) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT status FROM creators WHERE creator_id=?1",
        [id],
        |r| r.get(0),
    )
    .optional()?)
}

/// One level of redirection, as written by a merge; a chain is refused at write time.
pub fn redirect_target(c: &Connection, id: &str) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT target_creator_id FROM creator_redirects WHERE source_creator_id=?1",
        [id],
        |r| r.get(0),
    )
    .optional()?)
}

/// The id a caller should act on: one redirect step, or the id itself.
pub fn resolve(c: &Connection, id: &str) -> Result<String, CoreError> {
    Ok(redirect_target(c, id)?.unwrap_or_else(|| id.to_owned()))
}

/// Counts the user confirms before a merge: accounts, their subscriptions and their entries.
pub fn scope(c: &Connection, creator_id: &str) -> Result<Value, CoreError> {
    let (accounts, subscriptions, entries): (i64, i64, i64) = c.query_row(
        "SELECT (SELECT count(*) FROM account_bindings b WHERE b.creator_id=?1 AND b.state='active'),
                (SELECT count(*) FROM subscriptions s JOIN account_bindings b ON b.account_id=s.account_id WHERE b.creator_id=?1 AND b.state='active' AND s.acquisition_kind='subscription'),
                (SELECT count(*) FROM entries e JOIN account_bindings b ON b.account_id=e.account_id WHERE b.creator_id=?1 AND b.state='active')",
        [creator_id],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
    )?;
    Ok(json!({"accounts": accounts, "subscriptions": subscriptions, "entries": entries}))
}

/// The visible scope of one bound account: its creator, subscriptions and entries.
pub fn account_scope(c: &Connection, account_id: &str) -> Result<Value, CoreError> {
    c.query_row(
        "SELECT COALESCE((SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'),''),
                (SELECT count(*) FROM subscriptions WHERE account_id=?1 AND acquisition_kind='subscription'),
                (SELECT count(*) FROM entries WHERE account_id=?1 AND lifecycle!='deleted')",
        [account_id],
        |r| {
            Ok(json!({
                "account_id": account_id,
                "creator_id": r.get::<_, String>(0)?,
                "subscriptions": r.get::<_, i64>(1)?,
                "entries": r.get::<_, i64>(2)?,
            }))
        },
    )
    .map_err(CoreError::from)
}

pub fn bound_accounts(c: &Connection, creator_id: &str) -> Result<Vec<Value>, CoreError> {
    let mut stmt =
        c.prepare("SELECT account_id FROM account_bindings WHERE creator_id=?1 AND state='active' ORDER BY account_id")?;
    let ids = stmt
        .query_map([creator_id], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    ids.into_iter().map(|id| account_scope(c, &id)).collect()
}

pub fn record_operation(
    tx: &Transaction<'_>,
    kind: &str,
    source: Option<&str>,
    target: Option<&str>,
    account_ids: &[String],
    counts: &Value,
    now: &str,
) -> Result<String, CoreError> {
    let id = format!("crop-{}", uuid::Uuid::new_v4());
    tx.execute(
        "INSERT INTO creator_operations(operation_id,kind,source_creator_id,target_creator_id,account_ids,counts,created_at) VALUES(?1,?2,?3,?4,?5,?6,?7)",
        rusqlite::params![
            id,
            kind,
            source,
            target,
            serde_json::to_string(account_ids)?,
            counts.to_string(),
            now
        ],
    )?;
    Ok(id)
}

/// Moves every active binding of `source` to `target`, marks the source merged and records the
/// redirect. Refuses self-merges, merges of an already merged creator and any chain.
pub fn merge(
    tx: &Transaction<'_>,
    source: &str,
    target: &str,
    now: &str,
) -> Result<Value, CoreError> {
    if source == target {
        return Err(CoreError::InvalidInput(
            "cannot merge a creator into itself".into(),
        ));
    }
    if creator_status(tx, source)?.as_deref() != Some("active") {
        return Err(CoreError::InvalidInput(
            "source creator is not active".into(),
        ));
    }
    if creator_status(tx, target)?.as_deref() != Some("active") {
        return Err(CoreError::InvalidInput(
            "target creator is not active".into(),
        ));
    }
    if redirect_target(tx, target)?.is_some() {
        return Err(CoreError::InvalidInput(
            "target creator is itself merged; merge into its target".into(),
        ));
    }
    if redirect_target(tx, source)?.is_some() {
        return Err(CoreError::InvalidInput(
            "source creator is already merged".into(),
        ));
    }
    let has_incoming: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM creator_redirects WHERE target_creator_id=?1)",
        [source],
        |r| r.get(0),
    )?;
    if has_incoming {
        return Err(CoreError::InvalidInput(
            "source creator is a redirect target; merging it would build a chain".into(),
        ));
    }
    let before = scope(tx, source)?;
    let accounts: Vec<String> = tx
        .prepare("SELECT account_id FROM account_bindings WHERE creator_id=?1 AND state='active'")?
        .query_map([source], |r| r.get(0))?
        .collect::<Result<Vec<_>, _>>()?;
    tx.execute(
        "UPDATE account_bindings SET creator_id=?2 WHERE creator_id=?1 AND state='active'",
        rusqlite::params![source, target],
    )?;
    tx.execute(
        "UPDATE creators SET status='merged',updated_at=?2 WHERE creator_id=?1",
        rusqlite::params![source, now],
    )?;
    tx.execute(
        "INSERT INTO creator_redirects(source_creator_id,target_creator_id,created_at) VALUES(?1,?2,?3)",
        rusqlite::params![source, target, now],
    )?;
    let counts = json!({"accounts_moved": accounts.len(), "before": before});
    let operation = record_operation(
        tx,
        "merge",
        Some(source),
        Some(target),
        &accounts,
        &counts,
        now,
    )?;
    crate::objects::outbox_emit(
        tx,
        "creator.changed",
        source,
        &json!({"merged_into": target}),
    )?;
    crate::objects::outbox_emit(
        tx,
        "creator.changed",
        target,
        &json!({"merged_from": source}),
    )?;
    Ok(
        json!({"operation_id": operation, "merged_into": target, "accounts": accounts, "counts": counts}),
    )
}

/// Creates a new creator and moves the selected active bindings of `source` to it.
pub fn split(
    tx: &Transaction<'_>,
    source: &str,
    account_ids: &[String],
    new_creator_id: &str,
    display_name: &str,
    now: &str,
) -> Result<Value, CoreError> {
    if account_ids.is_empty() {
        return Err(CoreError::InvalidInput(
            "select at least one account".into(),
        ));
    }
    if creator_status(tx, source)?.as_deref() != Some("active") {
        return Err(CoreError::InvalidInput(
            "source creator is not active".into(),
        ));
    }
    let mut moved = Vec::new();
    for account in account_ids {
        let owner: Option<String> = tx
            .query_row(
                "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
                [account],
                |r| r.get(0),
            )
            .optional()?;
        match owner.as_deref() {
            Some(id) if id == source => moved.push(account.clone()),
            Some(_) => {
                return Err(CoreError::VersionConflict(format!(
                    "account {account} is bound elsewhere"
                )));
            }
            None => {
                return Err(CoreError::InvalidInput(format!(
                    "account {account} has no active binding"
                )));
            }
        }
    }
    tx.execute(
        "INSERT INTO creators(creator_id,display_name,aliases,tags,status,notes,created_at,updated_at) VALUES(?1,?2,'[]','[]','active','',?3,?3)",
        rusqlite::params![new_creator_id, display_name, now],
    )?;
    for account in &moved {
        tx.execute(
            "UPDATE account_bindings SET creator_id=?2 WHERE account_id=?1 AND state='active'",
            rusqlite::params![account, new_creator_id],
        )?;
    }
    let counts = json!({"accounts_moved": moved.len(), "source_kept": true});
    let operation = record_operation(
        tx,
        "split",
        Some(source),
        Some(new_creator_id),
        &moved,
        &counts,
        now,
    )?;
    crate::objects::outbox_emit(
        tx,
        "creator.changed",
        new_creator_id,
        &json!({"split_from": source}),
    )?;
    Ok(
        json!({"operation_id": operation, "creator_id": new_creator_id, "accounts": moved, "counts": counts}),
    )
}

/// Moves one account to another creator; the caller's expected current creator must still hold.
pub fn relink(
    tx: &Transaction<'_>,
    account_id: &str,
    expected: &str,
    target: &str,
    now: &str,
) -> Result<Value, CoreError> {
    if creator_status(tx, target)?.as_deref() != Some("active") {
        return Err(CoreError::InvalidInput(
            "target creator is not active".into(),
        ));
    }
    let owner: Option<String> = tx
        .query_row(
            "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
            [account_id],
            |r| r.get(0),
        )
        .optional()?;
    match owner.as_deref() {
        Some(id) if id == expected => {}
        Some(id) => {
            return Err(CoreError::VersionConflict(format!(
                "account is bound to {id}, not {expected}"
            )));
        }
        None => {
            return Err(CoreError::InvalidInput(
                "account has no active binding".into(),
            ));
        }
    }
    if expected == target {
        return Err(CoreError::InvalidInput(
            "account already belongs to the target".into(),
        ));
    }
    tx.execute(
        "UPDATE account_bindings SET creator_id=?2 WHERE account_id=?1 AND state='active'",
        rusqlite::params![account_id, target],
    )?;
    let accounts = vec![account_id.to_owned()];
    let counts = json!({"accounts_moved": 1});
    let operation = record_operation(
        tx,
        "relink",
        Some(expected),
        Some(target),
        &accounts,
        &counts,
        now,
    )?;
    crate::objects::outbox_emit(
        tx,
        "creator.changed",
        target,
        &json!({"relinked": account_id}),
    )?;
    Ok(json!({"operation_id": operation, "accounts": accounts, "counts": counts}))
}

/// Newest first; `after` is the previous page's last operation id.
pub fn operations(
    c: &Connection,
    creator_id: Option<&str>,
    after: Option<&str>,
    limit: i64,
) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 200);
    let cursor: Option<(String, String)> = match after {
        Some(id) => c
            .query_row(
                "SELECT created_at,operation_id FROM creator_operations WHERE operation_id=?1",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok(),
        None => None,
    };
    let mut stmt = c.prepare(
        "SELECT operation_id,kind,source_creator_id,target_creator_id,account_ids,counts,created_at
         FROM creator_operations
         WHERE (?1 IS NULL OR source_creator_id=?1 OR target_creator_id=?1)
           AND (?2 IS NULL OR (created_at,operation_id) < (?2,?3))
         ORDER BY created_at DESC, operation_id DESC LIMIT ?4",
    )?;
    let (at, id) = match &cursor {
        Some((at, id)) => (Some(at.as_str()), Some(id.as_str())),
        None => (None, None),
    };
    let mut items = stmt
        .query_map(rusqlite::params![creator_id, at, id, limit + 1], |r| {
            Ok(json!({
                "operation_id": r.get::<_, String>(0)?,
                "kind": r.get::<_, String>(1)?,
                "source_creator_id": r.get::<_, Option<String>>(2)?,
                "target_creator_id": r.get::<_, Option<String>>(3)?,
                "account_ids": serde_json::from_str::<Value>(&r.get::<_, String>(4)?).unwrap_or(Value::Null),
                "counts": serde_json::from_str::<Value>(&r.get::<_, String>(5)?).unwrap_or(Value::Null),
                "created_at": r.get::<_, String>(6)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items
            .last()
            .and_then(|v| v["operation_id"].as_str())
            .map(str::to_owned)
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}

/// Profile observations, newest first, with the whitelisted record they carried.
pub fn profile_history(
    c: &Connection,
    account_id: &str,
    before: i64,
    limit: i64,
) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 100);
    let mut items = c
        .prepare(
            "SELECT sequence,display_name,observed_at,evidence,profile_json FROM account_observations
             WHERE account_id=?1 AND sequence<?2 ORDER BY sequence DESC LIMIT ?3",
        )?
        .query_map(rusqlite::params![account_id, before, limit + 1], |r| {
            Ok(json!({
                "sequence": r.get::<_, i64>(0)?,
                "display_name": r.get::<_, Option<String>>(1)?,
                "observed_at": r.get::<_, String>(2)?,
                "evidence": serde_json::from_str::<Value>(&r.get::<_, String>(3)?).unwrap_or(Value::Null),
                "profile": serde_json::from_str::<Value>(&r.get::<_, String>(4)?).unwrap_or(Value::Null),
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items.last().map(|v| v["sequence"].clone())
    } else {
        None
    };
    Ok(json!({"items": items, "next_before": next}))
}

pub fn candidates(
    c: &Connection,
    account_id: Option<&str>,
    creator_id: Option<&str>,
    state: Option<&str>,
    after: Option<&str>,
    limit: i64,
) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 200);
    let cursor: Option<(String, String)> = match after {
        Some(id) => c
            .query_row(
                "SELECT created_at,candidate_id FROM identity_candidates WHERE candidate_id=?1",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok(),
        None => None,
    };
    let mut stmt = c.prepare(
        "SELECT candidate_id,account_id,proposed_creator_id,kind,score,evidence_json,state,created_at,decided_at
         FROM identity_candidates
         WHERE (?1 IS NULL OR account_id=?1) AND (?2 IS NULL OR proposed_creator_id=?2)
           AND (?3 IS NULL OR state=?3)
           AND (?4 IS NULL OR (created_at,candidate_id) < (?4,?5))
         ORDER BY created_at DESC, candidate_id DESC LIMIT ?6",
    )?;
    let (at, id) = match &cursor {
        Some((at, id)) => (Some(at.as_str()), Some(id.as_str())),
        None => (None, None),
    };
    let mut items = stmt
        .query_map(
            rusqlite::params![account_id, creator_id, state, at, id, limit + 1],
            |r| {
                Ok(json!({
                    "candidate_id": r.get::<_, String>(0)?,
                    "account_id": r.get::<_, String>(1)?,
                    "proposed_creator_id": r.get::<_, String>(2)?,
                    "kind": r.get::<_, String>(3)?,
                    "score": r.get::<_, f64>(4)?,
                    "evidence": serde_json::from_str::<Value>(&r.get::<_, String>(5)?).unwrap_or(Value::Null),
                    "state": r.get::<_, String>(6)?,
                    "created_at": r.get::<_, String>(7)?,
                    "decided_at": r.get::<_, Option<String>>(8)?,
                }))
            },
        )?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items
            .last()
            .and_then(|v| v["candidate_id"].as_str())
            .map(str::to_owned)
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}

/// Confirming binds the account explicitly; an already matching binding is idempotent, a
/// different one is a conflict that is never overwritten.
pub fn confirm(tx: &Transaction<'_>, candidate_id: &str, now: &str) -> Result<Value, CoreError> {
    let row: Option<(String, String, String, String)> = tx
        .query_row(
            "SELECT account_id,proposed_creator_id,state,kind FROM identity_candidates WHERE candidate_id=?1",
            [candidate_id],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
        )
        .optional()?;
    let Some((account_id, creator_id, state, kind)) = row else {
        return Err(CoreError::NotFound("identity candidate".into()));
    };
    if state == "confirmed" {
        return Ok(
            json!({"candidate_id": candidate_id, "state": "confirmed", "account_id": account_id, "creator_id": creator_id, "changed": false}),
        );
    }
    if state == "rejected" {
        return Err(CoreError::VersionConflict("candidate was rejected".into()));
    }
    if creator_status(tx, &creator_id)?.as_deref() != Some("active") {
        return Err(CoreError::InvalidInput(
            "proposed creator is not active".into(),
        ));
    }
    let owner: Option<String> = tx
        .query_row(
            "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
            [&account_id],
            |r| r.get(0),
        )
        .optional()?;
    match owner.as_deref() {
        Some(id) if id == creator_id => {}
        Some(id) => {
            return Err(CoreError::VersionConflict(format!(
                "account is bound to {id}"
            )));
        }
        None => {
            let binding = format!("bnd-{}", uuid::Uuid::new_v4());
            crate::store::accounts::bind(tx, &binding, &creator_id, &account_id, now)?;
        }
    }
    tx.execute(
        "UPDATE identity_candidates SET state='confirmed',decided_at=?2 WHERE candidate_id=?1",
        rusqlite::params![candidate_id, now],
    )?;
    let accounts = vec![account_id.clone()];
    record_operation(
        tx,
        "candidate_confirm",
        Some(&creator_id),
        Some(&creator_id),
        &accounts,
        &json!({"kind": kind}),
        now,
    )?;
    crate::objects::outbox_emit(
        tx,
        "account.bound",
        &account_id,
        &json!({"creator_id": creator_id}),
    )?;
    Ok(
        json!({"candidate_id": candidate_id, "state": "confirmed", "account_id": account_id, "creator_id": creator_id, "changed": true}),
    )
}

pub fn reject(tx: &Transaction<'_>, candidate_id: &str, now: &str) -> Result<Value, CoreError> {
    let updated = tx.execute(
        "UPDATE identity_candidates SET state='rejected',decided_at=?2 WHERE candidate_id=?1 AND state='pending'",
        rusqlite::params![candidate_id, now],
    )?;
    if updated == 0 {
        let state: Option<String> = tx
            .query_row(
                "SELECT state FROM identity_candidates WHERE candidate_id=?1",
                [candidate_id],
                |r| r.get(0),
            )
            .optional()?;
        let Some(state) = state else {
            return Err(CoreError::NotFound("identity candidate".into()));
        };
        return Ok(json!({"candidate_id": candidate_id, "state": state, "changed": false}));
    }
    record_operation(tx, "candidate_reject", None, None, &[], &json!({}), now)?;
    Ok(json!({"candidate_id": candidate_id, "state": "rejected", "changed": true}))
}
