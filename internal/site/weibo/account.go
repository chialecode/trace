package weibo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// Feature flags gating uncertain "我的 › 微博" tabs per docs/research/weibo.md §7.3 & §13.1 #13.
// Both default to false until ACT-022 V-13 / V-14 verify their exact online response contracts.
const (
	// LikesEnabled gates "我的 › 微博 › 赞过" (/ajax/statuses/likelist).
	LikesEnabled = false

	// FollowGroupsEnabled gates custom follow-group filtering (/ajax/feed/allGroups + group members).
	FollowGroupsEnabled = false
)

const (
	likesUnavailableMessage = "微博“赞过”列表接口暂不可用，其他功能不受影响"
)

// FollowingUser represents one creator followed by the signed-in Weibo account.
// Kept strictly in memory (never written to disk or logs per DEC-047).
type FollowingUser struct {
	UID            string `json:"uid"`
	ScreenName     string `json:"screenName"`
	Remark         string `json:"remark,omitempty"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	Description    string `json:"description,omitempty"`
	FollowersCount int64  `json:"followersCount,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	VerifiedReason string `json:"verifiedReason,omitempty"`
	SpecialFollow  bool   `json:"specialFollow,omitempty"`
}

// FollowingPage holds one page of the signed-in user's following list.
type FollowingPage struct {
	Users      []FollowingUser `json:"users"`
	Page       int             `json:"page"`
	NextPage   int             `json:"nextPage,omitempty"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Total      int             `json:"total"`
	HasMore    bool            `json:"hasMore"`
}

// FollowGroup describes one follow group returned by /ajax/feed/allGroups.
type FollowGroup struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type,omitempty"`
	Count int    `json:"count,omitempty"`
}

// AccountPostsPage holds one page of "我的收藏" or "我赞过的微博".
type AccountPostsPage struct {
	Posts    []Post `json:"posts"`
	Page     int    `json:"page"`
	NextPage int    `json:"nextPage,omitempty"`
	Total    int    `json:"total"`
	HasMore  bool   `json:"hasMore"`
}

// ListFollowing lists the signed-in user's followed creators via GET /ajax/friendships/friends?uid=<self>&page=<page>.
// Read-only; callers may pass an optional seen map for cross-page deduplication.
func (c *Client) ListFollowing(ctx context.Context, selfUID string, page int, seen map[string]bool) (*FollowingPage, error) {
	return c.listFriendsInternal(ctx, selfUID, "", page, seen)
}

// ListFollowGroupMembers lists followed creators belonging to a specific follow group via
// GET /ajax/friendships/friends?uid=<self>&list_id=<groupID>&page=<page>.
// UI visibility is gated by FollowGroupsEnabled; exposed directly so ACT-022 V-13 can probe it.
func (c *Client) ListFollowGroupMembers(ctx context.Context, selfUID, groupID string, page int, seen map[string]bool) (*FollowingPage, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "缺少微博关注分组编号",
		}
	}
	return c.listFriendsInternal(ctx, selfUID, groupID, page, seen)
}

