package douyin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

type liveQualityTierSpec struct {
	sdkKey    string
	legacyKey string
	qualityID int
	label     string
	rank      int
}

var douyinLiveTiers = []liveQualityTierSpec{
	{sdkKey: "origin", legacyKey: "ORIGIN", qualityID: 10000, label: "原画", rank: 5},
	{sdkKey: "uhd", legacyKey: "FULL_HD1", qualityID: 400, label: "蓝光", rank: 4},
	{sdkKey: "hd", legacyKey: "HD1", qualityID: 300, label: "超清", rank: 3},
	{sdkKey: "sd", legacyKey: "SD1", qualityID: 200, label: "高清", rank: 2},
	{sdkKey: "ld", legacyKey: "SD2", qualityID: 150, label: "标清", rank: 1},
}

// LiveRoomDetail holds normalized room, anchor, and raw stream metadata from web/enter or reflow/info.
type LiveRoomDetail struct {
	OwnerSecUID      string
	OwnerUID         string
	OwnerNickname    string
	OwnerAvatar      string
	OwnerShortID     string
	QueriedWebRID    string
	ResponseWebRID   string
	ConfirmedShortID bool
	RoomID           string
	RoomStatus       int // 2 = live, 4 = offline
	Title            string
	CoverURL         string
	AreaName         string
	LiveSince        int64
	AudioOnly        bool
	StreamURL        *rawLiveStreamURL
}

// PreferredWebRID returns the confirmed numeric short_id when available, falling back to ResponseWebRID.
func (r *LiveRoomDetail) PreferredWebRID() string {
	if r == nil {
		return ""
	}
	if r.ConfirmedShortID && r.OwnerShortID != "" && r.OwnerShortID != "0" {
		return r.OwnerShortID
	}
	if r.ResponseWebRID != "" {
		return r.ResponseWebRID
	}
	return r.QueriedWebRID
}

// LiveDedupeKey returns the canonical single-session recording deduplication key ("douyin:live:<room_id>").
func (r *LiveRoomDetail) LiveDedupeKey() string {
	if r == nil || r.RoomID == "" {
		return ""
	}
	return "douyin:live:" + r.RoomID
}

type rawLiveStreamURL struct {
	FLVPullURL    map[string]string `json:"flv_pull_url"`
	HLSPullURLMap map[string]string `json:"hls_pull_url_map"`
	HLSPullURL    string            `json:"hls_pull_url"`
	LiveCoreSDK   *struct {
		PullData *struct {
			StreamData string `json:"stream_data"`
		} `json:"pull_data"`
	} `json:"live_core_sdk_data"`
}

type rawLiveRoomNode struct {
	ID            json.RawMessage   `json:"id"`
	IDStr         string            `json:"id_str"`
	Status        int               `json:"status"`
	Title         string            `json:"title"`
	CreateTime    int64             `json:"create_time"`
	StartTime     int64             `json:"start_time"`
	LiveRoomMode  int               `json:"live_room_mode"`
	LiveTypeAudio bool              `json:"live_type_audio"`
	Cover         *rawURLContainer  `json:"cover"`
	Owner         *rawLiveOwnerNode `json:"owner"`
	StreamURL     *rawLiveStreamURL `json:"stream_url"`
	RoomViewStats *struct {
		DisplayValue int64 `json:"display_value"`
	} `json:"room_view_stats"`
}

type rawLiveOwnerNode struct {
	IDStr       string           `json:"id_str"`
	SecUID      string           `json:"sec_uid"`
	Nickname    string           `json:"nickname"`
	ShortID     json.RawMessage  `json:"short_id"`
	DisplayID   string           `json:"display_id"`
	WebRID      string           `json:"web_rid"`
	AvatarThumb *rawURLContainer `json:"avatar_thumb"`
	AvatarLarge *rawURLContainer `json:"avatar_large"`
}

type webEnterEnvelope struct {
	StatusCode int `json:"status_code"`
	Data       *struct {
		Data             []rawLiveRoomNode `json:"data"`
		User             *rawLiveOwnerNode `json:"user"`
		RoomStatus       int               `json:"room_status"`
		EnterMode        int               `json:"enter_mode"`
		WebRID           string            `json:"web_rid"`
		PartitionRoadMap *struct {
			Partition *struct {
				Title string `json:"title"`
			} `json:"partition"`
			SubPartition *struct {
				Partition *struct {
					Title string `json:"title"`
				} `json:"partition"`
			} `json:"sub_partition"`
		} `json:"partition_road_map"`
	} `json:"data"`
}

type reflowInfoEnvelope struct {
	StatusCode int `json:"status_code"`
	Data       *struct {
		Room *rawLiveRoomNode `json:"room"`
	} `json:"data"`
}

