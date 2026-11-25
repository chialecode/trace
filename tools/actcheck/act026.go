package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/douyin/sign"
	"github.com/chialecode/trace/internal/sitecheck"
)

// ACT-026: bounded Douyin read-only verification under the standing Douyin
// authorization (docs/dev-rules/workflow.md §1.2) for P0 "我的 › 喜欢" fixes
// (#52) and #37 read-only probes (collects/list, collects/video/list, history/read).
// Strictly capped at 1 run, <= 9 API requests, 0 media/CDN requests, >= 3s spacing,
// with panic-safe deferred receipt persistence and full raw-Cookie secret scanning.
const (
	act026ID             = "act026"
	act026Label          = "ACT-026"
	act026MaxRuns        = 1
	act026MaxAPI         = 9
	act026DefaultSpacing = 3200 * time.Millisecond
	act026Timeout        = 3 * time.Minute
)

var act026Limits = map[string]int{
	"dy_profile_self":    1,
	"dy_favorite":        2,
	"dy_collects_list":   1,
	"dy_collects_videos": 2,
	"dy_history_read":    2,
	"dy_spare":           1,
}

type act026Config struct {
	RootDir        string
	OutPath        string
	MinSpacing     time.Duration // 0 => 3.2s default; <0 => 0s in synthetic tests
	Endpoints      douyin.Endpoints
	Transport      http.RoundTripper
	AllowLocalhost bool
	StepHook       func(stepName string) // synthetic test hook (e.g. panic injection)
}

type act026RespTelemetry struct {
	HTTPStatus             int      `json:"httpStatus"`
	ContentType            string   `json:"contentType"`
	EmptyBody              bool     `json:"emptyBody"`
	ListShape              string   `json:"listShape,omitempty"`
	TopLevelKeys           []string `json:"topLevelKeys,omitempty"`
	HasBDTicketGuardResult bool     `json:"hasBDTicketGuardResult"`
	HasBDTuringVerify      bool     `json:"hasBDTuringVerify"`
}

type act026Receipt struct {
	Act             string            `json:"act"`
	Commit          string            `json:"commit,omitempty"`
	StartedAt       string            `json:"startedAt"`
	FinishedAt      string            `json:"finishedAt"`
	Passed          bool              `json:"passed"`
	StoppedBy       string            `json:"stoppedBy,omitempty"`
	Limits          map[string]any    `json:"limits"`
	APIUsage        sitecheck.Usage   `json:"apiUsage"`
	MediaUsage      mediaUsage        `json:"mediaUsage"`
	StatusCounts    map[string]int    `json:"statusCounts"`
	Steps           []step            `json:"steps"`
	Verdicts        map[string]string `json:"verdicts"`
	TempDeleted     bool              `json:"tempDataDeleted"`
	SecretScanClean bool              `json:"secretScanClean"`
	Notes           []string          `json:"notes,omitempty"`
}

func act026Classify(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/aweme/v1/web/user/profile/self/"):
		return "dy_profile_self"
	case strings.HasSuffix(p, "/aweme/v1/web/aweme/favorite/"):
		return "dy_favorite"
	case strings.HasSuffix(p, "/aweme/v1/web/collects/list/"):
		return "dy_collects_list"
	case strings.HasSuffix(p, "/aweme/v1/web/collects/video/list/"):
		return "dy_collects_videos"
	case strings.HasSuffix(p, "/aweme/v1/web/history/read/"):
		return "dy_history_read"
	default:
		return ""
	}
}

func act026SanitizeContentType(ct string) string {
	base, _, _ := strings.Cut(strings.TrimSpace(ct), ";")
	base = strings.ToLower(strings.TrimSpace(base))
	if len(base) > 64 {
		base = base[:64]
	}
	return base
}

