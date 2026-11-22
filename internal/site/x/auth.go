package x

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

var reValidXScreenName = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// SelfProfile holds the strictly verified account identity for the signed-in X user.
type SelfProfile struct {
	RestID     string `json:"restId"`
	ScreenName string `json:"screenName"`
	Name       string `json:"name"`
	AvatarURL  string `json:"avatarUrl,omitempty"`
}

// AccountSettings holds the parsed fields from GET api.x.com/1.1/account/settings.json.
type AccountSettings struct {
	ScreenName string `json:"screenName"`
	Protected  bool   `json:"protected,omitempty"`
	Language   string `json:"language,omitempty"`
}

// LoginWithCookie parses, cleans, and strictly verifies a user-pasted x.com Cookie using
// the 2-step confirmation flow (x.md §2.4 & §15.1 #6) before persisting it to x.bin:
//
//  0. Local pre-check via credential.ParseXCookieInput: requires non-empty auth_token and
//     valid alphanumeric ct0 (0 network requests on missing/invalid/oversized/control-char input;
//     raw Cookie input is never echoed).
//  1. GET https://api.x.com/1.1/account/settings.json (no redirect follow, Bearer + x-csrf-token
//     + x-twitter-auth-type: OAuth2Session + cleaned Cookie): requires HTTP 200, valid JSON,
//     empty errors[], and screen_name matching ^[A-Za-z0-9_]{1,15}$.
//  2. GraphQL UserByScreenName(screen_name): extracts rest_id, screen_name, name, avatar;
//     verifies screen_name matches Step 1 case-insensitively; if candidate cookies contain twid,
//     decodes twid via credential.ParseXTWID and requires twidRestID == rest_id.
//
// On any failure (auth_required, rate_limited, risk_control, mismatch, schema_changed, network),
// existing saved credentials are NEVER modified or cleared.
func (c *Client) LoginWithCookie(ctx context.Context, rawCookie string) (credential.Status, error) {
	prof, mergedCookies, err := c.VerifyCookie(ctx, rawCookie)
	if err != nil {
		return credential.Status{}, err
	}

	if c.cred == nil {
		return credential.Status{
			Site:     credential.SiteX,
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      prof.RestID,
			UniqueID: prof.ScreenName,
			Uname:    prof.Name,
			Face:     prof.AvatarURL,
		}, nil
	}

	return c.cred.SaveVerifiedXManual(mergedCookies, credential.XProfile{
		RestID:     prof.RestID,
		ScreenName: prof.ScreenName,
		Name:       prof.Name,
		Face:       prof.AvatarURL,
	})
}

// VerifyCookie validates and strictly verifies a raw pasted X Cookie string via the 2-step
// confirmation flow WITHOUT mutating the credential store. Safe for desktop login pre-check
// and remote server PUT credentials validation.
func (c *Client) VerifyCookie(ctx context.Context, rawCookie string) (*SelfProfile, map[string]string, error) {
	cleaned, err := parseXCookieInputError(rawCookie)
	if err != nil {
		return nil, nil, err
	}
	return c.verifyCandidateCookies(ctx, cleaned)
}

// VerifyCredential re-verifies the currently stored X credential using the strict 2-step flow.
// risk_control and rate_limited NEVER clear or expire the stored credential.
func (c *Client) VerifyCredential(ctx context.Context) (credential.Status, error) {
	if c.cred == nil {
		return credential.Status{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录 X",
		}
	}
	if err := c.RequireAuth(c.endpoints.AccountSettings); err != nil {
		return c.cred.Status(), err
	}

	prof, err := c.GetSelfProfile(ctx)
	if err != nil {
		return c.cred.Status(), err
	}

	st, uErr := c.cred.UpdateXProfile(credential.XProfile{
		RestID:     prof.RestID,
		ScreenName: prof.ScreenName,
		Name:       prof.Name,
		Face:       prof.AvatarURL,
	})
	if uErr != nil {
		return c.cred.Status(), uErr
	}
	return st, nil
}

// LogoutCredential clears the stored X credential.
func (c *Client) LogoutCredential() error {
	if c.cred == nil {
		return nil
	}
	return c.cred.Clear()
}

