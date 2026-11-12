package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
)

const (
	act010ID                    = "act010"
	act010Label                 = "ACT-010"
	act010MaxRuns               = 1
	act010MaxAPICalls           = 18
	act010MaxVODRequests        = 16
	act010MaxVODBytes     int64 = 64 << 20 // 64 MiB
	act010LargeFileCutoff int64 = 24 << 20 // 24 MiB: probe head 8 MiB + tail 1 MiB only
	act010RangeChunkBytes int64 = 8 << 20  // 8 MiB closed Range chunks
	act010TailProbeBytes  int64 = 1 << 20  // 1 MiB tail Range chunk
	act010MaxLiveConns          = 2
	act010MaxLiveBytes    int64 = 50 << 20 // 50 MiB
	act010MaxRecord             = 25 * time.Second
	act010DefaultRecord         = 20 * time.Second
	act010DefaultSpacing        = 1200 * time.Millisecond
)

var act010APILimits = map[string]int{
	"profile_self":  2,
	"profile_other": 2,
	"aweme_post":    3,
	"aweme_detail":  4,
	"web_enter":     4,
	"reflow_info":   2,
	"hls_playlist":  1,
}

var (
	reAct010SecUID    = regexp.MustCompile(`^MS4wLjAB[A-Za-z0-9_-]{16,}$`)
	reAct010WorkID    = regexp.MustCompile(`^[1-9][0-9]{14,21}$`)
	reAct010RoomDigit = regexp.MustCompile(`^[1-9][0-9]{2,21}$`)
	reAct010AllDigits = regexp.MustCompile(`^[0-9]+$`)
)

type act010Targets struct {
	Version    int              `json:"version"`
	Act        string           `json:"act"`
	ProvidedAt string           `json:"providedAt"`
	Users      []act010User     `json:"users"`
	Works      []act010Work     `json:"works"`
	LiveRooms  []act010LiveRoom `json:"liveRooms"`
}

type act010User struct {
	SecUID string `json:"secUid"`
	URL    string `json:"url"`
}

type act010Work struct {
	AwemeID string   `json:"awemeId"`
	Kind    string   `json:"kind"`   // "video" | "note"
	Expect  []string `json:"expect"` // "pinned" | "recent" | "mixed"
	URL     string   `json:"url"`
}

type act010LiveRoom struct {
	WebRID string `json:"webRid"`
	Form   string `json:"form"` // "short_id" | "other"
	URL    string `json:"url"`
}

type act010Config struct {
	RootDir        string
	OutPath        string
	RecordDuration time.Duration
	MinSpacing     time.Duration // 0 => 1.2s default; <0 => 0s in synthetic tests
	Endpoints      douyin.Endpoints
	Transport      http.RoundTripper
	AllowLocalhost bool
}

type act010Receipt struct {
	Act              string                 `json:"act"`
	Commit           string                 `json:"commit,omitempty"`
	StartedAt        string                 `json:"startedAt"`
	FinishedAt       string                 `json:"finishedAt"`
	Passed           bool                   `json:"passed"`
	StoppedBy        string                 `json:"stoppedBy,omitempty"`
	TempMediaDeleted bool                   `json:"tempMediaDeleted"`
	SecretScanClean  bool                   `json:"secretScanClean"`
	Steps            []step                 `json:"steps"`
	Verifications    map[string]any         `json:"verifications"`
	APIUsage         sitecheck.Usage        `json:"apiUsage"`
	APIStatusCodes   map[string]map[int]int `json:"apiStatusCodes"`
	VODMedia         mediaUsage             `json:"vodMedia"`
	LiveMedia        mediaUsage             `json:"liveMedia"`
	StatusCounts     map[string]int         `json:"httpStatusCounts"`
	Redirects        []map[string]any       `json:"redirects,omitempty"`
	Limits           map[string]any         `json:"limits"`
	Notes            []string               `json:"notes,omitempty"`
}

type act010Redactor struct {
	mu      sync.Mutex
	cookies map[string]bool
	ids     map[string]bool
	handles map[string]bool
	urls    map[string]bool
}

func newAct010Redactor() *act010Redactor {
	return &act010Redactor{
		cookies: make(map[string]bool),
		ids:     make(map[string]bool),
		handles: make(map[string]bool),
		urls:    make(map[string]bool),
	}
}

func isSecretDouyinCookie(key, val string) bool {
	kLower := strings.ToLower(strings.TrimSpace(key))
	switch kLower {
	case "", "is_staff_user", "login_time":
		return false
	}
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if len(v) < 6 {
		return false
	}
	switch strings.ToLower(v) {
	case "true", "false", "null", "none", "undefined":
		return false
	}
	if reAct010AllDigits.MatchString(v) && len(v) < 12 {
		return false
	}
	if credential.IsStrippedDouyinCookieKey(key) {
		return true
	}
	for _, allowed := range credential.AllowedDouyinCookieKeys {
		if strings.EqualFold(allowed, key) {
			return true
		}
	}
	return false
}

func (r *act010Redactor) addCookieHeader(raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, part := range strings.Split(raw, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if isSecretDouyinCookie(k, v) {
				r.cookies[v] = true
			}
		}
	}
}

func (r *act010Redactor) addCookieMap(m map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range m {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if isSecretDouyinCookie(k, v) {
			r.cookies[v] = true
		}
	}
}

func (r *act010Redactor) addIDs(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) >= 3 && v != "0" {
			r.ids[v] = true
		}
	}
}

func (r *act010Redactor) addHandles(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) >= 3 && v != "0" {
			r.handles[v] = true
		}
	}
}

func (r *act010Redactor) addURLs(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			r.urls[v] = true
		}
	}
}

func (r *act010Redactor) scrubError(msg string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := msg
	for c := range r.cookies {
		out = strings.ReplaceAll(out, c, "[REDACTED]")
	}
	for u := range r.urls {
		out = strings.ReplaceAll(out, u, "[URL]")
	}
	for id := range r.ids {
		if len(id) >= 4 {
			out = strings.ReplaceAll(out, id, "[ID]")
		}
	}
	for h := range r.handles {
		if len(h) >= 4 {
			out = strings.ReplaceAll(out, h, "[ID]")
		}
	}
	if i := strings.Index(out, "http://"); i >= 0 {
		out = out[:i] + "[URL]"
	}
	if i := strings.Index(out, "https://"); i >= 0 {
		out = out[:i] + "[URL]"
	}
	return out
}

func (r *act010Redactor) verifyJSONClean(rawJSON string, rc *act010Receipt) error {
	if findings := sitecheck.ScanTextForSecrets(rawJSON); len(findings) > 0 {
		return fmt.Errorf("receipt failed secret scan: %s", findings[0])
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.cookies {
		if strings.Contains(rawJSON, c) {
			return errors.New("receipt contains raw Cookie value")
		}
	}
	for u := range r.urls {
		if strings.Contains(rawJSON, u) {
			return errors.New("receipt contains target URL")
		}
	}
	for id := range r.ids {
		// Any ID appearing as a quoted JSON string is always a leak.
		if strings.Contains(rawJSON, `"`+id+`"`) {
			return errors.New("receipt contains target or runtime account/work/room ID")
		}
		// Non-numeric IDs (sec_uid) >= 4 chars or numeric IDs >= 9 digits must not appear anywhere.
		if (!reAct010AllDigits.MatchString(id) && len(id) >= 4) || len(id) >= 9 {
			if strings.Contains(rawJSON, id) {
				return errors.New("receipt contains target or runtime identifier substring")
			}
		}
	}
	if rc != nil {
		for h := range r.handles {
			for _, s := range rc.Steps {
				if strings.Contains(s.Error, h) {
					return errors.New("receipt step error contains account unique_id")
				}
			}
			for _, n := range rc.Notes {
				if strings.Contains(n, h) {
					return errors.New("receipt note contains account unique_id")
				}
			}
		}
	}
	return nil
}

type act010Checker struct {
	gate               *sitecheck.Gate
	vod                *mediaBudget
	live               *mediaBudget
	hls                *mediaBudget // optional separate <=1 HLS playlist budget (used by ACT-011)
	minSpacing         time.Duration
	allowLocalhost     bool
	guestControlActive atomic.Bool
	vodMediaActive     atomic.Bool
	liveStreamActive   atomic.Bool

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu             sync.Mutex
	stopped        string
	statuses       map[string]int
	apiStatusCodes map[string]map[int]int
	lastGuestCode  int
	lastAuthCode   int
	playlists      []map[string]any
	redirects      []map[string]any
}

// noteRedirect records the shape of a media 3xx response (status, target scheme,
// whether the target host is an IP literal or a domain, non-default port, and
// whether it stays on the same host) without keeping the signed target URL.
func (c *act010Checker) noteRedirect(cat string, r *http.Request, resp *http.Response) {
	shape := map[string]any{
		"category": cat,
		"status":   resp.StatusCode,
		"bytes":    resp.ContentLength,
	}
	if loc, err := r.URL.Parse(resp.Header.Get("Location")); err == nil && loc.Host != "" {
		host := loc.Hostname()
		shape["targetScheme"] = strings.ToLower(loc.Scheme)
		if net.ParseIP(host) != nil {
			shape["targetHostKind"] = "ip"
		} else {
			shape["targetHostKind"] = "domain"
		}
		shape["targetCustomPort"] = loc.Port() != "" && loc.Port() != "80" && loc.Port() != "443"
		shape["targetSameHost"] = strings.EqualFold(host, r.URL.Hostname())
	} else {
		shape["targetMissing"] = true
	}
	c.mu.Lock()
	if len(c.redirects) < 4 {
		c.redirects = append(c.redirects, shape)
	}
	c.mu.Unlock()
}

func (c *act010Checker) redirectShapes() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.redirects...)
}

