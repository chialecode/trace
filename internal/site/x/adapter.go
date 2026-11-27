package x

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
)

// Compile-time assertions that *Adapter implements all applicable cross-site contracts.
// Note: X intentionally does NOT implement site.LiveProvider (Broadcasts & Spaces excluded per x.md §15.1 #17).
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

// Client returns the underlying X protocol Client.
func (a *Adapter) Client() *Client {
	return a.client
}

// SiteID returns site.X ("x").
func (a *Adapter) SiteID() site.ID {
	return site.X
}

// MediaTransport forwards to the underlying client's cookieless X CDN MediaTransportSpec.
func (a *Adapter) MediaTransport() site.MediaTransportSpec {
	if a == nil || a.client == nil {
		return site.XMediaTransport(DefaultUserAgent, false)
	}
	return a.client.MediaTransport()
}

// RequireAuth forwards to the underlying client so download.SiteAuthChecker works seamlessly.
func (a *Adapter) RequireAuth(action string) error {
	if a == nil || a.client == nil {
		return &site.Error{Site: SiteID, Kind: site.ErrKindAuthRequired, Message: "请先登录 X"}
	}
	return a.client.RequireAuth(action)
}

// ReportAutomaticError forwards to the underlying client so download.SiteErrorReporter works seamlessly.
func (a *Adapter) ReportAutomaticError(ctx context.Context, err error) {
	if a != nil && a.client != nil {
		a.client.ReportAutomaticError(ctx, err)
	}
}

// MatchURL claims X / mirror / t.co / Periscope URLs (and loopback fixture URLs when AllowLocalhost is enabled).
func (a *Adapter) MatchURL(u *url.URL) bool {
	if a == nil || a.client == nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if id, ok := site.DetectHostSite(host); ok {
		return id == site.X
	}
	if !a.client.allowLocalhost || !site.IsLoopbackHost(host) {
		return false
	}
	segs := site.SplitCleanPath(u.Path)
	if len(segs) == 0 {
		return false
	}
	s0 := strings.ToLower(segs[0])
	switch s0 {
	case "tco", "i", "intent":
		return true
	}
	if len(segs) >= 3 && (strings.EqualFold(segs[1], "status") || strings.EqualFold(segs[1], "statuses")) {
		return true
	}
	return false
}

// Resolve implements site.Resolver.
func (a *Adapter) Resolve(ctx context.Context, input string) (site.ResolveTarget, error) {
	return a.client.ResolveLink(ctx, input)
}

// PostItemKey returns the canonical cache entry key for an X post ("x:dynamic:<status_id>") per x.md §15.1 #9.
func PostItemKey(statusID string) string {
	return "x:dynamic:" + strings.TrimSpace(statusID)
}

// PlanDownload implements site.DownloadPlanner using ONLY GraphQL TweetDetail (x.md §15.1 #8).
func (a *Adapter) PlanDownload(ctx context.Context, targetID string, _ int) (site.DownloadPlan, error) {
	post, err := a.client.GetPost(ctx, strings.TrimSpace(targetID))
	if err != nil {
		return site.DownloadPlan{}, err
	}
	return a.PlanDownloadFromPost(post)
}

