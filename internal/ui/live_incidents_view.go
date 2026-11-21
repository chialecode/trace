package ui

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

const recentLiveIncidentWindow = 7 * 24 * time.Hour

type liveIncidentSession struct {
	Key         string
	RecKey      string
	ItemDir     string
	Site        string
	UID         string
	Mid         int64
	Face        string
	RoomID      string
	Uname       string
	Title       string
	QualSpec    string
	Recording   bool
	StartedAt   string
	EndedAt     string
	RefTime     time.Time
	DurationSec int
	TotalBytes  int64
	Segments    int
	Incidents   []library.LiveIncident
	LibraryItem *library.ItemMetadata
}

func (a *App) liveIncidentsNavText() string {
	if n := a.recentLiveIncidentSessionCount(); n > 0 {
		return fmt.Sprintf("录制异常（%d）", n)
	}
	return "录制异常"
}

func (a *App) recentLiveIncidentSessionCount() int {
	sessions := a.collectLiveSessions()
	cutoff := time.Now().Add(-recentLiveIncidentWindow)
	count := 0
	for _, sess := range sessions {
		if len(sess.Incidents) == 0 {
			continue
		}
		if sess.Recording || (!sess.RefTime.IsZero() && !sess.RefTime.Before(cutoff)) {
			count++
		}
	}
	return count
}

func (a *App) localLiveLibraryItems() []library.ItemMetadata {
	targetRev := a.cacheStateRev + 1
	if a.backend != nil && a.backend.HasAnyLogin() && a.liveLibItemsCacheRev != targetRev {
		items, err := a.backend.ListLibraryItems(library.FeedFilter{
			Kind:  "live",
			Sort:  "cached_at",
			Limit: 200,
		})
		if err == nil {
			a.liveLibItemsCache = items
		}
		// Cache errors too for this rev so steady 60 FPS frames don't hammer SQLite.
		a.liveLibItemsCacheRev = targetRev
	}
	seen := make(map[string]bool, len(a.liveLibItemsCache)+len(a.LibraryItems))
	var out []library.ItemMetadata
	for _, src := range [][]library.ItemMetadata{a.liveLibItemsCache, a.LibraryItems} {
		for _, it := range src {
			if it.Kind != "live" {
				continue
			}
			key := firstNonEmptyStrUI(it.ItemKey, it.ItemDir, it.RawID)
			if key != "" && seen[key] {
				continue
			}
			if key != "" {
				seen[key] = true
			}
			out = append(out, it)
		}
	}
	return out
}

func cleanSessionDirKey(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return strings.ToLower(filepath.Clean(dir))
}

func liveInfoRoomIDString(info *library.LiveRecordingInfo) string {
	if info == nil {
		return ""
	}
	if s := strings.TrimSpace(info.RoomIDStr); s != "" {
		return s
	}
	if info.RoomID > 0 {
		return strconv.FormatInt(info.RoomID, 10)
	}
	return strings.TrimSpace(info.WebRID)
}

func (a *App) resolveActiveRowIncidents(siteID, roomID, uid, itemDir string, liveIncs []library.LiveIncident) []library.LiveIncident {
	sanitized := library.SanitizeLiveIncidents(liveIncs)
	if len(sanitized) > 0 {
		return sanitized
	}
	dirKey := cleanSessionDirKey(itemDir)
	normSite := normalizeSiteIDUI(siteID)
	for _, it := range a.localLiveLibraryItems() {
		if it.Live == nil || len(it.Live.Incidents) == 0 {
			continue
		}
		if dirKey != "" && cleanSessionDirKey(it.ItemDir) == dirKey {
			return library.SanitizeLiveIncidents(it.Live.Incidents)
		}
		if it.Status == "recording" && normalizeSiteIDUI(it.Site) == normSite {
			libRoom := liveInfoRoomIDString(it.Live)
			if (roomID != "" && libRoom == roomID) || (uid != "" && it.Author.UID == uid) {
				return library.SanitizeLiveIncidents(it.Live.Incidents)
			}
		}
	}
	return nil
}

