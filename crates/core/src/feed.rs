//! Timeline query contract: filters, stable cursor and counts share a database snapshot.
use crate::{db::DbHandle, error::CoreError};
use serde_json::{Value, json};
use trace_contracts::control::ArchiveTimelineArgs;

pub async fn query(db: &DbHandle, args: ArchiveTimelineArgs) -> Result<Value, CoreError> {
    for value in [&args.published_from, &args.published_before]
        .into_iter()
        .flatten()
    {
        time::OffsetDateTime::parse(value, &time::format_description::well_known::Rfc3339)
            .map_err(|_| CoreError::InvalidInput("invalid publication date filter".into()))?;
    }
    if args.codec.as_ref().is_some_and(|c| c.len() > 64) {
        return Err(CoreError::InvalidInput("codec filter budget".into()));
    }
    if [&args.tag, &args.content_kind]
        .into_iter()
        .flatten()
        .any(|t| t.len() > 128)
    {
        return Err(CoreError::InvalidInput("filter budget".into()));
    }
    sort_spec(args.sort.as_deref())?;
    db.read(move |c| timeline_page(c, &args)).await
}

fn timeline_page(c: &rusqlite::Connection, args: &ArchiveTimelineArgs) -> Result<Value, CoreError> {
    let query = trace_domain::search::normalize_v1(args.query.as_deref().unwrap_or_default());
    if !query.is_empty() {
        crate::cache::search_ready(c)?;
    }
    let scope = cursor_scope(args, &query);
    let cursor = decode_cursor(args, &scope)?;
    let limit = args.limit.clamp(1, 200);
    let rows_sql = rows_sql(c, args, &query)?;
    let phrase = trace_domain::search::fts_quote_phrase(&query);
    let cursor_rank = cursor["unknown"].as_i64().unwrap_or(0);
    let cursor_time = cursor["time"].as_str().unwrap_or_default();
    let cursor_id = cursor["id"].as_str().unwrap_or_default();
    let params = rusqlite::params![
        args.creator_id,
        args.account_id,
        args.namespace,
        args.kind,
        args.save_state,
        query,
        args.entry_id,
        cursor_id,
        cursor_rank,
        cursor_time,
        limit + 1,
        phrase,
        args.published_from,
        args.published_before,
        args.min_height,
        args.codec,
        args.authentication.map(|s| s.as_str()),
        args.tag
            .as_deref()
            .map(|t| t.trim().trim_start_matches('#').to_owned()),
        args.content_kind
    ];
    let (filtered, global, rows): (i64, i64, String) = c
        .prepare_cached(&rows_sql)?
        .query_row(params, |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?;
    let mut items: Vec<Value> = serde_json::from_str(&rows)?;
    let more = items.len() > limit as usize;
    items.truncate(limit as usize);
    annotate(c, &mut items)?;
    let next = if more {
        next_cursor(&items, &scope)
    } else {
        None
    };
    for item in &mut items {
        if let Some(fields) = item.as_object_mut() {
            fields.remove("sort_key");
        }
    }
    Ok(json!({
        "items": items,
        "next_cursor": next,
        "counts": {"global": global, "filtered": filtered, "scope": "all-matching-entries"},
    }))
}

/// Everything that shapes the result; a cursor only continues the query that produced it.
fn cursor_scope(args: &ArchiveTimelineArgs, query: &str) -> Value {
    let mut scope = json!([
        args.creator_id,
        args.account_id,
        args.namespace,
        args.kind,
        args.save_state,
        query,
        args.oldest,
        args.published_from,
        args.published_before,
        args.min_height,
        args.codec,
        args.authentication,
        args.sort,
        args.tag,
        args.content_kind
    ]);
    if let Some(id) = &args.entry_id {
        scope.as_array_mut().unwrap().push(json!(id));
    }
    scope
}

/// How the page is ordered: the column the cursor compares and what `scoped` must provide.
struct SortSpec {
    /// The ordering column of the `scoped`/`base` rows.
    key: &'static str,
    /// Extra select expression and join that give `scoped` that column.
    select: &'static str,
}

/// Unknown values (never published, never counted) sort after known ones in both directions.
fn sort_spec(sort: Option<&str>) -> Result<SortSpec, CoreError> {
    let numeric = |column: &str| match column {
        "plays" => {
            ", CASE WHEN sm.plays IS NULL THEN NULL ELSE printf('%020d',sm.plays) END AS sort_key"
        }
        "likes" => {
            ", CASE WHEN sm.likes IS NULL THEN NULL ELSE printf('%020d',sm.likes) END AS sort_key"
        }
        "comments" => {
            ", CASE WHEN sm.comments IS NULL THEN NULL ELSE printf('%020d',sm.comments) END AS sort_key"
        }
        "favorites" => {
            ", CASE WHEN sm.favorites IS NULL THEN NULL ELSE printf('%020d',sm.favorites) END AS sort_key"
        }
        _ => {
            ", CASE WHEN sm.max_duration_ms IS NULL THEN NULL ELSE printf('%020d',sm.max_duration_ms) END AS sort_key"
        }
    };
    match sort {
        None | Some("published") => Ok(SortSpec {
            key: "published_at",
            select: "",
        }),
        Some(column @ ("plays" | "likes" | "comments" | "favorites" | "duration")) => {
            Ok(SortSpec {
                key: "sort_key",
                select: numeric(column),
            })
        }
        Some(_) => Err(CoreError::InvalidInput("unknown timeline sort".into())),
    }
}

fn decode_cursor(args: &ArchiveTimelineArgs, scope: &Value) -> Result<Value, CoreError> {
    let cursor: Value = match args.cursor.as_deref() {
        Some(s) => serde_json::from_str(s)?,
        None => Value::Null,
    };
    if !cursor.is_null() && cursor["scope"] != *scope {
        return Err(CoreError::InvalidInput(
            "cursor does not match query".into(),
        ));
    }
    Ok(cursor)
}

fn cursor_filter(args: &ArchiveTimelineArgs, key: &str) -> String {
    let comparison = if args.oldest { ">" } else { "<" };
    format!(
        "(?8='' OR ({key} IS NULL)>?9 OR (({key} IS NULL)=?9 AND (COALESCE({key},'') {comparison} ?10 OR (COALESCE({key},'')=?10 AND entry_id {comparison} ?8))))"
    )
}

/// The search predicate and index hint for a normalized query (short queries use the
/// unigram/bigram tables, longer ones the trigram index).
fn search_clause(
    c: &rusqlite::Connection,
    query: &str,
) -> Result<(String, &'static str), CoreError> {
    Ok(match query.chars().count() {
        0=>("0".to_owned(),""),
        n if n<=2=>{
            let table=if n==1{"search_unigram"}else{"search_bigram"};
            let indexed:bool=c.query_row(&format!("SELECT EXISTS(SELECT 1 FROM {table} LIMIT 1)"),[],|r|r.get(0))?;
            if indexed && !query.chars().any(char::is_whitespace) {
                (format!("d.doc_id IN (SELECT doc_id FROM {table} WHERE gram=?6)"),"INDEXED BY idx_search_scope")
            } else { ("instr(d.normalized_text,?6)>0".into(),"") }
        }
        _ if query.chars().all(|c|c.is_ascii_alphanumeric() || ('\u{4e00}'..='\u{9fff}').contains(&c)) =>
            // A single quoted trigram phrase on normalized ASCII/CJK is an
            // exact substring. Other Unicode/punctuation keeps literal recheck.
            ("d.doc_id IN (SELECT rowid FROM search_fts WHERE search_fts MATCH ?12)".into(),"INDEXED BY idx_search_scope"),
        _=>("d.doc_id IN (SELECT rowid FROM search_fts WHERE search_fts MATCH ?12) AND instr(d.normalized_text,?6)>0".into(),""),
    })
}

/// The shared matching/scoped/base/filtered statement prefix of the timeline query.
fn matching_sql(
    c: &rusqlite::Connection,
    args: &ArchiveTimelineArgs,
    query: &str,
) -> Result<String, CoreError> {
    let direction = if args.oldest { "ASC" } else { "DESC" };
    let sort = sort_spec(args.sort.as_deref())?;
    let key = sort.key;
    let cursor_filter = cursor_filter(args, key);
    // The unified record is joined only when an ordering or a filter reads it.
    let meta_join = if sort.select.is_empty() && args.content_kind.is_none() {
        ""
    } else {
        "LEFT JOIN entry_meta sm ON sm.entry_id=e.entry_id"
    };
    let progress = crate::store::entries::SAVE_PROGRESS_SQL;
    // Select the bounded timeline page before loading revisions, media and
    // quality evidence. Save-state filters still evaluate their full scope.
    let page_source = if args.save_state.is_none() {
        format!(
            "(SELECT * FROM scoped WHERE EXISTS(SELECT 1 FROM entry_revisions r WHERE r.entry_id=scoped.entry_id) AND {cursor_filter} ORDER BY ({key} IS NULL),{key} {direction},entry_id {direction} LIMIT ?11)"
        )
    } else {
        "scoped".into()
    };
    let select = sort.select;
    // Only a query that asked for a kind of content has the unified record joined.
    let kind_clause = if args.content_kind.is_some() {
        " OR EXISTS(SELECT 1 FROM json_each(sm.content_kinds) WHERE value=?19)"
    } else {
        ""
    };
    let (search_filter, document_index) = search_clause(c, query)?;
    let namespace_filter = if args.namespace.is_some() {
        "e.namespace=?3"
    } else {
        "?3 IS NULL"
    };
    let sql = format!(
        r#"WITH matching AS MATERIALIZED (
      SELECT d.entry_id FROM search_documents d {document_index} WHERE ?6!='' AND (?3 IS NULL OR d.namespace=?3 OR d.namespace IS NULL) AND ({search_filter})
      UNION SELECT e.entry_id FROM entries e WHERE ?6!='' AND e.account_id IN (
        SELECT a.account_id FROM source_accounts a LEFT JOIN account_bindings b ON b.account_id=a.account_id AND b.state='active' LEFT JOIN creators cr ON cr.creator_id=b.creator_id
        WHERE instr(lower(COALESCE(cr.display_name,'')),?6)>0 OR instr(lower(COALESCE(a.display_name,'')),?6)>0 OR instr(lower(a.native_account_key),?6)>0 OR EXISTS(SELECT 1 FROM json_each(cr.aliases) WHERE instr(lower(value),?6)>0) OR EXISTS(SELECT 1 FROM json_each(cr.tags) WHERE instr(lower(value),?6)>0))
    ), scoped AS NOT MATERIALIZED (
      SELECT e.*{select} FROM entries e {meta_join} WHERE e.lifecycle!='deleted' AND (?2 IS NULL OR e.account_id=?2) AND {namespace_filter}
      AND (?18 IS NULL OR EXISTS(SELECT 1 FROM entry_terms t WHERE t.entry_id=e.entry_id AND t.kind='tag' AND t.value=?18))
      AND (?19 IS NULL{kind_clause})
      AND (?17 IS NULL OR EXISTS(SELECT 1 FROM acquisition_contexts x WHERE x.entry_id=e.entry_id AND x.auth_state=?17) OR (?17='unknown' AND NOT EXISTS(SELECT 1 FROM acquisition_contexts x WHERE x.entry_id=e.entry_id)))
      AND (?4 IS NULL OR e.native_kind=?4 OR (?4='image' AND e.native_kind='opus')) AND (?7 IS NULL OR e.entry_id=?7)
      AND (?1 IS NULL OR EXISTS(SELECT 1 FROM account_bindings b WHERE b.account_id=e.account_id AND b.state='active' AND b.creator_id=?1))
      AND (?6='' OR e.entry_id IN (SELECT entry_id FROM matching)) AND (?12 IS NOT NULL)
      AND (?13 IS NULL OR julianday(e.published_at)>=julianday(?13))
      AND (?14 IS NULL OR julianday(e.published_at)<julianday(?14))
      AND ((?15 IS NULL AND ?16 IS NULL) OR EXISTS(SELECT 1 FROM media_items m JOIN media_variants v ON v.media_id=m.media_id
        WHERE m.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=e.entry_id)
        AND EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=v.blob_id AND rp.state='verified')
        AND (?15 IS NULL OR json_extract(v.measured_spec,'$.height')>=?15)
        AND (?16 IS NULL OR lower(json_extract(v.measured_spec,'$.codec'))=lower(?16))))
    ), base AS (
      SELECT e.*,r.revision_id,r.title,r.normalized_body,a.display_name AS account_name,
        a.native_account_key,a.profile_url,b.creator_id,cr.display_name AS creator_name,
        {progress} AS save_progress
      FROM {page_source} e JOIN source_accounts a ON a.account_id=e.account_id
      LEFT JOIN account_bindings b ON b.account_id=e.account_id AND b.state='active'
      LEFT JOIN creators cr ON cr.creator_id=b.creator_id
      JOIN entry_revisions r ON r.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=e.entry_id)
    ), filtered AS (SELECT * FROM base WHERE (?5 IS NULL OR save_progress=?5))
    "#
    );

    Ok(sql)
}

