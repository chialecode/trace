package x

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

// Rule types mapped per docs/research/x.md §15.1 #10.
const (
	RuleTypeForward = "forward"
	RuleTypeVideo   = "video"
	RuleTypeDraw    = "draw"
	RuleTypeWord    = "word"
)

// Media item kinds within an X post's ordered media list (up to 4 items).
const (
	MediaKindPhoto = "photo"
	MediaKindGIF   = "gif"
	MediaKindVideo = "video"
)

// Rich card kinds for degraded non-media card attachments (x.md §5.5).
const (
	CardKindVote    = "vote"
	CardKindWebpage = "webpage"
	CardKindArticle = "article"
	CardKindLive    = "live"
)

const (
	// MaxThreadDepth caps same-author self-reply thread stitching to guard against pathological chains.
	MaxThreadDepth = 50

	// HLSOnlyNoticeText is the exact user-facing notice when a video only exposes HLS/m3u8 (x.md §15.1 #15).
	HLSOnlyNoticeText = "这条视频只有分段流，暂不支持缓存"

	maxTweetNestingDepth = 3
)

var (
	reVideoDimInURL = regexp.MustCompile(`/(\d{2,5})x(\d{2,5})/`)
	rePollCardName  = regexp.MustCompile(`(?i)^poll[2-4]choice_`)
	reStatusIDSuff  = regexp.MustCompile(`(?i)/status(?:es)?/([0-9]+)(?:/(?:photo|video)/[0-9]+)?/?$`)
)

