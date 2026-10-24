//! The TikTok adapter against sanitized synthetic pages and answers shaped like the site's.
use crate::site::TikTok;
use serde_json::json;
use trace_contracts::plugin::EnumerateParams;
use trace_domain::errors::FailureClass;
use trace_site_support::kit::serve::{SiteAdapter, checked_page, resolve_source};
use trace_site_support::kit::testing::Fixtures;

const SEC: &str = "MS4wLjABAAAAsyntheticSecUid00000000000000000000000000";
const PROFILE: &str = include_str!("../fixtures/profile.html");
const MISSING: &str = include_str!("../fixtures/profile_missing.html");
const PRIVATE: &str = include_str!("../fixtures/profile_private.html");
const LIST_1: &str = include_str!("../fixtures/item_list_1.json");
const LIST_2: &str = include_str!("../fixtures/item_list_2.json");
const UNSIGNED: &str = include_str!("../fixtures/item_list_unsigned.json");
const VIDEO: &str = include_str!("../fixtures/video.html");
const PHOTO: &str = include_str!("../fixtures/photo.html");
const NO_MEDIA: &str = include_str!("../fixtures/video_no_media.html");
const GONE: &str = include_str!("../fixtures/video_missing.html");
const PRIVATE_VIDEO: &str = include_str!("../fixtures/video_private.html");
const HOME_IN: &str = include_str!("../fixtures/home_logged_in.html");
const HOME_ANON: &str = include_str!("../fixtures/home_anonymous.html");
const CHALLENGE: &str = include_str!("../fixtures/challenge.html");
const V1: &str = "7000000000000000001";
const V2: &str = "7000000000000000002";
const P1: &str = "7000000000000000003";

fn params(cursor: Option<&str>) -> EnumerateParams {
    EnumerateParams {
        content_type: "video".into(),
        native_account_key: SEC.into(),
        mode: "history".into(),
        cursor: cursor.map(str::to_owned),
        recent_days: None,
        max_pages: 1,
    }
}

