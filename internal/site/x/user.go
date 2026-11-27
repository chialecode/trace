package x

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

const (
	// DefaultTimelinePageCount is the standard page size requested from UserTweets/Following/Bookmarks/Likes.
	DefaultTimelinePageCount = 20

	// VisibleTimelineLimitHint is X's approximate ~3,200-post UserTweets visibility cap (x.md §7.3).
	VisibleTimelineLimitHint = 3200

	// VisibleTimelineLimitText is the user-facing completion notice when X's visible limit is reached (x.md §15.1 #12).
	VisibleTimelineLimitText = "已到达 X 时间线可见上限"

	// Profile tab identifiers for X user profile browsing (DEC-092, DEC-093).
	ProfileTabPosts      = "posts"
	ProfileTabReplies    = "replies"
	ProfileTabMedia      = "media"
	ProfileTabHighlights = "highlights"
	ProfileTabArticles   = "articles"
)

// NormalizeProfileTab canonicalizes an X profile tab identifier, defaulting empty
// input to ProfileTabPosts. Returns ("", false) for unrecognized tab values.
func NormalizeProfileTab(tab string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "", ProfileTabPosts, "tweets":
		return ProfileTabPosts, true
	case ProfileTabReplies, "with_replies":
		return ProfileTabReplies, true
	case ProfileTabMedia:
		return ProfileTabMedia, true
	case ProfileTabHighlights:
		return ProfileTabHighlights, true
	case ProfileTabArticles, "article":
		return ProfileTabArticles, true
	default:
		return "", false
	}
}

// ProfileTabOperation maps a normalized X profile tab to its read-only GraphQL operation name.
func ProfileTabOperation(tab string) string {
	norm, ok := NormalizeProfileTab(tab)
	if !ok {
		return ""
	}
	switch norm {
	case ProfileTabReplies:
		return OpUserTweetsAndReplies
	case ProfileTabMedia:
		return OpUserMedia
	case ProfileTabHighlights:
		return OpUserHighlightsTweets
	case ProfileTabArticles:
		return OpUserArticlesTweets
	default:
		return OpUserTweets
	}
}

// UserProfile holds an X user's normalized profile from UserByRestId or UserByScreenName.
type UserProfile struct {
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
}

// UserTweetsRequest configures one paginated call to an X user profile timeline
// (UserTweets, UserTweetsAndReplies, UserMedia, or UserHighlightsTweets).
type UserTweetsRequest struct {
	RestID            string
	Tab               string // "", "posts", "replies", "media", "highlights" (defaults to "posts")
	Cursor            string
	Count             int
	SeenIDs           map[string]bool
	KnownProfile      *UserProfile
	CrossCheckProfile bool // when true and page 1 returns empty without KnownProfile, fetches UserByRestId
	TotalFetched      int  // optional cumulative count across prior pages (for visible-limit detection)
}

// UserTweetsPage holds one validated page of posts from an X user profile timeline.
type UserTweetsPage struct {
	Tab                 string         `json:"tab,omitempty"`
	Posts               []Post         `json:"posts"`
	NextCursor          string         `json:"nextCursor,omitempty"`
	TopCursor           string         `json:"topCursor,omitempty"`
	HasMore             bool           `json:"hasMore"`
	CursorStagnated     bool           `json:"cursorStagnated,omitempty"`
	ReachedVisibleLimit bool           `json:"reachedVisibleLimit,omitempty"`
	BottomTipsVisible   bool           `json:"bottomTipsVisible,omitempty"`
	BottomTipsText      string         `json:"bottomTipsText,omitempty"`
	UncertainEmpty      bool           `json:"uncertainEmpty,omitempty"`
	UncertainReason     string         `json:"uncertainReason,omitempty"`
	Author              *AuthorSummary `json:"author,omitempty"`
}

// GetUserByRestId fetches an X user's profile by permanent numeric rest_id via GraphQL UserByRestId.
func (c *Client) GetUserByRestId(ctx context.Context, restID string) (*UserProfile, error) {
	normID, err := site.NormalizeUID(SiteID, restID)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 用户 rest_id",
		}
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpUserByRestId,
		variables: map[string]any{
			"userId":                   normID,
			"withSafetyModeUserFields": true,
		},
		referer: "https://x.com/i/user/" + normID,
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpUserByRestId)
	prof, pErr := c.parseUserProfileData(ctx, gqlResp.data, endpoint, normID)
	if pErr != nil {
		return nil, pErr
	}
	return prof, nil
}

