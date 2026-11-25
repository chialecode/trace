package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/sitecheck"
)

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

func addAct010TargetsToRedactor(r *act010Redactor, tg act010Targets) {
	for _, u := range tg.Users {
		r.addIDs(u.SecUID)
		r.addURLs(u.URL)
	}
	for _, w := range tg.Works {
		r.addIDs(w.AwemeID)
		r.addURLs(w.URL)
	}
	for _, lr := range tg.LiveRooms {
		r.addIDs(lr.WebRID)
		r.addURLs(lr.URL)
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
	return true
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

func (r *act010Redactor) addCookieValuesFromStore(store *credential.Store) {
	if store == nil {
		return
	}
	for k, v := range store.Cookies() {
		if isSecretDouyinCookie(k, v) {
			r.mu.Lock()
			r.cookies[v] = true
			r.mu.Unlock()
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
	return redactURLs(out)
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
	apiLimits          map[string]int
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
	summary := parseHLSPlaylistSummary(status, contentType, raw, false)
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
			if err := checkAPICategoryAllowed(c.apiLimits, cat); err != nil {
				return nil, err
			}
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
			} else {
				if err := checkAPICategoryAllowed(c.apiLimits, cat); err != nil {
					return nil, err
				}
				if err := c.gate.AcquireAPI(cat); err != nil {
					return nil, err
				}
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

func (c *act010Checker) canAffordVOD(minBytes int64, minReqs int) bool {
	if int(c.vod.requests.Load())+minReqs > c.vod.maxRequests {
		return false
	}
	if c.vod.bytes.Load()+minBytes > c.vod.maxBytes {
		return false
	}
	return true
}

func newDouyinStepRunner(steps *[]step, ck *act010Checker, redactor *act010Redactor) *stepRunner {
	return &stepRunner{
		steps:       steps,
		stopReason:  ck.stopReason,
		tripStop:    ck.tripStop,
		classifyErr: site.KindOf,
		scrubErr:    redactor.scrubError,
		stopOnKinds: map[string]bool{
			site.ErrKindRiskControl:  true,
			site.ErrKindRateLimited:  true,
			site.ErrKindAuthRequired: true,
		},
	}
}

func finalizeAct010Receipt(
	id string,
	rc *act010Receipt,
	outPath string,
	gate *sitecheck.Gate,
	ck *act010Checker,
	redactor *act010Redactor,
	tempDeleted bool,
) error {
	rc.TempMediaDeleted = tempDeleted
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

	rc.SecretScanClean = true
	if err := writeCleanReceipt(outPath, *rc, func(rawJSON string) error {
		return redactor.verifyJSONClean(rawJSON, rc)
	}); err != nil {
		rc.SecretScanClean = false
		rc.Passed = false
		return err
	}
	if !rc.Passed {
		if rc.StoppedBy != "" {
			return fmt.Errorf("%s stopped by %s", id, rc.StoppedBy)
		}
		return fmt.Errorf("%s completed with step failures", id)
	}
	return nil
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
	cookieStr, err := readEnvCookieFile(root, "TRACE_TEST_DOUYIN_COOKIE_FILE")
	if err != nil {
		return "", nil, errors.New("cookie: " + err.Error())
	}
	cleaned, err := credential.ParseDouyinCookieInput(cookieStr)
	if err != nil {
		return "", nil, errors.New("cookie: Douyin test Cookie missing sessionid/sessionid_ss or exceeds 64 KiB / 32 KiB cleaned limit")
	}
	return cookieStr, cleaned, nil
}
