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

	// 4. Douyin secret/sentinel leak detection (sessionid=, sessionid_ss=, sid_guard=, msToken=, a_bogus=, x-signature)
	douyinLeaks := []struct {
		name string
		line string
		want string
	}{
		{"sessionid", "Cookie: sessionid=0123456789abcdef0123456789abcdef", "contains Cookie/token value"},
		{"sessionid_ss", "Cookie: sessionid_ss=fedcba9876543210fedcba9876543210", "contains Cookie/token value"},
		{"sid_guard", "Cookie: sid_guard=tok123%7C1760000000%7C5184000", "contains Cookie/token value"},
		{"msToken", "Cookie: msToken=AbCdEfGhIjKlMnOp123456", "contains Cookie/token value"},
		{"a_bogus_url", "URL: https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=7300000000000000001&a_bogus=dJUnkqSiQoWb", "contains signed URL"},
		{"x_signature_url", "CDN: https://v3-web.douyinvod.com/video/tos/cn/1/?x-expires=1763003600&x-signature=9f8e7d6c5b4a", "contains signed URL"},
	}
	for _, tc := range douyinLeaks {
		caseDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(caseDir, "LEAK.md"), []byte("# Leak\n\n"+tc.line+"\n"), 0o600); err != nil {
			t.Fatalf("write LEAK.md (%s): %v", tc.name, err)
		}
		gotFindings, _, err := Run(caseDir, emptyRefDir)
		if err != nil {
			t.Fatalf("Run(%s): %v", tc.name, err)
		}
		if joinedGot := strings.Join(gotFindings, "\n"); !strings.Contains(joinedGot, tc.want) {
			t.Fatalf("%s: expected finding %q, got: %v", tc.name, tc.want, gotFindings)
		}
	}
}
