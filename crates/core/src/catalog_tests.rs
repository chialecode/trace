//! Rule catalog: declarations are validated, persisted offline, matched to links and kept
//! when the plugin is disabled.
use crate::catalog::{extract_url, lanes, recognize, rules_of, view};
use crate::config::Profile;
use crate::core::Core;
use crate::store;
use serde_json::json;
use trace_contracts::control::Request;
use trace_contracts::plugin::{AdapterHello, AdapterLimits};
use trace_contracts::rules::{
    RuleDeclaration, RuleEntry, RuleLogin, RuleNetwork, RuleObject, RuleParam, RuleStatus,
    UrlPattern,
};

fn pattern(host: &str, path: &str, object: RuleObject) -> UrlPattern {
    UrlPattern {
        host: host.into(),
        path: path.into(),
        object,
        expand: false,
    }
}

fn rule(
    id: &str,
    entry: RuleEntry,
    patterns: Vec<UrlPattern>,
    status: RuleStatus,
) -> RuleDeclaration {
    RuleDeclaration {
        id: id.into(),
        site: id.split('.').next().unwrap().into(),
        title: id.into(),
        description: "测试规则".into(),
        entry,
        content_kinds: vec!["text".into(), "image".into()],
        url_patterns: patterns,
        params: vec![RuleParam {
            key: "include_reposts".into(),
            title: "包含转发".into(),
            kind: "bool".into(),
            default: "true".into(),
            options: vec![],
        }],
        login: RuleLogin::Optional,
        login_note: None,
        network: RuleNetwork::Direct,
        limits: vec![],
        status,
        verified_at: (status == RuleStatus::Implemented).then(|| "2026-10-16".into()),
        verified_scope: (status == RuleStatus::Implemented).then(|| "登录态 2 页列表".into()),
        lane: (entry == RuleEntry::Subscription).then(|| "post".into()),
    }
}

fn weibo_rules() -> Vec<RuleDeclaration> {
    vec![
        rule(
            "weibo.user.posts",
            RuleEntry::Subscription,
            vec![
                pattern("weibo.com", "/u/{uid}", RuleObject::Account),
                pattern("m.weibo.cn", "/profile/{uid}", RuleObject::Account),
            ],
            RuleStatus::Implemented,
        ),
        rule(
            "weibo.user.preview",
            RuleEntry::Preview,
            vec![pattern("weibo.com", "/u/{uid}", RuleObject::Account)],
            RuleStatus::Partial,
        ),
        rule(
            "weibo.post.link",
            RuleEntry::Link,
            vec![
                pattern("weibo.com", "/{uid}/{mid}", RuleObject::Entry),
                pattern("m.weibo.cn", "/detail/{mid}", RuleObject::Entry),
                UrlPattern {
                    expand: true,
                    ..pattern("t.cn", "/{code}", RuleObject::Entry)
                },
            ],
            RuleStatus::Implemented,
        ),
    ]
}

async fn core() -> (std::sync::Arc<Core>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    (core, dir)
}

async fn store_weibo(core: &Core, rules: Vec<RuleDeclaration>) {
    core.db
        .write(move |tx| {
            store::rules::replace_declared(
                tx,
                "weibo",
                "weibo",
                "0.2.0",
                &rules,
                "2026-10-16T00:00:00Z",
            )
        })
        .await
        .unwrap();
}

fn hello(platform: &str, capabilities: &[&str], rules: Vec<RuleDeclaration>) -> AdapterHello {
    AdapterHello {
        protocol_version: "0.1".into(),
        adapter_version: "0.2.0".into(),
        platform: platform.into(),
        capabilities: capabilities.iter().map(|c| c.to_string()).collect(),
        limits: AdapterLimits::default(),
        rules,
    }
}

