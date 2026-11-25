// Package weibo implements the Weibo PC Ajax protocol client and cross-site adapter.
package weibo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

const (
	// DefaultUserAgent is the fixed desktop Chrome User-Agent used on PC Ajax and media CDN requests.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

	// DefaultReferer is the Referer header sent on Weibo PC Ajax API and media CDN requests.
	DefaultReferer = "https://weibo.com/"

	// DefaultOrigin is the Origin header used on Weibo POST requests such as /tv/api/component.
	DefaultOrigin = "https://weibo.com"

	defaultMinInterval      = 1200 * time.Millisecond
	defaultJitterMax        = 400 * time.Millisecond
	defaultAPITimeout       = 15 * time.Second
	defaultRedirectTimeout  = 10 * time.Second
	defaultMediaIdleTimeout = 30 * time.Second
	defaultMaxBodyBytes     = 4 * 1024 * 1024 // 4 MiB (JSON API responses)
	defaultMaxImageBytes    = site.DefaultMaxImageBytes
)

// Endpoints holds configurable Weibo PC Ajax API URLs for production and httptest fixtures.
// Per weibo.md §13.1 #2, TRACE uses PC weibo.com endpoints only.
type Endpoints struct {
	PCBasicInfo     string
	ProfileInfo     string
	StatusesShow    string
	StatusesLong    string
	StatusesMyMblog string
	TVComponent     string
	Friends         string
	FollowContent   string
	AllGroups       string
	AllFavorites    string
	LikeList        string
}

// DefaultEndpoints returns the official Weibo PC HTTPS API endpoints.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		PCBasicInfo:     "https://weibo.com/ajax/setting/getBasicInfo",
		ProfileInfo:     "https://weibo.com/ajax/profile/info",
		StatusesShow:    "https://weibo.com/ajax/statuses/show",
		StatusesLong:    "https://weibo.com/ajax/statuses/longtext",
		StatusesMyMblog: "https://weibo.com/ajax/statuses/mymblog",
		TVComponent:     "https://weibo.com/tv/api/component",
		Friends:         "https://weibo.com/ajax/friendships/friends",
		FollowContent:   "https://weibo.com/ajax/profile/followContent",
		AllGroups:       "https://weibo.com/ajax/feed/allGroups",
		AllFavorites:    "https://weibo.com/ajax/favorites/all_fav",
		LikeList:        "https://weibo.com/ajax/statuses/likelist",
	}
}

func (e Endpoints) withDefaults() Endpoints {
	d := DefaultEndpoints()
	if e.PCBasicInfo == "" {
		e.PCBasicInfo = d.PCBasicInfo
	}
	if e.ProfileInfo == "" {
		e.ProfileInfo = d.ProfileInfo
	}
	if e.StatusesShow == "" {
		e.StatusesShow = d.StatusesShow
	}
	if e.StatusesLong == "" {
		e.StatusesLong = d.StatusesLong
	}
	if e.StatusesMyMblog == "" {
		e.StatusesMyMblog = d.StatusesMyMblog
	}
	if e.TVComponent == "" {
		e.TVComponent = d.TVComponent
	}
	if e.Friends == "" {
		e.Friends = d.Friends
	}
	if e.FollowContent == "" {
		e.FollowContent = d.FollowContent
	}
	if e.AllGroups == "" {
		e.AllGroups = d.AllGroups
	}
	if e.AllFavorites == "" {
		e.AllFavorites = d.AllFavorites
	}
	if e.LikeList == "" {
		e.LikeList = d.LikeList
	}
	return e
}

var reChromeMajor = regexp.MustCompile(`(?i)Chrome/([0-9]+)`)

type browserProfile struct {
	UserAgent       string
	SecCHUA         string
	SecCHUAMobile   string
	SecCHUAPlatform string
}

func deriveBrowserProfile(ua string) browserProfile {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		ua = DefaultUserAgent
	}
	major := "136"
	if m := reChromeMajor.FindStringSubmatch(ua); len(m) == 2 {
		major = m[1]
	}
	secPlatform := `"Windows"`
	lowerUA := strings.ToLower(ua)
	switch {
	case strings.Contains(lowerUA, "macintosh") || strings.Contains(lowerUA, "mac os x"):
		secPlatform = `"macOS"`
	case strings.Contains(lowerUA, "linux") && !strings.Contains(lowerUA, "android"):
		secPlatform = `"Linux"`
	}
	return browserProfile{
		UserAgent:       ua,
		SecCHUA:         fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", "Not.A/Brand";v="99"`, major, major),
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: secPlatform,
	}
}

