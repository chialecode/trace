//! Media acquisition (C27/C28).
//!
//! A group completes only when all its required resources are present. Every resource has
//! its own materialization, and without a validator a download restarts from the beginning.
use super::acquire::{self, acquisition_spec};
use super::mux::create_playback_mux;
use super::observe::with_context;
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::store::{
    self,
    entries::Source,
    media::{Item, Provenance},
};
use crate::transport::{HostRules, host_rules, platform_rules};
use serde_json::{Value, json};
use std::collections::HashMap;
use tokio::sync::OwnedSemaphorePermit;
use trace_contracts::acquisition::{AcquisitionContext, AuthenticationState};
use trace_contracts::control::QualityRequirement;
use trace_contracts::plugin::{
    MediaCandidate, ResolveMediaParams, ResolveMediaResult, ResourceIdentityDto, methods,
};
use trace_domain::ids::OperationId;

const RESOLVE_DEADLINE_MS: u64 = 30_000;
const SEGMENTS_MAX: usize = 1000;

/// The media group a job works on.
struct Group {
    entry_id: String,
    revision_id: String,
    name: String,
    source: Source,
    platform: String,
}

/// What the adapter offered for the group and the rules it is judged by.
struct Resolved {
    job: ClaimedJob,
    context: AcquisitionContext,
    plan: ResolveMediaResult,
    requirement: QualityRequirement,
    preference: bool,
    wait: i64,
    resources: Vec<ResourceIdentityDto>,
}

/// One acquired candidate: reused from an earlier variant, or freshly prepared.
enum Step {
    Reused {
        sha: String,
    },
    Fresh {
        op_id: OperationId,
        sha: String,
        /// The candidate the file stands for when it is not the one the plan offered: the
        /// video the media tool downloaded because the site refused the offered address.
        replaced_by: Option<Box<MediaCandidate>>,
    },
}

/// Hashes by attachment, plus segments waiting until their ordered whole is verified.
#[derive(Default)]
struct Acquired {
    hashes: HashMap<String, String>,
    pending_segments: Vec<(MediaCandidate, String, OperationId, String)>,
}

pub(crate) async fn run_media(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let group = load_group(core, job).await?;
    let mut items = load_items(core, &group).await?;
    let _budget = acquire_budget(core, &group.source.account_id).await?;
    core.metadata_pacing(&group.source.namespace).await;
    let mut resolved = resolve(core, job, &group, &items).await?;
    settle_plan(core, &group, &mut resolved).await?;
    let segment_groups = expand_segments(core, &group, &mut items, &mut resolved).await?;
    let job = upgrade_job(core, &resolved.job, &group).await?;
    let mut acquired = acquire_all(core, &job, &group, &items, &resolved).await?;
    verify_segment_groups(core, segment_groups, &acquired).await?;
    for (candidate, media_id, op_id, sha) in std::mem::take(&mut acquired.pending_segments) {
        acquire::commit_candidate(core, &job, &candidate, &media_id, &op_id, &sha).await?;
    }
    finish(core, &job, &group, &resolved).await
}

async fn load_group(core: &Core, job: &ClaimedJob) -> Result<Group, CoreError> {
    let field = |name: &str| {
        job.payload
            .get(name)
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string()
    };
    let entry_id = field("entry_id");
    let lookup = entry_id.clone();
    let source = core
        .db
        .read(move |c| store::entries::source(c, &lookup))
        .await?;
    let platform = source.namespace.split(':').next().unwrap_or("").to_string();
    Ok(Group {
        entry_id,
        revision_id: field("revision_id"),
        name: field("group"),
        source,
        platform,
    })
}

async fn load_items(core: &Core, g: &Group) -> Result<Vec<Item>, CoreError> {
    let (revision, name) = (g.revision_id.clone(), g.name.clone());
    let items = core
        .db
        .read(move |c| store::media::group_items(c, &revision, &name))
        .await?;
    if items.is_empty() {
        return Err(CoreError::NotFound("media group".into()));
    }
    Ok(items)
}

/// Budget: two media jobs overall and one per account.
async fn acquire_budget(
    core: &Core,
    account_id: &str,
) -> Result<(OwnedSemaphorePermit, OwnedSemaphorePermit), CoreError> {
    let closed = |_| CoreError::Internal("semaphore closed".into());
    let global = core
        .media_global
        .clone()
        .acquire_owned()
        .await
        .map_err(closed)?;
    let account = core
        .account_semaphore(account_id)
        .await
        .acquire_owned()
        .await
        .map_err(closed)?;
    Ok((global, account))
}

