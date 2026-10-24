//! The ids of the web client's GraphQL operations.
//!
//! Instagram changes an operation's `doc_id` whenever the operation changes, so the ids are
//! data. Ids named in `data/web_client.json` are used first; any other is read from the
//! scripts the page loads, where the web client defines each as a module named after the
//! operation. A rejected id is read again once; a second rejection is a changed interface.
use crate::web;
use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::{Duration, Instant};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::{self, net};
use trace_site_support::runtime::Transport;

pub(crate) const PROFILE: &str = "PolarisProfilePageContentQuery";
pub(crate) const POSTS: &str = "PolarisProfilePostsQuery";
/// The web client pages a profile with this operation; when it is not found the first
/// operation is asked for the next page.
pub(crate) const MORE: &str = "PolarisProfilePostsTabContentQuery_connection";
const NEEDED: [&str; 2] = [PROFILE, POSTS];
const WANTED: [&str; 3] = [PROFILE, POSTS, MORE];
/// How long read ids are trusted; a rejected id discards them earlier.
const TTL: Duration = Duration::from_secs(6 * 3600);
/// Scripts read at most per discovery.
const SCRIPTS_MAX: usize = 24;

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct Ops(BTreeMap<String, String>);

impl Ops {
    pub(crate) fn id(&self, name: &str) -> Result<&str, PluginError> {
        self.0
            .get(name)
            .map(String::as_str)
            .ok_or_else(|| kit::schema("the web client does not list a needed operation"))
    }

    pub(crate) fn has(&self, name: &str) -> bool {
        self.0.contains_key(name)
    }

    fn complete(&self) -> bool {
        NEEDED.iter().all(|n| self.has(n))
    }

    /// Adds what a script defines; what is already known is kept.
    fn read(&mut self, script: &str) {
        for name in WANTED {
            if !self.has(name)
                && let Some(id) = doc_id_in(script, name)
            {
                self.0.insert(name.to_owned(), id);
            }
        }
    }
}

/// The web client defines each operation's id as `__d("NAME_instagramRelayOperation",[],
/// function(…){e.exports="123"})`; the same name also appears as a dependency of other
/// modules, which carry no id right after it.
fn doc_id_in(script: &str, name: &str) -> Option<String> {
    let marker = format!("{name}_instagramRelayOperation\"");
    let mut from = 0;
    while let Some(found) = script[from..].find(&marker) {
        let start = from + found + marker.len();
        from = start;
        let window = &script[start..script.len().min(start + 220)];
        let Some(at) = window.find("exports=\"") else {
            continue;
        };
        let digits: String = window[at + "exports=\"".len()..]
            .chars()
            .take_while(char::is_ascii_digit)
            .collect();
        let closed = window[at + "exports=\"".len() + digits.len()..].starts_with('"');
        if closed && (6..=24).contains(&digits.len()) {
            return Some(digits);
        }
    }
    None
}

enum Slot {
    Empty,
    /// The ids in use were rejected: neither the data file's ids nor the read ones are trusted.
    Rejected,
    Known(Instant, Arc<Ops>),
}

/// The one set of ids of the process (tests keep one per thread, so they stay independent).
mod cache {
    use super::Slot;

    #[cfg(not(test))]
    static SLOT: std::sync::Mutex<Slot> = std::sync::Mutex::new(Slot::Empty);
    #[cfg(not(test))]
    pub(super) fn with<R>(f: impl FnOnce(&mut Slot) -> R) -> R {
        f(&mut SLOT.lock().unwrap())
    }

    #[cfg(test)]
    thread_local! {
        static SLOT: std::cell::RefCell<Slot> = const { std::cell::RefCell::new(Slot::Empty) };
    }
    #[cfg(test)]
    pub(super) fn with<R>(f: impl FnOnce(&mut Slot) -> R) -> R {
        SLOT.with(|s| f(&mut s.borrow_mut()))
    }
}

/// Reads the page's scripts in page order until every needed operation is found.
fn discover(net: &dyn Transport, scripts: &[String], mut ops: Ops) -> Result<Ops, PluginError> {
    for script in scripts.iter().take(SCRIPTS_MAX) {
        ops.read(&net::text(net, script, web::script_headers(), false)?);
        if ops.complete() {
            return Ok(ops);
        }
    }
    Err(kit::schema(
        "the web client's scripts do not define the needed operations",
    ))
}

/// The ids, read from the site when none are known or the known ones are old. `scripts` are
/// the addresses the page just read lists.
pub(crate) fn get(net: &dyn Transport, scripts: &[String]) -> Result<Arc<Ops>, PluginError> {
    let (known, seeded) = cache::with(|slot| match slot {
        Slot::Known(at, ops) if at.elapsed() < TTL => (Some(ops.clone()), true),
        Slot::Rejected => (None, false),
        _ => (None, true),
    });
    if let Some(known) = known {
        return Ok(known);
    }
    let start = Ops(if seeded {
        web::seed_doc_ids()
    } else {
        BTreeMap::new()
    });
    let fresh = Arc::new(if start.complete() {
        start
    } else {
        discover(net, scripts, start)?
    });
    cache::with(|slot| *slot = Slot::Known(Instant::now(), fresh.clone()));
    Ok(fresh)
}

/// Discards what is known: an operation was rejected and its id may have changed.
pub(crate) fn forget() {
    cache::with(|slot| *slot = Slot::Rejected);
}

#[cfg(test)]
mod tests {
    use super::*;

    const SCRIPT: &str = r#"__d("PolarisProfilePostsQuery.graphql",["PolarisProfilePostsQuery_instagramRelayOperation"],(function(a,b,c,d,e,f){e.exports={params:{id:b("PolarisProfilePostsQuery_instagramRelayOperation")}}}),null);
        __d("PolarisProfilePostsQuery_instagramRelayOperation",[],(function(a,b,c,d,e,f){e.exports="7000000000000001"}),null);
        __d("PolarisProfilePageContentQuery_instagramRelayOperation",[],(function(a,b,c,d,e,f){"use strict";e.exports="7000000000000002"}),null);
        __d("PolarisBadQuery_instagramRelayOperation",[],(function(){e.exports="12a"}),null);"#;

    #[test]
    fn an_id_is_the_value_the_operations_module_exports_not_the_name_in_a_dependency_list() {
        assert_eq!(
            doc_id_in(SCRIPT, POSTS).as_deref(),
            Some("7000000000000001")
        );
        assert_eq!(
            doc_id_in(SCRIPT, PROFILE).as_deref(),
            Some("7000000000000002")
        );
        assert_eq!(doc_id_in(SCRIPT, MORE), None);
        assert_eq!(doc_id_in(SCRIPT, "PolarisBadQuery"), None);
        let mut ops = Ops::default();
        ops.read(SCRIPT);
        assert!(ops.complete() && !ops.has(MORE));
        ops.read(&SCRIPT.replace("7000000000000001", "7000000000000009"));
        assert_eq!(
            ops.id(POSTS).unwrap(),
            "7000000000000001",
            "known ids are kept"
        );
        assert_eq!(
            Ops::default().id(POSTS).unwrap_err().class,
            trace_domain::errors::FailureClass::SchemaChanged
        );
    }
}
