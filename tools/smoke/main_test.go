package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		if err := os.WriteFile(filepath.Join(dir, "logs", "app.log"), []byte("2026-11-22T12:00:00Z INFO startup ok site=douyin site=weibo site=x state=logged_in\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ui-state.json"), []byte(`{"activeRoute":"/home","siteFilter":"x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials", "douyin.bin"), []byte{0x01, 0x00, 0xfe, 0x99, 0x42, 0xaa, 0xbb, 0xcc}, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials", "weibo.bin"), []byte{0x02, 0x00, 0xfd, 0x88, 0x53, 0xdd, 0xee, 0xff}, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials", "x.bin"), []byte{0x03, 0x00, 0xfc, 0x77, 0x64, 0x11, 0x22, 0x33}, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := scanDirForSecrets(dir); err != nil {
			t.Fatalf("expected clean directory to pass secret scan, got: %v", err)
		}
	})

	t.Run("x placeholders and public media URLs pass without false positives", func(t *testing.T) {
		dir := t.TempDir()
		cleanText := strings.Join([]string{
			`auth_token=<合成占位>; ct0=<合成占位>; kdt=<合成占位>; twid=<合成占位>; guest_id=<合成占位>`,
			`auth_token=<synthetic>; ct0=[REDACTED]; kdt=...`,
			`x-csrf-token: [REDACTED]`,
			`x-csrf-token: required`,
			`Authorization: Bearer <公开网页端固定 Bearer 令牌>`,
			`Authorization: Bearer [REDACTED]`,
			`https://pbs.twimg.com/media/Synthetic01?format=jpg&name=orig`,
			`https://video.twimg.com/ext_tw_video/1000000000000000009/pu/vid/avc1/1280x720/synth.mp4?tag=12`,
			`https://abs.twimg.com/responsive-web/client-web/ondemand.s.synth1a.js`,
			`https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12&token=<合成签名>`,
			`https://ton.x.com/i/ton/data/<合成路径>`,
		}, "\n")
		if err := os.WriteFile(filepath.Join(dir, "x-safe.log"), []byte(cleanText), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := scanDirForSecrets(dir); err != nil {
			t.Fatalf("expected X placeholders & public URLs to pass without false positives, got: %v", err)
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
		{
			name:     "weibo SUB cookie",
			fileName: "app.log",
			content:  "header Cookie: SUB=_2A25SynthWeiboSub0001",
			wantSub:  "SUB=",
		},
		{
			name:     "weibo SUBP cookie",
			fileName: "app.log",
			content:  "SUBP=0033WrSXqPxfM725SynthSubp",
			wantSub:  "SUBP=",
		},
		{
			name:     "weibo XSRF-TOKEN cookie",
			fileName: "app.log",
			content:  "XSRF-TOKEN=AbCdEfGh123456_7890",
			wantSub:  "XSRF-TOKEN=",
		},
		{
			name:     "weibo X-XSRF-TOKEN header",
			fileName: "app.log",
			content:  "req header X-XSRF-TOKEN: AbCdEfGh123456_7890",
			wantSub:  "X-XSRF-TOKEN:",
		},
		{
			name:     "weibo WBPSESS cookie",
			fileName: "app.log",
			content:  "WBPSESS=Dt2hbAUaXSynthWbpsess01==",
			wantSub:  "WBPSESS=",
		},
		{
			name:     "weibo SCF cookie",
			fileName: "app.log",
			content:  "SCF=AlSynthScfToken0123456789",
			wantSub:  "SCF=",
		},
		{
			name:     "weibo ALF cookie",
			fileName: "app.log",
			content:  "ALF=1795000000",
			wantSub:  "ALF=",
		},
		{
			name:     "weibo SSOLoginState cookie",
			fileName: "app.log",
			content:  "SSOLoginState=1763700000",
			wantSub:  "SSOLoginState=",
		},
		{
			name:     "weibo signed video URL with ssig",
			fileName: "item.json",
			content:  `{"url":"https://f.video.weibocdn.com/o0/synth01.mp4?Expires=1763700000&ssig=AbCdEf1234&KID=unistore,video"}`,
			wantSub:  "signed URL",
		},
		{
			name:     "weibo signed video URL with Expires and KID",
			fileName: "item.json",
			content:  `{"url":"https://f.video.weibocdn.com/o0/synth02.mp4?Expires=1763700000&KID=unistore,video"}`,
			wantSub:  "signed URL",
		},
		{
			name:     "weibo percent-encoded livephoto signed URL",
			fileName: "cache.log",
			content:  "GET https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D1763700000%26ssig%3DEncLiveSig1234%26KID%3Dunistore%2Cvideo",
			wantSub:  "signed URL",
		},
		{
			name:     "x auth_token cookie",
			fileName: "app.log",
			content:  "header Cookie: auth_token=0123456789abcdef0123456789abcdef01234567",
			wantSub:  "auth_token=",
		},
		{
			name:     "x auth_token percent-encoded",
			fileName: "app.log",
			content:  "query auth_token%3D0123456789abcdef0123456789abcdef01234567",
			wantSub:  "auth_token=",
		},
		{
			name:     "x ct0 cookie",
			fileName: "app.log",
			content:  "header Cookie: ct0=abcdef0123456789abcdef0123456789",
			wantSub:  "ct0=",
		},
		{
			name:     "x kdt cookie",
			fileName: "app.log",
			content:  "header Cookie: kdt=synthKdtDeviceToken000123456789",
			wantSub:  "kdt=",
		},
		{
			name:     "x x-csrf-token header",
			fileName: "app.log",
			content:  "req header x-csrf-token: abcdef0123456789abcdef0123456789",
			wantSub:  "x-csrf-token",
		},
		{
			name:     "x Authorization Bearer header",
			fileName: "app.log",
			content:  "req header Authorization: Bearer AAAAAAAAAAAAAAAAAAAAASynthSmokeBearer0001%3D%3D",
			wantSub:  "Authorization: Bearer",
		},
		{
			name:     "x signed video URL with token",
			fileName: "item.json",
			content:  `{"url":"https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12&token=synth_hls_sig_01234567"}`,
			wantSub:  "signed URL",
		},
		{
			name:     "x private ton.x.com URL",
			fileName: "cache.log",
			content:  `{"media":"https://ton.x.com/i/ton/data/dm/1000000000000000009/synth_private_dm.jpg"}`,
			wantSub:  "signed URL",
		},
	}

	rawSecrets := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"abcdef0123456789abcdef0123456789",
		"synthKdtDeviceToken000123456789",
		"AAAAAAAAAAAAAAAAAAAAASynthSmokeBearer0001%3D%3D",
		"synth_hls_sig_01234567",
		"synth_private_dm.jpg",
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
			for _, secret := range rawSecrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("scanDirForSecrets echoed raw secret %q in error %q", secret, err.Error())
				}
			}
		})
	}
}

