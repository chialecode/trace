//! What the Instagram adapter says it can subscribe to, preview and download.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

pub(crate) const LANE: &str = "post";

fn accounts(b: RuleBuilder) -> RuleBuilder {
    b.account("instagram.com", "/{name}")
        .account("instagram.com", "/{name}/reels")
}

fn partial(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial)
        .limit("真实站点检查（2026-10-18）：登录探测通过（页面标明已登录 Instagram 账号），但检查目标的主页被站点换成通用错误页（走代理与直连结果相同），适配器报告“站点不显示该主页”并按规则停止；列表与下载没有验证")
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let login = "需要登录；频繁请求容易触发验证，建议使用不常用的账号";
    let subscription = accounts(
        rule(
            "instagram",
            "instagram.user.posts",
            RuleEntry::Subscription,
            "用户帖子",
            "主页帖子流：图片、轮播与视频（含短视频）",
        )
        .kinds(&["text", "image", "video"])
        .lane(LANE)
        .login(RuleLogin::Required, login)
        .limit("不含快拍、精彩快拍和标签页；私密账号需要登录账号已关注"),
    );
    let preview = accounts(
        rule(
            "instagram",
            "instagram.user.preview",
            RuleEntry::Preview,
            "作者主页预览",
            "分页浏览后勾选下载",
        )
        .kinds(&["text", "image", "video"])
        .lane(LANE)
        .login(RuleLogin::Required, login),
    );
    let link = rule(
        "instagram",
        "instagram.post.link",
        RuleEntry::Link,
        "帖子链接",
        "单个帖子的文字、图片、轮播与视频",
    )
    .kinds(&["text", "image", "video"])
    .work("instagram.com", "/p/{id}")
    .work("instagram.com", "/reel/{id}")
    .work("instagram.com", "/reels/{id}")
    .work("instagram.com", "/tv/{id}")
    .work("instagram.com", "/{name}/p/{id}")
    .work("instagram.com", "/{name}/reel/{id}")
    .login(RuleLogin::Required, login);
    vec![
        partial(subscription).build(),
        partial(preview).build(),
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
                .normalized("instagram")
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
