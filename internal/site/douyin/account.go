package douyin

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

// Read-only lists of the signed-in account (douyin.md §12). Every call is a
// manual request: it uses the global pacing and login check, never retries,
// and never writes to the site.

const (
	collectVersionCode    = "170400"
	collectVersionName    = "17.4.0"
	followingPageSize     = 20
	favoritePageSize      = 18
	collectionPageSize    = 20
	collectFolderPage     = 10
	collectFolderWorkPage = 10
	watchHistoryPageSize  = 20
	// WatchHistoryEnabled gates 观看历史. Confirmed via ACT-026 D-V5 (2 pages of
	// /aweme/v1/web/history/read/ with aweme_list, has_more, max_cursor,
	// aweme_date, status_code, status_msg); see douyin.md §12.
	WatchHistoryEnabled = true
)

// Account endpoint paths, joined to the API host of AwemePost so fixtures
// that point the API at a loopback server keep these requests local too.
const (
	pathFollowingList  = "/aweme/v1/web/user/following/list/"
	pathFavorite       = "/aweme/v1/web/aweme/favorite/"
	pathListCollection = "/aweme/v1/web/aweme/listcollection/"
	pathCollectsList   = "/aweme/v1/web/collects/list/"
	pathCollectsVideos = "/aweme/v1/web/collects/video/list/"
	pathHistoryRead    = "/aweme/v1/web/history/read/"
	shellMessage       = "抖音返回了空壳，登录已失效或被拦截"
	// likesRefusedMessage guides users with legacy/incomplete cookies to copy a complete
	// Cookie from the Mine/Likes tab while keeping single-endpoint isolation (no site backoff).
	likesRefusedMessage        = "抖音“喜欢”列表返回空响应：请在浏览器打开抖音「我的 → 喜欢」页面后重新复制完整 Cookie 更新登录；其他功能不受影响"
	likesBDTuringParamsMessage = "抖音“喜欢”列表触发抖音动态校验拦截（x-vc-bdturing-parameters）：请在浏览器打开抖音「我的 → 喜欢」页面完成验证后重新复制完整 Cookie 更新登录；其他功能不受影响"
	likesBDTuringVerifyMessage = "抖音“喜欢”列表触发抖音动态校验拦截（bdturing-verify）：请在浏览器打开抖音「我的 → 喜欢」页面完成验证后重新复制完整 Cookie 更新登录；其他功能不受影响"
	cursorStalledMessage       = "抖音列表的翻页位置没有前进，已停止翻页"
)

func (c *Client) accountEndpoint(path string) string {
	base := c.endpoints.AwemePost
	if i := strings.Index(base, "/aweme/"); i > 0 {
		return base[:i] + path
	}
	return "https://www.douyin.com" + path
}

