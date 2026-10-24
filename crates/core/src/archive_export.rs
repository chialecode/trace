//! Lossless, bounded export from one database snapshot. Files precede the
//! completion manifest; interrupted exports remain visibly incomplete.
use crate::{core::Core, error::CoreError, storage_ops};
use rusqlite::{Connection, params};
use serde_json::{Value, json};
use std::{
    fs::File,
    io::{BufWriter, Write},
    path::Path,
};

struct Snapshot(std::path::PathBuf);
impl Drop for Snapshot {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}

fn json_file(path: &Path, value: &Value) -> Result<(), CoreError> {
    let mut out = BufWriter::new(File::create(path)?);
    serde_json::to_writer_pretty(&mut out, value)?;
    out.flush()?;
    out.get_ref().sync_all()?;
    Ok(())
}
fn json_line(out: &mut BufWriter<File>, value: &Value) -> Result<(), CoreError> {
    serde_json::to_writer(&mut *out, value)?;
    out.write_all(b"\n")?;
    Ok(())
}
fn safe_json(s: String) -> Value {
    trace_contracts::content::safe_metadata(&serde_json::from_str(&s).unwrap_or(Value::Null))
}

pub async fn export(
    core: &Core,
    target: &Path,
    include_media: bool,
    creator: Option<&str>,
) -> Result<Value, CoreError> {
    export_guarded(core, target, include_media, creator, None).await
}

const SELECT_ENTRY_REVISIONS: &str = "SELECT rowid,revision_id FROM entry_revisions WHERE entry_id=?1 AND rowid>?2 ORDER BY rowid LIMIT 32";

pub(crate) async fn export_guarded(
    core: &Core,
    target: &Path,
    include_media: bool,
    creator: Option<&str>,
    guard: Option<crate::jobs::ClaimedJob>,
) -> Result<Value, CoreError> {
    crate::paths::no_links(target)?;
    if target.exists() && std::fs::read_dir(target)?.next().is_some() {
        return Err(CoreError::InvalidInput(
            "export target must be empty".into(),
        ));
    }
    std::fs::create_dir_all(target)?;
    let _gate = core.archive_access.read().await;
    let temporary = core
        .profile
        .data_root
        .join(format!("export-snapshot-{}.sqlite3", uuid::Uuid::new_v4()));
    storage_ops::snapshot_db(&core.db, &temporary).await?;
    let snapshot = Snapshot(temporary);
    let mut conn =
        Connection::open_with_flags(&snapshot.0, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    json_file(
        &target.join("incomplete.json"),
        &json!({"state":"writing","export_schema":2}),
    )?;
    let mut index = BufWriter::new(File::create(target.join("entries.jsonl"))?);
    let mut cursor = String::new();
    let mut count = 0u64;
    let mut media_count = 0u64;
    loop {
        let rows = entry_page(&conn, creator, &cursor)?;
        if rows.is_empty() {
            break;
        }
        for (entry, title) in rows {
            cursor = entry.clone();
            // Prefix by full ID digest: titles may collide, differ only by case,
            // or be invalid Windows filenames. Source IDs never become paths.
            use sha2::Digest;
            let digest: String = sha2::Sha256::digest(entry.as_bytes())
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect();
            let name = format!(
                "{}-{}",
                digest,
                storage_ops::safe_filename(&title, "untitled")
            );
            let dir = target.join(&name);
            std::fs::create_dir(&dir)?;
            let mut current = storage_ops::generate_manifest(&conn, &entry)?
                .ok_or_else(|| CoreError::Internal("snapshot revision missing".into()))?;
            media_count += materialize(core, &dir, &mut current, include_media).await?;
            json_file(&dir.join("archive.json"), &current)?;
            std::fs::create_dir(dir.join("revisions"))?;
            let mut revision_after = 0i64;
            loop {
                let revisions = conn
                    .prepare(SELECT_ENTRY_REVISIONS)?
                    .query_map(params![entry, revision_after], |r| {
                        Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?))
                    })?
                    .collect::<Result<Vec<_>, _>>()?;
                if revisions.is_empty() {
                    break;
                }
                for (rowid, revision) in revisions {
                    revision_after = rowid;
                    let mut manifest =
                        storage_ops::revision_manifest(&conn, &entry, Some(&revision))?
                            .ok_or_else(|| {
                                CoreError::Internal("snapshot history missing".into())
                            })?;
                    media_count += materialize(core, &dir, &mut manifest, include_media).await?;
                    json_file(
                        &dir.join("revisions").join(format!("{rowid}.json")),
                        &manifest,
                    )?;
                    tokio::task::yield_now().await;
                }
            }
            observations(&conn, &entry, &dir)?;
            json_line(
                &mut index,
                &json!({"entry_id":entry,"title":title,"folder":name}),
            )?;
            count += 1;
            tokio::task::yield_now().await;
        }
    }
    index.flush()?;
    index.get_ref().sync_all()?;
    let accounts = accounts(&mut conn, target, creator).await?;
    let report = json!({"export_schema":2,"created_at":crate::jobs::now(),"include_media":include_media,"entry_count":count,"account_count":accounts,"copied_media":media_count,"entries":"entries.jsonl","accounts":"accounts.jsonl","credentials":"not-included","state":"complete"});
    json_file(&target.join("export.json.tmp"), &report)?;
    let target = target.to_path_buf();
    core.db
        .write(move |tx| {
            if let Some(job) = guard {
                job.check(tx)?;
            }
            std::fs::rename(target.join("export.json.tmp"), target.join("export.json"))?;
            std::fs::remove_file(target.join("incomplete.json"))?;
            Ok(())
        })
        .await?;
    Ok(report)
}

