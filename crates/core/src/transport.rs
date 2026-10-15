//! 受限 HTTP 传输：元数据代理与媒体下载（m1a-runtime-contracts §3）。
//!
//! 校验顺序：scheme → 主机白名单 → 每跳重定向 → 解析地址非私网；
//! Cookie 只随授权域请求发送，跨域重定向显式剥离敏感头并受
//! 白名单拦截；媒体下载带进度检查点与取消，不设大文件总时限。

use crate::config::Profile;
use crate::error::CoreError;
use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use std::net::{IpAddr, ToSocketAddrs};
use std::time::Duration;
use tokio_util::sync::CancellationToken;
use url::Url;

/// 平台网络范围（保守内置清单；profile.extra_media_hosts 可显式扩展）。
#[derive(Clone)]
pub struct HostRules {
    /// 元数据 API 域（允许携带 Cookie）。
    pub api_hosts: Vec<String>,
    /// 媒体/资源域（禁止 Cookie）。
    pub media_hosts: Vec<String>,
    /// 可选公共头白名单。
    pub allowed_headers: Vec<String>,
    /// 显式测试宿主（profile 文件配置）：允许 http/回环，仅离线 e2e。
    pub test_allowed: Vec<String>,
}

/// 媒体分块接收回调：(累计偏移, 本块字节)。
pub type ChunkWriter<'a> = (
    &'a mut crate::objects::StagedWriter,
    &'a crate::db::DbHandle,
    &'a trace_domain::ids::OperationId,
);

pub fn host_rules(profile: &Profile) -> HostRules {
    let mut media = vec![
        "bilivideo.com".to_owned(),
        "hdslb.com".to_owned(),
        "akamaized.net".to_owned(),
        "douyinvod.com".into(),
        "douyinpic.com".into(),
        "byteimg.com".into(),
        "pstatp.com".into(),
        "amemv.com".into(),
        "douyincdn.com".into(),
        "bilivideo.cn".into(),
        "bytefcdnrd.com".into(),
        "douyinliving.com".into(),
        "v.douyin.com".into(),
        "b23.tv".into(),
        "live.bilibili.com".into(),
        "www.iesdouyin.com".into(),
    ];
    media.extend(profile.extra_media_hosts.iter().cloned());
    #[cfg(debug_assertions)]
    let test_allowed: Vec<String> = profile.test_allowed_hosts.to_vec();
    #[cfg(not(debug_assertions))]
    let test_allowed: Vec<String> = Vec::new();
    HostRules {
        api_hosts: vec![
            "api.bilibili.com".into(),
            "www.bilibili.com".into(),
            "api.live.bilibili.com".into(),
            "www.douyin.com".into(),
            "live.douyin.com".into(),
            "webcast.amemv.com".into(),
        ]
        .into_iter()
        .collect(),
        media_hosts: media,
        allowed_headers: vec!["referer".into(), "user-agent".into()],
        test_allowed,
    }
}

pub fn host_matches(host: &str, rules: &[String]) -> bool {
    let host = host.trim_end_matches('.');
    let host = host.to_ascii_lowercase();
    rules
        .iter()
        .any(|r| host == *r || host.ends_with(&format!(".{r}")))
}

pub fn platform_rules(rules: &HostRules, platform: &str) -> HostRules {
    let mut scoped = rules.clone();
    scoped.api_hosts.retain(|h| match platform {
        "bilibili" => h.ends_with("bilibili.com"),
        "douyin" => h.ends_with("douyin.com") || h == "webcast.amemv.com",
        _ => false,
    });
    scoped.media_hosts.retain(|h| match platform {
        "bilibili" => !matches!(
            h.as_str(),
            "douyinvod.com"
                | "douyinpic.com"
                | "byteimg.com"
                | "pstatp.com"
                | "amemv.com"
                | "douyincdn.com"
                | "douyinliving.com"
                | "v.douyin.com"
                | "www.iesdouyin.com"
        ),
        "douyin" => !matches!(
            h.as_str(),
            "bilivideo.com"
                | "bilivideo.cn"
                | "hdslb.com"
                | "akamaized.net"
                | "bytefcdnrd.com"
                | "b23.tv"
                | "live.bilibili.com"
        ),
        _ => false,
    });
    scoped
}

/// 拒绝 localhost/私网/链路本地/组播地址（C22）。
pub fn assert_public_host(host: &str, port: u16) -> Result<(), CoreError> {
    let addrs: Vec<IpAddr> = (host, port)
        .to_socket_addrs()
        .map_err(|e| CoreError::Network(format!("dns resolve failed: {e}")))?
        .map(|a| a.ip())
        .collect();
    if addrs.is_empty() {
        return Err(CoreError::Network("dns resolved no addresses".into()));
    }
    for ip in addrs {
        if is_forbidden_ip(&ip) {
            return Err(CoreError::Network(format!("forbidden address class {ip}")));
        }
    }
    Ok(())
}

