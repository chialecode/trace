package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// WeiboBackfillSlotOverride supplies freshly fetched pic_id and optional /oslarge/
// URL for a specific 1-based DisplayIndex discovered via mymblog or statuses/show
// during historical Weibo cache backfill (DEC-079).
type WeiboBackfillSlotOverride struct {
	DisplayIndex     int
	PicID            string
	WatermarkFreeURL string
}

// WeiboBackfillResult summarizes the result of backfilling one historical Weibo
// item's watermark-free (/oslarge/) companion images and optional legacy links.
type WeiboBackfillResult struct {
	AddedCompanions int
	SkippedSlots    int
	LinksCleaned    bool
	StampedStandard bool
	Item            library.ItemMetadata
}

// BackfillWeiboNoWMCompanions downloads missing watermark-free (/oslarge/) companion
// images (images/NN_nowm.<ext>) for a cached Weibo post directly from sinaimg.cn
// CDN with zero Weibo JSON API requests (DEC-079). Primary original images are NEVER
// overwritten or deleted; companions whose SHA-256 equals the primary image or whose
// /oslarge/ returns 404/410/403/placeholder are cleanly skipped, and the manifest is
// stamped with library.WeiboCacheStandardVersion once every static slot is attempted.
func (m *Manager) BackfillWeiboNoWMCompanions(
	ctx context.Context,
	itemKey string,
	transport site.MediaTransportSpec,
	overrides []WeiboBackfillSlotOverride,
	cleanMetaFn func(library.ItemMetadata) (library.ItemMetadata, bool),
) (WeiboBackfillResult, error) {
	if m == nil || m.lib == nil {
		return WeiboBackfillResult{}, fmt.Errorf("download manager not initialized")
	}
	cleanKey := strings.TrimSpace(itemKey)
	if cleanKey == "" {
		return WeiboBackfillResult{}, fmt.Errorf("empty item key")
	}
	rawID := cleanKey
	if parts := strings.SplitN(cleanKey, ":", 3); len(parts) == 3 {
		rawID = parts[2]
	}
	unlock, err := m.lockItem(ctx, "weibo:"+strings.ReplaceAll(rawID, ":", "_"))
	if err != nil {
		return WeiboBackfillResult{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return WeiboBackfillResult{}, err
	}

	meta, ok, err := m.lib.GetItem(cleanKey)
	if err != nil {
		return WeiboBackfillResult{}, err
	}
	if !ok || strings.TrimSpace(meta.ItemDir) == "" {
		return WeiboBackfillResult{}, nil
	}
	_ = library.RecoverInterruptedItemSwap(meta.ItemDir, meta)
	meta, ok, err = m.lib.GetItem(cleanKey)
	if err != nil || !ok || strings.TrimSpace(meta.ItemDir) == "" {
		return WeiboBackfillResult{}, err
	}
	if !strings.EqualFold(meta.Site, string(site.Weibo)) || meta.Kind != "dynamic" || meta.IsArchivedSnapshot() {
		return WeiboBackfillResult{Item: meta}, nil
	}
	if err := library.RejectDirectoryLinks(meta.ItemDir); err != nil {
		return WeiboBackfillResult{}, err
	}

	var res WeiboBackfillResult
	if cleanMetaFn != nil {
		if cleaned, changed := cleanMetaFn(meta); changed {
			meta = cleaned
			res.LinksCleaned = true
		}
	}

	overrideByIdx := make(map[int]WeiboBackfillSlotOverride, len(overrides))
	for _, ov := range overrides {
		if ov.DisplayIndex > 0 {
			overrideByIdx[ov.DisplayIndex] = ov
		}
	}

	// Apply any discovered pic_id overrides to NotePages/AssetIDs even for slots that
	// already had companions or GIFs so future offline inspections know all pic_ids.
	assetIDsUpdated := false
	if len(meta.NotePages) > 0 && len(overrideByIdx) > 0 {
		for i := range meta.NotePages {
			idx := meta.NotePages[i].Index
			if idx <= 0 {
				idx = i + 1
			}
			if ov, has := overrideByIdx[idx]; has {
				if tok := library.SanitizeMediaToken(ov.PicID); tok != "" && meta.NotePages[i].MediaID == "" {
					meta.NotePages[i].MediaID = tok
					assetIDsUpdated = true
				}
			}
		}
		if len(meta.AssetIDs) == 0 {
			var ids []string
			for _, pg := range meta.NotePages {
				if tok := library.SanitizeMediaToken(pg.MediaID); tok != "" {
					ids = append(ids, tok)
				}
			}
			if len(ids) == len(meta.NotePages) && len(ids) > 0 {
				meta.AssetIDs = ids
				assetIDsUpdated = true
			}
		}
	}

	slots := library.WeiboStaticImageSlotsMissingNoWM(meta)
	if len(slots) == 0 {
		needSave := res.LinksCleaned || assetIDsUpdated
		if meta.CacheStandardVersion < library.WeiboCacheStandardVersion {
			meta.CacheStandardVersion = library.WeiboCacheStandardVersion
			res.StampedStandard = true
			needSave = true
		}
		if needSave {
			saved, sErr := m.lib.SaveItemMetadataOnly(meta.ItemDir, meta)
			if sErr != nil {
				return res, wrapWeiboBackfillStorageErr(sErr)
			}
			res.Item = saved
			return res, nil
		}
		res.Item = meta
		return res, nil
	}

	normTransport := normalizeWeiboMediaTransport(transport)
	spec := m.transportRangeSpec(normTransport, "image_nowm", false)

	stageDir := filepath.Join(meta.ItemDir, library.SwapStageDirPrefix+newSwapToken())
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return res, wrapWeiboBackfillStorageErr(err)
	}
	defer func() {
		_ = library.RemoveTempSubdirSafe(meta.ItemDir, stageDir)
	}()

	stageTask := Task{
		Site:    string(site.Weibo),
		Kind:    "dynamic",
		Source:  "queue",
		ItemKey: meta.ItemKey,
		ItemDir: stageDir,
	}

	type stagedBackfillCompanion struct {
		slot       library.WeiboStaticImageSlot
		media      library.MediaFile
		stagedFull string
		finalFull  string
	}
	var stagedCompanions []stagedBackfillCompanion

	allSlotsAttempted := true
	for _, slot := range slots {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		picID := slot.PicID
		var overrideURL string
		if ov, has := overrideByIdx[slot.DisplayIndex]; has {
			if tok := library.SanitizeMediaToken(ov.PicID); tok != "" {
				picID = tok
			}
			overrideURL = strings.TrimSpace(ov.WatermarkFreeURL)
		}
		cands := buildWeiboOSLargeBackfillURLs(picID, slot.PrimaryPath, overrideURL)
		if len(cands) == 0 {
			allSlotsAttempted = false
			continue
		}

		primarySHA := strings.TrimSpace(slot.PrimarySHA256)
		if primarySHA == "" && strings.TrimSpace(slot.PrimaryPath) != "" {
			if pFull, rErr := library.ResolveItemPath(meta.ItemDir, slot.PrimaryPath); rErr == nil {
				if calc, hErr := sha256FileHex(pFull); hErr == nil {
					primarySHA = calc
				}
			}
		}

		savedMedia, stagedFull, finalFull, dlErr := m.stageWeiboOSLargeBackfillSlot(ctx, &stageTask, stageDir, meta.ItemDir, spec, slot, primarySHA, cands)
		if dlErr != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if isSiteStopError(dlErr) || isStorageError(dlErr) {
				// Risk control / rate limit / auth / disk error: stop immediately WITHOUT
				// moving staged companions into itemDir or stamping v3.
				return res, wrapWeiboBackfillStorageErr(dlErr)
			}
			res.SkippedSlots++
			continue
		}
		if savedMedia == nil {
			res.SkippedSlots++
			continue
		}
		stagedCompanions = append(stagedCompanions, stagedBackfillCompanion{
			slot:       slot,
			media:      *savedMedia,
			stagedFull: stagedFull,
			finalFull:  finalFull,
		})
	}

	// Commit staged companions into itemDir ONLY after every slot finishes without
	// stop/cancel/storage errors, rolling back all newly moved files on any failure.
	var movedPaths []string
	rollbackMoved := func() {
		for _, p := range movedPaths {
			_ = os.Remove(p)
		}
	}
	for _, st := range stagedCompanions {
		if err := os.MkdirAll(filepath.Dir(st.finalFull), 0o700); err != nil {
			rollbackMoved()
			return res, wrapWeiboBackfillStorageErr(err)
		}
		if err := library.RejectDirectoryLinks(filepath.Dir(st.finalFull)); err != nil {
			rollbackMoved()
			return res, wrapWeiboBackfillStorageErr(err)
		}
		// Never overwrite an existing unregistered user file (INV-09).
		if _, stErr := os.Lstat(st.finalFull); stErr == nil {
			res.SkippedSlots++
			continue
		} else if !os.IsNotExist(stErr) {
			rollbackMoved()
			return res, wrapWeiboBackfillStorageErr(stErr)
		}
		if err := os.Rename(st.stagedFull, st.finalFull); err != nil {
			rollbackMoved()
			return res, wrapWeiboBackfillStorageErr(err)
		}
		movedPaths = append(movedPaths, st.finalFull)
		meta.Media = mergeMediaFiles(meta.Media, []library.MediaFile{st.media})
		if st.slot.PageIdx >= 0 && st.slot.PageIdx < len(meta.NotePages) {
			meta.NotePages[st.slot.PageIdx].NoWatermark = st.media.Path
		}
		res.AddedCompanions++
	}

	if allSlotsAttempted && meta.CacheStandardVersion < library.WeiboCacheStandardVersion {
		meta.CacheStandardVersion = library.WeiboCacheStandardVersion
		res.StampedStandard = true
	}

	if res.AddedCompanions > 0 || res.LinksCleaned || res.StampedStandard || assetIDsUpdated {
		saved, sErr := m.lib.SaveItem(meta.ItemDir, meta)
		if sErr != nil {
			rollbackMoved()
			return res, wrapWeiboBackfillStorageErr(sErr)
		}
		res.Item = saved
	} else {
		res.Item = meta
	}
	return res, nil
}

