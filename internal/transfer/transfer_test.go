package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/storage"
)

func shaOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func newLib(t *testing.T) *library.Store {
	t.Helper()
	dir := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lib, err := library.OpenWithDataDir(db, filepath.Join(dir, "cache"), dir)
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

// writeItem writes files and an item.json listing them (with their real
// bytes and SHA-256 unless the manifest is told otherwise).
type file struct {
	path, role string
	data       []byte
	listBytes  int64  // overrides the listed size when nonzero
	listSHA    string // overrides the listed SHA-256 when nonempty
	absent     bool   // listed but not written
}

func writeItem(t *testing.T, dir string, meta library.ItemMetadata, files ...file) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, s := int64(len(f.data)), shaOf(f.data)
		if f.listBytes != 0 {
			b = f.listBytes
		}
		if f.listSHA != "" {
			s = f.listSHA
		}
		if strings.HasPrefix(f.path, "part-") && meta.Live != nil {
			meta.Live.Segments = append(meta.Live.Segments, library.LiveSegmentInfo{File: f.path, Bytes: b, SHA256: s})
		}
		meta.Media = append(meta.Media, library.MediaFile{Role: f.role, Path: f.path, Bytes: b, SHA256: s})
		if !f.absent {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, f.path)), 0o700)
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(f.path)), f.data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if meta.Version == 0 {
		meta.Version = 1
	}
	raw, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "item.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func video(id, title string) library.ItemMetadata {
	return library.ItemMetadata{ID: "video:" + id, Site: "bilibili", Kind: "video", Title: title, Status: "complete", Author: library.ItemAuthor{Mid: 1001, Name: "UP"}}
}

// localItem saves an item into the local library.
func localItem(t *testing.T, lib *library.Store, rel string, meta library.ItemMetadata, files ...file) library.ItemMetadata {
	t.Helper()
	dir := filepath.Join(lib.CacheDir(), filepath.FromSlash(rel))
	_ = os.MkdirAll(dir, 0o700)
	for _, f := range files {
		_ = os.WriteFile(filepath.Join(dir, f.path), f.data, 0o600)
		meta.Media = append(meta.Media, library.MediaFile{Role: f.role, Path: f.path, Bytes: int64(len(f.data)), SHA256: shaOf(f.data)})
		if strings.HasPrefix(f.path, "part-") && meta.Live != nil {
			meta.Live.Segments = append(meta.Live.Segments, library.LiveSegmentInfo{File: f.path, Bytes: int64(len(f.data)), SHA256: shaOf(f.data)})
		}
	}
	saved, err := lib.SaveItem(dir, meta)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			st, _ := d.Info()
			out[p] = shaOf(b) + st.ModTime().String()
		}
		return nil
	})
	return out
}

func entryFor(t *testing.T, plan Plan, relDir string) Entry {
	t.Helper()
	for _, e := range plan.Entries {
		if e.RelDir == relDir || strings.HasSuffix(filepath.ToSlash(e.SourceDir), relDir) {
			return e
		}
	}
	t.Fatalf("no entry for %s in %+v", relDir, plan.Entries)
	return Entry{}
}

