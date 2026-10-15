//! The Instagram adapter against sanitized synthetic pages, scripts and answers shaped like the
//! site's.
use crate::ops;
use crate::site::Instagram;
use trace_contracts::plugin::{EnumerateParams, EnumeratedPage};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::serve::{SiteAdapter, checked_page, resolve_source};
use trace_site_support::kit::testing::{Fixtures, Recorded};

const HOME: &str = include_str!("../fixtures/home.html");
const HOME_ANON: &str = include_str!("../fixtures/home_anonymous.html");
const HOME_NO_VIEWER: &str = include_str!("../fixtures/home_no_viewer.html");
const HOME_NO_OPS: &str = include_str!("../fixtures/home_no_ops.html");
const PROFILE: &str = include_str!("../fixtures/profile.html");
const PROFILE_MISSING: &str = include_str!("../fixtures/profile_missing.html");
const SCRIPT_A: &str = include_str!("../fixtures/script_a.js");
const SCRIPT_B: &str = include_str!("../fixtures/script_b.js");
const SCRIPT_CHANGED: &str = include_str!("../fixtures/script_changed.js");
const USER: &str = include_str!("../fixtures/profile_query.json");
const USER_EMPTY: &str = include_str!("../fixtures/profile_query_empty.json");
const USER_LEGACY_COUNT: &str = include_str!("../fixtures/profile_query_legacy_count.json");
const USER_OTHER_NAME: &str = include_str!("../fixtures/profile_query_other_name.json");
const USER_OTHER_ID: &str = include_str!("../fixtures/profile_query_other_id.json");
const USER_PRIVATE: &str = include_str!("../fixtures/profile_query_private.json");
const USER_FOLLOWED: &str = include_str!("../fixtures/profile_query_private_followed.json");
const USER_NO_VIEWER: &str = include_str!("../fixtures/profile_query_no_viewer.json");
const USER_NONE: &str = include_str!("../fixtures/profile_query_no_user.json");
const POSTS_1: &str = include_str!("../fixtures/posts_1.json");
const POSTS_2: &str = include_str!("../fixtures/posts_2.json");
const POSTS_EMPTY: &str = include_str!("../fixtures/posts_empty.json");
const POSTS_STUCK: &str = include_str!("../fixtures/posts_stuck.json");
const POSTS_NO_CONNECTION: &str = include_str!("../fixtures/posts_no_connection.json");
const POSTS_NO_CODE: &str = include_str!("../fixtures/posts_no_code.json");
const ERROR_LOGIN: &str = include_str!("../fixtures/error_login.json");
const ERROR_CHALLENGE: &str = include_str!("../fixtures/error_challenge.json");
const ERROR_WAIT: &str = include_str!("../fixtures/error_wait.json");
const ERROR_STALE: &str = include_str!("../fixtures/error_stale.json");
const POST_PHOTO: &str = include_str!("../fixtures/post_photo.html");
const POST_REEL: &str = include_str!("../fixtures/post_reel.html");
const POST_CAROUSEL: &str = include_str!("../fixtures/post_carousel.html");
const POST_BROKEN: &str = include_str!("../fixtures/post_broken.html");
const POST_MISSING: &str = include_str!("../fixtures/post_missing.html");
const POST_NONE: &str = include_str!("../fixtures/post_none.html");
const UID: &str = "1500000000000000001";
const PROFILE_OP: &str = "doc_id=7000000000000002";
const POSTS_OP: &str = "doc_id=7000000000000001";
const MORE_OP: &str = "doc_id=7000000000000003";
const DTSG: &str = "NAdtsg_1:AXsynthetic-token";

/// The site's pages and scripts, which every operation needs first.
fn site() -> Fixtures {
    Fixtures::new()
        .on("GET https://www.instagram.com/\n", HOME)
        .on("GET https://www.instagram.com/synthetic.user/\n", PROFILE)
        .on("/ab/a1.js", SCRIPT_A)
        .on("/ab/b2.js", SCRIPT_B)
}

fn params(cursor: Option<&str>) -> EnumerateParams {
    EnumerateParams {
        content_type: "post".into(),
        native_account_key: UID.into(),
        mode: "history".into(),
        cursor: cursor.map(str::to_owned),
        recent_days: None,
        max_pages: 1,
    }
}