// GetSelfProfile verifies the currently active stored X credential via:
//  1. GET api.x.com/1.1/account/settings.json
//  2. GraphQL UserByScreenName(screen_name) + screen_name & optional twid cross-check
func (c *Client) GetSelfProfile(ctx context.Context) (*SelfProfile, error) {
	if err := c.RequireAuth(c.endpoints.AccountSettings); err != nil {
		return nil, err
	}
	meta, err := c.ensureMetadataForRequest(ctx, nil)
	if err != nil {
		return nil, err
	}

	rawSettings, err := c.doAPIRequest(ctx, apiRequestSpec{
		method:        http.MethodGet,
		operation:     OpAccountSettings,
		endpoint:      c.endpoints.AccountSettings,
		bearerHeader:  meta.BearerHeader(),
		referer:       DefaultReferer,
		origin:        DefaultOrigin,
		beforeAttempt: txidBeforeAttempt(meta),
	})
	if err != nil {
		return nil, err
	}

	settings, parseErr := parseStrictAccountSettings(rawSettings.body, site.SanitizeEndpoint(c.endpoints.AccountSettings))
	if parseErr != nil {
		c.onClassifiedError(ctx, parseErr, true)
		return nil, parseErr
	}

	prof, err := c.GetUserByScreenName(ctx, settings.ScreenName)
	if err != nil {
		return nil, err
	}

	var storedTWID string
	if c.cred != nil {
		storedTWID = c.cred.CookieValue("twid")
	}
	if matchErr := verifySelfProfileMatch(settings, prof, storedTWID, site.SanitizeEndpoint(c.endpoints.GraphQLBase+"/"+OpUserByScreenName)); matchErr != nil {
		c.onClassifiedError(ctx, matchErr, false)
		return nil, matchErr
	}

	return &SelfProfile{
		RestID:     prof.RestID,
		ScreenName: prof.ScreenName,
		Name:       firstNonEmpty(prof.Name, prof.ScreenName),
		AvatarURL:  prof.AvatarURL,
	}, nil
}

// CheckSettingsGuest runs an unauthenticated control request against api.x.com/1.1/account/settings.json
// with NO Cookie, NO x-csrf-token, and NO x-twitter-auth-type: OAuth2Session header (M7-K04 / ACT-024 V-01).
func (c *Client) CheckSettingsGuest(ctx context.Context) (*AccountSettings, error) {
	var bearer string
	var meta *FrontendMetadata
	if c.discovery != nil {
		if meta = c.discovery.Current(); meta != nil {
			bearer = meta.BearerHeader()
		}
	}
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		method:          http.MethodGet,
		operation:       OpAccountSettings,
		endpoint:        c.endpoints.AccountSettings,
		bearerHeader:    bearer,
		referer:         DefaultReferer,
		origin:          DefaultOrigin,
		guestNoCookie:   true,
		skipStoreExpire: true,
		beforeAttempt:   txidBeforeAttempt(meta),
	})
	if err != nil {
		return nil, err
	}
	settings, parseErr := parseStrictAccountSettings(rawResp.body, site.SanitizeEndpoint(c.endpoints.AccountSettings))
	if parseErr != nil {
		return nil, parseErr
	}
	return settings, nil
}

// CheckSettingsWithCookies probes api.x.com/1.1/account/settings.json with explicit candidate cookies
// (including guest-only cookies such as guest_id alone) WITHOUT mutating the credential store.
func (c *Client) CheckSettingsWithCookies(ctx context.Context, cookies map[string]string) (*AccountSettings, error) {
	carrier := newCandidateCookieCarrier(cookies)
	var bearer string
	var meta *FrontendMetadata
	if c.discovery != nil {
		if meta = c.discovery.Current(); meta != nil {
			bearer = meta.BearerHeader()
		}
	}
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		method:          http.MethodGet,
		operation:       OpAccountSettings,
		endpoint:        c.endpoints.AccountSettings,
		bearerHeader:    bearer,
		referer:         DefaultReferer,
		origin:          DefaultOrigin,
		candidate:       carrier,
		skipStoreExpire: true,
		beforeAttempt:   txidBeforeAttempt(meta),
	})
	if err != nil {
		return nil, err
	}
	settings, parseErr := parseStrictAccountSettings(rawResp.body, site.SanitizeEndpoint(c.endpoints.AccountSettings))
	if parseErr != nil {
		return nil, parseErr
	}
	return settings, nil
}