// GetUserByScreenName resolves a mutable @handle into a UserProfile with permanent rest_id
// via GraphQL UserByScreenName (1 API request).
func (c *Client) GetUserByScreenName(ctx context.Context, screenName string) (*UserProfile, error) {
	return c.getUserByScreenNameWithCandidate(ctx, screenName, nil)
}

func (c *Client) getUserByScreenNameWithCandidate(ctx context.Context, screenName string, candidate *candidateCookieCarrier) (*UserProfile, error) {
	handle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(screenName), "@"))
	if !reValidXScreenName.MatchString(handle) {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 用户名（screen_name）",
		}
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpUserByScreenName,
		variables: map[string]any{
			"screen_name":              handle,
			"withSafetyModeUserFields": true,
		},
		referer:         "https://x.com/" + handle,
		candidate:       candidate,
		skipStoreExpire: candidate != nil,
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpUserByScreenName)
	prof, pErr := c.parseUserProfileData(ctx, gqlResp.data, endpoint, "")
	if pErr != nil {
		return nil, pErr
	}
	return prof, nil
}

func (c *Client) parseUserProfileData(ctx context.Context, rawData json.RawMessage, endpoint, expectedRestID string) (*UserProfile, error) {
	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 用户资料响应失败",
			Err:      err,
		}
	}

	rawUser, hasUser := dataMap["user"]
	if !hasUser {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 用户资料响应缺少 user 对象，疑似触发限流空壳",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	if isJSONNull(rawUser) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 用户不存在",
		}
	}

	var userWrap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawUser, &userWrap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 用户资料 user 结构失败",
			Err:      err,
		}
	}
	if len(userWrap) == 0 {
		// x.md §7.3 row 2: {"data":{"user":{}}} -> rate_limited fake empty shell!
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 返回空壳用户响应，疑似触发限流，已停止请求",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	rawResult, hasResult := userWrap["result"]
	if !hasResult {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 用户资料响应缺少 result 字段，疑似触发限流空壳",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	if isJSONNull(rawResult) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 用户不存在",
		}
	}

	_, prof, uErr := parseRawUserResult(rawResult, c.allowLocalhost)
	if uErr != nil {
		uErr.Endpoint = endpoint
		return nil, uErr
	}
	if prof == nil || prof.RestID == "" {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "X 用户资料缺少有效的 rest_id",
		}
	}
	if expectedRestID != "" && prof.RestID != expectedRestID {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  fmt.Sprintf("X 返回的用户 rest_id 与请求目标不一致（期望 %s，实际 %s）", expectedRestID, prof.RestID),
		}
	}
	return prof, nil
}

// GetUserTweets fetches one page of a creator's "posts" timeline via GraphQL UserTweets, implementing:
//   - all 3 timeline response paths (timeline_v2, timeline, result.timeline_v2)
//   - TimelinePinEntry pinned post placed first with Pinned: true
//   - Bottom cursor pagination, cross-page dedup, cursor stagnation stop, visible-limit stop
//   - stripping promoted entries, recommendation modules, non-target-author entries, and replies
//   - EVERY row of docs/research/x.md §7.3 truth table
func (c *Client) GetUserTweets(ctx context.Context, req UserTweetsRequest) (*UserTweetsPage, error) {
	req.Tab = ProfileTabPosts
	return c.GetUserTabTweets(ctx, req)
}