// AuthorSummary holds normalized X user metadata embedded in posts, timelines, and profiles.
type AuthorSummary struct {
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

// PostStats holds online-only engagement counters (never persisted in snapshots per DEC-040).
type PostStats struct {
	ReplyCount    int64 `json:"replyCount"`
	RetweetCount  int64 `json:"retweetCount"`
	LikeCount     int64 `json:"likeCount"`
	QuoteCount    int64 `json:"quoteCount"`
	BookmarkCount int64 `json:"bookmarkCount"`
	ViewCount     int64 `json:"viewCount"`
}

// URLEntity holds one restored t.co -> expanded URL mapping from a post's entities.
type URLEntity struct {
	ShortURL    string `json:"shortUrl,omitempty"`
	ExpandedURL string `json:"expandedUrl,omitempty"`
	DisplayURL  string `json:"displayUrl,omitempty"`
}

// VideoVariant describes one video/GIF stream candidate from video_info.variants[].
// Ephemeral/CDN URLs are tagged json:"-" so they never persist into snapshots or logs.
type VideoVariant struct {
	ContentType string `json:"contentType"`
	Bitrate     int64  `json:"bitrate,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	URL         string `json:"-"`
}

// MediaItem represents one ordered media attachment (photo, animated_gif, or video, up to 4).
// All raw CDN download URLs are tagged json:"-" so signed URLs are never persisted.
type MediaItem struct {
	Index             int            `json:"index"` // 1-based original order
	MediaKey          string         `json:"mediaKey,omitempty"`
	Kind              string         `json:"kind"` // MediaKindPhoto, MediaKindGIF, MediaKindVideo
	GIF               bool           `json:"gif,omitempty"`
	FromOrigin        bool           `json:"fromOrigin,omitempty"`
	Width             int            `json:"width,omitempty"`
	Height            int            `json:"height,omitempty"`
	DurationMS        int64          `json:"durationMs,omitempty"`
	DurationSec       int            `json:"durationSec,omitempty"`
	AltText           string         `json:"altText,omitempty"`
	PublicPreviewURL  string         `json:"publicPreviewUrl,omitempty"` // unsigned pbs.twimg.com preview only
	ImageURL          string         `json:"-"`                          // primary ?format=<ext>&name=orig
	FallbackImageURLs []string       `json:"-"`                          // ?format=<ext>&name=large fallback
	VideoURL          string         `json:"-"`                          // highest bitrate video/mp4 (empty if HLSOnly)
	VideoFallbackURLs []string       `json:"-"`                          // lower bitrate video/mp4 fallbacks
	VideoBitrate      int64          `json:"videoBitrate,omitempty"`
	Variants          []VideoVariant `json:"variants,omitempty"`
	HLSOnly           bool           `json:"hlsOnly,omitempty"`
	HLSOnlyNotice     string         `json:"hlsOnlyNotice,omitempty"`
	HLSURL            string         `json:"-"`
}

// PollOption represents one option in an X poll card.
type PollOption struct {
	Position int    `json:"position"`
	Label    string `json:"label"`
	Votes    int64  `json:"votes,omitempty"`
}

// PollSummary holds online poll metadata extracted from a poll card.
type PollSummary struct {
	Options    []PollOption `json:"options"`
	EndTime    string       `json:"endTime,omitempty"`
	Final      bool         `json:"final,omitempty"`
	TotalVotes int64        `json:"totalVotes,omitempty"`
}

// RichCard represents a safely degraded non-media card (poll, URL card, X Article, Broadcast/Spaces).
// Degraded cards NEVER create video cache entries.
type RichCard struct {
	Kind        string `json:"kind"` // CardKindVote, CardKindWebpage, CardKindArticle, CardKindLive
	Label       string `json:"label"`
	Title       string `json:"title,omitempty"`
	Desc        string `json:"desc,omitempty"`
	URL         string `json:"url,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	Unsupported bool   `json:"unsupported"`
}

// Post represents a normalized X post parsed from TweetDetail, UserTweets, Bookmarks, or Likes.
type Post struct {
	ID                      string        `json:"id"`
	Author                  AuthorSummary `json:"author"`
	CreatedAt               int64         `json:"createdAt"`
	CreatedAtRaw            string        `json:"createdAtRaw,omitempty"`
	Text                    string        `json:"text"`
	TextRaw                 string        `json:"textRaw,omitempty"`
	IsLongText              bool          `json:"isLongText,omitempty"`
	ConversationID          string        `json:"conversationId,omitempty"`
	InReplyToStatusID       string        `json:"inReplyToStatusId,omitempty"`
	InReplyToUserID         string        `json:"inReplyToUserId,omitempty"`
	InReplyToScreenName     string        `json:"inReplyToScreenName,omitempty"`
	SelfThreadID            string        `json:"selfThreadId,omitempty"`
	Pinned                  bool          `json:"pinned,omitempty"`
	IsPreviewDisplay        bool          `json:"isPreviewDisplay,omitempty"`
	VisibilityLabel         string        `json:"visibilityLabel,omitempty"`
	PossiblySensitive       bool          `json:"possiblySensitive,omitempty"`
	WithheldInCountries     []string      `json:"withheldInCountries,omitempty"`
	EditTweetIDs            []string      `json:"editTweetIds,omitempty"`
	IsEdited                bool          `json:"isEdited,omitempty"`
	IsRetweet               bool          `json:"isRetweet,omitempty"`
	IsQuote                 bool          `json:"isQuote,omitempty"`
	Retweeted               *Post         `json:"retweeted,omitempty"`
	Quoted                  *Post         `json:"quoted,omitempty"`
	OriginUnavailable       bool          `json:"originUnavailable,omitempty"`
	OriginUnavailableReason string        `json:"originUnavailableReason,omitempty"`
	ThreadAncestors         []Post        `json:"threadAncestors,omitempty"`
	Thread                  []Post        `json:"thread,omitempty"`
	RuleType                string        `json:"ruleType"` // RuleTypeForward, RuleTypeVideo, RuleTypeDraw, RuleTypeWord
	Media                   []MediaItem   `json:"media,omitempty"`
	HasHLSOnlyVideo         bool          `json:"hasHlsOnlyVideo,omitempty"`
	Poll                    *PollSummary  `json:"poll,omitempty"`
	Card                    *RichCard     `json:"card,omitempty"`
	Stats                   PostStats     `json:"stats"`
	URLs                    []URLEntity   `json:"urls,omitempty"`
	CanonicalURL            string        `json:"canonicalUrl"`
	Unavailable             bool          `json:"unavailable,omitempty"`
	UnavailableReason       string        `json:"unavailableReason,omitempty"`
	UnavailableKind         string        `json:"unavailableKind,omitempty"`
}

// IsForward reports whether this post is a retweet or quote post (x.md §15.1 #9, #10).
func (p *Post) IsForward() bool {
	if p == nil {
		return false
	}
	return p.IsRetweet || p.IsQuote
}

// FullThreadText returns the full text of the post concatenated with any stitched same-author
// thread ancestors and self-reply continuations in chronological order.
func (p *Post) FullThreadText() string {
	if p == nil {
		return ""
	}
	parts := make([]string, 0, len(p.ThreadAncestors)+1+len(p.Thread))
	for _, a := range p.ThreadAncestors {
		if t := strings.TrimSpace(a.Text); t != "" {
			parts = append(parts, t)
		}
	}
	if t := strings.TrimSpace(p.Text); t != "" {
		parts = append(parts, t)
	}
	for _, r := range p.Thread {
		if t := strings.TrimSpace(r.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// EffectiveMediaWithOrigin resolves the ordered downloadable media list for a post,
// combining outer and origin media for retweets/quotes while tagging FromOrigin accurately.
func (p *Post) EffectiveMediaWithOrigin() (items []MediaItem, singleVideo *MediaItem, videoFromOrigin bool) {
	if p == nil {
		return nil, nil, false
	}
	var all []MediaItem
	appendTagged := func(src []MediaItem, fromOrigin bool) {
		for _, m := range src {
			cp := m
			cp.FromOrigin = fromOrigin
			cp.Index = len(all) + 1
			all = append(all, cp)
		}
	}

	if p.IsRetweet && p.Retweeted != nil && !p.Retweeted.Unavailable {
		appendTagged(p.Retweeted.Media, true)
		if p.Retweeted.Quoted != nil && !p.Retweeted.Quoted.Unavailable && len(all) == 0 {
			appendTagged(p.Retweeted.Quoted.Media, true)
		}
	} else {
		appendTagged(p.Media, false)
		if p.Quoted != nil && !p.Quoted.Unavailable && len(p.Quoted.Media) > 0 {
			appendTagged(p.Quoted.Media, true)
		}
	}

	if len(all) == 1 && all[0].Kind == MediaKindVideo && !all[0].GIF && !all[0].HLSOnly && all[0].VideoURL != "" {
		v := all[0]
		return nil, &v, v.FromOrigin
	}
	return all, nil, false
}

// GetPost fetches a single X post (including note_tweet full text, retweets, quotes, media,
// degraded cards, and author self-reply thread) using ONLY GraphQL TweetDetail (x.md §15.1 #8).
func (c *Client) GetPost(ctx context.Context, statusID string) (*Post, error) {
	sf, err := ParseSnowflakeID(strings.TrimSpace(statusID))
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 帖子编号",
		}
	}
	normID := sf.ID

	vars := map[string]any{
		"focalTweetId":                           normID,
		"with_rux_injections":                    false,
		"rankingMode":                            "Relevance",
		"includePromotedContent":                 false,
		"withCommunity":                          true,
		"withQuickPromoteEligibilityTweetFields": false,
		"withBirdwatchNotes":                     true,
		"withVoice":                              true,
		"withV2Timeline":                         true,
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpTweetDetail,
		variables: vars,
		referer:   canonicalStatusURL("", normID),
	})
	if err != nil {
		return nil, err
	}

	return c.parseTweetDetailData(gqlResp.data, normID)
}

// ParseTweetDetailData parses a raw TweetDetail `data` JSON object for expectedStatusID.
// Exposed for clean synthetic fixture verification and offline parsing.
func (c *Client) ParseTweetDetailData(rawData []byte, expectedStatusID string) (*Post, error) {
	return c.parseTweetDetailData(rawData, expectedStatusID)
}

func (c *Client) parseTweetDetailData(rawData json.RawMessage, expectedStatusID string) (*Post, error) {
	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpTweetDetail)

	var root struct {
		ThreadedConv *struct {
			Instructions []rawTimelineInstruction `json:"instructions"`
		} `json:"threaded_conversation_with_injections_v2"`
		ThreadedConvV1 *struct {
			Instructions []rawTimelineInstruction `json:"instructions"`
		} `json:"threaded_conversation_with_injections"`
	}
	if err := decodeJSONUseNumber(rawData, &root); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X TweetDetail 响应结构失败",
			Err:      err,
		}
	}

	var instructions []rawTimelineInstruction
	switch {
	case root.ThreadedConv != nil:
		instructions = root.ThreadedConv.Instructions
	case root.ThreadedConvV1 != nil:
		instructions = root.ThreadedConvV1.Instructions
	default:
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "X TweetDetail 响应缺少对话时间线指令",
		}
	}

	var (
		focalEntryCandidate *Post
		focalSlotMismatch   bool
		focalSlotActualID   string
		focalUnavailable    *Post
		orderedTweets       []*Post
		byID                = make(map[string]*Post)
	)

	recordTweet := func(p *Post) {
		if p == nil || p.Unavailable || p.ID == "" {
			return
		}
		if _, exists := byID[p.ID]; !exists {
			byID[p.ID] = p
			orderedTweets = append(orderedTweets, p)
		}
	}

	expectedEntryID := "tweet-" + expectedStatusID

	for _, instr := range instructions {
		instrType := instr.instructionType()
		if instrType != "TimelineAddEntries" && instrType != "TimelineAddToModule" {
			continue
		}

		// Handle direct entries.
		for _, entry := range instr.Entries {
			if isPromotedOrNoiseEntryID(entry.EntryID) {
				continue
			}
			cType := entry.Content.contentType()
			switch cType {
			case "TimelineTimelineItem":
				if entry.Content.ItemContent == nil || hasNonEmptyRawJSON(entry.Content.ItemContent.PromotedMetadata) {
					continue
				}
				if entry.Content.ItemContent.TweetResults == nil {
					continue
				}
				p := c.parseTweetResult(entry.Content.ItemContent.TweetResults.Result, 0)
				if p == nil {
					continue
				}
				isFocalSlot := entry.EntryID == expectedEntryID || strings.EqualFold(entry.Content.ItemContent.TweetDisplayType, "Tweet")
				if p.Unavailable {
					if entry.EntryID == expectedEntryID || focalUnavailable == nil {
						focalUnavailable = p
					}
					continue
				}
				recordTweet(p)
				if entry.EntryID == expectedEntryID {
					if p.ID != expectedStatusID {
						focalSlotMismatch = true
						focalSlotActualID = p.ID
					} else {
						focalEntryCandidate = p
					}
				} else if p.ID == expectedStatusID && focalEntryCandidate == nil {
					focalEntryCandidate = p
				} else if isFocalSlot && focalEntryCandidate == nil && !focalSlotMismatch {
					focalSlotMismatch = true
					focalSlotActualID = p.ID
				}

			case "TimelineTimelineModule":
				for _, modItem := range entry.Content.Items {
					if isPromotedOrNoiseEntryID(modItem.EntryID) {
						continue
					}
					ic := modItem.Item.ItemContent
					if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.TweetResults == nil {
						continue
					}
					p := c.parseTweetResult(ic.TweetResults.Result, 0)
					if p == nil {
						continue
					}
					if p.Unavailable {
						if p.ID == expectedStatusID && focalUnavailable == nil {
							focalUnavailable = p
						}
						continue
					}
					recordTweet(p)
					if p.ID == expectedStatusID && focalEntryCandidate == nil {
						focalEntryCandidate = p
					}
				}
			}
		}

		// Handle TimelineAddToModule items.
		for _, modItem := range instr.ModuleItems {
			if isPromotedOrNoiseEntryID(modItem.EntryID) {
				continue
			}
			ic := modItem.Item.ItemContent
			if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.TweetResults == nil {
				continue
			}
			p := c.parseTweetResult(ic.TweetResults.Result, 0)
			if p != nil && !p.Unavailable {
				recordTweet(p)
				if p.ID == expectedStatusID && focalEntryCandidate == nil {
					focalEntryCandidate = p
				}
			}
		}
	}

	// If focal tweet is unavailable / tombstone, return its classified single-target error.
	if focalEntryCandidate == nil && focalUnavailable != nil {
		kind := focalUnavailable.UnavailableKind
		if kind == "" {
			kind = site.ErrKindUnavailable
		}
		reason := firstNonEmpty(focalUnavailable.UnavailableReason, "该 X 帖子暂不可见")
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     kind,
			Endpoint: endpoint,
			Message:  reason,
		}
	}

	// Verify focal rest_id matches requested status_id (mismatch -> schema_changed per M7-K07!).
	if focalEntryCandidate == nil {
		if focalSlotMismatch || len(orderedTweets) > 0 {
			actual := focalSlotActualID
			if actual == "" && len(orderedTweets) > 0 {
				actual = orderedTweets[0].ID
			}
			return nil, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindSchemaChanged,
				Endpoint: endpoint,
				Message:  fmt.Sprintf("X 返回的帖子 ID 与请求目标不一致（期望 %s，实际 %s）", expectedStatusID, actual),
			}
		}
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 帖子不存在或已被删除",
		}
	}

	if focalEntryCandidate.ID != expectedStatusID {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  fmt.Sprintf("X 返回的帖子 ID 与请求目标不一致（期望 %s，实际 %s）", expectedStatusID, focalEntryCandidate.ID),
		}
	}

	// Stitch author self-reply thread from TweetDetail entries (cycle-guarded, depth-capped,
	// strictly excluding replies from other users).
	stitchAuthorSelfReplyThread(focalEntryCandidate, orderedTweets, byID)
	return focalEntryCandidate, nil
}

