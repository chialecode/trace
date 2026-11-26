package download

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

func (m *Manager) executeDouyinNoteTask(ctx context.Context, t *Task, plan WorkDownloadPlan, planner WorkPlanner) (result error) {
	previous, exists, itemDir, err := m.resolveDouyinItemDir(t, "dynamic", plan.AuthorUID, t.TargetID)
	if err != nil {
		return err
	}

	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	hasProtectedLocal := exists && localHasProtectedContent(previous)
	mode := effectiveConflictResolution(ctx, t.ConflictResolution, plan.ConflictResolution)
	forceOverwrite := effectiveForceOverwrite(ctx, t.Source, t.ForceOverwrite, plan.ForceOverwrite)
	isConflict := false
	if hasProtectedLocal {
		notice, conflict, degErr := classifyWorkPlanVsLocal(previous, plan, t.ItemKey, t.TargetID, t)
		if degErr != nil {
			return degErr
		}
		isConflict = conflict
		if isConflict && !forceOverwrite && mode != ConflictResolveKeepBoth && mode != ConflictResolveUseRemote {
			m.emitConflict(notice)
			return ErrConflictDetected
		}
		if forceOverwrite && mode == "" {
			mode = ConflictResolveUseRemote
		}
	}

	title := firstNonEmptyStr(plan.Title, plan.Desc, t.Title, "抖音图文 "+t.TargetID)
	desc := plan.Desc
	if hasProtectedLocal && !isConflict && !forceOverwrite {
		title = preserveMonotonicText(previous.Title, title, false)
		desc = preserveMonotonicText(previous.Desc, desc, false)
	}
	canonicalURL := firstNonEmptyStr(sanitizeStoredURL(plan.CanonicalURL), "https://www.douyin.com/note/"+t.TargetID)
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeStoredURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeStoredURL(plan.CoverURLs[0])
	}

	workDir := itemDir
	workTask := t
	var stageSub Task
	if hasProtectedLocal {
		workDir = filepath.Join(itemDir, library.SwapStageDirPrefix+newSwapToken())
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return err
		}
		defer func() {
			_ = library.RemoveTempSubdirSafe(itemDir, workDir)
		}()
		stageSub = cloneStageSubtask(t, workDir)
		workTask = &stageSub
		defer func() {
			t.Attempts = stageSub.Attempts
		}()
	}

	var mediaFiles []library.MediaFile
	var notePages []library.NotePageRecord
	var missing []library.MissingRecord
	var partialErrors []string
	finalSaved := false

	defer func() {
		if result == nil || finalSaved || hasProtectedLocal {
			return
		}
		if !isSiteStopError(result) {
			missing = append(missing, library.MissingRecord{
				Item:   "note",
				Reason: "图文缓存尚未完成 (" + classifyDownloadError(result) + ")",
			})
		}
		partialMeta := library.ItemMetadata{
			Version:     1,
			ID:          "dynamic:" + t.TargetID,
			Site:        "douyin",
			Kind:        "dynamic",
			Status:      "partial",
			Title:       title,
			Desc:        plan.Desc,
			Text:        firstNonEmptyStr(plan.Desc, plan.Title),
			DynamicType: "draw",
			TypeLabel:   "图文",
			URL:         canonicalURL,
			PubTs:       plan.PubTs,
			CachedAt:    previous.CachedAt,
			Author: library.ItemAuthor{
				Site:     "douyin",
				UID:      plan.AuthorUID,
				UniqueID: plan.AuthorUniqueID,
				Name:     t.AuthorName,
				Face:     t.AuthorFace,
			},
			Media:     mergeMediaFiles(previous.Media, mediaFiles),
			NotePages: notePages,
			Missing:   missing,
			Via:       m.douyinItemVia(t, previous, exists, itemDir),
		}
		saved, sErr := m.lib.SaveItem(itemDir, partialMeta)
		if sErr != nil {
			if !isSiteStopError(result) && ctx.Err() == nil {
				result = sErr
			}
			return
		}
		t.ItemKey = saved.ItemKey
	}()

	// Share a single plan refresh across every file in this note so expiring URLs
	// trigger at most one PlanWorkDownload API call for the whole note.
	refreshOncePlan := m.makeSinglePlanRefresher(ctx, t.TargetID, planner)

	// Optional cover.<ext> when explicitly provided.
	if cfg.SaveCover && len(plan.CoverURLs) > 0 {
		reusedCover := false
		if hasProtectedLocal && !isConflict && !forceOverwrite {
			for _, mf := range previous.Media {
				if mf.Role == "cover" && mediaIsValid(previous, mf.Path) {
					if copied, rErr := copyLocalMediaToStageAs(previous, mf.Path, mf.Path, "cover", workDir); rErr == nil {
						mediaFiles = append(mediaFiles, *copied)
						reusedCover = true
					}
					break
				}
			}
		}
		if !reusedCover {
			refreshCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				return fresh.CoverURLs, nil
			})
			if coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "cover", "cover", plan.CoverURLs, 0, 0, plan.Transport, refreshCover); cErr != nil {
				if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
					return cErr
				}
				missing = append(missing, library.MissingRecord{
					Item:   "cover",
					Reason: "封面保存失败 (" + classifyDownloadError(cErr) + ")",
				})
			} else if coverMedia != nil {
				mediaFiles = append(mediaFiles, *coverMedia)
			}
		}
	}

	// Process ordered pages 1..N.
	for i, page := range plan.Pages {
		pageIdx := page.Index
		if pageIdx <= 0 {
			pageIdx = i + 1
		}
		prefix := fmt.Sprintf("images/%02d", pageIdx)
		cleanPageMediaID := library.SanitizeMediaToken(page.MediaID)
		reusableLP, canReuseLocal := findReusableLocalPage(previous, page, pageIdx, isConflict)
		if forceOverwrite {
			canReuseLocal = false
		}
		pageKind := strings.ToLower(strings.TrimSpace(page.Kind))
		if pageKind == "" {
			if len(page.VideoURLs) > 0 {
				pageKind = "live_photo"
			} else {
				pageKind = "image"
			}
		}

		switch pageKind {
		case "image":
			var imgMedia *library.MediaFile
			var imgErr error
			if hasProtectedLocal && !isConflict && canReuseLocal && reusableLP.Image != "" {
				imgMedia, _ = copyLocalMediaToStageAs(previous, reusableLP.Image, prefix, "image", workDir)
			}
			if imgMedia == nil {
				refreshImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				imgMedia, imgErr = m.cacheDouyinSniffedImage(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshImg)
				if imgErr != nil && hasProtectedLocal && canReuseLocal && reusableLP.Image != "" && ctx.Err() == nil && !isStorageError(imgErr) {
					if copied, rErr := copyLocalMediaToStageAs(previous, reusableLP.Image, prefix, "image", workDir); rErr == nil {
						imgMedia, imgErr = copied, nil
					}
				}
			}
			if imgErr != nil {
				if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
					return imgErr
				}
				if hasProtectedLocal && (isConflict || forceOverwrite) && isDegradedSiteError(imgErr) {
					return wrapRemoteDegraded(imgErr)
				}
				reason := fmt.Sprintf("第 %02d 页图片下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
				partialErrors = append(partialErrors, reason)
				notePages = append(notePages, library.NotePageRecord{Index: pageIdx, Kind: "image", MediaID: cleanPageMediaID, Width: page.Width, Height: page.Height})
				continue
			}
			mediaFiles = append(mediaFiles, *imgMedia)
			notePages = append(notePages, library.NotePageRecord{
				Index:   pageIdx,
				Kind:    "image",
				MediaID: cleanPageMediaID,
				Image:   imgMedia.Path,
				Width:   page.Width,
				Height:  page.Height,
			})

		case "live_photo", "motion":
			var imgMedia *library.MediaFile
			var imgErr error
			if hasProtectedLocal && !isConflict && canReuseLocal && reusableLP.Image != "" {
				imgMedia, _ = copyLocalMediaToStageAs(previous, reusableLP.Image, prefix, "image", workDir)
			}
			if imgMedia == nil {
				refreshImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				imgMedia, imgErr = m.cacheDouyinSniffedImage(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshImg)
				if imgErr != nil && hasProtectedLocal && canReuseLocal && reusableLP.Image != "" && ctx.Err() == nil && !isStorageError(imgErr) {
					if copied, rErr := copyLocalMediaToStageAs(previous, reusableLP.Image, prefix, "image", workDir); rErr == nil {
						imgMedia, imgErr = copied, nil
					}
				}
			}
			if imgErr != nil {
				if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
					return imgErr
				}
				if hasProtectedLocal && (isConflict || forceOverwrite) && isDegradedSiteError(imgErr) {
					return wrapRemoteDegraded(imgErr)
				}
				reason := fmt.Sprintf("第 %02d 页实况静帧下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
				partialErrors = append(partialErrors, reason)
				notePages = append(notePages, library.NotePageRecord{Index: pageIdx, Kind: "motion", MediaID: cleanPageMediaID, Width: page.Width, Height: page.Height, Duration: page.DurationSec})
				continue
			}

			motionRel := fmt.Sprintf("images/%02d_live.mp4", pageIdx)
			var motionMedia *library.MediaFile
			var mErr error
			if hasProtectedLocal && !isConflict && canReuseLocal && reusableLP.Motion != "" {
				motionMedia, _ = copyLocalMediaToStageAs(previous, reusableLP.Motion, motionRel, "motion", workDir)
			}
			if motionMedia == nil {
				refreshMotion := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.VideoURLs, nil
					}
					return nil, nil
				})
				motionMedia, mErr = m.cacheDouyinSingleMP4(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "motion", motionRel, page.VideoURLs, page.NeedSetToken, page.DurationSec, page.Width, page.Height, plan.Transport, planner, refreshMotion)
				if mErr != nil && hasProtectedLocal && canReuseLocal && reusableLP.Motion != "" && ctx.Err() == nil && !isStorageError(mErr) {
					if copied, rErr := copyLocalMediaToStageAs(previous, reusableLP.Motion, motionRel, "motion", workDir); rErr == nil {
						motionMedia, mErr = copied, nil
					}
				}
			}
			rec := library.NotePageRecord{
				Index:    pageIdx,
				Kind:     "motion",
				MediaID:  cleanPageMediaID,
				Image:    imgMedia.Path,
				Width:    page.Width,
				Height:   page.Height,
				Duration: page.DurationSec,
			}
			if mErr != nil {
				if isSiteStopError(mErr) || isStorageError(mErr) || ctx.Err() != nil {
					return mErr
				}
				if hasProtectedLocal && (isConflict || forceOverwrite) && isDegradedSiteError(mErr) {
					return wrapRemoteDegraded(mErr)
				}
				reason := fmt.Sprintf("第 %02d 页实况视频下载失败 (%s)", pageIdx, classifyDownloadError(mErr))
				missing = append(missing, library.MissingRecord{Item: motionRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if motionMedia != nil {
				imgMedia.Motion = motionMedia.Path
				rec.Motion = motionMedia.Path
				if motionMedia.Duration > 0 {
					rec.Duration = motionMedia.Duration
				}
				mediaFiles = append(mediaFiles, *motionMedia)
			}
			mediaFiles = append(mediaFiles, *imgMedia)
			notePages = append(notePages, rec)

		case "video":
			var coverRel string
			if len(page.ImageURLs) > 0 {
				var coverMedia *library.MediaFile
				var cErr error
				if hasProtectedLocal && !isConflict && canReuseLocal && reusableLP.Cover != "" {
					coverMedia, _ = copyLocalMediaToStageAs(previous, reusableLP.Cover, prefix, "image", workDir)
				}
				if coverMedia == nil {
					refreshPageCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
						if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
							return p.ImageURLs, nil
						}
						return nil, nil
					})
					coverMedia, cErr = m.cacheDouyinSniffedImage(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshPageCover)
					if cErr != nil && hasProtectedLocal && canReuseLocal && reusableLP.Cover != "" && ctx.Err() == nil && !isStorageError(cErr) {
						if copied, rErr := copyLocalMediaToStageAs(previous, reusableLP.Cover, prefix, "image", workDir); rErr == nil {
							coverMedia, cErr = copied, nil
						}
					}
				}
				if cErr != nil {
					if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
						return cErr
					}
					reason := fmt.Sprintf("第 %02d 页视频封面下载失败 (%s)", pageIdx, classifyDownloadError(cErr))
					missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d.cover", pageIdx), Reason: reason})
					partialErrors = append(partialErrors, reason)
				} else if coverMedia != nil {
					coverRel = coverMedia.Path
					mediaFiles = append(mediaFiles, *coverMedia)
				}
			}

			videoRel := fmt.Sprintf("images/%02d.mp4", pageIdx)
			var vidMedia *library.MediaFile
			var vErr error
			if hasProtectedLocal && !isConflict && canReuseLocal && reusableLP.Video != "" &&
				(reusableLP.VideoInfo == nil || page.Width*page.Height <= reusableLP.VideoInfo.Width*reusableLP.VideoInfo.Height) {
				vidMedia, _ = copyLocalMediaToStageAs(previous, reusableLP.Video, videoRel, "video", workDir)
			}
			if vidMedia == nil {
				refreshPageVideo := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.VideoURLs, nil
					}
					return nil, nil
				})
				vidMedia, vErr = m.cacheDouyinSingleMP4(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "video", videoRel, page.VideoURLs, page.NeedSetToken, page.DurationSec, page.Width, page.Height, plan.Transport, planner, refreshPageVideo)
				if vErr != nil && hasProtectedLocal && canReuseLocal && reusableLP.Video != "" && ctx.Err() == nil && !isStorageError(vErr) {
					if copied, rErr := copyLocalMediaToStageAs(previous, reusableLP.Video, videoRel, "video", workDir); rErr == nil {
						vidMedia, vErr = copied, nil
					}
				}
			}
			rec := library.NotePageRecord{
				Index:    pageIdx,
				Kind:     "video",
				MediaID:  cleanPageMediaID,
				Cover:    coverRel,
				Width:    page.Width,
				Height:   page.Height,
				Duration: page.DurationSec,
			}
			if vErr != nil {
				if isSiteStopError(vErr) || isStorageError(vErr) || ctx.Err() != nil {
					return vErr
				}
				if hasProtectedLocal && (isConflict || forceOverwrite) && isDegradedSiteError(vErr) {
					return wrapRemoteDegraded(vErr)
				}
				reason := fmt.Sprintf("第 %02d 页视频下载失败 (%s)", pageIdx, classifyDownloadError(vErr))
				missing = append(missing, library.MissingRecord{Item: videoRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if vidMedia != nil {
				rec.Video = vidMedia.Path
				if vidMedia.Duration > 0 {
					rec.Duration = vidMedia.Duration
				}
				if vidMedia.Width > 0 {
					rec.Width = vidMedia.Width
				}
				if vidMedia.Height > 0 {
					rec.Height = vidMedia.Height
				}
				rec.VideoInfo = &library.DouyinVideoInfo{
					MediaID:     cleanPageMediaID,
					File:        vidMedia.Path,
					Gear:        page.Gear,
					QualityName: page.QualityName,
					Codec:       page.Codec,
					Width:       rec.Width,
					Height:      rec.Height,
					FPS:         page.FPS,
					Bitrate:     page.Bitrate,
				}
				mediaFiles = append(mediaFiles, *vidMedia)
			}
			notePages = append(notePages, rec)

		default:
			// Unrecognized page kind ("unknown"): keep available image, record unknownKind + missing, never fail whole item.
			var savedImg string
			if len(page.ImageURLs) > 0 {
				refreshUnknownImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				if imgMedia, imgErr := m.cacheDouyinSniffedImage(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshUnknownImg); imgErr != nil {
					if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
						return imgErr
					}
				} else if imgMedia != nil {
					savedImg = imgMedia.Path
					mediaFiles = append(mediaFiles, *imgMedia)
				}
			}
			unkTag := firstNonEmptyStr(page.UnknownKind, pageKind, "unknown")
			reason := firstNonEmptyStr(page.UnknownReason, fmt.Sprintf("第 %02d 页包含暂未识别的页面类型 (%s)，已保留可用图片", pageIdx, unkTag))
			missing = append(missing, library.MissingRecord{
				Item:   fmt.Sprintf("page_%02d", pageIdx),
				Reason: reason,
			})
			notePages = append(notePages, library.NotePageRecord{
				Index:       pageIdx,
				Kind:        "unknown",
				MediaID:     cleanPageMediaID,
				UnknownKind: unkTag,
				Image:       savedImg,
				Width:       page.Width,
				Height:      page.Height,
			})
		}
	}

	// Process background music if present on the note (preserving verified local music monotonically when same ID!).
	var musicInfo *library.MusicInfo
	if plan.Music != nil {
		musicCover := ""
		if len(plan.Music.CoverURLs) > 0 {
			musicCover = sanitizeStoredURL(plan.Music.CoverURLs[0])
		}
		musicInfo = &library.MusicInfo{
			ID:       plan.Music.ID,
			Title:    plan.Music.Title,
			Author:   plan.Music.Author,
			Duration: plan.Music.DurationSec,
			Cover:    musicCover,
			Original: plan.Music.Original,
		}
		sameMusic := !forceOverwrite && previous.Music != nil && previous.Music.File != "" && mediaIsValid(previous, previous.Music.File) &&
			(!isConflict || (previous.Music.ID != "" && plan.Music.ID == previous.Music.ID))
		if len(plan.Music.PlayURLs) == 0 {
			if hasProtectedLocal && sameMusic {
				if copied, rErr := copyLocalMediaToStageAs(previous, previous.Music.File, previous.Music.File, "audio", workDir); rErr == nil {
					musicInfo.File = copied.Path
					mediaFiles = append(mediaFiles, *copied)
				}
			}
			if musicInfo.File == "" {
				missing = append(missing, library.MissingRecord{
					Item:   "music",
					Reason: "配乐无可用地址（版权或地区限制）",
				})
			}
		} else if hasProtectedLocal && !isConflict && sameMusic {
			if copied, rErr := copyLocalMediaToStageAs(previous, previous.Music.File, previous.Music.File, "audio", workDir); rErr == nil {
				musicInfo.File = copied.Path
				mediaFiles = append(mediaFiles, *copied)
			}
		} else {
			refreshMusic := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if fresh.Music != nil {
					return fresh.Music.PlayURLs, nil
				}
				return nil, nil
			})
			musicTransport := plan.Transport
			musicTransport.PublicRedirects = true
			musicMedia, mErr := m.cacheDouyinSniffedAudio(ctx, workTask, workDir, previous, exists && !hasProtectedLocal, "music", plan.Music.PlayURLs, plan.Music.DurationSec, musicTransport, refreshMusic)
			if mErr != nil && hasProtectedLocal && sameMusic && ctx.Err() == nil && !isStorageError(mErr) {
				if copied, rErr := copyLocalMediaToStageAs(previous, previous.Music.File, previous.Music.File, "audio", workDir); rErr == nil {
					musicMedia, mErr = copied, nil
				}
			}
			if mErr != nil {
				if isSiteStopError(mErr) || isStorageError(mErr) || ctx.Err() != nil {
					return mErr
				}
				reason := musicFailureReason(mErr)
				missing = append(missing, library.MissingRecord{
					Item:   "music",
					Reason: reason,
				})
				partialErrors = append(partialErrors, reason)
			} else if musicMedia != nil {
				musicInfo.File = musicMedia.Path
				mediaFiles = append(mediaFiles, *musicMedia)
			}
		}
	} else if hasProtectedLocal && !isConflict && !forceOverwrite && previous.Music != nil && previous.Music.File != "" && mediaIsValid(previous, previous.Music.File) {
		cp := *previous.Music
		if copied, rErr := copyLocalMediaToStageAs(previous, previous.Music.File, previous.Music.File, "audio", workDir); rErr == nil {
			cp.File = copied.Path
			musicInfo = &cp
			mediaFiles = append(mediaFiles, *copied)
		}
	}

	if hasProtectedLocal && (isConflict || forceOverwrite) && len(partialErrors) > 0 {
		return wrapRemoteDegradedMsg(fmt.Sprintf("新版部分媒体下载失败，已保留本地原文件 (%s)", strings.Join(partialErrors, "; ")))
	}

	status := "complete"
	if len(missing) > 0 || len(partialErrors) > 0 {
		status = "partial"
		t.Status = "partial"
		t.ErrorMessage = summarizeMissingReasons(missing)
	}

	mediaFiles, notePages = preserveValidPreviousImagesForStage(itemDir, workDir, previous, exists && !isConflict && !forceOverwrite, mediaFiles, notePages)

	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + t.TargetID,
		Site:        "douyin",
		Kind:        "dynamic",
		Status:      status,
		Title:       title,
		Desc:        desc,
		Text:        firstNonEmptyStr(desc, title),
		DynamicType: "draw",
		TypeLabel:   "图文",
		URL:         canonicalURL,
		PubTs:       plan.PubTs,
		CachedAt:    previous.CachedAt,
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      plan.AuthorUID,
			UniqueID: plan.AuthorUniqueID,
			Name:     t.AuthorName,
			Face:     t.AuthorFace,
		},
		Media:        mediaFiles,
		NotePages:    notePages,
		Music:        musicInfo,
		Missing:      missing,
		DirectCached: true,
		Via:          m.douyinItemVia(t, previous, exists, itemDir),
	}
	stampCacheStandardAndAssets(&meta, plan.EditTs, plan.EditCount, buildPlanAssetIDs(plan))

	if hasProtectedLocal {
		saved, sErr := m.commitStagedItemSwap(itemDir, previous, true, meta, workDir, mode)
		if sErr != nil {
			return sErr
		}
		t.ItemKey = saved.ItemKey
		finalSaved = true
		return nil
	}

	saved, saveErr := m.lib.SaveItem(itemDir, meta)
	if saveErr != nil {
		return saveErr
	}
	t.ItemKey = saved.ItemKey
	finalSaved = true
	return nil
}

