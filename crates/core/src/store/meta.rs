//! Unified metadata of an entry: the indexed projection of the latest observation that
//! sorting, filtering and search read, plus the history of observed counters.
//!
//! Counters live outside revisions on purpose: a changed like count is an observation of the
//! same content, never a new version of it.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction, params};
use trace_contracts::entry_meta::{EntryMetaDto, EntryStats, TagCount, TagsArgs};
use trace_contracts::plugin::ResourceIdentityDto;
use trace_contracts::unified::{StatCounters, UnifiedMetadata};

/// What the observed resources say about the entry, whatever the site claims.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct MediaFacts {
    pub images: i64,
    pub videos: i64,
    pub kinds: Vec<String>,
    pub cover: Option<String>,
}

/// Counts and content kinds from the resource list; text counts when the body is not empty.
pub fn media_facts(resources: &[ResourceIdentityDto], has_text: bool) -> MediaFacts {
    let count = |role: &str| resources.iter().filter(|r| r.role == role).count() as i64;
    let mut kinds = Vec::new();
    let mut add = |present: bool, kind: &str| {
        if present {
            kinds.push(kind.to_owned());
        }
    };
    add(has_text, "text");
    add(count("image") > 0, "image");
    add(count("motion") > 0, "live_photo");
    add(count("video") > 0, "video");
    add(count("audio") + count("music") > 0, "audio");
    MediaFacts {
        images: count("image"),
        videos: count("video"),
        kinds,
        cover: resources
            .iter()
            .find(|r| r.role == "cover")
            .map(|r| r.source_attachment_id.clone()),
    }
}

/// The content of an observation that is not a mere summary.
pub struct Observed<'a> {
    pub entry_id: &'a str,
    pub site: &'a str,
    pub revision_id: &'a str,
    pub unified: UnifiedMetadata,
    pub facts: MediaFacts,
    pub now: &'a str,
}

/// Stores the projection of a new or reused revision and the counters seen with it.
pub fn record_observed(tx: &Transaction<'_>, o: &Observed<'_>) -> Result<(), CoreError> {
    let u = &o.unified;
    let mut kinds = o.facts.kinds.clone();
    kinds.extend(u.content_kinds.iter().cloned());
    kinds.sort();
    kinds.dedup();
    tx.execute(
        "INSERT INTO entry_meta(entry_id,site,revision_id,canonical_url,relation,related_key,edited_at,language,client,location_name,ip_region,content_kinds,image_count,video_count,max_duration_ms,max_width,max_height,cover,collection,category,pinned,age_restricted,sponsored,visibility,updated_at)
         VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19,?20,?21,?22,?23,?24,?25)
         ON CONFLICT(entry_id) DO UPDATE SET site=excluded.site,revision_id=excluded.revision_id,canonical_url=excluded.canonical_url,relation=excluded.relation,related_key=excluded.related_key,edited_at=excluded.edited_at,language=excluded.language,client=excluded.client,location_name=excluded.location_name,ip_region=excluded.ip_region,content_kinds=excluded.content_kinds,image_count=excluded.image_count,video_count=excluded.video_count,max_duration_ms=excluded.max_duration_ms,max_width=excluded.max_width,max_height=excluded.max_height,cover=excluded.cover,collection=excluded.collection,category=excluded.category,pinned=MAX(entry_meta.pinned,excluded.pinned),age_restricted=excluded.age_restricted,sponsored=excluded.sponsored,visibility=excluded.visibility,updated_at=excluded.updated_at",
        params![
            o.entry_id,
            o.site,
            o.revision_id,
            u.canonical_url,
            u.relation.as_str(),
            u.related_key,
            u.edited_at,
            u.language,
            u.client,
            u.location_name,
            u.ip_region,
            serde_json::to_string(&kinds)?,
            o.facts.images,
            o.facts.videos,
            u.max_duration_ms,
            u.max_width,
            u.max_height,
            u.cover.clone().or_else(|| o.facts.cover.clone()),
            u.collection,
            u.category,
            u.pinned,
            u.age_restricted,
            u.sponsored,
            u.visibility,
            o.now,
        ],
    )?;
    replace_terms(tx, o.entry_id, "tag", &u.tags)?;
    replace_terms(tx, o.entry_id, "mention", &u.mentions)?;
    apply_stats(tx, o.entry_id, &u.stats, o.now)
}

fn replace_terms(
    tx: &Transaction<'_>,
    entry_id: &str,
    kind: &str,
    values: &[String],
) -> Result<(), CoreError> {
    tx.execute(
        "DELETE FROM entry_terms WHERE entry_id=?1 AND kind=?2",
        params![entry_id, kind],
    )?;
    let mut insert = tx.prepare_cached(
        "INSERT OR IGNORE INTO entry_terms(entry_id,kind,value) VALUES(?1,?2,?3)",
    )?;
    for value in values {
        insert.execute(params![entry_id, kind, value])?;
    }
    Ok(())
}

