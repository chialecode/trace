//! Shared bounded stdio transport. Adapters have no HTTP client or vault access.
//!
//! One reader thread splits host requests from proxy replies; four workers run
//! the adapter's `dispatch`. The request queue is bounded (a full queue answers
//! `RateLimited`), `shutdown` ends the process, and every proxy call carries the
//! remaining deadline of its request.
use base64::Engine;
use serde_json::Value;
use std::cell::RefCell;
use std::io::Write;
use std::{
    cell::Cell,
    collections::HashMap,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, Instant},
};
use trace_contracts::{
    plugin::{PluginError, PluginRequest, PluginResponse, ProxyFetchParams, ProxyFetchResult},
    wire,
};
use trace_domain::errors::FailureClass;

type Reply = Result<Value, PluginError>;
type Pending = HashMap<String, std::sync::mpsc::SyncSender<Reply>>;

/// Requests waiting for a worker; beyond this the host gets `RateLimited`.
pub const QUEUE_CAPACITY: usize = 32;
pub const WORKERS: usize = 4;
/// Default budget of one proxied request when the caller names none.
pub const DEFAULT_FETCH_TIMEOUT: Duration = Duration::from_secs(25);
/// Ordinary proxy responses stay below one stdio frame.
pub const ORDINARY_MAX_BYTES: u32 = 512 * 1024;

/// Host requests without a deadline get this budget.
pub const DEFAULT_REQUEST_DEADLINE: Duration = Duration::from_secs(30);

thread_local! {
    /// End of the host request this worker is serving; proxy calls never outlive it.
    static REQUEST_END: Cell<Option<Instant>> = const { Cell::new(None) };
    /// Host-provided access context for this request. The proxy receives it back with the
    /// adapter call it belongs to; Core uses only its opaque evidence identity there.
    static REQUEST_CONTEXT: RefCell<Value> = const { RefCell::new(Value::Null) };
}

/// One bounded request through the host's constrained HTTP proxy.
#[derive(Debug, Clone)]
pub struct Fetch<'a> {
    pub method: &'a str,
    pub url: &'a str,
    pub headers: Value,
    pub credential: bool,
    /// Query text of a read-only POST (a form or JSON document, as the headers say).
    pub body: Option<&'a str>,
    pub max_bytes: u32,
    pub timeout: Duration,
}

impl<'a> Fetch<'a> {
    pub fn get(url: &'a str, headers: Value, credential: bool) -> Self {
        Self {
            method: "GET",
            url,
            headers,
            credential,
            body: None,
            max_bytes: ORDINARY_MAX_BYTES,
            timeout: DEFAULT_FETCH_TIMEOUT,
        }
    }
    pub fn large(mut self) -> Self {
        self.max_bytes = trace_contracts::plugin_chunks::METADATA_MAX;
        self
    }
    pub fn with_timeout(mut self, timeout: Duration) -> Self {
        self.timeout = timeout;
        self
    }
    pub fn with_method(mut self, method: &'a str) -> Self {
        self.method = method;
        self
    }
    /// A POST carrying `body`; the caller's headers must name its content type.
    pub fn post(url: &'a str, headers: Value, body: &'a str, credential: bool) -> Self {
        Self {
            method: "POST",
            body: Some(body),
            ..Self::get(url, headers, credential)
        }
    }
}

pub struct Client {
    output: Mutex<std::io::Stdout>,
    pending: Mutex<Pending>,
    serial: AtomicU64,
}

/// The one thing a site module needs from the network: a bounded request through the host's
/// proxy. The real [`Client`] speaks the plugin protocol; tests answer from canned responses.
pub trait Transport {
    fn request(&self, fetch: &Fetch) -> Result<ProxyFetchResult, PluginError>;
}

impl Transport for Client {
    fn request(&self, fetch: &Fetch) -> Result<ProxyFetchResult, PluginError> {
        Client::request(self, fetch)
    }
}

