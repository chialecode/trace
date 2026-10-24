//! The production executables of the sites that share the adapter kit, on stdio: what each
//! declares at hello is catalogued and recognizes its own links, with no network and no
//! account. Fixture-level mapping is tested inside each adapter crate.
use serde_json::{Value, json};
use std::{
    io::BufReader,
    path::PathBuf,
    process::{Child, Command, Stdio},
};
use trace_contracts::plugin::AdapterHello;
use trace_contracts::rules::RuleEntry;
use trace_contracts::wire;
use trace_core::{catalog, db::DbActor, store};

struct Adapter {
    child: Child,
    out: BufReader<std::process::ChildStdout>,
    input: std::process::ChildStdin,
}

impl Adapter {
    fn new(site: &str) -> Self {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join(format!("../../target/debug/trace-adapter-{site}.exe"));
        let mut child = Command::new(path)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("build production adapters before process tests");
        let out = BufReader::new(child.stdout.take().unwrap());
        let input = child.stdin.take().unwrap();
        Self { child, out, input }
    }

    fn hello(&mut self) -> AdapterHello {
        wire::write_frame(
            &mut self.input,
            &json!({"protocol_version": trace_domain::PLUGIN_PROTOCOL_VERSION,
                "request_id": "hello", "method": "hello", "params": {}, "deadline_ms": 5000}),
        )
        .unwrap();
        let frame: Value = wire::read_frame(&mut self.out).unwrap();
        assert_eq!(frame["request_id"], "hello");
        serde_json::from_value(frame["result"].clone()).expect("hello result")
    }
}

impl Drop for Adapter {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// What a person may paste for a site and what it must be taken for.
struct Case {
    link: &'static str,
    rule: &'static str,
}

/// Every site here has one subscription rule per lane, one preview rule and one link rule.
struct Site {
    id: &'static str,
    lanes: &'static [&'static str],
    cases: &'static [Case],
}

