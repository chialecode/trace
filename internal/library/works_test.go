package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

// A collaboration video cached for a member (not the uploader) belongs to that
// member in the feed, the media library author list and the cached-works
// count (DEC-061).
func TestCollabVideoBelongsToMemberItWasCachedFor(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)
	if err != nil {
		t.Fatal(err)
	}
	save := func(mid, bvid string, author ItemAuthor, via *ItemVia) {
		t.Helper()
		dir := filepath.Join(store.CacheDir(), "bilibili", mid, "videos", bvid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveItem(dir, ItemMetadata{Site: "bilibili", Kind: "video", ID: bvid, Title: bvid, PubTs: 100, Author: author, Via: via}); err != nil {
			t.Fatal(err)
		}
	}
	shop := ItemAuthor{Mid: 479, Name: "Shop"}
	save("1382", "BV1collab", shop, &ItemVia{Reason: ViaCollab, Site: "bilibili", UID: "1382", Name: "Member"})
	save("1382", "BV1own", ItemAuthor{Mid: 1382, Name: "Member"}, nil)
	save("555", "BV1other", ItemAuthor{Mid: 555, Name: "Other"}, nil)

	stats, err := store.Statistics()
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]int{}
	for _, a := range stats.Authors {
		owners[a.UID]++
		if a.UID == "1382" && (a.Name != "Member" || a.Mid != 1382) {
			t.Fatalf("member author row = %+v", a)
		}
	}
	if owners["479"] != 0 || owners["1382"] != 1 || owners["555"] != 1 {
		t.Fatalf("library authors = %+v, want the collaboration video under the member only", stats.Authors)
	}

	page, err := store.FeedPage(FeedPageRequest{Order: "newest", Direction: "older", Limit: 10, AuthorRefs: []AuthorRef{{Site: "bilibili", UID: "1382"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range page.Items {
		got[it.RawID] = true
	}
	if !got["BV1collab"] || !got["BV1own"] || got["BV1other"] || len(page.Items) != 2 {
		t.Fatalf("member feed = %+v", page.Items)
	}
	entries, err := store.QueryFeed(FeedFilter{AuthorRefs: []AuthorRef{{Site: "bilibili", UID: "1382"}}, CachedOnly: true})
	if err != nil || len(entries) != 2 {
		t.Fatalf("QueryFeed member = %d %v", len(entries), err)
	}
	// The uploader still finds it under their own ID.
	entries, err = store.QueryFeed(FeedFilter{AuthorRefs: []AuthorRef{{Site: "bilibili", UID: "479"}}})
	if err != nil || len(entries) != 1 || entries[0].RawID != "BV1collab" {
		t.Fatalf("QueryFeed uploader = %+v %v", entries, err)
	}

	counts, err := store.CountCachedWorks([]AuthorRef{{Site: "bilibili", UID: "1382"}, {Site: "bilibili", UID: "479"}, {Site: "bilibili", UID: "9"}})
	if err != nil {
		t.Fatal(err)
	}
	if counts["bilibili:1382"] != 2 || counts["bilibili:479"] != 1 || counts["bilibili:9"] != 0 {
		t.Fatalf("cached works = %+v", counts)
	}
}

// A video cached only because a creator forwarded it in a dynamic (via.reason == "forward",
// stored under the forwarder's forwards/ directory) must belong to the forwarder in the
// Media Library author list, item list, latest-pub lookup, and cached-works count — never
// creating a phantom author row for the original uploader.
func TestForwardVideoBelongsToForwarderNotUploaderInLibrary(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := OpenWithDataDir(db, filepath.Join(tmp, "cache"), tmp)
	if err != nil {
		t.Fatal(err)
	}

	fwdDir := filepath.Join(store.CacheDir(), "bilibili", "3546", "forwards", "BV1fwdvid")
	if err := os.MkdirAll(fwdDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(fwdDir, ItemMetadata{
		Site:         "bilibili",
		Kind:         "video",
		ID:           "BV1fwdvid",
		Title:        "被转发的视频",
		PubTs:        200,
		Status:       "complete",
		DirectCached: false,
		Author:       ItemAuthor{Site: "bilibili", Mid: 9999, UID: "9999", Name: "原视频作者"},
		Via:          &ItemVia{Reason: ViaForward, Site: "bilibili", UID: "3546", Name: "转发动态作者", DynamicID: "127286"},
	}); err != nil {
		t.Fatal(err)
	}

	dynDir := filepath.Join(store.CacheDir(), "bilibili", "3546", "dynamics", "127286")
	if err := os.MkdirAll(dynDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(dynDir, ItemMetadata{
		Site:         "bilibili",
		Kind:         "dynamic",
		ID:           "127286",
		Title:        "转发动态",
		PubTs:        210,
		Status:       "complete",
		DirectCached: true,
		Author:       ItemAuthor{Site: "bilibili", Mid: 3546, UID: "3546", Name: "转发动态作者"},
		Videos:       []string{"BV1fwdvid"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Statistics(): forwarder appears once, original uploader does NOT appear at all.
	stats, err := store.Statistics()
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]int{}
	for _, a := range stats.Authors {
		owners[a.UID]++
		if a.UID == "3546" && (a.Name != "转发动态作者" || a.Mid != 3546) {
			t.Fatalf("forwarder author row = %+v, want name=转发动态作者 mid=3546", a)
		}
	}
	if owners["9999"] != 0 || owners["3546"] != 1 {
		t.Fatalf("library authors = %+v, want forwarder only (no phantom uploader 9999)", stats.Authors)
	}

	// 2. ListItems(): forwarder sees both dynamic & forwarded video; original uploader sees neither.
	fwdItems, err := store.ListItems(FeedFilter{Site: "bilibili", AuthorUID: "3546"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fwdItems) != 2 {
		t.Fatalf("ListItems(forwarder) = %d items (%+v), want 2", len(fwdItems), fwdItems)
	}
	uploaderItems, err := store.ListItems(FeedFilter{Site: "bilibili", AuthorUID: "9999"})
	if err != nil {
		t.Fatal(err)
	}
	if len(uploaderItems) != 0 {
		t.Fatalf("ListItems(original uploader) = %+v, want 0", uploaderItems)
	}

	// 3. LatestPubByAuthor(): must not attribute forwarded video to original uploader.
	latestRows, err := store.LatestPubByAuthor()
	if err != nil {
		t.Fatal(err)
	}
	latest := map[string]int64{}
	for _, a := range latestRows {
		latest[a.Site+":"+a.UID] = a.LatestPubTs
	}
	if latest["bilibili:9999"] != 0 {
		t.Fatalf("LatestPubByAuthor(uploader) = %d, want 0", latest["bilibili:9999"])
	}
	if latest["bilibili:3546"] != 210 {
		t.Fatalf("LatestPubByAuthor(forwarder) = %d, want 210", latest["bilibili:3546"])
	}

	// 4. CountCachedWorks(): must not count forwarded video for original uploader.
	counts, err := store.CountCachedWorks([]AuthorRef{
		{Site: "bilibili", UID: "3546"},
		{Site: "bilibili", UID: "9999"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts["bilibili:9999"] != 0 {
		t.Fatalf("CountCachedWorks(uploader) = %d, want 0", counts["bilibili:9999"])
	}
}
