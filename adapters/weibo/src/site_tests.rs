//! The Weibo adapter against sanitized synthetic answers shaped like the site's.
use crate::link::mid_to_mblogid;
use crate::site::Weibo;
use serde_json::{Value, json};
use trace_contracts::plugin::EnumerateParams;
use trace_domain::errors::FailureClass;
use trace_site_support::kit::serve::{SiteAdapter, checked_page, resolve_source};
use trace_site_support::kit::testing::Fixtures;

const PROFILE: &str = include_str!("../fixtures/profile_info.json");
const PAGE1: &str = include_str!("../fixtures/mymblog_page1.json");
const PAGE2: &str = include_str!("../fixtures/mymblog_page2_empty.json");
const SHOW_LONG: &str = include_str!("../fixtures/show_long.json");
const LONGTEXT: &str = include_str!("../fixtures/longtext.json");

fn page1() -> Value {
    serde_json::from_str(PAGE1).unwrap()
}

/// The answer of `statuses/show` for the list status with this id.
fn show(id: &str) -> String {
    let list = page1();
    let status = list["data"]["list"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["idstr"] == id)
        .unwrap()
        .clone();
    let mut v = status;
    v["ok"] = json!(1);
    v.to_string()
}

fn params(cursor: Option<&str>) -> EnumerateParams {
    EnumerateParams {
        content_type: "post".into(),
        native_account_key: "1234567890".into(),
        mode: "history".into(),
        cursor: cursor.map(str::to_owned),
        recent_days: None,
        max_pages: 1,
    }
}

fn list_fixtures() -> Fixtures {
    Fixtures::new()
        .on("mymblog?uid=1234567890&page=1&feature=0", PAGE1)
        .on(
            "mymblog?uid=1234567890&page=2&feature=0&since_id=5000001",
            PAGE2,
        )
}

#[test]
fn an_account_resolves_from_its_id_its_domain_and_a_short_link() {
    let net = Fixtures::new()
        .on("profile/info?uid=1234567890", PROFILE)
        .on("profile/info?custom=mycustom", PROFILE)
        .on("profile/info?uid=1111111111", PROFILE)
        .redirect("t.cn/AbCdEf", "https://weibo.com/u/1234567890");
    let account = Weibo
        .resolve_account(&net, "https://weibo.com/u/1234567890")
        .unwrap();
    assert_eq!(account.native_account_key, "1234567890");
    assert_eq!(account.display_name.as_deref(), Some("合成创作者"));
    assert_eq!(
        account.profile_url.as_deref(),
        Some("https://weibo.com/u/1234567890")
    );
    assert_eq!(account.evidence["statuses_count"], 5);
    let by_domain = Weibo
        .resolve_account(&net, "https://weibo.com/mycustom")
        .unwrap();
    assert_eq!(by_domain.native_account_key, "1234567890");
    let by_short = Weibo.resolve_account(&net, "https://t.cn/AbCdEf").unwrap();
    assert_eq!(by_short.native_account_key, "1234567890");
    // The site answering with another account than the one named is not trusted.
    let err = Weibo
        .resolve_account(&net, "https://weibo.com/u/1111111111")
        .unwrap_err();
    assert_eq!(err.class, FailureClass::SchemaChanged);
    let err = Weibo
        .resolve_account(&net, "https://example.com/u/1234567890")
        .unwrap_err();
    assert_eq!(err.class, FailureClass::InvalidPayload);
}

#[test]
fn the_first_page_lists_every_status_with_what_the_preview_shows() {
    let net = list_fixtures();
    let page = Weibo.enumerate(&net, &params(None)).unwrap();
    let keys: Vec<_> = page
        .items
        .iter()
        .map(|e| e.native_entry_key.as_str())
        .collect();
    assert_eq!(
        keys,
        [
            "4906000000000005",
            "4906000000000004",
            "4906000000000003",
            "4906000000000002",
            "4906000000000001"
        ]
    );
    assert!(page.has_more);
    assert_eq!(page.next_cursor.as_deref(), Some("wb1:2:5000001"));
    let pinned = &page.items[0];
    assert!(pinned.pinned && pinned.native_kind == "post");
    assert_eq!(pinned.published_at.as_deref(), Some("2026-10-10T04:00:00Z"));
    assert_eq!(pinned.title, "置顶：欢迎 @朋友");
    let images = &page.items[1];
    let hints = images.hints.as_ref().unwrap();
    assert_eq!((hints.image_count, hints.video_count), (2, 0));
    assert_eq!(hints.content_kinds, ["text", "image", "live_photo"]);
    assert_eq!(
        hints.cover_url.as_deref(),
        Some("https://wx1.sinaimg.cn/thumbnail/pica0001.jpg")
    );
    let video = page.items[2].hints.as_ref().unwrap();
    assert_eq!((video.video_count, video.duration_ms), (1, Some(30_500)));
    assert_eq!(page.items[2].stats["shares"], 4);
    assert_eq!(page.coverage["total"], 5);
}

