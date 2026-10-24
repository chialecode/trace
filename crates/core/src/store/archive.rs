//! Bounded archive reads. Large bodies are retrieved verbatim in Unicode chunks;
//! history and resource inventories use stable database keys, never truncation.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Row, params};
use serde::Deserialize;
use serde_json::{Value, json};

const PAGE_BYTES: usize = 128 * 1024;
/// Largest chunk of a body or evidence text one read returns, in Unicode scalar values.
const CHUNK_CHARS: i64 = 16384;

#[derive(Default, Deserialize)]
#[serde(default)]
pub struct PageArgs {
    pub entry_id: String,
    pub section: String,
    pub revision_id: Option<String>,
    pub cursor: Option<String>,
}

#[derive(Default, Deserialize)]
#[serde(default)]
pub struct ReadArgs {
    pub entry_id: String,
    pub revision_id: String,
    pub field: String,
    pub offset: u64,
}

#[derive(Default, Deserialize)]
#[serde(default)]
pub struct ResourceArgs {
    pub cursor: Option<String>,
    pub namespace: Option<String>,
    pub role: Option<String>,
    pub creator_id: Option<String>,
    pub available: Option<bool>,
}

const REVISIONS_SQL: &str = "SELECT rowid,revision_id,substr(title,1,512),completeness,observed_at FROM entry_revisions WHERE entry_id=?1 AND rowid<?2 ORDER BY rowid DESC LIMIT 101";
const OBSERVATIONS_SQL: &str = "SELECT observation_seq,access_state,observed_at,revision_id FROM source_observations WHERE entry_id=?1 AND observation_seq<?2 ORDER BY observation_seq DESC LIMIT 101";
const MEDIA_SQL: &str = "SELECT mi.rowid,COALESCE(mv.rowid,0),mi.media_id,mi.role,mi.position,substr(mi.title,1,512),mv.variant_id,mv.source_representation,b.blob_id,b.size,mv.claimed_spec,mv.measured_spec,b.mime,EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=b.blob_id AND rp.state='verified'),mi.source_attachment_id,EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=mv.variant_id),CASE WHEN mv.source_representation='playback-mux-v1' THEN json_array_length(mv.claimed_spec,'$.derived_from')=2 AND NOT EXISTS(SELECT 1 FROM json_each(mv.claimed_spec,'$.derived_from') pair WHERE NOT EXISTS(SELECT 1 FROM asset_selections sel JOIN media_variants source ON source.variant_id=sel.variant_id JOIN media_items track ON track.media_id=sel.media_id WHERE track.revision_id=mi.revision_id AND track.group_key=mi.group_key AND source.blob_id=pair.value AND track.role=CASE pair.key WHEN 0 THEN 'video' WHEN 1 THEN 'audio' END)) ELSE 0 END,mv.created_at,mi.fetch_state FROM media_items mi JOIN entry_revisions r ON r.revision_id=mi.revision_id LEFT JOIN media_variants mv ON mv.media_id=mi.media_id LEFT JOIN blobs b ON b.blob_id=mv.blob_id WHERE r.entry_id=?1 AND r.revision_id=COALESCE(?2,(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)) AND (mi.rowid,COALESCE(mv.rowid,0))>(?3,?4) ORDER BY mi.rowid,COALESCE(mv.rowid,0) LIMIT 101";
const RESOURCES_SQL: &str = "SELECT m.rowid,COALESCE(v.rowid,0),m.media_id,m.role,m.position,m.source_attachment_id,v.variant_id,v.blob_id,v.source_representation,v.claimed_spec,v.measured_spec,b.size,b.mime,v.created_at,e.entry_id,e.namespace,substr(r.title,1,512),e.account_id,substr(a.display_name,1,256),a.native_account_key,ab.creator_id,substr(cr.display_name,1,256),EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified'),(SELECT count(*) FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified'),EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=v.variant_id),r.revision_id,m.fetch_state FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id LEFT JOIN media_variants v ON v.media_id=m.media_id LEFT JOIN blobs b ON b.blob_id=v.blob_id LEFT JOIN source_accounts a ON a.account_id=e.account_id LEFT JOIN account_bindings ab ON ab.account_id=a.account_id AND ab.state='active' LEFT JOIN creators cr ON cr.creator_id=ab.creator_id WHERE e.lifecycle!='deleted' AND (m.rowid,COALESCE(v.rowid,0))>(?1,?2) AND (?3 IS NULL OR e.namespace=?3) AND (?4 IS NULL OR m.role=?4) AND (?5 IS NULL OR ab.creator_id=?5) AND (?6 IS NULL OR EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified')=?6) ORDER BY m.rowid,COALESCE(v.rowid,0) LIMIT 101";

