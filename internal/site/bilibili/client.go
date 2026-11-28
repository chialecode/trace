package bilibili

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

const (
	DefaultUserAgent       = site.DefaultUserAgent
	DefaultReferer         = "https://www.bilibili.com/"
	DefaultLiveReferer     = "https://live.bilibili.com/"
	DefaultOrigin          = "https://www.bilibili.com"
	DefaultAPITimeout      = 15 * time.Second
	DefaultIdleTimeout     = 30 * time.Second
	DefaultPacingMinWait   = 500 * time.Millisecond
	DefaultPacingJitterMax = 200 * time.Millisecond
	MaxAPIResponseBytes    = 4 * 1024 * 1024
)

// Endpoints holds configurable base URLs so unit tests can point everything to 127.0.0.1 httptest servers.
type Endpoints struct {
	APIBase      string // default "https://api.bilibili.com"
	PassportBase string // default "https://passport.bilibili.com"
	WWWBase      string // default "https://www.bilibili.com"
	SpaceBase    string // default "https://space.bilibili.com"
	DynamicBase  string // default "https://t.bilibili.com"
	LiveAPIBase  string // default "https://api.live.bilibili.com"
	LiveWebBase  string // default "https://live.bilibili.com"
}

// DefaultEndpoints returns standard Bilibili production base URLs.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		APIBase:      "https://api.bilibili.com",
		PassportBase: "https://passport.bilibili.com",
		WWWBase:      "https://www.bilibili.com",
		SpaceBase:    "https://space.bilibili.com",
		DynamicBase:  "https://t.bilibili.com",
		LiveAPIBase:  "https://api.live.bilibili.com",
		LiveWebBase:  "https://live.bilibili.com",
	}
}

// ClientOptions configures a Bilibili site Client.
type ClientOptions struct {
	CredStore        *credential.Store
	Endpoints        Endpoints
	UserAgent        string
	APITimeout       time.Duration
	MediaIdleTimeout time.Duration
	PacingInterval   time.Duration
	PacingJitterMax  time.Duration // <0 disables jitter; 0 uses DefaultPacingJitterMax unless PacingInterval < 0
	PacingJitterFunc func(maxJitter time.Duration) time.Duration
	AllowLocalhost   bool
	NowFunc          func() time.Time
	// WrapTransport, when set, wraps both the API and media transports. Only
	// authorized site checks use it to gate each request before it is sent.
	WrapTransport             func(http.RoundTripper) http.RoundTripper
	ImageCDNFallbackProviders []site.PublicImageCDNProvider
}

// Client implements the Bilibili site adapter with dual HTTP clients, global pacing, WBI signing, and QR/refresh flows.
type Client struct {
	credStore            *credential.Store
	endpoints            Endpoints
	profile              site.BrowserProfile
	apiClient            *http.Client
	mediaClient          *http.Client
	liveMediaClient      *http.Client
	mediaIdleTimeout     time.Duration
	pacingInterval       time.Duration
	pacingJitterMax      time.Duration
	pacingJitterFunc     func(maxJitter time.Duration) time.Duration
	authExpired          func()
	wbiFetchedAt         time.Time
	allowLocalhost       bool
	cdnFallbackProviders []site.PublicImageCDNProvider
	fallbackWrap         func(http.RoundTripper) http.RoundTripper
	nowFunc              func() time.Time

	paceMu           sync.Mutex
	sequenceMu       sync.RWMutex
	apiGate          chan struct{}
	automationMu     sync.RWMutex
	automaticGuard   func() error
	automaticStopped func(string)
	accountReplacing func()
	lastReq          time.Time
	wbiMu            sync.Mutex
	imgKey           string
	subKey           string
	mixinKey         string

	qrMu       sync.Mutex
	qrRevision uint64
	activeQR   *qrFlowState
	qrHistory  map[string]string // flowId -> "active" | "canceled" | "completed"

	webIDMu    sync.Mutex
	webIDCache map[int64]cachedWebID
}

type cachedWebID struct {
	Value     string
	FetchedAt time.Time
}

type qrFlowState struct {
	FlowID    string
	QRCodeKey string
	URL       string
	PNG       []byte
	ExpiresAt time.Time
	Polling   bool
}

type sequenceContextKey struct{}

