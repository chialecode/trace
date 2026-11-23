package weibo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// Feature filter constants for /ajax/statuses/mymblog (weibo.md §6.2).
const (
	FeatureAll      = 0
	FeatureOriginal = 1
	FeatureImage    = 2
	FeatureVideo    = 3
	FeatureMusic    = 4
	FeatureArticle  = 10
)

// UserProfile holds a Weibo creator's normalized profile from /ajax/profile/info.
type UserProfile struct {
	UID             string `json:"uid"`
	ScreenName      string `json:"screenName"`
	AvatarURL       string `json:"avatarUrl,omitempty"`
	Description     string `json:"description,omitempty"`
	StatusesCount   int    `json:"statusesCount"`
	FollowersCount  int64  `json:"followersCount"`
	FriendsCount    int64  `json:"friendsCount"`
	Verified        bool   `json:"verified,omitempty"`
	VerifiedType    int    `json:"verifiedType,omitempty"`
	VerifiedReason  string `json:"verifiedReason,omitempty"`
	Domain          string `json:"domain,omitempty"`
	CoverImagePhone string `json:"coverImagePhone,omitempty"`
	BlockText       string `json:"blockText,omitempty"`
	CanonicalURL    string `json:"canonicalUrl"`
}

// GetUserProfile fetches a Weibo user's profile by stable numeric uid via /ajax/profile/info?uid=<uid>.
func (c *Client) GetUserProfile(ctx context.Context, uid string) (*UserProfile, error) {
	normUID, err := site.NormalizeUID(site.Weibo, uid)
	if err != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博用户 UID",
		}
	}
	q := url.Values{}
	q.Set("uid", normUID)
	prof, err := c.fetchUserProfile(ctx, q, "https://weibo.com/u/"+normUID, normUID)
	if err != nil {
		return nil, err
	}
	return prof, nil
}

// GetUserProfileByScreenName resolves a mutable /n/<screen_name> nickname into a stable UserProfile with uid.
func (c *Client) GetUserProfileByScreenName(ctx context.Context, screenName string) (*UserProfile, error) {
	nick := strings.TrimSpace(screenName)
	if nick == "" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "微博用户昵称不能为空",
		}
	}
	q := url.Values{}
	q.Set("screen_name", nick)
	return c.fetchUserProfile(ctx, q, DefaultReferer, "")
}

// GetUserProfileByCustom resolves a mutable custom domain (weibo.com/<custom>) into a stable UserProfile with uid.
func (c *Client) GetUserProfileByCustom(ctx context.Context, custom string) (*UserProfile, error) {
	dom := strings.TrimSpace(custom)
	if dom == "" || !reCustomDomain.MatchString(dom) || reservedWeiboRootPaths[strings.ToLower(dom)] {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博个性域名",
		}
	}
	q := url.Values{}
	q.Set("custom", dom)
	return c.fetchUserProfile(ctx, q, "https://weibo.com/"+dom, "")
}

func (c *Client) fetchUserProfile(ctx context.Context, q url.Values, referer string, expectedUID string) (*UserProfile, error) {
	return c.fetchUserProfileWithSpec(ctx, apiRequestSpec{
		endpoint: c.endpoints.ProfileInfo,
		query:    q,
		referer:  referer,
	}, expectedUID)
}

func (c *Client) fetchUserProfileWithSpec(ctx context.Context, spec apiRequestSpec, expectedUID string) (*UserProfile, error) {
	rawResp, err := c.doAPIRequest(ctx, spec)
	if err != nil {
		return nil, err
	}
	return c.parseUserProfileBody(rawResp.body, expectedUID)
}

