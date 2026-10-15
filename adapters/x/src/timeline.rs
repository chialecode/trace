//! A timeline answer: the posts it holds, the pinned one, and the cursor that goes on.
use crate::tweet;
use serde_json::Value;
use trace_site_support::kit::{page::find_all, string};

/// What one page of a timeline held.
#[derive(Default)]
pub(crate) struct Page<'a> {
    /// Posts in the order of the page, with whether the site pins them.
    pub posts: Vec<(&'a Value, bool)>,
    /// Entries that were not posts the adapter can read (tombstones, ads, prompts).
    pub unreadable: usize,
    pub cursor: Option<String>,
    /// The page carried its instructions at all.
    pub has_instructions: bool,
}

fn results_of(entry: &Value) -> Vec<&Value> {
    find_all(&entry["content"], &["tweet_results"])
        .into_iter()
        .map(|(_, v)| &v["result"])
        .collect()
}

/// Reads the instructions of a user timeline, wherever the answer nests them.
pub(crate) fn read(answer: &Value) -> Page<'_> {
    let mut page = Page::default();
    for (_, instructions) in find_all(&answer["data"], &["instructions"]) {
        let Some(list) = instructions.as_array() else {
            continue;
        };
        page.has_instructions = true;
        for instruction in list {
            let pinned = instruction["type"].as_str() == Some("TimelinePinEntry");
            let entries: Vec<&Value> = if pinned {
                vec![&instruction["entry"]]
            } else {
                instruction["entries"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .collect()
            };
            for entry in entries {
                let id = entry["entryId"].as_str().unwrap_or_default();
                if id.starts_with("cursor-bottom") {
                    page.cursor = string(&entry["content"]["value"]).or(page.cursor.take());
                } else if id.starts_with("tweet-") || id.starts_with("profile-conversation-") {
                    for result in results_of(entry) {
                        match tweet::unwrap(result) {
                            Some(post) => page.posts.push((post, pinned)),
                            None => page.unreadable += 1,
                        }
                    }
                }
            }
        }
    }
    page
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn posts_pins_conversations_and_the_bottom_cursor_are_found_in_either_nesting() {
        let post =
            |id: &str| json!({"tweet_results": {"result": {"__typename": "Tweet", "rest_id": id}}});
        for path in ["timeline_v2", "timeline"] {
            let mut timeline = json!({"timeline": {"instructions": [
            {"type": "TimelinePinEntry", "entry": {"entryId": "tweet-9", "content": {"itemContent": post("9")}}},
            {"type": "TimelineAddEntries", "entries": [
                {"entryId": "tweet-1", "content": {"itemContent": post("1")}},
                {"entryId": "profile-conversation-2", "content": {"items": [{"item": {"itemContent": post("2")}}, {"item": {"itemContent": post("3")}}]}},
                {"entryId": "tweet-4", "content": {"itemContent": {"tweet_results": {"result": {"__typename": "TweetTombstone"}}}}},
                {"entryId": "cursor-top-1", "content": {"value": "TOP"}},
                {"entryId": "cursor-bottom-1", "content": {"value": "BOTTOM"}},
            ]}]}});
            let wrapped = if path == "timeline" {
                json!({"timeline": timeline["timeline"].take()})
            } else {
                timeline
            };
            let answer = json!({"data": {"user": {"result": {path: wrapped}}}});
            let page = read(&answer);
            let ids: Vec<_> = page
                .posts
                .iter()
                .map(|(p, pin)| (p["rest_id"].as_str().unwrap(), *pin))
                .collect();
            assert_eq!(
                ids,
                [("9", true), ("1", false), ("2", false), ("3", false)],
                "{path}"
            );
            assert_eq!(page.unreadable, 1);
            assert_eq!(page.cursor.as_deref(), Some("BOTTOM"));
            assert!(page.has_instructions);
        }
        assert!(!read(&json!({"data": {"user": {}}})).has_instructions);
    }
}