#[test]
fn the_next_page_carries_the_site_token_and_an_empty_page_ends_the_list() {
    let net = list_fixtures();
    let first = Weibo.enumerate(&net, &params(None)).unwrap();
    let second = Weibo
        .enumerate(&net, &params(first.next_cursor.as_deref()))
        .unwrap();
    assert!(second.items.is_empty() && !second.has_more && second.next_cursor.is_none());
    let sent = net.requests();
    assert!(sent[1].url.contains("page=2") && sent[1].url.contains("since_id=5000001"));
    // The end of a list that was being read is ordinary.
    assert!(checked_page(second, &params(Some("wb1:2:5000001"))).is_ok());
}

#[test]
fn an_empty_start_is_believed_only_when_the_account_reports_no_statuses() {
    let empty_but_counted =
        Fixtures::new().on("mymblog", r#"{"ok":1,"data":{"list":[],"total":7}}"#);
    let page = Weibo.enumerate(&empty_but_counted, &params(None)).unwrap();
    assert_eq!(
        checked_page(page, &params(None)).unwrap_err().class,
        FailureClass::SourceUnavailable,
        "a visitor wall looks like this"
    );
    let truly_empty = Fixtures::new().on("mymblog", r#"{"ok":1,"data":{"list":[],"total":0}}"#);
    let page = Weibo.enumerate(&truly_empty, &params(None)).unwrap();
    assert!(checked_page(page, &params(None)).unwrap().items.is_empty());
}

#[test]
fn walls_limits_and_changed_shapes_of_a_weibo_response_are_told_apart() {
    let wall = Fixtures::new().on(
        "mymblog",
        r#"{"ok":-100,"url":"https://passport.weibo.com/x"}"#,
    );
    assert_eq!(
        Weibo.enumerate(&wall, &params(None)).unwrap_err().class,
        FailureClass::AuthRequired
    );
    let limited = Fixtures::new().status("mymblog", 429);
    assert_eq!(
        Weibo.enumerate(&limited, &params(None)).unwrap_err().class,
        FailureClass::RateLimited
    );
    let changed = Fixtures::new().on("mymblog", r#"{"ok":1,"data":{"items":[]}}"#);
    assert_eq!(
        Weibo.enumerate(&changed, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    let no_ids = Fixtures::new().on("mymblog", r#"{"ok":1,"data":{"list":[{"text":"x"}]}}"#);
    assert_eq!(
        Weibo.enumerate(&no_ids, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    for cursor in ["", "wb2:1:x", "wb1:0:x", "wb1:2:a b", "wb1:x:y"] {
        let err = Weibo
            .enumerate(&Fixtures::new(), &params(Some(cursor)))
            .unwrap_err();
        assert_eq!(err.class, FailureClass::SchemaChanged, "{cursor:?}");
    }
    let other_lane = EnumerateParams {
        content_type: "video".into(),
        ..params(None)
    };
    assert_eq!(
        Weibo
            .enumerate(&Fixtures::new(), &other_lane)
            .unwrap_err()
            .class,
        FailureClass::Unsupported
    );
}

#[test]
fn a_video_post_is_observed_with_its_renditions_and_unified_fields() {
    let net = Fixtures::new().on(
        "statuses/show?id=4906000000000003",
        show("4906000000000003"),
    );
    let work = Weibo.work(&net, "4906000000000003", "post").unwrap();
    let detail = work.observed();
    assert_eq!(detail.completeness, "full");
    assert_eq!(detail.native_entry_key, "4906000000000003");
    let roles: Vec<_> = detail.resources.iter().map(|r| r.role.as_str()).collect();
    assert_eq!(roles, ["video", "cover"]);
    let u = detail.unified.unwrap();
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://weibo.com/1234567890/Synth0003")
    );
    assert_eq!(u.content_kinds, ["text", "video"]);
    assert_eq!(u.max_duration_ms, Some(30_500));
    assert_eq!(
        (u.stats.shares, u.stats.comments, u.stats.likes),
        (Some(4), Some(5), Some(6))
    );
    assert!(u.cover.is_some());
    let best = work.candidates(false, false);
    let video = best.iter().find(|c| c.resource.role == "video").unwrap();
    assert_eq!(video.representation, "mp4-720p");
    assert_eq!(video.public_headers["referer"], "https://weibo.com/");
    let all = work.candidates(true, false);
    assert_eq!(all.iter().filter(|c| c.resource.role == "video").count(), 3);
    assert!(
        !serde_json::to_string(&detail.document)
            .unwrap()
            .contains("weibocdn"),
        "no address in the offline document"
    );
}

#[test]
fn an_image_post_with_a_live_photo_has_its_stills_and_its_motion() {
    let net = Fixtures::new().on(
        "statuses/show?id=4906000000000004",
        show("4906000000000004"),
    );
    let work = Weibo.work(&net, "4906000000000004", "post").unwrap();
    let roles: Vec<_> = work.assets.iter().map(|a| a.role).collect();
    assert_eq!(roles, ["image", "image", "motion"]);
    let u = work.observed().unified.unwrap();
    assert_eq!(u.content_kinds, ["text", "image", "live_photo"]);
    assert_eq!((u.max_width, u.max_height), (Some(2160), Some(1440)));
    assert_eq!(u.client.as_deref(), Some("iPhone 15"));
    let motion = work.candidates(false, false);
    let motion = motion.iter().find(|c| c.resource.role == "motion").unwrap();
    assert_eq!(
        motion.segments[0].primary,
        "https://livephoto.us.sinaimg.cn/synthetic0002.mov"
    );
}

#[test]
fn a_repost_keeps_its_relation_and_the_originals_words_but_not_its_media() {
    let net = Fixtures::new().on(
        "statuses/show?id=4906000000000002",
        show("4906000000000002"),
    );
    let work = Weibo.work(&net, "4906000000000002", "post").unwrap();
    assert!(
        work.assets.is_empty(),
        "the original's pictures belong to the original"
    );
    assert!(work.body.contains("转发理由"));
    assert!(work.body.contains("转发自 @原作者：被转发的原文"));
    let u = work.observed().unified.unwrap();
    assert_eq!(u.relation.as_str(), "repost");
    assert_eq!(u.related_key.as_deref(), Some("4905000000000001"));
    assert_eq!(work.title, "转发理由");
}

#[test]
fn a_long_post_is_observed_in_full_and_a_missing_text_makes_it_a_summary() {
    let full = Fixtures::new()
        .on("statuses/show?id=4906000000000001", SHOW_LONG)
        .on("statuses/longtext?id=Synth0000", LONGTEXT);
    let work = Weibo.work(&full, "4906000000000001", "post").unwrap();
    assert_eq!(work.observed().completeness, "full");
    assert!(work.body.contains("第二段提到 @某人"));
    assert_eq!(work.unified.mentions, ["某人"]);
    assert_eq!(work.unified.tags, ["长文话题"]);
    assert!(work.unified.edited_at.is_some());
    let cut = Fixtures::new()
        .on("statuses/show?id=4906000000000001", SHOW_LONG)
        .status("statuses/longtext", 500);
    let work = Weibo.work(&cut, "4906000000000001", "post").unwrap();
    let detail = work.observed();
    assert_eq!(detail.completeness, "summary");
    assert_eq!(detail.document.omissions, ["longtext_unavailable"]);
    let walled = Fixtures::new()
        .on("statuses/show?id=4906000000000001", SHOW_LONG)
        .on("statuses/longtext", r#"{"ok":-100}"#);
    let err = Weibo.work(&walled, "4906000000000001", "post").unwrap_err();
    assert_eq!(
        err.class,
        FailureClass::AuthRequired,
        "a login wall is not a short text"
    );
}

#[test]
fn an_observation_must_be_the_post_that_was_asked_for() {
    let net = Fixtures::new().on(
        "statuses/show?id=4906000000000009",
        show("4906000000000003"),
    );
    let err = Weibo.work(&net, "4906000000000009", "post").unwrap_err();
    assert_eq!(
        err.class,
        FailureClass::SchemaChanged,
        "another post's answer is refused"
    );
    assert_eq!(
        Weibo.work(&net, "12x", "post").unwrap_err().class,
        FailureClass::InvalidPayload
    );
    assert_eq!(
        Weibo
            .work(&net, "4906000000000003", "video")
            .unwrap_err()
            .class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn a_single_link_resolves_to_everything_the_offer_needs() {
    let net = Fixtures::new().on(
        "statuses/show?id=4906000000000003",
        show("4906000000000003"),
    );
    let short = mid_to_mblogid("4906000000000003").unwrap();
    for link in [
        "https://m.weibo.cn/detail/4906000000000003".to_owned(),
        format!("https://weibo.com/1234567890/{short}"),
    ] {
        let source = resolve_source(&Weibo, &net, &link).unwrap();
        assert_eq!(
            source.kind, "entry",
            "a post may hold several kinds of media"
        );
        assert_eq!(source.native_account_key, "1234567890");
        assert_eq!(source.display_name.as_deref(), Some("合成创作者"));
        assert_eq!(source.source_duration_seconds, Some(30.5));
        assert_eq!(
            source.canonical_url,
            "https://weibo.com/1234567890/Synth0003"
        );
        assert_eq!(
            source
                .media
                .candidates
                .iter()
                .filter(|c| c.resource.role == "video")
                .count(),
            3
        );
        assert_eq!(source.detail.unwrap().completeness, "full");
    }
    let err = resolve_source(&Weibo, &net, "https://weibo.com/u/1234567890").unwrap_err();
    assert_eq!(err.class, FailureClass::InvalidPayload);
}

/// The home page of a signed-in session, as the real site serves it (2026-10-18): the
/// configuration names the user twice. Values here are synthetic.
fn signed_in_home(uid: &str, idstr: &str) -> String {
    format!(
        r#"<html><head><script>window.$CONFIG = {{"uid":{uid},"user":{{"id":1000000001,"idstr":{idstr},"screen_name":"测试用户"}},"isNormal":true,"serverTime":1760000000}};</script></head><body></body></html>"#
    )
}

const SIGNED_IN_CONFIG: &str = r#"{"ok":1,"data":{"ab_test":{"group":"a"}}}"#;
const HOME: &str = "GET https://weibo.com/\n";

#[test]
fn the_login_probe_reports_what_the_site_says_and_nothing_more() {
    // A login: the configuration answers (and names no one), then the home page names the user.
    for (uid, idstr) in [
        (r#""1000000001""#, r#""1000000001""#),
        ("1000000001", r#""1000000001""#),
        ("null", r#""1000000001""#),
    ] {
        let valid = Fixtures::new()
            .on("ajax/config/get_config", SIGNED_IN_CONFIG)
            .on(HOME, signed_in_home(uid, idstr));
        let probe = Weibo.probe_auth(&valid).unwrap();
        assert_eq!(probe.state, "valid", "uid {uid}");
        assert_eq!(probe.capability["login_proven"], true);
        let urls: Vec<_> = valid.requests().iter().map(|r| r.url.clone()).collect();
        assert_eq!(
            urls,
            [
                "https://weibo.com/ajax/config/get_config",
                "https://weibo.com/"
            ]
        );
        assert!(
            valid.requests().iter().all(|r| r.credential),
            "the probe asks with the session"
        );
    }
    // A visitor: the configuration's login wall, before any page is opened.
    for wall in [
        Fixtures::new().on(
            "ajax/config/get_config",
            r#"{"ok":-100,"url":"https://login.sina.com.cn/sso/login.php"}"#,
        ),
        Fixtures::new().status("ajax/config/get_config", 401),
    ] {
        assert_eq!(Weibo.probe_auth(&wall).unwrap().state, "anonymous");
        assert_eq!(
            wall.count("https://weibo.com/"),
            1,
            "only the configuration"
        );
    }
    // A visitor whose configuration still answered: the home page sends it to the visitor login.
    let sent_away = Fixtures::new()
        .on("ajax/config/get_config", SIGNED_IN_CONFIG)
        .redirect(
            HOME,
            "https://passport.weibo.com/visitor/visitor?entry=miniblog",
        );
    assert_eq!(Weibo.probe_auth(&sent_away).unwrap().state, "anonymous");
    // The group list answers a visitor with `ok` 1 and the default groups (real site,
    // 2026-10-18, with no Cookie at all), and the mobile host is another cookie domain: neither
    // is ever consulted, so neither can pass a visitor off as a login.
    let walled = Fixtures::new()
        .on("ajax/config/get_config", r#"{"ok":-100}"#)
        .on(
            "m.weibo.cn/api/config",
            r#"{"ok":1,"data":{"login":true,"uid":"1000000001"}}"#,
        )
        .on(
            "ajax/feed/allGroups",
            r#"{"ok":1,"groups":[{"group_type":0,"data":[]}],"total_number":1}"#,
        )
        .on(HOME, signed_in_home(r#""1000000001""#, r#""1000000001""#));
    assert_eq!(Weibo.probe_auth(&walled).unwrap().state, "anonymous");
    assert!(
        walled
            .requests()
            .iter()
            .all(|r| r.url.contains("ajax/config/get_config"))
    );
    // Nothing that names a user is not a login, and not a visitor either.
    let pages = [
        "<html><body>欢迎</body></html>".to_owned(),
        r#"<script>window.$CONFIG = {"uid":"","user":null};</script>"#.to_owned(),
        signed_in_home(r#""0""#, r#""0""#),
        signed_in_home(r#""1000000002""#, r#""1000000001""#),
        signed_in_home("null", r#""not-a-number""#),
    ];
    for home in pages {
        let net = Fixtures::new()
            .on("ajax/config/get_config", SIGNED_IN_CONFIG)
            .on(HOME, home.clone());
        let probe = Weibo.probe_auth(&net).unwrap();
        assert_eq!(probe.state, "unknown", "{home}");
        assert_eq!(probe.capability["login_proven"], false);
    }
    for silent in [r#"{"ok":0}"#, r#"{"data":{}}"#] {
        let net = Fixtures::new()
            .on("ajax/config/get_config", silent)
            .on(HOME, signed_in_home(r#""1000000001""#, r#""1000000001""#));
        assert_eq!(Weibo.probe_auth(&net).unwrap().state, "unknown", "{silent}");
        assert_eq!(
            net.count("https://weibo.com/"),
            1,
            "no page after no answer"
        );
    }
    let limited = Fixtures::new().status("ajax/config/get_config", 429);
    assert!(
        Weibo.probe_auth(&limited).is_err(),
        "a refusal is not a verdict"
    );
    let page_limited = Fixtures::new()
        .on("ajax/config/get_config", SIGNED_IN_CONFIG)
        .status(HOME, 429);
    assert!(Weibo.probe_auth(&page_limited).is_err());
}

#[test]
fn requests_use_the_sites_headers_and_never_a_cookie_of_their_own() {
    let net = list_fixtures().on("profile/info", PROFILE);
    Weibo.enumerate(&net, &params(None)).unwrap();
    Weibo.resolve_account(&net, "1234567890").unwrap();
    for request in net.requests() {
        assert_eq!(request.method, "GET");
        assert!(
            request.credential,
            "the session is attached by Core where it exists"
        );
        let names: Vec<_> = request
            .headers
            .as_object()
            .unwrap()
            .keys()
            .cloned()
            .collect();
        for name in &names {
            assert!(
                trace_domain::sites::BASE_REQUEST_HEADERS.contains(&name.as_str())
                    || trace_domain::sites::site("weibo")
                        .unwrap()
                        .request_headers
                        .contains(&name.as_str()),
                "{name} is not a header Weibo may send"
            );
        }
        assert!(request.url.starts_with("https://weibo.com/ajax/"));
    }
}

#[test]
fn the_declared_rules_cover_every_link_form_the_adapter_understands() {
    use trace_contracts::rules::RuleEntry;
    let rules = Weibo.rules();
    let lane = |entry: RuleEntry| rules.iter().find(|r| r.entry == entry).unwrap();
    assert_eq!(lane(RuleEntry::Subscription).lane.as_deref(), Some("post"));
    assert_eq!(lane(RuleEntry::Preview).lane.as_deref(), Some("post"));
    let patterns = |entry: RuleEntry| &lane(entry).url_patterns;
    assert!(
        patterns(RuleEntry::Subscription)
            .iter()
            .any(|p| p.matches("weibo.com", "/u/1234567890").is_some())
    );
    assert!(
        patterns(RuleEntry::Subscription)
            .iter()
            .any(|p| p.matches("m.weibo.cn", "/profile/1234567890").is_some())
    );
    assert!(
        patterns(RuleEntry::Link)
            .iter()
            .any(|p| p.matches("weibo.com", "/1234567890/Synth0003").is_some())
    );
    assert!(patterns(RuleEntry::Link).iter().any(|p| {
        p.matches("m.weibo.cn", "/detail/4906000000000003")
            .is_some()
    }));
    assert!(
        patterns(RuleEntry::Link)
            .iter()
            .any(|p| p.host == "t.cn" && p.expand)
    );
}
