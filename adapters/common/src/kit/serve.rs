//! The plugin protocol for the sites that share the [`Work`] model: one trait to fill in,
//! one dispatcher that every such adapter runs.
use super::Work;
use super::memo::Recent;
use crate::error;
use crate::runtime::{self, Transport};
use serde_json::Value;
use trace_contracts::plugin::{
    AdapterHello, AdapterLimits, EnumerateParams, EnumeratedPage, ObserveParams, PluginError,
    PluginRequest, ProbeAuthResult, ResolveAccountParams, ResolveMediaParams, ResolveMediaResult,
    ResolvedAccount,
};
use trace_contracts::rules::RuleDeclaration;
use trace_contracts::source::ResolvedSource;
use trace_domain::errors::FailureClass;

/// Works read in the last two minutes. What `observe` or a link preview read serves the media
/// resolution of each group that follows, instead of one more read of the site per group.
static RECENT_WORKS: Recent<Work> = Recent::new(std::time::Duration::from_secs(120), 8);

/// A single-work link, once understood.
#[derive(Debug)]
pub struct Link {
    pub key: String,
    /// The native kind the list uses for this kind of work.
    pub kind: String,
    pub canonical: String,
}

/// What a site module provides. Every method makes bounded requests through `client` and
/// maps what comes back; none of them touches a Cookie, a file or a database.
pub trait SiteAdapter: Send + Sync + 'static {
    /// The site id of the registry; also the adapter's platform name.
    fn id(&self) -> &'static str;
    fn rules(&self) -> Vec<RuleDeclaration>;
    fn resolve_account(
        &self,
        client: &dyn Transport,
        input: &str,
    ) -> Result<ResolvedAccount, PluginError>;
    /// One page of the account's list for the lane in `params`.
    fn enumerate(
        &self,
        client: &dyn Transport,
        params: &EnumerateParams,
    ) -> Result<EnumeratedPage, PluginError>;
    /// The same page, with the host's access context. Only a site whose public list API needs
    /// a device identifier overrides this; the context never carries a login Cookie.
    fn enumerate_with_context(
        &self,
        client: &dyn Transport,
        params: &EnumerateParams,
        _context: &Value,
    ) -> Result<EnumeratedPage, PluginError> {
        self.enumerate(client, params)
    }
    /// One work in full: text, assets with their variants, unified fields.
    fn work(&self, client: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError>;
    /// Understands a link to a single work; may expand a short link through the site.
    fn link(&self, client: &dyn Transport, input: &str) -> Result<Link, PluginError>;
    fn probe_auth(&self, client: &dyn Transport) -> Result<ProbeAuthResult, PluginError>;
}

/// Runs the adapter until the host shuts it down.
pub fn run<S: SiteAdapter>(site: S) {
    runtime::serve_with(site, dispatch::<S>);
}

fn invalid(message: &str) -> PluginError {
    error(FailureClass::InvalidPayload, message)
}

fn params<T: serde::de::DeserializeOwned>(value: &Value, what: &str) -> Result<T, PluginError> {
    serde_json::from_value(value.clone()).map_err(|_| invalid(what))
}

fn encode<T: serde::Serialize>(value: &T) -> Result<Value, PluginError> {
    serde_json::to_value(value).map_err(|_| invalid("response serialization"))
}

pub fn hello<S: SiteAdapter>(site: &S) -> AdapterHello {
    AdapterHello {
        protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
        adapter_version: env!("CARGO_PKG_VERSION").into(),
        platform: site.id().into(),
        capabilities: [
            "resolve_account",
            "enumerate",
            "observe",
            "resolve_media",
            "probe_auth",
            "resolve_source",
            "plugin_chunks_v1",
        ]
        .map(String::from)
        .to_vec(),
        limits: AdapterLimits {
            max_page_items: trace_domain::MAX_PAGE_ITEMS,
            supports_cancel: false,
        },
        rules: site.rules(),
    }
}