func (c *Client) beginSequence(ctx context.Context, write bool) (context.Context, func()) {
	if ctx.Value(sequenceContextKey{}) == c {
		return ctx, func() {}
	}
	if write {
		c.sequenceMu.Lock()
		return context.WithValue(ctx, sequenceContextKey{}, c), c.sequenceMu.Unlock
	}
	c.sequenceMu.RLock()
	return context.WithValue(ctx, sequenceContextKey{}, c), c.sequenceMu.RUnlock
}

// NewClient constructs a Bilibili site adapter client.
func NewClient(opts ClientOptions) *Client {
	ep := opts.Endpoints
	def := DefaultEndpoints()
	if ep.APIBase == "" {
		ep.APIBase = def.APIBase
	}
	if ep.PassportBase == "" {
		ep.PassportBase = def.PassportBase
	}
	if ep.WWWBase == "" {
		ep.WWWBase = def.WWWBase
	}
	if ep.SpaceBase == "" {
		ep.SpaceBase = def.SpaceBase
	}
	if ep.DynamicBase == "" {
		ep.DynamicBase = def.DynamicBase
	}
	if ep.LiveAPIBase == "" {
		ep.LiveAPIBase = def.LiveAPIBase
	}
	if ep.LiveWebBase == "" {
		ep.LiveWebBase = def.LiveWebBase
	}

	apiTimeout := opts.APITimeout
	if apiTimeout <= 0 {
		apiTimeout = DefaultAPITimeout
	}
	idleTimeout := opts.MediaIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	profile := site.DeriveBrowserProfile(opts.UserAgent)
	pacing := opts.PacingInterval
	if pacing == 0 {
		pacing = DefaultPacingMinWait
	}
	var jitterMax time.Duration
	switch {
	case opts.PacingInterval < 0 || opts.PacingJitterMax < 0:
		jitterMax = 0
	case opts.PacingJitterMax > 0:
		jitterMax = opts.PacingJitterMax
	case opts.PacingInterval == 0:
		jitterMax = DefaultPacingJitterMax
	default:
		// Scale default jitter proportionally to custom positive PacingInterval,
		// capped at DefaultPacingJitterMax, so small custom intervals (e.g. 1ms
		// in tests) do not unexpectedly sleep up to 200ms.
		jitterMax = min(DefaultPacingJitterMax, pacing*2/5)
	}
	nowFn := opts.NowFunc
	if nowFn == nil {
		nowFn = time.Now
	}

	apiTransport := &http.Transport{
		Proxy: site.Proxy,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: apiTimeout,
	}

	mediaDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	mediaGuard := site.NewGuardedMediaDialer(site.Bilibili, opts.AllowLocalhost, mediaDialer, "已拦截内网或回环媒体节点地址")
	mediaTransport := &http.Transport{
		Proxy:                 mediaGuard.Proxy,
		DialContext:           mediaGuard.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}

	c := &Client{
		credStore:            opts.CredStore,
		endpoints:            ep,
		profile:              profile,
		mediaIdleTimeout:     idleTimeout,
		pacingInterval:       pacing,
		pacingJitterMax:      jitterMax,
		pacingJitterFunc:     opts.PacingJitterFunc,
		allowLocalhost:       opts.AllowLocalhost,
		cdnFallbackProviders: append([]site.PublicImageCDNProvider(nil), opts.ImageCDNFallbackProviders...),
		fallbackWrap:         opts.WrapTransport,
		nowFunc:              nowFn,
		qrHistory:            make(map[string]string),
		webIDCache:           make(map[int64]cachedWebID),
		apiGate:              make(chan struct{}, 1),
	}

	c.apiClient = &http.Client{
		Timeout:   apiTimeout,
		Transport: apiTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	c.mediaClient = &http.Client{
		Timeout:   0, // No total timeout on media streams; enforced via ResponseHeaderTimeout + WrapIdleReadCloser
		Transport: mediaTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("bilibili: media redirect limit exceeded")
			}
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			if err := c.ValidateMediaURL(req.URL.String()); err != nil {
				return fmt.Errorf("bilibili: blocked media redirect host %s", req.URL.Host)
			}
			return nil
		},
	}

	c.liveMediaClient = &http.Client{
		Timeout:   0, // No total timeout on live streams; enforced via ResponseHeaderTimeout + idle reader
		Transport: mediaTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return &SiteError{
					Site:    site.Bilibili,
					Kind:    ErrKindBlocked,
					Message: "直播流重定向超过 5 跳上限",
				}
			}
			if err := c.ValidateLiveMediaURL(req.URL.String(), true); err != nil {
				return err
			}
			site.BilibiliLiveTransport(c.allowLocalhost).ApplyHeaders(req)
			return nil
		},
	}

	if opts.AllowLocalhost {
		// Fixture clients must never escape to the real site, including redirects.
		c.apiClient.Transport = site.LoopbackTransport(apiTransport)
		c.mediaClient.Transport = site.LoopbackTransport(mediaTransport)
		c.liveMediaClient.Transport = site.LoopbackTransport(mediaTransport)
	}
	if opts.WrapTransport != nil {
		c.apiClient.Transport = opts.WrapTransport(c.apiClient.Transport)
		c.mediaClient.Transport = opts.WrapTransport(c.mediaClient.Transport)
		c.liveMediaClient.Transport = opts.WrapTransport(c.liveMediaClient.Transport)
	}
	return c
}