// GetUserTabTweets fetches one page of a creator's profile tab timeline
// (posts -> UserTweets, replies -> UserTweetsAndReplies, media -> UserMedia,
// highlights -> UserHighlightsTweets) under X's strict serial pacing and per-operation
// x-rate-limit-remaining <= 1 proactive brake (DEC-092).
func (c *Client) GetUserTabTweets(ctx context.Context, req UserTweetsRequest) (*UserTweetsPage, error) {
	normTab, ok := NormalizeProfileTab(req.Tab)
	if !ok {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: fmt.Sprintf("不支持的 X 用户页标签: %s", strings.TrimSpace(req.Tab)),
		}
	}
	normID, err := site.NormalizeUID(SiteID, req.RestID)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 用户 rest_id",
		}
	}
	count := req.Count
	if count <= 0 {
		count = DefaultTimelinePageCount
	}
	reqCursor := strings.TrimSpace(req.Cursor)
	opName := ProfileTabOperation(normTab)

	var vars map[string]any
	var toggles map[string]bool
	switch normTab {
	case ProfileTabReplies:
		vars = map[string]any{
			"userId":                 normID,
			"count":                  count,
			"includePromotedContent": false,
			"withCommunity":          true,
			"withVoice":              true,
			"withV2Timeline":         true,
		}
	case ProfileTabMedia:
		vars = map[string]any{
			"userId":                 normID,
			"count":                  count,
			"includePromotedContent": false,
			"withClientEventToken":   false,
			"withBirdwatchNotes":     false,
			"withVoice":              true,
			"withV2Timeline":         true,
		}
	case ProfileTabHighlights:
		vars = map[string]any{
			"userId":                 normID,
			"count":                  count,
			"includePromotedContent": false,
			"withBirdwatchNotes":     true,
			"withVoice":              true,
			"withV2Timeline":         true,
		}
	case ProfileTabArticles:
		vars = map[string]any{
			"userId":                 normID,
			"count":                  count,
			"includePromotedContent": false,
			"withVoice":              true,
			"withV2Timeline":         true,
		}
		toggles = articleFieldToggles()
	default:
		vars = map[string]any{
			"userId":                                 normID,
			"count":                                  count,
			"includePromotedContent":                 false,
			"withQuickPromoteEligibilityTweetFields": false,
			"withVoice":                              true,
			"withV2Timeline":                         true,
		}
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation:    opName,
		variables:    vars,
		fieldToggles: toggles,
		referer:      "https://x.com/i/user/" + normID,
	})
	if err != nil {
		return nil, err
	}

	return c.parseUserTimelineData(ctx, gqlResp.data, normID, normTab, opName, req)
}

