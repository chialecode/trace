//! Preview an author's profile, choose entries across pages and download them to a folder:
//! nothing is fetched or subscribed while browsing, a selection becomes one request whose
//! readable copies are written once all of its work has settled, and an expired preview is
//! resolved again instead of being trusted.
use serde_json::{Value, json};
use std::time::Duration;
use tempfile::TempDir;
use trace_contracts::control::{Request, commands as cmd};
use trace_contracts::preview::ProfilePage;
use trace_core::core::Core;
use trace_core::ipc::dispatch;
use trace_core::profile_preview::{Begin, start};
use trace_e2e::{FakeSource, test_profile};

fn req(command: &'static str, args: Value) -> Request {
    Request {
        protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
        request_id: format!("e2e-{}", uuid::Uuid::new_v4()),
        command: command.into(),
        idempotency_key: Some(format!("key-{}", uuid::Uuid::new_v4())),
        if_revision: None,
        args,
    }
}

async fn call(
    core: &std::sync::Arc<Core>,
    command: &'static str,
    args: Value,
) -> Result<Value, String> {
    let response = dispatch::dispatch(core, req(command, args)).await;
    if response.ok {
        Ok(response.result.unwrap_or(Value::Null))
    } else {
        Err(format!("{:?}", response.error))
    }
}

async fn count(core: &Core, sql: &'static str) -> i64 {
    core.db
        .read(move |c| Ok(c.query_row(sql, [], |r| r.get::<_, i64>(0))?))
        .await
        .unwrap_or(-1)
}

async fn open_profile(core: &Core) -> ProfilePage {
    start(
        core,
        Begin {
            site: "fake".into(),
            url: "https://fake.example/up-1".into(),
            rule: (
                "fake.user.preview".into(),
                "作者主页".into(),
                Some("video".into()),
            ),
            alternatives: Vec::new(),
            credential_id: None,
        },
    )
    .await
    .expect("profile preview")
}

async fn wait_delivery(core: &std::sync::Arc<Core>, id: &str) -> Value {
    let deadline = std::time::Instant::now() + Duration::from_secs(180);
    loop {
        let list = call(core, cmd::SOURCE_DELIVERIES, json!({"delivery_id": id}))
            .await
            .unwrap();
        let delivery = list["items"][0].clone();
        if delivery["state"] == "done" {
            return delivery;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "delivery did not finish: {delivery}"
        );
        tokio::time::sleep(Duration::from_millis(400)).await;
    }
}

fn files_below(dir: &std::path::Path) -> Vec<std::path::PathBuf> {
    let mut all = Vec::new();
    trace_e2e::collect_files(dir, &mut all);
    all
}