pub fn is_forbidden_ip(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => {
            v4.is_loopback()
                || v4.is_private()
                || v4.is_link_local()
                || v4.is_broadcast()
                || v4.is_multicast()
                || v4.is_unspecified()
                || v4.is_documentation()
        }
        IpAddr::V6(v6) => {
            v6.to_ipv4_mapped().is_some_and(|ip| is_forbidden_ip(&IpAddr::V4(ip)))
                || v6.is_loopback()
                || v6.is_unspecified()
                || v6.is_multicast()
                || (v6.segments()[0] & 0xfe00) == 0xfc00 // unique local
                || (v6.segments()[0] & 0xffc0) == 0xfe80 // link local
        }
    }
}

/// URL 校验：scheme → 白名单 → 地址类别。`test_allowed` 为 profile 文件
/// 显式配置的测试宿主（不随产品配置进入运行路径）：允许 http 与回环地址，
/// 供离线 e2e 本地服务器使用。
fn validate_url(raw: &str, rules: &HostRules) -> Result<Url, CoreError> {
    let url = Url::parse(raw).map_err(|_| CoreError::InvalidInput("bad url".into()))?;
    let host = url
        .host_str()
        .ok_or_else(|| CoreError::InvalidInput("no host".into()))?;
    if host_matches(host, &rules.test_allowed) {
        if url.scheme() != "https" && url.scheme() != "http" {
            return Err(CoreError::Network(format!(
                "scheme {} rejected",
                url.scheme()
            )));
        }
        return Ok(url);
    }
    if url.scheme() != "https" {
        return Err(CoreError::Network(format!(
            "scheme {} rejected",
            url.scheme()
        )));
    }
    let port = url.port_or_known_default().unwrap_or(443);
    if !host_matches(host, &rules.api_hosts) && !host_matches(host, &rules.media_hosts) {
        return Err(CoreError::Network(format!("host {host} not allowed")));
    }
    assert_public_host(host, port)?;
    Ok(url)
}

pub struct ConstrainedClient {
    budget: Option<std::sync::Arc<crate::request_budget::RequestBudget>>,
    #[cfg(test)]
    routes: std::sync::RwLock<std::collections::HashMap<String, Url>>,
}

#[derive(Debug, Clone, Default)]
pub struct FetchOutcome {
    pub status: u16,
    pub final_host: String,
    pub final_url: String,
    pub body: Vec<u8>,
    pub etag: Option<String>,
    pub content_length: Option<i64>,
}

impl ConstrainedClient {
    pub fn build() -> Result<Self, CoreError> {
        Ok(Self {
            budget: None,
            #[cfg(test)]
            routes: Default::default(),
        })
    }

