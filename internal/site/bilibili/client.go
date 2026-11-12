package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

const (
	DefaultUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	DefaultReferer       = "https://www.bilibili.com/"
	DefaultAPITimeout    = 15 * time.Second
	DefaultIdleTimeout   = 30 * time.Second
	DefaultPacingMinWait = 500 * time.Millisecond
	MaxAPIResponseBytes  = 4 * 1024 * 1024
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
	APITimeout       time.Duration
	MediaIdleTimeout time.Duration
	PacingInterval   time.Duration
	AllowLocalhost   bool
	NowFunc          func() time.Time
	// WrapTransport, when set, wraps both the API and media transports. Only
	// authorized site checks use it to gate each request before it is sent.
	WrapTransport func(http.RoundTripper) http.RoundTripper
}

// Client implements the Bilibili site adapter with dual HTTP clients, global pacing, WBI signing, and QR/refresh flows.
type Client struct {
	credStore        *credential.Store
	endpoints        Endpoints
	apiClient        *http.Client
	mediaClient      *http.Client
	mediaIdleTimeout time.Duration
	pacingInterval   time.Duration
	authExpired      func()
	wbiFetchedAt     time.Time
	allowLocalhost   bool
	nowFunc          func() time.Time

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
	pacing := opts.PacingInterval
	if pacing == 0 {
		pacing = DefaultPacingMinWait
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
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: apiTimeout,
	}

	mediaTransport := &http.Transport{
		Proxy: site.Proxy,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}

	c := &Client{
		credStore:        opts.CredStore,
		endpoints:        ep,
		mediaIdleTimeout: idleTimeout,
		pacingInterval:   pacing,
		allowLocalhost:   opts.AllowLocalhost,
		nowFunc:          nowFn,
		qrHistory:        make(map[string]string),
		webIDCache:       make(map[int64]cachedWebID),
		apiGate:          make(chan struct{}, 1),
	}

	c.apiClient = &http.Client{
		Timeout:   apiTimeout,
		Transport: apiTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	c.mediaClient = &http.Client{
		Timeout:   0, // No total timeout on media streams; enforced via ResponseHeaderTimeout + idleTimeoutReader
		Transport: mediaTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("bilibili: media redirect limit exceeded")
			}
			if !c.isAllowedMediaURL(req.URL) {
				return fmt.Errorf("bilibili: blocked media redirect host %s", req.URL.Host)
			}
			return nil
		},
	}

	if opts.AllowLocalhost {
		// Fixture clients must never escape to the real site, including redirects.
		c.apiClient.Transport = loopbackTransport{apiTransport}
		c.mediaClient.Transport = loopbackTransport{mediaTransport}
	}
	if opts.WrapTransport != nil {
		c.apiClient.Transport = opts.WrapTransport(c.apiClient.Transport)
		c.mediaClient.Transport = opts.WrapTransport(c.mediaClient.Transport)
	}
	return c
}

type loopbackTransport struct{ base http.RoundTripper }

func (t loopbackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, errors.New("fixture transport: non-loopback request rejected")
	}
	return t.base.RoundTrip(r)
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

// FetchImage reads a public original image without sending login cookies.
func (c *Client) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	return c.FetchImageLimited(ctx, rawURL, 50*1024*1024)
}

// FetchImageLimited applies the caller's smaller byte limit before reading a
// public image body. History thumbnails use 10 MiB; original viewing uses 50 MiB.
func (c *Client) FetchImageLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > 50*1024*1024 {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片大小上限无效"}
	}
	if err := c.RequireAuth("image"); err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil || !c.isAllowedMediaURL(u) || u.User != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片地址不受支持"}
	}
	u.Path = strings.SplitN(u.Path, "@", 2)[0]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "图片地址无效"}
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, &SiteError{Kind: ErrKindNetwork, Message: "原图加载失败，请重试"}
	}
	body := c.WrapMediaReader(resp.Body)
	defer body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTPStatus(resp.StatusCode, "image")
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
	return raw, nil
}

// WrapMediaReader wraps an io.ReadCloser with a per-read idle timeout.
func (c *Client) WrapMediaReader(rc io.ReadCloser) io.ReadCloser {
	return &idleTimeoutReadCloser{
		rc:      rc,
		timeout: c.mediaIdleTimeout,
	}
}