// buildSource makes a folder with at least one item of every category
// against the local library.
func buildSource(t *testing.T, lib *library.Store) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "library")
	vid := func(id string) string { return "bilibili/1001_UP/videos/" + id }
	// 新条目
	writeItem(t, filepath.Join(src, vid("BVnew")), video("BVnew", "新的"), file{path: "P01.mp4", role: "video", data: []byte("new-video")}, file{path: "cover.jpg", role: "cover", data: []byte("cover")})
	// 可补充: local has P01, the import also has P02.
	localItem(t, lib, vid("BVsup"), video("BVsup", "补充"), file{path: "P01.mp4", role: "video", data: []byte("sup-p1")})
	writeItem(t, filepath.Join(src, vid("BVsup")), video("BVsup", "补充"), file{path: "P01.mp4", role: "video", data: []byte("sup-p1")}, file{path: "P02.mp4", role: "video", data: []byte("sup-p2")})
	// 相同
	localItem(t, lib, vid("BVsame"), video("BVsame", "相同"), file{path: "P01.mp4", role: "video", data: []byte("same")})
	writeItem(t, filepath.Join(src, vid("BVsame")), video("BVsame", "相同"), file{path: "P01.mp4", role: "video", data: []byte("same")})
	// 冲突: same path, other content.
	localItem(t, lib, vid("BVconf"), video("BVconf", "冲突"), file{path: "P01.mp4", role: "video", data: []byte("local-content")}, file{path: "extra.jpg", role: "cover", data: []byte("local-extra")})
	writeItem(t, filepath.Join(src, vid("BVconf")), video("BVconf", "冲突"), file{path: "P01.mp4", role: "video", data: []byte("imported-content")}, file{path: "P02.mp4", role: "video", data: []byte("imported-p2")})
	// 冲突: another author folder.
	localItem(t, lib, "bilibili/2002_其他/videos/BVfolder", video("BVfolder", "别的文件夹"), file{path: "P01.mp4", role: "video", data: []byte("f")})
	writeItem(t, filepath.Join(src, vid("BVfolder")), video("BVfolder", "别的文件夹"), file{path: "P01.mp4", role: "video", data: []byte("f")})
	// 未传完: missing, wrong size, wrong SHA-256.
	writeItem(t, filepath.Join(src, vid("BVmiss")), video("BVmiss", "缺文件"), file{path: "P01.mp4", role: "video", data: []byte("ok")}, file{path: "P02.mp4", role: "video", data: []byte("gone"), absent: true})
	writeItem(t, filepath.Join(src, vid("BVsize")), video("BVsize", "大小不符"), file{path: "P01.mp4", role: "video", data: []byte("ok")}, file{path: "P02.mp4", role: "video", data: []byte("short"), listBytes: 999})
	writeItem(t, filepath.Join(src, vid("BVhash")), video("BVhash", "哈希不符"), file{path: "P01.mp4", role: "video", data: []byte("ok")}, file{path: "P02.mp4", role: "video", data: []byte("x"), listSHA: strings.Repeat("0", 64)})
	// 无法识别: no item.json, a newer version, a path leaving the folder.
	_ = os.MkdirAll(filepath.Join(src, vid("BVnojson")), 0o700)
	_ = os.WriteFile(filepath.Join(src, vid("BVnojson"), "P01.mp4"), []byte("orphan"), 0o600)
	v2 := video("BVv2", "新版本")
	v2.Version = 9
	writeItem(t, filepath.Join(src, vid("BVv2")), v2, file{path: "P01.mp4", role: "video", data: []byte("v2")})
	writeItem(t, filepath.Join(src, vid("BVesc")), video("BVesc", "越界"), file{path: "../../escape.mp4", role: "video", data: []byte("e"), absent: true})
	// Unfinished names are never part of a scan.
	_ = os.WriteFile(filepath.Join(src, vid("BVnew"), "P02.mp4.part"), []byte("half"), 0o600)
	_ = os.WriteFile(filepath.Join(src, vid("BVnew"), ".trace-item.json.1-2.tmp"), []byte("{"), 0o600)
	return src
}

// The scan sorts every kind correctly and writes nothing.
func TestScanSortsSixCategoriesWithoutWriting(t *testing.T) {
	lib := newLib(t)
	src := buildSource(t, lib)
	beforeSrc, beforeLib := snapshotTree(t, src), snapshotTree(t, lib.CacheDir())
	plan, err := Scan(context.Background(), src, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Category{
		"BVnew": CategoryNew, "BVsup": CategorySupplement, "BVsame": CategorySame, "BVconf": CategoryConflict, "BVfolder": CategoryConflict,
		"BVmiss": CategoryIncomplete, "BVsize": CategoryIncomplete, "BVhash": CategoryIncomplete,
		"BVnojson": CategoryUnrecognized, "BVv2": CategoryUnrecognized, "BVesc": CategoryUnrecognized,
	}
	for id, cat := range want {
		e := entryFor(t, plan, "videos/"+id)
		if e.Category != cat {
			t.Errorf("%s = %s (%s), want %s", id, e.Category, e.Reason, cat)
		}
	}
	if len(plan.Entries) != len(want) {
		t.Errorf("entries = %d, want %d", len(plan.Entries), len(want))
	}
	if e := entryFor(t, plan, "videos/BVconf"); len(e.Conflicts) != 1 || e.Conflicts[0] != "P01.mp4" {
		t.Errorf("conflict paths = %v", e.Conflicts)
	}
	if e := entryFor(t, plan, "videos/BVfolder"); !e.OtherFolder {
		t.Error("another author folder is not flagged")
	}
	if e := entryFor(t, plan, "videos/BVnew"); e.CopyFiles != 2 {
		t.Errorf("new item copies %d files, want 2 (no .part)", e.CopyFiles)
	}
	if after := snapshotTree(t, src); len(after) != len(beforeSrc) {
		t.Fatal("the scan changed the source")
	}
	for p, v := range snapshotTree(t, lib.CacheDir()) {
		if beforeLib[p] != v {
			t.Fatalf("the scan changed %s", p)
		}
	}
	// Cancelled scans stop.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, src, lib, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan = %v", err)
	}
}

