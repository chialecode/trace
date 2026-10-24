//! Readable, Windows-safe names for files in a download folder
//! (docs/design/multi-site-contracts.md §7.2).
//!
//! Everything here is pure: titles, author names and source ids are untrusted text, so each
//! becomes one path segment with illegal characters, device names, trailing dots and spaces
//! removed and a bounded length. Paths that cannot fit are refused before anything is written.
use sha2::{Digest, Sha256};
use unicode_normalization::UnicodeNormalization;

/// Longest single path segment, in characters.
pub const SEGMENT_MAX: usize = 120;
/// Longest full path, in UTF-16 units: the classic Windows limit, so Explorer can open it.
pub const PATH_MAX: usize = 259;
/// Longest source id kept verbatim; longer ids are shortened with a digest.
const ID_MAX: usize = 64;
const TEXT_NAME: &str = "正文.txt";
const UNKNOWN_DATE: &str = "未知日期";

const RESERVED: [&str; 28] = [
    "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "COM1", "COM2", "COM3", "COM4", "COM5",
    "COM6", "COM7", "COM8", "COM9", "COM¹", "COM²", "COM³", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5",
    "LPT6", "LPT7", "LPT8", "LPT9", "LPT¹",
];

fn is_illegal(c: char) -> bool {
    matches!(
        c,
        '/' | '\\' | ':' | '*' | '?' | '"' | '<' | '>' | '|' | '\u{7f}'
    ) || c.is_control()
        // Zero-width and direction controls hide what a name really says.
        || matches!(c, '\u{200b}'..='\u{200f}' | '\u{202a}'..='\u{202e}' | '\u{2066}'..='\u{2069}' | '\u{feff}')
}

fn is_reserved(segment: &str) -> bool {
    let stem = segment.split('.').next().unwrap_or_default();
    let upper = stem.trim_end().to_uppercase();
    RESERVED.contains(&upper.as_str()) || upper == "LPT²" || upper == "LPT³"
}

fn trim_end(text: &str) -> &str {
    text.trim_end_matches(['.', ' '])
}

/// One path segment from untrusted text: `max` characters at most, never empty, never a
/// Windows device name, never ending in a dot or space.
pub fn clean_segment(text: &str, fallback: &str, max: usize) -> String {
    let spaced: String = text
        .nfc()
        .map(|c| if is_illegal(c) { ' ' } else { c })
        .collect();
    let joined = spaced.split_whitespace().collect::<Vec<_>>().join(" ");
    let mut segment: String = trim_end(&joined).chars().take(max).collect();
    segment = trim_end(&segment).to_owned();
    if segment.is_empty() {
        segment = fallback.to_owned();
    }
    if is_reserved(&segment) {
        segment.insert(0, '_');
    }
    segment
}

/// The id as it appears in a name: ids that are too long keep a prefix and a digest, so two
/// different ids never share a name.
fn id_part(id: &str) -> String {
    let cleaned = clean_segment(id, "id", usize::MAX);
    if cleaned.chars().count() <= ID_MAX {
        return cleaned;
    }
    let digest: String = Sha256::digest(id.as_bytes())
        .iter()
        .take(4)
        .map(|b| format!("{b:02x}"))
        .collect();
    let head: String = cleaned.chars().take(ID_MAX - 9).collect();
    format!("{head}~{digest}")
}

/// `YYYY-MM-DD` from a stored publication time, or a fixed word when it is unknown.
pub fn date_part(published_at: Option<&str>) -> String {
    published_at
        .map(str::trim)
        .and_then(|s| s.get(..10))
        .filter(|d| {
            let b = d.as_bytes();
            b[4] == b'-'
                && b[7] == b'-'
                && b.iter()
                    .enumerate()
                    .all(|(i, c)| i == 4 || i == 7 || c.is_ascii_digit())
        })
        .map_or_else(|| UNKNOWN_DATE.to_owned(), str::to_owned)
}

