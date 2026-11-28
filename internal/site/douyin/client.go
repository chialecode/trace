package douyin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/guard"
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
	CredStore                 *credential.Store
	Endpoints                 Endpoints
	UserAgent                 string
	WebID                     string
	Signer                    sign.Signer
	MinInterval               time.Duration
	JitterMax                 time.Duration
	APITimeout                time.Duration
	RedirectTimeout           time.Duration
	MediaIdleTimeout          time.Duration
	MaxBodyBytes              int64
	MaxImageBytes             int64
	Transport                 http.RoundTripper
	WrapTransport             func(http.RoundTripper) http.RoundTripper
	AllowLocalhost            bool
	ImageCDNFallbackProviders []site.PublicImageCDNProvider
	Now                       func() time.Time
	Sleep                     func(ctx context.Context, d time.Duration) error
	Entropy                   io.Reader
	PreferredCodec            func() string
}

type liveWebRIDEntry struct {
	webRID           string
	confirmedShortID bool
}

// Client is the Douyin PC Web and Webcast protocol client.
// Outbound API requests are serialized (concurrency = 1) with 1000ms + 0..300ms jitter pacing,
// 15s timeout, 4 MiB body limit, manual redirect handling, and strict Cookie/CDN separation.
type Client struct {
	cred                 *credential.Store
	endpoints            Endpoints
	profile              BrowserProfile
	webID                string
	signer               sign.Signer
	httpClient           *http.Client
	redirectClient       *http.Client
	mediaClient          *http.Client
	mediaIdleTimeout     time.Duration
	maxBodyBytes         int64
	maxImageBytes        int64
	allowLocalhost       bool
	cdnFallbackProviders []site.PublicImageCDNProvider
	fallbackWrap         func(http.RoundTripper) http.RoundTripper

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

	msTokenMu         sync.Mutex
	fallbackMSToken   string
	fallbackSessionID string

	webIDMu         sync.Mutex
	syncedWebID     string
	syncedTTWIDHash string
	webIDWarmupHash string
	webIDWarmupCall *webIDWarmupCall
	webIDGen        uint64

	guardMu        sync.Mutex
	ephemeralGuard *guard.State
}

// GenerateWebID generates a new 19-digit Douyin webid string.
func GenerateWebID() (string, error) {
	return sign.GenerateWebID(rand.Reader)
}

type webIDWarmupCall struct {
	ttwidHash      string
	done           chan struct{}
	err            error
	callerCanceled bool
}

// ResponseDiagnostics holds strictly non-sensitive HTTP/protocol flags for runtime logging.
// It never carries raw header values, Cookies, tokens, fp, detail, IDs, or URLs.
type ResponseDiagnostics struct {
	HTTPStatus        int
	BodyBytes         int
	EmptyBody         bool
	HasBDTuringVerify bool
	HasBDTuringParams bool
	HasGuardResult    bool
	GuardResultCode   int
	WebIDSynced       bool
}

type responseDiagError struct {
	diag  ResponseDiagnostics
	cause error
}

func (e *responseDiagError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return fmt.Sprintf("http=%d bytes=%d", e.diag.HTTPStatus, e.diag.BodyBytes)
}

