package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestXKeysDirectoriesSanitizationRebuildCountsAndMediaFilters(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := filepath.Join(dataDir, "cache")
	db, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, cacheDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify library.db stays on schema version 2 (no unnecessary bump for X).
	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if CurrentSchemaVersion != 2 {
		t.Fatalf("CurrentSchemaVersion = %d, want 2", CurrentSchemaVersion)
	}
	if userVersion != CurrentSchemaVersion {
		t.Fatalf("library.db user_version = %d, want %d", userVersion, CurrentSchemaVersion)
	}

	// 1. NormalizeRawID, CanonicalItemKey, NormalizeAuthorUID, and X directory helpers.
	if got := NormalizeRawID("dynamic", "x:dynamic:1000000000000000009"); got != "1000000000000000009" {
		t.Fatalf("NormalizeRawID(x dynamic) = %q, want 1000000000000000009", got)
	}
	if got := NormalizeRawID("dynamic", "x:1000000000000000009"); got != "1000000000000000009" {
		t.Fatalf("NormalizeRawID(x bare) = %q, want 1000000000000000009", got)
	}
	if got := CanonicalItemKey("x", "dynamic", "x:dynamic:1000000000000000009"); got != "x:dynamic:1000000000000000009" {
		t.Fatalf("CanonicalItemKey(x dynamic) = %q", got)
	}
	if ns, nu := NormalizeAuthorUID("X", "0001000000000000000001", 0); ns != "x" || nu != "1000000000000000001" {
		t.Fatalf("NormalizeAuthorUID(x) = (%q, %q), want (x, 1000000000000000001)", ns, nu)
	}
	for _, badUID := range []string{"", "0", "000", "abc", "-1", "18446744073709551616", "../evil"} {
		if _, err := XCreatorDir(cacheDir, badUID); err == nil {
			t.Fatalf("XCreatorDir(%q) expected error, got nil", badUID)
		}
	}
	if _, err := XPostDir(cacheDir, "1000000000000000001", "../escape", false); err == nil {
		t.Fatal("XPostDir traversal should fail")
	}

	const (
		uidAuthor    = "1000000000000000001"
		uidForwarder = "1000000000000000002"
		uidOrigin    = "1000000000000000003"
	)

	// 2. Save an X single-video post (itemKey = x:dynamic:<status_id>) with X secrets/signed URLs to scrub.
	vidPostDir, err := XPostDir(cacheDir, "00"+uidAuthor, "1000000000000000011", false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(vidPostDir) != filepath.Clean(filepath.Join(cacheDir, "x", uidAuthor, "posts", "1000000000000000011")) {
		t.Fatalf("XPostDir = %q", vidPostDir)
	}
	if err := os.MkdirAll(vidPostDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"video.mp4": "synthetic-x-video",
		"cover.jpg": "synthetic-x-cover",
	} {
		if err := os.WriteFile(filepath.Join(vidPostDir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	savedVidPost, err := store.SaveItem(vidPostDir, ItemMetadata{
		Version:     1,
		ID:          "x:dynamic:1000000000000000011",
		Site:        "x",
		Kind:        "dynamic",
		Title:       "合成 X 视频帖",
		Text:        "合成 X 视频正文",
		DynamicType: "video",
		PubTs:       1763800100,
		Author: ItemAuthor{
			Site:     "x",
			UID:      "000" + uidAuthor,
			UniqueID: "synthetic_creator",
			Name:     "合成创作者一",
			Face:     "https://pbs.twimg.com/profile_images/1000000000000000001/avatar.jpg?auth_token=XSECRET_AUTH01&ct0=XSECRET_CT001",
		},
		Media: []MediaFile{
			{Role: "cover", Path: "cover.jpg", Source: "https://pbs.twimg.com/ext_tw_video_thumb/1/thumb.jpg?token=XSIG01"},
			{Role: "video", Path: "video.mp4", Source: "https://video.twimg.com/ext_tw_video/1/pu/vid/avc1/1280x720/clip.mp4?tag=12&token=XSIG02"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem x video post: %v", err)
	}
	if savedVidPost.ItemKey != "x:dynamic:1000000000000000011" || savedVidPost.Author.UID != uidAuthor || savedVidPost.Author.Site != "x" {
		t.Fatalf("unexpected saved x video post: %+v", savedVidPost)
	}
	if savedVidPost.URL != "https://x.com/i/web/status/1000000000000000011" {
		t.Fatalf("expected fallback X canonical URL, got %q", savedVidPost.URL)
	}
	if savedVidPost.Author.Face != "" {
		t.Fatalf("signed X author face must be scrubbed, got %q", savedVidPost.Author.Face)
	}
	rawVidManifest, err := os.ReadFile(filepath.Join(vidPostDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"XSECRET_AUTH01", "XSECRET_CT001", "XSIG01", "XSIG02", "video.twimg.com", "pbs.twimg.com"} {
		if strings.Contains(string(rawVidManifest), forbidden) {
			t.Fatalf("item.json leaked forbidden X URL/secret %q:\n%s", forbidden, rawVidManifest)
		}
	}

	// 3. Save an X multi-image + long note_tweet + author self-reply thread post.
	imgPostDir, err := XPostDir(cacheDir, uidAuthor, "1000000000000000012", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(imgPostDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"images/01.jpg": "x-img-01",
		"images/02.png": "x-img-02",
	} {
		if err := os.WriteFile(filepath.Join(imgPostDir, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	savedImgPost, err := store.SaveItem(imgPostDir, ItemMetadata{
		Version:     1,
		ID:          "1000000000000000012",
		Site:        "x",
		Kind:        "dynamic",
		Title:       "合成 X 多图线程帖",
		Text:        "根帖完整长文\n\n作者自回复线程第二段",
		DynamicType: "draw",
		URL:         "https://x.com/synthetic_creator/status/1000000000000000012?s=20&t=track",
		PubTs:       1763800200,
		Author: ItemAuthor{
			Site:     "x",
			UID:      uidAuthor,
			UniqueID: "synthetic_creator",
			Name:     "合成创作者一",
			Face:     "https://pbs.twimg.com/profile_images/1000000000000000001/avatar_400x400.jpg",
		},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg"},
			{Role: "image", Path: "images/02.png"},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg"},
			{Index: 2, Kind: "image", Image: "images/02.png"},
		},
		Thread: []ItemThreadPart{
			{ID: "1000000000000000013", PubTs: 1763800210, Text: "作者自回复线程第二段"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem x image+thread post: %v", err)
	}
	if savedImgPost.URL != "https://x.com/synthetic_creator/status/1000000000000000012" {
		t.Fatalf("clean X URL with query should strip query only, got %q", savedImgPost.URL)
	}
	if savedImgPost.Author.Face != "https://pbs.twimg.com/profile_images/1000000000000000001/avatar_400x400.jpg" {
		t.Fatalf("clean pbs.twimg.com avatar should be preserved, got %q", savedImgPost.Author.Face)
	}
	if len(savedImgPost.Thread) != 1 || savedImgPost.Thread[0].ID != "1000000000000000013" {
		t.Fatalf("expected persisted thread segment, got %+v", savedImgPost.Thread)
	}

	// 4. Save an X pure GIF post (images/01.mp4 with role="video", gif=true -> appears in "image", NOT "video").
	gifPostDir, err := XPostDir(cacheDir, uidAuthor, "1000000000000000014", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gifPostDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"images/01.jpg": "x-gif-poster",
		"images/01.mp4": "x-gif-mp4",
	} {
		if err := os.WriteFile(filepath.Join(gifPostDir, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveItem(gifPostDir, ItemMetadata{
		Version:     1,
		ID:          "1000000000000000014",
		Site:        "x",
		Kind:        "dynamic",
		Title:       "合成 X 纯 GIF 帖",
		Text:        "只有动图",
		DynamicType: "draw",
		PubTs:       1763800300,
		Author:      ItemAuthor{Site: "x", UID: uidAuthor, Name: "合成创作者一"},
		Media: []MediaFile{
			{Role: "cover", Path: "images/01.jpg"},
			{Role: "video", Path: "images/01.mp4", GIF: true},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "video", GIF: true, Cover: "images/01.jpg", Video: "images/01.mp4"},
		},
	}); err != nil {
		t.Fatalf("SaveItem x gif post: %v", err)
	}

	// 5. Save an X mixed-media post (photo + GIF + real video -> appears in BOTH "image" and "video").
	mixPostDir, err := XPostDir(cacheDir, uidAuthor, "1000000000000000015", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mixPostDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"images/01.jpg": "mix-photo",
		"images/02.jpg": "mix-gif-cover",
		"images/02.mp4": "mix-gif-mp4",
		"images/03.jpg": "mix-vid-cover",
		"images/03.mp4": "mix-vid-mp4",
	} {
		if err := os.WriteFile(filepath.Join(mixPostDir, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveItem(mixPostDir, ItemMetadata{
		Version:     1,
		ID:          "1000000000000000015",
		Site:        "x",
		Kind:        "dynamic",
		Title:       "合成 X 混排帖",
		Text:        "图文动图视频混排",
		DynamicType: "draw",
		PubTs:       1763800400,
		Author:      ItemAuthor{Site: "x", UID: uidAuthor, Name: "合成创作者一"},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg"},
			{Role: "cover", Path: "images/02.jpg"},
			{Role: "video", Path: "images/02.mp4", GIF: true},
			{Role: "cover", Path: "images/03.jpg"},
			{Role: "video", Path: "images/03.mp4"},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg"},
			{Index: 2, Kind: "video", GIF: true, Cover: "images/02.jpg", Video: "images/02.mp4"},
			{Index: 3, Kind: "video", Cover: "images/03.jpg", Video: "images/03.mp4"},
		},
	}); err != nil {
		t.Fatalf("SaveItem x mixed post: %v", err)
	}

	// 6. Save an X retweet/quote under forwarder's forwards/ folder with via (DEC-056).
	fwdDir, err := XPostDir(cacheDir, uidForwarder, "1000000000000000016", true)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(fwdDir) != filepath.Clean(filepath.Join(cacheDir, "x", uidForwarder, "forwards", "1000000000000000016")) {
		t.Fatalf("XPostDir(forward) = %q", fwdDir)
	}
	if err := os.MkdirAll(filepath.Join(fwdDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fwdDir, "images", "01.jpg"), []byte("fwd-img"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(fwdDir, ItemMetadata{
		Version:     1,
		ID:          "1000000000000000016",
		Site:        "x",
		Kind:        "dynamic",
		Title:       "转帖测试",
		Text:        "转帖评语",
		DynamicType: "forward",
		PubTs:       1763800500,
		Author:      ItemAuthor{Site: "x", UID: uidOrigin, Name: "原推作者"},
		Via:         &ItemVia{Reason: ViaForward, Site: "x", UID: uidForwarder, Name: "转发者二", DynamicID: "1000000000000000016"},
		Origin: &ItemOrigin{
			Site:              "x",
			ID:                "1000000000000000099",
			Author:            ItemAuthor{Site: "x", UID: uidOrigin, Name: "原推作者"},
			Unavailable:       true,
			UnavailableReason: "原帖已删除",
		},
		Media: []MediaFile{{Role: "image", Path: "images/01.jpg", FromOrigin: true}},
	}); err != nil {
		t.Fatalf("SaveItem x forward post: %v", err)
	}

	// 7. External cache folder containing x/<rest_id>/posts/<id>/item.json & forwards/<id>/item.json for AddLibraryRoot + RebuildIndex.
	extRoot := t.TempDir()
	extPostDir, err := XPostDir(extRoot, uidAuthor, "1000000000000000017", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extPostDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extPostDir, "video.mp4"), []byte("ext-x-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	extManifest := `{"version":1,"id":"1000000000000000017","site":"x","kind":"dynamic","title":"外部目录 X 视频","pubTs":1763800600,"author":{"site":"x","uid":"1000000000000000001","name":"合成创作者一"},"media":[{"role":"video","path":"video.mp4"}]}`
	if err := os.WriteFile(filepath.Join(extPostDir, "item.json"), []byte(extManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	extFwdDir, err := XPostDir(extRoot, uidForwarder, "1000000000000000018", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extFwdDir, 0o700); err != nil {
		t.Fatal(err)
	}
	extFwdManifest := `{"version":1,"id":"1000000000000000018","site":"x","kind":"dynamic","dynamicType":"forward","title":"外部目录 X 引用帖","text":"引用外层正文","pubTs":1763800700,"author":{"site":"x","uid":"1000000000000000002","name":"转发者二"},"via":{"reason":"forward","site":"x","uid":"1000000000000000002","name":"转发者二","dynamicId":"1000000000000000018"}}`
	if err := os.WriteFile(filepath.Join(extFwdDir, "item.json"), []byte(extFwdManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	addRep, err := store.AddLibraryRoot(extRoot)
	if err != nil || addRep.Added != 2 {
		t.Fatalf("AddLibraryRoot(x extRoot) = %+v err=%v, want Added=2", addRep, err)
	}
	dots := store.States([]string{"x:dynamic:1000000000000000011", "x:dynamic:1000000000000000017"})
	if dots["x:dynamic:1000000000000000011"].Dot != "green" || dots["x:dynamic:1000000000000000017"].Dot != "green" {
		t.Fatalf("unexpected X dot states: %+v", dots)
	}

	// 8. RebuildIndex recovers all 7 X items across cacheDir + extRoot.
	rebuildRep, err := store.RebuildIndex()
	if err != nil || rebuildRep.Indexed != 7 {
		t.Fatalf("RebuildIndex = %+v err=%v, want Indexed=7", rebuildRep, err)
	}
	reloadedGIF, ok, err := store.GetItem("x:dynamic:1000000000000000014")
	if err != nil || !ok || len(reloadedGIF.NotePages) != 1 || !reloadedGIF.NotePages[0].GIF {
		t.Fatalf("GIF flag lost after RebuildIndex: ok=%v err=%v meta=%+v", ok, err, reloadedGIF)
	}
	reloadedThread, ok, err := store.GetItem("x:dynamic:1000000000000000012")
	if err != nil || !ok || len(reloadedThread.Thread) != 1 || reloadedThread.Thread[0].Text != "作者自回复线程第二段" {
		t.Fatalf("Thread lost after RebuildIndex: ok=%v err=%v meta=%+v", ok, err, reloadedThread)
	}

	// 9. CountCachedWorks counts X posts accurately (author=5, forwarder=2, phantom origin=0).
	counts, err := store.CountCachedWorks([]AuthorRef{
		{Site: "x", UID: uidAuthor},
		{Site: "x", UID: uidForwarder},
		{Site: "x", UID: uidOrigin},
	})
	if err != nil {
		t.Fatalf("CountCachedWorks(x): %v", err)
	}
	if counts["x:"+uidAuthor] != 5 || counts["x:"+uidForwarder] != 2 || counts["x:"+uidOrigin] != 0 {
		t.Fatalf("unexpected X CountCachedWorks: %+v, want author=5 forwarder=2 origin=0", counts)
	}

	// 10. Media library filters ("image", "video", "forward") with Site="x" and ExcludeForward.
	imgItems, err := store.ListItems(FeedFilter{Site: "x", Kind: "image", ExcludeForward: true})
	if err != nil || len(imgItems) != 3 {
		t.Fatalf("x image filter (ExcludeForward) = %d items (%+v) err=%v, want 3 (multi-image + pure GIF + mixed)", len(imgItems), imgItems, err)
	}
	vidItems, err := store.ListItems(FeedFilter{Site: "x", Kind: "video", ExcludeForward: true})
	if err != nil || len(vidItems) != 3 {
		t.Fatalf("x video filter (ExcludeForward) = %d items (%+v) err=%v, want 3 (single video + mixed + ext video, NOT pure GIF)", len(vidItems), vidItems, err)
	}
	for _, it := range vidItems {
		if it.ItemKey == "x:dynamic:1000000000000000014" {
			t.Fatalf("pure GIF item %s should not match video filter", it.ItemKey)
		}
	}
	fwdItems, err := store.ListItems(FeedFilter{Site: "x", Kind: "forward"})
	if err != nil || len(fwdItems) != 2 {
		t.Fatalf("x forward filter = %d items (%+v) err=%v, want 2", len(fwdItems), fwdItems, err)
	}

	// 11. UpsertFeedEntry URL sanitization for "x": allows clean pbs.twimg.com image URLs, strips video/signed URLs.
	if err := store.UpsertFeedEntry(FeedEntry{
		Site:         "x",
		Kind:         "dynamic",
		RawID:        "1000000000000000021",
		AuthorUID:    "00" + uidAuthor,
		AuthorName:   "合成创作者一",
		AuthorFace:   "https://pbs.twimg.com/profile_images/1/face_400x400.jpg",
		CoverURL:     "https://pbs.twimg.com/media/SynthClean01?format=jpg&name=orig",
		CanonicalURL: "https://x.com/synthetic_creator/status/1000000000000000021?s=20",
		Title:        "干净公开图",
		PubTs:        1763801000,
	}); err != nil {
		t.Fatalf("UpsertFeedEntry clean x: %v", err)
	}
	if err := store.UpsertFeedEntry(FeedEntry{
		Site:         "x",
		Kind:         "dynamic",
		RawID:        "1000000000000000022",
		AuthorUID:    uidAuthor,
		AuthorName:   "合成创作者一",
		AuthorFace:   "https://pbs.twimg.com/profile_images/1/face.jpg?auth_token=LEAKED_FACE_AUTH",
		CoverURL:     "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/secret.mp4?tag=12&token=LEAKED_VID_TOK",
		CanonicalURL: "",
		Title:        "签名视频地址应剥离",
		PubTs:        1763801100,
	}); err != nil {
		t.Fatalf("UpsertFeedEntry signed x: %v", err)
	}
	feedRows, err := store.QueryFeed(FeedFilter{Site: "x", Limit: 50})
	if err != nil {
		t.Fatalf("QueryFeed(x): %v", err)
	}
	byKey := make(map[string]FeedEntry, len(feedRows))
	for _, r := range feedRows {
		byKey[r.FeedKey] = r
	}
	cleanEntry := byKey["x:dynamic:1000000000000000021"]
	if cleanEntry.CoverURL != "https://pbs.twimg.com/media/SynthClean01?format=jpg&name=orig" ||
		cleanEntry.AuthorFace != "https://pbs.twimg.com/profile_images/1/face_400x400.jpg" ||
		cleanEntry.AuthorUID != uidAuthor {
		t.Fatalf("clean X feed entry unexpectedly modified: %+v", cleanEntry)
	}
	signedEntry := byKey["x:dynamic:1000000000000000022"]
	if signedEntry.CoverURL != "" || signedEntry.AuthorFace != "" || signedEntry.CanonicalURL != "https://x.com/i/web/status/1000000000000000022" {
		t.Fatalf("signed X feed entry not sanitized: %+v", signedEntry)
	}

	// 12. Feed attribution: forwarder gets forwarded posts, phantom origin author gets 0.
	fwdFeed, err := store.FeedPage(FeedPageRequest{
		AuthorRefs: []AuthorRef{{Site: "x", UID: uidForwarder}},
		CachedOnly: true,
		Limit:      20,
	})
	if err != nil || len(fwdFeed.Items) != 2 {
		t.Fatalf("FeedPage(uidForwarder) = %d items err=%v, want 2", len(fwdFeed.Items), err)
	}
	origFeed, err := store.FeedPage(FeedPageRequest{
		AuthorRefs: []AuthorRef{{Site: "x", UID: uidOrigin}},
		CachedOnly: true,
		Limit:      20,
	})
	if err != nil || len(origFeed.Items) != 0 {
		t.Fatalf("FeedPage(uidOrigin) leaked forwarded X post: %+v err=%v", origFeed.Items, err)
	}
}

func TestXOriginThreadGIFSanitizationMergePortableAndLiveLastSequenceAt(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := filepath.Join(dataDir, "cache")
	db, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, cacheDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	const (
		subUID  = "1000000000000000031"
		origUID = "1000000000000000032"
	)

	fwdDir, err := XPostDir(cacheDir, subUID, "1000000000000000041", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fwdDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(fwdDir, "images", "01.jpg"), []byte("gif-cover"), 0o600)
	_ = os.WriteFile(filepath.Join(fwdDir, "images", "01.mp4"), []byte("gif-mp4"), 0o600)

	saved, err := store.SaveItem(fwdDir, ItemMetadata{
		Version:     1,
		ID:          "1000000000000000041",
		Site:        "x",
		Kind:        "dynamic",
		Title:       "引用长文与线程",
		Text:        "The torch bearer ran fast. 外层评语 auth_token=SYNTHAUTH999 ct0=SYNTHCT0999 kdt=SYNTHKDT999 Authorization: Bearer SYNTHBEARER999999999999999999999999 x-csrf-token: SYNTHCSRF999",
		DynamicType: "forward",
		TypeLabel:   "转发",
		PubTs:       1763802000,
		Author:      ItemAuthor{Site: "x", UID: subUID, UniqueID: "sub_user", Name: "订阅转发者"},
		Via:         &ItemVia{Reason: ViaForward, Site: "x", UID: subUID, Name: "订阅转发者", DynamicID: "1000000000000000041"},
		Card: &ItemRichCard{
			Kind:  "webpage",
			Label: "链接卡片",
			Title: "视频卡片",
			URL:   "https://video.twimg.com/ext_tw_video/1/pu/vid/clip.mp4?tag=12",
		},
		Thread: []ItemThreadPart{
			{
				ID:           "1000000000000000042",
				PubTs:        1763802010,
				Text:         "线程第二条保留正文 https://video.twimg.com/secret.mp4?tag=12 ct0=SYNTHCT0THREAD",
				MediaIndexes: []int{1},
			},
		},
		Origin: &ItemOrigin{
			Site:         "x",
			ID:           "1000000000000000091",
			CanonicalURL: "https://x.com/orig_user/status/1000000000000000091?auth_token=SYNTHORIGURL",
			Author: ItemAuthor{
				Site:     "x",
				UID:      "00" + origUID,
				UniqueID: "orig_user",
				Name:     "原推作者",
				Face:     "https://pbs.twimg.com/profile_images/2/orig.jpg?kdt=SYNTHORIGFACE",
			},
			PubTs: 1763801000,
			Text:  "原推正文保留，敏感参数剥离 https://ton.x.com/private/1.jpg x-csrf-token=SYNTHORIGCSRF",
		},
		Media: []MediaFile{
			{Role: "cover", Path: "images/01.jpg", FromOrigin: true},
			{Role: "video", Path: "images/01.mp4", GIF: true, FromOrigin: true},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "video", GIF: true, FromOrigin: true, Cover: "images/01.jpg", Video: "images/01.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem x quote+thread: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(fwdDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"SYNTHAUTH999", "SYNTHCT0999", "SYNTHKDT999", "SYNTHBEARER999999999999999999999999",
		"SYNTHCSRF999", "SYNTHCT0THREAD", "SYNTHORIGURL", "SYNTHORIGFACE", "SYNTHORIGCSRF",
		"video.twimg.com", "ton.x.com", "auth_token=", "ct0=", "kdt=", "x-csrf-token",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("item.json leaked X secret/URL %q:\n%s", secret, raw)
		}
	}
	// Ordinary English prose containing "bearer" must NOT be scrubbed.
	if !strings.Contains(saved.Text, "The torch bearer ran fast.") {
		t.Fatalf("ordinary prose lost in Text: %q", saved.Text)
	}
	if saved.Origin == nil || saved.Origin.Author.UID != origUID || saved.Origin.Author.UniqueID != "orig_user" || saved.Origin.Author.Face != "" {
		t.Fatalf("unexpected sanitized origin: %+v", saved.Origin)
	}
	if saved.Card != nil && saved.Card.URL != "" {
		t.Fatalf("video.twimg.com card URL must be scrubbed, got %+v", saved.Card)
	}

	// MergeItemMetadata must preserve/upgrade GIF, Thread, Origin.Author.UniqueID, and Live.LastSequenceAt.
	localBare := saved
	localBare.Origin = nil
	localBare.Thread = nil
	localBare.Media = []MediaFile{
		{Role: "cover", Path: "images/01.jpg", FromOrigin: false},
		{Role: "video", Path: "images/01.mp4", GIF: false, FromOrigin: false},
	}
	localBare.NotePages = []NotePageRecord{
		{Index: 1, Kind: "video", GIF: false, FromOrigin: false, Cover: "images/01.jpg", Video: "images/01.mp4"},
	}
	merged, _ := MergeItemMetadata(localBare, saved, MergeKeepLocal, nil)
	if !merged.Media[1].GIF || !merged.NotePages[0].GIF || !merged.Media[1].FromOrigin {
		t.Fatalf("MergeItemMetadata did not upgrade GIF/FromOrigin: media=%+v pages=%+v", merged.Media, merged.NotePages)
	}
	if len(merged.Thread) != 1 || merged.Thread[0].ID != "1000000000000000042" {
		t.Fatalf("MergeItemMetadata lost Thread: %+v", merged.Thread)
	}
	if merged.Origin == nil || merged.Origin.Author.UniqueID != "orig_user" {
		t.Fatalf("MergeItemMetadata lost Origin: %+v", merged.Origin)
	}

	// PortableManifest round-trip preserves GIF, Thread, and Live.LastSequenceAt.
	merged.Live = &LiveRecordingInfo{
		RoomID:         123456,
		LastSequence:   42,
		HasSequence:    true,
		LastSequenceAt: "2026-11-22T14:10:31Z",
	}
	portableBytes, err := PortableManifest(merged)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseItemMetadata(portableBytes)
	if err != nil {
		t.Fatalf("ParseItemMetadata(portable): %v", err)
	}
	if !reparsed.NotePages[0].GIF || !reparsed.Media[1].GIF || len(reparsed.Thread) != 1 {
		t.Fatalf("PortableManifest round-trip lost GIF/Thread: %+v", reparsed)
	}
	if reparsed.Live == nil || reparsed.Live.LastSequenceAt != "2026-11-22T14:10:31Z" {
		t.Fatalf("PortableManifest round-trip lost Live.LastSequenceAt: %+v", reparsed.Live)
	}

	// Live.LastSequenceAt with accidental signed URL/query must be scrubbed.
	badLive := ItemMetadata{
		Site: "bilibili",
		Kind: "live",
		ID:   "123456-1763800000",
		Live: &LiveRecordingInfo{
			RoomID:         123456,
			LastSequenceAt: "https://evil.example/?token=bad",
		},
	}
	SanitizeItemMetadata(&badLive)
	if badLive.Live.LastSequenceAt != "" {
		t.Fatalf("unsafe Live.LastSequenceAt was not scrubbed: %q", badLive.Live.LastSequenceAt)
	}
}

func TestXReplyContextThreadPartOriginAndNestedOriginDepthCap(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := filepath.Join(dataDir, "cache")
	db, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, err := OpenWithDataDir(db, cacheDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	const (
		uidAuthor = "1000000000000000061"
		uidOrig   = "1000000000000000062"
		uidInner  = "1000000000000000063"
		uidThread = "1000000000000000064"
	)

	// 1. Legacy v1 manifest without InReplyTo / Thread[].Origin / Origin.Origin still parses cleanly.
	legacyRaw := []byte(`{"version":1,"id":"1000000000000000601","site":"x","kind":"dynamic","title":"旧清单","pubTs":1763803000,"author":{"site":"x","uid":"1000000000000000061","name":"作者"}}`)
	legacyMeta, err := ParseItemMetadata(legacyRaw)
	if err != nil || legacyMeta.InReplyTo != nil || legacyMeta.Origin != nil {
		t.Fatalf("legacy v1 manifest parse failed: meta=%+v err=%v", legacyMeta, err)
	}

	// 2. Save item with InReplyTo, Thread[].Origin, and 3-level nested Origin -> depth > 1 MUST be stripped.
	postDir, err := XPostDir(cacheDir, uidAuthor, "1000000000000000602", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(postDir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveItem(postDir, ItemMetadata{
		Version:     1,
		ID:          "1000000000000000602",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "引用帖子",
		Title:       "两层嵌套引用与线程内引用",
		Text:        "外层回复并引用",
		PubTs:       1763803100,
		Author:      ItemAuthor{Site: "x", UID: uidAuthor, UniqueID: "outer_x", Name: "外层作者"},
		Via:         &ItemVia{Reason: ViaForward, Site: "x", UID: uidAuthor, Name: "外层作者", DynamicID: "1000000000000000602"},
		InReplyTo: &ItemReplyTarget{
			StatusID:   "001000000000000000501",
			UserID:     "001000000000000000502",
			ScreenName: "@reply_target_user",
		},
		Origin: &ItemOrigin{
			Site:   "x",
			ID:     "1000000000000000610",
			Author: ItemAuthor{Site: "x", UID: uidOrig, UniqueID: "mid_orig", Name: "中层作者"},
			Text:   "中层引用正文",
			InReplyTo: &ItemReplyTarget{
				StatusID:   "https://evil.example/?auth_token=SECRET_REPLY",
				UserID:     "1000000000000000509",
				ScreenName: "mid_reply_handle",
			},
			Origin: &ItemOrigin{
				Site:   "x",
				ID:     "1000000000000000611",
				Author: ItemAuthor{Site: "x", UID: uidInner, UniqueID: "inner_orig", Name: "内层作者"},
				Text:   "内层引用正文 ct0=SECRET_INNER_CT0",
				Origin: &ItemOrigin{
					Site:   "x",
					ID:     "1000000000000000612",
					Author: ItemAuthor{Site: "x", UID: "1000000000000000069", Name: "第三层必须被截断"},
					Text:   "SHOULD_BE_STRIPPED_DEPTH2",
				},
			},
		},
		Thread: []ItemThreadPart{
			{ID: "1000000000000000602", PubTs: 1763803100, Text: "外层回复并引用"},
			{
				ID:    "1000000000000000603",
				PubTs: 1763803150,
				Text:  "",
				Origin: &ItemOrigin{
					Site:   "x",
					ID:     "1000000000000000620",
					Author: ItemAuthor{Site: "x", UID: uidThread, UniqueID: "thread_quoted", Name: "线程被引作者"},
					Text:   "线程子节点内嵌引用正文 auth_token=SECRET_THREAD_TOK",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem DEC-091 manifest: %v", err)
	}
	if saved.InReplyTo == nil || saved.InReplyTo.StatusID != "1000000000000000501" || saved.InReplyTo.UserID != "1000000000000000502" || saved.InReplyTo.ScreenName != "reply_target_user" {
		t.Fatalf("unexpected sanitized InReplyTo: %+v", saved.InReplyTo)
	}
	if saved.Origin == nil || saved.Origin.InReplyTo == nil || saved.Origin.InReplyTo.StatusID != "" || saved.Origin.InReplyTo.ScreenName != "mid_reply_handle" {
		t.Fatalf("unsafe Origin.InReplyTo.StatusID should be scrubbed while keeping clean handle: %+v", saved.Origin)
	}
	if saved.Origin.Origin == nil || saved.Origin.Origin.ID != "1000000000000000611" {
		t.Fatalf("depth-1 Origin.Origin lost: %+v", saved.Origin)
	}
	if saved.Origin.Origin.Origin != nil {
		t.Fatalf("depth-2 Origin.Origin.Origin must be stripped to nil, got %+v", saved.Origin.Origin.Origin)
	}
	if len(saved.Thread) != 2 || saved.Thread[1].Origin == nil || saved.Thread[1].Origin.ID != "1000000000000000620" {
		t.Fatalf("thread part with empty text + Origin must be retained: %+v", saved.Thread)
	}

	raw, err := os.ReadFile(filepath.Join(postDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SECRET_REPLY", "SECRET_INNER_CT0", "SECRET_THREAD_TOK", "SHOULD_BE_STRIPPED_DEPTH2", "auth_token=", "ct0="} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("item.json leaked forbidden token %q:\n%s", forbidden, raw)
		}
	}

	// 3. RebuildIndex + GetItem preserves all DEC-091 fields.
	if _, err := store.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	reloaded, ok, err := store.GetItem("x:dynamic:1000000000000000602")
	if err != nil || !ok || reloaded.InReplyTo == nil || reloaded.Origin == nil || reloaded.Origin.Origin == nil || len(reloaded.Thread) != 2 || reloaded.Thread[1].Origin == nil {
		t.Fatalf("Reload after RebuildIndex lost DEC-091 fields: ok=%v err=%v meta=%+v", ok, err, reloaded)
	}

	// 4. MergeItemMetadata supplements InReplyTo, Origin.Origin, and matching Thread[].Origin.
	localBare := reloaded
	localBare.InReplyTo = nil
	localBare.Origin = &ItemOrigin{Site: "x", ID: "1000000000000000610", Author: ItemAuthor{Site: "x", UID: uidOrig, Name: "中层作者"}}
	localBare.Thread = []ItemThreadPart{
		{ID: "1000000000000000602", PubTs: 1763803100, Text: "外层回复并引用"},
		{ID: "1000000000000000603", PubTs: 1763803150, Text: ""},
	}
	merged, _ := MergeItemMetadata(localBare, reloaded, MergeKeepLocal, nil)
	if merged.InReplyTo == nil || merged.InReplyTo.ScreenName != "reply_target_user" {
		t.Fatalf("MergeItemMetadata lost InReplyTo: %+v", merged.InReplyTo)
	}
	if merged.Origin == nil || merged.Origin.Origin == nil || merged.Origin.Origin.ID != "1000000000000000611" || merged.Origin.Origin.Origin != nil {
		t.Fatalf("MergeItemMetadata failed to supplement depth-1 Origin.Origin: %+v", merged.Origin)
	}
	if len(merged.Thread) != 2 || merged.Thread[1].Origin == nil || merged.Thread[1].Origin.ID != "1000000000000000620" {
		t.Fatalf("MergeItemMetadata failed to supplement matching Thread[1].Origin: %+v", merged.Thread)
	}

	// 5. PortableManifest round-trip preserves fields and enforces depth cap <= 1.
	merged.Origin.Origin.Origin = &ItemOrigin{Site: "x", ID: "999", Text: "DEEP_FORBIDDEN"}
	portableBytes, err := PortableManifest(merged)
	if err != nil {
		t.Fatalf("PortableManifest: %v", err)
	}
	if strings.Contains(string(portableBytes), "DEEP_FORBIDDEN") {
		t.Fatalf("PortableManifest failed to strip depth > 1 nested origin:\n%s", portableBytes)
	}
	reparsed, err := ParseItemMetadata(portableBytes)
	if err != nil || reparsed.InReplyTo == nil || reparsed.Origin == nil || reparsed.Origin.Origin == nil || reparsed.Origin.Origin.Origin != nil || len(reparsed.Thread) != 2 || reparsed.Thread[1].Origin == nil {
		t.Fatalf("PortableManifest round-trip failed: err=%v meta=%+v", err, reparsed)
	}

	// 6. All-invalid InReplyTo sanitizes to nil.
	badReply := SanitizeItemReplyTarget(&ItemReplyTarget{
		StatusID:   "https://x.com/status/1?auth_token=abc",
		UserID:     "not_a_number",
		ScreenName: "evil.com/path?ct0=123",
	}, "x")
	if badReply != nil {
		t.Fatalf("expected all-invalid X ItemReplyTarget to sanitize to nil, got %+v", badReply)
	}
}
