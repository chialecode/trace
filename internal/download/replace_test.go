package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

type fakeGenericWorkPlanner struct {
	siteID     site.ID
	mu         sync.Mutex
	plans      map[string]func(ctx context.Context, call int) (WorkDownloadPlan, error)
	calls      map[string]int
	lastAuto   string
	reported   []error
	requireErr error
}

func newFakeGenericWorkPlanner(siteID site.ID) *fakeGenericWorkPlanner {
	return &fakeGenericWorkPlanner{
		siteID: siteID,
		plans:  make(map[string]func(ctx context.Context, call int) (WorkDownloadPlan, error)),
		calls:  make(map[string]int),
	}
}

func (p *fakeGenericWorkPlanner) SiteID() site.ID { return p.siteID }

func (p *fakeGenericWorkPlanner) RequireAuth(_ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requireErr
}

func (p *fakeGenericWorkPlanner) ReportAutomaticError(_ context.Context, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported = append(p.reported, err)
}

func (p *fakeGenericWorkPlanner) setPlan(targetID string, fn func(ctx context.Context, call int) (WorkDownloadPlan, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans[targetID] = fn
}

func (p *fakeGenericWorkPlanner) lastAutomaticSource() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAuto
}

func (p *fakeGenericWorkPlanner) reportedErrors() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reported)
}

func (p *fakeGenericWorkPlanner) PlanWorkDownload(ctx context.Context, targetID string, _ WorkPlanOptions) (WorkDownloadPlan, error) {
	p.mu.Lock()
	p.calls[targetID]++
	call := p.calls[targetID]
	p.lastAuto = site.AutomaticRequestSource(ctx)
	fn := p.plans[targetID]
	p.mu.Unlock()
	if fn == nil {
		return WorkDownloadPlan{}, &site.Error{Site: p.siteID, Kind: site.ErrKindUnavailable, Message: "作品不存在"}
	}
	return fn(ctx, call)
}

func localhostTransport(s site.ID) site.MediaTransportSpec {
	return site.MediaTransportSpec{
		Site:           s,
		AllowLocalhost: true,
		AllowedDomains: []string{"127.0.0.1", "localhost"},
	}
}