func parseSessionRefTime(startedAt, endedAt, cachedAt string, pubTs int64, incidents []library.LiveIncident) time.Time {
	for _, raw := range []string{endedAt, startedAt, cachedAt} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t
		}
	}
	for i := len(incidents) - 1; i >= 0; i-- {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(incidents[i].At)); err == nil {
			return t
		}
	}
	if pubTs > 0 {
		return time.Unix(pubTs, 0)
	}
	return time.Time{}
}

func (a *App) liveSessionsFingerprint() uint64 {
	h := newFNV64a()
	fnvWriteInt64(h, int64(a.cacheStateRev))
	fnvWriteInt64(h, int64(a.liveLibItemsCacheRev))
	fnvWriteInt64(h, int64(len(a.liveLibItemsCache)))
	fnvWriteInt64(h, int64(len(a.LibraryItems)))
	fnvWriteInt64(h, int64(len(a.LiveRooms)))
	for i := range a.LiveRooms {
		r := &a.LiveRooms[i]
		fnvWriteInt64(h, r.RoomID)
		fnvWriteInt64(h, r.UID)
		if r.Recording {
			fnvWriteInt64(h, 1)
		} else {
			fnvWriteInt64(h, 0)
		}
		fnvWriteInt64(h, int64(r.RecordSegments))
		fnvWriteInt64(h, r.RecordBytes)
		fnvWriteInt64(h, int64(r.RecordDurationSec))
		fnvWriteInt64(h, int64(len(r.Incidents)))
		fnvWriteStr(h, r.RecordItemDir)
		fnvWriteStr(h, r.Title)
	}
	fnvWriteInt64(h, int64(len(a.LiveAccounts)))
	for i := range a.LiveAccounts {
		st := &a.LiveAccounts[i]
		if normalizeSiteIDUI(st.Site) != "douyin" {
			continue
		}
		fnvWriteStr(h, st.UID)
		fnvWriteStr(h, st.RoomID)
		if st.Recording {
			fnvWriteInt64(h, 1)
		} else {
			fnvWriteInt64(h, 0)
		}
		fnvWriteInt64(h, int64(st.RecordSegments))
		fnvWriteInt64(h, st.RecordBytes)
		fnvWriteInt64(h, int64(st.RecordDurationSec))
		fnvWriteInt64(h, int64(len(st.Incidents)))
		fnvWriteStr(h, st.RecordItemDir)
		fnvWriteStr(h, st.Title)
	}
	for i := range a.LibraryItems {
		it := &a.LibraryItems[i]
		if it.Kind != "live" {
			continue
		}
		fnvWriteStr(h, it.ItemKey)
		fnvWriteStr(h, it.Status)
		if it.Live != nil {
			fnvWriteInt64(h, int64(len(it.Live.Incidents)))
		}
	}
	return h.Sum64()
}

type liveSessionInput struct {
	site        string
	recKey      string
	defaultKey  string
	itemDir     string
	uid         string
	mid         int64
	face        string
	roomID      string
	uname       string
	title       string
	qualSpec    string
	recording   bool
	durationSec int
	totalBytes  int64
	segments    int
	incidents   []library.LiveIncident
}

