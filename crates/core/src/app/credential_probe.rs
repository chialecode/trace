//! Credential probes: ask the adapter whether a credential still authenticates and keep the verdict.
//!
//! A probe answers only "does this credential authenticate". It says nothing about media
//! permission or network health, so a transient failure stays `unknown`, never `invalid`.
use super::plus_secs;
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use rusqlite::Transaction;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::plugin::methods::PROBE_AUTH;

const VALID_RECHECK_SECONDS: i64 = 21600;
const UNKNOWN_RECHECK_SECONDS: i64 = 60;
const PROBE_TIMEOUT_MS: u64 = 20_000;
const RETRY_BACKOFF_MAX_SECONDS: i64 = 3600;

/// Probes now, then schedules the next probe of the credential's current generation.
pub(crate) async fn probe(
    core: &Arc<Core>,
    credential_id: &str,
    platform: &str,
) -> Result<(String, Value), CoreError> {
    let result = probe_guarded(core, credential_id, platform, None).await?;
    let (id, platform) = (credential_id.to_owned(), platform.to_owned());
    core.db
        .write(move |tx| {
            // Read the current binding and state in the scheduling transaction: a
            // simultaneous credential replacement must not inherit an old result.
            let Some((generation, status)) = store::credentials::generation_and_status(tx, &id)
            else {
                return Ok(());
            };
            if status == "invalid" {
                return Ok(());
            }
            let wait = if status == "valid" {
                VALID_RECHECK_SECONDS
            } else {
                UNKNOWN_RECHECK_SECONDS
            };
            let next = plus_secs(&jobs::now(), wait);
            store::credentials::schedule_probe(tx, &id, generation, &platform, &next, &jobs::now())
        })
        .await?;
    Ok(result)
}

/// The recurring probe job of a credential.
pub(crate) async fn run_job(core: &Arc<Core>, job: &jobs::ClaimedJob) -> Result<(), CoreError> {
    let platform = job.payload["platform"]
        .as_str()
        .ok_or_else(|| CoreError::InvalidInput("credential platform".into()))?;
    core.metadata_pacing(platform).await;
    let (status, _) = probe_guarded(core, &job.subject_id, platform, Some(job.clone())).await?;
    let job = job.clone();
    match status.as_str() {
        "valid" => {
            core.db
                .write(move |tx| {
                    job.check(tx)?;
                    let next = plus_secs(&jobs::now(), VALID_RECHECK_SECONDS);
                    store::credentials::reschedule_probe_job(tx, &job.job_id, &next)?;
                    crate::issues::resolved_job(tx, &job.job_id)
                })
                .await
        }
        "invalid" => Err(CoreError::AuthRequired(
            "credential validation failed".into(),
        )),
        _ => {
            core.db
                .write(move |tx| {
                    job.check(tx)?;
                    let attempts = store::credentials::job_attempts(tx, &job.job_id)?;
                    let delay = 60_i64.saturating_mul(1_i64 << attempts.min(6));
                    let next = plus_secs(&jobs::now(), delay.min(RETRY_BACKOFF_MAX_SECONDS));
                    let error = json!({
                        "class": "transient_network",
                        "message": "credential validation unavailable",
                    });
                    store::credentials::retry_probe_job_later(
                        tx,
                        &job.job_id,
                        &next,
                        &error.to_string(),
                    )
                })
                .await
        }
    }
}

async fn probe_guarded(
    core: &Arc<Core>,
    credential_id: &str,
    platform: &str,
    guard: Option<jobs::ClaimedJob>,
) -> Result<(String, Value), CoreError> {
    if core.cancel.is_cancelled() {
        return Err(CoreError::Cancelled);
    }
    if let Some(job) = guard.clone() {
        core.db.read(move |c| job.check(c)).await?;
    }
    let cid = credential_id.to_owned();
    let (reference, generation) = core
        .db
        .read(move |c| store::credentials::reference_and_generation(c, &cid))
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
            PROBE_AUTH,
            json!({}),
            Some(PROBE_TIMEOUT_MS),
        )
        .await;
    let (status, capability) = classify_auth(&result);
    record_verdict(core, credential_id, &status, &capability, generation, guard).await?;
    if status == "valid" {
        wake_dependents(core, credential_id, generation).await?;
    } else {
        core.clear_cookie(&format!("{platform}/{credential_id}"));
    }
    Ok((status, capability))
}