impl Client {
    fn send(&self, v: &impl serde::Serialize) -> Result<(), PluginError> {
        let bytes = trace_contracts::plugin_chunks::encode(v)
            .map_err(|_| crate::error(FailureClass::InvalidPayload, "plugin message budget"))?;
        let mut out = self.output.lock().unwrap();
        out.write_all(&bytes)
            .and_then(|()| out.flush())
            .map_err(|_| crate::error(FailureClass::PluginCrash, "stdio write failed"))
    }
    pub fn get(
        &self,
        url: &str,
        headers: Value,
        credential: bool,
    ) -> Result<ProxyFetchResult, PluginError> {
        self.request(&Fetch::get(url, headers, credential))
    }
    pub fn fetch(
        &self,
        method: &str,
        url: &str,
        headers: Value,
        credential: bool,
    ) -> Result<ProxyFetchResult, PluginError> {
        self.request(&Fetch::get(url, headers, credential).with_method(method))
    }
    /// The one place a proxy request is built, sent and awaited.
    pub fn request(&self, fetch: &Fetch) -> Result<ProxyFetchResult, PluginError> {
        let id = format!("proxy-{}", self.serial.fetch_add(1, Ordering::Relaxed));
        let (tx, rx) = std::sync::mpsc::sync_channel(1);
        self.pending.lock().unwrap().insert(id.clone(), tx);
        let params = ProxyFetchParams {
            method: fetch.method.into(),
            url: fetch.url.into(),
            headers: fetch.headers.clone(),
            max_bytes: fetch.max_bytes,
            with_credential: fetch.credential,
            body: fetch.body.map(str::to_owned),
        };
        let large = fetch.max_bytes > ORDINARY_MAX_BYTES;
        let timeout = REQUEST_END.get().map_or(fetch.timeout, |end| {
            fetch
                .timeout
                .min(end.saturating_duration_since(Instant::now()))
        });
        let req = PluginRequest {
            protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
            request_id: id.clone(),
            deadline_ms: Some(timeout.as_millis().min(u64::MAX as u128) as u64),
            method: if large {
                "proxy.fetch_large"
            } else {
                "proxy.fetch"
            }
            .into(),
            params: serde_json::to_value(params).unwrap(),
            context: REQUEST_CONTEXT.with(|context| context.borrow().clone()),
        };
        let result = self.send(&req).and_then(|()| {
            rx.recv_timeout(timeout.max(Duration::from_millis(500)))
                .map_err(|_| {
                    crate::error(FailureClass::TransientNetwork, "proxy deadline exceeded")
                })?
        });
        self.pending.lock().unwrap().remove(&id);
        serde_json::from_value(result?)
            .map_err(|_| crate::error(FailureClass::InvalidPayload, "invalid proxy response"))
    }
    pub fn json(&self, url: &str, headers: Value, credential: bool) -> Result<Value, PluginError> {
        let result = self.get(url, headers, credential)?;
        decode_json(result)
    }
    /// A read-only query as a POST, answered with JSON.
    pub fn post_json(
        &self,
        url: &str,
        headers: Value,
        body: &str,
        credential: bool,
    ) -> Result<Value, PluginError> {
        decode_json(self.request(&Fetch::post(url, headers, body, credential).large())?)
    }
    pub fn json_large(
        &self,
        url: &str,
        headers: Value,
        credential: bool,
    ) -> Result<Value, PluginError> {
        decode_json(self.request(&Fetch::get(url, headers, credential).large())?)
    }
}

/// Status → failure class shared by every site.
pub fn classify_http(status: u16) -> FailureClass {
    match status {
        401 | 403 => FailureClass::PermissionDenied,
        412 | 429 => FailureClass::RateLimited,
        404 => FailureClass::SourceUnavailable,
        500..=599 => FailureClass::TransientNetwork,
        _ => FailureClass::SourceUnavailable,
    }
}

/// Decoded response body bytes of a proxied reply.
pub fn body_bytes(result: &ProxyFetchResult) -> Result<Vec<u8>, PluginError> {
    base64::engine::general_purpose::STANDARD
        .decode(&result.body_b64)
        .map_err(|_| crate::error(FailureClass::InvalidPayload, "invalid proxy body"))
}

