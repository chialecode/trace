//! 中文与字面搜索（docs/design.md §13.2，C17/C18）。
//!
//! 三级检索：FTS5 trigram（≥3 字）→ 单/双字候选表（1-2 字）→
//! 有界字面扫描兜底；所有路径都在规范化文本上做字面复核，特殊字符
//! 不构成 FTS/SQL 语法。索引由 Outbox 驱动，可重建、有代次。

use crate::db::DbHandle;
use crate::error::CoreError;
use trace_contracts::control::{SearchResultItemDto, SearchResultsDto, SearchStatusDto};
use trace_domain::search::{SEARCH_NORMALIZATION_VERSION, normalize_v1};

/// entries+entry_revisions 聚合行：(namespace, title, account_id, published_at, normalized_body)。
type EntryDocRow = (
    String,
    String,
    Option<String>,
    Option<String>,
    Option<String>,
    String,
    String,
);

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
    // 读取 pending outbox（entry/creator 相关）。
    let events: Vec<(i64, String, String, String)> = db
        .read(move |c| {
            let mut stmt = c.prepare(
                "SELECT seq, topic, aggregate_id, payload FROM outbox
                 WHERE processed_at IS NULL AND topic LIKE 'entry%' ORDER BY seq LIMIT ?1",
            )?;
            let rows = stmt
                .query_map([batch], |r| {
                    Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .await?;
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
    for (seq, topic, aggregate_id, _payload) in events {
        // 以 Entry 当前版本重建文档（幂等 upsert）。
        let entry_id = aggregate_id;
        let entry_for_read = entry_id.clone();
        let doc: Option<EntryDocRow> = db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT e.namespace, COALESCE(r.title,''), e.account_id, e.published_at, COALESCE(r.normalized_body,''),e.native_kind,e.native_entry_key
                     FROM entries e
                     LEFT JOIN current_entry_revisions r ON r.entry_id = e.entry_id
                     WHERE e.entry_id = ?1 AND e.lifecycle!='deleted'
                     LIMIT 1",
                    [&entry_for_read],
                    |r| {
                        Ok((
                            r.get(0)?,
                            r.get(1)?,
                            r.get(2)?,
                            r.get(3)?,
                            r.get(4)?,
                            r.get(5)?,
                            r.get(6)?,
                        ))
                    },
                )
                .ok())
            })
            .await?;
        let seq2 = seq;
        if let Some((namespace, title, account_id, published_at, body, kind, key)) = doc {
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
            let source = crate::feed::source_url(&namespace, &kind, &key).unwrap_or_default();
            let normalized = normalize_v1(&format!(
                "{title}\n{}\n{source}",
                body.as_deref().unwrap_or("")
            ));
            let (uni, bi) = grams(&normalized);
            let title2 = title.clone();
            let ns2 = namespace.clone();
            let cid = creator_id.clone();
            let pub2 = published_at.clone();
            let entry2 = entry_id.clone();
            let charge = (normalized.len() as i64)
                .saturating_mul(4)
                .saturating_add(1024)
                .saturating_add(128 * (uni.len() + bi.len()) as i64);
            let indexed=db.write(move |tx| {
                let (current,enabled,used,budget):(i64,bool,i64,i64)=tx.query_row("SELECT generation,enabled,estimated_bytes,budget_bytes FROM search_state WHERE id=1",[],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?)))?;
                if current!=epoch||!enabled{return Ok(false);}
                let old:i64=tx.query_row("SELECT COALESCE(sum(cache_bytes),0) FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",[&entry2],|r|r.get(0))?;
                if used.saturating_sub(old).saturating_add(charge)>budget {tx.execute("UPDATE search_state SET budget_blocked=1 WHERE id=1",[])?;return Ok(false);}
                let active:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND lifecycle!='deleted')",[&entry2],|r|r.get(0))?;
                if !active {tx.execute("UPDATE outbox SET processed_at=?2 WHERE seq=?1",rusqlite::params![seq2,crate::jobs::now()])?;return Ok(true);}
                // 删除旧版文档（级联 gram；FTS 由触发器清理）。
                tx.execute(
                    "DELETE FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
                    [&entry2],
                )?;
                tx.execute(
                    "INSERT INTO search_documents (doc_kind, ref_id, entry_id, creator_id, namespace, published_at, title, normalized_text, index_version,cache_bytes)
                     VALUES ('entry', ?1, ?1, ?2, ?3, ?4, ?5, ?6, ?7,?8)",
                    rusqlite::params![entry2, cid, ns2, pub2, title2, normalized, SEARCH_NORMALIZATION_VERSION,charge],
                )?;
                let doc_id: i64 = tx.query_row(
                    "SELECT doc_id FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
                    [&entry2],
                    |r| r.get(0),
                )?;
                for g in uni {
                    tx.execute(
                        "INSERT OR IGNORE INTO search_unigram (gram, doc_id) VALUES (?1, ?2)",
                        rusqlite::params![g, doc_id],
                    )?;
                }
                for g in bi {
                    tx.execute(
                        "INSERT OR IGNORE INTO search_bigram (gram, doc_id) VALUES (?1, ?2)",
                        rusqlite::params![g, doc_id],
                    )?;
                }
                tx.execute(
                    "UPDATE outbox SET processed_at=?2 WHERE seq=?1",
                    rusqlite::params![seq2, trace_domain::time::TraceTime::now(trace_domain::time::TimePrecision::Discovery).rfc3339],
                )?;
                tx.execute(
                    "UPDATE search_state SET indexed_seq=MAX(indexed_seq, ?1)",
                    [seq2],
                )?;
                Ok(true)
            })
            .await?;
            if !indexed {
                break;
            }
        } else {
            db.write(move |tx| {
                tx.execute(
                    "UPDATE outbox SET processed_at=?2 WHERE seq=?1",
                    rusqlite::params![
                        seq2,
                        trace_domain::time::TraceTime::now(
                            trace_domain::time::TimePrecision::Discovery
                        )
                        .rfc3339
                    ],
                )?;
                Ok(())
            })
            .await?;
        }
        processed += 1;
        let _ = topic;
    }
    Ok(processed)
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
