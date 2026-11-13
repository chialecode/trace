package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

// markHomeSeen records the newest post the main timeline has shown. The
// timeline always opens at the newest post; it keeps no reading position and
// counts nothing as unread: what was scrolled past was seen (DEC-040). The
// stored value is only the baseline for the next session's "有更新".
func (a *App) markHomeSeen(first, last int) {
	if a.HomeActorFilter != nil || first < 0 {
		return
	}
	var newest library.FeedEntry
	for i := max(first, 0); i <= last && i < len(a.FeedItems); i++ {
		if a.FeedItems[i].PubTs > newest.PubTs {
			newest = a.FeedItems[i]
		}
	}
	if newest.PubTs <= a.homeSeenTs {
		return
	}
	a.homeSeenTs = newest.PubTs
	if err := a.backend.SaveAnchor(uistate.FeedAnchor{Order: "newest", Key: newest.FeedKey, PubTs: newest.PubTs}); err != nil {
		a.showError(fmt.Errorf("保存主页查看时间失败：%w", err))
	}
}

// loadHomeBaseline reads, once per session, the newest post seen in the
// previous session; "有更新" lists people with cached posts newer than it.
func (a *App) loadHomeBaseline() {
	if a.homeBaselineLoaded {
		return
	}
	a.homeBaselineLoaded = true
	anchor := a.backend.Anchor()
	a.homeBaselineTs, a.homeSeenTs = anchor.PubTs, anchor.PubTs
}

// RestartHomeSession forgets the session's baseline, so the stored one is
// read again as at startup (screenshot scenes stage a previous session).
func (a *App) RestartHomeSession() {
	a.homeBaselineLoaded = false
}

func subscriptionAuthorRefs(sub subscription.Subscription) []library.AuthorRef {
	seen := make(map[string]bool)
	var out []library.AuthorRef
	add := func(siteID, uid string, mid int64) {
		normSite, normUID := library.NormalizeAuthorUID(siteID, uid, mid)
		if normUID == "" {
			return
		}
		k := normSite + ":" + normUID
		if !seen[k] {
			seen[k] = true
			out = append(out, library.AuthorRef{Site: normSite, UID: normUID})
		}
	}
	for _, acc := range sub.Accounts {
		add(acc.Site, acc.UID, 0)
	}
	if len(out) == 0 {
		pSite, pUID := subscriptionPrimarySiteAndUID(sub)
		add(pSite, pUID, sub.Mid)
	}
	return out
}

func (a *App) setFeedSiteFilter(siteFilter string) {
	next := normalizeSiteFilter(siteFilter)
	if normalizeSiteFilter(a.FeedSiteFilter) == next {
		return
	}
	a.FeedSiteFilter = next
	a.savedMainFeedItems = nil
	if a.backend != nil {
		st := a.backend.GetUIState()
		st.FeedFilter.Site = next
		if _, err := a.backend.UpdateUIState(st); err != nil {
			a.showError(fmt.Errorf("保存主页站点筛选失败：%w", err))
		}
	}
	if a.HomeActorFilter != nil {
		a.openHomeActorFeed(*a.HomeActorFilter)
		return
	}
	a.showLatestFeed()
}

