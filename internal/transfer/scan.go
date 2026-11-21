// Package transfer imports item folders into the local library and exports
// the library in the same layout (docs/design/remote.md §6.1, §6.3, §6.4):
// a read-only scan sorts every item into one of six categories, the user
// decides the conflicts, and the import copies through .part names,
// verifies bytes and SHA-256, and writes item.json last.
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/storage"
)

// Category is how one scanned item relates to the local library.
type Category string

const (
	CategoryNew          Category = "new"          // 新条目
	CategorySupplement   Category = "supplement"   // 可补充
	CategorySame         Category = "same"         // 相同
	CategoryConflict     Category = "conflict"     // 冲突
	CategoryIncomplete   Category = "incomplete"   // 未传完
	CategoryUnrecognized Category = "unrecognized" // 无法识别
)

// Categories in display order.
var Categories = []Category{CategoryNew, CategorySupplement, CategorySame, CategoryConflict, CategoryIncomplete, CategoryUnrecognized}

// File states found by the scan.
const (
	FileOK        = "ok"        // present with the listed bytes and SHA-256
	FileMissing   = "missing"   // listed but not in the folder
	FileSize      = "size"      // other size than listed
	FileHash      = "hash"      // other SHA-256 than listed
	LocalAbsent   = "absent"    // the local item does not have it
	LocalSame     = "same"      // the local item has the same content
	LocalDiffers  = "different" // the local item has other content under the name
	maxScanItems  = 100000
	itemJSONName  = "item.json"
	scanSHAChunks = 1 << 20
)

// FileCheck is one registered file of a scanned item.
type FileCheck struct {
	Path   string `json:"path"`
	Role   string `json:"role,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	State  string `json:"state"`
	Local  string `json:"local,omitempty"`
}

// Entry is one scanned item.
type Entry struct {
	Category Category `json:"category"`
	ItemKey  string   `json:"itemKey,omitempty"`
	Site     string   `json:"site,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Title    string   `json:"title,omitempty"`
	Author   string   `json:"author,omitempty"`
	CachedBy string   `json:"cachedBy,omitempty"`
	// SourceDir is the item folder in the scanned folder; RelDir its path
	// below the library root (site/author/kind/item).
	SourceDir string `json:"sourceDir"`
	RelDir    string `json:"relDir,omitempty"`
	// LocalDir is the local item's folder when the key is known locally.
	LocalDir string      `json:"localDir,omitempty"`
	Files    []FileCheck `json:"files,omitempty"`
	// Bytes to copy for the default handling.
	CopyFiles int   `json:"copyFiles"`
	CopyBytes int64 `json:"copyBytes"`
	// Conflicts lists paths with other content locally; OtherFolder is set
	// when the local item lives in another author folder.
	Conflicts   []string `json:"conflicts,omitempty"`
	OtherFolder bool     `json:"otherFolder,omitempty"`
	// Unverified counts listed files that are missing or differ.
	Unverified int    `json:"unverified,omitempty"`
	Reason     string `json:"reason,omitempty"`

	meta library.ItemMetadata
}

// Plan is the scan of one folder.
type Plan struct {
	Root      string           `json:"root"`
	ScannedAt string           `json:"scannedAt"`
	Entries   []Entry          `json:"entries"`
	Counts    map[Category]int `json:"counts"`
}

// Progress of a scan or an import.
type Progress struct {
	Phase      string `json:"phase"` // "scan", "copy", "export"
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes"`
	Current    string `json:"current,omitempty"`
}

// Library is what the scan and the import need from the local library.
type Library interface {
	GetItem(keyOrID string) (library.ItemMetadata, bool, error)
	CacheDir() string
	RelativeItemDir(itemDir string) (string, bool)
	SaveItem(itemDir string, meta library.ItemMetadata) (library.ItemMetadata, error)
}