func (a *App) collectLiveSessions() []liveIncidentSession {
	// Ensure liveLibItemsCache is populated for current cacheStateRev before hashing.
	libItems := a.localLiveLibraryItems()
	fp := a.liveSessionsFingerprint()
	st := a.fs()
	if st.liveSessionsSet && st.liveSessionsHash == fp {
		return st.liveSessions
	}

	matchedLibIdx := make(map[int]bool)

	matchLibForState := func(siteID, roomID, uid, itemDir string, active bool, incs []library.LiveIncident) *library.ItemMetadata {
		dirKey := cleanSessionDirKey(itemDir)
		normSite := normalizeSiteIDUI(siteID)
		if dirKey != "" {
			for i := range libItems {
				if cleanSessionDirKey(libItems[i].ItemDir) == dirKey {
					matchedLibIdx[i] = true
					cp := libItems[i]
					return &cp
				}
			}
		}
		if !active && len(incs) > 0 {
			// A finished room that lost its folder: the session of the same
			// room that holds the same incidents.
			at := make(map[string]bool, len(incs))
			for _, inc := range incs {
				at[inc.Kind+"@"+strings.TrimSpace(inc.At)] = true
			}
			for i := range libItems {
				it := libItems[i]
				if matchedLibIdx[i] || it.Live == nil || normalizeSiteIDUI(it.Site) != normSite {
					continue
				}
				libRoom := liveInfoRoomIDString(it.Live)
				if !((roomID != "" && libRoom == roomID) || (uid != "" && it.Author.UID == uid) || (uid != "" && it.Author.Mid > 0 && strconv.FormatInt(it.Author.Mid, 10) == uid)) {
					continue
				}
				for _, inc := range it.Live.Incidents {
					if at[inc.Kind+"@"+strings.TrimSpace(inc.At)] {
						matchedLibIdx[i] = true
						cp := it
						return &cp
					}
				}
			}
		}
		if active {
			for i := range libItems {
				if matchedLibIdx[i] || libItems[i].Status != "recording" {
					continue
				}
				if normalizeSiteIDUI(libItems[i].Site) != normSite {
					continue
				}
				libRoom := liveInfoRoomIDString(libItems[i].Live)
				if (roomID != "" && libRoom == roomID) || (uid != "" && libItems[i].Author.UID == uid) {
					matchedLibIdx[i] = true
					cp := libItems[i]
					return &cp
				}
			}
		}
		return nil
	}

	var sessions []liveIncidentSession
	appendFromInput := func(in liveSessionInput) {
		matched := matchLibForState(in.site, in.roomID, in.uid, in.itemDir, in.recording, in.incidents)
		if !in.recording && matched == nil && len(in.incidents) == 0 {
			return
		}
		if !in.recording && matched != nil {
			if matched.Live == nil && len(in.incidents) > 0 {
				matched.Live = &library.LiveRecordingInfo{Incidents: library.SanitizeLiveIncidents(in.incidents)}
			} else if matched.Live != nil && len(matched.Live.Incidents) == 0 && len(in.incidents) > 0 {
				matched.Live.Incidents = library.SanitizeLiveIncidents(in.incidents)
			}
			sessions = append(sessions, buildSessionFromLibraryItem(*matched))
			return
		}

		incs := library.SanitizeLiveIncidents(in.incidents)
		startedAt := ""
		key := in.defaultKey
		if matched != nil {
			if len(incs) == 0 && matched.Live != nil {
				incs = library.SanitizeLiveIncidents(matched.Live.Incidents)
			}
			if matched.Live != nil {
				startedAt = matched.Live.StartedAt
			}
			if matched.ItemKey != "" {
				key = matched.ItemKey
			}
		}
		if startedAt == "" && len(incs) > 0 {
			startedAt = incs[0].At
		}
		refTime := parseSessionRefTime(startedAt, "", "", 0, incs)
		if refTime.IsZero() {
			refTime = time.Now()
		}
		sessions = append(sessions, liveIncidentSession{
			Key:         key,
			RecKey:      in.recKey,
			ItemDir:     in.itemDir,
			Site:        in.site,
			UID:         in.uid,
			Mid:         in.mid,
			Face:        in.face,
			RoomID:      in.roomID,
			Uname:       in.uname,
			Title:       in.title,
			QualSpec:    in.qualSpec,
			Recording:   in.recording,
			StartedAt:   startedAt,
			RefTime:     refTime,
			DurationSec: in.durationSec,
			TotalBytes:  in.totalBytes,
			Segments:    max(1, in.segments),
			Incidents:   incs,
			LibraryItem: matched,
		})
	}

	for _, room := range a.LiveRooms {
		roomDec := strconv.FormatInt(room.RoomID, 10)
		uidStr := strconv.FormatInt(room.UID, 10)
		recKey := fmt.Sprintf("bilibili:live:%d", room.RoomID)
		appendFromInput(liveSessionInput{
			site:        "bilibili",
			recKey:      recKey,
			defaultKey:  recKey,
			itemDir:     room.RecordItemDir,
			uid:         uidStr,
			mid:         room.UID,
			roomID:      roomDec,
			uname:       firstNonEmptyStrUI(room.Uname, fmt.Sprintf("UID %d", room.UID)),
			title:       firstNonEmptyStrUI(room.Title, "哔哩哔哩直播"),
			qualSpec:    bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec),
			recording:   room.Recording,
			durationSec: int(room.RecordDurationSec),
			totalBytes:  room.RecordBytes,
			segments:    room.RecordSegments,
			incidents:   room.Incidents,
		})
	}

	for _, acct := range a.LiveAccounts {
		if normalizeSiteIDUI(acct.Site) != "douyin" {
			continue
		}
		recKey := firstNonEmptyStrUI(acct.RecordingKey, acct.AccountKey, acct.RoomID, acct.UID)
		appendFromInput(liveSessionInput{
			site:        "douyin",
			recKey:      recKey,
			defaultKey:  recKey,
			itemDir:     acct.RecordItemDir,
			uid:         acct.UID,
			face:        acct.Face,
			roomID:      acct.RoomID,
			uname:       firstNonEmptyStrUI(acct.Uname, acct.UID, "抖音主播"),
			title:       firstNonEmptyStrUI(acct.Title, "抖音直播"),
			qualSpec:    formatLiveAccountQualitySpec(acct),
			recording:   acct.Recording,
			durationSec: int(acct.RecordDurationSec),
			totalBytes:  acct.RecordBytes,
			segments:    acct.RecordSegments,
			incidents:   acct.Incidents,
		})
	}

	for i, it := range libItems {
		if matchedLibIdx[i] {
			continue
		}
		sessions = append(sessions, buildSessionFromLibraryItem(it))
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Recording != sessions[j].Recording {
			return sessions[i].Recording
		}
		if !sessions[i].RefTime.Equal(sessions[j].RefTime) {
			return sessions[i].RefTime.After(sessions[j].RefTime)
		}
		return sessions[i].Key > sessions[j].Key
	})

	st.liveSessionsHash = fp
	st.liveSessions = sessions
	st.liveSessionsSet = true
	return sessions
}

