//! HLS recording: each playlist track is captured segment by segment, with an explicit gap
//! row wherever the source skipped, restarted or slid past a segment.
use super::fragment::{begin, disk_budget, finish, gap};
use super::hls_source::{HLS_MAP, Resume, Source, load_progress, resume_point, save_progress};
use super::{RESOURCE_LIMIT, hls, invalid, media_rules, state, subtitles};
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::transport::HostRules;
use serde_json::json;
use trace_contracts::plugin::LiveStream;
use url::Url;

const PLAYLIST_LIMIT: usize = 512 * 1024;
/// New segments fetched per pass; the rest wait for the next poll.
const SEGMENTS_PER_PASS: u32 = 64;

/// One playlist to capture: the main video, or an alternative audio rendition.
struct Track {
    name: String,
    base: Url,
    playlist: hls::MediaPlaylist,
}

/// The tracks of a stream and its offered subtitle renditions.
struct Plan {
    tracks: Vec<Track>,
    subtitles: Vec<m3u8_rs::AlternativeMedia>,
    subtitle_base: Url,
}

/// What capturing one track achieved.
enum Outcome {
    /// A stop was requested while capturing.
    Stopping,
    /// Whether the playlist ended and every segment was captured.
    Finished(bool),
}

/// Where the capture of a track stands.
struct Cursor {
    clock: i64,
    last: Option<u64>,
    acquired: u32,
}

/// Whether to go on to the next segment.
enum Step {
    Next,
    PassLimit,
}

/// Everything the capture of one track needs.
struct Capture<'a> {
    src: Source<'a>,
    track: &'a str,
    epoch: u64,
}

pub(super) async fn record_hls(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
) -> Result<bool, CoreError> {
    let rules = media_rules(core, job).await?;
    let plan = plan_tracks(core, &rules, stream).await?;
    let mut ended = true;
    for track in plan.tracks {
        match capture_track(core, job, stream, &rules, track).await? {
            Outcome::Stopping => return Ok(true),
            Outcome::Finished(finished) => ended &= finished,
        }
    }
    subtitles::capture(core, job, stream, &plan.subtitles, &plan.subtitle_base).await?;
    Ok(ended)
}

async fn fetch_playlist(
    core: &Core,
    rules: &HostRules,
    stream: &LiveStream,
    url: &str,
) -> Result<(Vec<u8>, Url), CoreError> {
    core.http
        .live_resource(rules, url, &stream.public_headers, None, PLAYLIST_LIMIT)
        .await
}

/// Reads the stream's playlist; a master playlist yields its best variant, its audio
/// rendition (required when the variant names a group) and the subtitle renditions.
async fn plan_tracks(
    core: &Core,
    rules: &HostRules,
    stream: &LiveStream,
) -> Result<Plan, CoreError> {
    let (bytes, base) = fetch_playlist(core, rules, stream, &stream.url).await?;
    let subtitle_base = base.clone();
    let master = match hls::parse(&bytes)? {
        hls::Playlist::MediaPlaylist(playlist) => {
            return Ok(Plan {
                tracks: vec![Track {
                    name: "main".into(),
                    base,
                    playlist,
                }],
                subtitles: vec![],
                subtitle_base,
            });
        }
        hls::Playlist::MasterPlaylist(master) => master,
    };
    let variant = master
        .variants
        .iter()
        .filter(|v| !v.is_i_frame)
        .max_by_key(|v| v.bandwidth)
        .ok_or_else(|| invalid("HLS master has no variant"))?;
    let mut uris = vec![("main".to_owned(), variant.uri.clone())];
    if let Some(group) = &variant.audio {
        let audio = master
            .alternatives
            .iter()
            .filter(|a| {
                a.media_type == m3u8_rs::AlternativeMediaType::Audio && &a.group_id == group
            })
            .max_by_key(|a| a.default)
            .ok_or_else(|| invalid("HLS required audio missing"))?;
        if let Some(uri) = &audio.uri {
            uris.push(("audio".into(), uri.clone()));
        }
    }
    let subtitles = variant
        .subtitles
        .as_ref()
        .map(|group| {
            master
                .alternatives
                .iter()
                .filter(|a| {
                    a.media_type == m3u8_rs::AlternativeMediaType::Subtitles && &a.group_id == group
                })
                .cloned()
                .collect()
        })
        .unwrap_or_default();
    let mut tracks = vec![];
    for (name, uri) in uris {
        let uri = base.join(&uri).map_err(|_| invalid("HLS variant URI"))?;
        let (bytes, track_base) = fetch_playlist(core, rules, stream, uri.as_str()).await?;
        let hls::Playlist::MediaPlaylist(playlist) = hls::parse(&bytes)? else {
            return Err(invalid("nested HLS master depth"));
        };
        tracks.push(Track {
            name,
            base: track_base,
            playlist,
        });
    }
    Ok(Plan {
        tracks,
        subtitles,
        subtitle_base,
    })
}

