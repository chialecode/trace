package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
)

const (
	act022ID                        = "act022"
	act022Label                     = "ACT-022"
	act022MaxRuns                   = 1
	act022MaxAPICalls               = 37
	act022MaxShortLinkHops          = 3
	act022MaxShortLinkBytes   int64 = 192 << 10 // 3 hops * 64 KiB cap
	act022MaxProbeCDNReqs           = 10
	act022MaxProbeCDNBytes    int64 = 60 << 20 // 60 MiB
	act022MaxCacheCDNReqs           = 20
	act022MaxCacheCDNBytes    int64 = 100 << 20 // 100 MiB
	act022LargeVideoCutoff    int64 = 24 << 20  // 24 MiB: probe head 8 MiB + tail 1 MiB only
	act022RangeChunkBytes     int64 = 8 << 20
	act022TailProbeBytes      int64 = 1 << 20
	act022DefaultSpacing            = 1500 * time.Millisecond
	act022Timeout                   = 10 * time.Minute
	act022NonexistentSynthUID       = "999999999999999"
	act022NonexistentSynthMid       = "9999999999999999"
)

// act022APILimits matches the ACT-022 authorization's quota breakdown exactly (sum == 37).
var act022APILimits = map[string]int{
	"login":         5,
	"resolve_user":  2,
	"user_timeline": 6,
	"post_detail":   7,
	"error_probe":   3,
	"mine_follow":   4,
	"mine_fav":      2,
	"mine_likes":    1,
	"formal_cache":  4,
	"reserve":       3,
}

var act022ValidExpectTags = map[string]bool{
	"active":        true,
	"pinned":        true,
	"fastRetweet":   true,
	"halfYear":      true,
	"nickname":      true,
	"customDomain":  true,
	"longText":      true,
	"longRetweet":   true,
	"over9":         true,
	"gif":           true,
	"livePhoto":     true,
	"mixed":         true,
	"video":         true,
	"videoPage":     true,
	"deletedOrigin": true,
}

var (
	reAct022Digits    = regexp.MustCompile(`^[0-9]+$`)
	reAct022UID       = regexp.MustCompile(`^[1-9][0-9]{4,18}$`)
	reAct022Mid       = regexp.MustCompile(`^[1-9][0-9]{14,19}$`)
	reAct022MblogID   = regexp.MustCompile(`^[0-9a-zA-Z]{7,12}$`)
	reAct022VideoOID  = regexp.MustCompile(`^1034:[A-Za-z0-9_-]{6,64}$`)
	reAct022Container = regexp.MustCompile(`^(?:100505|100606)([1-9][0-9]{4,18})$`)
	reAct022ImgSize   = regexp.MustCompile(`/(large|mw2000|mw1024|orj960|orj1080|bmiddle|thumbnail)/`)
)

type act022Targets struct {
	Version    int              `json:"version"`
	Act        string           `json:"act"`
	ProvidedAt string           `json:"providedAt"`
	Users      []act022UserItem `json:"users"`
	Posts      []act022PostItem `json:"posts"`
	ShortLinks []string         `json:"shortLinks"`
	ShareTexts []string         `json:"shareTexts"`
}

type act022UserItem struct {
	URL    string   `json:"url"`
	Expect []string `json:"expect"`
	Note   string   `json:"note,omitempty"`
}

type act022PostItem struct {
	URL    string   `json:"url"`
	Expect []string `json:"expect"`
	Note   string   `json:"note,omitempty"`
}

type parsedAct022User struct {
	Item         act022UserItem
	UID          string
	Nickname     string
	CustomDomain string
}

type parsedAct022Post struct {
	Item      act022PostItem
	Mid       string
	MblogID   string
	AuthorUID string
	VideoOID  string
}

type act022Config struct {
	RootDir        string
	OutPath        string
	MinSpacing     time.Duration // 0 => 1.5s default; <0 => 0s in synthetic tests
	Endpoints      weibo.Endpoints
	Transport      http.RoundTripper
	AllowLocalhost bool
}

