//! The persisted rule catalog: the last declaration of every adapter, kept when the plugin
//! is disabled or its process is not running.
use crate::error::CoreError;
use rusqlite::{Connection, Transaction, params};
use trace_contracts::catalog::RuleView;
use trace_contracts::rules::{RuleDeclaration, RuleStatus};

/// Replaces what `plugin` declares for `site`. A rule the adapter no longer declares stays
/// in the catalog as retired, so an old reference still resolves to an explanation.
pub fn replace_declared(
    tx: &Transaction<'_>,
    plugin_id: &str,
    site: &str,
    adapter_version: &str,
    rules: &[RuleDeclaration],
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE site_rules SET declared=0,status='retired' WHERE plugin_id=?1 AND site=?2",
        params![plugin_id, site],
    )?;
    let mut upsert = tx.prepare_cached(
        "INSERT INTO site_rules(rule_id,site,plugin_id,adapter_version,declaration,status,declared,declared_at)
         VALUES(?1,?2,?3,?4,?5,?6,1,?7)
         ON CONFLICT(rule_id) DO UPDATE SET site=excluded.site,plugin_id=excluded.plugin_id,adapter_version=excluded.adapter_version,declaration=excluded.declaration,status=excluded.status,declared=1,declared_at=excluded.declared_at",
    )?;
    for rule in rules {
        upsert.execute(params![
            rule.id,
            site,
            plugin_id,
            adapter_version,
            serde_json::to_string(rule)?,
            status_text(rule.status),
            now
        ])?;
    }
    Ok(())
}

fn status_text(status: RuleStatus) -> &'static str {
    match status {
        RuleStatus::Implemented => "implemented",
        RuleStatus::Partial => "partial",
        RuleStatus::Unimplemented => "unimplemented",
        RuleStatus::Retired => "retired",
    }
}

/// Every catalogued rule of a site (all of them with `None`), ordered by id. A rule the
/// adapter stopped declaring reads as retired.
pub fn list(c: &Connection, site: Option<&str>) -> Result<Vec<RuleView>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT declaration,declared,declared_at,adapter_version FROM site_rules WHERE (?1 IS NULL OR site=?1) ORDER BY rule_id",
    )?;
    let rows = stmt
        .query_map([site], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, bool>(1)?,
                r.get::<_, String>(2)?,
                r.get::<_, String>(3)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let mut views = Vec::with_capacity(rows.len());
    for (text, declared, declared_at, adapter_version) in rows {
        let Ok(mut declaration) = serde_json::from_str::<RuleDeclaration>(&text) else {
            continue;
        };
        if !declared {
            declaration.status = RuleStatus::Retired;
        }
        views.push(RuleView {
            declaration,
            declared,
            declared_at,
            adapter_version,
        });
    }
    Ok(views)
}

/// `enabled` or `disabled`; a plugin that was never switched is enabled.
pub fn plugin_state(c: &Connection, site: &str) -> Result<String, CoreError> {
    let enabled: Option<bool> = c
        .query_row(
            "SELECT enabled FROM plugins_state WHERE plugin_id=?1",
            [site],
            |r| r.get(0),
        )
        .ok();
    Ok(if enabled == Some(false) {
        "disabled"
    } else {
        "enabled"
    }
    .into())
}
