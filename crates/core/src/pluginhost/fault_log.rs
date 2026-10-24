//! An opt-in record of what adapters and Core say when a call fails, for whoever diagnoses a
//! site that changed. The control interface and the archive only ever carry the failure class
//! (the words are never relayed to a client); this file is a local aid that exists only when the
//! daemon is started with `TRACE_ADAPTER_FAULT_LOG` naming it, and holds the method, the class
//! and the first words of the message: the adapter's own short description of what went wrong
//! (which may name the shape of the page it saw, such as its size or whether a state was present),
//! or Core's own words. It never holds a response body, a Cookie or other credential.
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::OnceLock;
use trace_contracts::plugin::PluginError;

const ENV: &str = "TRACE_ADAPTER_FAULT_LOG";
const MESSAGE_CHARS: usize = 480;
/// The file is cut back to its newest half when it grows past this.
const FILE_MAX_BYTES: u64 = 256 * 1024;

fn target() -> Option<&'static Path> {
    static TARGET: OnceLock<Option<PathBuf>> = OnceLock::new();
    TARGET
        .get_or_init(|| {
            std::env::var_os(ENV)
                .map(PathBuf::from)
                .filter(|p| p.is_absolute())
        })
        .as_deref()
}

pub(super) fn record(method: &str, error: &PluginError) {
    if let Some(path) = target() {
        append(path, method, error.class, &error.message);
    }
}

/// A command that failed inside Core itself, in Core's own words.
pub(crate) fn record_core(error: &crate::error::CoreError) {
    if let Some(path) = target() {
        append(path, "core", error.failure_class(), &error.to_string());
    }
}

fn append(path: &Path, method: &str, class: trace_domain::errors::FailureClass, message: &str) {
    let message: String = message
        .chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .take(MESSAGE_CHARS)
        .collect();
    let line = serde_json::json!({
        "method": method,
        "class": class,
        "message": message,
    });
    if std::fs::metadata(path).is_ok_and(|m| m.len() > FILE_MAX_BYTES)
        && let Ok(text) = std::fs::read_to_string(path)
    {
        let mut cut = text.len() / 2;
        while !text.is_char_boundary(cut) {
            cut += 1;
        }
        let newest = text[cut..].split_once('\n').map_or("", |(_, rest)| rest);
        let _ = std::fs::write(path, newest);
    }
    if let Ok(mut file) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
    {
        let _ = writeln!(file, "{line}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use trace_domain::errors::FailureClass;

    #[test]
    fn a_fault_is_one_short_line_and_the_file_stays_bounded() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("faults.jsonl");
        append(
            &path,
            "resolve_account",
            FailureClass::SchemaChanged,
            "no\nstate\tfound",
        );
        let text = std::fs::read_to_string(&path).unwrap();
        let line: serde_json::Value = serde_json::from_str(text.trim_end()).unwrap();
        assert_eq!(line["method"], "resolve_account");
        assert_eq!(line["class"], "schema_changed");
        assert_eq!(line["message"], "no state found");

        append(
            &path,
            "enumerate",
            FailureClass::SchemaChanged,
            &"x".repeat(5000),
        );
        let last = std::fs::read_to_string(&path).unwrap();
        let last: serde_json::Value = serde_json::from_str(last.lines().last().unwrap()).unwrap();
        assert_eq!(last["message"].as_str().unwrap().chars().count(), 480);

        for _ in 0..1500 {
            append(
                &path,
                "enumerate",
                FailureClass::SchemaChanged,
                &"y".repeat(200),
            );
        }
        assert!(std::fs::metadata(&path).unwrap().len() < 2 * FILE_MAX_BYTES);
        let kept = std::fs::read_to_string(&path).unwrap();
        assert!(
            kept.lines()
                .all(|l| serde_json::from_str::<serde_json::Value>(l).is_ok()),
            "a cut leaves whole lines only"
        );
    }
}
