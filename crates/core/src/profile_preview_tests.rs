//! Profile preview pages: each entry is shown once, the preview ends at its limits and says
//! so, and what TRACE already holds is marked.
use super::*;
use crate::config::Profile;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::catalog::RecognizedRule;
use trace_contracts::plugin::{EntryHints, EnumeratedEntry};

async fn core() -> (tempfile::TempDir, Arc<Core>) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    (dir, core)
}

fn entry(key: &str) -> EnumeratedEntry {
    EnumeratedEntry {
        native_entry_key: key.into(),
        native_kind: "post".into(),
        title: format!("标题 {key}"),
        published_at: None,
        published_precision: String::new(),
        published_margin_seconds: None,
        pinned: false,
        summary_body: Some("  摘要  ".into()),
        stats: Value::Null,
        hints: Some(EntryHints {
            content_kinds: vec!["image".into()],
            image_count: 2,
            ..Default::default()
        }),
    }
}

fn page(keys: impl IntoIterator<Item = String>, more: bool) -> Loaded {
    Loaded {
        page: EnumeratedPage {
            items: keys.into_iter().map(|k| entry(&k)).collect(),
            next_cursor: more.then(|| "next".to_owned()),
            has_more: more,
            coverage: Value::Null,
        },
    }
}

fn preview() -> repo::Preview {
    repo::Preview {
        preview_id: "pv".into(),
        platform: "weibo".into(),
        rule_id: "weibo.user.preview".into(),
        rule_title: "主页".into(),
        source_url: "https://weibo.com/u/1".into(),
        account_key: "1".into(),
        account_name: None,
        lane: None,
        credential_id: None,
        credential_generation: None,
        authentication: "anonymous".into(),
        pages: 0,
        next_cursor: None,
        has_more: true,
        expires_at: later(30).unwrap(),
    }
}

/// Shows the next page of the stored preview, the first one creating it.
async fn show(core: &Core, loaded: Loaded) -> ProfilePage {
    let stored = core.db.read(|c| repo::load(c, "pv")).await.unwrap();
    let first = stored.is_none();
    show_page(
        core,
        stored.unwrap_or_else(preview),
        loaded,
        Vec::new(),
        "主页".into(),
        first,
    )
    .await
    .unwrap()
}

fn keys(page: &ProfilePage) -> Vec<&str> {
    page.items.iter().map(|i| i.native_key.as_str()).collect()
}

#[tokio::test]
async fn an_entry_a_later_page_repeats_is_shown_once_and_the_page_counts_move_on() {
    let (_dir, core) = core().await;
    let first = show(&core, page(["a".to_owned(), "b".to_owned()], true)).await;
    assert_eq!(keys(&first), ["a", "b"]);
    assert_eq!(
        (first.pages_loaded, first.total_items, first.has_more),
        (1, 2, true)
    );
    assert_eq!(first.items[0].summary.as_deref(), Some("摘要"));
    assert_eq!(first.items[0].published_precision, "unknown");
    assert_eq!(
        (first.items[0].image_count, first.items[0].video_count),
        (2, 0)
    );
    // The next page overlaps: a pinned entry comes back, and a new one follows.
    let second = show(&core, page(["b".to_owned(), "c".to_owned()], false)).await;
    assert_eq!(keys(&second), ["c"]);
    assert_eq!(
        (second.pages_loaded, second.total_items, second.has_more),
        (2, 3, false)
    );
    assert!(!second.limited, "the profile simply ended");
}

#[tokio::test]
async fn the_page_and_entry_limits_end_the_preview_and_say_the_profile_has_more() {
    let (_dir, core) = core().await;
    let mut seen = 0usize;
    let mut last = None;
    for page_no in 0..MAX_PAGES {
        let batch = (0..100).map(|i| format!("k{page_no}-{i}"));
        let shown = show(&core, page(batch, true)).await;
        seen += shown.items.len();
        assert!(seen <= MAX_ITEMS, "never more than {MAX_ITEMS} entries");
        let ended = !shown.has_more;
        last = Some(shown);
        if ended {
            break;
        }
    }
    let last = last.unwrap();
    assert_eq!(
        seen, MAX_ITEMS,
        "the entry limit stops it before the page limit"
    );
    assert!(!last.has_more && last.limited);
    assert_eq!(last.total_items as usize, MAX_ITEMS);
    // A stored preview that cannot continue refuses another page.
    let stored = core
        .db
        .read(|c| repo::load(c, "pv"))
        .await
        .unwrap()
        .unwrap();
    assert!(!stored.has_more);
}

