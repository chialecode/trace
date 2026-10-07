//! Deterministic partial-frame/proxy completion race; never a production adapter.
use serde_json::{Value, json};
use std::io::Write;
use trace_contracts::wire;
fn main() {
    let mut input = std::io::BufReader::new(std::io::stdin());
    let mut out = std::io::stdout();
    while let Ok(r) = wire::read_frame::<_, Value>(&mut input) {
        if r["method"] == "hello" {
            wire::write_frame(&mut out,&json!({"request_id":r["request_id"],"ok":true,"result":{"protocol_version":"0.1","adapter_version":"fixture","platform":r["params"]["platform"],"capabilities":[],"limits":{"max_page_items":1,"supports_cancel":false}}})).unwrap();
        } else if r["method"] == "race" {
            wire::write_frame(&mut out,&json!({"protocol_version":"0.1","request_id":"proxy","method":"proxy.fetch","params":{"url":r["params"]["url"],"method":"GET","headers":{},"max_bytes":1024,"with_credential":false}})).unwrap();
            let response=wire::encode_frame(&json!({"request_id":r["request_id"],"ok":true,"result":{"payload":"中".repeat(65536)}})).unwrap();
            out.write_all(&response[..6]).unwrap();
            out.flush().unwrap();
            // The host must finish proxy.fetch before it can receive the rest.
            let proxy: Value = wire::read_frame(&mut input).unwrap();
            assert_eq!(proxy["request_id"], "proxy");
            std::thread::sleep(std::time::Duration::from_millis(50));
            out.write_all(&response[6..]).unwrap();
            out.flush().unwrap();
        }
    }
}