// stitchAuthorSelfReplyThread stitches same-author ancestors and self-reply continuations
// onto focal using a visited set cycle guard and MaxThreadDepth (50) cap.
// Replies authored by any user other than focal.Author.RestID are strictly filtered out.
func stitchAuthorSelfReplyThread(focal *Post, ordered []*Post, byID map[string]*Post) {
	if focal == nil || focal.Author.RestID == "" {
		return
	}
	authorID := focal.Author.RestID

	// 1. Walk upward along same-author ancestors (if focal is mid-thread).
	visited := map[string]bool{focal.ID: true}
	var revAncestors []Post
	cur := focal
	for len(revAncestors) < MaxThreadDepth {
		parentID := cur.InReplyToStatusID
		if parentID == "" || visited[parentID] {
			break
		}
		parent, ok := byID[parentID]
		if !ok || parent == nil || parent.Unavailable || parent.Author.RestID != authorID {
			break
		}
		visited[parent.ID] = true
		revAncestors = append(revAncestors, *parent)
		cur = parent
	}
	if len(revAncestors) > 0 {
		anc := make([]Post, len(revAncestors))
		for i := range revAncestors {
			anc[i] = revAncestors[len(revAncestors)-1-i]
		}
		focal.ThreadAncestors = anc
	}

	// 2. Build same-author child index (in_reply_to_status_id -> ordered same-author replies).
	children := make(map[string][]*Post)
	for _, p := range ordered {
		if p == nil || p.Unavailable || p.ID == focal.ID {
			continue
		}
		if p.Author.RestID != authorID {
			continue
		}
		if p.InReplyToStatusID != "" {
			children[p.InReplyToStatusID] = append(children[p.InReplyToStatusID], p)
		}
	}

	// Walk downward along same-author self-reply chain starting from focal.ID.
	var continuations []Post
	queue := []*Post{focal}
	for len(queue) > 0 && len(continuations) < MaxThreadDepth {
		node := queue[0]
		queue = queue[1:]
		kids := children[node.ID]
		for _, kid := range kids {
			if len(continuations) >= MaxThreadDepth {
				break
			}
			if visited[kid.ID] {
				continue
			}
			visited[kid.ID] = true
			continuations = append(continuations, *kid)
			queue = append(queue, kid)
		}
	}
	if len(continuations) > 0 {
		focal.Thread = continuations
	}
}

// ---------- Raw GraphQL JSON Structures & Unwrapping ----------

type rawTimelineInstruction struct {
	Type        string                   `json:"type"`
	Typename    string                   `json:"__typename"`
	Entry       *rawTimelineEntry        `json:"entry"`
	Entries     []rawTimelineEntry       `json:"entries"`
	ModuleItems []rawTimelineModuleEntry `json:"moduleItems"`
}

func (i *rawTimelineInstruction) instructionType() string {
	if i == nil {
		return ""
	}
	if s := strings.TrimSpace(i.Type); s != "" {
		return s
	}
	return strings.TrimSpace(i.Typename)
}

type rawTimelineEntry struct {
	EntryID   string             `json:"entryId"`
	SortIndex string             `json:"sortIndex"`
	Content   rawTimelineContent `json:"content"`
}

type rawTimelineContent struct {
	EntryType   string                   `json:"entryType"`
	Typename    string                   `json:"__typename"`
	CursorType  string                   `json:"cursorType"`
	Value       string                   `json:"value"`
	DisplayType string                   `json:"displayType"`
	ItemContent *rawTimelineItemContent  `json:"itemContent"`
	Items       []rawTimelineModuleEntry `json:"items"`
}

func (c *rawTimelineContent) contentType() string {
	if c == nil {
		return ""
	}
	if s := strings.TrimSpace(c.EntryType); s != "" {
		return s
	}
	return strings.TrimSpace(c.Typename)
}

type rawTimelineModuleEntry struct {
	EntryID string `json:"entryId"`
	Item    struct {
		ItemContent *rawTimelineItemContent `json:"itemContent"`
	} `json:"item"`
}

type rawTimelineItemContent struct {
	ItemType         string          `json:"itemType"`
	Typename         string          `json:"__typename"`
	TweetDisplayType string          `json:"tweetDisplayType"`
	PromotedMetadata json.RawMessage `json:"promotedMetadata"`
	TweetResults     *struct {
		Result json.RawMessage `json:"result"`
	} `json:"tweet_results"`
	UserResults *struct {
		Result json.RawMessage `json:"result"`
	} `json:"user_results"`
}

