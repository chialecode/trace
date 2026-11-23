package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

// M7-K18: cache-queue.json v2 -> v3 migration creates byte-identical .premigrate-2 backup,
// preserves existing Bilibili/Douyin/Weibo tasks, persists X Base64 cursors safely,
// rejects newer schema (>3) with NewerDataError before creating DBs, and fails closed if backup is blocked.
func TestXCacheQueueSchemaV3MigrationBackupAndDowngradeGuard(t *testing.T) {
	loc := testLocations(t)
	if err := os.MkdirAll(loc.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	queuePath := filepath.Join(loc.DataDir, cacheQueueFileName)
	v2JSON := []byte(`{
  "version": 2,
  "tasks": [
    {
      "id": "cq_bili_v2",
      "site": "bilibili",
      "user": "778899",
      "userName": "SynthBili",
      "list": "videos",
      "cursor": "2",
      "hasMore": true,
      "status": "paused",
      "pauseReason": "user",
      "createdAt": "2026-11-20T00:00:00Z",
      "updatedAt": "2026-11-20T00:00:00Z"
    },
    {
      "id": "cq_wb_v2",
      "site": "weibo",
      "user": "5001234567",
      "userName": "SynthWeibo",
      "list": "posts",
      "cursor": "2:since_xyz_9",
      "hasMore": true,
      "status": "paused",
      "pauseReason": "user",
      "createdAt": "2026-11-20T00:00:00Z",
      "updatedAt": "2026-11-20T00:00:00Z"
    }
  ]
}
`)
	if err := os.WriteFile(queuePath, v2JSON, 0o600); err != nil {
		t.Fatal(err)
	}

	b1, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatalf("NewBackend migrating v2 cache-queue.json: %v", err)
	}
	tasks1 := queueRecordsForTest(b1)
	if len(tasks1) != 2 {
		_ = b1.Close()
		t.Fatalf("migrated tasks = %+v, want 2", tasks1)
	}

	// Verify .premigrate-2 backup is byte-identical and file is bumped to v3.
	backupPath := queuePath + ".premigrate-2"
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		_ = b1.Close()
		t.Fatalf("expected %s backup: %v", backupPath, err)
	}
	if !bytes.Equal(backupBytes, v2JSON) {
		_ = b1.Close()
		t.Fatal(".premigrate-2 content mismatch")
	}
	migratedRaw, err := os.ReadFile(queuePath)
	if err != nil {
		_ = b1.Close()
		t.Fatal(err)
	}
	var doc cacheQueueFile
	if err := json.Unmarshal(migratedRaw, &doc); err != nil {
		_ = b1.Close()
		t.Fatal(err)
	}
	if doc.Version != cacheQueueSchemaVersion || len(doc.Tasks) != 2 {
		_ = b1.Close()
		t.Fatalf("migrated doc = v%d (%d tasks), want v3 and 2 tasks", doc.Version, len(doc.Tasks))
	}

	// Persist an X queue record with a real-style Base64 Bottom cursor (+ / ==) AND a record with an unsafe URL cursor.
	b1.queue.mu.Lock()
	b1.queue.tasks = append(b1.queue.tasks,
		&cacheQueueEntry{
			rec: cacheQueueRecord{
				ID:        "cq_x_b64",
				Site:      "x",
				User:      synthXCreatorRestID,
				UserName:  synthXCreatorName,
				List:      "posts",
				Cursor:    "DAAHCgAB/+SynthCursor99==",
				HasMore:   true,
				Status:    CacheQueuePaused,
				CreatedAt: "2026-11-22T00:00:00Z",
				UpdatedAt: "2026-11-22T00:00:00Z",
			},
			rt: cacheQueueRuntime{seenKeys: map[string]bool{}},
		},
		&cacheQueueEntry{
			rec: cacheQueueRecord{
				ID:        "cq_x_unsafe_cur",
				Site:      "x",
				User:      synthXSelfRestID,
				UserName:  synthXSelfName,
				List:      "posts",
				Cursor:    "https://evil.example/?auth_token=LEAK",
				HasMore:   true,
				Status:    CacheQueuePaused,
				CreatedAt: "2026-11-22T00:00:00Z",
				UpdatedAt: "2026-11-22T00:00:00Z",
			},
			rt: cacheQueueRuntime{seenKeys: map[string]bool{}},
		},
	)
	_ = b1.queue.saveLocked()
	b1.queue.mu.Unlock()
	_ = b1.Close()

	// Re-open idempotently and check Base64 cursor survived while unsafe URL cursor was stripped.
	b2, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatalf("re-open v3 cache-queue.json: %v", err)
	}
	var gotB64, gotUnsafe *cacheQueueRecord
	for _, r := range queueRecordsForTest(b2) {
		cp := r
		switch r.ID {
		case "cq_x_b64":
			gotB64 = &cp
		case "cq_x_unsafe_cur":
			gotUnsafe = &cp
		}
	}
	_ = b2.Close()
	if gotB64 == nil || gotB64.Site != "x" || gotB64.List != "posts" || gotB64.Cursor != "DAAHCgAB/+SynthCursor99==" {
		t.Fatalf("reloaded X Base64 queue task = %+v", gotB64)
	}
	if gotUnsafe == nil || gotUnsafe.Cursor != "" {
		t.Fatalf("unsafe X cursor was not stripped: %+v", gotUnsafe)
	}
	if afterBackup, _ := os.ReadFile(backupPath); !bytes.Equal(afterBackup, v2JSON) {
		t.Fatal(".premigrate-2 was modified on subsequent reopen")
	}

	// Downgrade guard: v4 cache-queue.json rejected with NewerDataError before creating library.db or mutating file.
	locNewer := testLocations(t)
	if err := os.MkdirAll(locNewer.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	newerPath := filepath.Join(locNewer.DataDir, cacheQueueFileName)
	newerBytes := []byte(`{"version":4,"tasks":[]}`)
	if err := os.WriteFile(newerPath, newerBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBackend(Options{Locations: locNewer, Desktop: NewMockDesktop(), DisableBgWorkers: true}); err == nil {
		t.Fatal("NewBackend accepted cache-queue.json v4")
	} else {
		var nde *storage.NewerDataError
		if !errors.As(err, &nde) || nde.File != cacheQueueFileName || nde.Found != 4 || nde.Supported != 3 {
			t.Fatalf("err = %v, want *storage.NewerDataError(Found=4, Supported=3)", err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(locNewer.DataDir, "library.db")); !os.IsNotExist(statErr) {
		t.Fatal("library.db was created despite v4 cache-queue.json rejection")
	}
	if afterNewer, _ := os.ReadFile(newerPath); !bytes.Equal(afterNewer, newerBytes) {
		t.Fatal("v4 cache-queue.json was mutated on rejection")
	}

	// Fail closed when .premigrate-2 path is blocked by a directory.
	locBlocked := testLocations(t)
	if err := os.MkdirAll(filepath.Join(locBlocked.DataDir, cacheQueueFileName+".premigrate-2"), 0o700); err != nil {
		t.Fatal(err)
	}
	blockedPath := filepath.Join(locBlocked.DataDir, cacheQueueFileName)
	if err := os.WriteFile(blockedPath, v2JSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBackend(Options{Locations: locBlocked, Desktop: NewMockDesktop(), DisableBgWorkers: true}); err == nil {
		t.Fatal("NewBackend migrated despite blocked .premigrate-2 backup path")
	}
	if afterBlocked, _ := os.ReadFile(blockedPath); !bytes.Equal(afterBlocked, v2JSON) {
		t.Fatal("cache-queue.json was mutated despite blocked backup")
	}
}

// M7-K15: PreviewAccount, SchedulerStatuses, FollowingGroups/FollowingCreatorsPage/WithCursor/Import (0 extra reqs),
// full-cache queue (originals only, skips retweets & quotes, retries partial, stops cleanly at visible limit),
// periodic full-cache sweep, statuses_count in account_works, and live exclusion (0 reqs).
func TestXFullCacheQueueOriginalsOnlyVisibleLimitAndSubscriptionFlow(t *testing.T) {
	const (
		origID    = "1791257321273781001"
		rtID      = "1791257321273781002"
		quoteID   = "1791257321273781003"
		partialID = "1791257321273781004"
		doneID    = "1791257321273781005"
	)

	h := newXServiceTestHarness(t, func(opts *Options) {
		opts.DisableBgWorkers = false
	})
	h.loginX(t)
	ctx := context.Background()

	var followingCalls atomic.Int32
	var userTweetsCalls atomic.Int32
	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		vars := r.URL.Query().Get("variables")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+x.OpFollowing):
			followingCalls.Add(1)
			u1 := synthXUserCoreJSON("2000000000000000001", "follow_one", "Follow One", h.xSrv.URL+"/xcdn/f1.png", "Bio 1", 11)
			u2 := synthXUserCoreJSON("2000000000000000002", "follow_two", "Follow Two", h.xSrv.URL+"/xcdn/f2.png", "Bio 2", 22)
			u3 := synthXUserCoreJSON("2000000000000000003", "follow_three", "Follow Three", h.xSrv.URL+"/xcdn/f3.png", "Bio 3", 33)
			if !strings.Contains(vars, "CUR_FOL_PAGE2") {
				entries := strings.Join([]string{
					synthXUserEntry("user-1", u1),
					synthXUserEntry("user-2", u2),
					synthXCursorEntry("cursor-bottom-1", "Bottom", "CUR_FOL_PAGE2=="),
				}, ",")
				_, _ = w.Write([]byte(synthXFollowingEnvelope(entries)))
				return true
			}
			// Page 2 overlaps u2 (must be deduplicated!) and ends without cursor.
			entries := strings.Join([]string{
				synthXUserEntry("user-2", u2),
				synthXUserEntry("user-3", u3),
				synthXCursorEntry("cursor-bottom-2", "Bottom", "0|0"),
			}, ",")
			_, _ = w.Write([]byte(synthXFollowingEnvelope(entries)))
			return true

		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets):
			userTweetsCalls.Add(1)
			authorJSON := synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, h.xSrv.URL+"/xcdn/creator_avatar.png", "Creator bio", 3500)
			if !strings.Contains(vars, "CUR_UT_PAGE2") {
				// Page 1: 1 original, 1 retweet (skip), 1 quote (skip), 1 already-complete (skip), 1 partial (re-cache!)
				tOrig := fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 05:00:00 +0000 2024","full_text":"Original text post 1"}}`, origID, authorJSON, origID, synthXCreatorRestID)
				tRT := fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 04:50:00 +0000 2024","full_text":"RT @other: skip me","retweeted_status_result":{"result":{"__typename":"Tweet","rest_id":"1791257321273700001","core":{"user_results":{"result":%s}},"legacy":{"id_str":"1791257321273700001","user_id_str":"3000000000000000001","full_text":"Inner RT"}}}}}`, rtID, authorJSON, rtID, synthXCreatorRestID, synthXUserCoreJSON("3000000000000000001", "other_user", "Other", "", "", 5))
				tQuote := fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"quoted_status_result":{"result":{"__typename":"Tweet","rest_id":"1791257321273700002","core":{"user_results":{"result":%s}},"legacy":{"id_str":"1791257321273700002","user_id_str":"3000000000000000002","full_text":"Inner Quote"}}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 04:40:00 +0000 2024","full_text":"Quoting someone else","is_quote_status":true,"quoted_status_id_str":"1791257321273700002"}}`, quoteID, authorJSON, synthXUserCoreJSON("3000000000000000002", "other_q", "OtherQ", "", "", 5), quoteID, synthXCreatorRestID)
				tDone := fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 04:30:00 +0000 2024","full_text":"Already cached post"}}`, doneID, authorJSON, doneID, synthXCreatorRestID)
				tPartial := fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 04:20:00 +0000 2024","full_text":"Partial post to complete","extended_entities":{"media":[{"media_key":"3_1004","type":"photo","media_url_https":%q,"original_info":{"width":400,"height":400}}]}}}`, partialID, authorJSON, partialID, synthXCreatorRestID, h.xSrv.URL+"/xcdn/partial_fix.png")
				entries := strings.Join([]string{
					synthXTweetEntry("tweet-"+origID, tOrig),
					synthXTweetEntry("tweet-"+rtID, tRT),
					synthXTweetEntry("tweet-"+quoteID, tQuote),
					synthXTweetEntry("tweet-"+doneID, tDone),
					synthXTweetEntry("tweet-"+partialID, tPartial),
					synthXCursorEntry("cursor-bottom-1", "Bottom", "CUR_UT_PAGE2=="),
				}, ",")
				_, _ = w.Write([]byte(synthXUserTimelineEnvelope(entries)))
				return true
			}
			// Page 2: empty with repeated/zero cursor while author has statuses_count=3500 -> visible limit reached!
			_, _ = w.Write([]byte(synthXUserTimelineEnvelope(synthXCursorEntry("cursor-bottom-2", "Bottom", "CUR_UT_PAGE2=="))))
			return true

		case strings.HasSuffix(r.URL.Path, "/"+x.OpUserByRestId):
			_, _ = w.Write([]byte(synthXUserResultEnvelope(
				synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName,
				h.xSrv.URL+"/xcdn/creator_avatar.png", "Creator bio", 3500,
			)))
			return true
		}
		return false
	}
	h.mu.Unlock()

	// 1. PreviewAccount resolves X handle/link with rest_id + UniqueID=@handle.
	accRef, existingSub, err := h.backend.PreviewAccount(ctx, "https://x.com/"+synthXCreatorHandle)
	if err != nil || existingSub != nil || accRef.Site != "x" || accRef.UID != synthXCreatorRestID || accRef.UniqueID != synthXCreatorHandle {
		t.Fatalf("PreviewAccount(x) = %+v sub=%v err=%v", accRef, existingSub, err)
	}
	createdSub, err := h.backend.CreateSubscription(synthXCreatorName, "", []subscription.AccountRef{accRef}, subscription.SubRules{FullCache: true})
	if err != nil || createdSub.ID == "" {
		t.Fatalf("CreateSubscription(x): %+v err=%v", createdSub, err)
	}

	// 2. SchedulerStatuses includes "x" with DisplayName "X".
	sched := h.backend.SchedulerStatuses()
	if xSched, ok := sched["x"]; !ok || xSched.DisplayName != "X" {
		t.Fatalf("SchedulerStatuses[x] = %+v ok=%v", xSched, ok)
	}

	// 3. FollowingGroups("x") returns nil, nil; FollowingCreatorsPage & WithCursor deduplicate & import with 0 extra requests.
	if grps, err := h.backend.FollowingGroups(ctx, "x"); err != nil || len(grps) != 0 {
		t.Fatalf("FollowingGroups(x) = %+v err=%v, want nil, nil", grps, err)
	}
	// Pre-subscribe follow_one so page 1 marks it Subscribed=true.
	if _, err := h.backend.CreateSubscription("Follow One", "", []subscription.AccountRef{{Site: "x", UID: "2000000000000000001", UniqueID: "follow_one", Name: "Follow One"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}
	fp1, err := h.backend.FollowingCreatorsPageWithCursor(ctx, "x", "", "")
	if err != nil || len(fp1.Items) != 2 || !fp1.HasMore || fp1.NextCursor != "CUR_FOL_PAGE2==" || fp1.GroupsEnabled {
		t.Fatalf("FollowingCreatorsPageWithCursor page 1 = %+v err=%v", fp1, err)
	}
	if !fp1.Items[0].Subscribed || fp1.Items[1].Subscribed || fp1.Items[1].UniqueID != "follow_two" {
		t.Fatalf("page 1 subscribed/uniqueId = %+v", fp1.Items)
	}
	fp2, err := h.backend.FollowingCreatorsPageWithCursor(ctx, "x", "", fp1.NextCursor)
	if err != nil || len(fp2.Items) != 1 || fp2.Items[0].UID != "2000000000000000003" || fp2.HasMore {
		t.Fatalf("FollowingCreatorsPageWithCursor page 2 (deduped) = %+v err=%v", fp2, err)
	}
	// Also verify page-number-based FollowingCreatorsPage works seamlessly for UI callers.
	p1, err := h.backend.FollowingCreatorsPage(ctx, "x", "", 1)
	if err != nil || len(p1.Items) != 2 || !p1.HasMore || p1.NextPage != 2 {
		t.Fatalf("FollowingCreatorsPage(page=1) = %+v err=%v", p1, err)
	}
	p2, err := h.backend.FollowingCreatorsPage(ctx, "x", "", 2)
	if err != nil || len(p2.Items) != 1 || p2.Items[0].UID != "2000000000000000003" {
		t.Fatalf("FollowingCreatorsPage(page=2) = %+v err=%v", p2, err)
	}

	// Import selected creators with ZERO extra site HTTP requests and verify UniqueID is persisted.
	reqsBeforeImport := h.xReqs.Load()
	impRes, err := h.backend.ImportFollowingCreators("x", []FollowingCreatorDTO{fp1.Items[1], fp2.Items[0]}, subscription.SubRules{})
	if err != nil || impRes.Created != 2 {
		t.Fatalf("ImportFollowingCreators(x) = %+v err=%v", impRes, err)
	}
	if h.xReqs.Load() != reqsBeforeImport {
		t.Fatalf("ImportFollowingCreators issued %d extra HTTP requests, want 0", h.xReqs.Load()-reqsBeforeImport)
	}
	if _, acc, ok := h.backend.subs.GetAccount("x", "2000000000000000002"); !ok || acc.UniqueID != "follow_two" {
		t.Fatalf("imported X account missing UniqueID: %+v ok=%v", acc, ok)
	}

	// 4. Seed one already-complete X item (must be skipped) and one partial X item (must be retried).
	doneDir := filepath.Join(h.backend.GetSettings().CacheDir, "x", synthXCreatorRestID, "posts", doneID)
	_ = os.MkdirAll(doneDir, 0o700)
	if _, err := h.backend.LibraryStore().SaveItem(doneDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:" + doneID, Site: "x", Kind: "dynamic", Status: "complete",
		Title: "Already cached", Author: library.ItemAuthor{Site: "x", UID: synthXCreatorRestID, Name: synthXCreatorName},
	}); err != nil {
		t.Fatal(err)
	}
	partialDir := filepath.Join(h.backend.GetSettings().CacheDir, "x", synthXCreatorRestID, "posts", partialID)
	_ = os.MkdirAll(partialDir, 0o700)
	if _, err := h.backend.LibraryStore().SaveItem(partialDir, library.ItemMetadata{
		Version: 1, ID: "dynamic:" + partialID, Site: "x", Kind: "dynamic", Status: "partial",
		Title: "Partial post", Author: library.ItemAuthor{Site: "x", UID: synthXCreatorRestID, Name: synthXCreatorName},
		Missing: []library.MissingRecord{{Item: "media_01.jpg", Reason: "network"}},
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Enqueue X full-cache queue (via startDueFullCacheSweeps) and wait until completed.
	started := h.backend.startDueFullCacheSweeps(ctx, time.Now().UTC())
	if len(started) != 1 {
		t.Fatalf("startDueFullCacheSweeps(x) started %v, want 1", started)
	}
	var finalTask CacheQueueTask
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, qt := range h.backend.ListCacheQueue() {
			if qt.ID == started[0] {
				finalTask = qt
			}
		}
		if finalTask.Status == CacheQueueCompleted || finalTask.Status == CacheQueueStopped {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalTask.Status != CacheQueueCompleted {
		t.Fatalf("X queue final task = %+v, want completed", finalTask)
	}
	if finalTask.ListLabel != "定期全部缓存 · 帖子" {
		t.Fatalf("X queue ListLabel = %q, want 定期全部缓存 · 帖子", finalTask.ListLabel)
	}
	if finalTask.Note != x.VisibleTimelineLimitText {
		t.Fatalf("X queue Note = %q, want %q", finalTask.Note, x.VisibleTimelineLimitText)
	}
	// Cached origID + completed partialID = 2 cached; skipped rtID + quoteID + doneID = 3 skipped!
	if finalTask.CachedCount != 2 || finalTask.SkippedCount != 3 {
		t.Fatalf("X queue counts = cached:%d skipped:%d, want cached:2 skipped:3", finalTask.CachedCount, finalTask.SkippedCount)
	}
	if _, exists, _ := h.backend.GetLibraryItem("x:dynamic:" + rtID); exists {
		t.Fatal("full-cache queue must skip retweets")
	}
	if _, exists, _ := h.backend.GetLibraryItem("x:dynamic:" + quoteID); exists {
		t.Fatal("full-cache queue must skip quote posts")
	}
	if item, exists, _ := h.backend.GetLibraryItem("x:dynamic:" + partialID); !exists || item.Status != "complete" {
		t.Fatalf("partial X item not completed by full-cache queue: %+v exists=%v", item, exists)
	}

	// Verify statuses_count recorded in SubscriptionAccountWorks.
	works := h.backend.SubscriptionAccountWorks()
	if aw := works["x:"+synthXCreatorRestID]; aw.Total != 3500 || aw.Cached < 3 {
		t.Fatalf("SubscriptionAccountWorks[x:%s] = %+v, want Total=3500 Cached>=3", synthXCreatorRestID, aw)
	}

	// 6. Live exclusion: 0 requests for X live statuses & recording refusals.
	reqsBeforeLive := h.xReqs.Load()
	if liveStates, err := h.backend.LiveStatuses(ctx, []string{"x:" + synthXCreatorRestID}); err != nil || len(liveStates) != 0 {
		t.Fatalf("LiveStatuses(x) = %+v err=%v, want empty nil", liveStates, err)
	}
	if _, err := h.backend.StartLiveRecordingByAccount(ctx, "x:"+synthXCreatorRestID); !site.IsKind(err, site.ErrKindUnavailable) || !strings.Contains(err.Error(), live.XLiveUnsupportedMessage) {
		t.Fatalf("StartLiveRecordingByAccount(x) err = %v, want %q", err, live.XLiveUnsupportedMessage)
	}
	if _, err := h.backend.StartLiveRecordingByRoom(ctx, "x", "1RDGlSynth01"); !site.IsKind(err, site.ErrKindUnavailable) || !strings.Contains(err.Error(), live.XLiveUnsupportedMessage) {
		t.Fatalf("StartLiveRecordingByRoom(x) err = %v, want %q", err, live.XLiveUnsupportedMessage)
	}
	if h.xReqs.Load() != reqsBeforeLive {
		t.Fatalf("X live exclusion made %d requests, want 0", h.xReqs.Load()-reqsBeforeLive)
	}
	if err := h.backend.OpenLiveInBrowser("x", "https://x.com/i/spaces/1nAKEsynth02"); err != nil {
		t.Fatalf("OpenLiveInBrowser(x space): %v", err)
	}
}

// M7-K16: Mine › X (Following, Bookmarks, Likes) read-only, memory-only, flags off by default,
// pagination & dedup, rate-limit circuit breaker isolating ONLY X, logout cleanup, and 0 disk/log leakage.
func TestXMineReadOnlyMemoryOnlyPaginationFlagsAndBreaker(t *testing.T) {
	const (
		sentinelFollowBio = "MINE_X_SENTINEL_FOLLOW_BIO_991"
		sentinelBookmark  = "MINE_X_SENTINEL_BOOKMARK_TEXT_992"
		sentinelLike      = "MINE_X_SENTINEL_LIKE_TEXT_993"
	)

	h := newXServiceTestHarness(t, nil)
	ctx := context.Background()

	// 1. Logged-out X -> AccountSections returns single unavailable login item; AccountPage sends 0 requests.
	unloggedSecs := h.backend.AccountSections("x")
	if len(unloggedSecs) != 1 || unloggedSecs[0].Kind != AccountLogin || unloggedSecs[0].Available {
		t.Fatalf("unlogged AccountSections(x) = %+v", unloggedSecs)
	}
	if _, err := h.backend.AccountPage(ctx, "x", AccountBookmarks, "", ""); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("unlogged AccountPage(x) err = %v", err)
	}
	if h.xReqs.Load() != 0 {
		t.Fatalf("unlogged Mine › X sent %d requests, want 0", h.xReqs.Load())
	}

	h.loginX(t)
	// Also log in Bilibili so we can prove X breaker never blocks Bilibili.
	if _, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=SYNTH_BILI_MINE; DedeUserID=778899; bili_jct=SYNTH_JCT_MINE", ""); err != nil {
		t.Fatal(err)
	}

	// 2. Logged-in X sections: Following, Bookmarks, Likes only (Lists & BookmarkFolders hidden by default).
	secs := h.backend.AccountSections("x")
	if len(secs) != 3 || secs[0].Kind != AccountFollowing || secs[1].Kind != AccountBookmarks || secs[2].Kind != AccountLikes {
		t.Fatalf("logged-in AccountSections(x) = %+v", secs)
	}
	if folders, err := h.backend.AccountFolders(ctx, "x", AccountBookmarks); err != nil || len(folders) != 0 {
		t.Fatalf("AccountFolders(x, bookmarks) with flag off = %+v err=%v", folders, err)
	}
	if _, err := h.backend.AccountPage(ctx, "x", AccountLists, "", ""); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("AccountPage(x, lists) with flag off err=%v, want bad_request", err)
	}

	var trigger429 atomic.Bool
	resetEpoch := time.Now().UTC().Add(12 * time.Minute).Unix()
	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if trigger429.Load() {
			w.Header().Set("x-rate-limit-limit", "50")
			w.Header().Set("x-rate-limit-remaining", "0")
			w.Header().Set("x-rate-limit-reset", fmt.Sprintf("%d", resetEpoch))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
			return true
		}
		vars := r.URL.Query().Get("variables")
		w.Header().Set("Content-Type", "application/json")
		authorJSON := synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, h.xSrv.URL+"/xcdn/creator_avatar.png", sentinelFollowBio, 10)
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+x.OpFollowing):
			if !strings.Contains(vars, "PIPE_CUR|2") {
				entries := synthXUserEntry("user-1", authorJSON) + "," + synthXCursorEntry("cursor-b1", "Bottom", "PIPE_CUR|2")
				_, _ = w.Write([]byte(synthXFollowingEnvelope(entries)))
				return true
			}
			// Page 2 duplicates user-1 and adds user-2 to verify cross-page dedup & pipe-cursor preservation!
			u2 := synthXUserCoreJSON("2000000000000000022", "second_follow", "Second Follow", "", "Second bio", 5)
			entries := synthXUserEntry("user-1", authorJSON) + "," + synthXUserEntry("user-2", u2) + "," + synthXCursorEntry("cursor-b2", "Bottom", "0|0")
			_, _ = w.Write([]byte(synthXFollowingEnvelope(entries)))
			return true

		case strings.HasSuffix(r.URL.Path, "/"+x.OpBookmarks):
			t1 := fmt.Sprintf(`{"__typename":"Tweet","rest_id":"1791257321273782001","core":{"user_results":{"result":%s}},"legacy":{"id_str":"1791257321273782001","user_id_str":%q,"created_at":"Fri May 17 06:00:00 +0000 2024","full_text":%q}}`, authorJSON, synthXCreatorRestID, sentinelBookmark)
			_, _ = w.Write([]byte(synthXBookmarksEnvelope(synthXTweetEntry("tweet-b1", t1))))
			return true

		case strings.HasSuffix(r.URL.Path, "/"+x.OpLikes):
			t2 := fmt.Sprintf(`{"__typename":"Tweet","rest_id":"1791257321273782002","core":{"user_results":{"result":%s}},"legacy":{"id_str":"1791257321273782002","user_id_str":%q,"created_at":"Fri May 17 06:05:00 +0000 2024","full_text":%q}}`, authorJSON, synthXCreatorRestID, sentinelLike)
			_, _ = w.Write([]byte(synthXUserTimelineEnvelope(synthXTweetEntry("tweet-l1", t2))))
			return true
		}
		return false
	}
	h.mu.Unlock()

	// 3. Paginate Following (including pipe character inside X cursor), Bookmarks, Likes.
	f1, err := h.backend.AccountPage(ctx, "x", AccountFollowing, "", "")
	if err != nil || len(f1.Items) != 1 || !f1.HasMore || f1.Items[0].OpenURL != "https://x.com/i/user/"+synthXCreatorRestID {
		t.Fatalf("Mine Following page 1 = %+v err=%v", f1, err)
	}
	f2, err := h.backend.AccountPage(ctx, "x", AccountFollowing, "", f1.Cursor)
	if err != nil || len(f2.Items) != 1 || f2.Items[0].ID != "2000000000000000022" || f2.HasMore {
		t.Fatalf("Mine Following page 2 = %+v err=%v", f2, err)
	}
	bm, err := h.backend.AccountPage(ctx, "x", AccountBookmarks, "", "")
	if err != nil || len(bm.Items) != 1 || bm.Items[0].XPost == nil || bm.Items[0].CacheKey != "x:dynamic:1791257321273782001" {
		t.Fatalf("Mine Bookmarks = %+v err=%v", bm, err)
	}
	lk, err := h.backend.AccountPage(ctx, "x", AccountLikes, "", "")
	if err != nil || len(lk.Items) != 1 || lk.Items[0].XPost == nil || lk.Items[0].BrowserURL == "" {
		t.Fatalf("Mine Likes = %+v err=%v", lk, err)
	}

	// 4. Privacy sentinel scan: Mine › X items must NEVER be persisted anywhere on disk, history, library, or trace.log!
	if hist := h.backend.ListHistory(); len(hist) != 0 {
		t.Fatalf("Mine › X polluted recent parse history: %+v", hist)
	}
	scanTreeForSentinels(t, h.loc.DataDir, []string{sentinelFollowBio, sentinelBookmark, sentinelLike})
	scanTreeForSentinels(t, h.loc.LogsDir, []string{sentinelFollowBio, sentinelBookmark, sentinelLike})
	logs, _ := h.backend.RecentLogs(200)
	if !strings.Contains(strings.Join(logs, "\n"), "account.x.bookmarks page=1 items=1") {
		t.Fatalf("expected metadata-only account.x.bookmarks log line, got: %v", logs)
	}

	// 5. Rate-limit 429 trips ONLY X breaker with reset timestamp; subsequent X Mine call fails locally with 0 requests; Bilibili unaffected!
	trigger429.Store(true)
	_, rlErr := h.backend.AccountPage(ctx, "x", AccountBookmarks, "", "")
	if !site.IsKind(rlErr, site.ErrKindRateLimited) {
		t.Fatalf("expected rate_limited error on 429, got %v", rlErr)
	}
	if inBackoff, until := h.backend.subs.IsSiteInBackoff("x"); !inBackoff || until.Before(time.Now().UTC()) {
		t.Fatalf("X breaker not active after 429: inBackoff=%v until=%v", inBackoff, until)
	}
	if xSched := h.backend.SchedulerStatuses()["x"]; !xSched.BackingOff || !strings.Contains(xSched.BackoffSummary, "限流") {
		t.Fatalf("SchedulerStatuses[x] after 429 = %+v", xSched)
	}
	if inBiliBackoff, _ := h.backend.subs.IsSiteInBackoff("bilibili"); inBiliBackoff {
		t.Fatal("X rate-limit breaker leaked into Bilibili")
	}
	reqsBeforeBlocked := h.xReqs.Load()
	if _, err := h.backend.AccountPage(ctx, "x", AccountLikes, "", ""); !site.IsKind(err, site.ErrKindBackoff) {
		t.Fatalf("expected local backoff error for X Mine while breaker active, got %v", err)
	}
	if h.xReqs.Load() != reqsBeforeBlocked {
		t.Fatalf("blocked X Mine sent %d requests, want 0", h.xReqs.Load()-reqsBeforeBlocked)
	}

	// 6. Logout clears in-memory X Mine seen maps.
	if _, err := h.backend.LogoutSite("x"); err != nil {
		t.Fatal(err)
	}
	h.backend.xMemMu.Lock()
	seenLen := len(h.backend.accountXSeen)
	h.backend.xMemMu.Unlock()
	if seenLen != 0 {
		t.Fatalf("LogoutSite(x) left %d entries in accountXSeen", seenLen)
	}
}