fn dispatch<S: SiteAdapter>(
    site: &S,
    client: &runtime::Client,
    req: &PluginRequest,
) -> Result<Value, PluginError> {
    let p = &req.params;
    match req.method.as_str() {
        "hello" => encode(&hello(site)),
        "resolve_account" => {
            let p: ResolveAccountParams = params(p, "account parameters")?;
            let account = site.resolve_account(client, &p.input)?;
            if account.native_account_key.is_empty() || account.native_account_key.len() > 256 {
                return Err(error(FailureClass::SchemaChanged, "account identity"));
            }
            encode(&account)
        }
        "enumerate" => {
            let p: EnumerateParams = params(p, "enumeration parameters")?;
            let page = site.enumerate_with_context(client, &p, &req.context)?;
            encode(&checked_page(page, &p)?)
        }
        "observe" => {
            let p: ObserveParams = params(p, "observation parameters")?;
            let work = read_work(site, client, &p.native_entry_key, &p.native_kind, false)?;
            encode(&work.observed())
        }
        "resolve_media" => {
            let p: ResolveMediaParams = params(p, "media parameters")?;
            let work = read_work(site, client, &p.native_entry_key, &p.native_kind, true)?;
            let all = p.quality_hint["all_variants"] == true;
            let prefer_fps = p.quality_hint["prefer_fps"] == true;
            let candidates = work
                .candidates(all, prefer_fps)
                .into_iter()
                .filter(|c| {
                    p.resources
                        .iter()
                        .any(|r| r.source_attachment_id == c.resource.source_attachment_id)
                })
                .collect();
            encode(&ResolveMediaResult {
                candidates,
                notes: work.notes(),
            })
        }
        "probe_auth" => encode(&site.probe_auth(client)?),
        "resolve_source" => {
            let input = p["input"].as_str().ok_or_else(|| invalid("link"))?;
            encode(&resolve_source(site, client, input)?)
        }
        _ => Err(error(FailureClass::Unsupported, "unsupported method")),
    }
}

/// One work. A fresh read asks the site and remembers the answer; with `reuse`, a read of the
/// same work a moment ago serves instead.
fn read_work<S: SiteAdapter>(
    site: &S,
    client: &dyn Transport,
    key: &str,
    kind: &str,
    reuse: bool,
) -> Result<Work, PluginError> {
    let memo = format!("{}/{kind}/{key}", site.id());
    if reuse && let Some(work) = RECENT_WORKS.get(&memo) {
        return Ok(work);
    }
    let work = site.work(client, key, kind)?;
    identity_matches(&work, key, kind)?;
    RECENT_WORKS.put(&memo, work.clone());
    Ok(work)
}

fn identity_matches(work: &Work, key: &str, kind: &str) -> Result<(), PluginError> {
    if work.key != key || work.kind != kind {
        return Err(error(
            FailureClass::SchemaChanged,
            "detail identity mismatch or empty detail",
        ));
    }
    Ok(())
}

/// How far an approximate date may be off when the adapter did not say: a year either way.
const APPROXIMATE_MARGIN: i64 = 366 * 86_400;

/// A list read only for its last days ends at the first page without anything newer, so a
/// check of what is new this week does not walk an author's whole history. Pinned works sit out
/// of order and a work without a date cannot be placed, so neither ends the reading.
fn nothing_recent(page: &EnumeratedPage, params: &EnumerateParams) -> bool {
    let Some(days) = params.recent_days.filter(|d| *d > 0) else {
        return false;
    };
    let cutoff = time::OffsetDateTime::now_utc()
        .unix_timestamp()
        .saturating_sub(days.saturating_mul(86_400));
    let mut older = false;
    for entry in page.items.iter().filter(|e| !e.pinned) {
        let at = entry.published_at.as_deref().and_then(|text| {
            time::OffsetDateTime::parse(text, &time::format_description::well_known::Rfc3339).ok()
        });
        // An approximate date (a list's "3 days ago") counts with its margin: the entry may
        // be a unit younger than it reads.
        let margin = if entry.published_precision == "approximate" {
            entry.published_margin_seconds.unwrap_or(APPROXIMATE_MARGIN)
        } else {
            0
        };
        match at {
            Some(at) if at.unix_timestamp().saturating_add(margin) < cutoff => older = true,
            _ => return false,
        }
    }
    older
}