// migrateAct022PreUsedLedger preserves pre-run Step 1 login calls recorded in the
// interim usage-act022.json format (`step1LoginApi.used`) by converting it atomically
// to sitecheck.Usage before sitecheck.OpenGate opens the file.
func migrateAct022PreUsedLedger(ledgerDir string) (int, error) {
	ledgerPath := filepath.Join(ledgerDir, fmt.Sprintf("usage-%s.json", act022ID))
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("act022 ledger read: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}

	var probe struct {
		ActID         string         `json:"actId"`
		Runs          int            `json:"runs"`
		APICalls      int            `json:"apiCalls"`
		Categories    map[string]int `json:"categories"`
		Step1LoginAPI *struct {
			Limit int `json:"limit"`
			Used  int `json:"used"`
		} `json:"step1LoginApi"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, fmt.Errorf("act022 ledger parse: %w", err)
	}
	if probe.APICalls > 0 || len(probe.Categories) > 0 || probe.Step1LoginAPI == nil || probe.Step1LoginAPI.Used <= 0 {
		return probe.Categories["login"], nil
	}

	used := probe.Step1LoginAPI.Used
	migrated := sitecheck.Usage{
		ActID:      act022ID,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Runs:       probe.Runs,
		APICalls:   used,
		Categories: map[string]int{"login": used},
	}
	out, err := json.MarshalIndent(migrated, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := storage.WriteFileAtomic(ledgerPath, out, 0o600); err != nil {
		return 0, fmt.Errorf("act022 ledger migrate: %w", err)
	}
	return used, nil
}

func loadAndValidateAct022Targets(root string, allowLocalhost bool) (act022Targets, []parsedAct022User, []parsedAct022Post, error) {
	var tg act022Targets
	p := filepath.Join(root, ".local", "site-check", "targets-weibo.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return tg, nil, nil, errors.New("targets: .local/site-check/targets-weibo.json is unreadable")
	}
	if err := json.Unmarshal(raw, &tg); err != nil {
		return tg, nil, nil, errors.New("targets: invalid JSON in targets-weibo.json")
	}
	if tg.Version != 0 && tg.Version != 1 {
		return tg, nil, nil, fmt.Errorf("targets: unsupported version %d", tg.Version)
	}

	users := make([]parsedAct022User, 0, len(tg.Users))
	for i, u := range tg.Users {
		for _, tag := range u.Expect {
			if !act022ValidExpectTags[tag] {
				return tg, nil, nil, fmt.Errorf("targets: users[%d] has invalid expect tag %q", i, tag)
			}
		}
		pu, err := parseAct022UserURL(u, allowLocalhost)
		if err != nil {
			return tg, nil, nil, fmt.Errorf("targets: users[%d]: %w", i, err)
		}
		users = append(users, pu)
	}

	posts := make([]parsedAct022Post, 0, len(tg.Posts))
	for i, post := range tg.Posts {
		for _, tag := range post.Expect {
			if !act022ValidExpectTags[tag] {
				return tg, nil, nil, fmt.Errorf("targets: posts[%d] has invalid expect tag %q", i, tag)
			}
		}
		pp, err := parseAct022PostURL(post, allowLocalhost)
		if err != nil {
			return tg, nil, nil, fmt.Errorf("targets: posts[%d]: %w", i, err)
		}
		posts = append(posts, pp)
	}

	return tg, users, posts, nil
}

func parseAct022UserURL(item act022UserItem, allowLocalhost bool) (parsedAct022User, error) {
	u, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || u.Host == "" {
		return parsedAct022User{}, errors.New("invalid user URL")
	}
	host := strings.ToLower(u.Hostname())
	if !allowLocalhost && !site.MatchDomainSuffix(host, []string{"weibo.com", "weibo.cn"}) {
		return parsedAct022User{}, errors.New("user URL host outside weibo domain")
	}
	segs := site.SplitCleanPath(u.Path)
	if len(segs) == 0 {
		return parsedAct022User{}, errors.New("user URL missing path")
	}
	out := parsedAct022User{Item: item}
	switch strings.ToLower(segs[0]) {
	case "u", "profile":
		if len(segs) < 2 || !reAct022UID.MatchString(segs[1]) {
			return parsedAct022User{}, errors.New("invalid /u/ uid")
		}
		out.UID, _ = site.NormalizeUID(site.Weibo, segs[1])
	case "p":
		if len(segs) < 2 {
			return parsedAct022User{}, errors.New("invalid /p/ container")
		}
		m := reAct022Container.FindStringSubmatch(segs[1])
		if len(m) != 2 {
			return parsedAct022User{}, errors.New("unsupported /p/ container prefix")
		}
		out.UID, _ = site.NormalizeUID(site.Weibo, m[1])
	case "n":
		if len(segs) < 2 || strings.TrimSpace(segs[1]) == "" {
			return parsedAct022User{}, errors.New("missing /n/ nickname")
		}
		nick, uErr := url.PathUnescape(segs[1])
		if uErr != nil {
			nick = segs[1]
		}
		out.Nickname = strings.TrimSpace(nick)
	default:
		if len(segs) == 1 && reAct022UID.MatchString(segs[0]) {
			out.UID, _ = site.NormalizeUID(site.Weibo, segs[0])
		} else if len(segs) == 1 && strings.TrimSpace(segs[0]) != "" {
			out.CustomDomain = strings.TrimSpace(segs[0])
		} else {
			return parsedAct022User{}, errors.New("unrecognized user URL form")
		}
	}
	return out, nil
}

func parseAct022PostURL(item act022PostItem, allowLocalhost bool) (parsedAct022Post, error) {
	u, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || u.Host == "" {
		return parsedAct022Post{}, errors.New("invalid post URL")
	}
	host := strings.ToLower(u.Hostname())
	if !allowLocalhost && !site.MatchDomainSuffix(host, []string{"weibo.com", "weibo.cn"}) {
		return parsedAct022Post{}, errors.New("post URL host outside weibo domain")
	}
	segs := site.SplitCleanPath(u.Path)
	q := u.Query()
	out := parsedAct022Post{Item: item}

	extractOID := func(raw string) string {
		s := strings.TrimSpace(raw)
		if dec, err := url.QueryUnescape(s); err == nil {
			s = strings.TrimSpace(dec)
		}
		if reAct022VideoOID.MatchString(s) {
			return s
		}
		return ""
	}

	if host == "video.weibo.com" && len(segs) >= 1 && strings.EqualFold(segs[0], "show") {
		if oid := extractOID(q.Get("fid")); oid != "" {
			out.VideoOID = oid
			return out, nil
		}
	}
	if len(segs) >= 3 && strings.EqualFold(segs[0], "tv") && strings.EqualFold(segs[1], "show") {
		if oid := extractOID(segs[2]); oid != "" {
			out.VideoOID = oid
			return out, nil
		}
	}
	if len(segs) >= 3 && strings.EqualFold(segs[0], "s") && strings.EqualFold(segs[1], "video") {
		oid := extractOID(q.Get("object_id"))
		if oid == "" {
			oid = extractOID(q.Get("fid"))
		}
		if oid != "" {
			out.VideoOID = oid
			return out, nil
		}
	}

	assignPostID := func(token string) bool {
		tok := strings.TrimSpace(token)
		if reAct022Mid.MatchString(tok) {
			out.Mid = tok
			out.MblogID, _ = weibo.MidToMblogID(tok)
			return true
		}
		if reAct022MblogID.MatchString(tok) {
			if mid, err := weibo.MblogIDToMid(tok); err == nil {
				out.Mid = mid
				out.MblogID = tok
				return true
			}
		}
		return false
	}

	if len(segs) >= 2 && (strings.EqualFold(segs[0], "detail") || strings.EqualFold(segs[0], "status")) {
		if assignPostID(segs[1]) {
			return out, nil
		}
	}
	if len(segs) >= 3 && segs[0] == "2" && strings.EqualFold(segs[1], "detail") {
		if assignPostID(segs[2]) {
			return out, nil
		}
	}
	if len(segs) >= 2 && reAct022UID.MatchString(segs[0]) && assignPostID(segs[1]) {
		out.AuthorUID, _ = site.NormalizeUID(site.Weibo, segs[0])
		return out, nil
	}
	if id := q.Get("id"); id != "" && assignPostID(id) {
		return out, nil
	}
	return parsedAct022Post{}, errors.New("unrecognized post/video URL form")
}

func readAndValidateWeiboCookie(root string) (string, map[string]string, error) {
	cookieStr, err := readEnvCookieFile(root, "TRACE_TEST_WEIBO_COOKIE_FILE")
	if err != nil {
		return "", nil, errors.New("cookie: " + err.Error())
	}
	cleaned, err := credential.ParseWeiboCookieInput(cookieStr)
	if err != nil {
		return "", nil, errors.New("cookie: Weibo test Cookie missing SUB, contains control chars, or exceeds 8 KiB")
	}
	return cookieStr, cleaned, nil
}

type act022Redactor struct {
	mu      sync.Mutex
	cookies map[string]bool
	ids     map[string]bool
	handles map[string]bool
	urls    map[string]bool
}

func newAct022Redactor() *act022Redactor {
	return &act022Redactor{
		cookies: make(map[string]bool),
		ids:     make(map[string]bool),
		handles: make(map[string]bool),
		urls:    make(map[string]bool),
	}
}

func isSecretWeiboCookie(key, val string) bool {
	k := strings.TrimSpace(key)
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if k == "" || len(v) < 6 {
		return false
	}
	switch strings.ToLower(v) {
	case "true", "false", "null", "none", "undefined":
		return false
	}
	for _, allowed := range credential.AllowedWeiboCookieKeys {
		if k == allowed {
			return true
		}
	}
	// Also protect any non-trivial stripped cookie values >= 8 chars from raw browser header.
	return len(v) >= 8
}

func (r *act022Redactor) addCookieHeader(raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, part := range strings.Split(raw, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if isSecretWeiboCookie(k, v) {
				r.cookies[v] = true
			}
		}
	}
}

func (r *act022Redactor) addCookieMap(m map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range m {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if isSecretWeiboCookie(k, v) {
			r.cookies[v] = true
		}
	}
}

func (r *act022Redactor) addIDs(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) >= 5 && v != "0" {
			r.ids[v] = true
		}
	}
}

func isTrackableHandle(v string) bool {
	runes := utf8.RuneCountInString(v)
	if len(v) == runes {
		return runes >= 3
	}
	return runes >= 2
}

func (r *act022Redactor) addHandles(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if isTrackableHandle(v) {
			r.handles[v] = true
		}
	}
}

func (r *act022Redactor) containsCookie(raw []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.cookies {
		if len(c) >= 6 && bytes.Contains(raw, []byte(c)) {
			return true
		}
	}
	return false
}

func (r *act022Redactor) addURLs(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			r.urls[v] = true
		}
	}
}

func (r *act022Redactor) addTargets(tg act022Targets, users []parsedAct022User, posts []parsedAct022Post) {
	for _, pu := range users {
		r.addURLs(pu.Item.URL)
		r.addIDs(pu.UID)
		r.addHandles(pu.Nickname, pu.CustomDomain)
	}
	for _, pp := range posts {
		r.addURLs(pp.Item.URL)
		r.addIDs(pp.Mid, pp.MblogID, pp.AuthorUID, pp.VideoOID)
		if _, after, ok := strings.Cut(pp.VideoOID, ":"); ok {
			r.addIDs(after)
		}
	}
	for _, sl := range tg.ShortLinks {
		r.addURLs(sl)
		if u, err := url.Parse(strings.TrimSpace(sl)); err == nil {
			segs := site.SplitCleanPath(u.Path)
			if len(segs) > 0 {
				r.addIDs(segs[len(segs)-1])
			}
		}
	}
	for _, st := range tg.ShareTexts {
		r.addHandles(st)
	}
}

func (r *act022Redactor) addPost(p *weibo.Post) {
	if p == nil {
		return
	}
	r.addIDs(p.Mid, p.MblogID, p.OriMid, p.Author.UID)
	r.addHandles(p.Author.ScreenName, p.Author.Domain)
	r.addURLs(p.CanonicalURL)
	if p.Video != nil {
		r.addIDs(p.Video.ObjectID)
		if _, after, ok := strings.Cut(p.Video.ObjectID, ":"); ok {
			r.addIDs(after)
		}
	}
	if p.Retweeted != nil {
		r.addPost(p.Retweeted)
	}
}

func (r *act022Redactor) scrubError(msg string) string {
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
		if len(id) >= 5 {
			out = strings.ReplaceAll(out, id, "[ID]")
		}
	}
	for h := range r.handles {
		if isTrackableHandle(h) {
			out = strings.ReplaceAll(out, h, "[HANDLE]")
		}
	}
	out = redactURLs(out)
	if runes := []rune(out); len(runes) > 180 {
		out = string(runes[:180])
	}
	return out
}

func (r *act022Redactor) verifyJSONClean(rawJSON string) error {
	if findings := sitecheck.ScanTextForSecrets(rawJSON); len(findings) > 0 {
		return fmt.Errorf("receipt failed secret scan: %s", findings[0])
	}
	if strings.Contains(rawJSON, "http://") || strings.Contains(rawJSON, "https://") {
		return errors.New("receipt contains a raw URL; not written")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.cookies {
		if strings.Contains(rawJSON, c) {
			return errors.New("receipt contains raw Cookie value; not written")
		}
	}
	for u := range r.urls {
		if strings.Contains(rawJSON, u) {
			return errors.New("receipt contains target URL; not written")
		}
	}
	for id := range r.ids {
		if strings.Contains(rawJSON, `"`+id+`"`) {
			return errors.New("receipt contains target or runtime identifier; not written")
		}
		if (!reAct022Digits.MatchString(id) && len(id) >= 5) || len(id) >= 7 {
			if strings.Contains(rawJSON, id) {
				return errors.New("receipt contains target or runtime identifier substring; not written")
			}
		}
	}
	for h := range r.handles {
		if !isTrackableHandle(h) {
			continue
		}
		runes := utf8.RuneCountInString(h)
		isASCII := len(h) == runes
		if isASCII && len(h) < 6 {
			if strings.Contains(rawJSON, `"`+h+`"`) {
				return errors.New("receipt contains account screen_name, domain, or title; not written")
			}
			continue
		}
		if strings.Contains(rawJSON, h) {
			return errors.New("receipt contains account screen_name, domain, or title; not written")
		}
	}
	return nil
}

type act022ReqKind int

const (
	act022KindAPI act022ReqKind = iota
	act022KindShortLink
	act022KindProbeCDN
	act022KindCacheCDN
	act022KindUnlisted
)

type act022APITelemetry struct {
	HTTPStatus int
	OK         int
	HasOK      bool
	Errno      int
	ErrorCode  int
	Code       string
	BodyBytes  int
}

type act022Checker struct {
	gate           *sitecheck.Gate
	apiLimits      map[string]int
	shortLink      *mediaBudget
	probeCDN       *mediaBudget
	cacheCDN       *mediaBudget
	minSpacing     time.Duration
	allowLocalhost bool

	guestControlActive          atomic.Bool
	expectedBusinessErrorActive atomic.Bool
	expectedCDNErrorActive      atomic.Bool

	phaseMu sync.RWMutex
	phase   string

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu             sync.Mutex
	stopped        string
	statuses       map[string]int
	pathCounts     map[string]int
	apiEnvelopes   map[string][]act022APITelemetry
	lastTelemetry  act022APITelemetry
	redirects      []map[string]any
	cookieOnCDNHit bool
}

func (c *act022Checker) setPhase(phase string) {
	c.phaseMu.Lock()
	c.phase = phase
	c.phaseMu.Unlock()
}

func (c *act022Checker) currentPhase() string {
	c.phaseMu.RLock()
	defer c.phaseMu.RUnlock()
	return c.phase
}

func (c *act022Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act022Checker) tripStop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act022Checker) snapshotLastTelemetry() act022APITelemetry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastTelemetry
}

func (c *act022Checker) redirectShapes() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.redirects...)
}

var act022ShortLinkAllowlistHosts = map[string]bool{
	"t.cn":                    true,
	"weibo.com":               true,
	"www.weibo.com":           true,
	"m.weibo.cn":              true,
	"weibo.cn":                true,
	"video.weibo.com":         true,
	"share.api.weibo.cn":      true,
	"weibointl.api.weibo.com": true,
	"live.weibo.com":          true,
}

func (c *act022Checker) noteCDNRedirect(cat string, r *http.Request, resp *http.Response) {
	shape := map[string]any{
		"category": cat,
		"status":   resp.StatusCode,
	}
	if loc, err := r.URL.Parse(resp.Header.Get("Location")); err == nil && loc.Host != "" {
		host := strings.ToLower(loc.Hostname())
		shape["targetScheme"] = strings.ToLower(loc.Scheme)
		shape["targetHostClass"] = hostClass(host)
		shape["targetIsIP"] = net.ParseIP(host) != nil
		shape["targetSameHost"] = strings.EqualFold(host, r.URL.Hostname())
		if cat == "short_link" {
			shape["withinWeiboAllowlist"] = act022ShortLinkAllowlistHosts[host] || (c.allowLocalhost && site.IsLoopbackHost(host))
		} else {
			shape["withinWeiboAllowlist"] = site.MatchDomainSuffix(host, site.DefaultWeiboMediaHosts()) || (c.allowLocalhost && site.IsLoopbackHost(host))
		}
	} else {
		shape["targetMissing"] = true
	}
	c.mu.Lock()
	if len(c.redirects) < 8 {
		c.redirects = append(c.redirects, shape)
	}
	c.mu.Unlock()
}

func (c *act022Checker) pace(ctx context.Context) error {
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

// isAuthorizedWeiboAPIPath checks the fail-closed endpoint + HTTP method allowlist for ACT-022.
func isAuthorizedWeiboAPIPath(method, p string) (canonicalPath string, allowedPhases map[string]bool, ok bool) {
	m := strings.ToUpper(strings.TrimSpace(method))
	switch {
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/setting/getBasicInfo"):
		return "/ajax/setting/getBasicInfo", map[string]bool{"login": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/profile/info"):
		return "/ajax/profile/info", map[string]bool{"login": true, "resolve_user": true, "user_timeline": true, "error_probe": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/statuses/mymblog"):
		return "/ajax/statuses/mymblog", map[string]bool{"user_timeline": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/statuses/show"):
		return "/ajax/statuses/show", map[string]bool{"post_detail": true, "error_probe": true, "formal_cache": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/statuses/longtext"):
		return "/ajax/statuses/longtext", map[string]bool{"post_detail": true, "formal_cache": true, "reserve": true}, true
	case m == http.MethodPost && strings.HasSuffix(p, "/tv/api/component"):
		return "/tv/api/component", map[string]bool{"post_detail": true, "formal_cache": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/friendships/friends"):
		return "/ajax/friendships/friends", map[string]bool{"mine_follow": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/profile/followContent"):
		return "/ajax/profile/followContent", map[string]bool{"mine_follow": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/feed/allGroups"):
		return "/ajax/feed/allGroups", map[string]bool{"mine_follow": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/favorites/all_fav"):
		return "/ajax/favorites/all_fav", map[string]bool{"mine_fav": true, "reserve": true}, true
	case m == http.MethodGet && strings.HasSuffix(p, "/ajax/statuses/likelist"):
		return "/ajax/statuses/likelist", map[string]bool{"mine_likes": true, "reserve": true}, true
	default:
		return "", nil, false
	}
}

func (c *act022Checker) classify(r *http.Request) (kind act022ReqKind, cat string, safePath string) {
	phase := c.currentPhase()
	host := strings.ToLower(r.URL.Hostname())
	p := r.URL.Path
	method := strings.ToUpper(r.Method)

	if phase == "short_link" {
		if method != http.MethodGet {
			return act022KindUnlisted, "unlisted", "unlisted"
		}
		if host == "t.cn" || site.MatchDomainSuffix(host, []string{"weibo.com", "weibo.cn"}) || (c.allowLocalhost && site.IsLoopbackHost(host)) {
			return act022KindShortLink, "short_link", hostClass(host) + "/<short>"
		}
		return act022KindUnlisted, "unlisted", "unlisted"
	}

	if canonPath, allowedPhases, ok := isAuthorizedWeiboAPIPath(method, p); ok {
		if !allowedPhases[phase] {
			return act022KindUnlisted, "unlisted", canonPath
		}
		return act022KindAPI, phase, canonPath
	}

	if (phase == "probe_cdn" || phase == "formal_cache") && (method == http.MethodGet || method == http.MethodHead) {
		isWeiboCDN := site.MatchDomainSuffix(host, site.DefaultWeiboMediaHosts()) ||
			(host == "video.weibo.com" && strings.ToLower(p) == "/media/play") ||
			(c.allowLocalhost && site.IsLoopbackHost(host))
		if isWeiboCDN {
			if phase == "probe_cdn" {
				return act022KindProbeCDN, "probe_cdn", hostClass(host)
			}
			return act022KindCacheCDN, "cache_cdn", hostClass(host)
		}
	}

	return act022KindUnlisted, "unlisted", "unlisted"
}

func (c *act022Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("act022 halted (%s)", reason)
		}

		kind, cat, safePath := c.classify(r)
		switch kind {
		case act022KindUnlisted:
			return nil, fmt.Errorf("%w: endpoint or method outside ACT-022 authorization", sitecheck.ErrQuotaExceeded)

		case act022KindShortLink:
			if r.Header.Get("Cookie") != "" {
				c.mu.Lock()
				c.cookieOnCDNHit = true
				c.mu.Unlock()
				return nil, errors.New("act022: refusing to send Cookie on t.cn short-link probe")
			}
			if err := c.shortLink.acquire(); err != nil {
				return nil, err
			}

		case act022KindAPI:
			if err := checkAPICategoryAllowed(c.apiLimits, cat); err != nil {
				return nil, err
			}
			if err := c.gate.AcquireAPI(cat); err != nil {
				return nil, err
			}

		case act022KindProbeCDN:
			if r.Header.Get("Cookie") != "" {
				c.mu.Lock()
				c.cookieOnCDNHit = true
				c.mu.Unlock()
				return nil, errors.New("act022: refusing to send Cookie to probe CDN")
			}
			if c.probeCDN.bytes.Load() >= c.probeCDN.maxBytes {
				return nil, fmt.Errorf("%w: probe CDN bytes exhausted (%d)", sitecheck.ErrQuotaExceeded, c.probeCDN.maxBytes)
			}
			if err := c.probeCDN.acquire(); err != nil {
				return nil, err
			}

		case act022KindCacheCDN:
			if r.Header.Get("Cookie") != "" {
				c.mu.Lock()
				c.cookieOnCDNHit = true
				c.mu.Unlock()
				return nil, errors.New("act022: refusing to send Cookie to formal-cache CDN")
			}
			if c.cacheCDN.bytes.Load() >= c.cacheCDN.maxBytes {
				return nil, fmt.Errorf("%w: formal-cache CDN bytes exhausted (%d)", sitecheck.ErrQuotaExceeded, c.cacheCDN.maxBytes)
			}
			if err := c.cacheCDN.acquire(); err != nil {
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
		c.pathCounts[safePath]++
		c.mu.Unlock()

		// Immediate zero-tolerance HTTP status stops: 414, 418, 429, 432.
		switch resp.StatusCode {
		case http.StatusRequestURITooLong, http.StatusTeapot, http.StatusTooManyRequests, 432:
			c.tripStop(fmt.Sprintf("http_%d", resp.StatusCode))
			return resp, nil
		}

		switch kind {
		case act022KindShortLink:
			if resp.StatusCode == http.StatusForbidden {
				c.tripStop("http_403")
			}
			if isMediaRedirect(resp) {
				c.noteCDNRedirect("short_link", r, resp)
			}
			resp.Body = countingBody{ReadCloser: resp.Body, budget: c.shortLink}
			return resp, nil

		case act022KindAPI:
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if readErr != nil {
				return nil, readErr
			}
			c.inspectAPIResponse(safePath, resp, raw)
			return resp, nil

		case act022KindProbeCDN, act022KindCacheCDN:
			budget := c.probeCDN
			if kind == act022KindCacheCDN {
				budget = c.cacheCDN
			}
			if resp.StatusCode == http.StatusForbidden && !c.expectedCDNErrorActive.Load() {
				c.tripStop("http_403_cdn")
				return resp, nil
			}
			if isMediaRedirect(resp) {
				c.noteCDNRedirect(cat, r, resp)
				resp.Body = countingBody{ReadCloser: resp.Body, budget: budget}
				return resp, nil
			}
			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			if resp.StatusCode == http.StatusOK && strings.Contains(ct, "text/html") {
				c.tripStop("cdn_html_verify_page")
				return resp, nil
			}
			resp.Body = countingBody{ReadCloser: resp.Body, budget: budget}
			return resp, nil
		}

		return resp, nil
	})
}

func classifiedKindOf(se *site.Error) string {
	if se == nil {
		return ""
	}
	return se.Kind
}

func jsonTopHasNonEmptyKey(raw []byte, key string) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(trimmed, &top) != nil {
		return false
	}
	v := bytes.TrimSpace(top[key])
	return len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`)) && !bytes.Equal(v, []byte("{}"))
}

