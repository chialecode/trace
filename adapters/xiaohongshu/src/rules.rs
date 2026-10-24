//! What the Xiaohongshu adapter says it can subscribe to, preview and download.
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleObject, RuleStatus};
use trace_site_support::kit::rules::{RuleBuilder, rule};

pub(crate) const LANE: &str = "note";

fn partial(b: RuleBuilder) -> RuleBuilder {
    b.status(RuleStatus::Partial)
        .limit("真实站点检查（2026-10-18）：账号识别与首屏列表通过；增量检查（自动下载关与开各一次）没有执行：列表不给发布时间，检查脚本挑不出带日期的作品来定范围；订阅的第一次检查要逐条取详情才能判断范围（首屏 32 条约 33 次请求）")
        .limit("2026-10-19 在最终包上尝试 DEC-041 的签名续页：站点以 HTTP 461 拒绝，按规则停止，没有换参数重试；完整历史标记为未取完")
}

pub(crate) fn rules() -> Vec<RuleDeclaration> {
    let login = "推荐登录；未登录常被要求登录或验证";
    let history = "读取主页首屏，并在宿主提供设备标识 a1 时用签名续页读取完整历史；2026-10-19 的最终包尝试被站点以 HTTP 461 拒绝，按 DEC-041 停止并把完整历史标为未取完（不换参数重试）";
    let subscription = rule(
        "xiaohongshu",
        "xiaohongshu.user.notes",
        RuleEntry::Subscription,
        "用户笔记",
        "作者主页首屏的图文（含实况图）与视频笔记",
    )
    .kinds(&["text", "image", "video"])
    .lane(LANE)
    .account("xiaohongshu.com", "/user/profile/{id}")
    .login(RuleLogin::Optional, login)
    .limit(history);
    let preview = rule(
        "xiaohongshu",
        "xiaohongshu.user.preview",
        RuleEntry::Preview,
        "作者主页预览",
        "浏览主页首屏后勾选下载",
    )
    .kinds(&["text", "image", "video"])
    .lane(LANE)
    .account("xiaohongshu.com", "/user/profile/{id}")
    .login(RuleLogin::Optional, login)
    .limit(history);
    let link = rule(
        "xiaohongshu",
        "xiaohongshu.note.link",
        RuleEntry::Link,
        "笔记链接",
        "单篇笔记的文字、图片、实况图与视频",
    )
    .kinds(&["text", "image", "video"])
    .work("xiaohongshu.com", "/explore/{id}")
    .work("xiaohongshu.com", "/discovery/item/{id}")
    .work("xiaohongshu.com", "/user/profile/{user}/{id}")
    .work("xiaohongshu.com", "/search_result/{id}")
    .short("xhslink.com", RuleObject::Entry)
    .login(RuleLogin::Optional, login)
    .limit("笔记需要站点给出的 xsec_token 才能打开；列表和分享链接会带上，只给笔记号时可能被拒绝");
    vec![
        partial(subscription).build(),
        preview
        .verified(
            "2026-10-18",
            "作者主页首屏列表（32 条，无发布时间，没有后续页）后勾选 2 条下载到文件夹：9 个文件共 25,950,636 字节，全部已导出",
        )
        .status(RuleStatus::Partial)
        .limit("2026-10-19 最终包上的列表在签名续页处被 HTTP 461 拒绝，预览没有重跑；首屏预览与勾选下载的通过记录只对应 2026-10-18 的包")
        .build(),
        link
        .verified(
            "2026-10-18",
            "一条视频笔记（3 个候选，选 1 个，7,855,669 字节）和一条图文笔记（10 个候选，选 5 个，729,933 字节）并入库；纯文字笔记没有验证",
        )
        .status(RuleStatus::Partial)
        .limit("视频流分组名不固定，按条目自述的编码分类；只验证了一条视频笔记")
        .limit("2026-10-19 最终包上的列表在签名续页处被 HTTP 461 拒绝后按规则停止，笔记链接没有在最终包上重跑")
        .build(),
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
                .normalized("xiaohongshu")
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