/// A page the host can trust: bounded, without repeats, moving on when it claims more, and
/// never an unexplained empty start of a list.
pub fn checked_page(
    mut page: EnumeratedPage,
    params: &EnumerateParams,
) -> Result<EnumeratedPage, PluginError> {
    if page.items.len() > trace_domain::MAX_PAGE_ITEMS as usize {
        return Err(error(FailureClass::SchemaChanged, "oversized page"));
    }
    let mut seen = std::collections::BTreeSet::new();
    page.items
        .retain(|e| !e.native_entry_key.is_empty() && seen.insert(e.native_entry_key.clone()));
    if page.has_more && nothing_recent(&page, params) {
        page.has_more = false;
        page.next_cursor = None;
    }
    if page.has_more && (page.next_cursor.is_none() || page.next_cursor == params.cursor) {
        return Err(error(
            FailureClass::SchemaChanged,
            "continuation does not advance",
        ));
    }
    if !page.has_more {
        page.next_cursor = None;
    }
    // The first page of a list that comes back empty is only believed when the adapter had
    // evidence that the account has nothing yet; otherwise it is a refusal in disguise.
    if page.items.is_empty() && params.cursor.is_none() && page.coverage["empty_confirmed"] != true
    {
        return Err(error(
            FailureClass::SourceUnavailable,
            "empty list is not confirmed",
        ));
    }
    Ok(page)
}

