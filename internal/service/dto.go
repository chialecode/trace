package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
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
	SecUID         string `json:"secUid"`
	UID            string `json:"uid,omitempty"`
	UniqueID       string `json:"uniqueId,omitempty"`
	ShortID        string `json:"shortId,omitempty"`
	Nickname       string `json:"nickname"`
	Signature      string `json:"signature,omitempty"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	AwemeCount     int    `json:"awemeCount"`
	FollowerCount  int64  `json:"followerCount"`
	FollowingCount int64  `json:"followingCount"`
	TotalFavorited int64  `json:"totalFavorited"`
	IsPrivate      bool   `json:"isPrivate,omitempty"`
	// PrivateVisible: the account is private but shows its works to the
	// signed-in account (an approved follower).
	PrivateVisible bool              `json:"privateVisible,omitempty"`
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

// WeiboAuthorDTO holds normalized Weibo creator metadata for UI rendering
// without requiring internal/ui to import internal/site/weibo.
type WeiboAuthorDTO struct {
	UID            string `json:"uid"`
	ScreenName     string `json:"screenName"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	Domain         string `json:"domain,omitempty"`
	Description    string `json:"description,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	VerifiedType   int    `json:"verifiedType,omitempty"`
	VerifiedReason string `json:"verifiedReason,omitempty"`
	StatusesCount  int    `json:"statusesCount,omitempty"`
	FollowersCount int64  `json:"followersCount,omitempty"`
	FriendsCount   int64  `json:"friendsCount,omitempty"`
}

// WeiboStatsDTO holds online-only engagement counters for a Weibo post (never cached to disk per DEC-040).
type WeiboStatsDTO struct {
	RepostsCount   int64 `json:"repostsCount"`
	CommentsCount  int64 `json:"commentsCount"`
	AttitudesCount int64 `json:"attitudesCount"`
}

// WeiboMediaItemDTO represents one ordered media attachment (image, GIF, LivePhoto, or mixed video).
type WeiboMediaItemDTO struct {
	Index             int    `json:"index"` // 1-based
	PicID             string `json:"picId,omitempty"`
	ObjectID          string `json:"objectId,omitempty"`
	Kind              string `json:"kind"` // "image", "gif", "live_photo", "video"
	FromOrigin        bool   `json:"fromOrigin,omitempty"`
	Width             int    `json:"width,omitempty"`
	Height            int    `json:"height,omitempty"`
	DurationSec       int    `json:"durationSec,omitempty"`
	ImageURL          string `json:"imageUrl,omitempty"`
	PublicPreviewURL  string `json:"publicPreviewUrl,omitempty"`
	MotionURL         string `json:"motionUrl,omitempty"`
	NeedsLivePhotoHop bool   `json:"needsLivePhotoHop,omitempty"`
	Codec             string `json:"codec,omitempty"`
	QualityLabel      string `json:"qualityLabel,omitempty"`
	FPS               int    `json:"fps,omitempty"`
	Bitrate           int64  `json:"bitrate,omitempty"`
}

// WeiboVideoDTO holds normalized progressive MP4 video metadata attached to a Weibo post or 1034: page.
type WeiboVideoDTO struct {
	ObjectID       string `json:"objectId,omitempty"`
	Title          string `json:"title,omitempty"`
	DurationSec    int    `json:"durationSec,omitempty"`
	CoverURL       string `json:"coverUrl,omitempty"`
	PublicCoverURL string `json:"publicCoverUrl,omitempty"`
	VideoURL       string `json:"videoUrl,omitempty"`
	Codec          string `json:"codec,omitempty"`
	QualityLabel   string `json:"qualityLabel,omitempty"`
	QualityDesc    string `json:"qualityDesc,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	FPS            int    `json:"fps,omitempty"`
	Bitrate        int64  `json:"bitrate,omitempty"`
	Size           int64  `json:"size,omitempty"`
}

