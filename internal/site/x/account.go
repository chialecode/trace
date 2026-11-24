package x

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// Feature flags gating uncertain "我的 › X" sub-features per docs/research/x.md §15.1 #16.
// Both default to false until ACT-024 V-08 confirms online behavior (including non-Premium graceful degradation).
const (
	// BookmarkFoldersEnabled gates X Premium bookmark folders.
	BookmarkFoldersEnabled = false

	// ListsEnabled gates X Lists browsing.
	ListsEnabled = false
)

// FollowingUser represents one creator followed by the signed-in X account.
// Strictly read-only and kept in memory only (never written to disk or logs per DEC-047).
type FollowingUser struct {
	RestID         string `json:"restId"`
	ScreenName     string `json:"screenName"`
	Name           string `json:"name"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	Description    string `json:"description,omitempty"`
	FollowersCount int64  `json:"followersCount,omitempty"`
	FriendsCount   int64  `json:"friendsCount,omitempty"`
	StatusesCount  int    `json:"statusesCount,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	IsBlueVerified bool   `json:"isBlueVerified,omitempty"`
	Protected      bool   `json:"protected,omitempty"`
}

// FollowingPage holds one paginated page from GraphQL Following.
type FollowingPage struct {
	Users           []FollowingUser `json:"users"`
	NextCursor      string          `json:"nextCursor,omitempty"`
	HasMore         bool            `json:"hasMore"`
	CursorStagnated bool            `json:"cursorStagnated,omitempty"`
}

// AccountPostsPage holds one paginated page from GraphQL Bookmarks or Likes.
type AccountPostsPage struct {
	Posts           []Post `json:"posts"`
	NextCursor      string `json:"nextCursor,omitempty"`
	HasMore         bool   `json:"hasMore"`
	CursorStagnated bool   `json:"cursorStagnated,omitempty"`
}

// ListFollowing lists the signed-in user's followed X creators via GraphQL Following
// (used for "我的 › X › 关注" and bulk subscription import). Strictly read-only.
func (c *Client) ListFollowing(ctx context.Context, selfRestID string, cursor string, seen map[string]bool) (*FollowingPage, error) {
	normID, err := site.NormalizeUID(SiteID, selfRestID)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindAuthRequired,
			Message: "缺少登录账号的 X rest_id，请重新登录 X",
		}
	}

	reqCursor := strings.TrimSpace(cursor)
	vars := map[string]any{
		"userId":                 normID,
		"count":                  DefaultTimelinePageCount,
		"includePromotedContent": false,
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpFollowing,
		variables: vars,
		referer:   "https://x.com/following",
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpFollowing)
	instructions, err := c.extractUserTimelineInstructionsOrErr(ctx, gqlResp.data, endpoint)
	if err != nil {
		return nil, err
	}

	if seen == nil {
		seen = make(map[string]bool)
	}

	var (
		users      []FollowingUser
		rawCount   int
		nextCursor string
		topCursor  string
	)

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
				ic := entry.Content.ItemContent
				if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.UserResults == nil {
					continue
				}
				summary, _, uErr := parseRawUserResult(ic.UserResults.Result, c.allowLocalhost)
				if uErr != nil || summary == nil || summary.RestID == "" {
					continue
				}
				rawCount++
				if seen[summary.RestID] {
					continue
				}
				seen[summary.RestID] = true
				users = append(users, FollowingUser{
					RestID:         summary.RestID,
					ScreenName:     summary.ScreenName,
					Name:           summary.Name,
					AvatarURL:      summary.AvatarURL,
					Description:    summary.Description,
					FollowersCount: summary.FollowersCount,
					FriendsCount:   summary.FriendsCount,
					StatusesCount:  summary.StatusesCount,
					Verified:       summary.Verified,
					IsBlueVerified: summary.IsBlueVerified,
					Protected:      summary.Protected,
				})
			}
		}
	}

	// X terminates Following with a "0|..." zero cursor or empty cursor or cursor stagnation.
	if isZeroXCursor(nextCursor) {
		nextCursor = ""
	}
	cursorStagnated := reqCursor != "" && nextCursor != "" && nextCursor == reqCursor
	hasMore := rawCount > 0 && nextCursor != "" && !cursorStagnated
	if !hasMore {
		nextCursor = ""
	}
	if users == nil {
		users = []FollowingUser{}
	}

	return &FollowingPage{
		Users:           users,
		NextCursor:      nextCursor,
		HasMore:         hasMore,
		CursorStagnated: cursorStagnated,
	}, nil
}

