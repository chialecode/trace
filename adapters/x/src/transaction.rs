//! The `x-client-transaction-id` request header of the web client.
//!
//! The web client signs each API call with an id derived from the page it was loaded from: the
//! page carries a verification key and four small animations, and one of the site's scripts
//! says which bytes of the key pick the animation's row and its time. The id is the SHA-256 of
//! the call and the animation's state at that time, wrapped with the key and the clock.
//!
//! The algorithm is the one published under the MIT license by several open-source clients
//! (docs/research/open-source-candidates.md); this is a separate implementation of it, checked
//! against vectors that the reference implementation produced for the synthetic page and script
//! in `fixtures/` (`transaction_vectors.json`).
use base64::Engine as _;
use base64::engine::general_purpose::{STANDARD, STANDARD_NO_PAD};
use sha2::{Digest, Sha256};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::{self, page};

/// The id's clock starts here (seconds since the epoch), which is 2023-04-30 in the site's time.
const EPOCH: i64 = 1_682_924_400;
/// Mixed into the hash and appended to the signed bytes; constants of the web client.
const KEYWORD: &str = "obfiowerehiring";
const TAIL: u8 = 3;
/// The animation's length in milliseconds, which its time is a fraction of.
const TOTAL_TIME: f64 = 4096.0;
/// Animations on the page, the rows of each and the numbers of a row: two colours, a
/// rotation and four curve values.
const FRAMES: usize = 4;
const ROW_NUMBERS: usize = 11;
const ID_PREFIX: &str = "loading-x-anim";
const KEY_MIN: usize = 16;
const KEY_MAX: usize = 256;

/// What the page and the script give: enough to sign any number of calls.
#[derive(Debug, Clone, PartialEq)]
pub(crate) struct Material {
    key: Vec<u8>,
    animation_key: String,
}

fn changed(what: &str) -> PluginError {
    kit::schema(&format!("the transaction id cannot be built: {what}"))
}

/// `(x[12], 16)` as the script writes it: a one-letter name, an index of one or two digits and
/// a radix of 16. The first one names the animation's row, the others the bytes of the time.
fn indices(script: &str) -> Vec<usize> {
    let bytes = script.as_bytes();
    let mut out = Vec::new();
    let mut at = 0;
    while let Some(open) = script[at..].find('(').map(|i| at + i) {
        at = open + 1;
        let rest = &bytes[at..];
        if rest.len() < 6
            || !(rest[0].is_ascii_alphanumeric() || rest[0] == b'_')
            || rest[1] != b'['
        {
            continue;
        }
        let digits = rest[2..].iter().take_while(|b| b.is_ascii_digit()).count();
        if !(1..=2).contains(&digits) || rest.get(2 + digits) != Some(&b']') {
            continue;
        }
        let mut next = 3 + digits;
        if rest.get(next) != Some(&b',') {
            continue;
        }
        next += 1;
        while rest.get(next).is_some_and(u8::is_ascii_whitespace) {
            next += 1;
        }
        if rest.get(next..next + 3) == Some(b"16)") {
            if let Ok(index) = script[at + 2..at + 2 + digits].parse() {
                out.push(index);
            }
            at += next + 3;
        }
    }
    out
}

/// The address of the script that holds the indices (`ondemand.s`), from the chunk tables the
/// page carries: one table names the chunk, another gives its hash.
pub(crate) fn ondemand_url(home: &str) -> Option<String> {
    let number = |from: &str| -> Option<String> {
        let digits: String = from.chars().take_while(char::is_ascii_digit).collect();
        (!digits.is_empty() && digits.len() <= 12).then_some(digits)
    };
    let index = home.match_indices(',').find_map(|(i, _)| {
        let digits = number(&home[i + 1..])?;
        let rest = &home[i + 1 + digits.len()..];
        let named = rest
            .strip_prefix(":\"")
            .or_else(|| rest.strip_prefix(":'"))?;
        named.starts_with("ondemand.s").then_some(digits)
    })?;
    let marker = format!(",{index}:\"");
    let hash = home.match_indices(&marker).find_map(|(i, _)| {
        let rest = &home[i + marker.len()..];
        let hex: String = rest.chars().take_while(char::is_ascii_hexdigit).collect();
        (!hex.is_empty() && hex.len() <= 32 && rest[hex.len()..].starts_with('"')).then_some(hex)
    })?;
    Some(format!(
        "https://abs.twimg.com/responsive-web/client-web/ondemand.s.{hash}a.js"
    ))
}

