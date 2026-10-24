//! User-clicked yt-dlp updates (DEC-039).
//!
//! TRACE never runs yt-dlp's `-U`. The user asks to check, sees the version, and confirms an
//! installation: Core fetches the release's `SHA2-256SUMS` and detached signature, verifies the
//! signature with the pinned release key, verifies the executable digest, installs the new
//! version side by side, checks that it starts and reports the expected version, then switches
//! only while no tool run is in flight. The previous version is kept for one-click rollback.
//! Failures leave the current tools untouched and are recorded; an unverified download is
//! deleted rather than installed.
mod signature;

use crate::core::Core;
use crate::error::CoreError;
use crate::network::Route;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;
use trace_domain::time::{TimePrecision, TraceTime};

pub const CHECK_KEY: &str = "tool:yt-dlp:check";
pub const ACTIVE_KEY: &str = "tool:yt-dlp:active";
pub const ERROR_KEY: &str = "tool:yt-dlp:error";
const LOCK: &str = include_str!("../../../../media-tools.lock.json");
const RELEASE_API: &str = "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest";
const RELEASE_DOWNLOAD: &str = "https://github.com/yt-dlp/yt-dlp/releases/download";
const MAX_TOOL_BYTES: u64 = 128 * 1024 * 1024;

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct Check {
    pub latest: String,
    pub current: String,
    pub newer: bool,
    pub checked_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
struct Active {
    version: String,
    previous: String,
    installed_at: String,
}

fn locked_version() -> String {
    serde_json::from_str::<Value>(LOCK)
        .ok()
        .and_then(|lock| lock["yt_dlp"]["version"].as_str().map(str::to_owned))
        .unwrap_or_default()
}

fn tools_root(core: &Core) -> PathBuf {
    core.objects
        .profile()
        .data_root
        .join("tools")
        .join("yt-dlp")
}

async fn setting(core: &Core, key: &str) -> Result<Option<String>, CoreError> {
    let key = key.to_owned();
    core.db
        .read(move |c| crate::store::downloads::setting(c, &key))
        .await
}

async fn put(core: &Core, key: &str, value: &str) -> Result<(), CoreError> {
    let key = key.to_owned();
    let value = value.to_owned();
    let now = TraceTime::now(TimePrecision::Discovery).to_string();
    core.db
        .write(move |tx| crate::store::downloads::put_setting(tx, &key, &value, &now))
        .await
}

fn http_client(route: Route) -> Result<reqwest::Client, CoreError> {
    let mut builder = reqwest::Client::builder()
        .timeout(Duration::from_secs(120))
        .connect_timeout(Duration::from_secs(20))
        .user_agent(format!("TRACE/{}", env!("CARGO_PKG_VERSION")))
        .redirect(reqwest::redirect::Policy::limited(10));
    if let Route::Proxy(proxy) = route {
        let mut endpoint = reqwest::Proxy::all(&proxy.url)
            .map_err(|e| CoreError::Network(format!("proxy rejected: {e}")))?;
        if let Some((user, password)) = proxy.auth {
            endpoint = endpoint.basic_auth(&user, &password);
        }
        builder = builder.proxy(endpoint);
    }
    builder
        .build()
        .map_err(|e| CoreError::Network(format!("http client: {e}")))
}

async fn get(
    client: &reqwest::Client,
    url: &str,
    limit: u64,
) -> Result<(Vec<u8>, Option<String>), CoreError> {
    let response = client
        .get(url)
        .header("Accept", "application/vnd.github+json")
        .send()
        .await
        .map_err(|e| CoreError::Network(format!("fetch {url}: {e}")))?;
    if !response.status().is_success() {
        return Err(CoreError::Network(format!(
            "fetch {url}: HTTP {}",
            response.status().as_u16()
        )));
    }
    if let Some(length) = response.content_length()
        && length > limit
    {
        return Err(CoreError::Network(format!(
            "fetch {url}: response is larger than {limit} bytes"
        )));
    }
    let etag = response
        .headers()
        .get(reqwest::header::ETAG)
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);
    let bytes = response
        .bytes()
        .await
        .map_err(|e| CoreError::Network(format!("read {url}: {e}")))?;
    if bytes.len() as u64 > limit {
        return Err(CoreError::Network(format!(
            "fetch {url}: response is larger than {limit} bytes"
        )));
    }
    Ok((bytes.to_vec(), etag))
}

