package site

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultFallbackHeaderTimeout = 15 * time.Second
	defaultFallbackIdleTimeout   = 20 * time.Second
	photonMaxImageBytes          = 50 * 1024 * 1024
)

// PublicImageCDNProvider describes one public image cache or web archive provider
// consulted only after direct origin image fetching fails with 403/404/410 or
// returns an expired placeholder image (DEC-075).
type PublicImageCDNProvider struct {
	Name           string
	AllowedDomains []string
	MaxRedirects   int
	BuildURL       func(origin *url.URL, cleanOriginURL string) (string, bool)
}

// PublicImageCDNFallbackConfig configures public CDN cache fallback behavior.
// In unit tests (AllowLocalhost == true), zero external requests are ever attempted
// unless Providers is explicitly populated with local 127.0.0.1 httptest endpoints.
type PublicImageCDNFallbackConfig struct {
	Providers       []PublicImageCDNProvider
	AllowLocalhost  bool
	WrapTransport   func(http.RoundTripper) http.RoundTripper
	HeaderTimeout   time.Duration
	IdleReadTimeout time.Duration
	UserAgent       string
}

// DefaultPublicImageCDNProviders returns the ordered public CDN cache and archive
// providers for unsigned public static image recovery (DEC-075):
//  1. Jetpack Photon (i0.wp.com) — queryless HTTPS static images up to 50 MiB;
//  2. wsrv.nl (weserv/images on Cloudflare CDN) — supports encoded query strings and animated frames (n=-1);
//  3. Internet Archive Wayback Machine (/web/0id_/) — earliest unmodified binary capture.
func DefaultPublicImageCDNProviders() []PublicImageCDNProvider {
	return []PublicImageCDNProvider{
		{
			Name:           "photon",
			AllowedDomains: []string{"wp.com"},
			MaxRedirects:   2,
			BuildURL:       BuildPhotonCacheURL,
		},
		{
			Name:           "wsrv",
			AllowedDomains: []string{"wsrv.nl", "weserv.nl"},
			MaxRedirects:   2,
			BuildURL:       BuildWsrvCacheURL,
		},
		{
			Name:           "wayback",
			AllowedDomains: []string{"archive.org"},
			MaxRedirects:   3,
			BuildURL:       BuildWaybackRawEarliestURL,
		},
	}
}

// BuildPhotonCacheURL builds a Jetpack Photon HTTPS cache URL (`https://i0.wp.com/<host><path>?ssl=1`)
// for queryless public static image URLs. Origin URLs carrying query parameters (such as
// pbs.twimg.com `?format=jpg&name=orig`) are skipped on Photon because Photon interprets
// top-level query parameters as transformation directives rather than origin parameters.
func BuildPhotonCacheURL(origin *url.URL, _ string) (string, bool) {
	if origin == nil || origin.Host == "" || origin.RawQuery != "" {
		return "", false
	}
	escapedPath := origin.EscapedPath()
	if escapedPath == "" || !strings.HasPrefix(escapedPath, "/") {
		return "", false
	}
	return "https://i0.wp.com/" + strings.ToLower(origin.Hostname()) + escapedPath + "?ssl=1", true
}

// BuildWsrvCacheURL builds a wsrv.nl cache URL (`https://wsrv.nl/?url=<encoded>&n=-1`),
// passing `n=-1` so multi-frame animated GIFs and WebPs retain all frames.
func BuildWsrvCacheURL(_ *url.URL, cleanOriginURL string) (string, bool) {
	if strings.TrimSpace(cleanOriginURL) == "" {
		return "", false
	}
	return "https://wsrv.nl/?url=" + url.QueryEscape(cleanOriginURL) + "&n=-1", true
}

// BuildWaybackRawEarliestURL builds an Internet Archive Wayback Machine earliest-capture
// identity URL (`https://web.archive.org/web/0id_/<cleanOriginURL>`). Using `0` selects
// the earliest snapshot (captured before origin deletion/placeholder replacement) and
// `id_` disables all HTML toolbar/link rewriting so unmodified raw image bytes are served.
func BuildWaybackRawEarliestURL(_ *url.URL, cleanOriginURL string) (string, bool) {
	if strings.TrimSpace(cleanOriginURL) == "" {
		return "", false
	}
	return "https://web.archive.org/web/0id_/" + cleanOriginURL, true
}

