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

const defaultUserPostsCount = 18

// UserProfile represents a Douyin user's profile returned by /aweme/v1/web/user/profile/other/.
type UserProfile struct {
	SecUID         string `json:"secUid"`
	UID            string `json:"uid,omitempty"`
	UniqueID       string `json:"uniqueId,omitempty"`
	ShortID        string `json:"shortId,omitempty"`
	Nickname       string `json:"nickname"`
	Signature      string `json:"signature,omitempty"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
	AwemeCount     int    `json:"awemeCount"`
	FollowerCount  int64  `json:"followerCount"`
	FollowingCount int64  `json:"followingCount"`
	TotalFavorited int64  `json:"totalFavorited"`
	IsPrivate      bool   `json:"isPrivate,omitempty"`
	LiveStatus     int    `json:"liveStatus,omitempty"`
	RoomID         string `json:"roomId,omitempty"`
	WebRID         string `json:"webRid,omitempty"`
	RoomData       string `json:"-"`
	CanonicalURL   string `json:"canonicalUrl"`
}

type profileOtherEnvelope struct {
	StatusCode int              `json:"status_code"`
	StatusMsg  string           `json:"status_msg"`
	User       *rawDouyinAuthor `json:"user"`
}

// GetUserProfile fetches a Douyin creator's profile by sec_uid via user/profile/other.
func (c *Client) GetUserProfile(ctx context.Context, secUID string) (*UserProfile, error) {
	secUID = strings.TrimSpace(secUID)
	if !reValidSecUID.MatchString(secUID) {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音用户 sec_uid",
		}
	}

	params := c.profile.CommonWebParams(
		c.webID,
		"",
		sign.Pair{Key: "sec_user_id", Value: secUID},
		sign.Pair{Key: "source", Value: "channel_pc_web"},
		sign.Pair{Key: "publish_video_strategy_type", Value: "2"},
		sign.Pair{Key: "personal_center_strategy", Value: "1"},
	)

	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.ProfileOther,
		referer:     "https://www.douyin.com/user/" + secUID,
		params:      params,
		requireSign: true,
	})
	if err != nil {
		return nil, err
	}

	var env profileOtherEnvelope
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.ProfileOther,
			Message:  "解析抖音用户资料 JSON 失败",
			Err:      err,
		}
	}

	if env.User == nil {
		se := &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindRiskControl,
			Endpoint: c.endpoints.ProfileOther,
			Message:  "抖音用户资料为空，疑似触发风控静默拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	gotSec := strings.TrimSpace(env.User.SecUID)
	if gotSec != secUID {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.ProfileOther,
			Message:  "抖音返回的 sec_uid 与请求目标不一致",
		}
	}

	uniqueID := strings.TrimSpace(env.User.UniqueID)
	shortID := strings.TrimSpace(env.User.ShortID)
	if uniqueID == "" && shortID != "" && shortID != "0" {
		uniqueID = shortID
	}

	var totalFav int64
	if favStr := rawNumberOrString(env.User.TotalFavorited); favStr != "" {
		totalFav, _ = strconv.ParseInt(favStr, 10, 64)
	}

	roomID := env.User.roomIDString()
	webRID := env.User.embeddedWebRID()
	if webRID != "" {
		confirmedShort := shortID != "" && shortID != "0" && reDigitsOnly.MatchString(shortID) && webRID == shortID
		c.rememberWebRID(secUID, webRID, confirmedShort)
	}

	return &UserProfile{
		SecUID:         secUID,
		UID:            strings.TrimSpace(env.User.uidString()),
		UniqueID:       uniqueID,
		ShortID:        shortID,
		Nickname:       strings.TrimSpace(env.User.Nickname),
		Signature:      strings.TrimSpace(env.User.Signature),
		AvatarURL:      pickAuthorAvatar(env.User, c.allowLocalhost),
		AwemeCount:     env.User.AwemeCount,
		FollowerCount:  env.User.FollowerCount,
		FollowingCount: env.User.FollowingCount,
		TotalFavorited: totalFav,
		IsPrivate:      env.User.Secret == 1,
		LiveStatus:     env.User.LiveStatus,
		RoomID:         roomID,
		WebRID:         webRID,
		RoomData:       env.User.RoomData,
		CanonicalURL:   "https://www.douyin.com/user/" + secUID,
	}, nil
}

// UserPostsRequest configures a single paginated call to /aweme/v1/web/aweme/post/.
type UserPostsRequest struct {
	SecUID        string
	MaxCursor     string
	Count         int
	SeenIDs       map[string]bool
	KnownProfile  *UserProfile
	StreamOptions StreamSelectOptions
}

// UserPostsPage holds one validated page of works from aweme/post.
type UserPostsPage struct {
	Works                  []WorkDetail   `json:"works"`
	NextCursor             string         `json:"nextCursor,omitempty"`
	MinCursor              string         `json:"minCursor,omitempty"`
	HasMore                bool           `json:"hasMore"`
	CursorStalled          bool           `json:"cursorStalled,omitempty"`
	EmptyFollowupSuggested bool           `json:"emptyFollowupSuggested,omitempty"`
	UncertainEmpty         bool           `json:"uncertainEmpty,omitempty"`
	UncertainReason        string         `json:"uncertainReason,omitempty"`
	Author                 *AuthorSummary `json:"author,omitempty"`
}

// ListUserPosts fetches one page of a creator's works and strictly enforces the §9.2 empty-shell truth table,
// is_top pinned marker preservation, cross-page aweme_id deduplication, and cursor-stall protection.
func (c *Client) ListUserPosts(ctx context.Context, req UserPostsRequest) (*UserPostsPage, error) {
	secUID := strings.TrimSpace(req.SecUID)
	if !reValidSecUID.MatchString(secUID) {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音用户 sec_uid",
		}
	}

	reqCursor := strings.TrimSpace(req.MaxCursor)
	if reqCursor == "" {
		reqCursor = "0"
	}
	isSubsequentPage := reqCursor != "0"

	count := req.Count
	if count <= 0 {
		count = defaultUserPostsCount
	}

	params := c.profile.CommonWebParams(
		c.webID,
		"",
		sign.Pair{Key: "sec_user_id", Value: secUID},
		sign.Pair{Key: "max_cursor", Value: reqCursor},
		sign.Pair{Key: "locate_query", Value: "false"},
		sign.Pair{Key: "show_live_replay_strategy", Value: "1"},
		sign.Pair{Key: "need_time_list", Value: "0"},
		sign.Pair{Key: "time_list_query", Value: "0"},
		sign.Pair{Key: "whale_cut_token", Value: ""},
		sign.Pair{Key: "cut_version", Value: "1"},
		sign.Pair{Key: "count", Value: strconv.Itoa(count)},
		sign.Pair{Key: "publish_video_strategy_type", Value: "2"},
	)

	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.AwemePost,
		referer:     "https://www.douyin.com/user/" + secUID,
		params:      params,
		requireSign: true,
	})
	if err != nil {
		return nil, err
	}

	// Inspect raw JSON keys first to enforce douyin.md §9.2 truth table!
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.AwemePost,
			Message:  "解析抖音作品列表 JSON 失败",
			Err:      err,
		}
	}

	rawList, hasListKey := top["aweme_list"]
	rawHasMore, hasMoreKey := top["has_more"]

	// Truth table rows 2 & 3: bare {"status_code":0} missing aweme_list or has_more, or aweme_list: null.
	listTrimmed := bytes.TrimSpace(rawList)
	if !hasListKey || !hasMoreKey || len(listTrimmed) == 0 || bytes.Equal(listTrimmed, []byte("null")) {
		if isSubsequentPage && (!hasListKey || !hasMoreKey) {
			se := &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: c.endpoints.AwemePost,
				Message:  "抖音登录已失效（翻页遇到访客空壳拦截），请在设置页重新粘贴 Cookie",
			}
			c.onClassifiedError(ctx, se, true)
			return nil, se
		}
		se := &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindRiskControl,
			Endpoint: c.endpoints.AwemePost,
			Message:  "抖音作品列表载荷缺失，疑似触发风控静默拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	var nodes []rawAwemeNode
	listDec := json.NewDecoder(bytes.NewReader(listTrimmed))
	listDec.UseNumber()
	if err := listDec.Decode(&nodes); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.AwemePost,
			Message:  "抖音 aweme_list 数组结构异常",
			Err:      err,
		}
	}

	hasMore := parseJSONBoolOrInt(rawHasMore)
	nextCursor := rawNumberOrString(top["max_cursor"])
	minCursor := rawNumberOrString(top["min_cursor"])

	// Empty explicit array aweme_list: [] branches (douyin.md §9.2 rows 4, 5, 6).
	if len(nodes) == 0 {
		if hasMore {
			// Row 4: aweme_list: [] && has_more == 1.
			if nextCursor == "" || nextCursor == "0" || nextCursor == reqCursor {
				se := &site.Error{
					Site:     site.Douyin,
					Kind:     site.ErrKindRiskControl,
					Endpoint: c.endpoints.AwemePost,
					Message:  "抖音作品列表返回空页且游标未推进，已停止翻页",
				}
				c.ReportAutomaticError(ctx, se)
				return nil, se
			}
			return &UserPostsPage{
				Works:                  []WorkDetail{},
				NextCursor:             nextCursor,
				MinCursor:              minCursor,
				HasMore:                true,
				EmptyFollowupSuggested: true,
				UncertainEmpty:         true,
				UncertainReason:        "本页作品均被服务端过滤，允许跟进一次下一页验证",
			}, nil
		}

		// has_more == 0
		if isSubsequentPage {
			// Row 5: max_cursor > 0, explicit aweme_list: [], explicit has_more: 0 -> genuine end of list!
			return &UserPostsPage{
				Works:      []WorkDetail{},
				NextCursor: nextCursor,
				MinCursor:  minCursor,
				HasMore:    false,
			}, nil
		}

		// Row 6: max_cursor == 0 (first page) -> cross-check KnownProfile!
		if req.KnownProfile != nil {
			if req.KnownProfile.IsPrivate {
				return nil, &site.Error{
					Site:     site.Douyin,
					Kind:     site.ErrKindForbidden,
					Endpoint: c.endpoints.AwemePost,
					Message:  "该抖音账号已设为私密，无法查看作品列表",
				}
			}
			if req.KnownProfile.AwemeCount == 0 {
				return &UserPostsPage{
					Works:      []WorkDetail{},
					NextCursor: nextCursor,
					MinCursor:  minCursor,
					HasMore:    false,
				}, nil
			}
			if req.KnownProfile.AwemeCount > 5 {
				return &UserPostsPage{
					Works:           []WorkDetail{},
					NextCursor:      nextCursor,
					MinCursor:       minCursor,
					HasMore:         false,
					UncertainEmpty:  true,
					UncertainReason: "账号显示有公开作品但首屏返回 0 条，疑似风控静默限流或仅好友可见，本轮结果不完整",
				}, nil
			}
			// 1..5 works: may all be friends-only or self-only, still mark uncertain for subscription safety (douyin.md §11).
			return &UserPostsPage{
				Works:           []WorkDetail{},
				NextCursor:      nextCursor,
				MinCursor:       minCursor,
				HasMore:         false,
				UncertainEmpty:  true,
				UncertainReason: "首屏无可浏览公开作品（可能为仅好友/私密可见），本轮结果记为不完整",
			}, nil
		}

		// No KnownProfile passed: treat page-1 empty list as uncertain until cross-checked!
		return &UserPostsPage{
			Works:           []WorkDetail{},
			NextCursor:      nextCursor,
			MinCursor:       minCursor,
			HasMore:         false,
			UncertainEmpty:  true,
			UncertainReason: "首屏返回 0 条作品且未交叉核对作者资料，本轮结果记为不完整",
		}, nil
	}

	seen := req.SeenIDs
	if seen == nil {
		seen = make(map[string]bool, len(nodes))
	}

	works := make([]WorkDetail, 0, len(nodes))
	var pageAuthor *AuthorSummary

	for i := range nodes {
		rawNode := &nodes[i]
		if pageAuthor == nil && rawNode.Author != nil {
			summary := toAuthorSummary(rawNode.Author, c.allowLocalhost)
			pageAuthor = &summary
			if summary.WebRID != "" && summary.SecUID == secUID {
				confirmedShort := summary.ShortID != "" && summary.ShortID != "0" && reDigitsOnly.MatchString(summary.ShortID) && summary.WebRID == summary.ShortID
				c.rememberWebRID(secUID, summary.WebRID, confirmedShort)
			}
		}

		id := strings.TrimSpace(rawNode.AwemeID)
		if id == "" || seen[id] {
			continue
		}
		if rawNode.Status != nil && (rawNode.Status.IsDelete || rawNode.Status.IsProhibited) {
			seen[id] = true
			continue
		}

		work, wErr := parseAwemeWork(rawNode, req.StreamOptions, c.allowLocalhost, false)
		if wErr != nil {
			return nil, wErr
		}
		seen[id] = true
		if work.Kind == WorkKindUnsupported {
			continue
		}
		works = append(works, *work)
	}

	cursorStalled := false
	if hasMore && (nextCursor == "" || nextCursor == "0" || nextCursor == reqCursor) {
		hasMore = false
		cursorStalled = true
	}

	return &UserPostsPage{
		Works:         works,
		NextCursor:    nextCursor,
		MinCursor:     minCursor,
		HasMore:       hasMore,
		CursorStalled: cursorStalled,
		Author:        pageAuthor,
	}, nil
}

func parseJSONBoolOrInt(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(t, &b) == nil {
		return b
	}
	return parseJSONInt(t) != 0
}
