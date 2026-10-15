//! Source accounts: resolving a link, binding to a creator, profile and name history.
use super::{need_str, parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        "account.resolve" => crate::accounts::resolve(core, args).await,
        "account.history" => history(core, &args).await,
        cmd::ACCOUNT_BIND => bind(core, parse(args)?).await,
        cmd::ACCOUNT_UNBIND => unbind(core, parse(args)?).await,
        cmd::ACCOUNT_UPDATE => update(core, parse(args)?).await,
        cmd::ACCOUNT_LIST => list(core, parse(args)?).await,
        cmd::ACCOUNT_PROFILE_HISTORY => super::identity::route(core, req).await,
        other => Err(unsupported(other)),
    }
}

async fn history(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "account_id")?;
    let before = args["before"].as_i64().unwrap_or(i64::MAX);
    core.db
        .read(move |c| store::accounts::name_history(c, &id, before))
        .await
}

/// An active binding to another creator is a conflict, never a false success.
async fn bind(core: &Arc<Core>, a: ctl::AccountBindArgs) -> Result<Value, CoreError> {
    let new_account = format!("acc-{}", uuid::Uuid::new_v4());
    let now = jobs::now();
    let account_id = core
        .db
        .write(move |tx| {
            let (display, url) = (a.display_name.as_deref(), a.profile_url.as_deref());
            let account = store::accounts::upsert(
                tx,
                &new_account,
                &a.namespace,
                &a.native_account_key,
                display,
                url,
                &now,
            )?;
            if store::accounts::bound_to_other_creator(tx, &account, &a.creator_id)? {
                return Err(CoreError::VersionConflict("account binding".into()));
            }
            let binding = format!("bnd-{}", uuid::Uuid::new_v4());
            store::accounts::bind(tx, &binding, &a.creator_id, &account, &now)?;
            let payload = json!({"creator_id": a.creator_id});
            crate::objects::outbox_emit(tx, "account.bound", &account, &payload)?;
            Ok(account)
        })
        .await?;
    Ok(json!({"account_id": account_id}))
}

async fn unbind(core: &Arc<Core>, a: ctl::AccountUnbindArgs) -> Result<Value, CoreError> {
    core.db
        .write(move |tx| store::accounts::unbind(tx, &a.account_id, &jobs::now()))
        .await?;
    Ok(json!({"ok": true}))
}

async fn update(core: &Arc<Core>, a: ctl::AccountUpdateArgs) -> Result<Value, CoreError> {
    core.db
        .write(move |tx| {
            if let Some(name) = &a.display_name {
                store::accounts::set_display_name(tx, &a.account_id, name, &jobs::now())?;
            }
            if let Some(url) = &a.profile_url {
                store::accounts::set_profile_url(tx, &a.account_id, url, &jobs::now())?;
            }
            Ok(())
        })
        .await?;
    Ok(json!({"ok": true}))
}

async fn list(core: &Arc<Core>, mut a: ctl::AccountListArgs) -> Result<Value, CoreError> {
    a.creator_id = super::resolve_creator(core, a.creator_id).await?;
    let items = core
        .db
        .read(move |c| store::accounts::list(c, a.creator_id.as_deref()))
        .await?;
    Ok(json!({ "items": items }))
}
