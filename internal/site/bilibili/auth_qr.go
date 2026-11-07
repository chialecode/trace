package bilibili

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
)

// QRSession is returned when starting a QR code login flow.
type QRSession struct {
	FlowID    string    `json:"flowId"`
	URL       string    `json:"url"`
	PNG       []byte    `json:"png"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// QRPollResult represents the current state of a QR login flow.
type QRPollResult struct {
	FlowID  string            `json:"flowId"`
	State   string            `json:"state"` // "waiting", "scanned", "confirmed", "expired", "canceled"
	Message string            `json:"message"`
	Account credential.Status `json:"account"`
}

// StartQR requests a new login QR code from Bilibili Passport, renders its PNG,
// and invalidates any previously active QR flow.
func (c *Client) StartQR(ctx context.Context) (QRSession, error) {
	ctx, unlock := c.beginSequence(ctx, false)
	defer unlock()
	c.qrMu.Lock()
	c.qrRevision++
	revision := c.qrRevision
	if c.activeQR != nil {
		c.qrHistory[c.activeQR.FlowID] = "canceled"
		c.activeQR = nil
	}
	c.qrMu.Unlock()
	fullURL := strings.TrimRight(c.endpoints.PassportBase, "/") + "/x/passport-login/web/qrcode/generate"
	var data struct {
		URL       string `json:"url"`
		QRCodeKey string `json:"qrcode_key"`
	}
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", false, &data); err != nil {
		return QRSession{}, err
	}
	if data.URL == "" || data.QRCodeKey == "" {
		return QRSession{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
			Message:  "二维码生成接口未返回有效的 url 或 qrcode_key",
		}
	}

	pngBytes, err := RenderQRPNG(data.URL, 6)
	if err != nil {
		return QRSession{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
			Message:  "渲染登录二维码失败",
			Err:      err,
		}
	}

	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	flowID := "qr-" + hex.EncodeToString(rnd[:])
	expiresAt := c.nowFunc().Add(180 * time.Second)

	c.qrMu.Lock()
	if revision != c.qrRevision || ctx.Err() != nil {
		c.qrMu.Unlock()
		return QRSession{}, &SiteError{Kind: ErrKindBadRequest, Message: "扫码流程已取消或替换"}
	}
	c.activeQR = &qrFlowState{
		FlowID:    flowID,
		QRCodeKey: data.QRCodeKey,
		URL:       data.URL,
		PNG:       pngBytes,
		ExpiresAt: expiresAt,
	}
	c.qrHistory[flowID] = "active"
	c.qrMu.Unlock()

	return QRSession{
		FlowID:    flowID,
		URL:       data.URL,
		PNG:       pngBytes,
		ExpiresAt: expiresAt,
	}, nil
}

// CancelQR cancels the QR login session identified by flowID (or the active session if flowID is empty).
func (c *Client) CancelQR(flowID string) {
	c.qrMu.Lock()
	defer c.qrMu.Unlock()
	if flowID == "" || (c.activeQR != nil && c.activeQR.FlowID == flowID) {
		c.qrRevision++
	}
	if flowID != "" {
		c.qrHistory[flowID] = "canceled"
	}
	if c.activeQR != nil && (flowID == "" || c.activeQR.FlowID == flowID) {
		c.qrHistory[c.activeQR.FlowID] = "canceled"
		c.activeQR = nil
	}
}

// PollQR polls the status of the QR login session identified by flowID.
// Late responses for canceled or superseded flowIDs are ignored and never write credentials (C06).
func (c *Client) PollQR(ctx context.Context, flowID string) (QRPollResult, error) {
	ctx, unlock := c.beginSequence(ctx, false)
	defer unlock()
	c.qrMu.Lock()
	histState := c.qrHistory[flowID]
	active := c.activeQR
	if active != nil && active.FlowID == flowID && active.Polling {
		c.qrMu.Unlock()
		return QRPollResult{FlowID: flowID, State: "waiting", Message: "正在检查扫码状态"}, nil
	}
	if active != nil && active.FlowID == flowID {
		active.Polling = true
	}
	c.qrMu.Unlock()
	defer func() {
		c.qrMu.Lock()
		if active != nil {
			active.Polling = false
		}
		c.qrMu.Unlock()
	}()

	if histState == "canceled" || active == nil || active.FlowID != flowID {
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "扫码流程已取消",
			Account: c.currentAccountStatus(),
		}, nil
	}

	if c.nowFunc().After(active.ExpiresAt) {
		return QRPollResult{
			FlowID:  flowID,
			State:   "expired",
			Message: "二维码已过期，请刷新重试",
			Account: c.currentAccountStatus(),
		}, nil
	}

	q := url.Values{}
	q.Set("qrcode_key", active.QRCodeKey)
	fullURL := strings.TrimRight(c.endpoints.PassportBase, "/") + "/x/passport-login/web/qrcode/poll?" + q.Encode()

	var pollData struct {
		URL          string `json:"url"`
		RefreshToken string `json:"refresh_token"`
		Timestamp    int64  `json:"timestamp"`
		Code         int    `json:"code"`
		Message      string `json:"message"`
	}
	resp, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", false, &pollData)
	if err != nil {
		return QRPollResult{}, err
	}

	// Re-check whether flowID was canceled while the HTTP request was in flight!
	c.qrMu.Lock()
	stillActive := c.activeQR != nil && c.activeQR.FlowID == flowID && c.qrHistory[flowID] == "active"
	c.qrMu.Unlock()
	if !stillActive {
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "扫码流程已取消",
			Account: c.currentAccountStatus(),
		}, nil
	}

	switch pollData.Code {
	case 86101:
		return QRPollResult{
			FlowID:  flowID,
			State:   "waiting",
			Message: "请使用哔哩哔哩手机客户端扫码登录",
			Account: c.currentAccountStatus(),
		}, nil
	case 86090:
		return QRPollResult{
			FlowID:  flowID,
			State:   "scanned",
			Message: "已扫码，请在手机端点击确认登录",
			Account: c.currentAccountStatus(),
		}, nil
	case 86038:
		return QRPollResult{
			FlowID:  flowID,
			State:   "expired",
			Message: "二维码已过期，请刷新二维码",
			Account: c.currentAccountStatus(),
		}, nil
	case 0:
		cookies := credential.ExtractSetCookies(resp.Cookies())
		if len(cookies) == 0 && pollData.URL != "" {
			// Fallback: extract query params from pollData.URL if Set-Cookie wasn't used.
			if u, pErr := url.Parse(pollData.URL); pErr == nil {
				rawMap := make(map[string]string)
				for k, vals := range u.Query() {
					if len(vals) > 0 {
						rawMap[k] = vals[0]
					}
				}
				cookies = credential.FilterCookieMap(rawMap)
			}
		}

		if cookies["SESSDATA"] == "" || cookies["bili_jct"] == "" {
			return QRPollResult{}, &SiteError{Kind: ErrKindSchemaChanged, Message: "扫码结果缺少有效凭证，原凭证已保留"}
		}
		mid, uname, face, verifyErr := c.ValidateCandidateCookies(ctx, cookies)
		if verifyErr != nil {
			return QRPollResult{}, verifyErr
		}

		c.qrMu.Lock()
		if c.activeQR == nil || c.activeQR.FlowID != flowID || c.qrHistory[flowID] != "active" || !c.nowFunc().Before(active.ExpiresAt) || ctx.Err() != nil {
			c.qrMu.Unlock()
			return QRPollResult{
				FlowID:  flowID,
				State:   "canceled",
				Message: "扫码流程已取消",
				Account: c.currentAccountStatus(),
			}, nil
		}
		var st credential.Status
		c.prepareAccountReplacement(mid)
		if c.credStore != nil {
			var saveErr error
			st, saveErr = c.credStore.SaveQR(cookies, pollData.RefreshToken, mid, uname, face)
			if saveErr != nil {
				c.qrMu.Unlock()
				return QRPollResult{}, fmt.Errorf("bilibili: save qr credential: %w", saveErr)
			}
		}
		c.qrHistory[flowID] = "completed"
		c.activeQR = nil
		c.qrMu.Unlock()
		c.ClearCaches()

		return QRPollResult{
			FlowID:  flowID,
			State:   "confirmed",
			Message: "扫码登录成功",
			Account: st,
		}, nil
	default:
		return QRPollResult{
			FlowID:  flowID,
			State:   "waiting",
			Message: "站点尚未确认扫码状态，请稍后重试",
			Account: c.currentAccountStatus(),
		}, nil
	}
}

func (c *Client) currentAccountStatus() credential.Status {
	if c.credStore == nil {
		return credential.Status{LoggedIn: false, State: "logged_out"}
	}
	return c.credStore.Status()
}