/// Python's `round(x, 2)`: the nearest value with two decimals, ties to even, of the number as
/// it is stored. Formatting does exactly that.
fn round2(x: f64) -> f64 {
    format!("{x:.2}").parse().unwrap_or(x)
}

fn solve(value: f64, min: f64, max: f64, floor: bool) -> f64 {
    let result = value * (max - min) / 255.0 + min;
    if floor {
        result.floor()
    } else {
        round2(result)
    }
}

fn bezier(a: f64, b: f64, m: f64) -> f64 {
    3.0 * a * (1.0 - m) * (1.0 - m) * m + 3.0 * b * (1.0 - m) * m * m + m * m * m
}

/// The value of the animation's easing curve (a cubic Bézier through `curve`) at `time`.
fn ease(curve: &[f64], time: f64) -> f64 {
    if time <= 0.0 {
        let gradient = if curve[0] > 0.0 {
            curve[1] / curve[0]
        } else if curve[1] == 0.0 && curve[2] > 0.0 {
            curve[3] / curve[2]
        } else {
            0.0
        };
        return gradient * time;
    }
    if time >= 1.0 {
        let gradient = if curve[2] < 1.0 {
            (curve[3] - 1.0) / (curve[2] - 1.0)
        } else if curve[2] == 1.0 && curve[0] < 1.0 {
            (curve[1] - 1.0) / (curve[0] - 1.0)
        } else {
            0.0
        };
        return 1.0 + gradient * (time - 1.0);
    }
    let (mut start, mut end, mut mid) = (0.0_f64, 1.0_f64, 0.0_f64);
    while start < end {
        mid = (start + end) / 2.0;
        let x = bezier(curve[0], curve[2], mid);
        if (time - x).abs() < 0.00001 {
            return bezier(curve[1], curve[3], mid);
        }
        if x < time {
            start = mid;
        } else {
            end = mid;
        }
    }
    bezier(curve[1], curve[3], mid)
}

/// A number in base 16 the way the client writes it, fraction included.
fn hex(mut x: f64) -> String {
    let digit = |n: i64| char::from_digit(n as u32, 16).map_or('0', |c| c.to_ascii_uppercase());
    let mut out: Vec<char> = Vec::new();
    let mut quotient = x as i64;
    let mut fraction = x - quotient as f64;
    while quotient > 0 {
        quotient = (x / 16.0) as i64;
        out.insert(0, digit((x - quotient as f64 * 16.0) as i64));
        x = quotient as f64;
    }
    if fraction == 0.0 {
        return out.into_iter().collect();
    }
    out.push('.');
    while fraction > 0.0 {
        fraction *= 16.0;
        let whole = fraction as i64;
        fraction -= whole as f64;
        out.push(digit(whole));
    }
    out.into_iter().collect()
}

/// The animation's state at `time` (a fraction of its length) as the client writes it: the
/// colour between the two given, and the rotation matrix, in hexadecimal digits.
fn animate(row: &[i64], time: f64) -> Option<String> {
    if row.len() < ROW_NUMBERS {
        return None;
    }
    let number = |i: usize| row[i] as f64;
    let from = [number(0), number(1), number(2), 1.0];
    let to = [number(3), number(4), number(5), 1.0];
    let rotation_to = solve(number(6), 60.0, 360.0, true);
    let curve: Vec<f64> = row[7..]
        .iter()
        .enumerate()
        .map(|(i, v)| solve(*v as f64, if i % 2 == 1 { -1.0 } else { 0.0 }, 1.0, false))
        .collect();
    let at = ease(&curve, time);
    let color: Vec<f64> = (0..4)
        .map(|i| (from[i] * (1.0 - at) + to[i] * at).clamp(0.0, 255.0))
        .collect();
    let rotation = 0.0 * (1.0 - at) + rotation_to * at;
    let radians = rotation * (std::f64::consts::PI / 180.0);
    let matrix = [radians.cos(), -radians.sin(), radians.sin(), radians.cos()];
    let mut out: Vec<String> = color[..3]
        .iter()
        .map(|v| format!("{:x}", v.round_ties_even() as i64))
        .collect();
    for value in matrix {
        let rounded = round2(value);
        let rounded = if rounded < 0.0 { -rounded } else { rounded };
        let text = hex(rounded);
        out.push(if text.starts_with('.') {
            format!("0{text}").to_ascii_lowercase()
        } else if text.is_empty() {
            "0".into()
        } else {
            text
        });
    }
    out.extend(["0".into(), "0".into()]);
    Some(out.concat().replace(['.', '-'], ""))
}

