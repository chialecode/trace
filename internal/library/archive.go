package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

const (
	// SwapStageDirPrefix prefixes temporary per-item staging directories for V2 media.
	SwapStageDirPrefix = ".trace-stage-"
	// SwapPrevDirPrefix prefixes temporary per-item backup directories holding replaced V1 files during swap.
	SwapPrevDirPrefix = ".trace-prev-"
)

// notArchivedSnapshotSQL filters library_items rows that are NOT independent
// archived V1 snapshots created by "保留两版" (keep_both).
const notArchivedSnapshotSQL = "COALESCE(json_extract(json_data, '$.archivedOf'), '') = ''"

// ArchiveItemSnapshot creates an independent first-class library item snapshot of
// itemKey in sibling directory "<parentDir>/<rawID>__v1-<unix_ts>" (appending
// "-2", "-3", ... on collision) with ArchivedOf set to the original canonical
// item key and DirectCached=true.
//
// All registered files of itemKey are verified and copied via "*.part" + fsync +
// rename before item.json is committed via SaveItem. Deleting V2 later never
// touches archived V1, and deleting archived V1 never touches V2.
func (s *Store) ArchiveItemSnapshot(itemKey string, archivedAt time.Time) (ItemMetadata, error) {
	if s == nil {
		return ItemMetadata{}, fmt.Errorf("library: store is nil")
	}
	if archivedAt.IsZero() {
		archivedAt = time.Now().UTC()
	}

	origMeta, ok, err := s.GetItem(itemKey)
	if err != nil {
		return ItemMetadata{}, err
	}
	if !ok || strings.TrimSpace(origMeta.ItemDir) == "" {
		return ItemMetadata{}, fmt.Errorf("library: item %q not found", itemKey)
	}
	if origMeta.IsArchivedSnapshot() {
		return ItemMetadata{}, fmt.Errorf("library: item %q is already an archived snapshot", itemKey)
	}
	if origMeta.Status == "recording" {
		return ItemMetadata{}, fmt.Errorf("library: 正在录制的直播条目不能归档")
	}
	if origMeta.Status == "missing_files" {
		return ItemMetadata{}, fmt.Errorf("library: 本地缓存文件缺失，无法归档旧版快照")
	}

	origCanonicalKey, validKey := ValidateCanonicalItemKey(origMeta.ItemKey)
	if !validKey {
		return ItemMetadata{}, fmt.Errorf("library: invalid canonical item key %q", origMeta.ItemKey)
	}
	rawID := strings.TrimSpace(origMeta.RawID)
	if rawID == "" {
		rawID = NormalizeRawID(origMeta.Kind, origMeta.ID)
	}
	if origMeta.Site == "weibo" && origMeta.Kind == "video" {
		rawID = strings.ReplaceAll(rawID, ":", "_")
	}
	if err := ValidateSafeIDSegment(rawID); err != nil {
		return ItemMetadata{}, err
	}

	origDir := filepath.Clean(origMeta.ItemDir)
	if err := RejectDirectoryLinks(origDir); err != nil {
		return ItemMetadata{}, err
	}

	roots := s.LibraryRoots()
	var matchedRoot string
	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		if _, rErr := EnsureWithinRoot(cleanRoot, origDir); rErr == nil && !samePath(cleanRoot, origDir) {
			matchedRoot = cleanRoot
			break
		}
	}
	if matchedRoot == "" {
		return ItemMetadata{}, fmt.Errorf("library: item directory is outside registered library roots")
	}

	parentDir := filepath.Dir(origDir)
	if samePath(parentDir, matchedRoot) {
		return ItemMetadata{}, fmt.Errorf("library: invalid item parent directory")
	}
	if _, err := EnsureWithinRoot(matchedRoot, parentDir); err != nil {
		return ItemMetadata{}, err
	}
	if err := RejectDirectoryLinks(parentDir); err != nil {
		return ItemMetadata{}, err
	}

	regFiles := RegisteredFiles(origMeta)
	hasOrigJSON := false
	for _, mf := range regFiles {
		if strings.EqualFold(mf.Path, "orig.json") {
			hasOrigJSON = true
			break
		}
	}
	if !hasOrigJSON {
		if origSnap, rErr := ResolveItemPath(origDir, "orig.json"); rErr == nil {
			if info, sErr := os.Lstat(origSnap); sErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
				regFiles = append(regFiles, ManifestFile{Path: "orig.json", Role: "snapshot", Bytes: info.Size()})
			}
		}
	}

	type verifiedSource struct {
		rel     string
		srcPath string
		bytes   int64
		wantSHA string
	}
	sources := make([]verifiedSource, 0, len(regFiles))
	for _, mf := range regFiles {
		srcFull, rErr := ResolveItemPath(origDir, mf.Path)
		if rErr != nil {
			return ItemMetadata{}, rErr
		}
		info, sErr := os.Lstat(srcFull)
		if sErr != nil {
			return ItemMetadata{}, fmt.Errorf("library: 本地原文件缺失 (%s): %w", mf.Path, sErr)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return ItemMetadata{}, fmt.Errorf("library: 本地原文件无效 (%s)", mf.Path)
		}
		if mf.Bytes > 0 && info.Size() != mf.Bytes {
			return ItemMetadata{}, fmt.Errorf("library: 本地原文件大小不匹配 (%s)", mf.Path)
		}
		sources = append(sources, verifiedSource{
			rel:     mf.Path,
			srcPath: srcFull,
			bytes:   info.Size(),
			wantSHA: mf.SHA256,
		})
	}

	unixTs := archivedAt.UTC().Unix()
	if unixTs <= 0 {
		unixTs = time.Now().UTC().Unix()
	}

	var archiveDir, archiveRawID string
	for attempt := 1; attempt <= 1000; attempt++ {
		suffix := strconv.FormatInt(unixTs, 10)
		if attempt > 1 {
			suffix = fmt.Sprintf("%d-%d", unixTs, attempt)
		}
		candRawID := fmt.Sprintf("%s__v1-%s", rawID, suffix)
		if err := ValidateSafeIDSegment(candRawID); err != nil {
			return ItemMetadata{}, err
		}
		candDir, err := EnsureWithinRoot(matchedRoot, filepath.Join(parentDir, candRawID))
		if err != nil {
			return ItemMetadata{}, err
		}
		if _, statErr := os.Lstat(candDir); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return ItemMetadata{}, statErr
		}
		candKey := CanonicalItemKey(origMeta.Site, origMeta.Kind, candRawID)
		inDBMeta, inDB, dbErr := s.GetItemMetaOnly(candKey)
		if dbErr != nil {
			return ItemMetadata{}, dbErr
		}
		if inDB || inDBMeta.ItemKey != "" {
			continue
		}
		archiveDir = candDir
		archiveRawID = candRawID
		break
	}
	if archiveDir == "" || archiveRawID == "" {
		return ItemMetadata{}, fmt.Errorf("library: 无法分配可用的旧版归档目录")
	}

	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return ItemMetadata{}, err
	}
	if err := RejectDirectoryLinks(archiveDir); err != nil {
		removeEmptyDirsBottomUp(archiveDir)
		return ItemMetadata{}, err
	}

	type copiedDigest struct {
		bytes  int64
		sha256 string
	}
	digests := make(map[string]copiedDigest, len(sources))
	var copiedAbs []string
	rollback := func() {
		for _, p := range copiedAbs {
			_ = os.Remove(p)
			_ = os.Remove(p + storage.TempSuffix)
		}
		removeEmptyDirsBottomUp(archiveDir)
	}

	for _, src := range sources {
		dstFull, rErr := ResolveItemPath(archiveDir, src.rel)
		if rErr != nil {
			rollback()
			return ItemMetadata{}, rErr
		}
		if err := os.MkdirAll(filepath.Dir(dstFull), 0o700); err != nil {
			rollback()
			return ItemMetadata{}, err
		}
		if err := RejectDirectoryLinks(filepath.Dir(dstFull)); err != nil {
			rollback()
			return ItemMetadata{}, err
		}
		written, gotSHA, cErr := copyFileViaPartVerified(src.srcPath, dstFull, src.wantSHA)
		if cErr != nil {
			rollback()
			return ItemMetadata{}, cErr
		}
		copiedAbs = append(copiedAbs, dstFull)
		digests[strings.ToLower(cleanManifestRel(src.rel))] = copiedDigest{bytes: written, sha256: gotSHA}
	}

	archiveMeta := cloneArchiveMetadata(origMeta)
	archiveMeta.Version = 1
	archiveMeta.ID = origMeta.Kind + ":" + archiveRawID
	archiveMeta.RawID = archiveRawID
	archiveMeta.ItemKey = CanonicalItemKey(origMeta.Site, origMeta.Kind, archiveRawID)
	archiveMeta.ItemDir = archiveDir
	archiveMeta.ArchivedOf = origCanonicalKey
	archiveMeta.DirectCached = true
	archiveMeta.Status = "complete"
	archiveMeta.PendingParts = nil
	archiveMeta.UpgradeAvailable = false
	archiveMeta.UpgradeHint = ""
	archiveMeta.URL = sanitizeManifestPageURL(origMeta.URL, origMeta.Site, origMeta.Kind, rawID, origMeta.Live)
	if strings.TrimSpace(archiveMeta.CachedAt) == "" {
		archiveMeta.CachedAt = archivedAt.UTC().Format(time.RFC3339)
	}
	for i := range archiveMeta.Media {
		norm := strings.ToLower(cleanManifestRel(archiveMeta.Media[i].Path))
		if d, ok := digests[norm]; ok {
			archiveMeta.Media[i].Bytes = d.bytes
			if archiveMeta.Media[i].SHA256 == "" {
				archiveMeta.Media[i].SHA256 = d.sha256
			}
		}
	}

	saved, err := s.SaveItem(archiveDir, archiveMeta)
	if err != nil {
		rollback()
		return ItemMetadata{}, err
	}
	return saved, nil
}

