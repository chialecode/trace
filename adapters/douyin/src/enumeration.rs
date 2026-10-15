//! One bounded request per step. Post, mix-list, series-list and collection
//! cursors are independent and durable; a source error never completes a sweep.
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use trace_contracts::plugin::{EnumerateParams, EnumeratedPage, PluginError};
use trace_domain::errors::FailureClass;
use trace_site_support::{error, id, runtime::Client};

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(tag = "source", rename_all = "snake_case", deny_unknown_fields)]
enum Cursor {
    Post {
        cursor: String,
    },
    List {
        cursor: String,
        series: bool,
    },
    Items {
        ids: Vec<String>,
        cursor: String,
        next_list: Option<String>,
        series: bool,
    },
}
fn invalid() -> PluginError {
    error(
        FailureClass::SchemaChanged,
        "invalid Douyin collection page or cursor",
    )
}
fn number(s: &str) -> bool {
    !s.is_empty() && s.len() <= 32 && s.bytes().all(|b| b.is_ascii_digit())
}
impl Cursor {
    fn decode(raw: Option<&str>) -> Result<Self, PluginError> {
        let raw = raw.unwrap_or("0");
        if number(raw) {
            return Ok(Self::Post { cursor: raw.into() });
        }
        if raw.len() > 4096 {
            return Err(invalid());
        }
        let parsed: Self = serde_json::from_str(raw.strip_prefix("dy1:").ok_or_else(invalid)?)
            .map_err(|_| invalid())?;
        let valid = match &parsed {
            Self::Post { cursor } | Self::List { cursor, .. } => number(cursor),
            Self::Items {
                ids,
                cursor,
                next_list,
                ..
            } => {
                !ids.is_empty()
                    && ids.len() <= 20
                    && ids.iter().all(|s| number(s))
                    && number(cursor)
                    && next_list.as_deref().is_none_or(number)
            }
        };
        if !valid {
            return Err(invalid());
        }
        Ok(parsed)
    }
    fn encode(&self) -> String {
        format!(
            "dy1:{}",
            serde_json::to_string(self).expect("cursor serialization")
        )
    }
    fn request(&self, account: &str) -> (&'static str, Vec<(&'static str, String)>) {
        let (path, mut params) = match self {
            Self::Post { cursor } => (
                "/aweme/v1/web/aweme/post/",
                vec![
                    ("sec_user_id", account.into()),
                    ("max_cursor", cursor.clone()),
                ],
            ),
            Self::List {
                cursor,
                series: false,
            } => (
                "/aweme/v1/web/mix/list/",
                vec![
                    ("sec_user_id", account.into()),
                    ("cursor", cursor.clone()),
                    ("req_from", "channel_pc_web".into()),
                ],
            ),
            Self::List {
                cursor,
                series: true,
            } => (
                "/aweme/v1/web/series/list/",
                vec![
                    ("sec_user_id", account.into()),
                    ("cursor", cursor.clone()),
                    ("req_from", "channel_pc_web".into()),
                    ("read_new_mix", "true".into()),
                ],
            ),
            Self::Items { ids, cursor, .. } => (
                "/aweme/v1/web/mix/aweme/",
                vec![("mix_id", ids[0].clone()), ("cursor", cursor.clone())],
            ),
        };
        params.push(("count", "20".into()));
        (path, params)
    }
}

