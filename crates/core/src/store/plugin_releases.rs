//! Installed plugin releases, side by side (stage plan §11.8).
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

pub struct NewRelease {
    pub plugin_id: String,
    pub version: String,
    pub manifest_json: String,
    pub path: String,
    pub digest: String,
    pub protocol: String,
    pub capabilities_json: String,
    pub now: String,
}

fn removal_operation_id(plugin_id: &str, version: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(plugin_id.as_bytes());
    hasher.update([0]);
    hasher.update(version.as_bytes());
    format!(
        "plugin-remove-intent-{}",
        hasher
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    )
}

fn removal_pending(c: &Connection, plugin_id: &str, version: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM management_operations
         WHERE operation_id=?1 AND kind='plugin_remove_intent')",
        [removal_operation_id(plugin_id, version)],
        |r| r.get(0),
    )?)
}

fn with_effective_state(c: &Connection, mut release: Value) -> Result<Value, CoreError> {
    let plugin_id = release["plugin_id"].as_str().unwrap_or("");
    let version = release["version"].as_str().unwrap_or("");
    if removal_pending(c, plugin_id, version)? {
        release["state"] = json!("removing");
    }
    Ok(release)
}

pub fn insert(tx: &Transaction<'_>, release: &NewRelease) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO plugin_releases(plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at) VALUES(?1,?2,?3,?4,?5,?6,?7,'installed',?8)",
        rusqlite::params![
            release.plugin_id,
            release.version,
            release.manifest_json,
            release.path,
            release.digest,
            release.protocol,
            release.capabilities_json,
            release.now
        ],
    )?;
    Ok(())
}

pub fn release(c: &Connection, plugin_id: &str, version: &str) -> Result<Option<Value>, CoreError> {
    let release = c.query_row(
        "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE plugin_id=?1 AND version=?2",
        rusqlite::params![plugin_id, version],
        row,
    )
    .optional()?;
    release
        .map(|value| with_effective_state(c, value))
        .transpose()
}

fn row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Value> {
    Ok(json!({
        "plugin_id": r.get::<_, String>(0)?,
        "version": r.get::<_, String>(1)?,
        "manifest": serde_json::from_str::<Value>(&r.get::<_, String>(2)?).unwrap_or(Value::Null),
        "path": r.get::<_, String>(3)?,
        "digest": r.get::<_, String>(4)?,
        "protocol": r.get::<_, String>(5)?,
        "capabilities": serde_json::from_str::<Value>(&r.get::<_, String>(6)?).unwrap_or(Value::Null),
        "state": r.get::<_, String>(7)?,
        "installed_at": r.get::<_, String>(8)?,
    }))
}

pub fn releases(c: &Connection, plugin_id: Option<&str>) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(
        "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE (?1 IS NULL OR plugin_id=?1) ORDER BY plugin_id,installed_at DESC",
    )?;
    let mut items = stmt
        .query_map([plugin_id], row)?
        .collect::<Result<Vec<_>, _>>()?;
    for release in &mut items {
        *release = with_effective_state(c, release.clone())?;
    }
    Ok(json!({"items": items}))
}

pub fn active(c: &Connection, plugin_id: &str) -> Result<Option<Value>, CoreError> {
    let release = c.query_row(
        "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE plugin_id=?1 AND state='active'",
        [plugin_id],
        row,
    )
    .optional()?;
    release
        .map(|value| with_effective_state(c, value))
        .transpose()
}

pub fn active_all(c: &Connection) -> Result<Vec<Value>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE state='active' ORDER BY plugin_id",
    )?;
    let mut rows = stmt.query_map([], row)?.collect::<Result<Vec<_>, _>>()?;
    for release in &mut rows {
        *release = with_effective_state(c, release.clone())?;
    }
    Ok(rows)
}

/// The release to fall back to: a compatible installed or superseded version, newest first.
pub fn previous(
    c: &Connection,
    plugin_id: &str,
    active_version: &str,
) -> Result<Option<Value>, CoreError> {
    let releases = c
        .prepare(
            "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE plugin_id=?1 AND version<>?2 AND state IN ('installed','superseded') ORDER BY installed_at DESC,version DESC",
        )?
        .query_map(rusqlite::params![plugin_id, active_version], row)?
        .collect::<Result<Vec<_>, _>>()?;
    for release in releases {
        if !removal_pending(c, plugin_id, release["version"].as_str().unwrap_or(""))? {
            return Ok(Some(release));
        }
    }
    Ok(None)
}