// isMediaRedirect reports a 3xx media response that carries a Location. CDN
// edges answer such redirects with a small text/html body ("302 Found"), which
// is a normal hop rather than a verification page.
func isMediaRedirect(resp *http.Response) bool {
	return resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.Header.Get("Location") != ""
}

func (c *act010Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act010Checker) tripStop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act010Checker) notePlaylist(status int, contentType string, raw []byte) map[string]any {
	tags := map[string]int{}
	entries, relative := 0, 0
	exts := map[string]int{}
	mapBeforeFirstEntry := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			name, _, _ := strings.Cut(line, ":")
			tags[name]++
			if name == "#EXT-X-MAP" && entries == 0 {
				mapBeforeFirstEntry = true
			}
			continue
		}
		entries++
		if !strings.HasPrefix(strings.ToLower(line), "http") {
			relative++
		}
		pathPart, _, _ := strings.Cut(line, "?")
		exts[strings.ToLower(filepath.Ext(pathPart))]++
	}
	summary := map[string]any{
		"status":              status,
		"contentType":         contentType,
		"bytes":               len(raw),
		"tags":                tags,
		"entries":             entries,
		"relativeEntries":     relative,
		"entryExt":            exts,
		"mapBeforeFirstEntry": mapBeforeFirstEntry,
	}
	c.mu.Lock()
	if len(c.playlists) < 2 {
		c.playlists = append(c.playlists, summary)
	}
	c.mu.Unlock()
	return summary
}

type act010ReqKind int

const (
	act010KindShortLink act010ReqKind = iota
	act010KindJSONAPI
	act010KindHLSPlaylist
	act010KindVODMedia
	act010KindLiveFLV
	act010KindUnlisted
)

func (c *act010Checker) classify(r *http.Request) (act010ReqKind, string) {
	host := strings.ToLower(r.URL.Hostname())
	p := r.URL.Path
	lowerPath := strings.ToLower(p)

	if isShortLinkHost(host) {
		return act010KindShortLink, "short_link"
	}

	switch {
	case strings.HasSuffix(p, "/aweme/v1/web/user/profile/self/") || strings.HasSuffix(p, "/aweme/v1/web/user/profile/self"):
		return act010KindJSONAPI, "profile_self"
	case strings.HasSuffix(p, "/aweme/v1/web/user/profile/other/") || strings.HasSuffix(p, "/aweme/v1/web/user/profile/other"):
		return act010KindJSONAPI, "profile_other"
	case strings.HasSuffix(p, "/aweme/v1/web/aweme/post/") || strings.HasSuffix(p, "/aweme/v1/web/aweme/post"):
		return act010KindJSONAPI, "aweme_post"
	case strings.HasSuffix(p, "/aweme/v1/web/aweme/detail/") || strings.HasSuffix(p, "/aweme/v1/web/aweme/detail"):
		return act010KindJSONAPI, "aweme_detail"
	case strings.HasSuffix(p, "/webcast/room/web/enter/") || strings.HasSuffix(p, "/webcast/room/web/enter"):
		return act010KindJSONAPI, "web_enter"
	case strings.HasSuffix(p, "/webcast/room/reflow/info/") || strings.HasSuffix(p, "/webcast/room/reflow/info"):
		return act010KindJSONAPI, "reflow_info"
	case strings.HasSuffix(lowerPath, ".m3u8"):
		return act010KindHLSPlaylist, "hls_playlist"
	case c.liveStreamActive.Load() || strings.HasSuffix(lowerPath, ".flv"):
		return act010KindLiveFLV, "live_flv"
	case c.vodMediaActive.Load():
		return act010KindVODMedia, "vod_media"
	default:
		return act010KindUnlisted, "unlisted"
	}
}

func isShortLinkHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "v.douyin.com" || strings.HasSuffix(host, ".v.douyin.com") ||
		host == "v.amemv.com" || strings.HasSuffix(host, ".v.amemv.com")
}

func (c *act010Checker) pace(ctx context.Context) error {
	if c.minSpacing <= 0 {
		return nil
	}
	c.paceMu.Lock()
	defer c.paceMu.Unlock()
	if !c.lastReqAt.IsZero() {
		wait := c.minSpacing - time.Since(c.lastReqAt)
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	c.lastReqAt = time.Now()
	return nil
}

func (c *act010Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("act010 halted (%s)", reason)
		}

		kind, cat := c.classify(r)
		switch kind {
		case act010KindShortLink:
			return nil, fmt.Errorf("%w: short_link redirect quota is 0", sitecheck.ErrQuotaExceeded)
		case act010KindUnlisted:
			return nil, fmt.Errorf("%w: unlisted endpoint %s", sitecheck.ErrQuotaExceeded, r.URL.Path)
		case act010KindJSONAPI:
			if err := c.gate.AcquireAPI(cat); err != nil {
				return nil, err
			}
		case act010KindHLSPlaylist:
			if r.Header.Get("Cookie") != "" {
				return nil, errors.New("act010: refusing to send Cookie to HLS playlist")
			}
			if c.hls != nil {
				if err := c.hls.acquire(); err != nil {
					return nil, err
				}
			} else if err := c.gate.AcquireAPI(cat); err != nil {
				return nil, err
			}
		case act010KindVODMedia:
			if r.Header.Get("Cookie") != "" {
				return nil, errors.New("act010: refusing to send Cookie to VOD/image CDN")
			}
			if c.vod.bytes.Load() >= c.vod.maxBytes {
				return nil, fmt.Errorf("%w: vod bytes exhausted (%d)", sitecheck.ErrQuotaExceeded, c.vod.maxBytes)
			}
			if err := c.vod.acquire(); err != nil {
				return nil, err
			}
		case act010KindLiveFLV:
			if r.Header.Get("Cookie") != "" {
				return nil, errors.New("act010: refusing to send Cookie to live FLV CDN")
			}
			if c.live.bytes.Load() >= c.live.maxBytes {
				return nil, fmt.Errorf("%w: live bytes exhausted (%d)", sitecheck.ErrQuotaExceeded, c.live.maxBytes)
			}
			if err := c.live.acquire(); err != nil {
				return nil, err
			}
		}

		if err := c.pace(r.Context()); err != nil {
			return nil, err
		}

		resp, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.statuses[fmt.Sprintf("%s:%d", cat, resp.StatusCode)]++
		c.mu.Unlock()

		// Immediate HTTP status stop conditions (403, 429, 444).
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 444 {
			peek, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(peek))
			switch resp.StatusCode {
			case http.StatusForbidden:
				if bytes.Contains(peek, []byte("ArgusSecurityPlugin")) {
					c.tripStop("http_403_argus")
				} else {
					c.tripStop("http_403")
				}
			case http.StatusTooManyRequests:
				c.tripStop("http_429")
			case 444:
				c.tripStop("http_444")
			}
			return resp, nil
		}

		if (kind == act010KindVODMedia || kind == act010KindLiveFLV || kind == act010KindHLSPlaylist) && isMediaRedirect(resp) {
			c.noteRedirect(cat, r, resp)
			switch kind {
			case act010KindVODMedia:
				resp.Body = countingBody{ReadCloser: resp.Body, budget: c.vod}
			case act010KindLiveFLV:
				resp.Body = countingBody{ReadCloser: resp.Body, budget: c.live}
			}
			return resp, nil
		}

		switch kind {
		case act010KindJSONAPI:
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if readErr != nil {
				return nil, readErr
			}
			c.inspectJSONAPIResponse(cat, resp, raw)

		case act010KindHLSPlaylist:
			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if readErr != nil {
				return nil, readErr
			}
			if c.hls != nil {
				c.hls.bytes.Add(int64(len(raw)))
			}
			if strings.Contains(ct, "text/html") || bytes.Contains(raw, []byte("验证码中间页")) {
				c.tripStop("html_verify_page")
				return resp, nil
			}
			c.notePlaylist(resp.StatusCode, resp.Header.Get("Content-Type"), raw)

		case act010KindVODMedia:
			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			if strings.Contains(ct, "text/html") {
				c.tripStop("html_verify_page")
			}
			resp.Body = countingBody{ReadCloser: resp.Body, budget: c.vod}

		case act010KindLiveFLV:
			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			if strings.Contains(ct, "text/html") {
				c.tripStop("html_verify_page")
			}
			resp.Body = countingBody{ReadCloser: resp.Body, budget: c.live}
		}

		return resp, nil
	})
}

