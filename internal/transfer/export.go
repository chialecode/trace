package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/storage"
)

type hasher struct{ h hash.Hash }

func newHasher() *hasher                   { return &hasher{h: sha256.New()} }
func (h *hasher) Write(p []byte)           { _, _ = h.h.Write(p) }
func (h *hasher) sum() string              { return hex.EncodeToString(h.h.Sum(nil)) }
func (e Entry) Meta() library.ItemMetadata { return e.meta }

// ExportSummary is the result of an export.
type ExportSummary struct {
	Items   int      `json:"items"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Skipped int      `json:"skipped"` // files missing locally
	Errors  []string `json:"errors,omitempty"`
}

func pathsOverlap(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ea, err := filepath.EvalSymlinks(ca); err == nil {
		ca = filepath.Clean(ea)
	}
	if eb, err := filepath.EvalSymlinks(cb); err == nil {
		cb = filepath.Clean(eb)
	}
	within := func(base, target string) bool {
		rel, err := filepath.Rel(base, target)
		if err != nil {
			return false
		}
		if rel == "." {
			return true
		}
		return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
	}
	if runtime.GOOS == "windows" {
		ca, cb = strings.ToLower(ca), strings.ToLower(cb)
	}
	return within(ca, cb) || within(cb, ca)
}

func libraryRoots(lib Library) []string {
	if lib == nil {
		return nil
	}
	if r, ok := lib.(interface{ LibraryRoots() []string }); ok {
		return r.LibraryRoots()
	}
	if c := lib.CacheDir(); c != "" {
		return []string{c}
	}
	return nil
}

// Export copies the finished files and item.json of items into dest in the
// library layout (remote.md §6.1, §6.4). Files already in dest with the same
// content are kept, so an interrupted export continues. Unfinished files
// (*.part) are never exported: only what the manifests register.
func Export(ctx context.Context, items []library.ItemMetadata, lib Library, dest string, progress func(Progress)) (ExportSummary, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	dest, err := filepath.Abs(dest)
	if err != nil {
		return ExportSummary{}, err
	}
	if err := library.RejectDirectoryLinks(dest); err != nil {
		return ExportSummary{}, fmt.Errorf("导出目录含符号链接或联接：%w", err)
	}
	for _, root := range libraryRoots(lib) {
		if root != "" && pathsOverlap(dest, root) {
			return ExportSummary{}, errors.New("导出目录不能与媒体库保存位置重叠")
		}
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return ExportSummary{}, err
	}
	var sum ExportSummary
	total := int64(0)
	for _, it := range items {
		for _, f := range library.RegisteredFiles(it) {
			total += f.Bytes
		}
	}
	pr := &progressState{fn: func(p Progress) { p.Phase = "export"; progress(p) }, total: total, items: len(items)}
	for i, it := range items {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		pr.item = i
		rel, ok := lib.RelativeItemDir(it.ItemDir)
		if !ok {
			sum.Errors = append(sum.Errors, it.Title+"：不在媒体库的保存位置里")
			continue
		}
		outDir, err := library.EnsureWithinRoot(dest, filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			sum.Errors = append(sum.Errors, rel+"："+err.Error())
			continue
		}
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			return sum, err
		}
		regFiles := library.RegisteredFiles(it)
		exportedPaths := map[string]bool{}
		copied := 0
		for _, f := range regFiles {
			if err := ctx.Err(); err != nil {
				return sum, err
			}
			src, err := library.ResolveItemPath(it.ItemDir, f.Path)
			if err != nil {
				sum.Skipped++
				continue
			}
			st, err := os.Lstat(src)
			if err != nil || !st.Mode().IsRegular() || library.IsLinkLike(st.Mode()) || (f.Bytes > 0 && st.Size() != f.Bytes) {
				sum.Skipped++
				continue
			}
			dst, err := library.ResolveItemPath(outDir, f.Path)
			if err != nil {
				sum.Skipped++
				continue
			}
			want := strings.ToLower(f.SHA256)
			if want == "" {
				_, s, err := hashFile(ctx, src)
				if err != nil {
					sum.Skipped++
					continue
				}
				want = s
			}
			if ds, err := os.Lstat(dst); err == nil && ds.Mode().IsRegular() && !library.IsLinkLike(ds.Mode()) && ds.Size() == st.Size() {
				if _, s, err := hashFile(ctx, dst); err == nil && strings.EqualFold(s, want) {
					pr.done += st.Size()
					copied++
					exportedPaths[strings.ToLower(f.Path)] = true
					continue
				}
			}
			pr.report(rel + "/" + f.Path)
			if err := copyVerified(ctx, src, dst, st.Size(), want, pr); err != nil {
				if ctx.Err() != nil {
					return sum, ctx.Err()
				}
				sum.Errors = append(sum.Errors, rel+"/"+f.Path+"："+err.Error())
				continue
			}
			copied++
			exportedPaths[strings.ToLower(f.Path)] = true
			sum.Files++
			sum.Bytes += st.Size()
		}
		if copied == 0 {
			sum.Errors = append(sum.Errors, rel+"：没有可导出的文件")
			removeEmptyUp(outDir, 4)
			continue
		}
		exportMeta := it
		if len(exportedPaths) < len(regFiles) {
			exportMeta, _ = library.KeepListedFiles(it, func(p string) bool {
				return exportedPaths[strings.ToLower(p)]
			})
			if exportMeta.Status == "complete" {
				exportMeta.Status = "partial"
			}
		}
		raw, err := library.PortableManifest(exportMeta)
		if err != nil {
			return sum, err
		}
		if err := storage.WriteFileAtomic(filepath.Join(outDir, itemJSONName), raw, 0o600); err != nil {
			return sum, fmt.Errorf("写入 %s/item.json 失败：%w", rel, err)
		}
		sum.Items++
	}
	pr.item = len(items)
	pr.report("")
	return sum, nil
}
