package bilibili

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/credential"
)

const bilibiliCorrespondPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDLgd2OAkcGVtoE3ThUREbio0Eg
Uc/prcajMKXvkCKFCWhJYJcLkcM2DKKcSeFpD/j6Boy538YXnR6VhcuUJOhH2x71
nzPjfdTcqMz7djHum0qSZA0AyCBDABUqCrfNgCiJ00Ra7GmRj+YCK1NJEuewlb40
JNrRuoEUXpabUzGB8QIDAQAB
-----END PUBLIC KEY-----`

var reCorrespondDiv = regexp.MustCompile(`<div\s+id=["']1-name["'][^>]*>([^<]+)</div>`)

// GenerateCorrespondPath encrypts "refresh_<timestamp>" using Bilibili's RSA-OAEP SHA-256 public key
// and returns the lowercase hex string for /correspond/1/<hex>.
func GenerateCorrespondPath(tsMillis int64) (string, error) {
	block, _ := pem.Decode([]byte(bilibiliCorrespondPublicKeyPEM))
	if block == nil {
		return "", fmt.Errorf("bilibili: failed to decode RSA public key PEM")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("bilibili: parse RSA public key: %w", err)
	}
	pubKey, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("bilibili: unexpected public key type %T", pubAny)
	}

	plaintext := fmt.Sprintf("refresh_%d", tsMillis)
	cipherBytes, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubKey, []byte(plaintext), nil)
	if err != nil {
		return "", fmt.Errorf("bilibili: RSA-OAEP encrypt: %w", err)
	}
	return hex.EncodeToString(cipherBytes), nil
}