// ShouldAttemptImageCDNFallback reports whether an origin image failure is eligible
// to trigger public CDN cache fallback. Fallback ONLY runs on HTTP 403/404/410
// (`forbidden` / `unavailable`) or when the origin returned a known expired placeholder
// image — NEVER on 429 rate-limiting, 412/414/418/432 risk-control, auth failures,
// oversized responses, or canceled contexts.
func ShouldAttemptImageCDNFallback(err error, sawPlaceholder bool) bool {
	if sawPlaceholder {
		return true
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if IsRoundStop(err) || IsCircuitBreak(err) {
		return false
	}
	status := HTTPStatusOf(err)
	if status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone {
		return true
	}
	if status != 0 {
		return false
	}
	return IsKind(err, ErrKindForbidden) || IsKind(err, ErrKindUnavailable)
}

var sensitiveQueryKeyDenylist = map[string]bool{
	"ssig":                 true,
	"expires":              true,
	"x-expires":            true,
	"kid":                  true,
	"a_bogus":              true,
	"x-bogus":              true,
	"mstoken":              true,
	"x-signature":          true,
	"signature":            true,
	"sig":                  true,
	"sign":                 true,
	"token":                true,
	"access_token":         true,
	"auth":                 true,
	"auth_key":             true,
	"authorization":        true,
	"key":                  true,
	"secret":               true,
	"ticket":               true,
	"cookie":               true,
	"sub":                  true,
	"subp":                 true,
	"xsrf-token":           true,
	"wbpsess":              true,
	"auth_token":           true,
	"ct0":                  true,
	"kdt":                  true,
	"sessionid":            true,
	"sessionid_ss":         true,
	"sid_guard":            true,
	"hdnts":                true,
	"txsecret":             true,
	"wssecret":             true,
	"policy":               true,
	"credential":           true,
	"x-amz-signature":      true,
	"x-amz-credential":     true,
	"x-amz-security-token": true,
	"x-oss-signature":      true,
	"q-signature":          true,
	"e":                    true,
	"st":                   true,
}

// IsEligibleForPublicImageCDNFallback strictly validates that rawURL is an unsigned,
// credential-free public static image URL safe to query on a public CDN cache or
// archive service (DEC-075). Any URL containing private hosts, video/redirector paths,
// auth tokens, or query signatures is rejected before any third-party request is built.
func IsEligibleForPublicImageCDNFallback(rawURL string, allowLocalhost bool) (cleanOriginURL string, parsed *url.URL, ok bool) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.ContainsAny(trimmed, " \t\r\n") {
		return "", nil, false
	}
	// Reject semicolon-delimited query parameters so url.ParseQuery never fails closed/ambiguous.
	if idx := strings.IndexByte(trimmed, '?'); idx >= 0 && strings.ContainsRune(trimmed[idx+1:], ';') {
		return "", nil, false
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil {
		return "", nil, false
	}
	u.Fragment = ""

	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", nil, false
	}

	// Hard host & path denylist (private X media, signed Weibo video/LivePhoto CDNs, VOD CDNs).
	if host == "ton.x.com" || strings.HasSuffix(host, ".ton.x.com") ||
		host == "ton.twitter.com" || strings.HasSuffix(host, ".ton.twitter.com") ||
		host == "video.twimg.com" || strings.HasSuffix(host, ".video.twimg.com") ||
		host == "video.weibo.com" ||
		host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") ||
		host == "us.sinaimg.cn" || strings.HasSuffix(host, ".us.sinaimg.cn") ||
		host == "bilivideo.com" || strings.HasSuffix(host, ".bilivideo.com") ||
		host == "bilivideo.cn" || strings.HasSuffix(host, ".bilivideo.cn") ||
		host == "douyinvod.com" || strings.HasSuffix(host, ".douyinvod.com") ||
		host == "pscp.tv" || strings.HasSuffix(host, ".pscp.tv") ||
		host == "periscope.tv" || strings.HasSuffix(host, ".periscope.tv") ||
		host == "cdn.syndication.twimg.com" {
		return "", nil, false
	}

	lowerPath := strings.ToLower(u.Path)
	if lowerPath == "" || lowerPath == "/" ||
		strings.Contains(lowerPath, "/i/ton/") ||
		strings.Contains(lowerPath, "/aweme/v1/play") ||
		strings.Contains(lowerPath, "/media/play") ||
		strings.Contains(lowerPath, "/bfs/subtitle") {
		return "", nil, false
	}
	if decodedPath, dErr := url.PathUnescape(u.EscapedPath()); dErr != nil || strings.Contains(strings.ToLower(decodedPath), "/i/ton/") {
		return "", nil, false
	}

	// Inspect query parameters against global signature/token denylist.
	q, qErr := url.ParseQuery(u.RawQuery)
	if qErr != nil {
		return "", nil, false
	}
	for k, vals := range q {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" || sensitiveQueryKeyDenylist[lk] {
			return "", nil, false
		}
		for _, v := range vals {
			lv := strings.ToLower(v)
			if strings.Contains(lv, "ssig=") || strings.Contains(lv, "expires=") || strings.Contains(lv, "token=") || strings.Contains(lv, "signature=") {
				return "", nil, false
			}
		}
	}

	isLoopback := allowLocalhost && IsLoopbackHost(host)
	if isLoopback {
		if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
			return "", nil, false
		}
		normQuery, qOK := normalizeAllowedPublicImageQuery(q, true)
		if !qOK {
			return "", nil, false
		}
		clean := &url.URL{
			Scheme:   strings.ToLower(u.Scheme),
			Host:     u.Host,
			Path:     u.Path,
			RawPath:  u.RawPath,
			RawQuery: normQuery,
		}
		return clean.String(), clean, true
	}

	if !strings.EqualFold(u.Scheme, "https") {
		return "", nil, false
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", nil, false
	}

	switch {
	case host == "sinaimg.cn" || strings.HasSuffix(host, ".sinaimg.cn"):
		// Unsigned static Weibo images carry zero query parameters.
		if u.RawQuery != "" {
			return "", nil, false
		}
		if !hasAllowedStaticImageExt(lowerPath) {
			return "", nil, false
		}
		clean := &url.URL{
			Scheme:  "https",
			Host:    host,
			Path:    u.Path,
			RawPath: u.RawPath,
		}
		return clean.String(), clean, true

	case host == "pbs.twimg.com":
		normQuery, qOK := normalizeAllowedPublicImageQuery(q, true)
		if !qOK {
			return "", nil, false
		}
		if normQuery == "" && !hasAllowedStaticImageExt(lowerPath) {
			return "", nil, false
		}
		clean := &url.URL{
			Scheme:   "https",
			Host:     host,
			Path:     u.Path,
			RawPath:  u.RawPath,
			RawQuery: normQuery,
		}
		return clean.String(), clean, true

	case strings.HasSuffix(host, ".hdslb.com"):
		if u.RawQuery != "" || !hasAllowedStaticImageExt(lowerPath) {
			return "", nil, false
		}
		cleanPath := strings.SplitN(u.Path, "@", 2)[0]
		if !hasAllowedStaticImageExt(strings.ToLower(cleanPath)) {
			return "", nil, false
		}
		clean := &url.URL{
			Scheme: "https",
			Host:   host,
			Path:   cleanPath,
		}
		return clean.String(), clean, true
	}

	return "", nil, false
}

