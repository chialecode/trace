//! Site plugins: state listing and enable/disable per platform.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

/// Methods every registered adapter offers before it reports its own list.
const DEFAULT_CAPABILITIES: [&str; 7] = [
    "resolve_account",
    "enumerate",
    "observe",
    "resolve_media",
    "probe_auth",
    "probe_live",
    "sample_live_auxiliary",
];

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    match req.command.as_str() {
        cmd::PLUGIN_LIST => list(core).await,
        cmd::PLUGIN_SET_STATE => set_state(core, parse(req.args.clone())?).await,
        cmd::PLUGIN_INSTALL => {
            let path = super::need_str(&req.args, "source_dir")?;
            crate::plugin_install::install(core, &path).await
        }
        cmd::PLUGIN_ACTIVATE => {
            let plugin = super::need_str(&req.args, "plugin_id")?;
            let version = super::need_str(&req.args, "version")?;
            crate::plugin_install::activate(core, &plugin, &version).await
        }
        cmd::PLUGIN_UPGRADE => {
            let path = super::need_str(&req.args, "source_dir")?;
            let installed = crate::plugin_install::install(core, &path).await?;
            let plugin = installed["plugin_id"].as_str().unwrap_or("").to_owned();
            let version = installed["version"].as_str().unwrap_or("").to_owned();
            let mut activated = crate::plugin_install::activate(core, &plugin, &version).await?;
            activated["installed"] = installed;
            Ok(activated)
        }
        cmd::PLUGIN_ROLLBACK => {
            let plugin = super::need_str(&req.args, "plugin_id")?;
            crate::plugin_install::rollback(core, &plugin).await
        }
        cmd::PLUGIN_REMOVE => {
            let plugin = super::need_str(&req.args, "plugin_id")?;
            let version = super::need_str(&req.args, "version")?;
            crate::plugin_install::remove(core, &plugin, &version).await
        }
        cmd::PLUGIN_RELEASES => {
            let plugin = req.args["plugin_id"].as_str().map(str::to_owned);
            core.db
                .read(move |c| store::plugin_releases::releases(c, plugin.as_deref()))
                .await
        }
        cmd::PLUGIN_CAPABILITIES => capabilities(core).await,
        cmd::PLUGIN_CAPABILITY_SET => {
            let plugin = super::need_str(&req.args, "plugin_id")?;
            let kind = super::need_str(&req.args, "kind")?;
            let enabled = req.args["enabled"]
                .as_bool()
                .ok_or_else(|| CoreError::InvalidInput("capability state".into()))?;
            if trace_contracts::capability::contract(&kind).is_none() {
                return Err(CoreError::InvalidInput("unknown capability".into()));
            }
            let key = format!("capability:{plugin}:{kind}");
            let now = jobs::now();
            let value = if enabled { "enabled" } else { "disabled" };
            let (plugin2, kind2) = (plugin.clone(), kind.clone());
            core.db
                .write(move |tx| {
                    tx.execute(
                        "INSERT INTO app_settings(key,value,updated_at) VALUES(?1,?2,?3) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at",
                        rusqlite::params![key, value, now],
                    )?;
                    crate::objects::outbox_emit(tx, "plugin.changed", &plugin2, &json!({"capability": kind2, "enabled": enabled}))?;
                    Ok(())
                })
                .await?;
            Ok(json!({"plugin_id": plugin, "kind": kind, "enabled": enabled}))
        }
        other => Err(unsupported(other)),
    }
}

