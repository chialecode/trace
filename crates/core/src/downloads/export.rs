//! Writing one entry into a download folder: naming, conflict handling, space check, then
//! each file through a temporary file and a verified rename, recorded as it goes.
use super::naming::{self, EntryNames, Piece, Planned};
use super::sidecar::{self, SidecarFile};
use super::user_message;
use super::write::{self, Source};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use crate::store::downloads::{EntryFacts, ExternalCopy, MediaFile};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use trace_contracts::download::{ExportItem, ExportedFile};

/// Space kept free on the target volume beyond the files themselves.
const SPACE_MARGIN: i64 = 64 * 1024 * 1024;

pub(super) fn failed(entry_id: &str, e: &CoreError) -> ExportItem {
    ExportItem {
        entry_id: entry_id.to_owned(),
        state: "failed".into(),
        files: Vec::new(),
        message: Some(user_message(e)),
    }
}

fn nothing(entry_id: &str, message: &str) -> ExportItem {
    ExportItem {
        entry_id: entry_id.to_owned(),
        state: "nothing".into(),
        files: Vec::new(),
        message: Some(message.into()),
    }
}

fn extension(mime: Option<&str>) -> &'static str {
    crate::archive_export::extension(mime.unwrap_or(""))
}

fn digest_of(blob_id: &str) -> Result<&str, CoreError> {
    crate::paths::blob_sha(blob_id)
}

fn nonblank(text: &str) -> Option<&str> {
    Some(text.trim()).filter(|t| !t.is_empty())
}

/// What goes into the entry's text file, if it has one.
fn text_of(facts: &EntryFacts, media_total: usize) -> Option<&str> {
    match media_total {
        0 => nonblank(&facts.body).or_else(|| nonblank(&facts.title)),
        1 => None,
        _ => nonblank(&facts.body),
    }
}

/// A file to put in the folder, with where its bytes come from.
struct Job {
    planned: Planned,
    role: &'static str,
    media_id: Option<String>,
    /// Library object for media; none for text and sidecar.
    source: Option<(PathBuf, String, i64)>,
}

struct Gathered {
    facts: EntryFacts,
    media: Vec<MediaFile>,
    stored: Option<store::meta::Stored>,
    existing: Vec<ExternalCopy>,
}

async fn gather(core: &Core, folder_id: &str, entry_id: &str) -> Result<Gathered, CoreError> {
    let (id, folder) = (entry_id.to_owned(), folder_id.to_owned());
    core.db
        .read(move |c| {
            let Some(facts) = store::downloads::entry_facts(c, &id)? else {
                return Err(CoreError::NotFound("条目不存在".into()));
            };
            Ok(Gathered {
                media: store::downloads::media_files(c, &facts.revision_id)?,
                stored: store::meta::load(c, &id)?,
                existing: store::downloads::copies_of_entry(c, &id, Some(&folder))?,
                facts,
            })
        })
        .await
}

fn entry_suffix(entry_id: &str) -> String {
    let digest: String = Sha256::digest(entry_id.as_bytes())
        .iter()
        .take(4)
        .map(|b| format!("{b:02x}"))
        .collect();
    format!("~{digest}")
}

/// Whether the name is taken by something that is not this entry's own record.
async fn taken(core: &Core, folder_id: &str, root: &Path, rel: &str) -> Result<bool, CoreError> {
    if root.join(rel).exists() {
        return Ok(true);
    }
    let (folder, rel) = (folder_id.to_owned(), rel.to_owned());
    Ok(core
        .db
        .read(move |c| store::downloads::copy_at(c, &folder, &rel))
        .await?
        .is_some())
}

