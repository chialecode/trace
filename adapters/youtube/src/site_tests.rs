//! The YouTube adapter against sanitized synthetic pages and answers shaped like the site's.
use crate::site::YouTube;
use serde_json::{Value, json};
use trace_contracts::plugin::EnumerateParams;
use trace_domain::errors::FailureClass;
use trace_site_support::kit::serve::{SiteAdapter, checked_page, resolve_source};
use trace_site_support::kit::testing::Fixtures;

const CHANNEL: &str = "UCtest_channel_00000001a";
const VERSION: &str = "2.20261015.01.00";
const VIDEOS: &str = include_str!("../fixtures/channel_videos.html");
const SHORTS: &str = include_str!("../fixtures/channel_shorts.html");
const POSTS: &str = include_str!("../fixtures/channel_posts.html");
const HOME_ONLY: &str = include_str!("../fixtures/channel_home_only.html");
const NO_SHORTS: &str = include_str!("../fixtures/channel_no_shorts.html");
const EMPTY_VIDEOS: &str = include_str!("../fixtures/channel_empty_videos.html");
const NO_TABS: &str = include_str!("../fixtures/channel_no_tabs.html");
const BROWSE_TAB: &str = include_str!("../fixtures/browse_videos_tab.json");
const BROWSE_2: &str = include_str!("../fixtures/browse_videos_2.json");
const BROWSE_EMPTY: &str = include_str!("../fixtures/browse_empty.json");
const WATCH: &str = include_str!("../fixtures/watch_video.html");
const WATCH_DAY: &str = include_str!("../fixtures/watch_date_only.html");
const WATCH_LIVE: &str = include_str!("../fixtures/watch_live.html");
const WATCH_BOT: &str = include_str!("../fixtures/watch_bot.html");
const WATCH_GONE: &str = include_str!("../fixtures/watch_removed.html");
const POST_A: &str = include_str!("../fixtures/post_a.html");
const POST_C: &str = include_str!("../fixtures/post_c.html");
const POST_A_ID: &str = "UgkxSyntheticPostAAAAAAAAAAAAAAAAAAAA01";
const POST_C_ID: &str = "UgkxSyntheticPostCCCCCCCCCCCCCCCCCCCC03";

fn params(lane: &str, cursor: Option<&str>) -> EnumerateParams {
    EnumerateParams {
        content_type: lane.into(),
        native_account_key: CHANNEL.into(),
        mode: "history".into(),
        cursor: cursor.map(str::to_owned),
        recent_days: None,
        max_pages: 1,
    }
}

fn keys(page: &trace_contracts::plugin::EnumeratedPage) -> Vec<&str> {
    page.items
        .iter()
        .map(|e| e.native_entry_key.as_str())
        .collect()
}

#[test]
fn a_channel_resolves_from_a_handle_an_id_and_a_link_and_the_identity_is_checked() {
    let net = Fixtures::new()
        .on("/@SyntheticChannel?hl=en", VIDEOS)
        .on(&format!("/channel/{CHANNEL}?hl=en"), VIDEOS)
        .on("/channel/UCother_channel_0000000a?hl=en", VIDEOS);
    for input in [
        "@SyntheticChannel",
        "https://www.youtube.com/@SyntheticChannel/videos",
        CHANNEL,
    ] {
        let account = YouTube.resolve_account(&net, input).unwrap();
        assert_eq!(account.native_account_key, CHANNEL, "{input}");
        assert_eq!(account.display_name.as_deref(), Some("合成频道"));
        assert_eq!(
            account.profile_url.as_deref(),
            Some(format!("https://www.youtube.com/channel/{CHANNEL}").as_str())
        );
    }
    // A page that is another channel's than the one asked for is not trusted.
    let err = YouTube
        .resolve_account(&net, "UCother_channel_0000000a")
        .unwrap_err();
    assert_eq!(err.class, FailureClass::SchemaChanged);
    let err = YouTube
        .resolve_account(&net, "https://example.com/@x")
        .unwrap_err();
    assert_eq!(err.class, FailureClass::InvalidPayload);
    assert!(
        net.requests().iter().all(|r| !r.credential),
        "no Cookie is ever asked for"
    );
}

