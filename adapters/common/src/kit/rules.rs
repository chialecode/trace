//! A short way to write the rule declarations of a site.
use trace_contracts::rules::{
    RuleDeclaration, RuleEntry, RuleLogin, RuleNetwork, RuleObject, RuleParam, RuleStatus,
    UrlPattern,
};
use trace_domain::sites;

pub struct RuleBuilder(RuleDeclaration);

/// Starts a rule of `site`; login and network default to what the site registry advises.
pub fn rule(site: &str, id: &str, entry: RuleEntry, title: &str, description: &str) -> RuleBuilder {
    let registry = sites::site(site);
    RuleBuilder(RuleDeclaration {
        id: id.into(),
        site: site.into(),
        title: title.into(),
        description: description.into(),
        entry,
        content_kinds: Vec::new(),
        url_patterns: Vec::new(),
        params: Vec::new(),
        login: registry.map_or(RuleLogin::Optional, |s| s.login.into()),
        login_note: None,
        network: registry.map_or(RuleNetwork::Direct, |s| s.network.into()),
        limits: Vec::new(),
        status: RuleStatus::Partial,
        verified_at: None,
        verified_scope: None,
        lane: None,
    })
}

impl RuleBuilder {
    pub fn kinds(mut self, kinds: &[&str]) -> Self {
        self.0.content_kinds = kinds.iter().map(|k| (*k).to_owned()).collect();
        self
    }
    pub fn lane(mut self, lane: &str) -> Self {
        self.0.lane = Some(lane.into());
        self
    }
    /// A link form that names an account.
    pub fn account(self, host: &str, path: &str) -> Self {
        self.pattern(host, path, RuleObject::Account, false)
    }
    /// A link form that names one work.
    pub fn work(self, host: &str, path: &str) -> Self {
        self.pattern(host, path, RuleObject::Entry, false)
    }
    /// A short link the adapter expands before the account or work can be told.
    pub fn short(self, host: &str, object: RuleObject) -> Self {
        self.pattern(host, "**", object, true)
    }
    fn pattern(mut self, host: &str, path: &str, object: RuleObject, expand: bool) -> Self {
        self.0.url_patterns.push(UrlPattern {
            host: host.into(),
            path: path.into(),
            object,
            expand,
        });
        self
    }
    pub fn login(mut self, login: RuleLogin, note: &str) -> Self {
        self.0.login = login;
        self.0.login_note = (!note.is_empty()).then(|| note.to_owned());
        self
    }
    pub fn limit(mut self, limit: &str) -> Self {
        self.0.limits.push(limit.into());
        self
    }
    pub fn param(mut self, key: &str, title: &str, default: &str) -> Self {
        self.0.params.push(RuleParam {
            key: key.into(),
            title: title.into(),
            kind: "bool".into(),
            default: default.into(),
            options: Vec::new(),
        });
        self
    }
    /// Declares the rule as implemented with the date and scope of its real verification.
    pub fn verified(mut self, date: &str, scope: &str) -> Self {
        self.0.status = RuleStatus::Implemented;
        self.0.verified_at = Some(date.into());
        self.0.verified_scope = Some(scope.into());
        self
    }
    pub fn status(mut self, status: RuleStatus) -> Self {
        self.0.status = status;
        self
    }
    pub fn build(self) -> RuleDeclaration {
        self.0
    }
}
