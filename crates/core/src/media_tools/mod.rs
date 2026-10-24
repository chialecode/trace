//! The locked external media tools (docs/design/multi-site-contracts.md §10): yt-dlp, with the
//! Deno runtime it asks for, answers which formats a YouTube video offers and where they are.
//!
//! Like FFmpeg they are replaceable executables that own no identity, path, success judgment
//! or recycling rule. Core runs them with fixed arguments, an empty environment, a time and
//! output limit and no credentials, then downloads and verifies the files itself. For a site
//! whose own video address the site may refuse (TikTok), the tool also downloads the file into
//! a directory of its own, as a second way, and Core takes the file from there.
mod fallback;
mod formats;
#[cfg(test)]
mod pipeline_tests;
mod plan;
mod run;
#[cfg(test)]
mod tests;

use crate::error::CoreError;
use serde_json::{Value, json};
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, RwLock};
use std::time::{Duration, Instant};
use trace_contracts::rules::{RuleDeclaration, RuleStatus};

pub(crate) use fallback::{fetch as fetch_replacement, replaceable};
pub(crate) use formats::ranged_chunk;
pub(crate) use plan::{complete, complete_source};

#[cfg(test)]
static TEST_VERSIONS: std::sync::LazyLock<
    std::sync::Mutex<std::collections::HashMap<PathBuf, String>>,
> = std::sync::LazyLock::new(|| std::sync::Mutex::new(std::collections::HashMap::new()));

/// Dynamic update tests use stand-in executables; production probing always starts the tool.
#[cfg(test)]
pub(crate) fn set_test_version(path: &Path, version: &str) {
    TEST_VERSIONS
        .lock()
        .unwrap()
        .insert(path.to_path_buf(), version.to_owned());
}

#[cfg(test)]
fn test_version(path: &Path) -> Option<String> {
    TEST_VERSIONS.lock().unwrap().get(path).cloned()
}

const LOCK: &str = include_str!("../../../../media-tools.lock.json");
const SUFFIX: &str = if cfg!(windows) { ".exe" } else { "" };
/// A tool answer is reused for this long: a preview, its confirmation and the download that
/// follows all see the same formats.
const ANSWER_TTL_SECS: u64 = 600;
const ANSWERS_MAX: usize = 16;
const MISSING: &str = "pinned media tools missing";

/// The sites whose media a tool answers for, and the tool.
pub(crate) fn tool_for(platform: &str) -> Option<&'static str> {
    (platform == "youtube").then_some("yt_dlp")
}

/// What a run of a tool may cost.
#[derive(Clone, Copy)]
struct Limits {
    run: Duration,
    /// A download takes longer than a query: the video comes down at the speed of the site.
    download: Duration,
    output: u64,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            run: Duration::from_secs(150),
            download: Duration::from_secs(600),
            // A format list with every language's automatic captions is a few megabytes.
            output: 24 * 1024 * 1024,
        }
    }
}

/// Where the tools were found; they are verified when first needed.
#[derive(Clone)]
struct Located {
    dir: PathBuf,
    yt_dlp: PathBuf,
    deno: PathBuf,
    source: &'static str,
}

type Checked = Result<String, &'static str>;

#[derive(Clone)]
pub struct MediaTools {
    located: Arc<RwLock<Option<Located>>>,
    /// The tools found by discovery, kept for rollback after an update.
    fallback: Option<Located>,
    checked: Arc<Mutex<Option<Checked>>>,
    limits: Limits,
    gate: Arc<tokio::sync::Semaphore>,
    answers: Arc<Mutex<HashMap<String, (Instant, Value)>>>,
}

impl MediaTools {
    fn new(located: Option<Located>) -> Self {
        Self {
            fallback: located.clone(),
            located: Arc::new(RwLock::new(located)),
            checked: Arc::new(Mutex::new(None)),
            limits: Limits::default(),
            gate: Arc::new(tokio::sync::Semaphore::new(1)),
            answers: Arc::default(),
        }
    }

