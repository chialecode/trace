//! Creator relations and identity candidates (stage plan §11.1/§11.2, C13).
//!
//! The archive identity is the point: a merge, split or relink changes the explicit binding
//! and nothing else, and a candidate never changes one on its own.
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

async fn merge_ok(core: &Arc<Core>, source: &str, target: &str) -> Value {
    merge_cmd(core, source, target).await.unwrap()
}

async fn merge_cmd(
    core: &Arc<Core>,
    source: &str,
    target: &str,
) -> Result<Value, crate::error::CoreError> {
    let preview = call(
        core,
        "creator.merge_preview",
        json!({"source_creator_id": source, "target_creator_id": target}),
    )
    .await?;
    call(
        core,
        "creator.merge",
        json!({"source_creator_id": source, "target_creator_id": target, "preview_token": preview["preview_token"]}),
    )
    .await
}

async fn relink_cmd(
    core: &Arc<Core>,
    account: &str,
    expected: &str,
    target: &str,
) -> Result<Value, crate::error::CoreError> {
    let preview = call(
        core,
        "creator.relink_preview",
        json!({"account_id": account, "expected_creator_id": expected, "target_creator_id": target}),
    )
    .await?;
    call(
        core,
        "creator.relink",
        json!({"account_id": account, "expected_creator_id": expected, "target_creator_id": target, "preview_token": preview["preview_token"]}),
    )
    .await
}

async fn split_ok(
    core: &Arc<Core>,
    source: &str,
    accounts: &[String],
    display_name: &str,
) -> Value {
    split_cmd(core, source, accounts, display_name)
        .await
        .unwrap()
}

async fn split_cmd(
    core: &Arc<Core>,
    source: &str,
    accounts: &[String],
    display_name: &str,
) -> Result<Value, crate::error::CoreError> {
    let preview = call(
        core,
        "creator.split_preview",
        json!({"source_creator_id": source, "account_ids": accounts}),
    )
    .await?;
    call(
        core,
        "creator.split",
        json!({"source_creator_id": source, "account_ids": accounts, "display_name": display_name, "preview_token": preview["preview_token"]}),
    )
    .await
}

async fn relink_ok(core: &Arc<Core>, account: &str, expected: &str, target: &str) -> Value {
    let preview = ok(
        core,
        "creator.relink_preview",
        json!({"account_id": account, "expected_creator_id": expected, "target_creator_id": target}),
    )
    .await;
    ok(
        core,
        "creator.relink",
        json!({"account_id": account, "expected_creator_id": expected, "target_creator_id": target, "preview_token": preview["preview_token"]}),
    )
    .await
}

async fn creator(core: &Arc<Core>, name: &str) -> String {
    let created = ok(core, "creator.create", json!({"display_name": name})).await;
    created["creator_id"].as_str().unwrap().to_owned()
}

/// One account bound to `creator_id` with a subscription and an archived entry of its own.
async fn account_with_entry(core: &Arc<Core>, creator_id: &str, key: &str) -> (String, String) {
    let creator = creator_id.to_owned();
    let key = key.to_owned();
    let seeded = key.clone();
    core.db
        .write(move |tx| {
            let account = format!("acc-{seeded}");
            let entry = format!("entry-{seeded}");
            tx.execute(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,'weibo',?2,?3,'t')",
                rusqlite::params![account, seeded, format!("名字{seeded}")],
            )?;
            tx.execute(
                "INSERT INTO account_bindings(binding_id,creator_id,account_id,state,created_at) VALUES(?1,?2,?3,'active','t')",
                rusqlite::params![format!("bnd-{seeded}"), creator, account],
            )?;
            tx.execute(
                "INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES(?1,?2,'post','{}','t','t')",
                rusqlite::params![format!("sub-{seeded}"), account],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'weibo','post',?2,?3,'t','2026-01-01T00:00:00Z')",
                rusqlite::params![entry, seeded, account],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,'d','标题','正文','t')",
                rusqlite::params![format!("rev-{seeded}"), entry],
            )?;
            tx.execute(
                "INSERT INTO account_observations(account_id,display_name,observed_at,evidence,profile_json) VALUES(?1,?2,'t','{}','{}')",
                rusqlite::params![format!("acc-{seeded}"), format!("名字{seeded}")],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    (format!("acc-{key}"), format!("entry-{key}"))
}

