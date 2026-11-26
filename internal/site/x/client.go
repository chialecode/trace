// Package x implements the X (formerly Twitter) web protocol client, runtime frontend
// metadata discovery, transaction header signing, Snowflake ID helpers, and cross-site adapter.
package x

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
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

const (
	// DefaultUserAgent is the fixed desktop Chrome User-Agent used on X web API and CDN requests.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

	// DefaultReferer is the Referer header sent on X web REST and GraphQL requests.
	DefaultReferer = "https://x.com/"

	// DefaultOrigin is the Origin header sent on X web REST and GraphQL requests.
	DefaultOrigin = "https://x.com"

	// DefaultClientLanguage is the fixed x-twitter-client-language header value.
	DefaultClientLanguage = "en"

	// DefaultAccountSettingsURL is the REST v1.1 login confirmation endpoint (immune to GraphQL queryId drift).
	DefaultAccountSettingsURL = "https://api.x.com/1.1/account/settings.json"

	// DefaultGraphQLBaseURL is the X web GraphQL base path.
	DefaultGraphQLBaseURL = "https://x.com/i/api/graphql"

	// DefaultMinInterval is the minimum serial request spacing mandated by NFR-NET-005 (4000ms).
	DefaultMinInterval = 4000 * time.Millisecond

	// DefaultJitterMax is the upper bound of uniform random request jitter mandated by NFR-NET-005 (0..2000ms).
	DefaultJitterMax = 2000 * time.Millisecond

	// DefaultAPITimeout is the per-request HTTP timeout mandated by NFR-NET-005 (15s).
	DefaultAPITimeout = 15 * time.Second

	// DefaultRedirectTimeout is the per-hop timeout for unauthenticated t.co expansion (10s).
	DefaultRedirectTimeout = 10 * time.Second

	// DefaultMaxBodyBytes caps API JSON response bodies at 4 MiB (NFR-NET-005).
	DefaultMaxBodyBytes int64 = 4 * 1024 * 1024

	// DefaultRateLimitJitterMax is the 0..60s jitter added on top of x-rate-limit-reset (x.md §15.1 #13).
	DefaultRateLimitJitterMax = 60 * time.Second

	// OpAccountSettings is the rate-limit bucket key for GET api.x.com/1.1/account/settings.json.
	OpAccountSettings = "AccountSettings"
)

// Endpoints holds configurable X web API URLs for production and httptest fixtures.
type Endpoints struct {
	HomeURL         string
	AccountSettings string
	GraphQLBase     string
}

// DefaultEndpoints returns the official X HTTPS web endpoints.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		HomeURL:         DefaultHomeURL,
		AccountSettings: DefaultAccountSettingsURL,
		GraphQLBase:     DefaultGraphQLBaseURL,
	}
}