    /// Finds the locked tools: the explicit directory, the one packaged next to the program,
    /// and in development builds `target/tools`. The first directory that holds them is the
    /// one used, and it must match the lock when first needed, or the tools stay unavailable
    /// rather than falling back to another. Nothing is executed here.
    pub async fn detect(profile: &crate::config::Profile) -> Self {
        let mut candidates: Vec<(PathBuf, &'static str)> = Vec::new();
        if let Some(dir) = &profile.tools_dir {
            candidates.push((dir.clone(), "explicit"));
        }
        if let Ok(exe) = std::env::current_exe()
            && let Some(parent) = exe.parent()
        {
            candidates.push((parent.join("tools"), "packaged"));
        }
        if cfg!(debug_assertions) {
            candidates.push((
                Path::new(env!("CARGO_MANIFEST_DIR")).join("../../target/tools"),
                "development",
            ));
        }
        let found = candidates.into_iter().find_map(|(dir, source)| {
            let yt_dlp = dir.join(format!("yt-dlp{SUFFIX}"));
            let deno = dir.join(format!("deno{SUFFIX}"));
            (yt_dlp.is_file() && deno.is_file()).then_some(Located {
                dir,
                yt_dlp,
                deno,
                source,
            })
        });
        Self::new(found)
    }

    #[cfg(test)]
    fn missing() -> Self {
        Self::new(None)
    }

    /// Tools that count as verified, for tests that stand in for them.
    #[cfg(test)]
    fn trusted(yt_dlp: PathBuf, deno: PathBuf) -> Self {
        let tools = Self::new(Some(Located {
            dir: yt_dlp.parent().unwrap().to_path_buf(),
            yt_dlp,
            deno,
            source: "test",
        }));
        *tools.checked.lock().unwrap() = Some(Ok("test".into()));
        tools
    }

    fn manifest(located: &Located) -> Result<Value, &'static str> {
        if located.source == "updated" {
            let bytes = std::fs::read(located.dir.join("update.json"))
                .map_err(|_| "media tools update manifest unreadable")?;
            return serde_json::from_slice(&bytes)
                .map_err(|_| "media tools update manifest unreadable");
        }
        serde_json::from_str(LOCK).map_err(|_| "media tools lock unreadable")
    }

    async fn check(located: &Located) -> Checked {
        let lock = Self::manifest(located)?;
        let required = [format!("yt-dlp{SUFFIX}"), format!("deno{SUFFIX}")];
        crate::asset_receipt::verify(&located.dir, &lock, &required)
            .map_err(|_| "media tools failed checksum validation")?;
        let wanted = lock["yt_dlp"]["version"].as_str().unwrap_or_default();
        if run::version(&located.yt_dlp).await.as_deref() != Some(wanted) {
            return Err("media tool version mismatch");
        }
        let runtime = lock["deno"]["version"].as_str().unwrap_or_default();
        if run::version(&located.deno).await.as_deref() != Some(runtime) {
            return Err("media runtime version mismatch");
        }
        Ok(wanted.to_owned())
    }