/// The statement that returns the total count and the JSON of one bounded page.
fn rows_sql(
    c: &rusqlite::Connection,
    args: &ArchiveTimelineArgs,
    query: &str,
) -> Result<String, CoreError> {
    let sql = matching_sql(c, args, query)?;
    let direction = if args.oldest { "ASC" } else { "DESC" };
    let key = sort_spec(args.sort.as_deref())?.key;
    let cursor_filter = cursor_filter(args, key);
    let count_source = if args.save_state.is_none() {
        "scoped WHERE EXISTS(SELECT 1 FROM entry_revisions r WHERE r.entry_id=scoped.entry_id)"
    } else {
        "filtered"
    };
    // One SQLite statement shares the matching set between total and page.
    // JSON aggregation still loads at most limit+1 rows, including empty pages.
    let rows_sql = format!(
        r#"{sql}, page AS (
      SELECT * FROM filtered WHERE {cursor_filter}
      ORDER BY ({key} IS NULL),{key} {direction},entry_id {direction} LIMIT ?11
    ) SELECT (SELECT count(*) FROM {count_source}),(SELECT count(*) FROM entries WHERE lifecycle!='deleted'),
      json_group_array(json_object('entry_id',entry_id,'current_revision_id',revision_id,'title',title,
        'namespace',namespace,'kind',native_kind,'published_at',published_at,'published_precision',published_precision,'sort_key',{key},
        'discovered_at',first_seen_at,'account_id',account_id,'account_name',account_name,'native_account_key',native_account_key,
        'profile_url',profile_url,'creator_id',creator_id,'creator_name',creator_name,'source_key',native_entry_key,
        'cover_blob_id',(SELECT v.blob_id FROM media_items m JOIN media_variants v ON v.media_id=m.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE m.revision_id=page.revision_id AND m.role='cover' LIMIT 1))) FROM page"#
    );
    Ok(rows_sql)
}

