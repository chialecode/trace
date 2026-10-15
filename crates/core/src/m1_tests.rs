use crate::{config::Profile, core::Core, jobs};
use serde_json::json;

async fn empty_core() -> (tempfile::TempDir, std::sync::Arc<Core>) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "m1-regressions".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    (dir, core)
}

#[tokio::test]
async fn c08_cache_budget_clear_rebuild_preserves_sources_and_rejects_partial_search() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('cache-account','douyin','123','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,first_seen_at,account_id) VALUES('cache-entry','douyin','article','123','t','cache-account')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('cache-revision','cache-entry','d','中文正文','完整来源正文','t')",[])?;
        crate::objects::outbox_emit(tx,"entry.updated","cache-entry",&json!({}))?;Ok(())
    }).await.unwrap();
    crate::search::index_pending(&core.db, 100).await.unwrap();
    let before = crate::cache::status(&core.db).await.unwrap();
    assert!(before["index_estimated_bytes"].as_i64().unwrap() > 0);
    command(
        &core,
        "cache.control",
        json!({"scope":"search","rebuild":false}),
    )
    .await;
    assert_eq!(
        crate::search::index_pending(&core.db, 100).await.unwrap(),
        0
    );
    let entry = command(
        &core,
        "archive.entry.get",
        json!({"entry_id":"cache-entry"}),
    )
    .await;
    assert_eq!(entry["entry"]["body"], "完整来源正文");
    assert!(
        crate::feed::query(
            &core.db,
            serde_json::from_value(json!({"query":"正文"})).unwrap()
        )
        .await
        .is_err(),
        "cleared index cannot return a misleading empty search"
    );
    command(
        &core,
        "cache.control",
        json!({"scope":"search","rebuild":true}),
    )
    .await;
    assert!(
        crate::feed::query(
            &core.db,
            serde_json::from_value(json!({"query":"正文"})).unwrap()
        )
        .await
        .is_err(),
        "rebuilding index cannot present partial results as complete"
    );
    core.db
        .write(|tx| {
            tx.execute("UPDATE search_state SET budget_bytes=1", [])?;
            Ok(())
        })
        .await
        .unwrap();
    assert_eq!(
        crate::search::index_pending(&core.db, 100).await.unwrap(),
        0
    );
    assert_eq!(
        crate::cache::status(&core.db).await.unwrap()["index_budget_blocked"],
        true
    );
    command(
        &core,
        "cache.configure",
        json!({"index_budget_bytes":1048576,"derived_budget_bytes":0}),
    )
    .await;
    assert!(crate::search::index_pending(&core.db, 100).await.unwrap() > 0);
    crate::search::index_pending(&core.db, 100).await.unwrap();
    let page = command(
        &core,
        "archive.timeline",
        json!({"query":"douyin.com/note/123"}),
    )
    .await;
    assert_eq!(
        page["items"].as_array().unwrap().len(),
        1,
        "rebuild restores source URL lookup as well as body"
    );
    let state = crate::cache::status(&core.db).await.unwrap();
    assert_eq!(state["index_rebuilding"], false);
    assert_eq!(state["thumbnail_bytes"], 0);
    assert!(
        !core
            .db
            .read(|c| crate::cache::derived_allowed(c, 1))
            .await
            .unwrap()
    );
    command(
        &core,
        "cache.control",
        json!({"scope":"playback","rebuild":false}),
    )
    .await;
    assert!(
        !core
            .db
            .read(|c| crate::cache::derived_allowed(c, 0))
            .await
            .unwrap()
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c08_resource_inventory_pages_variants_missing_copies_and_origins() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{
        tx.execute("INSERT INTO creators(creator_id,display_name,created_at,updated_at) VALUES('c','跨站创作者','t','t')",[])?;
        tx.execute("INSERT INTO blobs(blob_id,size,mime,first_seen_at) VALUES('sha256:synthetic',10,'image/png','t')",[])?;
        tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state) VALUES('local','sha256:synthetic','local','primary','x',1,'verified')",[])?;
        for site in ["bilibili","douyin"] {
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?1,'stable-id','来源账号','t')",[site])?;
            tx.execute("INSERT INTO account_bindings(binding_id,account_id,creator_id,state,created_at) VALUES(?1,?1,'c','active','t')",[site])?;
        }
        for i in 0..205 {
            let id=format!("entry-{i}");let site=if i%2==0{"bilibili"}else{"douyin"};
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,lifecycle) VALUES(?1,?2,'image',?1,?2,'t',?3)",rusqlite::params![id,site,if i==204{"deleted"}else{"active"}])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?1,?1,'图集','正文','t')",[&id])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,?1,'image',1,'photo-1')",[&id])?;
            if i%2==0 {for variant in 0..2 {
                tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,measured_spec,created_at) VALUES(?1,?2,?3,'sha256:synthetic','{\"width\":100,\"height\":200}','2026-10-08T01:02:03Z')",rusqlite::params![format!("v-{i}-{variant}"),id,variant.to_string()])?;
            }}
        }
        Ok(())
    }).await.unwrap();
    let mut seen = std::collections::BTreeSet::new();
    let mut cursor = None;
    loop {
        let page = command(
            &core,
            "archive.resources",
            json!({"cursor":cursor,"creator_id":"c"}),
        )
        .await;
        assert!(page["items"].as_array().unwrap().len() <= 100);
        for item in page["items"].as_array().unwrap() {
            assert!(seen.insert(format!("{}/{}", item["media_id"], item["variant_id"])));
            assert_ne!(item["entry_id"], "entry-204");
            assert_eq!(item["creator_name"], "跨站创作者");
            assert_eq!(item["account_key"], "stable-id");
            if item["namespace"] == "bilibili" {
                assert_eq!(item["saved_at"], "2026-10-08T01:02:03Z");
                assert_eq!(item["verified_copies"], 1);
            } else {
                assert_eq!(item["available"], false);
                assert!(item["saved_at"].is_null());
            }
        }
        cursor = page["next_cursor"].as_str().map(str::to_owned);
        if cursor.is_none() {
            break;
        }
    }
    assert_eq!(
        seen.len(),
        306,
        "every variant and each missing resource, without join duplication"
    );
    let filtered = command(
        &core,
        "archive.resources",
        json!({"namespace":"douyin","available":true}),
    )
    .await;
    assert!(filtered["items"].as_array().unwrap().is_empty());
    let detail = command(&core, "archive.entry.get", json!({"entry_id":"entry-0"})).await;
    assert_eq!(detail["media"][0]["saved_at"], "2026-10-08T01:02:03Z");
    core.shutdown().await;
}

