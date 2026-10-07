//! 命令分发：控制协议 → 应用服务（INV-09：写命令幂等收据同事务）。

use crate::core::Core;
use crate::error::CoreError;
use crate::jobs;
use base64::Engine as _;
use rusqlite::OptionalExtension;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::commands as cmd;
use trace_contracts::control::{Request, Response};
use trace_domain::CONTROL_PROTOCOL_VERSION;

// Both the stop operation and its visible draining count use this same scope.
const PLATFORM_JOB_SCOPE: &str = "(subject_id IN (SELECT credential_id FROM credentials_state WHERE platform=?1) OR subject_id IN (SELECT a.account_id FROM source_accounts a WHERE a.namespace=?1) OR subject_id IN (SELECT e.entry_id FROM entries e WHERE e.namespace=?1 OR e.namespace LIKE ?1||':%') OR subject_id IN (SELECT l.session_id FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id JOIN source_accounts a ON a.account_id=s.account_id WHERE a.namespace=?1 OR a.namespace LIKE ?1||':%') OR subject_id IN (SELECT s.subscription_id FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id WHERE a.namespace=?1 OR a.namespace LIKE ?1||':%'))";

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
            | "archive.delete"
            | "archive.restore_deleted"
            | "credential.update"
            | cmd::CREDENTIAL_IMPORT
            | cmd::CREDENTIAL_REVOKE
            | cmd::PLUGIN_SET_STATE
            | cmd::EXPORT_START
            | cmd::BACKUP_START
            | cmd::RESTORE_START
            | cmd::DAEMON_PAUSE
            | cmd::DAEMON_SHUTDOWN
            | "archive.entry.recheck"
            | "upgrade.decide"
            | "job.retry_failed"
            | "quality.configure"
            | "cache.configure"
            | "cache.control"
            | "account.resolve"
            | "source.confirm"
            | "source.cancel"
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
        "source.preview" => crate::source_acquisition::preview(core, parse(args)?).await,
        "source.confirm" => crate::source_acquisition::confirm(core, parse(args)?).await,
        "source.list" => crate::source_acquisition::list(core).await,
        "source.cancel" => {
            crate::source_acquisition::cancel(
                core,
                args["request_id"]
                    .as_str()
                    .ok_or_else(|| CoreError::InvalidInput("source request ID".into()))?
                    .to_owned(),
            )
            .await
        }

        cmd::DAEMON_STATUS => daemon_status(core).await,
        cmd::DAEMON_PAUSE => {
            let paused: bool = args
                .get("paused")
                .and_then(|v| v.as_bool())
                .unwrap_or(false);
            let was_paused = core
                .paused
                .swap(paused, std::sync::atomic::Ordering::SeqCst);
            if paused && !was_paused {
                core.db.write(|tx|{
                    tx.execute("UPDATE jobs SET state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?1,updated_at=?1 WHERE state IN ('leased','running')",[jobs::now()])?;
                    Ok(())
                }).await?;
            }
            daemon_status(core).await
        }
        cmd::DAEMON_SHUTDOWN => {
            let core2 = core.clone();
            tokio::spawn(async move {
                tokio::time::sleep(std::time::Duration::from_millis(150)).await;
                core2.shutdown().await;
            });
            Ok(serde_json::json!({"accepted": true}))
        }
        "archive.delete_preview" => {
            crate::deletion::preview(
                core,
                need_str(&args, "entry_id")?,
                need_str(&args, "scope")?,
            )
            .await
        }
        "archive.delete" => {
            crate::deletion::apply(
                core,
                need_str(&args, "entry_id")?,
                need_str(&args, "scope")?,
                need_str(&args, "token")?,
            )
            .await
        }
        "archive.restore_deleted" => {
            crate::deletion::restore(
                core,
                need_str(&args, "entry_id")?,
                args["generation"]
                    .as_i64()
                    .ok_or_else(|| CoreError::InvalidInput("deletion generation".into()))?,
            )
            .await
        }
        "archive.deleted" => {
            crate::deletion::listing(core, args["after"].as_str().unwrap_or("").to_owned()).await
        }
        "health.list" => crate::issues::listing(core).await,
        "health.refs" => {
            crate::issues::refs(
                core,
                need_str(&args, "issue_key")?,
                args["after"].as_str().unwrap_or("").to_owned(),
            )
            .await
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
            if a.display_name
                .as_ref()
                .is_some_and(|s| s.trim().is_empty() || s.len() > 1024)
                || a.status
                    .as_deref()
                    .is_some_and(|s| !matches!(s, "active" | "archived"))
            {
                return Err(CoreError::InvalidInput("creator name/status".into()));
            }
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
        "account.resolve" => crate::accounts::resolve(core, args).await,
        "account.history" => {
            let id = need_str(&args, "account_id")?;
            let before = args["before"].as_i64().unwrap_or(i64::MAX);
            core.db.read(move|c|{let mut items=c.prepare("SELECT sequence,display_name,observed_at,evidence FROM account_observations WHERE account_id=?1 AND sequence<?2 ORDER BY sequence DESC LIMIT 5")?.query_map(rusqlite::params![id,before],|r|Ok(serde_json::json!({"sequence":r.get::<_,i64>(0)?,"display_name":r.get::<_,Option<String>>(1)?,"observed_at":r.get::<_,String>(2)?,"evidence":serde_json::from_str::<Value>(&r.get::<_,String>(3)?).unwrap_or(Value::Null)})))?.collect::<Result<Vec<_>,_>>()?;let more=items.len()>4;items.truncate(4);let next=if more{items.last().map(|v|v["sequence"].clone())}else{None};Ok(serde_json::json!({"items":items,"next_before":next}))}).await
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
                                (SELECT count(*) FROM subscriptions s WHERE s.account_id=a.account_id AND s.acquisition_kind='subscription')
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
            crate::quality::validate_requirement(&a.quality)?;
            let quality = serde_json::to_string(&a.quality)?;
            if !["video", "dynamic", "image", "article", "live"].contains(&a.content_type.as_str())
            {
                return Err(CoreError::InvalidInput("unsupported content type".into()));
            }
            if a.check_interval_seconds
                .is_some_and(|n| !(5..=86400).contains(&n))
            {
                return Err(CoreError::InvalidInput(
                    "subscription interval outside 5..86400 seconds".into(),
                ));
            }
            let sub_id = format!("sub-{}", uuid::Uuid::new_v4());
            let account = a.account_id.clone();
            let content = a.content_type.clone();
            let range = if a.range.is_null() {
                serde_json::json!({"kind": "from_now"})
            } else {
                a.range.clone()
            };
            crate::scan_range::Range::parse(&range, &jobs::now())?;
            let credential = a.credential_id.clone();
            let capture_chat = a.capture_chat;
            let capture_subtitles = a.capture_subtitles;
            if (capture_chat || capture_subtitles) && content != "live" {
                return Err(CoreError::InvalidInput(
                    "chat capture requires live subscription".into(),
                ));
            }
            let interval = a.check_interval_seconds.unwrap_or(if content == "live" {
                30
            } else {
                crate::config::DEFAULT_CHECK_INTERVAL_SECONDS
            });
            let sub = sub_id.clone();
            let now = jobs::now();
            core.db
                .write(move |tx| {
                    if let Some(cid)=&credential {
                        let compatible:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM credentials_state c JOIN source_accounts a ON a.namespace=c.platform WHERE c.credential_id=?1 AND a.account_id=?2 AND c.status IN ('valid','authenticated'))",rusqlite::params![cid,account],|r|r.get(0))?;
                        if !compatible{return Err(CoreError::InvalidInput("credential must be valid and belong to the subscription platform".into()));}
                    }
                    tx.execute(
                        "INSERT INTO subscriptions (subscription_id, account_id, content_type, range_json,
                         credential_id, config_version, state, check_interval_seconds, created_at, updated_at)
                         VALUES (?1,?2,?3,?4,?5,1,'enabled',?6,?7,?7)",
                        rusqlite::params![sub, account, content, range.to_string(), credential, interval, now],
                    )?;
                    tx.execute("UPDATE subscriptions SET capture_chat=?2,capture_subtitles=?3,quality_json=?4 WHERE subscription_id=?1",rusqlite::params![sub,capture_chat,capture_subtitles,quality])?;
                    for lane in if content=="live"{vec!["incremental"]}else if range["kind"]!="from_now"{vec!["incremental","history","deep"]}else{vec!["incremental","deep"]}{
                        tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,?5,'queued',?6,?6)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),format!("scan:{sub}:{lane}"),sub,serde_json::json!({"subscription_id":sub,"mode":lane}).to_string(),if lane=="incremental"{1}else{2},now])?;
                        if lane=="deep"{tx.execute("UPDATE jobs SET next_run_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','+7 days') WHERE job_key=?1",[format!("scan:{sub}:deep")])?;}
                    }
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"subscription_id": sub_id}))
        }
        cmd::SUBSCRIPTION_UPDATE => {
            let a: ctl::SubscriptionUpdateArgs = parse(args)?;
            if let Some(p) = &a.quality {
                crate::quality::validate_requirement(p)?;
            }
            let quality = a.quality.as_ref().map(serde_json::to_string).transpose()?;
            let credential = a.credential_id.clone();
            if a.check_interval_seconds
                .is_some_and(|n| !(5..=86400).contains(&n))
            {
                return Err(CoreError::InvalidInput(
                    "subscription interval outside 5..86400 seconds".into(),
                ));
            }
            if let Some(range) = &a.range {
                crate::scan_range::Range::parse(range, &jobs::now())?;
            }
            let id = a.subscription_id.clone();
            let expected_revision = req.if_revision;
            let range = a.range.clone().map(|v| v.to_string());
            let enabled = a.enabled;
            let interval = a.check_interval_seconds;
            let capture_chat = a.capture_chat;
            let capture_subtitles = a.capture_subtitles;
            core.db
                .write(move |tx| {
                    if let Some(expect) = expected_revision {
                        let current:i64=tx.query_row("SELECT config_version FROM subscriptions WHERE subscription_id=?1",[&id],|r|r.get(0))?;
                        if current != expect { return Err(CoreError::VersionConflict(format!("subscription {id}"))); }
                    }

                    if let Some(ref cid)=credential {
                        if !cid.is_empty() {
                            let compatible:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM credentials_state c JOIN source_accounts a ON a.namespace=c.platform JOIN subscriptions s ON s.account_id=a.account_id WHERE c.credential_id=?1 AND s.subscription_id=?2 AND c.status IN ('valid','authenticated'))",rusqlite::params![cid,id],|r|r.get(0))?;
                            if !compatible { return Err(CoreError::InvalidInput("credential must be valid and belong to the subscription platform".into())); }
                        }
                        tx.execute("UPDATE subscriptions SET credential_id=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",rusqlite::params![id,if cid.is_empty(){None}else{Some(cid)},jobs::now()])?;
                        tx.execute("UPDATE jobs SET state=CASE WHEN state='paused' THEN 'paused' ELSE 'queued' END,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2,updated_at=?2 WHERE json_extract(payload,'$.subscription_id')=?1 AND state!='cancelled' AND job_type IN ('scan','observe','media','record','account_profile','live_auxiliary')",rusqlite::params![id,jobs::now()])?;
                    }
                    if let Some(p)=quality {
                        tx.execute("UPDATE subscriptions SET quality_json=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",rusqlite::params![id,p,jobs::now()])?;
                        tx.execute("UPDATE jobs SET state=CASE WHEN state='paused' THEN 'paused' ELSE 'queued' END,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE json_extract(payload,'$.subscription_id')=?1 AND job_type IN ('media','record') AND state!='cancelled'",rusqlite::params![id,jobs::now()])?;
                    }
                    if let Some(r) = range {
                        tx.execute(
                            "UPDATE subscriptions SET range_json=?2, config_version=config_version+1, updated_at=?3 WHERE subscription_id=?1",
                            rusqlite::params![id, r, jobs::now()],
                        )?;
                        tx.execute("DELETE FROM scan_checkpoints WHERE subscription_id=?1",[&id])?;
                        tx.execute("DELETE FROM scan_cursor_visits WHERE subscription_id=?1",[&id])?;
                        tx.execute("UPDATE jobs SET target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,state='queued',next_run_at=?2,payload=json_remove(payload,'$.cursor') WHERE job_type='scan' AND subject_id=?1",rusqlite::params![id,jobs::now()])?;
                        let history:bool=tx.query_row("SELECT content_type!='live' AND json_extract(range_json,'$.kind')!='from_now' FROM subscriptions WHERE subscription_id=?1",[&id],|r|r.get(0))?;
                        if history {
                            tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,2,'queued',?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),format!("scan:{id}:history"),id,serde_json::json!({"subscription_id":id,"mode":"history"}).to_string(),jobs::now()])?;
                        }else{tx.execute("UPDATE jobs SET state='cancelled' WHERE job_key=?1",[format!("scan:{id}:history")])?;}
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
                    if let Some(capture) = capture_subtitles {
                        let live:bool=tx.query_row("SELECT content_type='live' FROM subscriptions WHERE subscription_id=?1",[&id],|r|r.get(0))?;
                        if !live {return Err(CoreError::InvalidInput("subtitle capture requires live subscription".into()));}
                        tx.execute("UPDATE subscriptions SET capture_subtitles=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",rusqlite::params![id,capture,jobs::now()])?;
                    }
                    if let Some(capture) = capture_chat {
                        let live:bool=tx.query_row("SELECT content_type='live' FROM subscriptions WHERE subscription_id=?1",[&id],|r|r.get(0))?;
                        if !live {return Err(CoreError::InvalidInput("chat capture requires live subscription".into()));}
                        tx.execute("UPDATE subscriptions SET capture_chat=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",rusqlite::params![id,capture,jobs::now()])?;
                        tx.execute("UPDATE jobs SET state=?2,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?3 WHERE job_type='live_auxiliary' AND json_extract(payload,'$.subscription_id')=?1 AND state!='cancelled'",rusqlite::params![id,if capture{"queued"}else{"paused"},jobs::now()])?;
                    }
                    jobs::consumers::subscription_changed(tx, &id, credential.is_some() || a.quality.is_some() || a.range.is_some())?;
                    if let Some(cid)=credential.filter(|v|!v.is_empty()) {
                        let generation:i64=tx.query_row("SELECT generation FROM credentials_state WHERE credential_id=?1",[&cid],|r|r.get(0))?;
                        crate::upgrades::schedule(tx,&cid,generation)?;
                    }
                    Ok(())
                })
                .await?;
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::SUBSCRIPTION_CHECK_NOW => {
            let a: ctl::SubscriptionCheckNowArgs = parse(args)?;
            let sub = a.subscription_id.clone();
            let job_id=core.db.write(move|tx|{
                let ts=jobs::now();let key=format!("scan:{sub}:incremental");
                tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,0,'queued',?5,?5) ON CONFLICT(job_key) DO UPDATE SET next_run_at=excluded.updated_at,state='queued' WHERE jobs.state NOT IN ('running','leased','paused','cancelled')",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,sub,serde_json::json!({"subscription_id":sub,"mode":"incremental"}).to_string(),ts])?;
                Ok(tx.query_row("SELECT job_id FROM jobs WHERE job_key=?1",[key],|r|r.get::<_,String>(0))?)
            }).await?;
            Ok(serde_json::json!({"job_id": job_id}))
        }
        cmd::SUBSCRIPTION_LIST => {
            let a: ctl::SubscriptionListArgs = parse(args)?;
            let creator = a.creator_id.clone();
            let account = a.account_id.clone();
            let items: Vec<serde_json::Value> = core
                .db
                .read(move |c| {
                    let mut stmt = c.prepare(
                        "SELECT s.subscription_id, s.account_id, s.content_type, s.range_json, s.state,
                                s.config_version, s.credential_id, s.updated_at, s.capture_chat, s.capture_subtitles,s.quality_json,s.check_interval_seconds,
                                (SELECT MIN(j.next_run_at) FROM jobs j WHERE j.subject_id=s.subscription_id AND j.job_type='scan' AND j.state NOT IN ('cancelled','paused')),
                                (SELECT json_group_array(json_object('scan_type',p.scan_type,'coverage',json(p.coverage))) FROM scan_checkpoints p WHERE p.subscription_id=s.subscription_id)
                         FROM subscriptions s
                         WHERE s.acquisition_kind='subscription' AND (?1 IS NULL OR EXISTS (
                            SELECT 1 FROM account_bindings b WHERE b.account_id=s.account_id AND b.creator_id=?1)) AND (?2 IS NULL OR s.account_id=?2)",
                    )?;
                    let rows = stmt
                        .query_map(rusqlite::params![creator,account], |r| {
                            Ok(serde_json::json!({
                                "subscription_id": r.get::<_, String>(0)?,
                                "account_id": r.get::<_, String>(1)?,
                                "content_type": r.get::<_, String>(2)?,
                                "range": r.get::<_, String>(3).ok().and_then(|v|serde_json::from_str::<serde_json::Value>(&v).ok()).unwrap_or_default(),
                                "state": r.get::<_, String>(4)?,
                                "config_version": r.get::<_, i64>(5)?,
                                "credential_id": r.get::<_, Option<String>>(6)?,
                                "updated_at": r.get::<_, String>(7)?,
                                "capture_chat": r.get::<_, bool>(8)?,
                                "capture_subtitles": r.get::<_, bool>(9)?,
                                "check_interval_seconds":r.get::<_,i64>(11)?,
                                "next_check_at":r.get::<_,Option<String>>(12)?,
                                "coverage":r.get::<_,String>(13).ok().and_then(|v|serde_json::from_str::<serde_json::Value>(&v).ok()).unwrap_or_default(),
                                "quality": r.get::<_,String>(10).ok().and_then(|v|serde_json::from_str::<serde_json::Value>(&v).ok()).unwrap_or_default(),
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
        "archive.acquisitions" => crate::acquisitions::list(core, args).await,
        "upgrade.list" => crate::upgrades::list(core, args).await,
        "upgrade.resources" => crate::upgrades::resources(core, args).await,
        "upgrade.decide" => {
            let choices = if args.get("items").is_some() {
                parse(args["items"].clone())?
            } else {
                vec![parse(args)?]
            };
            crate::upgrades::decide(core, choices).await
        }

        cmd::ARCHIVE_ENTRY_GET => {
            let a: ctl::ArchiveEntryGetArgs = parse(args)?;
            let entry = a.entry_id.clone();
            let mut result = super::archive::entry(&core.db, a.entry_id).await?;
            result["authentication"] = core
                .db
                .read(move |c| crate::acquisitions::summary(c, &entry))
                .await?;
            Ok(result)
        }
        "cache.status" => crate::cache::status(&core.db).await,
        "cache.configure" => {
            crate::cache::configure(
                core,
                args["index_budget_bytes"]
                    .as_i64()
                    .ok_or_else(|| CoreError::InvalidInput("index budget".into()))?,
                args["derived_budget_bytes"]
                    .as_i64()
                    .ok_or_else(|| CoreError::InvalidInput("derived budget".into()))?,
            )
            .await
        }
        "cache.control" => {
            crate::cache::control(
                core,
                args["scope"].as_str().unwrap_or_default().to_owned(),
                args["rebuild"].as_bool().unwrap_or(false),
            )
            .await
        }
        "archive.resources" => super::archive::resources(&core.db, parse(args)?).await,
        "archive.entry.page" => super::archive::page(&core.db, parse(args)?).await,
        "archive.revision.read" => super::archive::read(&core.db, parse(args)?).await,
        "archive.observation.read" => {
            super::archive::observation(
                &core.db,
                need_str(&args, "entry_id")?,
                args["seq"]
                    .as_i64()
                    .ok_or_else(|| CoreError::InvalidInput("observation sequence".into()))?,
                args["offset"].as_u64().unwrap_or(0),
            )
            .await
        }
        "archive.entry.recheck" => {
            super::archive::recheck(&core.db, need_str(&args, "entry_id")?).await
        }
        "job.retry_failed" => {
            let ids: Vec<String> = parse(args["job_ids"].clone())?;
            if ids.is_empty() || ids.len() > 100 {
                return Err(CoreError::InvalidInput("retry batch size".into()));
            }
            let count=core.db.write(move|tx|{let mut count=0;for id in ids {
                count+=tx.execute("UPDATE jobs SET state='queued',target_generation=target_generation+1,attempts=0,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2,updated_at=?2 WHERE job_id=?1 AND state IN ('failed','waiting_retry','waiting_storage','waiting_auth','waiting_plugin','waiting_user')",rusqlite::params![id,jobs::now()])?;
            }Ok(count)}).await?;
            Ok(serde_json::json!({"requeued":count}))
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
                    jobs::consumers::retry(tx, &id)?;
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
            let cancelled: bool = core.db.write(move |tx| jobs::cancel_work(tx, &id)).await?;
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
                                "status": ({let status=r.get::<_,String>(3)?;if status=="authenticated"{"valid".to_owned()}else{status}}),
                                "capability": r.get::<_, String>(4).ok().and_then(|v|serde_json::from_str::<serde_json::Value>(&v).ok()).unwrap_or_default(),
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
            core.cache_cookie(&format!("{}/{}", a.platform, credential_id), a.secret);
            // 探测能力。
            let validate = dispatch_probe_auth(core, &credential_id, &a.platform).await;
            match validate {
                Ok(cap) => Ok(
                    serde_json::json!({"credential_id": credential_id, "status": cap.0, "capability": cap.1}),
                ),
                Err(e) => Ok(
                    serde_json::json!({"credential_id": credential_id, "status": "unknown", "error": e.failure_class()}),
                ),
            }
        }
        "credential.update" => {
            let id = need_str(&args, "credential_id")?;
            let secret = args["secret"]
                .as_str()
                .filter(|s| !s.is_empty() && s.len() <= 65536)
                .ok_or_else(|| CoreError::InvalidInput("credential text".into()))?
                .to_owned();
            let cid = id.clone();
            let (platform,old_ref,generation):(String,String,i64)=core.db.read(move|c|Ok(c.query_row("SELECT platform,vault_ref,generation FROM credentials_state WHERE credential_id=?1",[cid],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?)).await?;
            let new_ref = format!("vault-{}", uuid::Uuid::new_v4());
            core.vault.store(&new_ref, secret.as_bytes())?;
            let cid = id.clone();
            let reference = new_ref.clone();
            let swapped=core.db.write(move|tx|{
                let n=tx.execute("UPDATE credentials_state SET vault_ref=?2,generation=generation+1,status='unknown',validated_at=NULL,capability='{}',updated_at=?3 WHERE credential_id=?1 AND generation=?4",rusqlite::params![cid,reference,jobs::now(),generation])?;
                if n!=1{return Err(CoreError::VersionConflict("credential changed during update".into()));}
                tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE job_type='credential_probe' AND subject_id=?1",[&cid])?;
                tx.execute("UPDATE jobs SET state='waiting_auth',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?1) AND state NOT IN ('cancelled','paused','succeeded')",rusqlite::params![cid,jobs::now()])?;
                jobs::consumers::credential_changed(tx, &cid, false)?;
                Ok(())
            }).await;
            if let Err(e) = swapped {
                let _ = core.vault.remove(&new_ref);
                return Err(e);
            }
            core.clear_cookie(&format!("{platform}/{id}"));
            core.host.kill_session(&format!("{platform}/{id}")).await;
            core.vault.remove(&old_ref)?;
            let (status, capability) = dispatch_probe_auth(core, &id, &platform).await?;
            Ok(serde_json::json!({"credential_id":id,"status":status,"capability":capability}))
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
            let cid = a.credential_id.clone();
            let reference: String = core
                .db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT vault_ref FROM credentials_state WHERE credential_id=?1",
                        [cid],
                        |r| r.get(0),
                    )?)
                })
                .await?;
            let id = a.credential_id.clone();
            core.db
                .write(move |tx| {
                    tx.execute("UPDATE jobs SET state=CASE WHEN job_type='credential_probe' THEN 'cancelled' ELSE 'waiting_auth' END,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE (subject_id=?1 AND job_type='credential_probe' OR json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?1)) AND state NOT IN ('succeeded','cancelled','paused')",[&id])?;
                    tx.execute(
                        "DELETE FROM credentials_state WHERE credential_id=?1",
                        [&id],
                    )?;
                    jobs::consumers::credential_changed(tx, &id, false)?;
                    Ok(())
                })
                .await?;
            core.clear_cookie(&format!("{platform}/{}", a.credential_id));
            core.host
                .kill_session(&format!("{platform}/{}", a.credential_id))
                .await;
            // 撤销使等待凭证的任务进入 waiting_auth（只影响关联工作，不毁历史）。
            core.vault.remove(&reference)?;
            let revoked = a.credential_id.clone();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "UPDATE jobs SET state='waiting_auth',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?1 WHERE state IN ('queued','leased','running','waiting_retry','waiting_auth') AND (json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?2))",
                        rusqlite::params![jobs::now(),revoked],
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
                                active_tasks: 0,
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
            // Include every registered platform even when only one has saved state.
            let mut items = items;
            {
                for spec in crate::pluginhost::default_adapter_specs(&core.profile) {
                    if items.iter().any(|i| i.plugin_id == spec.plugin_id) {
                        continue;
                    }
                    let state = core.host.session_state(&spec.plugin_id).await;
                    items.push(ctl::PluginDto {
                        active_tasks: 0,
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
                            "probe_auth".into(),
                            "probe_live".into(),
                            "sample_live_auxiliary".into(),
                        ],
                    });
                }
            }
            let active = core.active_jobs.lock().unwrap().clone();
            let items = core
                .db
                .read(move |c| {
                    for item in &mut items {
                        let sql = format!("SELECT job_id FROM jobs WHERE {PLATFORM_JOB_SCOPE}");
                        let ids = c
                            .prepare(&sql)?
                            .query_map([&item.platform], |r| r.get::<_, String>(0))?
                            .collect::<Result<Vec<_>, _>>()?;
                        item.active_tasks = ids
                            .iter()
                            .map(|id| active.get(id).copied().unwrap_or(0))
                            .sum();
                        if item.state == "disabled" && item.active_tasks > 0 {
                            item.state = "stopping".into();
                        }
                    }
                    Ok(items)
                })
                .await?;
            Ok(serde_json::json!({ "items": items }))
        }
        cmd::PLUGIN_SET_STATE => {
            let a: ctl::PluginSetStateArgs = parse(args)?;
            if !matches!(a.state.as_str(), "enabled" | "disabled") {
                return Err(CoreError::InvalidInput("plugin state".into()));
            }
            let enabled = a.state == "enabled";
            let platform = a.plugin_id.clone();
            if !crate::pluginhost::default_adapter_specs(&core.profile)
                .iter()
                .any(|s| s.platform == platform)
            {
                return Err(CoreError::NotFound("plugin".into()));
            }
            let host_platform = platform.clone();
            if enabled {
                core.host.set_enabled(&platform, true);
            }
            core.db.write(move|tx|{
                let ts=jobs::now();
                tx.execute("INSERT INTO plugins_state(plugin_id,platform,version,state,protocol,binary_path,enabled,updated_at) VALUES(?1,?1,?2,?3,?4,'',?5,?6) ON CONFLICT(plugin_id) DO UPDATE SET enabled=excluded.enabled,state=excluded.state,updated_at=excluded.updated_at",rusqlite::params![platform,env!("CARGO_PKG_VERSION"),if enabled{"enabled"}else{"disabled"},trace_domain::PLUGIN_PROTOCOL_VERSION,enabled as i64,ts])?;
                let scope=PLATFORM_JOB_SCOPE;
                let sql=if enabled{format!("UPDATE jobs SET state='queued',next_run_at=?2,updated_at=?2 WHERE state='waiting_plugin' AND {scope}")}else{format!("UPDATE jobs SET state='waiting_plugin',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE job_type IN ('scan','observe','media','record','account_profile','live_auxiliary','credential_probe','auth_completion') AND state IN ('queued','leased','running','waiting_retry') AND {scope}")};
                tx.execute(&sql,rusqlite::params![platform,ts])?;
                Ok(())
            }).await?;
            if !enabled {
                core.host.set_enabled(&host_platform, false);
                core.host.kill_session(&host_platform).await;
            }
            Ok(serde_json::json!({"ok": true}))
        }
        cmd::STORAGE_USAGE => crate::storage_ops::usage(core).await,
        "live.fragments" => {
            let sid = args["session_id"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("session identity".into()))?
                .to_owned();
            let before = args["before"].as_i64().unwrap_or(i64::MAX);
            core.db.read(move|c|{
                let mut items=c.prepare("SELECT f.sequence,f.state,f.start_ms,f.end_ms,f.reason,v.blob_id,b.mime,EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=v.blob_id AND rp.state='verified'),COALESCE(m.acquisition_evidence,'{}'),f.captured_at,v.created_at FROM live_fragments f LEFT JOIN media_items m ON m.media_id=f.media_id LEFT JOIN asset_selections s ON s.media_id=m.media_id LEFT JOIN media_variants v ON v.variant_id=s.variant_id LEFT JOIN blobs b ON b.blob_id=v.blob_id WHERE f.session_id=?1 AND f.sequence<?2 AND COALESCE(m.role,'')!='recording_source' ORDER BY f.sequence DESC LIMIT 101")?.query_map(rusqlite::params![sid,before],|r|Ok(serde_json::json!({"sequence":r.get::<_,i64>(0)?,"state":r.get::<_,String>(1)?,"start_ms":r.get::<_,Option<i64>>(2)?,"end_ms":r.get::<_,Option<i64>>(3)?,"reason":r.get::<_,Option<String>>(4)?,"blob_id":r.get::<_,Option<String>>(5)?,"mime":r.get::<_,Option<String>>(6)?,"available":r.get::<_,bool>(7)?,"quality":r.get::<_,String>(8)?.parse::<serde_json::Value>().unwrap_or_default(),"captured_at":r.get::<_,Option<String>>(9)?,"saved_at":r.get::<_,Option<String>>(10)?})))?.collect::<Result<Vec<_>,_>>()?;
                for item in &mut items {
                    let labels=crate::acquisitions::fragment(c,&sid,item["sequence"].as_i64().unwrap_or(0))?;
                    item["authentication"]=labels["authentication"].clone();item["actual_spec"]=labels["actual_spec"].clone();
                }
                let more=items.len()>100;items.truncate(100);let next=more.then(||items.last().unwrap()["sequence"].clone());
                let total:i64=c.query_row("SELECT count(*) FROM live_fragments f LEFT JOIN media_items m ON m.media_id=f.media_id WHERE f.session_id=?1 AND COALESCE(m.role,'')!='recording_source'",[sid],|r|r.get(0))?;
                Ok(serde_json::json!({"items":items,"next_before":next,"total":total}))
            }).await
        }
        "quality.pin" => {
            let _gate = core.archive_access.read().await;
            let variant = args["variant_id"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("variant identity".into()))?
                .to_owned();
            let enabled = args["enabled"]
                .as_bool()
                .ok_or_else(|| CoreError::InvalidInput("pin state".into()))?;
            core.db
                .write(move |tx| crate::quality::pin(tx, &variant, enabled))
                .await?;
            Ok(serde_json::json!({"pinned":enabled}))
        }
        "quality.recycle" => crate::quality::recycle(core).await,
        "quality.list" => {
            crate::quality::listing(
                core,
                need_str(&args, "entry_id")?,
                args["after"].as_i64().unwrap_or(0),
            )
            .await
        }
        "quality.configure" => {
            let prefer = args["prefer_fps"]
                .as_bool()
                .ok_or_else(|| CoreError::InvalidInput("quality preference".into()))?;
            let wait = args["wait_seconds"]
                .as_i64()
                .filter(|n| (0..=2592000).contains(n))
                .ok_or_else(|| CoreError::InvalidInput("recycle wait".into()))?;
            let entry = need_str(&args, "entry_id")?;
            core.db.write(move|tx|{
                let policy=serde_json::json!({"prefer_fps":prefer,"wait_seconds":wait,"version":"m1-v1"});
                tx.execute("INSERT INTO meta(key,value) VALUES('quality_policy',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value",[policy.to_string()])?;
                let revision:String=tx.query_row("SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1",[&entry],|r|r.get(0))?;
                crate::quality::evaluate(tx,&revision,prefer,wait)?;
                  crate::quality::schedule(tx,true)?;
                crate::objects::outbox_emit(tx,"entry.quality_changed",&entry,&serde_json::json!({"policy":policy}))?;Ok(())
            }).await?;
            Ok(serde_json::json!({"updated":true}))
        }
        "live.auxiliary" => {
            let session = args["session_id"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("session id".into()))?
                .to_owned();
            crate::recording::auxiliary::page(core, session, args["before"].as_i64()).await
        }
        "live.set_recording" => {
            let sid = args["session_id"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("session id".into()))?
                .to_owned();
            let enabled = args["enabled"]
                .as_bool()
                .ok_or_else(|| CoreError::InvalidInput("recording state".into()))?;
            let expected = args["generation"].as_i64();
            core.db.write(move|tx|{
                let (generation,stopped):(i64,i64)=tx.query_row("SELECT generation,manual_stopped FROM live_sessions WHERE session_id=?1",[&sid],|r|Ok((r.get(0)?,r.get(1)?)))?;
                if expected.is_some_and(|e|e!=generation){return Err(CoreError::VersionConflict("live session".into()));}
                if stopped == i64::from(!enabled){return Ok(());}
                tx.execute("UPDATE live_sessions SET manual_stopped=?2,state=?3,generation=generation+1,stop_reason=?4 WHERE session_id=?1",rusqlite::params![sid,i64::from(!enabled),if enabled{"waiting"}else{"stopping"},if enabled{"manual_start"}else{"manual_stop"}])?;
                if enabled{tx.execute("UPDATE jobs SET state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE job_key=?1",rusqlite::params![format!("record:{sid}"),jobs::now()])?;}
                crate::objects::outbox_emit(tx,"live.updated",&sid,&serde_json::json!({"recording_enabled":enabled}))?;Ok(())
            }).await?;
            Ok(serde_json::json!({"ok":true}))
        }
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
            let (ns,kind,key):(String,String,String)=core.db.read(move|c|Ok(c.query_row("SELECT namespace,native_kind,native_entry_key FROM entries WHERE entry_id=?1",[a.entry_id],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?)).await?;
            let url = crate::feed::source_url(&ns, &kind, &key)
                .ok_or_else(|| CoreError::Unsupported("source URL unavailable".into()))?;
            #[cfg(windows)]
            {
                use std::os::windows::process::CommandExt;
                std::process::Command::new("explorer.exe")
                    .arg(url)
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
            let _gate = core.archive_access.read().await;
            let a: ctl::MediaLeaseArgs = parse(args)?;
            let sha = crate::paths::blob_sha(&a.blob_id)?.to_owned();
            let blob = a.blob_id;
            let mime:String=core.db.read(move |c|Ok(c.query_row("SELECT mime FROM blobs b WHERE blob_id=?1 AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=b.blob_id AND r.state='verified')",[blob],|r|r.get(0))?)).await?;
            let extension = match mime.as_str() {
                "video/mp4" => "mp4",
                "audio/mp4" => "m4a",
                "video/webm" => "webm",
                "video/x-flv" => "flv",
                "video/mp2t" => "ts",
                "audio/aac" => "aac",
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
            let _gate = core.archive_access.read().await;
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
            let results = crate::search::query(&core.db, a).await?;
            Ok(serde_json::to_value(results)?)
        }
        other => Err(CoreError::Unsupported(format!("command {other}"))),
    }
}

fn parse<T: serde::de::DeserializeOwned>(args: serde_json::Value) -> Result<T, CoreError> {
    serde_json::from_value(args).map_err(|e| CoreError::InvalidInput(format!("args: {e}")))
}
fn need_str(args: &serde_json::Value, key: &str) -> Result<String, CoreError> {
    args[key]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= 4096)
        .map(str::to_owned)
        .ok_or_else(|| CoreError::InvalidInput(format!("missing {key}")))
}

async fn daemon_status(core: &Arc<Core>) -> Result<serde_json::Value, CoreError> {
    let counts = jobs::counts(&core.db).await?;
    let usage = crate::storage_ops::usage(core).await?;
    Ok(serde_json::json!({
        "archive_id": core.profile.archive_id,
        "instance_id": core.instance_id.as_str(),
        "node_id": core.node_id.as_str(),
        "schema_version": trace_domain::SCHEMA_VERSION,
        "acquisition_budget":core.http.budget_snapshot(),
        "paused": core.paused.load(std::sync::atomic::Ordering::SeqCst),
        "started_at": core.started_at.rfc3339,
        "job_counts": counts,
        "budgets":{"metadata_min_interval_ms":core.profile.metadata_interval_ms(),"metadata_jitter_ms":core.profile.metadata_jitter_ms(),"scan_page_budget":core.profile.scan_page_budget(),"live_concurrency":core.profile.live_concurrency.clamp(1,4),"check_jitter_percent":5},
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
    core.db.read(move |c| {
        let dto = c.query_row(
            "SELECT creator_id, display_name, aliases, tags, status, notes, created_at, updated_at,
                    (SELECT count(*) FROM account_bindings b WHERE b.creator_id=creators.creator_id AND b.state='active'),
                    (SELECT count(*) FROM entries e JOIN account_bindings b2 ON b2.account_id=e.account_id WHERE b2.creator_id=creators.creator_id)
             FROM creators WHERE creator_id=?1", [&id2], |r| {
                let list = |column: usize| -> rusqlite::Result<Vec<String>> {
                    let raw: String = r.get(column)?;
                    serde_json::from_str(&raw).map_err(|error| rusqlite::Error::FromSqlConversionFailure(column,rusqlite::types::Type::Text,Box::new(error)))
                };
                Ok(ctl::CreatorDto {
                    creator_id:r.get(0)?, display_name:r.get(1)?, aliases:list(2)?, tags:list(3)?,
                    status:r.get(4)?, notes:r.get(5)?, created_at:r.get(6)?, updated_at:r.get(7)?,
                    account_count:r.get(8)?, entry_count:r.get(9)?,
                })
            }).optional()?.ok_or_else(|| CoreError::NotFound("creator".into()))?;
        Ok(serde_json::to_value(dto)?)
    }).await
}

/// 单条内容的正交状态聚合。
pub(crate) fn entry_states_sync(
    c: &rusqlite::Connection,
    e: &str,
) -> Result<(String, String, String, serde_json::Value), CoreError> {
    // 访问状态：最近观察。
    let access: String = c
                .prepare_cached("SELECT access_state FROM source_observations WHERE entry_id=?1 ORDER BY observation_seq DESC LIMIT 1")
                .and_then(|mut statement| statement.query_row([&e], |r| r.get(0)))
                .unwrap_or_else(|_| "unknown".into());
    // 保存程度：必要资源与 verified 副本。
    let resources = c.prepare_cached("SELECT mi.required, EXISTS(SELECT 1 FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=mi.media_id) FROM media_items mi WHERE mi.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)")?.query_map([&e],|r|Ok(trace_domain::quality::ResourceSave{required:r.get(0)?,has_verified_blob:r.get(1)?}))?.collect::<Result<Vec<_>,_>>()?;
    let mut progress = serde_json::to_value(trace_domain::quality::save_progress(&resources))?
        .as_str()
        .unwrap_or("unknown")
        .to_owned();
    let body_complete: bool=c.prepare_cached("SELECT completeness='full' FROM entry_revisions WHERE entry_id=?1 ORDER BY (completeness='full') DESC,observed_at DESC LIMIT 1").and_then(|mut statement| statement.query_row([e],|r|r.get(0))).unwrap_or(false);
    let text_kind: bool = c
        .prepare_cached(
            "SELECT native_kind IN ('dynamic','opus','article') FROM entries WHERE entry_id=?1",
        )
        .and_then(|mut statement| statement.query_row([e], |r| r.get(0)))
        .unwrap_or(false);
    if !body_complete && progress == "complete" {
        progress = "partial".into();
    }
    if body_complete && resources.is_empty() && text_kind {
        progress = "complete".into();
    }
    let live_incomplete:bool=c.prepare_cached("SELECT EXISTS(SELECT 1 FROM live_sessions l WHERE l.entry_id=?1 AND (l.state!='sealed' OR EXISTS(SELECT 1 FROM live_fragments f WHERE f.session_id=l.session_id AND f.state NOT IN ('sealed','superseded','source'))))")?.query_row([e],|r|r.get(0))?;
    if live_incomplete && progress == "complete" {
        progress = "partial".into();
    }
    let quality = crate::quality::state(c, e)?;
    let mut stmt = c.prepare_cached(
                "SELECT role, count(*) FROM media_items mi
                 WHERE mi.revision_id = (SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)
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
    let result = probe_auth_guarded(core, credential_id, platform, None).await?;
    let id = credential_id.to_owned();
    let p = platform.to_owned();
    core.db.write(move|tx|{
        // Read the current binding and state in the scheduling transaction: a
        // simultaneous credential replacement must not inherit an old result.
        let current=tx.query_row("SELECT generation,status FROM credentials_state WHERE credential_id=?1",[&id],|r|Ok((r.get::<_,i64>(0)?,r.get::<_,String>(1)?)));
        let Ok((generation,status))=current else{return Ok(());};
        if status=="invalid" {return Ok(());}
        let key=format!("credential-probe:{id}:{generation}");
        let next=plus_secs(&jobs::now(),if status=="valid"{21600}else{60});
        tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) VALUES(?1,?2,'credential_probe',?3,?4,2,'queued',?5,?6,?6) ON CONFLICT(job_key) DO UPDATE SET state='queued',next_run_at=excluded.next_run_at WHERE jobs.state NOT IN ('running','leased','cancelled','paused')",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,id,serde_json::json!({"credential_id":id,"credential_generation":generation,"platform":p}).to_string(),next,jobs::now()])?;Ok(())
    }).await?;
    Ok(result)
}

pub(crate) async fn probe_credential_job(
    core: &Arc<Core>,
    job: &jobs::ClaimedJob,
) -> Result<(), CoreError> {
    let platform = job.payload["platform"]
        .as_str()
        .ok_or_else(|| CoreError::InvalidInput("credential platform".into()))?;
    core.metadata_pacing(platform).await;
    let (status, _) =
        probe_auth_guarded(core, &job.subject_id, platform, Some(job.clone())).await?;
    match status.as_str() {
        "valid" => {
            let j = job.clone();
            core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE jobs SET state='queued',next_run_at=?2,lease_owner=NULL,lease_expires_at=NULL,attempts=0 WHERE job_id=?1",rusqlite::params![j.job_id,plus_secs(&jobs::now(),21600)])?;crate::issues::resolved_job(tx,&j.job_id)?;Ok(())}).await
        }
        "invalid" => Err(CoreError::AuthRequired(
            "credential validation failed".into(),
        )),
        _ => {
            let j = job.clone();
            core.db.write(move|tx|{j.check(tx)?;let delay=60_i64.saturating_mul(1_i64<<tx.query_row("SELECT attempts FROM jobs WHERE job_id=?1",[&j.job_id],|r|r.get::<_,u32>(0))?.min(6));tx.execute("UPDATE jobs SET state='waiting_retry',next_run_at=?2,lease_owner=NULL,lease_expires_at=NULL,last_error=?3 WHERE job_id=?1",rusqlite::params![j.job_id,plus_secs(&jobs::now(),delay.min(3600)),serde_json::json!({"class":"transient_network","message":"credential validation unavailable"}).to_string()])?;Ok(())}).await
        }
    }
}
async fn probe_auth_guarded(
    core: &Arc<Core>,
    credential_id: &str,
    platform: &str,
    guard: Option<jobs::ClaimedJob>,
) -> Result<(String, serde_json::Value), CoreError> {
    if core.cancel.is_cancelled() {
        return Err(CoreError::Cancelled);
    }
    if let Some(job) = guard.clone() {
        core.db.read(move |c| job.check(c)).await?;
    }
    let cid = credential_id.to_owned();
    let (reference, generation): (String, i64) = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT vault_ref,generation FROM credentials_state WHERE credential_id=?1",
                [cid],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await?;
    if guard
        .as_ref()
        .is_some_and(|j| j.payload["credential_generation"] != generation)
    {
        return Err(CoreError::Cancelled);
    }
    let secret = String::from_utf8(core.vault.load(&reference)?)
        .map_err(|_| CoreError::AuthRequired("credential encoding".into()))?;
    let credential_scope = format!("{credential_id}/g{generation}");
    core.cache_cookie(&format!("{platform}/{credential_scope}"), secret);
    let result = core
        .host
        .call_scoped(
            platform,
            Some(&credential_scope),
            trace_contracts::plugin::methods::PROBE_AUTH,
            serde_json::json!({}),
            Some(20_000),
        )
        .await;
    let (status, capability) = classify_auth(&result);
    let id = credential_id.to_string();
    let status2 = status.clone();
    let cap_json = capability.to_string();
    core.db
        .write(move |tx| {
            if let Some(j)=guard {j.check(tx)?;}
            tx.execute(
                "UPDATE credentials_state SET status=?2, capability=?3, validated_at=?4, updated_at=?4 WHERE credential_id=?1 AND generation=?5",
                rusqlite::params![id, status2, cap_json, jobs::now(),generation],
            ).and_then(|n|if n==1{Ok(n)}else{Err(rusqlite::Error::QueryReturnedNoRows)})?;
            if status2=="invalid" {
                tx.execute("UPDATE jobs SET state='waiting_auth',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?1) AND state NOT IN ('succeeded','failed','cancelled','paused')",[&id])?;
                jobs::consumers::credential_changed(tx,&id,false)?;
            }
            Ok(())
        })
        .await?;
    // 凭证有效：唤醒等待中的任务（重新授权后恢复，FR-AUTH-005）。
    if status == "valid" {
        let credential = credential_id.to_owned();
        core.db
            .write(move |tx| {
                let valid:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM credentials_state WHERE credential_id=?1 AND generation=?2 AND status='valid')",rusqlite::params![credential,generation],|r|r.get(0))?;
                if !valid {return Err(CoreError::Cancelled);}
                tx.execute(
                    "UPDATE jobs SET state='queued', next_run_at=?1, updated_at=?1 WHERE state='waiting_auth' AND json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?2 AND state='enabled' AND acquisition_kind='subscription')",
                    rusqlite::params![jobs::now(),credential],
                )?;
                jobs::consumers::credential_changed(tx, &credential, true)?;
                crate::upgrades::schedule(tx,&credential,generation)?;
                tx.execute("UPDATE jobs SET state='queued',target_generation=target_generation+1,next_run_at=?1,updated_at=?1 WHERE job_type='media' AND state='succeeded' AND json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?2 AND state='enabled' AND acquisition_kind='subscription') AND EXISTS(SELECT 1 FROM entry_revisions r JOIN media_items m ON m.revision_id=r.revision_id WHERE r.entry_id=jobs.subject_id AND (json_extract(m.acquisition_evidence,'$.capability_decreased')=1 OR json_extract(m.acquisition_evidence,'$.below_target')=1 OR (json_extract(m.acquisition_evidence,'$.requested_quality.min_height') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM asset_selections x JOIN media_variants v ON v.variant_id=x.variant_id JOIN replicas p ON p.blob_id=v.blob_id AND p.state='verified' WHERE x.media_id=m.media_id AND json_extract(v.measured_spec,'$.height')>=json_extract(m.acquisition_evidence,'$.requested_quality.min_height'))) OR (json_extract(m.acquisition_evidence,'$.requested_quality.min_fps') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM asset_selections x JOIN media_variants v ON v.variant_id=x.variant_id JOIN replicas p ON p.blob_id=v.blob_id AND p.state='verified' WHERE x.media_id=m.media_id AND json_extract(v.measured_spec,'$.fps')>=json_extract(m.acquisition_evidence,'$.requested_quality.min_fps')))))",rusqlite::params![jobs::now(),credential])?;
                Ok(())
            })
            .await?;
    } else {
        core.clear_cookie(&format!("{platform}/{credential_id}"));
    }
    Ok((status, capability))
}

fn classify_auth(result: &Result<serde_json::Value, CoreError>) -> (String, serde_json::Value) {
    match result {
        Ok(value) => (
            match value["state"].as_str() {
                Some("valid" | "authenticated") => "valid",
                Some("invalid" | "anonymous") => "invalid",
                _ => "unknown",
            }
            .into(),
            {
                let mut capabilities = serde_json::json!({"session_access":if matches!(value["state"].as_str(),Some("valid"|"authenticated")){"valid"}else if matches!(value["state"].as_str(),Some("invalid"|"anonymous")){"invalid"}else{"unknown"},"original_image":"unknown","video_quality":"unknown","membership_content":"unknown","live_access":"unknown"});
                if let Some(fields) =
                    trace_contracts::content::safe_metadata(&value["capability"]).as_object()
                {
                    capabilities.as_object_mut().unwrap().extend(fields.clone());
                }
                capabilities
            },
        ),
        Err(error) => (
            if matches!(
                error.failure_class(),
                trace_domain::errors::FailureClass::AuthRequired
            ) {
                "invalid"
            } else {
                "unknown"
            }
            .into(),
            serde_json::json!({"error":error.failure_class()}),
        ),
    }
}

#[cfg(test)]
mod auth_tests {
    use super::*;
    #[test]
    fn c06_authentication_is_not_media_permission_or_network_health() {
        for state in ["valid", "authenticated"] {
            let (status, cap) = classify_auth(&Ok(
                serde_json::json!({"state":state,"capability":{"media_access":"unknown","token":"PRIVATE_AUTH_SENTINEL"}}),
            ));
            assert_eq!(status, "valid");
            assert_eq!(cap["media_access"], "unknown");
            assert!(!cap.to_string().contains("PRIVATE_AUTH_SENTINEL"));
        }
        assert_eq!(
            classify_auth(&Ok(serde_json::json!({"state":"anonymous"}))).0,
            "invalid"
        );
        assert_eq!(
            classify_auth(&Err(CoreError::Network("offline".into()))).0,
            "unknown"
        );
        assert_eq!(
            classify_auth(&Err(CoreError::Adapter(
                trace_domain::errors::FailureClass::PermissionDenied
            )))
            .0,
            "unknown"
        );
        assert_eq!(
            classify_auth(&Err(CoreError::AuthRequired("expired".into()))).0,
            "invalid"
        );
    }
}