func TestFormatFocusFailure(t *testing.T) {
	withDetail := formatFocusFailure("traySnap.StealFocusDetected = true", RuntimeStateSnapshot{
		ForegroundPID:    4242,
		StealFocusDetail: `close_to_tray (hwnd=0x1020 class="STATIC" visible=false)`,
	})
	if !strings.Contains(withDetail, "close_to_tray") || !strings.Contains(withDetail, `class="STATIC"`) || !strings.Contains(withDetail, "fgPid=4242") {
		t.Fatalf("unexpected formatted focus failure with detail: %q", withDetail)
	}

	withoutDetail := formatFocusFailure("traySnap.StealFocusDetected = true", RuntimeStateSnapshot{
		ForegroundPID: 99,
	})
	if withoutDetail != "traySnap.StealFocusDetected = true (fgPid=99)" {
		t.Fatalf("unexpected formatted focus failure without detail: %q", withoutDetail)
	}
}

func makeDirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Fatalf("os.Symlink(%s, %s): %v", target, link, err)
	}
	// On non-elevated Windows without Developer Mode, fall back to an NTFS
	// directory junction (which os.Lstat also reports as a symlink/reparse point).
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
	}
}

func TestCleanArtifactsOutDir(t *testing.T) {
	repo := t.TempDir()
	artifactsRoot := filepath.Join(repo, "artifacts")
	smokeDir := filepath.Join(artifactsRoot, "smoke")
	siblingDir := filepath.Join(artifactsRoot, "uishots")
	distDir := filepath.Join(repo, "dist")

	for _, d := range []string{smokeDir, siblingDir, distDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(repo, "keep-root.txt"), "root")
	mustWrite(filepath.Join(distDir, "keep-dist.txt"), "dist")
	mustWrite(filepath.Join(siblingDir, "keep-sibling.txt"), "sibling")
	mustWrite(filepath.Join(smokeDir, "stale.json"), "stale")

	t.Run("valid category subdir cleaned without touching sibling", func(t *testing.T) {
		if err := cleanArtifactsOutDirIn(artifactsRoot, smokeDir); err != nil {
			t.Fatalf("cleanArtifactsOutDirIn valid dir: %v", err)
		}
		if _, err := os.Stat(filepath.Join(smokeDir, "stale.json")); !os.IsNotExist(err) {
			t.Fatalf("stale file not removed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(siblingDir, "keep-sibling.txt")); err != nil {
			t.Fatalf("sibling artifacts file touched: %v", err)
		}
	})

	t.Run("refuses repo root (-out .)", func(t *testing.T) {
		if err := cleanArtifactsOutDirIn(artifactsRoot, repo); err == nil {
			t.Fatal("expected error for repo root")
		}
		if _, err := os.Stat(filepath.Join(repo, "keep-root.txt")); err != nil {
			t.Fatalf("repo root file deleted: %v", err)
		}
	})

	t.Run("refuses dist (-out dist)", func(t *testing.T) {
		if err := cleanArtifactsOutDirIn(artifactsRoot, distDir); err == nil {
			t.Fatal("expected error for dist")
		}
		if _, err := os.Stat(filepath.Join(distDir, "keep-dist.txt")); err != nil {
			t.Fatalf("dist file deleted: %v", err)
		}
	})

	t.Run("refuses artifacts root itself", func(t *testing.T) {
		if err := cleanArtifactsOutDirIn(artifactsRoot, artifactsRoot); err == nil {
			t.Fatal("expected error for artifacts root itself")
		}
		if _, err := os.Stat(filepath.Join(siblingDir, "keep-sibling.txt")); err != nil {
			t.Fatalf("sibling artifacts file deleted: %v", err)
		}
	})

	t.Run("refuses traversal outside artifacts", func(t *testing.T) {
		if err := cleanArtifactsOutDirIn(artifactsRoot, filepath.Join(artifactsRoot, "..", "dist")); err == nil {
			t.Fatal("expected error for path traversal")
		}
		if _, err := os.Stat(filepath.Join(distDir, "keep-dist.txt")); err != nil {
			t.Fatalf("dist file deleted via traversal: %v", err)
		}
	})

	t.Run("refuses symlinked output dir and symlinked ancestor", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "victim")
		nested := filepath.Join(outside, "sub")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(filepath.Join(outside, "precious.txt"), "keep")
		mustWrite(filepath.Join(nested, "precious-sub.txt"), "keep-sub")

		linkOut := filepath.Join(artifactsRoot, "link-smoke")
		makeDirLink(t, outside, linkOut)

		if err := cleanArtifactsOutDirIn(artifactsRoot, linkOut); err == nil {
			t.Fatal("expected error for symlinked output dir")
		}
		if err := cleanArtifactsOutDirIn(artifactsRoot, filepath.Join(linkOut, "sub")); err == nil {
			t.Fatal("expected error for nested path under symlinked ancestor")
		}
		if _, err := os.Stat(filepath.Join(outside, "precious.txt")); err != nil {
			t.Fatalf("symlink target deleted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(nested, "precious-sub.txt")); err != nil {
			t.Fatalf("nested symlink target deleted: %v", err)
		}
	})

	t.Run("refuses symlinked artifacts root", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside-artifacts")
		sub := filepath.Join(outside, "smoke")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(filepath.Join(sub, "keep.txt"), "keep")
		linkRoot := filepath.Join(t.TempDir(), "artifacts-link")
		makeDirLink(t, outside, linkRoot)

		if err := cleanArtifactsOutDirIn(linkRoot, filepath.Join(linkRoot, "smoke")); err == nil {
			t.Fatal("expected error for symlinked artifacts root")
		}
		if _, err := os.Stat(filepath.Join(sub, "keep.txt")); err != nil {
			t.Fatalf("target under symlinked root deleted: %v", err)
		}
	})
}

