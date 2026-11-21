package ui

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// homeSubsSig tells what the subscription rows of Home were built from;
// they are built again only when it changes, never in every frame.
type homeSubsSig struct {
	site     string
	subsRev  int
	subsN    int
	statsRev int
	authorsN int
	live     uint64
	seenRev  int
	seenN    int
}

// homeSubscriptionRows lists the subscriptions in the Home panel (DEC-053
// ②): those with cached content under the site filter and those live now.
// The order is fixed when Home is entered (homeSubOrder): live first, then
// unread, then read, each by its newest cached post. During the stay a
// row only changes its dot and ring; subscriptions that come to qualify
// are added at the end, and the next entry to Home sorts again.
func (a *App) homeSubscriptionRows() []HomeUpdatedActor {
	if a.backend == nil || !a.backend.HasAnyLogin() {
		return nil
	}
	a.loadHomeBaseline()
	site := normalizeSiteFilter(a.FeedSiteFilter)
	sig := homeSubsSig{
		site:     site,
		subsRev:  a.subsRev,
		subsN:    len(a.Subscriptions),
		statsRev: a.statsRev,
		authorsN: len(a.libraryStatistics.Authors),
		live:     a.liveSignature(),
		seenRev:  a.homeSeenRev,
		seenN:    len(a.homeSeenLocal),
	}
	if a.homeSubRowsValid && sig == a.homeSubRowsSig {
		return a.homeSubRows
	}
	a.homeSubRowsSig, a.homeSubRowsValid = sig, true

	latest := make(map[string]library.ItemAuthor, len(a.libraryStatistics.Authors))
	for _, au := range a.libraryStatistics.Authors {
		normSite, normUID := library.NormalizeAuthorUID(au.Site, au.UID, au.Mid)
		if normUID == "" || au.LatestPubTs <= 0 || (site != "all" && normSite != site) {
			continue
		}
		latest[normSite+":"+normUID] = au
	}
	all := make(map[string]HomeUpdatedActor, len(a.Subscriptions))
	live := make(map[string]bool)
	var candidates []HomeUpdatedActor
	a.homeSubIndex = make(map[string]int, len(a.Subscriptions))
	for i, sub := range a.Subscriptions {
		a.homeSubIndex[sub.ID] = i
		actor := a.homeSubscriptionActor(sub, latest)
		all[actor.Key] = actor
		isLive := a.subscriptionLiveOnSite(sub, site)
		live[actor.Key] = isLive
		if actor.LatestTs > 0 || isLive {
			candidates = append(candidates, actor)
		}
	}

	if a.homeSubOrder == nil {
		a.homeSubOrder = make(map[string][]string)
	}
	order, ok := a.homeSubOrder[site]
	if !ok {
		group := func(act HomeUpdatedActor) int {
			switch {
			case live[act.Key]:
				return 0
			case a.homeSubUnread(act):
				return 1
			}
			return 2
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			gi, gj := group(candidates[i]), group(candidates[j])
			if gi != gj {
				return gi < gj
			}
			if gi == 0 {
				if ri, rj := a.homeSubUnread(candidates[i]), a.homeSubUnread(candidates[j]); ri != rj {
					return ri
				}
			}
			if candidates[i].LatestTs != candidates[j].LatestTs {
				return candidates[i].LatestTs > candidates[j].LatestTs
			}
			return candidates[i].Key < candidates[j].Key
		})
		order = make([]string, len(candidates))
		for i, act := range candidates {
			order[i] = act.Key
		}
		a.homeSubOrder[site] = order
	} else {
		// New this stay: added at the end, so that no row moves.
		listed := make(map[string]bool, len(order))
		for _, k := range order {
			listed[k] = true
		}
		for _, act := range candidates {
			if !listed[act.Key] {
				order = append(order, act.Key)
			}
		}
		a.homeSubOrder[site] = order
	}
	rows := make([]HomeUpdatedActor, 0, len(order))
	for _, k := range order {
		// A subscription removed meanwhile drops out; one that stopped
		// being live or lost its content keeps its row for the stay.
		if act, ok := all[k]; ok {
			rows = append(rows, act)
		}
	}
	a.homeSubRows = rows
	return rows
}