// ClientOptions configures a Weibo protocol Client.
type ClientOptions struct {
	CredStore                 *credential.Store
	Endpoints                 Endpoints
	UserAgent                 string
	MinInterval               time.Duration
	JitterMax                 time.Duration
	APITimeout                time.Duration
	RedirectTimeout           time.Duration
	MediaIdleTimeout          time.Duration
	MaxBodyBytes              int64
	MaxImageBytes             int64
	Transport                 http.RoundTripper
	WrapTransport             func(http.RoundTripper) http.RoundTripper
	ImageCDNFallbackProviders []site.PublicImageCDNProvider
	AllowLocalhost            bool
	Now                       func() time.Time
	Sleep                     func(ctx context.Context, d time.Duration) error
	Entropy                   io.Reader
	PreferredCodec            func() string
	OnXSRFToken               func(token string)
	ImageURLRewriter          func(picID, rawURL string) (preferredURL string, ok bool)
}

// Client is the Weibo PC Ajax protocol client.
// Outbound API requests are serialized (concurrency = 1) with 1200ms + 0..400ms jitter pacing,
// 15s timeout, 4 MiB body cap, disabled auto-redirects, XSRF-TOKEN mirroring, and cookieless CDN access.
type Client struct {
	cred                 *credential.Store
	endpoints            Endpoints
	profile              browserProfile
	httpClient           *http.Client
	redirectClient       *http.Client
	mediaClient          *http.Client
	mediaIdleTimeout     time.Duration
	maxBodyBytes         int64
	maxImageBytes        int64
	allowLocalhost       bool
	imageURLRewriter     func(picID, rawURL string) (string, bool)
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
	onXSRFToken func(token string)
	prefCodecFn func() string
}