/// Names the files of the entry. Names an earlier run recorded for the same file are kept, so
/// a re-run finds its files even after the title changed.
async fn plan(
    core: &Core,
    folder_id: &str,
    root: &Path,
    g: &Gathered,
    sidecar_on: bool,
) -> Result<Vec<Planned>, CoreError> {
    let facts = &g.facts;
    let site = trace_domain::sites::site(&facts.site).map_or(facts.site.as_str(), |s| s.title);
    let author = nonblank(&facts.author).unwrap_or(&facts.author_key);
    let names = EntryNames {
        site,
        author,
        published_at: facts.published_at.as_deref(),
        title: &facts.title,
        id: &facts.native_key,
    };
    let mut pieces: Vec<Piece> = g
        .media
        .iter()
        .map(|m| Piece::Media {
            key: m.media_id.clone(),
            ext: extension(m.mime.as_deref()).into(),
            present: m.blob_id.is_some(),
        })
        .collect();
    if text_of(facts, g.media.len()).is_some() {
        pieces.push(Piece::Text);
    }
    let root_len = root.to_string_lossy().encode_utf16().count();
    let recorded: HashMap<String, &str> = g
        .existing
        .iter()
        .map(|c| {
            let key = match c.role.as_str() {
                "media" => c.media_id.clone().unwrap_or_default(),
                other => other.to_owned(),
            };
            (key, c.rel_path.as_str())
        })
        .collect();
    for suffix in ["", &entry_suffix(&facts.entry_id)] {
        let mut planned = naming::layout(&names, &pieces, sidecar_on, root_len, suffix)
            .map_err(|e| CoreError::InvalidInput(e.to_string()))?;
        let mut clash = false;
        for p in &mut planned {
            if let Some(rel) = recorded.get(&p.key) {
                p.rel = (*rel).to_owned();
            } else if taken(core, folder_id, root, &p.rel).await? {
                clash = true;
            }
        }
        if !clash {
            return Ok(planned);
        }
    }
    Err(CoreError::InvalidInput(
        "下载目录里已有同名文件，无法区分，未写入".into(),
    ))
}

fn role_of(key: &str) -> &'static str {
    match key {
        "text" => "text",
        "sidecar" => "sidecar",
        _ => "media",
    }
}

/// How the file recorded earlier looks now.
enum Standing {
    /// Same length as recorded: nothing to do.
    Intact,
    /// There, but not what TRACE wrote.
    Edited,
    Gone,
}

fn standing(root: &Path, copy: &ExternalCopy) -> Standing {
    match std::fs::metadata(root.join(&copy.rel_path)) {
        Err(_) => Standing::Gone,
        Ok(m) if copy.state == "present" && copy.size >= 0 && m.len() == copy.size as u64 => {
            Standing::Intact
        }
        Ok(_) => Standing::Edited,
    }
}

fn exported(rel: &str, role: &str, size: i64, state: &str) -> ExportedFile {
    ExportedFile {
        rel_path: rel.to_owned(),
        role: role.to_owned(),
        size,
        state: state.to_owned(),
    }
}

fn now_copy(
    facts: &EntryFacts,
    folder_id: &str,
    job: &Job,
    size: i64,
    sha: Option<String>,
) -> ExternalCopy {
    ExternalCopy {
        copy_id: format!("cp-{}", uuid::Uuid::new_v4()),
        entry_id: Some(facts.entry_id.clone()),
        media_id: job.media_id.clone(),
        location_id: folder_id.to_owned(),
        rel_path: job.planned.rel.clone(),
        role: job.role.into(),
        size,
        sha256: sha,
        state: "writing".into(),
        checked_at: None,
    }
}

/// Writes one file and records it; the record only says "present" once the file is final.
async fn put(
    core: &Core,
    root: &Path,
    copy: ExternalCopy,
    source: Source,
    expect: Option<(u64, String)>,
) -> Result<write::Written, CoreError> {
    let dest = root.join(&copy.rel_path);
    let (root_owned, record) = (root.to_path_buf(), copy.clone());
    core.db
        .write({
            let record = record.clone();
            move |tx| store::downloads::begin_copy(tx, &record, &crate::jobs::now())
        })
        .await?;
    let written = tokio::task::spawn_blocking(move || {
        let expect = expect.as_ref().map(|(n, s)| (*n, s.as_str()));
        write::write_new(&root_owned, &dest, &source, expect)
    })
    .await
    .map_err(|_| CoreError::Internal("写入任务异常中止".into()))?;
    let id = record.copy_id.clone();
    match written {
        Ok(w) => {
            let (size, sha) = (w.size as i64, w.sha256.clone());
            core.db
                .write(move |tx| {
                    store::downloads::finish_copy(tx, &id, size, &sha, &crate::jobs::now())
                })
                .await?;
            Ok(w)
        }
        Err(e) => {
            // Nothing final was written, so the record would only be a claim about a
            // file that is not there.
            core.db
                .write(move |tx| store::downloads::delete_copy(tx, &id))
                .await?;
            Err(e)
        }
    }
}