// CachedWebRID returns the currently cached web_rid for secUID, or empty string if none/invalidated.
func (c *Client) CachedWebRID(secUID string) string {
	c.liveCacheMu.RLock()
	entry := c.webRIDByUser[strings.TrimSpace(secUID)]
	c.liveCacheMu.RUnlock()
	return entry.webRID
}

func (c *Client) rememberWebRID(secUID, webRID string, confirmedShortID bool) {
	secUID = strings.TrimSpace(secUID)
	webRID = strings.TrimSpace(webRID)
	if !reValidSecUID.MatchString(secUID) || !reRoomDigits.MatchString(webRID) {
		return
	}
	c.liveCacheMu.Lock()
	prev, exists := c.webRIDByUser[secUID]
	// Never overwrite a confirmed permanent short_id with a transient web_rid!
	if exists && prev.confirmedShortID && !confirmedShortID {
		c.liveCacheMu.Unlock()
		return
	}
	c.webRIDByUser[secUID] = liveWebRIDEntry{
		webRID:           webRID,
		confirmedShortID: confirmedShortID || prev.confirmedShortID && prev.webRID == webRID,
	}
	c.liveCacheMu.Unlock()
}

func (c *Client) clearCachedWebRID(secUID string) {
	c.liveCacheMu.Lock()
	delete(c.webRIDByUser, strings.TrimSpace(secUID))
	c.liveCacheMu.Unlock()
}

// EnterLiveRoom queries live.douyin.com/webcast/room/web/enter/ by web_rid and verifies expectedSecUID when non-empty.
func (c *Client) EnterLiveRoom(ctx context.Context, webRID string, expectedSecUID string) (*LiveRoomDetail, error) {
	webRID = strings.TrimSpace(webRID)
	expectedSecUID = strings.TrimSpace(expectedSecUID)
	if !reRoomDigits.MatchString(webRID) {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音直播间号 web_rid",
		}
	}

	params := c.profile.CommonLiveEnterParams(webRID, "")
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.LiveWebEnter,
		referer:     DefaultLiveReferer,
		origin:      DefaultLiveOrigin,
		params:      params,
		requireSign: true,
	})
	if err != nil {
		return nil, err
	}

	var env webEnterEnvelope
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.LiveWebEnter,
			Message:  "解析抖音直播间 web/enter JSON 失败",
			Err:      err,
		}
	}
	if env.Data == nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindUnavailable,
			Endpoint: c.endpoints.LiveWebEnter,
			Message:  "抖音直播间数据为空或不存在",
		}
	}

	var roomNode *rawLiveRoomNode
	if len(env.Data.Data) > 0 {
		roomNode = &env.Data.Data[0]
	}

	var owner *rawLiveOwnerNode
	if env.Data.User != nil && strings.TrimSpace(env.Data.User.SecUID) != "" {
		owner = env.Data.User
	} else if roomNode != nil && roomNode.Owner != nil {
		owner = roomNode.Owner
	}

	ownerSecUID := ""
	ownerUID := ""
	ownerNick := ""
	ownerShortID := ""
	ownerAvatar := ""
	respWebRID := strings.TrimSpace(env.Data.WebRID)
	if owner != nil {
		ownerSecUID = strings.TrimSpace(owner.SecUID)
		ownerUID = strings.TrimSpace(owner.IDStr)
		ownerNick = strings.TrimSpace(owner.Nickname)
		ownerShortID = rawNumberOrString(owner.ShortID)
		ownerAvatar = pickLiveOwnerAvatar(owner, c.allowLocalhost)
		if respWebRID == "" {
			respWebRID = strings.TrimSpace(owner.WebRID)
		}
	}
	if roomNode != nil && roomNode.Owner != nil {
		if ownerSecUID == "" {
			ownerSecUID = strings.TrimSpace(roomNode.Owner.SecUID)
		}
		if ownerShortID == "" || ownerShortID == "0" {
			ownerShortID = rawNumberOrString(roomNode.Owner.ShortID)
		}
		if respWebRID == "" {
			respWebRID = strings.TrimSpace(roomNode.Owner.WebRID)
		}
		if ownerAvatar == "" {
			ownerAvatar = pickLiveOwnerAvatar(roomNode.Owner, c.allowLocalhost)
		}
		if ownerNick == "" {
			ownerNick = strings.TrimSpace(roomNode.Owner.Nickname)
		}
	}

	if !reValidSecUID.MatchString(ownerSecUID) {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindUnavailable,
			Endpoint: c.endpoints.LiveWebEnter,
			Message:  "该直播间不存在或未返回有效主播标识",
		}
	}

	// Strict anchor sec_uid ownership check (M3-K16): reject recycled/mismatched web_rid!
	if expectedSecUID != "" && ownerSecUID != expectedSecUID {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindUnavailable,
			Endpoint: c.endpoints.LiveWebEnter,
			Message:  "缓存的直播间号已不再指向原订阅主播",
		}
	}

	roomID := ""
	roomStatus := 4
	title := ""
	coverURL := ""
	var liveSince int64
	var streamNode *rawLiveStreamURL

	if roomNode != nil {
		roomID = firstNonEmpty(roomNode.IDStr, rawNumberOrString(roomNode.ID))
		if roomID == "0" {
			roomID = ""
		}
		if roomNode.Status != 0 {
			roomStatus = roomNode.Status
		}
		title = strings.TrimSpace(roomNode.Title)
		if roomNode.Cover != nil {
			covers := filterNoteImageURLs(roomNode.Cover.URLList, c.allowLocalhost)
			if len(covers) > 0 {
				coverURL = covers[0]
			}
		}
		liveSince = roomNode.StartTime
		if liveSince == 0 {
			liveSince = roomNode.CreateTime
		}
		streamNode = roomNode.StreamURL
	}

	var areaName string
	if env.Data.PartitionRoadMap != nil {
		if env.Data.PartitionRoadMap.SubPartition != nil && env.Data.PartitionRoadMap.SubPartition.Partition != nil {
			areaName = strings.TrimSpace(env.Data.PartitionRoadMap.SubPartition.Partition.Title)
		}
		if areaName == "" && env.Data.PartitionRoadMap.Partition != nil {
			areaName = strings.TrimSpace(env.Data.PartitionRoadMap.Partition.Title)
		}
	}

	audioOnly := isRoomAudioOnly(env.Data.RoomStatus, roomNode)

	confirmedShort := ownerShortID != "" && ownerShortID != "0" && reRoomDigits.MatchString(ownerShortID) &&
		(webRID == ownerShortID || respWebRID == ownerShortID)

	// Preserve previously confirmed short_id for this anchor if already cached!
	c.liveCacheMu.RLock()
	prevEntry, hadPrev := c.webRIDByUser[ownerSecUID]
	c.liveCacheMu.RUnlock()
	if hadPrev && prevEntry.confirmedShortID && prevEntry.webRID != "" {
		ownerShortID = prevEntry.webRID
		confirmedShort = true
	}

	if respWebRID == "" {
		respWebRID = webRID
	}

	detail := &LiveRoomDetail{
		OwnerSecUID:      ownerSecUID,
		OwnerUID:         ownerUID,
		OwnerNickname:    ownerNick,
		OwnerAvatar:      ownerAvatar,
		OwnerShortID:     ownerShortID,
		QueriedWebRID:    webRID,
		ResponseWebRID:   respWebRID,
		ConfirmedShortID: confirmedShort,
		RoomID:           roomID,
		RoomStatus:       roomStatus,
		Title:            title,
		CoverURL:         coverURL,
		AreaName:         areaName,
		LiveSince:        liveSince,
		AudioOnly:        audioOnly,
		StreamURL:        streamNode,
	}

	c.rememberWebRID(ownerSecUID, detail.PreferredWebRID(), confirmedShort)
	return detail, nil
}