/// A timeline filter that still names the merged-away creator follows the redirect, so an
/// old link or bookmark keeps showing the same archive.
#[tokio::test]
async fn merge_confirmation_recomputes_its_scope_inside_the_write_transaction() {
    use std::future::Future;
    use std::task::Poll;
    let (_dir, core) = core().await;
    let source = creator(&core, "source").await;
    let target = creator(&core, "target").await;
    let other = creator(&core, "other").await;
    account_with_entry(&core, &source, "shown").await;
    let (surprise, _) = account_with_entry(&core, &other, "not-shown").await;
    let preview = ok(
        &core,
        "creator.merge_preview",
        json!({"source_creator_id": source, "target_creator_id": target}),
    )
    .await;
    let (entered_tx, entered_rx) = tokio::sync::oneshot::channel();
    let (release_tx, release_rx) = std::sync::mpsc::channel();
    let mut blocker = Box::pin(core.db.read(move |_| {
        entered_tx.send(()).unwrap();
        release_rx.recv().unwrap();
        Ok(())
    }));
    assert!(matches!(
        std::future::poll_fn(|cx| Poll::Ready(blocker.as_mut().poll(cx))).await,
        Poll::Pending
    ));
    entered_rx.await.unwrap();
    let mut confirm = Box::pin(call(
        &core,
        "creator.merge",
        json!({"source_creator_id": source, "target_creator_id": target, "preview_token": preview["preview_token"]}),
    ));
    assert!(matches!(
        std::future::poll_fn(|cx| Poll::Ready(confirm.as_mut().poll(cx))).await,
        Poll::Pending
    ));
    let source_for_write = source.clone();
    let changed_account = surprise.clone();
    let mut change = Box::pin(core.db.write(move |tx| {
        tx.execute(
            "UPDATE account_bindings SET creator_id=?1 WHERE account_id=?2 AND state='active'",
            rusqlite::params![source_for_write, changed_account],
        )?;
        Ok(())
    }));
    assert!(matches!(
        std::future::poll_fn(|cx| Poll::Ready(change.as_mut().poll(cx))).await,
        Poll::Pending
    ));
    release_tx.send(()).unwrap();
    blocker.await.unwrap();
    change.await.unwrap();
    // Do not poll confirmation again until the queued binding mutation has committed.
    let result = confirm.await.unwrap();
    assert_eq!(result["counts"]["accounts_moved"], 1, "{result}");
    assert_eq!(result["accounts"], json!(["acc-shown"]), "{result}");
}

#[tokio::test]
async fn split_and_relink_tokens_are_revalidated_with_their_writes() {
    let (_dir, core) = core().await;
    let source = creator(&core, "source").await;
    let target = creator(&core, "target").await;
    let other = creator(&core, "other").await;
    let (first, _) = account_with_entry(&core, &source, "first").await;
    let (second, _) = account_with_entry(&core, &source, "second").await;
    let split = ok(
        &core,
        "creator.split_preview",
        json!({"source_creator_id": source, "account_ids": [first, second]}),
    )
    .await;
    relink_ok(&core, &first, &source, &target).await;
    let stale_split = call(
        &core,
        "creator.split",
        json!({
            "source_creator_id": source,
            "account_ids": [first, second],
            "display_name": "changed",
            "preview_token": split["preview_token"],
        }),
    )
    .await;
    assert!(matches!(
        stale_split,
        Err(crate::error::CoreError::VersionConflict(_))
    ));
    let split_operations = ok(&core, "creator.operations", json!({"creator_id": source})).await;
    assert!(
        split_operations["items"]
            .as_array()
            .unwrap()
            .iter()
            .all(|item| item["kind"] != "split")
    );

    let relink = ok(
        &core,
        "creator.relink_preview",
        json!({
            "account_id": second,
            "expected_creator_id": source,
            "target_creator_id": target,
        }),
    )
    .await;
    relink_ok(&core, &second, &source, &other).await;
    let stale_relink = call(
        &core,
        "creator.relink",
        json!({
            "account_id": second,
            "expected_creator_id": source,
            "target_creator_id": target,
            "preview_token": relink["preview_token"],
        }),
    )
    .await;
    assert!(matches!(
        stale_relink,
        Err(crate::error::CoreError::VersionConflict(_))
    ));
    let owner: String = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
                [second],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(owner, other);
}