async fn capture_track(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
    rules: &HostRules,
    track: Track,
) -> Result<Outcome, CoreError> {
    let p = &track.playlist;
    let field = format!("hls_{}", track.name);
    let prior = load_progress(core, job, &field).await?;
    let resume = resume_point(&prior, p.media_sequence);
    record_window_gaps(core, job, &track, &resume).await?;
    let segments = hls::segments(p)?;
    let final_sequence = segments.last().map(|s| s.sequence);
    let capture = Capture {
        src: Source {
            core,
            job,
            stream,
            rules,
            base: &track.base,
        },
        track: &track.name,
        epoch: resume.epoch,
    };
    let mut cursor = Cursor {
        clock: track_clock(core, job, &track.name).await?,
        last: resume.last,
        acquired: 0,
    };
    let mut limited = false;
    for seg in segments {
        if state(core, job).await? == "stopping" {
            return Ok(Outcome::Stopping);
        }
        job.heartbeat(&core.db).await?;
        if let Step::PassLimit = capture.segment(&seg, &mut cursor).await? {
            limited = true;
            break;
        }
    }
    let progress = json!({"first": p.media_sequence, "last": cursor.last, "epoch": resume.epoch});
    save_progress(core, job, &field, &progress).await?;
    Ok(Outcome::Finished(
        p.end_list && !limited && cursor.last == final_sequence,
    ))
}

/// A restarted media sequence and segments that slid out of the window are discontinuities.
async fn record_window_gaps(
    core: &Core,
    job: &ClaimedJob,
    track: &Track,
    resume: &Resume,
) -> Result<(), CoreError> {
    let (name, epoch) = (&track.name, resume.epoch);
    if resume.reset {
        let source = format!("reset:{name}:{epoch}");
        gap(core, job, source, "media_sequence_reset").await?;
    }
    if let Some(last) = resume.prior_last
        && track.playlist.media_sequence > last + 1
    {
        let source = format!("window:{name}:{epoch}:{}", track.playlist.media_sequence);
        gap(core, job, source, "segments_slid_out_of_window").await?;
    }
    Ok(())
}

/// Where the track's timeline stands: the end of its latest sealed or gap fragment.
async fn track_clock(core: &Core, job: &ClaimedJob, track: &str) -> Result<i64, CoreError> {
    let (session, track) = (job.subject_id.clone(), track.to_owned());
    core.db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT COALESCE(MAX(end_ms),0) FROM live_fragments WHERE session_id=?1 AND state IN ('sealed','gap') AND json_extract(evidence,'$.track')=?2",
                rusqlite::params![session, track],
                |r| r.get(0),
            )?)
        })
        .await
}

