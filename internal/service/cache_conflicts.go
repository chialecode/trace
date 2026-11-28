package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
)

const (
	bilibiliRecheckCooldown = 7 * 24 * time.Hour
	bilibiliRecentUploadAge = 14 * 24 * time.Hour
	conflictUnchangedSuffix = "（本地原文件未改动）"
)

var errConflictBusy = errors.New("该内容冲突正在处理中")

// ListCacheConflicts returns all unresolved (pending/resolving/failed) cache conflicts
// sorted newest DetectedAt first, with local AvatarKey and ReasonLabel populated.
func (b *Backend) ListCacheConflicts() ([]CacheConflictDTO, error) {
	if b == nil {
		return nil, nil
	}
	if err := b.requireDesktop(); err != nil {
		return nil, err
	}
	if b.conflicts == nil {
		return nil, nil
	}
	b.reconcileLegacyInlineURLConflicts()
	recs := b.conflicts.snapshotSorted()
	out := make([]CacheConflictDTO, 0, len(recs))
	for _, r := range recs {
		authorUID := r.AuthorUID
		authorName := r.AuthorName
		title := r.Title
		if (authorUID == "" || authorName == "" || title == "" || title == r.TargetID) && b.lib != nil {
			if meta, ok, err := b.lib.GetItemMetaOnly(r.ItemKey); err == nil && ok {
				if authorUID == "" {
					authorUID = itemAuthorUIDString(meta)
				}
				if authorName == "" {
					authorName = strings.TrimSpace(meta.Author.Name)
				}
				if title == "" || title == r.TargetID {
					title = firstNonEmpty(meta.Title, meta.Desc, meta.Text, title)
				}
			}
		}
		avatarKey := ""
		if authorUID != "" {
			avatarKey = b.AccountAvatarPath(r.Site, authorUID)
		}
		out = append(out, CacheConflictDTO{
			ID:                r.ID,
			ItemKey:           r.ItemKey,
			Site:              r.Site,
			Kind:              r.Kind,
			TargetID:          r.TargetID,
			AuthorUID:         authorUID,
			AuthorName:        authorName,
			AvatarKey:         avatarKey,
			Title:             title,
			DetectedAt:        r.DetectedAt,
			Origin:            r.Origin,
			ReasonKind:        r.ReasonKind,
			ReasonLabel:       ConflictReasonLabel(r.ReasonKind),
			LocalSummary:      r.LocalSummary,
			RemoteSummary:     r.RemoteSummary,
			RemoteFingerprint: r.RemoteFingerprint,
			State:             r.State,
			LastError:         r.LastError,
		})
	}
	return out, nil
}

// PendingCacheConflictCount returns the count of unresolved cache conflicts for the Tasks sidebar.
func (b *Backend) PendingCacheConflictCount() int {
	if b == nil || b.requireDesktop() != nil || b.conflicts == nil {
		return 0
	}
	b.reconcileLegacyInlineURLConflicts()
	return b.conflicts.count()
}

func validateConflictChoice(choice string) (string, error) {
	switch strings.TrimSpace(choice) {
	case ConflictKeepLocal:
		return ConflictKeepLocal, nil
	case ConflictKeepBoth:
		return ConflictKeepBoth, nil
	case ConflictUseRemote:
		return ConflictUseRemote, nil
	default:
		return "", &site.Error{
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的冲突处理方式",
		}
	}
}

// ResolveCacheConflict applies choice ("keep_both", "keep_local", or "use_remote") to conflict id.
func (b *Backend) ResolveCacheConflict(ctx context.Context, id, choice string) error {
	if b == nil {
		return nil
	}
	if err := b.requireDesktop(); err != nil {
		return err
	}
	normChoice, err := validateConflictChoice(choice)
	if err != nil {
		return err
	}
	if b.conflicts == nil {
		return &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该内容冲突记录"}
	}
	cleanID := strings.TrimSpace(id)
	if cleanID == "" {
		return &site.Error{Kind: site.ErrKindBadRequest, Message: "冲突记录编号不能为空"}
	}

	// "保留本地": zero network calls, zero CacheDir mutations; record ignored fingerprint and remove conflict.
	if normChoice == ConflictKeepLocal {
		var rec cacheConflictRecord
		s := b.conflicts
		s.mu.Lock()
		idx := -1
		for i := range s.records {
			if s.records[i].ID == cleanID {
				idx = i
				rec = s.records[i]
				break
			}
		}
		if idx < 0 {
			s.mu.Unlock()
			return &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该内容冲突记录"}
		}
		if rec.State == ConflictStateResolving {
			s.mu.Unlock()
			return fmt.Errorf("%w", errConflictBusy)
		}
		fp := sanitizeConflictFingerprint(rec.RemoteFingerprint)
		if fp == "" {
			fp = sanitizeConflictFingerprint(rec.ReasonKind + "|" + rec.RemoteSummary)
		}
		prevRecords := append([]cacheConflictRecord(nil), s.records...)
		prevIgnored, hadPrevIgnored := s.ignored[rec.ItemKey]
		s.ignored[rec.ItemKey] = IgnoredRevision{
			RemoteFingerprint: fp,
			IgnoredAt:         s.nowRFC3339(),
		}
		s.records = append(s.records[:idx], s.records[idx+1:]...)
		if saveErr := s.saveLocked(); saveErr != nil {
			s.records = prevRecords
			if hadPrevIgnored {
				s.ignored[rec.ItemKey] = prevIgnored
			} else {
				delete(s.ignored, rec.ItemKey)
			}
			s.mu.Unlock()
			return saveErr
		}
		s.mu.Unlock()
		if b.events != nil {
			b.events.PublishSite(EventCacheConflictsChanged, rec.Site, rec.ItemKey)
		}
		return nil
	}

	// "保留两版" / "用新版替换": verify site login before touching conflict state or V1!
	s := b.conflicts
	s.mu.Lock()
	idx := -1
	var rec cacheConflictRecord
	for i := range s.records {
		if s.records[i].ID == cleanID {
			idx = i
			rec = s.records[i]
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该内容冲突记录"}
	}
	if rec.State == ConflictStateResolving {
		s.mu.Unlock()
		return fmt.Errorf("%w", errConflictBusy)
	}
	siteID, sErr := normalizeExplicitSiteID(rec.Site)
	if sErr != nil {
		s.mu.Unlock()
		return sErr
	}
	if authErr := b.requireSiteLogin(siteID); authErr != nil {
		s.mu.Unlock()
		return authErr
	}

	prevState := s.records[idx].State
	prevLastErr := s.records[idx].LastError
	s.records[idx].State = ConflictStateResolving
	s.records[idx].LastError = ""
	if saveErr := s.saveLocked(); saveErr != nil {
		s.records[idx].State = prevState
		s.records[idx].LastError = prevLastErr
		s.mu.Unlock()
		return saveErr
	}
	rec = s.records[idx]
	s.mu.Unlock()

	if b.events != nil {
		b.events.PublishSite(EventCacheConflictsChanged, rec.Site, rec.ItemKey)
	}

	execErr := b.executeConflictResolution(ctx, rec, siteID, normChoice)
	if execErr == nil {
		s.mu.Lock()
		for i := range s.records {
			if s.records[i].ID == cleanID {
				s.records = append(s.records[:i], s.records[i+1:]...)
				_ = s.saveLocked()
				break
			}
		}
		s.mu.Unlock()
		if b.events != nil {
			b.events.PublishSite(EventCacheConflictsChanged, rec.Site, rec.ItemKey)
		}
		return nil
	}

	formattedErr := b.formatConflictFailure(execErr)
	s.mu.Lock()
	for i := range s.records {
		if s.records[i].ID == cleanID {
			s.records[i].State = ConflictStateFailed
			s.records[i].LastError = formattedErr
			_ = s.saveLocked()
			break
		}
	}
	s.mu.Unlock()
	if b.events != nil {
		b.events.PublishSite(EventCacheConflictsChanged, rec.Site, rec.ItemKey)
	}
	return execErr
}

// ResolveAllCacheConflicts applies choice across all pending/failed conflicts.
func (b *Backend) ResolveAllCacheConflicts(ctx context.Context, choice string) (ConflictBatchResult, error) {
	if b == nil {
		return ConflictBatchResult{}, nil
	}
	if err := b.requireDesktop(); err != nil {
		return ConflictBatchResult{}, err
	}
	if strings.TrimSpace(choice) == "" {
		choice = DefaultConflictChoice
	}
	normChoice, err := validateConflictChoice(choice)
	if err != nil {
		return ConflictBatchResult{}, err
	}
	if b.conflicts == nil {
		return ConflictBatchResult{}, nil
	}

	snapshot := b.conflicts.snapshotSorted()
	var res ConflictBatchResult
	for _, item := range snapshot {
		if ctx != nil && ctx.Err() != nil && normChoice != ConflictKeepLocal {
			return res, ctx.Err()
		}
		curState, exists := b.conflictStateByID(item.ID)
		if !exists {
			continue
		}
		if curState == ConflictStateResolving {
			res.SkippedBusy++
			continue
		}
		if normChoice != ConflictKeepLocal {
			siteID, sErr := normalizeExplicitSiteID(item.Site)
			if sErr == nil && b.requireSiteLogin(siteID) != nil {
				res.SkippedAuth++
				continue
			}
		}
		rErr := b.ResolveCacheConflict(ctx, item.ID, normChoice)
		switch {
		case rErr == nil:
			res.Applied++
		case errors.Is(rErr, errConflictBusy):
			res.SkippedBusy++
		case site.IsKind(rErr, site.ErrKindAuthRequired) || bilibili.IsKind(rErr, bilibili.ErrKindAuthRequired):
			res.SkippedAuth++
		default:
			res.Failed++
		}
	}
	return res, nil
}

func (b *Backend) conflictStateByID(id string) (string, bool) {
	if b == nil || b.conflicts == nil {
		return "", false
	}
	b.conflicts.mu.Lock()
	defer b.conflicts.mu.Unlock()
	for _, r := range b.conflicts.records {
		if r.ID == id {
			return r.State, true
		}
	}
	return "", false
}

func (b *Backend) formatConflictFailure(err error) string {
	var msg string
	if err != nil {
		msg = err.Error()
	}
	if b != nil && b.conflicts != nil {
		msg = b.conflicts.sanitizeText(msg)
	} else {
		msg = sanitizeConflictText(msg)
	}
	msg = strings.TrimRight(strings.TrimSpace(msg), "。.；;")
	if msg == "" {
		msg = "处理内容冲突失败"
	}
	if !strings.HasSuffix(msg, conflictUnchangedSuffix) {
		msg += conflictUnchangedSuffix
	}
	return msg
}

func wrapDegradedIfRemoteInvalid(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRemoteDegradedRefused) {
		return err
	}
	if site.IsKind(err, site.ErrKindUnavailable) ||
		site.IsKind(err, site.ErrKindForbidden) ||
		site.IsKind(err, site.ErrKindBlocked) ||
		bilibili.IsKind(err, bilibili.ErrKindUnavailable) ||
		bilibili.IsKind(err, bilibili.ErrKindForbidden) {
		return fmt.Errorf("%w：%v", ErrRemoteDegradedRefused, err)
	}
	return err
}