// New items are indexed and readable; supplements copy only what
// is missing; the three conflict answers give the right files and
// manifests; an incomplete live imports its verified segments first and
// completes after the rest arrives.
func TestApplyNewSupplementConflictsAndIncomplete(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{library.MergeKeepLocal, library.MergeUseImported, library.MergeUnion} {
		t.Run(mode, func(t *testing.T) {
			lib := newLib(t)
			src := buildSource(t, lib)
			plan, err := Scan(ctx, src, lib, nil)
			if err != nil {
				t.Fatal(err)
			}
			sum, err := Apply(ctx, plan, Choices{Conflicts: map[string]string{"bilibili:video:BVconf": mode}}, lib, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(sum.Errors) != 0 {
				t.Fatalf("errors: %v", sum.Errors)
			}
			// New: indexed, readable media path, feed entry.
			nv, ok, _ := lib.GetItem("BVnew")
			if !ok || nv.Status != "complete" {
				t.Fatalf("new item = %+v %v", nv, ok)
			}
			if p, err := lib.MediaPath(nv.ItemKey, "P01.mp4"); err != nil {
				t.Fatalf("MediaPath: %v", err)
			} else if b, _ := os.ReadFile(p); string(b) != "new-video" {
				t.Fatal("new item media differs")
			}
			if feed, _ := lib.QueryFeed(library.FeedFilter{}); !feedHas(feed, nv.ItemKey) {
				t.Fatal("the new item is not in the feed")
			}
			if _, err := os.Stat(filepath.Join(nv.ItemDir, "P02.mp4.part")); err == nil {
				t.Fatal("an unfinished source file was imported")
			}
			// Supplement: P02 added, manifest lists both.
			sv, _, _ := lib.GetItem("BVsup")
			if b, _ := os.ReadFile(filepath.Join(sv.ItemDir, "P02.mp4")); string(b) != "sup-p2" || len(sv.Media) != 2 {
				t.Fatalf("supplement = %+v", sv.Media)
			}
			// Same and folder conflict (no answer) untouched.
			if sum.Skipped < 4 {
				t.Fatalf("skipped = %d", sum.Skipped)
			}
			// Conflict.
			cv, _, _ := lib.GetItem("BVconf")
			p1, _ := os.ReadFile(filepath.Join(cv.ItemDir, "P01.mp4"))
			_, extraErr := os.Stat(filepath.Join(cv.ItemDir, "extra.jpg"))
			p2, _ := os.ReadFile(filepath.Join(cv.ItemDir, "P02.mp4"))
			switch mode {
			case library.MergeKeepLocal, library.MergeUnion:
				if string(p1) != "local-content" || extraErr != nil || string(p2) != "imported-p2" || len(library.RegisteredFiles(cv)) != 3 {
					t.Fatalf("%s: P01=%q extra=%v P02=%q files=%+v", mode, p1, extraErr, p2, library.RegisteredFiles(cv))
				}
			case library.MergeUseImported:
				if string(p1) != "imported-content" || extraErr == nil || string(p2) != "imported-p2" || len(library.RegisteredFiles(cv)) != 2 {
					t.Fatalf("use imported: P01=%q extra=%v P02=%q files=%+v", p1, extraErr, p2, library.RegisteredFiles(cv))
				}
			}
			if cv.Status != "complete" {
				t.Fatalf("conflict item status = %s", cv.Status)
			}
			// Incomplete: the verified file is in, the item is partial.
			mv, ok, _ := lib.GetItem("BVmiss")
			if !ok || mv.Status != "partial" {
				t.Fatalf("incomplete item = %+v", mv)
			}
			if sum.Incomplete != 3 {
				t.Fatalf("incomplete imported = %d, want 3", sum.Incomplete)
			}
		})
	}

	// A live that was still being uploaded: two of three segments first.
	lib := newLib(t)
	src := filepath.Join(t.TempDir(), "cloud")
	liveDir := filepath.Join(src, "bilibili", "1001_UP", "lives", "live_42")
	liveMeta := func() library.ItemMetadata {
		return library.ItemMetadata{ID: "live:42:100", Site: "bilibili", Kind: "live", Title: "直播", Status: "recording", Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
			Live: &library.LiveRecordingInfo{RoomID: 42, LiveSince: 100, StartedAt: "2026-11-19T08:00:00Z"}}
	}
	writeItem(t, liveDir, liveMeta(), file{path: "part-001.mp4", role: "live_segment", data: []byte("seg1")}, file{path: "part-002.mp4", role: "live_segment", data: []byte("seg2")}, file{path: "part-003.mp4", role: "live_segment", data: []byte("seg3"), absent: true})
	plan, _ := Scan(ctx, src, lib, nil)
	if plan.Entries[0].Category != CategoryIncomplete {
		t.Fatalf("live = %s", plan.Entries[0].Category)
	}
	if _, err := Apply(ctx, plan, Choices{}, lib, nil); err != nil {
		t.Fatal(err)
	}
	lv, _, _ := lib.GetItem("bilibili:live:42:100")
	if lv.Status != "partial" || len(lv.Live.Segments) != 2 || len(lv.Missing) != 1 || lv.Missing[0].Item != "part-003.mp4" {
		t.Fatalf("live after the first import = %s %+v missing %+v", lv.Status, lv.Live.Segments, lv.Missing)
	}
	_ = os.WriteFile(filepath.Join(liveDir, "part-003.mp4"), []byte("seg3"), 0o600)
	plan, _ = Scan(ctx, src, lib, nil)
	if plan.Entries[0].Category != CategorySupplement || plan.Entries[0].CopyFiles != 1 {
		t.Fatalf("live after the last segment arrived = %s copy %d", plan.Entries[0].Category, plan.Entries[0].CopyFiles)
	}
	if _, err := Apply(ctx, plan, Choices{}, lib, nil); err != nil {
		t.Fatal(err)
	}
	lv, _, _ = lib.GetItem("bilibili:live:42:100")
	if lv.Status != "partial" || len(library.RegisteredFiles(lv)) != 3 {
		t.Fatalf("live while still recording there = %s", lv.Status)
	}
	// The remote finished the broadcast: the last manifest marks it ended.
	ended := liveMeta()
	ended.Status, ended.Live.EndedAt = "complete", "2026-11-19T09:00:00Z"
	writeItem(t, liveDir, ended, file{path: "part-001.mp4", role: "live_segment", data: []byte("seg1")}, file{path: "part-002.mp4", role: "live_segment", data: []byte("seg2")}, file{path: "part-003.mp4", role: "live_segment", data: []byte("seg3")})
	plan, _ = Scan(ctx, src, lib, nil)
	if plan.Entries[0].Category != CategorySupplement || plan.Entries[0].CopyFiles != 0 {
		t.Fatalf("ended live = %s copy %d, want a manifest-only supplement", plan.Entries[0].Category, plan.Entries[0].CopyFiles)
	}
	if _, err := Apply(ctx, plan, Choices{}, lib, nil); err != nil {
		t.Fatal(err)
	}
	if lv, _, _ = lib.GetItem("bilibili:live:42:100"); lv.Status != "complete" || lv.Live.EndedAt == "" {
		t.Fatalf("ended live = %s ended %q", lv.Status, lv.Live.EndedAt)
	}
}

func feedHas(feed []library.FeedEntry, key string) bool {
	for _, f := range feed {
		if f.ItemKey == key {
			return true
		}
	}
	return false
}

// Cancelling in the middle leaves no half file under a final name
// and no unfinished item in the index; importing again completes; the
// source stays unless asked, and then only imported files go.
func TestApplyCancelResumeAndDeleteSource(t *testing.T) {
	lib := newLib(t)
	src := filepath.Join(t.TempDir(), "library")
	big := make([]byte, 6<<20)
	for i := range big {
		big[i] = byte(i % 251)
	}
	dir := filepath.Join(src, "bilibili", "1001_UP", "videos", "BVbig")
	writeItem(t, dir, video("BVbig", "大文件"), file{path: "P01.mp4", role: "video", data: big}, file{path: "P02.mp4", role: "video", data: []byte("small")})
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("user file"), 0o600)
	plan, err := Scan(context.Background(), src, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = Apply(ctx, plan, Choices{}, lib, func(p Progress) {
		if p.Bytes > 2<<20 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled import = %v", err)
	}
	if _, ok, _ := lib.GetItem("BVbig"); ok {
		t.Fatal("an unfinished import is in the index")
	}
	target := filepath.Join(lib.CacheDir(), "bilibili", "1001_UP", "videos", "BVbig")
	_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !storage.IsTempName(d.Name()) {
			if st, _ := d.Info(); d.Name() == "P01.mp4" && st.Size() != int64(len(big)) {
				t.Fatalf("a half file under a final name: %s", p)
			}
			if d.Name() == "item.json" {
				t.Fatal("item.json was written before the files")
			}
		}
		return nil
	})

	plan, _ = Scan(context.Background(), src, lib, nil)
	sum, err := Apply(context.Background(), plan, Choices{}, lib, nil)
	if err != nil || sum.Imported != 1 {
		t.Fatalf("second import = %+v %v", sum, err)
	}
	it, ok, _ := lib.GetItem("BVbig")
	if !ok || it.Status != "complete" {
		t.Fatalf("item after the second import = %+v", it)
	}
	if _, err := os.Stat(filepath.Join(dir, "P01.mp4")); err != nil {
		t.Fatal("the source was changed without being asked")
	}

	// Import again with 删除来源: the files are the same, so they go; the
	// user's own file and its folder stay.
	plan, _ = Scan(context.Background(), src, lib, nil)
	plan.Entries[0].Category = CategorySupplement // same → still delete what is in the library
	sum, err = Apply(context.Background(), plan, Choices{DeleteSource: true}, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"P01.mp4", "P02.mp4", "item.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s was not deleted from the source", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal("a file that was not imported was deleted")
	}
}

