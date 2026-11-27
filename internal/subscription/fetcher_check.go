package subscription

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func (m *Manager) checkAccountBySite(
	ctx context.Context,
	sub *Subscription,
	acc AccountRef,
	accState *AccountState,
	isBaseline bool,
	globalAutoCache string,
) (accountCheckOutcome, error) {
	siteName := normalizeSchedulerSite(acc.Site)
	if siteName == "" {
		return accountCheckOutcome{}, &site.Error{
			Site:    site.ID(strings.TrimSpace(acc.Site)),
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点",
		}
	}
	acc.Site = siteName

	m.mu.Lock()
	fetcher := m.fetchers[site.ID(siteName)]
	biliClient := m.client
	m.mu.Unlock()

	if siteName == string(site.Bilibili) && biliClient != nil {
		newVids, newDyns, autoTasks, gap, err := m.checkSingleAccount(ctx, sub, acc, accState, isBaseline, globalAutoCache)
		return accountCheckOutcome{
			newVideos:      newVids,
			newDynamics:    newDyns,
			autoCacheTasks: autoTasks,
			gapDetected:    gap,
		}, err
	}

	if fetcher == nil {
		return accountCheckOutcome{}, &site.Error{
			Site:    site.ID(siteName),
			Kind:    site.ErrKindBadRequest,
			Message: "未注册该站点的订阅抓取器",
		}
	}
	return m.checkFetcherAccount(ctx, fetcher, sub, acc, accState, isBaseline)
}

func formatIncompleteNote(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "本轮结果不完整"
	}
	if strings.HasPrefix(trimmed, "本轮结果不完整") {
		return trimmed
	}
	return "本轮结果不完整：" + trimmed
}

type pendingUpdateRow struct {
	dynID    string
	pubTs    int64
	typ      string
	snapshot string
}

// watermarkTracker shares pinned-aware baseline & incremental watermark
// tracking across both Bilibili and cross-site fetchers (INV-12).
type watermarkTracker struct {
	siteID          site.ID
	bestNonPinnedID string
	bestNonPinnedTs int64
	bestPinnedID    string
	bestPinnedTs    int64
	baselinePinned  []string
}

func compareUint64ID(a, b string) (int, bool) {
	av, errA := strconv.ParseUint(strings.TrimSpace(a), 10, 64)
	bv, errB := strconv.ParseUint(strings.TrimSpace(b), 10, 64)
	if errA != nil || errB != nil {
		return 0, false
	}
	switch {
	case av < bv:
		return -1, true
	case av > bv:
		return 1, true
	default:
		return 0, true
	}
}

func isNewerWatermarkCandidate(siteID site.ID, candID string, candTs int64, curID string, curTs int64) bool {
	if siteID == site.X {
		if strings.TrimSpace(candID) == "" {
			return false
		}
		if strings.TrimSpace(curID) == "" {
			return true
		}
		if cmp, ok := compareUint64ID(candID, curID); ok {
			return cmp > 0
		}
		return compareNumericID(candID, curID) > 0
	}
	return candTs > curTs || (candTs == curTs && compareNumericID(candID, curID) > 0)
}

func (w *watermarkTracker) observe(id string, pubTs int64, isPinned, isBaseline bool) {
	if isPinned {
		if isBaseline {
			w.baselinePinned = append(w.baselinePinned, id)
		}
		if isNewerWatermarkCandidate(w.siteID, id, pubTs, w.bestPinnedID, w.bestPinnedTs) {
			w.bestPinnedTs = pubTs
			w.bestPinnedID = id
		}
		return
	}
	if isNewerWatermarkCandidate(w.siteID, id, pubTs, w.bestNonPinnedID, w.bestNonPinnedTs) {
		w.bestNonPinnedTs = pubTs
		w.bestNonPinnedID = id
	}
}

