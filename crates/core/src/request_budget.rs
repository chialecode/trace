//! Optional one-shot site validation limits. Normal scheduling keeps its own policy.
use crate::error::CoreError;
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    sync::Mutex,
    time::{Duration, Instant},
};
use url::Url;

pub struct RequestBudget {
    maximum: usize,
    expires: Instant,
    interval: Duration,
    serial: tokio::sync::Mutex<Option<Instant>>,
    state: Mutex<State>,
    media_limit: Option<usize>,
    audit_path: Option<std::path::PathBuf>,
    /// Metadata bytes one run may receive.
    byte_limit: usize,
}
/// Responses of a one-shot validation run together; read-only queries stay small.
const VALIDATION_BYTES: usize = 8 * 1024 * 1024;
/// What one metadata request of an acquisition run may add to its byte limit (page-state
/// documents and feed pages of the newer sites run to a couple of megabytes).
const BYTES_PER_REQUEST: usize = 2 * 1024 * 1024;
#[derive(Default, serde::Serialize, serde::Deserialize)]
#[serde(default)]
struct State {
    stopped: bool,
    bytes: usize,
    attempts: Vec<Value>,
    media_bytes: usize,
    evidence_media: HashMap<String, usize>,
    media_reserved: usize,
    source_observations: Vec<Value>,
}
/// Endpoints whose path may appear in the validation record. Anything else (a page, a
/// redirect, an address that carries an identity) is recorded as `<page-or-redirect>`.
const AUDITED_PATHS: [&str; 41] = [
    "/aweme/v1/web/aweme/detail/",
    "/aweme/v1/web/aweme/post/",
    "/aweme/v1/web/user/profile/other/",
    "/aweme/v1/web/user/profile/self/",
    "/aweme/v1/web/query/user/",
    "/aweme/v1/web/mix/list/",
    "/aweme/v1/web/series/list/",
    "/aweme/v1/web/mix/aweme/",
    "/x/web-interface/card",
    "/x/web-interface/nav",
    "/x/space/wbi/arc/search",
    "/x/web-interface/view",
    "/x/web-interface/wbi/view",
    "/x/player/wbi/playurl",
    "/x/player/wbi/v2",
    "/x/polymer/web-dynamic/v1/feed/space",
    "/x/polymer/web-dynamic/v1/detail",
    "/x/polymer/web-dynamic/v1/opus/feed/space",
    "/x/polymer/web-dynamic/v1/opus/detail",
    "/x/space/article",
    "/x/article/view",
    "/x/article/list/web/articles",
    "/audio/music-service-c/web/song/info",
    "/audio/music-service-c/web/url",
    "/room/v1/Room/room_init",
    "/room/v1/Room/get_info",
    "/xlive/web-room/v2/index/getRoomPlayInfo",
    "/xlive/web-room/v1/dM/gethistory",
    "/webcast/room/web/enter/",
    "/webcast/room/reflow/info/",
    "/webcast/im/fetch/",
    "/ajax/profile/info",
    "/ajax/statuses/mymblog",
    "/ajax/statuses/show",
    "/ajax/statuses/longtext",
    "/ajax/config/get_config",
    "/api/config",
    "/youtubei/v1/browse",
    "/api/post/item_list/",
    "/api/graphql",
    "/graphql/query",
];

/// The path to record for a request: a listed endpoint, a GraphQL operation of X (its name,
/// never its id), or a placeholder. Never a query, a token or an identity.
fn audited_path(path: &str) -> String {
    if AUDITED_PATHS.contains(&path) {
        return path.to_owned();
    }
    let operation = path
        .strip_prefix("/i/api/graphql/")
        .and_then(|rest| rest.split_once('/'))
        .map(|(_, name)| name)
        .filter(|name| !name.is_empty() && name.bytes().all(|b| b.is_ascii_alphanumeric()));
    match operation {
        Some(name) => format!("/i/api/graphql/*/{name}"),
        None => "<page-or-redirect>".to_owned(),
    }
}