func (c *Client) waitPacing(ctx context.Context) error {
	if c.pacingInterval <= 0 {
		return nil
	}
	c.paceMu.Lock()
	now := time.Now()
	next := c.lastReq.Add(c.pacingInterval)
	var wait time.Duration
	if next.After(now) {
		wait = next.Sub(now)
		c.lastReq = next
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

func (c *Client) doAPIRequest(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool) (*http.Response, []byte, error) {
	ctx, unlock := c.beginSequence(ctx, false)
	defer unlock()
	if includeCookies {
		if err := c.RequireAuth("api"); err != nil {
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
			Endpoint: fullURL,
			Message:  "请求已取消",
			Err:      err,
		}
	}
	defer endAPI()

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, nil, &SiteError{
			Kind:     ErrKindBadRequest,
			Endpoint: fullURL,
			Message:  "构造请求失败",
			Err:      err,
		}
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if includeCookies && c.credStore != nil {
		if ck := c.credStore.CookieHeader(); ck != "" {
			req.Header.Set("Cookie", ck)
		}
	}

	resp, err := c.apiClient.Do(req)
	if err != nil {
		return nil, nil, &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: fullURL,
			Message:  "网络请求失败",
			Err:      err,
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
			Endpoint: fullURL,
			Message:  "读取响应失败",
			Err:      err,
		}
	}

	if int64(len(raw)) > limit {
		return resp, nil, &SiteError{Kind: ErrKindSchemaChanged, Message: "接口响应超过大小上限"}
	}
	if resp.StatusCode != http.StatusOK {
		return resp, raw, classifyHTTPStatus(resp.StatusCode, fullURL)
	}
	return resp, raw, nil
}

func (c *Client) doBiliJSON(ctx context.Context, method, fullURL string, body io.Reader, contentType string, includeCookies bool, out any) (response *http.Response, result error) {
	defer func() { c.ReportAutomaticError(ctx, result) }()
	resp, raw, err := c.doAPIRequest(ctx, method, fullURL, body, contentType, includeCookies)
	if err != nil {
		return resp, err
	}

	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return resp, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
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
		if env.Code == -101 && c.credStore != nil {
			c.expireAuth()
		}
		return resp, classifyBiliCode(env.Code, msg, hasVoucher, fullURL)
	}

	if out != nil {
		if len(payload) == 0 || string(payload) == "null" {
			return resp, &SiteError{Kind: ErrKindSchemaChanged, Message: "接口响应缺少 data"}
		}
		if err := json.Unmarshal(payload, out); err != nil {
			return resp, &SiteError{
				Kind:     ErrKindSchemaChanged,
				Endpoint: fullURL,
				Message:  "解析接口数据字段失败",
				Err:      err,
			}
		}
	}
	return resp, nil
}

// doWBIGet executes a WBI-signed GET request. On a single -352 response (without v_voucher),
// it refreshes the WBI keys once from /x/web-interface/nav and retries once.
func (c *Client) doWBIGet(ctx context.Context, path string, params url.Values, out any) error {
	mixin, err := c.ensureMixinKey(ctx, false)
	if err != nil {
		return err
	}
	signed := SignWBIParams(params, mixin, c.nowFunc().Unix())
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + path + "?" + signed.Encode()

	_, err = c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, out)
	if err == nil {
		return nil
	}

	var se *SiteError
	if errors.As(err, &se) && se.Code == -352 && !strings.Contains(se.Message, "v_voucher") {
		// Refresh WBI keys once and retry once (C09).
		mixin2, rErr := c.ensureMixinKey(ctx, true)
		if rErr != nil {
			return err
		}
		signed2 := SignWBIParams(params, mixin2, c.nowFunc().Unix())
		retryURL := strings.TrimRight(c.endpoints.APIBase, "/") + path + "?" + signed2.Encode()
		_, err2 := c.doBiliJSON(ctx, http.MethodGet, retryURL, nil, "", true, out)
		return err2
	}
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