func jsonDataHasNonEmptyKey(raw []byte, key string) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(trimmed, &top) != nil {
		return false
	}
	dataRaw := bytes.TrimSpace(top["data"])
	if len(dataRaw) == 0 || dataRaw[0] != '{' {
		return false
	}
	var dm map[string]json.RawMessage
	if json.Unmarshal(dataRaw, &dm) != nil {
		return false
	}
	v := bytes.TrimSpace(dm[key])
	return len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`)) && !bytes.Equal(v, []byte("{}"))
}

func populateAct022TelFromJSON(trimmed []byte, tel *act022APITelemetry) (map[string]json.RawMessage, bool) {
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, false
	}
	if okVal, hasOK := extractAct022StrictInt(top["ok"]); hasOK {
		tel.OK = okVal
		tel.HasOK = true
	}
	tel.Errno = extractAct022Int(top, "errno")
	tel.ErrorCode = extractAct022Int(top, "error_code")
	if codeRaw, hasCode := top["code"]; hasCode {
		tel.Code = strings.Trim(strings.TrimSpace(string(codeRaw)), `"`)
	}
	return top, true
}

func (c *act022Checker) inspectAPIResponse(safePath string, resp *http.Response, raw []byte) {
	trimmed := bytes.TrimSpace(raw)
	tel := act022APITelemetry{
		HTTPStatus: resp.StatusCode,
		BodyBytes:  len(raw),
	}
	recordTel := func() {
		c.mu.Lock()
		c.lastTelemetry = tel
		if len(c.apiEnvelopes[safePath]) < 8 {
			c.apiEnvelopes[safePath] = append(c.apiEnvelopes[safePath], tel)
		}
		c.mu.Unlock()
	}

	isOptionalProbe := c.expectedBusinessErrorActive.Load() &&
		(safePath == "/ajax/statuses/likelist" ||
			safePath == "/ajax/profile/followContent" ||
			safePath == "/ajax/feed/allGroups" ||
			safePath == "/ajax/friendships/friends")
	isGuestControl := c.guestControlActive.Load()

	// Unexpected HTTP 403 (allow optional feature-flag probes and explicit V-10 JSON 20112).
	if resp.StatusCode == http.StatusForbidden {
		if c.expectedBusinessErrorActive.Load() {
			populateAct022TelFromJSON(trimmed, &tel)
		}
		recordTel()
		if isOptionalProbe {
			return
		}
		if c.expectedBusinessErrorActive.Load() && (tel.ErrorCode == 20112 || tel.Errno == 20112) {
			return
		}
		c.tripStop("http_403")
		return
	}

	// 3xx redirects on PC Ajax endpoints.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		recordTel()
		loc := strings.ToLower(resp.Header.Get("Location"))
		if strings.Contains(loc, "geetest") || strings.Contains(loc, "captcha") || strings.Contains(loc, "slider") || strings.Contains(loc, "security/verify") {
			c.tripStop("redirect_verify_page")
			return
		}
		if !isGuestControl {
			c.tripStop("login_or_visitor_redirect")
		}
		return
	}

	// 401 while carrying test Cookie.
	if resp.StatusCode == http.StatusUnauthorized {
		recordTel()
		if !isGuestControl {
			c.tripStop("http_401")
		}
		return
	}

	// Expected 404/400 on V-10 nonexistent probe or optional feature-flag probes (likelist/followContent/allGroups).
	if (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest) && c.expectedBusinessErrorActive.Load() {
		populateAct022TelFromJSON(trimmed, &tel)
		recordTel()
		return
	}

	// 0-byte body check.
	if len(trimmed) == 0 {
		recordTel()
		if !isOptionalProbe {
			c.tripStop("empty_api_body")
		}
		return
	}

	// HTML / non-JSON body check.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") || (trimmed[0] != '{' && trimmed[0] != '[') {
		recordTel()
		lower := strings.ToLower(string(trimmed[:min(len(trimmed), 8192)]))
		rawStr := string(trimmed[:min(len(trimmed), 8192)])
		if strings.Contains(lower, "geetest") || strings.Contains(lower, "captcha") || strings.Contains(lower, "slider") ||
			strings.Contains(rawStr, "滑块") || strings.Contains(rawStr, "验证码") || strings.Contains(rawStr, "安全验证") {
			c.tripStop("html_verify_page")
			return
		}
		if strings.Contains(lower, "sina visitor system") || strings.Contains(lower, "genvisitor") ||
			strings.Contains(lower, "newlogin") || strings.Contains(lower, "login.sina.com.cn") {
			if !isGuestControl {
				c.tripStop("html_visitor_or_login_page")
			}
			return
		}
		if !isOptionalProbe {
			c.tripStop("non_json_api_body")
		}
		return
	}

	// Parse JSON envelope using UseNumber to preserve 64-bit integer precision.
	top, okJSON := populateAct022TelFromJSON(trimmed, &tel)
	if !okJSON {
		recordTel()
		if !isOptionalProbe {
			c.tripStop("non_json_api_body")
		}
		return
	}
	recordTel()

	// Challenge fields in JSON always halt immediately.
	for _, k := range []string{"geetest", "captcha", "verify_url"} {
		if rawVal, ok := top[k]; ok {
			t := strings.TrimSpace(string(rawVal))
			if t != "" && t != "null" && t != `""` && t != "{}" && t != "[]" && t != "false" {
				c.tripStop("json_verify_challenge")
				return
			}
		}
	}

	if tel.HasOK && tel.OK == -100 {
		if !isGuestControl {
			c.tripStop("ok_minus_100")
		}
		return
	}

	if tel.ErrorCode == 10022 || tel.ErrorCode == 10023 || tel.ErrorCode == 10024 ||
		tel.Errno == 10022 || tel.Errno == 10023 || tel.Errno == 10024 {
		c.tripStop("rate_limited")
		return
	}
	if tel.Code == "100006" && !isGuestControl {
		c.tripStop("component_auth_required")
		return
	}
}

