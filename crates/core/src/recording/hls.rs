//! RFC 8216 ingestion. Parsing is delegated to m3u8-rs; this boundary enforces
//! budgets and repairs two verified 6.0.1 parser limitations without changing
//! KEY NONE semantics or silently dropping encryption state.
use crate::error::CoreError;
use m3u8_rs::{Key, KeyMethod, Map};

pub enum Playlist {
    MasterPlaylist(m3u8_rs::MasterPlaylist),
    MediaPlaylist(MediaPlaylist),
}
pub struct MediaPlaylist {
    parsed: m3u8_rs::MediaPlaylist,
    map_keys: Vec<Option<Key>>,
}
impl std::ops::Deref for MediaPlaylist {
    type Target = m3u8_rs::MediaPlaylist;
    fn deref(&self) -> &Self::Target {
        &self.parsed
    }
}

fn bad() -> CoreError {
    CoreError::InvalidInput("invalid HLS playlist".into())
}
pub fn parse(bytes: &[u8]) -> Result<Playlist, CoreError> {
    if bytes.len() > 512 * 1024 {
        return Err(bad());
    }
    let text = std::str::from_utf8(bytes).map_err(|_| bad())?;
    if !text.trim_start().starts_with("#EXTM3U") {
        return Err(bad());
    }
    // 6.0.1 erroneously requires IV for METHOD=NONE. An IV is unused for NONE.
    // Keep the NONE tag so it cancels earlier encryption; never remove the tag.
    let normalized = text
        .lines()
        .map(|l| {
            if l.starts_with("#EXT-X-KEY:")
                && l.split([':', ',']).any(|a| a == "METHOD=NONE")
                && !l.contains("IV=")
            {
                format!("{l},IV=0x00")
            } else {
                l.to_owned()
            }
        })
        .collect::<Vec<_>>()
        .join("\n")
        + "\n";
    let playlist = m3u8_rs::parse_playlist_res(normalized.as_bytes()).map_err(|_| bad())?;
    if let m3u8_rs::Playlist::MediaPlaylist(mut p) = playlist {
        if p.segments.len() > 2048
            || p.segments.len() != text.lines().filter(|l| l.starts_with("#EXTINF:")).count()
        {
            return Err(bad());
        }
        // m3u8-rs keeps the segment's final key but loses MAP/KEY declaration
        // ordering. Preserve only that ordering here; attribute grammar remains
        // delegated to the same parser, including quoted commas in a key URI.
        let mut declared_key = None;
        let mut map_key = None;
        let mut map_keys = Vec::new();
        for line in normalized.lines().map(str::trim) {
            if line.starts_with("#EXT-X-KEY:") {
                let single =
                    format!("#EXTM3U\n#EXT-X-TARGETDURATION:1\n{line}\n#EXTINF:1,\nsegment\n");
                let parsed =
                    m3u8_rs::parse_media_playlist_res(single.as_bytes()).map_err(|_| bad())?;
                declared_key = parsed.segments.first().and_then(|s| s.key.clone());
                if declared_key.is_none() {
                    return Err(bad());
                }
            } else if line.starts_with("#EXT-X-MAP:") {
                map_key = declared_key.clone();
            } else if !line.is_empty() && !line.starts_with('#') {
                map_keys.push(map_key.clone());
            }
        }
        if map_keys.len() != p.segments.len() {
            return Err(bad());
        }
        let mut key = None;
        for s in &mut p.segments {
            if s.key.is_some() {
                key = s.key.clone();
            } else {
                s.key = key.clone();
            }
            if !s.duration.is_finite() || s.duration <= 0.0 || s.uri.is_empty() {
                return Err(bad());
            }
        }
        return Ok(Playlist::MediaPlaylist(MediaPlaylist {
            parsed: p,
            map_keys,
        }));
    }
    let m3u8_rs::Playlist::MasterPlaylist(master) = playlist else {
        unreachable!()
    };
    Ok(Playlist::MasterPlaylist(master))
}

