package download

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

var candidateImageExts = []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".heic"}

// SetImageCDNFallbackProviders overrides the public CDN cache / archive providers
// used when origin image URLs fail with 403/404/410 or return expired placeholders.
// Intended for hermetic 127.0.0.1 httptest unit tests.
func (m *Manager) SetImageCDNFallbackProviders(providers []site.PublicImageCDNProvider) {
	m.mu.Lock()
	m.cdnFallbackProviders = append([]site.PublicImageCDNProvider(nil), providers...)
	m.mu.Unlock()
}

func (m *Manager) imageCDNFallbackConfig(allowLocalhost bool, userAgent string) site.PublicImageCDNFallbackConfig {
	m.mu.Lock()
	providers := append([]site.PublicImageCDNProvider(nil), m.cdnFallbackProviders...)
	wrap := m.mediaWrap
	m.mu.Unlock()
	return site.PublicImageCDNFallbackConfig{
		Providers:      providers,
		AllowLocalhost: allowLocalhost,
		WrapTransport:  wrap,
		UserAgent:      userAgent,
	}
}

func trimKnownImageExt(rel string) string {
	ext := strings.ToLower(path.Ext(rel))
	for _, known := range candidateImageExts {
		if ext == known {
			return rel[:len(rel)-len(ext)]
		}
	}
	return rel
}

func fileLooksLikeDisguisedText(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return true
	}
	defer f.Close()
	var head [256]byte
	n, _ := io.ReadFull(f, head[:])
	return site.LooksLikeDisguisedImageText(head[:n])
}

func isOwnedImageCandidate(t *Task, itemDir string, previous library.ItemMetadata, exists bool, rel string, diskMedia *[]library.MediaFile, diskLoaded *bool) bool {
	if exists {
		for _, mf := range previous.Media {
			if taskFileIdentity(mf.Path) == taskFileIdentity(rel) {
				return true
			}
		}
	}
	if t != nil {
		for _, f := range t.Files {
			if taskFileIdentity(f.Path) == taskFileIdentity(rel) && taskFileOwned(f) {
				return true
			}
		}
		if t.ProgressOwner != nil {
			for _, f := range t.ProgressOwner.Files {
				if taskFileIdentity(f.Path) == taskFileIdentity(t.ProgressPrefix+rel) && strings.EqualFold(f.ItemDir, itemDir) && taskFileOwned(f) {
					return true
				}
			}
		}
	}
	// Lazily inspect on-disk item.json inside itemDir at most once per slot check.
	if !*diskLoaded {
		*diskLoaded = true
		if manifestPath, err := library.ResolveItemPath(itemDir, "item.json"); err == nil {
			if raw, rErr := os.ReadFile(manifestPath); rErr == nil {
				var diskMeta library.ItemMetadata
				if json.Unmarshal(raw, &diskMeta) == nil {
					*diskMedia = diskMeta.Media
				}
			}
		}
	}
	for _, mf := range *diskMedia {
		if taskFileIdentity(mf.Path) == taskFileIdentity(rel) {
			return true
		}
	}
	return false
}