func extractAct022StrictInt(raw json.RawMessage) (int, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return 0, false
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	if dec.Decode(&n) == nil {
		if v, err := n.Int64(); err == nil {
			return int(v), true
		}
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v, true
		}
	}
	return 0, false
}

func extractAct022Int(top map[string]json.RawMessage, key string) int {
	if v, ok := extractAct022StrictInt(top[key]); ok && v != 0 {
		return v
	}
	if dataRaw, ok := top["data"]; ok {
		var dm map[string]json.RawMessage
		if json.Unmarshal(dataRaw, &dm) == nil {
			if v, ok := extractAct022StrictInt(dm[key]); ok && v != 0 {
				return v
			}
		}
	}
	return 0
}

func (c *act022Checker) canAffordProbeCDN(minBytes int64, minReqs int) bool {
	if int(c.probeCDN.requests.Load())+minReqs > c.probeCDN.maxRequests {
		return false
	}
	if c.probeCDN.bytes.Load()+minBytes > c.probeCDN.maxBytes {
		return false
	}
	return true
}

func (c *act022Checker) remainingCacheCDNRequests() int {
	rem := c.cacheCDN.maxRequests - int(c.cacheCDN.requests.Load())
	if rem < 0 {
		return 0
	}
	return rem
}

func newWeiboStepRunner(steps *[]step, ck *act022Checker, redactor *act022Redactor) *stepRunner {
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

func sniffWeiboAssetMagic(raw []byte) string {
	if len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a") {
		return "gif"
	}
	return sniffAssetMagic(raw)
}