func (w *watermarkTracker) apply(accState *AccountState, isBaseline bool) (updatedTs int64, updatedID string) {
	if w.siteID == site.X {
		if isBaseline {
			if w.bestNonPinnedID != "" {
				accState.NewestTs = w.bestNonPinnedTs
				accState.NewestID = w.bestNonPinnedID
				return w.bestNonPinnedTs, w.bestNonPinnedID
			}
			if w.bestPinnedID != "" {
				accState.NewestTs = w.bestPinnedTs
				accState.NewestID = w.bestPinnedID
				return w.bestPinnedTs, w.bestPinnedID
			}
			return 0, ""
		}
		if w.bestNonPinnedID != "" && isNewerWatermarkCandidate(site.X, w.bestNonPinnedID, w.bestNonPinnedTs, accState.NewestID, accState.NewestTs) {
			accState.NewestTs = w.bestNonPinnedTs
			accState.NewestID = w.bestNonPinnedID
			return w.bestNonPinnedTs, w.bestNonPinnedID
		}
		return 0, ""
	}
	if isBaseline {
		if w.bestNonPinnedTs > 0 {
			accState.NewestTs = w.bestNonPinnedTs
			accState.NewestID = w.bestNonPinnedID
			return w.bestNonPinnedTs, w.bestNonPinnedID
		}
		if w.bestPinnedTs > 0 {
			accState.NewestTs = w.bestPinnedTs
			accState.NewestID = w.bestPinnedID
			return w.bestPinnedTs, w.bestPinnedID
		}
		return 0, ""
	}
	if w.bestNonPinnedTs > accState.NewestTs || (w.bestNonPinnedTs == accState.NewestTs && w.bestNonPinnedTs > 0 && compareNumericID(w.bestNonPinnedID, accState.NewestID) > 0) {
		accState.NewestTs = w.bestNonPinnedTs
		accState.NewestID = w.bestNonPinnedID
		return w.bestNonPinnedTs, w.bestNonPinnedID
	}
	return 0, ""
}

