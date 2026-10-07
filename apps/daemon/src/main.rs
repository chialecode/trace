//! TRACE daemon：独立后台进程（INV-06）。
//!
//! 启动流程（m1a-runtime-contracts §1）：
//! 1. 先连接既有端点（握手成功即复用，"already running"）。
//! 2. 否则获取 OS 排他锁 → 打开数据库 → 迁移 → 恢复扫描 → 发布端点。
//! 3. 竞争失败者退出；握手成功才代表 ready，不凭 PID 文件判断。

use serde_json::json;
use std::io::Write as _;
use std::process::ExitCode;
use trace_core::config::{Profile, load_profile};
use trace_core::core::Core;
use trace_core::ipc::client::Client;
use trace_core::ipc::server::IpcServer;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().collect();
    let sub = args.get(1).map(|s| s.as_str()).unwrap_or("start");
    let profile_path = args
        .iter()
        .position(|a| a == "--profile")
        .and_then(|i| args.get(i + 1))
        .map(std::path::PathBuf::from)
        .or_else(|| {
            std::env::var("TRACE_PROFILE")
                .map(std::path::PathBuf::from)
                .ok()
        });
    let profile = match profile_path {
        Some(p) => match load_profile(&p) {
            Ok(pr) => pr,
            Err(e) => {
                eprintln!("trace-daemon: profile load failed ({p:?}): {e}");
                return ExitCode::from(2);
            }
        },
        None => default_profile(),
    };
    match sub {
        "start" => run_start(profile),
        "status" => run_status(profile),
        "shutdown" => run_shutdown(profile),
        "doctor" => run_doctor(&profile),
        "version" => {
            println!(
                "trace-daemon 0.1.0 (control protocol {})",
                trace_domain::CONTROL_PROTOCOL_VERSION
            );
            ExitCode::SUCCESS
        }
        other => {
            eprintln!("unknown subcommand: {other} (start|status|shutdown|doctor|version)");
            ExitCode::from(2)
        }
    }
}

/// 默认开发 Profile：仓库 profiles/dev/（.gitignore 覆盖）。
fn default_profile() -> Profile {
    Profile {
        archive_id: "dev".into(),
        data_root: std::path::PathBuf::from("profiles/dev/data"),
        archive_root: std::path::PathBuf::from("profiles/dev/archive"),
        ..Profile::default()
    }
}

fn run_start(profile: Profile) -> ExitCode {
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .expect("tokio runtime");
    rt.block_on(async move {
        // 1) 既有实例？
        if let Ok(client) = Client::connect(
            &profile.pipe_name(),
            "daemon-probe",
            Some(&profile.archive_id),
        )
        .await
        {
            let instance_id = client.hello.instance_id.clone();
            let _ = client.shutdown().await;
            println!(
                "{}",
                json!({"status": "already-running", "instance_id": instance_id})
            );
            return ExitCode::SUCCESS;
        }
        // 2) OS 排他锁（锁服务进程持有；异常退出自动释放）。
        if let Some(parent) = profile.lock_path().parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let lock_file = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(false)
            .open(profile.lock_path());
        let lock_file = match lock_file {
            Ok(f) => f,
            Err(e) => {
                eprintln!("trace-daemon: cannot open lock file: {e}");
                return ExitCode::from(2);
            }
        };
        if lock_file.try_lock().is_err() {
            eprintln!("trace-daemon: another instance holds the lock but is not reachable");
            return ExitCode::from(3);
        }
        // 3) 打开核心（迁移+恢复）。
        let core = match Core::open(profile.clone()).await {
            Ok(c) => c,
            Err(e) => {
                eprintln!("trace-daemon: core open failed: {e}");
                return ExitCode::from(2);
            }
        };
        let recovery = core.recover().await.unwrap_or_else(|e| {
            tracing::warn!("recovery partial failure: {e}");
            json!({"error": e.to_string()})
        });
        println!(
            "{}",
            json!({
                "status": "ready",
                "instance_id": core.instance_id.as_str(),
                "pipe": profile.pipe_name(),
                "recovery": recovery,
            })
        );
        core.start_scheduler();
        let server = IpcServer { core: core.clone() };
        // 优雅停机：SIGINT/SIGTERM → shutdown（停止调度→关适配器）。
        let core2 = core.clone();
        tokio::spawn(async move {
            let _ = tokio::signal::ctrl_c().await;
            println!("trace-daemon: shutdown signal");
            core2.shutdown().await;
            std::process::exit(0);
        });
        match server.serve().await {
            Ok(()) => ExitCode::SUCCESS,
            Err(e) => {
                eprintln!("trace-daemon: serve failed: {e}");
                ExitCode::from(2)
            }
        }
    })
}

