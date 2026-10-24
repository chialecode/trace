//! Network route per site: direct, the system proxy, or a manual HTTP/SOCKS5 proxy
//! (docs/design/multi-site-contracts.md §9).
//!
//! Settings are global with optional per-site overrides. With no saved setting the four
//! overseas sites follow the system proxy and the others connect directly. "Follow the
//! system" with the switch on but no address — the state a TUN-mode proxy tool leaves — is a
//! direct connection, which keeps the existing DNS pinning and address checks unchanged.
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::sync::RwLock;
use trace_domain::sites::{self, NetworkAdvice};

pub const GLOBAL: &str = "global";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Mode {
    Direct,
    System,
    Manual,
}

impl Mode {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Direct => "direct",
            Self::System => "system",
            Self::Manual => "manual",
        }
    }

    pub fn parse(value: &str) -> Option<Self> {
        match value {
            "direct" => Some(Self::Direct),
            "system" => Some(Self::System),
            "manual" => Some(Self::Manual),
            _ => None,
        }
    }
}

/// A saved choice for a scope (`global` or a site id).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Setting {
    pub mode: Mode,
    /// `http://host:port` or `socks5://host:port` for manual mode.
    pub proxy_url: Option<String>,
    /// Vault reference of the proxy login, when one is saved.
    pub auth_ref: Option<String>,
}

/// A proxy endpoint a request goes through.
#[derive(Clone, PartialEq, Eq)]
pub struct Proxy {
    pub url: String,
    pub auth: Option<(String, String)>,
}

impl std::fmt::Debug for Proxy {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Proxy")
            .field("url", &self.url)
            .field("auth", &self.auth.as_ref().map(|_| "<hidden>"))
            .finish()
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub enum Route {
    #[default]
    Direct,
    Proxy(Proxy),
}

/// The operating system's proxy setting as read, before interpretation.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SystemProxy {
    pub enabled: bool,
    pub server: String,
}

/// A manual proxy address: scheme `http` or `socks5`, a host and an explicit port, nothing else.
pub fn validate_proxy_url(raw: &str) -> Result<String, &'static str> {
    let url = url::Url::parse(raw.trim()).map_err(|_| "代理地址格式不正确")?;
    if !matches!(url.scheme(), "http" | "socks5") {
        return Err("代理地址需要以 http:// 或 socks5:// 开头");
    }
    let host = url
        .host_str()
        .filter(|h| !h.is_empty())
        .ok_or("代理地址缺少主机")?;
    let port = url.port().ok_or("代理地址需要带端口")?;
    if !url.username().is_empty() || url.password().is_some() {
        return Err("代理的用户名和密码请单独填写");
    }
    if url.query().is_some() || url.fragment().is_some() || !matches!(url.path(), "" | "/") {
        return Err("代理地址只需要主机和端口");
    }
    Ok(format!("{}://{host}:{port}", url.scheme()))
}

/// Reads the system setting string. `host:port` applies to every protocol; a list
/// `http=a:1;https=b:2;socks=c:3` offers per-protocol entries, and HTTPS traffic prefers
/// the https entry. An empty or disabled setting means no proxy.
pub fn interpret_system(system: &SystemProxy) -> Option<String> {
    let server = system.server.trim();
    if !system.enabled || server.is_empty() {
        return None;
    }
    let entry = if server.contains('=') {
        let pick = |name: &str| {
            server.split(';').find_map(|part| {
                let (protocol, address) = part.split_once('=')?;
                (protocol.trim().eq_ignore_ascii_case(name) && !address.trim().is_empty())
                    .then(|| (name.to_owned(), address.trim().to_owned()))
            })
        };
        pick("https")
            .or_else(|| pick("http"))
            .or_else(|| pick("socks"))?
    } else {
        ("http".to_owned(), server.to_owned())
    };
    let (protocol, address) = entry;
    let address = address
        .strip_prefix("http://")
        .or_else(|| address.strip_prefix("socks5://"))
        .unwrap_or(&address);
    let scheme = if protocol == "socks" {
        "socks5"
    } else {
        "http"
    };
    validate_proxy_url(&format!("{scheme}://{address}")).ok()
}