// NewClient constructs a configured Weibo protocol Client.
func NewClient(opts ClientOptions) *Client {
	profile := deriveBrowserProfile(opts.UserAgent)
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
		maxImageBytes = defaultMaxImageBytes
	}
	mediaIdleTimeout := opts.MediaIdleTimeout
	if mediaIdleTimeout <= 0 {
		mediaIdleTimeout = defaultMediaIdleTimeout
	}

	apiTransport := opts.Transport
	redirectTransport := opts.Transport
	mediaTransport := opts.Transport
	if apiTransport == nil {
		apiTransport = &http.Transport{
			Proxy:                 site.Proxy,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: apiTimeout,
		}
		redirDialer := &net.Dialer{
			Timeout:   redirTimeout,
			KeepAlive: 30 * time.Second,
		}
		redirGuard := site.NewGuardedMediaDialer(site.Weibo, opts.AllowLocalhost, redirDialer, "已拦截短链指向内网或回环地址")
		redirectTransport = &http.Transport{
			Proxy:                 redirGuard.Proxy,
			DialContext:           redirGuard.DialContext,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: redirTimeout,
		}
		mediaDialer := &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		mediaGuard := site.NewGuardedMediaDialer(site.Weibo, opts.AllowLocalhost, mediaDialer, "已拦截内网或回环微博媒体节点地址")
		mediaTransport = &http.Transport{
			Proxy:                 mediaGuard.Proxy,
			DialContext:           mediaGuard.DialContext,
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
			redirectTransport = site.LoopbackTransport(redirectTransport)
			mediaTransport = site.LoopbackTransport(mediaTransport)
		}
	}
	if opts.WrapTransport != nil {
		apiTransport = opts.WrapTransport(apiTransport)
		redirectTransport = opts.WrapTransport(redirectTransport)
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
		mediaIdleTimeout:     mediaIdleTimeout,
		maxBodyBytes:         maxBytes,
		maxImageBytes:        maxImageBytes,
		allowLocalhost:       opts.AllowLocalhost,
		imageURLRewriter:     opts.ImageURLRewriter,
		cdnFallbackProviders: append([]site.PublicImageCDNProvider(nil), opts.ImageCDNFallbackProviders...),
		fallbackWrap:         fallbackWrap,
		serialGate:           make(chan struct{}, 1),
		minInterval:          minInterval,
		jitterMax:            jitterMax,
		nowFn:                nowFn,
		sleepFn:              sleepFn,
		entropy:              ent,
		onXSRFToken:          opts.OnXSRFToken,
		prefCodecFn:          opts.PreferredCodec,
	}
	c.httpClient = &http.Client{
		Transport:     apiTransport,
		Timeout:       apiTimeout,
		CheckRedirect: noRedirect,
	}
	c.redirectClient = &http.Client{
		Transport:     redirectTransport,
		Timeout:       redirTimeout,
		CheckRedirect: noRedirect,
	}
	c.mediaClient = &http.Client{
		Transport:     mediaTransport,
		Timeout:       0, // Enforced per-read via ResponseHeaderTimeout + WrapIdleReadCloser so large originals/GIFs do not hit the 15s API wall clock
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

// Endpoints returns the client's configured endpoints.
func (c *Client) Endpoints() Endpoints {
	return c.endpoints
}

// UserAgent returns the configured desktop Chrome User-Agent.
func (c *Client) UserAgent() string {
	return c.profile.UserAgent
}

// MediaTransport returns the standard Weibo CDN MediaTransportSpec (cookieless, Referer weibo.com).
func (c *Client) MediaTransport() site.MediaTransportSpec {
	return site.WeiboMediaTransport(c.profile.UserAgent, c.allowLocalhost)
}

// LivePhotoMediaTransport returns the Weibo LivePhoto MediaTransportSpec allowing the exact
// initial hop https://video.weibo.com/media/play without adding weibo.com to global media hosts.
func (c *Client) LivePhotoMediaTransport() site.MediaTransportSpec {
	return site.WeiboLivePhotoMediaTransport(c.profile.UserAgent, c.allowLocalhost)
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

// SetXSRFTokenHandler registers a callback invoked whenever response Set-Cookie refreshes XSRF-TOKEN.
func (c *Client) SetXSRFTokenHandler(fn func(token string)) {
	c.cbMu.Lock()
	c.onXSRFToken = fn
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

// HasAuth reports whether an active Weibo credential is stored.
func (c *Client) HasAuth() bool {
	return c.cred != nil && c.cred.HasActiveCredential()
}

// RequireAuth returns an auth_required *site.Error without issuing any network request when Weibo is logged out.
func (c *Client) RequireAuth(endpoint string) error {
	if c.HasAuth() {
		return nil
	}
	return &site.Error{
		Site:     site.Weibo,
		Kind:     site.ErrKindAuthRequired,
		Endpoint: site.SanitizeEndpoint(endpoint),
		Message:  "请先登录微博",
	}
}

func (c *Client) acquireSlotAndPace(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, &site.Error{
			Site:    site.Weibo,
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
					Site:    site.Weibo,
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
	method          string
	endpoint        string
	query           url.Values
	formBody        string
	contentType     string
	referer         string
	origin          string
	pageReferer     string
	guestNoCookie   bool
	candidateCookie map[string]string
	extraHeaders    map[string]string
	emptyMeans      string
	skipStoreExpire bool
}

// RawProbeResult captures raw HTTP response telemetry for bounded verification tools (e.g. ACT-022).
type RawProbeResult struct {
	Endpoint   string
	StatusCode int
	Header     http.Header
	Body       []byte
}

type apiRawResponse struct {
	statusCode int
	header     http.Header
	body       []byte
}

func (c *Client) resolveCookieContext(spec apiRequestSpec) (cookieHeader, xsrfToken string, usingStore bool, err error) {
	switch {
	case spec.guestNoCookie:
		return "", "", false, nil

	case spec.candidateCookie != nil:
		cleaned := credential.CleanWeiboCookieMap(spec.candidateCookie)
		return formatCookieHeader(cleaned), strings.TrimSpace(cleaned["XSRF-TOKEN"]), false, nil

	default:
		if authErr := c.RequireAuth(spec.endpoint); authErr != nil {
			return "", "", false, authErr
		}
		cookieHdr := c.cred.CookieHeader()
		if cookieHdr == "" {
			return "", "", false, &site.Error{
				Site:     site.Weibo,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: site.SanitizeEndpoint(spec.endpoint),
				Message:  "请先登录微博",
			}
		}
		return cookieHdr, strings.TrimSpace(c.cred.XSRFToken()), true, nil
	}
}

func formatCookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	seen := make(map[string]bool, len(cookies))
	for _, k := range credential.AllowedWeiboCookieKeys {
		if v := cookies[k]; v != "" {
			parts = append(parts, k+"="+v)
			seen[k] = true
		}
	}
	var extra []string
	for k, v := range cookies {
		if !seen[k] && strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		parts = append(parts, k+"="+cookies[k])
	}
	return strings.Join(parts, "; ")
}

func (c *Client) doAPIRequest(ctx context.Context, spec apiRequestSpec) (*apiRawResponse, error) {
	rawResp, se := c.executeAPIRequest(ctx, spec, true)
	return rawResp, se
}

// ProbeRaw executes one bounded, paced PC Ajax API request without envelope rejection so ACT-022
// verification tools can inspect exact status codes and response structures while still
// honoring pacing, host allowlisting, size limits, and XSRF-TOKEN sync.
func (c *Client) ProbeRaw(ctx context.Context, method, endpoint string, query url.Values, formBody string, guestNoCookie bool, pageReferer string) (*RawProbeResult, error) {
	spec := apiRequestSpec{
		method:          method,
		endpoint:        endpoint,
		query:           query,
		formBody:        formBody,
		guestNoCookie:   guestNoCookie,
		pageReferer:     pageReferer,
		skipStoreExpire: true,
	}
	if formBody != "" {
		spec.contentType = "application/x-www-form-urlencoded"
	}
	raw, err := c.executeAPIRequest(ctx, spec, false)
	if err != nil {
		return nil, err
	}
	return &RawProbeResult{
		Endpoint:   site.SanitizeEndpoint(endpoint),
		StatusCode: raw.statusCode,
		Header:     raw.header,
		Body:       raw.body,
	}, nil
}

func (c *Client) executeAPIRequest(ctx context.Context, spec apiRequestSpec, classify bool) (*apiRawResponse, error) {
	cookieHeader, xsrfToken, usingStore, err := c.resolveCookieContext(spec)
	if err != nil {
		c.ReportAutomaticError(ctx, err)
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindNetwork,
			Endpoint: site.SanitizeEndpoint(spec.endpoint),
			Message:  "请求已取消",
			Err:      ctx.Err(),
		}
	}
	if err := c.checkAutomaticBudget(ctx, site.SanitizeEndpoint(spec.endpoint)); err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(strings.TrimSpace(spec.endpoint))
	if err != nil || parsedURL.Host == "" {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindBadRequest,
			Endpoint: site.SanitizeEndpoint(spec.endpoint),
			Message:  "无效的微博接口地址",
		}
	}
	if err := c.validateAPIURL(parsedURL); err != nil {
		return nil, err
	}
	if len(spec.query) > 0 {
		q := parsedURL.Query()
		for k, vals := range spec.query {
			q.Del(k)
			for _, v := range vals {
				q.Add(k, v)
			}
		}
		parsedURL.RawQuery = q.Encode()
	}

	release, err := c.acquireSlotAndPace(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	method := spec.method
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if spec.formBody != "" {
		bodyReader = strings.NewReader(spec.formBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, parsedURL.String(), bodyReader)
	if err != nil {
		return nil, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindBadRequest,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "构造微博请求失败",
			Err:      site.UnwrapURLError(err),
		}
	}

	c.applyAPIHeaders(req, spec, cookieHeader, xsrfToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		netErr := &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindNetwork,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "请求微博接口失败，请检查网络连接",
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
				Site:       site.Weibo,
				Kind:       site.ErrKindBlocked,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "微博响应体积超出 4 MiB 安全上限",
			}
		}
		netErr := &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Endpoint:   sanitizeEndpointLabel(parsedURL),
			Message:    "读取微博响应失败",
			Err:        err,
		}
		c.ReportAutomaticError(ctx, netErr)
		return nil, netErr
	}

	// Sync refreshed XSRF-TOKEN from Set-Cookie (never mutates core identity cookies).
	if !spec.guestNoCookie {
		if refreshedXSRF := extractXSRFTokenFromCookies(resp.Cookies()); refreshedXSRF != "" {
			c.notifyXSRFUpdated(refreshedXSRF, usingStore)
		}
	}

	rawResp := &apiRawResponse{
		statusCode: resp.StatusCode,
		header:     resp.Header.Clone(),
		body:       body,
	}

	if !classify {
		return rawResp, nil
	}

	if se := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
		Endpoint:   sanitizeEndpointLabel(parsedURL),
		StatusCode: rawResp.statusCode,
		Header:     rawResp.header,
		Body:       rawResp.body,
		HasCookie:  cookieHeader != "",
		EmptyMeans: spec.emptyMeans,
	}); se != nil {
		c.onClassifiedError(ctx, se, usingStore && !spec.skipStoreExpire)
		return rawResp, se
	}
	return rawResp, nil
}

