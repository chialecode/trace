//! Saved plugin state and the jobs that belong to one platform's plugin.
use crate::error::CoreError;
use rusqlite::Connection;
use trace_contracts::control::PluginDto;

// Both the stop operation and its visible draining count use this same scope.
const PLATFORM_JOB_SCOPE: &str = "(subject_id IN (SELECT credential_id FROM credentials_state WHERE platform=?1) OR subject_id IN (SELECT a.account_id FROM source_accounts a WHERE a.namespace=?1) OR subject_id IN (SELECT e.entry_id FROM entries e WHERE e.namespace=?1 OR e.namespace LIKE ?1||':%') OR subject_id IN (SELECT l.session_id FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id JOIN source_accounts a ON a.account_id=s.account_id WHERE a.namespace=?1 OR a.namespace LIKE ?1||':%') OR subject_id IN (SELECT s.subscription_id FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id WHERE a.namespace=?1 OR a.namespace LIKE ?1||':%'))";

/// Plugins with saved state; the live task count is filled in by the caller.
pub fn saved_states(c: &Connection) -> Result<Vec<PluginDto>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT plugin_id, platform, version, state, protocol, capabilities FROM plugins_state",
    )?;
    let rows = stmt
        .query_map([], |r| {
            Ok(PluginDto {
                active_tasks: 0,
                plugin_id: r.get(0)?,
                platform: r.get(1)?,
                version: r.get(2)?,
                state: r.get(3)?,
                protocol: r.get(4)?,
                capabilities: r
                    .get::<_, String>(5)
                    .ok()
                    .and_then(|s| serde_json::from_str(&s).ok())
                    .unwrap_or_default(),
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Every job that works on behalf of the platform, in any state.
pub fn job_ids(c: &Connection, platform: &str) -> Result<Vec<String>, CoreError> {
    let sql = format!("SELECT job_id FROM jobs WHERE {PLATFORM_JOB_SCOPE}");
    let ids = c
        .prepare(&sql)?
        .query_map([platform], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(ids)
}

/// Saves the enabled flag; enabling wakes the platform's waiting jobs, disabling parks its live ones.
pub fn set_enabled(
    c: &Connection,
    platform: &str,
    enabled: bool,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO plugins_state(plugin_id,platform,version,state,protocol,binary_path,enabled,updated_at) VALUES(?1,?1,?2,?3,?4,'',?5,?6) ON CONFLICT(plugin_id) DO UPDATE SET enabled=excluded.enabled,state=excluded.state,updated_at=excluded.updated_at",
        rusqlite::params![
            platform,
            env!("CARGO_PKG_VERSION"),
            if enabled { "enabled" } else { "disabled" },
            trace_domain::PLUGIN_PROTOCOL_VERSION,
            enabled as i64,
            now
        ],
    )?;
    let sql = if enabled {
        format!(
            "UPDATE jobs SET state='queued',next_run_at=?2,updated_at=?2 WHERE state='waiting_plugin' AND {PLATFORM_JOB_SCOPE}"
        )
    } else {
        format!(
            "UPDATE jobs SET state='waiting_plugin',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE job_type IN ('scan','observe','media','record','account_profile','live_auxiliary','credential_probe','auth_completion') AND state IN ('queued','leased','running','waiting_retry') AND {PLATFORM_JOB_SCOPE}"
        )
    };
    c.execute(&sql, rusqlite::params![platform, now])?;
    Ok(())
}
