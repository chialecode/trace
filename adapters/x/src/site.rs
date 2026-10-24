//! The X adapter's answers to the plugin protocol.
use crate::link::{Target, parse};
use crate::rules::LANE;
use crate::tweet::{self, POST};
use crate::{account, api, timeline};
use serde_json::json;
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, PluginError, ProbeAuthResult, ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::serve::{Link, SiteAdapter};
use trace_site_support::kit::{self, Work, numeric};
use trace_site_support::runtime::Transport;

pub(crate) struct X;

/// The list cursor is the site's own opaque token, carried as `x1:`.
fn decode_cursor(cursor: Option<&str>) -> Result<Option<String>, PluginError> {
    let Some(raw) = cursor else { return Ok(None) };
    raw.strip_prefix("x1:")
        .filter(|c| {
            (1..=512).contains(&c.len())
                && c.bytes().all(|b| {
                    b.is_ascii_alphanumeric()
                        || matches!(b, b'-' | b'_' | b'=' | b'+' | b'/' | b'|' | b'%' | b'.')
                })
        })
        .map(|c| Some(c.to_owned()))
        .ok_or_else(|| kit::schema("invalid X cursor"))
}

impl SiteAdapter for X {
    fn id(&self) -> &'static str {
        "x"
    }

    fn rules(&self) -> Vec<RuleDeclaration> {
        crate::rules::rules()
    }

    fn resolve_account(
        &self,
        net: &dyn Transport,
        input: &str,
    ) -> Result<ResolvedAccount, PluginError> {
        account::resolve(net, input)
    }

    fn enumerate(
        &self,
        net: &dyn Transport,
        params: &EnumerateParams,
    ) -> Result<EnumeratedPage, PluginError> {
        if params.content_type != LANE {
            return Err(error(FailureClass::Unsupported, "unknown X lane"));
        }
        let user = &params.native_account_key;
        if !numeric(user, 24) {
            return Err(error(FailureClass::InvalidPayload, "invalid X user id"));
        }
        let cursor = decode_cursor(params.cursor.as_deref())?;
        let mut variables = json!({
            "userId": user, "count": 20, "includePromotedContent": false,
            "withQuickPromoteEligibilityTweetFields": false, "withVoice": true, "withV2Timeline": true,
        });
        if let Some(c) = &cursor {
            variables["cursor"] = json!(c);
        }
        let answer = api::graphql(net, "UserTweets", &variables)?;
        let page = timeline::read(&answer);
        if !page.has_instructions {
            return Err(kit::schema("X timeline has no instructions"));
        }
        let items: Vec<_> = page
            .posts
            .iter()
            .filter_map(|(post, pinned)| tweet::entry(post, *pinned))
            .collect();
        if !page.posts.is_empty() && items.is_empty() {
            return Err(kit::schema("X posts carry no id"));
        }
        // The bottom cursor is always given; a page without posts is the end of the list.
        let more = !items.is_empty() && page.cursor.is_some();
        Ok(EnumeratedPage {
            next_cursor: more.then(|| format!("x1:{}", page.cursor.clone().unwrap_or_default())),
            has_more: more,
            coverage: json!({
                "source": "graphql.UserTweets",
                "observed_count": items.len(),
                "unreadable_count": page.unreadable,
                "empty_confirmed": false,
                "history": "unknown",
            }),
            items,
        })
    }

    fn work(&self, net: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
        if kind != POST || !numeric(key, 24) {
            return Err(error(FailureClass::InvalidPayload, "invalid X post"));
        }
        let answer = api::graphql(
            net,
            "TweetResultByRestId",
            &json!({"tweetId": key, "withCommunity": false, "includePromotedContent": false, "withVoice": false}),
        )?;
        let result = &answer["data"]["tweetResult"]["result"];
        if result.is_null() {
            return Err(error(FailureClass::SourceUnavailable, "X has no such post"));
        }
        let Some(post) = tweet::unwrap(result) else {
            return Err(match result["__typename"].as_str() {
                Some("TweetTombstone" | "TweetUnavailable") => {
                    kit::denied("the X post is not shown to this visitor")
                }
                _ => kit::schema("X post result is not understood"),
            });
        };
        let work = tweet::work(post, "graphql.TweetResultByRestId")?;
        if work.key != key {
            return Err(kit::schema("detail identity mismatch"));
        }
        Ok(work)
    }

    fn link(&self, net: &dyn Transport, input: &str) -> Result<Link, PluginError> {
        let expanded = account::expand(net, input)?;
        match parse(&expanded) {
            Some(Target::Post(id)) => Ok(Link {
                canonical: format!("{}/i/status/{id}", api::ORIGIN),
                key: id,
                kind: POST.into(),
            }),
            _ => Err(error(FailureClass::InvalidPayload, "not an X post link")),
        }
    }

    fn probe_auth(&self, net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
        account::probe(net)
    }
}
