//! FLV recording: the received bytes are kept whole as a raw capture, and a re-muxed
//! playable copy is cut at frame boundaries into rotating fragments. Every exit, including
//! write failures and revoked leases, reaches finalization.
use super::fragment::{Fragment, begin, disk_budget, finish, gap};
use super::{flv, invalid, media_rules, state};
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use serde_json::json;
use std::time::{Duration, Instant};
use trace_contracts::plugin::LiveStream;

const TICK: Duration = Duration::from_millis(200);
/// No bytes for this long means the stream stalled or the machine slept.
const IDLE_LIMIT: Duration = Duration::from_secs(65);
const SLICE_BYTES: usize = 32768;
/// A source timestamp this far behind the last one means its clock restarted.
const CLOCK_RESET_MS: u32 = 2000;
/// A source timestamp this far ahead of the last one means frames were skipped.
const CLOCK_GAP_MS: u32 = 65_000;
const MIN_FRAGMENT_BYTES: u64 = 65_536;
const MAX_FRAGMENT_BYTES: u64 = 2 * 1024 * 1024 * 1024;
const MAX_FRAGMENT_SECONDS: u64 = 86_400;
/// How far past the byte limit the playable copy may run while waiting for a boundary.
const BOUNDARY_SLACK_BYTES: u64 = 8 * 1024 * 1024;

/// Whether the capture goes on after a step.
enum Flow {
    Continue,
    Stop,
}

pub(super) async fn record_flv(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
) -> Result<bool, CoreError> {
    let rules = media_rules(core, job).await?;
    let mut response = core
        .http
        .open_media(&rules, &stream.url, &stream.public_headers, None)
        .await?;
    let raw = raw_flv(core, job, 0).await?;
    let mut capture = FlvCapture::new(core, job, stream, raw);
    let result = capture.pump(&mut response).await;
    capture.finalize(result).await
}

/// The state of one FLV capture: the raw fragment, the playable fragment being cut, and
/// the timeline both follow.
struct FlvCapture<'a> {
    core: &'a Core,
    job: &'a ClaimedJob,
    quality: &'a str,
    framer: flv::Framer,
    mux: flv_core::Muxer,
    /// The latest codec configuration of each kind, replayed at the start of every fragment.
    configs: Vec<flv_core::Tag>,
    raw: Option<Fragment>,
    part: Option<Fragment>,
    /// Raw captures the playable fragment was cut from.
    sources: Vec<String>,
    first: u32,
    last: u32,
    size: usize,
    started: Instant,
    raw_started: Instant,
    last_chunk: Instant,
    reason: &'static str,
    ended: bool,
    seconds: u64,
    byte_limit: u64,
}

impl<'a> FlvCapture<'a> {
    fn new(core: &'a Core, job: &'a ClaimedJob, stream: &'a LiveStream, raw: Fragment) -> Self {
        let now = Instant::now();
        Self {
            core,
            job,
            quality: &stream.quality,
            framer: flv::Framer::default(),
            mux: flv_core::Muxer::new(),
            configs: vec![],
            raw: Some(raw),
            part: None,
            sources: vec![],
            first: 0,
            last: 0,
            size: 0,
            started: now,
            raw_started: now,
            last_chunk: now,
            reason: "stream_disconnected",
            ended: false,
            seconds: core
                .profile
                .recording_fragment_seconds
                .clamp(1, MAX_FRAGMENT_SECONDS),
            byte_limit: core
                .profile
                .recording_fragment_bytes
                .clamp(MIN_FRAGMENT_BYTES, MAX_FRAGMENT_BYTES),
        }
    }

    /// Reads the response until it ends, a stop is requested or something fails.
    async fn pump(&mut self, response: &mut reqwest::Response) -> Result<(), CoreError> {
        let mut tick = tokio::time::interval(TICK);
        loop {
            let flow = tokio::select! {
                _ = tick.tick() => self.check_lifecycle().await?,
                value = response.chunk() => match value {
                    Ok(Some(chunk)) => {
                        self.core.http.received_media(chunk.len())?;
                        self.accept(&chunk).await?
                    }
                    Ok(None) => Flow::Stop,
                    Err(_) => {
                        return Err(CoreError::Network("recording transport interrupted".into()));
                    }
                },
            };
            if let Flow::Stop = flow {
                return Ok(());
            }
        }
    }

    async fn check_lifecycle(&mut self) -> Result<Flow, CoreError> {
        match state(self.core, self.job).await {
            Ok(s) if s == "stopping" => {
                self.reason = "requested_stop";
                self.ended = true;
                return Ok(Flow::Stop);
            }
            Err(_) => {
                self.reason = "lifecycle_interrupted";
                return Ok(Flow::Stop);
            }
            _ => {}
        }
        if let Err(e) = self.job.heartbeat(&self.core.db).await {
            self.reason = "lifecycle_interrupted";
            return Err(e);
        }
        if self.last_chunk.elapsed() > IDLE_LIMIT {
            self.reason = "idle_or_sleep_gap";
            return Ok(Flow::Stop);
        }
        Ok(Flow::Continue)
    }