// Scan reads root (an export folder, a cache folder, or the folder above
// it) and sorts every item it finds. It writes nothing.
func Scan(ctx context.Context, root string, lib Library, progress func(Progress)) (Plan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Plan{}, err
	}
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || library.IsLinkLike(st.Mode()) {
		return Plan{}, fmt.Errorf("不是一个文件夹：%s", root)
	}
	if err := library.RejectDirectoryLinks(root); err != nil {
		return Plan{}, fmt.Errorf("目录含符号链接或联接：%w", err)
	}
	if progress == nil {
		progress = func(Progress) {}
	}
	manifests, loose, err := findItems(ctx, root)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Root: root, ScannedAt: time.Now().UTC().Format(time.RFC3339), Counts: map[Category]int{}}
	total := len(manifests) + len(loose)
	for i, dir := range manifests {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		progress(Progress{Phase: "scan", Done: i, Total: total, Current: relOrBase(root, dir)})
		e := scanItem(ctx, root, dir, lib)
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		plan.Entries = append(plan.Entries, e)
	}
	for _, dir := range loose {
		plan.Entries = append(plan.Entries, Entry{Category: CategoryUnrecognized, SourceDir: dir, RelDir: relOrBase(root, dir), Reason: "没有 item.json"})
	}
	// Two manifests of one item in the scanned folder: only the first is used.
	seen := map[string]int{}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		if e.ItemKey == "" {
			continue
		}
		if j, ok := seen[e.ItemKey]; ok {
			*e = Entry{Category: CategoryUnrecognized, SourceDir: e.SourceDir, RelDir: e.RelDir, ItemKey: e.ItemKey, Title: e.Title,
				Reason: "与 " + plan.Entries[j].RelDir + " 是同一个条目，只导入一处"}
			continue
		}
		seen[e.ItemKey] = i
	}
	sort.SliceStable(plan.Entries, func(i, j int) bool { return plan.Entries[i].RelDir < plan.Entries[j].RelDir })
	for _, e := range plan.Entries {
		plan.Counts[e.Category]++
	}
	progress(Progress{Phase: "scan", Done: total, Total: total})
	return plan, nil
}

