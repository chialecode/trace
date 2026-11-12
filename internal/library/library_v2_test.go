package library

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/storage"
)

const frozenV1LibraryDDL = `
CREATE TABLE library_items (
	item_key TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	kind TEXT NOT NULL,
	raw_id TEXT NOT NULL,
	mid INTEGER NOT NULL DEFAULT 0,
	author_name TEXT NOT NULL DEFAULT '',
	author_face TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	cover_path TEXT NOT NULL DEFAULT '',
	canonical_url TEXT NOT NULL DEFAULT '',
	pub_ts INTEGER NOT NULL DEFAULT 0,
	cached_at TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'complete',
	item_dir TEXT NOT NULL DEFAULT '',
	total_bytes INTEGER NOT NULL DEFAULT 0,
	upgrade_available INTEGER NOT NULL DEFAULT 0,
	upgrade_hint TEXT NOT NULL DEFAULT '',
	json_data TEXT NOT NULL
);
CREATE INDEX idx_library_items_pub_ts ON library_items(pub_ts DESC);
CREATE INDEX idx_library_items_mid ON library_items(mid);
CREATE TABLE item_refs (
	from_key TEXT NOT NULL,
	to_key TEXT NOT NULL,
	PRIMARY KEY (from_key, to_key)
);
CREATE INDEX idx_item_refs_to ON item_refs(to_key);
CREATE TABLE feed_items (
	feed_key TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	kind TEXT NOT NULL,
	raw_id TEXT NOT NULL,
	mid INTEGER NOT NULL DEFAULT 0,
	author_name TEXT NOT NULL DEFAULT '',
	author_face TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	cover_url TEXT NOT NULL DEFAULT '',
	canonical_url TEXT NOT NULL DEFAULT '',
	pub_ts INTEGER NOT NULL DEFAULT 0,
	discovered_at TEXT NOT NULL DEFAULT '',
	cached INTEGER NOT NULL DEFAULT 0,
	cache_status TEXT NOT NULL DEFAULT '',
	item_key TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_feed_items_pub_ts ON feed_items(pub_ts DESC);
CREATE INDEX idx_feed_items_mid ON feed_items(mid);
CREATE TABLE download_tasks (
	task_id TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	kind TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'manual',
	target_id TEXT NOT NULL,
	cid INTEGER NOT NULL DEFAULT 0,
	page INTEGER NOT NULL DEFAULT 1,
	title TEXT NOT NULL DEFAULT '',
	part_title TEXT NOT NULL DEFAULT '',
	mid INTEGER NOT NULL DEFAULT 0,
	author_name TEXT NOT NULL DEFAULT '',
	author_face TEXT NOT NULL DEFAULT '',
	cover_url TEXT NOT NULL DEFAULT '',
	cache_linked_videos INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'queued',
	pause_reason TEXT NOT NULL DEFAULT '',
	error_kind TEXT NOT NULL DEFAULT '',
	error_message TEXT NOT NULL DEFAULT '',
	quality_hint TEXT NOT NULL DEFAULT '',
	bytes_downloaded INTEGER NOT NULL DEFAULT 0,
	total_bytes INTEGER NOT NULL DEFAULT 0,
	progress REAL NOT NULL DEFAULT 0,
	item_key TEXT NOT NULL DEFAULT '',
	item_dir TEXT NOT NULL DEFAULT '',
	files_json TEXT NOT NULL DEFAULT '[]',
	attempts_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
PRAGMA user_version = 1;
`

func dbHasColumn(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name, ctyp string
			notNull    int
			dflt       sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctyp, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(name, col) {
			return true
		}
	}
	return false
}

