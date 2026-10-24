//! 站点网络范围与目标校验：声明域名、地址类别、直连/代理两种校验口径。
use crate::config::Profile;
use crate::error::CoreError;
use std::net::{IpAddr, ToSocketAddrs};
use trace_domain::sites;
use url::Url;

/// 平台网络范围（保守内置清单；profile.extra_media_hosts 可显式扩展）。
#[derive(Clone)]
pub struct HostRules {
    /// 元数据 API 域（允许请求）。
    pub api_hosts: Vec<String>,
    /// 允许携带 Cookie 的域：用户复制 Cookie 的那个站点域名，是 `api_hosts` 的子集；
    /// 同站点的其他接口域名只发无 Cookie 的请求。
    pub cookie_hosts: Vec<String>,
    /// 媒体/资源域（禁止 Cookie）。
    pub media_hosts: Vec<String>,
    /// 可选公共头白名单。
    pub allowed_headers: Vec<String>,
    /// 显式测试宿主（profile 文件配置）：允许 http/回环，仅离线 e2e。
    pub test_allowed: Vec<String>,
    /// 范围所属站点：决定走哪条网络路线（直连或代理）；未限定站点时一律直连。
    pub site: Option<String>,
}

/// 全部已注册站点的域名与 profile 额外媒体域；按站点收窄见 [`platform_rules`]。
pub fn host_rules(profile: &Profile) -> HostRules {
    let mut api: Vec<String> = Vec::new();
    let mut cookie: Vec<String> = Vec::new();
    let mut media: Vec<String> = Vec::new();
    for site in sites::SITES.iter() {
        api.extend(site.api_hosts.iter().map(|h| (*h).to_owned()));
        cookie.extend(site.cookie_hosts.iter().map(|h| (*h).to_owned()));
        media.extend(site.media_hosts.iter().map(|h| (*h).to_owned()));
    }
    media.extend(profile.extra_media_hosts.iter().cloned());
    #[cfg(debug_assertions)]
    let test_allowed: Vec<String> = profile.test_allowed_hosts.to_vec();
    #[cfg(not(debug_assertions))]
    let test_allowed: Vec<String> = Vec::new();
    HostRules {
        api_hosts: api,
        cookie_hosts: cookie,
        media_hosts: media,
        allowed_headers: sites::BASE_REQUEST_HEADERS
            .iter()
            .map(|h| (*h).to_owned())
            .collect(),
        test_allowed,
        site: None,
    }
}

pub fn host_matches(host: &str, rules: &[String]) -> bool {
    let host = host.trim_end_matches('.');
    let host = host.to_ascii_lowercase();
    rules
        .iter()
        .any(|r| host == *r || host.ends_with(&format!(".{r}")))
}

/// 把范围收窄到一个站点自己声明的域名（Cookie 只会发往该站的 Cookie 域），并记下站点，
/// 供网络路线选择。profile 额外媒体域对每个站点保留；未注册的平台只剩测试宿主。
pub fn platform_rules(rules: &HostRules, platform: &str) -> HostRules {
    let mut scoped = rules.clone();
    let Some(site) = sites::site(platform) else {
        scoped.api_hosts.clear();
        scoped.cookie_hosts.clear();
        scoped.media_hosts.clear();
        scoped.site = None;
        return scoped;
    };
    let owned = |list: &[&str], h: &str| list.contains(&h);
    scoped.api_hosts.retain(|h| owned(site.api_hosts, h));
    scoped.cookie_hosts.retain(|h| owned(site.cookie_hosts, h));
    scoped.media_hosts.retain(|h| {
        owned(site.media_hosts, h)
            || !sites::SITES
                .iter()
                .any(|other| owned(other.media_hosts, h) || owned(other.api_hosts, h))
    });
    scoped
        .allowed_headers
        .extend(site.request_headers.iter().map(|h| (*h).to_owned()));
    scoped.site = Some(site.id.to_owned());
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
#[cfg(test)]
pub(super) fn validate_url(raw: &str, rules: &HostRules) -> Result<Url, CoreError> {
    validate_url_for(raw, rules, true)
}

/// `resolve` 为真时（直连）校验本机解析出的地址类别；经代理时域名由代理解析，
/// 本机不解析，改为拒绝字面 IP 目标，域名必须在该站声明的范围内。
pub(super) fn validate_url_for(
    raw: &str,
    rules: &HostRules,
    resolve: bool,
) -> Result<Url, CoreError> {
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
    if matches!(url.host(), Some(url::Host::Ipv4(_) | url::Host::Ipv6(_))) {
        return Err(CoreError::Network("literal address target rejected".into()));
    }
    let port = url.port_or_known_default().unwrap_or(443);
    if !host_matches(host, &rules.api_hosts) && !host_matches(host, &rules.media_hosts) {
        return Err(CoreError::Network(format!("host {host} not allowed")));
    }
    if resolve {
        assert_public_host(host, port)?;
    }
    Ok(url)
}