pub fn decode_json(result: ProxyFetchResult) -> Result<Value, PluginError> {
    if result.status != 200 {
        return Err(crate::error(
            classify_http(result.status),
            "site HTTP failure",
        ));
    }
    serde_json::from_slice(&body_bytes(&result)?).map_err(|_| {
        crate::error(
            FailureClass::SchemaChanged,
            "empty or non-JSON site response",
        )
    })
}

/// Serve an adapter that keeps no state outside its `dispatch` function.
pub fn serve(dispatch: fn(&Client, &PluginRequest) -> Reply) {
    struct Stateless(fn(&Client, &PluginRequest) -> Reply);
    serve_with(Stateless(dispatch), |s, c, r| (s.0)(c, r));
}

/// Serve an adapter with state shared by all workers (for example a signing-key
/// cache). `dispatch` gets the shared state, the proxy client and the request.
pub fn serve_with<S: Send + Sync + 'static>(
    shared: S,
    dispatch: fn(&S, &Client, &PluginRequest) -> Reply,
) {
    let client = Arc::new(Client {
        output: Mutex::new(std::io::stdout()),
        pending: Mutex::new(HashMap::new()),
        serial: AtomicU64::new(1),
    });
    let shared = Arc::new(shared);
    let (tx, rx) = std::sync::mpsc::sync_channel::<PluginRequest>(QUEUE_CAPACITY);
    let rx = Arc::new(Mutex::new(rx));
    for _ in 0..WORKERS {
        let client = client.clone();
        let shared = shared.clone();
        let rx = rx.clone();
        std::thread::spawn(move || {
            loop {
                let Ok(req) = rx.lock().unwrap().recv() else {
                    break;
                };
                let result = if req.protocol_version != trace_domain::PLUGIN_PROTOCOL_VERSION {
                    Err(crate::error(FailureClass::Unsupported, "protocol mismatch"))
                } else {
                    let budget = req
                        .deadline_ms
                        .map_or(DEFAULT_REQUEST_DEADLINE, Duration::from_millis);
                    REQUEST_END.set(Some(Instant::now() + budget));
                    REQUEST_CONTEXT.with(|context| *context.borrow_mut() = req.context.clone());
                    let result = dispatch(&shared, &client, &req);
                    REQUEST_END.set(None);
                    REQUEST_CONTEXT.with(|context| *context.borrow_mut() = Value::Null);
                    result
                };
                let (result, error) = match result {
                    Ok(v) => (Some(v), None),
                    Err(e) => (None, Some(e)),
                };
                let _ = client.send(&PluginResponse {
                    request_id: req.request_id,
                    ok: error.is_none(),
                    result,
                    error,
                });
            }
        });
    }
    let mut input = std::io::BufReader::new(std::io::stdin());
    let mut chunks = trace_contracts::plugin_chunks::Decoder::default();
    while let Ok(value) = wire::read_frame::<_, Value>(&mut input) {
        let value = match chunks.push(value) {
            Ok(Some(v)) => v,
            Ok(None) => continue,
            Err(_) => break,
        };
        if value.get("method").is_some() {
            if let Ok(req) = serde_json::from_value::<PluginRequest>(value) {
                if matches!(req.method.as_str(), "shutdown" | "cancel") {
                    let _ = client.send(&PluginResponse {
                        request_id: req.request_id,
                        ok: true,
                        result: Some(serde_json::json!({"ack":true})),
                        error: None,
                    });
                    if req.method == "shutdown" {
                        break;
                    }
                } else if let Err(std::sync::mpsc::TrySendError::Full(req)) = tx.try_send(req) {
                    let _ = client.send(&PluginResponse {
                        request_id: req.request_id,
                        ok: false,
                        result: None,
                        error: Some(crate::error(
                            FailureClass::RateLimited,
                            "adapter queue full",
                        )),
                    });
                }
            }
        } else if let Ok(resp) = serde_json::from_value::<PluginResponse>(value)
            && let Some(tx) = client.pending.lock().unwrap().remove(&resp.request_id)
        {
            let _ = tx.send(if resp.ok {
                Ok(resp.result.unwrap_or_default())
            } else {
                Err(resp
                    .error
                    .unwrap_or_else(|| crate::error(FailureClass::Unknown, "proxy failure")))
            });
        }
    }
}
