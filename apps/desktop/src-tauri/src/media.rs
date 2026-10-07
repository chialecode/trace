//! Bounded media responses through daemon leases; no frontend paths or direct archive reads.
use base64::Engine;
use serde_json::json;
use trace_contracts::control::commands as cmd;
use trace_core::config::{MEDIA_CHUNK_MAX, Profile};
use trace_core::ipc::client::Client;

pub fn byte_range(header: Option<&str>, total: u64) -> Option<(u64, u64)> {
    if total == 0 {
        return None;
    }
    let max = MEDIA_CHUNK_MAX as u64;
    let (start, end) = match header {
        None => (0, total - 1),
        Some(h) => {
            let (a, b) = h.strip_prefix("bytes=")?.split_once('-')?;
            if b.contains(',') {
                return None;
            }
            if a.is_empty() {
                let suffix = b.parse::<u64>().ok()?;
                if suffix == 0 {
                    return None;
                }
                (total.saturating_sub(suffix), total - 1)
            } else {
                (
                    a.parse::<u64>().ok()?,
                    if b.is_empty() {
                        total - 1
                    } else {
                        b.parse::<u64>().ok()?.min(total - 1)
                    },
                )
            }
        }
    };
    if start >= total || end < start {
        return None;
    }
    Some((start, end.min(start.saturating_add(max - 1))))
}

pub fn register_media_protocol(
    builder: tauri::Builder<tauri::Wry>,
    profile: Profile,
) -> tauri::Builder<tauri::Wry> {
    builder.register_asynchronous_uri_scheme_protocol(
        "trace-media",
        move |_ctx, request, responder| {
            let profile = profile.clone();
            tauri::async_runtime::spawn(async move {
                let response = serve(&profile, request).await.unwrap_or_else(|_| {
                    tauri::http::Response::builder()
                        .status(404)
                        .body(b"Media unavailable or daemon disconnected".to_vec())
                        .unwrap()
                });
                responder.respond(response);
            });
        },
    )
}

async fn serve(
    profile: &Profile,
    request: tauri::http::Request<Vec<u8>>,
) -> Result<tauri::http::Response<Vec<u8>>, String> {
    let sha = request
        .uri()
        .path()
        .strip_prefix("/blob/")
        .ok_or("invalid media URI")?;
    trace_core::paths::sha256(sha).map_err(|_| "invalid blob")?;
    let client = Client::connect(&profile.pipe_name(), "media", Some(&profile.archive_id))
        .await
        .map_err(|_| "daemon unavailable")?;
    let lease = client
        .request(
            cmd::MEDIA_LEASE,
            json!({"blob_id":format!("sha256:{sha}")}),
            None,
            None,
        )
        .await
        .map_err(|_| "media unavailable")?;
    let id = lease["lease_id"].as_str().ok_or("lease")?;
    let result = async {
        let total = lease["size"].as_u64().ok_or("size")?;
        let range = request.headers().get("range").and_then(|v| v.to_str().ok());
        let Some((start, end)) = byte_range(range, total) else {
            return tauri::http::Response::builder()
                .status(416)
                .header("Content-Range", format!("bytes */{total}"))
                .body(Vec::new())
                .map_err(|_| "response");
        };
        if request.method() != "GET" && request.method() != "HEAD" {
            return Err("method");
        }
        let data = if request.method() == "HEAD" {
            Vec::new()
        } else {
            let chunk = client
                .request(
                    cmd::MEDIA_READ,
                    json!({"lease_id":id,"offset":start,"length":end-start+1}),
                    None,
                    None,
                )
                .await
                .map_err(|_| "read failed")?;
            base64::engine::general_purpose::STANDARD
                .decode(chunk["data_b64"].as_str().ok_or("chunk")?)
                .map_err(|_| "decode")?
        };
        let mime = lease["mime"]
            .as_str()
            .filter(|s| {
                s.starts_with("video/") || s.starts_with("audio/") || s.starts_with("image/")
            })
            .unwrap_or("application/octet-stream");
        tauri::http::Response::builder()
            .status(if start == 0 && end + 1 == total && range.is_none() {
                200
            } else {
                206
            })
            .header("Content-Type", mime)
            .header("X-Content-Type-Options", "nosniff")
            .header("Accept-Ranges", "bytes")
            .header("Content-Length", end - start + 1)
            .header("Content-Range", format!("bytes {start}-{end}/{total}"))
            .header("Access-Control-Allow-Origin", "http://tauri.localhost")
            .body(data)
            .map_err(|_| "response")
    }
    .await;
    let _ = client
        .request(cmd::MEDIA_RELEASE, json!({"lease_id":id}), None, None)
        .await;
    result.map_err(str::to_owned)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn f010_ranges_are_bounded_and_suffix_semantics_are_correct() {
        assert_eq!(
            byte_range(None, 10_000_000),
            Some((0, MEDIA_CHUNK_MAX as u64 - 1))
        );
        assert_eq!(
            byte_range(Some("bytes=0-"), 10_000_000),
            Some((0, MEDIA_CHUNK_MAX as u64 - 1))
        );
        assert_eq!(byte_range(Some("bytes=-100"), 1000), Some((900, 999)));
        assert_eq!(
            byte_range(Some("bytes=-1000000"), 2000000),
            Some((1000000, 1000000 + MEDIA_CHUNK_MAX as u64 - 1))
        );
        for bad in [
            "bytes=-0",
            "bytes=1000-",
            "bytes=8-3",
            "bytes=0-1,3-4",
            "bad",
        ] {
            assert_eq!(byte_range(Some(bad), 1000), None);
        }
        assert_eq!(byte_range(None, 0), None);
    }
}