func (e *responseDiagError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ExtractResponseDiagnostics returns the desensitized Douyin response diagnostics attached to err, if any.
func ExtractResponseDiagnostics(err error) (ResponseDiagnostics, bool) {
	var de *responseDiagError
	if errors.As(err, &de) && de != nil {
		return de.diag, true
	}
	return ResponseDiagnostics{}, false
}

func attachResponseDiag(se *site.Error, diag ResponseDiagnostics) {
	if se == nil {
		return
	}
	var existing *responseDiagError
	if errors.As(se.Err, &existing) {
		return
	}
	se.Err = &responseDiagError{
		diag:  diag,
		cause: se.Err,
	}
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
	if webID == "" && opts.CredStore != nil && opts.CredStore.Site() == credential.SiteDouyin {
		if persisted, err := opts.CredStore.EnsureDouyinWebID(func() (string, error) {
			return sign.GenerateWebID(ent)
		}); err == nil && persisted != "" {
			webID = persisted
		}
	}
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
	maxImageBytes := opts.MaxImageBytes
	if maxImageBytes <= 0 {
		maxImageBytes = site.DefaultMaxImageBytes
	}
	mediaIdleTimeout := opts.MediaIdleTimeout
	if mediaIdleTimeout <= 0 {
		mediaIdleTimeout = 30 * time.Second
	}

	apiTransport := opts.Transport
	mediaTransport := opts.Transport
	if apiTransport == nil {
		apiTransport = &http.Transport{
			Proxy:                 site.Proxy,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: apiTimeout,
		}
		mediaDialer := &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		mediaGuard := site.NewGuardedMediaDialer(site.Douyin, opts.AllowLocalhost, mediaDialer, "已拦截内网或回环媒体节点地址")
		mediaTransport = &http.Transport{
			Proxy:                 mediaGuard.Proxy,
			DialContext:           mediaGuard.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: apiTimeout,
		}
		if opts.AllowLocalhost {
			// Fixture clients using real transports must never escape to the real site.
			apiTransport = site.LoopbackTransport(apiTransport)
			mediaTransport = site.LoopbackTransport(mediaTransport)
		}
	}
	if opts.WrapTransport != nil {
		apiTransport = opts.WrapTransport(apiTransport)
		mediaTransport = opts.WrapTransport(mediaTransport)
	}

	noRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	var fallbackWrap func(http.RoundTripper) http.RoundTripper
	if opts.Transport != nil {
		fallbackWrap = func(_ http.RoundTripper) http.RoundTripper {
			return mediaTransport
		}
	} else if opts.WrapTransport != nil {
		fallbackWrap = opts.WrapTransport
	}

	c := &Client{
		cred:                 opts.CredStore,
		endpoints:            opts.Endpoints.withDefaults(),
		profile:              profile,
		webID:                webID,
		signer:               signer,
		mediaIdleTimeout:     mediaIdleTimeout,
		maxBodyBytes:         maxBytes,
		maxImageBytes:        maxImageBytes,
		allowLocalhost:       opts.AllowLocalhost,
		cdnFallbackProviders: append([]site.PublicImageCDNProvider(nil), opts.ImageCDNFallbackProviders...),
		fallbackWrap:         fallbackWrap,
		serialGate:           make(chan struct{}, 1),
		minInterval:          minInterval,
		jitterMax:            jitterMax,
		nowFn:                nowFn,
		sleepFn:              sleepFn,
		entropy:              ent,
		prefCodecFn:          opts.PreferredCodec,
		webRIDByUser:         make(map[string]liveWebRIDEntry),
	}
	c.httpClient = &http.Client{
		Transport:     apiTransport,
		Timeout:       apiTimeout,
		CheckRedirect: noRedirect,
	}
	c.redirectClient = &http.Client{
		Transport:     apiTransport,
		Timeout:       redirTimeout,
		CheckRedirect: noRedirect,
	}
	c.mediaClient = &http.Client{
		Transport:     mediaTransport,
		Timeout:       0,
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

// WebID returns the client's current persistent Douyin webid.
func (c *Client) WebID() string {
	c.webIDMu.Lock()
	defer c.webIDMu.Unlock()
	return c.webID
}

func (c *Client) setWebID(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	c.webIDMu.Lock()
	c.webID = id
	c.webIDMu.Unlock()
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

// MediaHTTPClient returns the configured cookieless media HTTP client and idle timeout.
func (c *Client) MediaHTTPClient() (*http.Client, time.Duration) {
	return c.mediaClient, c.mediaIdleTimeout
}

// ResolvePlayRedirect resolves an authenticated Douyin main-site play redirector
// URL (https://www.douyin.com/aweme/v1/play/?...) into a cookieless CDN media URL.
// Cookies are sent ONLY to the validated Douyin API host and never forwarded on
// redirects (douyin.md §7). Used for works/notes whose direct CDN URLs require
// a session token (need_set_token=true / is_need_set_cookie=true) or return 403.
func (c *Client) ResolvePlayRedirect(ctx context.Context, playURL string) (string, error) {
	playURL = strings.TrimSpace(playURL)
	parsedURL, err := url.Parse(playURL)
	if err != nil || parsedURL.Host == "" {
		return "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音播放跳转地址",
		}
	}
	if err := c.validateAPIURL(parsedURL); err != nil {
		return "", err
	}
	cleanPath := strings.TrimSuffix(strings.ToLower(parsedURL.Path), "/")
	if cleanPath != "/aweme/v1/play" {
		return "", &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindBlocked,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "已拦截非播放跳转路径请求",
		}
	}
	lowerRaw := strings.ToLower(playURL)
	if strings.Contains(lowerRaw, "/playwm/") || strings.Contains(lowerRaw, "watermark=1") {
		return "", &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindBlocked,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "已拦截带水印的播放跳转地址",
		}
	}

	reqCtx, err := c.resolveCookieContext(apiRequestSpec{endpoint: playURL})
	if err != nil {
		c.ReportAutomaticError(ctx, err)
		return "", err
	}
	cookieHeader, usingStore := reqCtx.cookieHeader, reqCtx.usingStore
	if ctx.Err() != nil {
		return "", &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindNetwork,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "请求已取消",
			Err:      ctx.Err(),
		}
	}
	if err := c.checkAutomaticBudget(ctx, sanitizeEndpointLabel(parsedURL)); err != nil {
		return "", err
	}
	release, err := c.acquireSlotAndPace(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindBadRequest,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "构造抖音播放跳转请求失败",
			Err:      site.UnwrapURLError(err),
		}
	}
	c.applyPlayRedirectHeaders(req, parsedURL, cookieHeader)

	resp, err := c.redirectClient.Do(req)
	if err != nil {
		netErr := &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindNetwork,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "请求抖音播放跳转地址失败",
			Err:      site.UnwrapURLError(err),
		}
		c.ReportAutomaticError(ctx, netErr)
		return "", netErr
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		rawLoc := strings.TrimSpace(resp.Header.Get("Location"))
		if rawLoc == "" {
			return "", &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindUnavailable,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "抖音播放跳转响应缺少目标地址",
			}
		}
		lowerLoc := strings.ToLower(rawLoc)
		if strings.Contains(lowerLoc, "login") || strings.Contains(lowerLoc, "passport") {
			authErr := &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindAuthRequired,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "抖音登录状态已失效，请在设置页重新粘贴 Cookie",
			}
			c.onClassifiedError(ctx, authErr, usingStore)
			return "", authErr
		}
		if strings.Contains(lowerLoc, "verify") || strings.Contains(lowerLoc, "captcha") {
			rcErr := &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "抖音播放跳转触发验证拦截，已停止请求",
			}
			c.onClassifiedError(ctx, rcErr, usingStore)
			return "", rcErr
		}
		locURL, pErr := url.Parse(rawLoc)
		if pErr != nil {
			return "", &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindBlocked,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "抖音播放跳转目标地址格式无效",
			}
		}
		resolved := parsedURL.ResolveReference(locURL).String()
		vodSpec := c.VODTransport()
		vodSpec.PublicRedirects = true
		if vErr := vodSpec.ValidateRedirectURL(resolved); vErr != nil {
			return "", vErr
		}
		return resolved, nil
	}

	body, _ := readBoundedBody(resp.Body, 64*1024)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: resp.StatusCode,
			Endpoint:   sanitizeEndpointLabel(parsedURL),
			Message:    fmt.Sprintf("抖音播放跳转地址已不可用 (HTTP %d)", resp.StatusCode),
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		authErr := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: resp.StatusCode,
			Endpoint:   sanitizeEndpointLabel(parsedURL),
			Message:    "抖音登录状态已失效，请在设置页重新粘贴 Cookie",
		}
		c.onClassifiedError(ctx, authErr, usingStore)
		return "", authErr
	}
	rawResp := &apiRawResponse{
		statusCode: resp.StatusCode,
		header:     resp.Header.Clone(),
		body:       body,
	}
	if cErr := c.classifyHTTPAndEnvelopeError(ctx, sanitizeEndpointLabel(parsedURL), rawResp, usingStore); cErr != nil {
		return "", cErr
	}
	return "", &site.Error{
		Site:       site.Douyin,
		Kind:       site.ErrKindUnavailable,
		HTTPStatus: resp.StatusCode,
		Endpoint:   sanitizeEndpointLabel(parsedURL),
		Message:    fmt.Sprintf("抖音播放跳转未返回媒体地址 (HTTP %d)", resp.StatusCode),
	}
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
	candidateGuard  *guard.State
	// form, when non-nil, turns the request into a POST with an
	// application/x-www-form-urlencoded body; the encoded body is also fed to
	// the a_bogus body chain. GET requests leave it nil and are byte-identical.
	form []sign.Pair
	// emptyMeans, when set, replaces the risk-control reading of an HTTP 200
	// with a 0-byte body: the endpoint answers every non-browser request that
	// way, so it says nothing about the session and must not stop the site.
	emptyMeans string
}

