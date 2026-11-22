package weibo

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
)

// Compile-time assertions that *Adapter implements all applicable cross-site contracts.
// Note: Weibo intentionally does NOT implement site.LiveProvider (live recording excluded in M6).
var (
	_ site.Resolver            = (*Adapter)(nil)
	_ site.URLMatcher          = (*Adapter)(nil)
	_ site.DownloadPlanner     = (*Adapter)(nil)
	_ site.SubscriptionFetcher = (*Adapter)(nil)
)

// Adapter wraps *Client to implement internal/site's cross-site Resolver, URLMatcher,
// DownloadPlanner, and SubscriptionFetcher interfaces.
type Adapter struct {
	client *Client
}

// NewAdapter constructs a cross-site Adapter backed by client.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{client: client}
}

// Client returns the underlying Weibo protocol Client.
func (a *Adapter) Client() *Client {
	return a.client
}

// SiteID returns site.Weibo ("weibo").
func (a *Adapter) SiteID() site.ID {
	return site.Weibo
}

// RequireAuth forwards to the underlying client so download.SiteAuthChecker works seamlessly.
func (a *Adapter) RequireAuth(action string) error {
	if a == nil || a.client == nil {
		return &site.Error{Site: site.Weibo, Kind: site.ErrKindAuthRequired, Message: "请先登录微博"}
	}
	return a.client.RequireAuth(action)
}

// ReportAutomaticError forwards to the underlying client so download.SiteErrorReporter works seamlessly.
func (a *Adapter) ReportAutomaticError(ctx context.Context, err error) {
	if a != nil && a.client != nil {
		a.client.ReportAutomaticError(ctx, err)
	}
}

// MatchURL claims Weibo URLs (and loopback fixture URLs in httptest when AllowLocalhost is enabled).
func (a *Adapter) MatchURL(u *url.URL) bool {
	if a == nil || a.client == nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if id, ok := site.DetectHostSite(host); ok {
		return id == site.Weibo
	}
	if !a.client.allowLocalhost || !site.IsLoopbackHost(host) {
		return false
	}
	segs := site.SplitCleanPath(u.Path)
	if len(segs) == 0 {
		return false
	}
	switch strings.ToLower(segs[0]) {
	case "tcn", "sinaurl", "detail", "status", "statuses", "tv", "u", "n", "ajax", "ttarticle", "l":
		return true
	case "p":
		return len(segs) >= 2 && (reContainerUser.MatchString(segs[1]) || strings.HasPrefix(strings.ToLower(segs[1]), "100808"))
	case "s":
		return len(segs) >= 3 && strings.EqualFold(segs[1], "video")
	default:
		if _, err := site.NormalizeUID(site.Weibo, segs[0]); err == nil {
			return true
		}
		return false
	}
}

// Resolve implements site.Resolver.
func (a *Adapter) Resolve(ctx context.Context, input string) (site.ResolveTarget, error) {
	return a.client.ResolveLink(ctx, input)
}

// PostItemKey returns the canonical cache entry key for a Weibo post ("weibo:dynamic:<mid>") per §13.1 #3.
func PostItemKey(mid string) string {
	return "weibo:dynamic:" + strings.TrimSpace(mid)
}

// VideoItemKey returns the canonical cache entry key for a standalone 1034: video without a host post
// ("weibo:video:<oid with ':' replaced by '_'>") per §13.1 #3.
func VideoItemKey(oid string) string {
	return "weibo:video:" + strings.ReplaceAll(strings.TrimSpace(oid), ":", "_")
}

// PlanDownload implements site.DownloadPlanner for both Weibo posts (by mid/mblogid -> weibo:dynamic:<mid>)
// and standalone 1034: video pages (normalizing to host post mid when present, else weibo:video:<oid>).
func (a *Adapter) PlanDownload(ctx context.Context, targetID string, _ int) (site.DownloadPlan, error) {
	targetID = strings.TrimSpace(targetID)
	pref := a.client.preferredCodec()
	opt := PostOptions{
		StreamOptions: StreamSelectOptions{PreferredCodec: pref},
	}

	if strings.HasPrefix(targetID, "1034:") {
		vr, err := a.client.GetVideo(ctx, targetID, opt)
		if err != nil {
			return site.DownloadPlan{}, err
		}
		if vr.HostPost != nil {
			return a.PlanDownloadFromPost(vr.HostPost)
		}
		return a.planDownloadFromStandaloneVideo(vr)
	}

	post, err := a.client.GetPost(ctx, targetID, opt)
	if err != nil {
		return site.DownloadPlan{}, err
	}
	return a.PlanDownloadFromPost(post)
}

