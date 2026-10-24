//! Credential-derived access context stays inside Core. This is independent of
//! login verification and is restricted to the observed SecSDK endpoint family.
use crate::error::CoreError;
use md5::{Digest, Md5};
use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use url::Url;

const PRIVATE_QUERY: &[&str] = &[
    "uifid",
    "verifyfp",
    "fp",
    "timestamp",
    "x-secsdk-web-signature",
];
const PRIVATE_HEADERS: &[&str] = &[
    "uifid",
    "x-secsdk-web-signature",
    "x-secsdk-web-expire",
    "x-csrf-token",
    "x-csrftoken",
    "x-xsrf-token",
    "x-twitter-auth-type",
];

pub(crate) fn decorate(
    url: &mut Url,
    method: &str,
    cookie: Option<&str>,
    headers: &mut HeaderMap,
    now: i64,
) -> Result<(), CoreError> {
    if method != "GET"
        || url.host_str() != Some("www.douyin.com")
        || !matches!(
            url.path(),
            "/aweme/v1/web/aweme/detail/" | "/aweme/v1/web/aweme/post/"
        )
    {
        return Ok(());
    }
    let Some(cookie) = cookie else {
        return Ok(());
    };
    let mut pairs: Vec<(String, String)> = url.query_pairs().into_owned().collect();
    let mut names = std::collections::BTreeSet::new();
    if pairs.iter().any(|(k, _)| {
        PRIVATE_QUERY.contains(&k.to_ascii_lowercase().as_str()) || !names.insert(k.clone())
    }) {
        return Err(CoreError::InvalidInput("ambiguous access query".into()));
    }
    let jar: std::collections::BTreeMap<_, _> = cookie
        .split(';')
        .filter_map(|p| p.trim().split_once('='))
        .collect();
    let visitor = [
        "uifid",
        "uifid_temp",
        "uifidtemp",
        "UIFID",
        "UIFID_TEMP",
        "UIFIDTEMP",
    ]
    .into_iter()
    .find_map(|k| jar.get(k).filter(|v| !v.is_empty()).copied())
    .ok_or_else(|| CoreError::SourceUnavailable("site access context unavailable".into()))?;
    if let Some(fp) = jar.get("s_v_web_id").filter(|v| !v.is_empty()) {
        pairs.push(("verifyFp".into(), fp.to_string()));
        pairs.push(("fp".into(), fp.to_string()));
    }
    let stamp = now.to_string();
    pairs.push(("uifid".into(), visitor.into()));
    pairs.push(("timestamp".into(), stamp.clone()));
    // SecSDK signs the actual serialized query, including the existing A-Bogus.
    // Match percent-encoded UTF-8 (space=%20), preserving parameter order.
    let query = url::form_urlencoded::Serializer::new(String::new())
        .extend_pairs(&pairs)
        .finish()
        .replace('+', "%20")
        .replace("%7E", "~");
    let input = format!("{visitor}_{stamp}_A96D855A08C0A9707F8BEF0D9A527E4E_{query}");
    let signature = Md5::digest(input.as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>();
    url.set_query(Some(&format!("{query}&x-secsdk-web-signature={signature}")));
    for (key, value) in [
        ("uifid", visitor),
        ("x-secsdk-web-signature", signature.as_str()),
        ("x-secsdk-web-expire", stamp.as_str()),
    ] {
        headers.insert(
            key,
            HeaderValue::from_str(value)
                .map_err(|_| CoreError::InvalidInput("invalid access context".into()))?,
        );
    }
    Ok(())
}

/// Anti-forgery headers a site's web client sends with its API requests. They repeat a value
/// the session Cookie already carries, so Core adds them where the Cookie is attached and
/// the adapter never has to see the Cookie.
pub(crate) fn session_headers(site: Option<&str>, cookie: &str) -> Vec<(HeaderName, HeaderValue)> {
    // (Cookie name, header) pairs copied by value; (Cookie name, header, value) pairs set
    // to a fixed value when the Cookie is present.
    type Copied = &'static [(&'static str, &'static str)];
    type Fixed = &'static [(&'static str, &'static str, &'static str)];
    let (copied, fixed): (Copied, Fixed) = match site {
        // `auth_token` marks a logged-in web session; the value itself is never copied.
        Some("x") => (
            &[("ct0", "x-csrf-token")],
            &[("auth_token", "x-twitter-auth-type", "OAuth2Session")],
        ),
        Some("instagram") => (&[("csrftoken", "x-csrftoken")], &[]),
        Some("weibo") => (&[("XSRF-TOKEN", "x-xsrf-token")], &[]),
        _ => return Vec::new(),
    };
    let jar: std::collections::BTreeMap<&str, &str> = cookie
        .split(';')
        .filter_map(|p| p.trim().split_once('='))
        .filter(|(_, v)| !v.is_empty())
        .collect();
    let mut out = Vec::new();
    for (cookie_name, header) in copied {
        if let Some(value) = jar
            .get(cookie_name)
            .and_then(|v| HeaderValue::from_str(v).ok())
        {
            out.push((HeaderName::from_static(header), value));
        }
    }
    for (cookie_name, header, value) in fixed {
        if jar.contains_key(cookie_name) {
            out.push((
                HeaderName::from_static(header),
                HeaderValue::from_static(value),
            ));
        }
    }
    out
}