const formContentType = "application/x-www-form-urlencoded"

type apiRawResponse struct {
	statusCode  int
	header      http.Header
	body        []byte
	sentMSToken string
	syncedWebID string
	ttwidHash   string
	webIDGen    uint64
	diag        ResponseDiagnostics
}

type resolvedCookieContext struct {
	cookieHeader string
	msToken      string
	uifid        string
	verifyFp     string
	ttwidHash    string
	usingStore   bool
}

func hashTTWID(ttwid string) string {
	ttwid = strings.TrimSpace(ttwid)
	if ttwid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ttwid))
	return hex.EncodeToString(sum[:])
}

func headerValueFold(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	if v := strings.TrimSpace(h.Get(name)); v != "" {
		return v
	}
	for k, vals := range h {
		if strings.EqualFold(k, name) {
			for _, v := range vals {
				if tv := strings.TrimSpace(v); tv != "" {
					return tv
				}
			}
		}
	}
	return ""
}

// isValidServerWebID checks that raw is a 10..24 digit non-zero ASCII decimal ID
// as emitted by Douyin's cookie_ttwidinfo_webid response header.
func isValidServerWebID(raw string) bool {
	s := strings.TrimSpace(raw)
	if len(s) < 10 || len(s) > 24 {
		return false
	}
	if s[0] < '1' || s[0] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseServerWebID(h http.Header) string {
	v := strings.TrimSpace(headerValueFold(h, "cookie_ttwidinfo_webid"))
	if !isValidServerWebID(v) {
		return ""
	}
	return v
}

func buildResponseDiagnostics(statusCode int, header http.Header, body []byte, webIDSynced bool) ResponseDiagnostics {
	diag := ResponseDiagnostics{
		HTTPStatus:        statusCode,
		BodyBytes:         len(body),
		EmptyBody:         len(bytes.TrimSpace(body)) == 0,
		HasBDTuringVerify: headerValueFold(header, "bdturing-verify") != "",
		HasBDTuringParams: headerValueFold(header, "x-vc-bdturing-parameters") != "",
		WebIDSynced:       webIDSynced,
	}
	if rawGuard := headerValueFold(header, guard.HeaderResult); rawGuard != "" {
		if code, err := strconv.Atoi(rawGuard); err == nil {
			diag.HasGuardResult = true
			diag.GuardResultCode = code
		}
	}
	return diag
}

func (c *Client) currentWebIDGen() uint64 {
	c.webIDMu.Lock()
	defer c.webIDMu.Unlock()
	return c.webIDGen
}

func (c *Client) syncedWebIDFor(ttwidHash string) string {
	if ttwidHash == "" {
		return ""
	}
	c.webIDMu.Lock()
	defer c.webIDMu.Unlock()
	if c.syncedTTWIDHash == ttwidHash && c.syncedWebID != "" {
		return c.syncedWebID
	}
	return ""
}

func (c *Client) recordSyncedWebID(ttwidHash, webID string, expectedGen uint64) {
	if ttwidHash == "" || !isValidServerWebID(webID) {
		return
	}
	webID = strings.TrimSpace(webID)
	c.webIDMu.Lock()
	if c.webIDGen == expectedGen {
		c.syncedWebID = webID
		c.syncedTTWIDHash = ttwidHash
		c.webIDWarmupHash = ttwidHash
	}
	c.webIDMu.Unlock()
}

func (c *Client) markWebIDWarmupTried(ttwidHash string, expectedGen uint64) {
	if ttwidHash == "" {
		return
	}
	c.webIDMu.Lock()
	if c.webIDGen == expectedGen {
		c.webIDWarmupHash = ttwidHash
	}
	c.webIDMu.Unlock()
}

func (c *Client) clearSyncedWebID() {
	c.webIDMu.Lock()
	c.syncedWebID = ""
	c.syncedTTWIDHash = ""
	c.webIDWarmupHash = ""
	c.webIDGen++
	c.webIDMu.Unlock()
}

func (c *Client) applyLoginSyncedWebID(sentTTWIDHash, savedTTWIDHash, syncedWebID string) {
	c.webIDMu.Lock()
	c.syncedWebID = ""
	c.syncedTTWIDHash = ""
	c.webIDWarmupHash = ""
	c.webIDGen++
	if savedTTWIDHash != "" && savedTTWIDHash == sentTTWIDHash {
		c.webIDWarmupHash = savedTTWIDHash
		if isValidServerWebID(syncedWebID) {
			c.syncedWebID = strings.TrimSpace(syncedWebID)
			c.syncedTTWIDHash = savedTTWIDHash
		}
	}
	c.webIDMu.Unlock()
}

// ensureServerWebIDForFavorites synchronizes cookie_ttwidinfo_webid via a single lightweight
// read-only profile/self call on cold start when the active Cookie has a ttwid that has not
// yet synced or attempted warmup. Concurrent callers for the same ttwid share one warmup call.
func (c *Client) ensureServerWebIDForFavorites(ctx context.Context) error {
	for {
		if c.cred == nil || !c.cred.HasActiveCredential() {
			return nil
		}
		ttwidHash := hashTTWID(c.cred.CookieValue("ttwid"))
		if ttwidHash == "" {
			return nil
		}
		c.webIDMu.Lock()
		if c.syncedTTWIDHash == ttwidHash && c.syncedWebID != "" {
			c.webIDMu.Unlock()
			return nil
		}
		if c.webIDWarmupHash == ttwidHash {
			c.webIDMu.Unlock()
			return nil
		}
		if call := c.webIDWarmupCall; call != nil && call.ttwidHash == ttwidHash {
			c.webIDMu.Unlock()
			select {
			case <-ctx.Done():
				return &site.Error{
					Site:     site.Douyin,
					Kind:     site.ErrKindNetwork,
					Endpoint: site.SanitizeEndpoint(c.endpoints.ProfileSelf),
					Message:  "请求已取消",
					Err:      ctx.Err(),
				}
			case <-call.done:
				if call.err != nil {
					if call.callerCanceled {
						if ctx.Err() != nil {
							return &site.Error{
								Site:     site.Douyin,
								Kind:     site.ErrKindNetwork,
								Endpoint: site.SanitizeEndpoint(c.endpoints.ProfileSelf),
								Message:  "请求已取消",
								Err:      ctx.Err(),
							}
						}
						continue
					}
					return call.err
				}
				return nil
			}
		}
		call := &webIDWarmupCall{
			ttwidHash: ttwidHash,
			done:      make(chan struct{}),
		}
		c.webIDWarmupCall = call
		c.webIDMu.Unlock()

		return c.executeWebIDWarmupCall(ctx, call)
	}
}

func (c *Client) executeWebIDWarmupCall(ctx context.Context, call *webIDWarmupCall) (err error) {
	defer func() {
		canceled := ctx.Err() != nil
		c.webIDMu.Lock()
		call.err = err
		call.callerCanceled = canceled
		if c.webIDWarmupCall == call {
			c.webIDWarmupCall = nil
		}
		close(call.done)
		c.webIDMu.Unlock()
	}()
	return c.runWebIDWarmup(ctx)
}

func (c *Client) runWebIDWarmup(ctx context.Context) error {
	_, err := c.GetSelfProfile(ctx)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if site.IsKind(err, site.ErrKindAuthRequired) ||
			site.IsKind(err, site.ErrKindRiskControl) ||
			site.IsKind(err, site.ErrKindRateLimited) {
			return err
		}
		// Non-fatal/transient profile/self error: let Favorites proceed with default webid
		// without permanently suppressing future warmup attempts.
		return nil
	}
	return nil
}