func (m *Manager) cacheDouyinSniffedImage(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	role string,
	relStem string,
	urls []string,
	width, height int,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	inSwapStage := isSwapStageDir(itemDir)
	firstURL := ""
	if len(urls) > 0 {
		firstURL = urls[0]
	}
	if !inSwapStage {
		if kept, ok := m.findExistingValidImageSlot(t, itemDir, previous, exists, role, relStem, firstURL, width, height, false); ok {
			m.setFileProgress(t, role, kept.Path, "completed", kept.Bytes, kept.Bytes, 0)
			return kept, nil
		}
	}

	tempRel := relStem + imgSuffix
	if !inSwapStage {
		m.adoptLegacyStaging(t, itemDir, relStem, ".imgtmp", imgSuffix)
	}
	tempFull, err := library.ResolveItemPath(itemDir, tempRel)
	if err != nil {
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, tempRel, tempFull); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(tempFull), 0o700); err != nil {
		return nil, err
	}
	n, sha, ct, err := m.downloadWithTransportRefreshCT(ctx, transport, urls, tempFull, role, t, refreshURLs)
	var ext string
	if err == nil {
		if site.LooksLikeExpiredPlaceholderImageFile(tempFull, firstURL, width, height, false) {
			_ = os.Remove(tempFull)
			err = &site.Error{Site: transport.Site, Kind: site.ErrKindUnavailable, Message: WeiboImageUnavailableReason}
			if fbN, fbSHA, fbExt, fbOK := m.tryPublicImageCDNFallbackToFile(ctx, urls, width, height, false, tempFull, transport.AllowLocalhost, transport.UserAgent); fbOK {
				n, sha, ext, err = fbN, fbSHA, fbExt, nil
			}
		} else {
			var sErr error
			ext, sErr = sniffImageExtension(tempFull, ct)
			if sErr != nil {
				_ = os.Remove(tempFull)
				err = &site.Error{Site: transport.Site, Kind: site.ErrKindSchemaChanged, Message: sErr.Error()}
			}
		}
	} else if !isSiteStopError(err) && !isStorageError(err) && ctx.Err() == nil && site.ShouldAttemptImageCDNFallback(err, false) {
		_ = os.Remove(tempFull)
		_ = os.Remove(tempFull + ".part")
		if fbN, fbSHA, fbExt, fbOK := m.tryPublicImageCDNFallbackToFile(ctx, urls, width, height, false, tempFull, transport.AllowLocalhost, transport.UserAgent); fbOK {
			n, sha, ext, err = fbN, fbSHA, fbExt, nil
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			if !fileOrPartExists(tempFull) {
				m.removeTempTaskProgress(t, tempRel)
			}
		}
		return nil, err
	}
	finalRel := relStem + ext
	writeFinalRel := finalRel
	if inSwapStage {
		writeFinalRel = stagedRelPath(finalRel)
	}
	finalFull, err := library.ResolveItemPath(itemDir, writeFinalRel)
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, writeFinalRel, finalFull); err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(tempFull, finalFull); err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	m.removeTempTaskProgress(t, tempRel)
	m.setFileProgress(t, role, finalRel, "completed", n, n, 0)
	return &library.MediaFile{
		Role:   role,
		Path:   finalRel,
		Bytes:  n,
		SHA256: sha,
		Width:  width,
		Height: height,
	}, nil
}

