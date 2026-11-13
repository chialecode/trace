package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanDirForSecrets(t *testing.T) {
	t.Run("clean directory passes", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "credentials"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "logs", "app.log"), []byte("2026-11-12T12:00:00Z INFO startup ok site=douyin state=logged_in\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(`{"activeRoute":"/home","siteFilter":"all"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials", "douyin.bin"), []byte{0x01, 0x00, 0xfe, 0x99, 0x42, 0xaa, 0xbb, 0xcc}, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := scanDirForSecrets(dir); err != nil {
			t.Fatalf("expected clean directory to pass secret scan, got: %v", err)
		}
	})

	cases := []struct {
		name     string
		fileName string
		content  string
		wantSub  string
	}{
		{
			name:     "bilibili SESSDATA cookie",
			fileName: "app.log",
			content:  "cookie leaked: SESSDATA=synth_bili_value",
			wantSub:  "SESSDATA=",
		},
		{
			name:     "sentinel marker",
			fileName: "ui-state.json",
			content:  `{"debug":"SENTINEL_DOUYIN_SECRET_01"}`,
			wantSub:  "SENTINEL_",
		},
		{
			name:     "douyin sessionid cookie",
			fileName: "app.log",
			content:  "header Cookie: sessionid=synth_dy_sess",
			wantSub:  "sessionid=",
		},
		{
			name:     "douyin sessionid_ss cookie",
			fileName: "app.log",
			content:  "header Cookie: sessionid_ss=synth_dy_sess_ss",
			wantSub:  "sessionid_ss=",
		},
		{
			name:     "douyin sid_guard cookie",
			fileName: "app.log",
			content:  "sid_guard=synth_guard_val",
			wantSub:  "sid_guard=",
		},
		{
			name:     "douyin msToken cookie",
			fileName: "app.log",
			content:  "msToken=synth_mstoken_val",
			wantSub:  "msToken=",
		},
		{
			name:     "douyin UIFID cookie",
			fileName: "app.log",
			content:  "UIFID=synth_uifid_val",
			wantSub:  "UIFID=",
		},
		{
			name:     "douyin ttwid cookie",
			fileName: "app.log",
			content:  "ttwid=1%7Csynth_ttwid",
			wantSub:  "ttwid=",
		},
		{
			name:     "douyin signed URL with a_bogus",
			fileName: "cache.log",
			content:  "GET https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=7000000000000000001&a_bogus=SynthBogusToken1234",
			wantSub:  "signed URL",
		},
		{
			name:     "douyin signed URL with x-secsdk-web-signature",
			fileName: "cache.log",
			content:  "GET https://www.douyin.com/aweme/v1/web/aweme/post/?x-secsdk-web-signature=0123456789abcdef0123456789abcdef",
			wantSub:  "signed URL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, tc.fileName)
			if err := os.WriteFile(target, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := scanDirForSecrets(dir)
			if err == nil {
				t.Fatalf("expected scanDirForSecrets to fail for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) || !strings.Contains(err.Error(), tc.fileName) {
				t.Fatalf("unexpected error %q, want substrings %q and %q", err.Error(), tc.wantSub, tc.fileName)
			}
		})
	}
}