func cookieMapSessionFingerprint(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	return strings.TrimSpace(cookies["sessionid"]) + "|" + strings.TrimSpace(cookies["sessionid_ss"])
}

func (c *Client) storeSessionFingerprint() string {
	if c.cred == nil {
		return ""
	}
	return strings.TrimSpace(c.cred.CookieValue("sessionid")) + "|" + strings.TrimSpace(c.cred.CookieValue("sessionid_ss"))
}

func (c *Client) getOrCreateFallbackMSToken(sessionFP string) string {
	c.msTokenMu.Lock()
	defer c.msTokenMu.Unlock()
	if c.fallbackMSToken != "" && c.fallbackSessionID == sessionFP {
		return c.fallbackMSToken
	}
	gen, err := sign.GenerateMSToken(c.entropy)
	if err != nil {
		return ""
	}
	c.fallbackMSToken = gen
	c.fallbackSessionID = sessionFP
	return gen
}

func (c *Client) setFallbackMSToken(sessionFP, tok string) {
	c.msTokenMu.Lock()
	c.fallbackMSToken = strings.TrimSpace(tok)
	c.fallbackSessionID = sessionFP
	c.msTokenMu.Unlock()
}

func (c *Client) clearFallbackMSToken() {
	c.msTokenMu.Lock()
	c.fallbackMSToken = ""
	c.fallbackSessionID = ""
	c.msTokenMu.Unlock()
}