#[test]
fn an_account_resolves_to_its_stable_key_from_a_name_a_link_and_a_short_link() {
    let net = Fixtures::new()
        .on("/@synthetic.creator?lang=en", PROFILE)
        .on("/@someone.else?lang=en", PROFILE)
        .redirect(
            "vm.tiktok.com/ZMabc123",
            "https://www.tiktok.com/@synthetic.creator?_r=1",
        );
    for input in [
        "@synthetic.creator",
        "https://www.tiktok.com/@synthetic.creator",
        "https://vm.tiktok.com/ZMabc123/",
    ] {
        let account = TikTok.resolve_account(&net, input).unwrap();
        assert_eq!(account.native_account_key, SEC, "{input}");
        assert_eq!(account.display_name.as_deref(), Some("合成作者"));
        assert_eq!(
            account.profile_url.as_deref(),
            Some("https://www.tiktok.com/@synthetic.creator")
        );
        assert_eq!(account.evidence["video_count"], 3);
    }
    // The site answering with another account than the one named is not trusted.
    assert_eq!(
        TikTok
            .resolve_account(&net, "@someone.else")
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    assert_eq!(
        TikTok
            .resolve_account(&net, "https://example.com/@x")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn pages_that_say_gone_private_or_challenge_are_not_confused() {
    let class = |page: &str| {
        let net = Fixtures::new().on("/@synthetic.creator", page);
        TikTok
            .resolve_account(&net, "@synthetic.creator")
            .unwrap_err()
            .class
    };
    assert_eq!(class(MISSING), FailureClass::SourceUnavailable);
    assert_eq!(class(PRIVATE), FailureClass::PermissionDenied);
    assert_eq!(class(CHALLENGE), FailureClass::RateLimited);
    assert_eq!(class("<html>nothing</html>"), FailureClass::SchemaChanged);
}

#[test]
fn the_list_maps_videos_and_photo_posts_with_exact_counters_and_pages_by_the_sites_cursor() {
    let net = Fixtures::new()
        .on("cursor=0&", LIST_1)
        .on("cursor=1760000000000", LIST_2);
    let first = TikTok.enumerate(&net, &params(None)).unwrap();
    let keys: Vec<_> = first
        .items
        .iter()
        .map(|e| (e.native_entry_key.as_str(), e.native_kind.as_str()))
        .collect();
    assert_eq!(keys, [(V1, "video"), (P1, "photo"), (V2, "video")]);
    assert!(first.has_more);
    assert_eq!(first.next_cursor.as_deref(), Some("tt1:1760000000000"));
    let video = &first.items[0];
    assert_eq!(video.title, "第一个视频 #合成");
    assert_eq!(video.published_at.as_deref(), Some("2025-10-09T08:53:20Z"));
    assert_eq!(
        video.stats["plays"], 1_234_567,
        "the exact string form wins over the rounded number"
    );
    assert!(video.pinned && !first.items[2].pinned);
    let hints = video.hints.as_ref().unwrap();
    assert_eq!((hints.video_count, hints.duration_ms), (1, Some(31_000)));
    assert_eq!(
        hints.cover_url.as_deref(),
        Some("https://p16.tiktokcdn.com/obj/origin.jpeg?x=1")
    );
    let photo = first.items[1].hints.as_ref().unwrap();
    assert_eq!((photo.image_count, photo.video_count), (2, 0));
    assert_eq!(photo.content_kinds, ["image"]);
    assert_eq!(
        first.items[1].published_at.as_deref(),
        Some("2025-10-08T05:06:40Z")
    );

    let second = TikTok
        .enumerate(&net, &params(first.next_cursor.as_deref()))
        .unwrap();
    assert!(second.items.is_empty() && !second.has_more && second.next_cursor.is_none());
    assert!(checked_page(second, &params(Some("tt1:1760000000000"))).is_ok());
    let sent = net.requests();
    assert!(sent[0].url.contains(&format!("secUid={SEC}")) && sent[0].url.contains("count=30"));
    assert!(
        sent.iter().all(|r| r.credential),
        "the session is the visitor's, whatever it has"
    );
}

#[test]
fn a_list_without_items_is_unknown_never_empty() {
    // The site answers the unsigned call with no items; that must not read as "no posts".
    let net = Fixtures::new().on("item_list", UNSIGNED);
    assert_eq!(
        TikTok.enumerate(&net, &params(None)).unwrap_err().class,
        FailureClass::RateLimited
    );
    let net = Fixtures::new().on("item_list", "");
    assert_eq!(
        TikTok.enumerate(&net, &params(None)).unwrap_err().class,
        FailureClass::RateLimited
    );
    // An explicitly empty first page is still not believed.
    let net = Fixtures::new().on("item_list", LIST_2);
    let page = TikTok.enumerate(&net, &params(None)).unwrap();
    assert_eq!(
        checked_page(page, &params(None)).unwrap_err().class,
        FailureClass::SourceUnavailable
    );
    let net = Fixtures::new().on("item_list", "<html>");
    assert_eq!(
        TikTok.enumerate(&net, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    for cursor in ["", "tt2:1", "tt1:", "tt1:a", "tt1:1 2"] {
        let err = TikTok
            .enumerate(&Fixtures::new(), &params(Some(cursor)))
            .unwrap_err();
        assert_eq!(err.class, FailureClass::SchemaChanged, "{cursor:?}");
    }
    let other = EnumerateParams {
        content_type: "photo".into(),
        ..params(None)
    };
    assert_eq!(
        TikTok
            .enumerate(&Fixtures::new(), &other)
            .unwrap_err()
            .class,
        FailureClass::Unsupported
    );
}

#[test]
fn a_video_has_ranked_renditions_a_cover_and_everything_the_unified_model_asks() {
    let net = Fixtures::new().on(&format!("/@_/video/{V1}?lang=en"), VIDEO);
    let work = TikTok.work(&net, V1, "video").unwrap();
    assert_eq!(work.author_key, SEC);
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some(format!("https://www.tiktok.com/@synthetic.creator/video/{V1}").as_str())
    );
    assert_eq!(
        work.unified.tags,
        ["合成"],
        "the same tag from the markup and the description counts once"
    );
    assert_eq!(work.unified.mentions, ["friend.one"]);
    assert_eq!(work.unified.ip_region.as_deref(), Some("US"));
    assert_eq!(work.unified.language.as_deref(), Some("en"));
    assert_eq!(work.unified.visibility.as_deref(), Some("public"));
    assert_eq!(work.stats["likes"], 5000);
    assert_eq!(work.stats["favorites"], 90);
    let detail = work.observed();
    assert_eq!(detail.completeness, "full");
    let best = work.candidates(false, false);
    let video = best.iter().find(|c| c.resource.role == "video").unwrap();
    assert_eq!(
        video.representation, "normal_1080_0-bytevc1",
        "the most pixels win"
    );
    assert_eq!(video.expected_size, Some(5_200_000));
    let all = work.candidates(true, false);
    assert_eq!(all.iter().filter(|c| c.resource.role == "video").count(), 3);
    assert_eq!(all.iter().filter(|c| c.resource.role == "cover").count(), 3);
    let unified = detail.unified.unwrap();
    assert_eq!(unified.max_height, Some(1920));
    assert_eq!(unified.max_duration_ms, Some(31_000));
    assert_eq!(unified.content_kinds, ["text", "video"]);
}

#[test]
fn a_photo_post_keeps_its_pictures_in_order_and_a_withheld_sound_does_not_fail_it() {
    let net = Fixtures::new().on(&format!("/@_/photo/{P1}?lang=en"), PHOTO);
    let work = TikTok.work(&net, P1, "photo").unwrap();
    assert_eq!(work.kind, "photo");
    let roles: Vec<_> = work.assets.iter().map(|a| (a.role, a.required)).collect();
    assert_eq!(roles, [("image", true), ("image", true), ("audio", false)]);
    assert_eq!(work.observed().completeness, "full");
    assert!(work.unified.canonical_url.unwrap().contains("/photo/"));
    // Asking for a video where the page holds a photo post is not the same work.
    let asked_as_video = Fixtures::new().on("/@_/video/", PHOTO);
    assert_eq!(
        TikTok.work(&asked_as_video, P1, "video").unwrap_err().class,
        FailureClass::SchemaChanged
    );
}

#[test]
fn a_page_without_media_is_a_summary_and_the_site_statuses_are_told_apart() {
    let net = Fixtures::new().on("/video/", NO_MEDIA);
    let work = TikTok.work(&net, V2, "video").unwrap();
    assert_eq!(work.observed().completeness, "summary");
    let class = |page: &str| {
        TikTok
            .work(&Fixtures::new().on("/video/", page), V1, "video")
            .unwrap_err()
            .class
    };
    assert_eq!(class(GONE), FailureClass::SourceUnavailable);
    assert_eq!(class(PRIVATE_VIDEO), FailureClass::PermissionDenied);
    assert_eq!(class(CHALLENGE), FailureClass::RateLimited);
    assert_eq!(
        class(VIDEO.replace(V1, V2).as_str()),
        FailureClass::SchemaChanged
    );
    assert_eq!(
        TikTok
            .work(&Fixtures::new(), "x", "video")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn links_become_works_and_short_links_are_expanded_through_the_site() {
    let net = Fixtures::new()
        .redirect(
            "vt.tiktok.com/ZSabc123",
            &format!("https://www.tiktok.com/@synthetic.creator/video/{V1}?_r=1"),
        )
        .on(&format!("/@_/video/{V1}?lang=en"), VIDEO);
    let link = TikTok
        .link(&net, "https://vt.tiktok.com/ZSabc123/")
        .unwrap();
    assert_eq!((link.key.as_str(), link.kind.as_str()), (V1, "video"));
    let link = TikTok
        .link(&net, &format!("https://www.tiktok.com/@a/photo/{P1}"))
        .unwrap();
    assert_eq!((link.key.as_str(), link.kind.as_str()), (P1, "photo"));
    assert_eq!(
        TikTok
            .link(&net, "https://www.tiktok.com/@a")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
    let source = resolve_source(&TikTok, &net, "https://vt.tiktok.com/ZSabc123/").unwrap();
    assert_eq!(source.kind, "video");
    assert_eq!(source.native_account_key, SEC);
    assert_eq!(source.source_duration_seconds, Some(31.0));
    assert!(
        source
            .media
            .candidates
            .iter()
            .any(|c| c.resource.role == "video")
    );
}

#[test]
fn the_login_is_proven_by_the_current_user_and_its_absence_proves_nothing() {
    let probe = |page: &str| {
        TikTok
            .probe_auth(&Fixtures::new().on("lang=en", page))
            .unwrap()
    };
    let logged_in = probe(HOME_IN);
    assert_eq!(logged_in.state, "valid");
    assert_eq!(logged_in.capability["login_proven"], true);
    let anonymous = probe(HOME_ANON);
    assert_eq!(anonymous.state, "unknown");
    assert_eq!(
        anonymous.capability,
        json!({"login_proven": false, "source": "app-context"})
    );
}
