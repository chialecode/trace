//! A lease freezes the requesting subscription, policy and credential generation.
//! Re-reading a mutable subscription while executing would mix two identities.
use crate::{error::CoreError, jobs::ClaimedJob};
use rusqlite::{Connection, OptionalExtension};
use serde_json::{Value, json};

pub fn capture(c: &Connection, payload: &mut Value) -> Result<(), CoreError> {
    let Some(sub) = payload["subscription_id"].as_str() else {
        return Ok(());
    };
    let (version, credential, generation, quality): (i64, Option<String>, Option<i64>, String) = c.query_row(
        "SELECT s.config_version,s.credential_id,k.generation,s.quality_json FROM subscriptions s LEFT JOIN credentials_state k ON k.credential_id=s.credential_id WHERE s.subscription_id=?1",
        [sub], |r| Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?)))?;
    payload["execution_scope"] = json!({"subscription_version":version,
        "credential_id":credential,"credential_generation":generation,
        "quality":serde_json::from_str::<Value>(&quality)?});
    Ok(())
}

pub fn check(c: &Connection, job: &ClaimedJob) -> Result<(), CoreError> {
    if let Some(source) = job.payload["source_request_id"].as_str() {
        let valid:bool=c.query_row("SELECT EXISTS(SELECT 1 FROM source_requests WHERE request_id=?1 AND generation=?2 AND state!='cancelled' AND credential_generation IS (SELECT generation FROM credentials_state WHERE credential_id=source_requests.credential_id))",rusqlite::params![source,job.payload["source_request_generation"].as_i64()],|r|r.get(0))?;
        if !valid {
            return Err(CoreError::Cancelled);
        }
    }

    if (job.job_type == "credential_probe"
        || job.job_type == "auth_completion"
        || job.payload["completion"] == true)
        && let (Some(credential), Some(generation)) = (
            job.payload["credential_id"].as_str(),
            job.payload["credential_generation"].as_i64(),
        )
    {
        let current:bool=c.query_row("SELECT EXISTS(SELECT 1 FROM credentials_state WHERE credential_id=?1 AND generation=?2)",rusqlite::params![credential,generation],|r|r.get(0))?;
        if !current {
            return Err(CoreError::Cancelled);
        }
    }
    if let Some(parent) = job.payload["completion_parent"].as_str() {
        let cancelled: bool = c.query_row(
            "SELECT EXISTS(SELECT 1 FROM jobs WHERE job_id=?1 AND state='cancelled')",
            [parent],
            |r| r.get(0),
        )?;
        if cancelled {
            return Err(CoreError::Cancelled);
        }
    }
    let Some(sub) = job.payload["subscription_id"].as_str() else {
        return Ok(());
    };
    let Some(scope) = job.payload.get("execution_scope") else {
        return Ok(());
    };
    let credential = scope["credential_id"].as_str();
    let generation = scope["credential_generation"].as_i64();
    let valid: bool = c.query_row(
        "SELECT EXISTS(SELECT 1 FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id LEFT JOIN credentials_state k ON k.credential_id=s.credential_id WHERE s.subscription_id=?1 AND s.state='enabled' AND s.config_version=?2 AND s.credential_id IS ?3 AND k.generation IS ?4 AND NOT EXISTS(SELECT 1 FROM plugins_state p WHERE p.platform=a.namespace AND p.enabled=0))",
        rusqlite::params![sub,scope["subscription_version"].as_i64(),credential,generation], |r|r.get(0))?;
    if valid {
        Ok(())
    } else {
        Err(CoreError::Cancelled)
    }
}

/// Resolve the vault reference under the same lease check. The secret itself
/// never appears in this payload or any persistent acquisition history.
pub fn credential(
    c: &Connection,
    job: &ClaimedJob,
    platform: &str,
) -> Result<Option<(String, i64, String, String)>, CoreError> {
    job.check(c)?;
    let Some(sub) = job.payload["subscription_id"].as_str() else {
        return Ok(None);
    };
    let row = c.query_row(
        "SELECT k.credential_id,k.generation,k.vault_ref,k.status FROM subscriptions s JOIN credentials_state k ON k.credential_id=s.credential_id AND k.platform=?2 WHERE s.subscription_id=?1",
        rusqlite::params![sub,platform], |r| Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?))).optional()?;
    let configured: bool = c.query_row(
        "SELECT credential_id IS NOT NULL FROM subscriptions WHERE subscription_id=?1",
        [sub],
        |r| r.get(0),
    )?;
    if configured && row.is_none() {
        return Err(CoreError::AuthRequired("credential is unavailable".into()));
    }
    Ok(row)
}
