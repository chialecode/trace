package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/subscription"
)

// ACT-030: bounded Bilibili P1 read-only verification under the standing
// four-site authorization (DEC-077, docs/dev-rules/workflow.md §1.2) covering:
//   - UP space archive search, sort, partition filter & tlist parsing (#96)
//   - Zero-extra-request video chapter markers via single /x/player/wbi/v2 (#96)
//   - Upower fields on wbi/view and onlyfansAssetsV2 flag on feed/space (#96)
//
// Strictly capped at 1 run, <= 10 API calls across fail-closed categories,
// 0 media requests, >= 800ms spacing, 4m timeout, immediate stop without retry
// on -352 / -412 / HTTP 412 / -101 / v_voucher / rate limits, and panic-safe
// desensitized receipt persistence.
const (
	act030ID             = "act030"
	act030Label          = "ACT-030"
	act030MaxRuns        = 1
	act030MaxAPI         = 10
	act030DefaultSpacing = 850 * time.Millisecond
	act030Timeout        = 4 * time.Minute
)

var act030Limits = map[string]int{
	"nav":           2,
	"arc_search":    4,
	"video_view":    2,
	"subtitle_list": 1,
	"dynamic_feed":  1,
}

var reAct030SpaceMidPath = regexp.MustCompile(`^/(?:space/)?\d+/?$`)

type act030Config struct {
	RootDir        string
	OutPath        string
	MinSpacing     time.Duration // 0 => 850ms default (>= 800ms); <0 => 0s in synthetic tests
	Endpoints      bilibili.Endpoints
	AllowLocalhost bool
	StepHook       func(stepName string)
}

type act030RespTelemetry struct {
	HTTPStatus           int
	BiliCode             int
	HasBiliCode          bool
	HasVoucher           bool
	SentOnlyfansAssetsV2 bool
}

type act030Receipt struct {
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

func act030Classify(r *http.Request) (cat string, isJSONAPI bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	host := strings.ToLower(r.URL.Hostname())
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/x/web-interface/nav"):
		return "nav", true
	case strings.HasSuffix(path, "/x/space/wbi/arc/search"):
		return "arc_search", true
	case (host == "space.bilibili.com" || site.IsLoopbackHost(host)) && reAct030SpaceMidPath.MatchString(path):
		return "arc_search", false
	case strings.HasSuffix(path, "/x/web-interface/wbi/view"):
		return "video_view", true
	case strings.HasSuffix(path, "/x/player/wbi/v2"):
		return "subtitle_list", true
	case strings.HasSuffix(path, "/x/polymer/web-dynamic/v1/feed/space"):
		return "dynamic_feed", true
	default:
		return "", false
	}
}

type act030Checker struct {
	gate       *sitecheck.Gate
	apiLimits  map[string]int
	minSpacing time.Duration

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu       sync.Mutex
	stopped  string
	statuses map[string]int
	lastTel  map[string]act030RespTelemetry
}

func (c *act030Checker) stop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act030Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act030Checker) telemetryOf(cat string) (act030RespTelemetry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tel, ok := c.lastTel[cat]
	return tel, ok
}