func (c *Client) parseUserTimelineData(ctx context.Context, rawData json.RawMessage, normID, tab, opName string, req UserTweetsRequest) (*UserTweetsPage, error) {
	if opName == "" {
		opName = OpUserTweets
	}
	if tab == "" {
		tab = ProfileTabPosts
	}
	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + opName)

	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  fmt.Sprintf("解析 X %s 响应失败", opName),
			Err:      err,
		}
	}

	rawUser, hasUser := dataMap["user"]
	if !hasUser {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 时间线响应缺少 user 字段，疑似触发限流空壳",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	// Truth table row 3: data.user == null -> unavailable (single target, no circuit break).
	if isJSONNull(rawUser) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 用户不存在",
		}
	}

	var userWrap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawUser, &userWrap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 时间线 user 结构失败",
			Err:      err,
		}
	}
	// Truth table row 2: {"data":{"user":{}}} -> rate_limited fake empty shell (NOT 0 posts!).
	if len(userWrap) == 0 {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 返回空壳时间线响应，疑似触发限流，已停止请求",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	rawResult, hasResult := userWrap["result"]
	if !hasResult {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 时间线响应缺少 user.result，疑似触发限流空壳",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	// Truth table row 3: user.result == null -> unavailable.
	if isJSONNull(rawResult) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 用户不存在",
		}
	}

	var resMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawResult, &resMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 时间线 user.result 结构失败",
			Err:      err,
		}
	}
	if len(resMap) == 0 {
		// Truth table row 2: {"data":{"user":{"result":{}}}} -> fake empty shell.
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 时间线 user.result 为空壳，疑似触发限流，已停止请求",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	typename := parseJSONString(resMap["__typename"])
	// Truth table row 4: UserUnavailable (Suspended / Blocked / Offboarded).
	if typename == "UserUnavailable" {
		reason := parseJSONString(resMap["reason"])
		return nil, classifyUserUnavailableError(reason, endpoint)
	}

	// Check protected flag on user.result (Truth table row 5).
	isProtected := extractProtectedFlag(resMap)
	var inlineAuthor *AuthorSummary
	if a, _, _ := parseRawUserResult(rawResult, c.allowLocalhost); a != nil && a.RestID != "" {
		inlineAuthor = a
	}

	// Extract timeline instructions across ALL 3 supported X paths (x.md §7.1):
	//   1. result.timeline_v2.timeline.instructions
	//   2. result.timeline.timeline.instructions
	//   3. result.timeline.timeline_v2.instructions
	instructions, hasTimeline, parseErr := extractTimelineInstructions(resMap)
	if parseErr != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 时间线 instructions 结构失败",
			Err:      parseErr,
		}
	}
	if !hasTimeline {
		// Truth table row 5: protected user without accessible timeline -> forbidden (no circuit break).
		if isProtected || (req.KnownProfile != nil && req.KnownProfile.Protected) {
			return nil, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindForbidden,
				Endpoint: endpoint,
				Message:  "该 X 账号受保护，未获准关注前无法查看其帖子",
			}
		}
		// Truth table row 2: missing timeline/timeline_v2 object -> rate_limited fake empty shell!
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 时间线响应缺少 timeline 载荷，疑似触发限流空壳",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	seen := req.SeenIDs
	if seen == nil {
		seen = make(map[string]bool)
	}

	var (
		pinnedPosts   []Post
		regularPosts  []Post
		rawTweetCount int
		nextCursor    string
		topCursor     string
		pageAuthor    = inlineAuthor
	)

	acceptTweet := func(p *Post, pinned bool) {
		if p == nil || p.Unavailable || p.ID == "" {
			return
		}
		rawTweetCount++
		// Must belong to target creator (outer retweet has target creator as Author.RestID).
		// On UserTweetsAndReplies, profile-conversation-* VerticalConversation modules contain
		// context parent tweets from other users; strictly require legacy.user_id_str/Author.RestID == normID.
		if tab == ProfileTabPosts {
			if p.Author.RestID != "" && p.Author.RestID != normID {
				return
			}
			// x.md §15.1 #11: UserTweets ("posts" tab & subscriptions) filters out replies.
			if !p.IsRetweet && p.InReplyToStatusID != "" {
				return
			}
		} else {
			if p.Author.RestID != normID {
				return
			}
		}
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		if pinned {
			p.Pinned = true
			pinnedPosts = append(pinnedPosts, *p)
		} else {
			regularPosts = append(regularPosts, *p)
		}
		if pageAuthor == nil && p.Author.RestID == normID {
			cp := p.Author
			pageAuthor = &cp
		}
	}

	// Pass 1: TimelinePinEntry MUST be placed first (and recorded in seen before TimelineAddEntries!).
	for _, instr := range instructions {
		if instr.instructionType() != "TimelinePinEntry" || instr.Entry == nil {
			continue
		}
		entry := instr.Entry
		if isPromotedOrNoiseEntryID(entry.EntryID) {
			continue
		}
		ic := entry.Content.ItemContent
		if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.TweetResults == nil {
			continue
		}
		p := c.parseTweetResult(ic.TweetResults.Result, 0)
		acceptTweet(p, true)
	}

	// Pass 2: TimelineAddEntries / TimelineReplaceEntry / TimelineAddToModule.
	for _, instr := range instructions {
		switch instr.instructionType() {
		case "TimelineReplaceEntry":
			if instr.Entry != nil {
				extractCursorsFromEntry(instr.Entry, &topCursor, &nextCursor)
			}

		case "TimelineAddEntries":
			for i := range instr.Entries {
				entry := &instr.Entries[i]
				if extractCursorsFromEntry(entry, &topCursor, &nextCursor) {
					continue
				}
				if isPromotedOrNoiseEntryID(entry.EntryID) {
					continue
				}

				switch entry.Content.contentType() {
				case "TimelineTimelineItem":
					ic := entry.Content.ItemContent
					if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.TweetResults == nil {
						continue
					}
					p := c.parseTweetResult(ic.TweetResults.Result, 0)
					acceptTweet(p, false)

				case "TimelineTimelineModule":
					if isRecommendationModule(entry.EntryID, entry.Content.DisplayType) {
						continue
					}
					for _, modItem := range entry.Content.Items {
						if isPromotedOrNoiseEntryID(modItem.EntryID) {
							continue
						}
						ic := modItem.Item.ItemContent
						if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.TweetResults == nil {
							continue
						}
						p := c.parseTweetResult(ic.TweetResults.Result, 0)
						acceptTweet(p, false)
					}
				}
			}

		case "TimelineAddToModule":
			for _, modItem := range instr.ModuleItems {
				if isPromotedOrNoiseEntryID(modItem.EntryID) {
					continue
				}
				ic := modItem.Item.ItemContent
				if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.TweetResults == nil {
					continue
				}
				p := c.parseTweetResult(ic.TweetResults.Result, 0)
				acceptTweet(p, false)
			}
		}
	}

	posts := make([]Post, 0, len(pinnedPosts)+len(regularPosts))
	posts = append(posts, pinnedPosts...)
	posts = append(posts, regularPosts...)

	reqCursor := strings.TrimSpace(req.Cursor)
	cursorStagnated := reqCursor != "" && nextCursor != "" && nextCursor == reqCursor

	// Protected account with 0 accessible posts on page 1 -> Truth table row 5 (forbidden).
	if reqCursor == "" && len(posts) == 0 && rawTweetCount == 0 && (isProtected || (req.KnownProfile != nil && req.KnownProfile.Protected)) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindForbidden,
			Endpoint: endpoint,
			Message:  "该 X 账号受保护，未获准关注前无法查看其帖子",
		}
	}

	// Page 1 completely empty (valid instructions, 0 tweets): check Truth table row 1 vs uncertain empty.
	if reqCursor == "" && len(posts) == 0 && rawTweetCount == 0 {
		if tab == ProfileTabMedia || tab == ProfileTabHighlights || tab == ProfileTabArticles {
			if req.KnownProfile != nil && req.KnownProfile.Protected {
				return nil, &site.Error{
					Site:     SiteID,
					Kind:     site.ErrKindForbidden,
					Endpoint: endpoint,
					Message:  "该 X 账号受保护，未获准关注前无法查看其帖子",
				}
			}
			if pageAuthor == nil && req.KnownProfile != nil {
				pageAuthor = profileToAuthorSummary(req.KnownProfile)
			}
			return &UserTweetsPage{
				Tab:       tab,
				Posts:     []Post{},
				TopCursor: topCursor,
				HasMore:   false,
				Author:    pageAuthor,
			}, nil
		}
		prof := req.KnownProfile
		if prof == nil && req.CrossCheckProfile {
			fetched, pErr := c.GetUserByRestId(ctx, normID)
			if pErr != nil {
				return nil, pErr
			}
			prof = fetched
		}
		if prof != nil {
			if prof.Protected {
				return nil, &site.Error{
					Site:     SiteID,
					Kind:     site.ErrKindForbidden,
					Endpoint: endpoint,
					Message:  "该 X 账号受保护，未获准关注前无法查看其帖子",
				}
			}
			if pageAuthor == nil {
				pageAuthor = profileToAuthorSummary(prof)
			}
			if prof.StatusesCount == 0 {
				return &UserTweetsPage{
					Tab:       tab,
					Posts:     []Post{},
					TopCursor: topCursor,
					HasMore:   false,
					Author:    pageAuthor,
				}, nil
			}
			return &UserTweetsPage{
				Tab:             tab,
				Posts:           []Post{},
				TopCursor:       topCursor,
				HasMore:         false,
				UncertainEmpty:  true,
				UncertainReason: "账号帖子总数大于 0 但第 1 页返回空时间线，本轮结果记为不完整",
				Author:          pageAuthor,
			}, nil
		}
		// Known statuses_count on inlineAuthor == 0 -> genuine 0 posts; > 0 -> uncertain.
		if pageAuthor != nil && pageAuthor.StatusesCount > 0 {
			return &UserTweetsPage{
				Tab:             tab,
				Posts:           []Post{},
				TopCursor:       topCursor,
				HasMore:         false,
				UncertainEmpty:  true,
				UncertainReason: "账号帖子总数大于 0 但第 1 页返回空时间线，本轮结果记为不完整",
				Author:          pageAuthor,
			}, nil
		}
		return &UserTweetsPage{
			Tab:       tab,
			Posts:     []Post{},
			TopCursor: topCursor,
			HasMore:   false,
			Author:    pageAuthor,
		}, nil
	}

	// Determine pagination continuation, cursor stagnation stop, and visible-limit completion.
	hasMore := nextCursor != "" && !cursorStagnated
	reachedVisibleLimit := false

	knownTabTotal := func() int {
		switch tab {
		case ProfileTabMedia:
			if req.KnownProfile != nil {
				return req.KnownProfile.MediaCount
			}
			if pageAuthor != nil {
				return pageAuthor.MediaCount
			}
			return 0
		case ProfileTabHighlights, ProfileTabArticles:
			return 0
		default:
			if req.KnownProfile != nil {
				return req.KnownProfile.StatusesCount
			}
			if pageAuthor != nil {
				return pageAuthor.StatusesCount
			}
			return 0
		}
	}()

	// Subsequent page with 0 raw tweet entries or 0 new posts -> end of visible timeline!
	if reqCursor != "" && (rawTweetCount == 0 || len(posts) == 0) {
		hasMore = false
		nextCursor = ""
		totalSeen := req.TotalFetched + len(posts)
		if len(seen) > totalSeen {
			totalSeen = len(seen)
		}
		if cursorStagnated ||
			totalSeen >= VisibleTimelineLimitHint ||
			(knownTabTotal > 0 && (totalSeen == 0 || knownTabTotal > totalSeen)) {
			reachedVisibleLimit = true
		}
	} else if cursorStagnated {
		// Cursor repeated on a non-empty subsequent page -> stop immediately without losing posts!
		hasMore = false
		nextCursor = ""
		totalSeen := req.TotalFetched + len(posts)
		if len(seen) > totalSeen {
			totalSeen = len(seen)
		}
		if totalSeen >= VisibleTimelineLimitHint || (knownTabTotal > 0 && knownTabTotal > totalSeen) {
			reachedVisibleLimit = true
		}
	} else if !hasMore && req.TotalFetched+len(posts) >= VisibleTimelineLimitHint {
		reachedVisibleLimit = true
	}

	page := &UserTweetsPage{
		Tab:                 tab,
		Posts:               posts,
		NextCursor:          nextCursor,
		TopCursor:           topCursor,
		HasMore:             hasMore,
		CursorStagnated:     cursorStagnated,
		ReachedVisibleLimit: reachedVisibleLimit,
		Author:              pageAuthor,
	}
	if reachedVisibleLimit {
		page.BottomTipsVisible = true
		page.BottomTipsText = VisibleTimelineLimitText
	}
	return page, nil
}