func (e Endpoints) withDefaults() Endpoints {
	d := DefaultEndpoints()
	if strings.TrimSpace(e.HomeURL) == "" {
		e.HomeURL = d.HomeURL
	}
	if strings.TrimSpace(e.AccountSettings) == "" {
		e.AccountSettings = d.AccountSettings
	}
	if strings.TrimSpace(e.GraphQLBase) == "" {
		e.GraphQLBase = d.GraphQLBase
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

// RateLimitState tracks the latest observed x-rate-limit-* headers and proactive brake
// window for a single X operation name in memory.
type RateLimitState struct {
	Operation    string    `json:"operation"`
	Limit        int       `json:"limit"`
	Remaining    int       `json:"remaining"`
	ResetAt      time.Time `json:"resetAt"`
	BlockedUntil time.Time `json:"blockedUntil"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ClientOptions configures an X web protocol Client.
type ClientOptions struct {
	CredStore                 *credential.Store
	Endpoints                 Endpoints
	UserAgent                 string
	ClientLanguage            string
	MinInterval               time.Duration // <0 disables pacing (tests); 0 uses DefaultMinInterval (4000ms)
	JitterMax                 time.Duration // <0 sets 0; 0 uses DefaultJitterMax (2000ms) unless MinInterval < 0
	RateLimitJitterMax        time.Duration // <0 sets 0; 0 uses DefaultRateLimitJitterMax (60s)
	APITimeout                time.Duration
	RedirectTimeout           time.Duration
	MediaIdleTimeout          time.Duration
	MaxBodyBytes              int64
	MaxImageBytes             int64
	Transport                 http.RoundTripper
	AssetTransport            http.RoundTripper // optional override for abs.twimg.com frontend assets
	WrapTransport             func(http.RoundTripper) http.RoundTripper
	AllowLocalhost            bool
	ImageCDNFallbackProviders []site.PublicImageCDNProvider
	Now                       func() time.Time
	Sleep                     func(ctx context.Context, d time.Duration) error
	Entropy                   io.Reader
	FrontendDiscovery         *FrontendDiscovery // optional pre-wired FrontendDiscovery
	FrontendRefreshCooldown   time.Duration
	FrontendMetadataTTL       time.Duration
	TxIDOptions               []TxIDOption
	OnCT0Updated              func(ct0 string)
	OnFrontendCharge          RequestChargeFunc
}

// Client is the X web protocol client.
// Outbound API requests are strictly serialized (concurrency = 1) with 4000ms + 0..2000ms jitter
// pacing, 15s timeout, 4 MiB body cap, disabled auto-redirects, passive ct0 sync, at-most-once
// 353 replay, per-operation x-rate-limit-remaining <= 1 brake, runtime frontend metadata
// discovery, x-client-transaction-id signing, and cookieless CDN & t.co handling.
type Client struct {
	cred                 *credential.Store
	endpoints            Endpoints
	profile              browserProfile
	clientLanguage       string
	httpClient           *http.Client
	redirectClient       *http.Client
	mediaClient          *http.Client
	mediaIdleTimeout     time.Duration
	discovery            *FrontendDiscovery
	maxBodyBytes         int64
	maxImageBytes        int64
	allowLocalhost       bool
	cdnFallbackProviders []site.PublicImageCDNProvider
	fallbackWrap         func(http.RoundTripper) http.RoundTripper
	rateLimitJitterMax   time.Duration

	serialGate  chan struct{}
	paceMu      sync.Mutex
	lastReqAt   time.Time
	minInterval time.Duration
	jitterMax   time.Duration
	nowFn       func() time.Time
	sleepFn     func(ctx context.Context, d time.Duration) error
	entropy     io.Reader

	rlMu                    sync.Mutex
	rateLimits              map[string]RateLimitState
	consecutiveHeaderless88 int

	cbMu             sync.RWMutex
	autoBudget       func(ctx context.Context, source string, endpoint string) error
	autoBreaker      func(source string, err error)
	authExpired      func(reason string)
	onCT0Updated     func(ct0 string)
	onFrontendCharge RequestChargeFunc
}

// NewClient constructs a configured X web protocol Client.
func NewClient(opts ClientOptions) *Client {
	profile := deriveBrowserProfile(opts.UserAgent)
	clientLang := strings.TrimSpace(opts.ClientLanguage)
	if clientLang == "" {
		clientLang = DefaultClientLanguage
	}
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

	minInterval := DefaultMinInterval
	jitterMax := DefaultJitterMax
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

	rlJitter := DefaultRateLimitJitterMax
	if opts.RateLimitJitterMax < 0 {
		rlJitter = 0
	} else if opts.RateLimitJitterMax > 0 {
		rlJitter = opts.RateLimitJitterMax
	}

	apiTimeout := opts.APITimeout
	if apiTimeout <= 0 {
		apiTimeout = DefaultAPITimeout
	}
	redirTimeout := opts.RedirectTimeout
	if redirTimeout <= 0 {
		redirTimeout = DefaultRedirectTimeout
	}
	maxBytes := opts.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
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
	redirectTransport := opts.Transport
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
		redirDialer := &net.Dialer{
			Timeout:   redirTimeout,
			KeepAlive: 30 * time.Second,
		}
		redirGuard := site.NewGuardedMediaDialer(SiteID, opts.AllowLocalhost, redirDialer, "已拦截 t.co 短链指向内网或回环地址")
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
		mediaGuard := site.NewGuardedMediaDialer(SiteID, opts.AllowLocalhost, mediaDialer, "已拦截内网或回环 X 媒体节点地址")
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

	endpoints := opts.Endpoints.withDefaults()
	c := &Client{
		cred:                 opts.CredStore,
		endpoints:            endpoints,
		profile:              profile,
		clientLanguage:       clientLang,
		mediaIdleTimeout:     mediaIdleTimeout,
		maxBodyBytes:         maxBytes,
		maxImageBytes:        maxImageBytes,
		allowLocalhost:       opts.AllowLocalhost,
		cdnFallbackProviders: append([]site.PublicImageCDNProvider(nil), opts.ImageCDNFallbackProviders...),
		fallbackWrap:         fallbackWrap,
		rateLimitJitterMax:   rlJitter,
		serialGate:           make(chan struct{}, 1),
		minInterval:          minInterval,
		jitterMax:            jitterMax,
		nowFn:                nowFn,
		sleepFn:              sleepFn,
		entropy:              ent,
		rateLimits:           make(map[string]RateLimitState),
		onCT0Updated:         opts.OnCT0Updated,
		onFrontendCharge:     opts.OnFrontendCharge,
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
		Timeout:       0,
		CheckRedirect: noRedirect,
	}

	if opts.FrontendDiscovery != nil {
		c.discovery = opts.FrontendDiscovery
	} else {
		assetRT := opts.AssetTransport
		if assetRT == nil && opts.Transport != nil {
			assetRT = opts.Transport
		}
		if assetRT == nil && (opts.AllowLocalhost || opts.WrapTransport != nil) {
			assetGuard := site.NewGuardedMediaDialer(
				SiteID,
				opts.AllowLocalhost,
				&net.Dialer{Timeout: apiTimeout, KeepAlive: 30 * time.Second},
				"已拦截不安全的 X 前端资源地址",
			)
			var base http.RoundTripper = &http.Transport{
				Proxy:                 assetGuard.Proxy,
				DialContext:           assetGuard.DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          4,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: apiTimeout,
			}
			if opts.AllowLocalhost {
				base = site.LoopbackTransport(base)
			}
			assetRT = base
		}
		if assetRT != nil && opts.WrapTransport != nil && opts.AssetTransport == nil {
			assetRT = opts.WrapTransport(assetRT)
		}
		c.discovery = NewFrontendDiscovery(FrontendDiscoveryConfig{
			HomeURL:              endpoints.HomeURL,
			FetchHome:            c.fetchHomeHTML,
			AcquireRequest:       c.chargeFrontendRequest,
			UserAgent:            profile.UserAgent,
			Now:                  nowFn,
			RefreshCooldown:      opts.FrontendRefreshCooldown,
			MetadataTTL:          opts.FrontendMetadataTTL,
			AssetTimeout:         apiTimeout,
			AllowLocalhostAssets: opts.AllowLocalhost,
			AssetTransport:       assetRT,
			TxIDOptions:          opts.TxIDOptions,
		})
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

// Discovery returns the client's FrontendDiscovery manager.
func (c *Client) Discovery() *FrontendDiscovery {
	return c.discovery
}

// EnsureFrontendMetadata discovers or returns cached in-memory X frontend metadata.
func (c *Client) EnsureFrontendMetadata(ctx context.Context) (*FrontendMetadata, error) {
	if c.discovery == nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端元数据发现器未初始化",
		}
	}
	return c.discovery.Ensure(ctx)
}

// MediaTransport returns the standard X CDN MediaTransportSpec
// (pbs.twimg.com / video.twimg.com, cookieless, no Referer, no Origin, PublicRedirects=false).
func (c *Client) MediaTransport() site.MediaTransportSpec {
	return site.XMediaTransport(c.profile.UserAgent, c.allowLocalhost)
}

// MediaHTTPClient returns the configured cookieless media HTTP client and idle timeout.
func (c *Client) MediaHTTPClient() (*http.Client, time.Duration) {
	return c.mediaClient, c.mediaIdleTimeout
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

// SetCT0UpdatedHandler registers a callback invoked whenever response Set-Cookie rotates ct0.
func (c *Client) SetCT0UpdatedHandler(fn func(ct0 string)) {
	c.cbMu.Lock()
	c.onCT0Updated = fn
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

// HasAuth reports whether an active X credential is stored.
func (c *Client) HasAuth() bool {
	return c.cred != nil && c.cred.HasActiveCredential()
}

// RequireAuth returns an auth_required *site.Error with 0 network requests when X is logged out.
func (c *Client) RequireAuth(endpoint string) error {
	if c.HasAuth() {
		return nil
	}
	return &site.Error{
		Site:     SiteID,
		Kind:     site.ErrKindAuthRequired,
		Endpoint: site.SanitizeEndpoint(endpoint),
		Message:  "请先登录 X",
	}
}

// RateLimitStateOf returns the last observed rate-limit state for operation, if any.
func (c *Client) RateLimitStateOf(operation string) (RateLimitState, bool) {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	st, ok := c.rateLimits[operation]
	return st, ok
}

func (c *Client) checkOperationRateLimit(ctx context.Context, operation, endpoint string) error {
	if operation == "" {
		return nil
	}
	now := c.nowFn().UTC()
	c.rlMu.Lock()
	st, ok := c.rateLimits[operation]
	c.rlMu.Unlock()
	if !ok {
		return nil
	}
	if st.Remaining <= 1 && now.Before(st.BlockedUntil) {
		wait := st.BlockedUntil.Sub(now)
		rlErr := &RateLimitError{
			Operation:    operation,
			Limit:        st.Limit,
			Remaining:    st.Remaining,
			ResetAt:      st.ResetAt,
			BlockedUntil: st.BlockedUntil,
			RetryAfter:   wait,
		}
		se := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindRateLimited,
			Endpoint: site.SanitizeEndpoint(endpoint),
			Message:  FormatRateLimitMessage(wait),
			Err:      rlErr,
		}
		c.ReportAutomaticError(ctx, se)
		return se
	}
	return nil
}

func (c *Client) recordOperationRateLimit(operation string, hdr http.Header) ParsedRateLimitHeaders {
	parsed := ParseRateLimitHeaders(hdr)
	if operation == "" || !parsed.HasRemaining {
		return parsed
	}
	now := c.nowFn().UTC()
	st := RateLimitState{
		Operation: operation,
		Limit:     parsed.Limit,
		Remaining: parsed.Remaining,
		UpdatedAt: now,
	}
	if parsed.HasReset {
		st.ResetAt = time.Unix(parsed.ResetUnix, 0).UTC()
	}
	if parsed.Remaining <= 1 {
		baseReset := st.ResetAt
		if baseReset.IsZero() {
			baseReset = now.Add(15 * time.Minute)
			st.ResetAt = baseReset
		}
		jitter := c.randomDuration(c.rateLimitJitterMax)
		blockedUntil := baseReset.Add(jitter)
		if blockedUntil.After(now) {
			st.BlockedUntil = blockedUntil
		}
	}

	c.rlMu.Lock()
	c.rateLimits[operation] = st
	c.rlMu.Unlock()
	return parsed
}

func (c *Client) updateHeaderless88Counter(statusCode int, rlHeaders ParsedRateLimitHeaders, body []byte) int {
	errs := ExtractXErrors(body)
	isHeaderless88 := !rlHeaders.HasRemaining && (HasErrorCode(errs, 88) || statusCode == http.StatusTooManyRequests)
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	if isHeaderless88 {
		c.consecutiveHeaderless88++
		return c.consecutiveHeaderless88
	}
	if statusCode == http.StatusOK && len(errs) == 0 {
		c.consecutiveHeaderless88 = 0
	}
	return 0
}

// ---------- Serial Gate, Pacing & Quota Hooks ----------

func (c *Client) acquireSlotAndPace(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, &site.Error{
			Site:    SiteID,
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
			target := c.lastReqAt.Add(c.minInterval + c.randomDuration(c.jitterMax))
			if target.After(now) {
				wait = target.Sub(now)
			}
		}
		c.paceMu.Unlock()

		if wait > 0 {
			if err := c.sleepFn(ctx, wait); err != nil {
				release()
				return nil, &site.Error{
					Site:    SiteID,
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

func (c *Client) randomDuration(maxDur time.Duration) time.Duration {
	if maxDur <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := io.ReadFull(c.entropy, b[:]); err != nil {
		return 0
	}
	v := binary.LittleEndian.Uint64(b[:])
	maxNs := uint64(maxDur.Nanoseconds()) + 1
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

func (c *Client) chargeFrontendRequest(ctx context.Context, kind FrontendRequestKind, targetURL string) (func(), error) {
	sanitized := site.SanitizeEndpoint(targetURL)
	if err := c.checkAutomaticBudget(ctx, sanitized); err != nil {
		return nil, err
	}
	release, err := c.acquireSlotAndPace(ctx)
	if err != nil {
		return nil, err
	}

	c.cbMu.RLock()
	extra := c.onFrontendCharge
	c.cbMu.RUnlock()
	if extra != nil {
		if err := extra(ctx, kind, sanitized); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}

// ---------- Candidate Cookie Context (for LoginWithCookie / VerifyCookie) ----------

type candidateCookieCtxKey struct{}

type candidateCookieCarrier struct {
	mu      sync.Mutex
	cookies map[string]string
}

func newCandidateCookieCarrier(in map[string]string) *candidateCookieCarrier {
	cleaned := credential.CleanXCookieMap(in)
	return &candidateCookieCarrier{cookies: cleaned}
}

func (cc *candidateCookieCarrier) snapshot() map[string]string {
	if cc == nil {
		return nil
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cp := make(map[string]string, len(cc.cookies))
	for k, v := range cc.cookies {
		cp[k] = v
	}
	return cp
}

func (cc *candidateCookieCarrier) updateCT0(newCT0 string) {
	if cc == nil || !credential.IsValidXCT0(newCT0) {
		return
	}
	cc.mu.Lock()
	cc.cookies["ct0"] = newCT0
	cc.mu.Unlock()
}

func withCandidateCookies(ctx context.Context, carrier *candidateCookieCarrier) context.Context {
	if carrier == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, candidateCookieCtxKey{}, carrier)
}

func candidateCookiesFromContext(ctx context.Context) *candidateCookieCarrier {
	if ctx == nil {
		return nil
	}
	cc, _ := ctx.Value(candidateCookieCtxKey{}).(*candidateCookieCarrier)
	return cc
}

// ---------- Home HTML Fetcher for FrontendDiscovery ----------

func (c *Client) fetchHomeHTML(ctx context.Context, homeURL string) ([]byte, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(homeURL))
	if err != nil || parsedURL.Host == "" {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 首页地址",
		}
	}
	if err := c.validateAPIURL(parsedURL); err != nil {
		return nil, err
	}

	var cookieHeader string
	var usingStore bool
	candidate := candidateCookiesFromContext(ctx)
	if candidate != nil {
		cookieHeader = formatXCookieHeader(candidate.snapshot())
	} else {
		if authErr := c.RequireAuth(homeURL); authErr != nil {
			return nil, authErr
		}
		cleaned := credential.CleanXCookieMap(c.cred.Cookies())
		cookieHeader = formatXCookieHeader(cleaned)
		usingStore = true
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "构造 X 首页请求失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Ch-Ua", c.profile.SecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", c.profile.SecCHUAMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", c.profile.SecCHUAPlatform)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Del("Cookie")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		netErr := &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindNetwork,
			Endpoint: sanitizeEndpointLabel(parsedURL),
			Message:  "拉取 X 首页失败，请检查网络连接",
			Err:      site.UnwrapURLError(err),
		}
		c.ReportAutomaticError(ctx, netErr)
		return nil, netErr
	}
	defer resp.Body.Close()

	// Passive Set-Cookie: ct0 sync on authenticated home page fetches too.
	if newCT0, ok := credential.ExtractXCT0FromSetCookies(resp.Cookies()); ok {
		c.syncCT0(newCT0, usingStore, candidate)
	}

	body, err := readBoundedBody(resp.Body, MaxStaticAssetBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindSchemaChanged,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "X 首页 HTML 体积超过 8 MiB 安全上限",
			}
		}
		netErr := &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Endpoint:   sanitizeEndpointLabel(parsedURL),
			Message:    "读取 X 首页响应失败",
			Err:        err,
		}
		c.ReportAutomaticError(ctx, netErr)
		return nil, netErr
	}

	ep := sanitizeEndpointLabel(parsedURL)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		se := classifyXRedirectError(ep, resp.StatusCode, resp.Header.Get("Location"), cookieHeader != "")
		c.onClassifiedError(ctx, se, usingStore)
		return nil, se
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		se := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
			Endpoint:   ep,
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Body:       body,
			HasCookie:  cookieHeader != "",
			Now:        c.nowFn().UTC(),
		})
		if se != nil {
			c.onClassifiedError(ctx, se, usingStore)
			return nil, se
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: resp.StatusCode,
			Endpoint:   ep,
			Message:    fmt.Sprintf("X 首页返回异常状态码 HTTP %d", resp.StatusCode),
		}
	}
	// Detect explicit Cloudflare Turnstile / Arkose challenge pages on HTTP 200 HTML
	// (normal x.com/home contains twitter-site-verification meta tag, whereas challenge pages do not).
	if !strings.Contains(strings.ToLower(string(body)), "twitter-site-verification") {
		if challengeErr := classifyHomeChallengeOrLoginHTML(ep, resp.StatusCode, body); challengeErr != nil {
			c.onClassifiedError(ctx, challengeErr, usingStore)
			return nil, challengeErr
		}
	}
	return body, nil
}

func classifyHomeChallengeOrLoginHTML(endpoint string, statusCode int, body []byte) *site.Error {
	snippet := body
	if len(snippet) > 16384 {
		snippet = snippet[:16384]
	}
	lower := strings.ToLower(string(snippet))
	if containsAny(lower, "cf-turnstile", "challenges.cloudflare.com", "challenge-platform", "just a moment", "arkoselabs", "funcaptcha", "/account/access", "temporarily locked") {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	}
	if containsAny(lower, "/i/flow/login", "sign in to x", "log in to x", "login_redirect") {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 首页跳转或返回登录页，登录状态已失效，请重新粘贴 Cookie 登录",
		}
	}
	return nil
}

// ---------- Core Authenticated & Guest API Execution ----------

type apiRequestSpec struct {
	method          string
	operation       string
	endpoint        string
	query           url.Values
	bearerHeader    string
	referer         string
	origin          string
	guestNoCookie   bool
	candidate       *candidateCookieCarrier
	extraHeaders    map[string]string
	beforeAttempt   func(req *http.Request) error
	skipStoreExpire bool
}

type apiRawResponse struct {
	statusCode              int
	header                  http.Header
	body                    []byte
	rlHeaders               ParsedRateLimitHeaders
	consecutiveHeaderless88 int
	usingStore              bool
	hasCookie               bool
}

// RawProbeResult captures raw HTTP response telemetry for bounded verification tools (ACT-024).
type RawProbeResult struct {
	Endpoint   string
	StatusCode int
	Header     http.Header
	Body       []byte
}

// ProbeRaw executes one bounded, paced X REST/API request without envelope rejection so ACT-024
// verification tools can inspect exact status codes, headers, and payloads while honoring
// pacing, host allowlisting, size limits, and passive ct0 sync.
func (c *Client) ProbeRaw(ctx context.Context, method, operation, endpoint string, query url.Values, guestNoCookie bool) (*RawProbeResult, error) {
	var bearer string
	if meta := c.discovery.Current(); meta != nil {
		bearer = meta.BearerHeader()
	}
	raw, err := c.executeRawAPIRequest(ctx, apiRequestSpec{
		method:          method,
		operation:       operation,
		endpoint:        endpoint,
		query:           query,
		bearerHeader:    bearer,
		guestNoCookie:   guestNoCookie,
		skipStoreExpire: true,
	})
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

func (c *Client) doAPIRequest(ctx context.Context, spec apiRequestSpec) (*apiRawResponse, error) {
	rawResp, err := c.executeRawAPIRequest(ctx, spec)
	if err != nil {
		return nil, err
	}
	if se := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
		Endpoint:                spec.endpoint,
		Operation:               spec.operation,
		StatusCode:              rawResp.statusCode,
		Header:                  rawResp.header,
		Body:                    rawResp.body,
		HasCookie:               rawResp.hasCookie,
		Now:                     c.nowFn().UTC(),
		ConsecutiveHeaderless88: rawResp.consecutiveHeaderless88,
	}); se != nil {
		c.onClassifiedError(ctx, se, rawResp.usingStore && !spec.skipStoreExpire)
		return rawResp, se
	}
	return rawResp, nil
}

func (c *Client) resolveCookieContext(spec apiRequestSpec) (cookieHeader, ct0 string, isAuthSession, usingStore bool, err error) {
	switch {
	case spec.guestNoCookie:
		return "", "", false, false, nil

	case spec.candidate != nil:
		cleaned := credential.CleanXCookieMap(spec.candidate.snapshot())
		ct0Val := cleaned["ct0"]
		hasSession := cleaned["auth_token"] != "" && credential.IsValidXCT0(ct0Val)
		return formatXCookieHeader(cleaned), ct0Val, hasSession, false, nil

	default:
		if authErr := c.RequireAuth(spec.endpoint); authErr != nil {
			return "", "", false, false, authErr
		}
		cleaned := credential.CleanXCookieMap(c.cred.Cookies())
		ct0Val := cleaned["ct0"]
		cookieHdr := formatXCookieHeader(cleaned)
		if cookieHdr == "" || cleaned["auth_token"] == "" || !credential.IsValidXCT0(ct0Val) {
			return "", "", false, false, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindAuthRequired,
				Endpoint: site.SanitizeEndpoint(spec.endpoint),
				Message:  "请先登录 X",
			}
		}
		return cookieHdr, ct0Val, true, true, nil
	}
}

// formatXCookieHeader formats a cleaned X cookie map in canonical whitelist order so every
// whitelisted key (especially ct0) appears at most once and non-whitelisted keys never leak.
func formatXCookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	cleaned := credential.CleanXCookieMap(cookies)
	if len(cleaned) == 0 {
		return ""
	}
	parts := make([]string, 0, len(credential.AllowedXCookieKeys))
	for _, k := range credential.AllowedXCookieKeys {
		if v := cleaned[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

// executeRawAPIRequest performs one paced, budgeted, non-redirecting X API request with:
//   - per-operation x-rate-limit-remaining <= 1 brake (0 upstream requests when braked)
//   - passive Set-Cookie: ct0 sync on every authenticated response
//   - AT MOST ONE automatic replay on HTTP 403 + code 353 when response supplies a valid new ct0
//   - ZERO other automatic retries (caller handles GraphQL drift refresh separately)
func (c *Client) executeRawAPIRequest(ctx context.Context, spec apiRequestSpec) (*apiRawResponse, error) {
	if err := c.checkOperationRateLimit(ctx, spec.operation, spec.endpoint); err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(strings.TrimSpace(spec.endpoint))
	if err != nil || parsedURL.Host == "" {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindBadRequest,
			Endpoint: site.SanitizeEndpoint(spec.endpoint),
			Message:  "无效的 X 接口地址",
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

	replayed353 := false
	for {
		cookieHeader, ct0, isAuthSession, usingStore, err := c.resolveCookieContext(spec)
		if err != nil {
			c.ReportAutomaticError(ctx, err)
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindNetwork,
				Endpoint: sanitizeEndpointLabel(parsedURL),
				Message:  "请求已取消",
				Err:      ctx.Err(),
			}
		}
		if err := c.checkAutomaticBudget(ctx, sanitizeEndpointLabel(parsedURL)); err != nil {
			return nil, err
		}

		release, err := c.acquireSlotAndPace(ctx)
		if err != nil {
			return nil, err
		}

		method := spec.method
		if method == "" {
			method = http.MethodGet
		}
		req, reqErr := http.NewRequestWithContext(ctx, method, parsedURL.String(), nil)
		if reqErr != nil {
			release()
			return nil, &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindBadRequest,
				Endpoint: sanitizeEndpointLabel(parsedURL),
				Message:  "构造 X 请求失败",
				Err:      site.UnwrapURLError(reqErr),
			}
		}

		c.applyAPIHeaders(req, spec, cookieHeader, ct0, isAuthSession)
		if spec.beforeAttempt != nil {
			if hookErr := spec.beforeAttempt(req); hookErr != nil {
				release()
				return nil, hookErr
			}
		}

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			release()
			netErr := &site.Error{
				Site:     SiteID,
				Kind:     site.ErrKindNetwork,
				Endpoint: sanitizeEndpointLabel(parsedURL),
				Message:  "请求 X 接口失败，请检查网络连接",
				Err:      site.UnwrapURLError(doErr),
			}
			c.ReportAutomaticError(ctx, netErr)
			return nil, netErr
		}

		body, readErr := readBoundedBody(resp.Body, c.maxBodyBytes)
		resp.Body.Close()
		release()

		if readErr != nil {
			if errors.Is(readErr, errBodyTooLarge) {
				return nil, &site.Error{
					Site:       SiteID,
					Kind:       site.ErrKindBlocked,
					HTTPStatus: resp.StatusCode,
					Endpoint:   sanitizeEndpointLabel(parsedURL),
					Message:    "X 响应体积超出 4 MiB 安全上限",
				}
			}
			netErr := &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindNetwork,
				HTTPStatus: resp.StatusCode,
				Endpoint:   sanitizeEndpointLabel(parsedURL),
				Message:    "读取 X 响应失败",
				Err:        readErr,
			}
			c.ReportAutomaticError(ctx, netErr)
			return nil, netErr
		}

		rlHeaders := c.recordOperationRateLimit(spec.operation, resp.Header)
		consec88 := c.updateHeaderless88Counter(resp.StatusCode, rlHeaders, body)

		// Passive Set-Cookie: ct0 sync on every authenticated response (x.md §15.1 #7).
		// Rejects invalid characters and never fabricates ct0.
		var syncedNewCT0 string
		if !spec.guestNoCookie {
			if newCT0, ok := credential.ExtractXCT0FromSetCookies(resp.Cookies()); ok {
				c.syncCT0(newCT0, usingStore, spec.candidate)
				syncedNewCT0 = newCT0
			}
		}

		// HTTP 403 + error code 353 single replay ONLY when response supplied a valid new Set-Cookie: ct0.
		if !replayed353 && !spec.guestNoCookie && resp.StatusCode == http.StatusForbidden && syncedNewCT0 != "" && syncedNewCT0 != ct0 {
			if HasErrorCode(ExtractXErrors(body), 353) {
				replayed353 = true
				continue
			}
		}

		return &apiRawResponse{
			statusCode:              resp.StatusCode,
			header:                  resp.Header.Clone(),
			body:                    body,
			rlHeaders:               rlHeaders,
			consecutiveHeaderless88: consec88,
			usingStore:              usingStore,
			hasCookie:               cookieHeader != "",
		}, nil
	}
}

func (c *Client) applyAPIHeaders(req *http.Request, spec apiRequestSpec, cookieHeader, ct0 string, isAuthSession bool) {
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Ch-Ua", c.profile.SecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", c.profile.SecCHUAMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", c.profile.SecCHUAPlatform)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("x-twitter-active-user", "yes")
	req.Header.Set("x-twitter-client-language", c.clientLanguage)

	ref := spec.referer
	if ref == "" {
		ref = DefaultReferer
	}
	req.Header.Set("Referer", ref)
	orig := spec.origin
	if orig == "" {
		orig = DefaultOrigin
	}
	req.Header.Set("Origin", orig)

	if spec.bearerHeader != "" {
		req.Header.Set("Authorization", spec.bearerHeader)
	} else {
		req.Header.Del("Authorization")
	}

	if spec.guestNoCookie {
		req.Header.Del("Cookie")
		req.Header.Del("x-csrf-token")
		req.Header.Del("x-twitter-auth-type")
	} else {
		if cookieHeader != "" {
			req.Header.Set("Cookie", cookieHeader)
		} else {
			req.Header.Del("Cookie")
		}
		if credential.IsValidXCT0(ct0) {
			req.Header.Set("x-csrf-token", ct0)
		} else {
			req.Header.Del("x-csrf-token")
		}
		// x.md §2.2: x-twitter-auth-type: OAuth2Session ONLY on authenticated requests!
		if isAuthSession {
			req.Header.Set("x-twitter-auth-type", "OAuth2Session")
		} else {
			req.Header.Del("x-twitter-auth-type")
		}
	}

	// Never send x-xp-forwarded-for (excluded by x.md §15.1 #5).
	req.Header.Del("x-xp-forwarded-for")

	for k, v := range spec.extraHeaders {
		if strings.EqualFold(k, "x-xp-forwarded-for") {
			continue
		}
		if v != "" {
			req.Header.Set(k, v)
		}
	}
}

func (c *Client) syncCT0(newCT0 string, usingStore bool, candidate *candidateCookieCarrier) {
	if !credential.IsValidXCT0(newCT0) {
		return
	}
	if candidate != nil {
		candidate.updateCT0(newCT0)
	} else if usingStore && c.cred != nil {
		_ = c.cred.SyncXCT0(newCT0)
	}
	c.cbMu.RLock()
	cb := c.onCT0Updated
	c.cbMu.RUnlock()
	if cb != nil {
		cb(newCT0)
	}
}

func (c *Client) onClassifiedError(ctx context.Context, se *site.Error, markExpired bool) {
	if se == nil {
		return
	}
	// CRITICAL: risk_control and rate_limited MUST NEVER clear or expire credentials!
	if se.Kind == site.ErrKindAuthRequired && markExpired {
		c.markAuthExpired(se.Message)
	}
	c.ReportAutomaticError(ctx, se)
}

func (c *Client) markAuthExpired(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "X 登录状态已失效，请重新粘贴 Cookie 登录"
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

var allowedXAPIHosts = []string{
	"x.com",
	"www.x.com",
	"api.x.com",
}

func (c *Client) validateAPIURL(u *url.URL) error {
	if u == nil || u.Host == "" || u.User != nil {
		return &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不安全的 X 接口地址",
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
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非 HTTPS X 接口请求",
		}
	}
	if port := u.Port(); port != "" && port != "443" {
		return &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截非常规端口 X 接口请求",
		}
	}
	for _, allowed := range allowedXAPIHosts {
		if host == allowed {
			return nil
		}
	}
	return &site.Error{
		Site:    SiteID,
		Kind:    site.ErrKindBlocked,
		Message: "接口域名不在 X 允许列表中: " + host,
	}
}

var errBodyTooLarge = errors.New("x: response body exceeds limit")

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

// ValidateMediaURL checks that rawURL belongs to X's allowed HTTPS media CDN allowlist.
func (c *Client) ValidateMediaURL(rawURL string) error {
	return c.MediaTransport().ValidateURL(rawURL)
}

// FetchImage fetches an X CDN image (pbs.twimg.com) without sending Cookie, Referer, or Origin.
func (c *Client) FetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	return c.FetchImageLimited(ctx, rawURL, c.maxImageBytes)
}

// FetchImageLimited fetches up to maxBytes of an X CDN image with Cookie/Authorization/CSRF/Referer
// stripped. When origin returns 403/404/410 or an expired placeholder image, eligible unsigned
// pbs.twimg.com URLs fall back to public CDN caches (DEC-075). Requires active X auth (INV-07).
func (c *Client) FetchImageLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	if err := c.RequireAuth("image"); err != nil {
		return nil, "", err
	}
	normalized := normalizeXMediaURL(rawURL, c.allowLocalhost)
	transportSpec := c.MediaTransport()
	if err := transportSpec.ValidateURL(normalized); err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 {
		maxBytes = c.maxImageBytes
	}

	data, ct, sawPlaceholder, err := c.fetchSingleXImage(ctx, transportSpec, normalized, maxBytes)
	if err == nil {
		return data, ct, nil
	}
	if site.ShouldAttemptImageCDNFallback(err, sawPlaceholder) {
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
	}
	return nil, "", err
}

func (c *Client) fetchSingleXImage(ctx context.Context, transportSpec site.MediaTransportSpec, normalized string, maxBytes int64) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, "", false, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 图片地址",
			Err:     site.UnwrapURLError(err),
		}
	}
	transportSpec.ApplyHeaders(req)

	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, "", false, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindNetwork,
			Message: "拉取 X 图片失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	body := site.WrapIdleReadCloser(resp.Body, c.mediaIdleTimeout, func() error {
		return &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindNetwork,
			Message: "读取 X 图片超时",
		}
	})
	defer body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", false, &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: resp.StatusCode,
			Message:    fmt.Sprintf("X 图片返回 HTTP %d", resp.StatusCode),
		}
	}

	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain") {
		return nil, "", false, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "X 图片响应类型异常: " + ct,
		}
	}

	if resp.ContentLength > maxBytes {
		return nil, "", false, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "X 图片体积超出安全上限",
		}
	}

	data, err := readBoundedBody(body, maxBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return nil, "", false, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBlocked,
				Message: "X 图片体积超出安全上限",
			}
		}
		return nil, "", false, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindNetwork,
			Message: "读取 X 图片失败",
			Err:     err,
		}
	}
	if looksLikeHTMLOrJSONPayload(data) {
		return nil, "", false, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "X 图片响应内容疑似伪装页面",
		}
	}
	if site.LooksLikeExpiredPlaceholderImageBytes(data, normalized, 0, 0, false) {
		return nil, "", true, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindUnavailable,
			Message: "X 图片已在源站失效",
		}
	}
	return data, ct, false, nil
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
		if trimmed[0] == '{' && (strings.Contains(lower, `"errors"`) || strings.Contains(lower, `"data"`)) {
			return true
		}
	}
	return false
}

func normalizeXMediaURL(raw string, allowLocalhost bool) string {
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