#[test]
fn declared_rules_are_validated_and_unverified_implemented_becomes_partial() {
    let mut unverified = rule(
        "weibo.user.posts",
        RuleEntry::Subscription,
        vec![],
        RuleStatus::Implemented,
    );
    unverified.verified_at = None;
    let mut foreign = rule(
        "weibo.user.other",
        RuleEntry::Subscription,
        vec![],
        RuleStatus::Partial,
    );
    foreign.url_patterns = vec![pattern("example.com", "/u/{uid}", RuleObject::Account)];
    let mut stolen = rule(
        "x.user.posts",
        RuleEntry::Subscription,
        vec![],
        RuleStatus::Partial,
    );
    stolen.site = "x".into();
    let rules = rules_of(&hello(
        "weibo",
        &["enumerate"],
        vec![unverified, foreign, stolen],
    ));
    assert_eq!(
        rules.len(),
        1,
        "a foreign host and another site's id are rejected"
    );
    assert_eq!(rules[0].status, RuleStatus::Partial);
    assert!(!rules[0].limits.is_empty());
}

#[test]
fn adapters_without_declarations_get_undeclared_partial_rules_from_their_capabilities() {
    let rules = rules_of(&hello(
        "douyin",
        &["enumerate", "resolve_source", "observe"],
        vec![],
    ));
    let ids: Vec<_> = rules.iter().map(|r| r.id.as_str()).collect();
    assert_eq!(ids, ["douyin.user.undeclared", "douyin.link.undeclared"]);
    assert!(rules.iter().all(|r| r.status == RuleStatus::Partial));
    assert!(
        rules
            .iter()
            .all(|r| r.limits.iter().any(|l| l.contains("没有声明规则")))
    );
    assert!(
        rules_of(&hello("fake", &["enumerate"], vec![])).is_empty(),
        "unregistered platforms have no rules"
    );
}

#[tokio::test]
async fn the_catalog_page_is_served_from_the_database_even_with_the_plugin_disabled() {
    let (core, _dir) = core().await;
    store_weibo(&core, weibo_rules()).await;
    let request = |command: &str, args| Request {
        protocol_version: "0.1".into(),
        request_id: "r".into(),
        command: command.into(),
        args,
        idempotency_key: None,
        if_revision: None,
    };
    // No adapter binary exists in this profile: nothing here may start a process or a request.
    core.db
        .write(|tx| store::plugins::set_enabled(tx, "weibo", false, "2026-10-16T00:00:00Z"))
        .await
        .unwrap();
    let page = crate::app::execute(&core, &request("site.catalog", json!({})))
        .await
        .unwrap();
    let sites = page["sites"].as_array().unwrap();
    assert_eq!(sites.len(), 8);
    let weibo = sites.iter().find(|s| s["id"] == "weibo").unwrap();
    assert_eq!(weibo["plugin_state"], "disabled");
    assert_eq!(
        weibo["rules"].as_array().unwrap().len(),
        3,
        "the last declaration stays visible"
    );
    let implemented = weibo["rules"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|r| r["declaration"]["status"] == "implemented")
        .count();
    assert_eq!(implemented, 2);
    let youtube = sites.iter().find(|s| s["id"] == "youtube").unwrap();
    assert!(youtube["rules"].as_array().unwrap().is_empty());
}

#[tokio::test]
async fn a_rule_the_adapter_stops_declaring_is_retired_not_forgotten() {
    let (core, _dir) = core().await;
    store_weibo(&core, weibo_rules()).await;
    store_weibo(&core, weibo_rules().into_iter().take(1).collect()).await;
    let views = core
        .db
        .read(|c| store::rules::list(c, Some("weibo")))
        .await
        .unwrap();
    assert_eq!(views.len(), 3);
    let retired: Vec<_> = views
        .iter()
        .filter(|v| v.declaration.status == RuleStatus::Retired)
        .map(|v| v.declaration.id.as_str())
        .collect();
    assert_eq!(retired, ["weibo.post.link", "weibo.user.preview"]);
    assert!(
        views
            .iter()
            .filter(|v| !v.declared)
            .all(|v| !v.declaration.usable())
    );
}

