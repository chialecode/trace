//! Network settings: how each site's requests leave this machine. The proxy login goes to
//! the credential store; the database keeps only its reference.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::network::{GLOBAL, Mode, NetworkState, Setting, validate_proxy_url};
use crate::{jobs, store};
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;
use trace_contracts::network::{
    NetworkScope, NetworkSetArgs, NetworkSite, NetworkView, SystemProxyView,
};
use trace_domain::sites;

const MAX_LOGIN_PART: usize = 256;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    match req.command.as_str() {
        cmd::NETWORK_GET => Ok(serde_json::to_value(view(&core.network))?),
        cmd::NETWORK_SET => {
            set(core, parse(req.args.clone())?).await?;
            Ok(serde_json::to_value(view(&core.network))?)
        }
        other => Err(unsupported(other)),
    }
}

/// Loads the saved choices (and their logins) into the live state, at start.
pub async fn load(core: &Core) -> Result<(), CoreError> {
    let saved = core.db.read(store::network::load).await?;
    let mut settings = std::collections::HashMap::new();
    let mut logins = std::collections::HashMap::new();
    for (scope, setting) in saved {
        if let Some(reference) = &setting.auth_ref
            && let Some(login) = read_login(core, reference)
        {
            logins.insert(scope.clone(), login);
        }
        settings.insert(scope, setting);
    }
    core.network.replace(settings, logins);
    Ok(())
}

fn login_ref(scope: &str) -> String {
    format!("proxy-{scope}")
}

fn read_login(core: &Core, reference: &str) -> Option<(String, String)> {
    let bytes = core.vault.load(reference).ok()?;
    let value: Value = serde_json::from_slice(&bytes).ok()?;
    Some((
        value["u"].as_str()?.to_owned(),
        value["p"].as_str()?.to_owned(),
    ))
}

fn valid_scope(scope: &str) -> bool {
    scope == GLOBAL || sites::site(scope).is_some()
}

async fn set(core: &Arc<Core>, args: NetworkSetArgs) -> Result<(), CoreError> {
    if !valid_scope(&args.scope) {
        return Err(CoreError::InvalidInput("unknown network scope".into()));
    }
    let scope = args.scope.clone();
    if args.mode == "default" {
        core.db
            .write({
                let scope = scope.clone();
                move |tx| store::network::delete(tx, &scope)
            })
            .await?;
        core.vault.remove(&login_ref(&scope))?;
        core.network.clear(&scope);
        return Ok(());
    }
    let mode =
        Mode::parse(&args.mode).ok_or_else(|| CoreError::InvalidInput("unknown mode".into()))?;
    let mut setting = Setting {
        mode,
        proxy_url: None,
        auth_ref: None,
    };
    let mut login = None;
    let mut typed = None;
    if mode == Mode::Manual {
        let url = args
            .proxy_url
            .as_deref()
            .ok_or_else(|| CoreError::InvalidInput("手动代理需要填写代理地址".into()))?;
        setting.proxy_url =
            Some(validate_proxy_url(url).map_err(|e| CoreError::InvalidInput(e.into()))?);
        typed = typed_login(&args)?;
        login = match (&typed, args.keep_login) {
            (Some(new), _) => Some(new.clone()),
            (None, true) => read_login(core, &login_ref(&scope)),
            (None, false) => None,
        };
        if login.is_some() {
            setting.auth_ref = Some(login_ref(&scope));
        }
    } else if args.proxy_url.is_some() || args.username.is_some() || args.password.is_some() {
        return Err(CoreError::InvalidInput(
            "只有手动代理需要填写地址和登录信息".into(),
        ));
    }
    if let Some((user, password)) = &typed {
        let secret = serde_json::json!({"u": user, "p": password}).to_string();
        core.vault.store(&login_ref(&scope), secret.as_bytes())?;
    } else if login.is_none() {
        core.vault.remove(&login_ref(&scope))?;
    }
    let saved = setting.clone();
    core.db
        .write({
            let scope = scope.clone();
            move |tx| store::network::save(tx, &scope, &saved, &jobs::now())
        })
        .await?;
    core.network.set(&scope, setting, login);
    Ok(())
}

/// A login typed in the form: both parts together, or neither.
fn typed_login(args: &NetworkSetArgs) -> Result<Option<(String, String)>, CoreError> {
    let present = |value: &Option<String>| value.clone().filter(|v| !v.is_empty());
    match (present(&args.username), present(&args.password)) {
        (Some(user), Some(password)) => {
            if user.len() > MAX_LOGIN_PART || password.len() > MAX_LOGIN_PART {
                return Err(CoreError::InvalidInput("代理用户名或密码过长".into()));
            }
            Ok(Some((user, password)))
        }
        (None, None) => Ok(None),
        _ => Err(CoreError::InvalidInput(
            "代理用户名和密码需要一起填写".into(),
        )),
    }
}

pub fn view(network: &NetworkState) -> NetworkView {
    let mut settings = Vec::new();
    let scopes = std::iter::once(GLOBAL).chain(sites::SITES.iter().map(|s| s.id));
    for scope in scopes {
        if let Some(setting) = network.setting(scope) {
            settings.push(NetworkScope {
                scope: scope.into(),
                mode: setting.mode.as_str().into(),
                proxy_url: setting.proxy_url,
                has_login: setting.auth_ref.is_some(),
            });
        }
    }
    let sites_view = sites::SITES
        .iter()
        .map(|site| {
            let (source, setting) = network.effective(site.id);
            let proxied = matches!(network.route_for(site.id), crate::network::Route::Proxy(_));
            NetworkSite {
                site: site.id.into(),
                title: site.title.into(),
                advice: site.network.into(),
                source: if source == site.id {
                    "site".into()
                } else {
                    source
                },
                mode: setting.mode.as_str().into(),
                route: if proxied { "proxy" } else { "direct" }.into(),
                description: network.describe(site.id),
            }
        })
        .collect();
    let system = network.system();
    NetworkView {
        settings,
        sites: sites_view,
        system: SystemProxyView {
            enabled: system.enabled,
            address: crate::network::interpret_system(&system),
        },
    }
}
