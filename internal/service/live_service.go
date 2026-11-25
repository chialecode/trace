package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
)

// ListLiveRooms returns the current live status and recording state of all tracked rooms.
func (b *Backend) ListLiveRooms() []live.RoomState {
	if b.requireAnyLogin() != nil {
		return nil
	}
	return b.live.ListRooms()
}

// ListLiveAccounts returns unified live & recording states across all tracked rooms/accounts.
func (b *Backend) ListLiveAccounts() []LiveAccountState {
	if b.requireAnyLogin() != nil {
		return nil
	}
	rooms := b.live.ListRooms()
	out := make([]LiveAccountState, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, mapRoomToAccountState(r))
	}
	return out
}

// LiveStatus refreshes live status for visible Bilibili accounts, with a 60s cache.
// For subscribed accounts with auto-record enabled, the underlying live manager
// may start recording automatically; unsubscribed accounts are read-only.
func (b *Backend) LiveStatus(ctx context.Context, uids []int64) ([]live.RoomState, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	return b.live.RefreshForAccounts(ctx, uids)
}

// LiveStatuses refreshes live status for a mixed slice of "<site>:<uid>" account keys.
func (b *Backend) LiveStatuses(ctx context.Context, accountKeys []string) ([]LiveAccountState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	var biliUIDs []int64
	var douyinUIDs []string
	for _, rawKey := range accountKeys {
		siteID, uid, err := site.ParseAccountKey(rawKey)
		if err != nil {
			continue
		}
		switch siteID {
		case site.Bilibili:
			if mid, mErr := strconv.ParseInt(uid, 10, 64); mErr == nil && mid > 0 {
				biliUIDs = append(biliUIDs, mid)
			}
		case site.Douyin:
			douyinUIDs = append(douyinUIDs, uid)
		case site.Weibo:
			// Weibo live status polling is intentionally excluded (0 requests).
		case site.X:
			// X broadcasts & Spaces live status polling is intentionally excluded (0 requests).
		}
	}

	var out []LiveAccountState
	var firstErr error

	if len(biliUIDs) > 0 {
		if err := b.requireSiteLogin(site.Bilibili); err != nil {
			firstErr = err
		} else if rooms, err := b.live.RefreshForAccounts(ctx, biliUIDs); err != nil {
			firstErr = err
		} else {
			wantUIDs := make(map[int64]struct{}, len(biliUIDs))
			for _, uid := range biliUIDs {
				wantUIDs[uid] = struct{}{}
			}
			for _, r := range rooms {
				if r.Site != "" && !strings.EqualFold(r.Site, string(site.Bilibili)) {
					continue
				}
				if _, ok := wantUIDs[r.UID]; !ok {
					continue
				}
				out = append(out, mapRoomToAccountState(r))
			}
		}
	}

	if len(douyinUIDs) > 0 {
		if err := b.requireSiteLogin(site.Douyin); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, secUID := range douyinUIDs {
				cachedWebRID := b.lookupAccountWebRID(secUID)
				rs, err := b.live.CheckAccountLiveStatus(ctx, site.Douyin, secUID, cachedWebRID, false)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				out = append(out, mapRoomToAccountState(rs))
			}
		}
	}

	return out, firstErr
}

// PollLiveNow triggers an immediate live status batch poll.
func (b *Backend) PollLiveNow(ctx context.Context) ([]live.RoomState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.live.PollNow(ctx)
}

// OpenLiveRoomInBrowser opens a Bilibili live room in the external browser via Desktop.
func (b *Backend) OpenLiveRoomInBrowser(roomID int64) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if roomID <= 0 {
		return fmt.Errorf("service: invalid roomID %d", roomID)
	}
	return b.desktop.OpenExternalURL(fmt.Sprintf("https://live.bilibili.com/%d", roomID))
}