// ListBookmarks lists the signed-in account's bookmarks via GraphQL Bookmarks (read-only, in-memory).
func (c *Client) ListBookmarks(ctx context.Context, cursor string, seen map[string]bool) (*AccountPostsPage, error) {
	reqCursor := strings.TrimSpace(cursor)
	vars := map[string]any{
		"count":                  DefaultTimelinePageCount,
		"includePromotedContent": false,
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpBookmarks,
		variables: vars,
		referer:   "https://x.com/i/bookmarks",
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpBookmarks)
	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(gqlResp.data, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 书签列表响应失败",
			Err:      err,
		}
	}

	instructions, found, pErr := extractBookmarkTimelineInstructions(dataMap, endpoint)
	if pErr != nil {
		return nil, pErr
	}
	if !found {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 书签列表返回空壳响应，疑似触发限流",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	return c.parseAccountTimelinePosts(instructions, reqCursor, seen), nil
}

func extractBookmarkTimelineInstructions(dataMap map[string]json.RawMessage, endpoint string) ([]rawTimelineInstruction, bool, error) {
	scopes := []map[string]json.RawMessage{dataMap}
	if rawViewer, ok := dataMap["viewer"]; ok && !isJSONNull(rawViewer) {
		var viewerMap map[string]json.RawMessage
		if err := decodeJSONUseNumber(rawViewer, &viewerMap); err == nil && len(viewerMap) > 0 {
			scopes = append(scopes, viewerMap)
			if rawUR, ok2 := viewerMap["user_results"]; ok2 && !isJSONNull(rawUR) {
				var urMap map[string]json.RawMessage
				if err := decodeJSONUseNumber(rawUR, &urMap); err == nil {
					if rawRes, ok3 := urMap["result"]; ok3 && !isJSONNull(rawRes) {
						var resMap map[string]json.RawMessage
						if err := decodeJSONUseNumber(rawRes, &resMap); err == nil && len(resMap) > 0 {
							scopes = append(scopes, resMap)
						}
					}
				}
			}
		}
	}

	for _, scope := range scopes {
		for _, key := range []string{"bookmark_timeline_v2", "bookmark_timeline", "bookmarks"} {
			raw, ok := scope[key]
			if !ok || isJSONNull(raw) {
				continue
			}
			var container map[string]json.RawMessage
			if err := decodeJSONUseNumber(raw, &container); err != nil {
				return nil, false, &site.Error{
					Site:     SiteID,
					Kind:     site.ErrKindSchemaChanged,
					Endpoint: endpoint,
					Message:  "解析 X 书签时间线结构失败",
					Err:      err,
				}
			}
			if tlRaw, ok2 := container["timeline"]; ok2 && !isJSONNull(tlRaw) {
				var tlMap map[string]json.RawMessage
				if err := decodeJSONUseNumber(tlRaw, &tlMap); err != nil {
					return nil, false, &site.Error{
						Site:     SiteID,
						Kind:     site.ErrKindSchemaChanged,
						Endpoint: endpoint,
						Message:  "解析 X 书签时间线 timeline 结构失败",
						Err:      err,
					}
				}
				if instrRaw, hasInstr := tlMap["instructions"]; hasInstr && !isJSONNull(instrRaw) {
					var instructions []rawTimelineInstruction
					if err := decodeJSONUseNumber(instrRaw, &instructions); err != nil {
						return nil, false, &site.Error{
							Site:     SiteID,
							Kind:     site.ErrKindSchemaChanged,
							Endpoint: endpoint,
							Message:  "解析 X 书签时间线 instructions 失败",
							Err:      err,
						}
					}
					return instructions, true, nil
				}
			}
			if instrRaw, hasInstr := container["instructions"]; hasInstr && !isJSONNull(instrRaw) {
				var instructions []rawTimelineInstruction
				if err := decodeJSONUseNumber(instrRaw, &instructions); err != nil {
					return nil, false, &site.Error{
						Site:     SiteID,
						Kind:     site.ErrKindSchemaChanged,
						Endpoint: endpoint,
						Message:  "解析 X 书签时间线 instructions 失败",
						Err:      err,
					}
				}
				return instructions, true, nil
			}
		}
	}
	return nil, false, nil
}