func (m *Manager) checkSingleAccount(
	ctx context.Context,
	sub *Subscription,
	acc AccountRef,
	accState *AccountState,
	isBaseline bool,
	globalAutoCache string,
) (newVideos int, newDynamics int, autoCacheTasks int, gapDetected bool, err error) {
	if acc.Site != "" && acc.Site != "bilibili" {
		return 0, 0, 0, false, nil
	}
	mid, _ := strconv.ParseInt(acc.UID, 10, 64)
	if mid <= 0 {
		mid = sub.Mid
	}

	checkVideos := sub.Scope == "video"
	checkDynamics := sub.Scope == "dynamic" || sub.Scope == "both" || sub.Scope == ""
	nowStr := m.nowFunc().UTC().Format(time.RFC3339)

	if checkVideos {
		vpage, vErr := m.client.UserVideos(ctx, mid, 1, 20, "pubdate", "")
		if vErr != nil {
			// If Scope=="both" and the test server only mocks UserDynamics, allow UserDynamics to proceed on 404.
			if sub.Scope == "video" || bilibili.IsKind(vErr, bilibili.ErrKindRiskControl) || bilibili.IsKind(vErr, bilibili.ErrKindRateLimited) || bilibili.IsKind(vErr, bilibili.ErrKindAuthRequired) || bilibili.IsKind(vErr, bilibili.ErrKindNetwork) {
				return 0, 0, 0, false, vErr
			}
			if !checkDynamics {
				return 0, 0, 0, false, vErr
			}
		} else {
			maxPubTs := sub.LastVideoPubTs
			maxBVID := sub.LastVideoBVID

			for _, item := range vpage.Items {
				updateAccountProfile(sub, acc, item.Author, "")
				isNew := item.PubTs > sub.LastVideoPubTs || (item.PubTs == sub.LastVideoPubTs && item.BVID != "" && item.BVID != sub.LastVideoBVID && sub.LastVideoPubTs > 0)
				if item.PubTs > maxPubTs {
					maxPubTs = item.PubTs
					maxBVID = item.BVID
				}

				if m.lib != nil {
					if err := m.lib.UpsertFeedEntry(library.FeedEntry{
						Site:         "bilibili",
						Kind:         "video",
						RawID:        item.BVID,
						Mid:          mid,
						AuthorName:   firstNonEmptyStr(item.Author, sub.Uname),
						AuthorFace:   sub.Face,
						Title:        item.Title,
						Summary:      item.Description,
						CoverURL:     item.Cover,
						CanonicalURL: "https://www.bilibili.com/video/" + item.BVID,
						PubTs:        item.PubTs,
					}); err != nil {
						return newVideos, newDynamics, 0, false, err
					}
				}

				if !isBaseline && isNew {
					rawSnap, _ := json.Marshal(item)
					cleanSnap := sanitizeSnapshotJSON(site.Bilibili, string(rawSnap), site.CreatorPost{
						ID: item.BVID, PubTs: item.PubTs, Type: "video", Title: item.Title, CoverURL: item.Cover,
					})
					inserted, err := m.insertUpdateItem(acc.Site, acc.UID, item.BVID, item.PubTs, "video", cleanSnap, nowStr)
					if err != nil {
						return newVideos, newDynamics, 0, false, err
					}
					if inserted {
						newVideos++
					}
				}
			}
			sub.LastVideoPubTs = maxPubTs
			sub.LastVideoBVID = maxBVID
			if maxPubTs > accState.NewestTs {
				accState.NewestTs = maxPubTs
				accState.NewestID = maxBVID
			}
		}
	}

	if checkDynamics {
		maxPages := 3
		if isBaseline {
			maxPages = 1
		}
		offset := ""
		seenIDs := make(map[string]bool)
		reachedBaseline := isBaseline
		var wm watermarkTracker

		for pageIdx := 0; pageIdx < maxPages; pageIdx++ {
			dpage, dErr := m.client.UserDynamics(ctx, mid, offset)
			if dErr != nil {
				return newVideos, newDynamics, autoCacheTasks, false, dErr
			}

			for _, dyn := range dpage.Items {
				dynID := strings.TrimSpace(dyn.ID)
				if dynID == "" || seenIDs[dynID] {
					continue
				}
				seenIDs[dynID] = true
				updateAccountProfile(sub, acc, dyn.Author.Name, dyn.Author.Face)

				isBaselinePinned := !isBaseline && m.wasPinnedOnBaseline(acc.Site, acc.UID, dynID)
				// Pinned dynamics do not trigger baseline stop condition (subscriptions.md §2).
				if !isBaseline && !dyn.Pinned {
					if isBaselinePinned || (accState.NewestID != "" && dynID == accState.NewestID) || (accState.NewestTs > 0 && dyn.PubTs <= accState.NewestTs) {
						reachedBaseline = true
						break
					}
				}

				wm.observe(dynID, dyn.PubTs, dyn.Pinned, isBaseline)

				cover := ""
				if len(dyn.Images) > 0 {
					cover = dyn.Images[0].URL
				} else if len(dyn.Videos) > 0 {
					cover = dyn.Videos[0].Cover
				}
				if m.lib != nil {
					if err := m.lib.UpsertFeedEntry(library.FeedEntry{
						Site:         "bilibili",
						Kind:         "dynamic",
						RawID:        dynID,
						Mid:          mid,
						AuthorName:   firstNonEmptyStr(dyn.Author.Name, sub.Uname),
						AuthorFace:   sanitizeAccountFace(site.Bilibili, firstNonEmptyStr(dyn.Author.Face, sub.Face)),
						Title:        dyn.Title,
						Summary:      dyn.Text,
						CoverURL:     sanitizeAccountFace(site.Bilibili, cover),
						CanonicalURL: dyn.CanonicalURL,
						PubTs:        dyn.PubTs,
					}); err != nil {
						return newVideos, newDynamics, 0, false, err
					}
				}

				dynCategory := normalizeDynamicType(dyn.Type)
				rawSnap, _ := json.Marshal(dyn)
				cleanSnap := sanitizeSnapshotJSON(site.Bilibili, string(rawSnap), site.CreatorPost{
					ID: dynID, PubTs: dyn.PubTs, Type: dynCategory, Title: dyn.Title, CoverURL: cover,
				})
				// INV-12 (DEC-033): First check only establishes baseline; dynamics before baseline never enter update_item.
				if !isBaseline && !isBaselinePinned && !(dyn.Pinned && (dynID == accState.NewestID || (accState.NewestTs > 0 && dyn.PubTs <= accState.NewestTs))) {
					inserted, err := m.insertUpdateItem(acc.Site, acc.UID, dynID, dyn.PubTs, dynCategory, cleanSnap, nowStr)
					if err != nil {
						return newVideos, newDynamics, 0, false, err
					}
					if inserted {
						newDynamics++
					}
				}
			}

			if reachedBaseline || !dpage.HasMore || dpage.Offset == "" || dpage.Offset == offset {
				if reachedBaseline || !dpage.HasMore || dpage.Offset == "" {
					reachedBaseline = true
				}
				break
			}
			offset = dpage.Offset
		}

		if isBaseline {
			if err := m.recordBaselinePinnedIDs(acc.Site, acc.UID, wm.baselinePinned); err != nil {
				return newVideos, newDynamics, 0, false, err
			}
		} else if !reachedBaseline {
			gapDetected = true
		}
		if updatedTs, updatedID := wm.apply(accState, isBaseline); updatedTs > 0 {
			sub.LastDynamicPubTs = updatedTs
			sub.LastDynamicID = updatedID
		}
	}

	m.enforceAccountItemCap(acc.Site, acc.UID, 200)
	return newVideos, newDynamics, autoCacheTasks, gapDetected, nil
}

