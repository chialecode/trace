//! The Xiaohongshu adapter against sanitized synthetic pages shaped like the site's.
use crate::site::Xiaohongshu;
use serde_json::json;
use trace_contracts::plugin::{EnumerateParams, PluginError};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::serve::{SiteAdapter, checked_page, resolve_source};
use trace_site_support::kit::testing::Fixtures;

const PROFILE: &str = include_str!("../fixtures/profile.html");
const PROFILE_EMPTY: &str = include_str!("../fixtures/profile_empty.html");
const PROFILE_FOREIGN: &str = include_str!("../fixtures/profile_foreign.html");
const PROFILE_OUT: &str = include_str!("../fixtures/profile_logged_out.html");
const PROFILE_NO_INFO: &str = include_str!("../fixtures/profile_no_info.html");
const CHALLENGE: &str = include_str!("../fixtures/challenge.html");
const HOME_IN: &str = include_str!("../fixtures/home_logged_in.html");
const HOME_OUT: &str = include_str!("../fixtures/home_logged_out.html");
const HOME_SILENT: &str = include_str!("../fixtures/home_silent.html");
const NOTE_PHOTO: &str = include_str!("../fixtures/note_photo.html");
const NOTE_VIDEO: &str = include_str!("../fixtures/note_video.html");
const NOTE_BROKEN: &str = include_str!("../fixtures/note_broken.html");
const NOTE_HIDDEN: &str = include_str!("../fixtures/note_hidden.html");
const NOTE_NONE: &str = include_str!("../fixtures/note_none.html");
const USER: &str = "5a1b2c3d4e5f60718293a4b5";
const DEVICE: &str = "19f0e1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
const N1: &str = "65f0a1b2c3d4e5f607182931";
const N2: &str = "65f0a1b2c3d4e5f607182932";
const N3: &str = "65f0a1b2c3d4e5f607182933";
const N4: &str = "65f0a1b2c3d4e5f607182934";

fn signed_params(cursor: Option<&str>) -> EnumerateParams {
    EnumerateParams {
        content_type: "note".into(),
        native_account_key: USER.into(),
        mode: "history".into(),
        cursor: cursor.map(str::to_owned),
        recent_days: None,
        max_pages: 1,
    }
}

fn signed_page(notes: serde_json::Value, cursor: &str, has_more: bool) -> serde_json::Value {
    json!({
        "success": true,
        "data": {"notes": notes, "cursor": cursor, "has_more": has_more},
    })
}

fn signed_note(id: &str, kind: &str, title: &str, time: i64) -> serde_json::Value {
    json!({
        "id": id,
        "xsec_token": "SyntheticToken123",
        "note_card": {
            "note_id": id,
            "type": kind,
            "display_title": title,
            "user": {"user_id": USER},
            "interact_info": {"liked_count": "17", "sticky": false},
            "cover": {"url": "https://sns-webpic-qc.xhscdn.com/synthetic-cover.jpg"},
            "time": time,
        },
    })
}

fn params(cursor: Option<&str>) -> EnumerateParams {
    EnumerateParams {
        content_type: "note".into(),
        native_account_key: USER.into(),
        mode: "history".into(),
        cursor: cursor.map(str::to_owned),
        recent_days: None,
        max_pages: 1,
    }
}

fn class<T>(result: Result<T, PluginError>) -> FailureClass {
    result.map(|_| ()).unwrap_err().class
}

fn profile(html: &str) -> Fixtures {
    Fixtures::new().on(&format!("/user/profile/{USER}"), html)
}

#[test]
fn an_account_resolves_from_an_id_a_link_and_a_short_link_and_remembers_the_profile_token() {
    let net = profile(PROFILE).redirect(
        "xhslink.com/a/AbCdEf",
        &format!("https://www.xiaohongshu.com/user/profile/{USER}?xsec_token=PTokenAbcd123&xsec_source=pc_share"),
    );
    for input in [
        USER,
        &format!("https://www.xiaohongshu.com/user/profile/{USER}"),
        "https://xhslink.com/a/AbCdEf",
    ] {
        let account = Xiaohongshu.resolve_account(&net, input).unwrap();
        assert_eq!(account.native_account_key, USER, "{input}");
        assert_eq!(account.display_name.as_deref(), Some("合成作者"));
        assert_eq!(
            account.profile_url.as_deref(),
            Some(format!("https://www.xiaohongshu.com/user/profile/{USER}").as_str())
        );
        assert_eq!(account.evidence["interactions"]["fans"], 20);
    }
    // The token the short link carried opens the profile from then on.
    let asked: Vec<_> = net
        .requests()
        .into_iter()
        .filter(|r| r.url.contains("/user/profile/"))
        .map(|r| r.url)
        .collect();
    assert!(!asked[0].contains("xsec_token"));
    assert!(
        asked
            .last()
            .unwrap()
            .ends_with("?xsec_token=PTokenAbcd123&xsec_source=pc_feed")
    );
    assert!(
        net.requests()
            .iter()
            .filter(|r| r.method == "GET")
            .all(|r| r.credential)
    );
}