// PlanDownloadFromPost builds an in-memory site.DownloadPlan directly from an already parsed Post
// (allowing full-cache queues to reuse UserTweets payloads without extra TweetDetail calls when complete).
func (a *Adapter) PlanDownloadFromPost(post *Post) (site.DownloadPlan, error) {
	if post == nil || post.ID == "" {
		return site.DownloadPlan{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindUnavailable,
			Message: "X 帖子内容为空，无法生成缓存计划",
		}
	}

	mediaItems, singleVideo, videoFromOrigin := post.EffectiveMediaWithOrigin()
	title := buildPostSummaryTitle(post, 60)
	desc := post.FullThreadText()
	if desc == "" {
		desc = post.Text
	}

	var (
		originID, originCanonicalURL      string
		originUID, originName, originFace string
		originPubTs                       int64
		originText                        string
		originLinks                       []site.TextLink
		originUnavailable                 bool
		originUnavailableReason           string
		originCard                        *site.RichCardSummary
		originInReplyTo                   *site.ReplyTarget
		originOrigin                      *site.NestedOriginSummary
	)

	isQuote := !post.IsRetweet && post.IsQuote
	replySource := post
	if len(post.ThreadAncestors) > 0 {
		replySource = &post.ThreadAncestors[0]
	}
	inReplyTo := buildPostReplyTarget(replySource)

	var originPost *Post
	if post.IsRetweet && post.Retweeted != nil {
		originPost = post.Retweeted
	} else if post.IsQuote && post.Quoted != nil {
		originPost = post.Quoted
	}
	if originPost != nil {
		originID = originPost.ID
		originCanonicalURL = SafePublicWebURL(originPost.CanonicalURL)
		originUID = originPost.Author.RestID
		originName = formatAuthorDisplayName(originPost.Author)
		originFace = SafePublicImageURL(originPost.Author.AvatarURL)
		originPubTs = originPost.CreatedAt
		originText = scrubSignedURLTokensInText(originPost.FullThreadText())
		originLinks = BuildPostLinks(originPost)
		if post.IsRetweet && post.Retweeted != nil {
			originUnavailable = originPost.Unavailable
			originUnavailableReason = originPost.UnavailableReason
		} else {
			originUnavailable = originPost.Unavailable || post.OriginUnavailable
			originUnavailableReason = firstNonEmpty(post.OriginUnavailableReason, originPost.UnavailableReason)
		}
		originCard = toRichCardSummary(originPost.Card)
		originInReplyTo = buildPostReplyTarget(originPost)
		originOrigin = BuildNestedOriginSummary(originPost)
	} else if post.OriginUnavailable {
		originUnavailable = true
		originUnavailableReason = post.OriginUnavailableReason
	}

	outerCard := toRichCardSummary(post.Card)
	outerLinks := BuildPostLinks(post)
	authorName := formatAuthorDisplayName(post.Author)

	// Single progressive MP4 video (NOT GIF, NOT HLS-only) -> Kind "single_mp4" (x.md §15.1 #9).
	if singleVideo != nil {
		videoURLs := make([]string, 0, 1+len(singleVideo.VideoFallbackURLs))
		videoURLs = append(videoURLs, singleVideo.VideoURL)
		videoURLs = append(videoURLs, singleVideo.VideoFallbackURLs...)

		coverURLs := buildImageCandidateURLs(*singleVideo)
		return site.DownloadPlan{
			Site:                    SiteID,
			ItemKey:                 PostItemKey(post.ID),
			TargetID:                post.ID,
			Kind:                    "single_mp4",
			CanonicalURL:            SafePublicWebURL(post.CanonicalURL),
			AuthorUID:               post.Author.RestID,
			AuthorName:              authorName,
			AuthorFace:              SafePublicImageURL(post.Author.AvatarURL),
			Title:                   title,
			Desc:                    desc,
			Links:                   outerLinks,
			PubTs:                   post.CreatedAt,
			DurationSec:             singleVideo.DurationSec,
			VideoCodec:              "avc",
			QualityLabel:            formatVideoQualityLabel(singleVideo.Width, singleVideo.Height, singleVideo.VideoBitrate),
			Width:                   singleVideo.Width,
			Height:                  singleVideo.Height,
			Bitrate:                 singleVideo.VideoBitrate,
			IsForward:               post.IsForward(),
			IsQuote:                 isQuote,
			InReplyTo:               inReplyTo,
			OriginID:                originID,
			OriginCanonicalURL:      originCanonicalURL,
			OriginAuthorUID:         originUID,
			OriginAuthorName:        originName,
			OriginAuthorFace:        originFace,
			OriginPubTs:             originPubTs,
			OriginText:              originText,
			OriginLinks:             originLinks,
			OriginUnavailable:       originUnavailable,
			OriginUnavailableReason: originUnavailableReason,
			OriginCard:              originCard,
			OriginInReplyTo:         originInReplyTo,
			OriginOrigin:            originOrigin,
			Card:                    outerCard,
			VideoFromOrigin:         videoFromOrigin,
			VisibleLabel:            post.VisibilityLabel,
			VideoURLs:               videoURLs,
			CoverURLs:               coverURLs,
			Media:                   a.MediaTransport(),
		}, nil
	}

	// Photos, GIFs, mixed media (up to 4), HLS-only degraded video, or pure text/card -> Kind "image_note".
	assets := make([]site.MediaImageAsset, 0, len(mediaItems))
	var coverURLs []string

	for i, m := range mediaItems {
		idx := m.Index
		if idx <= 0 {
			idx = i + 1
		}
		imgURLs := buildImageCandidateURLs(m)
		if len(coverURLs) == 0 && len(imgURLs) > 0 {
			coverURLs = []string{imgURLs[0]}
		}

		var vidURLs []string
		// Never put .m3u8 URL into VideoURLs when HLSOnly == true (x.md §15.1 #15).
		if !m.HLSOnly && m.VideoURL != "" {
			vidURLs = append(vidURLs, m.VideoURL)
			vidURLs = append(vidURLs, m.VideoFallbackURLs...)
		}

		role := "image"
		switch m.Kind {
		case MediaKindGIF:
			role = "gif"
		case MediaKindVideo:
			if m.HLSOnly {
				role = "hls_only"
			} else {
				role = "video"
			}
		}

		asset := site.MediaImageAsset{
			Index:        idx,
			MediaRole:    role,
			FromOrigin:   m.FromOrigin,
			Width:        m.Width,
			Height:       m.Height,
			DurationSec:  m.DurationSec,
			QualityLabel: formatVideoQualityLabel(m.Width, m.Height, m.VideoBitrate),
			Bitrate:      m.VideoBitrate,
			ImageURLs:    imgURLs,
			VideoURLs:    vidURLs,
		}
		if role == "hls_only" {
			asset.VideoCodec = "hls"
		} else if role == "video" || role == "gif" {
			asset.VideoCodec = "avc"
		}
		assets = append(assets, asset)
	}

	if len(coverURLs) == 0 && post.Card != nil && post.Card.CoverURL != "" {
		coverURLs = []string{post.Card.CoverURL}
	}
	if len(coverURLs) == 0 && originPost != nil && !originPost.Unavailable && originPost.Card != nil && originPost.Card.CoverURL != "" {
		coverURLs = []string{originPost.Card.CoverURL}
	}

	return site.DownloadPlan{
		Site:                    SiteID,
		ItemKey:                 PostItemKey(post.ID),
		TargetID:                post.ID,
		Kind:                    "image_note",
		CanonicalURL:            SafePublicWebURL(post.CanonicalURL),
		AuthorUID:               post.Author.RestID,
		AuthorName:              authorName,
		AuthorFace:              SafePublicImageURL(post.Author.AvatarURL),
		Title:                   title,
		Desc:                    desc,
		Links:                   outerLinks,
		PubTs:                   post.CreatedAt,
		IsForward:               post.IsForward(),
		IsQuote:                 isQuote,
		InReplyTo:               inReplyTo,
		OriginID:                originID,
		OriginCanonicalURL:      originCanonicalURL,
		OriginAuthorUID:         originUID,
		OriginAuthorName:        originName,
		OriginAuthorFace:        originFace,
		OriginPubTs:             originPubTs,
		OriginText:              originText,
		OriginLinks:             originLinks,
		OriginUnavailable:       originUnavailable,
		OriginUnavailableReason: originUnavailableReason,
		OriginCard:              originCard,
		OriginInReplyTo:         originInReplyTo,
		OriginOrigin:            originOrigin,
		Card:                    outerCard,
		VideoFromOrigin:         videoFromOrigin,
		VisibleLabel:            post.VisibilityLabel,
		CoverURLs:               coverURLs,
		Images:                  assets,
		Media:                   a.MediaTransport(),
	}, nil
}

