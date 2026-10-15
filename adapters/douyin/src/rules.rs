//! What the Douyin adapter says it can subscribe to, preview and download, and how far each
//! rule has been checked against the real site.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleObject, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

const LOGIN: &str = "不登录可读部分公开内容；登录后列表与画质更完整";
const SYNTHETIC: &str = "只经合成夹具验证，没有真实站点验证";
const CHALLENGE: &str = "遇到风控或验证即停止，不前移游标，不当作“没有更新”";

/// One lane of an author's posts and where it has been checked.
struct Lane {
    lane: &'static str,
    id: &'static str,
    title: &'static str,
    what: &'static str,
    kinds: &'static [&'static str],
    note: &'static str,
}

const LANES: [Lane; 4] = [
    Lane {
        lane: "video",
        id: "videos",
        title: "投稿视频",
        what: "作者的投稿视频",
        kinds: &["video"],
        note: "图集、长图文与其它类型的投稿在这个通道里被筛除",
    },
    Lane {
        lane: "image",
        id: "images",
        title: "图集",
        what: "作者的图集与实况图：原图、动态片段与配乐",
        kinds: &["image", "live_photo", "audio"],
        note: "优先保存原始静态图；缺图保留占位，封面不冒充实况",
    },
    Lane {
        lane: "article",
        id: "articles",
        title: "长图文",
        what: "作者的长图文：全文有序保存，含图片与表格",
        kinds: &["text", "image", "article"],
        note: "列表只给截断的摘要，必须请求详情才算归档完成",
    },
    Lane {
        lane: "dynamic",
        id: "dynamics",
        title: "全部投稿",
        what: "作者的全部投稿：视频、图集、长图文与合集成员",
        kinds: &["text", "image", "live_photo", "video", "article"],
        note: "合集与系列分别续页；空页不能代表全量结束",
    },
];

fn accounts(b: RuleBuilder) -> RuleBuilder {
    b.account("www.douyin.com", "/user/{id}")
        .short("v.douyin.com", RuleObject::Account)
}

fn unverified(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial).limit(SYNTHETIC)
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let mut out = Vec::new();
    for lane in LANES {
        let base = |entry, id: String, title: String, what: &str| {
            accounts(
                rule("douyin", &id, entry, &title, what)
                    .kinds(lane.kinds)
                    .lane(lane.lane)
                    .login(RuleLogin::Optional, LOGIN),
            )
            .limit(lane.note)
            .limit(CHALLENGE)
        };
        let subscription = base(
            RuleEntry::Subscription,
            format!("douyin.user.{}", lane.id),
            format!("作者{}", lane.title),
            lane.what,
        );
        let preview = base(
            RuleEntry::Preview,
            format!("douyin.user.preview_{}", lane.id),
            format!("作者{}预览", lane.title),
            "分页浏览后勾选下载",
        );
        out.push(if lane.lane == "video" {
            subscription
                .verified(
                    "2026-10-15",
                    "登录态一位作者的投稿 2 页（各 20 条，第 2 页 19 条为视频）：游标前进、仍有更多、两页无重叠；0 媒体字节",
                )
                .status(RuleStatus::Partial)
                .limit("只验证了前两页；全部历史与匿名差异没有验证")
                .limit("2026-10-17 与 2026-10-18 的增量检查都在额度用尽时中止：自动下载关的检查找到了 1 条作品、第二次检查没有变化，但检查途中额度用完，自动下载开的检查没有执行")
                .limit("订阅的增量检查（自动下载关与开各一次）没有真实结果，按计划口径保持部分实现")
                .build()
        } else {
            unverified(subscription).build()
        });
        out.push(if lane.lane == "video" {
            preview
                .verified(
                    "2026-10-17",
                    "登录态一位作者的投稿列表 2 页（19 条视频、有更多），勾选 1 条下载到文件夹：1 个文件，约 2.4 MB，已导出",
                )
                .limit("只验证了前两页和一条的下载；全部历史与匿名差异没有验证")
                .build()
        } else {
            unverified(preview).build()
        });
    }
    out.push(
        unverified(
            accounts(
                rule(
                    "douyin",
                    "douyin.user.live",
                    RuleEntry::Subscription,
                    "作者直播",
                    "作者开播时录制，按封片保存",
                )
                .kinds(&["live"])
                .lane("live")
                .login(RuleLogin::Optional, LOGIN),
            )
            .limit("订阅入口没有真实验证；直接的直播间链接已真实验证（见 douyin.live.link）"),
        )
        .build(),
    );
    out.push(
        rule(
            "douyin",
            "douyin.video.link",
            RuleEntry::Link,
            "视频链接",
            "单个视频：显示当前来源给出的候选，勾选后下载",
        )
        .kinds(&["video"])
        .work("www.douyin.com", "/video/{id}")
        .work("www.iesdouyin.com", "/video/{id}")
        .short("v.douyin.com", RuleObject::Entry)
        .login(RuleLogin::Optional, LOGIN)
        .limit("真实验证只覆盖一个指定视频；地址名不证明无水印或原画，实际规格以保存后的探测为准")
        .verified(
            "2026-10-13",
            "一个指定视频的整条链路：1080×1920 AVC 加 AAC，22.833 秒，选择、下载、离线读取、完整解码",
        )
        .build(),
    );
    out.push(
        rule(
            "douyin",
            "douyin.live.link",
            RuleEntry::Link,
            "直播间链接",
            "单个直播间：选择流后按封片录制",
        )
        .kinds(&["live"])
        .work("live.douyin.com", "/{room}")
        .login(RuleLogin::Optional, LOGIN)
        .limit("真实验证只覆盖一个指定直播间；已停播的房间明确显示离线，长时间录制没有验证")
        .verified(
            "2026-10-13",
            "一个指定直播间：1080×1920 AVC 加 AAC，封片 68.426 秒，关闭界面后继续录制、导出与离线重开",
        )
        .build(),
    );
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_rule_is_well_formed_for_the_catalog() {
        let rules = rules();
        assert_eq!(rules.len(), 11);
        for rule in rules.iter().cloned() {
            let id = rule.id.clone();
            let normalized = rule
                .clone()
                .normalized("douyin")
                .unwrap_or_else(|e| panic!("{id}: {e}"));
            assert_eq!(normalized.status, rule.status, "{id} keeps its status");
            if normalized.status == RuleStatus::Implemented {
                assert!(normalized.verified_at.is_some(), "{id} has a date");
            } else {
                assert!(!normalized.limits.is_empty(), "{id} says what is missing");
            }
        }
    }

    #[test]
    fn only_what_was_checked_on_the_real_site_is_implemented() {
        let implemented: Vec<_> = rules()
            .into_iter()
            .filter(|r| r.status == RuleStatus::Implemented)
            .map(|r| (r.id, r.verified_at.unwrap()))
            .collect();
        assert_eq!(
            implemented,
            [
                (
                    "douyin.user.preview_videos".to_owned(),
                    "2026-10-17".to_owned()
                ),
                ("douyin.video.link".to_owned(), "2026-10-13".to_owned()),
                ("douyin.live.link".to_owned(), "2026-10-13".to_owned()),
            ]
        );
    }
}