func (m *Manager) cacheDouyinSniffedAudio(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	relStem string,
	urls []string,
	durationSec int,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	inSwapStage := isSwapStageDir(itemDir)
	if !inSwapStage && exists {
		for _, mf := range previous.Media {
			if mf.Role == "audio" && strings.HasPrefix(mf.Path, relStem+".") && m.mediaAlreadyCompleted(previous, mf) {
				m.setFileProgress(t, "audio", mf.Path, "completed", mf.Bytes, mf.Bytes, 0)
				cp := mf
				return &cp, nil
			}
		}
	}

	tempRel := relStem + audSuffix
	if !inSwapStage {
		m.adoptLegacyStaging(t, itemDir, relStem, ".audtmp", audSuffix)
	}
	tempFull, err := library.ResolveItemPath(itemDir, tempRel)
	if err != nil {
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, tempRel, tempFull); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, sha, ct, err := m.downloadWithTransportRefreshCT(ctx, transport, urls, tempFull, "audio", t, refreshURLs)
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			if !fileOrPartExists(tempFull) {
				m.removeTempTaskProgress(t, tempRel)
			}
		}
		return nil, err
	}
	ext, err := sniffAudioExtension(tempFull, ct)
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindSchemaChanged, Message: err.Error()}
	}
	finalRel := relStem + ext
	writeFinalRel := finalRel
	if inSwapStage {
		writeFinalRel = stagedRelPath(finalRel)
	}
	finalFull, err := library.ResolveItemPath(itemDir, writeFinalRel)
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, writeFinalRel, finalFull); err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(tempFull, finalFull); err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	m.removeTempTaskProgress(t, tempRel)
	m.setFileProgress(t, "audio", finalRel, "completed", n, n, 0)
	return &library.MediaFile{
		Role:     "audio",
		Path:     finalRel,
		Bytes:    n,
		SHA256:   sha,
		Duration: durationSec,
	}, nil
}

func findPlannedPage(pages []PlannedNotePage, index, fallbackPos int) (PlannedNotePage, bool) {
	for _, p := range pages {
		if p.Index == index {
			return p, true
		}
	}
	if fallbackPos >= 0 && fallbackPos < len(pages) {
		return pages[fallbackPos], true
	}
	return PlannedNotePage{}, false
}

func replaceRoleMedia(existing []library.MediaFile, role string, added library.MediaFile) []library.MediaFile {
	var filtered []library.MediaFile
	for _, mf := range existing {
		if mf.Role != role {
			filtered = append(filtered, mf)
		}
	}
	return append(filtered, added)
}
