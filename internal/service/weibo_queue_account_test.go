package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

func fastTestQueuePacing() *CacheQueuePacing {
	return &CacheQueuePacing{
		ItemMinDelay:        time.Millisecond,
		ItemMaxDelay:        time.Millisecond,
		LongRestMinItems:    100,
		LongRestMaxItems:    100,
		LongRestMinDelay:    time.Millisecond,
		LongRestMaxDelay:    time.Millisecond,
		BackoffCoolMinDelay: time.Millisecond,
		BackoffCoolMaxDelay: time.Millisecond,
		StartupMinDelay:     time.Millisecond,
		StartupMaxDelay:     time.Millisecond,
	}
}

func queueRecordsForTest(b *Backend) []cacheQueueRecord {
	b.queue.mu.Lock()
	defer b.queue.mu.Unlock()
	out := make([]cacheQueueRecord, 0, len(b.queue.tasks))
	for _, e := range b.queue.tasks {
		out = append(out, e.rec)
	}
	return out
}

func waitQueueTerminal(t *testing.T, b *Backend, taskID string) CacheQueueTask {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, task := range b.ListCacheQueue() {
			if task.ID == taskID && (task.Status == CacheQueueCompleted || task.Status == CacheQueueStopped || task.Status == CacheQueuePaused) {
				return task
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache queue task %s did not finish; tasks=%+v", taskID, b.ListCacheQueue())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestCacheQueueSchemaV1ToV2MigrationBackupIdempotencyAndNewerRejection(t *testing.T) {
	// 1. v1 -> v2 migration with .premigrate-1 backup, preserving existing Bilibili/Douyin tasks,
	//    plus idempotent reload and Weibo task/cursor persistence.
	loc := testLocations(t)
	if err := os.MkdirAll(loc.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	queuePath := filepath.Join(loc.DataDir, cacheQueueFileName)
	v1JSON := []byte(`{
  "version": 1,
  "tasks": [
    {
      "id": "bili_task_1",
      "site": "bilibili",
      "user": "12345",
      "userName": "旧B站作者",
      "list": "videos",
      "cursor": "2",
      "hasMore": true,
      "status": "paused",
      "createdAt": "2026-11-01T00:00:00Z",
      "updatedAt": "2026-11-01T00:00:00Z"
    },
    {
      "id": "dy_task_1",
      "site": "douyin",
      "user": "MS4wLjABAAAA_synth_v1",
      "userName": "旧抖音作者",
      "list": "works",
      "cursor": "1710000000",
      "hasMore": true,
      "status": "paused",
      "createdAt": "2026-11-01T00:00:00Z",
      "updatedAt": "2026-11-01T00:00:00Z"
    }
  ]
}
`)
	if err := os.WriteFile(queuePath, v1JSON, 0o600); err != nil {
		t.Fatal(err)
	}

	b1, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatalf("NewBackend migrating v1 cache-queue.json: %v", err)
	}
	tasks1 := queueRecordsForTest(b1)
	_ = b1.Close()
	if len(tasks1) != 2 {
		t.Fatalf("migrated tasks = %+v, want 2 preserved tasks", tasks1)
	}

	backupPath := queuePath + ".premigrate-1"
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("expected %s backup: %v", backupPath, err)
	}
	if !bytes.Equal(backupBytes, v1JSON) {
		t.Fatalf(".premigrate-1 content mismatch")
	}
	migratedBytes, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc cacheQueueFile
	if err := json.Unmarshal(migratedBytes, &doc); err != nil {
		t.Fatalf("unmarshal migrated cache-queue.json: %v", err)
	}
	if doc.Version != cacheQueueSchemaVersion || len(doc.Tasks) != 2 {
		t.Fatalf("migrated doc = version %d tasks %d, want v%d and 2 tasks", doc.Version, len(doc.Tasks), cacheQueueSchemaVersion)
	}

	// Re-open must be idempotent: backup untouched, v2 preserved.
	b2, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatalf("re-open after migration: %v", err)
	}
	if got := queueRecordsForTest(b2); len(got) != 2 {
		_ = b2.Close()
		t.Fatalf("tasks after reopen = %+v", got)
	}
	// Persist a Weibo record with page:since_id cursor directly to verify reload keeps site=weibo & cursor.
	b2.queue.mu.Lock()
	b2.queue.tasks = append(b2.queue.tasks, &cacheQueueEntry{
		rec: cacheQueueRecord{
			ID:        "wb_task_1",
			Site:      "weibo",
			User:      synthWeiboUID,
			UserName:  synthWeiboName,
			List:      "posts",
			Cursor:    "2:since_abc_123",
			HasMore:   true,
			Status:    CacheQueuePaused,
			CreatedAt: "2026-11-20T00:00:00Z",
			UpdatedAt: "2026-11-20T00:00:00Z",
		},
		rt: cacheQueueRuntime{seenKeys: map[string]bool{}},
	})
	_ = b2.queue.saveLocked()
	b2.queue.mu.Unlock()
	_ = b2.Close()

	b3, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatalf("re-open with weibo task: %v", err)
	}
	var foundWeibo *cacheQueueRecord
	for _, tk := range queueRecordsForTest(b3) {
		if tk.ID == "wb_task_1" {
			cp := tk
			foundWeibo = &cp
			break
		}
	}
	_ = b3.Close()
	if foundWeibo == nil || foundWeibo.Site != "weibo" || foundWeibo.List != "posts" || foundWeibo.Cursor != "2:since_abc_123" {
		t.Fatalf("reloaded weibo queue task = %+v", foundWeibo)
	}
	backupAfter, _ := os.ReadFile(backupPath)
	if !bytes.Equal(backupAfter, v1JSON) {
		t.Fatal(".premigrate-1 was modified on subsequent reopens")
	}

	// 2. Newer schema version (> cacheQueueSchemaVersion) must be rejected with storage.NewerDataError before opening DBs or mutating disk.
	locNewer := testLocations(t)
	if err := os.MkdirAll(locNewer.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	newerPath := filepath.Join(locNewer.DataDir, cacheQueueFileName)
	futureVer := cacheQueueSchemaVersion + 1
	newerBytes := []byte(fmt.Sprintf(`{"version":%d,"tasks":[]}`, futureVer))
	if err := os.WriteFile(newerPath, newerBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBackend(Options{Locations: locNewer, Desktop: NewMockDesktop(), DisableBgWorkers: true}); err == nil {
		t.Fatalf("NewBackend accepted cache-queue.json version %d", futureVer)
	} else {
		var nde *storage.NewerDataError
		if !errors.As(err, &nde) || nde.File != cacheQueueFileName || nde.Found != futureVer || nde.Supported != cacheQueueSchemaVersion {
			t.Fatalf("err = %v, want *storage.NewerDataError for cache-queue.json v%d", err, futureVer)
		}
	}
	if _, statErr := os.Stat(filepath.Join(locNewer.DataDir, "library.db")); !os.IsNotExist(statErr) {
		t.Fatal("library.db was created despite newer cache-queue.json rejection")
	}
	afterNewer, _ := os.ReadFile(newerPath)
	if !bytes.Equal(afterNewer, newerBytes) {
		t.Fatal("newer cache-queue.json was mutated on rejection")
	}

	// 3. Fail closed if .premigrate-1 backup path is blocked by a directory.
	locBlocked := testLocations(t)
	if err := os.MkdirAll(filepath.Join(locBlocked.DataDir, cacheQueueFileName+".premigrate-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	blockedPath := filepath.Join(locBlocked.DataDir, cacheQueueFileName)
	if err := os.WriteFile(blockedPath, v1JSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBackend(Options{Locations: locBlocked, Desktop: NewMockDesktop(), DisableBgWorkers: true}); err == nil {
		t.Fatal("NewBackend migrated despite directory blocking .premigrate-1")
	}
	stillV1, _ := os.ReadFile(blockedPath)
	if !bytes.Equal(stillV1, v1JSON) {
		t.Fatal("v1 cache-queue.json was mutated when backup was blocked")
	}
}

func TestWeiboFullCacheQueueOriginalOnlyReuseBottomTipsAndSweep(t *testing.T) {
	h := newWeiboThreeSiteHarness(t, func(o *Options) {
		o.CacheQueuePacing = fastTestQueuePacing()
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	if err := h.backend.dl.Start(runCtx); err != nil {
		cancelRun()
		t.Fatalf("dl.Start: %v", err)
	}
	h.backend.queue.Start(runCtx)
	t.Cleanup(cancelRun)
	h.wbBottomTips.Store(true)
	ctx := context.Background()

	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	// Pre-populate synthWeiboMidHost as a partial (yellow-dot) item to verify full cache re-queues partial originals.
	partialDir := filepath.Join(h.loc.DefaultCacheDir, "weibo", synthWeiboUID+"_"+synthWeiboName, "posts", synthWeiboMidHost)
	if err := os.MkdirAll(partialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.backend.LibraryStore().SaveItem(partialDir, library.ItemMetadata{
		ID:          synthWeiboMidHost,
		Site:        "weibo",
		Kind:        "dynamic",
		DynamicType: "video",
		Title:       "带视频的原创微博",
		Author:      library.ItemAuthor{UID: synthWeiboUID, Name: synthWeiboName},
		Status:      "partial",
	}); err != nil {
		t.Fatalf("SaveItem partial: %v", err)
	}

	// Start Weibo full-cache queue (default FullCacheUseFeatureOriginal=false -> feature=0 + local original-only filter).
	enqTask, err := h.backend.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:     "weibo",
		User:     synthWeiboUID,
		UserName: synthWeiboName,
		List:     "posts",
	})
	if err != nil || enqTask.ID == "" {
		t.Fatalf("EnqueueCacheQueue(weibo): task=%+v err=%v", enqTask, err)
	}

	done := waitQueueTerminal(t, h.backend, enqTask.ID)
	if done.Status != CacheQueueCompleted {
		t.Fatalf("Weibo full cache queue ended with status=%s pause=%s stop=%s note=%s", done.Status, done.PauseReason, done.StopReason, done.Note)
	}
	// BottomTipsVisible stopped cleanly and recorded BottomTipsText in Note & StatusText.
	if !strings.Contains(done.Note, "半年") || !strings.Contains(done.StatusText, "半年") {
		t.Fatalf("expected BottomTipsText in Note/StatusText, got note=%q statusText=%q", done.Note, done.StatusText)
	}
	// Default flag uses feature=0.
	if feat, _ := h.wbLastFeature.Load().(string); feat != "0" {
		t.Fatalf("default feature param = %q, want 0", feat)
	}
	// Original posts (plain, long, host) must be cached; retweets & fast-retweets must be skipped!
	for _, mid := range []string{synthWeiboMidPlain, synthWeiboMidLong, synthWeiboMidHost} {
		it, ok, err := h.backend.LibraryStore().GetItem("weibo:dynamic:" + mid)
		if err != nil || !ok || it.Status != "complete" {
			t.Fatalf("expected original post %s cached complete, got ok=%v status=%q err=%v", mid, ok, it.Status, err)
		}
	}
	for _, mid := range []string{synthWeiboMidRT, synthWeiboMidFast} {
		if _, ok, _ := h.backend.LibraryStore().GetItem("weibo:dynamic:" + mid); ok {
			t.Fatalf("retweet/fast-retweet %s must NOT be cached by full cache", mid)
		}
	}
	// Plain post and host post were complete in mymblog list -> reused from memory cache without calling statuses/show!
	// Only longtext post (synthWeiboMidLong) needed statuses/show (+ longtext).
	if got := h.wbShowReqs.Load(); got != 1 {
		t.Fatalf("statuses/show calls = %d, want 1 (only truncated longtext post; complete list posts reused from memory)", got)
	}
	// Verify cache-queue.json on disk contains ZERO signed URLs or secrets.
	queueRaw, err := os.ReadFile(filepath.Join(h.loc.DataDir, cacheQueueFileName))
	if err != nil {
		t.Fatalf("read cache-queue.json: %v", err)
	}
	for _, forbidden := range []string{"ssig=", "Expires=", "KID=", "http://", "https://", synthWeiboSUB} {
		if bytes.Contains(queueRaw, []byte(forbidden)) {
			t.Fatalf("cache-queue.json leaked forbidden substring %q: %s", forbidden, queueRaw)
		}
	}

	// Verify SetWeiboFullCacheUseFeatureOriginal(true) passes feature=1 on a new sweep.
	h.backend.SetWeiboFullCacheUseFeatureOriginal(true)
	if err := h.backend.RemoveCacheQueue(enqTask.ID); err != nil {
		t.Fatalf("RemoveCacheQueue: %v", err)
	}
	// Create a full_cache subscription for synthWeiboUID and trigger startDueFullCacheSweeps.
	sub, err := h.backend.CreateSubscription(
		"微博全量订阅",
		"",
		[]subscription.AccountRef{{Site: "weibo", UID: synthWeiboUID, Name: synthWeiboName}},
		subscription.DefaultSubRules(),
	)
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if _, err := h.backend.SetSubscriptionTags(sub.ID, []string{subscription.TagFullCache}); err != nil {
		t.Fatalf("SetSubscriptionTags: %v", err)
	}

	started := h.backend.startDueFullCacheSweeps(ctx, time.Now())
	if len(started) != 1 {
		t.Fatalf("startDueFullCacheSweeps started %v, want 1 weibo sweep", started)
	}
	sweepDone := waitQueueTerminal(t, h.backend, started[0])
	if sweepDone.Site != "weibo" || sweepDone.List != "posts" || sweepDone.Origin != QueueOriginFullCache {
		t.Fatalf("periodic sweep task = %+v", sweepDone)
	}
	if feat, _ := h.wbLastFeature.Load().(string); feat != "1" {
		t.Fatalf("expected feature=1 after SetWeiboFullCacheUseFeatureOriginal(true), got %q", feat)
	}
	if aw := h.backend.SubscriptionAccountWorks()["weibo:"+synthWeiboUID]; aw.Total != 42 {
		t.Fatalf("periodic sweep did not record statuses_count: %+v", aw)
	}
}

func TestWeiboPreviewAccountFollowingImportAndLiveExclusion(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()

	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}
	if _, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=778899", ""); err != nil {
		t.Fatalf("LoginWithCookie(bilibili): %v", err)
	}

	// 1. PreviewAccount resolves /n/<nick> and custom domain to stable numeric UID.
	for _, rawInput := range []string{
		"https://weibo.com/n/" + url.PathEscape(synthWeiboName),
		"https://weibo.com/" + synthWeiboDomain,
		"https://weibo.com/u/" + synthWeiboUID,
	} {
		ref, _, err := h.backend.PreviewAccount(ctx, rawInput)
		if err != nil || ref.Site != "weibo" || ref.UID != synthWeiboUID {
			t.Fatalf("PreviewAccount(%q) = %+v err=%v, want site=weibo uid=%s", rawInput, ref, err, synthWeiboUID)
		}
	}

	// Subscribe synthWeiboUID first so FollowingCreatorsPage marks it Subscribed=true.
	if _, err := h.backend.CreateSubscription(
		"已订阅微博",
		"",
		[]subscription.AccountRef{{Site: "weibo", UID: synthWeiboUID, Name: synthWeiboName}},
		subscription.SubRules{},
	); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// 2. FollowingGroups returns nil with 0 requests when FollowGroupsEnabled is false.
	groupsBefore := h.wbGroupReqs.Load()
	groupsOff, err := h.backend.FollowingGroups(ctx, "weibo")
	if err != nil || len(groupsOff) != 0 || h.wbGroupReqs.Load() != groupsBefore {
		t.Fatalf("FollowingGroups(flag=false) = %+v err=%v reqs=%d", groupsOff, err, h.wbGroupReqs.Load()-groupsBefore)
	}

	// Temporarily enable FollowGroups flag to verify groups path.
	prevGroupsFlag := accountWeiboFollowGroupsEnabled
	accountWeiboFollowGroupsEnabled = true
	t.Cleanup(func() { accountWeiboFollowGroupsEnabled = prevGroupsFlag })
	groupsOn, err := h.backend.FollowingGroups(ctx, "weibo")
	if err != nil || len(groupsOn) != 1 || groupsOn[0].ID != "grp_1" {
		t.Fatalf("FollowingGroups(flag=true) = %+v err=%v", groupsOn, err)
	}
	accountWeiboFollowGroupsEnabled = prevGroupsFlag

	// 3. FollowingCreatorsPage pages without duplicates across page 1 & 2 and marks already-subscribed unselectable.
	p1, err := h.backend.FollowingCreatorsPage(ctx, "weibo", "", 1)
	if err != nil || len(p1.Items) != 2 || !p1.HasMore {
		t.Fatalf("FollowingCreatorsPage p1 = %+v err=%v", p1, err)
	}
	if !p1.Items[0].Subscribed || p1.Items[1].Subscribed {
		t.Fatalf("Subscribed flags wrong on p1: %+v", p1.Items)
	}
	p2, err := h.backend.FollowingCreatorsPage(ctx, "weibo", "", 2)
	if err != nil || len(p2.Items) != 1 || p2.Items[0].UID != "5100000004" {
		t.Fatalf("FollowingCreatorsPage p2 (deduped) = %+v err=%v", p2, err)
	}

	// 4. ImportFollowingCreators imports via ImportAccountRefs with 0 extra Weibo API calls!
	wbBeforeImport := h.wbReqs.Load()
	imp, err := h.backend.ImportFollowingCreators("weibo", []FollowingCreatorDTO{p1.Items[1], p2.Items[0]}, subscription.SubRules{})
	if err != nil || imp.Created != 2 {
		t.Fatalf("ImportFollowingCreators = %+v err=%v", imp, err)
	}
	if h.wbReqs.Load() != wbBeforeImport {
		t.Fatalf("ImportFollowingCreators sent %d extra HTTP requests, want 0", h.wbReqs.Load()-wbBeforeImport)
	}

	// Existing Bilibili RelationTags / RelationFollowings still work unchanged.
	tags, err := h.backend.RelationTags(ctx)
	if err != nil || len(tags) != 1 {
		t.Fatalf("RelationTags = %+v err=%v", tags, err)
	}
	if _, err := h.backend.RelationFollowings(ctx, 0, 1, 20); err != nil {
		t.Fatalf("RelationFollowings: %v", err)
	}

	// 5. Live exclusion: LiveStatuses skips weibo with 0 requests; recording rejects with unsupported message; OpenLiveInBrowser uses weibo.com.
	wbBeforeLive := h.wbReqs.Load()
	liveStates, err := h.backend.LiveStatuses(ctx, []string{"weibo:" + synthWeiboUID, "bilibili:12345"})
	if err != nil {
		t.Fatalf("LiveStatuses mixed: %v", err)
	}
	for _, ls := range liveStates {
		if ls.Site == "weibo" {
			t.Fatalf("LiveStatuses unexpectedly returned weibo state: %+v", ls)
		}
	}
	if _, err := h.backend.StartLiveRecordingByAccount(ctx, "weibo:"+synthWeiboUID); err == nil || !strings.Contains(err.Error(), "微博直播暂不支持录制") {
		t.Fatalf("StartLiveRecordingByAccount(weibo) err = %v", err)
	}
	if _, err := h.backend.StartLiveRecordingByRoom(ctx, "weibo", "1022:2321325090000001"); err == nil || !strings.Contains(err.Error(), "微博直播暂不支持录制") {
		t.Fatalf("StartLiveRecordingByRoom(weibo) err = %v", err)
	}
	if h.wbReqs.Load() != wbBeforeLive {
		t.Fatalf("Weibo live exclusion sent %d HTTP requests, want 0", h.wbReqs.Load()-wbBeforeLive)
	}

	if err := h.backend.OpenLiveInBrowser("weibo", "1022:2321325090000001"); err != nil {
		t.Fatalf("OpenLiveInBrowser(weibo): %v", err)
	}
	actions := h.desktop.Actions()
	if len(actions) == 0 || actions[len(actions)-1].Target != "https://weibo.com/l/wblive/p/show/1022:2321325090000001" {
		t.Fatalf("OpenLiveInBrowser(weibo) actions = %+v", actions)
	}
	if strings.Contains(actions[len(actions)-1].Target, "bilibili.com") {
		t.Fatal("OpenLiveInBrowser(weibo) fell back to bilibili.com")
	}
}

func TestMyWeiboAccountListsMemoryOnlyIsolationAndFeatureFlags(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()

	// Unlogged -> single unavailable login section.
	secsOut := h.backend.AccountSections("weibo")
	if len(secsOut) != 1 || secsOut[0].Kind != AccountLogin || secsOut[0].Available {
		t.Fatalf("unlogged AccountSections(weibo) = %+v", secsOut)
	}

	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}
	if _, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=778899", ""); err != nil {
		t.Fatalf("LoginWithCookie(bilibili): %v", err)
	}

	// Default feature flags: following + favorites only (no likes; no follow groups).
	if weibo.LikesEnabled || weibo.FollowGroupsEnabled {
		t.Fatal("expected Weibo likes and follow groups flags to default to false")
	}
	secs := h.backend.AccountSections("weibo")
	if len(secs) != 2 || secs[0].Kind != AccountFollowing || secs[1].Kind != AccountFavorites {
		t.Fatalf("AccountSections(weibo) default = %+v, want [following, favorites]", secs)
	}
	groupReqsBefore := h.wbGroupReqs.Load()
	foldersOff, err := h.backend.AccountFolders(ctx, "weibo", AccountFollowing)
	if err != nil || len(foldersOff) != 0 || h.wbGroupReqs.Load() != groupReqsBefore {
		t.Fatalf("AccountFolders(weibo, following) with flag off = %+v err=%v", foldersOff, err)
	}

	// Following page 1 & 2 deduplicate across pages and produce safe weibo.com links.
	fp1, err := h.backend.AccountPage(ctx, "weibo", AccountFollowing, "", "")
	if err != nil || len(fp1.Items) != 2 || !fp1.HasMore {
		t.Fatalf("AccountPage(weibo, following) p1 = %+v err=%v", fp1, err)
	}
	if fp1.Items[0].OpenURL != "https://weibo.com/u/"+synthWeiboUID {
		t.Fatalf("unexpected following OpenURL: %+v", fp1.Items[0])
	}
	fp2, err := h.backend.AccountPage(ctx, "weibo", AccountFollowing, "", fp1.Cursor)
	if err != nil || len(fp2.Items) != 1 || fp2.Items[0].ID != "5100000004" {
		t.Fatalf("AccountPage(weibo, following) p2 deduped = %+v err=%v", fp2, err)
	}

	// Favorites page 1 & 2 read data.status, deduplicate, and populate AccountItemPost.
	fav1, err := h.backend.AccountPage(ctx, "weibo", AccountFavorites, "", "")
	if err != nil || len(fav1.Items) != 1 || fav1.Items[0].Kind != AccountItemPost || fav1.Items[0].Post == nil {
		t.Fatalf("AccountPage(weibo, favorites) p1 = %+v err=%v", fav1, err)
	}
	fav2, err := h.backend.AccountPage(ctx, "weibo", AccountFavorites, "", fav1.Cursor)
	if err != nil || len(fav2.Items) != 1 || fav2.Items[0].ID != "5090000000000092" {
		t.Fatalf("AccountPage(weibo, favorites) p2 deduped = %+v err=%v", fav2, err)
	}

	// Temporarily enable Likes + FollowGroups flags to verify gated branches work when enabled.
	prevLikes, prevGroups := accountWeiboLikesEnabled, accountWeiboFollowGroupsEnabled
	accountWeiboLikesEnabled = true
	accountWeiboFollowGroupsEnabled = true
	t.Cleanup(func() {
		accountWeiboLikesEnabled = prevLikes
		accountWeiboFollowGroupsEnabled = prevGroups
	})
	secsOn := h.backend.AccountSections("weibo")
	if len(secsOn) != 3 || secsOn[2].Kind != AccountLikes {
		t.Fatalf("AccountSections(weibo) with likes enabled = %+v", secsOn)
	}
	foldersOn, err := h.backend.AccountFolders(ctx, "weibo", AccountFollowing)
	if err != nil || len(foldersOn) != 2 || foldersOn[0].ID != AccountFolderAll || foldersOn[1].ID != "grp_1" {
		t.Fatalf("AccountFolders(weibo, following) enabled = %+v err=%v", foldersOn, err)
	}
	likesPage, err := h.backend.AccountPage(ctx, "weibo", AccountLikes, "", "")
	if err != nil || len(likesPage.Items) != 1 || likesPage.Items[0].Kind != AccountItemPost {
		t.Fatalf("AccountPage(weibo, likes) = %+v err=%v", likesPage, err)
	}
	accountWeiboLikesEnabled = prevLikes
	accountWeiboFollowGroupsEnabled = prevGroups

	// Sentinel check: ZERO My-Weibo titles/usernames/group names written anywhere in DataDir, LogsDir, or CredentialsDir!
	mySentinels := []string{
		"SYNTH_FOLLOWED_ONE",
		"SYNTH_FOLLOWED_TWO",
		"SYNTH_FOLLOWED_THREE",
		"SYNTH_FAV_SECRET_TITLE_ONE",
		"SYNTH_FAV_SECRET_TITLE_TWO",
		"SYNTH_PRIVATE_GROUP_TITLE",
		"SYNTH_LIKE_SECRET_TITLE",
	}
	assertNoSentinelsInTree(t, h.loc.DataDir, mySentinels)
	assertNoSentinelsInTree(t, h.loc.LogsDir, mySentinels)
	assertNoSentinelsInTree(t, h.loc.CredentialsDir, mySentinels)

	// Weibo circuit-break/risk418 stops Weibo "我的" cleanly, puts Weibo into backoff (0 extra HTTP reqs),
	// while Bilibili "我的" keeps working!
	h.wbMode.Store("risk418")
	if _, err := h.backend.AccountPage(ctx, "weibo", AccountFollowing, "", ""); err == nil || site.KindOf(err) != site.ErrKindRiskControl {
		t.Fatalf("expected risk_control error when Weibo returns 418, got %v", err)
	}
	wbBeforeBackoff := h.wbReqs.Load()
	if _, err := h.backend.AccountPage(ctx, "weibo", AccountFollowing, "", ""); err == nil || site.KindOf(err) != site.ErrKindBackoff {
		t.Fatalf("follow-up Weibo AccountPage after 418 = %v, want ErrKindBackoff", err)
	}
	if h.wbReqs.Load() != wbBeforeBackoff {
		t.Fatalf("backoff-gated Weibo AccountPage sent %d extra HTTP requests, want 0", h.wbReqs.Load()-wbBeforeBackoff)
	}
	biliPage, err := h.backend.AccountPage(ctx, "bilibili", AccountFollowing, "", "")
	if err != nil || len(biliPage.Items) != 1 {
		t.Fatalf("Bilibili AccountPage affected by Weibo risk418: %+v err=%v", biliPage, err)
	}

	// Logout clears Weibo memory caches and reverts AccountSections("weibo") to unavailable login.
	h.wbMode.Store("ok")
	if _, err := h.backend.LogoutSite("weibo"); err != nil {
		t.Fatalf("LogoutSite(weibo): %v", err)
	}
	secsAfter := h.backend.AccountSections("weibo")
	if len(secsAfter) != 1 || secsAfter[0].Kind != AccountLogin || secsAfter[0].Available {
		t.Fatalf("AccountSections(weibo) after logout = %+v", secsAfter)
	}
	if _, err := h.backend.AccountPage(ctx, "weibo", AccountFavorites, "", ""); err == nil {
		t.Fatal("AccountPage(weibo) succeeded after logout")
	}
}

func TestWeiboMemorySeenMapsConcurrentAccessAndPostLogoutNoRepopulation(t *testing.T) {
	// 1. Concurrent access across AccountPage and FollowingCreatorsPage on the same bucket must not race.
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 4; i++ {
		wg.Add(3)
		go func(page int) {
			defer wg.Done()
			if _, err := h.backend.AccountPage(ctx, "weibo", AccountFollowing, "", ""); err != nil {
				errCh <- err
			}
		}(i)
		go func(page int) {
			defer wg.Done()
			if _, err := h.backend.AccountPage(ctx, "weibo", AccountFavorites, "", ""); err != nil {
				errCh <- err
			}
		}(i)
		go func(page int) {
			defer wg.Done()
			if _, err := h.backend.FollowingCreatorsPage(ctx, "weibo", "", 1); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Weibo page call failed: %v", err)
	}

	// 2. An in-flight request that started before LogoutSite("weibo") and completes after logout
	//    must NOT repopulate accountWeiboSeen or weiboFollowingSeen (generation guard).
	for _, tc := range []struct {
		name string
		path string
		call func(b *Backend)
	}{
		{
			name: "account_favorites",
			path: "/ajax/favorites/all_fav",
			call: func(b *Backend) { _, _ = b.AccountPage(ctx, "weibo", AccountFavorites, "", "") },
		},
		{
			name: "following_creators",
			path: "/ajax/friendships/friends",
			call: func(b *Backend) { _, _ = b.FollowingCreatorsPage(ctx, "weibo", "", 1) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inFlightEntered := make(chan struct{}, 1)
			releaseInFlight := make(chan struct{})
			h2 := newWeiboThreeSiteHarness(t, func(o *Options) {
				o.WeiboOpts.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
					return roundTripFunc(func(r *http.Request) (*http.Response, error) {
						if r.URL.Path == tc.path {
							select {
							case inFlightEntered <- struct{}{}:
							default:
							}
							<-releaseInFlight
						}
						return base.RoundTrip(r)
					})
				}
			})
			if _, err := h2.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
				t.Fatalf("LoginWithCookie: %v", err)
			}

			doneCh := make(chan struct{})
			go func() {
				defer close(doneCh)
				tc.call(h2.backend)
			}()

			select {
			case <-inFlightEntered:
			case <-time.After(5 * time.Second):
				close(releaseInFlight)
				t.Fatal("timed out waiting for in-flight Weibo request")
			}

			if _, err := h2.backend.LogoutSite("weibo"); err != nil {
				close(releaseInFlight)
				t.Fatalf("LogoutSite(weibo): %v", err)
			}
			close(releaseInFlight)
			<-doneCh

			h2.backend.weiboMemMu.Lock()
			accCount := len(h2.backend.accountWeiboSeen)
			folCount := len(h2.backend.weiboFollowingSeen)
			h2.backend.weiboMemMu.Unlock()
			if accCount != 0 || folCount != 0 {
				t.Fatalf("in-flight pre-logout %s repopulated cleared seen maps: accountWeiboSeen=%d weiboFollowingSeen=%d", tc.name, accCount, folCount)
			}
		})
	}

	// 3. Guard against nil weiboCred / empty selfUID in FollowingCreatorsPage -> ErrKindAuthRequired.
	h3 := newWeiboThreeSiteHarness(t)
	if _, err := h3.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(h3): %v", err)
	}
	origCred := h3.backend.weiboCred
	h3.backend.weiboCred = nil
	_, err := h3.backend.FollowingCreatorsPage(ctx, "weibo", "", 1)
	h3.backend.weiboCred = origCred
	if err == nil || site.KindOf(err) != site.ErrKindAuthRequired {
		t.Fatalf("FollowingCreatorsPage with nil weiboCred err = %v, want ErrKindAuthRequired", err)
	}
}

