package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/storage"
)

// Choices are the user's answers to a plan.
type Choices struct {
	// Conflicts maps an item key to library.MergeKeepLocal,
	// MergeUseImported or MergeUnion; conflicts without an answer use
	// AllConflicts, and are skipped when that is empty too.
	Conflicts    map[string]string `json:"conflicts,omitempty"`
	AllConflicts string            `json:"allConflicts,omitempty"`
	// DeleteSource deletes, after an item is imported, the source files
	// that were verified and are now in the library, and the folders that
	// become empty. Off by default: the source is not touched.
	DeleteSource bool `json:"deleteSource"`
}

// Summary is the result of an import.
type Summary struct {
	Imported     int      `json:"imported"`     // new items
	Supplemented int      `json:"supplemented"` // items that got files
	Skipped      int      `json:"skipped"`      // same, unrecognized or undecided
	Conflicts    int      `json:"conflicts"`    // conflicts handled
	Incomplete   int      `json:"incomplete"`   // imported without every file
	Files        int      `json:"files"`
	Bytes        int64    `json:"bytes"`
	SourceFiles  int      `json:"sourceFilesDeleted,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

// Apply imports a plan. Each copied file is written as .part, checked
// against the listed bytes and SHA-256 and only then renamed; item.json is
// written last through the library, which indexes the item and adds it to
// the feed. Cancelling stops between files and removes the .part being
// written; running the same import again continues with what is left.
func Apply(ctx context.Context, plan Plan, choices Choices, lib Library, progress func(Progress)) (Summary, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	type job struct {
		e    Entry
		mode string
	}
	var jobs []job
	var sum Summary
	totalBytes := int64(0)
	for _, e := range plan.Entries {
		mode := ""
		switch e.Category {
		case CategorySame, CategoryUnrecognized:
			sum.Skipped++
			continue
		case CategoryConflict:
			mode = choices.Conflicts[e.ItemKey]
			if mode == "" {
				mode = choices.AllConflicts
			}
			if mode != library.MergeKeepLocal && mode != library.MergeUseImported && mode != library.MergeUnion {
				sum.Skipped++
				continue
			}
		case CategoryIncomplete:
			if e.Unverified == len(e.Files) {
				sum.Skipped++
				continue
			}
		}
		if e.meta.ItemKey == "" {
			sum.Skipped++
			continue
		}
		jobs = append(jobs, job{e: e, mode: mode})
		for _, f := range e.Files {
			if f.State == FileOK && (f.Local == LocalAbsent || (mode == library.MergeUseImported && f.Local == LocalDiffers) || (mode == library.MergeUnion && f.Local == LocalDiffers && e.Kind == "live")) {
				totalBytes += f.Bytes
			}
		}
	}
	pr := &progressState{fn: progress, total: totalBytes, items: len(jobs)}
	for i, j := range jobs {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		pr.item = i
		res, err := applyEntry(ctx, j.e, j.mode, choices.DeleteSource, lib, pr)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return sum, err
			}
			sum.Errors = append(sum.Errors, j.e.RelDir+"："+err.Error())
			continue
		}
		sum.Files += res.files
		sum.Bytes += res.bytes
		sum.SourceFiles += res.deleted
		switch {
		case j.e.Category == CategoryConflict:
			sum.Conflicts++
		case j.e.Category == CategoryIncomplete:
			sum.Incomplete++
		case j.e.LocalDir == "":
			sum.Imported++
		default:
			sum.Supplemented++
		}
	}
	pr.item = len(jobs)
	pr.report("")
	return sum, nil
}

type progressState struct {
	fn    func(Progress)
	total int64
	done  int64
	items int
	item  int
}

func (p *progressState) report(cur string) {
	p.fn(Progress{Phase: "copy", Done: p.item, Total: p.items, Bytes: p.done, TotalBytes: p.total, Current: cur})
}

type entryResult struct {
	files   int
	bytes   int64
	deleted int
}

func applyEntry(ctx context.Context, e Entry, mode string, deleteSource bool, lib Library, pr *progressState) (entryResult, error) {
	var res entryResult
	verified := map[string]bool{}
	for _, f := range e.Files {
		if f.State == FileOK {
			verified[strings.ToLower(f.Path)] = true
		}
	}
	// Only verified files are registered; the rest wait for the next import.
	imported, dropped := library.KeepListedFiles(e.meta, func(rel string) bool { return verified[strings.ToLower(rel)] })
	target := e.LocalDir
	var local library.ItemMetadata
	hasLocal := false
	if lib != nil {
		l, ok, err := lib.GetItem(imported.ItemKey)
		if err != nil {
			return res, err
		}
		if ok && l.ItemDir != "" {
			if l.Status == "recording" {
				return res, errors.New("本地正在录制这个条目，请等录制结束后再导入")
			}
			local, hasLocal, target = l, true, l.ItemDir
			if e.LocalDir == "" {
				// Item was created locally between Scan and Apply: refresh Local states
				// so we merge safely instead of overwriting local files blindly.
				localFiles := map[string]library.ManifestFile{}
				for _, rf := range library.RegisteredFiles(local) {
					localFiles[strings.ToLower(rf.Path)] = rf
				}
				filesCopy := make([]FileCheck, len(e.Files))
				copy(filesCopy, e.Files)
				for i := range filesCopy {
					if filesCopy[i].State == FileOK {
						filesCopy[i].Local = compareLocal(ctx, local.ItemDir, localFiles[strings.ToLower(filesCopy[i].Path)], filesCopy[i])
					}
				}
				e.Files = filesCopy
			}
		}
	}
	if !hasLocal {
		root := lib.CacheDir()
		dir, err := library.EnsureWithinRoot(root, filepath.Join(root, filepath.FromSlash(e.RelDir)))
		if err != nil {
			return res, err
		}
		target = dir
	}

	byPath := map[string]FileCheck{}
	for _, f := range e.Files {
		byPath[strings.ToLower(f.Path)] = f
	}
	// The manifest the item gets, and imported files stored under another
	// name (live segments renumbered after the local ones).
	var final library.ItemMetadata
	var renames []library.SegmentRename
	switch {
	case !hasLocal:
		final = imported
	case mode == library.MergeUseImported:
		final, _ = library.MergeItemMetadata(local, imported, library.MergeUseImported, nil)
	case mode == library.MergeKeepLocal:
		final, renames = library.MergeItemMetadata(local, imported, library.MergeKeepLocal, sameFn(byPath))
	default:
		final, renames = library.MergeItemMetadata(local, imported, library.MergeUnion, sameFn(byPath))
	}
	renameOf := map[string]string{}
	for _, r := range renames {
		renameOf[strings.ToLower(r.From)] = r.To
	}

	type copyJob struct {
		f  FileCheck
		to string
	}
	var copies []copyJob
	for _, f := range e.Files {
		if f.State != FileOK {
			continue
		}
		renamedTarget := renameOf[strings.ToLower(f.Path)]
		switch {
		case renamedTarget != "":
			copies = append(copies, copyJob{f, renamedTarget})
		case f.Local == LocalAbsent:
			copies = append(copies, copyJob{f, f.Path})
		case f.Local == LocalDiffers && mode == library.MergeUseImported:
			copies = append(copies, copyJob{f, f.Path})
		}
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return res, err
	}
	var copiedPaths []string
	for _, c := range copies {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		src, err := library.ResolveItemPath(e.SourceDir, c.f.Path)
		if err != nil {
			return res, err
		}
		dst, err := library.ResolveItemPath(target, c.to)
		if err != nil {
			return res, err
		}
		pr.report(e.RelDir + "/" + c.to)
		if err := copyVerified(ctx, src, dst, c.f.Bytes, c.f.SHA256, pr); err != nil {
			return res, err
		}
		res.files++
		res.bytes += c.f.Bytes
		copiedPaths = append(copiedPaths, c.f.Path)
	}

	final.Missing = nil
	registered := map[string]bool{}
	for _, f := range library.RegisteredFiles(final) {
		registered[strings.ToLower(f.Path)] = true
	}
	seenMissing := map[string]bool{}
	addMissing := func(rec library.MissingRecord) {
		k := strings.ToLower(strings.TrimSpace(rec.Item))
		if k == "" || registered[k] || seenMissing[k] {
			return
		}
		seenMissing[k] = true
		final.Missing = append(final.Missing, rec)
	}
	if hasLocal && mode != library.MergeUseImported {
		for _, rec := range local.Missing {
			addMissing(rec)
		}
	}
	for _, rec := range e.meta.Missing {
		addMissing(rec)
	}
	droppedSet := map[string]bool{}
	for _, d := range dropped {
		droppedSet[strings.ToLower(d)] = true
		addMissing(library.MissingRecord{Item: d, Reason: "导入时缺失或校验不符，下次导入补上"})
	}
	final.Status = importedStatus(final, target, e.meta.Status == "recording")
	// The local files the imported manifest no longer lists go once it is
	// written (保留导入的), except files that were only dropped because the
	// imported copy itself was incomplete.
	var stale []string
	if hasLocal && mode == library.MergeUseImported {
		keep := map[string]bool{}
		for _, f := range library.RegisteredFiles(final) {
			keep[strings.ToLower(f.Path)] = true
		}
		for _, f := range library.RegisteredFiles(local) {
			low := strings.ToLower(f.Path)
			if !keep[low] && !droppedSet[low] {
				stale = append(stale, f.Path)
			}
		}
	}
	final.ItemDir = target
	if _, err := lib.SaveItem(target, final); err != nil {
		return res, err
	}
	for _, rel := range stale {
		if p, err := library.ResolveItemPath(target, rel); err == nil {
			_ = os.Remove(p)
		}
	}
	if len(stale) > 0 {
		removeEmptySubdirs(target)
	}

	if deleteSource {
		done := map[string]bool{}
		for _, p := range copiedPaths {
			done[strings.ToLower(p)] = true
		}
		for _, f := range e.Files {
			if f.State == FileOK && f.Local == LocalSame {
				done[strings.ToLower(f.Path)] = true
			}
		}
		all := true
		for _, f := range e.Files {
			if !done[strings.ToLower(f.Path)] {
				all = false
				continue
			}
			if p, err := library.ResolveItemPath(e.SourceDir, f.Path); err == nil {
				if os.Remove(p) == nil {
					res.deleted++
				}
			}
		}
		if all {
			if os.Remove(filepath.Join(e.SourceDir, itemJSONName)) == nil {
				res.deleted++
			}
		}
		removeEmptySubdirs(e.SourceDir)
		removeEmptyUp(e.SourceDir, 4)
	}
	return res, nil
}

func sameFn(byPath map[string]FileCheck) func(string) bool {
	return func(rel string) bool {
		f, ok := byPath[strings.ToLower(rel)]
		return ok && f.Local == LocalSame
	}
}

// importedStatus is the status of an imported item: partial while a file
// it lists is not here (or the recording had not ended where it was
// made), complete otherwise.
func importedStatus(meta library.ItemMetadata, dir string, stillRecording bool) string {
	if meta.Status == "failed" && len(library.RegisteredFiles(meta)) == 0 {
		return "failed"
	}
	for _, f := range library.RegisteredFiles(meta) {
		if !library.VerifyMediaFile(dir, library.MediaFile{Path: f.Path, Bytes: f.Bytes}, false) {
			return "partial"
		}
	}
	if stillRecording || meta.Status == "recording" || len(meta.PendingParts) > 0 {
		return "partial"
	}
	if len(meta.Missing) > 0 {
		return "partial"
	}
	return "complete"
}

// copyVerified copies src to dst through dst.part and renames it only
// after the bytes and SHA-256 match.
func copyVerified(ctx context.Context, src, dst string, wantBytes int64, wantSHA string, pr *progressState) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	part := dst + storage.TempSuffix
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(part)
		}
	}()
	h := newHasher()
	buf := make([]byte, 1<<20)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, rerr := in.Read(buf)
		if r > 0 {
			if _, werr := out.Write(buf[:r]); werr != nil {
				return werr
			}
			h.Write(buf[:r])
			n += int64(r)
			if pr != nil {
				pr.done += int64(r)
				pr.report(filepath.Base(dst))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if (wantBytes > 0 && n != wantBytes) || (wantSHA != "" && !strings.EqualFold(h.sum(), wantSHA)) {
		return fmt.Errorf("%s 复制后校验不符（来源在扫描后被改动了？）", filepath.Base(src))
	}
	return os.Rename(part, dst)
}

// removeEmptySubdirs removes empty subdirectories inside dir (such as images/)
// bottom-up, never touching dir itself or symlinks/junctions.
func removeEmptySubdirs(dir string) {
	dir = filepath.Clean(dir)
	var subdirs []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if library.IsLinkLike(d.Type()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && p != dir {
			subdirs = append(subdirs, p)
		}
		return nil
	})
	sort.Slice(subdirs, func(i, j int) bool { return len(subdirs[i]) > len(subdirs[j]) })
	for _, d := range subdirs {
		if info, err := os.Lstat(d); err != nil || !info.IsDir() || library.IsLinkLike(info.Mode()) {
			continue
		}
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
}

// removeEmptyUp removes dir and up to levels-1 parents while they are
// empty.
func removeEmptyUp(dir string, levels int) {
	for i := 0; i < levels; i++ {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() || library.IsLinkLike(info.Mode()) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