/// The numbers of every row of the animation, from the path the page draws it with: the text
/// after the first nine characters, split at its `C` commands.
fn rows(path: &str) -> Vec<Vec<i64>> {
    path.get(9..)
        .unwrap_or_default()
        .split('C')
        .map(|item| {
            item.split(|c: char| !c.is_ascii_digit())
                .filter(|n| !n.is_empty())
                .filter_map(|n| n.parse().ok())
                .collect()
        })
        .collect()
}

impl Material {
    /// Reads the home page (its verification key and animations) and the script that says which
    /// bytes of the key to use.
    pub(crate) fn read(home: &str, script: &str) -> Result<Material, PluginError> {
        let encoded = page::meta_content(home, "twitter-site-verification")
            .ok_or_else(|| changed("the page has no verification key"))?;
        let key = STANDARD
            .decode(encoded.trim())
            .ok()
            .filter(|k| (KEY_MIN..=KEY_MAX).contains(&k.len()))
            .ok_or_else(|| changed("the verification key is not readable"))?;
        let found = indices(script);
        let (row_at, time_at) = found
            .split_first()
            .filter(|(_, rest)| !rest.is_empty())
            .ok_or_else(|| changed("the script names no key bytes"))?;
        let byte = |i: usize| {
            key.get(i)
                .map(|b| usize::from(*b))
                .ok_or_else(|| changed("the script names a byte the key does not have"))
        };
        let row_index = byte(*row_at)? % 16;
        let mut product: u64 = 1;
        for i in time_at {
            product = product.saturating_mul(byte(*i)? as u64 % 16);
        }
        // The clock is rounded to tens the way the client's `Math.round` does.
        let frame_time = ((product as f64 / 10.0 + 0.5).floor() * 10.0) / TOTAL_TIME;
        let frames = page::nested_attrs(home, ID_PREFIX, "d");
        let path = frames
            .get(byte(5)? % FRAMES)
            .and_then(|children| children.get(1))
            .and_then(Option::as_deref)
            .ok_or_else(|| changed("the page has no animation to read"))?;
        let rows = rows(path);
        let animation_key = rows
            .get(row_index)
            .and_then(|row| animate(row, frame_time))
            .ok_or_else(|| changed("the animation has no such row"))?;
        Ok(Material { key, animation_key })
    }

    /// The header value for one call: `method` upper case, `path` without its query, `now` in
    /// seconds since the epoch, and one byte of noise that masks the rest.
    pub(crate) fn header(&self, method: &str, path: &str, now: i64, noise: u8) -> String {
        let clock = u32::try_from(now - EPOCH).unwrap_or(0);
        let hash = Sha256::digest(
            format!("{method}!{path}!{clock}{KEYWORD}{}", self.animation_key).as_bytes(),
        );
        let signed = self
            .key
            .iter()
            .copied()
            .chain(clock.to_le_bytes())
            .chain(hash.iter().copied().take(16))
            .chain([TAIL]);
        let bytes: Vec<u8> = std::iter::once(noise)
            .chain(signed.map(|b| b ^ noise))
            .collect();
        STANDARD_NO_PAD.encode(bytes)
    }

    /// The header value for a call made now.
    pub(crate) fn sign(&self, method: &str, path: &str) -> String {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default();
        // The noise only has to differ between calls, and the clock's last digits do.
        let noise = (now.subsec_nanos() % 256) as u8;
        self.header(method, path, now.as_secs() as i64, noise)
    }

    #[cfg(test)]
    pub(crate) fn animation_key(&self) -> &str {
        &self.animation_key
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;

    const HOME: &str = include_str!("../fixtures/home_transaction.html");
    const SCRIPT: &str = include_str!("../fixtures/ondemand.js");
    const VECTORS: &str = include_str!("../fixtures/transaction_vectors.json");

    fn vectors() -> Value {
        serde_json::from_str(VECTORS).unwrap()
    }

    #[test]
    fn the_animation_key_and_the_ids_match_what_the_reference_implementation_produced() {
        let v = vectors();
        let material = Material::read(HOME, SCRIPT).unwrap();
        assert_eq!(
            material.animation_key(),
            v["animation_key"].as_str().unwrap()
        );
        for case in v["ids"].as_array().unwrap() {
            assert_eq!(
                material.header(
                    case["method"].as_str().unwrap(),
                    case["path"].as_str().unwrap(),
                    case["now"].as_i64().unwrap(),
                    case["salt"].as_u64().unwrap() as u8,
                ),
                case["id"].as_str().unwrap(),
                "{case}"
            );
        }
    }

    #[test]
    fn other_keys_pick_other_rows_and_frames_and_still_match_the_reference() {
        let v = vectors();
        for case in v["other_keys"].as_array().unwrap() {
            let key = case["key"].as_str().unwrap();
            let home = HOME.replacen(v_key(), key, 1);
            let material = Material::read(&home, SCRIPT).unwrap();
            assert_eq!(
                material.animation_key(),
                case["animation_key"].as_str().unwrap(),
                "row {} frame {}",
                case["row"],
                case["frame"]
            );
        }
    }

    fn v_key() -> &'static str {
        let start = HOME.find("content=\"").unwrap() + 9;
        let end = start + HOME[start..].find('"').unwrap();
        &HOME[start..end]
    }