func (c *act010Checker) inspectJSONAPIResponse(cat string, resp *http.Response, raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		c.tripStop("empty_api_body")
		return
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") || trimmed[0] != '{' {
		if bytes.Contains(trimmed, []byte("ArgusSecurityPlugin")) {
			c.tripStop("argus_block")
		} else {
			c.tripStop("html_verify_page")
		}
		return
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		c.tripStop("non_json_api_body")
		return
	}

	statusCode := 0
	if rawCode, ok := top["status_code"]; ok {
		var n json.Number
		if json.Unmarshal(rawCode, &n) == nil {
			if parsed, pErr := strconv.Atoi(n.String()); pErr == nil {
				statusCode = parsed
			}
		}
	}

	c.mu.Lock()
	if c.apiStatusCodes[cat] == nil {
		c.apiStatusCodes[cat] = make(map[int]int)
	}
	c.apiStatusCodes[cat][statusCode]++
	if cat == "profile_self" {
		if c.guestControlActive.Load() {
			c.lastGuestCode = statusCode
		} else {
			c.lastAuthCode = statusCode
		}
	}
	c.mu.Unlock()

	if hasNonEmptyVerifyField(top) {
		c.tripStop("json_verify_challenge")
		return
	}
	if dataRaw, ok := top["data"]; ok {
		var dataMap map[string]json.RawMessage
		if json.Unmarshal(dataRaw, &dataMap) == nil && hasNonEmptyVerifyField(dataMap) {
			c.tripStop("json_verify_challenge")
			return
		}
	}

	switch statusCode {
	case 2154, 10000, 10001, 10006:
		c.tripStop("risk_control")
		return
	case 8, 2483, 10008, 20003:
		if !c.guestControlActive.Load() {
			c.tripStop("auth_required")
			return
		}
	}
}

func hasNonEmptyVerifyField(m map[string]json.RawMessage) bool {
	for _, key := range []string{"verify_center_decision_conf", "verify_data", "captcha", "shark_decision_conf"} {
		raw, ok := m[key]
		if !ok {
			continue
		}
		val := strings.TrimSpace(string(raw))
		if val != "" && val != "null" && val != `""` && val != "{}" && val != "[]" {
			return true
		}
	}
	return false
}

func runAct010Main(root, out string, recordFor time.Duration) {
	rc, err := runAct010(context.Background(), act010Config{
		RootDir:        root,
		OutPath:        out,
		RecordDuration: recordFor,
	})
	if err != nil && rc.StartedAt == "" {
		fatal(err.Error())
	}
	outDisp := out
	if outDisp == "" {
		outDisp = filepath.Join(root, "artifacts", "site-check", "act010.json")
	}
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d vodMedia=%d/%dB liveMedia=%d/%dB receipt=%s\n",
		act010Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls,
		rc.VODMedia.Requests, rc.VODMedia.Bytes, rc.LiveMedia.Requests, rc.LiveMedia.Bytes, outDisp)
	for _, s := range rc.Steps {
		fmt.Printf("  %-26s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ACT-010 error: %v\n", err)
	}
	if err != nil || !rc.Passed {
		os.Exit(1)
	}
}