const SITES: &[Site] = &[
    Site {
        id: "weibo",
        lanes: &["post"],
        cases: &[
            Case {
                link: "https://weibo.com/u/1669879400",
                rule: "weibo.user.posts",
            },
            Case {
                link: "https://weibo.com/detail/4967123456789012",
                rule: "weibo.post.link",
            },
            Case {
                link: "https://weibo.com/1669879400/OAbCdEfGh",
                rule: "weibo.post.link",
            },
            Case {
                link: "https://t.cn/A6xxxxxx",
                rule: "weibo.user.posts",
            },
        ],
    },
    Site {
        id: "youtube",
        lanes: &["video", "short", "post"],
        cases: &[
            Case {
                link: "https://www.youtube.com/@SomeName",
                rule: "youtube.channel.videos",
            },
            Case {
                link: "https://www.youtube.com/@SomeName",
                rule: "youtube.channel.preview_posts",
            },
            Case {
                link: "https://www.youtube.com/@SomeName/shorts",
                rule: "youtube.channel.shorts",
            },
            Case {
                link: "https://www.youtube.com/channel/UCtest_channel_00000001a/posts",
                rule: "youtube.channel.preview_posts",
            },
            Case {
                link: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
                rule: "youtube.video.link",
            },
            Case {
                link: "https://youtu.be/dQw4w9WgXcQ",
                rule: "youtube.video.link",
            },
            Case {
                link: "https://www.youtube.com/shorts/dQw4w9WgXcQ",
                rule: "youtube.short.link",
            },
            Case {
                link: "https://www.youtube.com/post/UgkxSyntheticPostAAAAAAAAAAAAAAAAAAAA01",
                rule: "youtube.post.link",
            },
        ],
    },
    Site {
        id: "tiktok",
        lanes: &["video"],
        cases: &[
            Case {
                link: "https://www.tiktok.com/@sample.creator",
                rule: "tiktok.user.posts",
            },
            Case {
                link: "https://www.tiktok.com/@sample.creator",
                rule: "tiktok.user.preview",
            },
            Case {
                link: "https://www.tiktok.com/@sample.creator/video/7000000000000000001",
                rule: "tiktok.post.link",
            },
            Case {
                link: "https://www.tiktok.com/@sample.creator/photo/7000000000000000001",
                rule: "tiktok.post.link",
            },
            Case {
                link: "https://vm.tiktok.com/ZMabc123/",
                rule: "tiktok.post.link",
            },
        ],
    },
    Site {
        id: "xiaohongshu",
        lanes: &["note"],
        cases: &[
            Case {
                link: "https://www.xiaohongshu.com/user/profile/5a1b2c3d4e5f60718293a4b5?xsec_token=abcdefgh1234",
                rule: "xiaohongshu.user.notes",
            },
            Case {
                link: "https://www.xiaohongshu.com/user/profile/5a1b2c3d4e5f60718293a4b5",
                rule: "xiaohongshu.user.preview",
            },
            Case {
                link: "https://www.xiaohongshu.com/explore/65f0a1b2c3d4e5f607182931?xsec_token=abcdefgh1234",
                rule: "xiaohongshu.note.link",
            },
            Case {
                link: "https://www.xiaohongshu.com/discovery/item/65f0a1b2c3d4e5f607182931",
                rule: "xiaohongshu.note.link",
            },
            Case {
                link: "https://xhslink.com/a/AbCdEf",
                rule: "xiaohongshu.note.link",
            },
        ],
    },
    Site {
        id: "instagram",
        lanes: &["post"],
        cases: &[
            Case {
                link: "https://www.instagram.com/nasa/",
                rule: "instagram.user.posts",
            },
            Case {
                link: "https://www.instagram.com/nasa/",
                rule: "instagram.user.preview",
            },
            Case {
                link: "https://www.instagram.com/p/CxPhoto0001/",
                rule: "instagram.post.link",
            },
            Case {
                link: "https://www.instagram.com/reel/CxReel00002/",
                rule: "instagram.post.link",
            },
            Case {
                link: "https://www.instagram.com/nasa/p/CxPhoto0001/",
                rule: "instagram.post.link",
            },
        ],
    },
    Site {
        id: "x",
        lanes: &["post"],
        cases: &[
            Case {
                link: "https://x.com/nasa",
                rule: "x.user.posts",
            },
            Case {
                link: "https://twitter.com/nasa/media",
                rule: "x.user.preview",
            },
            Case {
                link: "https://x.com/nasa/status/1700000000000000001",
                rule: "x.post.link",
            },
            Case {
                link: "https://x.com/i/status/1700000000000000001",
                rule: "x.post.link",
            },
            Case {
                link: "https://t.co/AbCdEf1234",
                rule: "x.post.link",
            },
        ],
    },
    Site {
        id: "bilibili",
        lanes: &["video", "dynamic", "image", "article", "live"],
        cases: &[
            Case {
                link: "https://space.bilibili.com/94697",
                rule: "bilibili.user.videos",
            },
            Case {
                link: "https://space.bilibili.com/94697/video",
                rule: "bilibili.user.preview_videos",
            },
            Case {
                link: "https://space.bilibili.com/94697/upload/video",
                rule: "bilibili.user.videos",
            },
            Case {
                link: "https://space.bilibili.com/94697/dynamic",
                rule: "bilibili.user.dynamics",
            },
            Case {
                link: "https://space.bilibili.com/94697/opus",
                rule: "bilibili.user.opus",
            },
            Case {
                link: "https://space.bilibili.com/94697/article",
                rule: "bilibili.user.preview_articles",
            },
            Case {
                link: "https://www.bilibili.com/video/BV1Ab4C5D6E7",
                rule: "bilibili.video.link",
            },
            Case {
                link: "https://m.bilibili.com/video/BV1Ab4C5D6E7",
                rule: "bilibili.video.link",
            },
            Case {
                link: "https://b23.tv/AbCdEf",
                rule: "bilibili.video.link",
            },
            Case {
                link: "https://live.bilibili.com/12345",
                rule: "bilibili.live.link",
            },
        ],
    },
    Site {
        id: "douyin",
        lanes: &["video", "image", "article", "dynamic", "live"],
        cases: &[
            Case {
                link: "https://www.douyin.com/user/MS4wLjABAAAAsynthetic",
                rule: "douyin.user.videos",
            },
            Case {
                link: "https://www.douyin.com/user/MS4wLjABAAAAsynthetic",
                rule: "douyin.user.preview_dynamics",
            },
            Case {
                link: "https://v.douyin.com/AbCdEf/",
                rule: "douyin.user.live",
            },
            Case {
                link: "https://v.douyin.com/AbCdEf/",
                rule: "douyin.video.link",
            },
            Case {
                link: "https://www.douyin.com/video/7000000000000000001",
                rule: "douyin.video.link",
            },
            Case {
                link: "https://live.douyin.com/123456",
                rule: "douyin.live.link",
            },
        ],
    },
];

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn each_adapter_declares_catalogued_rules_that_recognize_its_links() {
    let dir = tempfile::tempdir().unwrap();
    let (db, _actor) = DbActor::start(
        rusqlite::Connection::open(dir.path().join("rules.db")).unwrap(),
        16,
    )
    .unwrap();
    for site in SITES {
        let hello = Adapter::new(site.id).hello();
        assert_eq!(hello.platform, site.id);
        let rules = catalog::rules_of(&hello);
        assert!(
            !rules.is_empty(),
            "{}: no rules survived normalization",
            site.id
        );
        assert_eq!(
            rules.len(),
            hello.rules.len(),
            "{}: a declared rule was rejected by the catalog",
            site.id
        );
        for entry in [RuleEntry::Subscription, RuleEntry::Preview, RuleEntry::Link] {
            assert!(
                rules.iter().any(|r| r.entry == entry),
                "{}: no {entry:?} rule",
                site.id
            );
        }
        let (id, stored) = (site.id.to_owned(), rules.clone());
        db.write(move |tx| {
            store::rules::replace_declared(tx, &id, &id, "test", &stored, "2026-10-16T00:00:00Z")
        })
        .await
        .unwrap();
        let id = site.id.to_owned();
        let lanes = db.read(move |c| catalog::lanes(c, &id)).await.unwrap();
        for lane in site.lanes {
            assert!(lanes.iter().any(|l| l == lane), "{}: lane {lane}", site.id);
        }
        for case in site.cases {
            let link = case.link.to_owned();
            let found = db
                .read(move |c| catalog::recognize(c, &link))
                .await
                .unwrap();
            assert!(
                found.rules.iter().any(|r| r.rule_id == case.rule),
                "{}: {} was taken for {:?}",
                site.id,
                case.link,
                found.rules.iter().map(|r| &r.rule_id).collect::<Vec<_>>()
            );
        }
    }
}