    /// The verified version, or why the tools cannot be used. Verified once per activation.
    pub async fn verified(&self) -> Result<String, &'static str> {
        if let Some(checked) = self.checked.lock().unwrap().clone() {
            return checked;
        }
        // The lock is dropped before the probe runs: a guard cannot cross an await.
        let located = self.located.read().ok().and_then(|guard| (*guard).clone());
        let result = match located.as_ref() {
            Some(located) => Self::check(located).await,
            None => Err(MISSING),
        };
        *self.checked.lock().unwrap() = Some(result.clone());
        result
    }

    pub async fn ready(&self) -> bool {
        self.verified().await.is_ok()
    }

    /// The paths of verified tools.
    fn programs(&self) -> Option<(PathBuf, PathBuf)> {
        if !self
            .checked
            .lock()
            .unwrap()
            .as_ref()
            .is_some_and(Result::is_ok)
        {
            return None;
        }
        self.located
            .read()
            .unwrap()
            .as_ref()
            .map(|l| (l.yt_dlp.clone(), l.deno.clone()))
    }

    /// Switches to an installed user-chosen update. The caller verifies the files, keeps the
    /// previous version and calls this only when no tool run is in flight (DEC-039).
    pub fn activate(&self, dir: PathBuf, version: &str) -> bool {
        let yt_dlp = dir.join(format!("yt-dlp{SUFFIX}"));
        let deno = dir.join(format!("deno{SUFFIX}"));
        if !yt_dlp.is_file() || !deno.is_file() {
            return false;
        }
        *self.located.write().unwrap() = Some(Located {
            dir,
            yt_dlp,
            deno,
            source: "updated",
        });
        *self.checked.lock().unwrap() = Some(Ok(version.to_owned()));
        true
    }

    /// Drops an update that no longer verifies and returns to the bundled tools.
    pub fn restore_bundled(&self) {
        *self.located.write().unwrap() = self.fallback.clone();
        *self.checked.lock().unwrap() = None;
    }

    /// Whether a tool run is in flight or waiting; an update switches only at idle.
    pub fn idle(&self) -> bool {
        match self.gate.try_acquire() {
            Ok(permit) => {
                drop(permit);
                true
            }
            Err(_) => false,
        }
    }

    /// Holds the same permit a real tool run needs while an update is switched. The caller keeps
    /// the permit until activation and the active pointer are persisted; a run that starts after
    /// the permit is taken waits, and one already running makes the switch wait (F-082).
    pub async fn exclusive(&self) -> Result<tokio::sync::OwnedSemaphorePermit, CoreError> {
        self.gate
            .clone()
            .acquire_owned()
            .await
            .map_err(|_| CoreError::Cancelled)
    }

    pub fn active_version(&self) -> Option<String> {
        match self.checked.lock() {
            Ok(guard) => (*guard).clone().and_then(Result::ok),
            Err(_) => None,
        }
    }

    /// The Deno that came with the package, for an update to reuse.
    pub fn bundled_deno(&self) -> Option<PathBuf> {
        self.fallback.as_ref().map(|l| l.deno.clone())
    }

    pub async fn verify_bundled(&self) -> Result<String, &'static str> {
        match self.fallback.as_ref() {
            Some(located) => Self::check(located).await,
            None => Err(MISSING),
        }
    }

    pub fn source(&self) -> &'static str {
        self.located
            .read()
            .unwrap()
            .as_ref()
            .map_or("unconfigured", |l| l.source)
    }

    /// For the status and doctor reports.
    pub async fn status(&self) -> Value {
        let verified = self.verified().await;
        let located = self.located.read().ok().and_then(|guard| (*guard).clone());
        let version = verified.as_ref().ok().cloned();
        let reason = verified.as_ref().err().copied();
        json!({
            "ready": reason.is_none(),
            "yt_dlp": located.as_ref().map(|l| l.yt_dlp.to_string_lossy().to_string()),
            "deno": located.as_ref().map(|l| l.deno.to_string_lossy().to_string()),
            "version": version,
            "source": located.as_ref().map_or("unconfigured", |l| l.source),
            "reason": reason,
        })
    }

    /// A rule that depends on the tool is at most `partial` while the tool is unavailable,
    /// and says why.
    pub(crate) async fn annotate(&self, site: &str, rules: &mut [RuleDeclaration]) {
        const NOTE: &str =
            "未检测到可用的媒体解析工具：视频只保存元数据，显示为“未下载”，工具就绪后可同步";
        if tool_for(site).is_none() || self.ready().await {
            return;
        }
        for rule in rules
            .iter_mut()
            .filter(|r| r.content_kinds.iter().any(|k| k == "video"))
        {
            if rule.status == RuleStatus::Implemented {
                rule.status = RuleStatus::Partial;
            }
            if !rule.limits.iter().any(|l| l == NOTE) {
                rule.limits.push(NOTE.into());
            }
        }
    }

    fn cached(&self, video: &str) -> Option<Value> {
        let answers = self.answers.lock().unwrap();
        answers
            .get(video)
            .filter(|(at, _)| at.elapsed().as_secs() < ANSWER_TTL_SECS)
            .map(|(_, info)| info.clone())
    }

    fn remember(&self, video: &str, info: &Value) {
        let mut answers = self.answers.lock().unwrap();
        answers.retain(|_, (at, _)| at.elapsed().as_secs() < ANSWER_TTL_SECS);
        if answers.len() >= ANSWERS_MAX
            && let Some(oldest) = answers
                .iter()
                .min_by_key(|(_, (at, _))| *at)
                .map(|(k, _)| k.clone())
        {
            answers.remove(&oldest);
        }
        answers.insert(video.to_owned(), (Instant::now(), info.clone()));
    }

    /// Runs the bundled tool once to learn its version (the same probe as verification).
    pub(crate) async fn version_of(path: &Path) -> Option<String> {
        #[cfg(test)]
        if let Some(version) = test_version(path) {
            return Some(version);
        }
        run::version(path).await
    }

    /// The error of a job that needs the tools while they cannot be used.
    pub(crate) async fn unavailable(&self) -> CoreError {
        CoreError::Unsupported(format!(
            "media tools unavailable: {}",
            self.verified().await.err().unwrap_or(MISSING)
        ))
    }
}