// CheckAndRefreshCookie executes the 5-step Bilibili Cookie renewal flow when needed.
// If a previous run completed step 4 (cookie/refresh) and failed before step 5 (confirm/refresh),
// it resumes directly at step 5 using the new bili_jct and pendingOldRefreshToken (C08).
func (c *Client) CheckAndRefreshCookie(ctx context.Context) (credential.Status, error) {
	ctx, unlock := c.beginSequence(ctx, true)
	defer unlock()
	if err := c.RequireAuth("/x/passport-login/web/cookie/info"); err != nil {
		return c.currentAccountStatus(), err
	}

	st := c.credStore.Status()
	if st.Source == "manual" {
		// DEC-031: Manual cookie login does not auto-renew.
		return st, nil
	}

	refreshToken, confirmPending, pendingOldRT := c.credStore.RefreshState()

	// Resume Step 5 if Step 4 already succeeded earlier (C08).
	if confirmPending && pendingOldRT != "" {
		newCSRF := c.credStore.CSRF()
		if err := c.postConfirmRefresh(ctx, newCSRF, pendingOldRT); err != nil {
			return c.credStore.Status(), err
		}
		if err := c.credStore.CompleteRefreshStep5(); err != nil {
			return c.credStore.Status(), err
		}
		return c.credStore.Status(), nil
	}

	// Step 1: Check /x/passport-login/web/cookie/info
	oldCSRF := c.credStore.CSRF()
	infoURL := strings.TrimRight(c.endpoints.PassportBase, "/") + "/x/passport-login/web/cookie/info?csrf=" + url.QueryEscape(oldCSRF)
	var infoData struct {
		Refresh   bool  `json:"refresh"`
		Timestamp int64 `json:"timestamp"`
	}
	if _, err := c.doBiliJSON(ctx, http.MethodGet, infoURL, nil, "", true, &infoData); err != nil {
		return c.credStore.Status(), err
	}

	if !infoData.Refresh {
		return c.credStore.MarkChecked()
	}

	if strings.TrimSpace(refreshToken) == "" {
		st, stateErr := c.credStore.MarkState("refresh_failed", "站点提示需要续期 Cookie，但当前缺少 refresh_token，请重新扫码或补充导入")
		return st, errors.Join(&SiteError{
			Kind:     ErrKindAuthRequired,
			Endpoint: infoURL,
			Message:  "缺少 refresh_token，无法自动续期 Cookie",
		}, stateErr)
	}

	// Step 2: Generate correspondPath via RSA-OAEP SHA-256
	correspondPath, err := GenerateCorrespondPath(infoData.Timestamp)
	if err != nil {
		return c.credStore.Status(), err
	}

	// Step 3: Fetch refresh_csrf from /correspond/1/<correspondPath>
	corrURL := strings.TrimRight(c.endpoints.WWWBase, "/") + "/correspond/1/" + correspondPath
	_, corrRaw, err := c.doAPIRequest(ctx, http.MethodGet, corrURL, nil, "", true)
	if err != nil {
		return c.credStore.Status(), err
	}
	m := reCorrespondDiv.FindSubmatch(corrRaw)
	if len(m) < 2 {
		return c.credStore.Status(), &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: corrURL,
			Message:  "无法从 correspond 页面提取 refresh_csrf",
		}
	}
	refreshCSRF := strings.TrimSpace(string(m[1]))

	// Step 4: POST /x/passport-login/web/cookie/refresh
	form := url.Values{}
	form.Set("csrf", oldCSRF)
	form.Set("refresh_csrf", refreshCSRF)
	form.Set("source", "main_web")
	form.Set("refresh_token", refreshToken)

	refreshURL := strings.TrimRight(c.endpoints.PassportBase, "/") + "/x/passport-login/web/cookie/refresh"
	var refreshData struct {
		Status       int    `json:"status"`
		Message      string `json:"message"`
		RefreshToken string `json:"refresh_token"`
	}
	resp, err := c.doBiliJSON(
		ctx,
		http.MethodPost,
		refreshURL,
		strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded",
		true,
		&refreshData,
	)
	if err != nil {
		var se *SiteError
		if errors.As(err, &se) && (se.Code == -101 || se.Code == 86095) {
			expSt, stateErr := c.credStore.MarkState("expired", "Cookie 自动续期失效，请重新扫码登录")
			return expSt, errors.Join(err, stateErr)
		}
		st, stateErr := c.credStore.MarkState("refresh_failed", "Cookie 自动续期失败，将在下一周期重试")
		return st, errors.Join(err, stateErr)
	}

	newCookies := credential.ExtractSetCookies(resp.Cookies())
	if len(newCookies) == 0 || newCookies["SESSDATA"] == "" || newCookies["bili_jct"] == "" {
		return c.credStore.Status(), &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: refreshURL,
			Message:  "cookie/refresh 响应未下发新的 SESSDATA 或 bili_jct",
		}
	}
	if refreshData.RefreshToken == "" {
		return c.credStore.Status(), &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: refreshURL,
			Message:  "cookie/refresh 响应未返回新的 refresh_token",
		}
	}
	if refreshData.Status != 0 {
		if refreshData.Status == 86095 {
			c.expireAuth()
		}
		return c.credStore.Status(), &SiteError{Kind: ErrKindAuthRequired, Code: refreshData.Status, Message: "站点未确认 Cookie 续期成功"}
	}
	// Set-Cookie can omit unchanged identity fields. Validate the merged candidate
	// in memory before replacing the encrypted original record.
	merged := c.credStore.Cookies()
	for key, value := range newCookies {
		merged[key] = value
	}
	mid, _, _, err := c.ValidateCandidateCookies(ctx, merged)
	if err != nil {
		return c.credStore.Status(), err
	}
	if st.Mid > 0 && mid != st.Mid {
		return c.credStore.Status(), &SiteError{Kind: ErrKindSchemaChanged, Message: "续期得到的账号与原登录账号不一致，原凭证已保留"}
	}

	// Atomically persist new cookies + new refresh_token + confirm_pending=true + pending_old_refresh_token=refreshToken
	if err := c.credStore.CommitRefreshStep4(newCookies, refreshData.RefreshToken, refreshToken); err != nil {
		return c.credStore.Status(), err
	}

	// Step 5: POST /x/passport-login/web/confirm/refresh using NEW bili_jct and OLD refresh_token (C08)
	newCSRF := newCookies["bili_jct"]
	if err := c.postConfirmRefresh(ctx, newCSRF, refreshToken); err != nil {
		// Step 4 is already persisted; next startup/check will retry Step 5 directly.
		return c.credStore.Status(), err
	}

	if err := c.credStore.CompleteRefreshStep5(); err != nil {
		return c.credStore.Status(), err
	}
	return c.credStore.Status(), nil
}

func (c *Client) postConfirmRefresh(ctx context.Context, newCSRF, oldRefreshToken string) error {
	form := url.Values{}
	form.Set("csrf", newCSRF)
	form.Set("refresh_token", oldRefreshToken)

	confirmURL := strings.TrimRight(c.endpoints.PassportBase, "/") + "/x/passport-login/web/confirm/refresh"
	_, err := c.doBiliJSON(
		ctx,
		http.MethodPost,
		confirmURL,
		strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded",
		true,
		nil,
	)
	return err
}