#[test]
fn the_first_videos_page_leaves_out_what_cannot_be_archived_and_pages_on_by_token() {
    let net = Fixtures::new()
        .on(&format!("/channel/{CHANNEL}/videos?hl=en"), VIDEOS)
        .on("\"continuation\":\"CONT_VIDEOS_1\"", BROWSE_2);
    let first = YouTube.enumerate(&net, &params("video", None)).unwrap();
    assert_eq!(keys(&first), ["vid_AAAAAA1", "vid_BBBBBB2", "vid_CCCCCC3"]);
    assert_eq!(
        first.coverage["skipped"],
        json!({"live_or_upcoming": 1, "members_only": 1}),
        "a live stream and a members-only video"
    );
    assert!(first.has_more);
    let cursor = first.next_cursor.clone().unwrap();
    assert_eq!(cursor, format!("yt1:{VERSION}:CONT_VIDEOS_1"));
    let entry = &first.items[0];
    assert_eq!(entry.native_kind, "video");
    assert_eq!(entry.title, "第一个视频");
    assert_eq!(entry.stats["plays"], 1234);
    assert!(
        first.items[1].stats["plays"].is_null(),
        "1.2K is not a count"
    );
    assert_eq!(first.items[2].stats["plays"], 0);
    let hints = entry.hints.as_ref().unwrap();
    assert_eq!((hints.video_count, hints.duration_ms), (1, Some(754_000)));
    assert_eq!(
        hints.cover_url.as_deref(),
        Some("https://i.ytimg.com/vi/vid_AAAAAA1/hqdefault.jpg")
    );
    assert_eq!(
        first.items[1].hints.as_ref().unwrap().duration_ms,
        Some(3_723_000)
    );
    // The list says "3 days ago": an approximate moment with a day of margin, never a source time.
    assert_eq!(
        (
            entry.published_precision.as_str(),
            entry.published_margin_seconds
        ),
        ("approximate", Some(86_400))
    );
    let now = trace_site_support::kit::clock::now_unix();
    assert!(
        (0..=5).any(|late| entry.published_at
            == trace_site_support::kit::clock::from_unix(now - late - 3 * 86_400)),
        "{:?}",
        entry.published_at
    );
    // A Short's list line carries no age at all.
    let shorts = Fixtures::new().on(&format!("/channel/{CHANNEL}/shorts?hl=en"), SHORTS);
    let shorts = YouTube.enumerate(&shorts, &params("short", None)).unwrap();
    assert!(
        shorts
            .items
            .iter()
            .all(|s| s.published_at.is_none() && s.published_precision == "unknown")
    );

    let second = YouTube
        .enumerate(&net, &params("video", Some(&cursor)))
        .unwrap();
    assert_eq!(
        keys(&second),
        ["vid_LOCK_L6"],
        "a playlist among the items is not a video"
    );
    assert_eq!(second.items[0].stats["plays"], 2048);
    assert_eq!(
        second.items[0].hints.as_ref().unwrap().duration_ms,
        Some(428_000)
    );
    assert!(!second.has_more && second.next_cursor.is_none());
    let sent = net.requests();
    let post = &sent[1];
    assert_eq!(post.method, "POST");
    assert!(post.url.ends_with("/youtubei/v1/browse?prettyPrint=false"));
    assert_eq!(post.headers["x-youtube-client-version"], VERSION);
    assert_eq!(post.headers["x-youtube-client-name"], "1");
    assert!(
        post.body
            .as_deref()
            .unwrap()
            .contains(&format!("\"clientVersion\":\"{VERSION}\""))
    );
    assert!(!post.credential);
}

#[test]
fn shorts_come_in_both_renderer_forms() {
    let net = Fixtures::new().on(&format!("/channel/{CHANNEL}/shorts?hl=en"), SHORTS);
    let page = YouTube.enumerate(&net, &params("short", None)).unwrap();
    assert_eq!(keys(&page), ["sht_AAAAAA1", "sht_BBBBBB2", "sht_OLD__C3"]);
    assert!(page.items.iter().all(|e| e.native_kind == "short"));
    assert_eq!(page.items[0].title, "短视频一");
    assert_eq!(page.items[0].stats["plays"], 5000);
    assert_eq!(page.items[2].title, "Old reel");
    assert!(!page.has_more);
    assert_eq!(
        checked_page(page, &params("short", None))
            .unwrap()
            .items
            .len(),
        3
    );
}