// ReflowRoomInfo queries webcast.amemv.com/webcast/room/reflow/info/ by single-session room_id.
func (c *Client) ReflowRoomInfo(ctx context.Context, roomID, secUID string) (*LiveRoomDetail, error) {
	roomID = strings.TrimSpace(roomID)
	secUID = strings.TrimSpace(secUID)
	if !reReflowRoomDigits.MatchString(roomID) {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音直播场次 room_id",
		}
	}

	params := []sign.Pair{
		{Key: "type_id", Value: "0"},
		{Key: "live_id", Value: "1"},
		{Key: "room_id", Value: roomID},
		{Key: "sec_user_id", Value: secUID},
		{Key: "version_code", Value: "99.99.99"},
		{Key: "app_id", Value: "1128"},
		{Key: "aid", Value: "6383"},
	}

	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.LiveReflowInfo,
		referer:     DefaultLiveReferer,
		origin:      DefaultLiveOrigin,
		params:      params,
		requireSign: false,
	})
	if err != nil {
		return nil, err
	}

	var env reflowInfoEnvelope
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.LiveReflowInfo,
			Message:  "解析抖音直播 reflow/info JSON 失败",
			Err:      err,
		}
	}
	if env.Data == nil || env.Data.Room == nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindUnavailable,
			Endpoint: c.endpoints.LiveReflowInfo,
			Message:  "抖音直播场次不存在或已结束",
		}
	}

	rm := env.Data.Room
	ownerSec := ""
	ownerUID := ""
	ownerNick := ""
	ownerShort := ""
	ownerAvatar := ""
	respWebRID := ""
	if rm.Owner != nil {
		ownerSec = strings.TrimSpace(rm.Owner.SecUID)
		ownerUID = strings.TrimSpace(rm.Owner.IDStr)
		ownerNick = strings.TrimSpace(rm.Owner.Nickname)
		ownerShort = rawNumberOrString(rm.Owner.ShortID)
		ownerAvatar = pickLiveOwnerAvatar(rm.Owner, c.allowLocalhost)
		respWebRID = strings.TrimSpace(rm.Owner.WebRID)
	}
	if secUID != "" && ownerSec != "" && ownerSec != secUID {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindUnavailable,
			Endpoint: c.endpoints.LiveReflowInfo,
			Message:  "直播场次归属主播 sec_uid 不匹配",
		}
	}
	if ownerSec == "" {
		ownerSec = secUID
	}

	gotRoomID := firstNonEmpty(rm.IDStr, rawNumberOrString(rm.ID), roomID)
	var coverURL string
	if rm.Cover != nil {
		if list := filterNoteImageURLs(rm.Cover.URLList, c.allowLocalhost); len(list) > 0 {
			coverURL = list[0]
		}
	}

	audioOnly := isRoomAudioOnly(-1, rm)
	confirmedShort := ownerShort != "" && ownerShort != "0" && reRoomDigits.MatchString(ownerShort) && respWebRID == ownerShort

	detail := &LiveRoomDetail{
		OwnerSecUID:      ownerSec,
		OwnerUID:         ownerUID,
		OwnerNickname:    ownerNick,
		OwnerAvatar:      ownerAvatar,
		OwnerShortID:     ownerShort,
		ResponseWebRID:   respWebRID,
		ConfirmedShortID: confirmedShort,
		RoomID:           gotRoomID,
		RoomStatus:       rm.Status,
		Title:            strings.TrimSpace(rm.Title),
		CoverURL:         coverURL,
		LiveSince:        firstNonZeroInt64(rm.StartTime, rm.CreateTime),
		AudioOnly:        audioOnly,
		StreamURL:        rm.StreamURL,
	}
	if ownerSec != "" && detail.PreferredWebRID() != "" {
		c.rememberWebRID(ownerSec, detail.PreferredWebRID(), confirmedShort)
	}
	return detail, nil
}