func buildSessionFromLibraryItem(it library.ItemMetadata) liveIncidentSession {
	siteID := normalizeSiteIDUI(it.Site)
	var startedAt, endedAt, roomID, qualSpec string
	var incs []library.LiveIncident
	var segCount int
	if it.Live != nil {
		startedAt = it.Live.StartedAt
		endedAt = it.Live.EndedAt
		roomID = liveInfoRoomIDString(it.Live)
		segCount = len(it.Live.Segments)
		incs = library.SanitizeLiveIncidents(it.Live.Incidents)
		qualSpec = formatLiveRecordingSpec(it.Live, "")
	}
	if segCount <= 0 {
		for _, m := range it.Media {
			if m.Role == "live_segment" || m.Role == "video" {
				segCount++
			}
		}
	}
	if startedAt == "" && it.PubTs > 0 {
		startedAt = time.Unix(it.PubTs, 0).UTC().Format(time.RFC3339)
	}
	refTime := parseSessionRefTime(startedAt, endedAt, it.CachedAt, it.PubTs, incs)
	uid := strings.TrimSpace(it.Author.UID)
	if uid == "" && it.Author.Mid > 0 {
		uid = strconv.FormatInt(it.Author.Mid, 10)
	}
	cp := it
	return liveIncidentSession{
		Key:         firstNonEmptyStrUI(it.ItemKey, it.RawID),
		ItemDir:     it.ItemDir,
		Site:        siteID,
		UID:         uid,
		Mid:         it.Author.Mid,
		Face:        it.Author.Face,
		RoomID:      roomID,
		Uname:       firstNonEmptyStrUI(it.Author.Name, uid, "主播"),
		Title:       firstNonEmptyStrUI(it.Title, "直播录像"),
		QualSpec:    qualSpec,
		Recording:   false,
		StartedAt:   startedAt,
		EndedAt:     endedAt,
		RefTime:     refTime,
		DurationSec: it.Duration,
		TotalBytes:  it.TotalBytes,
		Segments:    segCount,
		Incidents:   incs,
		LibraryItem: &cp,
	}
}

