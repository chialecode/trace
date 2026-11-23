package library

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// RefreshIndex must walk the filesystem without holding Store.mu (so UI reads
// never stall), skip SQLite writes for unchanged items, reconcile concurrent
// SaveItem/DeleteItem safely, and only update rows that actually changed.
func TestRefreshIndexUnlockedWalkConcurrentReadsAndSkipsUnchangedWrites(t *testing.T) {
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

	savePost := func(site, uid, id, title string, files ...string) (string, ItemMetadata) {
		t.Helper()
		dir := filepath.Join(cacheDir, site, uid, "posts", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		var media []MediaFile
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("payload-"+id+"-"+f), 0o600); err != nil {
				t.Fatal(err)
			}
			media = append(media, MediaFile{Role: "image", Path: f})
		}
		meta, err := store.SaveItem(dir, ItemMetadata{
			Version: 1,
			Site:    site,
			Kind:    "dynamic",
			ID:      id,
			Title:   title,
			PubTs:   1700000100,
			Author:  ItemAuthor{Site: site, UID: uid, Name: "Author-" + uid},
			Media:   media,
		})
		if err != nil {
			t.Fatalf("SaveItem(%s): %v", id, err)
		}
		return dir, meta
	}

	dir1, meta1 := savePost("x", "9001", "1800000000000000001", "Unchanged Post 1", "01.jpg", "02.jpg")
	dir2, meta2 := savePost("weibo", "51001", "5000000000000001", "Unchanged Post 2", "01.jpg")
	_, meta3 := savePost("x", "9001", "1800000000000000003", "Will Delete Concurrently", "01.jpg")
	_ = dir1

	// Install TEMP SQLite triggers to count every real INSERT/UPDATE on library_items & feed_items.
	for _, stmt := range []string{
		`CREATE TEMP TABLE write_audit (tbl TEXT NOT NULL, op TEXT NOT NULL)`,
		`CREATE TEMP TRIGGER audit_lib_ins AFTER INSERT ON library_items BEGIN INSERT INTO write_audit VALUES ('library_items', 'ins'); END`,
		`CREATE TEMP TRIGGER audit_lib_upd AFTER UPDATE ON library_items BEGIN INSERT INTO write_audit VALUES ('library_items', 'upd'); END`,
		`CREATE TEMP TRIGGER audit_feed_ins AFTER INSERT ON feed_items BEGIN INSERT INTO write_audit VALUES ('feed_items', 'ins'); END`,
		`CREATE TEMP TRIGGER audit_feed_upd AFTER UPDATE ON feed_items BEGIN INSERT INTO write_audit VALUES ('feed_items', 'upd'); END`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create audit trigger: %v", err)
		}
	}
	countWrites := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM write_audit`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	resetWrites := func() {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM write_audit`); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Pause RefreshIndex mid-walk and verify concurrent reads complete immediately without blocking.
	enteredWalk := make(chan struct{})
	releaseWalk := make(chan struct{})
	store.onRebuildWalk = func() {
		close(enteredWalk)
		<-releaseWalk
	}

	type refreshResult struct {
		rep RebuildReport
		err error
	}
	doneCh := make(chan refreshResult, 1)
	go func() {
		rep, err := store.RefreshIndex()
		doneCh <- refreshResult{rep: rep, err: err}
	}()

	select {
	case <-enteredWalk:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshIndex never reached unlocked walk phase")
	}

	// Concurrent reads while RefreshIndex is paused inside its disk walk MUST NOT block on Store.mu.
	readDone := make(chan error, 1)
	go func() {
		if got, ok, err := store.GetItem(meta1.ItemKey); err != nil || !ok || got.Title != meta1.Title {
			readDone <- fmt.Errorf("GetItem during walk: ok=%v err=%v title=%q", ok, err, got.Title)
			return
		}
		if got, ok, err := store.GetItemMetaOnly(meta2.ItemKey); err != nil || !ok || got.Title != meta2.Title {
			readDone <- fmt.Errorf("GetItemMetaOnly during walk: ok=%v err=%v title=%q", ok, err, got.Title)
			return
		}
		if page, err := store.FeedPage(FeedPageRequest{Order: "newest", Direction: "older", Limit: 10}); err != nil || len(page.Items) != 3 {
			readDone <- fmt.Errorf("FeedPage during walk: len=%d err=%v", len(page.Items), err)
			return
		}
		if counts, err := store.CountCachedWorks([]AuthorRef{{Site: "x", UID: "9001"}, {Site: "weibo", UID: "51001"}}); err != nil || counts["x:9001"] != 2 || counts["weibo:51001"] != 1 {
			readDone <- fmt.Errorf("CountCachedWorks during walk: %+v err=%v", counts, err)
			return
		}
		readDone <- nil
	}()
	select {
	case err := <-readDone:
		if err != nil {
			close(releaseWalk)
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		close(releaseWalk)
		t.Fatal("concurrent reads blocked while RefreshIndex was in disk walk")
	}

	close(releaseWalk)
	res := <-doneCh
	store.onRebuildWalk = nil
	if res.err != nil {
		t.Fatalf("RefreshIndex (unchanged): %v", res.err)
	}
	if res.rep.Scanned != 3 || res.rep.Indexed != 3 || len(res.rep.Removed) != 0 || len(res.rep.NewlyMissing) != 0 {
		t.Fatalf("unexpected unchanged RefreshIndex report: %+v", res.rep)
	}
	if writes := countWrites(); writes != 0 {
		t.Fatalf("expected 0 SQLite writes on unchanged RefreshIndex, got %d", writes)
	}

	// 2. Delete one media file on disk for meta2 only -> RefreshIndex must update ONLY meta2 (not meta1 or meta3).
	resetWrites()
	if err := os.Remove(filepath.Join(dir2, "01.jpg")); err != nil {
		t.Fatal(err)
	}
	rep2, err := store.RefreshIndex()
	if err != nil {
		t.Fatalf("RefreshIndex (1 changed): %v", err)
	}
	if len(rep2.NewlyMissing) != 1 || rep2.NewlyMissing[0].ItemKey != meta2.ItemKey {
		t.Fatalf("NewlyMissing = %+v, want [%s]", rep2.NewlyMissing, meta2.ItemKey)
	}
	var libUpdates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM write_audit WHERE tbl = 'library_items'`).Scan(&libUpdates); err != nil {
		t.Fatal(err)
	}
	if libUpdates != 1 {
		t.Fatalf("expected exactly 1 library_items write for 1 changed item, got %d", libUpdates)
	}

	// A follow-up RefreshIndex with no further changes performs 0 writes again.
	resetWrites()
	if _, err := store.RefreshIndex(); err != nil {
		t.Fatalf("RefreshIndex (second steady state): %v", err)
	}
	if writes := countWrites(); writes != 0 {
		t.Fatalf("expected 0 SQLite writes on second steady RefreshIndex, got %d", writes)
	}

	// 3. Concurrent DeleteItem + SaveItem during RefreshIndex walk must neither resurrect deleted items
	// nor clobber newly saved items.
	entered2 := make(chan struct{})
	release2 := make(chan struct{})
	store.onRebuildWalk = func() {
		close(entered2)
		<-release2
	}
	done2 := make(chan refreshResult, 1)
	go func() {
		rep, err := store.RefreshIndex()
		done2 <- refreshResult{rep: rep, err: err}
	}()
	<-entered2

	if err := store.DeleteItem(meta3.ItemKey, true); err != nil {
		close(release2)
		t.Fatalf("concurrent DeleteItem: %v", err)
	}
	_, meta4 := savePost("x", "9001", "1800000000000000004", "Saved During Walk", "01.jpg")
	close(release2)

	res2 := <-done2
	store.onRebuildWalk = nil
	if res2.err != nil {
		t.Fatalf("RefreshIndex with concurrent mutations: %v", res2.err)
	}
	if _, ok, err := store.GetItemMetaOnly(meta3.ItemKey); err != nil || ok {
		t.Fatalf("concurrently deleted item was resurrected: ok=%v err=%v", ok, err)
	}
	if got, ok, err := store.GetItemMetaOnly(meta4.ItemKey); err != nil || !ok || got.Title != "Saved During Walk" {
		t.Fatalf("concurrently saved item lost/corrupted: ok=%v err=%v meta=%+v", ok, err, got)
	}
	if len(res2.rep.Removed) != 0 {
		t.Fatalf("concurrent Store.DeleteItem should not be misreported as external removal: %+v", res2.rep.Removed)
	}
}

// RefreshIndex must NOT write item.json during its unlocked filesystem walk;
// manifest status updates must be deferred until the final s.mu.Lock() phase and
// skipped for items concurrently updated by SaveItem or removed by DeleteItem.
func TestRefreshIndexDefersManifestDiskWritesToLockedPhase(t *testing.T) {
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

	saveItem := func(id, title string) (string, ItemMetadata) {
		t.Helper()
		dir := filepath.Join(cacheDir, "x", "9002", "posts", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "01.jpg"), []byte("img-"+id), 0o600); err != nil {
			t.Fatal(err)
		}
		meta, err := store.SaveItem(dir, ItemMetadata{
			Version: 1,
			Site:    "x",
			Kind:    "dynamic",
			ID:      id,
			Title:   title,
			PubTs:   1700000500,
			Author:  ItemAuthor{Site: "x", UID: "9002", Name: "Author-9002"},
			Media:   []MediaFile{{Role: "image", Path: "01.jpg"}},
		})
		if err != nil {
			t.Fatalf("SaveItem(%s): %v", id, err)
		}
		return dir, meta
	}

	dirSave, metaSave := saveItem("1800000000000000101", "Original Title A")
	dirIdemp, metaIdemp := saveItem("1800000000000000102", "Idempotent Save D")
	dirDel, metaDel := saveItem("1800000000000000103", "To Delete Concurrently B")
	dirMiss, metaMiss := saveItem("1800000000000000104", "Truly Missing C")

	// Externally remove 01.jpg from all 4 items so the unlocked walk detects missing files on all 4.
	for _, d := range []string{dirSave, dirIdemp, dirDel, dirMiss} {
		if err := os.Remove(filepath.Join(d, "01.jpg")); err != nil {
			t.Fatal(err)
		}
	}

	afterWalk := make(chan struct{})
	releaseFinal := make(chan struct{})
	rebuildAfterWalkHook = func() {
		close(afterWalk)
		<-releaseFinal
	}
	defer func() { rebuildAfterWalkHook = nil }()

	type refreshRes struct {
		rep RebuildReport
		err error
	}
	doneCh := make(chan refreshRes, 1)
	go func() {
		rep, err := store.RefreshIndex()
		doneCh <- refreshRes{rep: rep, err: err}
	}()

	select {
	case <-afterWalk:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshIndex never reached post-walk pause")
	}

	// 1. Unlocked walk MUST NOT have overwritten item.json on disk for any item!
	for _, tc := range []struct {
		name string
		dir  string
	}{
		{"metaSave", dirSave},
		{"metaIdemp", dirIdemp},
		{"metaDel", dirDel},
		{"metaMiss", dirMiss},
	} {
		raw, err := os.ReadFile(filepath.Join(tc.dir, "item.json"))
		if err != nil {
			close(releaseFinal)
			t.Fatalf("read %s item.json after unlocked walk: %v", tc.name, err)
		}
		parsed, err := ParseItemMetadata(raw)
		if err != nil {
			close(releaseFinal)
			t.Fatalf("parse %s item.json: %v", tc.name, err)
		}
		if parsed.Status != "complete" {
			close(releaseFinal)
			t.Fatalf("%s item.json was written during unlocked walk: status=%q, want complete", tc.name, parsed.Status)
		}
	}

	// 2. Concurrent mutations while RefreshIndex has already scanned the missing files:
	//    - itemA: restore 01.jpg + SaveItem with updated title.
	//    - itemD: restore exact same 01.jpg + SaveItem with identical metadata (idempotent SQLite row).
	//    - itemB: DeleteItem(..., true) removes item.json and folder.
	if err := os.WriteFile(filepath.Join(dirSave, "01.jpg"), []byte("restored-A"), 0o600); err != nil {
		close(releaseFinal)
		t.Fatal(err)
	}
	updatedA := metaSave
	updatedA.Title = "Concurrently Updated A"
	updatedA.Status = "complete"
	updatedA.Media = []MediaFile{{Role: "image", Path: "01.jpg"}}
	if _, err := store.SaveItem(dirSave, updatedA); err != nil {
		close(releaseFinal)
		t.Fatalf("concurrent SaveItem(A): %v", err)
	}

	if err := os.WriteFile(filepath.Join(dirIdemp, "01.jpg"), []byte("img-"+metaIdemp.RawID), 0o600); err != nil {
		close(releaseFinal)
		t.Fatal(err)
	}
	if _, err := store.SaveItem(dirIdemp, metaIdemp); err != nil {
		close(releaseFinal)
		t.Fatalf("concurrent idempotent SaveItem(D): %v", err)
	}

	if err := store.DeleteItem(metaDel.ItemKey, true); err != nil {
		close(releaseFinal)
		t.Fatalf("concurrent DeleteItem(B): %v", err)
	}

	close(releaseFinal)
	res := <-doneCh
	if res.err != nil {
		t.Fatalf("RefreshIndex failed: %v", res.err)
	}

	// 3. Verify itemA was NOT clobbered on disk or in SQLite.
	rawA, err := os.ReadFile(filepath.Join(dirSave, "item.json"))
	if err != nil {
		t.Fatalf("read itemA item.json: %v", err)
	}
	diskA, err := ParseItemMetadata(rawA)
	if err != nil || diskA.Status != "complete" || diskA.Title != "Concurrently Updated A" {
		t.Fatalf("itemA item.json clobbered: err=%v status=%q title=%q", err, diskA.Status, diskA.Title)
	}
	sqlA, ok, err := store.GetItemMetaOnly(metaSave.ItemKey)
	if err != nil || !ok || sqlA.Status != "complete" || sqlA.Title != "Concurrently Updated A" {
		t.Fatalf("itemA SQLite clobbered: ok=%v err=%v status=%q title=%q", ok, err, sqlA.Status, sqlA.Title)
	}

	// 4. Verify itemD (idempotent SaveItem) was NOT clobbered on disk or in SQLite.
	rawD, err := os.ReadFile(filepath.Join(dirIdemp, "item.json"))
	if err != nil {
		t.Fatalf("read itemD item.json: %v", err)
	}
	diskD, err := ParseItemMetadata(rawD)
	if err != nil || diskD.Status != "complete" {
		t.Fatalf("itemD item.json clobbered: err=%v status=%q", err, diskD.Status)
	}
	sqlD, ok, err := store.GetItemMetaOnly(metaIdemp.ItemKey)
	if err != nil || !ok || sqlD.Status != "complete" {
		t.Fatalf("itemD SQLite clobbered: ok=%v err=%v status=%q", ok, err, sqlD.Status)
	}

	// 5. Verify deleted itemB was NOT resurrected on disk or in SQLite.
	if _, statErr := os.Stat(filepath.Join(dirDel, "item.json")); !os.IsNotExist(statErr) {
		t.Fatalf("deleted itemB item.json was resurrected on disk (statErr=%v)", statErr)
	}
	if _, ok, err := store.GetItemMetaOnly(metaDel.ItemKey); err != nil || ok {
		t.Fatalf("deleted itemB resurrected in SQLite: ok=%v err=%v", ok, err)
	}

	// 6. Verify untouched missing itemC WAS written to disk + SQLite inside the locked phase
	//    and is the ONLY item reported as missing/newly missing.
	rawC, err := os.ReadFile(filepath.Join(dirMiss, "item.json"))
	if err != nil {
		t.Fatalf("read itemC item.json: %v", err)
	}
	diskC, err := ParseItemMetadata(rawC)
	if err != nil || diskC.Status != "missing_files" {
		t.Fatalf("itemC item.json not updated to missing_files: err=%v status=%q", err, diskC.Status)
	}
	sqlC, ok, err := store.GetItemMetaOnly(metaMiss.ItemKey)
	if err != nil || !ok || sqlC.Status != "missing_files" {
		t.Fatalf("itemC SQLite not updated to missing_files: ok=%v err=%v status=%q", ok, err, sqlC.Status)
	}
	if res.rep.MissingFiles != 1 || len(res.rep.NewlyMissing) != 1 || res.rep.NewlyMissing[0].ItemKey != metaMiss.ItemKey {
		t.Fatalf("unexpected missing report: MissingFiles=%d NewlyMissing=%+v", res.rep.MissingFiles, res.rep.NewlyMissing)
	}

	// 7. Restoring itemC's media file flips both item.json and SQLite back to complete.
	if err := os.WriteFile(filepath.Join(dirMiss, "01.jpg"), []byte("img-"+metaMiss.RawID), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuildAfterWalkHook = nil
	if _, err := store.RefreshIndex(); err != nil {
		t.Fatalf("RefreshIndex restore C: %v", err)
	}
	rawCRestored, err := os.ReadFile(filepath.Join(dirMiss, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	diskCRestored, err := ParseItemMetadata(rawCRestored)
	if err != nil || diskCRestored.Status != "complete" {
		t.Fatalf("restored itemC item.json status=%q (err=%v), want complete", diskCRestored.Status, err)
	}
	sqlCRestored, ok, err := store.GetItemMetaOnly(metaMiss.ItemKey)
	if err != nil || !ok || sqlCRestored.Status != "complete" {
		t.Fatalf("restored itemC SQLite status=%q (ok=%v err=%v), want complete", sqlCRestored.Status, ok, err)
	}
}

// verifyItemFilesDetail must check checkSingleDirLink(itemDir) once per item
// (not once per media file), and GetItemMetaOnly must skip disk verification.
func TestVerifyItemFilesChecksItemDirOnceAndGetItemMetaOnlySkipsDisk(t *testing.T) {
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

	itemDir := filepath.Join(cacheDir, "weibo", "51002", "posts", "5000000000000099")
	if err := os.MkdirAll(filepath.Join(itemDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	var media []MediaFile
	var pages []NotePageRecord
	for i := 1; i <= 9; i++ {
		rel := fmt.Sprintf("images/%02d.jpg", i)
		if err := os.WriteFile(filepath.Join(itemDir, filepath.FromSlash(rel)), []byte(fmt.Sprintf("img-%02d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		media = append(media, MediaFile{Role: "image", Path: rel})
		pages = append(pages, NotePageRecord{Index: i, Kind: "image", Image: rel})
	}
	saved, err := store.SaveItem(itemDir, ItemMetadata{
		Version:   1,
		Site:      "weibo",
		Kind:      "dynamic",
		ID:        "5000000000000099",
		Title:     "Nine Image Post",
		PubTs:     1700000200,
		Author:    ItemAuthor{Site: "weibo", UID: "51002", Name: "NineImg"},
		Media:     media,
		NotePages: pages,
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// Warm recent ancestor dir cache (strictly above itemDir), then measure checkSingleDirLink calls.
	if err := rejectDirectoryLinksRecent(filepath.Dir(itemDir)); err != nil {
		t.Fatalf("warm ancestor cache: %v", err)
	}
	before := dirLinkCheckCount.Load()
	metaCopy := saved
	missing, totalBytes, missingPaths := verifyItemFilesDetail(itemDir, &metaCopy, false)
	checks := dirLinkCheckCount.Load() - before
	if missing || totalBytes == 0 || len(missingPaths) != 0 {
		t.Fatalf("verifyItemFilesDetail unexpected result: missing=%v bytes=%d paths=%v", missing, totalBytes, missingPaths)
	}
	if checks != 1 {
		t.Fatalf("verifyItemFilesDetail called checkSingleDirLink %d times for 9-image item, want 1", checks)
	}

	// Path traversal segments must still be rejected even with once-per-item dir validation.
	evil := saved
	evil.Media = append(evil.Media, MediaFile{Role: "image", Path: "../escape.jpg"})
	if miss, _, _ := verifyItemFilesDetail(itemDir, &evil, false); !miss {
		t.Fatal("verifyItemFilesDetail failed to reject traversal path ../escape.jpg")
	}

	// Delete one image on disk: GetItemMetaOnly must NOT hit disk (0 dirLinkChecks, keeps indexed status),
	// whereas GetItem verifies disk and transitions Status to missing_files.
	if err := os.Remove(filepath.Join(itemDir, "images", "09.jpg")); err != nil {
		t.Fatal(err)
	}
	beforeMeta := dirLinkCheckCount.Load()
	metaOnly, ok, err := store.GetItemMetaOnly(saved.ItemKey)
	metaChecks := dirLinkCheckCount.Load() - beforeMeta
	if err != nil || !ok {
		t.Fatalf("GetItemMetaOnly: ok=%v err=%v", ok, err)
	}
	if metaChecks != 0 {
		t.Fatalf("GetItemMetaOnly performed %d disk dir-link checks, want 0", metaChecks)
	}
	if metaOnly.Status != "complete" || metaOnly.ItemDir != filepath.Clean(itemDir) {
		t.Fatalf("GetItemMetaOnly = (status=%q dir=%q), want complete & %q", metaOnly.Status, metaOnly.ItemDir, filepath.Clean(itemDir))
	}

	fullMeta, ok, err := store.GetItem(saved.ItemKey)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if fullMeta.Status != "missing_files" {
		t.Fatalf("GetItem after file deletion: status=%q, want missing_files", fullMeta.Status)
	}
}

// CountCachedWorks must return exact counts for all 4 sites (bilibili, douyin,
// weibo, x) including collaboration and forward via semantics in a single grouped query.
func TestCountCachedWorksGroupedAllFourSitesAndViaSemantics(t *testing.T) {
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

	save := func(subDir string, meta ItemMetadata) {
		t.Helper()
		dir := filepath.Join(cacheDir, filepath.FromSlash(subDir))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "media.bin"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		meta.Version = 1
		meta.PubTs = 1700000300
		meta.Media = []MediaFile{{Role: "image", Path: "media.bin"}}
		if _, err := store.SaveItem(dir, meta); err != nil {
			t.Fatalf("SaveItem(%s): %v", meta.ID, err)
		}
	}

	// Bilibili: own video, collab video (credited to uploader & member), self-collab (must NOT double count),
	// forwarded video (credited to neither under bilibili video count), and non-video dynamic (excluded).
	save("bilibili/100/videos/BV1own", ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1own", Author: ItemAuthor{Mid: 100, Name: "U100"}})
	save("bilibili/200/videos/BV1collab", ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1collab", Author: ItemAuthor{Mid: 100, Name: "U100"}, Via: &ItemVia{Reason: ViaCollab, Site: "bilibili", UID: "200", Name: "U200"}})
	save("bilibili/100/videos/BV1selfcollab", ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1selfcollab", Author: ItemAuthor{Mid: 100, Name: "U100"}, Via: &ItemVia{Reason: ViaCollab, Site: "bilibili", UID: "100", Name: "U100"}})
	save("bilibili/200/forwards/BV1fwd", ItemMetadata{Site: "bilibili", Kind: "video", ID: "BV1fwd", Author: ItemAuthor{Mid: 100, Name: "U100"}, Via: &ItemVia{Reason: ViaForward, Site: "bilibili", UID: "200", Name: "U200"}})
	save("bilibili/100/dynamics/9001", ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: "9001", Author: ItemAuthor{Mid: 100, Name: "U100"}})

	// Douyin: own video, own note/dynamic, collab video (credited to both uploader & member).
	save("douyin/secA/works/7001", ItemMetadata{Site: "douyin", Kind: "video", ID: "7001", Author: ItemAuthor{Site: "douyin", UID: "secA", Name: "DA"}})
	save("douyin/secA/works/7002", ItemMetadata{Site: "douyin", Kind: "dynamic", ID: "7002", Author: ItemAuthor{Site: "douyin", UID: "secA", Name: "DA"}})
	save("douyin/secB/works/7003", ItemMetadata{Site: "douyin", Kind: "video", ID: "7003", Author: ItemAuthor{Site: "douyin", UID: "secA", Name: "DA"}, Via: &ItemVia{Reason: ViaCollab, Site: "douyin", UID: "secB", Name: "DB"}})

	// Weibo: own post, own standalone video, forwarded post (only forwarder), self-forwarded post (once for self).
	save("weibo/501/posts/wb1", ItemMetadata{Site: "weibo", Kind: "dynamic", ID: "wb1", Author: ItemAuthor{Site: "weibo", UID: "501", Name: "W1"}})
	save("weibo/501/posts/wbv1", ItemMetadata{Site: "weibo", Kind: "video", ID: "1034_1", Author: ItemAuthor{Site: "weibo", UID: "501", Name: "W1"}})
	save("weibo/502/forwards/wb2", ItemMetadata{Site: "weibo", Kind: "dynamic", ID: "wb2", Author: ItemAuthor{Site: "weibo", UID: "501", Name: "W1"}, Via: &ItemVia{Reason: ViaForward, Site: "weibo", UID: "502", Name: "W2"}})
	save("weibo/501/forwards/wb3", ItemMetadata{Site: "weibo", Kind: "dynamic", ID: "wb3", Author: ItemAuthor{Site: "weibo", UID: "501", Name: "W1"}, Via: &ItemVia{Reason: ViaForward, Site: "weibo", UID: "501", Name: "W1"}})

	// X: own post, retweet/quote by x2 of x1's post (only x2), self-quote by x1 (once for x1).
	save("x/801/posts/1801", ItemMetadata{Site: "x", Kind: "dynamic", ID: "1801", Author: ItemAuthor{Site: "x", UID: "801", Name: "X1"}})
	save("x/802/forwards/1802", ItemMetadata{Site: "x", Kind: "dynamic", ID: "1802", Author: ItemAuthor{Site: "x", UID: "801", Name: "X1"}, Via: &ItemVia{Reason: ViaForward, Site: "x", UID: "802", Name: "X2"}})
	save("x/801/forwards/1803", ItemMetadata{Site: "x", Kind: "dynamic", ID: "1803", Author: ItemAuthor{Site: "x", UID: "801", Name: "X1"}, Via: &ItemVia{Reason: ViaForward, Site: "x", UID: "801", Name: "X1"}})

	refs := []AuthorRef{
		{Site: "bilibili", UID: "100"},
		{Site: "bilibili", UID: "200"},
		{Site: "bilibili", UID: "999"},
		{Site: "douyin", UID: "secA"},
		{Site: "douyin", UID: "secB"},
		{Site: "weibo", UID: "501"},
		{Site: "weibo", UID: "502"},
		{Site: "x", UID: "801"},
		{Site: "x", UID: "802"},
		{Site: "x", UID: "803"},
		{Site: "unknown", UID: "1"},
	}
	got, err := store.CountCachedWorks(refs)
	if err != nil {
		t.Fatalf("CountCachedWorks: %v", err)
	}
	want := map[string]int{
		"bilibili:100": 3, // own + collab + selfcollab (NOT fwd, NOT dynamic)
		"bilibili:200": 1, // collab (NOT fwd)
		"bilibili:999": 0,
		"douyin:secA":  3, // video + dynamic + collab
		"douyin:secB":  1, // collab
		"weibo:501":    3, // own post + own video + self-forward (NOT wb2 forwarded by 502)
		"weibo:502":    1, // wb2 forwarded by 502
		"x:801":        2, // own post + self-quote (NOT 1802 forwarded by 802)
		"x:802":        1, // 1802 forwarded by 802
		"x:803":        0,
	}
	if len(got) != len(want) {
		t.Fatalf("CountCachedWorks len=%d (%+v), want %d (%+v)", len(got), got, len(want), want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("CountCachedWorks[%s] = %d, want %d (all=%+v)", k, got[k], w, got)
		}
	}
}