func (c *Client) parseUserProfileBody(body []byte, expectedUID string) (*UserProfile, error) {
	endpoint := site.SanitizeEndpoint(c.endpoints.ProfileInfo)
	var top struct {
		OK   int             `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析微博用户资料 JSON 失败",
			Err:      err,
		}
	}

	rawData := bytes.TrimSpace(top.Data)
	if len(rawData) == 0 || bytes.Equal(rawData, []byte("null")) {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpoint,
			Message:  "微博用户资料响应缺少 data 字段，疑似触发拦截",
		}
	}
	// PHP-backed Weibo endpoints may return empty array data:[] for nonexistent resources.
	if bytes.Equal(rawData, []byte("[]")) {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "微博用户不存在或暂不可查看",
		}
	}

	var dataObj struct {
		User      *rawWeiboUser `json:"user"`
		BlockText string        `json:"blockText"`
	}
	dataDec := json.NewDecoder(bytes.NewReader(rawData))
	dataDec.UseNumber()
	if err := dataDec.Decode(&dataObj); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析微博用户资料 data 结构失败",
			Err:      err,
		}
	}

	blockText := strings.TrimSpace(dataObj.BlockText)
	if dataObj.User == nil {
		if blockText != "" {
			return nil, blockTextError(endpoint, blockText)
		}
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "微博用户不存在或暂不可查看",
		}
	}

	gotUID := dataObj.User.uidString()
	screenName := strings.TrimSpace(dataObj.User.ScreenName)
	if (gotUID == "" || gotUID == "0") && screenName == "" {
		if blockText != "" {
			return nil, blockTextError(endpoint, blockText)
		}
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindUnavailable,
			Endpoint: endpoint,
			Message:  "微博用户不存在或暂不可查看",
		}
	}

	normUID, uErr := site.NormalizeUID(site.Weibo, gotUID)
	if uErr != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "微博用户资料缺少有效的数字 UID",
		}
	}
	if expectedUID != "" && normUID != expectedUID {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  fmt.Sprintf("微博返回的 UID 与请求目标不一致（期望 %s，实际 %s）", expectedUID, normUID),
		}
	}

	summary := dataObj.User.toAuthorSummary(c.allowLocalhost)
	return &UserProfile{
		UID:             normUID,
		ScreenName:      summary.ScreenName,
		AvatarURL:       summary.AvatarURL,
		Description:     summary.Description,
		StatusesCount:   summary.StatusesCount,
		FollowersCount:  summary.FollowersCount,
		FriendsCount:    summary.FriendsCount,
		Verified:        summary.Verified,
		VerifiedType:    summary.VerifiedType,
		VerifiedReason:  summary.VerifiedReason,
		Domain:          summary.Domain,
		CoverImagePhone: normalizeWeiboMediaURL(dataObj.User.CoverImagePhone, c.allowLocalhost),
		BlockText:       blockText,
		CanonicalURL:    "https://weibo.com/u/" + normUID,
	}, nil
}

func blockTextError(endpoint, blockText string) *site.Error {
	kind := site.ErrKindUnavailable
	if isForbiddenMessage(blockText) {
		kind = site.ErrKindForbidden
	}
	return &site.Error{
		Site:     site.Weibo,
		Kind:     kind,
		Endpoint: endpoint,
		Message:  blockText,
	}
}

// UserPostsRequest configures one paginated call to /ajax/statuses/mymblog.
type UserPostsRequest struct {
	UID                 string
	Page                int // 1-based; defaults to 1
	SinceID             string
	Feature             int // FeatureAll (0), FeatureOriginal (1), etc.
	SeenIDs             map[string]bool
	KnownProfile        *UserProfile
	CrossCheckProfile   bool // when true and page==1 returns empty without KnownProfile, fetches GetUserProfile
	StreamOptions       StreamSelectOptions
	UnauthenticatedCall bool // used only by ACT-022 V-04 unauthenticated control check
}

// UserPostsPage holds one validated page of posts from /ajax/statuses/mymblog.
type UserPostsPage struct {
	Posts             []Post         `json:"posts"`
	Page              int            `json:"page"`
	NextPage          int            `json:"nextPage,omitempty"`
	NextSinceID       string         `json:"nextSinceId,omitempty"`
	Total             int            `json:"total,omitempty"`
	StatusVisible     int            `json:"statusVisible,omitempty"`
	HasMore           bool           `json:"hasMore"`
	BottomTipsVisible bool           `json:"bottomTipsVisible,omitempty"`
	BottomTipsText    string         `json:"bottomTipsText,omitempty"`
	UncertainEmpty    bool           `json:"uncertainEmpty,omitempty"`
	UncertainReason   string         `json:"uncertainReason,omitempty"`
	Author            *AuthorSummary `json:"author,omitempty"`
}

// GetUserPosts fetches one page of a creator's timeline from /ajax/statuses/mymblog, enforcing:
//   - page + since_id pagination & cursor stall -> schema_changed
//   - pinned detection (isTop / mblogtype==2 / title=="置顶")
//   - fast retweet detection (ori_mid)
//   - ad / paid / foreign card filtering
//   - bottom_tips_visible / bottom_tips_text visibility-window completion
//   - full weibo.md §6.5 empty-shell vs genuine-empty truth table (cross-checking blockText & statuses_count)
func (c *Client) GetUserPosts(ctx context.Context, req UserPostsRequest) (*UserPostsPage, error) {
	normUID, err := site.NormalizeUID(site.Weibo, req.UID)
	if err != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博用户 UID",
		}
	}

	pageNum := req.Page
	if pageNum <= 0 {
		pageNum = 1
	}
	reqSinceID := strings.TrimSpace(req.SinceID)

	q := url.Values{}
	q.Set("uid", normUID)
	q.Set("page", strconv.Itoa(pageNum))
	q.Set("feature", strconv.Itoa(req.Feature))
	if reqSinceID != "" {
		q.Set("since_id", reqSinceID)
	}

	endpointLabel := site.SanitizeEndpoint(c.endpoints.StatusesMyMblog)
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:      c.endpoints.StatusesMyMblog,
		query:         q,
		referer:       "https://weibo.com/u/" + normUID,
		guestNoCookie: req.UnauthenticatedCall,
	})
	if err != nil {
		return nil, err
	}

	// Inspect raw JSON structure first to enforce weibo.md §6.5 empty-shell truth table!
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博列表 JSON 失败",
			Err:      err,
		}
	}

	rawData, hasData := top["data"]
	if !hasData || isJSONNull(rawData) {
		// Truth table row 3: HTTP 200 ok==1 missing data -> silent risk-control shell, never empty!
		se := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpointLabel,
			Message:  "微博列表响应缺少 data 字段，疑似触发风控空壳拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	var dataMap map[string]json.RawMessage
	dataDec := json.NewDecoder(bytes.NewReader(rawData))
	dataDec.UseNumber()
	if err := dataDec.Decode(&dataMap); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博列表 data 结构失败",
			Err:      err,
		}
	}

	rawList, hasList := dataMap["list"]
	if !hasList || isJSONNull(rawList) {
		// Truth table row 3: missing data.list or data.list: null -> risk_control shell!
		se := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpointLabel,
			Message:  "微博列表载荷缺失（data.list 为空），疑似触发风控静默拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	var rawItems []rawPost
	listDec := json.NewDecoder(bytes.NewReader(rawList))
	listDec.UseNumber()
	if err := listDec.Decode(&rawItems); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博列表 data.list 数组结构异常",
			Err:      err,
		}
	}

	nextSinceID := strings.TrimSpace(rawNumberOrString(dataMap["since_id"]))
	if nextSinceID == "0" {
		nextSinceID = ""
	}
	total := parseJSONInt(dataMap["total"])
	statusVisible := parseJSONInt(dataMap["status_visible"])
	bottomTipsVisible := parseJSONBoolOrInt(dataMap["bottom_tips_visible"])
	bottomTipsText := strings.TrimSpace(parseJSONString(dataMap["bottom_tips_text"]))

	// Truth table row 7: cursor stall -> schema_changed!
	if reqSinceID != "" && nextSinceID != "" && nextSinceID == reqSinceID {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博列表翻页游标停滞未前进，已中止翻页",
		}
	}

	// Empty data.list == [] branches (weibo.md §6.5 rows 4, 5, 6).
	if len(rawItems) == 0 {
		// Row 4: bottom_tips_visible == true -> reached visibility window limit (e.g. half-year visible).
		if bottomTipsVisible {
			return &UserPostsPage{
				Posts:             []Post{},
				Page:              pageNum,
				Total:             total,
				StatusVisible:     statusVisible,
				HasMore:           false,
				BottomTipsVisible: true,
				BottomTipsText:    bottomTipsText,
			}, nil
		}

		// Row 5: page > 1 && since_id == "" && !bottom_tips_visible -> genuine end of timeline.
		if pageNum > 1 && nextSinceID == "" {
			return &UserPostsPage{
				Posts:         []Post{},
				Page:          pageNum,
				Total:         total,
				StatusVisible: statusVisible,
				HasMore:       false,
			}, nil
		}

		// Row 6: page == 1 && since_id == "" && !bottom_tips_visible -> cross-check profile/info!
		if pageNum == 1 && nextSinceID == "" {
			prof := req.KnownProfile
			if prof == nil && req.CrossCheckProfile && c.canCallEndpoint(c.endpoints.ProfileInfo) {
				fetched, pErr := c.GetUserProfile(ctx, normUID)
				if pErr != nil {
					return nil, pErr
				}
				prof = fetched
			}
			if prof != nil {
				if prof.BlockText != "" {
					return nil, blockTextError(endpointLabel, prof.BlockText)
				}
				if prof.StatusesCount == 0 {
					return &UserPostsPage{
						Posts:         []Post{},
						Page:          pageNum,
						Total:         total,
						StatusVisible: statusVisible,
						HasMore:       false,
						Author: &AuthorSummary{
							UID:           prof.UID,
							ScreenName:    prof.ScreenName,
							AvatarURL:     prof.AvatarURL,
							StatusesCount: prof.StatusesCount,
						},
					}, nil
				}
				return &UserPostsPage{
					Posts:           []Post{},
					Page:            pageNum,
					Total:           total,
					StatusVisible:   statusVisible,
					HasMore:         false,
					UncertainEmpty:  true,
					UncertainReason: "账号微博总数大于 0 但第 1 页返回空列表，疑似风控限流或全私密，本轮结果记为不完整",
					Author: &AuthorSummary{
						UID:           prof.UID,
						ScreenName:    prof.ScreenName,
						AvatarURL:     prof.AvatarURL,
						StatusesCount: prof.StatusesCount,
					},
				}, nil
			}
			return &UserPostsPage{
				Posts:           []Post{},
				Page:            pageNum,
				Total:           total,
				StatusVisible:   statusVisible,
				HasMore:         false,
				UncertainEmpty:  true,
				UncertainReason: "第 1 页返回空微博列表且未核对作者资料，本轮结果记为不完整",
			}, nil
		}

		// Empty list but server still returned a non-empty advancing since_id: mark uncertain.
		return &UserPostsPage{
			Posts:           []Post{},
			Page:            pageNum,
			NextPage:        pageNum + 1,
			NextSinceID:     nextSinceID,
			Total:           total,
			StatusVisible:   statusVisible,
			HasMore:         false,
			UncertainEmpty:  true,
			UncertainReason: "本页返回空微博列表，已中止翻页并标记结果不完整",
		}, nil
	}

	// Non-empty data.list: filter ads/foreign cards, parse posts, dedupe across pages.
	seen := req.SeenIDs
	if seen == nil {
		seen = make(map[string]bool, len(rawItems))
	}

	parseOpt := PostOptions{
		StreamOptions:     req.StreamOptions,
		SkipLongTextFetch: true, // Timeline listing reuses payload without extra longtext requests (§6.6).
		SkipOriginFetch:   true, // Timeline listing never fans out origin statuses/show per item.
		ImageURLRewriter:  c.imageURLRewriter,
	}
	if parseOpt.StreamOptions.PreferredCodec == "" {
		parseOpt.StreamOptions.PreferredCodec = c.preferredCodec()
	}

	posts := make([]Post, 0, len(rawItems))
	var pageAuthor *AuthorSummary

	for i := range rawItems {
		item := &rawItems[i]
		if shouldFilterTimelineCard(item, normUID) {
			continue
		}
		mid := item.midString()
		if mid == "" || seen[mid] {
			continue
		}

		post, pErr := c.parseRawPost(item, parseOpt)
		if pErr != nil {
			return nil, pErr
		}
		seen[mid] = true
		if pageAuthor == nil && post.Author.UID == normUID {
			a := post.Author
			pageAuthor = &a
		}
		posts = append(posts, *post)
	}

	hasMore := !bottomTipsVisible && nextSinceID != ""
	nextPage := 0
	if hasMore {
		nextPage = pageNum + 1
	}

	return &UserPostsPage{
		Posts:             posts,
		Page:              pageNum,
		NextPage:          nextPage,
		NextSinceID:       nextSinceID,
		Total:             total,
		StatusVisible:     statusVisible,
		HasMore:           hasMore,
		BottomTipsVisible: bottomTipsVisible,
		BottomTipsText:    bottomTipsText,
		Author:            pageAuthor,
	}, nil
}

// shouldFilterTimelineCard removes promoted/ad/paid/foreign activity cards before they enter
// pagination or subscription checks (weibo.md §6.3).
func shouldFilterTimelineCard(item *rawPost, targetUID string) bool {
	if item == nil {
		return true
	}
	// Ad / promoted post (mblogtype == 1) or paid promotion or non-empty promotion block.
	if parseJSONInt64(item.MblogType) == 1 {
		return true
	}
	if parseJSONBoolOrInt(item.IsPaid) {
		return true
	}
	if !isJSONNull(item.Promotion) {
		t := bytes.TrimSpace(item.Promotion)
		if !bytes.Equal(t, []byte("{}")) && !bytes.Equal(t, []byte("[]")) && !bytes.Equal(t, []byte("false")) && !bytes.Equal(t, []byte("0")) {
			return true
		}
	}

	// Foreign user card that is NOT a fast retweet (ori_mid == "").
	oriMid := strings.TrimSpace(rawNumberOrString(item.OriMid))
	isFastRetweet := oriMid != "" && oriMid != "0"
	itemUID := item.User.uidString()
	if !isFastRetweet && itemUID != "" && itemUID != targetUID {
		return true
	}
	if !isFastRetweet && itemUID == "" {
		return true
	}
	return false
}
