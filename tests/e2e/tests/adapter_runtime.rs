//! Both production adapters run on the shared stdio runtime. The same
//! queue-full, shutdown and proxy-timeout counterexamples apply to each.
use serde_json::{Value, json};
use std::{
    io::BufReader,
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::mpsc,
    time::{Duration, Instant},
};
use trace_contracts::wire;

/// A request that reaches the proxy boundary and then waits on the host.
fn proxied_request(site: &str, id: &str, deadline_ms: u64) -> Value {
    let input = if site == "bilibili" {
        "123"
    } else {
        "MS4Runtime"
    };
    json!({"protocol_version":trace_domain::PLUGIN_PROTOCOL_VERSION,"request_id":id,"method":"resolve_account","params":{"input":input},"deadline_ms":deadline_ms})
}

struct Adapter {
    child: Child,
    input: std::process::ChildStdin,
    frames: mpsc::Receiver<Value>,
}
impl Adapter {
    fn new(site: &str) -> Self {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join(format!("../../target/debug/trace-adapter-{site}.exe"));
        let mut child = Command::new(path)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("build production adapters before process tests");
        let mut out = BufReader::new(child.stdout.take().unwrap());
        let input = child.stdin.take().unwrap();
        let (tx, frames) = mpsc::channel();
        std::thread::spawn(move || {
            while let Ok(frame) = wire::read_frame::<_, Value>(&mut out) {
                if tx.send(frame).is_err() {
                    break;
                }
            }
        });
        Self {
            child,
            input,
            frames,
        }
    }
    fn send(&mut self, frame: &Value) {
        wire::write_frame(&mut self.input, frame).unwrap();
    }
    fn next(&self, wait: Duration) -> Option<Value> {
        self.frames.recv_timeout(wait).ok()
    }
}
impl Drop for Adapter {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

#[test]
fn adapter_queue_is_bounded_and_full_queue_reports_rate_limit() {
    for site in ["bilibili", "douyin"] {
        let mut adapter = Adapter::new(site);
        let workers = trace_site_support::runtime::WORKERS;
        let capacity = trace_site_support::runtime::QUEUE_CAPACITY;
        let total = workers + capacity + 6;
        for n in 0..total {
            let frame = proxied_request(site, &format!("r{n}"), 30_000);
            adapter.send(&frame);
        }
        // Workers hold the first requests on pending proxy calls, so the proxy
        // requests of the busy workers and the overflow rejections both arrive.
        let (mut proxy, mut limited) = (0, vec![]);
        let started = Instant::now();
        while limited.len() < 6 && started.elapsed() < Duration::from_secs(10) {
            let Some(frame) = adapter.next(Duration::from_secs(2)) else {
                break;
            };
            if frame["method"]
                .as_str()
                .is_some_and(|m| m.starts_with("proxy."))
            {
                proxy += 1;
            } else if frame["ok"] == false {
                limited.push(frame);
            }
        }
        assert!(
            proxy <= workers,
            "{site}: only busy workers may hold proxy calls"
        );
        assert_eq!(
            limited.len(),
            6,
            "{site}: requests beyond the bounded queue fail fast"
        );
        for frame in &limited {
            assert_eq!(frame["error"]["class"], "rate_limited", "{site}");
        }
    }
}

#[test]
fn adapter_proxy_wait_ends_at_the_request_deadline() {
    for site in ["bilibili", "douyin"] {
        let mut adapter = Adapter::new(site);
        adapter.send(&proxied_request(site, "slow", 1_500));
        let started = Instant::now();
        let mut answer = None;
        while answer.is_none() && started.elapsed() < Duration::from_secs(8) {
            let Some(frame) = adapter.next(Duration::from_secs(8)) else {
                break;
            };
            if frame["request_id"] == "slow" && frame.get("method").is_none() {
                answer = Some(frame);
            }
        }
        let answer = answer.unwrap_or_else(|| panic!("{site}: no answer before the deadline"));
        assert_eq!(answer["ok"], false, "{site}");
        assert_eq!(answer["error"]["class"], "transient_network", "{site}");
        assert!(
            started.elapsed() < Duration::from_secs(5),
            "{site}: proxy wait must follow the 1.5s request deadline, not a fixed 25s"
        );
    }
}

#[test]
fn adapter_shutdown_acknowledges_and_exits_with_pending_work() {
    for site in ["bilibili", "douyin"] {
        let mut adapter = Adapter::new(site);
        adapter.send(&proxied_request(site, "pending", 30_000));
        adapter.send(&json!({"protocol_version":trace_domain::PLUGIN_PROTOCOL_VERSION,"request_id":"bye","method":"shutdown","params":{}}));
        let started = Instant::now();
        let mut acknowledged = false;
        while started.elapsed() < Duration::from_secs(5) {
            match adapter.next(Duration::from_millis(500)) {
                Some(frame) if frame["request_id"] == "bye" => {
                    assert_eq!(frame["ok"], true, "{site}");
                    acknowledged = true;
                }
                Some(_) => {}
                None => {
                    if let Ok(Some(_)) = adapter.child.try_wait() {
                        break;
                    }
                }
            }
        }
        assert!(acknowledged, "{site}: shutdown must be acknowledged");
        let exit = adapter
            .child
            .try_wait()
            .unwrap()
            .unwrap_or_else(|| panic!("{site}: adapter must exit after shutdown"));
        assert!(exit.success(), "{site}");
    }
}
