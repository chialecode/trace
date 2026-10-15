//! The request signature Xiaohongshu's public list API asks for.
//!
//! The site's web client signs each JSON request with the device identifier `a1` that the
//! visitor's own browser carries. The identifier reaches this module as host context; the
//! login Cookie and `web_session` never do (DEC-041). The signature is a fixed construction
//! (a digest, a small binary payload and a custom base64 alphabet), not a secret of this
//! project; the vectors below pin the bytes so a change shows up in a test.
use md5::{Digest, Md5};

const CUSTOM_ALPHABET: &[u8; 64] =
    b"ZmserbBoHQtNP+wOcza/LpngG8yJq42KWYj0DSfdikx3VT16IlUAFM97hECvuRX5";
const X3_ALPHABET: &[u8; 64] = b"MfgqrsbcyzPQRStuvC7mn501HIJBo2DEFTKdeNOwxWXYZap89+/A4UVLhijkl63G";
const HEX_KEY: &str = "71a302257793271ddd273bcee3e4b98d9d7935e1da33f5765e2ea8afb6dc77a51a499d23b67c20660025860cbf13d4540d92497f58686c574e508f46e1956344f39139bf4faf22a3eef120b79258145b2feb5193b6478669961298e79bedca646e1a693a926154a5a7a1bd1cf0dedb742f917a747a1e388b234f2277";
const VERSION_BYTES: [u8; 4] = [119, 104, 96, 41];
const CHECKSUM_FIXED_TAIL: [u8; 14] = [
    249, 65, 103, 103, 201, 181, 131, 99, 94, 7, 68, 250, 132, 21,
];
const B1_SECRET_KEY: &[u8] = b"xhswebmplfbt";
const X3_PREFIX: &str = "mns0301_";
const XYS_PREFIX: &str = "XYS_";

/// The four random values one signature needs; each stays in the range the site's own client
/// uses, so a fixed vector can pin the output.
#[derive(Debug, Clone, Copy)]
pub struct Parts {
    pub seed: u32,
    pub offset: u32,
    pub sequence: u32,
    pub window: u32,
}

impl Default for Parts {
    fn default() -> Self {
        use rand::RngCore;
        let mut rng = rand::rng();
        let next = &mut rng;
        Self {
            seed: next.next_u32(),
            offset: 10 + next.next_u32() % 41,
            sequence: 15 + next.next_u32() % 36,
            window: 900 + next.next_u32() % 301,
        }
    }
}

/// One request's signature headers.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Signature {
    pub x_s: String,
    pub x_t: String,
    pub x_s_common: String,
    pub b3_trace_id: String,
    pub xray_trace_id: String,
}

fn base64_with(data: &[u8], alphabet: &[u8; 64]) -> String {
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b0 = u32::from(chunk[0]);
        let b1 = u32::from(chunk.get(1).copied().unwrap_or(0));
        let b2 = u32::from(chunk.get(2).copied().unwrap_or(0));
        let n = (b0 << 16) | (b1 << 8) | b2;
        out.push(alphabet[(n >> 18) as usize & 63] as char);
        out.push(alphabet[(n >> 12) as usize & 63] as char);
        if chunk.len() > 1 {
            out.push(alphabet[(n >> 6) as usize & 63] as char);
        } else {
            out.push('=');
        }
        if chunk.len() > 2 {
            out.push(alphabet[n as usize & 63] as char);
        } else {
            out.push('=');
        }
    }
    out
}

fn custom_base64(data: &[u8]) -> String {
    base64_with(data, CUSTOM_ALPHABET)
}

fn x3_base64(data: &[u8]) -> String {
    base64_with(data, X3_ALPHABET)
}

fn hex_key() -> Vec<u8> {
    HEX_KEY
        .as_bytes()
        .as_chunks::<2>()
        .0
        .iter()
        .filter_map(|pair| {
            std::str::from_utf8(pair)
                .ok()
                .and_then(|s| u8::from_str_radix(s, 16).ok())
        })
        .collect()
}

fn xor_key(payload: &mut [u8]) {
    let key = hex_key();
    for (byte, mask) in payload.iter_mut().zip(key) {
        *byte ^= mask;
    }
}

