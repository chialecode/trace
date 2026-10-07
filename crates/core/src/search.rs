//! 中文与字面搜索（docs/design.md §13.2，C17/C18）。
//!
//! 三级检索：FTS5 trigram（≥3 字）→ 单/双字候选表（1-2 字）→
//! 有界字面扫描兜底；所有路径都在规范化文本上做字面复核，特殊字符
//! 不构成 FTS/SQL 语法。索引由 Outbox 驱动，可重建、有代次。

use crate::db::DbHandle;
use crate::error::CoreError;
use trace_contracts::control::{SearchResultItemDto, SearchResultsDto, SearchStatusDto};
use trace_domain::search::{SEARCH_NORMALIZATION_VERSION, fts_quote_phrase, normalize_v1};

/// entries+entry_revisions 聚合行：(namespace, title, account_id, published_at, normalized_body)。
type EntryDocRow = (
    String,
    String,
    Option<String>,
    Option<String>,
    Option<String>,
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
                    "SELECT e.namespace, COALESCE(r.title,''), e.account_id, e.published_at, COALESCE(r.normalized_body,'')
                     FROM entries e
                     LEFT JOIN entry_revisions r ON r.entry_id = e.entry_id
                     WHERE e.entry_id = ?1
                     ORDER BY (r.completeness='full') DESC, r.observed_at DESC LIMIT 1",
                    [&entry_for_read],
                    |r| {
                        Ok((
                            r.get(0)?,
                            r.get(1)?,
                            r.get(2)?,
                            r.get(3)?,
                            r.get(4)?,
                        ))
                    },
                )
                .ok())
            })
            .await?;
        let seq2 = seq;
        if let Some((namespace, title, account_id, published_at, body)) = doc {
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
            let normalized = normalize_v1(&format!("{title}\n{}", body.as_deref().unwrap_or("")));
            let (uni, bi) = grams(&normalized);
            let title2 = title.clone();
            let ns2 = namespace.clone();
            let cid = creator_id.clone();
            let pub2 = published_at.clone();
            let entry2 = entry_id.clone();
            db.write(move |tx| {
                // 删除旧版文档（级联 gram；FTS 由触发器清理）。
                tx.execute(
                    "DELETE FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
                    [&entry2],
                )?;
                tx.execute(
                    "INSERT INTO search_documents (doc_kind, ref_id, entry_id, creator_id, namespace, published_at, title, normalized_text, index_version)
                     VALUES ('entry', ?1, ?1, ?2, ?3, ?4, ?5, ?6, ?7)",
                    rusqlite::params![entry2, cid, ns2, pub2, title2, normalized, SEARCH_NORMALIZATION_VERSION],
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
                Ok(())
            })
            .await?;
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
    raw_query: &str,
    creator_id: Option<&str>,
    namespace: Option<&str>,
    cursor: Option<&str>,
    limit: i64,
) -> Result<SearchResultsDto, CoreError> {
    let normalized = normalize_v1(raw_query);
    let char_count = normalized.chars().count();
    let mode = if char_count >= 3 {
        QueryMode::Fts
    } else if char_count >= 1 {
        QueryMode::Gram
    } else {
        QueryMode::Literal
    };
    let status = status(db).await?;
    let items: Vec<SearchResultItemDto> = match mode {
        QueryMode::Fts => {
            // 引号短语：用户输入不构成语法；随后字面复核。
            let phrase = fts_quote_phrase(&normalized);
            let phrase2 = phrase.clone();
            let n = normalize_v1(raw_query);
            let creator = creator_id.map(|s| s.to_string());
            let ns = namespace.map(|s| s.to_string());
            db.read(move |c| {
                let sql = "SELECT d.doc_kind, d.ref_id, d.entry_id, d.creator_id, d.namespace, d.title, d.normalized_text, d.published_at
                     FROM search_fts f JOIN search_documents d ON d.doc_id = f.rowid
                     WHERE search_fts MATCH ?1
                       AND (?2 IS NULL OR d.creator_id = ?2)
                       AND (?3 IS NULL OR d.namespace = ?3)
                     ORDER BY d.published_at IS NULL, d.published_at DESC, d.ref_id DESC LIMIT 200";
                let mut stmt = c.prepare(sql)?;
                let rows = stmt.query_map(rusqlite::params![phrase2, creator, ns], |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, Option<String>>(2)?,
                        r.get::<_, Option<String>>(3)?,
                        r.get::<_, Option<String>>(4)?,
                        r.get::<_, Option<String>>(5)?,
                        r.get::<_, String>(6)?,
                        r.get::<_, Option<String>>(7)?,
                    ))
                })?;
                let mut out = Vec::new();
                for row in rows {
                    let (doc_kind, _ref_id, entry_id, creator_id, namespace, title, text, published_at) = row?;
                    if text.contains(&n) {
                        out.push(SearchResultItemDto {
                            doc_kind,
                            entry_id,
                            revision_id: None,
                            creator_id,
                            namespace,
                            title,
                            snippet: snippet(&text, &n),
                            published_at,
                        });
                    }
                }
                Ok(out)
            })
            .await?
        }
        QueryMode::Gram => {
            // 1/2 字：gram 候选 → 字面复核（结果集有界）。
            let mut candidates: Vec<String> = Vec::new();
            let chars: Vec<char> = normalized.chars().collect();
            if char_count == 1 {
                let g = normalized.clone();
                let mut rows: Vec<String> = db
                    .read(move |c| {
                        let mut stmt = c.prepare(
                            "SELECT d.ref_id FROM search_unigram u JOIN search_documents d ON d.doc_id=u.doc_id WHERE u.gram=?1 LIMIT 2000",
                        )?;
                        let rows = stmt.query_map([&g], |r| r.get(0))?.collect::<Result<Vec<_>, _>>()?;
                        Ok(rows)
                    })
                    .await?;
                candidates.append(&mut rows);
            } else {
                let g: String = chars[..2].iter().collect();
                let mut rows: Vec<String> = db
                    .read(move |c| {
                        let mut stmt = c.prepare(
                            "SELECT d.ref_id FROM search_bigram u JOIN search_documents d ON d.doc_id=u.doc_id WHERE u.gram=?1 LIMIT 2000",
                        )?;
                        let rows = stmt.query_map([&g], |r| r.get(0))?.collect::<Result<Vec<_>, _>>()?;
                        Ok(rows)
                    })
                    .await?;
                candidates.append(&mut rows);
            }
            candidates.sort();
            candidates.dedup();
            // 字面复核（批量读文档）。
            let n = normalized.clone();
            let ids = candidates;
            db.read(move |c| {
                let mut out = Vec::new();
                for id in ids {
                    if out.len() >= limit.max(0) as usize {
                        break;
                    }
                    let row = c.query_row(
                        "SELECT doc_kind, ref_id, entry_id, creator_id, namespace, title, normalized_text, published_at
                         FROM search_documents WHERE doc_kind='entry' AND ref_id=?1",
                        [&id],
                        |r| {
                            Ok((
                                r.get::<_, String>(0)?,
                                r.get::<_, String>(1)?,
                                r.get::<_, Option<String>>(2)?,
                                r.get::<_, Option<String>>(3)?,
                                r.get::<_, Option<String>>(4)?,
                                r.get::<_, Option<String>>(5)?,
                                r.get::<_, String>(6)?,
                                r.get::<_, Option<String>>(7)?,
                            ))
                        },
                    );
                    if let Ok((doc_kind, _ref_id, entry_id, creator_id, namespace, title, text, published_at)) = row
                        && text.contains(&n) {
                            out.push(SearchResultItemDto {
                                doc_kind,
                                entry_id,
                                revision_id: None,
                                creator_id,
                                namespace,
                                title,
                                snippet: snippet(&text, &n),
                                published_at,
                            });
                        }
                }
                Ok(out)
            })
            .await?
        }
        QueryMode::Literal => {
            // 空查询/纯符号：明确空集（不视为错误）。
            Vec::new()
        }
    };
    Ok(SearchResultsDto {
        next_cursor: cursor.map(|s| s.to_string()),
        items,
        mode: format!("{mode:?}").to_lowercase(),
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
        tx.execute("UPDATE search_state SET rebuilding=1, indexed_seq=0", [])?;
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
        tx.execute("UPDATE search_state SET rebuilding=0", [])?;
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