impl RequestBudget {
    pub fn new(maximum: usize, seconds: u64, interval_ms: u64) -> Result<Self, CoreError> {
        if !(1..=100).contains(&maximum)
            || !(1..=300).contains(&seconds)
            || !(1000..=60000).contains(&interval_ms)
        {
            return Err(CoreError::InvalidInput(
                "invalid site validation budget".into(),
            ));
        }
        Ok(Self {
            maximum,
            expires: Instant::now() + Duration::from_secs(seconds),
            interval: Duration::from_millis(interval_ms),
            serial: tokio::sync::Mutex::new(None),
            state: Mutex::new(State::default()),
            media_limit: None,
            audit_path: None,
            byte_limit: VALIDATION_BYTES,
        })
    }
    pub fn acquisition(
        limits: &crate::config::AcquisitionLimits,
        path: std::path::PathBuf,
    ) -> Result<Self, CoreError> {
        if !(1..=1000).contains(&limits.metadata_requests)
            || limits.media_bytes == 0
            || limits.media_bytes > 16 * 1024 * 1024 * 1024usize
            || !(2000..=60000).contains(&limits.interval_ms)
        {
            return Err(CoreError::InvalidInput("acquisition limits".into()));
        }
        let mut state: State = if path.exists() {
            serde_json::from_slice(&std::fs::read(&path)?)?
        } else {
            State::default()
        };
        // A crash can spend a reserved block before its precise counter flushes.
        state.media_bytes = state.media_bytes.max(state.media_reserved);
        Ok(Self {
            maximum: limits.metadata_requests,
            expires: Instant::now() + Duration::from_secs(3600),
            interval: Duration::from_millis(limits.interval_ms),
            serial: tokio::sync::Mutex::new(Some(Instant::now())),
            state: Mutex::new(state),
            media_limit: Some(limits.media_bytes),
            audit_path: Some(path),
            byte_limit: VALIDATION_BYTES.max(limits.metadata_requests * BYTES_PER_REQUEST),
        })
    }
    fn persist(&self, state: &State) -> Result<(), CoreError> {
        if let Some(path) = &self.audit_path {
            use std::io::Write;
            std::fs::create_dir_all(path.parent().unwrap())?;
            let mut file = tempfile::NamedTempFile::new_in(path.parent().unwrap())?;
            file.write_all(&serde_json::to_vec(state)?)?;
            file.as_file().sync_all()?;
            file.persist(path).map_err(|e| CoreError::Io(e.error))?;
        }
        Ok(())
    }
    pub async fn begin(&self, method: &reqwest::Method, url: &Url) -> Result<usize, CoreError> {
        self.begin_kind(method, url, true).await
    }
    pub async fn begin_kind(
        &self,
        method: &reqwest::Method,
        url: &Url,
        metadata: bool,
    ) -> Result<usize, CoreError> {
        self.begin_kind_with_evidence(method, url, metadata, None)
            .await
    }

    /// The same request record with an opaque execution identity from Core. Only a bounded
    /// identifier is retained so evidence can be attributed without persisting a URL or query.
    pub async fn begin_kind_with_evidence(
        &self,
        method: &reqwest::Method,
        url: &Url,
        metadata: bool,
        evidence_id: Option<&str>,
    ) -> Result<usize, CoreError> {
        let mut last = self.serial.lock().await;
        if let Some(at) = *last {
            tokio::time::sleep(self.interval.saturating_sub(at.elapsed())).await;
        }
        let mut state = self.state.lock().unwrap();
        if state.stopped
            || Instant::now() >= self.expires
            || state
                .attempts
                .iter()
                .filter(|v| v["metadata"] != false)
                .count()
                >= self.maximum
                && metadata
            || state.attempts.len() >= self.maximum + 256
            || !(matches!(*method, reqwest::Method::GET | reqwest::Method::HEAD)
                || (*method == reqwest::Method::POST && metadata))
        {
            state.stopped = true;
            return Err(CoreError::Cancelled);
        }
        let index = state.attempts.len();
        // Never log query, redirect token, Cookie or response body.
        let path = audited_path(url.path());
        let mut attempt = json!({
            "host": url.host_str(),
            "endpoint": path,
            "method": method.as_str(),
            "status": null,
            "metadata": metadata,
            "started_at": crate::jobs::now(),
        });
        if let Some(id) = safe_evidence_id(evidence_id) {
            attempt["evidence_id"] = Value::String(id);
        }
        state.attempts.push(attempt);
        self.persist(&state)?;
        *last = Some(Instant::now());
        Ok(index)
    }
    pub fn response(&self, index: usize, status: Option<u16>) {
        let mut state = self.state.lock().unwrap();
        state.attempts[index]["status"] = json!(status);
        if status.is_none_or(|v| !(200..400).contains(&v)) {
            state.stopped = true;
        }
        if self.persist(&state).is_err() {
            state.stopped = true;
        }
    }
    pub fn received(&self, bytes: usize) -> Result<(), CoreError> {
        let mut state = self.state.lock().unwrap();
        state.bytes = state.bytes.saturating_add(bytes);
        if state.bytes > self.byte_limit {
            state.stopped = true;
            return Err(CoreError::InvalidInput(
                "validation response byte budget".into(),
            ));
        }
        Ok(())
    }
    /// Diagnostic shape only: never retain bodies, identities or signed URLs.
    pub fn observe_response(&self, raw: &str, body: &[u8]) -> Result<(), CoreError> {
        let Ok(url) = Url::parse(raw) else {
            return Ok(());
        };
        if url.host_str() != Some("live.douyin.com") || url.path() != "/webcast/room/web/enter/" {
            return Ok(());
        }
        let parsed = serde_json::from_slice::<Value>(body);
        let value = parsed.as_ref().ok();
        let rooms = value.and_then(|v| v["data"]["data"].as_array());
        let states = rooms
            .into_iter()
            .flatten()
            .take(16)
            .map(|r| r["status"].as_i64())
            .collect::<Vec<_>>();
        let owner_present = rooms
            .into_iter()
            .flatten()
            .take(16)
            .map(|r| r["owner"].is_object())
            .collect::<Vec<_>>();
        let observation = json!({
            "endpoint": "/webcast/room/web/enter/",
            "body_bytes": body.len(),
            "json_valid": parsed.is_ok(),
            "status_code": value.and_then(|v|v["status_code"].as_i64()),
            "room_count": rooms.map(Vec::len),
            "room_states": states,
            "room_owner_present": owner_present,
        });
        let mut state = self.state.lock().unwrap();
        if state.source_observations.len() < 30 {
            state.source_observations.push(observation);
        }
        self.persist(&state)
    }
    pub fn stop(&self) {
        let mut s = self.state.lock().unwrap();
        s.stopped = true;
        let _ = self.persist(&s);
    }
    pub fn received_media(&self, bytes: usize) -> Result<(), CoreError> {
        self.received_media_for(bytes, None)
    }