// An exported folder imports into another data directory as the
// same items; a live recorded partly on two installations merges into one
// with continuous segment numbers and cachedBy kept.
func TestExportReimportAndTwoNodeLiveMerge(t *testing.T) {
	ctx := context.Background()
	a := newLib(t)
	a.SetNode(library.ItemNode{Node: strings.Repeat("a", 32), Name: "台式机"})
	localItem(t, a, "bilibili/1001_UP/videos/BVexp", video("BVexp", "导出"), file{path: "P01.mp4", role: "video", data: []byte("exp-video")}, file{path: "cover.jpg", role: "cover", data: []byte("exp-cover")})
	itemDir := filepath.Join(a.CacheDir(), "bilibili", "1001_UP", "videos", "BVexp")
	_ = os.WriteFile(filepath.Join(itemDir, "P02.mp4.part"), []byte("half"), 0o600)
	items, _ := a.ListItems(library.FeedFilter{})
	out := filepath.Join(t.TempDir(), "export")
	es, err := Export(ctx, items, a, out, nil)
	if err != nil || es.Items != 1 || es.Files != 2 {
		t.Fatalf("export = %+v %v", es, err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "bilibili", "1001_UP", "videos", "BVexp", "item.json"))
	if strings.Contains(string(raw), a.CacheDir()) || strings.Contains(string(raw), "itemDir") {
		t.Fatal("the exported item.json holds a local path")
	}
	if _, err := os.Stat(filepath.Join(out, "bilibili", "1001_UP", "videos", "BVexp", "P02.mp4.part")); err == nil {
		t.Fatal("an unfinished file was exported")
	}
	b := newLib(t)
	plan, _ := Scan(ctx, out, b, nil)
	if len(plan.Entries) != 1 || plan.Entries[0].Category != CategoryNew {
		t.Fatalf("reimport plan = %+v", plan.Entries)
	}
	if _, err := Apply(ctx, plan, Choices{}, b, nil); err != nil {
		t.Fatal(err)
	}
	ga, _, _ := a.GetItem("BVexp")
	gb, _, _ := b.GetItem("BVexp")
	if gb.ItemKey != ga.ItemKey || gb.Title != ga.Title || len(library.RegisteredFiles(gb)) != 2 || gb.CachedBy == nil || gb.CachedBy.Node != ga.CachedBy.Node {
		t.Fatalf("reimported = %+v, cachedBy %+v", gb, gb.CachedBy)
	}

	// Two installations recorded the same broadcast, each from part-001.
	live := func(node string) library.ItemMetadata {
		return library.ItemMetadata{ID: "live:42:100", Site: "bilibili", Kind: "live", Title: "直播", Status: "complete", Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
			CachedBy: &library.ItemNode{Node: node}, Live: &library.LiveRecordingInfo{RoomID: 42, LiveSince: 100, StartedAt: "2026-11-19T08:00:00Z", EndedAt: "2026-11-19T09:00:00Z",
				Incidents: []library.LiveIncident{{Kind: library.LiveIncidentReconnect, At: "2026-11-19T08:30:00Z", Part: "part-001.mp4"}}}}
	}
	localLive := localItem(t, b, "bilibili/1001_UP/lives/live_42", live("node-local"), file{path: "part-001.mp4", role: "live_segment", data: []byte("local-1")}, file{path: "part-002.mp4", role: "live_segment", data: []byte("local-2")})
	remoteSrc := filepath.Join(t.TempDir(), "cloud")
	other := live("node-remote")
	other.Live.Incidents[0].At = "2026-11-19T08:50:00Z"
	writeItem(t, filepath.Join(remoteSrc, "bilibili", "1001_UP", "lives", "live_42"), other,
		file{path: "part-001.mp4", role: "live_segment", data: []byte("remote-1")}, file{path: "part-002.mp4", role: "live_segment", data: []byte("remote-2")})
	plan, _ = Scan(ctx, remoteSrc, b, nil)
	if plan.Entries[0].Category != CategoryConflict {
		t.Fatalf("two-node live = %s", plan.Entries[0].Category)
	}
	if _, err := Apply(ctx, plan, Choices{AllConflicts: library.MergeUnion}, b, nil); err != nil {
		t.Fatal(err)
	}
	merged, _, _ := b.GetItem("bilibili:live:42:100")
	var names []string
	for _, s := range merged.Live.Segments {
		names = append(names, s.File)
	}
	if strings.Join(names, ",") != "part-001.mp4,part-002.mp4,part-003.mp4,part-004.mp4" {
		t.Fatalf("merged segments = %v", names)
	}
	for n, want := range map[string]string{"part-001.mp4": "local-1", "part-002.mp4": "local-2", "part-003.mp4": "remote-1", "part-004.mp4": "remote-2"} {
		if got, _ := os.ReadFile(filepath.Join(localLive.ItemDir, n)); string(got) != want {
			t.Fatalf("%s = %q, want %q", n, got, want)
		}
	}
	if len(merged.Live.Incidents) != 2 || merged.Live.Incidents[1].Part != "part-003.mp4" {
		t.Fatalf("merged incidents = %+v", merged.Live.Incidents)
	}
	if merged.CachedBy == nil || merged.CachedBy.Node != "node-local" || merged.Status != "complete" {
		t.Fatalf("merged = cachedBy %+v status %s", merged.CachedBy, merged.Status)
	}
	// Importing the same folder again changes nothing.
	plan, _ = Scan(ctx, remoteSrc, b, nil)
	if _, err := Apply(ctx, plan, Choices{AllConflicts: library.MergeUnion}, b, nil); err != nil {
		t.Fatal(err)
	}
	again, _, _ := b.GetItem("bilibili:live:42:100")
	if len(again.Live.Segments) != 4 {
		t.Fatalf("a second merge added segments again: %d", len(again.Live.Segments))
	}
}

