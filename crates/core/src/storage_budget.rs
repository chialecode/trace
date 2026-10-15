//! Shared, bounded storage headroom across recording and object writers.
//! Reservations are in-memory guards: cancellation releases them, while files
//! already written remain reflected in the filesystem's free-space counter.
use crate::{config::Profile, error::CoreError};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::Instant,
};

const GIB: u64 = 1024 * 1024 * 1024;
const UNKNOWN_TRANSFER: u64 = 64 * 1024 * 1024;
// Before samples exist, allow a 32 Mbit/s source. Raw and remuxed copies coexist.
const INITIAL_RATE: u64 = 4 * 1024 * 1024;

#[derive(Default)]
pub struct StorageBudget {
    state: Mutex<State>,
}
#[derive(Default)]
struct State {
    recordings: HashMap<String, Rate>,
    transfers: HashMap<String, u64>,
}
struct Rate {
    peak: u64,
    bytes: u64,
    since: Instant,
}
pub struct Reservation {
    budget: Arc<StorageBudget>,
    key: String,
    recording: bool,
}
impl Drop for Reservation {
    fn drop(&mut self) {
        let mut state = self.budget.state.lock().unwrap();
        if self.recording {
            state.recordings.remove(&self.key);
        } else {
            state.transfers.remove(&self.key);
        }
    }
}
impl StorageBudget {
    pub fn recording(self: &Arc<Self>, key: &str) -> Reservation {
        self.state.lock().unwrap().recordings.insert(
            key.into(),
            Rate {
                peak: INITIAL_RATE,
                bytes: 0,
                since: Instant::now(),
            },
        );
        Reservation {
            budget: self.clone(),
            key: key.into(),
            recording: true,
        }
    }
    pub fn transfer(self: &Arc<Self>, expected: Option<i64>) -> Reservation {
        let key = uuid::Uuid::new_v4().to_string();
        self.state
            .lock()
            .unwrap()
            .transfers
            .insert(key.clone(), transfer_reserve(expected, 0));
        Reservation {
            budget: self.clone(),
            key,
            recording: false,
        }
    }
    pub fn observe(&self, key: &str, bytes: usize, source_duration_ms: Option<u64>) {
        if let Some(rate) = self.state.lock().unwrap().recordings.get_mut(key) {
            if let Some(ms) = source_duration_ms.filter(|ms| *ms > 0) {
                rate.peak = rate
                    .peak
                    .max((bytes as u64).saturating_mul(1000).div_ceil(ms));
            } else {
                rate.bytes = rate.bytes.saturating_add(bytes as u64);
                let ms = rate.since.elapsed().as_millis().min(u64::MAX as u128) as u64;
                // Include partial startup bursts rather than waiting for a full second.
                rate.peak = rate
                    .peak
                    .max(rate.bytes.saturating_mul(1000).div_ceil(ms.max(1000)));
                if ms >= 1000 {
                    rate.bytes = 0;
                    rate.since = Instant::now();
                }
            }
        }
    }
    pub fn reserve_bytes(&self, profile: &Profile) -> u64 {
        let s = self.state.lock().unwrap();
        let minute = s.recordings.values().fold(0u64, |sum, r| {
            sum.saturating_add(r.peak.saturating_mul(120))
        });
        s.transfers.values().fold(
            GIB.max(profile.recording_disk_reserve_bytes).max(minute),
            |sum, b| sum.saturating_add(*b),
        )
    }
    pub fn check(&self, profile: &Profile, bytes: usize) -> Result<(), CoreError> {
        let required = self.reserve_bytes(profile).saturating_add(bytes as u64);
        check_free(
            crate::storage_ops::free_bytes(&profile.archive_root).map(|n| n.max(0) as u64),
            required,
        )
    }
}
fn check_free(free: Option<u64>, required: u64) -> Result<(), CoreError> {
    if free.is_some_and(|n| n < required) {
        return Err(CoreError::StorageFull(
            "shared recording/download/replacement reserve reached".into(),
        ));
    }
    #[cfg(windows)]
    if free.is_none() {
        return Err(CoreError::StorageFull("disk free space unavailable".into()));
    }
    Ok(())
}
fn transfer_reserve(expected: Option<i64>, written: u64) -> u64 {
    let size = expected
        .filter(|n| *n >= 0)
        .map(|n| n as u64)
        .unwrap_or(UNKNOWN_TRANSFER.max(written));
    size.saturating_sub(written).saturating_add(size) // remaining input + replacement/mux output
}
impl Reservation {
    pub fn progress(&self, expected: Option<i64>, written: u64) {
        if !self.recording {
            self.budget
                .state
                .lock()
                .unwrap()
                .transfers
                .insert(self.key.clone(), transfer_reserve(expected, written));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn disk_reserve_tracks_concurrent_minute_and_replacement_peak() {
        let b = Arc::new(StorageBudget::default());
        let p = Profile::default();
        assert_eq!(b.reserve_bytes(&p), GIB);
        let one = b.recording("one");
        let two = b.recording("two");
        b.observe("one", 12 * 1024 * 1024, Some(1000));
        let minute = (12 + 4) * 1024 * 1024 * 120;
        assert_eq!(b.reserve_bytes(&p), minute);
        let download = b.transfer(Some(2 * GIB as i64));
        let unknown = b.transfer(None);
        assert_eq!(b.reserve_bytes(&p), minute + 4 * GIB + 2 * UNKNOWN_TRANSFER);
        download.progress(Some(2 * GIB as i64), GIB);
        assert_eq!(b.reserve_bytes(&p), minute + 3 * GIB + 2 * UNKNOWN_TRANSFER);
        assert!(check_free(Some(b.reserve_bytes(&p) - 1), b.reserve_bytes(&p)).is_err());
        assert!(check_free(Some(b.reserve_bytes(&p)), b.reserve_bytes(&p)).is_ok());
        drop((download, unknown, one, two));
        assert_eq!(b.reserve_bytes(&p), GIB);
        assert_eq!(transfer_reserve(Some(i64::MAX), 0), 2 * i64::MAX as u64);
    }
}