    pub fn received_media_for(
        &self,
        bytes: usize,
        evidence_id: Option<&str>,
    ) -> Result<(), CoreError> {
        let Some(limit) = self.media_limit else {
            return Ok(());
        };
        let mut s = self.state.lock().unwrap();
        let next = s.media_bytes.saturating_add(bytes);
        if s.stopped || next > limit {
            s.stopped = true;
            self.persist(&s)?;
            return Err(CoreError::InvalidInput(
                "acquisition media budget exhausted".into(),
            ));
        }
        s.media_bytes = next;
        if let Some(id) = safe_evidence_id(evidence_id) {
            *s.evidence_media.entry(id).or_default() += bytes;
        }
        if next > s.media_reserved {
            s.media_reserved = next
                .div_ceil(1024 * 1024)
                .saturating_mul(1024 * 1024)
                .min(limit);
            self.persist(&s)?;
        }
        Ok(())
    }
    pub fn snapshot(&self) -> Value {
        let s = self.state.lock().unwrap();
        json!({
            "requests": s.attempts,
            "request_count": s.attempts.len(),
            "metadata_request_count": s.attempts.iter().filter(|v|v["metadata"]!=false).count(),
            "received_bytes": s.bytes,
            "media_received_bytes": s.media_bytes,
            "evidence_media_bytes": s.evidence_media,
            "media_reserved_bytes": s.media_reserved,
            "stopped": s.stopped,
            "source_observations": s.source_observations,
        })
    }
}

