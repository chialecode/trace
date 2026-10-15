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
    plugin::ResolveMediaResult,
    source::{ResolvedSource, SourceConfirmArgs, SourcePreviewArgs},
};

fn conflict() -> CoreError {
    CoreError::VersionConflict("source options changed; preview again".into())
}
pub(crate) fn digest(v: &Value) -> String {
    Sha256::digest(v.to_string().as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
pub fn platform(input: &str) -> Result<&'static str, CoreError> {
    let u = url::Url::parse(input).map_err(|_| {
        CoreError::InvalidInput("a link to an entry, video or live room is required".into())
    })?;
    if input.len() > 4096
        || u.scheme() != "https"
        || !u.username().is_empty()
        || u.password().is_some()
        || u.port().is_some()
    {
        return Err(CoreError::InvalidInput("invalid source URL".into()));
    }
    let host = u.host_str().unwrap_or_default().to_ascii_lowercase();
    trace_domain::sites::site_for_host(&host)
        .map(|site| site.id)
        .ok_or_else(|| CoreError::Unsupported("source site".into()))
}
pub use crate::source_offers::{
    live_meets_minimum, live_offer, offer, offer_spec, validate_measured,
};
fn source_snapshot(source: &ResolvedSource) -> Result<Value, CoreError> {
    if source.native_account_key.is_empty() || source.native_account_key.len() > 256 {
        return Err(CoreError::InvalidInput("source owner identity".into()));
    }
    let (key, title, manifest, mut offers) = if matches!(source.kind.as_str(), "video" | "entry") {
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
            || (source.kind == "video" && detail.native_kind != "video")
        {
            // Only the local fault log carries these words; a note the adapter attached to its
            // evidence (names of the page's fields, never values) says what was missing.
            let note: String = detail.source_version_evidence["media_shape"]
                .as_str()
                .unwrap_or_default()
                .chars()
                .filter(|c| c.is_ascii_graphic() || *c == ' ')
                .take(360)
                .collect();
            return Err(CoreError::SourceUnavailable(format!(
                "incomplete video detail ({}, {}){}{note}",
                detail.completeness,
                detail.native_kind,
                if note.is_empty() { "" } else { ": " }
            )));
        }
        let manifest = json!({
            "account": source.native_account_key,
            "resources": detail.resources,
            "title": detail.title,
            "body": digest(&json!(detail.normalized_body)),
            "document": digest(&serde_json::to_value(&detail.document)?),
        });
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
    // An entry that is only text has nothing to choose; a video or room without a stream is
    // not worth keeping.
    let text_only = source.kind == "entry"
        && source
            .detail
            .as_ref()
            .is_some_and(|d| d.resources.is_empty());
    if (offers.is_empty() && !text_only) || offers.len() > 512 {
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
    let native_kind = source
        .detail
        .as_ref()
        .map_or("live", |d| d.native_kind.as_str());
    Ok(
        json!({"native_key":key,"native_kind":native_kind,"title":title,"manifest":manifest,"offers":offers,"source_duration_seconds":duration,
               "published_at":source.detail.as_ref().and_then(|d| d.published_at.clone()),
               "published_precision":source.detail.as_ref().map(|d| d.published_precision.clone()).unwrap_or_default()}),
    )
}
type Credential = (String, i64, String, String);
type ActiveRecording = (String, String, String, Option<String>, Option<i64>);
pub(crate) fn credential(
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
/// A credential made ready for a call: the identity, the cookie scope it is cached under and
/// when it was last proven to be signed in.
pub(crate) struct Opened {
    pub credential: Option<(String, i64)>,
    pub scope: Option<String>,
    pub validated: Option<String>,
}

/// Loads the credential into the host's cache; with `prove` the site is asked once whether the
/// identity is still signed in.
pub(crate) async fn open_credential(
    core: &Core,
    site: &str,
    credential_id: Option<&str>,
    prove: bool,
) -> Result<Opened, CoreError> {
    let (cid, p) = (credential_id.map(str::to_owned), site.to_owned());
    let identity = core
        .db
        .read(move |c| credential(c, cid.as_deref(), &p))
        .await?;
    let Some((id, generation, reference, _)) = identity else {
        return Ok(Opened {
            credential: None,
            scope: None,
            validated: None,
        });
    };
    let value = String::from_utf8(core.vault.load(&reference)?)
        .map_err(|_| CoreError::AuthRequired("credential encoding".into()))?;
    let scope = format!("{id}/g{generation}");
    core.cache_cookie(&format!("{site}/{scope}"), value);
    let mut validated = None;
    if prove {
        validated = core.auth_proofs.recent(site, &scope);
        if validated.is_none() {
            core.metadata_pacing(site).await;
            let proof = core
                .host
                .call_scoped(site, Some(&scope), "probe_auth", json!({}), Some(30000))
                .await?;
            if !matches!(proof["state"].as_str(), Some("valid" | "authenticated")) {
                return Err(CoreError::AuthRequired(
                    "source identity is not authenticated".into(),
                ));
            }
            let at = jobs::now();
            core.auth_proofs.note(site, &scope, &at);
            validated = Some(at);
        }
    }
    Ok(Opened {
        credential: Some((id, generation)),
        scope: Some(scope),
        validated,
    })
}

pub async fn preview(core: &Core, args: SourcePreviewArgs) -> Result<Value, CoreError> {
    let site = platform(&args.url)?.to_owned();
    let Opened {
        credential,
        scope,
        validated,
    } = open_credential(core, &site, args.credential_id.as_deref(), true).await?;
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
    let mut source: ResolvedSource = serde_json::from_value(value)?;
    if platform(&source.canonical_url)? != site {
        return Err(CoreError::InvalidInput("cross-site source identity".into()));
    }
    crate::media_tools::complete_source(core, &site, &mut source).await?;
    save_preview(core, &site, source, credential, validated).await
}
const INSERT_SOURCE_REQUESTS: &str = "INSERT INTO source_requests(request_id,platform,kind,source_url,native_key,account_key,account_name,credential_id,credential_generation,authentication,validated_at,snapshot,created_at,expires_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14)";

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
    core.db
        .write(move |tx| {
            let current = credential(tx, identity.as_ref().map(|v| v.0.as_str()), &platform)?
                .map(|(id, g, _, _)| (id, g));
            if current != identity {
                return Err(conflict());
            }
            tx.execute(
                INSERT_SOURCE_REQUESTS,
                params![
                    id,
                    platform,
                    source.kind,
                    source.canonical_url,
                    snapshot["native_key"].as_str(),
                    source.native_account_key,
                    source.display_name,
                    identity.as_ref().map(|v| v.0.as_str()),
                    identity.as_ref().map(|v| v.1),
                    auth,
                    validated,
                    snapshot.to_string(),
                    jobs::now(),
                    expiry
                ],
            )?;
            Ok(json!({
                "preview_id": id,
                "kind": source.kind,
                "platform": platform,
                "source_url": source.canonical_url,
                "native_key": snapshot["native_key"],
                "title": snapshot["title"],
                "account_name": source.display_name,
                "authentication": auth,
                "validated_at": validated,
                "expires_at": expiry,
                "published_at": snapshot["published_at"],
                "published_precision": snapshot["published_precision"],
                "offers": snapshot["offers"],
            }))
        })
        .await
}
fn selection(snapshot: &Value, ids: &[String]) -> Result<Vec<Value>, CoreError> {
    let offers = snapshot["offers"].as_array().ok_or_else(conflict)?;
    if ids.len() > 256 || (ids.is_empty() && !offers.is_empty()) {
        return Err(CoreError::InvalidInput("choose source resources".into()));
    }
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
const SELECT_SOURCE_REQUESTS: &str = "SELECT platform,kind,account_key,account_name,credential_id,credential_generation,snapshot,state,expires_at,entry_id,job_id,scope_id FROM source_requests WHERE request_id=?1";
const SELECT_SOURCE_REQUESTS_2: &str = "SELECT r.request_id,r.job_id,r.selected,r.credential_id,r.credential_generation FROM source_requests r JOIN jobs j ON j.job_id=r.job_id LEFT JOIN live_sessions l ON l.session_id=r.session_id WHERE r.platform=?1 AND r.native_key=?2 AND r.kind='live' AND r.state!='cancelled' AND (l.state IN ('waiting','recording','reconnecting','stopping') OR (r.session_id IS NULL AND j.state IN ('queued','leased','running','waiting_retry','waiting_storage','waiting_auth'))) ORDER BY r.created_at LIMIT 1";
const INSERT_SUBSCRIPTIONS: &str = "INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,quality_json,acquisition_kind,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6,'one_shot',?7,?7)";

const SELECT_LIVE_SESSIONS: &str = "SELECT EXISTS(SELECT 1 FROM live_sessions l JOIN entries e ON e.entry_id=l.entry_id WHERE e.namespace=?1 AND l.room_key=?2 AND l.state IN ('waiting','recording','reconnecting','stopping'))";
const INSERT_SOURCE_ACCOUNTS: &str = "INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?2,?3,?4,?5)";
const SELECT_SOURCE_ACCOUNTS: &str =
    "SELECT account_id FROM source_accounts WHERE namespace=?1 AND native_account_key=?2";
const INSERT_ENTRIES: &str = "INSERT OR IGNORE INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,?2,?6,?3,?4,?5)";
const SELECT_ENTRIES: &str = "SELECT entry_id,lifecycle='deleted' OR media_suppressed=1 FROM entries WHERE namespace=?1 AND native_kind=?3 AND native_entry_key=?2";
const INSERT_JOBS: &str = "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'source_acquire',?3,?4,'queued',1,?5,?5)";
const UPDATE_SOURCE_REQUESTS_2: &str = "UPDATE source_requests SET state='queued',scope_id=?2,entry_id=?3,job_id=?4,selected=?5 WHERE request_id=?1";

pub async fn confirm(core: &Core, args: SourceConfirmArgs) -> Result<Value, CoreError> {
    let target = match &args.destination {
        Some(target) => Some(crate::downloads::preflight(core, target).await?),
        None => None,
    };
    core.db.write(move|tx|{
        let row = tx.query_row(SELECT_SOURCE_REQUESTS, [&args.preview_id], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, String>(2)?,
                r.get::<_, Option<String>>(3)?,
                r.get::<_, Option<String>>(4)?,
                r.get::<_, Option<i64>>(5)?,
                r.get::<_, String>(6)?,
                r.get::<_, String>(7)?,
                r.get::<_, String>(8)?,
                r.get::<_, Option<String>>(9)?,
                r.get::<_, Option<String>>(10)?,
                r.get::<_, Option<String>>(11)?,
            ))
        })?;
        if row.7!="preview" {
            let selected: String = tx.query_row(
                "SELECT selected FROM source_requests WHERE request_id=?1",
                [&args.preview_id],
                |r| r.get(0),
            )?;
            let values: Vec<Value> = serde_json::from_str(&selected)?;
            let old: std::collections::BTreeSet<_> =
                values.iter().filter_map(|v| v["id"].as_str()).collect();
            let new: std::collections::BTreeSet<_> =
                args.selected.iter().map(String::as_str).collect();
            if old!=new||row.7=="cancelled" {return Err(conflict());}
            return Ok(json!({"request_id":args.preview_id,"entry_id":row.9,"job_id":row.10,"state":row.7}));
        }
        if row.8<jobs::now(){return Err(conflict());}
        let current=credential(tx,row.4.as_deref(),&row.0)?;
        if current.as_ref().map(|v|v.1)!=row.5 {return Err(conflict());}
        let snapshot:Value=serde_json::from_str(&row.6)?;let chosen=selection(&snapshot,&args.selected)?;
        if row.1=="live" {
            let active: Option<ActiveRecording> = tx
                .query_row(
                    SELECT_SOURCE_REQUESTS_2,
                    params![row.0, snapshot["native_key"].as_str()],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?)),
                )
                .optional()?;
            if let Some((id,job,saved,credential,generation))=active {
                if credential!=row.4||generation!=row.5||serde_json::from_str::<Vec<Value>>(&saved)?!=chosen{return Err(conflict());}
                return Ok(json!({"request_id":id,"job_id":job,"state":"already_recording"}));
            }
            let ordinary:bool=tx.query_row(SELECT_LIVE_SESSIONS,params![row.0,snapshot["native_key"].as_str()],|r|r.get(0))?;
            if ordinary {return Err(CoreError::VersionConflict("room already has an active recording".into()));}
        }
        let timestamp=jobs::now();let account=format!("acct-{}",uuid::Uuid::new_v4());
        tx.execute(INSERT_SOURCE_ACCOUNTS,params![account,row.0,row.2,row.3,timestamp])?;
        let account:String=tx.query_row(SELECT_SOURCE_ACCOUNTS,params![row.0,row.2],|r|r.get(0))?;
        let scope=format!("once-{}",args.preview_id);
        // Internal one-source control scope. The schema prohibits scan jobs;
        // ordinary subscription APIs and login completion exclude these rows.
        tx.execute(INSERT_SUBSCRIPTIONS,params![scope,account,format!("source:{}",args.preview_id),json!({
            "kind": "one_shot",
            "source_id": snapshot["native_key"],
        }).to_string(),row.4,if row.1=="live"{json!({"min_height":720,"strict":true}).to_string()}else{"{}".into()},timestamp])?;
        let kind=snapshot["native_kind"].as_str().unwrap_or("video");
        let entry=if row.1!="live"{
            let id=format!("ent-{}",uuid::Uuid::new_v4());
            tx.execute(INSERT_ENTRIES,params![id,row.0,snapshot["native_key"].as_str(),account,timestamp,kind])?;
            let (id,deleted):(String,bool)=tx.query_row(SELECT_ENTRIES,params![row.0,snapshot["native_key"].as_str(),kind],|r|Ok((r.get(0)?,r.get(1)?)))?;
            if deleted {return Err(conflict());}Some(id)
        }else{None};
        let delivery=match (&target,&entry) {
            (Some(target),Some(entry))=>Some(crate::store::deliveries::attach_link(tx,&scope,&args.preview_id,&snapshot,entry,target)?),
            (Some(_),None)=>return Err(CoreError::InvalidInput("直播录制不能下载到目录".into())),
            _=>None,
        };
        let job=format!("job-{}",uuid::Uuid::new_v4());let payload=json!({"source_request_id":args.preview_id,"source_request_generation":1,"subscription_id":scope,"entry_id":entry,"credential_id":row.4,"credential_generation":row.5,"delivery_id":delivery});
        tx.execute(INSERT_JOBS,params![job,format!("source:{}",args.preview_id),entry.as_deref().unwrap_or(&args.preview_id),payload.to_string(),timestamp])?;
        tx.execute(UPDATE_SOURCE_REQUESTS_2,params![args.preview_id,scope,entry,job,serde_json::to_string(&chosen)?])?;
        Ok(json!({"request_id":args.preview_id,"entry_id":entry,"job_id":job,"state":"queued","delivery_id":delivery}))
    }).await
}