fn utf16(text: &str) -> usize {
    text.encode_utf16().count()
}

/// What one entry brings to the folder.
#[derive(Debug, Clone)]
pub struct EntryNames<'a> {
    pub site: &'a str,
    pub author: &'a str,
    pub published_at: Option<&'a str>,
    pub title: &'a str,
    pub id: &'a str,
}

/// One file to place, with the key that identifies it across runs.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Planned {
    pub key: String,
    /// Path below the download folder, `\`-separated.
    pub rel: String,
}

/// A file the entry brings, before it has a name.
#[derive(Debug, Clone)]
pub enum Piece {
    /// A media file; `key` identifies it (the media id), `ext` includes the dot. A file that is
    /// not there yet (deferred, not downloaded) still takes its number, so names stay stable
    /// when it arrives later.
    Media {
        key: String,
        ext: String,
        present: bool,
    },
    /// The entry's text.
    Text,
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum NameError {
    #[error("路径过长：{0} 个字符，超过 {PATH_MAX}；请选择更短的下载目录")]
    TooLong(usize),
}

fn stem_with(names: &EntryNames<'_>, title_chars: usize, suffix: &str) -> String {
    let date = date_part(names.published_at);
    let id = id_part(names.id);
    let title = clean_segment(names.title, "", title_chars);
    let mut stem = if title.is_empty() {
        format!("{date} [{id}]")
    } else {
        format!("{date} {title} [{id}]")
    };
    if !suffix.is_empty() {
        stem.push(' ');
        stem.push_str(suffix);
    }
    stem
}

fn build(
    names: &EntryNames<'_>,
    pieces: &[Piece],
    sidecar: bool,
    title_chars: usize,
    suffix: &str,
) -> Vec<Planned> {
    let site = clean_segment(names.site, "站点", SEGMENT_MAX);
    let author = clean_segment(names.author, "未知作者", SEGMENT_MAX);
    let stem = stem_with(names, title_chars, suffix);
    let media = pieces
        .iter()
        .filter(|p| matches!(p, Piece::Media { .. }))
        .count();
    let folder = media > 1;
    let width = media.to_string().len().max(2);
    let dir = format!("{site}\\{author}");
    let mut out = Vec::new();
    let mut index = 0usize;
    for piece in pieces {
        match piece {
            Piece::Media { key, ext, present } => {
                index += 1;
                if !present {
                    continue;
                }
                let rel = if folder {
                    format!("{dir}\\{stem}\\{index:0width$}{ext}")
                } else {
                    format!("{dir}\\{stem}{ext}")
                };
                out.push(Planned {
                    key: key.clone(),
                    rel,
                });
            }
            Piece::Text => {
                let rel = if folder {
                    format!("{dir}\\{stem}\\{TEXT_NAME}")
                } else if media == 0 {
                    format!("{dir}\\{stem}.txt")
                } else {
                    // Single media: the text lives in the sidecar.
                    continue;
                };
                out.push(Planned {
                    key: "text".into(),
                    rel,
                });
            }
        }
    }
    if sidecar {
        out.push(Planned {
            key: "sidecar".into(),
            rel: format!("{dir}\\{stem}.json"),
        });
    }
    out
}

/// Names every file of one entry below a folder of `root_len` UTF-16 units. The title is cut
/// as far as needed for every path to fit; if even an empty title cannot fit, the entry is
/// refused with the length that was needed. `suffix` distinguishes a name that is taken by
/// something else (a short entry digest).
pub fn layout(
    names: &EntryNames<'_>,
    pieces: &[Piece],
    sidecar: bool,
    root_len: usize,
    suffix: &str,
) -> Result<Vec<Planned>, NameError> {
    let mut needed = 0;
    for title_chars in (0..=SEGMENT_MAX).rev() {
        let planned = build(names, pieces, sidecar, title_chars, suffix);
        let longest = planned
            .iter()
            .map(|p| root_len + 1 + utf16(&p.rel))
            .max()
            .unwrap_or(0);
        let segment_ok = planned
            .iter()
            .all(|p| p.rel.split('\\').all(|s| s.chars().count() <= SEGMENT_MAX));
        if longest <= PATH_MAX && segment_ok {
            return Ok(planned);
        }
        needed = longest;
        if names.title.is_empty() {
            break;
        }
    }
    Err(NameError::TooLong(needed))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn names<'a>(title: &'a str, id: &'a str) -> EntryNames<'a> {
        EntryNames {
            site: "微博",
            author: "作者:甲",
            published_at: Some("2026-10-15T08:00:00Z"),
            title,
            id,
        }
    }

    fn media(key: &str, ext: &str) -> Piece {
        Piece::Media {
            key: key.into(),
            ext: ext.into(),
            present: true,
        }
    }

    #[test]
    fn illegal_characters_and_trailing_dots_are_removed() {
        assert_eq!(
            clean_segment("a/b\\c:d*e?f\"g<h>i|j", "x", 50),
            "a b c d e f g h i j"
        );
        assert_eq!(clean_segment("  title. . ", "x", 50), "title");
        assert_eq!(clean_segment("..", "fallback", 50), "fallback");
        assert_eq!(clean_segment("\u{202e}evil\u{200b}", "x", 50), "evil");
        assert_eq!(clean_segment("tab\tand\nline", "x", 50), "tab and line");
    }

    #[test]
    fn device_names_are_neutralised_with_or_without_extension() {
        for name in ["CON", "nul", "Com1", "LPT9", "aux.txt", "COM¹", "con ."] {
            let cleaned = clean_segment(name, "x", 50);
            assert!(cleaned.starts_with('_'), "{name} -> {cleaned}");
        }
        assert_eq!(clean_segment("console", "x", 50), "console");
        assert_eq!(clean_segment("COM10", "x", 50), "COM10");
    }

    #[test]
    fn segments_are_cut_to_the_limit_in_characters() {
        let long = "字".repeat(300);
        assert_eq!(
            clean_segment(&long, "x", SEGMENT_MAX).chars().count(),
            SEGMENT_MAX
        );
        // A cut that lands after a dot or space does not leave one at the end.
        let tricky = format!("{}. rest", "a".repeat(SEGMENT_MAX - 1));
        assert!(!clean_segment(&tricky, "x", SEGMENT_MAX).ends_with(['.', ' ']));
    }

    #[test]
    fn dates_come_from_the_publication_time_or_a_fixed_word() {
        assert_eq!(date_part(Some("2026-10-15T08:00:00Z")), "2026-10-15");
        assert_eq!(date_part(Some("2026-10-15")), "2026-10-15");
        assert_eq!(date_part(Some("yesterday")), UNKNOWN_DATE);
        assert_eq!(date_part(Some("../../etc")), UNKNOWN_DATE);
        assert_eq!(date_part(None), UNKNOWN_DATE);
    }

    #[test]
    fn single_media_is_one_file_with_a_sidecar_beside_it() {
        let planned = layout(
            &names("标题", "AbC123"),
            &[media("m1", ".mp4")],
            true,
            20,
            "",
        )
        .unwrap();
        assert_eq!(
            planned,
            [
                Planned {
                    key: "m1".into(),
                    rel: "微博\\作者 甲\\2026-10-15 标题 [AbC123].mp4".into()
                },
                Planned {
                    key: "sidecar".into(),
                    rel: "微博\\作者 甲\\2026-10-15 标题 [AbC123].json".into()
                },
            ]
        );
    }

    #[test]
    fn several_media_make_a_same_name_folder_with_numbered_files_and_text() {
        let pieces = [media("m1", ".jpg"), media("m2", ".mp4"), Piece::Text];
        let planned = layout(&names("t", "7"), &pieces, true, 20, "").unwrap();
        let rels: Vec<_> = planned.iter().map(|p| p.rel.as_str()).collect();
        assert_eq!(
            rels,
            [
                "微博\\作者 甲\\2026-10-15 t [7]\\01.jpg",
                "微博\\作者 甲\\2026-10-15 t [7]\\02.mp4",
                "微博\\作者 甲\\2026-10-15 t [7]\\正文.txt",
                "微博\\作者 甲\\2026-10-15 t [7].json",
            ]
        );
    }

    #[test]
    fn a_media_file_that_is_not_there_yet_keeps_its_number() {
        let pieces = [
            Piece::Media {
                key: "m1".into(),
                ext: ".jpg".into(),
                present: false,
            },
            media("m2", ".jpg"),
        ];
        let planned = layout(&names("t", "7"), &pieces, false, 20, "").unwrap();
        assert_eq!(planned.len(), 1);
        assert!(
            planned[0].rel.ends_with("[7]\\02.jpg"),
            "{}",
            planned[0].rel
        );
    }

    #[test]
    fn a_text_only_entry_is_one_text_file() {
        let planned = layout(&names("only text", "9"), &[Piece::Text], false, 20, "").unwrap();
        assert_eq!(planned.len(), 1);
        assert!(planned[0].rel.ends_with("2026-10-15 only text [9].txt"));
    }

    #[test]
    fn single_media_text_stays_out_of_the_folder_layout() {
        let planned = layout(
            &names("t", "1"),
            &[media("m1", ".jpg"), Piece::Text],
            false,
            20,
            "",
        )
        .unwrap();
        assert_eq!(planned.len(), 1);
    }

    #[test]
    fn the_title_is_cut_so_the_whole_path_fits_and_the_id_survives() {
        let title = "很长".repeat(200);
        let root_len = 150;
        let planned = layout(
            &names(&title, "ID-42"),
            &[media("m", ".mp4")],
            true,
            root_len,
            "",
        )
        .unwrap();
        for p in &planned {
            assert!(root_len + 1 + utf16(&p.rel) <= PATH_MAX, "{}", p.rel);
            assert!(p.rel.contains("[ID-42]"), "{}", p.rel);
        }
    }

    #[test]
    fn a_path_that_cannot_fit_even_without_a_title_is_refused() {
        let err = layout(&names("t", "1"), &[media("m", ".mp4")], true, 250, "").unwrap_err();
        assert!(matches!(err, NameError::TooLong(n) if n > PATH_MAX));
        assert!(err.to_string().contains("路径过长"));
    }

    #[test]
    fn long_ids_stay_distinct_after_shortening() {
        let a = "x".repeat(200);
        let b = format!("{}y", "x".repeat(199));
        assert_ne!(id_part(&a), id_part(&b));
        assert!(id_part(&a).chars().count() <= ID_MAX);
    }

    #[test]
    fn a_suffix_separates_a_name_that_is_already_taken() {
        let plain = layout(&names("t", "1"), &[media("m", ".jpg")], false, 20, "").unwrap();
        let marked = layout(
            &names("t", "1"),
            &[media("m", ".jpg")],
            false,
            20,
            "~a1b2c3d4",
        )
        .unwrap();
        assert_ne!(plain, marked);
        assert!(marked[0].rel.contains("[1] ~a1b2c3d4"));
    }

    #[test]
    fn hostile_names_never_escape_their_segment() {
        let hostile = EntryNames {
            site: "..",
            author: "..\\..\\windows",
            published_at: Some("../.."),
            title: "../../x",
            id: "..\\y",
        };
        let planned = layout(&hostile, &[media("m", ".jpg")], true, 20, "").unwrap();
        for p in &planned {
            assert!(
                p.rel
                    .split('\\')
                    .all(|s| s != ".." && s != "." && !s.is_empty()),
                "{}",
                p.rel
            );
            assert_eq!(p.rel.split('\\').count(), 3, "{}", p.rel);
        }
    }
}