// WeiboRichCardDTO represents a degraded non-media card (article, vote, music, webpage, live).
type WeiboRichCardDTO struct {
	Kind        string `json:"kind"` // "article", "vote", "music", "webpage", "live"
	Label       string `json:"label"`
	Title       string `json:"title,omitempty"`
	Desc        string `json:"desc,omitempty"`
	URL         string `json:"url,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	ObjectID    string `json:"objectId,omitempty"`
	Unsupported bool   `json:"unsupported"`
}

// WeiboPostView holds the normalized UI-facing view of a single Weibo post.
type WeiboPostView struct {
	Mid               string                `json:"mid"`
	MblogID           string                `json:"mblogid,omitempty"`
	Author            WeiboAuthorDTO        `json:"author"`
	CreatedAt         int64                 `json:"createdAt"`
	Text              string                `json:"text"`
	TextRaw           string                `json:"textRaw,omitempty"`
	IsLongText        bool                  `json:"isLongText,omitempty"`
	LongTextFetched   bool                  `json:"longTextFetched,omitempty"`
	Source            string                `json:"source,omitempty"`
	RegionName        string                `json:"regionName,omitempty"`
	EditCount         int                   `json:"editCount,omitempty"`
	IsTop             bool                  `json:"isTop,omitempty"`
	IsRetweet         bool                  `json:"isRetweet,omitempty"`
	IsFastRetweet     bool                  `json:"isFastRetweet,omitempty"`
	OriMid            string                `json:"oriMid,omitempty"`
	Retweeted         *WeiboPostView        `json:"retweeted,omitempty"`
	OriginIncomplete  bool                  `json:"originIncomplete,omitempty"`
	ForwarderHintUID  string                `json:"forwarderHintUid,omitempty"`
	ForwarderHintName string                `json:"forwarderHintName,omitempty"`
	RuleType          string                `json:"ruleType"` // "forward", "video", "draw", "word"
	VisibleType       int                   `json:"visibleType,omitempty"`
	VisibleLabel      string                `json:"visibleLabel,omitempty"`
	Images            []WeiboMediaItemDTO   `json:"images,omitempty"`
	Video             *WeiboVideoDTO        `json:"video,omitempty"`
	Card              *WeiboRichCardDTO     `json:"card,omitempty"`
	Stats             WeiboStatsDTO         `json:"stats"`
	Unavailable       bool                  `json:"unavailable,omitempty"`
	UnavailableReason string                `json:"unavailableReason,omitempty"`
	CanonicalURL      string                `json:"canonicalUrl"`
	CacheKey          string                `json:"cacheKey"`
	CacheState        library.DotState      `json:"cacheState"`
	CachedItem        *library.ItemMetadata `json:"cachedItem,omitempty"`
}

// WeiboVideoView holds the result of opening a standalone 1034:<id> Weibo video page.
// When HostPost is non-nil, the video resolved to its host post (weibo:dynamic:<mid>);
// otherwise it uses the standalone key weibo:video:1034_<id>.
type WeiboVideoView struct {
	ObjectID     string                `json:"objectId"`
	HostMid      string                `json:"hostMid,omitempty"`
	HostPost     *WeiboPostView        `json:"hostPost,omitempty"`
	Title        string                `json:"title,omitempty"`
	AuthorUID    string                `json:"authorUid,omitempty"`
	AuthorName   string                `json:"authorName,omitempty"`
	Video        *WeiboVideoDTO        `json:"video,omitempty"`
	CanonicalURL string                `json:"canonicalUrl"`
	CacheKey     string                `json:"cacheKey"`
	CacheState   library.DotState      `json:"cacheState"`
	CachedItem   *library.ItemMetadata `json:"cachedItem,omitempty"`
}

// WeiboUserView holds a Weibo creator's profile header fields.
type WeiboUserView struct {
	UID             string `json:"uid"`
	ScreenName      string `json:"screenName"`
	AvatarURL       string `json:"avatarUrl,omitempty"`
	Description     string `json:"description,omitempty"`
	StatusesCount   int    `json:"statusesCount"`
	FollowersCount  int64  `json:"followersCount"`
	FriendsCount    int64  `json:"friendsCount"`
	Verified        bool   `json:"verified,omitempty"`
	VerifiedType    int    `json:"verifiedType,omitempty"`
	VerifiedReason  string `json:"verifiedReason,omitempty"`
	Domain          string `json:"domain,omitempty"`
	CoverImagePhone string `json:"coverImagePhone,omitempty"`
	BlockText       string `json:"blockText,omitempty"`
	CanonicalURL    string `json:"canonicalUrl"`
}

// WeiboUserPostsPage holds one paginated page of a Weibo creator's timeline.
type WeiboUserPostsPage struct {
	Posts             []WeiboPostView `json:"posts"`
	Page              int             `json:"page"`
	NextCursor        string          `json:"nextCursor,omitempty"`
	HasMore           bool            `json:"hasMore"`
	BottomTipsVisible bool            `json:"bottomTipsVisible,omitempty"`
	BottomTipsText    string          `json:"bottomTipsText,omitempty"`
	UncertainEmpty    bool            `json:"uncertainEmpty,omitempty"`
	UncertainReason   string          `json:"uncertainReason,omitempty"`
	TotalWorks        int             `json:"totalWorks,omitempty"`
}

// FollowingGroupDTO represents a selectable following group for a site in the import dialog.
type FollowingGroupDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

// FollowingCreatorDTO represents one creator returned by a site's following list.
type FollowingCreatorDTO struct {
	Site         string `json:"site"`
	UID          string `json:"uid"`
	Name         string `json:"name"`
	Face         string `json:"face,omitempty"`
	Sign         string `json:"sign,omitempty"`
	Subscribed   bool   `json:"subscribed"`
	OwnerSubID   string `json:"ownerSubId,omitempty"`
	OwnerSubName string `json:"ownerSubName,omitempty"`
}

// FollowingCreatorsPageDTO holds one page of a site's following list for subscription import.
type FollowingCreatorsPageDTO struct {
	Site          string                `json:"site"`
	Items         []FollowingCreatorDTO `json:"items"`
	Page          int                   `json:"page"`
	NextPage      int                   `json:"nextPage,omitempty"`
	NextCursor    string                `json:"nextCursor,omitempty"`
	Total         int                   `json:"total,omitempty"`
	HasMore       bool                  `json:"hasMore"`
	GroupsEnabled bool                  `json:"groupsEnabled"`
}

// LiveAccountState describes the unified live broadcast and recording state of any <site>:<uid>
// creator or room across Bilibili and Douyin.
type LiveAccountState struct {
	Site              string                 `json:"site"`                   // "bilibili" or "douyin"
	AccountKey        string                 `json:"accountKey"`             // "<site>:<uid>"
	RecordingKey      string                 `json:"recordingKey,omitempty"` // "<site>:live:<room_id>" or decimal room ID
	UID               string                 `json:"uid"`                    // Bilibili decimal mid or Douyin sec_uid
	Mid               int64                  `json:"mid,omitempty"`          // Bilibili numeric mid when site=="bilibili"
	RoomID            string                 `json:"roomId,omitempty"`       // Bilibili decimal roomId or Douyin room_id_str
	WebRID            string                 `json:"webRid,omitempty"`       // Douyin web_rid (browser room number)
	Uname             string                 `json:"uname"`
	Face              string                 `json:"face,omitempty"`
	Title             string                 `json:"title,omitempty"`
	Cover             string                 `json:"cover,omitempty"`
	AreaName          string                 `json:"areaName,omitempty"`
	NoRoom            bool                   `json:"noRoom,omitempty"`
	LiveStatus        int                    `json:"liveStatus"` // 0=offline, 1=live, 2=carousel
	LiveSince         int64                  `json:"liveSince,omitempty"`
	StatusText        string                 `json:"statusText"` // "未开通直播间", "未开播", "直播中", "轮播中"
	QualityName       string                 `json:"qualityName,omitempty"`
	Codec             string                 `json:"codec,omitempty"`
	Transport         string                 `json:"transport,omitempty"` // "hls_fmp4" or "http_flv"
	AudioOnly         bool                   `json:"audioOnly,omitempty"`
	Recording         bool                   `json:"recording"`
	RecordSession     string                 `json:"recordSession,omitempty"`
	RecordDurationSec float64                `json:"recordDurationSec,omitempty"`
	RecordSpeedBps    int64                  `json:"recordSpeedBps,omitempty"`
	RecordBytes       int64                  `json:"recordBytes,omitempty"`
	RecordSegments    int                    `json:"recordSegments,omitempty"`
	RecordItemDir     string                 `json:"recordItemDir,omitempty"`
	RecordPartPath    string                 `json:"recordPartPath,omitempty"`
	RecordPartIndex   int                    `json:"recordPartIndex,omitempty"`
	RecordPartBytes   int64                  `json:"recordPartBytes,omitempty"`
	RecordPartReady   bool                   `json:"recordPartReady,omitempty"`
	PreviewSupported  bool                   `json:"previewSupported,omitempty"`
	PreviewReason     string                 `json:"previewReason,omitempty"`
	Incidents         []library.LiveIncident `json:"incidents,omitempty"`
	StopReason        string                 `json:"stopReason,omitempty"`
	LastError         string                 `json:"lastError,omitempty"`
	UpdatedAt         string                 `json:"updatedAt,omitempty"`
}

// LiveRecordingPreviewInfo exposes active live recording .part file and transport metadata for live preview playback.
type LiveRecordingPreviewInfo struct {
	RecordingKey     string                 `json:"recordingKey"`
	Site             string                 `json:"site"`
	AccountUID       string                 `json:"accountUid,omitempty"`
	RoomID           string                 `json:"roomId"`
	WebRID           string                 `json:"webRid,omitempty"`
	Uname            string                 `json:"uname"`
	Title            string                 `json:"title,omitempty"`
	Codec            string                 `json:"codec,omitempty"`
	Quality          int                    `json:"quality,omitempty"`
	QualityName      string                 `json:"qualityName,omitempty"`
	Transport        string                 `json:"transport,omitempty"` // "hls_fmp4" or "http_flv"
	AudioOnly        bool                   `json:"audioOnly,omitempty"`
	Recording        bool                   `json:"recording"`
	PartPath         string                 `json:"partPath,omitempty"`
	PartIndex        int                    `json:"partIndex,omitempty"`
	PartBytes        int64                  `json:"partBytes,omitempty"`
	PartDurationSec  float64                `json:"partDurationSec,omitempty"`
	PartReady        bool                   `json:"partReady,omitempty"`
	PreviewSupported bool                   `json:"previewSupported"`
	PreviewReason    string                 `json:"previewReason,omitempty"`
	ItemDir          string                 `json:"itemDir,omitempty"`
	DurationSec      float64                `json:"durationSec,omitempty"`
	SpeedBps         int64                  `json:"speedBps,omitempty"`
	BytesWritten     int64                  `json:"bytesWritten,omitempty"`
	Segments         int                    `json:"segments,omitempty"`
	Incidents        []library.LiveIncident `json:"incidents,omitempty"`
	StopReason       string                 `json:"stopReason,omitempty"`
	LastError        string                 `json:"lastError,omitempty"`
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

func mapWeiboAuthorDTO(a weibo.AuthorSummary) WeiboAuthorDTO {
	return WeiboAuthorDTO{
		UID:            a.UID,
		ScreenName:     a.ScreenName,
		AvatarURL:      a.AvatarURL,
		Domain:         a.Domain,
		Description:    a.Description,
		Verified:       a.Verified,
		VerifiedType:   a.VerifiedType,
		VerifiedReason: a.VerifiedReason,
		StatusesCount:  a.StatusesCount,
		FollowersCount: a.FollowersCount,
		FriendsCount:   a.FriendsCount,
	}
}

func mapWeiboVideoDTO(v *weibo.VideoAsset) *WeiboVideoDTO {
	if v == nil {
		return nil
	}
	out := &WeiboVideoDTO{
		ObjectID:       v.ObjectID,
		Title:          v.Title,
		DurationSec:    v.DurationSec,
		CoverURL:       firstNonEmpty(v.CoverURL, v.PublicCoverURL),
		PublicCoverURL: v.PublicCoverURL,
	}
	if v.Selected != nil {
		out.VideoURL = v.Selected.URL
		out.Codec = v.Selected.Codec
		out.QualityLabel = v.Selected.QualityLabel
		out.QualityDesc = v.Selected.QualityDesc
		out.Width = v.Selected.Width
		out.Height = v.Selected.Height
		out.FPS = v.Selected.FPS
		out.Bitrate = v.Selected.Bitrate
		out.Size = v.Selected.Size
		if out.DurationSec <= 0 && v.Selected.DurationSec > 0 {
			out.DurationSec = v.Selected.DurationSec
		}
	}
	return out
}

func mapWeiboRichCardDTO(c *weibo.RichCard) *WeiboRichCardDTO {
	if c == nil {
		return nil
	}
	return &WeiboRichCardDTO{
		Kind:        c.Kind,
		Label:       c.Label,
		Title:       c.Title,
		Desc:        c.Desc,
		URL:         c.URL,
		CoverURL:    c.CoverURL,
		ObjectID:    c.ObjectID,
		Unsupported: c.Unsupported,
	}
}

func mapWeiboMediaItems(assets []weibo.ImageAsset) []WeiboMediaItemDTO {
	if len(assets) == 0 {
		return nil
	}
	out := make([]WeiboMediaItemDTO, 0, len(assets))
	for i, img := range assets {
		idx := img.Index
		if idx <= 0 {
			idx = i + 1
		}
		dto := WeiboMediaItemDTO{
			Index:             idx,
			PicID:             img.PicID,
			ObjectID:          img.ObjectID,
			Kind:              img.Kind,
			FromOrigin:        img.FromOrigin,
			Width:             img.Width,
			Height:            img.Height,
			DurationSec:       img.DurationSec,
			ImageURL:          firstNonEmpty(img.URL, img.PublicPreviewURL),
			PublicPreviewURL:  img.PublicPreviewURL,
			MotionURL:         img.MotionURL,
			NeedsLivePhotoHop: img.NeedsLivePhotoHop,
		}
		if img.VideoStream != nil {
			dto.Codec = img.VideoStream.Codec
			dto.QualityLabel = img.VideoStream.QualityLabel
			dto.FPS = img.VideoStream.FPS
			dto.Bitrate = img.VideoStream.Bitrate
			if dto.MotionURL == "" {
				dto.MotionURL = img.VideoStream.URL
			}
			if dto.Width == 0 {
				dto.Width = img.VideoStream.Width
			}
			if dto.Height == 0 {
				dto.Height = img.VideoStream.Height
			}
			if dto.DurationSec == 0 {
				dto.DurationSec = img.VideoStream.DurationSec
			}
		}
		out = append(out, dto)
	}
	return out
}

func mapWeiboPostView(p *weibo.Post, cached *library.ItemMetadata, cacheState library.DotState) *WeiboPostView {
	if p == nil {
		return nil
	}
	cacheKey := ""
	if strings.TrimSpace(p.Mid) != "" {
		cacheKey = weibo.PostItemKey(p.Mid)
	}
	if cacheState.Key == "" && cacheKey != "" {
		cacheState.Key = cacheKey
	}
	out := &WeiboPostView{
		Mid:               p.Mid,
		MblogID:           p.MblogID,
		Author:            mapWeiboAuthorDTO(p.Author),
		CreatedAt:         p.CreatedAt,
		Text:              p.Text,
		TextRaw:           p.TextRaw,
		IsLongText:        p.IsLongText,
		LongTextFetched:   p.LongTextFetched,
		Source:            p.Source,
		RegionName:        p.RegionName,
		EditCount:         p.EditCount,
		IsTop:             p.IsTop,
		IsRetweet:         p.IsRetweet,
		IsFastRetweet:     p.IsFastRetweet,
		OriMid:            p.OriMid,
		OriginIncomplete:  p.OriginIncomplete,
		RuleType:          p.RuleType,
		VisibleType:       p.Visible.Type,
		VisibleLabel:      p.Visible.Label,
		Images:            mapWeiboMediaItems(p.Images),
		Video:             mapWeiboVideoDTO(p.Video),
		Card:              mapWeiboRichCardDTO(p.Card),
		Stats:             WeiboStatsDTO{RepostsCount: p.Stats.RepostsCount, CommentsCount: p.Stats.CommentsCount, AttitudesCount: p.Stats.AttitudesCount},
		Unavailable:       p.Unavailable,
		UnavailableReason: p.UnavailableReason,
		CanonicalURL:      p.CanonicalURL,
		CacheKey:          cacheKey,
		CacheState:        cacheState,
		CachedItem:        cached,
	}
	if p.Retweeted != nil {
		out.Retweeted = mapWeiboPostView(p.Retweeted, nil, library.DotState{})
	}
	return out
}

func mapWeiboUserView(u *weibo.UserProfile) *WeiboUserView {
	if u == nil {
		return nil
	}
	return &WeiboUserView{
		UID:             u.UID,
		ScreenName:      u.ScreenName,
		AvatarURL:       u.AvatarURL,
		Description:     u.Description,
		StatusesCount:   u.StatusesCount,
		FollowersCount:  u.FollowersCount,
		FriendsCount:    u.FriendsCount,
		Verified:        u.Verified,
		VerifiedType:    u.VerifiedType,
		VerifiedReason:  u.VerifiedReason,
		Domain:          u.Domain,
		CoverImagePhone: u.CoverImagePhone,
		BlockText:       u.BlockText,
		CanonicalURL:    u.CanonicalURL,
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
	var siteID site.ID
	switch strings.ToLower(strings.TrimSpace(rs.Site)) {
	case "", string(site.Bilibili):
		siteID = site.Bilibili
	case string(site.Douyin):
		siteID = site.Douyin
	case string(site.Weibo):
		siteID = site.Weibo
	default:
		siteID = site.ID(strings.ToLower(strings.TrimSpace(rs.Site)))
	}
	isDouyin := siteID == site.Douyin
	isBilibili := siteID == site.Bilibili

	uidStr := strings.TrimSpace(rs.AccountUID)
	var mid int64
	if isBilibili && rs.UID > 0 {
		uidStr = strconv.FormatInt(rs.UID, 10)
		mid = rs.UID
	}
	accountKey := ""
	if uidStr != "" {
		accountKey = formatAccountKey(siteID, uidStr)
	}
	roomStr := strings.TrimSpace(rs.RoomIDStr)
	if roomStr == "" && rs.RoomID > 0 {
		roomStr = strconv.FormatInt(rs.RoomID, 10)
	}
	recKey := strings.TrimSpace(rs.RoomKey)
	if recKey == "" && roomStr != "" {
		recKey = string(siteID) + ":live:" + roomStr
	}

	var (
		noRoom     bool
		audioOnly  bool
		liveStatus int
		statusText string
		transport  string
		webRID     string
	)
	if isDouyin {
		audioOnly = rs.AudioOnly || rs.LiveState == site.LiveStateAudioOnly
		liveStatus = rs.LiveStatus
		if audioOnly {
			liveStatus = 1
		}
		noRoom = rs.LiveState == site.LiveStateNone
		statusText = rs.StatusText
		transport = firstNonEmpty(rs.Transport, string(site.LiveTransportHTTPFLV))
		webRID = rs.WebRID
	} else {
		audioOnly = rs.AudioOnly
		liveStatus = rs.LiveStatus
		noRoom = rs.RoomID <= 0
		statusText = rs.StatusText
		if noRoom && statusText == "" {
			statusText = "未开通直播间"
		}
		transport = firstNonEmpty(rs.Transport, string(site.LiveTransportHLSFMP4))
	}

	partPath := rs.RecordPartPath
	if partPath == "" && rs.Recording {
		partPath = resolveRecordingPartPath(rs.RecordItemDir)
	}

	return LiveAccountState{
		Site:              string(siteID),
		AccountKey:        accountKey,
		RecordingKey:      recKey,
		UID:               uidStr,
		Mid:               mid,
		RoomID:            roomStr,
		WebRID:            webRID,
		Uname:             rs.Uname,
		Face:              rs.Face,
		Title:             rs.Title,
		Cover:             rs.Cover,
		AreaName:          rs.AreaName,
		NoRoom:            noRoom,
		LiveStatus:        liveStatus,
		LiveSince:         rs.LiveSince,
		StatusText:        statusText,
		QualityName:       rs.QualityName,
		Codec:             rs.Codec,
		Transport:         transport,
		AudioOnly:         audioOnly,
		Recording:         rs.Recording,
		RecordSession:     rs.RecordSession,
		RecordDurationSec: rs.RecordDurationSec,
		RecordSpeedBps:    rs.RecordSpeedBps,
		RecordBytes:       rs.RecordBytes,
		RecordSegments:    rs.RecordSegments,
		RecordItemDir:     rs.RecordItemDir,
		RecordPartPath:    partPath,
		RecordPartIndex:   rs.RecordPartIndex,
		RecordPartBytes:   rs.RecordPartBytes,
		RecordPartReady:   rs.RecordPartReady,
		PreviewSupported:  rs.PreviewSupported,
		PreviewReason:     rs.PreviewReason,
		Incidents:         append([]library.LiveIncident(nil), rs.Incidents...),
		StopReason:        rs.StopReason,
		LastError:         rs.LastError,
		UpdatedAt:         rs.UpdatedAt,
	}
}

func resolveRecordingPartPath(itemDir string) string {
	dir := strings.TrimSpace(itemDir)
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(e.Name()), ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if newest == "" || info.ModTime().After(newestMod) {
			newest = full
			newestMod = info.ModTime()
		}
	}
	return newest
}
