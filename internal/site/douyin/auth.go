package douyin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/guard"
)

var (
	reValidSecUID = regexp.MustCompile(`^MS4wLjAB[A-Za-z0-9_-]{16,}$`)
	reDigitsOnly  = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// SelfProfile represents the authenticated user profile verified by /aweme/v1/web/user/profile/self/.
type SelfProfile struct {
	UID       string
	SecUID    string
	UniqueID  string
	ShortID   string
	Nickname  string
	AvatarURL string
}

type profileSelfEnvelope struct {
	StatusCode int              `json:"status_code"`
	StatusMsg  string           `json:"status_msg"`
	User       *rawDouyinAuthor `json:"user"`
}

// LoginWithCookie cleans and validates a user-pasted Douyin Cookie via /aweme/v1/web/user/profile/self/
// before persisting it to the credential store.
//
// Failure modes (never echoing raw Cookie values and never overwriting existing credentials):
//  1. Missing sessionid/sessionid_ss (including ttwid/msToken-only guest cookies), >64 KiB raw input, or >32 KiB cleaned header -> invalid_input (0 requests)
//  2. profile/self returns unauthenticated status_code or empty/incomplete user -> auth_required
//  3. profile/self hits captcha / gateway verification -> risk_control
func (c *Client) LoginWithCookie(ctx context.Context, rawCookie string) (credential.Status, error) {
	cleaned, err := credential.ParseDouyinCookieInput(rawCookie)
	if err != nil {
		if errors.Is(err, credential.ErrInputTooLarge) {
			return credential.Status{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindInvalidInput,
				Message: "粘贴的 Cookie 长度超过 64 KiB 上限，请只复制抖音站点的 Cookie",
			}
		}
		if errors.Is(err, credential.ErrCleanedCookieTooLarge) {
			return credential.Status{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindInvalidInput,
				Message: "清洗后的抖音 Cookie 请求头长度超过 32 KiB 上限，请只复制抖音站点的 Cookie",
			}
		}
		return credential.Status{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindInvalidInput,
			Message: "未检测到有效的抖音登录会话字段（需包含 sessionid 或 sessionid_ss；仅含 ttwid 或 msToken 属于未登录访客状态）",
		}
	}

	candGuard, _ := guard.GenerateState(c.entropy, c.nowFn)
	var candGuardPtr *guard.State
	if candGuard.HasKeyPair() {
		candGuardPtr = &candGuard
	}
	profile, refreshedCookies, usedMSToken, syncedWebID, sentTTWIDHash, err := c.verifyCandidateCookies(ctx, cleaned, candGuardPtr)
	if err != nil {
		return credential.Status{}, err
	}

	if c.cred == nil {
		if candGuardPtr != nil {
			c.guardMu.Lock()
			cp := *candGuardPtr
			c.ephemeralGuard = &cp
			c.guardMu.Unlock()
		}
		c.applyLoginSyncedWebID(sentTTWIDHash, hashTTWID(refreshedCookies["ttwid"]), syncedWebID)
		if strings.TrimSpace(refreshedCookies["msToken"]) == "" && usedMSToken != "" {
			c.setFallbackMSToken(cookieMapSessionFingerprint(refreshedCookies), usedMSToken)
		} else {
			c.clearFallbackMSToken()
		}
		return credential.Status{
			Site:     credential.SiteDouyin,
			LoggedIn: true,
			State:    "logged_in",
			UID:      profile.UID,
			SecUID:   profile.SecUID,
			UniqueID: profile.UniqueID,
			Uname:    profile.Nickname,
			Face:     profile.AvatarURL,
		}, nil
	}

	var credGuard *credential.DouyinGuardState
	if candGuardPtr != nil {
		credGuard = toCredGuardState(*candGuardPtr)
	}
	st, saveErr := c.cred.SaveVerifiedDouyinManualWithGuard(refreshedCookies, credential.DouyinProfile{
		UID:      profile.UID,
		SecUID:   profile.SecUID,
		UniqueID: profile.UniqueID,
		Uname:    profile.Nickname,
		Face:     profile.AvatarURL,
	}, credGuard)
	if saveErr != nil {
		return credential.Status{}, saveErr
	}
	c.applyLoginSyncedWebID(sentTTWIDHash, hashTTWID(refreshedCookies["ttwid"]), syncedWebID)
	if strings.TrimSpace(refreshedCookies["msToken"]) == "" && usedMSToken != "" {
		c.setFallbackMSToken(cookieMapSessionFingerprint(refreshedCookies), usedMSToken)
	} else {
		c.clearFallbackMSToken()
	}
	return st, nil
}

// VerifyCredential re-verifies the currently stored Douyin credential via /aweme/v1/web/user/profile/self/.
// Risk-control challenges do not clear or expire the stored credential.
func (c *Client) VerifyCredential(ctx context.Context) (credential.Status, error) {
	if c.cred == nil {
		return credential.Status{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先在设置中粘贴 Cookie 登录抖音",
		}
	}
	if err := c.RequireAuth(c.endpoints.ProfileSelf); err != nil {
		return c.cred.Status(), err
	}

	prof, err := c.GetSelfProfile(ctx)
	if err != nil {
		return c.cred.Status(), err
	}

	st, uErr := c.cred.UpdateDouyinProfile(credential.DouyinProfile{
		UID:      prof.UID,
		SecUID:   prof.SecUID,
		UniqueID: prof.UniqueID,
		Uname:    prof.Nickname,
		Face:     prof.AvatarURL,
	})
	if uErr != nil {
		return c.cred.Status(), uErr
	}
	return st, nil
}

// LogoutCredential clears the stored Douyin credential and invalidates cached live room mappings.
func (c *Client) LogoutCredential() error {
	var clearErr error
	if c.cred != nil {
		clearErr = c.cred.Clear()
	}
	c.clearFallbackMSToken()
	c.clearSyncedWebID()
	c.clearEphemeralGuard()

	c.liveCacheMu.Lock()
	c.webRIDByUser = make(map[string]liveWebRIDEntry)
	c.liveCacheMu.Unlock()

	return clearErr
}

// GetSelfProfile fetches and strictly validates the currently logged-in user's profile via profile/self.
func (c *Client) GetSelfProfile(ctx context.Context) (*SelfProfile, error) {
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.ProfileSelf,
		referer:     DefaultReferer,
		params:      c.profile.CommonWebParams(c.webID, ""),
		requireSign: true,
	})
	if err != nil {
		return nil, err
	}
	prof, pErr := parseStrictSelfProfile(rawResp.body, c.endpoints.ProfileSelf, c.allowLocalhost)
	if pErr != nil {
		c.onClassifiedError(ctx, pErr, true)
		return nil, pErr
	}
	c.markWebIDWarmupTried(rawResp.ttwidHash, rawResp.webIDGen)
	return prof, nil
}

