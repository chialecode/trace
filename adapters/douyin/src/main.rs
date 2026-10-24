#![cfg_attr(test, allow(clippy::too_many_lines))]
mod account;
mod api;
mod detail;
mod dispatch;
mod enumeration;
mod live;
mod post;
mod rules;
mod signature;
mod source;
fn main() {
    trace_site_support::runtime::serve(dispatch::dispatch);
}
