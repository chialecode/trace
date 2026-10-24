//! 中文与字面搜索（docs/design.md §13.2，C17/C18）。
//!
//! 三级检索：FTS5 trigram（≥3 字）→ 单/双字候选表（1-2 字）→
//! 有界字面扫描兜底；所有路径都在规范化文本上做字面复核，特殊字符
//! 不构成 FTS/SQL 语法。索引由 Outbox 驱动，可重建、有代次。

use crate::db::DbHandle;
use crate::error::CoreError;
use trace_contracts::control::{SearchResultItemDto, SearchResultsDto, SearchStatusDto};
use trace_domain::search::{SEARCH_NORMALIZATION_VERSION, normalize_v1};

/// 把规范化文本拆成 1/2 字 gram 集合。
pub fn grams(text: &str) -> (Vec<String>, Vec<String>) {
    let chars: Vec<char> = text.chars().collect();
    let mut uni = std::collections::BTreeSet::new();
    let mut bi = std::collections::BTreeSet::new();
    for (i, c) in chars.iter().enumerate() {
        if !c.is_whitespace() {
            uni.insert(c.to_string());
        }
        if i + 1 < chars.len() && !c.is_whitespace() && !chars[i + 1].is_whitespace() {
            bi.insert(format!("{c}{}", chars[i + 1]));
        }
    }
    (uni.into_iter().collect(), bi.into_iter().collect())
}

/// One pending index event: the outbox row and the entry it names.
struct Event {
    seq: i64,
    entry_id: String,
}

/// The current state of an entry, as the index sees it.
struct EntryDoc {
    namespace: String,
    title: String,
    account_id: Option<String>,
    published_at: Option<String>,
    body: Option<String>,
    kind: String,
    key: String,
}

/// An entry ready to be written to the index.
struct Document {
    creator_id: Option<String>,
    namespace: String,
    published_at: Option<String>,
    title: String,
    normalized: String,
    unigrams: Vec<String>,
    bigrams: Vec<String>,
    /// Estimated bytes the document and its grams occupy.
    charge: i64,
}

const MARK_PROCESSED: &str = "UPDATE outbox SET processed_at=?2 WHERE seq=?1";

/// Outbox → 搜索文档同步（有界批次）。
pub async fn index_pending(db: &DbHandle, batch: i64) -> Result<i64, CoreError> {
    let (epoch, enabled): (i64, bool) = db
        .read(|c| {
            Ok(c.query_row(
                "SELECT generation,enabled=1 AND budget_blocked=0 FROM search_state WHERE id=1",
                [],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await?;
    if !enabled {
        return Ok(0);
    }
    let events = pending_events(db, batch).await?;
    if events.is_empty() {
        db.write(move |tx| {
            tx.execute(
                "UPDATE search_state SET rebuilding=0 WHERE generation=?1",
                [epoch],
            )?;
            Ok(())
        })
        .await?;
        return Ok(0);
    }
    let mut processed = 0i64;
    for event in events {
        if !index_event(db, epoch, &event).await? {
            break;
        }
        processed += 1;
    }
    Ok(processed)
}

/// 读取 pending outbox（entry/creator 相关）。
async fn pending_events(db: &DbHandle, batch: i64) -> Result<Vec<Event>, CoreError> {
    db.read(move |c| {
        let mut stmt = c.prepare(
            "SELECT seq, aggregate_id FROM outbox
             WHERE processed_at IS NULL AND topic LIKE 'entry%' ORDER BY seq LIMIT ?1",
        )?;
        let rows = stmt
            .query_map([batch], |r| {
                Ok(Event {
                    seq: r.get(0)?,
                    entry_id: r.get(1)?,
                })
            })?
            .collect::<Result<Vec<_>, _>>()?;
        Ok(rows)
    })
    .await
}

/// 以 Entry 当前版本重建文档（幂等 upsert）。`false` 表示索引已失效或预算用尽，应停止本批。
async fn index_event(db: &DbHandle, epoch: i64, event: &Event) -> Result<bool, CoreError> {
    let seq = event.seq;
    let Some(doc) = load_entry(db, &event.entry_id).await? else {
        db.write(move |tx| {
            tx.execute(MARK_PROCESSED, rusqlite::params![seq, discovery_time()])?;
            Ok(())
        })
        .await?;
        return Ok(true);
    };
    let document = prepare(db, doc).await?;
    let entry_id = event.entry_id.clone();
    db.write(move |tx| write_document(tx, epoch, seq, &entry_id, document))
        .await
}

fn discovery_time() -> String {
    trace_domain::time::TraceTime::now(trace_domain::time::TimePrecision::Discovery).rfc3339
}

async fn load_entry(db: &DbHandle, entry_id: &str) -> Result<Option<EntryDoc>, CoreError> {
    let entry_id = entry_id.to_owned();
    db.read(move |c| {
        Ok(c.query_row(
            "SELECT e.namespace, COALESCE(r.title,''), e.account_id, e.published_at, COALESCE(r.normalized_body,''),e.native_kind,e.native_entry_key
             FROM entries e
             LEFT JOIN current_entry_revisions r ON r.entry_id = e.entry_id
             WHERE e.entry_id = ?1 AND e.lifecycle!='deleted'
             LIMIT 1",
            [&entry_id],
            |r| {
                Ok(EntryDoc {
                    namespace: r.get(0)?,
                    title: r.get(1)?,
                    account_id: r.get(2)?,
                    published_at: r.get(3)?,
                    body: r.get(4)?,
                    kind: r.get(5)?,
                    key: r.get(6)?,
                })
            },
        )
        .ok())
    })
    .await
}

/// Normalizes the entry's text (title, body and source link) and sizes its grams.
async fn prepare(db: &DbHandle, doc: EntryDoc) -> Result<Document, CoreError> {
    let account_id = doc.account_id;
    let creator_id: Option<String> = db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
                [&account_id],
                |r| r.get(0),
            )
            .ok())
        })
        .await?;
    let source = crate::feed::source_url(&doc.namespace, &doc.kind, &doc.key).unwrap_or_default();
    let normalized = normalize_v1(&format!(
        "{}\n{}\n{source}",
        doc.title,
        doc.body.as_deref().unwrap_or("")
    ));
    let (unigrams, bigrams) = grams(&normalized);
    let charge = (normalized.len() as i64)
        .saturating_mul(4)
        .saturating_add(1024)
        .saturating_add(128 * (unigrams.len() + bigrams.len()) as i64);
    Ok(Document {
        creator_id,
        namespace: doc.namespace,
        published_at: doc.published_at,
        title: doc.title,
        normalized,
        unigrams,
        bigrams,
        charge,
    })
}