// FetchCreatorPosts implements site.SubscriptionFetcher for X incremental update checks
// using GraphQL UserTweets (x.md §7 & §15.1 #11, #12).
func (a *Adapter) FetchCreatorPosts(ctx context.Context, uid string, cursor string) (site.CreatorPostsPage, error) {
	seen := make(map[string]bool)
	page, err := a.client.GetUserTweets(ctx, UserTweetsRequest{
		RestID:            uid,
		Cursor:            cursor,
		Count:             DefaultTimelinePageCount,
		SeenIDs:           seen,
		CrossCheckProfile: strings.TrimSpace(cursor) == "",
	})
	if err != nil {
		return site.CreatorPostsPage{}, err
	}

	posts := make([]site.CreatorPost, 0, len(page.Posts))
	for i := range page.Posts {
		p := &page.Posts[i]
		originID := ""
		if p.Retweeted != nil && p.Retweeted.ID != "" {
			originID = p.Retweeted.ID
		} else if p.Quoted != nil && p.Quoted.ID != "" {
			originID = p.Quoted.ID
		}
		snapBytes, _ := json.Marshal(buildPostSnapshot(p))
		posts = append(posts, site.CreatorPost{
			ID:           p.ID,
			PubTs:        p.CreatedAt,
			Type:         p.RuleType,
			IsTop:        p.Pinned,
			OriginID:     originID,
			Title:        buildPostSummaryTitle(p, 80),
			CoverURL:     safePublicPostCoverURL(p),
			Snapshot:     string(snapBytes),
			VisibleLabel: p.VisibilityLabel,
		})
	}

	out := site.CreatorPostsPage{
		Posts:             posts,
		NextCursor:        page.NextCursor,
		HasMore:           page.HasMore,
		BottomTipsVisible: page.BottomTipsVisible,
		BottomTipsText:    page.BottomTipsText,
		UncertainEmpty:    page.UncertainEmpty,
		UncertainReason:   page.UncertainReason,
	}
	if page.Author != nil {
		out.AuthorName = firstNonEmpty(page.Author.Name, page.Author.ScreenName)
		out.AuthorFace = SafePublicImageURL(page.Author.AvatarURL)
		out.UniqueID = page.Author.ScreenName
		if page.Author.StatusesCount > 0 {
			out.TotalWorks = page.Author.StatusesCount
		}
	}
	return out, nil
}