func (c *Client) clearEphemeralGuard() {
	c.guardMu.Lock()
	c.ephemeralGuard = nil
	c.guardMu.Unlock()
}

func fromCredGuardState(in *credential.DouyinGuardState) (guard.State, bool) {
	if in == nil {
		return guard.State{}, false
	}
	st := guard.State{
		PrivateKeyPKCS8: in.PrivateKeyPKCS8,
		PublicKeySEC1:   in.PublicKeySEC1,
		Ticket:          in.Ticket,
		TsSign:          in.TsSign,
		ClientCert:      in.ClientCert,
		UpdatedAt:       in.UpdatedAt,
	}
	if _, err := guard.ParsePrivateKey(st.PrivateKeyPKCS8); err != nil {
		return guard.State{}, false
	}
	return st, true
}

func toCredGuardState(in guard.State) *credential.DouyinGuardState {
	if !in.HasKeyPair() && in.Ticket == "" && in.TsSign == "" && in.ClientCert == "" {
		return nil
	}
	return &credential.DouyinGuardState{
		PrivateKeyPKCS8: in.PrivateKeyPKCS8,
		PublicKeySEC1:   in.PublicKeySEC1,
		Ticket:          in.Ticket,
		TsSign:          in.TsSign,
		ClientCert:      in.ClientCert,
		UpdatedAt:       in.UpdatedAt,
	}
}

func (c *Client) prepareGuardHeaders(spec apiRequestSpec, path, fallbackTicket string, usingStore bool) (map[string]string, *guard.State) {
	var st guard.State
	switch {
	case spec.candidateGuard != nil:
		if _, err := guard.ParsePrivateKey(spec.candidateGuard.PrivateKeyPKCS8); err != nil {
			gen, gErr := guard.GenerateState(c.entropy, c.nowFn)
			if gErr != nil {
				return nil, nil
			}
			*spec.candidateGuard = gen
		}
		st = *spec.candidateGuard

	case usingStore && c.cred != nil:
		loaded, ok := fromCredGuardState(c.cred.DouyinGuard())
		if !ok {
			gen, gErr := guard.GenerateState(c.entropy, c.nowFn)
			if gErr != nil {
				return nil, nil
			}
			loaded = gen
			_ = c.cred.SaveDouyinGuard(toCredGuardState(loaded))
		}
		st = loaded

	default:
		c.guardMu.Lock()
		if c.ephemeralGuard == nil {
			if gen, gErr := guard.GenerateState(c.entropy, c.nowFn); gErr == nil {
				c.ephemeralGuard = &gen
			}
		}
		if c.ephemeralGuard != nil {
			st = *c.ephemeralGuard
		}
		c.guardMu.Unlock()
		if !st.HasKeyPair() {
			return nil, nil
		}
	}

	res, err := guard.BuildHeaders(guard.BuildHeadersInput{
		State:          st,
		Path:           path,
		FallbackTicket: fallbackTicket,
		Now:            c.nowFn,
		Entropy:        c.entropy,
	})
	if err != nil {
		return nil, nil
	}
	// Do not attach unregistered bd-ticket-guard-* handshake headers on read-only
	// endpoints before the server has issued a valid ts_sign + ticket (ACT-028:
	// unregistered handshake headers trigger bd-ticket-guard-result: 1103 instead
	// of 1101). Keep activeGuard non-nil so any server-data response is adopted.
	if !res.Signed {
		return nil, &st
	}
	return res.Headers, &st
}

func (c *Client) adoptGuardResponse(spec apiRequestSpec, active *guard.State, usingStore bool, header http.Header, cookies []*http.Cookie) {
	if active == nil {
		return
	}
	flatHeaders := make(map[string]string, len(header))
	for k, vals := range header {
		if len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
			flatHeaders[k] = vals[0]
		}
	}
	rawCookies := make(map[string]string, len(cookies))
	for _, ck := range cookies {
		if ck != nil && ck.Value != "" {
			rawCookies[ck.Name] = ck.Value
		}
	}
	srv := guard.ParseServerResponse(flatHeaders, rawCookies)
	next, changed := guard.MergeServerData(*active, srv, c.nowFn)
	if !changed {
		return
	}
	switch {
	case spec.candidateGuard != nil:
		*spec.candidateGuard = next
	case usingStore && c.cred != nil:
		_ = c.cred.SaveDouyinGuard(toCredGuardState(next))
	default:
		c.guardMu.Lock()
		cp := next
		c.ephemeralGuard = &cp
		c.guardMu.Unlock()
	}
}

func ensureCookieHasMSToken(cookieHeader, msToken string) string {
	cookieHeader = strings.TrimSpace(cookieHeader)
	msToken = strings.TrimSpace(msToken)
	if cookieHeader == "" || msToken == "" {
		return cookieHeader
	}
	for _, seg := range strings.Split(cookieHeader, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(seg), "=")
		if ok && strings.TrimSpace(k) == "msToken" && strings.TrimSpace(v) != "" {
			return cookieHeader
		}
	}
	return cookieHeader + "; msToken=" + msToken
}

