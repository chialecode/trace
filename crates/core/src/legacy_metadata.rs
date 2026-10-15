//! Unified metadata for what Bilibili and Douyin saved before the unified fields existed.
//!
//! Those two adapters keep the site's own record as safe metadata and do not map it. This
//! reads that stored record, never the site, and fills the unified fields it can answer: the
//! public link, duration and size, tags and mentions, region, collection, category, a
//! repost's origin. A field the record does not hold stays empty. The same reading serves a
//! new observation by one of these adapters and the entries observed before schema 7, which
//! a background pass fills in (`backfill`).
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use rusqlite::{Connection, Transaction};
use serde_json::Value;
use trace_contracts::plugin::ResourceIdentityDto;
use trace_contracts::unified::{Relation, StatCounters, UnifiedMetadata};

/// Entries one transaction fills.
const BATCH: i64 = 200;

fn text(value: &Value) -> Option<String> {
    let text = value.as_str()?.trim();
    (!text.is_empty()).then(|| text.to_owned())
}

fn number(value: &Value) -> Option<i64> {
    value
        .as_i64()
        .or_else(|| value.as_str().and_then(|s| s.trim().parse().ok()))
        .filter(|n| *n > 0)
}

/// "IP属地：广东" and its variants reduce to the region.
fn region(label: &Value) -> Option<String> {
    let label = text(label)?;
    let region = label
        .trim_start_matches("IP属地")
        .trim_start_matches([':', '：'])
        .trim();
    (!region.is_empty()).then(|| region.to_owned())
}

fn names(list: &Value, key: &str) -> Vec<String> {
    list.as_array()
        .into_iter()
        .flatten()
        .filter_map(|item| text(&item[key]))
        .collect()
}

/// The public address of a work: a site page followed by the key, when the key is plain.
fn page(prefix: &str, key: &str) -> Option<String> {
    (!key.is_empty()
        && key
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'-')))
    .then(|| format!("{prefix}{key}"))
}

/// Largest width and height among objects that carry them.
fn largest(items: impl Iterator<Item = (Option<i64>, Option<i64>)>) -> (Option<i64>, Option<i64>) {
    items.fold((None, None), |(w, h), (iw, ih)| (w.max(iw), h.max(ih)))
}

fn project_bilibili(kind: &str, key: &str, m: &Value) -> UnifiedMetadata {
    let mut u = UnifiedMetadata::default();
    match kind {
        "video" => {
            u.canonical_url = page("https://www.bilibili.com/video/", key);
            let pages = m["pages"].as_array().cloned().unwrap_or_default();
            let whole = number(&m["duration"]).map(|s| s * 1000);
            let longest = pages
                .iter()
                .filter_map(|p| number(&p["duration"]).map(|s| s * 1000))
                .max();
            u.max_duration_ms = whole.max(longest);
            // A rotated video is stored with its unrotated size.
            let size = |d: &Value| {
                let (w, h) = (number(&d["width"]), number(&d["height"]));
                if number(&d["rotate"]) == Some(1) {
                    (h, w)
                } else {
                    (w, h)
                }
            };
            let (w, h) = largest(
                std::iter::once(size(&m["dimension"]))
                    .chain(pages.iter().map(|p| size(&p["dimension"]))),
            );
            (u.max_width, u.max_height) = (w, h);
            u.category = text(&m["tname"]);
            u.collection = text(&m["ugc_season"]["title"]);
        }
        "article" => {
            u.canonical_url = page("https://www.bilibili.com/read/cv", key);
            u.content_kinds.push("article".into());
            u.category = text(&m["category"]["name"]);
            u.tags = names(&m["tags"], "name");
        }
        "opus" | "dynamic" => {
            u.canonical_url = page(
                if kind == "opus" {
                    "https://www.bilibili.com/opus/"
                } else {
                    "https://t.bilibili.com/"
                },
                key,
            );
            let item = &m["item"];
            let author = match &item["modules"] {
                Value::Array(modules) => modules
                    .iter()
                    .find(|module| module["module_author"].is_object())
                    .map(|module| &module["module_author"]),
                modules => Some(&modules["module_author"]),
            };
            u.ip_region = author.and_then(|a| region(&a["pub_location_text"]));
            if item["type"] == "DYNAMIC_TYPE_FORWARD"
                && let Some(origin) = text(&item["orig"]["id_str"])
            {
                u.relation = Relation::Repost;
                u.related_key = Some(origin);
            }
            let dynamic = &item["modules"]["module_dynamic"];
            let nodes = dynamic["desc"]["rich_text_nodes"].as_array();
            for node in nodes.into_iter().flatten() {
                let word = text(&node["text"]);
                match node["type"].as_str() {
                    Some("RICH_TEXT_NODE_TYPE_TOPIC") => u.tags.extend(word),
                    Some("RICH_TEXT_NODE_TYPE_AT") => u.mentions.extend(word),
                    _ => {}
                }
            }
            u.tags.extend(text(&dynamic["topic"]["name"]));
        }
        "music" => {
            u.canonical_url = page("https://www.bilibili.com/audio/au", key);
            u.content_kinds.push("audio".into());
        }
        _ => {}
    }
    u
}

