//! Explicit bounded large-message extension for trusted platform adapters.
//! Ordinary wire/control frames retain their 1 MiB limit. Chunks are ordered,
//! hashed, scoped to a request, and never expose a file or a shared body handle.
use crate::wire::{self, FrameError};
use base64::{Engine, engine::general_purpose::STANDARD};
use serde::Serialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::HashMap,
    time::{Duration, Instant},
};

pub const METADATA_MAX: u32 = 8 * 1024 * 1024;
pub const MESSAGE_MAX: usize = 12 * 1024 * 1024;
const CHUNK_MAX: usize = 192 * 1024;
const MAX_INFLIGHT: usize = 4;
fn invalid() -> FrameError {
    FrameError::Io(std::io::Error::new(
        std::io::ErrorKind::InvalidData,
        "invalid large plugin message",
    ))
}

pub fn encode<T: Serialize>(value: &T) -> Result<Vec<u8>, FrameError> {
    let bytes = serde_json::to_vec(value)?;
    if bytes.len() <= trace_domain::MAX_FRAME_BYTES as usize {
        return wire::encode_frame(value);
    }
    if bytes.len() > MESSAGE_MAX {
        return Err(FrameError::TooLarge(bytes.len() as u64));
    }
    let v: Value = serde_json::from_slice(&bytes)?;
    let id = v["request_id"]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= 128)
        .ok_or_else(invalid)?;
    let sha = Sha256::digest(&bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>();
    let mut frames = Vec::new();
    for (index, chunk) in bytes.chunks(CHUNK_MAX).enumerate() {
        frames.extend(wire::encode_frame(&json!({"plugin_chunk_v1":true,"request_id":id,"index":index,"total_bytes":bytes.len(),"sha256":sha,"body_b64":STANDARD.encode(chunk)}))?);
    }
    Ok(frames)
}
struct Partial {
    bytes: Vec<u8>,
    total: usize,
    next: u64,
    sha: String,
    started: Instant,
}
#[derive(Default)]
pub struct Decoder {
    partial: HashMap<String, Partial>,
}
impl Decoder {
    pub fn push(&mut self, value: Value) -> Result<Option<Value>, FrameError> {
        self.partial
            .retain(|_, p| p.started.elapsed() < Duration::from_secs(30));
        if value.get("plugin_chunk_v1").is_none() {
            return Ok(Some(value));
        }
        if value["plugin_chunk_v1"] != true {
            return Err(invalid());
        }
        let id = value["request_id"]
            .as_str()
            .filter(|s| !s.is_empty() && s.len() <= 128)
            .ok_or_else(invalid)?
            .to_owned();
        let index = value["index"].as_u64().ok_or_else(invalid)?;
        let total = value["total_bytes"]
            .as_u64()
            .filter(|n| *n > trace_domain::MAX_FRAME_BYTES as u64 && *n <= MESSAGE_MAX as u64)
            .ok_or_else(invalid)? as usize;
        let sha = value["sha256"]
            .as_str()
            .filter(|s| s.len() == 64 && s.bytes().all(|b| b.is_ascii_hexdigit()))
            .ok_or_else(invalid)?
            .to_owned();
        let text = value["body_b64"]
            .as_str()
            .filter(|s| s.len() <= CHUNK_MAX * 4 / 3 + 4)
            .ok_or_else(invalid)?;
        let chunk = STANDARD.decode(text).map_err(|_| invalid())?;
        if chunk.is_empty() || chunk.len() > CHUNK_MAX {
            return Err(invalid());
        }
        if index == 0 {
            if self.partial.contains_key(&id) || self.partial.len() >= MAX_INFLIGHT {
                return Err(invalid());
            }
            self.partial.insert(
                id.clone(),
                Partial {
                    bytes: Vec::new(),
                    total,
                    next: 0,
                    sha: sha.clone(),
                    started: Instant::now(),
                },
            );
        }
        let part = self.partial.get_mut(&id).ok_or_else(invalid)?;
        if part.next != index
            || part.total != total
            || part.sha != sha
            || part.bytes.len() + chunk.len() > total
        {
            self.partial.remove(&id);
            return Err(invalid());
        }
        part.bytes.extend(chunk);
        part.next += 1;
        if part.bytes.len() != total {
            return Ok(None);
        }
        let part = self.partial.remove(&id).unwrap();
        if Sha256::digest(&part.bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
            != part.sha
        {
            return Err(invalid());
        }
        let result: Value = serde_json::from_slice(&part.bytes)?;
        if result["request_id"] != id || result.get("plugin_chunk_v1").is_some() {
            return Err(invalid());
        }
        Ok(Some(result))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn c14_large_interleaving_expiry_restart_and_single_item_ceiling() {
        let a = json!({"request_id":"a","result":"a".repeat(1100000)});
        let b = json!({"request_id":"b","result":"b".repeat(1100000)});
        let frames = |v: &Value| {
            let encoded = encode(v).unwrap();
            let mut bytes = encoded.as_slice();
            let mut result = Vec::new();
            while !bytes.is_empty() {
                result.push(wire::read_frame::<_, Value>(&mut bytes).unwrap());
            }
            result
        };
        let (aa, bb) = (frames(&a), frames(&b));
        let mut d = Decoder::default();
        let mut finished = Vec::new();
        for (a, b) in aa.iter().zip(&bb) {
            for frame in [a, b] {
                if let Some(v) = d.push(frame.clone()).unwrap() {
                    finished.push(v);
                }
            }
        }
        assert_eq!(finished, vec![a, b]);
        let mut cancelled = Decoder::default();
        cancelled.push(aa[0].clone()).unwrap();
        drop(cancelled);
        assert!(Decoder::default().push(aa[1].clone()).is_err());
        let mut expired = Decoder::default();
        expired.push(aa[0].clone()).unwrap();
        expired.partial.get_mut("a").unwrap().started = Instant::now() - Duration::from_secs(31);
        assert!(expired.push(aa[1].clone()).is_err());
        assert!(
            encode(&json!({"request_id":"single-item","result":{"one":"x".repeat(MESSAGE_MAX)}}))
                .is_err()
        );
    }
    #[test]
    fn c14_large_messages_preserve_all_bytes_and_reject_corruption_order_and_budget() {
        let v = json!({"request_id":"source-page","ok":true,"result":"正文".repeat(250000)});
        assert!(wire::encode_frame(&v).is_err());
        let encoded = encode(&v).unwrap();
        let mut input = encoded.as_slice();
        let mut frames = Vec::new();
        while !input.is_empty() {
            frames.push(wire::read_frame::<_, Value>(&mut input).unwrap());
        }
        assert!(frames.len() > 1);
        let mut decoder = Decoder::default();
        let mut result = None;
        for f in &frames {
            result = decoder.push(f.clone()).unwrap();
        }
        assert_eq!(result, Some(v));
        let mut d = Decoder::default();
        assert!(d.push(frames[1].clone()).is_err());
        let mut f = frames[0].clone();
        f["total_bytes"] = json!(MESSAGE_MAX + 1);
        assert!(d.push(f).is_err());
        for f in &mut frames {
            f["sha256"] = json!("0".repeat(64));
        }
        let mut d = Decoder::default();
        for f in &frames[..frames.len() - 1] {
            assert!(d.push(f.clone()).unwrap().is_none());
        }
        assert!(d.push(frames.last().unwrap().clone()).is_err());
        let mut d = Decoder::default();
        for n in 0..MAX_INFLIGHT {
            let mut f = frames[0].clone();
            f["request_id"] = json!(format!("request-{n}"));
            assert!(d.push(f).unwrap().is_none());
        }
        let mut f = frames[0].clone();
        f["request_id"] = json!("overflow");
        assert!(d.push(f).is_err());
        drop(d);
        assert!(Decoder::default().push(frames[1].clone()).is_err());
    }
}
