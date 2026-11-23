package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckdocsPositiveAndNegativeCases(t *testing.T) {
	// 1. Clean synthetic repo + synthetic reference index
	repoDir := t.TempDir()
	refDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(refDir, "README.md"), []byte("# Ref\n\n### ExampleOrg/SecretReferenceRepo\n"), 0o600); err != nil {
		t.Fatalf("write ref README: %v", err)
	}

	targetMD := "# Target Doc\n\n<a id=\"custom-anchor\"></a>\n## Section One\nText mentioning `sessionid=`, `Set-Cookie: msToken=...`, `&a_bogus=<百分号编码>`, and `x-signature=<签名>` as placeholders.\n"
	if err := os.WriteFile(filepath.Join(repoDir, "target.md"), []byte(targetMD), 0o600); err != nil {
		t.Fatalf("write target.md: %v", err)
	}
	validMD := "# Main\n\nSee [sec](target.md#section-one) and [custom](target.md#custom-anchor).\n"
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(validMD), 0o600); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	findings, msg, err := Run(repoDir, refDir)
	if err != nil {
		t.Fatalf("Run valid repo: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings on valid repo, got %v", findings)
	}
	if !strings.Contains(msg, "loaded 2 reference terms") {
		t.Fatalf("unexpected msg: %q", msg)
	}

	// 2. Missing reference index should skip gracefully (CI behavior)
	emptyRefDir := filepath.Join(t.TempDir(), "nonexistent")
	t.Setenv("TRACE_REFERENCE_DIR", emptyRefDir)
	findings, msg, err = Run(filepath.Join(t.TempDir()), emptyRefDir)
	if err != nil || len(findings) != 0 {
		t.Fatalf("Run without ref index failed: findings=%v err=%v", findings, err)
	}
	if !strings.Contains(msg, "skipping reference-name check") {
		t.Fatalf("expected skip message when ref index missing, got %q", msg)
	}

	// 3. Negative cases: broken link, missing anchor, local path, cookie leak, reference project name
	badMD := strings.Join([]string{
		"# Bad Doc",
		"- Broken file: [missing](does-not-exist.md)",
		"- Broken anchor: [bad anchor](target.md#no-such-anchor)",
		"- Local path: C:\\Users\\alice\\secret.txt",
		"- Cookie leak: SESSDATA=abcdef1234567890",
		"- Reference leak: borrowed from SecretReferenceRepo",
	}, "\n")
	if err := os.WriteFile(filepath.Join(repoDir, "BAD.md"), []byte(badMD), 0o600); err != nil {
		t.Fatalf("write BAD.md: %v", err)
	}

	findings, _, err = Run(repoDir, refDir)
	if err != nil {
		t.Fatalf("Run bad repo: %v", err)
	}
	wantSubstrings := []string{
		"broken link target \"does-not-exist.md\"",
		"missing anchor #no-such-anchor",
		"contains local Windows absolute path",
		"contains Cookie/token value",
		"contains reference project name \"SecretReferenceRepo\"",
	}
	joined := strings.Join(findings, "\n")
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected finding containing %q, got:\n%s", want, joined)
		}
	}

	// 4. Douyin secret/sentinel & trace-remote token leak detection + redaction
	secretLeaks := []struct {
		name      string
		line      string
		want      string
		rawSecret string
	}{
		{"sessionid", "Cookie: sessionid=0123456789abcdef0123456789abcdef", "contains Cookie/token value", "0123456789abcdef0123456789abcdef"},
		{"sessionid_ss", "Cookie: sessionid_ss=fedcba9876543210fedcba9876543210", "contains Cookie/token value", "fedcba9876543210fedcba9876543210"},
		{"sid_guard", "Cookie: sid_guard=tok123%7C1760000000%7C5184000", "contains Cookie/token value", "tok123%7C1760000000%7C5184000"},
		{"msToken", "Cookie: msToken=AbCdEfGhIjKlMnOp123456", "contains Cookie/token value", "AbCdEfGhIjKlMnOp123456"},
		{"UIFID", "Cookie: UIFID=synth_uifid_secret_001", "contains Cookie/token value", "synth_uifid_secret_001"},
		{"ttwid", "Cookie: ttwid=1%7Csynth_ttwid_secret_002", "contains Cookie/token value", "synth_ttwid_secret_002"},
		{"odin_tt", "Cookie: odin_tt=synth_odin_secret_003", "contains Cookie/token value", "synth_odin_secret_003"},
		{"sid_tt", "Cookie: sid_tt=synth_sid_tt_secret_004", "contains Cookie/token value", "synth_sid_tt_secret_004"},
		{"uid_tt", "Cookie: uid_tt=synth_uid_tt_secret_005", "contains Cookie/token value", "synth_uid_tt_secret_005"},
		{"ac_signature", "Cookie: __ac_signature=_02B4Z6wo00f01synthsig", "contains Cookie/token value", "_02B4Z6wo00f01synthsig"},
		{"trace_remote", "Code: trace-remote:1:eyJ0b2tlbiI6InN5bnRoX3NlY3JldCJ9", "contains Cookie/token value", "eyJ0b2tlbiI6InN5bnRoX3NlY3JldCJ9"},
		{"a_bogus_url", "URL: https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=7300000000000000001&a_bogus=dJUnkqSiQoWb", "contains signed URL", "dJUnkqSiQoWb"},
		{"x_signature_url", "CDN: https://v3-web.douyinvod.com/video/tos/cn/1/?x-expires=1763003600&x-signature=9f8e7d6c5b4a", "contains signed URL", "9f8e7d6c5b4a"},
		{"x_signature_path_url", "CDN: https://v3-web.douyinvod.com/video/x-signature/7a6b5c4d3e2f/clip.mp4", "contains signed URL", "7a6b5c4d3e2f"},
	}
	for _, tc := range secretLeaks {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(caseDir, "LEAK.md"), []byte("# Leak\n\n"+tc.line+"\n"), 0o600); err != nil {
				t.Fatalf("write LEAK.md (%s): %v", tc.name, err)
			}
			gotFindings, _, err := Run(caseDir, emptyRefDir)
			if err != nil {
				t.Fatalf("Run(%s): %v", tc.name, err)
			}
			joinedGot := strings.Join(gotFindings, "\n")
			if !strings.Contains(joinedGot, tc.want) {
				t.Fatalf("%s: expected finding %q, got: %v", tc.name, tc.want, gotFindings)
			}
			if strings.Contains(joinedGot, tc.rawSecret) {
				t.Fatalf("%s: raw secret %q leaked into finding output: %s", tc.name, tc.rawSecret, joinedGot)
			}
			if !strings.Contains(joinedGot, "<redacted>") {
				t.Fatalf("%s: expected <redacted> in finding output: %s", tc.name, joinedGot)
			}
		})
	}
}