fn safe_evidence_id(value: Option<&str>) -> Option<String> {
    value
        .filter(|id| {
            (4..=160).contains(&id.len())
                && id
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'))
        })
        .map(str::to_owned)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_listed_endpoints_and_graphql_operation_names_reach_the_record() {
        for listed in [
            "/ajax/statuses/mymblog",
            "/youtubei/v1/browse",
            "/api/post/item_list/",
            "/graphql/query",
        ] {
            assert_eq!(audited_path(listed), listed);
        }
        assert_eq!(
            audited_path("/i/api/graphql/AbC-123_x/UserTweets"),
            "/i/api/graphql/*/UserTweets"
        );
        for hidden in [
            "/i/api/graphql/AbC/bad name",
            "/i/api/graphql/AbC/",
            "/@someone/video/7000000000000000001",
            "/user/profile/5a1b2c3d4e5f60718293a4b5",
            "/p/CxPhoto0001/",
            "/",
        ] {
            assert_eq!(audited_path(hidden), "<page-or-redirect>", "{hidden}");
        }
    }

    #[tokio::test]
    async fn acquisition_budget_survives_restart_and_stops_on_denial() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("budget.json");
        let limits = crate::config::AcquisitionLimits {
            metadata_requests: 2,
            media_bytes: 6,
            interval_ms: 2000,
        };
        let policy = RequestBudget::acquisition(&limits, path.clone()).unwrap();
        policy.received_media(4).unwrap();
        assert_eq!(policy.snapshot()["media_received_bytes"], 4);
        drop(policy);
        let policy = RequestBudget::acquisition(&limits, path.clone()).unwrap();
        assert!(policy.received_media(1).is_err());
        let separate = dir.path().join("denial.json");
        let policy = RequestBudget::acquisition(&limits, separate.clone()).unwrap();
        let url =
            Url::parse("https://www.douyin.com/aweme/v1/web/aweme/detail/?uifid=PRIVATE_CONTEXT")
                .unwrap();
        let request = policy.begin(&reqwest::Method::GET, &url).await.unwrap();
        policy.observe_response("https://live.douyin.com/webcast/room/web/enter/?uifid=PRIVATE_CONTEXT",&serde_json::to_vec(&json!({"status_code":0,"data":{"data":[{"status":4,"owner":{"id_str":"PRIVATE_CONTEXT"},"stream_url":{"flv_pull_url":{"HD":"https://cdn.douyinliving.com/PRIVATE_CONTEXT"}}}]}})).unwrap()).unwrap();
        assert_eq!(
            policy.snapshot()["source_observations"][0]["room_states"],
            json!([4])
        );
        policy.response(request, Some(403));
        drop(policy);
        let restored = RequestBudget::acquisition(&limits, separate.clone()).unwrap();
        assert!(restored.begin(&reqwest::Method::GET, &url).await.is_err());
        assert_eq!(restored.snapshot()["metadata_request_count"], 1);
        assert!(
            !std::fs::read_to_string(separate)
                .unwrap()
                .contains("PRIVATE_CONTEXT")
        );
    }
    #[tokio::test]
    async fn validation_counts_each_redirect_and_stops_without_secrets() {
        let policy = RequestBudget::new(2, 30, 1000).unwrap();
        let url = Url::parse(
            "https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=7&token=PRIVATE_TOKEN",
        )
        .unwrap();
        let first = policy.begin(&reqwest::Method::GET, &url).await.unwrap();
        policy.response(first, Some(302));
        let second = policy.begin(&reqwest::Method::GET, &url).await.unwrap();
        policy.response(second, Some(200));
        assert!(policy.begin(&reqwest::Method::GET, &url).await.is_err());
        assert_eq!(policy.snapshot()["request_count"], 2);
        assert!(!policy.snapshot().to_string().contains("PRIVATE_TOKEN"));
        for code in [None, Some(403), Some(429), Some(500)] {
            let policy = RequestBudget::new(20, 30, 1000).unwrap();
            let id = policy.begin(&reqwest::Method::GET, &url).await.unwrap();
            policy.response(id, code);
            assert!(policy.begin(&reqwest::Method::GET, &url).await.is_err());
            assert_eq!(policy.snapshot()["request_count"], 1);
        }
        // A read-only metadata query may be a POST; media never is.
        let policy = RequestBudget::new(2, 30, 1000).unwrap();
        assert!(
            policy
                .begin_kind(&reqwest::Method::POST, &url, false)
                .await
                .is_err()
        );
        let policy = RequestBudget::new(2, 30, 1000).unwrap();
        assert!(policy.begin(&reqwest::Method::POST, &url).await.is_ok());
        assert!(policy.received(8 * 1024 * 1024 + 1).is_err());
    }

    #[tokio::test]
    async fn request_evidence_keeps_only_an_opaque_execution_identity_and_per_context_bytes() {
        let dir = tempfile::tempdir().unwrap();
        let limits = crate::config::AcquisitionLimits {
            metadata_requests: 3,
            media_bytes: 1024,
            interval_ms: 2000,
        };
        let policy = RequestBudget::acquisition(&limits, dir.path().join("budget.json")).unwrap();
        let url = Url::parse("https://api.bilibili.com/x/web-interface/view?bvid=SECRET").unwrap();
        let index = policy
            .begin_kind_with_evidence(&reqwest::Method::GET, &url, true, Some("ctx-safe-01"))
            .await
            .unwrap();
        policy.response(index, Some(200));
        let bad = policy
            .begin_kind_with_evidence(
                &reqwest::Method::GET,
                &url,
                true,
                Some("token/SHOULD-NOT-PERSIST"),
            )
            .await
            .unwrap();
        policy.response(bad, Some(200));
        policy.received_media_for(64, Some("ctx-safe-01")).unwrap();
        let snapshot = policy.snapshot();
        assert_eq!(snapshot["requests"][0]["endpoint"], "/x/web-interface/view");
        assert_eq!(snapshot["requests"][0]["evidence_id"], "ctx-safe-01");
        assert!(snapshot["requests"][1].get("evidence_id").is_none());
        assert_eq!(snapshot["evidence_media_bytes"]["ctx-safe-01"], 64);
        assert!(!snapshot.to_string().contains("SECRET"));
        assert!(!snapshot.to_string().contains("SHOULD-NOT-PERSIST"));
    }
}