/// What an adapter says about a rule must be what the recorded real-site checks support:
/// the same set of rules, and for each the same status, date and scope. A rule is never
/// `implemented` here unless `docs/evidence/rule-evidence.json` (checked by the stage gate
/// against the authorized budgets and the required checks) says it was.
#[test]
fn rule_status_matches_recorded_real_evidence() {
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../docs/evidence/rule-evidence.json");
    let record: Value =
        serde_json::from_str(&std::fs::read_to_string(path).expect("rule evidence file"))
            .expect("rule evidence is JSON");
    let recorded_sites: Vec<_> = record["sites"]
        .as_object()
        .unwrap()
        .keys()
        .cloned()
        .collect();
    let mut declared_sites: Vec<_> = SITES.iter().map(|s| s.id.to_owned()).collect();
    declared_sites.sort();
    assert_eq!(recorded_sites, declared_sites, "every site has a record");
    for site in SITES {
        let hello = Adapter::new(site.id).hello();
        let rules = catalog::rules_of(&hello);
        let recorded = record["sites"][site.id]["rules"]
            .as_object()
            .unwrap_or_else(|| panic!("{}: no recorded rules", site.id));
        let mut recorded_ids: Vec<_> = recorded.keys().cloned().collect();
        let mut declared_ids: Vec<_> = rules.iter().map(|r| r.id.clone()).collect();
        recorded_ids.sort();
        declared_ids.sort();
        assert_eq!(recorded_ids, declared_ids, "{}: rules differ", site.id);
        for rule in &rules {
            let seen = &recorded[&rule.id];
            let word = |value: Value| value.as_str().unwrap().to_owned();
            assert_eq!(
                seen["status"].as_str(),
                Some(word(serde_json::to_value(rule.status).unwrap()).as_str()),
                "{}: status",
                rule.id
            );
            assert_eq!(
                seen["entry"].as_str(),
                Some(word(serde_json::to_value(rule.entry).unwrap()).as_str()),
                "{}: entry",
                rule.id
            );
            assert_eq!(
                seen.get("verifiedAt").and_then(Value::as_str),
                rule.verified_at.as_deref(),
                "{}: verification date",
                rule.id
            );
            assert_eq!(
                seen.get("scope").and_then(Value::as_str),
                rule.verified_scope.as_deref(),
                "{}: verification scope",
                rule.id
            );
        }
    }
}