// ---------- Snapshot Sanitization & Formatting Helpers ----------

// postSnapshot is the persisted subset of Post stored in updates.db CreatorPost.Snapshot.
// Per DEC-040 and NFR-SEC-005, it omits online-only Stats/Poll votes and strips all video/signed URLs.
type postSnapshot struct {
	ID                      string        `json:"id"`
	Author                  AuthorSummary `json:"author"`
	CreatedAt               int64         `json:"createdAt"`
	CreatedAtRaw            string        `json:"createdAtRaw,omitempty"`
	Text                    string        `json:"text"`
	IsLongText              bool          `json:"isLongText,omitempty"`
	Pinned                  bool          `json:"pinned,omitempty"`
	IsPreviewDisplay        bool          `json:"isPreviewDisplay,omitempty"`
	VisibilityLabel         string        `json:"visibilityLabel,omitempty"`
	IsEdited                bool          `json:"isEdited,omitempty"`
	EditTweetIDs            []string      `json:"editTweetIds,omitempty"`
	IsRetweet               bool          `json:"isRetweet,omitempty"`
	IsQuote                 bool          `json:"isQuote,omitempty"`
	InReplyToStatusID       string        `json:"inReplyToStatusId,omitempty"`
	InReplyToUserID         string        `json:"inReplyToUserId,omitempty"`
	InReplyToScreenName     string        `json:"inReplyToScreenName,omitempty"`
	Retweeted               *postSnapshot `json:"retweeted,omitempty"`
	Quoted                  *postSnapshot `json:"quoted,omitempty"`
	OriginUnavailable       bool          `json:"originUnavailable,omitempty"`
	OriginUnavailableReason string        `json:"originUnavailableReason,omitempty"`
	RuleType                string        `json:"ruleType"`
	Media                   []MediaItem   `json:"media,omitempty"`
	HasHLSOnlyVideo         bool          `json:"hasHlsOnlyVideo,omitempty"`
	Card                    *RichCard     `json:"card,omitempty"`
	CanonicalURL            string        `json:"canonicalUrl"`
	Unavailable             bool          `json:"unavailable,omitempty"`
	UnavailableReason       string        `json:"unavailableReason,omitempty"`
}

