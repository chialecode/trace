package service

import (
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
)

// RenewalStatus summarizes automatic session renewal capability and state for a site.
type RenewalStatus struct {
	Site          string `json:"site"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	LastRenewedAt string `json:"lastRenewedAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// DouyinAuthorDTO holds normalized Douyin creator summary fields for UI rendering
// without requiring internal/ui to import internal/site/douyin.
type DouyinAuthorDTO struct {
	SecUID    string `json:"secUid"`
	UID       string `json:"uid,omitempty"`
	UniqueID  string `json:"uniqueId,omitempty"`
	ShortID   string `json:"shortId,omitempty"`
	Nickname  string `json:"nickname"`
	Signature string `json:"signature,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	RoomID    string `json:"roomId,omitempty"`
	WebRID    string `json:"webRid,omitempty"`
}

// DouyinStatsDTO holds online engagement counts for a Douyin work.
// Douyin Web's placeholder play_count is intentionally omitted so the UI never displays 0 plays.
type DouyinStatsDTO struct {
	DiggCount    int64 `json:"diggCount"`
	CommentCount int64 `json:"commentCount"`
	CollectCount int64 `json:"collectCount"`
	ShareCount   int64 `json:"shareCount"`
}

// DouyinMusicDTO holds background music or original sound metadata attached to a Douyin work.
type DouyinMusicDTO struct {
	ID              string `json:"id,omitempty"`
	Title           string `json:"title,omitempty"`
	Author          string `json:"author,omitempty"`
	DurationSec     int    `json:"durationSec,omitempty"`
	IsOriginalSound bool   `json:"isOriginalSound"`
	CoverURL        string `json:"coverUrl,omitempty"`
	PlayURL         string `json:"playUrl,omitempty"`
	Playable        bool   `json:"playable"`
	UnavailableHint string `json:"unavailableHint,omitempty"`
}

// DouyinMediaPageDTO represents one ordered page of a Douyin image or mixed-media note post.
type DouyinMediaPageDTO struct {
	Index         int    `json:"index"` // 1-based
	Kind          string `json:"kind"`  // "image", "live_photo", "video", "unknown"
	ImageURL      string `json:"imageUrl,omitempty"`
	MotionURL     string `json:"motionUrl,omitempty"`
	VideoURL      string `json:"videoUrl,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	DurationSec   int    `json:"durationSec,omitempty"`
	Codec         string `json:"codec,omitempty"`
	QualityLabel  string `json:"qualityLabel,omitempty"`
	UnknownReason string `json:"unknownReason,omitempty"`
}

// DouyinWorkView holds the normalized view of a Douyin video or image/mixed note post.
type DouyinWorkView struct {
	ID                string                `json:"id"`
	Kind              string                `json:"kind"` // "video", "note", "unsupported"
	Title             string                `json:"title"`
	Desc              string                `json:"desc"`
	Hashtags          []string              `json:"hashtags,omitempty"`
	CreateTime        int64                 `json:"createTime"`
	DurationSec       int                   `json:"durationSec,omitempty"`
	IsTop             bool                  `json:"isTop,omitempty"`
	Author            DouyinAuthorDTO       `json:"author"`
	CoverURL          string                `json:"coverUrl,omitempty"`
	VideoURL          string                `json:"videoUrl,omitempty"`
	VideoCodec        string                `json:"videoCodec,omitempty"`
	QualityLabel      string                `json:"qualityLabel,omitempty"`
	GearName          string                `json:"gearName,omitempty"`
	Width             int                   `json:"width,omitempty"`
	Height            int                   `json:"height,omitempty"`
	DataSize          int64                 `json:"dataSize,omitempty"`
	Pages             []DouyinMediaPageDTO  `json:"pages,omitempty"`
	Music             *DouyinMusicDTO       `json:"music,omitempty"`
	Stats             DouyinStatsDTO        `json:"stats"`
	Uncacheable       bool                  `json:"uncacheable,omitempty"`
	UncacheableReason string                `json:"uncacheableReason,omitempty"`
	CanonicalURL      string                `json:"canonicalUrl"`
	CacheKey          string                `json:"cacheKey"`
	CachedItem        *library.ItemMetadata `json:"cachedItem,omitempty"`
}

// DouyinWorkCardDTO represents a single work card in the 3:4 Douyin creator grid.
type DouyinWorkCardDTO struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"` // "video", "note", "unsupported"
	Title             string `json:"title"`
	Desc              string `json:"desc,omitempty"`
	CreateTime        int64  `json:"createTime"`
	DurationSec       int    `json:"durationSec,omitempty"`
	IsTop             bool   `json:"isTop,omitempty"`
	CoverURL          string `json:"coverUrl,omitempty"`
	PageCount         int    `json:"pageCount,omitempty"`
	Uncacheable       bool   `json:"uncacheable,omitempty"`
	UncacheableReason string `json:"uncacheableReason,omitempty"`
	CanonicalURL      string `json:"canonicalUrl"`
	CacheKey          string `json:"cacheKey"`
}

