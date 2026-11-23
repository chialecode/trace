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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/sitecheck"
)

type act024Receipt struct {
	Act               string                             `json:"act"`
	Commit            string                             `json:"commit,omitempty"`
	StartedAt         string                             `json:"startedAt"`
	FinishedAt        string                             `json:"finishedAt"`
	Passed            bool                               `json:"passed"`
	StoppedBy         string                             `json:"stoppedBy,omitempty"`
	TempDataDeleted   bool                               `json:"tempDataDeleted"`
	SecretScanClean   bool                               `json:"secretScanClean"`
	PreUsedRequests   int                                `json:"preUsedRequests,omitempty"`
	PreUsedLoginCalls int                                `json:"preUsedLoginCalls,omitempty"`
	Steps             []step                             `json:"steps"`
	Verifications     map[string]any                     `json:"verifications"`
	VerdictHints      map[string]string                  `json:"verdictHints"`
	APIUsage          sitecheck.Usage                    `json:"apiUsage"`
	OperationCounts   map[string]int                     `json:"operationCounts"`
	StatusCounts      map[string]int                     `json:"httpStatusCounts"`
	RateLimits        map[string]act024RateLimitSnapshot `json:"rateLimits,omitempty"`
	ShortLinkHops     mediaUsage                         `json:"shortLinkHops"`
	MediaUsage        mediaUsage                         `json:"mediaUsage"`
	FrontendBytes     int64                              `json:"frontendBytes"`
	Redirects         []map[string]any                   `json:"redirects,omitempty"`
	Limits            map[string]any                     `json:"limits"`
	Notes             []string                           `json:"notes,omitempty"`
}

type xActSpec struct {
	id               string
	label            string
	maxRuns          int
	maxTotalRequests int
	apiLimits        map[string]int
}

var (
	xAct024Spec = xActSpec{
		id:               act024ID,
		label:            act024Label,
		maxRuns:          act024MaxRuns,
		maxTotalRequests: act024MaxTotalRequests,
		apiLimits:        act024APILimits,
	}
	xAct025Spec = xActSpec{
		id:               act025ID,
		label:            act025Label,
		maxRuns:          act024MaxRuns,
		maxTotalRequests: act024MaxTotalRequests,
		apiLimits:        act025APILimits,
	}
)

func runAct024Main(root, out string) {
	runXActMain(root, out, xAct024Spec)
}

func runAct025Main(root, out string) {
	runXActMain(root, out, xAct025Spec)
}

func runXActMain(root, out string, spec xActSpec) {
	rc, err := runXAct(context.Background(), spec, act024Config{
		RootDir: root,
		OutPath: out,
	})
	if err != nil && rc.StartedAt == "" {
		fatal(err.Error())
	}
	outDisp := out
	if outDisp == "" {
		outDisp = filepath.Join(root, "artifacts", "site-check", spec.id+".json")
	}
	fmt.Printf("%s passed=%v stoppedBy=%q requests=%d/%d shortHops=%d media=%d/%dB receipt=%s\n",
		spec.label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, spec.maxTotalRequests,
		rc.ShortLinkHops.Requests, rc.MediaUsage.Requests, rc.MediaUsage.Bytes, outDisp)
	for _, s := range rc.Steps {
		fmt.Printf("  %-34s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", spec.label, err)
		os.Exit(1)
	}
	if !rc.Passed {
		os.Exit(1)
	}
}

func runAct024(parentCtx context.Context, cfg act024Config) (act024Receipt, error) {
	return runXAct(parentCtx, xAct024Spec, cfg)
}

func runAct025(parentCtx context.Context, cfg act024Config) (act024Receipt, error) {
	return runXAct(parentCtx, xAct025Spec, cfg)
}