// Invariants #1, #5, and tempnames_test.go watchNames:
//   - Re-caching a Bilibili multi-part video whose CIDs/pages changed WITHOUT resolution trips
//     ErrConflictDetected, emits a sanitized ConflictNotice (zero URLs/queries/CDN hosts), and
//     leaves V1 item.json + MP4s + user-placed files 100% untouched.
//   - Resolving with ConflictResolveKeepBoth archives V1 into sibling "<bvid>__v1-<ts>" (with
//     ArchivedOf set and independent lifecycle) and swaps V2 atomically without exposing any
//     non-temp filenames to watchNames.
//   - Resolving a subsequent V3 edit with ConflictResolveUseRemote replaces live V2 in-place,
//     deletes stale V2-only part files (e.g. P02.mp4 when V3 shrinks to 1P), and preserves
//     unregistered user-placed files.
func TestBilibiliConflictTripwireKeepBothAndUseRemoteWithWatchNames(t *testing.T) {
	v1Bytes, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	v2Bytes, err := BuildSyntheticVideoM4S(90, 30) // 3s
	if err != nil {
		t.Fatal(err)
	}
	a1Bytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}
	a2Bytes, err := BuildSyntheticAudioM4S(141)
	if err != nil {
		t.Fatal(err)
	}

	var version atomic.Int32
	version.Store(1)

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		ver := version.Load()
		var pages []map[string]any
		title := "V1 Title"
		dur := 4
		switch ver {
		case 1:
			pages = []map[string]any{
				{"cid": 1001, "page": 1, "part": "V1-P1", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
				{"cid": 1002, "page": 2, "part": "V1-P2", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
			}
		case 2:
			title = "V2 Replaced Title https://leak.example/x?SESSDATA=secret&sig=abc"
			dur = 5
			pages = []map[string]any{
				{"cid": 2001, "page": 1, "part": "V2-P1", "duration": 3, "dimension": map[string]any{"width": 16, "height": 16}},
				{"cid": 2002, "page": 2, "part": "V2-P2", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
			}
		default:
			title = "V3 Shrunk 1P"
			dur = 3
			pages = []map[string]any{
				{"cid": 3001, "page": 1, "part": "V3-P1", "duration": 3, "dimension": map[string]any{"width": 16, "height": 16}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV17x411w7KC",
				"aid":      170001,
				"title":    title,
				"desc":     "Desc",
				"pic":      srvURL + "/media/cover.jpg",
				"pubdate":  1700000000,
				"duration": dur,
				"owner":    map[string]any{"mid": 888, "name": "Creator888", "face": srvURL + "/media/face.jpg"},
				"pages":    pages,
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		cid := r.URL.Query().Get("cid")
		vPath := "/media/v1.m4s"
		aPath := "/media/a1.m4s"
		dSec := 2
		if cid == "2001" || cid == "3001" {
			vPath = "/media/v2.m4s"
			aPath = "/media/a2.m4s"
			dSec = 3
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "dash",
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": dSec,
					"video": []map[string]any{
						{"id": 80, "baseUrl": srvURL + vPath, "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 500000},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + aPath, "codecs": "mp4a.40.2", "bandwidth": 192000},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
			{ID: 1, IDStr: "1", Progress: 300, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "dm"},
		}))
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		cidInt, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"bvid": "BV17x411w7KC", "cid": cidInt, "subtitle": map[string]any{"subtitles": []any{}}},
		})
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/media/v1.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(v1Bytes)))
		_, _ = w.Write(v1Bytes)
	})
	mux.HandleFunc("/media/a1.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(a1Bytes)))
		_, _ = w.Write(a1Bytes)
	})
	mux.HandleFunc("/media/v2.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(v2Bytes)))
		_, _ = w.Write(v2Bytes)
	})
	mux.HandleFunc("/media/a2.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(a2Bytes)))
		_, _ = w.Write(a2Bytes)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	ctx := context.Background()

	// Step 1: Initial V1 2-part cache.
	tasksV1, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV17x411w7KC", Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueVideo V1: %v", err)
	}
	var idsV1 []string
	for _, tk := range tasksV1 {
		idsV1 = append(idsV1, tk.TaskID)
	}
	if err := mgr.WaitTasks(ctx, idsV1...); err != nil {
		t.Fatalf("WaitTasks V1: %v", err)
	}

	v1Item, ok, err := libStore.GetItem("bilibili:video:BV17x411w7KC")
	if err != nil || !ok || v1Item.Status != "complete" || len(v1Item.Parts) != 2 {
		t.Fatalf("expected complete 2P V1, got %+v ok=%v err=%v", v1Item, ok, err)
	}
	if v1Item.CacheStandardVersion != library.CurrentCacheStandardVersion {
		t.Fatalf("expected CacheStandardVersion=%d, got %d", library.CurrentCacheStandardVersion, v1Item.CacheStandardVersion)
	}
	userNotePath := filepath.Join(v1Item.ItemDir, "my-notes.txt")
	if err := os.WriteFile(userNotePath, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	origManifestBytes, err := os.ReadFile(filepath.Join(v1Item.ItemDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	origP1Bytes, err := os.ReadFile(filepath.Join(v1Item.ItemDir, "P01.mp4"))
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Switch remote to V2 (CIDs changed 1001->2001, 1002->2002) and re-enqueue WITHOUT resolution -> Tripwire!
	var conflictMu sync.Mutex
	var notices []ConflictNotice
	mgr.SetConflictHandler(func(n ConflictNotice) {
		conflictMu.Lock()
		notices = append(notices, n)
		conflictMu.Unlock()
	})
	version.Store(2)

	tasksTrip, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV17x411w7KC", Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueVideo tripwire: %v", err)
	}
	var tripIDs []string
	for _, tk := range tasksTrip {
		tripIDs = append(tripIDs, tk.TaskID)
	}
	waitErr := mgr.WaitTasks(ctx, tripIDs...)
	if !errors.Is(waitErr, ErrConflictDetected) {
		t.Fatalf("expected ErrConflictDetected from WaitTasks, got %v", waitErr)
	}
	conflictMu.Lock()
	if len(notices) == 0 {
		conflictMu.Unlock()
		t.Fatalf("expected ConflictNotice emitted")
	}
	gotNotice := notices[0]
	conflictMu.Unlock()
	if gotNotice.ItemKey != "bilibili:video:BV17x411w7KC" || gotNotice.ReasonKind != ConflictReasonParts {
		t.Fatalf("unexpected ConflictNotice: %+v", gotNotice)
	}
	if len(gotNotice.RemoteFingerprint) != 64 {
		t.Fatalf("expected 64-hex RemoteFingerprint, got %q", gotNotice.RemoteFingerprint)
	}
	for _, bad := range []string{"http://", "https://", "SESSDATA", "sig=", "?"} {
		if strings.Contains(gotNotice.Title, bad) || strings.Contains(gotNotice.RemoteSummary, bad) {
			t.Fatalf("unsanitized ConflictNotice leaked %q: %+v", bad, gotNotice)
		}
	}
	// Verify V1 item.json and P01.mp4 are byte-for-byte untouched!
	afterTripManifest, _ := os.ReadFile(filepath.Join(v1Item.ItemDir, "item.json"))
	afterTripP1, _ := os.ReadFile(filepath.Join(v1Item.ItemDir, "P01.mp4"))
	if string(afterTripManifest) != string(origManifestBytes) || string(afterTripP1) != string(origP1Bytes) {
		t.Fatalf("tripwire mutated local V1 files!")
	}

	// Step 3: Resolve V2 with ConflictResolveKeepBoth while watchNames monitors CacheDir!
	checkWatchKeep := watchNames(t, libStore.CacheDir())
	keepCtx := WithConflictResolution(ctx, ConflictResolveKeepBoth)
	tasksKeep, err := mgr.EnqueueVideo(keepCtx, VideoEnqueueRequest{BVID: "BV17x411w7KC", Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueVideo keep_both: %v", err)
	}
	if len(tasksKeep) != 1 {
		t.Fatalf("expected single atomic task for keep_both multi-part replace, got %d", len(tasksKeep))
	}
	if err := mgr.WaitTasks(ctx, tasksKeep[0].TaskID); err != nil {
		t.Fatalf("WaitTasks keep_both: %v", err)
	}
	checkWatchKeep()

	v2Item, ok, err := libStore.GetItem("bilibili:video:BV17x411w7KC")
	if err != nil || !ok || v2Item.Status != "complete" || v2Item.Parts[0].CID != 2001 {
		t.Fatalf("expected V2 live item with CID 2001, got %+v ok=%v err=%v", v2Item, ok, err)
	}
	// Find archived V1 item in library!
	allItems, err := libStore.ListItems(library.FeedFilter{Site: "bilibili"})
	if err != nil {
		t.Fatal(err)
	}
	var foundArchive *library.ItemMetadata
	for i := range allItems {
		if allItems[i].ArchivedOf == "bilibili:video:BV17x411w7KC" {
			foundArchive = &allItems[i]
			break
		}
	}
	if foundArchive == nil || !strings.HasPrefix(foundArchive.RawID, "BV17x411w7KC__v1-") {
		t.Fatalf("expected archived V1 item, got items=%+v", allItems)
	}
	archP1, err := os.ReadFile(filepath.Join(foundArchive.ItemDir, "P01.mp4"))
	if err != nil || string(archP1) != string(origP1Bytes) {
		t.Fatalf("archived V1 P01.mp4 mismatch: err=%v", err)
	}

	// Step 4: Switch remote to V3 (shrunk to 1P, CID 3001) and resolve with ConflictResolveUseRemote.
	// P02.mp4 was removed in V3, so watchNames allows "P02.mp4" and "P02.danmaku.xml" to be cleaned up.
	version.Store(3)
	checkWatchRemote := watchNames(t, libStore.CacheDir())
	remoteCtx := WithConflictResolution(ctx, ConflictResolveUseRemote)
	tasksV3, err := mgr.EnqueueVideo(remoteCtx, VideoEnqueueRequest{BVID: "BV17x411w7KC", Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueVideo use_remote: %v", err)
	}
	if err := mgr.WaitTasks(ctx, tasksV3[0].TaskID); err != nil {
		t.Fatalf("WaitTasks use_remote: %v", err)
	}
	checkWatchRemote("P02.mp4", "P02.danmaku.xml")

	v3Item, ok, err := libStore.GetItem("bilibili:video:BV17x411w7KC")
	if err != nil || !ok || len(v3Item.Parts) != 1 || v3Item.Parts[0].CID != 3001 {
		t.Fatalf("expected 1P V3 with CID 3001, got %+v", v3Item)
	}
	if _, err := os.Stat(filepath.Join(v3Item.ItemDir, "P02.mp4")); !os.IsNotExist(err) {
		t.Fatalf("expected stale V2 P02.mp4 removed after 2P->1P use_remote, stat=%v", err)
	}
	// User-placed my-notes.txt MUST still be untouched!
	gotUserNotes, err := os.ReadFile(userNotePath)
	if err != nil || string(gotUserNotes) != "keep-me" {
		t.Fatalf("user-placed file lost or modified: err=%v got=%q", err, string(gotUserNotes))
	}
}

// Invariant #2: Remote degraded/deleted/placeholder/HLS-only/downgraded responses MUST return
// ErrRemoteDegradedRefused even when ConflictResolveUseRemote or ConflictResolveKeepBoth is set,
// leaving local V1 100% untouched.
func TestDegradedRemoteRefusedEvenOnUseRemoteAcrossSites(t *testing.T) {
	progMP4 := buildProgressiveWithoutElst(t, true, false, 0)

	var srvURL string
	var servePlaceholderGIF atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/media/img.jpg", func(w http.ResponseWriter, r *http.Request) {
		if servePlaceholderGIF.Load() {
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write(syntheticGIFMini())
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/media/video.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(progMP4)))
		_, _ = w.Write(progMP4)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	weiboPlanner := newFakeGenericWorkPlanner(site.Weibo)
	xPlanner := newFakeGenericWorkPlanner(site.X)
	mgr.RegisterWorkPlanner(weiboPlanner)
	mgr.RegisterWorkPlanner(xPlanner)

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	ctx := context.Background()

	// 1. Cache valid Weibo 1-image post V1.
	weiboPlanner.setPlan("5010000000000001", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Weibo,
			Kind:       "dynamic",
			TargetID:   "5010000000000001",
			AuthorUID:  "1700000001",
			AuthorName: "WBUser",
			Desc:       "V1 Weibo Post",
			Transport:  localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "wb_pic_v1", Width: 800, Height: 600, ImageURLs: []string{srvURL + "/media/img.jpg"}},
			},
		}, nil
	})
	tWB1, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "5010000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tWB1.TaskID); err != nil {
		t.Fatalf("WaitTasks Weibo V1: %v", err)
	}
	wbBefore, _, _ := libStore.GetItem("weibo:dynamic:5010000000000001")
	wbImgBefore, err := os.ReadFile(filepath.Join(wbBefore.ItemDir, "images", "01.jpg"))
	if err != nil {
		t.Fatal(err)
	}

	// Case A: Remote Weibo post deleted (404 / ErrKindUnavailable) even with ConflictResolveUseRemote!
	weiboPlanner.setPlan("5010000000000001", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "该微博已被作者删除"}
	})
	_, delErr := mgr.EnqueueWeiboWork(WithConflictResolution(ctx, ConflictResolveUseRemote), WeiboWorkEnqueueRequest{TargetID: "5010000000000001"})
	if !errors.Is(delErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on deleted Weibo post, got %v", delErr)
	}

	// Case B: Remote Weibo V2 image returns Sina HTTP-200 deleted-image placeholder GIF even with ConflictResolveUseRemote!
	servePlaceholderGIF.Store(true)
	weiboPlanner.setPlan("5010000000000001", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Weibo,
			Kind:       "dynamic",
			TargetID:   "5010000000000001",
			AuthorUID:  "1700000001",
			AuthorName: "WBUser",
			Desc:       "V2 Edited Text",
			EditCount:  1,
			Transport:  localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "wb_pic_v2", Width: 800, Height: 600, ImageURLs: []string{srvURL + "/media/img.jpg"}},
			},
		}, nil
	})
	tWB2, err := mgr.EnqueueWeiboWork(WithConflictResolution(ctx, ConflictResolveUseRemote), WeiboWorkEnqueueRequest{TargetID: "5010000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	phErr := mgr.WaitTasks(ctx, tWB2.TaskID)
	if !errors.Is(phErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on Sina placeholder GIF, got %v", phErr)
	}
	wbAfter, _, _ := libStore.GetItem("weibo:dynamic:5010000000000001")
	wbImgAfter, _ := os.ReadFile(filepath.Join(wbAfter.ItemDir, "images", "01.jpg"))
	if string(wbImgAfter) != string(wbImgBefore) || wbAfter.Text != wbBefore.Text {
		t.Fatalf("Weibo V1 was modified despite placeholder GIF refusal!")
	}
	servePlaceholderGIF.Store(false)

	// 2. Cache valid X progressive MP4 video V1, then simulate HLS-only & bitrate downgrade on UseRemote!
	xPlanner.setPlan("1800000000000000001", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.X,
			Kind:        "dynamic",
			TargetID:    "1800000000000000001",
			AuthorUID:   "1900000001",
			AuthorName:  "XCreator",
			Desc:        "X Video V1",
			MediaID:     "x_vid_1",
			DurationSec: 2,
			Width:       16,
			Height:      16,
			Bitrate:     2000000,
			VideoURLs:   []string{srvURL + "/media/video.mp4"},
			Transport:   localhostTransport(site.X),
		}, nil
	})
	tX1, err := mgr.EnqueueXWork(ctx, XWorkEnqueueRequest{TargetID: "1800000000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tX1.TaskID); err != nil {
		t.Fatalf("WaitTasks X V1: %v", err)
	}

	// Case C: X video returns HLSOnly == true even with ConflictResolveKeepBoth!
	xPlanner.setPlan("1800000000000000001", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.X,
			Kind:        "dynamic",
			TargetID:    "1800000000000000001",
			AuthorUID:   "1900000001",
			AuthorName:  "XCreator",
			Desc:        "X Video HLS",
			MediaID:     "x_vid_1",
			DurationSec: 2,
			HLSOnly:     true,
			VideoURLs:   []string{srvURL + "/media/playlist.m3u8"},
			Transport:   localhostTransport(site.X),
		}, nil
	})
	tXHLS, err := mgr.EnqueueXWork(WithConflictResolution(ctx, ConflictResolveKeepBoth), XWorkEnqueueRequest{TargetID: "1800000000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	hlsErr := mgr.WaitTasks(ctx, tXHLS.TaskID)
	if !errors.Is(hlsErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on X HLS-only downgrade, got %v", hlsErr)
	}

	// Case D: Same X video MediaID returns lower bitrate stream even with ConflictResolveUseRemote!
	xPlanner.setPlan("1800000000000000001", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.X,
			Kind:        "dynamic",
			TargetID:    "1800000000000000001",
			AuthorUID:   "1900000001",
			AuthorName:  "XCreator",
			Desc:        "X Video V1",
			MediaID:     "x_vid_1",
			DurationSec: 2,
			Width:       16,
			Height:      16,
			Bitrate:     500000, // lower than local 2000000!
			VideoURLs:   []string{srvURL + "/media/video.mp4"},
			Transport:   localhostTransport(site.X),
		}, nil
	})
	tXLow, err := mgr.EnqueueXWork(WithConflictResolution(ctx, ConflictResolveUseRemote), XWorkEnqueueRequest{TargetID: "1800000000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	lowErr := mgr.WaitTasks(ctx, tXLow.TaskID)
	if !errors.Is(lowErr, ErrRemoteDegradedRefused) {
		t.Fatalf("expected ErrRemoteDegradedRefused on stream quality downgrade, got %v", lowErr)
	}
}

// Invariants #3 & #4:
//   - Mixed V2 edit on a 2-page Weibo post where page 1 (unchanged MediaID) returns HTTP 404 on CDN
//     while page 2 is a brand-new image: ConflictResolveKeepBoth reuses verified local V1 page 1
//     and downloads page 2, succeeding with zero data loss!
//   - Monotonic protection on safe upgrades (mode == ""):
//     a) Forwarded Weibo post whose Origin later became Unavailable preserves local V1 Origin + fromOrigin media.
//     b) Truncated Weibo feed snippet does NOT overwrite already-cached full LongText.
func TestMixedV2AssetReuseAndMonotonicOriginLongTextProtection(t *testing.T) {
	var failPic1 atomic.Bool
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/media/pic1.jpg", func(w http.ResponseWriter, r *http.Request) {
		if failPic1.Load() {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/media/pic2_v1.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/media/pic2_v2.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(append(syntheticPNGMini(), []byte("extra-png-bytes")...))
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	weiboPlanner := newFakeGenericWorkPlanner(site.Weibo)
	mgr.RegisterWorkPlanner(weiboPlanner)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	ctx := context.Background()
	fullLongText := "这是一段非常完整的长微博正文内容，包含了第一段详细说明、第二段技术细节以及末尾总结。"

	// 1. Cache V1 2-page post with full LongText.
	weiboPlanner.setPlan("5020000000000002", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Weibo,
			Kind:       "dynamic",
			TargetID:   "5020000000000002",
			AuthorUID:  "1700000002",
			AuthorName: "Author7002",
			Desc:       fullLongText,
			Transport:  localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "pic_keep_1", Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic1.jpg"}},
				{Index: 2, Kind: "image", MediaID: "pic_old_2", Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic2_v1.jpg"}},
			},
		}, nil
	})
	tk1, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "5020000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tk1.TaskID); err != nil {
		t.Fatal(err)
	}

	// 2. Safe re-check (mode == "") with truncated snippet "...全文" must NOT trip conflict and MUST keep fullLongText!
	weiboPlanner.setPlan("5020000000000002", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Weibo,
			Kind:       "dynamic",
			TargetID:   "5020000000000002",
			AuthorUID:  "1700000002",
			AuthorName: "Author7002",
			Desc:       "这是一段非常完整的长微博正文内容，包含了第一段详细说明...全文",
			Transport:  localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "pic_keep_1", Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic1.jpg"}},
				{Index: 2, Kind: "image", MediaID: "pic_old_2", Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic2_v1.jpg"}},
			},
		}, nil
	})
	tkTrunc, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "5020000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkTrunc.TaskID); err != nil {
		t.Fatalf("truncated snippet should be treated as safe no-op, got %v", err)
	}
	gotAfterTrunc, _, _ := libStore.GetItem("weibo:dynamic:5020000000000002")
	if gotAfterTrunc.Text != fullLongText {
		t.Fatalf("monotonic LongText lost! got %q, want %q", gotAfterTrunc.Text, fullLongText)
	}

	// 3. Mixed V2 edit: pic_keep_1 CDN now 404s, while pic_old_2 is replaced by pic_new_2!
	failPic1.Store(true)
	weiboPlanner.setPlan("5020000000000002", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:       site.Weibo,
			Kind:       "dynamic",
			TargetID:   "5020000000000002",
			AuthorUID:  "1700000002",
			AuthorName: "Author7002",
			Desc:       "V2 Edited Post Body",
			EditCount:  2,
			Transport:  localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "pic_keep_1", Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic1.jpg"}},
				{Index: 2, Kind: "image", MediaID: "pic_new_2", Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic2_v2.png"}},
			},
		}, nil
	})
	tkMixed, err := mgr.EnqueueWeiboWork(WithConflictResolution(ctx, ConflictResolveKeepBoth), WeiboWorkEnqueueRequest{TargetID: "5020000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkMixed.TaskID); err != nil {
		t.Fatalf("mixed V2 edit should reuse local pic_keep_1 and succeed, got %v", err)
	}
	gotMixed, _, _ := libStore.GetItem("weibo:dynamic:5020000000000002")
	if gotMixed.Status != "complete" || len(gotMixed.NotePages) != 2 || gotMixed.NotePages[1].MediaID != "pic_new_2" {
		t.Fatalf("unexpected mixed V2 result: %+v", gotMixed)
	}
	failPic1.Store(false)

	// 4. Forwarded post Origin monotonic protection: V1 has valid Origin + fromOrigin image; later re-cache sees OriginUnavailable=true.
	weiboPlanner.setPlan("5030000000000003", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:             site.Weibo,
			Kind:             "dynamic",
			TargetID:         "5030000000000003",
			AuthorUID:        "1700000003",
			AuthorName:       "Forwarder",
			Desc:             "Forward comment",
			IsForward:        true,
			OriginID:         "5000000000000099",
			OriginAuthorUID:  "1700000099",
			OriginAuthorName: "OrigAuthor",
			OriginText:       "Precious Origin Text",
			Transport:        localhostTransport(site.Weibo),
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", MediaID: "orig_pic_1", FromOrigin: true, Width: 100, Height: 100, ImageURLs: []string{srvURL + "/media/pic1.jpg"}},
			},
		}, nil
	})
	tkFwd1, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "5030000000000003"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkFwd1.TaskID); err != nil {
		t.Fatal(err)
	}

	// Now Origin is deleted upstream (OriginUnavailable: true, 0 pages). Safe re-cache (mode == "") must preserve Origin + fromOrigin image!
	weiboPlanner.setPlan("5030000000000003", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:                    site.Weibo,
			Kind:                    "dynamic",
			TargetID:                "5030000000000003",
			AuthorUID:               "1700000003",
			AuthorName:              "Forwarder",
			Desc:                    "Forward comment",
			IsForward:               true,
			OriginID:                "5000000000000099",
			OriginUnavailable:       true,
			OriginUnavailableReason: "抱歉，作者已设置仅展示半年内微博",
			Transport:               localhostTransport(site.Weibo),
		}, nil
	})
	tkFwd2, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "5030000000000003"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WaitTasks(ctx, tkFwd2.TaskID); err != nil {
		t.Fatalf("safe re-cache with vanished origin failed: %v", err)
	}
	gotFwd, _, _ := libStore.GetItem("weibo:dynamic:5030000000000003")
	if gotFwd.Origin == nil || gotFwd.Origin.Unavailable || gotFwd.Origin.Text != "Precious Origin Text" {
		t.Fatalf("expected valid local Origin preserved monotonically, got %+v", gotFwd.Origin)
	}
	if len(gotFwd.NotePages) != 1 || !mediaIsValid(gotFwd, gotFwd.NotePages[0].Image) {
		t.Fatalf("expected fromOrigin image preserved monotonically, got %+v", gotFwd)
	}
}

