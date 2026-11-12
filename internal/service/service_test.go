package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func newTestBiliServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srvURL string

	samplePNG := func(col color.RGBA) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 16, 12))
		for y := 0; y < 12; y++ {
			for x := 0; x < 16; x++ {
				img.SetRGBA(x, y, col)
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return buf.Bytes()
	}

	mux.HandleFunc("/bfs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Errorf("R11 violation: thumbnail request leaked Cookie or Referer: %v", r.Header)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(samplePNG(color.RGBA{R: 214, G: 51, B: 108, A: 255}))
	})

	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true,
				"mid":     778899,
				"uname":   "TRACE测试账号",
				"face":    "https://i0.hdslb.com/bfs/face/member.jpg",
				"wbi_img": map[string]any{
					"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
					"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
				},
			},
		})
	})

	mux.HandleFunc("/x/passport-login/web/qrcode/generate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"url":        "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=testkey",
				"qrcode_key": "testkey",
			},
		})
	})

	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV1xx411c7mD",
				"aid":      170001,
				"title":    "测试视频详情",
				"desc":     "视频简介",
				"pic":      srvURL + "/bfs/archive/cover.jpg?upsig=secret_cover_sig",
				"pubdate":  1710000000,
				"duration": 120,
				"owner": map[string]any{
					"mid":  12345,
					"name": "测试UP主",
					"face": srvURL + "/bfs/face/up.jpg?upsig=secret_avatar_sig",
				},
				"pages": []map[string]any{
					{"cid": 1001, "page": 1, "part": "第一P", "duration": 120},
				},
			},
		})
	})

	mux.HandleFunc("/x/web-interface/card", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"card": map[string]any{
					"mid":  "12345",
					"name": "测试UP主",
					"sign": "签名内容",
					"face": "http://i0.hdslb.com/bfs/face/up.jpg",
					"fans": 999,
				},
				"archive_count": 10,
				"following":     true,
			},
		})
	})

	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"list": map[string]any{
					"vlist": []map[string]any{
						{
							"bvid":    "BV1xx411c7mD",
							"aid":     170001,
							"title":   "测试视频详情",
							"pic":     "http://i0.hdslb.com/bfs/archive/cover.jpg",
							"length":  "02:00",
							"created": 1710000000,
							"mid":     12345,
							"author":  "测试UP主",
						},
					},
				},
				"page": map[string]any{"pn": 1, "ps": 20, "count": 1},
			},
		})
	})

	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": false,
				"offset":   "",
				"items":    []any{},
			},
		})
	})

	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": "987654321012345678",
					"type":   "DYNAMIC_TYPE_WORD",
					"modules": map[string]any{
						"module_author": map[string]any{
							"mid":    12345,
							"name":   "测试UP主",
							"face":   "http://i0.hdslb.com/bfs/face/up.jpg",
							"pub_ts": 1710000100,
						},
						"module_dynamic": map[string]any{
							"desc": map[string]any{"text": "这是一条测试纯文字动态"},
						},
					},
				},
			},
		})
	})

	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     223344,
				"short_id":    223,
				"uid":         12345,
				"live_status": 1,
			},
		})
	})

	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"12345": map[string]any{
					"uid":         12345,
					"room_id":     223344,
					"uname":       "测试UP主",
					"title":       "正在直播写Go代码",
					"live_status": 1,
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	return srv
}