    pub fn with_budget(
        mut self,
        budget: std::sync::Arc<crate::request_budget::RequestBudget>,
    ) -> Self {
        self.budget = Some(budget);
        self
    }
    pub fn budget_snapshot(&self) -> Option<serde_json::Value> {
        self.budget.as_ref().map(|b| b.snapshot())
    }
    pub fn received_media(&self, bytes: usize) -> Result<(), CoreError> {
        if let Some(b) = &self.budget {
            b.received_media(bytes)?;
        }
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn route_fixture(&self, host: &str, local: &str) {
        let url = Url::parse(local).unwrap();
        assert_eq!(url.host_str(), Some("127.0.0.1"));
        self.routes.write().unwrap().insert(host.to_owned(), url);
    }

    /// Live streams, nested playlists, keys and MAP all enter through this same
    /// redirect/DNS-pinned boundary. No caller can inject Cookie or Authorization.
    pub async fn open_media(
        &self,
        rules: &HostRules,
        raw: &str,
        public: &serde_json::Value,
        range: Option<(u64, u64)>,
    ) -> Result<reqwest::Response, CoreError> {
        let url = validate_url(raw, rules)?;
        let mut headers = self.build_headers(rules, Some(public), None, &url)?;
        if let Some((start, length)) = range {
            let end = start
                .checked_add(length)
                .filter(|_| length > 0)
                .and_then(|v| v.checked_sub(1))
                .ok_or_else(|| CoreError::InvalidInput("invalid byte range".into()))?;
            headers.insert(
                reqwest::header::RANGE,
                HeaderValue::from_str(&format!("bytes={start}-{end}"))
                    .map_err(|_| CoreError::InvalidInput("range".into()))?,
            );
        }
        let response = self
            .request(rules, reqwest::Method::GET, url, headers, false)
            .await?;
        if let Some((start, length)) = range {
            let expected = format!("bytes {start}-{}/", start + length - 1);
            let content_range = response
                .headers()
                .get(reqwest::header::CONTENT_RANGE)
                .and_then(|v| v.to_str().ok())
                .unwrap_or("");
            if response.status().as_u16() != 206
                || !content_range.starts_with(&expected)
                || response.content_length().is_some_and(|n| n != length)
            {
                return Err(CoreError::Network("invalid ranged live resource".into()));
            }
        } else if response.status().as_u16() != 200 {
            return Err(status_error(response.status().as_u16()));
        }
        Ok(response)
    }

    pub async fn live_resource(
        &self,
        rules: &HostRules,
        raw: &str,
        public: &serde_json::Value,
        range: Option<(u64, u64)>,
        limit: usize,
    ) -> Result<(Vec<u8>, Url), CoreError> {
        let mut response = self.open_media(rules, raw, public, range).await?;
        let final_url = response.url().clone();
        let length = response.content_length();
        if length.is_some_and(|n| n > limit as u64) {
            return Err(CoreError::InvalidInput("live resource byte budget".into()));
        }
        let mut out = Vec::new();
        while let Some(chunk) = response
            .chunk()
            .await
            .map_err(|_| CoreError::Network("live resource stream interrupted".into()))?
        {
            if out.len() + chunk.len() > limit {
                return Err(CoreError::InvalidInput("live resource byte budget".into()));
            }
            self.received_media(chunk.len())?;
            out.extend_from_slice(&chunk);
        }
        if length.is_some_and(|n| n != out.len() as u64)
            || range.is_some_and(|(_, n)| n != out.len() as u64)
        {
            return Err(CoreError::Network("live resource length mismatch".into()));
        }
        Ok((out, final_url))
    }

    async fn request(
        &self,
        rules: &HostRules,
        method: reqwest::Method,
        url: Url,
        headers: HeaderMap,
        metadata: bool,
    ) -> Result<reqwest::Response, CoreError> {
        let mut current = url;
        let mut headers = headers;
        let mut method = method;
        for hop in 0..=5 {
            #[cfg(test)]
            if let Some(mut local) = self
                .routes
                .read()
                .unwrap()
                .get(current.host_str().unwrap_or(""))
                .cloned()
            {
                local.set_path(current.path());
                local.set_query(current.query());
                current = local;
                headers.remove(reqwest::header::COOKIE);
                headers.remove(reqwest::header::AUTHORIZATION);
            }
            let current_rules = rules.clone();
            let raw = current.to_string();
            tokio::task::spawn_blocking(move || validate_url(&raw, &current_rules))
                .await
                .map_err(|_| CoreError::Network("validation failed".into()))??;
            let host = current
                .host_str()
                .ok_or_else(|| CoreError::InvalidInput("host".into()))?;
            let addresses: Vec<_> =
                tokio::net::lookup_host((host, current.port_or_known_default().unwrap_or(443)))
                    .await
                    .map_err(|_| CoreError::Network("DNS failed".into()))?
                    .collect();
            if addresses.is_empty()
                || (!host_matches(host, &rules.test_allowed)
                    && addresses.iter().any(|a| is_forbidden_ip(&a.ip())))
            {
                return Err(CoreError::Network("forbidden resolved address".into()));
            }
            // Pin the same checked DNS answers used to connect, preventing a second unchecked resolution.
            let mut builder = reqwest::Client::builder()
                .no_proxy()
                .redirect(reqwest::redirect::Policy::none())
                .connect_timeout(Duration::from_secs(10))
                .read_timeout(Duration::from_secs(60))
                .resolve_to_addrs(host, &addresses);
            if metadata {
                builder = builder.timeout(Duration::from_secs(30));
            }
            let client = builder
                .build()
                .map_err(|_| CoreError::Network("client build failed".into()))?;
            let attempt = if let Some(budget) = &self.budget {
                Some(budget.begin_kind(&method, &current, metadata).await?)
            } else {
                None
            };
            let response = client
                .request(method.clone(), current.clone())
                .headers(headers.clone())
                .send()
                .await;
            if let (Some(budget), Some(index)) = (&self.budget, attempt) {
                budget.response(index, response.as_ref().ok().map(|r| r.status().as_u16()));
            }
            let response = response.map_err(|e| CoreError::Network(sanitize_reqwest(&e)))?;
            if !response.status().is_redirection() {
                return Ok(response);
            }
            if hop == 5 {
                return Err(CoreError::Network("too many redirects".into()));
            }
            let location = response
                .headers()
                .get(reqwest::header::LOCATION)
                .and_then(|v| v.to_str().ok())
                .ok_or_else(|| CoreError::Network("redirect location missing".into()))?;
            let mut next = current
                .join(location)
                .map_err(|_| CoreError::Network("invalid redirect".into()))?;
            if next.origin() != current.origin() {
                headers.remove(reqwest::header::COOKIE);
                headers.remove(reqwest::header::AUTHORIZATION);
                crate::site_context::strip_headers(&mut headers);
                next = Url::parse(&crate::site_context::public_url(&next))
                    .map_err(|_| CoreError::Network("redirect query".into()))?;
            }
            if response.status().as_u16() == 303 && method != reqwest::Method::HEAD {
                method = reqwest::Method::GET;
            }
            current = next;
        }
        Err(CoreError::Network("redirect limit".into()))
    }

    fn build_headers(
        &self,
        rules: &HostRules,
        extra_public: Option<&serde_json::Value>,
        cookie: Option<&str>,
        url: &Url,
    ) -> Result<HeaderMap, CoreError> {
        let mut headers = HeaderMap::new();
        if let Some(extra) = extra_public.filter(|v| v.is_object())
            && let Some(obj) = extra.as_object()
        {
            for (k, v) in obj {
                let name = k.to_ascii_lowercase();
                if !rules.allowed_headers.contains(&name) {
                    return Err(CoreError::InvalidInput(format!(
                        "header {name} not allowed"
                    )));
                }
                let val = v
                    .as_str()
                    .ok_or_else(|| CoreError::InvalidInput("header value".into()))?;
                headers.insert(
                    HeaderName::from_bytes(name.as_bytes())
                        .map_err(|_| CoreError::InvalidInput("bad header name".into()))?,
                    HeaderValue::from_str(val)
                        .map_err(|_| CoreError::InvalidInput("header value".into()))?,
                );
            }
        }
        if let Some(c) = cookie {
            // Cookie 只允许发送到 API 域（C22/C13）。
            let host = url.host_str().unwrap_or_default();
            if !host_matches(host, &rules.api_hosts) && !host_matches(host, &rules.test_allowed) {
                return Err(CoreError::Network("credential host not allowed".into()));
            }
            headers.insert(
                reqwest::header::COOKIE,
                HeaderValue::from_str(c)
                    .map_err(|_| CoreError::InvalidInput("cookie header".into()))?,
            );
        }
        Ok(headers)
    }

    /// 元数据代理取数（供插件 proxy.fetch）：有界响应体。
    pub async fn fetch_metadata(
        &self,
        rules: &HostRules,
        method: &str,
        url: &str,
        extra_public: Option<&serde_json::Value>,
        cookie: Option<&str>,
        max_bytes: u32,
    ) -> Result<FetchOutcome, CoreError> {
        if max_bytes == 0 || max_bytes > trace_contracts::plugin_chunks::METADATA_MAX {
            return Err(CoreError::InvalidInput("metadata body budget".into()));
        }
        let mut parsed = validate_url(url, rules)?;
        let mut headers = self.build_headers(rules, extra_public, cookie, &parsed)?;
        crate::site_context::decorate(
            &mut parsed,
            method,
            cookie,
            &mut headers,
            time::OffsetDateTime::now_utc().unix_timestamp(),
        )?;
        let m = match method.to_ascii_uppercase().as_str() {
            "GET" => reqwest::Method::GET,
            "HEAD" => reqwest::Method::HEAD,
            "POST" => reqwest::Method::POST,
            _ => return Err(CoreError::InvalidInput("method".into())),
        };
        let resp = self.request(rules, m, parsed, headers, true).await?;
        let status = resp.status().as_u16();
        let etag = resp
            .headers()
            .get(reqwest::header::ETAG)
            .and_then(|v| v.to_str().ok())
            .map(|s| s.to_string());
        let content_length = resp.content_length().map(|l| l as i64);
        if !resp.status().is_success() {
            // 不回显响应体；仅状态与最终主机（C26/C13）。
            let host = resp.url().host_str().unwrap_or_default().to_string();
            let _ = host;
            return Err(match status {
                403 | 412 => {
                    CoreError::Adapter(trace_domain::errors::FailureClass::PermissionDenied)
                }
                _ => status_error(status),
            });
        }
        let final_url = crate::site_context::public_url(resp.url());
        let host = resp.url().host_str().unwrap_or_default().to_string();
        // 有界读取：超限拒绝而不是静默截断。
        let mut body = Vec::new();
        let mut stream = resp;
        while let Some(chunk) = stream
            .chunk()
            .await
            .map_err(|e| CoreError::Network(sanitize_reqwest(&e)))?
        {
            if body.len() + chunk.len() > max_bytes as usize {
                return Err(CoreError::InvalidInput("response exceeds max bytes".into()));
            }
            if let Some(budget) = &self.budget {
                budget.received(chunk.len())?;
            }
            body.extend_from_slice(&chunk);
        }
        if let Some(budget) = &self.budget {
            budget.observe_response(&final_url, &body)?;
        }
        Ok(FetchOutcome {
            status,
            final_host: host,
            final_url,
            body,
            etag,
            content_length,
        })
    }

    /// 媒体下载：流式写入 + 进度回调 + 取消 + 可选 Range 续传。
    /// 返回 (总字节, ETag 校验器)。
    #[allow(clippy::too_many_arguments)]
    pub async fn download_media(
        &self,
        rules: &HostRules,
        url: &str,
        extra_public: Option<&serde_json::Value>,
        start_offset: u64,
        resume_validator: Option<&str>,
        writer: ChunkWriter<'_>,
        cancel: &CancellationToken,
        expect_content_length: Option<i64>,
    ) -> Result<(u64, Option<String>), CoreError> {
        let expected_total = expect_content_length
            .map(|n| {
                u64::try_from(n).map_err(|_| CoreError::InvalidInput("negative media size".into()))
            })
            .transpose()?;
        if start_offset > 0
            && resume_validator
                .is_none_or(|v| v.starts_with("W/") || !v.starts_with('"') || !v.ends_with('"'))
        {
            return Err(CoreError::InvalidInput(
                "resume requires a strong ETag".into(),
            ));
        }
        let parsed = validate_url(url, rules)?;
        let mut headers = self.build_headers(rules, extra_public, None, &parsed)?;
        if start_offset > 0
            && let Some(validator) = resume_validator
        {
            headers.insert(
                reqwest::header::RANGE,
                HeaderValue::from_str(&format!("bytes={start_offset}-")).unwrap(),
            );
            headers.insert(
                reqwest::header::IF_RANGE,
                HeaderValue::from_str(validator)
                    .map_err(|_| CoreError::InvalidInput("validator".into()))?,
            );
        }
        let resp = tokio::select! {
            r = self.request(rules,reqwest::Method::GET,parsed,headers,false) => r?,
            _ = cancel.cancelled() => return Err(CoreError::Cancelled),
        };
        let status = resp.status().as_u16();
        let etag = resp
            .headers()
            .get(reqwest::header::ETAG)
            .and_then(|v| v.to_str().ok())
            .map(|s| s.to_string());
        let mut required_total = expected_total.or(resp.content_length());
        if start_offset > 0 && status == 200 {
            return Err(CoreError::Network(
                "range unsupported: server returned 200".into(),
            ));
        }
        if start_offset > 0 && status == 206 {
            // 校验 Content-Range 起点与身份（C28）。
            let cr = resp
                .headers()
                .get(reqwest::header::CONTENT_RANGE)
                .and_then(|v| v.to_str().ok())
                .unwrap_or_default();
            let range = cr
                .strip_prefix("bytes ")
                .and_then(|r| r.split_once('/'))
                .and_then(|(span, total)| {
                    span.split_once('-').map(|(start, end)| (start, end, total))
                })
                .and_then(|(start, end, total)| {
                    Some((
                        start.parse::<u64>().ok()?,
                        end.parse::<u64>().ok()?,
                        total.parse::<u64>().ok()?,
                    ))
                });
            let Some((start, end, total)) = range else {
                return Err(CoreError::Network("invalid content-range".into()));
            };
            if start != start_offset
                || end < start
                || end.checked_add(1) != Some(total)
                || resp.content_length().is_some_and(|n| n != end - start + 1)
                || expect_content_length.is_some_and(|n| n < 0 || n as u64 != total)
            {
                return Err(CoreError::Network("inconsistent content-range".into()));
            }
            if let Some(v) = resume_validator
                && etag.as_deref() != Some(v)
            {
                return Err(CoreError::Network("object validator changed".into()));
            }
            required_total = Some(total);
        } else if status == 206 {
            return Err(CoreError::Network("unsolicited partial response".into()));
        } else if status != 200 {
            return Err(status_error(status));
        }
        if let (Some(exp), Some(actual)) = (expect_content_length, resp.content_length())
            && start_offset == 0
            && exp != actual as i64
        {
            return Err(CoreError::InvalidInput(format!(
                "content-length {actual} != expected {exp}"
            )));
        }
        if stream_is_html(resp.headers()) {
            return Err(CoreError::InvalidInput("HTML is not media".into()));
        }
        let mut offset = if status == 206 { start_offset } else { 0 };
        let mut stream = resp;
        let mut last_progress = tokio::time::Instant::now();
        loop {
            tokio::select! {
                chunk = stream.chunk() => {
                    match chunk {
                        Ok(Some(bytes)) => {
                            self.received_media(bytes.len())?;
                            let next = offset.checked_add(bytes.len() as u64)
                                .ok_or_else(|| CoreError::Network("media length overflow".into()))?;
                            if required_total.is_some_and(|total| next > total) {
                                return Err(CoreError::Network("media exceeds declared length".into()));
                            }
                            for chunk in bytes.chunks(256 * 1024) {
                                writer.0.write_chunk(writer.1, writer.2, chunk).await?;
                            }
                            offset = next;
                            last_progress = tokio::time::Instant::now();
                        }
                        Ok(None) => break,
                        Err(e) => return Err(CoreError::Network(sanitize_reqwest(&e))),
                    }
                }
                _ = tokio::time::sleep(Duration::from_secs(60)) => {
                    // 无进展超时：大文件不设总时限，但卡死按瞬时失败重试。
                    if last_progress.elapsed() >= Duration::from_secs(60) {
                        return Err(CoreError::Network("download stalled (no progress for 60s)".into()));
                    }
                }
                _ = cancel.cancelled() => return Err(CoreError::Cancelled),
            }
        }
        if required_total.is_some_and(|total| offset != total) {
            return Err(CoreError::Network(
                "media ended before declared length".into(),
            ));
        }
        Ok((offset, etag))
    }
}

fn status_error(status: u16) -> CoreError {
    match status {
        429 => CoreError::RateLimited("HTTP 429".into()),
        401 => CoreError::AuthRequired("HTTP 401".into()),
        403 | 404 | 410 => CoreError::SourceUnavailable(format!(
            "media source unavailable HTTP {status}; re-resolve"
        )),
        500..=599 => CoreError::Network(format!("HTTP {status}")),
        _ => CoreError::InvalidInput(format!("HTTP {status}")),
    }
}

fn stream_is_html(headers: &HeaderMap) -> bool {
    headers
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|s| s.to_ascii_lowercase().contains("text/html"))
}

