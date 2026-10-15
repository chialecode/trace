//! TRACE 领域类型与规则。
//!
//! 本 crate 不依赖 Tauri、数据库驱动或具体网站实现（见
//! docs/dev-rules/architecture-and-contracts.md）。它定义稳定身份、正交状态、
//! 时间语义、失败分类与版本判定规则；持久化表示由 `trace-core::storage`
//! 映射到 SQLite，跨进程表示由 `trace-contracts` 映射到协议 DTO。
#![cfg_attr(test, allow(clippy::too_many_lines))]

pub mod entities;
pub mod errors;
pub mod ids;
pub mod jobs;
pub mod quality;
pub mod revision;
pub mod search;
pub mod sites;
pub mod states;
pub mod time;

/// M1a 数据库/内容 schema 版本正本。
///
/// 提升版本号必须伴随迁移与兼容证据（docs/design.md 6.2/14.4）。
pub const SCHEMA_VERSION: i64 = 8;

/// 控制协议版本（docs/design/windows-runtime-contracts.md §2）。
pub const CONTROL_PROTOCOL_VERSION: &str = "0.1";

/// 插件协议版本（docs/design.md §8.2，0.x 验证期）。
pub const PLUGIN_PROTOCOL_VERSION: &str = "0.1";

/// 单帧上限（控制与插件协议一致，1 MiB）。
pub const MAX_FRAME_BYTES: u32 = 1024 * 1024;

/// 单页最大条目数（插件协议初值，docs/design.md §8.2）。
pub const MAX_PAGE_ITEMS: u32 = 100;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn protocol_versions_are_stable_in_m1a() {
        assert_eq!(CONTROL_PROTOCOL_VERSION, "0.1");
        assert_eq!(PLUGIN_PROTOCOL_VERSION, "0.1");
        assert_eq!(MAX_FRAME_BYTES, 1024 * 1024);
        assert_eq!(MAX_PAGE_ITEMS, 100);
        const {
            assert!(SCHEMA_VERSION >= 1);
        }
    }
}