type rawTweetResultNode struct {
	Typename   string          `json:"__typename"`
	RestID     string          `json:"rest_id"`
	Reason     string          `json:"reason"`
	Tweet      json.RawMessage `json:"tweet"`
	Tombstone  *rawTombstone   `json:"tombstone"`
	Core       *rawTweetCore   `json:"core"`
	Legacy     *rawTweetLegacy `json:"legacy"`
	NoteTweet  *rawNoteTweet   `json:"note_tweet"`
	QuotedRes  *rawTweetWrap   `json:"quoted_status_result"`
	Card       *rawCardWrapper `json:"card"`
	Article    *rawArticleWrap `json:"article"`
	Views      *rawTweetViews  `json:"views"`
	EditCtrl   *rawEditControl `json:"edit_control"`
	LimActions json.RawMessage `json:"limitedActionResults"`
	// Preview display fields when TweetPreviewDisplay places data directly under tweet.
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

type rawTombstone struct {
	Text *struct {
		Text string `json:"text"`
	} `json:"text"`
}

type rawTweetWrap struct {
	Result json.RawMessage `json:"result"`
}

type rawTweetCore struct {
	UserResults *struct {
		Result json.RawMessage `json:"result"`
	} `json:"user_results"`
}

type rawTweetViews struct {
	Count json.RawMessage `json:"count"`
	State string          `json:"state"`
}

type rawEditControl struct {
	EditTweetIDs       []string `json:"edit_tweet_ids"`
	EditControlInitial *struct {
		EditTweetIDs []string `json:"edit_tweet_ids"`
	} `json:"edit_control_initial"`
}

type rawNoteTweet struct {
	IsExpandable     bool `json:"is_expandable"`
	NoteTweetResults *struct {
		Result *struct {
			ID        string          `json:"id"`
			Text      string          `json:"text"`
			EntitySet *rawEntitiesSet `json:"entity_set"`
		} `json:"result"`
	} `json:"note_tweet_results"`
}

type rawEntitiesSet struct {
	URLs  []rawURLEntity   `json:"urls"`
	Media []rawMediaEntity `json:"media"`
}

type rawURLEntity struct {
	URL         string `json:"url"`
	ExpandedURL string `json:"expanded_url"`
	DisplayURL  string `json:"display_url"`
}

type rawMediaEntity struct {
	IDStr         string `json:"id_str"`
	MediaKey      string `json:"media_key"`
	Type          string `json:"type"` // "photo", "animated_gif", "video"
	URL           string `json:"url"`  // t.co media link in full_text
	MediaURLHTTPS string `json:"media_url_https"`
	ExtAltText    string `json:"ext_alt_text"`
	OriginalInfo  *struct {
		Width  json.RawMessage `json:"width"`
		Height json.RawMessage `json:"height"`
	} `json:"original_info"`
	Sizes *struct {
		Large *struct {
			W json.RawMessage `json:"w"`
			H json.RawMessage `json:"h"`
		} `json:"large"`
	} `json:"sizes"`
	VideoInfo *struct {
		DurationMillis json.RawMessage `json:"duration_millis"`
		AspectRatio    []int           `json:"aspect_ratio"`
		Variants       []struct {
			Bitrate     json.RawMessage `json:"bitrate"`
			ContentType string          `json:"content_type"`
			URL         string          `json:"url"`
		} `json:"variants"`
	} `json:"video_info"`
}

type rawTweetLegacy struct {
	IDStr               string `json:"id_str"`
	UserIDStr           string `json:"user_id_str"`
	CreatedAt           string `json:"created_at"`
	FullText            string `json:"full_text"`
	ConversationIDStr   string `json:"conversation_id_str"`
	InReplyToStatusID   string `json:"in_reply_to_status_id_str"`
	InReplyToUserID     string `json:"in_reply_to_user_id_str"`
	InReplyToScreenName string `json:"in_reply_to_screen_name"`
	SelfThread          *struct {
		IDStr string `json:"id_str"`
	} `json:"self_thread"`
	IsQuoteStatus       bool            `json:"is_quote_status"`
	QuotedStatusIDStr   string          `json:"quoted_status_id_str"`
	PossiblySensitive   bool            `json:"possibly_sensitive"`
	WithheldInCountries []string        `json:"withheld_in_countries"`
	ReplyCount          json.RawMessage `json:"reply_count"`
	RetweetCount        json.RawMessage `json:"retweet_count"`
	FavoriteCount       json.RawMessage `json:"favorite_count"`
	QuoteCount          json.RawMessage `json:"quote_count"`
	BookmarkCount       json.RawMessage `json:"bookmark_count"`
	Entities            *rawEntitiesSet `json:"entities"`
	ExtendedEntities    *rawEntitiesSet `json:"extended_entities"`
	RetweetedStatusRes  *rawTweetWrap   `json:"retweeted_status_result"`
}

type rawCardWrapper struct {
	RestID string `json:"rest_id"`
	Legacy *struct {
		Name          string `json:"name"`
		URL           string `json:"url"`
		BindingValues []struct {
			Key   string `json:"key"`
			Value struct {
				Type        string `json:"type"`
				StringValue string `json:"string_value"`
				BoolValue   bool   `json:"boolean_value"`
				ImageValue  *struct {
					URL string `json:"url"`
				} `json:"image_value"`
			} `json:"value"`
		} `json:"binding_values"`
	} `json:"legacy"`
}

type rawArticleWrap struct {
	ArticleResults *struct {
		Result *struct {
			RestID      string `json:"rest_id"`
			Title       string `json:"title"`
			PreviewText string `json:"preview_text"`
			CoverMedia  *struct {
				MediaInfo *struct {
					OriginalImgURL string `json:"original_img_url"`
				} `json:"media_info"`
			} `json:"cover_media"`
			CoverMediaImgURL string `json:"cover_media_img_url"`
		} `json:"result"`
	} `json:"article_results"`
}

// parseTweetResult unwraps X GraphQL tweet nodes strictly following x.md §5.2 #2 order:
//  1. TweetPreviewDisplay (FIRST — before checking result.tweet != nil!)
//  2. TweetTombstone / TweetUnavailable (SECOND)
//  3. TweetWithVisibilityResults / generic result.tweet wrapper (THIRD)
func (c *Client) parseTweetResult(raw json.RawMessage, depth int) *Post {
	if depth > maxTweetNestingDepth || isJSONNull(raw) {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}

	cur := trimmed
	var visibilityLabel string

	for unwrapIter := 0; unwrapIter < 4; unwrapIter++ {
		var node rawTweetResultNode
		if err := decodeJSONUseNumber(cur, &node); err != nil {
			return nil
		}

		// Step 1 (FIRST): TweetPreviewDisplay — subscriber-only preview with data directly on node.Tweet (no full legacy!).
		if node.Typename == "TweetPreviewDisplay" {
			return c.parseTweetPreviewDisplay(&node)
		}

		// Step 2 (SECOND): TweetTombstone / TweetUnavailable.
		if node.Typename == "TweetTombstone" {
			reason := "该帖子已不可见或已被删除"
			if node.Tombstone != nil && node.Tombstone.Text != nil {
				if t := strings.TrimSpace(node.Tombstone.Text.Text); t != "" {
					reason = t
				}
			}
			return &Post{
				ID:                strings.TrimSpace(node.RestID),
				Unavailable:       true,
				UnavailableReason: reason,
				UnavailableKind:   classifyTombstoneKind(reason),
			}
		}
		if node.Typename == "TweetUnavailable" {
			kind, msg := ClassifyTweetUnavailableReason(node.Reason)
			return &Post{
				ID:                strings.TrimSpace(node.RestID),
				Unavailable:       true,
				UnavailableReason: msg,
				UnavailableKind:   kind,
			}
		}

		// Step 3 (THIRD): TweetWithVisibilityResults (or nested result.tweet wrapper).
		if node.Typename == "TweetWithVisibilityResults" || (!isJSONNull(node.Tweet) && node.Legacy == nil) {
			if hasNonEmptyRawJSON(node.LimActions) && visibilityLabel == "" {
				visibilityLabel = "受限可见"
			}
			if !isJSONNull(node.Tweet) {
				cur = bytes.TrimSpace(node.Tweet)
				continue
			}
			return nil
		}

		// Reached inner Tweet node.
		return c.parseInnerTweetNode(&node, visibilityLabel, depth)
	}
	return nil
}

func (c *Client) parseTweetPreviewDisplay(outer *rawTweetResultNode) *Post {
	if outer == nil || isJSONNull(outer.Tweet) {
		return &Post{
			Unavailable:       true,
			UnavailableReason: "该订阅者专属预览帖暂不可见",
			UnavailableKind:   site.ErrKindForbidden,
		}
	}
	var preview rawTweetResultNode
	if err := decodeJSONUseNumber(outer.Tweet, &preview); err != nil {
		return nil
	}
	id := strings.TrimSpace(preview.RestID)
	if id == "" && preview.Legacy != nil {
		id = strings.TrimSpace(preview.Legacy.IDStr)
	}
	if sf, err := ParseSnowflakeID(id); err == nil {
		id = sf.ID
	}
	if id == "" {
		return nil
	}

	var author AuthorSummary
	if preview.Core != nil && preview.Core.UserResults != nil {
		if parsedAuthor, _, _ := parseRawUserResult(preview.Core.UserResults.Result, c.allowLocalhost); parsedAuthor != nil {
			author = *parsedAuthor
		}
	}

	textRaw := preview.Text
	if textRaw == "" && preview.Legacy != nil {
		textRaw = preview.Legacy.FullText
	}
	createdAtRaw := strings.TrimSpace(preview.CreatedAt)
	if createdAtRaw == "" && preview.Legacy != nil {
		createdAtRaw = strings.TrimSpace(preview.Legacy.CreatedAt)
	}
	createdAt := parseXCreatedAt(createdAtRaw, id)

	p := &Post{
		ID:               id,
		Author:           author,
		CreatedAt:        createdAt,
		CreatedAtRaw:     createdAtRaw,
		Text:             html.UnescapeString(strings.TrimSpace(textRaw)),
		TextRaw:          textRaw,
		IsPreviewDisplay: true,
		VisibilityLabel:  "订阅者专属预览",
		CanonicalURL:     canonicalStatusURL(author.ScreenName, id),
	}
	p.RuleType = computeRuleType(p)
	return p
}

func (c *Client) parseInnerTweetNode(node *rawTweetResultNode, visibilityLabel string, depth int) *Post {
	if node == nil {
		return nil
	}
	leg := node.Legacy
	id := strings.TrimSpace(node.RestID)
	if id == "" && leg != nil {
		id = strings.TrimSpace(leg.IDStr)
	}
	if id == "" {
		return nil
	}
	if sf, err := ParseSnowflakeID(id); err == nil {
		id = sf.ID
	}

	var author AuthorSummary
	if node.Core != nil && node.Core.UserResults != nil {
		if parsedAuthor, _, _ := parseRawUserResult(node.Core.UserResults.Result, c.allowLocalhost); parsedAuthor != nil {
			author = *parsedAuthor
		}
	}
	if author.RestID == "" && leg != nil && leg.UserIDStr != "" {
		if norm, err := site.NormalizeUID(SiteID, leg.UserIDStr); err == nil {
			author.RestID = norm
		} else {
			author.RestID = strings.TrimSpace(leg.UserIDStr)
		}
	}

	var createdAtRaw string
	var fullTextRaw string
	if leg != nil {
		createdAtRaw = strings.TrimSpace(leg.CreatedAt)
		fullTextRaw = leg.FullText
	}
	if createdAtRaw == "" {
		createdAtRaw = strings.TrimSpace(node.CreatedAt)
	}
	if fullTextRaw == "" {
		fullTextRaw = node.Text
	}
	createdAt := parseXCreatedAt(createdAtRaw, id)

	// Recursive note_tweet full-text hydration (x.md §5.2 #3 — applies to main, retweeted, and quoted!).
	isLongText := false
	var noteEntitySet *rawEntitiesSet
	if node.NoteTweet != nil && node.NoteTweet.NoteTweetResults != nil && node.NoteTweet.NoteTweetResults.Result != nil {
		nr := node.NoteTweet.NoteTweetResults.Result
		if strings.TrimSpace(nr.Text) != "" {
			fullTextRaw = nr.Text
			isLongText = true
			noteEntitySet = nr.EntitySet
		}
	}

	// Collect media items from extended_entities.media (fallback entities.media), preserving order.
	var rawMedia []rawMediaEntity
	if leg != nil {
		if leg.ExtendedEntities != nil && len(leg.ExtendedEntities.Media) > 0 {
			rawMedia = leg.ExtendedEntities.Media
		} else if leg.Entities != nil && len(leg.Entities.Media) > 0 {
			rawMedia = leg.Entities.Media
		}
	}
	mediaItems, mediaShortURLs, hasHLSOnly := parseMediaEntities(rawMedia, c.allowLocalhost)

	// Collect URL entities (note_tweet entity_set prioritized, merged with legacy entities).
	urlEntities := collectURLEntities(noteEntitySet, leg)
	cleanText := hydrateTweetText(fullTextRaw, id, urlEntities, mediaShortURLs)

	// Edit history (x.md §5.5).
	editIDs := extractEditTweetIDs(node.EditCtrl)
	isEdited := len(editIDs) > 1

	post := &Post{
		ID:              id,
		Author:          author,
		CreatedAt:       createdAt,
		CreatedAtRaw:    createdAtRaw,
		Text:            cleanText,
		TextRaw:         fullTextRaw,
		IsLongText:      isLongText,
		VisibilityLabel: visibilityLabel,
		EditTweetIDs:    editIDs,
		IsEdited:        isEdited,
		Media:           mediaItems,
		HasHLSOnlyVideo: hasHLSOnly,
		URLs:            urlEntities,
		CanonicalURL:    canonicalStatusURL(author.ScreenName, id),
	}

	if leg != nil {
		post.ConversationID = strings.TrimSpace(leg.ConversationIDStr)
		post.InReplyToStatusID = strings.TrimSpace(leg.InReplyToStatusID)
		post.InReplyToUserID = strings.TrimSpace(leg.InReplyToUserID)
		post.InReplyToScreenName = strings.TrimSpace(leg.InReplyToScreenName)
		if leg.SelfThread != nil {
			post.SelfThreadID = strings.TrimSpace(leg.SelfThread.IDStr)
		}
		post.PossiblySensitive = leg.PossiblySensitive
		if len(leg.WithheldInCountries) > 0 {
			post.WithheldInCountries = append([]string(nil), leg.WithheldInCountries...)
			if post.VisibilityLabel == "" {
				post.VisibilityLabel = "部分地区受限"
			}
		}
		post.Stats = PostStats{
			ReplyCount:    parseJSONInt64(leg.ReplyCount),
			RetweetCount:  parseJSONInt64(leg.RetweetCount),
			LikeCount:     parseJSONInt64(leg.FavoriteCount),
			QuoteCount:    parseJSONInt64(leg.QuoteCount),
			BookmarkCount: parseJSONInt64(leg.BookmarkCount),
		}
	}
	if node.Views != nil {
		post.Stats.ViewCount = parseJSONInt64(node.Views.Count)
	}

	// Parse Retweeted status recursively (graceful degradation when origin unavailable/tombstone/empty!).
	if leg != nil && leg.RetweetedStatusRes != nil {
		post.IsRetweet = true
		rt := c.parseTweetResult(leg.RetweetedStatusRes.Result, depth+1)
		if rt == nil {
			post.OriginUnavailable = true
			post.OriginUnavailableReason = "原帖已被删除或不可见"
		} else if rt.Unavailable {
			post.Retweeted = rt
			post.OriginUnavailable = true
			post.OriginUnavailableReason = firstNonEmpty(rt.UnavailableReason, "原帖已被删除或不可见")
		} else {
			post.Retweeted = rt
			if rt.HasHLSOnlyVideo {
				post.HasHLSOnlyVideo = true
			}
		}
	}

	// Parse Quoted status recursively (graceful degradation when origin unavailable/tombstone/empty!).
	hasQuotedWrap := node.QuotedRes != nil
	if (leg != nil && leg.IsQuoteStatus) || hasQuotedWrap {
		post.IsQuote = true
		if hasQuotedWrap {
			qt := c.parseTweetResult(node.QuotedRes.Result, depth+1)
			if qt == nil {
				post.OriginUnavailable = true
				post.OriginUnavailableReason = "引用的原帖已被删除或不可见"
			} else if qt.Unavailable {
				post.Quoted = qt
				post.OriginUnavailable = true
				post.OriginUnavailableReason = firstNonEmpty(qt.UnavailableReason, "引用的原帖已被删除或不可见")
			} else {
				post.Quoted = qt
				if qt.HasHLSOnlyVideo {
					post.HasHLSOnlyVideo = true
				}
			}
		} else {
			post.OriginUnavailable = true
			post.OriginUnavailableReason = "引用的原帖已被删除或不可见"
		}
	}

	// Safe degradation for X Articles, Polls, Broadcasts/Spaces, and URL Cards (NEVER creates video cache entry).
	post.Poll, post.Card = parseDegradedCardAndPoll(node.Article, node.Card, post.CanonicalURL, urlEntities, c.allowLocalhost)

	post.RuleType = computeRuleType(post)
	return post
}

// ClassifyTweetUnavailableReason maps every documented TweetUnavailable.reason value (x.md §5.5)
// to a single-target site.ErrKind (forbidden or unavailable, NEVER auth_required or circuit-break).
func ClassifyTweetUnavailableReason(reason string) (errKind string, message string) {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "protected":
		return site.ErrKindForbidden, "该账号受保护，无权查看这条帖子"
	case "blocked":
		return site.ErrKindForbidden, "当前账号被作者拉黑，无法查看这条帖子"
	case "countrywithheld", "country_withheld", "withheld":
		return site.ErrKindUnavailable, "该帖子因地区限制暂不可见"
	case "bouncedeleted", "bounce_deleted", "deleted":
		return site.ErrKindUnavailable, "该帖子已被删除"
	case "suspended":
		return site.ErrKindUnavailable, "该帖子作者账号已被封禁"
	case "nsfwloggedout", "nsfw_logged_out":
		return site.ErrKindForbidden, "该帖子包含敏感内容，需要登录查看"
	case "nsfwviewerhasnostatedage", "nsfw_viewer_has_no_stated_age":
		return site.ErrKindForbidden, "该帖子有年龄限制，请在浏览器中设置账号出生日期"
	case "offboarded":
		return site.ErrKindUnavailable, "该帖子作者账号已注销"
	default:
		return site.ErrKindUnavailable, "该帖子暂不可见"
	}
}