#[tokio::test]
async fn the_page_limit_stops_a_profile_of_small_pages() {
    let (_dir, core) = core().await;
    let mut last = None;
    for page_no in 0..MAX_PAGES {
        last = Some(show(&core, page([format!("only-{page_no}")], true)).await);
    }
    let last = last.unwrap();
    assert_eq!(last.pages_loaded, MAX_PAGES);
    assert!(
        !last.has_more && last.limited,
        "more existed, the limit ended it"
    );
}

#[tokio::test]
async fn two_loads_of_the_same_page_cannot_both_be_recorded() {
    let (_dir, core) = core().await;
    show(&core, page(["a".to_owned()], true)).await;
    let stored = || async {
        core.db
            .read(|c| repo::load(c, "pv"))
            .await
            .unwrap()
            .unwrap()
    };
    let race = |keys: Vec<String>, stored: repo::Preview| {
        show_page(
            &core,
            stored,
            page(keys, true),
            Vec::new(),
            "主页".into(),
            false,
        )
    };
    let (first, second) = (stored().await, stored().await);
    let (one, two) = tokio::join!(
        race(vec!["b".into()], first),
        race(vec!["c".into()], second)
    );
    assert!(
        one.is_ok() != two.is_ok(),
        "exactly one of two concurrent loads of the same page wins"
    );
}

#[tokio::test]
async fn held_entries_are_marked_with_how_much_of_them_is_saved() {
    let (_dir, core) = core().await;
    core.db
        .write(|tx| {
            tx.execute_batch(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t');
                 INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e1','weibo','post','held','a','t');
                 INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e2','weibo','post','bare','a','t');
                 INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r1','e1','d','t','b','2026-10-16T00:00:00Z');",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let shown = show(
        &core,
        page(
            ["held".to_owned(), "bare".to_owned(), "new".to_owned()],
            false,
        ),
    )
    .await;
    let by = |key: &str| shown.items.iter().find(|i| i.native_key == key).unwrap();
    assert!(by("held").in_library && by("held").entry_id.as_deref() == Some("e1"));
    assert!(by("held").save_progress.is_some());
    // An entry that was never observed is not "in the library" yet.
    assert!(!by("bare").in_library && !by("new").in_library);
}

#[test]
fn a_link_that_fits_no_preview_rule_says_why() {
    let recognition = Recognition {
        shown: "weibo.com/1".into(),
        site: Some("weibo".into()),
        site_title: Some("微博".into()),
        object: None,
        rules: Vec::new(),
        needs_expansion: false,
        message: Some("没有对应的规则".into()),
    };
    let error = preview_rules(&recognition).unwrap_err();
    assert!(error.to_string().contains("没有对应的规则"));
}

#[test]
fn an_authors_videos_are_offered_before_their_other_lanes() {
    use trace_contracts::rules::{RuleLogin, RuleNetwork, RuleObject, RuleStatus};
    let rule = |id: &str, lane: &str| RecognizedRule {
        rule_id: id.into(),
        title: id.into(),
        entry: RuleEntry::Preview,
        object: RuleObject::Account,
        status: RuleStatus::Partial,
        login: RuleLogin::Optional,
        network: RuleNetwork::Direct,
        lane: Some(lane.into()),
        captures: Vec::new(),
    };
    // The catalog lists rules by id: for a Bilibili space that puts articles first.
    let recognition = Recognition {
        shown: "space.bilibili.com/1".into(),
        site: Some("bilibili".into()),
        site_title: Some("哔哩哔哩".into()),
        object: Some(RuleObject::Account),
        rules: vec![
            rule("bilibili.user.preview_articles", "article"),
            rule("bilibili.user.preview_dynamics", "dynamic"),
            rule("bilibili.user.preview_opus", "image"),
            rule("bilibili.user.preview_videos", "video"),
        ],
        needs_expansion: false,
        message: None,
    };
    let ids: Vec<_> = preview_rules(&recognition)
        .unwrap()
        .into_iter()
        .map(|r| r.0)
        .collect();
    assert_eq!(
        ids,
        [
            "bilibili.user.preview_videos",
            "bilibili.user.preview_opus",
            "bilibili.user.preview_dynamics",
            "bilibili.user.preview_articles"
        ]
    );
}