func runXAct(parentCtx context.Context, spec xActSpec, cfg act024Config) (act024Receipt, error) {
	var rc act024Receipt
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", spec.id+".json")
	}
	spacing := cfg.MinSpacing
	jitter := cfg.JitterMax
	if spacing == 0 {
		spacing = act024DefaultMinSpacing
		if jitter == 0 {
			jitter = act024DefaultJitterMax
		}
	} else if spacing < 0 {
		spacing = 0
		jitter = 0
	}
	if jitter < 0 {
		jitter = 0
	}

	tg, parsedUsers, parsedProtectedUsers, parsedPosts, err := loadAndValidateAct024Targets(root, cfg.AllowLocalhost)
	if err != nil {
		return rc, err
	}
	rawCookie, cleanedInitial, err := readAndValidateXCookie(root)
	if err != nil {
		return rc, err
	}

	redactor := newAct024Redactor()
	redactor.addCookieHeader(rawCookie)
	redactor.addCookieMap(cleanedInitial)
	redactor.addTargets(tg, parsedUsers, parsedProtectedUsers, parsedPosts)

	ledgerDir := filepath.Join(root, ".local", "site-check")
	gate, err := sitecheck.OpenGate(ledgerDir, sitecheck.Limits{
		ActID:         spec.id,
		MaxRuns:       spec.maxRuns,
		MaxAPICalls:   spec.maxTotalRequests,
		MaxMediaFiles: act024MaxMediaRequests,
		MaxMediaBytes: act024MaxMediaBytes,
		MaxByCategory: spec.apiLimits,
	})
	if err != nil {
		return rc, err
	}
	preUsage := gate.Usage()
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	endpoints := cfg.Endpoints
	if endpoints.HomeURL == "" {
		endpoints.HomeURL = x.DefaultHomeURL
	}
	if endpoints.AccountSettings == "" {
		endpoints.AccountSettings = x.DefaultAccountSettingsURL
	}
	if endpoints.GraphQLBase == "" {
		endpoints.GraphQLBase = x.DefaultGraphQLBaseURL
	}

	ck := &act024Checker{
		actID:          spec.id,
		gate:           gate,
		apiLimits:      spec.apiLimits,
		shortLink:      &mediaBudget{name: "shortlink", maxRequests: act024MaxShortLinkHops, maxBytes: act024MaxShortLinkBytes},
		media:          &mediaBudget{name: "media", maxRequests: act024MaxMediaRequests, maxBytes: act024MaxMediaBytes},
		minSpacing:     spacing,
		jitterMax:      jitter,
		allowLocalhost: cfg.AllowLocalhost,
		endpoints:      endpoints,
		redactor:       redactor,
		latestCT0:      cleanedInitial["ct0"],
		statuses:       make(map[string]int),
		opCounts:       make(map[string]int),
		rateLimits:     make(map[string]act024RateLimitSnapshot),
		cdnHostClasses: make(map[string]int),
	}

	workDir, err := os.MkdirTemp("", "trace-"+spec.id+"-")
	if err != nil {
		return rc, err
	}
	cleanupWorkDir := func() bool {
		_ = os.RemoveAll(workDir)
		_, statErr := os.Stat(workDir)
		return os.IsNotExist(statErr)
	}
	defer cleanupWorkDir()

	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:      "dev",
		ForceMode:    "installed",
		LocalAppData: filepath.Join(workDir, "localappdata"),
		DocumentsDir: filepath.Join(workDir, "documents"),
	})
	if err != nil {
		return rc, err
	}

	var wrappedAssetRT http.RoundTripper
	if cfg.AssetTransport != nil {
		wrappedAssetRT = ck.wrap(cfg.AssetTransport)
	}

	backend, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		XOpts: x.ClientOptions{
			Endpoints:          endpoints,
			Transport:          cfg.Transport,
			AssetTransport:     wrappedAssetRT,
			WrapTransport:      ck.wrap,
			MinInterval:        -1, // Unified pacing (>= 5s + jitter) enforced by act024Checker.
			JitterMax:          -1,
			RateLimitJitterMax: -1,
			AllowLocalhost:     cfg.AllowLocalhost,
			TxIDOptions:        cfg.TxIDOptions,
			OnCT0Updated: func(newCT0 string) {
				redactor.addSecrets(newCT0)
				ck.setLatestCT0(newCT0)
			},
		},
	})
	if err != nil {
		return rc, err
	}
	closed := false
	closeBackend := func() {
		if !closed {
			closed = true
			_ = backend.Close()
		}
	}
	defer closeBackend()

	// Route formal DownloadManager media traffic through ck.wrapMedia (cookieless, no Referer, <= 5 reqs & <= 25 MiB).
	backend.DownloadManager().SetMediaTransportWrap(func(base http.RoundTripper) http.RoundTripper {
		if cfg.MediaTransport != nil {
			base = cfg.MediaTransport
		}
		return ck.wrapMedia(base)
	})

	xClient := backend.XClient()

	rawAPIBase := cfg.Transport
	if rawAPIBase == nil {
		var base http.RoundTripper = &http.Transport{
			Proxy:                 site.Proxy,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: x.DefaultAPITimeout,
		}
		if cfg.AllowLocalhost {
			base = site.LoopbackTransport(base)
		}
		rawAPIBase = base
	}
	rawAPIHTTP := &http.Client{
		Transport: ck.wrap(rawAPIBase),
		Timeout:   x.DefaultAPITimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	probeMediaBase := cfg.MediaTransport
	if probeMediaBase == nil {
		mediaGuard := site.NewGuardedMediaDialer(site.X, cfg.AllowLocalhost, &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}, "blocked internal X media host")
		var base http.RoundTripper = &http.Transport{
			Proxy:                 mediaGuard.Proxy,
			DialContext:           mediaGuard.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}
		if cfg.AllowLocalhost {
			base = site.LoopbackTransport(base)
		}
		probeMediaBase = base
	}
	defaultMediaSpec := xClient.MediaTransport()
	probeMediaHTTP := &http.Client{
		Transport: ck.wrapMedia(probeMediaBase),
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			req.Header.Del("X-Csrf-Token")
			req.Header.Del("Referer")
			req.Header.Del("Origin")
			if len(via) >= 3 {
				return errors.New("x media redirect limit exceeded")
			}
			host := strings.ToLower(req.URL.Hostname())
			if !(cfg.AllowLocalhost && site.IsLoopbackHost(host)) {
				if err := defaultMediaSpec.ValidateRedirectURL(req.URL.String()); err != nil {
					return err
				}
			}
			defaultMediaSpec.ApplyHeaders(req)
			return nil
		},
	}

	rc = act024Receipt{
		Act:               spec.label,
		Commit:            resolveReceiptCommit(root),
		StartedAt:         time.Now().UTC().Format(time.RFC3339),
		PreUsedRequests:   preUsage.APICalls,
		PreUsedLoginCalls: preUsage.Categories["login"],
		Verifications:     make(map[string]any),
		VerdictHints:      make(map[string]string),
		Limits: map[string]any{
			"categories":      spec.apiLimits,
			"totalRequests":   spec.maxTotalRequests,
			"frontendBytes":   act024MaxFrontendBytes,
			"shortLinkHops":   act024MaxShortLinkHops,
			"mediaRequests":   act024MaxMediaRequests,
			"mediaBytes":      act024MaxMediaBytes,
			"minSpacingMs":    spacing.Milliseconds(),
			"jitterMaxMs":     jitter.Milliseconds(),
			"timeoutSec":      int(act024Timeout.Seconds()),
			"allowLocalhost":  cfg.AllowLocalhost,
			"preflightReused": preUsage.APICalls > 0,
		},
	}

	ctx, cancel := context.WithTimeout(parentCtx, act024Timeout)
	defer cancel()

	runner := newXStepRunner(&rc.Steps, ck, redactor)

	// -------------------------------------------------------------------------
	// Step 1: V-04 (frontend discovery) + V-01 (2-step login & unauth control)
	//         + V-02 (ct0 passive sync & intentional x-csrf-token mismatch).
	// Budget: frontend <= 3..5 (core 3 + optional lazy chunks in Step 6), login <= 4..5.
	// -------------------------------------------------------------------------
	var selfRestID string
	var frontendDiscoveryOK bool
	step1OK := runner.run("v04_v01_v02_frontend_login_and_csrf", func(f map[string]any) error {
		ck.setPhase("login")
		defer ck.setPhase("")

		f["preUsedLoginCalls"] = preUsage.Categories["login"]
		f["cookieInitialHasAuthToken"] = cleanedInitial["auth_token"] != ""
		f["cookieInitialHasCT0"] = cleanedInitial["ct0"] != ""
		f["cookieInitialHasTWID"] = cleanedInitial["twid"] != ""
		f["cookieInitialHasGuestID"] = cleanedInitial["guest_id"] != ""
		f["cookieInitialHasKDT"] = cleanedInitial["kdt"] != ""

		// 1a. Formal 2-step login via Backend.LoginWithCookie (triggers FrontendDiscovery
		//     x.com/home + main.*.js + ondemand.s.*.js [frontend 3], then settings.json
		//     + UserByScreenName [login 2 new calls]).
		st, loginErr := backend.LoginWithCookie(ctx, "x", rawCookie, "")
		meta := xClient.Discovery().Current()
		if meta != nil {
			redactor.addFrontendMetadata(meta)
		}
		frontendDiscoveryOK = meta != nil && meta.Bearer != "" && meta.TxID != nil
		if loginErr != nil {
			fTel := ck.snapshotFrontendTelemetry()
			rc.Verifications["V-04"] = map[string]any{
				"status":                  "failed",
				"frontendDiscoveryOK":     frontendDiscoveryOK,
				"homeHTTPStatus":          fTel.HomeHTTPStatus,
				"homeBytes":               fTel.HomeBytes,
				"homeHasVerificationMeta": fTel.HomeHasVerificationMeta,
				"homeHasLoadingSVG":       fTel.HomeHasLoadingSVG,
				"bundleKind":              fTel.MainJSBundleKind,
				"mainJSBytes":             fTel.MainJSBytes,
				"ondemandJSBytes":         fTel.OndemandJSBytes,
				"scriptsCookieless":       !ck.cookieOnStaticAssetHit,
			}
			return loginErr
		}
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("login stopped: %s", stopped)
		}

		selfRestID = strings.TrimSpace(st.UID)
		redactor.addIDs(selfRestID)
		redactor.addHandles(st.UniqueID, st.Uname)
		redactor.addURLs(st.Face)

		// Capture V-04 structural facts (ZERO Bearer, queryId, or verification key literals!).
		fTel := ck.snapshotFrontendTelemetry()
		opSummary, coreOpsCount := summarizeAct024Operations(meta)
		bundleKind := fTel.MainJSBundleKind
		if bundleKind == "" {
			if fTel.HomeHasViteModule {
				bundleKind = "vite"
			} else {
				bundleKind = "webpack"
			}
		}
		v04 := map[string]any{
			"status":                    "verified",
			"frontendDiscoveryOK":       frontendDiscoveryOK,
			"bundleKind":                bundleKind,
			"homeHTTPStatus":            fTel.HomeHTTPStatus,
			"homeBytes":                 fTel.HomeBytes,
			"homeHasVerificationMeta":   fTel.HomeHasVerificationMeta,
			"homeHasLoadingSVG":         fTel.HomeHasLoadingSVG,
			"mainJSBytes":               fTel.MainJSBytes,
			"ondemandJSBytes":           fTel.OndemandJSBytes,
			"lazyChunksFetched":         fTel.LazyChunkCount,
			"lazyChunkJSBytes":          fTel.LazyChunkBytes,
			"scriptsCookieless":         !ck.cookieOnStaticAssetHit,
			"bearerExtracted":           meta != nil && meta.Bearer != "",
			"txidInitialized":           meta != nil && meta.TxID != nil,
			"txidHeaderSentOnGraphQL":   ck.txidSentCount.Load() > 0,
			"xpffHeaderSent":            false,
			"coreOpsCount":              coreOpsCount,
			"requiredOpsCount":          len(opSummary),
			"operations":                opSummary,
			"bookmarkFoldersOpInBundle": ck.getBookmarkFoldersSpec() != nil,
		}
		rc.Verifications["V-04"] = v04

		v01 := map[string]any{
			"status":                        "verified",
			"preUsedLoginCalls":             preUsage.Categories["login"],
			"loggedIn":                      st.LoggedIn,
			"source":                        st.Source,
			"settingsAndUserByScreenNameOK": st.LoggedIn && selfRestID != "" && strings.TrimSpace(st.UniqueID) != "",
			"hasRestID":                     selfRestID != "",
			"hasScreenName":                 strings.TrimSpace(st.UniqueID) != "",
			"hasName":                       strings.TrimSpace(st.Uname) != "",
			"hasAvatar":                     strings.TrimSpace(st.Face) != "",
			"cookieHasTWID":                 cleanedInitial["twid"] != "",
			"cookieHasGuestID":              cleanedInitial["guest_id"] != "",
			"cookieHasKDT":                  cleanedInitial["kdt"] != "",
		}

		// 1b. Unauthenticated control on settings.json.
		if gate.Usage().Categories["login"] < spec.apiLimits["login"] {
			ck.guestControlActive.Store(true)
			guestSettings, guestErr := xClient.CheckSettingsGuest(ctx)
			ck.guestControlActive.Store(false)
			guestTel := ck.snapshotLastTelemetry()
			if guestSettings != nil {
				redactor.addHandles(guestSettings.ScreenName)
			}
			if stopped := ck.stopReason(); stopped != "" {
				rc.Verifications["V-01"] = v01
				return fmt.Errorf("guest settings control stopped: %s", stopped)
			}
			guestRejected := guestErr != nil && site.IsKind(guestErr, site.ErrKindAuthRequired)
			v01["guestHTTPStatus"] = guestTel.HTTPStatus
			v01["guestErrorCodes"] = guestTel.ErrorCodes
			v01["guestErrKind"] = site.KindOf(guestErr)
			v01["guestRejectedAuthRequired"] = guestRejected
			if guestErr == nil {
				v01["status"] = "failed"
				rc.Verifications["V-01"] = v01
				return errors.New("settings.json without Cookie unexpectedly succeeded")
			}
		} else {
			v01["guestControlStatus"] = "not-run"
		}
		rc.Verifications["V-01"] = v01

		// 1c. V-02 controlled intentional x-csrf-token mismatch on settings.json.
		v02 := map[string]any{
			"passiveCT0RotatedOnNormalCalls": ck.passiveCT0Rotated.Load(),
		}
		if gate.Usage().Categories["login"] < spec.apiLimits["login"] && meta != nil && ck.stopReason() == "" {
			curCT0 := ck.getLatestCT0()
			badCT0 := makeMismatchedCT0(curCT0)
			redactor.addSecrets(badCT0)

			reqCookies := make(map[string]string, len(cleanedInitial))
			for k, v := range cleanedInitial {
				reqCookies[k] = v
			}
			if curCT0 != "" {
				reqCookies["ct0"] = curCT0
			}

			csrfReq, rErr := http.NewRequestWithContext(ctx, http.MethodGet, xClient.Endpoints().AccountSettings, nil)
			if rErr != nil {
				return rErr
			}
			csrfReq.Header.Set("User-Agent", xClient.UserAgent())
			csrfReq.Header.Set("Accept", "*/*")
			csrfReq.Header.Set("Referer", x.DefaultReferer)
			csrfReq.Header.Set("Origin", x.DefaultOrigin)
			csrfReq.Header.Set("x-twitter-active-user", "yes")
			csrfReq.Header.Set("x-twitter-client-language", x.DefaultClientLanguage)
			csrfReq.Header.Set("x-twitter-auth-type", "OAuth2Session")
			csrfReq.Header.Set("Authorization", meta.BearerHeader())
			csrfReq.Header.Set("Cookie", formatCleanedXCookieHeader(reqCookies))
			csrfReq.Header.Set("x-csrf-token", badCT0)

			ck.expectedCSRFCtrlActive.Store(true)
			csrfResp, csrfErr := rawAPIHTTP.Do(csrfReq)
			ck.expectedCSRFCtrlActive.Store(false)
			var rotatedCT0 string
			if csrfResp != nil {
				if newCT0, ok := credential.ExtractXCT0FromSetCookies(csrfResp.Cookies()); ok {
					redactor.addSecrets(newCT0)
					if newCT0 != curCT0 {
						rotatedCT0 = newCT0
					}
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(csrfResp.Body, 4096))
				_ = csrfResp.Body.Close()
			}
			csrfTel := ck.snapshotLastTelemetry()
			if stopped := ck.stopReason(); stopped != "" {
				v02["status"] = "failed"
				rc.Verifications["V-02"] = v02
				return fmt.Errorf("csrf mismatch control stopped: %s", stopped)
			}
			if csrfErr != nil {
				v02["status"] = "failed"
				rc.Verifications["V-02"] = v02
				return csrfErr
			}
			if rotatedCT0 != "" {
				_ = backend.CredentialManager().X().SyncXCT0(rotatedCT0)
				ck.setLatestCT0(rotatedCT0)
			}
			got353 := false
			for _, code := range csrfTel.ErrorCodes {
				if code == 353 {
					got353 = true
					break
				}
			}
			v02["status"] = "verified"
			v02["mismatchHTTPStatus"] = csrfTel.HTTPStatus
			v02["mismatchErrorCodes"] = csrfTel.ErrorCodes
			v02["mismatchGot353"] = got353
			v02["mismatchSetCookieCT0Present"] = csrfTel.SetCookieCT0Present
			v02["mismatchSetCookieCT0Valid"] = csrfTel.SetCookieCT0Valid
			v02["mismatchSetCookieCT0Changed"] = csrfTel.SetCookieCT0Changed
		} else {
			v02["status"] = "not-run"
			v02["reason"] = "login budget exhausted before CSRF mismatch probe"
		}
		rc.Verifications["V-02"] = v02

		f["v04"] = v04
		f["v01"] = v01
		f["v02"] = v02
		return nil
	})
	if (!step1OK || !frontendDiscoveryOK || selfRestID == "") && ck.stopReason() == "" {
		reason := ""
		if len(rc.Steps) > 0 {
			reason = rc.Steps[0].ErrKind
		}
		if reason == "" {
			if !frontendDiscoveryOK {
				reason = site.ErrKindSchemaChanged
			} else if selfRestID == "" {
				reason = "login_not_established"
			} else {
				reason = "step1_failed"
			}
		}
		ck.tripStop(reason)
	}

	// -------------------------------------------------------------------------
	// Step 2: V-05 — Single post details (TweetDetail <= 3).
	// Prioritizes video, deletedOrigin, and tcoLink when >3 posts are supplied.
	// Uses Backend.XPost so b.xPostCache stays warm for Step 7 formal CacheXPost (0 extra API calls).
	// Runs BEFORE V-03 so a t.co URL can be extracted from the tcoLink post when shortLinks is empty.
	// -------------------------------------------------------------------------
	var (
		videoCacheCandidateID         string
		fallbackMediaCacheCandidateID string
		extractedTcoURL               string
		samplePhotoURL                string
		unauthProbeStatusID           string
	)
	if len(parsedPosts) > 0 {
		unauthProbeStatusID = parsedPosts[0].StatusID
	}

	runner.run("v05_post_details", func(f map[string]any) error {
		selected := selectAct024DetailPosts(parsedPosts, spec.apiLimits["postDetail"])
		if len(selected) == 0 {
			rc.Verifications["V-05"] = map[string]any{
				"status": "not-run",
				"reason": "no post targets provided",
			}
			return errSkip
		}

		ck.setPhase("postDetail")
		defer ck.setPhase("")

		postSummaries := make([]map[string]any, 0, len(selected))
		succeededCount := 0
		var lastPostErr error
		anyLongText := false
		anyThread := false
		anyOriginUnavailable := false
		anyVideo := false
		anyHLSOnly := false
		anyGIF := false
		anyPoll := false
		anyCard := false
		anyTcoEntity := false

		for idx, pp := range selected {
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("postDetail stopped: %s", stopped)
			}
			dto, pErr := backend.XPost(ctx, pp.StatusID)
			if pErr != nil {
				if stopped := ck.stopReason(); stopped != "" {
					return fmt.Errorf("postDetail stopped: %s", stopped)
				}
				errKind := site.KindOf(pErr)
				if errKind == site.ErrKindUnavailable || errKind == site.ErrKindForbidden {
					lastPostErr = pErr
					postSummaries = append(postSummaries, map[string]any{
						"index":   idx,
						"expect":  pp.Item.Expect,
						"errKind": errKind,
					})
					continue
				}
				return pErr
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("postDetail stopped: %s", stopped)
			}
			succeededCount++
			addAct024PostDTOToRedactor(redactor, dto)

			if succeededCount == 1 || unauthProbeStatusID == "" {
				unauthProbeStatusID = dto.ID
			}

			mediaKinds := map[string]int{}
			postHasVideo := false
			postHasGIF := false
			for _, m := range dto.Media {
				mediaKinds[m.Kind]++
				if m.Kind == x.MediaKindVideo {
					postHasVideo = true
				}
				if m.GIF || m.Kind == x.MediaKindGIF {
					postHasGIF = true
				}
				if samplePhotoURL == "" && m.Kind == x.MediaKindPhoto {
					if m.ImageURL != "" {
						samplePhotoURL = m.ImageURL
					} else if m.PublicPreviewURL != "" {
						samplePhotoURL = m.PublicPreviewURL
					}
				}
			}

			if videoCacheCandidateID == "" && (hasTag(pp.Item.Expect, "video") || postHasVideo) {
				videoCacheCandidateID = dto.ID
			}
			if fallbackMediaCacheCandidateID == "" && len(dto.Media) > 0 {
				fallbackMediaCacheCandidateID = dto.ID
			}

			tcoCand := extractAct024TcoFromPostDTO(dto, cfg.AllowLocalhost)
			if tcoCand != "" {
				anyTcoEntity = true
				redactor.addURLs(tcoCand)
				if u, uErr := url.Parse(tcoCand); uErr == nil {
					segs := site.SplitCleanPath(u.Path)
					if len(segs) > 0 {
						redactor.addIDs(segs[len(segs)-1])
					}
				}
				if extractedTcoURL == "" || hasTag(pp.Item.Expect, "tcoLink") {
					extractedTcoURL = tcoCand
				}
			}

			if dto.IsLongText {
				anyLongText = true
			}
			if len(dto.Thread) > 0 || len(dto.ThreadAncestors) > 0 {
				anyThread = true
			}
			if dto.OriginUnavailable {
				anyOriginUnavailable = true
			}
			if postHasVideo {
				anyVideo = true
			}
			if dto.HasHLSOnlyVideo {
				anyHLSOnly = true
			}
			if postHasGIF {
				anyGIF = true
			}
			if dto.Poll != nil {
				anyPoll = true
			}
			if dto.Card != nil {
				anyCard = true
			}

			entry := map[string]any{
				"index":                      idx,
				"expect":                     pp.Item.Expect,
				"ruleType":                   dto.RuleType,
				"isLongText":                 dto.IsLongText,
				"textRuneLen":                utf8.RuneCountInString(dto.Text),
				"threadAncestors":            len(dto.ThreadAncestors),
				"threadReplies":              len(dto.Thread),
				"isRetweet":                  dto.IsRetweet,
				"isQuote":                    dto.IsQuote,
				"originUnavailable":          dto.OriginUnavailable,
				"hasOriginUnavailableReason": strings.TrimSpace(dto.OriginUnavailableReason) != "",
				"isPreviewDisplay":           dto.IsPreviewDisplay,
				"hasVisibilityLabel":         strings.TrimSpace(dto.VisibilityLabel) != "",
				"isEdited":                   dto.IsEdited,
				"mediaCount":                 len(dto.Media),
				"mediaKinds":                 mediaKinds,
				"hasHLSOnlyVideo":            dto.HasHLSOnlyVideo,
				"hasPoll":                    dto.Poll != nil,
				"urlEntityCount":             len(dto.URLs),
				"hasTcoEntity":               tcoCand != "",
				"authorHasScreenName":        strings.TrimSpace(dto.Author.ScreenName) != "",
				"authorHasName":              strings.TrimSpace(dto.Author.Name) != "",
			}
			if dto.Card != nil {
				entry["cardKind"] = dto.Card.Kind
				entry["cardUnsupported"] = dto.Card.Unsupported
			}
			postSummaries = append(postSummaries, entry)
		}

		v05 := map[string]any{
			"status":                  "verified",
			"totalTargetPosts":        len(parsedPosts),
			"fetchedCount":            len(postSummaries),
			"succeededCount":          succeededCount,
			"cappedAtThree":           len(parsedPosts) > spec.apiLimits["postDetail"],
			"posts":                   postSummaries,
			"hasNoteTweet":            anyLongText,
			"hasThread":               anyThread,
			"hasDeletedOrUnavailOrig": anyOriginUnavailable,
			"hasVideo":                anyVideo,
			"hasHLSOnlyVideo":         anyHLSOnly,
			"hasGIF":                  anyGIF,
			"hasPoll":                 anyPoll,
			"hasCard":                 anyCard,
			"hasTcoEntity":            anyTcoEntity,
			"uncoveredExpectTags":     missingPostExpectTags(parsedPosts),
		}
		if succeededCount == 0 {
			v05["status"] = "failed"
			rc.Verifications["V-05"] = v05
			f["v05"] = v05
			if lastPostErr != nil {
				return lastPostErr
			}
			return errors.New("all selected posts were unavailable")
		}
		rc.Verifications["V-05"] = v05
		f["v05"] = v05
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 3: V-03 — Cookieless t.co expansion (<= 3 hops) + 1 unauthenticated
	//         GraphQL control without Cookie (unauthControl <= 1).
	// -------------------------------------------------------------------------
	runner.run("v03_shortlink_and_unauth_control", func(f map[string]any) error {
		shortTarget := ""
		shortSource := ""
		if len(tg.ShortLinks) > 0 {
			shortTarget = strings.TrimSpace(tg.ShortLinks[0])
			shortSource = "targets"
		} else if extractedTcoURL != "" {
			shortTarget = extractedTcoURL
			shortSource = "post_entity"
		}

		unauthUserID := ""
		if unauthProbeStatusID == "" && len(parsedUsers) > 0 && parsedUsers[0].RestID != "" {
			unauthUserID = parsedUsers[0].RestID
		}

		if shortTarget == "" && unauthProbeStatusID == "" && unauthUserID == "" {
			rc.Verifications["V-03"] = map[string]any{
				"status":              "not-run",
				"shortLinkStatus":     "not-run",
				"unauthControlStatus": "not-run",
				"reason":              "no shortLinks, extracted t.co URL, or public post/user target available",
			}
			return errSkip
		}

		v03 := map[string]any{}

		// 3a. Cookieless t.co short-link expansion.
		if shortTarget != "" {
			ck.setPhase("shortlink")
			ck.guestControlActive.Store(true)
			finalURL, hops, slErr := xClient.ExpandShortLink(ctx, shortTarget)
			ck.guestControlActive.Store(false)
			ck.setPhase("")

			v03["shortLinkStatus"] = "verified"
			v03["shortLinkSource"] = shortSource
			v03["shortLinkHops"] = hops
			v03["shortLinkCookieless"] = !ck.cookieOnShortLinkHit
			if finalURL != "" {
				redactor.addURLs(finalURL)
				if u, uErr := url.Parse(finalURL); uErr == nil {
					v03["landedScheme"] = strings.ToLower(u.Scheme)
					v03["landedHostClass"] = hostClass(u.Hostname())
					segs := site.SplitCleanPath(u.Path)
					for _, seg := range segs {
						if reAct024Digits.MatchString(seg) {
							redactor.addIDs(seg)
						}
					}
					if len(segs) >= 1 && !strings.EqualFold(segs[0], "i") && !strings.EqualFold(segs[0], "intent") && reAct024ScreenName.MatchString(segs[0]) {
						redactor.addHandles(segs[0])
					}
					if h := strings.TrimSpace(u.Hostname()); h != "" && !act024AllowedMainHosts[strings.ToLower(h)] && !site.IsLoopbackHost(h) {
						redactor.addHandles(h)
					}
				}
				v03["shortLinkResolved"] = true
			}
			if slErr != nil {
				errKind := site.KindOf(slErr)
				v03["shortLinkResolved"] = false
				v03["shortLinkErrKind"] = errKind
				if ck.stopReason() != "" || errors.Is(slErr, sitecheck.ErrQuotaExceeded) ||
					errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited {
					v03["status"] = "failed"
					rc.Verifications["V-03"] = v03
					f["v03"] = v03
					return slErr
				}
				// External landing blocked by X host allowlist or login wall is valid V-03 evidence.
				v03["blockedExternal"] = errKind == site.ErrKindBlocked
				v03["landedOnLoginWall"] = errKind == site.ErrKindAuthRequired
			}
		} else {
			v03["shortLinkStatus"] = "not-run"
			v03["shortLinkReason"] = "no shortLinks provided and no t.co URL found in fetched posts"
		}

		// 3b. Unauthenticated GraphQL control without Cookie (unauthControl <= 1).
		meta := xClient.Discovery().Current()
		if (unauthProbeStatusID != "" || unauthUserID != "") && meta != nil && ck.stopReason() == "" {
			var opName string
			var vars map[string]any
			if unauthProbeStatusID != "" {
				opName = x.OpTweetDetail
				vars = map[string]any{
					"focalTweetId":           unauthProbeStatusID,
					"includePromotedContent": false,
					"withV2Timeline":         true,
				}
			} else {
				opName = x.OpUserByRestId
				vars = map[string]any{
					"userId":                   unauthUserID,
					"withSafetyModeUserFields": true,
				}
			}
			if spec, ok := meta.Operation(opName); ok && meta.TxID != nil {
				reqURL, uErr := buildAct024GraphQLURL(xClient.Endpoints().GraphQLBase, spec, vars)
				if uErr != nil {
					return uErr
				}
				uReq, rErr := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
				if rErr != nil {
					return rErr
				}
				txid, tErr := meta.TxID.Generate(uReq.Method, uReq.URL.Path)
				if tErr != nil {
					return tErr
				}
				uReq.Header.Set("User-Agent", xClient.UserAgent())
				uReq.Header.Set("Accept", "*/*")
				uReq.Header.Set("Referer", x.DefaultReferer)
				uReq.Header.Set("Origin", x.DefaultOrigin)
				uReq.Header.Set("x-twitter-active-user", "yes")
				uReq.Header.Set("x-twitter-client-language", x.DefaultClientLanguage)
				uReq.Header.Set("Authorization", meta.BearerHeader())
				uReq.Header.Set("x-client-transaction-id", txid)
				// Strictly NO Cookie, NO x-csrf-token, NO x-twitter-auth-type!
				uReq.Header.Del("Cookie")
				uReq.Header.Del("x-csrf-token")
				uReq.Header.Del("x-twitter-auth-type")

				ck.setPhase("unauthControl")
				ck.guestControlActive.Store(true)
				uResp, doErr := rawAPIHTTP.Do(uReq)
				ck.guestControlActive.Store(false)
				ck.setPhase("")

				var uBody []byte
				if uResp != nil {
					uBody, _ = io.ReadAll(io.LimitReader(uResp.Body, x.DefaultMaxBodyBytes))
					_ = uResp.Body.Close()
				}
				uTel := ck.snapshotLastTelemetry()
				if stopped := ck.stopReason(); stopped != "" {
					v03["status"] = "failed"
					rc.Verifications["V-03"] = v03
					f["v03"] = v03
					return fmt.Errorf("unauth GraphQL control stopped: %s", stopped)
				}
				if doErr != nil {
					v03["status"] = "failed"
					rc.Verifications["V-03"] = v03
					f["v03"] = v03
					return doErr
				}
				se := x.ClassifyHTTPAndEnvelopeError(x.EnvelopeErrorContext{
					Endpoint:   xClient.Endpoints().GraphQLBase + "/" + opName,
					Operation:  opName,
					StatusCode: uResp.StatusCode,
					Header:     uResp.Header,
					Body:       uBody,
					HasCookie:  false,
				})
				v03["unauthControlStatus"] = "verified"
				v03["unauthControlOp"] = opName
				v03["unauthHTTPStatus"] = uTel.HTTPStatus
				v03["unauthErrorCodes"] = uTel.ErrorCodes
				v03["unauthClassifiedKind"] = classifiedKindOf(se)
				v03["unauthRejected"] = se != nil || uResp.StatusCode != http.StatusOK
			} else {
				v03["unauthControlStatus"] = "not-run"
			}
		} else {
			v03["unauthControlStatus"] = "not-run"
		}

		if v03["shortLinkStatus"] == "verified" || v03["unauthControlStatus"] == "verified" {
			v03["status"] = "verified"
		} else {
			v03["status"] = "not-run"
		}
		rc.Verifications["V-03"] = v03
		f["v03"] = v03
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 4: V-06 — User profile & UserTweets pagination (userTimeline <= 4).
	// -------------------------------------------------------------------------
	runner.run("v06_user_profile_and_timeline", func(f map[string]any) error {
		var activeUser *parsedAct024User
		for i := range parsedUsers {
			pu := &parsedUsers[i]
			if activeUser == nil && (hasTag(pu.Item.Expect, "active") || hasTag(pu.Item.Expect, "pinned") || hasTag(pu.Item.Expect, "retweet") || hasTag(pu.Item.Expect, "images")) {
				activeUser = pu
			}
		}
		if activeUser == nil && len(parsedUsers) > 0 {
			activeUser = &parsedUsers[0]
		}
		if activeUser == nil {
			rc.Verifications["V-06"] = map[string]any{
				"status": "not-run",
				"reason": "no public user target provided",
			}
			return errSkip
		}

		ck.setPhase("userTimeline")
		defer ck.setPhase("")

		// Call 1: UserByRestId or UserByScreenName
		var (
			prof *x.UserProfile
			err  error
		)
		if activeUser.RestID != "" {
			prof, err = xClient.GetUserByRestId(ctx, activeUser.RestID)
		} else {
			prof, err = xClient.GetUserByScreenName(ctx, activeUser.ScreenName)
		}
		if err != nil {
			return err
		}
		redactor.addXUserProfile(prof)
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("user profile stopped: %s", stopped)
		}

		v06 := map[string]any{
			"status":               "verified",
			"profileHasRestID":     prof.RestID != "",
			"profileHasScreenName": prof.ScreenName != "",
			"profileHasName":       prof.Name != "",
			"profileHasAvatar":     prof.AvatarURL != "",
			"profileStatusesCount": prof.StatusesCount,
			"profileProtected":     prof.Protected,
			"profilePinnedCount":   len(prof.PinnedTweetIDs),
		}

		// Call 2: UserTweets page 1
		seen := make(map[string]bool)
		p1, err := xClient.GetUserTweets(ctx, x.UserTweetsRequest{
			RestID:       prof.RestID,
			KnownProfile: prof,
			SeenIDs:      seen,
		})
		if err != nil {
			return err
		}
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("user timeline page 1 stopped: %s", stopped)
		}

		p1IDs := make(map[string]bool, len(p1.Posts))
		pinnedCount := 0
		pinnedLeading := true
		sawNonPinned := false
		retweetCount := 0
		quoteCount := 0
		photoPostCount := 0
		videoPostCount := 0
		gifPostCount := 0
		longTextCount := 0
		hlsOnlyCount := 0
		monotonicNonPinned := true
		var prevNonPinnedID string

		for i := range p1.Posts {
			p := &p1.Posts[i]
			redactor.addXPost(p)
			p1IDs[p.ID] = true

			if p.Pinned {
				pinnedCount++
				if sawNonPinned {
					pinnedLeading = false
				}
			} else {
				sawNonPinned = true
				if prevNonPinnedID != "" {
					if cmp, cErr := x.CompareUint64ID(prevNonPinnedID, p.ID); cErr == nil && cmp < 0 {
						monotonicNonPinned = false
					}
				}
				prevNonPinnedID = p.ID
			}
			if p.IsRetweet {
				retweetCount++
			}
			if p.IsQuote {
				quoteCount++
			}
			if p.IsLongText {
				longTextCount++
			}
			if p.HasHLSOnlyVideo {
				hlsOnlyCount++
			}
			hasPhoto, hasVideo, hasGIF := false, false, false
			for _, m := range p.Media {
				switch {
				case m.Kind == x.MediaKindPhoto:
					hasPhoto = true
					if samplePhotoURL == "" {
						if m.ImageURL != "" {
							samplePhotoURL = m.ImageURL
						} else if m.PublicPreviewURL != "" {
							samplePhotoURL = m.PublicPreviewURL
						}
					}
				case m.GIF || m.Kind == x.MediaKindGIF:
					hasGIF = true
				case m.Kind == x.MediaKindVideo:
					hasVideo = true
				}
			}
			if hasPhoto {
				photoPostCount++
			}
			if hasVideo {
				videoPostCount++
			}
			if hasGIF {
				gifPostCount++
			}
		}

		v06["page1Items"] = len(p1.Posts)
		v06["page1HasMore"] = p1.HasMore
		v06["page1HasNextCursor"] = p1.NextCursor != ""
		v06["page1UncertainEmpty"] = p1.UncertainEmpty
		v06["page1PinnedCount"] = pinnedCount
		v06["page1PinnedLeading"] = pinnedLeading
		v06["page1RetweetCount"] = retweetCount
		v06["page1QuoteCount"] = quoteCount
		v06["page1PhotoPostCount"] = photoPostCount
		v06["page1VideoPostCount"] = videoPostCount
		v06["page1GIFPostCount"] = gifPostCount
		v06["page1LongTextCount"] = longTextCount
		v06["page1HLSOnlyCount"] = hlsOnlyCount
		v06["page1OuterIDsMonotonicNonPinned"] = monotonicNonPinned

		// Call 3: UserTweets page 2
		if p1.HasMore && p1.NextCursor != "" && ck.stopReason() == "" {
			p2, p2Err := xClient.GetUserTweets(ctx, x.UserTweetsRequest{
				RestID:       prof.RestID,
				Cursor:       p1.NextCursor,
				KnownProfile: prof,
				SeenIDs:      seen,
				TotalFetched: len(p1.Posts),
			})
			if p2Err != nil {
				return p2Err
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("user timeline page 2 stopped: %s", stopped)
			}
			dupes := 0
			for i := range p2.Posts {
				p := &p2.Posts[i]
				redactor.addXPost(p)
				if p1IDs[p.ID] {
					dupes++
				}
			}
			v06["page2Fetched"] = true
			v06["page2Items"] = len(p2.Posts)
			v06["page2HasMore"] = p2.HasMore
			v06["page2CursorAdvanced"] = p2.NextCursor != p1.NextCursor
			v06["page2CursorStagnated"] = p2.CursorStagnated
			v06["duplicateAcrossPages"] = dupes > 0
			v06["reachedVisibleLimit"] = p2.ReachedVisibleLimit
		} else {
			v06["page2Fetched"] = false
		}

		rc.Verifications["V-06"] = v06
		f["v06"] = v06
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 5: V-09 — Deterministic error code probes (errorProbe <= 2).
	// Absent targets (protectedUsers empty, missing nil) cleanly report not-run with 0 calls.
	// -------------------------------------------------------------------------
	runner.run("v09_error_probes", func(f map[string]any) error {
		var protectedTarget *parsedAct024User
		if len(parsedProtectedUsers) > 0 {
			protectedTarget = &parsedProtectedUsers[0]
		}
		var missingUserID, missingStatusID string
		if tg.Missing != nil {
			missingUserID = strings.TrimSpace(tg.Missing.UserID)
			missingStatusID = strings.TrimSpace(tg.Missing.StatusID)
		}

		if protectedTarget == nil && missingUserID == "" && missingStatusID == "" {
			rc.Verifications["V-09"] = map[string]any{
				"status":              "not-run",
				"protectedUserStatus": "not-run",
				"missingUserStatus":   "not-run",
				"missingPostStatus":   "not-run",
				"reason":              "no protectedUsers or missing targets provided",
			}
			return errSkip
		}

		ck.setPhase("errorProbe")
		defer ck.setPhase("")

		remainingErr := func() int {
			return spec.apiLimits["errorProbe"] - gate.Usage().Categories["errorProbe"]
		}

		v09 := map[string]any{"status": "verified"}

		// Probe 1: missing.userId (if provided)
		if missingUserID != "" && remainingErr() > 0 && ck.stopReason() == "" {
			ck.expectedBusinessErrorActive.Store(true)
			prof, uErr := xClient.GetUserByRestId(ctx, missingUserID)
			ck.expectedBusinessErrorActive.Store(false)
			tel := ck.snapshotLastTelemetry()
			if prof != nil {
				redactor.addXUserProfile(prof)
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("missing user probe stopped: %s", stopped)
			}
			if errors.Is(uErr, sitecheck.ErrQuotaExceeded) {
				return uErr
			}
			v09["missingUserStatus"] = "verified"
			v09["missingUser"] = map[string]any{
				"httpStatus":     tel.HTTPStatus,
				"errorCodes":     tel.ErrorCodes,
				"classifiedKind": site.KindOf(uErr),
				"returnedNil":    prof == nil,
			}
		} else {
			v09["missingUserStatus"] = "not-run"
		}

		// Probe 2: protected user (if provided; takes priority over 2nd missing probe so both kinds are covered)
		if protectedTarget != nil && remainingErr() > 0 && ck.stopReason() == "" {
			ck.expectedBusinessErrorActive.Store(true)
			var (
				prof *x.UserProfile
				pErr error
			)
			if protectedTarget.RestID != "" {
				prof, pErr = xClient.GetUserByRestId(ctx, protectedTarget.RestID)
			} else {
				prof, pErr = xClient.GetUserByScreenName(ctx, protectedTarget.ScreenName)
			}
			ck.expectedBusinessErrorActive.Store(false)
			tel := ck.snapshotLastTelemetry()
			if prof != nil {
				redactor.addXUserProfile(prof)
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("protected user probe stopped: %s", stopped)
			}
			if errors.Is(pErr, sitecheck.ErrQuotaExceeded) {
				return pErr
			}
			pInfo := map[string]any{
				"httpStatus":     tel.HTTPStatus,
				"errorCodes":     tel.ErrorCodes,
				"classifiedKind": site.KindOf(pErr),
			}
			if prof != nil {
				pInfo["profileProtectedFlag"] = prof.Protected
			}
			v09["protectedUserStatus"] = "verified"
			v09["protectedUser"] = pInfo
		} else {
			v09["protectedUserStatus"] = "not-run"
		}

		// Probe 3: missing.statusId (if provided and errorProbe budget remains)
		if missingStatusID != "" && remainingErr() > 0 && ck.stopReason() == "" {
			ck.expectedBusinessErrorActive.Store(true)
			post, sErr := xClient.GetPost(ctx, missingStatusID)
			ck.expectedBusinessErrorActive.Store(false)
			tel := ck.snapshotLastTelemetry()
			if post != nil {
				redactor.addXPost(post)
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("missing post probe stopped: %s", stopped)
			}
			if errors.Is(sErr, sitecheck.ErrQuotaExceeded) {
				return sErr
			}
			v09["missingPostStatus"] = "verified"
			v09["missingPost"] = map[string]any{
				"httpStatus":     tel.HTTPStatus,
				"errorCodes":     tel.ErrorCodes,
				"classifiedKind": site.KindOf(sErr),
				"returnedNil":    post == nil,
			}
		} else {
			v09["missingPostStatus"] = "not-run"
		}

		rc.Verifications["V-09"] = v09
		f["v09"] = v09
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 6: V-08 — Mine › X read-only lists (mine <= 5):
	//         Following (<= 2 pages), Bookmarks (1), Likes (1), optional BookmarkFoldersSlice (1).
	// -------------------------------------------------------------------------
	runner.run("v08_mine_lists", func(f map[string]any) error {
		if selfRestID == "" {
			rc.Verifications["V-08"] = map[string]any{
				"status": "not-run",
				"reason": "self rest_id unavailable",
			}
			return errSkip
		}

		ck.setPhase("mine")
		defer ck.setPhase("")
		defer func() {
			curMeta := xClient.Discovery().Current()
			if curMeta == nil {
				return
			}
			redactor.addFrontendMetadata(curMeta)
			if v04, ok := rc.Verifications["V-04"].(map[string]any); ok {
				opSummary, coreOpsCount := summarizeAct024Operations(curMeta)
				fTel := ck.snapshotFrontendTelemetry()
				v04["coreOpsCount"] = coreOpsCount
				v04["requiredOpsCount"] = len(opSummary)
				v04["operations"] = opSummary
				v04["lazyChunksFetched"] = fTel.LazyChunkCount
				v04["lazyChunkJSBytes"] = fTel.LazyChunkBytes
				v04["bookmarkFoldersOpInBundle"] = ck.getBookmarkFoldersSpec() != nil
			}
		}()

		remainingMine := func() int {
			return spec.apiLimits["mine"] - gate.Usage().Categories["mine"]
		}

		v08 := map[string]any{"status": "verified"}

		// 6a. Following page 1 (+ page 2 when cursor exists and >= 3 calls remain so Bookmarks + Likes stay guaranteed).
		seenFollow := make(map[string]bool)
		fp1, err := xClient.ListFollowing(ctx, selfRestID, "", seenFollow)
		if err != nil {
			return err
		}
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("mine following page 1 stopped: %s", stopped)
		}
		f1IDs := make(map[string]bool, len(fp1.Users))
		for _, u := range fp1.Users {
			redactor.addXFollowingUser(u)
			f1IDs[u.RestID] = true
		}
		v08["followingPage1Items"] = len(fp1.Users)
		v08["followingPage1HasMore"] = fp1.HasMore
		v08["followingPage1HasNextCursor"] = fp1.NextCursor != ""
		if len(fp1.Users) > 0 {
			v08["followingHasRestID"] = fp1.Users[0].RestID != ""
			v08["followingHasScreenName"] = fp1.Users[0].ScreenName != ""
		}

		if fp1.HasMore && fp1.NextCursor != "" && remainingMine() >= 3 && ck.stopReason() == "" {
			fp2, err2 := xClient.ListFollowing(ctx, selfRestID, fp1.NextCursor, seenFollow)
			if err2 != nil {
				return err2
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("mine following page 2 stopped: %s", stopped)
			}
			dupes := 0
			for _, u := range fp2.Users {
				redactor.addXFollowingUser(u)
				if f1IDs[u.RestID] {
					dupes++
				}
			}
			v08["followingPage2Fetched"] = true
			v08["followingPage2Items"] = len(fp2.Users)
			v08["followingPage2HasMore"] = fp2.HasMore
			v08["followingDuplicateAcrossPages"] = dupes > 0
		} else {
			v08["followingPage2Fetched"] = false
		}

		// 6b. Bookmarks page 1.
		seenBM := make(map[string]bool)
		bm, err := xClient.ListBookmarks(ctx, "", seenBM)
		if err != nil {
			return err
		}
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("mine bookmarks stopped: %s", stopped)
		}
		for i := range bm.Posts {
			redactor.addXPost(&bm.Posts[i])
		}
		v08["bookmarksItems"] = len(bm.Posts)
		v08["bookmarksHasMore"] = bm.HasMore

		// 6c. Likes page 1 (self only).
		seenLikes := make(map[string]bool)
		likes, err := xClient.ListLikes(ctx, selfRestID, "", seenLikes)
		if err != nil {
			return err
		}
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("mine likes stopped: %s", stopped)
		}
		for i := range likes.Posts {
			redactor.addXPost(&likes.Posts[i])
		}
		v08["likesItems"] = len(likes.Posts)
		v08["likesHasMore"] = likes.HasMore

		// 6d. Optional BookmarkFoldersSlice single probe (non-Premium degradation check; runs last).
		bfSpec := ck.getBookmarkFoldersSpec()
		meta := xClient.Discovery().Current()
		if bfSpec != nil && meta != nil && meta.TxID != nil && remainingMine() > 0 && ck.stopReason() == "" {
			reqURL, uErr := buildAct024GraphQLURL(xClient.Endpoints().GraphQLBase, *bfSpec, map[string]any{})
			if uErr == nil {
				bfReq, rErr := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
				if rErr == nil {
					if txid, tErr := meta.TxID.Generate(bfReq.Method, bfReq.URL.Path); tErr == nil {
						reqCookies := make(map[string]string, len(cleanedInitial))
						for k, v := range cleanedInitial {
							reqCookies[k] = v
						}
						if curCT0 := ck.getLatestCT0(); curCT0 != "" {
							reqCookies["ct0"] = curCT0
						}
						bfReq.Header.Set("User-Agent", xClient.UserAgent())
						bfReq.Header.Set("Accept", "*/*")
						bfReq.Header.Set("Referer", "https://x.com/i/bookmarks")
						bfReq.Header.Set("Origin", x.DefaultOrigin)
						bfReq.Header.Set("x-twitter-active-user", "yes")
						bfReq.Header.Set("x-twitter-client-language", x.DefaultClientLanguage)
						bfReq.Header.Set("x-twitter-auth-type", "OAuth2Session")
						bfReq.Header.Set("Authorization", meta.BearerHeader())
						bfReq.Header.Set("Cookie", formatCleanedXCookieHeader(reqCookies))
						bfReq.Header.Set("x-csrf-token", reqCookies["ct0"])
						bfReq.Header.Set("x-client-transaction-id", txid)

						ck.optionalFolderProbeActive.Store(true)
						ck.expectedBusinessErrorActive.Store(true)
						bfResp, bfErr := rawAPIHTTP.Do(bfReq)
						ck.expectedBusinessErrorActive.Store(false)
						ck.optionalFolderProbeActive.Store(false)

						var bfBody []byte
						if bfResp != nil {
							bfBody, _ = io.ReadAll(io.LimitReader(bfResp.Body, x.DefaultMaxBodyBytes))
							_ = bfResp.Body.Close()
						}
						bfTel := ck.snapshotLastTelemetry()
						if stopped := ck.stopReason(); stopped != "" {
							rc.Verifications["V-08"] = v08
							return fmt.Errorf("mine bookmark folders probe stopped: %s", stopped)
						}
						if bfErr != nil {
							if errors.Is(bfErr, sitecheck.ErrQuotaExceeded) {
								return bfErr
							}
							v08["bookmarkFoldersStatus"] = "unavailable"
							v08["bookmarkFoldersAvailable"] = false
						} else {
							bfCount, bfAvailable := inspectAct024BookmarkFoldersBody(bfResp.StatusCode, bfBody)
							v08["bookmarkFoldersStatus"] = "verified"
							v08["bookmarkFoldersHTTPStatus"] = bfTel.HTTPStatus
							v08["bookmarkFoldersErrorCodes"] = bfTel.ErrorCodes
							v08["bookmarkFoldersAvailable"] = bfAvailable
							v08["bookmarkFoldersCount"] = bfCount
						}
					}
				}
			}
		} else {
			v08["bookmarkFoldersStatus"] = "not-run"
		}

		rc.Verifications["V-08"] = v08
		f["v08"] = v08
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 7: V-07 — Media CDN & formal TRACE CacheXPost pipeline in temp dir.
	// Budget: media <= 5 requests & <= 25 MiB (strictly cookieless, no Referer).
	// Reuses Step 2's warm b.xPostCache entry so 0 extra TweetDetail calls are sent.
	// -------------------------------------------------------------------------
	runner.run("v07_media_and_formal_cache", func(f map[string]any) error {
		cacheTargetID := videoCacheCandidateID
		if cacheTargetID == "" {
			cacheTargetID = fallbackMediaCacheCandidateID
		}
		if cacheTargetID == "" && samplePhotoURL == "" {
			rc.Verifications["V-07"] = map[string]any{
				"status": "not-run",
				"reason": "no video or image media target collected",
			}
			return errSkip
		}

		ck.setPhase("media")
		defer ck.setPhase("")

		v07 := map[string]any{}

		if cacheTargetID != "" {
			if err := backend.DownloadManager().Start(ctx); err != nil {
				return err
			}
			task, tErr := backend.CacheXPost(ctx, cacheTargetID, nil)
			if tErr != nil {
				return tErr
			}

			doneTask, wErr := waitAct024DownloadTask(ctx, backend, ck, task.TaskID)
			if wErr != nil {
				return wErr
			}

			cRec := map[string]any{
				"taskStatus": doneTask.Status,
				"errorKind":  doneTask.ErrorKind,
			}
			hlsOnlyConfirmed := false
			mp4ffVerified := false

			if item, ok, gErr := backend.GetLibraryItem(doneTask.ItemKey); gErr == nil && ok {
				cRec["indexed"] = true
				cRec["itemStatus"] = item.Status
				cRec["kind"] = item.Kind
				cRec["dynamicType"] = item.DynamicType
				cRec["mediaCount"] = len(item.Media)
				cRec["missingCount"] = len(item.Missing)

				roles := map[string]int{}
				for _, m := range item.Media {
					roles[m.Role]++
					if m.Role == "video" {
						if p, pErr := backend.MediaPath(doneTask.ItemKey, m.Path); pErr == nil {
							if sum, iErr := download.InspectMP4FileContext(ctx, p); iErr == nil {
								mp4ffVerified = true
								cRec["mp4ffValid"] = true
								cRec["mp4Bytes"] = sum.FileBytes
								cRec["codecs"] = sum.Codecs
								cRec["width"] = sum.Width
								cRec["height"] = sum.Height
								cRec["moovBeforeMdat"] = sum.MoovBeforeMdat
								cRec["hasElst"] = sum.HasElst
								cRec["durationSec"] = float64(int(sum.DurationSec*1000+0.5)) / 1000
							} else {
								cRec["mp4ffValid"] = false
							}
						}
					}
				}
				cRec["mediaRoles"] = roles
				for _, miss := range item.Missing {
					if strings.Contains(miss.Reason, x.HLSOnlyNoticeText) {
						hlsOnlyConfirmed = true
					}
				}
			}

			cRec["hlsOnlyConfirmed"] = hlsOnlyConfirmed
			v07["formalCache"] = cRec
			v07["mp4ffValid"] = mp4ffVerified
			v07["hasHLSOnly"] = hlsOnlyConfirmed
			v07["range206"] = ck.mediaHTTP206Count.Load() > 0
			v07["cdnCookieless"] = !ck.cookieOnCDNHit
			v07["cdnNoReferer"] = !ck.refererOnCDNHit
			if hosts := ck.cdnHostClassCounts(); len(hosts) > 0 {
				v07["cdnHostClasses"] = hosts
			}

			// Verify zero leaked cookies, Bearer, CSRF, or signed URLs in temp cache manifests/DB/logs.
			tempSecretClean := scanAct024TempWorkDirClean(workDir, redactor)
			v07["tempCacheSecretScanClean"] = tempSecretClean
			if !tempSecretClean {
				v07["status"] = "failed"
				rc.Verifications["V-07"] = v07
				f["v07"] = v07
				return errors.New("secret or signed URL detected in temporary X cache metadata or logs")
			}

			if doneTask.Status != "completed" && !(doneTask.Status == "partial" && hlsOnlyConfirmed) {
				v07["status"] = "failed"
				rc.Verifications["V-07"] = v07
				f["v07"] = v07
				return fmt.Errorf("formal X cache ended as %s (%s)", doneTask.Status, doneTask.ErrorKind)
			}
			v07["status"] = "verified"
		} else {
			v07["formalCacheStatus"] = "not-run"
		}

		// Optional cookieless pbs.twimg.com original image probe (?format=<ext>&name=orig) if budget allows.
		if samplePhotoURL != "" && ck.stopReason() == "" &&
			ck.canAffordMedia(4<<20, 1) && gate.Usage().APICalls < spec.maxTotalRequests {
			imgFact, imgErr := probeXImageAsset(ctx, probeMediaHTTP, defaultMediaSpec, samplePhotoURL, 8<<20)
			if imgErr != nil {
				if ck.stopReason() != "" || errors.Is(imgErr, sitecheck.ErrQuotaExceeded) {
					v07["status"] = "failed"
					rc.Verifications["V-07"] = v07
					f["v07"] = v07
					return imgErr
				}
				v07["imageProbeErrKind"] = site.KindOf(imgErr)
			} else {
				v07["imageProbe"] = imgFact
				if v07["status"] == nil {
					v07["status"] = "verified"
					v07["cdnCookieless"] = !ck.cookieOnCDNHit
					v07["cdnNoReferer"] = !ck.refererOnCDNHit
				}
			}
		}

		if v07["status"] == nil {
			v07["status"] = "not-run"
		}
		rc.Verifications["V-07"] = v07
		f["v07"] = v07
		return nil
	})

	if cfg.StepHook != nil {
		cfg.StepHook(runner)
	}

	if curMeta := xClient.Discovery().Current(); curMeta != nil {
		redactor.addFrontendMetadata(curMeta)
	}
	closeBackend()
	rc.TempDataDeleted = cleanupWorkDir()
	rc.APIUsage = gate.Usage()
	rc.OperationCounts, rc.StatusCounts = ck.snapshotCounts()
	rc.ShortLinkHops = mediaUsage{int(ck.shortLink.requests.Load()), ck.shortLink.bytes.Load()}
	rc.MediaUsage = mediaUsage{int(ck.media.requests.Load()), ck.media.bytes.Load()}
	rc.FrontendBytes = ck.frontendBytes.Load()
	rc.RateLimits = ck.rateLimitSnapshots()
	rc.StoppedBy = ck.stopReason()
	rc.Redirects = ck.redirectShapes()
	rc.VerdictHints = buildAct024VerdictHints(rc.Verifications, rc.StoppedBy)

	rc.Passed = rc.StoppedBy == "" && rc.TempDataDeleted &&
		!ck.cookieOnCDNHit && !ck.refererOnCDNHit &&
		!ck.cookieOnStaticAssetHit && !ck.cookieOnShortLinkHit
	for _, s := range rc.Steps {
		if !s.OK {
			rc.Passed = false
		}
	}
	rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)

	rc.SecretScanClean = true
	if err := writeCleanReceipt(outPath, rc, func(rawJSON string) error {
		return redactor.verifyJSONClean(rawJSON)
	}); err != nil {
		rc.SecretScanClean = false
		rc.Passed = false
		return rc, err
	}
	if !rc.Passed {
		if rc.StoppedBy != "" {
			return rc, fmt.Errorf("%s stopped by %s", spec.id, rc.StoppedBy)
		}
		return rc, fmt.Errorf("%s completed with step failures", spec.id)
	}
	return rc, nil
}

func summarizeAct024Operations(meta *x.FrontendMetadata) (map[string]any, int) {
	opSummary := make(map[string]any)
	coreCount := 0
	if meta == nil {
		return opSummary, 0
	}
	coreSet := make(map[string]bool, len(x.CoreOperations()))
	for _, opName := range x.CoreOperations() {
		coreSet[opName] = true
	}
	for _, opName := range x.RequiredOperations() {
		if opSpec, ok := meta.Operation(opName); ok {
			if coreSet[opName] && opSpec.QueryID != "" {
				coreCount++
			}
			opSummary[opName] = map[string]any{
				"hasQueryId":       opSpec.QueryID != "",
				"featureCount":     len(opSpec.Features),
				"fieldToggleCount": len(opSpec.FieldToggles),
			}
		}
	}
	return opSummary, coreCount
}

func waitAct024DownloadTask(
	ctx context.Context,
	backend *service.Backend,
	ck *act024Checker,
	taskID string,
) (download.Task, error) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if stopped := ck.stopReason(); stopped != "" {
			return download.Task{}, fmt.Errorf("stopped during formal cache: %s", stopped)
		}
		list, _ := backend.ListDownloadTasks()
		for _, t := range list {
			if t.TaskID != taskID {
				continue
			}
			if t.Status == "completed" || t.Status == "partial" || t.Status == "failed" || strings.HasPrefix(t.Status, "paused") {
				return t, nil
			}
		}
		select {
		case <-ctx.Done():
			return download.Task{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return download.Task{}, errors.New("formal X cache task timed out after 3m")
}

func scanAct024TempWorkDirClean(workDir string, redactor *act024Redactor) bool {
	clean := true
	_ = filepath.WalkDir(workDir, func(p string, d os.DirEntry, wErr error) error {
		if wErr != nil || d.IsDir() {
			return nil
		}
		lowerName := strings.ToLower(d.Name())
		isScanTarget := lowerName == "item.json" ||
			strings.HasSuffix(lowerName, ".log") ||
			strings.HasSuffix(lowerName, ".json") ||
			strings.HasSuffix(lowerName, ".db") ||
			strings.HasSuffix(lowerName, ".db-wal") ||
			strings.HasSuffix(lowerName, ".db-shm")
		if !isScanTarget {
			return nil
		}
		if raw, rErr := os.ReadFile(p); rErr == nil {
			if len(sitecheck.ScanTextForSecrets(string(raw))) > 0 || redactor.containsSecretBytes(raw) {
				clean = false
			}
		}
		return nil
	})
	return clean
}

func addAct024PostDTOToRedactor(r *act024Redactor, dto *service.XPostDTO) {
	if r == nil || dto == nil {
		return
	}
	r.addIDs(dto.ID, dto.ConversationID, dto.InReplyToStatusID, dto.InReplyToUserID, dto.Author.RestID)
	r.addHandles(dto.Author.ScreenName, dto.Author.Name, dto.InReplyToScreenName)
	r.addURLs(dto.CanonicalURL, dto.Author.AvatarURL)
	r.addTexts(dto.Text)
	for _, ue := range dto.URLs {
		r.addURLs(ue.ShortURL, ue.ExpandedURL)
	}
	for _, m := range dto.Media {
		r.addURLs(m.ImageURL, m.PublicPreviewURL, m.VideoURL)
	}
	if dto.Card != nil {
		r.addURLs(dto.Card.URL, dto.Card.CoverURL)
		r.addTexts(dto.Card.Title, dto.Card.Desc)
	}
	if dto.Retweeted != nil {
		addAct024PostDTOToRedactor(r, dto.Retweeted)
	}
	if dto.Quoted != nil {
		addAct024PostDTOToRedactor(r, dto.Quoted)
	}
	for i := range dto.ThreadAncestors {
		addAct024PostDTOToRedactor(r, &dto.ThreadAncestors[i])
	}
	for i := range dto.Thread {
		addAct024PostDTOToRedactor(r, &dto.Thread[i])
	}
}

func extractAct024TcoFromPostDTO(dto *service.XPostDTO, allowLocalhost bool) string {
	if dto == nil {
		return ""
	}
	checkURLs := func(list []service.XURLEntityDTO) string {
		for _, ue := range list {
			cand := strings.TrimSpace(ue.ShortURL)
			if cand != "" && validateAct024ShortLinkURL(cand, allowLocalhost) == nil {
				return cand
			}
		}
		return ""
	}
	if u := checkURLs(dto.URLs); u != "" {
		return u
	}
	if dto.Quoted != nil {
		if u := checkURLs(dto.Quoted.URLs); u != "" {
			return u
		}
	}
	if dto.Retweeted != nil {
		if u := checkURLs(dto.Retweeted.URLs); u != "" {
			return u
		}
	}
	if m := reAct024TcoURLInText.FindString(dto.Text); m != "" && validateAct024ShortLinkURL(m, allowLocalhost) == nil {
		return m
	}
	return ""
}

func buildAct024GraphQLURL(base string, spec x.GraphQLOperationSpec, vars map[string]any) (string, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	varsBytes, err := json.Marshal(vars)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("variables", string(varsBytes))
	feats := spec.Features
	if feats == nil {
		feats = map[string]bool{}
	}
	featBytes, err := json.Marshal(feats)
	if err != nil {
		return "", err
	}
	q.Set("features", string(featBytes))
	if len(spec.FieldToggles) > 0 {
		toggBytes, err := json.Marshal(spec.FieldToggles)
		if err != nil {
			return "", err
		}
		q.Set("fieldToggles", string(toggBytes))
	}
	trimmedBase := strings.TrimRight(strings.TrimSpace(base), "/")
	return trimmedBase + "/" + spec.QueryID + "/" + spec.OperationName + "?" + q.Encode(), nil
}

func inspectAct024BookmarkFoldersBody(statusCode int, body []byte) (int, bool) {
	if statusCode != http.StatusOK {
		return 0, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 0, false
	}
	if len(x.ExtractXErrors(trimmed)) > 0 {
		return 0, false
	}
	var env struct {
		Data struct {
			Viewer struct {
				UserResults struct {
					Result struct {
						BookmarkCollectionsSlice *struct {
							Items []json.RawMessage `json:"items"`
						} `json:"bookmark_collections_slice"`
					} `json:"result"`
				} `json:"user_results"`
			} `json:"viewer"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if dec.Decode(&env) != nil {
		return 0, false
	}
	if env.Data.Viewer.UserResults.Result.BookmarkCollectionsSlice == nil {
		return 0, false
	}
	return len(env.Data.Viewer.UserResults.Result.BookmarkCollectionsSlice.Items), true
}

func missingPostExpectTags(posts []parsedAct024Post) []string {
	want := []string{"longText", "thread", "mixed", "gif", "video", "poll", "card", "deletedOrigin", "tcoLink"}
	have := make(map[string]bool)
	for _, pp := range posts {
		for _, tag := range pp.Item.Expect {
			have[tag] = true
		}
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

func buildAct024VerdictHints(verifications map[string]any, stoppedBy string) map[string]string {
	hints := make(map[string]string, 9)
	for _, key := range []string{"V-01", "V-02", "V-03", "V-04", "V-05", "V-06", "V-07", "V-08", "V-09"} {
		entry, ok := verifications[key].(map[string]any)
		if !ok || len(entry) == 0 {
			if stoppedBy != "" {
				hints[key] = "stopped"
			} else {
				hints[key] = "not-run"
			}
			continue
		}
		status, _ := entry["status"].(string)
		switch status {
		case "verified":
			hints[key] = "pass"
		case "not-run":
			hints[key] = "not-run"
		case "failed":
			if stoppedBy != "" {
				hints[key] = "stopped"
			} else {
				hints[key] = "fail"
			}
		default:
			if stoppedBy != "" {
				hints[key] = "stopped"
			} else {
				hints[key] = "partial"
			}
		}
	}
	return hints
}
