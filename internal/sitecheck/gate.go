package sitecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// ErrQuotaExceeded is returned BEFORE a network request is sent when an ACT quota is exhausted.
var ErrQuotaExceeded = errors.New("sitecheck: quota exceeded for authorization")

// Usage tracks cumulative site-check consumption for a specific ACT authorization ID.
type Usage struct {
	ActID          string         `json:"actId"`
	UpdatedAt      string         `json:"updatedAt"`
	Runs           int            `json:"runs"`
	APICalls       int            `json:"apiCalls"`
	NavCalls       int            `json:"navCalls"`
	DynamicFeeds   int            `json:"dynamicFeeds"`
	VideoViews     int            `json:"videoViews"`
	PlayURLCalls   int            `json:"playUrlCalls"`
	DynamicDetails int            `json:"dynamicDetails"`
	SpaceReads     int            `json:"spaceReads"`
	QRCalls        int            `json:"qrCalls"`
	MediaFiles     int            `json:"mediaFiles"`
	MediaBytes     int64          `json:"mediaBytes"`
	Categories     map[string]int `json:"categories,omitempty"`
}

// Limits defines the maximum allowed operations under one real-site authorization (docs/dev-rules/workflow.md#site-requests).
type Limits struct {
	ActID         string         `json:"actId"`
	MaxRuns       int            `json:"maxRuns"`
	MaxAPICalls   int            `json:"maxApiCalls"`
	MaxMediaFiles int            `json:"maxMediaFiles"`
	MaxMediaBytes int64          `json:"maxMediaBytes"`
	MaxByCategory map[string]int `json:"maxByCategory,omitempty"`
}

// Gate enforces ACT quota limits backed by a persistent JSON ledger (`usage-<ACT>.json`).
type Gate struct {
	mu         sync.Mutex
	ledgerPath string
	limits     Limits
	usage      Usage
}

// OpenGate loads or initializes the persistent usage ledger at `<ledgerDir>/usage-<actId>.json`.
func OpenGate(ledgerDir string, limits Limits) (*Gate, error) {
	actID := strings.TrimSpace(limits.ActID)
	if actID == "" {
		return nil, fmt.Errorf("sitecheck: actId is required")
	}
	if err := os.MkdirAll(ledgerDir, 0o700); err != nil {
		return nil, err
	}
	ledgerPath := filepath.Join(ledgerDir, fmt.Sprintf("usage-%s.json", actID))
	u := Usage{
		ActID:      actID,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Categories: make(map[string]int),
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sitecheck: read %s: %w", ledgerPath, err)
	}
	if err == nil && len(raw) > 0 {
		if uErr := json.Unmarshal(raw, &u); uErr != nil {
			return nil, fmt.Errorf("sitecheck: parse %s: %w", ledgerPath, uErr)
		}
		if strings.TrimSpace(u.ActID) == "" {
			u.ActID = actID
		}
		if u.Categories == nil {
			u.Categories = make(map[string]int)
		}
	}
	return &Gate{
		ledgerPath: ledgerPath,
		limits:     limits,
		usage:      u,
	}, nil
}

func cloneUsage(in Usage) Usage {
	cp := in
	cp.Categories = make(map[string]int, len(in.Categories))
	for k, v := range in.Categories {
		cp.Categories[k] = v
	}
	return cp
}

// Usage returns a snapshot of the current usage counters.
func (g *Gate) Usage() Usage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneUsage(g.usage)
}

// BeginRun checks and increments the run counter before starting a site-check run.
func (g *Gate) BeginRun() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limits.MaxRuns > 0 && g.usage.Runs >= g.limits.MaxRuns {
		return fmt.Errorf("%w: runs %d/%d", ErrQuotaExceeded, g.usage.Runs, g.limits.MaxRuns)
	}
	next := cloneUsage(g.usage)
	next.Runs++
	return g.commitUsageLocked(next)
}

// AcquireAPI checks and reserves 1 API call of the given category BEFORE the HTTP request is sent.
func (g *Gate) AcquireAPI(category string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.limits.MaxAPICalls > 0 && g.usage.APICalls >= g.limits.MaxAPICalls {
		return fmt.Errorf("%w: apiCalls %d/%d", ErrQuotaExceeded, g.usage.APICalls, g.limits.MaxAPICalls)
	}
	if len(g.limits.MaxByCategory) > 0 {
		maxCat, ok := g.limits.MaxByCategory[category]
		if !ok || maxCat <= 0 {
			return fmt.Errorf("%w: category %s not allowed", ErrQuotaExceeded, category)
		}
		cur := categoryCount(g.usage, category)
		if cur >= maxCat {
			return fmt.Errorf("%w: category %s %d/%d", ErrQuotaExceeded, category, cur, maxCat)
		}
	}

	next := cloneUsage(g.usage)
	next.APICalls++
	incrementCategory(&next, category)
	return g.commitUsageLocked(next)
}

