//! Running the tools: fixed arguments, an empty environment, no credentials, bounded time and
//! output. What a tool prints on stderr is only used to classify a failure, never kept.
use super::MediaTools;
use crate::error::CoreError;
use crate::network::Proxy;
use serde_json::Value;
use std::path::Path;
use std::process::Stdio;
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt};
use tokio::process::Command;
use tokio_util::sync::CancellationToken;
use trace_domain::errors::FailureClass;

const DIAGNOSTIC_MAX: u64 = 64 * 1024;
const VERSION_SECONDS: u64 = 20;
const CREATE_NO_WINDOW: u32 = 0x0800_0000;

/// What the tools may know about the machine: where to keep their own files and how to
/// reach the network. Nothing from the user's profile, configuration or environment.
pub(crate) struct Sandbox<'a> {
    pub work: &'a Path,
    pub proxy: Option<&'a Proxy>,
}

fn base_command(program: &Path) -> Command {
    let mut command = Command::new(program);
    command
        .env_clear()
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    #[cfg(windows)]
    command.creation_flags(CREATE_NO_WINDOW);
    if let Some(root) = std::env::var_os("SystemRoot") {
        command.env("SystemRoot", &root);
        command.env("PATH", Path::new(&root).join("System32"));
    }
    command.env("NO_COLOR", "1");
    command
}

/// The version a tool reports for itself (`yt-dlp --version`, `deno --version`'s first line).
pub(super) async fn version(program: &Path) -> Option<String> {
    // A launcher that unpacks itself needs somewhere to do it, and the environment is empty.
    let scratch = tempfile::tempdir().ok()?;
    let mut command = base_command(program);
    command
        .arg("--version")
        .env("TEMP", scratch.path())
        .env("TMP", scratch.path())
        .env("DENO_NO_UPDATE_CHECK", "1");
    let mut child = command.spawn().ok()?;
    let mut out = child.stdout.take()?.take(DIAGNOSTIC_MAX);
    let read = async {
        let mut bytes = Vec::new();
        out.read_to_end(&mut bytes).await.ok()?;
        child.wait().await.ok()?.success().then_some(bytes)
    };
    let bytes = tokio::time::timeout(Duration::from_secs(VERSION_SECONDS), read)
        .await
        .ok()??;
    let text = String::from_utf8_lossy(&bytes);
    let first = text.lines().next()?.trim();
    // `deno 2.9.7 (stable, release, x86_64-pc-windows-msvc)` or a bare `2026.08.19`.
    Some(
        first
            .strip_prefix("deno ")
            .unwrap_or(first)
            .split_whitespace()
            .next()?
            .to_owned(),
    )
}

fn proxy_variable(proxy: &Proxy) -> Result<String, CoreError> {
    let mut url =
        url::Url::parse(&proxy.url).map_err(|_| CoreError::InvalidInput("proxy address".into()))?;
    if let Some((user, password)) = &proxy.auth {
        let _ = url.set_username(user);
        let _ = url.set_password(Some(password));
    }
    Ok(url.to_string())
}

/// The arguments every run has. Everything that could vary with the user or the machine is
/// switched off: configuration files, plugins, updates, cookies, remote components, playlists
/// and the system proxy.
fn fixed(deno: &Path, cache: &Path, direct: bool) -> Vec<std::ffi::OsString> {
    let mut args: Vec<std::ffi::OsString> = [
        "--ignore-config",
        "--no-plugin-dirs",
        "--no-update",
        "--no-remote-components",
        "--no-js-runtimes",
        "--no-cookies",
        "--no-cookies-from-browser",
        "--no-playlist",
        "--no-warnings",
        "--no-progress",
        "--no-mark-watched",
        "--encoding",
        "utf-8",
        "--socket-timeout",
        "30",
        "--retries",
        "2",
        "--extractor-retries",
        "1",
    ]
    .map(Into::into)
    .to_vec();
    let mut runtime = std::ffi::OsString::from("deno:");
    runtime.push(deno);
    args.extend(["--js-runtimes".into(), runtime]);
    args.extend(["--cache-dir".into(), cache.into()]);
    if direct {
        // An empty proxy is a direct connection; without it the system setting would apply.
        args.extend(["--proxy".into(), "".into()]);
    }
    args
}

/// The arguments of the format query.
fn arguments(deno: &Path, cache: &Path, url: &str, direct: bool) -> Vec<std::ffi::OsString> {
    let mut args = fixed(deno, cache, direct);
    args.extend(["-J".into(), "--".into(), url.into()]);
    args
}

/// The name the downloaded file is given inside the run's own directory.
const DOWNLOAD_NAME: &str = "video";