fn environment_a(timestamp: u64) -> [u8; 8] {
    let mut raw = timestamp.to_le_bytes();
    let sum1: u32 = raw[1..5].iter().map(|b| u32::from(*b)).sum();
    let sum2: u32 = raw[5..8].iter().map(|b| u32::from(*b)).sum();
    raw[0] = ((sum1 & 0xff) + sum2) as u8;
    for byte in &mut raw {
        *byte ^= 41;
    }
    raw
}

fn signature_payload(content: &str, a1: &str, parts: Parts, timestamp: u64) -> Vec<u8> {
    let digest = Md5::digest(content.as_bytes());
    let mut payload = Vec::with_capacity(160);
    payload.extend_from_slice(&VERSION_BYTES);
    let seed = parts.seed.to_le_bytes();
    payload.extend_from_slice(&seed);
    let seed_byte = seed[0];
    payload.extend_from_slice(&environment_a(timestamp));
    payload.extend_from_slice(&(timestamp - u64::from(parts.offset)).to_le_bytes());
    payload.extend_from_slice(&parts.sequence.to_le_bytes());
    payload.extend_from_slice(&parts.window.to_le_bytes());
    payload.extend_from_slice(&(content.encode_utf16().count() as u32).to_le_bytes());
    for byte in &digest[..8] {
        payload.push(byte ^ seed_byte);
    }
    payload.push(52);
    let mut a1_padded = [0u8; 52];
    let a1_bytes = a1.as_bytes();
    let take = a1_bytes.len().min(52);
    a1_padded[..take].copy_from_slice(&a1_bytes[..take]);
    payload.extend_from_slice(&a1_padded);
    payload.push(10);
    let mut app = [0u8; 10];
    let app_bytes = b"xhs-pc-web";
    app[..app_bytes.len()].copy_from_slice(app_bytes);
    payload.extend_from_slice(&app);
    payload.push(1);
    payload.push(1);
    payload.push(seed_byte ^ 115);
    payload.extend_from_slice(&CHECKSUM_FIXED_TAIL);
    payload
}

