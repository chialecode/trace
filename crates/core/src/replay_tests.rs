//! Recorded session ↔ replay linking (stage plan §11.3, C17).
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use std::sync::Arc;

async fn core() -> (tempfile::TempDir, Arc<Core>) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    (dir, core)
}

async fn call(
    core: &Arc<Core>,
    command: &str,
    args: Value,
) -> Result<Value, crate::error::CoreError> {
    crate::app::execute(
        core,
        &trace_contracts::control::Request {
            protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
            request_id: uuid::Uuid::new_v4().to_string(),
            command: command.into(),
            idempotency_key: None,
            if_revision: None,
            args,
        },
    )
    .await
}

async fn ok(core: &Arc<Core>, command: &str, args: Value) -> Value {
    call(core, command, args)
        .await
        .unwrap_or_else(|e| panic!("{command}: {e}"))
}

/// One sealed session with intervals 0–5000, 5000–12000 and 15000–18000, plus a replay entry.
async fn fixture(core: &Arc<Core>, with_specs: bool) -> (String, String) {
    let spec = if with_specs {
        json!({"duration_ms": 8000, "width": 1280, "height": 720, "audio_tracks": 1, "audio_channels": 2, "codec": "h264"})
    } else {
        Value::Null
    };
    let replay_spec = json!({"duration_ms": 8000, "width": 1280, "height": 720, "audio_tracks": 1, "audio_channels": 2, "codec": "h264"});
    core.db
        .write(move |tx| {
            for (id, name) in [("a", "账号"), ("b", "别的账号")] {
                tx.execute(
                    "INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,'bilibili',?1,?2,'t')",
                    rusqlite::params![id, name],
                )?;
            }
            tx.execute(
                "INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','live','{}','t','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO live_sessions(session_id,subscription_id,room_key,source_session,state,started_at,ended_at) VALUES('l','s','room','1','sealed','2026-01-01T10:00:00Z','2026-01-01T12:00:00Z')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('live-entry','bilibili','live','room','a','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('lr','live-entry','d','直播','','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('fm','lr','recording_video',0,'x')",
                [],
            )?;
            for (seq, start, end) in [(1, 0, 5000), (2, 5000, 12000), (3, 15000, 18000)] {
                tx.execute(
                    "INSERT INTO live_fragments(fragment_id,session_id,sequence,source_sequence,media_id,state,start_ms,end_ms) VALUES(?1,'l',?2,?2,'fm',?3,?4,?5)",
                    rusqlite::params![format!("f{seq}"), seq, "sealed", start, end],
                )?;
            }
            if with_specs {
                tx.execute(
                    "INSERT INTO blobs(blob_id,size,first_seen_at) VALUES('sha256:aa',1,'t'),('sha256:bb',1,'t') ON CONFLICT(blob_id) DO NOTHING",
                    [],
                )?;
                tx.execute(
                    "INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES('fv','fm','recording-v1','sha256:aa','{}',?1,'t')",
                    rusqlite::params![spec.to_string()],
                )?;
            }
            // A replay entry of the same account, with a measured duration of 8 s.
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES('e','bilibili','video','BV1','a','t','2026-01-01T12:10:00Z')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','直播回放','回放正文','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('m','r','video',0,'v')",
                [],
            )?;
            if with_specs {
                tx.execute(
                    "INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES('v','m','video-v1','sha256:bb','{}',?1,'t')",
                    rusqlite::params![replay_spec.to_string()],
                )?;
            }
            Ok(())
        })
        .await
        .unwrap();
    ("l".into(), "e".into())
}

#[tokio::test]
async fn replay_candidates_compare_instants_with_timezone_offsets() {
    let (_dir, core) = core().await;
    let (session, _) = fixture(&core, true).await;
    core.db.write(|tx| {
        for (id, published) in [
            ("too-early", "2026-01-01T01:00:00Z"),
            ("inside-next-day", "2026-01-02T12:00:00Z"),
            ("inside-offset", "2026-01-03T01:00:00+08:00"),
            ("too-late", "2026-01-02T19:00:00Z"),
        ] {
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'bilibili','video',?1,'a','t',?2)", rusqlite::params![id, published])?;
        }
        Ok(())
    }).await.unwrap();
    let candidates = ok(
        &core,
        "live.replay.candidates",
        json!({"session_id": session}),
    )
    .await;
    let ids = candidates["items"]
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|r| r["entry_id"].as_str())
        .collect::<Vec<_>>();
    assert!(
        !ids.contains(&"too-early"),
        "outside lower instant boundary: {ids:?}"
    );
    assert!(
        !ids.contains(&"too-late"),
        "outside upper instant boundary: {ids:?}"
    );
    assert!(
        ids.contains(&"inside-next-day"),
        "inside upper-bound date: {ids:?}"
    );
    assert!(
        ids.contains(&"inside-offset"),
        "timezone-normalized inside instant: {ids:?}"
    );
}