/// Points the platform at one version inside a transaction (one active row).
pub fn set_active(
    tx: &Transaction<'_>,
    plugin_id: &str,
    version: &str,
    now: &str,
) -> Result<(), CoreError> {
    if removal_pending(tx, plugin_id, version)? {
        return Err(CoreError::VersionConflict(
            "a release being removed cannot be activated".into(),
        ));
    }
    tx.execute(
        "UPDATE plugin_releases SET state='superseded' WHERE plugin_id=?1 AND state='active' AND version<>?2",
        rusqlite::params![plugin_id, version],
    )?;
    let changed = tx.execute(
        "UPDATE plugin_releases SET state='active' WHERE plugin_id=?1 AND version=?2",
        rusqlite::params![plugin_id, version],
    )?;
    if changed == 0 {
        return Err(CoreError::NotFound("plugin release".into()));
    }
    let (path, protocol): (String, String) = tx.query_row(
        "SELECT path,protocol FROM plugin_releases WHERE plugin_id=?1 AND version=?2",
        rusqlite::params![plugin_id, version],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    tx.execute(
        "INSERT INTO plugins_state(plugin_id,platform,version,state,protocol,binary_path,enabled,updated_at) VALUES(?1,?1,?2,'enabled',?3,?4,1,?5)
         ON CONFLICT(plugin_id) DO UPDATE SET version=excluded.version,state=CASE WHEN plugins_state.enabled=0 THEN 'disabled' ELSE 'enabled' END,protocol=excluded.protocol,binary_path=excluded.binary_path,updated_at=excluded.updated_at",
        rusqlite::params![plugin_id, version, protocol, path, now],
    )?;
    Ok(())
}

/// Non-active releases whose directory may already be gone; the caller checks the filesystem.
pub fn non_active(
    c: &Connection,
    plugin_id: &str,
    version: &str,
) -> Result<Option<Value>, CoreError> {
    let release = c.query_row(
        "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE plugin_id=?1 AND version=?2 AND state!='active'",
        rusqlite::params![plugin_id, version],
        row,
    )
    .optional()?;
    release
        .map(|value| with_effective_state(c, value))
        .transpose()
}

pub fn non_active_rows(c: &Connection) -> Result<Vec<Value>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at FROM plugin_releases WHERE state!='active' ORDER BY plugin_id,version",
    )?;
    let mut rows = stmt.query_map([], row)?.collect::<Result<Vec<_>, _>>()?;
    for release in &mut rows {
        *release = with_effective_state(c, release.clone())?;
    }
    Ok(rows)
}

/// Records the durable intent before any file is moved. Retrying an interrupted removal keeps it.
pub fn begin_remove(
    tx: &Transaction<'_>,
    plugin_id: &str,
    version: &str,
    now: &str,
) -> Result<(), CoreError> {
    let state: Option<String> = tx
        .query_row(
            "SELECT state FROM plugin_releases WHERE plugin_id=?1 AND version=?2",
            rusqlite::params![plugin_id, version],
            |r| r.get(0),
        )
        .optional()?;
    match state.as_deref() {
        None => Err(CoreError::NotFound("plugin release".into())),
        Some("active") => Err(CoreError::VersionConflict(
            "the active plugin version cannot be removed".into(),
        )),
        Some("removing") => Ok(()),
        Some(_) => {
            let operation_id = removal_operation_id(plugin_id, version);
            tx.execute(
                "INSERT INTO management_operations(operation_id,kind,subject,detail_json,created_at)
                 VALUES(?1,'plugin_remove_intent',?2,?3,?4)
                 ON CONFLICT(operation_id) DO UPDATE SET
                   kind='plugin_remove_intent',created_at=excluded.created_at",
                rusqlite::params![
                    operation_id,
                    plugin_id,
                    json!({"version": version}).to_string(),
                    now
                ],
            )?;
            Ok(())
        }
    }
}

/// Finishes the database half only after the durable removal intent exists.
pub fn finish_remove(
    tx: &Transaction<'_>,
    plugin_id: &str,
    version: &str,
) -> Result<(), CoreError> {
    let intent = tx.execute(
        "UPDATE management_operations SET kind='plugin_remove_intent_completed'
         WHERE operation_id=?1 AND kind='plugin_remove_intent'",
        [removal_operation_id(plugin_id, version)],
    )?;
    if intent != 1 {
        return Err(CoreError::VersionConflict(
            "plugin removal intent is not present".into(),
        ));
    }
    let removed = tx.execute(
        "DELETE FROM plugin_releases WHERE plugin_id=?1 AND version=?2 AND state!='active'",
        rusqlite::params![plugin_id, version],
    )?;
    if removed == 1 {
        Ok(())
    } else {
        Err(CoreError::VersionConflict(
            "plugin removal intent is not present".into(),
        ))
    }
}
