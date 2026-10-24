//! Offline document DTO and bounded metadata boundary. Never contains fetch plans.
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Document {
    pub version: u32,
    pub blocks: Vec<Block>,
    pub omissions: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Block {
    RichText {
        spans: Vec<TextSpan>,
        style: String,
        depth: usize,
        ordinal: Option<u64>,
    },
    Text {
        text: String,
        style: String,
    },
    Resource {
        attachment_id: String,
        caption: String,
    },
    Link {
        text: String,
        url: String,
    },
    ListItem {
        text: String,
        depth: usize,
        ordinal: Option<u64>,
    },
    Table {
        rows: Vec<Vec<String>>,
        header: bool,
    },
}

/// Inert inline formatting; no CSS, markup, event handlers or remote targets.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct TextSpan {
    pub text: String,
    pub bold: bool,
    pub italic: bool,
    pub strike: bool,
}

impl Document {
    /// A compromised or older adapter cannot make an offline reader fetch a
    /// signed URL. Resource references must belong to the returned manifest.
    pub fn validate(
        &mut self,
        resources: &[crate::plugin::ResourceIdentityDto],
    ) -> Result<(), &'static str> {
        if self.blocks.len() > 8192
            || serde_json::to_vec(self)
                .map_err(|_| "document serialization")?
                .len()
                > 512 * 1024
        {
            return Err("document budget exceeded");
        }
        for block in &mut self.blocks {
            match block {
                Block::Resource { attachment_id, .. } => {
                    if !resources
                        .iter()
                        .any(|r| &r.source_attachment_id == attachment_id)
                    {
                        return Err("document references undeclared resource");
                    }
                }
                Block::Link { text, .. } => {
                    // Outbound navigation is a separate explicit source action.
                    *block = Block::Text {
                        text: text.clone(),
                        style: "paragraph".into(),
                    };
                    self.omissions.push("outbound_link_target_omitted".into());
                }
                _ => {}
            }
        }
        self.omissions.truncate(64);
        Ok(())
    }
}

/// Fail closed on unknown URL-bearing extensions and session/device fields.
pub fn safe_metadata(input: &Value) -> Value {
    type Omissions = std::collections::BTreeMap<&'static str, u64>;
    fn note(omissions: &mut Omissions, reason: &'static str, count: u64) {
        *omissions.entry(reason).or_default() += count;
    }
    fn visit(
        v: &Value,
        depth: usize,
        remaining: &mut usize,
        omissions: &mut Omissions,
    ) -> Option<Value> {
        if depth > 12 || *remaining == 0 {
            note(omissions, "depth_or_node_budget", 1);
            return None;
        }
        *remaining -= 1;
        match v {
            Value::Object(o) => {
                let mut out = Map::new();
                for (k, v) in o {
                    let key = k.to_ascii_lowercase();
                    if k.len() > 128
                        || [
                            "cookie",
                            "token",
                            "secret",
                            "signature",
                            "bogus",
                            "authorization",
                            "device",
                            "fingerprint",
                            "sec_user",
                            "sec_uid",
                            "session",
                            "ticket",
                            "password",
                            "headers",
                            "url",
                            "uri",
                            "addr",
                            "logid",
                            "fe_data",
                            "lynx",
                        ]
                        .iter()
                        .any(|s| key.contains(s))
                    {
                        note(omissions, "restricted_field", 1);
                        continue;
                    }
                    if let Some(value) = visit(v, depth + 1, remaining, omissions) {
                        let value = if (key == "id" || key.ends_with("_id")) && value.is_number() {
                            Value::String(value.to_string())
                        } else {
                            value
                        };
                        out.insert(k.clone(), value);
                    }
                }
                Some(Value::Object(out))
            }
            Value::Array(a) => {
                if a.len() > 256 {
                    note(omissions, "array_item_budget", (a.len() - 256) as u64);
                }
                Some(Value::Array(
                    a.iter()
                        .take(256)
                        .filter_map(|v| visit(v, depth + 1, remaining, omissions))
                        .collect(),
                ))
            }
            Value::String(s) => {
                if s.len() > 4096
                    || s.contains("://")
                    || s.contains("Bearer ")
                    || s.contains("Cookie:")
                    || s.contains("sessionid=")
                    || s.contains("a_bogus=")
                {
                    note(omissions, "text_budget_or_restricted_value", 1);
                    return None;
                }
                if s.starts_with('{') || s.starts_with('[') {
                    note(omissions, "encoded_extension", 1);
                    return None;
                }
                Some(v.clone())
            }
            Value::Number(n)
                if n.as_u64().is_some_and(|n| n > 9_007_199_254_740_991)
                    || n.as_i64().is_some_and(|n| n < -9_007_199_254_740_991) =>
            {
                Some(Value::String(n.to_string()))
            }
            _ => Some(v.clone()),
        }
    }
    let mut omissions = Omissions::new();
    let mut cleaned = visit(input, 0, &mut 4096, &mut omissions).unwrap_or(Value::Null);
    if !omissions.is_empty()
        && let Value::Object(o) = &mut cleaned
    {
        // Only fixed reason labels and counts survive; source keys and values
        // cannot turn the omission explanation into a second leakage channel.
        o.insert("_trace_omissions".into(), serde_json::json!(omissions));
    }
    if cleaned.to_string().len() > 128 * 1024 {
        serde_json::json!({"omitted":"metadata_byte_budget"})
    } else {
        cleaned
    }
}

/// Source claims rank acquisition offers only; they are never proof of saved
/// bytes, duration, track completeness or safe replacement.
pub fn source_rank(v: &serde_json::Value, prefer_fps: bool) -> Option<(i64, i64)> {
    let pixels = v["width"].as_i64()?.checked_mul(v["height"].as_i64()?)?;
    if pixels <= 0 {
        return None;
    }
    let fps = source_fps(v).unwrap_or(0.0);
    if prefer_fps && fps <= 0.0 {
        return None;
    }
    let fps = (fps * 1000.0) as i64;
    Some(if prefer_fps {
        (fps, pixels)
    } else {
        (pixels, fps)
    })
}
pub fn source_fps(v: &serde_json::Value) -> Option<f64> {
    let value = if v.get("fps").is_some() {
        &v["fps"]
    } else {
        &v["frame_rate"]
    };
    let n = value.as_f64().or_else(|| {
        value.as_str().and_then(|s| {
            if let Some((a, b)) = s.split_once('/') {
                Some(a.parse::<f64>().ok()? / b.parse::<f64>().ok()?)
            } else {
                s.parse().ok()
            }
        })
    })?;
    (n.is_finite() && n > 0.0 && n <= 1000.0).then_some(n)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn nested_secrets_and_signed_urls_do_not_reach_archive() {
        let value = serde_json::json!({"id":"90071992547409931","statistics":{"likes":12},"nested":[{"cookie":"sentinel","authentication_token":"sentinel","url_list":["https://cdn.invalid/?sign=sentinel"],"innocent":"https://cdn.invalid/?sign=sentinel"}],"encoded":"{\"token\":\"sentinel\"}"});
        let clean = safe_metadata(&value);
        assert!(!clean.to_string().contains("sentinel"));
        assert_eq!(clean["id"], "90071992547409931");
        assert_eq!(clean["statistics"]["likes"], 12);
    }
}
