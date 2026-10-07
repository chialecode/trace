//! daemon 进程级验证（C09/C12/C22）：
//! 双启动竞争、锁定档案、IPC ACL、错档案/超长帧拒绝、优雅退出。
//!
//! 需要先构建工作区（cargo build --workspace），daemon 二进制位于 target/debug。

use std::io::{Read, Write};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};
use tempfile::TempDir;
use trace_core::config::Profile;
use trace_core::ipc::client::Client;
use trace_e2e::{daemon_exe, test_profile};

fn write_profile(root: &std::path::Path) -> std::path::PathBuf {
    let profile: Profile = test_profile(root, &trace_e2e::adapter_dir());
    let path = root.join("profile.json");
    std::fs::write(&path, serde_json::to_vec_pretty(&profile).unwrap()).unwrap();
    path
}

#[tokio::test(flavor = "multi_thread")]
async fn dual_start_lock_acl_and_clean_shutdown() {
    let exe = daemon_exe();
    assert!(
        exe.exists(),
        "先运行 cargo build --workspace（缺少 {exe:?}）"
    );
    let root = TempDir::new().unwrap();
    let profile_path = write_profile(root.path());
    let profile = test_profile(root.path(), &trace_e2e::adapter_dir());

    // 1) 首个实例启动。
    let mut first = ProcessGuard(
        Command::new(&exe)
            .arg("start")
            .arg("--profile")
            .arg(&profile_path)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .expect("spawn daemon"),
    );

    // 就绪以握手为准（不凭 PID/文件）。
    let deadline = Instant::now() + Duration::from_secs(30);
    let client = loop {
        match Client::connect(&profile.pipe_name(), "e2e", Some(&profile.archive_id)).await {
            Ok(c) => break c,
            Err(_) if Instant::now() < deadline => {
                tokio::time::sleep(Duration::from_millis(400)).await;
            }
            Err(e) => panic!("daemon 未在 30s 内就绪：{e}"),
        }
    };

    // 2) 双启动：第二实例识别既有端点并成功退出（already-running）。
    let second = Command::new(&exe)
        .arg("start")
        .arg("--profile")
        .arg(&profile_path)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .expect("spawn second daemon");
    let out = String::from_utf8_lossy(&second.stdout);
    assert!(
        second.status.success() && out.contains("already-running"),
        "第二实例必须识别既有端点：status={:?} out={out}",
        second.status
    );

    // 3) 错 archive_id 握手必须被拒绝（C22）。
    let wrong = Client::connect(&profile.pipe_name(), "e2e", Some("other-archive")).await;
    assert!(wrong.is_err(), "错 archive_id 不应握手成功");

    // 4) 管道 DACL：受保护 + 当前用户/SY/BA（C22）。
    let sid = trace_core::ipc::server::current_user_sid().expect("sid");
    let acl_ok = trace_core::ipc::server::verify_pipe_acl(&profile.pipe_name(), &sid)
        .expect("read pipe acl");
    assert!(acl_ok, "管道 ACL 不符合受限 DACL 约束");

    // 5) 超长帧：直连管道写 2 MiB 长度头，服务端拒绝且不崩溃（C15/C22）。
    {
        let mut raw = std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .open(profile.pipe_name())
            .expect("open pipe raw");
        let oversize: u32 = 2 * 1024 * 1024;
        let _ = raw.write_all(&oversize.to_le_bytes());
        let _ = raw.write_all(&[0u8; 64]);
        let _ = raw.flush();
        // 服务端应关闭该会话；读取得到 EOF 或错误即可。
        let mut buf = [0u8; 16];
        let _ = raw.read(&mut buf);
    }
    // 服务仍在服务：正常请求可用（daemon.status）。
    let status = client
        .request("daemon.status", serde_json::json!({}), None, None)
        .await
        .expect("daemon.status after oversize frame");
    assert!(status["instance_id"].is_string());

    // F-013: a reconnect starts at the current sequence; it must not replay old creators.
    for i in 0..4 {
        client
            .request(
                "creator.create",
                serde_json::json!({"display_name":format!("history-{i}")}),
                None,
                None,
            )
            .await
            .unwrap();
    }
    let mut reconnected = Client::connect(
        &profile.pipe_name(),
        "cursor-test",
        Some(&profile.archive_id),
    )
    .await
    .unwrap();
    reconnected
        .request("daemon.status", serde_json::json!({}), None, None)
        .await
        .unwrap();
    assert!(
        tokio::time::timeout(Duration::from_millis(600), reconnected.events.recv())
            .await
            .is_err(),
        "historical outbox was replayed"
    );
    client
        .request(
            "creator.create",
            serde_json::json!({"display_name":"future-event"}),
            None,
            None,
        )
        .await
        .unwrap();
    assert!(matches!(
        tokio::time::timeout(Duration::from_secs(2), reconnected.events.recv())
            .await
            .unwrap(),
        Some(trace_core::ipc::client::ClientEvent::Change(_))
    ));
    reconnected.shutdown().await;

    // Fragment one large request across timer ticks while a second client publishes changes.
    {
        use tokio::io::AsyncWriteExt;
        use trace_contracts::control::{HelloRequest, Request, ServerFrame};
        let mut pipe = tokio::net::windows::named_pipe::ClientOptions::new()
            .open(profile.pipe_name())
            .unwrap();
        let hello = HelloRequest {
            client: "fragment-test".into(),
            protocol_version: "0.1".into(),
            archive_id: Some(profile.archive_id.clone()),
        };
        pipe.write_all(&trace_contracts::wire::encode_frame(&hello).unwrap())
            .await
            .unwrap();
        trace_core::ipc::server::read_frame_bytes(&mut pipe)
            .await
            .unwrap();
        let request = Request {
            protocol_version: "0.1".into(),
            request_id: "large-fragment".into(),
            command: "daemon.status".into(),
            idempotency_key: None,
            if_revision: None,
            args: serde_json::json!({"padding":"x".repeat(800_000)}),
        };
        let encoded = trace_contracts::wire::encode_frame(&request).unwrap();
        let (mut reader, mut writer) = tokio::io::split(pipe);
        let send = async {
            for (i, chunk) in encoded.chunks(4096).enumerate() {
                writer.write_all(chunk).await.unwrap();
                if i % 40 == 0 {
                    client
                        .request(
                            "creator.create",
                            serde_json::json!({"display_name":format!("concurrent-{i}")}),
                            None,
                            None,
                        )
                        .await
                        .unwrap();
                }
                tokio::time::sleep(Duration::from_millis(4)).await;
            }
        };
        let receive = async {
            let mut saw_change = false;
            loop {
                let bytes = trace_core::ipc::server::read_frame_bytes(&mut reader)
                    .await
                    .unwrap();
                match serde_json::from_slice::<ServerFrame>(&bytes).unwrap() {
                    ServerFrame::Response(r) => {
                        assert_eq!(r.request_id, "large-fragment");
                        assert!(r.ok);
                        break;
                    }
                    ServerFrame::Event(_) => saw_change = true,
                    _ => {}
                }
            }
            assert!(saw_change, "no events interleaved with fragmented frame");
        };
        tokio::time::timeout(Duration::from_secs(15), async {
            tokio::join!(send, receive);
        })
        .await
        .unwrap();
    }

    // 6) 优雅退出：shutdown 命令后进程退出，无残留。
    let _ = client
        .request("daemon.shutdown", serde_json::json!({}), None, None)
        .await;
    client.shutdown().await;
    let exited = wait_exit(&mut first, Duration::from_secs(15));
    assert!(exited, "daemon 未在 15s 内退出（检查子进程残留）");
    let _ = first.kill();

    // 7) 退出后再启动：档案可恢复（锁随进程释放）。
    let mut third = ProcessGuard(
        Command::new(&exe)
            .arg("start")
            .arg("--profile")
            .arg(&profile_path)
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn third daemon"),
    );
    let deadline = Instant::now() + Duration::from_secs(30);
    let c3 = loop {
        match Client::connect(&profile.pipe_name(), "e2e", Some(&profile.archive_id)).await {
            Ok(c) => break c,
            Err(_) if Instant::now() < deadline => {
                tokio::time::sleep(Duration::from_millis(400)).await;
            }
            Err(e) => panic!("重启后未就绪：{e}"),
        }
    };
    let _ = c3
        .request("daemon.shutdown", serde_json::json!({}), None, None)
        .await;
    c3.shutdown().await;
    let _ = wait_exit(&mut third, Duration::from_secs(15));
    let _ = third.kill();
}

fn wait_exit(child: &mut std::process::Child, timeout: Duration) -> bool {
    let start = Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return true,
            Ok(None) if start.elapsed() < timeout => {
                std::thread::sleep(Duration::from_millis(200));
            }
            _ => return false,
        }
    }
}

// Always reap this test's daemon, including when a fault assertion panics.
struct ProcessGuard(std::process::Child);
impl std::ops::Deref for ProcessGuard {
    type Target = std::process::Child;
    fn deref(&self) -> &Self::Target {
        &self.0
    }
}
impl std::ops::DerefMut for ProcessGuard {
    fn deref_mut(&mut self) -> &mut Self::Target {
        &mut self.0
    }
}
impl Drop for ProcessGuard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}
