//! Link preview, explicit selection and durable one-source acquisition. Network
//! plans are ephemeral; actual media and live fragments use the existing Core.
use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
};
use rusqlite::{Connection, OptionalExtension, params};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use trace_contracts::{
    plugin::{LiveStream, MediaCandidate, ResolveMediaResult},
    source::{ResolvedSource, SourceConfirmArgs, SourcePreviewArgs},
};

fn conflict() -> CoreError {
    CoreError::VersionConflict("source options changed; preview again".into())
}
fn digest(v: &Value) -> String {
    Sha256::digest(v.to_string().as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
pub fn platform(input: &str) -> Result<&'static str, CoreError> {
    let u = url::Url::parse(input)
        .map_err(|_| CoreError::InvalidInput("video or live URL required".into()))?;
    if input.len() > 4096
        || u.scheme() != "https"
        || !u.username().is_empty()
        || u.password().is_some()
        || u.port().is_some()
    {
        return Err(CoreError::InvalidInput("invalid source URL".into()));
    }
    match u.host_str() {
        Some(
            "www.bilibili.com" | "bilibili.com" | "m.bilibili.com" | "live.bilibili.com" | "b23.tv",
        ) => Ok("bilibili"),
        Some("www.douyin.com" | "live.douyin.com" | "v.douyin.com" | "www.iesdouyin.com") => {
            Ok("douyin")
        }
        _ => Err(CoreError::Unsupported("source site".into())),
    }
}
pub fn offer(candidate: &MediaCandidate) -> Value {
    let spec = offer_spec(&candidate.claimed_spec);
    let mut value = json!({"attachment_id":candidate.resource.source_attachment_id,"role":candidate.resource.role,"group":candidate.resource.group_key,"required":candidate.resource.required,"representation":candidate.representation,"spec":spec,"kind":candidate.kind,"bytes":candidate.expected_size,"sha256":candidate.expected_sha256,"segments":candidate.segments.iter().map(|s|json!({"bytes":s.expected_size,"sha256":s.expected_sha256})).collect::<Vec<_>>()});
    value["id"] = json!(digest(&value));
    value["title"] = json!(candidate.resource.title);
    value["position"] = json!(candidate.resource.position);
    value
}
pub(crate) fn offer_spec(claim: &Value) -> Value {
    // Safe metadata normalizes numeric *_id fields to strings. Preview and
    // execution must fingerprint that same representation, including DASH IDs.
    let claim = trace_contracts::content::safe_metadata(claim);
    let mut spec = crate::core::acquisition_spec(&claim);
    for key in ["bandwidth", "quality_id", "container"] {
        if let Some(v) = claim.get(key).filter(|v| !v.is_null()) {
            spec[key] = v.clone();
        }
    }
    spec
}
pub fn live_offer(stream: &LiveStream) -> Value {
    let mut value = json!({"role":"live","attachment_id":"live","required":true,"protocol":stream.protocol,"quality":stream.quality,"audio_only":stream.audio_only});
    value["id"] = json!(digest(&value));
    value
}
pub fn live_meets_minimum(measured: &Value) -> bool {
    measured["width"]
        .as_u64()
        .zip(measured["height"].as_u64())
        .is_some_and(|(w, h)| w.min(h) >= 720)
        && measured["codec"].as_str().is_some()
}
pub fn validate_measured(candidate: &MediaCandidate, measured: &Value) -> Result<(), CoreError> {
    if !matches!(
        candidate.resource.role.as_str(),
        "video" | "video_segment" | "audio" | "motion"
    ) {
        return Ok(());
    }
    let claim = &candidate.claimed_spec;
    if matches!(
        candidate.resource.role.as_str(),
        "video" | "video_segment" | "motion"
    ) {
        for key in ["width", "height"] {
            if let Some(wanted) = claim[key].as_u64().filter(|n| *n > 0)
                && measured[key].as_u64() != Some(wanted)
            {
                return Err(conflict());
            }
        }
        if measured["codec"].as_str().is_none() {
            return Err(conflict());
        }
        let family = |codec: &str| {
            if codec.starts_with("avc") || codec == "h264" {
                "h264"
            } else if codec.starts_with("hev") || codec.starts_with("hvc") || codec == "hevc" {
                "hevc"
            } else if codec.starts_with("av01") || codec == "av1" {
                "av1"
            } else {
                "unknown"
            }
        };
        if let Some(codec) = claim["codec"].as_str().or_else(|| claim["codecs"].as_str())
            && family(codec) != "unknown"
            && family(codec) != family(measured["codec"].as_str().unwrap_or(""))
        {
            return Err(conflict());
        }
        if let Some(wanted) = trace_contracts::content::source_fps(claim)
            && measured["fps"]
                .as_f64()
                .is_none_or(|fps| fps < wanted * 0.97)
        {
            return Err(conflict());
        }
    }
    if (candidate.resource.role == "audio" || candidate.kind == "muxed" && claim["silent"] != true)
        && measured["audio_tracks"].as_u64().unwrap_or(0) == 0
    {
        return Err(conflict());
    }
    Ok(())
}
fn source_snapshot(source: &ResolvedSource) -> Result<Value, CoreError> {
    if source.native_account_key.is_empty() || source.native_account_key.len() > 256 {
        return Err(CoreError::InvalidInput("source owner identity".into()));
    }
    let (key, title, manifest, mut offers) = if source.kind == "video" {
        let detail = source.detail.as_ref().ok_or_else(conflict)?;
        detail
            .document
            .clone()
            .validate(&detail.resources)
            .map_err(|s| CoreError::InvalidInput(s.into()))?;
        if detail.title.len() > 4096
            || detail.normalized_body.len() > 512 * 1024
            || serde_json::to_vec(&detail.document)?.len() > 512 * 1024
        {
            return Err(CoreError::InvalidInput("source document budget".into()));
        }
        if detail.completeness != "full"
            || detail.resources.len() > 256
            || detail.native_kind != "video"
        {
            return Err(CoreError::SourceUnavailable(
                "incomplete video detail".into(),
            ));
        }
        let manifest = json!({"account":source.native_account_key,"resources":detail.resources,"title":detail.title,"body":digest(&json!(detail.normalized_body)),"document":digest(&serde_json::to_value(&detail.document)?)});
        (
            detail.native_entry_key.clone(),
            detail.title.clone(),
            digest(&manifest),
            source
                .media
                .candidates
                .iter()
                .map(offer)
                .collect::<Vec<_>>(),
        )
    } else {
        let live = source.live.as_ref().ok_or_else(conflict)?;
        if live.state != "live" {
            return Err(CoreError::SourceUnavailable("room is not live".into()));
        }
        let manifest = digest(
            &json!({"room":live.room_key,"session":live.source_session,"account":source.native_account_key}),
        );
        (
            live.room_key.clone(),
            live.title.clone(),
            manifest,
            live.streams
                .iter()
                .filter(|s| !s.audio_only)
                .map(live_offer)
                .collect(),
        )
    };
    if offers.is_empty() || offers.len() > 512 {
        return Err(CoreError::SourceUnavailable(
            "source offer budget or unavailable video".into(),
        ));
    }
    let mut seen = std::collections::BTreeSet::new();
    offers.retain(|v| seen.insert(v["id"].as_str().unwrap().to_owned()));
    if let Some(detail) = &source.detail {
        for r in detail.resources.iter().filter(|r| r.required) {
            if !offers
                .iter()
                .any(|v| v["attachment_id"] == r.source_attachment_id)
            {
                return Err(CoreError::SourceUnavailable(
                    "necessary source resource unavailable".into(),
                ));
            }
        }
    }
    let duration = source
        .source_duration_seconds
        .filter(|n| n.is_finite() && *n > 0.0);
    Ok(
        json!({"native_key":key,"title":title,"manifest":manifest,"offers":offers,"source_duration_seconds":duration}),
    )
}
type Credential = (String, i64, String, String);
type ActiveRecording = (String, String, String, Option<String>, Option<i64>);
fn credential(
    c: &Connection,
    id: Option<&str>,
    site: &str,
) -> Result<Option<Credential>, CoreError> {
    let Some(id) = id else { return Ok(None) };
    let value=c.query_row("SELECT credential_id,generation,vault_ref,status FROM credentials_state WHERE credential_id=?1 AND platform=?2",params![id,site],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get::<_,String>(3)?))).optional()?;
    value
        .filter(|v| matches!(v.3.as_str(), "valid" | "authenticated"))
        .map(Some)
        .ok_or_else(|| CoreError::AuthRequired("credential requires validation".into()))
}
pub async fn preview(core: &Core, args: SourcePreviewArgs) -> Result<Value, CoreError> {
    let site = platform(&args.url)?.to_owned();
    let cid = args.credential_id.clone();
    let p = site.clone();
    let identity = core
        .db
        .read(move |c| credential(c, cid.as_deref(), &p))
        .await?;
    let scope = if let Some((id, generation, reference, _)) = &identity {
        let value = String::from_utf8(core.vault.load(reference)?)
            .map_err(|_| CoreError::AuthRequired("credential encoding".into()))?;
        let scope = format!("{id}/g{generation}");
        core.cache_cookie(&format!("{site}/{scope}"), value);
        Some(scope)
    } else {
        None
    };
    let mut validated = None;
    if scope.is_some() {
        core.metadata_pacing(&site).await;
        let proof = core
            .host
            .call_scoped(
                &site,
                scope.as_deref(),
                "probe_auth",
                json!({}),
                Some(30000),
            )
            .await?;
        if !matches!(proof["state"].as_str(), Some("valid" | "authenticated")) {
            return Err(CoreError::AuthRequired(
                "source identity is not authenticated".into(),
            ));
        }
        validated = Some(jobs::now());
    }
    core.metadata_pacing(&site).await;
    let value = core
        .host
        .call_scoped(
            &site,
            scope.as_deref(),
            "resolve_source",
            json!({"input":args.url}),
            Some(30000),
        )
        .await?;
    let source: ResolvedSource = serde_json::from_value(value)?;
    if platform(&source.canonical_url)? != site {
        return Err(CoreError::InvalidInput("cross-site source identity".into()));
    }
    save_preview(
        core,
        &site,
        source,
        identity.map(|(id, g, _, _)| (id, g)),
        validated,
    )
    .await
}
async fn save_preview(
    core: &Core,
    site: &str,
    source: ResolvedSource,
    identity: Option<(String, i64)>,
    validated: Option<String>,
) -> Result<Value, CoreError> {
    if identity.is_some() && source.media.notes["authentication"] == "anonymous" {
        return Err(conflict());
    }
    let snapshot = source_snapshot(&source)?;
    let id = format!("source-{}", uuid::Uuid::new_v4());
    let platform = site.to_owned();
    let expiry = (time::OffsetDateTime::now_utc() + time::Duration::minutes(5))
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|_| conflict())?;
    let auth = if identity.is_some() {
        "authenticated"
    } else {
        "anonymous"
    };
    core.db.write(move|tx|{
        let current=credential(tx,identity.as_ref().map(|v|v.0.as_str()),&platform)?.map(|(id,g,_,_)|(id,g));
        if current!=identity {return Err(conflict());}
        tx.execute("INSERT INTO source_requests(request_id,platform,kind,source_url,native_key,account_key,account_name,credential_id,credential_generation,authentication,validated_at,snapshot,created_at,expires_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14)",params![id,platform,source.kind,source.canonical_url,snapshot["native_key"].as_str(),source.native_account_key,source.display_name,identity.as_ref().map(|v|v.0.as_str()),identity.as_ref().map(|v|v.1),auth,validated,snapshot.to_string(),jobs::now(),expiry])?;
        Ok(json!({"preview_id":id,"kind":source.kind,"platform":platform,"source_url":source.canonical_url,"native_key":snapshot["native_key"],"title":snapshot["title"],"account_name":source.display_name,"authentication":auth,"validated_at":validated,"expires_at":expiry,"offers":snapshot["offers"]}))
    }).await
}
fn selection(snapshot: &Value, ids: &[String]) -> Result<Vec<Value>, CoreError> {
    if ids.is_empty() || ids.len() > 256 {
        return Err(CoreError::InvalidInput("choose source resources".into()));
    }
    let offers = snapshot["offers"].as_array().ok_or_else(conflict)?;
    let mut resources = std::collections::BTreeSet::new();
    let mut result = Vec::new();
    for id in ids {
        let value = offers
            .iter()
            .find(|v| v["id"] == *id)
            .ok_or_else(conflict)?;
        if !resources.insert(
            value["attachment_id"]
                .as_str()
                .ok_or_else(conflict)?
                .to_owned(),
        ) {
            return Err(CoreError::InvalidInput(
                "one representation per resource".into(),
            ));
        }
        result.push(value.clone());
    }
    if offers.iter().any(|v| {
        v["required"] == true && !resources.contains(v["attachment_id"].as_str().unwrap_or(""))
    }) {
        return Err(CoreError::InvalidInput(
            "necessary source resource not selected".into(),
        ));
    }
    Ok(result)
}
pub async fn confirm(core: &Core, args: SourceConfirmArgs) -> Result<Value, CoreError> {
    core.db.write(move|tx|{
        let row=tx.query_row("SELECT platform,kind,account_key,account_name,credential_id,credential_generation,snapshot,state,expires_at,entry_id,job_id,scope_id FROM source_requests WHERE request_id=?1",[&args.preview_id],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?,r.get::<_,String>(2)?,r.get::<_,Option<String>>(3)?,r.get::<_,Option<String>>(4)?,r.get::<_,Option<i64>>(5)?,r.get::<_,String>(6)?,r.get::<_,String>(7)?,r.get::<_,String>(8)?,r.get::<_,Option<String>>(9)?,r.get::<_,Option<String>>(10)?,r.get::<_,Option<String>>(11)?)))?;
        if row.7!="preview" {
            let selected:String=tx.query_row("SELECT selected FROM source_requests WHERE request_id=?1",[&args.preview_id],|r|r.get(0))?;
            let values:Vec<Value>=serde_json::from_str(&selected)?;let old:std::collections::BTreeSet<_>=values.iter().filter_map(|v|v["id"].as_str()).collect();let new:std::collections::BTreeSet<_>=args.selected.iter().map(String::as_str).collect();
            if old!=new||row.7=="cancelled" {return Err(conflict());}
            return Ok(json!({"request_id":args.preview_id,"entry_id":row.9,"job_id":row.10,"state":row.7}));
        }
        if row.8<jobs::now(){return Err(conflict());}
        let current=credential(tx,row.4.as_deref(),&row.0)?;
        if current.as_ref().map(|v|v.1)!=row.5 {return Err(conflict());}
        let snapshot:Value=serde_json::from_str(&row.6)?;let chosen=selection(&snapshot,&args.selected)?;
        if row.1=="live" {
            let active:Option<ActiveRecording>=tx.query_row("SELECT r.request_id,r.job_id,r.selected,r.credential_id,r.credential_generation FROM source_requests r JOIN jobs j ON j.job_id=r.job_id LEFT JOIN live_sessions l ON l.session_id=r.session_id WHERE r.platform=?1 AND r.native_key=?2 AND r.kind='live' AND r.state!='cancelled' AND (l.state IN ('waiting','recording','reconnecting','stopping') OR (r.session_id IS NULL AND j.state IN ('queued','leased','running','waiting_retry','waiting_storage','waiting_auth'))) ORDER BY r.created_at LIMIT 1",params![row.0,snapshot["native_key"].as_str()],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?))).optional()?;
            if let Some((id,job,saved,credential,generation))=active {
                if credential!=row.4||generation!=row.5||serde_json::from_str::<Vec<Value>>(&saved)?!=chosen{return Err(conflict());}
                return Ok(json!({"request_id":id,"job_id":job,"state":"already_recording"}));
            }
            let ordinary:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM live_sessions l JOIN entries e ON e.entry_id=l.entry_id WHERE e.namespace=?1 AND l.room_key=?2 AND l.state IN ('waiting','recording','reconnecting','stopping'))",params![row.0,snapshot["native_key"].as_str()],|r|r.get(0))?;
            if ordinary {return Err(CoreError::VersionConflict("room already has an active recording".into()));}
        }
        let timestamp=jobs::now();let account=format!("acct-{}",uuid::Uuid::new_v4());
        tx.execute("INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?2,?3,?4,?5)",params![account,row.0,row.2,row.3,timestamp])?;
        let account:String=tx.query_row("SELECT account_id FROM source_accounts WHERE namespace=?1 AND native_account_key=?2",params![row.0,row.2],|r|r.get(0))?;
        let scope=format!("once-{}",args.preview_id);
        // Internal one-source control scope. The schema prohibits scan jobs;
        // ordinary subscription APIs and login completion exclude these rows.
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,quality_json,acquisition_kind,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6,'one_shot',?7,?7)",params![scope,account,format!("source:{}",args.preview_id),json!({"kind":"one_shot","source_id":snapshot["native_key"]}).to_string(),row.4,if row.1=="live"{json!({"min_height":720,"strict":true}).to_string()}else{"{}".into()},timestamp])?;
        let entry=if row.1=="video"{
            let id=format!("ent-{}",uuid::Uuid::new_v4());
            tx.execute("INSERT OR IGNORE INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,?2,'video',?3,?4,?5)",params![id,row.0,snapshot["native_key"].as_str(),account,timestamp])?;
            let (id,deleted):(String,bool)=tx.query_row("SELECT entry_id,lifecycle='deleted' OR media_suppressed=1 FROM entries WHERE namespace=?1 AND native_kind='video' AND native_entry_key=?2",params![row.0,snapshot["native_key"].as_str()],|r|Ok((r.get(0)?,r.get(1)?)))?;
            if deleted {return Err(conflict());}Some(id)
        }else{None};
        let job=format!("job-{}",uuid::Uuid::new_v4());let payload=json!({"source_request_id":args.preview_id,"source_request_generation":1,"subscription_id":scope,"entry_id":entry,"credential_id":row.4,"credential_generation":row.5});
        tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'source_acquire',?3,?4,'queued',1,?5,?5)",params![job,format!("source:{}",args.preview_id),entry.as_deref().unwrap_or(&args.preview_id),payload.to_string(),timestamp])?;
        tx.execute("UPDATE source_requests SET state='queued',scope_id=?2,entry_id=?3,job_id=?4,selected=?5 WHERE request_id=?1",params![args.preview_id,scope,entry,job,serde_json::to_string(&chosen)?])?;
        Ok(json!({"request_id":args.preview_id,"entry_id":entry,"job_id":job,"state":"queued"}))
    }).await
}

