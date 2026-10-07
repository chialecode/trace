//! Optional RFC 8216 WebVTT tracks. All fetching uses the media proxy; FFprobe
//! only sees bounded local bytes. Failure is independent from A/V coverage.
use super::*;

pub(super) async fn capture(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
    tracks: &[m3u8_rs::AlternativeMedia],
    base: &Url,
) -> Result<(), CoreError> {
    let j = job.clone();
    let enabled:bool=core.db.read(move|c|Ok(c.query_row("SELECT s.capture_subtitles FROM subscriptions s JOIN live_sessions l ON l.subscription_id=s.subscription_id WHERE l.session_id=?1",[j.subject_id],|r|r.get(0))?)).await?;
    if !enabled {
        return Ok(());
    }
    let result = tokio::time::timeout(
        Duration::from_secs(5),
        acquire(core, job, stream, tracks, base),
    )
    .await;
    let evidence = match result {
        Ok(Ok(saved)) => {
            json!({"state":if tracks.is_empty(){"no_external_track"}else{"bounded_capture"},"tracks_offered":tracks.len(),"tracks_visited":tracks.len().min(4),"segments_saved":saved})
        }
        Ok(Err(error)) => {
            json!({"state":"failed","failure_class":format!("{:?}",error.failure_class())})
        }
        Err(_) => json!({"state":"failed","failure_class":"budget_timeout"}),
    };
    let j = job.clone();
    core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_sessions SET evidence=json_set(evidence,'$.subtitle_capture',json(?2)) WHERE session_id=?1",rusqlite::params![j.subject_id,evidence.to_string()])?;Ok(())}).await
}

async fn acquire(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
    tracks: &[m3u8_rs::AlternativeMedia],
    base: &Url,
) -> Result<usize, CoreError> {
    let rules = host_rules(&core.profile);
    let mut saved = 0;
    for track in tracks.iter().take(4) {
        let uri = base
            .join(
                track
                    .uri
                    .as_deref()
                    .ok_or_else(|| invalid("HLS subtitle URI missing"))?,
            )
            .map_err(|_| invalid("HLS subtitle URI"))?;
        let (bytes, base) = get(core, &rules, uri.as_str(), stream, None, 512 * 1024).await?;
        let hls::Playlist::MediaPlaylist(p) = hls::parse(&bytes)? else {
            return Err(invalid("nested subtitle master"));
        };
        let track_id = crate::config::short_hash(
            &json!([track.group_id, track.name, track.language]).to_string(),
        );
        let field = format!("subtitle_{track_id}");
        let f = field.clone();
        let sid = job.subject_id.clone();
        let prior: Value = core
            .db
            .read(move |c| {
                let e: String = c.query_row(
                    "SELECT evidence FROM live_sessions WHERE session_id=?1",
                    [sid],
                    |r| r.get(0),
                )?;
                Ok(serde_json::from_str::<Value>(&e)?[&f].clone())
            })
            .await?;
        let mut epoch = prior["epoch"].as_u64().unwrap_or(0);
        if prior["first"]
            .as_u64()
            .is_some_and(|n| p.media_sequence < n)
        {
            epoch += 1;
        }
        let mut last = prior["last"]
            .as_u64()
            .filter(|_| epoch == prior["epoch"].as_u64().unwrap_or(0));
        if last.is_some_and(|n| p.media_sequence > n.saturating_add(1)) {
            gap(
                core,
                job,
                format!("subtitle-gap:{track_id}:{epoch}:{}", p.media_sequence),
                "subtitle_window_missing",
            )
            .await?;
        }
        let mut acquired = 0;
        for seg in hls::segments(&p)? {
            if last.is_some_and(|n| seg.sequence <= n) {
                continue;
            }
            if acquired >= 4 {
                break;
            }
            acquired += 1;
            if matches!(state(core, job).await?.as_str(), "stopping" | "sealed") {
                return Ok(saved);
            }
            let source = format!(
                "subtitle:{track_id}:{epoch}:{}:{}",
                seg.discontinuity, seg.sequence
            );
            if seg.source_gap {
                gap(core, job, source, "subtitle_source_gap").await?;
                last = Some(seg.sequence);
                continue;
            }
            let url = base
                .join(&seg.uri)
                .map_err(|_| invalid("subtitle segment URI"))?;
            let (raw, _) = get(core, &rules, url.as_str(), stream, seg.range, 1024 * 1024).await?;
            let original = preserve_source(core, job, format!("raw:{source}"), &raw).await?;
            let mut originals = vec![original];
            let mut bytes = plaintext(
                core,
                &rules,
                &base,
                stream,
                raw,
                seg.key.as_ref(),
                seg.sequence,
            )
            .await?;
            if let Some(map) = seg.map {
                if seg
                    .map_key
                    .as_ref()
                    .is_some_and(|k| k.method == m3u8_rs::KeyMethod::AES128 && k.iv.is_none())
                {
                    return Err(invalid("subtitle MAP needs IV"));
                }
                let url = base
                    .join(&map.uri)
                    .map_err(|_| invalid("subtitle MAP URI"))?;
                let range = map
                    .byte_range
                    .map(|r| {
                        r.offset
                            .map(|o| (o, r.length))
                            .ok_or_else(|| invalid("subtitle MAP range"))
                    })
                    .transpose()?;
                let (raw, _) = get(core, &rules, url.as_str(), stream, range, 1024 * 1024).await?;
                originals.push(preserve_source(core, job, format!("map:{source}"), &raw).await?);
                let mut header = plaintext(
                    core,
                    &rules,
                    &base,
                    stream,
                    raw,
                    seg.map_key.as_ref(),
                    seg.sequence,
                )
                .await?;
                header.push(b'\n');
                header.extend(bytes);
                bytes = header;
            }
            if bytes.len() > 2 * 1024 * 1024
                || !std::str::from_utf8(&bytes)
                    .is_ok_and(|s| s.trim_start_matches('\u{feff}').starts_with("WEBVTT"))
            {
                return Err(invalid("invalid WebVTT"));
            }
            if matches!(state(core, job).await?.as_str(), "stopping" | "sealed") {
                return Ok(saved);
            }
            if let Some(mut fragment) = begin(core, job, source, "text/vtt", 0).await? {
                let mid = fragment.media.clone();
                let j = job.clone();
                let title = track.language.clone().unwrap_or_else(|| track.name.clone());
                core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE media_items SET role='subtitle',required=0,title=?2 WHERE media_id=?1",rusqlite::params![mid,title])?;Ok(())}).await?;
                fragment
                    .writer
                    .write_chunk(&core.db, &fragment.op, &bytes)
                    .await?;
                finish(core,job,fragment,seg.duration_ms,None,json!({"protocol":"hls_webvtt","track":track_id,"language":track.language,"sequence":seg.sequence,"discontinuity":seg.discontinuity,"source_media_ids":originals,"timeline":"webvtt_local_and_timestamp_map"})).await?;
                saved += 1;
            }
            last = Some(seg.sequence);
        }
        let j = job.clone();
        let progress = json!({"first":p.media_sequence,"last":last,"epoch":epoch});
        core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_sessions SET evidence=json_set(evidence,?2,json(?3)) WHERE session_id=?1",rusqlite::params![j.subject_id,format!("$.{field}"),progress.to_string()])?;Ok(())}).await?;
    }
    Ok(saved)
}