#[cfg(windows)]
fn read_system() -> SystemProxy {
    use windows::{
        Win32::System::Registry::{
            HKEY_CURRENT_USER, RRF_RT_REG_DWORD, RRF_RT_REG_SZ, RegGetValueW,
        },
        core::HSTRING,
    };
    const KEY: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings";
    let mut enable = 0u32;
    let mut size = 4u32;
    let found = unsafe {
        RegGetValueW(
            HKEY_CURRENT_USER,
            &HSTRING::from(KEY),
            &HSTRING::from("ProxyEnable"),
            RRF_RT_REG_DWORD,
            None,
            Some((&mut enable as *mut u32).cast()),
            Some(&mut size),
        )
    };
    if found.is_err() || enable == 0 {
        return SystemProxy::default();
    }
    let mut buffer = vec![0u16; 2048];
    let mut bytes = (buffer.len() * 2) as u32;
    let status = unsafe {
        RegGetValueW(
            HKEY_CURRENT_USER,
            &HSTRING::from(KEY),
            &HSTRING::from("ProxyServer"),
            RRF_RT_REG_SZ,
            None,
            Some(buffer.as_mut_ptr().cast()),
            Some(&mut bytes),
        )
    };
    let server = if status.is_ok() {
        String::from_utf16_lossy(&buffer[..(bytes as usize / 2).saturating_sub(1)])
    } else {
        String::new()
    };
    SystemProxy {
        enabled: true,
        server,
    }
}

#[cfg(not(windows))]
fn read_system() -> SystemProxy {
    let server = ["HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"]
        .iter()
        .find_map(|name| std::env::var(name).ok())
        .unwrap_or_default();
    SystemProxy {
        enabled: !server.is_empty(),
        server,
    }
}

type SystemReader = Box<dyn Fn() -> SystemProxy + Send + Sync>;

pub struct NetworkState {
    settings: RwLock<HashMap<String, Setting>>,
    auth: RwLock<HashMap<String, (String, String)>>,
    system: SystemReader,
}

impl Default for NetworkState {
    fn default() -> Self {
        Self::new(Box::new(read_system))
    }
}

impl NetworkState {
    pub fn new(system: SystemReader) -> Self {
        Self {
            settings: RwLock::default(),
            auth: RwLock::default(),
            system,
        }
    }

    /// Everything direct, whatever the system says: for components and tests with no network
    /// settings of their own.
    pub fn direct_only() -> Self {
        let state = Self::new(Box::new(SystemProxy::default));
        state.replace(HashMap::new(), HashMap::new());
        state
    }

    pub fn replace(
        &self,
        settings: HashMap<String, Setting>,
        auth: HashMap<String, (String, String)>,
    ) {
        *self.settings.write().unwrap() = settings;
        *self.auth.write().unwrap() = auth;
    }

    pub fn set(&self, scope: &str, setting: Setting, auth: Option<(String, String)>) {
        self.settings
            .write()
            .unwrap()
            .insert(scope.to_owned(), setting);
        let mut logins = self.auth.write().unwrap();
        match auth {
            Some(login) => logins.insert(scope.to_owned(), login),
            None => logins.remove(scope),
        };
    }

    /// The operating system's proxy setting as it is now.
    pub fn system(&self) -> SystemProxy {
        (self.system)()
    }

    pub fn clear(&self, scope: &str) {
        self.settings.write().unwrap().remove(scope);
        self.auth.write().unwrap().remove(scope);
    }

    pub fn setting(&self, scope: &str) -> Option<Setting> {
        self.settings.read().unwrap().get(scope).cloned()
    }

    /// The saved choice that applies to a site: its own, else the global one, else the
    /// default for the site (system proxy for the overseas four, direct for the rest).
    pub fn effective(&self, site: &str) -> (String, Setting) {
        let settings = self.settings.read().unwrap();
        for scope in [site, GLOBAL] {
            if let Some(setting) = settings.get(scope) {
                return (scope.to_owned(), setting.clone());
            }
        }
        let mode = match sites::site(site).map(|s| s.network) {
            Some(NetworkAdvice::ProxyRecommended) => Mode::System,
            _ => Mode::Direct,
        };
        (
            "default".into(),
            Setting {
                mode,
                proxy_url: None,
                auth_ref: None,
            },
        )
    }

    pub fn route_for(&self, site: &str) -> Route {
        let (scope, setting) = self.effective(site);
        match setting.mode {
            Mode::Direct => Route::Direct,
            Mode::System => interpret_system(&(self.system)())
                .map(|url| Route::Proxy(Proxy { url, auth: None }))
                .unwrap_or_default(),
            Mode::Manual => setting
                .proxy_url
                .map(|url| {
                    let auth = self.auth.read().unwrap().get(&scope).cloned();
                    Route::Proxy(Proxy { url, auth })
                })
                .unwrap_or_default(),
        }
    }

