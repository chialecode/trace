package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

type syntheticDouyinFetcher struct {
	mu      sync.Mutex
	calls   []string
	handler func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error)
}

func (f *syntheticDouyinFetcher) SiteID() site.ID {
	return site.Douyin
}

func (f *syntheticDouyinFetcher) FetchCreatorPosts(ctx context.Context, uid string, cursor string) (site.CreatorPostsPage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, uid+"@"+cursor)
	fn := f.handler
	f.mu.Unlock()
	if fn == nil {
		return site.CreatorPostsPage{}, nil
	}
	return fn(ctx, uid, cursor)
}

func (f *syntheticDouyinFetcher) callSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *syntheticDouyinFetcher) resetCalls() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func TestMultiSiteAccountPreviewLinkMoveOwnerAndWebRID(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mgr, err := NewManagerWithUpdatesDB(
		db,
		db,
		filepath.Join(tmp, "subscriptions.json"),
		nil,
		nil,
		bilibili.NewClient(bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: -1}),
		settings.Default(tmp).Subscription,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	secUID := "MS4wLjABAAAA__SyntheticCreator_01"
	signedFace := "https://p3-pc.douyinpic.com/aweme/100x100/avatar.jpeg?x-expires=1999999999&x-signature=SYNTH_SIG"

	normPreview, ownerBefore, err := mgr.PreviewResolvedAccount(AccountRef{
		Site:     "douyin",
		UID:      secUID,
		UniqueID: "synth_dy_01",
		WebRID:   "880011",
		Name:     "合成创作者",
		Face:     signedFace,
	})
	if err != nil {
		t.Fatalf("PreviewResolvedAccount unowned: %v", err)
	}
	if ownerBefore != nil {
		t.Fatalf("expected nil owner before creation, got %+v", ownerBefore)
	}
	if normPreview.UID != secUID || normPreview.Face != "" || normPreview.WebRID != "880011" || normPreview.UniqueID != "synth_dy_01" {
		t.Fatalf("unexpected normalized preview: %+v", normPreview)
	}

	// Reject whitespace or path traversal in Douyin sec_uid.
	for _, bad := range []string{" " + secUID, secUID + "\n", "MS4wLjABAAAA/../evil", "MS4wLjABAAAA:bad"} {
		if _, _, err := mgr.PreviewResolvedAccount(AccountRef{Site: "douyin", UID: bad}); err == nil {
			t.Fatalf("expected invalid sec_uid %q to be rejected", bad)
		}
	}

	subA, err := mgr.Create("跨站创作者A", "", []AccountRef{
		{Site: "bilibili", UID: "0094697", Name: "B站号"},
	}, SubRules{ScheduledCheck: true, AutoRecord: true})
	if err != nil {
		t.Fatalf("Create subA: %v", err)
	}
	subA, err = mgr.LinkAccount(subA.ID, normPreview)
	if err != nil {
		t.Fatalf("LinkAccount douyin: %v", err)
	}
	if len(subA.Accounts) != 2 {
		t.Fatalf("expected 2 accounts on subA, got %+v", subA.Accounts)
	}

	_, ownerAfter, err := mgr.PreviewResolvedAccount(AccountRef{
		Site:   "douyin",
		UID:    secUID,
		WebRID: "999999_changed",
	})
	if err != nil || ownerAfter == nil || ownerAfter.ID != subA.ID {
		t.Fatalf("ownership must match strictly by <site>:<uid> regardless of webRid, got owner=%+v err=%v", ownerAfter, err)
	}

	// Updating or clearing webRid and updating snapshot keep ownership intact.
	if err := mgr.UpdateAccountWebRID("douyin", secUID, "776655"); err != nil {
		t.Fatalf("UpdateAccountWebRID set: %v", err)
	}
	if err := mgr.UpdateAccountSnapshot("douyin", secUID, "新昵称", signedFace, "new_unique_01"); err != nil {
		t.Fatalf("UpdateAccountSnapshot: %v", err)
	}
	_, gotAcc, ok := mgr.GetAccount("douyin", secUID)
	if !ok || gotAcc.WebRID != "776655" || gotAcc.Name != "新昵称" || gotAcc.UniqueID != "new_unique_01" || gotAcc.Face != "" {
		t.Fatalf("unexpected account after webRid+snapshot update: ok=%v acc=%+v", ok, gotAcc)
	}
	if err := mgr.UpdateAccountWebRID("douyin", secUID, ""); err != nil {
		t.Fatalf("UpdateAccountWebRID clear: %v", err)
	}
	if _, clearedAcc, ok := mgr.GetAccount("douyin", secUID); !ok || clearedAcc.WebRID != "" {
		t.Fatalf("expected cleared webRid, got %+v", clearedAcc)
	}

	autoRec := mgr.ListAutoRecordAccounts("douyin")
	if len(autoRec) != 1 || autoRec[0].UID != secUID {
		t.Fatalf("expected 1 douyin autoRecord account, got %+v", autoRec)
	}

	// Move Douyin account to subB and unlink cleanly.
	subB, err := mgr.Create("创作者B", "", []AccountRef{
		{Site: "bilibili", UID: "2002", Name: "B2"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create subB: %v", err)
	}
	movedDest, err := mgr.Move("douyin", secUID, subB.ID)
	if err != nil {
		t.Fatalf("Move douyin account: %v", err)
	}
	if movedDest.ID != subB.ID {
		t.Fatalf("expected destination subB, got %s", movedDest.ID)
	}
	if owner, ok := mgr.OwnerOf("douyin", secUID); !ok || owner.ID != subB.ID {
		t.Fatalf("expected subB to own moved Douyin account, got ok=%v owner=%+v", ok, owner)
	}
	if _, err := mgr.Unlink(subB.ID, "douyin", secUID); err != nil {
		t.Fatalf("Unlink douyin: %v", err)
	}
	if _, ok := mgr.OwnerOf("douyin", secUID); ok {
		t.Fatalf("expected Douyin account unowned after Unlink")
	}
}

// Mixed-site baseline (0 update_item rows), Douyin auto-cache via shared rule,
// pinned old work not counted as new & not stopping early, Douyin risk_control isolating only Douyin
// while Bilibili proceeds, uncertain empty preserving baseline, unlogged skip note, and live hint piggyback.
func TestMixedSiteSubscriptionBaselinePinnedAutoCacheRiskIsolationAndUncertainEmpty(t *testing.T) {
	var biliReqs sync.Mutex
	var biliDynamics map[string][]map[string]any

	setBiliDynamics := func(m map[string][]map[string]any) {
		biliReqs.Lock()
		biliDynamics = m
		biliReqs.Unlock()
	}
	setBiliDynamics(map[string][]map[string]any{
		"1001": {
			{
				"id_str": "81001",
				"type":   "DYNAMIC_TYPE_AV",
				"modules": map[string]any{
					"module_author":  map[string]any{"mid": 1001, "name": "BiliCreator", "pub_ts": 1700001000},
					"module_dynamic": map[string]any{"desc": map[string]any{"text": "Bili baseline"}},
				},
			},
		},
	})

	var biliCallCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		biliReqs.Lock()
		biliCallCount++
		mid := r.URL.Query().Get("host_mid")
		items := biliDynamics[mid]
		biliReqs.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"has_more": false, "items": items},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	lib, err := library.Open(db, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	cred, _, err := credential.Open(filepath.Join(tmp, "bili.bin"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cred.SaveManual("SESSDATA=synth_bili_sess; bili_jct=synth_csrf", "rt")

	biliClient := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	biliClient.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	subCfg := settings.Default(tmp).Subscription
	subCfg.Enabled = true
	subCfg.IntervalMinutes = 30
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), lib, nil, biliClient, subCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetAccountInterval(0)

	now := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	dyAuth := true
	biliAuth := true
	mgr.SetSiteAuthCheck(func(s site.ID) bool {
		if s == site.Douyin {
			return dyAuth
		}
		return biliAuth
	})

	dyFetcher := &syntheticDouyinFetcher{}
	mgr.RegisterFetcher(dyFetcher)

	var autoCached []UpdateItem
	mgr.RegisterAutoCacheEnqueuer(site.Douyin, func(_ context.Context, item UpdateItem) (string, error) {
		autoCached = append(autoCached, item)
		return "dy-auto-" + item.DynID, nil
	})
	var biliAutoCached []UpdateItem
	mgr.RegisterAutoCacheEnqueuer(site.Bilibili, func(_ context.Context, item UpdateItem) (string, error) {
		biliAutoCached = append(biliAutoCached, item)
		return "bili-auto-" + item.DynID, nil
	})

	var liveHints []string
	mgr.SetOnLiveHint(func(acc AccountRef, roomID, webRID string) {
		liveHints = append(liveHints, acc.UID+":"+roomID+":"+webRID)
	})

	// Shared cache rule: auto-cache video + draw.
	sharedRule, err := mgr.SaveRule(CacheRule{
		Name:           "自动缓存视频与图文",
		ScheduledCheck: true,
		AutoCache:      true,
		Types:          []string{"video", "draw"},
	})
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	dyUID1 := "MS4wLjABAAAA__MixedCreator_A"
	dyUID2 := "MS4wLjABAAAA__SecondCreator_B"

	mixedSub, err := mgr.Create("混合创作者", "", []AccountRef{
		{Site: "bilibili", UID: "1001", Name: "BiliCreator"},
		{Site: "douyin", UID: dyUID1, Name: "DyCreatorA"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create mixedSub: %v", err)
	}
	now = time.Unix(1700001500, 0).UTC()
	if _, err := mgr.SetSubscriptionRule(mixedSub.ID, sharedRule.ID); err != nil {
		t.Fatalf("SetSubscriptionRule mixedSub: %v", err)
	}

	dySub2, err := mgr.Create("第二抖音号", "", []AccountRef{
		{Site: "douyin", UID: dyUID2, Name: "DyCreatorB"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create dySub2: %v", err)
	}
	if _, err := mgr.SetSubscriptionRule(dySub2.ID, sharedRule.ID); err != nil {
		t.Fatalf("SetSubscriptionRule dySub2: %v", err)
	}

	sentinel := "SYNTH_DY_SIGN_SENTINEL_9988"

	// Phase 1: First check establishes baseline on page 1 ONLY (newest non-pinned for dyUID1, all-pinned fallback for dyUID2)
	// with ZERO update_item rows and piggybacks page-1 live hint!
	dyFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		if cursor != "0" {
			t.Fatalf("baseline check must only request page 1 (cursor=0), got uid=%s cursor=%s", uid, cursor)
		}
		if uid == dyUID1 {
			return site.CreatorPostsPage{
				AuthorName: "DyCreatorA",
				AuthorFace: "https://p3.douyinpic.com/avatar.jpeg?a_bogus=" + sentinel,
				UniqueID:   "dy_a_id",
				LiveRoomID: "7300000000000000001",
				LiveWebRID: "665544",
				HasMore:    true,
				NextCursor: "1700001200000",
				Posts: []site.CreatorPost{
					{ID: "7100000000000000009", PubTs: 1700001800, Type: "video", IsTop: true, Title: "置顶作品"},
					{ID: "7100000000000000005", PubTs: 1700001200, Type: "video", IsTop: false, Title: "非置顶基线", CoverURL: "https://p3.douyinpic.com/c.jpeg?x-signature=" + sentinel},
				},
			}, nil
		}
		return site.CreatorPostsPage{
			AuthorName: "DyCreatorB",
			HasMore:    false,
			Posts: []site.CreatorPost{
				{ID: "7200000000000000001", PubTs: 1700001100, Type: "draw", IsTop: true, Title: "全置顶账号基线"},
			},
		}, nil
	}

	ctx := context.Background()
	sum1, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 1 CheckNow: %v", err)
	}
	if sum1.BaselineInitialized != 3 || sum1.AutoCacheTasks != 0 {
		t.Fatalf("expected 3 baselines initialized and 0 auto-cache tasks, got %+v", sum1)
	}
	var rawItemCount int
	if err := updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item`).Scan(&rawItemCount); err != nil || rawItemCount != 0 {
		t.Fatalf("INV-12 violated: expected 0 rows in update_item after baseline, got %d (err=%v)", rawItemCount, err)
	}
	stA, ok, _ := mgr.GetAccountState("douyin", dyUID1)
	if !ok || stA.NewestID != "7100000000000000005" || stA.NewestTs != 1700001200 {
		t.Fatalf("expected dyUID1 baseline from newest non-pinned post 7100000000000000005@1700001200, got %+v", stA)
	}
	stB, ok, _ := mgr.GetAccountState("douyin", dyUID2)
	if !ok || stB.NewestID != "7200000000000000001" || stB.NewestTs != 1700001100 {
		t.Fatalf("expected dyUID2 all-pinned baseline 7200000000000000001@1700001100, got %+v", stB)
	}
	if !slices.Contains(liveHints, dyUID1+":7300000000000000001:665544") {
		t.Fatalf("expected page-1 live hint piggyback with 0 extra requests, got %v", liveHints)
	}
	if _, accSnap, ok := mgr.GetAccount("douyin", dyUID1); !ok || accSnap.WebRID != "665544" || accSnap.UniqueID != "dy_a_id" {
		t.Fatalf("expected page-1 webRid & uniqueId backfilled onto account, got %+v", accSnap)
	}

	// Simulate full process restart: reopen a brand-new Manager on the same SQLite handles & subscriptions.json.
	reopenManager := func() *Manager {
		t.Helper()
		m2, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), lib, nil, biliClient, subCfg, nil)
		if err != nil {
			t.Fatalf("reopen Manager: %v", err)
		}
		m2.SetAccountInterval(0)
		m2.SetNowFunc(func() time.Time { return now })
		m2.SetSiteAuthCheck(func(s site.ID) bool {
			if s == site.Douyin {
				return dyAuth
			}
			return biliAuth
		})
		m2.RegisterFetcher(dyFetcher)
		m2.RegisterAutoCacheEnqueuer(site.Douyin, func(_ context.Context, item UpdateItem) (string, error) {
			autoCached = append(autoCached, item)
			return "dy-auto-" + item.DynID, nil
		})
		m2.RegisterAutoCacheEnqueuer(site.Bilibili, func(_ context.Context, item UpdateItem) (string, error) {
			biliAutoCached = append(biliAutoCached, item)
			return "bili-auto-" + item.DynID, nil
		})
		m2.SetOnLiveHint(func(acc AccountRef, roomID, webRID string) {
			liveHints = append(liveHints, acc.UID+":"+roomID+":"+webRID)
		})
		return m2
	}

	mgr = reopenManager()

	// Phase 1b (post-restart + unpin): creator unpins baseline-pinned 7100000000000000009 (IsTop=false, PubTs=1700001800 > 1700001200)
	// with HasMore=true. Even after restart, it must NOT be treated as new, must NOT trigger gapDetected, and must stop at page 1!
	now = now.Add(5 * time.Minute)
	dyFetcher.resetCalls()
	dyFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		if uid == dyUID1 {
			if cursor != "0" {
				t.Fatalf("unpinned baseline post must count as reaching baseline on page 1, got cursor=%s", cursor)
			}
			return site.CreatorPostsPage{
				HasMore:    true,
				NextCursor: "1700001200000",
				Posts: []site.CreatorPost{
					{ID: "7100000000000000009", PubTs: 1700001800, Type: "video", IsTop: false, Title: "原置顶已取消置顶"},
					{ID: "7100000000000000005", PubTs: 1700001200, Type: "video", IsTop: false, Title: "非置顶基线"},
				},
			}, nil
		}
		return site.CreatorPostsPage{
			Posts: []site.CreatorPost{
				{ID: "7200000000000000001", PubTs: 1700001100, Type: "draw", IsTop: true, Title: "全置顶账号基线"},
			},
		}, nil
	}
	sum1b, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 1b post-restart unpin CheckNow: %v", err)
	}
	if sum1b.NewVideos != 0 || sum1b.NewDynamics != 0 || sum1b.AutoCacheTasks != 0 || len(sum1b.GapAccounts) != 0 {
		t.Fatalf("expected 0 new items, 0 auto-cache tasks, and 0 gap after restart+unpin, got %+v", sum1b)
	}
	if err := updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item`).Scan(&rawItemCount); err != nil || rawItemCount != 0 {
		t.Fatalf("INV-12 violated after restart+unpin: expected 0 rows in update_item, got %d (err=%v)", rawItemCount, err)
	}

	// Reopen Manager again before Phase 2 to verify persisted baseline-pinned IDs also work when IsTop=true after restart.
	mgr = reopenManager()

	// Phase 2: Incremental check on dyUID1:
	// - Page 1 has:
	//   1) baseline-pinned 7100000000000000009 (NOT counted as new, does NOT stop early)
	//   2) newly pinned old post 7100000000000000001 with PubTs=1700000100 <= baseline (NOT counted as new, does NOT stop early!)
	//   3) new video 7100000000000000020 (PubTs=1700002500 > since) -> auto-cached as video!
	// - Page 2 has:
	//   1) duplicate 7100000000000000020 (deduped across pages!)
	//   2) new note 7100000000000000018 (PubTs=1700002200 > since, Type="draw") -> auto-cached as draw!
	//   3) baseline 7100000000000000005 (non-pinned -> triggers stop!)
	// Meanwhile dyUID2 returns UncertainEmpty -> records "本轮结果不完整", keeps baseline untouched!
	now = now.Add(10 * time.Minute)
	dyFetcher.resetCalls()
	dyFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		if uid == dyUID1 {
			if cursor == "0" {
				return site.CreatorPostsPage{
					HasMore:    true,
					NextCursor: "1700002500000",
					Posts: []site.CreatorPost{
						{ID: "7100000000000000009", PubTs: 1700001800, Type: "video", IsTop: true, Title: "原置顶"},
						{ID: "7100000000000000001", PubTs: 1700000100, Type: "video", IsTop: true, Title: "旧作品新置顶"},
						{
							ID:       "7100000000000000020",
							PubTs:    1700002500,
							Type:     "video",
							IsTop:    false,
							Title:    "新视频",
							CoverURL: "https://p3.douyinpic.com/v20.jpeg?a_bogus=" + sentinel,
							Snapshot: fmt.Sprintf(`{"id":"7100000000000000020","cover":"https://p3.douyinpic.com/v20.jpeg?a_bogus=%s"}`, sentinel),
						},
					},
				}, nil
			}
			return site.CreatorPostsPage{
				HasMore:    true,
				NextCursor: "1700001200000",
				Posts: []site.CreatorPost{
					{ID: "7100000000000000020", PubTs: 1700002500, Type: "video", IsTop: false, Title: "跨页重复视频"},
					{ID: "7100000000000000018", PubTs: 1700002200, Type: "draw", IsTop: false, Title: "新图文"},
					{ID: "7100000000000000005", PubTs: 1700001200, Type: "video", IsTop: false, Title: "非置顶基线"},
					{ID: "7100000000000000002", PubTs: 1700000500, Type: "video", IsTop: false, Title: "基线前旧作品"},
				},
			}, nil
		}
		return site.CreatorPostsPage{
			UncertainEmpty:  true,
			UncertainReason: "空壳响应 status_code=0",
		}, nil
	}

	sum2, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 2 CheckNow: %v", err)
	}
	if sum2.NewVideos != 1 || sum2.NewDynamics != 1 {
		t.Fatalf("expected exactly 1 new video and 1 new draw note (pinned old ignored, deduped), got %+v", sum2)
	}
	if len(autoCached) != 2 || autoCached[0].DynID != "7100000000000000018" || autoCached[0].Type != "draw" || autoCached[1].DynID != "7100000000000000020" || autoCached[1].Type != "video" {
		t.Fatalf("expected chronological auto-cache of draw(18) then video(20), got %+v", autoCached)
	}

	// Verify uncertain empty on dyUID2 recorded "本轮结果不完整" and kept baseline untouched!
	stB2, _, _ := mgr.GetAccountState("douyin", dyUID2)
	if stB2.LastKind != "incomplete" || !strings.HasPrefix(stB2.Note, "本轮结果不完整") {
		t.Fatalf("expected uncertain empty note '本轮结果不完整...', got %+v", stB2)
	}
	if stB2.BaselineAt != stB.BaselineAt || stB2.NewestID != "7200000000000000001" || stB2.NewestTs != 1700001100 {
		t.Fatalf("uncertain empty must NOT move baseline, before=%+v after=%+v", stB, stB2)
	}

	// Verify sentinel never entered updates.db or library.db!
	var leaked int
	_ = updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item WHERE snapshot LIKE ?`, "%"+sentinel+"%").Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("signed URL sentinel leaked into updates.db update_item")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM feed_items WHERE cover_url LIKE ? OR author_face LIKE ?`, "%"+sentinel+"%", "%"+sentinel+"%").Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("signed URL sentinel leaked into library.db feed_items")
	}

	// Phase 3: Douyin hits risk_control on first Douyin account -> remaining Douyin account (dyUID2) stops immediately
	// and Douyin enters backoff, while Bilibili in the SAME round succeeds and auto-caches its new video!
	now = now.Add(10 * time.Minute)
	setBiliDynamics(map[string][]map[string]any{
		"1001": {
			{
				"id_str": "81099",
				"type":   "DYNAMIC_TYPE_AV",
				"modules": map[string]any{
					"module_author":  map[string]any{"mid": 1001, "name": "BiliCreator", "pub_ts": 1700003000},
					"module_dynamic": map[string]any{"desc": map[string]any{"text": "Bili new video during Douyin risk"}},
				},
			},
			{
				"id_str": "81001",
				"type":   "DYNAMIC_TYPE_AV",
				"modules": map[string]any{
					"module_author":  map[string]any{"mid": 1001, "name": "BiliCreator", "pub_ts": 1700001000},
					"module_dynamic": map[string]any{"desc": map[string]any{"text": "Bili baseline"}},
				},
			},
		},
	})
	dyFetcher.resetCalls()
	dyFetcher.handler = func(_ context.Context, uid, _ string) (site.CreatorPostsPage, error) {
		return site.CreatorPostsPage{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindRiskControl,
			Message: "触发抖音验证码风控",
		}
	}

	sum3, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Phase 3 RunScheduledRound: %v", err)
	}
	if sum3.SiteStopped["douyin"] != site.ErrKindRiskControl {
		t.Fatalf("expected SiteStopped[douyin]=risk_control, got %+v", sum3)
	}
	if calls := dyFetcher.callSnapshot(); len(calls) != 1 {
		t.Fatalf("expected second Douyin account skipped immediately after first Douyin risk_control, got calls=%v", calls)
	}
	if len(biliAutoCached) != 1 || biliAutoCached[0].DynID != "81099" {
		t.Fatalf("expected Bilibili check & auto-cache unaffected by Douyin risk_control, got %+v", biliAutoCached)
	}
	if inBackoff, _ := mgr.IsSiteInBackoff("douyin"); !inBackoff {
		t.Fatalf("expected Douyin in backoff after risk_control")
	}
	if inBackoff, _ := mgr.IsSiteInBackoff("bilibili"); inBackoff {
		t.Fatalf("Bilibili must NOT enter backoff when only Douyin hit risk_control")
	}

	// Phase 4: Unlogged Douyin skips Douyin accounts with "未登录抖音，已跳过" and 0 fetcher calls while Bilibili runs!
	now = now.Add(2 * time.Hour) // past Douyin backoff
	dyAuth = false
	dyFetcher.resetCalls()
	biliBefore := biliCallCount
	sum4, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 4 unlogged Douyin CheckNow: %v", err)
	}
	if !slices.Contains(sum4.SkippedUnauthSites, "douyin") || len(dyFetcher.callSnapshot()) != 0 {
		t.Fatalf("expected Douyin skipped with 0 calls when unlogged, got sum=%+v calls=%v", sum4, dyFetcher.callSnapshot())
	}
	if biliCallCount <= biliBefore {
		t.Fatalf("expected Bilibili still checked when Douyin unlogged")
	}
	stUnauth, _, _ := mgr.GetAccountState("douyin", dyUID1)
	if stUnauth.Note != "未登录抖音，已跳过" {
		t.Fatalf("expected skip note '未登录抖音，已跳过', got %q", stUnauth.Note)
	}
}