func runAct010(parentCtx context.Context, cfg act010Config) (act010Receipt, error) {
	var rc act010Receipt
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	recordFor := cfg.RecordDuration
	if recordFor == 0 {
		recordFor = act010DefaultRecord
	}
	if recordFor < 0 || recordFor > act010MaxRecord {
		return rc, fmt.Errorf("recording duration %s exceeds authorized %s limit for ACT-010", recordFor, act010MaxRecord)
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", "act010.json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act010DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	tg, err := loadAndValidateAct010Targets(root)
	if err != nil {
		return rc, err
	}

	rawCookie, cleanedInitial, err := readAndValidateDouyinCookie(root)
	if err != nil {
		return rc, err
	}

	redactor := newAct010Redactor()
	redactor.addCookieHeader(rawCookie)
	redactor.addCookieMap(cleanedInitial)
	for _, u := range tg.Users {
		redactor.addIDs(u.SecUID)
		redactor.addURLs(u.URL)
	}
	for _, w := range tg.Works {
		redactor.addIDs(w.AwemeID)
		redactor.addURLs(w.URL)
	}
	for _, lr := range tg.LiveRooms {
		redactor.addIDs(lr.WebRID)
		redactor.addURLs(lr.URL)
	}

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID:         act010ID,
		MaxRuns:       act010MaxRuns,
		MaxAPICalls:   act010MaxAPICalls,
		MaxByCategory: act010APILimits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act010Checker{
		gate:           gate,
		vod:            &mediaBudget{name: "vod", maxRequests: act010MaxVODRequests, maxBytes: act010MaxVODBytes},
		live:           &mediaBudget{name: "live", maxRequests: act010MaxLiveConns, maxBytes: act010MaxLiveBytes},
		minSpacing:     spacing,
		allowLocalhost: cfg.AllowLocalhost,
		statuses:       make(map[string]int),
		apiStatusCodes: make(map[string]map[int]int),
	}

	workDir, err := os.MkdirTemp("", "trace-act010-")
	if err != nil {
		return rc, err
	}
	cleanupWorkDir := func() bool {
		_ = os.RemoveAll(workDir)
		_, statErr := os.Stat(workDir)
		return os.IsNotExist(statErr)
	}
	defer cleanupWorkDir()

	credStore, _, err := credential.OpenDouyin(filepath.Join(workDir, credential.DouyinFileName))
	if err != nil {
		return rc, err
	}

	wrappedTransport := ck.wrap(cfg.Transport)
	dyClient := douyin.NewClient(douyin.ClientOptions{
		CredStore:      credStore,
		Endpoints:      cfg.Endpoints,
		Transport:      wrappedTransport,
		MinInterval:    -1, // Pacing is enforced uniformly across API + CDN + live in act010Checker.
		JitterMax:      -1,
		AllowLocalhost: cfg.AllowLocalhost,
	})
	mediaHTTP := &http.Client{
		Transport: wrappedTransport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("media redirect limit exceeded")
			}
			return nil
		},
	}
	liveHTTP := &http.Client{
		Transport: wrappedTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("live redirect limit exceeded")
			}
			return nil
		},
	}

	rc = act010Receipt{
		Act:            act010Label,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		Verifications:  make(map[string]any),
		APIStatusCodes: ck.apiStatusCodes,
		StatusCounts:   ck.statuses,
		Limits: map[string]any{
			"api":               act010APILimits,
			"apiTotal":          act010MaxAPICalls,
			"shortLinkHops":     0,
			"vodMediaRequests":  act010MaxVODRequests,
			"vodMediaBytes":     act010MaxVODBytes,
			"largeFileCutoff":   act010LargeFileCutoff,
			"liveMediaRequests": act010MaxLiveConns,
			"liveMediaBytes":    act010MaxLiveBytes,
			"recordSeconds":     recordFor.Seconds(),
			"minSpacingMs":      spacing.Milliseconds(),
		},
	}
	if rawVer, vErr := os.ReadFile(filepath.Join(root, "dist", "trace-dev", "VERSION.txt")); vErr == nil {
		for _, line := range strings.Split(string(rawVer), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "commit="); ok {
				rc.Commit = v
			}
		}
	}

	ctx, cancel := context.WithTimeout(parentCtx, 4*time.Minute)
	defer cancel()

	runStep := func(name string, fn func(facts map[string]any) error) bool {
		if stopped := ck.stopReason(); stopped != "" {
			rc.Steps = append(rc.Steps, step{
				Name:    name,
				Skipped: true,
				Error:   "stopped earlier: " + stopped,
			})
			return false
		}
		facts := make(map[string]any)
		start := time.Now()
		err := fn(facts)
		st := step{
			Name:     name,
			OK:       err == nil,
			Facts:    facts,
			Duration: time.Since(start).Milliseconds(),
		}
		if errors.Is(err, errSkip) {
			st.OK = true
			st.Skipped = true
		} else if err != nil {
			st.ErrKind = site.KindOf(err)
			if errors.Is(err, sitecheck.ErrQuotaExceeded) {
				st.ErrKind = "quota"
			}
			st.Error = redactor.scrubError(err.Error())
			switch st.ErrKind {
			case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindAuthRequired:
				ck.tripStop(st.ErrKind)
			}
		}
		if len(st.Facts) == 0 {
			st.Facts = nil
		}
		rc.Steps = append(rc.Steps, st)
		return st.OK
	}

	// Step 1: V-01 & V-02 — profile/self guest control (no Cookie) vs authenticated Cookie.
	initialHadMSToken := cleanedInitial["msToken"] != ""
	initialHadUIFID := cleanedInitial["UIFID"] != "" || cleanedInitial["uifid"] != "" || cleanedInitial["UIFID_TEMP"] != ""
	initialHadTTWID := cleanedInitial["ttwid"] != ""
	runStep("v01_login_control", func(f map[string]any) error {
		ck.guestControlActive.Store(true)
		guestErr := dyClient.CheckProfileSelfGuest(ctx)
		ck.guestControlActive.Store(false)

		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("guest control stopped: %s", stopped)
		}
		guestRejectedAsExpected := guestErr != nil && site.IsKind(guestErr, site.ErrKindAuthRequired)
		ck.mu.Lock()
		guestCode := ck.lastGuestCode
		ck.mu.Unlock()
		f["guestRejectedAuthRequired"] = guestRejectedAsExpected
		f["guestStatusCode"] = guestCode
		if !guestRejectedAsExpected {
			if guestErr != nil {
				return guestErr
			}
			return errors.New("profile/self without Cookie unexpectedly succeeded")
		}

		st, loginErr := dyClient.LoginWithCookie(ctx, rawCookie)
		ck.mu.Lock()
		authCode := ck.lastAuthCode
		ck.mu.Unlock()
		if loginErr != nil {
			f["authStatusCode"] = authCode
			return loginErr
		}
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID)
		redactor.addCookieValuesFromStore(credStore)

		hasMSTokenAfter := credStore.MSToken() != ""
		hasTTWIDAfter := credStore.TTWID() != ""
		hasUIFIDAfter := credStore.UIFID() != ""
		f["authStatusCode"] = authCode
		f["loggedIn"] = st.LoggedIn
		f["hasUID"] = st.UID != ""
		f["hasSecUID"] = reAct010SecUID.MatchString(st.SecUID)
		f["hasNickname"] = st.Uname != ""
		f["cookieInitialHadMSToken"] = initialHadMSToken
		f["cookieInitialHadUIFID"] = initialHadUIFID
		f["cookieInitialHadTTWID"] = initialHadTTWID
		f["storeHasMSTokenAfter"] = hasMSTokenAfter
		f["storeHasUIFIDAfter"] = hasUIFIDAfter
		f["storeHasTTWIDAfter"] = hasTTWIDAfter

		rc.Verifications["V-01"] = map[string]any{
			"guestRejectedAuthRequired": guestRejectedAsExpected,
			"guestStatusCode":           guestCode,
			"authStatusCode":            authCode,
			"authProfileComplete":       st.LoggedIn && st.UID != "" && reAct010SecUID.MatchString(st.SecUID) && st.Uname != "",
		}
		return nil
	})

	// Step 2: V-02 & V-03 — profile/other (up to 2 users) + aweme/post (user[0] p1+p2, user[1] p1).
	profilesBySec := make(map[string]*douyin.UserProfile)
	var offlineUserSecUID string
	runStep("v02_v03_users_and_posts", func(f map[string]any) error {
		var userSummaries []map[string]any
		var v03Summary map[string]any

		for idx, u := range tg.Users {
			prof, err := dyClient.GetUserProfile(ctx, u.SecUID)
			if err != nil {
				return err
			}
			profilesBySec[u.SecUID] = prof
			redactor.addIDs(prof.SecUID, prof.UID, prof.ShortID, prof.RoomID, prof.WebRID)
			redactor.addHandles(prof.UniqueID)
			if (prof.RoomID == "" || prof.RoomID == "0") && offlineUserSecUID == "" {
				offlineUserSecUID = u.SecUID
			}

			seen := make(map[string]bool)
			p1, err := dyClient.ListUserPosts(ctx, douyin.UserPostsRequest{
				SecUID:       u.SecUID,
				MaxCursor:    "0",
				KnownProfile: prof,
				SeenIDs:      seen,
			})
			if err != nil {
				return err
			}
			pinnedCount := 0
			pinnedAllLeading := true
			sawNonPinned := false
			for _, w := range p1.Works {
				redactor.addIDs(w.ID, w.Author.SecUID, w.Author.UID, w.Author.ShortID, w.Author.RoomID, w.Author.WebRID)
				redactor.addHandles(w.Author.UniqueID)
				if w.IsTop {
					pinnedCount++
					if sawNonPinned {
						pinnedAllLeading = false
					}
				} else {
					sawNonPinned = true
				}
			}

			uFact := map[string]any{
				"userIndex":        idx,
				"isPrivate":        prof.IsPrivate,
				"liveStatus":       prof.LiveStatus,
				"hasShortId":       prof.ShortID != "" && prof.ShortID != "0",
				"page1Count":       len(p1.Works),
				"page1PinnedCount": pinnedCount,
				"page1HasMore":     p1.HasMore,
				"uncertainEmpty":   p1.UncertainEmpty,
			}

			if idx == 0 {
				page2Fetched := false
				page2Count := 0
				duplicateAcrossPages := false
				page1IDs := make(map[string]bool, len(p1.Works))
				for _, w := range p1.Works {
					page1IDs[w.ID] = true
				}
				if p1.HasMore && p1.NextCursor != "" && p1.NextCursor != "0" {
					page2Fetched = true
					p2, p2Err := dyClient.ListUserPosts(ctx, douyin.UserPostsRequest{
						SecUID:       u.SecUID,
						MaxCursor:    p1.NextCursor,
						KnownProfile: prof,
						SeenIDs:      seen,
					})
					if p2Err != nil {
						return p2Err
					}
					page2Count = len(p2.Works)
					for _, w := range p2.Works {
						redactor.addIDs(w.ID, w.Author.SecUID, w.Author.UID, w.Author.ShortID)
						redactor.addHandles(w.Author.UniqueID)
						if page1IDs[w.ID] {
							duplicateAcrossPages = true
						}
					}
					uFact["page2Count"] = page2Count
					uFact["page2HasMore"] = p2.HasMore
					uFact["cursorStalled"] = p2.CursorStalled
				}
				v03Summary = map[string]any{
					"page1Items":           len(p1.Works),
					"pinnedCount":          pinnedCount,
					"pinnedLeading":        pinnedAllLeading,
					"page2Fetched":         page2Fetched,
					"page2Items":           page2Count,
					"duplicateAcrossPages": duplicateAcrossPages,
				}
			}
			userSummaries = append(userSummaries, uFact)
		}

		f["users"] = userSummaries
		if v03Summary != nil {
			rc.Verifications["V-03"] = v03Summary
		}
		return nil
	})

	// Step 3: V-02 & V-04/V-05 metadata — aweme/detail for each work in targets.Works (<= 4).
	type fetchedWork struct {
		target act010Work
		detail *douyin.WorkDetail
	}
	var fetchedWorks []fetchedWork
	runStep("v02_v04_works_detail", func(f map[string]any) error {
		var items []map[string]any
		for idx, w := range tg.Works {
			detail, err := dyClient.GetWorkDetail(ctx, w.AwemeID, douyin.StreamSelectOptions{PreferredCodec: "avc"})
			if err != nil {
				return err
			}
			redactor.addIDs(detail.ID, detail.Author.SecUID, detail.Author.UID, detail.Author.ShortID, detail.Author.RoomID, detail.Author.WebRID)
			redactor.addHandles(detail.Author.UniqueID)
			if detail.Music != nil {
				redactor.addIDs(detail.Music.ID)
			}
			if detail.Kind != w.Kind {
				return fmt.Errorf("work[%d] kind=%q, want %q", idx, detail.Kind, w.Kind)
			}
			if detail.Stats.PlayCount != nil {
				return fmt.Errorf("work[%d] unexpectedly exposed placeholder playCount", idx)
			}

			pageKinds := make(map[string]int)
			for _, pg := range detail.Pages {
				pageKinds[pg.Kind]++
			}
			wFact := map[string]any{
				"workIndex":     idx,
				"declaredKind":  w.Kind,
				"parsedKind":    detail.Kind,
				"expect":        w.Expect,
				"awemeType":     detail.AwemeType,
				"isTop":         detail.IsTop,
				"durationSec":   detail.DurationSec,
				"pageCount":     len(detail.Pages),
				"uncacheable":   detail.Uncacheable,
				"hasMusic":      detail.Music != nil && len(detail.Music.PlayURLs) > 0,
				"playCountZero": false,
			}
			if len(pageKinds) > 0 {
				wFact["pageKinds"] = pageKinds
			}
			if detail.Music != nil {
				wFact["isOriginalSound"] = detail.Music.IsOriginalSound
			}
			if detail.Video != nil {
				wFact["videoQuality"] = detail.Video.QualityLabel
				wFact["videoCodec"] = detail.Video.Codec
				wFact["videoWidth"] = detail.Video.Width
				wFact["videoHeight"] = detail.Video.Height
				wFact["videoDataSize"] = detail.Video.DataSize
			}
			items = append(items, wFact)
			fetchedWorks = append(fetchedWorks, fetchedWork{target: w, detail: detail})
		}
		f["works"] = items
		return nil
	})

	// Step 4: V-04 & V-05 — CDN Range video download (>24 MiB partial probe) + note/mixed assets without Cookie.
	runStep("v04_v05_media_cdn", func(f map[string]any) error {
		if len(fetchedWorks) == 0 {
			return errSkip
		}
		ck.vodMediaActive.Store(true)
		defer ck.vodMediaActive.Store(false)

		vodTransport := dyClient.VODTransport()

		// 4a. Pick 1 video work for V-05 (prefer "recent" or smaller DataSize).
		var chosenVideo *fetchedWork
		for i := range fetchedWorks {
			fw := &fetchedWorks[i]
			if fw.detail.Kind != douyin.WorkKindVideo || fw.detail.Video == nil || len(fw.detail.Video.URLs) == 0 {
				continue
			}
			if chosenVideo == nil {
				chosenVideo = fw
				continue
			}
			if hasTag(fw.target.Expect, "recent") && !hasTag(chosenVideo.target.Expect, "recent") {
				chosenVideo = fw
			} else if fw.detail.Video.DataSize > 0 && chosenVideo.detail.Video.DataSize > 0 && fw.detail.Video.DataSize < chosenVideo.detail.Video.DataSize {
				chosenVideo = fw
			}
		}

		if chosenVideo != nil {
			vSummary, err := probeOrDownloadMP4(ctx, mediaHTTP, vodTransport, workDir, "v05-video.mp4",
				chosenVideo.detail.Video.URLs[0], chosenVideo.detail.Video.DataSize, float64(chosenVideo.detail.DurationSec), true, ck)
			if err != nil {
				return err
			}
			vSummary["selectedCodec"] = chosenVideo.detail.Video.Codec
			vSummary["selectedQuality"] = chosenVideo.detail.Video.QualityLabel
			f["v05Video"] = vSummary
			rc.Verifications["V-05"] = vSummary
		}

		// 4b. Inspect note & mixed-note assets for V-04 within remaining CDN budget.
		var imageProbes []map[string]any
		var livePhotoSummary map[string]any
		var mixedVideoSummary map[string]any
		var musicSummary map[string]any

		for i := range fetchedWorks {
			fw := &fetchedWorks[i]
			if fw.detail.Kind != douyin.WorkKindNote {
				continue
			}
			probedStaticOnThisNote := false
			for _, pg := range fw.detail.Pages {
				if ck.stopReason() != "" {
					return fmt.Errorf("stopped: %s", ck.stopReason())
				}
				switch pg.Kind {
				case douyin.NotePageImage, douyin.NotePageUnknown:
					if !probedStaticOnThisNote && len(pg.ImageURLs) > 0 && len(imageProbes) < 2 && ck.canAffordVOD(2<<20, 1) {
						imgFact, err := fetchAssetSample(ctx, mediaHTTP, vodTransport, pg.ImageURLs[0], 12<<20)
						if err != nil {
							return err
						}
						imgFact["pageKind"] = pg.Kind
						imageProbes = append(imageProbes, imgFact)
						probedStaticOnThisNote = true
					}
				case douyin.NotePageLivePhoto:
					if !probedStaticOnThisNote && len(pg.ImageURLs) > 0 && len(imageProbes) < 2 && ck.canAffordVOD(2<<20, 1) {
						imgFact, err := fetchAssetSample(ctx, mediaHTTP, vodTransport, pg.ImageURLs[0], 12<<20)
						if err != nil {
							return err
						}
						imgFact["pageKind"] = pg.Kind
						imageProbes = append(imageProbes, imgFact)
						probedStaticOnThisNote = true
					}
					if livePhotoSummary == nil && pg.VideoStream != nil && len(pg.VideoStream.URLs) > 0 && ck.canAffordVOD(9<<20, 2) {
						lpFact, err := probeOrDownloadMP4(ctx, mediaHTTP, vodTransport, workDir, "v04-livephoto.mp4",
							pg.VideoStream.URLs[0], pg.VideoStream.DataSize, float64(pg.VideoStream.DurationSec), false, ck)
						if err != nil {
							return err
						}
						livePhotoSummary = lpFact
					}
				case douyin.NotePageVideo:
					if mixedVideoSummary == nil && pg.VideoStream != nil && len(pg.VideoStream.URLs) > 0 && ck.canAffordVOD(9<<20, 2) {
						mvFact, err := probeOrDownloadMP4(ctx, mediaHTTP, vodTransport, workDir, "v04-note-video.mp4",
							pg.VideoStream.URLs[0], pg.VideoStream.DataSize, float64(pg.VideoStream.DurationSec), false, ck)
						if err != nil {
							return err
						}
						mvFact["selectedCodec"] = pg.VideoStream.Codec
						mvFact["selectedQuality"] = pg.VideoStream.QualityLabel
						mixedVideoSummary = mvFact
					}
				}
			}
			if musicSummary == nil && fw.detail.Music != nil && len(fw.detail.Music.PlayURLs) > 0 && ck.canAffordVOD(4<<20, 1) {
				mFact, err := fetchAssetSample(ctx, mediaHTTP, vodTransport, fw.detail.Music.PlayURLs[0], 16<<20)
				if err != nil {
					return err
				}
				mFact["isOriginalSound"] = fw.detail.Music.IsOriginalSound
				musicSummary = mFact
			}
		}

		v04 := map[string]any{
			"images": imageProbes,
		}
		if livePhotoSummary != nil {
			v04["livePhotoVideo"] = livePhotoSummary
		}
		if mixedVideoSummary != nil {
			v04["mixedNoteVideoPage"] = mixedVideoSummary
		}
		if musicSummary != nil {
			v04["music"] = musicSummary
		}
		f["v04NoteAssets"] = v04
		rc.Verifications["V-04"] = v04
		return nil
	})

	// Step 5: V-06 (reflow/info offline & online) + V-08 (two liveRooms forms comparison).
	var activeRoom *douyin.LiveRoomDetail
	runStep("v06_v08_live_rooms_and_reflow", func(f map[string]any) error {
		type formOutcome struct {
			form     string
			queried  string
			accepted bool
			errKind  string
			detail   *douyin.LiveRoomDetail
		}
		var outcomes []formOutcome
		formsTested := make([]string, 0, len(tg.LiveRooms))
		acceptedByForm := make(map[string]bool, len(tg.LiveRooms))
		respMatchesQueried := make(map[string]bool, len(tg.LiveRooms))

		for idx, lr := range tg.LiveRooms {
			formLabel := lr.Form
			if formLabel == "" {
				formLabel = fmt.Sprintf("form_%d", idx)
			}
			formsTested = append(formsTested, formLabel)

			detail, err := dyClient.EnterLiveRoom(ctx, lr.WebRID, "")
			if err != nil {
				if ck.stopReason() != "" || site.IsRoundStop(err) || errors.Is(err, sitecheck.ErrQuotaExceeded) {
					return err
				}
				acceptedByForm[formLabel] = false
				outcomes = append(outcomes, formOutcome{
					form:     formLabel,
					queried:  lr.WebRID,
					accepted: false,
					errKind:  site.KindOf(err),
				})
				continue
			}
			redactor.addIDs(detail.OwnerSecUID, detail.OwnerUID, detail.OwnerShortID, detail.QueriedWebRID, detail.ResponseWebRID, detail.RoomID)
			acceptedByForm[formLabel] = true
			respMatchesQueried[formLabel] = detail.ResponseWebRID == lr.WebRID
			outcomes = append(outcomes, formOutcome{
				form:     formLabel,
				queried:  lr.WebRID,
				accepted: true,
				detail:   detail,
			})
			if activeRoom == nil || (activeRoom.RoomStatus != 2 && detail.RoomStatus == 2) {
				activeRoom = detail
			}
		}

		allAccepted := len(outcomes) > 0
		sameOwnerSecUID := true
		sameRoomID := true
		var firstAccepted *douyin.LiveRoomDetail
		var shortIDFormMatchesOwnerShortID *bool
		var shortIDMatchesProfileOther *bool

		for i := range outcomes {
			o := &outcomes[i]
			if !o.accepted {
				allAccepted = false
				sameOwnerSecUID = false
				sameRoomID = false
				continue
			}
			if firstAccepted == nil {
				firstAccepted = o.detail
			} else {
				if o.detail.OwnerSecUID == "" || o.detail.OwnerSecUID != firstAccepted.OwnerSecUID {
					sameOwnerSecUID = false
				}
				if o.detail.RoomID != firstAccepted.RoomID {
					sameRoomID = false
				}
			}
			if o.form == "short_id" {
				matched := o.detail.OwnerShortID != "" && o.detail.OwnerShortID == o.queried
				shortIDFormMatchesOwnerShortID = &matched
				if prof, ok := profilesBySec[o.detail.OwnerSecUID]; ok {
					profMatch := prof.ShortID != "" && prof.ShortID == o.queried
					shortIDMatchesProfileOther = &profMatch
				}
			}
		}

		roomStatus := 0
		audioOnly := false
		if activeRoom != nil {
			roomStatus = activeRoom.RoomStatus
			audioOnly = activeRoom.AudioOnly
		}

		v08 := map[string]any{
			"formsTested":                  formsTested,
			"acceptedByForm":               acceptedByForm,
			"bothAccepted":                 allAccepted && len(outcomes) >= 2,
			"sameOwnerSecUid":              allAccepted && len(outcomes) >= 2 && sameOwnerSecUID,
			"sameRoomId":                   allAccepted && len(outcomes) >= 2 && sameRoomID,
			"responseWebRidMatchesQueried": respMatchesQueried,
			"roomStatus":                   roomStatus,
			"audioOnly":                    audioOnly,
		}
		if shortIDFormMatchesOwnerShortID != nil {
			v08["shortIdFormMatchesOwnerShortId"] = *shortIDFormMatchesOwnerShortID
		}
		if shortIDMatchesProfileOther != nil {
			v08["shortIdMatchesProfileOther"] = *shortIDMatchesProfileOther
		}
		f["v08"] = v08
		rc.Verifications["V-08"] = v08

		// V-06: Offline reflow/info (room_id=2) check + optional online reflow/info check.
		v06TargetSec := offlineUserSecUID
		if activeRoom != nil && activeRoom.RoomStatus != 2 && activeRoom.OwnerSecUID != "" {
			v06TargetSec = activeRoom.OwnerSecUID
		}
		if v06TargetSec == "" && len(tg.Users) > 0 {
			v06TargetSec = tg.Users[0].SecUID
		}

		v06 := map[string]any{}
		if v06TargetSec != "" {
			offDetail, offErr := dyClient.ReflowRoomInfo(ctx, "2", v06TargetSec)
			if offErr != nil {
				if ck.stopReason() != "" || site.IsRoundStop(offErr) || errors.Is(offErr, sitecheck.ErrQuotaExceeded) {
					return offErr
				}
				v06["offlineRoomId2Accepted"] = false
				v06["offlineRoomId2YieldsWebRid"] = false
				v06["offlineRoomId2ErrorKind"] = site.KindOf(offErr)
			} else {
				redactor.addIDs(offDetail.OwnerSecUID, offDetail.OwnerUID, offDetail.OwnerShortID, offDetail.ResponseWebRID, offDetail.RoomID)
				v06["offlineRoomId2Accepted"] = true
				v06["offlineRoomId2YieldsWebRid"] = offDetail.ResponseWebRID != ""
			}
		}

		if activeRoom != nil && activeRoom.RoomStatus == 2 && activeRoom.RoomID != "" {
			onDetail, onErr := dyClient.ReflowRoomInfo(ctx, activeRoom.RoomID, activeRoom.OwnerSecUID)
			if onErr != nil {
				if ck.stopReason() != "" || site.IsRoundStop(onErr) || errors.Is(onErr, sitecheck.ErrQuotaExceeded) {
					return onErr
				}
				v06["onlineReflowAccepted"] = false
				v06["onlineReflowErrorKind"] = site.KindOf(onErr)
			} else {
				redactor.addIDs(onDetail.OwnerSecUID, onDetail.OwnerUID, onDetail.OwnerShortID, onDetail.ResponseWebRID, onDetail.RoomID)
				v06["onlineReflowAccepted"] = true
				v06["onlineReflowYieldsWebRid"] = onDetail.ResponseWebRID != ""
				v06["onlineReflowOwnerMatches"] = onDetail.OwnerSecUID == activeRoom.OwnerSecUID
			}
		}

		f["v06"] = v06
		rc.Verifications["V-06"] = v06
		return nil
	})

	// Step 6: V-07 — Live stream inspection (HLS .m3u8 extension + HTTP-FLV wire header & <=25s recording).
	runStep("v07_live_stream_and_record", func(f map[string]any) error {
		if activeRoom == nil || activeRoom.RoomStatus != 2 {
			f["status"] = "not-run"
			f["reason"] = "live room offline"
			rc.Verifications["V-07"] = map[string]any{
				"status": "not-run",
				"reason": "live room offline",
			}
			return errSkip
		}

		streamCaps := inspectRoomStreamCapabilities(activeRoom, cfg.AllowLocalhost)
		f["streamCapabilities"] = streamCaps

		liveSpec := dyClient.LiveTransport()
		var hlsSummary map[string]any
		if hlsURL, _ := streamCaps["sampleHlsUrl"].(string); hlsURL != "" {
			delete(streamCaps, "sampleHlsUrl")
			req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, hlsURL, nil)
			if rErr == nil {
				liveSpec.ApplyHeaders(req)
				resp, hErr := mediaHTTP.Do(req)
				if hErr != nil {
					if ck.stopReason() != "" || errors.Is(hErr, sitecheck.ErrQuotaExceeded) {
						return hErr
					}
				} else {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					ck.mu.Lock()
					if len(ck.playlists) > 0 {
						hlsSummary = ck.playlists[len(ck.playlists)-1]
					}
					ck.mu.Unlock()
				}
			}
		} else {
			delete(streamCaps, "sampleHlsUrl")
		}

		plan, err := douyin.SelectFLVStreamFromRoom(activeRoom, nil, liveSpec, cfg.AllowLocalhost)
		if err != nil {
			return err
		}

		ck.liveStreamActive.Store(true)
		defer ck.liveStreamActive.Store(false)

		recSummary, err := recordShortFLV(ctx, liveHTTP, plan, workDir, recordFor, ck)
		if err != nil {
			return err
		}
		if hlsSummary != nil {
			recSummary["hlsPlaylist"] = hlsSummary
		}
		recSummary["originHasFLV"] = streamCaps["originHasFLV"]
		recSummary["originHasHLS"] = streamCaps["originHasHLS"]
		recSummary["status"] = "completed"
		for k, v := range recSummary {
			f[k] = v
		}
		rc.Verifications["V-07"] = recSummary
		return nil
	})

	// Step V-02 rollup across all executed signed & Argus-protected endpoints.
	if ck.stopReason() == "" {
		rc.Verifications["V-02"] = map[string]any{
			"passedAllSignedEndpoints": true,
			"cookieInitialHadMSToken":  initialHadMSToken,
			"cookieInitialHadUIFID":    initialHadUIFID,
			"cookieInitialHadTTWID":    initialHadTTWID,
			"apiCallsByCategory":       gate.Usage().Categories,
		}
	} else {
		rc.Verifications["V-02"] = map[string]any{
			"passedAllSignedEndpoints": false,
			"stoppedBy":                ck.stopReason(),
		}
	}

	rc.TempMediaDeleted = cleanupWorkDir()
	rc.APIUsage = gate.Usage()
	rc.VODMedia = mediaUsage{
		Requests: int(ck.vod.requests.Load()),
		Bytes:    ck.vod.bytes.Load(),
	}
	rc.LiveMedia = mediaUsage{
		Requests: int(ck.live.requests.Load()),
		Bytes:    ck.live.bytes.Load(),
	}
	rc.StoppedBy = ck.stopReason()
	rc.Redirects = ck.redirectShapes()
	rc.Passed = rc.StoppedBy == "" && rc.TempMediaDeleted
	for _, s := range rc.Steps {
		if !s.OK {
			rc.Passed = false
		}
	}
	rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)

	// Verify receipt redaction BEFORE setting SecretScanClean and writing disk file.
	rc.SecretScanClean = true
	rawJSON, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return rc, err
	}
	if err := redactor.verifyJSONClean(string(rawJSON), &rc); err != nil {
		rc.SecretScanClean = false
		rc.Passed = false
		return rc, err
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return rc, err
	}
	if err := storage.WriteFileAtomic(outPath, rawJSON, 0o644); err != nil {
		return rc, err
	}
	if !rc.Passed {
		if rc.StoppedBy != "" {
			return rc, fmt.Errorf("act010 stopped by %s", rc.StoppedBy)
		}
		return rc, errors.New("act010 completed with step failures")
	}
	return rc, nil
}