#[tokio::test]
async fn c05_immediate_global_resume_revokes_every_old_job_lease() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{
        for namespace in ["bilibili","douyin"]{
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES(?1,?1,'1','t')",[namespace])?;
            tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES(?1,?1,'video','{}','t','t')",[namespace])?;
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,?2,'video','1',?2,'t')",rusqlite::params![format!("{namespace}-entry"),namespace])?;
            for kind in ["scan","observe","media","record","account_profile"]{
                let subject=match kind{"scan"|"account_profile"=>namespace.to_owned(),_=>format!("{namespace}-entry")};
                let id=format!("{namespace}-{kind}");
                tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,attempts,created_at,updated_at) VALUES(?1,?1,?2,?3,?4,1,'queued',2,'t','t')",rusqlite::params![id,kind,subject,json!({"subscription_id":namespace}).to_string()])?;
            }
        }Ok(())
    }).await.unwrap();
    let old = jobs::claim_due(&core.db, "old", 20).await.unwrap();
    assert_eq!(old.len(), 10);
    for job in &old {
        job.mark_running(&core.db).await.unwrap();
    }
    command(
        &core,
        trace_contracts::control::commands::DAEMON_PAUSE,
        json!({"paused":true}),
    )
    .await;
    command(
        &core,
        trace_contracts::control::commands::DAEMON_PAUSE,
        json!({"paused":false}),
    )
    .await;
    for job in &old {
        assert!(job.heartbeat(&core.db).await.is_err());
        job.succeed(&core.db).await.unwrap();
    }
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row("SELECT count(*) FROM jobs WHERE state='queued'", [], |r| {
                    r.get::<_, i64>(0)
                })?,
                10,
                "a late completion changes no state"
            );
            Ok(())
        })
        .await
        .unwrap();
    let current = jobs::claim_due(&core.db, "new", 20).await.unwrap();
    assert_eq!(current.len(), 10);
    for job in &current {
        assert_eq!(job.generation, 2);
        job.mark_running(&core.db).await.unwrap();
    }
    command(
        &core,
        trace_contracts::control::commands::PLUGIN_SET_STATE,
        json!({"plugin_id":"bilibili","state":"disabled"}),
    )
    .await;
    for job in &current {
        let valid = job.heartbeat(&core.db).await.is_ok();
        assert_eq!(valid, job.job_id.starts_with("douyin"), "{}", job.job_type);
    }
    command(
        &core,
        trace_contracts::control::commands::PLUGIN_SET_STATE,
        json!({"plugin_id":"bilibili","state":"enabled"}),
    )
    .await;
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row("SELECT count(*) FROM jobs WHERE attempts=2", [], |r| r
                    .get::<_, i64>(0))?,
                10
            );
            Ok(())
        })
        .await
        .unwrap();
    let plugins = command(
        &core,
        trace_contracts::control::commands::PLUGIN_LIST,
        json!({}),
    )
    .await;
    let mut ids = plugins["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|v| v["plugin_id"].as_str().unwrap())
        .collect::<Vec<_>>();
    ids.sort();
    assert_eq!(
        ids,
        vec!["bilibili", "douyin", "fake"],
        "saved state cannot hide or duplicate a registered platform"
    );
    let renewed = jobs::claim_due(&core.db, "third", 20).await.unwrap();
    assert_eq!(renewed.len(), 5);
    for job in renewed {
        command(
            &core,
            trace_contracts::control::commands::JOB_CANCEL,
            json!({"job_id":job.job_id}),
        )
        .await;
        assert!(job.heartbeat(&core.db).await.is_err());
    }
    core.shutdown().await;
}

#[tokio::test]
async fn c01_recent_sweep_and_deep_cursor_survive_independently() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')",[])?;
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('sub','a','dynamic','{}','t','t')",[])?;
        for lane in ["incremental","deep"]{tx.execute("INSERT INTO scan_checkpoints(subscription_id,scan_type,cursor,coverage) VALUES('sub',?1,'old-cursor','{}')",[lane])?;crate::scan_range::record_cursor(tx,"sub",lane,Some("old-cursor"))?;}
        crate::scan_range::finish_recent_sweep(tx,"sub",Some("unvisited"),true,2)?;
        assert_eq!(tx.query_row("SELECT cursor FROM scan_checkpoints WHERE scan_type='incremental'",[],|r|r.get::<_,String>(0))?,"unvisited","page budget cannot discard the rest of the sweep");
        assert_eq!(tx.query_row("SELECT cursor FROM scan_checkpoints WHERE scan_type='deep'",[],|r|r.get::<_,String>(0))?,"old-cursor");
        assert_eq!(tx.query_row("SELECT json_extract(coverage,'$.history_complete') FROM scan_checkpoints WHERE scan_type='incremental'",[],|r|r.get::<_,i64>(0))?,0);
        assert!(crate::scan_range::record_cursor(tx,"sub","incremental",Some("old-cursor")).is_err());
        crate::scan_range::finish_recent_sweep(tx,"sub",None,false,1)?;
        assert!(tx.query_row("SELECT cursor IS NULL FROM scan_checkpoints WHERE scan_type='incremental'",[],|r|r.get::<_,bool>(0))?);
        crate::scan_range::record_cursor(tx,"sub","incremental",Some("old-cursor"))?;
        assert!(crate::scan_range::record_cursor(tx,"sub","deep",Some("old-cursor")).is_err());
        Ok(())
    }).await.unwrap();
    crate::scan_range::ensure_deep_lanes(&core.db)
        .await
        .unwrap();
    core.db
        .write(|tx| {
            tx.execute(
                "UPDATE jobs SET state='cancelled' WHERE job_key='scan:sub:deep'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    crate::scan_range::ensure_deep_lanes(&core.db)
        .await
        .unwrap();
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row("SELECT count(*) FROM jobs", [], |r| r.get::<_, i64>(0))?,
                1
            );
            assert_eq!(
                c.query_row("SELECT state FROM jobs", [], |r| r.get::<_, String>(0))?,
                "cancelled"
            );
            Ok(())
        })
        .await
        .unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c01_identity_alias_repost_type_collision_and_persistent_cursor_cycle() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{
        let resolved=|key:&str,name:&str|trace_contracts::plugin::ResolvedAccount{native_account_key:key.into(),display_name:Some(name.into()),profile_url:Some("https://attacker.invalid/?token=sentinel".into()),evidence:json!({"uid":"90071992547409931","source":"synthetic_profile","cookie":"sentinel"})};
        let first=crate::accounts::persist(tx,"douyin",resolved("MS4fixture","旧昵称"))?;
        let next=crate::accounts::persist(tx,"douyin",resolved("MS4fixture","新昵称"))?;
        assert_eq!(first["account_id"],next["account_id"]);assert!(next["profile_url"].as_str().unwrap().starts_with("https://www.douyin.com/user/"));
        let bili=crate::accounts::persist(tx,"bilibili",resolved("90071992547409931","同名"))?;assert_ne!(bili["account_id"],first["account_id"],"a cross-platform alias is not shared identity");
        assert_eq!(tx.query_row("SELECT count(*) FROM account_observations",[],|r|r.get::<_,i64>(0))?,3);
        let history=tx.prepare("SELECT display_name,evidence FROM account_observations ORDER BY sequence")?.query_map([],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?)))?.collect::<Result<Vec<_>,_>>()?;
        assert_eq!(history[0].0,"旧昵称");assert_eq!(history[1].0,"新昵称");assert!(history.iter().all(|(_,e)|!e.contains("sentinel")));
        let account=first["account_id"].as_str().unwrap();
        for kind in ["video","article","image","dynamic"]{tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,'douyin',?1,'55',?2,'t')",rusqlite::params![kind,account])?;}
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('original','douyin','dynamic','56',?1,'t')",[account])?;
        for (id,body,evidence) in [("dynamic","转发者评论",json!({"original_native_key":"56"})),("original","原作者正文",json!({}))]{tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,metadata_json) VALUES(?1,?1,?1,?1,?2,'t',?3)",rusqlite::params![id,body,evidence.to_string()])?;}
        tx.execute("UPDATE entries SET lifecycle='unavailable' WHERE entry_id='original'",[])?;
        assert_eq!(tx.query_row("SELECT normalized_body FROM entry_revisions WHERE entry_id='dynamic'",[],|r|r.get::<_,String>(0))?,"转发者评论");
        assert_eq!(tx.query_row("SELECT count(*) FROM entries WHERE native_entry_key='55'",[],|r|r.get::<_,i64>(0))?,4);
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('alias-sub',?1,'dynamic','{}','t','t')",[account])?;
        crate::scan_range::record_cursor(tx,"alias-sub","history",Some("older-page"))?;Ok(())
    }).await.unwrap();
    assert!(
        core.db
            .write(|tx| crate::scan_range::record_cursor(
                tx,
                "alias-sub",
                "history",
                Some("older-page")
            ))
            .await
            .is_err(),
        "cursor cycle persists outside the original transaction"
    );
    core.shutdown().await;
}
async fn command(
    core: &std::sync::Arc<Core>,
    name: &str,
    args: serde_json::Value,
) -> serde_json::Value {
    let result = crate::ipc::dispatch::dispatch(
        core,
        trace_contracts::control::Request {
            protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
            request_id: uuid::Uuid::new_v4().to_string(),
            command: name.into(),
            idempotency_key: None,
            if_revision: None,
            args,
        },
    )
    .await;
    assert!(result.ok, "{name}: {:?}", result.error);
    result.result.unwrap()
}