fn entry_page(
    conn: &Connection,
    creator: Option<&str>,
    after: &str,
) -> Result<Vec<(String, String)>, CoreError> {
    Ok(conn.prepare("SELECT e.entry_id,r.title FROM entries e JOIN current_entry_revisions r ON r.entry_id=e.entry_id WHERE e.lifecycle!='deleted' AND e.entry_id>?2 AND (?1 IS NULL OR EXISTS(SELECT 1 FROM account_bindings a WHERE a.account_id=e.account_id AND a.creator_id=?1)) ORDER BY e.entry_id LIMIT 64")?.query_map(params![creator,after],|r|Ok((r.get(0)?,r.get(1)?)))?.collect::<Result<Vec<_>,_>>()?)
}

async fn materialize(
    core: &Core,
    dir: &Path,
    manifest: &mut Value,
    include: bool,
) -> Result<u64, CoreError> {
    let mut count = 0;
    for m in manifest["media"]
        .as_array_mut()
        .ok_or_else(|| CoreError::Internal("manifest media".into()))?
    {
        m["export_state"] = json!(if include { "missing" } else { "metadata_only" });
        if !include {
            continue;
        }
        let Some(blob) = m["blob_id"].as_str() else {
            continue;
        };
        let sha = crate::paths::blob_sha(blob)?;
        let src = core.objects.object_path(sha)?;
        if !src.exists() {
            continue;
        }
        let name = format!("media/{sha}{}", extension(m["mime"].as_str().unwrap_or("")));
        let dst = dir.join(&name);
        if !dst.exists() {
            std::fs::create_dir_all(dir.join("media"))?;
            // Async file IO yields during large copies, so lifecycle cancellation
            // can leave an incomplete folder without publishing success.
            tokio::fs::copy(src, &dst).await?;
            count += 1;
        }
        let path = dst.clone();
        let expected = sha.to_owned();
        let size = m["size"].as_u64();
        let verified = tokio::task::spawn_blocking(move || {
            crate::objects::hash_file(&path).is_ok_and(|v| v == expected)
                && path.metadata().is_ok_and(|v| Some(v.len()) == size)
        })
        .await
        .map_err(|_| CoreError::Internal("export verification task".into()))?;
        if !verified {
            return Err(CoreError::InvalidInput(
                "export object checksum mismatch".into(),
            ));
        }
        m["export_state"] = json!("verified");
        m["export_file"] = json!(name);
    }
    Ok(count)
}