/// Collects rows into one page: at most 100 items and `PAGE_BYTES`, with a cursor when more exist.
fn bounded_rows(
    mut rows: rusqlite::Rows<'_>,
    map: impl Fn(&Row<'_>) -> rusqlite::Result<(Value, String)>,
) -> Result<Value, CoreError> {
    let mut items = Vec::new();
    let mut bytes = 0;
    let mut last = None;
    let mut more = false;
    while let Some(row) = rows.next()? {
        let (item, cursor) = map(row)?;
        let size = serde_json::to_vec(&item)?.len();
        if size > PAGE_BYTES {
            return Err(CoreError::InvalidInput(
                "archive row exceeds page budget".into(),
            ));
        }
        if bytes + size > PAGE_BYTES || items.len() == 100 {
            more = true;
            break;
        }
        bytes += size;
        items.push(item);
        last = Some(cursor);
    }
    Ok(json!({"items":items,"next_cursor":if more {last} else {None}}))
}

/// Authentication label and approval state of a saved variant.
fn variant_labels(
    c: &Connection,
    variant: Option<&str>,
) -> rusqlite::Result<(Value, Option<String>)> {
    let authentication = crate::acquisitions::variant_summary(c, variant)?;
    let approval = c
        .query_row(
            "SELECT approval_state FROM media_variants WHERE variant_id=?1",
            [variant],
            |row| row.get::<_, String>(0),
        )
        .optional()?;
    Ok((authentication, approval))
}

fn revisions_page(c: &Connection, a: &PageArgs, key: (i64, i64)) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(REVISIONS_SQL)?;
    bounded_rows(stmt.query(params![a.entry_id, key.0])?, |r| {
        let rowid: i64 = r.get(0)?;
        Ok((
            json!({
                "revision_id": r.get::<_, String>(1)?,
                "title": r.get::<_, String>(2)?,
                "completeness": r.get::<_, String>(3)?,
                "observed_at": r.get::<_, String>(4)?,
                "content_inline": false,
            }),
            json!([rowid, 0]).to_string(),
        ))
    })
}

fn observations_page(c: &Connection, a: &PageArgs, key: (i64, i64)) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(OBSERVATIONS_SQL)?;
    bounded_rows(stmt.query(params![a.entry_id, key.0])?, |r| {
        let seq: i64 = r.get(0)?;
        Ok((
            json!({
                "seq": seq,
                "access_state": r.get::<_, String>(1)?,
                "observed_at": r.get::<_, String>(2)?,
                "revision_id": r.get::<_, Option<String>>(3)?,
            }),
            json!([seq, 0]).to_string(),
        ))
    })
}

fn media_item(c: &Connection, r: &Row<'_>) -> rusqlite::Result<Value> {
    let variant = r.get::<_, Option<String>>(6)?;
    let (authentication, approval_state) = variant_labels(c, variant.as_deref())?;
    Ok(json!({
        "authentication": authentication,
        "approval_state": approval_state,
        "media_id": r.get::<_, String>(2)?,
        "role": r.get::<_, String>(3)?,
        "position": r.get::<_, i64>(4)?,
        "title": r.get::<_, Option<String>>(5)?,
        "variant_id": variant,
        "representation": r.get::<_, Option<String>>(7)?,
        "blob_id": r.get::<_, Option<String>>(8)?,
        "size": r.get::<_, Option<i64>>(9)?,
        "claimed_spec": r.get::<_, Option<String>>(10)?,
        "measured_spec": r.get::<_, Option<String>>(11)?,
        "mime": r.get::<_, Option<String>>(12)?,
        "available": r.get::<_, bool>(13)?,
        "attachment_id": r.get::<_, String>(14)?,
        "selected": r.get::<_, bool>(15)?,
        "playback_current": r.get::<_, bool>(16)?,
        "saved_at": r.get::<_, Option<String>>(17)?,
        "fetch_state": r.get::<_, String>(18)?,
    }))
}

