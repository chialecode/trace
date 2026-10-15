//! Installing, activating, rolling back and removing site plugins (stage plan §11.8).
//!
//! An install only copies and verifies; it never runs the plugin. A version directory is
//! immutable and side by side with the previous one, so a rollback is a pointer change. Removing
//! a release deletes only its own files: entries, text, objects and downloads are untouched.
use crate::core::Core;
use crate::error::CoreError;
use crate::pluginhost::AdapterSpec;
use crate::store::plugin_releases::{self, NewRelease};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::path::{Component, Path, PathBuf};
use std::sync::Arc;

const MANIFEST_MAX_BYTES: u64 = 1024 * 1024;
const PLUGIN_MAX_BYTES: u64 = 256 * 1024 * 1024;
const PLUGIN_MAX_FILES: usize = 4096;

#[derive(Debug, Deserialize)]
struct Manifest {
    id: String,
    name: String,
    version: String,
    protocol: String,
    platform: String,
    entrypoint: String,
    files: Vec<ManifestFile>,
    #[serde(default)]
    capabilities: Vec<String>,
    #[serde(default)]
    permissions: Vec<String>,
    #[serde(default)]
    limits: Value,
}

#[derive(Debug, Deserialize)]
struct ManifestFile {
    path: String,
    sha256: String,
}

fn safe_relative(path: &str) -> bool {
    !path.is_empty()
        && path.len() <= 512
        && !path.starts_with('/')
        && !path.starts_with('\\')
        && !path.contains(':')
        && !path
            .split(['/', '\\'])
            .any(|part| part == ".." || part == ".")
}