/// The arguments of a download: the best single file that carries picture and sound and is not
/// the site's watermarked copy, into the run's directory, at most `cap` bytes.
fn download_arguments(
    (deno, cache): (&Path, &Path),
    url: &str,
    direct: bool,
    cap: u64,
) -> Vec<std::ffi::OsString> {
    let mut args = fixed(deno, cache, direct);
    args.extend(
        [
            "-f",
            "b[format_id!*=download]/b",
            "--no-part",
            "--no-mtime",
            "--no-write-info-json",
            "--no-write-thumbnail",
            "--no-write-subs",
            "--no-embed-subs",
            "--no-embed-metadata",
            "--no-embed-chapters",
            "--concurrent-fragments",
            "1",
            "--max-filesize",
        ]
        .map(Into::into),
    );
    args.push(cap.to_string().into());
    args.extend(["-o".into(), format!("{DOWNLOAD_NAME}.%(ext)s").into()]);
    args.extend(["--".into(), url.into()]);
    args
}

/// A failure of the tool, told apart by what it said. The text is matched, never shown.
pub(super) fn classify(diagnostic: &str) -> CoreError {
    let text = diagnostic.to_ascii_lowercase();
    let has = |needles: &[&str]| needles.iter().any(|n| text.contains(n));
    if has(&[
        "not a bot",
        "captcha",
        "http error 429",
        "too many requests",
    ]) {
        CoreError::RateLimited(": the site asked for verification".into())
    } else if has(&[
        "private video",
        "sign in",
        "log in",
        "members-only",
        "age-restricted",
    ]) {
        CoreError::Adapter(FailureClass::PermissionDenied)
    } else if has(&[
        "video unavailable",
        "has been removed",
        "is not available",
        "no longer available",
        "does not exist",
        "copyright",
        "terminated",
    ]) {
        CoreError::SourceUnavailable("the video is not available".into())
    } else if has(&[
        "unable to download",
        "timed out",
        "connection",
        "getaddrinfo",
        "temporary failure",
        "http error 5",
        "proxy",
        "ssl",
    ]) {
        CoreError::Network("the media tool could not reach the site".into())
    } else {
        CoreError::Adapter(FailureClass::SchemaChanged)
    }
}

async fn read_all(
    mut stream: impl AsyncRead + Unpin,
    limit: u64,
) -> std::io::Result<(Vec<u8>, bool)> {
    let mut bytes = Vec::new();
    (&mut stream)
        .take(limit + 1)
        .read_to_end(&mut bytes)
        .await?;
    let over = bytes.len() as u64 > limit;
    bytes.truncate(limit as usize);
    Ok((bytes, over))
}

#[cfg(windows)]
fn kill_tree(pid: Option<u32>) {
    use std::os::windows::process::CommandExt;
    // The tool is a launcher with a child; ending only the launcher would leave the child.
    if let Some(pid) = pid {
        let _ = std::process::Command::new("taskkill")
            .args(["/PID", &pid.to_string(), "/T", "/F"])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .creation_flags(CREATE_NO_WINDOW)
            .spawn();
    }
}

#[cfg(not(windows))]
fn kill_tree(_pid: Option<u32>) {}

/// What a finished run left: its output, and whether the output ran past its limit.
struct Finished {
    stdout: Vec<u8>,
    stderr: Vec<u8>,
    over: bool,
    success: bool,
}

/// A file a tool downloaded, inside the run's own directory, which is removed with it.
pub(crate) struct Fetched {
    _dir: tempfile::TempDir,
    pub path: std::path::PathBuf,
    pub size: u64,
}

fn directory_size(dir: &Path) -> u64 {
    std::fs::read_dir(dir)
        .into_iter()
        .flatten()
        .flatten()
        .filter_map(|entry| entry.metadata().ok())
        .map(|meta| if meta.is_file() { meta.len() } else { 0 })
        .sum()
}

