package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/sitecheck"
)

const (
	act024ID                      = "act024"
	act024Label                   = "ACT-024"
	act025ID                      = "act025"
	act025Label                   = "ACT-025"
	act024MaxRuns                 = 1
	act024MaxTotalRequests        = 32
	act024MaxFrontendBytes  int64 = 8 << 20 // 8 MiB per frontend resource
	act024MaxMediaRequests        = 5
	act024MaxMediaBytes     int64 = 25 << 20 // 25 MiB total media CDN
	act024MaxShortLinkHops        = 3
	act024MaxShortLinkBytes int64 = 192 << 10 // 3 hops * 64 KiB
	act024DefaultMinSpacing       = 5 * time.Second
	act024DefaultJitterMax        = 1500 * time.Millisecond
	act024Timeout                 = 10 * time.Minute
	act024OpBookmarkFolders       = "BookmarkFoldersSlice"
)

// act024APILimits matches the ACT-024 authorization's quota breakdown exactly (sum == 32).
// Pre-existing preflight usage in .local/site-check/usage-act024.json (`apiCalls=1, login=1`)
// is preserved via sitecheck.OpenGate so login has <= 4 new calls and total has <= 31 new calls.
var act024APILimits = map[string]int{
	"frontend":      3,
	"login":         5,
	"shortlink":     3,
	"unauthControl": 1,
	"postDetail":    3,
	"userTimeline":  4,
	"errorProbe":    2,
	"mine":          5,
	"media":         5,
	"csrfReplay":    1,
}

// act025APILimits supports the follow-up X real-site check after Bookmarks/Likes lazy
// chunk resolution (home + main + ondemand + <=2 lazy chunks = frontend <= 5),
// keeping total budget strictly <= 32 on an independent ledger (.local/site-check/usage-act025.json).
var act025APILimits = map[string]int{
	"frontend":      5,
	"login":         4,
	"shortlink":     3,
	"unauthControl": 1,
	"postDetail":    3,
	"userTimeline":  3,
	"errorProbe":    2,
	"mine":          5,
	"media":         5,
	"csrfReplay":    1,
}

var act024ValidExpectTags = map[string]bool{
	"active":         true,
	"pinned":         true,
	"retweet":        true,
	"quote":          true,
	"retweetOrQuote": true,
	"longText":       true,
	"thread":         true,
	"mixed":          true,
	"images":         true,
	"gif":            true,
	"video":          true,
	"poll":           true,
	"card":           true,
	"protected":      true,
	"deletedOrigin":  true,
	"tcoLink":        true,
}

var (
	reAct024Digits            = regexp.MustCompile(`^[0-9]+$`)
	reAct024ScreenName        = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	reAct024ShortCode         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reAct024TcoURLInText      = regexp.MustCompile(`https?://t\.co/[A-Za-z0-9_-]{4,64}`)
	reAct024MainBundleFile    = regexp.MustCompile(`^main[.-][A-Za-z0-9_-]+\.js$`)
	reAct024OndemandFile      = regexp.MustCompile(`^ondemand\.s[.-][A-Za-z0-9_-]+\.js$`)
	reAct024BookmarkFoldersOp = regexp.MustCompile(`(?:["']?operationName["']?|["']?name["']?)\s*:\s*["']BookmarkFoldersSlice["']`)
	reAct024QueryIDField      = regexp.MustCompile(`(?:["']?queryId["']?|["']?id["']?)\s*:\s*["']([A-Za-z0-9_-]{6,64})["']`)
	reAct024FeatureSwitchList = regexp.MustCompile(`["']?featureSwitches["']?\s*:\s*\[([^\]]*)\]`)
	reAct024QuotedString      = regexp.MustCompile(`["']([A-Za-z_][A-Za-z0-9_]*)["']`)
)

// act024Targets supports all fields from both the X check plan
// and the local .local/site-check/targets-x.json schema.
type act024Targets struct {
	Version        int                  `json:"version"`
	Act            string               `json:"act"`
	ProvidedAt     string               `json:"providedAt"`
	Note           string               `json:"note,omitempty"`
	Users          []act024TargetItem   `json:"users"`
	Posts          []act024TargetItem   `json:"posts"`
	ProtectedUsers []act024TargetItem   `json:"protectedUsers,omitempty"`
	ShortLinks     []string             `json:"shortLinks,omitempty"`
	Missing        *act024MissingTarget `json:"missing,omitempty"`
}

type act024TargetItem struct {
	URL    string   `json:"url"`
	Expect []string `json:"expect"`
	Note   string   `json:"note,omitempty"`
}

type act024MissingTarget struct {
	UserID   string `json:"userId,omitempty"`
	StatusID string `json:"statusId,omitempty"`
}

type parsedAct024User struct {
	Item       act024TargetItem
	RestID     string
	ScreenName string
}

type parsedAct024Post struct {
	Item       act024TargetItem
	StatusID   string
	ScreenName string
}

type act024Config struct {
	RootDir        string
	OutPath        string
	MinSpacing     time.Duration // 0 => 5s default; <0 => 0s in synthetic tests
	JitterMax      time.Duration // 0 => 1.5s default; <0 => 0s in synthetic tests
	Endpoints      x.Endpoints
	Transport      http.RoundTripper
	AssetTransport http.RoundTripper
	MediaTransport http.RoundTripper
	AllowLocalhost bool
	TxIDOptions    []x.TxIDOption
	StepHook       func(runner *stepRunner) // optional test-only hook (e.g. panic recovery test)
}

var act024AllowedMainHosts = map[string]bool{
	"x.com":              true,
	"www.x.com":          true,
	"mobile.x.com":       true,
	"m.x.com":            true,
	"twitter.com":        true,
	"www.twitter.com":    true,
	"mobile.twitter.com": true,
	"m.twitter.com":      true,
}

func loadAndValidateAct024Targets(
	root string,
	allowLocalhost bool,
) (act024Targets, []parsedAct024User, []parsedAct024User, []parsedAct024Post, error) {
	var tg act024Targets
	p := filepath.Join(root, ".local", "site-check", "targets-x.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return tg, nil, nil, nil, errors.New("targets: .local/site-check/targets-x.json is unreadable")
	}
	if err := json.Unmarshal(raw, &tg); err != nil {
		return tg, nil, nil, nil, errors.New("targets: invalid JSON in targets-x.json")
	}
	if tg.Version != 0 && tg.Version != 1 {
		return tg, nil, nil, nil, fmt.Errorf("targets: unsupported version %d", tg.Version)
	}

	users := make([]parsedAct024User, 0, len(tg.Users))
	protectedUsers := make([]parsedAct024User, 0, len(tg.ProtectedUsers))

	for i, u := range tg.Users {
		for _, tag := range u.Expect {
			if !act024ValidExpectTags[tag] {
				return tg, nil, nil, nil, fmt.Errorf("targets: users[%d] has invalid expect tag %q", i, tag)
			}
		}
		pu, err := parseAct024UserTarget(u, allowLocalhost)
		if err != nil {
			return tg, nil, nil, nil, fmt.Errorf("targets: users[%d]: %w", i, err)
		}
		if hasTag(u.Expect, "protected") && !hasTag(u.Expect, "active") {
			protectedUsers = append(protectedUsers, pu)
		} else {
			users = append(users, pu)
			if hasTag(u.Expect, "protected") {
				protectedUsers = append(protectedUsers, pu)
			}
		}
	}

	for i, u := range tg.ProtectedUsers {
		for _, tag := range u.Expect {
			if !act024ValidExpectTags[tag] {
				return tg, nil, nil, nil, fmt.Errorf("targets: protectedUsers[%d] has invalid expect tag %q", i, tag)
			}
		}
		pu, err := parseAct024UserTarget(u, allowLocalhost)
		if err != nil {
			return tg, nil, nil, nil, fmt.Errorf("targets: protectedUsers[%d]: %w", i, err)
		}
		protectedUsers = append(protectedUsers, pu)
	}

	posts := make([]parsedAct024Post, 0, len(tg.Posts))
	for i, post := range tg.Posts {
		for _, tag := range post.Expect {
			if !act024ValidExpectTags[tag] {
				return tg, nil, nil, nil, fmt.Errorf("targets: posts[%d] has invalid expect tag %q", i, tag)
			}
		}
		pp, err := parseAct024PostTarget(post, allowLocalhost)
		if err != nil {
			return tg, nil, nil, nil, fmt.Errorf("targets: posts[%d]: %w", i, err)
		}
		posts = append(posts, pp)
	}

	for i, sl := range tg.ShortLinks {
		if err := validateAct024ShortLinkURL(sl, allowLocalhost); err != nil {
			return tg, nil, nil, nil, fmt.Errorf("targets: shortLinks[%d]: %w", i, err)
		}
	}

	if tg.Missing != nil {
		if uid := strings.TrimSpace(tg.Missing.UserID); uid != "" {
			norm, err := site.NormalizeUID(site.X, uid)
			if err != nil {
				return tg, nil, nil, nil, errors.New("targets: missing.userId is invalid")
			}
			tg.Missing.UserID = norm
		}
		if sid := strings.TrimSpace(tg.Missing.StatusID); sid != "" {
			sf, err := x.ParseSnowflakeID(sid)
			if err != nil {
				return tg, nil, nil, nil, errors.New("targets: missing.statusId is invalid")
			}
			tg.Missing.StatusID = sf.ID
		}
	}

	return tg, users, protectedUsers, posts, nil
}

