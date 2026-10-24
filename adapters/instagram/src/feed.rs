//! A profile's post list: one page of the web client's profile feed per call.
use crate::account::{id_of, media_count};
use crate::api::{self, Session};
use crate::link::username_ok;
use crate::{ops, post};
use serde_json::{Value, json};
use trace_contracts::plugin::{EnumeratedPage, PluginError};
use trace_site_support::kit::{self, page, string};
use trace_site_support::runtime::Transport;

const PAGE_SIZE: u32 = 12;
const CONNECTION: &str = "xdt_api__v1__feed__user_timeline_graphql_connection";

/// Where the next page starts: the profile's name (the feed is asked for by name, which the
/// first page learns from the profile) and the site's own cursor.
#[derive(Debug, PartialEq, Eq)]
struct Position {
    username: String,
    after: String,
}

fn cursor_ok(after: &str) -> bool {
    (1..=512).contains(&after.len())
        && after.bytes().all(|b| {
            b.is_ascii_alphanumeric()
                || matches!(b, b'-' | b'_' | b'=' | b'+' | b'/' | b'|' | b'%' | b'.')
        })
}

fn decode_cursor(cursor: Option<&str>) -> Result<Option<Position>, PluginError> {
    let Some(raw) = cursor else { return Ok(None) };
    raw.strip_prefix("ig1:")
        .and_then(|rest| rest.split_once(':'))
        .filter(|(name, after)| username_ok(name) && cursor_ok(after))
        .map(|(name, after)| {
            Some(Position {
                username: name.to_owned(),
                after: after.to_owned(),
            })
        })
        .ok_or_else(|| kit::schema("invalid Instagram cursor"))
}

fn variables(username: &str, after: Option<&str>) -> Value {
    let mut v = json!({
        "data": {
            "count": PAGE_SIZE,
            "include_reel_media_seen_timestamp": true,
            "include_relationship_info": true,
            "latest_besties_reel_media": true,
            "latest_reel_media": true,
        },
        "username": username,
        "__relay_internal__pv__PolarisIsLoggedInrelayprovider": true,
    });
    if let Some(after) = after {
        v["after"] = json!(after);
        v["first"] = json!(PAGE_SIZE);
        v["before"] = Value::Null;
        v["last"] = Value::Null;
    }
    v
}

pub(crate) fn page(
    net: &dyn Transport,
    profile_id: &str,
    cursor: Option<&str>,
) -> Result<EnumeratedPage, PluginError> {
    let position = decode_cursor(cursor)?;
    let session = Session::read(&api::fetch_page(net, "/")?)?;
    // The first page learns the profile's name from its id, and how many posts it has.
    let (username, expected) = match &position {
        Some(p) => (p.username.clone(), None),
        None => {
            let user = api::profile(net, &session, profile_id)?;
            if id_of(&user).is_some_and(|id| id != profile_id) {
                return Err(kit::schema("account identity mismatch"));
            }
            let name = string(&user["username"])
                .filter(|n| username_ok(n))
                .ok_or_else(|| kit::schema("profile has no username"))?;
            (name, media_count(&user))
        }
    };
    let after = position.as_ref().map(|p| p.after.as_str());
    let name = if after.is_some() && ops::get(net, &session.scripts)?.has(ops::MORE) {
        ops::MORE
    } else {
        ops::POSTS
    };
    let answer = api::graphql(
        net,
        &session,
        "/graphql/query",
        name,
        &variables(&username, after),
    )?;
    let connection = page::find_all(&answer, &[CONNECTION])
        .into_iter()
        .map(|(_, v)| v)
        .find(|v| v["edges"].is_array())
        .ok_or_else(|| kit::schema("Instagram answer has no post connection"))?;
    let edges = connection["edges"]
        .as_array()
        .map_or(&[][..], Vec::as_slice);
    let items: Vec<_> = edges
        .iter()
        .filter_map(|e| post::entry(&e["node"]))
        .collect();
    if !edges.is_empty() && items.is_empty() {
        return Err(kit::schema("Instagram posts carry no code"));
    }
    let end = string(&connection["page_info"]["end_cursor"]).filter(|c| cursor_ok(c));
    // A cursor that does not move would repeat the page forever.
    if end.as_deref() == after && end.is_some() {
        return Err(kit::schema("Instagram cursor did not advance"));
    }
    let more =
        connection["page_info"]["has_next_page"] == true && end.is_some() && !items.is_empty();
    Ok(EnumeratedPage {
        next_cursor: more.then(|| format!("ig1:{username}:{}", end.clone().unwrap_or_default())),
        has_more: more,
        coverage: json!({
            "source": format!("graphql.{name}"),
            "observed_count": items.len(),
            "empty_confirmed": position.is_none() && edges.is_empty() && expected == Some(0),
            "history": "unknown",
        }),
        items,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_cursor_carries_the_name_and_the_sites_own_token() {
        assert_eq!(decode_cursor(None).unwrap(), None);
        let p = decode_cursor(Some("ig1:some.user:QVFB_x-1=="))
            .unwrap()
            .unwrap();
        assert_eq!(
            (p.username.as_str(), p.after.as_str()),
            ("some.user", "QVFB_x-1==")
        );
        for bad in [
            "",
            "ig2:a:b",
            "ig1:a",
            "ig1::b",
            "ig1:a:",
            "ig1:a b:c",
            "ig1:a:b c",
            "ig1:explore:abc",
        ] {
            assert!(decode_cursor(Some(bad)).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn the_variables_ask_for_the_next_page_only_with_a_cursor() {
        assert!(variables("a", None).get("after").is_none());
        let next = variables("a", Some("x"));
        assert_eq!(
            (next["after"].as_str(), next["first"].as_u64()),
            (Some("x"), Some(12))
        );
        assert_eq!(next["username"], "a");
    }
}