/// Everything one entry's export works from.
struct Run<'a> {
    core: &'a Core,
    folder_id: &'a str,
    root: &'a Path,
    g: Gathered,
    text: Option<String>,
}

impl Run<'_> {
    fn by_key(&self) -> HashMap<&str, &ExternalCopy> {
        self.g
            .existing
            .iter()
            .map(|c| {
                let key = match c.role.as_str() {
                    "media" => c.media_id.as_deref().unwrap_or_default(),
                    other => other,
                };
                (key, c)
            })
            .collect()
    }

    /// The media and text files of the plan, with the library object of each media file.
    fn jobs(&self, planned: &[Planned]) -> Result<Vec<Job>, CoreError> {
        let mut jobs = Vec::new();
        for p in planned.iter().filter(|p| p.key != "sidecar") {
            let role = role_of(&p.key);
            let source = if role == "media" {
                let m = self.g.media.iter().find(|m| m.media_id == p.key);
                m.and_then(|m| m.blob_id.as_deref().zip(m.size))
                    .map(|(blob, size)| -> Result<_, CoreError> {
                        let digest = digest_of(blob)?;
                        Ok((
                            self.core.objects.object_path(digest)?,
                            digest.to_owned(),
                            size,
                        ))
                    })
                    .transpose()?
            } else {
                None
            };
            jobs.push(Job {
                planned: p.clone(),
                role,
                media_id: (role == "media").then(|| p.key.clone()),
                source,
            });
        }
        Ok(jobs)
    }

    /// Fails before any write when the volume cannot hold what is about to be written.
    fn check_space(&self, needed: i64) -> Result<(), CoreError> {
        let free = crate::storage_ops::free_bytes(&crate::library::existing_ancestor(self.root));
        match free {
            Some(free) if needed > 0 && free < needed + SPACE_MARGIN => {
                Err(CoreError::StorageFull(format!(
                    "下载目录所在磁盘空间不足：需要 {} MB，可用 {} MB",
                    needed / 1_048_576 + 1,
                    free / 1_048_576
                )))
            }
            _ => Ok(()),
        }
    }

    async fn write_job(&self, job: &Job) -> Result<i64, CoreError> {
        let facts = &self.g.facts;
        let (copy, source, expect) = match &job.source {
            Some((path, digest, size)) => {
                if !path.is_file() {
                    return Err(CoreError::InvalidInput(
                        "资源库中的文件缺失或损坏，请先修复资源库".into(),
                    ));
                }
                (
                    now_copy(facts, self.folder_id, job, *size, Some(digest.clone())),
                    Source::File(path.clone()),
                    Some((*size as u64, digest.clone())),
                )
            }
            None => {
                let bytes = self.text.clone().unwrap_or_default().into_bytes();
                let sha = hex(&bytes);
                (
                    now_copy(facts, self.folder_id, job, bytes.len() as i64, Some(sha)),
                    Source::Bytes(bytes),
                    None,
                )
            }
        };
        let size = copy.size;
        put(self.core, self.root, copy, source, expect).await?;
        Ok(size)
    }

    /// Writes the sidecar if it is missing or out of date; an edited one is left alone.
    async fn write_sidecar(
        &self,
        side: &Planned,
        listed: &[(&Job, String, i64)],
        files: &mut Vec<ExportedFile>,
    ) -> Result<bool, CoreError> {
        let dir = side.rel.rsplit_once('\\').map_or("", |(d, _)| d);
        let listing: Vec<SidecarFile> = listed
            .iter()
            .map(|(job, digest, size)| SidecarFile {
                name: job
                    .planned
                    .rel
                    .strip_prefix(dir)
                    .unwrap_or(&job.planned.rel)
                    .trim_start_matches('\\')
                    .replace('\\', "/"),
                role: job.role.into(),
                size: *size,
                sha256: digest.clone(),
            })
            .collect();
        let body = sidecar::build(
            &self.g.facts,
            &self.g.stored,
            &listing,
            self.text.as_deref(),
        );
        let sha = hex(&body);
        let size = body.len() as i64;
        let by_key = self.by_key();
        let write_it = match by_key.get("sidecar").map(|c| (*c, standing(self.root, c))) {
            Some((c, Standing::Intact)) if c.sha256.as_deref() == Some(sha.as_str()) => {
                files.push(exported(&side.rel, "sidecar", size, "present"));
                false
            }
            Some((c, Standing::Intact)) => {
                // TRACE's own file, out of date: replaced by the current one.
                let _ = std::fs::remove_file(self.root.join(&c.rel_path));
                true
            }
            Some((_, Standing::Edited)) => {
                files.push(exported(&side.rel, "sidecar", size, "changed"));
                false
            }
            Some((_, Standing::Gone)) | None => true,
        };
        if write_it {
            let job = Job {
                planned: side.clone(),
                role: "sidecar",
                media_id: None,
                source: None,
            };
            let copy = now_copy(&self.g.facts, self.folder_id, &job, size, Some(sha));
            put(self.core, self.root, copy, Source::Bytes(body), None).await?;
            files.push(exported(&side.rel, "sidecar", size, "written"));
        }
        Ok(write_it)
    }
}