func buildPostSnapshot(p *Post) *postSnapshot {
	if p == nil {
		return nil
	}
	author := p.Author
	author.AvatarURL = SafePublicImageURL(author.AvatarURL)

	var media []MediaItem
	if len(p.Media) > 0 {
		media = make([]MediaItem, len(p.Media))
		for i, m := range p.Media {
			m.PublicPreviewURL = SafePublicImageURL(m.PublicPreviewURL)
			m.ImageURL = ""
			m.FallbackImageURLs = nil
			m.VideoURL = ""
			m.VideoFallbackURLs = nil
			m.HLSURL = ""
			if len(m.Variants) > 0 {
				cleanVars := make([]VideoVariant, len(m.Variants))
				for j, v := range m.Variants {
					v.URL = ""
					cleanVars[j] = v
				}
				m.Variants = cleanVars
			}
			media[i] = m
		}
	}

	var card *RichCard
	if p.Card != nil {
		cc := *p.Card
		cc.URL = SafePublicWebURL(cc.URL)
		cc.CoverURL = SafePublicImageURL(cc.CoverURL)
		cc.Title = scrubSignedURLTokensInText(cc.Title)
		cc.Desc = scrubSignedURLTokensInText(cc.Desc)
		card = &cc
	}

	return &postSnapshot{
		ID:                      p.ID,
		Author:                  author,
		CreatedAt:               p.CreatedAt,
		CreatedAtRaw:            p.CreatedAtRaw,
		Text:                    scrubSignedURLTokensInText(p.Text),
		IsLongText:              p.IsLongText,
		Pinned:                  p.Pinned,
		IsPreviewDisplay:        p.IsPreviewDisplay,
		VisibilityLabel:         p.VisibilityLabel,
		IsEdited:                p.IsEdited,
		EditTweetIDs:            append([]string(nil), p.EditTweetIDs...),
		IsRetweet:               p.IsRetweet,
		IsQuote:                 p.IsQuote,
		InReplyToStatusID:       strings.TrimSpace(p.InReplyToStatusID),
		InReplyToUserID:         strings.TrimSpace(p.InReplyToUserID),
		InReplyToScreenName:     strings.TrimPrefix(strings.TrimSpace(p.InReplyToScreenName), "@"),
		Retweeted:               buildPostSnapshot(p.Retweeted),
		Quoted:                  buildPostSnapshot(p.Quoted),
		OriginUnavailable:       p.OriginUnavailable,
		OriginUnavailableReason: p.OriginUnavailableReason,
		RuleType:                p.RuleType,
		Media:                   media,
		HasHLSOnlyVideo:         p.HasHLSOnlyVideo,
		Card:                    card,
		CanonicalURL:            SafePublicWebURL(p.CanonicalURL),
		Unavailable:             p.Unavailable,
		UnavailableReason:       p.UnavailableReason,
	}
}

// BuildPostReplyTarget converts an X post's in_reply_to_* fields into a clean
// site.ReplyTarget, returning nil when the post is not a reply.
func BuildPostReplyTarget(p *Post) *site.ReplyTarget {
	return buildPostReplyTarget(p)
}

func buildPostReplyTarget(p *Post) *site.ReplyTarget {
	if p == nil {
		return nil
	}
	sid := strings.TrimSpace(p.InReplyToStatusID)
	uid := strings.TrimSpace(p.InReplyToUserID)
	handle := strings.TrimPrefix(strings.TrimSpace(p.InReplyToScreenName), "@")
	if sid == "" && uid == "" && handle == "" {
		return nil
	}
	return &site.ReplyTarget{
		StatusID:   sid,
		UserID:     uid,
		ScreenName: handle,
	}
}