func classifyTombstoneKind(text string) string {
	lower := strings.ToLower(text)
	if containsAny(lower, "protected", "blocked", "not authorized", "age-restricted") {
		return site.ErrKindForbidden
	}
	return site.ErrKindUnavailable
}

// computeRuleType classifies X posts for subscription auto-cache tags (DEC-091):
//   - pure retweet -> "forward" (cached only when cache_forward is enabled; skipped by full_cache)
//   - quote post (self-quote or quoting others) -> classified by the author's own media
//     ("video" / "draw" / "word") because the author wrote original commentary/media
//   - single non-GIF video -> "video"
//   - any photo, GIF, or mixed multi-item media -> "draw"
//   - otherwise (text, poll, URL card, article card, live/Spaces card) -> "word"
func computeRuleType(p *Post) string {
	if p == nil {
		return RuleTypeWord
	}
	if p.IsRetweet {
		return RuleTypeForward
	}
	if len(p.Media) == 1 && p.Media[0].Kind == MediaKindVideo && !p.Media[0].GIF {
		return RuleTypeVideo
	}
	if len(p.Media) > 0 {
		return RuleTypeDraw
	}
	return RuleTypeWord
}

// ---------- Media Extraction (Photo orig/large, GIF MP4, Video Bitrate & HLS-Only) ----------

