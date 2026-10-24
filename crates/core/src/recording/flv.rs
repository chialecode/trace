//! Bound the framing dependency and validate fields it deliberately omits.
use crate::error::CoreError;
use flv_core::{Demuxer, Tag};
const MAX_TAG: usize = 8 * 1024 * 1024;
pub struct Framer {
    buffer: Vec<u8>,
    header: bool,
    demux: Demuxer,
}
impl Default for Framer {
    fn default() -> Self {
        Self {
            buffer: vec![],
            header: false,
            demux: Demuxer::new(),
        }
    }
}
impl Framer {
    pub fn push(&mut self, bytes: &[u8]) -> Result<Vec<Tag>, CoreError> {
        fn bad() -> CoreError {
            CoreError::InvalidInput("invalid or oversized FLV frame".into())
        }
        if self.buffer.len() + bytes.len() > MAX_TAG + 65536 {
            return Err(bad());
        }
        self.buffer.extend_from_slice(bytes);
        if !self.header {
            if self.buffer.len() < 9 {
                return Ok(vec![]);
            }
            let offset = u32::from_be_bytes(self.buffer[5..9].try_into().unwrap()) as usize;
            if &self.buffer[..3] != b"FLV"
                || self.buffer[3] != 1
                || self.buffer[4] & !5 != 0
                || !(9..=4096).contains(&offset)
            {
                return Err(bad());
            }
            if self.buffer.len() < offset + 4 {
                return Ok(vec![]);
            }
            if self.buffer[offset..offset + 4] != [0; 4] {
                return Err(bad());
            }
            self.demux.push_bytes(&self.buffer[..offset + 4]);
            self.buffer.drain(..offset + 4);
            self.header = true;
        }
        let mut out = vec![];
        loop {
            if self.buffer.len() < 11 {
                break;
            }
            let size = ((self.buffer[1] as usize) << 16)
                | ((self.buffer[2] as usize) << 8)
                | self.buffer[3] as usize;
            if size > MAX_TAG || self.buffer[8..11] != [0; 3] {
                return Err(bad());
            }
            if self.buffer.len() < size + 15 {
                break;
            }
            if u32::from_be_bytes(self.buffer[size + 11..size + 15].try_into().unwrap()) as usize
                != size + 11
            {
                return Err(bad());
            }
            self.demux.push_bytes(&self.buffer[..size + 15]);
            self.buffer.drain(..size + 15);
            out.push(self.demux.poll_tag().map_err(|_| bad())?.ok_or_else(bad)?);
        }
        Ok(out)
    }
    pub fn tail_len(&self) -> usize {
        self.buffer.len()
    }
    pub fn has_video(&self) -> bool {
        self.demux.has_video().unwrap_or(false)
    }
    pub fn has_audio(&self) -> bool {
        self.demux.has_audio().unwrap_or(false)
    }
}
pub fn is_config(t: &Tag) -> bool {
    t.data.len() > 1
        && t.data[1] == 0
        && match t.tag_type {
            flv_core::TagType::Audio => t.data[0] >> 4 == 10,
            flv_core::TagType::Video => matches!(t.data[0] & 15, 7 | 12),
            _ => false,
        }
}
pub fn is_boundary(t: &Tag, video: bool) -> bool {
    if video {
        t.tag_type == flv_core::TagType::Video
            && t.data.first().is_some_and(|v| v >> 4 == 1)
            && !is_config(t)
            && (!t.data.first().is_some_and(|v| matches!(v & 15, 7 | 12))
                || t.data.get(1) == Some(&1))
    } else {
        t.tag_type == flv_core::TagType::Audio && !is_config(t)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn flv_partial_tags_corrupt_sizes_and_unbounded_headers() {
        let mut mux = flv_core::Muxer::new();
        let mut bytes = vec![];
        mux.write_header(true, false, &mut bytes);
        let tag = Tag {
            tag_type: flv_core::TagType::Audio,
            timestamp_ms: 99,
            data: vec![0xaf, 1, 1, 2, 3].into(),
        };
        mux.write_tag(&tag, &mut bytes).unwrap();
        let mut f = Framer::default();
        let mut out = vec![];
        for b in &bytes {
            out.extend(f.push(&[*b]).unwrap());
        }
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].timestamp_ms, 99);
        assert_eq!(f.tail_len(), 0);
        *bytes.last_mut().unwrap() = 0;
        assert!(Framer::default().push(&bytes).is_err());
        assert!(
            Framer::default()
                .push(b"FLV\x01\x05\xff\xff\xff\xff")
                .is_err()
        );
    }
}