/// A listing sees pinned state and counters before the detail is observed.
pub fn record_listing(
    tx: &Transaction<'_>,
    entry_id: &str,
    site: &str,
    pinned: bool,
    stats: &StatCounters,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO entry_meta(entry_id,site,pinned,updated_at) VALUES(?1,?2,?3,?4)
         ON CONFLICT(entry_id) DO UPDATE SET pinned=excluded.pinned,updated_at=excluded.updated_at",
        params![entry_id, site, pinned, now],
    )?;
    apply_stats(tx, entry_id, stats, now)
}

fn current_stats(tx: &Connection, entry_id: &str) -> Result<StatCounters, CoreError> {
    let row = tx
        .query_row(
            "SELECT plays,likes,comments,shares,favorites,coins,danmaku FROM entry_meta WHERE entry_id=?1",
            [entry_id],
            |r| {
                Ok(StatCounters {
                    plays: r.get(0)?,
                    likes: r.get(1)?,
                    comments: r.get(2)?,
                    shares: r.get(3)?,
                    favorites: r.get(4)?,
                    coins: r.get(5)?,
                    danmaku: r.get(6)?,
                })
            },
        )
        .optional()?;
    Ok(row.unwrap_or_default())
}

/// Counters the site did not report keep their last observed value. A change is written as
/// a new observation; an unchanged set writes nothing.
pub fn apply_stats(
    tx: &Transaction<'_>,
    entry_id: &str,
    seen: &StatCounters,
    now: &str,
) -> Result<(), CoreError> {
    if seen.is_empty() {
        return Ok(());
    }
    let old = current_stats(tx, entry_id)?;
    let merged = StatCounters {
        plays: seen.plays.or(old.plays),
        likes: seen.likes.or(old.likes),
        comments: seen.comments.or(old.comments),
        shares: seen.shares.or(old.shares),
        favorites: seen.favorites.or(old.favorites),
        coins: seen.coins.or(old.coins),
        danmaku: seen.danmaku.or(old.danmaku),
    };
    if merged == old {
        return Ok(());
    }
    tx.execute(
        "UPDATE entry_meta SET plays=?2,likes=?3,comments=?4,shares=?5,favorites=?6,coins=?7,danmaku=?8,stats_observed_at=?9 WHERE entry_id=?1",
        params![
            entry_id,
            merged.plays,
            merged.likes,
            merged.comments,
            merged.shares,
            merged.favorites,
            merged.coins,
            merged.danmaku,
            now
        ],
    )?;
    tx.execute(
        "INSERT INTO entry_stat_observations(entry_id,observed_at,plays,likes,comments,shares,favorites,coins,danmaku) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9)",
        params![
            entry_id,
            now,
            merged.plays,
            merged.likes,
            merged.comments,
            merged.shares,
            merged.favorites,
            merged.coins,
            merged.danmaku
        ],
    )?;
    Ok(())
}

/// The stored projection of one entry, as the detail view and sidecar files need it.
#[derive(Debug, Clone, PartialEq)]
pub struct Stored {
    pub site: String,
    pub unified: UnifiedMetadata,
    pub images: i64,
    pub videos: i64,
}

pub fn load(c: &Connection, entry_id: &str) -> Result<Option<Stored>, CoreError> {
    let row = c
        .query_row(
            "SELECT site,canonical_url,relation,related_key,edited_at,language,client,location_name,ip_region,content_kinds,image_count,video_count,max_duration_ms,max_width,max_height,cover,collection,category,pinned,age_restricted,sponsored,visibility,plays,likes,comments,shares,favorites,coins,danmaku FROM entry_meta WHERE entry_id=?1",
            [entry_id],
            |r| {
                let kinds: String = r.get(9)?;
                Ok(Stored {
                    site: r.get(0)?,
                    images: r.get(10)?,
                    videos: r.get(11)?,
                    unified: UnifiedMetadata {
                        canonical_url: r.get(1)?,
                        relation: trace_contracts::unified::Relation::parse(&r.get::<_, String>(2)?),
                        related_key: r.get(3)?,
                        edited_at: r.get(4)?,
                        language: r.get(5)?,
                        client: r.get(6)?,
                        location_name: r.get(7)?,
                        ip_region: r.get(8)?,
                        content_kinds: serde_json::from_str(&kinds).unwrap_or_default(),
                        max_duration_ms: r.get(12)?,
                        max_width: r.get(13)?,
                        max_height: r.get(14)?,
                        cover: r.get(15)?,
                        collection: r.get(16)?,
                        category: r.get(17)?,
                        pinned: r.get(18)?,
                        age_restricted: r.get(19)?,
                        sponsored: r.get(20)?,
                        visibility: r.get(21)?,
                        stats: StatCounters {
                            plays: r.get(22)?,
                            likes: r.get(23)?,
                            comments: r.get(24)?,
                            shares: r.get(25)?,
                            favorites: r.get(26)?,
                            coins: r.get(27)?,
                            danmaku: r.get(28)?,
                        },
                        tags: Vec::new(),
                        mentions: Vec::new(),
                    },
                })
            },
        )
        .optional()?;
    let Some(mut stored) = row else {
        return Ok(None);
    };
    let mut terms = c.prepare_cached(
        "SELECT kind,value FROM entry_terms WHERE entry_id=?1 ORDER BY kind,value",
    )?;
    for term in terms.query_map([entry_id], |r| {
        Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
    })? {
        let (kind, value) = term?;
        if kind == "tag" {
            stored.unified.tags.push(value);
        } else {
            stored.unified.mentions.push(value);
        }
    }
    Ok(Some(stored))
}

