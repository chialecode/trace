//! 事件契约：持久变更事件（可重放）与瞬时进度事件（可合并丢弃）。
//!
//! 持久事件的 `seq` 对应核心 Outbox 序号（单调递增，跨语言字符串传输）；
//! 客户端消费落后时必须收到 `resync_required` 并重新拉快照（C24）。

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChangeEvent {
    /// Outbox 序号（字符串传输避免 JS 精度丢失）。
    pub seq: String,
    pub topic: String,
    pub aggregate_id: String,
    /// 事件载荷（白名单字段，不含敏感值）。
    pub payload: serde_json::Value,
    pub occurred_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProgressEvent {
    pub job_id: String,
    pub progress: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
    /// 瞬时事件合并键（同键旧事件可丢弃）。
    pub merge_key: String,
}

/// 服务端 → 客户端事件帧。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "event", rename_all = "snake_case")]
pub enum EventFrame {
    Change(ChangeEvent),
    Progress(ProgressEvent),
    ResyncRequired {
        #[serde(default)]
        reason: String,
    },
    Ping {
        seq: String,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn seq_is_string_for_js_precision() {
        let e = ChangeEvent {
            seq: "9007199254740993".into(),
            topic: "entry.revision".into(),
            aggregate_id: "e1".into(),
            payload: serde_json::json!({}),
            occurred_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&e).unwrap();
        assert!(json.contains("\"seq\":\"9007199254740993\""));
    }
}