    #[test]
    fn the_script_is_read_for_the_indices_and_the_page_for_the_script() {
        assert_eq!(indices(SCRIPT), [2, 12, 14, 7, 31]);
        assert_eq!(
            indices("(a[1], 16)(b[22],16) (c[333], 16) (d[4], 10) (e[5],\t16)(ab[6], 16)"),
            [1, 22, 5],
            "one letter, one or two digits, a radix of 16"
        );
        assert_eq!(
            ondemand_url(HOME).as_deref(),
            Some("https://abs.twimg.com/responsive-web/client-web/ondemand.s.0123abcdefa.js")
        );
        assert_eq!(ondemand_url("no chunks here"), None);
        assert_eq!(
            ondemand_url(r#"{1:"ondemand.s"}"#),
            None,
            "the table entry needs the comma before it and a hash elsewhere"
        );
    }

    #[test]
    fn a_page_or_script_that_changed_is_a_changed_interface_not_a_wrong_id() {
        let bad = |home: &str, script: &str| Material::read(home, script).unwrap_err().class;
        let schema = trace_domain::errors::FailureClass::SchemaChanged;
        assert_eq!(bad("<html></html>", SCRIPT), schema);
        assert_eq!(bad(HOME, "nothing to see"), schema);
        assert_eq!(bad(HOME, "(a[2], 16)"), schema, "no time bytes");
        assert_eq!(
            bad(HOME, "(a[2], 16)(b[99], 16)"),
            schema,
            "a byte beyond the key"
        );
        let short = HOME.replacen(v_key(), "AAAA", 1);
        assert_eq!(bad(&short, SCRIPT), schema);
        let no_frames = HOME.replace("loading-x-anim", "other");
        assert_eq!(bad(&no_frames, SCRIPT), schema);
    }

    #[test]
    fn numbers_are_written_the_way_the_client_writes_them() {
        assert_eq!(hex(0.0), "");
        assert_eq!(hex(0.5), ".8");
        assert_eq!(hex(10.0), "A");
        assert_eq!(hex(255.0), "FF");
        assert_eq!(hex(1.0), "1");
        assert_eq!(hex(0.87), ".DEB851EB851EB8");
        assert_eq!(round2(0.125), 0.12, "ties go to the even digit");
        assert_eq!(round2(-0.874), -0.87);
        assert_eq!(solve(255.0, 60.0, 360.0, true), 360.0);
        assert_eq!(solve(128.0, 0.0, 1.0, false), 0.5);
    }

    #[test]
    fn the_clock_and_the_noise_are_in_the_id_and_only_the_noise_is_not_signed() {
        let material = Material::read(HOME, SCRIPT).unwrap();
        let a = material.header("GET", "/x", EPOCH + 10, 0x11);
        let b = material.header("GET", "/x", EPOCH + 10, 0x22);
        let c = material.header("GET", "/x", EPOCH + 11, 0x11);
        assert_ne!(a, b);
        assert_ne!(a, c);
        let decode = |id: &str| STANDARD_NO_PAD.decode(id).unwrap();
        let (da, db) = (decode(&a), decode(&b));
        assert_eq!((da[0], db[0]), (0x11, 0x22));
        let unmasked = |d: &[u8]| d[1..].iter().map(|x| x ^ d[0]).collect::<Vec<u8>>();
        assert_eq!(
            unmasked(&da),
            unmasked(&db),
            "the same call, whatever the noise"
        );
        assert_eq!(da.len(), 1 + 70 + 4 + 16 + 1);
        assert_eq!(*unmasked(&da).last().unwrap(), TAIL);
    }
}
