package x

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// ListSummary holds normalized read-only metadata for a public X List (DEC-093).
type ListSummary struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Description     string        `json:"description,omitempty"`
	MemberCount     int           `json:"memberCount"`
	SubscriberCount int           `json:"subscriberCount,omitempty"`
	Mode            string        `json:"mode,omitempty"` // "Public" | "Private"
	BannerURL       string        `json:"bannerUrl,omitempty"`
	Creator         AuthorSummary `json:"creator"`
	CanonicalURL    string        `json:"canonicalUrl"`
}

// ListsPage holds one paginated page of public X Lists belonging to a user.
type ListsPage struct {
	Lists           []ListSummary `json:"lists"`
	NextCursor      string        `json:"nextCursor,omitempty"`
	HasMore         bool          `json:"hasMore"`
	CursorStagnated bool          `json:"cursorStagnated,omitempty"`
}

// ListTweetsPage holds one paginated page of tweets from an X List timeline.
type ListTweetsPage struct {
	ListID          string `json:"listId"`
	Posts           []Post `json:"posts"`
	NextCursor      string `json:"nextCursor,omitempty"`
	TopCursor       string `json:"topCursor,omitempty"`
	HasMore         bool   `json:"hasMore"`
	CursorStagnated bool   `json:"cursorStagnated,omitempty"`
}

// ListMembersPage holds one paginated page of members from a public X List.
type ListMembersPage struct {
	ListID          string          `json:"listId"`
	Members         []FollowingUser `json:"members"`
	NextCursor      string          `json:"nextCursor,omitempty"`
	HasMore         bool            `json:"hasMore"`
	CursorStagnated bool            `json:"cursorStagnated,omitempty"`
}

// NormalizeListID validates a numeric X List ID (1..24 ASCII digits, non-zero).
func NormalizeListID(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > 24 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", false
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "", false
	}
	return s, true
}

// CanonicalListURL returns the canonical public URL for an X List ID.
func CanonicalListURL(listID string) string {
	return "https://x.com/i/lists/" + strings.TrimSpace(listID)
}

// GetListByRestId fetches read-only metadata for a public X List by numeric list ID
// via GraphQL ListByRestId (2-hop lazy chunk discovery, 5 features + 3 toggles).
func (c *Client) GetListByRestId(ctx context.Context, listID string) (*ListSummary, error) {
	normID, ok := NormalizeListID(listID)
	if !ok {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 列表 ID",
		}
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpListByRestId,
		variables: map[string]any{
			"listId": normID,
		},
		referer: CanonicalListURL(normID),
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpListByRestId)
	return c.parseListByRestIdData(gqlResp.data, normID, endpoint)
}

func (c *Client) parseListByRestIdData(rawData json.RawMessage, expectedID, endpoint string) (*ListSummary, error) {
	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 列表信息响应失败",
			Err:      err,
		}
	}

	rawList, hasList := dataMap["list"]
	if !hasList {
		rawList, hasList = dataMap["list_by_rest_id"]
	}
	if !hasList || isJSONNull(rawList) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 列表不存在、已被删除或设为私密",
		}
	}

	ls := parseRawListObject(rawList, c.allowLocalhost)
	if ls == nil || ls.ID == "" {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 列表不可查看或已设为私密",
		}
	}
	if expectedID != "" && ls.ID != expectedID {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  fmt.Sprintf("X 返回的列表 ID 与请求目标不一致（期望 %s，实际 %s）", expectedID, ls.ID),
		}
	}
	return ls, nil
}