func (r *act010Redactor) addCookieValuesFromStore(store *credential.Store) {
	if store == nil {
		return
	}
	for _, k := range credential.AllowedDouyinCookieKeys {
		v := store.CookieValue(k)
		if isSecretDouyinCookie(k, v) {
			r.mu.Lock()
			r.cookies[v] = true
			r.mu.Unlock()
		}
	}
}

func (c *act010Checker) canAffordVOD(minBytes int64, minReqs int) bool {
	if int(c.vod.requests.Load())+minReqs > c.vod.maxRequests {
		return false
	}
	if c.vod.bytes.Load()+minBytes > c.vod.maxBytes {
		return false
	}
	return true
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), want) {
			return true
		}
	}
	return false
}

func loadAndValidateAct010Targets(root string) (act010Targets, error) {
	var tg act010Targets
	p := filepath.Join(root, ".local", "site-check", "targets-douyin.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return tg, errors.New("targets: .local/site-check/targets-douyin.json is unreadable")
	}
	if err := json.Unmarshal(raw, &tg); err != nil {
		return tg, errors.New("targets: invalid JSON in targets-douyin.json")
	}
	if len(tg.Users) == 0 || len(tg.Users) > 2 {
		return tg, fmt.Errorf("targets: users count=%d, want 1..2", len(tg.Users))
	}
	if len(tg.Works) == 0 || len(tg.Works) > 4 {
		return tg, fmt.Errorf("targets: works count=%d, want 1..4", len(tg.Works))
	}
	if len(tg.LiveRooms) == 0 || len(tg.LiveRooms) > 4 {
		return tg, fmt.Errorf("targets: liveRooms count=%d, want 1..4", len(tg.LiveRooms))
	}

	checkNoShortLink := func(rawURL string) error {
		if strings.TrimSpace(rawURL) == "" {
			return nil
		}
		u, uErr := url.Parse(strings.TrimSpace(rawURL))
		if uErr != nil {
			return errors.New("targets: invalid URL in targets-douyin.json")
		}
		if isShortLinkHost(u.Hostname()) {
			return fmt.Errorf("%w: short-link redirect hops are 0 under ACT-010", sitecheck.ErrQuotaExceeded)
		}
		return nil
	}

	for i, u := range tg.Users {
		if !reAct010SecUID.MatchString(strings.TrimSpace(u.SecUID)) {
			return tg, fmt.Errorf("targets: users[%d].secUid is invalid", i)
		}
		if err := checkNoShortLink(u.URL); err != nil {
			return tg, err
		}
	}
	for i, w := range tg.Works {
		if !reAct010WorkID.MatchString(strings.TrimSpace(w.AwemeID)) {
			return tg, fmt.Errorf("targets: works[%d].awemeId is invalid", i)
		}
		if w.Kind != "video" && w.Kind != "note" {
			return tg, fmt.Errorf("targets: works[%d].kind=%q, want video or note", i, w.Kind)
		}
		if err := checkNoShortLink(w.URL); err != nil {
			return tg, err
		}
	}
	for i, lr := range tg.LiveRooms {
		if !reAct010RoomDigit.MatchString(strings.TrimSpace(lr.WebRID)) {
			return tg, fmt.Errorf("targets: liveRooms[%d].webRid is invalid", i)
		}
		if err := checkNoShortLink(lr.URL); err != nil {
			return tg, err
		}
	}
	return tg, nil
}

