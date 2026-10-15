//! OpenPGP detached-signature verification for the pinned yt-dlp release key (DEC-039).
//!
//! The release key is embedded in TRACE. Verification accepts a v4 RSA signature over the
//! release's `SHA2-256SUMS`: the signature packet's hashed prefix and trailer are hashed with
//! SHA-256, the RSA public-key operation is done on the signature MPI, and the result must be
//! the PKCS#1 v1.5 encoding of that digest. Only public release data is handled here; nothing
//! is fetched, stored or executed.
use num_bigint::BigUint;
use sha2::{Digest, Sha256, Sha512};

/// The yt-dlp release signing key, pinned with TRACE (public data, no secret).
pub const RELEASE_KEY: &str = include_str!("../../assets/yt-dlp-public.key");

fn dearmor(bytes: &[u8]) -> Option<Vec<u8>> {
    let Ok(text) = std::str::from_utf8(bytes) else {
        return Some(bytes.to_vec());
    };
    if !text.contains("-----BEGIN PGP") {
        return Some(bytes.to_vec());
    }
    let text = text.replace("\r\n", "\n");
    let start = text.find("\n\n")? + 2;
    let end = text[start..].find("-----END PGP")? + start;
    let body: String = text[start..end]
        .lines()
        .filter(|line| !line.contains(':') && !line.starts_with('='))
        .collect::<Vec<_>>()
        .join("");
    base64::Engine::decode(&base64::engine::general_purpose::STANDARD, body.trim()).ok()
}

struct Packet {
    tag: u8,
    body: Vec<u8>,
}

fn packets(data: &[u8]) -> Option<Vec<Packet>> {
    let mut out = Vec::new();
    let mut at = 0usize;
    while at < data.len() {
        let first = data[at];
        if first & 0x80 == 0 {
            return None;
        }
        at += 1;
        let (tag, length, header) = if first & 0x40 != 0 {
            let tag = first & 0x3f;
            if at >= data.len() {
                return None;
            }
            let b = data[at];
            at += 1;
            let (len, octets) = match b & 0x03 {
                0 => (usize::from(b), 1),
                1 => (0usize, 2),
                2 => (0usize, 4),
                _ => return None,
            };
            let len = match octets {
                1 => len,
                2 => {
                    if at + 2 > data.len() {
                        return None;
                    }
                    let v = usize::from(u16::from_be_bytes([data[at], data[at + 1]]));
                    at += 2;
                    v
                }
                _ => {
                    if at + 4 > data.len() {
                        return None;
                    }
                    let v = u32::from_be_bytes([data[at], data[at + 1], data[at + 2], data[at + 3]])
                        as usize;
                    at += 4;
                    v
                }
            };
            (tag, len, at)
        } else {
            let tag = (first >> 2) & 0x0f;
            let kind = first & 0x03;
            let octets = match kind {
                0 => 1,
                1 => 2,
                2 => 4,
                _ => return None,
            };
            if kind == 3 {
                return None;
            }
            let len = match octets {
                1 => {
                    if at >= data.len() {
                        return None;
                    }
                    let v = usize::from(data[at]);
                    at += 1;
                    v
                }
                2 => {
                    if at + 2 > data.len() {
                        return None;
                    }
                    let v = usize::from(u16::from_be_bytes([data[at], data[at + 1]]));
                    at += 2;
                    v
                }
                _ => {
                    if at + 4 > data.len() {
                        return None;
                    }
                    let v = u32::from_be_bytes([data[at], data[at + 1], data[at + 2], data[at + 3]])
                        as usize;
                    at += 4;
                    v
                }
            };
            (tag, len, at)
        };
        if length > data.len().saturating_sub(header) {
            return None;
        }
        out.push(Packet {
            tag,
            body: data[header..header + length].to_vec(),
        });
        at = header + length;
    }
    Some(out)
}

fn mpi(data: &[u8], at: &mut usize) -> Option<BigUint> {
    if *at + 2 > data.len() {
        return None;
    }
    let bits = usize::from(u16::from_be_bytes([data[*at], data[*at + 1]]));
    *at += 2;
    let bytes = bits.div_ceil(8);
    if *at + bytes > data.len() {
        return None;
    }
    let value = BigUint::from_bytes_be(&data[*at..*at + bytes]);
    *at += bytes;
    Some(value)
}

pub struct RsaKey {
    pub n: BigUint,
    pub e: BigUint,
}

/// Every RSA key or subkey in the pinned key block. The block itself is trusted; the signature
/// is accepted when any key in it verifies.
pub fn keys(data: &[u8]) -> Option<Vec<RsaKey>> {
    let packets = packets(&dearmor(data)?)?;
    let mut keys = Vec::new();
    for packet in packets {
        if !matches!(packet.tag, 6 | 14) {
            continue;
        }
        let body = &packet.body;
        if body.first().copied()? != 4 {
            continue;
        }
        let algorithm = *body.get(5)?;
        if !matches!(algorithm, 1..=3) {
            continue;
        }
        let mut at = 6;
        let Some(n) = mpi(body, &mut at) else {
            continue;
        };
        let Some(e) = mpi(body, &mut at) else {
            continue;
        };
        keys.push(RsaKey { n, e });
    }
    Some(keys)
}

