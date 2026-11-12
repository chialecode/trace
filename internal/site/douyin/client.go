package douyin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

const (
	defaultMinInterval     = 1000 * time.Millisecond
	defaultJitterMax       = 300 * time.Millisecond
	defaultAPITimeout      = 15 * time.Second
	defaultRedirectTimeout = 10 * time.Second
	defaultMaxBodyBytes    = 4 * 1024 * 1024 // 4 MiB
)

// Endpoints holds configurable Douyin Web and Webcast endpoint URLs for production and httptest.
type Endpoints struct {
	ProfileSelf    string
	ProfileOther   string
	AwemeDetail    string
	AwemePost      string
	LiveWebEnter   string
	LiveReflowInfo string
	LiveCheckAlive string
	// PlayOriginal is the play redirector that serves the untranscoded source (原画)
	// for ratio=default. Defaults to the official host only when AwemeDetail is also
	// the default, so tests pointing the API at a fixture never reach the real site.
	PlayOriginal string
}

// DefaultEndpoints returns the official Douyin HTTPS endpoints.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		ProfileSelf:    "https://www.douyin.com/aweme/v1/web/user/profile/self/",
		ProfileOther:   "https://www.douyin.com/aweme/v1/web/user/profile/other/",
		AwemeDetail:    "https://www.douyin.com/aweme/v1/web/aweme/detail/",
		AwemePost:      "https://www.douyin.com/aweme/v1/web/aweme/post/",
		LiveWebEnter:   "https://live.douyin.com/webcast/room/web/enter/",
		LiveReflowInfo: "https://webcast.amemv.com/webcast/room/reflow/info/",
		LiveCheckAlive: "https://webcast.amemv.com/webcast/room/check_alive/",
		PlayOriginal:   "https://aweme.snssdk.com/aweme/v1/play/",
	}
}

func (e Endpoints) withDefaults() Endpoints {
	d := DefaultEndpoints()
	if e.ProfileSelf == "" {
		e.ProfileSelf = d.ProfileSelf
	}
	if e.ProfileOther == "" {
		e.ProfileOther = d.ProfileOther
	}
	if e.AwemeDetail == "" {
		e.AwemeDetail = d.AwemeDetail
		if e.PlayOriginal == "" {
			e.PlayOriginal = d.PlayOriginal
		}
	}
	if e.AwemePost == "" {
		e.AwemePost = d.AwemePost
	}
	if e.LiveWebEnter == "" {
		e.LiveWebEnter = d.LiveWebEnter
	}
	if e.LiveReflowInfo == "" {
		e.LiveReflowInfo = d.LiveReflowInfo
	}
	if e.LiveCheckAlive == "" {
		e.LiveCheckAlive = d.LiveCheckAlive
	}
	return e
}

// ClientOptions configures a Douyin Client.
type ClientOptions struct {
	CredStore       *credential.Store
	Endpoints       Endpoints
	UserAgent       string
	WebID           string
	Signer          sign.Signer
	MinInterval     time.Duration
	JitterMax       time.Duration
	APITimeout      time.Duration
	RedirectTimeout time.Duration
	MaxBodyBytes    int64
	Transport       http.RoundTripper
	WrapTransport   func(http.RoundTripper) http.RoundTripper
	AllowLocalhost  bool
	Now             func() time.Time
	Sleep           func(ctx context.Context, d time.Duration) error
	Entropy         io.Reader
	PreferredCodec  func() string
}

type liveWebRIDEntry struct {
	webRID           string
	confirmedShortID bool
}