func parseMediaEntities(raw []rawMediaEntity, allowLocalhost bool) (items []MediaItem, shortURLs []string, hasHLSOnly bool) {
	if len(raw) == 0 {
		return nil, nil, false
	}
	items = make([]MediaItem, 0, len(raw))
	shortURLSet := make(map[string]bool)

	for _, rm := range raw {
		if u := strings.TrimSpace(rm.URL); u != "" && !shortURLSet[u] {
			shortURLSet[u] = true
			shortURLs = append(shortURLs, u)
		}

		width, height := extractMediaDimensions(&rm)
		// Only the original (name=orig) is cached. name=large and the bare
		// media URL are recompressed copies; falling back to them would save
		// a lower-quality picture as if it were the original (user request
		// 2026-11-24). A failed original is recorded as missing and retried.
		origImg, _ := buildPhotoURLs(rm.MediaURLHTTPS, allowLocalhost)
		var fallbackImgs []string

		item := MediaItem{
			Index:             len(items) + 1,
			MediaKey:          strings.TrimSpace(rm.MediaKey),
			Width:             width,
			Height:            height,
			AltText:           strings.TrimSpace(rm.ExtAltText),
			PublicPreviewURL:  SafePublicImageURL(origImg),
			ImageURL:          origImg,
			FallbackImageURLs: fallbackImgs,
		}

		switch strings.ToLower(strings.TrimSpace(rm.Type)) {
		case "photo":
			item.Kind = MediaKindPhoto

		case "animated_gif":
			item.Kind = MediaKindGIF
			item.GIF = true
			populateVideoVariants(&item, rm.VideoInfo, true, allowLocalhost)

		case "video":
			item.Kind = MediaKindVideo
			populateVideoVariants(&item, rm.VideoInfo, false, allowLocalhost)
			if item.HLSOnly {
				hasHLSOnly = true
			}

		default:
			item.Kind = MediaKindPhoto
		}

		items = append(items, item)
	}
	return items, shortURLs, hasHLSOnly
}

func extractMediaDimensions(rm *rawMediaEntity) (int, int) {
	if rm == nil {
		return 0, 0
	}
	if rm.OriginalInfo != nil {
		w := int(parseJSONInt64(rm.OriginalInfo.Width))
		h := int(parseJSONInt64(rm.OriginalInfo.Height))
		if w > 0 && h > 0 {
			return w, h
		}
	}
	if rm.Sizes != nil && rm.Sizes.Large != nil {
		w := int(parseJSONInt64(rm.Sizes.Large.W))
		h := int(parseJSONInt64(rm.Sizes.Large.H))
		if w > 0 && h > 0 {
			return w, h
		}
	}
	return 0, 0
}

func populateVideoVariants(item *MediaItem, vi *struct {
	DurationMillis json.RawMessage `json:"duration_millis"`
	AspectRatio    []int           `json:"aspect_ratio"`
	Variants       []struct {
		Bitrate     json.RawMessage `json:"bitrate"`
		ContentType string          `json:"content_type"`
		URL         string          `json:"url"`
	} `json:"variants"`
}, isGIF bool, allowLocalhost bool) {
	if item == nil || vi == nil {
		return
	}
	ms := parseJSONInt64(vi.DurationMillis)
	if ms > 0 {
		item.DurationMS = ms
		sec := int(math.Round(float64(ms) / 1000.0))
		if sec < 1 {
			sec = 1
		}
		item.DurationSec = sec
	}

	type mp4Cand struct {
		variant VideoVariant
		order   int
	}
	var mp4s []mp4Cand
	var hlsURL string

	for idx, rv := range vi.Variants {
		u := normalizeXMediaURL(rv.URL, allowLocalhost)
		if u == "" {
			continue
		}
		ct := strings.TrimSpace(rv.ContentType)
		bitrate := parseJSONInt64(rv.Bitrate)
		w, h := parseVideoDimensionsFromURL(u)

		v := VideoVariant{
			ContentType: ct,
			Bitrate:     bitrate,
			Width:       w,
			Height:      h,
			URL:         u,
		}
		item.Variants = append(item.Variants, v)

		lowerCT := strings.ToLower(ct)
		switch {
		case lowerCT == "video/mp4":
			mp4s = append(mp4s, mp4Cand{variant: v, order: idx})
		case lowerCT == "application/x-mpegurl" || strings.Contains(strings.ToLower(u), ".m3u8"):
			if hlsURL == "" {
				hlsURL = u
			}
		}
	}

	if len(mp4s) > 0 {
		sort.SliceStable(mp4s, func(i, j int) bool {
			if mp4s[i].variant.Bitrate != mp4s[j].variant.Bitrate {
				return mp4s[i].variant.Bitrate > mp4s[j].variant.Bitrate
			}
			areaI := mp4s[i].variant.Width * mp4s[i].variant.Height
			areaJ := mp4s[j].variant.Width * mp4s[j].variant.Height
			if areaI != areaJ {
				return areaI > areaJ
			}
			return mp4s[i].order < mp4s[j].order
		})
		best := mp4s[0].variant
		item.VideoURL = best.URL
		item.VideoBitrate = best.Bitrate
		if best.Width > 0 && best.Height > 0 {
			item.Width = best.Width
			item.Height = best.Height
		}
		for _, rest := range mp4s[1:] {
			if rest.variant.URL != best.URL {
				item.VideoFallbackURLs = append(item.VideoFallbackURLs, rest.variant.URL)
			}
		}
	} else if !isGIF && hlsURL != "" {
		item.HLSOnly = true
		item.HLSOnlyNotice = HLSOnlyNoticeText
		item.HLSURL = hlsURL
	}
}

func parseVideoDimensionsFromURL(rawURL string) (int, int) {
	m := reVideoDimInURL.FindStringSubmatch(rawURL)
	if len(m) != 3 {
		return 0, 0
	}
	w, err1 := strconv.Atoi(m[1])
	h, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}