func normalizeAct024TargetURL(raw string, allowLocalhost bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid URL")
	}
	if rewritten, ok := x.RewriteMirrorURL(u); ok {
		u = rewritten
	}
	host := strings.ToLower(u.Hostname())
	if allowLocalhost && site.IsLoopbackHost(host) {
		return u, nil
	}
	if !act024AllowedMainHosts[host] {
		return nil, errors.New("URL host outside X domain")
	}
	return u, nil
}

func parseAct024UserTarget(item act024TargetItem, allowLocalhost bool) (parsedAct024User, error) {
	u, err := normalizeAct024TargetURL(item.URL, allowLocalhost)
	if err != nil {
		return parsedAct024User{}, err
	}
	segs := site.SplitCleanPath(u.Path)
	q := u.Query()
	out := parsedAct024User{Item: item}

	if len(segs) >= 3 && strings.EqualFold(segs[0], "i") && strings.EqualFold(segs[1], "user") {
		norm, err := site.NormalizeUID(site.X, segs[2])
		if err != nil {
			return parsedAct024User{}, errors.New("invalid /i/user/ rest_id")
		}
		out.RestID = norm
		return out, nil
	}
	if len(segs) >= 2 && strings.EqualFold(segs[0], "intent") && strings.EqualFold(segs[1], "user") {
		if rawUID := strings.TrimSpace(q.Get("user_id")); rawUID != "" {
			norm, err := site.NormalizeUID(site.X, rawUID)
			if err != nil {
				return parsedAct024User{}, errors.New("invalid /intent/user user_id")
			}
			out.RestID = norm
			return out, nil
		}
		if rawHandle := strings.TrimPrefix(strings.TrimSpace(q.Get("screen_name")), "@"); reAct024ScreenName.MatchString(rawHandle) {
			out.ScreenName = rawHandle
			return out, nil
		}
		return parsedAct024User{}, errors.New("invalid /intent/user target")
	}
	if len(segs) >= 1 && reAct024ScreenName.MatchString(segs[0]) && !strings.EqualFold(segs[0], "i") && !strings.EqualFold(segs[0], "intent") {
		out.ScreenName = segs[0]
		return out, nil
	}
	return parsedAct024User{}, errors.New("unrecognized X user URL form")
}

func parseAct024PostTarget(item act024TargetItem, allowLocalhost bool) (parsedAct024Post, error) {
	u, err := normalizeAct024TargetURL(item.URL, allowLocalhost)
	if err != nil {
		return parsedAct024Post{}, err
	}
	segs := site.SplitCleanPath(u.Path)
	out := parsedAct024Post{Item: item}

	switch {
	case len(segs) >= 4 && strings.EqualFold(segs[0], "i") && strings.EqualFold(segs[1], "web") &&
		(strings.EqualFold(segs[2], "status") || strings.EqualFold(segs[2], "statuses")):
		sf, err := x.ParseSnowflakeID(segs[3])
		if err != nil {
			return parsedAct024Post{}, errors.New("invalid /i/web/status/ id")
		}
		out.StatusID = sf.ID
		return out, nil

	case len(segs) >= 3 && strings.EqualFold(segs[0], "i") &&
		(strings.EqualFold(segs[1], "status") || strings.EqualFold(segs[1], "statuses")):
		sf, err := x.ParseSnowflakeID(segs[2])
		if err != nil {
			return parsedAct024Post{}, errors.New("invalid /i/status/ id")
		}
		out.StatusID = sf.ID
		return out, nil

	case len(segs) >= 3 && (strings.EqualFold(segs[1], "status") || strings.EqualFold(segs[1], "statuses")):
		sf, err := x.ParseSnowflakeID(segs[2])
		if err != nil {
			return parsedAct024Post{}, errors.New("invalid /<handle>/status/ id")
		}
		out.StatusID = sf.ID
		if reAct024ScreenName.MatchString(segs[0]) && !strings.EqualFold(segs[0], "i") {
			out.ScreenName = segs[0]
		}
		return out, nil
	}
	return parsedAct024Post{}, errors.New("unrecognized X post URL form")
}

func validateAct024ShortLinkURL(raw string, allowLocalhost bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("invalid short link URL")
	}
	host := strings.ToLower(u.Hostname())
	segs := site.SplitCleanPath(u.Path)
	if host == "t.co" {
		if len(segs) != 1 || !reAct024ShortCode.MatchString(segs[0]) {
			return errors.New("invalid t.co short code")
		}
		return nil
	}
	if allowLocalhost && site.IsLoopbackHost(host) && len(segs) >= 2 && strings.EqualFold(segs[0], "tco") && reAct024ShortCode.MatchString(segs[1]) {
		return nil
	}
	return errors.New("shortLink must be a t.co URL")
}

// selectAct024DetailPosts chooses up to max (3) posts for TweetDetail, prioritizing
// video, deletedOrigin, and tcoLink (matching the ACT-024 authorization) before
// longText, thread, mixed, gif, images, poll, card, and remaining posts.
func selectAct024DetailPosts(posts []parsedAct024Post, maxCount int) []parsedAct024Post {
	if maxCount <= 0 || len(posts) == 0 {
		return nil
	}
	priorities := []string{
		"video",
		"deletedOrigin",
		"tcoLink",
		"longText",
		"thread",
		"mixed",
		"gif",
		"images",
		"poll",
		"card",
		"quote",
		"retweet",
		"retweetOrQuote",
	}
	picked := make([]parsedAct024Post, 0, min(maxCount, len(posts)))
	seen := make(map[string]bool, len(posts))
	addPost := func(pp parsedAct024Post) bool {
		if pp.StatusID == "" || seen[pp.StatusID] {
			return false
		}
		seen[pp.StatusID] = true
		picked = append(picked, pp)
		return len(picked) >= maxCount
	}

	for _, tag := range priorities {
		for _, pp := range posts {
			if hasTag(pp.Item.Expect, tag) && !seen[pp.StatusID] {
				if addPost(pp) {
					return picked
				}
				break
			}
		}
	}
	for _, pp := range posts {
		if addPost(pp) {
			return picked
		}
	}
	return picked
}

// readAndValidateXCookie reads TRACE_TEST_X_COOKIE_FILE from the process environment
// first, falling back to <root>/.env.local, and validates via credential.ParseXCookieInput.
func readAndValidateXCookie(root string) (string, map[string]string, error) {
	var cookieStr string
	if envPath := strings.Trim(strings.TrimSpace(os.Getenv("TRACE_TEST_X_COOKIE_FILE")), `"'`); envPath != "" {
		if !filepath.IsAbs(envPath) {
			envPath = filepath.Join(root, envPath)
		}
		raw, err := os.ReadFile(envPath)
		if err != nil {
			return "", nil, errors.New("cookie: TRACE_TEST_X_COOKIE_FILE unreadable")
		}
		cookieStr = strings.TrimSpace(string(raw))
	} else {
		var err error
		cookieStr, err = readEnvCookieFile(root, "TRACE_TEST_X_COOKIE_FILE")
		if err != nil {
			return "", nil, errors.New("cookie: " + err.Error())
		}
	}
	cleaned, err := credential.ParseXCookieInput(cookieStr)
	if err != nil {
		return "", nil, errors.New("cookie: X test Cookie missing auth_token/ct0, contains control chars, or exceeds 64 KiB / 32 KiB cleaned limit")
	}
	return cookieStr, cleaned, nil
}