    /// A plain-language description of how a site connects now, for settings pages.
    pub fn describe(&self, site: &str) -> String {
        let (_, setting) = self.effective(site);
        match (setting.mode, self.route_for(site)) {
            (Mode::Direct, _) => "直接连接".into(),
            (Mode::System, Route::Proxy(proxy)) => format!("跟随系统代理（{}）", proxy.url),
            (Mode::System, Route::Direct) => "跟随系统代理（系统没有代理地址，直接连接）".into(),
            (Mode::Manual, Route::Proxy(proxy)) => format!("手动代理（{}）", proxy.url),
            (Mode::Manual, Route::Direct) => "手动代理（地址缺失，直接连接）".into(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state(system: SystemProxy) -> NetworkState {
        NetworkState::new(Box::new(move || system.clone()))
    }

    fn system(server: &str) -> SystemProxy {
        SystemProxy {
            enabled: true,
            server: server.into(),
        }
    }

    #[test]
    fn manual_addresses_are_http_or_socks5_with_host_and_port_only() {
        assert_eq!(
            validate_proxy_url("http://127.0.0.1:7890").unwrap(),
            "http://127.0.0.1:7890"
        );
        assert_eq!(
            validate_proxy_url(" socks5://proxy.local:1080/ ").unwrap(),
            "socks5://proxy.local:1080"
        );
        for bad in [
            "127.0.0.1:7890",
            "ftp://h:1",
            "http://h",
            "http://user:pw@h:1",
            "http://h:1/path",
            "http://h:1?x=1",
            "https://h:1",
            "",
        ] {
            assert!(validate_proxy_url(bad).is_err(), "{bad} must be refused");
        }
    }

    #[test]
    fn the_system_setting_is_interpreted_and_an_empty_address_means_direct() {
        assert_eq!(
            interpret_system(&system("127.0.0.1:7890")).as_deref(),
            Some("http://127.0.0.1:7890")
        );
        assert_eq!(
            interpret_system(&system("http=a:1;https=b:2;socks=c:3")).as_deref(),
            Some("http://b:2")
        );
        assert_eq!(
            interpret_system(&system("socks=c:3")).as_deref(),
            Some("socks5://c:3")
        );
        assert_eq!(
            interpret_system(&system("")),
            None,
            "switch on, no address: a TUN tool, so direct"
        );
        assert_eq!(interpret_system(&system("   ")), None);
        assert_eq!(
            interpret_system(&SystemProxy {
                enabled: false,
                server: "127.0.0.1:7890".into()
            }),
            None
        );
        assert_eq!(interpret_system(&system("garbage")), None);
    }

    #[test]
    fn defaults_follow_the_system_for_overseas_sites_and_connect_directly_elsewhere() {
        let network = state(system("127.0.0.1:7890"));
        for overseas in ["instagram", "x", "youtube", "tiktok"] {
            assert!(
                matches!(network.route_for(overseas), Route::Proxy(_)),
                "{overseas}"
            );
        }
        for domestic in ["weibo", "xiaohongshu", "douyin", "bilibili"] {
            assert_eq!(network.route_for(domestic), Route::Direct, "{domestic}");
        }
        let tun = state(SystemProxy {
            enabled: true,
            server: String::new(),
        });
        assert_eq!(tun.route_for("youtube"), Route::Direct);
    }

    #[test]
    fn a_site_setting_beats_the_global_one_and_the_global_one_beats_the_default() {
        let network = state(system("127.0.0.1:7890"));
        let manual = |url: &str| Setting {
            mode: Mode::Manual,
            proxy_url: Some(url.into()),
            auth_ref: None,
        };
        network.set(GLOBAL, manual("http://g:1"), None);
        assert!(matches!(network.route_for("weibo"), Route::Proxy(p) if p.url == "http://g:1"));
        network.set(
            "weibo",
            Setting {
                mode: Mode::Direct,
                proxy_url: None,
                auth_ref: None,
            },
            None,
        );
        assert_eq!(network.route_for("weibo"), Route::Direct);
        network.set("x", manual("socks5://s:2"), Some(("u".into(), "p".into())));
        let Route::Proxy(proxy) = network.route_for("x") else {
            panic!("expected a proxy")
        };
        assert_eq!(proxy.auth, Some(("u".into(), "p".into())));
        assert!(!format!("{proxy:?}").contains('p') || !format!("{proxy:?}").contains("\"p\""));
        network.clear("x");
        assert!(matches!(network.route_for("x"), Route::Proxy(p) if p.url == "http://g:1"));
    }

    #[test]
    fn descriptions_say_what_will_happen() {
        let network = state(SystemProxy::default());
        assert_eq!(network.describe("bilibili"), "直接连接");
        assert!(network.describe("youtube").contains("系统没有代理地址"));
    }
}