func (m *Manager) checkFetcherAccount(
	ctx context.Context,
	fetcher site.SubscriptionFetcher,
	sub *Subscription,
	acc AccountRef,
	accState *AccountState,
	isBaseline bool,
) (accountCheckOutcome, error) {
	siteID := site.ID(acc.Site)
	maxPages := 2
	if isBaseline {
		maxPages = 1
	}

	cursor := ""
	if siteID == site.Douyin {
		cursor = "0"
	}
	seenIDs := make(map[string]bool)
	reachedBaseline := isBaseline

	var pendingFeed []library.FeedEntry
	var pendingItems []pendingUpdateRow
	wm := watermarkTracker{siteID: siteID}

	lastHasMore := false

	for pageIdx := 0; pageIdx < maxPages; pageIdx++ {
		page, err := fetcher.FetchCreatorPosts(ctx, acc.UID, cursor)
		if err != nil {
			return accountCheckOutcome{}, err
		}

		if pageIdx == 0 {
			m.applyPage1AuthorAndLiveHint(sub, acc, page)
			if page.TotalWorks > 0 {
				_ = m.RecordAccountWorks(acc.Site, acc.UID, page.TotalWorks)
			}
		}

		if page.UncertainEmpty {
			return accountCheckOutcome{
				incomplete:     true,
				incompleteNote: formatIncompleteNote(page.UncertainReason),
			}, nil
		}

		authorName := firstNonEmptyStr(page.AuthorName, firstNonEmptyStr(acc.Name, sub.Name))
		authorFace := sanitizeAccountFace(siteID, firstNonEmptyStr(page.AuthorFace, acc.Face))

		for _, post := range page.Posts {
			postID := strings.TrimSpace(post.ID)
			if siteID == site.X {
				if isPromotedXPost(post) {
					continue
				}
				normID, idErr := site.NormalizeUID(site.X, postID)
				if idErr != nil {
					continue
				}
				postID = normID
			}
			if postID == "" || seenIDs[postID] {
				continue
			}
			seenIDs[postID] = true

			isNonMonotonic := post.IsTop || post.IsFastRetweet
			isBaselinePinned := !isBaseline && m.wasPinnedOnBaseline(acc.Site, acc.UID, postID)
			if !isBaseline && !isNonMonotonic {
				if isBaselinePinned || postAtOrBeforeWatermarkForSite(siteID, postID, post.PubTs, accState) {
					reachedBaseline = true
					break
				}
			}

			wm.observe(postID, post.PubTs, isNonMonotonic, isBaseline)

			normType := normalizeSitePostType(siteID, post.Type)
			if siteID == site.Weibo && post.IsFastRetweet {
				normType = "forward"
			}
			feedKind, canonicalURL := fetcherPostFeedKindAndURL(siteID, acc.UID, postID, normType, post.Snapshot)

			pendingFeed = append(pendingFeed, library.FeedEntry{
				Site:         acc.Site,
				Kind:         feedKind,
				RawID:        postID,
				AuthorUID:    acc.UID,
				AuthorName:   authorName,
				AuthorFace:   authorFace,
				Title:        post.Title,
				CoverURL:     sanitizeAccountFace(siteID, post.CoverURL),
				CanonicalURL: canonicalURL,
				PubTs:        post.PubTs,
			})

			if !isBaseline {
				if isBaselinePinned {
					continue
				}
				if isNonMonotonic && postAtOrBeforeWatermarkForSite(siteID, postID, post.PubTs, accState) {
					continue
				}
				pendingItems = append(pendingItems, pendingUpdateRow{
					dynID:    postID,
					pubTs:    post.PubTs,
					typ:      normType,
					snapshot: sanitizeSnapshotJSON(siteID, post.Snapshot, post),
				})
			}
		}

		lastHasMore = page.HasMore && !page.BottomTipsVisible
		if reachedBaseline || !page.HasMore || page.BottomTipsVisible {
			reachedBaseline = true
			break
		}

		nextCursor := strings.TrimSpace(page.NextCursor)
		if siteID == site.Douyin {
			if nextCursor == "" || nextCursor == "0" || nextCursor == cursor {
				break
			}
			if cursor != "0" {
				if prevN, err1 := strconv.ParseInt(cursor, 10, 64); err1 == nil && prevN > 0 {
					if nextN, err2 := strconv.ParseInt(nextCursor, 10, 64); err2 == nil && nextN >= prevN {
						break
					}
				}
			}
		} else {
			if nextCursor == "" || nextCursor == cursor {
				break
			}
			if siteID == site.Weibo {
				if _, prevSince, ok1 := strings.Cut(cursor, ":"); ok1 && prevSince != "" {
					if _, nextSince, ok2 := strings.Cut(nextCursor, ":"); ok2 && nextSince == prevSince {
						break
					}
				}
			}
		}
		cursor = nextCursor
	}

	if m.lib != nil {
		for _, fe := range pendingFeed {
			if err := m.lib.UpsertFeedEntry(fe); err != nil {
				return accountCheckOutcome{}, err
			}
		}
	}

	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	var out accountCheckOutcome
	if !isBaseline {
		for _, it := range pendingItems {
			inserted, err := m.insertUpdateItem(acc.Site, acc.UID, it.dynID, it.pubTs, it.typ, it.snapshot, nowStr)
			if err != nil {
				return out, err
			}
			if inserted {
				if it.typ == "video" {
					out.newVideos++
				} else {
					out.newDynamics++
				}
			}
		}
		if !reachedBaseline && lastHasMore {
			out.gapDetected = true
		}
	} else {
		if err := m.recordBaselinePinnedIDs(acc.Site, acc.UID, wm.baselinePinned); err != nil {
			return out, err
		}
	}

	wm.apply(accState, isBaseline)
	m.enforceAccountItemCap(acc.Site, acc.UID, 200)
	return out, nil
}