#[test]
fn posts_list_text_pictures_polls_and_shared_videos_without_listing_the_shared_video() {
    let net = Fixtures::new().on(&format!("/channel/{CHANNEL}/posts?hl=en"), POSTS);
    let page = YouTube.enumerate(&net, &params("post", None)).unwrap();
    assert_eq!(
        keys(&page),
        [
            POST_A_ID,
            "UgkxSyntheticPostBBBBBBBBBBBBBBBBBBBB02",
            POST_C_ID
        ]
    );
    assert!(
        page.has_more,
        "the continuation token is the post list's, not a video's"
    );
    let a = &page.items[0];
    assert_eq!(a.title, "今天更新了 #合成 内容");
    assert_eq!(a.stats["likes"], 12);
    let hints = a.hints.as_ref().unwrap();
    assert_eq!((hints.image_count, hints.video_count), (1, 0));
    assert_eq!(
        hints.cover_url.as_deref(),
        Some("https://yt3.ggpht.com/img1=s1024")
    );
    assert_eq!(
        (a.published_precision.as_str(), a.published_margin_seconds),
        ("approximate", Some(7 * 86_400)),
        "\"2 weeks ago\" is a week wide"
    );
    let b = &page.items[1];
    assert!(b.summary_body.as_deref().unwrap().contains("投票：红 / 蓝"));
    assert!(b.stats["likes"].is_null());
    assert_eq!(
        page.items[2].hints.as_ref().unwrap().content_kinds,
        ["text", "video"]
    );
}