func renderLiveRowIncidentPill(c *ui.Context, key string, count int, onClick func()) {
	if count <= 0 {
		return
	}
	btn := ui.ButtonBase(c).
		Label("录制行异常："+key).
		Padding(4, 10).
		Radius(999).
		Background(ColorWarnBg).
		Border(1, ColorDotYellow).
		Shrink(0)
	if btn.Hovered() {
		btn.Background(ColorLiveBg).Border(1, ColorLive)
	}
	btn.Children(func() {
		ui.Text(c, fmt.Sprintf("⚠ %d 处异常", count)).
			FontSize(12).
			FontWeight(600).
			TextColor(ColorLive).
			SingleLine()
	})
	if btn.Clicked() && onClick != nil {
		onClick()
	}
}

func (a *App) openLiveIncidentLibraryItem(item library.ItemMetadata) {
	a.closeLivePreview()
	if a.backend != nil && item.ItemKey != "" {
		if fresh, ok, err := a.backend.GetLibraryItem(item.ItemKey); err == nil && ok {
			item = fresh
		}
	}
	a.OpenLibraryItemDetail(item)
}

func (a *App) renderRecentFinishedLiveSection(c *ui.Context, activeRows []activeLiveRow) {
	sessions := a.collectLiveSessions()
	cutoff := time.Now().Add(-recentLiveIncidentWindow)
	activeDirs := make(map[string]bool, len(activeRows))
	activeKeys := make(map[string]bool, len(activeRows))
	for _, r := range activeRows {
		if d := cleanSessionDirKey(r.ItemDir); d != "" {
			activeDirs[d] = true
		}
		if r.RecKey != "" {
			activeKeys[r.RecKey] = true
		}
	}

	dismissed := a.dismissedLiveSessionKeys()
	var recent []liveIncidentSession
	for _, sess := range sessions {
		if sess.Recording || dismissed[sess.Key] {
			continue
		}
		if d := cleanSessionDirKey(sess.ItemDir); d != "" && activeDirs[d] {
			continue
		}
		if activeKeys[sess.Key] || (sess.RecKey != "" && activeKeys[sess.RecKey]) {
			continue
		}
		if !sess.RefTime.IsZero() && sess.RefTime.Before(cutoff) {
			continue
		}
		recent = append(recent, sess)
		if len(recent) >= 20 {
			break
		}
	}
	if len(recent) == 0 {
		return
	}

	ui.Column(c).FillWidth().Gap(8).Padding(4, 0, 0, 0).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
			ui.Text(c, "最近结束的录制").FontSize(13).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
			if OutlinePillButton(c, fmt.Sprintf("全部移除（%d）", len(recent)), "移除全部最近结束的录制").Clicked() {
				keys := make([]string, 0, len(recent))
				for _, sess := range recent {
					keys = append(keys, sess.Key)
				}
				a.dismissLiveSessions(keys...)
			}
		})
		for _, item := range recent {
			sess := item
			ui.Row(c).
				Label("最近结束录制行："+sess.Key).
				FillWidth().
				Padding(11, 14).
				Radius(14).
				Background(ColorBgMain).
				Border(1, ColorBorder).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(12).
				ClipX().
				Children(func() {
					ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
						a.renderAccountAvatar(c, sess.Site, sess.Uname, sess.UID, sess.Mid, 36, false, "", nil, sess.Face)
						ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
								ui.Text(c, sess.Uname+" · "+sess.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
								renderSiteSmallBadgeWithDot(c, sess.Site, "最近录制站点："+sess.Key)
								if sess.QualSpec != "" {
									ui.Text(c, "· "+sess.QualSpec).Shrink(0).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								}
							})
							metaParts := []string{"已结束"}
							if st := formatIncidentSessionStart(sess.StartedAt, sess.RefTime); st != "" {
								metaParts = append(metaParts, "开始 "+st)
							}
							if sess.DurationSec > 0 {
								metaParts = append(metaParts, "时长 "+FormatDuration(sess.DurationSec))
							}
							if sess.TotalBytes > 0 {
								metaParts = append(metaParts, FormatBytes(sess.TotalBytes))
							}
							ui.Text(c, strings.Join(metaParts, " · ")).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						})
					})

					ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
						if len(sess.Incidents) > 0 {
							renderLiveRowIncidentPill(c, sess.Key, len(sess.Incidents), func() {
								a.selectTasksIncidents(sess.Key)
							})
						}
						if sess.LibraryItem != nil {
							if OutlinePillButton(c, "查看录像", "直播录制查看录像："+sess.Key).Clicked() {
								a.openLiveIncidentLibraryItem(*sess.LibraryItem)
							}
						}
						if OutlinePillButton(c, "移除", "移除最近结束的录制："+sess.Key).Clicked() {
							a.dismissLiveSessions(sess.Key)
						}
					})
				})
		}
	})
}