fn parse_release(body: &[u8], current: &str) -> Result<Check, CoreError> {
    let value: Value = serde_json::from_slice(body)
        .map_err(|_| CoreError::Network("release answer is not JSON".into()))?;
    if value["draft"].as_bool() == Some(true) || value["prerelease"].as_bool() == Some(true) {
        return Err(CoreError::Unsupported(
            "最新发布是预发布版本，暂不提供".into(),
        ));
    }
    let latest = value["tag_name"]
        .as_str()
        .filter(|tag| valid_version(tag))
        .ok_or_else(|| CoreError::Network("release has no usable version".into()))?
        .to_owned();
    let assets: Vec<&str> = value["assets"]
        .as_array()
        .map(|list| list.iter().filter_map(|a| a["name"].as_str()).collect())
        .unwrap_or_default();
    for required in ["yt-dlp.exe", "SHA2-256SUMS", "SHA2-256SUMS.sig"] {
        if !assets.contains(&required) {
            return Err(CoreError::Network(format!(
                "release {latest} does not offer {required}"
            )));
        }
    }
    Ok(Check {
        newer: newer(&latest, current),
        latest,
        current: current.to_owned(),
        checked_at: TraceTime::now(TimePrecision::Discovery).to_string(),
    })
}

/// Stable releases are dated `YYYY.MM.DD` with an optional build suffix.
fn valid_version(version: &str) -> bool {
    !version.is_empty()
        && version.len() <= 32
        && version.bytes().all(|b| b.is_ascii_digit() || b == b'.')
        && !version.starts_with('.')
        && !version.ends_with('.')
        && !version.contains("..")
}

fn parts(version: &str) -> Vec<u64> {
    version
        .split('.')
        .map(|part| part.parse().unwrap_or(0))
        .collect()
}

fn newer(latest: &str, current: &str) -> bool {
    let latest = parts(latest);
    let current = parts(current);
    for index in 0..latest.len().max(current.len()) {
        let a = latest.get(index).copied().unwrap_or(0);
        let b = current.get(index).copied().unwrap_or(0);
        if a != b {
            return a > b;
        }
    }
    false
}

/// The digest for one file in `SHA2-256SUMS`; the format is `<hex>  <name>`.
fn sums_digest(text: &str, name: &str) -> Option<String> {
    text.lines().find_map(|line| {
        let mut fields = line.split_whitespace();
        let digest = fields.next()?;
        let file = fields.next()?;
        (file == name && digest.len() == 64 && digest.bytes().all(|b| b.is_ascii_hexdigit()))
            .then(|| digest.to_ascii_lowercase())
    })
}