// AllowLocalhost reports whether this client is configured for local synthetic/fixture servers.
func (c *Client) AllowLocalhost() bool {
	return c != nil && c.allowLocalhost
}

// HasAuth reports whether the client currently holds an active login credential.
func (c *Client) HasAuth() bool {
	return c != nil && c.credStore != nil && c.credStore.HasActiveCredential()
}

// RequireAuth returns ErrKindAuthRequired if no active credential is available (INV-07).
func (c *Client) RequireAuth(endpoint string) error {
	if !c.HasAuth() {
		return &SiteError{
			Kind:     ErrKindAuthRequired,
			Endpoint: endpoint,
			Message:  "当前处于未登录状态，请先扫码或导入 Cookie 登录哔哩哔哩",
		}
	}
	return nil
}

// MediaHTTPClient returns the configured media HTTP client and idle timeout for the download/live engines.
func (c *Client) MediaHTTPClient() (*http.Client, time.Duration) {
	return c.mediaClient, c.mediaIdleTimeout
}

// LiveMediaHTTPClient returns the configured HTTP client and idle timeout for Bilibili
// live streams (permits scheduled public HTTPS:443 vendor CDN redirects with credentials
// stripped and private/loopback IP dialer guards enforced).
func (c *Client) LiveMediaHTTPClient() (*http.Client, time.Duration) {
	return c.liveMediaClient, c.mediaIdleTimeout
}

// MaxImageBytes is the maximum image/preview payload accepted by FetchImageLimited.
const MaxImageBytes = site.DefaultMaxImageBytes

// FetchImage reads a public original image without sending login cookies.
func (c *Client) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	return c.FetchImageLimited(ctx, rawURL, MaxImageBytes)
}

// FetchImageLimited applies the caller's smaller byte limit before reading a
// public image body. History thumbnails use 10 MiB; original viewing uses 128 MiB.
func (c *Client) FetchImageLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > MaxImageBytes {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片大小上限无效"}
	}
	if err := c.RequireAuth("image"); err != nil {
		return nil, err
	}
	if err := c.ValidateMediaURL(rawURL); err != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片地址不受支持"}
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片地址不受支持"}
	}
	u.Path = strings.SplitN(u.Path, "@", 2)[0]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片地址无效"}
	}
	req.Header.Set("User-Agent", c.profile.UserAgent)
	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, &SiteError{Kind: ErrKindNetwork, Message: "原图加载失败，请重试", Err: site.UnwrapURLError(err)}
	}
	body := c.WrapMediaReader(resp.Body)
	defer body.Close()
	if resp.StatusCode != http.StatusOK {
		statusErr := classifyHTTPStatus(resp.StatusCode, "image")
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			if fbData, ok := c.tryPublicImageCDNFallback(ctx, u.String(), maxBytes); ok {
				return fbData, nil
			}
		}
		return nil, statusErr
	}
	if resp.ContentLength > maxBytes {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片超过允许的大小上限"}
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, &SiteError{Kind: ErrKindNetwork, Message: "原图读取失败，请重试"}
	}
	if int64(len(raw)) > maxBytes {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片超过允许的大小上限"}
	}
	if site.LooksLikeExpiredPlaceholderImageBytes(raw, u.String(), 0, 0, false) {
		if fbData, ok := c.tryPublicImageCDNFallback(ctx, u.String(), maxBytes); ok {
			return fbData, nil
		}
		return nil, &SiteError{Kind: ErrKindUnavailable, Message: "图片已在源站失效"}
	}
	return raw, nil
}

