//! 媒体后端：外部可替换 ffprobe/FFmpeg（docs/design.md §10.3/§13.3）。
//!
//! 类型化参数、受控文件路径；敏感 URL 不进命令行。后端缺失时返回
//! 可诊断错误，不静默伪装成功（OSS-004）。

use crate::error::CoreError;
use serde_json::Value;
use std::path::{Path, PathBuf};
use std::time::Duration;
use tokio::process::Command;
use tokio_util::sync::CancellationToken;
use trace_domain::entities::MediaSpec;

// Excludes playlist/concat demuxers even if a downloaded file masquerades as media.
const LOCAL_FORMATS: &str = "mov,matroska,webm,flv,mpegts,mpegvideo,mp3,aac,ogg,flac,wav,image2,png_pipe,jpeg_pipe,webp_pipe,gif,webvtt";

#[derive(Debug, Clone)]
pub struct MediaBackend {
    pub ffmpeg: Option<PathBuf>,
    pub ffprobe: Option<PathBuf>,
    pub version: Option<String>,
    pub source: String,
    pub reason: Option<String>,
    gate: std::sync::Arc<tokio::sync::Semaphore>,
}

impl MediaBackend {
    pub async fn detect(profile: &crate::config::Profile) -> Self {
        let mut backend = Self {
            ffmpeg: None,
            ffprobe: None,
            version: None,
            source: "unconfigured".into(),
            reason: Some("pinned media backend missing".into()),
            gate: std::sync::Arc::new(tokio::sync::Semaphore::new(1)),
        };
        let mut candidates = Vec::new();
        if let Some(dir) = &profile.media_dir {
            candidates.push((dir.clone(), "explicit"));
        }
        if let Ok(exe) = std::env::current_exe() {
            candidates.push((exe.parent().unwrap().join("media"), "packaged"));
        }
        if cfg!(debug_assertions) {
            candidates.push((
                Path::new(env!("CARGO_MANIFEST_DIR")).join("../../target/media"),
                "development",
            ));
        }
        if profile.allow_media_path
            && let Some(exe) = which_simple("ffmpeg")
        {
            candidates.push((exe.parent().unwrap().to_path_buf(), "explicit-path"));
        }
        let cancel = CancellationToken::new();
        for (dir, source) in candidates {
            let suffix = if cfg!(windows) { ".exe" } else { "" };
            let ffmpeg = dir.join(format!("ffmpeg{suffix}"));
            let ffprobe = dir.join(format!("ffprobe{suffix}"));
            if !ffmpeg.is_file() || !ffprobe.is_file() {
                continue;
            }
            let checked = async {
                if source != "explicit-path" {
                    let lock: Value =
                        serde_json::from_str(include_str!("../../../media-backend.lock.json"))?;
                    let required = [format!("ffmpeg{suffix}"), format!("ffprobe{suffix}")];
                    crate::asset_receipt::verify(&dir, &lock, &required)?;
                }
                let mut version = None;
                for exe in [&ffmpeg, &ffprobe] {
                    let output =
                        bounded_output(Command::new(exe).arg("-version"), &cancel, 15).await?;
                    let output = String::from_utf8_lossy(&output);
                    let v = output
                        .lines()
                        .next()
                        .and_then(|l| l.split_whitespace().nth(2))
                        .unwrap_or_default()
                        .to_string();
                    if v != "n9.0.2-17-g2a571b6068-20260930"
                        || output.contains("--enable-gpl")
                        || output.contains("--enable-nonfree")
                        || version.as_ref().is_some_and(|previous| previous != &v)
                    {
                        return Err(CoreError::InvalidInput(
                            "media backend version/license mismatch".into(),
                        ));
                    }
                    version = Some(v);
                }
                Ok(version)
            }
            .await;
            match checked {
                Ok(version) => {
                    backend.ffmpeg = Some(ffmpeg);
                    backend.ffprobe = Some(ffprobe);
                    backend.version = version;
                    backend.source = source.into();
                    backend.reason = None;
                    return backend;
                }
                Err(_) => {
                    backend.reason =
                        Some("media backend failed checksum/version/license validation".into());
                    return backend;
                }
            }
        }
        backend
    }