fn media_page(c: &Connection, a: &PageArgs, key: (i64, i64)) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(MEDIA_SQL)?;
    let rows = stmt.query(params![a.entry_id, a.revision_id, key.0, key.1])?;
    bounded_rows(rows, |r| {
        Ok((
            media_item(c, r)?,
            json!([r.get::<_, i64>(0)?, r.get::<_, i64>(1)?]).to_string(),
        ))
    })
}

/// One section (revisions, observations or media) of an entry's history.
pub fn page(c: &Connection, a: &PageArgs) -> Result<Value, CoreError> {
    let key: (i64, i64) = match &a.cursor {
        Some(s) => {
            serde_json::from_str(s).map_err(|_| CoreError::InvalidInput("archive cursor".into()))?
        }
        None if a.section == "media" => (0, 0),
        None => (i64::MAX, 0),
    };
    match a.section.as_str() {
        "revisions" => revisions_page(c, a, key),
        "observations" => observations_page(c, a, key),
        "media" => media_page(c, a, key),
        _ => Err(CoreError::InvalidInput("archive section".into())),
    }
}

fn resource_item(c: &Connection, r: &Row<'_>) -> rusqlite::Result<Value> {
    let variant = r.get::<_, Option<String>>(6)?;
    let (authentication, approval_state) = variant_labels(c, variant.as_deref())?;
    Ok(json!({
        "authentication": authentication,
        "approval_state": approval_state,
        "media_id": r.get::<_, String>(2)?,
        "role": r.get::<_, String>(3)?,
        "position": r.get::<_, i64>(4)?,
        "attachment_id": r.get::<_, String>(5)?,
        "variant_id": variant,
        "blob_id": r.get::<_, Option<String>>(7)?,
        "representation": r.get::<_, Option<String>>(8)?,
        "claimed_spec": r.get::<_, Option<String>>(9)?,
        "measured_spec": r.get::<_, Option<String>>(10)?,
        "size": r.get::<_, Option<i64>>(11)?,
        "mime": r.get::<_, Option<String>>(12)?,
        "saved_at": r.get::<_, Option<String>>(13)?,
        "entry_id": r.get::<_, String>(14)?,
        "namespace": r.get::<_, String>(15)?,
        "title": r.get::<_, String>(16)?,
        "account_id": r.get::<_, Option<String>>(17)?,
        "account_name": r.get::<_, Option<String>>(18)?,
        "account_key": r.get::<_, Option<String>>(19)?,
        "creator_id": r.get::<_, Option<String>>(20)?,
        "creator_name": r.get::<_, Option<String>>(21)?,
        "available": r.get::<_, bool>(22)?,
        "verified_copies": r.get::<_, i64>(23)?,
        "selected": r.get::<_, bool>(24)?,
        "revision_id": r.get::<_, String>(25)?,
        "fetch_state": r.get::<_, String>(26)?,
    }))
}

/// Independent resource inventory, including missing resources and every saved
/// variant. Stable row keys keep pagination independent of acquisition times.
pub fn resources(c: &Connection, a: ResourceArgs) -> Result<Value, CoreError> {
    let key: (i64, i64) = a
        .cursor
        .as_deref()
        .map(serde_json::from_str)
        .transpose()
        .map_err(|_| CoreError::InvalidInput("resource cursor".into()))?
        .unwrap_or((0, 0));
    let mut stmt = c.prepare(RESOURCES_SQL)?;
    let rows = stmt.query(params![
        key.0,
        key.1,
        a.namespace,
        a.role,
        a.creator_id,
        a.available
    ])?;
    bounded_rows(rows, |r| {
        Ok((
            resource_item(c, r)?,
            json!([r.get::<_, i64>(0)?, r.get::<_, i64>(1)?]).to_string(),
        ))
    })
}

/// A chunk of a text column starting at `offset`, with the offset to continue from.
fn text_chunk(chunk: String, total: i64, offset: i64, what: &str) -> Result<Value, CoreError> {
    if offset > total {
        return Err(CoreError::InvalidInput(what.into()));
    }
    let end = offset + chunk.chars().count() as i64;
    Ok(json!({
        "text": chunk,
        "next_offset": if end < total { Some(end) } else { None },
        "total_chars": total,
    }))
}