func (c *Client) tryPublicImageCDNFallback(ctx context.Context, cleanURL string, maxBytes int64) ([]byte, bool) {
	fbCfg := site.PublicImageCDNFallbackConfig{
		Providers:       c.cdnFallbackProviders,
		AllowLocalhost:  c.allowLocalhost,
		WrapTransport:   c.fallbackWrap,
		IdleReadTimeout: c.mediaIdleTimeout,
		UserAgent:       c.profile.UserAgent,
	}
	raw, _, _, ok := site.FetchPublicImageCDNFallback(ctx, fbCfg, cleanURL, maxBytes, 0, 0, false)
	return raw, ok
}

// WrapMediaReader wraps an io.ReadCloser with a per-read idle timeout.
func (c *Client) WrapMediaReader(rc io.ReadCloser) io.ReadCloser {
	return site.WrapIdleReadCloser(rc, c.mediaIdleTimeout, func() error {
		return &SiteError{
			Kind:    ErrKindNetwork,
			Message: "媒体流读取超时（超过 30 秒无进展）",
		}
	})
}

func defaultPacingJitter(maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(raw[:]) % uint64(maxJitter+1))
}

func (c *Client) nextPacingJitter() time.Duration {
	if c.pacingJitterMax <= 0 {
		return 0
	}
	var jitter time.Duration
	if c.pacingJitterFunc != nil {
		jitter = c.pacingJitterFunc(c.pacingJitterMax)
	} else {
		jitter = defaultPacingJitter(c.pacingJitterMax)
	}
	if jitter < 0 {
		return 0
	}
	if jitter > c.pacingJitterMax {
		return c.pacingJitterMax
	}
	return jitter
}

func (c *Client) waitPacing(ctx context.Context) error {
	if c.pacingInterval <= 0 {
		return nil
	}
	c.paceMu.Lock()
	now := time.Now()
	var wait time.Duration
	if !c.lastReq.IsZero() {
		next := c.lastReq.Add(c.pacingInterval + c.nextPacingJitter())
		if next.After(now) {
			wait = next.Sub(now)
			c.lastReq = next
		} else {
			c.lastReq = now
		}
	} else {
		c.lastReq = now
	}
	c.paceMu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	TTL     int             `json:"ttl"`
	Data    json.RawMessage `json:"data"`
	Result  json.RawMessage `json:"result"`
}

func (e *apiEnvelope) UnmarshalJSON(raw []byte) error {
	type envelope apiEnvelope
	var decoded struct {
		*envelope
		Code *int `json:"code"`
	}
	decoded.envelope = (*envelope)(e)
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if decoded.Code == nil {
		return errors.New("接口响应缺少 code")
	}
	e.Code = *decoded.Code
	return nil
}