func (c *Client) resolveCookieContext(spec apiRequestSpec) (resolvedCookieContext, error) {
	switch {
	case spec.guestNoCookie:
		tok, gErr := sign.GenerateMSToken(c.entropy)
		if gErr != nil {
			tok = "SYNTHETIC_FALLBACK_MSTOKEN=="
		}
		return resolvedCookieContext{msToken: tok}, nil

	case spec.candidateCookie != nil:
		cleaned := credential.CleanDouyinCookieMap(spec.candidateCookie)
		cookieHdr := credential.FormatDouyinCookieHeader(cleaned)
		tok := strings.TrimSpace(cleaned["msToken"])
		if tok == "" {
			if gen, gErr := sign.GenerateMSToken(c.entropy); gErr == nil {
				tok = gen
			}
		}
		cookieHdr = ensureCookieHasMSToken(cookieHdr, tok)
		return resolvedCookieContext{
			cookieHeader: cookieHdr,
			msToken:      tok,
			uifid:        sign.ExtractUIFID(cleaned),
			verifyFp:     sign.ExtractVerifyFp(cleaned),
			ttwidHash:    hashTTWID(cleaned["ttwid"]),
		}, nil

	default:
		if authErr := c.RequireAuth(spec.endpoint); authErr != nil {
			return resolvedCookieContext{}, authErr
		}
		cookieHdr := c.cred.CookieHeader()
		if cookieHdr == "" {
			return resolvedCookieContext{}, &site.Error{
				Site:     site.Douyin,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: spec.endpoint,
				Message:  "请先在设置中粘贴 Cookie 登录抖音",
			}
		}
		tok := strings.TrimSpace(c.cred.MSToken())
		if tok != "" {
			c.clearFallbackMSToken()
		} else {
			tok = c.getOrCreateFallbackMSToken(c.storeSessionFingerprint())
		}
		cookieHdr = ensureCookieHasMSToken(cookieHdr, tok)
		uifidVal := strings.TrimSpace(c.cred.UIFID())
		fpVal := strings.TrimSpace(c.cred.CookieValue("s_v_web_id"))
		return resolvedCookieContext{
			cookieHeader: cookieHdr,
			msToken:      tok,
			uifid:        uifidVal,
			verifyFp:     fpVal,
			ttwidHash:    hashTTWID(c.cred.CookieValue("ttwid")),
			usingStore:   true,
		}, nil
	}
}

func (c *Client) doAPIRequest(ctx context.Context, spec apiRequestSpec) (*apiRawResponse, error) {
	// Sample webIDGen BEFORE reading the credential store so any concurrent logout,
	// expiry, or account switch (which mutates c.cred first and then advances webIDGen)
	// strictly linearizes after this generation and prevents stale responses from
	// overwriting the newer session's syncedWebID.
	startWebIDGen := c.currentWebIDGen()
	reqCtx, err := c.resolveCookieContext(spec)
	if err != nil {
		c.ReportAutomaticError(ctx, err)
		return nil, err
	}
	cookieHeader, msToken, uifid, verifyFp, usingStore := reqCtx.cookieHeader, reqCtx.msToken, reqCtx.uifid, reqCtx.verifyFp, reqCtx.usingStore
	if ctx.Err() != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindNetwork,
			Endpoint: site.SanitizeEndpoint(spec.endpoint),
			Message:  "请求已取消",
			Err:      ctx.Err(),
		}
	}
	if err := c.checkAutomaticBudget(ctx, site.SanitizeEndpoint(spec.endpoint)); err != nil {
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
			Endpoint: site.SanitizeEndpoint(spec.endpoint),
			Message:  "无效的接口地址",
		}
	}
	if err := c.validateAPIURL(parsedURL); err != nil {
		return nil, err
	}

	params := make([]sign.Pair, 0, len(spec.params)+1)
	params = append(params, spec.params...)
	webIDSynced := false
	if !spec.guestNoCookie && reqCtx.ttwidHash != "" {
		if synced := c.syncedWebIDFor(reqCtx.ttwidHash); synced != "" {
			webIDSynced = replaceSignPair(params, "webid", synced)
		}
	}
	if spec.requireSign && msToken != "" && !hasSignPair(params, "msToken") {
		params = append(params, sign.Pair{Key: "msToken", Value: msToken})
	}

	method, formBody, contentType := http.MethodGet, "", ""
	if spec.form != nil {
		method, formBody, contentType = http.MethodPost, sign.SerializePairs(spec.form), formContentType
	}

	var extraHeaders map[string]string
	if spec.requireSign {
		signed, sErr := c.signer.Sign(ctx, sign.SignInput{
			Endpoint:    parsedURL.Path,
			Params:      params,
			Body:        formBody,
			ContentType: contentType,
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

	var activeGuard *guard.State
	if !spec.guestNoCookie && guard.IsGuardedPath(parsedURL.Path) {
		var fallbackTicket string
		if spec.candidateCookie != nil {
			fallbackTicket = strings.TrimSpace(spec.candidateCookie["d_ticket"])
		} else if usingStore && c.cred != nil {
			fallbackTicket = strings.TrimSpace(c.cred.CookieValue("d_ticket"))
		}
		if gHeaders, gState := c.prepareGuardHeaders(spec, parsedURL.Path, fallbackTicket, usingStore); gState != nil {
			activeGuard = gState
			if len(gHeaders) > 0 {
				if extraHeaders == nil {
					extraHeaders = make(map[string]string, len(gHeaders))
				}
				for k, v := range gHeaders {
					extraHeaders[k] = v
				}
			}
		}
	}

	var reqBody io.Reader
	if method == http.MethodPost {
		reqBody = strings.NewReader(formBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, parsedURL.String(), reqBody)
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
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		netErr := &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindNetwork,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "请求抖音接口失败，请检查网络连接",
			Err:      site.UnwrapURLError(err),
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
			if mErr := c.cred.MergeContextCookies(merged); mErr == nil && strings.TrimSpace(merged["msToken"]) != "" {
				c.clearFallbackMSToken()
			}
		}
	}

	serverWebID := ""
	if !spec.guestNoCookie && resp.StatusCode == http.StatusOK {
		serverWebID = parseServerWebID(resp.Header)
	}

	if activeGuard != nil {
		c.adoptGuardResponse(spec, activeGuard, usingStore, resp.Header, resp.Cookies())
	}

	diag := buildResponseDiagnostics(resp.StatusCode, resp.Header, body, webIDSynced)
	rawResp := &apiRawResponse{
		statusCode:  resp.StatusCode,
		header:      resp.Header.Clone(),
		body:        body,
		sentMSToken: msToken,
		syncedWebID: serverWebID,
		ttwidHash:   reqCtx.ttwidHash,
		webIDGen:    startWebIDGen,
		diag:        diag,
	}
	if spec.emptyMeans != "" && rawResp.statusCode == http.StatusOK && len(bytes.TrimSpace(rawResp.body)) == 0 {
		msg := spec.emptyMeans
		switch {
		case diag.HasBDTuringParams:
			msg = likesBDTuringParamsMessage
		case diag.HasBDTuringVerify:
			msg = likesBDTuringVerifyMessage
		}
		se := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: http.StatusOK,
			Endpoint:   sanitizeEndpointLabel(parsedURL),
			Message:    msg,
		}
		attachResponseDiag(se, diag)
		return nil, se
	}
	if err := c.classifyHTTPAndEnvelopeError(ctx, sanitizeEndpointLabel(parsedURL), rawResp, usingStore); err != nil {
		return rawResp, err
	}
	if usingStore && reqCtx.ttwidHash != "" && serverWebID != "" {
		c.recordSyncedWebID(reqCtx.ttwidHash, serverWebID, startWebIDGen)
	}
	return rawResp, nil
}