func (c *Client) applyAPIHeaders(req *http.Request, spec apiRequestSpec, cookieHeader, xsrfToken string) {
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Sec-Ch-Ua", c.profile.SecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", c.profile.SecCHUAMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", c.profile.SecCHUAPlatform)

	ref := spec.referer
	if ref == "" {
		ref = DefaultReferer
	}
	req.Header.Set("Referer", ref)
	if xsrfToken != "" {
		req.Header.Set("X-XSRF-TOKEN", xsrfToken)
	}
	if spec.origin != "" {
		req.Header.Set("Origin", spec.origin)
	}
	if spec.pageReferer != "" {
		req.Header.Set("Page-Referer", spec.pageReferer)
	}
	if spec.contentType != "" {
		req.Header.Set("Content-Type", spec.contentType)
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Del("Cookie")
	}
	for k, v := range spec.extraHeaders {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}

func extractXSRFTokenFromCookies(cookies []*http.Cookie) string {
	var token string
	for _, ck := range cookies {
		if ck == nil {
			continue
		}
		if ck.Name == "XSRF-TOKEN" {
			v := strings.TrimSpace(ck.Value)
			if v != "" && !strings.ContainsAny(v, "\r\n;") {
				token = v
			}
		}
	}
	return token
}

func (c *Client) notifyXSRFUpdated(token string, usingStore bool) {
	if token == "" {
		return
	}
	if usingStore && c.cred != nil {
		_ = c.cred.MergeContextCookies(map[string]string{"XSRF-TOKEN": token})
	}
	c.cbMu.RLock()
	cb := c.onXSRFToken
	c.cbMu.RUnlock()
	if cb != nil {
		cb(token)
	}
}

func (c *Client) onClassifiedError(ctx context.Context, se *site.Error, markExpired bool) {
	if se == nil {
		return
	}
	// Never clear or expire credentials on risk_control or rate_limited (weibo.md §9.2 & M6-K09).
	if se.Kind == site.ErrKindAuthRequired && markExpired {
		c.markAuthExpired(se.Message)
	}
	c.ReportAutomaticError(ctx, se)
}

func (c *Client) markAuthExpired(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "微博登录状态已失效，请重新粘贴 Cookie 登录"
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

var allowedWeiboAPIHosts = []string{
	"weibo.com",
	"www.weibo.com",
}

func (c *Client) validateAPIURL(u *url.URL) error {
	if u == nil || u.Host == "" || u.User != nil {
		return &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不安全的微博接口地址",
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
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非 HTTPS 微博接口请求",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非常规端口微博接口请求",
		}
	}
	if !isExactHostInList(host, allowedWeiboAPIHosts) {
		return &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "接口域名不在微博允许列表中: " + host,
		}
	}
	return nil
}

