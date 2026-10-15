//! One work (post, video, note) as a site adapter understands it, and its mapping to the
//! offline detail, the unified metadata and the download candidates Core expects.
//!
//! A site module fills a [`Work`] from its own JSON; everything after that is shared, so the
//! six sites behave alike where it matters: stable resource identities, no URL in the
//! document, the best variant by default and every variant on request.
use crate::{candidate, resource, url_identity};
use serde_json::{Value, json};
use trace_contracts::content::{Block, Document, source_rank};
use trace_contracts::plugin::{MediaCandidate, ObservedDetail, ResourceIdentityDto};
use trace_contracts::unified::{StatCounters, UnifiedMetadata};

/// Most variants one asset may offer; a longer list is a site change, not a choice.
const VARIANTS_MAX: usize = 64;
/// Most assets one work may have (a carousel, an album).
const ASSETS_MAX: usize = 256;

/// One way to fetch an asset: a rendition of a video, a size of a picture.
#[derive(Debug, Clone)]
pub struct Variant {
    /// Names the rendition (`mp4-720p`); stable between observations.
    pub representation: String,
    /// Primary address first, then backups. Addresses are transient and never stored.
    pub urls: Vec<String>,
    /// `muxed`, `audio`, `image` or `cover`.
    pub kind: &'static str,
    /// Claims for ranking: `width`, `height`, `fps`, `bitrate`, `codec`; `rank` is the
    /// site's own order of preference when it gives no measures (higher is better).
    pub spec: Value,
    pub size: Option<i64>,
}

impl Variant {
    pub fn new(kind: &'static str, representation: &str, urls: Vec<String>) -> Self {
        Self {
            representation: representation.into(),
            urls,
            kind,
            spec: Value::Null,
            size: None,
        }
    }
    pub fn spec(mut self, spec: Value) -> Self {
        self.spec = spec;
        self
    }
    pub fn size(mut self, size: Option<i64>) -> Self {
        self.size = size.filter(|n| *n > 0);
        self
    }
    fn dimension(&self, key: &str) -> Option<i64> {
        self.spec[key].as_i64().filter(|n| *n > 0)
    }
}

/// A picture, a video, the motion part of a live photo, a soundtrack or a cover.
#[derive(Debug, Clone)]
pub struct Asset {
    /// `image`, `video`, `motion`, `audio` or `cover`.
    pub role: &'static str,
    /// The site's own id for the asset; empty falls back to the first address, without query.
    pub id: String,
    pub caption: String,
    /// A cover is optional; everything else belongs to the archive of the work.
    pub required: bool,
    /// Whether the offline document shows the asset in place.
    pub inline: bool,
    pub variants: Vec<Variant>,
    pub duration_ms: Option<i64>,
    /// Assets of one group are acquired together (a video and its soundtrack).
    pub group: Option<String>,
}

impl Asset {
    pub fn new(role: &'static str, id: &str, variants: Vec<Variant>) -> Self {
        Self {
            role,
            id: id.into(),
            caption: String::new(),
            required: role != "cover",
            inline: matches!(role, "image" | "video" | "motion"),
            variants,
            duration_ms: None,
            group: None,
        }
    }
    pub fn group(mut self, group: &str) -> Self {
        self.group = Some(group.into());
        self
    }
    pub fn caption(mut self, caption: &str) -> Self {
        self.caption = caption.into();
        self
    }
    pub fn duration(mut self, ms: Option<i64>) -> Self {
        self.duration_ms = ms.filter(|n| *n > 0);
        self
    }
    fn identity(&self) -> Option<String> {
        if !self.id.is_empty() {
            return Some(self.id.chars().take(96).collect());
        }
        self.variants
            .iter()
            .flat_map(|v| v.urls.first())
            .find_map(|u| url_identity(u))
    }
}