func wrapWeiboBackfillStorageErr(err error) error {
	if err == nil {
		return nil
	}
	if !isStorageError(err) || site.IsKind(err, site.ErrKindStorage) {
		return err
	}
	return &site.Error{
		Site:    site.Weibo,
		Kind:    site.ErrKindStorage,
		Message: "存储空间不足或写入失败",
		Err:     err,
	}
}

func (m *Manager) stageWeiboOSLargeBackfillSlot(
	ctx context.Context,
	stageTask *Task,
	stageDir, itemDir string,
	spec rangeDownloadSpec,
	slot library.WeiboStaticImageSlot,
	primarySHA string,
	candidates []string,
) (*library.MediaFile, string, string, error) {
	tempRel := slot.NoWMStem + imgSuffix
	tempFull, err := library.ResolveItemPath(stageDir, tempRel)
	if err != nil {
		return nil, "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(tempFull), 0o700); err != nil {
		return nil, "", "", err
	}
	cleanupTemp := func() {
		_ = os.Remove(tempFull)
		_ = os.Remove(tempFull + ".part")
		m.removeTempTaskProgress(stageTask, tempRel)
	}
	defer cleanupTemp()

	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, "", "", err
		}
		cleanupTemp()
		n, sha, ct, dlErr := m.downloadRange(ctx, spec, []string{cand}, tempFull, stageTask)
		if dlErr != nil {
			if ctx.Err() != nil {
				return nil, "", "", ctx.Err()
			}
			if isSiteStopError(dlErr) || isStorageError(dlErr) {
				return nil, "", "", dlErr
			}
			continue
		}
		if site.LooksLikeExpiredPlaceholderImageFile(tempFull, cand, 0, 0, false) {
			continue
		}
		ext, sErr := sniffImageExtension(tempFull, ct)
		if sErr != nil {
			continue
		}
		w, h, vErr := validateWeiboOSLargeImageFileWithSize(tempFull, ext)
		if vErr != nil {
			continue
		}
		// Deduplicate when /oslarge/ is byte-identical to the primary original image.
		if primarySHA != "" && strings.EqualFold(sha, primarySHA) {
			return nil, "", "", nil
		}

		finalRel := slot.NoWMStem + ext
		finalFull, rErr := library.ResolveItemPath(itemDir, finalRel)
		if rErr != nil {
			return nil, "", "", rErr
		}
		// Pre-check INV-09 conflict without mutating itemDir yet.
		if _, stErr := os.Lstat(finalFull); stErr == nil {
			return nil, "", "", nil
		} else if !os.IsNotExist(stErr) {
			return nil, "", "", stErr
		}

		stagedFull, srErr := library.ResolveItemPath(stageDir, finalRel)
		if srErr != nil {
			return nil, "", "", srErr
		}
		if err := os.MkdirAll(filepath.Dir(stagedFull), 0o700); err != nil {
			return nil, "", "", err
		}
		if err := os.Rename(tempFull, stagedFull); err != nil {
			return nil, "", "", err
		}
		return &library.MediaFile{
			Role:       "image_nowm",
			Path:       finalRel,
			FromOrigin: slot.FromOrigin,
			Bytes:      n,
			SHA256:     sha,
			Width:      w,
			Height:     h,
		}, stagedFull, finalFull, nil
	}
	return nil, "", "", nil
}