func isExactHostInList(host string, allowed []string) bool {
	for _, a := range allowed {
		if host == a {
			return true
		}
	}
	return false
}

func (c *Client) canCallEndpoint(ep string) bool {
	if !c.allowLocalhost {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(ep))
	return err == nil && site.IsLoopbackHost(u.Hostname())
}

var errBodyTooLarge = errors.New("weibo: response body exceeds limit")

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

func sanitizeEndpointLabel(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host + u.Path
}

// ValidateMediaURL checks that rawURL belongs to Weibo's allowed HTTPS media CDN allowlist.
func (c *Client) ValidateMediaURL(rawURL string) error {
	return c.MediaTransport().ValidateURL(rawURL)
}

// FetchImage fetches a Weibo CDN image (cover/avatar/post image) without sending any Cookie.
func (c *Client) FetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	return c.FetchImageLimited(ctx, rawURL, c.maxImageBytes)
}

// FetchImageLimited fetches up to maxBytes of a Weibo CDN image with Referer: https://weibo.com/
// and strictly stripped Cookie header. When Sina CDN returns 403/404/410 or an HTTP-200
// deleted-image placeholder GIF, alternate wx1..wx4 edge hosts and eligible public CDN
// caches (DEC-075) are attempted before returning an error. Requires active Weibo auth (INV-07).
func (c *Client) FetchImageLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	if err := c.RequireAuth("image"); err != nil {
		return nil, "", err
	}
	normalized := normalizeWeiboMediaURL(rawURL, c.allowLocalhost)
	transportSpec := c.MediaTransport()
	if err := transportSpec.ValidateURL(normalized); err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 {
		maxBytes = c.maxImageBytes
	}

	data, ct, sawPlaceholder, err := c.fetchSingleWeiboImage(ctx, transportSpec, normalized, maxBytes)
	if err == nil {
		return data, ct, nil
	}
	if !site.ShouldAttemptImageCDNFallback(err, sawPlaceholder) {
		return nil, "", err
	}

	for _, altURL := range alternateWeiboEdgeURLs(normalized) {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if transportSpec.ValidateURL(altURL) != nil {
			continue
		}
		altData, altCT, altPlaceholder, altErr := c.fetchSingleWeiboImage(ctx, transportSpec, altURL, maxBytes)
		if altErr == nil {
			return altData, altCT, nil
		}
		if altPlaceholder {
			sawPlaceholder = true
		}
	}

	fbCfg := site.PublicImageCDNFallbackConfig{
		Providers:       c.cdnFallbackProviders,
		AllowLocalhost:  c.allowLocalhost,
		WrapTransport:   c.fallbackWrap,
		IdleReadTimeout: c.mediaIdleTimeout,
		UserAgent:       c.profile.UserAgent,
	}
	if fbData, fbCT, _, fbOK := site.FetchPublicImageCDNFallback(ctx, fbCfg, normalized, maxBytes, 0, 0, false); fbOK {
		return fbData, fbCT, nil
	}
	return nil, "", err
}