/// The site's account of one work.
#[derive(Debug, Clone, Default)]
pub struct Work {
    pub key: String,
    /// The entry's native kind (`post`, `video`, `short`, ...); the lane filter and the
    /// observation both use it.
    pub kind: String,
    pub title: String,
    /// Full text, newline-separated paragraphs.
    pub body: String,
    pub published_at: Option<String>,
    /// `source` when the site gave the time, `unknown` otherwise.
    pub precision: &'static str,
    pub author_key: String,
    pub author_name: Option<String>,
    pub unified: UnifiedMetadata,
    pub stats: Value,
    /// Safe, bounded site extension fields (cleaned again by Core).
    pub metadata: Value,
    /// What the observation was built from: endpoint, structure version.
    pub evidence: Value,
    pub assets: Vec<Asset>,
    /// Reasons the work is not complete (text cut short, media the site hides).
    pub omissions: Vec<&'static str>,
    /// Headers the media hosts expect (referer, user-agent); no authentication data.
    pub media_headers: Value,
    /// The media tool of the host that answers for assets without variants (`yt_dlp`).
    pub external_media: Option<&'static str>,
}

impl Work {
    pub fn new(key: &str, kind: &str) -> Self {
        Self {
            key: key.into(),
            kind: kind.into(),
            precision: "unknown",
            stats: Value::Null,
            metadata: Value::Null,
            evidence: Value::Null,
            media_headers: Value::Null,
            ..Self::default()
        }
    }

    pub fn published(&mut self, at: Option<String>) {
        self.precision = if at.is_some() { "source" } else { "unknown" };
        self.published_at = at;
    }

    fn attachment_id(&self, asset: &Asset, index: usize) -> String {
        let identity = asset
            .identity()
            .unwrap_or_else(|| format!("missing-{index}"));
        format!("{}:{}:{index}:{identity}", self.key, asset.role)
    }

    /// Resource identities in a stable order: pictures and videos by position, then the rest.
    pub fn resources(&self) -> Vec<ResourceIdentityDto> {
        self.assets
            .iter()
            .enumerate()
            .take(ASSETS_MAX)
            .map(|(i, a)| {
                let mut r = resource(&self.attachment_id(a, i), a.role, i as i64, a.required);
                r.title = (!a.caption.is_empty()).then(|| a.caption.clone());
                r.group_key = a.group.clone();
                r
            })
            .collect()
    }

    fn document(&self, resources: &[ResourceIdentityDto]) -> Document {
        let mut blocks: Vec<Block> = self
            .body
            .lines()
            .filter(|l| !l.trim().is_empty())
            .map(|l| Block::Text {
                text: l.trim_end().to_owned(),
                style: "paragraph".into(),
            })
            .collect();
        for (asset, r) in self.assets.iter().zip(resources) {
            if asset.inline {
                blocks.push(Block::Resource {
                    attachment_id: r.source_attachment_id.clone(),
                    caption: asset.caption.clone(),
                });
            }
        }
        Document {
            version: 1,
            blocks,
            omissions: self.omissions.iter().map(|o| (*o).to_owned()).collect(),
        }
    }

    /// Content kinds, measures and the cover, derived from the assets the work really has.
    fn unified(&self, resources: &[ResourceIdentityDto]) -> UnifiedMetadata {
        let mut u = self.unified.clone();
        let has = |role: &str| self.assets.iter().any(|a| a.role == role);
        let mut kinds: Vec<String> = u.content_kinds.clone();
        for (present, kind) in [
            (!self.body.trim().is_empty(), "text"),
            (has("image"), "image"),
            (has("motion"), "live_photo"),
            (has("video"), "video"),
            // A soundtrack grouped with a video is part of the video, not a content kind.
            (
                self.assets
                    .iter()
                    .any(|a| a.role == "audio" && a.group.is_none()),
                "audio",
            ),
        ] {
            if present && !kinds.iter().any(|k| k == kind) {
                kinds.push(kind.into());
            }
        }
        u.content_kinds = kinds;
        let best = |f: &dyn Fn(&Variant) -> Option<i64>| {
            self.assets
                .iter()
                .filter(|a| matches!(a.role, "image" | "video" | "motion"))
                .flat_map(|a| a.variants.iter())
                .filter_map(f)
                .max()
        };
        u.max_width = u.max_width.or_else(|| best(&|v| v.dimension("width")));
        u.max_height = u.max_height.or_else(|| best(&|v| v.dimension("height")));
        u.max_duration_ms = u.max_duration_ms.or_else(|| {
            self.assets
                .iter()
                .filter(|a| matches!(a.role, "video" | "motion"))
                .filter_map(|a| a.duration_ms)
                .max()
        });
        u.cover = u.cover.or_else(|| {
            self.assets
                .iter()
                .zip(resources)
                .find(|(a, _)| a.role == "cover")
                .map(|(_, r)| r.source_attachment_id.clone())
        });
        if u.stats.is_empty() {
            u.stats = StatCounters::from_value(&self.stats);
        }
        u
    }

