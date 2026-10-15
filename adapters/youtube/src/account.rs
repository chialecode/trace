//! YouTube channels: identity from any link form, and the (absent) login probe.
use crate::api::{self, ORIGIN};
use crate::link::{Channel, parse_channel};
use serde_json::json;
use trace_contracts::plugin::{PluginError, ProbeAuthResult, ResolvedAccount};
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::{self, string};
use trace_site_support::runtime::Transport;

/// The path of the channel's own page, percent-encoded.
fn page_path(channel: &Channel) -> Option<String> {
    let mut url = url::Url::parse(ORIGIN).ok()?;
    {
        let mut segments = url.path_segments_mut().ok()?;
        match channel {
            Channel::Id(id) => segments.extend(["channel", id.as_str()]),
            Channel::Handle(handle) => segments.push(&format!("@{handle}")),
            Channel::Legacy(kind, name) => segments.extend([*kind, name.as_str()]),
        };
    }
    url.set_query(Some("hl=en"));
    Some(format!("{}?{}", url.path(), url.query()?))
}

pub(crate) fn resolve(net: &dyn Transport, input: &str) -> Result<ResolvedAccount, PluginError> {
    let channel = parse_channel(input).ok_or_else(|| {
        error(
            FailureClass::InvalidPayload,
            "not a YouTube channel link, handle or id",
        )
    })?;
    let path = page_path(&channel)
        .ok_or_else(|| error(FailureClass::InvalidPayload, "invalid YouTube channel name"))?;
    let data = api::initial_data(&api::html(net, &path)?)?;
    let id = api::channel_id(&data).ok_or_else(|| kit::schema("channel page has no channel id"))?;
    if let Channel::Id(wanted) = &channel
        && *wanted != id
    {
        return Err(kit::schema("channel identity mismatch"));
    }
    let meta = &data["metadata"]["channelMetadataRenderer"];
    Ok(ResolvedAccount {
        display_name: string(&meta["title"]),
        profile_url: Some(format!("{ORIGIN}/channel/{id}")),
        evidence: json!({
            "source": "channel.page",
            "vanity": string(&meta["vanityChannelUrl"]),
            "profile": trace_contracts::content::safe_metadata(&json!({
                "title": meta["title"],
                "description": meta["description"],
                "is_family_safe": meta["isFamilySafe"],
            })),
        }),
        native_account_key: id,
    })
}

/// Public videos need no login and this adapter sends no Cookie, so there is no session to
/// prove; the answer says so instead of claiming either state.
pub(crate) fn probe() -> ProbeAuthResult {
    ProbeAuthResult {
        state: "unknown".into(),
        capability: json!({"login_proven": false, "login_used": false}),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn channel_page_paths_are_encoded_per_segment() {
        assert_eq!(
            page_path(&Channel::Handle("中 文".into())).as_deref(),
            Some("/@%E4%B8%AD%20%E6%96%87?hl=en")
        );
        assert_eq!(
            page_path(&Channel::Legacy("c", "Name".into())).as_deref(),
            Some("/c/Name?hl=en")
        );
    }
}
