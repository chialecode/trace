//! What the Weibo adapter says it can subscribe to, preview and download.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleObject, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

pub(crate) const LANE: &str = "post";

fn accounts(b: RuleBuilder) -> RuleBuilder {
    b.account("weibo.com", "/u/{uid}")
        .account("weibo.com", "/n/{name}")
        .account("weibo.com", "/{name}")
        .account("weibo.cn", "/u/{uid}")
        .account("weibo.cn", "/profile/{uid}")
        .short("t.cn", RuleObject::Account)
}

fn unverified(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial).limit("真实站点检查（2026-10-18）：旧 Cookie 失效后用户重新复制，新 Cookie 已核对是登录状态；列表、下载与增量检查还没有在最终包上验证")
        .limit("登录探测先读桌面端配置接口（访客答“需要登录”），再读首页配置里写出的当前用户；两步都和不带 Cookie 的同一请求对照过。分组列表对访客也给出分组，不作为登录证明")
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let login =
        "需要登录：不带登录的请求，主页资料与微博列表都答“需要登录”（TRACE 不使用站点的访客身份）";
    let subscription = accounts(
        rule(
            "weibo",
            "weibo.user.posts",
            RuleEntry::Subscription,
            "用户微博",
            "作者发布的原创与转发微博：长文全文、图片（含实况图）、视频",
        )
        .kinds(&["text", "image", "live_photo", "video"])
        .lane(LANE)
        .login(RuleLogin::Required, login)
        .limit("转发的原微博只保存关系和文字摘录，不保存其图片和视频")
        .limit("头条文章只保存摘要与链接"),
    );
    let preview = accounts(
        rule(
            "weibo",
            "weibo.user.preview",
            RuleEntry::Preview,
            "作者主页预览",
            "分页浏览作者的微博，勾选后下载",
        )
        .kinds(&["text", "image", "live_photo", "video"])
        .lane(LANE)
        .login(RuleLogin::Required, login),
    );
    let link = rule(
        "weibo",
        "weibo.post.link",
        RuleEntry::Link,
        "微博详情",
        "单条微博的文字、图片、实况图与视频",
    )
    .kinds(&["text", "image", "live_photo", "video"])
    .work("weibo.com", "/{uid}/{id}")
    .work("weibo.com", "/detail/{id}")
    .work("weibo.cn", "/detail/{id}")
    .work("weibo.cn", "/status/{id}")
    .short("t.cn", RuleObject::Entry)
    .login(RuleLogin::Required, login);
    [subscription, preview, link]
        .into_iter()
        .map(|b| unverified(b).build())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_rule_is_well_formed_for_the_catalog() {
        let rules = rules();
        assert_eq!(rules.len(), 3);
        for rule in rules {
            let id = rule.id.clone();
            let normalized = rule
                .normalized("weibo")
                .unwrap_or_else(|e| panic!("{id}: {e}"));
            assert_eq!(normalized.status, RuleStatus::Partial);
            assert!(!normalized.limits.is_empty());
        }
    }
}