    /// The detail for `observe` and for a single-link preview.
    pub fn observed(&self) -> ObservedDetail {
        let resources = self.resources();
        let complete = self.omissions.is_empty() && !self.key.is_empty();
        ObservedDetail {
            document: self.document(&resources),
            metadata: self.metadata.clone(),
            native_entry_key: self.key.clone(),
            native_kind: self.kind.clone(),
            title: self.title.clone(),
            normalized_body: self.body.clone(),
            published_at: self.published_at.clone(),
            published_precision: self.precision.into(),
            access_state: "available".into(),
            source_version_evidence: self.evidence.clone(),
            unified: Some(self.unified(&resources)),
            resources,
            completeness: if complete { "full" } else { "summary" }.into(),
            stats: self.stats.clone(),
        }
    }

    /// Download candidates: the best variant of every asset, or all of them (best first)
    /// when the caller offers a choice.
    pub fn candidates(&self, all: bool, prefer_fps: bool) -> Vec<MediaCandidate> {
        let mut out = Vec::new();
        let resources = self.resources();
        for (asset, r) in self.assets.iter().zip(&resources) {
            let mut variants: Vec<&Variant> = asset
                .variants
                .iter()
                .filter(|v| !v.urls.is_empty())
                .take(VARIANTS_MAX)
                .collect();
            variants.sort_by_key(|v| {
                std::cmp::Reverse((
                    source_rank(&v.spec, prefer_fps),
                    v.spec["rank"].as_i64().unwrap_or(0),
                    v.spec["bitrate"].as_i64().unwrap_or(0),
                ))
            });
            let take = if all { variants.len() } else { 1 };
            for variant in variants.into_iter().take(take) {
                let Some(mut c) = candidate(
                    r.clone(),
                    variant.urls.clone(),
                    variant.kind,
                    variant.spec.clone(),
                ) else {
                    continue;
                };
                c.representation = variant.representation.clone();
                c.expected_size = variant.size;
                c.segments[0].expected_size = variant.size;
                c.public_headers = self.media_headers.clone();
                out.push(c);
            }
        }
        out
    }

    /// What the host is told besides the candidates: which tool answers for the rest.
    pub fn notes(&self) -> Value {
        match self.external_media {
            Some(tool) => json!({"external_media": tool}),
            None => json!({}),
        }
    }

    /// Longest video, for the single-link offer.
    pub fn duration_seconds(&self) -> Option<f64> {
        self.assets
            .iter()
            .filter(|a| a.role == "video")
            .filter_map(|a| a.duration_ms)
            .max()
            .map(|ms| ms as f64 / 1000.0)
    }

    /// The link of the work as the site shows it.
    pub fn canonical(&self) -> Option<&str> {
        self.unified.canonical_url.as_deref()
    }
}