// ---------- User Result & Timeline Helpers ----------

type rawUserResult struct {
	Typename       string `json:"__typename"`
	Reason         string `json:"reason"`
	RestID         string `json:"rest_id"`
	IsBlueVerified bool   `json:"is_blue_verified"`
	// New 2025/2026 X schema paths (preferred over legacy when present per x.md §5.2 #1):
	Core *struct {
		Name       string `json:"name"`
		ScreenName string `json:"screen_name"`
	} `json:"core"`
	Avatar *struct {
		ImageURL string `json:"image_url"`
	} `json:"avatar"`
	ProfileBio *struct {
		Description string `json:"description"`
	} `json:"profile_bio"`
	Privacy *struct {
		Protected bool `json:"protected"`
	} `json:"privacy"`
	// Classic legacy path (fallback):
	Legacy *struct {
		IDStr                string          `json:"id_str"`
		Name                 string          `json:"name"`
		ScreenName           string          `json:"screen_name"`
		Description          string          `json:"description"`
		ProfileImageURLHTTPS string          `json:"profile_image_url_https"`
		ProfileBannerURL     string          `json:"profile_banner_url"`
		Verified             bool            `json:"verified"`
		Protected            bool            `json:"protected"`
		FollowersCount       json.RawMessage `json:"followers_count"`
		FriendsCount         json.RawMessage `json:"friends_count"`
		StatusesCount        json.RawMessage `json:"statuses_count"`
		MediaCount           json.RawMessage `json:"media_count"`
		PinnedTweetIDsStr    []string        `json:"pinned_tweet_ids_str"`
	} `json:"legacy"`
}