#[tokio::test]
async fn clipped_replay_keeps_recorded_ranges() {
    let (_dir, core) = core().await;
    let (session, entry) = fixture(&core, true).await;
    let before: (i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row(
                    "SELECT count(*) FROM live_fragments WHERE session_id='l'",
                    [],
                    |r| r.get(0),
                )?,
                c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    let linked = ok(
        &core,
        "live.replay.link",
        json!({"session_id": session, "entry_id": entry, "decision": "keep_both"}),
    )
    .await;
    let coverage = &linked["coverage"];
    assert_eq!(coverage["complete"], false);
    let unique = coverage["unique_recorded_ranges"].as_array().unwrap();
    assert_eq!(unique.len(), 1);
    assert_eq!(unique[0]["start_ms"], 8000);
    assert_eq!(unique[0]["end_ms"], 18000);
    assert_eq!(linked["recording_kept"], true);
    let after: (i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row(
                    "SELECT count(*) FROM live_fragments WHERE session_id='l'",
                    [],
                    |r| r.get(0),
                )?,
                c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    assert_eq!(
        before, after,
        "linking never removes a fragment or a variant"
    );
}

#[tokio::test]
async fn replay_link_requires_same_account() {
    let (_dir, core) = core().await;
    let (session, _) = fixture(&core, true).await;
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('other','bilibili','video','BV2','b','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('ro','other','d','别处回放','正文','t')",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        call(
            &core,
            "live.replay.link",
            json!({"session_id": session, "entry_id": "other"}),
        )
        .await
        .is_err(),
        "another account's video cannot be this session's replay"
    );
    // An unsealed session is refused too.
    core.db
        .write(|tx| {
            tx.execute(
                "UPDATE live_sessions SET state='recording' WHERE session_id='l'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        call(
            &core,
            "live.replay.link",
            json!({"session_id": session, "entry_id": "e"}),
        )
        .await
        .is_err(),
        "an unsealed recording is not linked"
    );
}

#[tokio::test]
async fn incomparable_quality_keeps_both() {
    let (_dir, core) = core().await;
    let (session, entry) = fixture(&core, false).await;
    let linked = ok(
        &core,
        "live.replay.link",
        json!({"session_id": session, "entry_id": entry}),
    )
    .await;
    assert_eq!(linked["quality"], "incomparable");
    assert_eq!(linked["link"]["decision"], "keep_both");
    assert!(
        call(
            &core,
            "live.replay.link",
            json!({"session_id": session, "entry_id": entry, "decision": "replay_primary"}),
        )
        .await
        .is_err(),
        "an incomparable replay cannot become primary"
    );
}

#[tokio::test]
async fn the_first_relation_can_be_linked_or_rejected_from_zero() {
    let (_dir, core) = core().await;
    let (session, entry) = fixture(&core, true).await;
    let candidates = ok(
        &core,
        "live.replay.candidates",
        json!({"session_id": session}),
    )
    .await;
    let candidate = candidates["items"]
        .as_array()
        .unwrap()
        .iter()
        .find(|item| item["entry_id"] == entry)
        .unwrap_or_else(|| panic!("{candidates}"));
    assert_eq!(candidate["coverage"]["complete"], false);
    assert_eq!(
        candidate["coverage"]["unique_recorded_ranges"][0]["start_ms"],
        8000
    );
    assert!(
        matches!(candidate["quality"].as_str(), Some("same" | "unknown")),
        "{candidate}"
    );
    assert_eq!(candidate["can_replay_primary"], false);
    assert!(
        candidate["comparison_reason"]
            .as_str()
            .unwrap()
            .contains("shorter")
    );
    let links_before = ok(&core, "live.replay.links", json!({"session_id": session})).await;
    assert_eq!(
        links_before["items"].as_array().unwrap().len(),
        0,
        "candidate comparison is read-only"
    );
    let rejected = ok(
        &core,
        "live.replay.reject",
        json!({"session_id": session, "entry_id": entry, "reason": "不是同一场直播"}),
    )
    .await;
    assert_eq!(rejected["rejected"], true);
    let links = ok(&core, "live.replay.links", json!({"session_id": session})).await;
    assert_eq!(links["items"][0]["state"], "rejected");
    let linked = ok(
        &core,
        "live.replay.link",
        json!({"session_id": session, "entry_id": entry, "decision": "keep_both"}),
    )
    .await;
    assert_eq!(linked["link"]["state"], "confirmed");
    assert_eq!(linked["link"]["decision"], "keep_both");
    assert_eq!(linked["recording_kept"], true);
    let fragments: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM live_fragments WHERE session_id='l'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(fragments, 3, "all recorded fragments remain");
}

#[tokio::test]
async fn replay_reject_is_recorded() {
    let (_dir, core) = core().await;
    let (session, entry) = fixture(&core, true).await;
    ok(
        &core,
        "live.replay.link",
        json!({"session_id": session, "entry_id": entry}),
    )
    .await;
    let rejected = ok(
        &core,
        "live.replay.reject",
        json!({"session_id": session, "entry_id": entry, "reason": "不是同一场直播"}),
    )
    .await;
    assert_eq!(rejected["rejected"], true);
    let links = ok(&core, "live.replay.links", json!({"session_id": session})).await;
    assert_eq!(links["items"][0]["state"], "rejected");
    let audit = ok(
        &core,
        "management.operations",
        json!({"kind": "replay_reject"}),
    )
    .await;
    assert_eq!(audit["items"].as_array().unwrap().len(), 1);
}