// Per-site scheduled cap (Douyin <= 20, Bilibili <= 30), Douyin >= 2.5s spacing,
// Douyin 120 req/hr quota bucket deferral & mid-round stop, and bidirectional backoff isolation.
func TestPerSiteQuotaPacingRoundCapAndBackoffIsolation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mid := r.URL.Query().Get("host_mid")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": false,
				"items": []map[string]any{
					{
						"id_str": "500" + mid,
						"type":   "DYNAMIC_TYPE_WORD",
						"modules": map[string]any{
							"module_author":  map[string]any{"mid": 1, "name": "UP" + mid, "pub_ts": 1700000100},
							"module_dynamic": map[string]any{"desc": map[string]any{"text": "ok"}},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	cred, _, _ := credential.Open(filepath.Join(tmp, "bili.bin"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "rt")
	biliClient := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	biliClient.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	subCfg := settings.Default(tmp).Subscription
	subCfg.Enabled = true
	subCfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), nil, nil, biliClient, subCfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	biliClient.SetAutomaticRequestHandlers(mgr.AutomaticRequestAllowed, mgr.RecordAutomaticStop)

	// Keep real default intervals (2s Bilibili, 2.5s Douyin) and capture every pacing sleep without wall-clock delay!
	if got := mgr.SiteAccountInterval("douyin"); got != 2500*time.Millisecond {
		t.Fatalf("default Douyin interval = %v, want 2.5s", got)
	}
	if got := mgr.SiteAccountInterval("bilibili"); got != 2*time.Second {
		t.Fatalf("default Bilibili interval = %v, want 2s", got)
	}

	var sleepsMu sync.Mutex
	var recordedSleeps []time.Duration
	mgr.SetSleepFunc(func(_ context.Context, d time.Duration) error {
		sleepsMu.Lock()
		recordedSleeps = append(recordedSleeps, d)
		sleepsMu.Unlock()
		return nil
	})

	dyFetcher := &syntheticDouyinFetcher{
		handler: func(ctx context.Context, uid, _ string) (site.CreatorPostsPage, error) {
			if site.AutomaticRequestSource(ctx) != "" {
				if err := mgr.SiteAutomaticRequestAllowed("douyin"); err != nil {
					return site.CreatorPostsPage{}, err
				}
			}
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "7300000000000000100", PubTs: 1700000200, Type: "video", Title: uid},
				},
			}, nil
		},
	}
	mgr.RegisterFetcher(dyFetcher)
	mgr.SetSiteAuthCheck(func(site.ID) bool { return true })

	// Create 35 Bilibili accounts and 25 Douyin accounts.
	for i := 1; i <= 35; i++ {
		uid := fmt.Sprintf("%d", 2000+i)
		_, err := mgr.Create(fmt.Sprintf("Bili-%d", i), "", []AccountRef{{Site: "bilibili", UID: uid}}, SubRules{ScheduledCheck: true})
		if err != nil {
			t.Fatalf("create bili %d: %v", i, err)
		}
	}
	for i := 1; i <= 25; i++ {
		secUID := fmt.Sprintf("MS4wLjABAAAA__Creator_%02d", i)
		_, err := mgr.Create(fmt.Sprintf("Douyin-%d", i), "", []AccountRef{{Site: "douyin", UID: secUID}}, SubRules{ScheduledCheck: true})
		if err != nil {
			t.Fatalf("create douyin %d: %v", i, err)
		}
	}

	// 1. Scheduled round caps: Bilibili <= 30, Douyin <= 20 (total 50 checked),
	// with 29 Bilibili sleeps of 2s and 19 Douyin sleeps of 2.5s!
	ctx := context.Background()
	sum1, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound 1: %v", err)
	}
	if sum1.CheckedCreators != 50 {
		t.Fatalf("expected 30 Bilibili + 20 Douyin = 50 CheckedCreators, got %+v", sum1)
	}
	if gotDy := len(dyFetcher.callSnapshot()); gotDy != 20 {
		t.Fatalf("expected <=20 Douyin accounts in one round, got %d", gotDy)
	}
	var count2s, count25s int
	for _, d := range recordedSleeps {
		if d == 2*time.Second {
			count2s++
		} else if d == 2500*time.Millisecond {
			count25s++
		} else {
			t.Fatalf("unexpected sleep duration %v", d)
		}
	}
	if count2s != 29 || count25s != 19 {
		t.Fatalf("expected 29 Bilibili 2s sleeps and 19 Douyin 2.5s sleeps, got 2s=%d 2.5s=%d", count2s, count25s)
	}

	// 2. Douyin 120/hr quota exhaustion defers ONLY Douyin while Bilibili runs normally!
	// Already used 20 Douyin tokens in round 1; consume 85 more -> 105 used (15 left < 2*20=40 needed for 20 accounts).
	if !mgr.ConsumeSiteQuotaTokens("douyin", 85) {
		t.Fatalf("expected 85 Douyin tokens to fit within remaining 100")
	}
	if mgr.ConsumeSiteQuotaTokens("douyin", 16) {
		t.Fatalf("105 + 16 = 121 must exceed Douyin 120/hr bucket")
	}
	dyFetcher.resetCalls()
	sumQuota, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound Douyin quota deferred: %v", err)
	}
	if !sumQuota.DeferredByQuota || !slices.Contains(sumQuota.SiteDeferredByQuota, "douyin") {
		t.Fatalf("expected Douyin deferred by 120/hr quota, got %+v", sumQuota)
	}
	if len(dyFetcher.callSnapshot()) != 0 {
		t.Fatalf("expected 0 Douyin calls when deferred by quota, got %v", dyFetcher.callSnapshot())
	}
	if sumQuota.CheckedCreators != 30 {
		t.Fatalf("expected 30 Bilibili accounts still checked when Douyin quota exhausted, got %+v", sumQuota)
	}

	// 3. Bidirectional backoff isolation:
	// (a) Advance >1h so quotas replenish, put Douyin in backoff -> Bilibili runs!
	now = now.Add(2 * time.Hour)
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindRateLimited)
	dyFetcher.resetCalls()
	sumDyBackoff, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound Douyin in backoff: %v", err)
	}
	if !sumDyBackoff.DeferredByBackoff || !slices.Contains(sumDyBackoff.SiteDeferredByBackoff, "douyin") || sumDyBackoff.CheckedCreators != 30 || len(dyFetcher.callSnapshot()) != 0 {
		t.Fatalf("expected Douyin deferred by backoff while 30 Bilibili accounts ran, got sum=%+v dyCalls=%d", sumDyBackoff, len(dyFetcher.callSnapshot()))
	}

	// (b) Advance past Douyin backoff, put Bilibili in backoff -> Douyin runs!
	now = now.Add(3 * time.Hour)
	mgr.RecordSiteAutomaticStop("bilibili", site.ErrKindRiskControl)
	dyFetcher.resetCalls()
	sumBiliBackoff, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound Bilibili in backoff: %v", err)
	}
	if !sumBiliBackoff.DeferredByBackoff || !slices.Contains(sumBiliBackoff.SiteDeferredByBackoff, "bilibili") || sumBiliBackoff.CheckedCreators != 20 || len(dyFetcher.callSnapshot()) != 20 {
		t.Fatalf("expected Bilibili deferred by backoff while 20 Douyin accounts ran, got sum=%+v dyCalls=%d", sumBiliBackoff, len(dyFetcher.callSnapshot()))
	}

	states := mgr.ListSiteSchedulerStates()
	if states["bilibili"].BackoffUntil == "" || states["douyin"].BackoffUntil != "" {
		t.Fatalf("expected Bilibili backoff active and Douyin backoff cleared after successful Douyin round, got %+v", states)
	}
}