fn resource_identities(items: &[Item], group: &str) -> Vec<ResourceIdentityDto> {
    items
        .iter()
        .filter(|i| i.role != "video_segment")
        .map(|i| ResourceIdentityDto {
            role: i.role.clone(),
            source_attachment_id: i.attachment_id.clone(),
            position: i.position,
            title: None,
            group_key: Some(group.to_owned()),
            required: i.required,
        })
        .collect()
}

/// Asks the adapter how to get the group's resources, under a fresh acquisition context.
async fn resolve(
    core: &Core,
    job: &ClaimedJob,
    g: &Group,
    items: &[Item],
) -> Result<Resolved, CoreError> {
    let resources = resource_identities(items, &g.name);
    let (default_preference, wait) = crate::quality::policy(core).await?;
    let context = crate::acquisitions::begin(core, job, &g.platform, &g.entry_id).await?;
    crate::acquisitions::require_access(&context)?;
    let requirement = crate::quality::requirement(core, job).await?;
    let preference = requirement.prefer_fps.unwrap_or(default_preference);
    let params = ResolveMediaParams {
        native_entry_key: g.source.native_key.clone(),
        native_kind: g.source.native_kind.clone(),
        resources: resources.clone(),
        quality_hint: json!({
            "prefer_fps": preference,
            "all_variants": job.payload["source_request_id"].is_string(),
        }),
    };
    let value = core
        .adapter_call(
            job,
            &g.platform,
            methods::RESOLVE_MEDIA,
            serde_json::to_value(&params).unwrap_or_default(),
            Some(RESOLVE_DEADLINE_MS),
        )
        .await?;
    let mut plan: ResolveMediaResult = serde_json::from_value(value)
        .map_err(|e| CoreError::InvalidInput(format!("bad plan: {e}")))?;
    crate::media_tools::complete(
        core,
        &g.platform,
        &g.source.native_key,
        &resources,
        &params.quality_hint,
        &mut plan,
        job.payload["acquisition_context_id"].as_str(),
    )
    .await?;
    let context =
        crate::acquisitions::resolution_context(core, job, &g.entry_id, context, &plan.notes)
            .await?;
    let job = with_context(job, &context.context_id);
    for candidate in &mut plan.candidates {
        candidate.claimed_spec = trace_contracts::content::safe_metadata(&candidate.claimed_spec);
    }
    if job.payload["source_request_id"].is_string() {
        crate::source_acquisition::select_media(core, &job, &mut plan).await?;
    }
    Ok(Resolved {
        job,
        context,
        plan,
        requirement,
        preference,
        wait,
        resources,
    })
}

/// Records every offer, narrows the plan to what the quality request accepts, and settles
/// whether an independent audio resource is required.
async fn settle_plan(core: &Core, g: &Group, r: &mut Resolved) -> Result<(), CoreError> {
    let requested = serde_json::to_value(&r.requirement)?.to_string();
    let (revision, identities, offers) = (
        g.revision_id.clone(),
        r.resources.clone(),
        r.plan.candidates.clone(),
    );
    let (guard, preference) = (r.job.clone(), r.preference);
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::quality::record_offers(tx, &revision, &identities, &offers, preference)?;
            store::media::record_requested_quality(tx, &revision, &requested)
        })
        .await?;
    crate::quality::filter_required(&r.requirement, &r.resources, &mut r.plan.candidates)?;
    let direct = r.job.payload["source_request_id"].is_string();
    let authenticated = r.context.auth_state == AuthenticationState::Authenticated;
    if !direct && (authenticated || r.job.payload["completion"] == true) {
        crate::upgrades::highest_candidates(&mut r.plan.candidates, r.preference);
    }
    settle_audio(core, g, r).await
}

/// Explicit evidence may waive an independent audio resource, never a missing candidate alone.
async fn settle_audio(core: &Core, g: &Group, r: &Resolved) -> Result<(), CoreError> {
    let silent = r.plan.notes["silent_or_muxed"]
        .as_array()
        .is_some_and(|a| a.iter().any(|v| v.as_str() == Some(g.name.as_str())));
    let audio_present = r.plan.candidates.iter().any(|c| c.resource.role == "audio");
    let evidence = json!({
        "resolver": "v1",
        "audio_present": audio_present,
        "silent_or_muxed": silent,
    })
    .to_string();
    let (revision, name, guard) = (g.revision_id.clone(), g.name.clone(), r.job.clone());
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let required = !(silent && !audio_present);
            store::media::set_audio_requirement(tx, &revision, &name, required, &evidence)
        })
        .await
}

