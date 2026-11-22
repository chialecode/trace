package weibo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
)

// Rule types mapped per docs/research/weibo.md §13.1 #4.
const (
	RuleTypeForward = "forward"
	RuleTypeVideo   = "video"
	RuleTypeDraw    = "draw"
	RuleTypeWord    = "word"
)

// Media item kinds within a Weibo post's ordered media list.
const (
	MediaKindImage     = "image"
	MediaKindGIF       = "gif"
	MediaKindLivePhoto = "live_photo"
	MediaKindVideo     = "video"
)

// Rich card kinds for degraded non-media page_info attachments (weibo.md §4.6).
const (
	CardKindArticle = "article"
	CardKindVote    = "vote"
	CardKindMusic   = "music"
	CardKindWebpage = "webpage"
	CardKindLive    = "live"
)

// StreamSelectOptions controls video stream preference when parsing playback_list.
type StreamSelectOptions struct {
	PreferredCodec string // "avc" (default) or "hevc"
	MaxResolution  int    // optional upper cap on short-edge pixels (0 = unlimited)
}

// PostOptions configures GetPost and GetVideo behavior.
type PostOptions struct {
	StreamOptions       StreamSelectOptions
	SkipLongTextFetch   bool
	SkipOriginFetch     bool
	SkipHostPostLookup  bool
	ImageURLRewriter    func(picID, rawURL string) (preferredURL string, ok bool)
	UnauthenticatedCall bool // used only by ACT-022 V-09 guest-vs-login video quality comparison
}

