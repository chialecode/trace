//! e2e 集成测试辅助：本地合成 HTTP 源、测试 Profile、轮询等待。

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use trace_core::config::Profile;

/// 合成异构源：极简 HTTP/1.1 路由（仅本机回环，测试专用）。
pub struct FakeSource {
    pub base_url: String,
    pub credential_requests: std::sync::Arc<std::sync::atomic::AtomicUsize>,
}

impl FakeSource {
    /// 启动本地服务器；返回 base_url（http://127.0.0.1:port）。
    pub fn start() -> Self {
        Self::start_with_media_delay(0)
    }

    pub fn start_with_media_delay(delay_ms: u64) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind 127.0.0.1");
        let port = listener.local_addr().unwrap().port();
        let base_url = format!("http://127.0.0.1:{port}");
        let base = base_url.clone();
        let credential_requests = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let counter = credential_requests.clone();
        std::thread::Builder::new()
            .name("fake-source".into())
            .spawn(move || {
                for stream in listener.incoming() {
                    let Ok(mut stream) = stream else { continue };
                    let base = base.clone();
                    let counter = counter.clone();
                    std::thread::spawn(move || {
                        let _ = Self::handle_conn(&mut stream, &base, &counter, delay_ms);
                    });
                }
            })
            .expect("spawn fake source thread");
        Self {
            base_url,
            credential_requests,
        }
    }

    fn handle_conn(
        stream: &mut TcpStream,
        base: &str,
        counter: &std::sync::atomic::AtomicUsize,
        delay_ms: u64,
    ) -> std::io::Result<()> {
        let mut buf = Vec::new();
        let mut chunk = [0u8; 1024];
        loop {
            let n = stream.read(&mut chunk)?;
            if n == 0 {
                return Ok(());
            }
            buf.extend_from_slice(&chunk[..n]);
            if buf.windows(4).any(|w| w == b"\r\n\r\n") {
                break;
            }
            if buf.len() > 64 * 1024 {
                return Ok(());
            }
        }
        if buf
            .windows(b"SENTINEL-COOKIE-7f3a9-do-not-leak".len())
            .any(|w| w == b"SENTINEL-COOKIE-7f3a9-do-not-leak")
        {
            counter.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        }
        let head = String::from_utf8_lossy(&buf);
        let target = head.split_whitespace().nth(1).unwrap_or("/");
        let (path, query) = target.split_once('?').unwrap_or((target, ""));
        static AUDIO_FAILURE: std::sync::atomic::AtomicBool =
            std::sync::atomic::AtomicBool::new(false);
        // The single-video entry has a committed video before its audio fails.
        // A segmented entry correctly keeps all new segment references pending until group validation.
        if path == "/fake/media/v-1-p1/audio.bin"
            && !AUDIO_FAILURE.swap(true, std::sync::atomic::Ordering::SeqCst)
        {
            // Error body includes a credential-shaped sentinel; it must never reach logs/database.
            stream.write_all(b"HTTP/1.1 503 Unavailable\r\nContent-Length: 39\r\nConnection: close\r\n\r\nCookie: SENTINEL-COOKIE-7f3a9-do-not-leak")?;
            return Ok(());
        }
        let (status, ctype, mut body) = Self::route(path, query, base);
        // Keep the same two-day ordering while keeping the synthetic records in
        // full_loop's 365-day range. The old fixed 2023 dates stopped matching
        // when M1 began enforcing the configured subscription range.
        if ctype == "application/json" {
            let mut value: serde_json::Value = serde_json::from_slice(&body).unwrap();
            rebase_fixture_epochs(&mut value);
            body = serde_json::to_vec(&value).unwrap();
        }
        let resp = format!(
            "HTTP/1.1 {status}\r\nContent-Type: {ctype}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        );
        stream.write_all(resp.as_bytes())?;
        for chunk in body.chunks(1024) {
            stream.write_all(chunk)?;
            if path.starts_with("/fake/media/") && delay_ms > 0 {
                std::thread::sleep(Duration::from_millis(delay_ms));
            }
        }
        stream.flush()
    }

    fn parse_query(query: &str) -> Vec<(String, String)> {
        query
            .split('&')
            .filter(|s| !s.is_empty())
            .map(|kv| match kv.split_once('=') {
                Some((k, v)) => (k.to_string(), v.to_string()),
                None => (kv.to_string(), String::new()),
            })
            .collect()
    }

    fn route(path: &str, query: &str, base: &str) -> (&'static str, &'static str, Vec<u8>) {
        let q = Self::parse_query(query);
        let get = |k: &str| {
            q.iter()
                .find(|(kk, _)| kk == k)
                .map(|(_, v)| v.clone())
                .unwrap_or_default()
        };
        match path {
            "/fake/api/list" => {
                if get("cursor") == "c2" {
                    (
                        "200 OK",
                        "application/json",
                        r#"{"next": null, "entries": [
                            {"id": "v-2", "heading": "Second Clip", "epoch": 1702032000, "blurb": "second", "stats": {"views": 7}}
                        ]}"#
                        .as_bytes().to_vec(),
                    )
                } else {
                    (
                        "200 OK",
                        "application/json",
                        r#"{"next": "c2", "entries": [
                            {"id": "v-1", "heading": "合成视频一：中文搜索测试", "epoch": 1702204800, "blurb": "第一个合成条目", "stats": {"views": 5}}
                        ]}"#
                        .as_bytes().to_vec(),
                    )
                }
            }
            "/fake/api/detail" => {
                let id = get("id");
                match id.as_str() {
                    "v-1" => (
                        "200 OK",
                        "application/json",
                        r#"{"id": "v-1", "rev": 3, "heading": "合成视频一：中文搜索测试", "body": "完整正文内容：用于中文搜索与导出验证。", "epoch": 1702204800,
                             "poster": null,
                             "parts": [{"part_id": "p1", "position": 1, "label": "正片", "duration": 30}],
                             "stats": {"views": 5}}"#
                            .as_bytes().to_vec(),
                    ),
                    "v-2" => (
                        "200 OK",
                        "application/json",
                        r#"{"id": "v-2", "rev": 1, "heading": "Second Clip", "body": "second body", "epoch": 1702032000,
                             "poster": null,
                             "parts": [{"part_id": "p1", "position": 1, "label": "main", "duration": 20}],
                             "stats": {"views": 7}}"#
                            .as_bytes().to_vec(),
                    ),
                    _ => (
                        "404 Not Found",
                        "application/json",
                        r#"{"error": "no detail"}"#.as_bytes().to_vec(),
                    ),
                }
            }
            "/fake/api/stream" => {
                let entry = get("id");
                let part = get("part");
                if entry == "v-2" && part == "p1" {
                    let body = serde_json::json!({"segments": [
                        {
                            "u": format!("{base}/fake/media/v-2-segment-0.bin"),
                            "size": media_bytes("v-2-segment-0.bin").len(),
                        },
                        {
                            "u": format!("{base}/fake/media/v-2-segment-1.bin"),
                            "size": media_bytes("v-2-segment-1.bin").len(),
                        },
                    ]});
                    return (
                        "200 OK",
                        "application/json",
                        serde_json::to_vec(&body).unwrap(),
                    );
                }
                let url = format!("{base}/fake/media/{entry}-{part}.bin");
                let bytes = media_bytes(&format!("{entry}-{part}.bin"));
                (
                    "200 OK",
                    "application/json",
                    format!(
                        r#"{{"segments": [{{"u": "{url}", "size": {}}}]}}"#,
                        bytes.len()
                    )
                    .into_bytes(),
                )
            }
            p if p.starts_with("/fake/media/") => {
                let name = p.trim_start_matches("/fake/media/");
                ("200 OK", "application/octet-stream", media_bytes(name))
            }
            _ => ("404 Not Found", "text/plain", b"not found".to_vec()),
        }
    }
}