/// Adds the public link and the per-entry state summaries to the page rows.
fn annotate(c: &rusqlite::Connection, items: &mut [Value]) -> Result<(), CoreError> {
    for item in items.iter_mut() {
        let text = |name: &str| item[name].as_str().unwrap_or_default().to_owned();
        let (namespace, kind, key) = (text("namespace"), text("kind"), text("source_key"));
        let entry_id = text("entry_id");
        item["source_url"] = json!(entry_link(c, &entry_id, &namespace, &kind, &key));
        let (access, progress, quality, summary) = crate::store::entries::states(c, &entry_id)?;
        item["access_state"] = access.into();
        item["save_progress"] = progress.into();
        item["quality_state"] = quality.into();
        item["media_summary"] = summary;
        item["authentication"] = crate::acquisitions::summary(c, &entry_id)?;
        item["meta"] = serde_json::to_value(crate::store::meta::summary(c, &entry_id)?)?;
    }
    Ok(())
}

fn next_cursor(items: &[Value], scope: &Value) -> Option<String> {
    items.last().map(|i| {
        json!({
            "scope": scope,
            "unknown": if i["sort_key"].is_null() { 1 } else { 0 },
            "time": i["sort_key"].as_str().unwrap_or_default(),
            "id": i["entry_id"],
        })
        .to_string()
    })
}