// DouyinUserView holds a Douyin creator's profile header and optional live banner state.
type DouyinUserView struct {
	SecUID         string            `json:"secUid"`
	UID            string            `json:"uid,omitempty"`
	UniqueID       string            `json:"uniqueId,omitempty"`
	ShortID        string            `json:"shortId,omitempty"`
	Nickname       string            `json:"nickname"`
	Signature      string            `json:"signature,omitempty"`
	AvatarURL      string            `json:"avatarUrl,omitempty"`
	AwemeCount     int               `json:"awemeCount"`
	FollowerCount  int64             `json:"followerCount"`
	FollowingCount int64             `json:"followingCount"`
	TotalFavorited int64             `json:"totalFavorited"`
	IsPrivate      bool              `json:"isPrivate,omitempty"`
	LiveStatus     int               `json:"liveStatus,omitempty"` // 0=offline, 1=live
	RoomID         string            `json:"roomId,omitempty"`
	WebRID         string            `json:"webRid,omitempty"`
	CanonicalURL   string            `json:"canonicalUrl"`
	LiveRoom       *LiveAccountState `json:"liveRoom,omitempty"`
}

// DouyinUserWorksPage holds one page of a Douyin creator's works grid.
type DouyinUserWorksPage struct {
	Works           []DouyinWorkCardDTO `json:"works"`
	NextCursor      string              `json:"nextCursor,omitempty"`
	HasMore         bool                `json:"hasMore"`
	UncertainEmpty  bool                `json:"uncertainEmpty,omitempty"`
	UncertainReason string              `json:"uncertainReason,omitempty"`
}

// DouyinLiveView holds normalized Douyin live room metadata opened via /live/<web_rid>.
type DouyinLiveView struct {
	OwnerSecUID  string            `json:"ownerSecUid"`
	OwnerUID     string            `json:"ownerUid,omitempty"`
	OwnerName    string            `json:"ownerName"`
	OwnerAvatar  string            `json:"ownerAvatar,omitempty"`
	WebRID       string            `json:"webRid"`
	RoomID       string            `json:"roomId,omitempty"`
	LiveStatus   int               `json:"liveStatus"` // 0=offline, 1=live
	StatusText   string            `json:"statusText"`
	Title        string            `json:"title,omitempty"`
	CoverURL     string            `json:"coverUrl,omitempty"`
	AreaName     string            `json:"areaName,omitempty"`
	LiveSince    int64             `json:"liveSince,omitempty"`
	AudioOnly    bool              `json:"audioOnly,omitempty"`
	CanonicalURL string            `json:"canonicalUrl"`
	State        *LiveAccountState `json:"state,omitempty"`
}