// parseRawUserResult parses an X user result node supporting BOTH new userResult.core
// (2025/2026) and userResult.legacy paths (x.md §5.2 #1 & §7.1).
func parseRawUserResult(raw json.RawMessage, allowLocalhost bool) (*AuthorSummary, *UserProfile, *site.Error) {
	if isJSONNull(raw) {
		return nil, nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindUnavailable,
			Message: "该 X 用户不存在",
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil, nil, nil
	}

	var u rawUserResult
	if err := decodeJSONUseNumber(trimmed, &u); err != nil {
		return nil, nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "解析 X 用户结构失败",
			Err:     err,
		}
	}
	if u.Typename == "UserUnavailable" {
		return nil, nil, classifyUserUnavailableError(u.Reason, "")
	}

	rawID := strings.TrimSpace(u.RestID)
	if rawID == "" && u.Legacy != nil {
		rawID = strings.TrimSpace(u.Legacy.IDStr)
	}
	normID := ""
	if rawID != "" {
		if n, err := site.NormalizeUID(SiteID, rawID); err == nil {
			normID = n
		} else {
			normID = rawID
		}
	}

	var name, screenName, desc, avatarRaw, bannerRaw string
	var verified, protected bool
	var followers, friends int64
	var statuses, media int
	var pinned []string

	// Prefer new 2025/2026 userResult.core / avatar / profile_bio / privacy paths first.
	if u.Core != nil {
		name = strings.TrimSpace(u.Core.Name)
		screenName = strings.TrimSpace(u.Core.ScreenName)
	}
	if u.Avatar != nil {
		avatarRaw = strings.TrimSpace(u.Avatar.ImageURL)
	}
	if u.ProfileBio != nil {
		desc = strings.TrimSpace(u.ProfileBio.Description)
	}
	if u.Privacy != nil && u.Privacy.Protected {
		protected = true
	}

	// Fallback / supplement from userResult.legacy.
	if u.Legacy != nil {
		if name == "" {
			name = strings.TrimSpace(u.Legacy.Name)
		}
		if screenName == "" {
			screenName = strings.TrimSpace(u.Legacy.ScreenName)
		}
		if desc == "" {
			desc = strings.TrimSpace(u.Legacy.Description)
		}
		if avatarRaw == "" {
			avatarRaw = strings.TrimSpace(u.Legacy.ProfileImageURLHTTPS)
		}
		bannerRaw = strings.TrimSpace(u.Legacy.ProfileBannerURL)
		verified = u.Legacy.Verified
		if u.Legacy.Protected {
			protected = true
		}
		followers = parseJSONInt64(u.Legacy.FollowersCount)
		friends = parseJSONInt64(u.Legacy.FriendsCount)
		statuses = int(parseJSONInt64(u.Legacy.StatusesCount))
		media = int(parseJSONInt64(u.Legacy.MediaCount))
		if len(u.Legacy.PinnedTweetIDsStr) > 0 {
			pinned = append([]string(nil), u.Legacy.PinnedTweetIDsStr...)
		}
	}

	isBlue := u.IsBlueVerified
	combinedVerified := verified || isBlue
	avatarURL := upgradeAvatarURL(avatarRaw, allowLocalhost)
	bannerURL := normalizeXMediaURL(bannerRaw, allowLocalhost)

	if normID == "" && screenName == "" {
		return nil, nil, nil
	}

	summary := &AuthorSummary{
		RestID:         normID,
		ScreenName:     screenName,
		Name:           firstNonEmpty(name, screenName),
		AvatarURL:      avatarURL,
		Description:    desc,
		Verified:       combinedVerified,
		IsBlueVerified: isBlue,
		Protected:      protected,
		StatusesCount:  statuses,
		MediaCount:     media,
		FollowersCount: followers,
		FriendsCount:   friends,
	}
	prof := &UserProfile{
		RestID:         normID,
		ScreenName:     screenName,
		Name:           firstNonEmpty(name, screenName),
		Description:    desc,
		AvatarURL:      avatarURL,
		BannerURL:      bannerURL,
		Verified:       combinedVerified,
		IsBlueVerified: isBlue,
		FollowersCount: followers,
		FriendsCount:   friends,
		StatusesCount:  statuses,
		MediaCount:     media,
		Protected:      protected,
		PinnedTweetIDs: pinned,
		CanonicalURL:   "https://x.com/i/user/" + normID,
	}
	return summary, prof, nil
}

