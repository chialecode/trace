//! Metadata pacing: a minimum interval between requests per namespace (contracts §6).
//!
//! Tokio's FIFO mutex gives every caller a turn within its priority class. Racing all
//! sleepers at the same deadline let newer scans keep winning while an observe or resolve
//! waited indefinitely under saturation. The two class heads still share the interval and
//! a budget of four urgent turns in a row.
use crate::core::Core;
use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

/// Urgent callers may go this many times in a row before a regular caller gets a turn.
const URGENT_STREAK_LIMIT: usize = 4;
const YIELD_POLL: Duration = Duration::from_millis(100);

/// Counts a caller as waiting until it is dropped, so a cancelled wait never leaves a stale count.
struct Waiting<'a> {
    counts: &'a Mutex<HashMap<String, usize>>,
    key: String,
}

impl<'a> Waiting<'a> {
    fn enter(counts: &'a Mutex<HashMap<String, usize>>, namespace: &str) -> Self {
        *counts
            .lock()
            .unwrap()
            .entry(namespace.to_owned())
            .or_default() += 1;
        Self {
            counts,
            key: namespace.to_owned(),
        }
    }
}

impl Drop for Waiting<'_> {
    fn drop(&mut self) {
        if let Some(n) = self.counts.lock().unwrap().get_mut(&self.key) {
            *n = n.saturating_sub(1);
        }
    }
}

fn count_of(counts: &Mutex<HashMap<String, usize>>, namespace: &str) -> usize {
    counts.lock().unwrap().get(namespace).copied().unwrap_or(0)
}

impl Core {
    /// Waits until the namespace may take another metadata request.
    pub async fn metadata_pacing(&self, namespace: &str) {
        self.metadata_pacing_priority(namespace, false).await;
    }

    pub async fn metadata_pacing_priority(&self, namespace: &str, urgent: bool) {
        let interval_ms = self.profile.metadata_interval_ms()
            + rand::random_range(0..=self.profile.metadata_jitter_ms());
        let own_class = if urgent {
            &self.metadata_urgent
        } else {
            &self.metadata_regular
        };
        let _waiting = Waiting::enter(own_class, namespace);
        let turn = self.pacing_turn(namespace, urgent);
        let _turn = tokio::select! {
            _ = self.cancel.cancelled() => return,
            turn = turn.lock() => turn,
        };
        loop {
            if self.cancel.is_cancelled() {
                return;
            }
            let streak = count_of(&self.metadata_streak, namespace);
            let wait = if self.other_class_goes_first(namespace, urgent, streak) {
                Some(YIELD_POLL)
            } else {
                self.take_slot(namespace, interval_ms, urgent, streak)
            };
            match wait {
                None => return,
                Some(wait) => tokio::select! {
                    _ = self.cancel.cancelled() => return,
                    _ = tokio::time::sleep(wait) => {},
                },
            }
        }
    }

    fn pacing_turn(&self, namespace: &str, urgent: bool) -> Arc<tokio::sync::Mutex<()>> {
        let class = if urgent { "urgent" } else { "regular" };
        self.metadata_turns
            .lock()
            .unwrap()
            .entry(format!("{class}:{namespace}"))
            .or_insert_with(|| Arc::new(tokio::sync::Mutex::new(())))
            .clone()
    }

    /// Urgent callers yield after a full streak while regular ones wait; regular callers
    /// yield while urgent ones wait and the streak is not used up.
    fn other_class_goes_first(&self, namespace: &str, urgent: bool, streak: usize) -> bool {
        let urgent_waiters = count_of(&self.metadata_urgent, namespace);
        let regular_waiters = count_of(&self.metadata_regular, namespace);
        (urgent && regular_waiters > 0 && streak >= URGENT_STREAK_LIMIT)
            || (!urgent && urgent_waiters > 0 && streak < URGENT_STREAK_LIMIT)
    }

    /// Takes the namespace's slot when the interval has passed; otherwise returns the time left.
    fn take_slot(
        &self,
        namespace: &str,
        interval_ms: u64,
        urgent: bool,
        streak: usize,
    ) -> Option<Duration> {
        let min = Duration::from_millis(interval_ms);
        let mut last = self.metadata_last_request.lock().unwrap();
        let elapsed = last
            .get(namespace)
            .map(|t| t.elapsed())
            .unwrap_or(Duration::from_secs(u64::MAX / 2));
        if elapsed < min {
            return Some(min - elapsed);
        }
        last.insert(namespace.to_string(), Instant::now());
        let next_streak = if urgent { streak.saturating_add(1) } else { 0 };
        self.metadata_streak
            .lock()
            .unwrap()
            .insert(namespace.to_owned(), next_streak);
        None
    }
}
