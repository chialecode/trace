//! Completing an adapter's media plan with what the tool knows. The adapter marks the
//! resources it cannot answer for (`notes.external_media`); Core asks the tool for the
//! video's formats and adds the candidates, so everything after this point is the ordinary
//! pipeline: ranking, quality rules, download, verification and commit.
use super::formats::{self, video_id_ok};
use super::{MediaTools, run::Sandbox, tool_for};
use crate::core::Core;
use crate::error::CoreError;
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;
use trace_contracts::plugin::{ResolveMediaResult, ResourceIdentityDto};
use trace_contracts::source::ResolvedSource;

/// Requests a run of the tool is counted as against a bounded validation budget: the watch
/// page, the player data, the player script and the runtime's challenge solving.
const REQUESTS_PER_RUN: usize = 5;

/// Whether the tool has anything to add: the adapter asked for it, this site is one it
/// serves, and a video resource has no candidate yet.
pub(super) fn needed(
    platform: &str,
    resources: &[ResourceIdentityDto],
    plan: &ResolveMediaResult,
) -> bool {
    tool_for(platform) == Some("yt_dlp")
        && plan.notes["external_media"] == "yt_dlp"
        && resources.iter().any(|r| {
            r.role == "video"
                && !plan
                    .candidates
                    .iter()
                    .any(|c| c.resource.source_attachment_id == r.source_attachment_id)
        })
}

/// Asks the tool and adds its candidates to `plan`.
pub(super) async fn apply(
    tools: &MediaTools,
    sandbox: &Sandbox<'_>,
    cancel: &CancellationToken,
    native_key: &str,
    resources: &[ResourceIdentityDto],
    hint: &Value,
    plan: &mut ResolveMediaResult,
) -> Result<(), CoreError> {
    if !video_id_ok(native_key) {
        return Err(CoreError::InvalidInput("video identity".into()));
    }
    let info = tools.video_info(sandbox, native_key, cancel).await?;
    let all = hint["all_variants"] == true;
    let prefer_fps = hint["prefer_fps"] == true;
    let offers = formats::offers(&info, native_key, resources, all, prefer_fps)?;
    plan.candidates.extend(offers.candidates);
    if let Some(group) = offers.waives_audio {
        let mut waived = plan.notes["silent_or_muxed"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        waived.push(json!(group));
        plan.notes["silent_or_muxed"] = json!(waived);
    }
    Ok(())
}

/// Adds the tool's candidates to `plan` for the resources of one video. Does nothing for a
/// plan the adapter answered completely.
pub(crate) async fn complete(
    core: &Core,
    platform: &str,
    native_key: &str,
    resources: &[ResourceIdentityDto],
    hint: &Value,
    plan: &mut ResolveMediaResult,
    evidence_id: Option<&str>,
) -> Result<(), CoreError> {
    if !needed(platform, resources, plan) {
        return Ok(());
    }
    let tools = &core.media_tools;
    if !tools.ready().await {
        return Err(tools.unavailable().await);
    }
    let route = core.network.route_for(platform);
    let proxy = match &route {
        crate::network::Route::Proxy(proxy) => Some(proxy),
        crate::network::Route::Direct => None,
    };
    core.http
        .charge_external(
            "https://www.youtube.com/watch",
            REQUESTS_PER_RUN,
            true,
            evidence_id,
        )
        .await?;
    let work = core.profile.data_root.join("cache").join("media-tools");
    let sandbox = Sandbox { work: &work, proxy };
    apply(
        tools,
        &sandbox,
        &core.cancel,
        native_key,
        resources,
        hint,
        plan,
    )
    .await
}

/// The same for a single-link source: every rendition is offered, and a video that carries
/// its own sound (or has none) no longer needs its soundtrack resource.
pub(crate) async fn complete_source(
    core: &Core,
    platform: &str,
    source: &mut ResolvedSource,
) -> Result<(), CoreError> {
    let Some(detail) = &source.detail else {
        return Ok(());
    };
    let (key, resources) = (detail.native_entry_key.clone(), detail.resources.clone());
    complete(
        core,
        platform,
        &key,
        &resources,
        &json!({"all_variants": true}),
        &mut source.media,
        None,
    )
    .await?;
    waive_audio(source);
    Ok(())
}

/// A soundtrack resource of a group the tool marked as not needing one stops being required.
pub(super) fn waive_audio(source: &mut ResolvedSource) {
    let waived: Vec<String> = source.media.notes["silent_or_muxed"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|v| v.as_str().map(str::to_owned))
        .collect();
    if let Some(detail) = &mut source.detail {
        for resource in &mut detail.resources {
            let group = resource
                .group_key
                .as_deref()
                .unwrap_or(&resource.source_attachment_id);
            if resource.role == "audio" && waived.iter().any(|w| w == group) {
                resource.required = false;
            }
        }
    }
}
