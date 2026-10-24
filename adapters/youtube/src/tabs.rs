//! A channel's lists: the first page comes with the channel tab's own page, the rest with
//! continuation tokens of the browse call.
use crate::api::{self, find_all};
use crate::entry::{self, Item};
use crate::link::is_channel_id;
use crate::post;
use serde_json::{Value, json};
use std::collections::BTreeMap;
use trace_contracts::plugin::{EnumeratedEntry, PluginError};
use trace_site_support::kit::{self, schema, string};
use trace_site_support::runtime::Transport;

/// What one page of a list held.
#[derive(Default)]
pub(crate) struct Listing {
    pub items: Vec<EnumeratedEntry>,
    pub token: Option<String>,
    /// Items left out because they cannot be archived as they are (live, members only),
    /// by the reason.
    pub skipped: BTreeMap<&'static str, usize>,
    /// The channel has no such tab: it has nothing of this kind, by the site's own tab list.
    pub tab_absent: bool,
    /// The tab was read and its list container was there, even if it held nothing.
    pub container_seen: bool,
}

/// The slug a lane's tab has in links.
fn slug(lane: &str) -> &'static str {
    match lane {
        entry::VIDEO => "videos",
        entry::SHORT => "shorts",
        _ => "posts",
    }
}

fn tab_url(tab: &Value) -> &str {
    tab["endpoint"]["commandMetadata"]["webCommandMetadata"]["url"]
        .as_str()
        .unwrap_or_default()
}

fn is_lane_tab(tab: &Value, lane: &str) -> bool {
    let url = tab_url(tab).trim_end_matches('/');
    let wanted = slug(lane);
    url.ends_with(&format!("/{wanted}")) || (lane == "post" && url.ends_with("/community"))
}

fn tabs(data: &Value) -> Vec<&Value> {
    data["contents"]["twoColumnBrowseResultsRenderer"]["tabs"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|t| &t["tabRenderer"])
        .filter(|t| t.is_object())
        .collect()
}

/// The items and continuation of any part of a browse answer or a tab's content.
fn read(root: &Value, lane: &str) -> Listing {
    let mut keys = entry::keys(lane).to_vec();
    keys.push("continuationItemRenderer");
    let mut out = Listing::default();
    for (key, node) in find_all(root, &keys) {
        if key == "continuationItemRenderer" {
            out.token = string(&node["continuationEndpoint"]["continuationCommand"]["token"])
                .or_else(|| {
                    string(&node["button"]["buttonRenderer"]["command"]["continuationCommand"]["token"])
                })
                .or(out.token);
        } else if lane == entry::VIDEO || lane == entry::SHORT {
            match entry::item(key, node) {
                Item::Entry(e) => out.items.push(*e),
                Item::Skipped(why) => *out.skipped.entry(why).or_default() += 1,
            }
        } else if let Some(e) = post::entry(node) {
            out.items.push(e);
        } else {
            *out.skipped.entry("unreadable_post").or_default() += 1;
        }
    }
    out
}

fn container_present(content: &Value) -> bool {
    content["richGridRenderer"].is_object() || content["sectionListRenderer"].is_object()
}

/// The first page of `lane` of channel `id`, and the client version later pages need.
pub(crate) fn first(
    net: &dyn Transport,
    id: &str,
    lane: &str,
) -> Result<(Listing, String), PluginError> {
    if !is_channel_id(id) {
        return Err(kit::schema("invalid channel id"));
    }
    let page = api::html(net, &format!("/channel/{id}/{}?hl=en", slug(lane)))?;
    let data = api::initial_data(&page)?;
    let version = api::client_version(&page).ok_or_else(|| schema("page has no client version"))?;
    if api::channel_id(&data).as_deref() != Some(id) {
        return Err(schema("channel identity mismatch"));
    }
    let all = tabs(&data);
    let Some(tab) = all.iter().find(|t| is_lane_tab(t, lane)) else {
        // Only a real channel page (with tabs) can say the channel lacks one.
        if all.is_empty() {
            return Err(schema("channel page has no tabs"));
        }
        return Ok((
            Listing {
                tab_absent: true,
                ..Listing::default()
            },
            version,
        ));
    };
    let content = if tab["selected"] == true {
        tab["content"].clone()
    } else {
        // The site showed another tab (a channel's home): ask for the tab by its own address.
        let browse = &tab["endpoint"]["browseEndpoint"];
        let (Some(browse_id), Some(params)) =
            (string(&browse["browseId"]), string(&browse["params"]))
        else {
            return Err(schema("tab has no browse address"));
        };
        let answer = api::browse(
            net,
            &version,
            json!({"browseId": browse_id, "params": params}),
        )?;
        let selected = tabs(&answer)
            .into_iter()
            .find(|t| t["selected"] == true)
            .map(|t| t["content"].clone());
        selected.ok_or_else(|| schema("browse answer has no selected tab"))?
    };
    let mut listing = read(&content, lane);
    listing.container_seen = container_present(&content);
    Ok((listing, version))
}

/// A later page.
pub(crate) fn next(
    net: &dyn Transport,
    version: &str,
    token: &str,
    lane: &str,
) -> Result<Listing, PluginError> {
    let answer = api::browse(net, version, json!({"continuation": token}))?;
    let actions = [
        &answer["onResponseReceivedActions"],
        &answer["onResponseReceivedEndpoints"],
    ];
    if actions.iter().all(|a| a.is_null()) {
        return Err(schema("continuation answer has no items"));
    }
    let mut listing = Listing::default();
    for part in actions {
        let found = read(part, lane);
        listing.items.extend(found.items);
        for (why, n) in found.skipped {
            *listing.skipped.entry(why).or_default() += n;
        }
        listing.token = found.token.or(listing.token);
    }
    listing.container_seen = true;
    Ok(listing)
}