// buildPhotoURLs normalizes pbs.twimg.com image URLs to ?format=<ext>&name=orig (primary)
// and ?format=<ext>&name=large (fallback) per x.md §5.4 #1.
func buildPhotoURLs(rawURL string, allowLocalhost bool) (origURL, largeURL string) {
	norm := normalizeXMediaURL(rawURL, allowLocalhost)
	if norm == "" {
		return "", ""
	}
	u, err := url.Parse(norm)
	if err != nil || u.Host == "" {
		return norm, norm
	}

	// Strip legacy Twitter size suffix like ":large", ":orig", ":small".
	p := u.Path
	if colon := strings.LastIndexByte(p, ':'); colon > strings.LastIndexByte(p, '/') {
		p = p[:colon]
	}

	q := u.Query()
	format := strings.ToLower(strings.TrimSpace(q.Get("format")))
	if format == "" {
		if dot := strings.LastIndexByte(p, '.'); dot > strings.LastIndexByte(p, '/') {
			ext := strings.ToLower(p[dot+1:])
			switch ext {
			case "jpg", "jpeg":
				format = "jpg"
				p = p[:dot]
			case "png":
				format = "png"
				p = p[:dot]
			case "webp":
				format = "webp"
				p = p[:dot]
			}
		}
	} else if format == "jpeg" {
		format = "jpg"
	}

	makeWithSize := func(size string) string {
		cp := *u
		cp.Path = p
		nq := cp.Query()
		if format != "" {
			nq.Set("format", format)
		}
		nq.Set("name", size)
		cp.RawQuery = nq.Encode()
		return cp.String()
	}

	return makeWithSize("orig"), makeWithSize("large")
}

// upgradeAvatarURL replaces _normal. with _400x400. on profile_image_url_https per x.md §5.4 #1.
func upgradeAvatarURL(raw string, allowLocalhost bool) string {
	norm := normalizeXMediaURL(raw, allowLocalhost)
	if norm == "" {
		return ""
	}
	if strings.Contains(norm, "_normal.") {
		return strings.Replace(norm, "_normal.", "_400x400.", 1)
	}
	return norm
}

// ---------- Text Hydration & Entities ----------

func collectURLEntities(noteSet *rawEntitiesSet, leg *rawTweetLegacy) []URLEntity {
	seen := make(map[string]bool)
	var out []URLEntity
	add := func(list []rawURLEntity) {
		for _, item := range list {
			shortU := strings.TrimSpace(item.URL)
			expU := strings.TrimSpace(item.ExpandedURL)
			dispU := strings.TrimSpace(item.DisplayURL)
			if shortU == "" && expU == "" {
				continue
			}
			key := shortU + "|" + expU
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, URLEntity{
				ShortURL:    shortU,
				ExpandedURL: expU,
				DisplayURL:  dispU,
			})
		}
	}
	if noteSet != nil {
		add(noteSet.URLs)
	}
	if leg != nil && leg.Entities != nil {
		add(leg.Entities.URLs)
	}
	return out
}