func (c *Client) beginAPI(ctx context.Context) (func(), error) {
	select {
	case c.apiGate <- struct{}{}:
		if err := c.waitPacing(ctx); err != nil {
			<-c.apiGate
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			<-c.apiGate
			return nil, err
		}
		if err := c.allowAutomaticRequest(ctx); err != nil {
			<-c.apiGate
			return nil, err
		}
		return func() { <-c.apiGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type apiRequestKind int

const (
	reqKindJSON apiRequestKind = iota
	reqKindHTML
	reqKindBinary
)

type apiRequestOptions struct {
	referer        string
	omitReferer    bool
	accept         string
	origin         string
	kind           apiRequestKind
	explicitCookie string
}

func (c *Client) spaceReferer(mid int64) string {
	if mid > 0 {
		return fmt.Sprintf("https://space.bilibili.com/%d/", mid)
	}
	return DefaultReferer
}

func (c *Client) videoReferer(bvid string) string {
	bvid = strings.TrimSpace(bvid)
	if bvid != "" {
		return "https://www.bilibili.com/video/" + bvid + "/"
	}
	return DefaultReferer
}

func (c *Client) secFetchSite(referer string, target *url.URL) string {
	if target == nil {
		return site.DeriveSecFetchSite(referer, "")
	}
	if strings.TrimSpace(referer) == "" {
		return "none"
	}
	targetURL := target.String()
	if c.allowLocalhost && site.IsLoopbackHost(target.Hostname()) {
		p := target.Path
		switch {
		case strings.HasPrefix(p, "/b23/"):
			targetURL = "https://b23.tv" + p
		case strings.HasPrefix(p, "/room/"), strings.HasPrefix(p, "/xlive/"):
			targetURL = "https://api.live.bilibili.com" + p
		case strings.HasPrefix(p, "/x/passport-login/"):
			targetURL = "https://passport.bilibili.com" + p
		case strings.HasPrefix(p, "/x/"):
			targetURL = "https://api.bilibili.com" + p
		case strings.HasPrefix(p, "/correspond/"):
			targetURL = "https://www.bilibili.com" + p
		default:
			targetURL = "https://space.bilibili.com" + p
		}
	}
	return site.DeriveSecFetchSite(referer, targetURL)
}

func (c *Client) doAPIRequest(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool) (*http.Response, []byte, error) {
	return c.doAPIRequestWithOptions(ctx, method, fullURL, body, contentType, includeCookies, apiRequestOptions{})
}

func (c *Client) doAPIRequestWithCookie(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool, explicitCookie string) (*http.Response, []byte, error) {
	return c.doAPIRequestWithOptions(ctx, method, fullURL, body, contentType, includeCookies, apiRequestOptions{explicitCookie: explicitCookie})
}

func (c *Client) doAPIRequestWithOptions(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool, opts apiRequestOptions) (*http.Response, []byte, error) {
	ctx, unlock := c.beginSequence(ctx, false)
	defer unlock()
	safeEndpoint := site.SanitizeEndpoint(fullURL)
	if includeCookies && opts.explicitCookie == "" {
		if err := c.RequireAuth(safeEndpoint); err != nil {
			return nil, nil, err
		}
	}
	endAPI, err := c.beginAPI(ctx)
	if err != nil {
		var siteErr *SiteError
		if errors.As(err, &siteErr) {
			return nil, nil, err
		}
		return nil, nil, &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: safeEndpoint,
			Message:  "请求已取消",
			Err:      site.UnwrapURLError(err),
		}
	}
	defer endAPI()

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, nil, &SiteError{
			Kind:     ErrKindBadRequest,
			Endpoint: safeEndpoint,
			Message:  "构造请求失败",
			Err:      site.UnwrapURLError(err),
		}
	}
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	c.profile.ApplyClientHints(req.Header)

	accept := strings.TrimSpace(opts.accept)
	if accept == "" {
		switch opts.kind {
		case reqKindHTML:
			accept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
		case reqKindBinary:
			accept = "*/*"
		default:
			accept = "application/json, text/plain, */*"
		}
	}
	req.Header.Set("Accept", accept)

	referer := ""
	if !opts.omitReferer {
		referer = strings.TrimSpace(opts.referer)
		if referer == "" {
			referer = DefaultReferer
		}
		req.Header.Set("Referer", referer)
	}
	req.Header.Set("Sec-Fetch-Site", c.secFetchSite(referer, req.URL))
	if opts.kind == reqKindHTML {
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-User", "?1")
	} else {
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
	}
	origin := strings.TrimSpace(opts.origin)
	if origin == "" && method == http.MethodPost {
		origin = DefaultOrigin
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if opts.explicitCookie != "" {
		req.Header.Set("Cookie", opts.explicitCookie)
	} else if includeCookies && c.credStore != nil {
		if ck := c.credStore.CookieHeader(); ck != "" {
			req.Header.Set("Cookie", ck)
		}
	}

	resp, err := c.apiClient.Do(req)
	if err != nil {
		return nil, nil, &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: safeEndpoint,
			Message:  "网络请求失败",
			Err:      site.UnwrapURLError(err),
		}
	}
	defer resp.Body.Close()

	limit := int64(MaxAPIResponseBytes)
	if strings.Contains(fullURL, "/correspond/1/") || strings.HasPrefix(fullURL, strings.TrimRight(c.endpoints.SpaceBase, "/")+"/") && !strings.Contains(fullURL, "/x/") {
		limit = 2 * 1024 * 1024
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp, nil, &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: safeEndpoint,
			Message:  "读取响应失败",
			Err:      site.UnwrapURLError(err),
		}
	}

	if int64(len(raw)) > limit {
		return resp, nil, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: safeEndpoint, Message: "接口响应超过大小上限"}
	}
	if resp.StatusCode != http.StatusOK {
		return resp, raw, classifyHTTPStatus(resp.StatusCode, safeEndpoint)
	}
	return resp, raw, nil
}

func (c *Client) doBiliJSON(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool, out any) (*http.Response, error) {
	return c.doBiliJSONWithOptions(ctx, method, fullURL, body, contentType, includeCookies, out, true, apiRequestOptions{})
}

func (c *Client) doBiliJSONWithReport(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool, out any, reportAuto bool) (response *http.Response, result error) {
	return c.doBiliJSONWithOptions(ctx, method, fullURL, body, contentType, includeCookies, out, reportAuto, apiRequestOptions{})
}