async fn request(
    core: &Core,
    job: &ClaimedJob,
) -> Result<(String, String, Value, Vec<Value>), CoreError> {
    let guard = job.clone();
    core.db.read(move|c|{guard.check(c)?;let (platform,url,snapshot,selected):(String,String,String,String)=c.query_row("SELECT platform,source_url,snapshot,selected FROM source_requests WHERE request_id=?1 AND state!='cancelled' AND generation=?2",params![guard.payload["source_request_id"].as_str(),guard.payload["source_request_generation"].as_i64()],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?)))?;Ok((platform,url,serde_json::from_str(&snapshot)?,serde_json::from_str(&selected)?))}).await
}
pub async fn run(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let (platform, url, expected, selected) = request(core, job).await?;
    let mut guard = job.clone();
    if let Some(entry) = job.payload["entry_id"].as_str() {
        let context = crate::acquisitions::begin(core, job, &platform, entry).await?;
        crate::acquisitions::require_access(&context)?;
        guard.payload["acquisition_context_id"] = json!(context.context_id);
    }
    let value = core
        .adapter_call(
            &guard,
            &platform,
            "resolve_source",
            json!({"input":url}),
            Some(30000),
        )
        .await?;
    let mut source: ResolvedSource = serde_json::from_value(value)?;
    let current = source_snapshot(&source)?;
    if expected["manifest"] != current["manifest"]
        || expected["native_key"] != current["native_key"]
    {
        return Err(conflict());
    }
    selection(
        &current,
        &selected
            .iter()
            .map(|v| v["id"].as_str().unwrap().to_owned())
            .collect::<Vec<_>>(),
    )?;
    if source.kind == "video" {
        let entry = job.payload["entry_id"].as_str().ok_or_else(conflict)?;
        let e = entry.to_owned();
        let account: String = core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT account_id FROM entries WHERE entry_id=?1",
                    [e],
                    |r| r.get(0),
                )?)
            })
            .await?;
        let mut detail = source.detail.take().ok_or_else(conflict)?;
        // Optional attachments are explicit choices; required resources remain
        // in the revision, including every ordered segment of a selected stream.
        detail.resources.retain(|r| {
            r.required
                || selected
                    .iter()
                    .any(|v| v["attachment_id"] == r.source_attachment_id)
        });
        crate::core::persist_detail(core, &guard, entry, &account, detail).await?;
    } else {
        let snapshot = source.live.take().ok_or_else(conflict)?;
        let session = crate::recording::register(core, job, snapshot)
            .await?
            .ok_or_else(conflict)?;
        let id = job.payload["source_request_id"]
            .as_str()
            .ok_or_else(conflict)?
            .to_owned();
        let j = job.clone();
        core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE jobs SET payload=json_set(payload,'$.source_request_id',?2,'$.source_request_generation',?3) WHERE job_key=?1",params![format!("record:{session}"),id,j.payload["source_request_generation"].as_i64()])?;tx.execute("UPDATE source_requests SET session_id=?2,entry_id=(SELECT entry_id FROM live_sessions WHERE session_id=?2),state='running' WHERE request_id=?1",params![id,session])?;Ok(())}).await?;
    }
    Ok(())
}
pub async fn select_media(
    core: &Core,
    job: &ClaimedJob,
    plan: &mut ResolveMediaResult,
) -> Result<(), CoreError> {
    let (_, _, _, selected) = request(core, job).await?;
    let group = job.payload["group"].as_str().ok_or_else(conflict)?;
    let wanted = selected
        .iter()
        .filter(|v| {
            v["group"]
                .as_str()
                .unwrap_or(v["attachment_id"].as_str().unwrap_or(""))
                == group
        })
        .collect::<Vec<_>>();
    if wanted.is_empty() {
        return Err(conflict());
    }
    let mut result = Vec::new();
    for expected in wanted {
        let candidate = plan
            .candidates
            .iter()
            .find(|c| offer(c)["id"] == expected["id"])
            .ok_or_else(conflict)?;
        result.push(candidate.clone());
    }
    plan.candidates = result;
    Ok(())
}
pub async fn resolve_live(
    core: &Core,
    job: &ClaimedJob,
) -> Result<trace_contracts::plugin::LiveSnapshot, CoreError> {
    let (site, url, expected, chosen) = request(core, job).await?;
    let result = core
        .adapter_call(
            job,
            &site,
            "resolve_source",
            json!({"input":url}),
            Some(30000),
        )
        .await;
    let value = match result {
        Ok(value) => value,
        // Some ended Douyin rooms omit owner and room identity. The host only
        // exposes this whitelisted state after resolving the already bound URL.
        Err(CoreError::SourceUnavailable(reason)) if reason == "room is not live" => {
            return Ok(trace_contracts::plugin::LiveSnapshot {
                room_key: expected["native_key"].as_str().ok_or_else(conflict)?.into(),
                state: "offline".into(),
                source_session: None,
                title: expected["title"].as_str().unwrap_or("").into(),
                metadata: json!({"source_state":"offline"}),
                streams: vec![],
            });
        }
        Err(error) => return Err(error),
    };
    let source: ResolvedSource = serde_json::from_value(value)?;
    if let Some(live) = &source.live
        && source.kind == "live"
        && live.state == "offline"
        && expected["native_key"] == live.room_key
    {
        // Preview still rejects an offline room. An existing recording must
        // deliver the confirmed end state to the normal sealing path.
        return Ok(live.clone());
    }
    let current = source_snapshot(&source)?;
    if current["manifest"] != expected["manifest"] {
        return Err(conflict());
    }
    let mut live = source.live.ok_or_else(conflict)?;
    live.streams
        .retain(|s| chosen.iter().any(|v| v["id"] == live_offer(s)["id"]));
    if live.streams.is_empty() {
        return Err(conflict());
    }
    Ok(live)
}
pub async fn list(core: &Core) -> Result<Value, CoreError> {
    core.db.read(|c|{
        let rows=c.prepare("SELECT r.request_id,r.platform,r.kind,r.state,r.entry_id,r.job_id,r.session_id,r.snapshot,r.authentication,r.created_at,l.state,l.generation FROM source_requests r LEFT JOIN live_sessions l ON l.session_id=r.session_id ORDER BY r.created_at DESC,r.request_id LIMIT 50")?.query_map([],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?,r.get::<_,String>(2)?,r.get::<_,String>(3)?,r.get::<_,Option<String>>(4)?,r.get::<_,Option<String>>(5)?,r.get::<_,Option<String>>(6)?,r.get::<_,String>(7)?,r.get::<_,String>(8)?,r.get::<_,String>(9)?,r.get::<_,Option<String>>(10)?,r.get::<_,Option<i64>>(11)?)))?.collect::<Result<Vec<_>,_>>()?;
        let mut items=Vec::new();for r in rows {
            let tasks=c.prepare("SELECT job_id,job_type,state,last_error FROM jobs WHERE json_extract(payload,'$.source_request_id')=?1 ORDER BY created_at,job_id")?.query_map([&r.0],|r|Ok(json!({"job_id":r.get::<_,String>(0)?,"kind":r.get::<_,String>(1)?,"state":r.get::<_,String>(2)?,"error":r.get::<_,Option<String>>(3)?})))?.collect::<Result<Vec<_>,_>>()?;
            let snapshot:Value=serde_json::from_str(&r.7)?;
            let state=if r.3=="cancelled"{"cancelled"}else if tasks.is_empty(){"preview"}else if tasks.iter().all(|t|t["state"]=="succeeded"){"succeeded"}else if tasks.iter().any(|t|matches!(t["state"].as_str(),Some("failed"|"waiting_auth"|"cancelled"))){"needs_attention"}else{"running"};
            items.push(json!({"request_id":r.0,"platform":r.1,"kind":r.2,"state":state,"entry_id":r.4,"job_id":r.5,"session_id":r.6,"title":snapshot["title"],"authentication":r.8,"created_at":r.9,"session_state":r.10,"session_generation":r.11,"tasks":tasks}));
        }Ok(json!({"items":items}))
    }).await
}
pub async fn cancel(core: &Core, id: String) -> Result<Value, CoreError> {
    core.db.write(move|tx|{
        tx.execute("UPDATE source_requests SET state='cancelled',generation=generation+1 WHERE request_id=?1 AND state!='cancelled'",[&id])?;
        tx.execute("UPDATE subscriptions SET state='paused',config_version=config_version+1 WHERE subscription_id=(SELECT scope_id FROM source_requests WHERE request_id=?1)",[&id])?;
        tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE json_extract(payload,'$.source_request_id')=?1 AND state!='succeeded'",[&id])?;
        tx.execute("UPDATE live_sessions SET state='stopping',manual_stopped=1,stop_reason='user_stopped' WHERE session_id=(SELECT session_id FROM source_requests WHERE request_id=?1) AND state!='sealed'",[&id])?;
        Ok(json!({"state":"cancelled"}))
    }).await
}

#[cfg(test)]
#[path = "source_tests.rs"]
mod tests;
