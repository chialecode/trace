//! Credentials: import, replace, validate and revoke. Secrets live in the vault only.
use super::credential_probe::probe;
use super::{need_str, parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

const SECRET_MAX_BYTES: usize = 65536;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::CREDENTIAL_LIST => list(core, parse(args)?).await,
        cmd::CREDENTIAL_IMPORT => import(core, parse(args)?).await,
        "credential.update" => replace(core, &args).await,
        cmd::CREDENTIAL_VALIDATE => validate(core, parse(args)?).await,
        cmd::CREDENTIAL_REVOKE => revoke(core, parse(args)?).await,
        other => Err(unsupported(other)),
    }
}

async fn list(core: &Arc<Core>, a: ctl::CredentialListArgs) -> Result<Value, CoreError> {
    let items = core
        .db
        .read(move |c| store::credentials::list(c, a.platform.as_deref()))
        .await?;
    Ok(json!({ "items": items }))
}

/// Stores the secret, then probes the platform. A probe that cannot decide still keeps the import.
async fn import(core: &Arc<Core>, a: ctl::CredentialImportArgs) -> Result<Value, CoreError> {
    if a.secret.is_empty() {
        return Err(CoreError::InvalidInput("empty credential".into()));
    }
    let credential_id = format!("cred-{}", uuid::Uuid::new_v4());
    core.vault.store(&credential_id, a.secret.as_bytes())?;
    let (id, platform, subject) = (credential_id.clone(), a.platform.clone(), a.subject.clone());
    core.db
        .write(move |tx| {
            store::credentials::insert(tx, &id, &platform, subject.as_deref(), &jobs::now())
        })
        .await?;
    // Decrypted into the short-lived cache for the adapter proxy (memory only).
    core.cache_cookie(&format!("{}/{}", a.platform, credential_id), a.secret);
    match probe(core, &credential_id, &a.platform).await {
        Ok((status, capability)) => {
            Ok(json!({"credential_id": credential_id, "status": status, "capability": capability}))
        }
        Err(e) => Ok(
            json!({"credential_id": credential_id, "status": "unknown", "error": e.failure_class()}),
        ),
    }
}

/// Replaces the secret under a new vault entry; running work waits for the new verdict.
async fn replace(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "credential_id")?;
    let secret = args["secret"]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= SECRET_MAX_BYTES)
        .ok_or_else(|| CoreError::InvalidInput("credential text".into()))?
        .to_owned();
    let current = id.clone();
    let (platform, old_ref, generation) = core
        .db
        .read(move |c| store::credentials::binding_of(c, &current))
        .await?;
    let new_ref = format!("vault-{}", uuid::Uuid::new_v4());
    core.vault.store(&new_ref, secret.as_bytes())?;
    let (credential, reference) = (id.clone(), new_ref.clone());
    let swapped = core
        .db
        .write(move |tx| {
            let now = jobs::now();
            if !store::credentials::swap_secret(tx, &credential, &reference, generation, &now)? {
                return Err(CoreError::VersionConflict(
                    "credential changed during update".into(),
                ));
            }
            store::credentials::cancel_probe_jobs(tx, &credential)?;
            store::credentials::park_subscription_work(tx, &credential, &jobs::now())?;
            jobs::consumers::credential_changed(tx, &credential, false)
        })
        .await;
    if let Err(e) = swapped {
        let _ = core.vault.remove(&new_ref);
        return Err(e);
    }
    core.clear_cookie(&format!("{platform}/{id}"));
    core.host.kill_session(&format!("{platform}/{id}")).await;
    core.vault.remove(&old_ref)?;
    let (status, capability) = probe(core, &id, &platform).await?;
    Ok(json!({"credential_id": id, "status": status, "capability": capability}))
}

async fn validate(core: &Arc<Core>, a: ctl::CredentialRefArgs) -> Result<Value, CoreError> {
    let id = a.credential_id.clone();
    let platform = core
        .db
        .read(move |c| store::credentials::platform_of(c, &id))
        .await?;
    let (status, capability) = probe(core, &a.credential_id, &platform).await?;
    Ok(json!({"status": status, "capability": capability}))
}

/// Revoking parks the work that waited on the credential; history is never destroyed.
async fn revoke(core: &Arc<Core>, a: ctl::CredentialRefArgs) -> Result<Value, CoreError> {
    let id = a.credential_id;
    let lookup = id.clone();
    let (platform, reference) = core
        .db
        .read(move |c| {
            Ok((
                store::credentials::platform_of(c, &lookup)?,
                store::credentials::vault_ref_of(c, &lookup)?,
            ))
        })
        .await?;
    let removed = id.clone();
    core.db
        .write(move |tx| {
            store::credentials::park_for_revocation(tx, &removed)?;
            store::credentials::delete(tx, &removed)?;
            jobs::consumers::credential_changed(tx, &removed, false)
        })
        .await?;
    core.clear_cookie(&format!("{platform}/{id}"));
    core.host.kill_session(&format!("{platform}/{id}")).await;
    core.vault.remove(&reference)?;
    let revoked = id;
    core.db
        .write(move |tx| store::credentials::park_active_work(tx, &revoked, &jobs::now()))
        .await?;
    Ok(json!({"ok": true}))
}