func (c *Client) fetchNav(ctx context.Context) (navData, error) {
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/nav"
	_, raw, err := c.doAPIRequest(ctx, http.MethodGet, fullURL, nil, "", true)
	if err != nil {
		return navData{}, err
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return navData{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
			Message:  "解析 nav 响应失败",
			Err:      err,
		}
	}
	// Note: /x/web-interface/nav returns wbi_img inside data even when code == -101,
	// though TRACE only calls nav when logged in or validating credentials.
	var data navData
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &data)
	}
	if env.Code != 0 && data.WbiImg.ImgURL == "" {
		return navData{}, classifyBiliCode(env.Code, env.Message, bytesContainsVoucher(env.Data), fullURL)
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

	endAPI, err := c.beginAPI(ctx)
	if err != nil {
		var siteErr *SiteError
		if errors.As(err, &siteErr) {
			return 0, "", "", err
		}
		return 0, "", "", &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: "/x/web-interface/nav",
			Message:  "请求已取消",
			Err:      err,
		}
	}
	defer endAPI()
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/nav"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return 0, "", "", &SiteError{
			Kind:     ErrKindBadRequest,
			Endpoint: fullURL,
			Message:  "构造请求失败",
			Err:      err,
		}
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Cookie", cookieHeader)

	resp, err := c.apiClient.Do(req)
	if err != nil {
		return 0, "", "", &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: fullURL,
			Message:  "网络请求失败",
			Err:      err,
		}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxAPIResponseBytes+1))
	if err != nil {
		return 0, "", "", &SiteError{
			Kind:     ErrKindNetwork,
			Endpoint: fullURL,
			Message:  "读取响应失败",
			Err:      err,
		}
	}
	if len(raw) > MaxAPIResponseBytes {
		return 0, "", "", &SiteError{Kind: ErrKindSchemaChanged, Message: "登录校验响应超过大小上限"}
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", "", classifyHTTPStatus(resp.StatusCode, fullURL)
	}

	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, "", "", &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
			Message:  "接口返回非预期 JSON 结构",
			Err:      err,
		}
	}
	if env.Code != 0 || bytesContainsVoucher(env.Data) {
		return 0, "", "", classifyBiliCode(env.Code, env.Message, bytesContainsVoucher(env.Data), fullURL)
	}

	var data navData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return 0, "", "", &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
			Message:  "解析 nav 数据失败",
			Err:      err,
		}
	}
	if !data.IsLogin {
		return 0, "", "", &SiteError{
			Kind:     ErrKindAuthRequired,
			Endpoint: fullURL,
			Message:  "凭证无效或未登录",
		}
	}
	if data.Mid <= 0 {
		return 0, "", "", &SiteError{Kind: ErrKindSchemaChanged, Message: "登录校验缺少有效的账号 ID"}
	}
	if dede := strings.TrimSpace(filtered["DedeUserID"]); dede != "" && data.Mid > 0 {
		if dede != fmt.Sprintf("%d", data.Mid) {
			return 0, "", "", &SiteError{
				Kind:     ErrKindBadRequest,
				Endpoint: fullURL,
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
	defer unlock()
	if err := c.RequireAuth("/x/web-interface/nav"); err != nil {
		return c.credStore.Status(), err
	}
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/nav"
	var data navData
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &data); err != nil {
		return c.credStore.Status(), err
	}
	if !data.IsLogin {
		c.expireAuth()
		st := c.credStore.Status()
		return st, &SiteError{
			Kind:     ErrKindAuthRequired,
			Endpoint: fullURL,
			Message:  "登录凭证已失效",
		}
	}
	if data.Mid <= 0 {
		return c.currentAccountStatus(), &SiteError{Kind: ErrKindSchemaChanged, Message: "登录校验缺少有效的账号 ID"}
	}
	if imgKey, subKey := ExtractWBIKey(data.WbiImg.ImgURL), ExtractWBIKey(data.WbiImg.SubURL); imgKey != "" && subKey != "" {
		c.SetWBIKeys(imgKey, subKey)
	}
	return c.credStore.UpdateProfile(data.Mid, data.Uname, data.Face)
}

func (c *Client) isAllowedMediaURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if c.allowLocalhost && (host == "127.0.0.1" || host == "localhost") {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	allowedSuffixes := []string{
		".bilivideo.com",
		".bilivideo.cn",
		".hdslb.com",
		".bilibili.com",
		".akamaized.net",
	}
	for _, suf := range allowedSuffixes {
		if strings.HasSuffix(host, suf) || host == strings.TrimPrefix(suf, ".") {
			return true
		}
	}
	return false
}

func (c *Client) ValidateMediaURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || !c.isAllowedMediaURL(u) {
		return &SiteError{Kind: ErrKindBadRequest, Message: "媒体地址不受支持"}
	}
	return nil
}

func bytesContainsVoucher(b []byte) bool {
	return len(b) > 0 && strings.Contains(string(b), `"v_voucher"`)
}

type idleTimeoutReadCloser struct {
	rc      io.ReadCloser
	timeout time.Duration
}

func (r *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	if r.timeout <= 0 {
		return r.rc.Read(p)
	}
	type readResult struct {
		n   int
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		n, err := r.rc.Read(p)
		ch <- readResult{n: n, err: err}
	}()

	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-timer.C:
		_ = r.rc.Close()
		// The reader owns p until it exits; never let it mutate a reused buffer.
		<-ch
		return 0, &SiteError{
			Kind:    ErrKindNetwork,
			Message: "媒体流读取超时（超过 30 秒无进展）",
		}
	}
}

func (r *idleTimeoutReadCloser) Close() error {
	return r.rc.Close()
}

// SetAuthExpiredHandler is configured before starting background work.
func (c *Client) SetAuthExpiredHandler(handler func()) { c.authExpired = handler }
func (c *Client) expireAuth() {
	if c.credStore != nil {
		_, _ = c.credStore.MarkState("expired", "登录凭证已失效")
	}
	if c.authExpired != nil {
		c.authExpired()
	}
}