#[test]
fn a_missing_tab_a_home_page_in_its_place_and_an_empty_list_are_told_apart() {
    // The channel really has no Shorts tab: nothing of that kind, proven by its tab list.
    let net = Fixtures::new().on("/shorts?hl=en", NO_SHORTS);
    let page = YouTube.enumerate(&net, &params("short", None)).unwrap();
    assert!(page.items.is_empty() && page.coverage["tab_absent"] == true);
    assert!(
        checked_page(page, &params("short", None))
            .unwrap()
            .items
            .is_empty()
    );
    // A Videos tab that is there and empty.
    let net = Fixtures::new().on("/videos?hl=en", EMPTY_VIDEOS);
    let page = YouTube.enumerate(&net, &params("video", None)).unwrap();
    assert!(
        checked_page(page, &params("video", None))
            .unwrap()
            .items
            .is_empty()
    );
    // A page without any tab proves nothing: it may be a wall.
    let net = Fixtures::new().on("/videos?hl=en", NO_TABS);
    assert_eq!(
        YouTube
            .enumerate(&net, &params("video", None))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    // The site answers the tab address with the channel home: the tab is asked for by itself.
    let net = Fixtures::new()
        .on("/videos?hl=en", HOME_ONLY)
        .on("\"params\":\"params-videos\"", BROWSE_TAB);
    let page = YouTube.enumerate(&net, &params("video", None)).unwrap();
    assert_eq!(keys(&page), ["vid_AAAAAA1"]);
    let sent = net.requests();
    assert!(
        sent[1]
            .body
            .as_deref()
            .unwrap()
            .contains(&format!("\"browseId\":\"{CHANNEL}\""))
    );
}

#[test]
fn walls_limits_and_changed_shapes_of_a_youtube_page_are_told_apart() {
    let limited = Fixtures::new().status("/videos?hl=en", 429);
    assert_eq!(
        YouTube
            .enumerate(&limited, &params("video", None))
            .unwrap_err()
            .class,
        FailureClass::RateLimited
    );
    let not_a_page = Fixtures::new().on("/videos?hl=en", "<html>no state</html>");
    assert_eq!(
        YouTube
            .enumerate(&not_a_page, &params("video", None))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    let other_channel = Fixtures::new().on(
        "/videos?hl=en",
        VIDEOS.replace(CHANNEL, "UCother_channel_0000000a"),
    );
    assert_eq!(
        YouTube
            .enumerate(&other_channel, &params("video", None))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    let empty_answer = Fixtures::new().on("continuation", BROWSE_EMPTY);
    let cursor = format!("yt1:{VERSION}:CONT_VIDEOS_1");
    assert_eq!(
        YouTube
            .enumerate(&empty_answer, &params("video", Some(&cursor)))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged,
        "an answer without the continuation actions is a changed shape, not the end"
    );
    for cursor in ["", "yt2:1:x", "yt1:x", "yt1:2.2026.1:a b", "yt1::tok"] {
        let err = YouTube
            .enumerate(&Fixtures::new(), &params("video", Some(cursor)))
            .unwrap_err();
        assert_eq!(err.class, FailureClass::SchemaChanged, "{cursor:?}");
    }
    assert_eq!(
        YouTube
            .enumerate(&Fixtures::new(), &params("clip", None))
            .unwrap_err()
            .class,
        FailureClass::Unsupported
    );
    let consent =
        Fixtures::new().redirect("/videos?hl=en", "https://consent.youtube.com/m?continue=x");
    assert_eq!(
        YouTube
            .enumerate(&consent, &params("video", None))
            .unwrap_err()
            .class,
        FailureClass::PermissionDenied
    );
    let sorry = Fixtures::new().redirect(
        "/videos?hl=en",
        "https://www.youtube.com/sorry/index?continue=x",
    );
    assert_eq!(
        YouTube
            .enumerate(&sorry, &params("video", None))
            .unwrap_err()
            .class,
        FailureClass::RateLimited
    );
}

#[test]
fn a_video_is_its_details_with_a_cover_and_a_video_resource_the_media_tool_resolves() {
    let net = Fixtures::new().on("/watch?v=vid_AAAAAA1&hl=en", WATCH);
    let work = YouTube.work(&net, "vid_AAAAAA1", "video").unwrap();
    assert_eq!(work.title, "第一个视频");
    assert_eq!(work.author_key, CHANNEL);
    assert_eq!(work.published_at.as_deref(), Some("2026-09-01T17:00:00Z"));
    assert_eq!(work.precision, "source");
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some("https://youtu.be/vid_AAAAAA1")
    );
    assert_eq!(work.unified.category.as_deref(), Some("Education"));
    assert_eq!(work.unified.visibility.as_deref(), Some("public"));
    assert!(
        work.unified.tags.contains(&"合成".to_owned())
            && work.unified.tags.contains(&"标签一".to_owned())
    );
    assert_eq!(work.stats["plays"], 1234);
    let detail = work.observed();
    assert_eq!(detail.completeness, "full");
    let roles: Vec<_> = detail.resources.iter().map(|r| r.role.as_str()).collect();
    assert_eq!(roles, ["cover", "video", "audio"]);
    // The soundtrack is its own resource of the video's group: the best video comes without it.
    let groups: Vec<_> = detail
        .resources
        .iter()
        .map(|r| (r.role.as_str(), r.group_key.as_deref(), r.required))
        .collect();
    assert_eq!(
        groups,
        [
            ("cover", None, false),
            ("video", Some("media-vid_AAAAAA1"), true),
            ("audio", Some("media-vid_AAAAAA1"), true),
        ]
    );
    assert_eq!(work.notes()["external_media"], "yt_dlp");
    let unified = detail.unified.unwrap();
    assert_eq!(unified.max_duration_ms, Some(754_000));
    assert_eq!(
        unified.content_kinds,
        ["text", "video"],
        "a soundtrack grouped with the video is not a content kind of its own"
    );
    assert_eq!(detail.metadata["caption_tracks"][0]["language"], "zh-Hans");
    // The adapter offers the cover; the video and its sound have no address it could give.
    let candidates = work.candidates(true, false);
    assert_eq!(candidates.len(), 2, "both cover sizes, no video");
    assert!(candidates.iter().all(|c| c.resource.role == "cover"));
    assert!(
        !serde_json::to_string(&detail.document)
            .unwrap()
            .contains("http")
    );
}

#[test]
fn a_shorts_canonical_link_a_day_only_date_and_unlisted_are_kept_as_the_site_gave_them() {
    let net = Fixtures::new().on("/watch?v=vid_BBBBBB2&hl=en", WATCH_DAY);
    let work = YouTube.work(&net, "vid_BBBBBB2", "short").unwrap();
    assert_eq!(work.kind, "short");
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some("https://www.youtube.com/shorts/vid_BBBBBB2")
    );
    assert_eq!(work.published_at.as_deref(), Some("2026-08-30T00:00:00Z"));
    assert_eq!(work.precision, "day");
    assert_eq!(work.unified.visibility.as_deref(), Some("unlisted"));
}

#[test]
fn what_the_watch_page_refuses_is_never_taken_for_a_deleted_video() {
    let class = |page: &str, id: &str| {
        let net = Fixtures::new().on("/watch?v=", page);
        YouTube.work(&net, id, "video").unwrap_err().class
    };
    assert_eq!(class(WATCH_BOT, "vid_CCCCCC3"), FailureClass::RateLimited);
    assert_eq!(
        class(WATCH_GONE, "vid_CCCCCC3"),
        FailureClass::SourceUnavailable
    );
    assert_eq!(class(WATCH_LIVE, "vid_LIVE_L4"), FailureClass::Unsupported);
    // The page of another video than the one asked for.
    assert_eq!(class(WATCH, "vid_BBBBBB2"), FailureClass::SchemaChanged);
    assert_eq!(
        class("<html></html>", "vid_AAAAAA1"),
        FailureClass::SchemaChanged
    );
    assert_eq!(
        YouTube
            .work(&Fixtures::new(), "bad", "video")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn a_post_is_text_pictures_and_the_relation_to_a_shared_video() {
    let net = Fixtures::new()
        .on(&format!("/post/{POST_A_ID}?hl=en"), POST_A)
        .on(&format!("/post/{POST_C_ID}?hl=en"), POST_C);
    let work = YouTube.work(&net, POST_A_ID, "post").unwrap();
    assert_eq!(work.author_key, CHANNEL);
    assert_eq!(work.body, "今天更新了 #合成 内容");
    assert_eq!(work.unified.tags, ["合成"]);
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some(format!("https://www.youtube.com/post/{POST_A_ID}").as_str())
    );
    assert!(work.published_at.is_none() && work.precision == "unknown");
    let candidates = work.candidates(false, false);
    assert_eq!(candidates.len(), 1);
    assert_eq!(candidates[0].resource.role, "image");
    assert_eq!(work.evidence["published_text"], "2 weeks ago");

    let shared = YouTube.work(&net, POST_C_ID, "post").unwrap();
    assert_eq!(shared.unified.related_key.as_deref(), Some("vid_AAAAAA1"));
    assert!(shared.body.contains("https://youtu.be/vid_AAAAAA1"));
    assert!(shared.assets.is_empty());
    // A page that does not hold the post asked for is a changed shape.
    let wrong = Fixtures::new().on("/post/", POST_A);
    assert_eq!(
        YouTube.work(&wrong, POST_C_ID, "post").unwrap_err().class,
        FailureClass::SchemaChanged
    );
}

#[test]
fn links_become_works_and_the_single_link_flow_offers_what_the_adapter_can() {
    let link = YouTube
        .link(
            &Fixtures::new(),
            "https://www.youtube.com/shorts/sht_AAAAAA1",
        )
        .unwrap();
    assert_eq!(
        (link.key.as_str(), link.kind.as_str()),
        ("sht_AAAAAA1", "short")
    );
    let link = YouTube
        .link(&Fixtures::new(), "https://youtu.be/vid_AAAAAA1?si=x")
        .unwrap();
    assert_eq!(
        (link.key.as_str(), link.kind.as_str()),
        ("vid_AAAAAA1", "video")
    );
    assert_eq!(
        YouTube
            .link(&Fixtures::new(), "https://www.youtube.com/@x")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
    let net = Fixtures::new().on("/watch?v=vid_AAAAAA1&hl=en", WATCH);
    let source = resolve_source(&YouTube, &net, "https://youtu.be/vid_AAAAAA1").unwrap();
    assert_eq!(source.kind, "video");
    assert_eq!(source.native_account_key, CHANNEL);
    assert_eq!(source.source_duration_seconds, Some(754.0));
    let detail: Value = serde_json::to_value(source.detail.unwrap()).unwrap();
    assert_eq!(detail["native_kind"], "video");
}

#[test]
fn there_is_no_login_to_prove() {
    let probe = YouTube.probe_auth(&Fixtures::new()).unwrap();
    assert_eq!(probe.state, "unknown");
    assert_eq!(
        probe.capability,
        json!({"login_proven": false, "login_used": false})
    );
}
