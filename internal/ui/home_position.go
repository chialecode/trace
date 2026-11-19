package ui

import (
	"fmt"
	"math"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

// homePositionSaveDelay is how long the main timeline stays still before
// its reading position is saved (DEC-053 ①).
const homePositionSaveDelay = time.Second

// homeFeedView is the main timeline of one site filter of Home as it was
// left: its rows, place and paging state.
type homeFeedView struct {
	Items        []library.FeedEntry
	MoreBefore   bool
	MoreAfter    bool
	Err          string
	FailedBefore bool
	// Top and Offset are the post at the top of the view and how far its
	// top was above the view's.
	Top    string
	Offset int
}

// Phases of opening the main timeline at a saved position: the list is laid
// out at the post first, then scrolled by the saved offset.
const (
	homeRestoreNone = iota
	homeRestoreLayout
	homeRestoreSettle
)

// feedSiteParam is the site of a feed page request for a site filter.
func feedSiteParam(siteFilter string) string {
	if s := normalizeSiteFilter(siteFilter); s != "all" {
		return s
	}
	return ""
}

func (a *App) captureMainFeedView() *homeFeedView {
	top, offset := "", 0
	if a.homePosSeenSite == normalizeSiteFilter(a.FeedSiteFilter) && a.homePosSeen.Key != "" {
		top, offset = a.homePosSeen.Key, a.homePosSeen.OffsetPx
	} else if first, last := a.FeedListState.Visible(); first >= 0 && last >= first && first < len(a.FeedItems) {
		top = a.FeedItems[first].FeedKey
	}
	return &homeFeedView{
		Top:          top,
		Offset:       offset,
		Items:        append([]library.FeedEntry(nil), a.FeedItems...),
		MoreBefore:   a.feedMoreBefore,
		MoreAfter:    a.feedMoreAfter,
		Err:          a.FeedError,
		FailedBefore: a.feedFailedBefore,
	}
}

// applyMainFeedView shows a main timeline as it was left: a new list opens
// at the post that was at the top, by the same offset. (The list's own
// state is not kept: another list, a subscription's feed or another site
// filter's, has scrolled the element since.) What was cached meanwhile is
// loaded on reaching the top.
func (a *App) applyMainFeedView(v *homeFeedView) {
	a.FeedItems = append([]library.FeedEntry(nil), v.Items...)
	a.feedMoreBefore = true
	a.feedMoreAfter = v.MoreAfter
	a.FeedError = v.Err
	a.feedFailedBefore = v.FailedBefore
	a.FeedScroll = ui.ScrollState{}
	a.resetFeedListState()
	a.homeRestorePhase = homeRestoreNone
	if len(a.FeedItems) == 0 {
		a.feedMoreBefore = false
		a.loadInitialFeed()
		return
	}
	index, found := 0, false
	for i, e := range a.FeedItems {
		if e.FeedKey == v.Top {
			index, found = i, true
			break
		}
	}
	a.FeedListState.ScrollTo(index, ui.Start)
	a.homeRestoreOffset = 0
	if found {
		a.homeRestoreOffset = v.Offset
	}
	a.homeRestorePhase = homeRestoreLayout
}

// restoreHomePosition opens the main timeline of the site filter at the
// post read last in a previous session: it and the older posts load, the
// newer ones on scrolling up. A post deleted since gives way to the nearest
// one not newer than it. It reports false when there is no position or no
// post at or before it.
func (a *App) restoreHomePosition() bool {
	site := normalizeSiteFilter(a.FeedSiteFilter)
	pos, ok := a.backend.GetUIState().HomePositions[site]
	if !ok || pos.Key == "" {
		return false
	}
	// "\x00" makes the cursor take the post itself: no key lies between
	// a key and the key followed by NUL.
	page, err := a.backend.FeedPage(library.FeedPageRequest{
		Cursor:     &library.FeedCursor{Key: pos.Key + "\x00", PubTs: pos.PubTs},
		Direction:  "older",
		Order:      "newest",
		Site:       feedSiteParam(site),
		CachedOnly: true,
		Limit:      60,
	})
	if err != nil || len(page.Items) == 0 {
		return false
	}
	a.FeedItems = page.Items
	a.FeedError = ""
	a.feedMoreBefore, a.feedMoreAfter = true, page.HasMore
	a.FeedScroll = ui.ScrollState{}
	a.resetFeedListState()
	a.FeedListState.ScrollTo(0, ui.Start)
	a.homeRestoreOffset = 0
	if page.Items[0].FeedKey == pos.Key {
		a.homeRestoreOffset = pos.OffsetPx
	}
	a.homeRestorePhase = homeRestoreLayout
	// The position stands as it was until the user scrolls.
	if a.homePosSaved == nil {
		a.homePosSaved = make(map[string]uistate.HomePosition)
	}
	a.homePosSaved[site] = pos
	return true
}

// stepHomeRestore moves a restored timeline to the saved offset once the
// list is laid out at the post. It reports whether the restore is still
// under way, during which the timeline loads nothing at its edges.
func (a *App) stepHomeRestore(c *ui.Context, first, last int) bool {
	switch a.homeRestorePhase {
	case homeRestoreLayout:
		if first < 0 || last < first {
			c.AnimationFrame()
			return true
		}
		if a.homeRestoreOffset > 0 {
			a.FeedScroll.Y += float32(a.homeRestoreOffset)
		}
		a.homeRestorePhase = homeRestoreSettle
		c.AnimationFrame()
		return true
	case homeRestoreSettle:
		a.homeRestorePhase = homeRestoreNone
		c.AnimationFrame()
		return true
	}
	return false
}

// noteHomePosition follows where the main timeline is read, from the top
// row and the list as the last frame laid them out, and saves it once the
// list stays still for homePositionSaveDelay.
func (a *App) noteHomePosition(c *ui.Context, first int, rowTop, listTop float32, ok bool) {
	if !ok || a.HomeActorFilter != nil || a.homeRestorePhase != homeRestoreNone || first < 0 || first >= len(a.FeedItems) {
		return
	}
	entry := a.FeedItems[first]
	pos := uistate.HomePosition{Key: entry.FeedKey, PubTs: entry.PubTs, OffsetPx: max(0, int(math.Round(float64(listTop-rowTop))))}
	site := normalizeSiteFilter(a.FeedSiteFilter)
	now := c.Now()
	if pos != a.homePosSeen || site != a.homePosSeenSite {
		a.homePosSeen, a.homePosSeenSite, a.homePosSeenAt = pos, site, now
		c.After(homePositionSaveDelay)
		return
	}
	if now.Sub(a.homePosSeenAt) >= homePositionSaveDelay {
		a.saveHomePosition(site, pos)
	} else {
		c.After(homePositionSaveDelay - now.Sub(a.homePosSeenAt))
	}
}

// flushHomePosition saves the position last seen at once: on leaving the
// main timeline or Home, switching the site filter, and quitting.
func (a *App) flushHomePosition() {
	if a.homePosSeenSite == "" || a.homePosSeen.Key == "" {
		return
	}
	a.saveHomePosition(a.homePosSeenSite, a.homePosSeen)
}

// saveHomePosition writes the position of a site filter to ui-state.json
// unless it is already there.
func (a *App) saveHomePosition(site string, pos uistate.HomePosition) {
	if a.backend == nil {
		return
	}
	if saved, ok := a.homePosSaved[site]; ok && saved == pos {
		return
	}
	if a.homePosSaved == nil {
		a.homePosSaved = make(map[string]uistate.HomePosition)
	}
	// Recorded before writing, so that a failing disk is reported once
	// rather than in every frame.
	a.homePosSaved[site] = pos
	if a.backend.GetUIState().HomePositions[site] == pos {
		return
	}
	if _, err := a.backend.MutateUIState(func(st *uistate.State) {
		if st.HomePositions == nil {
			st.HomePositions = make(map[string]uistate.HomePosition)
		}
		st.HomePositions[site] = pos
	}); err != nil {
		a.showError(fmt.Errorf("保存主页阅读位置失败：%w", err))
	}
}