pub fn iv(key: &Key, sequence: u64) -> Result<[u8; 16], CoreError> {
    let mut result = [0u8; 16];
    if let Some(hex) = &key.iv {
        let hex = hex
            .strip_prefix("0x")
            .or_else(|| hex.strip_prefix("0X"))
            .ok_or_else(bad)?;
        if hex.is_empty() || hex.len() > 32 || !hex.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(bad());
        }
        let padded = format!("{hex:0>32}");
        for (i, slot) in result.iter_mut().enumerate() {
            *slot = u8::from_str_radix(&padded[i * 2..i * 2 + 2], 16).map_err(|_| bad())?;
        }
    } else {
        result[8..].copy_from_slice(&sequence.to_be_bytes());
    }
    Ok(result)
}
pub fn decrypt(bytes: &[u8], key: &[u8], iv: &[u8; 16]) -> Result<Vec<u8>, CoreError> {
    use aes::cipher::{BlockModeDecrypt, KeyIvInit, block_padding::Pkcs7};
    cbc::Decryptor::<aes::Aes128>::new_from_slices(key, iv)
        .map_err(|_| bad())?
        .decrypt_padded_vec::<Pkcs7>(bytes)
        .map_err(|_| CoreError::InvalidInput("HLS AES padding or key mismatch".into()))
}
pub fn supported_key(key: &Key) -> Result<(), CoreError> {
    if key.method != KeyMethod::None
        && (key.method != KeyMethod::AES128
            || key.keyformat.as_deref().is_some_and(|k| k != "identity"))
    {
        return Err(CoreError::Unsupported(
            "HLS DRM or encryption method unavailable".into(),
        ));
    }
    Ok(())
}

