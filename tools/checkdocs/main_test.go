package main

import (
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
