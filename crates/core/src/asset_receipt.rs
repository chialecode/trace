//! The receipt a fetch script leaves next to a locked asset directory (`verified.json`):
//! the lock that was verified and the SHA-256 of every file it produced. Core checks it
//! again before it executes anything from the directory.
use crate::error::CoreError;
use serde_json::Value;
use std::path::Path;

/// Whether `dir` holds exactly what `lock` pins: the receipt carries the same lock, every
/// listed file still has its recorded digest, and the `required` files are among them.
pub(crate) fn verify(dir: &Path, lock: &Value, required: &[String]) -> Result<(), CoreError> {
    let receipt: Value = serde_json::from_slice(&std::fs::read(dir.join("verified.json"))?)?;
    if receipt["lock"] != *lock {
        return Err(CoreError::InvalidInput("asset lock mismatch".into()));
    }
    let files = receipt["files"]
        .as_object()
        .ok_or_else(|| CoreError::InvalidInput("asset inventory missing".into()))?;
    for (name, sha) in files {
        if Path::new(name).components().count() != 1
            || crate::objects::hash_file(&dir.join(name))? != sha.as_str().unwrap_or_default()
        {
            return Err(CoreError::InvalidInput("asset checksum mismatch".into()));
        }
    }
    if required.iter().any(|name| !files.contains_key(name)) {
        return Err(CoreError::InvalidInput(
            "asset binaries missing from inventory".into(),
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn sha(path: &Path) -> String {
        crate::objects::hash_file(path).unwrap()
    }

    #[test]
    fn a_receipt_must_match_the_lock_the_files_and_the_required_names() {
        let dir = tempfile::tempdir().unwrap();
        let lock = json!({"tool": {"version": "1"}});
        std::fs::write(dir.path().join("a.exe"), b"binary").unwrap();
        let write = |lock: &Value, files: Value| {
            std::fs::write(
                dir.path().join("verified.json"),
                json!({"lock": lock, "files": files}).to_string(),
            )
            .unwrap();
        };
        let digest = sha(&dir.path().join("a.exe"));
        let need = vec!["a.exe".to_owned()];
        write(&lock, json!({"a.exe": digest}));
        verify(dir.path(), &lock, &need).unwrap();
        write(&json!({"tool": {"version": "2"}}), json!({"a.exe": digest}));
        assert!(verify(dir.path(), &lock, &need).is_err(), "another lock");
        write(&lock, json!({"a.exe": "0".repeat(64)}));
        assert!(verify(dir.path(), &lock, &need).is_err(), "changed file");
        write(&lock, json!({"nested/a.exe": digest}));
        assert!(verify(dir.path(), &lock, &need).is_err(), "path component");
        write(&lock, json!({}));
        assert!(verify(dir.path(), &lock, &need).is_err(), "missing name");
        assert!(verify(dir.path(), &lock, &[]).is_ok());
    }
}