// FollowingUser is one creator the signed-in account follows.
type FollowingUser struct {
	SecUID    string `json:"secUid"`
	UID       string `json:"uid,omitempty"`
	UniqueID  string `json:"uniqueId,omitempty"`
	Nickname  string `json:"nickname"`
	Signature string `json:"signature,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

// FollowingPage is one page of the following list. NextMaxTime is passed back as maxTime.
type FollowingPage struct {
	Users       []FollowingUser `json:"users"`
	NextMaxTime string          `json:"nextMaxTime"`
	HasMore     bool            `json:"hasMore"`
	Total       int             `json:"total"`
}

// Followings lists the creators selfSecUID follows; maxTime is "" for the first page.
func (c *Client) Followings(ctx context.Context, selfSecUID, maxTime string) (*FollowingPage, error) {
	selfSecUID = strings.TrimSpace(selfSecUID)
	if !reValidSecUID.MatchString(selfSecUID) {
		return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindAuthRequired, Message: "缺少登录账号的 sec_uid，请重新登录抖音"}
	}
	maxTime = strings.TrimSpace(maxTime)
	business := []sign.Pair{
		{Key: "user_id", Value: selfSecUID},
		{Key: "sec_user_id", Value: selfSecUID},
		{Key: "offset", Value: "0"},
		{Key: "count", Value: strconv.Itoa(followingPageSize)},
		{Key: "source_type", Value: "1"},
		{Key: "gps_access", Value: "0"},
		{Key: "address_book_access", Value: "0"},
		{Key: "min_change", Value: "0"},
	}
	if maxTime != "" && maxTime != "0" {
		business = append(business, sign.Pair{Key: "max_time", Value: maxTime})
	}
	ep := c.accountEndpoint(pathFollowingList)
	top, err := c.accountGet(ctx, ep, "https://www.douyin.com/user/self?showTab=following", c.profile.CommonWebParams(c.WebID(), "", business...), nil)
	if err != nil {
		return nil, err
	}
	var rawList json.RawMessage
	for _, key := range []string{"followings", "follow_list", "user_list"} {
		if v, ok := top[key]; ok && !isJSONNull(v) {
			rawList = v
			break
		}
	}
	rawHasMore, hasMoreKey := top["has_more"]
	if rawList == nil || !hasMoreKey {
		return nil, c.shellError(ctx, ep)
	}
	var users []rawDouyinAuthor
	if err := json.Unmarshal(rawList, &users); err != nil {
		return nil, schemaErr(ep, "抖音关注列表结构异常", err)
	}
	page := &FollowingPage{
		HasMore:     parseJSONBoolOrInt(rawHasMore),
		NextMaxTime: rawNumberOrString(top["min_time"]),
		Total:       int(parseJSONInt(top["total"])),
		Users:       make([]FollowingUser, 0, len(users)),
	}
	if page.HasMore && (page.NextMaxTime == "" || page.NextMaxTime == "0" || page.NextMaxTime == maxTime) {
		return nil, schemaErr(ep, cursorStalledMessage, nil)
	}
	seen := make(map[string]bool, len(users))
	for i := range users {
		s := toAuthorSummary(&users[i], c.allowLocalhost)
		if s.SecUID == "" || seen[s.SecUID] {
			continue
		}
		seen[s.SecUID] = true
		page.Users = append(page.Users, FollowingUser{
			SecUID: s.SecUID, UID: s.UID, UniqueID: s.UniqueID, Nickname: s.Nickname, Signature: s.Signature, AvatarURL: s.AvatarURL,
		})
	}
	return page, nil
}

// AccountWorksPage is one page of 喜欢, 收藏 or a 收藏夹.
type AccountWorksPage struct {
	Works      []WorkDetail `json:"works"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
}

// Favorites lists the works selfSecUID liked; cursor is "" for the first page.
func (c *Client) Favorites(ctx context.Context, selfSecUID, cursor string) (*AccountWorksPage, error) {
	selfSecUID = strings.TrimSpace(selfSecUID)
	if !reValidSecUID.MatchString(selfSecUID) {
		return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindAuthRequired, Message: "缺少登录账号的 sec_uid，请重新登录抖音"}
	}
	if err := c.ensureServerWebIDForFavorites(ctx); err != nil {
		return nil, err
	}
	cursor = normCursor(cursor)
	ep := c.accountEndpoint(pathFavorite)
	business := []sign.Pair{
		{Key: "sec_user_id", Value: selfSecUID},
		{Key: "max_cursor", Value: cursor},
	}
	if cursor == "0" {
		business = append(business, sign.Pair{Key: "min_cursor", Value: "0"})
	}
	business = append(business,
		sign.Pair{Key: "count", Value: strconv.Itoa(favoritePageSize)},
		sign.Pair{Key: "publish_video_strategy_type", Value: "2"},
		sign.Pair{Key: "pc_libra_divert", Value: "Windows"},
		sign.Pair{Key: "update_version_code", Value: collectVersionCode},
	)
	params := collectVersion(c.profile.CommonWebParams(c.WebID(), "", business...))
	raw, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    ep,
		referer:     "https://www.douyin.com/user/self?showTab=like",
		params:      params,
		requireSign: true,
		emptyMeans:  likesRefusedMessage,
	})
	if err != nil {
		return nil, err
	}
	top, err := decodeAccountTop(ep, raw.body)
	if err != nil {
		return nil, err
	}
	return c.parseAccountWorks(ctx, ep, top, cursor)
}