#[tokio::test]
async fn c01_c08_public_search_pages_full_scope_and_creator_edits_keep_identity() {
    let (_dir, core) = empty_core().await;
    let creator = command(
        &core,
        "creator.create",
        json!({"display_name":"原名称","aliases":[],"tags":[]}),
    )
    .await;
    let creator_id = creator["creator_id"].as_str().unwrap().to_owned();
    let account = command(
        &core,
        "account.bind",
        json!({"creator_id":creator_id,"namespace":"bilibili","native_account_key":"77"}),
    )
    .await;
    let account_id = account["account_id"].as_str().unwrap().to_owned();
    command(
        &core,
        "subscription.create",
        json!({"account_id":account_id,"content_type":"article","range":{"kind":"all"}}),
    )
    .await;
    let a = account_id.clone();
    core.db.write(move |tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('outsider','douyin','88','t')",[])?;
        // More than the old 2000-candidate cap precede the three wanted entries.
        for i in 0..2110 {
            let id=format!("search-{i:04}");
            let wanted=i>=2105;
            let account=if wanted {a.as_str()} else {"outsider"};
            let namespace=if wanted {"bilibili"} else {"douyin"};
            let kind=if i==2108 {"video"} else {"article"};
            let published=if i==2109 {"2025-12-31T23:59:59Z"} else {"2026-01-15T10:00:00Z"};
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,published_at,first_seen_at) VALUES(?1,?2,?3,?1,?4,?5,'t')",rusqlite::params![id,namespace,kind,account,published])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES(?1,?1,?1,'分页正文','共同归档正文','full','t')",[&id])?;
            tx.execute("INSERT INTO search_documents(doc_kind,ref_id,entry_id,namespace,normalized_text,index_version) VALUES('entry',?1,?1,?2,'共同归档正文',1)",rusqlite::params![id,namespace])?;
            let doc_id=tx.last_insert_rowid();
            tx.execute("INSERT INTO search_unigram(gram,doc_id) VALUES('共',?1)",[doc_id])?;
            tx.execute("INSERT INTO search_bigram(gram,doc_id) VALUES('共同',?1)",[doc_id])?;
        }
        Ok(())
    }).await.unwrap();
    let scope = json!({"query":"共","creator_id":creator_id,"namespace":"bilibili","kind":"article","from":"2026-01-01T00:00:00Z","to":"2026-02-01T00:00:00Z","limit":1});
    for query in ["共", "共同", "共同归档"] {
        let mut args = scope.clone();
        args["query"] = json!(query);
        let mut seen = Vec::new();
        let mut first_cursor = None;
        for _ in 0..5 {
            let page = command(&core, "search.query", args.clone()).await;
            assert_eq!(page["items"].as_array().unwrap().len(), 1);
            let item = &page["items"][0];
            assert_eq!(item["namespace"], "bilibili");
            assert_eq!(item["creator_id"], creator_id);
            assert!(item["snippet"].as_str().unwrap().contains(query));
            assert_eq!(item["revision_id"], item["entry_id"]);
            seen.push(item["entry_id"].as_str().unwrap().to_owned());
            if page["next_cursor"].is_null() {
                break;
            }
            first_cursor.get_or_insert(page["next_cursor"].clone());
            args["cursor"] = page["next_cursor"].clone();
        }
        assert_eq!(seen, ["search-2107", "search-2106", "search-2105"]);
        args["cursor"] = first_cursor.unwrap();
        args["kind"] = json!("video");
        let bad = crate::ipc::dispatch::dispatch(
            &core,
            trace_contracts::control::Request {
                protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
                request_id: "changed-search-scope".into(),
                command: "search.query".into(),
                idempotency_key: None,
                if_revision: None,
                args,
            },
        )
        .await;
        assert!(
            !bad.ok,
            "a cursor cannot be replayed against another filter"
        );
    }
    let edited=command(&core,"creator.update",json!({"creator_id":creator_id,"display_name":"新名称","aliases":["查询旧名"],"tags":["摄影标签"],"notes":"本地备注","status":"archived"})).await;
    assert_eq!(edited["creator_id"], creator_id);
    assert_eq!(edited["status"], "archived");
    assert_eq!(edited["aliases"], json!(["查询旧名"]));
    assert_eq!(edited["tags"], json!(["摄影标签"]));
    assert_eq!(edited["notes"], "本地备注");
    assert!(
        command(&core, "creator.list", json!({})).await["items"]
            .as_array()
            .unwrap()
            .is_empty()
    );
    assert_eq!(
        command(&core, "creator.list", json!({"include_archived":true})).await["items"][0]["creator_id"],
        creator_id
    );
    for query in ["新名称", "查询旧名", "摄影标签"] {
        let mut args = scope.clone();
        args["query"] = json!(query);
        args["limit"] = json!(10);
        assert_eq!(
            command(&core, "search.query", args).await["items"]
                .as_array()
                .unwrap()
                .len(),
            3
        );
    }
    command(
        &core,
        "creator.update",
        json!({"creator_id":creator_id,"status":"active"}),
    )
    .await;
    assert_eq!(
        command(&core, "creator.list", json!({})).await["items"][0]["creator_id"],
        creator_id
    );
    core.db
        .read(move |c| {
            assert_eq!(
                c.query_row(
                    "SELECT creator_id FROM account_bindings WHERE account_id=?1",
                    [&account_id],
                    |r| r.get::<_, String>(0)
                )?,
                creator_id
            );
            assert_eq!(
                c.query_row(
                    "SELECT state FROM subscriptions WHERE account_id=?1",
                    [account_id],
                    |r| r.get::<_, String>(0)
                )?,
                "enabled",
                "archiving a creator never pauses its subscription"
            );
            assert_eq!(
                c.query_row("SELECT count(*) FROM entries", [], |r| r.get::<_, i64>(0))?,
                2110
            );
            Ok(())
        })
        .await
        .unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c08_two_site_all_types_filters_pagination_current_revisions_and_ipc() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{
        tx.execute("INSERT INTO creators(creator_id,display_name,created_at,updated_at) VALUES('creator','中文共同创作者','t','t')",[])?;
        for ns in ["bilibili","douyin"]{
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?1,'90071992547409931','旧昵称','t')",[ns])?;
            tx.execute("INSERT INTO account_bindings(binding_id,account_id,creator_id,state,created_at) VALUES(?1,?1,'creator','active','t')",[ns])?;
            for kind in ["video","dynamic","image","article","live"]{
                tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,?2,?3,'123',?2,'t','2026-10-01T00:00:00Z')",rusqlite::params![format!("{ns}-{kind}"),ns,kind])?;
            }
        }Ok(())
    }).await.unwrap();
    jobs::enqueue(
        &core.db,
        "details".into(),
        "observe",
        "bilibili-article",
        json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    for ns in ["bilibili", "douyin"] {
        for kind in ["video", "dynamic", "image", "article", "live"] {
            for body in ["甲正文", "乙正文", "甲正文"] {
                let detail=serde_json::from_value(json!({"native_entry_key":"123","native_kind":kind,"title":"中文标题","normalized_body":body,"published_precision":"unknown","access_state":"available","completeness":"full","source_version_evidence":{},"resources":[],"stats":{}})).unwrap();
                crate::core::persist_detail(&core, &job, &format!("{ns}-{kind}"), ns, detail)
                    .await
                    .unwrap();
            }
        }
    }
    crate::search::index_pending(&core.db, 100).await.unwrap();
    let mut cursor = None;
    let mut ids = std::collections::BTreeSet::new();
    loop {
        let page = command(
            &core,
            trace_contracts::control::commands::ARCHIVE_TIMELINE,
            json!({"creator_id":"creator","query":"甲正","limit":3,"cursor":cursor}),
        )
        .await;
        assert_eq!(page["counts"]["filtered"], 10);
        assert_eq!(page["counts"]["global"], 10);
        for entry in page["items"].as_array().unwrap() {
            assert!(ids.insert(entry["entry_id"].as_str().unwrap().to_owned()));
        }
        cursor = page["next_cursor"].as_str().map(str::to_owned);
        if cursor.is_none() {
            break;
        }
    }
    assert_eq!(ids.len(), 10);
    for ns in ["bilibili", "douyin"] {
        for kind in ["video", "dynamic", "image", "article", "live"] {
            let page = command(
                &core,
                trace_contracts::control::commands::ARCHIVE_TIMELINE,
                json!({"namespace":ns,"kind":kind,"query":"甲"}),
            )
            .await;
            assert_eq!(page["counts"]["filtered"], 1);
            let id = format!("{ns}-{kind}");
            let detail = command(&core, "archive.entry.get", json!({"entry_id":id})).await;
            let current = &detail["entry"]["current_revision_id"];
            let rev = detail["revisions"]
                .as_array()
                .unwrap()
                .iter()
                .find(|r| &r["revision_id"] == current)
                .unwrap();
            assert_eq!(rev["body"], "甲正文");
            assert_eq!(detail["entry"]["body"], "甲正文");
            assert_eq!(detail["revisions"].as_array().unwrap().len(), 2);
            let manifest = core
                .db
                .read(move |c| crate::storage_ops::generate_manifest(c, &id))
                .await
                .unwrap()
                .unwrap();
            assert!(manifest.to_string().contains("甲正文"));
            assert!(!manifest.to_string().contains("乙正文"));
        }
    }
    let page = command(
        &core,
        trace_contracts::control::commands::ARCHIVE_TIMELINE,
        json!({"query":"乙正文"}),
    )
    .await;
    assert_eq!(page["counts"]["filtered"], 0);
    core.shutdown().await;
}