// findExistingValidImageSlot checks both previous.Media and owned files in itemDir
// on disk for an already-saved regular image matching relStem that is NOT a known
// expired/deleted placeholder image and passes validation. Legacy placeholder GIFs
// saved by older builds are removed so they are never mistaken for valid images,
// whereas genuine valid images on disk are ALWAYS preserved and never overwritten,
// and unowned user files remain untouched so claimTaskFile enforces INV-09.
func (m *Manager) findExistingValidImageSlot(
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	role string,
	relStem string,
	hintURL string,
	width, height int,
	isGIFSlot bool,
) (*library.MediaFile, bool) {
	if t != nil && t.ForceOverwrite {
		return nil, false
	}
	stem := trimKnownImageExt(relStem)
	matchesStem := func(p string) bool {
		if p == relStem || p == stem {
			return true
		}
		return strings.HasPrefix(p, stem+".")
	}

	if exists {
		for _, mf := range previous.Media {
			if mf.Role != role || !matchesStem(mf.Path) {
				continue
			}
			full, rErr := library.ResolveItemPath(itemDir, mf.Path)
			if rErr == nil {
				checkURL := firstNonEmptyStr(hintURL, mf.Source)
				if site.LooksLikeExpiredPlaceholderImageFile(full, checkURL, width, height, isGIFSlot) {
					_ = os.Remove(full)
					continue
				}
				if _, sz, sha, ok := site.ValidateExistingImageFile(full, checkURL, width, height, isGIFSlot); ok {
					cp := mf
					cp.Bytes = sz
					cp.SHA256 = sha
					if cp.Width == 0 && width > 0 {
						cp.Width = width
					}
					if cp.Height == 0 && height > 0 {
						cp.Height = height
					}
					return &cp, true
				}
				if library.VerifyMediaFile(itemDir, mf, true) && !fileLooksLikeDisguisedText(full) {
					cp := mf
					return &cp, true
				}
			}
			// Preserve remote/export mode where completed files were already uploaded and removed.
			if m.lib != nil && m.lib.AllowMovedFiles() && mf.Bytes > 0 && mf.SHA256 != "" && rErr == nil {
				if _, statErr := os.Stat(full); os.IsNotExist(statErr) {
					cp := mf
					return &cp, true
				}
			}
		}
	} else {
		if dirInfo, err := os.Stat(itemDir); err != nil || !dirInfo.IsDir() {
			return nil, false
		}
	}

	// Inspect owned candidate image files on disk inside itemDir (e.g. from an on-disk
	// item.json or owned task entry whose SHA256 was not yet recorded) so valid images
	// on disk are never overwritten by a failed or placeholder re-fetch.
	var relCandidates []string
	if relStem != stem {
		relCandidates = append(relCandidates, relStem)
	}
	for _, ext := range candidateImageExts {
		cand := stem + ext
		if cand != relStem {
			relCandidates = append(relCandidates, cand)
		}
	}
	var diskMedia []library.MediaFile
	var diskLoaded bool
	for _, rel := range relCandidates {
		candQuick := filepath.Join(itemDir, filepath.FromSlash(rel))
		if fi, lErr := os.Lstat(candQuick); lErr != nil || !fi.Mode().IsRegular() {
			continue
		}
		if !isOwnedImageCandidate(t, itemDir, previous, exists, rel, &diskMedia, &diskLoaded) {
			continue
		}
		full, rErr := library.ResolveItemPath(itemDir, rel)
		if rErr != nil {
			continue
		}
		if site.LooksLikeExpiredPlaceholderImageFile(full, hintURL, width, height, isGIFSlot) {
			continue
		}
		if _, sz, sha, ok := site.ValidateExistingImageFile(full, hintURL, width, height, isGIFSlot); ok {
			src := ""
			if role == "cover" || strings.HasPrefix(strings.ToLower(hintURL), "https://") {
				src = sanitizeStoredURL(hintURL)
			}
			return &library.MediaFile{
				Role:   role,
				Path:   rel,
				Bytes:  sz,
				SHA256: sha,
				Source: src,
				Width:  width,
				Height: height,
			}, true
		}
	}
	return nil, false
}

// preserveValidPreviousImages retains any valid non-placeholder image/cover entries
// from previous.Media (and previous.NotePages when current notePages is empty, e.g.
// when a retweet's origin post was later deleted on the site) whose stem was not
// replaced by a newly verified file in mediaFiles.
func preserveValidPreviousImages(
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	mediaFiles []library.MediaFile,
	notePages []library.NotePageRecord,
) ([]library.MediaFile, []library.NotePageRecord) {
	if !exists || len(previous.Media) == 0 {
		return mediaFiles, notePages
	}
	coveredPaths := make(map[string]bool, len(mediaFiles))
	coveredStems := make(map[string]bool, len(mediaFiles))
	for _, mf := range mediaFiles {
		coveredPaths[mf.Path] = true
		if mf.Role == "image" || mf.Role == "cover" || mf.Role == "image_nowm" {
			coveredStems[mf.Role+":"+trimKnownImageExt(mf.Path)] = true
		}
	}

	var keptImagePaths map[string]bool
	for _, old := range previous.Media {
		if old.Role != "image" && old.Role != "cover" && old.Role != "image_nowm" {
			continue
		}
		stemKey := old.Role + ":" + trimKnownImageExt(old.Path)
		if coveredPaths[old.Path] || coveredStems[stemKey] {
			continue
		}
		full, rErr := library.ResolveItemPath(itemDir, old.Path)
		if rErr != nil {
			continue
		}
		if site.LooksLikeExpiredPlaceholderImageFile(full, old.Source, old.Width, old.Height, old.GIF) {
			continue
		}
		if _, sz, sha, ok := site.ValidateExistingImageFile(full, old.Source, old.Width, old.Height, old.GIF); ok {
			cp := old
			cp.Bytes = sz
			cp.SHA256 = sha
			mediaFiles = append(mediaFiles, cp)
			coveredPaths[cp.Path] = true
			coveredStems[stemKey] = true
			if keptImagePaths == nil {
				keptImagePaths = make(map[string]bool)
			}
			keptImagePaths[cp.Path] = true
			continue
		}
		if library.VerifyMediaFile(itemDir, old, true) && !fileLooksLikeDisguisedText(full) {
			cp := old
			mediaFiles = append(mediaFiles, cp)
			coveredPaths[cp.Path] = true
			coveredStems[stemKey] = true
			if keptImagePaths == nil {
				keptImagePaths = make(map[string]bool)
			}
			keptImagePaths[cp.Path] = true
		}
	}

	if len(notePages) == 0 && len(previous.NotePages) > 0 && len(keptImagePaths) > 0 {
		for _, pg := range previous.NotePages {
			if pg.Image != "" && coveredPaths[pg.Image] {
				notePages = append(notePages, pg)
			}
		}
	}
	return mediaFiles, notePages
}