func deriveSecFetchSite(reqURL *url.URL, referer string) string {
	if reqURL == nil {
		return "same-origin"
	}
	refURL, err := url.Parse(strings.TrimSpace(referer))
	if err != nil || refURL.Host == "" {
		return "none"
	}
	reqHost := strings.ToLower(reqURL.Hostname())
	refHost := strings.ToLower(refURL.Hostname())
	if site.IsLoopbackHost(reqHost) {
		if strings.HasPrefix(reqURL.Path, "/aweme/") && (refHost == "www.douyin.com" || site.IsLoopbackHost(refHost)) {
			return "same-origin"
		}
		if strings.HasPrefix(reqURL.Path, "/webcast/room/web/") && (refHost == "live.douyin.com" || site.IsLoopbackHost(refHost)) {
			return "same-origin"
		}
		if refHost == "www.douyin.com" && strings.HasPrefix(reqURL.Path, "/webcast/room/web/") {
			return "same-site"
		}
		if (strings.HasPrefix(reqURL.Path, "/webcast/room/reflow/") || strings.HasPrefix(reqURL.Path, "/webcast/room/check_alive/")) &&
			site.MatchDomainSuffix(refHost, []string{"douyin.com"}) {
			return "cross-site"
		}
		return "same-origin"
	}
	return site.DeriveSecFetchSite(referer, reqURL.String())
}

func (c *Client) applyPlayRedirectHeaders(req *http.Request, parsedURL *url.URL, cookieHeader string) {
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	c.profile.ApplyClientHints(req.Header)
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("Sec-Fetch-Site", deriveSecFetchSite(parsedURL, DefaultReferer))
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Dest", "video")
	req.Header.Del("Origin")

	host := ""
	if parsedURL != nil {
		host = strings.ToLower(parsedURL.Hostname())
	}
	allowCookie := site.MatchDomainSuffix(host, []string{"douyin.com"}) ||
		(c.allowLocalhost && site.IsLoopbackHost(host))
	if allowCookie && cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Del("Cookie")
	}
}