// AcquireMedia checks and reserves media file/byte quota BEFORE downloading media bytes.
func (g *Gate) AcquireMedia(expectedBytes int64) error {
	if expectedBytes < 0 {
		return fmt.Errorf("sitecheck: invalid negative expectedBytes %d", expectedBytes)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limits.MaxMediaFiles > 0 && g.usage.MediaFiles >= g.limits.MaxMediaFiles {
		return fmt.Errorf("%w: mediaFiles %d/%d", ErrQuotaExceeded, g.usage.MediaFiles, g.limits.MaxMediaFiles)
	}
	if g.limits.MaxMediaBytes > 0 && g.usage.MediaBytes+expectedBytes > g.limits.MaxMediaBytes {
		return fmt.Errorf("%w: mediaBytes %d+%d/%d", ErrQuotaExceeded, g.usage.MediaBytes, expectedBytes, g.limits.MaxMediaBytes)
	}
	next := cloneUsage(g.usage)
	next.MediaFiles++
	next.MediaBytes += expectedBytes
	return g.commitUsageLocked(next)
}

// WrapTransport wraps base RoundTripper so every outgoing request is gated by AcquireAPI before transmission.
func (g *Gate) WrapTransport(base http.RoundTripper, classify func(*http.Request) string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cat := "api"
		if classify != nil {
			if c := classify(req); c != "" {
				cat = c
			}
		}
		if err := g.AcquireAPI(cat); err != nil {
			return nil, err
		}
		return base.RoundTrip(req)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func categoryCount(u Usage, category string) int {
	count := u.Categories[category]
	switch category {
	case "nav":
		if u.NavCalls > count {
			return u.NavCalls
		}
	case "dynamic_feed":
		if u.DynamicFeeds > count {
			return u.DynamicFeeds
		}
	case "video_view":
		if u.VideoViews > count {
			return u.VideoViews
		}
	case "playurl":
		if u.PlayURLCalls > count {
			return u.PlayURLCalls
		}
	case "dynamic_detail":
		if u.DynamicDetails > count {
			return u.DynamicDetails
		}
	case "space_read":
		if u.SpaceReads > count {
			return u.SpaceReads
		}
	case "qr":
		if u.QRCalls > count {
			return u.QRCalls
		}
	}
	return count
}

func incrementCategory(u *Usage, category string) {
	switch category {
	case "nav":
		u.NavCalls++
	case "dynamic_feed":
		u.DynamicFeeds++
	case "video_view":
		u.VideoViews++
	case "playurl":
		u.PlayURLCalls++
	case "dynamic_detail":
		u.DynamicDetails++
	case "space_read":
		u.SpaceReads++
	case "qr":
		u.QRCalls++
	}
	if u.Categories == nil {
		u.Categories = make(map[string]int)
	}
	u.Categories[category]++
}

func (g *Gate) commitUsageLocked(next Usage) error {
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := storage.WriteFileAtomic(g.ledgerPath, raw, 0o600); err != nil {
		return err
	}
	g.usage = next
	return nil
}

var secretReceiptMarkers = []string{
	// Bilibili Cookie & refresh markers
	"SESSDATA=",
	"bili_jct=",
	"DedeUserID__ckMd5=",
	"ac_time_value=",
	"refresh_token=",
	"access_key=",
	// Douyin Cookie & session markers
	"sessionid=",
	"sessionid_ss=",
	"sid_guard=",
	"msToken=",
}

var (
	signedReceiptURLRe  = regexp.MustCompile(`(?i)https?://[^\s"'<>` + "`" + `]*?(a_bogus|x-(?:secsdk-web-)?signature)(?:=[A-Za-z0-9%+/=._-]{4,}|/[A-Za-z0-9%+/=._-]{4,})`)
	weiboSecretCookieRe = regexp.MustCompile(`\b(SUB|SUBP|XSRF-TOKEN|WBPSESS|SCF|ALF|SSOLoginState|SUHB|PC_TOKEN|_T_WM)=[A-Za-z0-9%_-][A-Za-z0-9%|._+/=-]{7,}`)
	weiboSignedSSigRe   = regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `?&]|\\u0026|&amp;|%3f|%26)ssig(?:=|%3d)[A-Za-z0-9%+/=._-]{4,}`)
	weiboSignedExpKIDRe = regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `?&]|\\u0026|&amp;|%3f|%26)(?:expires(?:=|%3d)[0-9]{9,}[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%26)kid(?:=|%3d)[A-Za-z0-9%,._-]{3,}|kid(?:=|%3d)[A-Za-z0-9%,._-]{3,}[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%26)expires(?:=|%3d)[0-9]{9,})`)

	// X Cookie, CSRF header, Bearer token, and signed/private media URL patterns (docs/research/x.md §15.1 #20).
	xSecretCookieRawRe  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)(auth_token|ct0|kdt)=(?:\\?["'])?([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})`)
	xSecretCookieEncRe  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)(auth_token|ct0|kdt)%3d(?:%22|%27)?([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){7,})`)
	xSecretCookieJSONRe = regexp.MustCompile(`(?i)\\?"(auth_token|ct0|kdt)\\?"[ \t]*:[ \t]*\\?"([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})\\?"`)
	xCSRFHeaderRe       = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%20)x-csrf-token(?:[ \t]*:[ \t]*(?:\\?["'])?|:\[)([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFHeaderEncRe    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)x-csrf-token(?:%3a(?:%20|\+)*|:(?:%20)+)(?:%22|%27)?([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFKVRe           = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)x-csrf-token=(?:\\?["'])?([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFKVEncRe        = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)x-csrf-token%3d(?:%22|%27)?([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFJSONRe         = regexp.MustCompile(`(?i)\\?"x-csrf-token\\?"[ \t]*:[ \t]*\\?"([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})\\?"`)
	xAuthBearerHeaderRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%20)authorization(?:[ \t]*:[ \t]*(?:\\?["'])?|:\[)bearer[ \t]+([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){11,})`)
	xAuthBearerJSONRe   = regexp.MustCompile(`(?i)\\?"authorization\\?"[ \t]*:[ \t]*\\?"bearer[ \t]+([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){11,})\\?"`)
	xAuthBearerEncRe    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)authorization(?:%3a(?:%20|\+)*|:(?:%20)+)(?:%22|%27)?bearer(?:%20|\+)+([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){11,})`)
	xBareBearerRe       = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt])bearer[ \t]+([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){15,})`)
	xBearerJSONRe       = regexp.MustCompile(`(?i)\\?"bearer(?:_?token)?\\?"[ \t]*:[ \t]*\\?"([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){15,})\\?"`)
	xRawWebBearerRe     = regexp.MustCompile(`(?:^|[^A-Za-z0-9_]|\\[nrt]|(?i:%20|%3a|%3d|%22|%27))(A{18,}(?:[A-Za-z0-9._+/=-]|(?i:%2b|%2f|%3d|%7c)){16,})`)
	xSignedMediaURLRe   = regexp.MustCompile(`(?i)https?(?:://|%3a%2f%2f)(?:[A-Za-z0-9-]+\.)*(?:twimg\.com|pscp\.tv|periscope\.tv)(?:/|%2f)[^\s"'<>` + "`" + `]*?(?:\?|&|\\u0026|&amp;|%3f|%26)(x-amz-signature|x-amz-security-token|x-amz-credential|signature|sig|hmac|hdnts|policy|key-pair-id|token|auth)(?:=|%3d)([A-Za-z0-9][A-Za-z0-9%+/=._~-]{5,})`)
	xPrivateTonURLRe    = regexp.MustCompile(`(?i)https?(?:://|%3a%2f%2f)ton\.(?:x|twitter)\.com(?:/|%2f)([^\s"'` + "`" + `]+)`)
)