func TestDouyinNewPinnedGapCursorStallSharedAutoCacheCapAndMidRoundQuota(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	lib, err := library.Open(db, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}

	subCfg := settings.Default(tmp).Subscription
	subCfg.Enabled = true
	subCfg.IntervalMinutes = 30
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), lib, nil, nil, subCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetAccountInterval(0)

	now := time.Unix(1700010000, 0).UTC()
	mgr.SetNowFunc(func() time.Time { return now })
	mgr.SetSiteAuthCheck(func(site.ID) bool { return true })

	dyFetcher := &syntheticDouyinFetcher{}
	mgr.RegisterFetcher(dyFetcher)

	var enqueued []string
	mgr.RegisterAutoCacheEnqueuer(site.Douyin, func(_ context.Context, item UpdateItem) (string, error) {
		enqueued = append(enqueued, item.DynID+":"+item.Type)
		return "auto-" + item.DynID, nil
	})

	rule, err := mgr.SaveRule(CacheRule{
		Name:           "全类型自动缓存",
		ScheduledCheck: true,
		AutoCache:      true,
		Types:          []string{"video", "draw"},
	})
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	uidPinned := "MS4wLjABAAAA__NewPinnedCreator"
	uidGap := "MS4wLjABAAAA__GapCreator"
	uidStall := "MS4wLjABAAAA__StallCreator"

	sub1, err := mgr.Create("置顶与翻页组", "", []AccountRef{
		{Site: "douyin", UID: uidPinned, Name: "Pinned"},
		{Site: "douyin", UID: uidGap, Name: "Gap"},
		{Site: "douyin", UID: uidStall, Name: "Stall"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create sub1: %v", err)
	}
	if _, err := mgr.SetSubscriptionRule(sub1.ID, rule.ID); err != nil {
		t.Fatalf("SetSubscriptionRule: %v", err)
	}

	// Pre-populate one item in library.db so auto-cache skips creating a duplicate download task!
	if _, err := lib.SaveItem(filepath.Join(tmp, "cache", "douyin", "pre", "works", "7500000000000000101"), library.ItemMetadata{
		Site:   "douyin",
		Kind:   "video",
		ID:     "7500000000000000101",
		Title:  "已缓存作品",
		PubTs:  1700012001,
		Status: "complete",
		Author: library.ItemAuthor{Site: "douyin", UID: uidGap, Name: "Gap"},
	}); err != nil {
		t.Fatalf("SaveItem pre-cached: %v", err)
	}

	ctx := context.Background()

	// Round 1: establish baselines on all 3 accounts.
	dyFetcher.handler = func(_ context.Context, uid, _ string) (site.CreatorPostsPage, error) {
		return site.CreatorPostsPage{
			Posts: []site.CreatorPost{
				{ID: "7500000000000000001", PubTs: 1700010500, Type: "video", IsTop: false, Title: "基线"},
			},
		}, nil
	}
	if _, err := mgr.CheckNow(ctx, 0); err != nil {
		t.Fatalf("Round 1 baseline: %v", err)
	}

	// Round 2:
	// - uidPinned publishes a BRAND NEW pinned post (IsTop=true, PubTs > baseline) -> counted ONCE!
	// - uidGap has 23 new posts across 2 pages and still HasMore=true without reaching baseline ->
	//   capped at 2 pages, marks gapDetected ("可能还有更早的更新没有拿到"), 1 pre-cached item skipped,
	//   and together with uidPinned + uidStall hits the <=20 auto-cache tasks/round cap with backlog!
	// - uidStall returns NextCursor == cursor on page 1 -> cursor-stall guard stops at 1 page!
	now = now.Add(5 * time.Minute)
	dyFetcher.resetCalls()
	dyFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		switch uid {
		case uidPinned:
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "7500000000000000050", PubTs: 1700011000, Type: "draw", IsTop: true, Title: "新发布并置顶的图文"},
					{ID: "7500000000000000001", PubTs: 1700010500, Type: "video", IsTop: false, Title: "基线"},
				},
			}, nil
		case uidGap:
			if cursor == "0" {
				var p1 []site.CreatorPost
				for i := 112; i >= 101; i-- {
					p1 = append(p1, site.CreatorPost{
						ID:    fmt.Sprintf("7500000000000000%d", i),
						PubTs: int64(1700012000 + (i - 100)),
						Type:  "video",
						Title: fmt.Sprintf("GapP1-%d", i),
					})
				}
				return site.CreatorPostsPage{
					HasMore:    true,
					NextCursor: "1700012001000",
					Posts:      p1,
				}, nil
			}
			var p2 []site.CreatorPost
			for i := 100; i >= 90; i-- {
				p2 = append(p2, site.CreatorPost{
					ID:    fmt.Sprintf("7500000000000000%03d", i),
					PubTs: int64(1700011500 + i),
					Type:  "video",
					Title: fmt.Sprintf("GapP2-%d", i),
				})
			}
			return site.CreatorPostsPage{
				HasMore:    true,
				NextCursor: "1700011500000",
				Posts:      p2,
			}, nil
		case uidStall:
			return site.CreatorPostsPage{
				HasMore:    true,
				NextCursor: cursor, // stalled cursor (equals "0") -> must stop after page 1!
				Posts: []site.CreatorPost{
					{ID: "7500000000000000060", PubTs: 1700011200, Type: "video", IsTop: false, Title: "StallNew"},
					{ID: "7500000000000000001", PubTs: 1700010500, Type: "video", IsTop: false, Title: "基线"},
				},
			}, nil
		}
		return site.CreatorPostsPage{}, nil
	}

	sum2, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Round 2 CheckNow: %v", err)
	}
	// Calls: uidPinned(1) + uidGap(2 max pages) + uidStall(1 stalled cursor) = 4 total calls!
	if calls := dyFetcher.callSnapshot(); len(calls) != 4 {
		t.Fatalf("expected 4 fetcher calls (1 + 2 capped + 1 cursor-stalled), got %v", calls)
	}
	if !slices.Contains(sum2.GapAccounts, "douyin:"+uidGap) {
		t.Fatalf("expected GapAccounts to contain douyin:%s, got %v", uidGap, sum2.GapAccounts)
	}
	stGap, _, _ := mgr.GetAccountState("douyin", uidGap)
	if stGap.Note != "可能还有更早的更新没有拿到" {
		t.Fatalf("expected gap note on uidGap, got %q", stGap.Note)
	}
	// Total newly discovered items: 1 (uidPinned) + 23 (uidGap: 12 on p1 + 11 on p2) + 1 (uidStall) = 25.
	// 1 item (7500000000000000101) was already in library.db -> marked cached:douyin:video:7500000000000000101 without task slot!
	// Remaining 24 items -> 20 enqueued in Round 2, 4 left in AutoCacheBacklog!
	if sum2.AutoCacheTasks != 20 || sum2.AutoCacheBacklog != 4 || len(enqueued) != 20 {
		t.Fatalf("expected 20 auto-cache tasks and 4 backlog, got sum=%+v enqueued=%d", sum2, len(enqueued))
	}
	var cachedMarker string
	_ = updatesDB.QueryRow(`SELECT auto_task_id FROM update_item WHERE site='douyin' AND uid=? AND dyn_id='7500000000000000101'`, uidGap).Scan(&cachedMarker)
	if cachedMarker != "cached:douyin:video:7500000000000000101" {
		t.Fatalf("expected pre-cached item marked cached:douyin:video:..., got %q", cachedMarker)
	}

	// Round 3: New pinned post 7500000000000000050 is STILL pinned at the top -> must NOT be counted as new again,
	// and remaining 4 backlog items drain cleanly!
	now = now.Add(5 * time.Minute)
	dyFetcher.handler = func(_ context.Context, uid, _ string) (site.CreatorPostsPage, error) {
		switch uid {
		case uidPinned:
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "7500000000000000050", PubTs: 1700011000, Type: "draw", IsTop: true, Title: "仍在置顶的新图文"},
					{ID: "7500000000000000001", PubTs: 1700010500, Type: "video", IsTop: false, Title: "基线"},
				},
			}, nil
		case uidGap:
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "7500000000000000112", PubTs: 1700012012, Type: "video", IsTop: false, Title: "GapP1-112"},
				},
			}, nil
		default:
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "7500000000000000060", PubTs: 1700011200, Type: "video", IsTop: false, Title: "StallNew"},
				},
			}, nil
		}
	}
	sum3, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Round 3 CheckNow: %v", err)
	}
	if sum3.NewDynamics != 0 {
		t.Fatalf("already-discovered pinned post must NOT be counted again on Round 3, got %+v", sum3)
	}
	if sum3.AutoCacheTasks != 4 || sum3.AutoCacheBacklog != 0 {
		t.Fatalf("expected remaining 4 backlog items drained in Round 3, got %+v", sum3)
	}

	// Round 4: Mid-round Douyin 120/hr quota stop — pre-consume 113 tokens (7 left >= 2*3=6 conservative pre-check),
	// then each account uses 2 pages + 2 nested tokens so account #2 hits 120/hr mid-round and stops account #3!
	if !mgr.ConsumeSiteQuotaTokens("douyin", 113) {
		t.Fatalf("expected 113 Douyin tokens reserved")
	}
	dyFetcher.resetCalls()
	dyFetcher.handler = func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		if site.AutomaticRequestSource(ctx) != "" {
			// Simulate 4 automatic API tokens per account (e.g., page 1 + page 2 + nested profile/detail).
			for k := 0; k < 4; k++ {
				if err := mgr.SiteAutomaticRequestAllowed("douyin"); err != nil {
					return site.CreatorPostsPage{}, err
				}
			}
		}
		return site.CreatorPostsPage{
			Posts: []site.CreatorPost{
				{ID: "7500000000000000001", PubTs: 1700010500, Type: "video", IsTop: false},
			},
		}, nil
	}
	sum4, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 4 mid-round quota stop: %v", err)
	}
	if !sum4.DeferredByQuota || !slices.Contains(sum4.SiteDeferredByQuota, "douyin") {
		t.Fatalf("expected mid-round DeferredByQuota for douyin, got %+v", sum4)
	}
	if calls := dyFetcher.callSnapshot(); len(calls) != 2 {
		t.Fatalf("expected stop on 2nd Douyin account without calling 3rd account, got calls=%v", calls)
	}
	if st := mgr.GetSiteSchedulerState("douyin"); len(st.TokenTimestamps) > 120 {
		t.Fatalf("Douyin quota tokens must NEVER exceed 120/hr, got %d", len(st.TokenTimestamps))
	}
}