fn query_value(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        let keep = byte.is_ascii_alphanumeric()
            || matches!(
                byte,
                b'-' | b'_' | b'.' | b'!' | b'~' | b'*' | b'\'' | b'(' | b')' | b','
            );
        if keep {
            out.push(byte as char);
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

/// The string the site's client hashes for a GET: the path with its sorted query parameters.
fn content_for(path: &str, params: &[(&str, &str)]) -> String {
    let query = query_string(params);
    if query.is_empty() {
        path.to_owned()
    } else {
        format!("{path}?{query}")
    }
}

/// The query in the same order and encoding the signature covers, so the address that is
/// requested and the address that is signed are the same string.
pub(crate) fn query_string(params: &[(&str, &str)]) -> String {
    let mut pairs: Vec<_> = params.to_vec();
    pairs.sort_by(|a, b| a.0.cmp(b.0));
    pairs
        .into_iter()
        .map(|(key, value)| format!("{key}={}", query_value(value)))
        .collect::<Vec<_>>()
        .join("&")
}

fn x_s(content: &str, a1: &str, parts: Parts, timestamp: u64) -> String {
    let mut payload = signature_payload(content, a1, parts, timestamp);
    xor_key(&mut payload);
    payload.truncate(124);
    let x3 = format!("{X3_PREFIX}{}", x3_base64(&payload));
    let json = format!(r#"{{"x0":"4.2.6","x1":"xhs-pc-web","x2":"Windows","x3":"{x3}","x4":""}}"#);
    format!("{XYS_PREFIX}{}", custom_base64(json.as_bytes()))
}

fn rc4(key: &[u8], data: &[u8]) -> Vec<u8> {
    let mut s: [u8; 256] = [0; 256];
    for (index, value) in s.iter_mut().enumerate() {
        *value = index as u8;
    }
    let mut j = 0u8;
    for i in 0..256 {
        j = j.wrapping_add(s[i]).wrapping_add(key[i % key.len()]);
        s.swap(i, usize::from(j));
    }
    let (mut i, mut j) = (0u8, 0u8);
    let mut out = Vec::with_capacity(data.len());
    for byte in data {
        i = i.wrapping_add(1);
        j = j.wrapping_add(s[usize::from(i)]);
        s.swap(usize::from(i), usize::from(j));
        let mixed = s[usize::from(i)].wrapping_add(s[usize::from(j)]);
        out.push(byte ^ s[usize::from(mixed)]);
    }
    out
}

fn crc32_js(data: &[u8]) -> i32 {
    let mut crc = 0xffff_ffffu32;
    for byte in data {
        crc ^= u32::from(*byte);
        for _ in 0..8 {
            crc = (crc >> 1) ^ (0xedb8_8320 & 0u32.wrapping_sub(crc & 1));
        }
    }
    let c = crc ^ 0xffff_ffff;
    (0xffff_ffff ^ c ^ 0xedb8_8320) as i32
}

/// The fingerprint block the site's client sends with its list requests. The values are the
/// browser shape the site accepts; the timestamp and canvas hash are fixed data.
fn b1(sequence: u32, timestamp: u64) -> String {
    let payload = format!(
        concat!(
            r#"{{"x33":"0","x34":"0","x35":"0","x36":"{}","#,
            r#""x37":"0|0|0|0|0|0|0|0|0|1|0|0|0|0|0|0|0|0|1|0|0|0|0|0","#,
            r#""x38":"0|0|1|0|1|0|0|0|0|0|1|0|1|0|1|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0","#,
            r#""x39":0,"x42":"3.4.4","x43":"742cc32c","x44":"{}","#,
            r#""x45":"__SEC_CAV__1-1-1-1-1|__SEC_WSA__|","x46":"false","x48":"","#,
            r#""x49":"{{list:[],type:}}","x50":"","x51":"","x52":"","x82":"_0x17a2|_0x1954"}}"#
        ),
        sequence, timestamp
    );
    custom_base64(&rc4(B1_SECRET_KEY, payload.as_bytes()))
}

fn x_s_common(a1: &str, sequence: u32, timestamp: u64) -> String {
    let x8 = b1(sequence, timestamp);
    let x9 = crc32_js(x8.as_bytes());
    let json = format!(
        r#"{{"s0":5,"s1":"","x0":"1","x1":"4.2.6","x2":"Windows","x3":"xhs-pc-web","x4":"4.86.0","x5":"{a1}","x6":"","x7":"","x8":"{x8}","x9":{x9},"x10":0,"x11":"normal"}}"#
    );
    custom_base64(json.as_bytes())
}

fn trace_ids(timestamp: u64, sequence: u32) -> (String, String) {
    use rand::RngCore;
    let mut rng = rand::rng();
    let mut random = |bytes: usize| {
        (0..bytes)
            .map(|_| format!("{:02x}", (rng.next_u32() & 0xff) as u8))
            .collect::<String>()
    };
    let b3 = random(8);
    let part1 = (timestamp << 23) | u64::from(sequence) & 0x7f_ffff;
    let xray = format!("{part1:016x}{}", random(8));
    (b3, xray)
}

/// Signs one list request. `params` are the same pairs that go into the query string; the
/// device identifier is the host context value, never a Cookie.
pub fn headers(a1: &str, path: &str, params: &[(&str, &str)]) -> Signature {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0);
    let parts = Parts::default();
    let content = content_for(path, params);
    let (b3_trace_id, xray_trace_id) = trace_ids(now, parts.sequence);
    Signature {
        x_s: x_s(&content, a1, parts, now),
        x_t: now.to_string(),
        x_s_common: x_s_common(a1, parts.sequence % 20 + 1, now),
        b3_trace_id,
        xray_trace_id,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const A1: &str = "19f0e1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
    const USER: &str = "5a1b2c3d4e5f60718293a4b5";
    const XS: &str = "XYS_2UQhPsHCH0c1Pjh9HjIj2erjwjQhyoPTqBPT49pjHjIj2eHjwjQgynEDJ74AHjIj2ePjwjQTJdPIPAZlg98yGLTlGFzT2fSPGFzeJnl1LjR9p7pV4bQopFMawepnzaRx2bSxcLWUy0pe+FDF8oYG478czDk8+7LILgSI+sRMz/QiGAmhL/bmL9k/cdYBpniMJ0Q6qBz+2/SI/AzdPBz6PdQB4/QBJFREzp8O4r8B4MZEPpYi+FRFzrQ3JM4ScSz+c9EIqMQCLDkcpnbLP9IUP7YDPBTnGFQP4gqMwepC//YHJeDROaHVHdWFH0ijHdF=";
    const COMMON: &str = "2UQAPsHC+aIjqArjwjHjNsQhPsHCH0rjNsQhPaHCH0c1Pjh9HjIj2eHjwjQgynEDJ74AHjIj2ePjwjQhyoPTqBPT49pjHjIj2ecjwjHFN0W9N0ZjNsQh+aHCH0rE80mSPnHUGA+D+BLM80GI+ArhP0DAG/zj+nP98e4SwBGEPerUPAcM+0qhHjIj2eGjwjHjNsQh+UHCHjHVHdWhH0ijprz0y0zonLMI+gPU/pW3JFM0qD4Sno8G/B4BpoY1pdmG2jTyc9cILAYSt9RdGnHhPpmtJDVEy9lb+ppywBpgyBk/an4t+7pr2p8xt7ki8BEE+Fzy4gS8NFYfpFRc4LTC/0YkPFk1wpH3aUT1+rI9+SkY4LQdzDk/z/HUaoqUNAz6t9r7nd+xnaTN8AcMNAz+N7Q0q9z8PAYnzLhl//4sqomeyMQjafSjGMYnpFSftMmx878mpfMiqLHUJ989/7SUzDG7G7mrLnTp87k8qfRswrbQaBF7/rzrcfR1aBMP2nzDy7cUwLTB87Z6JnihzdSa2rk/4MkGygpdqpbA/p+Mz/WEyfYdcdrUqrpP2rHl8b4wJ9TepLS140pN874ELbqM8aT9+Lls20PMarVE2gzg/bY3/n+zpDVM+nQ/8gqF+g8IwnE0/L+zqSYfzeHEqDi3+rIFzDQY/SSPprhI+LEg+fT/aL8rwoi3pfE+zpYbJ7QbzeY1PFzm8/qhJ7Yg2eLM8BVEJo4aGAQk4ezD4DI3/grILe4yPLQxan+hzMpl+oSUL9QTabbTPMmL/nH3zAzhL7qUyL+dqn+GqoqI/BQYLoW7c04fzBplcUThppq9q/8S4FTUPS8w+gWA+dptcSSlcpiE+p8P+SPhprSfLMGlPd4GLAL6/A+f4UTEzrlSzDpEa/FRHjIj2eDjw0r9P/PF+0ch+/ZVHdWlPsHCPsIj2erlH0ijJfRUJnbVHdF=";

    #[test]
    fn a_list_request_matches_the_fixed_vector() {
        let content = content_for(
            "/api/sns/web/v1/user_posted",
            &[("num", "30"), ("cursor", ""), ("user_id", USER)],
        );
        assert_eq!(
            content,
            "/api/sns/web/v1/user_posted?cursor=&num=30&user_id=5a1b2c3d4e5f60718293a4b5"
        );
        let parts = Parts {
            seed: 2_147_483_648,
            offset: 30,
            sequence: 33,
            window: 1050,
        };
        assert_eq!(x_s(&content, A1, parts, 1_761_390_000_000), XS);
    }

    #[test]
    fn the_fingerprint_block_matches_the_fixed_vector() {
        assert_eq!(x_s_common(A1, 10, 1_761_390_000_000), COMMON);
    }

    #[test]
    fn the_query_is_sorted_and_percent_encoded_like_the_browser() {
        assert_eq!(
            content_for("/p", &[("b", "a b"), ("a", "中,文")]),
            "/p?a=%E4%B8%AD,%E6%96%87&b=a%20b"
        );
    }
}
