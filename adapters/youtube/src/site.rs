//! The YouTube adapter's answers to the plugin protocol.
use crate::entry::{SHORT, VIDEO};
use crate::link::{Target, is_channel_id, parse};
use crate::post::POST;
use crate::{account, api, post, tabs, watch};
use serde_json::json;
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, PluginError, ProbeAuthResult, ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::serve::{Link, SiteAdapter};
use trace_site_support::kit::{self, Work};
use trace_site_support::runtime::Transport;

pub(crate) struct YouTube;

/// Where a list read stopped: the client version the later pages need and the token.
struct Position {
    version: String,
    token: String,
}

impl Position {
    fn decode(cursor: &str) -> Result<Self, PluginError> {
        let bad = || kit::schema("invalid YouTube cursor");
        let rest = cursor.strip_prefix("yt1:").ok_or_else(bad)?;
        let (version, token) = rest.split_once(':').ok_or_else(bad)?;
        let clean = |s: &str, extra: &[char]| {
            !s.is_empty()
                && s.len() <= 4096
                && s.chars()
                    .all(|c| c.is_ascii_alphanumeric() || extra.contains(&c))
        };
        if !clean(version, &['.']) || !clean(token, &['-', '_', '%', '=', '+', '/', '.']) {
            return Err(bad());
        }
        Ok(Self {
            version: version.into(),
            token: token.into(),
        })
    }

    fn encode(version: &str, token: &str) -> String {
        format!("yt1:{version}:{token}")
    }
}

impl SiteAdapter for YouTube {
    fn id(&self) -> &'static str {
        "youtube"
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
        let lane = params.content_type.as_str();
        if !matches!(lane, VIDEO | SHORT | POST) {
            return Err(error(FailureClass::Unsupported, "unknown YouTube lane"));
        }
        let id = &params.native_account_key;
        if !is_channel_id(id) {
            return Err(error(
                FailureClass::InvalidPayload,
                "invalid YouTube channel id",
            ));
        }
        let (listing, version, first) = match params.cursor.as_deref() {
            None => {
                let (listing, version) = tabs::first(net, id, lane)?;
                (listing, version, true)
            }
            Some(cursor) => {
                let at = Position::decode(cursor)?;
                (
                    tabs::next(net, &at.version, &at.token, lane)?,
                    at.version,
                    false,
                )
            }
        };
        if !listing.items.is_empty() && listing.items.iter().any(|e| e.native_entry_key.is_empty())
        {
            return Err(kit::schema("list item without id"));
        }
        if listing.items.is_empty()
            && !listing.skipped.is_empty()
            && listing.token.is_none()
            && first
        {
            // Everything the page showed was left out: unknown, not empty.
            return Err(error(
                FailureClass::SourceUnavailable,
                "the list holds only items that cannot be archived",
            ));
        }
        let more = listing.token.is_some();
        Ok(EnumeratedPage {
            next_cursor: listing
                .token
                .as_deref()
                .map(|t| Position::encode(&version, t)),
            has_more: more,
            coverage: json!({
                "source": "channel.tab",
                "lane": lane,
                "observed_count": listing.items.len(),
                "skipped": listing.skipped,
                "tab_absent": listing.tab_absent,
                "empty_confirmed": first && listing.items.is_empty() && listing.skipped.is_empty()
                    && !more && (listing.tab_absent || listing.container_seen),
                "history": "unknown",
            }),
            items: listing.items,
        })
    }

    fn work(&self, net: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
        match kind {
            VIDEO | SHORT => watch::work(net, key, kind),
            POST => post::work(net, key),
            _ => Err(error(
                FailureClass::InvalidPayload,
                "unknown YouTube work kind",
            )),
        }
    }

    fn link(&self, _net: &dyn Transport, input: &str) -> Result<Link, PluginError> {
        let (key, kind, canonical) = match parse(input) {
            Some(Target::Video(id)) => (id.clone(), VIDEO, format!("https://youtu.be/{id}")),
            Some(Target::Short(id)) => (id.clone(), SHORT, format!("{}/shorts/{id}", api::ORIGIN)),
            Some(Target::Post(id)) => (id.clone(), POST, format!("{}/post/{id}", api::ORIGIN)),
            _ => {
                return Err(error(
                    FailureClass::InvalidPayload,
                    "not a YouTube video, Short or post link",
                ));
            }
        };
        Ok(Link {
            key,
            kind: kind.into(),
            canonical,
        })
    }

    fn probe_auth(&self, _net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
        Ok(account::probe())
    }
}
