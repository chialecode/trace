//! Optional RFC 8216 WebVTT tracks. All fetching uses the media proxy; FFprobe
//! only sees bounded local bytes. Failure is independent from A/V coverage.
use super::fragment::{begin, finish, gap};
use super::hls_source::{SUBTITLE_MAP, Source, load_progress, resume_point, save_progress};
use super::{hls, invalid, state};
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::transport::HostRules;
use serde_json::json;
use std::time::Duration;
use trace_contracts::plugin::LiveStream;
use url::Url;

/// Subtitle work must not delay the recording it accompanies.
const BUDGET: Duration = Duration::from_secs(5);
const MAX_TRACKS: usize = 4;
const MAX_SEGMENTS_PER_TRACK: u32 = 4;
const PLAYLIST_LIMIT: usize = 512 * 1024;
const SEGMENT_LIMIT: usize = 1024 * 1024;
const MAX_VTT_BYTES: usize = 2 * 1024 * 1024;

pub(super) async fn capture(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
    tracks: &[m3u8_rs::AlternativeMedia],
    base: &Url,
) -> Result<(), CoreError> {
    if !enabled(core, job).await? {
        return Ok(());
    }
    let result = tokio::time::timeout(BUDGET, acquire(core, job, stream, tracks, base)).await;
    let evidence = match result {
        Ok(Ok(saved)) => json!({
            "state": if tracks.is_empty() { "no_external_track" } else { "bounded_capture" },
            "tracks_offered": tracks.len(),
            "tracks_visited": tracks.len().min(MAX_TRACKS),
            "segments_saved": saved,
        }),
        Ok(Err(error)) => {
            json!({"state": "failed", "failure_class": format!("{:?}", error.failure_class())})
        }
        Err(_) => json!({"state": "failed", "failure_class": "budget_timeout"}),
    };
    let j = job.clone();
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_sessions SET evidence=json_set(evidence,'$.subtitle_capture',json(?2)) WHERE session_id=?1",
                rusqlite::params![j.subject_id, evidence.to_string()],
            )?;
            Ok(())
        })
        .await
}

/// Whether the subscription asked for subtitles.
async fn enabled(core: &Core, job: &ClaimedJob) -> Result<bool, CoreError> {
    let session = job.subject_id.clone();
    core.db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT s.capture_subtitles FROM subscriptions s JOIN live_sessions l ON l.subscription_id=s.subscription_id WHERE l.session_id=?1",
                [session],
                |r| r.get(0),
            )?)
        })
        .await
}

/// What capturing one track achieved.
enum Track {
    /// A stop was requested; the count is the segments saved in this track.
    Stopped(usize),
    Done(usize),
}

/// What became of one subtitle segment.
enum Segment {
    Stopped,
    Saved,
    /// Already stored elsewhere, so nothing new was written.
    Skipped,
}

async fn acquire(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
    tracks: &[m3u8_rs::AlternativeMedia],
    base: &Url,
) -> Result<usize, CoreError> {
    let rules = super::media_rules(core, job).await?;
    let mut saved = 0;
    for track in tracks.iter().take(MAX_TRACKS) {
        match capture_track(core, job, stream, &rules, track, base).await? {
            Track::Stopped(n) => return Ok(saved + n),
            Track::Done(n) => saved += n,
        }
    }
    Ok(saved)
}

