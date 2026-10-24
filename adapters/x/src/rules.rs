//! What the X adapter says it can subscribe to, preview and download.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleObject, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

pub(crate) const LANE: &str = "post";

fn accounts(b: RuleBuilder) -> RuleBuilder {
    let mut b = b;
    for host in ["x.com", "twitter.com"] {
        for path in ["/{name}", "/{name}/media", "/{name}/with_replies"] {
            b = b.account(host, path);
        }
    }
    b
}

fn partial(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial)
        .limit("真实站点检查（2026-10-18）：账号识别、列表 2 页、勾选下载（图片推文）通过，查询标识用随适配器发布的数据，没有触发重新读取；检查账号的前两页没有原生视频（转推的原推视频不属于转推），所以视频推文的链接与增量检查没有做；增量检查（自动下载关与开各一次）没有结果")
        .limit("站点拒绝未签名的请求时，按公开算法计算页面要求的事务 ID 请求头后重试；仍被拒绝按“未知”处理，不当作没有更新")
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let login = "需要登录；频繁请求有被限制的风险，建议使用不常用的账号";
    let subscription = accounts(
        rule(
            "x",
            "x.user.posts",
            RuleEntry::Subscription,
            "用户推文",
            "作者的推文、转推与引用：文字、图片、视频与 GIF",
        )
        .kinds(&["text", "image", "video"])
        .lane(LANE)
        .login(RuleLogin::Required, login)
        .limit("转推只保存关系与原推文字摘录，不保存原推的图片和视频")
        .limit("回复与点赞列表不在范围内"),
    );
    let preview = accounts(
        rule(
            "x",
            "x.user.preview",
            RuleEntry::Preview,
            "作者主页预览",
            "分页浏览后勾选下载",
        )
        .kinds(&["text", "image", "video"])
        .lane(LANE)
        .login(RuleLogin::Required, login),
    );
    let link = rule(
        "x",
        "x.post.link",
        RuleEntry::Link,
        "推文链接",
        "单条推文的文字、图片、视频与 GIF",
    )
    .kinds(&["text", "image", "video"])
    .work("x.com", "/{name}/status/{id}")
    .work("x.com", "/i/status/{id}")
    .work("twitter.com", "/{name}/status/{id}")
    .work("twitter.com", "/i/status/{id}")
    .short("t.co", RuleObject::Entry)
    .login(RuleLogin::Required, login);
    vec![
        partial(subscription).build(),
        preview
        .verified(
            "2026-10-18",
            "账号识别和列表 2 页（共 20 条、有后续页）后勾选 1 条图片推文下载到文件夹：1 个文件共 1,894,410 字节，全部已导出",
        )
        .limit("勾选下载只验证了图片推文；视频推文没有真实验证")
        .build(),
        partial(link).build(),
    ]
}

#[cfg(test)]
mod tests {
    use super::*;

    const IMPLEMENTED: [&str; 1] = ["x.user.preview"];

    #[test]
    fn every_rule_is_well_formed_for_the_catalog() {
        let rules = rules();
        assert_eq!(rules.len(), 3);
        for rule in rules {
            let id = rule.id.clone();
            let normalized = rule.normalized("x").unwrap_or_else(|e| panic!("{id}: {e}"));
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
