//! The X adapter against sanitized synthetic pages, scripts and answers shaped like the site's.
use crate::catalog;
use crate::site::X;
use std::sync::atomic::Ordering;
use trace_contracts::plugin::{EnumerateParams, PluginError, ProxyFetchResult};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::serve::{SiteAdapter, checked_page, resolve_source};
use trace_site_support::kit::testing::Fixtures;
use trace_site_support::runtime::{Fetch, Transport};

const HOME: &str = include_str!("../fixtures/home.html");
const HOME_ANON: &str = include_str!("../fixtures/home_anonymous.html");
const VENDOR: &str = include_str!("../fixtures/vendor.js");
const MAIN: &str = include_str!("../fixtures/main.js");
const USER: &str = include_str!("../fixtures/user.json");
const USER_LEGACY: &str = include_str!("../fixtures/user_legacy_names.json");
const USER_MISSING: &str = include_str!("../fixtures/user_missing.json");
const USER_SUSPENDED: &str = include_str!("../fixtures/user_suspended.json");
const USER_PROTECTED: &str = include_str!("../fixtures/user_protected.json");
const TIMELINE_1: &str = include_str!("../fixtures/timeline_1.json");
const TIMELINE_2: &str = include_str!("../fixtures/timeline_2.json");
const TIMELINE_LEGACY: &str = include_str!("../fixtures/timeline_legacy_nesting.json");
const TIMELINE_BROKEN: &str = include_str!("../fixtures/timeline_broken_media.json");
const TIMELINE_NONE: &str = include_str!("../fixtures/timeline_no_instructions.json");
const TWEET_PHOTO: &str = include_str!("../fixtures/tweet_photo.json");
const TWEET_VIDEO: &str = include_str!("../fixtures/tweet_video.json");
const TWEET_REPOST: &str = include_str!("../fixtures/tweet_repost.json");
const TWEET_QUOTE: &str = include_str!("../fixtures/tweet_quote.json");
const TWEET_LONG: &str = include_str!("../fixtures/tweet_long.json");
const TWEET_GONE: &str = include_str!("../fixtures/tweet_gone.json");
const TWEET_MISSING: &str = include_str!("../fixtures/tweet_missing.json");
const ERRORS_RATE: &str = include_str!("../fixtures/errors_rate.json");
const UID: &str = "1500000000000000001";
const PHOTO: &str = "1700000000000000001";
const VIDEO: &str = "1700000000000000002";
const REPOST: &str = "1700000000000000003";
const QUOTE: &str = "1700000000000000004";
const LONG: &str = "1700000000000000005";
const PINNED: &str = "1700000000000000009";