#[test]
fn the_reasons_a_profile_does_not_show_are_told_apart() {
    let resolve = |net: &Fixtures| Xiaohongshu.resolve_account(net, USER);
    assert_eq!(
        class(resolve(&profile(PROFILE_OUT))),
        FailureClass::AuthRequired
    );
    assert_eq!(
        class(resolve(&profile(PROFILE_NO_INFO))),
        FailureClass::SchemaChanged
    );
    assert_eq!(
        class(resolve(&profile(CHALLENGE))),
        FailureClass::RateLimited
    );
    assert_eq!(
        class(resolve(&profile("<html>nothing</html>"))),
        FailureClass::SchemaChanged
    );
    let walled = Fixtures::new().redirect(
        "/user/profile/",
        "https://www.xiaohongshu.com/login?redirectPath=%2Fexplore",
    );
    assert_eq!(class(resolve(&walled)), FailureClass::AuthRequired);
    let challenged = Fixtures::new().redirect(
        "/user/profile/",
        "https://www.xiaohongshu.com/website-login/captcha?x=1",
    );
    assert_eq!(class(resolve(&challenged)), FailureClass::RateLimited);
    assert_eq!(
        class(resolve(&Fixtures::new().status("/user/profile/", 404))),
        FailureClass::SourceUnavailable
    );
    for input in [
        "nickname",
        "",
        &format!("https://www.xiaohongshu.com/explore/{N1}"),
    ] {
        assert_eq!(
            class(Xiaohongshu.resolve_account(&Fixtures::new(), input)),
            FailureClass::InvalidPayload,
            "{input:?}"
        );
    }
}

#[test]
fn the_first_screen_lists_the_notes_of_the_profile_and_says_there_is_no_more() {
    let net = profile(PROFILE);
    let page = Xiaohongshu.enumerate(&net, &params(None)).unwrap();
    let keys: Vec<_> = page
        .items
        .iter()
        .map(|e| e.native_entry_key.as_str())
        .collect();
    assert_eq!(
        keys,
        [N4, N1, N2, N3],
        "the notes tab only, not the tabs of other users' notes"
    );
    assert!(!page.has_more && page.next_cursor.is_none());
    assert_eq!(page.coverage["history"], "first_screen");
    assert!(page.items[0].pinned && !page.items[1].pinned);
    let photo = &page.items[1];
    assert_eq!(
        (photo.title.as_str(), photo.native_kind.as_str()),
        ("图文笔记", "note")
    );
    assert_eq!(photo.stats["likes"], 1234);
    let hints = photo.hints.as_ref().unwrap();
    assert_eq!(hints.content_kinds, ["image"]);
    assert!(
        hints
            .cover_url
            .as_deref()
            .unwrap()
            .starts_with("https://sns-webpic-qc.xhscdn.com/")
    );
    assert_eq!(
        page.items[2].hints.as_ref().unwrap().content_kinds,
        ["video"]
    );
    assert!(photo.published_at.is_none() && photo.published_precision == "unknown");
    assert!(checked_page(page, &params(None)).is_ok());
}

#[test]
fn an_empty_screen_is_never_no_notes_and_a_foreign_note_is_an_identity_mismatch() {
    let page = Xiaohongshu
        .enumerate(&profile(PROFILE_EMPTY), &params(None))
        .unwrap();
    assert!(page.items.is_empty());
    assert_eq!(page.coverage["empty_confirmed"], false);
    assert_eq!(
        class(checked_page(page, &params(None))),
        FailureClass::SourceUnavailable
    );
    assert_eq!(
        class(Xiaohongshu.enumerate(&profile(PROFILE_FOREIGN), &params(None))),
        FailureClass::SchemaChanged
    );
    assert_eq!(
        class(Xiaohongshu.enumerate(&profile(PROFILE_OUT), &params(None))),
        FailureClass::AuthRequired
    );
    assert_eq!(
        class(Xiaohongshu.enumerate(&profile(PROFILE), &params(Some("x1:abc")))),
        FailureClass::SchemaChanged
    );
    let other = EnumerateParams {
        content_type: "post".into(),
        ..params(None)
    };
    assert_eq!(
        class(Xiaohongshu.enumerate(&Fixtures::new(), &other)),
        FailureClass::Unsupported
    );
    let bad = EnumerateParams {
        native_account_key: "abc".into(),
        ..params(None)
    };
    assert_eq!(
        class(Xiaohongshu.enumerate(&Fixtures::new(), &bad)),
        FailureClass::InvalidPayload
    );
}

