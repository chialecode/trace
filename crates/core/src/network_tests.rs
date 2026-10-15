//! Network settings through the control commands: validation, persistence, the proxy login
//! in the credential store only, and what the settings page shows.
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use trace_contracts::control::Request;

async fn core(dir: &tempfile::TempDir) -> std::sync::Arc<Core> {
    Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap()
}

async fn run(
    core: &std::sync::Arc<Core>,
    command: &str,
    args: Value,
) -> Result<Value, crate::error::CoreError> {
    crate::app::execute(
        core,
        &Request {
            protocol_version: "0.1".into(),
            request_id: "r".into(),
            command: command.into(),
            args,
            idempotency_key: None,
            if_revision: None,
        },
    )
    .await
}

fn site<'a>(view: &'a Value, id: &str) -> &'a Value {
    view["sites"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["site"] == id)
        .unwrap()
}

#[tokio::test]
async fn defaults_follow_the_system_for_overseas_sites_and_connect_directly_elsewhere() {
    let dir = tempfile::tempdir().unwrap();
    let core = core(&dir).await;
    let view = run(&core, "network.get", json!({})).await.unwrap();
    assert_eq!(view["settings"].as_array().unwrap().len(), 0);
    assert_eq!(view["sites"].as_array().unwrap().len(), 8);
    for overseas in ["instagram", "x", "youtube", "tiktok"] {
        let s = site(&view, overseas);
        assert_eq!(
            (s["mode"].as_str(), s["source"].as_str()),
            (Some("system"), Some("default")),
            "{overseas}"
        );
        assert_eq!(s["advice"], "proxy_recommended");
    }
    for domestic in ["weibo", "xiaohongshu", "douyin", "bilibili"] {
        let s = site(&view, domestic);
        assert_eq!(
            (s["mode"].as_str(), s["route"].as_str()),
            (Some("direct"), Some("direct")),
            "{domestic}"
        );
    }
}

#[tokio::test]
async fn a_manual_proxy_is_validated_saved_and_applied_to_the_chosen_scope() {
    let dir = tempfile::tempdir().unwrap();
    let core = core(&dir).await;
    let view = run(
        &core,
        "network.set",
        json!({"scope": "global", "mode": "manual", "proxy_url": "http://127.0.0.1:7890"}),
    )
    .await
    .unwrap();
    assert_eq!(view["settings"][0]["scope"], "global");
    for s in view["sites"].as_array().unwrap() {
        assert_eq!(
            (s["route"].as_str(), s["source"].as_str()),
            (Some("proxy"), Some("global")),
            "{s}"
        );
    }
    let view = run(
        &core,
        "network.set",
        json!({"scope": "bilibili", "mode": "direct"}),
    )
    .await
    .unwrap();
    assert_eq!(site(&view, "bilibili")["route"], "direct");
    assert_eq!(site(&view, "bilibili")["source"], "site");
    assert_eq!(site(&view, "douyin")["route"], "proxy");
    let view = run(
        &core,
        "network.set",
        json!({"scope": "bilibili", "mode": "default"}),
    )
    .await
    .unwrap();
    assert_eq!(site(&view, "bilibili")["source"], "global");
}

#[tokio::test]
async fn invalid_settings_are_refused_with_a_reason() {
    let dir = tempfile::tempdir().unwrap();
    let core = core(&dir).await;
    for args in [
        json!({"scope": "myspace", "mode": "direct"}),
        json!({"scope": "global", "mode": "tunnel"}),
        json!({"scope": "global", "mode": "manual"}),
        json!({"scope": "global", "mode": "manual", "proxy_url": "127.0.0.1:7890"}),
        json!({"scope": "global", "mode": "manual", "proxy_url": "ftp://127.0.0.1:21"}),
        json!({"scope": "global", "mode": "manual", "proxy_url": "http://user:pw@127.0.0.1:7890"}),
        json!({"scope": "global", "mode": "manual", "proxy_url": "http://127.0.0.1:7890", "username": "u"}),
        json!({"scope": "global", "mode": "system", "proxy_url": "http://127.0.0.1:7890"}),
        json!({"scope": "global", "mode": "direct", "username": "u", "password": "p"}),
        json!({"scope": "global", "mode": "direct", "surprise": 1}),
    ] {
        let err = run(&core, "network.set", args.clone()).await.unwrap_err();
        assert!(
            matches!(err, crate::error::CoreError::InvalidInput(_)),
            "{args}: {err:?}"
        );
    }
    let view = run(&core, "network.get", json!({})).await.unwrap();
    assert_eq!(
        view["settings"].as_array().unwrap().len(),
        0,
        "nothing was saved"
    );
}

