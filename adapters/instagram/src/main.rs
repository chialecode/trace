#![cfg_attr(test, allow(clippy::too_many_lines))]
//! The Instagram adapter: the web client's own path. A profile page gives the page tokens and
//! the profile id; the profile and its posts come from the GraphQL endpoints with the
//! operation ids the web client's scripts publish.
mod account;
mod api;
mod feed;
mod link;
mod media;
mod ops;
mod post;
mod rules;
mod site;
#[cfg(test)]
mod site_tests;
mod web;

fn main() {
    trace_site_support::kit::serve::run(site::Instagram);
}