impl MediaTools {
    /// A run of `program` in `temp`, with the environment the tools are allowed and nothing else.
    fn command(program: &Path, sandbox: &Sandbox<'_>, temp: &Path) -> Result<Command, CoreError> {
        let mut command = base_command(program);
        command
            .current_dir(temp)
            .env("TEMP", temp)
            .env("TMP", temp)
            .env("APPDATA", temp)
            .env("LOCALAPPDATA", temp)
            .env("USERPROFILE", temp)
            .env("HOME", temp)
            .env("DENO_DIR", sandbox.work.join("deno"))
            .env("DENO_NO_UPDATE_CHECK", "1")
            .env("PYTHONUTF8", "1")
            .env("PYTHONIOENCODING", "utf-8");
        if let Some(proxy) = sandbox.proxy {
            let address = proxy_variable(proxy)?;
            for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"] {
                command.env(name, &address);
            }
        }
        Ok(command)
    }

    /// Runs the command to its end within the time and output limits. With `cap`, a run whose
    /// directory grows past it is ended.
    async fn execute(
        &self,
        mut command: Command,
        (temp, cap): (&Path, Option<u64>),
        limit: Duration,
        cancel: &CancellationToken,
    ) -> Result<Finished, CoreError> {
        let mut child = command
            .spawn()
            .map_err(|_| CoreError::Unsupported("the media tool could not start".into()))?;
        let pid = child.id();
        let stdout = child.stdout.take().expect("piped");
        let stderr = child.stderr.take().expect("piped");
        let work = async {
            let (out, err) = tokio::join!(
                read_all(stdout, self.limits.output),
                read_all(stderr, DIAGNOSTIC_MAX)
            );
            let status = child.wait().await?;
            Ok::<_, std::io::Error>((out?, err?, status))
        };
        let outgrown = async {
            let Some(cap) = cap else {
                return std::future::pending::<()>().await;
            };
            loop {
                tokio::time::sleep(Duration::from_millis(250)).await;
                if directory_size(temp) > cap {
                    return;
                }
            }
        };
        let result = tokio::select! {
            r = work => Some(r),
            _ = cancel.cancelled() => None,
            _ = outgrown => {
                kill_tree(pid);
                return Err(CoreError::InvalidInput("media tool output limit".into()));
            }
            _ = tokio::time::sleep(limit) => {
                kill_tree(pid);
                return Err(CoreError::Network("the media tool timed out".into()));
            }
        };
        let Some(result) = result else {
            kill_tree(pid);
            return Err(CoreError::Cancelled);
        };
        let ((stdout, over), (stderr, _), status) =
            result.map_err(|_| CoreError::Unsupported("the media tool could not run".into()))?;
        Ok(Finished {
            stdout,
            stderr,
            over,
            success: status.success(),
        })
    }

    /// The tool's description of one YouTube video: `yt-dlp -J` for its watch page.
    pub(crate) async fn video_info(
        &self,
        sandbox: &Sandbox<'_>,
        video_id: &str,
        cancel: &CancellationToken,
    ) -> Result<Value, CoreError> {
        let key = video_id;
        let url = format!("https://www.youtube.com/watch?v={video_id}");
        if !self.ready().await {
            return Err(self.unavailable().await);
        }
        if let Some(info) = self.cached(key) {
            return Ok(info);
        }
        let _turn = self
            .gate
            .acquire()
            .await
            .map_err(|_| CoreError::Cancelled)?;
        // Another job may have asked while this one waited for its turn.
        if let Some(info) = self.cached(key) {
            return Ok(info);
        }
        // A switch may have completed while this run waited for the shared permit.
        let (yt, deno) = self.programs().ok_or(CoreError::Cancelled)?;
        std::fs::create_dir_all(sandbox.work)?;
        let temp = tempfile::Builder::new()
            .prefix("run-")
            .tempdir_in(sandbox.work)?;
        let cache = sandbox.work.join("cache");
        std::fs::create_dir_all(&cache)?;
        let mut command = Self::command(&yt, sandbox, temp.path())?;
        command.args(arguments(&deno, &cache, &url, sandbox.proxy.is_none()));
        let finished = self
            .execute(command, (temp.path(), None), self.limits.run, cancel)
            .await?;
        if finished.over {
            return Err(CoreError::InvalidInput("media tool output limit".into()));
        }
        if !finished.success {
            return Err(classify(&String::from_utf8_lossy(&finished.stderr)));
        }
        let info: Value = serde_json::from_slice(&finished.stdout)
            .map_err(|_| CoreError::Adapter(FailureClass::SchemaChanged))?;
        self.remember(key, &info);
        Ok(info)
    }

    /// The video at `url`, downloaded by the tool into a directory of its own: the one file it
    /// leaves, at most `cap` bytes. For a site that refuses Core's own request for the address
    /// its page gave; the tool keeps the site's session itself and is given none of ours.
    pub(crate) async fn fetch_video(
        &self,
        sandbox: &Sandbox<'_>,
        url: &str,
        cap: u64,
        cancel: &CancellationToken,
    ) -> Result<Fetched, CoreError> {
        if !self.ready().await {
            return Err(self.unavailable().await);
        }
        let _turn = self
            .gate
            .acquire()
            .await
            .map_err(|_| CoreError::Cancelled)?;
        // Downloads queued behind a switch must use the newly selected executable too.
        let (yt, deno) = self.programs().ok_or(CoreError::Cancelled)?;
        std::fs::create_dir_all(sandbox.work)?;
        let temp = tempfile::Builder::new()
            .prefix("get-")
            .tempdir_in(sandbox.work)?;
        let cache = sandbox.work.join("cache");
        std::fs::create_dir_all(&cache)?;
        let mut command = Self::command(&yt, sandbox, temp.path())?;
        command.args(download_arguments(
            (&deno, &cache),
            url,
            sandbox.proxy.is_none(),
            cap,
        ));
        let finished = self
            .execute(
                command,
                (temp.path(), Some(cap)),
                self.limits.download,
                cancel,
            )
            .await?;
        if !finished.success {
            return Err(classify(&String::from_utf8_lossy(&finished.stderr)));
        }
        let prefix = format!("{DOWNLOAD_NAME}.");
        let mut files = std::fs::read_dir(temp.path())?
            .flatten()
            .filter(|entry| entry.metadata().is_ok_and(|m| m.is_file()))
            .filter(|entry| entry.file_name().to_string_lossy().starts_with(&prefix));
        let (Some(file), None) = (files.next(), files.next()) else {
            return Err(CoreError::Adapter(FailureClass::SchemaChanged));
        };
        let size = file.metadata()?.len();
        if size == 0 || size > cap {
            return Err(CoreError::InvalidInput("media tool output limit".into()));
        }
        let path = file.path();
        drop(files);
        Ok(Fetched {
            _dir: temp,
            path,
            size,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn failures_are_told_apart_by_what_the_tool_said() {
        let cases = [
            ("Sign in to confirm you’re not a bot", "rate"),
            (
                "ERROR: Private video. Sign in if you've been granted access",
                "permission",
            ),
            ("ERROR: Sign in to confirm your age", "permission"),
            (
                "ERROR: Video unavailable. This video has been removed",
                "gone",
            ),
            (
                "ERROR: Unable to download webpage: <urlopen error timed out>",
                "network",
            ),
            ("ERROR: something we have never seen", "schema"),
        ];
        for (text, expected) in cases {
            let error = classify(text);
            let got = match error {
                CoreError::RateLimited(_) => "rate",
                CoreError::Adapter(FailureClass::PermissionDenied) => "permission",
                CoreError::SourceUnavailable(_) => "gone",
                CoreError::Network(_) => "network",
                CoreError::Adapter(FailureClass::SchemaChanged) => "schema",
                _ => "other",
            };
            assert_eq!(got, expected, "{text}");
        }
    }

    #[test]
    fn the_query_is_fixed_and_the_system_proxy_is_never_consulted() {
        let args = arguments(
            Path::new("C:/tools/deno.exe"),
            Path::new("C:/work/cache"),
            "https://www.youtube.com/watch?v=abcdefghijk",
            true,
        );
        let text: Vec<String> = args
            .iter()
            .map(|a| a.to_string_lossy().into_owned())
            .collect();
        for needed in [
            "--ignore-config",
            "--no-plugin-dirs",
            "--no-update",
            "--no-remote-components",
            "--no-cookies",
            "--no-cookies-from-browser",
            "--no-playlist",
        ] {
            assert!(text.iter().any(|a| a == needed), "{needed}");
        }
        let proxy = text.iter().position(|a| a == "--proxy").unwrap();
        assert_eq!(text[proxy + 1], "", "direct means an explicit empty proxy");
        assert_eq!(
            &text[text.len() - 3..],
            ["-J", "--", "https://www.youtube.com/watch?v=abcdefghijk"]
        );
        let routed = arguments(Path::new("d"), Path::new("c"), "https://u", false);
        assert!(
            routed.iter().all(|a| a != "--proxy"),
            "a routed run uses the environment"
        );
        assert!(text.iter().any(|a| a == "deno:C:/tools/deno.exe"));
    }

    #[test]
    fn a_proxy_login_reaches_the_tool_only_through_the_environment() {
        let proxy = Proxy {
            url: "http://127.0.0.1:7890".into(),
            auth: Some(("us er".into(), "p@ss:word".into())),
        };
        let address = proxy_variable(&proxy).unwrap();
        assert_eq!(address, "http://us%20er:p%40ss%3Aword@127.0.0.1:7890/");
        let plain = Proxy {
            url: "socks5://127.0.0.1:1080".into(),
            auth: None,
        };
        assert_eq!(proxy_variable(&plain).unwrap(), "socks5://127.0.0.1:1080");
    }
}
