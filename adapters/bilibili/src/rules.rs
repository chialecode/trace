//! What the Bilibili adapter says it can subscribe to, preview and download, and how far
//! each rule has been checked against the real site.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleObject, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

const SPACE: &str = "space.bilibili.com";
const LOGIN: &str = "不登录可读公开内容；登录后可取得更高画质与登录可见内容";
const SYNTHETIC: &str = "只经合成夹具验证，没有真实站点验证";
const CHALLENGE: &str = "遇到风控校验（-352 等）即停止，不前移游标，不当作“没有更新”";

/// One lane of an author's space: its tab, and where it has been checked.
struct Lane {
    lane: &'static str,
    id: &'static str,
    tab: &'static [&'static str],
    title: &'static str,
    what: &'static str,
    kinds: &'static [&'static str],
    note: &'static str,
}

const LANES: [Lane; 4] = [
    Lane {
        lane: "video",
        id: "videos",
        tab: &["video", "upload/video"],
        title: "投稿视频",
        what: "作者空间的投稿视频，含多 P、字幕与章节",
        kinds: &["video"],
        note: "列表带浏览器会附加的校验参数（dm_img_*、w_webid）；只取公开可见的投稿",
    },
    Lane {
        lane: "dynamic",
        id: "dynamics",
        tab: &["dynamic"],
        title: "动态",
        what: "作者的动态：文字、图文、转发与投稿卡片",
        kinds: &["text", "image", "video", "article"],
        note: "转发是独立发布，原文失败时保留转发正文；特殊卡片只保存安全文字并标明缺口",
    },
    Lane {
        lane: "image",
        id: "opus",
        tab: &["opus"],
        title: "图文",
        what: "作者的图文（opus）：有序文本、图片、列表与引用",
        kinds: &["text", "image"],
        note: "只有动态响应明示 opus 才建立与动态的对应，不按数字相等合并",
    },
    Lane {
        lane: "article",
        id: "articles",
        tab: &["article"],
        title: "专栏",
        what: "作者的专栏文章与文集：全文有序保存",
        kinds: &["text", "image", "article"],
        note: "未知的富文本节点明确标为不完整，不冒充全文",
    },
];

fn accounts(mut b: RuleBuilder, tabs: &[&str]) -> RuleBuilder {
    b = b.account(SPACE, "/{mid}");
    for tab in tabs {
        b = b.account(SPACE, &format!("/{{mid}}/{tab}"));
    }
    b
}

fn unverified(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial).limit(SYNTHETIC)
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let mut out = Vec::new();
    for lane in LANES {
        let base = |entry, id: String, title: String, what: &str| {
            accounts(
                rule("bilibili", &id, entry, &title, what)
                    .kinds(lane.kinds)
                    .lane(lane.lane)
                    .login(RuleLogin::Optional, LOGIN),
                lane.tab,
            )
            .limit(lane.note)
            .limit(CHALLENGE)
        };
        let subscription = base(
            RuleEntry::Subscription,
            format!("bilibili.user.{}", lane.id),
            format!("作者{}", lane.title),
            lane.what,
        );
        let preview = base(
            RuleEntry::Preview,
            format!("bilibili.user.preview_{}", lane.id),
            format!("作者{}预览", lane.title),
            "分页浏览后勾选下载",
        );
        out.push(if lane.lane == "video" {
            subscription
                .verified(
                    "2026-10-15",
                    "登录态一位作者的视频列表 2 页（各 30 条）：签名请求 200、页码前进、两页无重叠；0 媒体字节",
                )
                .status(RuleStatus::Partial)
                .limit("只验证了前两页；全部历史与匿名差异没有验证")
                .limit("2026-10-18 的增量检查在额度用尽时中止：自动下载关的检查找到了 1 条作品、第二次检查没有变化，但检查途中额度用完，自动下载开的检查没有执行")
                .limit("订阅的增量检查（自动下载关与开各一次）没有真实结果，按计划口径保持部分实现")
                .build()
        } else {
            unverified(subscription).build()
        });
        out.push(unverified(preview).build());
    }
    out.push(
        unverified(
            accounts(
                rule(
                    "bilibili",
                    "bilibili.user.live",
                    RuleEntry::Subscription,
                    "作者直播",
                    "作者开播时录制，按封片保存",
                )
                .kinds(&["live"])
                .lane("live")
                .login(RuleLogin::Optional, LOGIN),
                &[],
            )
            .limit("订阅入口没有真实验证；直接的直播间链接已真实验证（见 bilibili.live.link）"),
        )
        .build(),
    );
    out.push(
        rule(
            "bilibili",
            "bilibili.video.link",
            RuleEntry::Link,
            "视频链接",
            "单个视频：显示可得的画质与编码，勾选后下载，音轨一并保存",
        )
        .kinds(&["video"])
        .work("bilibili.com", "/video/{id}")
        .short("b23.tv", RuleObject::Entry)
        .login(RuleLogin::Optional, LOGIN)
        .limit("真实验证只覆盖一个指定视频；其他类型、匿名与会员的画质差异没有验证")
        .verified(
            "2026-10-13",
            "一个指定视频的整条链路：3840×2160 AVC 加 AAC，218.560 秒，选择、下载、离线读取、完整解码",
        )
        .build(),
    );
    out.push(
        rule(
            "bilibili",
            "bilibili.live.link",
            RuleEntry::Link,
            "直播间链接",
            "单个直播间：选择流后按封片录制",
        )
        .kinds(&["live"])
        .work("live.bilibili.com", "/{room}")
        .login(RuleLogin::Optional, LOGIN)
        .limit("真实验证只覆盖一个指定直播间；关闭窗口后继续录制与封片已核对，长时间录制没有验证")
        .verified(
            "2026-10-13",
            "一个指定直播间：1920×1080 AVC 加 AAC，封片 68.869 秒，关闭界面后继续录制、导出与离线重开",
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
                .normalized("bilibili")
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
                ("bilibili.video.link".to_owned(), "2026-10-13".to_owned()),
                ("bilibili.live.link".to_owned(), "2026-10-13".to_owned()),
            ]
        );
    }
}
