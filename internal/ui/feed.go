package ui

import (
	"fmt"
	"sort"

	"github.com/chialecode/trace/internal/library"
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
	if a.HomeActorFilter != nil {
		authors = a.HomeActorFilter.Authors
	}
	page, err := a.backend.FeedPage(library.FeedPageRequest{
		Cursor:     &library.FeedCursor{Key: edge.FeedKey, PubTs: edge.PubTs},
		Order:      "newest",
		Direction:  direction,
		CachedOnly: true,
		Authors:    authors,
		Limit:      30,
	})
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
	page, err := a.backend.FeedPage(library.FeedPageRequest{Order: "newest", CachedOnly: true, Limit: 60})
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
// that have cached content published after the last viewed timestamp (R9, Q-009).
func (a *App) homeUpdatedActors() []HomeUpdatedActor {
	if a.backend == nil || !a.backend.AccountStatus().LoggedIn {
		return nil
	}
	a.loadHomeBaseline()
	anchorTs := a.homeBaselineTs
	uiSt := a.backend.GetUIState()
	if anchorTs <= 0 && len(uiSt.HomeSeen) == 0 {
		return nil
	}

	type authorSummary struct {
		Name     string
		Face     string
		LatestTs int64
	}
	authorLatest := make(map[int64]authorSummary)
	for _, au := range a.libraryStatistics.Authors {
		if au.Mid > 0 && au.LatestPubTs > 0 {
			authorLatest[au.Mid] = authorSummary{
				Name:     au.Name,
				Face:     au.Face,
				LatestTs: au.LatestPubTs,
			}
		}
	}
	feedSlice := a.FeedItems
	if a.HomeActorFilter != nil && len(a.savedMainFeedItems) > 0 {
		feedSlice = a.savedMainFeedItems
	}
	for _, e := range feedSlice {
		if !e.Cached || e.Mid <= 0 || e.PubTs <= 0 {
			continue
		}
		prev := authorLatest[e.Mid]
		if e.PubTs > prev.LatestTs {
			prev.LatestTs = e.PubTs
			if e.AuthorName != "" {
				prev.Name = e.AuthorName
			}
			if e.AuthorFace != "" {
				prev.Face = e.AuthorFace
			}
			authorLatest[e.Mid] = prev
		}
	}
	if len(authorLatest) == 0 {
		return nil
	}

	ownedMid := make(map[int64]bool)
	var out []HomeUpdatedActor
	for _, sub := range a.Subscriptions {
		authors := subscriptionAuthors(sub)
		var maxTs int64
		repMid := sub.Mid
		repFace := sub.Face
		for _, mid := range authors {
			ownedMid[mid] = true
			if repMid == 0 {
				repMid = mid
			}
			if info, ok := authorLatest[mid]; ok {
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
			out = append(out, HomeUpdatedActor{
				Key:      key,
				SubID:    sub.ID,
				Mid:      repMid,
				Name:     firstNonEmptyStrUI(sub.Name, sub.Uname, fmt.Sprintf("UID %d", repMid)),
				Face:     repFace,
				Authors:  authors,
				LatestTs: maxTs,
			})
		}
	}

	for mid, info := range authorLatest {
		if ownedMid[mid] || info.LatestTs <= 0 {
			continue
		}
		key := fmt.Sprintf("mid:%d", mid)
		baseTs := max(anchorTs, uiSt.HomeSeen[key])
		if baseTs > 0 && info.LatestTs > baseTs {
			out = append(out, HomeUpdatedActor{
				Key:      key,
				Mid:      mid,
				Name:     firstNonEmptyStrUI(info.Name, fmt.Sprintf("UID %d", mid)),
				Face:     info.Face,
				Authors:  []int64{mid},
				LatestTs: info.LatestTs,
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
	return false
}

// OpenHomeUpdatedActor opens the single-subscription/creator feed view for the matching updated actor on Home (R9).
func (a *App) OpenHomeUpdatedActor(nameOrKey string) bool {
	for _, actor := range a.homeUpdatedActors() {
		if actor.Name == nameOrKey || actor.Key == nameOrKey || actor.SubID == nameOrKey {
			return a.openHomeActorFeed(actor)
		}
	}
	return false
}

// openHomeActorFeed opens the single-subscription (or single-creator) cached feed view on Home
// and records its latest cached timestamp in ui-state.json HomeSeen (R9, Q-009).
func (a *App) openHomeActorFeed(actor HomeUpdatedActor) bool {
	if len(actor.Authors) == 0 {
		return false
	}
	page, err := a.backend.FeedPage(library.FeedPageRequest{
		Order:      "newest",
		CachedOnly: true,
		Authors:    actor.Authors,
		Limit:      60,
	})
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
		if _, err := a.backend.UpdateUIState(st); err != nil {
			a.showError(fmt.Errorf("保存主页查看时间失败：%w", err))
		}
	}

	actorCopy := actor
	a.HomeActorFilter = &actorCopy
	a.FeedAuthorsFilter = append([]int64(nil), actor.Authors...)
	a.FeedScroll = ui.ScrollState{}
	a.resetFeedListState()
	a.FeedListState.ScrollTo(0, ui.Start)

	a.FeedItems = page.Items
	a.FeedError = ""
	a.feedMoreBefore = false
	a.feedMoreAfter = page.HasMore
	return true
}

// closeHomeActorFeed exits the single-subscription feed view and restores the main timeline position (R9).
func (a *App) closeHomeActorFeed() {
	if a.HomeActorFilter == nil {
		return
	}
	a.HomeActorFilter = nil
	a.FeedError = a.savedMainFeedError
	a.feedFailedBefore = a.savedMainFeedFailedBefore
	a.FeedAuthorsFilter = nil
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
