//! Synthetic HTTP replies at the proxy boundary, with the actual production
//! executables on stdio. No fake adapter, account request or downloaded payload.
use base64::Engine;
use serde_json::{Value, json};
use std::{
    io::BufReader,
    path::PathBuf,
    process::{Child, Command, Stdio},
};
use trace_contracts::wire;

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
    fn call(&mut self, method: &str, params: Value, mut proxy: impl FnMut(&str) -> Value) -> Value {
        wire::write_frame(&mut self.input,&json!({"protocol_version":trace_domain::PLUGIN_PROTOCOL_VERSION,"request_id":"test","method":method,"params":params,"deadline_ms":15000})).unwrap();
        for _ in 0..32 {
            let frame: Value = wire::read_frame(&mut self.out).unwrap();
            if matches!(
                frame["method"].as_str(),
                Some("proxy.fetch" | "proxy.fetch_large")
            ) {
                let url = frame["params"]["url"].as_str().unwrap();
                assert!(url.starts_with("https://"));
                if method == "resolve_source"
                    && url == "https://api.bilibili.com/x/web-interface/nav"
                {
                    assert_eq!(frame["params"]["with_credential"], true);
                }
                let body = proxy(url).to_string();
                wire::write_frame(&mut self.input,&json!({
                    "request_id": frame["request_id"],
                    "ok": true,
                    "result": {"status":200, "headers":{}, "body_b64":base64::engine::general_purpose::STANDARD.encode(body), "final_url":url},
                })).unwrap();
            } else {
                assert_eq!(frame["request_id"], "test");
                return frame;
            }
        }
        panic!("adapter exceeded proxy call budget")
    }
}
impl Drop for Adapter {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

#[test]
fn production_live_links_resolve_alias_room_owner_and_explicit_streams() {
    for site in ["bilibili", "douyin"] {
        let mut adapter = Adapter::new(site);
        let url = if site == "bilibili" {
            "https://live.bilibili.com/6"
        } else {
            "https://live.douyin.com/77"
        };
        let result=adapter.call("resolve_source",json!({"input":url}),|raw|{
            let url=url::Url::parse(raw).unwrap();match url.path(){
                "/x/web-interface/nav"=>serde_json::from_str(include_str!("../../fixtures/bilibili/nav.json")).unwrap(),
                "/xlive/web-room/v1/index/getInfoByRoom"=>json!({"code":0,"data":{"room_info":{"room_id":700,"uid":12,"live_status":1,"live_time":123,"title":"合成直播"},"anchor_info":{"base_info":{"uname":"合成主播"}}}}),
                "/xlive/web-room/v2/index/getRoomPlayInfo"=>{assert_eq!(url.query_pairs().find(|(k,_)|k=="room_id").unwrap().1,"700");json!({
                    "code": 0,
                    "data": {"playurl_info":{"playurl":{"stream":[{"format":[{"format_name":"flv", "codec":[{"codec_name":"avc", "current_qn":10000, "base_url":"/stream.flv", "url_info":[{"host":"https://fixture.bilivideo.com", "extra":"?token=ephemeral"}]}]}]}]}}},
                })},
                "/webcast/room/web/enter/"=>{assert_eq!(url.query_pairs().find(|(k,_)|k=="web_rid").unwrap().1,"77");json!({
                    "status_code": 0,
                    "data": {"data":[{"id_str":"90000000000000001", "status":2, "title":"合成直播", "owner":{"sec_uid":"MS4LiveOwner", "nickname":"合成主播"}, "stream_url":{"flv_pull_url":{"FULL_HD1":"http://fixture.douyinliving.com/live.flv?token=ephemeral"}}}]},
                })},
                _=>panic!("link resolution must not enumerate an author"),
            }
        });
        assert_eq!(result["ok"], true, "{result}");
        let value = &result["result"];
        assert_eq!(value["kind"], "live");
        assert_ne!(
            value["native_account_key"],
            if site == "bilibili" {
                json!("6")
            } else {
                json!("77")
            }
        );
        assert_eq!(value["live"]["state"], "live");
        assert!(
            value["live"]["streams"][0]["url"]
                .as_str()
                .unwrap()
                .starts_with("https://")
        );
        assert!(!value["live"]["metadata"].to_string().contains("ephemeral"));
        if site == "douyin" {
            let live = json!({"status_code": 0, "data": {
                "user": {"sec_uid": "MS4DifferentViewer"},
                "data": [{
                    "id_str": "90000000000000001",
                    "status": 2,
                    "title": "合成直播",
                    "owner": {"sec_uid": "", "id_str": "12345", "nickname": "原主播"},
                    "stream_url": {"flv_pull_url": {"FULL_HD1": "https://fixture.douyinliving.com/live.flv"}},
                }],
            }});
            let owner = adapter.call("resolve_source", json!({"input": url}), |_| live.clone());
            assert_eq!(owner["ok"], true);
            assert_eq!(owner["result"]["native_account_key"], "12345");
            assert_eq!(owner["result"]["live"]["state"], "live");
            assert_ne!(owner["result"]["native_account_key"], "MS4DifferentViewer");
            let offline = adapter.call("resolve_source", json!({"input":url}), |_| {
                json!({"status_code":0,"data":{"user":{"sec_uid":"MS4DifferentViewer"},"data":[{"status":4}]}})
            });
            assert_eq!(offline["ok"], false);
            assert_eq!(offline["error"]["class"], "source_unavailable");
            assert_eq!(offline["error"]["details"]["source_state"], "offline");
        }
        for input in [
            "https://unknown.invalid/video/1",
            "https://live.bilibili.com.evil.invalid/6",
            "https://live.douyin.com/77/extra",
        ] {
            let result = adapter.call("resolve_source", json!({"input":input}), |_| {
                panic!("invalid link sent network request")
            });
            assert_eq!(result["ok"], false);
        }
    }
}

#[test]
fn douyin_auth_probe_requires_self_profile_not_guest_uid() {
    let mut adapter = Adapter::new("douyin");
    for (response, expected) in [
        (
            json!({"status_code":0,"user":{"uid":"90071992547409931","sec_uid":"MS4synthetic","nickname":"private-auth-sentinel"}}),
            "valid",
        ),
        (
            json!({"status_code":0,"user_uid":"90071992547409931","is_login":true}),
            "unknown",
        ),
        (
            json!({"status_code":0,"user":{"uid":"0","sec_uid":"MS4synthetic"}}),
            "unknown",
        ),
        (
            json!({"status_code":0,"user":{"uid":"90071992547409931"}}),
            "unknown",
        ),
    ] {
        let reply = adapter.call("probe_auth", json!({}), |url| {
            assert_eq!(
                url::Url::parse(url).unwrap().path(),
                "/aweme/v1/web/user/profile/self/",
                "query/user can identify a guest and cannot prove an authenticated session"
            );
            response.clone()
        });
        assert_eq!(reply["ok"], true);
        assert_eq!(reply["result"]["state"], expected);
        assert_eq!(
            reply["result"]["capability"]["login_proven"],
            expected == "valid"
        );
        assert!(!reply.to_string().contains("private-auth-sentinel"));
        assert!(!reply.to_string().contains("90071992547409931"));
    }
    let denied = adapter.call("probe_auth", json!({}), |_| json!({"status_code":8}));
    assert_eq!(denied["ok"], false);
    assert_eq!(denied["error"]["class"], "auth_required");
}

#[test]
fn site_check_dry_run_does_not_read_credentials_or_create_output() {
    let dir = tempfile::tempdir().unwrap();
    let spec = dir.path().join("request.json");
    let output = dir.path().join("output");
    let daemon =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug/trace-daemon.exe");
    for method in ["observe", "publish"] {
        std::fs::write(&spec,json!({
            "platform": "douyin",
            "max_http_requests": 5,
            "deadline_seconds": 30,
            "min_interval_ms": 1000,
            "credential_file": dir.path().join("does-not-exist.secret"),
            "requests": [{"method":method, "params":{"native_kind":"article", "native_entry_key":"7"}}],
        }).to_string()).unwrap();
        let result = Command::new(&daemon)
            .arg("site-check")
            .arg("--request-file")
            .arg(&spec)
            .arg("--output")
            .arg(&output)
            .output()
            .unwrap();
        assert_eq!(result.status.success(), method == "observe");
        if method == "observe" {
            assert_eq!(
                serde_json::from_slice::<Value>(&result.stdout).unwrap()["status"],
                "not-run"
            );
        }
        assert!(!output.exists());
        assert!(!String::from_utf8_lossy(&result.stdout).contains("does-not-exist.secret"));
    }
}

#[test]
fn bilibili_music_cards_require_full_audio_and_preserve_parent_on_trial() {
    let mut adapter = Adapter::new("bilibili");
    for trial in [false, true] {
        let mut seen = Vec::new();
        let result=adapter.call("observe",json!({"native_entry_key":"42","native_kind":"dynamic"}),|url| {
            let u=url::Url::parse(url).unwrap();seen.push(u.path().to_owned());
            let data=match u.path() {
                "/x/polymer/web-dynamic/v1/detail"=>json!({"item":{"id_str":"42","modules":{"module_dynamic":{"desc":{"text":"发布正文"},"major":{"music":{"id":77,"title":"音乐卡"}}}}}}),
                "/audio/music-service-c/web/song/info"=>json!({"id":77,"title":"完整音乐","intro":"歌曲介绍","cover":"https://i0.hdslb.com/cover.jpg"}),
                "/audio/music-service-c/web/url"=>json!({"sid":77,"type":if trial{-1}else{1},"size":500,"cdns":["https://cdn.bilivideo.com/song.m4a?sign=PRIVATE_SONG_URL"]}),
                _=>panic!("unexpected music endpoint"),
            };json!({"code":0,"data":data})
        });
        assert_eq!(result["ok"], true, "{result}");
        assert_eq!(seen.len(), 3);
        assert!(
            result["result"]["normalized_body"]
                .as_str()
                .unwrap()
                .contains("发布正文")
        );
        assert_eq!(
            result["result"]["completeness"],
            if trial { "summary" } else { "full" }
        );
        assert!(!result.to_string().contains("PRIVATE_SONG_URL"));
        let resources = result["result"]["resources"].as_array().unwrap();
        assert_eq!(
            resources
                .iter()
                .filter(|r| r["role"] == "music" && r["required"] == true)
                .count(),
            usize::from(!trial)
        );
        if !trial {
            assert!(
                result["result"]["document"]["blocks"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .any(|b| b["attachment_id"] == "related:music:77:au:77")
            );
        }
    }
}

#[test]
fn collection_enumeration_and_c04_live_auxiliary_through_production_processes() {
    let mut douyin = Adapter::new("douyin");
    let mut paths = Vec::new();
    let mut cursor = Value::Null;
    let mut found = Vec::new();
    for _ in 0..6 {
        let page=douyin.call("enumerate",json!({"native_account_key":"MS4fixture","content_type":"all","mode":"history","cursor":cursor}),|url| {
            let u=url::Url::parse(url).unwrap();paths.push(u.path().to_owned());
            assert!(u.query_pairs().any(|(key,_)|key=="a_bogus"));
            match u.path() {
                "/aweme/v1/web/aweme/post/"=>json!({"status_code":0,"aweme_list":[],"has_more":false,"max_cursor":0}),
                "/aweme/v1/web/mix/list/"=>json!({"status_code":0,"mix_infos":[{"mix_id":"90071992547409931"}],"has_more":false,"cursor":0}),
                "/aweme/v1/web/series/list/"=>json!({"status_code":0,"series_infos":[],"has_more":false,"cursor":0}),
                "/aweme/v1/web/mix/aweme/"=>{
                    assert!(u.query_pairs().any(|(key,value)|key=="mix_id"&&value=="90071992547409931"));
                    json!({"status_code":0,"aweme_list":[{"aweme_id":"77","aweme_type":163,"desc":"合集中的长文"}],"has_more":false,"cursor":1})
                },
                _=>panic!("unexpected collection endpoint"),
            }
        });
        assert_eq!(page["ok"], true, "{page}");
        found.extend(page["result"]["items"].as_array().unwrap().clone());
        cursor = page["result"]["next_cursor"].clone();
        if page["result"]["has_more"] == false {
            break;
        }
    }
    assert_eq!(paths.len(), 4);
    assert_eq!(found.len(), 1);
    assert_eq!(found[0]["native_kind"], "article");
    assert!(cursor.is_null());
    let chat=douyin.call("sample_live_auxiliary",json!({"room_key":"7"}),|url| {
        let u=url::Url::parse(url).unwrap();
        if u.path()=="/aweme/v1/web/query/user/" {return json!({"status_code":0,"id":"90071992547409932","is_login":false})}
        assert_eq!(u.host_str(),Some("live.douyin.com"));assert_eq!(u.path(),"/webcast/im/fetch/");
        assert!(u.query_pairs().any(|(key,value)|key=="resp_content_type"&&value=="json"));
        json!({"status_code":0,"data":[{"common":{"method":"WebcastChatMessage","room_id":"7","msg_id":"90071992547409931"},"content":"弹幕","user":{"nickname":"甲"}}],"internal_ext":"PRIVATE_DEVICE_SENTINEL"})
    });
    assert_eq!(chat["ok"], true);
    assert_eq!(chat["result"]["coverage"], "sampled_history");
    assert!(!chat.to_string().contains("PRIVATE_DEVICE_SENTINEL"));
    let mut bili = Adapter::new("bilibili");
    let chat=bili.call("sample_live_auxiliary",json!({"room_key":"7"}),|url| {
        assert!(url.contains("/xlive/web-room/v1/dM/gethistory?roomid=7"));
        json!({"code":0,"data":{"room":[{"id_str":"90071992547409931","text":"弹幕","nickname":"乙"}],"admin":[]}})
    });
    assert_eq!(chat["ok"], true);
    assert_eq!(
        chat["result"]["messages"][0]["source_id"],
        "90071992547409931"
    );
}

#[test]
fn douyin_article_account_to_full_document_through_production_process() {
    let mut a = Adapter::new("douyin");
    let key = "90071992547409931";
    let mut proxy = |url: &str| {
        assert!(url.contains("a_bogus="));
        if url.contains("user/profile/other") {
            return json!({"status_code":0,"user":{"sec_uid":"MS4fixture","uid":"90071992547409932","nickname":"测试作者"}});
        }
        if url.contains("aweme/post") {
            return json!({"status_code":0,"aweme_list":[{"aweme_id":key,"aweme_type":163,"desc":"499字以下摘要"}],"has_more":false,"max_cursor":0});
        }
        assert!(url.contains("aweme/detail"));
        json!({
            "status_code": 0,
            "aweme_detail": {
                "aweme_id": key,
                "aweme_type": 163,
                "article_info": {"article_title":"完整长文", "article_content":{"markdown":format!("{}\n\n{}", "正文".repeat(5000), (0..30).map(|n|format!("![图{n}](https://example.douyinpic.com/{n}.jpg)\n")).collect::<String>())}, "has_more":false},
                "desc": "简介",
                "statistics": {"digg_count":7},
            },
        })
    };
    let account = a.call("resolve_account", json!({"input":"MS4fixture"}), &mut proxy);
    assert_eq!(account["result"]["native_account_key"], "MS4fixture");
    let page = a.call(
        "enumerate",
        json!({"native_account_key":"MS4fixture","content_type":"article","mode":"incremental"}),
        &mut proxy,
    );
    assert_eq!(page["result"]["items"][0]["native_kind"], "article");
    let detail = a.call(
        "observe",
        json!({"native_entry_key":key,"native_kind":"article"}),
        &mut proxy,
    );
    assert_eq!(detail["ok"], true);
    assert_eq!(detail["result"]["completeness"], "full");
    assert!(detail["result"]["normalized_body"].as_str().unwrap().len() > 10000);
    assert_eq!(detail["result"]["resources"].as_array().unwrap().len(), 30);
    let plans=a.call("resolve_media",json!({"native_entry_key":key,"native_kind":"article","resources":detail["result"]["resources"]}),&mut proxy);
    assert_eq!(plans["result"]["candidates"].as_array().unwrap().len(), 30);
    let failure = a.call(
        "observe",
        json!({"native_entry_key":key,"native_kind":"article"}),
        |_| json!({"status_code":0,"verify_data":{"token":"private-sentinel"}}),
    );
    assert_eq!(failure["ok"], false);
    assert!(!failure.to_string().contains("private-sentinel"));
}

#[test]
fn article_ops_av_mapping_and_opus_formula_list_production() {
    let mut a = Adapter::new("bilibili");
    for mismatch in [false, true] {
        let mut av_requested = false;
        let result=a.call("observe",json!({"native_entry_key":"7","native_kind":"article"}),|raw| {
            let u=url::Url::parse(raw).unwrap();
            match u.path() {
                "/x/article/view" => json!({"code":0,"data":{"id":7,"content":json!({"ops":[
                    {"insert":"标题"},{"insert":"\n","attribute":{"header":2}},
                    {"insert":{"native-image":{"url":"https://i0.hdslb.com/full.png","alt":"原图"}}},
                    {"insert":{"video-card":{"id":"av17","alt":"引用视频","url":"https://i0.hdslb.com/card.png"}}},
                    {"insert":"末段\n"}]}).to_string()}}),
                "/x/web-interface/nav"=>json!({"code":0,"data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}),
                "/x/web-interface/view"|"/x/web-interface/wbi/view"=>{
                    av_requested=true;
                    assert!(u.query_pairs().any(|(k,v)|k=="aid"&&v=="17"));
                    assert!(!u.query_pairs().any(|(k,_)|k=="bvid"));
                    json!({"code":0,"data":{"bvid":"BVfixture","aid":if mismatch {18}else{17},"title":"视频","desc":"卡内完整说明","pages":[{"cid":31,"page":1,"part":"P1"}]}})
                },
                "/x/player/wbi/v2"=>json!({"code":0,"data":{}}),
                "/x/player/playurl"|"/x/player/wbi/playurl"=>{
                    assert!(u.query_pairs().any(|(k,v)|k=="bvid"&&v=="BVfixture"));
                    json!({"code":0,"data":{"durl":[{"order":1,"size":10,"length":2000,"url":"https://cdn.bilivideo.com/fixture.mp4"}]}})
                },
                _=>panic!("unexpected article dependency endpoint: {}",u.path()),
            }
        });
        assert_eq!(result["ok"], true, "{result}");
        assert!(av_requested);
        let d = &result["result"];
        assert_eq!(d["completeness"], if mismatch { "summary" } else { "full" });
        let blocks = d["document"]["blocks"].as_array().unwrap();
        assert_eq!(blocks[0]["style"], "h2");
        assert_eq!(blocks.last().unwrap()["spans"][0]["text"], "末段");
        assert_eq!(
            d["resources"]
                .as_array()
                .unwrap()
                .iter()
                .filter(|r| r["role"] == "image")
                .count(),
            1
        );
        if !mismatch {
            assert!(blocks.iter().any(|b| {
                b["attachment_id"]
                    .as_str()
                    .is_some_and(|s| s.starts_with("related:video:BVfixture:"))
            }));
            assert!(
                d["normalized_body"]
                    .as_str()
                    .unwrap()
                    .contains("卡内完整说明")
            );
            assert_eq!(
                d["metadata"]["related_content"][0]["source"]["identity_mapping"]["aid"],
                "17"
            );
        } else {
            assert!(
                !d["normalized_body"]
                    .as_str()
                    .unwrap()
                    .contains("卡内完整说明")
            );
        }
    }
    let d=a.call("observe",json!({"native_entry_key":"9","native_kind":"opus"}),|_|json!({"code":0,"data":{"item":{"id_str":"9","modules":[{"module_content":{"paragraphs":[
        {"para_type":1,"text":{"nodes":[{"formula":{"latex_content":"E=mc^2"}}]}},
        {"para_type":5,"list":{"style":1,"items":[{"level":2,"order":7,"nodes":[{"word":{"words":"第七项"}}]}]}}
    ]}}]}}}));
    assert_eq!(d["ok"], true);
    let blocks = &d["result"]["document"]["blocks"];
    assert_eq!(blocks[0]["text"], "E=mc^2");
    assert_eq!(blocks[1]["depth"], 2);
    assert_eq!(blocks[1]["ordinal"], 7);
}

#[test]
fn bilibili_article_full_body_and_legacy_protocol_defaults() {
    let mut a = Adapter::new("bilibili");
    let detail=a.call("observe",json!({"native_entry_key":"7","native_kind":"article"}),|url|{assert!(url.contains("/x/article/view?id=7"));json!({"code":0,"data":{"id":7,"title":"专栏","content":"<h1>正文</h1><p>离线保留</p><img src='https://i0.hdslb.com/full.png'><script>doNotExecute()</script>"}})});
    assert_eq!(detail["ok"], true);
    assert_eq!(detail["result"]["resources"].as_array().unwrap().len(), 1);
    assert!(
        !detail["result"]["normalized_body"]
            .as_str()
            .unwrap()
            .contains("doNotExecute")
    );
    let old: trace_contracts::plugin::ObserveParams =
        serde_json::from_value(json!({"native_entry_key":"BVtest"})).unwrap();
    assert_eq!(old.native_kind, "video");
}

#[test]
fn bilibili_article_pictures_share_one_read_of_the_content() {
    let mut a = Adapter::new("bilibili");
    let reads = std::cell::Cell::new(0);
    let article = |url: &str| {
        assert!(
            url.contains("/x/article/view?id=7"),
            "unexpected route {url}"
        );
        reads.set(reads.get() + 1);
        json!({"code":0,"data":{"id":7,"title":"专栏","content":"<p>正文</p><img src='https://i0.hdslb.com/a.png'><img src='https://i0.hdslb.com/b.png'><img src='https://i0.hdslb.com/c.png'>"}})
    };
    let detail = a.call(
        "observe",
        json!({"native_entry_key":"7","native_kind":"article"}),
        article,
    );
    assert_eq!(detail["ok"], true, "{detail}");
    let resources = detail["result"]["resources"].as_array().unwrap().clone();
    assert_eq!(resources.len(), 3);
    for resource in &resources {
        let media = a.call(
            "resolve_media",
            json!({"native_entry_key":"7","native_kind":"article","resources":[resource],"quality_hint":{}}),
            article,
        );
        assert_eq!(media["ok"], true, "{media}");
        assert_eq!(media["result"]["candidates"].as_array().unwrap().len(), 1);
    }
    assert_eq!(
        reads.get(),
        1,
        "three pictures cost no further read of the article"
    );
    let again = a.call(
        "observe",
        json!({"native_entry_key":"7","native_kind":"article"}),
        article,
    );
    assert_eq!(again["ok"], true);
    assert_eq!(reads.get(), 2, "an observation is always a fresh read");
}

#[test]
fn bilibili_opus_video_cards_subtitles_and_failed_repost_preserve_parent() {
    let mut a = Adapter::new("bilibili");
    let mut proxy = |url: &str| {
        if url.contains("/nav") {
            return json!({"code":0,"data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}});
        }
        if url.contains("/opus/detail") {
            return json!({
                "code": 0,
                "data": {
                    "item": {
                        "id_str": "9",
                        "modules": [
                            {
                                "module_content": {
                                    "paragraphs": [
                                        {"para_type":1, "text":{"nodes":[{"word":{"words":"完整父正文"}}]}},
                                        {"para_type":6, "link_card":{"card":{"type":"LINK_CARD_TYPE_UGC", "ugc":{"bvid":"BVfixture", "title":"视频引用"}}}},
                                        {"para_type":1, "text":{"nodes":[{"word":{"words":"引用后的正文"}}]}},
                                    ],
                                },
                            },
                        ],
                    },
                },
            });
        }
        if url.contains("/web-dynamic/v1/detail") {
            return json!({"code":-404,"message":"private-sentinel"});
        }
        if url.contains("/player/wbi/v2") {
            return json!({"code":0,"data":{"cid":31,"subtitle":{"subtitles":[{"id_str":"99999999999999991","lan":"zh-CN","lan_doc":"中文","subtitle_url":"//aisubtitle.hdslb.com/fixture.json?auth_key=transient"}]},"view_points":[{"from":0,"to":2,"content":"章节"}]}});
        }
        if url.contains("/playurl") {
            return json!({"code":0,"data":{"durl":[{"order":1,"size":10,"length":2000,"url":"https://cdn.bilivideo.com/fixture.mp4"}]}});
        }
        assert!(url.contains("/view"), "unexpected metadata route");
        json!({"code":0,"data":{"bvid":"BVfixture","aid":17,"title":"视频全文","desc":"视频说明","pages":[{"cid":31,"page":1,"part":"P1"}]}})
    };
    let d = a.call(
        "observe",
        json!({"native_entry_key":"9","native_kind":"opus"}),
        &mut proxy,
    );
    assert_eq!(d["ok"], true, "{d}");
    assert_eq!(d["result"]["completeness"], "full");
    let blocks = d["result"]["document"]["blocks"].as_array().unwrap();
    assert_eq!(blocks.first().unwrap()["text"], "完整父正文");
    assert_eq!(blocks.last().unwrap()["text"], "引用后的正文");
    assert!(
        blocks
            .iter()
            .any(|b| b["text"].as_str().is_some_and(|t| t.contains("视频说明"))),
        "referenced video prose must survive offline rendering"
    );
    assert!(
        d["result"]["resources"]
            .as_array()
            .unwrap()
            .iter()
            .any(|r| r["role"] == "subtitle")
    );
    assert!(!d.to_string().contains("transient"));
    let media = a.call(
        "resolve_media",
        json!({"native_entry_key":"9","native_kind":"opus","resources":d["result"]["resources"]}),
        &mut proxy,
    );
    assert!(
        media["result"]["candidates"]
            .as_array()
            .unwrap()
            .iter()
            .any(|c| c["kind"] == "subtitle_json")
    );
    let d=a.call("observe",json!({"native_entry_key":"10","native_kind":"dynamic"}),|url|{
        if url.contains("/detail?id=10") {json!({
            "code": 0,
            "data": {"item":{"id_str":"10", "modules":{"module_dynamic":{"desc":{"text":"父动态仍可读"}, "major":{"archive":{"bvid":"BVdeleted"}}}}, "orig":{"id_str":"11"}}},
        })}else{json!({"code":-404,"message":"private-sentinel"})}
    });
    assert_eq!(d["ok"], true, "{d}");
    assert_eq!(d["result"]["completeness"], "summary");
    assert!(
        d["result"]["normalized_body"]
            .as_str()
            .unwrap()
            .contains("父动态仍可读")
    );
    assert_eq!(
        d["result"]["metadata"]["related_unavailable"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    assert!(!d.to_string().contains("private-sentinel"));
}

#[test]
fn bilibili_collection_membership_and_douyin_motion_production_mapping() {
    let mut b = Adapter::new("bilibili");
    let d=b.call("observe",json!({"native_entry_key":"7","native_kind":"article"}),|url|{
        if url.contains("/list/web/articles") {json!({
            "code": 0,
            "data": {"list":{"id":12, "name":"文集"}, "articles":[{"id":7, "title":"文章"}, {"id":8, "title":"下一篇"}]},
        })}else{json!({"code":0,"data":{"id":7,"title":"文章","content":"<p>完整文章</p>","list":{"id":12}}})}
    });
    assert_eq!(
        d["result"]["metadata"]["collection"]["data"]["articles"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    let mut a = Adapter::new("douyin");
    let d=a.call("observe",json!({
        "native_entry_key": "5",
        "native_kind": "image",
    }),|_|json!({"status_code":0,"aweme_detail":{"aweme_id":"5","aweme_type":68,"images":[{"uri":"image-0","width":3000,"height":2000,"url_list":["https://cdn.douyinpic.com/full.jpg"],"video":{"play_addr":{"url_list":["https://cdn.douyinvod.com/live.mp4"]}}}],"mix_info":{"mix_id":"991","mix_name":"系列"}}}));
    assert_eq!(d["result"]["resources"].as_array().unwrap().len(), 2);
    assert_eq!(d["result"]["resources"][1]["role"], "motion");
    assert_eq!(d["result"]["metadata"]["mix_info"]["mix_id"], "991");
}