#[tokio::test]
async fn links_match_the_right_rules_and_explain_what_they_cannot_match() {
    let (core, _dir) = core().await;
    store_weibo(&core, weibo_rules()).await;
    let recognition = |text: &'static str| {
        let core = core.clone();
        async move { core.db.read(move |c| recognize(c, text)).await.unwrap() }
    };

    let account =
        recognition("看看这个博主 https://weibo.com/u/1234567890?from=share，很不错").await;
    assert_eq!(account.site.as_deref(), Some("weibo"));
    assert_eq!(account.object, Some(RuleObject::Account));
    let ids: Vec<_> = account.rules.iter().map(|r| r.rule_id.as_str()).collect();
    assert_eq!(ids, ["weibo.user.posts", "weibo.user.preview"]);
    assert_eq!(
        account.rules[0].captures,
        [("uid".to_owned(), "1234567890".to_owned())]
    );
    assert!(
        !account.shown.contains("share"),
        "the query is never echoed"
    );

    let entry = recognition("https://m.weibo.cn/detail/4999999999999999").await;
    assert_eq!(entry.object, Some(RuleObject::Entry));
    assert_eq!(entry.rules.len(), 1);
    assert_eq!(entry.rules[0].rule_id, "weibo.post.link");

    let short = recognition("https://t.cn/AbCdEf").await;
    assert!(
        short.needs_expansion,
        "a short link must be expanded by the adapter first"
    );

    let unsupported_page = recognition("https://weibo.com/hot/search/x/y/z").await;
    assert!(unsupported_page.rules.is_empty());
    assert!(unsupported_page.message.unwrap().contains("没有对应的规则"));

    let other_site = recognition("https://example.com/u/1").await;
    assert_eq!(other_site.site, None);
    assert_eq!(other_site.message.as_deref(), Some("不是已支持的网站"));

    let lookalike = recognition("https://weibo.com.evil.example/u/1").await;
    assert_eq!(
        lookalike.site, None,
        "a host that merely starts with a site name is not the site"
    );

    let empty_site = recognition("https://www.youtube.com/@someone").await;
    assert_eq!(empty_site.site.as_deref(), Some("youtube"));
    assert!(empty_site.message.unwrap().contains("还没有加载"));

    let credentials = recognition("https://user:pw@weibo.com/u/1").await;
    assert!(credentials.rules.is_empty());
}

#[tokio::test]
async fn retired_and_unimplemented_rules_are_not_offered_to_links() {
    let (core, _dir) = core().await;
    let mut rules = weibo_rules();
    rules[1].status = RuleStatus::Unimplemented;
    store_weibo(&core, rules).await;
    let hit = core
        .db
        .read(|c| recognize(c, "https://weibo.com/u/1"))
        .await
        .unwrap();
    assert_eq!(hit.rules.len(), 1);
    assert_eq!(hit.rules[0].rule_id, "weibo.user.posts");
    let lanes = core.db.read(|c| lanes(c, "weibo")).await.unwrap();
    assert_eq!(lanes, ["post"]);
    let all = core.db.read(view).await.unwrap();
    assert_eq!(all.sites[0].id, "weibo");
}

#[test]
fn urls_are_extracted_from_share_text_and_bare_hosts() {
    assert_eq!(
        extract_url("【标题】https://v.douyin.com/abc/ 复制此链接").as_deref(),
        Some("https://v.douyin.com/abc/")
    );
    assert_eq!(
        extract_url("weibo.com/u/1").as_deref(),
        Some("https://weibo.com/u/1")
    );
    assert_eq!(
        extract_url("（https://x.com/a/status/1）").as_deref(),
        Some("https://x.com/a/status/1")
    );
    assert_eq!(extract_url("   "), None);
    assert_eq!(extract_url(&"x".repeat(5000)), None);
}
