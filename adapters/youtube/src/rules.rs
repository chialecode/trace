//! What the YouTube adapter says it can subscribe to, preview and download.
use crate::entry::{SHORT, VIDEO};
use crate::post::POST;
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

/// The channel link forms; `tab` is the segment that names this lane's tab.
fn channel(b: RuleBuilder, tab: &str) -> RuleBuilder {
    let mut b = b;
    for host in ["youtube.com"] {
        for base in ["/@{handle}", "/channel/{id}", "/c/{name}", "/user/{name}"] {
            b = b
                .account(host, base)
                .account(host, &format!("{base}/{tab}"));
        }
    }
    b
}

fn unverified(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial).limit("尚未经过真实站点验证")
}

const PUBLIC: &str = "只处理公开内容，不使用登录；会员专享与年龄限制内容不保存";
const DATED: &str = "列表只给“几天前”，按近似日期（带误差）判断范围；确切发布日在打开作品时取得";

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let mut out = channel_rules();
    out.extend(link_rules());
    out
}

/// The subscription and preview rule of every lane of a channel.
fn channel_rules() -> Vec<RuleDeclaration> {
    let lanes = [
        (
            VIDEO,
            "videos",
            "视频",
            "频道的长视频",
            &["video"][..],
            "视频文件由锁定版本的外部解析工具获取，工具缺失时只保存元数据",
        ),
        (
            SHORT,
            "shorts",
            "Shorts",
            "频道的 Shorts 短视频",
            &["video"][..],
            "视频文件由锁定版本的外部解析工具获取，工具缺失时只保存元数据",
        ),
        (
            POST,
            "posts",
            "社区帖子",
            "频道的社区帖子：文字、图片、投票与分享的视频",
            &["text", "image"][..],
            "投票只保存选项文字，不保存票数；分享的视频只保存关系",
        ),
    ];
    let mut out = Vec::new();
    for (lane, tab, title, what, kinds, media) in lanes {
        let subscription = channel(
            rule(
                "youtube",
                &format!("youtube.channel.{tab}"),
                RuleEntry::Subscription,
                &format!("频道{title}"),
                what,
            )
            .kinds(kinds)
            .lane(lane),
            tab,
        )
        .login(RuleLogin::None, PUBLIC)
        .limit(DATED)
        .limit(media);
        let preview = channel(
            rule(
                "youtube",
                &format!("youtube.channel.preview_{tab}"),
                RuleEntry::Preview,
                &format!("频道{title}预览"),
                "分页浏览后勾选下载",
            )
            .kinds(kinds)
            .lane(lane),
            tab,
        )
        .login(RuleLogin::None, PUBLIC)
        .limit(DATED);
        if tab == "videos" {
            out.push(
                subscription
                    .verified(
                        "2026-10-17",
                        "一个频道的视频列表 2 页（共 30 条、还有更多）和频道识别（句柄解析为频道 ID）；没有媒体字节",
                    )
                    .status(RuleStatus::Partial)
                    .limit("2026-10-18 的增量检查：自动下载关通过（5 条作品，视频保持“未下载”，第二次检查没有变化）；自动下载开的检查在额度用尽时中止，没有结果。Shorts 与社区帖子的订阅没有真实检查")
                    .limit("自动下载开的增量检查没有真实结果，按计划口径保持部分实现")
                    .build(),
            );
            out.push(
                preview
                    .verified(
                        "2026-10-17",
                        "同一频道视频列表 2 页后勾选 2 条下载到文件夹：2 个文件共 225,294,407 字节，全部已导出",
                    )
                    .status(RuleStatus::Partial)
                    .limit("2026-10-19 最终包上的列表页没有读到初始数据（站点返回了不含频道数据的页面），按规则停止；通过记录只对应 2026-10-17 的包")
                    .build(),
            );
        } else {
            out.push(unverified(subscription).build());
            out.push(unverified(preview).build());
        }
    }
    out
}

/// The link rules: one per kind of address a work can have.
fn link_rules() -> Vec<RuleDeclaration> {
    let link = |id: &str, title: &str, what: &str, kinds: &[&str]| {
        rule("youtube", id, RuleEntry::Link, title, what)
            .kinds(kinds)
            .login(RuleLogin::None, PUBLIC)
    };
    vec![
        link(
            "youtube.video.link",
            "视频链接",
            "单个视频的元数据、封面与视频文件",
            &["video"],
        )
        .work("youtube.com", "/watch")
        .work("youtube.com", "/live/{id}")
        .work("youtube.com", "/embed/{id}")
        .work("youtu.be", "/{id}")
        .limit("视频文件由锁定版本的外部解析工具获取，工具缺失时只保存元数据")
        .limit(
            "视频服务器对单次长连接限速，视频与音轨按 10 MiB 分段请求；直播回放与会员视频没有验证",
        )
        .verified(
            "2026-10-17",
            "一个视频：26 个候选，选 720p 以内的视频与音频各一，分段下载 43,211,617 字节并入库",
        )
        .status(RuleStatus::Partial)
        .limit("2026-10-19 最终包上的列表页没有读到初始数据，视频链接没有在最终包上重跑；通过记录只对应 2026-10-17 的包")
        .build(),
        unverified(
            link(
                "youtube.short.link",
                "Shorts 链接",
                "单个 Short 的元数据、封面与视频文件",
                &["video"],
            )
            .work("youtube.com", "/shorts/{id}"),
        )
        .limit("视频文件由锁定版本的外部解析工具获取，工具缺失时只保存元数据")
        .build(),
        link(
            "youtube.post.link",
            "社区帖子链接",
            "单个社区帖子的文字与图片",
            &["text", "image"],
        )
        .work("youtube.com", "/post/{id}")
        .limit(DATED)
        .limit("只验证了一个带图片的帖子；纯文字、投票和分享视频的帖子没有真实验证")
        .verified(
            "2026-10-17",
            "一个带图片的社区帖子：5 个候选，选 1 个，33,194 字节并入库",
        )
        .status(RuleStatus::Partial)
        .limit("2026-10-19 最终包上的列表页没有读到初始数据，社区帖子链接没有在最终包上重跑；通过记录只对应 2026-10-17 的包")
        .build(),
    ]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_rule_is_well_formed_for_the_catalog() {
        let rules = rules();
        assert_eq!(rules.len(), 9);
        for rule in rules {
            let id = rule.id.clone();
            let normalized = rule
                .normalized("youtube")
                .unwrap_or_else(|e| panic!("{id}: {e}"));
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
            .map(|r| r.id)
            .collect();
        assert_eq!(
            implemented,
            Vec::<&str>::new(),
            "the 2026-10-19 final package did not read the list; keep every rule partial"
        );
    }
}