pub fn enumerate(client: &Client, params: &EnumerateParams) -> Result<EnumeratedPage, PluginError> {
    let cursor = Cursor::decode(params.cursor.as_deref())?;
    let (path, query) = cursor.request(&params.native_account_key);
    let query = query
        .iter()
        .map(|(k, v)| (*k, v.as_str()))
        .collect::<Vec<_>>();
    let reply = super::protocol::api(client, path, &query)?;
    advance(
        cursor,
        reply,
        &params.content_type,
        &params.native_account_key,
    )
}
fn more(v: &Value) -> Result<bool, PluginError> {
    match &v["has_more"] {
        Value::Bool(b) => Ok(*b),
        Value::Number(n) if n.as_u64().is_some_and(|n| n <= 1) => Ok(n.as_u64() == Some(1)),
        _ => Err(invalid()),
    }
}
fn list_continuation(series: bool, next: Option<String>) -> Option<Cursor> {
    if let Some(cursor) = next {
        Some(Cursor::List { cursor, series })
    } else if !series {
        Some(Cursor::List {
            cursor: "0".into(),
            series: true,
        })
    } else {
        None
    }
}
fn check_authors(v: &Value, account: &str) -> Result<(), PluginError> {
    let entries = v["aweme_list"].as_array().ok_or_else(invalid)?;
    if entries.len() > 20
        || entries.iter().any(|entry| {
            entry["author"]["sec_uid"]
                .as_str()
                .is_some_and(|key| key != account)
        })
    {
        return Err(invalid());
    }
    Ok(())
}
fn advance(
    cursor: Cursor,
    mut v: Value,
    lane: &str,
    account: &str,
) -> Result<EnumeratedPage, PluginError> {
    let mut page;
    let next;
    match cursor {
        Cursor::Post { cursor: previous } => {
            check_authors(&v, account)?;
            page = super::protocol::page(&v, lane)?;
            next = if page.has_more {
                let cursor = page
                    .next_cursor
                    .take()
                    .filter(|s| number(s) && s != &previous)
                    .ok_or_else(invalid)?;
                Some(Cursor::Post { cursor })
            } else {
                Some(Cursor::List {
                    cursor: "0".into(),
                    series: false,
                })
            };
        }
        Cursor::List {
            cursor: previous,
            series,
        } => {
            let has_more = more(&v)?;
            let next_list = if has_more {
                Some(
                    id(&v["cursor"])
                        .filter(|s| number(s) && s != &previous)
                        .ok_or_else(invalid)?,
                )
            } else {
                None
            };
            let list = if series {
                v.get("series_infos")
            } else {
                v.get("mix_infos").or_else(|| v.get("mix_list"))
            }
            .ok_or_else(invalid)?;
            let empty = Vec::new();
            let list = if list.is_null() && !has_more {
                &empty
            } else {
                list.as_array().ok_or_else(invalid)?
            };
            if list.len() > 20 {
                return Err(invalid());
            }
            let mut ids = Vec::new();
            for item in list {
                let item = item
                    .get("mix_info")
                    .filter(|v| v.is_object())
                    .unwrap_or(item);
                if item["author"]["sec_uid"]
                    .as_str()
                    .is_some_and(|key| key != account)
                {
                    return Err(invalid());
                }
                let key = id(&item[if series { "series_id" } else { "mix_id" }])
                    .filter(|s| number(s))
                    .ok_or_else(invalid)?;
                if !ids.contains(&key) {
                    ids.push(key)
                }
            }
            next = if ids.is_empty() {
                list_continuation(series, next_list)
            } else {
                Some(Cursor::Items {
                    ids,
                    cursor: "0".into(),
                    next_list,
                    series,
                })
            };
            page = EnumeratedPage {
                items: vec![],
                next_cursor: None,
                has_more: false,
                coverage: json!({"source":if series {"series.list"} else {"mix.list"},"collections_observed":list.len(),"history":"unknown"}),
            };
        }
        Cursor::Items {
            mut ids,
            cursor: previous,
            next_list,
            series,
        } => {
            check_authors(&v, account)?;
            let key = ids[0].clone();
            v["max_cursor"] = v["cursor"].clone();
            page = super::protocol::page(&v, lane)?;
            page.coverage = json!({"source":"mix.aweme","collection_id":key,"collection_source":if series {"series"} else {"mix"},"observed_count":v["aweme_list"].as_array().map(Vec::len),"history":"unknown"});
            next = if page.has_more {
                let cursor = page
                    .next_cursor
                    .take()
                    .filter(|s| number(s) && s != &previous)
                    .ok_or_else(invalid)?;
                Some(Cursor::Items {
                    ids,
                    cursor,
                    next_list,
                    series,
                })
            } else {
                ids.remove(0);
                if ids.is_empty() {
                    list_continuation(series, next_list)
                } else {
                    Some(Cursor::Items {
                        ids,
                        cursor: "0".into(),
                        next_list,
                        series,
                    })
                }
            };
        }
    }
    page.has_more = next.is_some();
    page.next_cursor = next.map(|c| c.encode());
    page.coverage["all_sources_exhausted"] = json!(!page.has_more);
    Ok(page)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn c01_collections_keep_independent_bounded_cursors_and_reject_incomplete_pages() {
        let mut cursor = Cursor::decode(None).unwrap();
        let steps = [
            (
                "aweme/post",
                json!({"aweme_list":[],"has_more":0,"max_cursor":0}),
                0,
            ),
            (
                "mix/list",
                json!({"mix_infos":[{"mix_id":"90071992547409931"},{"mix_info":{"mix_id":"2"}}],"has_more":1,"cursor":20}),
                0,
            ),
            (
                "mix/aweme",
                json!({"aweme_list":[{"aweme_id":"3","video":{},"create_time":1}],"has_more":1,"cursor":1}),
                1,
            ),
            (
                "mix/aweme",
                json!({"aweme_list":[],"has_more":1,"cursor":2}),
                0,
            ),
            (
                "mix/aweme",
                json!({"aweme_list":[{"aweme_id":"1","aweme_type":163}],"has_more":0,"cursor":3}),
                1,
            ),
            (
                "mix/aweme",
                json!({"aweme_list":[{"aweme_id":"2","images":[]}],"has_more":0,"cursor":1}),
                1,
            ),
            (
                "mix/list",
                json!({"mix_infos":[],"has_more":0,"cursor":0}),
                0,
            ),
            (
                "series/list",
                json!({"series_infos":[{"series_id":"4"}],"has_more":1,"cursor":99}),
                0,
            ),
            (
                "mix/aweme",
                json!({"aweme_list":[{"aweme_id":"4","video":{}}],"has_more":0,"cursor":1}),
                1,
            ),
            (
                "series/list",
                json!({"series_infos":null,"has_more":0,"cursor":0}),
                0,
            ),
        ];
        let mut visited = std::collections::HashSet::new();
        for (index, (path, response, count)) in steps.iter().enumerate() {
            let (request, query) = cursor.request("MS4fixture");
            assert!(request.contains(path));
            if *path == "series/list" {
                assert!(query.contains(&("read_new_mix", "true".into())))
            }
            let page = advance(cursor.clone(), response.clone(), "all", "MS4fixture").unwrap();
            assert_eq!(page.items.len(), *count);
            assert_eq!(page.has_more, index + 1 < steps.len());
            if let Some(next) = page.next_cursor {
                assert!(visited.insert(next.clone()));
                cursor = Cursor::decode(Some(&next)).unwrap();
            }
        }
        for bad in [
            json!({}),
            json!({"mix_infos":[],"has_more":true,"cursor":0}),
            json!({"mix_infos":[{"mix_id":"x"}],"has_more":false}),
        ] {
            assert!(
                advance(
                    Cursor::List {
                        cursor: "0".into(),
                        series: false
                    },
                    bad,
                    "all",
                    "MS4fixture"
                )
                .is_err()
            );
        }
        assert!(advance(Cursor::decode(None).unwrap(),json!({"aweme_list":[{"aweme_id":"1","video":{},"author":{"sec_uid":"MS4other"}}],"has_more":0}),"all","MS4fixture").is_err());
        assert!(Cursor::decode(Some("dy1:{\"source\":\"items\",\"ids\":[],\"cursor\":\"0\",\"next_list\":null,\"series\":false}")).is_err());
        assert!(Cursor::decode(Some("https://private.invalid/?secret=1")).is_err());
    }
}