func buildWeiboOSLargeBackfillURLs(picID, primaryPath, overrideURL string) []string {
	var out []string
	seen := make(map[string]bool, 8)
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, raw)
	}
	altHosts := []string{"wx1.sinaimg.cn", "wx2.sinaimg.cn", "wx3.sinaimg.cn", "wx4.sinaimg.cn"}
	if overrideURL != "" {
		add(overrideURL)
		if u, err := url.Parse(overrideURL); err == nil && strings.EqualFold(u.Scheme, "https") {
			host := strings.ToLower(u.Hostname())
			if host == "wx1.sinaimg.cn" || host == "wx2.sinaimg.cn" || host == "wx3.sinaimg.cn" || host == "wx4.sinaimg.cn" {
				for _, h := range altHosts {
					if h == host {
						continue
					}
					cp := *u
					cp.Host = h
					add(cp.String())
				}
			}
		}
	}
	cleanPicID := library.SanitizeMediaToken(picID)
	if cleanPicID == "" {
		return out
	}
	ext := strings.ToLower(path.Ext(strings.TrimSpace(primaryPath)))
	exts := []string{".jpg"}
	switch ext {
	case ".png", ".webp":
		exts = []string{ext, ".jpg"}
	case ".jpeg":
		exts = []string{".jpg", ".jpeg"}
	case ".jpg", "":
		exts = []string{".jpg"}
	}
	for _, e := range exts {
		for _, h := range altHosts {
			add("https://" + h + "/oslarge/" + cleanPicID + e)
		}
	}
	return out
}

func sha256FileHex(fullPath string) (string, error) {
	f, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
