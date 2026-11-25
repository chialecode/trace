package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/douyin/guard"
	"github.com/chialecode/trace/internal/site/douyin/sign"
	"github.com/chialecode/trace/internal/sitecheck"
)

// ACT-027: bounded Douyin read-only verification under the standing Douyin
// authorization (docs/dev-rules/workflow.md §1.2) for pure-Go ECDSA P-256
// bd-ticket-guard handshake (profile/self) and "我的 › 喜欢" (aweme/favorite).
// Strictly capped at 1 run, <= 6 API requests, 0 media/CDN requests, >= 3.2s spacing,
// with panic-safe deferred receipt persistence and Cookie + guard secret scanning.
const (
	act027ID             = "act027"
	act027Label          = "ACT-027"
	act027MaxRuns        = 1
	act027MaxAPI         = 6
	act027DefaultSpacing = 3200 * time.Millisecond
	act027Timeout        = 3 * time.Minute
)

var act027Limits = map[string]int{
	"dy_profile_self": 2,
	"dy_favorite":     2,
	"dy_spare":        2,
}

type act027Config struct {
	RootDir        string
	OutPath        string
	MinSpacing     time.Duration // 0 => 3.2s default; <0 => 0s in synthetic tests
	Endpoints      douyin.Endpoints
	Transport      http.RoundTripper
	AllowLocalhost bool
	StepHook       func(stepName string)
}

// act027RespTelemetry holds strictly non-sensitive transport and protocol flags:
// never records public/private keys, tickets, ts_sign, req_sign, or raw header text.
type act027RespTelemetry struct {
	HTTPStatus                 int      `json:"httpStatus"`
	ContentType                string   `json:"contentType"`
	EmptyBody                  bool     `json:"emptyBody"`
	ListShape                  string   `json:"listShape,omitempty"`
	TopLevelKeys               []string `json:"topLevelKeys,omitempty"`
	SentGuardHandshake         bool     `json:"sentGuardHandshake"`
	SentGuardSigned            bool     `json:"sentGuardSigned"`
	HasBDTicketGuardResult     bool     `json:"hasBDTicketGuardResult"`
	HasBDTicketGuardResultCode bool     `json:"hasBDTicketGuardResultCode"`
	BDTicketGuardResultCode    *int     `json:"bdTicketGuardResultCode,omitempty"`
	HasServerDataHeader        bool     `json:"hasServerDataHeader"`
	ServerIssuedTicket         bool     `json:"serverIssuedTicket"`
	ServerIssuedTsSign         bool     `json:"serverIssuedTsSign"`
	ServerIssuedClientCert     bool     `json:"serverIssuedClientCert"`
	HasBDTuringVerify          bool     `json:"hasBDTuringVerify"`
}

type act027Receipt struct {
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

func act027Classify(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/aweme/v1/web/user/profile/self/"):
		return "dy_profile_self"
	case strings.HasSuffix(p, "/aweme/v1/web/aweme/favorite/"):
		return "dy_favorite"
	default:
		return ""
	}
}

type act027Checker struct {
	gate       *sitecheck.Gate
	apiLimits  map[string]int
	minSpacing time.Duration

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu       sync.Mutex
	stopped  string
	statuses map[string]int
	lastTel  map[string]act027RespTelemetry
}

func (c *act027Checker) stop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act027Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act027Checker) telemetryOf(cat string) (act027RespTelemetry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tel, ok := c.lastTel[cat]
	return tel, ok
}