// act024Redactor tracks all secrets, identifiers, handles, URLs, and post text
// snippets encountered during ACT-024 so none can ever enter error strings or
// the serialized act024.json receipt.
type act024Redactor struct {
	mu      sync.Mutex
	secrets map[string]bool // cookies, ct0, bearer, verificationKey, queryIds, mismatched ct0
	ids     map[string]bool // rest_id, status_id, t.co codes
	handles map[string]bool // screen_name, display name
	urls    map[string]bool // target and runtime URLs
	texts   map[string]bool // distinctive post text snippets
}

func newAct024Redactor() *act024Redactor {
	return &act024Redactor{
		secrets: make(map[string]bool),
		ids:     make(map[string]bool),
		handles: make(map[string]bool),
		urls:    make(map[string]bool),
		texts:   make(map[string]bool),
	}
}

func isSecretXCookie(key, val string) bool {
	k := strings.TrimSpace(key)
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if k == "" || len(v) < 6 {
		return false
	}
	switch strings.ToLower(v) {
	case "true", "false", "null", "none", "undefined":
		return false
	}
	for _, allowed := range credential.AllowedXCookieKeys {
		if k == allowed {
			return true
		}
	}
	return len(v) >= 8
}

func (r *act024Redactor) addCookieHeader(raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, part := range strings.Split(raw, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if isSecretXCookie(k, v) {
				r.secrets[v] = true
			}
		}
	}
}

func (r *act024Redactor) addCookieMap(m map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range m {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if isSecretXCookie(k, v) {
			r.secrets[v] = true
		}
		if k == "twid" {
			if restID, err := credential.ParseXTWID(v); err == nil && len(restID) >= 5 {
				r.ids[restID] = true
			}
		}
	}
}

func (r *act024Redactor) addSecrets(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) >= 6 {
			r.secrets[v] = true
		}
	}
}

func (r *act024Redactor) addIDs(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) >= 5 && v != "0" {
			r.ids[v] = true
		}
	}
}

func (r *act024Redactor) addHandles(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "@"))
		if isTrackableHandle(v) {
			r.handles[v] = true
		}
	}
}

func (r *act024Redactor) addURLs(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			r.urls[v] = true
		}
	}
}

func (r *act024Redactor) addTexts(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, raw := range vals {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		runes := []rune(t)
		if len(runes) >= 8 {
			if len(runes) > 36 {
				r.texts[string(runes[:36])] = true
			} else {
				r.texts[t] = true
			}
		}
	}
}

