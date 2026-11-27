package library

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chialecode/trace/internal/storage"
)

// ManifestFile is one file an item.json registers, relative to the item
// folder (forward slashes), with what the manifest knows of it.
type ManifestFile struct {
	Path   string `json:"path"`
	Role   string `json:"role,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func cleanManifestRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
}

// visitManifestFiles visits every file-path field referenced by meta in a
// canonical order. Callers filter/deduplicate as needed.
func visitManifestFiles(meta ItemMetadata, fn func(ManifestFile)) {
	for _, m := range meta.Media {
		if m.Path != "" {
			fn(ManifestFile{Path: m.Path, Role: m.Role, Bytes: m.Bytes, SHA256: m.SHA256})
		}
		if m.Motion != "" {
			fn(ManifestFile{Path: m.Motion, Role: "motion"})
		}
	}
	for _, p := range meta.Parts {
		if p.File != "" {
			fn(ManifestFile{Path: p.File, Role: "video"})
		}
		if p.Danmaku != "" {
			fn(ManifestFile{Path: p.Danmaku, Role: "danmaku"})
		}
		if p.Audio.SavedAs != "" {
			fn(ManifestFile{Path: p.Audio.SavedAs, Role: "audio"})
		}
		for _, sub := range p.Subtitles {
			if sub != "" {
				fn(ManifestFile{Path: sub, Role: "subtitle"})
			}
		}
	}
	if meta.DouyinVideo != nil && meta.DouyinVideo.File != "" {
		fn(ManifestFile{Path: meta.DouyinVideo.File, Role: "video"})
	}
	for _, np := range meta.NotePages {
		if np.Image != "" {
			fn(ManifestFile{Path: np.Image, Role: "image"})
		}
		if np.NoWatermark != "" {
			fn(ManifestFile{Path: np.NoWatermark, Role: "image_nowm"})
		}
		if np.Cover != "" {
			fn(ManifestFile{Path: np.Cover, Role: "cover"})
		}
		if np.Motion != "" {
			fn(ManifestFile{Path: np.Motion, Role: "motion"})
		}
		if np.Video != "" {
			fn(ManifestFile{Path: np.Video, Role: "video"})
		}
		if np.VideoInfo != nil && np.VideoInfo.File != "" {
			fn(ManifestFile{Path: np.VideoInfo.File, Role: "video"})
		}
	}
	if meta.Music != nil {
		if meta.Music.File != "" {
			fn(ManifestFile{Path: meta.Music.File, Role: "audio"})
		}
		if meta.Music.Cover != "" {
			fn(ManifestFile{Path: meta.Music.Cover, Role: "cover"})
		}
	}
	if meta.Live != nil {
		for _, seg := range meta.Live.Segments {
			if seg.File != "" {
				fn(ManifestFile{Path: seg.File, Role: "live_segment", Bytes: seg.Bytes, SHA256: seg.SHA256})
			}
		}
	}
	if meta.Article != nil {
		if meta.Article.CoverRel != "" {
			fn(ManifestFile{Path: meta.Article.CoverRel, Role: "cover"})
		}
		for _, blk := range meta.Article.Blocks {
			if blk.MediaRel != "" {
				fn(ManifestFile{Path: blk.MediaRel, Role: "image"})
			}
		}
	}
}

// RegisteredFiles lists every finished file meta registers (media, parts,
// danmaku, subtitles, note pages, music, live segments), each once, in a
// stable order. Unfinished names (*.part, .trace-*) and paths that leave the
// item folder are never listed; item.json itself is not either.
func RegisteredFiles(meta ItemMetadata) []ManifestFile {
	seen := map[string]int{}
	var out []ManifestFile
	visitManifestFiles(meta, func(f ManifestFile) {
		rel := strings.TrimSpace(filepath.ToSlash(f.Path))
		if rel == "" || strings.EqualFold(rel, "item.json") || strings.ContainsAny(rel, "\\:\x00") {
			return
		}
		clean := cleanManifestRel(rel)
		if clean == "" || strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") || filepath.IsAbs(filepath.FromSlash(clean)) {
			return
		}
		if storage.IsTempName(filepath.Base(clean)) {
			return
		}
		key := strings.ToLower(clean)
		if i, ok := seen[key]; ok {
			if out[i].Bytes == 0 {
				out[i].Bytes = f.Bytes
			}
			if out[i].SHA256 == "" {
				out[i].SHA256 = f.SHA256
			}
			if out[i].Role == "" {
				out[i].Role = f.Role
			}
			return
		}
		seen[key] = len(out)
		f.Path = clean
		out = append(out, f)
	})
	return out
}

func isRegisteredInManifest(meta ItemMetadata, normRel string) bool {
	if normRel == "" {
		return false
	}
	found := false
	visitManifestFiles(meta, func(f ManifestFile) {
		if !found && cleanManifestRel(f.Path) == normRel {
			found = true
		}
	})
	return found
}

type fileStatSnap struct {
	size    int64
	modTime int64
	hashOK  bool
}

type hashSnapKey struct {
	path   string
	sha256 string
}

// verifySHA256Hook is an optional test-only hook invoked right before hashing a
// file on disk so concurrency and TOCTOU tests can synchronize deterministically.
var verifySHA256Hook func(full string)

func verifyItemFiles(itemDir string, meta *ItemMetadata, checkHash ...bool) (missing bool, totalBytes int64) {
	missing, totalBytes, _ = verifyItemFilesDetail(itemDir, meta, len(checkHash) == 0 || checkHash[0])
	return missing, totalBytes
}

// verifyItemFilesDetail is verifyItemFiles that also returns the manifest
// paths that are missing, empty or no longer match the recorded size/hash.
func verifyItemFilesDetail(itemDir string, meta *ItemMetadata, hash bool) (missing bool, totalBytes int64, missingPaths []string) {
	missing, totalBytes, missingPaths, _ = verifyItemFilesCore(itemDir, meta, hash, nil, nil)
	return missing, totalBytes, missingPaths
}

// verifyItemFilesWithSnaps verifies item files (including SHA-256 when hash is
// true) and records (size, modTime, hashOK) snapshots for hashed Media entries
// so a subsequent locked stat-only pass can detect concurrent file mutations.
func verifyItemFilesWithSnaps(itemDir string, meta *ItemMetadata, hash bool) (missing bool, totalBytes int64, snaps map[hashSnapKey]fileStatSnap) {
	if hash {
		snaps = make(map[hashSnapKey]fileStatSnap, len(meta.Media))
	}
	missing, totalBytes, _, _ = verifyItemFilesCore(itemDir, meta, hash, snaps, nil)
	return missing, totalBytes, snaps
}

// verifyItemFilesAgainstSnaps performs a fast stat-only verification (no
// SHA-256 disk reads) and reports drifted=true if any media file with a
// non-empty SHA256 appeared or changed (size or modTime) since expectedSnaps
// was captured.
func verifyItemFilesAgainstSnaps(itemDir string, meta *ItemMetadata, expectedSnaps map[hashSnapKey]fileStatSnap) (missing bool, totalBytes int64, drifted bool) {
	missing, totalBytes, _, drifted = verifyItemFilesCore(itemDir, meta, false, nil, expectedSnaps)
	return missing, totalBytes, drifted
}

func verifyItemFilesCore(
	itemDir string,
	meta *ItemMetadata,
	hash bool,
	recordSnaps map[hashSnapKey]fileStatSnap,
	expectedSnaps map[hashSnapKey]fileStatSnap,
) (missing bool, totalBytes int64, missingPaths []string, drifted bool) {
	checkedPaths := make(map[string]bool)
	noted := make(map[string]bool)
	note := func(rel string) {
		if rel = cleanManifestRel(strings.TrimSpace(rel)); rel != "" && !noted[rel] {
			noted[rel] = true
			missingPaths = append(missingPaths, rel)
		}
	}

	// Validate itemDir (and its recent ancestor chain) once per item instead of
	// repeating EvalSymlinks + GetLongPathNameW for every media file in the folder.
	verifiedDir, dirErr := validateItemDirRead(itemDir)

	checkRel := func(rel string) (string, os.FileInfo, bool) {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return "", nil, true
		}
		if dirErr != nil {
			note(rel)
			return "", nil, false
		}
		full, err := resolveVerifiedItemPathRead(verifiedDir, rel)
		if err != nil {
			note(rel)
			return "", nil, false
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			note(rel)
			return "", nil, false
		}
		norm := cleanManifestRel(rel)
		if !checkedPaths[norm] {
			checkedPaths[norm] = true
			totalBytes += info.Size()
		}
		return full, info, true
	}

	for i := range meta.Media {
		rel := meta.Media[i].Path
		full, info, ok := checkRel(rel)
		if !ok {
			missing = true
		} else if info != nil {
			sizeOK := true
			if meta.Media[i].Bytes > 0 && meta.Media[i].Bytes != info.Size() {
				missing = true
				sizeOK = false
				note(rel)
			} else if meta.Media[i].Bytes == 0 {
				meta.Media[i].Bytes = info.Size()
			}
			if sizeOK && meta.Media[i].SHA256 != "" {
				key := hashSnapKey{
					path:   cleanManifestRel(rel),
					sha256: strings.ToLower(strings.TrimSpace(meta.Media[i].SHA256)),
				}
				if hash {
					okHash := info.Mode().IsRegular() && verifyFileSHA256(full, meta.Media[i].SHA256)
					if recordSnaps != nil {
						recordSnaps[key] = fileStatSnap{
							size:    info.Size(),
							modTime: info.ModTime().UnixNano(),
							hashOK:  okHash,
						}
					}
					if !okHash {
						missing = true
						note(rel)
					}
				} else if expectedSnaps != nil {
					snap, hadSnap := expectedSnaps[key]
					if !hadSnap || snap.size != info.Size() || snap.modTime != info.ModTime().UnixNano() {
						drifted = true
					} else if !snap.hashOK {
						missing = true
						note(rel)
					}
				}
			}
		}
		if meta.Media[i].Motion != "" {
			if _, _, ok := checkRel(meta.Media[i].Motion); !ok {
				missing = true
			}
		}
	}

	visitManifestFiles(*meta, func(f ManifestFile) {
		norm := cleanManifestRel(f.Path)
		if norm == "" || checkedPaths[norm] || noted[norm] {
			return
		}
		if _, _, ok := checkRel(f.Path); !ok {
			missing = true
		}
	})

	hasStructuredMedia := len(meta.Media) > 0 || len(meta.Parts) > 0 ||
		(meta.DouyinVideo != nil && meta.DouyinVideo.File != "") ||
		len(meta.NotePages) > 0 ||
		(meta.Music != nil && meta.Music.File != "") ||
		(meta.Live != nil && len(meta.Live.Segments) > 0)
	if !hasStructuredMedia && meta.Kind != "dynamic" && meta.Status != "partial" && meta.Status != "recording" {
		missing = true
	}
	return missing, totalBytes, missingPaths, drifted
}

// sumItemBytesAllowMoved computes TotalBytes for remote/allow-moved-files mode
// (remote.md §6.1): files still on disk use their actual size (backfilling zero
// Bytes in meta.Media), while files already uploaded and deleted retain their
// manifest-recorded size without demoting meta.Status to missing_files.
func sumItemBytesAllowMoved(itemDir string, meta *ItemMetadata) int64 {
	seen := make(map[string]bool)
	var totalBytes int64
	verifiedDir, dirErr := validateItemDirRead(itemDir)
	addFile := func(rel string, recordedBytes int64) int64 {
		norm := cleanManifestRel(rel)
		if norm == "" || seen[strings.ToLower(norm)] {
			return recordedBytes
		}
		seen[strings.ToLower(norm)] = true
		size := recordedBytes
		if dirErr == nil {
			if full, err := resolveVerifiedItemPathRead(verifiedDir, rel); err == nil {
				if info, err := os.Lstat(full); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
					size = info.Size()
				}
			}
		}
		totalBytes += size
		return size
	}
	for i := range meta.Media {
		if meta.Media[i].Path != "" {
			sz := addFile(meta.Media[i].Path, meta.Media[i].Bytes)
			if meta.Media[i].Bytes == 0 && sz > 0 {
				meta.Media[i].Bytes = sz
			}
		}
		if meta.Media[i].Motion != "" {
			addFile(meta.Media[i].Motion, 0)
		}
	}
	visitManifestFiles(*meta, func(f ManifestFile) {
		addFile(f.Path, f.Bytes)
	})
	return totalBytes
}

func verifyFileSHA256(full, expectedSHA string) bool {
	if hook := verifySHA256Hook; hook != nil {
		hook(full)
	}
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedSHA)
}

// VerifyMediaFile checks a registered file without rewriting its expected size.
func VerifyMediaFile(itemDir string, file MediaFile, checkHash bool) bool {
	full, err := resolveItemPathRead(itemDir, file.Path)
	if err != nil {
		return false
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || file.Bytes > 0 && info.Size() != file.Bytes {
		return false
	}
	if !checkHash || file.SHA256 == "" {
		return true
	}
	return verifyFileSHA256(full, file.SHA256)
}

// RemoveItemFile deletes one file of an indexed item after it was uploaded
// (docs/design/remote.md §6.2). Only a file the item's manifest registers,
// or its item.json, inside the item folder, is removed (INV-09); the index
// entry stays. A file already gone is not an error.
func (s *Store) RemoveItemFile(itemKey, rel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok, err := s.getItemLocked(itemKey)
	if err != nil {
		return err
	}
	if !ok || meta.ItemDir == "" {
		return fmt.Errorf("library: item %s is not indexed", itemKey)
	}
	clean := cleanManifestRel(filepath.ToSlash(rel))
	if !strings.EqualFold(clean, "item.json") && !isRegisteredInManifest(meta, clean) {
		return fmt.Errorf("library: %s is not registered by %s", clean, itemKey)
	}
	if !safeRegisteredPathStrict(meta.ItemDir, clean) {
		return fmt.Errorf("library: unsafe path %s", clean)
	}
	full, err := ResolveItemPath(meta.ItemDir, clean)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveEmptyItemFolders removes the item's folder and the author and kind
// folders above it when they are empty (never a library root).
func (s *Store) RemoveEmptyItemFolders(itemKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok, err := s.getItemLocked(itemKey)
	if err != nil || !ok || meta.ItemDir == "" {
		return
	}
	removeEmptyDirsBottomUp(meta.ItemDir)
	if _, err := os.Stat(meta.ItemDir); os.IsNotExist(err) {
		removeEmptyAuthorDirs(meta.ItemDir, s.allRootsLocked())
	}
}

// AllItemKeys lists every indexed item key, sorted.
func (s *Store) AllItemKeys() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT item_key FROM library_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// RelativeItemDir is the item folder relative to the library root that holds
// it (forward slashes), e.g. "bilibili/<作者>/videos/<条目>".
func (s *Store) RelativeItemDir(itemDir string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	itemDir = filepath.Clean(itemDir)
	for _, root := range s.allRootsLocked() {
		rel, err := filepath.Rel(filepath.Clean(root), itemDir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}