/// The link a person can open for an entry: the address the site itself gave the work
/// when the observation kept one, else the one rebuilt from the native identity.
pub fn entry_link(
    c: &rusqlite::Connection,
    entry_id: &str,
    namespace: &str,
    kind: &str,
    key: &str,
) -> Option<String> {
    let stored: Option<String> = c
        .query_row(
            "SELECT canonical_url FROM entry_meta WHERE entry_id=?1",
            [entry_id],
            |r| r.get(0),
        )
        .ok()
        .flatten();
    stored
        .filter(|u| !u.is_empty())
        .or_else(|| source_url(namespace, kind, key))
}

/// Stable public links are reconstructed from validated native identities.
pub fn source_url(namespace: &str, kind: &str, key: &str) -> Option<String> {
    let site = namespace.split(':').next()?;
    if kind == "live" {
        let room = key.split(':').next()?;
        if !room.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
        return match site {
            "bilibili" => Some(format!("https://live.bilibili.com/{room}")),
            _ => None,
        };
    }
    let alnum = !key.is_empty() && key.bytes().all(|b| b.is_ascii_alphanumeric());
    // Short ids of video and photo sites also use `-` and `_`.
    let token = !key.is_empty()
        && key.len() <= 64
        && key
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_');
    match (site, kind) {
        ("bilibili", "video") if alnum => Some(format!("https://www.bilibili.com/video/{key}")),
        ("bilibili", "dynamic" | "opus") if alnum => {
            Some(format!("https://www.bilibili.com/opus/{key}"))
        }
        ("bilibili", "article") if alnum => Some(format!("https://www.bilibili.com/read/cv{key}")),
        ("douyin", "video" | "image" | "article") if alnum => Some(format!(
            "https://www.douyin.com/{}{}",
            if kind == "video" { "video/" } else { "note/" },
            key
        )),
        ("weibo", "post") if alnum => Some(format!("https://weibo.com/detail/{key}")),
        ("youtube", "video") if token => Some(format!("https://www.youtube.com/watch?v={key}")),
        ("youtube", "short") if token => Some(format!("https://www.youtube.com/shorts/{key}")),
        ("youtube", "post") if token => Some(format!("https://www.youtube.com/post/{key}")),
        ("x", "post") if alnum => Some(format!("https://x.com/i/status/{key}")),
        ("instagram", "post" | "reel") if token => {
            Some(format!("https://www.instagram.com/p/{key}/"))
        }
        ("xiaohongshu", "note") if alnum => {
            Some(format!("https://www.xiaohongshu.com/explore/{key}"))
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn dates_specs_alias_tags_and_source_links_share_pagination() {
        let dir = tempfile::tempdir().unwrap();
        let (db, _actor) = crate::db::DbActor::start(
            rusqlite::Connection::open(dir.path().join("filters.db")).unwrap(),
            64,
        )
        .unwrap();
        db.write(|tx|{
            tx.execute("INSERT INTO creators(creator_id,display_name,aliases,tags,created_at,updated_at) VALUES('c','作者','[\"旧名\"]','[\"摄影\"]','t','t')",[])?;
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')",[])?;
            tx.execute("INSERT INTO account_bindings(binding_id,creator_id,account_id,state,created_at) VALUES('b','c','a','active','t')",[])?;
            for i in 0..4 {
                let e=format!("e{i}");let r=format!("r{i}");let m=format!("m{i}");let blob=format!("sha256:{i}");
                tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'bilibili','video',?2,'a','t',?3)",rusqlite::params![e,format!("BV{i}"),if i==3{None}else{Some(format!("2026-10-0{}T00:00:00Z",i+1))}])?;
                tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,?1,'正文','中文第一测试文本','2026-10-08T00:00:00Z')",rusqlite::params![r,e])?;
                tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,?2,'video',0,?1)",rusqlite::params![m,r])?;
                tx.execute("INSERT INTO blobs(blob_id,size,first_seen_at) VALUES(?1,1,'t')",[&blob])?;
                tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES(?1,?1,'source',?2,'{}',?3,'t')",rusqlite::params![m,blob,json!({"height":if i==1{1080}else{2160},"codec":"h264"}).to_string()])?;
                tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES(?1,?1,'n','s','x','verified')",[&blob])?;
                crate::objects::outbox_emit(tx,"entry.revision.created",&e,&json!({}))?;
            }
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('old','e1','old','旧版','旧文','2026-10-01T00:00:00Z')",[])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('old','old','video',0,'old')",[])?;
            tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES('old','old','source','sha256:0','{}','{\"height\":4320,\"codec\":\"h264\"}','t')",[])?;
            Ok(())
        }).await.unwrap();
        crate::search::index_pending(&db, 100).await.unwrap();
        for (filter, count) in [
            (json!({"query":"旧名"}), 4),
            (json!({"query":"摄影"}), 4),
            (json!({"query":"https://www.bilibili.com/video/BV1"}), 1),
            (json!({"min_height":2160,"codec":"h264"}), 3),
            (
                json!({"published_from":"2026-10-01T00:00:00Z","published_before":"2026-10-03T00:00:00Z","min_height":2160}),
                1,
            ),
            (json!({"codec":"av1"}), 0),
        ] {
            let result = query(&db, serde_json::from_value(filter).unwrap())
                .await
                .unwrap();
            assert_eq!(result["counts"]["filtered"], count);
            assert_eq!(result["items"].as_array().unwrap().len(), count as usize);
        }
        let page = query(
            &db,
            serde_json::from_value(json!({"min_height":2160,"limit":1})).unwrap(),
        )
        .await
        .unwrap();
        assert!(page["next_cursor"].is_string());
        assert!(
            query(
                &db,
                serde_json::from_value(json!({"min_height":1080,"cursor":page["next_cursor"]}))
                    .unwrap()
            )
            .await
            .is_err()
        );
        assert!(
            query(
                &db,
                serde_json::from_value(json!({"published_from":"not-a-date"})).unwrap()
            )
            .await
            .is_err()
        );
    }

    #[test]
    fn quoted_ascii_cjk_trigrams_equal_literal_substrings() {
        let c = rusqlite::Connection::open_in_memory().unwrap();
        c.execute_batch("CREATE VIRTUAL TABLE exact USING fts5(text,tokenize='trigram');")
            .unwrap();
        let documents = [
            "中文归档视频abcd",
            "测试abc中文123",
            "中中中abcabca",
            "空格 abc xyz",
            "not AND or 中文",
            "ábc e\u{301} 中文",
        ];
        for d in documents {
            c.execute(
                "INSERT INTO exact(text) VALUES(?1)",
                [trace_domain::search::normalize_v1(d)],
            )
            .unwrap();
        }
        let mut queries = vec!["没有匹配".to_owned(), "abc中文123".into()];
        for text in documents {
            let chars = text.chars().collect::<Vec<_>>();
            for length in 3..=chars.len() {
                for window in chars.windows(length) {
                    let q = window.iter().collect::<String>();
                    if q.chars().all(|c| {
                        c.is_ascii_alphanumeric() || ('\u{4e00}'..='\u{9fff}').contains(&c)
                    }) {
                        queries.push(trace_domain::search::normalize_v1(&q));
                    }
                }
            }
        }
        for q in queries {
            let matched = c
                .prepare("SELECT rowid FROM exact WHERE exact MATCH ?1 ORDER BY rowid")
                .unwrap()
                .query_map([trace_domain::search::fts_quote_phrase(&q)], |r| {
                    r.get::<_, i64>(0)
                })
                .unwrap()
                .collect::<Result<Vec<_>, _>>()
                .unwrap();
            let literal = c
                .prepare("SELECT rowid FROM exact WHERE instr(text,?1)>0 ORDER BY rowid")
                .unwrap()
                .query_map([&q], |r| r.get::<_, i64>(0))
                .unwrap()
                .collect::<Result<Vec<_>, _>>()
                .unwrap();
            assert_eq!(matched, literal, "{q}");
        }
    }

    #[tokio::test]
    async fn filters_counts_cursor_unknown_time_and_order_share_scope() {
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