func (a *App) loadFeedEdge(before bool) {
	if len(a.FeedItems) == 0 {
		return
	}
	index, direction := len(a.FeedItems)-1, "older"
	if before {
		index = 0
		direction = "newer"
	}
	edge := a.FeedItems[index]
	authors := a.FeedAuthorsFilter
	authorRefs := a.FeedAuthorRefsFilter
	if a.HomeActorFilter != nil {
		authors = a.HomeActorFilter.Authors
		authorRefs = a.HomeActorFilter.AuthorRefs
	}
	siteFilter := normalizeSiteFilter(a.FeedSiteFilter)
	if siteFilter == "all" {
		siteFilter = ""
	}
	req := library.FeedPageRequest{
		Cursor:     &library.FeedCursor{Key: edge.FeedKey, PubTs: edge.PubTs},
		Order:      "newest",
		Direction:  direction,
		Site:       siteFilter,
		CachedOnly: true,
		Limit:      30,
	}
	if len(authorRefs) > 0 {
		req.AuthorRefs = authorRefs
	} else {
		req.Authors = authors
	}
	page, err := a.backend.FeedPage(req)
	if err != nil {
		a.FeedError = err.Error()
		a.feedFailedBefore = before
		a.showError(err)
		return
	}
	a.FeedError = ""
	if before {
		a.feedMoreBefore = page.HasMore
	} else {
		a.feedMoreAfter = page.HasMore
	}
	if len(page.Items) == 0 {
		return
	}
	seen := make(map[string]bool, len(a.FeedItems))
	for _, entry := range a.FeedItems {
		seen[entry.FeedKey] = true
	}
	var added []library.FeedEntry
	for _, entry := range page.Items {
		if !seen[entry.FeedKey] {
			added = append(added, entry)
			seen[entry.FeedKey] = true
		}
	}
	if before {
		a.FeedItems = append(added, a.FeedItems...)
	} else {
		a.FeedItems = append(a.FeedItems, added...)
	}
}

// showLatestFeed reloads the main timeline from the newest post.
func (a *App) showLatestFeed() {
	siteFilter := normalizeSiteFilter(a.FeedSiteFilter)
	if siteFilter == "all" {
		siteFilter = ""
	}
	page, err := a.backend.FeedPage(library.FeedPageRequest{Order: "newest", Site: siteFilter, CachedOnly: true, Limit: 60})
	if err != nil {
		a.showError(err)
		return
	}
	a.FeedItems = page.Items
	a.FeedError = ""
	a.feedMoreBefore, a.feedMoreAfter = false, page.HasMore
	a.FeedScroll = ui.ScrollState{}
	a.resetFeedListState()
	a.FeedListState.ScrollTo(0, ui.Start)
}