fn digest(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

async fn check(core: &Core) -> Result<Check, CoreError> {
    let current = core
        .media_tools
        .verified()
        .await
        .unwrap_or_else(|_| locked_version());
    let route = core.network.route_for("youtube");
    let client = http_client(route)?;
    let (body, _) = get(&client, RELEASE_API, 4 * 1024 * 1024).await?;
    let check = parse_release(&body, &current)?;
    let text = serde_json::to_string(&check)?;
    put(core, CHECK_KEY, &text).await?;
    Ok(check)
}

async fn status(core: &Core) -> Result<Value, CoreError> {
    let tools = core.media_tools.status().await;
    let check = setting(core, CHECK_KEY)
        .await?
        .and_then(|text| serde_json::from_str::<Check>(&text).ok());
    let active = setting(core, ACTIVE_KEY)
        .await?
        .and_then(|text| serde_json::from_str::<Active>(&text).ok());
    let error = setting(core, ERROR_KEY).await?;
    Ok(json!({
        "tool": "yt-dlp",
        "locked": locked_version(),
        "current": tools["version"],
        "ready": tools["ready"],
        "source": tools["source"],
        "path": tools["yt_dlp"],
        "reason": tools["reason"],
        "update": check,
        "installed": active,
        "error": error,
    }))
}

fn stage(
    root: &Path,
    version: &str,
    exe: &[u8],
    deno: &Path,
    previous: &str,
) -> Result<PathBuf, CoreError> {
    let final_dir = root.join(version);
    let staging = root.join(format!(".staging-{version}"));
    if staging.exists() {
        std::fs::remove_dir_all(&staging)?;
    }
    std::fs::create_dir_all(&staging)?;
    let staged_exe = staging.join(format!("yt-dlp{}", std::env::consts::EXE_SUFFIX));
    std::fs::write(&staged_exe, exe)?;
    let staged_deno = staging.join(format!("deno{}", std::env::consts::EXE_SUFFIX));
    if std::fs::hard_link(deno, &staged_deno).is_err() {
        std::fs::copy(deno, &staged_deno)?;
    }
    let manifest = json!({
        "yt_dlp": {"version": version, "sha256": digest(exe)},
        "previous": previous,
        "deno": {
            "version": serde_json::from_str::<Value>(LOCK)?["deno"]["version"],
            "sha256": digest(&std::fs::read(&staged_deno)?),
        }
    });
    std::fs::write(
        staging.join("update.json"),
        serde_json::to_vec_pretty(&manifest)?,
    )?;
    if final_dir.exists() {
        std::fs::remove_dir_all(&final_dir)?;
    }
    std::fs::rename(&staging, &final_dir)?;
    Ok(final_dir)
}

fn digest_of(path: &Path) -> Option<String> {
    std::fs::read(path).ok().map(|bytes| digest(&bytes))
}

/// Checks an installed update before it is used: the files are the ones its own manifest
/// names, both programs start and report the versions in it. A failed check never activates.
async fn installed_verifies(dir: &Path, version: &str) -> bool {
    let Ok(bytes) = std::fs::read(dir.join("update.json")) else {
        return false;
    };
    let Ok(manifest) = serde_json::from_slice::<Value>(&bytes) else {
        return false;
    };
    if manifest["yt_dlp"]["version"] != json!(version) {
        return false;
    }
    let exe = dir.join(format!("yt-dlp{}", std::env::consts::EXE_SUFFIX));
    let deno = dir.join(format!("deno{}", std::env::consts::EXE_SUFFIX));
    let Some(exe_digest) = manifest["yt_dlp"]["sha256"].as_str() else {
        return false;
    };
    let Some(deno_digest) = manifest["deno"]["sha256"].as_str() else {
        return false;
    };
    if digest_of(&exe).as_deref() != Some(exe_digest) {
        return false;
    }
    if digest_of(&deno).as_deref() != Some(deno_digest) {
        return false;
    }
    if crate::media_tools::MediaTools::version_of(&exe)
        .await
        .as_deref()
        != Some(version)
    {
        return false;
    }
    let expected_deno = manifest["deno"]["version"].as_str().unwrap_or_default();
    crate::media_tools::MediaTools::version_of(&deno)
        .await
        .as_deref()
        == Some(expected_deno)
}

fn previous_from_update(dir: &Path) -> Option<String> {
    std::fs::read(dir.join("update.json"))
        .ok()
        .and_then(|bytes| serde_json::from_slice::<Value>(&bytes).ok())
        .and_then(|manifest| manifest["previous"].as_str().map(str::to_owned))
}

async fn deno_ready(core: &Core) -> Result<PathBuf, CoreError> {
    let deno = core
        .media_tools
        .bundled_deno()
        .ok_or_else(|| CoreError::Unsupported("找不到随包的 Deno，无法安装更新".into()))?;
    let expected = serde_json::from_str::<Value>(LOCK)?["deno"]["version"]
        .as_str()
        .unwrap_or_default()
        .to_owned();
    if crate::media_tools::MediaTools::version_of(&deno)
        .await
        .as_deref()
        != Some(expected.as_str())
    {
        return Err(CoreError::Unsupported(
            "随包的 Deno 不可用或版本不符，本次不安装更新".into(),
        ));
    }
    Ok(deno)
}

async fn apply(core: &Arc<Core>, version: &str) -> Result<Value, CoreError> {
    if !valid_version(version) {
        return Err(CoreError::InvalidInput("版本号格式不正确".into()));
    }
    let check = setting(core, CHECK_KEY)
        .await?
        .and_then(|text| serde_json::from_str::<Check>(&text).ok())
        .ok_or_else(|| CoreError::InvalidInput("请先检查更新".into()))?;
    if check.latest != version || !check.newer {
        return Err(CoreError::InvalidInput(
            "这个版本不是刚刚检查到的新版本".into(),
        ));
    }
    let current = core
        .media_tools
        .verified()
        .await
        .unwrap_or_else(|_| locked_version());
    // F-081: the same version must be refused before any download, directory removal or
    // manifest/pointer write; otherwise `previous` would point at itself and the original
    // rollback chain would be lost. A check made against another active version is stale.
    if version == current {
        return Err(CoreError::InvalidInput(
            "这个版本已经是当前版本，不需要重新安装".into(),
        ));
    }
    if check.current != current {
        return Err(CoreError::VersionConflict(
            "更新检查已过期：当前工具版本已经变化，请重新检查".into(),
        ));
    }
    let deno = deno_ready(core).await?;
    let client = http_client(core.network.route_for("youtube"))?;
    let base = format!("{RELEASE_DOWNLOAD}/{version}");
    let (sums, _) = get(&client, &format!("{base}/SHA2-256SUMS"), 64 * 1024).await?;
    let (sig, _) = get(&client, &format!("{base}/SHA2-256SUMS.sig"), 64 * 1024).await?;
    signature::verify(&sums, &sig).map_err(|reason| {
        CoreError::Unsupported(format!("更新签名校验失败，未安装任何文件：{reason}"))
    })?;
    let sums_text = String::from_utf8_lossy(&sums);
    let wanted = sums_digest(
        &sums_text,
        &format!("yt-dlp{}", std::env::consts::EXE_SUFFIX),
    )
    .ok_or_else(|| CoreError::Unsupported("签名清单里没有 yt-dlp 可执行文件".into()))?;
    let (exe, _) = get(
        &client,
        &format!("{base}/yt-dlp{}", std::env::consts::EXE_SUFFIX),
        MAX_TOOL_BYTES,
    )
    .await?;
    if digest(&exe) != wanted {
        return Err(CoreError::Unsupported(
            "下载的可执行文件摘要与签名清单不符，未安装".into(),
        ));
    }
    // Start it once before switching: a version that does not start is never activated.
    let probe = tempfile::Builder::new().prefix("tool-probe-").tempdir()?;
    let probe_exe = probe
        .path()
        .join(format!("yt-dlp{}", std::env::consts::EXE_SUFFIX));
    std::fs::write(&probe_exe, &exe)?;
    if crate::media_tools::MediaTools::version_of(&probe_exe)
        .await
        .as_deref()
        != Some(version)
    {
        return Err(CoreError::Unsupported(
            "新版本启动检查未通过，未安装".into(),
        ));
    }
    // F-082: the switch shares the held permit of a real tool run. A run that starts after this
    // point waits; one already in flight makes the switch wait instead of racing it.
    let _switch = core.media_tools.exclusive().await?;
    let still_current = core
        .media_tools
        .active_version()
        .unwrap_or_else(locked_version);
    if still_current != current {
        return Err(CoreError::VersionConflict(
            "另一个更新已经切换；本次未安装".into(),
        ));
    }
    let root = tools_root(core);
    std::fs::create_dir_all(&root)?;
    let previous = current;
    if previous == version {
        return Err(CoreError::InvalidInput(
            "更新链不能指回自身；本次未安装".into(),
        ));
    }
    let installed = match stage(&root, version, &exe, &deno, &previous) {
        Ok(dir) => dir,
        Err(error) => {
            put(core, ERROR_KEY, &error.to_string()).await?;
            return Err(error);
        }
    };
    if !installed_verifies(&installed, version).await {
        let message = "新版本的启动或摘要检查未通过，未切换；当前工具未改变";
        put(core, ERROR_KEY, message).await?;
        return Err(CoreError::Unsupported(message.into()));
    }
    if !core.media_tools.activate(installed, version) {
        let message = "新版本切换失败，当前工具未改变";
        put(core, ERROR_KEY, message).await?;
        return Err(CoreError::Unsupported(message.into()));
    }
    let active = Active {
        version: version.to_owned(),
        previous,
        installed_at: TraceTime::now(TimePrecision::Discovery).to_string(),
    };
    put(core, ACTIVE_KEY, &serde_json::to_string(&active)?).await?;
    put(core, ERROR_KEY, "").await?;
    status(core).await
}

async fn rollback(core: &Core) -> Result<Value, CoreError> {
    let _switch = core.media_tools.exclusive().await?;
    let active = setting(core, ACTIVE_KEY)
        .await?
        .and_then(|text| serde_json::from_str::<Active>(&text).ok())
        .ok_or_else(|| CoreError::InvalidInput("没有可回退的更新".into()))?;
    let locked = locked_version();
    let target = active.previous.clone();
    if target == active.version {
        return Err(CoreError::Unsupported(
            "回退链指向自身，当前工具未改变".into(),
        ));
    }
    let next_previous = if target == locked {
        core.media_tools
            .verify_bundled()
            .await
            .map_err(|_| CoreError::Unsupported("随包版本校验失败，当前工具未改变".into()))?;
        core.media_tools.restore_bundled();
        None
    } else {
        let dir = tools_root(core).join(&target);
        if !installed_verifies(&dir, &target).await {
            return Err(CoreError::Unsupported(format!(
                "回退版本 {} 的文件已不完整",
                target
            )));
        }
        let next = previous_from_update(&dir).unwrap_or_else(|| locked.clone());
        if !core.media_tools.activate(dir, &target) {
            return Err(CoreError::Unsupported(format!(
                "回退版本 {} 无法切换，当前工具未改变",
                target
            )));
        }
        Some(next)
    };
    if let Some(previous) = next_previous {
        let active = Active {
            version: target,
            previous,
            installed_at: TraceTime::now(TimePrecision::Discovery).to_string(),
        };
        put(core, ACTIVE_KEY, &serde_json::to_string(&active)?).await?;
    } else {
        put(core, ACTIVE_KEY, "").await?;
    }
    status(core).await
}

/// Startup check (DEC-039): an installed update is used only while its manifest verifies;
/// otherwise TRACE falls back to the bundled tools and records why.
pub async fn restore(core: &Arc<Core>) -> Result<(), CoreError> {
    let Some(active) = setting(core, ACTIVE_KEY)
        .await?
        .and_then(|text| serde_json::from_str::<Active>(&text).ok())
    else {
        return Ok(());
    };
    let dir = tools_root(core).join(&active.version);
    if !installed_verifies(&dir, &active.version).await
        || !core.media_tools.activate(dir, &active.version)
    {
        core.media_tools.restore_bundled();
        put(
            core,
            ERROR_KEY,
            "已安装的工具更新校验失败，已回到随包版本；可回退或重新安装",
        )
        .await?;
    }
    Ok(())
}

pub async fn route(core: &Arc<Core>, command: &str, args: Value) -> Result<Value, CoreError> {
    match command {
        "tool.status" => status(core).await,
        "tool.check_update" => Ok(serde_json::to_value(check(core).await?)?),
        "tool.apply_update" => {
            let version = args["version"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("缺少版本号".into()))?;
            apply(core, version).await
        }
        "tool.rollback" => rollback(core).await,
        _ => Err(CoreError::Unsupported(format!("unknown command {command}"))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dated_versions_compare_numerically() {
        assert!(newer("2026.09.01", "2026.08.19"));
        assert!(newer("2026.08.19.1", "2026.08.19"));
        assert!(!newer("2026.08.19", "2026.08.19"));
        assert!(!newer("2025.12.31", "2026.01.01"));
        for bad in ["", ".", "..", "v2026.08.19", "2026..08", "2026.08.19/"] {
            assert!(!valid_version(bad), "{bad}");
        }
    }

    #[test]
    fn sums_lines_name_the_executable() {
        let text = "abc  yt-dlp\n\
                    0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  yt-dlp.exe\n\
                    ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff  yt-dlp_x86.exe\n";
        assert_eq!(
            sums_digest(text, "yt-dlp.exe").as_deref(),
            Some("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
        );
        assert!(sums_digest(text, "other.exe").is_none());
    }

    #[test]
    fn a_release_answer_without_the_executable_is_refused() {
        let body = br#"{"tag_name":"2026.09.01","draft":false,"prerelease":false,"assets":[{"name":"yt-dlp"}]}"#;
        assert!(parse_release(body, "2026.08.19").is_err());
        let body = br#"{"tag_name":"2026.09.01","draft":false,"prerelease":false,"assets":[{"name":"yt-dlp.exe"},{"name":"SHA2-256SUMS"},{"name":"SHA2-256SUMS.sig"}]}"#;
        let check = parse_release(body, "2026.08.19").unwrap();
        assert!(check.newer);
        assert_eq!(check.latest, "2026.09.01");
    }

    async fn core() -> (tempfile::TempDir, Arc<Core>) {
        let dir = tempfile::tempdir().unwrap();
        let core = Core::open(crate::config::Profile {
            data_root: dir.path().join("data"),
            archive_root: dir.path().join("archive"),
            vault_dir: Some(dir.path().join("vault")),
            ..Default::default()
        })
        .await
        .unwrap();
        (dir, core)
    }

    #[tokio::test]
    async fn a_status_call_never_checks_for_an_update_by_itself() {
        let (_dir, core) = core().await;
        let value = route(&core, "tool.status", json!({})).await.unwrap();
        assert_eq!(value["tool"], "yt-dlp");
        assert_eq!(value["locked"], locked_version());
        assert!(
            value["update"].is_null(),
            "no update is checked without a click"
        );
        assert!(setting(&core, CHECK_KEY).await.unwrap().is_none());
        assert!(value["installed"].is_null());
    }

    #[tokio::test]
    async fn a_version_that_was_not_just_checked_is_refused_before_any_download() {
        let (_dir, core) = core().await;
        let error = route(&core, "tool.apply_update", json!({"version": "2099.01.01"}))
            .await
            .unwrap_err();
        assert!(matches!(error, CoreError::InvalidInput(_)), "{error}");
        assert!(error.to_string().contains("请先检查更新"), "{error}");
        assert!(setting(&core, CHECK_KEY).await.unwrap().is_none());
    }

    #[tokio::test]
    async fn an_installed_update_must_match_its_manifest_and_start() {
        let dir = tempfile::tempdir().unwrap();
        let exe = dir
            .path()
            .join(format!("yt-dlp{}", std::env::consts::EXE_SUFFIX));
        let deno = dir
            .path()
            .join(format!("deno{}", std::env::consts::EXE_SUFFIX));
        std::fs::write(&exe, b"not a program").unwrap();
        std::fs::write(&deno, b"not a program").unwrap();
        let manifest = json!({
            "yt_dlp": {"version": "2026.09.01", "sha256": digest(b"not a program")},
            "deno": {"version": "2.9.7", "sha256": digest(b"not a program")},
        });
        std::fs::write(
            dir.path().join("update.json"),
            serde_json::to_vec(&manifest).unwrap(),
        )
        .unwrap();
        assert!(
            !installed_verifies(dir.path(), "2026.09.01").await,
            "a file that does not start is not activated"
        );
        std::fs::write(&exe, b"tampered").unwrap();
        assert!(
            !installed_verifies(dir.path(), "2026.09.01").await,
            "a file that no longer matches its manifest is not activated"
        );
        assert!(!installed_verifies(dir.path(), "2026.10.01").await);
    }

    fn version_dir(root: &Path, version: &str, previous: &str) -> PathBuf {
        let dir = root.join(version);
        std::fs::create_dir_all(&dir).unwrap();
        let exe = dir.join(format!("yt-dlp{}", std::env::consts::EXE_SUFFIX));
        let deno = dir.join(format!("deno{}", std::env::consts::EXE_SUFFIX));
        std::fs::write(&exe, format!("synthetic {version}")).unwrap();
        std::fs::write(&deno, b"synthetic deno").unwrap();
        let manifest = json!({
            "yt_dlp": {"version": version, "sha256": digest(&std::fs::read(&exe).unwrap())},
            "previous": previous,
            "deno": {"version": "2.9.7", "sha256": digest(&std::fs::read(&deno).unwrap())},
        });
        std::fs::write(
            dir.join("update.json"),
            serde_json::to_vec(&manifest).unwrap(),
        )
        .unwrap();
        crate::media_tools::set_test_version(&exe, version);
        crate::media_tools::set_test_version(&deno, "2.9.7");
        dir
    }

    async fn active(core: &Arc<Core>, version: &str, previous: &str) {
        put(
            core,
            ACTIVE_KEY,
            &serde_json::to_string(&Active {
                version: version.into(),
                previous: previous.into(),
                installed_at: "t".into(),
            })
            .unwrap(),
        )
        .await
        .unwrap();
    }

    #[tokio::test]
    async fn rollback_survives_restarts_and_a_damaged_candidate_keeps_the_current_version() {
        let (_dir, mut core) = core().await;
        let root = tools_root(&core);
        std::fs::create_dir_all(&root).unwrap();
        let v1 = version_dir(&root, "2026.01.01", "2025.12.01");
        let v2 = version_dir(&root, "2026.02.01", "2026.01.01");
        let v3 = version_dir(&root, "2026.03.01", "2026.02.01");
        core.media_tools.activate(v3.clone(), "2026.03.01");
        active(&core, "2026.03.01", "2026.02.01").await;
        let first = rollback(&core).await.unwrap();
        assert_eq!(first["current"], "2026.02.01");
        assert!(
            setting(&core, ACTIVE_KEY)
                .await
                .unwrap()
                .unwrap()
                .contains("2026.01.01")
        );
        // A fresh Core must restore both the active version and its next rollback target.
        let profile = core.profile.clone();
        core.shutdown().await;
        core = Core::open(profile.clone()).await.unwrap();
        assert_eq!(
            core.media_tools.active_version().as_deref(),
            Some("2026.02.01")
        );
        let second = rollback(&core).await.unwrap();
        assert_eq!(second["current"], "2026.01.01");
        core.shutdown().await;
        core = Core::open(profile).await.unwrap();
        assert_eq!(
            core.media_tools.active_version().as_deref(),
            Some("2026.01.01")
        );
        // A candidate that no longer verifies is refused without touching the active version.
        std::fs::remove_file(v2.join(format!("yt-dlp{}", std::env::consts::EXE_SUFFIX))).unwrap();
        core.media_tools.activate(v3, "2026.03.01");
        active(&core, "2026.03.01", "2026.02.01").await;
        assert!(rollback(&core).await.is_err());
        assert_eq!(
            core.media_tools.active_version().as_deref(),
            Some("2026.03.01")
        );
        assert!(
            setting(&core, ACTIVE_KEY)
                .await
                .unwrap()
                .unwrap()
                .contains("2026.03.01")
        );
        restore(&core).await.unwrap();
        assert_eq!(
            core.media_tools.active_version().as_deref(),
            Some("2026.03.01")
        );
        assert!(v1.exists());
    }

    #[tokio::test]
    async fn the_same_version_and_a_stale_check_are_refused_before_any_change() {
        let (_dir, core) = core().await;
        let root = tools_root(&core);
        std::fs::create_dir_all(&root).unwrap();
        let v1 = version_dir(&root, "2026.01.01", "2025.12.01");
        let v2 = version_dir(&root, "2026.02.01", "2026.01.01");
        std::fs::write(v1.join("marker.txt"), b"keep me").unwrap();
        core.media_tools.activate(v1.clone(), "2026.01.01");
        active(&core, "2026.01.01", "2025.12.01").await;
        put(
            &core,
            CHECK_KEY,
            &serde_json::to_string(&Check {
                latest: "2026.01.01".into(),
                current: "2026.01.01".into(),
                newer: true,
                checked_at: "t".into(),
            })
            .unwrap(),
        )
        .await
        .unwrap();
        let error = apply(&core, "2026.01.01").await.unwrap_err();
        assert!(error.to_string().contains("已经是当前版本"), "{error}");
        assert!(v1.join("marker.txt").exists());
        assert_eq!(
            core.media_tools.active_version().as_deref(),
            Some("2026.01.01")
        );
        // A check made against another current version is stale even if it names a newer one.
        core.media_tools.activate(v2.clone(), "2026.02.01");
        active(&core, "2026.02.01", "2026.01.01").await;
        put(
            &core,
            CHECK_KEY,
            &serde_json::to_string(&Check {
                latest: "2026.03.01".into(),
                current: "2026.01.01".into(),
                newer: true,
                checked_at: "t".into(),
            })
            .unwrap(),
        )
        .await
        .unwrap();
        let error = apply(&core, "2026.03.01").await.unwrap_err();
        assert!(error.to_string().contains("过期"), "{error}");
        assert!(v1.join("marker.txt").exists());
        assert_eq!(
            core.media_tools.active_version().as_deref(),
            Some("2026.02.01")
        );
    }

    #[tokio::test]
    async fn a_switch_waits_for_a_tool_run_and_holds_the_same_permit() {
        let (_dir, core) = core().await;
        let run = core.media_tools.exclusive().await.unwrap();
        let mut waiting = {
            let core = core.clone();
            tokio::spawn(async move { rollback(&core).await })
        };
        assert!(
            tokio::time::timeout(Duration::from_millis(100), &mut waiting)
                .await
                .is_err(),
            "the switch must not overtake an in-flight tool run"
        );
        drop(run);
        let error = waiting.await.unwrap().unwrap_err();
        assert!(error.to_string().contains("没有可回退"), "{error}");
    }
}