func classifyUserUnavailableError(reason, endpoint string) *site.Error {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "blocked":
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindForbidden,
			Endpoint: endpoint,
			Message:  "当前账号被该 X 用户拉黑，无法查看其资料或帖子",
		}
	case "suspended":
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 账号已被封禁",
		}
	case "offboarded":
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 账号已注销",
		}
	default:
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 账号当前不可用",
		}
	}
}

func extractProtectedFlag(resMap map[string]json.RawMessage) bool {
	if rawPriv, ok := resMap["privacy"]; ok && !isJSONNull(rawPriv) {
		var priv struct {
			Protected bool `json:"protected"`
		}
		if decodeJSONUseNumber(rawPriv, &priv) == nil && priv.Protected {
			return true
		}
	}
	if rawLeg, ok := resMap["legacy"]; ok && !isJSONNull(rawLeg) {
		var leg struct {
			Protected bool `json:"protected"`
		}
		if decodeJSONUseNumber(rawLeg, &leg) == nil && leg.Protected {
			return true
		}
	}
	return false
}

// extractTimelineInstructions extracts timeline instructions across ALL 3 X timeline shapes:
//   - result.timeline_v2.timeline.instructions
//   - result.timeline.timeline.instructions
//   - result.timeline.timeline_v2.instructions
//   - plus direct timeline_v2.instructions / timeline.instructions fallback.
func extractTimelineInstructions(resMap map[string]json.RawMessage) ([]rawTimelineInstruction, bool, error) {
	tryContainer := func(raw json.RawMessage) ([]rawTimelineInstruction, bool, error) {
		if isJSONNull(raw) {
			return nil, false, nil
		}
		var m map[string]json.RawMessage
		if err := decodeJSONUseNumber(raw, &m); err != nil {
			return nil, false, err
		}
		// Nested timeline / timeline_v2 wrapper.
		for _, subKey := range []string{"timeline", "timeline_v2"} {
			if subRaw, ok := m[subKey]; ok && !isJSONNull(subRaw) {
				var subMap map[string]json.RawMessage
				if err := decodeJSONUseNumber(subRaw, &subMap); err != nil {
					return nil, false, err
				}
				if instrRaw, hasInstr := subMap["instructions"]; hasInstr && !isJSONNull(instrRaw) {
					var instrs []rawTimelineInstruction
					if err := decodeJSONUseNumber(instrRaw, &instrs); err != nil {
						return nil, false, err
					}
					return instrs, true, nil
				}
			}
		}
		// Direct instructions array inside container.
		if instrRaw, hasInstr := m["instructions"]; hasInstr && !isJSONNull(instrRaw) {
			var instrs []rawTimelineInstruction
			if err := decodeJSONUseNumber(instrRaw, &instrs); err != nil {
				return nil, false, err
			}
			return instrs, true, nil
		}
		return nil, false, nil
	}

	for _, topKey := range []string{"timeline_v2", "timeline"} {
		if raw, ok := resMap[topKey]; ok && !isJSONNull(raw) {
			instrs, found, err := tryContainer(raw)
			if err != nil {
				return nil, false, err
			}
			if found {
				return instrs, true, nil
			}
		}
	}
	return nil, false, nil
}