#[tokio::test]
async fn c08_large_revision_history_and_media_have_lossless_bounded_pages() {
    let (_dir, core) = empty_core().await;
    let body = "完整正文😀\n\"".repeat(24000);
    let document=json!({"version":1,"blocks":[{"kind":"text","style":"paragraph","text":body}],"omissions":[]}).to_string();
    let expected_body = body.clone();
    let expected_document = document.clone();
    core.db.write(move |tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('large-a','douyin','1','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,first_seen_at,account_id) VALUES('large','douyin','article','123','t','large-a')",[])?;
        for n in 0..130 {
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at,document_json) VALUES(?1,'large',?1,'全文',?2,'full','t',?3)",rusqlite::params![format!("r{n}"),if n==0{&body}else{"旧正文"},if n==0{document.as_str()}else{"{}"}])?;
        }
        for n in 0..225 {tx.execute("INSERT INTO source_observations(observation_id,observation_seq,entry_id,access_state,observed_at,revision_id,evidence) VALUES(?1,?2,'large','available','t',?3,'{}')",rusqlite::params![format!("o{n}"),n,if n==224{"r0"}else{"r129"}])?;}
        for n in 0..260 {tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,'r0','image',?2,?1)",rusqlite::params![format!("m{n}"),n])?;}
        Ok(())
    }).await.unwrap();
    let initial = command(&core, "archive.entry.get", json!({"entry_id":"large"})).await;
    assert!(trace_contracts::wire::encode_frame(&initial).is_ok());
    assert_eq!(initial["entry"]["current_revision_id"], "r0");
    assert_eq!(
        initial["revisions"]
            .as_array()
            .unwrap()
            .iter()
            .find(|r| r["revision_id"] == "r0")
            .unwrap()["content_inline"],
        false
    );
    for (section, count, key) in [
        ("revisions", 130, "revision_id"),
        ("observations", 225, "seq"),
        ("media", 260, "media_id"),
    ] {
        let mut cursor = None;
        let mut seen = std::collections::BTreeSet::new();
        loop {
            let page = command(
                &core,
                "archive.entry.page",
                json!({"entry_id":"large","section":section,"revision_id":"r0","cursor":cursor}),
            )
            .await;
            assert!(trace_contracts::wire::encode_frame(&page).is_ok());
            for item in page["items"].as_array().unwrap() {
                assert!(seen.insert(item[key].to_string()));
            }
            cursor = page["next_cursor"].as_str().map(str::to_owned);
            if cursor.is_none() {
                break;
            }
        }
        assert_eq!(seen.len(), count);
    }
    for (field, expected) in [("body", expected_body), ("document", expected_document)] {
        let mut assembled = String::new();
        let mut offset = 0;
        loop {
            let part = command(
                &core,
                "archive.revision.read",
                json!({"entry_id":"large","revision_id":"r0","field":field,"offset":offset}),
            )
            .await;
            assert!(trace_contracts::wire::encode_frame(&part).is_ok());
            assembled.push_str(part["text"].as_str().unwrap());
            let Some(next) = part["next_offset"].as_u64() else {
                break;
            };
            assert!(next > offset);
            offset = next;
        }
        assert_eq!(assembled, expected);
    }
    assert!(
        crate::ipc::archive::read(
            &core.db,
            crate::ipc::archive::ReadArgs {
                entry_id: "other".into(),
                revision_id: "r0".into(),
                field: "body".into(),
                offset: 0
            }
        )
        .await
        .is_err()
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c01_cursor_cycle_survives_turns_and_range_change_revokes_scan() {
    let (_dir, core) = empty_core().await;
    core.db.write(|tx|{tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')",[])?;Ok(())}).await.unwrap();
    let result = command(
        &core,
        "subscription.create",
        json!({"account_id":"a","content_type":"video","range":{"kind":"all"}}),
    )
    .await;
    let sub = result["subscription_id"].as_str().unwrap().to_owned();
    for next in ["1", "2"] {
        let sub = sub.clone();
        core.db
            .write(move |tx| crate::scan_range::record_cursor(tx, &sub, "history", Some(next)))
            .await
            .unwrap();
    }
    let sub2 = sub.clone();
    assert!(
        core.db
            .write(move |tx| crate::scan_range::record_cursor(tx, &sub2, "history", Some("1")))
            .await
            .is_err()
    );
    let sub2 = sub.clone();
    core.db
        .write(move |tx| crate::scan_range::record_cursor(tx, &sub2, "incremental", Some("1")))
        .await
        .unwrap();
    let job = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    command(
        &core,
        "subscription.update",
        json!({"subscription_id":sub,"range":{"kind":"recent_days","days":7}}),
    )
    .await;
    assert!(job.heartbeat(&core.db).await.is_err());
    let count: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM scan_cursor_visits", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(count, 0);
    core.shutdown().await;
}

#[tokio::test]
async fn c02_summary_promotion_stats_and_atomic_resource_jobs() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "m1-details".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx|{tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','stable','now')",[])?;tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','douyin','article','90071992547409931','a','now')",[])?;Ok(())}).await.unwrap();
    jobs::enqueue(
        &core.db,
        "observe:e".into(),
        "observe",
        "e",
        json!({"entry_id":"e"}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    let mut detail:trace_contracts::plugin::ObservedDetail=serde_json::from_value(json!({"native_entry_key":"90071992547409931","native_kind":"article","title":"正文","normalized_body":"同一正文","published_at":null,"published_precision":"unknown","access_state":"available","source_version_evidence":{},"resources":[],"completeness":"summary","stats":{"likes":1}})).unwrap();
    crate::core::persist_detail(&core, &job, "e", "a", detail.clone())
        .await
        .unwrap();
    detail.completeness = "full".into();
    detail.document = trace_contracts::content::Document {
        version: 1,
        blocks: vec![trace_contracts::content::Block::Text {
            text: "同一正文".into(),
            style: "paragraph".into(),
        }],
        omissions: vec![],
    };
    crate::core::persist_detail(&core, &job, "e", "a", detail.clone())
        .await
        .unwrap();
    detail.stats = json!({"likes":99});
    crate::core::persist_detail(&core, &job, "e", "a", detail.clone())
        .await
        .unwrap();
    let facts:(i64,String,i64,String)=core.db.read(|c|Ok((c.query_row("SELECT count(*) FROM entry_revisions",[],|r|r.get(0))?,c.query_row("SELECT completeness FROM entry_revisions",[],|r|r.get(0))?,c.query_row("SELECT count(*) FROM source_observations",[],|r|r.get(0))?,c.query_row("SELECT evidence FROM source_observations ORDER BY observation_seq DESC LIMIT 1",[],|r|r.get(0))?))).await.unwrap();
    assert_eq!((facts.0, facts.1.as_str(), facts.2), (1, "full", 3));
    assert_eq!(
        serde_json::from_str::<serde_json::Value>(&facts.3).unwrap()["stats"]["likes"],
        99
    );
    detail
        .resources
        .push(trace_contracts::plugin::ResourceIdentityDto {
            role: "image".into(),
            source_attachment_id: "i1".into(),
            position: 0,
            title: None,
            group_key: None,
            required: true,
        });
    crate::core::persist_detail(&core, &job, "e", "a", detail)
        .await
        .unwrap();
    let (revs, media, queued): (i64, i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM entry_revisions", [], |r| r.get(0))?,
                c.query_row("SELECT count(*) FROM media_items", [], |r| r.get(0))?,
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='media'",
                    [],
                    |r| r.get(0),
                )?,
            ))
        })
        .await
        .unwrap();
    assert_eq!((revs, media, queued), (2, 1, 1));
    core.shutdown().await;
}