func TestTwoNodeLiveMergeAsymmetricCountsSequentialOrder(t *testing.T) {
	ctx := context.Background()
	lib := newLib(t)
	liveMeta := func(node string) library.ItemMetadata {
		return library.ItemMetadata{
			ID: "live:77:200", Site: "bilibili", Kind: "live", Title: "直播", Status: "complete",
			Author: library.ItemAuthor{Mid: 1001, Name: "UP"}, CachedBy: &library.ItemNode{Node: node},
			Live: &library.LiveRecordingInfo{RoomID: 77, LiveSince: 200, StartedAt: "2026-11-19T08:00:00Z", EndedAt: "2026-11-19T09:00:00Z"},
		}
	}
	// Local has 1 segment, remote has 2 segments starting from part-001.
	localLive := localItem(t, lib, "bilibili/1001_UP/lives/live_77", liveMeta("node-local"),
		file{path: "part-001.mp4", role: "live_segment", data: []byte("local-1")})
	remoteSrc := filepath.Join(t.TempDir(), "cloud")
	writeItem(t, filepath.Join(remoteSrc, "bilibili", "1001_UP", "lives", "live_77"), liveMeta("node-remote"),
		file{path: "part-001.mp4", role: "live_segment", data: []byte("remote-1")},
		file{path: "part-002.mp4", role: "live_segment", data: []byte("remote-2")})

	plan, err := Scan(ctx, remoteSrc, lib, nil)
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].Category != CategoryConflict {
		t.Fatalf("scan = %+v, err=%v", plan.Entries, err)
	}
	var lastProg Progress
	if _, err := Apply(ctx, plan, Choices{AllConflicts: library.MergeUnion}, lib, func(p Progress) { lastProg = p }); err != nil {
		t.Fatal(err)
	}
	if lastProg.Done != lastProg.Total || lastProg.Total != 1 {
		t.Fatalf("final progress = %+v, want Done==Total==1", lastProg)
	}
	merged, _, _ := lib.GetItem("bilibili:live:77:200")
	for n, want := range map[string]string{
		"part-001.mp4": "local-1",
		"part-002.mp4": "remote-1",
		"part-003.mp4": "remote-2",
	} {
		if got, err := os.ReadFile(filepath.Join(localLive.ItemDir, n)); err != nil || string(got) != want {
			t.Fatalf("%s = %q (%v), want %q", n, got, err, want)
		}
	}
	if len(merged.Live.Segments) != 3 || merged.Live.Segments[1].File != "part-002.mp4" || merged.Live.Segments[2].File != "part-003.mp4" {
		t.Fatalf("merged segments = %+v", merged.Live.Segments)
	}
}