func probeWeiboImageAsset(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	imgURL string,
	limitBytes int64,
	decodeGIF bool,
) (map[string]any, error) {
	if err := spec.ValidateURL(imgURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, err
	}
	spec.ApplyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out := map[string]any{
		"status":      resp.StatusCode,
		"contentType": resp.Header.Get("Content-Type"),
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return out, fmt.Errorf("image HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limitBytes+1))
	if err != nil {
		return out, err
	}
	if int64(len(raw)) > limitBytes {
		return out, fmt.Errorf("image exceeded %d byte cap", limitBytes)
	}
	if len(raw) == 0 {
		return out, errors.New("image returned 0 bytes")
	}

	magic := sniffWeiboAssetMagic(raw)
	out["magicFormat"] = magic
	out["bytes"] = len(raw)
	if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(raw)); cfgErr == nil {
		out["width"] = cfg.Width
		out["height"] = cfg.Height
	}
	if decodeGIF && magic == "gif" {
		if g, gErr := gif.DecodeAll(bytes.NewReader(raw)); gErr == nil {
			out["gifFrameCount"] = len(g.Image)
			out["gifAnimated"] = len(g.Image) > 1
		}
	}
	return out, nil
}

func rewriteWeiboImageVariant(rawURL, variant string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return "", false
	}
	if !reAct022ImgSize.MatchString(u.Path) {
		return "", false
	}
	u.Path = reAct022ImgSize.ReplaceAllString(u.Path, "/"+variant+"/")
	return u.String(), true
}