/// Terms of one kind shown with an entry; the rest stay in the database for filtering.
const TERMS_SHOWN: usize = 32;

/// The stored projection as the interface shows it; `None` before anything was observed.
pub fn summary(c: &Connection, entry_id: &str) -> Result<Option<EntryMetaDto>, CoreError> {
    let Some(stored) = load(c, entry_id)? else {
        return Ok(None);
    };
    let observed_at: Option<String> = c.query_row(
        "SELECT stats_observed_at FROM entry_meta WHERE entry_id=?1",
        [entry_id],
        |r| r.get(0),
    )?;
    let u = stored.unified;
    let (mut tags, mut mentions) = (u.tags, u.mentions);
    tags.truncate(TERMS_SHOWN);
    mentions.truncate(TERMS_SHOWN);
    Ok(Some(EntryMetaDto {
        site: stored.site,
        canonical_url: u.canonical_url,
        relation: u.relation.as_str().to_owned(),
        related_key: u.related_key,
        edited_at: u.edited_at,
        language: u.language,
        client: u.client,
        location_name: u.location_name,
        ip_region: u.ip_region,
        content_kinds: u.content_kinds,
        image_count: stored.images,
        video_count: stored.videos,
        duration_ms: u.max_duration_ms,
        width: u.max_width,
        height: u.max_height,
        collection: u.collection,
        category: u.category,
        visibility: u.visibility,
        pinned: u.pinned,
        age_restricted: u.age_restricted,
        sponsored: u.sponsored,
        tags,
        mentions,
        stats: EntryStats {
            plays: u.stats.plays,
            likes: u.stats.likes,
            comments: u.stats.comments,
            shares: u.stats.shares,
            favorites: u.stats.favorites,
            coins: u.stats.coins,
            danmaku: u.stats.danmaku,
            observed_at,
        },
    }))
}

/// Tags by how many entries carry them, most used first.
pub fn top_tags(c: &Connection, args: &TagsArgs) -> Result<Vec<TagCount>, CoreError> {
    let limit = args.limit.unwrap_or(30).clamp(1, 100);
    let prefix = args
        .prefix
        .as_deref()
        .map(str::trim)
        .filter(|p| !p.is_empty())
        .map(|p| p.trim_start_matches('#').to_owned());
    let mut stmt = c.prepare_cached(
        "SELECT t.value,count(*) AS n FROM entry_terms t JOIN entries e ON e.entry_id=t.entry_id
         WHERE t.kind='tag' AND e.lifecycle!='deleted'
           AND (?1 IS NULL OR e.namespace=?1) AND (?2 IS NULL OR e.account_id=?2)
           AND (?3 IS NULL OR e.account_id IN (SELECT b.account_id FROM account_bindings b WHERE b.creator_id=?3 AND b.state='active'))
           AND (?4 IS NULL OR substr(t.value,1,length(?4))=?4)
         GROUP BY t.value ORDER BY n DESC,t.value LIMIT ?5",
    )?;
    let rows = stmt
        .query_map(
            params![
                args.namespace,
                args.account_id,
                args.creator_id,
                prefix,
                limit
            ],
            |r| {
                Ok(TagCount {
                    tag: r.get(0)?,
                    entries: r.get(1)?,
                })
            },
        )?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Counter history of an entry, oldest first.
pub fn stat_history(
    c: &Connection,
    entry_id: &str,
) -> Result<Vec<(String, StatCounters)>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT observed_at,plays,likes,comments,shares,favorites,coins,danmaku FROM entry_stat_observations WHERE entry_id=?1 ORDER BY sequence",
    )?;
    let rows = stmt
        .query_map([entry_id], |r| {
            Ok((
                r.get(0)?,
                StatCounters {
                    plays: r.get(1)?,
                    likes: r.get(2)?,
                    comments: r.get(3)?,
                    shares: r.get(4)?,
                    favorites: r.get(5)?,
                    coins: r.get(6)?,
                    danmaku: r.get(7)?,
                },
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}