#[test]
fn a_note_is_opened_with_the_token_the_list_gave_for_it() {
    let net = profile(PROFILE).on(&format!("/explore/{N1}"), NOTE_PHOTO);
    Xiaohongshu.enumerate(&net, &params(None)).unwrap();
    Xiaohongshu.work(&net, N1, "note").unwrap();
    let asked = net
        .requests()
        .into_iter()
        .find(|r| r.url.contains("/explore/"))
        .unwrap();
    assert!(
        asked.url.ends_with(&format!(
            "/explore/{N1}?xsec_token=ABtoken_1xxxx&xsec_source=pc_user"
        )),
        "{}",
        asked.url
    );
    assert!(asked.credential);
    // A note this process has not been given a token for is asked for without one.
    let unknown = "65f0a1b2c3d4e5f607182939";
    let bare = Fixtures::new().on("/explore/", NOTE_VIDEO);
    let _ = Xiaohongshu.work(&bare, unknown, "note");
    assert!(
        bare.requests()[0]
            .url
            .ends_with(&format!("/explore/{unknown}"))
    );
}

#[test]
fn a_photo_note_keeps_every_picture_its_live_part_and_everything_the_unified_model_asks() {
    let net = Fixtures::new().on(&format!("/explore/{N1}"), NOTE_PHOTO);
    let work = Xiaohongshu.work(&net, N1, "note").unwrap();
    assert_eq!(work.author_key, USER);
    assert_eq!(work.author_name.as_deref(), Some("合成作者"));
    assert_eq!(work.title, "合成笔记");
    assert_eq!(work.body, "正文 #旅行# #美食#\n第二行");
    assert_eq!(work.unified.tags, ["旅行", "美食"]);
    assert_eq!(work.unified.mentions, ["朋友一"]);
    assert_eq!(work.unified.ip_region.as_deref(), Some("广东"));
    assert_eq!(work.published_at.as_deref(), Some("2025-10-09T08:53:20Z"));
    assert_eq!(
        work.unified.edited_at.as_deref(),
        Some("2025-10-09T09:53:20Z")
    );
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some(format!("https://www.xiaohongshu.com/explore/{N1}").as_str())
    );
    assert_eq!(
        (
            work.stats["likes"].as_i64(),
            work.stats["favorites"].as_i64()
        ),
        (Some(1234), Some(56))
    );
    assert_eq!(
        (
            work.stats["comments"].as_i64(),
            work.stats["shares"].as_i64()
        ),
        (Some(7), Some(8))
    );
    let roles: Vec<_> = work.assets.iter().map(|a| a.role).collect();
    assert_eq!(roles, ["image", "image", "motion"]);
    let best = work.candidates(false, false);
    assert_eq!(
        best.iter()
            .find(|c| c.resource.role == "image")
            .unwrap()
            .representation,
        "default"
    );
    assert_eq!(
        work.candidates(true, false)
            .iter()
            .filter(|c| c.resource.role == "image")
            .count(),
        4
    );
    let detail = work.observed();
    assert_eq!(detail.completeness, "full");
    assert_eq!(
        detail.unified.unwrap().content_kinds,
        ["text", "image", "live_photo"]
    );
}

#[test]
fn a_video_note_ranks_its_streams_and_a_note_without_a_stream_is_a_summary() {
    let net = Fixtures::new().on("/explore/", NOTE_VIDEO);
    let work = Xiaohongshu.work(&net, N2, "note").unwrap();
    assert_eq!(
        work.title, "正文 #旅行# #美食#",
        "a note without a title is named by its first line"
    );
    let best = work.candidates(false, false);
    let video = best.iter().find(|c| c.resource.role == "video").unwrap();
    assert_eq!(
        video.representation, "h264-1080x1920",
        "the higher bitrate wins a tie"
    );
    assert_eq!(video.expected_size, Some(9_000_000));
    assert_eq!(work.duration_seconds(), Some(30.5));
    let roles: Vec<_> = work.assets.iter().map(|a| a.role).collect();
    assert_eq!(roles, ["video", "cover"]);
    assert_eq!(
        work.observed().unified.unwrap().content_kinds,
        ["text", "video"]
    );
    let net = Fixtures::new().on("/explore/", NOTE_BROKEN);
    let broken = Xiaohongshu.work(&net, N3, "note").unwrap();
    assert_eq!(broken.observed().completeness, "summary");
}