func isLikelyTokenValue(v string, minLen int, plainAlphaMin int) bool {
	if len(v) < minLen {
		return false
	}
	allHex := true
	hasLower := false
	hasInternalUpper := false
	hasPunct := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= '0' && c <= '9', c == '_', c == '%', c == '+', c == '/', c == '=':
			return true
		case c >= 'a' && c <= 'f':
			hasLower = true
		case c >= 'A' && c <= 'F':
			if i > 0 {
				hasInternalUpper = true
			}
		case c >= 'g' && c <= 'z':
			allHex = false
			hasLower = true
		case c >= 'A' && c <= 'Z':
			allHex = false
			if i > 0 {
				hasInternalUpper = true
			}
		case c == '-' || c == '.':
			allHex = false
			hasPunct = true
		default:
			allHex = false
		}
	}
	if allHex || (hasLower && hasInternalUpper) {
		return true
	}
	return !hasPunct && len(v) >= plainAlphaMin
}

func isPlaceholderURLPath(path string) bool {
	if len(path) < 4 {
		return true
	}
	if strings.HasPrefix(path, "[") || strings.HasPrefix(path, ".") {
		return true
	}
	if strings.ContainsAny(path, "<>") {
		return true
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, `\u003c`) || strings.Contains(lower, "%3c")
}