fn keys(page: &EnumeratedPage) -> Vec<&str> {
    page.items
        .iter()
        .map(|e| e.native_entry_key.as_str())
        .collect()
}

fn posted(net: &Fixtures) -> Vec<Recorded> {
    net.requests()
        .into_iter()
        .filter(|r| r.method == "POST")
        .collect()
}

#[test]
fn operation_ids_are_read_once_from_the_pages_scripts_and_the_call_is_the_web_clients() {
    let net = site().on(PROFILE_OP, USER);
    let account = Instagram.resolve_account(&net, "@synthetic.user").unwrap();
    assert_eq!(account.native_account_key, UID);
    assert_eq!(account.display_name.as_deref(), Some("合成用户"));
    assert_eq!(
        account.profile_url.as_deref(),
        Some("https://www.instagram.com/synthetic.user/")
    );
    assert_eq!(account.evidence["media_count"], 6);
    Instagram
        .resolve_account(&net, "https://www.instagram.com/synthetic.user/")
        .unwrap();
    // The first script lists no operation, the second does; nothing is read twice.
    assert_eq!((net.count("/ab/a1.js"), net.count("/ab/b2.js")), (1, 1));
    let calls = posted(&net);
    assert_eq!(calls.len(), 2);
    let call = &calls[0];
    assert_eq!(call.url, "https://www.instagram.com/api/graphql");
    assert!(call.credential, "the session is the visitor's");
    let headers = &call.headers;
    assert_eq!(headers["x-ig-app-id"], "936619743392459");
    assert!(
        headers["x-asbd-id"]
            .as_str()
            .unwrap()
            .bytes()
            .all(|b| b.is_ascii_digit())
    );
    assert_eq!(headers["x-fb-lsd"], "lsd_Token1");
    assert_eq!(
        headers["x-fb-friendly-name"],
        "PolarisProfilePageContentQuery"
    );
    assert_eq!(headers["content-type"], "application/x-www-form-urlencoded");
    let body = call.body.as_deref().unwrap();
    let jazoest = format!("jazoest=2{}", DTSG.bytes().map(u32::from).sum::<u32>());
    for part in [
        PROFILE_OP,
        "fb_dtsg=NAdtsg_1%3AAXsynthetic-token",
        &jazoest,
        "lsd=lsd_Token1",
        "fb_api_req_friendly_name=PolarisProfilePageContentQuery",
        &format!("%22id%22%3A%22{UID}%22"),
    ] {
        assert!(body.contains(part), "{part} in {body}");
    }
    let sent = net.requests();
    assert!(
        sent.iter()
            .filter(|r| r.url.contains("/rsrc.php/"))
            .all(|r| !r.credential)
    );
    assert!(
        sent.iter().all(|r| {
            r.headers.get("cookie").is_none() && r.headers.get("x-csrftoken").is_none()
        })
    );
}

