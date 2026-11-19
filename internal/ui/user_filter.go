package ui

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

// User-page list filter (DEC-046 ⑧): 全部 or 未缓存. It filters the pages
// already loaded, so switching sends no request; later pages are filtered
// the same way as they arrive.
const (
	userFilterAll      = "all"
	userFilterUncached = "uncached"
)

// uncachedFilterCache keeps the last filtering result until the list or the
// cache state changes, so frames do not query the library each time.
type uncachedFilterCache struct {
	rev  int
	list string
	keys []string // the list filtered; a reload of the same length can hold other posts
	idx  []int
}

// uncachedIndices returns the positions of keys neither cached nor being
// cached. listKey names the list (user and tab).
func (a *App) uncachedIndices(listKey string, keys []string) []int {
	fc := &a.userFilterCache
	if fc.idx != nil && fc.rev == a.cacheStateRev && fc.list == listKey && slices.Equal(fc.keys, keys) {
		return fc.idx
	}
	var ask []string
	for _, k := range keys {
		if k != "" {
			ask = append(ask, k)
		}
	}
	states := a.backend.States(ask)
	idx := make([]int, 0, len(keys))
	for i, k := range keys {
		st, ok := states[k]
		if k == "" || !ok || st.Dot == "" || st.Dot == "none" {
			idx = append(idx, i)
		}
	}
	*fc = uncachedFilterCache{rev: a.cacheStateRev, list: listKey, keys: slices.Clone(keys), idx: idx}
	return idx
}

// renderUserListFilter draws the 全部 / 未缓存 switch above a user's list.
func (a *App) renderUserListFilter(c *ui.Context) {
	cur := a.UserListFilter
	if cur != userFilterUncached {
		cur = userFilterAll
	}
	renderSegmentedPills(c, cur, []segmentedChoice{
		{Key: userFilterAll, Text: "全部", Label: "用户页筛选：全部"},
		{Key: userFilterUncached, Text: "未缓存", Label: "用户页筛选：未缓存"},
	}, func(next string) {
		a.UserListFilter = next
	})
}