// Client is the Douyin PC Web and Webcast protocol client.
// Outbound API requests are serialized (concurrency = 1) with 1000ms + 0..300ms jitter pacing,
// 15s timeout, 4 MiB body limit, manual redirect handling, and strict Cookie/CDN separation.
type Client struct {
	cred           *credential.Store
	endpoints      Endpoints
	profile        BrowserProfile
	webID          string
	signer         sign.Signer
	httpClient     *http.Client
	redirectClient *http.Client
	mediaClient    *http.Client
	maxBodyBytes   int64
	allowLocalhost bool

	serialGate  chan struct{}
	paceMu      sync.Mutex
	lastReqAt   time.Time
	minInterval time.Duration
	jitterMax   time.Duration
	nowFn       func() time.Time
	sleepFn     func(ctx context.Context, d time.Duration) error
	entropy     io.Reader

	cbMu        sync.RWMutex
	autoBudget  func(ctx context.Context, source string, endpoint string) error
	autoBreaker func(source string, err error)
	authExpired func(reason string)
	prefCodecFn func() string

	liveCacheMu  sync.RWMutex
	webRIDByUser map[string]liveWebRIDEntry
}

// NewClient constructs a configured Douyin Client.
func NewClient(opts ClientOptions) *Client {
	profile := DeriveBrowserProfile(opts.UserAgent)
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	ent := opts.Entropy
	if ent == nil {
		ent = rand.Reader
	}
	sleepFn := opts.Sleep
	if sleepFn == nil {
		sleepFn = defaultContextSleep
	}

	webID := strings.TrimSpace(opts.WebID)
	if webID == "" {
		if gen, err := sign.GenerateWebID(ent); err == nil {
			webID = gen
		} else {
			webID = "7300000000000000001"
		}
	}

	signer := opts.Signer
	if signer == nil {
		signer = sign.NewSigner(sign.Options{
			Now:     nowFn,
			Entropy: ent,
		})
	}

	minInterval := defaultMinInterval
	jitterMax := defaultJitterMax
	if opts.MinInterval < 0 {
		minInterval = 0
		jitterMax = 0
	} else if opts.MinInterval > 0 {
		minInterval = opts.MinInterval
	}
	if opts.JitterMax < 0 {
		jitterMax = 0
	} else if opts.JitterMax > 0 {
		jitterMax = opts.JitterMax
	}

	apiTimeout := opts.APITimeout
	if apiTimeout <= 0 {
		apiTimeout = defaultAPITimeout
	}
	redirTimeout := opts.RedirectTimeout
	if redirTimeout <= 0 {
		redirTimeout = defaultRedirectTimeout
	}
	maxBytes := opts.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}

	baseTransport := opts.Transport
	if baseTransport == nil {
		baseTransport = &http.Transport{
			Proxy:                 site.Proxy,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: apiTimeout,
		}
	}
	if opts.WrapTransport != nil {
		baseTransport = opts.WrapTransport(baseTransport)
	}

	noRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	c := &Client{
		cred:           opts.CredStore,
		endpoints:      opts.Endpoints.withDefaults(),
		profile:        profile,
		webID:          webID,
		signer:         signer,
		maxBodyBytes:   maxBytes,
		allowLocalhost: opts.AllowLocalhost,
		serialGate:     make(chan struct{}, 1),
		minInterval:    minInterval,
		jitterMax:      jitterMax,
		nowFn:          nowFn,
		sleepFn:        sleepFn,
		entropy:        ent,
		prefCodecFn:    opts.PreferredCodec,
		webRIDByUser:   make(map[string]liveWebRIDEntry),
	}
	c.httpClient = &http.Client{
		Transport:     baseTransport,
		Timeout:       apiTimeout,
		CheckRedirect: noRedirect,
	}
	c.redirectClient = &http.Client{
		Transport:     baseTransport,
		Timeout:       redirTimeout,
		CheckRedirect: noRedirect,
	}
	c.mediaClient = &http.Client{
		Transport:     baseTransport,
		Timeout:       apiTimeout,
		CheckRedirect: noRedirect,
	}
	return c
}

func defaultContextSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Profile returns the client's coherent desktop Chrome browser profile.
func (c *Client) Profile() BrowserProfile {
	return c.profile
}

// Endpoints returns the configured endpoint URLs.
func (c *Client) Endpoints() Endpoints {
	return c.endpoints
}

// VODTransport returns the Douyin VOD and image CDN transport policy.
func (c *Client) VODTransport() site.MediaTransportSpec {
	return site.DouyinVODTransport(c.profile.UserAgent, c.allowLocalhost)
}