func (c *Client) verifyCandidateCookies(ctx context.Context, cleaned map[string]string) (*SelfProfile, map[string]string, error) {
	// If twid is present in the pasted cookie, validate its syntax upfront (still 0 requests if malformed).
	rawTWID := strings.TrimSpace(cleaned["twid"])
	if rawTWID != "" {
		if _, err := credential.ParseXTWID(rawTWID); err != nil {
			return nil, nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindAuthRequired,
				Message: "无法核对 Cookie 中的 twid 身份标识，本次未保存新 Cookie",
			}
		}
	}

	carrier := newCandidateCookieCarrier(cleaned)

	// Discover Bearer / queryId / txid using candidate cookies in memory (never mutates c.cred!).
	meta, err := c.ensureMetadataForRequest(ctx, carrier)
	if err != nil {
		return nil, nil, classifyCandidateLoginError(err, site.SanitizeEndpoint(c.endpoints.HomeURL))
	}

	// Step 1: GET api.x.com/1.1/account/settings.json with candidate cookies (supports 353 single replay).
	rawSettings, err := c.doAPIRequest(ctx, apiRequestSpec{
		method:          http.MethodGet,
		operation:       OpAccountSettings,
		endpoint:        c.endpoints.AccountSettings,
		bearerHeader:    meta.BearerHeader(),
		referer:         DefaultReferer,
		origin:          DefaultOrigin,
		candidate:       carrier,
		skipStoreExpire: true,
		beforeAttempt:   txidBeforeAttempt(meta),
	})
	if err != nil {
		return nil, nil, classifyCandidateLoginError(err, site.SanitizeEndpoint(c.endpoints.AccountSettings))
	}

	settings, parseErr := parseStrictAccountSettings(rawSettings.body, site.SanitizeEndpoint(c.endpoints.AccountSettings))
	if parseErr != nil {
		return nil, nil, parseErr
	}

	// Step 2: GraphQL UserByScreenName(screen_name) with the exact same candidate cookie carrier.
	prof, err := c.getUserByScreenNameWithCandidate(ctx, settings.ScreenName, carrier)
	if err != nil {
		return nil, nil, classifyCandidateLoginError(err, site.SanitizeEndpoint(c.endpoints.GraphQLBase+"/"+OpUserByScreenName))
	}

	merged := carrier.snapshot()
	if matchErr := verifySelfProfileMatch(settings, prof, merged["twid"], site.SanitizeEndpoint(c.endpoints.GraphQLBase+"/"+OpUserByScreenName)); matchErr != nil {
		return nil, nil, matchErr
	}

	return &SelfProfile{
		RestID:     prof.RestID,
		ScreenName: prof.ScreenName,
		Name:       firstNonEmpty(prof.Name, prof.ScreenName),
		AvatarURL:  prof.AvatarURL,
	}, merged, nil
}

func parseXCookieInputError(rawCookie string) (map[string]string, error) {
	cleaned, err := credential.ParseXCookieInput(rawCookie)
	if err == nil {
		return cleaned, nil
	}
	switch {
	case errors.Is(err, credential.ErrInputTooLarge):
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindInvalidInput,
			Message: "粘贴的 Cookie 长度超过 8 KiB 上限，请只复制 x.com 站点的 Cookie",
		}
	case errors.Is(err, credential.ErrInvalidCookieControlChar):
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindInvalidInput,
			Message: "粘贴的 Cookie 包含非法控制字符或换行，请重新从浏览器复制",
		}
	case errors.Is(err, credential.ErrMissingXAuthToken):
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindInvalidInput,
			Message: "未检测到有效的 X 登录字段（需包含 auth_token 与 ct0），请从已登录 x.com 的浏览器重新复制 Cookie",
		}
	case errors.Is(err, credential.ErrMissingXCT0), errors.Is(err, credential.ErrInvalidXCT0):
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindInvalidInput,
			Message: "未检测到有效的 X CSRF 令牌（ct0），请从已登录 x.com 的浏览器重新复制 Cookie",
		}
	default:
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindInvalidInput,
			Message: "粘贴的 X Cookie 格式无效，请从已登录 x.com 的浏览器重新复制",
		}
	}
}

