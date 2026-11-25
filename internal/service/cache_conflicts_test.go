package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
)

func writeVerifiedLocalFile(t *testing.T, dir, rel string, data []byte, role string, w, h, dur int) library.MediaFile {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return library.MediaFile{
		Role:     role,
		Path:     rel,
		Bytes:    int64(len(data)),
		SHA256:   hex.EncodeToString(sum[:]),
		Width:    w,
		Height:   h,
		Duration: dur,
	}
}

func TestCacheConflictsSchemaGuardCorruptRecoveryAndRemoteExclusion(t *testing.T) {
	// 1. version > 1 in cache-conflicts.json is refused by GuardDataSchemas without modifying the file.
	locNewer := testLocations(t)
	if err := os.MkdirAll(locNewer.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	newerPath := filepath.Join(locNewer.DataDir, "cache-conflicts.json")
	newerPayload := []byte(`{"version":99,"conflicts":[]}`)
	if err := os.WriteFile(newerPath, newerPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewBackend(Options{
		Locations:        locNewer,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	var ndErr *storage.NewerDataError
	if !errors.As(err, &ndErr) || ndErr.File != "cache-conflicts.json" || ndErr.Found != 99 {
		t.Fatalf("expected NewerDataError for cache-conflicts.json v99, got %v", err)
	}
	afterNewer, readErr := os.ReadFile(newerPath)
	if readErr != nil || string(afterNewer) != string(newerPayload) {
		t.Fatalf("v99 cache-conflicts.json was modified: %q (err=%v)", string(afterNewer), readErr)
	}

	// 2. Corrupt cache-conflicts.json is backed up and recovered cleanly on startup.
	locCorrupt := testLocations(t)
	if err := os.MkdirAll(locCorrupt.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locCorrupt.DataDir, "cache-conflicts.json"), []byte(`{"version":1,"conflicts":[broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	bCorrupt, err := NewBackend(Options{
		Locations:        locCorrupt,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("corrupt cache-conflicts.json failed to recover: %v", err)
	}
	defer bCorrupt.Close()
	if len(bCorrupt.DiagnosticsSnapshot().CorruptRecovered) == 0 {
		t.Fatal("expected corrupt backup recorded in DiagnosticsSnapshot().CorruptRecovered")
	}

	// 3. Interrupted "resolving" conflict recovers to "pending" on startup.
	locResolving := testLocations(t)
	if err := os.MkdirAll(locResolving.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seedResolving := `{"version":1,"conflicts":[{"id":"cf_test01","itemKey":"bilibili:video:BV1xx411c7mD","site":"bilibili","kind":"video","targetId":"BV1xx411c7mD","title":"测试视频","detectedAt":"2026-06-01T10:00:00Z","reasonKind":"part_changed","localSummary":"共 2P","remoteSummary":"共 1P","remoteFingerprint":"fp_seed01","state":"resolving"}]}`
	if err := os.WriteFile(filepath.Join(locResolving.DataDir, "cache-conflicts.json"), []byte(seedResolving), 0o600); err != nil {
		t.Fatal(err)
	}
	bRecover, err := NewBackend(Options{
		Locations:        locResolving,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bRecover.Close()
	list, err := bRecover.ListCacheConflicts()
	if err != nil || len(list) != 1 || list[0].State != ConflictStatePending || list[0].ReasonLabel != "分P变更" {
		t.Fatalf("recovered conflict mismatch: %+v (err=%v)", list, err)
	}

	// 4. ProfileRemote (trace-server) is 100% excluded from ConflictService.
	srv, _ := countingBili(t)
	bRemote, _ := remoteTestBackend(t, srv.URL)
	ctx := context.Background()
	if bRemote.PendingCacheConflictCount() != 0 {
		t.Fatalf("remote PendingCacheConflictCount = %d, want 0", bRemote.PendingCacheConflictCount())
	}
	if _, err := bRemote.ListCacheConflicts(); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("remote ListCacheConflicts = %v, want ErrRemoteUnsupported", err)
	}
	if err := bRemote.ResolveCacheConflict(ctx, "cf_1", ConflictKeepLocal); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("remote ResolveCacheConflict = %v, want ErrRemoteUnsupported", err)
	}
	if _, err := bRemote.ResolveAllCacheConflicts(ctx, ""); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("remote ResolveAllCacheConflicts = %v, want ErrRemoteUnsupported", err)
	}
}

func TestCacheConflictsPrivacySentinelTripwireAndIgnoredRevisions(t *testing.T) {
	clock := newFakeQueueClock(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clock)
	ctx := context.Background()

	const (
		secretSESS   = "SYNTH_CONFLICT_SESSDATA_SECRET_99"
		secretCSRF   = "SYNTH_CONFLICT_CSRF_SECRET_99"
		secretSigned = "SYNTH_SIGNED_QUERY_SSIG_88"
		secretAuth   = "SYNTH_X_AUTH_TOKEN_77"
	)
	if _, err := b.ImportCookie(ctx, "SESSDATA="+secretSESS+"; bili_jct="+secretCSRF+"; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}

	var changedEvents atomic.Int32
	unsub := b.SubscribeEvents(func(ev Event) {
		if ev.Type == EventCacheConflictsChanged {
			changedEvents.Add(1)
		}
	})
	defer unsub()

	// Trigger via download-layer SetConflictHandler tripwire with poisoned metadata.
	b.recordDownloadConflictNotice(download.ConflictNotice{
		ItemKey:           "weibo:dynamic:5090000000000999",
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          "5090000000000999",
		AuthorUID:         "5100000002",
		AuthorName:        "博主甲 https://f.video.weibocdn.com/a.mp4?ssig=" + secretSigned + "&Expires=123",
		Title:             "标题 https://video.twimg.com/ext_tw_video/1/pu/vid/a.mp4?tag=12 auth_token=" + secretAuth,
		Origin:            "manual",
		ReasonKind:        ConflictReasonMedia,
		LocalSummary:      "本地 2 张图 i0.hdslb.com/bfs/a.jpg?SESSDATA=" + secretSESS,
		RemoteSummary:     "源站 1 段视频 v3-web.douyinvod.com/v.mp4?a_bogus=abc&sessionid=xyz",
		RemoteFingerprint: "https://f.video.weibocdn.com/v2.mp4?ssig=" + secretSigned,
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      "weibo",
			UID:       "5100000088",
			Name:      "转发者 ?sub=leak&wbpsess=leak",
			DynamicID: "5090000000000998",
		},
	})

	if changedEvents.Load() != 1 {
		t.Fatalf("EventCacheConflictsChanged count = %d, want 1", changedEvents.Load())
	}
	if got := b.PendingCacheConflictCount(); got != 1 {
		t.Fatalf("PendingCacheConflictCount = %d, want 1", got)
	}

	list, err := b.ListCacheConflicts()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListCacheConflicts = %+v, %v", list, err)
	}
	conflictID := list[0].ID
	v2FP := list[0].RemoteFingerprint
	if v2FP == "" || strings.Contains(v2FP, "http") || strings.Contains(v2FP, secretSigned) {
		t.Fatalf("RemoteFingerprint was not scrubbed: %q", v2FP)
	}

	// Choose "保留本地" -> 0 network calls, conflict removed, ignoredRevisions persisted.
	if err := b.ResolveCacheConflict(ctx, conflictID, ConflictKeepLocal); err != nil {
		t.Fatalf("ResolveCacheConflict(keep_local): %v", err)
	}
	if b.PendingCacheConflictCount() != 0 {
		t.Fatalf("expected 0 pending conflicts after keep_local, got %d", b.PendingCacheConflictCount())
	}

	// Inspect cache-conflicts.json on disk for privacy sentinel compliance.
	conflictsPath := filepath.Join(b.Locations().DataDir, "cache-conflicts.json")
	rawBytes, err := os.ReadFile(conflictsPath)
	if err != nil {
		t.Fatalf("read cache-conflicts.json: %v", err)
	}
	rawStr := string(rawBytes)
	for _, forbidden := range []string{
		"http://", "https://", "?", "&", "=",
		"weibocdn.com", "twimg.com", "hdslb.com", "douyinvod.com",
		secretSESS, secretCSRF, secretSigned, secretAuth,
	} {
		if strings.Contains(rawStr, forbidden) {
			t.Fatalf("privacy leak in cache-conflicts.json: found %q in:\n%s", forbidden, rawStr)
		}
	}

	// Re-encountering the exact same V2 fingerprint (e.g. 7-day fullCache) must stay suppressed!
	eventsBeforeRepeat := changedEvents.Load()
	clock.Advance(7 * 24 * time.Hour)
	b.recordDownloadConflictNotice(download.ConflictNotice{
		ItemKey:           "weibo:dynamic:5090000000000999",
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          "5090000000000999",
		ReasonKind:        ConflictReasonMedia,
		LocalSummary:      "本地 2 张图",
		RemoteSummary:     "源站 1 段视频",
		RemoteFingerprint: "https://f.video.weibocdn.com/v2.mp4?ssig=" + secretSigned, // hashes to same v2FP
	})
	if b.PendingCacheConflictCount() != 0 || changedEvents.Load() != eventsBeforeRepeat {
		t.Fatalf("ignored V2 revision re-alerted! count=%d events=%d", b.PendingCacheConflictCount(), changedEvents.Load())
	}

	// A subsequent V3 edit with a new fingerprint MUST reopen the conflict!
	b.recordDownloadConflictNotice(download.ConflictNotice{
		ItemKey:           "weibo:dynamic:5090000000000999",
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          "5090000000000999",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "本地 2 张图",
		RemoteSummary:     "源站第 3 版重新编辑",
		RemoteFingerprint: "fp_v3_new_edit",
	})
	if b.PendingCacheConflictCount() != 1 || changedEvents.Load() != eventsBeforeRepeat+1 {
		t.Fatalf("V3 edit did not reopen conflict! count=%d events=%d", b.PendingCacheConflictCount(), changedEvents.Load())
	}
}

func TestDouyinWorkPlanCacheAndFourSiteListDiffZeroExtraRequests(t *testing.T) {
	clock := newFakeQueueClock(time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	thumbPNG := historyThumbnailPNG(t, 48, 48)
	mp4Bytes := synthWeiboMP4Bytes(t)

	var (
		dyDetailCalls atomic.Int32
		dyOrigCalls   atomic.Int32
	)
	const dySecUID = "MS4wLjABAAAA_synth_dy_creator_58"
	var dySrv *httptest.Server
	dySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/obj/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(thumbPNG)
			return
		case strings.HasPrefix(r.URL.Path, "/vod/"):
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "v.mp4", time.Time{}, bytes.NewReader(mp4Bytes))
			return
		case r.URL.Path == "/aweme/v1/play/":
			dyOrigCalls.Add(1)
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "orig.mp4", time.Time{}, bytes.NewReader(mp4Bytes))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"8801","sec_uid":%q,"nickname":"抖音测试号"}}`, dySecUID)
		case "/aweme/v1/web/aweme/detail/":
			dyDetailCalls.Add(1)
			http.Error(w, "unexpected detail call", http.StatusInternalServerError)
		case "/aweme/v1/web/aweme/post/":
			awemes := []map[string]any{
				// 1. Unchanged complete video already at "original" gear -> (a) 0-call skip
				{
					"aweme_id":    "7500000000000000001",
					"aweme_type":  0,
					"desc":        "不变视频",
					"create_time": 1720000100,
					"duration":    2000,
					"author":      map[string]any{"uid": "9901", "sec_uid": dySecUID, "nickname": "作者A"},
					"video": map[string]any{
						"width": 1080, "height": 1920, "duration": 2000,
						"play_addr": map[string]any{"uri": "v0200fg10000samevid001", "width": 1080, "height": 1920, "url_list": []string{dySrv.URL + "/vod/v1.mp4"}},
					},
				},
				// 2. Same URI video where local was 720p transcoded and OriginalURLs (原画) is now available -> (b) safe upgrade!
				{
					"aweme_id":    "7500000000000000002",
					"aweme_type":  0,
					"desc":        "可升级原画视频",
					"create_time": 1720000090,
					"duration":    2000,
					"author":      map[string]any{"uid": "9901", "sec_uid": dySecUID, "nickname": "作者A"},
					"video": map[string]any{
						"width": 1080, "height": 1920, "duration": 2000,
						"play_addr": map[string]any{"uri": "v0200fg10000upgrvid002", "width": 1080, "height": 1920, "url_list": []string{dySrv.URL + "/vod/v2.mp4"}},
					},
				},
				// 3. Paid / uncacheable remote work when local V1 is valid -> (c) skip keeping V1 untouched!
				{
					"aweme_id":    "7500000000000000003",
					"aweme_type":  0,
					"desc":        "变更为付费视频",
					"create_time": 1720000080,
					"duration":    2000,
					"author":      map[string]any{"uid": "9901", "sec_uid": dySecUID, "nickname": "作者A"},
					"charge_info": map[string]any{"is_charge_content": true, "has_paid": false},
					"video": map[string]any{
						"width": 1080, "height": 1920, "duration": 2000,
						"play_addr": map[string]any{"uri": "v0200fg10000paidvid003", "width": 1080, "height": 1920, "url_list": []string{dySrv.URL + "/vod/v3.mp4"}},
					},
				},
				// 4. V2 conflict: video URI replaced -> (d) record conflict with 0 detail calls, V1 untouched!
				{
					"aweme_id":    "7500000000000000004",
					"aweme_type":  0,
					"desc":        "视频源替换测试",
					"create_time": 1720000070,
					"duration":    2000,
					"author":      map[string]any{"uid": "9901", "sec_uid": dySecUID, "nickname": "作者A"},
					"video": map[string]any{
						"width": 1080, "height": 1920, "duration": 2000,
						"play_addr": map[string]any{"uri": "v0200fg10000newvidv204", "width": 1080, "height": 1920, "url_list": []string{dySrv.URL + "/vod/v4_v2.mp4"}},
					},
				},
				// 5. Uncached video -> plans directly from ListUserPosts (douyinWorkPlanCache + OriginalURLs) with 0 detail calls!
				{
					"aweme_id":    "7500000000000000005",
					"aweme_type":  0,
					"desc":        "全新原画视频",
					"create_time": 1720000060,
					"duration":    2000,
					"author":      map[string]any{"uid": "9901", "sec_uid": dySecUID, "nickname": "作者A"},
					"video": map[string]any{
						"width": 1080, "height": 1920, "duration": 2000,
						"play_addr": map[string]any{"uri": "v0200fg10000freshvid05", "width": 1080, "height": 1920, "url_list": []string{dySrv.URL + "/vod/v5.mp4"}},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 0,
				"has_more":    0,
				"max_cursor":  0,
				"aweme_list":  awemes,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer dySrv.Close()

	biliSrv, _ := countingBili(t)
	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: biliSrv.URL, PassportBase: biliSrv.URL, WWWBase: biliSrv.URL,
				SpaceBase: biliSrv.URL, DynamicBase: biliSrv.URL, LiveAPIBase: biliSrv.URL, LiveWebBase: biliSrv.URL,
			},
			PacingInterval: -1, AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf:  dySrv.URL + "/aweme/v1/web/user/profile/self/",
				ProfileOther: dySrv.URL + "/aweme/v1/web/user/profile/other/",
				AwemeDetail:  dySrv.URL + "/aweme/v1/web/aweme/detail/",
				AwemePost:    dySrv.URL + "/aweme/v1/web/aweme/post/",
				PlayOriginal: dySrv.URL + "/aweme/v1/play/",
			},
			MinInterval: -1, JitterMax: -1, AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		_ = b.Close()
	}()
	if err := b.dl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_58; ttwid=ttwid_58", ""); err != nil {
		t.Fatal(err)
	}

	// Seed 4 local V1 Douyin video items.
	seedDyVideo := func(id, desc, gear, mediaID string) string {
		dir := filepath.Join(b.lib.CacheDir(), "douyin", "dy_author", "works", id)
		v1Bytes := []byte("LOCAL_V1_PROTECTED_" + id)
		mf := writeVerifiedLocalFile(t, dir, "video.mp4", v1Bytes, "video", 1080, 1920, 2)
		meta := library.ItemMetadata{
			Version:              1,
			ID:                   "video:" + id,
			Site:                 "douyin",
			Kind:                 "video",
			Status:               "complete",
			Title:                desc,
			Desc:                 desc,
			Duration:             2,
			CacheStandardVersion: library.CurrentCacheStandardVersion,
			Author:               library.ItemAuthor{Site: "douyin", UID: dySecUID, Name: "作者A"},
			Media:                []library.MediaFile{mf},
			AssetIDs:             []string{mediaID},
			DouyinVideo: &library.DouyinVideoInfo{
				MediaID:     mediaID,
				File:        "video.mp4",
				Gear:        gear,
				QualityName: "1080P",
				Width:       1080,
				Height:      1920,
			},
		}
		if _, sErr := b.lib.SaveItem(dir, meta); sErr != nil {
			t.Fatal(sErr)
		}
		return filepath.Join(dir, "video.mp4")
	}

	seedDyVideo("7500000000000000001", "不变视频", "original", "v0200fg10000samevid001")
	seedDyVideo("7500000000000000002", "可升级原画视频", "normal_720_0", "v0200fg10000upgrvid002")
	v3Path := seedDyVideo("7500000000000000003", "变更为付费视频", "original", "v0200fg10000paidvid003")
	v4Path := seedDyVideo("7500000000000000004", "视频源替换测试", "original", "v0200fg10000oldvidv104")

	// Also verify archived snapshots (__v1-<ts>) are skipped unconditionally.
	archDir := filepath.Join(b.lib.CacheDir(), "douyin", "dy_author", "works", "7500000000000000004__v1-1720000000")
	archMF := writeVerifiedLocalFile(t, archDir, "video.mp4", []byte("ARCH_V1"), "video", 1080, 1920, 2)
	if _, err := b.lib.SaveItem(archDir, library.ItemMetadata{
		Version:    1,
		ID:         "video:7500000000000000004__v1-1720000000",
		Site:       "douyin",
		Kind:       "video",
		Status:     "complete",
		Title:      "旧版归档",
		ArchivedOf: "douyin:video:7500000000000000004",
		Author:     library.ItemAuthor{Site: "douyin", UID: dySecUID, Name: "作者A"},
		Media:      []library.MediaFile{archMF},
	}); err != nil {
		t.Fatal(err)
	}

	b.queue.Start(ctx)
	qTask, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:   "douyin",
		List:   "works",
		User:   dySecUID,
		Origin: QueueOriginFullCache,
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue douyin: %v", err)
	}

	// Item #2 enqueues original upgrade, waits 1 inter-item gap, skips #3, records conflict on #4, and caches #5!
	dur, _ := clock.WaitForPendingTimer(t, 20*time.Second)
	clock.Advance(dur)

	done := waitQueuePredicate(t, b, qTask.ID, "douyin fullCache sweep completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})
	if dyDetailCalls.Load() != 0 {
		t.Fatalf("expected 0 aweme/detail calls thanks to douyinWorkPlanCache + list diff, got %d", dyDetailCalls.Load())
	}
	if dyOrigCalls.Load() == 0 {
		t.Fatal("expected ListUserPosts OriginalURLs redirector to be invoked")
	}
	if done.CachedCount != 2 || done.SkippedCount != 3 {
		t.Fatalf("CachedCount=%d SkippedCount=%d, want 2 and 3", done.CachedCount, done.SkippedCount)
	}
	// Item #5 was cached directly at Gear == "original" from ListUserPosts with 0 detail calls!
	freshMeta, exists5, err := b.GetLibraryItem("douyin:video:7500000000000000005")
	if err != nil || !exists5 || freshMeta.DouyinVideo == nil || freshMeta.DouyinVideo.Gear != "original" {
		t.Fatalf("item #5 did not cache original stream from ListUserPosts: exists=%v video=%+v err=%v", exists5, freshMeta.DouyinVideo, err)
	}
	// Verify ConflictResolution propagation on WorkDownloadPlan when resolving conflicts.
	if cachedDet, ok := b.douyinWorkCache.get("7500000000000000005", b.preferredDouyinCodec()); !ok {
		t.Fatal("expected item #5 in douyinWorkPlanCache")
	} else {
		planWithRes, pErr := b.buildDouyinWorkPlanFromDetail(download.WithConflictResolution(ctx, ConflictKeepBoth), cachedDet, b.currentWorkPlanOptions())
		if pErr != nil || planWithRes.ConflictResolution != ConflictKeepBoth || planWithRes.MediaID != "v0200fg10000freshvid05" {
			t.Fatalf("unexpected plan conflict/media fields: %+v (err=%v)", planWithRes, pErr)
		}
	}
	// Items #3 (paid) and #4 (V2 conflict) must keep V1 bytes 100% untouched!
	if got3, _ := os.ReadFile(v3Path); string(got3) != "LOCAL_V1_PROTECTED_7500000000000000003" {
		t.Fatalf("degraded paid item #3 modified local V1: %q", string(got3))
	}
	if got4, _ := os.ReadFile(v4Path); string(got4) != "LOCAL_V1_PROTECTED_7500000000000000004" {
		t.Fatalf("conflicted item #4 modified local V1: %q", string(got4))
	}
	conflicts, err := b.ListCacheConflicts()
	if err != nil || len(conflicts) != 1 || conflicts[0].ItemKey != "douyin:video:7500000000000000004" || conflicts[0].ReasonKind != ConflictReasonMedia {
		t.Fatalf("expected 1 Douyin media conflict on item #4, got %+v (err=%v)", conflicts, err)
	}

	// Unit-check Weibo, X, and Bilibili dynamics 0-call list diff classification directly:
	// Weibo: unchanged -> skip; PicID changed -> conflict; EditCount + Text changed -> conflict; Unavailable -> skip.
	wbLocal := library.ItemMetadata{
		ItemKey:   "weibo:dynamic:5090000000000101",
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Text:      "原始微博正文",
		EditCount: 0,
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", MediaID: "005XpicV1", Image: "images/01.jpg"}},
	}
	wbSame := weibo.Post{
		Mid:    "5090000000000101",
		Author: weibo.AuthorSummary{UID: "51001", ScreenName: "微博作者"},
		Text:   "原始微博正文",
		Images: []weibo.ImageAsset{{Index: 1, PicID: "005XpicV1", Kind: weibo.MediaKindImage, URL: "https://wx1.sinaimg.cn/large/005XpicV1.jpg"}},
	}
	if dec := b.inspectWeiboCachedPost(wbSame, wbLocal, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("unchanged Weibo post action = %v, want inspectSkipNoOp", dec.action)
	}
	wbMediaV2 := wbSame
	wbMediaV2.Images = []weibo.ImageAsset{{Index: 1, PicID: "005XpicV2Replaced", Kind: weibo.MediaKindImage}}
	if dec := b.inspectWeiboCachedPost(wbMediaV2, wbLocal, QueueOriginFullCache); dec.action != inspectConflict || dec.conflict.ReasonKind != ConflictReasonMedia {
		t.Fatalf("Weibo PicID replacement = %+v, want media conflict", dec)
	}
	wbLiveUpgrade := wbSame
	wbLiveUpgrade.Images = []weibo.ImageAsset{{Index: 1, PicID: "005XpicV1", Kind: weibo.MediaKindLivePhoto, MotionURL: "https://video.weibo.com/live.mov"}}
	if dec := b.inspectWeiboCachedPost(wbLiveUpgrade, wbLocal, QueueOriginFullCache); dec.action != inspectEnqueue {
		t.Fatalf("Weibo LivePhoto enrichment = %v, want inspectEnqueue", dec.action)
	}
	wbDeleted := wbSame
	wbDeleted.Unavailable = true
	if dec := b.inspectWeiboCachedPost(wbDeleted, wbLocal, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("unavailable Weibo post = %v, want inspectSkipNoOp", dec.action)
	}

	// X: unchanged -> skip; EditTweetIDs V2 -> conflict; HLSOnly when local has MP4 -> skip; higher bitrate -> upgrade.
	xLocal := library.ItemMetadata{
		ItemKey: "x:dynamic:1800000000000000101",
		RawID:   "1800000000000000101",
		Site:    "x",
		Kind:    "dynamic",
		Status:  "complete",
		Text:    "Original X post",
		DouyinVideo: &library.DouyinVideoInfo{
			MediaID: "7_1800000000000000101",
			File:    "video.mp4",
			Width:   1280,
			Height:  720,
			Bitrate: 800000,
		},
	}
	xSame := x.Post{
		ID:           "1800000000000000101",
		Author:       x.AuthorSummary{RestID: "9001", ScreenName: "xauthor"},
		Text:         "Original X post",
		EditTweetIDs: []string{"1800000000000000101"},
		Media: []x.MediaItem{{
			Index: 1, MediaKey: "7_1800000000000000101", Kind: x.MediaKindVideo,
			Width: 1280, Height: 720, VideoBitrate: 800000, VideoURL: "https://video.twimg.com/v.mp4",
		}},
	}
	if dec := b.inspectXCachedPost(xSame, xLocal, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("unchanged X post = %v, want inspectSkipNoOp", dec.action)
	}
	xHigherBitrate := xSame
	xHigherBitrate.Media = []x.MediaItem{{
		Index: 1, MediaKey: "7_1800000000000000101", Kind: x.MediaKindVideo,
		Width: 1920, Height: 1080, VideoBitrate: 2176000, VideoURL: "https://video.twimg.com/v1080.mp4",
	}}
	if dec := b.inspectXCachedPost(xHigherBitrate, xLocal, QueueOriginFullCache); dec.action != inspectEnqueue {
		t.Fatalf("higher quality X MP4 = %v, want inspectEnqueue", dec.action)
	}
	xHLSDegraded := xSame
	xHLSDegraded.Media = []x.MediaItem{{
		Index: 1, MediaKey: "7_1800000000000000101", Kind: x.MediaKindVideo, HLSOnly: true,
	}}
	if dec := b.inspectXCachedPost(xHLSDegraded, xLocal, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("HLS-only degraded X post = %v, want inspectSkipNoOp", dec.action)
	}
	xEdited := xSame
	xEdited.Text = "Edited V2 X post text"
	xEdited.EditTweetIDs = []string{"1800000000000000101", "1800000000000000202"}
	if dec := b.inspectXCachedPost(xEdited, xLocal, QueueOriginFullCache); dec.action != inspectConflict || dec.conflict.ReasonKind != ConflictReasonText {
		t.Fatalf("edited X post = %+v, want text_edited conflict", dec)
	}

	// Bilibili dynamic: unchanged -> skip; LiveURL motion -> upgrade; deleted origin when orig.json exists -> skip; edited text -> conflict.
	dynLocal := library.ItemMetadata{
		ItemKey: "bilibili:dynamic:990001",
		Site:    "bilibili",
		Kind:    "dynamic",
		Status:  "complete",
		Title:   "动态标题",
		Text:    "动态正文",
		Media: []library.MediaFile{
			{Role: "snapshot", Path: "orig.json"},
			{Role: "image", Path: "images/01.jpg", Source: "https://i0.hdslb.com/bfs/new_dyn/abc.jpg"},
		},
	}
	dynSame := bilibili.DynamicItem{
		ID:     "990001",
		Title:  "动态标题",
		Text:   "动态正文",
		Origin: &bilibili.DynamicItem{ID: "880001"},
		Images: []bilibili.DynamicMedia{{URL: "https://i0.hdslb.com/bfs/new_dyn/abc.jpg?x-expires=1"}},
	}
	if dec := b.inspectBilibiliCachedDynamic(dynSame, dynLocal, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("unchanged Bilibili dynamic = %v, want inspectSkipNoOp", dec.action)
	}
	dynOriginGone := dynSame
	dynOriginGone.Origin = nil
	if dec := b.inspectBilibiliCachedDynamic(dynOriginGone, dynLocal, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("Bilibili dynamic with deleted origin = %v, want inspectSkipNoOp preserving orig.json", dec.action)
	}
	dynLiveMotion := dynSame
	dynLiveMotion.Images = []bilibili.DynamicMedia{{URL: "https://i0.hdslb.com/bfs/new_dyn/abc.jpg", LiveURL: "https://upos-sz-mirrorcos.bilivideo.com/live.mp4"}}
	if dec := b.inspectBilibiliCachedDynamic(dynLiveMotion, dynLocal, QueueOriginFullCache); dec.action != inspectEnqueue {
		t.Fatalf("Bilibili dynamic LiveURL motion = %v, want inspectEnqueue", dec.action)
	}
	dynImgReplaced := dynSame
	dynImgReplaced.Images = []bilibili.DynamicMedia{{URL: "https://i0.hdslb.com/bfs/new_dyn/replaced_v2.jpg"}}
	if dec := b.inspectBilibiliCachedDynamic(dynImgReplaced, dynLocal, QueueOriginFullCache); dec.action != inspectConflict || dec.conflict.ReasonKind != ConflictReasonMedia {
		t.Fatalf("Bilibili dynamic image replaced = %+v, want media conflict", dec)
	}
}

func TestBilibiliVideosThreeGateCooldownAndConflictResolution(t *testing.T) {
	nowStart := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	clock := newFakeQueueClock(nowStart)

	var (
		viewCalls    atomic.Int32
		playURLCalls atomic.Int32
		mu           sync.Mutex
		remoteMode   = map[string]string{
			"BV1gateB00001": "ceiling",
			"BV1gateC00002": "recent_recheck",
			"BV1probe00003": "noop_stamp",
			"BV1confl00004": "cid_changed",
		}
	)
	vBytes, err := download.BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}

	var biliSrv *httptest.Server
	biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/m4s/v2_video.m4s":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "v2_video.m4s", time.Time{}, bytes.NewReader(vBytes))
			return
		case "/m4s/v2_audio.m4s":
			w.Header().Set("Content-Type", "audio/mp4")
			http.ServeContent(w, r, "v2_audio.m4s", time.Time{}, bytes.NewReader(aBytes))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试登录用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/1001":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22synth_webid%22%7D</script></html>`)
		case "/x/space/wbi/arc/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"page": map[string]any{"pn": 1, "ps": 30, "count": 4},
					"list": map[string]any{
						"vlist": []map[string]any{
							{"bvid": "BV1gateB00001", "aid": 1, "title": "GateB上限跳过", "description": "简介", "length": "00:02", "created": nowStart.Add(-30 * 24 * time.Hour).Unix(), "mid": 1001, "author": "UP"},
							{"bvid": "BV1gateC00002", "aid": 2, "title": "GateC冷却跳过", "description": "简介", "length": "00:02", "created": nowStart.Add(-30 * 24 * time.Hour).Unix(), "mid": 1001, "author": "UP"},
							{"bvid": "BV1probe00003", "aid": 3, "title": "过冷却探测盖戳", "description": "简介", "length": "00:02", "created": nowStart.Add(-30 * 24 * time.Hour).Unix(), "mid": 1001, "author": "UP"},
							{"bvid": "BV1confl00004", "aid": 4, "title": "UP主重传换源标题更新", "description": "简介", "length": "00:02", "created": nowStart.Add(-30 * 24 * time.Hour).Unix(), "mid": 1001, "author": "UP"},
						},
					},
				},
			})
		case "/x/web-interface/wbi/view", "/x/web-interface/view":
			viewCalls.Add(1)
			bvid := r.URL.Query().Get("bvid")
			mu.Lock()
			mode := remoteMode[bvid]
			mu.Unlock()
			switch mode {
			case "deleted":
				_, _ = fmt.Fprint(w, `{"code":-404,"message":"啥都木有"}`)
			case "cid_changed", "v2_ready":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"bvid": bvid, "aid": 4, "title": "UP主重传换源标题更新", "desc": "简介",
						"pubdate": nowStart.Add(-30 * 24 * time.Hour).Unix(), "duration": 2,
						"owner": map[string]any{"mid": 1001, "name": "UP"},
						"pages": []map[string]any{
							{"cid": 99004, "page": 1, "part": "V2新分P", "duration": 2},
						},
					},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"bvid": bvid, "aid": 3, "title": "过冷却探测盖戳", "desc": "简介",
						"pubdate": nowStart.Add(-30 * 24 * time.Hour).Unix(), "duration": 2,
						"owner": map[string]any{"mid": 1001, "name": "UP"},
						"pages": []map[string]any{
							{"cid": 3001, "page": 1, "part": "P1", "duration": 2},
						},
					},
				})
			}
		case "/x/player/wbi/playurl":
			playURLCalls.Add(1)
			bvid := r.URL.Query().Get("bvid")
			mu.Lock()
			mode := remoteMode[bvid]
			mu.Unlock()
			if mode == "preview_only" {
				_, _ = fmt.Fprint(w, `{"code":0,"data":{"is_preview":true,"accept_quality":[80]}}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"quality":        80,
					"accept_format":  "hdflv2,flv,mp4",
					"accept_quality": []int{80},
					"dash": map[string]any{
						"duration": 2,
						"video": []map[string]any{
							{"id": 80, "baseUrl": biliSrv.URL + "/m4s/v2_video.m4s", "bandwidth": 1000000, "mimeType": "video/mp4", "codecs": "avc1.64001F", "width": 1920, "height": 1080, "frameRate": "30", "codecid": 7},
						},
						"audio": []map[string]any{
							{"id": 30280, "baseUrl": biliSrv.URL + "/m4s/v2_audio.m4s", "bandwidth": 192000, "mimeType": "audio/mp4", "codecs": "mp4a.40.2"},
						},
					},
				},
			})
		case "/x/player/wbi/v2":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"subtitle":{"subtitles":[]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer biliSrv.Close()

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: biliSrv.URL, PassportBase: biliSrv.URL, WWWBase: biliSrv.URL,
				SpaceBase: biliSrv.URL, DynamicBase: biliSrv.URL, LiveAPIBase: biliSrv.URL, LiveWebBase: biliSrv.URL,
			},
			PacingInterval: -1, AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		_ = b.Close()
	}()
	if err := b.dl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ImportCookie(ctx, "SESSDATA=synth_58; bili_jct=csrf_58; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	// Disable danmaku/subtitles/cover in settings so V2 test focuses cleanly on video stream.
	curSet := b.GetSettings()
	curSet.Download.SaveDanmaku = false
	curSet.Download.SaveSubtitles = false
	curSet.Download.SaveCover = false
	if _, err := b.UpdateSettings(curSet); err != nil {
		t.Fatal(err)
	}

	seedBiliVideo := func(bvid, title string, cid int64, qn, stdVer int, recheckedAt string) string {
		dir := filepath.Join(b.lib.CacheDir(), "bilibili", "1001", "videos", bvid)
		v1Bytes := []byte("BILI_V1_FILE_" + bvid)
		mf := writeVerifiedLocalFile(t, dir, "P01.mp4", v1Bytes, "video", 1920, 1080, 2)
		meta := library.ItemMetadata{
			Version:              1,
			ID:                   "video:" + bvid,
			Site:                 "bilibili",
			Kind:                 "video",
			Status:               "complete",
			Title:                title,
			Desc:                 "简介",
			PubTs:                nowStart.Add(-30 * 24 * time.Hour).Unix(),
			Duration:             2,
			CacheStandardVersion: stdVer,
			RecheckedAt:          recheckedAt,
			Author:               library.ItemAuthor{Site: "bilibili", Mid: 1001, UID: "1001", Name: "UP"},
			Media:                []library.MediaFile{mf},
			Parts: []library.VideoPartRecord{{
				CID:           cid,
				Page:          1,
				Title:         "P1",
				Duration:      2,
				File:          "P01.mp4",
				DanmakuState:  "skipped",
				SubtitleState: "none",
				Video:         library.PartVideoInfo{Quality: qn, QualityName: bilibili.QualityName(qn)},
				Audio:         library.PartAudioInfo{Quality: 30280, Name: "192K 高音质"},
				Available:     library.PartAvailableInfo{BestVideo: qn, BestAudio: 30280, ProbedAt: nowStart.Add(-10 * 24 * time.Hour).Format(time.RFC3339)},
			}},
		}
		if _, sErr := b.lib.SaveItem(dir, meta); sErr != nil {
			t.Fatal(sErr)
		}
		return filepath.Join(dir, "P01.mp4")
	}

	// 1) Gate B: at CurrentCacheStandardVersion + 4K ceiling (qn=120) -> 0 calls!
	seedBiliVideo("BV1gateB00001", "GateB上限跳过", 1001, 120, library.CurrentCacheStandardVersion, "")
	// 2) Gate C: old standard v1, but RecheckedAt 2 days ago (< 7d) -> 0 calls!
	seedBiliVideo("BV1gateC00002", "GateC冷却跳过", 2001, 80, 1, nowStart.Add(-2*24*time.Hour).Format(time.RFC3339))
	// 3) Expired Gate C (10 days ago): probes VideoDetail + PlayURL (paced!) and stamps RecheckedAt & CurrentCacheStandardVersion!
	seedBiliVideo("BV1probe00003", "过冷却探测盖戳", 3001, 80, 1, nowStart.Add(-10*24*time.Hour).Format(time.RFC3339))
	// 4) Gate A (list Title changed) + CID replaced (4001 -> 99004): probes VideoDetail and records part_changed conflict!
	v4File := seedBiliVideo("BV1confl00004", "旧标题V1", 4001, 80, library.CurrentCacheStandardVersion, nowStart.Add(-1*time.Hour).Format(time.RFC3339))

	b.queue.Start(ctx)
	qTask, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:   "bilibili",
		List:   "videos",
		User:   "1001",
		Origin: QueueOriginFullCache,
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue bilibili videos: %v", err)
	}

	// Item #1 and #2 skip with 0 timers; Item #3 probes and arms a 40s pacing timer before Item #4 probes!
	d1, _ := clock.WaitForPendingTimer(t, 20*time.Second)
	if d1 != 40*time.Second {
		t.Fatalf("expected 40s pacing timer after Bilibili probe #3, got %v", d1)
	}
	clock.Advance(d1)

	waitQueuePredicate(t, b, qTask.ID, "bilibili videos sweep completed", func(qt CacheQueueTask) bool {
		return qt.Status == CacheQueueCompleted
	})

	// Exactly 2 VideoDetail calls (#3 and #4) and 1 PlayURL call (#3; #4 aborted on CID conflict)!
	if viewCalls.Load() != 2 || playURLCalls.Load() != 1 {
		t.Fatalf("expected viewCalls=2 playURLCalls=1 (Gate B & Gate C skipped #1 & #2), got view=%d playurl=%d", viewCalls.Load(), playURLCalls.Load())
	}

	// Item #3 was stamped with RecheckedAt and CurrentCacheStandardVersion.
	probedMeta, _, err := b.GetLibraryItem("bilibili:video:BV1probe00003")
	if err != nil || probedMeta.CacheStandardVersion != library.CurrentCacheStandardVersion || probedMeta.RecheckedAt == "" {
		t.Fatalf("item #3 was not stamped after clean probe: %+v (err=%v)", probedMeta, err)
	}

	// Item #4 recorded a part_changed conflict and kept V1 untouched!
	conflicts, err := b.ListCacheConflicts()
	if err != nil || len(conflicts) != 1 || conflicts[0].ItemKey != "bilibili:video:BV1confl00004" || conflicts[0].ReasonKind != ConflictReasonParts {
		t.Fatalf("expected 1 part_changed conflict on BV1confl00004, got %+v (err=%v)", conflicts, err)
	}
	conflictID := conflicts[0].ID

	// Hard block test #1: user clicks "用新版替换" (use_remote), but remote is now preview-only -> ErrRemoteDegradedRefused, V1 untouched!
	mu.Lock()
	remoteMode["BV1confl00004"] = "preview_only"
	mu.Unlock()
	resErr := b.ResolveCacheConflict(ctx, conflictID, ConflictUseRemote)
	if !errors.Is(resErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on preview_only remote, got %v", resErr)
	}
	if v1Data, _ := os.ReadFile(v4File); string(v1Data) != "BILI_V1_FILE_BV1confl00004" {
		t.Fatalf("V1 file was modified despite ErrRemoteDegradedRefused! got %q", string(v1Data))
	}
	failedList, _ := b.ListCacheConflicts()
	if len(failedList) != 1 || failedList[0].State != ConflictStateFailed || !strings.HasSuffix(failedList[0].LastError, "（本地原文件未改动）") {
		t.Fatalf("conflict failure banner mismatch: %+v", failedList)
	}

	// Hard block test #2: remote is now deleted (-404) -> ErrRemoteDegradedRefused, V1 untouched!
	mu.Lock()
	remoteMode["BV1confl00004"] = "deleted"
	mu.Unlock()
	if err := b.ResolveCacheConflict(ctx, conflictID, ConflictKeepBoth); !errors.Is(err, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on deleted remote, got %v", err)
	}
	if v1Data, _ := os.ReadFile(v4File); string(v1Data) != "BILI_V1_FILE_BV1confl00004" {
		t.Fatalf("V1 file was modified on deleted remote! got %q", string(v1Data))
	}

	// Batch resolution test: seed a logged-out Douyin conflict and a busy Weibo conflict,
	// then resolve all with default choice ("keep_both"):
	// - logged-out Douyin -> SkippedAuth = 1
	// - busy Weibo -> SkippedBusy = 1
	// - valid Bilibili V2 -> Applied = 1
	b.recordDownloadConflictNotice(download.ConflictNotice{
		ItemKey:           "douyin:video:7500000000000999",
		Site:              "douyin",
		Kind:              "video",
		TargetID:          "7500000000000999",
		ReasonKind:        ConflictReasonMedia,
		LocalSummary:      "本地旧版",
		RemoteSummary:     "源站新版",
		RemoteFingerprint: "fp_dy_auth_skip",
	})
	b.recordDownloadConflictNotice(download.ConflictNotice{
		ItemKey:           "weibo:dynamic:5090000000000888",
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          "5090000000000888",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "本地旧版",
		RemoteSummary:     "源站新版",
		RemoteFingerprint: "fp_wb_busy_skip",
	})
	b.conflicts.mu.Lock()
	for i := range b.conflicts.records {
		if b.conflicts.records[i].ItemKey == "weibo:dynamic:5090000000000888" {
			b.conflicts.records[i].State = ConflictStateResolving
		}
	}
	b.conflicts.mu.Unlock()

	// Direct ResolveCacheConflict on logged-out Douyin must fail with ErrKindAuthRequired without changing state!
	conflictsAll, _ := b.ListCacheConflicts()
	for _, c := range conflictsAll {
		if c.Site == "douyin" {
			if authErr := b.ResolveCacheConflict(ctx, c.ID, ConflictKeepBoth); !site.IsKind(authErr, site.ErrKindAuthRequired) {
				t.Fatalf("expected auth_required on logged-out Douyin conflict, got %v", authErr)
			}
		}
	}

	mu.Lock()
	remoteMode["BV1confl00004"] = "v2_ready"
	mu.Unlock()

	batchRes, err := b.ResolveAllCacheConflicts(ctx, "")
	if err != nil {
		t.Fatalf("ResolveAllCacheConflicts: %v", err)
	}
	if batchRes.Applied != 1 || batchRes.SkippedAuth != 1 || batchRes.SkippedBusy != 1 || batchRes.Failed != 0 {
		t.Fatalf("unexpected ConflictBatchResult: %+v", batchRes)
	}
	// BV1confl00004 conflict was resolved and removed from cache-conflicts.json!
	v2Meta, ok, err := b.GetLibraryItem("bilibili:video:BV1confl00004")
	if err != nil || !ok || v2Meta.Title != "UP主重传换源标题更新" {
		t.Fatalf("BV1confl00004 resolution did not refresh manifest: ok=%v err=%v title=%q", ok, err, v2Meta.Title)
	}
	remaining, err := b.ListCacheConflicts()
	if err != nil || len(remaining) != 2 {
		t.Fatalf("expected 2 remaining skipped conflicts (auth + busy), got %+v (err=%v)", remaining, err)
	}
	for _, rem := range remaining {
		if rem.ItemKey == "bilibili:video:BV1confl00004" {
			t.Fatalf("resolved conflict BV1confl00004 still present: %+v", rem)
		}
	}
}

func TestLegacyInlineURLConflictNormalizationAndAutoResolution(t *testing.T) {
	const (
		cid    = "100808c9a1f5c18abbfc31cecd32f45a14ddc1"
		oriURL = "sinaweibo://pageinfo?containerid=" + cid + "&extparam=合成超话%23tabbar_follow%3D5012"
	)

	// 1. NormalizeConflictText treats legacy inline parenthesized URLs & newline vs space identically,
	// while still distinguishing substantive text edits.
	legacyText := "合成超话 (" + oriURL + ")\n我是正文第一句，附 参考文档 (https://example.com/guide?ref=1) 与 旧站 (http://example.org/p)。"
	cleanRemote := "合成超话 我是正文第一句，附 参考文档 与 旧站。"
	if gotL, gotR := download.NormalizeConflictText(legacyText), download.NormalizeConflictText(cleanRemote); gotL != gotR {
		t.Fatalf("NormalizeConflictText mismatch:\nlocal =%q\nremote=%q", gotL, gotR)
	}
	if gotL, gotR := download.NormalizeConflictText("#合成[超话]# 今日记录"), download.NormalizeConflictText("合成超话 今日记录"); gotL != gotR {
		t.Fatalf("supertopic markup NormalizeConflictText mismatch: %q vs %q", gotL, gotR)
	}
	if download.NormalizeConflictText(legacyText) == download.NormalizeConflictText("合成超话 我把正文改成了完全不同的内容。") {
		t.Fatal("NormalizeConflictText must not collapse genuinely edited text")
	}

	// 2. inspectWeiboCachedPost no longer triggers a false text conflict for legacy cached Weibo posts,
	// while a real text edit still reports text_edited.
	clock := newFakeQueueClock(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clock)

	wbLegacyMeta := library.ItemMetadata{
		ItemKey:   "weibo:dynamic:5090000000000201",
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Text:      legacyText,
		Desc:      legacyText,
		AssetIDs:  []string{"005XpicLeg01"},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", MediaID: "005XpicLeg01", Image: "images/01.jpg"}},
	}
	wbCleanRemote := weibo.Post{
		Mid:    "5090000000000201",
		Author: weibo.AuthorSummary{UID: "51001", ScreenName: "微博作者"},
		Text:   cleanRemote,
		Links: []site.TextLink{
			{Label: "合成超话", URL: "https://weibo.com/p/" + cid},
			{Label: "参考文档", URL: "https://example.com/guide?ref=1"},
		},
		Images: []weibo.ImageAsset{{Index: 1, PicID: "005XpicLeg01", Kind: weibo.MediaKindImage, URL: "https://wx1.sinaimg.cn/large/005XpicLeg01.jpg"}},
	}
	if dec := b.inspectWeiboCachedPost(wbCleanRemote, wbLegacyMeta, QueueOriginFullCache); dec.action != inspectSkipNoOp {
		t.Fatalf("legacy inline URL vs clean remote = %+v, want inspectSkipNoOp", dec)
	}

	wbEditedRemote := wbCleanRemote
	wbEditedRemote.Text = "合成超话 我把正文改成了完全不同的内容。"
	if dec := b.inspectWeiboCachedPost(wbEditedRemote, wbLegacyMeta, QueueOriginFullCache); dec.action != inspectConflict || dec.conflict.ReasonKind != ConflictReasonText {
		t.Fatalf("genuinely edited remote = %+v, want text_edited conflict", dec)
	}

	// 3. Existing pending false-positive conflict on disk auto-resolves on load/list,
	// migrating local item.json to clean Text + safe Links[] while preserving CachedAt and media untouched;
	// a genuinely edited pending conflict remains pending!
	imgBytes := []byte("ORIGINAL_LOCAL_IMAGE_BYTES_UNTOUCHED")
	itemDirFalse := filepath.Join(b.lib.CacheDir(), "weibo", "51001", "posts", "5090000000000202")
	imgMF := writeVerifiedLocalFile(t, itemDirFalse, "images/01.jpg", imgBytes, "image", 800, 600, 0)
	const origCachedAt = "2026-03-01T08:30:00Z"
	if _, err := b.lib.SaveItem(itemDirFalse, library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:5090000000000202",
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Title:     "合成超话 (" + oriURL + ")",
		Desc:      legacyText,
		Text:      legacyText,
		URL:       "https://weibo.com/51001/5090000000000202",
		PubTs:     1720001000,
		CachedAt:  origCachedAt,
		Author:    library.ItemAuthor{Site: "weibo", UID: "51001", Name: "微博作者"},
		Media:     []library.MediaFile{imgMF},
		AssetIDs:  []string{"005XpicLeg02"},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", MediaID: "005XpicLeg02", Image: "images/01.jpg", Width: 800, Height: 600}},
	}); err != nil {
		t.Fatalf("SaveItem(false positive): %v", err)
	}

	itemDirReal := filepath.Join(b.lib.CacheDir(), "weibo", "51001", "posts", "5090000000000203")
	imgMFReal := writeVerifiedLocalFile(t, itemDirReal, "images/01.jpg", imgBytes, "image", 800, 600, 0)
	if _, err := b.lib.SaveItem(itemDirReal, library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:5090000000000203",
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Title:     "真实编辑测试",
		Desc:      legacyText,
		Text:      legacyText,
		URL:       "https://weibo.com/51001/5090000000000203",
		PubTs:     1720001100,
		CachedAt:  origCachedAt,
		Author:    library.ItemAuthor{Site: "weibo", UID: "51001", Name: "微博作者"},
		Media:     []library.MediaFile{imgMFReal},
		AssetIDs:  []string{"005XpicReal03"},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", MediaID: "005XpicReal03", Image: "images/01.jpg", Width: 800, Height: 600}},
	}); err != nil {
		t.Fatalf("SaveItem(real edit): %v", err)
	}

	falseFP := download.WorkConflictFingerprint("weibo", "dynamic", "5090000000000202", []string{"005XpicLeg02"}, 0, 1, 0, cleanRemote)
	realFP := download.WorkConflictFingerprint("weibo", "dynamic", "5090000000000203", []string{"005XpicReal03"}, 0, 1, 0, "合成超话 真正被作者编辑过的全新正文")
	if _, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           "weibo:dynamic:5090000000000202",
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          "5090000000000202",
		Title:             "旧格式误报",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "共 1 项媒体",
		RemoteSummary:     "源站微博正文内容已修改",
		RemoteFingerprint: falseFP,
		State:             ConflictStatePending,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           "weibo:dynamic:5090000000000203",
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          "5090000000000203",
		Title:             "真实编辑冲突",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "共 1 项媒体",
		RemoteSummary:     "源站微博正文内容已修改",
		RemoteFingerprint: realFP,
		State:             ConflictStatePending,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := b.ListCacheConflicts()
	if err != nil {
		t.Fatalf("ListCacheConflicts: %v", err)
	}
	if len(list) != 1 || list[0].ItemKey != "weibo:dynamic:5090000000000203" {
		t.Fatalf("expected only genuine conflict to remain, got %+v", list)
	}
	if b.PendingCacheConflictCount() != 1 {
		t.Fatalf("PendingCacheConflictCount = %d, want 1", b.PendingCacheConflictCount())
	}

	// Verify auto-migrated item.json has clean text, safe Links[], preserved CachedAt, v1 schema, and untouched image file.
	migrated, ok, err := b.lib.GetItem("weibo:dynamic:5090000000000202")
	if err != nil || !ok {
		t.Fatalf("GetItem migrated failed: ok=%v err=%v", ok, err)
	}
	if migrated.Version != 1 || migrated.CachedAt != origCachedAt {
		t.Fatalf("migrated metadata altered version/cachedAt: v=%d cachedAt=%q", migrated.Version, migrated.CachedAt)
	}
	wantMigratedText := "合成超话\n我是正文第一句，附 参考文档 与 旧站。"
	if migrated.Text != wantMigratedText || migrated.Desc != wantMigratedText {
		t.Fatalf("migrated Text/Desc = (%q, %q), want %q", migrated.Text, migrated.Desc, wantMigratedText)
	}
	if strings.Contains(migrated.Title, "sinaweibo://") {
		t.Fatalf("migrated Title still contains sinaweibo://: %q", migrated.Title)
	}
	if len(migrated.Links) != 3 ||
		migrated.Links[0] != (library.ItemLink{Label: "合成超话", URL: "https://weibo.com/p/" + cid}) ||
		migrated.Links[1] != (library.ItemLink{Label: "参考文档", URL: "https://example.com/guide?ref=1"}) ||
		migrated.Links[2] != (library.ItemLink{Label: "旧站", URL: "http://example.org/p"}) {
		t.Fatalf("migrated Links = %+v", migrated.Links)
	}
	gotImg, rErr := os.ReadFile(filepath.Join(itemDirFalse, "images", "01.jpg"))
	if rErr != nil || string(gotImg) != string(imgBytes) {
		t.Fatalf("local image file was touched during legacy text migration: err=%v bytes=%q", rErr, string(gotImg))
	}

	// 4. [P2 regression] Parenthesized sinaweibo:// containing whitespace must NOT be stripped or swallow prose
	// in either NormalizeConflictText or extractLegacyInlineLinksFromText (strictly consistent with RewriteSafeSinaWeiboAppURL).
	withSpaceA := "标题 (sinaweibo://pageinfo?containerid=" + cid + " 附带真实说明甲)"
	withSpaceB := "标题 (sinaweibo://pageinfo?containerid=" + cid + " 附带真实说明乙)"
	if download.NormalizeConflictText(withSpaceA) == download.NormalizeConflictText(withSpaceB) {
		t.Fatalf("NormalizeConflictText must not swallow whitespace-containing parenthesized sinaweibo:// prose")
	}
	if cleanedSpace, linksSpace, changedSpace := extractLegacyInlineLinksFromText(withSpaceA, "weibo"); changedSpace || cleanedSpace != withSpaceA || len(linksSpace) != 0 {
		t.Fatalf("extractLegacyInlineLinksFromText must reject whitespace-containing sinaweibo://: changed=%v cleaned=%q links=%+v", changedSpace, cleanedSpace, linksSpace)
	}

	// 5. [P1 regression] Truncated list-page Weibo long-text (IsLongText=true, LongTextFetched=false) or
	// truncated-prefix long-text expansion MUST NOT auto-dismiss an existing pending text_edited conflict.
	const longMid = "5090000000000209"
	longDir := filepath.Join(b.lib.CacheDir(), "weibo", "51001", "posts", longMid)
	longLegacyText := "合成超话 (" + oriURL + ")\n旧版长微博前段内容...展开"
	savedLong, err := b.lib.SaveItem(longDir, library.ItemMetadata{
		Version:  1,
		ID:       "dynamic:" + longMid,
		Site:     "weibo",
		Kind:     "dynamic",
		Status:   "complete",
		Title:    "旧版长微博",
		Text:     longLegacyText,
		Desc:     longLegacyText,
		URL:      "https://weibo.com/51001/" + longMid,
		CachedAt: origCachedAt,
		Author:   library.ItemAuthor{Site: "weibo", UID: "51001", Name: "微博作者"},
	})
	if err != nil {
		t.Fatalf("SaveItem(long legacy): %v", err)
	}
	if _, _, err := b.conflicts.recordOrUpdate(cacheConflictRecord{
		ItemKey:           savedLong.ItemKey,
		Site:              "weibo",
		Kind:              "dynamic",
		TargetID:          longMid,
		Title:             "旧版长微博",
		ReasonKind:        ConflictReasonText,
		LocalSummary:      "纯文字微博",
		RemoteSummary:     "源站微博正文内容已修改",
		RemoteFingerprint: "fp_real_longtext_edit_209",
		State:             ConflictStatePending,
	}); err != nil {
		t.Fatal(err)
	}

	// 5a: Unfetched long text on list page (IsLongText=true, LongTextFetched=false) -> must keep pending conflict!
	_ = b.inspectWeiboCachedPost(weibo.Post{
		Mid:             longMid,
		Author:          weibo.AuthorSummary{UID: "51001", ScreenName: "微博作者"},
		Text:            "合成超话 旧版长微博前段内容...展开",
		IsLongText:      true,
		LongTextFetched: false,
	}, savedLong, QueueOriginFullCache)
	if !b.conflicts.hasPendingTextConflictForItem(savedLong.ItemKey) {
		t.Fatal("truncated unfetched Weibo long text must NOT auto-dismiss pending text_edited conflict")
	}

	// 5b: Long-text expansion where substantiveTextChanged is true (expanded text differs from truncated local) -> must keep pending conflict!
	_ = b.inspectWeiboCachedPost(weibo.Post{
		Mid:             longMid,
		Author:          weibo.AuthorSummary{UID: "51001", ScreenName: "微博作者"},
		Text:            "合成超话 旧版长微博前段内容以及大幅修订的后半段完整正文内容。",
		IsLongText:      true,
		LongTextFetched: true,
	}, savedLong, QueueOriginFullCache)
	if !b.conflicts.hasPendingTextConflictForItem(savedLong.ItemKey) {
		t.Fatal("long-text expansion with substantive text difference must NOT auto-dismiss pending text_edited conflict")
	}

	// 5c: Full remote text fetched AND exact normalized equivalence -> cleanly auto-resolves!
	_ = b.inspectWeiboCachedPost(weibo.Post{
		Mid:             longMid,
		Author:          weibo.AuthorSummary{UID: "51001", ScreenName: "微博作者"},
		Text:            "合成超话 旧版长微博前段内容...展开",
		IsLongText:      true,
		LongTextFetched: true,
		Links:           []site.TextLink{{Label: "合成超话", URL: "https://weibo.com/p/" + cid}},
	}, savedLong, QueueOriginFullCache)
	if b.conflicts.hasPendingTextConflictForItem(savedLong.ItemKey) {
		t.Fatal("full fetched remote text with exact normalized match should auto-resolve legacy inline-URL conflict")
	}
}