// OpenLiveInBrowser opens a Bilibili roomID, Douyin web_rid, or Weibo live link/ID in the external system browser.
func (b *Backend) OpenLiveInBrowser(siteID, roomOrWebRID string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return err
	}
	clean := strings.TrimSpace(roomOrWebRID)
	if clean == "" || clean == "0" {
		return &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
	}
	switch id {
	case site.Bilibili:
		for _, r := range clean {
			if r < '0' || r > '9' {
				return &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
			}
		}
		return b.desktop.OpenExternalURL("https://live.bilibili.com/" + clean)
	case site.Douyin:
		for _, r := range clean {
			if r < '0' || r > '9' {
				return &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
			}
		}
		return b.desktop.OpenExternalURL("https://live.douyin.com/" + clean)
	case site.Weibo:
		if safe := safeBrowserURL(clean); safe != "" {
			return b.desktop.OpenExternalURL(safe)
		}
		for _, r := range clean {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == ':' || r == '_' || r == '-') {
				return &site.Error{Site: site.Weibo, Kind: site.ErrKindBadRequest, Message: "无效的微博直播间号"}
			}
		}
		return b.desktop.OpenExternalURL("https://weibo.com/l/wblive/p/show/" + clean)
	case site.X:
		if safe := safeBrowserURL(clean); safe != "" {
			return b.desktop.OpenExternalURL(safe)
		}
		for _, r := range clean {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '-') {
				return &site.Error{Site: site.X, Kind: site.ErrKindBadRequest, Message: "无效的 X 直播或 Space 链接"}
			}
		}
		return b.desktop.OpenExternalURL("https://x.com/i/broadcasts/" + clean)
	default:
		return &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

// StartLiveRecording starts recording a Bilibili live room.
func (b *Backend) StartLiveRecording(ctx context.Context, roomID int64) (live.RoomState, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return live.RoomState{}, err
	}
	return b.live.StartRecording(ctx, roomID)
}

// StartLiveRecordingByAccount starts live recording for a "<site>:<uid>" creator account.
func (b *Backend) StartLiveRecordingByAccount(ctx context.Context, accountKey string) (LiveAccountState, error) {
	siteID, uid, err := site.ParseAccountKey(accountKey)
	if err != nil {
		return LiveAccountState{}, err
	}
	if siteID == site.Weibo {
		return LiveAccountState{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     live.WeiboLiveUnsupportedMessage,
		}
	}
	if siteID == site.X {
		return LiveAccountState{}, &site.Error{
			Site:        site.X,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     live.XLiveUnsupportedMessage,
		}
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return LiveAccountState{}, err
	}
	switch siteID {
	case site.Bilibili:
		mid, mErr := strconv.ParseInt(uid, 10, 64)
		if mErr != nil || mid <= 0 {
			return LiveAccountState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的哔哩哔哩账号 UID"}
		}
		rooms, rErr := b.live.RefreshForAccounts(ctx, []int64{mid})
		if rErr != nil {
			return LiveAccountState{}, rErr
		}
		var targetRoomID int64
		for _, r := range rooms {
			if (r.Site == "" || strings.EqualFold(r.Site, string(site.Bilibili))) && r.UID == mid && r.RoomID > 0 {
				targetRoomID = r.RoomID
				break
			}
		}
		if targetRoomID <= 0 {
			return LiveAccountState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindUnavailable, Message: "该账号未开通直播间"}
		}
		rs, sErr := b.live.StartRecording(ctx, targetRoomID)
		return mapRoomToAccountState(rs), sErr
	case site.Douyin:
		return b.startDouyinLiveRecording(ctx, uid, b.lookupAccountWebRID(uid))
	default:
		return LiveAccountState{}, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

// StartLiveRecordingByRoom starts live recording given an explicit site and roomID/webRID.
func (b *Backend) StartLiveRecordingByRoom(ctx context.Context, siteID, roomOrWebRID string) (LiveAccountState, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return LiveAccountState{}, err
	}
	if id == site.Weibo {
		return LiveAccountState{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     live.WeiboLiveUnsupportedMessage,
		}
	}
	if id == site.X {
		return LiveAccountState{}, &site.Error{
			Site:        site.X,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     live.XLiveUnsupportedMessage,
		}
	}
	if err := b.requireSiteLogin(id); err != nil {
		return LiveAccountState{}, err
	}
	switch id {
	case site.Bilibili:
		roomID, pErr := strconv.ParseInt(strings.TrimSpace(roomOrWebRID), 10, 64)
		if pErr != nil || roomID <= 0 {
			return LiveAccountState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
		}
		rs, sErr := b.live.StartRecording(ctx, roomID)
		return mapRoomToAccountState(rs), sErr
	case site.Douyin:
		return b.startDouyinLiveRecording(ctx, "", strings.TrimSpace(roomOrWebRID))
	default:
		return LiveAccountState{}, &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

// StopLiveRecording stops an active Bilibili live room recording and finalizes item.json.
func (b *Backend) StopLiveRecording(roomID int64) (live.RoomState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return live.RoomState{}, err
	}
	return b.live.StopRecording(roomID)
}

// StopLiveRecordingByKey stops an active live recording by canonical recording key ("<site>:live:<room_id>" or decimal roomID).
func (b *Backend) StopLiveRecordingByKey(recKey string) (LiveAccountState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return LiveAccountState{}, err
	}
	rs, err := b.live.StopRecordingByKey(strings.TrimSpace(recKey))
	return mapRoomToAccountState(rs), err
}

