//! 帧编解码：4 字节 LE 长度 + UTF-8 JSON，上限 1 MiB。
//!
//! 读到长度先校验上限再分配缓冲（C22）。

use serde::Serialize;
use serde::de::DeserializeOwned;
use std::io::{Read, Write};
use trace_domain::MAX_FRAME_BYTES;

#[derive(Debug, thiserror::Error)]
pub enum FrameError {
    #[error("frame too large: {0} bytes (max {MAX_FRAME_BYTES})")]
    TooLarge(u64),
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
    #[error("json error: {0}")]
    Json(#[from] serde_json::Error),
    #[error("connection closed before frame completed")]
    Truncated,
}

/// 从异步/同步读取端读取一帧并反序列化。`read_exact` 分配前已校验长度。
pub fn read_frame<R: Read, T: DeserializeOwned>(r: &mut R) -> Result<T, FrameError> {
    let mut len_buf = [0u8; 4];
    r.read_exact(&mut len_buf)?;
    let len = u32::from_le_bytes(len_buf);
    if len > MAX_FRAME_BYTES {
        return Err(FrameError::TooLarge(len as u64));
    }
    let mut buf = vec![0u8; len as usize];
    r.read_exact(&mut buf)?;
    Ok(serde_json::from_slice(&buf)?)
}

pub fn write_frame<W: Write, T: Serialize>(w: &mut W, value: &T) -> Result<(), FrameError> {
    let json = serde_json::to_vec(value)?;
    let len = json.len() as u32;
    if len > MAX_FRAME_BYTES {
        return Err(FrameError::TooLarge(len as u64));
    }
    w.write_all(&len.to_le_bytes())?;
    w.write_all(&json)?;
    w.flush()?;
    Ok(())
}

/// 编码为帧字节（测试与异步层复用）。
pub fn encode_frame<T: Serialize>(value: &T) -> Result<Vec<u8>, FrameError> {
    let json = serde_json::to_vec(value)?;
    if json.len() as u64 > MAX_FRAME_BYTES as u64 {
        return Err(FrameError::TooLarge(json.len() as u64));
    }
    let mut out = Vec::with_capacity(4 + json.len());
    out.extend_from_slice(&(json.len() as u32).to_le_bytes());
    out.extend_from_slice(&json);
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_frame() {
        let mut buf = Vec::new();
        write_frame(
            &mut buf,
            &serde_json::json!({"a": 1, "big": 123456789012345u64}),
        )
        .unwrap();
        let v: serde_json::Value = read_frame(&mut &buf[..]).unwrap();
        assert_eq!(v["a"], 1);
        assert_eq!(v["big"], 123456789012345u64);
    }

    #[test]
    fn rejects_overlong_length_before_allocation() {
        let mut input = Vec::new();
        input.extend_from_slice(&u32::MAX.to_le_bytes());
        let err = read_frame::<_, serde_json::Value>(&mut &input[..]).unwrap_err();
        assert!(matches!(err, FrameError::TooLarge(_)));
    }

    #[test]
    fn rejects_overlong_payload_on_encode() {
        let big = "x".repeat(MAX_FRAME_BYTES as usize + 10);
        assert!(encode_frame(&big).is_err());
    }
}