// LiveAccountState describes the unified live broadcast and recording state of any <site>:<uid>
// creator or room across Bilibili and Douyin.
type LiveAccountState struct {
	Site              string  `json:"site"`                   // "bilibili" or "douyin"
	AccountKey        string  `json:"accountKey"`             // "<site>:<uid>"
	RecordingKey      string  `json:"recordingKey,omitempty"` // "<site>:live:<room_id>" or decimal room ID
	UID               string  `json:"uid"`                    // Bilibili decimal mid or Douyin sec_uid
	Mid               int64   `json:"mid,omitempty"`          // Bilibili numeric mid when site=="bilibili"
	RoomID            string  `json:"roomId,omitempty"`       // Bilibili decimal roomId or Douyin room_id_str
	WebRID            string  `json:"webRid,omitempty"`       // Douyin web_rid (browser room number)
	Uname             string  `json:"uname"`
	Face              string  `json:"face,omitempty"`
	Title             string  `json:"title,omitempty"`
	Cover             string  `json:"cover,omitempty"`
	AreaName          string  `json:"areaName,omitempty"`
	NoRoom            bool    `json:"noRoom,omitempty"`
	LiveStatus        int     `json:"liveStatus"` // 0=offline, 1=live, 2=carousel
	LiveSince         int64   `json:"liveSince,omitempty"`
	StatusText        string  `json:"statusText"` // "未开通直播间", "未开播", "直播中", "轮播中"
	QualityName       string  `json:"qualityName,omitempty"`
	Codec             string  `json:"codec,omitempty"`
	Transport         string  `json:"transport,omitempty"` // "hls_fmp4" or "http_flv"
	AudioOnly         bool    `json:"audioOnly,omitempty"`
	Recording         bool    `json:"recording"`
	RecordSession     string  `json:"recordSession,omitempty"`
	RecordDurationSec float64 `json:"recordDurationSec,omitempty"`
	RecordSpeedBps    int64   `json:"recordSpeedBps,omitempty"`
	RecordBytes       int64   `json:"recordBytes,omitempty"`
	RecordSegments    int     `json:"recordSegments,omitempty"`
	RecordItemDir     string  `json:"recordItemDir,omitempty"`
	StopReason        string  `json:"stopReason,omitempty"`
	LastError         string  `json:"lastError,omitempty"`
	UpdatedAt         string  `json:"updatedAt,omitempty"`
}

// SiteSchedulerStatus reports per-site scheduled subscription check timing and risk-control backoff
// for the Settings -> 订阅与直播 view.
type SiteSchedulerStatus struct {
	Site            string `json:"site"`
	DisplayName     string `json:"displayName"`
	LastCheckAt     string `json:"lastCheckAt,omitempty"`
	NextCheckAt     string `json:"nextCheckAt,omitempty"`
	BackingOff      bool   `json:"backingOff"`
	CurrentInterval string `json:"currentInterval,omitempty"`
	BackoffUntil    string `json:"backoffUntil,omitempty"`
	BackoffSummary  string `json:"backoffSummary,omitempty"`
	LastError       string `json:"lastError,omitempty"`
}

func douyinCacheKeyForKind(kind, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if kind == douyin.WorkKindNote {
		return "douyin:dynamic:" + id
	}
	return "douyin:video:" + id
}

func mapDouyinAuthorDTO(a douyin.AuthorSummary) DouyinAuthorDTO {
	return DouyinAuthorDTO{
		SecUID:    a.SecUID,
		UID:       a.UID,
		UniqueID:  a.UniqueID,
		ShortID:   a.ShortID,
		Nickname:  a.Nickname,
		Signature: a.Signature,
		AvatarURL: a.AvatarURL,
		RoomID:    a.RoomID,
		WebRID:    a.WebRID,
	}
}