// PlanDownloadFromPost builds an in-memory site.DownloadPlan directly from an already fetched Post
// (allowing full-cache queues to reuse mymblog list payloads without extra statuses/show calls per §6.6).
func (a *Adapter) PlanDownloadFromPost(post *Post) (site.DownloadPlan, error) {
	if post == nil || post.Mid == "" {
		return site.DownloadPlan{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindUnavailable,
			Message: "微博内容为空，无法生成缓存计划",
		}
	}

	images, video, videoFromOrigin := post.EffectiveMediaWithOrigin()
	title := buildPostSummaryTitle(post, 60)
	desc := post.Text
	if desc == "" {
		desc = post.TextRaw
	}

	var (
		originID, originMblogID, originCanonicalURL string
		originUID, originName, originFace           string
		originPubTs                                 int64
		originText, originVideoTitle                string
		originUnavailable                           bool
		originUnavailableReason                     string
		originCard                                  *site.RichCardSummary
	)
	if post.Retweeted != nil {
		rt := post.Retweeted
		originID = rt.Mid
		originMblogID = rt.MblogID
		originCanonicalURL = SafePublicWebURL(rt.CanonicalURL)
		originUID = rt.Author.UID
		originName = rt.Author.ScreenName
		originFace = SafePublicImageURL(rt.Author.AvatarURL)
		originPubTs = rt.CreatedAt
		originText = rt.Text
		if originText == "" {
			originText = rt.TextRaw
		}
		originText = scrubSignedURLTokensInText(originText)
		if rt.Video != nil {
			originVideoTitle = strings.TrimSpace(rt.Video.Title)
		}
		originUnavailable = rt.Unavailable
		originUnavailableReason = strings.TrimSpace(rt.UnavailableReason)
		originCard = toRichCardSummary(rt.Card)
	} else if post.OriMid != "" {
		originID = post.OriMid
	}
	if originVideoTitle == "" && videoFromOrigin && video != nil {
		originVideoTitle = strings.TrimSpace(video.Title)
	}
	outerCard := toRichCardSummary(post.Card)

	// Single standalone video post -> Kind "single_mp4" (still uses itemKey "weibo:dynamic:<mid>" per §13.1 #3!).
	if len(images) == 0 && video != nil && video.Selected != nil {
		sel := video.Selected
		videoURLs := buildStreamCandidateURLs(video.Streams, sel.URL)
		var coverURLs []string
		if video.CoverURL != "" {
			coverURLs = []string{video.CoverURL}
		}
		return site.DownloadPlan{
			Site:                    site.Weibo,
			ItemKey:                 PostItemKey(post.Mid),
			TargetID:                post.Mid,
			Kind:                    "single_mp4",
			CanonicalURL:            post.CanonicalURL,
			AuthorUID:               post.Author.UID,
			AuthorName:              post.Author.ScreenName,
			AuthorFace:              post.Author.AvatarURL,
			Title:                   title,
			Desc:                    desc,
			PubTs:                   post.CreatedAt,
			DurationSec:             video.DurationSec,
			VideoCodec:              sel.Codec,
			QualityLabel:            sel.QualityLabel,
			Width:                   sel.Width,
			Height:                  sel.Height,
			FPS:                     sel.FPS,
			Bitrate:                 sel.Bitrate,
			ExpectedSize:            sel.Size,
			IsForward:               post.IsRetweet,
			IsFastRetweet:           post.IsFastRetweet,
			OriginID:                originID,
			OriginMblogID:           originMblogID,
			OriginCanonicalURL:      originCanonicalURL,
			OriginAuthorUID:         originUID,
			OriginAuthorName:        originName,
			OriginAuthorFace:        originFace,
			OriginPubTs:             originPubTs,
			OriginText:              originText,
			OriginVideoTitle:        originVideoTitle,
			OriginUnavailable:       originUnavailable,
			OriginUnavailableReason: originUnavailableReason,
			OriginCard:              originCard,
			Card:                    outerCard,
			VideoFromOrigin:         videoFromOrigin,
			VisibleType:             post.Visible.Type,
			VisibleLabel:            post.Visible.Label,
			VideoURLs:               videoURLs,
			CoverURLs:               coverURLs,
			Media:                   a.client.MediaTransport(),
		}, nil
	}

	// Images, >9 images, GIF, LivePhoto, mixed media, or pure text/card post -> Kind "image_note".
	mediaSpec := a.client.MediaTransport()
	hasLivePhotoHop := false
	assets := make([]site.MediaImageAsset, 0, len(images))
	var coverURLs []string

	for i, img := range images {
		idx := img.Index
		if idx <= 0 {
			idx = i + 1
		}
		imgURLs := make([]string, 0, 1+len(img.FallbackURLs))
		if img.URL != "" {
			imgURLs = append(imgURLs, img.URL)
		}
		imgURLs = append(imgURLs, img.FallbackURLs...)
		if len(coverURLs) == 0 && len(imgURLs) > 0 {
			coverURLs = []string{imgURLs[0]}
		}

		var vidURLs []string
		if img.MotionURL != "" {
			vidURLs = append(vidURLs, img.MotionURL)
		}
		vidURLs = append(vidURLs, img.MotionFallbackURLs...)
		if img.NeedsLivePhotoHop {
			hasLivePhotoHop = true
		}

		asset := site.MediaImageAsset{
			Index:       idx,
			MediaRole:   img.Kind,
			FromOrigin:  img.FromOrigin,
			Width:       img.Width,
			Height:      img.Height,
			DurationSec: img.DurationSec,
			ImageURLs:   imgURLs,
			VideoURLs:   vidURLs,
		}
		if img.VideoStream != nil {
			asset.VideoCodec = img.VideoStream.Codec
			asset.QualityLabel = img.VideoStream.QualityLabel
			asset.FPS = img.VideoStream.FPS
			asset.Bitrate = img.VideoStream.Bitrate
		}
		assets = append(assets, asset)
	}

	if hasLivePhotoHop {
		mediaSpec = a.client.LivePhotoMediaTransport()
	}
	if len(coverURLs) == 0 && post.Card != nil && post.Card.CoverURL != "" {
		coverURLs = []string{post.Card.CoverURL}
	}
	if len(coverURLs) == 0 && post.Retweeted != nil && !post.Retweeted.Unavailable && post.Retweeted.Card != nil && post.Retweeted.Card.CoverURL != "" {
		coverURLs = []string{post.Retweeted.Card.CoverURL}
	}

	return site.DownloadPlan{
		Site:                    site.Weibo,
		ItemKey:                 PostItemKey(post.Mid),
		TargetID:                post.Mid,
		Kind:                    "image_note",
		CanonicalURL:            post.CanonicalURL,
		AuthorUID:               post.Author.UID,
		AuthorName:              post.Author.ScreenName,
		AuthorFace:              post.Author.AvatarURL,
		Title:                   title,
		Desc:                    desc,
		PubTs:                   post.CreatedAt,
		IsForward:               post.IsRetweet,
		IsFastRetweet:           post.IsFastRetweet,
		OriginID:                originID,
		OriginMblogID:           originMblogID,
		OriginCanonicalURL:      originCanonicalURL,
		OriginAuthorUID:         originUID,
		OriginAuthorName:        originName,
		OriginAuthorFace:        originFace,
		OriginPubTs:             originPubTs,
		OriginText:              originText,
		OriginVideoTitle:        originVideoTitle,
		OriginUnavailable:       originUnavailable,
		OriginUnavailableReason: originUnavailableReason,
		OriginCard:              originCard,
		Card:                    outerCard,
		VideoFromOrigin:         videoFromOrigin,
		VisibleType:             post.Visible.Type,
		VisibleLabel:            post.Visible.Label,
		CoverURLs:               coverURLs,
		Images:                  assets,
		Media:                   mediaSpec,
	}, nil
}