func (r *act024Redactor) addTargets(
	tg act024Targets,
	users, protectedUsers []parsedAct024User,
	posts []parsedAct024Post,
) {
	for _, pu := range users {
		r.addURLs(pu.Item.URL)
		r.addIDs(pu.RestID)
		r.addHandles(pu.ScreenName)
	}
	for _, pu := range protectedUsers {
		r.addURLs(pu.Item.URL)
		r.addIDs(pu.RestID)
		r.addHandles(pu.ScreenName)
	}
	for _, pp := range posts {
		r.addURLs(pp.Item.URL)
		r.addIDs(pp.StatusID)
		r.addHandles(pp.ScreenName)
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
	if tg.Missing != nil {
		r.addIDs(tg.Missing.UserID, tg.Missing.StatusID)
	}
}

func (r *act024Redactor) addFrontendMetadata(meta *x.FrontendMetadata) {
	if meta == nil {
		return
	}
	r.addSecrets(meta.Bearer, meta.TxIDMaterial.VerificationKey)
	r.addURLs(meta.MainJSURL, meta.OndemandURL)
	r.addURLs(meta.FetchedLazyChunkURLs()...)
	for _, opName := range x.OptionalOperations() {
		r.addURLs(meta.LazyChunkCandidates(opName)...)
	}
	for _, op := range meta.Operations {
		r.addSecrets(op.QueryID)
	}
}

func (r *act024Redactor) addXUserProfile(prof *x.UserProfile) {
	if prof == nil {
		return
	}
	r.addIDs(prof.RestID)
	r.addIDs(prof.PinnedTweetIDs...)
	r.addHandles(prof.ScreenName, prof.Name)
	r.addURLs(prof.CanonicalURL, prof.AvatarURL, prof.BannerURL)
	r.addTexts(prof.Description)
}

func (r *act024Redactor) addXFollowingUser(u x.FollowingUser) {
	r.addIDs(u.RestID)
	r.addHandles(u.ScreenName, u.Name)
	r.addURLs(u.AvatarURL)
	r.addTexts(u.Description)
}

func (r *act024Redactor) addXPost(p *x.Post) {
	if p == nil {
		return
	}
	r.addIDs(p.ID, p.ConversationID, p.InReplyToStatusID, p.InReplyToUserID, p.Author.RestID)
	r.addHandles(p.Author.ScreenName, p.Author.Name, p.InReplyToScreenName)
	r.addURLs(p.CanonicalURL, p.Author.AvatarURL)
	r.addTexts(p.Text, p.TextRaw)
	for _, ue := range p.URLs {
		r.addURLs(ue.ShortURL, ue.ExpandedURL)
	}
	for _, m := range p.Media {
		r.addURLs(m.PublicPreviewURL, m.ImageURL, m.VideoURL, m.HLSURL)
	}
	if p.Retweeted != nil {
		r.addXPost(p.Retweeted)
	}
	if p.Quoted != nil {
		r.addXPost(p.Quoted)
	}
	for i := range p.ThreadAncestors {
		r.addXPost(&p.ThreadAncestors[i])
	}
	for i := range p.Thread {
		r.addXPost(&p.Thread[i])
	}
}

func (r *act024Redactor) containsSecretBytes(raw []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sec := range r.secrets {
		if len(sec) >= 6 && bytes.Contains(raw, []byte(sec)) {
			return true
		}
	}
	return false
}

func (r *act024Redactor) scrubError(msg string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := msg
	for sec := range r.secrets {
		out = strings.ReplaceAll(out, sec, "[REDACTED]")
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
	for tx := range r.texts {
		if len(tx) >= 8 {
			out = strings.ReplaceAll(out, tx, "[TEXT]")
		}
	}
	out = redactURLs(out)
	if runes := []rune(out); len(runes) > 180 {
		out = string(runes[:180])
	}
	return out
}

// act024ReceiptSafeEnums lists fixed schema enum/category/operation tokens emitted
// in act024.json so a followed user whose display name happens to equal "Video" or
// "Active" cannot cause a false-positive leak rejection on a clean receipt.
var act024ReceiptSafeEnums = map[string]bool{
	"act-024": true, "act024": true, "act-025": true, "act025": true,
	"verified": true, "not-run": true, "failed": true, "unavailable": true,
	"pass": true, "partial": true, "stopped": true, "fail": true,
	"webpack": true, "vite": true, "unknown": true,
	"video": true, "photo": true, "gif": true, "draw": true, "word": true, "forward": true,
	"active": true, "pinned": true, "retweet": true, "quote": true, "retweetorquote": true,
	"longtext": true, "thread": true, "mixed": true, "images": true, "poll": true,
	"card": true, "protected": true, "deletedorigin": true, "tcolink": true,
	"vote": true, "webpage": true, "article": true, "live": true,
	"frontend": true, "login": true, "shortlink": true, "unauthcontrol": true,
	"postdetail": true, "usertimeline": true, "errorprobe": true, "mine": true,
	"media": true, "csrfreplay": true, "api": true,
	"home_html": true, "main_js": true, "ondemand_js": true, "static_js": true, "accountsettings": true,
	"userbyscreenname": true, "userbyrestid": true, "usertweets": true,
	"tweetdetail": true, "following": true, "bookmarks": true, "likes": true,
	"bookmarkfoldersslice": true,
	"manual":               true, "completed": true, "dynamic": true, "cover": true,
	"targets": true, "post_entity": true, "https": true, "http": true,
	"jpeg": true, "png": true, "webp": true, "mp4_or_m4a": true, "other": true,
	"auth_required": true, "rate_limited": true, "risk_control": true,
	"forbidden": true, "blocked": true, "schema_changed": true, "bad_request": true,
	"network": true, "quota": true, "unsupported_link": true,
}

// act024HandleExemptMetadataKeys lists fixed-schema receipt metadata keys whose
// values are controlled enum/step/MIME/host-class/codec/timestamp/diagnostic
// tokens rather than user handles or display names. Exempting these keys from
// the loose r.handles substring check prevents real X accounts named "Twitter",
// "Image", "Profile", "Timeline", etc. from falsely blocking act024.json
// persistence while ScanTextForSecrets, raw URL checks, r.secrets, r.urls,
// r.ids, and r.texts remain enforced across the entire JSON.
var act024HandleExemptMetadataKeys = map[string]bool{
	"act":             true,
	"actId":           true,
	"commit":          true,
	"startedAt":       true,
	"finishedAt":      true,
	"updatedAt":       true,
	"name":            true,
	"reason":          true,
	"shortLinkReason": true,
	"contentType":     true,
	"hostClass":       true,
	"landedHostClass": true,
	"targetHostClass": true,
	"codecs":          true,
	"stoppedBy":       true,
	"errorKind":       true,
	"errKind":         true,
	"cardKind":        true,
}

func (r *act024Redactor) verifyJSONClean(rawJSON string) error {
	if findings := sitecheck.ScanTextForSecrets(rawJSON); len(findings) > 0 {
		return fmt.Errorf("receipt failed secret scan: %s", findings[0])
	}
	if strings.Contains(rawJSON, "http://") || strings.Contains(rawJSON, "https://") {
		return errors.New("receipt contains a raw URL; not written")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for sec := range r.secrets {
		if strings.Contains(rawJSON, sec) {
			return errors.New("receipt contains raw secret/token value; not written")
		}
	}
	for u := range r.urls {
		if strings.Contains(rawJSON, u) {
			return errors.New("receipt contains target or runtime URL; not written")
		}
	}
	for id := range r.ids {
		if strings.Contains(rawJSON, `"`+id+`"`) {
			return errors.New("receipt contains target or runtime identifier; not written")
		}
		if (!reAct024Digits.MatchString(id) && len(id) >= 5) || len(id) >= 7 {
			if strings.Contains(rawJSON, id) {
				return errors.New("receipt contains target or runtime identifier substring; not written")
			}
		}
	}
	for tx := range r.texts {
		if strings.Contains(rawJSON, tx) {
			return errors.New("receipt contains post text snippet; not written")
		}
	}

	// Inspect every JSON string VALUE (ignoring JSON schema keys) for leaked handles/names.
	var parsed any
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		return fmt.Errorf("receipt JSON re-parse failed: %w", err)
	}
	var leakErr error
	var walkValues func(pathKey string, v any)
	walkValues = func(pathKey string, v any) {
		if leakErr != nil {
			return
		}
		switch node := v.(type) {
		case map[string]any:
			for k, val := range node {
				walkValues(k, val)
			}
		case []any:
			for _, elem := range node {
				walkValues(pathKey, elem)
			}
		case string:
			trimmed := strings.TrimSpace(node)
			if trimmed == "" {
				return
			}
			if act024HandleExemptMetadataKeys[pathKey] {
				return
			}
			lowerVal := strings.ToLower(trimmed)
			isFreeTextField := pathKey == "error" || pathKey == "notes"
			if !isFreeTextField && act024ReceiptSafeEnums[lowerVal] {
				return
			}
			for h := range r.handles {
				if !isTrackableHandle(h) {
					continue
				}
				lowerH := strings.ToLower(h)
				if !isFreeTextField && act024ReceiptSafeEnums[lowerH] {
					continue
				}
				runes := utf8.RuneCountInString(h)
				isASCII := len(h) == runes
				if isASCII && len(h) < 5 {
					if strings.EqualFold(trimmed, h) {
						leakErr = errors.New("receipt contains account screen_name or display name; not written")
						return
					}
					continue
				}
				if isASCII {
					if strings.Contains(lowerVal, lowerH) {
						leakErr = errors.New("receipt contains account screen_name or display name; not written")
						return
					}
				} else if strings.Contains(trimmed, h) {
					leakErr = errors.New("receipt contains account screen_name or display name; not written")
					return
				}
			}
		}
	}
	walkValues("", parsed)
	return leakErr
}

type act024ReqKind int

const (
	act024KindFrontendHome act024ReqKind = iota
	act024KindFrontendScript
	act024KindAPI
	act024KindShortLink
	act024KindMedia
	act024KindUnlisted
)

type act024RateLimitSnapshot struct {
	Limit        int   `json:"limit,omitempty"`
	HasLimit     bool  `json:"hasLimit,omitempty"`
	Remaining    int   `json:"remaining"`
	HasRemaining bool  `json:"hasRemaining"`
	Reset        int64 `json:"reset,omitempty"`
	HasReset     bool  `json:"hasReset,omitempty"`
}

type act024APITelemetry struct {
	HTTPStatus           int
	ErrorCodes           []int
	BodyBytes            int
	RateLimit            act024RateLimitSnapshot
	SetCookieCT0Present  bool
	SetCookieCT0Valid    bool
	SetCookieCT0Changed  bool
	IsFakeEmptyUserShell bool
}

type act024FrontendTelemetry struct {
	HomeHTTPStatus          int
	HomeBytes               int
	HomeHasVerificationMeta bool
	HomeHasLoadingSVG       bool
	HomeHasViteModule       bool
	MainJSBytes             int
	MainJSBundleKind        string
	OndemandJSBytes         int
	LazyChunkCount          int
	LazyChunkBytes          int
}

type act024Checker struct {
	actID          string
	gate           *sitecheck.Gate
	apiLimits      map[string]int
	shortLink      *mediaBudget
	media          *mediaBudget
	minSpacing     time.Duration
	jitterMax      time.Duration
	allowLocalhost bool
	endpoints      x.Endpoints
	redactor       *act024Redactor

	guestControlActive          atomic.Bool
	expectedCSRFCtrlActive      atomic.Bool
	expectedBusinessErrorActive atomic.Bool
	optionalFolderProbeActive   atomic.Bool
	pendingCSRFReplay           atomic.Bool

	unexpected353Count atomic.Int32
	txidSentCount      atomic.Int64
	mediaHTTP206Count  atomic.Int64
	passiveCT0Rotated  atomic.Bool
	frontendBytes      atomic.Int64

	phaseMu sync.RWMutex
	phase   string

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu                     sync.Mutex
	stopped                string
	latestCT0              string
	statuses               map[string]int
	opCounts               map[string]int
	rateLimits             map[string]act024RateLimitSnapshot
	lastTelemetry          act024APITelemetry
	frontendTel            act024FrontendTelemetry
	bookmarkFoldersSpec    *x.GraphQLOperationSpec
	redirects              []map[string]any
	cdnHostClasses         map[string]int
	cookieOnCDNHit         bool
	refererOnCDNHit        bool
	cookieOnStaticAssetHit bool
	cookieOnShortLinkHit   bool
}

func (c *act024Checker) setPhase(phase string) {
	c.phaseMu.Lock()
	c.phase = phase
	c.phaseMu.Unlock()
}

func (c *act024Checker) currentPhase() string {
	c.phaseMu.RLock()
	defer c.phaseMu.RUnlock()
	return c.phase
}

func (c *act024Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act024Checker) tripStop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act024Checker) setLatestCT0(v string) {
	if !credential.IsValidXCT0(v) {
		return
	}
	c.mu.Lock()
	c.latestCT0 = v
	c.mu.Unlock()
}

func (c *act024Checker) getLatestCT0() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latestCT0
}

func (c *act024Checker) snapshotLastTelemetry() act024APITelemetry {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := c.lastTelemetry
	cp.ErrorCodes = append([]int(nil), c.lastTelemetry.ErrorCodes...)
	return cp
}

func (c *act024Checker) snapshotFrontendTelemetry() act024FrontendTelemetry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frontendTel
}

func (c *act024Checker) getBookmarkFoldersSpec() *x.GraphQLOperationSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bookmarkFoldersSpec == nil {
		return nil
	}
	cp := *c.bookmarkFoldersSpec
	if c.bookmarkFoldersSpec.Features != nil {
		cp.Features = make(map[string]bool, len(c.bookmarkFoldersSpec.Features))
		for k, v := range c.bookmarkFoldersSpec.Features {
			cp.Features[k] = v
		}
	}
	return &cp
}

func (c *act024Checker) snapshotCounts() (ops map[string]int, statuses map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ops = make(map[string]int, len(c.opCounts))
	for k, v := range c.opCounts {
		ops[k] = v
	}
	statuses = make(map[string]int, len(c.statuses))
	for k, v := range c.statuses {
		statuses[k] = v
	}
	return ops, statuses
}

func (c *act024Checker) rateLimitSnapshots() map[string]act024RateLimitSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rateLimits) == 0 {
		return nil
	}
	out := make(map[string]act024RateLimitSnapshot, len(c.rateLimits))
	for k, v := range c.rateLimits {
		out[k] = v
	}
	return out
}

func (c *act024Checker) redirectShapes() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.redirects...)
}