/// Stores the verdict for exactly this generation; a rejected credential parks its work.
async fn record_verdict(
    core: &Arc<Core>,
    credential_id: &str,
    status: &str,
    capability: &Value,
    generation: i64,
    guard: Option<jobs::ClaimedJob>,
) -> Result<(), CoreError> {
    let (id, status) = (credential_id.to_owned(), status.to_owned());
    let capability = capability.to_string();
    core.db
        .write(move |tx| {
            if let Some(job) = guard {
                job.check(tx)?;
            }
            let now = jobs::now();
            store::credentials::record_probe_result(
                tx,
                &id,
                &status,
                &capability,
                generation,
                &now,
            )?;
            if status == "invalid" {
                store::credentials::park_unfinished_subscription_work(tx, &id)?;
                jobs::consumers::credential_changed(tx, &id, false)?;
            }
            Ok(())
        })
        .await
}

/// A valid credential wakes the work that waited for authentication (re-authorisation resumes, FR-AUTH-005).
async fn wake_dependents(
    core: &Arc<Core>,
    credential_id: &str,
    generation: i64,
) -> Result<(), CoreError> {
    let credential = credential_id.to_owned();
    core.db
        .write(move |tx| wake(tx, &credential, generation))
        .await
}

fn wake(tx: &Transaction<'_>, credential: &str, generation: i64) -> Result<(), CoreError> {
    if !store::credentials::is_valid_generation(tx, credential, generation)? {
        return Err(CoreError::Cancelled);
    }
    store::credentials::wake_waiting_auth(tx, credential, &jobs::now())?;
    jobs::consumers::credential_changed(tx, credential, true)?;
    crate::upgrades::schedule(tx, credential, generation)?;
    store::credentials::requeue_media_below_target(tx, credential, &jobs::now())
}

fn capability_document(value: &Value) -> Value {
    let state = value["state"].as_str();
    let session_access = match state {
        Some("valid" | "authenticated") => "valid",
        Some("invalid" | "anonymous") => "invalid",
        _ => "unknown",
    };
    let mut capabilities = json!({
        "session_access": session_access,
        "original_image": "unknown",
        "video_quality": "unknown",
        "membership_content": "unknown",
        "live_access": "unknown",
    });
    if let Some(fields) = trace_contracts::content::safe_metadata(&value["capability"]).as_object()
    {
        capabilities.as_object_mut().unwrap().extend(fields.clone());
    }
    capabilities
}

fn classify_auth(result: &Result<Value, CoreError>) -> (String, Value) {
    match result {
        Ok(value) => {
            let status = match value["state"].as_str() {
                Some("valid" | "authenticated") => "valid",
                Some("invalid" | "anonymous") => "invalid",
                _ => "unknown",
            };
            (status.into(), capability_document(value))
        }
        Err(error) => {
            let rejected = matches!(
                error.failure_class(),
                trace_domain::errors::FailureClass::AuthRequired
            );
            (
                if rejected { "invalid" } else { "unknown" }.into(),
                json!({"error": error.failure_class()}),
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn authentication_is_not_media_permission_or_network_health() {
        for state in ["valid", "authenticated"] {
            let (status, cap) = classify_auth(&Ok(
                json!({"state":state,"capability":{"media_access":"unknown","token":"PRIVATE_AUTH_SENTINEL"}}),
            ));
            assert_eq!(status, "valid");
            assert_eq!(cap["media_access"], "unknown");
            assert!(!cap.to_string().contains("PRIVATE_AUTH_SENTINEL"));
        }
        assert_eq!(
            classify_auth(&Ok(json!({"state":"anonymous"}))).0,
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