func (c *act030Checker) waitSpacing(ctx context.Context) error {
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

func (c *act030Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		cat, isJSONAPI := act030Classify(r)
		if cat == "" {
			return nil, fmt.Errorf("%w: endpoint outside act030 authorized API list", sitecheck.ErrQuotaExceeded)
		}
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("act030 halted (%s)", reason)
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

		sentOnlyfansV2 := false
		if cat == "dynamic_feed" {
			features := r.URL.Query().Get("features")
			for _, tok := range strings.Split(features, ",") {
				if strings.TrimSpace(tok) == "onlyfansAssetsV2" {
					sentOnlyfansV2 = true
					break
				}
			}
		}

		resp, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}

		statusKey := cat
		if !isJSONAPI && cat == "arc_search" {
			statusKey = "arc_search_space_html"
		}
		c.mu.Lock()
		c.statuses[fmt.Sprintf("%s:%d", statusKey, resp.StatusCode)]++
		c.mu.Unlock()

		if resp.StatusCode == http.StatusPreconditionFailed {
			c.stop("http_412")
		} else if resp.StatusCode == http.StatusTooManyRequests {
			c.stop("http_429")
		}

		if isJSONAPI {
			raw, rErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if rErr != nil {
				return nil, rErr
			}

			tel := act030RespTelemetry{
				HTTPStatus:           resp.StatusCode,
				SentOnlyfansAssetsV2: sentOnlyfansV2,
			}
			var env struct {
				Code *int            `json:"code"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(raw, &env) == nil && env.Code != nil {
				tel.HasBiliCode = true
				tel.BiliCode = *env.Code
				tel.HasVoucher = bytes.Contains(env.Data, []byte(`"v_voucher"`))
				// Trip stop immediately so production WBI -352 retry cannot issue a second request.
				switch {
				case tel.HasVoucher:
					c.stop("bili_v_voucher")
				case tel.BiliCode == -352:
					c.stop("bili_-352")
				case tel.BiliCode == -412:
					c.stop("bili_-412")
				case tel.BiliCode == -101:
					c.stop("bili_-101")
				}
			}
			c.mu.Lock()
			c.lastTel[cat] = tel
			c.mu.Unlock()
		}

		return resp, nil
	})
}

func runAct030Main(root, out string) {
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", act030ID+".json")
	}
	rc, err := runAct030(context.Background(), act030Config{
		RootDir: root,
		OutPath: out,
	})
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d/%d media=%d/%dB receipt=%s\n",
		act030Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, act030MaxAPI,
		rc.MediaUsage.Requests, rc.MediaUsage.Bytes, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-30s ok=%v skipped=%v %s %v\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Facts)
	}
	for k, v := range rc.Verdicts {
		fmt.Printf("  %s: %s\n", k, v)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", act030Label, err)
		os.Exit(1)
	}
}

func loadAct030Targets(root string) (targets, error) {
	path := filepath.Join(root, ".local", "site-check", "targets-act007.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return targets{}, errors.New("targets: .local/site-check/targets-act007.json unreadable")
	}
	var tg targets
	if err := json.Unmarshal(raw, &tg); err != nil {
		return targets{}, errors.New("targets: .local/site-check/targets-act007.json invalid JSON")
	}
	if tg.VideoMid <= 0 || tg.DynamicMid <= 0 {
		return targets{}, errors.New("targets: videoMid and dynamicMid must both be > 0")
	}
	return tg, nil
}

// extractAct030Keyword picks a 2-rune search keyword from a video title in memory.
// It prefers a contiguous 2-rune Han bigram, then a contiguous 2-rune letter/digit
// run, then the first 2 non-space runes. The keyword is never written to the receipt.
func extractAct030Keyword(title string) string {
	runes := []rune(title)
	for i := 0; i+1 < len(runes); i++ {
		if unicode.Is(unicode.Han, runes[i]) && unicode.Is(unicode.Han, runes[i+1]) {
			return string(runes[i : i+2])
		}
	}
	for i := 0; i+1 < len(runes); i++ {
		if (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i])) &&
			(unicode.IsLetter(runes[i+1]) || unicode.IsDigit(runes[i+1])) {
			return string(runes[i : i+2])
		}
	}
	var nonSpace []rune
	for _, r := range runes {
		if !unicode.IsSpace(r) {
			nonSpace = append(nonSpace, r)
			if len(nonSpace) == 2 {
				return string(nonSpace)
			}
		}
	}
	return string(nonSpace)
}

func isZonesSortedDesc(zones []bilibili.UserVideoZone) bool {
	for i := 1; i < len(zones); i++ {
		prev, cur := zones[i-1], zones[i]
		if prev.Count < cur.Count {
			return false
		}
		if prev.Count == cur.Count && prev.TID > cur.TID {
			return false
		}
	}
	return true
}

func isChaptersOrdered(chapters []bilibili.VideoChapter) bool {
	for i, ch := range chapters {
		if ch.FromSec < 0 || ch.ToSec <= ch.FromSec {
			return false
		}
		if i > 0 {
			prev := chapters[i-1]
			if prev.FromSec > ch.FromSec || (prev.FromSec == ch.FromSec && prev.ToSec > ch.ToSec) {
				return false
			}
		}
	}
	return true
}

func verifyAct030TargetsAbsent(rawJSON string, videoMid, dynamicMid int64, keyword string) error {
	for _, mid := range []int64{videoMid, dynamicMid} {
		s := strconv.FormatInt(mid, 10)
		if strings.Contains(rawJSON, `"`+s+`"`) {
			return errors.New("receipt contains target mid string; not written")
		}
		if len(s) >= 5 && strings.Contains(rawJSON, s) {
			return errors.New("receipt contains target mid substring; not written")
		}
	}
	if keyword != "" {
		hasNonASCII := false
		for _, r := range keyword {
			if r > unicode.MaxASCII {
				hasNonASCII = true
				break
			}
		}
		if hasNonASCII && strings.Contains(rawJSON, keyword) {
			return errors.New("receipt contains extracted search keyword; not written")
		}
	}
	return nil
}

func runAct030(parent context.Context, cfg act030Config) (rc act030Receipt, retErr error) {
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", act030ID+".json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act030DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	commit := resolveReceiptCommit(".")
	if commit == "" {
		commit = resolveReceiptCommit(root)
	}
	rc = act030Receipt{
		Act:       act030Label,
		Commit:    commit,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Limits: map[string]any{
			"maxRuns":        act030MaxRuns,
			"apiTotal":       act030MaxAPI,
			"categories":     act030Limits,
			"mediaRequests":  0,
			"mediaBytes":     0,
			"minSpacingMs":   spacing.Milliseconds(),
			"timeoutSeconds": int(act030Timeout.Seconds()),
			"allowLocalhost": cfg.AllowLocalhost,
		},
		Verdicts: map[string]string{},
	}

	tg, err := loadAct030Targets(root)
	if err != nil {
		return rc, err
	}
	biliCookie, err := readCookie(root)
	if err != nil {
		return rc, errors.New("bilibili cookie: " + err.Error())
	}

	redactor := newAct010Redactor()
	redactor.addCookieHeader(biliCookie)
	registerAllAct026RawCookies(redactor, parseAct026RawPairs(biliCookie))
	videoMidStr := strconv.FormatInt(tg.VideoMid, 10)
	dynamicMidStr := strconv.FormatInt(tg.DynamicMid, 10)
	redactor.addIDs(videoMidStr, dynamicMidStr)
	if tg.LiveRoom > 0 {
		redactor.addIDs(strconv.FormatInt(tg.LiveRoom, 10))
	}

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID:         act030ID,
		MaxRuns:       act030MaxRuns,
		MaxAPICalls:   act030MaxAPI,
		MaxMediaFiles: 0,
		MaxMediaBytes: 0,
		MaxByCategory: act030Limits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act030Checker{
		gate:       gate,
		apiLimits:  act030Limits,
		minSpacing: spacing,
		statuses:   make(map[string]int),
		lastTel:    make(map[string]act030RespTelemetry),
	}

	var (
		workDir       string
		backend       *service.Backend
		closeOnce     sync.Once
		searchKeyword string
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

	// Panic-safe deferred receipt persistence once gate.BeginRun() consumes the 1/1 run ledger.
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
				retErr = errors.New("act030 aborted after recovered panic")
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
			if !s.OK && !s.Skipped {
				rc.Passed = false
			}
		}
		rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		rc.SecretScanClean = true

		if wErr := writeCleanReceipt(outPath, rc,
			func(raw string) error { return redactor.verifyJSONClean(raw, nil) },
			func(raw string) error { return act020NoHandles(redactor, raw) },
			func(raw string) error {
				return verifyCookieValuesAbsent(raw, biliCookie, "SESSDATA", "bili_jct", "DedeUserID", "DedeUserID__ckMd5", "sid")
			},
			func(raw string) error {
				return verifyAct030TargetsAbsent(raw, tg.VideoMid, tg.DynamicMid, searchKeyword)
			},
		); wErr != nil {
			rc.SecretScanClean = false
			rc.Passed = false
			if retErr == nil {
				retErr = wErr
			}
			return
		}
		if !rc.Passed && retErr == nil {
			retErr = errors.New("act030 completed with failures or stops")
		}
	}()

	workDir, err = os.MkdirTemp("", "trace-"+act030ID+"-")
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
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints:      cfg.Endpoints,
			PacingInterval: -1, // Enforced uniformly at >= 800ms in act030Checker.wrap.
			AllowLocalhost: cfg.AllowLocalhost,
			WrapTransport:  ck.wrap,
		},
	})
	if err != nil {
		return rc, err
	}

	ctx, cancel := context.WithTimeout(parent, act030Timeout)
	defer cancel()

	stopKinds := map[string]bool{
		bilibili.ErrKindRiskControl:  true,
		bilibili.ErrKindRateLimited:  true,
		bilibili.ErrKindAuthRequired: true,
		site.ErrKindBackoff:          true,
	}
	runStep := func(name string, fn func(f map[string]any) error) bool {
		r := stepRunner{
			steps:       &rc.Steps,
			stopReason:  ck.stopReason,
			tripStop:    ck.stop,
			classifyErr: bilibili.KindOf,
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

	registerVideosPage := func(p bilibili.UserVideosPage) {
		for _, z := range p.Zones {
			redactor.addHandles(z.Name)
		}
		for _, it := range p.Items {
			redactor.addIDs(it.BVID, strconv.FormatInt(it.AID, 10), strconv.FormatInt(it.Mid, 10))
			redactor.addHandles(it.Title, it.Description, it.Author)
			redactor.addURLs(it.Cover)
		}
	}

	registerDynamicItem := func(it bilibili.DynamicItem) {
		redactor.addIDs(it.ID, strconv.FormatInt(it.Author.Mid, 10))
		redactor.addHandles(it.Title, it.Text, it.Author.Name)
		redactor.addURLs(it.Author.Face, it.CanonicalURL)
		for _, img := range it.Images {
			redactor.addURLs(img.URL, img.LiveURL)
		}
		for _, v := range it.Videos {
			redactor.addIDs(v.BVID, strconv.FormatInt(v.AID, 10))
			redactor.addHandles(v.Title, v.Desc)
			redactor.addURLs(v.Cover)
		}
	}

	// Step 1: bili_login
	loginOK := runStep("bili_login", func(f map[string]any) error {
		st, lErr := backend.ImportCookie(ctx, biliCookie, "")
		if tel, ok := ck.telemetryOf("nav"); ok {
			f["httpStatus"] = tel.HTTPStatus
		}
		if st.Mid > 0 {
			redactor.addIDs(strconv.FormatInt(st.Mid, 10))
		}
		redactor.addHandles(st.Uname)
		redactor.addURLs(st.Face)
		f["loggedIn"] = st.LoggedIn
		f["source"] = st.Source
		f["hasMid"] = st.Mid > 0
		f["hasUname"] = st.Uname != ""
		if lErr != nil {
			return lErr
		}
		if !st.LoggedIn {
			return errors.New("bilibili login status is false")
		}
		return nil
	})
	if !loginOK {
		if ck.stopReason() == "" {
			ck.stop("login_failed")
		}
	} else {
		// Create a local-only subscription in the temp workspace (0 network requests)
		// so we can verify UserVideosPage(filter/keyword) preserves account_works total.
		_, _ = backend.CreateSubscription("act030-target", "", []subscription.AccountRef{{
			Site: string(site.Bilibili),
			UID:  videoMidStr,
		}}, subscription.SubRules{})
	}

	worksKey := "bilibili:" + videoMidStr
	var (
		baselineWorksTotal int
		firstZoneTID       int
		sampleBVID         string
		sampleCID          int64
	)

	// Step 2: p1_space_default_and_tlist
	runStep("p1_space_default_and_tlist", func(f map[string]any) error {
		page, pErr := backend.UserVideosPage(ctx, tg.VideoMid, 1)
		if tel, ok := ck.telemetryOf("arc_search"); ok {
			f["httpStatus"] = tel.HTTPStatus
		}
		if pErr != nil {
			return pErr
		}
		registerVideosPage(page)

		sorted := isZonesSortedDesc(page.Zones)
		for _, z := range page.Zones {
			if z.TID > 0 {
				firstZoneTID = z.TID
				break
			}
		}
		for _, it := range page.Items {
			if sampleBVID == "" && it.BVID != "" {
				sampleBVID = it.BVID
				searchKeyword = extractAct030Keyword(it.Title)
			}
			if sampleBVID != "" && searchKeyword == "" {
				searchKeyword = extractAct030Keyword(it.Title)
			}
			if sampleBVID != "" && searchKeyword != "" {
				break
			}
		}

		baselineWorksTotal = backend.SubscriptionAccountWorks()[worksKey].Total
		f["items"] = len(page.Items)
		f["total"] = page.Total
		f["zonesCount"] = len(page.Zones)
		f["zonesSortedDesc"] = sorted
		f["hasFirstZoneTID"] = firstZoneTID > 0
		f["hasSampleVideo"] = sampleBVID != ""
		f["worksTotalRecorded"] = baselineWorksTotal == page.Total

		if len(page.Items) == 0 || page.Total <= 0 || sampleBVID == "" {
			return errors.New("default space video page returned no videos")
		}
		if !sorted {
			return errors.New("tlist zones are not sorted by count desc then tid asc")
		}
		return nil
	})

	// Step 3: p1_space_zone_and_sort
	runStep("p1_space_zone_and_sort", func(f map[string]any) error {
		if firstZoneTID <= 0 {
			f["reason"] = "no_zone_tlist"
			return errSkip
		}
		page, pErr := backend.UserVideosPage(ctx, tg.VideoMid, 1, service.UserVideosPageOptions{
			Order: "click",
			TID:   firstZoneTID,
		})
		var httpStatus int
		if tel, ok := ck.telemetryOf("arc_search"); ok {
			httpStatus = tel.HTTPStatus
			f["httpStatus"] = httpStatus
		}
		if pErr != nil {
			return pErr
		}
		registerVideosPage(page)

		afterTotal := backend.SubscriptionAccountWorks()[worksKey].Total
		preserved := afterTotal == baselineWorksTotal
		f["order"] = "click"
		f["items"] = len(page.Items)
		f["filteredTotal"] = page.Total
		f["worksTotalPreserved"] = preserved

		if httpStatus != http.StatusOK {
			return fmt.Errorf("unexpected arc_search http status %d", httpStatus)
		}
		if len(page.Items) == 0 {
			return errors.New("zone+sort filtered page returned 0 items")
		}
		if !preserved {
			return fmt.Errorf("zone+sort call overwrote baseline works total (%d -> %d)", baselineWorksTotal, afterTotal)
		}
		return nil
	})

	// Step 4: p1_space_keyword_search
	runStep("p1_space_keyword_search", func(f map[string]any) error {
		if searchKeyword == "" {
			f["reason"] = "no_keyword"
			return errSkip
		}
		page, pErr := backend.UserVideosPage(ctx, tg.VideoMid, 1, service.UserVideosPageOptions{
			Keyword: searchKeyword,
		})
		var httpStatus int
		if tel, ok := ck.telemetryOf("arc_search"); ok {
			httpStatus = tel.HTTPStatus
			f["httpStatus"] = httpStatus
		}
		if pErr != nil {
			return pErr
		}
		registerVideosPage(page)

		afterTotal := backend.SubscriptionAccountWorks()[worksKey].Total
		preserved := afterTotal == baselineWorksTotal
		f["keywordRuneCount"] = utf8.RuneCountInString(searchKeyword)
		f["items"] = len(page.Items)
		f["filteredTotal"] = page.Total
		f["worksTotalPreserved"] = preserved

		if httpStatus != http.StatusOK {
			return fmt.Errorf("unexpected arc_search http status %d", httpStatus)
		}
		if len(page.Items) < 1 {
			return errors.New("keyword search returned 0 items")
		}
		if !preserved {
			return fmt.Errorf("keyword search overwrote baseline works total (%d -> %d)", baselineWorksTotal, afterTotal)
		}
		return nil
	})

	// Step 5: p1_video_view_and_upower
	runStep("p1_video_view_and_upower", func(f map[string]any) error {
		if sampleBVID == "" {
			return errors.New("no sample video selected from step 2")
		}
		vd, vErr := backend.BilibiliClient().VideoDetail(ctx, sampleBVID)
		if tel, ok := ck.telemetryOf("video_view"); ok {
			f["httpStatus"] = tel.HTTPStatus
		}
		if vErr != nil {
			return vErr
		}
		redactor.addIDs(vd.BVID, strconv.FormatInt(vd.AID, 10), strconv.FormatInt(vd.Author.Mid, 10))
		redactor.addHandles(vd.Title, vd.Desc, vd.Author.Name)
		redactor.addURLs(vd.Cover, vd.Author.Face, vd.CanonicalURL)
		for _, p := range vd.Pages {
			redactor.addIDs(strconv.FormatInt(p.CID, 10))
			redactor.addHandles(p.Title)
		}
		if len(vd.Pages) > 0 {
			sampleCID = vd.Pages[0].CID
		}

		f["pagesCount"] = len(vd.Pages)
		f["hasCID"] = sampleCID > 0
		f["isUpowerExclusive"] = vd.IsUpowerExclusive
		f["isUpowerPlay"] = vd.IsUpowerPlay
		f["isUpowerPreview"] = vd.IsUpowerPreview
		f["upowerFieldsParsed"] = true

		if len(vd.Pages) == 0 || sampleCID <= 0 {
			return errors.New("video detail missing pages or cid")
		}
		return nil
	})

	// Step 6: p1_zero_extra_req_chapters
	runStep("p1_zero_extra_req_chapters", func(f map[string]any) error {
		if sampleBVID == "" || sampleCID <= 0 {
			return errors.New("missing sample bvid or cid from step 5")
		}
		beforeUsage := gate.Usage()
		beforeSub := beforeUsage.Categories["subtitle_list"]
		beforeTotal := beforeUsage.APICalls

		extras, sErr := backend.BilibiliClient().MissingSubtitlesAndChapters(ctx, sampleBVID, sampleCID, true, nil)
		if tel, ok := ck.telemetryOf("subtitle_list"); ok {
			f["httpStatus"] = tel.HTTPStatus
		}
		for _, tr := range extras.Subtitles {
			redactor.addHandles(tr.LanDoc)
			redactor.addURLs(tr.SubtitleURL)
		}
		for _, ch := range extras.Chapters {
			redactor.addHandles(ch.Title)
		}
		if sErr != nil {
			return sErr
		}

		afterUsage := gate.Usage()
		subDelta := afterUsage.Categories["subtitle_list"] - beforeSub
		totalDelta := afterUsage.APICalls - beforeTotal
		ordered := isChaptersOrdered(extras.Chapters)

		f["subtitleListCalls"] = subDelta
		f["totalAPIDelta"] = totalDelta
		f["zeroExtraRequests"] = subDelta == 1 && totalDelta == 1
		f["subtitlesCount"] = len(extras.Subtitles)
		f["chaptersCount"] = len(extras.Chapters)
		f["chaptersOrdered"] = ordered

		if afterUsage.Categories["subtitle_list"] != 1 || subDelta != 1 || totalDelta != 1 {
			return fmt.Errorf("expected strictly 1 subtitle_list request with 0 extra calls (subDelta=%d totalDelta=%d)", subDelta, totalDelta)
		}
		if !ordered {
			return errors.New("chapters are not ordered by fromSec asc")
		}
		return nil
	})

	// Step 7: p1_dynamic_onlyfans_flag
	runStep("p1_dynamic_onlyfans_flag", func(f map[string]any) error {
		page, dErr := backend.UserDynamicsPage(ctx, tg.DynamicMid, "")
		var (
			httpStatus     int
			sentOnlyfansV2 bool
		)
		if tel, ok := ck.telemetryOf("dynamic_feed"); ok {
			httpStatus = tel.HTTPStatus
			sentOnlyfansV2 = tel.SentOnlyfansAssetsV2
			f["httpStatus"] = httpStatus
			f["sentOnlyfansAssetsV2"] = sentOnlyfansV2
		}
		if dErr != nil {
			return dErr
		}

		onlyFansCount := 0
		blockedMajorCount := 0
		degradedCount := 0
		for _, it := range page.Items {
			registerDynamicItem(it)
			if it.Origin != nil {
				registerDynamicItem(*it.Origin)
			}
			if it.IsOnlyFans {
				onlyFansCount++
			}
			if it.Blocked {
				blockedMajorCount++
			}
			if it.Degraded {
				degradedCount++
			}
		}

		f["items"] = len(page.Items)
		f["hasMore"] = page.HasMore
		f["onlyFansCount"] = onlyFansCount
		f["blockedMajorCount"] = blockedMajorCount
		f["degradedCount"] = degradedCount

		if httpStatus != http.StatusOK {
			return fmt.Errorf("unexpected dynamic_feed http status %d", httpStatus)
		}
		if !sentOnlyfansV2 {
			return errors.New("dynamic_feed request did not include onlyfansAssetsV2 feature flag")
		}
		if len(page.Items) == 0 {
			return errors.New("dynamic_feed returned 0 items")
		}
		return nil
	})

	return rc, nil
}