func (c *act024Checker) cdnHostClassCounts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cdnHostClasses) == 0 {
		return nil
	}
	out := make(map[string]int, len(c.cdnHostClasses))
	for k, v := range c.cdnHostClasses {
		out[k] = v
	}
	return out
}

func (c *act024Checker) canAffordMedia(minBytes int64, minReqs int) bool {
	if int(c.media.requests.Load())+minReqs > c.media.maxRequests {
		return false
	}
	if c.media.bytes.Load()+minBytes > c.media.maxBytes {
		return false
	}
	return true
}

func (c *act024Checker) pace(ctx context.Context) error {
	if c.minSpacing <= 0 {
		return nil
	}
	c.paceMu.Lock()
	defer c.paceMu.Unlock()
	if !c.lastReqAt.IsZero() {
		jitter := randomDurationRange(c.jitterMax)
		wait := (c.minSpacing + jitter) - time.Since(c.lastReqAt)
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

func randomDurationRange(maxDur time.Duration) time.Duration {
	if maxDur <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return 0
	}
	v := binary.LittleEndian.Uint64(b[:])
	maxNs := uint64(maxDur.Nanoseconds()) + 1
	return time.Duration(v % maxNs)
}

func (c *act024Checker) noteRedirect(cat string, r *http.Request, resp *http.Response) {
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
		switch cat {
		case "shortlink":
			shape["withinXAllowlist"] = act024AllowedMainHosts[host] || host == "t.co" || (c.allowLocalhost && site.IsLoopbackHost(host))
		default:
			shape["withinXAllowlist"] = site.MatchDomainSuffix(host, site.DefaultXMediaHosts()) || (c.allowLocalhost && site.IsLoopbackHost(host))
			if host == "video-s.twimg.com" {
				shape["targetIsVideoSTwimg"] = true
			}
		}
	} else {
		shape["targetMissing"] = true
	}
	c.mu.Lock()
	if len(c.redirects) < 12 {
		c.redirects = append(c.redirects, shape)
	}
	c.mu.Unlock()
}

// isForbiddenXPathOrOp explicitly blocks SearchTimeline, live/Spaces, official API v2,
// login/onboarding flows, and any mutating/write operations per the ACT-024 authorization.
func isForbiddenXPathOrOp(method, p string) bool {
	if !strings.EqualFold(method, http.MethodGet) {
		return true
	}
	lp := strings.ToLower(p)
	forbiddenSubstrings := []string{
		"/2/",
		"/i/flow/login",
		"/1.1/onboarding",
		"/onboarding/",
		"/live_video_stream",
		"/fleets/",
		"searchtimeline",
		"broadcastquery",
		"audiospacebyid",
		"usermedia",
		"usertweetsandreplies",
		"tweetresultbyrestid",
		"createtweet",
		"deletetweet",
		"favoritetweet",
		"unfavoritetweet",
		"createretweet",
		"deleteretweet",
		"createbookmark",
		"deletebookmark",
		"/friendships/create",
		"/friendships/destroy",
	}
	for _, sub := range forbiddenSubstrings {
		if strings.Contains(lp, sub) {
			return true
		}
	}
	return false
}

func (c *act024Checker) isHomeURL(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	home := strings.TrimSpace(c.endpoints.HomeURL)
	if home == "" {
		home = x.DefaultHomeURL
	}
	hu, err := url.Parse(home)
	if err != nil {
		return false
	}
	return strings.EqualFold(r.URL.Hostname(), hu.Hostname()) && r.URL.Path == hu.Path
}

func (c *act024Checker) isAccountSettingsURL(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	ep := strings.TrimSpace(c.endpoints.AccountSettings)
	if ep == "" {
		ep = x.DefaultAccountSettingsURL
	}
	eu, err := url.Parse(ep)
	if err == nil && strings.EqualFold(r.URL.Hostname(), eu.Hostname()) && r.URL.Path == eu.Path {
		return true
	}
	return strings.HasSuffix(r.URL.Path, "/1.1/account/settings.json")
}

func (c *act024Checker) extractGraphQLOp(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	gqlBase := strings.TrimSpace(c.endpoints.GraphQLBase)
	if gqlBase == "" {
		gqlBase = x.DefaultGraphQLBaseURL
	}
	basePath := "/i/api/graphql"
	if bu, err := url.Parse(gqlBase); err == nil && bu.Path != "" {
		basePath = strings.TrimRight(bu.Path, "/")
	}
	prefix := basePath + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}

func (c *act024Checker) classifyAPI(r *http.Request) (kind act024ReqKind, cat string, safeOp string) {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	host := strings.ToLower(r.URL.Hostname())
	p := r.URL.Path
	phase := c.currentPhase()

	if isForbiddenXPathOrOp(method, p) {
		return act024KindUnlisted, "unlisted", "unlisted"
	}

	// 1. Short-link expansion (t.co or localhost /tco/* during shortlink phase).
	if phase == "shortlink" {
		if host == "t.co" || (c.allowLocalhost && site.IsLoopbackHost(host) && strings.HasPrefix(strings.ToLower(p), "/tco/")) {
			return act024KindShortLink, "shortlink", "t.co"
		}
		return act024KindUnlisted, "unlisted", "unlisted"
	}

	// 2. Frontend discovery resources: x.com/home + abs.twimg.com/responsive-web/*.js
	if c.isHomeURL(r) || ((host == "x.com" || host == "www.x.com") && p == "/home") {
		if phase == "frontend" || phase == "login" {
			return act024KindFrontendHome, "frontend", "home_html"
		}
		return act024KindUnlisted, "unlisted", "home_html"
	}
	if strings.HasPrefix(p, x.StaticAssetPathPrefix) &&
		(host == x.StaticAssetHost || (c.allowLocalhost && site.IsLoopbackHost(host))) {
		if phase == "frontend" || phase == "login" || phase == "mine" {
			base := path.Base(p)
			switch {
			case reAct024MainBundleFile.MatchString(base):
				return act024KindFrontendScript, "frontend", "main_js"
			case reAct024OndemandFile.MatchString(base):
				return act024KindFrontendScript, "frontend", "ondemand_js"
			case strings.HasSuffix(strings.ToLower(base), ".js"):
				return act024KindFrontendScript, "frontend", "static_js"
			}
		}
		return act024KindUnlisted, "unlisted", "static_js"
	}

	// 3. REST login confirmation & V-02 CSRF control (/i/api/1.1/account/settings.json).
	if c.isAccountSettingsURL(r) {
		if phase == "login" {
			return act024KindAPI, "login", x.OpAccountSettings
		}
		return act024KindUnlisted, "unlisted", x.OpAccountSettings
	}

	// 4. Allowlisted read-only GraphQL operations, gated strictly by current phase.
	if opName, ok := c.extractGraphQLOp(r); ok {
		allowed := false
		switch opName {
		case x.OpUserByScreenName:
			allowed = phase == "login" || phase == "userTimeline" || phase == "errorProbe"
		case x.OpUserByRestId:
			allowed = phase == "userTimeline" || phase == "errorProbe" || phase == "unauthControl"
		case x.OpUserTweets:
			allowed = phase == "userTimeline" || phase == "unauthControl"
		case x.OpTweetDetail:
			allowed = phase == "postDetail" || phase == "errorProbe" || phase == "unauthControl"
		case x.OpFollowing, x.OpBookmarks, x.OpLikes:
			allowed = phase == "mine"
		case act024OpBookmarkFolders:
			allowed = phase == "mine" && c.optionalFolderProbeActive.Load()
		}
		if allowed {
			return act024KindAPI, phase, opName
		}
		return act024KindUnlisted, "unlisted", opName
	}

	return act024KindUnlisted, "unlisted", "unlisted"
}

func (c *act024Checker) recordRateLimit(safeOp string, hdr http.Header) (act024RateLimitSnapshot, bool) {
	parsed := x.ParseRateLimitHeaders(hdr)
	snap := act024RateLimitSnapshot{
		Limit:        parsed.Limit,
		HasLimit:     parsed.HasLimit,
		Remaining:    parsed.Remaining,
		HasRemaining: parsed.HasRemaining,
		Reset:        parsed.ResetUnix,
		HasReset:     parsed.HasReset,
	}
	if snap.HasLimit || snap.HasRemaining || snap.HasReset {
		c.mu.Lock()
		c.rateLimits[safeOp] = snap
		c.mu.Unlock()
	}
	if snap.HasRemaining && snap.Remaining <= 1 {
		c.tripStop("rate_limit_remaining_low")
		return snap, true
	}
	return snap, false
}

