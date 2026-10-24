//! The TikTok adapter's answers to the plugin protocol.
use crate::item::{self, PHOTO, VIDEO};
use crate::link::{Target, parse};
use crate::rules::LANE;
use crate::{account, api};
use serde_json::{Value, json};
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, PluginError, ProbeAuthResult, ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::serve::{Link, SiteAdapter};
use trace_site_support::kit::{self, Work, numeric, string};
use trace_site_support::runtime::Transport;

pub(crate) struct TikTok;

fn sec_uid_ok(id: &str) -> bool {
    (16..=128).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

/// The list cursor is the site's own number (a time in milliseconds), carried as `tt1:`.
fn decode_cursor(cursor: Option<&str>) -> Result<String, PluginError> {
    let Some(raw) = cursor else {
        return Ok("0".into());
    };
    raw.strip_prefix("tt1:")
        .filter(|c| numeric(c, 20))
        .map(str::to_owned)
        .ok_or_else(|| kit::schema("invalid TikTok cursor"))
}

impl SiteAdapter for TikTok {
    fn id(&self) -> &'static str {
        "tiktok"
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
            return Err(error(FailureClass::Unsupported, "unknown TikTok lane"));
        }
        let sec_uid = &params.native_account_key;
        if !sec_uid_ok(sec_uid) {
            return Err(error(
                FailureClass::InvalidPayload,
                "invalid TikTok account key",
            ));
        }
        let cursor = decode_cursor(params.cursor.as_deref())?;
        let v = api::item_list(net, sec_uid, &cursor)?;
        if let Some(e) = account::status(v["statusCode"].as_i64().unwrap_or(0)) {
            return Err(e);
        }
        let list = v["itemList"]
            .as_array()
            .ok_or_else(|| kit::limited("TikTok list answer has no items"))?;
        let items: Vec<_> = list.iter().filter_map(item::entry).collect();
        if !list.is_empty() && items.is_empty() {
            return Err(kit::schema("TikTok items carry no id"));
        }
        let next = string(&v["cursor"]).or_else(|| v["cursor"].as_i64().map(|n| n.to_string()));
        let more = v["hasMore"] == true && next.as_deref().is_some_and(|n| numeric(n, 20));
        Ok(EnumeratedPage {
            items,
            next_cursor: more.then(|| format!("tt1:{}", next.unwrap_or_default())),
            has_more: more,
            coverage: json!({
                "source": "api.post.item_list",
                "observed_count": list.len(),
                "empty_confirmed": false,
                "history": "unknown",
            }),
        })
    }

    fn work(&self, net: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
        if !numeric(key, 24) || !matches!(kind, VIDEO | PHOTO) {
            return Err(error(FailureClass::InvalidPayload, "invalid TikTok work"));
        }
        let path = format!("/@_/{kind}/{key}?lang=en");
        let scope = api::scope(&api::html(net, &path)?)?;
        let detail = &scope["webapp.video-detail"];
        if !detail.is_object() {
            return Err(kit::schema("page has no work detail"));
        }
        if let Some(e) = account::status(detail["statusCode"].as_i64().unwrap_or(0)) {
            return Err(e);
        }
        let item: &Value = &detail["itemInfo"]["itemStruct"];
        let work = item::work(item, "work.page")?;
        if work.key != key || work.kind != kind {
            return Err(kit::schema("detail identity mismatch"));
        }
        Ok(work)
    }

    fn link(&self, net: &dyn Transport, input: &str) -> Result<Link, PluginError> {
        let expanded = account::expand(net, input)?;
        let (user, id, kind) = match parse(&expanded) {
            Some(Target::Video { user, id }) => (user, id, VIDEO),
            Some(Target::Photo { user, id }) => (user, id, PHOTO),
            _ => {
                return Err(error(
                    FailureClass::InvalidPayload,
                    "not a TikTok video or photo link",
                ));
            }
        };
        Ok(Link {
            canonical: format!("{}/@{user}/{kind}/{id}", api::ORIGIN),
            key: id,
            kind: kind.into(),
        })
    }

    fn probe_auth(&self, net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
        account::probe(net)
    }
}
