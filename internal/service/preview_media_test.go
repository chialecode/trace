package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
)

func TestStagePreviewMediaCacheHitReuseAndStartupSweep(t *testing.T) {
	if maxPreviewMediaBytes != 128*1024*1024 {
		t.Fatalf("maxPreviewMediaBytes = %d, want 128 MiB", maxPreviewMediaBytes)
	}

	tmp := t.TempDir()
	loc := appdirs.Locations{
		AppName:         "trace-dev",
		Channel:         "dev",
		Mode:            "test",
		DataDir:         filepath.Join(tmp, "data"),
		CredentialsDir:  filepath.Join(tmp, "data", "credentials"),
		LogsDir:         filepath.Join(tmp, "data", "logs"),
		DefaultCacheDir: filepath.Join(tmp, "cache"),
	}

	// Pre-populate stale preview and crashed temp files before NewBackend starts.
	previewDir := filepath.Join(loc.DataDir, "cache", "preview")
	if err := os.MkdirAll(previewDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(previewDir, "preview_douyin_stale000000000001.mp4")
	crashedTmp := filepath.Join(previewDir, storage.TempPrefix+"preview_douyin_crash.123-deadbeef.tmp")
	if err := os.WriteFile(staleFile, []byte("stale-leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crashedTmp, []byte("crashed-partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	var cdnHits atomic.Int32
	mp4Payload := append([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, bytes.Repeat([]byte("A"), 2048)...)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status_code":0,"user":{"uid":"1001","sec_uid":"MS4wLjABAAAA_preview_self","nickname":"预览测试号","unique_id":"preview_user"}}`)
		case "/cdn/short.mp4":
			cdnHits.Add(1)
			if r.Header.Get("Cookie") != "" {
				t.Errorf("Douyin preview CDN leaked Cookie: %q", r.Header.Get("Cookie"))
			}
			if r.Header.Get("Referer") != douyin.DefaultReferer {
				t.Errorf("Douyin preview CDN Referer = %q, want %q", r.Header.Get("Referer"), douyin.DefaultReferer)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4Payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}

	// Startup sweep must remove both stale staged previews and crashed temp files.
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("expected startup sweep to remove stale preview file, stat err=%v", err)
	}
	if _, err := os.Stat(crashedTmp); !os.IsNotExist(err) {
		t.Fatalf("expected startup sweep to remove crashed temp file, stat err=%v", err)
	}

	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_PREVIEW_REUSE_SESS; sessionid_ss=SYNTH_PREVIEW_REUSE_SESS; uid_tt=1001", ""); err != nil {
		_ = b.Close()
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}

	// First StagePreviewMedia call fetches once and writes to previewDir (never library.db).
	mediaURL := srv.URL + "/cdn/short.mp4?expires=1999999999&sign=SYNTH_SIGN_REUSE"
	firstPath, err := b.StagePreviewMedia(ctx, "douyin", mediaURL, ".mp4")
	if err != nil {
		_ = b.Close()
		t.Fatalf("StagePreviewMedia first call: %v", err)
	}
	if cdnHits.Load() != 1 {
		_ = b.Close()
		t.Fatalf("cdnHits after first call = %d, want 1", cdnHits.Load())
	}
	if filepath.Dir(firstPath) != previewDir {
		_ = b.Close()
		t.Fatalf("staged file dir = %q, want %q", filepath.Dir(firstPath), previewDir)
	}
	if !strings.HasPrefix(filepath.Base(firstPath), "preview_douyin_") || !strings.HasSuffix(firstPath, ".mp4") {
		_ = b.Close()
		t.Fatalf("unexpected staged filename: %q", filepath.Base(firstPath))
	}

	// Library DB and CacheDir must remain completely untouched by temporary previews.
	items, err := b.ListLibraryItems(library.FeedFilter{Limit: 10})
	if err != nil || len(items) != 0 {
		_ = b.Close()
		t.Fatalf("expected 0 library items after StagePreviewMedia, got %d err=%v", len(items), err)
	}

	// Backdate mtime, call StagePreviewMedia again -> cache hit with 0 HTTP requests and refreshed mtime.
	past := time.Now().Add(-15 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(firstPath, past, past); err != nil {
		_ = b.Close()
		t.Fatalf("os.Chtimes: %v", err)
	}
	secondPath, err := b.StagePreviewMedia(ctx, "douyin", mediaURL, ".mp4")
	if err != nil {
		_ = b.Close()
		t.Fatalf("StagePreviewMedia second call: %v", err)
	}
	if secondPath != firstPath {
		_ = b.Close()
		t.Fatalf("cache-hit path = %q, want %q", secondPath, firstPath)
	}
	if cdnHits.Load() != 1 {
		_ = b.Close()
		t.Fatalf("cache-hit should make 0 new HTTP requests, got total hits = %d", cdnHits.Load())
	}
	info, err := os.Stat(secondPath)
	if err != nil {
		_ = b.Close()
		t.Fatalf("stat cache-hit file: %v", err)
	}
	if !info.ModTime().After(past.Add(time.Minute)) {
		_ = b.Close()
		t.Fatalf("expected cache-hit to refresh mtime beyond %v, got %v", past, info.ModTime())
	}

	// Backend.Close() removes previewDir cleanly.
	if err := b.Close(); err != nil {
		t.Fatalf("Backend.Close: %v", err)
	}
	if _, err := os.Stat(previewDir); !os.IsNotExist(err) {
		t.Fatalf("expected Backend.Close to remove previewDir, stat err=%v", err)
	}
}

func TestStagePreviewMediaStreamingLimitCancelAndVideoFriendlyErrors(t *testing.T) {
	cancelGate := make(chan struct{})
	chunkSent := make(chan struct{})
	var closeOnce sync.Once
	closeGate := func() { closeOnce.Do(func() { close(cancelGate) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status_code":0,"user":{"uid":"1002","sec_uid":"MS4wLjABAAAA_limit_self_000002","nickname":"限额测试号","unique_id":"limit_user"}}`)
		case "/cdn/oversize_cl.mp4":
			payload := append([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, bytes.Repeat([]byte("B"), 80*1024)...)
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			_, _ = w.Write(payload)
		case "/cdn/oversize_chunked.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			fl, _ := w.(http.Flusher)
			chunk := bytes.Repeat([]byte("C"), 16*1024)
			for i := 0; i < 6; i++ {
				if r.Context().Err() != nil {
					return
				}
				if i == 0 {
					chunkCopy := append([]byte(nil), chunk...)
					copy(chunkCopy[:12], []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
					_, _ = w.Write(chunkCopy)
				} else {
					_, _ = w.Write(chunk)
				}
				if fl != nil {
					fl.Flush()
				}
			}
		case "/cdn/cancel_stream.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 1, 2, 3, 4})
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			select {
			case chunkSent <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
			case <-cancelGate:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	// Unblock any handler before srv.Close per LESSONS.md.
	t.Cleanup(srv.Close)
	t.Cleanup(closeGate)

	tmp := t.TempDir()
	b, err := NewBackend(Options{
		Locations: appdirs.Locations{
			AppName:         "trace-dev",
			Channel:         "dev",
			Mode:            "test",
			DataDir:         filepath.Join(tmp, "data"),
			CredentialsDir:  filepath.Join(tmp, "data", "credentials"),
			LogsDir:         filepath.Join(tmp, "data", "logs"),
			DefaultCacheDir: filepath.Join(tmp, "cache"),
		},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
			},
			MinInterval:    -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	// Bound preview cap to 64 KiB for fast, quiet streaming limit verification.
	b.maxPreviewBytes = 64 * 1024

	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_LIMIT_SESS; sessionid_ss=SYNTH_LIMIT_SESS; uid_tt=1002", ""); err != nil {
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}

	assertCleanPreviewDir := func(t *testing.T, label string) {
		t.Helper()
		entries, rErr := os.ReadDir(b.previewDir)
		if rErr != nil {
			t.Fatalf("%s: ReadDir previewDir: %v", label, rErr)
		}
		if len(entries) != 0 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("%s: expected 0 leftover files in previewDir, got %v", label, names)
		}
	}

	// 1. Oversize via Content-Length header.
	_, err = b.StagePreviewMedia(ctx, "douyin", srv.URL+"/cdn/oversize_cl.mp4", ".mp4")
	if err == nil || !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("oversize Content-Length err = %v, want ErrKindBlocked", err)
	}
	if !strings.Contains(err.Error(), "缓存到媒体库") || strings.Contains(err.Error(), "抖音图片") {
		t.Fatalf("oversize error message should be video-friendly and suggest 缓存到媒体库, got: %q", err.Error())
	}
	assertCleanPreviewDir(t, "oversize_cl")

	// 2. Oversize via chunked streaming without Content-Length.
	_, err = b.StagePreviewMedia(ctx, "douyin", srv.URL+"/cdn/oversize_chunked.mp4", ".mp4")
	if err == nil || !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("oversize chunked err = %v, want ErrKindBlocked", err)
	}
	if !strings.Contains(err.Error(), "缓存到媒体库") || strings.Contains(err.Error(), "抖音图片") {
		t.Fatalf("chunked oversize error message should be video-friendly, got: %q", err.Error())
	}
	assertCleanPreviewDir(t, "oversize_chunked")

	// 3. Context cancellation mid-stream cleans up temp file cleanly.
	cancelCtx, cancel := context.WithCancel(ctx)
	doneCh := make(chan error, 1)
	go func() {
		_, sErr := b.StagePreviewMedia(cancelCtx, "douyin", srv.URL+"/cdn/cancel_stream.mp4", ".mp4")
		doneCh <- sErr
	}()
	select {
	case <-chunkSent:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for first streamed chunk")
	}
	cancel()
	select {
	case sErr := <-doneCh:
		if sErr == nil || !site.IsKind(sErr, site.ErrKindNetwork) {
			t.Fatalf("cancelled stream err = %v, want ErrKindNetwork", sErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancelled StagePreviewMedia to return")
	}
	assertCleanPreviewDir(t, "cancel_stream")
}

func TestStagePreviewMediaDouyinPlayRedirectCookielessAndAllowlist(t *testing.T) {
	var playHits atomic.Int32
	var cdnHits atomic.Int32
	var evilHits atomic.Int32
	mp4Payload := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 'm', 'p', '4', '2', 0, 0, 0, 8, 'm', 'd', 'a', 't'}

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		host := strings.ToLower(r.URL.Hostname())
		mkResp := func(status int, ct, loc string, body []byte) *http.Response {
			hdr := make(http.Header)
			if ct != "" {
				hdr.Set("Content-Type", ct)
			}
			if loc != "" {
				hdr.Set("Location", loc)
			}
			return &http.Response{
				StatusCode: status,
				Header:     hdr,
				Body:       ioNopCloser(bytes.NewReader(body)),
				Request:    r,
			}
		}

		switch {
		case host == "www.douyin.com" && r.URL.Path == "/aweme/v1/web/user/profile/self/":
			return mkResp(http.StatusOK, "application/json", "", []byte(`{"status_code":0,"user":{"uid":"2001","sec_uid":"MS4wLjABAAAA_redir_self_000003","nickname":"跳转测试号","unique_id":"redir_user"}}`)), nil
		case host == "www.douyin.com" && r.URL.Path == "/aweme/v1/play/":
			playHits.Add(1)
			if !strings.Contains(r.Header.Get("Cookie"), "sessionid=SYNTH_REDIR_COOKIE") {
				t.Errorf("play redirector on www.douyin.com expected Douyin Cookie")
			}
			switch r.URL.Query().Get("video_id") {
			case "synth_ok":
				return mkResp(http.StatusFound, "", "https://v26-web.douyinvod.com/synth/resolved.mp4?sign=SYNTH_REDIR_SIG_777", nil), nil
			case "synth_evil":
				return mkResp(http.StatusFound, "", "https://evil.example.com/bad.mp4", nil), nil
			default:
				return mkResp(http.StatusNotFound, "text/plain", "", []byte("missing")), nil
			}
		case host == "v26-web.douyinvod.com" && r.URL.Path == "/synth/resolved.mp4":
			cdnHits.Add(1)
			if r.Header.Get("Cookie") != "" {
				t.Errorf("resolved Douyin CDN request leaked Cookie: %q", r.Header.Get("Cookie"))
			}
			if r.Header.Get("Authorization") != "" {
				t.Errorf("resolved Douyin CDN request leaked Authorization: %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Referer") != douyin.DefaultReferer {
				t.Errorf("resolved Douyin CDN Referer = %q, want %q", r.Header.Get("Referer"), douyin.DefaultReferer)
			}
			return mkResp(http.StatusOK, "video/mp4", "", mp4Payload), nil
		case host == "evil.example.com":
			evilHits.Add(1)
			return mkResp(http.StatusOK, "video/mp4", "", mp4Payload), nil
		default:
			return mkResp(http.StatusNotFound, "", "", nil), nil
		}
	})

	tmp := t.TempDir()
	b, err := NewBackend(Options{
		Locations: appdirs.Locations{
			AppName:         "trace-dev",
			Channel:         "dev",
			Mode:            "test",
			DataDir:         filepath.Join(tmp, "data"),
			CredentialsDir:  filepath.Join(tmp, "data", "credentials"),
			LogsDir:         filepath.Join(tmp, "data", "logs"),
			DefaultCacheDir: filepath.Join(tmp, "cache"),
		},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		DouyinOpts: douyin.ClientOptions{
			MinInterval: -1,
			Transport:   rt,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	ctx := context.Background()

	// Logged-out request is refused with 0 HTTP hits.
	if _, err := b.StagePreviewMedia(ctx, "douyin", "https://www.douyin.com/aweme/v1/play/?video_id=synth_ok&line=0", ".mp4"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("logged-out StagePreviewMedia err = %v, want auth_required", err)
	}
	if playHits.Load() != 0 || cdnHits.Load() != 0 {
		t.Fatalf("logged-out call made HTTP requests: play=%d cdn=%d", playHits.Load(), cdnHits.Load())
	}

	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=SYNTH_REDIR_COOKIE; sessionid_ss=SYNTH_REDIR_COOKIE; uid_tt=2001", ""); err != nil {
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}

	// 1. Valid play redirect resolves via ResolvePlayRedirect and fetches CDN cookielessly.
	playURL := "https://www.douyin.com/aweme/v1/play/?video_id=synth_ok&line=0"
	staged, err := b.StagePreviewMedia(ctx, "douyin", playURL, ".mp4")
	if err != nil {
		t.Fatalf("StagePreviewMedia(play redirect ok): %v", err)
	}
	got, err := os.ReadFile(staged)
	if err != nil || !bytes.Equal(got, mp4Payload) {
		t.Fatalf("staged payload mismatch: len=%d err=%v", len(got), err)
	}
	if playHits.Load() != 1 || cdnHits.Load() != 1 {
		t.Fatalf("hits = (play:%d cdn:%d), want (1, 1)", playHits.Load(), cdnHits.Load())
	}

	// 2. Second call with same play redirect URL reuses local preview with 0 HTTP hits.
	staged2, err := b.StagePreviewMedia(ctx, "douyin", playURL, ".mp4")
	if err != nil || staged2 != staged {
		t.Fatalf("second StagePreviewMedia = (%q, %v), want (%q, nil)", staged2, err, staged)
	}
	if playHits.Load() != 1 || cdnHits.Load() != 1 {
		t.Fatalf("cache hit should not call play redirect or CDN again, got (play:%d cdn:%d)", playHits.Load(), cdnHits.Load())
	}

	// 3. Play redirect targeting non-allowlist host is blocked with 0 requests to evil host.
	if _, err := b.StagePreviewMedia(ctx, "douyin", "https://www.douyin.com/aweme/v1/play/?video_id=synth_evil&line=0", ".mp4"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("evil redirect err = %v, want blocked", err)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("evil host received %d requests, want 0", evilHits.Load())
	}

	// 4. Watermarked play redirect is blocked with 0 extra playHits.
	hitsBeforeWM := playHits.Load()
	if _, err := b.StagePreviewMedia(ctx, "douyin", "https://www.douyin.com/aweme/v1/play/?video_id=synth_ok&watermark=1", ".mp4"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("watermark play redirect err = %v, want blocked", err)
	}
	if playHits.Load() != hitsBeforeWM {
		t.Fatalf("watermarked redirect should send 0 HTTP requests")
	}
}

func TestStagePreviewMediaCredentialAndSignedURLSentinelRedaction(t *testing.T) {
	const (
		dyCookieSentinel = "SYNTH_DY_PREVIEW_SECRET_9911"
		wbCookieSentinel = "SYNTH_WB_PREVIEW_SECRET_9922"
		xCookieSentinel  = "SYNTH_X_PREVIEW_SECRET_9933"
		urlSignSentinel  = "SYNTH_SIGNED_QUERY_TOKEN_8844"
	)

	mp4Stub := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 1, 2, 3, 4}

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		host := strings.ToLower(r.URL.Hostname())
		mkResp := func(status int, ct string, body []byte) *http.Response {
			hdr := make(http.Header)
			if ct != "" {
				hdr.Set("Content-Type", ct)
			}
			return &http.Response{StatusCode: status, Header: hdr, Body: ioNopCloser(bytes.NewReader(body)), Request: r}
		}
		switch {
		case host == "www.douyin.com" && r.URL.Path == "/aweme/v1/web/user/profile/self/":
			return mkResp(http.StatusOK, "application/json", []byte(`{"status_code":0,"user":{"uid":"3001","sec_uid":"MS4wLjABAAAA_sec_self_000004","nickname":"脱敏测试号","unique_id":"sec_user"}}`)), nil
		case host == "v26-web.douyinvod.com" && r.URL.Path == "/ok.mp4":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("douyin CDN leaked Cookie")
			}
			return mkResp(http.StatusOK, "video/mp4", mp4Stub), nil
		case host == "v26-web.douyinvod.com" && r.URL.Path == "/fail_net.mp4":
			return nil, errors.New("synthetic connection reset")
		case host == "v26-web.douyinvod.com" && r.URL.Path == "/fail_403.mp4":
			return mkResp(http.StatusForbidden, "text/plain", []byte("forbidden")), nil
		case host == "weibo.com" && r.URL.Path == "/ajax/profile/info":
			return mkResp(http.StatusOK, "application/json", []byte(`{"ok":1,"data":{"user":{"idstr":"3002","screen_name":"微博脱敏号"}}}`)), nil
		case host == "f.video.weibocdn.com" && r.URL.Path == "/ok.mp4":
			if r.Header.Get("Cookie") != "" {
				t.Errorf("weibo CDN leaked Cookie")
			}
			return mkResp(http.StatusOK, "video/mp4", mp4Stub), nil
		case host == "video.twimg.com" && r.URL.Path == "/ok.mp4":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" {
				t.Errorf("x CDN leaked headers: Cookie=%q Referer=%q Origin=%q", r.Header.Get("Cookie"), r.Header.Get("Referer"), r.Header.Get("Origin"))
			}
			return mkResp(http.StatusOK, "video/mp4", mp4Stub), nil
		case host == "video.twimg.com" && r.URL.Path == "/fail_net.mp4":
			return nil, errors.New("synthetic dial timeout")
		default:
			return mkResp(http.StatusNotFound, "", nil), nil
		}
	})

	tmp := t.TempDir()
	loc := appdirs.Locations{
		AppName:         "trace-dev",
		Channel:         "dev",
		Mode:            "test",
		DataDir:         filepath.Join(tmp, "data"),
		CredentialsDir:  filepath.Join(tmp, "data", "credentials"),
		LogsDir:         filepath.Join(tmp, "data", "logs"),
		DefaultCacheDir: filepath.Join(tmp, "cache"),
	}
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		DouyinOpts: douyin.ClientOptions{
			MinInterval: -1,
			Transport:   rt,
		},
		WeiboOpts: weibo.ClientOptions{
			MinInterval: -1,
			Transport:   rt,
		},
		XOpts: x.ClientOptions{
			MinInterval: -1,
			Transport:   rt,
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid="+dyCookieSentinel+"; sessionid_ss="+dyCookieSentinel+"; uid_tt=3001", ""); err != nil {
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}
	// Seed Weibo and X credentials directly into their isolated credential stores to avoid full frontend bootstrap overhead.
	if _, err := b.weiboCred.SaveVerifiedWeiboManual(map[string]string{"SUB": wbCookieSentinel}, credential.WeiboProfile{UID: "3002", Uname: "微博脱敏号"}); err != nil {
		t.Fatalf("save weibo cred: %v", err)
	}
	if _, err := b.xCred.SaveVerifiedXManual(map[string]string{"auth_token": xCookieSentinel, "ct0": "SYNTHCT09933"}, credential.XProfile{RestID: "3003", ScreenName: "x_redact", Name: "X脱敏号"}); err != nil {
		t.Fatalf("save x cred: %v", err)
	}

	sentinels := []string{dyCookieSentinel, wbCookieSentinel, xCookieSentinel, urlSignSentinel}
	checkNoSentinel := func(contextLabel, text string) {
		for _, s := range sentinels {
			if strings.Contains(text, s) {
				t.Fatalf("sentinel %q leaked into %s: %q", s, contextLabel, text)
			}
		}
	}

	// Success paths across Douyin, Weibo, X with signed query token.
	for _, tc := range []struct {
		site string
		url  string
	}{
		{"douyin", "https://v26-web.douyinvod.com/ok.mp4?auth_key=" + urlSignSentinel + "&expires=1999999999"},
		{"weibo", "https://f.video.weibocdn.com/ok.mp4?ssig=" + urlSignSentinel + "&Expires=1999999999"},
		{"x", "https://video.twimg.com/ok.mp4?tag=12&token=" + urlSignSentinel},
	} {
		p, err := b.StagePreviewMedia(ctx, tc.site, tc.url, ".mp4")
		if err != nil {
			t.Fatalf("StagePreviewMedia(%s) ok failed: %v", tc.site, err)
		}
		checkNoSentinel("staged filename ("+tc.site+")", filepath.Base(p))
	}

	// Failure paths must never leak signed URL query token or Cookie into errors.
	for _, tc := range []struct {
		site string
		url  string
	}{
		{"douyin", "https://v26-web.douyinvod.com/fail_net.mp4?sign=" + urlSignSentinel},
		{"douyin", "https://v26-web.douyinvod.com/fail_403.mp4?sign=" + urlSignSentinel},
		{"douyin", "https://evil.example.com/blocked.mp4?sign=" + urlSignSentinel},
		{"x", "https://video.twimg.com/fail_net.mp4?token=" + urlSignSentinel},
	} {
		_, err := b.StagePreviewMedia(ctx, tc.site, tc.url, ".mp4")
		if err == nil {
			t.Fatalf("expected error for %s %s", tc.site, tc.url)
		}
		checkNoSentinel("error ("+tc.site+")", err.Error())
	}

	// Verify logs, history.json, and library.db contain zero sentinels and zero preview items.
	for _, rel := range []string{
		filepath.Join(loc.LogsDir, "trace.log"),
		filepath.Join(loc.DataDir, "history.json"),
	} {
		if raw, rErr := os.ReadFile(rel); rErr == nil {
			checkNoSentinel(filepath.Base(rel), string(raw))
		}
	}
	items, err := b.ListLibraryItems(library.FeedFilter{Limit: 20})
	if err != nil || len(items) != 0 {
		t.Fatalf("expected 0 items in library.db, got %d err=%v", len(items), err)
	}
	if histBytes, err := json.Marshal(b.ListHistory()); err == nil {
		checkNoSentinel("ListHistory", string(histBytes))
	}
}
