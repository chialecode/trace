//! A few recent answers, so that the steps of one acquisition (the detail, then the media of
//! each group of the same work) read the site once instead of once per step. An adapter process
//! serves one credential scope, so what it remembers never crosses accounts.
use std::sync::Mutex;
use std::time::{Duration, Instant};

pub struct Recent<T> {
    slots: Mutex<Vec<(String, Instant, T)>>,
    ttl: Duration,
    capacity: usize,
}

impl<T: Clone> Recent<T> {
    pub const fn new(ttl: Duration, capacity: usize) -> Self {
        Self {
            slots: Mutex::new(Vec::new()),
            ttl,
            capacity,
        }
    }

    /// The answer remembered under `key`, while it is still fresh.
    pub fn get(&self, key: &str) -> Option<T> {
        let mut slots = self.slots.lock().unwrap();
        let ttl = self.ttl;
        slots.retain(|(_, at, _)| at.elapsed() < ttl);
        slots
            .iter()
            .find(|(k, _, _)| k == key)
            .map(|(_, _, value)| value.clone())
    }

    /// Remembers `value`, replacing the older answer of the same key; the oldest one makes
    /// room when the memory is full.
    pub fn put(&self, key: &str, value: T) {
        let mut slots = self.slots.lock().unwrap();
        slots.retain(|(k, _, _)| k != key);
        if slots.len() >= self.capacity {
            slots.remove(0);
        }
        slots.push((key.to_owned(), Instant::now(), value));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_answer_is_served_until_it_is_old_and_the_oldest_gives_way() {
        let memo = Recent::new(Duration::from_secs(60), 2);
        memo.put("a", 1);
        memo.put("b", 2);
        assert_eq!(memo.get("a"), Some(1));
        memo.put("c", 3);
        assert_eq!(memo.get("a"), None, "the oldest answer made room");
        assert_eq!((memo.get("b"), memo.get("c")), (Some(2), Some(3)));
        memo.put("b", 20);
        assert_eq!(memo.get("b"), Some(20), "a newer answer replaces the older");
        let stale = Recent::new(Duration::ZERO, 2);
        stale.put("a", 1);
        assert_eq!(stale.get("a"), None, "an old answer is not served");
    }
}
