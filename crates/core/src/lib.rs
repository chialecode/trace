//! TRACE 核心：应用服务、DB actor、对象库、调度、IPC 与凭证。
//!
//! 单写者原则：所有 SQLite 写事务经 DB actor 串行（INV-06/09）；
//! GUI 关闭不影响本 crate 的服务生命周期。
#![cfg_attr(test, allow(clippy::too_many_lines))]

pub mod app;
#[cfg(test)]
mod archive_catalog_tests;
mod asset_receipt;
mod auth_proofs;
pub mod catalog;
#[cfg(test)]
mod catalog_tests;
pub mod config;
pub mod core;
pub mod db;
#[cfg(test)]
mod delivery_tests;
pub mod downloads;
pub mod error;
pub mod events;
pub mod explorer;
pub mod ipc;
pub mod jobs;
mod legacy_metadata;
pub mod library;
#[cfg(test)]
mod management_tests;
pub mod media;
#[cfg(test)]
mod media_sync_tests;
pub mod media_tools;
pub mod network;
#[cfg(test)]
mod network_tests;
pub mod objects;
mod pacing;
pub mod paths;
mod pipeline;
pub mod plugin_install;
#[cfg(test)]
mod plugin_tests;
pub mod pluginhost;
pub mod quality;
pub mod recording;
#[cfg(test)]
mod replay_tests;
pub mod replays;
pub mod request_budget;
#[cfg(test)]
mod resource_tests;
pub mod resources;
pub mod scan_range;
pub mod search;
pub mod site_check;
pub mod storage_ops;
pub mod store;
#[cfg(test)]
mod timeline_sort_tests;
mod tool_update;
pub mod transport;
#[cfg(test)]
mod unified_metadata_tests;
pub mod vault;

pub mod accounts;
pub mod acquisitions;
mod archive_export;
pub mod feed;
pub mod identity;
#[cfg(test)]
mod identity_tests;
#[cfg(test)]
mod import_tests;
pub mod imports;
pub mod upgrades;

pub mod storage_budget;

pub mod issues;
pub mod windows_integration;

pub mod deletion;

pub mod cache;
pub mod profile_preview;
pub mod session_jar;
mod site_context;
pub mod source_acquisition;
pub mod source_offers;
