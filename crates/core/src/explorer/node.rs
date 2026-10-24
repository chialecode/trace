//! Addresses of explorer nodes and the one rule for names below a download folder.
//!
//! The interface never builds an address: it hands back what Core produced. Parsing is strict
//! all the same, so a tampered address is refused before it reaches a query or a path.
use crate::error::CoreError;
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Node {
    Library,
    Site(String),
    Account {
        site: String,
        account: String,
    },
    /// `year` is `None` for entries without a publication time.
    Year {
        site: String,
        account: String,
        year: Option<String>,
    },
    Month {
        site: String,
        account: String,
        year: String,
        month: String,
    },
    Entry(String),
    Downloads,
    /// A folder below a download folder; `rel` holds clean names, never `.`, `..` or a path.
    Folder {
        location: String,
        rel: Vec<String>,
    },
}

const ID_MAX: usize = 128;
const SEGMENT_MAX: usize = 255;
/// Deepest folder nesting an address may carry.
pub const DEPTH_MAX: usize = 32;

fn id(text: &str, tag: &str) -> Result<String, CoreError> {
    let value = text.strip_prefix(tag).ok_or_else(|| bad("explorer node"))?;
    if value.is_empty()
        || value.len() > ID_MAX
        || !value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'_' | b'-'))
    {
        return Err(bad("explorer node id"));
    }
    Ok(value.to_owned())
}

fn bad(what: &str) -> CoreError {
    CoreError::InvalidInput(what.into())
}

impl Node {
    pub fn parse(text: &str) -> Result<Node, CoreError> {
        let parts: Vec<&str> = text.split('/').collect();
        match parts.as_slice() {
            ["lib"] => Ok(Node::Library),
            ["dl"] => Ok(Node::Downloads),
            ["lib", e] if e.starts_with("e:") => Ok(Node::Entry(id(e, "e:")?)),
            ["lib", s] => Ok(Node::Site(id(s, "s:")?)),
            ["lib", s, a] => Ok(Node::Account {
                site: id(s, "s:")?,
                account: id(a, "a:")?,
            }),
            ["lib", s, a, y] => Ok(Node::Year {
                site: id(s, "s:")?,
                account: id(a, "a:")?,
                year: year(y)?,
            }),
            ["lib", s, a, y, m] => Ok(Node::Month {
                site: id(s, "s:")?,
                account: id(a, "a:")?,
                year: year(y)?.ok_or_else(|| bad("explorer month"))?,
                month: month(m)?,
            }),
            ["dl", location, rest @ ..] if rest.len() <= DEPTH_MAX => {
                let rel = rest
                    .iter()
                    .map(|segment| decode_segment(segment))
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(Node::Folder {
                    location: id(location, "")?,
                    rel,
                })
            }
            _ => Err(bad("explorer node")),
        }
    }

    pub fn text(&self) -> String {
        match self {
            Node::Library => "lib".into(),
            Node::Downloads => "dl".into(),
            Node::Entry(e) => format!("lib/e:{e}"),
            Node::Site(s) => format!("lib/s:{s}"),
            Node::Account { site, account } => format!("lib/s:{site}/a:{account}"),
            Node::Year {
                site,
                account,
                year,
            } => format!(
                "lib/s:{site}/a:{account}/y:{}",
                year.as_deref().unwrap_or("none")
            ),
            Node::Month {
                site,
                account,
                year,
                month,
            } => format!("lib/s:{site}/a:{account}/y:{year}/m:{month}"),
            Node::Folder { location, rel } => {
                let mut text = format!("dl/{location}");
                for name in rel {
                    text.push('/');
                    text.push_str(&URL_SAFE_NO_PAD.encode(name));
                }
                text
            }
        }
    }

    /// The node of a child folder of a download folder.
    pub fn child_folder(location: &str, rel: &[String], name: &str) -> Node {
        let mut rel = rel.to_vec();
        rel.push(name.to_owned());
        Node::Folder {
            location: location.to_owned(),
            rel,
        }
    }
}

fn year(text: &str) -> Result<Option<String>, CoreError> {
    let value = text
        .strip_prefix("y:")
        .ok_or_else(|| bad("explorer year"))?;
    if value == "none" {
        return Ok(None);
    }
    if value.len() == 4 && value.bytes().all(|b| b.is_ascii_digit()) {
        return Ok(Some(value.to_owned()));
    }
    Err(bad("explorer year"))
}

fn month(text: &str) -> Result<String, CoreError> {
    let value = text
        .strip_prefix("m:")
        .ok_or_else(|| bad("explorer month"))?;
    match value.parse::<u32>() {
        Ok(m) if value.len() == 2 && (1..=12).contains(&m) => Ok(value.to_owned()),
        _ => Err(bad("explorer month")),
    }
}

fn decode_segment(text: &str) -> Result<String, CoreError> {
    let bytes = URL_SAFE_NO_PAD
        .decode(text)
        .map_err(|_| bad("explorer folder"))?;
    let name = String::from_utf8(bytes).map_err(|_| bad("explorer folder"))?;
    clean_name(&name)?;
    Ok(name)
}

