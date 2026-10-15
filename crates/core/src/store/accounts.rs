//! Source accounts, their bindings to creators and name history.
use crate::error::CoreError;
use rusqlite::Connection;
use serde_json::{Value, json};

/// Up to four name observations older than `before`, newest first, with a cursor when more exist.
pub fn name_history(c: &Connection, account_id: &str, before: i64) -> Result<Value, CoreError> {
    let mut items = c
        .prepare("SELECT sequence,display_name,observed_at,evidence FROM account_observations WHERE account_id=?1 AND sequence<?2 ORDER BY sequence DESC LIMIT 5")?
        .query_map(rusqlite::params![account_id, before], |r| {
            Ok(json!({
                "sequence": r.get::<_, i64>(0)?,
                "display_name": r.get::<_, Option<String>>(1)?,
                "observed_at": r.get::<_, String>(2)?,
                "evidence": serde_json::from_str::<Value>(&r.get::<_, String>(3)?).unwrap_or(Value::Null),
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() > 4;
    items.truncate(4);
    let next = if more {
        items.last().map(|v| v["sequence"].clone())
    } else {
        None
    };
    Ok(json!({"items": items, "next_before": next}))
}

/// Creates the account unless (namespace, key) exists; returns the id that now owns it.
pub fn upsert(
    c: &Connection,
    new_id: &str,
    namespace: &str,
    native_key: &str,
    display_name: Option<&str>,
    profile_url: Option<&str>,
    now: &str,
) -> Result<String, CoreError> {
    c.execute(
        "INSERT INTO source_accounts (account_id, namespace, native_account_key, display_name, profile_url, first_seen_at)
         VALUES (?1,?2,?3,?4,?5,?6)
         ON CONFLICT(namespace, native_account_key) DO NOTHING",
        rusqlite::params![new_id, namespace, native_key, display_name, profile_url, now],
    )?;
    Ok(c.query_row(
        "SELECT account_id FROM source_accounts WHERE namespace=?1 AND native_account_key=?2",
        rusqlite::params![namespace, native_key],
        |r| r.get(0),
    )?)
}

/// The account is actively bound to a different creator.
pub fn bound_to_other_creator(
    c: &Connection,
    account_id: &str,
    creator_id: &str,
) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM account_bindings WHERE account_id=?1 AND state='active' AND creator_id!=?2)",
        rusqlite::params![account_id, creator_id],
        |r| r.get(0),
    )?)
}

pub fn bind(
    c: &Connection,
    binding_id: &str,
    creator_id: &str,
    account_id: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO account_bindings (binding_id, creator_id, account_id, state, created_at)
         VALUES (?1,?2,?3,'active',?4) ON CONFLICT(account_id) DO UPDATE SET creator_id=excluded.creator_id,state='active',ended_at=NULL",
        rusqlite::params![binding_id, creator_id, account_id, now],
    )?;
    Ok(())
}

pub fn unbind(c: &Connection, account_id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE account_bindings SET state='ended', ended_at=?2 WHERE account_id=?1",
        rusqlite::params![account_id, now],
    )?;
    Ok(())
}

pub fn set_display_name(
    c: &Connection,
    account_id: &str,
    name: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE source_accounts SET display_name=?2, profile_updated_at=?3 WHERE account_id=?1",
        rusqlite::params![account_id, name, now],
    )?;
    Ok(())
}

pub fn set_profile_url(
    c: &Connection,
    account_id: &str,
    url: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE source_accounts SET profile_url=?2, profile_updated_at=?3 WHERE account_id=?1",
        rusqlite::params![account_id, url, now],
    )?;
    Ok(())
}

/// Accounts (optionally of one creator) with their subscription counts.
pub fn list(c: &Connection, creator_id: Option<&str>) -> Result<Vec<Value>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT a.account_id, a.namespace, a.native_account_key, a.display_name, a.profile_url,
                b.creator_id,
                (SELECT count(*) FROM subscriptions s WHERE s.account_id=a.account_id AND s.acquisition_kind='subscription')
         FROM source_accounts a
         LEFT JOIN account_bindings b ON b.account_id=a.account_id AND b.state='active'
         WHERE (?1 IS NULL OR b.creator_id = ?1)",
    )?;
    let rows = stmt
        .query_map([creator_id], |r| {
            Ok(json!({
                "account_id": r.get::<_, String>(0)?,
                "namespace": r.get::<_, String>(1)?,
                "native_account_key": r.get::<_, String>(2)?,
                "display_name": r.get::<_, Option<String>>(3)?,
                "profile_url": r.get::<_, Option<String>>(4)?,
                "creator_id": r.get::<_, Option<String>>(5)?,
                "subscription_count": r.get::<_, i64>(6)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}
