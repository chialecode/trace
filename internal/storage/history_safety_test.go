package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestHistorySharedThumbnailRespectsWindowsFilenameCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filename comparison")
	}
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	name, err := store.SaveThumb("video", "legacy-case", "cover", ".png", []byte("shared image"))
	if err != nil {
		t.Fatal(err)
	}
	for i, reference := range []string{name, strings.ToUpper(name)} {
		if err := store.Record(HistoryEntry{Kind: "video", ID: string(rune('a' + i)), CoverThumb: reference}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Remove("video", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath(strings.ToUpper(name)); err != nil {
		t.Fatalf("deleting a lowercase reference removed the same file used by an uppercase reference: %v", err)
	}
	if err := store.Remove("video", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), name)); !os.IsNotExist(err) {
		t.Fatalf("last case-insensitive reference retained its thumbnail: %v", err)
	}
}

func TestHistoryCleanupKeepsSharedThumbnails(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := store.SaveThumb("video", "shared", "cover", ".png", []byte("shared image"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := store.Record(HistoryEntry{Kind: "video", ID: id, CoverThumb: thumb}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Remove("video", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath(thumb); err != nil {
		t.Fatalf("removing one history entry removed a thumbnail still used by another: %v", err)
	}
	if err := store.Remove("video", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), thumb)); !os.IsNotExist(err) {
		t.Fatalf("last reference did not clean up its thumbnail: %v", err)
	}
}

func TestHistoryCleanupNeverUsesIDPrefixes(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	var retained string
	for _, id := range []string{"part", "part_alt"} {
		thumb, err := store.SaveThumb("video", id, "cover", ".png", []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Record(HistoryEntry{Kind: "video", ID: id, CoverThumb: thumb}); err != nil {
			t.Fatal(err)
		}
		if id == "part_alt" {
			retained = thumb
		}
	}
	if err := store.Remove("video", "part"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath(retained); err != nil {
		t.Fatalf("cleanup matched a different object's ID prefix: %v", err)
	}
}

func TestHistoryClearPreservesUnregisteredFiles(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := store.SaveThumb("video", "owned", "cover", ".png", []byte("owned image"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(HistoryEntry{Kind: "video", ID: "owned", CoverThumb: thumb}); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(store.ThumbsDir(), "my-photo.png")
	if err := os.WriteFile(userPath, []byte("user file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ThumbPath("my-photo.png"); err == nil {
		t.Fatal("history media access served an unregistered file")
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(userPath); err != nil || string(raw) != "user file" {
		t.Fatalf("clearing history deleted an unregistered file: raw=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), thumb)); !os.IsNotExist(err) {
		t.Fatalf("clearing history left its registered thumbnail: %v", err)
	}
}

func TestHistoryThumbUpdatesCannotOverwriteReferencedFiles(t *testing.T) {
	dir := t.TempDir()
	historyPath := filepath.Join(dir, "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	oldName, err := store.SaveThumb("video", "BV1Case", "cover", ".png", []byte("old image"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(HistoryEntry{Kind: "video", ID: "BV1Case", CoverThumb: oldName}); err != nil {
		t.Fatal(err)
	}
	newName, err := store.SaveThumb("video", "BV1Case", "cover", ".png", []byte("new image"))
	if err != nil {
		t.Fatal(err)
	}
	if newName == oldName {
		t.Fatal("a refreshed thumbnail overwrites a file referenced by the committed history")
	}
	otherName, err := store.SaveThumb("video", "BV1case", "cover", ".png", []byte("other image"))
	if err != nil {
		t.Fatal(err)
	}
	if otherName == newName {
		t.Fatal("case-sensitive object IDs collided in the thumbnail cache")
	}
	if err := os.Remove(historyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(HistoryEntry{Kind: "video", ID: "BV1Case", CoverThumb: newName}); err == nil {
		t.Fatal("expected the history write to fail")
	}
	if items := store.List(); len(items) != 1 || items[0].CoverThumb != oldName {
		t.Fatalf("failed history write changed the committed entry: %+v", items)
	}
	if raw, err := os.ReadFile(filepath.Join(store.ThumbsDir(), oldName)); err != nil || string(raw) != "old image" {
		t.Fatalf("failed history update changed the old thumbnail: raw=%q err=%v", raw, err)
	}
}

func TestHistoryThumbnailCommitFailureDiscardsOnlyNewFiles(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Kind: "video", ID: "BV1Commit"}, HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("original")}); err != nil {
		t.Fatal(err)
	}
	old := store.List()[0]
	if err := os.Remove(historyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Kind: "video", ID: "BV1Commit"}, HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("replacement")}); err == nil {
		t.Fatal("expected history commit failure")
	}
	entries, err := os.ReadDir(store.ThumbsDir())
	if err != nil || len(entries) != 1 || entries[0].Name() != old.CoverThumb {
		t.Fatalf("failed thumbnail commit left new files or removed the old one: %+v err=%v", entries, err)
	}
	if got := store.List(); len(got) != 1 || got[0].CoverThumb != old.CoverThumb {
		t.Fatalf("failed commit changed the history reference: %+v", got)
	}
}

func TestHistoryThumbnailCleanupFailureIsRecoverableAfterRestart(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Kind: "user", ID: "123"}, HistoryThumbnail{Role: "avatar", Ext: ".png", Data: []byte("avatar")}); err != nil {
		t.Fatal(err)
	}
	thumb := store.List()[0].AvatarThumb
	thumbPath := filepath.Join(store.ThumbsDir(), thumb)
	if err := os.Remove(thumbPath); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be removed as a registered thumbnail file. This
	// produces the same retry path as a sharing or permission failure.
	if err := os.Mkdir(thumbPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err == nil {
		t.Fatal("thumbnail cleanup failure was hidden")
	}
	if len(store.List()) != 0 {
		t.Fatal("the successful metadata commit was rolled back after cleanup failed")
	}
	if err := os.Remove(thumbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbPath, []byte("avatar"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Clear(); err != nil {
		t.Fatalf("persisted cleanup could not be retried: %v", err)
	}
	if _, err := os.Stat(thumbPath); !os.IsNotExist(err) {
		t.Fatalf("restart lost the pending thumbnail cleanup: %v", err)
	}
}

// Thumbnails fetched after an entry was recorded keep its place,
// replace its old image and are not written for an entry removed meanwhile.
func TestHistoryAttachThumbsKeepsPlaceAndSkipsRemovedEntries(t *testing.T) {
	store, _, err := OpenHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(HistoryEntry{Site: "bilibili", Kind: "video", ID: "BV1Attach"}, HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("old cover")}); err != nil {
		t.Fatal(err)
	}
	old := store.List()[0].CoverThumb
	if err := store.Record(HistoryEntry{Site: "bilibili", Kind: "user", ID: "42"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachThumbs("bilibili", "video", "BV1Attach", HistoryThumbnail{Role: "cover", Ext: ".png", Data: []byte("new cover")}, HistoryThumbnail{Role: "avatar", Ext: ".png", Data: []byte("avatar")}); err != nil {
		t.Fatal(err)
	}
	items := store.List()
	if len(items) != 2 || items[0].ID != "42" || items[1].ID != "BV1Attach" {
		t.Fatalf("attaching thumbnails moved the entry: %+v", items)
	}
	if items[1].CoverThumb == "" || items[1].CoverThumb == old || items[1].AvatarThumb == "" {
		t.Fatalf("thumbnails were not attached: %+v", items[1])
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), old)); !os.IsNotExist(err) {
		t.Fatalf("the replaced cover was kept: %v", err)
	}

	if err := store.Remove("user", "42"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(store.ThumbsDir())
	if err := store.AttachThumbs("bilibili", "user", "42", HistoryThumbnail{Role: "avatar", Ext: ".png", Data: []byte("late avatar")}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(store.ThumbsDir())
	if len(after) != len(before) || len(store.List()) != 1 {
		t.Fatalf("a removed entry came back or got a file: %d -> %d files, %+v", len(before), len(after), store.List())
	}
}

func TestHistoryMultiSiteIsolationAndEmptySiteDefault(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	// Empty site defaults to "bilibili" and stays isolated from "douyin" with identical kind+id+bytes.
	if err := store.RecordWithThumbs(
		HistoryEntry{Kind: "video", ID: "9001", Title: "Bilibili Title"},
		HistoryThumbnail{Role: "cover", Ext: ".jpg", Data: []byte("identical-bytes")},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWithThumbs(
		HistoryEntry{
			Site:         "douyin",
			Kind:         "video",
			ID:           "9001",
			Title:        "My Signature Dish",
			CanonicalURL: "https://www.douyin.com/video/9001?a_bogus=secret123",
		},
		HistoryThumbnail{Role: "cover", Ext: ".jpg", Data: []byte("identical-bytes")},
	); err != nil {
		t.Fatal(err)
	}

	items := store.List()
	if len(items) != 2 {
		t.Fatalf("expected 2 isolated multi-site entries, got %+v", items)
	}
	var dyItem, biliItem HistoryEntry
	for _, it := range items {
		switch it.Site {
		case "douyin":
			dyItem = it
		case "bilibili":
			biliItem = it
		}
	}
	if dyItem.CoverThumb == "" || biliItem.CoverThumb == "" || dyItem.CoverThumb == biliItem.CoverThumb {
		t.Fatalf("multi-site thumbnails collided or missing: dy=%q bili=%q", dyItem.CoverThumb, biliItem.CoverThumb)
	}
	if dyItem.CanonicalURL != "https://www.douyin.com/video/9001" {
		t.Fatalf("signed query not stripped on douyin canonical URL: %q", dyItem.CanonicalURL)
	}
	if dyItem.Title != "My Signature Dish" {
		t.Fatalf("benign title unexpectedly redacted: %q", dyItem.Title)
	}

	// Removing with legacy Remove("video", "9001") (or empty site) removes only bilibili:video:9001.
	if err := store.Remove("video", "9001"); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 1 || store.List()[0].Site != "douyin" {
		t.Fatalf("removing bilibili entry affected douyin entry: %+v", store.List())
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), biliItem.CoverThumb)); !os.IsNotExist(err) {
		t.Fatalf("bilibili thumbnail was not cleaned up: %v", err)
	}
	if _, err := store.ThumbPath(dyItem.CoverThumb); err != nil {
		t.Fatalf("douyin thumbnail was deleted when removing bilibili entry: %v", err)
	}

	// Removing douyin entry cleans up douyin thumbnail; history.json stays version 1.
	if err := store.RemoveBySite("douyin", "video", "9001"); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("douyin entry remained after RemoveBySite: %+v", store.List())
	}
	if _, err := os.Stat(filepath.Join(store.ThumbsDir(), dyItem.CoverThumb)); !os.IsNotExist(err) {
		t.Fatalf("douyin thumbnail was not cleaned up: %v", err)
	}

	v, exists, err := ReadSchemaVersion(historyPath, SchemaJSON)
	if err != nil || !exists || v != 1 {
		t.Fatalf("history.json version must stay 1, got v=%d exists=%v err=%v", v, exists, err)
	}

	// Douyin cookie/token markers in IDs are rejected and redacted in text.
	if err := store.Record(HistoryEntry{Site: "douyin", Kind: "user", ID: "sessionid=leak"}); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("credential marker ID should be rejected: %+v", store.List())
	}
}

func TestHistoryWeiboCanonicalURLsSensitiveMarkersAndUnknownSiteIsolation(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Weibo entries with empty CanonicalURL use explicit Weibo URLs and never fall back to Bilibili.
	cases := []struct {
		entry   HistoryEntry
		wantURL string
	}{
		{
			entry:   HistoryEntry{Site: "weibo", Kind: "dynamic", ID: "5000000000000001", Title: "合成微博正文"},
			wantURL: "https://weibo.com/detail/5000000000000001",
		},
		{
			entry:   HistoryEntry{Site: "weibo", Kind: "video", ID: "1034_5000000000000001", Title: "合成独立视频"},
			wantURL: "https://weibo.com/tv/show/1034:5000000000000001",
		},
		{
			entry:   HistoryEntry{Site: "weibo", Kind: "user", ID: "1000000001", Title: "合成博主"},
			wantURL: "https://weibo.com/u/1000000001",
		},
		{
			entry:   HistoryEntry{Site: "weibo", Kind: "dynamic", ID: "5000000000000002", Title: "带签名链接", CanonicalURL: "https://weibo.com/1000000001/O00000002?Expires=1999999999&ssig=SYNTHSIG01&KID=unistore,video"},
			wantURL: "https://weibo.com/1000000001/O00000002",
		},
		{
			entry:   HistoryEntry{Site: "unknownsite", Kind: "dynamic", ID: "999001", Title: "未知站点"},
			wantURL: "",
		},
	}
	for _, tc := range cases {
		if err := store.Record(tc.entry); err != nil {
			t.Fatalf("Record(%+v): %v", tc.entry, err)
		}
	}
	byKey := make(map[string]HistoryEntry)
	for _, it := range store.List() {
		byKey[historyKey(it)] = it
	}
	for _, tc := range cases {
		got, ok := byKey[historyKey(tc.entry)]
		if !ok {
			t.Fatalf("missing recorded entry %+v", tc.entry)
		}
		if got.CanonicalURL != tc.wantURL {
			t.Fatalf("entry (%s,%s,%s) CanonicalURL = %q, want %q", tc.entry.Site, tc.entry.Kind, tc.entry.ID, got.CanonicalURL, tc.wantURL)
		}
		if tc.entry.Site != "bilibili" && strings.Contains(got.CanonicalURL, "bilibili.com") {
			t.Fatalf("non-bilibili entry fell back to bilibili URL: %+v", got)
		}
	}

	// 2. Weibo cookie & signed URL markers are rejected in IDs and redacted in titles/subtitles.
	sensitiveSamples := []string{
		"SUB=SYNTHETIC_SUB_TOKEN",
		"SUBP=SYNTHETIC_SUBP_TOKEN",
		"XSRF-TOKEN=SYNTHETIC_XSRF",
		"WBPSESS=SYNTHETIC_WBPSESS",
		"SCF=SYNTHETIC_SCF",
		"ALF=1999999999",
		"SSOLoginState=1999999999",
		"ssig=SYNTHETIC_SSIG",
		"KID=unistore,video",
		"livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001.mov%3FExpires%3D1999999999%26ssig%3DENCODED_SSIG_TOK%26KID%3Dunistore%2Cvideo",
		"SUB%3DENCODED_SUB_TOK",
	}
	for i, sample := range sensitiveSamples {
		if err := store.Record(HistoryEntry{Site: "weibo", Kind: "dynamic", ID: sample, Title: "ignored"}); err != nil {
			t.Fatal(err)
		}
		id := "50000000000009" + strconv.Itoa(10+i)
		if err := store.Record(HistoryEntry{
			Site:         "weibo",
			Kind:         "dynamic",
			ID:           id,
			Title:        "leaked " + sample,
			Subtitle:     "sub " + sample,
			CanonicalURL: "https://weibo.com/" + sample + "/detail",
		}); err != nil {
			t.Fatal(err)
		}
	}

	rawBytes, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(rawBytes)
	for _, forbidden := range []string{
		"SYNTHETIC_SUB_TOKEN", "SYNTHETIC_SUBP_TOKEN", "SYNTHETIC_XSRF",
		"SYNTHETIC_WBPSESS", "SYNTHETIC_SCF", "SYNTHETIC_SSIG", "SYNTHSIG01",
		"ENCODED_SSIG_TOK", "ENCODED_SUB_TOK", "unistore",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("history.json leaked sensitive Weibo token %q:\n%s", forbidden, raw)
		}
	}
}

func TestHistoryXCanonicalURLsSensitiveMarkersAndV1Preservation(t *testing.T) {
	dir := t.TempDir()
	historyPath := filepath.Join(dir, "history.json")
	store, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. X entries with empty CanonicalURL use explicit x.com fallbacks and never fall back to Bilibili,
	// while explicit x.com URLs strip query strings and preserve clean canonical paths.
	cases := []struct {
		entry   HistoryEntry
		wantURL string
	}{
		{
			entry:   HistoryEntry{Site: "x", Kind: "dynamic", ID: "1000000000000000009", Title: "合成 X 帖子"},
			wantURL: "https://x.com/i/web/status/1000000000000000009",
		},
		{
			entry:   HistoryEntry{Site: "x", Kind: "post", ID: "1000000000000000010", Title: "合成 X post 别名"},
			wantURL: "https://x.com/i/web/status/1000000000000000010",
		},
		{
			entry:   HistoryEntry{Site: "x", Kind: "user", ID: "1000000000000000001", Title: "合成 X 用户"},
			wantURL: "https://x.com/i/user/1000000000000000001",
		},
		{
			entry:   HistoryEntry{Site: "x", Kind: "dynamic", ID: "1000000000000000011", Title: "带追踪参数链接", CanonicalURL: "https://x.com/example_user/status/1000000000000000011?s=20&t=SYNTH_TRACK"},
			wantURL: "https://x.com/example_user/status/1000000000000000011",
		},
	}
	for _, tc := range cases {
		if err := store.RecordWithThumbs(tc.entry, HistoryThumbnail{Role: "cover", Ext: ".jpg", Data: []byte("thumb-" + tc.entry.ID)}); err != nil {
			t.Fatalf("RecordWithThumbs(%+v): %v", tc.entry, err)
		}
	}

	// Also record a Bilibili entry with the same numeric ID to prove site isolation.
	if err := store.Record(HistoryEntry{Site: "bilibili", Kind: "dynamic", ID: "1000000000000000009", Title: "同ID B站动态"}); err != nil {
		t.Fatal(err)
	}

	// Verify history.json remains schema version 1 and preserves all site="x" entries across reload and subsequent saves.
	v, exists, err := ReadSchemaVersion(historyPath, SchemaJSON)
	if err != nil || !exists || v != 1 {
		t.Fatalf("history.json version must stay 1, got v=%d exists=%v err=%v", v, exists, err)
	}
	reopened, _, err := OpenHistory(historyPath)
	if err != nil {
		t.Fatalf("reopen history.json v1 with site=x: %v", err)
	}
	if err := reopened.RemoveBySite("bilibili", "dynamic", "1000000000000000009"); err != nil {
		t.Fatalf("RemoveBySite bilibili: %v", err)
	}
	byKey := make(map[string]HistoryEntry)
	for _, it := range reopened.List() {
		byKey[historyKey(it)] = it
	}
	for _, tc := range cases {
		got, ok := byKey[historyKey(tc.entry)]
		if !ok {
			t.Fatalf("site=x entry lost across v1 reload/save: %+v", tc.entry)
		}
		if got.Site != "x" || got.CanonicalURL != tc.wantURL {
			t.Fatalf("entry (%s,%s,%s) = (site=%q, url=%q), want (x, %q)", tc.entry.Site, tc.entry.Kind, tc.entry.ID, got.Site, got.CanonicalURL, tc.wantURL)
		}
	}

	// 2. X cookie, CSRF, and Bearer token markers (raw & URL-encoded) are rejected in IDs and redacted in text/URL paths.
	sensitiveSamples := []string{
		"auth_token=SYNTHETIC_X_AUTH_TOKEN_01",
		"ct0=SYNTHETIC_X_CT0_TOKEN_02",
		"kdt=SYNTHETIC_X_KDT_TOKEN_03",
		"twid=u%3DSYNTHETIC_X_TWID_04",
		"guest_id=v1%3ASYNTHETIC_X_GUEST_05",
		"x-csrf-token: SYNTHETIC_X_CSRF_HDR_06",
		"Authorization: Bearer SYNTHETIC_X_BEARER_07",
		"auth_token%3DSYNTHETIC_X_AUTH_ENC_08",
		"ct0%3dSYNTHETIC_X_CT0_ENC_09",
		"kdt%3DSYNTHETIC_X_KDT_ENC_10",
		"x-csrf-token%3DSYNTHETIC_X_CSRF_ENC_11",
		"Authorization%3A%20Bearer%20SYNTHETIC_X_BEARER_ENC_12",
	}
	for i, sample := range sensitiveSamples {
		if err := reopened.Record(HistoryEntry{Site: "x", Kind: "dynamic", ID: sample, Title: "ignored"}); err != nil {
			t.Fatal(err)
		}
		id := "10000000000000099" + strconv.Itoa(10+i)
		if err := reopened.Record(HistoryEntry{
			Site:         "x",
			Kind:         "dynamic",
			ID:           id,
			Title:        "leaked " + sample,
			Subtitle:     "sub " + sample,
			CanonicalURL: "https://x.com/" + sample + "/status/" + id,
		}); err != nil {
			t.Fatal(err)
		}
	}

	rawBytes, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(rawBytes)
	for _, forbidden := range []string{
		"SYNTH_TRACK",
		"SYNTHETIC_X_AUTH_TOKEN_01",
		"SYNTHETIC_X_CT0_TOKEN_02",
		"SYNTHETIC_X_KDT_TOKEN_03",
		"SYNTHETIC_X_TWID_04",
		"SYNTHETIC_X_GUEST_05",
		"SYNTHETIC_X_CSRF_HDR_06",
		"SYNTHETIC_X_BEARER_07",
		"SYNTHETIC_X_AUTH_ENC_08",
		"SYNTHETIC_X_CT0_ENC_09",
		"SYNTHETIC_X_KDT_ENC_10",
		"SYNTHETIC_X_CSRF_ENC_11",
		"SYNTHETIC_X_BEARER_ENC_12",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("history.json leaked sensitive X token %q:\n%s", forbidden, raw)
		}
	}
}