func TestBuildSmokeChildEnvForcesQuiet(t *testing.T) {
	inherited := []string{"PATH=/usr/bin", "TRACE_QUIET=0", "trace_quiet=false", "OTHER=ok"}
	env := buildSmokeChildEnv(inherited, "l", "a", "u", "d", "t", "s")
	var quietVals []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "TRACE_QUIET") {
			quietVals = append(quietVals, v)
		}
	}
	if len(quietVals) != 1 || quietVals[0] != "1" {
		t.Fatalf("TRACE_QUIET values = %v, want [1]", quietVals)
	}
}

func TestFormatSmokeFailureIncludesEarlyError(t *testing.T) {
	earlyErr := errors.New("missing trace.exe in /pkg")
	msg := formatSmokeFailure(SmokeReceipt{DurationMs: 12}, earlyErr)
	if !strings.Contains(msg, "missing trace.exe") || strings.Contains(msg, "[]") {
		t.Fatalf("unexpected failure message: %q", msg)
	}
}

func TestHasTokenLeakChecksRestartOutput(t *testing.T) {
	dir := t.TempDir()
	tok := "secret-smoke-token-1234"
	if hasTokenLeak(dir, tok, "first run ok", "restart ok") {
		t.Fatal("unexpected leak reported on clean outputs")
	}
	if !hasTokenLeak(dir, tok, "first run ok", "restarted with "+tok) {
		t.Fatal("expected leak in restart output to be detected")
	}
}

func TestWriteServerReceiptInChecksBounds(t *testing.T) {
	repo := t.TempDir()
	allowed := filepath.Join(repo, "artifacts")
	if err := writeServerReceiptIn(allowed, filepath.Join(repo, "dist"), ServerSmokeReceipt{Passed: true}); err == nil {
		t.Fatal("expected error writing server receipt outside artifacts")
	}
	out := filepath.Join(allowed, "smoke-server")
	if err := writeServerReceiptIn(allowed, out, ServerSmokeReceipt{Passed: true}); err != nil {
		t.Fatalf("writeServerReceiptIn valid dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "smoke.json")); err != nil {
		t.Fatalf("smoke.json not written: %v", err)
	}
}

func TestWeiboFakeRemoteSmokeAndSentinels(t *testing.T) {
	if err := runWeiboFakeRemoteSmoke(context.Background()); err != nil {
		t.Fatalf("runWeiboFakeRemoteSmoke failed: %v", err)
	}
}

func TestXFakeRemoteSmokeAndSentinels(t *testing.T) {
	if err := runXFakeRemoteSmoke(context.Background()); err != nil {
		t.Fatalf("runXFakeRemoteSmoke failed: %v", err)
	}
}
