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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/douyin/guard"
	"github.com/chialecode/trace/internal/site/douyin/sign"
	"github.com/chialecode/trace/internal/sitecheck"
)

// ACT-029: bounded Douyin read-only production-chain verification under the standing
// Douyin authorization (docs/dev-rules/workflow.md §1.2).
// Exercises the exact desktop production path:
//
//	service.NewBackend -> LoginWithCookie -> DPAPI-encrypted credential.Store write ->
//	fresh credential.OpenDouyin + fresh service.NewBackend reload ->
//	Backend.AccountPage(site.Douyin, AccountLikes) page 1 & page 2.
//
// Strictly capped at 1 run, <= 5 API requests (dy_profile_self=1, dy_favorite=3, dy_spare=1),
// 0 media/CDN requests, >= 3.2s spacing, with panic-safe deferred receipt persistence
// and exhaustive Cookie + guard secret scanning.
const (
	act029ID             = "act029"
	act029Label          = "ACT-029"
	act029MaxRuns        = 1
	act029MaxAPI         = 5
	act029DefaultSpacing = 3200 * time.Millisecond
	act029Timeout        = 3 * time.Minute
)

var act029Limits = map[string]int{
	"dy_profile_self": 1,
	"dy_favorite":     3,
	"dy_spare":        1,
}

type act029Config struct {
	RootDir        string
	OutPath        string
	MinSpacing     time.Duration // 0 => 3.2s default; <0 => 0s in synthetic tests
	Endpoints      douyin.Endpoints
	Transport      http.RoundTripper
	AllowLocalhost bool
	StepHook       func(stepName string)
}

// act029RespTelemetry records strictly non-sensitive transport, protocol, and shape flags.
// Never records Cookie values, keys/tickets, ts_sign, req_sign, or raw header values.
type act029RespTelemetry struct {
	HTTPStatus                 int      `json:"httpStatus"`
	Proto                      string   `json:"proto,omitempty"`
	ContentType                string   `json:"contentType"`
	BodyBytes                  int      `json:"bodyBytes"`
	EmptyBody                  bool     `json:"emptyBody"`
	ListShape                  string   `json:"listShape,omitempty"`
	TopLevelKeys               []string `json:"topLevelKeys,omitempty"`
	ResponseHeaderKeys         []string `json:"responseHeaderKeys,omitempty"`
	SentCookieBytes            int      `json:"sentCookieBytes"`
	SentCookieHasSVWebID       bool     `json:"sentCookieHasSVWebID"`
	SentCookieHasUIFID         bool     `json:"sentCookieHasUIFID"`
	SentCookieHasMSToken       bool     `json:"sentCookieHasMSToken"`
	SentStage2Sig              bool     `json:"sentStage2Sig"`
	SentVerifyFpParam          bool     `json:"sentVerifyFpParam"`
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

type act029Receipt struct {
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

func act029Classify(r *http.Request) string {
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

func hasBDTicketGuardKey(pairs map[string]string) bool {
	for k := range pairs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(k)), "bd_ticket_guard_") {
			return true
		}
	}
	return false
}

type act029Checker struct {
	gate       *sitecheck.Gate
	apiLimits  map[string]int
	minSpacing time.Duration

	paceMu    sync.Mutex
	lastReqAt time.Time

	mu       sync.Mutex
	stopped  string
	statuses map[string]int
	lastTel  map[string]act029RespTelemetry
}

func (c *act029Checker) stop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

func (c *act029Checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *act029Checker) telemetryOf(cat string) (act029RespTelemetry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tel, ok := c.lastTel[cat]
	return tel, ok
}

