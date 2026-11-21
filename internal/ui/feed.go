package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

// markHomeSeen records the newest post the main timeline has shown, as the
// anchor (DEC-040, kept by DEC-053). It is the initial "seen" of every
// subscription without its own homeSeen entry in the next session; the
// reading position is kept separately per site filter (homePositions).
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

// loadHomeBaseline reads, once per session, the anchor of the previous
// session and the subscriptions seen (homeSeen): a subscription is unread
// when it has cached content newer than its homeSeen entry, or than the
// anchor when it has none (DEC-053 ②).
func (a *App) loadHomeBaseline() {
	if a.homeBaselineLoaded {
		return
	}
	a.homeBaselineLoaded = true
	anchor := a.backend.Anchor()
	a.homeBaselineTs, a.homeSeenTs = anchor.PubTs, anchor.PubTs
	a.homeSeenLocal = a.backend.GetUIState().HomeSeen
	a.homeSubRowsValid = false
}

// RestartHomeSession forgets the session's baseline, reading positions and
// the order of the subscriptions on Home, so they are read again as at
// startup (tests and screenshot scenes stage a previous session).
func (a *App) RestartHomeSession() {
	a.homeBaselineLoaded = false
	a.homePosRestored = nil
	a.homeFilterViews = nil
	a.homePosSaved = nil
	a.homePosSeen, a.homePosSeenSite = uistate.HomePosition{}, ""
	a.homeSubOrder = nil
	a.homeSubRowsValid = false
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

// setFeedSiteFilter switches the site filter of Home. Each filter keeps its
// own main timeline and place within the session, and the first time a
// filter shows in a session it opens where it was read last (DEC-053 ①).
func (a *App) setFeedSiteFilter(siteFilter string) {
	next := normalizeSiteFilter(siteFilter)
	prev := normalizeSiteFilter(a.FeedSiteFilter)
	if prev == next {
		return
	}
	if a.homeFilterViews == nil {
		a.homeFilterViews = make(map[string]*homeFeedView)
	}
	if a.HomeActorFilter == nil {
		a.flushHomePosition()
		a.homeFilterViews[prev] = a.captureMainFeedView()
	} else if a.savedMain != nil {
		a.homeFilterViews[prev] = a.savedMain
	}
	a.homePosSeen, a.homePosSeenSite = uistate.HomePosition{}, ""
	a.FeedSiteFilter = next
	a.homeSubRowsValid = false
	if a.backend != nil {
		if _, err := a.backend.MutateUIState(func(st *uistate.State) { st.FeedFilter.Site = next }); err != nil {
			a.showError(fmt.Errorf("保存主页站点筛选失败：%w", err))
		}
	}
	view := a.homeFilterViews[next]
	delete(a.homeFilterViews, next)
	if a.HomeActorFilter != nil {
		// The main timeline of the new filter shows again on leaving the
		// subscription; without one, it is loaded then.
		a.savedMain = view
		a.openHomeActorFeed(*a.HomeActorFilter)
		return
	}
	if view != nil {
		a.applyMainFeedView(view)
		return
	}
	a.FeedItems = nil
	a.FeedScroll = ui.ScrollState{}
	a.resetFeedListState()
	a.FeedListState.ScrollTo(0, ui.Start)
	a.loadInitialFeed()
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
	req := library.FeedPageRequest{
		Cursor:     &library.FeedCursor{Key: edge.FeedKey, PubTs: edge.PubTs},
		Order:      "newest",
		Direction:  direction,
		Site:       feedSiteParam(a.FeedSiteFilter),
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
		a.FeedError = userErrorText(err)
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
	page, err := a.backend.FeedPage(library.FeedPageRequest{Order: "newest", Site: feedSiteParam(a.FeedSiteFilter), CachedOnly: true, Limit: 60})
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

// OpenHomeUpdatedActor opens the feed of the subscription on Home whose
// name, key ("sub:<id>") or id is nameOrKey.
func (a *App) OpenHomeUpdatedActor(nameOrKey string) bool {
	for _, actor := range a.homeSubscriptionRows() {
		if actor.Name == nameOrKey || actor.Key == nameOrKey || actor.SubID == nameOrKey {
			return a.openHomeActorFeed(actor)
		}
	}
	return false
}

// openHomeActorFeed opens the cached feed of one subscription on Home and
// records it as seen: homeSeen["sub:<id>"] becomes the newest cached post
// it has under the site filter, and its dot goes at once (DEC-053 ②).
func (a *App) openHomeActorFeed(actor HomeUpdatedActor) bool {
	if len(actor.Authors) == 0 && len(actor.AuthorRefs) == 0 {
		return false
	}
	req := library.FeedPageRequest{
		Order:      "newest",
		Site:       feedSiteParam(a.FeedSiteFilter),
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
		a.flushHomePosition()
		a.savedMain = a.captureMainFeedView()
	}
	a.markSubscriptionSeen(actor)

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

// markSubscriptionSeen writes homeSeen for a subscription opened on Home:
// the larger of what it had and its newest cached post.
func (a *App) markSubscriptionSeen(actor HomeUpdatedActor) {
	if actor.Key == "" || actor.LatestTs <= 0 {
		return
	}
	if a.homeSeenLocal[actor.Key] >= actor.LatestTs {
		return
	}
	next := make(map[string]int64, len(a.homeSeenLocal)+1)
	for k, v := range a.homeSeenLocal {
		next[k] = v
	}
	next[actor.Key] = actor.LatestTs
	a.homeSeenLocal = next
	a.homeSeenRev++
	if a.backend.GetUIState().HomeSeen[actor.Key] >= actor.LatestTs {
		return
	}
	if _, err := a.backend.MutateUIState(func(st *uistate.State) {
		if st.HomeSeen == nil {
			st.HomeSeen = make(map[string]int64)
		}
		if st.HomeSeen[actor.Key] < actor.LatestTs {
			st.HomeSeen[actor.Key] = actor.LatestTs
		}
	}); err != nil {
		a.showError(fmt.Errorf("保存主页查看时间失败：%w", err))
	}
}

// closeHomeActorFeed leaves a subscription's feed for the main timeline of
// the site filter, where it was left.
func (a *App) closeHomeActorFeed() {
	if a.HomeActorFilter == nil {
		return
	}
	a.HomeActorFilter = nil
	a.FeedAuthorsFilter = nil
	a.FeedAuthorRefsFilter = nil
	view := a.savedMain
	a.savedMain = nil
	if view != nil && len(view.Items) > 0 {
		a.applyMainFeedView(view)
		return
	}
	a.resetFeedListState()
	a.FeedScroll = ui.ScrollState{}
	a.FeedItems = nil
	a.loadInitialFeed()
}
