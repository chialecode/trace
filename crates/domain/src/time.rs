//! 时间模型（docs/design.md §5.5）。
//!
//! 全部持久化时间为显式 UTC RFC 3339 字符串；发布时间未知时保持为空并
//! 记录精度，不使用抓取时间伪装作者编辑时间。

use serde::{Deserialize, Serialize};
use std::fmt;
use time::OffsetDateTime;
use time::format_description::well_known::Rfc3339;

/// 时间来源精度。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum TimePrecision {
    /// 来源明确给出的时间（作者发布时间等）。
    #[default]
    Source,
    /// 本地发现/保存时间。
    Discovery,
    /// 未知，仅知道存在该事件。
    Unknown,
}

/// 带精度的时间值；`rfc3339` 恒为 UTC（`Z` 结尾）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TraceTime {
    pub rfc3339: String,
    pub precision: TimePrecision,
}

impl TraceTime {
    pub fn now(precision: TimePrecision) -> Self {
        let now = OffsetDateTime::now_utc();
        Self {
            rfc3339: now
                .format(&Rfc3339)
                .unwrap_or_else(|_| "1970-01-01T00:00:00Z".into()),
            precision,
        }
    }

    pub fn from_source(ts: OffsetDateTime) -> Self {
        Self {
            rfc3339: ts
                .format(&Rfc3339)
                .unwrap_or_else(|_| "1970-01-01T00:00:00Z".into()),
            precision: TimePrecision::Source,
        }
    }

    /// 解析持久化字符串；不可信输入返回 None 而不是伪值。
    pub fn parse(value: &str, precision: TimePrecision) -> Option<Self> {
        OffsetDateTime::parse(value, &Rfc3339).ok().map(|_| Self {
            rfc3339: value.to_string(),
            precision,
        })
    }

    /// 排序键：RFC 3339 UTC 字符串按字典序即时间序（固定宽度年份）。
    pub fn ordering_key(&self) -> &str {
        &self.rfc3339
    }
}

impl fmt::Display for TraceTime {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.rfc3339)
    }
}

/// 单调时钟毫秒值，仅用于本进程内的调度等待，不持久化为来源时间。
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub struct MonotonicMs(pub u64);

#[cfg(test)]
mod tests {
    use super::*;
    use time::macros::datetime;

    #[test]
    fn roundtrip_and_ordering() {
        let t = TraceTime::from_source(datetime!(2024-01-02 03:04:05 UTC));
        assert!(t.rfc3339.starts_with("2024-01-02T03:04:05"));
        let parsed = TraceTime::parse(&t.rfc3339, TimePrecision::Source).unwrap();
        assert_eq!(parsed, t);
        assert!(TraceTime::parse("not-a-time", TimePrecision::Unknown).is_none());
        let later = TraceTime::from_source(datetime!(2025-01-02 03:04:05 UTC));
        assert!(later.ordering_key() > t.ordering_key());
    }

    #[test]
    fn unknown_time_is_representable() {
        let t = TraceTime::now(TimePrecision::Unknown);
        assert_eq!(t.precision, TimePrecision::Unknown);
    }
}