pub struct Signature {
    version: u8,
    sig_type: u8,
    public_algorithm: u8,
    hash_algorithm: u8,
    /// The packet body from the version byte through the hashed subpackets.
    hashed_prefix: Vec<u8>,
    left16: [u8; 2],
    value: BigUint,
}

pub fn signature(data: &[u8]) -> Option<Signature> {
    let packets = packets(&dearmor(data)?)?;
    let packet = packets.into_iter().find(|p| p.tag == 2)?;
    let body = packet.body;
    let version = *body.first()?;
    if version != 4 {
        return None;
    }
    let sig_type = *body.get(1)?;
    let public_algorithm = *body.get(2)?;
    let hash_algorithm = *body.get(3)?;
    let hashed_len = usize::from(u16::from_be_bytes([*body.get(4)?, *body.get(5)?]));
    let prefix_end = 6 + hashed_len;
    if prefix_end + 2 > body.len() {
        return None;
    }
    let hashed_prefix = body[..prefix_end].to_vec();
    let unhashed_len = usize::from(u16::from_be_bytes([
        *body.get(prefix_end)?,
        *body.get(prefix_end + 1)?,
    ]));
    let mut at = prefix_end + 2 + unhashed_len;
    if at + 2 > body.len() {
        return None;
    }
    let left16 = [body[at], body[at + 1]];
    at += 2;
    let value = mpi(&body, &mut at)?;
    Some(Signature {
        version,
        sig_type,
        public_algorithm,
        hash_algorithm,
        hashed_prefix,
        left16,
        value,
    })
}

/// The PKCS#1 v1.5 DigestInfo prefix for the hash the signature names.
fn digest_info(hash_algorithm: u8) -> Option<&'static [u8]> {
    match hash_algorithm {
        8 => Some(&[
            0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02,
            0x01, 0x05, 0x00, 0x04, 0x20,
        ]),
        10 => Some(&[
            0x30, 0x51, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02,
            0x03, 0x05, 0x00, 0x04, 0x40,
        ]),
        _ => None,
    }
}

fn verify_with(key: &RsaKey, signature: &Signature, digest: &[u8]) -> bool {
    let signed = signature.value.modpow(&key.e, &key.n).to_bytes_be();
    let size = key.n.to_bytes_be().len();
    if signed.len() > size {
        return false;
    }
    let mut padded = vec![0u8; size - signed.len()];
    padded.extend_from_slice(&signed);
    if padded.len() < 35 {
        return false;
    }
    if padded[0] != 0 || padded[1] != 1 {
        return false;
    }
    let Some(separator) = padded[2..].iter().position(|b| *b == 0).map(|p| p + 2) else {
        return false;
    };
    if separator < 10 {
        return false;
    }
    let Some(info) = digest_info(signature.hash_algorithm) else {
        return false;
    };
    let content = &padded[separator + 1..];
    if content.len() != info.len() + digest.len() {
        return false;
    }
    content[..info.len()] == *info && content[info.len()..] == *digest
}

/// Verifies a detached v4 signature over `data` with the pinned release key.
pub fn verify(data: &[u8], detached: &[u8]) -> Result<(), String> {
    let signature = signature(detached).ok_or_else(|| "unreadable signature".to_owned())?;
    if signature.version != 4 || signature.sig_type != 0x00 {
        return Err("not a binary document signature".into());
    }
    if signature.public_algorithm != 1 || !matches!(signature.hash_algorithm, 8 | 10) {
        return Err("not an RSA/SHA-2 signature".into());
    }
    let mut signed = Vec::with_capacity(data.len() + signature.hashed_prefix.len() + 6);
    signed.extend_from_slice(data);
    signed.extend_from_slice(&signature.hashed_prefix);
    let mut trailer = vec![0x04, 0xff];
    trailer.extend_from_slice(&(signature.hashed_prefix.len() as u32).to_be_bytes());
    signed.extend_from_slice(&trailer);
    let digest = if signature.hash_algorithm == 10 {
        Sha512::digest(&signed).to_vec()
    } else {
        Sha256::digest(&signed).to_vec()
    };
    if signature.left16 != [digest[0], digest[1]] {
        return Err("signature checksum mismatch".into());
    }
    let keys = keys(RELEASE_KEY.as_bytes()).ok_or_else(|| "release key unreadable".to_owned())?;
    if keys
        .iter()
        .any(|key| verify_with(key, &signature, digest.as_slice()))
    {
        return Ok(());
    }
    Err("signature does not verify with the pinned release key".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    const SUMS: &[u8] = include_bytes!("../../testdata/yt-dlp/SHA2-256SUMS");
    const SIG: &[u8] = include_bytes!("../../testdata/yt-dlp/SHA2-256SUMS.sig");

    #[test]
    fn the_pinned_release_signature_verifies() {
        verify(SUMS, SIG).expect("the real release signature verifies with the pinned key");
    }

    #[test]
    fn a_tampered_sums_file_is_rejected() {
        let mut tampered = SUMS.to_vec();
        tampered.extend_from_slice(b"\n");
        assert!(verify(&tampered, SIG).is_err());
    }

    #[test]
    fn a_tampered_signature_is_rejected() {
        let mut tampered = SIG.to_vec();
        let at = tampered.len() / 2;
        tampered[at] ^= 0x01;
        assert!(verify(SUMS, &tampered).is_err());
    }
}