func TestCheckdocsBroadenedPathsAndPlaceholders(t *testing.T) {
	emptyRefDir := filepath.Join(t.TempDir(), "nonexistent")
	t.Setenv("TRACE_REFERENCE_DIR", emptyRefDir)

	t.Run("non-CD Windows drives and /home/<user>/ detected", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			line string
			want string
		}{
			{"E drive", `Path: E:\Projects\trace`, "contains local Windows absolute path"},
			{"z drive lowercase", `Path: z:\work\file.txt`, "contains local Windows absolute path"},
			{"unix home user", `Path: /home/alice/notes.md`, "contains local Unix home path"},
			{"unix home runner", `Path: /home/runner/work/trace`, "contains local Unix home path"},
			{"macOS Users", `Path: /Users/devuser/trace`, "contains local Unix home path"},
		} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "DOC.md"), []byte("# Doc\n\n"+tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			findings, _, err := Run(dir, emptyRefDir)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !strings.Contains(strings.Join(findings, "\n"), tc.want) {
				t.Fatalf("%s: expected %q, got %v", tc.name, tc.want, findings)
			}
		}
	})

	t.Run("UI route /home and placeholder trace-remote:1: are allowed", func(t *testing.T) {
		dir := t.TempDir()
		doc := "# Safe Doc\n\nRoute `/home` and prefix `trace-remote:1:` are placeholders.\n"
		if err := os.WriteFile(filepath.Join(dir, "SAFE.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		findings, _, err := Run(dir, emptyRefDir)
		if err != nil || len(findings) != 0 {
			t.Fatalf("unexpected findings for safe placeholders: %v err=%v", findings, err)
		}
	})
}

func TestCheckdocsNestedFencesAndCodeBlockHeadings(t *testing.T) {
	emptyRefDir := filepath.Join(t.TempDir(), "nonexistent")
	t.Setenv("TRACE_REFERENCE_DIR", emptyRefDir)

	dir := t.TempDir()
	// Target has a real heading and a # comment inside both 3-backtick and 4-backtick fences.
	target := strings.Join([]string{
		"# Real Title",
		"",
		"```bash",
		"# comment-in-code-block",
		"```",
		"",
		"````markdown",
		"```bash",
		"# comment-in-nested-fence",
		"```",
		"[fake-link-in-nested-fence](does-not-exist.md)",
		"````",
		"",
		"## Real Section",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "target.md"), []byte(target), 0o600); err != nil {
		t.Fatal(err)
	}
	index := strings.Join([]string{
		"# Index",
		"- Valid: [real](target.md#real-section)",
		"- Bad code comment anchor: [bad1](target.md#comment-in-code-block)",
		"- Bad nested comment anchor: [bad2](target.md#comment-in-nested-fence)",
		"",
		"````markdown",
		"```",
		"[ignored](missing-inside-4-fence.md)",
		"```",
		"````",
		"- Real broken link after 4-fence: [real-broken](missing-after-4-fence.md)",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	findings, _, err := Run(dir, emptyRefDir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{
		"missing anchor #comment-in-code-block",
		"missing anchor #comment-in-nested-fence",
		`broken link target "missing-after-4-fence.md"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected finding %q, got:\n%s", want, joined)
		}
	}
	for _, forbid := range []string{
		"does-not-exist.md",
		"missing-inside-4-fence.md",
		"#real-section",
	} {
		if strings.Contains(joined, forbid) {
			t.Fatalf("unexpected finding mentioning %q:\n%s", forbid, joined)
		}
	}
}

func TestCheckdocsWeiboSecretsAndPlaceholders(t *testing.T) {
	emptyRefDir := filepath.Join(t.TempDir(), "nonexistent")
	t.Setenv("TRACE_REFERENCE_DIR", emptyRefDir)

	leaks := []struct {
		name      string
		line      string
		want      string
		rawSecret string
	}{
		{"weibo_sub", "Cookie: SUB=_2A25SynthSubToken0001abcdef", "contains Cookie/token value", "_2A25SynthSubToken0001abcdef"},
		{"weibo_subp", "Cookie: SUBP=0033WrSXqPxfM725Ws9jqgMF55529P9D", "contains Cookie/token value", "0033WrSXqPxfM725Ws9jqgMF55529P9D"},
		{"weibo_xsrf_token", "Cookie: XSRF-TOKEN=AbCdEfGh123456_7890-XyZ", "contains Cookie/token value", "AbCdEfGh123456_7890-XyZ"},
		{"weibo_wbpsess", "Cookie: WBPSESS=Dt2hbAUaXSynthWbpsess0123==", "contains Cookie/token value", "Dt2hbAUaXSynthWbpsess0123=="},
		{"weibo_scf", "Cookie: SCF=AlSynthScfToken0123456789._-", "contains Cookie/token value", "AlSynthScfToken0123456789._-"},
		{"weibo_alf", "Cookie: ALF=1795000000", "contains Cookie/token value", "1795000000"},
		{"weibo_sso_login_state", "Cookie: SSOLoginState=1763700000", "contains Cookie/token value", "1763700000"},
		{"weibo_suhb", "Cookie: SUHB=02M_SynthSuhb001", "contains Cookie/token value", "02M_SynthSuhb001"},
		{"weibo_pc_token", "Cookie: PC_TOKEN=0123456789abcdef0123456789abcdef", "contains Cookie/token value", "0123456789abcdef0123456789abcdef"},
		{"weibo_t_wm", "Cookie: _T_WM=fedcba9876543210fedcba9876543210", "contains Cookie/token value", "fedcba9876543210fedcba9876543210"},
		{"weibo_video_signed_url", "CDN: https://f.video.weibocdn.com/o0/synth01.mp4?Expires=1763700000&ssig=AbCdEf%2B12%3D&KID=unistore,video", "contains signed URL", "AbCdEf%2B12%3D"},
		{"weibo_livephoto_signed_url", "CDN: https://livephoto.us.sinaimg.cn/001synth.mov?ssig=9f8e7d6c5b&Expires=1763700000&KID=unistore,video", "contains signed URL", "9f8e7d6c5b"},
		{"weibo_expires_kid_url", "CDN: https://f.video.weibocdn.com/o0/synth02.mp4?Expires=1763700000&KID=unistore,video", "contains signed URL", "1763700000"},
		{"weibo_kid_expires_url", "CDN: https://f.video.weibocdn.com/o0/synth03.mp4?KID=unistore,video&Expires=1763700000", "contains signed URL", "1763700000"},
		{"weibo_bare_signed_query", "Query: Expires=1763700000&ssig=SynthSig9876&KID=unistore,video", "contains signed URL", "SynthSig9876"},
		{"weibo_qmarkless_amp_url", "CDN: https://f.video.weibocdn.com/o0/synth01.mp4&ssig=SecretSig1234", "contains signed URL", "SecretSig1234"},
		{"weibo_qmarkless_json_amp_url", `CDN: https://f.video.weibocdn.com/o0/synth01.mp4\u0026ssig=SecretJsonSig5678`, "contains signed URL", "SecretJsonSig5678"},
		{"weibo_livephoto_encoded_url", "CDN: https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D1763700000%26ssig%3DEncLiveSig1234%26KID%3Dunistore%2Cvideo", "contains signed URL", "EncLiveSig1234"},
		{"weibo_livephoto_encoded_exp_kid", "CDN: https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D1763700001%26KID%3Dunistore%2Cvideo", "contains signed URL", "1763700001"},
		{"weibo_livephoto_encoded_bare", "Encoded: https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D1763700002%26ssig%3DBareEncSig9999%26KID%3Dunistore%2Cvideo", "contains signed URL", "BareEncSig9999"},
	}
	for _, tc := range leaks {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(caseDir, "LEAK.md"), []byte("# Leak\n\n"+tc.line+"\n"), 0o600); err != nil {
				t.Fatalf("write LEAK.md (%s): %v", tc.name, err)
			}
			gotFindings, _, err := Run(caseDir, emptyRefDir)
			if err != nil {
				t.Fatalf("Run(%s): %v", tc.name, err)
			}
			joinedGot := strings.Join(gotFindings, "\n")
			if !strings.Contains(joinedGot, tc.want) {
				t.Fatalf("%s: expected finding %q, got: %v", tc.name, tc.want, gotFindings)
			}
			if strings.Contains(joinedGot, tc.rawSecret) {
				t.Fatalf("%s: raw secret %q leaked into finding output: %s", tc.name, tc.rawSecret, joinedGot)
			}
			if !strings.Contains(joinedGot, "<redacted>") {
				t.Fatalf("%s: expected <redacted> in finding output: %s", tc.name, joinedGot)
			}
		})
	}

	t.Run("placeholders and public URLs allowed", func(t *testing.T) {
		dir := t.TempDir()
		safeDoc := strings.Join([]string{
			"# Weibo Safe Placeholders",
			"",
			"- Cookie placeholders: `SUB=<合成占位>`, `SUB=<synthetic>`, `XSRF-TOKEN=<合成占位>`, `WBPSESS=<synthetic>`, `SUBP=<placeholder>`, `SCF=<placeholder>`, `ALF=<Unix秒>`, `SSOLoginState=<Unix秒>`.",
			"- Bare key mentions: `SUB=` / `SUBP=` / `XSRF-TOKEN=` / `WBPSESS=` / `SUB=...` / `SUB=[REDACTED]`.",
			"- Lowercase query parameter: `/library?type=&sub=subscription_0001&mid=`.",
			"- Signed URL placeholders: `Expires=<Unix秒>&ssig=<合成签名>&KID=unistore,video`.",
			"- Full URL placeholders: `https://f.video.weibocdn.com/o0/demo.mp4?Expires=<Unix秒>&ssig=<合成签名>&KID=unistore,video`.",
			"- Reordered URL placeholder: `https://f.video.weibocdn.com/o0/demo.mp4?KID=unistore,video&Expires=<Unix秒>&ssig=<合成签名>`.",
			"- Encoded LivePhoto placeholder: `https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D<Unix秒>%26ssig%3D<合成签名>%26KID%3Dunistore%2Cvideo`.",
			"- Public image URL: `https://wx1.sinaimg.cn/large/001SynThPic01ly1synthetic00001.jpg`.",
		}, "\n")
		if err := os.WriteFile(filepath.Join(dir, "SAFE.md"), []byte(safeDoc), 0o600); err != nil {
			t.Fatal(err)
		}
		findings, _, err := Run(dir, emptyRefDir)
		if err != nil || len(findings) != 0 {
			t.Fatalf("unexpected findings for Weibo placeholders: %v (err=%v)", findings, err)
		}
	})

	t.Run("actual docs/research/weibo.md has no secret false positives", func(t *testing.T) {
		weiboDocPath := filepath.Join("..", "..", "docs", "research", "weibo.md")
		raw, err := os.ReadFile(weiboDocPath)
		if err != nil {
			t.Fatalf("read %s: %v", weiboDocPath, err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "weibo.md"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		findings, _, err := Run(dir, emptyRefDir)
		if err != nil {
			t.Fatalf("Run on weibo.md copy: %v", err)
		}
		for _, f := range findings {
			if strings.Contains(f, "Cookie/token") || strings.Contains(f, "signed URL") {
				t.Fatalf("false positive on docs/research/weibo.md: %s", f)
			}
		}
	})
}

func TestCheckdocsXSecretsAndPlaceholders(t *testing.T) {
	emptyRefDir := filepath.Join(t.TempDir(), "nonexistent")
	t.Setenv("TRACE_REFERENCE_DIR", emptyRefDir)

	synthAuth := "0123456789abcdef0123456789abcdef01234567"
	synthCT0 := "abcdef0123456789abcdef0123456789"
	synthKDT := "synthKdtDeviceToken000123456789"
	synthWebBearer := "AAAAAAAAAAAAAAAAAAAAASynthWebBearer0001%3D%3D"

	leaks := []struct {
		name      string
		line      string
		want      string
		rawSecret string
	}{
		{"x_auth_token", "Cookie: auth_token=" + synthAuth, "contains Cookie/token value", synthAuth},
		{"x_auth_token_quoted", `Cookie: auth_token="` + synthAuth + `"`, "contains Cookie/token value", synthAuth},
		{"x_auth_token_encoded", "Encoded: auth_token%3D" + synthAuth, "contains Cookie/token value", synthAuth},
		{"x_auth_token_query_escaped", "Query: " + url.QueryEscape("Cookie: auth_token="+synthAuth+"; ct0="+synthCT0+"; kdt="+synthKDT), "contains Cookie/token value", synthAuth},
		{"x_auth_token_json", `JSON: {"auth_token":"` + synthAuth + `"}`, "contains Cookie/token value", synthAuth},
		{"x_ct0", "Cookie: ct0=" + synthCT0, "contains Cookie/token value", synthCT0},
		{"x_ct0_encoded", "Encoded: ct0%3d" + synthCT0, "contains Cookie/token value", synthCT0},
		{"x_ct0_json", `JSON: {"ct0":"` + synthCT0 + `"}`, "contains Cookie/token value", synthCT0},
		{"x_kdt", "Cookie: kdt=" + synthKDT, "contains Cookie/token value", synthKDT},
		{"x_kdt_encoded", "Encoded: kdt%3D" + synthKDT, "contains Cookie/token value", synthKDT},
		{"x_kdt_json", `JSON: {"kdt":"` + synthKDT + `"}`, "contains Cookie/token value", synthKDT},
		{"x_csrf_header", "x-csrf-token: " + synthCT0, "contains Cookie/token value", synthCT0},
		{"x_csrf_header_upper_quoted", `X-CSRF-Token: "` + synthCT0 + `"`, "contains Cookie/token value", synthCT0},
		{"x_csrf_kv", "x-csrf-token=" + synthCT0, "contains Cookie/token value", synthCT0},
		{"x_csrf_kv_encoded", "x-csrf-token%3D" + synthCT0, "contains Cookie/token value", synthCT0},
		{"x_csrf_query_escaped", url.QueryEscape("x-csrf-token: " + synthCT0), "contains Cookie/token value", synthCT0},
		{"x_csrf_path_escaped", url.PathEscape("x-csrf-token: " + synthCT0), "contains Cookie/token value", synthCT0},
		{"x_csrf_json", `JSON: {"x-csrf-token":"` + synthCT0 + `"}`, "contains Cookie/token value", synthCT0},
		{"x_bearer_header", "Authorization: Bearer " + synthWebBearer, "contains Cookie/token value", synthWebBearer},
		{"x_bearer_json", `JSON: {"Authorization":"Bearer AAAAAAAAAAAAAAAAAAAAASynthJsonBearer0002%2F%3D"}`, "contains Cookie/token value", "AAAAAAAAAAAAAAAAAAAAASynthJsonBearer0002%2F%3D"},
		{"x_bearer_query_escaped", url.QueryEscape("Authorization: Bearer SYNTH_BEARER_ENC_507"), "contains Cookie/token value", "SYNTH_BEARER_ENC_507"},
		{"x_bearer_path_escaped", url.PathEscape("Authorization: Bearer SYNTH_BEARER_PATH_508"), "contains Cookie/token value", "SYNTH_BEARER_PATH_508"},
		{"x_bearer_bare", "Token: `Bearer AAAAAAAAAAAAAAAAAAAAASynthBareBearer0003`", "contains Cookie/token value", "AAAAAAAAAAAAAAAAAAAAASynthBareBearer0003"},
		{"x_bearer_raw_literal", "Literal: `AAAAAAAAAAAAAAAAAAAAASynthRawWebBearer0005%3D%3D`", "contains Cookie/token value", "AAAAAAAAAAAAAAAAAAAAASynthRawWebBearer0005%3D%3D"},
		{"x_signed_hls_token", "HLS: https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12&token=synth_hls_sig_01234567", "contains signed URL", "synth_hls_sig_01234567"},
		{"x_signed_hls_json_amp", `HLS: https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12\u0026token=synth_hls_json_01234567`, "contains signed URL", "synth_hls_json_01234567"},
		{"x_signed_cloudfront_mp4", "CDN: https://video.twimg.com/amplify_video/1000000000000000009/vid/synth.mp4?Expires=1763700000&Signature=AbCdEfGh12345678&Key-Pair-Id=APKASYNTH0001", "contains signed URL", "AbCdEfGh12345678"},
		{"x_signed_hdnts_mp4", "CDN: https://video-s.twimg.com/ext_tw_video/1000000000000000009/pu/vid/synth.mp4?hdnts=exp=1763700000~acl=/ext_tw_video/*~hmac=0123456789abcdef0123456789abcdef", "contains signed URL", "0123456789abcdef0123456789abcdef"},
		{"x_signed_encoded_url", "Encoded: https%3A%2F%2Fvideo.twimg.com%2Fext_tw_video%2F1000000000000000009%2Fpu%2Fpl%2Fsynth.m3u8%3Ftag%3D12%26token%3Dsynth_enc_sig_01234567", "contains signed URL", "synth_enc_sig_01234567"},
		{"x_signed_pscp_live", "Live: https://prod-fastly-us.video.pscp.tv/Transcoding/v1/hls/synth/playlist.m3u8?token=synth_pscp_sig_01234567", "contains signed URL", "synth_pscp_sig_01234567"},
		{"x_private_ton_x", "DM: https://ton.x.com/i/ton/data/dm/1000000000000000009/synth_private_dm.jpg", "contains signed URL", "synth_private_dm.jpg"},
		{"x_private_ton_twitter", "DM: https://ton.twitter.com/i/ton/data/dm/1000000000000000009/synth_private_dm2.jpg", "contains signed URL", "synth_private_dm2.jpg"},
	}

	for _, tc := range leaks {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(caseDir, "LEAK.md"), []byte("# Leak\n\n"+tc.line+"\n"), 0o600); err != nil {
				t.Fatalf("write LEAK.md (%s): %v", tc.name, err)
			}
			gotFindings, _, err := Run(caseDir, emptyRefDir)
			if err != nil {
				t.Fatalf("Run(%s): %v", tc.name, err)
			}
			joinedGot := strings.Join(gotFindings, "\n")
			if !strings.Contains(joinedGot, tc.want) {
				t.Fatalf("%s: expected finding %q, got: %v", tc.name, tc.want, gotFindings)
			}
			if strings.Contains(joinedGot, tc.rawSecret) {
				t.Fatalf("%s: raw secret %q leaked into finding output: %s", tc.name, tc.rawSecret, joinedGot)
			}
			if !strings.Contains(joinedGot, "<redacted>") {
				t.Fatalf("%s: expected <redacted> in finding output: %s", tc.name, joinedGot)
			}
		})
	}

	t.Run("placeholders and public URLs allowed", func(t *testing.T) {
		dir := t.TempDir()
		safeDoc := strings.Join([]string{
			"# X Safe Placeholders",
			"",
			"- Cookie placeholders: `auth_token=<合成占位>`, `ct0=<合成占位>`, `kdt=<合成占位>`, `twid=<合成占位>`, `guest_id=<合成占位>`.",
			"- English placeholders: `auth_token=<synthetic>`, `ct0=<synthetic>`, `kdt=<synthetic>`, `x-csrf-token: <ct0>`.",
			"- Bare key mentions: `auth_token=` / `ct0=` / `kdt=` / `Set-Cookie: ct0=...` / `auth_token=[REDACTED]` / `x-csrf-token: [REDACTED]`.",
			"- Bearer placeholders: `Bearer <公开网页端固定 Bearer 令牌>`, `Authorization: Bearer <令牌>`, `Authorization: Bearer [REDACTED]`, App-Only Bearer Token, Bearer authentication-mechanism.",
			"- Public image & avatar URLs: `https://pbs.twimg.com/media/Synthetic01?format=jpg&name=orig`, `https://pbs.twimg.com/profile_images/1000000000000000001/synth_400x400.jpg`.",
			"- Public GIF & progressive MP4 URLs: `https://video.twimg.com/tweet_video/SyntheticGif01.mp4`, `https://video.twimg.com/ext_tw_video/1000000000000000009/pu/vid/avc1/1280x720/synth.mp4?tag=12`.",
			"- Public static script URL: `https://abs.twimg.com/responsive-web/client-web/ondemand.s.<hash>a.js`.",
			"- Signed/private URL placeholders: `https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12&token=<合成签名>`, `https://cdn.syndication.twimg.com/tweet-result?id=<id>&token=<token>`, `https://ton.x.com/i/ton/data/<合成路径>`.",
		}, "\n")
		if err := os.WriteFile(filepath.Join(dir, "SAFE.md"), []byte(safeDoc), 0o600); err != nil {
			t.Fatal(err)
		}
		findings, _, err := Run(dir, emptyRefDir)
		if err != nil || len(findings) != 0 {
			t.Fatalf("unexpected findings for X placeholders: %v (err=%v)", findings, err)
		}
	})

	t.Run("actual docs/research/x.md has no secret false positives", func(t *testing.T) {
		xDocPath := filepath.Join("..", "..", "docs", "research", "x.md")
		raw, err := os.ReadFile(xDocPath)
		if err != nil {
			t.Fatalf("read %s: %v", xDocPath, err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "x.md"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		findings, _, err := Run(dir, emptyRefDir)
		if err != nil {
			t.Fatalf("Run on x.md copy: %v", err)
		}
		for _, f := range findings {
			if strings.Contains(f, "Cookie/token") || strings.Contains(f, "signed URL") {
				t.Fatalf("false positive on docs/research/x.md: %s", f)
			}
		}
	})
}
