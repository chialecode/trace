//! The file-level guarantees of a download write: verified before it gets its name, never over
//! an existing file, nothing half-finished left behind.
use super::write::{self, Source, TEMP_DIR};
use sha2::{Digest, Sha256};
use std::path::Path;

fn sha(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

fn names(dir: &Path) -> Vec<String> {
    let mut out: Vec<String> = std::fs::read_dir(dir)
        .map(|r| {
            r.flatten()
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect()
        })
        .unwrap_or_default();
    out.sort();
    out
}

#[test]
fn rename_new_refuses_to_replace_and_leaves_both_files_alone() {
    let dir = tempfile::tempdir().unwrap();
    let (a, b) = (dir.path().join("a"), dir.path().join("b"));
    std::fs::write(&a, b"new").unwrap();
    std::fs::write(&b, b"old").unwrap();
    let err = write::rename_new(&a, &b).unwrap_err();
    assert_eq!(err.kind(), std::io::ErrorKind::AlreadyExists);
    assert_eq!(std::fs::read(&a).unwrap(), b"new");
    assert_eq!(std::fs::read(&b).unwrap(), b"old");
    std::fs::remove_file(&b).unwrap();
    write::rename_new(&a, &b).unwrap();
    assert!(!a.exists());
    assert_eq!(std::fs::read(&b).unwrap(), b"new");
}

#[test]
fn a_verified_write_lands_whole_and_leaves_no_temporary_file() {
    let dir = tempfile::tempdir().unwrap();
    let dest = dir.path().join("sub").join("file.bin");
    let bytes = vec![9u8; 3 * 1024 * 1024 + 5];
    let written = write::write_new(
        dir.path(),
        &dest,
        &Source::Bytes(bytes.clone()),
        Some((bytes.len() as u64, &sha(&bytes))),
    )
    .unwrap();
    assert_eq!(written.size, bytes.len() as u64);
    assert_eq!(std::fs::read(&dest).unwrap(), bytes);
    assert_eq!(names(&dir.path().join(TEMP_DIR)), Vec::<String>::new());
}

#[test]
fn a_failed_verification_leaves_neither_a_final_file_nor_a_temporary_one() {
    let dir = tempfile::tempdir().unwrap();
    let source = dir.path().join("source.bin");
    std::fs::write(&source, b"actual").unwrap();
    let root = dir.path().join("out");
    std::fs::create_dir_all(&root).unwrap();
    for expect in [(6u64, sha(b"other!")), (7u64, sha(b"actual"))] {
        let dest = root.join("deep").join("file.bin");
        let err = write::write_new(
            &root,
            &dest,
            &Source::File(source.clone()),
            Some((expect.0, &expect.1)),
        )
        .unwrap_err();
        assert!(err.to_string().contains("校验失败"), "{err}");
        assert!(!dest.exists());
        assert!(
            !root.join("deep").exists(),
            "no folder for a file that never landed"
        );
        assert_eq!(names(&root.join(TEMP_DIR)), Vec::<String>::new());
    }
}

#[test]
fn an_existing_file_is_never_overwritten() {
    let dir = tempfile::tempdir().unwrap();
    let dest = dir.path().join("mine.txt");
    std::fs::write(&dest, b"mine").unwrap();
    let err =
        write::write_new(dir.path(), &dest, &Source::Bytes(b"theirs".to_vec()), None).unwrap_err();
    assert!(err.to_string().contains("已存在"), "{err}");
    assert_eq!(std::fs::read(&dest).unwrap(), b"mine");
    assert_eq!(names(&dir.path().join(TEMP_DIR)), Vec::<String>::new());
}

#[test]
fn a_missing_source_fails_cleanly() {
    let dir = tempfile::tempdir().unwrap();
    let dest = dir.path().join("out.bin");
    let err = write::write_new(
        dir.path(),
        &dest,
        &Source::File(dir.path().join("nope")),
        None,
    )
    .unwrap_err();
    assert!(err.to_string().contains("读取"), "{err}");
    assert!(!dest.exists());
    assert_eq!(names(&dir.path().join(TEMP_DIR)), Vec::<String>::new());
}

#[test]
fn probing_reports_a_folder_that_is_really_a_file() {
    let dir = tempfile::tempdir().unwrap();
    let file = dir.path().join("f");
    std::fs::write(&file, b"x").unwrap();
    assert!(write::probe_writable(&file).is_err());
    let ok = dir.path().join("new").join("deep");
    write::probe_writable(&ok).unwrap();
    assert!(ok.is_dir());
    write::clear_temp(&ok);
    assert!(!ok.join(TEMP_DIR).exists());
}

#[cfg(windows)]
#[test]
fn a_folder_that_links_elsewhere_is_refused() {
    let dir = tempfile::tempdir().unwrap();
    let outside = dir.path().join("outside");
    let root = dir.path().join("root");
    std::fs::create_dir_all(&outside).unwrap();
    std::fs::create_dir_all(&root).unwrap();
    // A junction needs no privilege, unlike a symbolic link.
    let status = std::process::Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(root.join("link"))
        .arg(&outside)
        .output()
        .unwrap();
    if !status.status.success() {
        return; // the test machine cannot create junctions
    }
    let dest = root.join("link").join("file.bin");
    let err = write::write_new(&root, &dest, &Source::Bytes(b"x".to_vec()), None).unwrap_err();
    assert!(err.to_string().contains("链接"), "{err}");
    assert!(names(&outside).is_empty());
}

#[test]
fn full_disk_and_denied_errors_are_told_apart() {
    use crate::error::CoreError;
    let full = write::io_error(std::io::Error::from_raw_os_error(112), "写入失败");
    assert!(matches!(full, CoreError::StorageFull(m) if m.contains("空间不足")));
    let denied = write::io_error(
        std::io::Error::from(std::io::ErrorKind::PermissionDenied),
        "写入失败",
    );
    assert!(matches!(denied, CoreError::InvalidInput(m) if m.contains("权限")));
}
