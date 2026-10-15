#[cfg(test)]
mod tests {
    use base64::Engine;
    use serde_json::{Value, json};
    use std::io::{BufReader, Write};
    use std::path::PathBuf;
    use std::process::{Child, Command, Stdio};
    use std::sync::mpsc;
    use std::time::Duration;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use trace_contracts::wire;
    use trace_core::config::Profile;
    use trace_core::transport::{ConstrainedClient, host_matches, host_rules};

    struct OwnedChild(Child);
    impl Drop for OwnedChild {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    #[test]
    fn known_live_media_cdns_work_without_user_allowlist_or_cookies() {
        let rules = host_rules(&Profile::default());
        let missing = ["sample.bytefcdnrd.com", "sample.douyinliving.com"]
            .into_iter()
            .filter(|host| !host_matches(host, &rules.media_hosts))
            .collect::<Vec<_>>();
        assert!(
            missing.is_empty(),
            "source-returned live CDN families are missing: {missing:?}"
        );
        assert!(!host_matches("sample.bytefcdnrd.com", &rules.api_hosts));
        assert!(!host_matches("sample.douyinliving.com", &rules.api_hosts));
        assert!(!host_matches(
            "sample.douyinliving.com.evil.example",
            &rules.media_hosts
        ));
        let bilibili = trace_core::transport::platform_rules(&rules, "bilibili");
        let douyin = trace_core::transport::platform_rules(&rules, "douyin");
        assert!(!host_matches(
            "sample.douyinliving.com",
            &bilibili.media_hosts
        ));
        assert!(!host_matches("sample.bytefcdnrd.com", &douyin.media_hosts));
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn production_post_page_survives_bounded_metadata_transport() {
        let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
        let binary = root.join("target/debug/trace-adapter-douyin.exe");
        let mut command = Command::new(binary);
        command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null());
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            command.creation_flags(0x08000000);
        }
        let mut child = OwnedChild(command.spawn().unwrap());
        let mut input = child.0.stdin.take().unwrap();
        let output = child.0.stdout.take().unwrap();
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let mut output = BufReader::new(output);
            while let Ok(frame) = wire::read_frame::<_, Value>(&mut output) {
                if tx.send(frame).is_err() {
                    break;
                }
            }
        });
        wire::write_frame(&mut input, &json!({
            "protocol_version":"0.1", "request_id":"large-page", "method":"enumerate", "deadline_ms":10000,
            "params":{"native_account_key":"synthetic-owner", "content_type":"video", "mode":"incremental", "max_pages":1}
        })).unwrap();
        let request = rx.recv_timeout(Duration::from_secs(5)).unwrap();
        assert_eq!(request["method"], "proxy.fetch_large");
        assert!(
            request["params"]["url"]
                .as_str()
                .unwrap()
                .contains("/aweme/v1/web/aweme/post/")
        );
        let max_bytes = request["params"]["max_bytes"].as_u64().unwrap() as u32;

        // Twenty source entries with verbose source metadata. No real identity,
        // Cookie, URLs or copied account data is used. Size matches the valid
        // 20-item response observed on the explicitly authorized first page.
        let items = (0..20).map(|i| json!({
            "aweme_id":format!("900000000000000{i:02}"), "aweme_type":0, "desc":"synthetic video",
            "create_time":1700000000, "author":{"sec_uid":"synthetic-owner"}, "statistics":{}, "video":{}
        })).collect::<Vec<_>>();
        let mut page = json!({"status_code":0, "aweme_list":items, "has_more":1, "max_cursor":"1700000000000", "source_padding":""});
        let original_length = serde_json::to_vec(&page).unwrap().len();
        let observed_length = 1_454_679;
        page["source_padding"] = Value::String("x".repeat(observed_length - original_length));
        let body = serde_json::to_vec(&page).unwrap();
        assert_eq!(body.len(), observed_length);
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut input = [0; 8192];
            let _ = socket.read(&mut input).await;
            let header = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            );
            let _ = socket.write_all(header.as_bytes()).await;
            let _ = socket.write_all(&body).await;
        });
        let profile = Profile {
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Profile::default()
        };
        let http = ConstrainedClient::build().unwrap();
        let fetched = http
            .fetch_metadata(
                &host_rules(&profile),
                "GET",
                &format!("http://{address}/post"),
                None,
                None,
                max_bytes,
            )
            .await;
        let reply = match fetched {
            Ok(value) => json!({"request_id":request["request_id"], "ok":true, "result":{
                "status":200, "headers":{}, "final_url":request["params"]["url"],
                "body_b64":base64::engine::general_purpose::STANDARD.encode(value.body)
            }}),
            Err(error) => {
                println!(
                    "sourceBytes={observed_length} requestedMaxBytes={max_bytes} coreFailure={:?}",
                    error.failure_class()
                );
                json!({"request_id":request["request_id"], "ok":false, "error":{
                    "class":error.failure_class(), "message":"bounded proxy rejected response", "retry_after_ms":null, "details":null
                }})
            }
        };
        input
            .write_all(&trace_contracts::plugin_chunks::encode(&reply).unwrap())
            .unwrap();
        input.flush().unwrap();
        let answer = rx.recv_timeout(Duration::from_secs(5)).unwrap();
        server.abort();
        assert_eq!(answer["request_id"], "large-page");
        assert_eq!(
            answer["ok"], true,
            "a normal-size source page needs a bounded paging/body strategy; adapter result: {answer}"
        );
        assert_eq!(answer["result"]["items"].as_array().unwrap().len(), 20);
    }
}