func postAtOrBeforeWatermarkForSite(siteID site.ID, postID string, pubTs int64, accState *AccountState) bool {
	if accState == nil {
		return false
	}
	if accState.NewestID != "" && postID == accState.NewestID {
		return true
	}
	if siteID == site.X {
		if accState.NewestID != "" {
			if cmp, ok := compareUint64ID(postID, accState.NewestID); ok {
				return cmp <= 0
			}
			return compareNumericID(postID, accState.NewestID) <= 0
		}
		if accState.NewestTs > 0 && pubTs > 0 {
			return pubTs <= accState.NewestTs
		}
		return false
	}
	return postAtOrBeforeWatermark(postID, pubTs, accState)
}

func postAtOrBeforeWatermark(postID string, pubTs int64, accState *AccountState) bool {
	if accState == nil {
		return false
	}
	if accState.NewestID != "" && postID == accState.NewestID {
		return true
	}
	if accState.NewestTs <= 0 {
		return false
	}
	if pubTs < accState.NewestTs {
		return true
	}
	if pubTs == accState.NewestTs {
		return accState.NewestID == "" || compareNumericID(postID, accState.NewestID) <= 0
	}
	return false
}

func isPromotedXPost(post site.CreatorPost) bool {
	idLower := strings.ToLower(strings.TrimSpace(post.ID))
	if strings.HasPrefix(idLower, "promoted") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(post.Type)) {
	case "promoted", "ad", "advertisement":
		return true
	}
	raw := strings.TrimSpace(post.Snapshot)
	if raw != "" {
		var meta struct {
			Promoted         bool   `json:"promoted"`
			IsPromoted       bool   `json:"isPromoted"`
			EntryID          string `json:"entryId"`
			PromotedMetadata any    `json:"promotedMetadata"`
		}
		if json.Unmarshal([]byte(raw), &meta) == nil {
			if meta.Promoted || meta.IsPromoted || meta.PromotedMetadata != nil {
				return true
			}
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(meta.EntryID)), "promoted") {
				return true
			}
		}
	}
	return false
}

