//! Creator relations, profile history and pending identity candidates (stage plan §11.1/§11.2).
use super::{need_str, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use rusqlite::Connection;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::CREATOR_MERGE_PREVIEW => merge_preview(core, &args).await,
        cmd::CREATOR_SPLIT_PREVIEW => split_preview(core, &args).await,
        cmd::CREATOR_RELINK_PREVIEW => relink_preview(core, &args).await,
        cmd::CREATOR_MERGE => merge(core, &args).await,
        cmd::CREATOR_SPLIT => split(core, &args).await,
        cmd::CREATOR_RELINK => relink(core, &args).await,
        cmd::CREATOR_OPERATIONS => operations(core, &args).await,
        cmd::IDENTITY_CANDIDATES => candidates(core, &args).await,
        cmd::IDENTITY_CONFIRM => confirm(core, &args).await,
        cmd::IDENTITY_REJECT => reject(core, &args).await,
        cmd::ACCOUNT_PROFILE_HISTORY => profile_history(core, &args).await,
        other => Err(unsupported(other)),
    }
}

fn token(kind: &str, impact: &Value) -> String {
    use sha2::{Digest, Sha256};
    let mut hasher = Sha256::new();
    hasher.update(kind.as_bytes());
    hasher.update([0]);
    hasher.update(impact.to_string().as_bytes());
    hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

/// The exact impact the user must confirm: both creators, every account and the entries and
/// subscriptions each account owns. A changed binding or count changes the token (F-072).
fn merge_impact(c: &Connection, source: &str, target: &str) -> Result<(Value, String), CoreError> {
    if source == target {
        return Err(CoreError::InvalidInput(
            "cannot merge a creator into itself".into(),
        ));
    }
    let impact = serde_json::json!({
        "kind": "merge",
        "source_creator_id": source,
        "target_creator_id": target,
        "source_scope": store::identity::scope(c, source)?,
        "target_scope": store::identity::scope(c, target)?,
        "accounts": store::identity::bound_accounts(c, source)?,
    });
    let token = token("merge", &impact);
    Ok((impact, token))
}

fn split_impact(
    c: &Connection,
    source: &str,
    accounts: &[String],
) -> Result<(Value, String), CoreError> {
    if accounts.is_empty() {
        return Err(CoreError::InvalidInput(
            "select at least one account".into(),
        ));
    }
    let mut rows = Vec::new();
    for account in accounts {
        let scope = store::identity::account_scope(c, account)?;
        if scope["creator_id"] != source {
            return Err(CoreError::VersionConflict(format!(
                "account {account} is no longer bound to the source creator"
            )));
        }
        rows.push(scope);
    }
    let impact = serde_json::json!({
        "kind": "split",
        "source_creator_id": source,
        "source_scope": store::identity::scope(c, source)?,
        "accounts": rows,
    });
    let token = token("split", &impact);
    Ok((impact, token))
}

fn relink_impact(
    c: &Connection,
    account: &str,
    expected: &str,
    target: &str,
) -> Result<(Value, String), CoreError> {
    let scope = store::identity::account_scope(c, account)?;
    if scope["creator_id"] != expected {
        return Err(CoreError::VersionConflict(format!(
            "account {account} is no longer bound to the expected creator"
        )));
    }
    let impact = serde_json::json!({
        "kind": "relink",
        "account": scope,
        "expected_creator_id": expected,
        "target_creator_id": target,
        "target_scope": store::identity::scope(c, target)?,
    });
    let token = token("relink", &impact);
    Ok((impact, token))
}

async fn merge_preview(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let source = need_str(args, "source_creator_id")?;
    let target = need_str(args, "target_creator_id")?;
    let (impact, token) = core
        .db
        .read(move |c| merge_impact(c, &source, &target))
        .await?;
    Ok(serde_json::json!({"impact": impact, "preview_token": token}))
}

async fn split_preview(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let source = need_str(args, "source_creator_id")?;
    let accounts = account_ids(args)?;
    let (impact, token) = core
        .db
        .read(move |c| split_impact(c, &source, &accounts))
        .await?;
    Ok(serde_json::json!({"impact": impact, "preview_token": token}))
}

async fn relink_preview(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let account = need_str(args, "account_id")?;
    let expected = need_str(args, "expected_creator_id")?;
    let target = need_str(args, "target_creator_id")?;
    let (impact, token) = core
        .db
        .read(move |c| relink_impact(c, &account, &expected, &target))
        .await?;
    Ok(serde_json::json!({"impact": impact, "preview_token": token}))
}

fn account_ids(args: &Value) -> Result<Vec<String>, CoreError> {
    let accounts: Vec<String> = args["account_ids"]
        .as_array()
        .map(|values| {
            values
                .iter()
                .filter_map(Value::as_str)
                .take(256)
                .map(str::to_owned)
                .collect()
        })
        .ok_or_else(|| CoreError::InvalidInput("account_ids".into()))?;
    if accounts.is_empty() {
        return Err(CoreError::InvalidInput("account_ids".into()));
    }
    Ok(accounts)
}

async fn merge(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let source = need_str(args, "source_creator_id")?;
    let target = need_str(args, "target_creator_id")?;
    let given = need_str(args, "preview_token")?;
    core.db
        .write(move |tx| {
            let (_, expected) = merge_impact(tx, &source, &target)?;
            if given != expected {
                return Err(CoreError::VersionConflict(
                    "creator merge preview is stale; preview again".into(),
                ));
            }
            store::identity::merge(tx, &source, &target, &jobs::now())
        })
        .await
}

async fn split(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let source = need_str(args, "source_creator_id")?;
    let accounts = account_ids(args)?;
    let given = need_str(args, "preview_token")?;
    let name = args["display_name"]
        .as_str()
        .map(str::trim)
        .filter(|s| !s.is_empty() && s.len() <= 1024)
        .map(str::to_owned)
        .unwrap_or_else(|| "拆分的创作者".to_owned());
    let new_id = format!("cre-{}", uuid::Uuid::new_v4());
    let id = new_id.clone();
    core.db
        .write(move |tx| {
            let (_, expected) = split_impact(tx, &source, &accounts)?;
            if given != expected {
                return Err(CoreError::VersionConflict(
                    "creator split preview is stale; preview again".into(),
                ));
            }
            store::identity::split(tx, &source, &accounts, &id, &name, &jobs::now())
        })
        .await
}

async fn relink(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let account = need_str(args, "account_id")?;
    let expected = need_str(args, "expected_creator_id")?;
    let target = need_str(args, "target_creator_id")?;
    let given = need_str(args, "preview_token")?;
    core.db
        .write(move |tx| {
            let (_, token) = relink_impact(tx, &account, &expected, &target)?;
            if given != token {
                return Err(CoreError::VersionConflict(
                    "creator relink preview is stale; preview again".into(),
                ));
            }
            store::identity::relink(tx, &account, &expected, &target, &jobs::now())
        })
        .await
}

async fn operations(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let creator = args["creator_id"].as_str().map(str::to_owned);
    let after = args["after"].as_str().map(str::to_owned);
    let limit = args["limit"].as_i64().unwrap_or(50);
    core.db
        .read(move |c| store::identity::operations(c, creator.as_deref(), after.as_deref(), limit))
        .await
}

async fn candidates(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let account = args["account_id"].as_str().map(str::to_owned);
    let creator = args["creator_id"].as_str().map(str::to_owned);
    let state = args["state"].as_str().map(str::to_owned);
    let after = args["after"].as_str().map(str::to_owned);
    let limit = args["limit"].as_i64().unwrap_or(50);
    core.db
        .read(move |c| {
            store::identity::candidates(
                c,
                account.as_deref(),
                creator.as_deref(),
                state.as_deref(),
                after.as_deref(),
                limit,
            )
        })
        .await
}

async fn confirm(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let candidate = need_str(args, "candidate_id")?;
    core.db
        .write(move |tx| store::identity::confirm(tx, &candidate, &jobs::now()))
        .await
}

async fn reject(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let candidate = need_str(args, "candidate_id")?;
    core.db
        .write(move |tx| store::identity::reject(tx, &candidate, &jobs::now()))
        .await
}

async fn profile_history(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let account = need_str(args, "account_id")?;
    let before = args["before"].as_i64().unwrap_or(i64::MAX);
    let limit = args["limit"].as_i64().unwrap_or(20);
    core.db
        .read(move |c| store::identity::profile_history(c, &account, before, limit))
        .await
}
