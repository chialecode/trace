package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

func TestWeiboKeysSanitizationRebuildCountsAndMediaFilters(t *testing.T) {
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

	// 1. NormalizeRawID and CanonicalItemKey for Weibo posts and standalone 1034: videos.
	if got := NormalizeRawID("dynamic", "weibo:dynamic:5000000000000001"); got != "5000000000000001" {
		t.Fatalf("NormalizeRawID(weibo dynamic) = %q, want 5000000000000001", got)
	}
	if got := CanonicalItemKey("weibo", "dynamic", "weibo:dynamic:5000000000000001"); got != "weibo:dynamic:5000000000000001" {
		t.Fatalf("CanonicalItemKey(weibo dynamic) = %q", got)
	}
	if got := CanonicalItemKey("weibo", "video", "1034:5000000000000009"); got != "weibo:video:1034_5000000000000009" {
		t.Fatalf("CanonicalItemKey(weibo video colon) = %q", got)
	}
	if got := CanonicalItemKey("weibo", "video", "weibo:video:1034_5000000000000009"); got != "weibo:video:1034_5000000000000009" {
		t.Fatalf("CanonicalItemKey(weibo video prefixed) = %q", got)
	}

	const (
		uidAuthor    = "1000000001"
		uidForwarder = "1000000002"
		uidOrigin    = "1000000003"
	)

	// 2. Save a Weibo single-video post (itemKey = weibo:dynamic:<mid>, media role = video) with signed URLs to scrub.
	vidPostDir, err := WeiboPostDir(cacheDir, uidAuthor, "5000000000000001", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vidPostDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"video.mp4": "synthetic-weibo-video",
		"cover.jpg": "synthetic-weibo-cover",
	} {
		if err := os.WriteFile(filepath.Join(vidPostDir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	savedVidPost, err := store.SaveItem(vidPostDir, ItemMetadata{
		Version:     1,
		ID:          "weibo:dynamic:5000000000000001",
		Site:        "weibo",
		Kind:        "dynamic",
		Title:       "合成微博视频帖",
		Text:        "合成微博视频正文",
		DynamicType: "video",
		PubTs:       1730000100,
		Author: ItemAuthor{
			Site: "weibo",
			UID:  "000" + uidAuthor,
			Name: "合成博主一",
			Face: "https://tvax1.sinaimg.cn/crop.0.0.180.180/avatar.jpg?Expires=1999999999&ssig=SYNTHSIG01&KID=imgbed,tva",
		},
		Media: []MediaFile{
			{Role: "cover", Path: "cover.jpg", Source: "https://wx1.sinaimg.cn/large/cover.jpg?ssig=SYNTHSIG02"},
			{Role: "video", Path: "video.mp4", Source: "https://f.video.weibocdn.com/synth.mp4?Expires=1999999999&ssig=SYNTHSIG03&KID=unistore,video"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem weibo video post: %v", err)
	}
	if savedVidPost.ItemKey != "weibo:dynamic:5000000000000001" || savedVidPost.Author.UID != uidAuthor {
		t.Fatalf("unexpected saved weibo video post: %+v", savedVidPost)
	}
	if savedVidPost.URL != "https://weibo.com/detail/5000000000000001" {
		t.Fatalf("expected fallback Weibo canonical URL, got %q", savedVidPost.URL)
	}
	if savedVidPost.Author.Face != "" {
		t.Fatalf("signed Weibo author face should be scrubbed, got %q", savedVidPost.Author.Face)
	}
	rawManifest, err := os.ReadFile(filepath.Join(vidPostDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SYNTHSIG01", "SYNTHSIG02", "SYNTHSIG03", "ssig=", "Expires=", "KID=", "weibocdn.com"} {
		if strings.Contains(string(rawManifest), forbidden) {
			t.Fatalf("item.json leaked Weibo signed URL fragment %q:\n%s", forbidden, rawManifest)
		}
	}

	// 3. Save a Weibo image + LivePhoto + mixed-video post (itemKey = weibo:dynamic:<mid>).
	imgPostDir, err := WeiboPostDir(cacheDir, uidAuthor, "5000000000000002", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(imgPostDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"images/01.jpg":      "still-01",
		"images/02.gif":      "GIF89a-anim",
		"images/03.jpg":      "live-still",
		"images/03_live.mp4": "live-motion",
	} {
		if err := os.WriteFile(filepath.Join(imgPostDir, filepath.FromSlash(rel)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveItem(imgPostDir, ItemMetadata{
		Version:     1,
		ID:          "5000000000000002",
		Site:        "weibo",
		Kind:        "dynamic",
		Title:       "合成微博图文帖",
		Text:        "图文含动图与实况",
		DynamicType: "draw",
		URL:         "https://weibo.com/1000000001/O00000002",
		PubTs:       1730000200,
		Author: ItemAuthor{
			Site: "weibo",
			UID:  uidAuthor,
			Name: "合成博主一",
			Face: "https://tvax1.sinaimg.cn/public/avatar.jpg",
		},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg"},
			{Role: "image", Path: "images/02.gif"},
			{Role: "image", Path: "images/03.jpg", Motion: "images/03_live.mp4"},
			{Role: "motion", Path: "images/03_live.mp4"},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg"},
			{Index: 2, Kind: "image", Image: "images/02.gif"},
			{Index: 3, Kind: "motion", Image: "images/03.jpg", Motion: "images/03_live.mp4"},
		},
	}); err != nil {
		t.Fatalf("SaveItem weibo image post: %v", err)
	}

	// 4. Save a forwarded Weibo post under forwarder's forwards/ folder with via (DEC-056).
	fwdDir, err := WeiboPostDir(cacheDir, uidForwarder, "5000000000000003", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fwdDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fwdDir, "images", "01.jpg"), []byte("fwd-img"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(fwdDir, ItemMetadata{
		Version:     1,
		ID:          "5000000000000003",
		Site:        "weibo",
		Kind:        "dynamic",
		Title:       "转发微博",
		Text:        "转发评语",
		DynamicType: "forward",
		PubTs:       1730000300,
		Author:      ItemAuthor{Site: "weibo", UID: uidOrigin, Name: "原博主"},
		Via:         &ItemVia{Reason: ViaForward, Site: "weibo", UID: uidForwarder, Name: "转发博主二", DynamicID: "5000000000000003"},
		Media:       []MediaFile{{Role: "image", Path: "images/01.jpg"}},
	}); err != nil {
		t.Fatalf("SaveItem weibo forward post: %v", err)
	}

	// 5. External library root with standalone Weibo 1034: video and a forwarded post for AddLibraryRoot + RebuildIndex.
	extRoot := t.TempDir()
	extVidDir, err := WeiboPostDir(extRoot, uidAuthor, "1034:5000000000000009", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extVidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extVidDir, "video.mp4"), []byte("standalone-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	extManifest := `{"version":1,"id":"1034:5000000000000009","site":"weibo","kind":"video","title":"独立视频页","pubTs":1730000400,"author":{"site":"weibo","uid":"1000000001","name":"合成博主一"},"media":[{"role":"video","path":"video.mp4"}]}`
	if err := os.WriteFile(filepath.Join(extVidDir, "item.json"), []byte(extManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	addRep, err := store.AddLibraryRoot(extRoot)
	if err != nil || addRep.Added != 1 {
		t.Fatalf("AddLibraryRoot(weibo extRoot) = %+v err=%v, want Added=1", addRep, err)
	}
	// Lookup works with both colon and underscore forms.
	for _, lookup := range []string{"weibo:video:1034_5000000000000009", "weibo:video:1034:5000000000000009"} {
		gotVid, ok, gErr := store.GetItem(lookup)
		if gErr != nil || !ok || gotVid.ItemKey != "weibo:video:1034_5000000000000009" {
			t.Fatalf("GetItem(%q) = (%+v, %v, %v)", lookup, gotVid, ok, gErr)
		}
		if gotVid.URL != "https://weibo.com/tv/show/1034:5000000000000009" {
			t.Fatalf("standalone video canonical URL = %q", gotVid.URL)
		}
	}
	dots := store.States([]string{"weibo:dynamic:5000000000000001", "weibo:video:1034:5000000000000009"})
	if dots["weibo:dynamic:5000000000000001"].Dot != "green" || dots["weibo:video:1034:5000000000000009"].Dot != "green" {
		t.Fatalf("unexpected Weibo dot states: %+v", dots)
	}

	if got := sanitizeWeiboManifestURL("https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001.mov%3FExpires%3D1999999999%26ssig%3DENCODED_SIG%26KID%3Dunistore%2Cvideo"); got != "" {
		t.Fatalf("percent-encoded signed LivePhoto URL should be scrubbed, got %q", got)
	}

	// 6. RebuildIndex recovers all 4 Weibo items across active cacheDir + extRoot.
	rebuildRep, err := store.RebuildIndex()
	if err != nil || rebuildRep.Indexed != 4 {
		t.Fatalf("RebuildIndex = %+v err=%v, want Indexed=4", rebuildRep, err)
	}

	// 7. CountCachedWorks counts all Weibo items for uidAuthor (3) and uidForwarder (1), and 0 for phantom uidOrigin.
	counts, err := store.CountCachedWorks([]AuthorRef{
		{Site: "weibo", UID: uidAuthor},
		{Site: "weibo", UID: uidForwarder},
		{Site: "weibo", UID: uidOrigin},
	})
	if err != nil {
		t.Fatalf("CountCachedWorks: %v", err)
	}
	if counts["weibo:"+uidAuthor] != 3 || counts["weibo:"+uidForwarder] != 1 || counts["weibo:"+uidOrigin] != 0 {
		t.Fatalf("unexpected Weibo CountCachedWorks: %+v", counts)
	}

	// 8. Media library filters ("image", "video", "forward") with ExcludeForward.
	imgItems, err := store.ListItems(FeedFilter{Site: "weibo", Kind: "image", ExcludeForward: true})
	if err != nil || len(imgItems) != 1 || imgItems[0].ItemKey != "weibo:dynamic:5000000000000002" {
		t.Fatalf("image filter (ExcludeForward) = %+v err=%v", imgItems, err)
	}
	vidItems, err := store.ListItems(FeedFilter{Site: "weibo", Kind: "video", ExcludeForward: true})
	if err != nil || len(vidItems) != 2 {
		t.Fatalf("video filter (ExcludeForward) = %d items (%+v) err=%v, want 2 (video post + standalone video)", len(vidItems), vidItems, err)
	}
	fwdItems, err := store.ListItems(FeedFilter{Site: "weibo", Kind: "forward"})
	if err != nil || len(fwdItems) != 1 || fwdItems[0].ItemKey != "weibo:dynamic:5000000000000003" {
		t.Fatalf("forward filter = %+v err=%v", fwdItems, err)
	}
}

func TestWeiboOriginSanitizationMergeAndFeedViaForwardAttribution(t *testing.T) {
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
		subUID  = "1000000011"
		origUID = "1000000022"
	)

	// 1. Save a regular retweet with Origin + Card containing signed URL tokens to scrub.
	regDir, err := WeiboPostDir(cacheDir, subUID, "5000000000000011", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(regDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(regDir, "images", "01.jpg"), []byte("outer"), 0o600)
	_ = os.WriteFile(filepath.Join(regDir, "images", "02.jpg"), []byte("origin"), 0o600)

	regSaved, err := store.SaveItem(regDir, ItemMetadata{
		Version:     1,
		ID:          "5000000000000011",
		Site:        "weibo",
		Kind:        "dynamic",
		Title:       "外层转发",
		Text:        "外层评语",
		DynamicType: "forward",
		TypeLabel:   "转发",
		PubTs:       1730001000,
		Author:      ItemAuthor{Site: "weibo", UID: subUID, Name: "订阅博主"},
		Via:         &ItemVia{Reason: ViaForward, Site: "weibo", UID: subUID, Name: "订阅博主", DynamicID: "5000000000000011"},
		Card: &ItemRichCard{
			Kind:  "webpage",
			Label: "网页链接",
			Title: "外层卡片",
			URL:   "https://f.video.weibocdn.com/leaked.mp4?Expires=1999999999&ssig=CARDSIG",
		},
		Origin: &ItemOrigin{
			Site:         "weibo",
			ID:           "5000000000000091",
			MblogID:      "O00000091",
			CanonicalURL: "https://weibo.com/1000000022/O00000091?ssig=ORIGURLSIG&Expires=1999999999",
			Author: ItemAuthor{
				Site: "weibo",
				UID:  "00" + origUID,
				Name: "原作者",
				Face: "https://tvax1.sinaimg.cn/face.jpg?Expires=1999999999&ssig=ORIGFACESIG&KID=imgbed,tva",
			},
			PubTs: 1730000500,
			Title: "原视频标题",
			Text:  "完整原博正文，真的吗？附链接 https://f.video.weibocdn.com/a.mp4?ssig=TEXTSIG&Expires=1999999999 与 SUB=LEAKSUB999 结束。",
			Card: &ItemRichCard{
				Kind:  "article",
				Label: "头条文章",
				Title: "文章标题",
				Desc:  "文章摘要",
				URL:   "https://weibo.com/ttarticle/p/show?id=2309405000000001",
			},
		},
		Media: []MediaFile{
			{Role: "image", Path: "images/01.jpg", FromOrigin: false},
			{Role: "image", Path: "images/02.jpg", FromOrigin: true},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", FromOrigin: false},
			{Index: 2, Kind: "image", Image: "images/02.jpg", FromOrigin: true},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem regular retweet: %v", err)
	}
	rawReg, err := os.ReadFile(filepath.Join(regDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"CARDSIG", "ORIGURLSIG", "ORIGFACESIG", "TEXTSIG", "LEAKSUB999", "ssig=", "Expires=", "KID="} {
		if strings.Contains(string(rawReg), secret) {
			t.Fatalf("regular retweet item.json leaked %q:\n%s", secret, rawReg)
		}
	}
	if regSaved.Origin == nil || regSaved.Origin.Author.UID != origUID || regSaved.Origin.Author.Face != "" {
		t.Fatalf("unexpected sanitized origin: %+v", regSaved.Origin)
	}
	if !strings.Contains(regSaved.Origin.Text, "真的吗？") {
		t.Fatalf("prose question mark lost in Origin.Text: %q", regSaved.Origin.Text)
	}
	if regSaved.Origin.Card == nil || regSaved.Origin.Card.URL != "https://weibo.com/ttarticle/p/show?id=2309405000000001" {
		t.Fatalf("clean article card URL was unexpectedly modified: %+v", regSaved.Origin.Card)
	}
	if regSaved.Card != nil && regSaved.Card.URL != "" {
		t.Fatalf("signed outer card URL was not scrubbed: %+v", regSaved.Card)
	}
	if regSaved.NotePages[0].FromOrigin || !regSaved.NotePages[1].FromOrigin {
		t.Fatalf("FromOrigin flags not preserved: %+v", regSaved.NotePages)
	}

	// 2. MergeItemMetadata supplements Origin, Card, and FromOrigin flags cleanly.
	localBare := regSaved
	localBare.Origin = nil
	localBare.Card = nil
	localBare.NotePages = []NotePageRecord{
		{Index: 1, Kind: "image", Image: "images/01.jpg", FromOrigin: false},
		{Index: 2, Kind: "image", Image: "images/02.jpg", FromOrigin: false},
	}
	localBare.Media = []MediaFile{
		{Role: "image", Path: "images/01.jpg", FromOrigin: false},
		{Role: "image", Path: "images/02.jpg", FromOrigin: false},
	}
	merged, _ := MergeItemMetadata(localBare, regSaved, MergeKeepLocal, nil)
	if merged.Origin == nil || merged.Origin.ID != "5000000000000091" || merged.Origin.Card == nil {
		t.Fatalf("MergeItemMetadata lost Origin: %+v", merged.Origin)
	}
	if !merged.NotePages[1].FromOrigin || !merged.Media[1].FromOrigin {
		t.Fatalf("MergeItemMetadata did not upgrade FromOrigin: pages=%+v media=%+v", merged.NotePages, merged.Media)
	}
	portableBytes, err := PortableManifest(merged)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseItemMetadata(portableBytes)
	if err != nil || reparsed.Origin == nil || reparsed.Origin.ID != "5000000000000091" || !reparsed.NotePages[1].FromOrigin {
		t.Fatalf("PortableManifest round-trip lost Origin/FromOrigin: %+v err=%v", reparsed, err)
	}

	// 3. Save a fast-retweet (Author.UID = origUID, Via = {ViaForward, UID = subUID}) under subUID/forwards/.
	fastDir, err := WeiboPostDir(cacheDir, subUID, "5000000000000012", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fastDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(fastDir, "images", "01.jpg"), []byte("fast"), 0o600)
	if _, err := store.SaveItem(fastDir, ItemMetadata{
		Version:     1,
		ID:          "5000000000000012",
		Site:        "weibo",
		Kind:        "dynamic",
		Title:       "快转微博正文",
		Text:        "快转原博完整正文",
		DynamicType: "forward",
		TypeLabel:   "转发",
		PubTs:       1730002000,
		Author:      ItemAuthor{Site: "weibo", UID: origUID, Name: "原作者"},
		Via:         &ItemVia{Reason: ViaForward, Site: "weibo", UID: subUID, Name: "订阅博主", DynamicID: "5000000000000012"},
		Media:       []MediaFile{{Role: "image", Path: "images/01.jpg"}},
	}); err != nil {
		t.Fatalf("SaveItem fast-retweet: %v", err)
	}

	// 4. FeedPage & QueryFeed for subUID MUST return BOTH regular retweet and fast-retweet;
	// FeedPage & QueryFeed for origUID MUST return 0 items (fast-retweet must not leak to origUID!).
	subFeed, err := store.FeedPage(FeedPageRequest{
		AuthorRefs: []AuthorRef{{Site: "weibo", UID: subUID}},
		CachedOnly: true,
		Limit:      20,
	})
	if err != nil || len(subFeed.Items) != 2 {
		t.Fatalf("FeedPage(subUID) = %d items (%+v) err=%v, want 2", len(subFeed.Items), subFeed.Items, err)
	}
	origFeed, err := store.FeedPage(FeedPageRequest{
		AuthorRefs: []AuthorRef{{Site: "weibo", UID: origUID}},
		CachedOnly: true,
		Limit:      20,
	})
	if err != nil || len(origFeed.Items) != 0 {
		t.Fatalf("FeedPage(origUID) leaked fast-retweet: %+v err=%v", origFeed.Items, err)
	}
	subQF, err := store.QueryFeed(FeedFilter{AuthorRefs: []AuthorRef{{Site: "weibo", UID: subUID}}, CachedOnly: true})
	if err != nil || len(subQF) != 2 {
		t.Fatalf("QueryFeed(subUID) = %d items err=%v, want 2", len(subQF), err)
	}
	origQF, err := store.QueryFeed(FeedFilter{AuthorRefs: []AuthorRef{{Site: "weibo", UID: origUID}}, CachedOnly: true})
	if err != nil || len(origQF) != 0 {
		t.Fatalf("QueryFeed(origUID) leaked fast-retweet: %+v err=%v", origQF, err)
	}

	// 5. LatestPubByAuthor must attribute fast-retweet pub_ts (1730002000) to subUID and NOT origUID.
	latest, err := store.LatestPubByAuthor()
	if err != nil {
		t.Fatalf("LatestPubByAuthor: %v", err)
	}
	var gotSubLatest, gotOrigLatest int64
	for _, au := range latest {
		if au.Site == "weibo" && au.UID == subUID {
			gotSubLatest = au.LatestPubTs
		}
		if au.Site == "weibo" && au.UID == origUID {
			gotOrigLatest = au.LatestPubTs
		}
	}
	if gotSubLatest != 1730002000 || gotOrigLatest != 0 {
		t.Fatalf("LatestPubByAuthor sub=%d orig=%d, want 1730002000 and 0", gotSubLatest, gotOrigLatest)
	}
}