#[tokio::test]
async fn identity_previews_show_scope_and_a_changed_binding_invalidates_the_confirmation() {
    let (_dir, core) = core().await;
    let source = creator(&core, "甲").await;
    let target = creator(&core, "乙").await;
    let (account, entry) = account_with_entry(&core, &source, "1").await;
    let preview = ok(
        &core,
        "creator.merge_preview",
        json!({"source_creator_id": source, "target_creator_id": target}),
    )
    .await;
    assert_eq!(preview["impact"]["source_scope"]["accounts"], 1);
    assert_eq!(preview["impact"]["source_scope"]["entries"], 1);
    assert_eq!(preview["impact"]["accounts"][0]["account_id"], account);
    assert_eq!(preview["impact"]["accounts"][0]["entries"], 1);
    let token = preview["preview_token"].as_str().unwrap().to_owned();
    // No write happened for the preview itself.
    let operations = ok(&core, "creator.operations", json!({})).await;
    assert_eq!(operations["items"].as_array().unwrap().len(), 0);
    // The account moves after the preview; the old confirmation is now stale.
    relink_ok(&core, &account, &source, &target).await;
    let stale = call(
        &core,
        "creator.merge",
        json!({"source_creator_id": source, "target_creator_id": target, "preview_token": token}),
    )
    .await;
    assert!(
        matches!(stale, Err(crate::error::CoreError::VersionConflict(_))),
        "{stale:?}"
    );
    let operations = ok(&core, "creator.operations", json!({})).await;
    assert!(
        operations["items"]
            .as_array()
            .unwrap()
            .iter()
            .all(|item| item["kind"] != "merge")
    );
    let timeline = ok(
        &core,
        "archive.timeline",
        json!({"creator_id": target, "limit": 10}),
    )
    .await;
    assert_eq!(timeline["items"][0]["entry_id"], entry);
}

#[tokio::test]
async fn timeline_follows_creator_redirect() {
    let (_dir, core) = core().await;
    let source = creator(&core, "源创作者").await;
    let target = creator(&core, "目标创作者").await;
    let (_account, entry) = account_with_entry(&core, &source, "redirect").await;
    merge_ok(&core, &source, &target).await;
    let page = ok(
        &core,
        "archive.timeline",
        json!({"creator_id": source, "limit": 50}),
    )
    .await;
    assert!(
        page["items"]
            .as_array()
            .unwrap()
            .iter()
            .any(|item| item["entry_id"] == json!(entry)),
        "the old creator id still finds the entry after the merge: {page}"
    );
}