pub(super) async fn export_entry(
    core: &Core,
    folder_id: &str,
    root: &Path,
    entry_id: &str,
    sidecar_on: bool,
) -> Result<ExportItem, CoreError> {
    let _gate = core.archive_access.read().await;
    let g = gather(core, folder_id, entry_id).await?;
    if g.facts.lifecycle == "deleted" {
        return Ok(nothing(entry_id, "条目已删除"));
    }
    let text = text_of(&g.facts, g.media.len()).map(str::to_owned);
    if !g.media.iter().any(|m| m.blob_id.is_some()) && text.is_none() {
        return Ok(nothing(
            entry_id,
            "条目还没有已保存的文件（可能按订阅设置未下载，或还在下载中）",
        ));
    }
    let planned = plan(core, folder_id, root, &g, sidecar_on).await?;
    let run = Run {
        core,
        folder_id,
        root,
        g,
        text,
    };
    let by_key = run.by_key();
    // Media and text first; the sidecar last, because it lists the files by digest.
    let jobs = run.jobs(&planned)?;
    let mut to_write = Vec::new();
    let mut files = Vec::new();
    let mut listed = Vec::new();
    for job in &jobs {
        let size_hint = job.source.as_ref().map_or(0, |s| s.2);
        match by_key
            .get(job.planned.key.as_str())
            .map(|c| standing(root, c))
        {
            Some(Standing::Intact) => {
                files.push(exported(&job.planned.rel, job.role, size_hint, "present"));
            }
            Some(Standing::Edited) => {
                files.push(exported(&job.planned.rel, job.role, size_hint, "changed"));
            }
            Some(Standing::Gone) | None => to_write.push(job),
        }
        if let Some((_, digest, size)) = &job.source {
            listed.push((job, digest.clone(), *size));
        }
    }
    run.check_space(
        to_write
            .iter()
            .map(|j| j.source.as_ref().map_or(0, |s| s.2))
            .sum(),
    )?;
    let mut wrote = false;
    for job in to_write {
        let size = run.write_job(job).await?;
        wrote = true;
        files.push(exported(&job.planned.rel, job.role, size, "written"));
    }
    if sidecar_on && let Some(side) = planned.iter().find(|p| p.key == "sidecar") {
        wrote |= run.write_sidecar(side, &listed, &mut files).await?;
    }
    Ok(ExportItem {
        entry_id: entry_id.to_owned(),
        state: if wrote { "exported" } else { "unchanged" }.into(),
        files,
        message: None,
    })
}

fn hex(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