fn char_offset(offset: u64, what: &str) -> Result<i64, CoreError> {
    i64::try_from(offset)
        .ok()
        .filter(|n| *n < i64::MAX)
        .ok_or_else(|| CoreError::InvalidInput(what.into()))
}

pub fn read(c: &Connection, a: ReadArgs) -> Result<Value, CoreError> {
    let column = match a.field.as_str() {
        "body" => "normalized_body",
        "document" => "document_json",
        "metadata" => "metadata_json",
        _ => return Err(CoreError::InvalidInput("revision field".into())),
    };
    let offset = char_offset(a.offset, "revision offset")?;
    // SQLite substr counts Unicode scalar values, so chunks never split UTF-8.
    let (chunk, total): (String, i64) = c.query_row(
        &format!("SELECT substr({column},?3+1,{CHUNK_CHARS}),length({column}) FROM entry_revisions WHERE entry_id=?1 AND revision_id=?2"),
        params![a.entry_id, a.revision_id, offset],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    text_chunk(chunk, total, offset, "revision offset")
}

pub fn observation(c: &Connection, entry: &str, seq: i64, offset: u64) -> Result<Value, CoreError> {
    let offset = char_offset(offset, "observation offset")?;
    let (text, total): (String, i64) = c.query_row(
        &format!("SELECT substr(evidence,?3+1,{CHUNK_CHARS}),length(evidence) FROM source_observations WHERE entry_id=?1 AND observation_seq=?2"),
        params![entry, seq, offset],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    text_chunk(text, total, offset, "observation offset")
}

/// The subscription that re-observes an entry: namespace, kind, native key and subscription id.
pub fn recheck_target(
    c: &Connection,
    entry: &str,
) -> Result<(String, String, String, String), CoreError> {
    Ok(c.query_row(
        "SELECT e.namespace,e.native_kind,e.native_entry_key,s.subscription_id FROM entries e JOIN subscriptions s ON s.account_id=e.account_id AND s.state='enabled' AND s.content_type!='live' WHERE e.entry_id=?1 AND e.lifecycle!='deleted' AND e.media_suppressed=0 AND e.native_kind!='live' ORDER BY (s.content_type=e.native_kind) DESC,s.created_at,s.subscription_id LIMIT 1",
        [entry],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
    )?)
}

fn entry_header(c: &Connection, entry_id: &str) -> Result<Value, CoreError> {
    Ok(c.query_row(
        "SELECT entry_id,namespace,native_kind,native_entry_key,published_at,published_precision,first_seen_at FROM entries WHERE entry_id=?1 AND lifecycle!='deleted'",
        [entry_id],
        |r| {
            Ok(json!({
                "entry_id": r.get::<_, String>(0)?,
                "namespace": r.get::<_, String>(1)?,
                "kind": r.get::<_, String>(2)?,
                "native_key": r.get::<_, String>(3)?,
                "published_at": r.get::<_, Option<String>>(4)?,
                "published_precision": r.get::<_, Option<String>>(5)?,
                "discovered_at": r.get::<_, String>(6)?,
            }))
        },
    )?)
}

/// The current revision with its content inline, unless that would exceed the page budget.
fn current_revision(c: &Connection, id: &str) -> Result<Value, CoreError> {
    let (title, body, document, metadata, completeness, observed): (
        String,
        String,
        String,
        String,
        String,
        String,
    ) = c.query_row(
        "SELECT title,normalized_body,document_json,metadata_json,completeness,observed_at FROM entry_revisions WHERE revision_id=?1",
        [id],
        |r| {
            Ok((
                r.get(0)?,
                r.get(1)?,
                r.get(2)?,
                r.get(3)?,
                r.get(4)?,
                r.get(5)?,
            ))
        },
    )?;
    let mut revision = json!({
        "revision_id": id,
        "title": title.chars().take(512).collect::<String>(),
        "completeness": completeness,
        "observed_at": observed,
        "content_inline": true,
        "body": body,
        "document": serde_json::from_str::<Value>(&document)?,
        "metadata": serde_json::from_str::<Value>(&metadata)?,
    });
    if serde_json::to_vec(&revision)?.len() > PAGE_BYTES {
        revision
            .as_object_mut()
            .unwrap()
            .retain(|k, _| !["body", "document", "metadata"].contains(&k.as_str()));
        revision["content_inline"] = json!(false);
    }
    Ok(revision)
}

fn live_session(c: &Connection, entry_id: &str) -> Result<Option<Value>, CoreError> {
    Ok(c.query_row(
        "SELECT session_id,state,generation,manual_stopped,started_at,ended_at,stop_reason FROM live_sessions WHERE entry_id=?1",
        [entry_id],
        |r| {
            Ok(json!({
                "session_id": r.get::<_, String>(0)?,
                "state": r.get::<_, String>(1)?,
                "generation": r.get::<_, i64>(2)?,
                "manual_stopped": r.get::<_, bool>(3)?,
                "started_at": r.get::<_, String>(4)?,
                "ended_at": r.get::<_, Option<String>>(5)?,
                "stop_reason": r.get::<_, Option<String>>(6)?,
            }))
        },
    )
    .optional()?)
}

/// An entry with its newest history, observations, media and live session.
pub fn entry(c: &Connection, entry_id: &str) -> Result<Value, CoreError> {
    let mut entry = entry_header(c, entry_id)?;
    let current: Option<String> = c
        .query_row(
            "SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1",
            [entry_id],
            |r| r.get(0),
        )
        .optional()?;
    entry["current_revision_id"] = json!(current);
    let section = |name: &str| PageArgs {
        entry_id: entry_id.to_owned(),
        section: name.into(),
        revision_id: current.clone(),
        ..Default::default()
    };
    let mut revisions = page(c, &section("revisions"))?;
    if let Some(id) = &current {
        let revision = current_revision(c, id)?;
        entry["body"] = revision.get("body").cloned().unwrap_or(json!(""));
        let items = revisions["items"].as_array_mut().unwrap();
        match items.iter_mut().find(|r| r["revision_id"] == *id) {
            Some(old) => *old = revision,
            None => items.push(revision),
        }
    }
    let observations = page(c, &section("observations"))?;
    let media = page(c, &section("media"))?;
    let discovered_by = {
        let mut stmt = c.prepare(
            "SELECT subscription_id,first_discovered_at,last_discovered_at
             FROM entry_subscriptions WHERE entry_id=?1
             ORDER BY first_discovered_at,subscription_id",
        )?;
        stmt.query_map([entry_id], |r| {
            Ok(json!({
                "subscription_id": r.get::<_, String>(0)?,
                "first_discovered_at": r.get::<_, String>(1)?,
                "last_discovered_at": r.get::<_, String>(2)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?
    };
    let (observation_count, latest_observation_seq): (i64, i64) = c.query_row(
        "SELECT count(*),COALESCE(MAX(observation_seq),0)
         FROM source_observations WHERE entry_id=?1",
        [entry_id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    Ok(json!({
        "entry": entry,
        "revisions": revisions["items"],
        "revisions_cursor": revisions["next_cursor"],
        "observations": observations["items"],
        "observations_cursor": observations["next_cursor"],
        "observation_count": observation_count,
        "latest_observation_seq": latest_observation_seq,
        "discovered_by_subscriptions": discovered_by,
        "media": media["items"],
        "media_cursor": media["next_cursor"],
        "live": live_session(c, entry_id)?,
        "fragments": [],
    }))
}

/// Facts for one incremental subscription check. Every field is either an opaque local identity,
/// public archive identity, a timestamp or an outcome; credentials, query strings and source URLs
/// never enter this view.
pub fn subscription_evidence(
    c: &Connection,
    subscription_id: &str,
    limit: i64,
) -> Result<Value, CoreError> {
    let exists = c.query_row(
        "SELECT EXISTS(SELECT 1 FROM subscriptions WHERE subscription_id=?1)",
        [subscription_id],
        |r| r.get::<_, bool>(0),
    )?;
    if !exists {
        return Err(CoreError::NotFound("subscription".into()));
    }
    let cap = limit.clamp(1, 500);
    let relations = subscription_evidence_relations(c, subscription_id, cap + 1)?;
    let truncated = relations.len() > cap as usize;
    let mut entries = relations
        .into_iter()
        .take(cap as usize)
        .map(|relation| subscription_entry_evidence(c, subscription_id, relation))
        .collect::<Result<Vec<_>, _>>()?;
    let nested_truncated = entries.iter().any(|entry| entry["truncated"] == true);
    for entry in &mut entries {
        if let Some(object) = entry.as_object_mut() {
            object.remove("truncated");
        }
    }
    Ok(json!({
        "subscription_id": subscription_id,
        "entries": entries,
        "truncated": truncated || nested_truncated,
    }))
}

type EvidenceRelation = (String, String, Option<String>, String, String);

fn subscription_evidence_relations(
    c: &Connection,
    subscription_id: &str,
    limit: i64,
) -> Result<Vec<EvidenceRelation>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT d.entry_id,e.native_entry_key,e.published_at,d.first_discovered_at,d.last_discovered_at
         FROM entry_subscriptions d JOIN entries e ON e.entry_id=d.entry_id
         WHERE d.subscription_id=?1
         ORDER BY d.last_discovered_at DESC,d.entry_id LIMIT ?2",
    )?;
    Ok(stmt
        .query_map(params![subscription_id, limit], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, Option<String>>(2)?,
                r.get::<_, String>(3)?,
                r.get::<_, String>(4)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?)
}

fn subscription_entry_evidence(
    c: &Connection,
    subscription_id: &str,
    relation: EvidenceRelation,
) -> Result<Value, CoreError> {
    let (entry_id, native_key, published_at, first_discovered_at, last_discovered_at) = relation;
    let (observation_count, latest_observation_seq) = c.query_row(
        "SELECT count(*),COALESCE(MAX(o.observation_seq),0)
         FROM source_observations o JOIN acquisition_contexts x ON x.context_id=o.context_id
         WHERE o.entry_id=?1 AND x.subscription_id=?2",
        params![&entry_id, subscription_id],
        |r| Ok((r.get::<_, i64>(0)?, r.get::<_, i64>(1)?)),
    )?;
    let (observations, mut truncated) = evidence_observations(c, &entry_id, subscription_id)?;
    let discovered_by = evidence_discoveries(c, &entry_id)?;
    let (jobs, jobs_truncated) = evidence_jobs(c, subscription_id, &entry_id)?;
    let (contexts, contexts_truncated) = evidence_contexts(c, subscription_id, &entry_id)?;
    let (acquisitions, acquisitions_truncated) =
        evidence_acquisitions(c, subscription_id, &entry_id)?;
    truncated |= jobs_truncated || contexts_truncated || acquisitions_truncated;
    Ok(json!({
        "entry_id": entry_id,
        "native_key": native_key,
        "published_at": published_at,
        "relation": {
            "first_discovered_at": first_discovered_at,
            "last_discovered_at": last_discovered_at,
        },
        "discovered_by_subscriptions": discovered_by,
        "observation_count": observation_count,
        "latest_observation_seq": latest_observation_seq,
        "observations": observations,
        "jobs": jobs,
        "contexts": contexts,
        "acquisitions": acquisitions,
        "truncated": truncated,
    }))
}

fn evidence_observations(
    c: &Connection,
    entry_id: &str,
    subscription_id: &str,
) -> Result<(Vec<Value>, bool), CoreError> {
    let rows = c
        .prepare(
            "SELECT o.observation_seq,o.access_state,o.observed_at,o.context_id
             FROM source_observations o JOIN acquisition_contexts x ON x.context_id=o.context_id
             WHERE o.entry_id=?1 AND x.subscription_id=?2
             ORDER BY o.observation_seq LIMIT 257",
        )?
        .query_map(params![entry_id, subscription_id], |r| {
            Ok(json!({
                "seq": r.get::<_, i64>(0)?,
                "access_state": r.get::<_, String>(1)?,
                "observed_at": r.get::<_, String>(2)?,
                "context_id": r.get::<_, Option<String>>(3)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let truncated = rows_are_truncated(&rows);
    Ok((rows.into_iter().take(256).collect(), truncated))
}

fn evidence_discoveries(c: &Connection, entry_id: &str) -> Result<Vec<Value>, CoreError> {
    Ok(c.prepare(
        "SELECT subscription_id,first_discovered_at,last_discovered_at
         FROM entry_subscriptions WHERE entry_id=?1 ORDER BY subscription_id LIMIT 256",
    )?
    .query_map([entry_id], |r| {
        Ok(json!({
            "subscription_id": r.get::<_, String>(0)?,
            "first_discovered_at": r.get::<_, String>(1)?,
            "last_discovered_at": r.get::<_, String>(2)?,
        }))
    })?
    .collect::<Result<Vec<_>, _>>()?)
}

fn evidence_jobs(
    c: &Connection,
    subscription_id: &str,
    entry_id: &str,
) -> Result<(Vec<Value>, bool), CoreError> {
    let rows = c
        .prepare(
            "SELECT j.job_id,j.job_key,j.job_type,j.state,j.target_generation,
                    w.requested,w.completed,w.state,j.updated_at
             FROM job_consumers w JOIN jobs j ON j.job_id=w.job_id
             WHERE w.subscription_id=?1 AND j.subject_id=?2
             ORDER BY j.updated_at DESC,j.job_id LIMIT 257",
        )?
        .query_map(params![subscription_id, entry_id], |r| {
            Ok(json!({
                "job_id": r.get::<_, String>(0)?,
                "job_key": r.get::<_, String>(1)?,
                "job_type": r.get::<_, String>(2)?,
                "state": r.get::<_, String>(3)?,
                "job_generation": r.get::<_, i64>(4)?,
                "consumer_requested": r.get::<_, i64>(5)?,
                "consumer_completed": r.get::<_, i64>(6)?,
                "consumer_state": r.get::<_, String>(7)?,
                "updated_at": r.get::<_, String>(8)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let truncated = rows_are_truncated(&rows);
    Ok((rows.into_iter().take(256).collect(), truncated))
}

fn evidence_contexts(
    c: &Connection,
    subscription_id: &str,
    entry_id: &str,
) -> Result<(Vec<Value>, bool), CoreError> {
    let rows = c
        .prepare(
            "SELECT context_id,job_id,job_generation,started_at,auth_state,basis
             FROM acquisition_contexts
             WHERE subscription_id=?1 AND entry_id=?2
             ORDER BY started_at,context_id LIMIT 257",
        )?
        .query_map(params![subscription_id, entry_id], |r| {
            Ok(json!({
                "context_id": r.get::<_, String>(0)?,
                "job_id": r.get::<_, Option<String>>(1)?,
                "job_generation": r.get::<_, Option<i64>>(2)?,
                "started_at": r.get::<_, String>(3)?,
                "auth_state": r.get::<_, String>(4)?,
                "basis": r.get::<_, String>(5)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let truncated = rows_are_truncated(&rows);
    Ok((rows.into_iter().take(256).collect(), truncated))
}

fn evidence_acquisitions(
    c: &Connection,
    subscription_id: &str,
    entry_id: &str,
) -> Result<(Vec<Value>, bool), CoreError> {
    let rows = c
        .prepare(
            "SELECT a.acquisition_id,a.context_id,a.media_id,a.variant_id,
                    a.outcome,a.finished_at,a.measured_spec
             FROM resource_acquisitions a
             JOIN acquisition_contexts x ON x.context_id=a.context_id
             WHERE x.subscription_id=?1 AND x.entry_id=?2
             ORDER BY a.sequence LIMIT 257",
        )?
        .query_map(params![subscription_id, entry_id], |r| {
            Ok(json!({
                "acquisition_id": r.get::<_, String>(0)?,
                "context_id": r.get::<_, String>(1)?,
                "media_id": r.get::<_, Option<String>>(2)?,
                "variant_id": r.get::<_, Option<String>>(3)?,
                "outcome": r.get::<_, String>(4)?,
                "finished_at": r.get::<_, String>(5)?,
                "measured_spec": r.get::<_, String>(6)
                    .ok()
                    .and_then(|value| serde_json::from_str::<Value>(&value).ok())
                    .unwrap_or(Value::Null),
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let truncated = rows_are_truncated(&rows);
    Ok((rows.into_iter().take(256).collect(), truncated))
}

fn rows_are_truncated(rows: &[Value]) -> bool {
    rows.len() > 256
}