func toRichCardSummary(c *RichCard) *site.RichCardSummary {
	if c == nil {
		return nil
	}
	return &site.RichCardSummary{
		Kind:  strings.TrimSpace(c.Kind),
		Label: strings.TrimSpace(c.Label),
		Title: scrubSignedURLTokensInText(strings.TrimSpace(c.Title)),
		Desc:  scrubSignedURLTokensInText(strings.TrimSpace(c.Desc)),
		URL:   SafePublicWebURL(c.URL),
	}
}

func (a *Adapter) planDownloadFromStandaloneVideo(vr *VideoPageResult) (site.DownloadPlan, error) {
	if vr == nil || vr.Video == nil || vr.Video.Selected == nil {
		return site.DownloadPlan{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindUnavailable,
			Message: "该微博视频未提供可缓存的视频流",
		}
	}
	sel := vr.Video.Selected
	videoURLs := buildStreamCandidateURLs(vr.Video.Streams, sel.URL)
	var coverURLs []string
	if vr.Video.CoverURL != "" {
		coverURLs = []string{vr.Video.CoverURL}
	}
	return site.DownloadPlan{
		Site:         site.Weibo,
		ItemKey:      VideoItemKey(vr.ObjectID),
		TargetID:     vr.ObjectID,
		Kind:         "single_mp4",
		CanonicalURL: vr.CanonicalURL,
		AuthorUID:    vr.AuthorUID,
		AuthorName:   vr.AuthorName,
		Title:        vr.Title,
		DurationSec:  vr.Video.DurationSec,
		VideoCodec:   sel.Codec,
		QualityLabel: sel.QualityLabel,
		Width:        sel.Width,
		Height:       sel.Height,
		FPS:          sel.FPS,
		Bitrate:      sel.Bitrate,
		ExpectedSize: sel.Size,
		VideoURLs:    videoURLs,
		CoverURLs:    coverURLs,
		Media:        a.client.MediaTransport(),
	}, nil
}