func hydrateTweetText(rawText, selfID string, urls []URLEntity, mediaShortURLs []string) string {
	text := rawText

	// 1. Strip trailing / embedded media t.co short links corresponding to extended_entities.media[].url.
	for _, mu := range mediaShortURLs {
		if mu != "" {
			text = strings.ReplaceAll(text, mu, "")
		}
	}

	// 2. Replace entities.urls[] t.co links with expanded_url; strip self-truncation link pointing at selfID.
	for _, ue := range urls {
		if ue.ShortURL == "" {
			continue
		}
		if ue.ExpandedURL == "" {
			continue
		}
		if isSelfStatusURL(ue.ExpandedURL, selfID) {
			text = strings.ReplaceAll(text, ue.ShortURL, "")
			continue
		}
		text = strings.ReplaceAll(text, ue.ShortURL, ue.ExpandedURL)
	}

	// 3. Clean up whitespace left by stripped short URLs while preserving newlines.
	text = html.UnescapeString(text)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		for strings.Contains(line, "  ") {
			line = strings.ReplaceAll(line, "  ", " ")
		}
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isSelfStatusURL(expandedURL, selfID string) bool {
	if selfID == "" || expandedURL == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(expandedURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !allowedXMainHosts[host] {
		return false
	}
	m := reStatusIDSuff.FindStringSubmatch(u.Path)
	return len(m) == 2 && strings.TrimLeft(m[1], "0") == selfID
}

func extractEditTweetIDs(ec *rawEditControl) []string {
	if ec == nil {
		return nil
	}
	src := ec.EditTweetIDs
	if len(src) == 0 && ec.EditControlInitial != nil {
		src = ec.EditControlInitial.EditTweetIDs
	}
	if len(src) == 0 {
		return nil
	}
	out := make([]string, 0, len(src))
	for _, raw := range src {
		if s := strings.TrimSpace(raw); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- Safe Degradation for Polls, Articles, Broadcasts/Spaces, and URL Cards ----------

func parseDegradedCardAndPoll(
	article *rawArticleWrap,
	card *rawCardWrapper,
	canonicalPostURL string,
	urlEntities []URLEntity,
	allowLocalhost bool,
) (*PollSummary, *RichCard) {
	// 1. X Articles card (article.article_results.result).
	if article != nil && article.ArticleResults != nil && article.ArticleResults.Result != nil {
		ar := article.ArticleResults.Result
		title := strings.TrimSpace(ar.Title)
		preview := strings.TrimSpace(ar.PreviewText)
		cover := strings.TrimSpace(ar.CoverMediaImgURL)
		if cover == "" && ar.CoverMedia != nil && ar.CoverMedia.MediaInfo != nil {
			cover = strings.TrimSpace(ar.CoverMedia.MediaInfo.OriginalImgURL)
		}
		artURL := canonicalPostURL
		if id := strings.TrimSpace(ar.RestID); id != "" && reArticleID.MatchString(id) {
			artURL = "https://x.com/i/article/" + id
		}
		if title != "" || preview != "" {
			return nil, &RichCard{
				Kind:        CardKindArticle,
				Label:       "X 文章",
				Title:       title,
				Desc:        preview,
				URL:         SafePublicWebURL(artURL),
				CoverURL:    SafePublicImageURL(normalizeXMediaURL(cover, allowLocalhost)),
				Unsupported: true,
			}
		}
	}

	if card == nil || card.Legacy == nil {
		return nil, nil
	}

	name := strings.TrimSpace(card.Legacy.Name)
	lowerName := strings.ToLower(name)
	strVals := make(map[string]string, len(card.Legacy.BindingValues))
	boolVals := make(map[string]bool)
	imgVals := make(map[string]string)
	for _, bv := range card.Legacy.BindingValues {
		k := strings.TrimSpace(bv.Key)
		if k == "" {
			continue
		}
		if s := strings.TrimSpace(bv.Value.StringValue); s != "" {
			strVals[k] = s
		}
		if strings.EqualFold(bv.Value.Type, "BOOLEAN") {
			boolVals[k] = bv.Value.BoolValue
		}
		if bv.Value.ImageValue != nil && strings.TrimSpace(bv.Value.ImageValue.URL) != "" {
			imgVals[k] = strings.TrimSpace(bv.Value.ImageValue.URL)
		}
	}

	resolveCardURL := func(raw string) string {
		s := strings.TrimSpace(raw)
		for _, ue := range urlEntities {
			if ue.ShortURL == s && ue.ExpandedURL != "" {
				s = ue.ExpandedURL
				break
			}
		}
		if s == "" {
			return SafePublicWebURL(canonicalPostURL)
		}
		return SafePublicWebURL(s)
	}

	// 2. Poll card (poll[2-4]choice_*).
	if rePollCardName.MatchString(lowerName) {
		var options []PollOption
		var total int64
		var descParts []string
		for pos := 1; pos <= 4; pos++ {
			lbl := strVals[fmt.Sprintf("choice%d_label", pos)]
			if lbl == "" {
				continue
			}
			cnt, _ := strconv.ParseInt(strVals[fmt.Sprintf("choice%d_count", pos)], 10, 64)
			options = append(options, PollOption{
				Position: pos,
				Label:    lbl,
				Votes:    cnt,
			})
			total += cnt
			descParts = append(descParts, fmt.Sprintf("%d. %s", pos, lbl))
		}
		if len(options) > 0 {
			poll := &PollSummary{
				Options:    options,
				EndTime:    strVals["end_datetime_utc"],
				Final:      boolVals["counts_are_final"],
				TotalVotes: total,
			}
			return poll, &RichCard{
				Kind:        CardKindVote,
				Label:       "投票",
				Title:       fmt.Sprintf("投票（%d 个选项）", len(options)),
				Desc:        strings.Join(descParts, " / "),
				URL:         SafePublicWebURL(canonicalPostURL),
				Unsupported: true,
			}
		}
	}

	// 3. Broadcast / Spaces card (NEVER creates video cache entry; must display exact LiveUnsupportedText).
	if containsAny(lowerName, "broadcast", "audiospace", "periscope", "live_event") {
		title := firstNonEmpty(strVals["broadcast_title"], strVals["title"], strVals["event_title"], "X 直播 / Spaces")
		cardURL := firstNonEmpty(strVals["broadcast_url"], strVals["card_url"], card.Legacy.URL)
		if spaceID := strVals["id"]; spaceID != "" && containsAny(lowerName, "audiospace") {
			cardURL = "https://x.com/i/spaces/" + spaceID
		} else if bID := strVals["broadcast_id"]; bID != "" && containsAny(lowerName, "broadcast") {
			cardURL = "https://x.com/i/broadcasts/" + bID
		}
		cover := firstNonEmpty(imgVals["broadcast_thumbnail_large"], imgVals["broadcast_thumbnail"], imgVals["thumbnail_image_large"])
		return nil, &RichCard{
			Kind:        CardKindLive,
			Label:       "直播 / Spaces",
			Title:       title,
			Desc:        LiveUnsupportedText,
			URL:         resolveCardURL(cardURL),
			CoverURL:    SafePublicImageURL(normalizeXMediaURL(cover, allowLocalhost)),
			Unsupported: true,
		}
	}

	// 4. Unified card (may wrap broadcast/spaces, article, or web link).
	if strings.Contains(lowerName, "unified_card") {
		if ucRaw := strVals["unified_card"]; ucRaw != "" {
			if rc := parseUnifiedCardJSON(ucRaw, canonicalPostURL, allowLocalhost); rc != nil {
				return nil, rc
			}
		}
	}

	// 5. Article card fallback via card.legacy.name.
	if strings.Contains(lowerName, "article") {
		title := firstNonEmpty(strVals["title"], "X 文章")
		desc := firstNonEmpty(strVals["description"], strVals["preview_text"])
		cover := firstNonEmpty(imgVals["summary_photo_image_large"], imgVals["thumbnail_image_large"], imgVals["photo_image_full_size_original"])
		return nil, &RichCard{
			Kind:        CardKindArticle,
			Label:       "X 文章",
			Title:       title,
			Desc:        desc,
			URL:         resolveCardURL(firstNonEmpty(strVals["card_url"], card.Legacy.URL)),
			CoverURL:    SafePublicImageURL(normalizeXMediaURL(cover, allowLocalhost)),
			Unsupported: true,
		}
	}

	// 6. Standard URL / summary / summary_large_image / player card.
	title := firstNonEmpty(strVals["title"], strVals["vanity_url"], strVals["domain"])
	desc := strVals["description"]
	cover := firstNonEmpty(
		imgVals["summary_photo_image_large"],
		imgVals["summary_photo_image_original"],
		imgVals["photo_image_full_size_original"],
		imgVals["thumbnail_image_large"],
		imgVals["player_image_large"],
	)
	cardURL := resolveCardURL(firstNonEmpty(strVals["card_url"], card.Legacy.URL))
	if title == "" && desc == "" && cardURL == "" {
		return nil, nil
	}
	return nil, &RichCard{
		Kind:        CardKindWebpage,
		Label:       "网页链接",
		Title:       title,
		Desc:        desc,
		URL:         cardURL,
		CoverURL:    SafePublicImageURL(normalizeXMediaURL(cover, allowLocalhost)),
		Unsupported: true,
	}
}

func parseUnifiedCardJSON(raw string, canonicalPostURL string, allowLocalhost bool) *RichCard {
	var uc struct {
		Type               string `json:"type"`
		DestinationObjects map[string]struct {
			Type string `json:"type"`
			Data struct {
				URLData *struct {
					URL       string `json:"url"`
					VanityURL string `json:"vanity"`
				} `json:"url_data"`
			} `json:"data"`
		} `json:"destination_objects"`
		ComponentObjects map[string]struct {
			Type string `json:"type"`
			Data struct {
				Title *struct {
					Content string `json:"content"`
				} `json:"title"`
				Subtitle *struct {
					Content string `json:"content"`
				} `json:"subtitle"`
			} `json:"data"`
		} `json:"component_objects"`
	}
	if err := decodeJSONUseNumber([]byte(raw), &uc); err != nil {
		return nil
	}
	var title, desc, link string
	for _, comp := range uc.ComponentObjects {
		if title == "" && comp.Data.Title != nil {
			title = strings.TrimSpace(comp.Data.Title.Content)
		}
		if desc == "" && comp.Data.Subtitle != nil {
			desc = strings.TrimSpace(comp.Data.Subtitle.Content)
		}
	}
	for _, dest := range uc.DestinationObjects {
		if link == "" && dest.Data.URLData != nil {
			link = strings.TrimSpace(dest.Data.URLData.URL)
		}
	}
	if link == "" {
		link = canonicalPostURL
	}
	lowerType := strings.ToLower(uc.Type)
	if containsAny(lowerType, "broadcast", "audiospace", "live") {
		return &RichCard{
			Kind:        CardKindLive,
			Label:       "直播 / Spaces",
			Title:       firstNonEmpty(title, "X 直播 / Spaces"),
			Desc:        LiveUnsupportedText,
			URL:         SafePublicWebURL(link),
			Unsupported: true,
		}
	}
	if title == "" && desc == "" {
		return nil
	}
	return &RichCard{
		Kind:        CardKindWebpage,
		Label:       "网页链接",
		Title:       title,
		Desc:        desc,
		URL:         SafePublicWebURL(link),
		Unsupported: true,
	}
}

// ---------- Timestamp & Public URL Sanitization Helpers ----------

const xTimeLayout = "Mon Jan 02 15:04:05 -0700 2006"

func parseXCreatedAt(raw, statusID string) int64 {
	s := strings.TrimSpace(raw)
	if s != "" {
		if t, err := time.Parse(xTimeLayout, s); err == nil {
			return t.UTC().Unix()
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC().Unix()
		}
	}
	if sf, err := ParseSnowflakeID(statusID); err == nil && sf.IsSnowflake {
		return sf.Timestamp.Unix()
	}
	return 0
}

// HasSignedQueryMarker reports whether raw contains ephemeral signature / auth query markers
// that must never be persisted to snapshots, manifests, or logs (NFR-SEC-005).
func HasSignedQueryMarker(raw string) bool {
	lower := strings.ToLower(raw)
	return containsAny(lower,
		"x-amz-signature",
		"x-amz-credential",
		"x-amz-security-token",
		"signature=",
		"signature%3d",
		"sig=",
		"sig%3d",
		"expires=",
		"expires%3d",
		"policy=",
		"key-pair-id=",
		"token=",
		"token%3d",
		"auth_token=",
		"ct0=",
	)
}

// SafePublicImageURL returns raw only when it is an unsigned public pbs.twimg.com image URL
// safe to store in updates.db snapshots (never returns video.twimg.com or signed URLs).
func SafePublicImageURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || HasSignedQueryMarker(s) {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "pbs.twimg.com" {
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	return u.String()
}

// SafePublicWebURL returns raw only when it is a safe public HTTP(S) web page URL without
// credentials or signed CDN query tokens.
func SafePublicWebURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || HasSignedQueryMarker(s) {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "video.twimg.com" || strings.HasSuffix(host, ".video.twimg.com") {
		return ""
	}
	return u.String()
}

func isPromotedOrNoiseEntryID(entryID string) bool {
	lower := strings.ToLower(strings.TrimSpace(entryID))
	if lower == "" {
		return false
	}
	for _, pref := range []string{
		"promoted-",
		"promotedtweet-",
		"who-to-follow-",
		"who-to-subscribe-",
		"topics-module-",
		"label-",
		"messageprompt-",
		"tombstone-",
		"cursor-",
	} {
		if strings.HasPrefix(lower, pref) {
			return true
		}
	}
	return false
}

func hasNonEmptyRawJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte("{}")) || bytes.Equal(t, []byte("[]")) || bytes.Equal(t, []byte("false")) {
		return false
	}
	return true
}
