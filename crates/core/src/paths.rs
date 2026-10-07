//! Shared validation for content-addressed files. Never interpret a digest as a path.
use crate::error::CoreError;
use std::path::{Path, PathBuf};

pub fn sha256(value: &str) -> Result<&str, CoreError> {
    if value.len() != 64
        || !value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err(CoreError::InvalidInput("invalid sha256 digest".into()));
    }
    Ok(value)
}

pub fn blob_sha(value: &str) -> Result<&str, CoreError> {
    sha256(
        value
            .strip_prefix("sha256:")
            .ok_or_else(|| CoreError::InvalidInput("invalid blob id".into()))?,
    )
}

/// Reject symlinks/junctions, including existing ancestors of a new target.
pub fn no_links(path: &Path) -> Result<(), CoreError> {
    for ancestor in path.ancestors() {
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
                        "linked archive path rejected".into(),
                    ));
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e.into()),
        }
    }
    Ok(())
}

pub fn object(root: &Path, digest: &str) -> Result<PathBuf, CoreError> {
    let digest = sha256(digest)?;
    let path = root.join(&digest[..2]).join(digest);
    no_links(&path)?;
    Ok(path)
}