func buildStreamCandidateURLs(streams []VideoStream, primary string) []string {
	seen := make(map[string]bool, len(streams)+1)
	out := make([]string, 0, len(streams)+1)
	if primary != "" {
		seen[primary] = true
		out = append(out, primary)
	}
	for _, s := range streams {
		if s.URL != "" && !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s.URL)
		}
	}
	return out
}

// FormatCursor encodes a (page, sinceID) pair into an opaque pagination cursor for SubscriptionFetcher.
func FormatCursor(page int, sinceID string) string {
	if page <= 1 && strings.TrimSpace(sinceID) == "" {
		return ""
	}
	if page <= 0 {
		page = 1
	}
	return strconv.Itoa(page) + ":" + strings.TrimSpace(sinceID)
}

// ParseCursor decodes a pagination cursor produced by FormatCursor back into (page, sinceID).
func ParseCursor(cursor string) (page int, sinceID string) {
	s := strings.TrimSpace(cursor)
	if s == "" {
		return 1, ""
	}
	if pStr, rest, ok := strings.Cut(s, ":"); ok {
		if p, err := strconv.Atoi(strings.TrimSpace(pStr)); err == nil && p > 0 {
			return p, strings.TrimSpace(rest)
		}
	}
	// Bare since_id fallback (page 2 if a non-empty cursor is supplied without explicit page prefix).
	return 2, s
}

