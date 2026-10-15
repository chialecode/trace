//! TRACE 核心：应用服务、DB actor、对象库、调度、IPC 与凭证。
//!
//! 单写者原则：所有 SQLite 写事务经 DB actor 串行（INV-06/09）；
//! GUI 关闭不影响本 crate 的服务生命周期。

pub mod config;
pub mod core;
pub mod db;
pub mod error;
pub mod events;
pub mod ipc;
pub mod jobs;
#[cfg(test)]
mod m1_tests;
pub mod media;
pub mod objects;
pub mod paths;
pub mod pluginhost;
pub mod quality;
pub mod recording;
pub mod request_budget;
pub mod scan_range;
pub mod search;
pub mod site_check;
pub mod storage_ops;
pub mod transport;
pub mod vault;

pub mod accounts;
pub mod acquisitions;
mod archive_export;
pub mod feed;
pub mod upgrades;

pub mod storage_budget;

pub mod issues;
pub mod windows_integration;

pub mod deletion;

pub mod cache;
mod site_context;
pub mod source_acquisition;