func fetcherPostFeedKindAndURL(siteID site.ID, uid, postID, normType, rawSnapshot string) (feedKind, canonicalURL string) {
	switch siteID {
	case site.Douyin:
		if normType == "draw" {
			return "dynamic", "https://www.douyin.com/note/" + postID
		}
		return "video", "https://www.douyin.com/video/" + postID
	case site.Weibo:
		return "dynamic", weiboCanonicalPostURL(uid, postID, rawSnapshot)
	case site.X:
		if trimmed := strings.TrimSpace(postID); trimmed != "" {
			return "dynamic", "https://x.com/i/web/status/" + trimmed
		}
		return "dynamic", ""
	case site.Bilibili:
		return "dynamic", "https://t.bilibili.com/" + postID
	default:
		return "dynamic", ""
	}
}

func weiboCanonicalPostURL(uid, postID, rawSnapshot string) string {
	trimmedSnap := strings.TrimSpace(rawSnapshot)
	if trimmedSnap != "" {
		var meta struct {
			CanonicalURL string `json:"canonicalUrl"`
			CanonicalAlt string `json:"canonical_url"`
			MblogID      string `json:"mblogId"`
			MblogIDAlt   string `json:"mblogid"`
		}
		if json.Unmarshal([]byte(trimmedSnap), &meta) == nil {
			for _, candidate := range []string{meta.CanonicalURL, meta.CanonicalAlt} {
				c := strings.TrimSpace(candidate)
				if strings.HasPrefix(c, "https://weibo.com/") && !strings.ContainsAny(c, "?#") {
					return c
				}
			}
			mblogID := firstNonEmptyStr(strings.TrimSpace(meta.MblogID), strings.TrimSpace(meta.MblogIDAlt))
			if mblogID != "" && !strings.ContainsAny(mblogID, "/?# \t\r\n") && strings.TrimSpace(uid) != "" {
				return "https://weibo.com/" + strings.TrimSpace(uid) + "/" + mblogID
			}
		}
	}
	if strings.TrimSpace(uid) != "" && strings.TrimSpace(postID) != "" {
		return "https://weibo.com/" + strings.TrimSpace(uid) + "/" + strings.TrimSpace(postID)
	}
	if strings.TrimSpace(postID) != "" {
		return "https://weibo.com/detail/" + strings.TrimSpace(postID)
	}
	return ""
}

