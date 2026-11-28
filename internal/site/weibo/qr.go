package weibo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qrcode "github.com/yeqown/go-qrcode/v2"
	_ "golang.org/x/image/webp"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

const (
	qrSessionTTL            = 180 * time.Second
	maxQRImageBytes         = 256 * 1024 // 256 KiB cap for fetched QR code images
	maxQRImageDimension     = 2048       // bounds PNG/JPEG/GIF pixel dimensions against decompression bombs
	maxCrossDomainURLs      = 12
	maxCrossDomainRedirects = 2
	maxQRHistoryEntries     = 64
)

// QRSession is returned when starting a Weibo PC Web SSO QR code login flow.
// Transient secrets such as qrid and remote image URLs are kept internal and never exposed.
type QRSession struct {
	FlowID    string    `json:"flowId"`
	PNG       []byte    `json:"png"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// QRPollResult represents the current state of a Weibo QR code login flow.
type QRPollResult struct {
	FlowID  string            `json:"flowId"`
	State   string            `json:"state"` // "waiting", "scanned", "confirmed", "expired", "canceled"
	Message string            `json:"message"`
	Account credential.Status `json:"account"`
}

type qrFlowState struct {
	FlowID    string
	QRID      string
	ExpiresAt time.Time
	Polling   bool
}

type qrController struct {
	mu       sync.Mutex
	revision uint64
	active   *qrFlowState
	history  map[string]string
	cbSeq    atomic.Uint64
}

// Official Weibo/Sina passport hosts strictly permitted for SSO QR login endpoints and crossDomainUrlList.
var allowedWeiboSSOHosts = []string{
	"passport.weibo.com",
	"login.sina.com.cn",
	"passport.weibo.cn",
	"weibo.com",
	"www.weibo.com",
}

// Exact hosts permitted for fetching remote Weibo QR image bytes (in addition to *.weibo.cn / *.weibo.com).
var allowedWeiboQRImageExactHosts = []string{
	"v2.qr.weibo.cn",
	"qr.weibo.cn",
	"passport.weibo.com",
	"passport.weibo.cn",
	"login.sina.com.cn",
	"weibo.com",
	"www.weibo.com",
	"weibo.cn",
}

func newQRHTTPClient(opts ClientOptions, timeout time.Duration, noRedirect func(*http.Request, []*http.Request) error) *http.Client {
	tr := opts.Transport
	if tr == nil {
		dialer := &net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}
		guard := site.NewGuardedMediaDialer(site.Weibo, opts.AllowLocalhost, dialer, "已拦截微博扫码请求指向内网或回环地址")
		tr = &http.Transport{
			Proxy:                 guard.Proxy,
			DialContext:           guard.DialContext,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: timeout,
		}
		if opts.AllowLocalhost {
			tr = site.LoopbackTransport(tr)
		}
	}
	if opts.WrapTransport != nil {
		tr = opts.WrapTransport(tr)
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: noRedirect,
	}
}

func (c *Client) nextJSONPCallback() string {
	seq := c.qr.cbSeq.Add(1)
	return fmt.Sprintf("STK_%d%04d", c.nowFn().UnixMilli(), seq%10000)
}

func (c *Client) currentAccountStatus() credential.Status {
	if c.cred == nil {
		return credential.Status{Site: credential.SiteWeibo, LoggedIn: false, State: "logged_out"}
	}
	return c.cred.Status()
}

func (c *Client) recordQRHistoryLocked(flowID, state string) {
	if flowID == "" {
		return
	}
	if c.qr.history == nil {
		c.qr.history = make(map[string]string)
	}
	if len(c.qr.history) >= maxQRHistoryEntries {
		for k := range c.qr.history {
			if c.qr.active != nil && k == c.qr.active.FlowID {
				continue
			}
			delete(c.qr.history, k)
			if len(c.qr.history) < maxQRHistoryEntries/2 {
				break
			}
		}
	}
	c.qr.history[flowID] = state
}

func (c *Client) isQRFlowActive(flowID string) bool {
	c.qr.mu.Lock()
	defer c.qr.mu.Unlock()
	return c.qr.active != nil && c.qr.active.FlowID == flowID && c.qr.history[flowID] == "active"
}

// StartQR requests a fresh Weibo SSO v2 login QR code, downloads or encodes ready-to-render
// PNG image bytes, and invalidates any previously active Weibo QR flow.
func (c *Client) StartQR(ctx context.Context) (QRSession, error) {
	c.qr.mu.Lock()
	c.qr.revision++
	revision := c.qr.revision
	if c.qr.active != nil {
		c.recordQRHistoryLocked(c.qr.active.FlowID, "canceled")
		c.qr.active = nil
	}
	c.qr.mu.Unlock()

	cb := c.nextJSONPCallback()
	q := url.Values{}
	q.Set("entry", "miniblog")
	q.Set("size", "180")
	q.Set("callback", cb)

	rawResp, err := c.doSSOGet(ctx, c.endpoints.QRImage, q, "")
	if err != nil {
		return QRSession{}, err
	}
	endpointLabel := site.SanitizeEndpoint(c.endpoints.QRImage)
	payload, err := unwrapJSONP(rawResp.body, cb, endpointLabel)
	if err != nil {
		return QRSession{}, err
	}

	var env struct {
		Retcode json.RawMessage `json:"retcode"`
		Msg     string          `json:"msg"`
		Data    *struct {
			QRID      string `json:"qrid"`
			Image     string `json:"image"`
			URL       string `json:"url"`
			QRURL     string `json:"qr_url"`
			QRCodeURL string `json:"qrcode_url"`
		} `json:"data"`
	}
	if err := decodeStrictJSON(payload, &env); err != nil {
		return QRSession{}, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博二维码生成响应失败",
			Err:      err,
		}
	}
	retcode := strings.TrimSpace(rawNumberOrString(env.Retcode))
	if retcode != "20000000" {
		return QRSession{}, classifySSOBusinessError(endpointLabel, retcode, env.Msg, "微博获取登录二维码失败")
	}
	if env.Data == nil || strings.TrimSpace(env.Data.QRID) == "" {
		return QRSession{}, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博二维码接口未返回有效的会话标识",
		}
	}

	qrid := strings.TrimSpace(env.Data.QRID)
	imgURL := strings.TrimSpace(env.Data.Image)
	scanURI := firstNonEmpty(env.Data.URL, env.Data.QRURL, env.Data.QRCodeURL)

	var pngBytes []byte
	switch {
	case imgURL != "":
		pngBytes, err = c.fetchQRImagePNG(ctx, imgURL)
		if err != nil {
			return QRSession{}, err
		}
	case scanURI != "":
		pngBytes, err = RenderQRPNG(scanURI, 6)
		if err != nil {
			return QRSession{}, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindSchemaChanged,
				Endpoint: endpointLabel,
				Message:  "生成微博登录二维码图片失败",
				Err:      err,
			}
		}
	default:
		return QRSession{}, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博二维码接口未返回二维码图片或扫码地址",
		}
	}

	var rnd [8]byte
	if c.entropy != nil {
		_, _ = io.ReadFull(c.entropy, rnd[:])
	} else {
		_, _ = rand.Read(rnd[:])
	}
	flowID := "wbqr-" + hex.EncodeToString(rnd[:])
	expiresAt := c.nowFn().Add(qrSessionTTL)

	c.qr.mu.Lock()
	if revision != c.qr.revision || ctx.Err() != nil {
		c.qr.mu.Unlock()
		return QRSession{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "微博扫码流程已取消或替换",
		}
	}
	c.qr.active = &qrFlowState{
		FlowID:    flowID,
		QRID:      qrid,
		ExpiresAt: expiresAt,
	}
	c.recordQRHistoryLocked(flowID, "active")
	c.qr.mu.Unlock()

	return QRSession{
		FlowID:    flowID,
		PNG:       pngBytes,
		ExpiresAt: expiresAt,
	}, nil
}

// CancelQR cancels the Weibo QR login session identified by flowID (or the active session if flowID is empty).
func (c *Client) CancelQR(flowID string) {
	c.qr.mu.Lock()
	defer c.qr.mu.Unlock()
	if flowID == "" || (c.qr.active != nil && c.qr.active.FlowID == flowID) {
		c.qr.revision++
	}
	if flowID != "" {
		c.recordQRHistoryLocked(flowID, "canceled")
	}
	if c.qr.active != nil && (flowID == "" || c.qr.active.FlowID == flowID) {
		c.recordQRHistoryLocked(c.qr.active.FlowID, "canceled")
		c.qr.active = nil
	}
}

// PollQR polls the status of the Weibo QR login session identified by flowID.
// Late responses for canceled, expired, or superseded flowIDs are dropped and never touch stored credentials.
func (c *Client) PollQR(ctx context.Context, flowID string) (QRPollResult, error) {
	c.qr.mu.Lock()
	histState := c.qr.history[flowID]
	active := c.qr.active
	if active != nil && active.FlowID == flowID && active.Polling {
		c.qr.mu.Unlock()
		return QRPollResult{
			FlowID:  flowID,
			State:   "waiting",
			Message: "正在检查扫码状态",
			Account: c.currentAccountStatus(),
		}, nil
	}
	claimedPoll := false
	if active != nil && active.FlowID == flowID {
		active.Polling = true
		claimedPoll = true
	}
	c.qr.mu.Unlock()
	defer func() {
		if !claimedPoll {
			return
		}
		c.qr.mu.Lock()
		active.Polling = false
		c.qr.mu.Unlock()
	}()

	if histState == "canceled" || active == nil || active.FlowID != flowID {
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "扫码流程已取消",
			Account: c.currentAccountStatus(),
		}, nil
	}

	if c.nowFn().After(active.ExpiresAt) {
		c.qr.mu.Lock()
		if c.qr.active == active {
			c.recordQRHistoryLocked(flowID, "expired")
			c.qr.active = nil
		}
		c.qr.mu.Unlock()
		return QRPollResult{
			FlowID:  flowID,
			State:   "expired",
			Message: "二维码已过期，请刷新二维码",
			Account: c.currentAccountStatus(),
		}, nil
	}

	cb := c.nextJSONPCallback()
	q := url.Values{}
	q.Set("entry", "miniblog")
	q.Set("qrid", active.QRID)
	q.Set("callback", cb)

	rawResp, err := c.doSSOGet(ctx, c.endpoints.QRCheck, q, "")
	if err != nil {
		return QRPollResult{}, err
	}

	// Re-check whether flowID was canceled or superseded while the poll request was in flight.
	if !c.isQRFlowActive(flowID) {
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "扫码流程已取消",
			Account: c.currentAccountStatus(),
		}, nil
	}

	endpointLabel := site.SanitizeEndpoint(c.endpoints.QRCheck)
	payload, err := unwrapJSONP(rawResp.body, cb, endpointLabel)
	if err != nil {
		return QRPollResult{}, err
	}

	var env struct {
		Retcode json.RawMessage `json:"retcode"`
		Msg     string          `json:"msg"`
		Data    *struct {
			Alt string `json:"alt"`
		} `json:"data"`
	}
	if err := decodeStrictJSON(payload, &env); err != nil {
		return QRPollResult{}, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博扫码状态响应失败",
			Err:      err,
		}
	}

	retcode := strings.TrimSpace(rawNumberOrString(env.Retcode))
	switch retcode {
	case "50114001":
		return QRPollResult{
			FlowID:  flowID,
			State:   "waiting",
			Message: "请使用微博 App 扫码登录",
			Account: c.currentAccountStatus(),
		}, nil
	case "50114002":
		return QRPollResult{
			FlowID:  flowID,
			State:   "scanned",
			Message: "已扫码，请在微博 App 确认登录",
			Account: c.currentAccountStatus(),
		}, nil
	case "50114004":
		c.qr.mu.Lock()
		if c.qr.active == active {
			c.recordQRHistoryLocked(flowID, "expired")
			c.qr.active = nil
		}
		c.qr.mu.Unlock()
		return QRPollResult{
			FlowID:  flowID,
			State:   "expired",
			Message: "二维码已过期，请刷新二维码",
			Account: c.currentAccountStatus(),
		}, nil
	case "50114003", "50114015":
		c.qr.mu.Lock()
		if c.qr.active == active {
			c.recordQRHistoryLocked(flowID, "canceled")
			c.qr.active = nil
		}
		c.qr.mu.Unlock()
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "已在手机端取消或扫码会话中断，请重新获取二维码",
			Account: c.currentAccountStatus(),
		}, nil
	case "20000000":
		alt := ""
		if env.Data != nil {
			alt = strings.TrimSpace(env.Data.Alt)
		}
		if alt == "" {
			return QRPollResult{}, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindSchemaChanged,
				Endpoint: endpointLabel,
				Message:  "微博扫码确认成功但缺少临时登录票据，原凭证已保留",
			}
		}
		return c.completeQRLogin(ctx, flowID, active, alt)
	default:
		return QRPollResult{}, classifySSOBusinessError(endpointLabel, retcode, env.Msg, "微博扫码状态接口返回异常状态")
	}
}

func (c *Client) completeQRLogin(ctx context.Context, flowID string, active *qrFlowState, alt string) (QRPollResult, error) {
	collected, ssoUID, canceled, err := c.exchangeQRAltForCookies(ctx, flowID, alt)
	if err != nil {
		return QRPollResult{}, err
	}
	if canceled || !c.isQRFlowActive(flowID) {
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "扫码流程已取消",
			Account: c.currentAccountStatus(),
		}, nil
	}

	cleaned := credential.CleanWeiboCookieMap(collected)
	if cleaned["SUB"] == "" {
		return QRPollResult{}, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.QRSsoLogin),
			Message:  "微博扫码跨域登录未获取到有效凭证（缺少 SUB），原凭证已保留",
		}
	}

	// Strictly verify login on PC weibo.com (getBasicInfo + profile/info) and capture XSRF-TOKEN.
	prof, mergedCookies, verifyErr := c.verifyCandidateCookies(ctx, cleaned)
	if verifyErr != nil {
		return QRPollResult{}, verifyErr
	}
	if ssoUID != "" && prof.UID != ssoUID {
		return QRPollResult{}, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindAuthRequired,
			Endpoint: site.SanitizeEndpoint(c.endpoints.PCBasicInfo),
			Message:  "微博扫码登录返回的账号 UID 与登录校验结果不一致，原凭证已保留",
		}
	}

	// Final race-free commit check: canceled, expired, or superseded sessions must never overwrite credentials.
	c.qr.mu.Lock()
	stillValid := c.qr.active != nil &&
		c.qr.active.FlowID == flowID &&
		c.qr.history[flowID] == "active" &&
		!c.nowFn().After(active.ExpiresAt) &&
		ctx.Err() == nil
	if !stillValid {
		c.qr.mu.Unlock()
		return QRPollResult{
			FlowID:  flowID,
			State:   "canceled",
			Message: "扫码流程已取消",
			Account: c.currentAccountStatus(),
		}, nil
	}

	var st credential.Status
	if c.cred != nil {
		var saveErr error
		st, saveErr = c.cred.SaveVerifiedWeiboQR(mergedCookies, credential.WeiboProfile{
			UID:   prof.UID,
			Uname: prof.ScreenName,
			Face:  prof.AvatarURL,
		})
		if saveErr != nil {
			c.qr.mu.Unlock()
			return QRPollResult{}, fmt.Errorf("weibo: save qr credential: %w", saveErr)
		}
	} else {
		st = credential.Status{
			Site:     credential.SiteWeibo,
			LoggedIn: true,
			State:    "logged_in",
			Source:   "qr",
			UID:      prof.UID,
			Uname:    prof.ScreenName,
			Face:     prof.AvatarURL,
		}
	}
	c.recordQRHistoryLocked(flowID, "completed")
	c.qr.active = nil
	c.qr.mu.Unlock()

	return QRPollResult{
		FlowID:  flowID,
		State:   "confirmed",
		Message: "扫码登录成功",
		Account: st,
	}, nil
}

func (c *Client) exchangeQRAltForCookies(ctx context.Context, flowID, alt string) (map[string]string, string, bool, error) {
	cb := c.nextJSONPCallback()
	q := url.Values{}
	q.Set("entry", "miniblog")
	q.Set("returntype", "TEXT")
	q.Set("crossdomain", "1")
	q.Set("cdult", "3")
	q.Set("domain", "weibo.com")
	q.Set("alt", alt)
	q.Set("savestate", "30")
	q.Set("callback", cb)

	rawResp, err := c.doSSOGet(ctx, c.endpoints.QRSsoLogin, q, "")
	if err != nil {
		return nil, "", false, err
	}
	if !c.isQRFlowActive(flowID) {
		return nil, "", true, nil
	}

	collected := make(map[string]string)
	if loginURL, parseErr := url.Parse(strings.TrimSpace(c.endpoints.QRSsoLogin)); parseErr == nil {
		mergeWeiboDomainSetCookies(collected, loginURL, rawResp.header, c.allowLocalhost)
	}

	endpointLabel := site.SanitizeEndpoint(c.endpoints.QRSsoLogin)
	payload, err := unwrapJSONP(rawResp.body, cb, endpointLabel)
	if err != nil {
		return nil, "", false, err
	}

	var loginResp struct {
		Retcode            json.RawMessage `json:"retcode"`
		Reason             string          `json:"reason"`
		Msg                string          `json:"msg"`
		UID                json.RawMessage `json:"uid"`
		Nick               string          `json:"nick"`
		CrossDomainURLList []string        `json:"crossDomainUrlList"`
		ArrURL             []string        `json:"arrURL"`
	}
	if err := decodeStrictJSON(payload, &loginResp); err != nil {
		return nil, "", false, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解析微博 SSO 登录换票响应失败",
			Err:      err,
		}
	}

	retcode := strings.TrimSpace(rawNumberOrString(loginResp.Retcode))
	if retcode != "0" && retcode != "20000000" {
		return nil, "", false, classifySSOBusinessError(endpointLabel, retcode, firstNonEmpty(loginResp.Reason, loginResp.Msg), "微博 SSO 票据登录失败")
	}

	var ssoUID string
	if rawUID := strings.TrimSpace(rawNumberOrString(loginResp.UID)); rawUID != "" && rawUID != "0" {
		if norm, nErr := site.NormalizeUID(site.Weibo, rawUID); nErr == nil {
			ssoUID = norm
		}
	}

	// Merge crossDomainUrlList and arrURL in order without duplicates.
	seenURL := make(map[string]bool)
	candidates := make([]string, 0, len(loginResp.CrossDomainURLList)+len(loginResp.ArrURL))
	for _, list := range [][]string{loginResp.CrossDomainURLList, loginResp.ArrURL} {
		for _, raw := range list {
			u := strings.TrimSpace(raw)
			if u == "" || seenURL[u] {
				continue
			}
			seenURL[u] = true
			candidates = append(candidates, u)
		}
	}

	visited := 0
	for _, rawTarget := range candidates {
		if visited >= maxCrossDomainURLs {
			break
		}
		targetURL, valErr := validateSSOCrossDomainURL(rawTarget, c.allowLocalhost)
		if valErr != nil {
			// Strictly skip any non-allowlisted, non-HTTPS, non-443, or userinfo URL.
			continue
		}
		if !c.isQRFlowActive(flowID) {
			return nil, "", true, nil
		}
		visited++
		if err := c.visitCrossDomainURL(ctx, flowID, targetURL, collected); err != nil {
			return nil, "", false, err
		}
	}

	return collected, ssoUID, false, nil
}

func (c *Client) visitCrossDomainURL(ctx context.Context, flowID string, startURL *url.URL, collected map[string]string) error {
	curURL := startURL
	for hop := 0; hop <= maxCrossDomainRedirects; hop++ {
		if !c.isQRFlowActive(flowID) {
			return nil
		}
		cookieHdr := ""
		if isWeiboComCookieHost(curURL.Hostname(), c.allowLocalhost) && len(collected) > 0 {
			cookieHdr = formatCookieHeader(collected)
		}
		rawResp, err := c.doSSOGetAllowRedirect(ctx, curURL.String(), nil, cookieHdr)
		if err != nil {
			return err
		}
		mergeWeiboDomainSetCookies(collected, curURL, rawResp.header, c.allowLocalhost)

		if rawResp.statusCode >= 300 && rawResp.statusCode < 400 {
			loc := strings.TrimSpace(rawResp.header.Get("Location"))
			if loc == "" {
				return nil
			}
			lowerLoc := strings.ToLower(loc)
			if isRiskRedirectLocation(lowerLoc) {
				return &site.Error{
					Site:       site.Weibo,
					Kind:       site.ErrKindRiskControl,
					HTTPStatus: rawResp.statusCode,
					Endpoint:   sanitizeEndpointLabel(curURL),
					Message:    "微博跨域登录跳转至安全验证页，已立即停止请求",
				}
			}
			resolved, rErr := curURL.Parse(loc)
			if rErr != nil {
				return nil
			}
			nextURL, valErr := validateSSOCrossDomainURL(resolved.String(), c.allowLocalhost)
			if valErr != nil {
				// Never follow redirects to non-allowlisted hosts.
				return nil
			}
			curURL = nextURL
			continue
		}
		return nil
	}
	return nil
}

func (c *Client) doSSOGet(ctx context.Context, rawEndpoint string, query url.Values, cookieHeader string) (*apiRawResponse, error) {
	rawResp, err := c.doSSOGetAllowRedirect(ctx, rawEndpoint, query, cookieHeader)
	if err != nil {
		return nil, err
	}
	if rawResp.statusCode >= 300 && rawResp.statusCode < 400 {
		endpointLabel := site.SanitizeEndpoint(rawEndpoint)
		loc := strings.ToLower(strings.TrimSpace(rawResp.header.Get("Location")))
		if isRiskRedirectLocation(loc) || isVisitorRedirectLocation(loc) {
			return nil, &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: rawResp.statusCode,
				Endpoint:   endpointLabel,
				Message:    "微博扫码接口跳转至安全验证页，已立即停止请求",
			}
		}
		return nil, &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: rawResp.statusCode,
			Endpoint:   endpointLabel,
			Message:    fmt.Sprintf("微博扫码接口返回异常跳转（HTTP %d）", rawResp.statusCode),
		}
	}
	return rawResp, nil
}

func (c *Client) doSSOGetAllowRedirect(ctx context.Context, rawEndpoint string, query url.Values, cookieHeader string) (*apiRawResponse, error) {
	parsedURL, err := validateSSOCrossDomainURL(rawEndpoint, c.allowLocalhost)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		q := parsedURL.Query()
		for k, vals := range query {
			q.Del(k)
			for _, v := range vals {
				q.Add(k, v)
			}
		}
		parsedURL.RawQuery = q.Encode()
	}

	endpointLabel := sanitizeEndpointLabel(parsedURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindBadRequest,
			Endpoint: endpointLabel,
			Message:  "构造微博扫码请求失败",
			Err:      site.UnwrapURLError(err),
		}
	}
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("Sec-Ch-Ua", c.profile.SecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", c.profile.SecCHUAMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", c.profile.SecCHUAPlatform)
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Del("Cookie")
	}

	resp, err := c.qrClient.Do(req)
	if err != nil {
		unwrapped := site.UnwrapURLError(err)
		if site.IsKind(unwrapped, site.ErrKindBlocked) {
			return nil, unwrapped
		}
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindNetwork,
			Endpoint: endpointLabel,
			Message:  "请求微博扫码服务失败，请检查网络连接",
			Err:      unwrapped,
		}
	}
	defer resp.Body.Close()

	body, err := readBoundedBody(resp.Body, c.maxBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindBlocked,
				HTTPStatus: resp.StatusCode,
				Endpoint:   endpointLabel,
				Message:    "微博扫码响应体积超出安全上限",
			}
		}
		return nil, &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Endpoint:   endpointLabel,
			Message:    "读取微博扫码响应失败",
			Err:        err,
		}
	}

	if se := classifySSOHTTPStatus(endpointLabel, resp.StatusCode); se != nil {
		return nil, se
	}
	return &apiRawResponse{
		statusCode: resp.StatusCode,
		header:     resp.Header.Clone(),
		body:       body,
	}, nil
}

func classifySSOHTTPStatus(endpoint string, statusCode int) *site.Error {
	switch {
	case statusCode == http.StatusOK || (statusCode >= 300 && statusCode < 400):
		return nil
	case statusCode == http.StatusForbidden || statusCode == http.StatusTeapot || statusCode == 432:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博扫码触发安全验证或风控拦截（HTTP %d），已立即停止请求", statusCode),
		}
	case statusCode == http.StatusRequestURITooLong || statusCode == http.StatusTooManyRequests:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRateLimited,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博扫码请求过于频繁（HTTP %d），请稍后再试", statusCode),
		}
	case statusCode >= 500:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博通行证服务暂时不可用（HTTP %d）", statusCode),
		}
	default:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博扫码接口返回异常状态码 HTTP %d", statusCode),
		}
	}
}

func classifySSOBusinessError(endpoint, retcode, msg, fallback string) *site.Error {
	msg = strings.TrimSpace(msg)
	codeInt, _ := strconv.Atoi(retcode)
	if isRiskMessage(msg) {
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Code:     codeInt,
			Endpoint: endpoint,
			Message:  "微博扫码触发安全验证或风控拦截，已停止请求",
		}
	}
	if isRateLimitMessage(msg) {
		return &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRateLimited,
			Code:     codeInt,
			Endpoint: endpoint,
			Message:  "微博扫码请求过于频繁，请稍后再试",
		}
	}
	outMsg := fallback
	if retcode != "" {
		outMsg = fmt.Sprintf("%s（retcode=%s）", fallback, retcode)
	}
	return &site.Error{
		Site:     site.Weibo,
		Kind:     site.ErrKindSchemaChanged,
		Code:     codeInt,
		Endpoint: endpoint,
		Message:  outMsg,
	}
}

func (c *Client) fetchQRImagePNG(ctx context.Context, rawURL string) ([]byte, error) {
	parsedURL, err := validateQRImageURL(rawURL, c.allowLocalhost)
	if err != nil {
		return nil, err
	}
	endpointLabel := sanitizeEndpointLabel(parsedURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindBadRequest,
			Endpoint: endpointLabel,
			Message:  "无效的微博二维码图片地址",
			Err:      site.UnwrapURLError(err),
		}
	}
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "image/png,image/webp,image/*,*/*;q=0.8")
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Del("Cookie")

	resp, err := c.qrClient.Do(req)
	if err != nil {
		unwrapped := site.UnwrapURLError(err)
		if site.IsKind(unwrapped, site.ErrKindBlocked) {
			return nil, unwrapped
		}
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindNetwork,
			Endpoint: endpointLabel,
			Message:  "拉取微博二维码图片失败，请检查网络连接",
			Err:      unwrapped,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if se := classifySSOHTTPStatus(endpointLabel, resp.StatusCode); se != nil {
			return nil, se
		}
		return nil, &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: resp.StatusCode,
			Endpoint:   endpointLabel,
			Message:    fmt.Sprintf("微博二维码图片返回 HTTP %d", resp.StatusCode),
		}
	}
	if resp.ContentLength > maxQRImageBytes {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindBlocked,
			Endpoint: endpointLabel,
			Message:  "微博二维码图片体积超出 256 KiB 安全上限",
		}
	}

	raw, err := readBoundedBody(resp.Body, maxQRImageBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindBlocked,
				Endpoint: endpointLabel,
				Message:  "微博二维码图片体积超出 256 KiB 安全上限",
			}
		}
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindNetwork,
			Endpoint: endpointLabel,
			Message:  "读取微博二维码图片失败",
			Err:      err,
		}
	}
	ext, ok := site.SniffImageMagicBytes(raw)
	if !ok {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博二维码图片内容不是有效的图像文件",
		}
	}
	cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(raw))
	if cfgErr != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxQRImageDimension || cfg.Height > maxQRImageDimension {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "微博二维码图片尺寸无效或不受支持",
		}
	}
	img, _, decErr := image.Decode(bytes.NewReader(raw))
	if decErr != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "解码微博二维码图片失败",
			Err:      decErr,
		}
	}
	if ext == ".png" {
		return raw, nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpointLabel,
			Message:  "转换微博二维码图片为 PNG 失败",
			Err:      err,
		}
	}
	return buf.Bytes(), nil
}

// ValidateSSOCrossDomainURL validates that rawURL is a safe HTTPS:443 URL with no userinfo
// belonging strictly to the official Weibo/Sina passport host allowlist.
func validateSSOCrossDomainURL(rawURL string, allowLocalhost bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截空的微博通行证地址",
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不安全的微博通行证地址",
		}
	}
	host := strings.ToLower(u.Hostname())
	if allowLocalhost && site.IsLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return u, nil
		}
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非 HTTPS 微博通行证请求",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非常规端口微博通行证请求",
		}
	}
	if !isExactHostInList(host, allowedWeiboSSOHosts) {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "微博通行证域名不在允许列表中: " + host,
		}
	}
	return u, nil
}

func validateQRImageURL(rawURL string, allowLocalhost bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if strings.HasPrefix(trimmed, "//") {
		trimmed = "https:" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不安全的微博二维码图片地址",
		}
	}
	host := strings.ToLower(u.Hostname())
	if allowLocalhost && site.IsLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return u, nil
		}
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非 HTTPS 微博二维码图片地址",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非常规端口微博二维码图片地址",
		}
	}
	if !isAllowedQRImageHost(host) {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "微博二维码图片域名不在允许列表中: " + host,
		}
	}
	return u, nil
}

func isAllowedQRImageHost(host string) bool {
	if isExactHostInList(host, allowedWeiboQRImageExactHosts) {
		return true
	}
	if strings.HasSuffix(host, ".weibo.cn") && len(host) > len(".weibo.cn") && !strings.HasPrefix(host, ".") {
		return true
	}
	if strings.HasSuffix(host, ".weibo.com") && len(host) > len(".weibo.com") && !strings.HasPrefix(host, ".") {
		return true
	}
	return false
}

func isWeiboComCookieHost(host string, allowLocalhost bool) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "weibo.com" || h == "www.weibo.com" || h == "passport.weibo.com" || strings.HasSuffix(h, ".weibo.com") {
		return true
	}
	return allowLocalhost && site.IsLoopbackHost(h)
}

func isWeiboComCookieDomain(domain string) bool {
	d := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	return d == "weibo.com" || strings.HasSuffix(d, ".weibo.com")
}

func mergeWeiboDomainSetCookies(dst map[string]string, reqURL *url.URL, hdr http.Header, allowLocalhost bool) {
	if dst == nil || reqURL == nil || hdr == nil {
		return
	}
	host := reqURL.Hostname()
	hostIsWeiboCom := isWeiboComCookieHost(host, allowLocalhost)
	resp := &http.Response{Header: hdr}
	raw := make(map[string]string)
	for _, ck := range resp.Cookies() {
		if ck == nil || ck.Value == "" || ck.MaxAge < 0 || strings.EqualFold(strings.TrimSpace(ck.Value), "deleted") {
			continue
		}
		if ck.Domain != "" {
			if !isWeiboComCookieDomain(ck.Domain) {
				continue
			}
		} else if !hostIsWeiboCom {
			continue
		}
		raw[ck.Name] = ck.Value
	}
	for k, v := range credential.CleanWeiboCookieMap(raw) {
		dst[k] = v
	}
}

func unwrapJSONP(body []byte, expectedCallback, endpoint string) ([]byte, error) {
	trimmed := bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	trimmed = bytes.TrimSpace(trimmed)
	if len(trimmed) == 0 {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindRiskControl,
			Endpoint: endpoint,
			Message:  "微博扫码接口返回空响应，疑似触发拦截",
		}
	}

	// Detect disguised HTML verification / captcha pages before JSONP parsing.
	if trimmed[0] == '<' {
		return nil, classifyNonJSONFake200(endpoint, http.StatusOK, trimmed, false)
	}

	trimmed = bytes.TrimRight(trimmed, "; \t\r\n")
	if len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' {
		return trimmed, nil
	}

	if expectedCallback == "" || !isSafeJSONPCallback(expectedCallback) {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindBadRequest,
			Endpoint: endpoint,
			Message:  "非法的 JSONP 回调标识",
		}
	}

	// Allow strict "STK_xxx({...})" or "window.STK_xxx && STK_xxx({...})".
	rest := trimmed
	winPrefix := []byte("window." + expectedCallback)
	if bytes.HasPrefix(rest, winPrefix) {
		after := bytes.TrimSpace(rest[len(winPrefix):])
		if !bytes.HasPrefix(after, []byte("&&")) {
			return nil, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindSchemaChanged,
				Endpoint: endpoint,
				Message:  "微博扫码 JSONP 回调前缀校验失败",
			}
		}
		rest = bytes.TrimSpace(after[2:])
	}

	callPrefix := []byte(expectedCallback + "(")
	if !bytes.HasPrefix(rest, callPrefix) || !bytes.HasSuffix(rest, []byte(")")) {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "微博扫码 JSONP 回调格式校验失败",
		}
	}
	inner := bytes.TrimSpace(rest[len(callPrefix) : len(rest)-1])
	if len(inner) < 2 || inner[0] != '{' || inner[len(inner)-1] != '}' {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: endpoint,
			Message:  "微博扫码 JSONP 负载不是有效 JSON 对象",
		}
	}
	return inner, nil
}

func isSafeJSONPCallback(cb string) bool {
	if !strings.HasPrefix(cb, "STK_") || len(cb) <= len("STK_") {
		return false
	}
	for i := len("STK_"); i < len(cb); i++ {
		if cb[i] < '0' || cb[i] > '9' {
			return false
		}
	}
	return true
}

func decodeStrictJSON(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing tokens after JSON object")
	}
	return nil
}

// RenderQRPNG encodes a Weibo QR login URI into PNG bytes using go-qrcode/v2.
func RenderQRPNG(text string, scale int) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("登录二维码内容为空")
	}
	if scale < 2 || scale > 16 {
		scale = 6
	}
	code, err := qrcode.NewWith(text, qrcode.WithEncodingMode(qrcode.EncModeByte))
	if err != nil {
		return nil, errors.New("登录二维码内容无法编码")
	}
	writer := &weiboQRBitmapWriter{scale: scale}
	if err := code.Save(writer); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, writer.image); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type weiboQRBitmapWriter struct {
	scale int
	image *image.RGBA
}

func (w *weiboQRBitmapWriter) Close() error { return nil }

func (w *weiboQRBitmapWriter) Write(matrix qrcode.Matrix) error {
	const border = 4
	w.image = image.NewRGBA(image.Rect(0, 0, (matrix.Width()+2*border)*w.scale, (matrix.Height()+2*border)*w.scale))
	draw.Draw(w.image, w.image.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	black := image.NewUniform(color.Black)
	matrix.Iterate(qrcode.IterDirection_ROW, func(x, y int, value qrcode.QRValue) {
		if value.IsSet() {
			r := image.Rect((x+border)*w.scale, (y+border)*w.scale, (x+border+1)*w.scale, (y+border+1)*w.scale)
			draw.Draw(w.image, r, black, image.Point{}, draw.Src)
		}
	})
	return nil
}