#[tokio::test(flavor = "multi_thread")]
async fn a_profile_is_browsed_without_fetching_and_the_chosen_entries_land_in_a_folder() {
    let source = FakeSource::start();
    // SAFETY: this test binary holds one test, and the variable is set before any adapter starts.
    unsafe {
        std::env::set_var("FAKE_BASE_URL", &source.base_url);
    }
    let root = TempDir::new().unwrap();
    let out = TempDir::new().unwrap();
    let profile = test_profile(
        root.path(),
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../target/debug")
            .as_path(),
    );
    let core = Core::open(profile).await.expect("core open");
    core.start_scheduler();

    // Browsing: the first page lists v-1; nothing is downloaded, subscribed or recorded.
    let first = open_profile(&core).await;
    assert_eq!(first.items.len(), 1);
    assert_eq!(first.items[0].native_key, "v-1");
    assert!(!first.items[0].in_library);
    assert!(first.has_more && !first.limited);
    assert_eq!(
        (
            first.pages_loaded,
            first.total_items,
            first.account_key.as_str()
        ),
        (1, 1, "up-1")
    );
    assert_eq!(first.authentication, "anonymous");
    for table in [
        "media_items",
        "blobs",
        "entries",
        "subscriptions",
        "external_copies",
    ] {
        let sql: &'static str = Box::leak(format!("SELECT count(*) FROM {table}").into_boxed_str());
        assert_eq!(
            count(&core, sql).await,
            0,
            "{table} must stay empty while browsing"
        );
    }

    assert_eq!(
        count(
            &core,
            "SELECT count(*) FROM jobs WHERE job_type IN ('scan','observe','media','source_acquire','delivery')"
        )
        .await,
        0,
        "browsing queues no work"
    );

    // The next page continues the same preview; asking past the end is refused.
    let second: ProfilePage = serde_json::from_value(
        call(
            &core,
            cmd::SOURCE_PROFILE_PREVIEW,
            json!({"preview_id": first.preview_id}),
        )
        .await
        .unwrap(),
    )
    .unwrap();
    assert_eq!(second.items.len(), 1);
    assert_eq!(second.items[0].native_key, "v-2");
    assert_eq!((second.pages_loaded, second.total_items), (2, 2));
    assert!(!second.has_more);
    assert!(
        call(
            &core,
            cmd::SOURCE_PROFILE_PREVIEW,
            json!({"preview_id": first.preview_id}),
        )
        .await
        .is_err()
    );
    assert!(
        call(
            &core,
            cmd::SOURCE_PROFILE_PREVIEW,
            json!({"preview_id": first.preview_id, "url": "https://fake.example/x"}),
        )
        .await
        .is_err(),
        "continuing a preview takes no new link"
    );

    // A selection can only name what was shown.
    let stranger = call(
        &core,
        cmd::SOURCE_PROFILE_DOWNLOAD,
        json!({"preview_id": first.preview_id, "native_keys": ["not-listed"],
               "target": {"kind": "library"}}),
    )
    .await;
    assert!(stranger.is_err());
    assert!(
        call(
            &core,
            cmd::SOURCE_PROFILE_DOWNLOAD,
            json!({"preview_id": first.preview_id, "native_keys": ["v-1"],
                   "target": {"kind": "folder", "dir": "relative\\path"}}),
        )
        .await
        .is_err(),
        "an unusable folder is refused before anything is queued"
    );
    assert_eq!(count(&core, "SELECT count(*) FROM deliveries").await, 0);

    // Download both entries, chosen across the two pages, to a folder.
    let dir = out.path().join("downloads");
    let report = call(
        &core,
        cmd::SOURCE_PROFILE_DOWNLOAD,
        json!({"preview_id": first.preview_id, "native_keys": ["v-1", "v-2", "v-1"],
               "target": {"kind": "folder", "dir": dir.to_string_lossy()}}),
    )
    .await
    .unwrap();
    assert_eq!(
        (
            report["accepted"].as_u64(),
            report["already_in_library"].as_u64()
        ),
        (Some(2), Some(0))
    );
    let delivery = wait_delivery(&core, report["delivery_id"].as_str().unwrap()).await;
    for item in delivery["items"].as_array().unwrap() {
        assert_eq!(item["state"], "exported", "{item}");
        assert!(item["files"].as_i64().unwrap() > 0);
    }
    let written = files_below(&dir);
    assert!(
        written
            .iter()
            .any(|p| p.extension().is_some_and(|e| e == "json")),
        "a sidecar is written beside the media: {written:?}"
    );
    assert!(
        written
            .iter()
            .all(|p| !p.to_string_lossy().contains(".trace-tmp")),
        "no temporary file is left behind"
    );
    assert!(
        count(
            &core,
            "SELECT count(*) FROM external_copies WHERE state='present'"
        )
        .await
            >= 2
    );
    assert_eq!(count(&core, "SELECT count(*) FROM entries").await, 2);

    // Browsing again marks the entries as held, and downloading them again fetches nothing.
    let again = open_profile(&core).await;
    assert!(again.items[0].in_library);
    assert_eq!(again.items[0].save_progress.as_deref(), Some("complete"));
    let media_jobs = count(&core, "SELECT count(*) FROM jobs WHERE job_type='media'").await;
    let other = out.path().join("second");
    let report = call(
        &core,
        cmd::SOURCE_PROFILE_DOWNLOAD,
        json!({"preview_id": again.preview_id, "native_keys": ["v-1"],
               "target": {"kind": "folder", "dir": other.to_string_lossy()}}),
    )
    .await
    .unwrap();
    assert_eq!(
        (
            report["accepted"].as_u64(),
            report["already_in_library"].as_u64()
        ),
        (Some(0), Some(1))
    );
    let delivery = wait_delivery(&core, report["delivery_id"].as_str().unwrap()).await;
    assert_eq!(delivery["items"][0]["state"], "exported");
    assert!(!files_below(&other).is_empty());
    assert_eq!(
        count(&core, "SELECT count(*) FROM jobs WHERE job_type='media'").await,
        media_jobs,
        "the library already held the bytes"
    );

    // A preview that expired is not trusted: the caller resolves the profile again.
    core.db
        .write(|tx| {
            tx.execute(
                "UPDATE profile_previews SET expires_at='2000-01-01T00:00:00Z'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    for result in [
        call(
            &core,
            cmd::SOURCE_PROFILE_DOWNLOAD,
            json!({"preview_id": again.preview_id, "native_keys": ["v-1"],
                   "target": {"kind": "library"}}),
        )
        .await,
        call(
            &core,
            cmd::SOURCE_PROFILE_PREVIEW,
            json!({"preview_id": again.preview_id}),
        )
        .await,
    ] {
        let error = result.unwrap_err();
        assert!(error.contains("VersionConflict"), "{error}");
    }
    let fresh = open_profile(&core).await;
    assert_ne!(fresh.preview_id, again.preview_id);
}