func (c *act024Checker) labelID() string {
	if c != nil && strings.TrimSpace(c.actID) != "" {
		return c.actID
	}
	return act024ID
}

// wrap gates all x.Client traffic (frontend discovery, REST/GraphQL API, t.co shortlinks,
// and single 353 replay) before any upstream request is sent. Background avatar fetches
// on x.Client.mediaClient fall into act024KindUnlisted and are rejected with 0 network calls.
func (c *act024Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("%s halted (%s)", c.labelID(), reason)
		}

		kind, cat, safeOp := c.classifyAPI(r)
		switch kind {
		case act024KindUnlisted:
			return nil, fmt.Errorf("%w: endpoint or method outside %s authorization", sitecheck.ErrQuotaExceeded, strings.ToUpper(c.labelID()))

		case act024KindFrontendScript:
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				c.mu.Lock()
				c.cookieOnStaticAssetHit = true
				c.mu.Unlock()
				return nil, fmt.Errorf("%s: refusing to send auth headers to abs.twimg.com script", c.labelID())
			}
			if err := checkAPICategoryAllowed(c.apiLimits, "frontend"); err != nil {
				return nil, err
			}
			if err := c.gate.AcquireAPI("frontend"); err != nil {
				return nil, err
			}

		case act024KindFrontendHome:
			if err := checkAPICategoryAllowed(c.apiLimits, "frontend"); err != nil {
				return nil, err
			}
			if err := c.gate.AcquireAPI("frontend"); err != nil {
				return nil, err
			}

		case act024KindShortLink:
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				c.mu.Lock()
				c.cookieOnShortLinkHit = true
				c.mu.Unlock()
				return nil, errors.New("act024: refusing to send auth headers to t.co shortlink")
			}
			if int(c.shortLink.requests.Load()) >= c.shortLink.maxRequests {
				return nil, fmt.Errorf("%w: shortlink hops exhausted (%d)", sitecheck.ErrQuotaExceeded, c.shortLink.maxRequests)
			}
			if err := checkAPICategoryAllowed(c.apiLimits, "shortlink"); err != nil {
				return nil, err
			}
			if err := c.gate.AcquireAPI("shortlink"); err != nil {
				return nil, err
			}
			if err := c.shortLink.acquire(); err != nil {
				return nil, err
			}

		case act024KindAPI:
			chargeCat := cat
			if c.pendingCSRFReplay.Swap(false) {
				chargeCat = "csrfReplay"
			}
			if err := checkAPICategoryAllowed(c.apiLimits, chargeCat); err != nil {
				return nil, err
			}
			if err := c.gate.AcquireAPI(chargeCat); err != nil {
				return nil, err
			}
			cat = chargeCat
		}

		if r.Header.Get("x-client-transaction-id") != "" {
			c.txidSentCount.Add(1)
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
		c.opCounts[safeOp]++
		c.mu.Unlock()

		// Stop check 1: ANY response header x-rate-limit-remaining <= 1 halts immediately!
		rlSnap, rlStopped := c.recordRateLimit(safeOp, resp.Header)

		// Stop check 2: HTTP 429 always halts immediately!
		if resp.StatusCode == http.StatusTooManyRequests {
			c.tripStop("http_429")
		}

		switch kind {
		case act024KindShortLink:
			if resp.StatusCode == http.StatusForbidden {
				c.tripStop("http_403_shortlink")
			}
			if isMediaRedirect(resp) {
				c.noteRedirect("shortlink", r, resp)
				loc := resp.Header.Get("Location")
				if isXChallengeRedirectLocation(loc) {
					c.tripStop("challenge_redirect")
				}
			}
			resp.Body = countingBody{ReadCloser: resp.Body, budget: c.shortLink}
			return resp, nil

		case act024KindFrontendHome, act024KindFrontendScript:
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, act024MaxFrontendBytes+1))
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			if int64(len(raw)) > act024MaxFrontendBytes {
				return nil, fmt.Errorf("%w: frontend resource %s exceeded 8 MiB cap", sitecheck.ErrQuotaExceeded, safeOp)
			}
			c.frontendBytes.Add(int64(len(raw)))
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			c.inspectFrontendResponse(kind, safeOp, r, resp, raw, rlStopped)
			return resp, nil

		case act024KindAPI:
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, x.DefaultMaxBodyBytes+1))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if readErr != nil {
				return nil, readErr
			}
			c.inspectAPIResponse(safeOp, r, resp, raw, rlSnap)
			return resp, nil
		}

		return resp, nil
	})
}

// wrapMedia gates all formal CacheXPost and V-07 media CDN traffic (pbs.twimg.com,
// video.twimg.com, or loopback in tests), strictly enforcing cookieless, no-Referer,
// <= 5 requests, <= 25 MiB, total <= 32 requests, and all stop conditions.
func (c *act024Checker) wrapMedia(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("%s halted (%s)", c.labelID(), reason)
		}

		method := strings.ToUpper(strings.TrimSpace(r.Method))
		if method != http.MethodGet && method != http.MethodHead {
			return nil, fmt.Errorf("%w: media method %s not authorized", sitecheck.ErrQuotaExceeded, method)
		}
		if c.currentPhase() != "media" {
			return nil, fmt.Errorf("%w: media CDN request outside media phase", sitecheck.ErrQuotaExceeded)
		}

		host := strings.ToLower(r.URL.Hostname())
		allowedHost := site.MatchDomainSuffix(host, site.DefaultXMediaHosts()) ||
			(c.allowLocalhost && site.IsLoopbackHost(host))
		if !allowedHost {
			return nil, fmt.Errorf("%w: media host outside X allowlist", sitecheck.ErrQuotaExceeded)
		}

		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
			c.mu.Lock()
			c.cookieOnCDNHit = true
			c.mu.Unlock()
			return nil, errors.New("act024: refusing to send Cookie/auth headers to X media CDN")
		}
		if r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" {
			c.mu.Lock()
			c.refererOnCDNHit = true
			c.mu.Unlock()
			return nil, errors.New("act024: refusing to send Referer/Origin headers to X media CDN")
		}

		if c.media.bytes.Load() >= c.media.maxBytes {
			return nil, fmt.Errorf("%w: media bytes exhausted (%d)", sitecheck.ErrQuotaExceeded, c.media.maxBytes)
		}
		if int(c.media.requests.Load()) >= c.media.maxRequests {
			return nil, fmt.Errorf("%w: media requests exhausted (%d)", sitecheck.ErrQuotaExceeded, c.media.maxRequests)
		}
		if err := checkAPICategoryAllowed(c.apiLimits, "media"); err != nil {
			return nil, err
		}
		if err := c.gate.AcquireAPI("media"); err != nil {
			return nil, err
		}
		if err := c.media.acquire(); err != nil {
			return nil, err
		}

		if err := c.pace(r.Context()); err != nil {
			return nil, err
		}

		resp, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}

		hc := hostClass(host)
		c.mu.Lock()
		c.statuses[fmt.Sprintf("media:%d", resp.StatusCode)]++
		c.opCounts["media:"+hc]++
		if c.cdnHostClasses == nil {
			c.cdnHostClasses = make(map[string]int)
		}
		c.cdnHostClasses[hc]++
		c.mu.Unlock()

		_, _ = c.recordRateLimit("media", resp.Header)

		if resp.StatusCode == http.StatusTooManyRequests {
			c.tripStop("http_429")
			return resp, nil
		}
		if resp.StatusCode == http.StatusPartialContent {
			c.mediaHTTP206Count.Add(1)
		}
		if isMediaRedirect(resp) {
			c.noteRedirect("media", r, resp)
			loc := resp.Header.Get("Location")
			if isXChallengeRedirectLocation(loc) {
				c.tripStop("challenge_redirect")
			} else if isXLoginRedirectLocation(loc) {
				c.tripStop("login_redirect")
			}
			resp.Body = countingBody{ReadCloser: resp.Body, budget: c.media}
			return resp, nil
		}

		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if strings.Contains(ct, "text/html") {
			peek, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(peek))
			if isXChallengeHTML(peek) {
				c.tripStop("challenge_html_page")
			} else if isXLoginHTML(peek) {
				c.tripStop("login_redirect")
			} else if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusForbidden {
				c.tripStop("challenge_html_page")
			}
			return resp, nil
		}
		if resp.StatusCode == http.StatusForbidden {
			peek, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(peek))
			errs := x.ExtractXErrors(peek)
			switch {
			case x.HasErrorCode(errs, 64):
				c.tripStop("x_error_64")
			case x.HasErrorCode(errs, 226):
				c.tripStop("x_error_226")
			case x.HasErrorCode(errs, 326):
				c.tripStop("x_error_326")
			case isXChallengeHTML(peek):
				c.tripStop("challenge_html_page")
			default:
				c.tripStop("http_403_cdn")
			}
			return resp, nil
		}

		resp.Body = countingBody{ReadCloser: resp.Body, budget: c.media}
		return resp, nil
	})
}