func (c *Client) doBiliJSONWithOptions(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool, out any, reportAuto bool, reqOpts apiRequestOptions) (response *http.Response, result error) {
	if reportAuto {
		defer func() { c.ReportAutomaticError(ctx, result) }()
	}
	safeEndpoint := site.SanitizeEndpoint(fullURL)
	resp, raw, err := c.doAPIRequestWithOptions(ctx, method, fullURL, body, contentType, includeCookies, reqOpts)
	if err != nil {
		return resp, err
	}

	payload, envErr := c.decodeBiliEnvelope(raw, safeEndpoint, true)
	if envErr != nil {
		return resp, envErr
	}

	if nd, ok := out.(nullableData); ok {
		if len(payload) == 0 || string(payload) == "null" {
			return resp, nil
		}
		out = nd.out
	}
	if out != nil {
		if len(payload) == 0 || string(payload) == "null" {
			return resp, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: safeEndpoint, Message: "接口响应缺少 data"}
		}
		if emptyArrayForMap(payload, out) {
			return resp, nil
		}
		if err := json.Unmarshal(payload, out); err != nil {
			return resp, &SiteError{
				Kind:     ErrKindSchemaChanged,
				Endpoint: safeEndpoint,
				Message:  "解析接口数据字段失败",
				Err:      err,
			}
		}
	}
	return resp, nil
}

func (c *Client) decodeBiliEnvelope(raw []byte, safeEndpoint string, expireOn101 bool) (json.RawMessage, error) {
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: safeEndpoint,
			Message:  "接口返回非预期 JSON 结构",
			Err:      err,
		}
	}

	payload := env.Data
	if len(payload) == 0 {
		payload = env.Result
	}
	hasVoucher := bytesContainsVoucher(payload)
	if env.Code != 0 || hasVoucher {
		msg := env.Message
		if msg == "" {
			msg = env.Msg
		}
		if expireOn101 && env.Code == -101 && c.credStore != nil {
			c.expireAuth()
		}
		return nil, classifyBiliCode(env.Code, msg, hasVoucher, safeEndpoint)
	}
	return payload, nil
}

// emptyArrayForMap reports whether an object-keyed result came back as an
// empty JSON array. Bilibili's live endpoints serialize an empty map that way:
// get_status_info_by_uids answers `"data": []` when no requested UID has a
// live room. The map stays empty instead of failing as a schema change.
func emptyArrayForMap(payload json.RawMessage, out any) bool {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Map {
		return false
	}
	return strings.Join(strings.Fields(string(payload)), "") == "[]"
}

// doWBIGet executes a WBI-signed GET request. On a single -352 response (without v_voucher),
// it refreshes the WBI keys once from /x/web-interface/nav and retries once before reporting
// an automatic-request circuit break.
func (c *Client) doWBIGet(ctx context.Context, path string, params url.Values, out any) error {
	return c.doWBIGetWithReferer(ctx, path, params, out, "")
}

func (c *Client) doWBIGetWithReferer(ctx context.Context, path string, params url.Values, out any, referer string) error {
	mixin, err := c.ensureMixinKey(ctx, false)
	if err != nil {
		return err
	}
	signed := SignWBIParams(params, mixin, c.nowFunc().Unix())
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + path + "?" + EncodeWBIQuery(signed)
	reqOpts := apiRequestOptions{referer: referer}

	_, err = c.doBiliJSONWithOptions(ctx, http.MethodGet, fullURL, nil, "", true, out, false, reqOpts)
	if err == nil {
		return nil
	}

	var se *SiteError
	if errors.As(err, &se) && se.Code == -352 && !strings.Contains(se.Message, "v_voucher") {
		// Refresh WBI keys once and retry once before tripping automatic backoff.
		mixin2, rErr := c.ensureMixinKey(ctx, true)
		if rErr != nil {
			c.ReportAutomaticError(ctx, err)
			return err
		}
		signed2 := SignWBIParams(params, mixin2, c.nowFunc().Unix())
		retryURL := strings.TrimRight(c.endpoints.APIBase, "/") + path + "?" + EncodeWBIQuery(signed2)
		_, err2 := c.doBiliJSONWithOptions(ctx, http.MethodGet, retryURL, nil, "", true, out, true, reqOpts)
		return err2
	}
	c.ReportAutomaticError(ctx, err)
	return err
}