func (c *act029Checker) waitSpacing(ctx context.Context) error {
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

func (c *act029Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		cat := act029Classify(r)
		if cat == "" {
			return nil, fmt.Errorf("%w: endpoint outside act029 authorized API list", sitecheck.ErrQuotaExceeded)
		}
		if reason := c.stopReason(); reason != "" {
			return nil, fmt.Errorf("act029 halted (%s)", reason)
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

		sentCookie := r.Header.Get("Cookie")
		sentPairs := parseAct026RawPairs(sentCookie)
		sentHandshake := strings.TrimSpace(r.Header.Get(guard.HeaderREEPublicKey)) != ""
		sentSigned := strings.TrimSpace(r.Header.Get(guard.HeaderClientData)) != "" &&
			strings.TrimSpace(r.Header.Get(guard.HeaderWebSignType)) != ""
		q := r.URL.Query()
		sentStage2 := strings.TrimSpace(r.Header.Get("x-secsdk-web-signature")) != "" &&
			strings.TrimSpace(q.Get("x-secsdk-web-signature")) != ""
		sentVerifyFp := strings.TrimSpace(q.Get("verifyFp")) != ""

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
		respHeaderKeys := make([]string, 0, len(resp.Header))
		for k, vals := range resp.Header {
			respHeaderKeys = append(respHeaderKeys, strings.ToLower(k))
			if len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
				flatHeaders[k] = vals[0]
			}
		}
		sort.Strings(respHeaderKeys)
		rawSetCookies := make(map[string]string)
		for _, ck := range resp.Cookies() {
			if ck != nil && ck.Value != "" {
				rawSetCookies[ck.Name] = ck.Value
			}
		}
		srvGuard := guard.ParseServerResponse(flatHeaders, rawSetCookies)
		shape, topKeys := act026InspectBodyShape(raw)

		tel := act029RespTelemetry{
			HTTPStatus:                 resp.StatusCode,
			Proto:                      resp.Proto,
			ContentType:                act026SanitizeContentType(resp.Header.Get("Content-Type")),
			BodyBytes:                  len(raw),
			EmptyBody:                  len(bytes.TrimSpace(raw)) == 0,
			ListShape:                  shape,
			TopLevelKeys:               topKeys,
			ResponseHeaderKeys:         respHeaderKeys,
			SentCookieBytes:            len(sentCookie),
			SentCookieHasSVWebID:       sentPairs["s_v_web_id"] != "",
			SentCookieHasUIFID:         sign.ExtractUIFID(sentPairs) != "",
			SentCookieHasMSToken:       sentPairs["msToken"] != "",
			SentStage2Sig:              sentStage2,
			SentVerifyFpParam:          sentVerifyFp,
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

func runAct029Main(root, out string) {
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", act029ID+".json")
	}
	rc, err := runAct029(context.Background(), act029Config{
		RootDir: root,
		OutPath: out,
	})
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d/%d media=%d/%dB receipt=%s\n",
		act029Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, act029MaxAPI,
		rc.MediaUsage.Requests, rc.MediaUsage.Bytes, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-28s ok=%v skipped=%v %s %v\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Facts)
	}
	for k, v := range rc.Verdicts {
		fmt.Printf("  %s: %s\n", k, v)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", act029Label, err)
		os.Exit(1)
	}
}