// GetListTweets fetches one paginated page of latest tweets in a public X List via
// GraphQL ListLatestTweetsTimeline (42 features + 9 toggles, withArticle* toggles enabled).
func (c *Client) GetListTweets(ctx context.Context, listID, cursor string, seen map[string]bool) (*ListTweetsPage, error) {
	normID, ok := NormalizeListID(listID)
	if !ok {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 列表 ID",
		}
	}

	reqCursor := strings.TrimSpace(cursor)
	vars := map[string]any{
		"listId": normID,
		"count":  DefaultTimelinePageCount,
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation:    OpListLatestTweetsTimeline,
		variables:    vars,
		fieldToggles: articleFieldToggles(),
		referer:      CanonicalListURL(normID),
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpListLatestTweetsTimeline)
	instructions, found, pErr := extractListTimelineInstructions(gqlResp.data, "tweets_timeline")
	if pErr != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 列表帖子时间线失败",
			Err:      pErr,
		}
	}
	if !found {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 列表不可查看或暂无可访问帖子",
		}
	}

	if seen == nil {
		seen = make(map[string]bool)
	}
	var (
		posts      []Post
		nextCursor string
		topCursor  string
	)
	accept := func(p *Post) {
		if p == nil || p.Unavailable || p.ID == "" || seen[p.ID] {
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
					accept(c.parseTweetResult(ic.TweetResults.Result, 0))
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
						accept(c.parseTweetResult(ic.TweetResults.Result, 0))
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
				accept(c.parseTweetResult(ic.TweetResults.Result, 0))
			}
		}
	}

	cursorStagnated := reqCursor != "" && nextCursor != "" && nextCursor == reqCursor
	hasMore := nextCursor != "" && !cursorStagnated && len(posts) > 0 && !isZeroXCursor(nextCursor)
	if !hasMore {
		nextCursor = ""
	}
	if posts == nil {
		posts = []Post{}
	}
	return &ListTweetsPage{
		ListID:          normID,
		Posts:           posts,
		NextCursor:      nextCursor,
		TopCursor:       topCursor,
		HasMore:         hasMore,
		CursorStagnated: cursorStagnated,
	}, nil
}

// GetListMembers fetches one paginated page of members of a public X List via
// GraphQL ListMembers (read-only).
func (c *Client) GetListMembers(ctx context.Context, listID, cursor string, seen map[string]bool) (*ListMembersPage, error) {
	normID, ok := NormalizeListID(listID)
	if !ok {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 列表 ID",
		}
	}

	reqCursor := strings.TrimSpace(cursor)
	vars := map[string]any{
		"listId": normID,
		"count":  DefaultTimelinePageCount,
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: OpListMembers,
		variables: vars,
		referer:   CanonicalListURL(normID) + "/members",
	})
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + OpListMembers)
	instructions, found, pErr := extractListTimelineInstructions(gqlResp.data, "members_timeline")
	if pErr != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 列表成员响应失败",
			Err:      pErr,
		}
	}
	if !found {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 列表不可查看或成员列表不可访问",
		}
	}

	if seen == nil {
		seen = make(map[string]bool)
	}
	var (
		members    []FollowingUser
		nextCursor string
		topCursor  string
	)
	acceptUser := func(rawUser json.RawMessage) {
		if isJSONNull(rawUser) {
			return
		}
		author, _, _ := parseRawUserResult(rawUser, c.allowLocalhost)
		if author == nil || author.RestID == "" || seen[author.RestID] {
			return
		}
		seen[author.RestID] = true
		members = append(members, FollowingUser{
			RestID:         author.RestID,
			ScreenName:     author.ScreenName,
			Name:           author.Name,
			AvatarURL:      author.AvatarURL,
			Description:    author.Description,
			Protected:      author.Protected,
			Verified:       author.Verified,
			IsBlueVerified: author.IsBlueVerified,
			FollowersCount: author.FollowersCount,
		})
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
					if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.UserResults == nil {
						continue
					}
					acceptUser(ic.UserResults.Result)
				case "TimelineTimelineModule":
					for _, modItem := range entry.Content.Items {
						if isPromotedOrNoiseEntryID(modItem.EntryID) {
							continue
						}
						ic := modItem.Item.ItemContent
						if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.UserResults == nil {
							continue
						}
						acceptUser(ic.UserResults.Result)
					}
				}
			}
		case "TimelineAddToModule":
			for _, modItem := range instr.ModuleItems {
				if isPromotedOrNoiseEntryID(modItem.EntryID) {
					continue
				}
				ic := modItem.Item.ItemContent
				if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) || ic.UserResults == nil {
					continue
				}
				acceptUser(ic.UserResults.Result)
			}
		}
	}

	cursorStagnated := reqCursor != "" && nextCursor != "" && nextCursor == reqCursor
	hasMore := nextCursor != "" && !cursorStagnated && len(members) > 0 && !isZeroXCursor(nextCursor)
	if !hasMore {
		nextCursor = ""
	}
	if members == nil {
		members = []FollowingUser{}
	}
	return &ListMembersPage{
		ListID:          normID,
		Members:         members,
		NextCursor:      nextCursor,
		HasMore:         hasMore,
		CursorStagnated: cursorStagnated,
	}, nil
}