pub(crate) fn extension(mime: &str) -> &'static str {
    match mime {
        "image/png" => ".png",
        "image/jpeg" => ".jpg",
        "image/webp" => ".webp",
        "image/gif" => ".gif",
        "image/avif" => ".avif",
        "video/mp4" | "audio/mp4" => ".mp4",
        "video/x-flv" => ".flv",
        "video/mp2t" => ".ts",
        "video/webm" => ".webm",
        "audio/mpeg" => ".mp3",
        "audio/aac" => ".aac",
        "audio/ogg" => ".ogg",
        "audio/flac" => ".flac",
        "audio/wav" | "audio/x-wav" => ".wav",
        "application/json" | "application/vnd.trace.subtitle+json" => ".json",
        "text/vtt" => ".vtt",
        "application/x-subrip" => ".srt",
        _ => ".bin",
    }
}

const SELECT_RESOURCE_ACQUISITIONS: &str = "SELECT a.acquisition_id,x.context_id,x.auth_state,x.validated_at,x.basis,x.started_at,a.finished_at,a.media_id,a.variant_id,a.outcome,a.measured_spec FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE x.entry_id=?1 ORDER BY a.sequence";
const SELECT_SOURCE_OBSERVATIONS: &str = "SELECT observation_seq,access_state,observed_at,revision_id,evidence FROM source_observations WHERE entry_id=?1 ORDER BY observation_seq";
const SELECT_LIVE_MESSAGES: &str = "SELECT m.sequence,m.source_id,m.source_time,m.author,m.body,m.kind,m.observed_at FROM live_messages m JOIN live_sessions s ON s.session_id=m.session_id WHERE s.entry_id=?1 ORDER BY m.sequence";
const SELECT_LIVE_AUXILIARY_SAMPLES: &str = "SELECT a.observed_at,a.coverage,a.source_count,a.saved_count,a.omitted,a.failure_class FROM live_auxiliary_samples a JOIN live_sessions s ON s.session_id=a.session_id WHERE s.entry_id=?1 ORDER BY a.sequence";
const SELECT_LIVE_SESSIONS: &str = "SELECT s.session_id,s.room_key,s.source_session,s.state,s.started_at,s.ended_at,s.stop_reason,s.evidence,f.sequence,f.source_sequence,f.media_id,f.state,f.start_ms,f.end_ms,f.discontinuity,f.reason,f.evidence FROM live_sessions s LEFT JOIN live_fragments f ON f.session_id=s.session_id WHERE s.entry_id=?1 ORDER BY s.session_id,f.sequence";

