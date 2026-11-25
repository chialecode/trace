package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
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
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/subscription"
)

func makeServiceTestJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

type serviceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f serviceRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func seedWeiboStaticItemForBackfill(t *testing.T, b *Backend, uid, mid, picID string, imgBytes []byte, stdVer int, via *library.ItemVia, text string, extraMedia []library.MediaFile, pages []library.NotePageRecord) library.ItemMetadata {
	t.Helper()
	subDir := "posts"
	if via != nil && via.Reason == library.ViaForward {
		subDir = "forwards"
	}
	itemDir := filepath.Join(b.GetSettings().CacheDir, "weibo", uid, subDir, mid)
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var media []library.MediaFile
	var notePages []library.NotePageRecord
	var assetIDs []string
	if len(imgBytes) > 0 {
		mf := writeVerifiedLocalFile(t, itemDir, "images/01.jpg", imgBytes, "image", 48, 36, 0)
		media = append(media, mf)
		notePages = []library.NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.jpg", MediaID: picID}}
		if picID != "" {
			assetIDs = []string{picID}
		}
	}
	if len(extraMedia) > 0 {
		media = append(media, extraMedia...)
	}
	if len(pages) > 0 {
		notePages = pages
	}
	if text == "" {
		text = "微博历史测试 " + mid
	}
	saved, err := b.lib.SaveItem(itemDir, library.ItemMetadata{
		Site:                 "weibo",
		Kind:                 "dynamic",
		ID:                   mid,
		RawID:                mid,
		Title:                text,
		Text:                 text,
		PubTs:                1700000000,
		Status:               "complete",
		CacheStandardVersion: stdVer,
		Author:               library.ItemAuthor{Site: "weibo", UID: uid, Name: "合成博主"},
		Via:                  via,
		AssetIDs:             assetIDs,
		Media:                media,
		NotePages:            notePages,
	})
	if err != nil {
		t.Fatalf("SaveItem %s: %v", mid, err)
	}
	return saved
}

