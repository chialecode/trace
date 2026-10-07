//! Bounded archive reads. Large bodies are retrieved verbatim in Unicode chunks;
//! history and resource inventories use stable database keys, never truncation.
use crate::{db::DbHandle, error::CoreError};
use rusqlite::{Connection, OptionalExtension, params};
use serde::Deserialize;
use serde_json::{Value, json};

const PAGE_BYTES: usize = 128 * 1024;

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

fn bounded_rows(
    mut rows: rusqlite::Rows<'_>,
    map: impl Fn(&rusqlite::Row<'_>) -> rusqlite::Result<(Value, String)>,
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

fn page_on(c: &Connection, a: &PageArgs) -> Result<Value, CoreError> {
    let key: (i64, i64) = match &a.cursor {
        Some(s) => {
            serde_json::from_str(s).map_err(|_| CoreError::InvalidInput("archive cursor".into()))?
        }
        None => {
            if a.section == "media" {
                (0, 0)
            } else {
                (i64::MAX, 0)
            }
        }
    };
    match a.section.as_str() {
        "revisions" => {
            let mut stmt = c.prepare("SELECT rowid,revision_id,substr(title,1,512),completeness,observed_at FROM entry_revisions WHERE entry_id=?1 AND rowid<?2 ORDER BY rowid DESC LIMIT 101")?;
            bounded_rows(stmt.query(params![a.entry_id, key.0])?, |r| {
                let rowid: i64 = r.get(0)?;
                Ok((
                    json!({"revision_id":r.get::<_,String>(1)?,"title":r.get::<_,String>(2)?,"completeness":r.get::<_,String>(3)?,"observed_at":r.get::<_,String>(4)?,"content_inline":false}),
                    json!([rowid, 0]).to_string(),
                ))
            })
        }
        "observations" => {
            let mut stmt = c.prepare("SELECT observation_seq,access_state,observed_at,revision_id FROM source_observations WHERE entry_id=?1 AND observation_seq<?2 ORDER BY observation_seq DESC LIMIT 101")?;
            bounded_rows(stmt.query(params![a.entry_id, key.0])?, |r| {
                let seq: i64 = r.get(0)?;
                Ok((
                    json!({"seq":seq,"access_state":r.get::<_,String>(1)?,"observed_at":r.get::<_,String>(2)?,"revision_id":r.get::<_,Option<String>>(3)?}),
                    json!([seq, 0]).to_string(),
                ))
            })
        }
        "media" => {
            let mut stmt = c.prepare("SELECT mi.rowid,COALESCE(mv.rowid,0),mi.media_id,mi.role,mi.position,substr(mi.title,1,512),mv.variant_id,mv.source_representation,b.blob_id,b.size,mv.claimed_spec,mv.measured_spec,b.mime,EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=b.blob_id AND rp.state='verified'),mi.source_attachment_id,EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=mv.variant_id),CASE WHEN mv.source_representation='playback-mux-v1' THEN json_array_length(mv.claimed_spec,'$.derived_from')=2 AND NOT EXISTS(SELECT 1 FROM json_each(mv.claimed_spec,'$.derived_from') pair WHERE NOT EXISTS(SELECT 1 FROM asset_selections sel JOIN media_variants source ON source.variant_id=sel.variant_id JOIN media_items track ON track.media_id=sel.media_id WHERE track.revision_id=mi.revision_id AND track.group_key=mi.group_key AND source.blob_id=pair.value AND track.role=CASE pair.key WHEN 0 THEN 'video' WHEN 1 THEN 'audio' END)) ELSE 0 END,mv.created_at FROM media_items mi JOIN entry_revisions r ON r.revision_id=mi.revision_id LEFT JOIN media_variants mv ON mv.media_id=mi.media_id LEFT JOIN blobs b ON b.blob_id=mv.blob_id WHERE r.entry_id=?1 AND r.revision_id=COALESCE(?2,(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)) AND (mi.rowid,COALESCE(mv.rowid,0))>(?3,?4) ORDER BY mi.rowid,COALESCE(mv.rowid,0) LIMIT 101")?;
            bounded_rows(
                stmt.query(params![a.entry_id, a.revision_id, key.0, key.1])?,
                |r| {
                    Ok((
                        json!({"authentication":crate::acquisitions::variant_summary(c,r.get::<_,Option<String>>(6)?.as_deref())?,"approval_state":c.query_row("SELECT approval_state FROM media_variants WHERE variant_id=?1",[r.get::<_,Option<String>>(6)?],|row|row.get::<_,String>(0)).optional()?,"media_id":r.get::<_,String>(2)?,"role":r.get::<_,String>(3)?,"position":r.get::<_,i64>(4)?,"title":r.get::<_,Option<String>>(5)?,"variant_id":r.get::<_,Option<String>>(6)?,"representation":r.get::<_,Option<String>>(7)?,"blob_id":r.get::<_,Option<String>>(8)?,"size":r.get::<_,Option<i64>>(9)?,"claimed_spec":r.get::<_,Option<String>>(10)?,"measured_spec":r.get::<_,Option<String>>(11)?,"mime":r.get::<_,Option<String>>(12)?,"available":r.get::<_,bool>(13)?,"attachment_id":r.get::<_,String>(14)?,"selected":r.get::<_,bool>(15)?,"playback_current":r.get::<_,bool>(16)?,"saved_at":r.get::<_,Option<String>>(17)?}),
                        json!([r.get::<_, i64>(0)?, r.get::<_, i64>(1)?]).to_string(),
                    ))
                },
            )
        }
        _ => Err(CoreError::InvalidInput("archive section".into())),
    }
}

pub async fn page(db: &DbHandle, a: PageArgs) -> Result<Value, CoreError> {
    db.read(move |c| page_on(c, &a)).await
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

/// Independent resource inventory, including missing resources and every saved
/// variant. Stable row keys keep pagination independent of acquisition times.
pub async fn resources(db: &DbHandle, a: ResourceArgs) -> Result<Value, CoreError> {
    let key: (i64, i64) = a
        .cursor
        .as_deref()
        .map(serde_json::from_str)
        .transpose()
        .map_err(|_| CoreError::InvalidInput("resource cursor".into()))?
        .unwrap_or((0, 0));
    db.read(move |c| {
        let mut stmt=c.prepare("SELECT m.rowid,COALESCE(v.rowid,0),m.media_id,m.role,m.position,m.source_attachment_id,v.variant_id,v.blob_id,v.source_representation,v.claimed_spec,v.measured_spec,b.size,b.mime,v.created_at,e.entry_id,e.namespace,substr(r.title,1,512),e.account_id,substr(a.display_name,1,256),a.native_account_key,ab.creator_id,substr(cr.display_name,1,256),EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified'),(SELECT count(*) FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified'),EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=v.variant_id),r.revision_id FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id LEFT JOIN media_variants v ON v.media_id=m.media_id LEFT JOIN blobs b ON b.blob_id=v.blob_id LEFT JOIN source_accounts a ON a.account_id=e.account_id LEFT JOIN account_bindings ab ON ab.account_id=a.account_id AND ab.state='active' LEFT JOIN creators cr ON cr.creator_id=ab.creator_id WHERE e.lifecycle!='deleted' AND (m.rowid,COALESCE(v.rowid,0))>(?1,?2) AND (?3 IS NULL OR e.namespace=?3) AND (?4 IS NULL OR m.role=?4) AND (?5 IS NULL OR ab.creator_id=?5) AND (?6 IS NULL OR EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified')=?6) ORDER BY m.rowid,COALESCE(v.rowid,0) LIMIT 101")?;
        bounded_rows(stmt.query(params![key.0,key.1,a.namespace,a.role,a.creator_id,a.available])?, |r| Ok((json!({
            "authentication":crate::acquisitions::variant_summary(c,r.get::<_,Option<String>>(6)?.as_deref())?,"approval_state":c.query_row("SELECT approval_state FROM media_variants WHERE variant_id=?1",[r.get::<_,Option<String>>(6)?],|row|row.get::<_,String>(0)).optional()?,"media_id":r.get::<_,String>(2)?,"role":r.get::<_,String>(3)?,"position":r.get::<_,i64>(4)?,"attachment_id":r.get::<_,String>(5)?,
            "variant_id":r.get::<_,Option<String>>(6)?,"blob_id":r.get::<_,Option<String>>(7)?,"representation":r.get::<_,Option<String>>(8)?,"claimed_spec":r.get::<_,Option<String>>(9)?,"measured_spec":r.get::<_,Option<String>>(10)?,"size":r.get::<_,Option<i64>>(11)?,"mime":r.get::<_,Option<String>>(12)?,"saved_at":r.get::<_,Option<String>>(13)?,
            "entry_id":r.get::<_,String>(14)?,"namespace":r.get::<_,String>(15)?,"title":r.get::<_,String>(16)?,"account_id":r.get::<_,Option<String>>(17)?,"account_name":r.get::<_,Option<String>>(18)?,"account_key":r.get::<_,Option<String>>(19)?,"creator_id":r.get::<_,Option<String>>(20)?,"creator_name":r.get::<_,Option<String>>(21)?,"available":r.get::<_,bool>(22)?,"verified_copies":r.get::<_,i64>(23)?,"selected":r.get::<_,bool>(24)?,"revision_id":r.get::<_,String>(25)?
        }),json!([r.get::<_,i64>(0)?,r.get::<_,i64>(1)?]).to_string())))
    }).await
}

pub async fn read(db: &DbHandle, a: ReadArgs) -> Result<Value, CoreError> {
    let column = match a.field.as_str() {
        "body" => "normalized_body",
        "document" => "document_json",
        "metadata" => "metadata_json",
        _ => return Err(CoreError::InvalidInput("revision field".into())),
    };
    let offset = i64::try_from(a.offset)
        .ok()
        .filter(|n| *n < i64::MAX)
        .ok_or_else(|| CoreError::InvalidInput("revision offset".into()))?;
    db.read(move |c| {
        // SQLite substr counts Unicode scalar values, so chunks never split UTF-8.
        let (chunk,total):(String,i64)=c.query_row(&format!("SELECT substr({column},?3+1,16384),length({column}) FROM entry_revisions WHERE entry_id=?1 AND revision_id=?2"),params![a.entry_id,a.revision_id,offset],|r|Ok((r.get(0)?,r.get(1)?)))?;
        if offset > total { return Err(CoreError::InvalidInput("revision offset".into())); }
        let end=offset+chunk.chars().count() as i64;
        Ok(json!({"text":chunk,"next_offset":if end<total {Some(end)} else {None},"total_chars":total}))
    }).await
}

pub async fn observation(
    db: &DbHandle,
    entry: String,
    seq: i64,
    offset: u64,
) -> Result<Value, CoreError> {
    let offset = i64::try_from(offset)
        .ok()
        .filter(|n| *n < i64::MAX)
        .ok_or_else(|| CoreError::InvalidInput("observation offset".into()))?;
    db.read(move|c|{
        let (text,total):(String,i64)=c.query_row("SELECT substr(evidence,?3+1,16384),length(evidence) FROM source_observations WHERE entry_id=?1 AND observation_seq=?2",params![entry,seq,offset],|r|Ok((r.get(0)?,r.get(1)?)))?;
        if offset>total{return Err(CoreError::InvalidInput("observation offset".into()));}
        let end=offset+text.chars().count()as i64;
        Ok(json!({"text":text,"next_offset":if end<total{Some(end)}else{None},"total_chars":total}))
    }).await
}

pub async fn recheck(db: &DbHandle, entry: String) -> Result<Value, CoreError> {
    db.write(move|tx|{
        let (namespace,kind,key,sub):(String,String,String,String)=tx.query_row("SELECT e.namespace,e.native_kind,e.native_entry_key,s.subscription_id FROM entries e JOIN subscriptions s ON s.account_id=e.account_id AND s.state='enabled' AND s.content_type!='live' WHERE e.entry_id=?1 AND e.lifecycle!='deleted' AND e.media_suppressed=0 AND e.native_kind!='live' ORDER BY (s.content_type=e.native_kind) DESC,s.created_at,s.subscription_id LIMIT 1",[&entry],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?)))?;
        let key=format!("observe:{namespace}:{kind}:{key}");
        crate::jobs::consumers::discover(tx,&entry,&sub)?;
        let id=crate::jobs::consumers::request(tx,&key,"observe",&entry,json!({"entry_id":entry,"subscription_id":sub}),&sub)?;
        let state:String=tx.query_row("SELECT state FROM jobs WHERE job_id=?1",[&id],|r|r.get(0))?;
        if state=="cancelled" {return Err(CoreError::VersionConflict("explicitly cancelled work needs an explicit job retry".into()))}
        if !matches!(state.as_str(),"running"|"leased") {crate::jobs::consumers::retry(tx,&id)?;}
        Ok(json!({"job_id":id}))
    }).await
}

pub async fn entry(db: &DbHandle, entry_id: String) -> Result<Value, CoreError> {
    db.read(move |c| {
        let mut entry:Value=c.query_row("SELECT entry_id,namespace,native_kind,native_entry_key,published_at,published_precision,first_seen_at FROM entries WHERE entry_id=?1 AND lifecycle!='deleted'",[&entry_id],|r|Ok(json!({"entry_id":r.get::<_,String>(0)?,"namespace":r.get::<_,String>(1)?,"kind":r.get::<_,String>(2)?,"native_key":r.get::<_,String>(3)?,"published_at":r.get::<_,Option<String>>(4)?,"published_precision":r.get::<_,Option<String>>(5)?,"discovered_at":r.get::<_,String>(6)?})))?;
        let current:Option<String>=c.query_row("SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1",[&entry_id],|r|r.get(0)).optional()?;
        entry["current_revision_id"]=json!(current);
        let args=|section:&str|PageArgs{entry_id:entry_id.clone(),section:section.into(),revision_id:current.clone(),..Default::default()};
        let mut revisions=page_on(c,&args("revisions"))?;
        if let Some(id)=&current {
            let (title,body,document,metadata,completeness,observed):(String,String,String,String,String,String)=c.query_row("SELECT title,normalized_body,document_json,metadata_json,completeness,observed_at FROM entry_revisions WHERE revision_id=?1",[id],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?,r.get(5)?)))?;
            let mut revision=json!({"revision_id":id,"title":title.chars().take(512).collect::<String>(),"completeness":completeness,"observed_at":observed,"content_inline":true,"body":body,"document":serde_json::from_str::<Value>(&document)?,"metadata":serde_json::from_str::<Value>(&metadata)?});
            if serde_json::to_vec(&revision)?.len()>PAGE_BYTES {revision.as_object_mut().unwrap().retain(|k,_|!["body","document","metadata"].contains(&k.as_str()));revision["content_inline"]=json!(false);}
            entry["body"]=revision.get("body").cloned().unwrap_or(json!(""));
            let items=revisions["items"].as_array_mut().unwrap();
            if let Some(old)=items.iter_mut().find(|r|r["revision_id"]==*id) {*old=revision;} else {items.push(revision);}
        }
        let observations=page_on(c,&args("observations"))?;
        let media=page_on(c,&args("media"))?;
        let live=c.query_row("SELECT session_id,state,generation,manual_stopped,started_at,ended_at,stop_reason FROM live_sessions WHERE entry_id=?1",[&entry_id],|r|Ok(json!({"session_id":r.get::<_,String>(0)?,"state":r.get::<_,String>(1)?,"generation":r.get::<_,i64>(2)?,"manual_stopped":r.get::<_,bool>(3)?,"started_at":r.get::<_,String>(4)?,"ended_at":r.get::<_,Option<String>>(5)?,"stop_reason":r.get::<_,Option<String>>(6)?}))).optional()?;
        Ok(json!({"entry":entry,"revisions":revisions["items"],"revisions_cursor":revisions["next_cursor"],"observations":observations["items"],"observations_cursor":observations["next_cursor"],"media":media["items"],"media_cursor":media["next_cursor"],"live":live,"fragments":[]}))
    }).await
}