/// A claim of width and height as the spec value ranking reads.
pub fn size_spec(width: Option<i64>, height: Option<i64>) -> Value {
    json!({"width": width, "height": height})
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> Work {
        let mut w = Work::new("w1", "post");
        w.body = "第一段\n\n第二段".into();
        w.unified.canonical_url = Some("https://weibo.com/1/w1".into());
        w.stats = json!({"likes": 3, "comments": "2"});
        w.assets = vec![
            Asset::new(
                "image",
                "pic1",
                vec![
                    Variant::new(
                        "image",
                        "large",
                        vec!["https://wx1.sinaimg.cn/large/a.jpg?x=1".into()],
                    )
                    .spec(size_spec(Some(2000), Some(1500))),
                ],
            ),
            Asset::new(
                "video",
                "",
                vec![
                    Variant::new(
                        "muxed",
                        "480p",
                        vec!["https://f.video.weibocdn.com/a.mp4?s=1".into()],
                    )
                    .spec(json!({"width": 640, "height": 480, "bitrate": 800})),
                    Variant::new(
                        "muxed",
                        "1080p",
                        vec!["https://f.video.weibocdn.com/b.mp4?s=2".into()],
                    )
                    .spec(json!({"width": 1920, "height": 1080, "bitrate": 3000}))
                    .size(Some(4096)),
                ],
            )
            .duration(Some(12_000)),
            Asset::new(
                "cover",
                "c1",
                vec![Variant::new(
                    "cover",
                    "cover",
                    vec!["https://wx1.sinaimg.cn/m/c.jpg".into()],
                )],
            ),
        ];
        w
    }

    #[test]
    fn the_detail_keeps_addresses_out_of_the_document_and_the_identities_stable() {
        let w = sample();
        let detail = w.observed();
        let json = serde_json::to_string(&detail.document).unwrap();
        assert!(!json.contains("http"), "{json}");
        assert_eq!(detail.completeness, "full");
        assert_eq!(detail.resources.len(), 3);
        assert!(
            detail.resources[0]
                .source_attachment_id
                .starts_with("w1:image:0:pic1")
        );
        // An asset without an id is identified by its first address without the query, so a
        // rotating signature does not make a new resource.
        let mut again = sample();
        again.assets[1].variants[0].urls[0] = "https://f.video.weibocdn.com/a.mp4?s=other".into();
        again.assets[1].variants[1].urls[0] = "https://f.video.weibocdn.com/b.mp4?s=other".into();
        assert_eq!(again.resources(), w.resources());
        let blocks = &detail.document.blocks;
        assert_eq!(
            blocks.len(),
            4,
            "two paragraphs, a picture and a video; a cover is not inline"
        );
        assert!(!detail.resources[2].required, "a cover is optional");
    }

    #[test]
    fn unified_fields_come_from_the_assets_the_work_really_has() {
        let u = sample().observed().unified.unwrap();
        assert_eq!(
            u.content_kinds,
            ["text", "image", "video"].map(String::from).to_vec()
        );
        assert_eq!((u.max_width, u.max_height), (Some(2000), Some(1500)));
        assert_eq!(u.max_duration_ms, Some(12_000));
        assert!(u.cover.unwrap().contains(":cover:2:c1"));
        assert_eq!(u.stats.likes, Some(3));
        assert_eq!(u.stats.comments, Some(2));
    }

    #[test]
    fn candidates_default_to_the_best_variant_and_list_all_on_request() {
        let w = sample();
        let best = w.candidates(false, false);
        assert_eq!(best.len(), 3);
        let video = best.iter().find(|c| c.resource.role == "video").unwrap();
        assert_eq!(video.representation, "1080p");
        assert_eq!(video.expected_size, Some(4096));
        assert_eq!(video.segments[0].expected_size, Some(4096));
        let all = w.candidates(true, false);
        let videos: Vec<_> = all.iter().filter(|c| c.resource.role == "video").collect();
        assert_eq!(
            videos
                .iter()
                .map(|c| c.representation.as_str())
                .collect::<Vec<_>>(),
            ["1080p", "480p"]
        );
        assert_eq!(w.duration_seconds(), Some(12.0));
    }

    #[test]
    fn omissions_make_the_observation_a_summary_never_a_full_record() {
        let mut w = sample();
        w.omissions.push("longtext_unavailable");
        let detail = w.observed();
        assert_eq!(detail.completeness, "summary");
        assert_eq!(detail.document.omissions, ["longtext_unavailable"]);
    }

    #[test]
    fn a_video_answered_by_a_tool_names_it_and_keeps_its_sound_in_the_same_group() {
        let mut w = Work::new("v1", "video");
        assert_eq!(w.notes(), json!({}));
        w.external_media = Some("yt_dlp");
        w.assets = vec![
            Asset::new("video", "", vec![]).group("media-v1"),
            Asset::new("audio", "", vec![]).group("media-v1"),
        ];
        assert_eq!(w.notes(), json!({"external_media": "yt_dlp"}));
        let detail = w.observed();
        assert_eq!(
            detail
                .resources
                .iter()
                .map(|r| (r.role.as_str(), r.group_key.as_deref()))
                .collect::<Vec<_>>(),
            [("video", Some("media-v1")), ("audio", Some("media-v1"))]
        );
        // The soundtrack is part of the video, not a separate kind of content.
        assert_eq!(detail.unified.unwrap().content_kinds, ["video"]);
        assert!(w.candidates(true, false).is_empty());
    }

    #[test]
    fn an_asset_without_any_address_is_listed_but_offers_nothing() {
        let mut w = sample();
        w.assets.push(Asset::new("image", "empty", vec![]));
        assert_eq!(w.resources().len(), 4);
        assert_eq!(w.candidates(false, false).len(), 3);
    }
}