func (m *Manager) applyPage1AuthorAndLiveHint(sub *Subscription, acc AccountRef, page site.CreatorPostsPage) {
	siteID := site.ID(acc.Site)
	cleanFace := sanitizeAccountFace(siteID, page.AuthorFace)

	var cleanUniqueID, liveRoomID, liveWebRID string
	if siteID == site.Douyin {
		cleanUniqueID = strings.TrimSpace(page.UniqueID)
		if utf8.RuneCountInString(cleanUniqueID) > 64 {
			runes := []rune(cleanUniqueID)
			cleanUniqueID = string(runes[:64])
		}
		liveRoomID = strings.TrimSpace(page.LiveRoomID)
		liveWebRID = strings.TrimSpace(page.LiveWebRID)
		if liveRoomID == "" && liveWebRID == "" {
			for _, p := range page.Posts {
				if p.LiveRoomID != "" || p.LiveWebRID != "" {
					liveRoomID = strings.TrimSpace(p.LiveRoomID)
					liveWebRID = strings.TrimSpace(p.LiveWebRID)
					break
				}
			}
		}
		if liveWebRID != "" && validateOptionalDouyinToken(liveWebRID, "webRid") != nil {
			liveWebRID = ""
		}
	} else if siteID == site.X {
		if handle, err := normalizeXScreenNameSnapshot(page.UniqueID); err == nil {
			cleanUniqueID = handle
		}
	}

	updatedAcc := acc
	for i := range sub.Accounts {
		if sub.Accounts[i].Site == acc.Site && sub.Accounts[i].UID == acc.UID {
			if page.AuthorName != "" {
				sub.Accounts[i].Name = page.AuthorName
			}
			if cleanFace != "" {
				sub.Accounts[i].Face = cleanFace
			}
			if siteID == site.Douyin {
				if cleanUniqueID != "" {
					sub.Accounts[i].UniqueID = cleanUniqueID
				}
				if liveWebRID != "" {
					sub.Accounts[i].WebRID = liveWebRID
				}
			} else if siteID == site.X && cleanUniqueID != "" {
				sub.Accounts[i].UniqueID = cleanUniqueID
			}
			updatedAcc = sub.Accounts[i]
		}
	}

	if raw := strings.TrimSpace(page.AuthorFace); raw != "" {
		m.mu.Lock()
		avatarFn := m.onAuthorAvatar
		m.mu.Unlock()
		if avatarFn != nil {
			avatarFn(acc.Site, acc.UID, raw)
		}
	}

	if siteID == site.Douyin && (liveRoomID != "" || liveWebRID != "") {
		m.mu.Lock()
		hintFn := m.onLiveHint
		m.mu.Unlock()
		if hintFn != nil {
			hintFn(updatedAcc, liveRoomID, liveWebRID)
		}
	}
}

func (m *Manager) recordBaselinePinnedIDs(siteName, uid string, ids []string) error {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updatesDB == nil {
		return nil
	}
	tx, err := m.updatesDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM account_baseline_pinned WHERE site = ? AND uid = ?`, normSite, uid); err != nil {
		return err
	}
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO account_baseline_pinned (site, uid, post_id) VALUES (?, ?, ?)`,
			normSite, uid, trimmed,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *Manager) wasPinnedOnBaseline(siteName, uid, id string) bool {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updatesDB == nil {
		return false
	}
	var found int
	err := m.updatesDB.QueryRow(
		`SELECT 1 FROM account_baseline_pinned WHERE site = ? AND uid = ? AND post_id = ? LIMIT 1`,
		normSite, uid, trimmed,
	).Scan(&found)
	return err == nil && found == 1
}

