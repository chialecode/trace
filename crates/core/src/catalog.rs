//! The rule catalog: what each adapter says it supports, what Core keeps of it, and how a
//! pasted link is matched to a rule (docs/design/multi-site-contracts.md §3).
//!
//! The supported-sites page and link recognition read only the persisted catalog, so both
//! work offline and while a plugin is disabled. An adapter that declares nothing (one built
//! before rule declarations existed) gets `partial` rules derived from its capabilities and
//! marked as undeclared.
use crate::core::Core;
use crate::error::CoreError;
use rusqlite::Connection;
use trace_contracts::catalog::{Recognition, RecognizedRule, SiteCatalog, SiteView};
use trace_contracts::plugin::AdapterHello;
use trace_contracts::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleNetwork, RuleStatus};
use trace_domain::sites::{self, Site};

const UNDECLARED: &str = "适配器没有声明规则，按它已有的能力推断";

/// The rules of a handshake: the declared ones that pass validation, or derived ones.
pub fn rules_of(hello: &AdapterHello) -> Vec<RuleDeclaration> {
    let Some(site) = sites::site(&hello.platform) else {
        return Vec::new();
    };
    if hello.rules.is_empty() {
        return derived_rules(site, hello);
    }
    hello
        .rules
        .iter()
        .filter_map(|rule| {
            rule.clone()
                .normalized(site.id)
                .inspect_err(
                    |error| tracing::warn!(site = site.id, %error, "rule declaration rejected"),
                )
                .ok()
        })
        .collect()
}

fn derived_rules(site: &Site, hello: &AdapterHello) -> Vec<RuleDeclaration> {
    let has = |capability: &str| hello.capabilities.iter().any(|c| c == capability);
    let base = |id: &str, entry: RuleEntry, title: &str, lane: Option<&str>| RuleDeclaration {
        id: format!("{}.{id}", site.id),
        site: site.id.into(),
        title: format!("{}{title}", site.title),
        description: "由已有能力推断，没有适配器声明".into(),
        entry,
        content_kinds: Vec::new(),
        url_patterns: Vec::new(),
        params: Vec::new(),
        login: RuleLogin::from(site.login),
        login_note: None,
        network: RuleNetwork::from(site.network),
        limits: vec![UNDECLARED.into()],
        status: RuleStatus::Partial,
        verified_at: None,
        verified_scope: None,
        lane: lane.map(str::to_owned),
    };
    let mut rules = Vec::new();
    if has("enumerate") {
        rules.push(base(
            "user.undeclared",
            RuleEntry::Subscription,
            "用户内容（未声明）",
            Some("video"),
        ));
    }
    if has("resolve_source") {
        rules.push(base(
            "link.undeclared",
            RuleEntry::Link,
            "单个链接（未声明）",
            None,
        ));
    }
    rules
        .into_iter()
        .filter_map(|r| r.normalized(site.id).ok())
        .collect()
}

/// Asks every registered, enabled adapter for its declaration and stores it. An adapter
/// that cannot be started keeps its last stored declaration. Returns how many answered.
pub async fn refresh(core: &Core) -> Result<usize, CoreError> {
    let mut answered = 0;
    for spec in crate::pluginhost::default_adapter_specs(&core.profile) {
        if sites::site(&spec.platform).is_none() {
            continue;
        }
        let Ok(hello) = core.host.hello(&spec.platform).await else {
            continue;
        };
        let mut rules = rules_of(&hello);
        core.media_tools.annotate(&spec.platform, &mut rules).await;
        let (plugin, site, version) = (spec.plugin_id, spec.platform, hello.adapter_version);
        core.db
            .write(move |tx| {
                crate::store::rules::replace_declared(
                    tx,
                    &plugin,
                    &site,
                    &version,
                    &rules,
                    &crate::jobs::now(),
                )
            })
            .await?;
        answered += 1;
    }
    Ok(answered)
}

/// The supported-sites page: every site, in registry order, with its catalogued rules.
pub fn view(c: &Connection) -> Result<SiteCatalog, CoreError> {
    let mut out = Vec::new();
    for site in sites::SITES {
        out.push(SiteView {
            id: site.id.into(),
            title: site.title.into(),
            login: site.login.into(),
            network: site.network.into(),
            plugin_state: crate::store::rules::plugin_state(c, site.id)?,
            rules: crate::store::rules::list(c, Some(site.id))?,
        });
    }
    Ok(SiteCatalog { sites: out })
}