func copyFileViaPartVerified(src, dst, wantSHA string) (int64, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()

	partPath := dst + storage.TempSuffix
	_ = os.Remove(partPath)
	out, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(partPath)
	}

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		cleanup()
		return 0, "", err
	}
	if written <= 0 {
		cleanup()
		return 0, "", fmt.Errorf("library: 归档文件为空 (%s)", filepath.Base(dst))
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return 0, "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(partPath)
		return 0, "", err
	}
	gotSHA := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(gotSHA, wantSHA) {
		_ = os.Remove(partPath)
		return 0, "", fmt.Errorf("library: 归档文件校验和不一致 (%s)", filepath.Base(dst))
	}
	if err := os.Rename(partPath, dst); err != nil {
		_ = os.Remove(partPath)
		return 0, "", err
	}
	return written, gotSHA, nil
}

func cloneArchiveMetadata(in ItemMetadata) ItemMetadata {
	out := cloneItemMetadataSlices(in)
	if in.Parts != nil {
		out.Parts = make([]VideoPartRecord, len(in.Parts))
		for i, p := range in.Parts {
			p.Subtitles = append([]string(nil), p.Subtitles...)
			p.SubtitleTracks = append([]PartSubtitleInfo(nil), p.SubtitleTracks...)
			p.Available.AcceptQuality = append([]int(nil), p.Available.AcceptQuality...)
			p.Available.AvailableGears = append([]string(nil), p.Available.AvailableGears...)
			p.Available.Formats = append([]string(nil), p.Available.Formats...)
			p.Available.Returned = append([]int(nil), p.Available.Returned...)
			out.Parts[i] = p
		}
	}
	if in.DouyinVideo != nil {
		dv := *in.DouyinVideo
		dv.AvailableGears = append([]string(nil), in.DouyinVideo.AvailableGears...)
		out.DouyinVideo = &dv
	}
	if in.NotePages != nil {
		out.NotePages = make([]NotePageRecord, len(in.NotePages))
		for i, np := range in.NotePages {
			if np.VideoInfo != nil {
				vi := *np.VideoInfo
				vi.AvailableGears = append([]string(nil), np.VideoInfo.AvailableGears...)
				np.VideoInfo = &vi
			}
			out.NotePages[i] = np
		}
	}
	if in.Music != nil {
		mu := *in.Music
		out.Music = &mu
	}
	if in.Via != nil {
		v := *in.Via
		out.Via = &v
	}
	if in.Origin != nil {
		orig := *in.Origin
		if orig.Card != nil {
			c := *orig.Card
			orig.Card = &c
		}
		out.Origin = &orig
	}
	if in.Card != nil {
		c := *in.Card
		out.Card = &c
	}
	if in.Thread != nil {
		out.Thread = make([]ItemThreadPart, len(in.Thread))
		for i, tp := range in.Thread {
			tp.MediaIndexes = append([]int(nil), tp.MediaIndexes...)
			out.Thread[i] = tp
		}
	}
	if in.CachedBy != nil {
		cb := *in.CachedBy
		out.CachedBy = &cb
	}
	return out
}