func (c *act027Checker) waitSpacing(ctx context.Context) error {
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

func (c *act027Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		cat := act027Classify(r)
		if cat == "" {
			return nil, fmt.Errorf("%w: endpoint outside act027 authorized API list", sitecheck.ErrQuotaExceeded)
		}
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("act027 halted (%s)", reason)
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

		sentHandshake := strings.TrimSpace(r.Header.Get(guard.HeaderREEPublicKey)) != ""
		sentSigned := strings.TrimSpace(r.Header.Get(guard.HeaderClientData)) != "" &&
			strings.TrimSpace(r.Header.Get(guard.HeaderWebSignType)) != ""

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

		flatHeaders := make(map[string]string, len(resp.Header))
		for k, vals := range resp.Header {
			if len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
				flatHeaders[k] = vals[0]
			}
		}
		rawSetCookies := make(map[string]string)
		for _, ck := range resp.Cookies() {
			if ck != nil && ck.Value != "" {
				rawSetCookies[ck.Name] = ck.Value
			}
		}
		srvGuard := guard.ParseServerResponse(flatHeaders, rawSetCookies)
		shape, topKeys := act026InspectBodyShape(raw)

		tel := act027RespTelemetry{
			HTTPStatus:                 resp.StatusCode,
			ContentType:                act026SanitizeContentType(resp.Header.Get("Content-Type")),
			EmptyBody:                  len(bytes.TrimSpace(raw)) == 0,
			ListShape:                  shape,
			TopLevelKeys:               topKeys,
			SentGuardHandshake:         sentHandshake,
			SentGuardSigned:            sentSigned,
			HasBDTicketGuardResult:     srvGuard.HasResultHeader,
			HasBDTicketGuardResultCode: srvGuard.HasResultCode,
			HasServerDataHeader:        srvGuard.HasServerData,
			ServerIssuedTicket:         srvGuard.Ticket != "",
			ServerIssuedTsSign:         srvGuard.TsSign != "",
			ServerIssuedClientCert:     srvGuard.ClientCert != "",
			HasBDTuringVerify:          strings.TrimSpace(resp.Header.Get("bdturing-verify")) != "",
		}
		if srvGuard.HasResultCode {
			code := srvGuard.ResultCode
			tel.BDTicketGuardResultCode = &code
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

func runAct027Main(root, out string) {
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", act027ID+".json")
	}
	rc, err := runAct027(context.Background(), act027Config{
		RootDir: root,
		OutPath: out,
	})
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d/%d media=%d/%dB receipt=%s\n",
		act027Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, act027MaxAPI,
		rc.MediaUsage.Requests, rc.MediaUsage.Bytes, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-26s ok=%v skipped=%v %s %v\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Facts)
	}
	for k, v := range rc.Verdicts {
		fmt.Printf("  %s: %s\n", k, v)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", act027Label, err)
		os.Exit(1)
	}
}

func verifyAct027GuardSecretsAbsent(rawJSON string, secrets []string) error {
	for _, sec := range secrets {
		s := strings.TrimSpace(sec)
		if len(s) < 6 {
			continue
		}
		if strings.Contains(rawJSON, s) {
			return errors.New("receipt would contain bd-ticket-guard secret material; not written")
		}
	}
	return nil
}

