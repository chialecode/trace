package weibo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

// SelfProfile holds the strictly verified account identity for the currently logged-in Weibo user.
type SelfProfile struct {
	UID        string `json:"uid"`
	ScreenName string `json:"screenName"`
	Domain     string `json:"domain,omitempty"`
	AvatarURL  string `json:"avatarUrl,omitempty"`
}

// BasicInfo holds the parsed fields from GET /ajax/setting/getBasicInfo.
// Per ACT-022 login confirmation (weibo.md §13.1 #2), getBasicInfo returns 18 profile-setting fields
// including screen_name and domain, and does NOT return uid or avatar.
type BasicInfo struct {
	UID         string `json:"uid,omitempty"`
	ScreenName  string `json:"screenName"`
	Domain      string `json:"domain,omitempty"`
	Description string `json:"description,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Province    string `json:"province,omitempty"`
	City        string `json:"city,omitempty"`
}

// LoginWithCookie parses, whitelists, and strictly verifies a user-pasted weibo.com Cookie
// on the PC domain before saving it to the Weibo credential store (weibo.md §13.1 #2).
//
// Exact flow (PC domain ONLY, never calls m.weibo.cn, never silently falls back to another endpoint):
//  1. Parse & clean via credential.ParseWeiboCookieInput (64 KiB raw cap, 32 KiB cleaned cap, control-char check, SUB required -> invalid_input, 0 requests).
//  2. GET https://weibo.com/ajax/setting/getBasicInfo -> require HTTP 200, ok==1, non-empty data.screen_name.
//  3. GET https://weibo.com/ajax/profile/info?screen_name=<screen_name> -> require valid numeric data.user.idstr,
//     avatar, and exact data.user.screen_name == basicInfo.screen_name match; otherwise fail without saving.
//
// On any failure, raw Cookie input is never echoed and existing stored credentials are untouched.
func (c *Client) LoginWithCookie(ctx context.Context, rawCookie string) (credential.Status, error) {
	cleaned, err := credential.ParseWeiboCookieInput(rawCookie)
	if err != nil {
		switch {
		case errors.Is(err, credential.ErrInputTooLarge):
			return credential.Status{}, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindInvalidInput,
				Message: "粘贴的 Cookie 长度超过 64 KiB 上限，请只复制 weibo.com 站点的 Cookie",
			}
		case errors.Is(err, credential.ErrCleanedCookieTooLarge):
			return credential.Status{}, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindInvalidInput,
				Message: "清洗后的微博 Cookie 请求头长度超过 32 KiB 上限，请只复制 weibo.com 站点的 Cookie",
			}
		case errors.Is(err, credential.ErrInvalidCookieControlChar):
			return credential.Status{}, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindInvalidInput,
				Message: "粘贴的 Cookie 包含非法控制字符或换行，请重新从浏览器复制",
			}
		default:
			return credential.Status{}, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindInvalidInput,
				Message: "未检测到有效的微博登录字段（需包含 SUB），请从已登录 weibo.com 的浏览器重新复制 Cookie",
			}
		}
	}

	prof, mergedCookies, err := c.verifyCandidateCookies(ctx, cleaned)
	if err != nil {
		return credential.Status{}, err
	}

	if c.cred == nil {
		return credential.Status{
			Site:     credential.SiteWeibo,
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      prof.UID,
			Uname:    prof.ScreenName,
			Face:     prof.AvatarURL,
		}, nil
	}

	return c.cred.SaveVerifiedWeiboManual(mergedCookies, credential.WeiboProfile{
		UID:   prof.UID,
		Uname: prof.ScreenName,
		Face:  prof.AvatarURL,
	})
}

// VerifyCredential re-verifies the currently stored Weibo credential using the strict PC 2-step flow.
// risk_control and rate_limited never clear or expire the stored credential.
func (c *Client) VerifyCredential(ctx context.Context) (credential.Status, error) {
	if c.cred == nil {
		return credential.Status{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录微博",
		}
	}
	if err := c.RequireAuth(c.endpoints.PCBasicInfo); err != nil {
		return c.cred.Status(), err
	}

	prof, err := c.GetSelfProfile(ctx)
	if err != nil {
		return c.cred.Status(), err
	}

	st, uErr := c.cred.UpdateWeiboProfile(credential.WeiboProfile{
		UID:   prof.UID,
		Uname: prof.ScreenName,
		Face:  prof.AvatarURL,
	})
	if uErr != nil {
		return c.cred.Status(), uErr
	}
	return st, nil
}

// LogoutCredential clears the stored Weibo credential.
func (c *Client) LogoutCredential() error {
	if c.cred == nil {
		return nil
	}
	return c.cred.Clear()
}

// GetSelfProfile verifies the active stored credential on the PC domain via:
//  1. GET /ajax/setting/getBasicInfo (proves PC login and obtains screen_name)
//  2. GET /ajax/profile/info?screen_name=<screen_name> (resolves stable numeric uid + avatar and cross-checks screen_name)
func (c *Client) GetSelfProfile(ctx context.Context) (*SelfProfile, error) {
	rawBasic, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint: c.endpoints.PCBasicInfo,
		referer:  DefaultReferer,
	})
	if err != nil {
		return nil, err
	}
	basic, parseErr := parseStrictBasicInfo(rawBasic.body, site.SanitizeEndpoint(c.endpoints.PCBasicInfo))
	if parseErr != nil {
		c.onClassifiedError(ctx, parseErr, true)
		return nil, parseErr
	}

	q := url.Values{}
	q.Set("screen_name", basic.ScreenName)
	prof, err := c.fetchUserProfileWithSpec(ctx, apiRequestSpec{
		endpoint: c.endpoints.ProfileInfo,
		query:    q,
		referer:  DefaultReferer,
	}, basic.UID)
	if err != nil {
		return nil, err
	}

	if err := verifySelfProfileMatch(basic, prof, site.SanitizeEndpoint(c.endpoints.ProfileInfo)); err != nil {
		c.onClassifiedError(ctx, err, false)
		return nil, err
	}

	return &SelfProfile{
		UID:        prof.UID,
		ScreenName: prof.ScreenName,
		Domain:     firstNonEmpty(prof.Domain, basic.Domain),
		AvatarURL:  prof.AvatarURL,
	}, nil
}

// CheckBasicInfoGuest runs an unauthenticated control request against /ajax/setting/getBasicInfo
// (used by ACT-022 V-01 to prove unauthenticated requests fail as auth_required).
func (c *Client) CheckBasicInfoGuest(ctx context.Context) (*BasicInfo, error) {
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:        c.endpoints.PCBasicInfo,
		referer:         DefaultReferer,
		guestNoCookie:   true,
		skipStoreExpire: true,
	})
	if err != nil {
		return nil, err
	}
	basic, parseErr := parseStrictBasicInfo(rawResp.body, site.SanitizeEndpoint(c.endpoints.PCBasicInfo))
	if parseErr != nil {
		return nil, parseErr
	}
	return basic, nil
}

// CheckBasicInfoWithCookies probes /ajax/setting/getBasicInfo with explicit candidate cookies
// without touching the credential store (used by ACT-022 V-01 guest/cleaned cookie checks).
func (c *Client) CheckBasicInfoWithCookies(ctx context.Context, cookies map[string]string) (*BasicInfo, error) {
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:        c.endpoints.PCBasicInfo,
		referer:         DefaultReferer,
		candidateCookie: cookies,
		skipStoreExpire: true,
	})
	if err != nil {
		return nil, err
	}
	basic, parseErr := parseStrictBasicInfo(rawResp.body, site.SanitizeEndpoint(c.endpoints.PCBasicInfo))
	if parseErr != nil {
		return nil, parseErr
	}
	return basic, nil
}

func (c *Client) verifyCandidateCookies(ctx context.Context, cleaned map[string]string) (*SelfProfile, map[string]string, error) {
	merged := make(map[string]string, len(cleaned))
	for k, v := range cleaned {
		merged[k] = v
	}

	// Step 1: GET /ajax/setting/getBasicInfo with cleaned candidate Cookie.
	rawBasic, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:        c.endpoints.PCBasicInfo,
		referer:         DefaultReferer,
		candidateCookie: merged,
		skipStoreExpire: true,
	})
	if err != nil {
		return nil, nil, classifyCandidateLoginError(err, site.SanitizeEndpoint(c.endpoints.PCBasicInfo))
	}
	mergeResponseXSRF(merged, rawBasic.header)

	basic, parseErr := parseStrictBasicInfo(rawBasic.body, site.SanitizeEndpoint(c.endpoints.PCBasicInfo))
	if parseErr != nil {
		return nil, nil, parseErr
	}

	// Step 2: GET /ajax/profile/info?screen_name=<screen_name> with the same PC Cookie.
	q := url.Values{}
	q.Set("screen_name", basic.ScreenName)
	rawProfile, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:        c.endpoints.ProfileInfo,
		query:           q,
		referer:         DefaultReferer,
		candidateCookie: merged,
		skipStoreExpire: true,
	})
	if err != nil {
		return nil, nil, classifyCandidateLoginError(err, site.SanitizeEndpoint(c.endpoints.ProfileInfo))
	}
	mergeResponseXSRF(merged, rawProfile.header)

	prof, pErr := c.parseUserProfileBody(rawProfile.body, basic.UID)
	if pErr != nil {
		if site.IsKind(pErr, site.ErrKindRiskControl) || site.IsKind(pErr, site.ErrKindRateLimited) {
			return nil, nil, classifyCandidateLoginError(pErr, site.SanitizeEndpoint(c.endpoints.ProfileInfo))
		}
		return nil, nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: site.SanitizeEndpoint(c.endpoints.ProfileInfo),
			Message:  "无法确认微博登录账号的 UID 与头像，本次未保存新 Cookie",
			Err:      pErr,
		}
	}

	if matchErr := verifySelfProfileMatch(basic, prof, site.SanitizeEndpoint(c.endpoints.ProfileInfo)); matchErr != nil {
		return nil, nil, matchErr
	}

	return &SelfProfile{
		UID:        prof.UID,
		ScreenName: prof.ScreenName,
		Domain:     firstNonEmpty(prof.Domain, basic.Domain),
		AvatarURL:  prof.AvatarURL,
	}, merged, nil
}

func mergeResponseXSRF(dst map[string]string, hdr http.Header) {
	if hdr == nil || dst == nil {
		return
	}
	resp := &http.Response{Header: hdr}
	if xsrf := extractXSRFTokenFromCookies(resp.Cookies()); xsrf != "" {
		dst["XSRF-TOKEN"] = xsrf
	}
}

func classifyCandidateLoginError(err error, endpoint string) error {
	switch site.KindOf(err) {
	case site.ErrKindRiskControl, site.ErrKindRateLimited:
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.KindOf(err),
			Endpoint: endpoint,
			Message:  "微博触发安全验证或访问受限，本次未保存新 Cookie，请稍后或在浏览器正常访问微博后再试",
		}
	case site.ErrKindAuthRequired:
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "该 Cookie 未处于已登录状态、已过期或仅为访客会话，请从已登录 weibo.com 的浏览器重新复制",
		}
	default:
		return err
	}
}

func parseStrictBasicInfo(body []byte, endpoint string) (*BasicInfo, *site.Error) {
	var env struct {
		OK   int `json:"ok"`
		Data *struct {
			UID         json.RawMessage `json:"uid"`
			IDStr       string          `json:"idstr"`
			ScreenName  string          `json:"screen_name"`
			Domain      string          `json:"domain"`
			Description string          `json:"description"`
			Gender      string          `json:"gender"`
			Province    string          `json:"province"`
			City        string          `json:"city"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析微博登录确认响应失败",
			Err:      err,
		}
	}
	if env.OK != 1 || env.Data == nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "该 Cookie 未处于已登录状态或已过期，请从已登录 weibo.com 的浏览器重新复制",
		}
	}

	screenName := strings.TrimSpace(env.Data.ScreenName)
	if screenName == "" {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "微博未返回当前账号昵称（可能为未登录或访客会话），请确认已登录 weibo.com 后重新复制 Cookie",
		}
	}

	// getBasicInfo typically omits uid; if present, validate it as a positive 5..19 digit UID.
	rawUID := strings.TrimSpace(env.Data.IDStr)
	if rawUID == "" {
		rawUID = strings.TrimSpace(rawNumberOrString(env.Data.UID))
	}
	normUID := ""
	if rawUID != "" && rawUID != "0" {
		if n, err := site.NormalizeUID(site.Weibo, rawUID); err == nil {
			normUID = n
		}
	}

	return &BasicInfo{
		UID:         normUID,
		ScreenName:  screenName,
		Domain:      strings.TrimSpace(env.Data.Domain),
		Description: strings.TrimSpace(env.Data.Description),
		Gender:      strings.TrimSpace(env.Data.Gender),
		Province:    strings.TrimSpace(env.Data.Province),
		City:        strings.TrimSpace(env.Data.City),
	}, nil
}

func verifySelfProfileMatch(basic *BasicInfo, prof *UserProfile, endpoint string) *site.Error {
	if basic == nil || prof == nil {
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认微博登录账号信息，本次未保存新 Cookie",
		}
	}
	if _, err := site.NormalizeUID(site.Weibo, prof.UID); err != nil {
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认微博登录账号的有效 UID，本次未保存新 Cookie",
		}
	}
	// Strict screen_name identity match per weibo.md §13.1 #2.
	if strings.TrimSpace(prof.ScreenName) == "" || strings.TrimSpace(prof.ScreenName) != strings.TrimSpace(basic.ScreenName) {
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认当前微博登录账号（profile/info 返回昵称与账号设置不一致），本次未保存新 Cookie",
		}
	}
	if basic.UID != "" && prof.UID != basic.UID {
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认当前微博登录账号（UID 核对不一致），本次未保存新 Cookie",
		}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