func (c *Client) ensureMixinKey(ctx context.Context, forceRefresh bool) (string, error) {
	c.wbiMu.Lock()
	current := c.mixinKey
	fetchedAt := c.wbiFetchedAt
	c.wbiMu.Unlock()
	if current != "" && !forceRefresh && c.nowFunc().Sub(fetchedAt) < 30*time.Minute {
		return current, nil
	}

	nav, err := c.fetchNav(ctx)
	if err != nil {
		return "", err
	}
	imgKey := ExtractWBIKey(nav.WbiImg.ImgURL)
	subKey := ExtractWBIKey(nav.WbiImg.SubURL)
	mixin := MixinKey(imgKey, subKey)
	if mixin == "" {
		return "", &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: "/x/web-interface/nav",
			Message:  "无法从 nav 接口提取有效的 WBI 密钥",
		}
	}

	c.wbiMu.Lock()
	c.imgKey = imgKey
	c.subKey = subKey
	c.mixinKey = mixin
	c.wbiFetchedAt = c.nowFunc()
	c.wbiMu.Unlock()
	return mixin, nil
}

// SetWBIKeys sets WBI img_key and sub_key directly (useful in unit tests).
func (c *Client) SetWBIKeys(imgKey, subKey string) {
	c.wbiMu.Lock()
	defer c.wbiMu.Unlock()
	c.imgKey = imgKey
	c.subKey = subKey
	c.mixinKey = MixinKey(imgKey, subKey)
	c.wbiFetchedAt = c.nowFunc()
}

type navData struct {
	IsLogin bool   `json:"isLogin"`
	Mid     int64  `json:"mid"`
	Uname   string `json:"uname"`
	Face    string `json:"face"`
	WbiImg  struct {
		ImgURL string `json:"img_url"`
		SubURL string `json:"sub_url"`
	} `json:"wbi_img"`
}

func (c *Client) fetchNav(ctx context.Context) (nav navData, result error) {
	defer func() { c.ReportAutomaticError(ctx, result) }()
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/nav"
	safeEndpoint := site.SanitizeEndpoint(fullURL)
	_, raw, err := c.doAPIRequest(ctx, http.MethodGet, fullURL, nil, "", true)
	if err != nil {
		return navData{}, err
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return navData{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: safeEndpoint,
			Message:  "解析 nav 响应失败",
			Err:      err,
		}
	}
	hasVoucher := bytesContainsVoucher(env.Data)
	if hasVoucher || env.Code == -101 {
		if env.Code == -101 && c.credStore != nil {
			c.expireAuth()
		}
		return navData{}, classifyBiliCode(env.Code, env.Message, hasVoucher, safeEndpoint)
	}
	var data navData
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &data)
	}
	if env.Code != 0 && data.WbiImg.ImgURL == "" {
		return navData{}, classifyBiliCode(env.Code, env.Message, false, safeEndpoint)
	}
	return data, nil
}

// ValidateCandidateCookies validates candidate manual cookies via /x/web-interface/nav without modifying the credential store.
func (c *Client) ValidateCandidateCookies(ctx context.Context, cookies map[string]string) (int64, string, string, error) {
	ctx, unlock := c.beginSequence(ctx, false)
	defer unlock()
	filtered := credential.FilterCookieMap(cookies)
	if filtered["SESSDATA"] == "" {
		return 0, "", "", &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "缺少必需的 SESSDATA 字段",
		}
	}
	var pairs []string
	for _, k := range credential.AllowedCookieKeys {
		if v := filtered[k]; v != "" {
			pairs = append(pairs, k+"="+v)
		}
	}
	cookieHeader := strings.Join(pairs, "; ")

	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/nav"
	safeEndpoint := site.SanitizeEndpoint(fullURL)
	_, raw, err := c.doAPIRequestWithCookie(ctx, http.MethodGet, fullURL, nil, "", false, cookieHeader)
	if err != nil {
		return 0, "", "", err
	}
	payload, envErr := c.decodeBiliEnvelope(raw, safeEndpoint, false)
	if envErr != nil {
		return 0, "", "", envErr
	}

	var data navData
	if err := json.Unmarshal(payload, &data); err != nil {
		return 0, "", "", &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: safeEndpoint,
			Message:  "解析 nav 数据失败",
			Err:      err,
		}
	}
	if !data.IsLogin {
		return 0, "", "", &SiteError{
			Kind:     ErrKindAuthRequired,
			Endpoint: safeEndpoint,
			Message:  "凭证无效或未登录",
		}
	}
	if data.Mid <= 0 {
		return 0, "", "", &SiteError{Kind: ErrKindSchemaChanged, Endpoint: safeEndpoint, Message: "登录校验缺少有效的账号 ID"}
	}
	if dede := strings.TrimSpace(filtered["DedeUserID"]); dede != "" && data.Mid > 0 {
		if dede != fmt.Sprintf("%d", data.Mid) {
			return 0, "", "", &SiteError{
				Kind:     ErrKindBadRequest,
				Endpoint: safeEndpoint,
				Message:  "Cookie 中的 DedeUserID 与当前登录账号不一致",
			}
		}
	}
	if imgKey, subKey := ExtractWBIKey(data.WbiImg.ImgURL), ExtractWBIKey(data.WbiImg.SubURL); imgKey != "" && subKey != "" {
		c.SetWBIKeys(imgKey, subKey)
	}
	return data.Mid, data.Uname, normalizeHTTPSURL(data.Face), nil
}