func normalizeDynamicType(rawType string) string {
	switch strings.ToUpper(strings.TrimSpace(rawType)) {
	case "DYNAMIC_TYPE_AV", "VIDEO":
		return "video"
	case "DYNAMIC_TYPE_DRAW", "DRAW":
		return "draw"
	case "DYNAMIC_TYPE_WORD", "WORD":
		return "word"
	case "DYNAMIC_TYPE_FORWARD", "FORWARD":
		return "forward"
	case "DYNAMIC_TYPE_ARTICLE", "ARTICLE":
		return "article"
	default:
		return "other"
	}
}

func normalizeSitePostType(siteID site.ID, raw string) string {
	if siteID == site.X {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "forward", "retweet", "repost":
			return "forward"
		case "video":
			return "video"
		case "draw", "note", "image", "image_note", "photo", "gif", "animated_gif", "mixed":
			return "draw"
		case "quote", "word", "text", "poll", "card", "article", "other", "":
			return "word"
		default:
			return "word"
		}
	}
	return normalizeFetcherPostType(raw)
}

func normalizeFetcherPostType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "video":
		return "video"
	case "draw", "note", "image", "image_note":
		return "draw"
	case "word":
		return "word"
	case "forward":
		return "forward"
	case "article":
		return "article"
	default:
		return "other"
	}
}

func sanitizeSnapshotJSON(siteID site.ID, raw string, post site.CreatorPost) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		var generic any
		if json.Unmarshal([]byte(trimmed), &generic) == nil {
			cleaned := scrubSignedValues(siteID, generic)
			if encoded, err := json.Marshal(cleaned); err == nil {
				return string(encoded)
			}
		}
	}
	fallback := map[string]any{
		"id":    post.ID,
		"pubTs": post.PubTs,
		"type":  normalizeSitePostType(siteID, post.Type),
		"title": post.Title,
	}
	if cleanCover := sanitizeAccountFace(siteID, post.CoverURL); cleanCover != "" {
		fallback["coverUrl"] = cleanCover
	}
	encoded, _ := json.Marshal(fallback)
	return string(encoded)
}

func scrubSignedValues(siteID site.ID, v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, elem := range val {
			if siteID == site.Weibo && isWeiboEphemeralSnapshotKey(k) {
				continue
			}
			if siteID == site.X && isXEphemeralSnapshotKey(k) {
				continue
			}
			out[k] = scrubSignedValues(siteID, elem)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = scrubSignedValues(siteID, elem)
		}
		return out
	case string:
		lower := strings.ToLower(strings.TrimSpace(val))
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//") {
			return sanitizeAccountFace(siteID, val)
		}
		if siteID == site.X && hasXSecretMarker(lower) {
			return ""
		}
		return val
	default:
		return v
	}
}

func hasXSecretMarker(lower string) bool {
	for _, marker := range []string{
		"auth_token=", "auth_token%3d",
		"ct0=", "ct0%3d",
		"kdt=", "kdt%3d",
		"x-csrf-token",
		"authorization: bearer", "authorization:bearer",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isXEphemeralSnapshotKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "videourl", "video_url", "videourls", "video_urls",
		"videoinfo", "video_info", "variants", "streams",
		"streamurl", "stream_url", "hlsurl", "hls_url", "m3u8url", "m3u8_url",
		"mp4url", "mp4_url", "playbackurl", "playback_url",
		"gifurl", "gif_url",
		"stats", "favoritecount", "favorite_count",
		"retweetcount", "retweet_count", "replycount", "reply_count",
		"quotecount", "quote_count", "bookmarkcount", "bookmark_count",
		"viewcount", "view_count", "views",
		"cookie", "cookies", "auth_token", "ct0", "authorization", "bearer":
		return true
	}
	return false
}

func isWeiboEphemeralSnapshotKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "videourl", "video_url", "videourls", "video_urls",
		"livephotourl", "live_photo_url", "motionurl", "motion_url",
		"motionfallbackurls", "playback_list", "stream_url", "stream_url_hd",
		"mp4_hd_url", "mp4_720p_mp4", "hevc_mp4_hd",
		"stats", "repostscount", "reposts_count",
		"commentscount", "comments_count",
		"attitudescount", "attitudes_count":
		return true
	}
	return false
}
