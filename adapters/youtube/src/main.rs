#![cfg_attr(test, allow(clippy::too_many_lines))]
//! The YouTube adapter: channel pages and the web client's browse interface for lists, the
//! watch page for details. The video files themselves are resolved by Core's media tool.
mod account;
mod api;
mod entry;
mod link;
mod post;
mod rules;
mod site;
#[cfg(test)]
mod site_tests;
mod tabs;
mod watch;

fn main() {
    trace_site_support::kit::serve::run(site::YouTube);
}