const RESERVED: [&str; 22] = [
    "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8",
    "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
];

/// One name below a download folder: no separators, no relative parts, nothing Windows would
/// read as a device, a stream or a different folder.
pub fn clean_name(name: &str) -> Result<(), CoreError> {
    let stem = name
        .split('.')
        .next()
        .unwrap_or_default()
        .to_ascii_uppercase();
    let unsafe_name = name.is_empty()
        || name.chars().count() > SEGMENT_MAX
        || name == "."
        || name == ".."
        || name.ends_with('.')
        || name.ends_with(' ')
        || name
            .chars()
            .any(|c| c.is_control() || "<>:\"/\\|?*".contains(c))
        || RESERVED.contains(&stem.trim_end());
    if unsafe_name {
        return Err(bad("unsafe file name"));
    }
    Ok(())
}

/// A path below a download folder (`\` or `/` separated) as clean names.
pub fn clean_rel(rel: &str) -> Result<Vec<String>, CoreError> {
    if rel.len() > 1024 {
        return Err(bad("path too long"));
    }
    let names: Vec<String> = rel
        .split(['\\', '/'])
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        .collect();
    if names.len() > DEPTH_MAX + 1 {
        return Err(bad("path too deep"));
    }
    for name in &names {
        clean_name(name)?;
    }
    Ok(names)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_node_round_trips_through_its_address() {
        let nodes = [
            Node::Library,
            Node::Downloads,
            Node::Site("weibo".into()),
            Node::Entry("entry-1".into()),
            Node::Account {
                site: "x".into(),
                account: "acct-1".into(),
            },
            Node::Year {
                site: "x".into(),
                account: "a".into(),
                year: Some("2026".into()),
            },
            Node::Year {
                site: "x".into(),
                account: "a".into(),
                year: None,
            },
            Node::Month {
                site: "x".into(),
                account: "a".into(),
                year: "2026".into(),
                month: "03".into(),
            },
            Node::Folder {
                location: "dl-1".into(),
                rel: vec![],
            },
            Node::Folder {
                location: "dl-1".into(),
                rel: vec!["微博".into(), "作者 甲".into(), "a b".into()],
            },
        ];
        for node in nodes {
            assert_eq!(Node::parse(&node.text()).unwrap(), node);
        }
    }

    #[test]
    fn a_tampered_address_is_refused() {
        let slash = URL_SAFE_NO_PAD.encode("a/b");
        let dots = URL_SAFE_NO_PAD.encode("..");
        let drive = URL_SAFE_NO_PAD.encode("C:");
        let device = URL_SAFE_NO_PAD.encode("nul.txt");
        for text in [
            "",
            "lib/",
            "lib/s:",
            "lib/s:a b",
            "lib/s:../x",
            "lib/x:weibo",
            "lib/s:weibo/a:1/y:20x6",
            "lib/s:weibo/a:1/y:none/m:01",
            "lib/s:weibo/a:1/y:2026/m:13",
            "lib/s:weibo/a:1/y:2026/m:1",
            "lib/s:weibo/a:1/y:2026/m:01/extra",
            "dl",
            "dl/",
            "dl/loc/%2e%2e",
            "dl/loc/!!!",
            &format!("dl/loc/{slash}"),
            &format!("dl/loc/{dots}"),
            &format!("dl/loc/{drive}"),
            &format!("dl/loc/{device}"),
            "dl/../x",
        ] {
            if text == "dl" {
                assert_eq!(Node::parse(text).unwrap(), Node::Downloads);
                continue;
            }
            assert!(Node::parse(text).is_err(), "{text:?} must be refused");
        }
        let deep = format!(
            "dl/loc{}",
            format!("/{}", URL_SAFE_NO_PAD.encode("a")).repeat(DEPTH_MAX + 1)
        );
        assert!(Node::parse(&deep).is_err());
    }

    #[test]
    fn names_that_could_leave_the_folder_or_name_a_device_are_refused() {
        for name in [
            "", ".", "..", "a/b", "a\\b", "C:", "a:b", "con", "NUL.txt", "Com1", "x.", "x ", "a|b",
            "a*b", "a?b", "a\u{0}b", "a\nb", "\"q\"",
        ] {
            assert!(clean_name(name).is_err(), "{name:?} must be refused");
        }
        for name in [
            "作者 甲",
            "2026-10-15 标题 [AbC123].mp4",
            "01.jpg",
            ".trace",
            "console",
        ] {
            assert!(clean_name(name).is_ok(), "{name:?} is fine");
        }
        assert_eq!(
            clean_rel("微博\\作者\\x.mp4").unwrap(),
            ["微博", "作者", "x.mp4"]
        );
        assert!(clean_rel("..\\x").is_err());
        assert!(clean_rel("a\\..\\x").is_err());
        assert!(clean_rel(&"a\\".repeat(DEPTH_MAX + 5)).is_err());
    }
}