async fn capture_track(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
    rules: &HostRules,
    track: &m3u8_rs::AlternativeMedia,
    base: &Url,
) -> Result<Track, CoreError> {
    let uri = base
        .join(
            track
                .uri
                .as_deref()
                .ok_or_else(|| invalid("HLS subtitle URI missing"))?,
        )
        .map_err(|_| invalid("HLS subtitle URI"))?;
    let (bytes, track_base) = core
        .http
        .live_resource(
            rules,
            uri.as_str(),
            &stream.public_headers,
            None,
            PLAYLIST_LIMIT,
        )
        .await?;
    let hls::Playlist::MediaPlaylist(p) = hls::parse(&bytes)? else {
        return Err(invalid("nested subtitle master"));
    };
    let track_id =
        crate::config::short_hash(&json!([track.group_id, track.name, track.language]).to_string());
    let field = format!("subtitle_{track_id}");
    let prior = load_progress(core, job, &field).await?;
    let resume = resume_point(&prior, p.media_sequence);
    if resume
        .last
        .is_some_and(|n| p.media_sequence > n.saturating_add(1))
    {
        let source = format!(
            "subtitle-gap:{track_id}:{}:{}",
            resume.epoch, p.media_sequence
        );
        gap(core, job, source, "subtitle_window_missing").await?;
    }
    let src = Source {
        core,
        job,
        stream,
        rules,
        base: &track_base,
    };
    let (mut last, mut saved, mut taken) = (resume.last, 0, 0);
    for seg in hls::segments(&p)? {
        if last.is_some_and(|n| seg.sequence <= n) {
            continue;
        }
        if taken >= MAX_SEGMENTS_PER_TRACK {
            break;
        }
        taken += 1;
        let source = format!(
            "subtitle:{track_id}:{}:{}:{}",
            resume.epoch, seg.discontinuity, seg.sequence
        );
        match segment(&src, track, &track_id, &seg, source).await? {
            Segment::Stopped => return Ok(Track::Stopped(saved)),
            Segment::Saved => saved += 1,
            Segment::Skipped => {}
        }
        last = Some(seg.sequence);
    }
    let progress = json!({"first": p.media_sequence, "last": last, "epoch": resume.epoch});
    save_progress(core, job, &field, &progress).await?;
    Ok(Track::Done(saved))
}

async fn stop_requested(core: &Core, job: &ClaimedJob) -> Result<bool, CoreError> {
    Ok(matches!(
        state(core, job).await?.as_str(),
        "stopping" | "sealed"
    ))
}

async fn segment(
    src: &Source<'_>,
    track: &m3u8_rs::AlternativeMedia,
    track_id: &str,
    seg: &hls::Segment,
    source: String,
) -> Result<Segment, CoreError> {
    let (core, job) = (src.core, src.job);
    if stop_requested(core, job).await? {
        return Ok(Segment::Stopped);
    }
    if seg.source_gap {
        gap(core, job, source, "subtitle_source_gap").await?;
        return Ok(Segment::Skipped);
    }
    let url = src
        .base
        .join(&seg.uri)
        .map_err(|_| invalid("subtitle segment URI"))?;
    let (raw, _) = src.fetch(url.as_str(), seg.range, SEGMENT_LIMIT).await?;
    let mut originals = vec![src.preserve(format!("raw:{source}"), &raw).await?];
    let mut bytes = src.plain(raw, seg.key.as_ref(), seg.sequence).await?;
    let init = src
        .init_part(seg, &SUBTITLE_MAP, SEGMENT_LIMIT, format!("map:{source}"))
        .await?;
    if let Some(init) = init {
        originals.push(init.original);
        let mut joined = init.plain;
        joined.push(b'\n');
        joined.extend(bytes);
        bytes = joined;
    }
    let webvtt = std::str::from_utf8(&bytes)
        .is_ok_and(|s| s.trim_start_matches('\u{feff}').starts_with("WEBVTT"));
    if bytes.len() > MAX_VTT_BYTES || !webvtt {
        return Err(invalid("invalid WebVTT"));
    }
    if stop_requested(core, job).await? {
        return Ok(Segment::Stopped);
    }
    let Some(mut fragment) = begin(core, job, source, "text/vtt", 0).await? else {
        return Ok(Segment::Skipped);
    };
    label_subtitle(core, job, &fragment.media, track).await?;
    fragment
        .writer
        .write_chunk(&core.db, &fragment.op, &bytes)
        .await?;
    let evidence = json!({
        "protocol": "hls_webvtt",
        "track": track_id,
        "language": track.language,
        "sequence": seg.sequence,
        "discontinuity": seg.discontinuity,
        "source_media_ids": originals,
        "timeline": "webvtt_local_and_timestamp_map",
    });
    finish(core, job, fragment, seg.duration_ms, None, evidence).await?;
    Ok(Segment::Saved)
}

/// The fragment is a subtitle resource, optional and titled by its language.
async fn label_subtitle(
    core: &Core,
    job: &ClaimedJob,
    media: &str,
    track: &m3u8_rs::AlternativeMedia,
) -> Result<(), CoreError> {
    let (j, media) = (job.clone(), media.to_owned());
    let title = track.language.clone().unwrap_or_else(|| track.name.clone());
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE media_items SET role='subtitle',required=0,title=?2 WHERE media_id=?1",
                rusqlite::params![media, title],
            )?;
            Ok(())
        })
        .await
}