func TestWeiboFavoritesAccountPageMediaRetweetCacheStateAndPlanReuseGuard(t *testing.T) {
	favSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ok": 1,
			"data": {
				"total_number": 2,
				"status": [
					{
						"id": 77001,
						"status": {
							"idstr": "5090000000000101",
							"mblogid": "FavMedia1",
							"text_raw": "SYNTH_FAV_MEDIA_OUTER_POST",
							"user": {"idstr": "5100000011", "screen_name": "SYNTH_FAV_AUTHOR_A"},
							"pic_ids": ["synfavpic1", "synfavpic2"],
							"bmiddle_pic": "http://wx2.sinaimg.cn/bmiddle/synfavpic1.jpg"
						}
					},
					{
						"idstr": "5090000000000102",
						"mblogid": "FavMedia2",
						"text_raw": "SYNTH_FAV_RETWEET_COMMENT",
						"user": {"idstr": "5100000012", "screen_name": "SYNTH_FAV_AUTHOR_B"},
						"retweeted_status": {
							"idstr": "5090000000000202",
							"mblogid": "FavOrig02",
							"text_raw": "SYNTH_FAV_RETWEET_ORIGIN_TEXT",
							"user": {"idstr": "5100000022", "screen_name": "SYNTH_FAV_ORIGIN_AUTHOR"},
							"pic_ids": ["synrtfav1"],
							"page_info": {
								"object_type": "video",
								"object_id": "1034:5090000000000202",
								"page_title": "SYNTH_FAV_ORIGIN_VIDEO_TITLE",
								"page_pic": "http://wx3.sinaimg.cn/orj360/synrtvidcover.jpg",
								"media_info": {"duration": 52}
							}
						}
					}
				]
			}
		}`))
	}))
	defer favSrv.Close()

	h := newWeiboThreeSiteHarness(t, func(opts *Options) {
		opts.WeiboOpts.Endpoints.AllFavorites = favSrv.URL + "/ajax/favorites/all_fav"
	})
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	// Pre-populate item 1 in LibraryStore to verify mapWeiboAccountPostsPage populates CacheState.Dot.
	savedDir := filepath.Join(h.loc.DefaultCacheDir, "weibo", "5100000011_creator", "posts", "5090000000000101")
	if err := os.MkdirAll(savedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.backend.LibraryStore().SaveItem(savedDir, library.ItemMetadata{
		ID:     "5090000000000101",
		Site:   "weibo",
		Kind:   "dynamic",
		Title:  "已归档条目",
		Author: library.ItemAuthor{UID: "5100000011", Name: "本地作者"},
		Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	page, err := h.backend.AccountPage(ctx, "weibo", AccountFavorites, "", "")
	if err != nil {
		t.Fatalf("AccountPage(weibo, favorites): %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(page.Items))
	}

	// Item 1: wrapped status unwrapped, 2 synthesized /large/ images rewritten to /oslarge/ with /large/ fallback, CacheState=green.
	it1 := page.Items[0]
	if it1.ID != "5090000000000101" || it1.Post == nil || len(it1.Post.Images) != 2 || it1.Post.CacheState.Dot != "green" {
		t.Fatalf("unexpected it1: %+v post=%+v", it1, it1.Post)
	}
	if it1.Post.Images[0].ImageURL != "https://wx2.sinaimg.cn/oslarge/synfavpic1.jpg" ||
		len(it1.Post.Images[0].FallbackURLs) == 0 ||
		it1.Post.Images[0].FallbackURLs[0] != "https://wx2.sinaimg.cn/large/synfavpic1.jpg" ||
		it1.Cover != "https://wx2.sinaimg.cn/oslarge/synfavpic1.jpg" {
		t.Fatalf("it1 image/cover = (%q, fb=%v, cover=%q)", it1.Post.Images[0].ImageURL, it1.Post.Images[0].FallbackURLs, it1.Cover)
	}

	// Item 2: retweeted origin carries synthesized image AND abbreviated video cover/title/duration + OriginIncomplete=true.
	it2 := page.Items[1]
	if it2.Post == nil || !it2.Post.OriginIncomplete || it2.Post.Retweeted == nil {
		t.Fatalf("unexpected it2 retweet: %+v", it2.Post)
	}
	rt := it2.Post.Retweeted
	if len(rt.Images) != 1 || rt.Images[0].ImageURL != "https://wx1.sinaimg.cn/oslarge/synrtfav1.jpg" ||
		len(rt.Images[0].FallbackURLs) == 0 || rt.Images[0].FallbackURLs[0] != "https://wx1.sinaimg.cn/large/synrtfav1.jpg" {
		t.Fatalf("unexpected rt.Images: %+v", rt.Images)
	}
	if rt.Video == nil || rt.Video.Title != "SYNTH_FAV_ORIGIN_VIDEO_TITLE" || rt.Video.CoverURL != "https://wx3.sinaimg.cn/large/synrtvidcover.jpg" || rt.Video.DurationSec != 52 {
		t.Fatalf("unexpected rt.Video: %+v", rt.Video)
	}

	// Verify canReuseWeiboPostPlan rejects MediaIncomplete posts so download planning fetches statuses/show.
	incompletePost := &weibo.Post{
		Mid:             "5090000000000101",
		Author:          weibo.AuthorSummary{UID: "5100000011"},
		MediaIncomplete: true,
		Images:          []weibo.ImageAsset{{Index: 1, PicID: "synfavpic1", Kind: weibo.MediaKindImage, URL: "https://wx2.sinaimg.cn/large/synfavpic1.jpg"}},
	}
	if canReuseWeiboPostPlan(incompletePost) {
		t.Fatal("canReuseWeiboPostPlan accepted MediaIncomplete=true post")
	}

	// Privacy sentinel check: zero favorite post texts or screen names persisted to disk/logs.
	assertNoSentinelsInTree(t, h.loc.DataDir, []string{
		"SYNTH_FAV_MEDIA_OUTER_POST",
		"SYNTH_FAV_AUTHOR_A",
		"SYNTH_FAV_RETWEET_COMMENT",
		"SYNTH_FAV_RETWEET_ORIGIN_TEXT",
		"SYNTH_FAV_ORIGIN_AUTHOR",
		"SYNTH_FAV_ORIGIN_VIDEO_TITLE",
	})
	assertNoSentinelsInTree(t, h.loc.LogsDir, []string{
		"SYNTH_FAV_MEDIA_OUTER_POST",
		"SYNTH_FAV_RETWEET_ORIGIN_TEXT",
	})
}