func hasAllowedStaticImageExt(lowerPath string) bool {
	clean := strings.SplitN(lowerPath, ":", 2)[0]
	switch path.Ext(clean) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".heic":
		return true
	}
	return false
}

func normalizeAllowedPublicImageQuery(q url.Values, allowFormatAndName bool) (string, bool) {
	if len(q) == 0 {
		return "", true
	}
	if !allowFormatAndName {
		return "", false
	}
	var parts []string
	for k, vals := range q {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk != "format" && lk != "name" {
			return "", false
		}
		if len(vals) != 1 || !isSafeShortAlphaNum(vals[0]) {
			return "", false
		}
	}
	if f := q.Get("format"); f != "" {
		parts = append(parts, "format="+strings.ToLower(f))
	}
	if n := q.Get("name"); n != "" {
		parts = append(parts, "name="+n)
	}
	return strings.Join(parts, "&"), true
}

func isSafeShortAlphaNum(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// FetchPublicImageCDNFallback queries configured public CDN cache / web archive
// providers in order for an eligible unsigned public static image URL, returning
// the first verified genuine non-placeholder image payload along with its MIME type
// and provider name.
func FetchPublicImageCDNFallback(
	ctx context.Context,
	cfg PublicImageCDNFallbackConfig,
	originURL string,
	maxBytes int64,
	expectedW, expectedH int,
	isGIFSlot bool,
) (data []byte, contentType string, providerName string, ok bool) {
	if ctx.Err() != nil {
		return nil, "", "", false
	}
	// Never issue real external requests in loopback test mode unless test providers are explicitly supplied.
	if cfg.AllowLocalhost && len(cfg.Providers) == 0 {
		return nil, "", "", false
	}
	cleanOrigin, parsedOrigin, eligible := IsEligibleForPublicImageCDNFallback(originURL, cfg.AllowLocalhost)
	if !eligible {
		return nil, "", "", false
	}

	providers := cfg.Providers
	if len(providers) == 0 {
		providers = DefaultPublicImageCDNProviders()
	}
	if maxBytes <= 0 || maxBytes > DefaultMaxImageBytes {
		maxBytes = DefaultMaxImageBytes
	}

	headerTimeout := cfg.HeaderTimeout
	if headerTimeout <= 0 {
		headerTimeout = defaultFallbackHeaderTimeout
	}
	idleTimeout := cfg.IdleReadTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultFallbackIdleTimeout
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	guard := NewGuardedMediaDialer(Weibo, cfg.AllowLocalhost, dialer, "已拦截公共图片缓存指向内网或回环地址")
	ownedTransport := &http.Transport{
		Proxy:                 guard.Proxy,
		DialContext:           guard.DialContext,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
	defer ownedTransport.CloseIdleConnections()

	var rt http.RoundTripper = ownedTransport
	if cfg.AllowLocalhost {
		rt = LoopbackTransport(rt)
	}
	if cfg.WrapTransport != nil {
		rt = cfg.WrapTransport(rt)
	}

	for _, prov := range providers {
		if ctx.Err() != nil {
			return nil, "", "", false
		}
		if prov.BuildURL == nil {
			continue
		}
		targetURL, built := prov.BuildURL(parsedOrigin, cleanOrigin)
		if !built || strings.TrimSpace(targetURL) == "" {
			continue
		}
		if !validateFallbackProviderURL(targetURL, prov.AllowedDomains, cfg.AllowLocalhost) {
			continue
		}

		provLimit := maxBytes
		if prov.Name == "photon" && provLimit > photonMaxImageBytes {
			provLimit = photonMaxImageBytes
		}
		maxHops := prov.MaxRedirects
		if maxHops < 0 {
			maxHops = 0
		}

		allowedDomains := prov.AllowedDomains
		allowLocal := cfg.AllowLocalhost
		client := &http.Client{
			Transport: rt,
			Timeout:   0,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > maxHops {
					return errors.New("fallback redirect limit exceeded")
				}
				if !validateFallbackProviderURL(req.URL.String(), allowedDomains, allowLocal) {
					return errors.New("fallback redirect host blocked")
				}
				stripSensitiveFallbackHeaders(req, ua)
				return nil
			},
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			continue
		}
		stripSensitiveFallbackHeaders(req, ua)

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, ct, valid := readAndValidateFallbackImageResponse(resp, provLimit, idleTimeout, cleanOrigin, expectedW, expectedH, isGIFSlot)
		if valid {
			return raw, ct, prov.Name, true
		}
	}

	return nil, "", "", false
}

func stripSensitiveFallbackHeaders(req *http.Request, ua string) {
	if req == nil {
		return
	}
	req.Header.Del("Cookie")
	req.Header.Del("Authorization")
	req.Header.Del("X-Csrf-Token")
	req.Header.Del("X-XSRF-TOKEN")
	req.Header.Del("Referer")
	req.Header.Del("Origin")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
}

func validateFallbackProviderURL(rawURL string, allowedDomains []string, allowLocalhost bool) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if allowLocalhost && IsLoopbackHost(host) {
		return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	return MatchDomainSuffix(host, allowedDomains)
}

func readAndValidateFallbackImageResponse(
	resp *http.Response,
	maxBytes int64,
	idleTimeout time.Duration,
	cleanOriginURL string,
	expectedW, expectedH int,
	isGIFSlot bool,
) ([]byte, string, bool) {
	body := WrapIdleReadCloser(resp.Body, idleTimeout, nil)
	defer body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	if resp.ContentLength > maxBytes {
		return nil, "", false
	}
	rawCT := strings.TrimSpace(resp.Header.Get("Content-Type"))
	mt, _, _ := mime.ParseMediaType(rawCT)
	mt = strings.ToLower(strings.TrimSpace(mt))
	switch mt {
	case "text/html", "application/xhtml+xml", "application/json", "text/json", "text/plain", "text/xml", "application/xml", "application/javascript", "text/javascript":
		return nil, "", false
	}
	if mt != "" && !strings.HasPrefix(mt, "image/") && mt != "application/octet-stream" {
		return nil, "", false
	}

	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maxBytes {
		return nil, "", false
	}
	if LooksLikeDisguisedImageText(data) {
		return nil, "", false
	}
	ext, magicOK := SniffImageMagicBytes(data)
	if !magicOK {
		return nil, "", false
	}
	if LooksLikeExpiredPlaceholderImageBytes(data, cleanOriginURL, expectedW, expectedH, isGIFSlot) {
		return nil, "", false
	}
	if mt == "" || mt == "application/octet-stream" {
		mt = mimeTypeForImageExt(ext)
	}
	return data, mt, true
}

func mimeTypeForImageExt(ext string) string {
	switch ext {
	case ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".avif":
		return "image/avif"
	case ".heic":
		return "image/heic"
	default:
		return "image/jpeg"
	}
}