impl Capture<'_> {
    async fn segment(&self, seg: &hls::Segment, cursor: &mut Cursor) -> Result<Step, CoreError> {
        let source = format!(
            "hls:{}:{}:{}:{}",
            self.track, self.epoch, seg.discontinuity, seg.sequence
        );
        if seg.source_gap {
            self.declared_gap(seg, source, cursor).await?;
            return Ok(Step::Next);
        }
        if let Some(end) = self.sealed_end(&source).await? {
            cursor.clock = end;
            cursor.last = Some(seg.sequence);
            return Ok(Step::Next);
        }
        if cursor.acquired >= SEGMENTS_PER_PASS {
            return Ok(Step::PassLimit);
        }
        cursor.acquired += 1;
        self.acquire(seg, source, cursor).await?;
        Ok(Step::Next)
    }

    /// End of the sealed fragment already stored for the source sequence, if any.
    async fn sealed_end(&self, source: &str) -> Result<Option<i64>, CoreError> {
        self.end_of(
            source,
            "SELECT end_ms FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state='sealed'",
        )
        .await
    }

    /// End of the sealed or gap fragment already stored for the source sequence, if any.
    async fn settled_end(&self, source: &str) -> Result<Option<i64>, CoreError> {
        self.end_of(
            source,
            "SELECT end_ms FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state IN ('gap','sealed') AND end_ms IS NOT NULL",
        )
        .await
    }

    async fn end_of(&self, source: &str, sql: &'static str) -> Result<Option<i64>, CoreError> {
        use rusqlite::OptionalExtension;
        let (session, source) = (self.src.job.subject_id.clone(), source.to_owned());
        self.src
            .core
            .db
            .read(move |c| {
                Ok(
                    c.query_row(sql, rusqlite::params![session, source], |r| r.get(0))
                        .optional()?,
                )
            })
            .await
    }

    /// The source declared the segment missing: a gap row spanning its duration.
    async fn declared_gap(
        &self,
        seg: &hls::Segment,
        source: String,
        cursor: &mut Cursor,
    ) -> Result<(), CoreError> {
        let (core, job) = (self.src.core, self.src.job);
        if let Some(end) = self.settled_end(&source).await? {
            cursor.clock = end;
            cursor.last = Some(seg.sequence);
            return Ok(());
        }
        gap(core, job, source.clone(), "source_declared_gap").await?;
        let end = cursor.clock.saturating_add(seg.duration_ms);
        let (session, start) = (job.subject_id.clone(), cursor.clock);
        let evidence =
            json!({"track": self.track, "timeline": "captured_source_duration"}).to_string();
        core.db
            .write(move |tx| {
                tx.execute(
                    "UPDATE live_fragments SET start_ms=COALESCE(start_ms,?3),end_ms=COALESCE(end_ms,?4),evidence=?5 WHERE session_id=?1 AND source_sequence=?2 AND state='gap'",
                    rusqlite::params![session, source, start, end, evidence],
                )?;
                Ok(())
            })
            .await?;
        cursor.clock = end;
        cursor.last = Some(seg.sequence);
        Ok(())
    }

    /// Fetches the segment, keeps the original response, and seals the playable bytes.
    async fn acquire(
        &self,
        seg: &hls::Segment,
        source: String,
        cursor: &mut Cursor,
    ) -> Result<(), CoreError> {
        let (core, job, stream) = (self.src.core, self.src.job, self.src.stream);
        disk_budget(core, RESOURCE_LIMIT)?;
        let url = self
            .src
            .base
            .join(&seg.uri)
            .map_err(|_| invalid("HLS segment URI"))?;
        let (bytes, _) = self
            .src
            .fetch(url.as_str(), seg.range, RESOURCE_LIMIT)
            .await?;
        core.objects.budget.observe(
            &job.subject_id,
            bytes.len(),
            Some(seg.duration_ms.max(1) as u64),
        );
        let original = self.src.preserve(format!("raw:{source}"), &bytes).await?;
        let bytes = self
            .src
            .plain(bytes, seg.key.as_ref(), seg.sequence)
            .await?;
        let mime = if seg.map.is_some() {
            "video/mp4"
        } else {
            "video/mp2t"
        };
        let Some(mut fragment) = begin(core, job, source, mime, cursor.clock).await? else {
            return Ok(());
        };
        let mut source_media = vec![original];
        let map_source = format!(
            "raw-map:{}:{}:{}:{}",
            self.track, self.epoch, seg.discontinuity, seg.sequence
        );
        if let Some(init) = self
            .src
            .init_part(seg, &HLS_MAP, RESOURCE_LIMIT, map_source)
            .await?
        {
            source_media.push(init.original);
            fragment
                .writer
                .write_chunk(&core.db, &fragment.op, &init.plain)
                .await?;
        }
        fragment
            .writer
            .write_chunk(&core.db, &fragment.op, &bytes)
            .await?;
        cursor.clock = cursor.clock.saturating_add(seg.duration_ms);
        let encrypted = seg
            .key
            .as_ref()
            .is_some_and(|k| k.method == m3u8_rs::KeyMethod::AES128);
        let evidence = json!({
            "protocol": "hls",
            "track": self.track,
            "quality": stream.quality,
            "sequence": seg.sequence,
            "discontinuity": seg.discontinuity,
            "source_media_ids": source_media,
            "transformation": "authorized_decrypt_and_initialization_prefix",
            "timeline": "captured_source_duration",
            "encrypted": encrypted,
        });
        finish(core, job, fragment, cursor.clock, None, evidence).await?;
        cursor.last = Some(seg.sequence);
        Ok(())
    }
}
