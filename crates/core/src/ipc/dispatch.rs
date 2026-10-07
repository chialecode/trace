//! 命令分发：控制协议 → 应用服务（INV-09：写命令幂等收据同事务）。

use crate::core::Core;
use crate::error::CoreError;
use crate::jobs;
use base64::Engine as _;
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::commands as cmd;
use trace_contracts::control::{Request, Response};
use trace_domain::CONTROL_PROTOCOL_VERSION;

pub fn error_response(request_id: &str, e: CoreError) -> Response {
    Response {
        protocol_version: CONTROL_PROTOCOL_VERSION.into(),
        request_id: request_id.into(),
        ok: false,
        result: None,
        error: Some(e.to_api_error()),
    }
}

fn ok_response(request_id: &str, result: serde_json::Value) -> Response {
    Response {
        protocol_version: CONTROL_PROTOCOL_VERSION.into(),
        request_id: request_id.into(),
        ok: true,
        result: Some(result),
        error: None,
    }
}

/// 判断命令是否为写命令（需要幂等收据）。
fn is_write(command: &str) -> bool {
    matches!(
        command,
        cmd::CREATOR_CREATE
            | cmd::CREATOR_UPDATE
            | cmd::ACCOUNT_BIND
            | cmd::ACCOUNT_UNBIND
            | cmd::ACCOUNT_UPDATE
            | cmd::SUBSCRIPTION_CREATE
            | cmd::SUBSCRIPTION_UPDATE
            | cmd::CREDENTIAL_IMPORT
            | cmd::CREDENTIAL_REVOKE
            | cmd::PLUGIN_SET_STATE
            | cmd::EXPORT_START
            | cmd::BACKUP_START
            | cmd::RESTORE_START
            | cmd::DAEMON_PAUSE
            | cmd::DAEMON_SHUTDOWN
    )
}

pub async fn dispatch(core: &Arc<Core>, req: Request) -> Response {
    // 幂等收据：同键重发返回首次结果（C10）。
    if let (true, Some(key)) = (is_write(&req.command), req.idempotency_key.clone()) {
        let hit: Option<String> = {
            let key2 = key.clone();
            core.db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT result_json FROM receipts WHERE idempotency_key=?1",
                        [&key2],
                        |r| r.get::<_, String>(0),
                    )
                    .ok())
                })
                .await
                .unwrap_or(None)
        };
        if let Some(cached) = hit {
            return Response {
                protocol_version: CONTROL_PROTOCOL_VERSION.into(),
                request_id: req.request_id.clone(),
                ok: true,
                result: Some(serde_json::from_str(&cached).unwrap_or(serde_json::Value::Null)),
                error: None,
            };
        }
        let result = execute(core, &req).await;
        return match result {
            Ok(value) => {
                let value_str = value.to_string();
                let k = key;
                let _ = core
                    .db
                    .write(move |tx| {
                        tx.execute(
                            "INSERT OR IGNORE INTO receipts (idempotency_key, command, result_json, created_at)
                             VALUES (?1,?2,?3,?4)",
                            rusqlite::params![k, "receipt", value_str, jobs::now()],
                        )?;
                        Ok(())
                    })
                    .await;
                ok_response(&req.request_id, value)
            }
            Err(e) => error_response(&req.request_id, e),
        };
    }
    match execute(core, &req).await {
        Ok(value) => ok_response(&req.request_id, value),
        Err(e) => error_response(&req.request_id, e),
    }
}