// dismissedLiveSessionKeys are the finished recordings removed from
// "最近结束的录制" (ui-state.json; the recordings stay in the media library).
func (a *App) dismissedLiveSessionKeys() map[string]bool {
	if a.dismissedLive == nil {
		a.dismissedLive = map[string]bool{}
		if a.backend != nil {
			for k := range a.backend.GetUIState().DismissedLiveSessions {
				a.dismissedLive[k] = true
			}
		}
	}
	return a.dismissedLive
}

func (a *App) dismissLiveSessions(keys ...string) {
	now := time.Now().UTC().Format(time.RFC3339)
	var add []string
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			add = append(add, k)
		}
	}
	if len(add) == 0 {
		return
	}
	if _, err := a.backend.MutateUIState(func(st *uistate.State) {
		if st.DismissedLiveSessions == nil {
			st.DismissedLiveSessions = map[string]string{}
		}
		for _, k := range add {
			st.DismissedLiveSessions[k] = now
		}
	}); err != nil {
		a.showError(fmt.Errorf("保存任务列表失败：%w", err))
		return
	}
	dismissed := a.dismissedLiveSessionKeys()
	for _, k := range add {
		dismissed[k] = true
	}
}

// clearLiveSessionIncidents deletes a finished session's incident records
// (Tasks › 录制异常 "删除记录"); the recording is kept.
func (a *App) clearLiveSessionIncidents(sess liveIncidentSession) {
	itemKey := ""
	if sess.LibraryItem != nil {
		itemKey = sess.LibraryItem.ItemKey
	}
	err := a.backend.ClearLiveIncidents(itemKey, sess.Site, sess.RoomID, sess.UID)
	if err != nil {
		a.showError(err)
		return
	}
	a.liveLibItemsCacheRev = 0
	a.cacheStateRev++
	a.RefreshData()
}

func matchesIncidentSessionFocus(sess liveIncidentSession, focus string) bool {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return false
	}
	if sess.Key == focus || sess.RecKey == focus {
		return true
	}
	if sess.ItemDir != "" && cleanSessionDirKey(sess.ItemDir) == cleanSessionDirKey(focus) {
		return true
	}
	return false
}

func (a *App) renderTasksIncidentsView(c *ui.Context) {
	allSessions := a.collectLiveSessions()
	cutoff := time.Now().Add(-recentLiveIncidentWindow)
	var recentSessions, olderSessions []liveIncidentSession
	for _, sess := range allSessions {
		if len(sess.Incidents) == 0 {
			continue
		}
		if sess.Recording || (!sess.RefTime.IsZero() && !sess.RefTime.Before(cutoff)) {
			recentSessions = append(recentSessions, sess)
		} else {
			olderSessions = append(olderSessions, sess)
		}
	}

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			if len(recentSessions) == 0 && len(olderSessions) == 0 {
				ui.Column(c).Label("录制异常空状态").FillWidth().Padding(48, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "最近没有录制异常").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "录制过程中如果遇到断流重连、分片丢失、下载过慢、空间不足或收尾转换失败，会按场次记录在这里。").
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
				return
			}

			if len(recentSessions) == 0 && len(olderSessions) > 0 {
				ui.Text(c, "最近 7 天没有录制异常").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			}
			for _, item := range recentSessions {
				a.renderLiveIncidentSessionCard(c, item)
			}
			if len(olderSessions) > 0 {
				ui.Text(c, fmt.Sprintf("更早的记录（7 天前，共 %d 场）", len(olderSessions))).
					FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
				for _, item := range olderSessions {
					a.renderLiveIncidentSessionCard(c, item)
				}
			}
		})
	})
}

