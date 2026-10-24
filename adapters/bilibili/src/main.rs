//! Bilibili 平台适配器（插件协议 0.1，stdio 帧）。
//!
//! 进程模型与队列、截止时间、shutdown 行为由共享运行时提供
//! （`trace_site_support::runtime`）；所有网络访问经宿主受限代理，
//! Cookie 永不进入本进程。
#![cfg_attr(test, allow(clippy::too_many_lines))]

mod account;
mod api;
mod article;
mod audio;
mod browser;
mod clock;
mod content;
mod detail;
mod dispatch;
mod list;
mod live;
mod media;
mod rules;
mod sign;
mod source;
mod wbi;

use serde_json::Value;
use std::sync::Mutex;
use std::time::{Duration, Instant};
use trace_contracts::plugin::{PluginError, PluginRequest};
use trace_site_support::runtime::{self, Client, Transport};

/// 一次请求可见的适配器状态：代理客户端与跨请求共享的 WBI 密钥缓存。
pub struct AdapterState<'a> {
    pub client: &'a dyn Transport,
    pub wbi: &'a Mutex<wbi::WbiCache>,
}

fn handle(
    wbi: &Mutex<wbi::WbiCache>,
    client: &Client,
    req: &PluginRequest,
) -> Result<Value, PluginError> {
    let started = Instant::now();
    let deadline = Duration::from_millis(req.deadline_ms.unwrap_or(30_000));
    dispatch::dispatch(&AdapterState { client, wbi }, req, deadline, started)
}

fn main() {
    runtime::serve_with(Mutex::new(wbi::WbiCache::default()), handle);
}