    pub async fn probe(
        &self,
        path: &Path,
        cancel: &CancellationToken,
    ) -> Result<MediaSpec, CoreError> {
        let ffprobe = self
            .ffprobe
            .as_ref()
            .ok_or_else(|| CoreError::Unsupported("media backend not configured".into()))?;
        let _permit = self
            .gate
            .acquire()
            .await
            .map_err(|_| CoreError::Cancelled)?;
        let output = bounded_output(
            Command::new(ffprobe)
                .args([
                    "-v",
                    "error",
                    "-print_format",
                    "json",
                    "-show_format",
                    "-show_streams",
                    "-protocol_whitelist",
                    "file,pipe",
                    "-format_whitelist",
                    LOCAL_FORMATS,
                ])
                .arg(path),
            cancel,
            30,
        )
        .await?;
        let json: Value = serde_json::from_slice(&output)?;
        Ok(parse_probe(&json))
    }

    /// 流拷贝封装（DASH 音视频合并；不重编码）。
    #[allow(clippy::too_many_arguments)]
    pub async fn mux_stream_copy(
        &self,
        inputs: &[PathBuf],
        output: PathBuf,
        cancel: &CancellationToken,
    ) -> Result<(), CoreError> {
        let ffmpeg = self.ffmpeg.as_ref().ok_or_else(|| {
            CoreError::Unsupported("media backend (ffmpeg) not configured".into())
        })?;
        let mut cmd = Command::new(ffmpeg);
        cmd.arg("-y")
            .arg("-hide_banner")
            .arg("-loglevel")
            .arg("error");
        for i in inputs {
            cmd.args([
                "-protocol_whitelist",
                "file,pipe",
                "-format_whitelist",
                LOCAL_FORMATS,
            ])
            .arg("-i")
            .arg(i);
        }
        cmd.arg("-map").arg("0:v:0");
        if inputs.len() > 1 {
            cmd.arg("-map").arg("1:a:0");
        }
        cmd.arg("-c")
            .arg("copy")
            .arg("-movflags")
            .arg("+faststart")
            .arg(&output);
        let _permit = self
            .gate
            .acquire()
            .await
            .map_err(|_| CoreError::Cancelled)?;
        bounded_output(&mut cmd, cancel, 300).await?;
        Ok(())
    }
}

async fn bounded_output(
    command: &mut Command,
    cancel: &CancellationToken,
    seconds: u64,
) -> Result<Vec<u8>, CoreError> {
    use tokio::io::AsyncReadExt;
    command
        .kill_on_drop(true)
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::null());
    #[cfg(windows)]
    command.creation_flags(0x0800_0000);
    let mut child = command
        .spawn()
        .map_err(|_| CoreError::Unsupported("media backend could not start".into()))?;
    let mut stdout = child.stdout.take().unwrap().take(1024 * 1024 + 1);
    let result = async {
        let mut bytes = Vec::new();
        stdout.read_to_end(&mut bytes).await?;
        if bytes.len() > 1024 * 1024 {
            return Err(CoreError::InvalidInput("media backend output limit".into()));
        }
        if !child.wait().await?.success() {
            return Err(CoreError::InvalidInput(
                "media backend rejected input".into(),
            ));
        }
        Ok(bytes)
    };
    tokio::select! {
        result = result => result,
        _ = cancel.cancelled() => Err(CoreError::Cancelled),
        _ = tokio::time::sleep(Duration::from_secs(seconds)) => Err(CoreError::Network("media backend timeout".into())),
    }
}

fn parse_probe(json: &Value) -> MediaSpec {
    let mut spec = MediaSpec::default();
    if let Some(fmt) = json.get("format") {
        spec.container = fmt
            .get("format_name")
            .and_then(|v| v.as_str())
            .map(|s| s.to_string());
        spec.duration_ms = fmt
            .get("duration")
            .and_then(|v| v.as_str())
            .and_then(|s| s.parse::<f64>().ok())
            .map(|d| (d * 1000.0) as i64);
        spec.bitrate_bps = fmt
            .get("bit_rate")
            .and_then(|v| v.as_str())
            .and_then(|s| s.parse::<i64>().ok());
    }
    let mut audio_tracks = 0i64;
    let mut subtitle_tracks = 0i64;
    if let Some(streams) = json.get("streams").and_then(|s| s.as_array()) {
        for s in streams {
            match s.get("codec_type").and_then(|v| v.as_str()) {
                Some("video") => {
                    if spec.codec.is_none() {
                        spec.codec = s
                            .get("codec_name")
                            .and_then(|v| v.as_str())
                            .map(String::from);
                        spec.width = s.get("width").and_then(|v| v.as_i64());
                        spec.height = s.get("height").and_then(|v| v.as_i64());
                        let fps = s
                            .get("avg_frame_rate")
                            .and_then(|v| v.as_str())
                            .and_then(parse_ratio);
                        spec.fps = fps;
                    }
                }
                Some("audio") => {
                    audio_tracks += 1;
                    if spec.audio_codec.is_none() {
                        spec.audio_codec = s
                            .get("codec_name")
                            .and_then(|v| v.as_str())
                            .map(String::from);
                        spec.audio_channels = s.get("channels").and_then(|v| v.as_i64());
                    }
                }
                Some("subtitle") => subtitle_tracks += 1,
                _ => {}
            }
        }
    }
    spec.audio_tracks = Some(audio_tracks);
    spec.subtitle_tracks = Some(subtitle_tracks);
    spec
}