fn project_douyin(kind: &str, key: &str, m: &Value) -> UnifiedMetadata {
    let mut u = UnifiedMetadata {
        ip_region: region(&m["ip_label"]),
        collection: text(&m["mix_info"]["mix_name"]),
        pinned: m["is_top"] == 1,
        sponsored: m["is_ads"] == true,
        ..Default::default()
    };
    u.location_name = text(&m["poi_info"]["poi_name"]);
    u.category = m["video_tag"]
        .as_array()
        .and_then(|tags| tags.iter().find_map(|t| text(&t["tag_name"])));
    u.tags = names(&m["text_extra"], "hashtag_name");
    u.tags.extend(names(&m["cha_list"], "cha_name"));
    match kind {
        "video" => {
            u.canonical_url = page("https://www.douyin.com/video/", key);
            u.max_duration_ms = number(&m["video"]["duration"]);
            (u.max_width, u.max_height) = largest(std::iter::once((
                number(&m["video"]["width"]),
                number(&m["video"]["height"]),
            )));
        }
        "image" => {
            u.canonical_url = page("https://www.douyin.com/note/", key);
            let images = m["images"]
                .as_array()
                .or_else(|| m["image_post_info"]["images"].as_array());
            (u.max_width, u.max_height) = largest(
                images
                    .into_iter()
                    .flatten()
                    .map(|i| (number(&i["width"]), number(&i["height"]))),
            );
        }
        "article" => u.content_kinds.push("article".into()),
        _ => {}
    }
    u
}

/// The unified fields a stored record answers. `None` for a site whose adapters map their own.
pub(crate) fn project(
    site: &str,
    kind: &str,
    key: &str,
    metadata: &Value,
    stats: &Value,
) -> Option<UnifiedMetadata> {
    let mut unified = match site {
        "bilibili" => project_bilibili(kind, key, metadata),
        "douyin" => project_douyin(kind, key, metadata),
        _ => return None,
    };
    unified.stats = StatCounters::from_value(stats);
    Some(unified.sanitized(site))
}

/// What is needed to fill one entry.
struct Candidate {
    entry: String,
    site: String,
    kind: String,
    key: String,
    revision: String,
    metadata: Value,
    stats: Value,
    body_empty: bool,
}

fn candidates(c: &Connection) -> Result<Vec<Candidate>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT e.entry_id,e.namespace,e.native_kind,e.native_entry_key,r.revision_id,r.metadata_json,r.normalized_body,
                COALESCE((SELECT json_extract(o.evidence,'$.stats') FROM source_observations o WHERE o.entry_id=e.entry_id AND json_extract(o.evidence,'$.stats') NOT IN ('{}','null') ORDER BY o.observation_seq DESC LIMIT 1),'{}')
         FROM entries e
         JOIN entry_revisions r ON r.revision_id=(SELECT revision_id FROM entry_revisions WHERE entry_id=e.entry_id ORDER BY (completeness='full') DESC, observed_at DESC, revision_id DESC LIMIT 1)
         LEFT JOIN entry_meta m ON m.entry_id=e.entry_id
         WHERE (m.entry_id IS NULL OR m.revision_id IS NULL) AND (e.namespace='bilibili' OR e.namespace='douyin')
         ORDER BY e.entry_id LIMIT ?1",
    )?;
    let rows = stmt
        .query_map([BATCH], |r| {
            let metadata: String = r.get(5)?;
            let body: String = r.get(6)?;
            let stats: String = r.get(7)?;
            Ok(Candidate {
                entry: r.get(0)?,
                site: r.get(1)?,
                kind: r.get(2)?,
                key: r.get(3)?,
                revision: r.get(4)?,
                metadata: serde_json::from_str(&metadata).unwrap_or(Value::Null),
                stats: serde_json::from_str(&stats).unwrap_or(Value::Null),
                body_empty: body.trim().is_empty(),
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

fn resources(c: &Transaction<'_>, revision: &str) -> Result<Vec<ResourceIdentityDto>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT role,source_attachment_id,position,title,group_key,required FROM media_items WHERE revision_id=?1 ORDER BY position,media_id",
    )?;
    let rows = stmt
        .query_map([revision], |r| {
            Ok(ResourceIdentityDto {
                role: r.get(0)?,
                source_attachment_id: r.get(1)?,
                position: r.get(2)?,
                title: r.get(3)?,
                group_key: r.get(4)?,
                required: r.get::<_, i64>(5)? != 0,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Fills the entries of one batch that have no unified record yet; returns how many it filled.
fn fill_batch(tx: &Transaction<'_>, now: &str) -> Result<usize, CoreError> {
    let batch = candidates(tx)?;
    for candidate in &batch {
        let Some(unified) = project(
            &candidate.site,
            &candidate.kind,
            &candidate.key,
            &candidate.metadata,
            &candidate.stats,
        ) else {
            continue;
        };
        let facts =
            store::meta::media_facts(&resources(tx, &candidate.revision)?, !candidate.body_empty);
        store::meta::record_observed(
            tx,
            &store::meta::Observed {
                entry_id: &candidate.entry,
                site: &candidate.site,
                revision_id: &candidate.revision,
                unified,
                facts,
                now,
            },
        )?;
    }
    Ok(batch.len())
}

/// Fills in the unified record of every entry observed before schema 7, a bounded batch at a
/// time. Entries without a stored revision are left alone: there is nothing to read. Running
/// it again changes nothing.
pub async fn backfill(core: &Core) -> Result<usize, CoreError> {
    let mut filled = 0;
    loop {
        if core.cancel.is_cancelled() {
            return Ok(filled);
        }
        let done = core
            .db
            .write(|tx| fill_batch(tx, &crate::jobs::now()))
            .await?;
        filled += done;
        if done < BATCH as usize {
            return Ok(filled);
        }
        tokio::task::yield_now().await;
    }
}

#[cfg(test)]
#[path = "legacy_metadata_tests.rs"]
mod tests;
