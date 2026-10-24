#![cfg_attr(test, allow(clippy::too_many_lines))]
//! The Xiaohongshu adapter: the pages' embedded state and the signed profile list. The list
//! signature needs the device identifier `a1` that the host passes as access context; the
//! login Cookie and `web_session` stay in Core (DEC-041).
mod account;
mod api;
mod link;
mod media;
mod memo;
mod note;
mod rules;
mod sign;
mod site;
#[cfg(test)]
mod site_tests;

fn main() {
    trace_site_support::kit::serve::run(site::Xiaohongshu);
}