// LiveTransport returns the Douyin live CDN transport policy.
func (c *Client) LiveTransport() site.MediaTransportSpec {
	return site.DouyinLiveTransport(c.profile.UserAgent, c.allowLocalhost)
}

// SetAutomaticRequestHandlers wires per-site automatic request quota and circuit-breaker hooks.
func (c *Client) SetAutomaticRequestHandlers(
	budget func(ctx context.Context, source string, endpoint string) error,
	breaker func(source string, err error),
) {
	c.cbMu.Lock()
	c.autoBudget = budget
	c.autoBreaker = breaker
	c.cbMu.Unlock()
}

// SetAuthExpiredHandler registers a callback invoked when an authenticated request fails with auth_required.
func (c *Client) SetAuthExpiredHandler(fn func(reason string)) {
	c.cbMu.Lock()
	c.authExpired = fn
	c.cbMu.Unlock()
}

// ReportAutomaticError notifies the registered automatic circuit breaker when ctx carries an automatic source.
func (c *Client) ReportAutomaticError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	source := site.AutomaticRequestSource(ctx)
	if source == "" {
		return
	}
	c.cbMu.RLock()
	breaker := c.autoBreaker
	c.cbMu.RUnlock()
	if breaker != nil {
		breaker(source, err)
	}
}

// HasAuth reports whether an active Douyin credential is stored.
func (c *Client) HasAuth() bool {
	return c.cred != nil && c.cred.HasActiveCredential()
}

// RequireAuth returns an auth_required *site.Error without issuing any network request when Douyin is logged out.
func (c *Client) RequireAuth(endpoint string) error {
	if c.HasAuth() {
		return nil
	}
	return &site.Error{
		Site:     site.Douyin,
		Kind:     site.ErrKindAuthRequired,
		Endpoint: endpoint,
		Message:  "请先在设置中粘贴 Cookie 登录抖音",
	}
}

func (c *Client) acquireSlotAndPace(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindNetwork,
			Message: "请求已取消",
			Err:     ctx.Err(),
		}
	case c.serialGate <- struct{}{}:
	}

	released := false
	release := func() {
		if !released {
			released = true
			<-c.serialGate
		}
	}

	if c.minInterval > 0 {
		c.paceMu.Lock()
		now := c.nowFn()
		var wait time.Duration
		if !c.lastReqAt.IsZero() {
			target := c.lastReqAt.Add(c.minInterval + c.randomJitter())
			if target.After(now) {
				wait = target.Sub(now)
			}
		}
		c.paceMu.Unlock()

		if wait > 0 {
			if err := c.sleepFn(ctx, wait); err != nil {
				release()
				return nil, &site.Error{
					Site:    site.Douyin,
					Kind:    site.ErrKindNetwork,
					Message: "请求等待被取消",
					Err:     err,
				}
			}
		}

		c.paceMu.Lock()
		c.lastReqAt = c.nowFn()
		c.paceMu.Unlock()
	}

	return release, nil
}

func (c *Client) randomJitter() time.Duration {
	if c.jitterMax <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := io.ReadFull(c.entropy, b[:]); err != nil {
		return 0
	}
	v := binary.LittleEndian.Uint64(b[:])
	maxNs := uint64(c.jitterMax.Nanoseconds()) + 1
	return time.Duration(v % maxNs)
}

func (c *Client) checkAutomaticBudget(ctx context.Context, endpoint string) error {
	source := site.AutomaticRequestSource(ctx)
	if source == "" {
		return nil
	}
	c.cbMu.RLock()
	budget := c.autoBudget
	c.cbMu.RUnlock()
	if budget == nil {
		return nil
	}
	return budget(ctx, source, endpoint)
}

type apiRequestSpec struct {
	endpoint        string
	referer         string
	origin          string
	params          []sign.Pair
	requireSign     bool
	guestNoCookie   bool
	candidateCookie map[string]string
}

type apiRawResponse struct {
	statusCode int
	header     http.Header
	body       []byte
}

