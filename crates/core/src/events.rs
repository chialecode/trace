//! 事件中心：瞬时进度广播（可合并丢弃）+ 持久变更经 Outbox 重放。

use tokio::sync::broadcast;
use trace_contracts::events::ProgressEvent;

pub struct EventHub {
    tx: broadcast::Sender<ProgressEvent>,
}

impl EventHub {
    pub fn new() -> Self {
        let (tx, _) = broadcast::channel(1024);
        Self { tx }
    }

    pub fn publish_progress(
        &self,
        job_id: &str,
        progress: f64,
        detail: Option<String>,
        merge_key: &str,
    ) {
        let _ = self.tx.send(ProgressEvent {
            job_id: job_id.into(),
            progress,
            detail,
            merge_key: merge_key.into(),
        });
    }

    pub fn subscribe(&self) -> broadcast::Receiver<ProgressEvent> {
        self.tx.subscribe()
    }
}

impl Default for EventHub {
    fn default() -> Self {
        Self::new()
    }
}