#[test]
fn a_hidden_note_a_page_without_a_note_and_a_verification_are_not_the_same_failure() {
    let ask =
        |html: &str| class(Xiaohongshu.work(&Fixtures::new().on("/explore/", html), N1, "note"));
    assert_eq!(ask(NOTE_HIDDEN), FailureClass::PermissionDenied);
    assert_eq!(ask(NOTE_NONE), FailureClass::SchemaChanged);
    assert_eq!(ask(CHALLENGE), FailureClass::RateLimited);
    assert_eq!(
        ask(NOTE_VIDEO),
        FailureClass::PermissionDenied,
        "the page holds another note"
    );
    let walled = Fixtures::new().redirect("/explore/", "https://www.xiaohongshu.com/login");
    assert_eq!(
        class(Xiaohongshu.work(&walled, N1, "note")),
        FailureClass::AuthRequired
    );
    assert_eq!(
        class(Xiaohongshu.work(&Fixtures::new().status("/explore/", 404), N1, "note")),
        FailureClass::SourceUnavailable
    );
    assert_eq!(
        class(Xiaohongshu.work(&Fixtures::new(), "abc", "note")),
        FailureClass::InvalidPayload
    );
    assert_eq!(
        class(Xiaohongshu.work(&Fixtures::new(), N1, "post")),
        FailureClass::InvalidPayload
    );
}

#[test]
fn links_become_works_and_the_token_of_a_share_link_is_kept_for_the_note() {
    let net = Fixtures::new()
        .redirect(
            "xhslink.com/a/NoteLink1",
            &format!("https://www.xiaohongshu.com/explore/{N1}?xsec_token=NTokenAbcd123&xsec_source=pc_share"),
        )
        .on(&format!("/explore/{N1}"), NOTE_PHOTO);
    let link = Xiaohongshu
        .link(&net, "https://xhslink.com/a/NoteLink1")
        .unwrap();
    assert_eq!((link.key.as_str(), link.kind.as_str()), (N1, "note"));
    assert_eq!(
        link.canonical,
        format!("https://www.xiaohongshu.com/explore/{N1}")
    );
    let link = Xiaohongshu
        .link(
            &net,
            &format!("https://www.xiaohongshu.com/discovery/item/{N2}"),
        )
        .unwrap();
    assert_eq!(link.key, N2);
    assert_eq!(
        class(Xiaohongshu.link(
            &net,
            &format!("https://www.xiaohongshu.com/user/profile/{USER}")
        )),
        FailureClass::InvalidPayload
    );
    let source = resolve_source(&Xiaohongshu, &net, "https://xhslink.com/a/NoteLink1").unwrap();
    assert_eq!(source.kind, "entry");
    assert_eq!(source.native_account_key, USER);
    assert!(
        source
            .media
            .candidates
            .iter()
            .any(|c| c.resource.role == "image")
    );
    let opened = net
        .requests()
        .into_iter()
        .rfind(|r| r.url.contains("/explore/"))
        .unwrap();
    assert!(opened.url.contains("xsec_token=NTokenAbcd123"));
}

#[test]
fn the_login_is_proven_by_the_page_state_and_a_silent_page_proves_nothing() {
    let probe = |html: &str| {
        Xiaohongshu
            .probe_auth(&Fixtures::new().on("/explore", html))
            .unwrap()
    };
    let logged_in = probe(HOME_IN);
    assert_eq!(logged_in.state, "valid");
    assert_eq!(logged_in.capability["login_proven"], true);
    assert_eq!(probe(HOME_OUT).state, "invalid");
    let silent = probe(HOME_SILENT);
    assert_eq!(silent.state, "unknown");
    assert_eq!(silent.capability["login_proven"], false);
    let unreadable = probe("<html><title>some page</title>no data</html>");
    assert_eq!(
        unreadable.state, "unknown",
        "a page without state is not a verdict"
    );
    let detail = unreadable.capability["detail"].as_str().unwrap();
    assert!(
        detail.contains("state named false") && detail.contains("some page"),
        "{detail}"
    );
    let walled = Fixtures::new().redirect("/explore", "https://www.xiaohongshu.com/login");
    assert_eq!(Xiaohongshu.probe_auth(&walled).unwrap().state, "invalid");
    assert_eq!(
        class(Xiaohongshu.probe_auth(&Fixtures::new().status("/explore", 429))),
        FailureClass::RateLimited
    );
}