// ListLikes lists the signed-in account's own liked posts via GraphQL Likes (read-only, in-memory).
func (c *Client) ListLikes(ctx context.Context, selfRestID string, cursor string, seen map[string]bool) (*AccountPostsPage, error) {
	normID, err := site.NormalizeUID(SiteID, selfRestID)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindAuthRequired,
			Message: "缺少登录账号的 X rest_id，请重新登录 X",
		}
	}

	reqCursor := strings.TrimSpace(cursor)
	vars := map[string]any{
		"userId":                 normID,
		"count":                  DefaultTimelinePageCount,
		"includePromotedContent": false,
		"withClientEventToken":   false,
		"withBirdwatchNotes":     false,
		"withVoice":              true,
		"withV2Timeline":         true,
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpLikes,
		variables: vars,
		referer:   "https://x.com/i/user/" + normID + "/likes",
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpLikes)
	instructions, err := c.extractUserTimelineInstructionsOrErr(ctx, gqlResp.data, endpoint)
	if err != nil {
		return nil, err
	}
	return c.parseAccountTimelinePosts(instructions, reqCursor, seen), nil
}

func (c *Client) extractUserTimelineInstructionsOrErr(ctx context.Context, rawData json.RawMessage, endpoint string) ([]rawTimelineInstruction, error) {
	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 账号列表响应失败",
			Err:      err,
		}
	}

	rawUser, hasUser := dataMap["user"]
	if !hasUser {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 账号列表缺少 user 对象，疑似触发限流空壳",
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
			Message:  "解析 X 账号列表 user 结构失败",
			Err:      err,
		}
	}
	if len(userWrap) == 0 {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 账号列表返回空壳响应，疑似触发限流",
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
			Message:  "X 账号列表缺少 result 字段，疑似触发限流空壳",
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

	var resMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawResult, &resMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 账号列表 result 结构失败",
			Err:      err,
		}
	}
	if len(resMap) == 0 {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 账号列表返回空壳 result，疑似触发限流",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	if parseJSONString(resMap["__typename"]) == "UserUnavailable" {
		return nil, classifyUserUnavailableError(parseJSONString(resMap["reason"]), endpoint)
	}

	instructions, found, pErr := extractTimelineInstructions(resMap)
	if pErr != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 账号列表时间线结构失败",
			Err:      pErr,
		}
	}
	if !found {
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 账号列表缺少 timeline 数据，疑似触发限流空壳",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}
	return instructions, nil
}

func (c *Client) parseAccountTimelinePosts(instructions []rawTimelineInstruction, reqCursor string, seen map[string]bool) *AccountPostsPage {
	if seen == nil {
		seen = make(map[string]bool)
	}
	var (
		posts      []Post
		rawCount   int
		nextCursor string
		topCursor  string
	)

	addTweet := func(raw json.RawMessage) {
		p := c.parseTweetResult(raw, 0)
		if p == nil || p.Unavailable || p.ID == "" {
			return
		}
		rawCount++
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		posts = append(posts, *p)
	}

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
					addTweet(ic.TweetResults.Result)

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
						addTweet(ic.TweetResults.Result)
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
				addTweet(ic.TweetResults.Result)
			}
		}
	}

	if isZeroXCursor(nextCursor) {
		nextCursor = ""
	}
	cursorStagnated := reqCursor != "" && nextCursor != "" && nextCursor == reqCursor
	hasMore := rawCount > 0 && nextCursor != "" && !cursorStagnated
	if !hasMore {
		nextCursor = ""
	}
	if posts == nil {
		posts = []Post{}
	}

	return &AccountPostsPage{
		Posts:           posts,
		NextCursor:      nextCursor,
		HasMore:         hasMore,
		CursorStagnated: cursorStagnated,
	}
}

func isZeroXCursor(cursor string) bool {
	c := strings.TrimSpace(cursor)
	return c == "" || c == "0" || strings.HasPrefix(c, "0|")
}