func (c *Client) listFriendsInternal(ctx context.Context, selfUID, listID string, page int, seen map[string]bool) (*FollowingPage, error) {
	normUID, err := site.NormalizeUID(site.Weibo, selfUID)
	if err != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "缺少登录账号的微博 UID，请重新登录微博",
		}
	}
	if page <= 0 {
		page = 1
	}

	q := url.Values{}
	q.Set("uid", normUID)
	q.Set("page", strconv.Itoa(page))
	if listID != "" {
		q.Set("list_id", listID)
	}

	endpointLabel := site.SanitizeEndpoint(c.endpoints.Friends)
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint: c.endpoints.Friends,
		query:    q,
		referer:  "https://weibo.com/u/page/follow/" + normUID,
	})
	if err != nil {
		return nil, err
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博关注列表 JSON 失败",
			Err:      err,
		}
	}

	// Support both top-level {ok:1, users:[...]} and wrapped {ok:1, data:{users:[...]}}.
	container := top
	if _, hasUsers := top["users"]; !hasUsers {
		if rawData, hasData := top["data"]; hasData && !isJSONNull(rawData) {
			var dm map[string]json.RawMessage
			if json.Unmarshal(rawData, &dm) == nil {
				container = dm
			}
		}
	}

	rawUsers, hasUsers := container["users"]
	if !hasUsers || isJSONNull(rawUsers) {
		se := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpointLabel,
			Message:  "微博关注列表返回空壳，疑似触发拦截或登录失效",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	var rawList []rawWeiboUser
	uDec := json.NewDecoder(bytes.NewReader(rawUsers))
	uDec.UseNumber()
	if err := uDec.Decode(&rawList); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博关注列表 users 数组结构异常",
			Err:      err,
		}
	}

	rawNextCursor, hasNextCursorKey := container["next_cursor"]
	nextCursor := strings.TrimSpace(rawNumberOrString(rawNextCursor))
	if nextCursor == "0" {
		nextCursor = ""
	}
	total := int(parseJSONInt64(container["total_number"]))

	users := parseFollowingUsers(rawList, seen, c.allowLocalhost)
	var hasMore bool
	if hasNextCursorKey {
		hasMore = len(rawList) > 0 && nextCursor != ""
	} else if seen != nil {
		hasMore = len(rawList) > 0 && (total == 0 || len(seen) < total)
	} else {
		hasMore = len(rawList) > 0 && (total == 0 || len(users) < total)
	}
	nextPage := 0
	if hasMore {
		nextPage = page + 1
	}

	return &FollowingPage{
		Users:      users,
		Page:       page,
		NextPage:   nextPage,
		NextCursor: nextCursor,
		Total:      total,
		HasMore:    hasMore,
	}, nil
}