// LiveRecordingPreview returns an in-memory snapshot of an active or recent live recording session for preview playback.
func (b *Backend) LiveRecordingPreview(recKey string) (LiveRecordingPreviewInfo, bool) {
	if b.requireAnyLogin() != nil || b.live == nil {
		return LiveRecordingPreviewInfo{}, false
	}
	target := strings.TrimSpace(recKey)
	if target == "" {
		return LiveRecordingPreviewInfo{}, false
	}
	if p, ok := b.live.RecordingPreview(target); ok {
		roomID := strings.TrimSpace(p.RoomIDStr)
		if roomID == "" && p.RoomID > 0 {
			roomID = strconv.FormatInt(p.RoomID, 10)
		}
		return LiveRecordingPreviewInfo{
			RecordingKey:     p.RecordingKey,
			Site:             p.Site,
			AccountUID:       p.AccountUID,
			RoomID:           roomID,
			WebRID:           p.WebRID,
			Uname:            p.Uname,
			Title:            p.Title,
			Codec:            p.Codec,
			Quality:          p.Quality,
			QualityName:      p.QualityName,
			Transport:        p.Transport,
			AudioOnly:        p.AudioOnly,
			Recording:        p.Recording,
			PartPath:         p.PartPath,
			PartIndex:        p.PartIndex,
			PartBytes:        p.PartBytes,
			PartDurationSec:  p.PartDurationSec,
			PartReady:        p.PartReady,
			PreviewSupported: p.PreviewSupported,
			PreviewReason:    p.PreviewReason,
			ItemDir:          p.ItemDir,
			DurationSec:      p.DurationSec,
			SpeedBps:         p.SpeedBps,
			BytesWritten:     p.BytesWritten,
			Segments:         p.Segments,
			Incidents:        append([]library.LiveIncident(nil), p.Incidents...),
			StopReason:       p.StopReason,
			LastError:        p.LastError,
		}, true
	}
	for _, st := range b.ListLiveAccounts() {
		if st.RecordingKey != target && st.AccountKey != target && st.RoomID != target && st.UID != target && st.WebRID != target {
			continue
		}
		partPath := st.RecordPartPath
		if partPath == "" && st.Recording && st.RecordItemDir != "" {
			partPath = resolveRecordingPartPath(st.RecordItemDir)
		}
		partBytes := st.RecordPartBytes
		if partBytes <= 0 && partPath != "" {
			if info, err := os.Stat(partPath); err == nil {
				partBytes = info.Size()
			}
		}
		partReady := st.RecordPartReady || partBytes > 0
		supported := st.PreviewSupported || (st.Recording && partPath != "" && !st.AudioOnly)
		return LiveRecordingPreviewInfo{
			RecordingKey:     st.RecordingKey,
			Site:             st.Site,
			AccountUID:       st.UID,
			RoomID:           st.RoomID,
			WebRID:           st.WebRID,
			Uname:            st.Uname,
			Title:            st.Title,
			Codec:            st.Codec,
			QualityName:      st.QualityName,
			Transport:        st.Transport,
			AudioOnly:        st.AudioOnly,
			Recording:        st.Recording,
			PartPath:         partPath,
			PartIndex:        st.RecordPartIndex,
			PartBytes:        partBytes,
			PartReady:        partReady,
			PreviewSupported: supported,
			PreviewReason:    st.PreviewReason,
			ItemDir:          st.RecordItemDir,
			DurationSec:      st.RecordDurationSec,
			SpeedBps:         st.RecordSpeedBps,
			BytesWritten:     st.RecordBytes,
			Segments:         st.RecordSegments,
			Incidents:        append([]library.LiveIncident(nil), st.Incidents...),
			StopReason:       st.StopReason,
			LastError:        st.LastError,
		}, true
	}
	return LiveRecordingPreviewInfo{}, false
}

