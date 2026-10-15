#![cfg_attr(test, allow(clippy::too_many_lines))]
//! The Weibo adapter: the site's web JSON interface mapped to the common work model.
mod account;
mod api;
mod link;
mod media;
mod post;
mod rules;
mod site;
#[cfg(test)]
mod site_tests;

fn main() {
    trace_site_support::kit::serve::run(site::Weibo);
}