func TestApplyPreservesMissingRefusesRecordingProtectsIncompleteConflictAndCleansImages(t *testing.T) {
	ctx := context.Background()
	lib := newLib(t)
	src := filepath.Join(t.TempDir(), "src")

	// 1. Pre-existing Missing records stay preserved (status stays partial).
	metaWithMissing := video("BVmisskeep", "含缺失记录")
	metaWithMissing.Status = "partial"
	metaWithMissing.Missing = []library.MissingRecord{{Item: "P02.mp4", Reason: "需要大会员"}}
	writeItem(t, filepath.Join(src, "bilibili", "1001_UP", "videos", "BVmisskeep"), metaWithMissing,
		file{path: "P01.mp4", role: "video", data: []byte("p1")})

	// 2. Dynamic with images/ subdir for DeleteSource cleanup.
	dynMeta := library.ItemMetadata{ID: "888", Site: "bilibili", Kind: "dynamic", Title: "图文", Status: "complete", Author: library.ItemAuthor{Mid: 1001, Name: "UP"}}
	dynSrcDir := filepath.Join(src, "bilibili", "1001_UP", "dynamics", "dyn_888")
	writeItem(t, dynSrcDir, dynMeta, file{path: "images/01.jpg", role: "image", data: []byte("img1")})

	// 3. Incomplete conflict with MergeUseImported must not delete valid local P02.mp4.
	localConf := localItem(t, lib, "bilibili/1001_UP/videos/BVincconf", video("BVincconf", "不完整冲突"),
		file{path: "P01.mp4", role: "video", data: []byte("local-p1")},
		file{path: "P02.mp4", role: "video", data: []byte("local-p2")})
	incConfDir := filepath.Join(src, "bilibili", "1001_UP", "videos", "BVincconf")
	writeItem(t, incConfDir, video("BVincconf", "不完整冲突"),
		file{path: "P01.mp4", role: "video", data: []byte("remote-p1")},
		file{path: "P02.mp4", role: "video", data: []byte("remote-p2")})
	_ = os.Remove(filepath.Join(incConfDir, "P02.mp4")) // make source incomplete

	plan, err := Scan(ctx, src, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Start recording a concurrent local item AFTER Scan to verify Apply refuses overwriting it.
	recMeta := library.ItemMetadata{
		ID: "live:99:300", Site: "bilibili", Kind: "live", Title: "录制中", Status: "recording",
		Author: library.ItemAuthor{Mid: 1001, Name: "UP"},
		Live:   &library.LiveRecordingInfo{RoomID: 99, LiveSince: 300},
	}
	recSrcDir := filepath.Join(src, "bilibili", "1001_UP", "lives", "live_99")
	writeItem(t, recSrcDir, recMeta, file{path: "part-001.mp4", role: "live_segment", data: []byte("r1")})
	plan2, err := Scan(ctx, src, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Now create the local item as actively recording before Apply runs.
	localItem(t, lib, "bilibili/1001_UP/lives/live_99", recMeta, file{path: "part-001.mp4", role: "live_segment", data: []byte("live-local")})
	_ = plan

	sum, err := Apply(ctx, plan2, Choices{AllConflicts: library.MergeUseImported, DeleteSource: true}, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Errors) != 1 || !strings.Contains(sum.Errors[0], "正在录制") {
		t.Fatalf("expected 1 recording error, got %+v", sum.Errors)
	}
	gotMiss, ok, _ := lib.GetItem("BVmisskeep")
	if !ok || gotMiss.Status != "partial" || len(gotMiss.Missing) != 1 {
		t.Fatalf("pre-existing Missing wiped: status=%q missing=%+v", gotMiss.Status, gotMiss.Missing)
	}
	if _, err := os.Stat(filepath.Join(localConf.ItemDir, "P02.mp4")); err != nil {
		t.Fatalf("valid local P02.mp4 deleted during incomplete MergeUseImported: %v", err)
	}
	if _, err := os.Stat(dynSrcDir); !os.IsNotExist(err) {
		t.Fatalf("source dynamic folder with images/ subdir was not cleaned up: %v", err)
	}
}

func TestExportRejectsLibraryOverlapAndFiltersMissingFiles(t *testing.T) {
	ctx := context.Background()
	lib := newLib(t)
	it := localItem(t, lib, "bilibili/1001_UP/videos/BVpartexp", video("BVpartexp", "部分导出"),
		file{path: "P01.mp4", role: "video", data: []byte("p1")},
		file{path: "P02.mp4", role: "video", data: []byte("p2")})

	// Exporting into CacheDir (or a subdir of it) must fail before touching anything.
	if _, err := Export(ctx, []library.ItemMetadata{it}, lib, lib.CacheDir(), nil); err == nil {
		t.Fatal("Export accepted CacheDir as destination")
	}
	if _, err := Export(ctx, []library.ItemMetadata{it}, lib, filepath.Join(lib.CacheDir(), "nested"), nil); err == nil {
		t.Fatal("Export accepted subdirectory of CacheDir as destination")
	}

	// Remove P02.mp4 locally: exported item.json must not claim P02.mp4 exists.
	_ = os.Remove(filepath.Join(it.ItemDir, "P02.mp4"))
	dest := filepath.Join(t.TempDir(), "out")
	var lastProg Progress
	sum, err := Export(ctx, []library.ItemMetadata{it}, lib, dest, func(p Progress) { lastProg = p })
	if err != nil || sum.Items != 1 || sum.Skipped != 1 {
		t.Fatalf("Export = %+v, err=%v", sum, err)
	}
	if lastProg.Done != lastProg.Total || lastProg.Total != 1 {
		t.Fatalf("final export progress = %+v, want Done==Total==1", lastProg)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "bilibili", "1001_UP", "videos", "BVpartexp", "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := library.ParseItemMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if files := library.RegisteredFiles(meta); len(files) != 1 || files[0].Path != "P01.mp4" {
		t.Fatalf("exported manifest registered files = %+v, want only P01.mp4", files)
	}
}

func TestTransferWeiboRetweetPreservesOriginAndFromOrigin(t *testing.T) {
	ctx := context.Background()
	srcLib := newLib(t)
	dstLib := newLib(t)

	const (
		fwdUID  = "1000000051"
		origUID = "1000000099"
		mid     = "5000000000006001"
	)
	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + mid,
		Site:        "weibo",
		Kind:        "dynamic",
		Status:      "complete",
		Title:       "外层转发评语",
		Text:        "外层转发评语",
		DynamicType: "forward",
		TypeLabel:   "转发",
		URL:         "https://weibo.com/" + fwdUID + "/Q6001",
		PubTs:       1763900000,
		Author:      library.ItemAuthor{Site: "weibo", UID: fwdUID, Name: "转发博主"},
		Via:         &library.ItemVia{Reason: library.ViaForward, Site: "weibo", UID: fwdUID, Name: "转发博主", DynamicID: mid},
		Origin: &library.ItemOrigin{
			Site:         "weibo",
			ID:           "5000000000009001",
			MblogID:      "Q9001",
			CanonicalURL: "https://weibo.com/" + origUID + "/Q9001",
			Author:       library.ItemAuthor{Site: "weibo", UID: origUID, Name: "原博主"},
			PubTs:        1763890000,
			Text:         "完整的原博正文与多图",
			Card:         &library.ItemRichCard{Kind: "article", Label: "头条文章", Title: "原博文章", URL: "https://weibo.com/ttarticle/p/show?id=1"},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", FromOrigin: false},
			{Index: 2, Kind: "image", Image: "images/02.jpg", FromOrigin: true},
		},
	}
	dir := filepath.Join(srcLib.CacheDir(), "weibo", fwdUID, "forwards", mid)
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		rel  string
		data []byte
		orig bool
	}{
		{"images/01.jpg", []byte("outer-img-data"), false},
		{"images/02.jpg", []byte("origin-img-data"), true},
	} {
		full := filepath.Join(dir, filepath.FromSlash(f.rel))
		if err := os.WriteFile(full, f.data, 0o600); err != nil {
			t.Fatal(err)
		}
		meta.Media = append(meta.Media, library.MediaFile{
			Role:       "image",
			Path:       f.rel,
			FromOrigin: f.orig,
			Bytes:      int64(len(f.data)),
			SHA256:     shaOf(f.data),
		})
	}
	savedSrc, err := srcLib.SaveItem(dir, meta)
	if err != nil {
		t.Fatalf("SaveItem(src): %v", err)
	}

	exportDir := filepath.Join(t.TempDir(), "export")
	es, err := Export(ctx, []library.ItemMetadata{savedSrc}, srcLib, exportDir, nil)
	if err != nil || es.Items != 1 || es.Files != 2 {
		t.Fatalf("Export = %+v err=%v", es, err)
	}
	plan, err := Scan(ctx, exportDir, dstLib, nil)
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].Category != CategoryNew {
		t.Fatalf("Scan = %+v err=%v", plan.Entries, err)
	}
	if plan.Entries[0].RelDir != "weibo/"+fwdUID+"/forwards/"+mid {
		t.Fatalf("exported RelDir = %q, want weibo/%s/forwards/%s", plan.Entries[0].RelDir, fwdUID, mid)
	}
	if _, err := Apply(ctx, plan, Choices{}, dstLib, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, ok, err := dstLib.GetItem("weibo:dynamic:" + mid)
	if err != nil || !ok {
		t.Fatalf("GetItem(dst): ok=%v err=%v", ok, err)
	}
	if got.Origin == nil || got.Origin.Author.UID != origUID || got.Origin.Text != "完整的原博正文与多图" || got.Origin.Card == nil || got.Origin.Card.Kind != "article" {
		t.Fatalf("imported Origin mismatch: %+v", got.Origin)
	}
	if len(got.NotePages) != 2 || got.NotePages[0].FromOrigin || !got.NotePages[1].FromOrigin {
		t.Fatalf("imported NotePages FromOrigin mismatch: %+v", got.NotePages)
	}
	if len(got.Media) != 2 || got.Media[0].FromOrigin || !got.Media[1].FromOrigin {
		t.Fatalf("imported Media FromOrigin mismatch: %+v", got.Media)
	}
	if got.Via == nil || got.Via.Reason != library.ViaForward || got.Via.UID != fwdUID {
		t.Fatalf("imported Via mismatch: %+v", got.Via)
	}
}