// ClearCaches resets cached WBI keys and space w_webid entries on login/logout.
func (c *Client) ClearCaches() {
	c.wbiMu.Lock()
	c.imgKey = ""
	c.subKey = ""
	c.mixinKey = ""
	c.wbiMu.Unlock()

	c.webIDMu.Lock()
	c.webIDCache = make(map[int64]cachedWebID)
	c.webIDMu.Unlock()
}

// VerifyCredential calls /x/web-interface/nav to verify that current cookies are valid and updates the cached profile.
func (c *Client) VerifyCredential(ctx context.Context) (credential.Status, error) {
	ctx, unlock := c.beginSequence(ctx, false)
	unlocked := false
	release := func() {
		if !unlocked {
			unlocked = true
			unlock()
		}
	}
	defer release()
	if err := c.RequireAuth("/x/web-interface/nav"); err != nil {
		return c.currentAccountStatus(), err
	}
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/nav"
	safeEndpoint := site.SanitizeEndpoint(fullURL)
	var data navData
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &data); err != nil {
		return c.currentAccountStatus(), err
	}
	if !data.IsLogin {
		release()
		c.expireAuth()
		st := c.currentAccountStatus()
		return st, &SiteError{
			Kind:     ErrKindAuthRequired,
			Endpoint: safeEndpoint,
			Message:  "登录凭证已失效",
		}
	}
	if data.Mid <= 0 {
		return c.currentAccountStatus(), &SiteError{Kind: ErrKindSchemaChanged, Endpoint: safeEndpoint, Message: "登录校验缺少有效的账号 ID"}
	}
	if imgKey, subKey := ExtractWBIKey(data.WbiImg.ImgURL), ExtractWBIKey(data.WbiImg.SubURL); imgKey != "" && subKey != "" {
		c.SetWBIKeys(imgKey, subKey)
	}
	if c.credStore == nil {
		return c.currentAccountStatus(), nil
	}
	return c.credStore.UpdateProfile(data.Mid, data.Uname, data.Face)
}

// ValidateMediaURL checks that rawURL matches Bilibili's allowed HTTPS media CDN whitelist.
func (c *Client) ValidateMediaURL(rawURL string) error {
	return site.BilibiliMediaTransport(c.allowLocalhost).ValidateURL(rawURL)
}

// ValidateLiveMediaURL checks a Bilibili live stream URL. Initial playurl hosts
// (allowScheduled=false) must match official Bilibili CDN domains; scheduled
// multivariant playlists, redirects, and segment targets (allowScheduled=true)
// may target public HTTPS:443 vendor CDN hostnames while still blocking non-HTTPS,
// non-443 ports, IP literals, local names, and userinfo.
func (c *Client) ValidateLiveMediaURL(rawURL string, allowScheduled bool) error {
	spec := site.BilibiliLiveTransport(c.allowLocalhost)
	if allowScheduled {
		return spec.ValidateRedirectURL(rawURL)
	}
	return spec.ValidateURL(rawURL)
}

func bytesContainsVoucher(b []byte) bool {
	return len(b) > 0 && strings.Contains(string(b), `"v_voucher"`)
}

// SetAuthExpiredHandler registers a callback invoked when an authenticated request fails with expired credentials.
func (c *Client) SetAuthExpiredHandler(handler func()) {
	c.automationMu.Lock()
	c.authExpired = handler
	c.automationMu.Unlock()
}

func (c *Client) expireAuth() {
	if c.credStore != nil {
		_, _ = c.credStore.MarkState("expired", "登录凭证已失效")
	}
	c.automationMu.RLock()
	handler := c.authExpired
	c.automationMu.RUnlock()
	if handler != nil {
		handler()
	}
}