// ListFollowingContent lists followed creators via the alternative /ajax/profile/followContent endpoint
// (weibo.md §7.1 endpoint A, available as backup and for ACT-022 V-13 comparison).
func (c *Client) ListFollowingContent(ctx context.Context, page int, nextCursor string, seen map[string]bool) (*FollowingPage, error) {
	if page <= 0 {
		page = 1
	}
	reqCursor := strings.TrimSpace(nextCursor)

	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("sortType", "all")
	if reqCursor != "" && reqCursor != "0" {
		q.Set("next_cursor", reqCursor)
	}

	endpointLabel := site.SanitizeEndpoint(c.endpoints.FollowContent)
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint: c.endpoints.FollowContent,
		query:    q,
		referer:  DefaultReferer,
	})
	if err != nil {
		return nil, err
	}

	var env struct {
		OK   int `json:"ok"`
		Data *struct {
			Follows *struct {
				Users       []rawWeiboUser  `json:"users"`
				NextCursor  json.RawMessage `json:"next_cursor"`
				TotalNumber json.RawMessage `json:"total_number"`
			} `json:"follows"`
			TotalNumber json.RawMessage `json:"total_number"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博 followContent JSON 失败",
			Err:      err,
		}
	}
	if env.Data == nil || env.Data.Follows == nil || env.Data.Follows.Users == nil {
		se := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpointLabel,
			Message:  "微博 followContent 返回空壳，疑似触发拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	outCursor := strings.TrimSpace(rawNumberOrString(env.Data.Follows.NextCursor))
	if outCursor == "0" {
		outCursor = ""
	}
	if reqCursor != "" && outCursor != "" && outCursor == reqCursor {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博关注列表游标未前进，已中止翻页",
		}
	}

	total := int(parseJSONInt64(env.Data.Follows.TotalNumber))
	if total == 0 {
		total = int(parseJSONInt64(env.Data.TotalNumber))
	}
	users := parseFollowingUsers(env.Data.Follows.Users, seen, c.allowLocalhost)
	hasMore := len(env.Data.Follows.Users) > 0 && outCursor != ""
	nextPage := 0
	if hasMore {
		nextPage = page + 1
	}
	return &FollowingPage{
		Users:      users,
		Page:       page,
		NextPage:   nextPage,
		NextCursor: outCursor,
		Total:      total,
		HasMore:    hasMore,
	}, nil
}

func parseFollowingUsers(rawList []rawWeiboUser, seen map[string]bool, allowLocalhost bool) []FollowingUser {
	if seen == nil {
		seen = make(map[string]bool, len(rawList))
	}
	out := make([]FollowingUser, 0, len(rawList))
	for i := range rawList {
		u := &rawList[i]
		uid := u.uidString()
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, FollowingUser{
			UID:            uid,
			ScreenName:     strings.TrimSpace(u.ScreenName),
			Remark:         strings.TrimSpace(u.Remark),
			AvatarURL:      pickBestAvatarURL(u, allowLocalhost),
			Description:    strings.TrimSpace(u.Description),
			FollowersCount: parseJSONInt64(u.FollowersCount),
			Verified:       u.Verified,
			VerifiedReason: strings.TrimSpace(u.VerifiedReason),
			SpecialFollow:  parseJSONBoolOrInt(u.SpecialFollow),
		})
	}
	return out
}

// ListFollowGroups lists the signed-in user's follow groups via GET /ajax/feed/allGroups.
// UI visibility is gated by FollowGroupsEnabled; exposed directly for ACT-022 V-13 probing.
func (c *Client) ListFollowGroups(ctx context.Context) ([]FollowGroup, error) {
	q := url.Values{}
	q.Set("is_new_segment", "1")
	q.Set("fetch_hot", "1")

	endpointLabel := site.SanitizeEndpoint(c.endpoints.AllGroups)
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint: c.endpoints.AllGroups,
		query:    q,
		referer:  DefaultReferer,
	})
	if err != nil {
		return nil, err
	}

	var top struct {
		OK     int `json:"ok"`
		Groups []struct {
			Title string `json:"title"`
			Group []struct {
				GID    json.RawMessage `json:"gid"`
				ListID json.RawMessage `json:"list_id"`
				Title  string          `json:"title"`
				Type   json.RawMessage `json:"type"`
				Count  json.RawMessage `json:"count"`
			} `json:"group"`
			GID    json.RawMessage `json:"gid"`
			ListID json.RawMessage `json:"list_id"`
			Type   json.RawMessage `json:"type"`
			Count  json.RawMessage `json:"count"`
		} `json:"groups"`
		Data *struct {
			Groups []struct {
				GID    json.RawMessage `json:"gid"`
				ListID json.RawMessage `json:"list_id"`
				Title  string          `json:"title"`
				Type   json.RawMessage `json:"type"`
				Count  json.RawMessage `json:"count"`
			} `json:"groups"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博关注分组 JSON 失败",
			Err:      err,
		}
	}

	seen := make(map[string]bool)
	var groups []FollowGroup
	addGroup := func(gidRaw, listIDRaw json.RawMessage, title string, typeRaw, countRaw json.RawMessage) {
		id := firstNonEmpty(rawNumberOrString(listIDRaw), rawNumberOrString(gidRaw))
		title = strings.TrimSpace(title)
		if id == "" || title == "" || seen[id] {
			return
		}
		seen[id] = true
		groups = append(groups, FollowGroup{
			ID:    id,
			Title: title,
			Type:  strings.TrimSpace(rawNumberOrString(typeRaw)),
			Count: int(parseJSONInt64(countRaw)),
		})
	}

	for _, sec := range top.Groups {
		if len(sec.Group) > 0 {
			for _, g := range sec.Group {
				addGroup(g.GID, g.ListID, g.Title, g.Type, g.Count)
			}
		} else {
			addGroup(sec.GID, sec.ListID, sec.Title, sec.Type, sec.Count)
		}
	}
	if top.Data != nil {
		for _, g := range top.Data.Groups {
			addGroup(g.GID, g.ListID, g.Title, g.Type, g.Count)
		}
	}

	return groups, nil
}