func runAct027(parent context.Context, cfg act027Config) (rc act027Receipt, retErr error) {
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", act027ID+".json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act027DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	commit := resolveReceiptCommit(".")
	if commit == "" {
		commit = resolveReceiptCommit(root)
	}
	rc = act027Receipt{
		Act:       act027Label,
		Commit:    commit,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Limits: map[string]any{
			"maxRuns":        act027MaxRuns,
			"apiTotal":       act027MaxAPI,
			"categories":     act027Limits,
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

	var (
		guardSecMu   sync.Mutex
		guardSecrets []string
	)
	recordGuardSecrets := func(vals []string) {
		if len(vals) == 0 {
			return
		}
		guardSecMu.Lock()
		guardSecrets = append(guardSecrets, vals...)
		guardSecMu.Unlock()
		redactor.mu.Lock()
		for _, v := range vals {
			if len(v) >= 6 {
				redactor.cookies[v] = true
			}
		}
		redactor.mu.Unlock()
	}

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID:         act027ID,
		MaxRuns:       act027MaxRuns,
		MaxAPICalls:   act027MaxAPI,
		MaxMediaFiles: 0,
		MaxMediaBytes: 0,
		MaxByCategory: act027Limits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act027Checker{
		gate:       gate,
		apiLimits:  act027Limits,
		minSpacing: spacing,
		statuses:   make(map[string]int),
		lastTel:    make(map[string]act027RespTelemetry),
	}

	var (
		workDir   string
		backend   *service.Backend
		closeOnce sync.Once
	)
	closeResources := func() bool {
		closeOnce.Do(func() {
			if backend != nil {
				if dyStore := backend.CredentialManager().Douyin(); dyStore != nil {
					recordGuardSecrets(dyStore.DouyinGuardSecrets())
				}
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
				retErr = errors.New("act027 aborted after recovered panic")
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

		guardSecMu.Lock()
		secSnapshot := append([]string(nil), guardSecrets...)
		guardSecMu.Unlock()

		if wErr := writeCleanReceipt(outPath, rc,
			func(raw string) error { return redactor.verifyJSONClean(raw, nil) },
			func(raw string) error { return act020NoHandles(redactor, raw) },
			func(raw string) error { return verifyAct026CookieValuesAbsent(raw, dyCookie) },
			func(raw string) error { return verifyAct027GuardSecretsAbsent(raw, secSnapshot) },
		); wErr != nil {
			rc.SecretScanClean = false
			rc.Passed = false
			if retErr == nil {
				retErr = wErr
			}
			return
		}
		if !rc.Passed && retErr == nil {
			retErr = errors.New("act027 completed with failures or stops")
		}
	}()

	// Step 0 (offline, 0 network requests): boolean presence of key Cookie names only.
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

	workDir, err = os.MkdirTemp("", "trace-"+act027ID+"-")
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
			MinInterval:    -1, // Enforced uniformly at >= 3.2s in act027Checker.wrap.
			JitterMax:      -1,
			AllowLocalhost: cfg.AllowLocalhost,
		},
	})
	if err != nil {
		return rc, err
	}

	ctx, cancel := context.WithTimeout(parent, act027Timeout)
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

	seenIDs := map[string]bool{}
	countDupes := func(items []service.AccountItem) int {
		n := 0
		for _, it := range items {
			if it.ID == "" {
				continue
			}
			key := it.Kind + ":" + it.ID
			if seenIDs[key] {
				n++
			}
			seenIDs[key] = true
		}
		return n
	}
	var prevCursor string
	recordPage := func(f map[string]any, p service.AccountPage, isPage2 bool) {
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
		if isPage2 {
			f["cursorAdvanced"] = p.Cursor == "" || p.Cursor != prevCursor
		}
		prevCursor = p.Cursor
	}
	attachTelemetry := func(f map[string]any, cat string) {
		if tel, ok := ck.telemetryOf(cat); ok {
			f["httpStatus"] = tel.HTTPStatus
			f["contentType"] = tel.ContentType
			f["emptyBody"] = tel.EmptyBody
			f["sentGuardHandshake"] = tel.SentGuardHandshake
			f["sentGuardSigned"] = tel.SentGuardSigned
			f["hasBDTicketGuardResult"] = tel.HasBDTicketGuardResult
			f["hasBDTicketGuardResultCode"] = tel.HasBDTicketGuardResultCode
			if tel.BDTicketGuardResultCode != nil {
				f["bdTicketGuardResultCode"] = *tel.BDTicketGuardResultCode
			}
			f["hasServerDataHeader"] = tel.HasServerDataHeader
			f["serverIssuedTicket"] = tel.ServerIssuedTicket
			f["serverIssuedTsSign"] = tel.ServerIssuedTsSign
			f["serverIssuedClientCert"] = tel.ServerIssuedClientCert
			f["hasBDTuringVerify"] = tel.HasBDTuringVerify
			if tel.ListShape != "" {
				f["listShape"] = tel.ListShape
			}
			if len(tel.TopLevelKeys) > 0 {
				f["topLevelKeys"] = tel.TopLevelKeys
			}
		}
	}
	attachStoreGuardFlags := func(f map[string]any) {
		dyStore := backend.CredentialManager().Douyin()
		if dyStore == nil {
			return
		}
		recordGuardSecrets(dyStore.DouyinGuardSecrets())
		g := dyStore.DouyinGuard()
		f["storeHasGuardKeyPair"] = g != nil && g.PrivateKeyPKCS8 != "" && g.PublicKeySEC1 != ""
		f["storeHasGuardTicket"] = g != nil && g.Ticket != ""
		f["storeHasGuardTsSign"] = g != nil && g.TsSign != ""
		f["storeHasGuardClientCert"] = g != nil && g.ClientCert != ""
	}

	// Step 1: dy_login (profile/self handshake advertising our fresh ECDSA P-256 public key).
	loginOK := runStep("dy_login", func(f map[string]any) error {
		st, lErr := backend.LoginWithCookie(ctx, "douyin", dyCookie, "")
		attachTelemetry(f, "dy_profile_self")
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID, st.Uname)
		redactor.addURLs(st.Face)
		redactor.addCookieValuesFromStore(backend.CredentialManager().Douyin())
		attachStoreGuardFlags(f)
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

	// Step 2: D-V2_likes (aweme/favorite page 1, and page 2 when has_more).
	runStep("D-V2_likes", func(f map[string]any) error {
		hadTsSignBefore := false
		if dyStore := backend.CredentialManager().Douyin(); dyStore != nil {
			if g := dyStore.DouyinGuard(); g != nil && g.TsSign != "" {
				hadTsSignBefore = true
			}
		}

		p1, lErr := backend.AccountPage(ctx, "douyin", service.AccountLikes, "", "")
		attachTelemetry(f, "dy_favorite")
		attachStoreGuardFlags(f)

		// If page 1 returned 0B unavailable ONLY while simultaneously issuing a brand-new ts_sign
		// for our P-256 keypair (and no stop condition tripped), exercise the 2nd favorite slot
		// once with the newly signed bd-ticket-guard-client-data header.
		hasTsSignAfter := false
		if dyStore := backend.CredentialManager().Douyin(); dyStore != nil {
			if g := dyStore.DouyinGuard(); g != nil && g.TsSign != "" {
				hasTsSignAfter = true
			}
		}
		if lErr != nil && site.IsKind(lErr, site.ErrKindUnavailable) && !hadTsSignBefore && hasTsSignAfter && ck.stopReason() == "" {
			p1Retry, errRetry := backend.AccountPage(ctx, "douyin", service.AccountLikes, "", "")
			fRetry := map[string]any{}
			attachTelemetry(fRetry, "dy_favorite")
			attachStoreGuardFlags(fRetry)
			if errRetry != nil {
				f["retryAfterTicket"] = fRetry
				return errRetry
			}
			recordPage(fRetry, p1Retry, false)
			fRetry["dupesPage1"] = countDupes(p1Retry.Items)
			f["retryAfterTicket"] = fRetry
			return nil
		}

		if lErr != nil {
			return lErr
		}
		recordPage(f, p1, false)
		f["dupesPage1"] = countDupes(p1.Items)
		if p1.HasMore && ck.stopReason() == "" {
			p2, err2 := backend.AccountPage(ctx, "douyin", service.AccountLikes, "", p1.Cursor)
			f2 := map[string]any{}
			attachTelemetry(f2, "dy_favorite")
			attachStoreGuardFlags(f2)
			if err2 != nil {
				f["page2"] = f2
				return err2
			}
			recordPage(f2, p2, true)
			f2["dupesAcrossPages"] = countDupes(p2.Items)
			f["page2"] = f2
		}
		return nil
	})

	return rc, nil
}
