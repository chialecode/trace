#![cfg_attr(test, allow(clippy::too_many_lines))]
//! The TikTok adapter: the web client's pages with their embedded state, and the list
//! interface that answers only with the site's request signature.
mod account;
mod api;
mod item;
mod link;
mod media;
mod rules;
mod site;
#[cfg(test)]
mod site_tests;

fn main() {
    trace_site_support::kit::serve::run(site::TikTok);
}