// ListFavorites lists the signed-in user's favorited posts via
// GET /ajax/favorites/all_fav?uid=<self>&page=<page>&with_total=true, strictly reading data.status (weibo.md §7.2).
func (c *Client) ListFavorites(ctx context.Context, selfUID string, page int, seen map[string]bool) (*AccountPostsPage, error) {
	normUID, err := site.NormalizeUID(site.Weibo, selfUID)
	if err != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "缺少登录账号的微博 UID，请重新登录微博",
		}
	}
	if page <= 0 {
		page = 1
	}

	q := url.Values{}
	q.Set("uid", normUID)
	q.Set("page", strconv.Itoa(page))
	q.Set("with_total", "true")

	endpointLabel := site.SanitizeEndpoint(c.endpoints.AllFavorites)
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint: c.endpoints.AllFavorites,
		query:    q,
		referer:  "https://weibo.com/fav",
	})
	if err != nil {
		return nil, err
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博收藏列表 JSON 失败",
			Err:      err,
		}
	}

	rawData, hasData := top["data"]
	if !hasData || isJSONNull(rawData) {
		se := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpointLabel,
			Message:  "微博收藏列表缺少 data 字段，疑似触发风控拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	var dataMap map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博收藏列表 data 结构失败",
			Err:      err,
		}
	}

	// Strictly read data.status per weibo.md §7.2 (NOT data.list!).
	rawStatuses, hasStatuses := dataMap["status"]
	if !hasStatuses || isJSONNull(rawStatuses) {
		se := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpointLabel,
			Message:  "微博收藏列表缺少 data.status 字段，疑似触发拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(rawStatuses, &rawEntries); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博收藏列表 data.status 数组结构异常",
			Err:      err,
		}
	}

	total := int(parseJSONInt64(dataMap["total_number"]))
	posts, err := c.parseAccountPostEntries(rawEntries, seen)
	if err != nil {
		return nil, err
	}

	hasMore := len(rawEntries) > 0
	if total > 0 && seen != nil && len(seen) >= total {
		hasMore = false
	}
	nextPage := 0
	if hasMore {
		nextPage = page + 1
	}
	return &AccountPostsPage{
		Posts:    posts,
		Page:     page,
		NextPage: nextPage,
		Total:    total,
		HasMore:  hasMore,
	}, nil
}