// FetchCreatorPosts implements site.SubscriptionFetcher for Weibo incremental update checks.
// On page-1 empty arrays without bottom_tips_visible, it cross-checks profile/info per weibo.md §6.5.
func (a *Adapter) FetchCreatorPosts(ctx context.Context, uid string, cursor string) (site.CreatorPostsPage, error) {
	pageNum, sinceID := ParseCursor(cursor)
	seen := make(map[string]bool)

	page, err := a.client.GetUserPosts(ctx, UserPostsRequest{
		UID:               uid,
		Page:              pageNum,
		SinceID:           sinceID,
		Feature:           FeatureAll,
		SeenIDs:           seen,
		CrossCheckProfile: pageNum == 1,
		StreamOptions:     StreamSelectOptions{PreferredCodec: a.client.preferredCodec()},
	})
	if err != nil {
		return site.CreatorPostsPage{}, err
	}

	posts := make([]site.CreatorPost, 0, len(page.Posts))
	for i := range page.Posts {
		p := &page.Posts[i]
		originID := p.OriMid
		if originID == "" && p.Retweeted != nil {
			originID = p.Retweeted.Mid
		}
		// Build a DEC-040 & NFR-SEC-004 sanitized snapshot: omits online-only Stats and scrubs all signed URLs.
		snapBytes, _ := json.Marshal(buildPostSnapshot(p))
		posts = append(posts, site.CreatorPost{
			ID:            p.Mid,
			PubTs:         p.CreatedAt,
			Type:          p.RuleType,
			IsTop:         p.IsTop,
			IsFastRetweet: p.IsFastRetweet,
			OriginID:      originID,
			Title:         buildPostSummaryTitle(p, 80),
			CoverURL:      safePublicPostCoverURL(p),
			Snapshot:      string(snapBytes),
			VisibleType:   p.Visible.Type,
			VisibleLabel:  p.Visible.Label,
		})
	}

	nextCursor := ""
	if page.HasMore && page.NextSinceID != "" {
		nextCursor = FormatCursor(page.NextPage, page.NextSinceID)
	}

	out := site.CreatorPostsPage{
		Posts:             posts,
		NextCursor:        nextCursor,
		HasMore:           page.HasMore,
		UncertainEmpty:    page.UncertainEmpty,
		UncertainReason:   page.UncertainReason,
		BottomTipsVisible: page.BottomTipsVisible,
		BottomTipsText:    page.BottomTipsText,
		TotalWorks:        page.Total,
	}
	if page.Author != nil {
		out.AuthorName = page.Author.ScreenName
		out.AuthorFace = SafePublicImageURL(page.Author.AvatarURL)
		if page.Author.StatusesCount > 0 {
			out.TotalWorks = page.Author.StatusesCount
		}
	}
	return out, nil
}

// postSnapshot is the persisted subset of Post stored in updates.db CreatorPost.Snapshot.
// Per DEC-040 and stage-plan §4.7, it omits online-only engagement Stats and strips all signed URLs.
type postSnapshot struct {
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
	Retweeted         *postSnapshot   `json:"retweeted,omitempty"`
	RuleType          string          `json:"ruleType"`
	Visible           VisibilityInfo  `json:"visible"`
	Images            []ImageAsset    `json:"images,omitempty"`
	Video             *VideoAsset     `json:"video,omitempty"`
	Card              *RichCard       `json:"card,omitempty"`
	URLMappings       []URLStructItem `json:"urlMappings,omitempty"`
	CanonicalURL      string          `json:"canonicalUrl"`
	Unavailable       bool            `json:"unavailable,omitempty"`
	UnavailableReason string          `json:"unavailableReason,omitempty"`
}