    fn reserve_disk(&mut self, bytes: usize) -> Result<(), CoreError> {
        if disk_budget(self.core, bytes).is_err() {
            self.reason = "disk_budget_exhausted";
            return Err(CoreError::StorageFull("recording reserve reached".into()));
        }
        Ok(())
    }

    /// Keeps the chunk in the raw capture and cuts its frames into the playable fragment.
    async fn accept(&mut self, chunk: &[u8]) -> Result<Flow, CoreError> {
        let (core, job) = (self.core, self.job);
        if self.last_chunk.elapsed() > IDLE_LIMIT {
            self.reason = "idle_or_sleep_gap";
            return Ok(Flow::Stop);
        }
        self.last_chunk = Instant::now();
        core.objects
            .budget
            .observe(&job.subject_id, chunk.len(), None);
        self.reserve_disk(chunk.len())?;
        self.write_raw(chunk).await?;
        for slice in chunk.chunks(SLICE_BYTES) {
            let Ok(tags) = self.framer.push(slice) else {
                self.reason = "invalid_flv_frame";
                return Ok(Flow::Stop);
            };
            for tag in tags {
                self.write_tag(tag).await?;
            }
        }
        if self.size as u64 > self.byte_limit + BOUNDARY_SLACK_BYTES {
            self.reason = "no_rotation_boundary";
            return Ok(Flow::Stop);
        }
        self.rotate_raw_if_due().await?;
        Ok(Flow::Continue)
    }

    async fn write_raw(&mut self, chunk: &[u8]) -> Result<(), CoreError> {
        if self.raw.is_none() {
            self.raw = Some(raw_flv(self.core, self.job, self.last as i64).await?);
            self.raw_started = Instant::now();
        }
        let raw = self.raw.as_mut().unwrap();
        if !self.sources.contains(&raw.media) {
            self.sources.push(raw.media.clone());
        }
        raw.writer.write_chunk(&self.core.db, &raw.op, chunk).await
    }

    async fn rotate_raw_if_due(&mut self) -> Result<(), CoreError> {
        let due = self.raw.as_ref().is_some_and(|r| {
            r.writer.written_len() >= self.byte_limit
                || self.raw_started.elapsed() >= Duration::from_secs(self.seconds)
        });
        if due {
            let raw = self.raw.take().unwrap();
            let tail = self.framer.tail_len();
            finish_raw_flv(self.core, self.job, raw, self.last, "rotation", tail).await?;
        }
        Ok(())
    }

    async fn write_tag(&mut self, tag: flv_core::Tag) -> Result<(), CoreError> {
        let timed = matches!(
            tag.tag_type,
            flv_core::TagType::Audio | flv_core::TagType::Video
        ) && !flv::is_config(&tag);
        let clock_reset = timed && tag.timestamp_ms.saturating_add(CLOCK_RESET_MS) < self.last;
        let clock_gap = timed
            && self.part.is_some()
            && tag.timestamp_ms > self.last.saturating_add(CLOCK_GAP_MS);
        let changed = flv::is_config(&tag)
            && self
                .configs
                .iter()
                .any(|t| t.tag_type == tag.tag_type && t.data != tag.data);
        let boundary = flv::is_boundary(&tag, self.framer.has_video());
        let rotate = self.fragment_due(&tag) && boundary;
        if clock_reset || clock_gap || changed || rotate {
            let why = if clock_reset {
                "source_clock_reset"
            } else if clock_gap {
                "source_timestamp_gap"
            } else if changed {
                "codec_configuration_changed"
            } else {
                "rotation"
            };
            self.cut(why, clock_reset || clock_gap || changed).await?;
            if clock_reset {
                self.last = tag.timestamp_ms;
            }
        }
        if flv::is_config(&tag) {
            self.configs.retain(|t| t.tag_type != tag.tag_type);
            self.configs.push(tag.clone());
        }
        if self.part.is_none() {
            if !boundary {
                return Ok(());
            }
            self.open_part(&tag).await?;
        }
        self.reserve_disk(tag.data.len())?;
        let mut normalized = tag.clone();
        normalized.timestamp_ms = tag.timestamp_ms.saturating_sub(self.first);
        let mut bytes = vec![];
        self.mux
            .write_tag(&normalized, &mut bytes)
            .map_err(|_| invalid("FLV mux frame"))?;
        self.append(&bytes).await?;
        if timed {
            self.last = tag.timestamp_ms;
        }
        Ok(())
    }

    /// Time, source duration or size says the playable fragment has run long enough.
    fn fragment_due(&self, tag: &flv_core::Tag) -> bool {
        self.started.elapsed() >= Duration::from_secs(self.seconds)
            || u64::from(tag.timestamp_ms.saturating_sub(self.first)) >= self.seconds * 1000
            || self.size as u64 >= self.byte_limit
    }

