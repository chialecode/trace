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
