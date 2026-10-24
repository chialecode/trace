//! Timestamps as the sites give them, as RFC 3339 text in UTC.
use time::OffsetDateTime;
use time::format_description::well_known::Rfc3339;

/// Seconds since the epoch. Anything outside 2000..=2100 is a site's placeholder, not a date.
pub fn from_unix(seconds: i64) -> Option<String> {
    if !(946_684_800..=4_102_444_800).contains(&seconds) {
        return None;
    }
    OffsetDateTime::from_unix_timestamp(seconds)
        .ok()?
        .format(&Rfc3339)
        .ok()
}

/// Milliseconds since the epoch.
pub fn from_unix_ms(ms: i64) -> Option<String> {
    from_unix(ms.div_euclid(1000))
}

/// A numeric value that may be seconds or milliseconds: sites mix both.
pub fn from_unix_any(value: i64) -> Option<String> {
    if value > 100_000_000_000 {
        from_unix_ms(value)
    } else {
        from_unix(value)
    }
}

/// `Sat Oct 10 12:00:00 +0800 2026`, the date text of Weibo's JSON.
pub fn parse_ctime(text: &str) -> Option<String> {
    let format = time::format_description::parse_borrowed::<2>(
        "[weekday repr:short] [month repr:short] [day padding:zero] [hour]:[minute]:[second] [offset_hour sign:mandatory][offset_minute] [year]",
    )
    .ok()?;
    let at = OffsetDateTime::parse(text.trim(), &format).ok()?;
    from_unix(at.unix_timestamp())
}

/// ISO 8601 / RFC 3339 text, normalized to UTC.
pub fn parse_iso(text: &str) -> Option<String> {
    let at = OffsetDateTime::parse(text.trim(), &Rfc3339).ok()?;
    from_unix(at.unix_timestamp())
}

/// A calendar date (`2026-10-10`) as midnight UTC: the precision is a day, which callers
/// record as such.
pub fn parse_date(text: &str) -> Option<String> {
    let format = time::macros::format_description!("[year]-[month]-[day]");
    let date = time::Date::parse(text.trim(), &format).ok()?;
    let at = date.with_hms(0, 0, 0).ok()?.assume_utc();
    from_unix(at.unix_timestamp())
}

/// How long ago a list says a work appeared ("3 days ago", "Streamed 2 weeks ago",
/// "1 year ago (edited)", or the compact "9h ago", "12d ago", "1mo ago"), counted from `now`
/// (seconds since the epoch): the moment that many whole units back, and the unit in seconds. The
/// true time lies within one unit of it, so callers record the date as approximate with that
/// margin and never as the publish time.
pub fn parse_ago(text: &str, now: i64) -> Option<(String, i64)> {
    let lower = text.trim().to_ascii_lowercase();
    let lower = lower.strip_suffix(')').map_or(lower.as_str(), |t| {
        t.rsplit_once('(').map_or(t, |(head, _)| head.trim_end())
    });
    let body = lower.strip_suffix(" ago")?;
    let (count, unit) = ago_parts(body)?;
    let count = Some(count).filter(|n| (1..=1000).contains(n))?;
    let unit_seconds: i64 = match unit {
        "s" | "sec" | "second" | "seconds" => 1,
        "m" | "min" | "minute" | "minutes" => 60,
        "h" | "hr" | "hour" | "hours" => 3_600,
        "d" | "day" | "days" => 86_400,
        "w" | "wk" | "week" | "weeks" => 7 * 86_400,
        "mo" | "month" | "months" => 30 * 86_400,
        "y" | "yr" | "year" | "years" => 365 * 86_400,
        _ => return None,
    };
    let at = now.checked_sub(count.checked_mul(unit_seconds)?)?;
    Some((from_unix(at)?, unit_seconds))
}

/// The count and the unit word of an age: "3 days" or the compact "3d", after any label.
fn ago_parts(body: &str) -> Option<(i64, &str)> {
    let mut words = body.rsplit(' ');
    let last = words.next()?;
    let digits = last.bytes().take_while(u8::is_ascii_digit).count();
    if digits > 0 {
        return Some((last[..digits].parse().ok()?, &last[digits..]))
            .filter(|(_, unit)| !unit.is_empty());
    }
    Some((words.next()?.parse().ok()?, last))
}

/// The present, in seconds since the epoch.
pub fn now_unix() -> i64 {
    OffsetDateTime::now_utc().unix_timestamp()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_relative_age_gives_an_approximate_moment_and_its_unit() {
        let now = 1_760_000_000;
        let ago = |t: &str| parse_ago(t, now);
        assert_eq!(
            ago("3 days ago"),
            Some((from_unix(now - 3 * 86_400).unwrap(), 86_400))
        );
        assert_eq!(
            ago("Streamed 2 weeks ago"),
            Some((from_unix(now - 14 * 86_400).unwrap(), 7 * 86_400))
        );
        assert_eq!(
            ago("1 year ago (edited)"),
            Some((from_unix(now - 365 * 86_400).unwrap(), 365 * 86_400))
        );
        assert_eq!(
            ago("5 hours ago").map(|(_, unit)| unit),
            Some(3_600),
            "hours are an hour wide"
        );
        // The compact form the site's list uses.
        for (text, count, unit) in [
            ("9h ago", 9, 3_600),
            ("12d ago", 12, 86_400),
            ("2w ago", 2, 7 * 86_400),
            ("1mo ago", 1, 30 * 86_400),
            ("3y ago", 3, 365 * 86_400),
            ("45m ago", 45, 60),
            ("Streamed 4d ago", 4, 86_400),
        ] {
            assert_eq!(
                ago(text),
                Some((from_unix(now - count * unit).unwrap(), unit)),
                "{text}"
            );
        }
        for text in [
            "",
            "ago",
            "yesterday",
            "3 days",
            "x days ago",
            "0 days ago",
            "3 fortnights ago",
            "d ago",
            "9 ago",
            "12x ago",
            "0d ago",
            "1,234 views",
            "12 Oct 2026",
        ] {
            assert_eq!(parse_ago(text, now), None, "{text}");
        }
    }

    #[test]
    fn epoch_values_in_either_unit_become_utc_text() {
        assert_eq!(
            from_unix(1_760_000_000).as_deref(),
            Some("2025-10-09T08:53:20Z")
        );
        assert_eq!(from_unix_ms(1_760_000_000_123), from_unix(1_760_000_000));
        assert_eq!(
            from_unix_any(1_760_000_000),
            from_unix_any(1_760_000_000_000)
        );
        assert_eq!(from_unix(0), None);
        assert_eq!(from_unix(-5), None);
    }

    #[test]
    fn site_date_texts_are_parsed_and_normalized() {
        assert_eq!(
            parse_ctime("Sat Oct 10 12:00:00 +0800 2026").as_deref(),
            Some("2026-10-10T04:00:00Z")
        );
        assert_eq!(parse_ctime("not a date"), None);
        assert_eq!(
            parse_iso("2026-10-10T12:00:00+08:00").as_deref(),
            Some("2026-10-10T04:00:00Z")
        );
        assert_eq!(
            parse_date("2026-10-10").as_deref(),
            Some("2026-10-10T00:00:00Z")
        );
    }
}