    /// Seals the playable fragment; a discontinuity also leaves an explicit gap row.
    async fn cut(&mut self, why: &str, discontinuity: bool) -> Result<(), CoreError> {
        if let Some(part) = self.part.take() {
            let range = (self.first, self.last);
            finish_flv(
                self.core,
                self.job,
                part,
                range,
                why,
                &self.sources,
                self.quality,
            )
            .await?;
        }
        if discontinuity {
            let source = format!("flv-gap:{}", uuid::Uuid::new_v4());
            gap(self.core, self.job, source, why).await?;
        }
        self.sources = vec![self.raw.as_ref().unwrap().media.clone()];
        self.size = 0;
        self.started = Instant::now();
        Ok(())
    }

    /// Starts a playable fragment at the tag, led by the stream header and configurations.
    async fn open_part(&mut self, tag: &flv_core::Tag) -> Result<(), CoreError> {
        let source = format!("flv:{}", uuid::Uuid::new_v4());
        self.part = begin(
            self.core,
            self.job,
            source,
            "video/x-flv",
            tag.timestamp_ms as i64,
        )
        .await?;
        self.first = tag.timestamp_ms;
        self.started = Instant::now();
        self.size = 0;
        let mut header = vec![];
        self.mux.write_header(
            self.framer.has_audio(),
            self.framer.has_video(),
            &mut header,
        );
        for config in &self.configs {
            let mut config = config.clone();
            config.timestamp_ms = 0;
            self.mux
                .write_tag(&config, &mut header)
                .map_err(|_| invalid("FLV configuration"))?;
        }
        self.append(&header).await
    }

    async fn append(&mut self, bytes: &[u8]) -> Result<(), CoreError> {
        if let Some(part) = &mut self.part {
            part.writer
                .write_chunk(&self.core.db, &part.op, bytes)
                .await?;
            self.size += bytes.len();
        }
        Ok(())
    }

    /// Finalizes both independently: a failure of the raw reference commit must not drop
    /// the already buffered playable prefix, and vice versa.
    async fn finalize(mut self, capture: Result<(), CoreError>) -> Result<bool, CoreError> {
        if capture.is_err() && self.reason == "stream_disconnected" {
            self.reason = "stream_write_failed";
        }
        let (core, job, reason) = (self.core, self.job, self.reason);
        let raw_result = match self.raw.take() {
            Some(raw) => {
                let tail = self.framer.tail_len();
                finish_raw_flv(core, job, raw, self.last, reason, tail).await
            }
            None => Ok(()),
        };
        let media_result = match self.part.take() {
            Some(part) => {
                let range = (self.first, self.last);
                finish_flv(core, job, part, range, reason, &self.sources, self.quality).await
            }
            None => Ok(()),
        };
        let gap_result = if self.ended {
            Ok(())
        } else {
            gap(
                core,
                job,
                format!("flv-gap:{}", uuid::Uuid::new_v4()),
                reason,
            )
            .await
        };
        capture?;
        raw_result?;
        media_result?;
        gap_result?;
        Ok(self.ended)
    }
}

async fn raw_flv(core: &Core, job: &ClaimedJob, start: i64) -> Result<Fragment, CoreError> {
    let raw = begin(
        core,
        job,
        format!("raw-flv:{}", uuid::Uuid::new_v4()),
        "application/octet-stream",
        start,
    )
    .await?
    .ok_or_else(|| invalid("raw capture identity"))?;
    let media = raw.media.clone();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE media_items SET role='recording_source',required=0 WHERE media_id=?1",
                [media],
            )?;
            Ok(())
        })
        .await?;
    Ok(raw)
}

async fn finish_raw_flv(
    core: &Core,
    job: &ClaimedJob,
    raw: Fragment,
    last: u32,
    reason: &str,
    tail: usize,
) -> Result<(), CoreError> {
    let bytes = raw.writer.written_len();
    let evidence = json!({
        "protocol": "flv",
        "original_capture": true,
        "byte_count": bytes,
        "boundary": "exact_received_bytes",
        "incomplete_tail_bytes": tail,
    });
    finish(core, job, raw, last as i64, Some(reason), evidence).await
}

async fn finish_flv(
    core: &Core,
    job: &ClaimedJob,
    part: Fragment,
    source_range: (u32, u32),
    reason: &str,
    sources: &[String],
    quality: &str,
) -> Result<(), CoreError> {
    let evidence = json!({
        "protocol": "flv",
        "quality": quality,
        "source_media_ids": sources,
        "transformation": "frame_boundary_mux_timestamp_zero",
        "source_start_ms": source_range.0,
    });
    finish(
        core,
        job,
        part,
        source_range.1 as i64,
        Some(reason),
        evidence,
    )
    .await
}
