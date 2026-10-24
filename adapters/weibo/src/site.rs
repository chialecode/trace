//! The Weibo adapter's answers to the plugin protocol.
use crate::link::{Target, parse};
use crate::rules::LANE;
use crate::{account, api, post};
use serde_json::{Value, json};
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, PluginError, ProbeAuthResult, ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::serve::{Link, SiteAdapter};
use trace_site_support::kit::{self, Work, count, string};
use trace_site_support::runtime::Transport;

pub(crate) struct Weibo;

/// Where a list read stopped: the page and the continuation token the site gave with it.
struct Position {
    page: u32,
    since: String,
}

impl Position {
    fn decode(cursor: Option<&str>) -> Result<Self, PluginError> {
        let Some(raw) = cursor else {
            return Ok(Self {
                page: 1,
                since: String::new(),
            });
        };
        let bad = || kit::schema("invalid Weibo cursor");
        let rest = raw.strip_prefix("wb1:").ok_or_else(bad)?;
        let (page, since) = rest.split_once(':').ok_or_else(bad)?;
        let page: u32 = page.parse().map_err(|_| bad())?;
        let clean = since
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_');
        if !(1..=10_000).contains(&page) || !clean || since.len() > 64 {
            return Err(bad());
        }
        Ok(Self {
            page,
            since: since.to_owned(),
        })
    }

    fn encode(page: u32, since: &str) -> String {
        format!("wb1:{page}:{since}")
    }
}

impl SiteAdapter for Weibo {
    fn id(&self) -> &'static str {
        "weibo"
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
            return Err(error(FailureClass::Unsupported, "unknown Weibo lane"));
        }
        let uid = &params.native_account_key;
        if !kit::numeric(uid, 20) {
            return Err(error(FailureClass::InvalidPayload, "invalid Weibo user id"));
        }
        let position = Position::decode(params.cursor.as_deref())?;
        let page = position.page.to_string();
        let mut query = vec![("uid", uid.as_str()), ("page", &page), ("feature", "0")];
        if !position.since.is_empty() {
            query.push(("since_id", &position.since));
        }
        let v = api::ajax(net, "/ajax/statuses/mymblog", &query)?;
        let list = v["data"]["list"]
            .as_array()
            .ok_or_else(|| kit::schema("Weibo list is missing"))?;
        let items = post::entries(list);
        if !list.is_empty() && items.is_empty() {
            return Err(kit::schema("Weibo statuses carry no id"));
        }
        let total = count(&v["data"]["total"]);
        let since = string(&v["data"]["since_id"]).unwrap_or_default();
        // The site ends a list with an empty page; a full page may be followed by more.
        let more = !list.is_empty();
        Ok(EnumeratedPage {
            items,
            next_cursor: more.then(|| Position::encode(position.page + 1, &since)),
            has_more: more,
            coverage: json!({
                "source": "ajax.statuses.mymblog",
                "page": position.page,
                "observed_count": list.len(),
                "total": total,
                "empty_confirmed": total == Some(0) && position.page == 1,
                "history": "unknown",
            }),
        })
    }

    fn work(&self, net: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
        if kind != "post" || !kit::numeric(key, 20) {
            return Err(error(FailureClass::InvalidPayload, "invalid Weibo post"));
        }
        let v = api::ajax(
            net,
            "/ajax/statuses/show",
            &[("id", key), ("locale", "zh-CN"), ("isGetLongText", "true")],
        )?;
        // The status is the answer itself; some deployments wrap it in `data`.
        let status = if v["idstr"].is_null() && v["data"].is_object() {
            &v["data"]
        } else {
            &v
        };
        let long = self.long_text(net, status)?;
        let work = post::work(status, long.as_deref(), "ajax.statuses.show")?;
        if work.key != key {
            return Err(kit::schema("detail identity mismatch"));
        }
        Ok(work)
    }

    fn link(&self, net: &dyn Transport, input: &str) -> Result<Link, PluginError> {
        let expanded = account::expand(net, input)?;
        match parse(&expanded) {
            Some(Target::Post(mid)) => Ok(Link {
                canonical: format!("https://weibo.com/detail/{mid}"),
                key: mid,
                kind: "post".into(),
            }),
            _ => Err(error(FailureClass::InvalidPayload, "not a Weibo post link")),
        }
    }

    fn probe_auth(&self, net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
        account::probe(net)
    }
}

impl Weibo {
    /// The full text of a status whose list text is cut short. A missing text is an
    /// omission of the observation, but a login wall or a limit is the caller's to see.
    fn long_text(
        &self,
        net: &dyn Transport,
        status: &Value,
    ) -> Result<Option<String>, PluginError> {
        if status["isLongText"] != true {
            return Ok(None);
        }
        if let Some(text) = string(&status["longText"]["longTextContent"]) {
            return Ok(Some(text));
        }
        let Some(short) = string(&status["mblogid"]) else {
            return Ok(None);
        };
        match api::ajax(net, "/ajax/statuses/longtext", &[("id", &short)]) {
            Ok(v) => Ok(string(&v["data"]["longTextContent"])),
            Err(e)
                if matches!(
                    e.class,
                    FailureClass::AuthRequired
                        | FailureClass::RateLimited
                        | FailureClass::PermissionDenied
                ) =>
            {
                Err(e)
            }
            Err(_) => Ok(None),
        }
    }
}