func (c *Client) resolveCookieContext(spec apiRequestSpec) (cookieHeader, msToken, uifid, verifyFp string, usingStore bool, err error) {
	switch {
	case spec.guestNoCookie:
		tok, gErr := sign.GenerateMSToken(c.entropy)
		if gErr != nil {
			tok = "SYNTHETIC_FALLBACK_MSTOKEN=="
		}
		return "", tok, "", "", false, nil

	case spec.candidateCookie != nil:
		cleaned := credential.CleanDouyinCookieMap(spec.candidateCookie)
		var parts []string
		for _, k := range credential.AllowedDouyinCookieKeys {
			if v := cleaned[k]; v != "" {
				parts = append(parts, k+"="+v)
			}
		}
		tok := strings.TrimSpace(cleaned["msToken"])
		if tok == "" {
			if gen, gErr := sign.GenerateMSToken(c.entropy); gErr == nil {
				tok = gen
			}
		}
		return strings.Join(parts, "; "), tok, sign.ExtractUIFID(cleaned), sign.ExtractVerifyFp(cleaned), false, nil

	default:
		if authErr := c.RequireAuth(spec.endpoint); authErr != nil {
			return "", "", "", "", false, authErr
		}
		cookieHdr := c.cred.CookieHeader()
		if cookieHdr == "" {
			return "", "", "", "", false, &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: spec.endpoint,
				Message:  "请先在设置中粘贴 Cookie 登录抖音",
			}
		}
		tok := strings.TrimSpace(c.cred.MSToken())
		if tok == "" {
			if gen, gErr := sign.GenerateMSToken(c.entropy); gErr == nil {
				tok = gen
			}
		}
		uifidVal := strings.TrimSpace(c.cred.UIFID())
		fpVal := strings.TrimSpace(c.cred.CookieValue("s_v_web_id"))
		return cookieHdr, tok, uifidVal, fpVal, true, nil
	}
}

func (c *Client) doAPIRequest(ctx context.Context, spec apiRequestSpec) (*apiRawResponse, error) {
	if err := c.checkAutomaticBudget(ctx, spec.endpoint); err != nil {
		return nil, err
	}

	cookieHeader, msToken, uifid, verifyFp, usingStore, err := c.resolveCookieContext(spec)
	if err != nil {
		c.ReportAutomaticError(ctx, err)
		return nil, err
	}

	release, err := c.acquireSlotAndPace(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	parsedURL, err := url.Parse(spec.endpoint)
	if err != nil || parsedURL.Host == "" {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindBadRequest,
			Endpoint: spec.endpoint,
			Message:  "无效的接口地址",
		}
	}
	if err := c.validateAPIURL(parsedURL); err != nil {
		return nil, err
	}

	params := make([]sign.Pair, 0, len(spec.params)+1)
	params = append(params, spec.params...)
	if spec.requireSign && msToken != "" && !hasSignPair(params, "msToken") {
		params = append(params, sign.Pair{Key: "msToken", Value: msToken})
	}

	var extraHeaders map[string]string
	if spec.requireSign {
		signed, sErr := c.signer.Sign(ctx, sign.SignInput{
			Endpoint:    parsedURL.Path,
			Params:      params,
			UserAgent:   c.profile.UserAgent,
			BrowserInfo: c.profile.BrowserInfo,
			UIFID:       uifid,
			VerifyFp:    verifyFp,
		})
		if sErr != nil {
			return nil, &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindBadRequest,
				Endpoint: sanitizeEndpointLabel(parsedURL),
				Message:  "抖音请求签名生成失败",
				Err:      sErr,
			}
		}
		parsedURL.RawQuery = signed.SignedQuery
		extraHeaders = signed.Headers
	} else if len(params) > 0 {
		parsedURL.RawQuery = sign.SerializePairs(params)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindBadRequest,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "构造抖音请求失败",
			Err:      err,
		}
	}

	c.applyAPIHeaders(req, spec.referer, spec.origin, cookieHeader, extraHeaders)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		netErr := &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindNetwork,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "请求抖音接口失败，请检查网络连接",
			Err:      err,
		}
		c.ReportAutomaticError(ctx, netErr)
		return nil, netErr
	}
	defer resp.Body.Close()

	body, err := readBoundedBody(resp.Body, c.maxBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindBlocked,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "抖音响应体积超出 4 MiB 安全上限",
			}
		}
		netErr := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Endpoint:   sanitizeEndpointLabel(parsedURL),
			Message:    "读取抖音响应失败",
			Err:        err,
		}
		c.ReportAutomaticError(ctx, netErr)
		return nil, netErr
	}

	if usingStore && c.cred != nil {
		merged := credential.ExtractDouyinSetCookies(resp.Cookies())
		if xms := strings.TrimSpace(resp.Header.Get("X-Ms-Token")); xms != "" {
			if merged == nil {
				merged = make(map[string]string, 1)
			}
			merged["msToken"] = xms
		}
		if len(merged) > 0 {
			_ = c.cred.MergeContextCookies(merged)
		}
	}

	rawResp := &apiRawResponse{
		statusCode: resp.StatusCode,
		header:     resp.Header.Clone(),
		body:       body,
	}
	if err := c.classifyHTTPAndEnvelopeError(ctx, sanitizeEndpointLabel(parsedURL), rawResp, usingStore); err != nil {
		return rawResp, err
	}
	return rawResp, nil
}