func readAndValidateDouyinCookie(root string) (string, map[string]string, error) {
	f, err := os.Open(filepath.Join(root, ".env.local"))
	if err != nil {
		return "", nil, errors.New("cookie: .env.local unreadable")
	}
	defer f.Close()

	var cookiePath string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "TRACE_TEST_DOUYIN_COOKIE_FILE="); ok {
			cookiePath = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	if cookiePath == "" {
		return "", nil, errors.New("cookie: TRACE_TEST_DOUYIN_COOKIE_FILE is not set in .env.local")
	}
	if !filepath.IsAbs(cookiePath) {
		cookiePath = filepath.Join(root, cookiePath)
	}
	raw, err := os.ReadFile(cookiePath)
	if err != nil {
		return "", nil, errors.New("cookie: TRACE_TEST_DOUYIN_COOKIE_FILE unreadable")
	}
	cookieStr := strings.TrimSpace(string(raw))
	cleaned, err := credential.ParseDouyinCookieInput(cookieStr)
	if err != nil {
		return "", nil, errors.New("cookie: Douyin test Cookie missing sessionid/sessionid_ss or exceeds 8 KiB")
	}
	return cookieStr, cleaned, nil
}

// probeOrDownloadMP4 downloads an MP4 in 8 MiB closed Range chunks when <= 24 MiB and validates
// it via EnsureSingleMP4Context; when > 24 MiB, it fetches only the first 8 MiB and last 1 MiB
// via Range to inspect top-level ISO BMFF container boxes (ftyp/moov/mdat).
func probeOrDownloadMP4(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	workDir string,
	fileName string,
	mediaURL string,
	hintSize int64,
	expectedDurationSec float64,
	strictDuration bool,
	ck *act010Checker,
) (map[string]any, error) {
	if err := spec.ValidateURL(mediaURL); err != nil {
		return nil, err
	}

	firstEnd := act010RangeChunkBytes - 1
	if hintSize > 0 && hintSize < act010RangeChunkBytes {
		firstEnd = hintSize - 1
	}

	headBytes, status1, totalSize, contentType, err := fetchRangeChunk(ctx, client, spec, mediaURL, 0, firstEnd)
	if err != nil {
		return nil, err
	}
	if ck.stopReason() != "" {
		return nil, fmt.Errorf("stopped: %s", ck.stopReason())
	}
	if totalSize <= 0 && hintSize > 0 {
		totalSize = hintSize
	}
	if totalSize <= 0 && status1 == http.StatusOK {
		totalSize = int64(len(headBytes))
	}

	// Large file (> 24 MiB): fetch ONLY head 8 MiB + tail 1 MiB to inspect MP4 container boxes.
	if totalSize > act010LargeFileCutoff {
		tailStart := totalSize - act010TailProbeBytes
		if tailStart < int64(len(headBytes)) {
			tailStart = int64(len(headBytes))
		}
		var tailBytes []byte
		statuses := []int{status1}
		if tailStart < totalSize {
			var status2 int
			tailBytes, status2, _, _, err = fetchRangeChunk(ctx, client, spec, mediaURL, tailStart, totalSize-1)
			if err != nil {
				return nil, err
			}
			statuses = append(statuses, status2)
		}
		boxSummary, bErr := inspectPartialMP4HeadTail(headBytes, tailStart, tailBytes, totalSize)
		if bErr != nil {
			return nil, bErr
		}
		boxSummary["largeFilePartialProbe"] = true
		boxSummary["totalBytes"] = totalSize
		boxSummary["downloadedBytes"] = int64(len(headBytes) + len(tailBytes))
		boxSummary["rangeRequests"] = len(statuses)
		boxSummary["rangeStatuses"] = statuses
		boxSummary["contentType"] = contentType
		return boxSummary, nil
	}

	// Normal file (<= 24 MiB): assemble full MP4 via 8 MiB closed Range chunks and run EnsureSingleMP4Context.
	outPath := filepath.Join(workDir, fileName)
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(headBytes); err != nil {
		_ = f.Close()
		return nil, err
	}
	written := int64(len(headBytes))
	rangeReqs := 1
	http206Count := 0
	if status1 == http.StatusPartialContent {
		http206Count++
	}

	for status1 == http.StatusPartialContent && totalSize > 0 && written < totalSize {
		nextEnd := written + act010RangeChunkBytes - 1
		if nextEnd >= totalSize {
			nextEnd = totalSize - 1
		}
		chunk, st, _, _, cErr := fetchRangeChunk(ctx, client, spec, mediaURL, written, nextEnd)
		if cErr != nil {
			_ = f.Close()
			return nil, cErr
		}
		if len(chunk) == 0 {
			_ = f.Close()
			return nil, errors.New("unexpected 0-byte media chunk")
		}
		if _, wErr := f.Write(chunk); wErr != nil {
			_ = f.Close()
			return nil, wErr
		}
		written += int64(len(chunk))
		rangeReqs++
		if st == http.StatusPartialContent {
			http206Count++
		}
		if st == http.StatusOK {
			break
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	checkDur := 0.0
	if strictDuration {
		checkDur = expectedDurationSec
	}
	muxSum, err := download.EnsureSingleMP4Context(ctx, outPath, checkDur)
	if err != nil {
		return nil, err
	}
	durDiff := 0.0
	if expectedDurationSec > 0 {
		durDiff = math.Abs(muxSum.DurationSec - expectedDurationSec)
	}
	return map[string]any{
		"largeFilePartialProbe": false,
		"bytes":                 muxSum.FileBytes,
		"rangeRequests":         rangeReqs,
		"http206Count":          http206Count,
		"contentType":           contentType,
		"codecs":                muxSum.Codecs,
		"width":                 muxSum.Width,
		"height":                muxSum.Height,
		"moovBeforeMdat":        muxSum.MoovBeforeMdat,
		"hasElst":               muxSum.HasElst,
		"declaredDurationSec":   expectedDurationSec,
		"durationSec":           math.Round(muxSum.DurationSec*1000) / 1000,
		"durationDiffSec":       math.Round(durDiff*1000) / 1000,
	}, nil
}

func fetchRangeChunk(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	mediaURL string,
	startByte int64,
	endByte int64,
) ([]byte, int, int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, 0, 0, "", err
	}
	spec.ApplyHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startByte, endByte))

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, 0, resp.Header.Get("Content-Type"), fmt.Errorf("media HTTP %d", resp.StatusCode)
	}
	maxRead := (endByte - startByte + 1) + (1 << 20)
	if resp.StatusCode == http.StatusOK && maxRead < act010LargeFileCutoff+1024 {
		maxRead = act010LargeFileCutoff + 1024
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRead))
	if err != nil {
		return nil, resp.StatusCode, 0, resp.Header.Get("Content-Type"), err
	}

	totalSize := parseContentRangeTotal(resp.Header.Get("Content-Range"))
	if totalSize <= 0 && resp.ContentLength > 0 && resp.StatusCode == http.StatusOK {
		totalSize = resp.ContentLength
	}
	return data, resp.StatusCode, totalSize, resp.Header.Get("Content-Type"), nil
}