#[tokio::test]
async fn the_proxy_login_lives_in_the_credential_store_and_survives_a_restart() {
    let dir = tempfile::tempdir().unwrap();
    let core = core(&dir).await;
    let view = run(
        &core,
        "network.set",
        json!({"scope": "youtube", "mode": "manual", "proxy_url": "socks5://127.0.0.1:1080", "username": "alice", "password": "s3cret-pw"}),
    )
    .await
    .unwrap();
    let text = view.to_string();
    assert!(
        !text.contains("s3cret-pw") && !text.contains("alice"),
        "the login is never echoed"
    );
    assert_eq!(view["settings"][0]["has_login"], true);

    let rows: Vec<String> = core
        .db
        .read(|c| {
            Ok(c.prepare("SELECT scope||mode||ifnull(proxy_url,'')||ifnull(auth_ref,'') FROM network_settings")?
                .query_map([], |r| r.get(0))?
                .collect::<Result<Vec<_>, _>>()?)
        })
        .await
        .unwrap();
    assert!(
        rows.iter()
            .all(|r| !r.contains("s3cret-pw") && !r.contains("alice"))
    );
    let vault_files: Vec<_> = std::fs::read_dir(dir.path().join("vault"))
        .unwrap()
        .filter_map(Result::ok)
        .collect();
    assert!(!vault_files.is_empty());
    for file in vault_files {
        let raw = std::fs::read(file.path()).unwrap();
        assert!(
            !String::from_utf8_lossy(&raw).contains("s3cret-pw"),
            "the vault file is encrypted"
        );
    }

    // A restart reads the setting and the login back.
    core.network.replace(Default::default(), Default::default());
    crate::app::network::load(&core).await.unwrap();
    let crate::network::Route::Proxy(proxy) = core.network.route_for("youtube") else {
        panic!("youtube must use the saved proxy")
    };
    assert_eq!(proxy.url, "socks5://127.0.0.1:1080");
    assert_eq!(proxy.auth, Some(("alice".into(), "s3cret-pw".into())));

    // Keeping the login while changing the address; then dropping the login.
    run(
        &core,
        "network.set",
        json!({"scope": "youtube", "mode": "manual", "proxy_url": "http://127.0.0.1:8080", "keep_login": true}),
    )
    .await
    .unwrap();
    let crate::network::Route::Proxy(proxy) = core.network.route_for("youtube") else {
        panic!("proxy expected")
    };
    assert_eq!(proxy.auth, Some(("alice".into(), "s3cret-pw".into())));
    run(
        &core,
        "network.set",
        json!({"scope": "youtube", "mode": "manual", "proxy_url": "http://127.0.0.1:8080"}),
    )
    .await
    .unwrap();
    let crate::network::Route::Proxy(proxy) = core.network.route_for("youtube") else {
        panic!("proxy expected")
    };
    assert_eq!(proxy.auth, None);
    assert!(!core.vault.exists("proxy-youtube"));
}

#[tokio::test]
async fn switching_to_the_system_or_default_removes_the_saved_login() {
    let dir = tempfile::tempdir().unwrap();
    let core = core(&dir).await;
    run(
        &core,
        "network.set",
        json!({"scope": "x", "mode": "manual", "proxy_url": "http://127.0.0.1:7890", "username": "u", "password": "p"}),
    )
    .await
    .unwrap();
    assert!(core.vault.exists("proxy-x"));
    run(
        &core,
        "network.set",
        json!({"scope": "x", "mode": "system"}),
    )
    .await
    .unwrap();
    assert!(!core.vault.exists("proxy-x"));
    run(
        &core,
        "network.set",
        json!({"scope": "x", "mode": "default"}),
    )
    .await
    .unwrap();
    let view = run(&core, "network.get", json!({})).await.unwrap();
    assert_eq!(view["settings"].as_array().unwrap().len(), 0);
}

#[test]
fn the_network_commands_are_offered_to_the_interface() {
    use trace_contracts::control::commands::is_ui_command;
    assert!(is_ui_command("network.get") && is_ui_command("network.set"));
}