#[test]
fn c07_schema3_backup_migration_and_failed_migration_rollback() {
    use crate::db::schema::*;
    for fail in [false, true] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("old.db");
        let mut c = rusqlite::Connection::open(&path).unwrap();
        c.execute_batch(MIGRATION_001).unwrap();
        c.execute_batch(MIGRATION_002_TRIGGERS).unwrap();
        c.execute_batch("ALTER TABLE media_items ADD COLUMN acquisition_evidence TEXT; INSERT INTO meta VALUES('schema_version','3'); INSERT INTO creators(creator_id,display_name,created_at,updated_at) VALUES('keep','旧档案','then','then');").unwrap();
        if fail {
            c.execute_batch("ALTER TABLE entry_revisions ADD COLUMN metadata_json TEXT")
                .unwrap();
        }
        let result = run_migrations(&mut c);
        assert_eq!(result.is_err(), fail);
        let version: String = c
            .query_row(
                "SELECT value FROM meta WHERE key='schema_version'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(
            version,
            if fail {
                "3".to_owned()
            } else {
                trace_domain::SCHEMA_VERSION.to_string()
            }
        );
        let snapshots = std::fs::read_dir(dir.path())
            .unwrap()
            .filter_map(Result::ok)
            .filter(|e| e.file_name().to_string_lossy().contains("pre-schema-4"))
            .collect::<Vec<_>>();
        assert_eq!(snapshots.len(), 1);
        let b = rusqlite::Connection::open(snapshots[0].path()).unwrap();
        let title: String = b
            .query_row(
                "SELECT display_name FROM creators WHERE creator_id='keep'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(title, "旧档案");
        let backup_version: String = b
            .query_row(
                "SELECT value FROM meta WHERE key='schema_version'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(backup_version, "3");
        if fail {
            let has_document:bool=c.query_row("SELECT EXISTS(SELECT 1 FROM pragma_table_info('entry_revisions') WHERE name='document_json')",[],|r|r.get(0)).unwrap();
            assert!(!has_document, "whole migration transaction must roll back");
        }
    }
}

#[tokio::test]
async fn c07_schema3_source_search_rebuild_preserves_old_body_and_snapshot() {
    use crate::db::schema::*;
    let dir = tempfile::tempdir().unwrap();
    let profile = Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    };
    std::fs::create_dir_all(profile.db_path().parent().unwrap()).unwrap();
    {
        let c = rusqlite::Connection::open(profile.db_path()).unwrap();
        c.execute_batch(MIGRATION_001).unwrap();
        c.execute_batch(MIGRATION_002_TRIGGERS).unwrap();
        c.execute_batch("ALTER TABLE media_items ADD COLUMN acquisition_evidence TEXT; INSERT INTO meta VALUES('schema_version','3'); INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('old-account','bilibili','42','t'); INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('old-entry','bilibili','video','BV1fixture','old-account','t'); INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES('old-rev','old-entry','digest','旧库标题','迁移正文保留','full','t'); INSERT INTO search_documents(doc_kind,ref_id,entry_id,normalized_text,index_version) VALUES('entry','old-entry','old-entry','旧库标题 迁移正文保留',1);").unwrap();
    }
    let core = Core::open(profile.clone()).await.unwrap();
    assert!(crate::search::index_pending(&core.db, 100).await.unwrap() > 0);
    crate::search::index_pending(&core.db, 100).await.unwrap();
    for query in ["迁", "迁移", "迁移正文", "bilibili.com/video/BV1fixture"] {
        let page = command(&core, "archive.timeline", json!({"query":query})).await;
        assert_eq!(
            page["items"].as_array().unwrap().len(),
            1,
            "old schema source query: {query}"
        );
        assert_eq!(page["items"][0]["entry_id"], "old-entry");
    }
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT normalized_body FROM entry_revisions WHERE revision_id='old-rev'",
                    [],
                    |r| r.get::<_, String>(0)
                )?,
                "迁移正文保留"
            );
            Ok(())
        })
        .await
        .unwrap();
    let snapshots = std::fs::read_dir(profile.db_path().parent().unwrap())
        .unwrap()
        .filter_map(Result::ok)
        .filter(|e| e.file_name().to_string_lossy().contains("pre-schema-4"))
        .collect::<Vec<_>>();
    assert_eq!(snapshots.len(), 1);
    let old = rusqlite::Connection::open(snapshots[0].path()).unwrap();
    assert_eq!(
        old.query_row(
            "SELECT value FROM meta WHERE key='schema_version'",
            [],
            |r| r.get::<_, String>(0)
        )
        .unwrap(),
        "3"
    );
    assert_eq!(
        old.query_row("SELECT count(*) FROM outbox", [], |r| r.get::<_, i64>(0))
            .unwrap(),
        0,
        "migration must generate rebuild work even when old outbox was compacted"
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c07_deletion_scopes_stale_preview_shared_bytes_and_explicit_restore() {
    use sha2::Digest;
    let _dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: _dir.path().join("data"),
        archive_root: _dir.path().join("archive"),
        vault_dir: Some(_dir.path().join("vault")),
        manual_delete_wait_seconds: 0,
        ..Default::default()
    })
    .await
    .unwrap();
    assert_eq!(Profile::default().manual_delete_wait_seconds, 7 * 86400);
    let bytes = b"shared archive bytes";
    let sha = sha2::Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>();
    let blob = format!("sha256:{sha}");
    let path = core.objects.object_path(&sha).unwrap();
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(&path, bytes).unwrap();
    let b = blob.clone();
    core.db.write(move|tx|{
        tx.execute("UPDATE meta SET value='{\"prefer_fps\":false,\"wait_seconds\":0}' WHERE key='quality_policy'",[])?;
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','id','t')",[])?;
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','video','{}','t','t')",[])?;
        tx.execute("INSERT INTO blobs(blob_id,size,first_seen_at) VALUES(?1,20,'t')",[&b])?;
        tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state) VALUES('local',?1,'local','primary','x',1,'verified')",[&b])?;
        for e in ["first","second"]{
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,'douyin','video',?1,'a','t')",[e])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?1,?1,'删除测试','保留正文','t')",[e])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,?1,'video',0,'original')",[e])?;
            tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,created_at) VALUES(?1,?1,'original',?2,'t')",rusqlite::params![e,b])?;
            tx.execute("INSERT INTO asset_selections(media_id,variant_id,policy_version,updated_at) VALUES(?1,?1,'v1','t')",[e])?;
        }
        Ok(())
    }).await.unwrap();
    jobs::enqueue(
        &core.db,
        "deletion-media".into(),
        "media",
        "first",
        json!({"entry_id":"first","subscription_id":"s"}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let old = jobs::claim_due(&core.db, "deletion", 1)
        .await
        .unwrap()
        .remove(0);
    old.mark_running(&core.db).await.unwrap();
    let preview = crate::deletion::preview(&core, "first".into(), "archive".into())
        .await
        .unwrap();
    core.db.write(|tx|{tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('later','first','later','编辑','新正文','u')",[])?;Ok(())}).await.unwrap();
    assert!(
        crate::deletion::apply(
            &core,
            "first".into(),
            "archive".into(),
            preview["token"].as_str().unwrap().into()
        )
        .await
        .is_err()
    );
    let preview = crate::deletion::preview(&core, "first".into(), "archive".into())
        .await
        .unwrap();
    let removed = crate::deletion::apply(
        &core,
        "first".into(),
        "archive".into(),
        preview["token"].as_str().unwrap().into(),
    )
    .await
    .unwrap();
    assert!(old.heartbeat(&core.db).await.is_err());
    let p = crate::feed::query(&core.db, serde_json::from_value(json!({})).unwrap())
        .await
        .unwrap();
    assert_eq!(p["counts"]["global"], 1);
    assert!(
        crate::ipc::archive::entry(&core.db, "first".into())
            .await
            .is_err()
    );
    crate::quality::recycle(&core).await.unwrap();
    crate::quality::recycle(&core).await.unwrap();
    assert!(path.exists(), "second source still retains shared bytes");
    assert!(
        crate::deletion::restore(&core, "first".into(), 0)
            .await
            .is_err()
    );
    crate::deletion::restore(
        &core,
        "first".into(),
        removed["generation"].as_i64().unwrap(),
    )
    .await
    .unwrap();
    let restored = crate::ipc::archive::entry(&core.db, "first".into())
        .await
        .unwrap();
    assert!(!restored.is_null());
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT state FROM jobs WHERE job_key='deletion-media'",
                    [],
                    |r| r.get::<_, String>(0)
                )?,
                "cancelled",
                "restoring metadata never silently restarts a cancelled acquisition"
            );
            assert_eq!(
                c.query_row("SELECT count(*) FROM deletion_records", [], |r| r
                    .get::<_, i64>(0))?,
                2
            );
            Ok(())
        })
        .await
        .unwrap();
    // Copy deletion retains metadata; cache deletion cannot touch originals.
    let cache = crate::deletion::preview(&core, "first".into(), "cache".into())
        .await
        .unwrap();
    assert_eq!(cache["variants"], 0);
    crate::deletion::apply(
        &core,
        "first".into(),
        "cache".into(),
        cache["token"].as_str().unwrap().into(),
    )
    .await
    .unwrap();
    assert!(path.exists());
    let external = _dir.path().join("unmanaged-original.bin");
    std::fs::write(&external, bytes).unwrap();
    let external_locator = external.to_string_lossy().to_string();
    let shared_blob = blob.clone();
    core.db.write(move|tx|{crate::quality::pin(tx,"first",true)?;tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state) VALUES('external',?1,'local','external',?2,0,'verified')",rusqlite::params![shared_blob,external_locator])?;Ok(())}).await.unwrap();
    let lease = command(&core, "media.lease", json!({"blob_id":blob})).await;
    for e in ["first", "second"] {
        let p = crate::deletion::preview(&core, e.into(), "copies".into())
            .await
            .unwrap();
        crate::deletion::apply(
            &core,
            e.into(),
            "copies".into(),
            p["token"].as_str().unwrap().into(),
        )
        .await
        .unwrap();
    }
    crate::quality::recycle(&core).await.unwrap();
    assert!(
        path.exists(),
        "active reader and explicit pin survive copy deletion"
    );
    let chunk = command(
        &core,
        "media.read_range",
        json!({"lease_id":lease["lease_id"],"offset":0,"length":20}),
    )
    .await;
    use base64::Engine;
    assert_eq!(
        base64::engine::general_purpose::STANDARD
            .decode(chunk["data_b64"].as_str().unwrap())
            .unwrap(),
        bytes
    );
    command(
        &core,
        "media.release",
        json!({"lease_id":lease["lease_id"]}),
    )
    .await;
    crate::quality::recycle(&core).await.unwrap();
    assert!(path.exists(), "pin is independent of reader lease");
    core.db
        .write(|tx| crate::quality::pin(tx, "first", false))
        .await
        .unwrap();
    let preview = crate::deletion::preview(&core, "first".into(), "copies".into())
        .await
        .unwrap();
    let removed = crate::deletion::apply(
        &core,
        "first".into(),
        "copies".into(),
        preview["token"].as_str().unwrap().into(),
    )
    .await
    .unwrap();
    crate::quality::recycle(&core).await.unwrap();
    assert!(!path.exists());
    crate::deletion::restore(
        &core,
        "first".into(),
        removed["generation"].as_i64().unwrap(),
    )
    .await
    .unwrap();
    crate::quality::recycle(&core).await.unwrap();
    assert_eq!(
        std::fs::read(&path).unwrap(),
        bytes,
        "restore between move and unlink rescues original bytes"
    );
    let preview = crate::deletion::preview(&core, "first".into(), "copies".into())
        .await
        .unwrap();
    crate::deletion::apply(
        &core,
        "first".into(),
        "copies".into(),
        preview["token"].as_str().unwrap().into(),
    )
    .await
    .unwrap();
    crate::quality::recycle(&core).await.unwrap();
    crate::quality::recycle(&core).await.unwrap();
    assert!(
        !path.exists(),
        "all explicit copy references released after wait"
    );
    assert_eq!(
        std::fs::read(external).unwrap(),
        bytes,
        "unmanaged external original is never moved or removed"
    );
    assert_eq!(
        crate::feed::query(&core.db, serde_json::from_value(json!({})).unwrap())
            .await
            .unwrap()["counts"]["global"],
        2,
        "copy removal leaves both metadata records"
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c06_health_merges_related_jobs_recovers_and_preserves_delivery_failures() {
    use trace_domain::errors::FailureClass;
    let (_dir, core) = empty_core().await;
    for n in 0..3 {
        jobs::enqueue(
            &core.db,
            format!("health-{n}"),
            "local",
            "fixture",
            json!({}),
            1,
            1,
            0,
        )
        .await
        .unwrap();
    }
    let jobs = jobs::claim_due(&core.db, "health", 3).await.unwrap();
    for j in &jobs {
        j.mark_running(&core.db).await.unwrap();
        j.fail(&core.db, &FailureClass::AuthRequired, "REDACTED", None)
            .await
            .unwrap();
    }
    let listed = crate::issues::listing(&core).await.unwrap();
    assert_eq!(listed["items"].as_array().unwrap().len(), 1);
    assert_eq!(listed["items"][0]["affected_jobs"], 3);
    let key = listed["items"][0]["issue_key"].as_str().unwrap().to_owned();
    assert_eq!(
        crate::issues::refs(&core, key.clone(), String::new())
            .await
            .unwrap()["items"]
            .as_array()
            .unwrap()
            .len(),
        3
    );
    let k = key.clone();
    core.db.write(move|tx|{tx.execute("UPDATE issue_notifications SET state='failed',last_error='system_notification_unavailable' WHERE issue_key=?1",[k])?;Ok(())}).await.unwrap();
    assert_eq!(
        crate::issues::listing(&core).await.unwrap()["items"][0]["notification_state"],
        "failed"
    );
    for (index, j) in jobs.iter().enumerate() {
        let id = j.job_id.clone();
        core.db
            .write(move |tx| crate::issues::resolved_job(tx, &id))
            .await
            .unwrap();
        let result = crate::issues::listing(&core).await.unwrap();
        assert_eq!(result["items"][0]["resolved_at"].is_null(), index != 2);
    }
    assert_eq!(
        crate::issues::listing(&core).await.unwrap()["items"][0]["notification_state"],
        "pending"
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c06_credential_update_rebind_generation_background_retry_and_quality_recovery() {
    use base64::Engine;
    use std::sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    };
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "credential-update".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        adapter_dir: Some(
            std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug"),
        ),
        ..Default::default()
    })
    .await
    .unwrap();
    let online = Arc::new(AtomicBool::new(true));
    let switch = online.clone();
    core.host.set_proxy_fixture(Arc::new(move |p| {
        let url = p["url"].as_str().unwrap();
        assert_eq!(url, "https://api.bilibili.com/x/web-interface/nav");
        if !switch.load(Ordering::SeqCst) {
            return Err(trace_contracts::plugin::PluginError {
                class: trace_domain::errors::FailureClass::TransientNetwork,
                message: "synthetic offline".into(),
                retry_after_ms: None,
                details: json!({}),
            });
        }
        Ok(trace_contracts::plugin::ProxyFetchResult {
            status: 200,
            headers: json!({}),
            body_b64: base64::engine::general_purpose::STANDARD.encode(
                serde_json::to_vec(&json!({"code":0,"data":{"isLogin":true,"vipStatus":1}}))
                    .unwrap(),
            ),
            final_url: url.into(),
        })
    }));
    let imported = command(
        &core,
        "credential.import",
        json!({"platform":"bilibili","secret":"SESSION=synthetic-v1","subject":"合成凭证"}),
    )
    .await;
    assert_eq!(imported["status"], "valid");
    let cid = imported["credential_id"].as_str().unwrap().to_owned();
    let c = cid.clone();
    core.db.write(move|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','7','t')",[])?;
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,created_at,updated_at) VALUES('s','a','video','{}',?1,'t','t')",[c])?;
        for (id,state) in [("waiting","waiting_auth"),("cancelled","cancelled"),("paused","paused")]{tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,state,created_at,updated_at) VALUES(?1,?1,'media','e','{\"subscription_id\":\"s\"}',?2,'t','t')",rusqlite::params![id,state])?;}Ok(())
    }).await.unwrap();
    let updated = command(
        &core,
        "credential.update",
        json!({"credential_id":cid,"secret":"SESSION=synthetic-v2"}),
    )
    .await;
    assert_eq!(updated["credential_id"], cid);
    assert_eq!(updated["status"], "valid");
    assert!(
        !core.vault.exists(&cid),
        "old encrypted version removed after atomic binding swap"
    );
    assert_eq!(
        crate::vault::load_current(&core, &cid).await.unwrap(),
        b"SESSION=synthetic-v2"
    );
    core.db
        .read(|c| {
            for (id, state) in [
                ("waiting", "queued"),
                ("cancelled", "cancelled"),
                ("paused", "paused"),
            ] {
                assert_eq!(
                    c.query_row("SELECT state FROM jobs WHERE job_id=?1", [id], |r| r
                        .get::<_, String>(0))?,
                    state
                );
            }
            Ok(())
        })
        .await
        .unwrap();
    let listed = command(&core, "credential.list", json!({})).await;
    assert!(!listed.to_string().contains("synthetic-v"));
    assert_eq!(listed["items"][0]["capability"]["live_access"], "unknown");
    command(&core,"subscription.update",json!({"subscription_id":"s","credential_id":"","quality":{"min_height":1080,"strict":true},"range":{"kind":"date_range","start":"2020-01-01T00:00:00Z","end":"2021-01-01T00:00:00Z"},"check_interval_seconds":123})).await;
    let subs = command(&core, "subscription.list", json!({"account_id":"a"})).await;
    assert!(subs["items"][0]["credential_id"].is_null());
    assert_eq!(subs["items"][0]["quality"]["strict"], true);
    assert_eq!(subs["items"][0]["check_interval_seconds"], 123);
    assert_eq!(subs["items"][0]["range"]["kind"], "date_range");
    command(
        &core,
        "subscription.update",
        json!({"subscription_id":"s","credential_id":cid}),
    )
    .await;
    online.store(false, Ordering::SeqCst);
    let offline = command(
        &core,
        "credential.update",
        json!({"credential_id":cid,"secret":"SESSION=synthetic-v3"}),
    )
    .await;
    assert_eq!(
        offline["status"], "unknown",
        "network failure is not logout"
    );
    let c = cid.clone();
    core.db.read(move|db|{let (generation,scheduled):(i64,bool)=db.query_row("SELECT generation,EXISTS(SELECT 1 FROM jobs WHERE job_type='credential_probe' AND subject_id=credentials_state.credential_id AND state='queued' AND next_run_at>?2) FROM credentials_state WHERE credential_id=?1",rusqlite::params![c,jobs::now()],|r|Ok((r.get(0)?,r.get(1)?)))?;assert_eq!(generation,3);assert!(scheduled,"transient validation is retried with a persistent delay");Ok(())}).await.unwrap();
    command(&core, "credential.revoke", json!({"credential_id":cid})).await;
    assert!(crate::vault::load_current(&core, &cid).await.is_err());
    core.shutdown().await;
}
