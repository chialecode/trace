// SPDX-License-Identifier: MIT
// Copyright (c) 2025 c.zeong
// Adapted from DTV 9cd17311fa32575fa68f7e79feafdb165c13edd3. See ../NOTICE.
// Changes: RustCrypto SM3, explicit clock input. No network or credential access.
use std::collections::HashMap;

fn rc4_encrypt(plaintext: &str, key: &str) -> String {
    // Mirror Python behavior: iterate over scalars and truncate each to a byte.
    let pbytes: Vec<u8> = plaintext.chars().map(|c| c as u32 as u8).collect();
    let kbytes: Vec<u8> = key.chars().map(|c| c as u32 as u8).collect();

    let mut s: Vec<usize> = (0..256).collect();
    let mut j = 0usize;
    for i in 0..256 {
        j = (j + s[i] + kbytes[i % kbytes.len()] as usize) % 256;
        s.swap(i, j);
    }

    let mut i = 0usize;
    j = 0usize;
    let mut result = String::with_capacity(pbytes.len());
    for ch in pbytes {
        i = (i + 1) % 256;
        j = (j + s[i]) % 256;
        s.swap(i, j);
        let t = (s[i] + s[j]) % 256;
        let out = (ch ^ s[t] as u8) as char;
        result.push(out);
    }
    result
}

fn sm3_sum(data: &[u8]) -> Vec<u8> {
    use sm3::Digest;
    sm3::Sm3::digest(data).to_vec()
}

fn get_long_int(round_num: usize, long_str: &str) -> u32 {
    // Operate on Unicode scalar values to mirror Python `chr` behavior.
    let chars: Vec<u32> = long_str.chars().map(|c| c as u32).collect();
    let i = round_num * 3;
    let b1 = *chars.get(i).unwrap_or(&0);
    let b2 = *chars.get(i + 1).unwrap_or(&0);
    let b3 = *chars.get(i + 2).unwrap_or(&0);
    (b1 << 16) | (b2 << 8) | b3
}

fn result_encrypt(long_str: &str, num: &str) -> String {
    let encoding_tables: HashMap<&str, &str> = HashMap::from([
        (
            "s0",
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=",
        ),
        (
            "s1",
            "Dkdpgh4ZKsQB80/Mfvw36XI1R25+WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe=",
        ),
        (
            "s2",
            "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe=",
        ),
        (
            "s3",
            "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe",
        ),
        (
            "s4",
            "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe",
        ),
    ]);
    let masks = [16515072u32, 258048, 4032, 63];
    let shifts = [18u32, 12, 6, 0];
    let table = encoding_tables[num].as_bytes();

    let mut result = String::new();
    let char_len = long_str.chars().count();
    let mut round_num = 0usize;
    let mut long_int = get_long_int(round_num, long_str);
    let total_chars = ((char_len as f64 / 3.0) * 4.0).ceil() as usize;
    for i in 0..total_chars {
        if i / 4 != round_num {
            round_num += 1;
            long_int = get_long_int(round_num, long_str);
        }
        let idx = i % 4;
        let char_index = ((long_int & masks[idx]) >> shifts[idx]) as usize;
        result.push(table[char_index] as char);
    }
    result
}

fn gener_random(random_num: i32, option: [i32; 2]) -> Vec<u8> {
    let byte1 = random_num & 255;
    let byte2 = (random_num >> 8) & 255;
    vec![
        ((byte1 & 170) | (option[0] & 85)) as u8,
        ((byte1 & 85) | (option[0] & 170)) as u8,
        ((byte2 & 170) | (option[1] & 85)) as u8,
        ((byte2 & 85) | (option[1] & 170)) as u8,
    ]
}

fn generate_random_str() -> String {
    let vals = [0.123456789f64, 0.987654321f64, 0.555555555f64];
    let mut bytes = Vec::new();
    bytes.extend(gener_random((vals[0] * 10000.0) as i32, [3, 45]));
    bytes.extend(gener_random((vals[1] * 10000.0) as i32, [1, 0]));
    bytes.extend(gener_random((vals[2] * 10000.0) as i32, [1, 5]));
    bytes.into_iter().map(|b| b as char).collect()
}

fn split_to_bytes(num: i64) -> [i64; 4] {
    [
        (num >> 24) & 255,
        (num >> 16) & 255,
        (num >> 8) & 255,
        num & 255,
    ]
}

/// Start time (b[16]) and the three request arguments → b[20..=37].
fn fill_start_and_arguments(b: &mut [i64], arguments: [i32; 3]) {
    let start = split_to_bytes(b[16]);
    b[20..=23].copy_from_slice(&start);
    b[24] = (b[16] / 256 / 256 / 256 / 256) & 255;
    b[25] = (b[16] / 256 / 256 / 256 / 256 / 256) & 255;
    b[26..=29].copy_from_slice(&split_to_bytes(arguments[0] as i64));
    b[30] = (arguments[1] / 256) as i64 & 255;
    b[31] = (arguments[1] % 256) as i64 & 255;
    b[32..=33].copy_from_slice(&split_to_bytes(arguments[1] as i64)[..2]);
    b[34..=37].copy_from_slice(&split_to_bytes(arguments[2] as i64));
}

/// End time (b[10]) → b[44..=50].
fn fill_end_time(b: &mut [i64]) {
    let end = split_to_bytes(b[10]);
    b[44..=47].copy_from_slice(&end);
    b[48] = b[8];
    b[49] = (b[10] / 256 / 256 / 256 / 256) & 255;
    b[50] = (b[10] / 256 / 256 / 256 / 256 / 256) & 255;
}