async fn execute(core: &Arc<Core>, req: &Request) -> Result<serde_json::Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::DAEMON_STATUS => daemon_status(core).await,
        cmd::DAEMON_PAUSE => {
            let paused: bool = args
                .get("paused")
                .and_then(|v| v.as_bool())
                .unwrap_or(false);
            core.paused
                .store(paused, std::sync::atomic::Ordering::SeqCst);
            daemon_status(core).await
        }
        cmd::DAEMON_SHUTDOWN => {
            let core2 = core.clone();
            tokio::spawn(async move {
                tokio::time::sleep(std::time::Duration::from_millis(150)).await;
                core2.shutdown().await;
                std::process::exit(0);
            });
            Ok(serde_json::json!({"accepted": true}))
        }
        cmd::CREATOR_CREATE => {
            let a: ctl::CreatorCreateArgs = parse(args)?;
            let id = format!("cre-{}", uuid::Uuid::new_v4());
            let now = jobs::now();
            let aliases = serde_json::to_string(&a.aliases)?;
            let tags = serde_json::to_string(&a.tags)?;
            let display = a.display_name.clone();
            let notes = a.notes.clone();
            let id2 = id.clone();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "INSERT INTO creators (creator_id, display_name, aliases, tags, status, notes, created_at, updated_at)
                         VALUES (?1,?2,?3,?4,'active',?5,?6,?6)",
                        rusqlite::params![id2, display, aliases, tags, notes, now],
                    )?;
                    crate::objects::outbox_emit(tx, "creator.changed", &id2, &serde_json::json!({}))?;
                    Ok(())
                })
                .await?;
            creator_dto(core, &id).await
        }
        cmd::CREATOR_UPDATE => {
            let a: ctl::CreatorUpdateArgs = parse(args)?;
            let id = a.creator_id.clone();
            let display = a.display_name.clone();
            let aliases = a
                .aliases
                .clone()
                .map(|v| serde_json::to_string(&v).unwrap());
            let tags = a.tags.clone().map(|v| serde_json::to_string(&v).unwrap());
            let notes = a.notes.clone();
            let status = a.status.clone();
            core.db
                .write(move |tx| {
                    if display.is_some() {
                        tx.execute(
                            "UPDATE creators SET display_name=?2, updated_at=?3 WHERE creator_id=?1",
                            rusqlite::params![id, display, jobs::now()],
                        )?;
                    }
                    if let Some(al) = aliases {
                        tx.execute(
                            "UPDATE creators SET aliases=?2, updated_at=?3 WHERE creator_id=?1",
                            rusqlite::params![id, al, jobs::now()],
                        )?;
                    }
                    if let Some(t) = tags {
                        tx.execute(
                            "UPDATE creators SET tags=?2, updated_at=?3 WHERE creator_id=?1",
                            rusqlite::params![id, t, jobs::now()],
                        )?;
                    }
                    if let Some(n) = notes {
                        tx.execute(
                            "UPDATE creators SET notes=?2, updated_at=?3 WHERE creator_id=?1",
                            rusqlite::params![id, n, jobs::now()],
                        )?;
                    }
                    if let Some(s) = status {
                        tx.execute(
                            "UPDATE creators SET status=?2, updated_at=?3 WHERE creator_id=?1",
                            rusqlite::params![id, s, jobs::now()],
                        )?;
                    }
                    crate::objects::outbox_emit(tx, "creator.changed", &id, &serde_json::json!({}))?;
                    Ok(())
                })
                .await?;
            // 改名不改变身份（INV-01）；搜索索引随事件更新。
            creator_dto(core, &a.creator_id).await
        }
        cmd::CREATOR_LIST => {
            let include_archived = args
                .get("include_archived")
                .and_then(|v| v.as_bool())
                .unwrap_or(false);
            let ids: Vec<String> = core
                .db
                .read(move |c| {
                    let sql = if include_archived {
                        "SELECT creator_id FROM creators ORDER BY created_at"
                    } else {
                        "SELECT creator_id FROM creators WHERE status='active' ORDER BY created_at"
                    };
                    let mut stmt = c.prepare(sql)?;
                    let rows = stmt
                        .query_map([], |r| r.get(0))?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            let mut items = Vec::new();
            for id in ids {
                items.push(creator_dto(core, &id).await?);
            }
            Ok(serde_json::json!({ "items": items }))
        }
        cmd::CREATOR_GET => {
            let a: ctl::CreatorGetArgs = parse(args)?;
            let mut creator = creator_dto(core, &a.creator_id).await?;
            let accounts: Vec<serde_json::Value> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT a.account_id, a.namespace, a.native_account_key, a.display_name, a.profile_url
                         FROM source_accounts a JOIN account_bindings b ON b.account_id=a.account_id
                         WHERE b.creator_id=?1",
                    )?;
                    let rows = stmt
                        .query_map([&a.creator_id], |r| {
                            Ok(serde_json::json!({
                                "account_id": r.get::<_, String>(0)?,
                                "namespace": r.get::<_, String>(1)?,
                                "native_account_key": r.get::<_, String>(2)?,
                                "display_name": r.get::<_, Option<String>>(3)?,
                                "profile_url": r.get::<_, Option<String>>(4)?,
                            }))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            creator["accounts"] = serde_json::Value::Array(accounts);
            Ok(creator)
        }
        cmd::ACCOUNT_BIND => {
            let a: ctl::AccountBindArgs = parse(args)?;
            let account_id = format!("acc-{}", uuid::Uuid::new_v4());
            let ns = a.namespace.clone();
            let key = a.native_account_key.clone();
            let display = a.display_name.clone();
            let url = a.profile_url.clone();
            let creator = a.creator_id.clone();
            let acc = account_id.clone();
            let now = jobs::now();
            let actual_id = core.db
                .write(move |tx| {
                    tx.execute(
                        "INSERT INTO source_accounts (account_id, namespace, native_account_key, display_name, profile_url, first_seen_at)
                         VALUES (?1,?2,?3,?4,?5,?6)
                         ON CONFLICT(namespace, native_account_key) DO NOTHING",
                        rusqlite::params![acc, ns, key, display, url, now],
                    )?;
                    let actual: String = tx.query_row(
                        "SELECT account_id FROM source_accounts WHERE namespace=?1 AND native_account_key=?2",
                        rusqlite::params![ns, key],
                        |r| r.get(0),
                    )?;
                    // An active binding to another creator is a conflict, never a false success.
                    let conflict: bool = tx.query_row("SELECT EXISTS(SELECT 1 FROM account_bindings WHERE account_id=?1 AND state='active' AND creator_id!=?2)", rusqlite::params![actual,creator], |r| r.get(0))?;
                    if conflict { return Err(CoreError::VersionConflict("account binding".into())); }
                    tx.execute(
                        "INSERT INTO account_bindings (binding_id, creator_id, account_id, state, created_at)
                         VALUES (?1,?2,?3,'active',?4) ON CONFLICT(account_id) DO UPDATE SET creator_id=excluded.creator_id,state='active',ended_at=NULL",
                        rusqlite::params![format!("bnd-{}", uuid::Uuid::new_v4()), creator, actual, now],
                    )?;
                    crate::objects::outbox_emit(tx, "account.bound", &actual, &serde_json::json!({"creator_id": creator}))?;
                    Ok(actual)
                })
                .await?;
            Ok(serde_json::json!({"account_id": actual_id}))
        }
        cmd::ACCOUNT_UNBIND => {
            let a: ctl::AccountUnbindArgs = parse(args)?;
            let id = a.account_id.clone();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "UPDATE account_bindings SET state='ended', ended_at=?2 WHERE account_id=?1",
                        rusqlite::params![id, jobs::now()],
                    )?;
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::ACCOUNT_UPDATE => {
            let a: ctl::AccountUpdateArgs = parse(args)?;
            let id = a.account_id.clone();
            let display = a.display_name.clone();
            let url = a.profile_url.clone();
            core.db
                .write(move |tx| {
                    if let Some(d) = display {
                        tx.execute(
                            "UPDATE source_accounts SET display_name=?2, profile_updated_at=?3 WHERE account_id=?1",
                            rusqlite::params![id, d, jobs::now()],
                        )?;
                    }
                    if let Some(u) = url {
                        tx.execute(
                            "UPDATE source_accounts SET profile_url=?2, profile_updated_at=?3 WHERE account_id=?1",
                            rusqlite::params![id, u, jobs::now()],
                        )?;
                    }
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::ACCOUNT_LIST => {
            let a: ctl::AccountListArgs = parse(args)?;
            let creator = a.creator_id.clone();
            let items: Vec<serde_json::Value> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT a.account_id, a.namespace, a.native_account_key, a.display_name, a.profile_url,
                                b.creator_id,
                                (SELECT count(*) FROM subscriptions s WHERE s.account_id=a.account_id)
                         FROM source_accounts a
                         LEFT JOIN account_bindings b ON b.account_id=a.account_id AND b.state='active'
                         WHERE (?1 IS NULL OR b.creator_id = ?1)",
                    )?;
                    let rows = stmt
                        .query_map([creator], |r| {
                            Ok(serde_json::json!({
                                "account_id": r.get::<_, String>(0)?,
                                "namespace": r.get::<_, String>(1)?,
                                "native_account_key": r.get::<_, String>(2)?,
                                "display_name": r.get::<_, Option<String>>(3)?,
                                "profile_url": r.get::<_, Option<String>>(4)?,
                                "creator_id": r.get::<_, Option<String>>(5)?,
                                "subscription_count": r.get::<_, i64>(6)?,
                            }))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            Ok(serde_json::json!({ "items": items }))
        }
        cmd::SUBSCRIPTION_CREATE => {
            let a: ctl::SubscriptionCreateArgs = parse(args)?;
            let sub_id = format!("sub-{}", uuid::Uuid::new_v4());
            let account = a.account_id.clone();
            let content = a.content_type.clone();
            let range = if a.range.is_null() {
                serde_json::json!({"kind": "from_now"})
            } else {
                a.range.clone()
            };
            let credential = a.credential_id.clone();
            let interval = a
                .check_interval_seconds
                .unwrap_or(crate::config::DEFAULT_CHECK_INTERVAL_SECONDS);
            let sub = sub_id.clone();
            let now = jobs::now();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "INSERT INTO subscriptions (subscription_id, account_id, content_type, range_json,
                         credential_id, config_version, state, check_interval_seconds, created_at, updated_at)
                         VALUES (?1,?2,?3,?4,?5,1,'enabled',?6,?7,?7)",
                        rusqlite::params![sub, account, content, range.to_string(), credential, interval, now],
                    )?;
                    Ok(())
                })
                .await?;
            // 创建初始扫描任务。
            let jk = format!("scan:{sub_id}:incremental");
            jobs::enqueue(
                &core.db,
                jk,
                "scan",
                &sub_id,
                serde_json::json!({"subscription_id": sub_id, "mode": "incremental"}),
                1,
                1,
                0,
            )
            .await?;
            Ok(serde_json::json!({"subscription_id": sub_id}))
        }
        cmd::SUBSCRIPTION_UPDATE => {
            let a: ctl::SubscriptionUpdateArgs = parse(args)?;
            let id = a.subscription_id.clone();
            let expected_revision = req.if_revision;
            let range = a.range.clone().map(|v| v.to_string());
            let enabled = a.enabled;
            let interval = a.check_interval_seconds;
            core.db
                .write(move |tx| {
                    if let Some(expect) = expected_revision {
                        let current:i64=tx.query_row("SELECT config_version FROM subscriptions WHERE subscription_id=?1",[&id],|r|r.get(0))?;
                        if current != expect { return Err(CoreError::VersionConflict(format!("subscription {id}"))); }
                    }

                    if let Some(r) = range {
                        tx.execute(
                            "UPDATE subscriptions SET range_json=?2, config_version=config_version+1, updated_at=?3 WHERE subscription_id=?1",
                            rusqlite::params![id, r, jobs::now()],
                        )?;
                    }
                    if let Some(e) = enabled {
                        jobs::set_subscription_enabled(tx, &id, e)?;
                    }
                    if let Some(i) = interval {
                        tx.execute(
                            "UPDATE subscriptions SET check_interval_seconds=?2, config_version=config_version+1, updated_at=?3 WHERE subscription_id=?1",
                            rusqlite::params![id, i, jobs::now()],
                        )?;
                    }
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::SUBSCRIPTION_CHECK_NOW => {
            let a: ctl::SubscriptionCheckNowArgs = parse(args)?;
            let sub = a.subscription_id.clone();
            let jk = format!("scan:{sub}:manual:{}", uuid::Uuid::new_v4());
            let job_id = jobs::enqueue(
                &core.db,
                jk,
                "scan",
                &sub,
                serde_json::json!({"subscription_id": sub, "mode": "incremental"}),
                0,
                1,
                0,
            )
            .await?;
            Ok(serde_json::json!({"job_id": job_id}))
        }
        cmd::SUBSCRIPTION_LIST => {
            let a: ctl::SubscriptionListArgs = parse(args)?;
            let creator = a.creator_id.clone();
            let items: Vec<serde_json::Value> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT s.subscription_id, s.account_id, s.content_type, s.range_json, s.state,
                                s.config_version, s.credential_id, s.updated_at
                         FROM subscriptions s
                         WHERE (?1 IS NULL OR EXISTS (
                            SELECT 1 FROM account_bindings b WHERE b.account_id=s.account_id AND b.creator_id=?1))",
                    )?;
                    let rows = stmt
                        .query_map([creator], |r| {
                            Ok(serde_json::json!({
                                "subscription_id": r.get::<_, String>(0)?,
                                "account_id": r.get::<_, String>(1)?,
                                "content_type": r.get::<_, String>(2)?,
                                "range": r.get::<_, String>(3)?,
                                "state": r.get::<_, String>(4)?,
                                "config_version": r.get::<_, i64>(5)?,
                                "credential_id": r.get::<_, Option<String>>(6)?,
                                "updated_at": r.get::<_, String>(7)?,
                            }))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            Ok(serde_json::json!({ "items": items }))
        }
        cmd::ARCHIVE_TIMELINE => {
            let a: ctl::ArchiveTimelineArgs = parse(args)?;
            crate::feed::query(&core.db, a).await
        }

        cmd::ARCHIVE_ENTRY_GET => {
            let a: ctl::ArchiveEntryGetArgs = parse(args)?;
            let entry_id = a.entry_id.clone();
            let detail = core
                .db
                .read(move |c| {
                    let entry: serde_json::Value = c.query_row(
                        "SELECT e.entry_id, e.namespace, e.native_kind, e.native_entry_key, e.published_at,
                                e.published_precision, e.first_seen_at
                         FROM entries e WHERE e.entry_id=?1",
                        [&entry_id],
                        |r| {
                            Ok(serde_json::json!({
                                "entry_id": r.get::<_, String>(0)?,
                                "namespace": r.get::<_, String>(1)?,
                                "kind": r.get::<_, String>(2)?,
                                "native_key": r.get::<_, String>(3)?,
                                "published_at": r.get::<_, Option<String>>(4)?,
                                "published_precision": r.get::<_, Option<String>>(5)?,
                                "discovered_at": r.get::<_, String>(6)?,
                            }))
                        },
                    )?;
                    let mut revisions = Vec::new();
                    let mut stmt = c.prepare(
                        "SELECT revision_id, title, normalized_body, completeness, observed_at, source_version_evidence
                         FROM entry_revisions WHERE entry_id=?1 ORDER BY observed_at ASC",
                    )?;
                    let rows = stmt
                        .query_map([&entry_id], |r| {
                            Ok(serde_json::json!({
                                "revision_id": r.get::<_, String>(0)?,
                                "title": r.get::<_, String>(1)?,
                                "body": r.get::<_, String>(2)?,
                                "completeness": r.get::<_, String>(3)?,
                                "observed_at": r.get::<_, String>(4)?,
                                "evidence": r.get::<_, Option<String>>(5)?,
                            }))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    revisions.extend(rows);
                    let mut observations = Vec::new();
                    let mut stmt = c.prepare(
                        "SELECT observation_seq, access_state, observed_at, revision_id
                         FROM source_observations WHERE entry_id=?1 ORDER BY observation_seq ASC",
                    )?;
                    let obs = stmt
                        .query_map([&entry_id], |r| {
                            Ok(serde_json::json!({
                                "seq": r.get::<_, i64>(0)?,
                                "access_state": r.get::<_, String>(1)?,
                                "observed_at": r.get::<_, String>(2)?,
                                "revision_id": r.get::<_, Option<String>>(3)?,
                            }))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    observations.extend(obs);
                    Ok(serde_json::json!({"entry": entry, "revisions": revisions, "observations": observations}))
                })
                .await?;
            // 媒体与变体。
            let media: Vec<serde_json::Value> = core
                .db
                .read({
                    let e = a.entry_id.clone();
                    move |c| {
                        let mut stmt = c.prepare(
                            "SELECT mi.media_id, mi.role, mi.position, mi.title,
                                    mv.variant_id, mv.source_representation, b.blob_id, b.size,
                                    mv.claimed_spec, mv.measured_spec, b.mime,
                                    EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=b.blob_id AND rp.state='verified')
                             FROM entries e
                             JOIN entry_revisions r ON r.entry_id = e.entry_id
                             JOIN media_items mi ON mi.revision_id = r.revision_id
                             LEFT JOIN media_variants mv ON mv.media_id = mi.media_id
                             LEFT JOIN blobs b ON b.blob_id = mv.blob_id
                             WHERE e.entry_id = ?1 AND r.revision_id=(SELECT revision_id FROM entry_revisions WHERE entry_id=?1 ORDER BY (completeness='full') DESC,observed_at DESC LIMIT 1)
                             ORDER BY mi.position, mi.role",
                        )?;
                        let rows = stmt
                            .query_map([&e], |r| {
                                Ok(serde_json::json!({
                                    "media_id": r.get::<_, String>(0)?,
                                    "role": r.get::<_, String>(1)?,
                                    "position": r.get::<_, i64>(2)?,
                                    "title": r.get::<_, Option<String>>(3)?,
                                    "variant_id": r.get::<_, Option<String>>(4)?,
                                    "representation": r.get::<_, Option<String>>(5)?,
                                    "blob_id": r.get::<_, Option<String>>(6)?,
                                    "size": r.get::<_, Option<i64>>(7)?,
                                    "claimed_spec": r.get::<_, Option<String>>(8)?,
                                    "measured_spec": r.get::<_, Option<String>>(9)?,
                                    "mime": r.get::<_,Option<String>>(10)?,
                                    "available": r.get::<_,bool>(11)?,
                                }))
                            })?
                            .collect::<Result<Vec<_>, _>>()?;
                        Ok(rows)
                    }
                })
                .await?;
            let mut detail = detail;
            detail["media"] = serde_json::Value::Array(media);
            detail["entry"]["body"] = detail["revisions"]
                .as_array()
                .and_then(|r| {
                    r.iter()
                        .rev()
                        .find(|r| r["completeness"] == "full")
                        .or_else(|| r.last())
                })
                .map(|r| r["body"].clone())
                .unwrap_or_default();
            Ok(detail)
        }
        cmd::JOB_LIST => {
            let a: ctl::JobListArgs = parse(args)?;
            let limit = a.limit.clamp(1, 200);
            let state = a.state.clone();
            let jt = a.job_type.clone();
            let items: Vec<ctl::JobDto> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT job_id, job_key, job_type, subject_id, state, attempts, target_generation,
                                priority, next_run_at, last_error, created_at, updated_at
                         FROM jobs WHERE (?1 IS NULL OR state=?1) AND (?2 IS NULL OR job_type=?2)
                         ORDER BY updated_at DESC LIMIT ?3",
                    )?;
                    let rows = stmt
                        .query_map(rusqlite::params![state, jt, limit], |r| {
                            Ok(ctl::JobDto {
                                job_id: r.get(0)?,
                                job_key: r.get(1)?,
                                job_type: r.get(2)?,
                                subject_id: r.get(3)?,
                                state: r.get(4)?,
                                attempts: r.get(5)?,
                                target_generation: r.get(6)?,
                                priority: r.get(7)?,
                                next_run_at: r.get(8)?,
                                last_error: r.get::<_, Option<String>>(9)?
                                    .and_then(|s| serde_json::from_str(&s).ok()),
                                created_at: r.get(10)?,
                                updated_at: r.get(11)?,
                            })
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            Ok(serde_json::to_value(ctl::PageDto {
                items,
                next_cursor: None,
            })?)
        }
        cmd::JOB_GET => {
            let a: ctl::JobGetArgs = parse(args)?;
            let id = a.job_id.clone();
            let items: Vec<ctl::JobDto> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT job_id, job_key, job_type, subject_id, state, attempts, target_generation,
                                priority, next_run_at, last_error, created_at, updated_at
                         FROM jobs WHERE job_id=?1",
                    )?;
                    let rows = stmt
                        .query_map([&id], |r| {
                            Ok(ctl::JobDto {
                                job_id: r.get(0)?,
                                job_key: r.get(1)?,
                                job_type: r.get(2)?,
                                subject_id: r.get(3)?,
                                state: r.get(4)?,
                                attempts: r.get(5)?,
                                target_generation: r.get(6)?,
                                priority: r.get(7)?,
                                next_run_at: r.get(8)?,
                                last_error: r.get::<_, Option<String>>(9)?
                                    .and_then(|s| serde_json::from_str(&s).ok()),
                                created_at: r.get(10)?,
                                updated_at: r.get(11)?,
                            })
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            items
                .into_iter()
                .next()
                .map(|j| serde_json::to_value(j).unwrap())
                .ok_or_else(|| CoreError::NotFound("job".into()))
        }
        cmd::JOB_RETRY => {
            let a: ctl::JobRetryArgs = parse(args)?;
            let id = a.job_id.clone();
            core.db
                .write(move |tx| {
                    // 显式重试：保留执行历史，重排队（不删除 last_error 历史）。
                    let n = tx.execute(
                        "UPDATE jobs SET state='queued', target_generation=target_generation+1, attempts=0, lease_owner=NULL, lease_expires_at=NULL, next_run_at=?2, updated_at=?2
                         WHERE job_id=?1 AND state IN ('succeeded','failed','cancelled','waiting_auth','waiting_user','waiting_plugin','waiting_retry','waiting_storage')",
                        rusqlite::params![id, jobs::now()],
                    )?;
                    if n == 0 {
                        return Err(CoreError::NotFound("retryable job".into()));
                    }
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::JOB_CANCEL => {
            let a: ctl::JobCancelArgs = parse(args)?;
            let job = ctl::JobDto {
                job_id: a.job_id.clone(),
                ..Default::default()
            };
            // 直接取消（不新建目标代次——M1a 无需 Attempt 级取消语义）。
            let id = a.job_id.clone();
            let cancelled: bool = core
                .db
                .write(move |tx| {
                    let n = tx.execute(
                        "UPDATE jobs SET state='cancelled', target_generation=target_generation+1, lease_owner=NULL, lease_expires_at=NULL, updated_at=?2
                         WHERE job_id=?1 AND state NOT IN ('succeeded','failed','cancelled')",
                        rusqlite::params![id, jobs::now()],
                    )?;
                    Ok(n == 1)
                })
                .await?;
            let _ = job;
            Ok(serde_json::json!({"cancelled": cancelled}))
        }
        cmd::CREDENTIAL_LIST => {
            let a: ctl::CredentialListArgs = parse(args)?;
            let platform = a.platform.clone();
            let items: Vec<serde_json::Value> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT credential_id, platform, subject, status, capability, validated_at, created_at
                         FROM credentials_state WHERE (?1 IS NULL OR platform=?1)",
                    )?;
                    let rows = stmt
                        .query_map([platform], |r| {
                            Ok(serde_json::json!({
                                "credential_id": r.get::<_, String>(0)?,
                                "platform": r.get::<_, String>(1)?,
                                "subject": r.get::<_, Option<String>>(2)?,
                                "status": r.get::<_, String>(3)?,
                                "capability": r.get::<_, String>(4)?,
                                "validated_at": r.get::<_, Option<String>>(5)?,
                                "created_at": r.get::<_, String>(6)?,
                            }))
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            Ok(serde_json::json!({ "items": items }))
        }
        cmd::CREDENTIAL_IMPORT => {
            let a: ctl::CredentialImportArgs = parse(args)?;
            if a.secret.is_empty() {
                return Err(CoreError::InvalidInput("empty credential".into()));
            }
            let credential_id = format!("cred-{}", uuid::Uuid::new_v4());
            core.vault.store(&credential_id, a.secret.as_bytes())?;
            let id = credential_id.clone();
            let platform = a.platform.clone();
            let subject = a.subject.clone();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "INSERT INTO credentials_state (credential_id, platform, subject, status, capability, created_at, updated_at, vault_ref)
                         VALUES (?1,?2,?3,'unknown','{}',?4,?4,?1)",
                        rusqlite::params![id, platform, subject, jobs::now()],
                    )?;
                    Ok(())
                })
                .await?;
            // 解密入短期缓存供适配器代理使用（仅内存）。
            core.cache_cookie(&a.platform, a.secret);
            // 探测能力。
            let validate = dispatch_probe_auth(core, &credential_id, &a.platform).await;
            match validate {
                Ok(cap) => Ok(
                    serde_json::json!({"credential_id": credential_id, "status": cap.0, "capability": cap.1}),
                ),
                Err(e) => Ok(
                    serde_json::json!({"credential_id": credential_id, "status": "invalid", "error": e.failure_class()}),
                ),
            }
        }
        cmd::CREDENTIAL_VALIDATE => {
            let a: ctl::CredentialRefArgs = parse(args)?;
            let platform: String = {
                let id = a.credential_id.clone();
                core.db
                    .read(move |c| {
                        Ok(c.query_row(
                            "SELECT platform FROM credentials_state WHERE credential_id=?1",
                            [&id],
                            |r| r.get::<_, String>(0),
                        )?)
                    })
                    .await?
            };
            let (status, capability) =
                dispatch_probe_auth(core, &a.credential_id, &platform).await?;
            Ok(serde_json::json!({"status": status, "capability": capability}))
        }
        cmd::CREDENTIAL_REVOKE => {
            let a: ctl::CredentialRefArgs = parse(args)?;
            let platform: String = {
                let id = a.credential_id.clone();
                core.db
                    .read(move |c| {
                        Ok(c.query_row(
                            "SELECT platform FROM credentials_state WHERE credential_id=?1",
                            [&id],
                            |r| r.get::<_, String>(0),
                        )?)
                    })
                    .await?
            };
            core.vault.remove(&a.credential_id)?;
            let id = a.credential_id.clone();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "DELETE FROM credentials_state WHERE credential_id=?1",
                        [&id],
                    )?;
                    Ok(())
                })
                .await?;
            core.clear_cookie(&platform);
            // 撤销使等待凭证的任务进入 waiting_auth（只影响关联工作，不毁历史）。
            core.db
                .write(|tx| {
                    tx.execute(
                        "UPDATE jobs SET state='waiting_auth', updated_at=?1 WHERE state='waiting_auth' OR (state IN ('queued','waiting_retry') AND job_type IN ('scan','media'))",
                        [jobs::now()],
                    )?;
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::PLUGIN_LIST => {
            let items: Vec<ctl::PluginDto> = core
                .db
                .read(|c| {
                    let mut stmt = c.prepare(
                        "SELECT plugin_id, platform, version, state, protocol, capabilities FROM plugins_state",
                    )?;
                    let rows = stmt
                        .query_map([], |r| {
                            Ok(ctl::PluginDto {
                                plugin_id: r.get(0)?,
                                platform: r.get(1)?,
                                version: r.get(2)?,
                                state: r.get(3)?,
                                protocol: r.get(4)?,
                                capabilities: r
                                    .get::<_, String>(5)
                                    .ok()
                                    .and_then(|s| serde_json::from_str(&s).ok())
                                    .unwrap_or_default(),
                            })
                        })?
                        .collect::<Result<Vec<_>, _>>()?;
                    Ok(rows)
                })
                .await?;
            // 空表时呈现内置适配器注册信息（bilibili）。
            let mut items = items;
            if items.is_empty() {
                for spec in crate::pluginhost::default_adapter_specs(&core.profile) {
                    let state = core.host.session_state(&spec.plugin_id).await;
                    items.push(ctl::PluginDto {
                        plugin_id: spec.plugin_id,
                        platform: spec.platform,
                        version: "0.1.0".into(),
                        state: format!("{state:?}").to_lowercase(),
                        protocol: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
                        capabilities: vec![
                            "resolve_account".into(),
                            "enumerate".into(),
                            "observe".into(),
                            "resolve_media".into(),
                        ],
                    });
                }
            }
            Ok(serde_json::json!({ "items": items }))
        }
        cmd::PLUGIN_SET_STATE => {
            let a: ctl::PluginSetStateArgs = parse(args)?;
            match a.state.as_str() {
                "enabled" => {
                    // 重新可用：waiting_plugin 任务复活（停用→启用的恢复路径）。
                    core.db
                        .write(|tx| {
                            tx.execute(
                                "UPDATE jobs SET state='queued', updated_at=?1 WHERE state='waiting_plugin'",
                                [jobs::now()],
                            )?;
                            Ok(())
                        })
                        .await?;
                }
                "disabled" => {
                    core.host.kill_session(&a.plugin_id).await;
                    core.db
                        .write(|tx| {
                            tx.execute(
                                "UPDATE jobs SET state='waiting_plugin', updated_at=?1 WHERE job_type IN ('scan','observe','media') AND state IN ('queued','leased','running','waiting_retry')",
                                [jobs::now()],
                            )?;
                            Ok(())
                        })
                        .await?;
                }
                _ => return Err(CoreError::InvalidInput("state".into())),
            }
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::STORAGE_USAGE => crate::storage_ops::usage(core).await,
        cmd::EXPORT_START => {
            let a: ctl::ExportStartArgs = parse(args)?;
            if a.target_dir.trim().is_empty() {
                return Err(CoreError::InvalidInput("target".into()));
            }
            crate::paths::no_links(std::path::Path::new(&a.target_dir))?;
            let job_id = jobs::enqueue(
                &core.db,
                format!("export:{}", uuid::Uuid::new_v4()),
                "export",
                "archive",
                serde_json::to_value(&a)?,
                1,
                1,
                2,
            )
            .await?;
            Ok(serde_json::json!({"accepted":true,"job_id":job_id,"target":a.target_dir}))
        }
        cmd::BACKUP_START => {
            let a: ctl::BackupStartArgs = if args.as_object().is_some_and(|a| a.is_empty()) {
                ctl::BackupStartArgs {
                    target_root: core
                        .profile
                        .data_root
                        .join("state/backups")
                        .to_string_lossy()
                        .to_string(),
                    include_media: true,
                }
            } else {
                parse(args)?
            };
            if a.target_root.trim().is_empty() {
                return Err(CoreError::InvalidInput("target".into()));
            }
            crate::paths::no_links(std::path::Path::new(&a.target_root))?;
            let job_id = jobs::enqueue(
                &core.db,
                format!("backup:{}", uuid::Uuid::new_v4()),
                "backup",
                "archive",
                serde_json::to_value(&a)?,
                1,
                1,
                2,
            )
            .await?;
            Ok(serde_json::json!({"accepted":true,"job_id":job_id,"target":a.target_root}))
        }
        cmd::BACKUP_LIST => {
            let root = core.profile.data_root.join("state/backups");
            crate::paths::no_links(&root)?;
            let mut items = Vec::new();
            if root.exists() {
                for entry in std::fs::read_dir(root)?.take(200) {
                    let path = entry?.path();
                    crate::paths::no_links(&path)?;
                    let manifest = path.join("manifest.json");
                    if manifest.is_file()
                        && manifest.metadata()?.len() <= 1_048_576
                        && let Ok(mut value) =
                            serde_json::from_slice::<serde_json::Value>(&std::fs::read(manifest)?)
                    {
                        value["backup_root"] = path.to_string_lossy().to_string().into();
                        items.push(value);
                    }
                }
            }
            items.sort_by(|a, b| b["created_at"].as_str().cmp(&a["created_at"].as_str()));
            Ok(
                serde_json::json!({"items":items,"next_cursor":null,"scope":"local-default-backups-up-to-200"}),
            )
        }
        cmd::RESTORE_START => {
            let a: ctl::RestoreStartArgs = parse(args)?;
            let core2 = core.clone();
            let result = crate::storage_ops::restore(
                &core2,
                std::path::Path::new(&a.backup_root),
                std::path::Path::new(&a.target_root),
            )
            .await?;
            Ok(result)
        }
        cmd::ARCHIVE_OPEN_SOURCE => {
            let a: ctl::ArchiveEntryGetArgs = parse(args)?;
            let (ns, key): (String, String) = core
                .db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT namespace,native_entry_key FROM entries WHERE entry_id=?1",
                        [a.entry_id],
                        |r| Ok((r.get(0)?, r.get(1)?)),
                    )?)
                })
                .await?;
            if !ns.starts_with("bilibili") || !key.bytes().all(|b| b.is_ascii_alphanumeric()) {
                return Err(CoreError::Unsupported("source URL unavailable".into()));
            }
            #[cfg(windows)]
            {
                use std::os::windows::process::CommandExt;
                std::process::Command::new("explorer.exe")
                    .arg(format!("https://www.bilibili.com/video/{key}"))
                    .creation_flags(0x0800_0000)
                    .spawn()?;
            }
            Ok(serde_json::json!({"opened":true}))
        }
        cmd::MEDIA_RELEASE => {
            let id = args["lease_id"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("lease id".into()))?;
            core.leases.lock().unwrap().remove(id);
            Ok(serde_json::json!({"released":true}))
        }
        cmd::MEDIA_OPEN_EXTERNAL => {
            let a: ctl::MediaLeaseArgs = parse(args)?;
            let sha = crate::paths::blob_sha(&a.blob_id)?.to_owned();
            let blob = a.blob_id;
            let mime:String=core.db.read(move |c|Ok(c.query_row("SELECT mime FROM blobs b WHERE blob_id=?1 AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=b.blob_id AND r.state='verified')",[blob],|r|r.get(0))?)).await?;
            let extension = match mime.as_str() {
                "video/mp4" => "mp4",
                "audio/mp4" => "m4a",
                "video/webm" => "webm",
                "video/x-flv" => "flv",
                "image/jpeg" => "jpg",
                "image/png" => "png",
                "image/webp" => "webp",
                _ => return Err(CoreError::Unsupported("external media type".into())),
            };
            let source = core.objects.object_path(&sha)?;
            let data = core.profile.data_root.join("open-media");
            crate::paths::no_links(&data)?;
            std::fs::create_dir_all(&data)?;
            let target = data.join(format!("{sha}.{extension}"));
            crate::paths::no_links(&target)?;
            let expected = sha.clone();
            let output = target.clone();
            tokio::task::spawn_blocking(move || -> Result<(), CoreError> {
                if crate::objects::hash_file(&source)? != expected {
                    return Err(CoreError::InvalidInput("media checksum mismatch".into()));
                }
                std::fs::copy(source, &output)?;
                if crate::objects::hash_file(&output)? != expected {
                    return Err(CoreError::InvalidInput("media copy mismatch".into()));
                }
                Ok(())
            })
            .await
            .map_err(|_| CoreError::Internal("media preparation failed".into()))??;
            // Only an allowlisted extension and a core-generated path ever reach the OS launcher.
            #[cfg(windows)]
            {
                use std::os::windows::process::CommandExt;
                std::process::Command::new("explorer.exe")
                    .arg(std::fs::canonicalize(target)?)
                    .creation_flags(0x0800_0000)
                    .spawn()?;
            }
            Ok(serde_json::json!({"opened":true}))
        }
        cmd::MEDIA_LEASE => {
            let a: ctl::MediaLeaseArgs = parse(args)?;
            let sha = crate::paths::blob_sha(&a.blob_id)?.to_owned();
            let (size, mime): (i64, Option<String>) = core
                .db
                .read({
                    let blob = format!("sha256:{sha}");
                    move |c| {
                        Ok(c.query_row(
                            "SELECT size, mime FROM blobs b WHERE blob_id=?1 AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=b.blob_id AND r.state='verified')",
                            [&blob],
                            |r| Ok((r.get(0)?, r.get(1)?)),
                        )?)
                    }
                })
                .await?;
            if core.objects.blob_size(&sha)? != size as u64 {
                return Err(CoreError::NotFound("media missing or changed".into()));
            }
            let mime2 = mime.clone();
            let lease_id = format!("lease-{}", uuid::Uuid::new_v4());
            {
                let mut leases = core.leases.lock().unwrap();
                leases.retain(|_, l| l.expires_at > std::time::Instant::now());
                if leases.len() >= 256 {
                    return Err(CoreError::InvalidInput("media lease limit".into()));
                }
            }
            core.leases.lock().unwrap().insert(
                lease_id.clone(),
                crate::core::MediaLease {
                    blob_sha: sha,
                    size,
                    mime: mime2,
                    expires_at: std::time::Instant::now()
                        + std::time::Duration::from_secs(crate::config::MEDIA_LEASE_TTL_SECS),
                },
            );
            Ok(serde_json::to_value(ctl::MediaLeaseDto {
                lease_id,
                blob_id: a.blob_id,
                size,
                mime,
                expires_at: plus_secs(&jobs::now(), crate::config::MEDIA_LEASE_TTL_SECS as i64),
            })?)
        }
        cmd::MEDIA_READ => {
            let a: ctl::MediaReadArgs = parse(args)?;
            if a.offset < 0 || a.length <= 0 || a.length > crate::config::MEDIA_CHUNK_MAX {
                return Err(CoreError::InvalidInput("length".into()));
            }
            let lease = {
                let map = core.leases.lock().unwrap();
                map.get(&a.lease_id).cloned()
            };
            let Some(lease) = lease else {
                return Err(CoreError::NotFound("lease".into()));
            };
            if lease.expires_at < std::time::Instant::now() {
                core.leases.lock().unwrap().remove(&a.lease_id);
                return Err(CoreError::NotFound("lease expired".into()));
            }
            let data =
                core.objects
                    .read_range(&lease.blob_sha, a.offset as u64, a.length as u64)?;
            let eof = (a.offset + data.len() as i64) >= lease.size;
            Ok(serde_json::to_value(ctl::MediaChunkDto {
                lease_id: a.lease_id,
                offset: a.offset,
                eof,
                data_b64: base64::engine::general_purpose::STANDARD.encode(&data),
            })?)
        }
        cmd::SEARCH_QUERY => {
            let a: ctl::SearchQueryArgs = parse(args)?;
            let results = crate::search::query(
                &core.db,
                &a.query,
                a.creator_id.as_deref(),
                a.namespace.as_deref(),
                a.cursor.as_deref(),
                a.limit.clamp(1, 200),
            )
            .await?;
            Ok(serde_json::to_value(results)?)
        }
        other => Err(CoreError::Unsupported(format!("command {other}"))),
    }
}

fn parse<T: serde::de::DeserializeOwned>(args: serde_json::Value) -> Result<T, CoreError> {
    serde_json::from_value(args).map_err(|e| CoreError::InvalidInput(format!("args: {e}")))
}

async fn daemon_status(core: &Arc<Core>) -> Result<serde_json::Value, CoreError> {
    let counts = jobs::counts(&core.db).await?;
    let usage = crate::storage_ops::usage(core).await?;
    Ok(serde_json::json!({
        "archive_id": core.profile.archive_id,
        "instance_id": core.instance_id.as_str(),
        "node_id": core.node_id.as_str(),
        "schema_version": trace_domain::SCHEMA_VERSION,
        "paused": core.paused.load(std::sync::atomic::Ordering::SeqCst),
        "started_at": core.started_at.rfc3339,
        "job_counts": counts,
        "storage": usage,
        "media_backend": {
            "ffmpeg": core.media_backend.ffmpeg.as_ref().map(|p| p.to_string_lossy().to_string()),
            "ffprobe": core.media_backend.ffprobe.as_ref().map(|p| p.to_string_lossy().to_string()),
            "version": core.media_backend.version,
        },
    }))
}

async fn creator_dto(core: &Arc<Core>, id: &str) -> Result<serde_json::Value, CoreError> {
    let id2 = id.to_string();
    core.db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT creator_id, display_name, aliases, tags, status, notes, created_at, updated_at,
                        (SELECT count(*) FROM account_bindings b WHERE b.creator_id=creators.creator_id AND b.state='active'),
                        (SELECT count(*) FROM entries e JOIN account_bindings b2 ON b2.account_id=e.account_id WHERE b2.creator_id=creators.creator_id)
                 FROM creators WHERE creator_id=?1",
                [&id2],
                |r| {
                    Ok(serde_json::json!({
                        "creator_id": r.get::<_, String>(0)?,
                        "display_name": r.get::<_, String>(1)?,
                        "aliases": r.get::<_, String>(2)?,
                        "tags": r.get::<_, String>(3)?,
                        "status": r.get::<_, String>(4)?,
                        "notes": r.get::<_, String>(5)?,
                        "created_at": r.get::<_, String>(6)?,
                        "updated_at": r.get::<_, String>(7)?,
                        "account_count": r.get::<_, i64>(8)?,
                        "entry_count": r.get::<_, i64>(9)?,
                    }))
                },
            )?)
        })
        .await
        .map_err(|_| CoreError::NotFound("creator".into()))
}

/// 单条内容的正交状态聚合。
pub(crate) fn entry_states_sync(
    c: &rusqlite::Connection,
    e: &str,
) -> Result<(String, String, String, serde_json::Value), CoreError> {
    // 访问状态：最近观察。
    let access: String = c
                .query_row(
                    "SELECT access_state FROM source_observations WHERE entry_id=?1 ORDER BY observation_seq DESC LIMIT 1",
                    [&e],
                    |r| r.get(0),
                )
                .unwrap_or_else(|_| "unknown".into());
    // 保存程度：必要资源与 verified 副本。
    let resources = c.prepare("SELECT mi.required, EXISTS(SELECT 1 FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=mi.media_id) FROM media_items mi WHERE mi.revision_id=(SELECT revision_id FROM entry_revisions WHERE entry_id=?1 ORDER BY (completeness='full') DESC,observed_at DESC LIMIT 1)")?.query_map([&e],|r|Ok(trace_domain::quality::ResourceSave{required:r.get(0)?,has_verified_blob:r.get(1)?}))?.collect::<Result<Vec<_>,_>>()?;
    let progress = serde_json::to_value(trace_domain::quality::save_progress(&resources))?
        .as_str()
        .unwrap_or("unknown")
        .to_owned();
    // No persisted target comparison exists yet: measured bytes alone do not establish quality.
    let quality = "unknown".to_string();
    let mut stmt = c.prepare(
                "SELECT role, count(*) FROM media_items mi
                 WHERE mi.revision_id = (SELECT revision_id FROM entry_revisions WHERE entry_id=?1 ORDER BY (completeness='full') DESC, observed_at DESC LIMIT 1)
                 GROUP BY role",
            )?;
    let roles: serde_json::Map<String, serde_json::Value> = stmt
        .query_map([&e], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?)))?
        .collect::<Result<Vec<_>, _>>()?
        .into_iter()
        .map(|(k, v)| (k, serde_json::Value::from(v)))
        .collect();
    Ok((access, progress, quality, serde_json::Value::Object(roles)))
}

fn plus_secs(rfc3339: &str, secs: i64) -> String {
    use time::format_description::well_known::Rfc3339;
    if let Ok(t) = time::OffsetDateTime::parse(rfc3339, &Rfc3339) {
        let t2 = t + time::Duration::seconds(secs);
        t2.format(&Rfc3339).unwrap_or_else(|_| rfc3339.to_string())
    } else {
        rfc3339.to_string()
    }
}

/// probe_auth 走适配器：更新凭证状态与能力。
async fn dispatch_probe_auth(
    core: &Arc<Core>,
    credential_id: &str,
    platform: &str,
) -> Result<(String, serde_json::Value), CoreError> {
    let result = core
        .host
        .call(
            platform,
            trace_contracts::plugin::methods::PROBE_AUTH,
            serde_json::json!({}),
            Some(20_000),
        )
        .await;
    let (status, capability) = match &result {
        Ok(v) => (
            v.get("state")
                .and_then(|s| s.as_str())
                .unwrap_or("unknown")
                .to_string(),
            v.get("capability")
                .cloned()
                .unwrap_or(serde_json::json!({})),
        ),
        Err(e) => (
            "invalid".to_string(),
            serde_json::json!({"error": e.failure_class()}),
        ),
    };
    let id = credential_id.to_string();
    let status2 = status.clone();
    let cap_json = capability.to_string();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE credentials_state SET status=?2, capability=?3, validated_at=?4, updated_at=?4 WHERE credential_id=?1",
                rusqlite::params![id, status2, cap_json, jobs::now()],
            )?;
            Ok(())
        })
        .await?;
    // 凭证有效：唤醒等待中的任务（重新授权后恢复，FR-AUTH-005）。
    if status == "valid" {
        core.db
            .write(|tx| {
                tx.execute(
                    "UPDATE jobs SET state='queued', next_run_at=?1, updated_at=?1 WHERE state='waiting_auth'",
                    [jobs::now()],
                )?;
                Ok(())
            })
            .await?;
    } else {
        core.clear_cookie(platform);
    }
    Ok((status, capability))
}