func parseStrictAccountSettings(body []byte, endpoint string) (*AccountSettings, *site.Error) {
	if errs := ExtractXErrors(body); len(errs) > 0 {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindAuthRequired,
			Code:     errs[0].Code,
			Endpoint: endpoint,
			Message:  "X 登录确认响应包含错误，当前 Cookie 未处于有效登录状态，本次未保存",
		}
	}

	var raw struct {
		ScreenName string `json:"screen_name"`
		Protected  bool   `json:"protected"`
		Language   string `json:"language"`
	}
	if err := decodeJSONUseNumber(body, &raw); err != nil {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "X 登录确认响应格式无效，无法确认已登录状态，本次未保存",
			Err:      err,
		}
	}

	screenName := strings.TrimSpace(raw.ScreenName)
	if !reValidXScreenName.MatchString(screenName) {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "X 未返回有效的账号用户名（可能为未登录或访客会话），请确认已登录 x.com 后重新复制 Cookie",
		}
	}

	return &AccountSettings{
		ScreenName: screenName,
		Protected:  raw.Protected,
		Language:   strings.TrimSpace(raw.Language),
	}, nil
}

func verifySelfProfileMatch(settings *AccountSettings, prof *UserProfile, rawTWID string, endpoint string) *site.Error {
	if settings == nil || prof == nil {
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认 X 登录账号信息，本次未保存新 Cookie",
		}
	}
	normRestID, err := site.NormalizeUID(SiteID, prof.RestID)
	if err != nil {
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认 X 登录账号的有效 rest_id，本次未保存新 Cookie",
		}
	}
	if !reValidXScreenName.MatchString(prof.ScreenName) || !strings.EqualFold(prof.ScreenName, settings.ScreenName) {
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "无法确认当前 X 登录账号（UserByScreenName 返回的用户名与账号设置不一致），本次未保存新 Cookie",
		}
	}
	if trimmedTWID := strings.TrimSpace(rawTWID); trimmedTWID != "" {
		twidRestID, tErr := credential.ParseXTWID(trimmedTWID)
		if tErr != nil || twidRestID != normRestID {
			return &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: endpoint,
				Message:  "无法确认当前 X 登录账号（Cookie 中的 twid 与服务端 rest_id 不一致），本次未保存新 Cookie",
			}
		}
	}
	return nil
}

func classifyCandidateLoginError(err error, endpoint string) error {
	if err == nil {
		return nil
	}
	switch site.KindOf(err) {
	case site.ErrKindRateLimited:
		var se *site.Error
		if errors.As(err, &se) && se != nil {
			return se
		}
		return &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: endpoint,
			Message:  "X 请求过于频繁（限流），本次未保存新 Cookie，请稍后再试",
		}
	case site.ErrKindRiskControl:
		var se *site.Error
		code := 0
		httpStatus := 0
		if errors.As(err, &se) && se != nil {
			code = se.Code
			httpStatus = se.HTTPStatus
		}
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	case site.ErrKindAuthRequired:
		var se *site.Error
		code := 0
		httpStatus := 0
		if errors.As(err, &se) && se != nil {
			code = se.Code
			httpStatus = se.HTTPStatus
		}
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "该 Cookie 未处于已登录状态、已过期或仅为访客会话，本次未保存，请从已登录 x.com 的浏览器重新复制",
		}
	case site.ErrKindUnavailable:
		// The login confirmation targets the signed-in account itself, so "not
		// found" (e.g. code 34) means the confirmation endpoint moved, not that a
		// user or post is missing. Report it as a frontend/API change.
		var se *site.Error
		code := 0
		httpStatus := 0
		if errors.As(err, &se) && se != nil {
			code = se.Code
			httpStatus = se.HTTPStatus
		}
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindSchemaChanged,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "X 登录确认接口返回“不存在”，可能是 X 调整了接口地址，本次未保存新 Cookie（Cookie 本身不一定有问题）",
		}
	default:
		return err
	}
}

// txidBeforeAttempt attaches x-client-transaction-id to a REST request, like
// the X web client does on every API call. Without it X answers the REST
// login confirmation with 404 code 34 (seen in real use, 2026-11-23).
// A nil meta or generator leaves the request unchanged.
func txidBeforeAttempt(meta *FrontendMetadata) func(*http.Request) error {
	if meta == nil || meta.TxID == nil {
		return nil
	}
	gen := meta.TxID
	return func(httpReq *http.Request) error {
		txid, err := gen.Generate(httpReq.Method, httpReq.URL.Path)
		if err != nil {
			return err
		}
		httpReq.Header.Set("x-client-transaction-id", txid)
		return nil
	}
}
