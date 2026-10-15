//! Source identity is stable; profile changes are append-only observations.
use crate::{core::Core, error::CoreError};
use rusqlite::OptionalExtension;
use serde_json::{Value, json};
use trace_contracts::plugin::ResolvedAccount;

pub fn persist(
    tx: &rusqlite::Transaction<'_>,
    namespace: &str,
    account: ResolvedAccount,
) -> Result<Value, CoreError> {
    let key = &account.native_account_key;
    let valid = match namespace {
        "bilibili" => !key.is_empty() && key.len() <= 32 && key.bytes().all(|b| b.is_ascii_digit()),
        "douyin" => {
            key.starts_with("MS4")
                && key.len() <= 256
                && key
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
        }
        _ => false,
    };
    if !valid {
        return Err(CoreError::InvalidInput("source account identity".into()));
    }
    let name = account.display_name.and_then(|n| {
        trace_contracts::content::safe_metadata(&json!(n))
            .as_str()
            .map(|n| n.chars().take(512).collect::<String>())
    });
    let evidence = trace_contracts::content::safe_metadata(&account.evidence);
    let url = if namespace == "bilibili" {
        format!("https://space.bilibili.com/{key}")
    } else {
        format!("https://www.douyin.com/user/{key}")
    };
    let now = crate::jobs::now();
    tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,profile_url,first_seen_at,profile_updated_at) VALUES(?1,?2,?3,?4,?5,?6,?6) ON CONFLICT(namespace,native_account_key) DO UPDATE SET display_name=COALESCE(excluded.display_name,source_accounts.display_name),profile_url=excluded.profile_url,profile_updated_at=excluded.profile_updated_at",rusqlite::params![format!("acc-{}",uuid::Uuid::new_v4()),namespace,key,name,url,now])?;
    let id: String = tx.query_row(
        "SELECT account_id FROM source_accounts WHERE namespace=?1 AND native_account_key=?2",
        rusqlite::params![namespace, key],
        |r| r.get(0),
    )?;
    tx.execute("INSERT INTO account_observations(account_id,display_name,observed_at,evidence) VALUES(?1,?2,?3,?4)",rusqlite::params![id,name,now,evidence.to_string()])?;
    crate::objects::outbox_emit(tx, "account.updated", &id, &json!({"account_id":id}))?;
    Ok(
        json!({"account_id":id,"native_account_key":key,"display_name":name,"profile_url":url,"evidence":evidence}),
    )
}

pub async fn resolve(core: &Core, args: Value) -> Result<Value, CoreError> {
    let namespace = args["namespace"]
        .as_str()
        .filter(|s| matches!(*s, "bilibili" | "douyin"))
        .ok_or_else(|| CoreError::InvalidInput("platform".into()))?;
    let input = args["input"]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= 2048)
        .ok_or_else(|| CoreError::InvalidInput("account input".into()))?;
    let credential = args["credential_id"].as_str();
    if let Some(id) = credential {
        let key = id.to_owned();
        let platform = namespace.to_owned();
        let valid=core.db.read(move|c|Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM credentials_state WHERE credential_id=?1 AND platform=?2 AND status='valid')",rusqlite::params![key,platform],|r|r.get::<_,bool>(0))?)).await?;
        if !valid {
            return Err(CoreError::AuthRequired("account credential".into()));
        }
        let secret = String::from_utf8(crate::vault::load_current(core, id).await?)
            .map_err(|_| CoreError::AuthRequired("credential encoding".into()))?;
        core.cache_cookie(&format!("{namespace}/{id}"), secret);
    }
    core.metadata_pacing(namespace).await;
    let value = core
        .host
        .call_scoped(
            namespace,
            credential,
            "resolve_account",
            json!({"input":input}),
            Some(30000),
        )
        .await?;
    let account: ResolvedAccount = serde_json::from_value(value)?;
    let namespace = namespace.to_owned();
    core.db
        .write(move |tx| persist(tx, &namespace, account))
        .await
}

pub async fn schedule(
    core: &Core,
    job: &crate::jobs::ClaimedJob,
    sub: &str,
) -> Result<(), CoreError> {
    let sub = sub.to_owned();
    let guard = job.clone();
    core.db.write(move|tx|{guard.check(tx)?;
        tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) SELECT 'profile:'||a.account_id,'profile:'||a.account_id,'account_profile',a.account_id,json_object('subscription_id',s.subscription_id),2,'queued',COALESCE(a.profile_updated_at,?2),?2,?2 FROM source_accounts a JOIN subscriptions s ON s.account_id=a.account_id WHERE s.subscription_id=?1",rusqlite::params![sub,crate::jobs::now()])?;
        reconcile_subscription(tx, &sub)
    }).await
}