func mapDouyinWorkView(w *douyin.WorkDetail, cached *library.ItemMetadata) *DouyinWorkView {
	if w == nil {
		return nil
	}
	var coverURL string
	if len(w.CoverURLs) > 0 {
		coverURL = w.CoverURLs[0]
	}
	out := &DouyinWorkView{
		ID:                w.ID,
		Kind:              w.Kind,
		Title:             w.Title,
		Desc:              w.Desc,
		Hashtags:          append([]string(nil), w.Hashtags...),
		CreateTime:        w.CreateTime,
		DurationSec:       w.DurationSec,
		IsTop:             w.IsTop,
		Author:            mapDouyinAuthorDTO(w.Author),
		CoverURL:          coverURL,
		Stats:             DouyinStatsDTO{DiggCount: w.Stats.DiggCount, CommentCount: w.Stats.CommentCount, CollectCount: w.Stats.CollectCount, ShareCount: w.Stats.ShareCount},
		Uncacheable:       w.Uncacheable,
		UncacheableReason: w.UncacheableReason,
		CanonicalURL:      w.CanonicalURL,
		CacheKey:          douyinCacheKeyForKind(w.Kind, w.ID),
		CachedItem:        cached,
	}
	if w.Video != nil {
		if len(w.Video.URLs) > 0 {
			out.VideoURL = w.Video.URLs[0]
		}
		out.VideoCodec = w.Video.Codec
		out.QualityLabel = w.Video.QualityLabel
		out.GearName = w.Video.GearName
		out.Width = w.Video.Width
		out.Height = w.Video.Height
		out.DataSize = w.Video.DataSize
	}
	if len(w.Pages) > 0 {
		out.Pages = make([]DouyinMediaPageDTO, 0, len(w.Pages))
		for _, p := range w.Pages {
			dto := DouyinMediaPageDTO{
				Index:         p.Index,
				Kind:          p.Kind,
				Width:         p.Width,
				Height:        p.Height,
				UnknownReason: p.UnknownReason,
			}
			if len(p.ImageURLs) > 0 {
				dto.ImageURL = p.ImageURLs[0]
			}
			if p.VideoStream != nil {
				dto.DurationSec = p.VideoStream.DurationSec
				dto.Codec = p.VideoStream.Codec
				dto.QualityLabel = p.VideoStream.QualityLabel
				if len(p.VideoStream.URLs) > 0 {
					if p.Kind == douyin.NotePageLivePhoto {
						dto.MotionURL = p.VideoStream.URLs[0]
					} else {
						dto.VideoURL = p.VideoStream.URLs[0]
					}
				}
			}
			out.Pages = append(out.Pages, dto)
		}
	}
	if w.Music != nil {
		m := &DouyinMusicDTO{
			ID:              w.Music.ID,
			Title:           w.Music.Title,
			Author:          w.Music.Author,
			DurationSec:     w.Music.DurationSec,
			IsOriginalSound: w.Music.IsOriginalSound,
			CoverURL:        w.Music.CoverURL,
		}
		if len(w.Music.PlayURLs) > 0 {
			m.PlayURL = w.Music.PlayURLs[0]
			m.Playable = true
		} else {
			m.UnavailableHint = "该配乐暂不支持在线试听"
		}
		out.Music = m
	}
	return out
}

func mapDouyinWorkCard(w douyin.WorkDetail) DouyinWorkCardDTO {
	var coverURL string
	if len(w.CoverURLs) > 0 {
		coverURL = w.CoverURLs[0]
	} else if len(w.Pages) > 0 && len(w.Pages[0].ImageURLs) > 0 {
		coverURL = w.Pages[0].ImageURLs[0]
	}
	return DouyinWorkCardDTO{
		ID:                w.ID,
		Kind:              w.Kind,
		Title:             w.Title,
		Desc:              w.Desc,
		CreateTime:        w.CreateTime,
		DurationSec:       w.DurationSec,
		IsTop:             w.IsTop,
		CoverURL:          coverURL,
		PageCount:         len(w.Pages),
		Uncacheable:       w.Uncacheable,
		UncacheableReason: w.UncacheableReason,
		CanonicalURL:      w.CanonicalURL,
		CacheKey:          douyinCacheKeyForKind(w.Kind, w.ID),
	}
}