fn observations(conn: &Connection, entry: &str, dir: &Path) -> Result<(), CoreError> {
    let mut acquisitions = BufWriter::new(File::create(dir.join("acquisitions.jsonl"))?);
    let mut statement = conn.prepare(SELECT_RESOURCE_ACQUISITIONS)?;
    let mut acquisition_rows = statement.query([entry])?;
    while let Some(r) = acquisition_rows.next()? {
        json_line(
            &mut acquisitions,
            &json!({
                "acquisition_id": r.get::<_, String>(0)?,
                "context_id": r.get::<_, String>(1)?,
                "authentication": r.get::<_, String>(2)?,
                "validated_at": r.get::<_, Option<String>>(3)?,
                "basis": r.get::<_, String>(4)?,
                "started_at": r.get::<_, String>(5)?,
                "finished_at": r.get::<_, String>(6)?,
                "media_id": r.get::<_, Option<String>>(7)?,
                "variant_id": r.get::<_, Option<String>>(8)?,
                "outcome": r.get::<_, String>(9)?,
                "measured_spec": safe_json(r.get(10)?),
            }),
        )?;
    }
    acquisitions.flush()?;
    let mut out = BufWriter::new(File::create(dir.join("observations.jsonl"))?);
    let mut stmt = conn.prepare(SELECT_SOURCE_OBSERVATIONS)?;
    let mut rows = stmt.query([entry])?;
    while let Some(r) = rows.next()? {
        json_line(
            &mut out,
            &json!({
                "sequence": r.get::<_, i64>(0)?,
                "access_state": r.get::<_, String>(1)?,
                "observed_at": r.get::<_, String>(2)?,
                "revision_id": r.get::<_, Option<String>>(3)?,
                "evidence": safe_json(r.get(4)?),
            }),
        )?;
    }
    out.flush()?;
    let mut chat = BufWriter::new(File::create(dir.join("live-chat.jsonl"))?);
    let mut stmt = conn.prepare(SELECT_LIVE_MESSAGES)?;
    let mut rows = stmt.query([entry])?;
    while let Some(r) = rows.next()? {
        json_line(
            &mut chat,
            &json!({
                "sequence": r.get::<_, i64>(0)?,
                "source_id": r.get::<_, String>(1)?,
                "source_time": r.get::<_, Option<String>>(2)?,
                "author": r.get::<_, Option<String>>(3)?,
                "text": r.get::<_, String>(4)?,
                "kind": r.get::<_, String>(5)?,
                "observed_at": r.get::<_, String>(6)?,
            }),
        )?;
    }
    chat.flush()?;
    let mut samples = BufWriter::new(File::create(dir.join("live-chat-samples.jsonl"))?);
    let mut stmt = conn.prepare(SELECT_LIVE_AUXILIARY_SAMPLES)?;
    let mut rows = stmt.query([entry])?;
    while let Some(r) = rows.next()? {
        json_line(
            &mut samples,
            &json!({
                "observed_at": r.get::<_, String>(0)?,
                "coverage": r.get::<_, String>(1)?,
                "source_count": r.get::<_, i64>(2)?,
                "saved_count": r.get::<_, i64>(3)?,
                "omitted": r.get::<_, i64>(4)?,
                "failure_class": r.get::<_, Option<String>>(5)?,
            }),
        )?;
    }
    samples.flush()?;
    let mut out = BufWriter::new(File::create(dir.join("live.jsonl"))?);
    let mut stmt = conn.prepare(SELECT_LIVE_SESSIONS)?;
    let mut rows = stmt.query([entry])?;
    while let Some(r) = rows.next()? {
        json_line(
            &mut out,
            &json!({
                "session_id": r.get::<_, String>(0)?,
                "room": r.get::<_, String>(1)?,
                "source_session": r.get::<_, String>(2)?,
                "state": r.get::<_, String>(3)?,
                "started_at": r.get::<_, String>(4)?,
                "ended_at": r.get::<_, Option<String>>(5)?,
                "stop_reason": r.get::<_, Option<String>>(6)?,
                "evidence": safe_json(r.get(7)?),
                "fragment": {
                    "sequence": r.get::<_, Option<i64>>(8)?,
                    "source_sequence": r.get::<_, Option<String>>(9)?,
                    "media_id": r.get::<_, Option<String>>(10)?,
                    "state": r.get::<_, Option<String>>(11)?,
                    "start_ms": r.get::<_, Option<i64>>(12)?,
                    "end_ms": r.get::<_, Option<i64>>(13)?,
                    "discontinuity": r.get::<_, Option<i64>>(14)?,
                    "reason": r.get::<_, Option<String>>(15)?,
                    "evidence": safe_json(r.get::<_, Option<String>>(16)?.unwrap_or_default()),
                    "acquisition": r
                        .get::<_, Option<i64>>(8)?
                        .map(|seq| {
                            let session = r.get::<_, String>(0).unwrap_or_default();
                            crate::acquisitions::fragment(conn, &session, seq)
                        })
                        .transpose()?,
                },
            }),
        )?;
    }
    out.flush()?;
    Ok(())
}

const SELECT_SOURCE_ACCOUNTS: &str = "SELECT account_id,namespace,native_account_key,display_name,profile_url FROM source_accounts a WHERE account_id>?2 AND (?1 IS NULL OR EXISTS(SELECT 1 FROM account_bindings b WHERE b.account_id=a.account_id AND b.creator_id=?1)) ORDER BY account_id LIMIT 32";