// Collection lists 全部收藏 via the POST form endpoint; cursor is "" for the first page.
func (c *Client) Collection(ctx context.Context, cursor string) (*AccountWorksPage, error) {
	cursor = normCursor(cursor)
	ep := c.accountEndpoint(pathListCollection)
	params := collectVersion(c.profile.CommonWebParams(c.WebID(), "",
		sign.Pair{Key: "publish_video_strategy_type", Value: "2"},
	))
	form := []sign.Pair{
		{Key: "count", Value: strconv.Itoa(collectionPageSize)},
		{Key: "cursor", Value: cursor},
	}
	top, err := c.accountGet(ctx, ep, "https://www.douyin.com/user/self?from_tab_name=main&showTab=favorite_collection", params, form)
	if err != nil {
		return nil, err
	}
	return c.parseAccountWorks(ctx, ep, top, cursor)
}

// CollectFolder is one of the signed-in account's 收藏夹.
type CollectFolder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Count    int    `json:"count"`
	CoverURL string `json:"coverUrl,omitempty"`
}

// CollectFoldersPage is one page of 收藏夹.
type CollectFoldersPage struct {
	Folders    []CollectFolder `json:"folders"`
	NextCursor string          `json:"nextCursor"`
	HasMore    bool            `json:"hasMore"`
}

// CollectFolders lists the account's 收藏夹; cursor is "" for the first page.
func (c *Client) CollectFolders(ctx context.Context, cursor string) (*CollectFoldersPage, error) {
	cursor = normCursor(cursor)
	ep := c.accountEndpoint(pathCollectsList)
	params := collectVersion(c.profile.CommonWebParams(c.WebID(), "",
		sign.Pair{Key: "cursor", Value: cursor},
		sign.Pair{Key: "count", Value: strconv.Itoa(collectFolderPage)},
	))
	top, err := c.accountGet(ctx, ep, "https://www.douyin.com/user/self?showTab=favorite_collection", params, nil)
	if err != nil {
		return nil, err
	}
	rawList, ok := top["collects_list"]
	rawHasMore, hasMoreKey := top["has_more"]
	if !hasMoreKey || (!ok && cursor == "0") {
		return nil, c.shellError(ctx, ep)
	}
	var folders []struct {
		IDStr  string           `json:"collects_id_str"`
		ID     json.RawMessage  `json:"collects_id"`
		Name   string           `json:"collects_name"`
		Total  json.RawMessage  `json:"total_number"`
		Status int              `json:"status"`
		Cover  *rawURLContainer `json:"collects_cover"`
	}
	if ok && !isJSONNull(rawList) {
		dec := json.NewDecoder(bytes.NewReader(rawList))
		dec.UseNumber()
		if err := dec.Decode(&folders); err != nil {
			return nil, schemaErr(ep, "抖音收藏夹列表结构异常", err)
		}
	}
	page := &CollectFoldersPage{HasMore: parseJSONBoolOrInt(rawHasMore), NextCursor: firstCursor(top), Folders: make([]CollectFolder, 0, len(folders))}
	if page.HasMore && (page.NextCursor == "0" || page.NextCursor == cursor) {
		return nil, schemaErr(ep, cursorStalledMessage, nil)
	}
	for _, f := range folders {
		id := strings.TrimSpace(f.IDStr)
		if id == "" {
			id = rawNumberOrString(f.ID)
		}
		if !reDigitsOnly.MatchString(id) {
			return nil, schemaErr(ep, "抖音收藏夹缺少编号", nil)
		}
		cf := CollectFolder{ID: id, Name: strings.TrimSpace(f.Name), Count: int(parseJSONInt(f.Total))}
		if f.Cover != nil {
			if urls := filterNoteImageURLs(f.Cover.URLList, c.allowLocalhost); len(urls) > 0 {
				cf.CoverURL = urls[0]
			}
		}
		page.Folders = append(page.Folders, cf)
	}
	return page, nil
}

