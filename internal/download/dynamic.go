package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func (m *Manager) executeDynamicTask(ctx context.Context, t *Task) (result error) {
	unlock, err := m.lockItem(ctx, "dynamic:"+t.TargetID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	storedMode, _ := m.taskResolutionAndPages(t.TaskID)
	mode := effectiveConflictResolution(ctx, t.ConflictResolution, storedMode)

	previous, exists, err := m.lib.GetItem("dynamic:" + t.TargetID)
	if err != nil {
		return err
	}
	if exists && previous.ItemDir != "" {
		_ = library.RecoverInterruptedItemSwap(previous.ItemDir, previous)
		previous, exists, err = m.lib.GetItem("dynamic:" + t.TargetID)
		if err != nil {
			return err
		}
	}
	hasProtectedLocal := exists && localHasProtectedContent(previous)

	dyn, err := m.client.DynamicDetail(ctx, t.TargetID)
	if err != nil {
		if hasProtectedLocal && isDegradedSiteError(err) {
			return wrapRemoteDegraded(err)
		}
		return err
	}

	forward := dyn.Origin != nil || strings.EqualFold(dyn.Type, "DYNAMIC_TYPE_FORWARD")
	for origin := dyn.Origin; origin != nil; origin = origin.Origin {
		dyn.Images = append(dyn.Images, origin.Images...)
		dyn.Videos = append(dyn.Videos, origin.Videos...)
	}
	// A forward's parse already copies the origin's media when the forward has
	// none of its own; keep each image and video once.
	dyn.Images = dedupeDynamicImages(dyn.Images)
	dyn.Videos = dedupeDynamicVideos(dyn.Videos)
	t.Title = dyn.Title
	t.Mid = dyn.Author.Mid
	t.AuthorName = dyn.Author.Name
	t.AuthorFace = dyn.Author.Face

	m.mu.Lock()
	multiPart := m.dlSettings.MultiPart
	m.mu.Unlock()

	cacheRoot := m.lib.CacheDir()
	// Forwards and what they carry live in the forwarder's own forwards/
	// folder, apart from their own posts (DEC-056).
	typeDir := "dynamics"
	if forward {
		typeDir = "forwards"
	}
	authorDir := filepath.Join(cacheRoot, "bilibili", strconv.FormatInt(dyn.Author.Mid, 10))
	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(authorDir, typeDir, dyn.ID))
	if err != nil {
		return err
	}
	if exists && previous.ItemDir != "" {
		itemDir = previous.ItemDir
	} else if t.ItemDir != "" {
		checked, dirErr := library.EnsureWithinRoot(cacheRoot, t.ItemDir)
		if dirErr != nil {
			return dirErr
		}
		itemDir = checked
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return err
	}
	t.ItemDir = itemDir
	t.ItemKey = library.CanonicalItemKey("bilibili", "dynamic", dyn.ID)

	var videoRefs []string
	var dynAssets []string
	for _, video := range dyn.Videos {
		if video.BVID != "" {
			videoRefs = append(videoRefs, "video:"+video.BVID)
			if tok := library.SanitizeMediaToken(video.BVID); tok != "" {
				dynAssets = append(dynAssets, tok)
			}
		}
	}
	for _, img := range dyn.Images {
		if tok := cleanBilibiliImageIdentity(img.URL); tok != "" {
			dynAssets = append(dynAssets, tok)
		}
	}

	if hasProtectedLocal {
		notice, isConflict, degErr := classifyBilibiliDynamicVsLocal(previous, dyn, videoRefs, t)
		if degErr != nil {
			return degErr
		}
		if isConflict && mode != ConflictResolveKeepBoth && mode != ConflictResolveUseRemote {
			m.emitConflict(notice)
			return ErrConflictDetected
		}
		return m.executeBilibiliDynamicStaged(ctx, t, dyn, previous, itemDir, authorDir, cacheRoot, forward, multiPart, videoRefs, dynAssets, mode, isConflict)
	}

	for idx, img := range dyn.Images {
		ext := guessImageExt(img.URL)
		relName := fmt.Sprintf("images/%02d%s", idx+1, ext)
		m.setFileProgress(t, "image", relName, "waiting", 0, 0, 0)
		if img.LiveURL != "" {
			m.setFileProgress(t, "motion", motionRelName(idx, img.LiveURL), "waiting", 0, 0, 0)
		}
	}
	m.persistTaskProgress(*t)

	var mediaFiles []library.MediaFile
	var partialErrors []string
	finalSaved := false
	defer func() {
		if result == nil || finalSaved {
			return
		}
		// An interruption must retain already completed files and their hashes.
		missing := []library.MissingRecord{{Item: "dynamic", Reason: "动态缓存尚未完成 (" + bilibili.KindOf(result) + ")"}}
		meta := library.ItemMetadata{Version: 1, ID: "dynamic:" + dyn.ID, Site: "bilibili", Kind: "dynamic", Status: "partial", Title: dyn.Title, Text: dyn.Text, DynamicType: dyn.Type, TypeLabel: dyn.TypeLabel, URL: dyn.CanonicalURL, PubTs: dyn.PubTs, CachedAt: previous.CachedAt, Author: library.ItemAuthor{Mid: dyn.Author.Mid, Name: dyn.Author.Name, Face: dyn.Author.Face}, Media: mergeMediaFiles(previous.Media, mediaFiles), Videos: videoRefs, Missing: missing}
		if _, err := m.lib.SaveItem(itemDir, meta); err != nil {
			result = err
		}
	}()

	// Save orig.json snapshot if this dynamic is a forward post (data-and-cache.md §2.1 & §2.3).
	if dyn.Origin != nil {
		origBytes, mErr := json.MarshalIndent(dyn.Origin, "", "  ")
		if mErr != nil {
			return mErr
		}
		origPath := filepath.Join(itemDir, "orig.json")
		if err := m.claimTaskFile(t, itemDir, "orig.json", origPath); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := storage.WriteFileAtomic(origPath, origBytes, 0o600); err != nil {
			return err
		}
		sum := sha256.Sum256(origBytes)
		mediaFiles = append(mediaFiles, library.MediaFile{
			Role:   "snapshot",
			Path:   "orig.json",
			Bytes:  int64(len(origBytes)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}

	for idx, img := range dyn.Images {
		ext := guessImageExt(img.URL)
		relName := fmt.Sprintf("images/%02d%s", idx+1, ext)
		// Existing manifests retain their registered names and location.
		if exists {
			for _, media := range previous.Media {
				if media.Role == "image" && media.Source == img.URL {
					relName = media.Path
					break
				}
			}
		}
		fullPath := filepath.Join(itemDir, relName)

		// If image already exists on disk from an earlier partial attempt, reuse it without re-downloading.
		if mediaIsValid(previous, relName) {
			info, statErr := os.Stat(fullPath)
			if statErr != nil {
				return statErr
			}
			m.setFileProgress(t, "image", relName, "completed", info.Size(), info.Size(), 0)
			for _, media := range previous.Media {
				if media.Path == relName {
					still := media
					motion, err := m.cacheLiveMotion(ctx, t, itemDir, previous, exists, idx, img.LiveURL, &partialErrors)
					if err != nil {
						return err
					}
					if motion != nil {
						still.Motion = motion.Path
						mediaFiles = append(mediaFiles, *motion)
					}
					mediaFiles = append(mediaFiles, still)
					break
				}
			}
			continue
		}

		m.setFileProgress(t, "image", relName, "downloading", 0, 0, 0)
		n, sha, dlErr := m.downloadFileWithRange(ctx, []string{img.URL}, fullPath, t)
		if dlErr != nil {
			m.setFileProgress(t, "image", relName, "failed", 0, 0, 0)
			if isStopError(dlErr) || isStorageError(dlErr) || ctx.Err() != nil {
				return dlErr
			}
			partialErrors = append(partialErrors, fmt.Sprintf("img_%02d: %v", idx+1, dlErr))
			continue
		}
		m.setFileProgress(t, "image", relName, "completed", n, n, 0)
		still := library.MediaFile{
			Role:   "image",
			Path:   relName,
			Bytes:  n,
			SHA256: sha,
			Source: img.URL,
			Width:  img.Width,
			Height: img.Height,
		}
		motion, err := m.cacheLiveMotion(ctx, t, itemDir, previous, exists, idx, img.LiveURL, &partialErrors)
		if err != nil {
			return err
		}
		if motion != nil {
			still.Motion = motion.Path
			mediaFiles = append(mediaFiles, *motion)
		}
		mediaFiles = append(mediaFiles, still)
	}

	for _, v := range dyn.Videos {
		if v.BVID == "" || !t.CacheLinkedVideos {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		detail, err := m.client.VideoDetail(ctx, v.BVID)
		if err != nil {
			return err
		}
		for _, page := range detail.Pages {
			if multiPart == "first" && page.Page != 1 {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			vidTask := Task{Site: "bilibili", Kind: "video", TargetID: v.BVID, CID: page.CID, Page: page.Page, Source: t.Source, LinkedVideo: true, ProgressOwner: t, ProgressPrefix: "videos/" + v.BVID + "/", Attempts: t.Attempts}
			switch {
			case detail.Author.Mid == dyn.Author.Mid:
			case forward:
				if dir, dirErr := library.EnsureWithinRoot(cacheRoot, filepath.Join(authorDir, "forwards", detail.BVID)); dirErr == nil {
					vidTask.ItemDir, vidTask.ViaName, vidTask.ViaDynamicID = dir, dyn.Author.Name, dyn.ID
				}
			case detail.HasStaffMember(dyn.Author.Mid):
				// A collaboration video posted by a member who is not the uploader.
				if dir, dirErr := library.EnsureWithinRoot(cacheRoot, filepath.Join(authorDir, "videos", detail.BVID)); dirErr == nil {
					vidTask.ItemDir = dir
				}
			}
			if err := m.executeVideoTask(ctx, &vidTask); err != nil {
				t.Attempts = vidTask.Attempts
				if isStopError(err) || isStorageError(err) || ctx.Err() != nil {
					return err
				}
				partialErrors = append(partialErrors, fmt.Sprintf("video %s P%d: %v", v.BVID, page.Page, err))
			} else {
				t.Attempts = vidTask.Attempts
			}
			if vidTask.Status == "partial" {
				partialErrors = append(partialErrors, fmt.Sprintf("video %s P%d: 部分缓存", v.BVID, page.Page))
			}
		}
	}

	status := "complete"
	var missing []library.MissingRecord
	if dyn.Degraded {
		status = "partial"
		t.Status = "partial"
		missing = append(missing, library.MissingRecord{Item: "dynamic", Reason: fmt.Sprintf("类型不支持 (%s)", dyn.TypeLabel)})
		t.ErrorMessage = summarizeMissingReasons(missing)
	}
	if len(partialErrors) > 0 {
		status = "partial"
		t.Status = "partial"
		for _, pe := range partialErrors {
			missing = append(missing, library.MissingRecord{Item: "media", Reason: pe})
		}
	}

	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + dyn.ID,
		Site:        "bilibili",
		Kind:        "dynamic",
		Status:      status,
		Title:       dyn.Title,
		Text:        dyn.Text,
		DynamicType: dyn.Type,
		TypeLabel:   dyn.TypeLabel,
		URL:         dyn.CanonicalURL,
		PubTs:       dyn.PubTs,
		CachedAt:    previous.CachedAt,
		Author: library.ItemAuthor{
			Mid:  dyn.Author.Mid,
			Name: dyn.Author.Name,
			Face: dyn.Author.Face,
		},
		Media:   mediaFiles,
		Videos:  videoRefs,
		Missing: missing,
	}
	stampCacheStandardAndAssets(&meta, 0, 0, dynAssets)

	savedMeta, saveErr := m.lib.SaveItem(itemDir, meta)
	if saveErr != nil {
		return saveErr
	}
	t.ItemKey = savedMeta.ItemKey
	finalSaved = true

	if len(partialErrors) > 0 {
		return fmt.Errorf("部分媒体下载失败 (%s)", strings.Join(partialErrors, "; "))
	}
	return nil
}

// motionRelName is where the motion clip of a dynamic's idx-th live photo
// (实况) is kept, next to its still.
func motionRelName(idx int, rawURL string) string {
	ext := ".mp4"
	if u, err := url.Parse(rawURL); err == nil {
		switch e := strings.ToLower(path.Ext(u.Path)); e {
		case ".mp4", ".mov", ".m4v":
			ext = e
		}
	}
	return fmt.Sprintf("images/%02d_live%s", idx+1, ext)
}

// cacheLiveMotion keeps the motion clip of a live photo with its still. Clips
// that require MP4 validation/moov relocation are downloaded to a *.stage.part
// staging path and atomically renamed to their final name only after validation
// passes (remote.md §6.1).
func (m *Manager) cacheLiveMotion(ctx context.Context, t *Task, itemDir string, previous library.ItemMetadata, exists bool, idx int, liveURL string, partialErrors *[]string) (*library.MediaFile, error) {
	if liveURL == "" {
		return nil, nil
	}
	relName := motionRelName(idx, liveURL)
	if exists {
		for _, media := range previous.Media {
			if media.Role == "motion" && media.Source == liveURL {
				relName = media.Path
				break
			}
		}
	}
	if mediaIsValid(previous, relName) {
		for _, media := range previous.Media {
			if media.Path == relName {
				m.setFileProgress(t, "motion", relName, "completed", media.Bytes, media.Bytes, 0)
				file := media
				return &file, nil
			}
		}
	}
	fullPath, err := library.ResolveItemPath(itemDir, relName)
	if err != nil {
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, relName, fullPath); err != nil {
		return nil, err
	}

	needsMP4Check := strings.EqualFold(path.Ext(relName), ".mp4") || strings.EqualFold(path.Ext(relName), ".m4v")
	if !needsMP4Check {
		m.setFileProgress(t, "motion", relName, "downloading", 0, 0, 0)
		n, sha, dlErr := m.downloadFileWithRange(ctx, []string{liveURL}, fullPath, t)
		if dlErr != nil {
			m.setFileProgress(t, "motion", relName, "failed", 0, 0, 0)
			if isStopError(dlErr) || isStorageError(dlErr) || ctx.Err() != nil {
				return nil, dlErr
			}
			*partialErrors = append(*partialErrors, fmt.Sprintf("img_%02d 实况: %v", idx+1, dlErr))
			return nil, nil
		}
		m.setFileProgress(t, "motion", relName, "completed", n, n, 0)
		return &library.MediaFile{Role: "motion", Path: relName, Bytes: n, SHA256: sha, Source: liveURL}, nil
	}

	stageRel := relName + stageSuffix
	stagePath := fullPath + stageSuffix
	m.setFileProgress(t, "motion", relName, "downloading", 0, 0, 0)
	_, _, err = m.downloadFileWithRange(ctx, []string{liveURL}, stagePath, t)
	if err != nil {
		if ctx.Err() == nil && !fileOrPartExists(stagePath) {
			m.removeTempTaskProgress(t, stageRel)
		}
		m.setFileProgress(t, "motion", relName, "failed", 0, 0, 0)
		if isStopError(err) || isStorageError(err) || ctx.Err() != nil {
			return nil, err
		}
		*partialErrors = append(*partialErrors, fmt.Sprintf("img_%02d 实况: %v", idx+1, err))
		return nil, nil
	}

	// Keep the clip a plain progressive MP4 with moov ahead of mdat, so system
	// players preview it too (DEC-046 ①); a fragmented or broken clip is
	// removed while still under the temporary staging name.
	m.setFileProgress(t, "motion", relName, "verifying", 0, 0, 0)
	summary, vErr := EnsureSingleMP4Context(ctx, stagePath, 0)
	if vErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_ = os.Remove(stagePath)
		m.removeTempTaskProgress(t, stageRel)
		m.setFileProgress(t, "motion", relName, "failed", 0, 0, 0)
		*partialErrors = append(*partialErrors, fmt.Sprintf("img_%02d 实况: %v", idx+1, vErr))
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stagePath, fullPath); err != nil {
		return nil, err
	}
	m.removeTempTaskProgress(t, stageRel)
	m.setFileProgress(t, "motion", relName, "completed", summary.FileBytes, summary.FileBytes, 0)
	return &library.MediaFile{
		Role:     "motion",
		Path:     relName,
		Bytes:    summary.FileBytes,
		SHA256:   summary.SHA256,
		Source:   liveURL,
		Width:    summary.Width,
		Height:   summary.Height,
		Duration: int(math.Round(summary.DurationSec)),
	}, nil
}

func guessImageExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Path != "" {
		ext := strings.ToLower(path.Ext(u.Path))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif":
			return ext
		}
	}
	return ".jpg"
}

func dedupeDynamicImages(in []bilibili.DynamicMedia) []bilibili.DynamicMedia {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, img := range in {
		if img.URL != "" && seen[img.URL] {
			continue
		}
		seen[img.URL] = true
		out = append(out, img)
	}
	return out
}

func dedupeDynamicVideos(in []bilibili.DynamicVideoRef) []bilibili.DynamicVideoRef {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, v := range in {
		if v.BVID != "" && seen[v.BVID] {
			continue
		}
		seen[v.BVID] = true
		out = append(out, v)
	}
	return out
}

func (m *Manager) executeBilibiliDynamicStaged(
	ctx context.Context,
	t *Task,
	dyn bilibili.DynamicItem,
	previous library.ItemMetadata,
	itemDir, authorDir, cacheRoot string,
	forward bool,
	multiPart string,
	videoRefs []string,
	dynAssets []string,
	mode string,
	isConflict bool,
) error {
	stageDir := filepath.Join(itemDir, library.SwapStageDirPrefix+newSwapToken())
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return err
	}
	defer func() {
		_ = library.RemoveTempSubdirSafe(itemDir, stageDir)
	}()

	var mediaFiles []library.MediaFile
	var partialErrors []string

	// Monotonic orig.json protection (Invariant #4): never lose valid local orig.json!
	if dyn.Origin != nil {
		origBytes, err := json.MarshalIndent(dyn.Origin, "", "  ")
		if err != nil {
			return err
		}
		n, sha, err := writeBytesToStage(stageDir, "orig.json", origBytes)
		if err != nil {
			return err
		}
		mediaFiles = append(mediaFiles, library.MediaFile{Role: "snapshot", Path: "orig.json", Bytes: n, SHA256: sha})
	} else if !isConflict && mediaIsValid(previous, "orig.json") {
		if mf, err := copyLocalMediaToStageAs(previous, "orig.json", "orig.json", "snapshot", stageDir); err == nil {
			mediaFiles = append(mediaFiles, *mf)
		}
	}

	localBySource := make(map[string]library.MediaFile, len(previous.Media))
	for _, mf := range previous.Media {
		if mf.Role == "image" && mf.Source != "" && mediaIsValid(previous, mf.Path) {
			localBySource[bilibiliSourceKey(mf.Source)] = mf
		}
	}

	stageSub := cloneStageSubtask(t, stageDir)
	defer func() {
		t.Attempts = stageSub.Attempts
	}()

	for idx, img := range dyn.Images {
		ext := guessImageExt(img.URL)
		relName := fmt.Sprintf("images/%02d%s", idx+1, ext)
		cleanSrc := bilibiliSourceKey(img.URL)
		localImg, hasSameSource := localBySource[cleanSrc]

		var still library.MediaFile
		reusedStill := false
		if !isConflict && hasSameSource {
			if copied, cErr := copyLocalMediaToStageAs(previous, localImg.Path, relName, "image", stageDir); cErr == nil {
				still = *copied
				reusedStill = true
			}
		}
		if !reusedStill {
			stagedImg, sErr := stagedFullPath(stageDir, relName)
			if sErr != nil {
				return sErr
			}
			n, sha, dlErr := m.downloadFileWithRange(ctx, []string{img.URL}, stagedImg, &stageSub)
			if dlErr != nil {
				// Invariant #3: reuse verified local image on mixed V2 edits when CDN expired!
				if hasSameSource {
					if copied, cErr := copyLocalMediaToStageAs(previous, localImg.Path, relName, "image", stageDir); cErr == nil {
						still = *copied
						reusedStill = true
					}
				}
				if !reusedStill {
					if isStopError(dlErr) || isStorageError(dlErr) || ctx.Err() != nil {
						return dlErr
					}
					if isDegradedSiteError(dlErr) && isConflict {
						return wrapRemoteDegraded(dlErr)
					}
					partialErrors = append(partialErrors, fmt.Sprintf("img_%02d: %v", idx+1, dlErr))
					continue
				}
			} else {
				still = library.MediaFile{
					Role:   "image",
					Path:   relName,
					Bytes:  n,
					SHA256: sha,
					Source: img.URL,
					Width:  img.Width,
					Height: img.Height,
				}
			}
		}
		m.setFileProgress(t, "image", relName, "completed", still.Bytes, still.Bytes, 0)

		// LivePhoto motion clip
		if img.LiveURL != "" {
			mRel := motionRelName(idx, img.LiveURL)
			var motionMF *library.MediaFile
			if hasSameSource && localImg.Motion != "" && mediaIsValid(previous, localImg.Motion) {
				if copiedM, mErr := copyLocalMediaToStageAs(previous, localImg.Motion, mRel, "motion", stageDir); mErr == nil {
					motionMF = copiedM
				}
			}
			if motionMF == nil {
				stagedMotion, mErr := stagedFullPath(stageDir, mRel)
				if mErr != nil {
					return mErr
				}
				rawStage := stagedMotion + stageSuffix
				_, _, dlErr := m.downloadFileWithRange(ctx, []string{img.LiveURL}, rawStage, &stageSub)
				if dlErr == nil {
					if summary, vErr := EnsureSingleMP4Context(ctx, rawStage, 0); vErr == nil {
						if rErr := os.Rename(rawStage, stagedMotion); rErr == nil {
							motionMF = &library.MediaFile{
								Role:     "motion",
								Path:     mRel,
								Bytes:    summary.FileBytes,
								SHA256:   summary.SHA256,
								Source:   img.LiveURL,
								Width:    summary.Width,
								Height:   summary.Height,
								Duration: int(math.Round(summary.DurationSec)),
							}
						}
					} else {
						_ = os.Remove(rawStage)
					}
				} else if isStopError(dlErr) || isStorageError(dlErr) || ctx.Err() != nil {
					return dlErr
				}
			}
			if motionMF != nil {
				still.Motion = motionMF.Path
				mediaFiles = append(mediaFiles, *motionMF)
			} else {
				partialErrors = append(partialErrors, fmt.Sprintf("img_%02d 实况下载失败", idx+1))
			}
		} else if !isConflict && hasSameSource && localImg.Motion != "" && mediaIsValid(previous, localImg.Motion) {
			mRel := fmt.Sprintf("images/%02d_live%s", idx+1, filepath.Ext(localImg.Motion))
			if copiedM, mErr := copyLocalMediaToStageAs(previous, localImg.Motion, mRel, "motion", stageDir); mErr == nil {
				still.Motion = copiedM.Path
				mediaFiles = append(mediaFiles, *copiedM)
			}
		}
		mediaFiles = append(mediaFiles, still)
	}

	for _, v := range dyn.Videos {
		if v.BVID == "" || !t.CacheLinkedVideos {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		detail, err := m.client.VideoDetail(ctx, v.BVID)
		if err != nil {
			return err
		}
		for _, page := range detail.Pages {
			if multiPart == "first" && page.Page != 1 {
				continue
			}
			vidTask := Task{
				Site:               "bilibili",
				Kind:               "video",
				TargetID:           v.BVID,
				CID:                page.CID,
				Page:               page.Page,
				Source:             t.Source,
				ConflictResolution: mode,
				LinkedVideo:        true,
				ProgressOwner:      t,
				ProgressPrefix:     "videos/" + v.BVID + "/",
				Attempts:           t.Attempts,
			}
			switch {
			case detail.Author.Mid == dyn.Author.Mid:
			case forward:
				if dir, dirErr := library.EnsureWithinRoot(cacheRoot, filepath.Join(authorDir, "forwards", detail.BVID)); dirErr == nil {
					vidTask.ItemDir, vidTask.ViaName, vidTask.ViaDynamicID = dir, dyn.Author.Name, dyn.ID
				}
			case detail.HasStaffMember(dyn.Author.Mid):
				if dir, dirErr := library.EnsureWithinRoot(cacheRoot, filepath.Join(authorDir, "videos", detail.BVID)); dirErr == nil {
					vidTask.ItemDir = dir
				}
			}
			if err := m.executeVideoTask(ctx, &vidTask); err != nil {
				t.Attempts = vidTask.Attempts
				if errors.Is(err, ErrConflictDetected) || errors.Is(err, ErrRemoteDegradedRefused) || isStopError(err) || isStorageError(err) || ctx.Err() != nil {
					return err
				}
				partialErrors = append(partialErrors, fmt.Sprintf("video %s P%d: %v", v.BVID, page.Page, err))
			} else {
				t.Attempts = vidTask.Attempts
			}
		}
	}

	if len(partialErrors) > 0 && isConflict {
		return wrapRemoteDegradedMsg(fmt.Sprintf("新版部分媒体已失效，已保留本地原文件 (%s)", strings.Join(partialErrors, "; ")))
	}

	status := "complete"
	var missing []library.MissingRecord
	if len(partialErrors) > 0 {
		status = "partial"
		t.Status = "partial"
		for _, pe := range partialErrors {
			missing = append(missing, library.MissingRecord{Item: "media", Reason: pe})
		}
	}
	v2Text := dyn.Text
	if !isConflict {
		v2Text = preserveMonotonicText(previous.Text, dyn.Text, false)
	}
	v2Meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + dyn.ID,
		Site:        "bilibili",
		Kind:        "dynamic",
		Status:      status,
		Title:       dyn.Title,
		Text:        v2Text,
		DynamicType: dyn.Type,
		TypeLabel:   dyn.TypeLabel,
		URL:         dyn.CanonicalURL,
		PubTs:       dyn.PubTs,
		CachedAt:    previous.CachedAt,
		Author:      library.ItemAuthor{Mid: dyn.Author.Mid, Name: dyn.Author.Name, Face: dyn.Author.Face},
		Media:       mediaFiles,
		Videos:      videoRefs,
		Missing:     missing,
		Via:         previous.Via,
	}
	stampCacheStandardAndAssets(&v2Meta, 0, 0, dynAssets)

	saved, err := m.commitStagedItemSwap(itemDir, previous, true, v2Meta, stageDir, mode)
	if err != nil {
		return err
	}
	t.ItemKey = saved.ItemKey
	if len(partialErrors) > 0 {
		return fmt.Errorf("部分媒体下载失败 (%s)", strings.Join(partialErrors, "; "))
	}
	return nil
}