// CheckProfileSelfGuest performs an unauthenticated control request to profile/self (used only by ACT-010 V-01).
// It succeeds when profile/self rejects the unauthenticated request as expected (auth_required).
func (c *Client) CheckProfileSelfGuest(ctx context.Context) error {
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:      c.endpoints.ProfileSelf,
		referer:       DefaultReferer,
		params:        c.profile.CommonWebParams(c.webID, ""),
		requireSign:   true,
		guestNoCookie: true,
	})
	if err != nil {
		return err
	}
	_, pErr := parseStrictSelfProfile(rawResp.body, c.endpoints.ProfileSelf, c.allowLocalhost)
	if pErr != nil {
		return pErr
	}
	return nil
}

func (c *Client) verifyCandidateCookies(ctx context.Context, cleaned map[string]string, candidateGuard *guard.State) (*SelfProfile, map[string]string, string, string, string, error) {
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:        c.endpoints.ProfileSelf,
		referer:         DefaultReferer,
		params:          c.profile.CommonWebParams(c.webID, ""),
		requireSign:     true,
		candidateCookie: cleaned,
		candidateGuard:  candidateGuard,
	})
	if err != nil {
		if site.IsKind(err, site.ErrKindRiskControl) || site.IsKind(err, site.ErrKindRateLimited) {
			return nil, nil, "", "", "", &site.Error{
				Site:     site.Douyin,
				Kind:     site.KindOf(err),
				Endpoint: c.endpoints.ProfileSelf,
				Message:  "抖音触发安全验证或访问受限，本次未保存新 Cookie，请稍后或在浏览器正常访问抖音后再试",
			}
		}
		if site.IsKind(err, site.ErrKindAuthRequired) {
			return nil, nil, "", "", "", &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: c.endpoints.ProfileSelf,
				Message:  "该 Cookie 未处于已登录状态或已过期，请从已登录抖音网页的浏览器重新复制",
			}
		}
		return nil, nil, "", "", "", err
	}

	prof, pErr := parseStrictSelfProfile(rawResp.body, c.endpoints.ProfileSelf, c.allowLocalhost)
	if pErr != nil {
		return nil, nil, "", "", "", pErr
	}

	merged := make(map[string]string, len(cleaned))
	for k, v := range cleaned {
		merged[k] = v
	}
	if rawResp.header != nil {
		resp := &http.Response{Header: rawResp.header}
		for k, v := range credential.ExtractDouyinSetCookies(resp.Cookies()) {
			merged[k] = v
		}
		if xms := strings.TrimSpace(rawResp.header.Get("X-Ms-Token")); xms != "" {
			merged["msToken"] = xms
		}
	}
	return prof, merged, rawResp.sentMSToken, rawResp.syncedWebID, rawResp.ttwidHash, nil
}

func parseStrictSelfProfile(body []byte, endpoint string, allowLocalhost bool) (*SelfProfile, *site.Error) {
	var env profileSelfEnvelope
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "解析抖音当前账号资料失败",
			Err:      err,
		}
	}
	if env.StatusCode != 0 || env.User == nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindAuthRequired,
			Code:     env.StatusCode,
			Endpoint: endpoint,
			Message:  "该 Cookie 未处于已登录状态或已过期，请从已登录抖音网页的浏览器重新复制",
		}
	}

	uid := strings.TrimSpace(env.User.uidString())
	secUID := strings.TrimSpace(env.User.SecUID)
	nickname := strings.TrimSpace(env.User.Nickname)

	// Condition 3 (douyin.md §3.2): uid numeric != "0", sec_uid matches ^MS4wLjAB[A-Za-z0-9_-]{16,}$, nickname non-empty.
	if uid == "" || uid == "0" || !reDigitsOnly.MatchString(uid) ||
		!reValidSecUID.MatchString(secUID) || nickname == "" {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "抖音未返回完整登录账号信息（需包含有效的 uid、sec_uid 与昵称），请确认已登录抖音网页后重新复制 Cookie",
		}
	}

	uniqueID := strings.TrimSpace(env.User.UniqueID)
	shortID := strings.TrimSpace(env.User.ShortID)
	if uniqueID == "" && shortID != "" && shortID != "0" {
		uniqueID = shortID
	}

	return &SelfProfile{
		UID:       uid,
		SecUID:    secUID,
		UniqueID:  uniqueID,
		ShortID:   shortID,
		Nickname:  nickname,
		AvatarURL: pickAuthorAvatar(env.User, allowLocalhost),
	}, nil
}