#[derive(Clone)]
pub struct Segment {
    pub sequence: u64,
    pub discontinuity: u64,
    pub uri: String,
    pub range: Option<(u64, u64)>,
    pub key: Option<Key>,
    pub map: Option<Map>,
    pub map_key: Option<Key>,
    pub duration_ms: i64,
    pub source_gap: bool,
}
pub fn segments(p: &MediaPlaylist) -> Result<Vec<Segment>, CoreError> {
    let mut previous: Option<(String, u64)> = None;
    let mut discontinuity = p.discontinuity_sequence;
    let mut map: Option<Map> = None;
    p.segments
        .iter()
        .enumerate()
        .map(|(i, s)| {
            if s.map.is_some() {
                map = s.map.clone();
            }
            if s.discontinuity {
                discontinuity += 1;
            }
            let range = if let Some(r) = &s.byte_range {
                let offset = match r.offset {
                    Some(v) => v,
                    None => previous
                        .as_ref()
                        .filter(|(u, _)| u == &s.uri)
                        .map(|(_, v)| *v)
                        .ok_or_else(bad)?,
                };
                let end = offset
                    .checked_add(r.length)
                    .filter(|_| r.length > 0)
                    .ok_or_else(bad)?;
                previous = Some((s.uri.clone(), end));
                Some((offset, r.length))
            } else {
                previous = None;
                None
            };
            if let Some(k) = &s.key {
                supported_key(k)?;
            }
            Ok(Segment {
                sequence: p.media_sequence.checked_add(i as u64).ok_or_else(bad)?,
                discontinuity,
                uri: s.uri.clone(),
                range,
                key: s.key.clone(),
                map: map.clone(),
                map_key: p.map_keys[i].clone(),
                duration_ms: (s.duration as f64 * 1000.0) as i64,
                source_gap: s.unknown_tags.iter().any(|t| t.tag == "X-GAP"),
            })
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn key_none_inheritance_ranges_map_and_discontinuity() {
        let text=b"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:9\n#EXT-X-MAP:URI=\"init.mp4\",BYTERANGE=\"16@0\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:6,\n#EXT-X-BYTERANGE:16@16\na.mp4\n#EXTINF:6,\n#EXT-X-BYTERANGE:16\na.mp4\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-DISCONTINUITY\n#EXTINF:6,\nb.ts\n#EXT-X-ENDLIST\n";
        let Playlist::MediaPlaylist(p) = parse(text).unwrap() else {
            panic!()
        };
        let s = segments(&p).unwrap();
        assert!(p.end_list);
        assert_eq!(s[1].range, Some((32, 16)));
        assert_eq!(s[1].key.as_ref().unwrap().method, KeyMethod::AES128);
        assert!(s[1].map.is_some());
        assert_eq!(s[2].key.as_ref().unwrap().method, KeyMethod::None);
        assert_eq!(s[2].discontinuity, 1);
        assert_eq!(s[2].sequence, 11);
        assert_eq!(
            &iv(s[0].key.as_ref().unwrap(), 9).unwrap()[8..],
            &9u64.to_be_bytes()
        );
    }
    #[test]
    fn map_keeps_declaration_key_across_segment_key_rotation() {
        let Playlist::MediaPlaylist(p)=parse(b"#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-KEY:METHOD=AES-128,URI=\"key-a\",IV=0x01\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2,\na.m4s\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:2,\nb.m4s\n").unwrap()else{panic!()};
        let s = segments(&p).unwrap();
        assert_eq!(s[1].key.as_ref().unwrap().method, KeyMethod::None);
        assert_eq!(s[1].map_key.as_ref().unwrap().uri.as_deref(), Some("key-a"));
        assert_eq!(s[1].map_key.as_ref().unwrap().iv.as_deref(), Some("0x01"));
    }
    #[test]
    fn ranges_drm_and_truncated_cipher_fail() {
        let Playlist::MediaPlaylist(p) =
            parse(b"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\n#EXT-X-BYTERANGE:20\nx.ts\n")
                .unwrap()
        else {
            panic!()
        };
        assert!(segments(&p).is_err());
        assert!(decrypt(&[1; 15], &[0; 16], &[0; 16]).is_err());
        let k = Key {
            method: KeyMethod::SampleAES,
            ..Default::default()
        };
        assert!(supported_key(&k).is_err());
    }
    #[test]
    fn map_key_respects_tag_order_before_same_segment() {
        for (tags, map_uri, segment_uri) in [
            (
                "#EXT-X-MAP:URI=\"init\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"segment-key\",IV=0x01",
                None,
                Some("segment-key"),
            ),
            (
                "#EXT-X-KEY:METHOD=AES-128,URI=\"map,key\",IV=0x02\n#EXT-X-MAP:URI=\"init\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"segment-key\",IV=0x01",
                Some("map,key"),
                Some("segment-key"),
            ),
            (
                "#EXT-X-KEY:METHOD=AES-128,URI=\"map-key\",IV=0x02\n#EXT-X-MAP:URI=\"init\"\n#EXT-X-KEY:METHOD=NONE",
                Some("map-key"),
                None,
            ),
        ] {
            let text = format!(
                "#EXTM3U\n#EXT-X-TARGETDURATION:2\n{tags}\n#EXTINF:2,\none\n#EXTINF:2,\ntwo\n"
            );
            let Playlist::MediaPlaylist(p) = parse(text.as_bytes()).unwrap() else {
                panic!()
            };
            for s in segments(&p).unwrap() {
                assert_eq!(s.map_key.as_ref().and_then(|k| k.uri.as_deref()), map_uri);
                assert_eq!(s.key.as_ref().and_then(|k| k.uri.as_deref()), segment_uri);
            }
        }
    }
    #[test]
    fn aes128_known_roundtrip_and_wrong_key() {
        use aes::cipher::{BlockModeEncrypt, KeyIvInit, block_padding::Pkcs7};
        let plain = b"ordinary authorized media segment";
        let encrypted = cbc::Encryptor::<aes::Aes128>::new_from_slices(&[7; 16], &[9; 16])
            .unwrap()
            .encrypt_padded_vec::<Pkcs7>(plain);
        assert_eq!(decrypt(&encrypted, &[7; 16], &[9; 16]).unwrap(), plain);
        assert!(decrypt(&encrypted, &[1; 16], &[9; 16]).is_err());
    }
}