func act026InspectBodyShape(raw []byte) (shape string, keys []string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "empty_body", nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(trimmed, &top) != nil {
		return "non_json", nil
	}
	shape = "no_aweme_list"
	if v, ok := top["aweme_list"]; ok {
		var arr []json.RawMessage
		switch {
		case bytes.Equal(bytes.TrimSpace(v), []byte("null")):
			shape = "aweme_list_null"
		case json.Unmarshal(v, &arr) == nil:
			shape = fmt.Sprintf("aweme_list_len_%d", len(arr))
		default:
			shape = "aweme_list_other"
		}
	}
	keys = make([]string, 0, len(top))
	for k := range top {
		if len(k) <= 40 && !strings.ContainsAny(k, "/:=") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return shape, keys
}

type act026Checker struct {
	gate       *sitecheck.Gate
	apiLimits  map[string]int
	minSpacing time.Duration

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu       sync.Mutex
	stopped  string
	statuses map[string]int
	lastTel  map[string]act026RespTelemetry
}

func (c *act026Checker) stop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act026Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act026Checker) telemetryOf(cat string) (act026RespTelemetry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tel, ok := c.lastTel[cat]
	return tel, ok
}

func (c *act026Checker) waitSpacing(ctx context.Context) error {
	if c.minSpacing <= 0 {
		return nil
	}
	c.paceMu.Lock()
	defer c.paceMu.Unlock()
	if !c.lastReqAt.IsZero() {
		wait := time.Until(c.lastReqAt.Add(c.minSpacing))
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

func (c *act026Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		cat := act026Classify(r)
		if cat == "" {
			return nil, fmt.Errorf("%w: endpoint outside act026 authorized API list", sitecheck.ErrQuotaExceeded)
		}
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("act026 halted (%s)", reason)
		}
		if err := checkAPICategoryAllowed(c.apiLimits, cat); err != nil {
			return nil, err
		}
		if err := c.gate.AcquireAPI(cat); err != nil {
			return nil, err
		}
		if err := c.waitSpacing(r.Context()); err != nil {
			return nil, err
		}
		resp, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		raw, rErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if rErr != nil {
			return nil, rErr
		}

		shape, topKeys := act026InspectBodyShape(raw)
		tel := act026RespTelemetry{
			HTTPStatus:             resp.StatusCode,
			ContentType:            act026SanitizeContentType(resp.Header.Get("Content-Type")),
			EmptyBody:              len(bytes.TrimSpace(raw)) == 0,
			ListShape:              shape,
			TopLevelKeys:           topKeys,
			HasBDTicketGuardResult: strings.TrimSpace(resp.Header.Get("bd-ticket-guard-result")) != "",
			HasBDTuringVerify:      strings.TrimSpace(resp.Header.Get("bdturing-verify")) != "",
		}
		c.mu.Lock()
		c.statuses[fmt.Sprintf("%s:%d", cat, resp.StatusCode)]++
		c.lastTel[cat] = tel
		c.mu.Unlock()

		if resp.StatusCode == http.StatusUnauthorized ||
			resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == 444 {
			c.stop(fmt.Sprintf("http_%d", resp.StatusCode))
		} else if tel.HasBDTuringVerify {
			c.stop("bdturing_verify")
		}
		return resp, nil
	})
}

func runAct026Main(root, out string) {
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", act026ID+".json")
	}
	rc, err := runAct026(context.Background(), act026Config{
		RootDir: root,
		OutPath: out,
	})
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d/%d media=%d/%dB receipt=%s\n",
		act026Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, act026MaxAPI,
		rc.MediaUsage.Requests, rc.MediaUsage.Bytes, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-26s ok=%v skipped=%v %s %v\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Facts)
	}
	for k, v := range rc.Verdicts {
		fmt.Printf("  %s: %s\n", k, v)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", act026Label, err)
		os.Exit(1)
	}
}