// CheckRoomsAlive batch-checks whether known active 19-digit session room_ids are still live.
// Uses json.Decoder.UseNumber() to preserve 64-bit room_id precision and maps results by room_id key (douyin.md §8.6).
func (c *Client) CheckRoomsAlive(ctx context.Context, roomIDs []string) (map[string]bool, error) {
	var validIDs []string
	seen := make(map[string]bool, len(roomIDs))
	for _, raw := range roomIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		if !reRoomDigits.MatchString(id) {
			return nil, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindBadRequest,
				Message: "无效的抖音 room_id",
			}
		}
		seen[id] = true
		validIDs = append(validIDs, id)
	}
	result := make(map[string]bool, len(validIDs))
	for _, id := range validIDs {
		result[id] = false
	}
	if len(validIDs) == 0 {
		return result, nil
	}

	params := []sign.Pair{
		{Key: "aid", Value: "6383"},
		{Key: "device_platform", Value: "web"},
		{Key: "room_ids", Value: strings.Join(validIDs, ",")},
	}

	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.LiveCheckAlive,
		referer:     DefaultLiveReferer,
		origin:      DefaultLiveOrigin,
		params:      params,
		requireSign: false,
	})
	if err != nil {
		return nil, err
	}

	var env struct {
		StatusCode int `json:"status_code"`
		Data       []struct {
			RoomID    json.Number `json:"room_id"`
			RoomIDStr string      `json:"room_id_str"`
			Alive     bool        `json:"alive"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.LiveCheckAlive,
			Message:  "解析抖音 check_alive JSON 失败",
			Err:      err,
		}
	}

	for _, item := range env.Data {
		id := strings.TrimSpace(item.RoomIDStr)
		if id == "" {
			id = strings.TrimSpace(item.RoomID.String())
		}
		if id != "" {
			result[id] = item.Alive
		}
	}
	return result, nil
}

// CheckLiveStatus checks a subscribed creator's live state by sec_uid and optional cachedWebRID.
// If cachedWebRID is stale, unavailable (4001038), or belongs to another sec_uid, it clears the cache
// and falls back to the room_id route with AT MOST 1 extra request while preserving AccountUID = sec_uid (M3-K16).
func (c *Client) CheckLiveStatus(ctx context.Context, secUID string, cachedWebRID string) (site.LiveStatus, error) {
	secUID = strings.TrimSpace(secUID)
	if !reValidSecUID.MatchString(secUID) {
		return site.LiveStatus{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音主播 sec_uid",
		}
	}

	webRID := strings.TrimSpace(cachedWebRID)
	if webRID == "" {
		webRID = c.CachedWebRID(secUID)
	}

	if webRID != "" {
		room, err := c.EnterLiveRoom(ctx, webRID, secUID)
		if err == nil {
			return c.roomToLiveStatus(secUID, room, false), nil
		}
		// Propagate round-stop or transient network errors without wiping cached web_rid.
		if site.IsRoundStop(err) || site.IsKind(err, site.ErrKindNetwork) {
			return site.LiveStatus{}, err
		}

		// Invalidated or mismatched web_rid -> clear cache and fall back to profile/other (<= 1 extra request!).
		c.clearCachedWebRID(secUID)
		return c.checkLiveViaProfile(ctx, secUID, true, false)
	}

	// No cached web_rid yet -> check via profile/other; if live and missing web_rid, allow <= 1 reflow/info request.
	return c.checkLiveViaProfile(ctx, secUID, false, true)
}

func (c *Client) checkLiveViaProfile(ctx context.Context, secUID string, invalidated bool, allowReflowLookup bool) (site.LiveStatus, error) {
	prof, err := c.GetUserProfile(ctx, secUID)
	if err != nil {
		return site.LiveStatus{}, err
	}

	if prof.RoomID == "" || prof.RoomID == "0" {
		return site.LiveStatus{
			Site:              site.Douyin,
			AccountUID:        secUID,
			WebRID:            prof.WebRID,
			RoomID:            "",
			Status:            site.LiveStateOffline,
			Uname:             prof.Nickname,
			Face:              prof.AvatarURL,
			InvalidatedWebRID: invalidated,
		}, nil
	}

	// Try parsing embedded room_data first (0 extra requests!).
	if strings.TrimSpace(prof.RoomData) != "" {
		var embedded rawLiveRoomNode
		dec := json.NewDecoder(strings.NewReader(prof.RoomData))
		dec.UseNumber()
		if dec.Decode(&embedded) == nil {
			webRID := prof.WebRID
			if webRID != "" {
				c.rememberWebRID(secUID, webRID, prof.ShortID != "" && prof.ShortID == webRID)
			}
			var cover string
			if embedded.Cover != nil {
				if list := filterNoteImageURLs(embedded.Cover.URLList, c.allowLocalhost); len(list) > 0 {
					cover = list[0]
				}
			}
			audioOnly := isRoomAudioOnly(-1, &embedded)
			state := site.LiveStateLive
			if audioOnly {
				state = site.LiveStateAudioOnly
			}
			var qID int
			var qLabel, codec string
			if !audioOnly && embedded.StreamURL != nil {
				if plan, sErr := selectFLVFromStreamURL(embedded.StreamURL, nil, c.LiveTransport(), c.allowLocalhost); sErr == nil {
					qID = plan.Quality
					qLabel = plan.QualityName
					codec = plan.Codec
				}
			}
			return site.LiveStatus{
				Site:              site.Douyin,
				AccountUID:        secUID,
				WebRID:            webRID,
				RoomID:            prof.RoomID,
				Status:            state,
				Title:             strings.TrimSpace(embedded.Title),
				Uname:             prof.Nickname,
				Face:              prof.AvatarURL,
				Cover:             cover,
				LiveSince:         firstNonZeroInt64(embedded.StartTime, embedded.CreateTime),
				Quality:           qID,
				QualityName:       qLabel,
				Codec:             codec,
				InvalidatedWebRID: invalidated,
			}, nil
		}
	}

	if allowReflowLookup && c.canCallEndpoint(c.endpoints.LiveReflowInfo) {
		if reflowRoom, rErr := c.ReflowRoomInfo(ctx, prof.RoomID, secUID); rErr == nil {
			return c.roomToLiveStatus(secUID, reflowRoom, invalidated), nil
		}
	}

	return site.LiveStatus{
		Site:              site.Douyin,
		AccountUID:        secUID,
		WebRID:            prof.WebRID,
		RoomID:            prof.RoomID,
		Status:            site.LiveStateLive,
		Uname:             prof.Nickname,
		Face:              prof.AvatarURL,
		InvalidatedWebRID: invalidated,
	}, nil
}

func (c *Client) roomToLiveStatus(secUID string, room *LiveRoomDetail, invalidated bool) site.LiveStatus {
	if room == nil {
		return site.LiveStatus{
			Site:              site.Douyin,
			AccountUID:        secUID,
			Status:            site.LiveStateOffline,
			InvalidatedWebRID: invalidated,
		}
	}

	state := site.LiveStateOffline
	var qID int
	var qLabel, codec string
	if room.RoomStatus == 2 && room.RoomID != "" {
		if room.AudioOnly {
			state = site.LiveStateAudioOnly
		} else {
			state = site.LiveStateLive
			if room.StreamURL != nil {
				if plan, err := selectFLVFromStreamURL(room.StreamURL, nil, c.LiveTransport(), c.allowLocalhost); err == nil {
					qID = plan.Quality
					qLabel = plan.QualityName
					codec = plan.Codec
				}
			}
		}
	}

	return site.LiveStatus{
		Site:              site.Douyin,
		AccountUID:        secUID,
		WebRID:            room.PreferredWebRID(),
		RoomID:            room.RoomID,
		Status:            state,
		Title:             room.Title,
		Uname:             room.OwnerNickname,
		Face:              room.OwnerAvatar,
		Cover:             room.CoverURL,
		AreaName:          room.AreaName,
		LiveSince:         room.LiveSince,
		Quality:           qID,
		QualityName:       qLabel,
		Codec:             codec,
		InvalidatedWebRID: invalidated,
	}
}

// SelectLiveStream resolves the target room and selects the highest-quality HTTP-FLV stream
// (origin > uhd > hd > sd > ld), excluding only_audio=1 and reporting clear Chinese errors
// on audio-only or HLS-only rooms (DEC-043 & M3-K16).
func (c *Client) SelectLiveStream(ctx context.Context, secUID string, roomOrWebRID string, codecOrder []string) (site.LiveStreamPlan, error) {
	secUID = strings.TrimSpace(secUID)
	target := strings.TrimSpace(roomOrWebRID)
	if target == "" && secUID != "" {
		target = c.CachedWebRID(secUID)
	}

	var room *LiveRoomDetail
	var err error

	switch {
	case rePure19Digits.MatchString(target) && c.canCallEndpoint(c.endpoints.LiveReflowInfo):
		room, err = c.ReflowRoomInfo(ctx, target, secUID)
		if err != nil && c.canCallEndpoint(c.endpoints.LiveWebEnter) {
			room, err = c.EnterLiveRoom(ctx, target, secUID)
		}

	case target != "":
		room, err = c.EnterLiveRoom(ctx, target, secUID)
		if err != nil && !site.IsRoundStop(err) && !site.IsKind(err, site.ErrKindNetwork) && secUID != "" {
			c.clearCachedWebRID(secUID)
			prof, pErr := c.GetUserProfile(ctx, secUID)
			if pErr != nil {
				return site.LiveStreamPlan{}, pErr
			}
			if prof.RoomID == "" || prof.RoomID == "0" {
				return site.LiveStreamPlan{}, &site.Error{
					Site:    site.Douyin,
					Kind:    site.ErrKindUnavailable,
					Message: "主播当前未开播",
				}
			}
			room, err = c.ReflowRoomInfo(ctx, prof.RoomID, secUID)
		}

	case secUID != "":
		prof, pErr := c.GetUserProfile(ctx, secUID)
		if pErr != nil {
			return site.LiveStreamPlan{}, pErr
		}
		if prof.RoomID == "" || prof.RoomID == "0" {
			return site.LiveStreamPlan{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindUnavailable,
				Message: "主播当前未开播",
			}
		}
		room, err = c.ReflowRoomInfo(ctx, prof.RoomID, secUID)

	default:
		return site.LiveStreamPlan{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "缺少抖音直播间标识或主播 sec_uid",
		}
	}

	if err != nil {
		return site.LiveStreamPlan{}, err
	}

	return SelectFLVStreamFromRoom(room, codecOrder, c.LiveTransport(), c.allowLocalhost)
}

// SelectFLVStreamFromRoom selects the best HTTP-FLV stream from a parsed LiveRoomDetail.
func SelectFLVStreamFromRoom(room *LiveRoomDetail, codecOrder []string, mediaSpec site.MediaTransportSpec, allowLocalhost bool) (site.LiveStreamPlan, error) {
	if room == nil || room.RoomStatus != 2 || room.RoomID == "" {
		return site.LiveStreamPlan{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "主播当前未开播",
		}
	}
	if room.AudioOnly {
		return site.LiveStreamPlan{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     "这个直播间是语音直播，暂不支持录制",
		}
	}
	if room.StreamURL == nil {
		return site.LiveStreamPlan{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "抖音直播间未返回可用拉流地址",
		}
	}

	plan, err := selectFLVFromStreamURL(room.StreamURL, codecOrder, mediaSpec, allowLocalhost)
	if err != nil {
		return site.LiveStreamPlan{}, err
	}
	plan.AccountUID = room.OwnerSecUID
	plan.WebRID = room.PreferredWebRID()
	plan.RoomID = room.RoomID
	plan.Status = site.LiveStateLive
	return plan, nil
}

type rawSDKStreamData struct {
	Data map[string]map[string]struct {
		FLV       string `json:"flv"`
		HLS       string `json:"hls"`
		SDKParams string `json:"sdk_params"`
	} `json:"data"`
}

type flvTierCandidate struct {
	tier  liveQualityTierSpec
	codec string
	urls  []string
}

func selectFLVFromStreamURL(s *rawLiveStreamURL, codecOrder []string, mediaSpec site.MediaTransportSpec, allowLocalhost bool) (site.LiveStreamPlan, error) {
	prefCodec := preferredCodecFromOrder(codecOrder)
	var candidates []flvTierCandidate
	sawAudioOnlyURL := false
	sawHLSURL := false

	checkURL := func(raw string) (string, bool) {
		uStr := upgradeDouyinHTTPS(raw, allowLocalhost)
		if uStr == "" {
			return "", false
		}
		if isAudioOnlyStreamURL(uStr) {
			sawAudioOnlyURL = true
			return "", false
		}
		if mediaSpec.ValidateURL(uStr) != nil {
			return "", false
		}
		return uStr, true
	}

	// 1. Parse live_core_sdk_data.pull_data.stream_data (double-escaped JSON).
	if s.LiveCoreSDK != nil && s.LiveCoreSDK.PullData != nil && strings.TrimSpace(s.LiveCoreSDK.PullData.StreamData) != "" {
		var sd rawSDKStreamData
		if json.Unmarshal([]byte(s.LiveCoreSDK.PullData.StreamData), &sd) == nil {
			for tierKey, lineMap := range sd.Data {
				lowerTier := strings.ToLower(strings.TrimSpace(tierKey))
				if lowerTier == "ao" || lowerTier == "audio" {
					sawAudioOnlyURL = true
					continue
				}
				spec, ok := matchLiveTierBySDKKey(lowerTier)
				if !ok {
					continue
				}

				// Group main + backup lines.
				var urls []string
				codec := ""
				for _, lineName := range []string{"main", "backup"} {
					line, exists := lineMap[lineName]
					if !exists {
						continue
					}
					if line.HLS != "" && !isAudioOnlyStreamURL(line.HLS) {
						sawHLSURL = true
					}
					if uStr, valid := checkURL(line.FLV); valid {
						urls = append(urls, uStr)
						if codec == "" {
							codec = parseSDKParamsCodec(line.SDKParams)
						}
					}
				}
				urls = dedupStrings(urls)
				if len(urls) > 0 {
					if codec == "" {
						codec = "avc"
					}
					candidates = append(candidates, flvTierCandidate{
						tier:  spec,
						codec: codec,
						urls:  urls,
					})
				}
			}
		}
	}

	// 2. Fallback to legacy flv_pull_url map when stream_data yielded no FLV candidates.
	if len(candidates) == 0 && len(s.FLVPullURL) > 0 {
		for legacyKey, rawURL := range s.FLVPullURL {
			upperKey := strings.ToUpper(strings.TrimSpace(legacyKey))
			if upperKey == "AO" || upperKey == "AUDIO" {
				sawAudioOnlyURL = true
				continue
			}
			spec, ok := matchLiveTierByLegacyKey(upperKey)
			if !ok {
				continue
			}
			if uStr, valid := checkURL(rawURL); valid {
				candidates = append(candidates, flvTierCandidate{
					tier:  spec,
					codec: inferLiveURLCodec(uStr),
					urls:  []string{uStr},
				})
			}
		}
	}

	if len(s.HLSPullURLMap) > 0 || strings.TrimSpace(s.HLSPullURL) != "" {
		for _, u := range s.HLSPullURLMap {
			if strings.TrimSpace(u) != "" && !isAudioOnlyStreamURL(u) {
				sawHLSURL = true
			}
		}
		if strings.TrimSpace(s.HLSPullURL) != "" && !isAudioOnlyStreamURL(s.HLSPullURL) {
			sawHLSURL = true
		}
	}

	if len(candidates) == 0 {
		if sawAudioOnlyURL && !sawHLSURL {
			return site.LiveStreamPlan{}, &site.Error{
				Site:        site.Douyin,
				Kind:        site.ErrKindUnavailable,
				Unsupported: true,
				Message:     "这个直播间是语音直播，暂不支持录制",
			}
		}
		if sawHLSURL {
			return site.LiveStreamPlan{}, &site.Error{
				Site:        site.Douyin,
				Kind:        site.ErrKindUnavailable,
				Unsupported: true,
				Message:     "该直播间仅提供 HLS 流，当前版本暂不支持录制（仅支持 HTTP-FLV）",
			}
		}
		return site.LiveStreamPlan{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "未找到可用的抖音 HTTP-FLV 直播流地址",
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].tier.rank != candidates[j].tier.rank {
			return candidates[i].tier.rank > candidates[j].tier.rank
		}
		iPref := candidates[i].codec == prefCodec
		jPref := candidates[j].codec == prefCodec
		return iPref && !jPref
	})

	best := candidates[0]
	return site.LiveStreamPlan{
		Site:        site.Douyin,
		Transport:   site.LiveTransportHTTPFLV,
		Protocol:    "http_stream",
		Format:      "flv",
		Codec:       best.codec,
		Quality:     best.tier.qualityID,
		QualityName: best.tier.label,
		URLs:        best.urls,
		Media:       mediaSpec,
	}, nil
}

// isRoomAudioOnly determines whether a live room is an audio/radio-only broadcast.
// Per douyin.md §8.2, web/enter sets outer data.room_status == 1 for voice/radio broadcasts
// (0 = video live, 2 = offline; data.enter_mode == 1 is an entry-routing flag and MUST NOT
// mark a room_status == 0 video room as audio-only). In reflow/info and embedded room_data
// (outerRoomStatus < 0), room.live_type_audio == true or pure-audio stream_url (only_audio=1)
// indicates an audio-only room; live_room_mode == 2 is only used when no non-audio video stream exists.
func isRoomAudioOnly(outerRoomStatus int, rm *rawLiveRoomNode) bool {
	if outerRoomStatus == 1 {
		return true
	}
	if rm == nil {
		return false
	}
	if rm.LiveTypeAudio {
		return true
	}
	if rm.StreamURL != nil {
		return isStreamNodePureAudio(rm.StreamURL)
	}
	return rm.LiveRoomMode == 2
}

func isStreamNodePureAudio(s *rawLiveStreamURL) bool {
	if s == nil {
		return false
	}
	totalNonEmpty := 0
	nonAudioCount := 0

	check := func(raw string, isAudioKey bool) {
		if strings.TrimSpace(raw) == "" {
			return
		}
		totalNonEmpty++
		if !isAudioKey && !isAudioOnlyStreamURL(raw) {
			nonAudioCount++
		}
	}

	if s.LiveCoreSDK != nil && s.LiveCoreSDK.PullData != nil && strings.TrimSpace(s.LiveCoreSDK.PullData.StreamData) != "" {
		var sd rawSDKStreamData
		if json.Unmarshal([]byte(s.LiveCoreSDK.PullData.StreamData), &sd) == nil {
			for tierKey, lineMap := range sd.Data {
				lt := strings.ToLower(strings.TrimSpace(tierKey))
				isAO := lt == "ao" || lt == "audio"
				for _, line := range lineMap {
					check(line.FLV, isAO)
					check(line.HLS, isAO)
				}
			}
		}
	}
	for k, v := range s.FLVPullURL {
		uk := strings.ToUpper(strings.TrimSpace(k))
		check(v, uk == "AO" || uk == "AUDIO")
	}
	for k, v := range s.HLSPullURLMap {
		uk := strings.ToUpper(strings.TrimSpace(k))
		check(v, uk == "AO" || uk == "AUDIO")
	}
	check(s.HLSPullURL, false)

	return totalNonEmpty > 0 && nonAudioCount == 0
}

func isAudioOnlyStreamURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "only_audio=1")
}

func matchLiveTierBySDKKey(key string) (liveQualityTierSpec, bool) {
	for _, spec := range douyinLiveTiers {
		if spec.sdkKey == key {
			return spec, true
		}
	}
	return liveQualityTierSpec{}, false
}

func matchLiveTierByLegacyKey(key string) (liveQualityTierSpec, bool) {
	if key == "LD" {
		return douyinLiveTiers[len(douyinLiveTiers)-1], true
	}
	for _, spec := range douyinLiveTiers {
		if spec.legacyKey == key {
			return spec, true
		}
	}
	return liveQualityTierSpec{}, false
}

func parseSDKParamsCodec(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var sp struct {
		VCodec string `json:"VCodec"`
	}
	if json.Unmarshal([]byte(raw), &sp) == nil {
		vc := strings.ToLower(strings.TrimSpace(sp.VCodec))
		if strings.Contains(vc, "265") || strings.Contains(vc, "hevc") || strings.Contains(vc, "bytevc1") {
			return "hevc"
		}
		if strings.Contains(vc, "264") || strings.Contains(vc, "avc") {
			return "avc"
		}
	}
	return ""
}

func inferLiveURLCodec(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		lower := strings.ToLower(u.Path + "?" + u.RawQuery)
		if strings.Contains(lower, "265") || strings.Contains(lower, "hevc") || strings.Contains(lower, "bytevc1") {
			return "hevc"
		}
	}
	return "avc"
}

func preferredCodecFromOrder(order []string) string {
	for _, c := range order {
		lc := strings.ToLower(strings.TrimSpace(c))
		switch lc {
		case "hevc", "h265", "hev1", "hvc1":
			return "hevc"
		case "avc", "h264", "avc1":
			return "avc"
		}
	}
	return "avc"
}

func pickLiveOwnerAvatar(o *rawLiveOwnerNode, allowLocalhost bool) string {
	if o == nil {
		return ""
	}
	for _, box := range []*rawURLContainer{o.AvatarLarge, o.AvatarThumb} {
		if box == nil {
			continue
		}
		if list := filterNoteImageURLs(box.URLList, allowLocalhost); len(list) > 0 {
			return list[0]
		}
	}
	return ""
}

func firstNonZeroInt64(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