/// A single-segment candidate takes the segment's size and digest; the two must not disagree.
fn single_segment(candidate: &MediaCandidate) -> Result<MediaCandidate, CoreError> {
    let mut single = candidate.clone();
    let segment = &candidate.segments[0];
    let size_conflict = single
        .expected_size
        .zip(segment.expected_size)
        .is_some_and(|(a, b)| a != b);
    let digest_conflict = single
        .expected_sha256
        .as_ref()
        .zip(segment.expected_sha256.as_ref())
        .is_some_and(|(a, b)| a != b);
    if size_conflict || digest_conflict {
        return Err(CoreError::InvalidInput(
            "conflicting segment validation".into(),
        ));
    }
    single.expected_size = single.expected_size.or(segment.expected_size);
    single.expected_sha256 = single
        .expected_sha256
        .or_else(|| segment.expected_sha256.clone());
    Ok(single)
}

/// A multi-segment candidate split into ordered segment candidates.
struct Split {
    candidates: Vec<MediaCandidate>,
    /// New media rows: id, attachment, position.
    rows: Vec<(String, String, i64)>,
    attachments: Vec<String>,
    evidence: String,
}

/// Each container segment remains an independent ordered resource. Never concatenate bytes.
fn split_segments(candidate: &MediaCandidate, original_media: &str) -> Split {
    let mut split = Split {
        candidates: Vec::new(),
        rows: Vec::new(),
        attachments: Vec::new(),
        evidence: String::new(),
    };
    for (index, segment) in candidate.segments.iter().enumerate() {
        let mut c = candidate.clone();
        c.resource.source_attachment_id = format!(
            "{}:segment:{index}",
            candidate.resource.source_attachment_id
        );
        c.resource.role = "video_segment".into();
        c.resource.position = index as i64;
        c.resource.required = true;
        c.segments = vec![segment.clone()];
        c.expected_size = segment.expected_size;
        c.expected_sha256 = segment.expected_sha256.clone();
        split
            .attachments
            .push(c.resource.source_attachment_id.clone());
        split.rows.push((
            format!("{original_media}-segment-{index}"),
            c.resource.source_attachment_id.clone(),
            index as i64,
        ));
        split.candidates.push(c);
    }
    split.evidence = json!({
        "validation": "ordered-segments-v1",
        "expected_size": candidate.expected_size,
        "expected_sha256": candidate.expected_sha256,
        "segments": candidate.segments.iter().map(|s| json!({
            "expected_size": s.expected_size,
            "expected_sha256": s.expected_sha256,
        })).collect::<Vec<_>>(),
    })
    .to_string();
    split
}

/// Replaces multi-segment candidates by their segments; returns each original with the
/// attachments of its segments so the ordered whole can be verified later.
async fn expand_segments(
    core: &Core,
    g: &Group,
    items: &mut Vec<Item>,
    r: &mut Resolved,
) -> Result<Vec<(MediaCandidate, Vec<String>)>, CoreError> {
    let mut expanded = Vec::new();
    let mut groups = Vec::new();
    for candidate in &r.plan.candidates {
        match candidate.segments.len() {
            0 => {
                return Err(CoreError::InvalidInput(
                    "invalid media segment count".into(),
                ));
            }
            n if n > SEGMENTS_MAX => {
                return Err(CoreError::InvalidInput(
                    "invalid media segment count".into(),
                ));
            }
            1 => expanded.push(single_segment(candidate)?),
            _ => {
                let original = items
                    .iter()
                    .find(|i| i.attachment_id == candidate.resource.source_attachment_id)
                    .map(|i| i.media_id.clone())
                    .ok_or_else(|| CoreError::InvalidInput("segment resource".into()))?;
                let split = split_segments(candidate, &original);
                for (media_id, attachment_id, index) in &split.rows {
                    items.push(Item {
                        media_id: media_id.clone(),
                        role: "video_segment".into(),
                        position: *index,
                        attachment_id: attachment_id.clone(),
                        required: true,
                    });
                }
                let (revision, name, guard) =
                    (g.revision_id.clone(), g.name.clone(), r.job.clone());
                let (rows, evidence) = (split.rows, split.evidence);
                core.db
                    .write(move |tx| {
                        guard.check(tx)?;
                        store::media::split_into_segments(
                            tx, &original, &evidence, &revision, &name, &rows,
                        )
                    })
                    .await?;
                expanded.extend(split.candidates);
                groups.push((candidate.clone(), split.attachments));
            }
        }
    }
    r.plan.candidates = expanded;
    Ok(groups)
}