func (b *Backend) executeConflictResolution(ctx context.Context, rec cacheConflictRecord, siteID site.ID, choice string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	resCtx := download.WithConflictResolution(ctx, choice)

	var localMeta library.ItemMetadata
	var hasLocal bool
	if b.lib != nil {
		localMeta, hasLocal, _ = b.lib.GetItem(rec.ItemKey)
	}
	localHadMedia := hasLocal && hasValidLocalMedia(localMeta)
	via := rec.Via
	if via == nil && hasLocal && localMeta.Via != nil {
		cp := *localMeta.Via
		via = &cp
	}

	switch siteID {
	case site.Bilibili:
		if rec.Kind == "video" {
			vd, err := b.bili.VideoDetail(resCtx, rec.TargetID)
			if err != nil {
				return wrapDegradedIfRemoteInvalid(err)
			}
			if vd.IsUpowerLocked() {
				return wrapDegradedIfRemoteInvalid(bilibili.NewUpowerLockedError("/x/web-interface/wbi/view"))
			}
			if len(vd.Pages) == 0 {
				return fmt.Errorf("%w：源站视频已无可用分P", ErrRemoteDegradedRefused)
			}
			// Pre-probe P1 PlayURL so preview-only / restricted videos are hard-blocked before enqueueing,
			// unless P1's CID already exists as a verified local V1 part (mixed V2 edit Invariant #3, where
			// executeBilibiliVideoConflictReplace reuses local P1 even if P1's remote PlayURL expired).
			if !localMetaHasVerifiedPartCID(localMeta, vd.Pages[0].CID) {
				if _, pErr := b.bili.PlayURL(resCtx, vd.BVID, vd.Pages[0].CID, b.bilibiliPlaySelectionOpts()); pErr != nil {
					return wrapDegradedIfRemoteInvalid(pErr)
				}
			}
			var forMid int64
			if via != nil && via.Reason == library.ViaCollab {
				forMid, _ = strconv.ParseInt(strings.TrimSpace(via.UID), 10, 64)
			}
			tasks, err := b.EnqueueVideoCache(resCtx, download.VideoEnqueueRequest{
				BVID:   vd.BVID,
				Detail: &vd,
				Source: "manual",
				ForMid: forMid,
			})
			if err != nil {
				return wrapDegradedIfRemoteInvalid(err)
			}
			ids := make([]string, 0, len(tasks))
			for _, t := range tasks {
				ids = append(ids, t.TaskID)
			}
			return b.waitConflictTasks(resCtx, ids)
		}

		dyn, err := b.bili.DynamicDetail(resCtx, rec.TargetID)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		if dyn.Degraded || dyn.Blocked {
			return fmt.Errorf("%w：源站动态已不可用或受限", ErrRemoteDegradedRefused)
		}
		if hasLocal && hasSnapshotMedia(localMeta, "orig.json") && dyn.Origin == nil {
			return fmt.Errorf("%w：转发原动态已在源站失效", ErrRemoteDegradedRefused)
		}
		effImgs, effVids := effectiveBiliDynamicMedia(dyn)
		if localHadMedia && len(effImgs) == 0 && len(effVids) == 0 {
			return fmt.Errorf("%w：源站动态已无有效媒体附件", ErrRemoteDegradedRefused)
		}
		t, err := b.EnqueueDynamicCache(resCtx, download.DynamicEnqueueRequest{
			DynamicID: dyn.ID,
			Item:      &dyn,
			Source:    "manual",
		})
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		return b.waitConflictTasks(resCtx, []string{t.TaskID})

	case site.Douyin:
		detail, err := b.douyin.GetWorkDetail(resCtx, rec.TargetID, douyin.StreamSelectOptions{
			PreferredCodec: b.preferredDouyinCodec(),
		})
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		if detail == nil || detail.Uncacheable || detail.Kind == douyin.WorkKindUnsupported {
			reason := "源站作品当前受限或不可缓存"
			if detail != nil && strings.TrimSpace(detail.UncacheableReason) != "" {
				reason = detail.UncacheableReason
			}
			return fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, reason)
		}
		if localHadMedia {
			if detail.Kind == douyin.WorkKindNote && len(detail.Pages) == 0 {
				return fmt.Errorf("%w：源站图文已无有效页面", ErrRemoteDegradedRefused)
			}
			if detail.Kind == douyin.WorkKindVideo && (detail.Video == nil || len(detail.Video.URLs) == 0) {
				return fmt.Errorf("%w：源站视频已无可用播放流", ErrRemoteDegradedRefused)
			}
		}
		plan, err := b.buildDouyinWorkPlanFromDetail(resCtx, detail, b.currentWorkPlanOptions())
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		plan.ConflictResolution = choice
		var forUID, forName string
		if via != nil && via.Reason == library.ViaCollab {
			forUID, forName = via.UID, via.Name
		}
		t, err := b.enqueueDouyinWorkWithPlan(resCtx, rec.TargetID, "manual", forUID, forName, &plan)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		return b.waitConflictTasks(resCtx, []string{t.TaskID})

	case site.Weibo:
		postOpt := weibo.PostOptions{
			StreamOptions: weibo.StreamSelectOptions{
				PreferredCodec: b.preferredWeiboCodec(),
				MaxResolution:  b.currentWeiboMaxResolution(),
			},
		}
		if rec.Kind == "video" {
			oid := rec.TargetID
			if strings.HasPrefix(oid, "1034_") {
				oid = "1034:" + strings.TrimPrefix(oid, "1034_")
			}
			vr, err := b.weibo.GetVideo(resCtx, oid, postOpt)
			if err != nil {
				return wrapDegradedIfRemoteInvalid(err)
			}
			if vr == nil || (vr.HostPost == nil && (vr.Video == nil || vr.Video.Selected == nil || strings.TrimSpace(vr.Video.Selected.URL) == "")) {
				return fmt.Errorf("%w：源站微博视频已失效", ErrRemoteDegradedRefused)
			}
			t, err := b.CacheWeiboVideo(resCtx, oid)
			if err != nil {
				return wrapDegradedIfRemoteInvalid(err)
			}
			return b.waitConflictTasks(resCtx, []string{t.TaskID})
		}

		post, err := b.weibo.GetPost(resCtx, rec.TargetID, postOpt)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		if post == nil || post.Unavailable {
			reason := "源站微博已不可见或已删除"
			if post != nil && strings.TrimSpace(post.UnavailableReason) != "" {
				reason = post.UnavailableReason
			}
			return fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, reason)
		}
		if post.Retweeted != nil && post.Retweeted.Unavailable && hasLocal && localMeta.Origin != nil && !localMeta.Origin.Unavailable {
			return fmt.Errorf("%w：源站转发原帖已失效", ErrRemoteDegradedRefused)
		}
		imgs, vid := post.EffectiveMedia()
		if localHadMedia && len(imgs) == 0 && (vid == nil || vid.Selected == nil || strings.TrimSpace(vid.Selected.URL) == "") {
			return fmt.Errorf("%w：源站微博已无有效媒体附件", ErrRemoteDegradedRefused)
		}
		plan, err := b.buildWeiboWorkPlanFromPost(post)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		plan.ConflictResolution = choice
		t, err := b.enqueueWeiboPostWithSource(resCtx, rec.TargetID, "manual", via, &plan)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		return b.waitConflictTasks(resCtx, []string{t.TaskID})

	case site.X:
		post, err := b.x.GetPost(resCtx, rec.TargetID)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		if post == nil || post.Unavailable || post.IsPreviewDisplay {
			reason := "源站帖子已不可用或仅展示付费预览"
			if post != nil && strings.TrimSpace(post.UnavailableReason) != "" {
				reason = post.UnavailableReason
			}
			return fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, reason)
		}
		if post.OriginUnavailable && hasLocal && localMeta.Origin != nil && !localMeta.Origin.Unavailable {
			return fmt.Errorf("%w：源站引用或转推原帖已失效", ErrRemoteDegradedRefused)
		}
		if localHadMedia && !xPostHasValidDownloadableMedia(post) {
			return fmt.Errorf("%w：源站帖子已无有效可下载媒体", ErrRemoteDegradedRefused)
		}
		plan, err := b.buildXWorkPlanFromPost(post)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		plan.ConflictResolution = choice
		t, err := b.enqueueXPostWithSource(resCtx, rec.TargetID, "manual", via, &plan)
		if err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
		return b.waitConflictTasks(resCtx, []string{t.TaskID})

	default:
		return &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

func localMetaHasVerifiedPartCID(meta library.ItemMetadata, cid int64) bool {
	if cid <= 0 || strings.TrimSpace(meta.ItemDir) == "" {
		return false
	}
	for _, lp := range meta.Parts {
		if lp.CID != cid || strings.TrimSpace(lp.File) == "" {
			continue
		}
		for _, mf := range meta.Media {
			if strings.EqualFold(mf.Path, lp.File) && library.VerifyMediaFile(meta.ItemDir, mf, true) {
				return true
			}
		}
	}
	return false
}

func (b *Backend) waitConflictTasks(ctx context.Context, taskIDs []string) error {
	if len(taskIDs) == 0 {
		return nil
	}
	if b.dl != nil {
		if err := b.dl.WaitTasks(ctx, taskIDs...); err != nil {
			return wrapDegradedIfRemoteInvalid(err)
		}
	}
	wakeCh := make(chan struct{}, 1)
	unsub := b.SubscribeEvents(func(ev Event) {
		if ev.Type == EventTaskUpdated || ev.Type == EventLibraryChanged {
			select {
			case wakeCh <- struct{}{}:
			default:
			}
		}
	})
	defer unsub()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		allDone := true
		for _, taskID := range taskIDs {
			t, ok, err := b.lookupConflictTask(taskID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("未找到冲突处理下载任务 %s", taskID)
			}
			switch t.Status {
			case "queued", "running", "downloading", "muxing":
				allDone = false
			case "completed":
				// Succeeded.
			default: // "partial", "failed", "canceled", "paused"
				msg := firstNonEmpty(t.ErrorMessage, t.LastError, "冲突处理下载未完成")
				if strings.Contains(msg, ErrRemoteDegradedRefused.Error()) ||
					strings.Contains(msg, "已拒绝覆盖本地缓存") ||
					strings.Contains(msg, download.WeiboImageUnavailableReason) ||
					strings.Contains(msg, download.XHLSOnlyMissingReason) ||
					t.ErrorKind == site.ErrKindUnavailable ||
					t.ErrorKind == site.ErrKindForbidden ||
					t.ErrorKind == site.ErrKindBlocked {
					return fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, msg)
				}
				if strings.Contains(msg, download.ErrConflictDetected.Error()) {
					return fmt.Errorf("%w：%s", download.ErrConflictDetected, msg)
				}
				return errors.New(msg)
			}
		}
		if allDone {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wakeCh:
		case <-ticker.C:
		}
	}
}

func (b *Backend) lookupConflictTask(taskID string) (download.Task, bool, error) {
	if detail, ok, err := b.TaskDetail(taskID); err == nil && ok {
		return detail.Task, true, nil
	}
	if tasks, err := b.ListDownloadTasks(); err == nil {
		for _, t := range tasks {
			if t.TaskID == taskID {
				return t, true, nil
			}
		}
	}
	if b.dl != nil {
		return b.dl.GetTask(taskID)
	}
	return download.Task{}, false, nil
}

func (b *Backend) bilibiliPlaySelectionOpts() bilibili.PlaySelectionOptions {
	if b == nil || b.settings == nil {
		return bilibili.PlaySelectionOptions{TargetQn: 127}
	}
	dlCfg := b.settings.Get().Download
	return bilibili.PlaySelectionOptions{
		CodecOrder:          append([]string(nil), dlCfg.CodecOrder...),
		PreferredCodec:      dlCfg.PreferredVideoCodec,
		PreferLosslessAudio: dlCfg.PreferLosslessAudio,
		PreferDolbyAudio:    dlCfg.PreferDolbyAudio,
		TargetQn:            parseBilibiliQualityCap(dlCfg.VideoQuality),
	}
}

func parseBilibiliQualityCap(videoQuality string) int {
	switch strings.ToLower(strings.TrimSpace(videoQuality)) {
	case "127", "8k":
		return 127
	case "120", "2160", "2160p", "4k":
		return 120
	case "116", "1080p60":
		return 116
	case "112", "1080p+":
		return 112
	case "80", "1080", "1080p":
		return 80
	case "64", "720", "720p":
		return 64
	case "32", "480", "480p":
		return 32
	case "16", "360", "360p":
		return 16
	default:
		return 127
	}
}

// --- 4-Site Quota-Safe fullCache & cache-queue Re-Inspection ---

type cachedInspectAction int

const (
	inspectSkipNoOp cachedInspectAction = iota
	inspectEnqueue
	inspectConflict
	inspectProbePaced
)

type cachedInspectDecision struct {
	action      cachedInspectAction
	conflict    cacheConflictRecord
	enqueue     func(ctx context.Context) (queueEnqueued, error)
	postInspect func()
}

func hasValidLocalCache(meta library.ItemMetadata) bool {
	if meta.Status == "complete" {
		return true
	}
	return hasValidLocalMedia(meta)
}

func hasValidLocalMedia(meta library.ItemMetadata) bool {
	for _, p := range meta.Parts {
		if strings.TrimSpace(p.File) != "" {
			return true
		}
	}
	if meta.DouyinVideo != nil && strings.TrimSpace(meta.DouyinVideo.File) != "" {
		return true
	}
	for _, np := range meta.NotePages {
		if strings.TrimSpace(np.Image) != "" || strings.TrimSpace(np.Video) != "" || strings.TrimSpace(np.Motion) != "" {
			return true
		}
	}
	for _, m := range meta.Media {
		if m.Role == "video" || m.Role == "image" || m.Role == "motion" {
			if strings.TrimSpace(m.Path) != "" {
				return true
			}
		}
	}
	return false
}

func hasSnapshotMedia(meta library.ItemMetadata, relPath string) bool {
	for _, m := range meta.Media {
		if m.Path == relPath {
			return true
		}
	}
	return false
}

func countMediaByRole(files []library.MediaFile, role string) int {
	n := 0
	for _, f := range files {
		if f.Role == role {
			n++
		}
	}
	return n
}

func shortEdgePixels(w, h int) int {
	if w > 0 && h > 0 {
		return min(w, h)
	}
	if w > 0 {
		return w
	}
	return h
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func durationDiffExceeds(localSec, remoteSec int) bool {
	if localSec <= 0 || remoteSec <= 0 {
		return false
	}
	tol := max(2, int(float64(localSec)*0.03+0.5))
	return absInt(localSec-remoteSec) > tol
}

func normComparableText(s string) string {
	return download.NormalizeConflictText(s)
}

func substantiveTextChanged(localText, remoteText string) bool {
	return normComparableText(localText) != normComparableText(remoteText)
}

func metaVideoMediaID(meta library.ItemMetadata) string {
	if meta.DouyinVideo != nil {
		if tok := library.SanitizeMediaToken(meta.DouyinVideo.MediaID); tok != "" {
			return tok
		}
	}
	if len(meta.AssetIDs) == 1 {
		return library.SanitizeMediaToken(meta.AssetIDs[0])
	}
	return ""
}

func normalizeStoredNotePageKind(kind string, isGIF bool) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if isGIF || k == "gif" || k == "animated_gif" {
		return "gif"
	}
	switch k {
	case "live_photo", "motion":
		return "motion"
	case "video":
		return "video"
	case "unknown":
		return "unknown"
	default:
		return "image"
	}
}

func summarizeLocalItem(meta library.ItemMetadata) string {
	switch {
	case meta.Site == "bilibili" && meta.Kind == "video":
		if len(meta.Parts) == 0 {
			return "本地已缓存投稿视频"
		}
		var labels []string
		for i, p := range meta.Parts {
			if i >= 3 {
				labels = append(labels, fmt.Sprintf("等共 %dP", len(meta.Parts)))
				break
			}
			q := firstNonEmpty(p.Video.QualityName, bilibili.QualityName(p.Video.Quality))
			labels = append(labels, fmt.Sprintf("P%02d %s", p.Page, q))
		}
		return fmt.Sprintf("共 %dP（%s）", len(meta.Parts), strings.Join(labels, "、"))

	case meta.DouyinVideo != nil && len(meta.NotePages) == 0:
		q := firstNonEmpty(meta.DouyinVideo.QualityName, meta.DouyinVideo.Gear, "视频")
		if meta.Duration > 0 {
			return fmt.Sprintf("单段视频（%s，时长 %d 秒）", q, meta.Duration)
		}
		return fmt.Sprintf("单段视频（%s）", q)

	case len(meta.NotePages) > 0:
		var imgs, motions, vids int
		for _, pg := range meta.NotePages {
			switch normalizeStoredNotePageKind(pg.Kind, pg.GIF) {
			case "motion":
				motions++
			case "video":
				vids++
			default:
				imgs++
			}
		}
		var segs []string
		if imgs > 0 {
			segs = append(segs, fmt.Sprintf("%d 张图片", imgs))
		}
		if motions > 0 {
			segs = append(segs, fmt.Sprintf("%d 张实况", motions))
		}
		if vids > 0 {
			segs = append(segs, fmt.Sprintf("%d 段视频", vids))
		}
		return fmt.Sprintf("共 %d 项媒体（%s）", len(meta.NotePages), strings.Join(segs, "、"))

	case meta.Site == "bilibili" && meta.Kind == "dynamic":
		imgs := countMediaByRole(meta.Media, "image")
		if imgs > 0 || len(meta.Videos) > 0 {
			return fmt.Sprintf("动态附件（%d 张图片、%d 个关联视频）", imgs, len(meta.Videos))
		}
		return "纯文字动态"
	}
	return "本地已缓存版本"
}

// 1. Douyin works 0-call list inspection.
func (b *Backend) inspectDouyinCachedWork(detail douyin.WorkDetail, meta library.ItemMetadata, queueOrigin, forUID, forName string) cachedInspectDecision {
	dec := b.inspectDouyinCachedWorkDeferred(detail, meta, queueOrigin, forUID, forName)
	if dec.postInspect != nil {
		dec.postInspect()
		dec.postInspect = nil
	}
	return dec
}