/// reqwest 错误脱敏：只保留类别信息，不带 URL 查询参数（签名地址）。
fn sanitize_reqwest(e: &reqwest::Error) -> String {
    if e.is_timeout() {
        "timeout".into()
    } else if e.is_connect() {
        "connect failed".into()
    } else if e.is_decode() {
        "decode failed".into()
    } else {
        "request failed".into()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn host_rules_match_suffixes_only() {
        let rules = HostRules {
            api_hosts: vec!["api.bilibili.com".into()],
            media_hosts: vec!["bilivideo.com".into()],
            allowed_headers: vec![],
            test_allowed: vec![],
        };
        assert!(host_matches("api.bilibili.com", &rules.api_hosts));
        assert!(host_matches("API.BILIBILI.COM", &rules.api_hosts));
        assert!(!host_matches(
            "evil-api.bilibili.com.evil.io",
            &rules.api_hosts
        ));
        assert!(host_matches(
            "upos-sz-mirror.bilivideo.com",
            &rules.media_hosts
        ));
        assert!(!host_matches("bilibili.com.evil.io", &rules.media_hosts));
    }

    #[test]
    fn forbidden_ips_cover_private_and_metadata_ranges() {
        assert!(is_forbidden_ip(&"127.0.0.1".parse().unwrap()));
        assert!(is_forbidden_ip(&"169.254.169.254".parse().unwrap()));
        assert!(is_forbidden_ip(&"10.0.0.5".parse().unwrap()));
        assert!(is_forbidden_ip(&"192.168.1.1".parse().unwrap()));
        assert!(is_forbidden_ip(&"::1".parse().unwrap()));
        assert!(!is_forbidden_ip(&"8.8.8.8".parse().unwrap()));
    }

    #[test]
    fn validate_url_rejects_non_https_and_unknown_hosts() {
        let rules = host_rules(&Profile::default());
        assert!(validate_url("http://api.bilibili.com/x", &rules).is_err());
        assert!(validate_url("https://evil.io/x", &rules).is_err());
        assert!(validate_url("file:///c:/x", &rules).is_err());
        assert!(validate_url("https://api.bilibili.com/x", &rules).is_ok());
    }

    #[test]
    fn test_allowed_hosts_bypass_scheme_and_loopback_only_when_explicit() {
        // 显式测试宿主：允许 http + 回环（离线 e2e 本地服务器）。
        let profile = Profile {
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Profile::default()
        };
        let rules = host_rules(&profile);
        assert!(validate_url("http://127.0.0.1:8099/api", &rules).is_ok());
        // 未列入测试宿主时回环仍被拒。
        let strict = host_rules(&Profile::default());
        assert!(validate_url("http://127.0.0.1:8099/api", &strict).is_err());
    }
}

#[cfg(test)]
mod fault_tests {
    use super::*;
    use crate::{
        db::DbActor,
        objects::{ObjectStore, ReceiveParams},
    };
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use trace_domain::ids::OperationId;

    #[tokio::test]
    async fn c06_site_check_budget_applies_to_real_http_redirect_hops() {
        let failed = source(
            "HTTP/1.1 429 Too Many Requests\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                .into(),
            0,
            0,
        )
        .await;
        let first=source(format!("HTTP/1.1 302 Found\r\nLocation: {failed}?token=PRIVATE_REDIRECT\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"),0,0).await;
        let budget =
            std::sync::Arc::new(crate::request_budget::RequestBudget::new(2, 20, 1000).unwrap());
        let client = ConstrainedClient::build()
            .unwrap()
            .with_budget(budget.clone());
        let rules = host_rules(&Profile {
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Default::default()
        });
        assert!(
            client
                .fetch_metadata(&rules, "GET", &first, None, None, 1024)
                .await
                .is_err()
        );
        assert!(
            client
                .fetch_metadata(&rules, "GET", &first, None, None, 1024)
                .await
                .is_err()
        );
        let result = budget.snapshot();
        assert_eq!(result["request_count"], 2);
        assert_eq!(result["requests"][0]["status"], 302);
        assert_eq!(result["requests"][1]["status"], 429);
        assert!(!result.to_string().contains("PRIVATE_REDIRECT"));
    }

    async fn source(headers: String, chunks: usize, delay_ms: u64) -> String {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = vec![0; 4096];
            let _ = socket.read(&mut request).await;
            if socket.write_all(headers.as_bytes()).await.is_err() {
                return;
            }
            let chunk = vec![9; 1024 * 1024];
            for _ in 0..chunks {
                if socket.write_all(&chunk).await.is_err() {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(delay_ms)).await;
            }
        });
        format!("http://127.0.0.1:{port}/media")
    }

    #[tokio::test]
    async fn f007_http_range_resume_checks_identity_and_full_hash() {
        let dir = tempfile::tempdir().unwrap();
        let profile = Profile {
            data_root: dir.path().join("data"),
            archive_root: dir.path().join("archive"),
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Profile::default()
        };
        std::fs::create_dir_all(profile.data_root.join("state")).unwrap();
        let (db, _actor) =
            DbActor::start(rusqlite::Connection::open(profile.db_path()).unwrap(), 64).unwrap();
        let store = ObjectStore::new(profile.clone());
        let prefix = vec![1; crate::config::CHECKPOINT_INTERVAL_BYTES as usize];
        let total = prefix.len() + 1024 * 1024;
        let op = OperationId::new("http-resume");
        let params = ReceiveParams {
            job_id: None,
            subject_kind: "test".into(),
            subject_id: "test".into(),
            generation: 1,
            mime: None,
            expected_size: Some(total as i64),
            expected_sha256: None,
            source_validator: Some("\"fixed\"".into()),
        };
        let mut writer = store
            .start_receiving(&db, params.clone(), &op)
            .await
            .unwrap();
        writer.write_chunk(&db, &op, &prefix).await.unwrap();
        drop(writer);
        let mut writer = store.start_receiving(&db, params, &op).await.unwrap();
        let client = ConstrainedClient::build().unwrap();
        for (status, range, etag) in [
            (
                "200 OK",
                format!("bytes {}-{}/{}", prefix.len(), total - 1, total),
                "\"fixed\"",
            ),
            (
                "206 Partial Content",
                format!("bytes 0-{}/{}", total - 1, total),
                "\"fixed\"",
            ),
            (
                "206 Partial Content",
                format!("bytes {}-{}/{}", prefix.len(), total - 1, total),
                "\"changed\"",
            ),
            ("206 Partial Content", "bytes invalid".into(), "\"fixed\""),
        ] {
            let url = source(format!("HTTP/1.1 {status}\r\nContent-Length: 1048576\r\nContent-Range: {range}\r\nETag: {etag}\r\nConnection: close\r\n\r\n"),1,0).await;
            assert!(
                client
                    .download_media(
                        &host_rules(&profile),
                        &url,
                        None,
                        prefix.len() as u64,
                        Some("\"fixed\""),
                        (&mut writer, &db, &op),
                        &CancellationToken::new(),
                        Some(total as i64)
                    )
                    .await
                    .is_err()
            );
        }
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/range", listener.local_addr().unwrap());
        let offset = prefix.len();
        let server = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = Vec::new();
            while !request.ends_with(b"\r\n\r\n") {
                request.push(socket.read_u8().await.unwrap());
            }
            let request = String::from_utf8(request).unwrap().to_lowercase();
            assert!(request.contains(&format!("range: bytes={offset}-")));
            assert!(request.contains("if-range: \"fixed\""));
            socket.write_all(format!("HTTP/1.1 206 Partial Content\r\nContent-Length: 1048576\r\nContent-Range: bytes {offset}-{}/{total}\r\nETag: \"fixed\"\r\nConnection: close\r\n\r\n",total-1).as_bytes()).await.unwrap();
            socket.write_all(&vec![9; 1024 * 1024]).await.unwrap();
        });
        client
            .download_media(
                &host_rules(&profile),
                &url,
                None,
                prefix.len() as u64,
                Some("\"fixed\""),
                (&mut writer, &db, &op),
                &CancellationToken::new(),
                Some(total as i64),
            )
            .await
            .unwrap();
        server.await.unwrap();
        let (size, sha) = store.finish_and_validate(&db, &op, writer).await.unwrap();
        use sha2::Digest;
        let mut hash = sha2::Sha256::new();
        hash.update(&prefix);
        hash.update(vec![9; 1024 * 1024]);
        let expected: String = hash.finalize().iter().map(|b| format!("{b:02x}")).collect();
        assert_eq!(size, total as u64);
        assert_eq!(sha, expected);
        assert!(matches!(status_error(429), CoreError::RateLimited(_)));
        for code in [403, 404, 410] {
            assert!(matches!(
                status_error(code),
                CoreError::SourceUnavailable(_)
            ));
        }
        assert!(matches!(status_error(503), CoreError::Network(_)));
    }

    #[tokio::test]
    async fn f011_redirects_validate_each_host_and_private_address() {
        let profile = Profile {
            test_allowed_hosts: vec!["127.0.0.1".into()],
            // Allowed as media hosts, but still forbidden by their resolved address classes.
            extra_media_hosts: vec!["127.0.0.2".into(), "169.254.169.254".into()],
            ..Profile::default()
        };
        for target in [
            "https://example.invalid/media",
            "https://127.0.0.2/private",
            "https://169.254.169.254/metadata",
        ] {
            let url=source(format!("HTTP/1.1 302 Found\r\nLocation: {target}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"),0,0).await;
            assert!(
                ConstrainedClient::build()
                    .unwrap()
                    .fetch_metadata(&host_rules(&profile), "GET", &url, None, None, 100)
                    .await
                    .is_err()
            );
        }
    }

    #[tokio::test]
    async fn c13_metadata_403_412_stop_as_access_restrictions_without_empty_success_or_cookie_expiry()
     {
        let profile = Profile {
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Default::default()
        };
        for status in [403, 412] {
            let url=source(format!("HTTP/1.1 {status} Restricted\r\nContent-Length: 21\r\nConnection: close\r\n\r\nSYNTHETIC_SECRET_BODY"),0,0).await;
            let error = ConstrainedClient::build()
                .unwrap()
                .fetch_metadata(&host_rules(&profile), "GET", &url, None, None, 100)
                .await
                .err()
                .unwrap();
            assert_eq!(
                error.failure_class(),
                trace_domain::errors::FailureClass::PermissionDenied
            );
            assert!(!error.failure_class().suggest_retryable());
            assert!(!format!("{error:?}").contains("SYNTHETIC_SECRET_BODY"));
        }
        // Media URL expiration retains the original re-resolution contract.
        assert!(matches!(status_error(403), CoreError::SourceUnavailable(_)));
    }

    #[tokio::test]
    async fn f007_stream_over_thirty_seconds_and_cancellation() {
        let dir = tempfile::tempdir().unwrap();
        let profile = Profile {
            data_root: dir.path().join("data"),
            archive_root: dir.path().join("archive"),
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Profile::default()
        };
        std::fs::create_dir_all(profile.data_root.join("state")).unwrap();
        let (db, _actor) =
            DbActor::start(rusqlite::Connection::open(profile.db_path()).unwrap(), 64).unwrap();
        let store = ObjectStore::new(profile.clone());
        let op = OperationId::new("slow");
        let params = ReceiveParams {
            job_id: None,
            subject_kind: "test".into(),
            subject_id: "test".into(),
            generation: 1,
            mime: None,
            expected_size: Some(33 * 1024 * 1024),
            expected_sha256: None,
            source_validator: None,
        };
        let mut writer = store
            .start_receiving(&db, params.clone(), &op)
            .await
            .unwrap();
        let url = source(
            format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                33 * 1024 * 1024
            ),
            33,
            1000,
        )
        .await;
        let start = std::time::Instant::now();
        ConstrainedClient::build()
            .unwrap()
            .download_media(
                &host_rules(&profile),
                &url,
                None,
                0,
                None,
                (&mut writer, &db, &op),
                &CancellationToken::new(),
                params.expected_size,
            )
            .await
            .unwrap();
        assert!(start.elapsed() >= Duration::from_secs(30));
        let (size, sha) = store.finish_and_validate(&db, &op, writer).await.unwrap();
        assert_eq!(size, 33 * 1024 * 1024);
        let payload = profile.staging_dir().join("slow/payload");
        assert_eq!(crate::objects::hash_file(&payload).unwrap(), sha);
        let op = OperationId::new("cancel");
        let mut writer = store.start_receiving(&db, params, &op).await.unwrap();
        let url = source(
            format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                33 * 1024 * 1024
            ),
            33,
            1000,
        )
        .await;
        let cancel = CancellationToken::new();
        let signal = cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(150)).await;
            signal.cancel();
        });
        let start = std::time::Instant::now();
        let result = ConstrainedClient::build()
            .unwrap()
            .download_media(
                &host_rules(&profile),
                &url,
                None,
                0,
                None,
                (&mut writer, &db, &op),
                &cancel,
                None,
            )
            .await;
        assert!(matches!(result, Err(CoreError::Cancelled)));
        assert!(start.elapsed() < Duration::from_secs(3));
    }
}