fn run_status(profile: Profile) -> ExitCode {
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("runtime");
    rt.block_on(async move {
        match Client::connect(&profile.pipe_name(), "cli", Some(&profile.archive_id)).await {
            Ok(client) => match client
                .request(
                    trace_contracts::control::commands::DAEMON_STATUS,
                    json!({}),
                    None,
                    None,
                )
                .await
            {
                Ok(status) => {
                    println!(
                        "{}",
                        serde_json::to_string_pretty(&status).unwrap_or_default()
                    );
                    let _ = client.shutdown().await;
                    ExitCode::SUCCESS
                }
                Err(e) => {
                    eprintln!("trace-daemon: status query failed: {e}");
                    ExitCode::from(2)
                }
            },
            Err(_) => {
                println!("{}", json!({"status": "not-running"}));
                ExitCode::from(3)
            }
        }
    })
}

fn run_shutdown(profile: Profile) -> ExitCode {
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("runtime");
    rt.block_on(async move {
        match Client::connect(&profile.pipe_name(), "cli", Some(&profile.archive_id)).await {
            Ok(client) => {
                let _ = client
                    .request(
                        trace_contracts::control::commands::DAEMON_SHUTDOWN,
                        json!({}),
                        None,
                        None,
                    )
                    .await;
                let _ = client.shutdown().await;
                println!("{}", json!({"status": "shutdown-requested"}));
                ExitCode::SUCCESS
            }
            Err(_) => {
                println!("{}", json!({"status": "not-running"}));
                ExitCode::SUCCESS
            }
        }
    })
}

/// doctor：不接触档案数据的自检输出（媒体后端、WebView2、路径、发行端点）。
fn run_doctor(profile: &Profile) -> ExitCode {
    let backend = tokio::runtime::Runtime::new()
        .expect("doctor runtime")
        .block_on(trace_core::media::MediaBackend::detect(profile));
    let webview2 = detect_webview2();
    let report = json!({
        "version": env!("CARGO_PKG_VERSION"),
        "media_backend": {
            "ffmpeg": backend.ffmpeg.as_ref().map(|p| p.to_string_lossy().to_string()),
            "ffprobe": backend.ffprobe.as_ref().map(|p| p.to_string_lossy().to_string()),
            "version": backend.version,
            "source": backend.source,
            "variant": "lgpl-shared",
            "reason": backend.reason,
            "note": "缺失时仍可保存原件与外部打开；探测/封装能力受限并如实显示未知",
        },
        "webview2": webview2,
        "profile": {
            "archive_id": profile.archive_id,
            "data_root": profile.data_root,
            "archive_root": profile.archive_root,
            "adapter_dir": profile.adapter_dir,
            "test_allowed_hosts": profile.test_allowed_hosts,
        },
        "release_check": {
            "test_allowed_hosts_empty": profile.test_allowed_hosts.is_empty(),
        },
    });
    println!(
        "{}",
        serde_json::to_string_pretty(&report).unwrap_or_default()
    );
    if profile.test_allowed_hosts.is_empty() {
        ExitCode::SUCCESS
    } else {
        // 发行自检：显式配置了测试端点 → 醒目失败（C32）。
        eprintln!("trace-daemon: doctor: profile allows test endpoints; not a release-safe config");
        ExitCode::from(4)
    }
}

/// WebView2 运行时探测：注册表键或安装目录任一存在即视为可用。
fn detect_webview2() -> serde_json::Value {
    let reg = std::process::Command::new("reg")
        .args([
            "query",
            r"HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}",
            "/v",
            "pv",
        ])
        .output()
        .ok();
    let reg_version = reg
        .and_then(|o| String::from_utf8(o.stdout).ok())
        .and_then(|s| {
            s.lines()
                .find(|l| l.contains("pv"))
                .map(|l| l.trim().to_string())
        });
    let dir = std::path::Path::new(r"C:\Program Files (x86)\Microsoft\EdgeWebView\Application");
    json!({
        "registry": reg_version,
        "install_dir_present": dir.exists(),
        "available": reg_version.is_some() || dir.exists(),
    })
}

// 保持 stdout 行缓冲（供进程级测试读取就绪行）。
fn _flush() {
    let _ = std::io::stdout().flush();
}
