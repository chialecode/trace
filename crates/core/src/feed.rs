//! Timeline query contract: filters, stable cursor and counts share a database snapshot.
use crate::{db::DbHandle, error::CoreError};
use serde_json::{Value, json};
use trace_contracts::control::ArchiveTimelineArgs;

pub async fn query(db: &DbHandle, args: ArchiveTimelineArgs) -> Result<Value, CoreError> {
    db.read(move |c| {
        let query=trace_domain::search::normalize_v1(args.query.as_deref().unwrap_or_default());
        let mut scope=json!([args.creator_id,args.account_id,args.namespace,args.kind,args.save_state,query,args.oldest]);
        if let Some(id)=&args.entry_id {scope.as_array_mut().unwrap().push(json!(id));}
        let cursor:Value=match args.cursor.as_deref(){Some(s)=>serde_json::from_str(s)?,None=>Value::Null};
        if !cursor.is_null() && cursor["scope"]!=scope{return Err(CoreError::InvalidInput("cursor does not match query".into()));}
        let limit=args.limit.clamp(1,200);
        let sql=r#"WITH base AS (
          SELECT e.*,r.revision_id,r.title,r.normalized_body,a.display_name AS account_name,
            a.native_account_key,a.profile_url,b.creator_id,cr.display_name AS creator_name,
            CASE WHEN NOT EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id AND m.required=1) THEN 'unknown'
              WHEN NOT EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id AND m.required=1 AND NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE v.media_id=m.media_id)) THEN 'complete'
              WHEN EXISTS(SELECT 1 FROM media_items m JOIN media_variants v ON v.media_id=m.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE m.revision_id=r.revision_id AND m.required=1) THEN 'partial' ELSE 'pending' END AS save_progress
          FROM entries e JOIN source_accounts a ON a.account_id=e.account_id
          LEFT JOIN account_bindings b ON b.account_id=e.account_id AND b.state='active'
          LEFT JOIN creators cr ON cr.creator_id=b.creator_id
          JOIN entry_revisions r ON r.revision_id=(SELECT revision_id FROM entry_revisions WHERE entry_id=e.entry_id ORDER BY (completeness='full') DESC,observed_at DESC LIMIT 1)
        ), filtered AS (SELECT * FROM base WHERE (?1 IS NULL OR creator_id=?1) AND (?2 IS NULL OR account_id=?2)
          AND (?3 IS NULL OR namespace=?3) AND (?4 IS NULL OR native_kind=?4) AND (?5 IS NULL OR save_progress=?5)
          AND (?6='' OR EXISTS(SELECT 1 FROM search_documents d WHERE d.entry_id=base.entry_id AND instr(d.normalized_text,?6)>0) OR instr(lower(COALESCE(creator_name,'')),?6)>0 OR instr(lower(COALESCE(account_name,'')),?6)>0 OR instr(lower(native_account_key),?6)>0) AND (?7 IS NULL OR entry_id=?7))
        "#;
        let params=rusqlite::params![args.creator_id,args.account_id,args.namespace,args.kind,args.save_state,query,args.entry_id];
        let filtered:i64=c.query_row(&format!("{sql} SELECT count(*) FROM filtered"),params,|r|r.get(0))?;
        let global:i64=c.query_row("SELECT count(*) FROM entries",[],|r|r.get(0))?;
        let direction=if args.oldest{"ASC"}else{"DESC"};
        let comparison=if args.oldest{">"}else{"<"};
        let cursor_rank=cursor["unknown"].as_i64().unwrap_or(0);
        let cursor_time=cursor["time"].as_str().unwrap_or_default();
        let cursor_id=cursor["id"].as_str().unwrap_or_default();
        let rows_sql=format!("{sql} SELECT entry_id,revision_id,title,namespace,native_kind,published_at,published_precision,first_seen_at,account_id,account_name,native_account_key,profile_url,creator_id,creator_name,native_entry_key,
          (SELECT v.blob_id FROM media_items m JOIN media_variants v ON v.media_id=m.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE m.revision_id=filtered.revision_id AND m.role='cover' LIMIT 1)
          FROM filtered WHERE (?8='' OR (published_at IS NULL)>?9 OR ((published_at IS NULL)=?9 AND (COALESCE(published_at,'') {comparison} ?10 OR (COALESCE(published_at,'')=?10 AND entry_id {comparison} ?8))))
          ORDER BY (published_at IS NULL),published_at {direction},entry_id {direction} LIMIT ?11");
        let mut items:Vec<Value>=c.prepare(&rows_sql)?.query_map(rusqlite::params![args.creator_id,args.account_id,args.namespace,args.kind,args.save_state,query,args.entry_id,cursor_id,cursor_rank,cursor_time,limit+1],|r|{
            let ns:String=r.get(3)?;let key:String=r.get(14)?;
            let source_url=if ns.starts_with("bilibili") && key.bytes().all(|b|b.is_ascii_alphanumeric()){Some(format!("https://www.bilibili.com/video/{key}"))}else{None};
            Ok(json!({"entry_id":r.get::<_,String>(0)?,"current_revision_id":r.get::<_,String>(1)?,"title":r.get::<_,String>(2)?,"namespace":ns,"kind":r.get::<_,String>(4)?,"published_at":r.get::<_,Option<String>>(5)?,"published_precision":r.get::<_,Option<String>>(6)?,"discovered_at":r.get::<_,String>(7)?,"account_id":r.get::<_,String>(8)?,"account_name":r.get::<_,Option<String>>(9)?,"native_account_key":r.get::<_,String>(10)?,"profile_url":r.get::<_,Option<String>>(11)?,"creator_id":r.get::<_,Option<String>>(12)?,"creator_name":r.get::<_,Option<String>>(13)?,"source_key":key,"source_url":source_url,"cover_blob_id":r.get::<_,Option<String>>(15)?}))
        })?.collect::<Result<_,_>>()?;
        let more=items.len()>limit as usize;items.truncate(limit as usize);
        for item in &mut items {
            let (access,progress,quality,summary) = crate::ipc::dispatch::entry_states_sync(c,item["entry_id"].as_str().unwrap_or_default())?;
            item["access_state"]=access.into(); item["save_progress"]=progress.into(); item["quality_state"]=quality.into(); item["media_summary"]=summary;
        }
        let next=if more {items.last().map(|i|json!({"scope":scope,"unknown":if i["published_at"].is_null(){1}else{0},"time":i["published_at"].as_str().unwrap_or_default(),"id":i["entry_id"]}).to_string())}else{None};
        Ok(json!({"items":items,"next_cursor":next,"counts":{"global":global,"filtered":filtered,"scope":"all-matching-entries"}}))
    }).await
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn u04_filters_counts_cursor_unknown_time_and_order_share_scope() {
        let dir = tempfile::tempdir().unwrap();
        let (db, _actor) = crate::db::DbActor::start(
            rusqlite::Connection::open(dir.path().join("feed.db")).unwrap(),
            64,
        )
        .unwrap();
        db.write(|tx|{
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')",[])?;
            for i in 0..65 {
                let id=format!("e-{i:03}");let revision=format!("r-{i:03}");
                tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'bilibili','video',?1,'a','t',?2)",rusqlite::params![id,if i>=60{None}else{Some(format!("2026-10-01T00:00:{i:02}Z"))}])?;
                tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,'d','正文标题','正文','t')",rusqlite::params![revision,id])?;
                tx.execute("INSERT INTO search_documents(doc_kind,ref_id,entry_id,normalized_text,index_version) VALUES('entry',?1,?1,'正文标题',1)",[id])?;
            }Ok(())
        }).await.unwrap();
        let member = query(
            &db,
            serde_json::from_value(json!({"entry_id":"e-064","query":"正文","limit":1})).unwrap(),
        )
        .await
        .unwrap();
        assert_eq!(member["items"][0]["entry_id"], "e-064");
        assert_eq!(member["counts"]["filtered"], 1);
        let excluded = query(
            &db,
            serde_json::from_value(json!({"entry_id":"e-064","namespace":"fake"})).unwrap(),
        )
        .await
        .unwrap();
        assert!(excluded["items"].as_array().unwrap().is_empty());
        for oldest in [false, true] {
            let mut cursor = Value::Null;
            let mut ids = Vec::new();
            loop {
                let args=serde_json::from_value(json!({"query":"正文","kind":"video","namespace":"bilibili","oldest":oldest,"limit":17,"cursor":cursor})).unwrap();
                let page = query(&db, args).await.unwrap();
                assert_eq!(page["counts"]["filtered"], 65);
                assert_eq!(page["counts"]["global"], 65);
                for item in page["items"].as_array().unwrap() {
                    ids.push(item["entry_id"].as_str().unwrap().to_owned());
                }
                cursor = page["next_cursor"].clone();
                if cursor.is_null() {
                    break;
                }
            }
            assert_eq!(ids.len(), 65);
            let unique: std::collections::HashSet<_> = ids.iter().collect();
            assert_eq!(unique.len(), 65);
            assert!(ids[60..].iter().all(|id| id.as_str() >= "e-060"));
            assert_eq!(ids[0], if oldest { "e-000" } else { "e-059" });
        }
        let page = query(
            &db,
            serde_json::from_value(json!({"query":"不存在"})).unwrap(),
        )
        .await
        .unwrap();
        assert_eq!(page["counts"]["filtered"], 0);
        assert_eq!(page["counts"]["global"], 65);
        assert!(page["items"].as_array().unwrap().is_empty());
    }
}