// TestServiceAuthIsolationOpenLinkAndRedaction verifies pre-login isolation, credential validation without overwriting existing sessions, link opening, and secret redaction.
func TestServiceAuthIsolationOpenLinkAndRedaction(t *testing.T) {
	srv := newTestBiliServer(t)
	defer srv.Close()

	tmp := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:   "dev",
		ForceMode: "portable",
		DataDir:   filepath.Join(tmp, "trace-data"),
		CacheDir:  filepath.Join(tmp, "trace-cache"),
	})
	if err != nil {
		t.Fatalf("ResolveWithOptions: %v", err)
	}

	mockDesk := NewMockDesktop()
	b, err := NewBackend(Options{
		Locations: loc,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
		Desktop:          mockDesk,
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()

	ctx := context.Background()

	// All business methods require login when no site has an active session, including local library access.
	_, err = b.OpenLink(ctx, "BV1xx411c7mD")
	if bilibili.KindOf(err) != bilibili.ErrKindAuthRequired {
		t.Fatalf("expected auth_required when logged out, got %v", err)
	}

	itemDir := filepath.Join(loc.DefaultCacheDir, "bilibili", "12345_测试UP主", "videos", "2024-03-09_测试离线视频_BV1offline123")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = os.WriteFile(filepath.Join(itemDir, "P01_part.mp4"), []byte("synthetic mp4"), 0o600)
	_, err = b.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Site:   "bilibili",
		Kind:   "video",
		ID:     "BV1offline123",
		Title:  "测试离线视频",
		PubTs:  1710000000,
		Author: library.ItemAuthor{Mid: 12345, Name: "测试UP主"},
		Parts:  []library.VideoPartRecord{{CID: 99, Page: 1, Title: "P1", File: "P01_part.mp4"}},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}
	offlineItems, err := b.ListLibraryItems(library.FeedFilter{})
	if bilibili.KindOf(err) != bilibili.ErrKindAuthRequired || len(offlineItems) != 0 {
		t.Fatalf("expected local library auth_required, got %d (err=%v)", len(offlineItems), err)
	}

	// 2. Start QR login, verify invalid manual Cookie is rejected BEFORE overwriting credential state,
	// then import valid manual Cookie -> QR session is superseded/canceled, manual Cookie active.
	qrSess, err := b.StartQRLogin(ctx)
	if err != nil || qrSess.FlowID == "" {
		t.Fatalf("StartQRLogin: %v", err)
	}
	b.CancelQRLogin(qrSess.FlowID)

	// DedeUserID mismatch (999999 != 778899 from nav) must fail and leave state logged_out!
	if _, badErr := b.ImportCookie(ctx, "SESSDATA=bad; DedeUserID=999999", ""); badErr == nil {
		t.Fatalf("expected ImportCookie with mismatched DedeUserID to fail")
	}
	if b.AccountStatus().LoggedIn {
		t.Fatalf("expected failed ImportCookie not to log in")
	}

	secretSess := "SUPER_SECRET_SESSDATA_VALUE_999"
	secretJct := "SUPER_SECRET_BILI_JCT_VALUE_888"
	secretRefresh := "SUPER_SECRET_REFRESH_TOKEN_777"
	st, err := b.ImportCookie(ctx, "SESSDATA="+secretSess+"; bili_jct="+secretJct+"; DedeUserID=778899; extra_track=ignored", secretRefresh)
	if err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}
	if !st.LoggedIn || st.Mid != 778899 {
		t.Fatalf("unexpected account status after ImportCookie: %+v", st)
	}
	if offlineItems, err := b.ListLibraryItems(library.FeedFilter{}); err != nil || len(offlineItems) != 1 {
		t.Fatalf("local library after login: %d items, err=%v", len(offlineItems), err)
	}

	// Failed subsequent ImportCookie must NOT overwrite existing valid credentials!
	if _, badErr := b.ImportCookie(ctx, "SESSDATA=bad2; DedeUserID=111111", ""); badErr == nil {
		t.Fatalf("expected ImportCookie with mismatched DedeUserID to fail")
	}
	if curSt := b.AccountStatus(); !curSt.LoggedIn || curSt.Mid != 778899 {
		t.Fatalf("expected failed ImportCookie to preserve existing credentials, got %+v", curSt)
	}

	// MediaPath resolves registered file and rejects path traversal / unregistered files.
	mp, err := b.MediaPath("BV1offline123", "P01_part.mp4")
	if err != nil || mp == "" {
		t.Fatalf("MediaPath registered file failed: %v", err)
	}
	if _, err := b.MediaPath("BV1offline123", "../outside.mp4"); err == nil {
		t.Fatalf("expected MediaPath to reject traversal")
	}

	// 3. OpenLink for video, user, dynamic, and live room; verify local parse-history thumbnail caching & deletion linkage.
	vidRes, err := b.OpenLink(ctx, "https://www.bilibili.com/video/BV1xx411c7mD?spm_id_from=333.999")
	if err != nil || vidRes.Video == nil || vidRes.Video.BVID != "BV1xx411c7mD" {
		t.Fatalf("OpenLink video failed: res=%+v err=%v", vidRes, err)
	}
	// OpenLink records the entry without waiting for images; the
	// thumbnails follow from FetchHistoryThumbs and keep the entry's place.
	histAfterVid := b.ListHistory()
	if len(histAfterVid) == 0 || histAfterVid[0].ID != "BV1xx411c7mD" || histAfterVid[0].AvatarThumb != "" || histAfterVid[0].CoverThumb != "" {
		t.Fatalf("expected OpenLink to record the entry before any thumbnail, got %+v", histAfterVid)
	}
	if vidRes.HistoryThumbs == nil || vidRes.HistoryThumbs.Kind != "video" || vidRes.HistoryThumbs.ID != "BV1xx411c7mD" {
		t.Fatalf("expected a deferred thumbnail job, got %+v", vidRes.HistoryThumbs)
	}
	if err := b.FetchHistoryThumbs(ctx, *vidRes.HistoryThumbs); err != nil {
		t.Fatalf("FetchHistoryThumbs: %v", err)
	}
	histAfterVid = b.ListHistory()
	if len(histAfterVid) == 0 || histAfterVid[0].AvatarThumb == "" || histAfterVid[0].CoverThumb == "" {
		t.Fatalf("expected FetchHistoryThumbs to cache AvatarThumb and CoverThumb in thumbs/, got %+v", histAfterVid)
	}
	avPath, err := b.HistoryThumbPath(histAfterVid[0].AvatarThumb)
	if err != nil || avPath == "" || strings.ContainsAny(histAfterVid[0].AvatarThumb, "?&=") {
		t.Fatalf("expected clean local AvatarThumb path, got name=%q path=%q err=%v", histAfterVid[0].AvatarThumb, avPath, err)
	}
	cvPath, err := b.HistoryThumbPath(histAfterVid[0].CoverThumb)
	if err != nil || cvPath == "" || strings.ContainsAny(histAfterVid[0].CoverThumb, "?&=") {
		t.Fatalf("expected clean local CoverThumb path, got name=%q path=%q err=%v", histAfterVid[0].CoverThumb, cvPath, err)
	}
	// Verify non-whitelisted external host is rejected for thumbnail caching.
	if badThumb := b.fetchHistoryThumb(ctx, "cover", "https://evil.example.invalid/cover.png"); len(badThumb.Data) != 0 {
		t.Fatal("expected non-whitelisted host thumbnail to be rejected")
	}
	userRes, err := b.OpenLink(ctx, "UID:12345")
	if err != nil || userRes.User == nil || userRes.User.Mid != 12345 {
		t.Fatalf("OpenLink user failed: res=%+v err=%v", userRes, err)
	}
	dynRes, err := b.OpenLink(ctx, "https://t.bilibili.com/987654321012345678")
	if err != nil || dynRes.Dynamic == nil || dynRes.Dynamic.ID != "987654321012345678" {
		t.Fatalf("OpenLink dynamic failed: res=%+v err=%v", dynRes, err)
	}
	liveRes, err := b.OpenLink(ctx, "https://live.bilibili.com/223344")
	if err != nil || liveRes.LiveRoom == nil || liveRes.LiveRoom.RoomID != 223344 {
		t.Fatalf("OpenLink live failed: res=%+v err=%v", liveRes, err)
	}

	// Verify MockDesktop records external URL and folder open actions without spawning OS processes (§6.4).
	if err := b.OpenLiveRoomInBrowser(223344); err != nil {
		t.Fatalf("OpenLiveRoomInBrowser: %v", err)
	}
	if err := b.OpenDataFolder("data"); err != nil {
		t.Fatalf("OpenDataFolder: %v", err)
	}
	if len(mockDesk.Actions()) != 2 {
		t.Fatalf("expected 2 recorded desktop actions, got %d", len(mockDesk.Actions()))
	}

	// 4. Verify diagnostics, RecentLogs(500), history.json, settings.json, ui-state.json, and trace.log never contain raw secret values.
	b.logInfo("attempting to log secret: " + secretSess + " and " + secretJct + " and " + secretRefresh)
	b.logInfo("attempting to log signed url: https://upos-sz-mirrorcos.bilivideo.com/upgcxcode/video.m4s?e=ig8euxZM2rNcNbdlhoNvNC8BqJIzNbfq9rVEuxTEnE8L5F6VnEsSTx0vkX8fqJeYTj_lta53NCM=&upsig=secret123")
	diagSnap := b.DiagnosticsSnapshot()
	if diagSnap.IndexStatus == "" || diagSnap.SubscriptionCheckStatus == "" || diagSnap.AutoCacheStatus == "" || diagSnap.LiveRecordStatus == "" {
		t.Fatalf("expected non-empty background status fields in DiagnosticsSnapshot, got %+v", diagSnap)
	}
	recentLogs, err := b.RecentLogs(500)
	if err != nil || len(recentLogs) == 0 {
		t.Fatalf("expected RecentLogs(500) to return sanitized lines, got len=%d err=%v", len(recentLogs), err)
	}
	for _, ln := range recentLogs {
		for _, sec := range []string{secretSess, secretJct, secretRefresh, "upsig="} {
			if strings.Contains(ln, sec) {
				t.Fatalf("RecentLogs leaked secret/signed query %q in line %q", sec, ln)
			}
		}
	}
	diagBytes, _ := json.Marshal(diagSnap)
	filesToCheck := []string{
		filepath.Join(loc.DataDir, "settings.json"),
		filepath.Join(loc.DataDir, "ui-state.json"),
		filepath.Join(loc.DataDir, "history.json"),
		filepath.Join(loc.LogsDir, "trace.log"),
	}
	for _, fp := range filesToCheck {
		if _, rErr := os.Stat(fp); rErr != nil {
			t.Fatalf("expected written file %s: %v", fp, rErr)
		}
	}
	// Scan every file TRACE wrote (databases, WAL, journals, manifests, logs,
	// and the encrypted credential), not only the named JSON files.
	scanned := 0
	for _, root := range []string{loc.DataDir, loc.LogsDir, loc.DefaultCacheDir} {
		_ = filepath.WalkDir(root, func(fp string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			raw, rErr := os.ReadFile(fp)
			if rErr != nil {
				return nil // locked SQLite files are covered by their WAL/journal
			}
			scanned++
			for _, sec := range []string{secretSess, secretJct, secretRefresh, "spm_id_from"} {
				if strings.Contains(string(raw), sec) {
					t.Fatalf("C37 violation: file %s leaked secret/tracking string %q", fp, sec)
				}
			}
			return nil
		})
	}
	t.Logf("C37 scanned %d files", scanned)
	if scanned < len(filesToCheck) {
		t.Fatalf("C37 scan covered only %d files", scanned)
	}
	for _, sec := range []string{secretSess, secretJct, secretRefresh} {
		if strings.Contains(string(diagBytes), sec) {
			t.Fatalf("C37 violation: DiagnosticsSnapshot leaked secret %q", sec)
		}
	}

	// Logout clears credential file.
	outSt, err := b.Logout()
	if err != nil || outSt.LoggedIn || outSt.State != "logged_out" {
		t.Fatalf("Logout failed: st=%+v err=%v", outSt, err)
	}
}

func TestServiceBackendContractsAndLogRedaction(t *testing.T) {
	TestServiceAuthIsolationOpenLinkAndRedaction(t)
}