// ClearLiveIncidents deletes the recorded incidents of a finished live session
// (Tasks › 录制异常): the item's live.incidents when itemKey names a finished
// recording, and the in-memory list of matching rooms that are not recording.
// The recording files are kept. A session still recording is refused.
func (b *Backend) ClearLiveIncidents(itemKey, siteID, roomID, uid string) error {
	itemKey = strings.TrimSpace(itemKey)
	changed := false
	if itemKey != "" && b.lib != nil {
		item, ok, err := b.lib.GetItem(itemKey)
		if err != nil {
			return err
		}
		if ok && item.Kind == "live" {
			if item.Status == "recording" {
				return &site.Error{Kind: site.ErrKindBadRequest, Message: "这场直播还在录制，结束后才能删除异常记录"}
			}
			if item.Live != nil && len(item.Live.Incidents) > 0 {
				item.Live.Incidents = nil
				if _, err := b.lib.SaveLiveItem(item.ItemDir, item); err != nil {
					return err
				}
				changed = true
			}
		}
	}
	if b.live != nil && b.live.ClearIdleIncidents(siteID, roomID, uid) {
		changed = true
	}
	if changed && b.events != nil {
		b.events.Publish(EventLibraryChanged, "incidents_cleared")
	}
	return nil
}

func (b *Backend) startDouyinLiveRecording(ctx context.Context, uid string, webRID string) (LiveAccountState, error) {
	rs, err := b.live.StartDouyinRecording(ctx, uid, webRID)
	return mapRoomToAccountState(rs), err
}

func (b *Backend) syncDouyinAccountWebRID(secUID, webRID string) {
	secUID = strings.TrimSpace(secUID)
	webRID = strings.TrimSpace(webRID)
	if secUID == "" || webRID == "" || b.subs == nil {
		return
	}
	_ = b.subs.UpdateAccountWebRID(string(site.Douyin), secUID, webRID)
}

func (b *Backend) lookupAccountWebRID(secUID string) string {
	secUID = strings.TrimSpace(secUID)
	if secUID == "" {
		return ""
	}
	if b.douyin != nil {
		if w := b.douyin.CachedWebRID(secUID); w != "" {
			return w
		}
	}
	if b.subs != nil {
		if _, acc, ok := b.subs.GetAccount(string(site.Douyin), secUID); ok && acc.WebRID != "" {
			return acc.WebRID
		}
	}
	return ""
}