func parseAct026RawPairs(rawCookie string) map[string]string {
	cleaned := strings.TrimSpace(rawCookie)
	if strings.HasPrefix(strings.ToLower(cleaned), "cookie:") {
		cleaned = strings.TrimSpace(cleaned[len("cookie:"):])
	}
	cleaned = strings.ReplaceAll(cleaned, "\r", ";")
	cleaned = strings.ReplaceAll(cleaned, "\n", ";")
	out := make(map[string]string)
	for _, part := range strings.Split(cleaned, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.Trim(strings.TrimSpace(part[eq+1:]), `"'`)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

var act026SafeStructuralTokens = map[string]bool{
	"true": true, "false": true, "null": true, "none": true, "undefined": true,
	"pass": true, "work": true, "video": true, "note": true, "user": true,
	"douyin": true, "skipped": true, "unavailable": true, "empty_body": true,
	"non_json": true, "aweme_list": true, "has_more": true, "status_code": true,
	"cursor": true, "max_cursor": true, "min_cursor": true,
}

func isAct026CookieSecretCandidate(key, val string) bool {
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if len(v) < 4 {
		return false
	}
	lowerVal := strings.ToLower(v)
	if act026SafeStructuralTokens[lowerVal] {
		return false
	}
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	if lowerKey == "is_staff_user" || lowerKey == "login_time" {
		return false
	}
	if reAct010AllDigits.MatchString(v) && len(v) < 8 {
		return false
	}
	return true
}

func act026SecretVariants(key, val string) []string {
	v := strings.Trim(strings.TrimSpace(val), `"'`)
	if !isAct026CookieSecretCandidate(key, v) {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if !isAct026CookieSecretCandidate(key, s) || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(v)
	if esc := url.QueryEscape(v); esc != v {
		add(esc)
	}
	if unesc, err := url.QueryUnescape(v); err == nil && unesc != v {
		add(unesc)
	}
	// Also register sub-tokens of composite ByteDance cookies (e.g. 1.0.0-<tok>, <tok>|<ts>|...).
	unwrapped := v
	if unesc, err := url.QueryUnescape(v); err == nil {
		unwrapped = unesc
	}
	if after, ok := strings.CutPrefix(unwrapped, "1.0.0-"); ok && len(after) >= 6 {
		add(after)
	}
	for _, seg := range strings.Split(unwrapped, "|") {
		if len(seg) >= 8 && !reAct010AllDigits.MatchString(seg) {
			add(seg)
		}
	}
	return out
}

func registerAllAct026RawCookies(r *act010Redactor, rawPairs map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range rawPairs {
		for _, cand := range act026SecretVariants(k, v) {
			r.cookies[cand] = true
		}
	}
}

func verifyAct026CookieValuesAbsent(rawJSON, rawCookie string) error {
	rawPairs := parseAct026RawPairs(rawCookie)
	keys := make([]string, 0, len(rawPairs))
	for k := range rawPairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if err := verifyCookieValuesAbsent(rawJSON, rawCookie, "sessionid", "sessionid_ss", "sid_ucp_v1", "ssid_ucp_v1", "s_v_web_id", "msToken", "d_ticket"); err != nil {
		return err
	}
	for _, k := range keys {
		for _, cand := range act026SecretVariants(k, rawPairs[k]) {
			if strings.Contains(rawJSON, cand) {
				return fmt.Errorf("receipt would contain secret cookie %s; not written", k)
			}
		}
	}
	return nil
}

func runAct026(parent context.Context, cfg act026Config) (rc act026Receipt, retErr error) {
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", act026ID+".json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act026DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	commit := resolveReceiptCommit(".")
	if commit == "" {
		commit = resolveReceiptCommit(root)
	}
	rc = act026Receipt{
		Act:       act026Label,
		Commit:    commit,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Limits: map[string]any{
			"maxRuns":        act026MaxRuns,
			"apiTotal":       act026MaxAPI,
			"categories":     act026Limits,
			"mediaRequests":  0,
			"mediaBytes":     0,
			"minSpacingMs":   spacing.Milliseconds(),
			"allowLocalhost": cfg.AllowLocalhost,
		},
		Verdicts: map[string]string{},
	}

	dyCookie, dyClean, err := readAndValidateDouyinCookie(root)
	if err != nil {
		return rc, err
	}
	rawPairs := parseAct026RawPairs(dyCookie)

	redactor := newAct010Redactor()
	redactor.addCookieHeader(dyCookie)
	redactor.addCookieMap(dyClean)
	registerAllAct026RawCookies(redactor, rawPairs)

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID:         act026ID,
		MaxRuns:       act026MaxRuns,
		MaxAPICalls:   act026MaxAPI,
		MaxMediaFiles: 0,
		MaxMediaBytes: 0,
		MaxByCategory: act026Limits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act026Checker{
		gate:       gate,
		apiLimits:  act026Limits,
		minSpacing: spacing,
		statuses:   make(map[string]int),
		lastTel:    make(map[string]act026RespTelemetry),
	}

	var (
		workDir   string
		backend   *service.Backend
		closeOnce sync.Once
	)
	closeResources := func() bool {
		closeOnce.Do(func() {
			if backend != nil {
				_ = backend.Close()
			}
		})
		if workDir == "" {
			return true
		}
		_ = os.RemoveAll(workDir)
		_, statErr := os.Stat(workDir)
		return os.IsNotExist(statErr)
	}

	// Guarantee clean desensitized receipt persistence on every exit path (including panics or early errors)
	// once gate.BeginRun() has consumed the 1/1 run ledger (LESSONS.md ACT-022).
	defer func() {
		if rec := recover(); rec != nil {
			scrubbed := act020Scrub(redactor.scrubError(fmt.Sprint(rec)))
			ck.stop("panic")
			rc.Steps = append(rc.Steps, step{
				Name:    "panic_recovered",
				OK:      false,
				ErrKind: "panic",
				Error:   "recovered panic: " + scrubbed,
			})
			if retErr == nil {
				retErr = errors.New("act026 aborted after recovered panic")
			}
		}

		rc.TempDeleted = closeResources()
		rc.APIUsage = gate.Usage()
		ck.mu.Lock()
		rc.StatusCounts = make(map[string]int, len(ck.statuses))
		for k, v := range ck.statuses {
			rc.StatusCounts[k] = v
		}
		rc.StoppedBy = ck.stopped
		ck.mu.Unlock()

		rc.Verdicts = make(map[string]string, len(rc.Steps))
		for _, s := range rc.Steps {
			v := "pass"
			switch {
			case s.Skipped:
				v = "skipped"
			case !s.OK:
				if s.ErrKind != "" {
					v = "fail:" + s.ErrKind
				} else {
					v = "fail"
				}
			}
			rc.Verdicts[s.Name] = v
		}

		rc.Passed = rc.TempDeleted && rc.StoppedBy == "" && len(rc.Steps) > 0 && retErr == nil
		for _, s := range rc.Steps {
			// D-V5_watch_history is an exploratory read-only probe for #37; all core steps must succeed.
			if !s.OK && !s.Skipped && s.Name != "D-V5_watch_history" {
				rc.Passed = false
			}
		}
		rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		rc.SecretScanClean = true
		if wErr := writeCleanReceipt(outPath, rc,
			func(raw string) error { return redactor.verifyJSONClean(raw, nil) },
			func(raw string) error { return act020NoHandles(redactor, raw) },
			func(raw string) error { return verifyAct026CookieValuesAbsent(raw, dyCookie) },
		); wErr != nil {
			rc.SecretScanClean = false
			rc.Passed = false
			if retErr == nil {
				retErr = wErr
			}
			return
		}
		if !rc.Passed && retErr == nil {
			retErr = errors.New("act026 completed with failures or stops")
		}
	}()

	// Step 0 (offline, 0 network requests): boolean presence of key Cookie names ONLY.
	// Never record any Cookie value or byte length.
	rc.Steps = append(rc.Steps, step{
		Name: "step0_cookie_presence",
		OK:   true,
		Facts: map[string]any{
			"has_sessionid":  rawPairs["sessionid"] != "" || rawPairs["sessionid_ss"] != "",
			"has_sid_ucp_v1": rawPairs["sid_ucp_v1"] != "",
			"has_s_v_web_id": rawPairs["s_v_web_id"] != "",
			"has_uifid":      sign.ExtractUIFID(rawPairs) != "",
			"has_msToken":    rawPairs["msToken"] != "",
			"has_d_ticket":   rawPairs["d_ticket"] != "",
		},
	})
	if cfg.StepHook != nil {
		cfg.StepHook("step0_cookie_presence")
	}

	workDir, err = os.MkdirTemp("", "trace-"+act026ID+"-")
	if err != nil {
		return rc, err
	}
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:      "dev",
		ForceMode:    "installed",
		LocalAppData: filepath.Join(workDir, "localappdata"),
		DocumentsDir: filepath.Join(workDir, "documents"),
	})
	if err != nil {
		return rc, err
	}
	backend, err = service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		DouyinOpts: douyin.ClientOptions{
			Endpoints:      cfg.Endpoints,
			Transport:      cfg.Transport,
			WrapTransport:  ck.wrap,
			MinInterval:    -1, // Enforced uniformly at >= 3s in act026Checker.wrap.
			JitterMax:      -1,
			AllowLocalhost: cfg.AllowLocalhost,
		},
	})
	if err != nil {
		return rc, err
	}

	ctx, cancel := context.WithTimeout(parent, act026Timeout)
	defer cancel()

	stopKinds := map[string]bool{
		site.ErrKindRiskControl:  true,
		site.ErrKindRateLimited:  true,
		site.ErrKindAuthRequired: true,
		site.ErrKindBackoff:      true,
	}
	runStep := func(name string, fn func(f map[string]any) error) bool {
		r := stepRunner{
			steps:       &rc.Steps,
			stopReason:  ck.stopReason,
			tripStop:    ck.stop,
			classifyErr: site.KindOf,
			scrubErr:    func(msg string) string { return act020Scrub(redactor.scrubError(msg)) },
			stopOnKinds: stopKinds,
		}
		return r.run(name, func(f map[string]any) error {
			if cfg.StepHook != nil {
				cfg.StepHook(name)
			}
			return fn(f)
		})
	}

	seenIDs := map[string]map[string]bool{}
	dupes := func(key string, items []service.AccountItem) int {
		m := seenIDs[key]
		if m == nil {
			m = map[string]bool{}
			seenIDs[key] = m
		}
		n := 0
		for _, it := range items {
			id := it.Kind + ":" + it.ID
			if it.ID == "" {
				continue
			}
			if m[id] {
				n++
			}
			m[id] = true
		}
		return n
	}
	cursors := map[string]string{}
	recordPage := func(f map[string]any, key string, p service.AccountPage) {
		f["items"], f["hasMore"], f["total"] = len(p.Items), p.HasMore, p.Total
		complete := 0
		kinds := map[string]int{}
		for _, it := range p.Items {
			redactor.addIDs(it.ID, it.AuthorID)
			redactor.addHandles(it.Title, it.Author, it.UniqueID, it.Signature)
			redactor.addURLs(it.Cover, it.OpenURL, it.BrowserURL)
			kinds[it.Kind]++
			if it.Invalid || it.Kind == service.AccountItemUnsupported {
				if it.Title != "" {
					complete++
				}
			} else if it.ID != "" && it.Title != "" && it.Cover != "" && it.OpenURL != "" {
				complete++
			}
		}
		f["kinds"], f["fieldsComplete"] = kinds, complete
		if prev, ok := cursors[key]; ok {
			f["cursorAdvanced"] = p.Cursor == "" || p.Cursor != prev
		}
		cursors[key] = p.Cursor
	}
	attachTelemetry := func(f map[string]any, cat string) {
		if tel, ok := ck.telemetryOf(cat); ok {
			f["httpStatus"] = tel.HTTPStatus
			f["contentType"] = tel.ContentType
			f["emptyBody"] = tel.EmptyBody
			f["hasBDTicketGuardResult"] = tel.HasBDTicketGuardResult
			f["hasBDTuringVerify"] = tel.HasBDTuringVerify
			if tel.ListShape != "" {
				f["listShape"] = tel.ListShape
			}
		}
	}

	// Step 1: dy_login (LoginWithCookie -> profile/self).
	// Stop immediately on auth_required, 403/429/444, captcha challenge, or any login failure.
	loginOK := runStep("dy_login", func(f map[string]any) error {
		st, lErr := backend.LoginWithCookie(ctx, "douyin", dyCookie, "")
		attachTelemetry(f, "dy_profile_self")
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID, st.Uname)
		redactor.addURLs(st.Face)
		redactor.addCookieValuesFromStore(backend.CredentialManager().Douyin())
		f["loggedIn"] = st.LoggedIn
		f["hasUID"] = st.UID != ""
		f["hasSecUID"] = reAct010SecUID.MatchString(st.SecUID)
		f["hasNickname"] = st.Uname != ""
		if lErr != nil {
			return lErr
		}
		if !st.LoggedIn {
			return errors.New("douyin login status is false")
		}
		return nil
	})
	if !loginOK && ck.stopReason() == "" {
		reason := "login_failed"
		if len(rc.Steps) > 0 && rc.Steps[len(rc.Steps)-1].ErrKind != "" {
			reason = rc.Steps[len(rc.Steps)-1].ErrKind
		}
		ck.stop(reason)
	}

	// Step 2: D-V2_likes (Favorites page 1, and page 2 when has_more).
	// A 0-byte HTTP 200 unavailable response records diagnostic booleans without halting
	// D-V4 / D-V5 read-only probes; 403/429/444/challenge/auth_required halt immediately.
	runStep("D-V2_likes", func(f map[string]any) error {
		p1, lErr := backend.AccountPage(ctx, "douyin", service.AccountLikes, "", "")
		attachTelemetry(f, "dy_favorite")
		if lErr != nil {
			return lErr
		}
		recordPage(f, "dy_likes", p1)
		f["dupesPage1"] = dupes("dy_likes", p1.Items)
		if p1.HasMore && ck.stopReason() == "" {
			p2, err2 := backend.AccountPage(ctx, "douyin", service.AccountLikes, "", p1.Cursor)
			f2 := map[string]any{}
			attachTelemetry(f2, "dy_favorite")
			if err2 != nil {
				f["page2"] = f2
				return err2
			}
			recordPage(f2, "dy_likes", p2)
			f2["dupesAcrossPages"] = dupes("dy_likes", p2.Items)
			f["page2"] = f2
		}
		return nil
	})

	// Step 3: D-V4_collect_folders (collects/list + collects/video/list p1 [& p2 if has_more]).
	runStep("D-V4_collect_folders", func(f map[string]any) error {
		folders, fErr := backend.DouyinClient().CollectFolders(ctx, "")
		attachTelemetry(f, "dy_collects_list")
		if fErr != nil {
			return fErr
		}
		f["folders"], f["hasMore"] = len(folders.Folders), folders.HasMore
		idStr := 0
		for _, fo := range folders.Folders {
			redactor.addIDs(fo.ID)
			redactor.addHandles(fo.Name)
			redactor.addURLs(fo.CoverURL)
			if fo.ID != "" {
				idStr++
			}
		}
		f["foldersWithID"] = idStr
		if len(folders.Folders) == 0 || ck.stopReason() != "" {
			return nil
		}
		p1, vErr := backend.AccountPage(ctx, "douyin", service.AccountCollection, folders.Folders[0].ID, "")
		fp := map[string]any{}
		attachTelemetry(fp, "dy_collects_videos")
		if vErr != nil {
			f["firstFolder"] = fp
			return vErr
		}
		recordPage(fp, "dy_folder", p1)
		fp["dupesPage1"] = dupes("dy_folder", p1.Items)
		if p1.HasMore && ck.stopReason() == "" {
			p2, vErr2 := backend.AccountPage(ctx, "douyin", service.AccountCollection, folders.Folders[0].ID, p1.Cursor)
			fp2 := map[string]any{}
			attachTelemetry(fp2, "dy_collects_videos")
			if vErr2 != nil {
				fp["page2"] = fp2
				f["firstFolder"] = fp
				return vErr2
			}
			recordPage(fp2, "dy_folder", p2)
			fp2["dupesAcrossPages"] = dupes("dy_folder", p2.Items)
			fp["page2"] = fp2
		}
		f["firstFolder"] = fp
		return nil
	})

	// Step 4: D-V5_watch_history (history/read read-only probe for #37).
	{
		histStopKinds := map[string]bool{
			site.ErrKindRateLimited:  true,
			site.ErrKindAuthRequired: true,
			site.ErrKindBackoff:      true,
		}
		r := stepRunner{
			steps:       &rc.Steps,
			stopReason:  ck.stopReason,
			tripStop:    ck.stop,
			classifyErr: site.KindOf,
			scrubErr:    func(msg string) string { return act020Scrub(redactor.scrubError(msg)) },
			stopOnKinds: histStopKinds,
		}
		r.run("D-V5_watch_history", func(f map[string]any) error {
			if cfg.StepHook != nil {
				cfg.StepHook("D-V5_watch_history")
			}
			p1, hErr := backend.DouyinClient().WatchHistory(ctx, "")
			attachTelemetry(f, "dy_history_read")
			if tel, ok := ck.telemetryOf("dy_history_read"); ok && len(tel.TopLevelKeys) > 0 {
				f["topLevelKeys"] = append([]string(nil), tel.TopLevelKeys...)
			}
			if hErr != nil {
				if site.IsKind(hErr, site.ErrKindRiskControl) {
					tel, _ := ck.telemetryOf("dy_history_read")
					if tel.HTTPStatus != http.StatusOK || tel.HasBDTuringVerify || strings.Contains(hErr.Error(), "验证") {
						ck.stop(site.ErrKindRiskControl)
					}
				}
				return hErr
			}
			f["items"], f["hasMore"] = len(p1.Works), p1.HasMore
			seenHist := map[string]bool{}
			for _, w := range p1.Works {
				redactor.addIDs(w.ID, w.Author.SecUID, w.Author.UID)
				redactor.addHandles(w.Title, w.Author.Nickname, w.Author.UniqueID)
				seenHist[w.ID] = true
			}
			if p1.HasMore && ck.stopReason() == "" {
				p2, hErr2 := backend.DouyinClient().WatchHistory(ctx, p1.NextCursor)
				f2 := map[string]any{}
				attachTelemetry(f2, "dy_history_read")
				if hErr2 != nil {
					f["page2"] = f2
					return hErr2
				}
				dupesAcross := 0
				for _, w := range p2.Works {
					redactor.addIDs(w.ID, w.Author.SecUID, w.Author.UID)
					redactor.addHandles(w.Title, w.Author.Nickname, w.Author.UniqueID)
					if w.ID != "" && seenHist[w.ID] {
						dupesAcross++
					}
				}
				f2["items"] = len(p2.Works)
				f2["hasMore"] = p2.HasMore
				f2["cursorAdvanced"] = p2.NextCursor == "" || p2.NextCursor != p1.NextCursor
				f2["dupesAcrossPages"] = dupesAcross
				f["page2"] = f2
			}
			return nil
		})
	}

	// Note: if D-V5_watch_history alone returned HTTP 200 empty/shell without challenge,
	// keep StoppedBy focused on real site-wide halts so D-V5's exploratory status is clear.
	return rc, nil
}