// preserveValidPreviousImagesForStage applies preserveValidPreviousImages only when
// allow is true (skipped on explicit V1->V2 conflict replacement/archiving so V2 keeps
// its exact page set) and copies any newly retained previous images into workDir when
// staging inside a .trace-stage-* subdirectory.
func preserveValidPreviousImagesForStage(
	itemDir, workDir string,
	previous library.ItemMetadata,
	allow bool,
	mediaFiles []library.MediaFile,
	notePages []library.NotePageRecord,
) ([]library.MediaFile, []library.NotePageRecord) {
	if !allow {
		return mediaFiles, notePages
	}
	beforeLen := len(mediaFiles)
	mediaFiles, notePages = preserveValidPreviousImages(itemDir, previous, true, mediaFiles, notePages)
	if workDir != "" && workDir != itemDir && len(mediaFiles) > beforeLen {
		prevWithDir := previous
		if prevWithDir.ItemDir == "" {
			prevWithDir.ItemDir = itemDir
		}
		kept := make([]library.MediaFile, 0, len(mediaFiles))
		keptPaths := make(map[string]bool, len(mediaFiles))
		for i, mf := range mediaFiles {
			if i < beforeLen {
				kept = append(kept, mf)
				keptPaths[mf.Path] = true
				continue
			}
			if copied, err := copyLocalMediaToStageAs(prevWithDir, mf.Path, mf.Path, mf.Role, workDir); err == nil && copied != nil {
				cp := mf
				cp.Bytes = copied.Bytes
				cp.SHA256 = copied.SHA256
				kept = append(kept, cp)
				keptPaths[cp.Path] = true
			}
		}
		mediaFiles = kept
		if len(notePages) > 0 {
			filteredPages := notePages[:0]
			for _, pg := range notePages {
				if pg.Image == "" || keptPaths[pg.Image] {
					if pg.NoWatermark != "" && !keptPaths[pg.NoWatermark] {
						pg.NoWatermark = ""
					}
					filteredPages = append(filteredPages, pg)
				}
			}
			notePages = filteredPages
		}
	}
	return mediaFiles, notePages
}

// tryPublicImageCDNFallbackToFile attempts to recover an expired unsigned public
// static image from configured public CDN cache / web archive providers into tempFull,
// verifying strict image magic bytes and non-placeholder status before returning ok=true.
func (m *Manager) tryPublicImageCDNFallbackToFile(
	ctx context.Context,
	candidateURLs []string,
	width, height int,
	isGIFSlot bool,
	tempFull string,
	allowLocalhost bool,
	userAgent string,
) (size int64, shaHex string, ext string, ok bool) {
	if ctx.Err() != nil || len(candidateURLs) == 0 {
		return 0, "", "", false
	}
	cfg := m.imageCDNFallbackConfig(allowLocalhost, userAgent)
	seen := make(map[string]bool, len(candidateURLs))
	for _, raw := range candidateURLs {
		u := strings.TrimSpace(raw)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if ctx.Err() != nil {
			return 0, "", "", false
		}
		data, _, _, fetched := site.FetchPublicImageCDNFallback(ctx, cfg, u, MaxImageFileBytes, width, height, isGIFSlot)
		if !fetched || len(data) == 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(tempFull), 0o700); err != nil {
			return 0, "", "", false
		}
		if err := storage.WriteFileAtomic(tempFull, data, 0o600); err != nil {
			_ = os.Remove(tempFull)
			continue
		}
		sniffExt, sz, sha, valid := site.ValidateExistingImageFile(tempFull, u, width, height, isGIFSlot)
		if !valid {
			_ = os.Remove(tempFull)
			continue
		}
		return sz, sha, sniffExt, true
	}
	return 0, "", "", false
}