/// The first link in pasted text (share texts wrap links in prose), without trailing
/// punctuation. A bare `host/path` is read as an https link.
pub fn extract_url(input: &str) -> Option<String> {
    let text = input.trim();
    if text.is_empty() || text.len() > 4096 {
        return None;
    }
    let from = ["https://", "http://"]
        .iter()
        .filter_map(|p| text.find(p))
        .min();
    let candidate = match from {
        Some(start) => &text[start..],
        None => text
            .split_whitespace()
            .find(|t| t.contains('.') && t.contains('/'))
            .or_else(|| text.split_whitespace().find(|t| t.contains('.')))?,
    };
    // Share texts glue Chinese punctuation to the link: a link ends at whitespace or at
    // the first non-ASCII character, and loses trailing ASCII punctuation.
    let end = candidate
        .find(|c: char| c.is_whitespace() || !c.is_ascii())
        .unwrap_or(candidate.len());
    let token = candidate[..end].trim_end_matches([',', '.', ')', ']', '>', '"', '\'', ';']);
    if token.is_empty() {
        return None;
    }
    Some(if token.contains("://") {
        token.to_owned()
    } else {
        format!("https://{token}")
    })
}

fn empty(shown: String, message: &str) -> Recognition {
    Recognition {
        shown,
        site: None,
        site_title: None,
        object: None,
        rules: Vec::new(),
        needs_expansion: false,
        message: Some(message.into()),
    }
}

/// Matches a pasted link against the catalog of the site that owns its host.
pub fn recognize(c: &Connection, input: &str) -> Result<Recognition, CoreError> {
    let Some(raw) = extract_url(input) else {
        return Ok(empty(String::new(), "没有找到链接"));
    };
    let Ok(url) = url::Url::parse(&raw) else {
        return Ok(empty(String::new(), "链接格式不正确"));
    };
    let host = url.host_str().unwrap_or_default().to_ascii_lowercase();
    let shown = format!("{host}{}", url.path());
    if !matches!(url.scheme(), "http" | "https")
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return Ok(empty(shown, "只支持不含账号信息的 http/https 链接"));
    }
    let Some(site) = sites::site_for_host(&host) else {
        return Ok(empty(shown, "不是已支持的网站"));
    };
    let catalog = crate::store::rules::list(c, Some(site.id))?;
    let mut recognition = Recognition {
        shown,
        site: Some(site.id.into()),
        site_title: Some(site.title.into()),
        object: None,
        rules: Vec::new(),
        needs_expansion: false,
        message: None,
    };
    // Each rule offers its best-fitting pattern; only the most specific fit among all rules
    // is the intended reading (`/u/123` is an account, not user "u" with a post "123").
    let mut fits = Vec::new();
    for view in catalog.iter().filter(|v| v.declaration.usable()) {
        let best = view
            .declaration
            .url_patterns
            .iter()
            .filter_map(|p| p.matches(&host, url.path()).map(|hit| (p.expand, hit)))
            .max_by_key(|(_, hit)| hit.specificity);
        if let Some((expand, hit)) = best {
            fits.push((&view.declaration, expand, hit));
        }
    }
    let top = fits.iter().map(|(_, _, hit)| hit.specificity).max();
    for (rule, expand, hit) in fits
        .into_iter()
        .filter(|(_, _, h)| Some(h.specificity) == top)
    {
        recognition.needs_expansion |= expand;
        recognition.object.get_or_insert(hit.object);
        recognition.rules.push(RecognizedRule {
            rule_id: rule.id.clone(),
            title: rule.title.clone(),
            entry: rule.entry,
            object: hit.object,
            status: rule.status,
            login: rule.login,
            network: rule.network,
            lane: rule.lane.clone(),
            captures: hit.captures,
        });
    }
    recognition
        .rules
        .sort_by(|a, b| (a.entry as u8, &a.rule_id).cmp(&(b.entry as u8, &b.rule_id)));
    if recognition.rules.is_empty() {
        recognition.message = Some(if catalog.is_empty() {
            format!("{} 的规则还没有加载，请稍后再试", site.title)
        } else {
            format!(
                "识别为 {} 的链接，但没有对应的规则（可能是暂不支持的页面）",
                site.title
            )
        });
    }
    Ok(recognition)
}

/// The subscription lanes a site offers: those of its usable subscription rules, or the
/// legacy lanes for a site whose adapter predates rule declarations.
pub fn lanes(c: &Connection, site: &str) -> Result<Vec<String>, CoreError> {
    let mut lanes: Vec<String> = crate::store::rules::list(c, Some(site))?
        .into_iter()
        .filter(|v| v.declaration.entry == RuleEntry::Subscription && v.declaration.usable())
        .filter_map(|v| v.declaration.lane)
        .collect();
    lanes.sort();
    lanes.dedup();
    Ok(lanes)
}