// ScanTextForSecrets checks an arbitrary text or JSON payload for Bilibili/Douyin/Weibo/X
// Cookie/CSRF/Bearer markers and signed or private media URLs.
func ScanTextForSecrets(text string) []string {
	var findings []string
	for _, marker := range secretReceiptMarkers {
		if strings.Contains(text, marker) {
			findings = append(findings, fmt.Sprintf("contains secret marker %q", marker))
		}
	}
	seenWeiboKeys := make(map[string]bool)
	for _, m := range weiboSecretCookieRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 && !seenWeiboKeys[m[1]] {
			seenWeiboKeys[m[1]] = true
			findings = append(findings, fmt.Sprintf("contains secret marker %q (redacted)", m[1]+"="))
		}
	}
	seenXKeys := make(map[string]bool)
	for _, re := range []*regexp.Regexp{xSecretCookieRawRe, xSecretCookieEncRe, xSecretCookieJSONRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) < 3 || !isLikelyTokenValue(m[2], 8, 24) {
				continue
			}
			key := strings.ToLower(m[1])
			if !seenXKeys[key] {
				seenXKeys[key] = true
				findings = append(findings, fmt.Sprintf("contains secret marker %q (redacted)", key+"="))
			}
		}
	}
	csrfFound := false
	for _, re := range []*regexp.Regexp{xCSRFHeaderRe, xCSRFHeaderEncRe, xCSRFKVRe, xCSRFKVEncRe, xCSRFJSONRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) >= 2 && isLikelyTokenValue(m[1], 8, 24) {
				csrfFound = true
				break
			}
		}
		if csrfFound {
			break
		}
	}
	if csrfFound {
		findings = append(findings, `contains secret marker "x-csrf-token" (redacted)`)
	}
	bearerFound := false
	for _, re := range []*regexp.Regexp{xAuthBearerHeaderRe, xAuthBearerJSONRe, xAuthBearerEncRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) >= 2 && isLikelyTokenValue(m[1], 12, 32) {
				bearerFound = true
				break
			}
		}
		if bearerFound {
			break
		}
	}
	if !bearerFound {
		for _, re := range []*regexp.Regexp{xBareBearerRe, xBearerJSONRe} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if len(m) >= 2 && isLikelyTokenValue(m[1], 16, 32) {
					bearerFound = true
					break
				}
			}
			if bearerFound {
				break
			}
		}
	}
	if !bearerFound {
		for _, m := range xRawWebBearerRe.FindAllStringSubmatch(text, -1) {
			if len(m) >= 2 && strings.Trim(m[1], "A") != "" {
				bearerFound = true
				break
			}
		}
	}
	if bearerFound {
		findings = append(findings, `contains secret marker "Authorization: Bearer" (redacted)`)
	}
	if m := signedReceiptURLRe.FindStringSubmatch(text); len(m) >= 2 {
		findings = append(findings, fmt.Sprintf("contains signed URL marker %q (redacted)", strings.ToLower(m[1])))
	}
	if weiboSignedSSigRe.MatchString(text) {
		findings = append(findings, `contains signed URL marker "ssig" (redacted)`)
	} else if weiboSignedExpKIDRe.MatchString(text) {
		findings = append(findings, `contains signed URL marker "expires+kid" (redacted)`)
	}
	if m := xSignedMediaURLRe.FindStringSubmatch(text); len(m) >= 3 {
		findings = append(findings, fmt.Sprintf("contains signed URL marker %q (redacted)", "x:"+strings.ToLower(m[1])))
	}
	for _, m := range xPrivateTonURLRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 && !isPlaceholderURLPath(m[1]) {
			findings = append(findings, `contains signed URL marker "x:ton-media" (redacted)`)
			break
		}
	}
	return findings
}

// ScanReceiptsForSecrets scans JSON receipts in dir for any leaked Cookie/token markers or signed URLs.
// Returns nil without error if dir does not exist (such as clean CI environments).
func ScanReceiptsForSecrets(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var findings []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, err
		}
		for _, item := range ScanTextForSecrets(string(raw)) {
			findings = append(findings, fmt.Sprintf("%s %s", entry.Name(), item))
		}
	}
	return findings, nil
}
