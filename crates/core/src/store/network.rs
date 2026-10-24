//! Saved network choices (global and per site). The proxy login is not here: only its
//! credential-store reference is.
use crate::error::CoreError;
use crate::network::{Mode, Setting};
use rusqlite::{Connection, Transaction, params};

pub fn load(c: &Connection) -> Result<Vec<(String, Setting)>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT scope,mode,proxy_url,auth_ref FROM network_settings ORDER BY scope",
    )?;
    let rows = stmt
        .query_map([], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, Option<String>>(2)?,
                r.get::<_, Option<String>>(3)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows
        .into_iter()
        .filter_map(|(scope, mode, proxy_url, auth_ref)| {
            Some((
                scope,
                Setting {
                    mode: Mode::parse(&mode)?,
                    proxy_url,
                    auth_ref,
                },
            ))
        })
        .collect())
}

pub fn save(
    tx: &Transaction<'_>,
    scope: &str,
    setting: &Setting,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO network_settings(scope,mode,proxy_url,auth_ref,updated_at) VALUES(?1,?2,?3,?4,?5)
         ON CONFLICT(scope) DO UPDATE SET mode=excluded.mode,proxy_url=excluded.proxy_url,auth_ref=excluded.auth_ref,updated_at=excluded.updated_at",
        params![
            scope,
            setting.mode.as_str(),
            setting.proxy_url,
            setting.auth_ref,
            now
        ],
    )?;
    Ok(())
}

pub fn delete(tx: &Transaction<'_>, scope: &str) -> Result<(), CoreError> {
    tx.execute("DELETE FROM network_settings WHERE scope=?1", [scope])?;
    Ok(())
}