/// The job, carrying the upgrade candidate it completes when there is one.
async fn upgrade_job(core: &Core, job: &ClaimedJob, g: &Group) -> Result<ClaimedJob, CoreError> {
    let mut upgrade = job.clone();
    if let Some(id) = crate::upgrades::prepare(core, job, &g.revision_id, &g.name).await? {
        upgrade.payload["upgrade_candidate_id"] = Value::String(id);
    }
    Ok(upgrade)
}

/// Representations are compared by their identifying fields; a direct request also keys on
/// what the offer exposes to the user.
fn comparable_spec(direct: bool, spec: &Value) -> Value {
    if direct {
        crate::source_acquisition::offer_spec(spec)
    } else {
        acquisition_spec(spec)
    }
}

async fn acquire_all(
    core: &Core,
    job: &ClaimedJob,
    g: &Group,
    items: &[Item],
    r: &Resolved,
) -> Result<Acquired, CoreError> {
    let rules = platform_rules(&host_rules(&core.profile), &g.platform);
    let mut acquired = Acquired::default();
    for candidate in &r.plan.candidates {
        let attachment = &candidate.resource.source_attachment_id;
        let Some(item) = items.iter().find(|i| &i.attachment_id == attachment) else {
            continue;
        };
        let media_id = item.media_id.clone();
        match acquire_candidate(core, job, &rules, (g, r), candidate, &media_id).await? {
            Step::Reused { sha } => {
                acquired.hashes.insert(attachment.clone(), sha);
            }
            Step::Fresh {
                op_id,
                sha,
                replaced_by,
            } => {
                // The file the tool downloaded in place of the refused one is committed as what
                // it is.
                let candidate = replaced_by.as_deref().unwrap_or(candidate);
                acquired.hashes.insert(attachment.clone(), sha.clone());
                if candidate.resource.role == "video_segment" {
                    acquired
                        .pending_segments
                        .push((candidate.clone(), media_id, op_id, sha));
                } else {
                    acquire::commit_candidate(core, job, candidate, &media_id, &op_id, &sha)
                        .await?;
                }
            }
        }
    }
    Ok(acquired)
}

async fn acquire_candidate(
    core: &Core,
    job: &ClaimedJob,
    rules: &HostRules,
    (g, r): (&Group, &Resolved),
    candidate: &MediaCandidate,
    media_id: &str,
) -> Result<Step, CoreError> {
    if let Some(sha) = reuse_stored(core, job, r, candidate, media_id).await? {
        return Ok(Step::Reused { sha });
    }
    let original = acquire::owned_operation(core, job, media_id, candidate).await?;
    let mut op_id = acquire::uncommitted_operation(core, &original).await?;
    let mut replaced_by = None;
    let sha = if core.objects.ready(&core.db, &op_id, job).await? {
        let op = op_id.as_str().to_owned();
        core.db
            .read(move |c| store::media::received_sha256(c, &op))
            .await?
    } else {
        match acquire::download(core, job, rules, candidate, media_id, &op_id).await {
            Ok(sha) => sha,
            Err(refusal)
                if crate::media_tools::replaceable(
                    &g.platform,
                    &candidate.resource.role,
                    &refusal,
                ) =>
            {
                let taken = acquire::download_by_tool(
                    core,
                    job,
                    (&g.platform, &g.source.native_key),
                    candidate,
                    media_id,
                )
                .await?;
                let Some((tool_op, tool_candidate, sha)) = taken else {
                    return Err(refusal);
                };
                (op_id, replaced_by) = (tool_op, Some(Box::new(tool_candidate)));
                sha
            }
            Err(error) => return Err(error),
        }
    };
    let promised = replaced_by.as_deref().unwrap_or(candidate);
    acquire::verify_files(core, promised, std::slice::from_ref(&sha)).await?;
    Ok(Step::Fresh {
        op_id,
        sha,
        replaced_by,
    })
}