// findItems lists the folders holding an item.json and the folders with
// files but no item.json outside any item folder.
func findItems(ctx context.Context, root string) (manifests, loose []string, err error) {
	withFiles := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == root {
				return walkErr
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if library.IsLinkLike(d.Type()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.EqualFold(name, itemJSONName) {
			if len(manifests) >= maxScanItems {
				return fmt.Errorf("文件夹里的条目超过 %d 个，请分批导入", maxScanItems)
			}
			manifests = append(manifests, filepath.Dir(p))
			return nil
		}
		if storage.IsTempName(name) || strings.HasPrefix(name, ".") || strings.EqualFold(name, "trace-subscriptions.json") {
			return nil
		}
		withFiles[filepath.Dir(p)] = true
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	isItem := map[string]bool{}
	for _, d := range manifests {
		isItem[d] = true
	}
	for d := range withFiles {
		inside := false
		for cur := d; ; {
			if isItem[cur] {
				inside = true
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur || len(parent) < len(root) {
				break
			}
			cur = parent
		}
		if !inside && d != root {
			loose = append(loose, d)
		}
	}
	sort.Strings(manifests)
	sort.Strings(loose)
	return manifests, loose, nil
}

func relOrBase(root, dir string) string {
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(dir)
}

// libraryRelDir finds site/author/kind/item at the end of dir.
func libraryRelDir(root, dir string, meta library.ItemMetadata) (string, bool) {
	rel := relOrBase(root, dir)
	parts := strings.Split(rel, "/")
	if len(parts) < 4 {
		return rel, false
	}
	tail := parts[len(parts)-4:]
	if !strings.EqualFold(tail[0], meta.Site) {
		return rel, false
	}
	for _, p := range tail {
		if p == "" || p == "." || p == ".." {
			return rel, false
		}
	}
	return strings.Join(tail, "/"), true
}

func scanItem(ctx context.Context, root, dir string, lib Library) Entry {
	e := Entry{SourceDir: dir, RelDir: relOrBase(root, dir)}
	if err := library.RejectDirectoryLinks(dir); err != nil {
		e.Category, e.Reason = CategoryUnrecognized, "条目目录含符号链接或目录联接"
		return e
	}
	raw, err := os.ReadFile(filepath.Join(dir, itemJSONName))
	if err != nil {
		e.Category, e.Reason = CategoryUnrecognized, "item.json 读不出来"
		return e
	}
	meta, err := library.ParseItemMetadata(raw)
	if err != nil {
		e.Category, e.Reason = CategoryUnrecognized, "item.json 格式不正确"
		return e
	}
	e.ItemKey, e.Site, e.Kind, e.Title, e.Author = meta.ItemKey, meta.Site, meta.Kind, meta.Title, meta.Author.Name
	if meta.CachedBy != nil {
		e.CachedBy = firstNonEmpty(meta.CachedBy.Name, meta.CachedBy.Node)
	}
	if meta.Version > 1 {
		e.Category, e.Reason = CategoryUnrecognized, fmt.Sprintf("item.json 的格式版本 %d 比这个程序新", meta.Version)
		return e
	}
	if bad := library.UnsafeManifestPaths(meta); len(bad) > 0 {
		e.Category, e.Reason = CategoryUnrecognized, "item.json 的文件路径越出了条目文件夹："+bad[0]
		return e
	}
	relDir, ok := libraryRelDir(root, dir, meta)
	if !ok {
		e.Category, e.Reason = CategoryUnrecognized, "不在“站点/作者/种类/条目”的文件夹结构里"
		return e
	}
	e.RelDir = relDir
	e.meta = meta

	var local library.ItemMetadata
	hasLocal := false
	if lib != nil {
		if l, ok, err := lib.GetItem(meta.ItemKey); err == nil && ok && l.ItemDir != "" {
			local, hasLocal = l, true
			e.LocalDir = l.ItemDir
			if lr, ok := lib.RelativeItemDir(l.ItemDir); ok && !strings.EqualFold(lr, relDir) {
				e.OtherFolder = true
			}
		}
	}
	localFiles := map[string]library.ManifestFile{}
	if hasLocal {
		for _, f := range library.RegisteredFiles(local) {
			localFiles[strings.ToLower(f.Path)] = f
		}
	}
	verified := 0
	for _, f := range library.RegisteredFiles(meta) {
		if err := ctx.Err(); err != nil {
			return e
		}
		fc := FileCheck{Path: f.Path, Role: f.Role, Bytes: f.Bytes, SHA256: strings.ToLower(f.SHA256)}
		full, err := library.ResolveItemPath(dir, f.Path)
		size, sum, herr := int64(0), "", error(nil)
		if err == nil {
			size, sum, herr = hashFile(ctx, full)
		}
		switch {
		case err != nil || herr != nil:
			fc.State = FileMissing
		case f.Bytes > 0 && size != f.Bytes:
			fc.State = FileSize
		case f.SHA256 != "" && !strings.EqualFold(sum, f.SHA256):
			fc.State = FileHash
		default:
			fc.State = FileOK
			fc.Bytes, fc.SHA256 = size, sum
			verified++
		}
		if fc.State == FileOK {
			fc.Local = LocalAbsent
			if hasLocal {
				fc.Local = compareLocal(ctx, local.ItemDir, localFiles[strings.ToLower(f.Path)], fc)
			}
			if fc.Local == LocalDiffers {
				e.Conflicts = append(e.Conflicts, fc.Path)
			}
			if fc.Local == LocalAbsent {
				e.CopyFiles++
				e.CopyBytes += fc.Bytes
			}
		} else {
			e.Unverified++
		}
		e.Files = append(e.Files, fc)
	}
	switch {
	case verified == 0:
		e.Category = CategoryIncomplete
		e.Reason = "条目里一个能校验通过的文件都没有，跳过"
		if len(e.Files) == 0 {
			e.Category, e.Reason = CategoryUnrecognized, "item.json 没有登记任何文件"
		}
	case len(e.Conflicts) > 0 || e.OtherFolder:
		e.Category = CategoryConflict
		if e.OtherFolder {
			e.Reason = "本地的同一条目在另一个作者文件夹里"
		} else {
			e.Reason = fmt.Sprintf("%d 个同名文件内容不同", len(e.Conflicts))
		}
	case e.Unverified > 0:
		e.Category = CategoryIncomplete
		e.Reason = fmt.Sprintf("%d 个文件缺失或校验不符，先导入能校验通过的", e.Unverified)
	case !hasLocal:
		e.Category = CategoryNew
	case e.CopyFiles > 0:
		e.Category = CategorySupplement
	case local.Live != nil && meta.Live != nil && local.Live.EndedAt == "" && meta.Live.EndedAt != "" && meta.Status != "recording":
		// Only the manifest is newer: the recording ended where it was made.
		e.Category, e.Reason = CategorySupplement, "直播已录完，更新清单"
	default:
		e.Category = CategorySame
	}
	return e
}

// compareLocal compares a verified imported file with the local item's
// file of the same name (registered or left by an interrupted import).
func compareLocal(ctx context.Context, localDir string, reg library.ManifestFile, fc FileCheck) string {
	full, err := library.ResolveItemPath(localDir, fc.Path)
	if err != nil {
		return LocalAbsent
	}
	st, err := os.Lstat(full)
	if err != nil || !st.Mode().IsRegular() || library.IsLinkLike(st.Mode()) {
		return LocalAbsent
	}
	if st.Size() != fc.Bytes {
		if reg.Path == "" {
			return LocalAbsent // an unregistered leftover is replaced
		}
		return LocalDiffers
	}
	sum := strings.ToLower(reg.SHA256)
	if sum == "" || reg.Bytes != st.Size() {
		_, s, err := hashFile(ctx, full)
		if err != nil {
			return LocalAbsent
		}
		sum = s
	}
	if strings.EqualFold(sum, fc.SHA256) {
		return LocalSame
	}
	if reg.Path == "" {
		return LocalAbsent
	}
	return LocalDiffers
}

// hashFile returns the size and SHA-256 of a regular file.
func hashFile(ctx context.Context, path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return 0, "", errors.New("not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx: ctx, r: f})
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > scanSHAChunks {
		p = p[:scanSHAChunks]
	}
	return c.r.Read(p)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