// GetUserLists fetches one paginated page of a user's public X Lists via
// CombinedLists (falling back to ListOwnerships if CombinedLists is absent from
// discovered chunks). Strictly read-only.
func (c *Client) GetUserLists(ctx context.Context, restID, cursor string, seen map[string]bool) (*ListsPage, error) {
	normUID, err := site.NormalizeUID(SiteID, restID)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 用户 rest_id",
		}
	}

	reqCursor := strings.TrimSpace(cursor)
	vars := map[string]any{
		"userId": normUID,
		"count":  DefaultTimelinePageCount,
	}
	if reqCursor != "" {
		vars["cursor"] = reqCursor
	}

	opName := OpCombinedLists
	gqlResp, err := c.doGraphQL(ctx, graphqlRequest{
		operation: opName,
		variables: vars,
		referer:   "https://x.com/i/user/" + normUID + "/lists",
	})
	if err != nil && site.IsKind(err, site.ErrKindSchemaChanged) {
		opName = OpListOwnerships
		gqlResp, err = c.doGraphQL(ctx, graphqlRequest{
			operation: opName,
			variables: vars,
			referer:   "https://x.com/i/user/" + normUID + "/lists",
		})
	}
	if err != nil {
		return nil, err
	}

	endpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + opName)
	return c.parseUserListsData(gqlResp.data, reqCursor, seen, endpoint)
}

func (c *Client) parseUserListsData(rawData json.RawMessage, reqCursor string, seen map[string]bool, endpoint string) (*ListsPage, error) {
	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 用户公开列表响应失败",
			Err:      err,
		}
	}
	rawUser, hasUser := dataMap["user"]
	if !hasUser || isJSONNull(rawUser) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 用户不存在或无法查看列表",
		}
	}
	var userWrap struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := decodeJSONUseNumber(rawUser, &userWrap); err != nil || len(userWrap.Result) == 0 {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "该 X 用户公开列表不可查看",
		}
	}
	if parseJSONString(userWrap.Result["__typename"]) == "UserUnavailable" {
		return nil, classifyUserUnavailableError(parseJSONString(userWrap.Result["reason"]), endpoint)
	}

	instructions, hasTimeline, pErr := extractTimelineInstructions(userWrap.Result)
	if pErr != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析 X 用户列表时间线失败",
			Err:      pErr,
		}
	}
	if !hasTimeline {
		return &ListsPage{Lists: []ListSummary{}}, nil
	}

	if seen == nil {
		seen = make(map[string]bool)
	}
	var (
		lists      []ListSummary
		nextCursor string
		topCursor  string
	)
	acceptList := func(rawList json.RawMessage) {
		if isJSONNull(rawList) {
			return
		}
		ls := parseRawListObject(rawList, c.allowLocalhost)
		if ls == nil || ls.ID == "" || seen[ls.ID] {
			return
		}
		seen[ls.ID] = true
		lists = append(lists, *ls)
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
					if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) {
						continue
					}
					acceptList(ic.List)
				case "TimelineTimelineModule":
					for _, modItem := range entry.Content.Items {
						if isPromotedOrNoiseEntryID(modItem.EntryID) {
							continue
						}
						ic := modItem.Item.ItemContent
						if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) {
							continue
						}
						acceptList(ic.List)
					}
				}
			}
		case "TimelineAddToModule":
			for _, modItem := range instr.ModuleItems {
				if isPromotedOrNoiseEntryID(modItem.EntryID) {
					continue
				}
				ic := modItem.Item.ItemContent
				if ic == nil || hasNonEmptyRawJSON(ic.PromotedMetadata) {
					continue
				}
				acceptList(ic.List)
			}
		}
	}

	cursorStagnated := reqCursor != "" && nextCursor != "" && nextCursor == reqCursor
	hasMore := nextCursor != "" && !cursorStagnated && len(lists) > 0 && !isZeroXCursor(nextCursor)
	if !hasMore {
		nextCursor = ""
	}
	if lists == nil {
		lists = []ListSummary{}
	}
	return &ListsPage{
		Lists:           lists,
		NextCursor:      nextCursor,
		HasMore:         hasMore,
		CursorStagnated: cursorStagnated,
	}, nil
}