/// Replaces the entry's document in one transaction, unless the index was rebuilt or its
/// budget is used up meanwhile.
fn write_document(
    tx: &rusqlite::Transaction<'_>,
    epoch: i64,
    seq: i64,
    entry_id: &str,
    doc: Document,
) -> Result<bool, CoreError> {
    let (current, enabled, used, budget): (i64, bool, i64, i64) = tx.query_row(
        "SELECT generation,enabled,estimated_bytes,budget_bytes FROM search_state WHERE id=1",
        [],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
    )?;
    if current != epoch || !enabled {
        return Ok(false);
    }
    let old: i64 = tx.query_row(
        "SELECT COALESCE(sum(cache_bytes),0) FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
        [entry_id],
        |r| r.get(0),
    )?;
    if used.saturating_sub(old).saturating_add(doc.charge) > budget {
        tx.execute("UPDATE search_state SET budget_blocked=1 WHERE id=1", [])?;
        return Ok(false);
    }
    let active: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND lifecycle!='deleted')",
        [entry_id],
        |r| r.get(0),
    )?;
    if !active {
        tx.execute(MARK_PROCESSED, rusqlite::params![seq, crate::jobs::now()])?;
        return Ok(true);
    }
    // 删除旧版文档（级联 gram；FTS 由触发器清理）。
    tx.execute(
        "DELETE FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
        [entry_id],
    )?;
    tx.execute(
        "INSERT INTO search_documents (doc_kind, ref_id, entry_id, creator_id, namespace, published_at, title, normalized_text, index_version,cache_bytes)
         VALUES ('entry', ?1, ?1, ?2, ?3, ?4, ?5, ?6, ?7,?8)",
        rusqlite::params![
            entry_id,
            doc.creator_id,
            doc.namespace,
            doc.published_at,
            doc.title,
            doc.normalized,
            SEARCH_NORMALIZATION_VERSION,
            doc.charge
        ],
    )?;
    let doc_id: i64 = tx.query_row(
        "SELECT doc_id FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
        [entry_id],
        |r| r.get(0),
    )?;
    for g in doc.unigrams {
        tx.execute(
            "INSERT OR IGNORE INTO search_unigram (gram, doc_id) VALUES (?1, ?2)",
            rusqlite::params![g, doc_id],
        )?;
    }
    for g in doc.bigrams {
        tx.execute(
            "INSERT OR IGNORE INTO search_bigram (gram, doc_id) VALUES (?1, ?2)",
            rusqlite::params![g, doc_id],
        )?;
    }
    tx.execute(MARK_PROCESSED, rusqlite::params![seq, discovery_time()])?;
    tx.execute(
        "UPDATE search_state SET indexed_seq=MAX(indexed_seq, ?1)",
        [seq],
    )?;
    Ok(true)
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum QueryMode {
    Fts,
    Gram,
    Literal,
}