func (a *App) renderLiveIncidentSessionCard(c *ui.Context, sess liveIncidentSession) {
	focused := matchesIncidentSessionFocus(sess, a.SelectedLiveIncidentKey)
	borderCol := ColorBorder
	if focused {
		borderCol = ColorAccent
	} else if sess.Recording {
		borderCol = ColorLive
	}

	ui.Column(c).
		Label("录制异常场次："+sess.Key).
		FillWidth().
		Padding(14).
		Radius(16).
		Background(ColorBgMain).
		Border(1, borderCol).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
					a.renderAccountAvatar(c, sess.Site, sess.Uname, sess.UID, sess.Mid, 38, sess.Recording, "", nil, sess.Face)
					ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							if sess.Recording {
								ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
									ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
								})
							} else {
								ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorBgSecondary).Children(func() {
									ui.Text(c, "已结束").FontSize(11).FontWeight(500).TextColor(ColorTextMuted).SingleLine()
								})
							}
							ui.Text(c, sess.Uname+" · "+sess.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
							renderSiteSmallBadgeWithDot(c, sess.Site, "异常场次站点："+sess.Key)
							ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorWarnBg).Children(func() {
								ui.Text(c, fmt.Sprintf("⚠ %d 处异常", len(sess.Incidents))).
									FontSize(11).FontWeight(600).TextColor(ColorLive).SingleLine()
							})
						})
						var subParts []string
						if st := formatIncidentSessionStart(sess.StartedAt, sess.RefTime); st != "" {
							subParts = append(subParts, "开始时间 "+st)
						}
						if sess.RoomID != "" {
							subParts = append(subParts, "直播间 "+sess.RoomID)
						}
						if sess.QualSpec != "" {
							subParts = append(subParts, sess.QualSpec)
						}
						ui.Text(c, strings.Join(subParts, " · ")).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
				})

				ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(8).Children(func() {
					if sess.Recording {
						if OutlinePillButton(c, "查看正在录制", "查看异常正在录制："+sess.Key).Clicked() {
							a.selectTasksLive()
						}
					} else {
						if sess.LibraryItem != nil {
							if OutlinePillButton(c, "查看录像详情", "打开异常录像详情："+sess.Key).Clicked() {
								a.openLiveIncidentLibraryItem(*sess.LibraryItem)
							}
						}
						if DangerOutlinePillButton(c, "删除记录", "删除录制异常记录："+sess.Key).Clicked() {
							a.clearLiveSessionIncidents(sess)
						}
					}
				})
			})

			ui.Column(c).
				FillWidth().
				BorderWidth(1, 0, 0, 0).
				BorderColor(ColorBorder).
				Padding(8, 0, 0, 0).
				Gap(6).
				ClipX().
				Children(func() {
					for idx, rawInc := range sess.Incidents {
						inc := rawInc
						ui.Row(c).
							Label(fmt.Sprintf("异常条目：%s#%d", sess.Key, idx)).
							FillWidth().
							Padding(6, 10).
							Radius(10).
							Background(ColorBgSecondary).
							AlignItems(ui.Center).
							Gap(10).
							ClipX().
							Children(func() {
								clockText := firstNonEmptyStrUI(formatIncidentTimeText(inc.At), "--:--:--")
								ui.Text(c, clockText).
									FontSize(12).
									FontWeight(500).
									TextColor(ColorTextMuted).
									Shrink(0).
									SingleLine()
								tagText, tagColor, tagBg := incidentKindBadgeStyle(inc)
								ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(tagBg).Children(func() {
									ui.Text(c, tagText).FontSize(11).FontWeight(600).TextColor(tagColor).SingleLine()
								})
								ui.Text(c, formatIncidentDetailText(inc)).
									FontSize(13).
									TextColor(ColorTextMain).
									Grow(1).
									MaxLines(2)
							})
					}
				})
		})
}