// AuthorSummary holds normalized creator metadata embedded in posts and profiles.
type AuthorSummary struct {
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

// PostStats holds online-only engagement counters (never cached to disk per DEC-040).
type PostStats struct {
	RepostsCount   int64 `json:"repostsCount"`
	CommentsCount  int64 `json:"commentsCount"`
	AttitudesCount int64 `json:"attitudesCount"`
}

// VisibilityInfo holds Weibo visible.type / list_id metadata (weibo.md §4.7).
type VisibilityInfo struct {
	Type   int    `json:"type"`
	ListID string `json:"listId,omitempty"`
	Label  string `json:"label"`
}

// URLStructItem captures one short-URL mapping from url_struct (used to expand t.cn in text locally).
type URLStructItem struct {
	ShortURL     string `json:"shortUrl,omitempty"`
	LongURL      string `json:"longUrl,omitempty"`
	URLTitle     string `json:"urlTitle,omitempty"`
	InlinePicURL string `json:"-"`
}

// ImageAsset represents one ordered media entry (image, GIF, LivePhoto, or mixed-post video).
// Ephemeral/signed download URLs are tagged json:"-" so they never persist into snapshots or logs.
type ImageAsset struct {
	Index              int          `json:"index"` // 1-based
	PicID              string       `json:"picId,omitempty"`
	ObjectID           string       `json:"objectId,omitempty"`
	Kind               string       `json:"kind"` // MediaKindImage, MediaKindGIF, MediaKindLivePhoto, MediaKindVideo
	FromOrigin         bool         `json:"fromOrigin,omitempty"`
	Width              int          `json:"width,omitempty"`
	Height             int          `json:"height,omitempty"`
	DurationSec        int          `json:"durationSec,omitempty"`
	PublicPreviewURL   string       `json:"publicPreviewUrl,omitempty"` // only unsigned sinaimg.cn URL
	URL                string       `json:"-"`
	FallbackURLs       []string     `json:"-"`
	MotionURL          string       `json:"-"` // LivePhoto .mov/.mp4 or mixed video .mp4
	MotionFallbackURLs []string     `json:"-"`
	GIFVideoURL        string       `json:"-"` // optional transcoded MP4 for GIF (primary is original .gif in URL)
	NeedsLivePhotoHop  bool         `json:"needsLivePhotoHop,omitempty"`
	VideoStream        *VideoStream `json:"videoStream,omitempty"`
}

// VideoStream represents one normalized progressive MP4 stream candidate.
type VideoStream struct {
	Label        string   `json:"label,omitempty"`
	QualityIndex int      `json:"qualityIndex,omitempty"`
	QualityLabel string   `json:"qualityLabel,omitempty"`
	QualityDesc  string   `json:"qualityDesc,omitempty"`
	ShortEdge    int      `json:"shortEdge,omitempty"`
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	FPS          int      `json:"fps,omitempty"`
	Bitrate      int64    `json:"bitrate,omitempty"`
	Size         int64    `json:"size,omitempty"`
	DurationSec  int      `json:"durationSec,omitempty"`
	Codec        string   `json:"codec"` // "avc" or "hevc"
	VideoCodec   string   `json:"videoCodec,omitempty"`
	AudioCodec   string   `json:"audioCodec,omitempty"`
	DynamicRange string   `json:"dynamicRange,omitempty"`
	URL          string   `json:"-"`
	FallbackURLs []string `json:"-"`
}

// VideoAsset represents a downloadable progressive MP4 video attached to a post or 1034: page.
type VideoAsset struct {
	ObjectID       string        `json:"objectId,omitempty"`
	Title          string        `json:"title,omitempty"`
	DurationSec    int           `json:"durationSec,omitempty"`
	PublicCoverURL string        `json:"publicCoverUrl,omitempty"`
	CoverURL       string        `json:"-"`
	Selected       *VideoStream  `json:"selected,omitempty"`
	Streams        []VideoStream `json:"streams,omitempty"`
	LegacyFallback bool          `json:"legacyFallback,omitempty"`
}

// RichCard represents a degraded non-video/non-image card attachment (article, vote, music, webpage, live).
type RichCard struct {
	Kind        string `json:"kind"` // CardKindArticle, CardKindVote, CardKindMusic, CardKindWebpage, CardKindLive
	Label       string `json:"label"`
	Title       string `json:"title,omitempty"`
	Desc        string `json:"desc,omitempty"`
	URL         string `json:"url,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	ObjectID    string `json:"objectId,omitempty"`
	Unsupported bool   `json:"unsupported"`
}

// Post represents a normalized Weibo post (weibo.md §4).
type Post struct {
	Mid               string          `json:"mid"`
	MblogID           string          `json:"mblogid"`
	Author            AuthorSummary   `json:"author"`
	CreatedAt         int64           `json:"createdAt"`
	CreatedAtRaw      string          `json:"createdAtRaw,omitempty"`
	Text              string          `json:"text"`
	TextRaw           string          `json:"textRaw,omitempty"`
	IsLongText        bool            `json:"isLongText,omitempty"`
	LongTextFetched   bool            `json:"longTextFetched,omitempty"`
	Source            string          `json:"source,omitempty"`
	RegionName        string          `json:"regionName,omitempty"`
	EditCount         int             `json:"editCount,omitempty"`
	IsTop             bool            `json:"isTop,omitempty"`
	IsRetweet         bool            `json:"isRetweet,omitempty"`
	IsFastRetweet     bool            `json:"isFastRetweet,omitempty"`
	OriMid            string          `json:"oriMid,omitempty"`
	Retweeted         *Post           `json:"retweeted,omitempty"`
	OriginIncomplete  bool            `json:"originIncomplete,omitempty"`
	RuleType          string          `json:"ruleType"` // RuleTypeForward, RuleTypeVideo, RuleTypeDraw, RuleTypeWord
	Visible           VisibilityInfo  `json:"visible"`
	Images            []ImageAsset    `json:"images,omitempty"`
	Video             *VideoAsset     `json:"video,omitempty"`
	Card              *RichCard       `json:"card,omitempty"`
	Stats             PostStats       `json:"stats"`
	URLMappings       []URLStructItem `json:"urlMappings,omitempty"`
	CanonicalURL      string          `json:"canonicalUrl"`
	Unavailable       bool            `json:"unavailable,omitempty"`
	UnavailableReason string          `json:"unavailableReason,omitempty"`
}

// VideoPageResult holds the result of resolving a standalone 1034:<oid> video page via /tv/api/component.
// When HostPost is non-nil, the 1034: video normalized cleanly onto its host Weibo post.
type VideoPageResult struct {
	ObjectID     string      `json:"objectId"`
	HostMid      string      `json:"hostMid,omitempty"`
	HostPost     *Post       `json:"hostPost,omitempty"`
	Title        string      `json:"title,omitempty"`
	AuthorUID    string      `json:"authorUid,omitempty"`
	AuthorName   string      `json:"authorName,omitempty"`
	Video        *VideoAsset `json:"video,omitempty"`
	CanonicalURL string      `json:"canonicalUrl"`
}

// --- Raw JSON decoding structs (always UseNumber / RawMessage for IDs) ---

type rawWeiboUser struct {
	ID              json.RawMessage `json:"id"`
	IDStr           string          `json:"idstr"`
	ScreenName      string          `json:"screen_name"`
	AvatarHD        string          `json:"avatar_hd"`
	AvatarLarge     string          `json:"avatar_large"`
	ProfileImageURL string          `json:"profile_image_url"`
	Description     string          `json:"description"`
	Domain          string          `json:"domain"`
	Verified        bool            `json:"verified"`
	VerifiedType    json.RawMessage `json:"verified_type"`
	VerifiedReason  string          `json:"verified_reason"`
	StatusesCount   json.RawMessage `json:"statuses_count"`
	FollowersCount  json.RawMessage `json:"followers_count"`
	FriendsCount    json.RawMessage `json:"friends_count"`
	CoverImagePhone string          `json:"cover_image_phone"`
	Remark          string          `json:"remark"`
	SpecialFollow   json.RawMessage `json:"special_follow"`
}

func (u *rawWeiboUser) uidString() string {
	if u == nil {
		return ""
	}
	if s := strings.TrimSpace(u.IDStr); s != "" {
		if norm, err := site.NormalizeUID(site.Weibo, s); err == nil {
			return norm
		}
		return s
	}
	if s := rawNumberOrString(u.ID); s != "" {
		if norm, err := site.NormalizeUID(site.Weibo, s); err == nil {
			return norm
		}
		return s
	}
	return ""
}

func (u *rawWeiboUser) toAuthorSummary(allowLocalhost bool) AuthorSummary {
	if u == nil {
		return AuthorSummary{}
	}
	return AuthorSummary{
		UID:            u.uidString(),
		ScreenName:     strings.TrimSpace(u.ScreenName),
		AvatarURL:      pickBestAvatarURL(u, allowLocalhost),
		Domain:         strings.TrimSpace(u.Domain),
		Description:    strings.TrimSpace(u.Description),
		Verified:       u.Verified,
		VerifiedType:   int(parseJSONInt64(u.VerifiedType)),
		VerifiedReason: strings.TrimSpace(u.VerifiedReason),
		StatusesCount:  int(parseJSONInt64(u.StatusesCount)),
		FollowersCount: parseJSONInt64(u.FollowersCount),
		FriendsCount:   parseJSONInt64(u.FriendsCount),
	}
}

func pickBestAvatarURL(u *rawWeiboUser, allowLocalhost bool) string {
	if u == nil {
		return ""
	}
	for _, cand := range []string{u.AvatarHD, u.AvatarLarge, u.ProfileImageURL} {
		if n := normalizeWeiboMediaURL(cand, allowLocalhost); n != "" {
			return n
		}
	}
	return ""
}

type rawPicVariant struct {
	URL    string          `json:"url"`
	Width  json.RawMessage `json:"width"`
	Height json.RawMessage `json:"height"`
	Type   string          `json:"type"`
}

type rawPicInfo struct {
	PicID      string         `json:"pic_id"`
	Type       string         `json:"type"` // "pic", "gif", "livephoto"
	Video      string         `json:"video"`
	Thumbnail  *rawPicVariant `json:"thumbnail"`
	BMiddle    *rawPicVariant `json:"bmiddle"`
	Large      *rawPicVariant `json:"large"`
	MW2000     *rawPicVariant `json:"mw2000"`
	Original   *rawPicVariant `json:"original"`
	Largest    *rawPicVariant `json:"largest"`
	LargeCover *rawPicVariant `json:"largecover"`
	WOriginal  *rawPicVariant `json:"woriginal"`
}

type rawPlaybackMeta struct {
	Label        string          `json:"label"`
	QualityIndex json.RawMessage `json:"quality_index"`
	QualityLabel string          `json:"quality_label"`
	QualityDesc  string          `json:"quality_desc"`
	QualityClass string          `json:"quality_class"`
	IsHidden     json.RawMessage `json:"is_hidden"`
}

type rawPlaybackPlayInfo struct {
	URL        string          `json:"url"`
	Mime       string          `json:"mime"`
	Protocol   string          `json:"protocol"`
	Width      json.RawMessage `json:"width"`
	Height     json.RawMessage `json:"height"`
	FPS        json.RawMessage `json:"fps"`
	Bitrate    json.RawMessage `json:"bitrate"`
	Size       json.RawMessage `json:"size"`
	Duration   json.RawMessage `json:"duration"`
	VideoCodec string          `json:"video_codecs"`
	AudioCodec string          `json:"audio_codecs"`
	Extension  *struct {
		TranscodeInfo *struct {
			OriginVideoDR string `json:"origin_video_dr"`
		} `json:"transcode_info"`
	} `json:"extension"`
}

type rawPlaybackEntry struct {
	Meta     *rawPlaybackMeta     `json:"meta"`
	PlayInfo *rawPlaybackPlayInfo `json:"play_info"`
}

type rawMediaInfo struct {
	Name         string             `json:"name"`
	StreamURL    string             `json:"stream_url"`
	StreamURLHD  string             `json:"stream_url_hd"`
	MP4HDURL     string             `json:"mp4_hd_url"`
	MP4SDURL     string             `json:"mp4_sd_url"`
	MP4720P      string             `json:"mp4_720p_mp4"`
	H265MP4HD    string             `json:"h265_mp4_hd"`
	HEVCMP4720P  string             `json:"hevc_mp4_720p"`
	ReplayHD     string             `json:"replay_hd"`
	LiveStatus   json.RawMessage    `json:"live_status"`
	Duration     json.RawMessage    `json:"duration"`
	PlaybackList []rawPlaybackEntry `json:"playback_list"`
	OnlineUsers  string             `json:"online_users"`
	VideoTitle   string             `json:"video_title"`
	BigPicInfo   *rawPicInfo        `json:"big_pic_info"`
	PicBig       *rawPicVariant     `json:"pic_big"`
	VideoDetails json.RawMessage    `json:"video_details"`
}

type rawPageInfo struct {
	Type       json.RawMessage `json:"type"`
	ObjectType string          `json:"object_type"`
	ObjectID   string          `json:"object_id"`
	PageURL    string          `json:"page_url"`
	PageTitle  string          `json:"page_title"`
	PageDesc   string          `json:"page_desc"`
	Content1   string          `json:"content1"`
	Content2   string          `json:"content2"`
	PagePic    json.RawMessage `json:"page_pic"`
	MediaInfo  *rawMediaInfo   `json:"media_info"`
}

type rawMixMediaItem struct {
	ID   string          `json:"id"`
	Type string          `json:"type"` // "pic" or "video"
	Data json.RawMessage `json:"data"`
}

type rawMixMediaInfo struct {
	Items []rawMixMediaItem `json:"items"`
}

type rawURLStruct struct {
	ShortURL   string                 `json:"short_url"`
	LongURL    string                 `json:"long_url"`
	OriURL     string                 `json:"ori_url"`
	URLTitle   string                 `json:"url_title"`
	ObjectType string                 `json:"object_type"`
	URLTypePic string                 `json:"url_type_pic"`
	PicInfos   map[string]*rawPicInfo `json:"pic_infos"`
}

type rawLongText struct {
	LongTextContent string          `json:"longTextContent"`
	URLStruct       []rawURLStruct  `json:"url_struct"`
	TopicStruct     json.RawMessage `json:"topic_struct"`
}

type rawPost struct {
	ID            json.RawMessage `json:"id"`
	IDStr         string          `json:"idstr"`
	Mid           json.RawMessage `json:"mid"`
	MblogID       string          `json:"mblogid"`
	Bid           string          `json:"bid"`
	CreatedAt     string          `json:"created_at"`
	Text          string          `json:"text"`
	TextRaw       string          `json:"text_raw"`
	IsLongText    json.RawMessage `json:"isLongText"`
	LongText      *rawLongText    `json:"longText"`
	ContinueTag   json.RawMessage `json:"continue_tag"`
	Source        string          `json:"source"`
	RegionName    string          `json:"region_name"`
	EditCount     json.RawMessage `json:"edit_count"`
	IsTop         json.RawMessage `json:"isTop"`
	MblogType     json.RawMessage `json:"mblogtype"`
	IsPaid        json.RawMessage `json:"is_paid"`
	Promotion     json.RawMessage `json:"promotion"`
	ProfileTypeID string          `json:"profile_type_id"`
	OriMid        json.RawMessage `json:"ori_mid"`
	Deleted       json.RawMessage `json:"deleted"`
	Title         *struct {
		Text string `json:"text"`
	} `json:"title"`
	User            *rawWeiboUser          `json:"user"`
	RetweetedStatus *rawPost               `json:"retweeted_status"`
	PicNum          json.RawMessage        `json:"pic_num"`
	PicIDs          []string               `json:"pic_ids"`
	PicInfos        map[string]*rawPicInfo `json:"pic_infos"`
	MixMediaIDs     []string               `json:"mix_media_ids"`
	MixMediaInfo    *rawMixMediaInfo       `json:"mix_media_info"`
	PageInfo        *rawPageInfo           `json:"page_info"`
	URLStruct       []rawURLStruct         `json:"url_struct"`
	Visible         *struct {
		Type   json.RawMessage `json:"type"`
		ListID json.RawMessage `json:"list_id"`
	} `json:"visible"`
	RepostsCount   json.RawMessage `json:"reposts_count"`
	CommentsCount  json.RawMessage `json:"comments_count"`
	AttitudesCount json.RawMessage `json:"attitudes_count"`
}

func (r *rawPost) midString() string {
	if r == nil {
		return ""
	}
	if s := strings.TrimSpace(r.IDStr); s != "" {
		return s
	}
	if s := rawNumberOrString(r.Mid); s != "" {
		return s
	}
	return rawNumberOrString(r.ID)
}

func (r *rawPost) mblogIDString() string {
	if r == nil {
		return ""
	}
	if s := strings.TrimSpace(r.MblogID); s != "" {
		return s
	}
	if s := strings.TrimSpace(r.Bid); s != "" {
		return s
	}
	if mid := r.midString(); mid != "" {
		if mb, err := MidToMblogID(mid); err == nil {
			return mb
		}
	}
	return ""
}

// GetPost fetches and normalizes a single Weibo post via /ajax/statuses/show, expanding
// long text (outer and retweeted) when not inlined and validating returned mid consistency.
func (c *Client) GetPost(ctx context.Context, idOrMblogID string, opts ...PostOptions) (*Post, error) {
	var opt PostOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.StreamOptions.PreferredCodec == "" {
		opt.StreamOptions.PreferredCodec = c.preferredCodec()
	}
	if opt.ImageURLRewriter == nil {
		opt.ImageURLRewriter = c.imageURLRewriter
	}

	reqMid, reqMblogID, ok := parsePostIdentifier(idOrMblogID)
	if !ok {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博编号或 mblogid",
		}
	}

	q := url.Values{}
	q.Set("id", reqMblogID)
	q.Set("locale", "zh-CN")
	q.Set("isGetLongText", "true")

	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:      c.endpoints.StatusesShow,
		query:         q,
		referer:       DefaultReferer,
		guestNoCookie: opt.UnauthenticatedCall,
	})
	if err != nil {
		return nil, err
	}

	var rp rawPost
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&rp); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.StatusesShow),
			Message:  "解析微博详情 JSON 失败",
			Err:      err,
		}
	}

	// Consistency check (weibo.md §4.1 & M6-K07): returned idstr/mid must match requested mid.
	gotMid := rp.midString()
	if gotMid == "" || gotMid != reqMid {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.StatusesShow),
			Message:  fmt.Sprintf("微博详情返回的编号与请求不符（期望 %s，实际 %s）", reqMid, gotMid),
		}
	}

	post, err := c.parseRawPost(&rp, opt)
	if err != nil {
		return nil, err
	}

	// Expand long text and enrich abbreviated retweeted_status when not inlined by isGetLongText=true.
	// Skip on UnauthenticatedCall (e.g. ACT-022 V-09 guest video probes) so unauthenticated probes
	// never leak stored cookies or fail on auth-gated endpoints.
	if !opt.UnauthenticatedCall {
		if !opt.SkipLongTextFetch && needsLongTextFetch(&rp) && post.MblogID != "" {
			lt, ltErr := c.GetLongText(ctx, post.MblogID)
			if ltErr != nil {
				if site.IsRoundStop(ltErr) {
					return nil, ltErr
				}
			} else if lt != nil && strings.TrimSpace(lt.LongTextContent) != "" {
				applyLongText(post, lt, c.allowLocalhost)
			}
		}

		if rp.RetweetedStatus != nil && post.Retweeted != nil && !post.Retweeted.Unavailable {
			rtID := strings.TrimSpace(post.Retweeted.MblogID)
			if rtID == "" {
				rtID = strings.TrimSpace(post.Retweeted.Mid)
			}
			needMedia := needsOriginMediaFetch(rp.RetweetedStatus, post.Retweeted)
			needLong := needsOriginLongTextFetch(rp.RetweetedStatus, post.Retweeted)

			// When retweeted_status omitted media/video details (e.g. pic_ids without pic_infos,
			// pic_num > len(images), mix_media_ids, or video page_info without playback_list),
			// fetch the origin post once with SkipOriginFetch:true (strictly 1-hop, zero recursion).
			if !opt.SkipOriginFetch && needMedia && rtID != "" && c.canCallEndpoint(c.endpoints.StatusesShow) {
				originOpt := opt
				originOpt.SkipOriginFetch = true
				enriched, oErr := c.GetPost(ctx, rtID, originOpt)
				if oErr != nil {
					if site.IsRoundStop(oErr) {
						return nil, oErr
					}
				} else if enriched != nil {
					if enriched.Video == nil && post.Retweeted.Video != nil {
						enriched.Video = post.Retweeted.Video
					}
					if enriched.CanonicalURL == "" && post.Retweeted.CanonicalURL != "" {
						enriched.CanonicalURL = post.Retweeted.CanonicalURL
					}
					post.Retweeted = enriched
					post.OriginIncomplete = false
					needMedia = false
					needLong = false
				}
			}

			// Expand retweeted long post separately when not yet expanded (including >140 runes
			// where isLongText=true without inlined longText, or fallback when origin GetPost degraded).
			if !opt.SkipLongTextFetch && needLong && rtID != "" {
				lt, ltErr := c.GetLongText(ctx, rtID)
				if ltErr != nil {
					if site.IsRoundStop(ltErr) {
						return nil, ltErr
					}
				} else if lt != nil && strings.TrimSpace(lt.LongTextContent) != "" {
					applyLongText(post.Retweeted, lt, c.allowLocalhost)
					if !needMedia {
						post.OriginIncomplete = false
					}
				}
			}
		}
	}

	return post, nil
}

// LongTextData holds the decoded payload of /ajax/statuses/longtext.
type LongTextData struct {
	LongTextContent string
	URLMappings     []URLStructItem
}

// GetLongText fetches full text for a long Weibo post via GET /ajax/statuses/longtext?id=<mblogid>.
func (c *Client) GetLongText(ctx context.Context, mblogIDOrMid string) (*LongTextData, error) {
	id := strings.TrimSpace(mblogIDOrMid)
	if id == "" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "缺少长微博编号",
		}
	}
	// Prefer mblogid per weibo.md §4.2.
	if reMidDigits.MatchString(id) {
		if mb, err := MidToMblogID(id); err == nil {
			id = mb
		}
	}

	q := url.Values{}
	q.Set("id", id)
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint: c.endpoints.StatusesLong,
		query:    q,
		referer:  DefaultReferer,
	})
	if err != nil {
		return nil, err
	}

	var env struct {
		OK   int          `json:"ok"`
		Data *rawLongText `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.StatusesLong),
			Message:  "解析长微博正文 JSON 失败",
			Err:      err,
		}
	}
	if env.Data == nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.StatusesLong),
			Message:  "长微博接口响应缺少 data 字段",
		}
	}
	return &LongTextData{
		LongTextContent: strings.TrimSpace(env.Data.LongTextContent),
		URLMappings:     parseURLStruct(env.Data.URLStruct, c.allowLocalhost),
	}, nil
}

