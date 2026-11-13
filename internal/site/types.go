package site

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultUserAgent is the desktop Chrome User-Agent used when a site spec does not override it.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// ResolveTarget describes a normalized target parsed or resolved from user input.
type ResolveTarget struct {
	Site          ID     `json:"site"`
	Kind          string `json:"kind"` // "video", "note", "dynamic", "user", "live", "bangumi", "article", "collection"
	ID            string `json:"id"`
	SecUID        string `json:"secUid,omitempty"`
	WebRID        string `json:"webRid,omitempty"`
	RoomID        string `json:"roomId,omitempty"`
	NumericAID    int64  `json:"aid,omitempty"`
	Page          int    `json:"page,omitempty"`
	NumericRoomID int64  `json:"numericRoomId,omitempty"`
	NumericMid    int64  `json:"mid,omitempty"`
	CanonicalURL  string `json:"canonicalUrl"`
	Unsupported   bool   `json:"unsupported,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// MediaTransportSpec defines outbound CDN host allowlisting and headers for media/live streams.
// ApplyHeaders always strips Cookie so user session secrets never reach CDN edges.
type MediaTransportSpec struct {
	Site           ID       `json:"site"`
	AllowedDomains []string `json:"allowedDomains"`
	UserAgent      string   `json:"userAgent,omitempty"`
	Referer        string   `json:"referer,omitempty"`
	Origin         string   `json:"origin,omitempty"`
	AllowLocalhost bool     `json:"-"`
	// PublicRedirects lets a redirect hop leave AllowedDomains for another public
	// HTTPS host on port 443 (never an IP literal or a local name). The first URL must
	// still match AllowedDomains; live CDNs schedule edges on vendor domains that no
	// fixed suffix list covers. Resolved addresses are still checked by the dialer.
	PublicRedirects bool `json:"-"`
}

// DefaultBilibiliCDNHosts returns the official Bilibili media/cover CDN domain suffixes.
func DefaultBilibiliCDNHosts() []string {
	return []string{
		"bilivideo.com",
		"bilivideo.cn",
		"hdslb.com",
		"bilibili.com",
		"akamaized.net",
	}
}

// DefaultDouyinMediaHosts returns the official Douyin VOD, image, and live CDN domain suffixes.
func DefaultDouyinMediaHosts() []string {
	return []string{
		"douyinvod.com",
		"zjcdn.com",
		"douyinpic.com",
		"byteimg.com",
		"douyinstatic.com",
		"bdxiguaimg.com",
		"snssdk.com",
		"bytevcloudcdn.com",
		"ibytedtos.com",
		"pstatp.com",
		"amemv.com",
		"douyincdn.com",
		"bytefcdnrd.com",
	}
}

// DefaultDouyinLiveHosts returns the official Douyin live stream CDN domain suffixes.
func DefaultDouyinLiveHosts() []string {
	return []string{
		"douyincdn.com",
		"bytefcdnrd.com",
		"amemv.com",
		"douyinvod.com",
		"zjcdn.com",
	}
}

// BilibiliMediaTransport returns the standard Bilibili media CDN transport policy.
func BilibiliMediaTransport(allowLocalhost bool) MediaTransportSpec {
	return MediaTransportSpec{
		Site:           Bilibili,
		AllowedDomains: DefaultBilibiliCDNHosts(),
		UserAgent:      DefaultUserAgent,
		Referer:        "https://www.bilibili.com/",
		Origin:         "https://www.bilibili.com",
		AllowLocalhost: allowLocalhost,
	}
}

// DouyinVODTransport returns the standard Douyin VOD and image-note CDN transport policy.
func DouyinVODTransport(userAgent string, allowLocalhost bool) MediaTransportSpec {
	if strings.TrimSpace(userAgent) == "" {
		userAgent = DefaultUserAgent
	}
	return MediaTransportSpec{
		Site:           Douyin,
		AllowedDomains: DefaultDouyinMediaHosts(),
		UserAgent:      userAgent,
		Referer:        "https://www.douyin.com/",
		AllowLocalhost: allowLocalhost,
	}
}

// DouyinLiveTransport returns the standard Douyin live stream CDN transport policy.
func DouyinLiveTransport(userAgent string, allowLocalhost bool) MediaTransportSpec {
	if strings.TrimSpace(userAgent) == "" {
		userAgent = DefaultUserAgent
	}
	return MediaTransportSpec{
		Site:           Douyin,
		AllowedDomains: DefaultDouyinLiveHosts(),
		UserAgent:      userAgent,
		Referer:        "https://live.douyin.com/",
		Origin:         "https://live.douyin.com",
		AllowLocalhost: allowLocalhost,
		// Douyin live FLV pull hosts answer 302 to a scheduled edge on another
		// domain (observed in ACT-012); see ValidateRedirectURL.
		PublicRedirects: true,
	}
}

// ValidateURL verifies that rawURL uses HTTPS on port 443 and matches AllowedDomains
// (or loopback when AllowLocalhost is enabled for synthetic tests).
func (m MediaTransportSpec) ValidateURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || u.User != nil {
		return &Error{
			Site:    m.Site,
			Kind:    ErrKindBadRequest,
			Message: "媒体地址格式无效",
		}
	}
	host := strings.ToLower(u.Hostname())
	if m.AllowLocalhost && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return nil
		}
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return &Error{
			Site:    m.Site,
			Kind:    ErrKindBlocked,
			Message: "已拦截非 HTTPS 媒体地址",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return &Error{
			Site:    m.Site,
			Kind:    ErrKindBlocked,
			Message: "已拦截非常规端口媒体地址",
		}
	}
	if !MatchDomainSuffix(host, m.AllowedDomains) {
		return &Error{
			Site:    m.Site,
			Kind:    ErrKindBlocked,
			Message: "媒体域名不在允许列表中: " + host,
		}
	}
	return nil
}

// ValidateRedirectURL checks one redirect hop. Without PublicRedirects it is the
// same as ValidateURL. With PublicRedirects a hop may also target any public DNS
// name over HTTPS on port 443; IP literals, single-label and local names stay blocked.
func (m MediaTransportSpec) ValidateRedirectURL(rawURL string) error {
	err := m.ValidateURL(rawURL)
	if err == nil || !m.PublicRedirects || KindOf(err) != ErrKindBlocked {
		return err
	}
	u, perr := url.Parse(strings.TrimSpace(rawURL))
	if perr != nil || !strings.EqualFold(u.Scheme, "https") {
		return err
	}
	if port := u.Port(); port != "" && port != "443" {
		return err
	}
	if !IsPublicDNSName(u.Hostname()) {
		return &Error{
			Site:    m.Site,
			Kind:    ErrKindBlocked,
			Message: "已拦截跳转到 IP 或本地主机名的媒体地址",
		}
	}
	return nil
}

// IsPublicDNSName reports whether host is a syntactically valid multi-label DNS
// name that is not an IP literal and not a local-only name.
func IsPublicDNSName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	if tld[0] >= '0' && tld[0] <= '9' {
		return false
	}
	switch tld {
	case "localhost", "local", "internal", "lan", "home", "localdomain", "intranet", "corp", "invalid", "test", "example":
		return false
	}
	if strings.HasSuffix(host, ".home.arpa") || tld == "arpa" {
		return false
	}
	return true
}

// ApplyHeaders sets configured UA/Referer/Origin headers and explicitly strips Cookie.
func (m MediaTransportSpec) ApplyHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Del("Cookie")
	if m.UserAgent != "" {
		req.Header.Set("User-Agent", m.UserAgent)
	}
	if m.Referer != "" {
		req.Header.Set("Referer", m.Referer)
	}
	if m.Origin != "" {
		req.Header.Set("Origin", m.Origin)
	}
}

// MatchDomainSuffix reports whether host equals or is a subdomain of any domain in allowed.
func MatchDomainSuffix(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, dom := range allowed {
		d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(dom)), ".")
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// LiveTransport identifies the live stream transport protocol used by the recorder.
type LiveTransport string

const (
	LiveTransportHLSFMP4 LiveTransport = "hls_fmp4"
	LiveTransportHTTPFLV LiveTransport = "http_flv"
)

// Normalized live broadcast states shared across sites.
const (
	LiveStateOffline   = "offline"
	LiveStateLive      = "live"
	LiveStateRound     = "round"
	LiveStateAudioOnly = "audio_only"
	LiveStateNone      = "none"
)

// LiveStatus represents normalized live broadcast state for a creator or room.
type LiveStatus struct {
	Site              ID     `json:"site"`
	AccountUID        string `json:"accountUid"`
	WebRID            string `json:"webRid,omitempty"`
	RoomID            string `json:"roomId,omitempty"`
	Status            string `json:"status"` // LiveStateOffline, LiveStateLive, LiveStateRound, LiveStateAudioOnly, LiveStateNone
	Title             string `json:"title,omitempty"`
	Uname             string `json:"uname,omitempty"`
	Face              string `json:"face,omitempty"`
	Cover             string `json:"cover,omitempty"`
	AreaName          string `json:"areaName,omitempty"`
	LiveSince         int64  `json:"liveSince,omitempty"`
	Quality           int    `json:"quality,omitempty"`
	QualityName       string `json:"qualityName,omitempty"`
	Codec             string `json:"codec,omitempty"`
	InvalidatedWebRID bool   `json:"invalidatedWebRid,omitempty"`
}

// LiveStreamPlan holds the selected live stream transport, codec, quality, and candidate URLs.
type LiveStreamPlan struct {
	Site        ID                 `json:"site"`
	AccountUID  string             `json:"accountUid,omitempty"`
	WebRID      string             `json:"webRid,omitempty"`
	RoomID      string             `json:"roomId"`
	Status      string             `json:"status"`
	Transport   LiveTransport      `json:"transport"`
	Protocol    string             `json:"protocol,omitempty"`
	Format      string             `json:"format,omitempty"`
	Codec       string             `json:"codec,omitempty"`
	Quality     int                `json:"quality,omitempty"`
	QualityName string             `json:"qualityName,omitempty"`
	URLs        []string           `json:"-"`
	Media       MediaTransportSpec `json:"-"`
}

// FormatLiveQualitySpec formats quality label and codec into "原画 · AVC" style display text.
func FormatLiveQualitySpec(qualityName string, quality int, codec string) string {
	qName := strings.TrimSpace(qualityName)
	if qName == "" && quality > 0 {
		qName = strconv.Itoa(quality)
	}
	cName := strings.ToUpper(strings.TrimSpace(codec))
	if qName != "" && cName != "" {
		return qName + " · " + cName
	}
	if qName != "" {
		return qName
	}
	return cName
}

// MediaImageAsset describes a single image page (and optional Live Photo video clip) in an image-note post.
type MediaImageAsset struct {
	Index     int      `json:"index"` // 1-based page index
	Width     int      `json:"width,omitempty"`
	Height    int      `json:"height,omitempty"`
	ImageURLs []string `json:"-"`
	VideoURLs []string `json:"-"`
}

// DownloadPlan describes resolved media streams and metadata for caching an item locally.
type DownloadPlan struct {
	Site         ID       `json:"site"`
	ItemKey      string   `json:"itemKey"`
	TargetID     string   `json:"targetId"`
	Kind         string   `json:"kind"` // "dash", "single_mp4", "image_note"
	AuthorUID    string   `json:"authorUid"`
	AuthorName   string   `json:"authorName,omitempty"`
	Title        string   `json:"title,omitempty"`
	Desc         string   `json:"desc,omitempty"`
	PubTs        int64    `json:"pubTs,omitempty"`
	DurationSec  int      `json:"durationSec,omitempty"`
	VideoCodec   string   `json:"videoCodec,omitempty"`
	QualityLabel string   `json:"qualityLabel,omitempty"`
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	ExpectedSize int64    `json:"expectedSize,omitempty"`
	VideoURLs    []string `json:"-"`
	// OriginalURLs, when set, point at the untranscoded source (原画) and are tried
	// before VideoURLs; VideoURLs and the quality fields describe the fallback.
	OriginalURLs []string           `json:"-"`
	AudioURLs    []string           `json:"-"`
	CoverURLs    []string           `json:"-"`
	MusicURLs    []string           `json:"-"`
	Images       []MediaImageAsset  `json:"-"`
	Media        MediaTransportSpec `json:"-"`
}

// CreatorPost represents a single normalized creator feed entry returned during subscription checks.
type CreatorPost struct {
	ID         string `json:"id"`
	PubTs      int64  `json:"pubTs"`
	Type       string `json:"type"` // "video", "draw", "word", "forward", "article", "other"
	IsTop      bool   `json:"isTop,omitempty"`
	Title      string `json:"title,omitempty"`
	CoverURL   string `json:"coverUrl,omitempty"`
	Snapshot   string `json:"snapshot,omitempty"`
	LiveWebRID string `json:"liveWebRid,omitempty"`
	LiveRoomID string `json:"liveRoomId,omitempty"`
}

// CreatorPostsPage holds one page of creator posts for subscription update checking.
type CreatorPostsPage struct {
	Posts           []CreatorPost `json:"posts"`
	NextCursor      string        `json:"nextCursor,omitempty"`
	HasMore         bool          `json:"hasMore"`
	UncertainEmpty  bool          `json:"uncertainEmpty,omitempty"`
	UncertainReason string        `json:"uncertainReason,omitempty"`
	AuthorName      string        `json:"authorName,omitempty"`
	AuthorFace      string        `json:"authorFace,omitempty"`
	UniqueID        string        `json:"uniqueId,omitempty"`
	LiveWebRID      string        `json:"liveWebRid,omitempty"`
	LiveRoomID      string        `json:"liveRoomId,omitempty"`
}

// Resolver resolves pasted URLs, share text, or site-specific tokens into a normalized ResolveTarget.
type Resolver interface {
	SiteID() ID
	Resolve(ctx context.Context, input string) (ResolveTarget, error)
}

// URLMatcher can be optionally implemented by a Resolver to claim custom test or site URLs.
type URLMatcher interface {
	MatchURL(u *url.URL) bool
}

// DownloadPlanner plans downloadable streams for a site target.
type DownloadPlanner interface {
	SiteID() ID
	PlanDownload(ctx context.Context, targetID string, page int) (DownloadPlan, error)
}

// DownloadExecutor executes a queued download item for its site.
type DownloadExecutor interface {
	SiteID() ID
	ExecuteDownload(ctx context.Context, itemKey string) error
}

// LiveProvider queries creator live broadcast status and selects live recording streams.
type LiveProvider interface {
	SiteID() ID
	CheckLiveStatus(ctx context.Context, uid string, cachedWebRID string) (LiveStatus, error)
	SelectLiveStream(ctx context.Context, uid string, roomOrWebRID string, codecOrder []string) (LiveStreamPlan, error)
}

// SubscriptionFetcher fetches paginated creator posts for incremental subscription checks.
type SubscriptionFetcher interface {
	SiteID() ID
	FetchCreatorPosts(ctx context.Context, uid string, cursor string) (CreatorPostsPage, error)
}
