//! Synthetic profile only: the logon entry point never changes HKCU Run here.
#[cfg(windows)]
#[test]
fn windows_notification_history_with_suppressed_popup() {
    use std::{os::windows::process::CommandExt, process::Command};
    let output = Command::new(trace_e2e::adapter_dir().join("windows_probe.exe"))
        .arg("--notify-synthetic")
        .creation_flags(0x08000000)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(String::from_utf8_lossy(&output.stdout).contains("own-tag history verified"));
}
#[cfg(windows)]
#[tokio::test]
async fn background_launcher_double_start_single_daemon_and_invalid_profile() {
    use std::{os::windows::process::CommandExt, process::Command, time::Duration};
    use trace_core::{config::Profile, ipc::client::Client};
    let dir = tempfile::tempdir().unwrap();
    let binaries = trace_e2e::adapter_dir();
    let profile = Profile {
        archive_id: format!("startup-{}", uuid::Uuid::new_v4()),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        adapter_dir: Some(binaries.clone()),
        ..Default::default()
    };
    let path = dir.path().join("profile with spaces.json");
    std::fs::write(&path, serde_json::to_vec(&profile).unwrap()).unwrap();
    let launch = |path: &std::path::Path| {
        let mut c = Command::new(binaries.join("trace-desktop.exe"));
        c.creation_flags(0x08000000)
            .args(["--background-only", "--profile"])
            .arg(path)
            .env_remove("TRACE_PROFILE")
            .env_remove("TRACE_DAEMON_PATH")
            .spawn()
            .unwrap()
    };
    let first = launch(&path);
    let second = launch(&path);
    let statuses = tokio::task::spawn_blocking(move || {
        let mut first = first;
        let mut second = second;
        (first.wait().unwrap(), second.wait().unwrap())
    })
    .await
    .unwrap();
    assert!(statuses.0.success() && statuses.1.success());
    let client = Client::connect(
        &profile.pipe_name(),
        "startup-test",
        Some(&profile.archive_id),
    )
    .await
    .unwrap();
    let status = client
        .request("daemon.status", serde_json::json!({}), None, None)
        .await
        .unwrap();
    let another = launch(&path);
    assert!(
        tokio::task::spawn_blocking(move || {
            let mut c = another;
            c.wait().unwrap()
        })
        .await
        .unwrap()
        .success()
    );
    let later = client
        .request("daemon.status", serde_json::json!({}), None, None)
        .await
        .unwrap();
    assert_eq!(status["instance_id"], later["instance_id"]);
    assert!(status["instance_id"].is_string());
    let invalid = dir.path().join("missing.json");
    let child = launch(&invalid);
    assert!(
        !tokio::task::spawn_blocking(move || {
            let mut c = child;
            c.wait().unwrap()
        })
        .await
        .unwrap()
        .success()
    );
    assert!(!invalid.exists());
    for args in [
        vec!["--profile", invalid.to_str().unwrap()],
        vec!["--profile"],
    ] {
        let result = Command::new(binaries.join("trace-desktop.exe"))
            .args(args)
            .env_remove("TRACE_PROFILE")
            .creation_flags(0x08000000)
            .output()
            .unwrap();
        assert_eq!(
            result.status.code(),
            Some(2),
            "invalid explicit GUI profile must fail before creating a window"
        );
    }
    client
        .request("daemon.shutdown", serde_json::json!({}), None, None)
        .await
        .unwrap();
    tokio::time::timeout(Duration::from_secs(10), async {
        loop {
            if Client::connect(
                &profile.pipe_name(),
                "startup-test",
                Some(&profile.archive_id),
            )
            .await
            .is_err()
            {
                break;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    })
    .await
    .unwrap();
}
