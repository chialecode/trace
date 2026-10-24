//! 时间换算：当前 Unix 秒与 RFC 3339 文本。

pub(crate) fn unix_now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

pub(crate) fn rfc3339(unix: i64) -> String {
    use time::format_description::well_known::Rfc3339;
    time::OffsetDateTime::from_unix_timestamp(unix)
        .map(|t| t.format(&Rfc3339).unwrap_or_default())
        .unwrap_or_default()
}