// CollectFolderWorks lists one 收藏夹 by its string id; cursor is "" for the first page.
func (c *Client) CollectFolderWorks(ctx context.Context, folderID, cursor string) (*AccountWorksPage, error) {
	folderID = strings.TrimSpace(folderID)
	if !reDigitsOnly.MatchString(folderID) {
		return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "抖音收藏夹编号无效"}
	}
	cursor = normCursor(cursor)
	ep := c.accountEndpoint(pathCollectsVideos)
	params := collectVersion(c.profile.CommonWebParams(c.WebID(), "",
		sign.Pair{Key: "collects_id", Value: folderID},
		sign.Pair{Key: "cursor", Value: cursor},
		sign.Pair{Key: "count", Value: strconv.Itoa(collectFolderWorkPage)},
	))
	top, err := c.accountGet(ctx, ep, "https://www.douyin.com/user/self?showTab=favorite_collection", params, nil)
	if err != nil {
		return nil, err
	}
	return c.parseAccountWorks(ctx, ep, top, cursor)
}

// WatchHistory lists 观看历史 (douyin.md §12, confirmed by ACT-026 D-V5).
// cursor is "" for the first page; subsequent pages pass the previous max_cursor.
func (c *Client) WatchHistory(ctx context.Context, cursor string) (*AccountWorksPage, error) {
	cursor = normCursor(cursor)
	ep := c.accountEndpoint(pathHistoryRead)
	params := c.profile.CommonWebParams(c.WebID(), "",
		sign.Pair{Key: "max_cursor", Value: cursor},
		sign.Pair{Key: "count", Value: strconv.Itoa(watchHistoryPageSize)},
	)
	top, err := c.accountGet(ctx, ep, "https://www.douyin.com/user/self?showTab=record", params, nil)
	if err != nil {
		return nil, err
	}
	return c.parseAccountWorks(ctx, ep, top, cursor)
}

// accountGet sends one signed account request (GET, or POST when form is set) and decodes the top-level object.
func (c *Client) accountGet(ctx context.Context, ep, referer string, params, form []sign.Pair) (map[string]json.RawMessage, error) {
	origin := ""
	if form != nil {
		origin = DefaultOrigin
	}
	raw, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    ep,
		referer:     referer,
		origin:      origin,
		params:      params,
		requireSign: true,
		form:        form,
	})
	if err != nil {
		return nil, err
	}
	return decodeAccountTop(ep, raw.body)
}

func decodeAccountTop(ep string, body []byte) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, schemaErr(ep, "解析抖音列表 JSON 失败", err)
	}
	return top, nil
}