async fn accounts(
    conn: &mut Connection,
    target: &Path,
    creator: Option<&str>,
) -> Result<u64, CoreError> {
    let mut out = BufWriter::new(File::create(target.join("accounts.jsonl"))?);
    let mut history = BufWriter::new(File::create(target.join("account-observations.jsonl"))?);
    let mut after = String::new();
    let mut count = 0;
    loop {
        let accounts = conn
            .prepare(SELECT_SOURCE_ACCOUNTS)?
            .query_map(params![creator, after], |r| {
                Ok(json!({
                    "account_id": r.get::<_, String>(0)?,
                    "namespace": r.get::<_, String>(1)?,
                    "native_account_key": r.get::<_, String>(2)?,
                    "display_name": r.get::<_, Option<String>>(3)?,
                    "profile_url": r.get::<_, Option<String>>(4)?,
                }))
            })?
            .collect::<Result<Vec<_>, _>>()?;
        if accounts.is_empty() {
            break;
        }
        for account in accounts {
            after = account["account_id"].as_str().unwrap().to_owned();
            json_line(&mut out, &trace_contracts::content::safe_metadata(&account))?;
            {
                let mut stmt=conn.prepare("SELECT sequence,display_name,observed_at,evidence FROM account_observations WHERE account_id=?1 ORDER BY sequence")?;
                let mut rows = stmt.query([&after])?;
                while let Some(r) = rows.next()? {
                    json_line(
                        &mut history,
                        &json!({
                            "account_id": after,
                            "sequence": r.get::<_, i64>(0)?,
                            "display_name": r.get::<_, Option<String>>(1)?,
                            "observed_at": r.get::<_, String>(2)?,
                            "evidence": safe_json(r.get(3)?),
                        }),
                    )?;
                }
            }
            count += 1;
            tokio::task::yield_now().await;
        }
    }
    out.flush()?;
    history.flush()?;
    Ok(count)
}

#[cfg(test)]
const INSERT_SOURCE_ACCOUNTS: &str = "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','MS4export','t')";
#[cfg(test)]
const INSERT_ENTRIES: &str = "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','douyin','article','1','a','t')";
#[cfg(test)]
const INSERT_MEDIA_ITEMS: &str = "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('m','old','image',0,'image-original'),('missing','new','image',0,'new-image')";
#[cfg(test)]
const INSERT_BLOBS: &str =
    "INSERT INTO blobs(blob_id,size,mime,first_seen_at) VALUES(?1,?2,'image/png','t')";
#[cfg(test)]
const INSERT_REPLICAS: &str = "INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES('rp',?1,'local','primary','original','verified')";
#[cfg(test)]
const INSERT_LIVE_FRAGMENTS: &str = "INSERT INTO live_fragments(fragment_id,session_id,sequence,source_sequence,state,reason) VALUES('f','s',1,'missing-1','gap','source_window_missing')";
#[cfg(test)]
const INSERT_LIVE_MESSAGES: &str = "INSERT INTO live_messages(session_id,source_id,source_time,author,body,kind,observed_at) VALUES('s','90071992547409931','t','署名','离线采样正文','chat','t')";
#[cfg(test)]
const INSERT_LIVE_AUXILIARY_SAMPLES: &str = "INSERT INTO live_auxiliary_samples(session_id,observed_at,coverage,source_count,saved_count,omitted) VALUES('s','t','sampled_history',2,1,1)";
#[cfg(test)]
const INSERT_VARIANT_RETENTION: &str = "INSERT INTO variant_retention(variant_id,pinned,retained,reason,updated_at) VALUES('v',1,1,'user_pinned','t')";
#[cfg(test)]
const SELECT_LIVE_MESSAGES_2: &str =
    "SELECT body FROM live_messages WHERE source_id='90071992547409931'";
#[cfg(test)]
const SELECT_VARIANT_RETENTION: &str =
    "SELECT pinned=1 AND retained=1 FROM variant_retention WHERE variant_id='v'";
#[cfg(test)]
const SELECT_SUBSCRIPTIONS: &str =
    "SELECT state='paused' AND credential_id IS NULL FROM subscriptions WHERE subscription_id='s'";