#[test]
fn the_signed_list_continues_the_profile_and_carries_the_device_signature() {
    let net = Fixtures::new().json(
        "https://edith.xiaohongshu.com/api/sns/web/v1/user_posted?",
        &signed_page(
            json!([
                signed_note(N1, "normal", "图文笔记", 1_761_300_000_000i64),
                signed_note(N2, "video", "视频笔记", 1_761_200_000_000i64),
            ]),
            "next-cursor",
            true,
        ),
    );
    let page = Xiaohongshu
        .enumerate_with_context(&net, &signed_params(None), &json!({"device_id": DEVICE}))
        .unwrap();
    assert_eq!(page.items.len(), 2);
    assert_eq!(page.items[0].native_entry_key, N1);
    assert_eq!(
        page.items[1].hints.as_ref().unwrap().content_kinds,
        ["video"]
    );
    assert_eq!(page.items[0].published_precision, "exact");
    assert!(page.items[0].published_at.is_some());
    assert!(page.has_more);
    assert_eq!(page.next_cursor.as_deref(), Some("next-cursor"));
    assert_eq!(page.coverage["history"], "signed_list");
    let request = net
        .requests()
        .into_iter()
        .find(|r| r.url.contains("user_posted"))
        .unwrap();
    assert!(request.credential, "the host attaches the login Cookie");
    assert_eq!(
        request.url,
        "https://edith.xiaohongshu.com/api/sns/web/v1/user_posted?cursor=&image_formats=jpg,webp,avif&num=30&user_id=5a1b2c3d4e5f60718293a4b5"
    );
    for header in ["x-s", "x-t", "x-s-common", "x-b3-traceid", "x-xray-traceid"] {
        assert!(request.headers.get(header).is_some(), "{header}");
    }
    assert!(request.headers["x-s"].as_str().unwrap().starts_with("XYS_"));
    assert!(
        request.headers.get("cookie").is_none(),
        "the adapter never sees or sends a Cookie"
    );
}

#[test]
fn a_refused_signature_stops_the_history_instead_of_being_retried() {
    for status in [406, 461] {
        let net = Fixtures::new().http_status("user_posted", status);
        assert_eq!(
            class(Xiaohongshu.enumerate_with_context(
                &net,
                &signed_params(None),
                &json!({"device_id": DEVICE}),
            )),
            FailureClass::PermissionDenied,
            "{status}"
        );
        assert_eq!(
            net.count("user_posted"),
            1,
            "no retry with other parameters"
        );
    }
    for (code, expected) in [
        (406, FailureClass::PermissionDenied),
        (461, FailureClass::PermissionDenied),
        (429, FailureClass::RateLimited),
    ] {
        let net = Fixtures::new().json("user_posted", &json!({"success": false, "code": code}));
        assert_eq!(
            class(Xiaohongshu.enumerate_with_context(
                &net,
                &signed_params(None),
                &json!({"device_id": DEVICE}),
            )),
            expected,
            "business code {code}"
        );
        assert_eq!(net.count("user_posted"), 1);
    }
    let empty = Fixtures::new().json("user_posted", &signed_page(json!([]), "", false));
    let page = Xiaohongshu
        .enumerate_with_context(&empty, &signed_params(None), &json!({"device_id": DEVICE}))
        .unwrap();
    assert!(page.items.is_empty() && !page.has_more);
    assert_eq!(
        page.coverage["empty_confirmed"], false,
        "an empty answer is not proof that the account has no notes"
    );
    let foreign = Fixtures::new().json(
        "user_posted",
        &signed_page(
            json!([{
                "id": N3,
                "note_card": {
                    "note_id": N3,
                    "user": {"user_id": "someone-else"},
                },
            }]),
            "",
            false,
        ),
    );
    assert_eq!(
        class(Xiaohongshu.enumerate_with_context(
            &foreign,
            &signed_params(None),
            &json!({"device_id": DEVICE}),
        )),
        FailureClass::SchemaChanged
    );
}

#[test]
fn without_the_device_identifier_the_first_screen_is_used_and_no_signed_request_is_made() {
    let net = profile(PROFILE);
    let page = Xiaohongshu.enumerate(&net, &params(None)).unwrap();
    assert_eq!(page.coverage["history"], "first_screen");
    assert_eq!(net.count("user_posted"), 0);
    let cursorless =
        Xiaohongshu.enumerate_with_context(&net, &params(Some("cursor")), &serde_json::Value::Null);
    assert_eq!(class(cursorless), FailureClass::SchemaChanged);
}
