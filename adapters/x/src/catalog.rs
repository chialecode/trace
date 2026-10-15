//! The web client's query ids, feature switches and request-signing material.
//!
//! X changes the id of a GraphQL operation whenever the operation changes, so ids are data
//! that ships next to the adapter (`data/web_client.json`, updated like any other data file)
//! and that the site itself publishes: the page lists its scripts, and the scripts list every
//! operation with the switches it needs and the public token the client sends. The shipped
//! data is used first, without a request; the site's scripts are read only when the site
//! rejects it, and the answer (or the failure) is remembered so a changed site costs a few
//! requests per interval, not per call. The same read of the home page yields the material
//! of the `x-client-transaction-id` header (`transaction.rs`) for the calls that need it.
use crate::api::ORIGIN;
use crate::transaction::{self, Material};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::sync::{Arc, OnceLock};
use std::time::{Duration, Instant};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::{self, net, page};
use trace_site_support::runtime::Transport;

/// The operations this adapter calls.
pub(crate) const NEEDED: [&str; 3] = ["UserByScreenName", "UserTweets", "TweetResultByRestId"];
/// Scripts the page lists that are read at most per discovery, and the on-demand chunks read
/// after them when the listed scripts do not hold every operation.
const SCRIPTS_MAX: usize = 6;
const CHUNKS_MAX: usize = 3;
const SCRIPT_HOST: &str = "https://abs.twimg.com/";
const BEARER_PREFIX: &str = "AAAAAAAAAAAAAAAAAAAAA";