// BuildNestedOriginSummary extracts at most 1 nested quoted/retweeted post summary
// from an outer origin post (DEC-091), preventing unbounded nesting.
func BuildNestedOriginSummary(parent *Post) *site.NestedOriginSummary {
	if parent == nil {
		return nil
	}
	var inner *Post
	if parent.Quoted != nil {
		inner = parent.Quoted
	} else if parent.Retweeted != nil {
		inner = parent.Retweeted
	}
	if inner != nil {
		return &site.NestedOriginSummary{
			Site:              SiteID,
			ID:                strings.TrimSpace(inner.ID),
			CanonicalURL:      SafePublicWebURL(inner.CanonicalURL),
			AuthorUID:         strings.TrimSpace(inner.Author.RestID),
			AuthorUniqueID:    strings.TrimSpace(inner.Author.ScreenName),
			AuthorName:        formatAuthorDisplayName(inner.Author),
			AuthorFace:        SafePublicImageURL(inner.Author.AvatarURL),
			PubTs:             inner.CreatedAt,
			Text:              scrubSignedURLTokensInText(inner.FullThreadText()),
			Links:             BuildPostLinks(inner),
			Card:              toRichCardSummary(inner.Card),
			InReplyTo:         buildPostReplyTarget(inner),
			Unavailable:       inner.Unavailable || parent.OriginUnavailable,
			UnavailableReason: firstNonEmpty(parent.OriginUnavailableReason, inner.UnavailableReason),
		}
	}
	if parent.OriginUnavailable {
		return &site.NestedOriginSummary{
			Site:              SiteID,
			Unavailable:       true,
			UnavailableReason: strings.TrimSpace(parent.OriginUnavailableReason),
		}
	}
	return nil
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

func buildImageCandidateURLs(m MediaItem) []string {
	out := make([]string, 0, 1+len(m.FallbackImageURLs))
	if m.ImageURL != "" {
		out = append(out, m.ImageURL)
	}
	out = append(out, m.FallbackImageURLs...)
	return out
}

func formatAuthorDisplayName(a AuthorSummary) string {
	return firstNonEmpty(a.Name, a.ScreenName, a.RestID)
}

func formatVideoQualityLabel(w, h int, bitrate int64) string {
	if w > 0 && h > 0 {
		shortEdge := w
		if h < shortEdge {
			shortEdge = h
		}
		return fmt.Sprintf("%dp", shortEdge)
	}
	if bitrate > 0 {
		return fmt.Sprintf("%dkbps", bitrate/1000)
	}
	return ""
}

func safePublicPostCoverURL(p *Post) string {
	if p == nil {
		return ""
	}
	for _, m := range p.Media {
		if m.PublicPreviewURL != "" {
			return m.PublicPreviewURL
		}
	}
	if p.Card != nil && p.Card.CoverURL != "" {
		return SafePublicImageURL(p.Card.CoverURL)
	}
	if p.Retweeted != nil && !p.Retweeted.Unavailable {
		if u := safePublicPostCoverURL(p.Retweeted); u != "" {
			return u
		}
	}
	if p.Quoted != nil && !p.Quoted.Unavailable {
		if u := safePublicPostCoverURL(p.Quoted); u != "" {
			return u
		}
	}
	return ""
}

func buildPostSummaryTitle(p *Post, maxRunes int) string {
	if p == nil {
		return ""
	}
	text := strings.TrimSpace(p.Text)
	if text == "" && p.Card != nil {
		text = strings.TrimSpace(p.Card.Title)
	}
	if text == "" && p.Retweeted != nil && !p.Retweeted.Unavailable {
		text = strings.TrimSpace(p.Retweeted.Text)
	}
	if text == "" && p.Quoted != nil && !p.Quoted.Unavailable {
		text = strings.TrimSpace(p.Quoted.Text)
	}
	if text == "" {
		return "X 帖子 " + p.ID
	}
	text = strings.Join(strings.Fields(text), " ")
	if maxRunes > 0 && utf8.RuneCountInString(text) > maxRunes {
		runes := []rune(text)
		return string(runes[:maxRunes]) + "…"
	}
	return text
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