func buildPostSnapshot(p *Post) *postSnapshot {
	if p == nil {
		return nil
	}
	author := p.Author
	author.AvatarURL = SafePublicImageURL(author.AvatarURL)

	var images []ImageAsset
	if len(p.Images) > 0 {
		images = make([]ImageAsset, len(p.Images))
		for i, img := range p.Images {
			img.PublicPreviewURL = SafePublicImageURL(img.PublicPreviewURL)
			img.URL = ""
			img.FallbackURLs = nil
			img.MotionURL = ""
			img.MotionFallbackURLs = nil
			img.GIFVideoURL = ""
			images[i] = img
		}
	}

	var video *VideoAsset
	if p.Video != nil {
		vc := *p.Video
		vc.PublicCoverURL = SafePublicImageURL(vc.PublicCoverURL)
		vc.CoverURL = ""
		video = &vc
	}

	var card *RichCard
	if p.Card != nil {
		cc := *p.Card
		cc.URL = SafePublicWebURL(cc.URL)
		cc.CoverURL = SafePublicImageURL(cc.CoverURL)
		card = &cc
	}

	var mappings []URLStructItem
	if len(p.URLMappings) > 0 {
		mappings = make([]URLStructItem, 0, len(p.URLMappings))
		for _, m := range p.URLMappings {
			shortU := SafePublicWebURL(m.ShortURL)
			longU := SafePublicWebURL(m.LongURL)
			if shortU == "" && longU == "" && strings.TrimSpace(m.URLTitle) == "" {
				continue
			}
			mappings = append(mappings, URLStructItem{
				ShortURL: shortU,
				LongURL:  longU,
				URLTitle: m.URLTitle,
			})
		}
	}

	return &postSnapshot{
		Mid:               p.Mid,
		MblogID:           p.MblogID,
		Author:            author,
		CreatedAt:         p.CreatedAt,
		CreatedAtRaw:      p.CreatedAtRaw,
		Text:              scrubSignedURLTokensInText(p.Text),
		TextRaw:           scrubSignedURLTokensInText(p.TextRaw),
		IsLongText:        p.IsLongText,
		LongTextFetched:   p.LongTextFetched,
		Source:            p.Source,
		RegionName:        p.RegionName,
		EditCount:         p.EditCount,
		IsTop:             p.IsTop,
		IsRetweet:         p.IsRetweet,
		IsFastRetweet:     p.IsFastRetweet,
		OriMid:            p.OriMid,
		Retweeted:         buildPostSnapshot(p.Retweeted),
		RuleType:          p.RuleType,
		Visible:           p.Visible,
		Images:            images,
		Video:             video,
		Card:              card,
		URLMappings:       mappings,
		CanonicalURL:      SafePublicWebURL(p.CanonicalURL),
		Unavailable:       p.Unavailable,
		UnavailableReason: p.UnavailableReason,
	}
}

var reHTTPURLSpan = regexp.MustCompile(`(?i)https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+`)

func scrubSignedURLTokensInText(s string) string {
	if !HasSignedQueryMarker(s) {
		return s
	}
	return reHTTPURLSpan.ReplaceAllStringFunc(s, func(tok string) string {
		if !HasSignedQueryMarker(tok) {
			return tok
		}
		trimmed := strings.TrimRight(tok, ".,;:!?)]}\"'")
		suffix := tok[len(trimmed):]
		return site.SanitizeEndpoint(trimmed) + suffix
	})
}

func safePublicPostCoverURL(p *Post) string {
	if p == nil {
		return ""
	}
	if p.Video != nil && p.Video.PublicCoverURL != "" {
		return p.Video.PublicCoverURL
	}
	for _, img := range p.Images {
		if img.PublicPreviewURL != "" {
			return img.PublicPreviewURL
		}
	}
	if p.Card != nil && p.Card.CoverURL != "" {
		return SafePublicImageURL(p.Card.CoverURL)
	}
	if p.Retweeted != nil && !p.Retweeted.Unavailable {
		return safePublicPostCoverURL(p.Retweeted)
	}
	return ""
}

func buildPostSummaryTitle(p *Post, maxRunes int) string {
	if p == nil {
		return ""
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		text = strings.TrimSpace(p.TextRaw)
	}
	if text == "" && p.Video != nil {
		text = strings.TrimSpace(p.Video.Title)
	}
	if text == "" && p.Card != nil {
		text = strings.TrimSpace(p.Card.Title)
	}
	if text == "" && p.Retweeted != nil {
		text = strings.TrimSpace(p.Retweeted.Text)
	}
	if text == "" {
		return "微博 " + p.Mid
	}
	// Flatten newlines to single spaces for summary title.
	text = strings.Join(strings.Fields(text), " ")
	if maxRunes > 0 && utf8.RuneCountInString(text) > maxRunes {
		runes := []rune(text)
		return string(runes[:maxRunes]) + "…"
	}
	return text
}