func computeExpiresRemainingSec(rawURL string) (int64, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, false
	}
	q := u.Query()
	rawExp := q.Get("Expires")
	if rawExp == "" {
		rawExp = q.Get("expires")
	}
	if rawExp == "" {
		return 0, false
	}
	expUnix, err := strconv.ParseInt(strings.TrimSpace(rawExp), 10, 64)
	if err != nil || expUnix <= 0 {
		return 0, false
	}
	return expUnix - time.Now().Unix(), true
}

func probeOrDownloadWeiboMP4(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	workDir string,
	fileName string,
	mediaURL string,
	hintSize int64,
	expectedDurationSec float64,
	strictDuration bool,
	ck *act022Checker,
) (map[string]any, error) {
	if err := spec.ValidateURL(mediaURL); err != nil {
		return nil, err
	}

	firstEnd := act022RangeChunkBytes - 1
	if hintSize > 0 && hintSize < act022RangeChunkBytes {
		firstEnd = hintSize - 1
	}

	headBytes, status1, totalSize, contentType, err := fetchRangeChunk(ctx, client, spec, mediaURL, 0, firstEnd)
	if err != nil {
		return nil, err
	}
	if ck.stopReason() != "" {
		return nil, fmt.Errorf("stopped: %s", ck.stopReason())
	}
	if len(headBytes) == 0 {
		return nil, errors.New("unexpected 0-byte initial Weibo media chunk")
	}
	if totalSize <= 0 && hintSize > 0 {
		totalSize = hintSize
	}
	if totalSize <= 0 && status1 == http.StatusOK {
		totalSize = int64(len(headBytes))
	}

	if totalSize > act022LargeVideoCutoff {
		tailStart := totalSize - act022TailProbeBytes
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
		boxSummary["http206Supported"] = status1 == http.StatusPartialContent
		boxSummary["contentType"] = contentType
		return boxSummary, nil
	}

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
		nextEnd := written + act022RangeChunkBytes - 1
		if nextEnd >= totalSize {
			nextEnd = totalSize - 1
		}
		chunk, st, _, _, cErr := fetchRangeChunk(ctx, client, spec, mediaURL, written, nextEnd)
		if cErr != nil {
			_ = f.Close()
			return nil, cErr
		}
		if st != http.StatusPartialContent {
			_ = f.Close()
			return nil, fmt.Errorf("media range offset %d expected HTTP 206, got %d", written, st)
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
		http206Count++
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	topBoxes := topLevelBoxes(outPath)
	magic := sniffWeiboAssetMagic(headBytes)

	checkDur := 0.0
	if strictDuration {
		checkDur = expectedDurationSec
	}
	muxSum, err := download.EnsureSingleMP4Context(ctx, outPath, checkDur)
	if err != nil {
		return map[string]any{
			"largeFilePartialProbe": false,
			"bytes":                 written,
			"magicFormat":           magic,
			"topBoxes":              topBoxes,
			"http206Supported":      http206Count > 0,
			"contentType":           contentType,
			"mp4ffVerified":         false,
		}, err
	}
	return map[string]any{
		"largeFilePartialProbe": false,
		"bytes":                 muxSum.FileBytes,
		"rangeRequests":         rangeReqs,
		"http206Count":          http206Count,
		"http206Supported":      http206Count > 0,
		"contentType":           contentType,
		"magicFormat":           magic,
		"topBoxes":              topBoxes,
		"codecs":                muxSum.Codecs,
		"width":                 muxSum.Width,
		"height":                muxSum.Height,
		"moovBeforeMdat":        muxSum.MoovBeforeMdat,
		"hasElst":               muxSum.HasElst,
		"durationSec":           float64(int(muxSum.DurationSec*1000+0.5)) / 1000,
		"mp4ffVerified":         true,
	}, nil
}

func probeWeiboLivePhotoMotion(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	workDir string,
	motionURL string,
	ck *act022Checker,
) (map[string]any, error) {
	if err := spec.ValidateURL(motionURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, motionURL, nil)
	if err != nil {
		return nil, err
	}
	spec.ApplyHeaders(req)

	liveClient := &http.Client{
		Transport: client.Transport,
		Timeout:   client.Timeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("weibo livephoto redirect limit exceeded")
			}
			host := strings.ToLower(r.URL.Hostname())
			if !(ck != nil && ck.allowLocalhost && site.IsLoopbackHost(host)) {
				if err := spec.ValidateRedirectURL(r.URL.String()); err != nil {
					return err
				}
			}
			spec.ApplyHeaders(r)
			return nil
		},
	}

	resp, err := liveClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return map[string]any{"status": resp.StatusCode}, fmt.Errorf("livephoto HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > 16<<20 {
		return nil, errors.New("livephoto clip exceeded 16 MiB cap")
	}
	if len(raw) == 0 {
		return nil, errors.New("livephoto clip returned 0 bytes")
	}

	outPath := filepath.Join(workDir, "v12-livephoto.mov")
	if err := os.WriteFile(outPath, raw, 0o600); err != nil {
		return nil, err
	}
	majorBrand := ""
	if len(raw) >= 12 && string(raw[4:8]) == "ftyp" {
		majorBrand = strings.TrimSpace(string(raw[8:12]))
	}
	summary := map[string]any{
		"status":      resp.StatusCode,
		"contentType": resp.Header.Get("Content-Type"),
		"bytes":       len(raw),
		"magicFormat": sniffWeiboAssetMagic(raw),
		"majorBrand":  majorBrand,
		"topBoxes":    topLevelBoxes(outPath),
	}
	if inspectSum, iErr := download.InspectMP4FileContext(ctx, outPath); iErr == nil {
		summary["mp4ffInspectOK"] = true
		summary["codecs"] = inspectSum.Codecs
		summary["width"] = inspectSum.Width
		summary["height"] = inspectSum.Height
		summary["moovBeforeMdat"] = inspectSum.MoovBeforeMdat
		summary["durationSec"] = float64(int(inspectSum.DurationSec*1000+0.5)) / 1000
	} else {
		summary["mp4ffInspectOK"] = false
	}
	// Verify lossless QuickTime .mov -> ISO BMFF .mp4 repackaging via EnsureLivePhotoMP4Context.
	if muxSum, repackaged, mErr := download.EnsureLivePhotoMP4Context(ctx, outPath, 0); mErr == nil {
		summary["ensureLivePhotoMP4OK"] = true
		summary["quickTimeRepackaged"] = repackaged
		summary["normalizedMoovBeforeMdat"] = muxSum.MoovBeforeMdat
	} else {
		summary["ensureLivePhotoMP4OK"] = false
	}
	return summary, nil
}