async fn archive_fingerprint(core: &Arc<Core>) -> Value {
    core.db
        .read(|c| {
            let mut entries = c.prepare("SELECT entry_id,account_id,namespace,native_entry_key FROM entries ORDER BY entry_id")?;
            let rows = entries
                .query_map([], |r| {
                    Ok(json!([r.get::<_, String>(0)?, r.get::<_, String>(1)?, r.get::<_, String>(2)?, r.get::<_, String>(3)?]))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            let mut bindings = c.prepare("SELECT account_id,creator_id,state FROM account_bindings ORDER BY account_id")?;
            let binds = bindings
                .query_map([], |r| {
                    Ok(json!([r.get::<_, String>(0)?, r.get::<_, String>(1)?, r.get::<_, String>(2)?]))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(json!({"entries": rows, "bindings": binds}))
        })
        .await
        .unwrap()
}

#[tokio::test]
async fn creator_merge_keeps_archive_identity() {
    let (_dir, core) = core().await;
    let source = creator(&core, "甲").await;
    let target = creator(&core, "乙").await;
    let (account, entry) = account_with_entry(&core, &source, "1").await;
    let before = archive_fingerprint(&core).await;
    let result = merge_ok(&core, &source, &target).await;
    assert_eq!(result["merged_into"], target);
    assert_eq!(result["accounts"], json!([account]));
    let after = archive_fingerprint(&core).await;
    assert_eq!(
        before["entries"], after["entries"],
        "entries keep their account"
    );
    // The binding is the only relationship that changed.
    let bindings: Vec<Value> = after["bindings"].as_array().unwrap().clone();
    assert_eq!(bindings.len(), 1);
    assert_eq!(bindings[0][1], target);
    // The old id redirects and the timeline follows it.
    let old = ok(&core, "creator.get", json!({"creator_id": source})).await;
    assert_eq!(old["merged_into"], target);
    assert_eq!(old["accounts"][0]["account_id"], account);
    let timeline = ok(
        &core,
        "archive.timeline",
        json!({"creator_id": source, "limit": 10}),
    )
    .await;
    assert_eq!(timeline["items"][0]["entry_id"], entry);
    // The merged creator is not listed as an independent entry.
    let list = ok(&core, "creator.list", json!({"include_archived": true})).await;
    let ids: Vec<&str> = list["items"]
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|v| v["creator_id"].as_str())
        .collect();
    assert!(ids.contains(&target.as_str()));
    assert!(!ids.contains(&source.as_str()));
}

#[tokio::test]
async fn creator_merge_rejects_cycles() {
    let (_dir, core) = core().await;
    let a = creator(&core, "甲").await;
    let b = creator(&core, "乙").await;
    let c = creator(&core, "丙").await;
    assert!(
        merge_cmd(&core, &a, &a).await.is_err(),
        "a creator cannot merge into itself"
    );
    merge_ok(&core, &a, &b).await;
    let before = archive_fingerprint(&core).await;
    assert!(
        merge_cmd(&core, &b, &c).await.is_err(),
        "merging a redirect target would build a second redirect step"
    );
    assert_eq!(archive_fingerprint(&core).await, before);
    let old = ok(&core, "creator.get", json!({"creator_id": a})).await;
    assert_eq!(old["merged_into"], b);
    assert!(
        merge_cmd(&core, &b, &a).await.is_err(),
        "a merged creator is not an active target"
    );
    assert!(
        merge_cmd(&core, &c, &a).await.is_err(),
        "merging onto a merged creator would build a chain"
    );
    // One redirect step only: c may merge into the final target.
    merge_ok(&core, &c, &b).await;
}

#[tokio::test]
async fn creator_split_moves_selected_accounts_only() {
    let (_dir, core) = core().await;
    let source = creator(&core, "甲").await;
    let (first, _) = account_with_entry(&core, &source, "1").await;
    let (second, _) = account_with_entry(&core, &source, "2").await;
    let result = split_ok(&core, &source, std::slice::from_ref(&first), "拆分").await;
    let new_id = result["creator_id"].as_str().unwrap().to_owned();
    assert_ne!(new_id, source);
    let owner: String = core
        .db
        .read({
            let account = first.clone();
            move |c| {
                Ok(c.query_row(
                    "SELECT creator_id FROM account_bindings WHERE account_id=?1",
                    [account],
                    |r| r.get(0),
                )?)
            }
        })
        .await
        .unwrap();
    assert_eq!(owner, new_id);
    let kept: String = core
        .db
        .read({
            let account = second.clone();
            move |c| {
                Ok(c.query_row(
                    "SELECT creator_id FROM account_bindings WHERE account_id=?1",
                    [account],
                    |r| r.get(0),
                )?)
            }
        })
        .await
        .unwrap();
    assert_eq!(kept, source);
    // The split-out account keeps its subscription and entry.
    let entries: i64 = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT count(*) FROM entries WHERE account_id=?1",
                [first],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(entries, 1);
    assert!(
        split_cmd(&core, &source, &[], "空").await.is_err(),
        "an empty selection is refused"
    );
}

#[tokio::test]
async fn creator_relink_checks_expected_source() {
    let (_dir, core) = core().await;
    let a = creator(&core, "甲").await;
    let b = creator(&core, "乙").await;
    let (account, _) = account_with_entry(&core, &a, "1").await;
    assert!(
        relink_cmd(&core, &account, &b, &b).await.is_err(),
        "a stale expectation is a conflict, never a success"
    );
    relink_ok(&core, &account, &a, &b).await;
    let owner: String = core
        .db
        .read({
            let account = account.clone();
            move |c| {
                Ok(c.query_row(
                    "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
                    [account],
                    |r| r.get(0),
                )?)
            }
        })
        .await
        .unwrap();
    assert_eq!(owner, b);
}

#[tokio::test]
async fn creator_operations_are_paginated() {
    let (_dir, core) = core().await;
    let a = creator(&core, "甲").await;
    let b = creator(&core, "乙").await;
    let c = creator(&core, "丙").await;
    let (account, _) = account_with_entry(&core, &a, "1").await;
    relink_ok(&core, &account, &a, &b).await;
    merge_ok(&core, &b, &c).await;
    let page = ok(&core, "creator.operations", json!({"limit": 1})).await;
    assert_eq!(page["items"].as_array().unwrap().len(), 1);
    assert!(page["next_after"].is_string());
    let next = page["next_after"].as_str().unwrap().to_owned();
    let second = ok(
        &core,
        "creator.operations",
        json!({"limit": 5, "after": next}),
    )
    .await;
    assert_eq!(second["items"].as_array().unwrap().len(), 1);
    let filtered = ok(
        &core,
        "creator.operations",
        json!({"creator_id": c, "limit": 10}),
    )
    .await;
    assert_eq!(filtered["items"].as_array().unwrap().len(), 1);
    assert_eq!(filtered["items"][0]["kind"], "merge");
}

#[tokio::test]
async fn profile_history_keeps_observations() {
    let (_dir, core) = core().await;
    let creator = creator(&core, "甲").await;
    let (account, _) = account_with_entry(&core, &creator, "1").await;
    for (name, at) in [
        ("旧名", "2026-01-01T00:00:00Z"),
        ("新名", "2026-02-01T00:00:00Z"),
    ] {
        let account = account.clone();
        core.db
            .write(move |tx| {
                tx.execute(
                    "INSERT INTO account_observations(account_id,display_name,observed_at,evidence,profile_json) VALUES(?1,?2,?3,'{}','{\"display_name\":\"x\"}')",
                    rusqlite::params![account, name, at],
                )?;
                Ok(())
            })
            .await
            .unwrap();
    }
    let history = ok(
        &core,
        "account.profile_history",
        json!({"account_id": account, "limit": 1}),
    )
    .await;
    assert_eq!(history["items"].as_array().unwrap().len(), 1);
    assert_eq!(history["items"][0]["display_name"], "新名");
    assert_eq!(history["items"][0]["profile"]["display_name"], "x");
    let before = history["items"][0]["sequence"].as_i64().unwrap();
    let older = ok(
        &core,
        "account.profile_history",
        json!({"account_id": account, "before": before, "limit": 5}),
    )
    .await;
    assert_eq!(older["items"][0]["display_name"], "旧名");
}

#[tokio::test]
async fn similar_names_are_candidates_only() {
    let (_dir, core) = core().await;
    let a = creator(&core, "甲").await;
    let b = creator(&core, "乙").await;
    let (_, _) = account_with_entry(&core, &a, "1").await;
    // The other site account is named with a different case, width and spacing.
    let proposed = b.clone();
    core.db
        .write(move |tx| {
            tx.execute(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES('acc-2','weibo','2','名 字１','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO account_bindings(binding_id,creator_id,account_id,state,created_at) VALUES('bnd-2',?1,'acc-2','active','t')",
                [proposed],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    // The first account is named 名字1; observing it produces a candidate for creator b.
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE source_accounts SET display_name='名字1' WHERE account_id='acc-1'",
                [],
            )?;
            crate::identity::generate_candidates(tx, "acc-1", "weibo", Some("名字1"), "t")?;
            Ok(())
        })
        .await
        .unwrap();
    let candidates = ok(&core, "identity.candidates", json!({"state": "pending"})).await;
    let items = candidates["items"].as_array().unwrap();
    assert_eq!(
        items.len(),
        1,
        "full-width and case differences still match"
    );
    assert_eq!(items[0]["proposed_creator_id"], b);
    assert_eq!(items[0]["kind"], "same_name");
    assert_eq!(items[0]["score"], 1.0);
    // No binding changed before the user confirms.
    let owner: String = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT creator_id FROM account_bindings WHERE account_id='acc-1'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(owner, a, "a candidate never changes a binding on its own");
}

#[tokio::test]
async fn identity_confirm_is_explicit_and_conflict_safe() {
    let (_dir, core) = core().await;
    let a = creator(&core, "甲").await;
    let b = creator(&core, "乙").await;
    let (account, _) = account_with_entry(&core, &a, "1").await;
    let proposed = b.clone();
    core.db
        .write({
            let account = account.clone();
            move |tx| {
                tx.execute(
                    "INSERT INTO identity_candidates(candidate_id,account_id,proposed_creator_id,kind,score,evidence_json,state,created_at) VALUES('cand-1',?1,?2,'similar_name',0.75,'{}','pending','t')",
                    rusqlite::params![account, proposed],
                )?;
                Ok(())
            }
        })
        .await
        .unwrap();
    // The account is already bound to another creator: confirming is a conflict.
    assert!(
        call(&core, "identity.confirm", json!({"candidate_id": "cand-1"}))
            .await
            .is_err(),
        "an existing binding is never overwritten"
    );
    let still: String = core
        .db
        .read({
            let account = account.clone();
            move |c| {
                Ok(c.query_row(
                    "SELECT creator_id FROM account_bindings WHERE account_id=?1",
                    [account],
                    |r| r.get(0),
                )?)
            }
        })
        .await
        .unwrap();
    assert_eq!(still, a);
    // Confirming a candidate for an unbound account binds it explicitly.
    core.db
        .write(|tx| {
            tx.execute(
                "UPDATE account_bindings SET state='ended' WHERE account_id='acc-1'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let confirmed = ok(&core, "identity.confirm", json!({"candidate_id": "cand-1"})).await;
    assert_eq!(confirmed["state"], "confirmed");
    assert_eq!(confirmed["creator_id"], b);
    // Repeating the same confirmation is idempotent.
    let again = ok(&core, "identity.confirm", json!({"candidate_id": "cand-1"})).await;
    assert_eq!(again["changed"], false);
}

#[tokio::test]
async fn identity_reject_keeps_history() {
    let (_dir, core) = core().await;
    let a = creator(&core, "甲").await;
    let b = creator(&core, "乙").await;
    let (account, _) = account_with_entry(&core, &a, "1").await;
    let proposed = b.clone();
    core.db
        .write({
            let account = account.clone();
            move |tx| {
                tx.execute(
                    "INSERT INTO identity_candidates(candidate_id,account_id,proposed_creator_id,kind,score,evidence_json,state,created_at) VALUES('cand-1',?1,?2,'similar_name',0.75,'{}','pending','t')",
                    rusqlite::params![account, proposed],
                )?;
                Ok(())
            }
        })
        .await
        .unwrap();
    ok(&core, "identity.reject", json!({"candidate_id": "cand-1"})).await;
    let pending = ok(&core, "identity.candidates", json!({"state": "pending"})).await;
    assert!(pending["items"].as_array().unwrap().is_empty());
    let rejected = ok(&core, "identity.candidates", json!({"state": "rejected"})).await;
    assert_eq!(rejected["items"].as_array().unwrap().len(), 1);
    // The observation history is untouched by a rejection.
    let history = ok(
        &core,
        "account.profile_history",
        json!({"account_id": account}),
    )
    .await;
    assert!(!history["items"].as_array().unwrap().is_empty());
}

#[tokio::test]
async fn profile_evidence_has_no_credentials() {
    let evidence = json!({
        "bio": "简介",
        "avatar_url": "https://example.invalid/a.png",
        "cookie": "a1=secret; web_session=secret",
        "session_token": "secret",
        "x-s": "signed",
        "media_url": "https://cdn.invalid/short-lived?sign=1",
        "followers": 12,
    });
    let record =
        crate::identity::profile_record(&evidence, Some("名字"), Some("https://weibo.com/u/1"));
    assert_eq!(record["bio"], "简介");
    assert_eq!(record["followers"], 12);
    let text = record.to_string();
    assert!(
        !text.contains("secret"),
        "a credential never enters a profile record"
    );
    assert!(!text.contains("signed"));
    assert!(
        !text.contains("cdn.invalid"),
        "a short-lived address is dropped"
    );
}