/// Page id and application id → b[51..=60].
fn fill_page_and_app(b: &mut [i64]) {
    let page_id = 110624i64;
    b[51] = page_id;
    b[52..=55].copy_from_slice(&split_to_bytes(page_id));
    let aid = 6383i64;
    b[56] = aid;
    b[57] = aid & 255;
    b[58] = (aid >> 8) & 255;
    b[59] = (aid >> 16) & 255;
    b[60] = (aid >> 24) & 255;
}

/// Positions XOR-ed into the checksum, in the upstream order.
const CHECKSUM_INDEXES: [usize; 43] = [
    18, 20, 26, 30, 38, 40, 42, 21, 27, 31, 35, 39, 41, 43, 22, 28, 32, 36, 23, 29, 33, 37, 44, 45,
    46, 47, 48, 49, 50, 24, 25, 52, 53, 54, 55, 57, 58, 59, 60, 65, 66, 70, 71,
];

/// Output order of the byte table before the browser environment string.
const OUTPUT_ORDER: [usize; 44] = [
    18, 20, 52, 26, 30, 34, 58, 38, 40, 53, 42, 21, 27, 54, 55, 31, 35, 57, 39, 41, 43, 22, 28, 32,
    60, 36, 23, 29, 33, 37, 44, 45, 59, 46, 47, 48, 49, 50, 24, 25, 65, 66, 70, 71,
];

fn generate_rc4_bb_str(
    url_search_params: &str,
    user_agent: &str,
    window_env_str: &str,
    suffix: &str,
    arguments: [i32; 3],
    start_time: i64,
) -> String {
    // Double SM3 hashing to align with upstream Python behavior.
    let url_list = sm3_sum(&sm3_sum(
        format!("{}{}", url_search_params, suffix).as_bytes(),
    ));
    let cus_once = sm3_sum(suffix.as_bytes());
    let cus = sm3_sum(&cus_once);
    let ua_key = [0u8, 1u8, 14u8];
    let ua = sm3_sum(
        result_encrypt(
            &rc4_encrypt(user_agent, &String::from_utf8_lossy(&ua_key)),
            "s3",
        )
        .as_bytes(),
    );

    let mut b = vec![0i64; 80];
    b[8] = 3;
    b[10] = start_time + 100;
    b[16] = start_time;
    b[18] = 44;
    fill_start_and_arguments(&mut b, arguments);
    b[38] = *url_list.get(21).unwrap_or(&0) as i64;
    b[39] = *url_list.get(22).unwrap_or(&0) as i64;
    b[40] = *cus.get(21).unwrap_or(&0) as i64;
    b[41] = *cus.get(22).unwrap_or(&0) as i64;
    b[42] = *ua.get(23).unwrap_or(&0) as i64;
    b[43] = *ua.get(24).unwrap_or(&0) as i64;
    fill_end_time(&mut b);
    fill_page_and_app(&mut b);

    let window_env_list: Vec<i64> = window_env_str.chars().map(|c| c as i64).collect();
    let env_len = window_env_list.len() as i64;
    b[64] = env_len;
    b[65] = env_len & 255;
    b[66] = (env_len >> 8) & 255;

    let checksum = CHECKSUM_INDEXES.iter().fold(0, |acc, &i| acc ^ b[i]);
    b[72] = checksum;

    let mut bb: Vec<i64> = OUTPUT_ORDER.iter().map(|&i| b[i]).collect();
    bb.extend(window_env_list);
    bb.push(checksum);

    let plaintext: String = bb.into_iter().map(|b| (b as u8) as char).collect();
    rc4_encrypt(&plaintext, &(121u8 as char).to_string())
}

pub fn sign_at(query: &str, user_agent: &str, timestamp_ms: i64) -> String {
    let window_env_str = "1920|1080|1920|1040|0|30|0|0|1872|92|1920|1040|1857|92|1|24|Win32";
    format!(
        "{}=",
        result_encrypt(
            &format!(
                "{}{}",
                generate_random_str(),
                generate_rc4_bb_str(
                    query,
                    user_agent,
                    window_env_str,
                    "cus",
                    [0, 1, 14],
                    timestamp_ms
                )
            ),
            "s4"
        )
    )
}

#[cfg(test)]
mod golden {
    #[test]
    fn a_bogus_fixed_upstream_vector_and_identity_binding() {
        // Independent upstream implementation with only SystemTime replaced by
        // 1700000000000; upstream revision and license are fixed in NOTICE.
        let q = "aid=6383&aweme_id=90071992547409931";
        let expected = "E7mhBmg6mEVNgf6X56KLfY3q6633YIoI0HViMD2faVfLqL39HMYD9exoIBGvXKWjwG/-IeYjy4hbO3xprQAjM36UHWwEUdQ2mgWkKl5Q5I0j53iruyRDntmF4vj3SFlm5XNAEOk0y75rKb70Woqe-vIlO62-zo0/9f8=";
        assert_eq!(super::sign_at(q, "Mozilla/5.0", 1700000000000), expected);
        assert_ne!(super::sign_at(q, "different UA", 1700000000000), expected);
        assert_ne!(super::sign_at(q, "Mozilla/5.0", 1700000001000), expected);
        assert_ne!(
            super::sign_at("aid=6383&aweme_id=1", "Mozilla/5.0", 1700000000000),
            expected
        );
    }
}