const SELECT_SOURCE_REQUESTS_3: &str = "SELECT platform,source_url,snapshot,selected FROM source_requests WHERE request_id=?1 AND state!='cancelled' AND generation=?2";

async fn request(
    core: &Core,
    job: &ClaimedJob,
) -> Result<(String, String, Value, Vec<Value>), CoreError> {
    let guard = job.clone();
    core.db
        .read(move |c| {
            guard.check(c)?;
            let (platform, url, snapshot, selected): (String, String, String, String) = c
                .query_row(
                    SELECT_SOURCE_REQUESTS_3,
                    params![
                        guard.payload["source_request_id"].as_str(),
                        guard.payload["source_request_generation"].as_i64()
                    ],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
                )?;
            Ok((
                platform,
                url,
                serde_json::from_str(&snapshot)?,
                serde_json::from_str(&selected)?,
            ))
        })
        .await
}
const UPDATE_SOURCE_REQUESTS: &str = "UPDATE source_requests SET session_id=?2,entry_id=(SELECT entry_id FROM live_sessions WHERE session_id=?2),state='running' WHERE request_id=?1";
const UPDATE_JOBS: &str = "UPDATE jobs SET payload=json_set(payload,'$.source_request_id',?2,'$.source_request_generation',?3) WHERE job_key=?1";

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
    crate::media_tools::complete_source(core, &platform, &mut source).await?;
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
    if source.kind != "live" {
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
        crate::pipeline::observe::persist_detail(core, &guard, entry, &account, detail).await?;
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
        core.db
            .write(move |tx| {
                j.check(tx)?;
                tx.execute(
                    UPDATE_JOBS,
                    params![
                        format!("record:{session}"),
                        id,
                        j.payload["source_request_generation"].as_i64()
                    ],
                )?;
                tx.execute(UPDATE_SOURCE_REQUESTS, params![id, session])?;
                Ok(())
            })
            .await?;
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
const SELECT_SOURCE_REQUESTS_4: &str = "SELECT r.request_id,r.platform,r.kind,r.state,r.entry_id,r.job_id,r.session_id,r.snapshot,r.authentication,r.created_at,l.state,l.generation FROM source_requests r LEFT JOIN live_sessions l ON l.session_id=r.session_id ORDER BY r.created_at DESC,r.request_id LIMIT 50";
const SELECT_JOBS: &str = "SELECT job_id,job_type,state,last_error FROM jobs WHERE json_extract(payload,'$.source_request_id')=?1 ORDER BY created_at,job_id";

pub async fn list(core: &Core) -> Result<Value, CoreError> {
    core.db
        .read(|c| {
            let rows = c
                .prepare(SELECT_SOURCE_REQUESTS_4)?
                .query_map([], |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                        r.get::<_, String>(3)?,
                        r.get::<_, Option<String>>(4)?,
                        r.get::<_, Option<String>>(5)?,
                        r.get::<_, Option<String>>(6)?,
                        r.get::<_, String>(7)?,
                        r.get::<_, String>(8)?,
                        r.get::<_, String>(9)?,
                        r.get::<_, Option<String>>(10)?,
                        r.get::<_, Option<i64>>(11)?,
                    ))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            let mut items = Vec::new();
            for r in rows {
                let tasks = c
                    .prepare(SELECT_JOBS)?
                    .query_map([&r.0], |r| {
                        Ok(json!({
                            "job_id": r.get::<_, String>(0)?,
                            "kind": r.get::<_, String>(1)?,
                            "state": r.get::<_, String>(2)?,
                            "error": r.get::<_, Option<String>>(3)?,
                        }))
                    })?
                    .collect::<Result<Vec<_>, _>>()?;
                let snapshot: Value = serde_json::from_str(&r.7)?;
                let state = if r.3 == "cancelled" {
                    "cancelled"
                } else if tasks.is_empty() {
                    "preview"
                } else if tasks.iter().all(|t| t["state"] == "succeeded") {
                    "succeeded"
                } else if tasks.iter().any(|t| {
                    matches!(
                        t["state"].as_str(),
                        Some("failed" | "waiting_auth" | "cancelled")
                    )
                }) {
                    "needs_attention"
                } else {
                    "running"
                };
                items.push(json!({
                    "request_id": r.0,
                    "platform": r.1,
                    "kind": r.2,
                    "state": state,
                    "entry_id": r.4,
                    "job_id": r.5,
                    "session_id": r.6,
                    "title": snapshot["title"],
                    "authentication": r.8,
                    "created_at": r.9,
                    "session_state": r.10,
                    "session_generation": r.11,
                    "tasks": tasks,
                }));
            }
            Ok(json!({"items":items}))
        })
        .await
}
pub async fn cancel(core: &Core, id: String) -> Result<Value, CoreError> {
    core.db.write(move|tx|{
        tx.execute("UPDATE source_requests SET state='cancelled',generation=generation+1 WHERE request_id=?1 AND state!='cancelled'",[&id])?;
        tx.execute("UPDATE subscriptions SET state='paused',config_version=config_version+1 WHERE subscription_id=(SELECT scope_id FROM source_requests WHERE request_id=?1)",[&id])?;
        tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE json_extract(payload,'$.source_request_id')=?1 AND state!='succeeded'",[&id])?;
        tx.execute("UPDATE live_sessions SET state='stopping',manual_stopped=1,stop_reason='user_stopped' WHERE session_id=(SELECT session_id FROM source_requests WHERE request_id=?1) AND state!='sealed'",[&id])?;
        crate::store::deliveries::cancel_for_request(tx,&id)?;
        Ok(json!({"state":"cancelled"}))
    }).await
}

#[cfg(test)]
#[path = "source_tests.rs"]
mod tests;