func (c *Client) fetchSingleWeiboImage(ctx context.Context, transportSpec site.MediaTransportSpec, targetURL string, maxBytes int64) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, "", false, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博图片地址",
			Err:     site.UnwrapURLError(err),
		}
	}
	transportSpec.ApplyHeaders(req)

	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, "", false, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindNetwork,
			Message: "拉取微博图片失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	body := site.WrapIdleReadCloser(resp.Body, c.mediaIdleTimeout, func() error {
		return &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindNetwork,
			Message: "读取微博图片超时",
		}
	})
	defer body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", false, &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: resp.StatusCode,
			Message:    fmt.Sprintf("微博图片返回 HTTP %d", resp.StatusCode),
		}
	}

	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain") {
		return nil, "", false, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "微博图片响应类型异常: " + ct,
		}
	}

	if resp.ContentLength > maxBytes {
		return nil, "", false, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "微博图片体积超出安全上限",
		}
	}

	data, err := readBoundedBody(body, maxBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, "", false, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindBlocked,
				Message: "微博图片体积超出安全上限",
			}
		}
		return nil, "", false, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindNetwork,
			Message: "读取微博图片失败",
			Err:     err,
		}
	}
	if looksLikeHTMLOrJSONPayload(data) {
		return nil, "", false, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBlocked,
			Message: "微博图片响应内容疑似伪装页面",
		}
	}
	if site.LooksLikeExpiredPlaceholderImageBytes(data, targetURL, 0, 0, false) {
		return nil, "", true, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindUnavailable,
			Message: "微博图片已在源站失效",
		}
	}
	return data, ct, false, nil
}

func alternateWeiboEdgeURLs(rawURL string) []string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host != "wx1.sinaimg.cn" && host != "wx2.sinaimg.cn" && host != "wx3.sinaimg.cn" && host != "wx4.sinaimg.cn" {
		return nil
	}
	var out []string
	for _, h := range []string{"wx1.sinaimg.cn", "wx2.sinaimg.cn", "wx3.sinaimg.cn", "wx4.sinaimg.cn"} {
		if h == host {
			continue
		}
		cp := *u
		cp.Host = h
		out = append(out, cp.String())
	}
	return out
}

func looksLikeHTMLOrJSONPayload(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return true
	}
	if trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '<' {
		lower := strings.ToLower(string(trimmed[:min(len(trimmed), 256)]))
		if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") || strings.HasPrefix(lower, "<head") || strings.HasPrefix(lower, "<script") {
			return true
		}
		if trimmed[0] == '{' && strings.Contains(lower, `"ok"`) {
			return true
		}
	}
	return false
}

func normalizeWeiboMediaURL(raw string, allowLocalhost bool) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "//") {
		return "https:" + s
	}
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
