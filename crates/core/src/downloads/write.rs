//! Writing one file into a download folder: bytes go to a temporary folder on the same
//! volume, length and digest are verified, and only then does the file get its final name.
//! A file that already has the final name is never replaced.
use crate::error::CoreError;
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};

/// Folder inside a download folder where unfinished files live; its content is never listed
/// as the user's.
pub const TEMP_DIR: &str = ".trace-tmp";
const CHUNK: usize = 1024 * 1024;

pub enum Source {
    File(PathBuf),
    Bytes(Vec<u8>),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Written {
    pub size: u64,
    pub sha256: String,
}

/// Renames without replacing: fails with `AlreadyExists` when `to` is taken.
pub fn rename_new(from: &Path, to: &Path) -> std::io::Result<()> {
    #[cfg(windows)]
    {
        use windows::Win32::Foundation::{ERROR_ALREADY_EXISTS, ERROR_FILE_EXISTS};
        use windows::Win32::Storage::FileSystem::{MOVEFILE_WRITE_THROUGH, MoveFileExW};
        use windows::core::PCWSTR;
        let wide = |p: &Path| -> Vec<u16> {
            let mut w: Vec<u16> = p.as_os_str().to_string_lossy().encode_utf16().collect();
            w.push(0);
            w
        };
        let (a, b) = (wide(from), wide(to));
        // No MOVEFILE_REPLACE_EXISTING: an existing destination makes the call fail.
        unsafe {
            MoveFileExW(
                PCWSTR(a.as_ptr()),
                PCWSTR(b.as_ptr()),
                MOVEFILE_WRITE_THROUGH,
            )
        }
        .map_err(|e| {
            let code = (e.code().0 as u32) & 0xffff;
            if code == ERROR_ALREADY_EXISTS.0 || code == ERROR_FILE_EXISTS.0 {
                std::io::Error::from(std::io::ErrorKind::AlreadyExists)
            } else {
                std::io::Error::from_raw_os_error(code as i32)
            }
        })
    }
    #[cfg(not(windows))]
    {
        std::fs::hard_link(from, to)?;
        std::fs::remove_file(from)
    }
}

/// Maps an I/O failure to the error the user can act on.
pub fn io_error(e: std::io::Error, what: &str) -> CoreError {
    // 112 = ERROR_DISK_FULL, 39 = ERROR_HANDLE_DISK_FULL
    if matches!(e.raw_os_error(), Some(112 | 39)) || e.kind() == std::io::ErrorKind::StorageFull {
        return CoreError::StorageFull(format!("{what}：目标磁盘空间不足"));
    }
    if e.kind() == std::io::ErrorKind::PermissionDenied {
        return CoreError::InvalidInput(format!("{what}：没有写入权限"));
    }
    CoreError::InvalidInput(format!("{what}：{e}"))
}

/// Refuses a folder that cannot take a file now (missing and not creatable, read-only,
/// protected), before any download starts.
pub fn probe_writable(root: &Path) -> Result<(), CoreError> {
    std::fs::create_dir_all(root).map_err(|e| io_error(e, "下载目录不可用"))?;
    if !root.is_dir() {
        return Err(CoreError::InvalidInput("下载目录不是文件夹".into()));
    }
    let temp = root.join(TEMP_DIR);
    std::fs::create_dir_all(&temp).map_err(|e| io_error(e, "下载目录不可写"))?;
    let probe = temp.join(format!("{}.probe", uuid::Uuid::new_v4()));
    File::create(&probe)
        .and_then(|mut f| f.write_all(b"trace"))
        .map_err(|e| io_error(e, "下载目录不可写"))?;
    let _ = std::fs::remove_file(&probe);
    Ok(())
}

/// Rejects a path below `root` that goes through a link: a folder that points elsewhere could
/// take the write out of the download folder.
pub fn no_links_below(root: &Path, path: &Path) -> Result<(), CoreError> {
    for ancestor in path.ancestors() {
        if ancestor == root || !ancestor.starts_with(root) {
            break;
        }
        match std::fs::symlink_metadata(ancestor) {
            Ok(meta) => {
                #[cfg(windows)]
                let reparse = {
                    use std::os::windows::fs::MetadataExt;
                    meta.file_attributes() & 0x400 != 0
                };
                #[cfg(not(windows))]
                let reparse = false;
                if meta.file_type().is_symlink() || reparse {
                    return Err(CoreError::InvalidInput(
                        "下载目录内含链接，已拒绝写入".into(),
                    ));
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(io_error(e, "无法检查下载目录")),
        }
    }
    Ok(())
}

fn copy_hashed(source: &Source, out: &mut File) -> Result<Written, CoreError> {
    let mut hasher = Sha256::new();
    let mut size = 0u64;
    match source {
        Source::Bytes(bytes) => {
            hasher.update(bytes);
            out.write_all(bytes).map_err(|e| io_error(e, "写入失败"))?;
            size = bytes.len() as u64;
        }
        Source::File(path) => {
            let mut input = File::open(path).map_err(|e| io_error(e, "读取资源库文件失败"))?;
            let mut buf = vec![0u8; CHUNK];
            loop {
                let n = input
                    .read(&mut buf)
                    .map_err(|e| io_error(e, "读取资源库文件失败"))?;
                if n == 0 {
                    break;
                }
                hasher.update(&buf[..n]);
                out.write_all(&buf[..n])
                    .map_err(|e| io_error(e, "写入失败"))?;
                size += n as u64;
            }
        }
    }
    out.flush().map_err(|e| io_error(e, "写入失败"))?;
    out.sync_all().map_err(|e| io_error(e, "写入失败"))?;
    Ok(Written {
        size,
        sha256: hasher
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect(),
    })
}

/// Writes `source` to `dest` (below `root`). The temporary file is removed on every failure,
/// so an interrupted run leaves nothing that looks like a finished file.
pub fn write_new(
    root: &Path,
    dest: &Path,
    source: &Source,
    expect: Option<(u64, &str)>,
) -> Result<Written, CoreError> {
    no_links_below(root, dest)?;
    let temp_dir = root.join(TEMP_DIR);
    std::fs::create_dir_all(&temp_dir).map_err(|e| io_error(e, "无法创建临时目录"))?;
    let temp: PathBuf = temp_dir.join(format!("{}.part", uuid::Uuid::new_v4()));
    let result = (|| {
        let mut out = File::create(&temp).map_err(|e| io_error(e, "无法创建临时文件"))?;
        let written = copy_hashed(source, &mut out)?;
        drop(out);
        if let Some((size, sha)) = expect
            && (written.size != size || !written.sha256.eq_ignore_ascii_case(sha))
        {
            return Err(CoreError::InvalidInput(
                "校验失败：写入内容与资源库中的记录不一致".into(),
            ));
        }
        if let Some(parent) = dest.parent() {
            std::fs::create_dir_all(parent).map_err(|e| io_error(e, "无法创建目录"))?;
            no_links_below(root, dest)?;
        }
        rename_new(&temp, dest).map_err(|e| {
            if e.kind() == std::io::ErrorKind::AlreadyExists {
                CoreError::InvalidInput("目标文件已存在，未覆盖".into())
            } else {
                io_error(e, "无法放入最终位置")
            }
        })?;
        Ok(written)
    })();
    if result.is_err() {
        let _ = std::fs::remove_file(&temp);
    }
    result
}

/// Size and digest of a file now, for change detection.
pub fn measure(path: &Path) -> std::io::Result<Written> {
    let mut file = File::open(path)?;
    let mut hasher = Sha256::new();
    let mut size = 0u64;
    let mut buf = vec![0u8; CHUNK];
    loop {
        let n = file.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
        size += n as u64;
    }
    Ok(Written {
        size,
        sha256: hasher
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect(),
    })
}

/// Removes unfinished files an interrupted run left behind (older than the run that is
/// starting now — callers pass the folder only when no export of it is running).
pub fn clear_temp(root: &Path) {
    let _ = std::fs::remove_dir_all(root.join(TEMP_DIR));
}