func extractCursorsFromEntry(entry *rawTimelineEntry, topCursor, bottomCursor *string) bool {
	if entry == nil {
		return false
	}
	cType := entry.Content.contentType()
	isCursor := cType == "TimelineTimelineCursor" ||
		strings.HasPrefix(strings.ToLower(entry.EntryID), "cursor-") ||
		entry.Content.CursorType != ""
	if !isCursor {
		return false
	}
	val := strings.TrimSpace(entry.Content.Value)
	if val == "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(entry.Content.CursorType)) {
	case "bottom":
		*bottomCursor = val
	case "top":
		*topCursor = val
	default:
		lowerID := strings.ToLower(entry.EntryID)
		if strings.HasPrefix(lowerID, "cursor-bottom") {
			*bottomCursor = val
		} else if strings.HasPrefix(lowerID, "cursor-top") {
			*topCursor = val
		}
	}
	return true
}

func isRecommendationModule(entryID, displayType string) bool {
	lowerID := strings.ToLower(strings.TrimSpace(entryID))
	if containsAny(lowerID, "who-to-", "who-to-follow", "who-to-subscribe", "topics-module", "follow-recommendations", "similar-to") {
		return true
	}
	lowerDisp := strings.ToLower(strings.TrimSpace(displayType))
	return containsAny(lowerDisp, "carousel", "gridcarousel", "verticalwithcontextline")
}

func profileToAuthorSummary(p *UserProfile) *AuthorSummary {
	if p == nil {
		return nil
	}
	return &AuthorSummary{
		RestID:         p.RestID,
		ScreenName:     p.ScreenName,
		Name:           p.Name,
		AvatarURL:      p.AvatarURL,
		Description:    p.Description,
		Verified:       p.Verified,
		IsBlueVerified: p.IsBlueVerified,
		Protected:      p.Protected,
		StatusesCount:  p.StatusesCount,
		MediaCount:     p.MediaCount,
		FollowersCount: p.FollowersCount,
		FriendsCount:   p.FriendsCount,
	}
}