fn sha256_file(path: &Path) -> Result<String, CoreError> {
    use std::io::Read;
    let mut file = std::fs::File::open(path).map_err(CoreError::Io)?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0u8; 64 * 1024];
    loop {
        let read = file.read(&mut buffer).map_err(CoreError::Io)?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

/// Every regular file below `root`, with symlinks/junctions refused rather than followed.
fn walk_files(root: &Path) -> Result<Vec<PathBuf>, CoreError> {
    let mut out = Vec::new();
    fn walk(dir: &Path, out: &mut Vec<PathBuf>) -> Result<(), CoreError> {
        let entries = std::fs::read_dir(dir).map_err(CoreError::Io)?;
        for entry in entries {
            let entry = entry.map_err(CoreError::Io)?;
            let path = entry.path();
            let meta = std::fs::symlink_metadata(&path).map_err(CoreError::Io)?;
            if meta.file_type().is_symlink() {
                return Err(CoreError::InvalidInput(
                    "plugin directories may not contain links".into(),
                ));
            }
            if meta.is_dir() {
                walk(&path, out)?;
            } else if meta.is_file() {
                if out.len() >= PLUGIN_MAX_FILES {
                    return Err(CoreError::InvalidInput("plugin has too many files".into()));
                }
                out.push(path);
            }
        }
        Ok(())
    }
    walk(root, &mut out)?;
    Ok(out)
}

fn read_manifest(dir: &Path) -> Result<(Manifest, Vec<u8>), CoreError> {
    let path = dir.join("plugin.json");
    let meta = std::fs::metadata(&path).map_err(|_| CoreError::NotFound("plugin.json".into()))?;
    if meta.len() > MANIFEST_MAX_BYTES {
        return Err(CoreError::InvalidInput("plugin manifest too large".into()));
    }
    let bytes = std::fs::read(&path).map_err(CoreError::Io)?;
    let manifest: Manifest = serde_json::from_slice(&bytes)
        .map_err(|e| CoreError::InvalidInput(format!("plugin.json: {e}")))?;
    Ok((manifest, bytes))
}

fn validate(core: &Core, manifest: &Manifest) -> Result<String, CoreError> {
    let shape = |s: &str, max: usize| !s.is_empty() && s.len() <= max;
    if !shape(&manifest.id, 64)
        || !shape(&manifest.name, 128)
        || !shape(&manifest.version, 64)
        || !shape(&manifest.platform, 32)
    {
        return Err(CoreError::InvalidInput("plugin identity fields".into()));
    }
    let version_ok = manifest
        .version
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'+'));
    if !version_ok {
        return Err(CoreError::InvalidInput("plugin version".into()));
    }
    if manifest.protocol != trace_domain::PLUGIN_PROTOCOL_VERSION {
        return Err(CoreError::InvalidInput(format!(
            "plugin protocol {} is not supported",
            manifest.protocol
        )));
    }
    if trace_domain::sites::site(&manifest.platform).is_none() {
        return Err(CoreError::InvalidInput("unknown plugin platform".into()));
    }
    if !safe_relative(&manifest.entrypoint) {
        return Err(CoreError::InvalidInput("plugin entrypoint".into()));
    }
    if manifest.files.is_empty() || manifest.files.len() > PLUGIN_MAX_FILES {
        return Err(CoreError::InvalidInput("plugin file list".into()));
    }
    let mut declared = std::collections::HashSet::new();
    let mut hasher = Sha256::new();
    for file in &manifest.files {
        if !safe_relative(&file.path) || !declared.insert(file.path.clone()) {
            return Err(CoreError::InvalidInput(format!(
                "plugin file path {}",
                file.path
            )));
        }
        if file.sha256.len() != 64 || !file.sha256.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(CoreError::InvalidInput("plugin file digest".into()));
        }
        hasher.update(file.path.as_bytes());
        hasher.update([0]);
        hasher.update(file.sha256.to_ascii_lowercase().as_bytes());
        hasher.update([0]);
    }
    if !declared.contains(&manifest.entrypoint) {
        return Err(CoreError::InvalidInput(
            "the entrypoint is not one of the declared files".into(),
        ));
    }
    for kind in &manifest.capabilities {
        if trace_contracts::capability::contract(kind).is_none() {
            return Err(CoreError::InvalidInput(format!(
                "unknown capability {kind}"
            )));
        }
    }
    for permission in &manifest.permissions {
        if permission.len() > 64 || permission.is_empty() {
            return Err(CoreError::InvalidInput("plugin permission".into()));
        }
    }
    if !manifest.limits.is_null() && !manifest.limits.is_object() {
        return Err(CoreError::InvalidInput("plugin limits".into()));
    }
    let _ = core;
    Ok(hasher
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

fn plugin_dir(core: &Core, plugin_id: &str, version: &str) -> PathBuf {
    core.profile
        .data_root
        .join("plugins")
        .join(plugin_id)
        .join(version)
}

fn remove_dir(path: &Path) -> Result<(), CoreError> {
    if path.exists() {
        std::fs::remove_dir_all(path).map_err(CoreError::Io)?;
    }
    Ok(())
}

#[allow(clippy::too_many_lines)] // one install transaction: validate, copy, verify, record
pub async fn install(core: &Arc<Core>, source_dir: &str) -> Result<Value, CoreError> {
    let source = std::path::absolute(PathBuf::from(source_dir)).map_err(CoreError::Io)?;
    if source_dir.is_empty() || source_dir.len() > 4096 {
        return Err(CoreError::InvalidInput("plugin path".into()));
    }
    if source
        .components()
        .any(|c| matches!(c, Component::ParentDir))
    {
        return Err(CoreError::InvalidInput("plugin path".into()));
    }
    let (manifest, manifest_bytes) = read_manifest(&source)?;
    let file_digest = validate(core, &manifest)?;
    let digest = {
        let mut hasher = Sha256::new();
        hasher.update(&manifest_bytes);
        hasher.update(file_digest.as_bytes());
        hasher
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    };
    let files = walk_files(&source)?;
    let declared: std::collections::HashMap<&str, &ManifestFile> = manifest
        .files
        .iter()
        .map(|f| (f.path.as_str(), f))
        .collect();
    let mut total = 0u64;
    let mut verified = Vec::new();
    for path in &files {
        let rel = path
            .strip_prefix(&source)
            .map_err(|_| CoreError::Internal("plugin path".into()))?
            .to_string_lossy()
            .replace('\\', "/");
        if rel == "plugin.json" {
            continue;
        }
        let Some(file) = declared.get(rel.as_str()) else {
            return Err(CoreError::InvalidInput(format!(
                "undeclared plugin file {rel}"
            )));
        };
        let size = std::fs::metadata(path).map_err(CoreError::Io)?.len();
        total += size;
        if total > PLUGIN_MAX_BYTES {
            return Err(CoreError::InvalidInput("plugin is too large".into()));
        }
        let actual = sha256_file(path)?;
        if !actual.eq_ignore_ascii_case(&file.sha256) {
            return Err(CoreError::InvalidInput(format!(
                "plugin file digest mismatch: {rel}"
            )));
        }
        verified.push((rel, path.clone()));
    }
    if verified.len() != manifest.files.len() {
        return Err(CoreError::InvalidInput(
            "the manifest declares files the package does not contain".into(),
        ));
    }
    let destination = plugin_dir(core, &manifest.id, &manifest.version);
    if destination.exists() {
        let existing = {
            let (id, v) = (manifest.id.clone(), manifest.version.clone());
            core.db
                .read(move |c| plugin_releases::release(c, &id, &v))
                .await?
        };
        if existing
            .as_ref()
            .is_some_and(|r| r["digest"] == json!(digest))
        {
            return Ok(json!({
                "plugin_id": manifest.id,
                "version": manifest.version,
                "digest": digest,
                "installed": false,
                "note": "this version is already installed with the same digest",
            }));
        }
        return Err(CoreError::VersionConflict(
            "this plugin version is already installed with different contents".into(),
        ));
    }
    let staging = core
        .profile
        .data_root
        .join("plugins")
        .join(format!(".staging-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&staging).map_err(CoreError::Io)?;
    let copy = (|| -> Result<(), CoreError> {
        for (rel, path) in &verified {
            let target = staging.join(Path::new(rel));
            if let Some(parent) = target.parent() {
                std::fs::create_dir_all(parent).map_err(CoreError::Io)?;
            }
            std::fs::copy(path, &target).map_err(CoreError::Io)?;
            if !sha256_file(&target)?
                .eq_ignore_ascii_case(&declared[rel.as_str()].sha256.to_ascii_lowercase())
            {
                return Err(CoreError::Internal("copied plugin digest mismatch".into()));
            }
        }
        std::fs::write(staging.join("plugin.json"), &manifest_bytes).map_err(CoreError::Io)?;
        Ok(())
    })();
    if let Err(error) = copy {
        remove_dir(&staging)?;
        return Err(error);
    }
    if let Some(parent) = destination.parent() {
        std::fs::create_dir_all(parent).map_err(CoreError::Io)?;
    }
    std::fs::rename(&staging, &destination).map_err(CoreError::Io)?;
    let now = crate::jobs::now();
    let path = destination.to_string_lossy().to_string();
    let manifest_json = String::from_utf8(manifest_bytes)
        .map_err(|_| CoreError::InvalidInput("plugin manifest encoding".into()))?;
    let capabilities = serde_json::to_string(&manifest.capabilities)?;
    let release = NewRelease {
        plugin_id: manifest.id.clone(),
        version: manifest.version.clone(),
        manifest_json,
        path: path.clone(),
        digest: digest.clone(),
        protocol: manifest.protocol.clone(),
        capabilities_json: capabilities,
        now: now.clone(),
    };
    let (id, version) = (manifest.id.clone(), manifest.version.clone());
    let (record_id, record_version, record_digest, record_now) =
        (id.clone(), version.clone(), digest.clone(), now.clone());
    let capabilities_list = manifest.capabilities.clone();
    core.db
        .write(move |tx| {
            plugin_releases::insert(tx, &release)?;
            crate::store::management::record(
                tx,
                "plugin_install",
                &record_id,
                &json!({"version": record_version, "digest": record_digest}),
                &record_now,
            )?;
            crate::objects::outbox_emit(tx, "plugin.changed", &record_id, &json!({}))?;
            Ok(())
        })
        .await?;
    Ok(json!({
        "plugin_id": id,
        "version": version,
        "digest": digest,
        "path": path,
        "capabilities": capabilities_list,
        "installed": true,
    }))
}

fn driver(core: &Core, release: &Value) -> Result<AdapterSpec, CoreError> {
    let manifest = &release["manifest"];
    let platform = manifest["platform"]
        .as_str()
        .ok_or_else(|| CoreError::InvalidInput("plugin manifest".into()))?;
    let entrypoint = manifest["entrypoint"]
        .as_str()
        .filter(|p| safe_relative(p))
        .ok_or_else(|| CoreError::InvalidInput("plugin entrypoint".into()))?;
    let plugin_id = release["plugin_id"]
        .as_str()
        .ok_or_else(|| CoreError::Internal("plugin release".into()))?;
    let path = Path::new(release["path"].as_str().unwrap_or("")).join(entrypoint);
    if !path.is_file() {
        return Err(CoreError::NotFound("plugin entrypoint file".into()));
    }
    let manifest_protocol = manifest["protocol"].as_str().unwrap_or("");
    if manifest_protocol != trace_domain::PLUGIN_PROTOCOL_VERSION {
        return Err(CoreError::InvalidInput(
            "the plugin's protocol does not match this host".into(),
        ));
    }
    let _ = core;
    Ok(AdapterSpec {
        plugin_id: plugin_id.to_owned(),
        platform: platform.to_owned(),
        binary_path: path,
    })
}

async fn platform_tasks(core: &Arc<Core>, platform: &str) -> Result<usize, CoreError> {
    let active = core.active_jobs.lock().unwrap().clone();
    let ids = {
        let platform = platform.to_owned();
        core.db
            .read(move |c| crate::store::plugins::job_ids(c, &platform))
            .await?
    };
    Ok(ids
        .iter()
        .map(|id| active.get(id).copied().unwrap_or(0))
        .sum())
}

/// Stops new dispatch, waits briefly for running tasks to reach a file boundary, then switches.
pub async fn activate(
    core: &Arc<Core>,
    plugin_id: &str,
    version: &str,
) -> Result<Value, CoreError> {
    let _lifecycle = core.plugin_lifecycle.lock().await;
    let release = {
        let (id, v) = (plugin_id.to_owned(), version.to_owned());
        core.db
            .read(move |c| plugin_releases::release(c, &id, &v))
            .await?
    }
    .ok_or_else(|| CoreError::NotFound("plugin release".into()))?;
    if release["state"] == json!("removing") {
        return Err(CoreError::VersionConflict(
            "a release being removed cannot be activated".into(),
        ));
    }
    if release["state"] == json!("active") {
        return Ok(
            json!({"activated": false, "platform": release["manifest"]["platform"], "version": version}),
        );
    }
    let spec = driver(core, &release)?;
    let platform = spec.platform.clone();
    core.host.set_enabled(&platform, false);
    let mut waited = 0u64;
    while platform_tasks(core, &platform).await? > 0 {
        if waited >= 5_000 {
            core.host.set_enabled(&platform, true);
            return Err(CoreError::VersionConflict(
                "tasks of this platform are still running; try again when they settle".into(),
            ));
        }
        tokio::time::sleep(std::time::Duration::from_millis(100)).await;
        waited += 100;
    }
    core.host.replace_spec(spec).await;
    core.host.set_enabled(&platform, true);
    let now = crate::jobs::now();
    let (id, v) = (plugin_id.to_owned(), version.to_owned());
    core.db
        .write(move |tx| {
            plugin_releases::set_active(tx, &id, &v, &now)?;
            crate::store::management::record(
                tx,
                "plugin_activate",
                &id,
                &json!({"version": v}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "plugin.changed", &id, &json!({}))?;
            Ok(())
        })
        .await?;
    Ok(json!({"activated": true, "platform": platform, "version": version}))
}

pub async fn rollback(core: &Arc<Core>, plugin_id: &str) -> Result<Value, CoreError> {
    let active = {
        let id = plugin_id.to_owned();
        core.db
            .read(move |c| plugin_releases::active(c, &id))
            .await?
    }
    .ok_or_else(|| CoreError::NotFound("active plugin".into()))?;
    let active_version = active["version"].as_str().unwrap_or("").to_owned();
    let previous = {
        let (id, v) = (plugin_id.to_owned(), active_version.clone());
        core.db
            .read(move |c| plugin_releases::previous(c, &id, &v))
            .await?
    }
    .ok_or_else(|| CoreError::VersionConflict("no earlier release to roll back to".into()))?;
    let version = previous["version"]
        .as_str()
        .ok_or_else(|| CoreError::Internal("plugin release".into()))?
        .to_owned();
    let mut result = activate(core, plugin_id, &version).await?;
    result["rolled_back_from"] = json!(active_version);
    let now = crate::jobs::now();
    let (id, v) = (plugin_id.to_owned(), version.clone());
    core.db
        .write(move |tx| {
            crate::store::management::record(
                tx,
                "plugin_rollback",
                &id,
                &json!({"version": v}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "plugin.changed", &id, &json!({}))?;
            Ok(())
        })
        .await?;
    Ok(result)
}

pub async fn remove(core: &Arc<Core>, plugin_id: &str, version: &str) -> Result<Value, CoreError> {
    let _lifecycle = core.plugin_lifecycle.lock().await;
    let release = {
        let (id, v) = (plugin_id.to_owned(), version.to_owned());
        core.db
            .read(move |c| plugin_releases::release(c, &id, &v))
            .await?
    }
    .ok_or_else(|| CoreError::NotFound("plugin release".into()))?;
    if release["state"] == json!("active") {
        return Err(CoreError::VersionConflict(
            "the active plugin version cannot be removed".into(),
        ));
    }
    let path = PathBuf::from(release["path"].as_str().unwrap_or(""));
    let plugins_root = core.profile.data_root.join("plugins");
    if !path.starts_with(&plugins_root) {
        return Err(CoreError::Internal(
            "plugin path is outside the plugin root".into(),
        ));
    }
    let trash = path.with_file_name(format!(".removing-{version}"));
    let (id, v) = (plugin_id.to_owned(), version.to_owned());
    let intent_at = crate::jobs::now();
    core.db
        .write(move |tx| plugin_releases::begin_remove(tx, &id, &v, &intent_at))
        .await?;
    remove_release_files(&path, &trash).map_err(|error| {
        CoreError::Io(std::io::Error::other(format!(
            "插件文件删除未完成，移除意图与可重试状态已保留：{error}"
        )))
    })?;
    let now = crate::jobs::now();
    let (id, v) = (plugin_id.to_owned(), version.to_owned());
    core.db
        .write(move |tx| {
            plugin_releases::finish_remove(tx, &id, &v)?;
            crate::store::management::record(
                tx,
                "plugin_remove",
                &id,
                &json!({"version": v}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "plugin.changed", &id, &json!({}))?;
            Ok(())
        })
        .await?;
    Ok(json!({"removed": true, "version": version, "archive_untouched": true}))
}

fn remove_release_files(path: &Path, trash: &Path) -> Result<(), CoreError> {
    if path.exists() {
        if trash.exists() {
            remove_dir(trash)?;
        }
        std::fs::rename(path, trash).map_err(CoreError::Io)?;
    }
    remove_dir(trash)
}

/// Completes durable removal intents. Cleanup errors are warnings: a locked optional plugin must
/// never make the archive unreadable, and the `removing` row makes the next retry explicit.
pub async fn reconcile_removals(core: &Arc<Core>) -> Result<(), CoreError> {
    let plugins_root = core.profile.data_root.join("plugins");
    let rows = core.db.read(plugin_releases::non_active_rows).await?;
    for release in rows {
        let path = PathBuf::from(release["path"].as_str().unwrap_or(""));
        let version = release["version"].as_str().unwrap_or("");
        if !path.starts_with(&plugins_root) {
            continue;
        }
        let trash = path.with_file_name(format!(".removing-{version}"));
        let id = release["plugin_id"].as_str().unwrap_or("").to_owned();
        if release["state"] != json!("removing") {
            // Compatibility with an older interrupted rename: a missing release plus its
            // tombstone is the only evidence strong enough to infer removal intent.
            if path.exists() || !trash.exists() {
                continue;
            }
            let (record_id, record_version) = (id.clone(), version.to_owned());
            let intent_at = crate::jobs::now();
            core.db
                .write(move |tx| {
                    plugin_releases::begin_remove(tx, &record_id, &record_version, &intent_at)
                })
                .await?;
        }
        if let Err(error) = remove_release_files(&path, &trash) {
            eprintln!(
                "plugin removal remains pending for {id} {version}: {error}; the archive stays available"
            );
            continue;
        }
        let v = version.to_owned();
        core.db
            .write(move |tx| {
                plugin_releases::finish_remove(tx, &id, &v)?;
                crate::store::management::record(
                    tx,
                    "plugin_remove_recovered",
                    &id,
                    &json!({"version": v}),
                    &crate::jobs::now(),
                )
            })
            .await?;
    }
    Ok(())
}

/// Called at startup: the active installed release replaces the bundled adapter for its platform.
pub async fn restore_active(core: &Arc<Core>) -> Result<(), CoreError> {
    let releases = core.db.read(plugin_releases::active_all).await?;
    for release in releases {
        let Ok(spec) = driver(core, &release) else {
            continue;
        };
        core.host.replace_spec(spec).await;
    }
    Ok(())
}

/// Capabilities the active release declares, for the UI's badges and the routing gate.
pub async fn declared_capabilities(
    core: &Arc<Core>,
    plugin_id: &str,
) -> Result<Vec<String>, CoreError> {
    let release = {
        let id = plugin_id.to_owned();
        core.db
            .read(move |c| plugin_releases::active(c, &id))
            .await?
    };
    Ok(release
        .as_ref()
        .and_then(|r| r["capabilities"].as_array())
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default())
}
