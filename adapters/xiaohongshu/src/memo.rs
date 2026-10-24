//! The `xsec_token` of notes this process has seen.
//!
//! A note page is opened with the token the profile list or a share link gave for it. The
//! token is not a credential, but it is not stored either: it is remembered in memory, for a
//! bounded number of notes, from the list read or the link that named the note until the note
//! is observed.
use crate::link::token_ok;
use std::collections::VecDeque;

/// Notes remembered at most; the oldest are forgotten first.
const NOTES_MAX: usize = 2000;

#[derive(Default)]
pub(crate) struct Memo(VecDeque<(String, String)>);

impl Memo {
    fn remember(&mut self, note: &str, token: &str) {
        if !token_ok(token) {
            return;
        }
        self.0.retain(|(n, _)| n != note);
        self.0.push_back((note.to_owned(), token.to_owned()));
        while self.0.len() > NOTES_MAX {
            self.0.pop_front();
        }
    }

    fn recall(&self, note: &str) -> Option<String> {
        self.0
            .iter()
            .find(|(n, _)| n == note)
            .map(|(_, t)| t.clone())
    }
}

#[cfg(not(test))]
static MEMO: std::sync::Mutex<Option<Memo>> = std::sync::Mutex::new(None);

#[cfg(not(test))]
fn with<R>(f: impl FnOnce(&mut Memo) -> R) -> R {
    f(MEMO.lock().unwrap().get_or_insert_with(Memo::default))
}

// Tests keep one memo per thread, so they stay independent.
#[cfg(test)]
thread_local! {
    static MEMO: std::cell::RefCell<Memo> = std::cell::RefCell::new(Memo::default());
}

#[cfg(test)]
fn with<R>(f: impl FnOnce(&mut Memo) -> R) -> R {
    MEMO.with(|m| f(&mut m.borrow_mut()))
}

pub(crate) fn remember(note: &str, token: &str) {
    with(|m| m.remember(note, token));
}

pub(crate) fn recall(note: &str) -> Option<String> {
    with(|m| m.recall(note))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tokens_are_remembered_per_note_replaced_and_bounded() {
        remember("n1", "abcdefgh1");
        remember("n2", "bad token");
        assert_eq!(recall("n1").as_deref(), Some("abcdefgh1"));
        assert_eq!(recall("n2"), None, "a malformed token is not kept");
        remember("n1", "abcdefgh2");
        assert_eq!(recall("n1").as_deref(), Some("abcdefgh2"));
        for i in 0..NOTES_MAX + 5 {
            remember(&format!("m{i}"), "abcdefgh3");
        }
        assert_eq!(recall("n1"), None, "the oldest note was forgotten");
        assert!(recall(&format!("m{}", NOTES_MAX + 4)).is_some());
        assert_eq!(with(|m| m.0.len()), NOTES_MAX);
    }
}