func (c *Client) applyAPIHeaders(req *http.Request, referer, origin, cookieHeader string, extra map[string]string) {
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Sec-Ch-Ua", c.profile.SecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", c.profile.SecCHUAMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", c.profile.SecCHUAPlatform)
	if referer == "" {
		referer = DefaultReferer
	}
	req.Header.Set("Referer", referer)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Del("Cookie")
	}
	for k, v := range extra {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}

var allowedAPIHosts = []string{
	"douyin.com",
	"iesdouyin.com",
	"amemv.com",
}

func (c *Client) validateAPIURL(u *url.URL) error {
	if u == nil || u.Host == "" || u.User != nil {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不安全的接口地址",
		}
	}
	host := strings.ToLower(u.Hostname())
	if c.allowLocalhost && isLoopbackHost(host) {
		if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
			return nil
		}
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非 HTTPS 抖音接口请求",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非常规端口抖音接口请求",
		}
	}
	if !site.MatchDomainSuffix(host, allowedAPIHosts) {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "接口域名不在抖音允许列表中: " + host,
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

var errBodyTooLarge = errors.New("douyin: response body exceeds limit")

func readBoundedBody(r io.Reader, limit int64) ([]byte, error) {
	lr := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func hasSignPair(pairs []sign.Pair, key string) bool {
	for _, p := range pairs {
		if p.Key == key {
			return true
		}
	}
	return false
}

func sanitizeEndpointLabel(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// classifyHTTPAndEnvelopeError inspects HTTP status, 0-byte bodies, HTML verification pages,
// Argus 403 gateway blocks, verify_center_decision_conf challenges, and status_code per douyin.md §9.1.
func (c *Client) classifyHTTPAndEnvelopeError(ctx context.Context, endpoint string, resp *apiRawResponse, usingStore bool) error {
	trimmed := bytes.TrimSpace(resp.body)

	switch {
	case resp.statusCode == http.StatusForbidden:
		bodyStr := string(trimmed)
		if strings.Contains(bodyStr, "ArgusSecurityPlugin") || strings.Contains(strings.ToLower(bodyStr), "verify") {
			err := &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: resp.statusCode,
				Endpoint:   endpoint,
				Message:    "抖音安全网关拒绝了当前请求，已停止访问",
			}
			c.ReportAutomaticError(ctx, err)
			return err
		}
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRateLimited,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音请求被限制（HTTP 403），已暂停访问",
		}
		c.ReportAutomaticError(ctx, err)
		return err

	case resp.statusCode == http.StatusTooManyRequests || resp.statusCode == 444:
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRateLimited,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("抖音请求过于频繁（HTTP %d），已暂停自动请求", resp.statusCode),
		}
		c.ReportAutomaticError(ctx, err)
		return err

	case resp.statusCode >= 500:
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("抖音服务端暂时不可用（HTTP %d）", resp.statusCode),
		}
		c.ReportAutomaticError(ctx, err)
		return err

	case resp.statusCode >= 300 && resp.statusCode < 400:
		loc := strings.ToLower(resp.header.Get("Location"))
		kind := site.ErrKindRiskControl
		msg := "抖音接口返回异常跳转，疑似触发验证拦截"
		if strings.Contains(loc, "login") || strings.Contains(loc, "passport") {
			kind = site.ErrKindAuthRequired
			msg = "抖音登录状态已失效，请在设置页重新粘贴 Cookie"
		}
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       kind,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    msg,
		}
		c.onClassifiedError(ctx, err, usingStore)
		return err

	case resp.statusCode != http.StatusOK:
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("抖音接口返回异常状态码 HTTP %d", resp.statusCode),
		}
		c.ReportAutomaticError(ctx, err)
		return err
	}

	// HTTP 200 with 0-byte body -> silent risk-control drop (douyin.md §9.1 & §9.2).
	if len(trimmed) == 0 {
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音返回空响应，疑似触发签名或风控拦截",
		}
		c.ReportAutomaticError(ctx, err)
		return err
	}

	// Non-JSON HTML/text (e.g., <title>验证码中间页</title> or verify_page) -> risk_control.
	if trimmed[0] != '{' && trimmed[0] != '[' {
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音返回了非 JSON 验证页面，已立即停止请求",
		}
		c.ReportAutomaticError(ctx, err)
		return err
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音返回 JSON 解析失败",
			Err:        err,
		}
	}

	if hasNonEmptyChallengeField(top) {
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音触发验证码挑战，已立即停止请求",
		}
		c.ReportAutomaticError(ctx, err)
		return err
	}

	code := parseJSONInt(top["status_code"])
	statusMsg := parseJSONString(top["status_msg"])
	if statusMsg == "" {
		if dataRaw, ok := top["data"]; ok {
			var dataMap map[string]json.RawMessage
			if json.Unmarshal(dataRaw, &dataMap) == nil {
				if hasNonEmptyChallengeField(dataMap) {
					err := &site.Error{
						Site:       site.Douyin,
						Kind:       site.ErrKindRiskControl,
						HTTPStatus: resp.statusCode,
						Endpoint:   endpoint,
						Message:    "抖音直播间触发验证挑战，已立即停止请求",
					}
					c.ReportAutomaticError(ctx, err)
					return err
				}
				if code == 0 {
					code = parseJSONInt(dataMap["status_code"])
				}
				if statusMsg == "" {
					statusMsg = parseJSONString(dataMap["prompts"])
					if statusMsg == "" {
						statusMsg = parseJSONString(dataMap["message"])
					}
				}
			}
		}
	}

	if code == 0 {
		return nil
	}

	se := classifyStatusCode(code, statusMsg, endpoint, resp.statusCode)
	c.onClassifiedError(ctx, se, usingStore)
	return se
}