// ListLikes probes GET /ajax/statuses/likelist?uid=<self>&page=<page> (never ?id=<mid>!).
// UI entry is gated by LikesEnabled = false per weibo.md §7.3 & §13.1 #13.
// If the endpoint returns 404/403/0-byte/unexpected shell, it degrades cleanly to ErrKindUnavailable
// without triggering site-wide backoff/circuit-break (weibo.md §7.3).
func (c *Client) ListLikes(ctx context.Context, selfUID string, page int, seen map[string]bool) (*AccountPostsPage, error) {
	normUID, err := site.NormalizeUID(site.Weibo, selfUID)
	if err != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "缺少登录账号的微博 UID，请重新登录微博",
		}
	}
	if page <= 0 {
		page = 1
	}

	q := url.Values{}
	q.Set("uid", normUID)
	q.Set("page", strconv.Itoa(page))

	endpointLabel := site.SanitizeEndpoint(c.endpoints.LikeList)
	rawResp, err := c.executeAPIRequest(ctx, apiRequestSpec{
		endpoint:        c.endpoints.LikeList,
		query:           q,
		referer:         "https://weibo.com/u/" + normUID,
		emptyMeans:      likesUnavailableMessage,
		skipStoreExpire: true,
	}, false)
	if err != nil {
		return nil, err
	}

	// Per weibo.md §7.3: 404/403/0-byte/unexpected structure -> unavailable without tripping circuit breaker.
	if rawResp.statusCode == http.StatusNotFound || rawResp.statusCode == http.StatusForbidden ||
		(rawResp.statusCode == http.StatusOK && len(bytes.TrimSpace(rawResp.body)) == 0) {
		return nil, &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: rawResp.statusCode,
			Endpoint:   endpointLabel,
			Message:    likesUnavailableMessage,
		}
	}

	if se := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
		Endpoint:   endpointLabel,
		StatusCode: rawResp.statusCode,
		Header:     rawResp.header,
		Body:       rawResp.body,
		HasCookie:  true,
		EmptyMeans: likesUnavailableMessage,
	}); se != nil {
		if se.Kind == site.ErrKindAuthRequired {
			c.onClassifiedError(ctx, se, true)
			return nil, se
		}
		// Never trip site-wide backoff on uncertain likelist probe errors (except genuine rate limit 414/429).
		if se.Kind == site.ErrKindRateLimited {
			c.ReportAutomaticError(ctx, se)
			return nil, se
		}
		return nil, &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: rawResp.statusCode,
			Endpoint:   endpointLabel,
			Message:    likesUnavailableMessage,
		}
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpointLabel,
			Message:  likesUnavailableMessage,
		}
	}

	rawData, hasData := top["data"]
	if !hasData || isJSONNull(rawData) {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpointLabel,
			Message:  likesUnavailableMessage,
		}
	}
	var dataMap map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &dataMap); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpointLabel,
			Message:  likesUnavailableMessage,
		}
	}

	var rawList json.RawMessage
	for _, key := range []string{"list", "status", "statuses"} {
		if v, ok := dataMap[key]; ok && !isJSONNull(v) {
			rawList = v
			break
		}
	}
	if len(rawList) == 0 {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpointLabel,
			Message:  likesUnavailableMessage,
		}
	}

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(rawList, &rawEntries); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpointLabel,
			Message:  likesUnavailableMessage,
		}
	}

	total := int(parseJSONInt64(dataMap["total"]))
	if total == 0 {
		total = int(parseJSONInt64(dataMap["total_number"]))
	}
	posts, err := c.parseAccountPostEntries(rawEntries, seen)
	if err != nil {
		return nil, err
	}
	hasMore := len(rawEntries) > 0
	nextPage := 0
	if hasMore {
		nextPage = page + 1
	}
	return &AccountPostsPage{
		Posts:    posts,
		Page:     page,
		NextPage: nextPage,
		Total:    total,
		HasMore:  hasMore,
	}, nil
}

func (c *Client) parseAccountPostEntries(rawEntries []json.RawMessage, seen map[string]bool) ([]Post, error) {
	if seen == nil {
		seen = make(map[string]bool, len(rawEntries))
	}
	opt := PostOptions{
		StreamOptions:     StreamSelectOptions{PreferredCodec: c.preferredCodec()},
		SkipLongTextFetch: true,
		ImageURLRewriter:  c.imageURLRewriter,
	}
	posts := make([]Post, 0, len(rawEntries))
	for _, entry := range rawEntries {
		var rp rawPost
		dec := json.NewDecoder(bytes.NewReader(entry))
		dec.UseNumber()
		if err := dec.Decode(&rp); err != nil {
			continue
		}
		// Handle wrapped {"status": {...}} elements gracefully if returned by favorites/all_fav.
		if rp.midString() == "" {
			var wrapper struct {
				Status *rawPost `json:"status"`
			}
			wDec := json.NewDecoder(bytes.NewReader(entry))
			wDec.UseNumber()
			if wDec.Decode(&wrapper) == nil && wrapper.Status != nil {
				rp = *wrapper.Status
			}
		}
		mid := rp.midString()
		if mid == "" || seen[mid] {
			continue
		}
		post, err := c.parseRawPost(&rp, opt)
		if err != nil {
			return nil, err
		}
		seen[mid] = true
		posts = append(posts, *post)
	}
	return posts, nil
}
