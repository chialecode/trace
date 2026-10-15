//! The tool as the second way to a video a site refused to hand over. TikTok's video address
//! answers only the session that produced it; when Core's own request for it is refused even
//! with that session's Cookies, the tool fetches the video's page itself and downloads the file
//! into a directory of its own. It runs as the format query does (fixed arguments, an empty
//! environment, no credentials, bounded time and size), and Core takes the file from there:
//! it is hashed, measured and committed like any other, and says which tool it came from.
use super::MediaTools;
use super::run::{Fetched, Sandbox};
use crate::core::Core;
use crate::error::CoreError;
use trace_domain::sites;

/// Requests a download is counted as against a bounded validation budget: the page, the player
/// data and the file, with one to spare.
const REQUESTS_PER_FETCH: usize = 4;
/// The most a download may be when the adapter promised a size: twice the promise, at least
/// this much (the rendition the tool picks need not be the promised one) and never more than the
/// ceiling.
const CAP_FLOOR: u64 = 64 << 20;
const CAP_CEILING: u64 = 1 << 30;

/// The page of a video, which the tool is asked for. Only a site that needs the tool has one.
fn page(platform: &str, key: &str) -> Option<String> {
    let numeric = !key.is_empty() && key.len() <= 24 && key.bytes().all(|b| b.is_ascii_digit());
    (platform == "tiktok" && numeric).then(|| format!("https://www.tiktok.com/@_/video/{key}"))
}

/// A failure that says the site would not hand the file over, as opposed to one that says the
/// network or the job failed.
pub(crate) fn refused(error: &CoreError) -> bool {
    match error {
        CoreError::SourceUnavailable(reason) => reason.starts_with("media source unavailable HTTP"),
        CoreError::AuthRequired(reason) => reason == "HTTP 401",
        _ => false,
    }
}

/// Whether the tool may stand in for the download of a candidate that failed with `error`: a
/// video of a site whose own addresses answer only their session.
pub(crate) fn replaceable(platform: &str, role: &str, error: &CoreError) -> bool {
    sites::site(platform).is_some_and(|site| site.session_media)
        && role == "video"
        && refused(error)
}

/// The most bytes a replacement may be, given what the adapter promised.
fn cap(promised: Option<i64>) -> u64 {
    promised
        .and_then(|n| u64::try_from(n).ok())
        .map_or(CAP_FLOOR.saturating_mul(4), |n| n.saturating_mul(2))
        .clamp(CAP_FLOOR, CAP_CEILING)
}

/// Downloads the video of `key` with the tool. `None` when the tool has nothing to add: it is
/// not for this site, or it is not available (the original refusal then stands).
pub(crate) async fn fetch(
    core: &Core,
    platform: &str,
    key: &str,
    promised: Option<i64>,
    evidence_id: Option<&str>,
) -> Result<Option<Fetched>, CoreError> {
    let Some(url) = page(platform, key) else {
        return Ok(None);
    };
    let tools: &MediaTools = &core.media_tools;
    if !tools.ready().await {
        return Ok(None);
    }
    let route = core.network.route_for(platform);
    let proxy = match &route {
        crate::network::Route::Proxy(proxy) => Some(proxy),
        crate::network::Route::Direct => None,
    };
    core.http
        .charge_external(&url, REQUESTS_PER_FETCH, false, evidence_id)
        .await?;
    let work = core.profile.data_root.join("cache").join("media-tools");
    let sandbox = Sandbox { work: &work, proxy };
    tools
        .fetch_video(&sandbox, &url, cap(promised), &core.cancel)
        .await
        .map(Some)
}

#[cfg(test)]
mod tests {
    use super::*;
    use trace_domain::errors::FailureClass;

    #[test]
    fn only_a_refusal_of_a_session_sites_video_hands_the_download_to_the_tool() {
        let gone = |code: u16| {
            CoreError::SourceUnavailable(format!(
                "media source unavailable HTTP {code}; re-resolve"
            ))
        };
        for code in [403, 404, 410] {
            assert!(replaceable("tiktok", "video", &gone(code)), "{code}");
        }
        assert!(replaceable(
            "tiktok",
            "video",
            &CoreError::AuthRequired("HTTP 401".into())
        ));
        for error in [
            CoreError::RateLimited("HTTP 429".into()),
            CoreError::Network("HTTP 503".into()),
            CoreError::Network("download stalled (no progress for 60s)".into()),
            CoreError::SourceUnavailable("the video is not available".into()),
            CoreError::Adapter(FailureClass::PermissionDenied),
        ] {
            assert!(!replaceable("tiktok", "video", &error), "{error:?}");
        }
        assert!(
            !replaceable("tiktok", "cover", &gone(403)),
            "a cover is not a video"
        );
        for site in [
            "weibo",
            "x",
            "youtube",
            "bilibili",
            "douyin",
            "instagram",
            "xiaohongshu",
        ] {
            assert!(!replaceable(site, "video", &gone(403)), "{site}");
        }
    }

    #[test]
    fn the_tool_is_asked_for_a_numeric_tiktok_video_page_only() {
        assert_eq!(
            page("tiktok", "7000000000000000001").as_deref(),
            Some("https://www.tiktok.com/@_/video/7000000000000000001")
        );
        for (platform, key) in [
            ("tiktok", ""),
            ("tiktok", "7a"),
            ("tiktok", "70000000000000000000000001"),
            ("tiktok", "1/../2"),
            ("youtube", "7000000000000000001"),
        ] {
            assert_eq!(page(platform, key), None, "{platform} {key}");
        }
    }

    #[test]
    fn a_download_is_bounded_by_the_promised_size_within_fixed_limits() {
        assert_eq!(cap(None), 256 << 20);
        assert_eq!(cap(Some(1 << 20)), CAP_FLOOR);
        assert_eq!(cap(Some(100 << 20)), 200 << 20);
        assert_eq!(cap(Some(i64::MAX)), CAP_CEILING);
        assert_eq!(cap(Some(-5)), 256 << 20);
    }
}
