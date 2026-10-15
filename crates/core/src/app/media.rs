//! Media access: short-lived read leases and handing a verified copy to the OS viewer.
use super::{parse, plus_secs, require_str, unsupported};
use crate::core::{Core, MediaLease};
use crate::error::CoreError;
use crate::{config, jobs, store};
use base64::Engine as _;
use serde_json::Value;
use std::ffi::OsStr;
use std::sync::Arc;
use std::time::{Duration, Instant};
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

const LEASE_LIMIT: usize = 256;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = &req.args;
    match req.command.as_str() {
        cmd::MEDIA_RELEASE => {
            let id = require_str(args, "lease_id", "lease id")?;
            core.leases.lock().unwrap().remove(&id);
            Ok(serde_json::json!({"released": true}))
        }
        cmd::MEDIA_OPEN_EXTERNAL => open_external(core, parse(args.clone())?).await,
        cmd::MEDIA_LEASE => lease(core, parse(args.clone())?).await,
        cmd::MEDIA_READ => read_range(core, parse(args.clone())?),
        other => Err(unsupported(other)),
    }
}

/// Opens a path or URL with the Windows shell; no console window appears.
pub(crate) fn launch_explorer(target: &OsStr) -> Result<(), CoreError> {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        std::process::Command::new("explorer.exe")
            .arg(target)
            .creation_flags(CREATE_NO_WINDOW)
            .spawn()?;
    }
    #[cfg(not(windows))]
    let _ = target;
    Ok(())
}

/// Shows a file (selected in its folder) or a folder in the system's file explorer. The path
/// comes from Core, never from the interface.
pub(crate) fn reveal_in_explorer(target: &OsStr, is_dir: bool) -> Result<(), CoreError> {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        let mut command = std::process::Command::new("explorer.exe");
        if is_dir {
            command.arg(target);
        } else {
            command.raw_arg(format!("/select,\"{}\"", target.to_string_lossy()));
        }
        command.creation_flags(CREATE_NO_WINDOW).spawn()?;
    }
    #[cfg(not(windows))]
    let _ = (target, is_dir);
    Ok(())
}

pub(crate) fn external_extension(mime: &str) -> Result<&'static str, CoreError> {
    Ok(match mime {
        "video/mp4" => "mp4",
        "audio/mp4" => "m4a",
        "video/webm" => "webm",
        "video/x-flv" => "flv",
        "video/mp2t" => "ts",
        "audio/aac" => "aac",
        "image/jpeg" => "jpg",
        "image/png" => "png",
        "image/webp" => "webp",
        _ => return Err(CoreError::Unsupported("external media type".into())),
    })
}

/// Copies the verified blob to a core-owned path, checking the hash before and after.
async fn copy_verified(
    source: std::path::PathBuf,
    expected: String,
    output: std::path::PathBuf,
) -> Result<(), CoreError> {
    tokio::task::spawn_blocking(move || -> Result<(), CoreError> {
        if crate::objects::hash_file(&source)? != expected {
            return Err(CoreError::InvalidInput("media checksum mismatch".into()));
        }
        std::fs::copy(source, &output)?;
        if crate::objects::hash_file(&output)? != expected {
            return Err(CoreError::InvalidInput("media copy mismatch".into()));
        }
        Ok(())
    })
    .await
    .map_err(|_| CoreError::Internal("media preparation failed".into()))?
}

async fn open_external(core: &Arc<Core>, a: ctl::MediaLeaseArgs) -> Result<Value, CoreError> {
    let _gate = core.archive_access.read().await;
    let sha = crate::paths::blob_sha(&a.blob_id)?.to_owned();
    let blob = a.blob_id;
    let mime = core
        .db
        .read(move |c| store::media::verified_mime(c, &blob))
        .await?;
    let extension = external_extension(&mime)?;
    let source = core.objects.object_path(&sha)?;
    let folder = core.profile.data_root.join("open-media");
    crate::paths::no_links(&folder)?;
    std::fs::create_dir_all(&folder)?;
    let target = folder.join(format!("{sha}.{extension}"));
    crate::paths::no_links(&target)?;
    copy_verified(source, sha, target.clone()).await?;
    // Only an allowlisted extension and a core-generated path ever reach the OS launcher.
    launch_explorer(std::fs::canonicalize(target)?.as_os_str())?;
    Ok(serde_json::json!({"opened": true}))
}

async fn lease(core: &Arc<Core>, a: ctl::MediaLeaseArgs) -> Result<Value, CoreError> {
    let _gate = core.archive_access.read().await;
    let sha = crate::paths::blob_sha(&a.blob_id)?.to_owned();
    let blob = format!("sha256:{sha}");
    let (size, mime) = core
        .db
        .read(move |c| store::media::verified_size_and_mime(c, &blob))
        .await?;
    if core.objects.blob_size(&sha)? != size as u64 {
        return Err(CoreError::NotFound("media missing or changed".into()));
    }
    let lease_id = format!("lease-{}", uuid::Uuid::new_v4());
    {
        let mut leases = core.leases.lock().unwrap();
        leases.retain(|_, l| l.expires_at > Instant::now());
        if leases.len() >= LEASE_LIMIT {
            return Err(CoreError::InvalidInput("media lease limit".into()));
        }
    }
    core.leases.lock().unwrap().insert(
        lease_id.clone(),
        MediaLease {
            blob_sha: sha,
            size,
            mime: mime.clone(),
            expires_at: Instant::now() + Duration::from_secs(config::MEDIA_LEASE_TTL_SECS),
        },
    );
    Ok(serde_json::to_value(ctl::MediaLeaseDto {
        lease_id,
        blob_id: a.blob_id,
        size,
        mime,
        expires_at: plus_secs(&jobs::now(), config::MEDIA_LEASE_TTL_SECS as i64),
    })?)
}

fn read_range(core: &Arc<Core>, a: ctl::MediaReadArgs) -> Result<Value, CoreError> {
    if a.offset < 0 || a.length <= 0 || a.length > config::MEDIA_CHUNK_MAX {
        return Err(CoreError::InvalidInput("length".into()));
    }
    let lease = core.leases.lock().unwrap().get(&a.lease_id).cloned();
    let Some(lease) = lease else {
        return Err(CoreError::NotFound("lease".into()));
    };
    if lease.expires_at < Instant::now() {
        core.leases.lock().unwrap().remove(&a.lease_id);
        return Err(CoreError::NotFound("lease expired".into()));
    }
    let data = core
        .objects
        .read_range(&lease.blob_sha, a.offset as u64, a.length as u64)?;
    let eof = (a.offset + data.len() as i64) >= lease.size;
    Ok(serde_json::to_value(ctl::MediaChunkDto {
        lease_id: a.lease_id,
        offset: a.offset,
        eof,
        data_b64: base64::engine::general_purpose::STANDARD.encode(&data),
    })?)
}