/// A single link to everything Core needs to offer, preview and download it.
pub fn resolve_source<S: SiteAdapter>(
    site: &S,
    client: &dyn Transport,
    input: &str,
) -> Result<ResolvedSource, PluginError> {
    let link = site.link(client, input)?;
    let work = read_work(site, client, &link.key, &link.kind, false)?;
    if work.author_key.is_empty() {
        return Err(error(FailureClass::SchemaChanged, "work owner identity"));
    }
    let detail = work.observed();
    let media = work.candidates(true, false);
    Ok(ResolvedSource {
        kind: if work.kind == "video" {
            "video"
        } else {
            "entry"
        }
        .into(),
        canonical_url: work.canonical().map_or(link.canonical, str::to_owned),
        native_account_key: work.author_key.clone(),
        display_name: work.author_name.clone(),
        source_duration_seconds: work.duration_seconds(),
        detail: Some(detail),
        media: ResolveMediaResult {
            candidates: media,
            notes: work.notes(),
        },
        live: None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use trace_contracts::plugin::EnumeratedEntry;

    fn entry(key: &str) -> EnumeratedEntry {
        EnumeratedEntry {
            native_entry_key: key.into(),
            native_kind: "post".into(),
            title: String::new(),
            published_at: None,
            published_precision: "unknown".into(),
            published_margin_seconds: None,
            pinned: false,
            summary_body: None,
            stats: Value::Null,
            hints: None,
        }
    }

    fn page(items: &[&str], more: bool, cursor: Option<&str>) -> EnumeratedPage {
        EnumeratedPage {
            items: items.iter().map(|k| entry(k)).collect(),
            next_cursor: cursor.map(str::to_owned),
            has_more: more,
            coverage: json!({}),
        }
    }

    fn params(cursor: Option<&str>) -> EnumerateParams {
        EnumerateParams {
            content_type: "post".into(),
            native_account_key: "1".into(),
            mode: "history".into(),
            cursor: cursor.map(str::to_owned),
            recent_days: None,
            max_pages: 1,
        }
    }

    struct Counting(std::sync::atomic::AtomicUsize);

    impl SiteAdapter for Counting {
        fn id(&self) -> &'static str {
            "memo-test"
        }
        fn rules(&self) -> Vec<RuleDeclaration> {
            Vec::new()
        }
        fn resolve_account(
            &self,
            _: &dyn Transport,
            _: &str,
        ) -> Result<ResolvedAccount, PluginError> {
            Err(error(FailureClass::Unsupported, "unused"))
        }
        fn enumerate(
            &self,
            _: &dyn Transport,
            _: &EnumerateParams,
        ) -> Result<EnumeratedPage, PluginError> {
            Err(error(FailureClass::Unsupported, "unused"))
        }
        fn work(&self, _: &dyn Transport, key: &str, kind: &str) -> Result<Work, PluginError> {
            self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            Ok(Work::new(key, kind))
        }
        fn link(&self, _: &dyn Transport, _: &str) -> Result<Link, PluginError> {
            Err(error(FailureClass::Unsupported, "unused"))
        }
        fn probe_auth(&self, _: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
            Err(error(FailureClass::Unsupported, "unused"))
        }
    }

    struct Silent;

    impl Transport for Silent {
        fn request(
            &self,
            _: &runtime::Fetch,
        ) -> Result<trace_contracts::plugin::ProxyFetchResult, PluginError> {
            Err(error(FailureClass::Unsupported, "no request expected"))
        }
    }

    #[test]
    fn the_media_of_every_group_reuses_the_work_that_was_just_read() {
        let site = Counting(Default::default());
        let reads = || site.0.load(std::sync::atomic::Ordering::SeqCst);
        read_work(&site, &Silent, "w1", "post", false).unwrap();
        for _ in 0..5 {
            read_work(&site, &Silent, "w1", "post", true).unwrap();
        }
        assert_eq!(reads(), 1, "five media groups cost no further read");
        read_work(&site, &Silent, "w2", "post", true).unwrap();
        assert_eq!(reads(), 2, "another work is read once");
        read_work(&site, &Silent, "w1", "post", false).unwrap();
        assert_eq!(reads(), 3, "an observation is always a fresh read");
    }

    #[test]
    fn repeats_and_blank_keys_are_dropped_and_a_finished_list_has_no_cursor() {
        let checked = checked_page(
            page(&["a", "a", "", "b"], false, Some("stale")),
            &params(None),
        )
        .unwrap();
        let keys: Vec<_> = checked
            .items
            .iter()
            .map(|e| e.native_entry_key.as_str())
            .collect();
        assert_eq!(keys, ["a", "b"]);
        assert_eq!(checked.next_cursor, None);
    }

    fn dated(key: &str, at: &str, pinned: bool) -> EnumeratedEntry {
        EnumeratedEntry {
            published_at: Some(at.into()),
            published_precision: "source".into(),
            published_margin_seconds: None,
            pinned,
            ..entry(key)
        }
    }

    #[test]
    fn a_reading_of_the_last_days_ends_at_the_first_page_with_nothing_newer() {
        let now = time::OffsetDateTime::now_utc();
        let days_ago = |n: i64| {
            (now - time::Duration::days(n))
                .format(&time::format_description::well_known::Rfc3339)
                .unwrap()
        };
        let recent = EnumerateParams {
            recent_days: Some(7),
            ..params(Some("c1"))
        };
        let more = |items: Vec<EnumeratedEntry>| EnumeratedPage {
            items,
            next_cursor: Some("c2".into()),
            has_more: true,
            coverage: json!({}),
        };
        // Newer works on the page: the reading goes on.
        let page = more(vec![
            dated("a", &days_ago(1), false),
            dated("b", &days_ago(30), false),
        ]);
        assert!(checked_page(page, &recent).unwrap().has_more);
        // Only older works, a pinned old one among them: it ends here.
        let page = more(vec![
            dated("p", &days_ago(900), true),
            dated("b", &days_ago(30), false),
        ]);
        let ended = checked_page(page, &recent).unwrap();
        assert!(!ended.has_more && ended.next_cursor.is_none());
        // A pinned work is not a reason to stop, nor an undated one a reason to go on or off.
        let page = more(vec![dated("p", &days_ago(900), true), entry("u")]);
        assert!(checked_page(page, &recent).unwrap().has_more);
        // An approximate date ("N days ago") counts with a unit of margin: "7 days ago" may
        // still be inside the last 7 days, "9 days ago" cannot.
        let approx = |key: &str, days: i64| EnumeratedEntry {
            published_precision: "approximate".into(),
            published_margin_seconds: Some(86_400),
            ..dated(key, &days_ago(days), false)
        };
        let page = more(vec![approx("x", 7), approx("y", 30)]);
        assert!(checked_page(page, &recent).unwrap().has_more);
        let page = more(vec![approx("x", 9), approx("y", 30)]);
        assert!(!checked_page(page, &recent).unwrap().has_more);
        // Without a limit of days nothing is cut.
        let page = more(vec![dated("b", &days_ago(30), false)]);
        assert!(checked_page(page, &params(Some("c1"))).unwrap().has_more);
    }

    #[test]
    fn a_claim_of_more_must_move_on() {
        assert!(checked_page(page(&["a"], true, None), &params(None)).is_err());
        assert!(checked_page(page(&["a"], true, Some("c1")), &params(Some("c1"))).is_err());
        assert!(checked_page(page(&["a"], true, Some("c2")), &params(Some("c1"))).is_ok());
    }

    #[test]
    fn an_empty_start_of_a_list_is_a_refusal_unless_the_adapter_proved_it() {
        let err = checked_page(page(&[], false, None), &params(None)).unwrap_err();
        assert_eq!(err.class, FailureClass::SourceUnavailable);
        let mut proven = page(&[], false, None);
        proven.coverage = json!({"empty_confirmed": true});
        assert!(
            checked_page(proven, &params(None))
                .unwrap()
                .items
                .is_empty()
        );
        // The end of a list that was being read is an ordinary empty page.
        assert!(checked_page(page(&[], false, None), &params(Some("c9"))).is_ok());
    }
}