/// The four independent contracts, the active release's declarations and the per-kind switches.
async fn capabilities(core: &Arc<Core>) -> Result<Value, CoreError> {
    let releases = core.db.read(store::plugin_releases::active_all).await?;
    let settings: std::collections::HashMap<String, String> = core
        .db
        .read(|c| {
            let mut stmt =
                c.prepare("SELECT key,value FROM app_settings WHERE key LIKE 'capability:%'")?;
            let rows = stmt
                .query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)))?
                .collect::<Result<std::collections::HashMap<_, _>, _>>()?;
            Ok(rows)
        })
        .await?;
    let contracts: Vec<Value> = trace_contracts::capability::CAPABILITIES
        .iter()
        .map(|contract| {
            json!({
                "kind": contract.kind,
                "version": contract.version,
                "max_bytes": contract.max_bytes,
                "methods": contract.methods.iter().map(|m| json!({
                    "name": m.name,
                    "params": m.params,
                    "results": m.results,
                    "timeout_ms": m.timeout_ms,
                    "max_bytes": m.max_bytes,
                })).collect::<Vec<_>>(),
            })
        })
        .collect();
    let active: Vec<Value> = releases
        .iter()
        .map(|release| {
            let plugin_id = release["plugin_id"].as_str().unwrap_or("");
            let declared: Vec<Value> = release["capabilities"]
                .as_array()
                .map(|items| {
                    items
                        .iter()
                        .filter_map(Value::as_str)
                        .map(|kind| {
                            let key = format!("capability:{plugin_id}:{kind}");
                            let enabled =
                                settings.get(&key).map(String::as_str) != Some("disabled");
                            json!({"kind": kind, "enabled": enabled})
                        })
                        .collect()
                })
                .unwrap_or_default();
            json!({
                "plugin_id": plugin_id,
                "version": release["version"],
                "platform": release["manifest"]["platform"],
                "capabilities": declared,
            })
        })
        .collect();
    Ok(json!({"contracts": contracts, "active": active}))
}

/// Whether a plugin's optional extension of `kind` is switched on (default on after install).
pub async fn capability_enabled(
    core: &Core,
    plugin_id: &str,
    kind: &str,
) -> Result<bool, CoreError> {
    let key = format!("capability:{plugin_id}:{kind}");
    let value: Option<String> = core
        .db
        .read(move |c| {
            Ok(
                c.query_row("SELECT value FROM app_settings WHERE key=?1", [key], |r| {
                    r.get(0)
                })
                .ok(),
            )
        })
        .await?;
    Ok(value.as_deref() != Some("disabled"))
}

/// Adds every registered platform that has no saved state yet.
async fn with_registered(core: &Arc<Core>, mut items: Vec<ctl::PluginDto>) -> Vec<ctl::PluginDto> {
    for spec in crate::pluginhost::default_adapter_specs(&core.profile) {
        if items.iter().any(|i| i.plugin_id == spec.plugin_id) {
            continue;
        }
        let state = core.host.session_state(&spec.plugin_id).await;
        items.push(ctl::PluginDto {
            active_tasks: 0,
            plugin_id: spec.plugin_id,
            platform: spec.platform,
            version: "0.1.0".into(),
            state: format!("{state:?}").to_lowercase(),
            protocol: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
            capabilities: DEFAULT_CAPABILITIES.map(str::to_owned).to_vec(),
        });
    }
    items
}

/// A disabled plugin that still runs tasks is "stopping" until they drain.
async fn list(core: &Arc<Core>) -> Result<Value, CoreError> {
    let saved = core.db.read(store::plugins::saved_states).await?;
    let mut items = with_registered(core, saved).await;
    let active = core.active_jobs.lock().unwrap().clone();
    items = core
        .db
        .read(move |c| {
            for item in &mut items {
                let ids = store::plugins::job_ids(c, &item.platform)?;
                item.active_tasks = ids
                    .iter()
                    .map(|id| active.get(id).copied().unwrap_or(0))
                    .sum();
                if item.state == "disabled" && item.active_tasks > 0 {
                    item.state = "stopping".into();
                }
            }
            Ok(items)
        })
        .await?;
    Ok(json!({ "items": items }))
}

async fn set_state(core: &Arc<Core>, a: ctl::PluginSetStateArgs) -> Result<Value, CoreError> {
    if !matches!(a.state.as_str(), "enabled" | "disabled") {
        return Err(CoreError::InvalidInput("plugin state".into()));
    }
    let enabled = a.state == "enabled";
    let platform = a.plugin_id;
    let registered = crate::pluginhost::default_adapter_specs(&core.profile)
        .iter()
        .any(|s| s.platform == platform);
    if !registered {
        return Err(CoreError::NotFound("plugin".into()));
    }
    if enabled {
        core.host.set_enabled(&platform, true);
    }
    let saved = platform.clone();
    core.db
        .write(move |tx| store::plugins::set_enabled(tx, &saved, enabled, &jobs::now()))
        .await?;
    if !enabled {
        core.host.set_enabled(&platform, false);
        core.host.kill_session(&platform).await;
    }
    Ok(json!({"ok": true}))
}