fn rebase_fixture_epochs(value: &mut serde_json::Value) {
    let today = time::OffsetDateTime::now_utc().unix_timestamp() / 86400 * 86400;
    match value {
        serde_json::Value::Object(o) => {
            if let Some(epoch) = o.get_mut("epoch") {
                match epoch.as_i64() {
                    Some(1702204800) => *epoch = (today - 86400).into(),
                    Some(1702032000) => *epoch = (today - 3 * 86400).into(),
                    _ => {}
                }
            }
            for child in o.values_mut() {
                rebase_fixture_epochs(child);
            }
        }
        serde_json::Value::Array(a) => {
            for child in a {
                rebase_fixture_epochs(child)
            }
        }
        _ => {}
    }
}

/// 确定性媒体字节（可复算 sha256）。
pub fn media_bytes(name: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity(96 * 1024);
    out.extend_from_slice(b"TRACE-FAKE-MEDIA:");
    out.extend_from_slice(name.as_bytes());
    out.extend_from_slice(b":");
    let mut i = 0u32;
    while out.len() < 96 * 1024 {
        i = i.wrapping_mul(1664525).wrapping_add(1013904223);
        out.extend_from_slice(&i.to_le_bytes());
    }
    out
}

/// 测试 Profile：全路径隔离在 root 下，显式放行 127.0.0.1（仅本机回环）。
pub fn test_profile(root: &Path, adapter_dir: &Path) -> Profile {
    Profile {
        acquisition_limits: None,
        media_dir: None,
        tools_dir: None,
        allow_media_path: false,
        live_concurrency: 4,
        metadata_min_interval_ms: 2000,
        metadata_jitter_ms: 250,
        scan_page_budget: 10,
        quality_prefer_fps: false,
        recycle_wait_seconds: 86400,
        manual_delete_wait_seconds: 7 * 86400,
        recording_fragment_seconds: 600,
        recording_fragment_bytes: 512 * 1024 * 1024,
        recording_disk_reserve_bytes: trace_core::config::DISK_SAFETY_MARGIN_BYTES as u64,
        archive_id: "e2e".into(),
        data_root: root.join("data"),
        archive_root: root.join("archive"),
        vault_dir: Some(root.join("vault")),
        adapter_dir: Some(adapter_dir.to_path_buf()),
        extra_media_hosts: Vec::new(),
        test_allowed_hosts: vec!["127.0.0.1".into()],
    }
}

/// 适配器目录：target/debug（cargo test 工作区构建产物）。
pub fn adapter_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../target/debug")
}

pub fn daemon_exe() -> PathBuf {
    adapter_dir().join(if cfg!(windows) {
        "trace-daemon.exe"
    } else {
        "trace-daemon"
    })
}

/// 带总超时的轮询等待；f 返回 Some(err) 即失败，None 继续轮询。
pub fn wait_until<F: FnMut() -> Option<String>>(timeout: Duration, mut f: F) -> Result<(), String> {
    let start = Instant::now();
    loop {
        if let Some(err) = f() {
            return Err(err);
        }
        if start.elapsed() > timeout {
            return Err(format!("timeout after {timeout:?}"));
        }
        std::thread::sleep(Duration::from_millis(300));
    }
}

/// 递归收集目录下全部文件路径（哨兵扫描用）。
pub fn collect_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        if p.is_dir() {
            collect_files(&p, out);
        } else {
            out.push(p);
        }
    }
}