/// The site's pages and scripts, which every operation needs first.
fn site() -> Fixtures {
    Fixtures::new()
        .on("GET https://x.com/\n", HOME)
        .on("main.bbbb2222.js", MAIN)
        .on("vendor.aaaa1111.js", VENDOR)
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

fn keys(page: &trace_contracts::plugin::EnumeratedPage) -> Vec<&str> {
    page.items
        .iter()
        .map(|e| e.native_entry_key.as_str())
        .collect()
}

/// The shipped ids (`data/web_client.json`), which the first call of a process uses.
const SHIPPED_USER_BY_NAME: &str = "AMIBMjtxEEATh4z8V9GtRg";
const SHIPPED_USER_TWEETS: &str = "7_KBXBpG0zBPRN6UCIV0-w";

#[test]
fn the_shipped_ids_features_and_token_are_used_first_and_the_site_is_not_read() {
    let net = Fixtures::new().on("/UserByScreenName", USER);
    let account = X.resolve_account(&net, "@synthetic_user").unwrap();
    assert_eq!(account.native_account_key, UID);
    X.resolve_account(&net, "https://x.com/synthetic_user")
        .unwrap();
    let sent = net.requests();
    assert_eq!(
        sent.len(),
        2,
        "one call per account and nothing else: no page, no script: {sent:?}"
    );
    let call = &sent[0];
    assert!(
        call.url.contains(&format!(
            "/i/api/graphql/{SHIPPED_USER_BY_NAME}/UserByScreenName?"
        )),
        "{}",
        call.url
    );
    assert!(call.credential, "the session is the visitor's");
    assert!(
        call.headers["authorization"]
            .as_str()
            .unwrap()
            .starts_with("Bearer AAAAAAAAAAAAAAAAAAAAA")
    );
    // The client's declared values are sent, not everything on.
    assert!(
        call.url
            .contains("%22hidden_profile_subscriptions_enabled%22%3Atrue")
    );
    assert!(
        call.url
            .contains("%22rweb_tipjar_consumption_enabled%22%3Afalse")
    );
    assert!(call.url.contains("%22withPayments%22%3Afalse"));
    assert!(call.url.contains("screen_name%22%3A%22synthetic_user"));
    // Nothing signs the call until the site asks for a signature.
    assert!(call.headers.get("x-client-transaction-id").is_none());
    assert!(
        sent.iter()
            .all(|r| r.headers.get("cookie").is_none() && r.headers.get("x-csrf-token").is_none())
    );
}

#[test]
fn a_rejected_id_is_replaced_by_the_one_the_sites_scripts_publish_and_remembered() {
    // The site published a new id: the shipped one is rejected, the page and its main script
    // (the other script is not needed) say what to ask instead.
    let net = site()
        .status(&format!("/{SHIPPED_USER_TWEETS}/UserTweets"), 404)
        .on("/QidUserTweets2/UserTweets", TIMELINE_1);
    let page = X.enumerate(&net, &params(None)).unwrap();
    assert!(!page.items.is_empty());
    let pages = |net: &Fixtures| {
        net.requests()
            .iter()
            .filter(|r| r.url == "https://x.com/")
            .count()
    };
    assert_eq!((pages(&net), net.count("main.bbbb2222.js")), (1, 1));
    assert_eq!(net.count("vendor.aaaa1111.js"), 0);
    assert_eq!(net.count("/UserTweets"), 2);
    let sent = net.requests();
    let page_request = sent.iter().find(|r| r.url == "https://x.com/").unwrap();
    assert!(page_request.credential, "the visitor's own page");
    // Only the visitor's own page and the API carry the session; the script host never does.
    assert!(
        sent.iter()
            .filter(|r| !r.url.contains("/i/api/") && r.url != "https://x.com/")
            .all(|r| !r.credential)
    );
    let ask = sent
        .iter()
        .rfind(|r| r.url.contains("/UserTweets"))
        .unwrap();
    assert!(
        ask.url
            .contains("features=%7B%22feat_a%22%3Atrue%2C%22feat_c%22%3Atrue%7D")
    );
    // What was read is used from then on: no page, no script, and no second rejection.
    X.enumerate(&net, &params(Some("x1:CURSOR_BOTTOM_1")))
        .unwrap();
    assert_eq!((pages(&net), net.count("main.bbbb2222.js")), (1, 1));
    assert_eq!(net.count("/UserTweets"), 3);
}

#[test]
fn an_id_that_stays_rejected_or_scripts_that_do_not_list_it_are_a_changed_interface() {
    // An id that stays rejected is never an empty list.
    let stubborn = site()
        .status(&format!("/{SHIPPED_USER_TWEETS}/UserTweets"), 404)
        .status("/QidUserTweets2/UserTweets", 404);
    let error = X.enumerate(&stubborn, &params(None)).unwrap_err();
    assert_eq!(error.class, FailureClass::SchemaChanged);
    assert!(
        error.message.contains("after its ids were read again"),
        "{}",
        error.message
    );
    // Asking again at once reads nothing: the scripts were read moments ago.
    let before = stubborn.requests().len();
    assert_eq!(
        X.enumerate(&stubborn, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    assert_eq!(
        stubborn.requests().len(),
        before + 2,
        "the two API calls only"
    );

    // Scripts that do not list the operations are a changed interface too, and the failure is
    // remembered: a changed site costs one read per interval, not one per call.
    catalog::forget();
    let empty = Fixtures::new()
        .on("GET https://x.com/\n", HOME)
        .on("client-web", VENDOR)
        .status(&format!("/{SHIPPED_USER_TWEETS}/UserTweets"), 404);
    assert_eq!(
        X.enumerate(&empty, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    let reads = empty.requests().len();
    assert_eq!(
        X.enumerate(&empty, &params(None)).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    assert_eq!(
        empty.requests().len(),
        reads + 1,
        "only the rejected call is made again, the site's scripts are not"
    );
}

#[test]
fn operations_the_main_script_loads_on_demand_are_read_from_the_api_chunk() {
    let home = r#"<html><script src="https://abs.twimg.com/responsive-web/client-web/main.cccc3333.js"></script></html>"#;
    let main = r#"const t="Bearer AAAAAAAAAAAAAAAAAAAAASyntheticFixtureTokenNotARealTokenSyntheticFixtureTokenNotARealToken%3Dxxxxxxxx";var o=e=>e+"."+{"loader":"11112222","api":"9d8dcb8"}[e]+"a.js";"#;
    let net = Fixtures::new()
        .on("GET https://x.com/\n", home)
        .on("main.cccc3333.js", main)
        .on("client-web/api.9d8dcb8a.js", MAIN)
        .status(&format!("/{SHIPPED_USER_BY_NAME}/UserByScreenName"), 404)
        .on("/QidUserByScreenName1/UserByScreenName", USER);
    let account = X.resolve_account(&net, "@synthetic_user").unwrap();
    assert_eq!(account.native_account_key, UID);
    let asked: Vec<String> = net.requests().into_iter().map(|r| r.url).collect();
    assert!(
        asked.iter().any(|u| u.contains("api.9d8dcb8a.js")),
        "{asked:?}"
    );

    // When nothing lists the operations the failure says what was seen, in counts and names of
    // public files only.
    catalog::forget();
    let bare = Fixtures::new()
        .on("GET https://x.com/\n", home)
        .on("main.cccc3333.js", "var a=1;")
        .status(&format!("/{SHIPPED_USER_BY_NAME}/UserByScreenName"), 404);
    let error = X.resolve_account(&bare, "@synthetic_user").unwrap_err();
    assert_eq!(error.class, FailureClass::SchemaChanged);
    assert!(
        error
            .message
            .contains("page lists 1 scripts, 1 on the script host"),
        "{}",
        error.message
    );
    assert!(
        error
            .message
            .contains("missing UserByScreenName,UserTweets,TweetResultByRestId"),
        "{}",
        error.message
    );
}

const HOME_SIGNED: &str = include_str!("../fixtures/home_transaction.html");
const ONDEMAND: &str = include_str!("../fixtures/ondemand.js");

/// A site that rejects every call of its GraphQL interface that is not signed with a
/// transaction id and answers everything else from the fixtures.
struct WantsSignature {
    inner: Fixtures,
    refused: std::sync::atomic::AtomicUsize,
}

impl Transport for WantsSignature {
    fn request(&self, fetch: &Fetch<'_>) -> Result<ProxyFetchResult, PluginError> {
        if fetch.url.contains("/i/api/graphql/")
            && fetch.headers.get("x-client-transaction-id").is_none()
        {
            self.refused.fetch_add(1, Ordering::SeqCst);
            return Err(trace_site_support::error(
                FailureClass::SourceUnavailable,
                "site HTTP failure",
            ));
        }
        self.inner.request(fetch)
    }
}

fn signed_id_of(call: &trace_site_support::kit::testing::Recorded) -> Vec<u8> {
    use base64::Engine as _;
    base64::engine::general_purpose::STANDARD_NO_PAD
        .decode(call.headers["x-client-transaction-id"].as_str().unwrap())
        .unwrap()
}

#[test]
fn a_site_that_rejects_unsigned_calls_is_called_again_with_a_transaction_id_and_then_always_with_one()
 {
    // Its scripts still publish the shipped ids, so what the site wanted was the signature.
    let main = MAIN
        .replace("QidUserByScreenName1", SHIPPED_USER_BY_NAME)
        .replace("QidUserTweets2", SHIPPED_USER_TWEETS);
    let net = WantsSignature {
        inner: Fixtures::new()
            .on(
                "GET https://x.com/
",
                HOME_SIGNED,
            )
            .on("main.bbbb2222.js", main)
            .on("ondemand.s.0123abcdefa.js", ONDEMAND)
            .on("/UserByScreenName", USER)
            .on("/UserTweets", TIMELINE_1),
        refused: 0.into(),
    };
    let account = X.resolve_account(&net, "@synthetic_user").unwrap();
    assert_eq!(account.native_account_key, UID);
    assert_eq!(net.refused.load(Ordering::SeqCst), 1, "the unsigned call");
    let sent = net.inner.requests();
    let urls: Vec<&str> = sent.iter().map(|r| r.url.as_str()).collect();
    assert_eq!(urls.len(), 4, "{urls:?}");
    assert_eq!(urls[0], "https://x.com/");
    assert!(urls[1].ends_with("main.bbbb2222.js"));
    assert!(urls[2].ends_with("ondemand.s.0123abcdefa.js"), "{urls:?}");
    assert!(urls[3].contains(&format!("/{SHIPPED_USER_BY_NAME}/UserByScreenName")));
    assert!(
        sent[2..3].iter().all(|r| !r.credential),
        "the script host never gets the session"
    );
    // 1 byte of noise, the 70 byte key, the clock, 16 bytes of hash and the closing byte.
    assert_eq!(signed_id_of(&sent[3]).len(), 1 + 70 + 4 + 16 + 1);

    // The next call is signed at once: nothing is read again and nothing is refused.
    X.enumerate(&net, &params(None)).unwrap();
    assert_eq!(net.refused.load(Ordering::SeqCst), 1);
    let sent = net.inner.requests();
    assert_eq!(sent.len(), 5);
    assert!(
        sent[4]
            .url
            .contains(&format!("/{SHIPPED_USER_TWEETS}/UserTweets"))
    );
    assert_eq!(signed_id_of(&sent[4]).len(), 92);
}

#[test]
fn a_new_id_and_a_signature_arrive_together_when_the_site_changed_both() {
    let net = Fixtures::new()
        .on(
            "GET https://x.com/
",
            HOME_SIGNED,
        )
        .on("main.bbbb2222.js", MAIN)
        .on("ondemand.s.0123abcdefa.js", ONDEMAND)
        .status(&format!("/{SHIPPED_USER_BY_NAME}/UserByScreenName"), 404)
        .on("/QidUserByScreenName1/UserByScreenName", USER);
    X.resolve_account(&net, "@synthetic_user").unwrap();
    let sent = net.requests();
    let call = sent.last().unwrap();
    assert!(call.url.contains("/QidUserByScreenName1/"));
    assert_eq!(signed_id_of(call).len(), 92);
    assert!(
        sent[0].headers.get("x-client-transaction-id").is_none(),
        "the shipped call is not signed"
    );
}

#[test]
fn a_home_page_without_the_signing_material_still_gives_the_ids() {
    // `home.html` has no verification key: the catalog it gives cannot sign, and does not need to
    // for a site that only changed ids.
    let net = site()
        .status(&format!("/{SHIPPED_USER_BY_NAME}/UserByScreenName"), 404)
        .on("/QidUserByScreenName1/UserByScreenName", USER);
    X.resolve_account(&net, "@synthetic_user").unwrap();
    assert!(
        net.requests()
            .iter()
            .all(|r| r.headers.get("x-client-transaction-id").is_none())
    );
    assert_eq!(net.count("ondemand"), 0);
}

#[test]
fn an_account_resolves_to_its_stable_key_and_the_reasons_for_no_user_are_told_apart() {
    let resolve = |input: &str, answer: &str| {
        let net = site()
            .on("/UserByScreenName", answer)
            .redirect("t.co/AbCdEf1234", "https://x.com/synthetic_user");
        X.resolve_account(&net, input)
    };
    let account = resolve("https://t.co/AbCdEf1234", USER).unwrap();
    assert_eq!(account.native_account_key, UID);
    assert_eq!(account.display_name.as_deref(), Some("合成用户"));
    assert_eq!(
        account.profile_url.as_deref(),
        Some("https://x.com/synthetic_user")
    );
    assert_eq!(account.evidence["statuses_count"], 6);
    assert_eq!(
        resolve("synthetic_user", USER_LEGACY)
            .unwrap()
            .display_name
            .as_deref(),
        Some("旧结构")
    );
    assert_eq!(
        resolve("synthetic_user", USER_MISSING).unwrap_err().class,
        FailureClass::SourceUnavailable
    );
    assert_eq!(
        resolve("synthetic_user", USER_SUSPENDED).unwrap_err().class,
        FailureClass::SourceUnavailable
    );
    assert_eq!(
        resolve("synthetic_user", USER_PROTECTED).unwrap_err().class,
        FailureClass::PermissionDenied
    );
    assert_eq!(
        resolve("someone_else", USER).unwrap_err().class,
        FailureClass::SchemaChanged
    );
    assert_eq!(
        resolve("https://x.com/home", USER).unwrap_err().class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn the_timeline_lists_pinned_reposts_quotes_and_long_posts_and_pages_by_the_bottom_cursor() {
    let net = site()
        .on("CURSOR_BOTTOM_1", TIMELINE_2)
        .on("/UserTweets", TIMELINE_1);
    let first = X.enumerate(&net, &params(None)).unwrap();
    assert_eq!(keys(&first), [PINNED, PHOTO, VIDEO, REPOST, QUOTE, LONG]);
    assert!(first.has_more);
    assert_eq!(first.next_cursor.as_deref(), Some("x1:CURSOR_BOTTOM_1"));
    assert_eq!(
        first.coverage["unreadable_count"], 1,
        "a tombstone is not a post"
    );
    assert!(first.items[0].pinned && !first.items[1].pinned);
    let photo = &first.items[1];
    assert_eq!(photo.published_at.as_deref(), Some("2026-10-14T20:19:24Z"));
    assert_eq!(photo.title, "A photo & more https://example.org/page");
    assert_eq!(photo.stats["plays"], 4321);
    assert_eq!(photo.stats["likes"], 12);
    assert_eq!(photo.stats["favorites"], 4);
    let hints = photo.hints.as_ref().unwrap();
    assert_eq!((hints.image_count, hints.video_count), (1, 1));
    assert_eq!(hints.content_kinds, ["text", "image", "video"]);
    let video = first.items[2].hints.as_ref().unwrap();
    assert_eq!((video.video_count, video.duration_ms), (1, Some(15_500)));
    assert!(
        first.items[3]
            .summary_body
            .as_deref()
            .unwrap()
            .contains("the original words")
    );
    let repost = first.items[3].hints.as_ref().unwrap();
    assert_eq!(
        (
            repost.content_kinds.as_slice(),
            repost.image_count,
            repost.video_count
        ),
        (&["text".to_owned()][..], 0, 0),
        "the original's pictures and video are not part of a repost, so the list does not offer them"
    );
    assert_eq!(
        first.items[5].title,
        "The full long text of the post, beyond the short limit."
    );

    let second = X
        .enumerate(&net, &params(first.next_cursor.as_deref()))
        .unwrap();
    assert!(second.items.is_empty() && !second.has_more && second.next_cursor.is_none());
    assert!(checked_page(second, &params(Some("x1:CURSOR_BOTTOM_1"))).is_ok());
    let call = net
        .requests()
        .into_iter()
        .find(|r| r.url.contains("/UserTweets"))
        .unwrap();
    assert!(
        call.url.contains(&format!("userId%22%3A%22{UID}")) && call.url.contains("count%22%3A20")
    );
}

#[test]
fn walls_limits_and_changed_shapes_of_a_timeline_are_told_apart() {
    let class = |net: Fixtures| X.enumerate(&net, &params(None)).unwrap_err().class;
    assert_eq!(
        class(site().on("/UserTweets", TIMELINE_NONE)),
        FailureClass::SchemaChanged
    );
    assert_eq!(
        class(site().on("/UserTweets", ERRORS_RATE)),
        FailureClass::RateLimited
    );
    assert_eq!(
        class(site().status("/UserTweets", 401)),
        FailureClass::AuthRequired
    );
    assert_eq!(
        class(site().status("/UserTweets", 429)),
        FailureClass::RateLimited
    );
    // Posts that carry no media file are listed; their omission shows in the detail.
    let page = X
        .enumerate(&site().on("/UserTweets", TIMELINE_BROKEN), &params(None))
        .unwrap();
    assert_eq!(keys(&page), ["1700000000000000007"]);
    let legacy = X
        .enumerate(&site().on("/UserTweets", TIMELINE_LEGACY), &params(None))
        .unwrap();
    assert_eq!(keys(&legacy), [VIDEO]);
    for cursor in ["", "x2:abc", "x1:", "x1:a b", "x1:a\"b"] {
        let err = X
            .enumerate(&Fixtures::new(), &params(Some(cursor)))
            .unwrap_err();
        assert_eq!(err.class, FailureClass::SchemaChanged, "{cursor:?}");
    }
    let other = EnumerateParams {
        content_type: "media".into(),
        ..params(None)
    };
    assert_eq!(
        X.enumerate(&Fixtures::new(), &other).unwrap_err().class,
        FailureClass::Unsupported
    );
    let bad_user = EnumerateParams {
        native_account_key: "abc".into(),
        ..params(None)
    };
    assert_eq!(
        X.enumerate(&Fixtures::new(), &bad_user).unwrap_err().class,
        FailureClass::InvalidPayload
    );
}

#[test]
fn a_post_with_pictures_and_a_gif_keeps_original_sizes_and_expanded_links() {
    let net = site().on("/TweetResultByRestId", TWEET_PHOTO);
    let work = X.work(&net, PHOTO, "post").unwrap();
    assert_eq!(work.body, "A photo & more https://example.org/page");
    assert_eq!(work.unified.tags, ["synthetic"]);
    assert_eq!(work.unified.mentions, ["friend_one"]);
    assert_eq!(work.unified.client.as_deref(), Some("Twitter Web App"));
    assert_eq!(work.unified.language.as_deref(), Some("en"));
    assert_eq!(
        work.unified.canonical_url.as_deref(),
        Some(format!("https://x.com/synthetic_user/status/{PHOTO}").as_str())
    );
    assert_eq!(work.metadata["quote_count"], 2);
    let roles: Vec<_> = work.assets.iter().map(|a| a.role).collect();
    assert_eq!(roles, ["image", "video", "cover"]);
    let best = work.candidates(false, false);
    let image = best.iter().find(|c| c.resource.role == "image").unwrap();
    assert_eq!(image.representation, "orig");
    assert_eq!(
        work.candidates(true, false)
            .iter()
            .filter(|c| c.resource.role == "image")
            .count(),
        4
    );
    assert_eq!(work.observed().completeness, "full");
    let call = net
        .requests()
        .into_iter()
        .find(|r| r.url.contains("TweetResultByRestId"))
        .unwrap();
    assert!(
        call.url
            .contains("%22withArticleRichContentState%22%3Afalse")
    );
    assert!(call.url.contains("%22withAuxiliaryUserLabels%22%3Afalse"));
}

#[test]
fn video_renditions_rank_by_size_and_playlists_are_not_files() {
    let net = site().on("/TweetResultByRestId", TWEET_VIDEO);
    let work = X.work(&net, VIDEO, "post").unwrap();
    let all = work.candidates(true, false);
    let videos: Vec<_> = all
        .iter()
        .filter(|c| c.resource.role == "video")
        .map(|c| c.representation.as_str())
        .collect();
    assert_eq!(videos, ["mp4-2176000", "mp4-832000", "mp4-256000"]);
    let unified = work.observed().unified.unwrap();
    assert_eq!(
        (
            unified.max_width,
            unified.max_height,
            unified.max_duration_ms
        ),
        (Some(1280), Some(720), Some(15_500))
    );
}

#[test]
fn reposts_and_quotes_keep_their_relation_and_reposts_do_not_copy_the_originals_media() {
    let repost = X
        .work(
            &site().on("/TweetResultByRestId", TWEET_REPOST),
            REPOST,
            "post",
        )
        .unwrap();
    assert_eq!(
        repost.unified.relation,
        trace_contracts::unified::Relation::Repost
    );
    assert_eq!(
        repost.unified.related_key.as_deref(),
        Some("1600000000000000001")
    );
    assert_eq!(repost.body, "转发自 @other_user：the original words");
    assert!(repost.assets.is_empty());
    let quote = X
        .work(
            &site().on("/TweetResultByRestId", TWEET_QUOTE),
            QUOTE,
            "post",
        )
        .unwrap();
    assert_eq!(
        quote.unified.relation,
        trace_contracts::unified::Relation::Quote
    );
    assert_eq!(
        quote.unified.related_key.as_deref(),
        Some("1600000000000000002")
    );
    assert_eq!(
        quote.body,
        "my comment\n\n引用 @synthetic_user：quoted words"
    );
    assert_eq!(quote.title, "my comment");
    let long = X
        .work(&site().on("/TweetResultByRestId", TWEET_LONG), LONG, "post")
        .unwrap();
    assert_eq!(
        long.body,
        "The full long text of the post, beyond the short limit."
    );
    assert_eq!(long.evidence["long_text"], true);
}

#[test]
fn a_tombstone_a_missing_post_and_a_post_of_another_id_are_not_the_same_failure() {
    let class = |answer: &str, key: &str| {
        X.work(&site().on("/TweetResultByRestId", answer), key, "post")
            .unwrap_err()
            .class
    };
    assert_eq!(class(TWEET_GONE, PHOTO), FailureClass::PermissionDenied);
    assert_eq!(class(TWEET_MISSING, PHOTO), FailureClass::SourceUnavailable);
    assert_eq!(class(TWEET_PHOTO, VIDEO), FailureClass::SchemaChanged);
    assert_eq!(
        X.work(&Fixtures::new(), "abc", "post").unwrap_err().class,
        FailureClass::InvalidPayload
    );
    let broken = X
        .enumerate(&site().on("/UserTweets", TIMELINE_BROKEN), &params(None))
        .unwrap();
    assert_eq!(broken.items.len(), 1);
}

#[test]
fn links_become_works_and_the_single_link_flow_offers_the_media() {
    let net = site()
        .redirect(
            "t.co/Zz9Zz9",
            &format!("https://x.com/synthetic_user/status/{VIDEO}?s=20"),
        )
        .on("/TweetResultByRestId", TWEET_VIDEO);
    let link = X.link(&net, "https://t.co/Zz9Zz9").unwrap();
    assert_eq!((link.key.as_str(), link.kind.as_str()), (VIDEO, "post"));
    let link = X
        .link(&net, &format!("https://twitter.com/a/status/{PHOTO}"))
        .unwrap();
    assert_eq!(link.key, PHOTO);
    assert_eq!(
        X.link(&net, "https://x.com/a").unwrap_err().class,
        FailureClass::InvalidPayload
    );
    let source = resolve_source(&X, &net, &format!("https://x.com/a/status/{VIDEO}")).unwrap();
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
fn the_login_is_proven_by_the_sessions_user_id_and_its_absence_proves_nothing() {
    let probe = |page: &str| {
        X.probe_auth(&Fixtures::new().on("GET https://x.com/\n", page))
            .unwrap()
    };
    let logged_in = probe(HOME);
    assert_eq!(logged_in.state, "valid");
    assert_eq!(logged_in.capability["login_proven"], true);
    let anonymous = probe(HOME_ANON);
    assert_eq!(anonymous.state, "unknown");
    assert_eq!(anonymous.capability["state_read"], true);
}