func hasNonEmptyChallengeField(m map[string]json.RawMessage) bool {
	for _, key := range []string{"verify_center_decision_conf", "verify_data", "captcha", "shark_decision_conf"} {
		raw, ok := m[key]
		if !ok {
			continue
		}
		t := bytes.TrimSpace(raw)
		if len(t) == 0 || bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte(`""`)) || bytes.Equal(t, []byte("{}")) || bytes.Equal(t, []byte("[]")) {
			continue
		}
		return true
	}
	return false
}

func classifyStatusCode(code int, msg, endpoint string, httpStatus int) *site.Error {
	lowerMsg := strings.ToLower(msg)
	isLoginHint := strings.Contains(msg, "未登录") ||
		strings.Contains(msg, "请登录") ||
		strings.Contains(msg, "需要登录") ||
		strings.Contains(lowerMsg, "not login") ||
		strings.Contains(lowerMsg, "login")

	switch code {
	case 2483:
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindAuthRequired,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "抖音登录状态已失效，请在设置页重新粘贴 Cookie",
		}
	case 8, 2154, 2156:
		if isLoginHint {
			return &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindAuthRequired,
				Code:       code,
				HTTPStatus: httpStatus,
				Endpoint:   endpoint,
				Message:    "抖音登录状态已失效，请在设置页重新粘贴 Cookie",
			}
		}
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "抖音访问受限或触发风控拦截",
		}
	case 10000, 10001:
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "抖音触发验证码风控拦截，已停止请求",
		}
	case 2096:
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindForbidden,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "该抖音账号已设为私密，无法查看内容",
		}
	case 4001038, 10011:
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindUnavailable,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "该抖音直播间不存在或当前不可用",
		}
	case 2, 5:
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindBadRequest,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    "抖音接口请求参数无效",
		}
	default:
		if isLoginHint {
			return &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindAuthRequired,
				Code:       code,
				HTTPStatus: httpStatus,
				Endpoint:   endpoint,
				Message:    "抖音登录状态已失效，请在设置页重新粘贴 Cookie",
			}
		}
		return &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindUnavailable,
			Code:       code,
			HTTPStatus: httpStatus,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("抖音接口返回错误（status_code=%d）", code),
		}
	}
}