// homeSubscriptionActor is a subscription as a row of Home: its newest
// cached post under the site filter is the newest of its accounts'.
func (a *App) homeSubscriptionActor(sub subscription.Subscription, latest map[string]library.ItemAuthor) HomeUpdatedActor {
	refs := subscriptionAuthorRefs(sub)
	repSite, repUID := subscriptionPrimarySiteAndUID(sub)
	repMid := sub.Mid
	repFace := sub.Face
	var maxTs int64
	var latestName string
	for _, ref := range refs {
		if repUID == "" {
			repSite, repUID = ref.Site, ref.UID
		}
		if repMid == 0 && ref.Site == "bilibili" && ref.UID == repUID {
			repMid, _ = strconv.ParseInt(ref.UID, 10, 64)
		}
		if info, ok := latest[ref.Site+":"+ref.UID]; ok {
			if info.LatestPubTs > maxTs {
				maxTs = info.LatestPubTs
				latestName = info.Name
			}
			if repFace == "" && info.Face != "" {
				repFace = info.Face
			}
		}
	}
	fallbackName := repUID
	if repMid > 0 {
		fallbackName = fmt.Sprintf("UID %d", repMid)
	}
	return HomeUpdatedActor{
		Key:        "sub:" + sub.ID,
		SubID:      sub.ID,
		Site:       repSite,
		UID:        repUID,
		Mid:        repMid,
		Name:       firstNonEmptyStrUI(sub.Name, sub.Uname, latestName, fallbackName),
		Face:       repFace,
		Authors:    subscriptionAuthors(sub),
		AuthorRefs: refs,
		LatestTs:   maxTs,
	}
}

// homeSubUnread tells whether a subscription has cached content not seen:
// newer than its homeSeen entry, or than the anchor read at startup when it
// has none. With neither, as on first use, it is read.
func (a *App) homeSubUnread(actor HomeUpdatedActor) bool {
	if actor.LatestTs <= 0 {
		return false
	}
	seen, ok := a.homeSeenLocal[actor.Key]
	if !ok {
		seen = a.homeBaselineTs
	}
	return seen > 0 && actor.LatestTs > seen
}

// subscriptionLiveOnSite reports whether sub has a live account matching
// siteFilter ("all", "bilibili", or "douyin"). Unlike findSubscriptionLiveAccount,
// a live or recording account on one site never masks a live account on another.
func (a *App) subscriptionLiveOnSite(sub subscription.Subscription, siteFilter string) bool {
	filter := normalizeSiteFilter(siteFilter)
	if filter == "all" || filter == "bilibili" {
		if rm := a.findSubscriptionLiveRoom(sub); rm != nil && rm.LiveStatus == 1 {
			return true
		}
	}
	for i := range a.LiveAccounts {
		st := &a.LiveAccounts[i]
		if st.LiveStatus != 1 {
			continue
		}
		stSite := normalizeSiteIDUI(st.Site)
		if filter != "all" && stSite != filter {
			continue
		}
		for _, acc := range sub.Accounts {
			if normalizeSiteIDUI(acc.Site) == stSite && st.UID == acc.UID {
				return true
			}
		}
		if len(sub.Accounts) == 0 && normalizeSiteIDUI(sub.Site) == stSite && st.UID != "" && st.UID == subscriptionPrimaryUID(sub) {
			return true
		}
	}
	return false
}

// homeSubLive is the live ring of a row of Home.
func (a *App) homeSubLive(actor HomeUpdatedActor) bool {
	if i, ok := a.homeSubIndex[actor.SubID]; ok && i < len(a.Subscriptions) && a.Subscriptions[i].ID == actor.SubID {
		return a.subscriptionLiveOnSite(a.Subscriptions[i], a.FeedSiteFilter)
	}
	return false
}

// liveSignature sums who is live now, so that a change rebuilds the rows.
func (a *App) liveSignature() uint64 {
	var sum uint64
	for _, rm := range a.LiveRooms {
		if rm.LiveStatus == 1 {
			sum += uint64(rm.UID)*2654435761 + 1
		}
	}
	for _, st := range a.LiveAccounts {
		if st.LiveStatus == 1 {
			h := uint64(14695981039346656037)
			for _, b := range []byte(st.Site + ":" + st.UID) {
				h = (h ^ uint64(b)) * 1099511628211
			}
			sum += h
		}
	}
	return sum
}

// enterHomeSubscriptions starts a stay on Home: the subscriptions are
// sorted again when next listed.
func (a *App) enterHomeSubscriptions() {
	a.homeSubOrder = nil
	a.homeSubRowsValid = false
}

// renderHomeSubAvatar draws a subscription's avatar with its live ring and,
// when unread, an accent dot at the lower right ringed with the background.
func (a *App) renderHomeSubAvatar(c *ui.Context, actor HomeUpdatedActor, size float32, ringBg ui.Color, onClick func()) {
	live := a.homeSubLive(actor)
	// The box leaves room for the ring either way, so names line up.
	outer := size + 6
	unread := a.homeSubUnread(actor)
	ui.Box(c).Size(outer, outer).Shrink(0).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		label := ""
		if onClick != nil {
			label = "主页订阅头像：" + actor.Name
		}
		a.renderAccountAvatar(c, actor.Site, actor.Name, actor.UID, actor.Mid, size, live, label, onClick, actor.Face)
		if unread {
			dot := max(float32(8), size*0.22)
			ui.Box(c).Label("未读圆点："+actor.Name).Absolute().Right(-1).Bottom(-1).Size(dot+4, dot+4).Draw(func(p *ui.Painter, r ui.Rect) {
				p.Fill(r, ringBg, r.W/2)
				inner := ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
				p.Fill(inner, ColorAccent, inner.W/2)
			})
		}
	})
}