func mapDouyinUserView(u *douyin.UserProfile, liveRoom *LiveAccountState) *DouyinUserView {
	if u == nil {
		return nil
	}
	liveStatus := 0
	if u.LiveStatus == 1 {
		liveStatus = 1
	}
	if liveRoom != nil && liveRoom.LiveStatus == 1 {
		liveStatus = 1
	}
	return &DouyinUserView{
		SecUID:         u.SecUID,
		UID:            u.UID,
		UniqueID:       u.UniqueID,
		ShortID:        u.ShortID,
		Nickname:       u.Nickname,
		Signature:      u.Signature,
		AvatarURL:      u.AvatarURL,
		AwemeCount:     u.AwemeCount,
		FollowerCount:  u.FollowerCount,
		FollowingCount: u.FollowingCount,
		TotalFavorited: u.TotalFavorited,
		IsPrivate:      u.IsPrivate,
		LiveStatus:     liveStatus,
		RoomID:         u.RoomID,
		WebRID:         u.WebRID,
		CanonicalURL:   u.CanonicalURL,
		LiveRoom:       liveRoom,
	}
}

func formatAccountKey(siteID site.ID, uid string) string {
	if key, err := site.FormatAccountKey(siteID, uid); err == nil {
		return key
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	return string(siteID) + ":" + uid
}

func mapRoomToAccountState(rs live.RoomState) LiveAccountState {
	if strings.EqualFold(rs.Site, string(site.Douyin)) {
		uidStr := strings.TrimSpace(rs.AccountUID)
		accountKey := ""
		if uidStr != "" {
			accountKey = formatAccountKey(site.Douyin, uidStr)
		}
		roomStr := strings.TrimSpace(rs.RoomIDStr)
		if roomStr == "" && rs.RoomID > 0 {
			roomStr = strconv.FormatInt(rs.RoomID, 10)
		}
		recKey := strings.TrimSpace(rs.RoomKey)
		if recKey == "" && roomStr != "" {
			recKey = "douyin:live:" + roomStr
		}
		audioOnly := rs.LiveState == site.LiveStateAudioOnly
		liveStatus := rs.LiveStatus
		if audioOnly {
			liveStatus = 1
		}
		return LiveAccountState{
			Site:              string(site.Douyin),
			AccountKey:        accountKey,
			RecordingKey:      recKey,
			UID:               uidStr,
			RoomID:            roomStr,
			WebRID:            rs.WebRID,
			Uname:             rs.Uname,
			Face:              rs.Face,
			Title:             rs.Title,
			Cover:             rs.Cover,
			AreaName:          rs.AreaName,
			NoRoom:            rs.LiveState == site.LiveStateNone,
			LiveStatus:        liveStatus,
			LiveSince:         rs.LiveSince,
			StatusText:        rs.StatusText,
			QualityName:       rs.QualityName,
			Codec:             rs.Codec,
			Transport:         string(site.LiveTransportHTTPFLV),
			AudioOnly:         audioOnly,
			Recording:         rs.Recording,
			RecordSession:     rs.RecordSession,
			RecordDurationSec: rs.RecordDurationSec,
			RecordSpeedBps:    rs.RecordSpeedBps,
			RecordBytes:       rs.RecordBytes,
			RecordSegments:    rs.RecordSegments,
			RecordItemDir:     rs.RecordItemDir,
			StopReason:        rs.StopReason,
			LastError:         rs.LastError,
			UpdatedAt:         rs.UpdatedAt,
		}
	}

	uidStr := strings.TrimSpace(rs.AccountUID)
	accountKey := ""
	if rs.UID > 0 {
		uidStr = strconv.FormatInt(rs.UID, 10)
	}
	if uidStr != "" {
		accountKey = formatAccountKey(site.Bilibili, uidStr)
	}
	roomStr := strings.TrimSpace(rs.RoomIDStr)
	if roomStr == "" && rs.RoomID > 0 {
		roomStr = strconv.FormatInt(rs.RoomID, 10)
	}
	recKey := strings.TrimSpace(rs.RoomKey)
	if recKey == "" && roomStr != "" {
		recKey = "bilibili:live:" + roomStr
	}
	noRoom := rs.RoomID <= 0
	statusText := rs.StatusText
	if noRoom && statusText == "" {
		statusText = "未开通直播间"
	}
	return LiveAccountState{
		Site:              string(site.Bilibili),
		AccountKey:        accountKey,
		RecordingKey:      recKey,
		UID:               uidStr,
		Mid:               rs.UID,
		RoomID:            roomStr,
		Uname:             rs.Uname,
		Face:              rs.Face,
		Title:             rs.Title,
		Cover:             rs.Cover,
		AreaName:          rs.AreaName,
		NoRoom:            noRoom,
		LiveStatus:        rs.LiveStatus,
		LiveSince:         rs.LiveSince,
		StatusText:        statusText,
		QualityName:       rs.QualityName,
		Codec:             rs.Codec,
		Transport:         string(site.LiveTransportHLSFMP4),
		Recording:         rs.Recording,
		RecordSession:     rs.RecordSession,
		RecordDurationSec: rs.RecordDurationSec,
		RecordSpeedBps:    rs.RecordSpeedBps,
		RecordBytes:       rs.RecordBytes,
		RecordSegments:    rs.RecordSegments,
		RecordItemDir:     rs.RecordItemDir,
		StopReason:        rs.StopReason,
		LastError:         rs.LastError,
		UpdatedAt:         rs.UpdatedAt,
	}
}

func mapBilibiliRoomToAccountState(rs live.RoomState) LiveAccountState {
	return mapRoomToAccountState(rs)
}

func mapSiteLiveStatusToAccountState(st site.LiveStatus) LiveAccountState {
	siteStr := string(st.Site)
	if siteStr == "" {
		siteStr = string(site.Douyin)
	}
	accountKey := ""
	if st.AccountUID != "" {
		accountKey = formatAccountKey(site.ID(siteStr), st.AccountUID)
	}
	recKey := ""
	if st.RoomID != "" {
		recKey = siteStr + ":live:" + st.RoomID
	}
	liveStatus := 0
	statusText := "未开播"
	noRoom := false
	audioOnly := false
	switch st.Status {
	case site.LiveStateLive:
		liveStatus = 1
		statusText = "直播中"
	case site.LiveStateAudioOnly:
		liveStatus = 1
		audioOnly = true
		statusText = "纯语音直播"
	case site.LiveStateRound:
		liveStatus = 2
		statusText = "轮播中"
	case site.LiveStateNone:
		noRoom = true
		statusText = "未开通直播间"
	default:
		statusText = "未开播"
	}
	transport := string(site.LiveTransportHTTPFLV)
	if st.Site == site.Bilibili {
		transport = string(site.LiveTransportHLSFMP4)
	}
	return LiveAccountState{
		Site:         siteStr,
		AccountKey:   accountKey,
		RecordingKey: recKey,
		UID:          st.AccountUID,
		RoomID:       st.RoomID,
		WebRID:       st.WebRID,
		Uname:        st.Uname,
		Face:         st.Face,
		Title:        st.Title,
		Cover:        st.Cover,
		AreaName:     st.AreaName,
		NoRoom:       noRoom,
		LiveStatus:   liveStatus,
		LiveSince:    st.LiveSince,
		StatusText:   statusText,
		QualityName:  st.QualityName,
		Codec:        st.Codec,
		Transport:    transport,
		AudioOnly:    audioOnly,
	}
}