func (c *Client) onClassifiedError(ctx context.Context, se *site.Error, usingStore bool) {
	if se == nil {
		return
	}
	if se.Kind == site.ErrKindAuthRequired && usingStore {
		c.markAuthExpired(se.Message)
	}
	c.ReportAutomaticError(ctx, se)
}

func (c *Client) markAuthExpired(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "抖音登录状态已失效，请在设置页重新粘贴 Cookie"
	}
	if c.cred != nil {
		_, _ = c.cred.MarkState("expired", reason)
	}
	c.cbMu.RLock()
	cb := c.authExpired
	c.cbMu.RUnlock()
	if cb != nil {
		cb(reason)
	}
}

func parseJSONInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		if v, iErr := n.Int64(); iErr == nil {
			return int(v)
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var v int
		_, _ = fmt.Sscanf(strings.TrimSpace(s), "%d", &v)
		return v
	}
	return 0
}

func parseJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// ValidateMediaURL checks that rawURL belongs to Douyin's allowed HTTPS media/live CDN whitelist.
func (c *Client) ValidateMediaURL(rawURL string) error {
	return c.VODTransport().ValidateURL(rawURL)
}

// FetchImage fetches a Douyin cover/avatar/note image without sending Cookie and without stripping '@'.
func (c *Client) FetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	return c.FetchImageLimited(ctx, rawURL, c.maxBodyBytes)
}

// FetchImageLimited fetches up to maxBytes of a Douyin CDN image, preserving full query signatures
// and strictly stripping any Cookie header.
func (c *Client) FetchImageLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	normalized := upgradeDouyinHTTPS(strings.TrimSpace(rawURL), c.allowLocalhost)
	transportSpec := c.VODTransport()
	if err := transportSpec.ValidateURL(normalized); err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 {
		maxBytes = c.maxBodyBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的图片地址",
			Err:     err,
		}
	}
	transportSpec.ApplyHeaders(req)

	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindNetwork,
			Message: "拉取抖音图片失败",
			Err:     err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: resp.StatusCode,
			Message:    fmt.Sprintf("抖音图片返回 HTTP %d", resp.StatusCode),
		}
	}

	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain") {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "抖音图片响应类型异常: " + ct,
		}
	}

	data, err := readBoundedBody(resp.Body, maxBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, "", &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindBlocked,
				Message: "抖音图片体积超出安全上限",
			}
		}
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindNetwork,
			Message: "读取抖音图片失败",
			Err:     err,
		}
	}
	return data, ct, nil
}

func upgradeDouyinHTTPS(raw string, allowLocalhost bool) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(s), "http://") {
		if allowLocalhost {
			if u, err := url.Parse(s); err == nil && isLoopbackHost(strings.ToLower(u.Hostname())) {
				return s
			}
		}
		return "https://" + s[len("http://"):]
	}
	return s
}
