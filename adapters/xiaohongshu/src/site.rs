//! The Xiaohongshu adapter's answers to the plugin protocol.
use crate::link::{Target, id_ok, parse, token_in};
use crate::note::{self, NOTE};
use crate::rules::LANE;
use crate::{account, api, memo};
use serde_json::{Value, json};
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, PluginError, ProbeAuthResult, ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::serve::{Link, SiteAdapter};
use trace_site_support::kit::{self, Work};
use trace_site_support::runtime::Transport;

pub(crate) struct Xiaohongshu;

/// The notes the profile page itself shows, for a visitor whose session did not provide the
/// device identifier the signed list needs. The page's first screen is not "no more notes".
fn first_screen(net: &dyn Transport, user_id: &str) -> Result<EnumeratedPage, PluginError> {
    let state = account::profile_state(net, user_id)?;
    let cards = note::cards(&state, user_id)?;
    for card in &cards {
        if let Some(token) = &card.token {
            memo::remember(&card.id, token);
        }
    }
    let items: Vec<_> = cards.iter().map(note::entry).collect();
    Ok(EnumeratedPage {
        items,
        next_cursor: None,
        has_more: false,
        coverage: json!({
            "source": "profile.state",
            "observed_count": cards.len(),
            "empty_confirmed": false,
            "history": "first_screen",
        }),
    })
}

impl SiteAdapter for Xiaohongshu {
    fn id(&self) -> &'static str {
        "xiaohongshu"
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

    /// The first screen of the profile, or — when the host passes the device identifier — the
    /// signed list that continues to the end of the account's history. An empty screen is
    /// never confirmed as "no notes"; a refused signature stops the list.
    fn enumerate(
        &self,
        net: &dyn Transport,
        params: &EnumerateParams,
    ) -> Result<EnumeratedPage, PluginError> {
        self.enumerate_with_context(net, params, &serde_json::Value::Null)
    }

    fn enumerate_with_context(
        &self,
        net: &dyn Transport,
        params: &EnumerateParams,
        context: &serde_json::Value,
    ) -> Result<EnumeratedPage, PluginError> {
        if params.content_type != LANE {
            return Err(error(FailureClass::Unsupported, "unknown Xiaohongshu lane"));
        }
        let user_id = &params.native_account_key;
        if !id_ok(user_id) {
            return Err(error(
                FailureClass::InvalidPayload,
                "invalid Xiaohongshu user id",
            ));
        }
        let device = context["device_id"]
            .as_str()
            .filter(|value| (8..=128).contains(&value.len()));
        let Some(device) = device else {
            if params.cursor.is_some() {
                return Err(kit::schema("Xiaohongshu has no list cursor"));
            }
            return first_screen(net, user_id);
        };
        let cursor = params.cursor.as_deref().unwrap_or("");
        let answer = api::signed_notes(net, user_id, cursor, device)?;
        let data = &answer["data"];
        let Some(notes) = data["notes"].as_array() else {
            return Err(kit::schema("Xiaohongshu list answer has no notes"));
        };
        let cards = note::api_cards(&Value::Array(notes.clone()), user_id)?;
        for card in &cards {
            if let Some(token) = &card.token {
                memo::remember(&card.id, token);
            }
        }
        let has_more = data["has_more"] == true;
        let next = data["cursor"].as_str().filter(|cursor| !cursor.is_empty());
        if has_more && next.is_none() {
            return Err(kit::schema("Xiaohongshu list claims more without a cursor"));
        }
        let items: Vec<_> = cards.iter().map(note::entry).collect();
        Ok(EnumeratedPage {
            items,
            next_cursor: has_more.then(|| next.unwrap_or_default().to_owned()),
            has_more,
            coverage: json!({
                "source": "user_posted",
                "observed_count": cards.len(),
                "empty_confirmed": false,
                "history": "signed_list",
            }),
        })
    }

    fn work(&self, net: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
        if kind != NOTE || !id_ok(key) {
            return Err(error(
                FailureClass::InvalidPayload,
                "invalid Xiaohongshu note",
            ));
        }
        let path = account::path_with_token(&format!("/explore/{key}"), key, "pc_user");
        let state = api::state(net, &path)?;
        let Some(found) = note::detail(&state, key) else {
            // The page is there but holds no such note: the site hides it from this visitor
            // (it needs a token the visitor was not given), which is not "deleted".
            return Err(if state["note"]["noteDetailMap"].is_object() {
                kit::denied("the Xiaohongshu note is not shown to this visitor")
            } else {
                kit::schema("page has no note detail")
            });
        };
        note::work(found, "note.page")
    }

    fn link(&self, net: &dyn Transport, input: &str) -> Result<Link, PluginError> {
        let expanded = account::expand(net, input)?;
        let Some(Target::Note(id)) = parse(&expanded) else {
            return Err(error(
                FailureClass::InvalidPayload,
                "not a Xiaohongshu note link",
            ));
        };
        if let Some(token) = token_in(&expanded) {
            memo::remember(&id, &token);
        }
        Ok(Link {
            canonical: format!("{}/explore/{id}", api::ORIGIN),
            key: id,
            kind: NOTE.into(),
        })
    }

    fn probe_auth(&self, net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
        account::probe(net)
    }
}