func (c *act024Checker) inspectFrontendResponse(
	kind act024ReqKind,
	safeOp string,
	r *http.Request,
	resp *http.Response,
	raw []byte,
	_ bool,
) {
	if kind == act024KindFrontendHome {
		if newCT0, ok := credential.ExtractXCT0FromSetCookies(resp.Cookies()); ok {
			if c.redactor != nil {
				c.redactor.addSecrets(newCT0)
			}
			if newCT0 != c.getLatestCT0() {
				c.passiveCT0Rotated.Store(true)
				c.setLatestCT0(newCT0)
			}
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			c.noteRedirect("frontend", r, resp)
			if isXChallengeRedirectLocation(loc) {
				c.tripStop("challenge_redirect")
			} else if isXLoginRedirectLocation(loc) {
				c.tripStop("login_redirect")
			}
			return
		}
		if resp.StatusCode == http.StatusUnauthorized {
			c.tripStop("auth_401")
			return
		}
		if resp.StatusCode == http.StatusForbidden {
			errs := x.ExtractXErrors(raw)
			switch {
			case x.HasErrorCode(errs, 64):
				c.tripStop("x_error_64")
			case x.HasErrorCode(errs, 226):
				c.tripStop("x_error_226")
			case x.HasErrorCode(errs, 326):
				c.tripStop("x_error_326")
			case isXChallengeHTML(raw):
				c.tripStop("challenge_html_page")
			default:
				c.tripStop("http_403")
			}
			return
		}
		lower := strings.ToLower(string(raw))
		hasVerifyMeta := strings.Contains(lower, "twitter-site-verification")
		hasLoadingSVG := strings.Contains(lower, "loading-x-anim-0")
		hasViteModule := strings.Contains(lower, `type="module"`) || strings.Contains(lower, `type='module'`) || strings.Contains(lower, "/x-web/")

		c.mu.Lock()
		c.frontendTel.HomeHTTPStatus = resp.StatusCode
		c.frontendTel.HomeBytes = len(raw)
		c.frontendTel.HomeHasVerificationMeta = hasVerifyMeta
		c.frontendTel.HomeHasLoadingSVG = hasLoadingSVG
		c.frontendTel.HomeHasViteModule = hasViteModule
		c.mu.Unlock()

		// Per x.md §15.2 preflight: normal x.com/home HTTP 200 has twitter-site-verification
		// even when Cloudflare scripts are embedded; only flag challenge/login HTML when absent.
		if !hasVerifyMeta {
			if isXChallengeHTML(raw) {
				c.tripStop("challenge_html_page")
				return
			}
			if isXLoginHTML(raw) {
				c.tripStop("login_redirect")
				return
			}
		}
		return
	}

	// Frontend script (main.*.js or ondemand.s.*.js)
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") && isXChallengeHTML(raw) {
		c.tripStop("challenge_html_page")
		return
	}
	if resp.StatusCode == http.StatusForbidden && isXChallengeHTML(raw) {
		c.tripStop("challenge_html_page")
		return
	}
	switch safeOp {
	case "main_js":
		bundleKind := "webpack"
		lp := strings.ToLower(r.URL.Path)
		if strings.Contains(lp, "/x-web/") || strings.HasPrefix(strings.ToLower(path.Base(r.URL.Path)), "main-") {
			bundleKind = "vite"
		}
		bfSpec := extractOptionalBookmarkFoldersSpec(string(raw))
		if bfSpec != nil && c.redactor != nil {
			c.redactor.addSecrets(bfSpec.QueryID)
		}
		c.mu.Lock()
		c.frontendTel.MainJSBytes = len(raw)
		c.frontendTel.MainJSBundleKind = bundleKind
		c.bookmarkFoldersSpec = bfSpec
		c.mu.Unlock()
	case "ondemand_js":
		c.mu.Lock()
		c.frontendTel.OndemandJSBytes = len(raw)
		c.mu.Unlock()
	case "static_js":
		bfSpec := extractOptionalBookmarkFoldersSpec(string(raw))
		if bfSpec != nil && c.redactor != nil {
			c.redactor.addSecrets(bfSpec.QueryID)
		}
		c.mu.Lock()
		c.frontendTel.LazyChunkCount++
		c.frontendTel.LazyChunkBytes += len(raw)
		if c.bookmarkFoldersSpec == nil && bfSpec != nil {
			c.bookmarkFoldersSpec = bfSpec
		}
		c.mu.Unlock()
	}
}

func (c *act024Checker) inspectAPIResponse(
	safeOp string,
	r *http.Request,
	resp *http.Response,
	raw []byte,
	rlSnap act024RateLimitSnapshot,
) {
	trimmed := bytes.TrimSpace(raw)
	isGuestControl := c.guestControlActive.Load() || r.Header.Get("Cookie") == ""
	isExpectedCSRF := c.expectedCSRFCtrlActive.Load()
	isExpectedBusiness := c.expectedBusinessErrorActive.Load()
	isOptionalFolder := c.optionalFolderProbeActive.Load() && safeOp == act024OpBookmarkFolders

	tel := act024APITelemetry{
		HTTPStatus: resp.StatusCode,
		BodyBytes:  len(raw),
		RateLimit:  rlSnap,
	}

	// Inspect Set-Cookie: ct0 rotation on every response.
	// On the intentional V-02 CSRF mismatch probe, x-csrf-token is badCT0, so compare
	// Set-Cookie: ct0 against the active session ct0 (c.getLatestCT0()) instead.
	baselineCT0 := strings.TrimSpace(r.Header.Get("x-csrf-token"))
	if isExpectedCSRF {
		baselineCT0 = c.getLatestCT0()
	}
	for _, sc := range resp.Cookies() {
		if sc != nil && sc.Name == "ct0" {
			tel.SetCookieCT0Present = true
			break
		}
	}
	var freshCT0 string
	if extractedCT0, ok := credential.ExtractXCT0FromSetCookies(resp.Cookies()); ok {
		tel.SetCookieCT0Valid = true
		if c.redactor != nil {
			c.redactor.addSecrets(extractedCT0)
		}
		if extractedCT0 != baselineCT0 {
			tel.SetCookieCT0Changed = true
			freshCT0 = extractedCT0
			if !isExpectedCSRF && !isGuestControl {
				c.passiveCT0Rotated.Store(true)
			}
		}
	}

	var errs []x.XAPIErrorItem
	if len(trimmed) > 0 && trimmed[0] == '{' {
		errs = x.ExtractXErrors(trimmed)
		for _, e := range errs {
			if e.HasCode {
				tel.ErrorCodes = append(tel.ErrorCodes, e.Code)
			}
		}
	}
	tel.IsFakeEmptyUserShell = isFakeEmptyXUserShell(trimmed)

	recordTel := func() {
		c.mu.Lock()
		c.lastTelemetry = tel
		c.mu.Unlock()
	}
	recordTel()

	// 1. Always-fatal X error codes across ALL statuses and modes: 88, 64, 226, 326.
	if x.HasErrorCode(errs, 88) {
		c.tripStop("x_error_88")
		return
	}
	if x.HasErrorCode(errs, 64) {
		c.tripStop("x_error_64")
		return
	}
	if x.HasErrorCode(errs, 226) {
		c.tripStop("x_error_226")
		return
	}
	if x.HasErrorCode(errs, 326) {
		c.tripStop("x_error_326")
		return
	}

	// 2. 3xx redirects on API endpoints (auto-redirect is disabled).
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		c.noteRedirect("api", r, resp)
		if isXChallengeRedirectLocation(loc) {
			c.tripStop("challenge_redirect")
			return
		}
		if isXLoginRedirectLocation(loc) {
			if !isGuestControl {
				c.tripStop("login_redirect")
			}
			return
		}
		if !isGuestControl {
			c.tripStop("unexpected_api_redirect")
		}
		return
	}

	// 3. HTTP 401 or auth error codes (32, 89, 215, 239) on authenticated requests.
	if resp.StatusCode == http.StatusUnauthorized ||
		x.HasErrorCode(errs, 32) || x.HasErrorCode(errs, 89) ||
		x.HasErrorCode(errs, 215) || x.HasErrorCode(errs, 239) {
		if !isGuestControl {
			switch {
			case x.HasErrorCode(errs, 32):
				c.tripStop("auth_error_32")
			case x.HasErrorCode(errs, 89):
				c.tripStop("auth_error_89")
			default:
				c.tripStop("auth_401")
			}
		}
		return
	}

	// 4. CSRF mismatch code 353 handling.
	if x.HasErrorCode(errs, 353) {
		if isExpectedCSRF {
			// Single controlled V-02 mismatch probe: record without stopping.
			return
		}
		if freshCT0 == "" {
			c.tripStop("csrf_353_no_new_ct0")
			return
		}
		prev := c.unexpected353Count.Add(1)
		if prev > 1 || c.gate.Usage().Categories["csrfReplay"] >= c.apiLimits["csrfReplay"] {
			c.tripStop("unexpected_second_353")
			return
		}
		// Allow x.Client's single automatic replay using the reserved csrfReplay (1/1) slot.
		c.pendingCSRFReplay.Store(true)
		return
	}

	// 5. Challenge / login HTML or non-JSON body check.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if len(trimmed) > 0 && (strings.Contains(ct, "text/html") || (trimmed[0] != '{' && trimmed[0] != '[')) {
		if isXChallengeHTML(trimmed) {
			c.tripStop("challenge_html_page")
			return
		}
		if isXLoginHTML(trimmed) {
			if !isGuestControl {
				c.tripStop("login_redirect")
			}
			return
		}
		if !isGuestControl && !isOptionalFolder && !isExpectedBusiness && !isExpectedCSRF {
			c.tripStop("non_json_api_body")
		}
		return
	}

	// 6. Unexpected HTTP 403 (after 64/226/326/353/HTML already checked above).
	if resp.StatusCode == http.StatusForbidden {
		if isGuestControl || isOptionalFolder || isExpectedCSRF {
			return
		}
		// Single-target protected/blocked codes (37, 136, 161, 179, 219) during errorProbe are expected.
		if isExpectedBusiness && (x.HasErrorCode(errs, 37) || x.HasErrorCode(errs, 136) ||
			x.HasErrorCode(errs, 161) || x.HasErrorCode(errs, 179) || x.HasErrorCode(errs, 219)) {
			return
		}
		c.tripStop("http_403")
		return
	}

	// 7. Fake empty user shell {"data":{"user":{}}} or {"data":{"user":{"result":{}}}}.
	if tel.IsFakeEmptyUserShell && !isGuestControl {
		c.tripStop("fake_empty_user_shell")
		return
	}

	// 8. Empty 0-byte body on HTTP 200 authenticated API response.
	if resp.StatusCode == http.StatusOK && len(trimmed) == 0 && !isGuestControl && !isOptionalFolder {
		c.tripStop("empty_api_body")
		return
	}
}