func TestBackfillSummaryZeroNetworkAndCounts(t *testing.T) {
	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	if _, err := b.subs.Create("合成博主", "", []subscription.AccountRef{
		{Site: "weibo", UID: "100001", Name: "合成博主"},
	}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}

	jpg := makeServiceTestJPEG(t, 40, 30, color.RGBA{R: 180, G: 70, B: 40, A: 255})

	// 1. Missing NoWM + has AssetIDs -> FastCDN candidate.
	seedWeiboStaticItemForBackfill(t, b, "100001", "5093000000000001", "001fastA", jpg, 2, nil, "", nil, nil)
	// 2. Missing NoWM + no AssetIDs + subscribed author -> Subscribed candidate.
	seedWeiboStaticItemForBackfill(t, b, "100001", "5093000000000002", "", jpg, 2, nil, "", nil, nil)
	// 3. Missing NoWM + no AssetIDs + forward -> Fallback detail candidate.
	seedWeiboStaticItemForBackfill(t, b, "100001", "5093000000000003", "", jpg, 2, &library.ItemVia{Reason: library.ViaForward, Site: "weibo", UID: "100001"}, "", nil, nil)
	// 4. Text-only legacy inline URL -> LegacyLinks candidate.
	seedWeiboStaticItemForBackfill(t, b, "100001", "5093000000000004", "", nil, 2, nil, "查看公告 (https://example.com/post/4)", nil, nil)
	// 5. GIF-only -> excluded.
	gifDir := filepath.Join(b.GetSettings().CacheDir, "weibo", "100001", "posts", "5093000000000005")
	gifMF := writeVerifiedLocalFile(t, gifDir, "images/01.gif", []byte("GIF89a123456"), "image", 16, 16, 0)
	if _, err := b.lib.SaveItem(gifDir, library.ItemMetadata{
		Site: "weibo", Kind: "dynamic", ID: "5093000000000005", RawID: "5093000000000005",
		Status: "complete", CacheStandardVersion: 0,
		Author:    library.ItemAuthor{Site: "weibo", UID: "100001", Name: "合成博主"},
		Media:     []library.MediaFile{gifMF},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.gif", GIF: true}},
	}); err != nil {
		t.Fatal(err)
	}
	// 6. Already at WeiboCacheStandardVersion = 3 -> excluded.
	seedWeiboStaticItemForBackfill(t, b, "100001", "5093000000000006", "006done", jpg, library.WeiboCacheStandardVersion, nil, "", nil, nil)
	// 7. Archived snapshot -> excluded.
	archDir := filepath.Join(b.GetSettings().CacheDir, "weibo", "100001", "posts", "5093000000000001__v1-1700000001")
	archMF := writeVerifiedLocalFile(t, archDir, "images/01.jpg", jpg, "image", 40, 30, 0)
	if _, err := b.lib.SaveItem(archDir, library.ItemMetadata{
		Site: "weibo", Kind: "dynamic", ID: "5093000000000001__v1-1700000001", RawID: "5093000000000001__v1-1700000001",
		Status: "complete", CacheStandardVersion: 2, ArchivedOf: "weibo:dynamic:5093000000000001",
		Author:    library.ItemAuthor{Site: "weibo", UID: "100001", Name: "合成博主"},
		Media:     []library.MediaFile{archMF},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", Image: "images/01.jpg"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Zero login, zero network!
	sum, err := b.BackfillSummary()
	if err != nil {
		t.Fatalf("BackfillSummary: %v", err)
	}
	if sum.WeiboMissingNoWM != 3 || sum.WeiboLegacyLinks != 1 || sum.WeiboFastCDNCandidates != 1 ||
		sum.WeiboSubscribedCount != 1 || sum.WeiboFallbackDetailCount != 1 || sum.TotalItems != 4 {
		t.Fatalf("unexpected BackfillSummary(on): %+v", sum)
	}

	// Disable WeiboRemoveWatermark -> only legacy link item remains.
	cur := b.GetSettings()
	cur.WeiboRemoveWatermark = false
	if _, err := b.UpdateSettings(cur); err != nil {
		t.Fatal(err)
	}
	sumOff, err := b.BackfillSummary()
	if err != nil {
		t.Fatalf("BackfillSummary(off): %v", err)
	}
	if sumOff.WeiboMissingNoWM != 0 || sumOff.WeiboLegacyLinks != 1 || sumOff.TotalItems != 1 {
		t.Fatalf("unexpected BackfillSummary(off): %+v", sumOff)
	}
}

func TestFullCacheLinkageWeiboNoWMAndLegacyLinks(t *testing.T) {
	clock := newFakeQueueClock(time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clock)

	jpg := makeServiceTestJPEG(t, 40, 30, color.RGBA{R: 120, G: 90, B: 200, A: 255})
	saved := seedWeiboStaticItemForBackfill(t, b, "100001", "5094000000000001", "001linkPic", jpg, 2, nil, "图文联动测试", nil, nil)

	post := weibo.Post{
		Mid:       "5094000000000001",
		Text:      "图文联动测试",
		CreatedAt: 1700000000,
		Author:    weibo.AuthorSummary{UID: "100001", ScreenName: "合成博主"},
		Images: []weibo.ImageAsset{
			{
				Index:            1,
				PicID:            "001linkPic",
				Kind:             weibo.MediaKindImage,
				URL:              "https://wx1.sinaimg.cn/large/001linkPic.jpg",
				WatermarkFreeURL: "https://wx1.sinaimg.cn/oslarge/001linkPic.jpg",
				Width:            40,
				Height:           30,
			},
		},
	}

	// 1. Local v2 missing NN_nowm + remote post provides WatermarkFreeURL -> inspectEnqueue (0 extra statuses/show calls!).
	res := b.inspectWeiboCachedPost(post, saved, "full_cache")
	if res.action != inspectEnqueue {
		t.Fatalf("expected inspectEnqueue for Weibo missing NoWM companion, got %+v", res)
	}

	// 2. Once stamped with WeiboCacheStandardVersion = 3 -> inspectSkipNoOp.
	savedV3 := saved
	savedV3.CacheStandardVersion = library.WeiboCacheStandardVersion
	resV3 := b.inspectWeiboCachedPost(post, savedV3, "full_cache")
	if resV3.action != inspectSkipNoOp {
		t.Fatalf("expected inspectSkipNoOp for v3 item, got %+v", resV3)
	}

	// 3. Pre-DEC-073 inline URL item -> silently migrated offline without touching media!
	legacySaved := seedWeiboStaticItemForBackfill(t, b, "100001", "5094000000000002", "", nil, 2, nil, "请查看 原文链接 (https://example.com/article/2)", nil, nil)
	legacyPost := weibo.Post{
		Mid:       "5094000000000002",
		Text:      "请查看 原文链接",
		Links:     []site.TextLink{{Label: "原文链接", URL: "https://example.com/article/2"}},
		CreatedAt: 1700000000,
		Author:    weibo.AuthorSummary{UID: "100001", ScreenName: "合成博主"},
	}
	resLegacy := b.inspectWeiboCachedPost(legacyPost, legacySaved, "full_cache")
	if resLegacy.action != inspectSkipNoOp {
		t.Fatalf("expected inspectSkipNoOp after offline legacy link upgrade, got %+v", resLegacy)
	}
	upgraded, ok, err := b.lib.GetItem(legacySaved.ItemKey)
	if err != nil || !ok {
		t.Fatalf("GetItem upgraded legacy: ok=%v err=%v", ok, err)
	}
	if strings.Contains(upgraded.Text, "https://example.com") || len(upgraded.Links) != 1 || upgraded.Links[0].URL != "https://example.com/article/2" {
		t.Fatalf("legacy inline link was not cleanly migrated offline: text=%q links=%+v", upgraded.Text, upgraded.Links)
	}
}

func TestWeiboBackfillQueueThreeTierFastCDNQuotaAndRestartRecovery(t *testing.T) {
	orig1 := makeServiceTestJPEG(t, 44, 34, color.RGBA{R: 200, G: 60, B: 40, A: 255})
	nowm1 := makeServiceTestJPEG(t, 30, 22, color.RGBA{R: 30, G: 160, B: 220, A: 255})
	nowmC1 := makeServiceTestJPEG(t, 32, 24, color.RGBA{R: 60, G: 190, B: 90, A: 255})
	nowmC2 := makeServiceTestJPEG(t, 28, 20, color.RGBA{R: 180, G: 90, B: 220, A: 255})

	var (
		mymblogHits atomic.Int32
		showHits    atomic.Int32
		cdnHits     atomic.Int32
		show429     atomic.Bool
	)

	const (
		uidSub = "100001"
		midA   = "5095000000000001"
		midB1  = "5095000000000002"
		midB2  = "5095000000000003"
		midB3  = "5095000000000004"
		midC1a = "5095000000000005"
		midC1b = "5095000000000006"
		midC2  = "5095000000000007"
	)

	wbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = w.Write([]byte(`{"ok":1,"data":{"screen_name":"合成博主"}}`))
		case "/ajax/profile/info":
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"100001","screen_name":"合成博主","statuses_count":20}}}`))
		case "/ajax/statuses/mymblog":
			mymblogHits.Add(1)
			m1, _ := weibo.MidToMblogID(midC1a)
			m2, _ := weibo.MidToMblogID(midC1b)
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"ok": 1,
				"data": {
					"since_id": "",
					"bottom_tips_visible": true,
					"bottom_tips_text": "仅展示最近半年微博",
					"list": [
						{
							"idstr": %q, "mblogid": %q, "created_at": "Sat Nov 21 15:00:00 +0800 2026",
							"text_raw": "订阅批量匹配A",
							"user": {"idstr": "100001", "screen_name": "合成博主"},
							"pic_ids": ["picC1a"],
							"pic_infos": {"picC1a": {"pic_id": "picC1a", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/picC1a.jpg", "width": 44, "height": 34}}}
						},
						{
							"idstr": %q, "mblogid": %q, "created_at": "Sat Nov 21 14:00:00 +0800 2026",
							"text_raw": "订阅批量匹配B",
							"user": {"idstr": "100001", "screen_name": "合成博主"},
							"pic_ids": ["picC1b"],
							"pic_infos": {"picC1b": {"pic_id": "picC1b", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/picC1b.jpg", "width": 44, "height": 34}}}
						}
					]
				}
			}`, midC1a, m1, midC1b, m2)))
		case "/ajax/statuses/show":
			showHits.Add(1)
			if show429.Load() {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"ok":0,"msg":"rate limited"}`))
				return
			}
			mC2, _ := weibo.MidToMblogID(midC2)
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"ok": 1,
				"idstr": %q, "mblogid": %q, "created_at": "Sat Nov 21 13:00:00 +0800 2026",
				"text_raw": "兜底详情补全",
				"user": {"idstr": "100001", "screen_name": "合成博主"},
				"pic_ids": ["picC2"],
				"pic_infos": {"picC2": {"pic_id": "picC2", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/picC2.jpg", "width": 44, "height": 34}}}
			}`, midC2, mC2)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer wbSrv.Close()

	cdnRT := serviceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		cdnHits.Add(1)
		var body []byte
		switch {
		case strings.Contains(req.URL.Path, "/oslarge/picFast"):
			body = nowm1
		case strings.Contains(req.URL.Path, "/oslarge/picC1"):
			body = nowmC1
		case strings.Contains(req.URL.Path, "/oslarge/picC2"):
			body = nowmC2
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("404")),
			}, nil
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: int64(len(body)),
			Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
			Body:          io.NopCloser(bytes.NewReader(body)),
		}, nil
	})

	makeLoc := func(name string) appdirs.Locations {
		tmp := t.TempDir()
		l, err := appdirs.ResolveWithOptions(appdirs.Options{
			Channel:   "dev",
			ForceMode: "portable",
			DataDir:   filepath.Join(tmp, name+"-data"),
			CacheDir:  filepath.Join(tmp, name+"-cache"),
		})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	biliSrv := newTestBiliServer(t)
	defer biliSrv.Close()

	makeBackend := func(l appdirs.Locations, clk *fakeQueueClock) *Backend {
		b, err := NewBackend(Options{
			Locations:        l,
			Desktop:          NewMockDesktop(),
			DisableBgWorkers: true,
			CacheQueueClock:  clk,
			CacheQueueRand:   func(n int) int { return n / 2 },
			CacheQueuePacing: &CacheQueuePacing{
				ItemMinDelay:        40 * time.Second,
				ItemMaxDelay:        60 * time.Second,
				FastCDNMinDelay:     2 * time.Second,
				FastCDNMaxDelay:     5 * time.Second,
				LongRestMinItems:    2,
				LongRestMaxItems:    2,
				LongRestMinDelay:    10 * time.Minute,
				LongRestMaxDelay:    15 * time.Minute,
				BackoffCoolMinDelay: 10 * time.Minute,
				BackoffCoolMaxDelay: 10 * time.Minute,
				StartupMinDelay:     0,
				StartupMaxDelay:     0,
			},
			BilibiliOpts: bilibili.ClientOptions{
				Endpoints: bilibili.Endpoints{
					APIBase:      biliSrv.URL,
					PassportBase: biliSrv.URL,
					WWWBase:      biliSrv.URL,
					SpaceBase:    biliSrv.URL,
					DynamicBase:  biliSrv.URL,
					LiveAPIBase:  biliSrv.URL,
					LiveWebBase:  biliSrv.URL,
				},
				PacingInterval: time.Millisecond,
				AllowLocalhost: true,
			},
			WeiboOpts: weibo.ClientOptions{
				Endpoints: weibo.Endpoints{
					PCBasicInfo:     wbSrv.URL + "/ajax/setting/getBasicInfo",
					ProfileInfo:     wbSrv.URL + "/ajax/profile/info",
					StatusesMyMblog: wbSrv.URL + "/ajax/statuses/mymblog",
					StatusesShow:    wbSrv.URL + "/ajax/statuses/show",
				},
				MinInterval:    -1,
				JitterMax:      -1,
				AllowLocalhost: true,
			},
		})
		if err != nil {
			t.Fatalf("NewBackend: %v", err)
		}
		b.dl.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper { return cdnRT })
		return b
	}

	// ---- PART 1: Tier A (offline) + Tier B (3 pure CDN items > LongRestMinItems=2) under EXHAUSTED 100/h Weibo API quota! ----
	loc1 := makeLoc("part1")
	clock1 := newFakeQueueClock(time.Now().UTC())
	b1 := makeBackend(loc1, clock1)
	b1Closed := false
	defer func() {
		if !b1Closed {
			_ = b1.Close()
		}
	}()
	ctx := context.Background()
	// Log in ONLY to Bilibili so HasAnyLogin() is true while Weibo remains LOGGED OUT!
	if _, err := b1.ImportCookie(ctx, "SESSDATA=s; bili_jct=j; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie bilibili: %v", err)
	}

	// Exhaust Weibo 100/h JSON API quota completely before starting Tier A + Tier B!
	if !b1.subs.ConsumeSiteQuotaTokens("weibo", subscription.DefaultWeiboHourlyQuota) {
		t.Fatal("failed to exhaust Weibo hourly quota")
	}
	if b1.subs.CanUseSiteQuotaTokens("weibo", 1) {
		t.Fatal("expected Weibo hourly quota to be exhausted")
	}

	seedWeiboStaticItemForBackfill(t, b1, uidSub, midA, "", nil, 2, nil, "旧格式 (https://example.com/a)", nil, nil)
	seedWeiboStaticItemForBackfill(t, b1, uidSub, midB1, "picFast1", orig1, 2, nil, "", nil, nil)
	seedWeiboStaticItemForBackfill(t, b1, uidSub, midB2, "picFast2", orig1, 2, nil, "", nil, nil)
	seedWeiboStaticItemForBackfill(t, b1, uidSub, midB3, "picFast3", orig1, 2, nil, "", nil, nil)

	// Start backfill WITHOUT even logging into Weibo (Tier A + Tier B require 0 login & 0 API quota!).
	b1.queue.Start(ctx)
	task1, err := b1.StartWeiboBackfill(ctx)
	if err != nil {
		t.Fatalf("StartWeiboBackfill(Tier A+B): %v", err)
	}
	if task1.Kind != CacheQueueKindWeiboBackfill || task1.BackfillTotal != 4 {
		t.Fatalf("unexpected backfill task: %+v", task1)
	}

	deadline := time.Now().Add(8 * time.Second)
	for {
		list := b1.ListCacheQueue()
		if len(list) == 1 && list[0].Status == CacheQueueCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Tier A+B backfill; queues=%+v", list)
		}
		if _, ok := clock1.AdvanceToNextTimer(); !ok {
			time.Sleep(15 * time.Millisecond)
		}
	}

	if mymblogHits.Load() != 0 || showHits.Load() != 0 {
		t.Fatalf("Tier A+B must make ZERO Weibo API calls, got mymblog=%d show=%d", mymblogHits.Load(), showHits.Load())
	}
	if cdnHits.Load() != 3 {
		t.Fatalf("expected 3 CDN hits for 3 Tier B items, got %d", cdnHits.Load())
	}
	// Verify every non-zero timer scheduled during Tier A+B was strictly in [2s, 5s] (NO 40s+, NO 10m long rest!).
	fastWaits := 0
	for _, d := range clock1.History() {
		if d == 0 {
			continue
		}
		if d < 2*time.Second || d > 5*time.Second {
			t.Fatalf("Tier B scheduled non-fast delay %v (history=%v); must stay 2-5s without long rest", d, clock1.History())
		}
		fastWaits++
	}
	if fastWaits < 2 {
		t.Fatalf("expected at least 2 fast 2-5s CDN pacing waits across 3 Tier B items, got %d (history=%v)", fastWaits, clock1.History())
	}

	afterA, ok, _ := b1.lib.GetItem("weibo:dynamic:" + midA)
	if !ok || strings.Contains(afterA.Text, "https://example.com") || len(afterA.Links) != 1 || afterA.CacheStandardVersion != library.WeiboCacheStandardVersion {
		t.Fatalf("Tier A item not properly upgraded: %+v", afterA)
	}
	for _, mid := range []string{midB1, midB2, midB3} {
		it, ok, _ := b1.lib.GetItem("weibo:dynamic:" + mid)
		if !ok || it.CacheStandardVersion != library.WeiboCacheStandardVersion || len(it.NotePages) != 1 || it.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
			t.Fatalf("Tier B item %s not properly backfilled: %+v", mid, it)
		}
		gotOrig, _ := os.ReadFile(filepath.Join(it.ItemDir, "images", "01.jpg"))
		if !bytes.Equal(gotOrig, orig1) {
			t.Fatalf("Tier B item %s primary image was altered!", mid)
		}
	}
	if _, err := b1.RemoveFinishedCacheQueues(); err != nil {
		t.Fatal(err)
	}
	_ = b1.Close()
	b1Closed = true

	// ---- PART 2: Tier C1 (subscribed mymblog batch) + Tier C2 (statuses/show fallback) + 429 pause + restart recovery! ----
	loc2 := makeLoc("part2")
	clock2 := newFakeQueueClock(time.Now().UTC())
	b2 := makeBackend(loc2, clock2)
	b2Closed := false
	defer func() {
		if !b2Closed {
			_ = b2.Close()
		}
	}()
	if _, err := b2.LoginWithCookie(ctx, "weibo", "SUB=SYNTH_SUB_BF_C; SUBP=SYNTH_SUBP_BF_C", ""); err != nil {
		t.Fatalf("LoginWithCookie: %v", err)
	}
	if _, err := b2.subs.Create("合成博主", "", []subscription.AccountRef{{Site: "weibo", UID: uidSub, Name: "合成博主"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}

	seedWeiboStaticItemForBackfill(t, b2, uidSub, midC1a, "", orig1, 2, nil, "", nil, nil)
	seedWeiboStaticItemForBackfill(t, b2, uidSub, midC1b, "", orig1, 2, nil, "", nil, nil)
	seedWeiboStaticItemForBackfill(t, b2, uidSub, midC2, "", orig1, 2, &library.ItemVia{Reason: library.ViaForward, Site: "weibo", UID: uidSub}, "", nil, nil)

	show429.Store(true)
	b2.queue.Start(ctx)
	if _, err := b2.StartWeiboBackfill(ctx); err != nil {
		t.Fatalf("StartWeiboBackfill(Tier C): %v", err)
	}

	deadline = time.Now().Add(8 * time.Second)
	for {
		list := b2.ListCacheQueue()
		if len(list) == 1 && list[0].Status == CacheQueueWaitingBackoff {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Tier C2 429 waiting_backoff; queues=%+v", list)
		}
		if _, ok := clock2.AdvanceToNextTimer(); !ok {
			time.Sleep(15 * time.Millisecond)
		}
	}

	if mymblogHits.Load() != 1 {
		t.Fatalf("expected 1 mymblog batch call for Tier C1, got %d", mymblogHits.Load())
	}
	for _, mid := range []string{midC1a, midC1b} {
		it, ok, _ := b2.lib.GetItem("weibo:dynamic:" + mid)
		if !ok || it.CacheStandardVersion != library.WeiboCacheStandardVersion || it.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
			t.Fatalf("Tier C1 item %s should already be backfilled before C2 429: %+v", mid, it)
		}
	}

	rawQueueJSON, err := os.ReadFile(filepath.Join(loc2.DataDir, "cache-queue.json"))
	if err != nil {
		t.Fatalf("read cache-queue.json: %v", err)
	}
	var qDoc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(rawQueueJSON, &qDoc); err != nil || qDoc.Version != 3 {
		t.Fatalf("cache-queue.json version = %d (err=%v), want 3", qDoc.Version, err)
	}
	if strings.Contains(string(rawQueueJSON), "http://") || strings.Contains(string(rawQueueJSON), "https://") || strings.Contains(string(rawQueueJSON), "SYNTH_SUB") {
		t.Fatalf("cache-queue.json leaked URL or secret: %s", string(rawQueueJSON))
	}

	// Simulate app restart! Close b2, reopen b3 past backoff with 429 cleared.
	_ = b2.Close()
	b2Closed = true
	show429.Store(false)
	clock3 := newFakeQueueClock(time.Now().UTC().Add(6 * time.Hour))
	b3 := makeBackend(loc2, clock3)
	defer func() { _ = b3.Close() }()
	b3.queue.Start(ctx)

	deadline = time.Now().Add(8 * time.Second)
	for {
		list := b3.ListCacheQueue()
		if len(list) == 1 && list[0].Status == CacheQueueCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for resumed Tier C2 backfill after restart; queues=%+v", list)
		}
		if _, ok := clock3.AdvanceToNextTimer(); !ok {
			time.Sleep(15 * time.Millisecond)
		}
	}

	afterC2, ok, _ := b3.lib.GetItem("weibo:dynamic:" + midC2)
	if !ok || afterC2.CacheStandardVersion != library.WeiboCacheStandardVersion || afterC2.NotePages[0].NoWatermark != "images/01_nowm.jpg" {
		t.Fatalf("Tier C2 item %s not completed after restart: %+v", midC2, afterC2)
	}
	if mymblogHits.Load() != 1 {
		t.Fatalf("mymblog was unexpectedly re-called after restart: hits=%d", mymblogHits.Load())
	}
	finalSum, err := b3.BackfillSummary()
	if err != nil || finalSum.TotalItems != 0 {
		t.Fatalf("expected BackfillSummary TotalItems=0 after full completion, got %+v (err=%v)", finalSum, err)
	}
}