#[cfg(test)]
const INSERT_ENTRY_REVISIONS: &str = "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?1,?1,'t','b','t')";
#[cfg(test)]
const INSERT_ENTRIES_2: &str = "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,'douyin','article',?1,'a','t')";

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn export_all_pages_versions_accounts_resources_and_failure_manifest() {
        let temp = tempfile::tempdir().unwrap();
        let core = Core::open(crate::config::Profile {
            archive_id: "export-test".into(),
            data_root: temp.path().join("data"),
            archive_root: temp.path().join("archive"),
            vault_dir: Some(temp.path().join("vault")),
            ..Default::default()
        })
        .await
        .unwrap();
        use sha2::Digest;
        let bytes = b"\x89PNG\r\n\x1a\nsynthetic-export-original";
        let sha: String = sha2::Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        let path = core.objects.object_path(&sha).unwrap();
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(&path, bytes).unwrap();
        let blob = format!("sha256:{sha}");
        core.db.write(move|tx|{
            tx.execute(INSERT_SOURCE_ACCOUNTS,[])?;
            tx.execute("INSERT INTO account_observations(account_id,display_name,observed_at,evidence) VALUES('a','旧名','t','{\"uid\":\"90071992547409931\",\"cookie\":\"export-secret\"}'),('a','新名','u','{}')",[])?;
            tx.execute(INSERT_ENTRIES,[])?;
            for (rev,body,stamp) in [("old","旧完整正文","t"),("new","编辑后的正文","u")]{tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,completeness,document_json,metadata_json) VALUES(?1,'e',?1,'CON',?2,?3,'full','{\"version\":1,\"blocks\":[]}','{}')",params![rev,body,stamp])?;}
            tx.execute("INSERT INTO source_observations(observation_id,entry_id,revision_id,access_state,observed_at,observation_seq,evidence) VALUES('o','e','old','available','t',1,'{\"statistics\":{\"count\":1}}'),('n','e','new','available','u',2,'{}')",[])?;
            tx.execute(INSERT_MEDIA_ITEMS,[])?;
            tx.execute(INSERT_BLOBS,params![blob,bytes.len() as i64])?;
            tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES('v','m','original',?1,'{}','{}','t')",[&blob])?;
            tx.execute(INSERT_REPLICAS,[&blob])?;
            tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','live','{}','t','t')",[])?;
            tx.execute("INSERT INTO live_sessions(session_id,subscription_id,room_key,source_session,entry_id,state,started_at,evidence) VALUES('s','s','1','source-session','e','ended','t','{}')",[])?;
            tx.execute(INSERT_LIVE_FRAGMENTS,[])?;
            tx.execute(INSERT_LIVE_MESSAGES,[])?;
            tx.execute(INSERT_LIVE_AUXILIARY_SAMPLES,[])?;
            tx.execute(INSERT_VARIANT_RETENTION,[])?;
            Ok(())
        }).await.unwrap();
        let target = temp.path().join("out");
        let result = export(&core, &target, true, None).await.unwrap();
        assert_eq!(result["entry_count"], 1);
        assert_eq!(result["account_count"], 1);
        assert_eq!(result["copied_media"], 1);
        assert!(!target.join("incomplete.json").exists());
        let item: Value = serde_json::from_str(
            std::fs::read_to_string(target.join("entries.jsonl"))
                .unwrap()
                .trim(),
        )
        .unwrap();
        let folder = target.join(item["folder"].as_str().unwrap());
        assert!(item["folder"].as_str().unwrap().ends_with("_CON"));
        let current: Value =
            serde_json::from_slice(&std::fs::read(folder.join("archive.json")).unwrap()).unwrap();
        assert_eq!(current["revision"]["body"], "编辑后的正文");
        assert_eq!(current["media"][0]["export_state"], "missing");
        let mut old = None;
        for file in std::fs::read_dir(folder.join("revisions")).unwrap() {
            let v: Value =
                serde_json::from_slice(&std::fs::read(file.unwrap().path()).unwrap()).unwrap();
            if v["revision"]["id"] == "old" {
                old = Some(v);
            }
        }
        let old = old.unwrap();
        assert_eq!(old["revision"]["body"], "旧完整正文");
        assert!(
            old["media"][0]["export_file"]
                .as_str()
                .unwrap()
                .ends_with(".png")
        );
        assert_eq!(
            std::fs::read(folder.join(old["media"][0]["export_file"].as_str().unwrap())).unwrap(),
            bytes
        );
        assert_eq!(
            std::fs::read_to_string(folder.join("observations.jsonl"))
                .unwrap()
                .lines()
                .count(),
            2
        );
        assert!(
            std::fs::read_to_string(folder.join("live.jsonl"))
                .unwrap()
                .contains("source_window_missing")
        );
        let chat = std::fs::read_to_string(folder.join("live-chat.jsonl")).unwrap();
        assert!(chat.contains("90071992547409931") && chat.contains("离线采样正文"));
        assert!(
            std::fs::read_to_string(folder.join("live-chat-samples.jsonl"))
                .unwrap()
                .contains("sampled_history")
        );
        // Both metadata-only and full backups preserve new tables and retention;
        // restore must fence inherited jobs and require new credentials.
        for include in [false, true] {
            let backups = temp.path().join(format!("backup-{include}"));
            let info = storage_ops::backup(&core, &backups, include).await.unwrap();
            let restored_dir = temp.path().join(format!("restore-{include}"));
            storage_ops::restore(
                &core,
                &backups.join(info["backup_id"].as_str().unwrap()),
                &restored_dir,
            )
            .await
            .unwrap();
            let mut profile: crate::config::Profile =
                serde_json::from_slice(&std::fs::read(restored_dir.join("profile.json")).unwrap())
                    .unwrap();
            profile.vault_dir = Some(restored_dir.join("private-vault"));
            let restored = Core::open(profile).await.unwrap();
            restored.recover().await.unwrap();
            restored
                .db
                .read(|c| {
                    assert_eq!(
                        c.query_row(SELECT_LIVE_MESSAGES_2, [], |r| r.get::<_, String>(0))?,
                        "离线采样正文"
                    );
                    assert_eq!(
                        c.query_row("SELECT omitted FROM live_auxiliary_samples", [], |r| r
                            .get::<_, i64>(0))?,
                        1
                    );
                    assert!(c.query_row(SELECT_VARIANT_RETENTION, [], |r| r.get::<_, bool>(0))?);
                    assert!(c.query_row(SELECT_SUBSCRIPTIONS, [], |r| r.get::<_, bool>(0))?);
                    assert_eq!(
                        c.query_row("SELECT count(*) FROM credentials_state", [], |r| r
                            .get::<_, i64>(0))?,
                        0
                    );
                    Ok(())
                })
                .await
                .unwrap();
            let copied = restored.objects.object_path(&sha).unwrap();
            assert_eq!(copied.exists(), include);
            if include {
                assert_eq!(std::fs::read(copied).unwrap(), bytes);
            }
            let again = restored_dir.join("export");
            export(&restored, &again, include, None).await.unwrap();
            assert_eq!(
                std::fs::read_to_string(
                    again
                        .join(item["folder"].as_str().unwrap())
                        .join("live-chat.jsonl")
                )
                .unwrap(),
                chat
            );
            restored.shutdown().await;
        }
        let history = std::fs::read_to_string(target.join("account-observations.jsonl")).unwrap();
        assert_eq!(history.lines().count(), 2);
        assert!(!history.contains("export-secret"));
        assert!(history.contains("90071992547409931"));
        assert!(
            export(&core, &target, true, None).await.is_err(),
            "existing export must remain intact"
        );
        std::fs::write(&path, b"damaged").unwrap();
        let broken = temp.path().join("broken");
        assert!(export(&core, &broken, true, None).await.is_err());
        assert!(!broken.join("export.json").exists());
        assert!(broken.join("incomplete.json").exists());
        // Exercise exactly the production keyset reader beyond the old 10k cap.
        core.db
            .write(|tx| {
                for n in 0..10005 {
                    let id = format!("large-{n:05}");
                    tx.execute(INSERT_ENTRIES_2, [&id])?;
                    tx.execute(INSERT_ENTRY_REVISIONS, [id])?;
                }
                let mut after = String::new();
                let mut ids = std::collections::HashSet::new();
                loop {
                    let page = entry_page(tx, None, &after)?;
                    if page.is_empty() {
                        break;
                    }
                    assert!(page.len() <= 64);
                    for (id, _) in page {
                        assert!(ids.insert(id.clone()));
                        after = id;
                    }
                }
                assert_eq!(ids.len(), 10006);
                Ok(())
            })
            .await
            .unwrap();
        core.shutdown().await;
    }
}
