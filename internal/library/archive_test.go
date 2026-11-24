package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestArchiveItemSnapshotAndCollisionSuffixes(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatalf("OpenWithDataDir: %v", err)
	}

	// 1. Bilibili video with supplements + user-placed file.
	vidDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV17x411w7KC")
	if err := os.MkdirAll(vidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mp4Bytes := []byte("synthetic-v1-mp4-payload")
	dmBytes := []byte("<d>v1-danmaku</d>")
	vttBytes := []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nv1 sub\n")
	srtBytes := []byte("1\n00:00:00,000 --> 00:00:01,000\nv1 sub\n")
	coverBytes := []byte("v1-cover-jpeg")
	userNotes := []byte("my personal local notes")

	for name, data := range map[string][]byte{
		"P01.mp4":         mp4Bytes,
		"P01.danmaku.xml": dmBytes,
		"P01.zh-CN.vtt":   vttBytes,
		"P01.zh-CN.srt":   srtBytes,
		"cover.jpg":       coverBytes,
		"notes.txt":       userNotes,
	} {
		if err := os.WriteFile(filepath.Join(vidDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	origSaved, err := store.SaveItem(vidDir, ItemMetadata{
		Version:              1,
		ID:                   "video:BV17x411w7KC",
		Site:                 "bilibili",
		Kind:                 "video",
		Status:               "complete",
		Title:                "Original V1 Title",
		Desc:                 "Original V1 Desc",
		URL:                  "https://www.bilibili.com/video/BV17x411w7KC",
		PubTs:                1700000100,
		Duration:             12,
		DirectCached:         false,
		CacheStandardVersion: CurrentCacheStandardVersion,
		AssetIDs:             []string{"P01_5001"},
		Author:               ItemAuthor{Site: "bilibili", Mid: 1001, UID: "1001", Name: "Creator1001"},
		Media: []MediaFile{
			{Role: "cover", Path: "cover.jpg", Bytes: int64(len(coverBytes)), SHA256: sha256HexBytes(coverBytes)},
			{Role: "video", Path: "P01.mp4", Bytes: int64(len(mp4Bytes)), SHA256: sha256HexBytes(mp4Bytes)},
			{Role: "danmaku", Path: "P01.danmaku.xml", Bytes: int64(len(dmBytes)), SHA256: sha256HexBytes(dmBytes)},
			{Role: "subtitle", Path: "P01.zh-CN.vtt", Bytes: int64(len(vttBytes)), SHA256: sha256HexBytes(vttBytes)},
			{Role: "subtitle", Path: "P01.zh-CN.srt", Bytes: int64(len(srtBytes)), SHA256: sha256HexBytes(srtBytes)},
		},
		Parts: []VideoPartRecord{
			{
				CID:            5001,
				Page:           1,
				Title:          "Part 1",
				Duration:       12,
				File:           "P01.mp4",
				Danmaku:        "P01.danmaku.xml",
				DanmakuState:   "saved",
				Subtitles:      []string{"P01.zh-CN.vtt", "P01.zh-CN.srt"},
				SubtitleState:  "saved",
				SubtitleTracks: []PartSubtitleInfo{{Language: "zh-CN", Name: "中文", VTT: "P01.zh-CN.vtt", SRT: "P01.zh-CN.srt"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem orig: %v", err)
	}

	archiveTs := time.Now().UTC().Truncate(time.Second)
	unixTs := archiveTs.Unix()

	arch1, err := store.ArchiveItemSnapshot(origSaved.ItemKey, archiveTs)
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot #1: %v", err)
	}
	wantRaw1 := fmt.Sprintf("BV17x411w7KC__v1-%d", unixTs)
	if arch1.RawID != wantRaw1 || arch1.ID != "video:"+wantRaw1 {
		t.Fatalf("arch1 ID=%q RawID=%q, want video:%s", arch1.ID, arch1.RawID, wantRaw1)
	}
	if arch1.ArchivedOf != "bilibili:video:BV17x411w7KC" {
		t.Fatalf("arch1.ArchivedOf = %q, want bilibili:video:BV17x411w7KC", arch1.ArchivedOf)
	}
	if !arch1.DirectCached || arch1.Status != "complete" {
		t.Fatalf("arch1 DirectCached=%v Status=%q, want true/complete", arch1.DirectCached, arch1.Status)
	}
	if arch1.URL != "https://www.bilibili.com/video/BV17x411w7KC" {
		t.Fatalf("arch1.URL = %q", arch1.URL)
	}
	if filepath.Base(arch1.ItemDir) != wantRaw1 || filepath.Dir(arch1.ItemDir) != filepath.Dir(vidDir) {
		t.Fatalf("arch1.ItemDir = %q, want sibling of %q", arch1.ItemDir, vidDir)
	}
	// User-placed file is not copied into the registered archive snapshot.
	if _, err := os.Stat(filepath.Join(arch1.ItemDir, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected notes.txt in archive dir: %v", err)
	}
	gotMP4, err := os.ReadFile(filepath.Join(arch1.ItemDir, "P01.mp4"))
	if err != nil || string(gotMP4) != string(mp4Bytes) {
		t.Fatalf("archived P01.mp4 mismatch: err=%v", err)
	}

	// Colliding archive timestamps append -2 and -3 without clobbering #1.
	arch2, err := store.ArchiveItemSnapshot(origSaved.ItemKey, archiveTs)
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot #2: %v", err)
	}
	wantRaw2 := fmt.Sprintf("BV17x411w7KC__v1-%d-2", unixTs)
	if arch2.RawID != wantRaw2 || arch2.ArchivedOf != origSaved.ItemKey {
		t.Fatalf("arch2 = %+v, want RawID=%s", arch2, wantRaw2)
	}
	arch3, err := store.ArchiveItemSnapshot(origSaved.ItemKey, archiveTs)
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot #3: %v", err)
	}
	wantRaw3 := fmt.Sprintf("BV17x411w7KC__v1-%d-3", unixTs)
	if arch3.RawID != wantRaw3 {
		t.Fatalf("arch3.RawID = %q, want %q", arch3.RawID, wantRaw3)
	}

	// 2. Bilibili dynamic with orig.json + Weibo post with NotePages/Origin/Via/EditCount.
	dynDir := filepath.Join(cacheDir, "bilibili", "1001", "dynamics", "88990011")
	if err := os.MkdirAll(filepath.Join(dynDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	imgBytes := []byte("dyn-img-1")
	origJSONBytes := []byte(`{"id":"776655","text":"origin snapshot"}`)
	if err := os.WriteFile(filepath.Join(dynDir, "images", "01.jpg"), imgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dynDir, "orig.json"), origJSONBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	dynSaved, err := store.SaveItem(dynDir, ItemMetadata{
		Version: 1,
		ID:      "dynamic:88990011",
		Site:    "bilibili",
		Kind:    "dynamic",
		Status:  "complete",
		Title:   "Dyn V1",
		Text:    "Dyn V1 Body",
		PubTs:   1700000200,
		Author:  ItemAuthor{Site: "bilibili", Mid: 1001, UID: "1001", Name: "Creator1001"},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(imgBytes)), SHA256: sha256HexBytes(imgBytes)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dynArch, err := store.ArchiveItemSnapshot(dynSaved.ItemKey, archiveTs)
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot dynamic: %v", err)
	}
	gotOrigSnap, err := os.ReadFile(filepath.Join(dynArch.ItemDir, "orig.json"))
	if err != nil || string(gotOrigSnap) != string(origJSONBytes) {
		t.Fatalf("archived orig.json mismatch: err=%v got=%q", err, string(gotOrigSnap))
	}
}

func TestArchiveIndependentDeleteAndCountsAndUpgrades(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cacheDir := filepath.Join(tmp, "cache")
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		t.Fatal(err)
	}

	archiveTs := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)

	// Save live items across sites (including upgradeable, collab, and forward) and archive each.
	vidDir := filepath.Join(cacheDir, "bilibili", "2002", "videos", "BV1upgr")
	_ = os.MkdirAll(vidDir, 0o700)
	v1Data := []byte("v1-video-bytes")
	_ = os.WriteFile(filepath.Join(vidDir, "P01.mp4"), v1Data, 0o600)
	liveVid, err := store.SaveItem(vidDir, ItemMetadata{
		Version:          1,
		ID:               "video:BV1upgr",
		Site:             "bilibili",
		Kind:             "video",
		Status:           "complete",
		Title:            "V1 Upgradeable",
		PubTs:            1700001000,
		UpgradeAvailable: true,
		UpgradeHint:      "有更高画质",
		Author:           ItemAuthor{Site: "bilibili", Mid: 2002, UID: "2002", Name: "UP2002"},
		Media:            []MediaFile{{Role: "video", Path: "P01.mp4", Bytes: int64(len(v1Data)), SHA256: sha256HexBytes(v1Data)}},
		Parts:            []VideoPartRecord{{CID: 101, Page: 1, File: "P01.mp4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archVid, err := store.ArchiveItemSnapshot(liveVid.ItemKey, archiveTs)
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot liveVid: %v", err)
	}

	// Forwarded Weibo post under forwarder 3003.
	wbDir := filepath.Join(cacheDir, "weibo", "3003", "forwards", "5099887766554433")
	_ = os.MkdirAll(filepath.Join(wbDir, "images"), 0o700)
	wbImg := []byte("weibo-v1-img")
	_ = os.WriteFile(filepath.Join(wbDir, "images", "01.jpg"), wbImg, 0o600)
	liveWB, err := store.SaveItem(wbDir, ItemMetadata{
		Version:      1,
		ID:           "dynamic:5099887766554433",
		Site:         "weibo",
		Kind:         "dynamic",
		Status:       "complete",
		Title:        "Weibo Forward V1",
		Text:         "Forward comment V1",
		PubTs:        1700002000,
		EditCount:    1,
		AssetIDs:     []string{"pic_v1"},
		DirectCached: true,
		Author:       ItemAuthor{Site: "weibo", UID: "3003", Name: "Forwarder3003"},
		Via:          &ItemVia{Reason: ViaForward, Site: "weibo", UID: "3003", Name: "Forwarder3003", DynamicID: "5099887766554433"},
		Origin:       &ItemOrigin{Site: "weibo", ID: "5011111111111111", Text: "Origin Text"},
		Media:        []MediaFile{{Role: "image", Path: "images/01.jpg", Bytes: int64(len(wbImg)), SHA256: sha256HexBytes(wbImg)}},
		NotePages:    []NotePageRecord{{Index: 1, Kind: "image", MediaID: "pic_v1", Image: "images/01.jpg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archWB, err := store.ArchiveItemSnapshot(liveWB.ItemKey, archiveTs)
	if err != nil {
		t.Fatalf("ArchiveItemSnapshot liveWB: %v", err)
	}

	// 1. CountCachedWorks must exclude archived snapshots (counts stay 1, never 2).
	counts, err := store.CountCachedWorks([]AuthorRef{
		{Site: "bilibili", UID: "2002"},
		{Site: "weibo", UID: "3003"},
	})
	if err != nil {
		t.Fatalf("CountCachedWorks: %v", err)
	}
	if counts["bilibili:2002"] != 1 || counts["weibo:3003"] != 1 {
		t.Fatalf("CountCachedWorks double-counted archived snapshots: %+v", counts)
	}

	// 2. UpgradeCandidates and CheckUpgradeKeys must exclude archived snapshots.
	cands, err := store.UpgradeCandidates(50)
	if err != nil {
		t.Fatalf("UpgradeCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0].ItemKey != liveVid.ItemKey {
		t.Fatalf("UpgradeCandidates = %+v, want only %s", cands, liveVid.ItemKey)
	}

	var checkedBVIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkedBVIDs = append(checkedBVIDs, r.URL.Query().Get("bvid"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "dash",
				"accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 5,
					"video": []map[string]any{
						{"id": 80, "baseUrl": "https://upos-sz-mirrorcos.bilivideo.com/v.m4s", "codecid": 7, "codecs": "avc1", "width": 1920, "height": 1080},
					},
					"audio": []map[string]any{
						{"id": 30280, "baseUrl": "https://upos-sz-mirrorcos.bilivideo.com/a.m4s", "codecs": "mp4a"},
					},
				},
			},
		})
	}))
	defer srv.Close()
	credStore, _, err := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = credStore.SaveManual("SESSDATA=s; bili_jct=c; DedeUserID=1001", "rt")
	biliClient := bilibili.NewClient(bilibili.ClientOptions{
		CredStore: credStore,
		Endpoints: bilibili.Endpoints{
			APIBase: srv.URL, PassportBase: srv.URL, WWWBase: srv.URL,
			SpaceBase: srv.URL, DynamicBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL,
		},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	biliClient.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	upgItems, err := store.CheckUpgradeKeys(context.Background(), biliClient, []string{liveVid.ItemKey, archVid.ItemKey}, true, bilibili.PlaySelectionOptions{})
	if err != nil {
		t.Fatalf("CheckUpgradeKeys: %v", err)
	}
	if len(upgItems) != 1 || upgItems[0].ItemKey != liveVid.ItemKey || len(checkedBVIDs) != 1 || checkedBVIDs[0] != "BV1upgr" {
		t.Fatalf("CheckUpgradeKeys should only inspect live item, got items=%+v checkedBVIDs=%v", upgItems, checkedBVIDs)
	}

	// 3. Deleting live V2 must NOT delete or corrupt archived V1.
	planLive, err := store.PlanDelete(liveVid.ItemKey)
	if err != nil {
		t.Fatalf("PlanDelete liveVid: %v", err)
	}
	if len(planLive.ItemKeys) != 1 || planLive.ItemKeys[0] != liveVid.ItemKey {
		t.Fatalf("expected liveVid delete plan for single key, got %+v", planLive)
	}
	if _, err := store.Delete(liveVid.ItemKey); err != nil {
		t.Fatalf("Delete liveVid: %v", err)
	}
	if _, ok, _ := store.GetItem(liveVid.ItemKey); ok {
		t.Fatalf("expected liveVid removed from DB")
	}
	gotArchVid, ok, err := store.GetItem(archVid.ItemKey)
	if err != nil || !ok || gotArchVid.Status != "complete" {
		t.Fatalf("archived V1 should remain complete after deleting live V2: ok=%v status=%q err=%v", ok, gotArchVid.Status, err)
	}
	if _, err := os.Stat(filepath.Join(archVid.ItemDir, "P01.mp4")); err != nil {
		t.Fatalf("archived V1 P01.mp4 missing after deleting live V2: %v", err)
	}

	// 4. Deleting archived V1 must NOT delete or corrupt live V2.
	if _, err := store.Delete(archWB.ItemKey); err != nil {
		t.Fatalf("Delete archWB: %v", err)
	}
	if _, ok, _ := store.GetItem(archWB.ItemKey); ok {
		t.Fatalf("expected archWB removed from DB")
	}
	gotLiveWB, ok, err := store.GetItem(liveWB.ItemKey)
	if err != nil || !ok || gotLiveWB.Status != "complete" {
		t.Fatalf("live Weibo item should remain complete after deleting archived V1: ok=%v status=%q err=%v", ok, gotLiveWB.Status, err)
	}
	if _, err := os.Stat(filepath.Join(liveWB.ItemDir, "images", "01.jpg")); err != nil {
		t.Fatalf("live Weibo image missing after deleting archived V1: %v", err)
	}
}

func TestRecoverInterruptedItemSwapStartupAndRefresh(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "library.db")
	cacheDir := filepath.Join(tmp, "cache")

	db, _, _, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenWithDataDir(db, cacheDir, tmp)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}

	itemDir := filepath.Join(cacheDir, "douyin", "sec_u1", "videos", "7300000000000000001")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		db.Close()
		t.Fatal(err)
	}
	v1MP4 := []byte("intact-v1-video-payload")
	v1Cover := []byte("intact-v1-cover-payload")
	if err := os.WriteFile(filepath.Join(itemDir, "video.mp4"), v1MP4, 0o600); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "cover.jpg"), v1Cover, 0o600); err != nil {
		db.Close()
		t.Fatal(err)
	}
	saved, err := store.SaveItem(itemDir, ItemMetadata{
		Version:     1,
		ID:          "video:7300000000000000001",
		Site:        "douyin",
		Kind:        "video",
		Status:      "complete",
		Title:       "Douyin Crash Recovery",
		PubTs:       1700003000,
		Duration:    5,
		Author:      ItemAuthor{Site: "douyin", UID: "sec_u1", Name: "CreatorDY"},
		DouyinVideo: &DouyinVideoInfo{MediaID: "vid_v1", File: "video.mp4", Width: 1080, Height: 1920},
		Media: []MediaFile{
			{Role: "cover", Path: "cover.jpg", Bytes: int64(len(v1Cover)), SHA256: sha256HexBytes(v1Cover)},
			{Role: "video", Path: "video.mp4", Bytes: int64(len(v1MP4)), SHA256: sha256HexBytes(v1MP4)},
		},
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}

	// Case A: Direct RecoverInterruptedItemSwap restores verified .trace-prev-* and removes .trace-stage-*.
	prevDir := filepath.Join(itemDir, SwapPrevDirPrefix+"crashA")
	stageDir := filepath.Join(itemDir, SwapStageDirPrefix+"crashA")
	_ = os.MkdirAll(prevDir, 0o700)
	_ = os.MkdirAll(stageDir, 0o700)
	if err := os.Rename(filepath.Join(itemDir, "video.mp4"), filepath.Join(prevDir, "video.mp4.part")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(stageDir, "video.mp4.part"), []byte("uncommitted-v2"), 0o600)

	if err := RecoverInterruptedItemSwap(itemDir, saved); err != nil {
		db.Close()
		t.Fatalf("RecoverInterruptedItemSwap: %v", err)
	}
	restoredBytes, err := os.ReadFile(filepath.Join(itemDir, "video.mp4"))
	if err != nil || string(restoredBytes) != string(v1MP4) {
		db.Close()
		t.Fatalf("restored video.mp4 mismatch: err=%v got=%q", err, string(restoredBytes))
	}
	if _, err := os.Stat(prevDir); !os.IsNotExist(err) {
		db.Close()
		t.Fatalf("expected prevDir removed, stat=%v", err)
	}
	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		db.Close()
		t.Fatalf("expected stageDir removed, stat=%v", err)
	}

	// Case B: Corrupt .trace-prev-* file with wrong SHA-256 must NOT be restored.
	corruptPrev := filepath.Join(itemDir, SwapPrevDirPrefix+"corrupt")
	_ = os.MkdirAll(corruptPrev, 0o700)
	_ = os.Remove(filepath.Join(itemDir, "video.mp4"))
	_ = os.WriteFile(filepath.Join(corruptPrev, "video.mp4.part"), []byte("tampered-bytes"), 0o600)
	if err := RecoverInterruptedItemSwap(itemDir, saved); err != nil {
		db.Close()
		t.Fatalf("RecoverInterruptedItemSwap corrupt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(itemDir, "video.mp4")); !os.IsNotExist(err) {
		db.Close()
		t.Fatalf("corrupt backup should NOT be restored to live path")
	}
	// Put valid video.mp4 back and test startup OpenWithDataDir recovery for cover.jpg.
	_ = os.WriteFile(filepath.Join(itemDir, "video.mp4"), v1MP4, 0o600)
	bootPrev := filepath.Join(itemDir, SwapPrevDirPrefix+"boot")
	_ = os.MkdirAll(bootPrev, 0o700)
	if err := os.Rename(filepath.Join(itemDir, "cover.jpg"), filepath.Join(bootPrev, "cover.jpg.part")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	db2, _, _, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	store2, err := OpenWithDataDir(db2, cacheDir, tmp)
	if err != nil {
		t.Fatalf("reopen OpenWithDataDir: %v", err)
	}
	gotAfterBoot, ok, err := store2.GetItem(saved.ItemKey)
	if err != nil || !ok || gotAfterBoot.Status != "complete" || len(gotAfterBoot.Missing) != 0 {
		t.Fatalf("expected complete item after boot recovery, got %+v ok=%v err=%v", gotAfterBoot, ok, err)
	}
	if _, err := os.Stat(bootPrev); !os.IsNotExist(err) {
		t.Fatalf("expected bootPrev cleaned up on startup, stat=%v", err)
	}
}