func TestServiceForceRecacheItemOverwriteAndGuards(t *testing.T) {
	v1Img := makeServiceTestJPEG(t, 40, 30, color.RGBA{R: 220, G: 50, B: 50, A: 255})
	v2Img := makeServiceTestJPEG(t, 44, 34, color.RGBA{R: 40, G: 180, B: 220, A: 255})
	v2NoWM := makeServiceTestJPEG(t, 28, 20, color.RGBA{R: 50, G: 210, B: 100, A: 255})

	const mid = "5096000000000001"
	mblogID, _ := weibo.MidToMblogID(mid)

	var (
		mu      sync.RWMutex
		deleted bool
	)

	wbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = w.Write([]byte(`{"ok":1,"data":{"screen_name":"强制重下号"}}`))
		case "/ajax/profile/info":
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"100001","screen_name":"强制重下号"}}}`))
		case "/ajax/statuses/show":
			mu.RLock()
			isDel := deleted
			mu.RUnlock()
			if isDel {
				_, _ = w.Write([]byte(`{"ok":0,"msg":"该微博已被作者删除"}`))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"ok": 1,
				"idstr": %q, "mblogid": %q, "created_at": "Sat Nov 21 15:00:00 +0800 2026",
				"text_raw": "强制重下微博",
				"user": {"idstr": "100001", "screen_name": "强制重下号"},
				"pic_ids": ["forcePic1"],
				"pic_infos": {"forcePic1": {"pic_id": "forcePic1", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/forcePic1.jpg", "width": 44, "height": 34}}}
			}`, mid, mblogID)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer wbSrv.Close()

	cdnRT := serviceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch req.URL.Path {
		case "/large/forcePic1.jpg":
			body = v2Img
		case "/oslarge/forcePic1.jpg":
			body = v2NoWM
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("404"))}, nil
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: int64(len(body)),
			Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
			Body:          io.NopCloser(bytes.NewReader(body)),
		}, nil
	})

	loc := testLocations(t)
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:  wbSrv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:  wbSrv.URL + "/ajax/profile/info",
				StatusesShow: wbSrv.URL + "/ajax/statuses/show",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	b.dl.SetMediaTransportWrap(func(_ http.RoundTripper) http.RoundTripper { return cdnRT })
	if err := b.dl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := b.LoginWithCookie(ctx, "weibo", "SUB=SYNTH_SUB_FORCE; SUBP=SYNTH_SUBP_FORCE", ""); err != nil {
		t.Fatal(err)
	}

	// Reject archived snapshot & live item.
	archDir := filepath.Join(b.GetSettings().CacheDir, "weibo", "100001", "posts", mid+"__v1-1700000001")
	archMF := writeVerifiedLocalFile(t, archDir, "images/01.jpg", v1Img, "image", 40, 30, 0)
	arch, err := b.lib.SaveItem(archDir, library.ItemMetadata{
		Site: "weibo", Kind: "dynamic", ID: mid + "__v1-1700000001", RawID: mid + "__v1-1700000001",
		Status: "complete", ArchivedOf: "weibo:dynamic:" + mid,
		Media: []library.MediaFile{archMF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ForceRecacheItem(ctx, arch.ItemKey); err == nil {
		t.Fatal("ForceRecacheItem must reject archived snapshots")
	}

	liveDir := filepath.Join(b.GetSettings().CacheDir, "bilibili", "20001", "lives", "9001")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	liveItem, err := b.lib.SaveItem(liveDir, library.ItemMetadata{
		Site: "bilibili", Kind: "live", ID: "9001", RawID: "9001", Status: "complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ForceRecacheItem(ctx, liveItem.ItemKey); err == nil {
		t.Fatal("ForceRecacheItem must reject live recordings")
	}

	// Seed V1 Weibo item, force-recache -> overwrites with V2 primary + companion!
	saved := seedWeiboStaticItemForBackfill(t, b, "100001", mid, "forcePic1", v1Img, 2, nil, "强制重下微博", nil, nil)
	task, err := b.ForceRecacheItem(ctx, saved.ItemKey)
	if err != nil {
		t.Fatalf("ForceRecacheItem: %v", err)
	}
	if err := b.dl.WaitTasks(ctx, task.TaskID); err != nil {
		t.Fatalf("WaitTasks ForceRecacheItem: %v", err)
	}
	gotPrimary, _ := os.ReadFile(filepath.Join(saved.ItemDir, "images", "01.jpg"))
	gotNoWM, _ := os.ReadFile(filepath.Join(saved.ItemDir, "images", "01_nowm.jpg"))
	if !bytes.Equal(gotPrimary, v2Img) || !bytes.Equal(gotNoWM, v2NoWM) {
		sumGot := sha256.Sum256(gotPrimary)
		sumWant := sha256.Sum256(v2Img)
		t.Fatalf("ForceRecacheItem did not overwrite local files: got=%s want=%s nowmLen=%d",
			hex.EncodeToString(sumGot[:]), hex.EncodeToString(sumWant[:]), len(gotNoWM))
	}

	// Remote deleted -> ForceRecacheItem refuses and leaves V2 local files untouched!
	mu.Lock()
	deleted = true
	mu.Unlock()
	if _, err := b.ForceRecacheItem(ctx, saved.ItemKey); err == nil {
		t.Fatal("ForceRecacheItem should fail when remote post is deleted")
	}
	stillPrimary, _ := os.ReadFile(filepath.Join(saved.ItemDir, "images", "01.jpg"))
	stillNoWM, _ := os.ReadFile(filepath.Join(saved.ItemDir, "images", "01_nowm.jpg"))
	if !bytes.Equal(stillPrimary, v2Img) || !bytes.Equal(stillNoWM, v2NoWM) {
		t.Fatal("local files were damaged when ForceRecacheItem hit deleted remote post")
	}
}
