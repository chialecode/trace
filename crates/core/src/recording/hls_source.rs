//! Fetching and preserving the resources of one HLS playlist: segments, keys and
//! initialization maps. All requests go through the constrained media proxy.
use super::fragment::{begin, finish};
use super::{hls, invalid};
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::transport::HostRules;
use serde_json::{Value, json};
use trace_contracts::plugin::LiveStream;
use url::Url;

const AES_KEY_BYTES: usize = 16;

/// The playlist a resource belongs to, with everything needed to fetch it.
pub(super) struct Source<'a> {
    pub core: &'a Core,
    pub job: &'a ClaimedJob,
    pub stream: &'a LiveStream,
    pub rules: &'a HostRules,
    /// Relative URIs of the playlist resolve against this.
    pub base: &'a Url,
}

/// Messages for the three ways an initialization map can be refused.
pub(super) struct MapErrors {
    pub iv: &'static str,
    pub uri: &'static str,
    pub range: &'static str,
}

pub(super) const HLS_MAP: MapErrors = MapErrors {
    iv: "encrypted HLS MAP requires explicit IV",
    uri: "HLS MAP URI",
    range: "HLS MAP range requires offset",
};

pub(super) const SUBTITLE_MAP: MapErrors = MapErrors {
    iv: "subtitle MAP needs IV",
    uri: "subtitle MAP URI",
    range: "subtitle MAP range",
};

/// An initialization map: the preserved original and the bytes that precede the segment.
pub(super) struct InitPart {
    pub original: String,
    pub plain: Vec<u8>,
}

impl Source<'_> {
    pub(super) async fn fetch(
        &self,
        url: &str,
        range: Option<(u64, u64)>,
        limit: usize,
    ) -> Result<(Vec<u8>, Url), CoreError> {
        self.core
            .http
            .live_resource(self.rules, url, &self.stream.public_headers, range, limit)
            .await
    }

    /// The segment as playable bytes: decrypted with the authorized AES-128 key, if any.
    pub(super) async fn plain(
        &self,
        bytes: Vec<u8>,
        key: Option<&m3u8_rs::Key>,
        sequence: u64,
    ) -> Result<Vec<u8>, CoreError> {
        let Some(k) = key else {
            return Ok(bytes);
        };
        hls::supported_key(k)?;
        if k.method != m3u8_rs::KeyMethod::AES128 {
            return Ok(bytes);
        }
        let uri = k
            .uri
            .as_deref()
            .ok_or_else(|| invalid("HLS key URI missing"))?;
        let url = self.base.join(uri).map_err(|_| invalid("HLS key URI"))?;
        let (key_bytes, _) = self.fetch(url.as_str(), None, AES_KEY_BYTES).await?;
        if key_bytes.len() != AES_KEY_BYTES {
            return Err(invalid("HLS AES key length"));
        }
        hls::decrypt(&bytes, &key_bytes, &hls::iv(k, sequence)?)
    }

    /// Keeps the exact response as its own non-required recording source and returns its id.
    pub(super) async fn preserve(&self, source: String, bytes: &[u8]) -> Result<String, CoreError> {
        use sha2::Digest;
        let digest: String = sha2::Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        let source = format!("{source}:sha256:{digest}");
        let Some(mut raw) = begin(
            self.core,
            self.job,
            source.clone(),
            "application/octet-stream",
            0,
        )
        .await?
        else {
            return self.preserved_media(source).await;
        };
        let media = raw.media.clone();
        let m = media.clone();
        self.core
            .db
            .write(move |tx| {
                tx.execute(
                    "UPDATE media_items SET role='recording_source',required=0 WHERE media_id=?1",
                    [m],
                )?;
                Ok(())
            })
            .await?;
        raw.writer
            .write_chunk(&self.core.db, &raw.op, bytes)
            .await?;
        let evidence = json!({
            "original_capture": true,
            "byte_count": bytes.len(),
            "boundary": "exact_resource_response",
        });
        finish(self.core, self.job, raw, 0, None, evidence).await?;
        Ok(media)
    }

    /// A resource saved before keeps its media id; saving it again is a no-op.
    async fn preserved_media(&self, source: String) -> Result<String, CoreError> {
        let session = self.job.subject_id.clone();
        self.core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT media_id FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state IN ('sealed','source')",
                    rusqlite::params![session, source],
                    |r| r.get(0),
                )?)
            })
            .await
    }

    /// The segment's initialization map, fetched, preserved and decrypted. `preserve_as`
    /// names the preserved original.
    pub(super) async fn init_part(
        &self,
        seg: &hls::Segment,
        errors: &MapErrors,
        limit: usize,
        preserve_as: String,
    ) -> Result<Option<InitPart>, CoreError> {
        let Some(map) = &seg.map else {
            return Ok(None);
        };
        let needs_iv = seg
            .map_key
            .as_ref()
            .is_some_and(|k| k.method == m3u8_rs::KeyMethod::AES128 && k.iv.is_none());
        if needs_iv {
            return Err(invalid(errors.iv));
        }
        let url = self.base.join(&map.uri).map_err(|_| invalid(errors.uri))?;
        let range = map
            .byte_range
            .as_ref()
            .map(|r| {
                r.offset
                    .map(|o| (o, r.length))
                    .ok_or_else(|| invalid(errors.range))
            })
            .transpose()?;
        let (raw, _) = self.fetch(url.as_str(), range, limit).await?;
        let original = self.preserve(preserve_as, &raw).await?;
        let plain = self.plain(raw, seg.map_key.as_ref(), seg.sequence).await?;
        Ok(Some(InitPart { original, plain }))
    }
}

/// Where a track's capture resumes: the media-sequence window it had reached.
pub(super) struct Resume {
    pub epoch: u64,
    /// The playlist restarted below the sequence it had reached.
    pub reset: bool,
    /// The last sequence saved, whatever the epoch.
    pub prior_last: Option<u64>,
    /// The last sequence saved in the current epoch.
    pub last: Option<u64>,
}

pub(super) fn resume_point(prior: &Value, media_sequence: u64) -> Resume {
    let prior_epoch = prior["epoch"].as_u64().unwrap_or(0);
    let reset = prior["first"]
        .as_u64()
        .is_some_and(|first| media_sequence < first);
    let epoch = prior_epoch + u64::from(reset);
    let prior_last = prior["last"].as_u64();
    Resume {
        epoch,
        reset,
        prior_last,
        last: prior_last.filter(|_| epoch == prior_epoch),
    }
}

/// The progress saved for a playlist field of the session's evidence.
pub(super) async fn load_progress(
    core: &Core,
    job: &ClaimedJob,
    field: &str,
) -> Result<Value, CoreError> {
    let (session, field) = (job.subject_id.clone(), field.to_owned());
    core.db
        .read(move |c| {
            let evidence: String = c.query_row(
                "SELECT evidence FROM live_sessions WHERE session_id=?1",
                [session],
                |r| r.get(0),
            )?;
            Ok(serde_json::from_str::<Value>(&evidence)?[&field].clone())
        })
        .await
}

pub(super) async fn save_progress(
    core: &Core,
    job: &ClaimedJob,
    field: &str,
    progress: &Value,
) -> Result<(), CoreError> {
    let (j, path, progress) = (job.clone(), format!("$.{field}"), progress.to_string());
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_sessions SET evidence=json_set(evidence,?2,json(?3)) WHERE session_id=?1",
                rusqlite::params![j.subject_id, path, progress],
            )?;
            Ok(())
        })
        .await
}