fn parse_ratio(s: &str) -> Option<f64> {
    let (a, b) = s.split_once('/')?;
    let a: f64 = a.parse().ok()?;
    let b: f64 = b.parse().ok()?;
    if b == 0.0 { None } else { Some(a / b) }
}

/// 轻量 which：只查 PATH，不执行（诊断用途）。
fn which_simple(name: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    for dir in std::env::split_paths(&path) {
        let candidate = dir.join(if cfg!(windows) {
            format!("{name}.exe")
        } else {
            name.to_string()
        });
        if candidate.is_file() {
            return Some(candidate);
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn local_backend_rejects_disguised_playlists_without_network() {
        let backend = MediaBackend::detect(&crate::config::Profile::default()).await;
        let dir = tempfile::tempdir().unwrap();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let file = dir.path().join("media.mp4");
        let cancel = CancellationToken::new();
        std::fs::write(
            &file,
            format!(
                "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nhttp://{}/secret\n#EXT-X-ENDLIST\n",
                listener.local_addr().unwrap()
            ),
        )
        .unwrap();
        assert!(backend.probe(&file, &cancel).await.is_err());
        assert!(
            backend
                .mux_stream_copy(
                    std::slice::from_ref(&file),
                    dir.path().join("out.mp4"),
                    &cancel
                )
                .await
                .is_err()
        );
        assert!(
            tokio::time::timeout(Duration::from_millis(100), listener.accept())
                .await
                .is_err(),
            "FFmpeg/FFprobe must not bypass the controlled proxy"
        );
        std::fs::write(
            &file,
            "ffconcat version 1.0\nfile 'file:///private-input'\n",
        )
        .unwrap();
        assert!(backend.probe(&file, &cancel).await.is_err());
    }

    #[tokio::test]
    async fn pinned_backend_probes_and_muxes_fixed_avc_aac_samples() {
        let backend = MediaBackend::detect(&crate::config::Profile::default()).await;
        assert!(
            backend.ffmpeg.is_some(),
            "run python scripts/fetch_media.py before media integration tests: {:?}",
            backend.reason
        );
        let dir = tempfile::tempdir().unwrap();
        let cancel = CancellationToken::new();
        let video = dir.path().join("video.mp4");
        let audio = dir.path().join("audio.m4a");
        let mux = dir.path().join("mux.mp4");
        bounded_output(
            Command::new(backend.ffmpeg.as_ref().unwrap())
                .args([
                    "-y",
                    "-v",
                    "error",
                    "-f",
                    "lavfi",
                    "-i",
                    "testsrc=size=160x96:rate=10",
                    "-t",
                    "1",
                    "-c:v",
                    "libopenh264",
                    "-pix_fmt",
                    "yuv420p",
                ])
                .arg(&video),
            &cancel,
            30,
        )
        .await
        .unwrap();
        bounded_output(
            Command::new(backend.ffmpeg.as_ref().unwrap())
                .args([
                    "-y",
                    "-v",
                    "error",
                    "-f",
                    "lavfi",
                    "-i",
                    "sine=frequency=440:sample_rate=44100",
                    "-t",
                    "1",
                    "-c:a",
                    "aac",
                ])
                .arg(&audio),
            &cancel,
            30,
        )
        .await
        .unwrap();
        let spec = backend.probe(&video, &cancel).await.unwrap();
        assert_eq!(spec.width, Some(160));
        assert_eq!(spec.audio_tracks, Some(0));
        assert_eq!(spec.codec.as_deref(), Some("h264"));
        backend
            .mux_stream_copy(&[video, audio], mux.clone(), &cancel)
            .await
            .unwrap();
        let spec = backend.probe(&mux, &cancel).await.unwrap();
        assert_eq!(spec.audio_tracks, Some(1));
        assert_eq!(spec.audio_codec.as_deref(), Some("aac"));
        assert!(spec.duration_ms.unwrap() >= 1000);
        for index in 0..2 {
            let flv = dir.path().join(format!("segment-{index}.flv"));
            bounded_output(
                Command::new(backend.ffmpeg.as_ref().unwrap())
                    .args([
                        "-y",
                        "-v",
                        "error",
                        "-f",
                        "lavfi",
                        "-i",
                        "testsrc=size=160x96:rate=10",
                        "-t",
                        "1",
                        "-c:v",
                        "flv",
                        "-pix_fmt",
                        "yuv420p",
                    ])
                    .arg(&flv),
                &cancel,
                30,
            )
            .await
            .unwrap();
            let spec = backend.probe(&flv, &cancel).await.unwrap();
            assert_eq!(spec.container.as_deref(), Some("flv"));
            assert_eq!(spec.width, Some(160));
        }
    }

    #[test]
    fn probe_parser_extracts_specs() {
        let json: Value = serde_json::json!({
            "format": {"format_name": "mov,mp4", "duration": "12.5", "bit_rate": "1000000"},
            "streams": [
                {"codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "avg_frame_rate": "30000/1001"},
                {"codec_type": "audio", "codec_name": "aac", "channels": 2}
            ]
        });
        let spec = parse_probe(&json);
        assert_eq!(spec.codec.as_deref(), Some("h264"));
        assert_eq!(spec.width, Some(1920));
        assert_eq!(spec.audio_tracks, Some(1));
        assert_eq!(spec.audio_codec.as_deref(), Some("aac"));
        assert!((spec.fps.unwrap() - 29.97).abs() < 0.01);
        assert_eq!(spec.duration_ms, Some(12500));
    }

    #[test]
    fn silent_video_has_zero_audio_tracks() {
        let json: Value = serde_json::json!({
            "format": {"format_name": "mp4"},
            "streams": [{"codec_type": "video", "codec_name": "h264"}]
        });
        let spec = parse_probe(&json);
        assert_eq!(spec.audio_tracks, Some(0), "无声必须有证据");
    }

    #[tokio::test]
    async fn rejects_unverified_backend_before_execution() {
        let dir = tempfile::tempdir().unwrap();
        // Deliberately non-executable placeholders: validation must reject before process launch.
        for name in ["ffmpeg.exe", "ffprobe.exe"] {
            std::fs::write(dir.path().join(name), b"not an executable").unwrap();
        }
        let profile = crate::config::Profile {
            media_dir: Some(dir.path().to_path_buf()),
            ..crate::config::Profile::default()
        };
        let lock: Value =
            serde_json::from_str(include_str!("../../../media-backend.lock.json")).unwrap();
        for receipt in [
            serde_json::json!({"lock":{},"files":{}}),
            serde_json::json!({"lock":lock,"files":{"ffmpeg.exe":"0".repeat(64),"ffprobe.exe":"0".repeat(64)}}),
        ] {
            std::fs::write(
                dir.path().join("verified.json"),
                serde_json::to_vec(&receipt).unwrap(),
            )
            .unwrap();
            let backend = MediaBackend::detect(&profile).await;
            assert!(backend.ffmpeg.is_none());
            assert!(backend.ffprobe.is_none());
            assert_eq!(
                backend.reason.as_deref(),
                Some("media backend failed checksum/version/license validation")
            );
        }
    }

    #[tokio::test]
    async fn backend_detection_is_explicit() {
        let backend = MediaBackend::detect(&crate::config::Profile::default()).await;
        // 本机可能有或没有 ffprobe；关键是不伪装：缺失时 probe 返回 Unsupported。
        match &backend.ffprobe {
            Some(_) => {}
            None => {
                let dir = tempfile::tempdir().unwrap();
                let err = backend
                    .probe(&dir.path().join("x.mp4"), &CancellationToken::new())
                    .await
                    .unwrap_err();
                assert!(matches!(err, CoreError::Unsupported(_)));
            }
        }
    }
}