func extractListTimelineInstructions(rawData json.RawMessage, timelineField string) ([]rawTimelineInstruction, bool, error) {
	var dataMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawData, &dataMap); err != nil {
		return nil, false, err
	}
	rawList, ok := dataMap["list"]
	if !ok || isJSONNull(rawList) {
		return nil, false, nil
	}
	var listMap map[string]json.RawMessage
	if err := decodeJSONUseNumber(rawList, &listMap); err != nil {
		return nil, false, err
	}
	rawTL, ok := listMap[timelineField]
	if !ok || isJSONNull(rawTL) {
		return nil, false, nil
	}
	var tlWrap struct {
		Timeline *struct {
			Instructions []rawTimelineInstruction `json:"instructions"`
		} `json:"timeline"`
		TimelineV2 *struct {
			Instructions []rawTimelineInstruction `json:"instructions"`
		} `json:"timeline_v2"`
		Instructions []rawTimelineInstruction `json:"instructions"`
	}
	if err := decodeJSONUseNumber(rawTL, &tlWrap); err != nil {
		return nil, false, err
	}
	switch {
	case tlWrap.Timeline != nil:
		return tlWrap.Timeline.Instructions, true, nil
	case tlWrap.TimelineV2 != nil:
		return tlWrap.TimelineV2.Instructions, true, nil
	case tlWrap.Instructions != nil:
		return tlWrap.Instructions, true, nil
	default:
		return nil, false, nil
	}
}

func parseRawListObject(raw json.RawMessage, allowLocalhost bool) *ListSummary {
	if isJSONNull(raw) {
		return nil
	}
	var node struct {
		IDStr              string                 `json:"id_str"`
		ID                 json.RawMessage        `json:"id"`
		Name               string                 `json:"name"`
		Description        string                 `json:"description"`
		MemberCount        json.RawMessage        `json:"member_count"`
		SubscriberCount    json.RawMessage        `json:"subscriber_count"`
		Mode               string                 `json:"mode"`
		CustomBannerMedia  *rawArticleMediaEntity `json:"custom_banner_media"`
		DefaultBannerMedia *rawArticleMediaEntity `json:"default_banner_media"`
		UserResults        *struct {
			Result json.RawMessage `json:"result"`
		} `json:"user_results"`
	}
	if err := decodeJSONUseNumber(raw, &node); err != nil {
		return nil
	}
	rawID := strings.TrimSpace(node.IDStr)
	if rawID == "" {
		rawID = parseJSONScalarString(node.ID)
	}
	normID, ok := NormalizeListID(rawID)
	if !ok {
		return nil
	}

	var creator AuthorSummary
	if node.UserResults != nil && !isJSONNull(node.UserResults.Result) {
		if a, _, _ := parseRawUserResult(node.UserResults.Result, allowLocalhost); a != nil {
			creator = *a
		}
	}

	bannerURL := ""
	for _, b := range []*rawArticleMediaEntity{node.CustomBannerMedia, node.DefaultBannerMedia} {
		if b != nil && b.MediaInfo != nil && strings.TrimSpace(b.MediaInfo.OriginalImgURL) != "" {
			if safe := SafePublicImageURL(normalizeXMediaURL(b.MediaInfo.OriginalImgURL, allowLocalhost)); safe != "" {
				bannerURL = safe
				break
			}
		}
	}

	mode := strings.TrimSpace(node.Mode)
	if mode == "" {
		mode = "Public"
	}

	return &ListSummary{
		ID:              normID,
		Name:            strings.TrimSpace(node.Name),
		Description:     strings.TrimSpace(node.Description),
		MemberCount:     clampNonNegativeInt(parseJSONInt64(node.MemberCount)),
		SubscriberCount: clampNonNegativeInt(parseJSONInt64(node.SubscriberCount)),
		Mode:            mode,
		BannerURL:       bannerURL,
		Creator:         creator,
		CanonicalURL:    CanonicalListURL(normID),
	}
}