#[test]
fn a_rejected_id_is_read_again_once_and_a_second_rejection_is_a_changed_interface() {
    // The site published a new id: the first call is rejected, the second read finds it.
    let net = Fixtures::new()
        .on("GET https://www.instagram.com/\n", HOME)
        .on("/ab/a1.js", SCRIPT_A)
        .once("/ab/b2.js", SCRIPT_B)
        .on("/ab/b2.js", SCRIPT_CHANGED)
        .on(PROFILE_OP, USER)
        .status(POSTS_OP, 400)
        .on("doc_id=7000000000000009", POSTS_1);
    let page = Instagram.enumerate(&net, &params(None)).unwrap();
    assert!(!page.items.is_empty());
    assert_eq!(net.count("/ab/b2.js"), 2);
    // An id that stays rejected is never an empty list.
    ops::forget();
    let stubborn = site().on(PROFILE_OP, USER).status(POSTS_OP, 400);
    assert_eq!(
        Instagram
            .enumerate(&stubborn, &params(None))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    // The answer may say so itself.
    ops::forget();
    let said = site().on(PROFILE_OP, USER).on(POSTS_OP, ERROR_STALE);
    assert_eq!(
        Instagram.enumerate(&said, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    // Scripts that define no operation are a changed interface too.
    ops::forget();
    let none = Fixtures::new()
        .on("GET https://www.instagram.com/\n", HOME_NO_OPS)
        .on("/ab/a1.js", SCRIPT_A);
    assert_eq!(
        Instagram.enumerate(&none, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
}

#[test]
fn an_account_resolves_to_its_stable_key_and_the_reasons_for_no_account_are_told_apart() {
    let resolve = |answer: &str| {
        let net = site().on(PROFILE_OP, answer);
        Instagram.resolve_account(&net, "synthetic.user")
    };
    assert_eq!(
        resolve(USER_LEGACY_COUNT).unwrap().evidence["media_count"],
        6
    );
    assert!(
        resolve(USER_FOLLOWED).is_ok(),
        "a private account that is followed is shown"
    );
    let class = |r: Result<_, trace_contracts::plugin::PluginError>| {
        r.map(|_: trace_contracts::plugin::ResolvedAccount| ())
            .unwrap_err()
            .class
    };
    assert_eq!(class(resolve(USER_PRIVATE)), FailureClass::PermissionDenied);
    assert_eq!(class(resolve(USER_OTHER_NAME)), FailureClass::SchemaChanged);
    assert_eq!(class(resolve(USER_OTHER_ID)), FailureClass::SchemaChanged);
    assert_eq!(class(resolve(USER_NO_VIEWER)), FailureClass::AuthRequired);
    assert_eq!(class(resolve(USER_NONE)), FailureClass::SourceUnavailable);
    assert_eq!(class(resolve(ERROR_LOGIN)), FailureClass::AuthRequired);
    assert_eq!(class(resolve(ERROR_CHALLENGE)), FailureClass::RateLimited);
    assert_eq!(class(resolve(ERROR_WAIT)), FailureClass::RateLimited);

    let page = |html: &str| {
        let net = Fixtures::new()
            .on("GET https://www.instagram.com/synthetic.user/\n", html)
            .on("/ab/", SCRIPT_B);
        Instagram.resolve_account(&net, "@synthetic.user")
    };
    assert_eq!(
        class(page(PROFILE_MISSING)),
        FailureClass::SourceUnavailable
    );
    // The page the site serves in place of a profile it will not show: the signed-in client's
    // error route, not a changed page, and not a login wall.
    let error_route = HOME.replace(
        "</body>",
        r#"<script type="application/json">{"props":{"page_logging":{"name":"httpErrorPage","params":{}},"failure_reason":null,"restricted_age":null,"page_type":"PROFILE"}}</script></body>"#,
    );
    assert_ne!(error_route, HOME);
    assert_eq!(class(page(&error_route)), FailureClass::SourceUnavailable);
    assert_eq!(class(page(HOME_ANON)), FailureClass::AuthRequired);
    assert_eq!(class(page(HOME_NO_VIEWER)), FailureClass::SchemaChanged);
    assert_eq!(
        class(page(HOME)),
        FailureClass::SchemaChanged,
        "a page without a profile id"
    );
    let walled = Fixtures::new().redirect(
        "GET https://www.instagram.com/synthetic.user/\n",
        "https://www.instagram.com/accounts/login/?next=%2Fsynthetic.user%2F",
    );
    assert_eq!(
        class(Instagram.resolve_account(&walled, "@synthetic.user")),
        FailureClass::AuthRequired
    );
    let challenged = Fixtures::new().redirect(
        "GET https://www.instagram.com/synthetic.user/\n",
        "https://www.instagram.com/challenge/?next=%2F",
    );
    assert_eq!(
        class(Instagram.resolve_account(&challenged, "@synthetic.user")),
        FailureClass::RateLimited
    );
    for input in ["explore", "https://www.instagram.com/p/CxPhoto0001/", ""] {
        assert_eq!(
            class(Instagram.resolve_account(&Fixtures::new(), input)),
            FailureClass::InvalidPayload,
            "{input:?}"
        );
    }
}

#[test]
fn the_profile_feed_lists_posts_reels_and_carousels_and_pages_by_the_sites_cursor() {
    let net = site()
        .on(PROFILE_OP, USER)
        .on(MORE_OP, POSTS_2)
        .on(POSTS_OP, POSTS_1);
    let first = Instagram.enumerate(&net, &params(None)).unwrap();
    assert_eq!(
        keys(&first),
        [
            "CxPinned004",
            "CxPhoto0001",
            "CxReel00002",
            "CxCarou0003",
            "CxNoCapt005",
            "CxBroken006"
        ]
    );
    assert!(first.has_more);
    assert_eq!(
        first.next_cursor.as_deref(),
        Some("ig1:synthetic.user:QVFB_cursor1==")
    );
    assert!(first.items[0].pinned && !first.items[1].pinned);
    let photo = &first.items[1];
    assert_eq!(photo.native_kind, "post");
    assert_eq!(photo.published_at.as_deref(), Some("2025-10-09T08:53:20Z"));
    assert_eq!(photo.title, "A photo #synthetic with @friend.one and more");
    assert_eq!(
        (
            photo.stats["likes"].as_i64(),
            photo.stats["comments"].as_i64()
        ),
        (Some(12), Some(3))
    );
    let hints = photo.hints.as_ref().unwrap();
    assert_eq!((hints.image_count, hints.video_count), (1, 0));
    assert_eq!(hints.content_kinds, ["text", "image"]);
    let reel = first.items[2].hints.as_ref().unwrap();
    assert_eq!((reel.video_count, reel.duration_ms), (1, Some(12_500)));
    assert_eq!(first.items[2].stats["plays"], 4321);
    assert!(reel.cover_url.as_deref().unwrap().starts_with("https://"));
    let carousel = first.items[3].hints.as_ref().unwrap();
    assert_eq!((carousel.image_count, carousel.video_count), (1, 1));
    let bare = &first.items[4];
    assert!(bare.title.is_empty() && bare.summary_body.is_none());
    assert_eq!(bare.hints.as_ref().unwrap().content_kinds, ["image"]);
    assert_eq!(first.coverage["empty_confirmed"], false);

    let second = Instagram
        .enumerate(&net, &params(first.next_cursor.as_deref()))
        .unwrap();
    assert_eq!(keys(&second), ["CxLast00007"]);
    assert!(!second.has_more && second.next_cursor.is_none());
    assert!(checked_page(second, &params(Some("ig1:synthetic.user:QVFB_cursor1=="))).is_ok());
    // The name travels in the cursor: the second page asks the feed, not the profile.
    let calls = posted(&net);
    assert_eq!(calls.len(), 3);
    assert!(calls[1].body.as_deref().unwrap().contains(POSTS_OP));
    let paging = calls[2].body.as_deref().unwrap();
    assert!(paging.contains(MORE_OP), "{paging}");
    assert!(
        paging.contains("fb_api_req_friendly_name=PolarisProfilePostsTabContentQuery_connection")
    );
    assert!(paging.contains("%22after%22%3A%22QVFB_cursor1%3D%3D%22"));
    assert_eq!(
        calls[2].headers["x-fb-friendly-name"],
        "PolarisProfilePostsTabContentQuery_connection"
    );
    assert_eq!(calls[2].url, "https://www.instagram.com/graphql/query");
}

#[test]
fn an_empty_feed_is_empty_only_when_the_profile_says_it_has_no_posts() {
    let page = |profile: &str| {
        let net = site().on(PROFILE_OP, profile).on(POSTS_OP, POSTS_EMPTY);
        Instagram.enumerate(&net, &params(None)).unwrap()
    };
    let confirmed = page(USER_EMPTY);
    assert!(confirmed.items.is_empty() && !confirmed.has_more);
    assert_eq!(confirmed.coverage["empty_confirmed"], true);
    assert!(checked_page(confirmed, &params(None)).is_ok());
    // A profile that has posts and a feed that shows none is not "no posts".
    let unconfirmed = page(USER);
    assert_eq!(unconfirmed.coverage["empty_confirmed"], false);
    assert_eq!(
        checked_page(unconfirmed, &params(None)).unwrap_err().class,
        FailureClass::SourceUnavailable
    );
}

#[test]
fn walls_limits_and_changed_shapes_of_a_feed_are_told_apart() {
    let class = |answer: &str| {
        let net = site().on(PROFILE_OP, USER).on(POSTS_OP, answer);
        Instagram.enumerate(&net, &params(None)).unwrap_err().class
    };
    assert_eq!(class(POSTS_NO_CONNECTION), FailureClass::SchemaChanged);
    assert_eq!(class(POSTS_NO_CODE), FailureClass::SchemaChanged);
    assert_eq!(class(ERROR_LOGIN), FailureClass::AuthRequired);
    assert_eq!(class(ERROR_CHALLENGE), FailureClass::RateLimited);
    assert_eq!(class(ERROR_WAIT), FailureClass::RateLimited);
    assert_eq!(class("<html>not json</html>"), FailureClass::SchemaChanged);
    let status = |code: u16| {
        let net = site().on(PROFILE_OP, USER).status(POSTS_OP, code);
        Instagram.enumerate(&net, &params(None)).unwrap_err().class
    };
    assert_eq!(status(401), FailureClass::AuthRequired);
    assert_eq!(status(429), FailureClass::RateLimited);
    assert_eq!(status(500), FailureClass::TransientNetwork);
    // A cursor that does not move is a stuck list, not an end.
    let stuck = site().on(MORE_OP, POSTS_STUCK);
    assert_eq!(
        Instagram
            .enumerate(&stuck, &params(Some("ig1:synthetic.user:QVFB_cursor1==")))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    for cursor in ["", "ig2:a:b", "ig1:a", "ig1:explore:abc", "ig1:a:b c"] {
        let err = Instagram
            .enumerate(&Fixtures::new(), &params(Some(cursor)))
            .unwrap_err();
        assert_eq!(err.class, FailureClass::SchemaChanged, "{cursor:?}");
    }
    let other = EnumerateParams {
        content_type: "reel".into(),
        ..params(None)
    };
    assert_eq!(
        Instagram
            .enumerate(&Fixtures::new(), &other)
            .unwrap_err()
            .class,
        FailureClass::Unsupported
    );
    let bad = EnumerateParams {
        native_account_key: "abc".into(),
        ..params(None)
    };
    assert_eq!(
        Instagram
            .enumerate(&Fixtures::new(), &bad)
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
    // The profile must be the one asked for.
    let wrong = site().on(PROFILE_OP, USER_OTHER_ID);
    assert_eq!(
        Instagram
            .enumerate(&wrong, &params(None))
            .unwrap_err()
            .class,
        FailureClass::SchemaChanged
    );
    let logged_out = site().on(PROFILE_OP, USER_NO_VIEWER);
    assert_eq!(
        Instagram
            .enumerate(&logged_out, &params(None))
            .unwrap_err()
            .class,
        FailureClass::AuthRequired
    );
}

#[test]
fn a_photo_post_keeps_every_size_and_everything_the_unified_model_asks() {
    let net = Fixtures::new().on("GET https://www.instagram.com/p/CxPhoto0001/\n", POST_PHOTO);
    let work = Instagram.work(&net, "CxPhoto0001", "post").unwrap();
    assert_eq!(work.author_key, UID);
    assert_eq!(work.author_name.as_deref(), Some("合成用户"));
    assert_eq!(work.body, "A photo #synthetic with @friend.one and more");
    assert_eq!(work.unified.tags, ["synthetic"]);
    assert_eq!(work.unified.mentions, ["friend.one"]);
    assert_eq!(
        work.unified.location_name.as_deref(),
        Some("Synthetic City")
    );
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some("https://www.instagram.com/p/CxPhoto0001/")
    );
    assert_eq!(work.stats["likes"], 12);
    let best = work.candidates(false, false);
    assert_eq!(best.len(), 1);
    assert_eq!(best[0].representation, "1080x1080");
    assert_eq!(work.candidates(true, false).len(), 3);
    let detail = work.observed();
    assert_eq!(detail.completeness, "full");
    let unified = detail.unified.unwrap();
    assert_eq!(
        (unified.max_width, unified.max_height),
        (Some(1080), Some(1080))
    );
    assert_eq!(unified.content_kinds, ["text", "image"]);
    assert!(net.requests()[0].credential);
}

#[test]
fn a_reel_and_a_carousel_offer_their_videos_with_covers_and_in_order() {
    let net = Fixtures::new().on("/p/CxReel00002/", POST_REEL);
    let reel = Instagram.work(&net, "CxReel00002", "post").unwrap();
    let roles: Vec<_> = reel.assets.iter().map(|a| a.role).collect();
    assert_eq!(roles, ["video", "cover"]);
    let best = reel.candidates(false, false);
    let video = best.iter().find(|c| c.resource.role == "video").unwrap();
    assert_eq!(
        video.representation, "v101-720x1280",
        "the sites first listing wins a tie"
    );
    assert_eq!(reel.duration_seconds(), Some(12.5));
    assert_eq!(reel.stats["plays"], 4321);
    assert_eq!(reel.metadata["product_type"], "clips");

    let net = Fixtures::new().on("/p/CxCarou0003/", POST_CAROUSEL);
    let carousel = Instagram.work(&net, "CxCarou0003", "post").unwrap();
    let roles: Vec<_> = carousel.assets.iter().map(|a| a.role).collect();
    assert_eq!(roles, ["image", "video", "cover"]);
    assert_eq!(carousel.observed().completeness, "full");
    assert_eq!(carousel.metadata["carousel_size"], 2);
    // A medium without a file makes the observation a summary, never a full record.
    let net = Fixtures::new().on("/p/CxBroken006/", POST_BROKEN);
    let broken = Instagram.work(&net, "CxBroken006", "post").unwrap();
    assert!(broken.assets.is_empty());
    assert_eq!(broken.observed().completeness, "summary");
}

#[test]
fn a_missing_post_a_page_without_data_and_a_post_of_another_code_are_not_the_same_failure() {
    let class = |html: &str, key: &str| {
        let net = Fixtures::new().on("/p/", html);
        Instagram.work(&net, key, "post").unwrap_err().class
    };
    assert_eq!(
        class(POST_MISSING, "CxPhoto0001"),
        FailureClass::SourceUnavailable
    );
    assert_eq!(class(POST_NONE, "CxPhoto0001"), FailureClass::SchemaChanged);
    assert_eq!(class(POST_REEL, "CxPhoto0001"), FailureClass::SchemaChanged);
    assert_eq!(class(HOME_ANON, "CxPhoto0001"), FailureClass::AuthRequired);
    assert_eq!(
        Instagram
            .work(&Fixtures::new(), "ab", "post")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
    assert_eq!(
        Instagram
            .work(&Fixtures::new(), "CxPhoto0001", "reel")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn links_become_works_and_the_single_link_flow_offers_the_media() {
    let net = Fixtures::new().on("/p/CxReel00002/", POST_REEL);
    for (input, key) in [
        (
            "https://www.instagram.com/reel/CxReel00002/?igsh=abc",
            "CxReel00002",
        ),
        ("https://instagram.com/p/CxPhoto0001", "CxPhoto0001"),
        (
            "https://www.instagram.com/someone/p/CxPhoto0001/",
            "CxPhoto0001",
        ),
    ] {
        let link = Instagram.link(&net, input).unwrap();
        assert_eq!(
            (link.key.as_str(), link.kind.as_str()),
            (key, "post"),
            "{input}"
        );
        assert_eq!(
            link.canonical,
            format!("https://www.instagram.com/p/{key}/")
        );
    }
    assert_eq!(
        Instagram
            .link(&net, "https://www.instagram.com/someone/")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
    let source = resolve_source(
        &Instagram,
        &net,
        "https://www.instagram.com/reel/CxReel00002/",
    )
    .unwrap();
    assert_eq!(source.kind, "entry");
    assert_eq!(source.native_account_key, UID);
    assert!(
        source
            .media
            .candidates
            .iter()
            .any(|c| c.resource.role == "video")
    );
}

#[test]
fn the_login_is_proven_by_the_viewer_the_page_names_and_a_silent_page_proves_nothing() {
    let probe = |page: &str| {
        Instagram
            .probe_auth(&Fixtures::new().on("GET https://www.instagram.com/\n", page))
            .unwrap()
    };
    let logged_in = probe(HOME);
    assert_eq!(logged_in.state, "valid");
    assert_eq!(logged_in.capability["login_proven"], true);
    assert_eq!(probe(HOME_ANON).state, "invalid");
    let silent = probe(HOME_NO_VIEWER);
    assert_eq!(silent.state, "unknown");
    assert_eq!(silent.capability["login_proven"], false);
    let walled = Fixtures::new().redirect(
        "GET https://www.instagram.com/\n",
        "https://www.instagram.com/accounts/login/",
    );
    assert_eq!(Instagram.probe_auth(&walled).unwrap().state, "invalid");
    let limited = Fixtures::new().status("GET https://www.instagram.com/\n", 429);
    assert_eq!(
        Instagram.probe_auth(&limited).unwrap_err().class,
        FailureClass::RateLimited
    );
}