func (b *Backend) inspectDouyinCachedWorkDeferred(detail douyin.WorkDetail, meta library.ItemMetadata, queueOrigin, forUID, forName string) cachedInspectDecision {
	if meta.IsArchivedSnapshot() {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if detail.Uncacheable || detail.Kind == douyin.WorkKindUnsupported {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	remoteKind := "video"
	if detail.Kind == douyin.WorkKindNote || len(detail.Pages) > 0 {
		remoteKind = "dynamic"
	}

	var remoteAssets []string
	singleVidDur := 0
	if remoteKind == "video" {
		if mid := douyinDetailMediaID(&detail); mid != "" {
			remoteAssets = []string{mid}
		}
		singleVidDur = detail.DurationSec
		if singleVidDur <= 0 && detail.Video != nil && detail.Video.DurationSec > 0 {
			singleVidDur = detail.Video.DurationSec
		}
	} else {
		for _, pg := range detail.Pages {
			if mid := douyinPageMediaID(pg); mid != "" {
				remoteAssets = append(remoteAssets, mid)
			}
		}
	}
	fp := download.WorkConflictFingerprint(
		"douyin",
		remoteKind,
		detail.ID,
		remoteAssets,
		0,
		len(detail.Pages),
		singleVidDur,
		detail.Desc,
	)
	originTag := firstNonEmpty(queueOrigin, "cache_queue")

	makeConflict := func(reasonKind, remoteSummary string) cachedInspectDecision {
		var via *library.ItemVia
		if meta.Via != nil {
			cp := *meta.Via
			via = &cp
		} else if normFor, err := site.NormalizeUID(site.Douyin, forUID); err == nil && detail.Author.SecUID != "" && normFor != detail.Author.SecUID {
			via = &library.ItemVia{Reason: library.ViaCollab, Site: string(site.Douyin), UID: normFor, Name: strings.TrimSpace(forName)}
		}
		return cachedInspectDecision{
			action: inspectConflict,
			conflict: cacheConflictRecord{
				ItemKey:           meta.ItemKey,
				Site:              "douyin",
				Kind:              meta.Kind,
				TargetID:          detail.ID,
				AuthorUID:         firstNonEmpty(detail.Author.SecUID, itemAuthorUIDString(meta)),
				AuthorName:        firstNonEmpty(detail.Author.Nickname, meta.Author.Name),
				Title:             firstNonEmpty(detail.Title, detail.Desc, meta.Title),
				Origin:            originTag,
				ReasonKind:        reasonKind,
				LocalSummary:      summarizeLocalItem(meta),
				RemoteSummary:     remoteSummary,
				RemoteFingerprint: fp,
				Via:               via,
			},
		}
	}

	var postInspectActions []func()
	appendPostInspect := func(fn func()) {
		if fn != nil {
			postInspectActions = append(postInspectActions, fn)
		}
	}
	withPostInspect := func(dec cachedInspectDecision) cachedInspectDecision {
		if len(postInspectActions) == 1 {
			dec.postInspect = postInspectActions[0]
		} else if len(postInspectActions) > 1 {
			fns := postInspectActions
			dec.postInspect = func() {
				for _, fn := range fns {
					fn()
				}
			}
		}
		return dec
	}

	if hasValidLocalCache(meta) {
		if remoteKind != meta.Kind {
			remoteLabel := "视频作品"
			if remoteKind == "dynamic" {
				remoteLabel = fmt.Sprintf("图文作品（共 %d 页）", len(detail.Pages))
			}
			return makeConflict(ConflictReasonMedia, "源站作品类型已变更为"+remoteLabel)
		}
		if remoteKind == "video" {
			remoteMID := douyinDetailMediaID(&detail)
			localMID := metaVideoMediaID(meta)
			if localMID != "" && remoteMID != "" && localMID != remoteMID {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站视频流标识已替换（时长 %d 秒）", detail.DurationSec))
			}
			if meta.Duration > 0 && detail.DurationSec > 0 && durationDiffExceeds(meta.Duration, detail.DurationSec) {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站视频时长变更为 %d 秒", detail.DurationSec))
			}
			if substantiveTextChanged(meta.Desc, detail.Desc) {
				return makeConflict(ConflictReasonText, "源站作品文案已修改")
			}
		} else {
			if len(meta.NotePages) > 0 && len(detail.Pages) != len(meta.NotePages) {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站图文页数变更为 %d 页", len(detail.Pages)))
			}
			for i := 0; i < len(meta.NotePages) && i < len(detail.Pages); i++ {
				localKind := normalizeStoredNotePageKind(meta.NotePages[i].Kind, meta.NotePages[i].GIF)
				remotePageKind := normalizeStoredNotePageKind(detail.Pages[i].Kind, false)
				remotePageID := douyinPageMediaID(detail.Pages[i])
				if meta.NotePages[i].MediaID != "" && remotePageID != "" && meta.NotePages[i].MediaID != remotePageID {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("第 %02d 页媒体内容已替换", i+1))
				}
				if localKind != remotePageKind && localKind != "unknown" && remotePageKind != "unknown" {
					// Static image gaining LivePhoto motion clip on same URI is safe enrichment;
					// LivePhoto losing motion clip is degradation; video <-> image/motion is conflict.
					if (localKind == "image" && remotePageKind == "motion") || (localKind == "motion" && remotePageKind == "image") {
						continue
					}
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("第 %02d 页媒体类型变更为 %s", i+1, remotePageKind))
				}
			}
			if substantiveTextChanged(meta.Desc, detail.Desc) {
				return makeConflict(ConflictReasonText, "源站作品文案已修改")
			}
		}
		if b.conflicts != nil && b.conflicts.hasPendingTextConflictForItem(meta.ItemKey) {
			itemKeyCopy := meta.ItemKey
			remoteLinks := extractSafeLinksFromDiffText(detail.Desc, "douyin")
			appendPostInspect(func() {
				b.reconcileLiveLegacyTextConflict(itemKeyCopy, remoteLinks)
			})
		}
	}

	// (c) Degraded / quality-downgraded remote -> keep V1 untouched.
	if remoteKind == "video" {
		if detail.Video == nil || len(detail.Video.URLs) == 0 {
			return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
		}
		if meta.DouyinVideo != nil {
			localShort := shortEdgePixels(meta.DouyinVideo.Width, meta.DouyinVideo.Height)
			remoteShort := shortEdgePixels(detail.Video.Width, detail.Video.Height)
			if localShort > 0 && remoteShort > 0 && remoteShort < localShort && len(detail.OriginalURLs) == 0 {
				return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
			}
		}
	} else {
		for i := 0; i < len(meta.NotePages) && i < len(detail.Pages); i++ {
			if meta.NotePages[i].Motion != "" && detail.Pages[i].Kind == douyin.NotePageImage {
				return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
			}
		}
	}

	// (b) Safe non-destructive upgrade / enrichment / completion.
	if library.NeedsCompletion(meta) {
		if !allMissingAreDouyinPermanent(meta.Missing, detail) {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	}
	var dlCfg settings.DownloadSettings
	if b != nil && b.settings != nil {
		dlCfg = b.settings.Get().Download
	}
	if dlCfg.SaveCover && len(detail.CoverURLs) > 0 && !hasMediaRole(meta.Media, "cover") {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}
	if remoteKind == "video" && meta.DouyinVideo != nil {
		opts := b.currentWorkPlanOptions()
		prefCodec := resolveDouyinCodecFromOrder(opts.CodecOrder, b.preferredDouyinCodec())
		isOriginal := strings.EqualFold(meta.DouyinVideo.Gear, "original") || strings.Contains(meta.DouyinVideo.QualityName, "原画")
		origUnavailable := strings.EqualFold(meta.DouyinVideo.OriginalState, "unavailable")
		if parseDouyinQualityCap(opts.VideoQuality) <= 0 && len(detail.OriginalURLs) > 0 && !isOriginal && !origUnavailable {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
		if !isOriginal {
			if sel, _ := selectDouyinVODStream(&detail, opts.VideoQuality, prefCodec); sel != nil {
				localShort := shortEdgePixels(meta.DouyinVideo.Width, meta.DouyinVideo.Height)
				selShort := shortEdgePixels(sel.Width, sel.Height)
				if localShort > 0 && selShort > localShort {
					return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
				}
				if localShort > 0 && selShort == localShort {
					if meta.DouyinVideo.FPS > 0 && sel.FPS > meta.DouyinVideo.FPS {
						return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
					}
					if meta.DouyinVideo.Bitrate > 0 && sel.BitRate > meta.DouyinVideo.Bitrate {
						return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
					}
				}
			}
		}
	}
	if remoteKind == "dynamic" {
		for i := 0; i < len(meta.NotePages) && i < len(detail.Pages); i++ {
			if detail.Pages[i].Kind == douyin.NotePageLivePhoto && strings.TrimSpace(meta.NotePages[i].Motion) == "" {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
			if meta.NotePages[i].UnknownKind != "" && !detail.Pages[i].UnknownKind {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
			if detail.Pages[i].Kind == douyin.NotePageVideo && detail.Pages[i].VideoStream != nil && meta.NotePages[i].VideoInfo != nil {
				vs := detail.Pages[i].VideoStream
				vi := meta.NotePages[i].VideoInfo
				localShort := shortEdgePixels(vi.Width, vi.Height)
				remoteShort := shortEdgePixels(vs.Width, vs.Height)
				if localShort > 0 && remoteShort > localShort {
					return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
				}
				if localShort > 0 && remoteShort == localShort {
					if vi.FPS > 0 && vs.FPS > vi.FPS {
						return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
					}
					if vi.Bitrate > 0 && vs.BitRate > vi.Bitrate {
						return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
					}
				}
			}
		}
		if detail.Music != nil && len(detail.Music.PlayURLs) > 0 && (meta.Music == nil || strings.TrimSpace(meta.Music.File) == "") {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	}

	// Tier 0 lock-free metadata backfill: AssetIDs, MediaID, and DouyinCacheStandardVersion = 3.
	needTier0 := false
	if len(meta.AssetIDs) == 0 && len(remoteAssets) > 0 {
		needTier0 = true
	}
	if remoteKind == "video" && meta.DouyinVideo != nil && meta.DouyinVideo.MediaID == "" && len(remoteAssets) > 0 {
		needTier0 = true
	}
	if remoteKind == "dynamic" {
		for i := 0; i < len(meta.NotePages) && i < len(detail.Pages); i++ {
			if meta.NotePages[i].MediaID == "" && douyinPageMediaID(detail.Pages[i]) != "" {
				needTier0 = true
				break
			}
		}
	}
	if meta.Status == "complete" && len(meta.Missing) == 0 && meta.CacheStandardVersion < library.DouyinCacheStandardVersion {
		needTier0 = true
	}
	if needTier0 && b != nil && b.lib != nil && strings.TrimSpace(meta.ItemKey) != "" {
		itemKeyCopy := meta.ItemKey
		assetsCopy := append([]string(nil), remoteAssets...)
		pageMediaIDs := make([]string, len(detail.Pages))
		for i, pg := range detail.Pages {
			pageMediaIDs[i] = douyinPageMediaID(pg)
		}
		appendPostInspect(func() {
			fresh, ok, gErr := b.lib.GetItem(itemKeyCopy)
			if gErr != nil || !ok || strings.TrimSpace(fresh.ItemDir) == "" {
				return
			}
			changed := false
			if len(fresh.AssetIDs) == 0 && len(assetsCopy) > 0 {
				fresh.AssetIDs = assetsCopy
				changed = true
			}
			if remoteKind == "video" && fresh.DouyinVideo != nil && fresh.DouyinVideo.MediaID == "" && len(assetsCopy) > 0 {
				fresh.DouyinVideo.MediaID = assetsCopy[0]
				changed = true
			}
			if remoteKind == "dynamic" {
				for i := 0; i < len(fresh.NotePages) && i < len(pageMediaIDs); i++ {
					if fresh.NotePages[i].MediaID == "" && pageMediaIDs[i] != "" {
						fresh.NotePages[i].MediaID = pageMediaIDs[i]
						changed = true
					}
				}
			}
			if fresh.Status == "complete" && len(fresh.Missing) == 0 && fresh.CacheStandardVersion < library.DouyinCacheStandardVersion {
				fresh.CacheStandardVersion = library.DouyinCacheStandardVersion
				changed = true
			}
			if !changed {
				return
			}
			if _, sErr := b.lib.SaveItemMetadataOnly(fresh.ItemDir, fresh); sErr == nil && b.events != nil {
				b.events.PublishSite(EventLibraryChanged, string(site.Douyin), itemKeyCopy)
			}
		})
	}

	return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
}

func allMissingAreDouyinPermanent(missing []library.MissingRecord, detail douyin.WorkDetail) bool {
	if len(missing) == 0 {
		return false
	}
	for _, m := range missing {
		itemLower := strings.ToLower(strings.TrimSpace(m.Item))
		isMusic := itemLower == "music" || strings.HasPrefix(itemLower, "music.") ||
			strings.Contains(m.Reason, "配乐") || strings.Contains(m.Reason, "版权")
		if isMusic {
			if detail.Music == nil || len(detail.Music.PlayURLs) == 0 {
				continue
			}
			return false
		}
		isUnknown := (strings.HasPrefix(itemLower, "page_") && strings.HasSuffix(itemLower, ".unknown")) ||
			strings.Contains(m.Reason, "未知") || strings.Contains(m.Reason, "暂不支持")
		if isUnknown {
			stillUnknown := true
			if idx := parsePageMissingIndex(itemLower); idx >= 1 && idx <= len(detail.Pages) {
				stillUnknown = detail.Pages[idx-1].UnknownKind
			} else {
				for _, pg := range detail.Pages {
					if !pg.UnknownKind {
						stillUnknown = false
						break
					}
				}
			}
			if stillUnknown {
				continue
			}
			return false
		}
		return false
	}
	return true
}

func parsePageMissingIndex(itemLower string) int {
	if !strings.HasPrefix(itemLower, "page_") {
		return 0
	}
	rest := strings.TrimPrefix(itemLower, "page_")
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		rest = rest[:dot]
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func hasMediaRole(media []library.MediaFile, role string) bool {
	for _, m := range media {
		if strings.EqualFold(strings.TrimSpace(m.Role), role) && strings.TrimSpace(m.Path) != "" {
			return true
		}
	}
	return false
}

// 2. Weibo posts 0-call list inspection.
func (b *Backend) inspectWeiboCachedPost(post weibo.Post, meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
	dec := b.inspectWeiboCachedPostDeferred(post, meta, queueOrigin)
	if dec.postInspect != nil {
		dec.postInspect()
		dec.postInspect = nil
	}
	return dec
}

// inspectWeiboCachedPostDeferred performs the pure read-only comparison of a
// cached Weibo item while deferring any metadata write / synchronous event
// publication into dec.postInspect so callers holding cacheQueueManager.mu
// never perform disk/SQLite writes or publish events under the queue lock.
func (b *Backend) inspectWeiboCachedPostDeferred(post weibo.Post, meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
	if meta.IsArchivedSnapshot() {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if post.Unavailable {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if post.Retweeted != nil && post.Retweeted.Unavailable && meta.Origin != nil && !meta.Origin.Unavailable {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}

	images, singleVideo := post.EffectiveMedia()
	remoteSingleVid := singleVideo != nil && len(images) == 0
	var remoteAssets []string
	fpKind := "dynamic"
	fpPages := len(images)
	fpVidDur := 0
	if remoteSingleVid {
		fpKind = "video"
		fpPages = 0
		fpVidDur = singleVideo.DurationSec
		if oid := library.SanitizeMediaToken(singleVideo.ObjectID); oid != "" {
			remoteAssets = []string{oid}
		}
	} else {
		for _, img := range images {
			if mid := weiboImageMediaID(img); mid != "" {
				remoteAssets = append(remoteAssets, mid)
			}
		}
	}
	fp := download.WorkConflictFingerprint(
		"weibo",
		fpKind,
		post.Mid,
		remoteAssets,
		max(0, post.EditCount),
		fpPages,
		fpVidDur,
		post.Text,
	)
	originTag := firstNonEmpty(queueOrigin, "cache_queue")

	makeConflict := func(reasonKind, remoteSummary string) cachedInspectDecision {
		var via *library.ItemVia
		if meta.Via != nil {
			cp := *meta.Via
			via = &cp
		}
		return cachedInspectDecision{
			action: inspectConflict,
			conflict: cacheConflictRecord{
				ItemKey:           meta.ItemKey,
				Site:              "weibo",
				Kind:              meta.Kind,
				TargetID:          post.Mid,
				AuthorUID:         firstNonEmpty(post.Author.UID, itemAuthorUIDString(meta)),
				AuthorName:        firstNonEmpty(post.Author.ScreenName, meta.Author.Name),
				Title:             firstNonEmpty(trimQueueSummaryTitle(post.Text), meta.Title, post.Mid),
				Origin:            originTag,
				ReasonKind:        reasonKind,
				LocalSummary:      summarizeLocalItem(meta),
				RemoteSummary:     remoteSummary,
				RemoteFingerprint: fp,
				Via:               via,
			},
		}
	}

	var postInspectActions []func()
	appendPostInspect := func(fn func()) {
		if fn != nil {
			postInspectActions = append(postInspectActions, fn)
		}
	}
	withPostInspect := func(dec cachedInspectDecision) cachedInspectDecision {
		if len(postInspectActions) == 1 {
			dec.postInspect = postInspectActions[0]
		} else if len(postInspectActions) > 1 {
			fns := postInspectActions
			dec.postInspect = func() {
				for _, fn := range fns {
					fn()
				}
			}
		}
		return dec
	}

	localSingleVid := meta.DouyinVideo != nil && len(meta.NotePages) == 0
	localPages := len(meta.NotePages)
	mediaAbbreviated := post.MediaIncomplete || post.OriginIncomplete || (post.Retweeted != nil && post.Retweeted.MediaIncomplete)
	removeWM := b.weiboRemoveWatermarkEnabled()

	if hasValidLocalCache(meta) {
		if !mediaAbbreviated {
			if remoteSingleVid != localSingleVid {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站媒体结构已变更（图片 %d 项，单视频 %v）", len(images), remoteSingleVid))
			}
			if len(images) != localPages {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站媒体数量变更为 %d 项", len(images)))
			}
			if remoteSingleVid {
				remoteOID := library.SanitizeMediaToken(singleVideo.ObjectID)
				localOID := metaVideoMediaID(meta)
				if localOID != "" && remoteOID != "" && localOID != remoteOID {
					return makeConflict(ConflictReasonMedia, "源站微博视频对象已替换")
				}
				if meta.Duration > 0 && singleVideo.DurationSec > 0 && durationDiffExceeds(meta.Duration, singleVideo.DurationSec) {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站微博视频时长变更为 %d 秒", singleVideo.DurationSec))
				}
			}
			for i := 0; i < localPages && i < len(images); i++ {
				remoteID := weiboImageMediaID(images[i])
				if meta.NotePages[i].MediaID != "" && remoteID != "" && meta.NotePages[i].MediaID != remoteID {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("第 %02d 项微博媒体已替换", i+1))
				}
				localIsVid := normalizeStoredNotePageKind(meta.NotePages[i].Kind, meta.NotePages[i].GIF) == "video"
				remoteIsVid := images[i].Kind == weibo.MediaKindVideo
				if localIsVid != remoteIsVid {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("第 %02d 项微博媒体类型已变更", i+1))
				}
			}
		}

		localBody := firstNonEmpty(meta.Text, meta.Desc)
		fullRemoteTextAvailable := !post.IsLongText || post.LongTextFetched
		editAdvanced := post.EditCount > 0 && meta.EditCount > 0 && post.EditCount > meta.EditCount
		if editAdvanced {
			return makeConflict(ConflictReasonText, fmt.Sprintf("源站微博已重新编辑（第 %d 次编辑）", post.EditCount))
		}
		if fullRemoteTextAvailable && substantiveTextChanged(localBody, post.Text) {
			isLongTextExpansion := weiboTextLooksTruncated(localBody, post.Text, true)
			if !isLongTextExpansion && !download.LooksLikeTruncatedTextPrefix(post.Text, localBody) {
				if post.EditCount > 0 {
					return makeConflict(ConflictReasonText, fmt.Sprintf("源站微博正文已编辑（第 %d 次编辑）", post.EditCount))
				}
				return makeConflict(ConflictReasonText, "源站微博正文内容已修改")
			}
		}
		if fullRemoteTextAvailable && !substantiveTextChanged(localBody, post.Text) {
			remoteLinks := MapSiteTextLinks(post.Links)
			hasPendingTextConflict := b.conflicts != nil && b.conflicts.hasPendingTextConflictForItem(meta.ItemKey)
			if cleanedMeta, hadLegacy := cleanLegacyInlineLinksInItem(meta, remoteLinks); hadLegacy {
				itemKeyCopy := meta.ItemKey
				linksCopy := append([]TextLinkDTO(nil), remoteLinks...)
				meta = cleanedMeta
				appendPostInspect(func() {
					if hasPendingTextConflict {
						_, _ = b.reconcileLiveLegacyTextConflict(itemKeyCopy, linksCopy)
						return
					}
					if b.lib == nil {
						return
					}
					fresh, ok, gErr := b.lib.GetItem(itemKeyCopy)
					if gErr != nil || !ok || strings.TrimSpace(fresh.ItemDir) == "" {
						return
					}
					cleaned, stillLegacy := cleanLegacyInlineLinksInItem(fresh, linksCopy)
					if !stillLegacy {
						return
					}
					if weiboCanStampV4Tier0(cleaned, removeWM, post.EditCount) {
						cleaned.CacheStandardVersion = library.WeiboCacheStandardVersion
					}
					if _, sErr := b.lib.SaveItemMetadataOnly(fresh.ItemDir, cleaned); sErr == nil && b.events != nil {
						b.events.PublishSite(EventLibraryChanged, string(site.Weibo), itemKeyCopy)
					}
				})
			}
		}
	}

	// If list payload is abbreviated (MediaIncomplete / OriginIncomplete), never
	// raise false ConflictReasonMedia; route to enqueue (which triggers GetPost +
	// replace.go tripwire) when upgrade/backfill is needed, else skip.
	if mediaAbbreviated {
		needCompletion := library.NeedsCompletion(meta) && !allMissingAreWeiboUnavailable(meta.Missing)
		needNoWM := removeWM && library.WeiboNeedsNoWMBackfill(meta, true)
		needEditHist := post.EditCount > 0 && len(meta.EditHistory) == 0 && meta.CacheStandardVersion < library.WeiboCacheStandardVersion
		needLongText := post.IsLongText && weiboTextLooksTruncated(firstNonEmpty(meta.Text, meta.Desc), post.Text, post.LongTextFetched)
		if needCompletion || needNoWM || needEditHist || needLongText || meta.CacheStandardVersion < library.WeiboCacheStandardVersion {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
		return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
	}

	// (c) Degraded / downgraded checks.
	if remoteSingleVid {
		if singleVideo.Selected == nil || strings.TrimSpace(singleVideo.Selected.URL) == "" {
			return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
		}
		if meta.DouyinVideo != nil {
			localShort := shortEdgePixels(meta.DouyinVideo.Width, meta.DouyinVideo.Height)
			remoteShort := firstPositiveInt(singleVideo.Selected.ShortEdge, shortEdgePixels(singleVideo.Selected.Width, singleVideo.Selected.Height))
			if localShort > 0 && remoteShort > 0 && remoteShort < localShort {
				return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
			}
		}
	}

	// (b) Safe upgrade / completion.
	if library.NeedsCompletion(meta) {
		// Avoid re-downloading when every missing item is a permanently unavailable Sina placeholder GIF and media IDs are unchanged.
		if !allMissingAreWeiboUnavailable(meta.Missing) {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	}
	if remoteSingleVid && singleVideo.Selected != nil && meta.DouyinVideo != nil {
		localShort := shortEdgePixels(meta.DouyinVideo.Width, meta.DouyinVideo.Height)
		remoteShort := firstPositiveInt(singleVideo.Selected.ShortEdge, shortEdgePixels(singleVideo.Selected.Width, singleVideo.Selected.Height))
		if localShort > 0 && remoteShort > localShort {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	}
	for i := 0; i < localPages && i < len(images); i++ {
		if images[i].Kind == weibo.MediaKindLivePhoto && strings.TrimSpace(meta.NotePages[i].Motion) == "" {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
		if images[i].Kind == weibo.MediaKindVideo && images[i].VideoStream != nil && meta.NotePages[i].VideoInfo != nil {
			sel := images[i].VideoStream
			vi := meta.NotePages[i].VideoInfo
			localShort := shortEdgePixels(vi.Width, vi.Height)
			remoteShort := firstPositiveInt(sel.ShortEdge, shortEdgePixels(sel.Width, sel.Height))
			if localShort > 0 && remoteShort > localShort {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
			if localShort > 0 && remoteShort == localShort && vi.Bitrate > 0 && sel.Bitrate > vi.Bitrate {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
		}
	}
	if localBody := strings.TrimSpace(firstNonEmpty(meta.Text, meta.Desc)); localBody != "" {
		if (post.IsLongText && weiboTextLooksTruncated(localBody, post.Text, post.LongTextFetched)) ||
			(!post.IsLongText && weiboTextLooksTruncated(localBody, post.Text, true)) {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	}
	if removeWM && library.WeiboNeedsNoWMBackfill(meta, true) && weiboImagesHaveWatermarkFreeCandidate(images, meta) {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}
	// DEC-094 / DEC-096: Backfill EditHistory once for edited posts below v4.
	if post.EditCount > 0 && len(meta.EditHistory) == 0 && meta.CacheStandardVersion < library.WeiboCacheStandardVersion {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}

	// Tier 0 lock-free metadata backfill: structured Links / Origin.Links, EditCount sync,
	// and uniform WeiboCacheStandardVersion = 4 stamping across text/video/GIF/image posts.
	remoteItemLinks := siteLinksToLibraryLinks(post.Links)
	var remoteOrigLinks []library.ItemLink
	if post.Retweeted != nil && len(post.Retweeted.Links) > 0 {
		remoteOrigLinks = siteLinksToLibraryLinks(post.Retweeted.Links)
	}
	needTier0 := (len(meta.Links) == 0 && len(remoteItemLinks) > 0) ||
		(meta.Origin != nil && len(meta.Origin.Links) == 0 && len(remoteOrigLinks) > 0) ||
		(post.EditCount > 0 && meta.EditCount == 0) ||
		weiboCanStampV4Tier0(meta, removeWM, post.EditCount)
	if needTier0 && b != nil && b.lib != nil && strings.TrimSpace(meta.ItemKey) != "" {
		itemKeyCopy := meta.ItemKey
		editCountCopy := post.EditCount
		appendPostInspect(func() {
			fresh, ok, gErr := b.lib.GetItem(itemKeyCopy)
			if gErr != nil || !ok || strings.TrimSpace(fresh.ItemDir) == "" {
				return
			}
			changed := false
			if len(fresh.Links) == 0 && len(remoteItemLinks) > 0 {
				fresh.Links = append([]library.ItemLink(nil), remoteItemLinks...)
				changed = true
			}
			if fresh.Origin != nil && len(fresh.Origin.Links) == 0 && len(remoteOrigLinks) > 0 {
				fresh.Origin.Links = append([]library.ItemLink(nil), remoteOrigLinks...)
				changed = true
			}
			if editCountCopy > 0 && fresh.EditCount == 0 {
				fresh.EditCount = editCountCopy
				changed = true
			}
			if weiboCanStampV4Tier0(fresh, removeWM, editCountCopy) {
				fresh.CacheStandardVersion = library.WeiboCacheStandardVersion
				changed = true
			}
			if !changed {
				return
			}
			if _, sErr := b.lib.SaveItemMetadataOnly(fresh.ItemDir, fresh); sErr == nil && b.events != nil {
				b.events.PublishSite(EventLibraryChanged, string(site.Weibo), itemKeyCopy)
			}
		})
	}

	return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
}

func weiboCanStampV4Tier0(meta library.ItemMetadata, removeWM bool, remoteEditCount int) bool {
	if meta.Status != "complete" || len(meta.Missing) > 0 || meta.CacheStandardVersion >= library.WeiboCacheStandardVersion {
		return false
	}
	if remoteEditCount > 0 && len(meta.EditHistory) == 0 {
		return false
	}
	if meta.EditCount > 0 && len(meta.EditHistory) == 0 {
		return false
	}
	if len(library.WeiboStaticImageSlotsMissingNoWM(meta)) > 0 {
		if !removeWM {
			// Watermark removal is off: do not stamp v3/v4 on static-image posts so
			// enabling watermark removal later still triggers NoWM backfill.
			return false
		}
		if meta.CacheStandardVersion < library.WeiboNoWMStandardVersion {
			return false
		}
	}
	return true
}

func weiboTextLooksTruncated(localBody, remoteText string, remoteLongFetched bool) bool {
	trimmed := strings.TrimSpace(localBody)
	if trimmed == "" {
		return false
	}
	for _, suf := range []string{
		"...展开", "…展开",
		"...全文", "…全文",
		"展开全文", "查看全文",
	} {
		if strings.HasSuffix(trimmed, suf) {
			return true
		}
	}
	if download.LooksLikeTruncatedTextPrefix(trimmed, remoteText) {
		return true
	}
	if remoteLongFetched && utf8.RuneCountInString(strings.TrimSpace(remoteText)) > utf8.RuneCountInString(trimmed)+10 {
		if strings.HasSuffix(trimmed, "...") || strings.HasSuffix(trimmed, "…") {
			return true
		}
		if download.LooksLikeTruncatedTextPrefix(trimmed, remoteText) {
			return true
		}
	}
	return false
}

func siteLinksToLibraryLinks(in []site.TextLink) []library.ItemLink {
	if len(in) == 0 {
		return nil
	}
	raw := make([]library.ItemLink, 0, len(in))
	for _, l := range in {
		raw = append(raw, library.ItemLink{Label: l.Label, URL: l.URL})
	}
	return library.SanitizeItemLinks(raw)
}

func weiboImagesHaveWatermarkFreeCandidate(images []weibo.ImageAsset, meta library.ItemMetadata) bool {
	if len(images) == 0 {
		return false
	}
	for i, img := range images {
		if img.Kind == weibo.MediaKindGIF || img.Kind == weibo.MediaKindVideo {
			continue
		}
		if strings.TrimSpace(img.WatermarkFreeURL) == "" {
			continue
		}
		if len(meta.NotePages) > 0 {
			if i >= len(meta.NotePages) {
				continue
			}
			pg := meta.NotePages[i]
			if strings.TrimSpace(pg.Image) == "" || pg.GIF || strings.EqualFold(pg.Kind, "video") || strings.EqualFold(pg.Kind, "gif") {
				continue
			}
			if strings.TrimSpace(pg.NoWatermark) == "" {
				return true
			}
		} else {
			return true
		}
	}
	return false
}

func allMissingAreWeiboUnavailable(missing []library.MissingRecord) bool {
	if len(missing) == 0 {
		return false
	}
	for _, m := range missing {
		if !strings.Contains(m.Reason, download.WeiboImageUnavailableReason) {
			return false
		}
	}
	return true
}

// 3. X posts 0-call list inspection.
func (b *Backend) inspectXCachedPost(post x.Post, meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
	dec := b.inspectXCachedPostDeferred(post, meta, queueOrigin)
	if dec.postInspect != nil {
		dec.postInspect()
		dec.postInspect = nil
	}
	return dec
}

func (b *Backend) inspectXCachedPostDeferred(post x.Post, meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
	if meta.IsArchivedSnapshot() {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if post.Unavailable || post.IsPreviewDisplay {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if post.OriginUnavailable && meta.Origin != nil && !meta.Origin.Unavailable {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if post.IsQuote && post.Quoted == nil && !post.OriginUnavailable && meta.Origin != nil {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}

	mediaItems, singleVideo, _ := post.EffectiveMediaWithOrigin()
	var remoteMediaKeys []string
	fpKind := "dynamic"
	fpPages := len(mediaItems)
	fpVidDur := 0
	if singleVideo != nil {
		fpKind = "video"
		fpPages = 0
		fpVidDur = singleVideo.DurationSec
		if mk := library.SanitizeMediaToken(singleVideo.MediaKey); mk != "" {
			remoteMediaKeys = []string{mk}
		}
	} else if len(mediaItems) == 1 && mediaItems[0].Kind == x.MediaKindVideo && !mediaItems[0].GIF {
		fpKind = "video"
		fpPages = 0
		fpVidDur = mediaItems[0].DurationSec
		if mk := library.SanitizeMediaToken(mediaItems[0].MediaKey); mk != "" {
			remoteMediaKeys = []string{mk}
		}
	} else {
		for _, m := range mediaItems {
			if mk := library.SanitizeMediaToken(m.MediaKey); mk != "" {
				remoteMediaKeys = append(remoteMediaKeys, mk)
			}
		}
	}
	xEdits, _ := xEditMetadata(&post)
	fp := download.WorkConflictFingerprint(
		"x",
		fpKind,
		post.ID,
		remoteMediaKeys,
		xEdits,
		fpPages,
		fpVidDur,
		post.Text,
	)
	originTag := firstNonEmpty(queueOrigin, "cache_queue")

	makeConflict := func(reasonKind, remoteSummary string) cachedInspectDecision {
		var via *library.ItemVia
		if meta.Via != nil {
			cp := *meta.Via
			via = &cp
		}
		authorName := firstNonEmpty(formatXDisplayName(post.Author.Name, post.Author.ScreenName), meta.Author.Name)
		return cachedInspectDecision{
			action: inspectConflict,
			conflict: cacheConflictRecord{
				ItemKey:           meta.ItemKey,
				Site:              "x",
				Kind:              meta.Kind,
				TargetID:          post.ID,
				AuthorUID:         firstNonEmpty(post.Author.RestID, itemAuthorUIDString(meta)),
				AuthorName:        authorName,
				Title:             firstNonEmpty(trimQueueSummaryTitle(post.Text), meta.Title, post.ID),
				Origin:            originTag,
				ReasonKind:        reasonKind,
				LocalSummary:      summarizeLocalItem(meta),
				RemoteSummary:     remoteSummary,
				RemoteFingerprint: fp,
				Via:               via,
			},
		}
	}

	var postInspectActions []func()
	appendPostInspect := func(fn func()) {
		if fn != nil {
			postInspectActions = append(postInspectActions, fn)
		}
	}
	withPostInspect := func(dec cachedInspectDecision) cachedInspectDecision {
		if len(postInspectActions) == 1 {
			dec.postInspect = postInspectActions[0]
		} else if len(postInspectActions) > 1 {
			fns := postInspectActions
			dec.postInspect = func() {
				for _, fn := range fns {
					fn()
				}
			}
		}
		return dec
	}

	// DEC-093 / DEC-096: Detect older X article posts stored only as a degraded
	// CardKindArticle stub (no ItemArticle) so they upgrade via TweetDetail
	// without tripping false text/media conflicts.
	localDegradedArticle := meta.Article == nil &&
		((post.Card != nil && post.Card.Kind == x.CardKindArticle) ||
			(meta.Card != nil && strings.EqualFold(strings.TrimSpace(meta.Card.Kind), "article")) ||
			(post.Article != nil && post.Article.HasFullContent()))
	if localDegradedArticle && meta.CacheStandardVersion < library.XCacheStandardVersion {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}

	// Handle multi-tweet threads on UserTweets list where only the focal tweet is present in post.
	unstitchedThreadOnList := strings.TrimSpace(post.SelfThreadID) != "" &&
		len(post.Thread) == 0 && len(post.ThreadAncestors) == 0 && len(meta.Thread) > 1
	localFocalText := firstNonEmpty(meta.Text, meta.Desc)
	localComparePages := meta.NotePages
	if unstitchedThreadOnList {
		for _, tp := range meta.Thread {
			if strings.TrimSpace(tp.ID) == strings.TrimSpace(post.ID) {
				localFocalText = tp.Text
				var sub []library.NotePageRecord
				for _, idx := range tp.MediaIndexes {
					if idx >= 1 && idx <= len(meta.NotePages) {
						sub = append(sub, meta.NotePages[idx-1])
					}
				}
				localComparePages = sub
				break
			}
		}
	}

	remoteSingleVid := (singleVideo != nil || (len(mediaItems) == 1 && mediaItems[0].Kind == x.MediaKindVideo && !mediaItems[0].GIF)) && !unstitchedThreadOnList
	localSingleVid := (meta.DouyinVideo != nil || hasMissingItem(meta.Missing, "video.mp4")) && len(meta.NotePages) == 0
	needNoteTweetUpgrade := false

	if hasValidLocalCache(meta) {
		// Check X edit revision history (edit_tweet_ids).
		if len(post.EditTweetIDs) > 1 {
			remoteEdits := len(post.EditTweetIDs) - 1
			latestID := strings.TrimSpace(post.EditTweetIDs[len(post.EditTweetIDs)-1])
			if (meta.EditCount == 0 && latestID != "" && latestID != strings.TrimSpace(post.ID) && latestID != strings.TrimSpace(meta.RawID)) ||
				(meta.EditCount > 0 && remoteEdits > meta.EditCount) {
				if substantiveTextChanged(localFocalText, post.Text) {
					return makeConflict(ConflictReasonText, fmt.Sprintf("源站帖子已产生新编辑版本（共 %d 次编辑）", remoteEdits))
				}
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站帖子已产生新编辑版本（共 %d 次编辑）", remoteEdits))
			}
		}

		if !unstitchedThreadOnList && remoteSingleVid != localSingleVid {
			return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站帖子媒体结构已变更（当前 %d 项媒体）", len(mediaItems)))
		}
		if remoteSingleVid && singleVideo != nil {
			remoteKey := library.SanitizeMediaToken(singleVideo.MediaKey)
			localKey := metaVideoMediaID(meta)
			if localKey != "" && remoteKey != "" && localKey != remoteKey {
				return makeConflict(ConflictReasonMedia, "源站帖子视频已替换")
			}
			if meta.Duration > 0 && singleVideo.DurationSec > 0 && durationDiffExceeds(meta.Duration, singleVideo.DurationSec) {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站帖子视频时长变更为 %d 秒", singleVideo.DurationSec))
			}
		} else if !remoteSingleVid {
			if len(mediaItems) != len(localComparePages) {
				return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站帖子媒体数量变更为 %d 项", len(mediaItems)))
			}
			for i := 0; i < len(localComparePages) && i < len(mediaItems); i++ {
				remoteKey := library.SanitizeMediaToken(mediaItems[i].MediaKey)
				if localComparePages[i].MediaID != "" && remoteKey != "" && localComparePages[i].MediaID != remoteKey {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("第 %02d 项帖子媒体已替换", i+1))
				}
				localKind := normalizeStoredNotePageKind(localComparePages[i].Kind, localComparePages[i].GIF)
				remoteKind := "image"
				if mediaItems[i].GIF || mediaItems[i].Kind == x.MediaKindGIF {
					remoteKind = "gif"
				} else if mediaItems[i].Kind == x.MediaKindVideo {
					remoteKind = "video"
				}
				if localKind != remoteKind {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("第 %02d 项帖子媒体类型变更为 %s", i+1, remoteKind))
				}
			}
		}

		if substantiveTextChanged(localFocalText, post.Text) {
			// DEC-096: Exempt note_tweet truncated prefix expansion (and truncated
			// timeline previews when local already has full note_tweet text).
			if download.LooksLikeTruncatedTextPrefix(localFocalText, post.Text) {
				needNoteTweetUpgrade = true
			} else if !download.LooksLikeTruncatedTextPrefix(post.Text, localFocalText) {
				return makeConflict(ConflictReasonText, "源站帖子正文已编辑")
			}
		}
		if !unstitchedThreadOnList && !substantiveTextChanged(localFocalText, post.Text) &&
			b.conflicts != nil && b.conflicts.hasPendingTextConflictForItem(meta.ItemKey) {
			itemKeyCopy := meta.ItemKey
			remoteLinks := MapSiteTextLinks(x.BuildPostLinks(&post))
			appendPostInspect(func() {
				b.reconcileLiveLegacyTextConflict(itemKeyCopy, remoteLinks)
			})
		}
	}

	// (c) Degraded / HLS-only / quality downgrade -> keep V1 untouched.
	if remoteSingleVid {
		var vidItem x.MediaItem
		if singleVideo != nil {
			vidItem = *singleVideo
		} else if len(mediaItems) == 1 {
			vidItem = mediaItems[0]
		}
		if (vidItem.HLSOnly || strings.TrimSpace(vidItem.VideoURL) == "") && meta.DouyinVideo != nil && meta.DouyinVideo.File != "" {
			return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
		}
		if meta.DouyinVideo != nil && meta.DouyinVideo.File != "" {
			localShort := shortEdgePixels(meta.DouyinVideo.Width, meta.DouyinVideo.Height)
			remoteShort := shortEdgePixels(vidItem.Width, vidItem.Height)
			if localShort > 0 && remoteShort > 0 && remoteShort < localShort {
				return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
			}
			if localShort == remoteShort && meta.DouyinVideo.Bitrate > 0 && vidItem.VideoBitrate > 0 && vidItem.VideoBitrate < meta.DouyinVideo.Bitrate {
				return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
			}
		}
	}

	// (b) Safe upgrade / completion.
	if needNoteTweetUpgrade {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}
	if library.NeedsCompletion(meta) {
		if !allMissingAreXHLSOnlyWhenStillHLS(meta.Missing, mediaItems) {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	}
	if remoteSingleVid && singleVideo != nil && !singleVideo.HLSOnly && strings.TrimSpace(singleVideo.VideoURL) != "" {
		if meta.DouyinVideo == nil || meta.DouyinVideo.File == "" {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
		localShort := shortEdgePixels(meta.DouyinVideo.Width, meta.DouyinVideo.Height)
		remoteShort := shortEdgePixels(singleVideo.Width, singleVideo.Height)
		if localShort > 0 && remoteShort > localShort {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
		if localShort == remoteShort && meta.DouyinVideo.Bitrate > 0 && singleVideo.VideoBitrate > meta.DouyinVideo.Bitrate {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}
	} else if !remoteSingleVid {
		for i := 0; i < len(localComparePages) && i < len(mediaItems); i++ {
			rm := mediaItems[i]
			if rm.Kind != x.MediaKindVideo || rm.GIF || rm.HLSOnly || strings.TrimSpace(rm.VideoURL) == "" {
				continue
			}
			vi := localComparePages[i].VideoInfo
			if vi == nil {
				continue
			}
			localShort := shortEdgePixels(vi.Width, vi.Height)
			remoteShort := shortEdgePixels(rm.Width, rm.Height)
			if localShort > 0 && remoteShort > localShort {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
			if localShort == remoteShort && vi.Bitrate > 0 && rm.VideoBitrate > vi.Bitrate {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
		}
	}
	if len(post.Thread) > 0 && len(post.Thread)+1 > len(meta.Thread) {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}
	// DEC-096: UserTweets only carries SelfThreadID (without stitched Thread);
	// trigger one TweetDetail lookup when below XCacheStandardVersion so new
	// self-reply segments & ThreadAncestors are backfilled and stamped v3.
	if strings.TrimSpace(post.SelfThreadID) != "" && meta.CacheStandardVersion < library.XCacheStandardVersion {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}

	// DEC-091 / DEC-096 Tier 0 lock-free metadata backfill via postInspect.
	postCopy := post
	if b != nil && b.lib != nil && strings.TrimSpace(meta.ItemKey) != "" && xNeedsTier0MetadataBackfill(meta, &postCopy) {
		itemKeyCopy := meta.ItemKey
		appendPostInspect(func() {
			fresh, ok, gErr := b.lib.GetItem(itemKeyCopy)
			if gErr != nil || !ok || strings.TrimSpace(fresh.ItemDir) == "" {
				return
			}
			if !applyXTier0MetadataBackfill(&fresh, &postCopy) {
				return
			}
			if _, sErr := b.lib.SaveItemMetadataOnly(fresh.ItemDir, fresh); sErr == nil && b.events != nil {
				b.events.PublishSite(EventLibraryChanged, string(site.X), itemKeyCopy)
			}
		})
	}

	return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
}

func buildLibraryReplyTarget(rt *site.ReplyTarget, siteID string) *library.ItemReplyTarget {
	if rt == nil {
		return nil
	}
	return library.SanitizeItemReplyTarget(&library.ItemReplyTarget{
		StatusID:   rt.StatusID,
		UserID:     rt.UserID,
		ScreenName: rt.ScreenName,
	}, siteID)
}

func buildLibraryOriginFromNested(n *site.NestedOriginSummary) *library.ItemOrigin {
	if n == nil {
		return nil
	}
	var card *library.ItemRichCard
	if n.Card != nil {
		card = &library.ItemRichCard{
			Kind:  n.Card.Kind,
			Label: n.Card.Label,
			Title: n.Card.Title,
			Desc:  n.Card.Desc,
			URL:   n.Card.URL,
		}
	}
	return &library.ItemOrigin{
		Site:         string(n.Site),
		ID:           n.ID,
		CanonicalURL: n.CanonicalURL,
		Author: library.ItemAuthor{
			Site:     string(n.Site),
			UID:      n.AuthorUID,
			UniqueID: n.AuthorUniqueID,
			Name:     n.AuthorName,
			Face:     n.AuthorFace,
		},
		PubTs:             n.PubTs,
		Text:              n.Text,
		Links:             siteLinksToLibraryLinks(n.Links),
		Card:              card,
		InReplyTo:         buildLibraryReplyTarget(n.InReplyTo, string(n.Site)),
		Unavailable:       n.Unavailable,
		UnavailableReason: n.UnavailableReason,
	}
}

func buildLibraryOriginFromXPost(post *x.Post) *library.ItemOrigin {
	if post == nil {
		return nil
	}
	var orig *x.Post
	if post.IsRetweet && post.Retweeted != nil {
		orig = post.Retweeted
	} else if post.IsQuote && post.Quoted != nil {
		orig = post.Quoted
	}
	if orig == nil {
		return nil
	}
	var card *library.ItemRichCard
	if orig.Card != nil {
		card = &library.ItemRichCard{
			Kind:  orig.Card.Kind,
			Label: orig.Card.Label,
			Title: orig.Card.Title,
			Desc:  orig.Card.Desc,
			URL:   x.SafePublicWebURL(orig.Card.URL),
		}
	}
	return &library.ItemOrigin{
		Site:         string(site.X),
		ID:           strings.TrimSpace(orig.ID),
		CanonicalURL: x.SafePublicWebURL(orig.CanonicalURL),
		Author: library.ItemAuthor{
			Site:     string(site.X),
			UID:      strings.TrimSpace(orig.Author.RestID),
			UniqueID: strings.TrimSpace(orig.Author.ScreenName),
			Name:     firstNonEmpty(strings.TrimSpace(orig.Author.Name), strings.TrimSpace(orig.Author.ScreenName), strings.TrimSpace(orig.Author.RestID)),
			Face:     x.SafePublicImageURL(orig.Author.AvatarURL),
		},
		PubTs:             orig.CreatedAt,
		Text:              strings.TrimSpace(orig.FullThreadText()),
		Links:             siteLinksToLibraryLinks(x.BuildPostLinks(orig)),
		Card:              card,
		InReplyTo:         buildLibraryReplyTarget(x.BuildPostReplyTarget(orig), string(site.X)),
		Origin:            buildLibraryOriginFromNested(x.BuildNestedOriginSummary(orig)),
		Unavailable:       orig.Unavailable || post.OriginUnavailable,
		UnavailableReason: firstNonEmpty(post.OriginUnavailableReason, orig.UnavailableReason),
	}
}

func xNeedsTier0MetadataBackfill(meta library.ItemMetadata, post *x.Post) bool {
	cp := meta
	if cp.Origin != nil {
		o := *cp.Origin
		cp.Origin = &o
	}
	if len(cp.Thread) > 0 {
		cp.Thread = append([]library.ItemThreadPart(nil), cp.Thread...)
	}
	return applyXTier0MetadataBackfill(&cp, post)
}

func applyXTier0MetadataBackfill(meta *library.ItemMetadata, post *x.Post) bool {
	if meta == nil || post == nil {
		return false
	}
	changed := false
	if meta.InReplyTo == nil {
		if rt := buildLibraryReplyTarget(x.BuildPostReplyTarget(post), string(site.X)); rt != nil {
			meta.InReplyTo = rt
			changed = true
		}
	}
	if len(meta.Links) == 0 {
		if links := siteLinksToLibraryLinks(x.BuildPostLinks(post)); len(links) > 0 {
			meta.Links = links
			changed = true
		}
	}
	if remoteOrigin := buildLibraryOriginFromXPost(post); remoteOrigin != nil {
		if meta.Origin == nil {
			meta.Origin = remoteOrigin
			changed = true
		} else {
			if meta.Origin.InReplyTo == nil && remoteOrigin.InReplyTo != nil {
				meta.Origin.InReplyTo = remoteOrigin.InReplyTo
				changed = true
			}
			if meta.Origin.Origin == nil && remoteOrigin.Origin != nil {
				meta.Origin.Origin = remoteOrigin.Origin
				changed = true
			}
			if len(meta.Origin.Links) == 0 && len(remoteOrigin.Links) > 0 {
				meta.Origin.Links = remoteOrigin.Links
				changed = true
			}
			if meta.Origin.Author.UniqueID == "" && remoteOrigin.Author.UniqueID != "" {
				meta.Origin.Author.UniqueID = remoteOrigin.Author.UniqueID
				changed = true
			}
		}
	}
	if len(meta.Thread) > 0 {
		remoteByID := make(map[string]*x.Post, 1+len(post.Thread)+len(post.ThreadAncestors))
		remoteByID[strings.TrimSpace(post.ID)] = post
		for i := range post.ThreadAncestors {
			tp := &post.ThreadAncestors[i]
			remoteByID[strings.TrimSpace(tp.ID)] = tp
		}
		for i := range post.Thread {
			tp := &post.Thread[i]
			remoteByID[strings.TrimSpace(tp.ID)] = tp
		}
		for i := range meta.Thread {
			tp, ok := remoteByID[strings.TrimSpace(meta.Thread[i].ID)]
			if !ok || tp == nil {
				continue
			}
			if len(meta.Thread[i].Links) == 0 {
				if links := siteLinksToLibraryLinks(x.BuildPostLinks(tp)); len(links) > 0 {
					meta.Thread[i].Links = links
					changed = true
				}
			}
			if meta.Thread[i].Origin == nil && strings.TrimSpace(tp.ID) != strings.TrimSpace(post.ID) {
				if orig := buildLibraryOriginFromXPost(tp); orig != nil {
					meta.Thread[i].Origin = orig
					changed = true
				}
			}
		}
	}
	if normLabel := meta.NormalizeDisplayTypeLabel(); normLabel != "" && normLabel != strings.TrimSpace(meta.TypeLabel) {
		meta.TypeLabel = normLabel
		changed = true
	}
	if meta.Status == "complete" && len(meta.Missing) == 0 && meta.CacheStandardVersion < library.XCacheStandardVersion {
		meta.CacheStandardVersion = library.XCacheStandardVersion
		changed = true
	}
	return changed
}

func hasMissingItem(missing []library.MissingRecord, item string) bool {
	for _, m := range missing {
		if m.Item == item {
			return true
		}
	}
	return false
}

func allMissingAreXHLSOnlyWhenStillHLS(missing []library.MissingRecord, remoteMedia []x.MediaItem) bool {
	if len(missing) == 0 {
		return false
	}
	for _, m := range missing {
		if !strings.Contains(m.Reason, download.XHLSOnlyMissingReason) {
			return false
		}
	}
	for _, rm := range remoteMedia {
		if rm.Kind == x.MediaKindVideo && !rm.GIF && !rm.HLSOnly && strings.TrimSpace(rm.VideoURL) != "" {
			return false
		}
	}
	return true
}

func xPostHasValidDownloadableMedia(post *x.Post) bool {
	if post == nil {
		return false
	}
	items, singleVid, _ := post.EffectiveMediaWithOrigin()
	if singleVid != nil && !singleVid.HLSOnly && strings.TrimSpace(singleVid.VideoURL) != "" {
		return true
	}
	for _, m := range items {
		switch m.Kind {
		case x.MediaKindPhoto:
			if strings.TrimSpace(m.ImageURL) != "" || strings.TrimSpace(m.PublicPreviewURL) != "" {
				return true
			}
		case x.MediaKindGIF:
			if strings.TrimSpace(m.VideoURL) != "" {
				return true
			}
		case x.MediaKindVideo:
			if !m.HLSOnly && strings.TrimSpace(m.VideoURL) != "" {
				return true
			}
		}
	}
	return false
}

// 4. Bilibili dynamics 0-call list inspection.
func (b *Backend) inspectBilibiliCachedDynamic(d bilibili.DynamicItem, meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
	dec := b.inspectBilibiliCachedDynamicDeferred(d, meta, queueOrigin)
	if dec.postInspect != nil {
		dec.postInspect()
		dec.postInspect = nil
	}
	return dec
}

func (b *Backend) inspectBilibiliCachedDynamicDeferred(d bilibili.DynamicItem, meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
	if meta.IsArchivedSnapshot() {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if d.Degraded {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	if hasSnapshotMedia(meta, "orig.json") && d.Origin == nil {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}

	effImgs, effVids := effectiveBiliDynamicMedia(d)
	var remoteImgURLs []string
	var remoteAssets []string
	for _, v := range effVids {
		if tok := library.SanitizeMediaToken(v.BVID); tok != "" {
			remoteAssets = append(remoteAssets, tok)
		}
	}
	for _, img := range effImgs {
		remoteImgURLs = append(remoteImgURLs, img.URL)
		if tok := cleanBiliDynamicImgAssetToken(img.URL); tok != "" {
			remoteAssets = append(remoteAssets, tok)
		}
	}
	var remoteBVIDs []string
	for _, v := range effVids {
		if strings.TrimSpace(v.BVID) != "" {
			remoteBVIDs = append(remoteBVIDs, "video:"+strings.TrimSpace(v.BVID))
		}
	}

	fp := download.BilibiliDynamicConflictFingerprint(d.ID, remoteImgURLs, remoteBVIDs, d.Title, d.Text)
	originTag := firstNonEmpty(queueOrigin, "cache_queue")

	var postInspectActions []func()
	appendPostInspect := func(fn func()) {
		if fn != nil {
			postInspectActions = append(postInspectActions, fn)
		}
	}
	withPostInspect := func(dec cachedInspectDecision) cachedInspectDecision {
		if len(postInspectActions) == 1 {
			dec.postInspect = postInspectActions[0]
		} else if len(postInspectActions) > 1 {
			fns := postInspectActions
			dec.postInspect = func() {
				for _, fn := range fns {
					fn()
				}
			}
		}
		return dec
	}

	var localImgs []library.MediaFile
	for _, m := range meta.Media {
		if m.Role == "image" {
			localImgs = append(localImgs, m)
		}
	}

	if hasValidLocalCache(meta) {
		makeConflict := func(reasonKind, remoteSummary string) cachedInspectDecision {
			return cachedInspectDecision{
				action: inspectConflict,
				conflict: cacheConflictRecord{
					ItemKey:           meta.ItemKey,
					Site:              "bilibili",
					Kind:              "dynamic",
					TargetID:          d.ID,
					AuthorUID:         firstNonEmpty(strconvFormatPosInt64(d.Author.Mid), itemAuthorUIDString(meta)),
					AuthorName:        firstNonEmpty(d.Author.Name, meta.Author.Name),
					Title:             firstNonEmpty(d.Title, trimQueueSummaryTitle(d.Text), meta.Title, d.ID),
					Origin:            originTag,
					ReasonKind:        reasonKind,
					LocalSummary:      summarizeLocalItem(meta),
					RemoteSummary:     remoteSummary,
					RemoteFingerprint: fp,
				},
			}
		}

		if len(effImgs) != len(localImgs) {
			return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站动态图片数量变更为 %d 张", len(effImgs)))
		}
		for i := range localImgs {
			if localImgs[i].Source != "" {
				lp := urlCleanPath(localImgs[i].Source)
				rp := urlCleanPath(effImgs[i].URL)
				if lp != "" && rp != "" && lp != rp {
					return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站动态第 %02d 张图片已替换", i+1))
				}
			}
		}
		if !slices.Equal(meta.Videos, remoteBVIDs) && (len(meta.Videos) > 0 || len(remoteBVIDs) > 0) {
			return makeConflict(ConflictReasonMedia, fmt.Sprintf("源站动态关联视频变更为 %d 个", len(remoteBVIDs)))
		}
		if substantiveTextChanged(meta.Title, d.Title) || substantiveTextChanged(meta.Text, d.Text) {
			return makeConflict(ConflictReasonText, "源站动态标题或正文已修改")
		}
		if b.conflicts != nil && b.conflicts.hasPendingTextConflictForItem(meta.ItemKey) {
			itemKeyCopy := meta.ItemKey
			remoteLinks := extractSafeLinksFromDiffText(d.Text, "bilibili")
			appendPostInspect(func() {
				b.reconcileLiveLegacyTextConflict(itemKeyCopy, remoteLinks)
			})
		}

		// (b) Missing orig.json snapshot on forward dynamic (DEC-096).
		if d.Origin != nil && !hasSnapshotMedia(meta, "orig.json") {
			return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
		}

		// (b) Path-aligned LivePhoto motion clip check (DEC-096).
		localByCleanPath := make(map[string]library.MediaFile, len(localImgs))
		localBySlot := make(map[int]library.MediaFile, len(localImgs))
		for _, mf := range localImgs {
			if cp := urlCleanPath(mf.Source); cp != "" {
				localByCleanPath[cp] = mf
			}
			if slot := parseDynamicImageSlotIndex(mf.Path); slot > 0 {
				localBySlot[slot] = mf
			}
		}
		for i, img := range effImgs {
			if strings.TrimSpace(img.LiveURL) == "" {
				continue
			}
			var matched library.MediaFile
			found := false
			if cp := urlCleanPath(img.URL); cp != "" {
				matched, found = localByCleanPath[cp]
			}
			if !found {
				matched, found = localBySlot[i+1]
			}
			if !found && i < len(localImgs) {
				matched = localImgs[i]
				found = true
			}
			if found && strings.TrimSpace(matched.Motion) == "" {
				return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
			}
		}
	}

	if library.NeedsCompletion(meta) {
		return withPostInspect(cachedInspectDecision{action: inspectEnqueue})
	}

	// (b) Linked video completeness / BilibiliVideoStandardVersion check (DEC-096).
	if len(effVids) > 0 && b != nil && b.lib != nil {
		now := time.Now().UTC()
		if b.queue != nil && b.queue.clock != nil {
			now = b.queue.clock.Now().UTC()
		}
		needLinkedVideos := false
		for _, v := range effVids {
			bvid := strings.TrimSpace(v.BVID)
			if bvid == "" {
				continue
			}
			vidKey := library.CanonicalItemKey("bilibili", "video", bvid)
			vidMeta, exists, gErr := b.lib.GetItem(vidKey)
			if gErr != nil {
				continue
			}
			if !exists {
				if meta.CacheStandardVersion < library.BilibiliDynamicStandardVersion {
					needLinkedVideos = true
					break
				}
				continue
			}
			if library.NeedsCompletion(vidMeta) {
				needLinkedVideos = true
				break
			}
			if vidMeta.CacheStandardVersion < library.BilibiliVideoStandardVersion {
				lastChecked := latestCheckedTime(vidMeta.RecheckedAt, vidMeta.UpgradeCheckedAt)
				if lastChecked.IsZero() || now.Sub(lastChecked) >= bilibiliRecheckCooldown {
					needLinkedVideos = true
					break
				}
			}
		}
		if needLinkedVideos {
			dynCopy := d
			cleanID := strings.TrimSpace(dynCopy.ID)
			itemKey := library.CanonicalItemKey("bilibili", "dynamic", cleanID)
			title := trimQueueSummaryTitle(firstNonEmpty(dynCopy.Title, dynCopy.Text, cleanID))
			return withPostInspect(cachedInspectDecision{
				action: inspectEnqueue,
				enqueue: func(ctx context.Context) (queueEnqueued, error) {
					t, err := b.EnqueueDynamicCache(ctx, download.DynamicEnqueueRequest{
						DynamicID:         dynCopy.ID,
						Item:              &dynCopy,
						CacheLinkedVideos: true,
						Source:            "queue",
					})
					if err != nil {
						return queueEnqueued{}, err
					}
					return queueEnqueued{
						taskIDs:        []string{t.TaskID},
						itemKey:        firstNonEmpty(t.ItemKey, itemKey),
						title:          trimQueueSummaryTitle(firstNonEmpty(t.Title, title, cleanID)),
						reusedListPlan: true,
					}, nil
				},
			})
		}
	}

	// Tier 0 lock-free metadata backfill: legacy inline links, AssetIDs, and
	// BilibiliDynamicStandardVersion = 3.
	_, hasLegacyInline := cleanLegacyInlineLinksInItem(meta, nil)
	needTier0 := hasLegacyInline ||
		(len(meta.AssetIDs) == 0 && len(remoteAssets) > 0) ||
		(meta.Status == "complete" && len(meta.Missing) == 0 && meta.CacheStandardVersion < library.BilibiliDynamicStandardVersion)
	if needTier0 && b != nil && b.lib != nil && strings.TrimSpace(meta.ItemKey) != "" {
		itemKeyCopy := meta.ItemKey
		assetsCopy := append([]string(nil), remoteAssets...)
		remoteLinks := extractSafeLinksFromDiffText(d.Text, "bilibili")
		appendPostInspect(func() {
			fresh, ok, gErr := b.lib.GetItem(itemKeyCopy)
			if gErr != nil || !ok || strings.TrimSpace(fresh.ItemDir) == "" {
				return
			}
			changed := false
			if cleaned, hadLegacy := cleanLegacyInlineLinksInItem(fresh, remoteLinks); hadLegacy {
				fresh = cleaned
				changed = true
			}
			if len(fresh.AssetIDs) == 0 && len(assetsCopy) > 0 {
				fresh.AssetIDs = assetsCopy
				changed = true
			}
			if fresh.Status == "complete" && len(fresh.Missing) == 0 && fresh.CacheStandardVersion < library.BilibiliDynamicStandardVersion {
				fresh.CacheStandardVersion = library.BilibiliDynamicStandardVersion
				changed = true
			}
			if !changed {
				return
			}
			if _, sErr := b.lib.SaveItemMetadataOnly(fresh.ItemDir, fresh); sErr == nil && b.events != nil {
				b.events.PublishSite(EventLibraryChanged, string(site.Bilibili), itemKeyCopy)
			}
		})
	}

	return withPostInspect(cachedInspectDecision{action: inspectSkipNoOp})
}

func cleanBiliDynamicImgAssetToken(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
		base := path.Base(u.Path)
		return library.SanitizeMediaToken(strings.TrimSuffix(base, path.Ext(base)))
	}
	return ""
}

func parseDynamicImageSlotIndex(relPath string) int {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(relPath), `\`, "/"))
	stem := strings.TrimSuffix(base, path.Ext(base))
	if stem == "" {
		return 0
	}
	digits := 0
	for digits < len(stem) && stem[digits] >= '0' && stem[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0
	}
	n, err := strconv.Atoi(stem[:digits])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func effectiveBiliDynamicMedia(d bilibili.DynamicItem) ([]bilibili.DynamicMedia, []bilibili.DynamicVideoRef) {
	imgs := append([]bilibili.DynamicMedia(nil), d.Images...)
	vids := append([]bilibili.DynamicVideoRef(nil), d.Videos...)
	for orig := d.Origin; orig != nil; orig = orig.Origin {
		imgs = append(imgs, orig.Images...)
		vids = append(vids, orig.Videos...)
	}
	seenImg := make(map[string]bool, len(imgs))
	dedupImgs := make([]bilibili.DynamicMedia, 0, len(imgs))
	for _, img := range imgs {
		key := urlCleanPath(img.URL)
		if key == "" {
			key = strings.TrimSpace(img.URL)
		}
		if key != "" && !seenImg[key] {
			seenImg[key] = true
			dedupImgs = append(dedupImgs, img)
		}
	}
	seenVid := make(map[string]bool, len(vids))
	dedupVids := make([]bilibili.DynamicVideoRef, 0, len(vids))
	for _, v := range vids {
		bvid := strings.TrimSpace(v.BVID)
		if bvid != "" && !seenVid[bvid] {
			seenVid[bvid] = true
			dedupVids = append(dedupVids, v)
		}
	}
	return dedupImgs, dedupVids
}

func urlCleanPath(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
		return u.Path
	}
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

func strconvFormatPosInt64(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

func firstPositiveInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// 5. Bilibili videos 3-Gate Filter (Gate A list diff, Gate B standard/ceiling skip, Gate C 7d cooldown + paced probe).
func (b *Backend) inspectBilibiliCachedVideo(v bilibili.UserVideoItem, meta library.ItemMetadata, queueOrigin string, forMid int64) cachedInspectDecision {
	if meta.IsArchivedSnapshot() {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}
	now := time.Now().UTC()
	if b != nil && b.queue != nil && b.queue.clock != nil {
		now = b.queue.clock.Now().UTC()
	}

	probeFn := func(ctx context.Context) (queueEnqueued, error) {
		return b.probeBilibiliCachedVideo(ctx, v, meta, queueOrigin, forMid)
	}

	if library.NeedsCompletion(meta) {
		return cachedInspectDecision{
			action:  inspectProbePaced,
			enqueue: probeFn,
		}
	}

	// Gate A (0-call list diff): Title, Description, or |DurationSec - meta.Duration| > 2 changed -> probe immediately!
	titleChanged := strings.TrimSpace(v.Title) != "" && substantiveTextChanged(meta.Title, v.Title)
	descChanged := strings.TrimSpace(v.Description) != "" && substantiveTextChanged(meta.Desc, v.Description)
	durChanged := v.DurationSec > 0 && meta.Duration > 0 && absInt(v.DurationSec-meta.Duration) > 2
	if titleChanged || descChanged || durChanged {
		return cachedInspectDecision{
			action:  inspectProbePaced,
			enqueue: probeFn,
		}
	}

	// Gate B (site cacheStandardVersion + quality ceiling + extras check): 0 calls!
	var dlCfg settings.DownloadSettings
	if b != nil && b.settings != nil {
		dlCfg = b.settings.Get().Download
	}
	if meta.Status == "complete" &&
		!library.NeedsCompletion(meta) &&
		!meta.UpgradeAvailable &&
		meta.CacheStandardVersion >= library.BilibiliVideoStandardVersion &&
		bilibiliVideoAtCeiling(meta, dlCfg, now) {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}

	// Gate C (7-day RecheckedAt / UpgradeCheckedAt cooldown + cache-queue pacing):
	// Also prevents infinite re-probing of deleted, unavailable, or downgraded videos.
	lastChecked := latestCheckedTime(meta.RecheckedAt, meta.UpgradeCheckedAt)
	if !lastChecked.IsZero() && now.Sub(lastChecked) < bilibiliRecheckCooldown {
		return cachedInspectDecision{action: inspectSkipNoOp}
	}

	return cachedInspectDecision{
		action:  inspectProbePaced,
		enqueue: probeFn,
	}
}

func bilibiliVideoAtCeiling(meta library.ItemMetadata, dlCfg settings.DownloadSettings, now time.Time) bool {
	return bilibiliVideoStreamsAtCeiling(meta, dlCfg, now) && bilibiliVideoExtrasSatisfied(meta, dlCfg)
}

func bilibiliPartStreamAtCeiling(part library.VideoPartRecord, dlCfg settings.DownloadSettings) bool {
	if part.Available.Downgraded {
		return false
	}
	capQn := parseBilibiliQualityCap(dlCfg.VideoQuality)
	effectiveTarget := capQn
	if part.Available.BestVideo > 0 && bilibili.QualityRank(part.Available.BestVideo) < bilibili.QualityRank(effectiveTarget) {
		effectiveTarget = part.Available.BestVideo
	}
	if part.Available.BestVideo > 0 && bilibili.QualityRank(part.Video.Quality) < bilibili.QualityRank(effectiveTarget) {
		return false
	}
	// DEC-096: Never treat 116 (1080P60) as an unconditional ceiling when user
	// configured "best" / 4K / 8K and BestVideo has not been probed.
	atVideoCeiling := bilibili.QualityRank(part.Video.Quality) >= bilibili.QualityRank(capQn) ||
		(part.Available.ProbedAt != "" && part.Available.BestVideo > 0 && bilibili.QualityRank(part.Video.Quality) >= bilibili.QualityRank(effectiveTarget))
	if !atVideoCeiling {
		return false
	}
	// Check audio preferences against probed audio capabilities (Hi-Res 30251 / Dolby 30250).
	if dlCfg.PreferLosslessAudio && part.Available.HasFlac && part.Audio.Quality != 30251 {
		return false
	}
	if dlCfg.PreferDolbyAudio && !dlCfg.PreferLosslessAudio && part.Available.HasDolby && part.Audio.Quality != 30250 {
		return false
	}
	if part.Available.BestAudio > 0 {
		desiredAudio := part.Available.BestAudio
		if desiredAudio == 30251 && !dlCfg.PreferLosslessAudio {
			if dlCfg.PreferDolbyAudio && part.Available.HasDolby {
				desiredAudio = 30250
			} else {
				desiredAudio = 30280
			}
		} else if desiredAudio == 30250 && !dlCfg.PreferDolbyAudio {
			desiredAudio = 30280
		}
		if biliAudioPriority(part.Audio.Quality) < biliAudioPriority(desiredAudio) {
			return false
		}
	}
	return true
}

func bilibiliVideoStreamsAtCeiling(meta library.ItemMetadata, dlCfg settings.DownloadSettings, now time.Time) bool {
	if len(meta.Parts) == 0 {
		return false
	}
	// Uploads newer than 14 days may still receive delayed 4K transcodes or AI subtitles.
	if meta.PubTs > 0 {
		pubTime := time.Unix(meta.PubTs, 0).UTC()
		if now.After(pubTime) && now.Sub(pubTime) < bilibiliRecentUploadAge {
			return false
		}
	}
	for _, part := range meta.Parts {
		if !bilibiliPartStreamAtCeiling(part, dlCfg) {
			return false
		}
	}
	return true
}

func bilibiliVideoExtrasSatisfied(meta library.ItemMetadata, dlCfg settings.DownloadSettings) bool {
	if len(meta.Parts) == 0 {
		return false
	}
	if dlCfg.SaveCover && !hasMediaRole(meta.Media, "cover") {
		return false
	}
	if dlCfg.MultiPart != "first" && meta.RemotePartCount > 0 && len(meta.Parts) < meta.RemotePartCount {
		return false
	}
	for _, part := range meta.Parts {
		if dlCfg.SaveDanmaku && (strings.TrimSpace(part.Danmaku) == "" || part.DanmakuState != "saved") {
			return false
		}
		if dlCfg.SaveSubtitles {
			if part.SubtitleState != "saved" {
				return false
			}
			if dlCfg.IncludeAI && !part.SubtitleIncludesAI {
				return false
			}
		}
	}
	return true
}

func latestCheckedTime(vals ...string) time.Time {
	var best time.Time
	for _, raw := range vals {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
			if best.IsZero() || t.After(best) {
				best = t
			}
		}
	}
	return best
}

func biliAudioPriority(id int) int {
	switch id {
	case 30251:
		return 50
	case 30250:
		return 40
	case 30280:
		return 30
	case 30232:
		return 20
	case 30216:
		return 10
	default:
		return 0
	}
}

func (b *Backend) probeBilibiliCachedVideo(
	ctx context.Context,
	v bilibili.UserVideoItem,
	meta library.ItemMetadata,
	queueOrigin string,
	forMid int64,
) (queueEnqueued, error) {
	bvid := strings.TrimSpace(firstNonEmpty(v.BVID, meta.RawID))
	itemKey := firstNonEmpty(meta.ItemKey, library.CanonicalItemKey("bilibili", "video", bvid))
	probeCtx := site.WithAutomaticRequests(ctx, "cache_queue")

	now := time.Now().UTC()
	if b.queue != nil && b.queue.clock != nil {
		now = b.queue.clock.Now().UTC()
	}
	nowStr := now.Format(time.RFC3339)

	// Record RecheckedAt (without touching Status, media, or CacheStandardVersion)
	// when remote is deleted/unavailable/forbidden/downgraded so Gate C 7-day
	// cooldown prevents infinite re-probing loops (DEC-096).
	stampRecheckedOnly := func() {
		if b == nil || b.lib == nil {
			return
		}
		fresh, ok, gErr := b.lib.GetItem(itemKey)
		if gErr != nil || !ok || strings.TrimSpace(fresh.ItemDir) == "" {
			return
		}
		fresh.RecheckedAt = nowStr
		fresh.UpgradeCheckedAt = nowStr
		_, _ = b.lib.SaveItemMetadataOnly(fresh.ItemDir, fresh)
	}

	vd, err := b.bili.VideoDetail(probeCtx, bvid)
	if err != nil {
		if bilibili.IsKind(err, bilibili.ErrKindUnavailable) ||
			bilibili.IsKind(err, bilibili.ErrKindForbidden) ||
			site.IsKind(err, site.ErrKindUnavailable) ||
			site.IsKind(err, site.ErrKindForbidden) ||
			site.IsKind(err, site.ErrKindBlocked) {
			// (c) Remote deleted / unavailable -> preserve local V1 untouched & arm Gate C cooldown!
			stampRecheckedOnly()
			return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
		}
		return queueEnqueued{}, err
	}
	if len(vd.Pages) == 0 || vd.IsUpowerLocked() {
		stampRecheckedOnly()
		return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
	}

	// Build structural fingerprint and compare cached parts' CIDs & ordering.
	remoteByPage := make(map[int]bilibili.VideoPage, len(vd.Pages))
	remoteCIDPage := make(map[int64]int, len(vd.Pages))
	for _, pg := range vd.Pages {
		remoteByPage[pg.Page] = pg
		if pg.CID > 0 {
			remoteCIDPage[pg.CID] = pg.Page
		}
	}
	fp := download.BilibiliVideoConflictFingerprint(bvid, vd.Pages, vd.Title, vd.Desc)

	var conflictSummary string
	cachedPages := make(map[int]bool, len(meta.Parts))
	for _, lp := range meta.Parts {
		if lp.Page > 0 && strings.TrimSpace(lp.File) != "" {
			cachedPages[lp.Page] = true
		}
		if lp.CID <= 0 {
			continue
		}
		rp, ok := remoteByPage[lp.Page]
		if !ok {
			conflictSummary = fmt.Sprintf("源站分P已删减（当前共 %dP，缺少原 P%02d）", len(vd.Pages), lp.Page)
			break
		}
		if rp.CID != lp.CID {
			if movedPage, existsElsewhere := remoteCIDPage[lp.CID]; existsElsewhere {
				conflictSummary = fmt.Sprintf("源站分P顺序已调整（原 P%02d 移至 P%02d，当前共 %dP）", lp.Page, movedPage, len(vd.Pages))
			} else {
				conflictSummary = fmt.Sprintf("源站 P%02d 视频源已更换（CID 已变更，当前共 %dP）", lp.Page, len(vd.Pages))
			}
			break
		}
		if lp.Duration > 0 && rp.Duration > 0 && durationDiffExceeds(lp.Duration, rp.Duration) {
			conflictSummary = fmt.Sprintf("源站 P%02d 时长变更为 %d 秒（原 %d 秒）", lp.Page, rp.Duration, lp.Duration)
			break
		}
	}

	if conflictSummary != "" {
		// (d) Changed / removed / reordered CID -> record conflict in cache-conflicts.json without touching V1!
		var via *library.ItemVia
		if meta.Via != nil {
			cp := *meta.Via
			via = &cp
		}
		rec := cacheConflictRecord{
			ItemKey:           itemKey,
			Site:              "bilibili",
			Kind:              "video",
			TargetID:          bvid,
			AuthorUID:         firstNonEmpty(strconvFormatPosInt64(vd.Author.Mid), itemAuthorUIDString(meta)),
			AuthorName:        firstNonEmpty(vd.Author.Name, meta.Author.Name),
			Title:             firstNonEmpty(vd.Title, meta.Title, bvid),
			Origin:            firstNonEmpty(queueOrigin, "cache_queue"),
			ReasonKind:        ConflictReasonParts,
			LocalSummary:      summarizeLocalItem(meta),
			RemoteSummary:     conflictSummary,
			RemoteFingerprint: fp,
			Via:               via,
		}
		if b.conflicts != nil {
			if saved, changed, cErr := b.conflicts.recordOrUpdate(rec); cErr == nil && changed && b.events != nil {
				b.events.PublishSite(EventCacheConflictsChanged, saved.Site, saved.ItemKey)
			}
		}
		return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
	}

	// Detect missing pages (including missing middle pages and MultiPart: first -> all)
	// or incomplete items while keeping existing P1..PN CIDs intact (DEC-096).
	dlCfg := b.GetSettings().Download
	var missingPages []int
	for _, pg := range vd.Pages {
		if dlCfg.MultiPart == "first" && pg.Page != 1 {
			continue
		}
		if !cachedPages[pg.Page] {
			missingPages = append(missingPages, pg.Page)
		}
	}
	if library.NeedsCompletion(meta) || len(missingPages) > 0 {
		var pagesToFetch []int
		if library.NeedsCompletion(meta) {
			for _, pg := range vd.Pages {
				if dlCfg.MultiPart == "first" && pg.Page != 1 {
					continue
				}
				pagesToFetch = append(pagesToFetch, pg.Page)
			}
		} else {
			pagesToFetch = missingPages
		}
		if len(pagesToFetch) > 0 {
			detailCopy := vd
			tasks, enqErr := b.EnqueueVideoCache(probeCtx, download.VideoEnqueueRequest{
				BVID:   bvid,
				Detail: &detailCopy,
				Pages:  []int{pagesToFetch[0]},
				Source: "queue",
				ForMid: forMid,
			})
			if enqErr != nil {
				return queueEnqueued{}, enqErr
			}
			var ids []string
			for _, t := range tasks {
				ids = append(ids, t.TaskID)
			}
			var nextParts []queuePartEnqueuer
			var pendingPages []int
			for _, pNum := range pagesToFetch[1:] {
				pendingPages = append(pendingPages, pNum)
				nextParts = append(nextParts, b.makeBilibiliPartEnqueuer(bvid, &detailCopy, pNum, forMid))
			}
			return queueEnqueued{
				taskIDs:      ids,
				itemKey:      itemKey,
				title:        trimQueueSummaryTitle(firstNonEmpty(vd.Title, v.Title, bvid)),
				nextParts:    nextParts,
				pendingPages: pendingPages,
			}, nil
		}
	}

	// Refresh local metadata from latest GetItem before evaluating streams/extras.
	freshMeta, exists, gErr := b.lib.GetItem(itemKey)
	if gErr != nil || !exists {
		freshMeta = meta
	}
	if len(freshMeta.Parts) == 0 {
		stampRecheckedOnly()
		return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
	}

	// Evaluate missing extras across ALL cached parts (P1..PN, DEC-096).
	needChapters := freshMeta.CacheStandardVersion < library.BilibiliVideoStandardVersion
	needDanmaku := false
	needSubtitles := false
	for _, lp := range freshMeta.Parts {
		if lp.CID <= 0 {
			continue
		}
		if dlCfg.SaveDanmaku && (strings.TrimSpace(lp.Danmaku) == "" || lp.DanmakuState != "saved") {
			needDanmaku = true
		}
		if dlCfg.SaveSubtitles && (lp.SubtitleState != "saved" || (dlCfg.IncludeAI && !lp.SubtitleIncludesAI)) {
			needSubtitles = true
		}
	}
	needCover := dlCfg.SaveCover && strings.TrimSpace(vd.Cover) != "" && !hasMediaRole(freshMeta.Media, "cover")
	needExtras := needChapters || needDanmaku || needSubtitles

	// DEC-096 Request-saving optimization: when all cached parts' video/audio
	// streams are already at ceiling, skip PlayURL completely (0 PlayURL calls)
	// and pass pre-fetched VideoDetail directly to CacheSupplementWithDetail!
	if bilibiliVideoStreamsAtCeiling(freshMeta, dlCfg, now) {
		freshMeta.RemotePartCount = len(vd.Pages)
		freshMeta.Title = firstNonEmpty(vd.Title, freshMeta.Title)
		freshMeta.Desc = firstNonEmpty(vd.Desc, freshMeta.Desc)
		if needExtras {
			// Persist RemotePartCount/header WITHOUT prematurely stamping CacheStandardVersion!
			if freshMeta.ItemDir != "" {
				_, _ = b.lib.SaveItemMetadataOnly(freshMeta.ItemDir, freshMeta)
			}
			detailCopy := vd
			t, sErr := b.dl.CacheSupplementWithDetail(probeCtx, freshMeta.RawID, "queue", &detailCopy)
			if sErr != nil {
				return queueEnqueued{}, sErr
			}
			b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:supplement:"+freshMeta.RawID)
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: itemKey,
				title:   trimQueueSummaryTitle(firstNonEmpty(vd.Title, freshMeta.Title, bvid)),
			}, nil
		}
		if needCover {
			detailCopy := vd
			tasks, enqErr := b.EnqueueVideoCache(probeCtx, download.VideoEnqueueRequest{
				BVID:   bvid,
				Detail: &detailCopy,
				Pages:  []int{freshMeta.Parts[0].Page},
				Source: "queue",
				ForMid: forMid,
			})
			if enqErr != nil {
				return queueEnqueued{}, enqErr
			}
			var ids []string
			for _, t := range tasks {
				ids = append(ids, t.TaskID)
			}
			return queueEnqueued{
				taskIDs: ids,
				itemKey: itemKey,
				title:   trimQueueSummaryTitle(firstNonEmpty(vd.Title, freshMeta.Title, bvid)),
			}, nil
		}
		freshMeta.UpgradeCheckedAt = nowStr
		freshMeta.RecheckedAt = nowStr
		freshMeta.UpgradeAvailable = false
		freshMeta.UpgradeHint = ""
		if freshMeta.ItemDir != "" {
			_, _ = b.lib.SaveItemMetadataOnly(freshMeta.ItemDir, freshMeta)
		}
		return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
	}

	// Probe PlayURL across all cached parts not yet at stream ceiling (P1..PN).
	playOpts := b.bilibiliPlaySelectionOpts()
	anyBetter := false
	anyDowngraded := false
	probedAny := false
	for idx := range freshMeta.Parts {
		lp := freshMeta.Parts[idx]
		if lp.CID <= 0 {
			continue
		}
		if idx > 0 && bilibiliPartStreamAtCeiling(lp, dlCfg) {
			continue
		}
		probedAny = true
		playPlan, pErr := b.bili.PlayURL(probeCtx, bvid, lp.CID, playOpts)
		if pErr != nil {
			if bilibili.IsKind(pErr, bilibili.ErrKindUnavailable) ||
				bilibili.IsKind(pErr, bilibili.ErrKindForbidden) ||
				site.IsKind(pErr, site.ErrKindUnavailable) ||
				site.IsKind(pErr, site.ErrKindForbidden) ||
				site.IsKind(pErr, site.ErrKindBlocked) {
				// (c) Preview-only / restricted -> preserve V1 untouched & arm Gate C cooldown!
				stampRecheckedOnly()
				return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
			}
			return queueEnqueued{}, pErr
		}
		videoBetter := bilibili.QualityRank(playPlan.Video.Quality) > bilibili.QualityRank(lp.Video.Quality)
		audioBetter := playPlan.Audio != nil && biliAudioPriority(playPlan.Audio.Quality) > biliAudioPriority(lp.Audio.Quality)
		videoDowngraded := bilibili.QualityRank(playPlan.Video.Quality) < bilibili.QualityRank(lp.Video.Quality)
		audioDowngraded := playPlan.Audio != nil && lp.Audio.Quality > 0 && biliAudioPriority(playPlan.Audio.Quality) < biliAudioPriority(lp.Audio.Quality)

		if (videoDowngraded && !audioBetter) || (audioDowngraded && !videoBetter) {
			anyDowngraded = true
			continue
		}
		freshMeta.Parts[idx].Available = library.PartAvailableInfo{
			AcceptQuality: playPlan.Available.AcceptQuality,
			Formats:       playPlan.Available.Formats,
			Returned:      playPlan.Available.Returned,
			BestVideo:     playPlan.Available.BestVideo,
			BestAudio:     playPlan.Available.BestAudio,
			HasDolby:      playPlan.Available.HasDolby,
			HasFlac:       playPlan.Available.HasFlac,
			ProbedAt:      nowStr,
			Downgraded:    playPlan.Available.Downgraded,
			DowngradeHint: playPlan.Available.DowngradeHint,
		}
		if videoBetter || audioBetter {
			anyBetter = true
			break
		}
	}

	if !probedAny || (anyDowngraded && !anyBetter) {
		// (c) Remote stream downgraded (e.g. lost VIP) -> preserve V1 untouched & arm Gate C cooldown!
		stampRecheckedOnly()
		return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
	}

	freshMeta.RemotePartCount = len(vd.Pages)

	// (b) Same-CID higher quality stream available -> enqueue upgrade!
	if anyBetter {
		freshMeta.UpgradeCheckedAt = nowStr
		freshMeta.RecheckedAt = nowStr
		freshMeta.UpgradeAvailable = true
		if freshMeta.ItemDir != "" {
			_, _ = b.lib.SaveItem(freshMeta.ItemDir, freshMeta)
		}
		t, uErr := b.dl.UpgradeWithSource(probeCtx, itemKey, "queue")
		if uErr != nil {
			return queueEnqueued{}, uErr
		}
		b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:upgrade:"+itemKey)
		return queueEnqueued{
			taskIDs: []string{t.TaskID},
			itemKey: itemKey,
			title:   trimQueueSummaryTitle(firstNonEmpty(vd.Title, freshMeta.Title, bvid)),
		}, nil
	}

	// Check missing chapters / danmaku / subtitles across P1..PN WITHOUT prematurely
	// stamping CacheStandardVersion before CacheSupplement finishes (DEC-096).
	if needExtras {
		freshMeta.Title = firstNonEmpty(vd.Title, freshMeta.Title)
		freshMeta.Desc = firstNonEmpty(vd.Desc, freshMeta.Desc)
		freshMeta.UpgradeCheckedAt = nowStr
		freshMeta.UpgradeAvailable = false
		freshMeta.UpgradeHint = ""
		if freshMeta.ItemDir != "" {
			_, _ = b.lib.SaveItem(freshMeta.ItemDir, freshMeta)
		}
		detailCopy := vd
		t, sErr := b.dl.CacheSupplementWithDetail(probeCtx, freshMeta.RawID, "queue", &detailCopy)
		if sErr != nil {
			return queueEnqueued{}, sErr
		}
		b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:supplement:"+freshMeta.RawID)
		return queueEnqueued{
			taskIDs: []string{t.TaskID},
			itemKey: itemKey,
			title:   trimQueueSummaryTitle(firstNonEmpty(vd.Title, freshMeta.Title, bvid)),
		}, nil
	}

	if needCover {
		detailCopy := vd
		tasks, enqErr := b.EnqueueVideoCache(probeCtx, download.VideoEnqueueRequest{
			BVID:   bvid,
			Detail: &detailCopy,
			Pages:  []int{freshMeta.Parts[0].Page},
			Source: "queue",
			ForMid: forMid,
		})
		if enqErr != nil {
			return queueEnqueued{}, enqErr
		}
		var ids []string
		for _, t := range tasks {
			ids = append(ids, t.TaskID)
		}
		return queueEnqueued{
			taskIDs: ids,
			itemKey: itemKey,
			title:   trimQueueSummaryTitle(firstNonEmpty(vd.Title, freshMeta.Title, bvid)),
		}, nil
	}

	// (a) No-op (already at v3 and streams probed): stamp RecheckedAt so subsequent sweeps skip with 0 calls!
	freshMeta.Title = firstNonEmpty(vd.Title, freshMeta.Title)
	freshMeta.Desc = firstNonEmpty(vd.Desc, freshMeta.Desc)
	freshMeta.UpgradeCheckedAt = nowStr
	freshMeta.RecheckedAt = nowStr
	freshMeta.UpgradeAvailable = false
	freshMeta.UpgradeHint = ""
	if freshMeta.CacheStandardVersion < library.BilibiliVideoStandardVersion {
		freshMeta.CacheStandardVersion = library.BilibiliVideoStandardVersion
	}
	if freshMeta.ItemDir != "" {
		_, _ = b.lib.SaveItem(freshMeta.ItemDir, freshMeta)
	}
	return queueEnqueued{itemKey: itemKey, probedNoTask: true}, nil
}