func isFakeEmptyXUserShell(trimmed []byte) bool {
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if dec.Decode(&top) != nil {
		return false
	}
	rawData := bytes.TrimSpace(top["data"])
	if len(rawData) == 0 || rawData[0] != '{' {
		return false
	}
	var dataMap map[string]json.RawMessage
	dDec := json.NewDecoder(bytes.NewReader(rawData))
	dDec.UseNumber()
	if dDec.Decode(&dataMap) != nil {
		return false
	}
	rawUser, hasUser := dataMap["user"]
	if !hasUser {
		return false
	}
	userTrim := bytes.TrimSpace(rawUser)
	if len(userTrim) == 0 || userTrim[0] != '{' {
		return false
	}
	var userMap map[string]json.RawMessage
	uDec := json.NewDecoder(bytes.NewReader(userTrim))
	uDec.UseNumber()
	if uDec.Decode(&userMap) != nil {
		return false
	}
	if len(userMap) == 0 {
		return true
	}
	if rawRes, hasRes := userMap["result"]; hasRes {
		resTrim := bytes.TrimSpace(rawRes)
		if len(resTrim) > 0 && resTrim[0] == '{' {
			var resMap map[string]json.RawMessage
			rDec := json.NewDecoder(bytes.NewReader(resTrim))
			rDec.UseNumber()
			if rDec.Decode(&resMap) == nil && len(resMap) == 0 {
				return true
			}
		}
	}
	return false
}

func isXChallengeRedirectLocation(rawLoc string) bool {
	loc := strings.ToLower(strings.TrimSpace(rawLoc))
	if loc == "" {
		return false
	}
	for _, m := range []string{"/account/access", "arkose", "captcha", "challenge", "funcaptcha", "/i/flow/consent", "turnstile"} {
		if strings.Contains(loc, m) {
			return true
		}
	}
	return false
}

func isXLoginRedirectLocation(rawLoc string) bool {
	loc := strings.ToLower(strings.TrimSpace(rawLoc))
	if loc == "" {
		return false
	}
	for _, m := range []string{"/i/flow/login", "/login", "/account/login", "/sessions", "redirect_after_login"} {
		if strings.Contains(loc, m) {
			return true
		}
	}
	return false
}

func isXChallengeHTML(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	snippet := body
	if len(snippet) > 16384 {
		snippet = snippet[:16384]
	}
	lower := strings.ToLower(string(snippet))
	for _, m := range []string{
		"cf-turnstile",
		"challenges.cloudflare.com",
		"challenge-platform",
		"just a moment",
		"checking your browser",
		"arkoselabs",
		"funcaptcha",
		"fc-token",
		"captcha",
		"/account/access",
		"account has been locked",
		"temporarily locked",
		"unusual login activity",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func isXLoginHTML(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	snippet := body
	if len(snippet) > 16384 {
		snippet = snippet[:16384]
	}
	lower := strings.ToLower(string(snippet))
	for _, m := range []string{
		"/i/flow/login",
		"sign in to x",
		"log in to x",
		"log in to twitter",
		"sign in to twitter",
		"login_redirect",
		"js-login-form",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// extractOptionalBookmarkFoldersSpec inspects the already-fetched main.*.js text in
// memory for BookmarkFoldersSlice (for optional V-08 folder probing within mine <= 5).
func extractOptionalBookmarkFoldersSpec(mainJS string) *x.GraphQLOperationSpec {
	loc := reAct024BookmarkFoldersOp.FindStringIndex(mainJS)
	if loc == nil {
		return nil
	}
	start := loc[0] - 512
	if start < 0 {
		start = 0
	}
	end := loc[1] + 1024
	if end > len(mainJS) {
		end = len(mainJS)
	}
	window := mainJS[start:end]
	qm := reAct024QueryIDField.FindStringSubmatch(window)
	if len(qm) != 2 {
		return nil
	}
	spec := &x.GraphQLOperationSpec{
		OperationName: act024OpBookmarkFolders,
		QueryID:       qm[1],
		Features:      make(map[string]bool),
	}
	if fm := reAct024FeatureSwitchList.FindStringSubmatch(window); len(fm) == 2 {
		for _, km := range reAct024QuotedString.FindAllStringSubmatch(fm[1], -1) {
			if len(km) == 2 {
				spec.Features[km[1]] = true
			}
		}
	}
	return spec
}

// makeMismatchedCT0 produces a syntactically valid ASCII alphanumeric token guaranteed
// to differ from currentCT0 for the controlled V-02 CSRF mismatch check.
func makeMismatchedCT0(currentCT0 string) string {
	candidate := "00000000000000000000000000000000"
	if strings.EqualFold(strings.TrimSpace(currentCT0), candidate) {
		candidate = "11111111111111111111111111111111"
	}
	return candidate
}

func formatCleanedXCookieHeader(cookies map[string]string) string {
	cleaned := credential.CleanXCookieMap(cookies)
	parts := make([]string, 0, len(credential.AllowedXCookieKeys))
	for _, k := range credential.AllowedXCookieKeys {
		if v := cleaned[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

func newXStepRunner(steps *[]step, ck *act024Checker, redactor *act024Redactor) *stepRunner {
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

func probeXImageAsset(
	ctx context.Context,
	client *http.Client,
	spec site.MediaTransportSpec,
	imgURL string,
	limitBytes int64,
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
	if u, uErr := url.Parse(imgURL); uErr == nil {
		out["hasNameOrigParam"] = strings.EqualFold(u.Query().Get("name"), "orig")
		out["hostClass"] = hostClass(u.Hostname())
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

	out["magicFormat"] = sniffWeiboAssetMagic(raw)
	out["bytes"] = len(raw)
	if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(raw)); cfgErr == nil {
		out["width"] = cfg.Width
		out["height"] = cfg.Height
	}
	return out, nil
}