// parseAccountWorks applies douyin.md §12:
//   - missing aweme_list or missing has_more is always a shell (login lost or blocked);
//   - aweme_list:null on a subsequent last page (reqCursor != "0" && has_more == 0) is an empty
//     last page; on page 1 or with has_more != 0 it remains a shell;
//   - aweme_list items decode individually so one deleted/private/broken item does not fail the
//     whole page, while an entirely unparseable non-empty list still reports schema_changed.
func (c *Client) parseAccountWorks(ctx context.Context, ep string, top map[string]json.RawMessage, reqCursor string) (*AccountWorksPage, error) {
	rawList, hasList := top["aweme_list"]
	rawHasMore, hasMoreKey := top["has_more"]
	if !hasList || !hasMoreKey {
		return nil, c.shellError(ctx, ep)
	}
	hasMore := parseJSONBoolOrInt(rawHasMore)
	if isJSONNull(rawList) {
		if reqCursor != "0" && !hasMore {
			return &AccountWorksPage{
				Works:      []WorkDetail{},
				NextCursor: firstCursor(top),
				HasMore:    false,
			}, nil
		}
		return nil, c.shellError(ctx, ep)
	}
	var rawItems []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rawList))
	dec.UseNumber()
	if err := dec.Decode(&rawItems); err != nil {
		return nil, schemaErr(ep, "抖音 aweme_list 数组结构异常", err)
	}
	page := &AccountWorksPage{HasMore: hasMore, NextCursor: firstCursor(top), Works: make([]WorkDetail, 0, len(rawItems))}
	if page.HasMore && (page.NextCursor == "0" || page.NextCursor == reqCursor) {
		return nil, schemaErr(ep, cursorStalledMessage, nil)
	}
	seen := make(map[string]bool, len(rawItems))
	var (
		cleanSkipped int
		failedItems  int
		firstItemErr error
	)
	for _, rawItem := range rawItems {
		if isJSONNull(rawItem) {
			failedItems++
			if firstItemErr == nil {
				firstItemErr = schemaErr(ep, "抖音作品条目为空", nil)
			}
			continue
		}
		var n rawAwemeNode
		itemDec := json.NewDecoder(bytes.NewReader(rawItem))
		itemDec.UseNumber()
		if err := itemDec.Decode(&n); err != nil {
			failedItems++
			if firstItemErr == nil {
				firstItemErr = schemaErr(ep, "抖音作品条目结构异常", err)
			}
			continue
		}
		id := strings.TrimSpace(n.AwemeID)
		if id == "" {
			failedItems++
			if firstItemErr == nil {
				firstItemErr = schemaErr(ep, "抖音作品条目缺少 aweme_id", nil)
			}
			continue
		}
		if seen[id] {
			cleanSkipped++
			continue
		}
		if n.Status != nil && (n.Status.IsDelete || n.Status.IsProhibited) {
			seen[id] = true
			cleanSkipped++
			continue
		}
		work, err := parseAwemeWork(&n, StreamSelectOptions{}, c.allowLocalhost, false)
		if err != nil {
			failedItems++
			if firstItemErr == nil {
				firstItemErr = err
			}
			continue
		}
		seen[id] = true
		page.Works = append(page.Works, *work)
	}
	if len(rawItems) > 0 && len(page.Works) == 0 && cleanSkipped == 0 && failedItems > 0 {
		if site.IsKind(firstItemErr, site.ErrKindSchemaChanged) {
			return nil, firstItemErr
		}
		return nil, schemaErr(ep, "抖音列表作品条目全部解析失败", firstItemErr)
	}
	return page, nil
}

func (c *Client) shellError(ctx context.Context, ep string) error {
	se := &site.Error{Site: site.Douyin, Kind: site.ErrKindRiskControl, Endpoint: ep, Message: shellMessage}
	c.ReportAutomaticError(ctx, se)
	return se
}

func schemaErr(ep, msg string, err error) error {
	return &site.Error{Site: site.Douyin, Kind: site.ErrKindSchemaChanged, Endpoint: ep, Message: msg, Err: err}
}

// firstCursor reads the next-page cursor from `cursor` or `max_cursor`, preferring the first non-zero one.
func firstCursor(top map[string]json.RawMessage) string {
	first := ""
	for _, key := range []string{"cursor", "max_cursor"} {
		v := strings.TrimSpace(rawNumberOrString(top[key]))
		if v != "" && v != "0" {
			return v
		}
		if first == "" && v != "" {
			first = v
		}
	}
	if first == "" {
		return "0"
	}
	return first
}

func normCursor(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "0"
	}
	return s
}

// collectVersion swaps the common version pair for the one the collection endpoints expect.
func collectVersion(pairs []sign.Pair) []sign.Pair {
	for i := range pairs {
		switch pairs[i].Key {
		case "version_code":
			pairs[i].Value = collectVersionCode
		case "version_name":
			pairs[i].Value = collectVersionName
		}
	}
	return pairs
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}
