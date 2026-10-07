//! WBI 请求签名（站点算法版本 wbi-v1；bilibili-capabilities §2"独立版本化
//! 站点算法，有固定向量"）。
//!
//! 规则：参数值剔除 `!'()*`，附加 wts（秒），按键 ASCII 排序，RFC3986 百分号
//! 编码后拼接混淆 key 取 MD5。密钥来自 nav 接口 wbi_img（短期内存缓存）。

use md5::{Digest, Md5};

/// 置换表（社区公开算法 wbi-v1；修改需 bump ALGO_VERSION 并更新固定向量）。
pub const MIXIN_KEY_ENC_TAB: [usize; 64] = [
    46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29,
    28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25,
    54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
];

/// 算法版本标识（进入覆盖证据，便于回溯）。
pub const ALGO_VERSION: &str = "wbi-v1";

/// 从 img_key+sub_key 按置换表导出 32 字符混淆 key。
pub fn mixin_key(img_key: &str, sub_key: &str) -> String {
    let raw: Vec<char> = format!("{img_key}{sub_key}").chars().collect();
    MIXIN_KEY_ENC_TAB
        .iter()
        .filter_map(|&i| raw.get(i))
        .collect()
}

fn filter_value(v: &str) -> String {
    v.chars().filter(|c| !"!'()*".contains(*c)).collect()
}

/// RFC3986 unreserved 之外的百分号编码（与站点 encodeURIComponent 对齐：
/// 未过滤值中不会出现 `!'()*`，故与 `quote(safe="")` 等价）。
fn percent_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(*b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// 签名：返回 (规范化 query string, w_rid)。调用方把两者一起追加到 URL。
pub fn sign(params: &[(String, String)], mixin: &str, wts: u64) -> (String, String) {
    let mut all: Vec<(String, String)> = params
        .iter()
        .map(|(k, v)| (k.clone(), filter_value(v)))
        .collect();
    all.push(("wts".into(), wts.to_string()));
    all.sort_by(|a, b| a.0.cmp(&b.0));
    let qs: String = all
        .iter()
        .map(|(k, v)| format!("{}={}", percent_encode(k), percent_encode(v)))
        .collect::<Vec<_>>()
        .join("&");
    let mut h = Md5::new();
    h.update(format!("{qs}{mixin}").as_bytes());
    let digest = h.finalize();
    let w_rid: String = digest.iter().map(|b| format!("{b:02x}")).collect();
    (qs, w_rid)
}

/// WBI 密钥缓存（30 分钟 TTL；失效刷新一次）。
#[derive(Default)]
pub struct WbiCache {
    pub key: Option<String>,
    pub fetched_at: Option<std::time::Instant>,
}

impl WbiCache {
    pub fn fresh(&self) -> bool {
        match (&self.key, &self.fetched_at) {
            (Some(_), Some(t)) => t.elapsed() < std::time::Duration::from_secs(30 * 60),
            _ => false,
        }
    }

    pub fn update(&mut self, img_key: &str, sub_key: &str) {
        self.key = Some(mixin_key(img_key, sub_key));
        self.fetched_at = Some(std::time::Instant::now());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 固定向量：社区示例密钥导出的混淆 key。
    const IMG: &str = "7cd084941338484aae1ad9425b84077c";
    const SUB: &str = "4932caff0ff746eab6f01bf08b70ac45";
    const MIXIN: &str = "ea1db124af3c7062474693fa704f4ff8ab4a500c8e7ca0784bf98095b48cd341";

    #[test]
    fn mixin_key_fixed_vector() {
        assert_eq!(mixin_key(IMG, SUB), MIXIN);
    }

    #[test]
    fn sign_fixed_vector_basic() {
        let params = vec![
            ("foo".to_string(), "one".to_string()),
            ("bar".to_string(), "two".to_string()),
            ("zab".to_string(), "three".to_string()),
        ];
        let (qs, rid) = sign(&params, MIXIN, 1_702_204_800);
        assert_eq!(qs, "bar=two&foo=one&wts=1702204800&zab=three");
        assert_eq!(rid, "776653b3b52c0c8d2106f418f05c2426");
    }

    #[test]
    fn sign_filters_forbidden_value_chars() {
        // wts 由 sign() 自行附加，不在业务参数内。
        let params = vec![("a".to_string(), "on!e()*'".to_string())];
        let (qs, rid) = sign(&params, MIXIN, 1_702_204_800);
        assert_eq!(qs, "a=one&wts=1702204800");
        assert_eq!(rid, "c95be09151d35615ef16ac92b5cb8abf");
    }

    #[test]
    fn sign_percent_encodes_unicode_and_space() {
        let params = vec![("keyword".to_string(), "中文 搜索".to_string())];
        let (qs, rid) = sign(&params, MIXIN, 1_702_204_800);
        assert_eq!(
            qs,
            "keyword=%E4%B8%AD%E6%96%87%20%E6%90%9C%E7%B4%A2&wts=1702204800"
        );
        assert_eq!(rid, "0b16ba426079e7fa395c202709cdf9f5");
    }

    #[test]
    fn cache_ttl_gates_freshness() {
        let mut c = WbiCache::default();
        assert!(!c.fresh());
        c.update(IMG, SUB);
        assert!(c.fresh());
        c.fetched_at = Some(std::time::Instant::now() - std::time::Duration::from_secs(1801));
        assert!(!c.fresh());
    }
}