/// An earlier variant of the same representation makes an outbox replay download nothing (C07).
async fn reuse_stored(
    core: &Core,
    job: &ClaimedJob,
    r: &Resolved,
    candidate: &MediaCandidate,
    media_id: &str,
) -> Result<Option<String>, CoreError> {
    let direct = job.payload["source_request_id"].is_string();
    let claimed = comparable_spec(direct, &candidate.claimed_spec);
    let (media, representation) = (media_id.to_owned(), candidate.representation.clone());
    let (auth, credential) = (
        r.context.auth_state.as_str(),
        r.context.credential_id.clone(),
    );
    let (generation, work) = (r.context.credential_generation, job.clone());
    let stored = core
        .db
        .read(move |c| {
            let by = Provenance {
                auth_state: auth,
                credential_id: credential.as_deref(),
                credential_generation: generation,
                job_id: &work.job_id,
                job_generation: work.generation,
            };
            store::media::stored_variants(c, &media, &representation, &by)
        })
        .await?;
    let found = stored.into_iter().find(|v| {
        serde_json::from_str::<Value>(&v.claimed_spec)
            .is_ok_and(|spec| comparable_spec(direct, &spec) == claimed)
    });
    let Some(variant) = found else {
        return Ok(None);
    };
    let sha = crate::paths::blob_sha(&variant.blob_id)?.to_owned();
    let video_like = matches!(
        candidate.resource.role.as_str(),
        "video" | "motion" | "video_segment"
    );
    if direct || (r.requirement.strict && video_like) {
        recheck_reused(core, job, candidate, &sha, direct).await?;
    }
    if acquire::verify_files(core, candidate, std::slice::from_ref(&sha))
        .await
        .is_err()
    {
        return Ok(None);
    }
    crate::upgrades::reused(
        core,
        job,
        media_id,
        &variant.variant_id,
        variant.same_provenance,
    )
    .await?;
    Ok(Some(sha))
}

/// A reused file is measured again against the current request before it counts.
async fn recheck_reused(
    core: &Core,
    job: &ClaimedJob,
    candidate: &MediaCandidate,
    sha: &str,
    direct: bool,
) -> Result<(), CoreError> {
    let path = core.objects.object_path(sha)?;
    let measured = core.media_backend.probe(&path, &core.cancel).await?;
    if direct {
        crate::source_acquisition::validate_measured(candidate, &serde_json::to_value(&measured)?)?;
    }
    crate::quality::enforce_measured(
        core,
        job,
        &candidate.resource.role,
        &serde_json::to_value(measured)?,
    )
    .await
}

/// Validates the ordered whole before any newly prepared segment counts toward completeness.
/// This also supports old protocol peers that provide only an aggregate size and digest.
async fn verify_segment_groups(
    core: &Core,
    groups: Vec<(MediaCandidate, Vec<String>)>,
    acquired: &Acquired,
) -> Result<(), CoreError> {
    for (candidate, attachments) in groups {
        let hashes = attachments
            .iter()
            .map(|att| {
                acquired
                    .hashes
                    .get(att)
                    .cloned()
                    .ok_or_else(|| CoreError::InvalidInput("unresolved segment".into()))
            })
            .collect::<Result<Vec<_>, _>>()?;
        acquire::verify_files(core, &candidate, &hashes).await?;
    }
    Ok(())
}

/// An upgrade job ends with its candidate; any other job must have every required resource.
async fn finish(core: &Core, job: &ClaimedJob, g: &Group, r: &Resolved) -> Result<(), CoreError> {
    if job.payload["upgrade_candidate_id"].is_string() {
        return crate::upgrades::finish(core, job).await;
    }
    let (revision, name) = (g.revision_id.clone(), g.name.clone());
    let missing = core
        .db
        .read(move |c| store::media::missing_required(c, &revision, &name))
        .await?;
    if missing > 0 {
        return Err(CoreError::Network(
            "required media missing from resolution".into(),
        ));
    }
    let (revision, guard) = (g.revision_id.clone(), job.clone());
    let (preference, wait) = (r.preference, r.wait);
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::quality::evaluate(tx, &revision, preference, wait)
        })
        .await?;
    create_playback_mux(core, job, &g.revision_id, &g.name).await
}
