//! 导出、一致备份与恢复（docs/design.md §14，C20/C21/C23）。
//!
//! - 备份：SQLite 在线备份生成一致快照 → 从快照枚举对象集合 → 复制校验
//!   → 最后发布完成清单（manifest-last）。
//! - 恢复：仅空隔离目标；校验 schema/路径/哈希/关系；凭证不随备份，
//!   恢复后显示需重新登录。
//! - 导出：可读目录 + JSON 清单；Windows 保留名与重名处理；不携带凭证。

use crate::config::Profile;
use crate::db::DbHandle;
use crate::error::CoreError;
use std::io::Read;
use std::path::Path;
use trace_domain::time::{TimePrecision, TraceTime};

fn now() -> String {
    TraceTime::now(TimePrecision::Discovery).rfc3339
}

fn hash_file(path: &Path) -> Result<String, CoreError> {
    use sha2::Digest as _;
    let mut f = std::fs::File::open(path)?;
    let mut h = sha2::Sha256::new();
    let mut buf = vec![0u8; 256 * 1024];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(h.finalize().iter().map(|b| format!("{b:02x}")).collect())
}

/// 归档清单（§6.5）：内容事实快照，不含本机路径/凭证/签名 URL。
pub fn generate_manifest(
    c: &rusqlite::Connection,
    entry_id: &str,
) -> Result<Option<serde_json::Value>, CoreError> {
    let row = c
        .query_row(
            "SELECT e.namespace, e.native_kind, e.native_entry_key, a.namespace, a.native_account_key,
                    r.revision_id, r.title, r.normalized_body, r.observed_at, e.published_at, e.published_precision
             FROM entries e
             JOIN entry_revisions r ON r.entry_id = e.entry_id
             JOIN source_accounts a ON a.account_id = e.account_id
             WHERE e.entry_id = ?1
             ORDER BY r.observed_at DESC LIMIT 1",
            [entry_id],
            |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, String>(2)?,
                    r.get::<_, String>(3)?,
                    r.get::<_, String>(4)?,
                    r.get::<_, String>(5)?,
                    r.get::<_, String>(6)?,
                    r.get::<_, String>(7)?,
                    r.get::<_, String>(8)?,
                    r.get::<_, Option<String>>(9)?,
                    r.get::<_, Option<String>>(10)?,
                ))
            },
        )
        .ok();
    let Some((ns, kind, key, _ans, _ak, rev_id, title, body, observed_at, published_at, precision)) =
        row
    else {
        return Ok(None);
    };
    let mut stmt = c.prepare(
        "SELECT mi.role, mi.position, mi.source_attachment_id, mv.source_representation, b.blob_id, b.size,
                mv.claimed_spec, mv.measured_spec
         FROM media_items mi
         LEFT JOIN media_variants mv ON mv.media_id = mi.media_id
         LEFT JOIN blobs b ON b.blob_id = mv.blob_id
         WHERE mi.revision_id = ?1
         ORDER BY mi.position",
    )?;
    let media: Vec<serde_json::Value> = stmt
        .query_map([rev_id.as_str()], |r| {
            Ok(serde_json::json!({
                "role": r.get::<_, String>(0)?,
                "position": r.get::<_, i64>(1)?,
                "source_attachment_id": r.get::<_, String>(2)?,
                "representation": r.get::<_, Option<String>>(3)?,
                "blob_id": r.get::<_, Option<String>>(4)?,
                "size": r.get::<_, Option<i64>>(5)?,
                "claimed_spec": r.get::<_, Option<String>>(6)?.and_then(|s| serde_json::from_str::<Value>(&s).ok()).unwrap_or(Value::Null),
                "measured_spec": r.get::<_, Option<String>>(7)?.and_then(|s| serde_json::from_str::<Value>(&s).ok()).unwrap_or(Value::Null),
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(Some(serde_json::json!({
        "manifest_schema": 1,
        "entry": {"namespace": ns, "kind": kind, "native_key": key},
        "revision": {"id": rev_id, "title": title, "body": body, "observed_at": observed_at},
        "published_at": published_at,
        "published_precision": precision,
        "media": media,
    })))
}

/// 生成并落盘当前清单（entry.revision.created 的派生步骤）。
pub async fn write_manifests(
    db: &DbHandle,
    profile: &Profile,
    limit: i64,
) -> Result<i64, CoreError> {
    let entries: Vec<String> = db
        .read(move |c| {
            let mut stmt = c.prepare(
                "SELECT DISTINCT json_extract(payload,'$.revision_id') IS NOT NULL, aggregate_id
                 FROM outbox WHERE topic='entry.revision.created' AND processed_at IS NULL LIMIT ?1",
            )?;
            // 第一列仅用于兼容旧数据；实际取 aggregate_id。
            let rows = stmt
                .query_map([limit], |r| r.get::<_, String>(1))?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .await?;
    let mut written = 0i64;
    for entry_id in entries {
        let e = entry_id.clone();
        let manifest: Option<serde_json::Value> = db
            .write(move |tx| {
                let m = generate_manifest(tx, &e)?;
                tx.execute(
                    "UPDATE outbox SET processed_at=?2 WHERE seq IN
                     (SELECT seq FROM outbox WHERE topic='entry.revision.created' AND aggregate_id=?1 AND processed_at IS NULL)",
                    rusqlite::params![e, now()],
                )?;
                Ok(m)
            })
            .await?;
        if let Some(m) = manifest {
            let dir = profile.manifests_dir().join(&entry_id);
            std::fs::create_dir_all(&dir)?;
            let body = serde_json::to_vec_pretty(&m)?;
            use sha2::Digest as _;
            let mut hasher = sha2::Sha256::new();
            hasher.update(&body);
            let hex: String = hasher
                .finalize()
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect();
            let path = dir.join(format!("{hex}.json"));
            std::fs::write(&path, &body)?;
            written += 1;
        }
    }
    Ok(written)
}

/// 元数据一致备份（SQLite 在线备份 API）+ 可选完整媒体。
pub async fn backup(
    core: &crate::core::Core,
    target_root: &Path,
    include_media: bool,
) -> Result<serde_json::Value, CoreError> {
    let backup_id = format!("bk-{}", uuid::Uuid::new_v4());
    let dir = target_root.join(&backup_id);
    std::fs::create_dir_all(&dir)?;
    // 1) 一致数据库快照。
    let snapshot_path = dir.join("catalog.sqlite3");
    snapshot_db(&core.db, &snapshot_path).await?;
    let db_sha = hash_file(&snapshot_path)?;
    // 2) 从快照枚举对象集合（不从变化中的主库猜测）。
    let wanted: Vec<(String, i64)> = {
        let conn = rusqlite::Connection::open(&snapshot_path)?;
        let mut stmt = conn.prepare(
            "SELECT DISTINCT b.blob_id, b.size FROM blobs b
             JOIN media_variants mv ON mv.blob_id = b.blob_id",
        )?;

        stmt.query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?)))?
            .collect::<Result<Vec<_>, _>>()?
    };
    // 3) 复制对象（可选媒体）。
    let mut objects = Vec::new();
    if include_media {
        std::fs::create_dir_all(dir.join("objects"))?;
        for (blob_id, size) in &wanted {
            let sha = crate::paths::blob_sha(blob_id)?;
            let src = core.objects.object_path(sha)?;
            if !src.exists() {
                objects.push(serde_json::json!({"blob_id": blob_id, "size": size, "included": false, "reason": "missing"}));
                continue;
            }
            let dst = dir.join("objects").join(sha);
            std::fs::copy(&src, &dst)?;
            let copied_sha = hash_file(&dst)?;
            objects.push(serde_json::json!({
                "blob_id": blob_id, "size": size, "included": copied_sha == sha,
            }));
        }
    }
    // 4) manifest-last：最后发布完成清单（原子改名）。
    let manifest = serde_json::json!({
        "backup_schema": 1,
        "backup_id": backup_id,
        "created_at": now(),
        "archive_id": core.profile.archive_id,
        "include_media": include_media,
        "db_sha256": db_sha,
        "objects": objects,
        "credentials": "not-included",
    });
    let tmp = dir.join("manifest.json.tmp");
    std::fs::write(&tmp, serde_json::to_vec_pretty(&manifest)?)?;
    std::fs::rename(&tmp, dir.join("manifest.json"))?;
    Ok(manifest)
}

async fn snapshot_db(db: &DbHandle, target: &Path) -> Result<(), CoreError> {
    // 经 actor 线程执行在线备份（rusqlite backup API），保证一致性。
    let target = target.to_path_buf();
    db.read(move |src| {
        let mut dst = rusqlite::Connection::open(&target)?;
        let backup = rusqlite::backup::Backup::new(src, &mut dst)?;
        backup.run_to_completion(64, std::time::Duration::from_millis(5), None)?;
        Ok(())
    })
    .await
}

/// 恢复到空隔离目标；坏备份/兼容性失败拒绝并保留原档案。
pub async fn restore(
    _core: &crate::core::Core,
    backup_root: &Path,
    target_root: &Path,
) -> Result<serde_json::Value, CoreError> {
    use crate::paths::{blob_sha, no_links, sha256};
    no_links(backup_root)?;
    no_links(target_root)?;
    let manifest_path = backup_root.join("manifest.json");
    no_links(&manifest_path)?;
    let manifest: serde_json::Value = serde_json::from_slice(&std::fs::read(&manifest_path)?)?;
    if manifest["backup_schema"].as_i64() != Some(1) {
        return Err(CoreError::InvalidInput("unsupported backup schema".into()));
    }
    if target_root.exists() && std::fs::read_dir(target_root)?.next().is_some() {
        return Err(CoreError::InvalidInput(
            "restore target must be empty".into(),
        ));
    }
    // Validate all untrusted identities before any destination write, even omitted objects.
    let objs = manifest["objects"]
        .as_array()
        .ok_or_else(|| CoreError::InvalidInput("objects missing".into()))?;
    let mut included = std::collections::HashMap::new();
    for o in objs {
        let digest = blob_sha(o["blob_id"].as_str().unwrap_or_default())?;
        let size = o["size"]
            .as_i64()
            .filter(|s| *s >= 0)
            .ok_or_else(|| CoreError::InvalidInput("invalid size".into()))?;
        let present = o["included"]
            .as_bool()
            .ok_or_else(|| CoreError::InvalidInput("included missing".into()))?;
        if included
            .insert(digest.to_owned(), (size, present))
            .is_some()
        {
            return Err(CoreError::InvalidInput("duplicate object".into()));
        }
    }
    let src_db = backup_root.join("catalog.sqlite3");
    no_links(&src_db)?;
    if hash_file(&src_db)? != sha256(manifest["db_sha256"].as_str().unwrap_or_default())? {
        return Err(CoreError::InvalidInput("db checksum mismatch".into()));
    }
    let source =
        rusqlite::Connection::open_with_flags(&src_db, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    let version: String = source.query_row(
        "SELECT value FROM meta WHERE key='schema_version'",
        [],
        |r| r.get(0),
    )?;
    if version
        .parse::<i64>()
        .ok()
        .filter(|v| *v > 0 && *v <= trace_domain::SCHEMA_VERSION)
        .is_none()
    {
        return Err(CoreError::InvalidInput(
            "unsupported database schema".into(),
        ));
    }
    let integrity: String = source.query_row("PRAGMA integrity_check", [], |r| r.get(0))?;
    if integrity != "ok" || source.prepare("PRAGMA foreign_key_check")?.exists([])? {
        return Err(CoreError::InvalidInput(
            "invalid database relationships".into(),
        ));
    }
    let wanted: Vec<(String, i64)> = source
        .prepare("SELECT blob_id,size FROM blobs")?
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<_, _>>()?;
    let mut verified = Vec::new();
    for (blob, size) in &wanted {
        let digest = blob_sha(blob)?;
        if *size < 0 {
            return Err(CoreError::InvalidInput("invalid blob size".into()));
        }
        if let Some((declared, true)) = included.remove(digest) {
            if declared != *size {
                return Err(CoreError::InvalidInput("manifest size mismatch".into()));
            }
            let src = backup_root.join("objects").join(digest);
            no_links(&src)?;
            if src.exists() {
                if src.metadata()?.len() != *size as u64 || hash_file(&src)? != digest {
                    return Err(CoreError::InvalidInput("object checksum mismatch".into()));
                }
                verified.push((blob.clone(), src));
            }
        }
    }
    if !included.is_empty() {
        return Err(CoreError::InvalidInput(
            "manifest object absent from catalog".into(),
        ));
    }
    drop(source);
    std::fs::create_dir_all(target_root)?;
    let target = std::fs::canonicalize(target_root)?;
    let profile = Profile {
        archive_id: format!("restored-{}", uuid::Uuid::new_v4()),
        data_root: target.join("data"),
        archive_root: target.join("archive"),
        ..Profile::default()
    };
    std::fs::create_dir_all(profile.db_path().parent().unwrap())?;
    std::fs::copy(&src_db, profile.db_path())?;
    // Destination copy is also verified; never trust a stale preflight digest.
    if hash_file(&profile.db_path())? != manifest["db_sha256"].as_str().unwrap_or_default() {
        return Err(CoreError::InvalidInput(
            "copied db checksum mismatch".into(),
        ));
    }
    let mut conn = rusqlite::Connection::open(profile.db_path())?;
    let tx = conn.transaction()?;
    tx.execute("DELETE FROM credentials_state", [])?;
    tx.execute(
        "UPDATE subscriptions SET credential_id=NULL, state='paused'",
        [],
    )?;
    tx.execute("DELETE FROM materializations", [])?;
    tx.execute("DELETE FROM receipts", [])?;
    tx.execute("DELETE FROM plugins_state", [])?;
    tx.execute("UPDATE jobs SET state='cancelled', lease_owner=NULL, lease_expires_at=NULL WHERE state NOT IN ('succeeded','cancelled','failed')", [])?;
    tx.execute(
        "UPDATE replicas SET state='absent',verified_at=NULL,locator=substr(blob_id,8)",
        [],
    )?;
    for (blob, src) in &verified {
        let digest = blob_sha(blob)?;
        let dst = crate::paths::object(&profile.objects_dir(), digest)?;
        std::fs::create_dir_all(dst.parent().unwrap())?;
        std::fs::copy(src, &dst)?;
        if hash_file(&dst)? != digest {
            return Err(CoreError::InvalidInput(
                "copied object checksum mismatch".into(),
            ));
        }
        tx.execute("INSERT OR REPLACE INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at) VALUES(?1,?2,'local','primary',?3,1,'verified',?4)", rusqlite::params![format!("restored-{digest}"),blob,digest,now()])?;
    }
    tx.commit()?;
    let profile_path = target.join("profile.json");
    std::fs::write(&profile_path, serde_json::to_vec_pretty(&profile)?)?;
    Ok(serde_json::json!({
        "restored_objects": verified.len(),
        "missing_objects": wanted.len() - verified.len(),
        "profile_path": profile_path,
        "credentials": "require-relogin",
    }))
}

/// 导出：可读目录 + 清单；重名/保留名处理；缺媒体明示。
pub async fn export(
    core: &crate::core::Core,
    target_dir: &Path,
    include_media: bool,
    creator_id: Option<&str>,
) -> Result<serde_json::Value, CoreError> {
    std::fs::create_dir_all(target_dir)?;
    let mut used_names: std::collections::HashSet<String> = std::collections::HashSet::new();
    let rows: Vec<(String, String)> = {
        let creator = creator_id.map(|s| s.to_string());
        core.db
            .read(move |c| {
                let mut stmt = c.prepare(
                    "SELECT e.entry_id, r.title FROM entries e
                     JOIN entry_revisions r ON r.entry_id = e.entry_id
                     WHERE (?1 IS NULL OR EXISTS (
                        SELECT 1 FROM account_bindings ab WHERE ab.account_id = e.account_id AND ab.creator_id = ?1))
                     ORDER BY e.entry_id LIMIT 10000",
                )?;
                let rows = stmt
                    .query_map(rusqlite::params![creator], |r| Ok((r.get(0)?, r.get(1)?)))?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows)
            })
            .await?
    };
    let mut items = Vec::new();
    for (entry_id, title) in &rows {
        let e = entry_id.clone();
        let manifest: Option<serde_json::Value> =
            core.db.read(move |c| generate_manifest(c, &e)).await?;
        let Some(m) = manifest else { continue };
        let base = safe_filename(title, entry_id);
        let mut name = base.clone();
        let mut i = 1;
        while used_names.contains(&name.to_lowercase()) {
            i += 1;
            name = format!("{base} ({i})");
        }
        used_names.insert(name.to_lowercase());
        let dir = target_dir.join(&name);
        std::fs::create_dir_all(&dir)?;
        std::fs::write(dir.join("archive.json"), serde_json::to_vec_pretty(&m)?)?;
        if include_media {
            std::fs::create_dir_all(dir.join("media"))?;
            if let Some(media) = m["media"].as_array() {
                for item in media {
                    if let Some(blob) = item["blob_id"].as_str() {
                        let sha = crate::paths::blob_sha(blob)?;
                        let src = core.objects.object_path(sha)?;
                        if src.exists() {
                            let ext = ext_for(item["representation"].as_str().unwrap_or("video"));
                            std::fs::copy(&src, dir.join("media").join(format!("{sha}{ext}")))?;
                        }
                    }
                }
            }
        }
        items.push(serde_json::json!({"entry_id": entry_id, "title": title, "folder": name}));
    }
    let report = serde_json::json!({
        "export_schema": 1,
        "created_at": now(),
        "include_media": include_media,
        "items": items,
        "credentials": "not-included",
    });
    std::fs::write(
        target_dir.join("export.json"),
        serde_json::to_vec_pretty(&report)?,
    )?;
    Ok(report)
}

fn ext_for(_repr: &str) -> &'static str {
    ".mp4"
}

/// Windows 安全文件名：保留名、非法字符、穿越全部中和（C21/C15）。
pub fn safe_filename(title: &str, fallback: &str) -> String {
    const RESERVED: [&str; 22] = [
        "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8",
        "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
    ];
    let mut s: String = title
        .chars()
        .map(|c| match c {
            '/' | '\\' | ':' | '*' | '?' | '"' | '<' | '>' | '|' | '\u{0}'..='\u{1f}' => ' ',
            c => c,
        })
        .collect();
    s = s.trim().trim_end_matches(['.', ' ']).to_string();
    if s.is_empty() {
        s = fallback.to_string();
    }
    if s.len() > 80 {
        s = s.chars().take(80).collect();
    }
    let upper = s.to_ascii_uppercase();
    if RESERVED.contains(&upper.as_str())
        || upper
            .split('.')
            .next()
            .map(|p| RESERVED.contains(&p))
            .unwrap_or(false)
    {
        s = format!("_{s}");
    }
    s
}

/// 存储用量统计。
pub async fn usage(core: &crate::core::Core) -> Result<serde_json::Value, CoreError> {
    let db_bytes = std::fs::metadata(core.profile.db_path())
        .map(|m| m.len())
        .unwrap_or(0);
    let (object_count, objects_bytes): (i64, i64) = core
        .db
        .read(|c| {
            let (n, s): (i64, i64) = c.query_row(
                "SELECT count(*), COALESCE(sum(size),0) FROM blobs",
                [],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?;
            Ok((n, s))
        })
        .await?;
    let staging_bytes = dir_size(&core.profile.staging_dir());
    Ok(serde_json::json!({
        "db_bytes": db_bytes,
        "objects_bytes": objects_bytes,
        "staging_bytes": staging_bytes,
        "object_count": object_count,
        "free_bytes": free_bytes(&core.profile.archive_root),
    }))
}

fn dir_size(p: &Path) -> i64 {
    let mut total = 0i64;
    if let Ok(it) = std::fs::read_dir(p) {
        for e in it.flatten() {
            if let Ok(t) = e.file_type() {
                if t.is_dir() {
                    total += dir_size(&e.path());
                } else {
                    total += e.metadata().map(|m| m.len() as i64).unwrap_or(0);
                }
            }
        }
    }
    total
}

fn free_bytes(p: &Path) -> Option<i64> {
    #[cfg(windows)]
    {
        use windows::Win32::Storage::FileSystem::GetDiskFreeSpaceExW;
        use windows::core::PCWSTR;
        let mut wide: Vec<u16> = p.as_os_str().to_string_lossy().encode_utf16().collect();
        wide.push(0);
        let mut free: u64 = 0;
        unsafe {
            GetDiskFreeSpaceExW(PCWSTR(wide.as_ptr()), Some(&mut free), None, None).ok()?;
        }
        Some(free as i64)
    }
    #[cfg(not(windows))]
    {
        let _ = p;
        None
    }
}

use serde_json::Value;

#[cfg(test)]
mod restore_tests {
    use super::*;
    use crate::core::Core;

    async fn fixture() -> (
        tempfile::TempDir,
        std::sync::Arc<Core>,
        std::path::PathBuf,
        String,
    ) {
        let dir = tempfile::TempDir::new().unwrap();
        let core = Core::open(Profile {
            archive_id: "restore-test".into(),
            data_root: dir.path().join("data"),
            archive_root: dir.path().join("archive"),
            vault_dir: Some(dir.path().join("vault")),
            ..Profile::default()
        })
        .await
        .unwrap();
        let bytes = b"restore validated media";
        use sha2::Digest;
        let sha: String = sha2::Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        let object = core.objects.object_path(&sha).unwrap();
        std::fs::create_dir_all(object.parent().unwrap()).unwrap();
        std::fs::write(object, bytes).unwrap();
        let sh = sha.clone();
        core.db.write(move |tx| {
            tx.execute("INSERT INTO blobs(blob_id,size,mime,first_seen_at) VALUES(?1,23,'video/mp4','now')",[format!("sha256:{sh}")])?;
            tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES('rep',?1,'local','primary',?2,'verified')",rusqlite::params![format!("sha256:{sh}"), sh])?;
            Ok(())
        }).await.unwrap();
        // Minimal catalog fixture includes an unreferenced object, which restore must still validate.
        let root = dir.path().join("backup");
        let info = backup(&core, &root, false).await.unwrap();
        let bk = root.join(info["backup_id"].as_str().unwrap());
        (dir, core, bk, sha)
    }

    #[tokio::test]
    async fn f002_rejects_malicious_ids_before_writes() {
        let (dir, core, bk, _) = fixture().await;
        let manifest = bk.join("manifest.json");
        let original: Value = serde_json::from_slice(&std::fs::read(&manifest).unwrap()).unwrap();
        for bad in [
            "sha256:../../outside",
            "sha256:C:/outside",
            "sha256:/outside",
            "sha256:x",
            "sha256:中",
            "sha256:",
            "x",
            "sha256:../",
        ] {
            let mut m = original.clone();
            m["objects"] = serde_json::json!([{"blob_id":bad,"size":1,"included":true}]);
            std::fs::write(&manifest, serde_json::to_vec(&m).unwrap()).unwrap();
            let target = dir.path().join("restored");
            assert!(restore(&core, &bk, &target).await.is_err(), "{bad}");
            assert!(!target.exists());
            assert!(!dir.path().join("outside").exists());
        }
    }

    #[tokio::test]
    async fn f003_restore_opens_and_marks_omitted_or_corrupt_objects() {
        let (dir, core, bk, sha) = fixture().await;
        let target = dir.path().join("metadata-only");
        let result = restore(&core, &bk, &target).await.unwrap();
        assert_eq!(result["missing_objects"], 1);
        let mut profile = crate::config::load_profile(&target.join("profile.json")).unwrap();
        profile.vault_dir = Some(dir.path().join("restored-vault"));
        let restored = Core::open(profile).await.unwrap();
        restored.recover().await.unwrap();
        let state: String = restored
            .db
            .read(|c| Ok(c.query_row("SELECT state FROM replicas", [], |r| r.get(0))?))
            .await
            .unwrap();
        assert_eq!(state, "absent");
        assert!(!restored.objects.object_path(&sha).unwrap().exists());
        let manifest = bk.join("manifest.json");
        let mut m: Value = serde_json::from_slice(&std::fs::read(&manifest).unwrap()).unwrap();
        m["objects"] =
            serde_json::json!([{"blob_id":format!("sha256:{sha}"),"size":23,"included":true}]);
        std::fs::write(manifest, serde_json::to_vec(&m).unwrap()).unwrap();
        std::fs::create_dir_all(bk.join("objects")).unwrap();
        std::fs::write(bk.join("objects").join(&sha), b"corrupt").unwrap();
        assert!(
            restore(&core, &bk, &dir.path().join("corrupt"))
                .await
                .is_err()
        );
        assert!(!dir.path().join("corrupt").exists());
        std::fs::copy(
            core.objects.object_path(&sha).unwrap(),
            bk.join("objects").join(&sha),
        )
        .unwrap();
        let full = dir.path().join("full");
        restore(&core, &bk, &full).await.unwrap();
        let mut profile = crate::config::load_profile(&full.join("profile.json")).unwrap();
        profile.vault_dir = Some(dir.path().join("full-vault"));
        let restored = Core::open(profile).await.unwrap();
        restored.recover().await.unwrap();
        assert_eq!(
            restored.objects.read_range(&sha, 0, 100).unwrap(),
            b"restore validated media"
        );
        assert_eq!(
            hash_file(&restored.objects.object_path(&sha).unwrap()).unwrap(),
            sha
        );
    }
}