// Invariants #4 (Bilibili supplements) & #6 (Windows open-file lock rollback + fault-injection rollback + queue quota + UpgradeWithSource):
func TestMonotonicBilibiliUpgradeWindowsFileLockRollbackAndQueueQuota(t *testing.T) {
	v720, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	v1080, err := BuildSyntheticVideoM4S(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := BuildSyntheticAudioM4S(94)
	if err != nil {
		t.Fatal(err)
	}

	var maxQn atomic.Int32
	maxQn.Store(64) // initially 720P, later 80 (1080P) on same CID
	var failDanmakuAndSub atomic.Bool

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV19x411w7UP",
				"aid":      190001,
				"title":    "Upgrade Video",
				"desc":     "Same CID stream upgrade",
				"pic":      srvURL + "/media/cover.jpg",
				"pubdate":  1700000000,
				"duration": 4,
				"owner":    map[string]any{"mid": 888, "name": "Creator888", "face": srvURL + "/media/face.jpg"},
				"pages": []map[string]any{
					{"cid": 7001, "page": 1, "part": "P1", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
					{"cid": 7002, "page": 2, "part": "P2", "duration": 2, "dimension": map[string]any{"width": 16, "height": 16}},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		qn := int(maxQn.Load())
		videos := []map[string]any{
			{"id": 64, "baseUrl": srvURL + "/media/v720.m4s", "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 300000},
		}
		acceptQ := []int{80, 64}
		if qn >= 80 {
			videos = append([]map[string]any{
				{"id": 80, "baseUrl": srvURL + "/media/v1080.m4s", "codecid": 7, "codecs": "avc1.42c00a", "width": 16, "height": 16, "frameRate": "30.0", "bandwidth": 800000},
			}, videos...)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        qn,
				"accept_format":  "dash",
				"accept_quality": acceptQ,
				"dash": map[string]any{
					"duration": 2,
					"video":    videos,
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": srvURL + "/media/a.m4s", "codecs": "mp4a.40.2", "bandwidth": 192000},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		if failDanmakuAndSub.Load() {
			http.Error(w, "danmaku unavailable", http.StatusNotFound)
			return
		}
		_, _ = w.Write(bilibili.EncodeDmSegMobileReply([]bilibili.DanmakuElem{
			{ID: 1, IDStr: "1", Progress: 400, Mode: 1, Fontsize: 25, Color: 0xFFFFFF, Content: "Precious V1 Danmaku"},
		}))
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		cidInt, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
		if failDanmakuAndSub.Load() {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bvid": "BV19x411w7UP", "cid": cidInt, "subtitle": map[string]any{"subtitles": []any{}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV19x411w7UP",
				"cid":  cidInt,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{"id": 1, "lan": "zh-CN", "lan_doc": "中文", "subtitle_url": srvURL + "/media/sub.json"},
					},
				},
			},
		})
	})
	mux.HandleFunc("/media/sub.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": []map[string]any{{"from": 0.1, "to": 0.9, "content": "Precious V1 Subtitle"}},
		})
	})
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(syntheticJPEGMini())
	})
	mux.HandleFunc("/media/v720.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(v720)))
		_, _ = w.Write(v720)
	})
	mux.HandleFunc("/media/v1080.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(v1080)))
		_, _ = w.Write(v1080)
	})
	mux.HandleFunc("/media/a.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})

	mgr, libStore, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	dyPlanner := newFakeGenericWorkPlanner(site.Douyin)
	mgr.RegisterWorkPlanner(dyPlanner)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	ctx := context.Background()

	// 1. Cache 720P V1 with valid danmaku & subtitles.
	tV1, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV19x411w7UP", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tk := range tV1 {
		ids = append(ids, tk.TaskID)
	}
	if err := mgr.WaitTasks(ctx, ids...); err != nil {
		t.Fatal(err)
	}

	v1Meta, _, _ := libStore.GetItem("bilibili:video:BV19x411w7UP")
	origDM, err := os.ReadFile(filepath.Join(v1Meta.ItemDir, "P01.danmaku.xml"))
	if err != nil {
		t.Fatal(err)
	}
	origVTT, err := os.ReadFile(filepath.Join(v1Meta.ItemDir, "P01.zh-CN.vtt"))
	if err != nil {
		t.Fatal(err)
	}

	// 2a. Test Windows open-file lock rollback during ConflictResolveKeepBoth!
	// Hold an OS handle on P02.mp4 (without FILE_SHARE_DELETE on Windows) so P01.mp4 moves into .trace-prev-*
	// and P02.mp4 fails os.Rename -> rollback() must restore P01.mp4 AND delete the archived snapshot!
	if runtime.GOOS == "windows" {
		lockedP2, err := os.Open(filepath.Join(v1Meta.ItemDir, "P02.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		tLock, err := mgr.EnqueueVideo(WithConflictResolution(ctx, ConflictResolveKeepBoth), VideoEnqueueRequest{BVID: "BV19x411w7UP", Source: "manual"})
		if err != nil {
			lockedP2.Close()
			t.Fatal(err)
		}
		lockErr := mgr.WaitTasks(ctx, tLock[0].TaskID)
		_ = lockedP2.Close()
		if lockErr == nil {
			t.Fatalf("expected Windows file sharing violation error when P02.mp4 is open, got nil")
		}
		// P01.mp4 and P02.mp4 must both be present in itemDir, and no leftover .trace-prev-* or orphan archive!
		if _, err := os.Stat(filepath.Join(v1Meta.ItemDir, "P01.mp4")); err != nil {
			t.Fatalf("P01.mp4 was NOT rolled back after P02.mp4 lock failure: %v", err)
		}
		entries, _ := os.ReadDir(v1Meta.ItemDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), library.SwapPrevDirPrefix) || strings.HasPrefix(e.Name(), library.SwapStageDirPrefix) {
				t.Fatalf("leftover swap temp dir %q after rollback", e.Name())
			}
		}
		itemsAfterRollback, _ := libStore.ListItems(library.FeedFilter{Site: "bilibili"})
		for _, it := range itemsAfterRollback {
			if it.ArchivedOf != "" {
				t.Fatalf("orphan archived snapshot %s remained after rolled-back keep_both", it.ItemKey)
			}
		}
	}

	// 2b. Cross-platform partial-backup + keep_both archive rollback via testSwapHook on P02.mp4 (runs on Linux CI too!).
	testSwapHook = func(step, src, _ string) error {
		if step == "backup_v1" && strings.HasSuffix(filepath.ToSlash(src), "/P02.mp4") {
			return fmt.Errorf("injected sharing violation on P02.mp4")
		}
		return nil
	}
	tPartial, err := mgr.EnqueueVideo(WithConflictResolution(ctx, ConflictResolveKeepBoth), VideoEnqueueRequest{BVID: "BV19x411w7UP", Source: "manual"})
	if err != nil {
		testSwapHook = nil
		t.Fatal(err)
	}
	partialErr := mgr.WaitTasks(ctx, tPartial[0].TaskID)
	testSwapHook = nil
	if partialErr == nil {
		t.Fatalf("expected injected P02.mp4 backup_v1 failure")
	}
	if _, err := os.Stat(filepath.Join(v1Meta.ItemDir, "P01.mp4")); err != nil {
		t.Fatalf("P01.mp4 was NOT rolled back after partial backup_v1 failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v1Meta.ItemDir, "P02.mp4")); err != nil {
		t.Fatalf("P02.mp4 missing after partial backup_v1 failure: %v", err)
	}
	itemsAfterPartial, _ := libStore.ListItems(library.FeedFilter{Site: "bilibili"})
	for _, it := range itemsAfterPartial {
		if it.ArchivedOf != "" {
			t.Fatalf("orphan archived snapshot %s remained after rolled-back keep_both", it.ItemKey)
		}
	}

	// 3. Test deterministic testSwapHook fault injection at "save_manifest".
	testSwapHook = func(step, _, _ string) error {
		if step == "save_manifest" {
			return fmt.Errorf("injected manifest write failure")
		}
		return nil
	}
	tHook, err := mgr.EnqueueVideo(WithConflictResolution(ctx, ConflictResolveUseRemote), VideoEnqueueRequest{BVID: "BV19x411w7UP", Source: "manual"})
	if err != nil {
		testSwapHook = nil
		t.Fatal(err)
	}
	hookErr := mgr.WaitTasks(ctx, tHook[0].TaskID)
	testSwapHook = nil
	if hookErr == nil {
		t.Fatalf("expected injected save_manifest failure")
	}
	afterHookMeta, ok, err := libStore.GetItem("bilibili:video:BV19x411w7UP")
	if err != nil || !ok || afterHookMeta.Status != "complete" {
		t.Fatalf("V1 should remain complete after save_manifest rollback: %+v", afterHookMeta)
	}

	// 4. Enable 1080P (qn=80) while danmaku & subtitle endpoints fail, and run UpgradeWithSource(..., "queue")!
	// Monotonic protection MUST preserve V1 danmaku & subtitles while upgrading P01 & P02 to 1080P!
	maxQn.Store(80)
	failDanmakuAndSub.Store(true)
	upgTask, err := mgr.UpgradeWithSource(ctx, "bilibili:video:BV19x411w7UP", "queue")
	if err != nil {
		t.Fatalf("UpgradeWithSource: %v", err)
	}
	if upgTask.Source != "queue" {
		t.Fatalf("expected Source=queue, got %q", upgTask.Source)
	}
	if err := mgr.WaitTasks(ctx, upgTask.TaskID); err != nil {
		t.Fatalf("WaitTasks UpgradeWithSource: %v", err)
	}
	upgraded, _, _ := libStore.GetItem("bilibili:video:BV19x411w7UP")
	if upgraded.Parts[0].Video.Quality != 80 {
		t.Fatalf("expected upgraded quality 80, got %d", upgraded.Parts[0].Video.Quality)
	}
	dmAfter, err := os.ReadFile(filepath.Join(upgraded.ItemDir, "P01.danmaku.xml"))
	if err != nil || string(dmAfter) != string(origDM) {
		t.Fatalf("monotonic danmaku lost on upgrade: err=%v", err)
	}
	vttAfter, err := os.ReadFile(filepath.Join(upgraded.ItemDir, "P01.zh-CN.vtt"))
	if err != nil || string(vttAfter) != string(origVTT) {
		t.Fatalf("monotonic subtitle lost on upgrade: err=%v", err)
	}

	// 5. Verify Source == "queue" attaches site.AutomaticRequestSource(ctx) == "download" on WorkPlanner!
	progMP4 := buildProgressiveWithoutElst(t, true, false, 0)
	mux.HandleFunc("/media/dy.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(progMP4)))
		_, _ = w.Write(progMP4)
	})
	dyPlanner.setPlan("7310000000000000009", func(_ context.Context, _ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:        site.Douyin,
			Kind:        "video",
			TargetID:    "7310000000000000009",
			AuthorUID:   "MS4wLjABAAAA_test_author_sec_uid_009",
			AuthorName:  "DY9",
			Title:       "Queue Quota Check",
			MediaID:     "dy_vid_9",
			DurationSec: 2,
			Width:       16,
			Height:      16,
			VideoURLs:   []string{srvURL + "/media/dy.mp4"},
			Transport:   localhostTransport(site.Douyin),
		}, nil
	})
	tQueue, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{
		AwemeID: "7310000000000000009",
		Source:  "queue",
	})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork queue: %v", err)
	}
	if err := mgr.WaitTasks(ctx, tQueue.TaskID); err != nil {
		t.Fatalf("WaitTasks Douyin queue: %v", err)
	}
	if got := dyPlanner.lastAutomaticSource(); got != "download" {
		t.Fatalf("expected AutomaticRequestSource=download for Source=queue, got %q", got)
	}
}
