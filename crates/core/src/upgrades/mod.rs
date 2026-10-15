//! Login completion has its own bounded durable lane. Verified candidates stay
//! separate from selections until a user confirms the exact comparison snapshot.
use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
};
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};
use trace_contracts::acquisition::{UpgradeAction, UpgradeDecision};

mod decide;
mod finish;
mod list;
mod prepare;
mod schedule;

pub use decide::decide;
pub use finish::{abandon, finish, invalidate_stale};
pub use list::{list, resources};
pub use prepare::{has_pending, prepare, reused, variant};
pub use schedule::{highest_candidates, run, schedule, schedule_media};

use prepare::{complete, confirmation_current, snapshot};
use schedule::policy;