func (c *Client) applyAPIHeaders(req *http.Request, referer, origin, cookieHeader string, extra map[string]string) {
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	c.profile.ApplyClientHints(req.Header)
	if referer == "" {
		referer = DefaultReferer
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("Sec-Fetch-Site", deriveSecFetchSite(req.URL, referer))
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
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
	if c.allowLocalhost && site.IsLoopbackHost(host) {
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

func replaceSignPair(pairs []sign.Pair, key, value string) bool {
	replaced := false
	for i := range pairs {
		if pairs[i].Key == key {
			pairs[i].Value = value
			replaced = true
		}
	}
	return replaced
}

func sanitizeEndpointLabel(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// classifyHTTPAndEnvelopeError inspects HTTP status, BDTuring headers, 0-byte bodies, HTML verification pages,
// Argus 403 gateway blocks, verify_center_decision_conf challenges, and status_code per douyin.md §9.1.
func (c *Client) classifyHTTPAndEnvelopeError(ctx context.Context, endpoint string, resp *apiRawResponse, usingStore bool) error {
	trimmed := bytes.TrimSpace(resp.body)
	diag := resp.diag
	if diag.HTTPStatus == 0 {
		diag = buildResponseDiagnostics(resp.statusCode, resp.header, resp.body, false)
	}

	switch {
	case resp.statusCode == http.StatusUnauthorized:
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音登录状态已失效（HTTP 401），请在设置页重新粘贴 Cookie",
		}
		attachResponseDiag(err, diag)
		c.onClassifiedError(ctx, err, usingStore)
		return err

	case resp.statusCode == http.StatusForbidden:
		bodyStr := string(trimmed)
		if strings.Contains(bodyStr, "ArgusSecurityPlugin") || strings.Contains(strings.ToLower(bodyStr), "verify") || diag.HasBDTuringParams || diag.HasBDTuringVerify {
			err := &site.Error{
				Site:       site.Douyin,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: resp.statusCode,
				Endpoint:   endpoint,
				Message:    "抖音安全网关拒绝了当前请求，已停止访问",
			}
			attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
		c.ReportAutomaticError(ctx, err)
		return err
	}

	// BDTuring dynamic verification challenge headers (x-vc-bdturing-parameters / bdturing-verify).
	if diag.HasBDTuringParams || diag.HasBDTuringVerify {
		msg := "抖音触发动态校验拦截（bdturing-verify），已立即停止请求"
		if diag.HasBDTuringParams {
			msg = "抖音触发动态校验拦截（x-vc-bdturing-parameters），已立即停止请求"
		}
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    msg,
		}
		attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
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
		attachResponseDiag(err, diag)
		c.ReportAutomaticError(ctx, err)
		return err
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		se := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音返回 JSON 解析失败",
			Err:        err,
		}
		attachResponseDiag(se, diag)
		return se
	}

	if hasNonEmptyChallengeField(top) {
		err := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: resp.statusCode,
			Endpoint:   endpoint,
			Message:    "抖音触发验证码挑战，已立即停止请求",
		}
		attachResponseDiag(err, diag)
		c.ReportAutomaticError(ctx, err)
		return err
	}

	code := parseJSONInt(top["status_code"])
	statusMsg := parseJSONString(top["status_msg"])
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
				attachResponseDiag(err, diag)
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

	if code == 0 {
		return nil
	}

	se := classifyStatusCode(code, statusMsg, endpoint, resp.statusCode)
	attachResponseDiag(se, diag)
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
	c.clearFallbackMSToken()
	c.clearSyncedWebID()
	c.clearEphemeralGuard()
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
	return c.FetchImageLimited(ctx, rawURL, c.maxImageBytes)
}

// FetchImageLimited fetches up to maxBytes of a Douyin CDN image, preserving full query signatures
// and strictly stripping any Cookie header. Requires active Douyin auth (INV-07).
func (c *Client) FetchImageLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	if err := c.RequireAuth("image"); err != nil {
		return nil, "", err
	}
	normalized := upgradeDouyinHTTPS(strings.TrimSpace(rawURL), c.allowLocalhost)
	transportSpec := c.VODTransport()
	if err := transportSpec.ValidateURL(normalized); err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 {
		maxBytes = c.maxImageBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的图片地址",
			Err:     site.UnwrapURLError(err),
		}
	}
	transportSpec.ApplyHeaders(req)

	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindNetwork,
			Message: "拉取抖音图片失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	body := site.WrapIdleReadCloser(resp.Body, c.mediaIdleTimeout, func() error {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindNetwork,
			Message: "读取抖音图片超时",
		}
	})
	defer body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := &site.Error{
			Site:       site.Douyin,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: resp.StatusCode,
			Message:    fmt.Sprintf("抖音图片返回 HTTP %d", resp.StatusCode),
		}
		if site.ShouldAttemptImageCDNFallback(statusErr, false) {
			if fbData, fbCT, ok := c.tryPublicImageCDNFallback(ctx, normalized, maxBytes); ok {
				return fbData, fbCT, nil
			}
		}
		return nil, "", statusErr
	}

	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain") {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "抖音图片响应类型异常: " + ct,
		}
	}

	if resp.ContentLength > maxBytes {
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "抖音图片体积超出安全上限",
		}
	}

	data, err := readBoundedBody(body, maxBytes)
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
	if site.LooksLikeExpiredPlaceholderImageBytes(data, normalized, 0, 0, false) {
		if fbData, fbCT, ok := c.tryPublicImageCDNFallback(ctx, normalized, maxBytes); ok {
			return fbData, fbCT, nil
		}
		return nil, "", &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "抖音图片已在源站失效",
		}
	}
	return data, ct, nil
}

func (c *Client) tryPublicImageCDNFallback(ctx context.Context, normalized string, maxBytes int64) ([]byte, string, bool) {
	fbCfg := site.PublicImageCDNFallbackConfig{
		Providers:       c.cdnFallbackProviders,
		AllowLocalhost:  c.allowLocalhost,
		WrapTransport:   c.fallbackWrap,
		IdleReadTimeout: c.mediaIdleTimeout,
		UserAgent:       c.profile.UserAgent,
	}
	raw, ct, _, ok := site.FetchPublicImageCDNFallback(ctx, fbCfg, normalized, maxBytes, 0, 0, false)
	return raw, ct, ok
}

func upgradeDouyinHTTPS(raw string, allowLocalhost bool) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(s), "http://") {
		if allowLocalhost {
			if u, err := url.Parse(s); err == nil && site.IsLoopbackHost(u.Hostname()) {
				return s
			}
		}
		return "https://" + s[len("http://"):]
	}
	return s
}
