package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
)

// RenewalStatus summarizes automatic session renewal capability and state for a site.
type RenewalStatus struct {
	Site          string `json:"site"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	LastRenewedAt string `json:"lastRenewedAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// UserVideosPageOptions specifies sorting, keyword search, and partition filtering
// for Bilibili creator uploads (/x/space/wbi/arc/search).
type UserVideosPageOptions struct {
	Order   string `json:"order,omitempty"`   // "pubdate" (latest), "click" (most played), "stow" (most favorited)
	Keyword string `json:"keyword,omitempty"` // keyword filter submitted explicitly by user
	TID     int    `json:"tid,omitempty"`     // partition id; 0 = all partitions
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
	Index             int      `json:"index"` // 1-based
	PicID             string   `json:"picId,omitempty"`
	ObjectID          string   `json:"objectId,omitempty"`
	Kind              string   `json:"kind"` // "image", "gif", "live_photo", "video"
	FromOrigin        bool     `json:"fromOrigin,omitempty"`
	Width             int      `json:"width,omitempty"`
	Height            int      `json:"height,omitempty"`
	DurationSec       int      `json:"durationSec,omitempty"`
	ImageURL          string   `json:"imageUrl,omitempty"`
	FallbackURLs      []string `json:"-"`
	PublicPreviewURL  string   `json:"publicPreviewUrl,omitempty"`
	MotionURL         string   `json:"motionUrl,omitempty"`
	NeedsLivePhotoHop bool     `json:"needsLivePhotoHop,omitempty"`
	Codec             string   `json:"codec,omitempty"`
	QualityLabel      string   `json:"qualityLabel,omitempty"`
	FPS               int      `json:"fps,omitempty"`
	Bitrate           int64    `json:"bitrate,omitempty"`
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

// WeiboEditRevisionDTO holds one normalized historical revision of a Weibo post
// returned by GET /ajax/statuses/editHistory for online viewing (DEC-094).
type WeiboEditRevisionDTO struct {
	Version int                 `json:"version"`
	EditTs  int64               `json:"editTs,omitempty"`
	Text    string              `json:"text,omitempty"`
	Links   []TextLinkDTO       `json:"links,omitempty"`
	Images  []WeiboMediaItemDTO `json:"images,omitempty"`
}

// WeiboPostView holds the normalized UI-facing view of a single Weibo post.
// TextLinkDTO holds a human-readable hyperlink label paired with a safe HTTP(S) target URL (DEC-073).
type TextLinkDTO struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// NormalizeLegacyWeiboAppURL converts allowlisted legacy Weibo mobile deep links
// (sinaweibo://pageinfo?containerid=... and sinaweibo://userinfo?uid=...) into
// canonical desktop HTTPS URLs, returning "" for any unknown or unsafe input.
func NormalizeLegacyWeiboAppURL(rawURL string) string {
	return weibo.RewriteSafeSinaWeiboAppURL(rawURL)
}

type WeiboPostView struct {
	Mid               string                `json:"mid"`
	MblogID           string                `json:"mblogid,omitempty"`
	Author            WeiboAuthorDTO        `json:"author"`
	CreatedAt         int64                 `json:"createdAt"`
	Text              string                `json:"text"`
	TextRaw           string                `json:"textRaw,omitempty"`
	Links             []TextLinkDTO         `json:"links,omitempty"`
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
	IsMuteUser      bool   `json:"isMuteUser,omitempty"`
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

// XAuthorDTO holds normalized X creator summary fields for UI rendering
// without requiring internal/ui to import internal/site/x.
type XAuthorDTO struct {
	RestID         string `json:"restId"`
	ScreenName     string `json:"screenName"`
	Name           string `json:"name"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	Description    string `json:"description,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	IsBlueVerified bool   `json:"isBlueVerified,omitempty"`
	Protected      bool   `json:"protected,omitempty"`
	StatusesCount  int    `json:"statusesCount,omitempty"`
	MediaCount     int    `json:"mediaCount,omitempty"`
	FollowersCount int64  `json:"followersCount,omitempty"`
	FriendsCount   int64  `json:"friendsCount,omitempty"`
}

// XStatsDTO holds online-only engagement counters for an X post (never persisted per DEC-040).
type XStatsDTO struct {
	ReplyCount    int64 `json:"replyCount"`
	RetweetCount  int64 `json:"retweetCount"`
	LikeCount     int64 `json:"likeCount"`
	QuoteCount    int64 `json:"quoteCount"`
	BookmarkCount int64 `json:"bookmarkCount"`
	ViewCount     int64 `json:"viewCount"`
}

// XURLEntityDTO holds one restored t.co -> expanded URL mapping from a post's entities.
type XURLEntityDTO struct {
	ShortURL    string `json:"shortUrl,omitempty"`
	ExpandedURL string `json:"expandedUrl,omitempty"`
	DisplayURL  string `json:"displayUrl,omitempty"`
}

// XMediaItemDTO represents one ordered media attachment (photo, GIF, or video, up to 4 items).
type XMediaItemDTO struct {
	Index            int    `json:"index"` // 1-based
	MediaKey         string `json:"mediaKey,omitempty"`
	Kind             string `json:"kind"` // "photo", "gif", "video"
	GIF              bool   `json:"gif,omitempty"`
	FromOrigin       bool   `json:"fromOrigin,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	DurationMS       int64  `json:"durationMs,omitempty"`
	DurationSec      int    `json:"durationSec,omitempty"`
	AltText          string `json:"altText,omitempty"`
	ImageURL         string `json:"imageUrl,omitempty"`
	PublicPreviewURL string `json:"publicPreviewUrl,omitempty"`
	VideoURL         string `json:"videoUrl,omitempty"`
	VideoBitrate     int64  `json:"videoBitrate,omitempty"`
	QualityLabel     string `json:"qualityLabel,omitempty"`
	HLSOnly          bool   `json:"hlsOnly,omitempty"`
	HLSOnlyNotice    string `json:"hlsOnlyNotice,omitempty"`
}

// XPollOptionDTO represents one option in an X poll summary.
type XPollOptionDTO struct {
	Position int    `json:"position"`
	Label    string `json:"label"`
	Votes    int64  `json:"votes,omitempty"`
}

// XPollSummaryDTO holds online-only poll summary fields for an X post.
type XPollSummaryDTO struct {
	Options    []XPollOptionDTO `json:"options"`
	EndTime    string           `json:"endTime,omitempty"`
	Final      bool             `json:"final,omitempty"`
	TotalVotes int64            `json:"totalVotes,omitempty"`
}

// XRichCardDTO represents a degraded non-media card (poll, URL card, X Article, Broadcast/Spaces).
type XRichCardDTO struct {
	Kind        string `json:"kind"` // "vote", "webpage", "article", "live"
	Label       string `json:"label"`
	Title       string `json:"title,omitempty"`
	Desc        string `json:"desc,omitempty"`
	URL         string `json:"url,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	Unsupported bool   `json:"unsupported"`
}

// XPostDTO holds the normalized UI-facing view of a single X post.
type XPostDTO struct {
	ID                      string                `json:"id"`
	Author                  XAuthorDTO            `json:"author"`
	CreatedAt               int64                 `json:"createdAt"`
	Text                    string                `json:"text"`
	IsLongText              bool                  `json:"isLongText,omitempty"`
	ConversationID          string                `json:"conversationId,omitempty"`
	InReplyToStatusID       string                `json:"inReplyToStatusId,omitempty"`
	InReplyToUserID         string                `json:"inReplyToUserId,omitempty"`
	InReplyToScreenName     string                `json:"inReplyToScreenName,omitempty"`
	SelfThreadID            string                `json:"selfThreadId,omitempty"`
	Pinned                  bool                  `json:"pinned,omitempty"`
	IsPreviewDisplay        bool                  `json:"isPreviewDisplay,omitempty"`
	VisibilityLabel         string                `json:"visibilityLabel,omitempty"`
	PossiblySensitive       bool                  `json:"possiblySensitive,omitempty"`
	WithheldInCountries     []string              `json:"withheldInCountries,omitempty"`
	EditTweetIDs            []string              `json:"editTweetIds,omitempty"`
	IsEdited                bool                  `json:"isEdited,omitempty"`
	IsRetweet               bool                  `json:"isRetweet,omitempty"`
	IsQuote                 bool                  `json:"isQuote,omitempty"`
	Retweeted               *XPostDTO             `json:"retweeted,omitempty"`
	Quoted                  *XPostDTO             `json:"quoted,omitempty"`
	OriginUnavailable       bool                  `json:"originUnavailable,omitempty"`
	OriginUnavailableReason string                `json:"originUnavailableReason,omitempty"`
	ForwarderHintUID        string                `json:"forwarderHintUid,omitempty"`
	ForwarderHintName       string                `json:"forwarderHintName,omitempty"`
	ThreadAncestors         []XPostDTO            `json:"threadAncestors,omitempty"`
	Thread                  []XPostDTO            `json:"thread,omitempty"`
	RuleType                string                `json:"ruleType"` // "forward", "video", "draw", "word"
	Media                   []XMediaItemDTO       `json:"media,omitempty"`
	HasHLSOnlyVideo         bool                  `json:"hasHlsOnlyVideo,omitempty"`
	Poll                    *XPollSummaryDTO      `json:"poll,omitempty"`
	Card                    *XRichCardDTO         `json:"card,omitempty"`
	Article                 *XArticleDTO          `json:"article,omitempty"`
	Stats                   XStatsDTO             `json:"stats"`
	Links                   []TextLinkDTO         `json:"links,omitempty"`
	URLs                    []XURLEntityDTO       `json:"urls,omitempty"`
	CanonicalURL            string                `json:"canonicalUrl"`
	Unavailable             bool                  `json:"unavailable,omitempty"`
	UnavailableReason       string                `json:"unavailableReason,omitempty"`
	UnavailableKind         string                `json:"unavailableKind,omitempty"`
	CacheKey                string                `json:"cacheKey"`
	CacheState              library.DotState      `json:"cacheState"`
	CachedItem              *library.ItemMetadata `json:"cachedItem,omitempty"`
}

// XArticleImageDTO describes the cover or an inline image inside an X Article (DEC-093).
type XArticleImageDTO struct {
	MediaID          string `json:"mediaId,omitempty"`
	MediaKey         string `json:"mediaKey,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	AltText          string `json:"altText,omitempty"`
	ImageURL         string `json:"imageUrl,omitempty"`
	PublicPreviewURL string `json:"publicPreviewUrl,omitempty"`
}

// XArticleBlockDTO describes one ordered rich-text, inline-image, or embedded-tweet
// block parsed from an X Article's Draft.js content_state (DEC-093).
type XArticleBlockDTO struct {
	Type    string            `json:"type"`
	Text    string            `json:"text,omitempty"`
	Links   []TextLinkDTO     `json:"links,omitempty"`
	Image   *XArticleImageDTO `json:"image,omitempty"`
	TweetID string            `json:"tweetId,omitempty"`
}

// XArticleDTO carries full parsed rich-text content for an X Article attached to a post.
type XArticleDTO struct {
	ID           string             `json:"id,omitempty"`
	RestID       string             `json:"restId,omitempty"`
	Title        string             `json:"title,omitempty"`
	PreviewText  string             `json:"previewText,omitempty"`
	PlainText    string             `json:"plainText,omitempty"`
	Cover        *XArticleImageDTO  `json:"cover,omitempty"`
	Blocks       []XArticleBlockDTO `json:"blocks,omitempty"`
	CanonicalURL string             `json:"canonicalUrl,omitempty"`
}

// HasFullContent reports whether the article DTO carries full rich blocks or plain text
// beyond a degraded preview card stub.
func (a *XArticleDTO) HasFullContent() bool {
	if a == nil {
		return false
	}
	return len(a.Blocks) > 0 || strings.TrimSpace(a.PlainText) != ""
}

// XListDTO holds read-only metadata of a public X List (DEC-093).
type XListDTO struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	Mode            string     `json:"mode,omitempty"`
	MemberCount     int        `json:"memberCount"`
	SubscriberCount int        `json:"subscriberCount,omitempty"`
	CreatedAt       int64      `json:"createdAt,omitempty"`
	BannerURL       string     `json:"bannerUrl,omitempty"`
	Creator         XAuthorDTO `json:"creator"`
	CanonicalURL    string     `json:"canonicalUrl"`
}

// XListTweetsPageDTO holds one paginated page of tweets in a public X List.
type XListTweetsPageDTO struct {
	ListID     string     `json:"listId"`
	Posts      []XPostDTO `json:"posts"`
	NextCursor string     `json:"nextCursor,omitempty"`
	HasMore    bool       `json:"hasMore"`
}

// XListMemberDTO describes one member of a public X List with local subscription ownership.
type XListMemberDTO struct {
	RestID         string `json:"restId"`
	ScreenName     string `json:"screenName"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	IsBlueVerified bool   `json:"isBlueVerified,omitempty"`
	Protected      bool   `json:"protected,omitempty"`
	FollowersCount int64  `json:"followersCount,omitempty"`
	CanonicalURL   string `json:"canonicalUrl,omitempty"`
	Subscribed     bool   `json:"subscribed,omitempty"`
	OwnerSubID     string `json:"ownerSubId,omitempty"`
	OwnerSubName   string `json:"ownerSubName,omitempty"`
}

// XListMembersPageDTO holds one paginated page of members in a public X List.
type XListMembersPageDTO struct {
	ListID     string           `json:"listId"`
	Members    []XListMemberDTO `json:"members"`
	NextCursor string           `json:"nextCursor,omitempty"`
	HasMore    bool             `json:"hasMore"`
}

// XUserListsPageDTO holds public X Lists associated with an X creator.
type XUserListsPageDTO struct {
	UserRestID string     `json:"userRestId"`
	Lists      []XListDTO `json:"lists"`
	NextCursor string     `json:"nextCursor,omitempty"`
	HasMore    bool       `json:"hasMore"`
}

// XUserDTO holds an X creator's profile header fields and local subscription status.
type XUserDTO struct {
	RestID         string   `json:"restId"`
	ScreenName     string   `json:"screenName"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	AvatarURL      string   `json:"avatarUrl,omitempty"`
	BannerURL      string   `json:"bannerUrl,omitempty"`
	Verified       bool     `json:"verified,omitempty"`
	IsBlueVerified bool     `json:"isBlueVerified,omitempty"`
	FollowersCount int64    `json:"followersCount"`
	FriendsCount   int64    `json:"friendsCount"`
	StatusesCount  int      `json:"statusesCount"`
	MediaCount     int      `json:"mediaCount,omitempty"`
	Protected      bool     `json:"protected,omitempty"`
	PinnedTweetIDs []string `json:"pinnedTweetIds,omitempty"`
	CanonicalURL   string   `json:"canonicalUrl"`
	Subscribed     bool     `json:"subscribed,omitempty"`
	OwnerSubID     string   `json:"ownerSubId,omitempty"`
	OwnerSubName   string   `json:"ownerSubName,omitempty"`
}

// XUserPostsPageDTO holds one paginated page of an X creator's timeline.
type XUserPostsPageDTO struct {
	Tab                 string      `json:"tab,omitempty"`
	Posts               []XPostDTO  `json:"posts"`
	NextCursor          string      `json:"nextCursor,omitempty"`
	HasMore             bool        `json:"hasMore"`
	ReachedVisibleLimit bool        `json:"reachedVisibleLimit,omitempty"`
	BottomTipsVisible   bool        `json:"bottomTipsVisible,omitempty"`
	BottomTipsText      string      `json:"bottomTipsText,omitempty"`
	UncertainEmpty      bool        `json:"uncertainEmpty,omitempty"`
	UncertainReason     string      `json:"uncertainReason,omitempty"`
	Author              *XAuthorDTO `json:"author,omitempty"`
	TotalWorks          int         `json:"totalWorks,omitempty"`
}

// MaxXCurrentPageBatchCache caps how many uncached X posts from the currently loaded
// tab page can be enqueued into download_tasks in a single manual click (DEC-092).
const MaxXCurrentPageBatchCache = 20

// XBatchCachePageRequest enqueues uncached, cacheable posts from an already loaded X
// profile tab page (replies/media/highlights) directly into download_tasks without
// altering cache-queue.json v3 schema (DEC-092).
type XBatchCachePageRequest struct {
	UserUID  string     `json:"userUid,omitempty"`
	UserName string     `json:"userName,omitempty"`
	Tab      string     `json:"tab,omitempty"`
	Posts    []XPostDTO `json:"posts"`
	Limit    int        `json:"limit,omitempty"`
}

// XBatchCachePageResult summarizes the outcome of CacheXPostsPage.
type XBatchCachePageResult struct {
	Enqueued           int             `json:"enqueued"`
	Tasks              []download.Task `json:"tasks,omitempty"`
	SkippedCached      int             `json:"skippedCached,omitempty"`
	SkippedUnsupported int             `json:"skippedUnsupported,omitempty"`
	SkippedCapped      int             `json:"skippedCapped,omitempty"`
	Failed             int             `json:"failed,omitempty"`
	FirstError         string          `json:"firstError,omitempty"`
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
	UniqueID     string `json:"uniqueId,omitempty"`
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
			FallbackURLs:      append([]string(nil), img.FallbackURLs...),
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

// MapSiteTextLinks converts cross-site TextLink items to UI-facing TextLinkDTO items.
func MapSiteTextLinks(links []site.TextLink) []TextLinkDTO {
	if len(links) == 0 {
		return nil
	}
	out := make([]TextLinkDTO, 0, len(links))
	for _, l := range links {
		lbl := strings.TrimSpace(l.Label)
		u := strings.TrimSpace(l.URL)
		if lbl == "" || u == "" {
			continue
		}
		out = append(out, TextLinkDTO{Label: lbl, URL: u})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MapItemLinksToDTO converts cached item.json ItemLink records to UI-facing TextLinkDTO items.
func MapItemLinksToDTO(links []library.ItemLink) []TextLinkDTO {
	if len(links) == 0 {
		return nil
	}
	out := make([]TextLinkDTO, 0, len(links))
	for _, l := range links {
		lbl := strings.TrimSpace(l.Label)
		u := strings.TrimSpace(l.URL)
		if lbl == "" || u == "" {
			continue
		}
		out = append(out, TextLinkDTO{Label: lbl, URL: u})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapWeiboEditRevisions(revs []weibo.EditRevision) []WeiboEditRevisionDTO {
	if len(revs) == 0 {
		return nil
	}
	out := make([]WeiboEditRevisionDTO, 0, len(revs))
	for _, rev := range revs {
		out = append(out, WeiboEditRevisionDTO{
			Version: rev.Version,
			EditTs:  rev.EditTs,
			Text:    rev.Text,
			Links:   MapSiteTextLinks(rev.Links),
			Images:  mapWeiboMediaItems(rev.Images),
		})
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
	links := MapSiteTextLinks(p.Links)
	if len(links) == 0 && cached != nil {
		links = MapItemLinksToDTO(cached.Links)
	}
	out := &WeiboPostView{
		Mid:               p.Mid,
		MblogID:           p.MblogID,
		Author:            mapWeiboAuthorDTO(p.Author),
		CreatedAt:         p.CreatedAt,
		Text:              p.Text,
		TextRaw:           p.TextRaw,
		Links:             links,
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
		IsMuteUser:      u.IsMuteUser,
		CanonicalURL:    u.CanonicalURL,
	}
}

func mapXAuthorDTO(a x.AuthorSummary) XAuthorDTO {
	return XAuthorDTO{
		RestID:         a.RestID,
		ScreenName:     a.ScreenName,
		Name:           a.Name,
		AvatarURL:      a.AvatarURL,
		Description:    a.Description,
		Verified:       a.Verified,
		IsBlueVerified: a.IsBlueVerified,
		Protected:      a.Protected,
		StatusesCount:  a.StatusesCount,
		MediaCount:     a.MediaCount,
		FollowersCount: a.FollowersCount,
		FriendsCount:   a.FriendsCount,
	}
}

func mapXRichCardDTO(c *x.RichCard) *XRichCardDTO {
	if c == nil {
		return nil
	}
	return &XRichCardDTO{
		Kind:        c.Kind,
		Label:       c.Label,
		Title:       c.Title,
		Desc:        c.Desc,
		URL:         c.URL,
		CoverURL:    c.CoverURL,
		Unsupported: c.Unsupported,
	}
}

func mapXArticleImageDTO(img *x.ArticleImage) *XArticleImageDTO {
	if img == nil {
		return nil
	}
	return &XArticleImageDTO{
		MediaID:          img.MediaID,
		MediaKey:         img.MediaKey,
		Width:            img.Width,
		Height:           img.Height,
		AltText:          img.AltText,
		ImageURL:         firstNonEmpty(img.ImageURL, img.PublicPreviewURL),
		PublicPreviewURL: img.PublicPreviewURL,
	}
}

func mapXArticleDTO(art *x.Article) *XArticleDTO {
	if art == nil {
		return nil
	}
	out := &XArticleDTO{
		ID:           art.ID,
		RestID:       art.RestID,
		Title:        art.Title,
		PreviewText:  art.PreviewText,
		PlainText:    art.PlainText,
		Cover:        mapXArticleImageDTO(art.Cover),
		CanonicalURL: art.CanonicalURL,
	}
	if len(art.Blocks) > 0 {
		out.Blocks = make([]XArticleBlockDTO, 0, len(art.Blocks))
		for _, blk := range art.Blocks {
			var links []TextLinkDTO
			if len(blk.Links) > 0 {
				links = make([]TextLinkDTO, 0, len(blk.Links))
				for _, l := range blk.Links {
					if u := strings.TrimSpace(l.URL); u != "" {
						links = append(links, TextLinkDTO{
							Label: firstNonEmpty(strings.TrimSpace(l.Label), u),
							URL:   u,
						})
					}
				}
			}
			out.Blocks = append(out.Blocks, XArticleBlockDTO{
				Type:    blk.Type,
				Text:    blk.Text,
				Links:   links,
				Image:   mapXArticleImageDTO(blk.Image),
				TweetID: blk.TweetID,
			})
		}
	}
	return out
}

func mapXListDTO(ls *x.ListSummary) *XListDTO {
	if ls == nil {
		return nil
	}
	return &XListDTO{
		ID:              ls.ID,
		Name:            ls.Name,
		Description:     ls.Description,
		Mode:            ls.Mode,
		MemberCount:     ls.MemberCount,
		SubscriberCount: ls.SubscriberCount,
		BannerURL:       ls.BannerURL,
		Creator:         mapXAuthorDTO(ls.Creator),
		CanonicalURL:    ls.CanonicalURL,
	}
}

func mapXListMemberDTO(u x.FollowingUser, ownerSubID, ownerSubName string) XListMemberDTO {
	handle := strings.TrimPrefix(strings.TrimSpace(u.ScreenName), "@")
	canonURL := ""
	if handle != "" {
		canonURL = "https://x.com/" + handle
	} else if strings.TrimSpace(u.RestID) != "" {
		canonURL = "https://x.com/i/user/" + strings.TrimSpace(u.RestID)
	}
	return XListMemberDTO{
		RestID:       u.RestID,
		ScreenName:   handle,
		Name:         u.Name,
		Description:  u.Description,
		AvatarURL:    u.AvatarURL,
		CanonicalURL: canonURL,
		Subscribed:   strings.TrimSpace(ownerSubID) != "",
		OwnerSubID:   ownerSubID,
		OwnerSubName: ownerSubName,
	}
}

func mapXPollSummaryDTO(p *x.PollSummary) *XPollSummaryDTO {
	if p == nil {
		return nil
	}
	opts := make([]XPollOptionDTO, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, XPollOptionDTO{
			Position: o.Position,
			Label:    o.Label,
			Votes:    o.Votes,
		})
	}
	return &XPollSummaryDTO{
		Options:    opts,
		EndTime:    p.EndTime,
		Final:      p.Final,
		TotalVotes: p.TotalVotes,
	}
}

func formatXMediaQualityLabel(width, height int, bitrate int64) string {
	switch {
	case height >= 2160 || width >= 3840:
		return "2160P"
	case height >= 1440 || width >= 2560:
		return "1440P"
	case height >= 1080 || width >= 1920:
		return "1080P"
	case height >= 720 || width >= 1280:
		return "720P"
	case height >= 480 || width >= 854:
		return "480P"
	case height > 0:
		return strconv.Itoa(height) + "P"
	case bitrate > 0:
		return strconv.FormatInt(bitrate/1000, 10) + "kbps"
	default:
		return ""
	}
}

func mapXMediaItems(items []x.MediaItem) []XMediaItemDTO {
	if len(items) == 0 {
		return nil
	}
	out := make([]XMediaItemDTO, 0, len(items))
	for i, m := range items {
		idx := m.Index
		if idx <= 0 {
			idx = i + 1
		}
		dto := XMediaItemDTO{
			Index:            idx,
			MediaKey:         m.MediaKey,
			Kind:             m.Kind,
			GIF:              m.GIF || m.Kind == x.MediaKindGIF,
			FromOrigin:       m.FromOrigin,
			Width:            m.Width,
			Height:           m.Height,
			DurationMS:       m.DurationMS,
			DurationSec:      m.DurationSec,
			AltText:          m.AltText,
			ImageURL:         firstNonEmpty(m.ImageURL, m.PublicPreviewURL),
			PublicPreviewURL: m.PublicPreviewURL,
			VideoBitrate:     m.VideoBitrate,
			QualityLabel:     formatXMediaQualityLabel(m.Width, m.Height, m.VideoBitrate),
			HLSOnly:          m.HLSOnly,
			HLSOnlyNotice:    m.HLSOnlyNotice,
		}
		if !m.HLSOnly {
			dto.VideoURL = m.VideoURL
		}
		if dto.HLSOnly && dto.HLSOnlyNotice == "" {
			dto.HLSOnlyNotice = x.HLSOnlyNoticeText
		}
		out = append(out, dto)
	}
	return out
}

func mapXPostDTO(p *x.Post, cached *library.ItemMetadata, cacheState library.DotState) *XPostDTO {
	if p == nil {
		return nil
	}
	cacheKey := ""
	if strings.TrimSpace(p.ID) != "" {
		cacheKey = x.PostItemKey(p.ID)
	}
	if cacheState.Key == "" && cacheKey != "" {
		cacheState.Key = cacheKey
	}
	var urls []XURLEntityDTO
	if len(p.URLs) > 0 {
		urls = make([]XURLEntityDTO, 0, len(p.URLs))
		for _, u := range p.URLs {
			urls = append(urls, XURLEntityDTO{
				ShortURL:    u.ShortURL,
				ExpandedURL: u.ExpandedURL,
				DisplayURL:  u.DisplayURL,
			})
		}
	}
	links := MapSiteTextLinks(x.BuildPostLinks(p))
	if len(links) == 0 && cached != nil {
		links = MapItemLinksToDTO(cached.Links)
	}
	out := &XPostDTO{
		ID:                      p.ID,
		Author:                  mapXAuthorDTO(p.Author),
		CreatedAt:               p.CreatedAt,
		Text:                    p.Text,
		IsLongText:              p.IsLongText,
		ConversationID:          p.ConversationID,
		InReplyToStatusID:       p.InReplyToStatusID,
		InReplyToUserID:         p.InReplyToUserID,
		InReplyToScreenName:     p.InReplyToScreenName,
		SelfThreadID:            p.SelfThreadID,
		Pinned:                  p.Pinned,
		IsPreviewDisplay:        p.IsPreviewDisplay,
		VisibilityLabel:         p.VisibilityLabel,
		PossiblySensitive:       p.PossiblySensitive,
		WithheldInCountries:     append([]string(nil), p.WithheldInCountries...),
		EditTweetIDs:            append([]string(nil), p.EditTweetIDs...),
		IsEdited:                p.IsEdited,
		IsRetweet:               p.IsRetweet,
		IsQuote:                 p.IsQuote,
		OriginUnavailable:       p.OriginUnavailable,
		OriginUnavailableReason: p.OriginUnavailableReason,
		ForwarderHintUID:        "",
		ForwarderHintName:       "",
		RuleType:                p.RuleType,
		Media:                   mapXMediaItems(p.Media),
		HasHLSOnlyVideo:         p.HasHLSOnlyVideo,
		Poll:                    mapXPollSummaryDTO(p.Poll),
		Card:                    mapXRichCardDTO(p.Card),
		Article:                 mapXArticleDTO(p.Article),
		Stats: XStatsDTO{
			ReplyCount:    p.Stats.ReplyCount,
			RetweetCount:  p.Stats.RetweetCount,
			LikeCount:     p.Stats.LikeCount,
			QuoteCount:    p.Stats.QuoteCount,
			BookmarkCount: p.Stats.BookmarkCount,
			ViewCount:     p.Stats.ViewCount,
		},
		Links:             links,
		URLs:              urls,
		CanonicalURL:      p.CanonicalURL,
		Unavailable:       p.Unavailable,
		UnavailableReason: p.UnavailableReason,
		UnavailableKind:   p.UnavailableKind,
		CacheKey:          cacheKey,
		CacheState:        cacheState,
		CachedItem:        cached,
	}
	if p.Retweeted != nil {
		out.Retweeted = mapXPostDTO(p.Retweeted, nil, library.DotState{})
	}
	if p.Quoted != nil {
		out.Quoted = mapXPostDTO(p.Quoted, nil, library.DotState{})
	}
	if len(p.ThreadAncestors) > 0 {
		out.ThreadAncestors = make([]XPostDTO, 0, len(p.ThreadAncestors))
		for i := range p.ThreadAncestors {
			if mapped := mapXPostDTO(&p.ThreadAncestors[i], nil, library.DotState{}); mapped != nil {
				out.ThreadAncestors = append(out.ThreadAncestors, *mapped)
			}
		}
	}
	if len(p.Thread) > 0 {
		out.Thread = make([]XPostDTO, 0, len(p.Thread))
		for i := range p.Thread {
			if mapped := mapXPostDTO(&p.Thread[i], nil, library.DotState{}); mapped != nil {
				out.Thread = append(out.Thread, *mapped)
			}
		}
	}
	return out
}

func mapXUserDTO(u *x.UserProfile, ownerSubID, ownerSubName string) *XUserDTO {
	if u == nil {
		return nil
	}
	return &XUserDTO{
		RestID:         u.RestID,
		ScreenName:     u.ScreenName,
		Name:           u.Name,
		Description:    u.Description,
		AvatarURL:      u.AvatarURL,
		BannerURL:      u.BannerURL,
		Verified:       u.Verified,
		IsBlueVerified: u.IsBlueVerified,
		FollowersCount: u.FollowersCount,
		FriendsCount:   u.FriendsCount,
		StatusesCount:  u.StatusesCount,
		MediaCount:     u.MediaCount,
		Protected:      u.Protected,
		PinnedTweetIDs: append([]string(nil), u.PinnedTweetIDs...),
		CanonicalURL:   u.CanonicalURL,
		Subscribed:     strings.TrimSpace(ownerSubID) != "",
		OwnerSubID:     ownerSubID,
		OwnerSubName:   ownerSubName,
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
