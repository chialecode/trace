//! The Instagram adapter's answers to the plugin protocol.
use crate::link::{Target, code_ok, parse};
use crate::post::{self, POST};
use crate::rules::LANE;
use crate::web::{ORIGIN, Viewer, viewer_in};
use crate::{account, api, feed};
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, PluginError, ProbeAuthResult, ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::serve::{Link, SiteAdapter};
use trace_site_support::kit::{self, Work, numeric};
use trace_site_support::runtime::Transport;

pub(crate) struct Instagram;

impl SiteAdapter for Instagram {
    fn id(&self) -> &'static str {
        "instagram"
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
            return Err(error(FailureClass::Unsupported, "unknown Instagram lane"));
        }
        if !numeric(&params.native_account_key, 24) {
            return Err(error(
                FailureClass::InvalidPayload,
                "invalid Instagram profile id",
            ));
        }
        feed::page(net, &params.native_account_key, params.cursor.as_deref())
    }

    fn work(&self, net: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
        if kind != POST || !code_ok(key) {
            return Err(error(
                FailureClass::InvalidPayload,
                "invalid Instagram post",
            ));
        }
        let html = api::fetch_page(net, &format!("/p/{key}/"))?;
        let Some(node) = post::find_in_page(&html, key) else {
            if viewer_in(&html) == Viewer::Anonymous {
                return Err(kit::login_needed("Instagram shows the login page"));
            }
            return Err(if html.contains("this page isn") {
                error(
                    FailureClass::SourceUnavailable,
                    "Instagram has no such post",
                )
            } else {
                kit::schema("page has no post data")
            });
        };
        let work = post::work(&node, "post.page")?;
        if work.key != key {
            return Err(kit::schema("detail identity mismatch"));
        }
        Ok(work)
    }

    fn link(&self, _net: &dyn Transport, input: &str) -> Result<Link, PluginError> {
        match parse(input) {
            Some(Target::Post(code)) => Ok(Link {
                canonical: format!("{ORIGIN}/p/{code}/"),
                key: code,
                kind: POST.into(),
            }),
            _ => Err(error(
                FailureClass::InvalidPayload,
                "not an Instagram post link",
            )),
        }
    }

    fn probe_auth(&self, net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
        account::probe(net)
    }
}
