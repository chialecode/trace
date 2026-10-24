#![cfg_attr(test, allow(clippy::too_many_lines))]
//! The X adapter: the web client's GraphQL interface. The query ids and feature switches ship
//! as data next to the adapter and are read from the site's own scripts when the site rejects
//! them; calls the site wants signed carry the web client's `x-client-transaction-id`.
mod account;
mod api;
mod catalog;
mod link;
mod media;
mod rules;
mod site;
#[cfg(test)]
mod site_tests;
mod timeline;
mod transaction;
mod tweet;

fn main() {
    trace_site_support::kit::serve::run(site::X);
}