/// One operation: its id, the feature switches with the value the client sends for each, and
/// the field toggles (always sent off).
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Operation {
    pub id: String,
    pub features: Vec<(String, bool)>,
    pub toggles: Vec<String>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub(crate) struct Catalog {
    pub bearer: Option<String>,
    pub operations: BTreeMap<String, Operation>,
    /// What signs the calls that need the transaction id; only a catalog read from the site
    /// has it.
    pub material: Option<Arc<Material>>,
}

/// `data/web_client.json`: shipped with the adapter, read once.
fn shipped() -> Arc<Catalog> {
    static SHIPPED: OnceLock<Arc<Catalog>> = OnceLock::new();
    SHIPPED
        .get_or_init(|| {
            let data: Value = serde_json::from_str(include_str!("../data/web_client.json"))
                .unwrap_or(Value::Null);
            Arc::new(from_data(&data))
        })
        .clone()
}

fn from_data(data: &Value) -> Catalog {
    let mut catalog = Catalog {
        bearer: data["bearer"].as_str().map(str::to_owned),
        ..Catalog::default()
    };
    for (name, op) in data["operations"].as_object().into_iter().flatten() {
        let (Some(id), Some(features)) = (op["id"].as_str(), op["features"].as_object()) else {
            continue;
        };
        catalog.operations.insert(
            name.clone(),
            Operation {
                id: id.to_owned(),
                features: features
                    .iter()
                    .map(|(k, v)| (k.clone(), v.as_bool().unwrap_or(true)))
                    .collect(),
                toggles: op["toggles"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|t| t.as_str().map(str::to_owned))
                    .collect(),
            },
        );
    }
    catalog
}

impl Catalog {
    pub(crate) fn operation(&self, name: &str) -> Result<&Operation, PluginError> {
        self.operations
            .get(name)
            .ok_or_else(|| kit::schema("the web client does not list a needed operation"))
    }

    fn complete(&self) -> bool {
        self.bearer.is_some() && NEEDED.iter().all(|n| self.operations.contains_key(*n))
    }

    /// Adds what a script lists; what is already known is kept.
    pub(crate) fn read(&mut self, script: &str) {
        if self.bearer.is_none() {
            self.bearer = bearer_in(script);
        }
        for (name, op) in operations_in(script) {
            self.operations.entry(name).or_insert(op);
        }
    }
}

/// The public token of the web client (the same for every visitor).
fn bearer_in(script: &str) -> Option<String> {
    let start = script.find(BEARER_PREFIX)?;
    let token: String = script[start..]
        .chars()
        .take_while(|c| c.is_ascii_alphanumeric() || matches!(c, '%' | '=' | '-' | '_'))
        .collect();
    (token.len() >= 80 && token.len() <= 200).then_some(token)
}

/// `"a","b"` up to the closing bracket.
fn quoted_list(text: &str) -> Vec<String> {
    let Some(end) = text.find(']') else {
        return Vec::new();
    };
    text[..end]
        .split(',')
        .filter_map(|item| {
            item.trim()
                .trim_matches('"')
                .split('"')
                .next()
                .map(str::to_owned)
        })
        .filter(|name| {
            !name.is_empty() && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
        })
        .collect()
}

fn list_after(block: &str, marker: &str) -> Vec<String> {
    block
        .find(marker)
        .map(|i| quoted_list(&block[i + marker.len()..]))
        .unwrap_or_default()
}

/// Every `queryId:"…",operationName:"…"` the script lists, with its switches and toggles.
fn operations_in(script: &str) -> Vec<(String, Operation)> {
    const ID: &str = "queryId:\"";
    const NAME: &str = ",operationName:\"";
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(found) = script[from..].find(ID) {
        let start = from + found + ID.len();
        from = start;
        let Some(id_end) = script[start..].find('"') else {
            break;
        };
        let id = &script[start..start + id_end];
        let after = &script[start + id_end + 1..];
        let Some(name_rest) = after.strip_prefix(NAME) else {
            continue;
        };
        let Some(name_end) = name_rest.find('"') else {
            continue;
        };
        let name = &name_rest[..name_end];
        // The block of this operation ends where the next one starts.
        let tail = &name_rest[name_end..];
        let block = &tail[..tail.find(ID).unwrap_or(tail.len()).min(8000)];
        let valid = |s: &str| {
            !s.is_empty()
                && s.len() <= 64
                && s.chars()
                    .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
        };
        if !valid(id) || !valid(name) {
            continue;
        }
        out.push((
            name.to_owned(),
            Operation {
                id: id.to_owned(),
                features: list_after(block, "featureSwitches:[")
                    .into_iter()
                    .map(|name| (name, true))
                    .collect(),
                toggles: list_after(block, "fieldToggles:["),
            },
        ));
    }
    out
}

/// The scripts the main script loads on demand: webpack's `e=>e+"."+{name:"hash",…}[e]+"a.js"`,
/// as the addresses next to the main script. The ones that look like the API's come first.
fn chunk_urls(script_url: &str, script: &str) -> Vec<String> {
    const END: &str = "\"a.js\"";
    let Some(end) = script.find(END) else {
        return Vec::new();
    };
    let head = &script[..end];
    let Some(close) = head.rfind('}') else {
        return Vec::new();
    };
    let Some(open) = head[..close].rfind('{') else {
        return Vec::new();
    };
    let Some(dir) = script_url.rfind('/').map(|i| &script_url[..=i]) else {
        return Vec::new();
    };
    let mut named: Vec<(String, String)> = head[open + 1..close]
        .split(',')
        .filter_map(|entry| {
            let (name, hash) = entry.split_once(':')?;
            let (name, hash) = (name.trim().trim_matches('"'), hash.trim().trim_matches('"'));
            let name_ok = !name.is_empty()
                && name.len() <= 64
                && name
                    .chars()
                    .all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-' | '~'));
            let hash_ok =
                (4..=16).contains(&hash.len()) && hash.chars().all(|c| c.is_ascii_alphanumeric());
            (name_ok && hash_ok).then(|| (name.to_owned(), format!("{dir}{name}.{hash}a.js")))
        })
        .collect();
    named.sort_by_key(|(name, _)| {
        (
            name != "api",
            !name.starts_with("api"),
            !name.to_ascii_lowercase().contains("api"),
        )
    });
    named
        .into_iter()
        .filter(|(name, _)| name.to_ascii_lowercase().contains("api"))
        .map(|(_, url)| url)
        .collect()
}

fn script_headers() -> serde_json::Value {
    json!({"referer": format!("{ORIGIN}/"), "accept": "*/*"})
}

/// Reads the home page and the site's scripts: the operations, and the material of the
/// transaction id when the page and its on-demand script allow it (a page without it gives a
/// catalog that cannot sign, which is only a problem for a site that asks for signatures).
fn discover(net: &dyn Transport) -> Result<Catalog, PluginError> {
    // The visitor's own page: a visitor without a session is served a bare shell that lists one
    // small script, the web client's scripts are listed to the signed-in page.
    let home = net::text(net, &format!("{ORIGIN}/"), crate::api::page_headers(), true)?;
    let mut catalog = operations(net, &home)?;
    catalog.material = material(net, &home);
    Ok(catalog)
}

/// The transaction id's material: the page's key and animations, and the script that says which
/// bytes of the key to use.
fn material(net: &dyn Transport, home: &str) -> Option<Arc<Material>> {
    let script = net::text(
        net,
        &transaction::ondemand_url(home)?,
        script_headers(),
        false,
    )
    .ok()?;
    Material::read(home, &script).ok().map(Arc::new)
}

/// Reads the page's scripts (the main one first) and, when those do not hold every operation,
/// the API chunks the main script loads on demand, until everything is found.
fn operations(net: &dyn Transport, home: &str) -> Result<Catalog, PluginError> {
    let listed = page::script_srcs(home);
    let mut scripts: Vec<String> = listed
        .iter()
        .filter(|s| s.starts_with(SCRIPT_HOST) && s.ends_with(".js"))
        .cloned()
        .collect();
    scripts.sort_by_key(|s| !s.contains("/main."));
    let mut catalog = Catalog::default();
    let mut chunks: Vec<String> = Vec::new();
    let mut seen = Vec::new();
    for script in scripts.iter().take(SCRIPTS_MAX) {
        let text = net::text(net, script, script_headers(), false)?;
        catalog.read(&text);
        if catalog.complete() {
            return Ok(catalog);
        }
        let found = chunk_urls(script, &text);
        seen.push(format!("{} bytes, {} api chunks", text.len(), found.len()));
        chunks.extend(found);
    }
    let mut read = 0;
    for chunk in chunks.iter().take(CHUNKS_MAX) {
        catalog.read(&net::text(net, chunk, script_headers(), false)?);
        read += 1;
        if catalog.complete() {
            return Ok(catalog);
        }
    }
    let missing: Vec<&str> = NEEDED
        .iter()
        .copied()
        .filter(|n| !catalog.operations.contains_key(*n))
        .collect();
    Err(kit::schema(&format!(
        "the web client's scripts do not list the needed operations (page lists {} scripts, {} on the script host; read [{}]; {read} api chunks read; {} operations, token {}, missing {})",
        listed.len(),
        scripts.len(),
        seen.join("; "),
        catalog.operations.len(),
        catalog.bearer.is_some(),
        missing.join(",")
    )))
}

/// What the process remembers of the site, apart from the shipped data.
#[derive(Default)]
struct Known {
    /// The last catalog read from the site, and when.
    read: Option<(Instant, Arc<Catalog>)>,
    /// The last failure to read it, and when.
    failed: Option<(Instant, PluginError)>,
    /// The site rejected calls without the transaction id and accepted the same call with it.
    signing: bool,
}

/// The one memory of the process (tests keep one per thread, so they stay independent).
mod cache {
    use super::Known;

    #[cfg(not(test))]
    static SLOT: std::sync::Mutex<Option<Known>> = std::sync::Mutex::new(None);
    #[cfg(not(test))]
    pub(super) fn with<R>(f: impl FnOnce(&mut Known) -> R) -> R {
        f(SLOT.lock().unwrap().get_or_insert_with(Known::default))
    }

    #[cfg(test)]
    thread_local! {
        static SLOT: std::cell::RefCell<Known> = std::cell::RefCell::new(Known::default());
    }
    #[cfg(test)]
    pub(super) fn with<R>(f: impl FnOnce(&mut Known) -> R) -> R {
        SLOT.with(|s| f(&mut s.borrow_mut()))
    }
}

/// A catalog read from the site is trusted for this long, and a catalog read just now is not
/// read again however often the site rejects it.
const TTL: Duration = Duration::from_secs(6 * 3600);
const READ_AGAIN_AFTER: Duration = Duration::from_secs(120);
/// A read that failed is not repeated for this long: a site whose scripts changed answers the
/// same way again, and every try costs requests against its budget.
const RETRY_AFTER_FAILURE: Duration = Duration::from_secs(900);

/// What to call the site with: the catalog read from it while that is recent, else the shipped
/// data, which costs nothing. A site known to want signed calls is read for its material first.
pub(crate) fn get(net: &dyn Transport) -> Result<Arc<Catalog>, PluginError> {
    let (read, signing) = cache::with(|known| {
        (
            known
                .read
                .as_ref()
                .filter(|(at, _)| at.elapsed() < TTL)
                .map(|(_, catalog)| catalog.clone()),
            known.signing,
        )
    });
    match read {
        Some(catalog) => Ok(catalog),
        None if signing => refresh(net),
        None => Ok(shipped()),
    }
}

/// The catalog from the site's own scripts, because what was used was rejected. A catalog read
/// moments ago, and a failure moments ago, are answered from memory.
pub(crate) fn refresh(net: &dyn Transport) -> Result<Arc<Catalog>, PluginError> {
    let remembered = cache::with(|known| {
        if let Some((at, catalog)) = &known.read
            && at.elapsed() < READ_AGAIN_AFTER
        {
            return Some(Ok(catalog.clone()));
        }
        known
            .failed
            .as_ref()
            .filter(|(at, _)| at.elapsed() < RETRY_AFTER_FAILURE)
            .map(|(_, error)| Err(error.clone()))
    });
    if let Some(remembered) = remembered {
        return remembered;
    }
    let result = discover(net).map(Arc::new);
    cache::with(|known| match &result {
        Ok(catalog) => {
            known.read = Some((Instant::now(), catalog.clone()));
            known.failed = None;
        }
        Err(error) => known.failed = Some((Instant::now(), error.clone())),
    });
    result
}

/// The site accepted a call only once it was signed: later reads of the site include signing
/// material, and an old one is read again before it is used.
pub(crate) fn needs_signing() {
    cache::with(|known| known.signing = true);
}

/// Discards what is remembered (tests start from the shipped data).
#[cfg(test)]
pub(crate) fn forget() {
    cache::with(|known| *known = Known::default());
}

#[cfg(test)]
mod tests {
    use super::*;

    const SCRIPT: &str = r#"e.exports={queryId:"AbC-123_x",operationName:"UserTweets",operationType:"query",metadata:{featureSwitches:["rweb_a","rweb_b"],fieldToggles:["withArticlePlainText"]}}},n={queryId:"Zz9",operationName:"UserByScreenName",operationType:"query",metadata:{featureSwitches:["rweb_c"]}};var t="Bearer AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA";queryId:"bad id",operationName:"X""#;

    #[test]
    fn operations_switches_and_the_public_token_are_read_from_a_script() {
        let mut catalog = Catalog::default();
        catalog.read(SCRIPT);
        let tweets = &catalog.operations["UserTweets"];
        assert_eq!(tweets.id, "AbC-123_x");
        assert_eq!(
            tweets.features,
            [("rweb_a".to_owned(), true), ("rweb_b".to_owned(), true)]
        );
        assert_eq!(tweets.toggles, ["withArticlePlainText"]);
        let user = &catalog.operations["UserByScreenName"];
        assert_eq!(
            (
                user.id.as_str(),
                user.features.as_slice(),
                user.toggles.len()
            ),
            ("Zz9", [("rweb_c".to_owned(), true)].as_slice(), 0)
        );
        assert_eq!(
            catalog.operations.len(),
            2,
            "an id with a space is not an id"
        );
        assert!(
            catalog
                .bearer
                .as_deref()
                .unwrap()
                .starts_with("AAAAAAAAAAAAAAAAAAAAA")
        );
        assert!(!catalog.complete());
    }

    #[test]
    fn api_chunks_are_found_in_the_main_scripts_chunk_map() {
        let main = r#"var o=e=>e+"."+{"loader.Abc":"1a2b3c","api":"9d8dcb8","bundle.Profile":"ffee0011",ondemand_s:"abcd1234","i18n/en":"77aa88bb"}[e]+"a.js";"#;
        let url = "https://abs.twimg.com/responsive-web/client-web/main.bbbb2222.js";
        assert_eq!(
            chunk_urls(url, main),
            ["https://abs.twimg.com/responsive-web/client-web/api.9d8dcb8a.js"],
            "only the chunks that look like the API's"
        );
        assert!(chunk_urls(url, "nothing to load").is_empty());
        assert!(chunk_urls("not a url", main).is_empty());
    }

    #[test]
    fn the_shipped_data_names_every_needed_operation_with_the_values_the_client_declares() {
        let shipped = shipped();
        assert!(shipped.complete(), "{shipped:?}");
        assert!(
            shipped.material.is_none(),
            "signing material comes from the site"
        );
        for name in NEEDED {
            let op = shipped.operation(name).unwrap();
            assert!(
                op.id.len() >= 20
                    && op
                        .id
                        .chars()
                        .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'),
                "{name}: {}",
                op.id
            );
            assert!(op.features.len() >= 10, "{name}");
            assert!(
                op.features.iter().any(|(_, on)| !on),
                "{name}: a declared 'off' is sent off, not forced on"
            );
            assert!(
                op.toggles.contains(&"withAuxiliaryUserLabels".to_owned()),
                "{name}"
            );
        }
        assert!(
            shipped
                .bearer
                .as_deref()
                .unwrap()
                .starts_with(BEARER_PREFIX)
        );
        // The shipped data survives the reading rules the site's scripts are held to.
        assert_eq!(
            bearer_in(shipped.bearer.as_deref().unwrap()),
            shipped.bearer
        );
    }

    #[test]
    fn data_that_cannot_be_read_gives_no_operations_instead_of_wrong_ones() {
        let read = |data: &str| from_data(&serde_json::from_str(data).unwrap());
        assert!(
            read(r#"{"operations":{"A":{"id":"x"}}}"#)
                .operations
                .is_empty()
        );
        assert!(
            read(r#"{"operations":{"A":{"features":{}}}}"#)
                .operations
                .is_empty()
        );
        let one = read(
            r#"{"bearer":"b","operations":{"A":{"id":"i","features":{"f":false,"g":true,"h":7},"toggles":["t",3]}}}"#,
        );
        let a = &one.operations["A"];
        assert_eq!(
            a.features,
            [
                ("f".to_owned(), false),
                ("g".to_owned(), true),
                ("h".to_owned(), true)
            ]
        );
        assert_eq!(a.toggles, ["t"]);
    }

    #[test]
    fn what_is_known_is_kept_and_a_missing_operation_is_a_changed_shape() {
        let mut catalog = Catalog::default();
        catalog.read(SCRIPT);
        catalog.read(r#"queryId:"Other",operationName:"UserTweets",metadata:{}"#);
        assert_eq!(catalog.operations["UserTweets"].id, "AbC-123_x");
        assert_eq!(
            catalog.operation("TweetResultByRestId").unwrap_err().class,
            trace_domain::errors::FailureClass::SchemaChanged
        );
    }
}