func parseContentRangeTotal(hdr string) int64 {
	// Format: "bytes 0-8388607/31457280"
	_, slashAfter, ok := strings.Cut(strings.TrimSpace(hdr), "/")
	if !ok {
		return 0
	}
	slashAfter = strings.TrimSpace(slashAfter)
	if slashAfter == "" || slashAfter == "*" {
		return 0
	}
	n, err := strconv.ParseInt(slashAfter, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func inspectPartialMP4HeadTail(head []byte, tailOffset int64, tail []byte, totalSize int64) (map[string]any, error) {
	if len(head) < 16 {
		return nil, errors.New("partial MP4 head shorter than 16 bytes")
	}

	readSlice := func(offset int64, n int) ([]byte, bool) {
		if offset < 0 || offset+int64(n) > totalSize {
			return nil, false
		}
		if offset+int64(n) <= int64(len(head)) {
			return head[offset : offset+int64(n)], true
		}
		if len(tail) > 0 && offset >= tailOffset && (offset-tailOffset)+int64(n) <= int64(len(tail)) {
			rel := offset - tailOffset
			return tail[rel : rel+int64(n)], true
		}
		return nil, false
	}

	var boxes []string
	hasFtyp, hasMoov, hasMdat := false, false, false
	moovIdx, mdatIdx := -1, -1
	var offset int64
	for stepCount := 0; offset < totalSize && stepCount < 64; stepCount++ {
		hdr, ok := readSlice(offset, 8)
		if !ok {
			break
		}
		sz32 := binary.BigEndian.Uint32(hdr[0:4])
		boxType := string(hdr[4:8])
		var boxSize int64
		switch sz32 {
		case 0:
			boxSize = totalSize - offset
		case 1:
			ext, okExt := readSlice(offset+8, 8)
			if !okExt {
				boxSize = 0
			} else {
				u64 := binary.BigEndian.Uint64(ext)
				if u64 > uint64(totalSize-offset) || u64 < 16 {
					return nil, fmt.Errorf("invalid 64-bit MP4 box size in %q", boxType)
				}
				boxSize = int64(u64)
			}
		default:
			if sz32 < 8 {
				return nil, fmt.Errorf("invalid MP4 box size %d in %q", sz32, boxType)
			}
			boxSize = int64(sz32)
		}
		if boxSize <= 0 || offset+boxSize > totalSize {
			break
		}

		boxes = append(boxes, boxType)
		switch boxType {
		case "ftyp":
			hasFtyp = true
		case "moov":
			hasMoov = true
			if moovIdx < 0 {
				moovIdx = len(boxes) - 1
			}
		case "mdat":
			hasMdat = true
			if mdatIdx < 0 {
				mdatIdx = len(boxes) - 1
			}
		}
		offset += boxSize
	}

	if !hasFtyp || (!hasMoov && !hasMdat) {
		return nil, fmt.Errorf("partial MP4 missing expected ISO BMFF boxes (got %v)", boxes)
	}

	return map[string]any{
		"boxes":          boxes,
		"hasFtyp":        hasFtyp,
		"hasMoov":        hasMoov,
		"hasMdat":        hasMdat,
		"moovBeforeMdat": hasMoov && hasMdat && moovIdx < mdatIdx,
	}, nil
}

func fetchAssetSample(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	assetURL string,
	limitBytes int64,
) (map[string]any, error) {
	if err := spec.ValidateURL(assetURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	spec.ApplyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("asset HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limitBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limitBytes {
		return nil, fmt.Errorf("asset exceeded %d byte cap", limitBytes)
	}
	if len(raw) == 0 {
		return nil, errors.New("asset returned 0 bytes")
	}

	return map[string]any{
		"status":      resp.StatusCode,
		"contentType": resp.Header.Get("Content-Type"),
		"magicFormat": sniffAssetMagic(raw),
		"bytes":       len(raw),
	}, nil
}

func sniffAssetMagic(raw []byte) string {
	switch {
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "jpeg"
	case len(raw) >= 8 && string(raw[0:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(raw) >= 12 && string(raw[4:8]) == "ftyp":
		brand := strings.TrimSpace(string(raw[8:12]))
		switch brand {
		case "heic", "heix", "mif1", "msf1":
			return "heic"
		case "M4A", "mp42", "isom", "iso2":
			return "mp4_or_m4a"
		default:
			return "bmff:" + brand
		}
	case len(raw) >= 3 && string(raw[0:3]) == "ID3":
		return "mp3"
	case len(raw) >= 2 && raw[0] == 0xFF && (raw[1]&0xE0) == 0xE0:
		return "aac_or_mp3_adts"
	default:
		return "other"
	}
}

func inspectRoomStreamCapabilities(room *douyin.LiveRoomDetail, allowLocalhost bool) map[string]any {
	out := map[string]any{
		"originHasFLV": false,
		"originHasHLS": false,
	}
	if room == nil || room.StreamURL == nil {
		return out
	}

	var tiers []string
	var sampleHLS string
	if room.StreamURL.LiveCoreSDK != nil && room.StreamURL.LiveCoreSDK.PullData != nil &&
		strings.TrimSpace(room.StreamURL.LiveCoreSDK.PullData.StreamData) != "" {
		var parsed struct {
			Data map[string]map[string]struct {
				FLV string `json:"flv"`
				HLS string `json:"hls"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(room.StreamURL.LiveCoreSDK.PullData.StreamData), &parsed) == nil {
			for tier, lines := range parsed.Data {
				tiers = append(tiers, tier)
				mainLine := lines["main"]
				if strings.EqualFold(tier, "origin") {
					out["originHasFLV"] = strings.TrimSpace(mainLine.FLV) != ""
					out["originHasHLS"] = strings.TrimSpace(mainLine.HLS) != ""
					if sampleHLS == "" && strings.TrimSpace(mainLine.HLS) != "" {
						sampleHLS = strings.TrimSpace(mainLine.HLS)
					}
				}
			}
			sort.Strings(tiers)
		}
	}
	if sampleHLS == "" && strings.TrimSpace(room.StreamURL.HLSPullURL) != "" {
		sampleHLS = strings.TrimSpace(room.StreamURL.HLSPullURL)
	}
	if sampleHLS == "" {
		for _, v := range room.StreamURL.HLSPullURLMap {
			if strings.TrimSpace(v) != "" {
				sampleHLS = strings.TrimSpace(v)
				break
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(sampleHLS), "http://") && !allowLocalhost {
		sampleHLS = "https://" + sampleHLS[len("http://"):]
	}
	if len(tiers) > 0 {
		out["sdkTiers"] = tiers
	}
	if sampleHLS != "" {
		out["sampleHlsUrl"] = sampleHLS
	}
	return out
}

func recordShortFLV(
	parentCtx context.Context,
	client *http.Client,
	plan site.LiveStreamPlan,
	workDir string,
	recordFor time.Duration,
	ck *act010Checker,
) (map[string]any, error) {
	if len(plan.URLs) == 0 {
		return nil, errors.New("no live FLV URL in stream plan")
	}

	maxAttempts := len(plan.URLs)
	if maxAttempts > act010MaxLiveConns {
		maxAttempts = act010MaxLiveConns
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ck.stopReason() != "" {
			return nil, fmt.Errorf("stopped: %s", ck.stopReason())
		}
		streamURL := plan.URLs[attempt]
		if err := plan.Media.ValidateURL(streamURL); err != nil {
			lastErr = err
			continue
		}

		recCtx, cancel := context.WithTimeout(parentCtx, recordFor)
		req, err := http.NewRequestWithContext(recCtx, http.MethodGet, streamURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		plan.Media.ApplyHeaders(req)

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			if ck.stopReason() != "" || errors.Is(err, sitecheck.ErrQuotaExceeded) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("live FLV HTTP %d", resp.StatusCode)
			if ck.stopReason() != "" {
				return nil, lastErr
			}
			continue
		}

		partPath := filepath.Join(workDir, fmt.Sprintf("live-part-%03d.mp4.part", attempt+1))
		partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
		if err != nil {
			_ = resp.Body.Close()
			cancel()
			return nil, err
		}

		var wireBuf bytes.Buffer
		tee := io.TeeReader(resp.Body, &cappedBufferWriter{buf: &wireBuf, max: 256 * 1024})
		remuxer := live.NewFLVRemuxer(tee, live.FLVRemuxOptions{})
		cp, remuxErr := remuxer.WritePart(recCtx, partFile, nil)
		_ = resp.Body.Close()
		_ = partFile.Close()
		timedOut := recCtx.Err() != nil
		cancel()

		if cp.BytesWritten == 0 {
			if remuxErr != nil {
				lastErr = remuxErr
			} else {
				lastErr = errors.New("live FLV produced 0 remuxed bytes")
			}
			continue
		}
		// Normal time-bounded stop after recordFor when at least 1 fragment was written.
		if remuxErr != nil && !timedOut && !errors.Is(remuxErr, live.ErrFLVTruncated) {
			return nil, remuxErr
		}

		finalPath := filepath.Join(workDir, fmt.Sprintf("live-part-%03d.mp4", attempt+1))
		mp4Sum, err := defragmentLivePartFile(parentCtx, partPath, finalPath)
		if err != nil {
			return nil, err
		}

		wireHeader := detectFLVWireVideoFormat(wireBuf.Bytes())
		return map[string]any{
			"selectedQuality":    plan.QualityName,
			"selectedCodec":      plan.Codec,
			"wireVideoTagFormat": wireHeader,
			"remuxFourCC":        string(cp.FourCC),
			"partCount":          1,
			"fragments":          cp.Sequence,
			"partBytes":          cp.BytesWritten,
			"mp4Bytes":           mp4Sum.FileBytes,
			"codecs":             mp4Sum.Codecs,
			"width":              mp4Sum.Width,
			"height":             mp4Sum.Height,
			"moovBeforeMdat":     mp4Sum.MoovBeforeMdat,
			"durationSec":        math.Round(mp4Sum.DurationSec*1000) / 1000,
		}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("live FLV recording failed")
}

type cappedBufferWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *cappedBufferWriter) Write(p []byte) (int, error) {
	rem := w.max - w.buf.Len()
	if rem > 0 {
		if len(p) <= rem {
			_, _ = w.buf.Write(p)
		} else {
			_, _ = w.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

func detectFLVWireVideoFormat(sample []byte) string {
	fr := live.NewFLVReader(bytes.NewReader(sample))
	for i := 0; i < 64; i++ {
		tag, err := fr.NextTag()
		if err != nil {
			break
		}
		if tag.Type != 9 || len(tag.Data) == 0 {
			continue
		}
		b0 := tag.Data[0]
		if b0&0x80 != 0 {
			if len(tag.Data) >= 5 {
				fourCC := string(tag.Data[1:5])
				if fourCC == "hvc1" || fourCC == "hev1" {
					return "hevc-" + fourCC
				}
				return "exflv-" + fourCC
			}
			return "exflv"
		}
		switch b0 & 0x0f {
		case 7:
			return "avc1"
		case 12:
			return "hevc-cid12"
		default:
			return fmt.Sprintf("codec-%d", b0&0x0f)
		}
	}
	return "unknown"
}

func defragmentLivePartFile(ctx context.Context, partPath, outPath string) (download.MuxTrackSummary, error) {
	inFile, err := os.Open(partPath)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	defer inFile.Close()

	parsed, err := mp4.DecodeFile(inFile, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	if err := mp4.Defragment(parsed, inFile, outFile); err != nil {
		_ = outFile.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := outFile.Sync(); err != nil {
		_ = outFile.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := outFile.Close(); err != nil {
		return download.MuxTrackSummary{}, err
	}

	summary, err := download.InspectMP4FileContext(ctx, outPath)
	if err != nil {
		return summary, err
	}
	hasVideo := false
	for _, h := range summary.Handlers {
		if h == "vide" {
			hasVideo = true
		}
	}
	if !hasVideo || !summary.MoovBeforeMdat || summary.DurationSec <= 0 {
		return summary, errors.New("finalized live MP4 failed track, moov, or duration check")
	}
	return summary, nil
}