func dbHasIndex(t *testing.T, db *sql.DB, indexName string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestLibrarySchemaV2UpgradeRollbackAndIdempotency(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := filepath.Join(dataDir, "cache")
	dbPath := filepath.Join(dataDir, "library.db")

	db, _, _, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(frozenV1LibraryDDL); err != nil {
		t.Fatal(err)
	}
	itemJSON := `{"version":1,"id":"BV10001","site":"bilibili","kind":"video","status":"complete","title":"V1 Video","url":"https://www.bilibili.com/video/BV10001","pubTs":1700000100,"cachedAt":"2026-01-01T00:00:00Z","author":{"mid":42001,"name":"CreatorA","face":""}}`
	if _, err := db.Exec(`INSERT INTO library_items (item_key, site, kind, raw_id, mid, author_name, title, pub_ts, json_data) VALUES ('bilibili:video:BV10001','bilibili','video','BV10001',42001,'CreatorA','V1 Video',1700000100,?)`, itemJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO feed_items (feed_key, site, kind, raw_id, mid, author_name, title, pub_ts, cached, item_key) VALUES ('bilibili:video:BV10001','bilibili','video','BV10001',42001,'CreatorA','V1 Video',1700000100,1,'bilibili:video:BV10001')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO download_tasks (task_id, site, kind, target_id, mid, author_name, created_at, updated_at) VALUES ('task-1','bilibili','video','BV10001',42001,'CreatorA','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// Inject mid-SQL-transaction fault before commit: entire DDL + backfill rolls back.
	restore := SetMigrateV2HookForTest(func(tx *sql.Tx) error {
		return errors.New("injected mid-tx fault")
	})
	if _, err := OpenWithDataDir(db, cacheDir, dataDir); err == nil {
		restore()
		t.Fatal("expected mid-tx fault error")
	}
	restore()

	var uv int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv != 1 {
		t.Fatalf("expected user_version=1 after rollback, got %d err=%v", uv, err)
	}
	if dbHasColumn(t, db, "library_items", "author_uid") || dbHasColumn(t, db, "feed_items", "author_uid") || dbHasColumn(t, db, "download_tasks", "author_uid") {
		t.Fatal("author_uid column leaked after rolled-back migration")
	}

	// Clean migration upgrades to v2, backfills author_uid, preserves mid, and builds composite indexes.
	store, err := OpenWithDataDir(db, cacheDir, dataDir)
	if err != nil {
		t.Fatalf("OpenWithDataDir upgrade: %v", err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv != 2 {
		t.Fatalf("expected user_version=2, got %d err=%v", uv, err)
	}
	for _, idx := range []string{
		"idx_library_items_site_author",
		"idx_feed_items_site_author",
		"idx_download_tasks_site_author",
	} {
		if !dbHasIndex(t, db, idx) {
			t.Fatalf("missing index %s", idx)
		}
	}
	for _, tbl := range []string{"library_items", "feed_items", "download_tasks"} {
		var mid int64
		var authorUID string
		if err := db.QueryRow("SELECT mid, author_uid FROM "+tbl+" LIMIT 1").Scan(&mid, &authorUID); err != nil {
			t.Fatalf("query %s: %v", tbl, err)
		}
		if mid != 42001 || authorUID != "42001" {
			t.Fatalf("%s backfill mismatch: mid=%d author_uid=%q", tbl, mid, authorUID)
		}
	}

	// Idempotent reopen on v2 does not re-run migration hook.
	hookCalled := false
	restore2 := SetMigrateV2HookForTest(func(tx *sql.Tx) error {
		hookCalled = true
		return nil
	})
	defer restore2()
	if _, err := OpenWithDataDir(db, cacheDir, dataDir); err != nil {
		t.Fatalf("second OpenWithDataDir: %v", err)
	}
	if hookCalled {
		t.Fatal("v2 database unexpectedly ran migration again")
	}

	stats, err := store.Statistics()
	if err != nil || len(stats.Authors) != 1 || stats.Authors[0].Site != "bilibili" || stats.Authors[0].UID != "42001" || stats.Authors[0].Mid != 42001 {
		t.Fatalf("unexpected migrated Statistics: %+v err=%v", stats, err)
	}
	_ = db.Close()
}

func TestDouyinKeysDirsManifestsZeroSignedURLsAndAddRoot(t *testing.T) {
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

	secUID := "MS4wLjABAAAA__SynthCaseUser_01"
	sum := sha256.Sum256([]byte(secUID))
	wantHash := hex.EncodeToString(sum[:])[:12]

	gotHash, err := DouyinCreatorDirHash(secUID)
	if err != nil || gotHash != wantHash {
		t.Fatalf("DouyinCreatorDirHash=%q want %q err=%v", gotHash, wantHash, err)
	}
	// Case sensitivity: lowercase variant produces a distinct hash.
	lowerHash, err := DouyinCreatorDirHash(strings.ToLower(secUID))
	if err != nil || lowerHash == gotHash {
		t.Fatalf("expected case-sensitive sec_uid hash, got %q vs %q", lowerHash, gotHash)
	}
	for _, bad := range []string{"", "has space", "path/sep", `back\sep`, "tra..versal", "co:lon"} {
		if _, err := DouyinCreatorDirHash(bad); err == nil {
			t.Fatalf("expected invalid sec_uid %q to be rejected", bad)
		}
	}

	if k := CanonicalItemKey("douyin", "video", "7300000001"); k != "douyin:video:7300000001" {
		t.Fatalf("unexpected video key %q", k)
	}
	if k := CanonicalItemKey("douyin", "dynamic", "7300000002"); k != "douyin:dynamic:7300000002" {
		t.Fatalf("unexpected dynamic key %q", k)
	}
	if k := CanonicalItemKey("douyin", "live", "7400000001"); k != "douyin:live:7400000001" {
		t.Fatalf("unexpected live key %q", k)
	}

	// 1. Douyin mixed note manifest (image + motion + video+cover + unknownKind + music) & zero signed URLs.
	noteDir, err := DouyinWorkDir(cacheDir, secUID, "7300000002")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(noteDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		"cover.jpg":          "cover-bytes",
		"images/01.jpg":      "still-image",
		"images/02.jpg":      "live-still",
		"images/02_live.mp4": "live-motion",
		"images/03.jpg":      "video-page-cover",
		"images/03.mp4":      "video-page-stream",
		"images/04.jpg":      "unknown-fallback-image",
		"music.m4a":          "music-audio",
		"user-notes.txt":     "keep-me",
	} {
		if err := os.WriteFile(filepath.Join(noteDir, filepath.FromSlash(rel)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	savedNote, err := store.SaveItem(noteDir, ItemMetadata{
		Version: 1,
		ID:      "7300000002",
		Site:    "douyin",
		Kind:    "dynamic",
		Title:   "Mixed Note",
		Text:    "Synthetic mixed note caption",
		URL:     "https://www.douyin.com/note/7300000002?a_bogus=signed_secret&x-expires=999",
		PubTs:   1710000200,
		Author: ItemAuthor{
			Site:     "douyin",
			UID:      secUID,
			UniqueID: "synth_creator_01",
			Name:     "SynthDouyinCreator",
			Face:     "https://example.invalid/avatar.jpeg?x-expires=1710009999&x-signature=secret",
		},
		Media: []MediaFile{
			{Role: "cover", Path: "cover.jpg", Source: "https://example.invalid/cdn?a_bogus=leak"},
		},
		NotePages: []NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", Width: 1080, Height: 1440},
			{Index: 2, Kind: "motion", Image: "images/02.jpg", Motion: "images/02_live.mp4"},
			{
				Index: 3,
				Kind:  "video",
				Cover: "images/03.jpg",
				Video: "images/03.mp4",
				VideoInfo: &DouyinVideoInfo{
					File:           "images/03.mp4",
					Gear:           "adapt_lowest_1080_1",
					Codec:          "h264",
					Width:          1080,
					Height:         1920,
					FPS:            30,
					Bitrate:        2400000,
					AvailableGears: []string{"adapt_lowest_1080_1", "normal_720_0"},
				},
			},
			{Index: 4, Kind: "unknown", UnknownKind: "sticker_card_v9", Image: "images/04.jpg"},
		},
		Music: &MusicInfo{
			ID:       "m_01",
			Title:    "Synth Track",
			Author:   "Synth Musician",
			Duration: 18,
			File:     "music.m4a",
			Cover:    "https://example.invalid/music.jpeg?x-signature=secret",
		},
	})
	if err != nil {
		t.Fatalf("SaveItem mixed note: %v", err)
	}
	if savedNote.Status != "complete" {
		t.Fatalf("expected complete status, got %s", savedNote.Status)
	}

	rawOnDisk, err := os.ReadFile(filepath.Join(noteDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"a_bogus", "x-signature", "x-expires", "example.invalid"} {
		if strings.Contains(string(rawOnDisk), forbidden) {
			t.Fatalf("item.json persisted signed/remote URL fragment %q: %s", forbidden, rawOnDisk)
		}
	}
	if savedNote.URL != "https://www.douyin.com/note/7300000002" {
		t.Fatalf("unexpected sanitized URL: %q", savedNote.URL)
	}
	for _, rel := range []string{"images/01.jpg", "images/02_live.mp4", "images/03.mp4", "music.m4a"} {
		if _, err := store.MediaPath("douyin:dynamic:7300000002", rel); err != nil {
			t.Fatalf("MediaPath(%s) failed: %v", rel, err)
		}
	}

	// 2. External library root with Douyin video + Douyin live + Bilibili collision check.
	extRoot := t.TempDir()
	vidDir, err := DouyinWorkDir(extRoot, secUID, "9009")
	if err != nil {
		t.Fatal(err)
	}
	liveDir, err := DouyinLiveDir(extRoot, secUID, "7400000099")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vidDir, "video.mp4"), []byte("video-mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "part-001.mp4"), []byte("live-seg"), 0o600); err != nil {
		t.Fatal(err)
	}
	vidManifest := `{"version":1,"id":"9009","site":"douyin","kind":"video","title":"Ext Video","url":"https://www.douyin.com/video/9009","pubTs":1710000300,"author":{"site":"douyin","uid":"` + secUID + `","uniqueId":"synth_creator_01","name":"SynthDouyinCreator"},"douyinVideo":{"file":"video.mp4","gear":"normal_1080_0","codec":"h264","fps":60,"bitrate":3800000}}`
	liveManifest := `{"version":1,"id":"7400000099","site":"douyin","kind":"live","title":"Ext Live","url":"https://live.douyin.com/888123","pubTs":1710000400,"author":{"site":"douyin","uid":"` + secUID + `","name":"SynthDouyinCreator"},"live":{"room_id":"7400000099","webRid":"888123","source":"flv","segments":[{"file":"part-001.mp4","bytes":8,"codec":"h264"}]}}`
	if err := os.WriteFile(filepath.Join(vidDir, "item.json"), []byte(vidManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "item.json"), []byte(liveManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	// Also cache a Bilibili item with the same raw ID "9009" to verify strict cross-site key isolation.
	biliDir := filepath.Join(cacheDir, "bilibili", "555", "videos", "9009")
	if err := os.MkdirAll(biliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(biliDir, "P01.mp4"), []byte("bili-mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(biliDir, ItemMetadata{
		ID:     "9009",
		Site:   "bilibili",
		Kind:   "video",
		Title:  "Bili 9009",
		PubTs:  1710000100,
		Author: ItemAuthor{Mid: 555, Name: "BiliCreator"},
		Parts:  []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4"}},
	}); err != nil {
		t.Fatal(err)
	}

	addRep, err := store.AddLibraryRoot(extRoot)
	if err != nil || addRep.Added != 2 {
		t.Fatalf("AddLibraryRoot expected Added=2, got %+v err=%v", addRep, err)
	}

	dyVid, ok, err := store.GetItem("douyin:video:9009")
	if err != nil || !ok || dyVid.Site != "douyin" || dyVid.DouyinVideo == nil || dyVid.DouyinVideo.FPS != 60 {
		t.Fatalf("GetItem(douyin:video:9009) mismatch: ok=%v meta=%+v err=%v", ok, dyVid, err)
	}
	biliVid, ok, err := store.GetItem("bilibili:video:9009")
	if err != nil || !ok || biliVid.Site != "bilibili" || biliVid.Author.Mid != 555 {
		t.Fatalf("GetItem(bilibili:video:9009) mismatch: ok=%v meta=%+v err=%v", ok, biliVid, err)
	}
	if _, ok, _ := store.GetItem("douyin:video:missing-9009"); ok {
		t.Fatal("non-existent douyin key matched another item")
	}
	dyLive, ok, err := store.GetItem("douyin:live:7400000099")
	if err != nil || !ok || dyLive.Live == nil || dyLive.Live.WebRID != "888123" || dyLive.Live.RoomID != 7400000099 {
		t.Fatalf("GetItem(douyin:live:7400000099) mismatch: ok=%v live=%+v err=%v", ok, dyLive.Live, err)
	}

	// 3. Multi-site author filters, Statistics(), LatestPubByAuthor(), and HomeSeen helpers.
	dyOnly, err := store.ListItems(FeedFilter{AuthorRefs: []AuthorRef{{Site: "douyin", UID: secUID}}})
	if err != nil || len(dyOnly) != 3 {
		t.Fatalf("expected 3 Douyin items by AuthorRef, got %d err=%v", len(dyOnly), err)
	}
	page, err := store.FeedPage(FeedPageRequest{Site: "douyin", Limit: 10})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("expected 3 Douyin feed items, got %d err=%v", len(page.Items), err)
	}
	stats, err := store.Statistics()
	if err != nil || len(stats.Authors) != 2 {
		t.Fatalf("expected 2 distinct (site, author_uid) authors in Statistics, got %+v err=%v", stats.Authors, err)
	}
	latestByAuthor, err := store.LatestPubByAuthor()
	if err != nil || len(latestByAuthor) != 2 || latestByAuthor[0].Site != "douyin" || latestByAuthor[0].LatestPubTs != 1710000400 {
		t.Fatalf("unexpected LatestPubByAuthor: %+v err=%v", latestByAuthor, err)
	}
	seenMap := map[string]int64{
		"mid:555":                           1700000000,
		AuthorHomeSeenKey("douyin", secUID): 1710000350,
	}
	if got := LookupAuthorHomeSeen(seenMap, "bilibili", "555"); got != 1700000000 {
		t.Fatalf("legacy mid HomeSeen fallback failed: got %d", got)
	}
	if got := LookupAuthorHomeSeen(seenMap, "douyin", secUID); got != 1710000350 {
		t.Fatalf("douyin AuthorHomeSeenKey lookup failed: got %d", got)
	}

	// 4. Delete Douyin note removes registered notePages/music files and preserves user-notes.txt.
	delPlan, err := store.Delete("douyin:dynamic:7300000002")
	if err != nil {
		t.Fatalf("Delete mixed note: %v", err)
	}
	if len(delPlan.PreservedUserFiles) != 1 || delPlan.PreservedUserFiles[0] != "user-notes.txt" {
		t.Fatalf("expected user-notes.txt preserved, got %+v", delPlan.PreservedUserFiles)
	}
	if _, err := os.Stat(filepath.Join(noteDir, "music.m4a")); !os.IsNotExist(err) {
		t.Fatalf("music.m4a should be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(noteDir, "user-notes.txt")); err != nil {
		t.Fatalf("user-notes.txt must remain: %v", err)
	}
}