// homeUpdatedActors computes the list of subscriptions (and unsubscribed creators)
// that have cached content published after the last viewed timestamp.
func (a *App) homeUpdatedActors() []HomeUpdatedActor {
	if a.backend == nil || !a.backend.HasAnyLogin() {
		return nil
	}
	a.loadHomeBaseline()
	anchorTs := a.homeBaselineTs
	uiSt := a.backend.GetUIState()
	if anchorTs <= 0 && len(uiSt.HomeSeen) == 0 {
		return nil
	}

	type authorSummary struct {
		Site     string
		UID      string
		Mid      int64
		Name     string
		Face     string
		LatestTs int64
	}
	authorLatest := make(map[string]authorSummary)
	for _, au := range a.libraryStatistics.Authors {
		normSite, normUID := library.NormalizeAuthorUID(au.Site, au.UID, au.Mid)
		if normUID == "" || au.LatestPubTs <= 0 {
			continue
		}
		mid := au.Mid
		if normSite == "bilibili" && mid <= 0 {
			mid, _ = strconv.ParseInt(normUID, 10, 64)
		}
		authorLatest[normSite+":"+normUID] = authorSummary{
			Site:     normSite,
			UID:      normUID,
			Mid:      mid,
			Name:     au.Name,
			Face:     au.Face,
			LatestTs: au.LatestPubTs,
		}
	}
	feedSlice := a.FeedItems
	if a.HomeActorFilter != nil && len(a.savedMainFeedItems) > 0 {
		feedSlice = a.savedMainFeedItems
	}
	for _, e := range feedSlice {
		if !e.Cached || e.PubTs <= 0 {
			continue
		}
		normSite, normUID := library.NormalizeAuthorUID(e.Site, e.AuthorUID, e.Mid)
		if normUID == "" {
			continue
		}
		k := normSite + ":" + normUID
		prev := authorLatest[k]
		prev.Site = normSite
		prev.UID = normUID
		if e.Mid > 0 {
			prev.Mid = e.Mid
		}
		if e.PubTs > prev.LatestTs {
			prev.LatestTs = e.PubTs
			if e.AuthorName != "" {
				prev.Name = e.AuthorName
			}
			if e.AuthorFace != "" {
				prev.Face = e.AuthorFace
			}
			authorLatest[k] = prev
		}
	}
	if len(authorLatest) == 0 {
		return nil
	}

	ownedKeys := make(map[string]bool)
	var out []HomeUpdatedActor
	for _, sub := range a.Subscriptions {
		authors := subscriptionAuthors(sub)
		refs := subscriptionAuthorRefs(sub)
		var maxTs int64
		repSite, repUID := subscriptionPrimarySiteAndUID(sub)
		repMid := sub.Mid
		repFace := sub.Face
		for _, ref := range refs {
			k := ref.Site + ":" + ref.UID
			ownedKeys[k] = true
			if repUID == "" {
				repSite = ref.Site
				repUID = ref.UID
			}
			if repMid == 0 && ref.Site == "bilibili" {
				repMid, _ = strconv.ParseInt(ref.UID, 10, 64)
			}
			if info, ok := authorLatest[k]; ok {
				if info.LatestTs > maxTs {
					maxTs = info.LatestTs
				}
				if repFace == "" && info.Face != "" {
					repFace = info.Face
				}
			}
		}
		if maxTs <= 0 {
			continue
		}
		key := "sub:" + sub.ID
		baseTs := max(anchorTs, uiSt.HomeSeen[key])
		if baseTs > 0 && maxTs > baseTs {
			fallbackName := repUID
			if repMid > 0 {
				fallbackName = fmt.Sprintf("UID %d", repMid)
			}
			out = append(out, HomeUpdatedActor{
				Key:        key,
				SubID:      sub.ID,
				Site:       repSite,
				UID:        repUID,
				Mid:        repMid,
				Name:       firstNonEmptyStrUI(sub.Name, sub.Uname, fallbackName),
				Face:       repFace,
				Authors:    authors,
				AuthorRefs: refs,
				LatestTs:   maxTs,
			})
		}
	}

	for k, info := range authorLatest {
		if ownedKeys[k] || info.LatestTs <= 0 {
			continue
		}
		key := library.AuthorHomeSeenKey(info.Site, info.UID)
		seenTs := library.LookupAuthorHomeSeen(uiSt.HomeSeen, info.Site, info.UID)
		baseTs := max(anchorTs, seenTs)
		if baseTs > 0 && info.LatestTs > baseTs {
			var mids []int64
			if info.Site == "bilibili" && info.Mid > 0 {
				mids = []int64{info.Mid}
			}
			fallbackName := info.UID
			if info.Mid > 0 {
				fallbackName = fmt.Sprintf("UID %d", info.Mid)
			}
			out = append(out, HomeUpdatedActor{
				Key:        key,
				Site:       info.Site,
				UID:        info.UID,
				Mid:        info.Mid,
				Name:       firstNonEmptyStrUI(info.Name, fallbackName),
				Face:       info.Face,
				Authors:    mids,
				AuthorRefs: []library.AuthorRef{{Site: info.Site, UID: info.UID}},
				LatestTs:   info.LatestTs,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].LatestTs != out[j].LatestTs {
			return out[i].LatestTs > out[j].LatestTs
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (a *App) isActorLive(actor HomeUpdatedActor) bool {
	for _, rm := range a.LiveRooms {
		if rm.LiveStatus != 1 {
			continue
		}
		for _, id := range actor.Authors {
			if rm.UID == id {
				return true
			}
		}
	}
	for _, st := range a.LiveAccounts {
		if st.LiveStatus != 1 {
			continue
		}
		for _, ref := range actor.AuthorRefs {
			if strings.EqualFold(st.Site, ref.Site) && st.UID == ref.UID {
				return true
			}
		}
	}
	return false
}

// OpenHomeUpdatedActor opens the single-subscription/creator feed view for the matching updated actor on Home.
func (a *App) OpenHomeUpdatedActor(nameOrKey string) bool {
	for _, actor := range a.homeUpdatedActors() {
		if actor.Name == nameOrKey || actor.Key == nameOrKey || actor.SubID == nameOrKey {
			return a.openHomeActorFeed(actor)
		}
	}
	return false
}

// openHomeActorFeed opens the single-subscription (or single-creator) cached feed view on Home
// and records its latest cached timestamp in ui-state.json HomeSeen.
func (a *App) openHomeActorFeed(actor HomeUpdatedActor) bool {
	if len(actor.Authors) == 0 && len(actor.AuthorRefs) == 0 {
		return false
	}
	siteFilter := normalizeSiteFilter(a.FeedSiteFilter)
	if siteFilter == "all" {
		siteFilter = ""
	}
	req := library.FeedPageRequest{
		Order:      "newest",
		Site:       siteFilter,
		CachedOnly: true,
		Limit:      60,
	}
	if len(actor.AuthorRefs) > 0 {
		req.AuthorRefs = actor.AuthorRefs
	} else {
		req.Authors = actor.Authors
	}
	page, err := a.backend.FeedPage(req)
	if err != nil {
		a.showError(err)
		return false
	}
	if a.HomeActorFilter == nil {
		a.savedMainFeedItems = append([]library.FeedEntry(nil), a.FeedItems...)
		a.savedMainFeedListState = a.FeedListState
		a.savedMainFeedScrollY = a.FeedScroll.Y
		a.savedMainFeedMoreBefore = a.feedMoreBefore
		a.savedMainFeedMoreAfter = a.feedMoreAfter
		a.savedMainFeedError = a.FeedError
		a.savedMainFeedFailedBefore = a.feedFailedBefore
	}

	if actor.Key != "" && actor.LatestTs > 0 {
		st := a.backend.GetUIState()
		if st.HomeSeen == nil {
			st.HomeSeen = make(map[string]int64)
		}
		st.HomeSeen[actor.Key] = actor.LatestTs
		if actor.Site == "bilibili" && actor.Mid > 0 && actor.SubID == "" {
			st.HomeSeen[fmt.Sprintf("mid:%d", actor.Mid)] = actor.LatestTs
		}
		if _, err := a.backend.UpdateUIState(st); err != nil {
			a.showError(fmt.Errorf("保存主页查看时间失败：%w", err))
		}
	}

	actorCopy := actor
	a.HomeActorFilter = &actorCopy
	a.FeedAuthorsFilter = append([]int64(nil), actor.Authors...)
	a.FeedAuthorRefsFilter = append([]library.AuthorRef(nil), actor.AuthorRefs...)
	a.FeedScroll = ui.ScrollState{}
	a.resetFeedListState()
	a.FeedListState.ScrollTo(0, ui.Start)

	a.FeedItems = page.Items
	a.FeedError = ""
	a.feedMoreBefore = false
	a.feedMoreAfter = page.HasMore
	return true
}

// closeHomeActorFeed exits the single-subscription feed view and restores the main timeline position.
func (a *App) closeHomeActorFeed() {
	if a.HomeActorFilter == nil {
		return
	}
	a.HomeActorFilter = nil
	a.FeedError = a.savedMainFeedError
	a.feedFailedBefore = a.savedMainFeedFailedBefore
	a.FeedAuthorsFilter = nil
	a.FeedAuthorRefsFilter = nil
	a.resetFeedListState()
	if len(a.savedMainFeedItems) > 0 {
		a.FeedItems = append([]library.FeedEntry(nil), a.savedMainFeedItems...)
		// The actor view uses a fresh ListState. Restore the main list's
		// measured row heights, stable key and inset together with its offset.
		a.FeedListState = a.savedMainFeedListState
		a.feedMoreBefore = a.savedMainFeedMoreBefore
		a.feedMoreAfter = a.savedMainFeedMoreAfter
		a.FeedScroll.Y = a.savedMainFeedScrollY
	} else {
		a.loadInitialFeed()
	}
	a.feedMoreBefore = true
}
