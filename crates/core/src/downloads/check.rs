//! Checking recorded copies against what is on disk now, and forgetting records.
use super::write;
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use crate::store::downloads::ExternalCopy;
use std::collections::HashMap;
use std::path::PathBuf;
use trace_contracts::download::{CheckReport, CopyView, ForgetReport};

pub(super) fn copy_view(c: &ExternalCopy) -> CopyView {
    CopyView {
        copy_id: c.copy_id.clone(),
        entry_id: c.entry_id.clone(),
        location_id: c.location_id.clone(),
        rel_path: c.rel_path.clone(),
        role: c.role.clone(),
        size: c.size,
        state: c.state.clone(),
        checked_at: c.checked_at.clone(),
    }
}

/// What a recorded copy is now.
#[derive(Debug, PartialEq, Eq)]
enum Observed {
    Present,
    Changed,
    Missing,
}

fn observe(path: &std::path::Path, copy: &ExternalCopy) -> Observed {
    let Ok(meta) = std::fs::metadata(path) else {
        return Observed::Missing;
    };
    if copy.size >= 0 && meta.len() != copy.size as u64 {
        return Observed::Changed;
    }
    match (write::measure(path), copy.sha256.as_deref()) {
        (Ok(w), Some(sha)) if w.sha256.eq_ignore_ascii_case(sha) => Observed::Present,
        (Err(e), _) if e.kind() == std::io::ErrorKind::NotFound => Observed::Missing,
        _ => Observed::Changed,
    }
}

/// Re-reads every recorded copy (of one folder and/or one entry, or all) and updates its
/// state: "present", "changed" when the content differs from what TRACE wrote, "missing"
/// when the file is gone. Files are only read.
pub async fn check(
    core: &Core,
    location_id: Option<&str>,
    entry_id: Option<&str>,
) -> Result<CheckReport, CoreError> {
    let _run = core.downloads_run.lock().await;
    let (loc, entry) = (location_id.map(str::to_owned), entry_id.map(str::to_owned));
    let (copies, folders) = core
        .db
        .read(move |c| {
            let copies = store::downloads::copies_to_check(c, loc.as_deref(), entry.as_deref())?;
            let mut folders = HashMap::new();
            for copy in &copies {
                if !folders.contains_key(&copy.location_id)
                    && let Some(f) = store::downloads::folder(c, &copy.location_id)?
                {
                    folders.insert(copy.location_id.clone(), f.path);
                }
            }
            Ok((copies, folders))
        })
        .await?;
    let results = tokio::task::spawn_blocking(move || {
        copies
            .into_iter()
            .map(|copy| {
                let observed = folders
                    .get(&copy.location_id)
                    .map_or(Observed::Missing, |root| {
                        observe(&PathBuf::from(root).join(&copy.rel_path), &copy)
                    });
                (copy, observed)
            })
            .collect::<Vec<_>>()
    })
    .await
    .map_err(|_| CoreError::Internal("检查任务异常中止".into()))?;
    let mut report = CheckReport {
        checked: results.len() as i64,
        present: 0,
        changed: 0,
        missing: 0,
        problems: Vec::new(),
    };
    let mut updates = Vec::new();
    for (mut copy, observed) in results {
        // An interrupted write has no final file; the record only claimed it.
        let abandoned = copy.state == "writing" && observed == Observed::Missing;
        let state = match observed {
            Observed::Present => {
                report.present += 1;
                "present"
            }
            Observed::Changed => {
                report.changed += 1;
                "changed"
            }
            Observed::Missing => {
                report.missing += 1;
                "missing"
            }
        };
        copy.state = state.into();
        if state != "present" && !abandoned {
            report.problems.push(copy_view(&copy));
        }
        updates.push((copy.copy_id, state, abandoned));
    }
    core.db
        .write(move |tx| {
            let now = crate::jobs::now();
            for (id, state, abandoned) in updates {
                if abandoned {
                    store::downloads::delete_copy(tx, &id)?;
                } else {
                    store::downloads::set_copy_state(tx, &id, state, &now)?;
                }
            }
            Ok(())
        })
        .await?;
    Ok(report)
}

/// The recorded copies of an entry, as last checked.
pub async fn copies(core: &Core, entry_id: &str) -> Result<Vec<CopyView>, CoreError> {
    let id = entry_id.to_owned();
    let rows = core
        .db
        .read(move |c| store::downloads::copies_of_entry(c, &id, None))
        .await?;
    Ok(rows.iter().map(copy_view).collect())
}

/// Removes TRACE's records of an entry's copies; the files stay where they are.
pub async fn forget(
    core: &Core,
    entry_id: &str,
    location_id: Option<&str>,
) -> Result<ForgetReport, CoreError> {
    let _run = core.downloads_run.lock().await;
    let (id, loc) = (entry_id.to_owned(), location_id.map(str::to_owned));
    let removed = core
        .db
        .write(move |tx| store::downloads::forget(tx, &id, loc.as_deref()))
        .await?;
    Ok(ForgetReport { removed })
}