// downloadBilibiliImageStaged downloads a Bilibili cover or dynamic image via
// .part staging, validates magic bytes / Content-Type and non-placeholder status
// (with public CDN cache fallback on 403/404/410 or placeholder), and never
// overwrites an already-valid local image.
func (m *Manager) downloadBilibiliImageStaged(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	role string,
	relName string,
	rawURL string,
	width, height int,
) (*library.MediaFile, error) {
	relStem := trimKnownImageExt(relName)
	if kept, ok := m.findExistingValidImageSlot(t, itemDir, previous, exists, role, relStem, rawURL, width, height, false); ok {
		m.setFileProgress(t, role, kept.Path, "completed", kept.Bytes, kept.Bytes, 0)
		return kept, nil
	}

	finalFull := filepath.Join(itemDir, filepath.FromSlash(relName))
	_, preStatErr := os.Lstat(finalFull)
	needSeparateTemp := preStatErr == nil

	var (
		targetFull = finalFull
		tempRel    string
	)
	if needSeparateTemp {
		var err error
		finalFull, err = library.ResolveItemPath(itemDir, relName)
		if err != nil {
			return nil, err
		}
		if err := m.claimTaskFile(t, itemDir, relName, finalFull); err != nil {
			return nil, err
		}
		tempRel = relStem + imgSuffix
		targetFull, err = library.ResolveItemPath(itemDir, tempRel)
		if err != nil {
			return nil, err
		}
	}

	m.setFileProgress(t, role, relName, "downloading", 0, 0, 0)
	spec := m.bilibiliRangeSpec()
	spec.role = role
	spec.checkDisguised = true
	spec.reuseVerified = false
	spec.updateFinalRow = false

	allowLocal := m.client != nil && m.client.AllowLocalhost()
	n, sha, ct, dlErr := m.downloadRange(ctx, spec, []string{rawURL}, targetFull, t)
	if dlErr == nil {
		if site.LooksLikeExpiredPlaceholderImageFile(targetFull, rawURL, width, height, false) {
			_ = os.Remove(targetFull)
			dlErr = &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: WeiboImageUnavailableReason}
			if fbN, fbSHA, _, fbOK := m.tryPublicImageCDNFallbackToFile(ctx, []string{rawURL}, width, height, false, targetFull, allowLocal, bilibili.DefaultUserAgent); fbOK {
				n, sha, dlErr = fbN, fbSHA, nil
			}
		} else if _, sErr := sniffImageExtension(targetFull, ct); sErr != nil {
			_ = os.Remove(targetFull)
			dlErr = &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: sErr.Error()}
		}
	} else if !isStopError(dlErr) && !isStorageError(dlErr) && ctx.Err() == nil && site.ShouldAttemptImageCDNFallback(dlErr, false) {
		_ = os.Remove(targetFull)
		_ = os.Remove(targetFull + ".part")
		if fbN, fbSHA, _, fbOK := m.tryPublicImageCDNFallbackToFile(ctx, []string{rawURL}, width, height, false, targetFull, allowLocal, bilibili.DefaultUserAgent); fbOK {
			n, sha, dlErr = fbN, fbSHA, nil
		}
	}

	if dlErr != nil {
		if ctx.Err() == nil {
			_ = os.Remove(targetFull)
			if tempRel != "" && !fileOrPartExists(targetFull) {
				m.removeTempTaskProgress(t, tempRel)
			}
		}
		m.setFileProgress(t, role, relName, "failed", 0, 0, 0)
		return nil, dlErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if needSeparateTemp {
		if err := os.Rename(targetFull, finalFull); err != nil {
			if ctx.Err() == nil {
				_ = os.Remove(targetFull)
				m.removeTempTaskProgress(t, tempRel)
			}
			return nil, err
		}
		m.removeTempTaskProgress(t, tempRel)
	}
	m.setFileProgress(t, role, relName, "completed", n, n, 0)
	return &library.MediaFile{
		Role:   role,
		Path:   relName,
		Bytes:  n,
		SHA256: sha,
		Source: rawURL,
		Width:  width,
		Height: height,
	}, nil
}