// GetVideo resolves an independent video page (1034:<media_id>) via POST /tv/api/component?page=/tv/show/<oid>.
// When the component returns a valid host post mid and SkipHostPostLookup is false, it normalizes
// onto the host post via GetPost; otherwise it returns the standalone video streams.
func (c *Client) GetVideo(ctx context.Context, oid string, opts ...PostOptions) (*VideoPageResult, error) {
	var opt PostOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.StreamOptions.PreferredCodec == "" {
		opt.StreamOptions.PreferredCodec = c.preferredCodec()
	}

	oid = strings.TrimSpace(oid)
	if decoded, err := url.QueryUnescape(oid); err == nil {
		oid = strings.TrimSpace(decoded)
	}
	if !reVideoOID.MatchString(oid) {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博视频对象编号（需为 1034:...）",
		}
	}

	pagePath := "/tv/show/" + oid
	q := url.Values{}
	q.Set("page", pagePath)

	payloadBytes, _ := json.Marshal(map[string]any{
		"Component_Play_Playinfo": map[string]string{
			"oid": oid,
		},
	})
	form := url.Values{}
	form.Set("data", string(payloadBytes))

	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		method:        http.MethodPost,
		endpoint:      c.endpoints.TVComponent,
		query:         q,
		formBody:      form.Encode(),
		contentType:   "application/x-www-form-urlencoded",
		referer:       "https://weibo.com" + pagePath,
		origin:        DefaultOrigin,
		pageReferer:   pagePath,
		guestNoCookie: opt.UnauthenticatedCall,
	})
	if err != nil {
		return nil, err
	}

	var env struct {
		Code json.RawMessage `json:"code"`
		Data *struct {
			PlayInfo *struct {
				Mid        json.RawMessage   `json:"mid"`
				Title      string            `json:"title"`
				Text       string            `json:"text"`
				Author     string            `json:"author"`
				AuthorID   json.RawMessage   `json:"author_id"`
				CoverImage string            `json:"cover_image"`
				Duration   json.RawMessage   `json:"duration"`
				URLs       map[string]string `json:"urls"`
				StreamHD   string            `json:"stream_url_hd"`
				StreamURL  string            `json:"stream_url"`
			} `json:"Component_Play_Playinfo"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.TVComponent),
			Message:  "解析微博视频组件 JSON 失败",
			Err:      err,
		}
	}
	if env.Data == nil || env.Data.PlayInfo == nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.TVComponent),
			Message:  "微博视频组件响应缺少 Component_Play_Playinfo",
		}
	}

	pi := env.Data.PlayInfo
	hostMid := strings.TrimSpace(rawNumberOrString(pi.Mid))
	if !reMidDigits.MatchString(hostMid) {
		hostMid = ""
	}
	authorUID := strings.TrimSpace(rawNumberOrString(pi.AuthorID))
	if norm, nErr := site.NormalizeUID(site.Weibo, authorUID); nErr == nil {
		authorUID = norm
	}

	title := strings.TrimSpace(pi.Title)
	if title == "" {
		title = strings.TrimSpace(pi.Text)
	}
	coverURL := normalizeWeiboMediaURL(pi.CoverImage, c.allowLocalhost)
	durSec := int(parseJSONFloat64(pi.Duration) + 0.5)

	streams := parseComponentVideoStreams(pi.URLs, pi.StreamHD, pi.StreamURL, durSec, opt.StreamOptions, c.allowLocalhost)
	var videoAsset *VideoAsset
	if len(streams) > 0 {
		sel := streams[0]
		videoAsset = &VideoAsset{
			ObjectID:       oid,
			Title:          title,
			DurationSec:    durSec,
			PublicCoverURL: SafePublicImageURL(coverURL),
			CoverURL:       coverURL,
			Selected:       &sel,
			Streams:        streams,
		}
	}

	result := &VideoPageResult{
		ObjectID:     oid,
		HostMid:      hostMid,
		Title:        title,
		AuthorUID:    authorUID,
		AuthorName:   strings.TrimSpace(pi.Author),
		Video:        videoAsset,
		CanonicalURL: "https://weibo.com/tv/show/" + oid,
	}

	// Normalize to host post when host mid is returned (weibo.md §4.5 #5 & M6-K07).
	if hostMid != "" && !opt.SkipHostPostLookup && c.canCallEndpoint(c.endpoints.StatusesShow) {
		hostPost, pErr := c.GetPost(ctx, hostMid, opt)
		if pErr != nil {
			if site.IsRoundStop(pErr) {
				return nil, pErr
			}
			// Host post deleted/forbidden -> gracefully keep standalone video result.
		} else if hostPost != nil {
			if hostPost.Video == nil && videoAsset != nil {
				hostPost.Video = videoAsset
				hostPost.RuleType = classifyPostRuleType(hostPost)
			} else if hostPost.Video != nil && hostPost.Video.ObjectID == "" {
				hostPost.Video.ObjectID = oid
			}
			result.HostPost = hostPost
			if hostPost.CanonicalURL != "" {
				result.CanonicalURL = hostPost.CanonicalURL
			}
		}
	}

	if result.HostPost == nil && result.Video == nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: site.SanitizeEndpoint(c.endpoints.TVComponent),
			Message:  "该微博视频未提供可播放的视频流",
		}
	}
	return result, nil
}

// ParseRawPostJSON decodes a raw Weibo post JSON object (from statuses/show, mymblog, or favorites)
// into a normalized Post. Exposed for reuse by tools/actcheck and unit tests.
func (c *Client) ParseRawPostJSON(rawJSON []byte, opts ...PostOptions) (*Post, error) {
	var opt PostOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.StreamOptions.PreferredCodec == "" {
		opt.StreamOptions.PreferredCodec = c.preferredCodec()
	}
	if opt.ImageURLRewriter == nil {
		opt.ImageURLRewriter = c.imageURLRewriter
	}
	var rp rawPost
	dec := json.NewDecoder(bytes.NewReader(rawJSON))
	dec.UseNumber()
	if err := dec.Decode(&rp); err != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindSchemaChanged,
			Message: "解析微博对象 JSON 失败",
			Err:     err,
		}
	}
	return c.parseRawPost(&rp, opt)
}

func (c *Client) parseRawPost(rp *rawPost, opt PostOptions) (*Post, error) {
	if rp == nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindSchemaChanged,
			Message: "微博对象为空",
		}
	}
	return c.parseRawPostInternal(rp, opt, false)
}

func (c *Client) parseRawPostInternal(rp *rawPost, opt PostOptions, isRetweetChild bool) (*Post, error) {
	if isRetweetChild && isUnavailableRetweetOrigin(rp) {
		reason := extractUnavailableRetweetReason(rp)
		return &Post{
			Mid:               rp.midString(),
			MblogID:           rp.mblogIDString(),
			Unavailable:       true,
			UnavailableReason: reason,
			RuleType:          RuleTypeWord,
			Visible:           VisibilityInfo{Type: 0, Label: "不可见"},
		}, nil
	}

	mid := rp.midString()
	mblogID := rp.mblogIDString()
	if mid == "" && mblogID != "" {
		if m, err := MblogIDToMid(mblogID); err == nil {
			mid = m
		}
	}
	if mid == "" && !isRetweetChild {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindSchemaChanged,
			Message: "微博对象缺少有效的 idstr/mid",
		}
	}

	author := rp.User.toAuthorSummary(c.allowLocalhost)
	createdTs := parseWeiboCreatedAt(rp.CreatedAt)

	isLongText := parseJSONBoolOrInt(rp.IsLongText)
	urlMappings := parseURLStruct(rp.URLStruct, c.allowLocalhost)
	textRaw := strings.TrimSpace(rp.TextRaw)
	longTextFetched := false
	if rp.LongText != nil && strings.TrimSpace(rp.LongText.LongTextContent) != "" {
		textRaw = strings.TrimSpace(rp.LongText.LongTextContent)
		longTextFetched = true
		if len(rp.LongText.URLStruct) > 0 {
			urlMappings = mergeURLMappings(urlMappings, parseURLStruct(rp.LongText.URLStruct, c.allowLocalhost))
		}
	}
	if textRaw == "" && strings.TrimSpace(rp.Text) != "" {
		textRaw = stripHTMLText(rp.Text)
	}
	displayText := restoreShortURLsInText(textRaw, urlMappings)

	oriMid := strings.TrimSpace(rawNumberOrString(rp.OriMid))
	isFastRetweet := oriMid != "" && oriMid != "0"

	isTop := parseJSONBoolOrInt(rp.IsTop) ||
		parseJSONInt64(rp.MblogType) == 2 ||
		(rp.Title != nil && strings.TrimSpace(rp.Title.Text) == "置顶") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(rp.ProfileTypeID)), "proweibotop")

	vis := parseVisibility(rp.Visible)

	// Parse images / mixed media strictly in mix_media_info.items or pic_ids order (weibo.md §4.4).
	images := c.parsePostMediaItems(rp, opt)

	// Parse page_info as either downloadable video OR degraded rich card (weibo.md §4.5, §4.6).
	video, card := c.parsePageInfo(rp.PageInfo, opt.StreamOptions)

	// If mix_media_info had only 1 item and it is a video (0 images), promote to single Video for §13.1 #4.
	if video == nil && len(images) == 1 && images[0].Kind == MediaKindVideo && images[0].VideoStream != nil {
		item := images[0]
		sel := *item.VideoStream
		video = &VideoAsset{
			ObjectID:       item.ObjectID,
			DurationSec:    item.DurationSec,
			PublicCoverURL: item.PublicPreviewURL,
			CoverURL:       item.URL,
			Selected:       &sel,
			Streams:        []VideoStream{sel},
		}
		images = nil
	}

	var retweeted *Post
	if rp.RetweetedStatus != nil && !isRetweetChild {
		rt, rtErr := c.parseRawPostInternal(rp.RetweetedStatus, opt, true)
		if rtErr == nil {
			retweeted = rt
		}
	}

	// Normalize outer-hoisted retweet video onto retweeted_status (weibo.md §4.3):
	// Weibo often attaches the retweeted video's page_info on the outer status (or on both).
	// Keep the richer video on retweeted and clear the outer duplicate so detail/feed UI never
	// misplaces or duplicates it.
	if retweeted != nil && !retweeted.Unavailable {
		if video != nil {
			if retweeted.Video == nil || videoAssetIsRicher(video, retweeted.Video) {
				retweeted.Video = video
			}
			video = nil
		}
		if card != nil && retweeted.Card != nil && card.ObjectID != "" && card.ObjectID == retweeted.Card.ObjectID {
			card = nil
		}
	}

	originIncomplete := false
	if rp.RetweetedStatus != nil && retweeted != nil && !retweeted.Unavailable {
		originIncomplete = needsOriginPostFetch(rp.RetweetedStatus, retweeted)
	}

	post := &Post{
		Mid:              mid,
		MblogID:          mblogID,
		Author:           author,
		CreatedAt:        createdTs,
		CreatedAtRaw:     strings.TrimSpace(rp.CreatedAt),
		Text:             displayText,
		TextRaw:          textRaw,
		IsLongText:       isLongText,
		LongTextFetched:  longTextFetched,
		Source:           stripHTMLText(rp.Source),
		RegionName:       strings.TrimSpace(rp.RegionName),
		EditCount:        int(parseJSONInt64(rp.EditCount)),
		IsTop:            isTop,
		IsRetweet:        retweeted != nil || isFastRetweet,
		IsFastRetweet:    isFastRetweet,
		OriMid:           oriMid,
		Retweeted:        retweeted,
		OriginIncomplete: originIncomplete,
		Visible:          vis,
		Images:           images,
		Video:            video,
		Card:             card,
		Stats: PostStats{
			RepostsCount:   parseJSONInt64(rp.RepostsCount),
			CommentsCount:  parseJSONInt64(rp.CommentsCount),
			AttitudesCount: parseJSONInt64(rp.AttitudesCount),
		},
		URLMappings:  urlMappings,
		CanonicalURL: canonicalPostURL(author.UID, mid, mblogID),
	}
	post.RuleType = classifyPostRuleType(post)
	return post, nil
}

// EffectiveMedia returns the deduplicated media (images + optional standalone video) across the
// outer post and its retweeted_status (weibo.md §4.3: deduplicate by pic_id and object_id).
// Each returned ImageAsset carries FromOrigin indicating outer vs retweeted ownership.
func (p *Post) EffectiveMedia() ([]ImageAsset, *VideoAsset) {
	imgs, vid, _ := p.EffectiveMediaWithOrigin()
	return imgs, vid
}

// EffectiveMediaWithOrigin returns deduplicated media plus whether a standalone video belongs to
// the retweeted origin post.
func (p *Post) EffectiveMediaWithOrigin() ([]ImageAsset, *VideoAsset, bool) {
	if p == nil {
		return nil, nil, false
	}
	var images []ImageAsset
	seenPic := make(map[string]bool)
	seenObj := make(map[string]bool)

	addImages := func(list []ImageAsset, fromOrigin bool) {
		for _, img := range list {
			if img.PicID != "" && seenPic[img.PicID] {
				continue
			}
			if img.ObjectID != "" && seenObj[img.ObjectID] {
				continue
			}
			if img.PicID != "" {
				seenPic[img.PicID] = true
			}
			if img.ObjectID != "" {
				seenObj[img.ObjectID] = true
			}
			copied := img
			copied.Index = len(images) + 1
			copied.FromOrigin = fromOrigin
			images = append(images, copied)
		}
	}

	addImages(p.Images, false)
	if p.Retweeted != nil && !p.Retweeted.Unavailable {
		addImages(p.Retweeted.Images, true)
	}

	var video *VideoAsset
	videoFromOrigin := false
	if p.Video != nil && p.Video.Selected != nil {
		if p.Video.ObjectID == "" || !seenObj[p.Video.ObjectID] {
			video = p.Video
			if p.Video.ObjectID != "" {
				seenObj[p.Video.ObjectID] = true
			}
		}
	}
	if video == nil && p.Retweeted != nil && !p.Retweeted.Unavailable && p.Retweeted.Video != nil && p.Retweeted.Video.Selected != nil {
		if p.Retweeted.Video.ObjectID == "" || !seenObj[p.Retweeted.Video.ObjectID] {
			video = p.Retweeted.Video
			videoFromOrigin = true
		}
	}

	// If a post has BOTH images and a standalone video (e.g. outer comment image + origin video),
	// keep them consistent: append the video into images so image_note preserves every asset and
	// preserves whether the video came from the retweeted origin!
	if len(images) > 0 && video != nil && video.Selected != nil {
		sel := *video.Selected
		images = append(images, ImageAsset{
			Index:              len(images) + 1,
			ObjectID:           video.ObjectID,
			Kind:               MediaKindVideo,
			FromOrigin:         videoFromOrigin,
			Width:              sel.Width,
			Height:             sel.Height,
			DurationSec:        video.DurationSec,
			PublicPreviewURL:   video.PublicCoverURL,
			URL:                video.CoverURL,
			MotionURL:          sel.URL,
			MotionFallbackURLs: append([]string(nil), sel.FallbackURLs...),
			VideoStream:        &sel,
		})
		video = nil
		videoFromOrigin = false
	}

	return images, video, videoFromOrigin
}

func videoAssetIsRicher(cand, cur *VideoAsset) bool {
	if cur == nil || cur.Selected == nil {
		return cand != nil && cand.Selected != nil
	}
	if cand == nil || cand.Selected == nil {
		return false
	}
	if cur.LegacyFallback && !cand.LegacyFallback {
		return true
	}
	if !cur.LegacyFallback && cand.LegacyFallback {
		return false
	}
	if cand.Selected.ShortEdge != cur.Selected.ShortEdge {
		return cand.Selected.ShortEdge > cur.Selected.ShortEdge
	}
	return len(cand.Streams) > len(cur.Streams)
}

func rawPageInfoLooksLikeVideo(pi *rawPageInfo) bool {
	if pi == nil {
		return false
	}
	objType := strings.ToLower(strings.TrimSpace(pi.ObjectType))
	pageType := strings.ToLower(strings.TrimSpace(rawNumberOrString(pi.Type)))
	if objType == "live" || pageType == "live" || (pi.MediaInfo != nil && parseJSONInt64(pi.MediaInfo.LiveStatus) == 1) {
		return false
	}
	if pageType == "5" || objType == "movie" {
		return false
	}
	if objType == "video" || pageType == "11" || pageType == "video" {
		return true
	}
	if reVideoOID.MatchString(strings.TrimSpace(pi.ObjectID)) {
		return true
	}
	return pi.MediaInfo != nil && len(pi.MediaInfo.PlaybackList) > 0
}

func rawURLStructLooksLikeVideo(items []rawURLStruct) bool {
	for _, it := range items {
		if strings.EqualFold(strings.TrimSpace(it.ObjectType), "video") {
			return true
		}
		if strings.Contains(strings.ToLower(it.URLTypePic), "video") {
			return true
		}
		for _, u := range []string{it.LongURL, it.OriURL} {
			lower := strings.ToLower(strings.TrimSpace(u))
			if strings.Contains(lower, "video.weibo.com") || strings.Contains(lower, "/tv/show/1034:") || strings.Contains(lower, "1034:") {
				return true
			}
		}
	}
	return false
}

// needsOriginMediaFetch reports whether an embedded retweeted_status is missing images, mixed-media
// items, or video playback streams and requires a single statuses/show fetch on the origin post.
func needsOriginMediaFetch(rpRT *rawPost, parsedRT *Post) bool {
	if rpRT == nil || parsedRT == nil || parsedRT.Unavailable {
		return false
	}
	if strings.TrimSpace(parsedRT.Mid) == "" && strings.TrimSpace(parsedRT.MblogID) == "" {
		return false
	}
	picNum := int(parseJSONInt64(rpRT.PicNum))
	if len(rpRT.PicIDs) > len(parsedRT.Images) || picNum > len(parsedRT.Images) {
		return true
	}
	if len(rpRT.MixMediaIDs) > len(parsedRT.Images) {
		return true
	}
	if parsedRT.Video == nil {
		if rpRT.PageInfo != nil && rawPageInfoLooksLikeVideo(rpRT.PageInfo) {
			return true
		}
		if len(parsedRT.Images) == 0 && rawURLStructLooksLikeVideo(rpRT.URLStruct) {
			return true
		}
	}
	return false
}

// needsOriginLongTextFetch reports whether an embedded retweeted_status marks isLongText=true
// without inlined longText (even when text_raw exceeds 140 runes).
func needsOriginLongTextFetch(rpRT *rawPost, parsedRT *Post) bool {
	if rpRT == nil || parsedRT == nil || parsedRT.Unavailable || parsedRT.LongTextFetched {
		return false
	}
	if !parseJSONBoolOrInt(rpRT.IsLongText) {
		return false
	}
	return rpRT.LongText == nil || strings.TrimSpace(rpRT.LongText.LongTextContent) == ""
}

// needsOriginPostFetch reports whether an embedded retweeted_status is incomplete in text or media.
func needsOriginPostFetch(rpRT *rawPost, parsedRT *Post) bool {
	return needsOriginMediaFetch(rpRT, parsedRT) || needsOriginLongTextFetch(rpRT, parsedRT)
}

func classifyPostRuleType(p *Post) string {
	if p == nil {
		return RuleTypeWord
	}
	// Rule per weibo.md §13.1 #4:
	// 1. retweeted_status or ori_mid -> forward
	// 2. images (including mixed media) -> draw
	// 3. sole media is a single video -> video
	// 4. otherwise (pure text or only degraded cards) -> word
	if p.IsRetweet || p.IsFastRetweet || p.Retweeted != nil || p.OriMid != "" {
		return RuleTypeForward
	}
	if len(p.Images) > 0 {
		return RuleTypeDraw
	}
	if p.Video != nil && p.Video.Selected != nil {
		return RuleTypeVideo
	}
	return RuleTypeWord
}

func needsLongTextFetch(rp *rawPost) bool {
	if rp == nil || !parseJSONBoolOrInt(rp.IsLongText) {
		return false
	}
	if rp.LongText != nil && strings.TrimSpace(rp.LongText.LongTextContent) != "" {
		return false
	}
	if !isJSONNull(rp.ContinueTag) {
		return true
	}
	if strings.Contains(rp.Text, `class="expand"`) || strings.Contains(rp.Text, ">展开<") || strings.Contains(rp.Text, "展开全文") {
		return true
	}
	trimmed := strings.TrimSpace(rp.TextRaw)
	if trimmed == "" || strings.HasSuffix(trimmed, "...") || strings.HasSuffix(trimmed, "…") || strings.HasSuffix(trimmed, "全文") {
		return true
	}
	// A Weibo long post exceeds 140 characters; if text_raw is still <= 140 runes, assume truncated.
	return utf8.RuneCountInString(trimmed) <= 140
}

func applyLongText(post *Post, lt *LongTextData, _ bool) {
	if post == nil || lt == nil || lt.LongTextContent == "" {
		return
	}
	post.TextRaw = lt.LongTextContent
	post.URLMappings = mergeURLMappings(post.URLMappings, lt.URLMappings)
	post.Text = restoreShortURLsInText(post.TextRaw, post.URLMappings)
	post.LongTextFetched = true
}

func isUnavailableRetweetOrigin(rp *rawPost) bool {
	if rp == nil {
		return true
	}
	if del := strings.TrimSpace(rawNumberOrString(rp.Deleted)); del == "1" || strings.EqualFold(del, "true") {
		return true
	}
	if rp.User == nil || rp.User.uidString() == "" {
		return true
	}
	rawText := strings.TrimSpace(rp.TextRaw)
	if rawText == "" {
		rawText = stripHTMLText(rp.Text)
	}
	if len(rp.PicIDs) == 0 && rp.PageInfo == nil && rp.MixMediaInfo == nil && isUnavailableOriginPrompt(rawText) {
		return true
	}
	return false
}

func isUnavailableOriginPrompt(s string) bool {
	return containsAny(s,
		"抱歉，此微博已被作者删除",
		"此微博已被作者删除",
		"由于作者设置，你暂时没有这条微博的查看权限",
		"目前内容暂不可见",
		"该微博因被多人举报",
		"暂不可查看",
		"抱歉，作者已设置仅展示",
	)
}

func extractUnavailableRetweetReason(rp *rawPost) string {
	if rp != nil {
		if t := strings.TrimSpace(rp.TextRaw); t != "" {
			return t
		}
		if t := stripHTMLText(rp.Text); t != "" {
			return t
		}
	}
	return "原微博已删除或暂不可见"
}

func parseVisibility(raw *struct {
	Type   json.RawMessage `json:"type"`
	ListID json.RawMessage `json:"list_id"`
}) VisibilityInfo {
	if raw == nil {
		return VisibilityInfo{Type: 0, Label: "公开"}
	}
	vType := int(parseJSONInt64(raw.Type))
	listID := strings.TrimSpace(rawNumberOrString(raw.ListID))
	if listID == "0" {
		listID = ""
	}
	switch vType {
	case 0:
		return VisibilityInfo{Type: 0, ListID: listID, Label: "公开"}
	case 1:
		return VisibilityInfo{Type: 1, ListID: listID, Label: "仅自己可见"}
	default:
		return VisibilityInfo{Type: vType, ListID: listID, Label: "受限可见（粉丝/好友圈/分组）"}
	}
}

func (c *Client) parsePostMediaItems(rp *rawPost, opt PostOptions) []ImageAsset {
	// Priority 1: mix_media_info.items[] preserves exact author ordering of mixed pics + videos (weibo.md §4.4 #2).
	if rp.MixMediaInfo != nil && len(rp.MixMediaInfo.Items) > 0 {
		out := make([]ImageAsset, 0, len(rp.MixMediaInfo.Items))
		for _, item := range rp.MixMediaInfo.Items {
			itemType := strings.ToLower(strings.TrimSpace(item.Type))
			switch itemType {
			case "pic":
				var pi rawPicInfo
				dec := json.NewDecoder(bytes.NewReader(item.Data))
				dec.UseNumber()
				if err := dec.Decode(&pi); err != nil {
					continue
				}
				picID := strings.TrimSpace(pi.PicID)
				if picID == "" {
					picID = strings.TrimSpace(item.ID)
				}
				if asset, ok := c.buildImageAsset(len(out)+1, picID, &pi, opt.ImageURLRewriter); ok {
					out = append(out, asset)
				}

			case "video":
				var mv struct {
					ObjectID  string          `json:"object_id"`
					PagePic   json.RawMessage `json:"page_pic"`
					MediaInfo *rawMediaInfo   `json:"media_info"`
				}
				dec := json.NewDecoder(bytes.NewReader(item.Data))
				dec.UseNumber()
				if err := dec.Decode(&mv); err != nil || mv.MediaInfo == nil {
					continue
				}
				streams, _ := c.selectVideoStreams(mv.MediaInfo, opt.StreamOptions)
				if len(streams) == 0 {
					continue
				}
				sel := streams[0]
				coverURL := extractPagePicURL(mv.PagePic, mv.MediaInfo, c.allowLocalhost)
				oid := strings.TrimSpace(mv.ObjectID)
				if oid == "" {
					oid = strings.TrimSpace(item.ID)
				}
				durSec := sel.DurationSec
				if durSec == 0 {
					durSec = int(parseJSONFloat64(mv.MediaInfo.Duration) + 0.5)
				}
				out = append(out, ImageAsset{
					Index:              len(out) + 1,
					ObjectID:           oid,
					Kind:               MediaKindVideo,
					Width:              sel.Width,
					Height:             sel.Height,
					DurationSec:        durSec,
					PublicPreviewURL:   SafePublicImageURL(coverURL),
					URL:                coverURL,
					MotionURL:          sel.URL,
					MotionFallbackURLs: append([]string(nil), sel.FallbackURLs...),
					VideoStream:        &sel,
				})
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	// Priority 2: pic_ids[] slice order indexing pic_infos[id] (supports >9 / 10..18+ images, never iterates map!).
	if len(rp.PicIDs) > 0 && len(rp.PicInfos) > 0 {
		out := make([]ImageAsset, 0, len(rp.PicIDs))
		for _, picID := range rp.PicIDs {
			picID = strings.TrimSpace(picID)
			if picID == "" {
				continue
			}
			pi := rp.PicInfos[picID]
			if pi == nil {
				continue
			}
			if asset, ok := c.buildImageAsset(len(out)+1, picID, pi, opt.ImageURLRewriter); ok {
				out = append(out, asset)
			}
		}
		return out
	}

	return nil
}

func (c *Client) buildImageAsset(index int, picID string, pi *rawPicInfo, rewriter func(picID, rawURL string) (string, bool)) (ImageAsset, bool) {
	if pi == nil {
		return ImageAsset{}, false
	}
	if picID == "" {
		picID = strings.TrimSpace(pi.PicID)
	}

	// Size priority per weibo.md §4.4 #3 & §13.1 #10: largest > original > mw2000 > large > bmiddle (then thumbnail).
	type tierCand struct {
		priority int
		url      string
		width    int
		height   int
		area     int64
	}
	orderedVariants := []*rawPicVariant{
		pi.Largest,
		pi.Original,
		pi.MW2000,
		pi.Large,
		pi.BMiddle,
		pi.WOriginal,
		pi.Thumbnail,
	}
	cands := make([]tierCand, 0, len(orderedVariants))
	seenURL := make(map[string]bool)
	for i, v := range orderedVariants {
		if v == nil {
			continue
		}
		u := normalizeWeiboMediaURL(v.URL, c.allowLocalhost)
		if u == "" || seenURL[u] {
			continue
		}
		seenURL[u] = true
		w := int(parseJSONInt64(v.Width))
		h := int(parseJSONInt64(v.Height))
		cands = append(cands, tierCand{
			priority: i,
			url:      u,
			width:    w,
			height:   h,
			area:     int64(w) * int64(h),
		})
	}
	if len(cands) == 0 {
		return ImageAsset{}, false
	}

	// Verify largest pixel area among populated tiers while preserving priority order on ties or missing dims.
	bestIdx := 0
	for i := 1; i < len(cands); i++ {
		if cands[bestIdx].area > 0 && cands[i].area > cands[bestIdx].area {
			bestIdx = i
		}
	}
	primary := cands[bestIdx]
	fallbacks := make([]string, 0, len(cands)-1)
	for i, cand := range cands {
		if i == bestIdx {
			continue
		}
		fallbacks = append(fallbacks, cand.url)
	}

	// Optional configurable watermark-free rewrite hook (ready for ACT-022 V-07, disabled by default).
	primaryURL := primary.url
	if rewriter != nil {
		if rewritten, ok := rewriter(picID, primaryURL); ok && rewritten != "" && rewritten != primaryURL {
			fallbacks = append([]string{primaryURL}, fallbacks...)
			primaryURL = rewritten
		}
	}

	picType := strings.ToLower(strings.TrimSpace(pi.Type))
	kind := MediaKindImage
	var motionURL string
	var gifVideoURL string
	var needsLivePhotoHop bool

	switch picType {
	case "gif":
		kind = MediaKindGIF
		if v := normalizeWeiboMediaURL(pi.Video, c.allowLocalhost); v != "" {
			gifVideoURL = v
		}
	case "livephoto":
		kind = MediaKindLivePhoto
		motionURL = normalizeWeiboMediaURL(pi.Video, c.allowLocalhost)
		if motionURL != "" && site.IsWeiboLivePhotoInitialURL(motionURL) {
			needsLivePhotoHop = true
		}
	}

	return ImageAsset{
		Index:             index,
		PicID:             picID,
		Kind:              kind,
		Width:             primary.width,
		Height:            primary.height,
		PublicPreviewURL:  SafePublicImageURL(primary.url),
		URL:               primaryURL,
		FallbackURLs:      fallbacks,
		MotionURL:         motionURL,
		GIFVideoURL:       gifVideoURL,
		NeedsLivePhotoHop: needsLivePhotoHop,
	}, true
}

// RewriteSinaimgPath rewrites a sinaimg.cn size segment (e.g. /mw2000/, /large/, /orj360/)
// to targetSize (e.g. "oslarge" or "osj1080"). Provided as an opt-in helper for ACT-022 V-07.
func RewriteSinaimgPath(rawURL, targetSize string) (string, bool) {
	targetSize = strings.Trim(strings.TrimSpace(targetSize), "/")
	if targetSize == "" {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !site.MatchDomainSuffix(strings.ToLower(u.Hostname()), []string{"sinaimg.cn"}) {
		return "", false
	}
	segs := site.SplitCleanPath(u.Path)
	if len(segs) < 2 {
		return "", false
	}
	switch strings.ToLower(segs[0]) {
	case "mw690", "mw1024", "mw2000", "large", "bmiddle", "orj360", "orj480", "woriginal":
		segs[0] = targetSize
		cloned := *u
		cloned.Path = "/" + strings.Join(segs, "/")
		return cloned.String(), true
	default:
		return "", false
	}
}

func (c *Client) parsePageInfo(pi *rawPageInfo, opts StreamSelectOptions) (*VideoAsset, *RichCard) {
	if pi == nil {
		return nil, nil
	}
	objType := strings.ToLower(strings.TrimSpace(pi.ObjectType))
	pageType := strings.ToLower(strings.TrimSpace(rawNumberOrString(pi.Type)))
	pageURL := strings.TrimSpace(pi.PageURL)
	lowerURL := strings.ToLower(pageURL)
	title := strings.TrimSpace(pi.PageTitle)
	desc := strings.TrimSpace(pi.PageDesc)
	if desc == "" {
		desc = strings.TrimSpace(pi.Content1)
	}

	// 1. Live card MUST NEVER be treated as a downloadable MP4 (weibo.md §4.5 #1, §4.6).
	if objType == "live" || pageType == "live" || (pi.MediaInfo != nil && parseJSONInt64(pi.MediaInfo.LiveStatus) == 1) {
		cover := extractPagePicURL(pi.PagePic, pi.MediaInfo, c.allowLocalhost)
		return nil, &RichCard{
			Kind:        CardKindLive,
			Label:       "直播（暂不支持录制）",
			Title:       title,
			Desc:        desc,
			URL:         safeCardWebURL(pageURL),
			CoverURL:    SafePublicImageURL(cover),
			ObjectID:    strings.TrimSpace(pi.ObjectID),
			Unsupported: true,
		}
	}

	// 2. Movie entry card (type == "5" or object_type == "movie") has no downloadable video stream (§4.5 #1).
	if pageType == "5" || objType == "movie" {
		return nil, &RichCard{
			Kind:        CardKindWebpage,
			Label:       "影视词条",
			Title:       title,
			Desc:        desc,
			URL:         safeCardWebURL(pageURL),
			Unsupported: true,
		}
	}

	// 3. Article card (§4.6).
	if objType == "article" || pageType == "article" || pageType == "24" ||
		strings.Contains(lowerURL, "card.weibo.com/article/") || strings.Contains(lowerURL, "/ttarticle/") {
		cover := extractPagePicURL(pi.PagePic, nil, c.allowLocalhost)
		return nil, &RichCard{
			Kind:        CardKindArticle,
			Label:       "头条文章",
			Title:       title,
			Desc:        desc,
			URL:         safeCardWebURL(pageURL),
			CoverURL:    SafePublicImageURL(cover),
			ObjectID:    strings.TrimSpace(pi.ObjectID),
			Unsupported: true,
		}
	}

	// 4. Vote card (§4.6).
	if objType == "vote" || pageType == "vote" || strings.Contains(lowerURL, "vote.weibo.com") {
		return nil, &RichCard{
			Kind:        CardKindVote,
			Label:       "投票（暂不支持）",
			Title:       title,
			Desc:        desc,
			URL:         safeCardWebURL(pageURL),
			ObjectID:    strings.TrimSpace(pi.ObjectID),
			Unsupported: true,
		}
	}

	// 5. Music / audio card (§4.6).
	if objType == "audio" || objType == "music" || pageType == "music" || pageType == "audio" {
		cover := extractPagePicURL(pi.PagePic, nil, c.allowLocalhost)
		return nil, &RichCard{
			Kind:        CardKindMusic,
			Label:       "音乐",
			Title:       title,
			Desc:        desc,
			URL:         safeCardWebURL(pageURL),
			CoverURL:    SafePublicImageURL(cover),
			ObjectID:    strings.TrimSpace(pi.ObjectID),
			Unsupported: true,
		}
	}

	// 6. Downloadable VOD video (object_type == "video" or type == "11"/"video" with media_info).
	if pi.MediaInfo != nil && (objType == "video" || pageType == "11" || pageType == "video" || len(pi.MediaInfo.PlaybackList) > 0) {
		streams, legacy := c.selectVideoStreams(pi.MediaInfo, opts)
		if len(streams) > 0 {
			sel := streams[0]
			cover := extractPagePicURL(pi.PagePic, pi.MediaInfo, c.allowLocalhost)
			vidTitle := title
			if vidTitle == "" {
				vidTitle = strings.TrimSpace(pi.MediaInfo.VideoTitle)
			}
			if vidTitle == "" {
				vidTitle = strings.TrimSpace(pi.MediaInfo.Name)
			}
			durSec := sel.DurationSec
			if durSec == 0 {
				durSec = int(parseJSONFloat64(pi.MediaInfo.Duration) + 0.5)
			}
			return &VideoAsset{
				ObjectID:       strings.TrimSpace(pi.ObjectID),
				Title:          vidTitle,
				DurationSec:    durSec,
				PublicCoverURL: SafePublicImageURL(cover),
				CoverURL:       cover,
				Selected:       &sel,
				Streams:        streams,
				LegacyFallback: legacy,
			}, nil
		}
	}

	// 7. Generic webpage / topic / place / search / story / collection card (§4.6).
	// NEVER reuse topic/search/place page_pic as post cover!
	if objType != "" || title != "" || pageURL != "" {
		cover := ""
		switch objType {
		case "topic", "search", "place":
			cover = ""
		default:
			cover = SafePublicImageURL(extractPagePicURL(pi.PagePic, nil, c.allowLocalhost))
		}
		label := "网页链接"
		switch objType {
		case "topic":
			label = "超话/话题"
		case "place":
			label = "地点"
		case "collection":
			label = "视频合集"
		}
		return nil, &RichCard{
			Kind:        CardKindWebpage,
			Label:       label,
			Title:       title,
			Desc:        desc,
			URL:         safeCardWebURL(pageURL),
			CoverURL:    cover,
			ObjectID:    strings.TrimSpace(pi.ObjectID),
			Unsupported: true,
		}
	}

	return nil, nil
}

func (c *Client) selectVideoStreams(mi *rawMediaInfo, opts StreamSelectOptions) ([]VideoStream, bool) {
	if mi == nil {
		return nil, false
	}
	prefCodec := normalizePreferredCodec(opts.PreferredCodec)
	mediaDur := int(parseJSONFloat64(mi.Duration) + 0.5)

	streams := make([]VideoStream, 0, len(mi.PlaybackList))
	seenURL := make(map[string]bool)

	for _, entry := range mi.PlaybackList {
		if entry.PlayInfo == nil {
			continue
		}
		rawURL := normalizeWeiboMediaURL(entry.PlayInfo.URL, c.allowLocalhost)
		if rawURL == "" || seenURL[rawURL] {
			continue
		}
		// Strictly filter out hidden tiers, non-MP4 (like scrubber_hd JPEG sprites!), and non-progressive protocols.
		if entry.Meta != nil && parseJSONBoolOrInt(entry.Meta.IsHidden) {
			continue
		}
		mime := strings.ToLower(strings.TrimSpace(entry.PlayInfo.Mime))
		if mime != "video/mp4" {
			continue
		}
		label := ""
		qualityIdx := 0
		qualityLabel := ""
		qualityDesc := ""
		if entry.Meta != nil {
			label = strings.TrimSpace(entry.Meta.Label)
			qualityIdx = int(parseJSONInt64(entry.Meta.QualityIndex))
			qualityLabel = strings.TrimSpace(entry.Meta.QualityLabel)
			qualityDesc = strings.TrimSpace(entry.Meta.QualityDesc)
		}
		if strings.Contains(strings.ToLower(label), "scrubber") {
			continue
		}
		proto := strings.ToLower(strings.TrimSpace(entry.PlayInfo.Protocol))
		if proto != "" && proto != "general" && proto != "http" && proto != "https" {
			continue
		}

		w := int(parseJSONInt64(entry.PlayInfo.Width))
		h := int(parseJSONInt64(entry.PlayInfo.Height))
		fps := int(parseJSONFloat64(entry.PlayInfo.FPS) + 0.5)
		bitrate := parseJSONInt64(entry.PlayInfo.Bitrate)
		size := parseJSONInt64(entry.PlayInfo.Size)
		dur := int(parseJSONFloat64(entry.PlayInfo.Duration) + 0.5)
		if dur == 0 {
			dur = mediaDur
		}

		codec := detectWeiboVideoCodec(entry.PlayInfo.VideoCodec, label)
		shortEdge := normalizeResolutionTier(w, h, qualityIdx, label, qualityLabel)
		if qualityLabel == "" && shortEdge > 0 {
			qualityLabel = resolutionTierLabel(shortEdge)
		}
		dr := ""
		if entry.PlayInfo.Extension != nil && entry.PlayInfo.Extension.TranscodeInfo != nil {
			dr = strings.TrimSpace(entry.PlayInfo.Extension.TranscodeInfo.OriginVideoDR)
		}

		seenURL[rawURL] = true
		streams = append(streams, VideoStream{
			Label:        label,
			QualityIndex: qualityIdx,
			QualityLabel: qualityLabel,
			QualityDesc:  qualityDesc,
			ShortEdge:    shortEdge,
			Width:        w,
			Height:       h,
			FPS:          fps,
			Bitrate:      bitrate,
			Size:         size,
			DurationSec:  dur,
			Codec:        codec,
			VideoCodec:   strings.TrimSpace(entry.PlayInfo.VideoCodec),
			AudioCodec:   strings.TrimSpace(entry.PlayInfo.AudioCodec),
			DynamicRange: dr,
			URL:          rawURL,
		})
	}

	if len(streams) > 0 {
		sortVideoStreams(streams, prefCodec, opts.MaxResolution)
		return streams, false
	}

	// Legacy media_info fallback ONLY when playback_list yielded 0 valid MP4 streams (weibo.md §4.5 #2).
	legacyFields := []struct {
		label string
		url   string
		codec string
		tier  int
	}{
		{label: "mp4_720p_mp4", url: mi.MP4720P, codec: "avc", tier: 720},
		{label: "mp4_hd_url", url: mi.MP4HDURL, codec: "avc", tier: 720},
		{label: "stream_url_hd", url: mi.StreamURLHD, codec: "avc", tier: 720},
		{label: "hevc_mp4_720p", url: mi.HEVCMP4720P, codec: "hevc", tier: 720},
		{label: "h265_mp4_hd", url: mi.H265MP4HD, codec: "hevc", tier: 720},
		{label: "mp4_sd_url", url: mi.MP4SDURL, codec: "avc", tier: 480},
		{label: "stream_url", url: mi.StreamURL, codec: "avc", tier: 480},
		{label: "replay_hd", url: mi.ReplayHD, codec: "avc", tier: 720},
	}
	legacyStreams := make([]VideoStream, 0, len(legacyFields))
	for _, lf := range legacyFields {
		u := normalizeWeiboMediaURL(lf.url, c.allowLocalhost)
		if u == "" || seenURL[u] {
			continue
		}
		seenURL[u] = true
		w, h, qLabel, detectedCodec := inferLegacyVideoMetadata(u, lf.label, lf.codec)
		shortEdge := normalizeResolutionTier(w, h, lf.tier, qLabel, "")
		legacyStreams = append(legacyStreams, VideoStream{
			Label:        lf.label,
			QualityIndex: shortEdge,
			QualityLabel: resolutionTierLabel(shortEdge),
			ShortEdge:    shortEdge,
			Width:        w,
			Height:       h,
			DurationSec:  mediaDur,
			Codec:        detectedCodec,
			URL:          u,
		})
	}
	if len(legacyStreams) > 0 {
		sortVideoStreams(legacyStreams, prefCodec, opts.MaxResolution)
		return legacyStreams, true
	}
	return nil, false
}

func parseComponentVideoStreams(urls map[string]string, streamHD, streamURL string, durSec int, opts StreamSelectOptions, allowLocalhost bool) []VideoStream {
	prefCodec := normalizePreferredCodec(opts.PreferredCodec)
	seenURL := make(map[string]bool)
	streams := make([]VideoStream, 0, len(urls)+2)

	// Sort map keys deterministically before building candidate slice.
	labels := make([]string, 0, len(urls))
	for k := range urls {
		labels = append(labels, k)
	}
	sort.Strings(labels)

	for _, label := range labels {
		u := normalizeWeiboMediaURL(urls[label], allowLocalhost)
		if u == "" || seenURL[u] {
			continue
		}
		seenURL[u] = true
		tier := inferResolutionFromLabel(label)
		codec := detectWeiboVideoCodec("", label)
		qLabel := strings.TrimSpace(label)
		if qLabel == "" && tier > 0 {
			qLabel = resolutionTierLabel(tier)
		}
		streams = append(streams, VideoStream{
			Label:        label,
			QualityIndex: tier,
			QualityLabel: qLabel,
			ShortEdge:    tier,
			DurationSec:  durSec,
			Codec:        codec,
			URL:          u,
		})
	}

	if len(streams) == 0 {
		for _, pair := range []struct {
			label string
			raw   string
			tier  int
		}{
			{"stream_url_hd", streamHD, 720},
			{"stream_url", streamURL, 480},
		} {
			u := normalizeWeiboMediaURL(pair.raw, allowLocalhost)
			if u == "" || seenURL[u] {
				continue
			}
			seenURL[u] = true
			streams = append(streams, VideoStream{
				Label:        pair.label,
				QualityIndex: pair.tier,
				QualityLabel: resolutionTierLabel(pair.tier),
				ShortEdge:    pair.tier,
				DurationSec:  durSec,
				Codec:        "avc",
				URL:          u,
			})
		}
	}

	if len(streams) > 0 {
		sortVideoStreams(streams, prefCodec, opts.MaxResolution)
	}
	return streams
}

func sortVideoStreams(streams []VideoStream, prefCodec string, maxRes int) {
	// If maxRes cap is set and at least one stream satisfies it, prioritize streams <= maxRes first.
	hasWithinCap := false
	if maxRes > 0 {
		for _, s := range streams {
			if s.ShortEdge > 0 && s.ShortEdge <= maxRes {
				hasWithinCap = true
				break
			}
		}
	}
	sort.SliceStable(streams, func(i, j int) bool {
		a, b := streams[i], streams[j]
		if hasWithinCap {
			aIn := a.ShortEdge > 0 && a.ShortEdge <= maxRes
			bIn := b.ShortEdge > 0 && b.ShortEdge <= maxRes
			if aIn != bIn {
				return aIn
			}
		}
		// 1. Short-edge resolution tier descending (2160 > 1440 > 1080 > 720 > 480 > 360).
		if a.ShortEdge != b.ShortEdge {
			return a.ShortEdge > b.ShortEdge
		}
		// 2. quality_index descending.
		if a.QualityIndex != b.QualityIndex {
			return a.QualityIndex > b.QualityIndex
		}
		// 3. Codec preference match within same resolution tier.
		aPref := a.Codec == prefCodec
		bPref := b.Codec == prefCodec
		if aPref != bPref {
			return aPref
		}
		// 4. FPS descending.
		if a.FPS != b.FPS {
			return a.FPS > b.FPS
		}
		// 5. Bitrate descending.
		if a.Bitrate != b.Bitrate {
			return a.Bitrate > b.Bitrate
		}
		// 6. Size descending.
		return a.Size > b.Size
	})
}

func normalizePreferredCodec(pref string) string {
	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "hevc", "h265", "h.265", "hev1", "hvc1":
		return "hevc"
	default:
		return "avc"
	}
}

func detectWeiboVideoCodec(videoCodec, label string) string {
	vc := strings.ToLower(strings.TrimSpace(videoCodec))
	lbl := strings.ToLower(strings.TrimSpace(label))
	if strings.HasPrefix(vc, "hev1") || strings.HasPrefix(vc, "hvc1") || strings.Contains(vc, "hevc") || strings.Contains(vc, "h265") ||
		strings.Contains(lbl, "hevc") || strings.Contains(lbl, "h265") || strings.Contains(lbl, "265") {
		return "hevc"
	}
	return "avc"
}

func normalizeResolutionTier(w, h, qualityIdx int, label, qualityLabel string) int {
	if w > 0 && h > 0 {
		shortEdge := w
		if h < shortEdge {
			shortEdge = h
		}
		switch {
		case shortEdge >= 2100:
			return 2160
		case shortEdge >= 1400:
			return 1440
		case shortEdge >= 1000:
			return 1080
		case shortEdge >= 700:
			return 720
		case shortEdge >= 460:
			return 480
		case shortEdge >= 320:
			return 360
		default:
			return shortEdge
		}
	}
	if t := inferResolutionFromLabel(label + " " + qualityLabel); t > 0 {
		return t
	}
	if qualityIdx > 0 {
		return qualityIdx
	}
	return 0
}

func inferResolutionFromLabel(s string) int {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "4k") || strings.Contains(lower, "2160"):
		return 2160
	case strings.Contains(lower, "2k") || strings.Contains(lower, "1440"):
		return 1440
	case strings.Contains(lower, "1080"):
		return 1080
	case strings.Contains(lower, "720"):
		return 720
	case strings.Contains(lower, "480"):
		return 480
	case strings.Contains(lower, "360") || strings.Contains(lower, "_ld") || strings.Contains(lower, "流畅"):
		return 360
	case strings.Contains(lower, "_hd") || strings.Contains(lower, "高清") || strings.Contains(lower, "超清"):
		return 720
	case strings.Contains(lower, "_sd") || strings.Contains(lower, "标清"):
		return 480
	default:
		return 0
	}
}

func resolutionTierLabel(tier int) string {
	switch tier {
	case 2160:
		return "4K"
	case 1440:
		return "2K"
	case 1080:
		return "1080P"
	case 720:
		return "720P"
	case 480:
		return "480P"
	case 360:
		return "360P"
	default:
		if tier > 0 {
			return fmt.Sprintf("%dP", tier)
		}
		return ""
	}
}

var reTemplateDims = regexp.MustCompile(`(\d{2,4})x(\d{2,4})`)

func inferLegacyVideoMetadata(rawURL, fallbackLabel, fallbackCodec string) (w, h int, qLabel, codec string) {
	qLabel = fallbackLabel
	codec = fallbackCodec
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, 0, qLabel, codec
	}
	q := u.Query()
	if lbl := strings.TrimSpace(q.Get("label")); lbl != "" {
		qLabel = lbl
		codec = detectWeiboVideoCodec("", lbl)
	}
	if tpl := strings.TrimSpace(q.Get("template")); tpl != "" {
		if m := reTemplateDims.FindStringSubmatch(tpl); len(m) == 3 {
			w, _ = strconv.Atoi(m[1])
			h, _ = strconv.Atoi(m[2])
		}
	}
	return w, h, qLabel, codec
}

func extractPagePicURL(rawPagePic json.RawMessage, mi *rawMediaInfo, allowLocalhost bool) string {
	t := bytes.TrimSpace(rawPagePic)
	if len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		if t[0] == '"' {
			if s := parseJSONString(t); s != "" {
				return normalizeWeiboMediaURL(s, allowLocalhost)
			}
		} else if t[0] == '{' {
			var obj struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(t, &obj) == nil && obj.URL != "" {
				return normalizeWeiboMediaURL(obj.URL, allowLocalhost)
			}
		}
	}
	if mi != nil {
		if mi.BigPicInfo != nil {
			for _, v := range []*rawPicVariant{mi.BigPicInfo.Largest, mi.BigPicInfo.Original, mi.BigPicInfo.Large} {
				if v != nil && strings.TrimSpace(v.URL) != "" {
					return normalizeWeiboMediaURL(v.URL, allowLocalhost)
				}
			}
		}
		if mi.PicBig != nil && mi.PicBig.URL != "" {
			return normalizeWeiboMediaURL(mi.PicBig.URL, allowLocalhost)
		}
	}
	return ""
}

func safeCardWebURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return ""
	}
	return u.String()
}

// HasSignedQueryMarker reports whether raw contains Weibo ephemeral media signature parameters
// (ssig / Expires / KID) in plain or percent-encoded form.
func HasSignedQueryMarker(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return false
	}
	return containsAny(lower, "ssig=", "expires=", "kid=", "ssig%3d", "expires%3d", "kid%3d")
}

// SafePublicWebURL returns rawURL only when it is an http(s) URL without userinfo and without
// Weibo signed media query markers (safe for persistence in snapshots).
func SafePublicWebURL(rawURL string) string {
	s := safeCardWebURL(rawURL)
	if s == "" || HasSignedQueryMarker(s) {
		return ""
	}
	return s
}

// SafePublicImageURL returns rawURL only when it is an HTTPS sinaimg.cn image URL (or loopback
// in synthetic tests) without userinfo and without ephemeral signature parameters (ssig / Expires / KID).
func SafePublicImageURL(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if HasSignedQueryMarker(u.RawQuery) || HasSignedQueryMarker(s) {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if site.IsLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "http") {
			return s
		}
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	if site.MatchDomainSuffix(host, []string{"sinaimg.cn"}) {
		return s
	}
	return ""
}

func parseURLStruct(items []rawURLStruct, allowLocalhost bool) []URLStructItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]URLStructItem, 0, len(items))
	for _, it := range items {
		shortU := strings.TrimSpace(it.ShortURL)
		longU := strings.TrimSpace(it.LongURL)
		if longU == "" {
			longU = strings.TrimSpace(it.OriURL)
		}
		title := strings.TrimSpace(it.URLTitle)
		var inlinePic string
		if len(it.PicInfos) > 0 {
			keys := make([]string, 0, len(it.PicInfos))
			for k := range it.PicInfos {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				pi := it.PicInfos[k]
				if pi == nil {
					continue
				}
				for _, v := range []*rawPicVariant{pi.WOriginal, pi.Largest, pi.Original, pi.Large} {
					if v != nil && strings.TrimSpace(v.URL) != "" {
						inlinePic = normalizeWeiboMediaURL(v.URL, allowLocalhost)
						break
					}
				}
				if inlinePic != "" {
					break
				}
			}
		}
		if shortU == "" && longU == "" && title == "" && inlinePic == "" {
			continue
		}
		out = append(out, URLStructItem{
			ShortURL:     shortU,
			LongURL:      longU,
			URLTitle:     title,
			InlinePicURL: inlinePic,
		})
	}
	return out
}

func mergeURLMappings(base, extra []URLStructItem) []URLStructItem {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base))
	out := make([]URLStructItem, 0, len(base)+len(extra))
	for _, item := range base {
		out = append(out, item)
		if item.ShortURL != "" {
			seen[item.ShortURL] = true
		}
	}
	for _, item := range extra {
		if item.ShortURL != "" && seen[item.ShortURL] {
			continue
		}
		out = append(out, item)
		if item.ShortURL != "" {
			seen[item.ShortURL] = true
		}
	}
	return out
}

// restoreShortURLsInText replaces t.cn links in text locally using url_struct without any network requests.
func restoreShortURLsInText(text string, mappings []URLStructItem) string {
	if text == "" || len(mappings) == 0 {
		return text
	}
	out := text
	for _, m := range mappings {
		if m.ShortURL == "" || !strings.Contains(out, m.ShortURL) {
			continue
		}
		replacement := ""
		if m.LongURL != "" && !strings.Contains(strings.ToLower(m.LongURL), "://t.cn/") && !HasSignedQueryMarker(m.LongURL) {
			if m.URLTitle != "" && m.URLTitle != "网页链接" && !strings.Contains(m.LongURL, m.URLTitle) {
				replacement = m.URLTitle + " (" + m.LongURL + ")"
			} else {
				replacement = m.LongURL
			}
		} else if m.URLTitle != "" {
			replacement = m.URLTitle
		}
		if replacement != "" {
			out = strings.ReplaceAll(out, m.ShortURL, replacement)
		}
	}
	return out
}

var (
	reHTMLBreak = regexp.MustCompile(`(?i)<br\s*/?>`)
	reHTMLTag   = regexp.MustCompile(`<[^>]+>`)
)

func stripHTMLText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = reHTMLBreak.ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

func parseWeiboCreatedAt(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	for _, layout := range []string{
		time.RubyDate, // "Mon Jan 02 15:04:05 -0700 2006"
		"Mon Jan _2 15:04:05 -0700 2006",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func (c *Client) preferredCodec() string {
	c.cbMu.RLock()
	fn := c.prefCodecFn
	c.cbMu.RUnlock()
	if fn != nil {
		if v := strings.TrimSpace(fn()); v != "" {
			return normalizePreferredCodec(v)
		}
	}
	return "avc"
}