pub(crate) fn strip_headers(headers: &mut HeaderMap) {
    for key in PRIVATE_HEADERS {
        headers.remove(*key);
    }
}
pub(crate) fn public_url(url: &Url) -> String {
    let mut clean = url.clone();
    let pairs: Vec<_> = url
        .query_pairs()
        .into_owned()
        .filter(|(key, _)| !PRIVATE_QUERY.contains(&key.to_ascii_lowercase().as_str()))
        .collect();
    clean.set_query(None);
    if !pairs.is_empty() {
        clean.query_pairs_mut().extend_pairs(pairs);
    }
    clean.into()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn secsdk_context_is_scoped_unique_and_never_returned() {
        let original = "https://www.douyin.com/aweme/v1/web/aweme/detail/?aid=6383&text=a+b%2B~%E4%B8%AD&a_bogus=public%2F%3D";
        let mut url = Url::parse(original).unwrap();
        let mut headers = HeaderMap::new();
        decorate(
            &mut url,
            "GET",
            Some("uifid=synthetic-visitor; s_v_web_id=synthetic-fp"),
            &mut headers,
            1702204169,
        )
        .unwrap();
        assert_eq!(
            url.query_pairs().filter(|(k, _)| k == "timestamp").count(),
            1
        );
        assert!(url.query().unwrap().contains("text=a%20b%2B~%E4%B8%AD"));
        assert_eq!(
            headers["x-secsdk-web-signature"],
            "5b0e0e23b1c6ab3081fc116b4fa2a672"
        );
        assert!(
            url.as_str()
                .ends_with("&x-secsdk-web-signature=5b0e0e23b1c6ab3081fc116b4fa2a672")
        );
        assert!(!public_url(&url).contains("synthetic"));
        assert!(!public_url(&url).contains("secsdk"));
        assert!(
            decorate(
                &mut url,
                "GET",
                Some("uifid=other"),
                &mut headers,
                1702204169
            )
            .is_err()
        );
        strip_headers(&mut headers);
        assert!(headers.is_empty());
        for endpoint in [
            "https://www.douyin.com/aweme/v1/web/user/profile/self/",
            "https://live.douyin.com/aweme/v1/web/aweme/detail/",
            "https://www.douyin.com.evil.invalid/aweme/v1/web/aweme/detail/",
        ] {
            let mut u = Url::parse(endpoint).unwrap();
            decorate(&mut u, "GET", Some("uifid=synthetic"), &mut headers, 1).unwrap();
            assert_eq!(u.as_str(), endpoint);
            assert!(headers.is_empty());
        }
        let mut u = Url::parse(original).unwrap();
        assert!(decorate(&mut u, "GET", Some("sessionid=synthetic"), &mut headers, 1).is_err());
    }
    #[tokio::test]
    async fn secsdk_wire_bytes_and_cross_origin_redirect_never_export_context() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let first = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let second = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = second.local_addr().unwrap().port();
        let http = crate::transport::ConstrainedClient::build().unwrap();
        http.route_fixture(
            "www.douyin.com",
            &format!("http://{}", first.local_addr().unwrap()),
        );
        let first = tokio::spawn(async move {
            let (mut stream, _) = first.accept().await.unwrap();
            let mut request = Vec::new();
            while !request.ends_with(b"\r\n\r\n") {
                request.push(stream.read_u8().await.unwrap());
            }
            let text = String::from_utf8(request).unwrap();
            let target = text
                .lines()
                .next()
                .unwrap()
                .split_whitespace()
                .nth(1)
                .unwrap();
            let u = Url::parse(&format!("https://www.douyin.com{target}")).unwrap();
            let pairs: std::collections::BTreeMap<_, _> = u.query_pairs().into_owned().collect();
            let (query, sig) = u
                .query()
                .unwrap()
                .rsplit_once("&x-secsdk-web-signature=")
                .unwrap();
            let digest = Md5::digest(
                format!(
                    "synthetic-visitor_{}_A96D855A08C0A9707F8BEF0D9A527E4E_{query}",
                    pairs["timestamp"]
                )
                .as_bytes(),
            )
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>();
            assert_eq!(sig, digest);
            assert!(text.contains(&format!("x-secsdk-web-signature: {sig}")));
            assert!(query.contains("text=a%20b%2B~%E4%B8%AD"));
            stream.write_all(format!("HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1:{port}/public?uifid=synthetic-visitor&x-secsdk-web-signature={sig}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").as_bytes()).await.unwrap();
        });
        let second = tokio::spawn(async move {
            let (mut stream, _) = second.accept().await.unwrap();
            let mut request = Vec::new();
            while !request.ends_with(b"\r\n\r\n") {
                request.push(stream.read_u8().await.unwrap());
            }
            let text = String::from_utf8(request).unwrap().to_ascii_lowercase();
            for private in ["uifid", "secsdk", "synthetic-visitor", "cookie:"] {
                assert!(!text.contains(private));
            }
            stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
                .await
                .unwrap();
        });
        let rules = crate::transport::host_rules(&crate::config::Profile {
            test_allowed_hosts: vec!["127.0.0.1".into()],
            ..Default::default()
        });
        let result=http.fetch_metadata(&rules,"GET","https://www.douyin.com/aweme/v1/web/aweme/detail/?text=a+b%2B~%E4%B8%AD&a_bogus=public",None,Some("uifid=synthetic-visitor;s_v_web_id=synthetic-fp"),524288).await.unwrap();
        assert!(!result.final_url.contains("synthetic"));
        assert!(!result.final_url.contains("secsdk"));
        first.await.unwrap();
        second.await.unwrap();
    }
}