pub async fn query(
    db: &DbHandle,
    args: trace_contracts::control::SearchQueryArgs,
) -> Result<SearchResultsDto, CoreError> {
    db.read(crate::cache::search_ready).await?;
    let normalized = normalize_v1(&args.query);
    let mode = match normalized.chars().count() {
        0 => "literal",
        1 | 2 => "gram",
        _ => "fts",
    };
    let status = status(db).await?;
    if normalized.is_empty() {
        return Ok(SearchResultsDto {
            items: Vec::new(),
            next_cursor: None,
            mode: mode.into(),
            status,
        });
    }
    // Public search and the timeline share membership, current revisions, date
    // bounds and scoped cursors. Never pretruncate candidates before filtering.
    let page = crate::feed::query(
        db,
        trace_contracts::control::ArchiveTimelineArgs {
            authentication: args.authentication,
            query: Some(normalized.clone()),
            creator_id: args.creator_id,
            namespace: args.namespace,
            kind: args.kind,
            published_from: args.from,
            published_before: args.to,
            cursor: args.cursor,
            limit: args.limit,
            entry_id: None,
            account_id: None,
            save_state: None,
            oldest: false,
            min_height: None,
            codec: None,
            sort: None,
            tag: None,
            content_kind: None,
        },
    )
    .await?;
    let rows = page["items"]
        .as_array()
        .cloned()
        .ok_or_else(|| CoreError::Internal("search page".into()))?;
    let items = db.read(move |c| {
        let mut out = Vec::with_capacity(rows.len());
        let mut body = c.prepare("SELECT substr(normalized_body, MAX(1,instr(normalized_body,?2)-10), 100) FROM entry_revisions WHERE revision_id=?1")?;
        for row in rows {
            let field = |name: &str| row[name].as_str().map(str::to_owned);
            let revision = field("current_revision_id");
            // Load only a short excerpt, even when an archived article is huge.
            let text: String = body.query_row(rusqlite::params![revision, normalized], |r| r.get(0))?;
            out.push(SearchResultItemDto { doc_kind: "entry".into(), entry_id: field("entry_id"),
                revision_id: revision, creator_id: field("creator_id"), namespace: field("namespace"),
                title: field("title"), snippet: snippet(&text,&normalized), published_at: field("published_at") });
        }
        Ok(out)
    }).await?;
    Ok(SearchResultsDto {
        items,
        next_cursor: page["next_cursor"].as_str().map(str::to_owned),
        mode: mode.into(),
        status,
    })
}

fn snippet(text: &str, needle: &str) -> String {
    match text.find(needle) {
        Some(pos) => {
            let start = text
                .char_indices()
                .map(|(i, _)| i)
                .filter(|i| *i <= pos)
                .rev()
                .nth(10)
                .unwrap_or(0);
            let mut s = text[start.min(text.len())..].to_string();
            if s.chars().count() > 80 {
                s = s.chars().take(80).collect();
            }
            s
        }
        None => text.chars().take(80).collect(),
    }
}

pub async fn status(db: &DbHandle) -> Result<SearchStatusDto, CoreError> {
    db.read(|c| {
        let (indexed, _version, rebuilding): (i64, i64, i64) = c.query_row(
            "SELECT indexed_seq, index_version, rebuilding FROM search_state WHERE id=1",
            [],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
        )?;
        let total: i64 =
            c.query_row("SELECT COALESCE(MAX(seq),0) FROM outbox", [], |r| r.get(0))?;
        Ok(SearchStatusDto {
            indexed_seq: indexed,
            total_seq: total,
            lagging: total > indexed,
            rebuilding: rebuilding == 1,
        })
    })
    .await
}

/// 全量重建：清空文档表后按 Entry 重放（新代次，追平前不切换语义由
/// 状态行表达——M1a 单索引表重建期间 lagging=true 明示追赶中）。
pub async fn rebuild(db: &DbHandle) -> Result<(), CoreError> {
    db.write(|tx| {
        tx.execute("DELETE FROM search_documents", [])?;
        tx.execute("UPDATE search_state SET rebuilding=1, indexed_seq=0,enabled=1,budget_blocked=0,generation=generation+1", [])?;
        tx.execute(
            "UPDATE outbox SET processed_at=NULL WHERE topic LIKE 'entry%'",
            [],
        )?;
        Ok(())
    })
    .await?;
    // 追平。
    loop {
        let n = index_pending(db, 500).await?;
        if n == 0 {
            break;
        }
    }
    db.write(|tx| {
        tx.execute(
            "UPDATE search_state SET rebuilding=0 WHERE budget_blocked=0",
            [],
        )?;
        Ok(())
    })
    .await
    .map(|_: ()| ())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn grams_cover_single_and_double_chars() {
        let (uni, bi) = grams("中文 abc");
        assert!(uni.contains(&"中".to_string()) && uni.contains(&"a".to_string()));
        assert!(bi.contains(&"中文".to_string()));
        assert!(!uni.contains(&" ".to_string()), "whitespace excluded");
        assert!(!bi.iter().any(|g| g.contains(' ')));
    }

    #[test]
    fn snippet_is_bounded() {
        let s = snippet("x".repeat(500).as_str(), "x");
        assert!(s.chars().count() <= 80);
    }
}