func hasInterruptedSwapDirs(itemDir string) bool {
	itemDir = filepath.Clean(strings.TrimSpace(itemDir))
	if itemDir == "" || itemDir == "." {
		return false
	}
	entries, err := os.ReadDir(itemDir)
	if err != nil {
		return false
	}
	for _, d := range entries {
		name := d.Name()
		if strings.HasPrefix(name, SwapPrevDirPrefix) || strings.HasPrefix(name, SwapStageDirPrefix) {
			return true
		}
	}
	return false
}

// RecoverInterruptedItemSwap inspects itemDir for leftover ".trace-prev-*" and
// ".trace-stage-*" subdirectories from an interrupted V2 replacement swap.
// If any file inside ".trace-prev-*" matches a registered file's SHA-256 in meta
// while the live path in itemDir is missing (or invalid), it restores that file
// into itemDir before removing ".trace-prev-*" and ".trace-stage-*".
func RecoverInterruptedItemSwap(itemDir string, meta ItemMetadata) error {
	itemDir = filepath.Clean(strings.TrimSpace(itemDir))
	if itemDir == "" || itemDir == "." {
		return nil
	}
	info, err := os.Lstat(itemDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if isLinkLike(info.Mode()) || !info.IsDir() {
		return fmt.Errorf("library: item directory is not a regular directory")
	}
	if err := RejectDirectoryLinks(itemDir); err != nil {
		return err
	}

	entries, err := os.ReadDir(itemDir)
	if err != nil {
		return err
	}
	var prevDirs, stageDirs []string
	for _, d := range entries {
		name := d.Name()
		if !strings.HasPrefix(name, SwapPrevDirPrefix) && !strings.HasPrefix(name, SwapStageDirPrefix) {
			continue
		}
		full := filepath.Join(itemDir, name)
		if isLinkLike(d.Type()) {
			return fmt.Errorf("library: swap directory %q is a symlink or junction", name)
		}
		if !d.IsDir() {
			continue
		}
		if strings.HasPrefix(name, SwapPrevDirPrefix) {
			prevDirs = append(prevDirs, full)
		} else {
			stageDirs = append(stageDirs, full)
		}
	}
	if len(prevDirs) == 0 && len(stageDirs) == 0 {
		return nil
	}
	sort.Strings(prevDirs)
	sort.Strings(stageDirs)

	expected := make(map[string]ManifestFile)
	for _, mf := range RegisteredFiles(meta) {
		expected[strings.ToLower(mf.Path)] = mf
	}
	for _, m := range meta.Media {
		clean := cleanManifestRel(m.Path)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		cur := expected[key]
		if cur.Path == "" {
			cur.Path = clean
		}
		if cur.Bytes == 0 && m.Bytes > 0 {
			cur.Bytes = m.Bytes
		}
		if cur.SHA256 == "" && m.SHA256 != "" {
			cur.SHA256 = m.SHA256
		}
		expected[key] = cur
	}

	for _, prevDir := range prevDirs {
		if err := RejectDirectoryLinks(prevDir); err != nil {
			return err
		}
		walkErr := filepath.WalkDir(prevDir, func(path string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if isLinkLike(d.Type()) {
				return fmt.Errorf("library: link detected inside swap backup directory")
			}
			if d.IsDir() {
				return nil
			}
			rel, rErr := filepath.Rel(prevDir, path)
			if rErr != nil {
				return rErr
			}
			relSlash := cleanManifestRel(filepath.ToSlash(rel))
			mf, ok := expected[strings.ToLower(relSlash)]
			if !ok && strings.HasSuffix(strings.ToLower(relSlash), storage.TempSuffix) {
				trimmed := relSlash[:len(relSlash)-len(storage.TempSuffix)]
				mf, ok = expected[strings.ToLower(cleanManifestRel(trimmed))]
			}
			if !ok || strings.TrimSpace(mf.SHA256) == "" {
				return nil
			}

			// Keep valid live files untouched; only restore when the live path is missing or invalid.
			if VerifyMediaFile(itemDir, MediaFile{Path: mf.Path, Bytes: mf.Bytes, SHA256: mf.SHA256}, true) {
				return nil
			}
			if !verifyFileSHA256(path, mf.SHA256) {
				return nil
			}

			liveFull, err := ResolveItemPath(itemDir, mf.Path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(liveFull), 0o700); err != nil {
				return err
			}
			if err := RejectDirectoryLinks(filepath.Dir(liveFull)); err != nil {
				return err
			}
			if err := os.Rename(path, liveFull); err != nil {
				return err
			}
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}

	var cleanErrs []error
	for _, dir := range prevDirs {
		if err := RemoveTempSubdirSafe(itemDir, dir); err != nil {
			cleanErrs = append(cleanErrs, err)
		}
	}
	for _, dir := range stageDirs {
		if err := RemoveTempSubdirSafe(itemDir, dir); err != nil {
			cleanErrs = append(cleanErrs, err)
		}
	}
	return errors.Join(cleanErrs...)
}

// RemoveTempSubdirSafe removes regular files and empty subdirectories inside a
// ".trace-stage-*" or ".trace-prev-*" directory strictly within itemDir without
// ever calling os.RemoveAll or traversing symlinks/junctions (INV-08/09).
func RemoveTempSubdirSafe(itemDir, subDir string) error {
	itemDir = filepath.Clean(itemDir)
	subDir = filepath.Clean(subDir)
	base := filepath.Base(subDir)
	if !strings.HasPrefix(base, SwapStageDirPrefix) && !strings.HasPrefix(base, SwapPrevDirPrefix) {
		return fmt.Errorf("library: refusing to clean non-temp directory %q", base)
	}
	if _, err := EnsureWithinRoot(itemDir, subDir); err != nil || samePath(itemDir, subDir) {
		return fmt.Errorf("library: temp directory escapes item directory")
	}
	info, err := os.Lstat(subDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if isLinkLike(info.Mode()) || !info.IsDir() {
		return fmt.Errorf("library: temp directory is not a regular directory")
	}
	if err := RejectDirectoryLinks(subDir); err != nil {
		return err
	}

	var files []string
	var dirs []string
	walkErr := filepath.WalkDir(subDir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			if os.IsNotExist(wErr) {
				return nil
			}
			return wErr
		}
		if isLinkLike(d.Type()) {
			return fmt.Errorf("library: link detected in temp directory")
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		} else {
			files = append(files, path)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	var errs []error
	for _, f := range files {
		if rErr := os.Remove(f); rErr != nil && !os.IsNotExist(rErr) {
			errs = append(errs, rErr)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, d := range dirs {
		if rErr := os.Remove(d); rErr != nil && !os.IsNotExist(rErr) {
			errs = append(errs, rErr)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) recoverInterruptedSwapsLocked() error {
	rows, err := s.db.Query(`SELECT item_dir, json_data FROM library_items WHERE item_dir != ''`)
	if err != nil {
		return err
	}
	type candidate struct {
		dir  string
		json string
	}
	var toRecover []candidate
	for rows.Next() {
		var dir, rawJSON string
		if scanErr := rows.Scan(&dir, &rawJSON); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		if hasInterruptedSwapDirs(dir) {
			toRecover = append(toRecover, candidate{dir: dir, json: rawJSON})
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, c := range toRecover {
		meta, pErr := ParseItemMetadata([]byte(c.json))
		if pErr != nil {
			continue
		}
		meta.ItemDir = filepath.Clean(c.dir)
		_ = RecoverInterruptedItemSwap(meta.ItemDir, meta)
	}
	return nil
}
