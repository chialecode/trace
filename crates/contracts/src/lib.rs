//! 跨进程契约正本（Rust DTO/schema，docs/design/windows-runtime-contracts.md §2）。
//!
//! 控制协议与插件协议共用 4 字节 little-endian 长度 + UTF-8 JSON 帧，
//! 上限 [`trace_domain::MAX_FRAME_BYTES`]。TypeScript 侧类型由本 crate 导出的
//! JSON Schema 与黄金样本对齐（tests/golden），不得维护第二套业务模型。
#![cfg_attr(test, allow(clippy::too_many_lines))]

pub mod capability;
pub mod catalog;
pub mod content;
pub mod control;
pub mod download;
pub mod entry_meta;
pub mod events;
pub mod explorer;
pub mod library;
pub mod management;
pub mod media_sync;
pub mod network;
pub mod plugin;
pub mod plugin_chunks;
pub mod preview;
pub mod rules;
pub mod unified;
pub mod wire;

/// 供打包/诊断展示的契约版本信息。
pub fn contract_versions() -> serde_json::Value {
    serde_json::json!({
        "control_protocol": trace_domain::CONTROL_PROTOCOL_VERSION,
        "plugin_protocol": trace_domain::PLUGIN_PROTOCOL_VERSION,
        "schema_version": trace_domain::SCHEMA_VERSION,
        "max_frame_bytes": trace_domain::MAX_FRAME_BYTES,
        "max_page_items": trace_domain::MAX_PAGE_ITEMS,
    })
}

#[cfg(test)]
mod tests {
    #[test]
    fn versions_exposed() {
        let v = super::contract_versions();
        assert_eq!(v["control_protocol"], "0.1");
    }
}
pub mod acquisition;
pub mod source;