/// A profile belongs to the account, while its credential scope belongs to an
/// enabled subscription. Transfer that scope atomically when its owner pauses.
/// Revoking the lease also rejects a reply from the previous credential scope.
pub(crate) fn reconcile_subscription(
    tx: &rusqlite::Transaction<'_>,
    sub: &str,
) -> Result<(), CoreError> {
    let current: Option<(String, Option<String>, String)> = tx.query_row(
        "SELECT j.job_id,json_extract(j.payload,'$.subscription_id'),j.state FROM jobs j JOIN subscriptions s ON s.account_id=j.subject_id WHERE s.subscription_id=?1 AND j.job_type='account_profile'", [sub],
        |r| Ok((r.get(0)?,r.get(1)?,r.get(2)?))).optional()?;
    let Some((job_id, owner, state)) = current else {
        return Ok(());
    };
    if state == "cancelled" {
        return Ok(());
    }
    let next: Option<String> = tx.query_row(
        "SELECT s.subscription_id FROM subscriptions s JOIN jobs j ON j.subject_id=s.account_id WHERE j.job_id=?1 AND s.state='enabled' ORDER BY (s.subscription_id=?2) DESC,s.subscription_id LIMIT 1",
        rusqlite::params![job_id,owner], |r| r.get(0)).optional()?;
    if next == owner && state != "paused" {
        return Ok(());
    }
    let paused = next.is_none();
    let disabled: bool = tx.query_row("SELECT EXISTS(SELECT 1 FROM jobs j JOIN source_accounts a ON a.account_id=j.subject_id JOIN plugins_state p ON p.platform=a.namespace WHERE j.job_id=?1 AND p.enabled=0)", [&job_id], |r| r.get(0))?;
    tx.execute("UPDATE jobs SET payload=json_set(payload,'$.subscription_id',?2),state=?3,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?4 WHERE job_id=?1",
        rusqlite::params![job_id,next.or(owner),if paused {"paused"} else if disabled {"waiting_plugin"} else {"queued"},crate::jobs::now()])?;
    Ok(())
}

pub async fn refresh_job(core: &Core, job: &crate::jobs::ClaimedJob) -> Result<(), CoreError> {
    let id = job.subject_id.clone();
    let (namespace, key) = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT namespace,native_account_key FROM source_accounts WHERE account_id=?1",
                [id],
                |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)),
            )?)
        })
        .await?;
    core.metadata_pacing(&namespace).await;
    let value = core
        .adapter_call(
            job,
            &namespace,
            "resolve_account",
            json!({"input":key}),
            Some(30000),
        )
        .await?;
    let account: ResolvedAccount = serde_json::from_value(value)?;
    if account.native_account_key != key {
        return Err(CoreError::InvalidInput(
            "account refresh identity mismatch".into(),
        ));
    }
    let guard = job.clone();
    core.db.write(move|tx|{
        guard.check(tx)?;persist(tx,&namespace,account)?;
        let next=(time::OffsetDateTime::now_utc()+time::Duration::days(1)).format(&time::format_description::well_known::Rfc3339).map_err(|_|CoreError::Internal("clock".into()))?;
        tx.execute("UPDATE jobs SET state='queued',next_run_at=?2,lease_owner=NULL,lease_expires_at=NULL WHERE job_id=?1",rusqlite::params![guard.job_id,next])?;Ok(())
    }).await
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{config::Profile, jobs};

    #[tokio::test]
    async fn c01_profile_scope_transfers_between_enabled_subscriptions_without_late_commit() {
        let dir = tempfile::tempdir().unwrap();
        let core = Core::open(Profile {
            data_root: dir.path().join("data"),
            archive_root: dir.path().join("archive"),
            vault_dir: Some(dir.path().join("vault")),
            ..Default::default()
        })
        .await
        .unwrap();
        core.db.write(|tx| {
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')", [])?;
            for (sub,kind) in [("first", "video"), ("second", "article")] {
                tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES(?1,'a',?2,'{}','t','t')", rusqlite::params![sub,kind])?;
            }
            tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,attempts,created_at,updated_at) VALUES('profile:a','profile:a','account_profile','a','{\"subscription_id\":\"first\"}',2,'queued',2,'t','t')", [])?;
            Ok(())
        }).await.unwrap();
        let first = jobs::claim_due(&core.db, "scope-test", 1)
            .await
            .unwrap()
            .remove(0);
        first.mark_running(&core.db).await.unwrap();
        core.db
            .write(|tx| jobs::set_subscription_enabled(tx, "first", false))
            .await
            .unwrap();
        assert!(first.heartbeat(&core.db).await.is_err());
        let second = jobs::claim_due(&core.db, "scope-test", 1)
            .await
            .unwrap()
            .remove(0);
        assert_eq!(second.payload["subscription_id"], "second");
        assert!(second.generation > first.generation);
        second.mark_running(&core.db).await.unwrap();
        core.db
            .write(|tx| jobs::set_subscription_enabled(tx, "second", false))
            .await
            .unwrap();
        assert!(second.heartbeat(&core.db).await.is_err());
        assert!(
            jobs::claim_due(&core.db, "scope-test", 10)
                .await
                .unwrap()
                .is_empty()
        );
        core.db.write(|tx| {
            tx.execute("INSERT INTO plugins_state(plugin_id,platform,version,protocol,binary_path,enabled,updated_at) VALUES('bilibili','bilibili','1','1','none',0,'t')", [])?;
            jobs::set_subscription_enabled(tx, "first", true)?;
            let state: String = tx.query_row("SELECT state FROM jobs WHERE job_id='profile:a'", [], |r| r.get(0))?;
            assert_eq!(state,"waiting_plugin");
            tx.execute("UPDATE plugins_state SET enabled=1", [])?;
            tx.execute("UPDATE jobs SET state='queued' WHERE job_id='profile:a'", [])?;
            let attempts: i64 = tx.query_row("SELECT attempts FROM jobs WHERE job_id='profile:a'", [], |r| r.get(0))?;
            assert_eq!(attempts,2);
            Ok(())
        }).await.unwrap();
        // Explicit cancellation cannot be undone by a second subscription scan.
        core.db
            .write(|tx| {
                tx.execute(
                    "UPDATE jobs SET state='cancelled' WHERE job_id='profile:a'",
                    [],
                )?;
                jobs::set_subscription_enabled(tx, "second", true)?;
                reconcile_subscription(tx, "second")?;
                assert_eq!(
                    tx.query_row("SELECT state FROM jobs WHERE job_id='profile:a'", [], |r| r
                        .get::<_, String>(0))?,
                    "cancelled"
                );
                Ok(())
            })
            .await
            .unwrap();
        core.shutdown().await;
    }
}
