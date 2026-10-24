//! What the TikTok adapter says it can subscribe to, preview and download.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleObject, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

pub(crate) const LANE: &str = "video";

fn accounts(b: RuleBuilder) -> RuleBuilder {
    b.account("tiktok.com", "/@{name}")
        .short("vm.tiktok.com", RuleObject::Account)
        .short("vt.tiktok.com", RuleObject::Account)
}

fn partial(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial)
        .limit("真实站点检查（2026-10-18）：账号识别、列表 2 页、勾选下载与视频链接下载通过，视频文件由 Core 带上页面会话 Cookie 取得；增量检查（自动下载关与开各一次）在站点答复列表“无数据”（限流）后按规则停止，没有结果；图集与音频作品没有真实检查")
        .limit("2026-10-19 在最终包上重做列表时站点直接以限流答复，按规则停止，没有重试")
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let login = "公开作品不需要登录；登录后可读范围更大";
    let list_limit = "作品列表接口需要站点的请求签名，没有签名时站点返回空结果；读不到时按“未知”处理，不当作没有更新";
    let subscription = accounts(
        rule(
            "tiktok",
            "tiktok.user.posts",
            RuleEntry::Subscription,
            "用户作品",
            "作者发布的视频和图集",
        )
        .kinds(&["video", "image", "audio"])
        .lane(LANE)
        .login(RuleLogin::Optional, login)
        .limit(list_limit),
    );
    let preview = accounts(
        rule(
            "tiktok",
            "tiktok.user.preview",
            RuleEntry::Preview,
            "作者主页预览",
            "分页浏览后勾选下载",
        )
        .kinds(&["video", "image", "audio"])
        .lane(LANE)
        .login(RuleLogin::Optional, login)
        .limit(list_limit),
    );
    let link = rule(
        "tiktok",
        "tiktok.post.link",
        RuleEntry::Link,
        "作品链接",
        "单个视频或图集",
    )
    .kinds(&["video", "image", "audio"])
    .work("tiktok.com", "/@{name}/video/{id}")
    .work("tiktok.com", "/@{name}/photo/{id}")
    .short("vm.tiktok.com", RuleObject::Entry)
    .short("vt.tiktok.com", RuleObject::Entry)
    .login(RuleLogin::Optional, login)
    .limit("下载地址依赖站点页面设置的会话 Cookie，未验证前可能被拒绝");
    vec![
        partial(subscription).build(),
        preview
        .verified(
            "2026-10-18",
            "同一账号视频列表 2 页（共 30 条、还有更多）后勾选 2 条下载到文件夹：2 个文件共 2,644,096 字节，全部已导出",
        )
        .status(RuleStatus::Partial)
        .limit("站点的视频地址要页面会话 Cookie：Core 只在内存里带上，不落盘、不给适配器；仍被拒绝时改用随包的外部解析工具（离线验证，真实站点未用到）")
        .limit("2026-10-19 最终包上的列表被站点限流拒绝，预览没有重跑；通过记录只对应 2026-10-18 的包")
        .build(),
        partial(link).build(),
    ]
}

#[cfg(test)]
mod tests {
    use super::*;

    const IMPLEMENTED: [&str; 0] = [];

    #[test]
    fn every_rule_is_well_formed_for_the_catalog() {
        let rules = rules();
        assert_eq!(rules.len(), 3);
        for rule in rules {
            let id = rule.id.clone();
            let normalized = rule
                .normalized("tiktok")
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
        assert_eq!(implemented, IMPLEMENTED);
    }
}