func formatIncidentSessionStart(startedAt string, fallback time.Time) string {
	if raw := strings.TrimSpace(startedAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
	}
	if !fallback.IsZero() {
		return fallback.Local().Format("2006-01-02 15:04:05")
	}
	return ""
}

func formatIncidentTimeText(raw string) string {
	return formatRFC3339Clock(raw, true)
}

func formatIncidentSeconds(sec float64) string {
	if sec <= 0 {
		return "0 秒"
	}
	if sec < 120 {
		rounded := math.Round(sec*10) / 10
		if math.Abs(rounded-math.Round(rounded)) < 0.05 {
			return fmt.Sprintf("%d 秒", int(math.Round(rounded)))
		}
		return fmt.Sprintf("%.1f 秒", rounded)
	}
	totalSec := int(math.Round(sec))
	mins := totalSec / 60
	rem := totalSec % 60
	if rem == 0 {
		return fmt.Sprintf("%d 分钟", mins)
	}
	return fmt.Sprintf("%d 分 %d 秒", mins, rem)
}

func incidentKindBadgeStyle(inc library.LiveIncident) (text string, fg ui.Color, bg ui.Color) {
	switch inc.Kind {
	case library.LiveIncidentReconnect:
		return "断流重连", ColorLive, ColorWarnBg
	case library.LiveIncidentLostSegments:
		if inc.LostSegments > 0 {
			return "分片丢失", ColorLive, ColorWarnBg
		}
		return "断流空档", ColorLive, ColorWarnBg
	case library.LiveIncidentSlow:
		return "下载过慢", ColorInfoBlue, ColorInfoBlueBg
	case library.LiveIncidentDisk:
		return "空间不足", ColorError, ColorErrorBg
	case library.LiveIncidentConvertFailed:
		return "收尾转换失败", ColorError, ColorErrorBg
	default:
		return "录制异常", ColorLive, ColorWarnBg
	}
}

func formatIncidentDetailText(inc library.LiveIncident) string {
	var desc string
	switch inc.Kind {
	case library.LiveIncidentReconnect:
		if inc.DurationSec > 0 {
			desc = fmt.Sprintf("断流后重新取流，恢复用时 %s", formatIncidentSeconds(inc.DurationSec))
		} else {
			desc = "断流后重新取流"
		}
	case library.LiveIncidentLostSegments:
		switch {
		case inc.LostSegments > 0 && inc.DurationSec > 0:
			desc = fmt.Sprintf("媒体序号跳号丢失 %d 段，估算影响约 %s", inc.LostSegments, formatIncidentSeconds(inc.DurationSec))
		case inc.LostSegments > 0:
			desc = fmt.Sprintf("媒体序号跳号丢失 %d 段", inc.LostSegments)
		case inc.DurationSec > 0:
			desc = fmt.Sprintf("断流到重连之间空档约 %s", formatIncidentSeconds(inc.DurationSec))
		default:
			desc = "直播流出现分片丢失或断流空档"
		}
	case library.LiveIncidentSlow:
		if inc.DurationSec > 0 {
			desc = fmt.Sprintf("下载速度低于流码率，持续 %s", formatIncidentSeconds(inc.DurationSec))
		} else {
			desc = "下载速度持续低于流码率"
		}
	case library.LiveIncidentDisk:
		desc = firstNonEmptyStrUI(inc.Reason, "保存位置可用磁盘空间不足，录制已停止")
	case library.LiveIncidentConvertFailed:
		reason := firstNonEmptyStrUI(inc.Reason, "录制分段收尾转换失败")
		if inc.Part != "" && !strings.Contains(reason, inc.Part) {
			desc = inc.Part + "：" + reason
		} else {
			desc = reason
		}
	default:
		desc = firstNonEmptyStrUI(inc.Reason, "录制异常")
	}
	if inc.Count > 1 {
		desc += fmt.Sprintf("（累计 %d 次）", inc.Count)
	}
	return desc
}