func runAct029(parent context.Context, cfg act029Config) (rc act029Receipt, retErr error) {
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", act029ID+".json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act029DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	commit := resolveReceiptCommit(".")
	if commit == "" {
		commit = resolveReceiptCommit(root)
	}
	rc = act029Receipt{
		Act:       act029Label,
		Commit:    commit,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Limits: map[string]any{
			"maxRuns":        act029MaxRuns,
			"apiTotal":       act029MaxAPI,
			"categories":     act029Limits,
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
		ActID:         act029ID,
		MaxRuns:       act029MaxRuns,
		MaxAPICalls:   act029MaxAPI,
		MaxMediaFiles: 0,
		MaxMediaBytes: 0,
		MaxByCategory: act029Limits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act029Checker{
		gate:       gate,
		apiLimits:  act029Limits,
		minSpacing: spacing,
		statuses:   make(map[string]int),
		lastTel:    make(map[string]act029RespTelemetry),
	}

	var (
		workDir   string
		backend1  *service.Backend
		backend2  *service.Backend
		closeOnce sync.Once
	)
	closeBackend := func(b **service.Backend) {
		if b == nil || *b == nil {
			return
		}
		if dyStore := (*b).CredentialManager().Douyin(); dyStore != nil {
			redactor.addCookieValuesFromStore(dyStore)
			recordGuardSecrets(dyStore.DouyinGuardSecrets())
		}
		_ = (*b).Close()
		*b = nil
	}
	closeResources := func() bool {
		closeOnce.Do(func() {
			closeBackend(&backend1)
			closeBackend(&backend2)
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
				retErr = errors.New("act029 aborted after recovered panic")
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
			retErr = errors.New("act029 completed with failures or stops")
		}
	}()

	// Step 0 (offline, 0 network requests): byte counts, key counts, and boolean presence before/after cleaning.
	cleanedHeader := credential.FormatDouyinCookieHeader(dyClean)
	rc.Steps = append(rc.Steps, step{
		Name: "step0_cookie_offline_check",
		OK:   true,
		Facts: map[string]any{
			"rawCookieBytes":              len(dyCookie),
			"rawKeyCount":                 len(rawPairs),
			"cleanedKeyCount":             len(dyClean),
			"cleanedHeaderBytes":          len(cleanedHeader),
			"raw_has_sessionid":           rawPairs["sessionid"] != "" || rawPairs["sessionid_ss"] != "",
			"raw_has_sid_ucp_v1":          rawPairs["sid_ucp_v1"] != "",
			"raw_has_s_v_web_id":          rawPairs["s_v_web_id"] != "",
			"raw_has_uifid":               sign.ExtractUIFID(rawPairs) != "",
			"raw_has_msToken":             rawPairs["msToken"] != "",
			"raw_has_d_ticket":            rawPairs["d_ticket"] != "",
			"raw_has_bd_ticket_guard":     hasBDTicketGuardKey(rawPairs),
			"cleaned_has_sessionid":       dyClean["sessionid"] != "" || dyClean["sessionid_ss"] != "",
			"cleaned_has_sid_ucp_v1":      dyClean["sid_ucp_v1"] != "",
			"cleaned_has_s_v_web_id":      dyClean["s_v_web_id"] != "",
			"cleaned_has_uifid":           sign.ExtractUIFID(dyClean) != "",
			"cleaned_has_msToken":         dyClean["msToken"] != "",
			"cleaned_has_d_ticket":        dyClean["d_ticket"] != "",
			"cleaned_has_bd_ticket_guard": hasBDTicketGuardKey(dyClean),
		},
	})
	if cfg.StepHook != nil {
		cfg.StepHook("step0_cookie_offline_check")
	}

	workDir, err = os.MkdirTemp("", "trace-"+act029ID+"-")
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

	openBackend := func() (*service.Backend, error) {
		return service.NewBackend(service.Options{
			Locations:        loc,
			Desktop:          service.NewMockDesktop(),
			DisableBgWorkers: true,
			DouyinOpts: douyin.ClientOptions{
				Endpoints:      cfg.Endpoints,
				Transport:      cfg.Transport,
				WrapTransport:  ck.wrap,
				MinInterval:    -1, // Enforced uniformly at >= 3.2s in act029Checker.wrap.
				JitterMax:      -1,
				AllowLocalhost: cfg.AllowLocalhost,
			},
		})
	}

	backend1, err = openBackend()
	if err != nil {
		return rc, err
	}

	ctx, cancel := context.WithTimeout(parent, act029Timeout)
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
		f["cursorNonEmpty"] = strings.TrimSpace(p.Cursor) != ""
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
			if tel.Proto != "" {
				f["proto"] = tel.Proto
			}
			f["contentType"] = tel.ContentType
			f["bodyBytes"] = tel.BodyBytes
			f["emptyBody"] = tel.EmptyBody
			f["sentCookieBytes"] = tel.SentCookieBytes
			f["sentCookieHasSVWebID"] = tel.SentCookieHasSVWebID
			f["sentCookieHasUIFID"] = tel.SentCookieHasUIFID
			f["sentCookieHasMSToken"] = tel.SentCookieHasMSToken
			f["sentStage2Sig"] = tel.SentStage2Sig
			f["sentVerifyFpParam"] = tel.SentVerifyFpParam
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
			if len(tel.ResponseHeaderKeys) > 0 {
				f["responseHeaderKeys"] = tel.ResponseHeaderKeys
			}
		}
	}

	// Call 1: production LoginWithCookie -> DPAPI encrypted save to douyin.bin.
	loginOK := runStep("call1_prod_login", func(f map[string]any) error {
		st, lErr := backend1.LoginWithCookie(ctx, string(site.Douyin), dyCookie, "")
		attachTelemetry(f, "dy_profile_self")
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID, st.Uname)
		redactor.addURLs(st.Face)
		if dyStore := backend1.CredentialManager().Douyin(); dyStore != nil {
			redactor.addCookieValuesFromStore(dyStore)
			recordGuardSecrets(dyStore.DouyinGuardSecrets())
			g := dyStore.DouyinGuard()
			f["storeHasGuardKeyPair"] = g != nil && g.PrivateKeyPKCS8 != "" && g.PublicKeySEC1 != ""
			f["storeHasGuardTsSign"] = g != nil && g.TsSign != ""
			f["storeHasSVWebID"] = dyStore.CookieValue("s_v_web_id") != ""
			f["storeHasSidUcpV1"] = dyStore.CookieValue("sid_ucp_v1") != ""
			f["storeHasUIFID"] = dyStore.UIFID() != ""
			f["storeHasMSToken"] = dyStore.MSToken() != ""
		}
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

	// Close backend1 so douyin.bin is strictly reopened from disk via DPAPI.
	closeBackend(&backend1)

	// Offline DPAPI round-trip reload check (0 requests): verify douyin.bin decrypts cleanly
	// and preserves s_v_web_id, sid_ucp_v1, uifid, sessionid, and profile fields.
	reloadOK := runStep("step1b_store_dpapi_reload", func(f map[string]any) error {
		binPath := filepath.Join(loc.CredentialsDir, credential.DouyinFileName)
		info, sErr := os.Stat(binPath)
		if sErr != nil {
			return fmt.Errorf("douyin.bin missing after LoginWithCookie: %w", sErr)
		}
		f["douyinBinExists"] = true
		f["douyinBinBytes"] = info.Size()

		reloaded, backup, oErr := credential.OpenDouyin(binPath)
		if oErr != nil {
			return fmt.Errorf("credential.OpenDouyin failed: %w", oErr)
		}
		redactor.addCookieValuesFromStore(reloaded)
		recordGuardSecrets(reloaded.DouyinGuardSecrets())

		st := reloaded.Status()
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID, st.Uname)
		redactor.addURLs(st.Face)

		g := reloaded.DouyinGuard()
		reloadedCookies := reloaded.Cookies()
		f["backupCreated"] = backup != ""
		f["activeCredential"] = reloaded.HasActiveCredential()
		f["loggedIn"] = st.LoggedIn
		f["hasUID"] = st.UID != ""
		f["hasSecUID"] = reAct010SecUID.MatchString(st.SecUID)
		f["hasNickname"] = st.Uname != ""
		f["reloadedCookieCount"] = len(reloadedCookies)
		f["reloadedCookieHeaderBytes"] = len(reloaded.CookieHeader())
		f["reloaded_has_sessionid"] = reloaded.CookieValue("sessionid") != "" || reloaded.CookieValue("sessionid_ss") != ""
		f["reloaded_has_sid_ucp_v1"] = reloaded.CookieValue("sid_ucp_v1") != ""
		f["reloaded_has_s_v_web_id"] = reloaded.CookieValue("s_v_web_id") != ""
		f["reloaded_has_uifid"] = reloaded.UIFID() != ""
		f["reloaded_has_msToken"] = reloaded.MSToken() != ""
		f["reloaded_has_d_ticket"] = reloaded.CookieValue("d_ticket") != ""
		f["reloaded_has_bd_ticket_guard"] = hasBDTicketGuardKey(reloadedCookies)
		f["reloadedHasGuardKeyPair"] = g != nil && g.PrivateKeyPKCS8 != "" && g.PublicKeySEC1 != ""
		f["reloadedHasGuardTsSign"] = g != nil && g.TsSign != ""

		if backup != "" || !reloaded.HasActiveCredential() || !st.LoggedIn {
			return errors.New("reloaded douyin credential store is not active")
		}
		if rawPairs["s_v_web_id"] != "" && reloaded.CookieValue("s_v_web_id") == "" {
			return errors.New("s_v_web_id lost across DPAPI save/reload")
		}
		if rawPairs["sid_ucp_v1"] != "" && reloaded.CookieValue("sid_ucp_v1") == "" {
			return errors.New("sid_ucp_v1 lost across DPAPI save/reload")
		}
		if sign.ExtractUIFID(rawPairs) != "" && reloaded.UIFID() == "" {
			return errors.New("uifid lost across DPAPI save/reload")
		}
		return nil
	})
	if !reloadOK && ck.stopReason() == "" {
		ck.stop("store_reload_failed")
	}

	// Open fresh backend2 against the same loc so AccountPage uses the DPAPI-reloaded Store.
	if ck.stopReason() == "" {
		backend2, err = openBackend()
		if err != nil {
			return rc, err
		}
	}

	// Call 2: production Backend.AccountPage(ctx, site.Douyin, AccountLikes, "", "") page 1.
	var page1 service.AccountPage
	page1OK := runStep("call2_prod_likes_page1", func(f map[string]any) error {
		if backend2 == nil {
			return errSkip
		}
		p1, pErr := backend2.AccountPage(ctx, string(site.Douyin), service.AccountLikes, "", "")
		attachTelemetry(f, "dy_favorite")
		if pErr != nil {
			return pErr
		}
		page1 = p1
		recordPage(f, p1, false)
		f["dupesPage1"] = countDupes(p1.Items)
		return nil
	})

	// Call 3: production Backend.AccountPage page 2 when page 1 succeeded and HasMore==true.
	runStep("call3_prod_likes_page2", func(f map[string]any) error {
		if backend2 == nil || !page1OK || !page1.HasMore {
			return errSkip
		}
		p2, pErr := backend2.AccountPage(ctx, string(site.Douyin), service.AccountLikes, "", page1.Cursor)
		attachTelemetry(f, "dy_favorite")
		if pErr != nil {
			return pErr
		}
		recordPage(f, p2, true)
		f["dupesAcrossPages"] = countDupes(p2.Items)
		return nil
	})

	return rc, nil
}
