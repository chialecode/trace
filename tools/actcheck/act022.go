package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/sitecheck"
)

type act022Receipt struct {
	Act               string           `json:"act"`
	Commit            string           `json:"commit,omitempty"`
	StartedAt         string           `json:"startedAt"`
	FinishedAt        string           `json:"finishedAt"`
	Passed            bool             `json:"passed"`
	StoppedBy         string           `json:"stoppedBy,omitempty"`
	TempDataDeleted   bool             `json:"tempDataDeleted"`
	SecretScanClean   bool             `json:"secretScanClean"`
	PreUsedLoginCalls int              `json:"preUsedLoginCalls,omitempty"`
	Steps             []step           `json:"steps"`
	Verifications     map[string]any   `json:"verifications"`
	APIUsage          sitecheck.Usage  `json:"apiUsage"`
	PathCounts        map[string]int   `json:"apiPathCounts"`
	StatusCounts      map[string]int   `json:"httpStatusCounts"`
	ShortLinkHops     mediaUsage       `json:"shortLinkHops"`
	ProbeCDN          mediaUsage       `json:"probeCdnMedia"`
	CacheCDN          mediaUsage       `json:"cacheCdnMedia"`
	Redirects         []map[string]any `json:"redirects,omitempty"`
	Limits            map[string]any   `json:"limits"`
	Notes             []string         `json:"notes,omitempty"`
}

func runAct022Main(root, out string) {
	rc, err := runAct022(context.Background(), act022Config{
		RootDir: root,
		OutPath: out,
	})
	if err != nil && rc.StartedAt == "" {
		fatal(err.Error())
	}
	outDisp := out
	if outDisp == "" {
		outDisp = filepath.Join(root, "artifacts", "site-check", act022ID+".json")
	}
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d/%d shortHops=%d probeCdn=%d/%dB cacheCdn=%d/%dB receipt=%s\n",
		act022Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, act022MaxAPICalls,
		rc.ShortLinkHops.Requests, rc.ProbeCDN.Requests, rc.ProbeCDN.Bytes,
		rc.CacheCDN.Requests, rc.CacheCDN.Bytes, outDisp)
	for _, s := range rc.Steps {
		fmt.Printf("  %-30s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", act022Label, err)
		os.Exit(1)
	}
	if !rc.Passed {
		os.Exit(1)
	}
}

func runAct022(parentCtx context.Context, cfg act022Config) (act022Receipt, error) {
	var rc act022Receipt
	root := cfg.RootDir
	if root == "" {
		root = "."
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", act022ID+".json")
	}
	spacing := cfg.MinSpacing
	if spacing == 0 {
		spacing = act022DefaultSpacing
	} else if spacing < 0 {
		spacing = 0
	}

	tg, parsedUsers, parsedPosts, err := loadAndValidateAct022Targets(root, cfg.AllowLocalhost)
	if err != nil {
		return rc, err
	}
	rawCookie, cleanedInitial, err := readAndValidateWeiboCookie(root)
	if err != nil {
		return rc, err
	}

	redactor := newAct022Redactor()
	redactor.addCookieHeader(rawCookie)
	redactor.addCookieMap(cleanedInitial)
	redactor.addTargets(tg, parsedUsers, parsedPosts)

	ledgerDir := filepath.Join(root, ".local", "site-check")
	preUsedLogin, err := migrateAct022PreUsedLedger(ledgerDir)
	if err != nil {
		return rc, err
	}

	gate, err := sitecheck.OpenGate(ledgerDir, sitecheck.Limits{
		ActID:         act022ID,
		MaxRuns:       act022MaxRuns,
		MaxAPICalls:   act022MaxAPICalls,
		MaxByCategory: act022APILimits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act022Checker{
		gate:           gate,
		apiLimits:      act022APILimits,
		shortLink:      &mediaBudget{name: "short_link", maxRequests: act022MaxShortLinkHops, maxBytes: act022MaxShortLinkBytes},
		probeCDN:       &mediaBudget{name: "probe_cdn", maxRequests: act022MaxProbeCDNReqs, maxBytes: act022MaxProbeCDNBytes},
		cacheCDN:       &mediaBudget{name: "cache_cdn", maxRequests: act022MaxCacheCDNReqs, maxBytes: act022MaxCacheCDNBytes},
		minSpacing:     spacing,
		allowLocalhost: cfg.AllowLocalhost,
		statuses:       make(map[string]int),
		pathCounts:     make(map[string]int),
		apiEnvelopes:   make(map[string][]act022APITelemetry),
	}

	workDir, err := os.MkdirTemp("", "trace-"+act022ID+"-")
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

	backend, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		WeiboOpts: weibo.ClientOptions{
			Endpoints:      cfg.Endpoints,
			Transport:      cfg.Transport,
			WrapTransport:  ck.wrap,
			MinInterval:    -1, // Pacing is enforced uniformly by act022Checker (>= 1.5s).
			JitterMax:      -1,
			AllowLocalhost: cfg.AllowLocalhost,
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

	// Wrap DownloadManager media clients so Step 9 formal cache CDN traffic is quota-gated & cookieless.
	backend.DownloadManager().SetMediaTransportWrap(ck.wrap)

	wbClient := backend.WeiboClient()
	defaultMediaSpec := wbClient.MediaTransport()
	probeMediaHTTP := &http.Client{
		Transport: ck.wrap(cfg.Transport),
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("weibo media redirect limit exceeded")
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

	rc = act022Receipt{
		Act:               act022Label,
		Commit:            resolveReceiptCommit(root),
		StartedAt:         time.Now().UTC().Format(time.RFC3339),
		PreUsedLoginCalls: preUsedLogin,
		Verifications:     make(map[string]any),
		PathCounts:        ck.pathCounts,
		StatusCounts:      ck.statuses,
		Limits: map[string]any{
			"api":              act022APILimits,
			"apiTotal":         act022MaxAPICalls,
			"shortLinkHops":    act022MaxShortLinkHops,
			"probeCdnRequests": act022MaxProbeCDNReqs,
			"probeCdnBytes":    act022MaxProbeCDNBytes,
			"cacheCdnRequests": act022MaxCacheCDNReqs,
			"cacheCdnBytes":    act022MaxCacheCDNBytes,
			"minSpacingMs":     spacing.Milliseconds(),
			"timeoutSec":       int(act022Timeout.Seconds()),
		},
	}

	ctx, cancel := context.WithTimeout(parentCtx, act022Timeout)
	defer cancel()

	runner := newWeiboStepRunner(&rc.Steps, ck, redactor)

	// -------------------------------------------------------------------------
	// Step 1: V-01 — No-cookie control + strict PC 2-step login confirmation.
	// Honors pre-used Step 1 calls from usage-act022.json (2 already used -> <=3 left).
	// -------------------------------------------------------------------------
	var selfUID string
	runner.run("v01_login_control", func(f map[string]any) error {
		ck.setPhase("login")
		defer ck.setPhase("")

		f["preUsedLoginCalls"] = preUsedLogin
		f["cookieInitialHasSUB"] = cleanedInitial["SUB"] != ""
		f["cookieInitialHasSUBP"] = cleanedInitial["SUBP"] != ""
		f["cookieInitialHasXSRF"] = cleanedInitial["XSRF-TOKEN"] != ""
		f["cookieInitialHasWBPSESS"] = cleanedInitial["WBPSESS"] != ""
		f["cookieInitialHasSSOLoginState"] = cleanedInitial["SSOLoginState"] != ""

		// 1a. Unauthenticated control on getBasicInfo.
		ck.guestControlActive.Store(true)
		guestBasic, guestErr := wbClient.CheckBasicInfoGuest(ctx)
		ck.guestControlActive.Store(false)
		guestTel := ck.snapshotLastTelemetry()
		if guestBasic != nil {
			redactor.addIDs(guestBasic.UID)
			redactor.addHandles(guestBasic.ScreenName, guestBasic.Domain)
		}
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("guest control stopped: %s", stopped)
		}
		guestRejected := guestErr != nil && site.IsKind(guestErr, site.ErrKindAuthRequired)
		f["guestHTTPStatus"] = guestTel.HTTPStatus
		f["guestHasOK"] = guestTel.HasOK
		if guestTel.HasOK {
			f["guestOK"] = guestTel.OK
		}
		f["guestErrKind"] = site.KindOf(guestErr)
		f["guestRejectedAuthRequired"] = guestRejected
		if guestErr == nil {
			return errors.New("getBasicInfo without Cookie unexpectedly succeeded")
		}

		// 1b. Strict PC 2-step login via production Backend.LoginWithCookie
		// (GET /ajax/setting/getBasicInfo + GET /ajax/profile/info?screen_name=<nick>).
		st, loginErr := backend.LoginWithCookie(ctx, "weibo", rawCookie, "")
		if loginErr != nil {
			return loginErr
		}
		selfUID = strings.TrimSpace(st.UID)
		redactor.addIDs(selfUID)
		redactor.addHandles(st.Uname)

		f["loggedIn"] = st.LoggedIn
		f["source"] = st.Source
		f["hasUID"] = selfUID != ""
		f["hasScreenName"] = strings.TrimSpace(st.Uname) != ""
		f["hasAvatar"] = strings.TrimSpace(st.Face) != ""

		rc.Verifications["V-01"] = map[string]any{
			"status":                    "verified",
			"guestRejectedAuthRequired": guestRejected,
			"guestHTTPStatus":           guestTel.HTTPStatus,
			"guestOK":                   guestTel.OK,
			"loggedIn":                  st.LoggedIn,
			"pcTwoStepResolvedUID":      selfUID != "",
			"hasScreenName":             strings.TrimSpace(st.Uname) != "",
			"hasAvatar":                 strings.TrimSpace(st.Face) != "",
			"cookieHasXSRF":             cleanedInitial["XSRF-TOKEN"] != "",
			"cookieHasSSOLoginState":    cleanedInitial["SSOLoginState"] != "",
		}
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 2: V-02 — t.cn short-link expansion (no Cookie, <=3 hops) & shareTexts.
	// -------------------------------------------------------------------------
	runner.run("v02_short_link_and_share", func(f map[string]any) error {
		if len(tg.ShortLinks) == 0 && len(tg.ShareTexts) == 0 {
			rc.Verifications["V-02"] = map[string]any{"status": "not-run", "reason": "no shortLinks or shareTexts provided"}
			return errSkip
		}

		v02 := map[string]any{
			"shareTextsCount": len(tg.ShareTexts),
		}
		if len(tg.ShareTexts) > 0 {
			extracted := 0
			for _, st := range tg.ShareTexts {
				if u, ok := weibo.ExtractShareURL(st); ok && u != "" {
					extracted++
					redactor.addURLs(u)
				}
			}
			v02["shareTextsExtracted"] = extracted
		} else {
			v02["shareTextStatus"] = "not-run"
		}

		if len(tg.ShortLinks) > 0 {
			ck.setPhase("short_link")
			ck.guestControlActive.Store(true)
			finalURL, hops, slErr := wbClient.ExpandShortLink(ctx, tg.ShortLinks[0])
			ck.guestControlActive.Store(false)
			ck.setPhase("")

			v02["shortLinkHops"] = hops
			if finalURL != "" {
				redactor.addURLs(finalURL)
				if u, uErr := url.Parse(finalURL); uErr == nil {
					v02["landedHostClass"] = hostClass(u.Hostname())
					v02["landedScheme"] = strings.ToLower(u.Scheme)
					// Protect path IDs from leaking in any error message.
					for _, seg := range site.SplitCleanPath(u.Path) {
						redactor.addIDs(seg)
					}
				}
				v02["resolved"] = true
			}
			if slErr != nil {
				errKind := site.KindOf(slErr)
				v02["resolved"] = false
				v02["errKind"] = errKind
				if ck.stopReason() != "" || errors.Is(slErr, sitecheck.ErrQuotaExceeded) ||
					errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited {
					for k, v := range v02 {
						f[k] = v
					}
					rc.Verifications["V-02"] = v02
					return slErr
				}
				// Unauthenticated t.cn landing on login wall or external block is recorded without halting ACT-022.
				v02["landedOnLoginWall"] = errKind == site.ErrKindAuthRequired
				v02["blockedExternal"] = errKind == site.ErrKindBlocked
			}
			v02["status"] = "verified"
		} else {
			v02["shortLinkStatus"] = "not-run"
		}

		for k, v := range v02 {
			f[k] = v
		}
		rc.Verifications["V-02"] = v02
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 3: V-03 — /n/<nickname> & customDomain -> stable UID resolution (<=2 API).
	// -------------------------------------------------------------------------
	runner.run("v03_resolve_nick_and_custom", func(f map[string]any) error {
		var nickUser, customUser *parsedAct022User
		for i := range parsedUsers {
			pu := &parsedUsers[i]
			if nickUser == nil && (hasTag(pu.Item.Expect, "nickname") || pu.Nickname != "") && pu.Nickname != "" {
				nickUser = pu
			}
			if customUser == nil && (hasTag(pu.Item.Expect, "customDomain") || pu.CustomDomain != "") && pu.CustomDomain != "" {
				customUser = pu
			}
		}
		if nickUser == nil && customUser == nil {
			rc.Verifications["V-03"] = map[string]any{"status": "not-run", "reason": "no nickname or customDomain target provided"}
			return errSkip
		}

		ck.setPhase("resolve_user")
		defer ck.setPhase("")

		v03 := map[string]any{"status": "verified"}
		if nickUser != nil {
			prof, err := wbClient.GetUserProfileByScreenName(ctx, nickUser.Nickname)
			if err != nil {
				return err
			}
			redactor.addIDs(prof.UID)
			redactor.addHandles(prof.ScreenName, prof.Domain)
			v03["nicknameResolvedUID"] = prof.UID != ""
			v03["nicknameHasScreenName"] = prof.ScreenName != ""
			v03["nicknameStatusesCount"] = prof.StatusesCount
		} else {
			v03["nicknameStatus"] = "not-run"
		}

		if customUser != nil {
			prof, err := wbClient.GetUserProfileByCustom(ctx, customUser.CustomDomain)
			if err != nil {
				return err
			}
			redactor.addIDs(prof.UID)
			redactor.addHandles(prof.ScreenName, prof.Domain)
			v03["customDomainResolvedUID"] = prof.UID != ""
			v03["customDomainHasScreenName"] = prof.ScreenName != ""
			v03["customDomainStatusesCount"] = prof.StatusesCount
		} else {
			v03["customDomainStatus"] = "not-run"
		}

		for k, v := range v03 {
			f[k] = v
		}
		rc.Verifications["V-03"] = v03
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 4: V-04 & V-05 — User profile, mymblog p1+p2, no-cookie control,
	// feature=1 original filter, and visibility-window (halfYear / 3-year) checks.
	// Budget: user_timeline <= 6 (+ optional 1 reserve call if 2nd halfYear target exists).
	// -------------------------------------------------------------------------
	runner.run("v04_v05_user_profile_and_mymblog", func(f map[string]any) error {
		var activeUser *parsedAct022User
		var halfYearUsers []*parsedAct022User
		for i := range parsedUsers {
			pu := &parsedUsers[i]
			if pu.UID == "" {
				continue
			}
			if activeUser == nil && (hasTag(pu.Item.Expect, "active") || hasTag(pu.Item.Expect, "pinned") || hasTag(pu.Item.Expect, "fastRetweet")) {
				activeUser = pu
			}
			if hasTag(pu.Item.Expect, "halfYear") {
				halfYearUsers = append(halfYearUsers, pu)
			}
		}
		if activeUser == nil && len(halfYearUsers) == 0 {
			rc.Verifications["V-04"] = map[string]any{"status": "not-run", "reason": "no active or halfYear user target provided"}
			rc.Verifications["V-05"] = map[string]any{"status": "not-run", "reason": "no active or halfYear user target provided"}
			return errSkip
		}

		ck.setPhase("user_timeline")
		defer ck.setPhase("")

		v04 := map[string]any{}
		v05 := map[string]any{}

		if activeUser != nil {
			// Call 1: profile/info
			prof, err := wbClient.GetUserProfile(ctx, activeUser.UID)
			if err != nil {
				return err
			}
			redactor.addIDs(prof.UID)
			redactor.addHandles(prof.ScreenName, prof.Domain)
			v04["profileHasUID"] = prof.UID == activeUser.UID
			v04["profileStatusesCount"] = prof.StatusesCount
			v04["profileHasAvatar"] = prof.AvatarURL != ""

			// Call 2: mymblog page 1 (feature=0)
			seen := make(map[string]bool)
			p1, err := wbClient.GetUserPosts(ctx, weibo.UserPostsRequest{
				UID:          activeUser.UID,
				Page:         1,
				Feature:      weibo.FeatureAll,
				KnownProfile: prof,
				SeenIDs:      seen,
			})
			if err != nil {
				return err
			}
			pinnedCount, fastRetweetCount, retweetCount := 0, 0, 0
			fastRetweetOwnerMatchesCreator := 0
			pinnedAllLeading := true
			sawNonPinned := false
			p1IDs := make(map[string]bool, len(p1.Posts))
			for i := range p1.Posts {
				p := &p1.Posts[i]
				redactor.addPost(p)
				p1IDs[p.Mid] = true
				if p.IsTop {
					pinnedCount++
					if sawNonPinned {
						pinnedAllLeading = false
					}
				} else {
					sawNonPinned = true
				}
				if p.IsFastRetweet {
					fastRetweetCount++
					if p.Author.UID == activeUser.UID {
						fastRetweetOwnerMatchesCreator++
					}
				}
				if p.IsRetweet {
					retweetCount++
				}
			}
			v04["page1Items"] = len(p1.Posts)
			v04["page1HasMore"] = p1.HasMore
			v04["page1HasSinceID"] = p1.NextSinceID != ""
			v04["page1UncertainEmpty"] = p1.UncertainEmpty

			v05["page1PinnedCount"] = pinnedCount
			v05["page1PinnedLeading"] = pinnedAllLeading
			v05["page1FastRetweetCount"] = fastRetweetCount
			v05["page1FastRetweetOwnerMatchesCreator"] = fastRetweetOwnerMatchesCreator
			v05["page1RetweetCount"] = retweetCount

			// Call 3: mymblog page 2 (page=2 + since_id)
			if p1.HasMore && p1.NextSinceID != "" {
				p2, p2Err := wbClient.GetUserPosts(ctx, weibo.UserPostsRequest{
					UID:          activeUser.UID,
					Page:         p1.NextPage,
					SinceID:      p1.NextSinceID,
					Feature:      weibo.FeatureAll,
					KnownProfile: prof,
					SeenIDs:      seen,
				})
				if p2Err != nil {
					return p2Err
				}
				dupes := 0
				for i := range p2.Posts {
					p := &p2.Posts[i]
					redactor.addPost(p)
					if p1IDs[p.Mid] {
						dupes++
					}
				}
				v04["page2Fetched"] = true
				v04["page2Items"] = len(p2.Posts)
				v04["page2HasMore"] = p2.HasMore
				v04["page2CursorAdvanced"] = p2.NextSinceID != p1.NextSinceID
				v04["duplicateAcrossPages"] = dupes > 0
			} else {
				v04["page2Fetched"] = false
			}

			// Call 4: unauthenticated mymblog control (guestControlActive=true)
			ck.guestControlActive.Store(true)
			guestPage, guestErr := wbClient.GetUserPosts(ctx, weibo.UserPostsRequest{
				UID:                 activeUser.UID,
				Page:                1,
				Feature:             weibo.FeatureAll,
				UnauthenticatedCall: true,
			})
			ck.guestControlActive.Store(false)
			guestTel := ck.snapshotLastTelemetry()
			if guestPage != nil {
				for i := range guestPage.Posts {
					redactor.addPost(&guestPage.Posts[i])
				}
				v04["guestMymblogItems"] = len(guestPage.Posts)
				v04["guestMymblogUncertainEmpty"] = guestPage.UncertainEmpty
			}
			if stopped := ck.stopReason(); stopped != "" {
				return fmt.Errorf("guest mymblog control stopped: %s", stopped)
			}
			v04["guestMymblogHTTPStatus"] = guestTel.HTTPStatus
			if guestTel.HasOK {
				v04["guestMymblogOK"] = guestTel.OK
			}
			v04["guestMymblogErrKind"] = site.KindOf(guestErr)
			v04["status"] = "verified"

			// Call 5: feature=1 original-only filter check (V-05 / weibo.md §13.1 #6)
			origPage, origErr := wbClient.GetUserPosts(ctx, weibo.UserPostsRequest{
				UID:          activeUser.UID,
				Page:         1,
				Feature:      weibo.FeatureOriginal,
				KnownProfile: prof,
			})
			if origErr != nil {
				return origErr
			}
			origRetweets, origFastRetweets := 0, 0
			for i := range origPage.Posts {
				p := &origPage.Posts[i]
				redactor.addPost(p)
				if p.IsRetweet {
					origRetweets++
				}
				if p.IsFastRetweet {
					origFastRetweets++
				}
			}
			v05["feature1Items"] = len(origPage.Posts)
			v05["feature1RetweetCount"] = origRetweets
			v05["feature1FastRetweetCount"] = origFastRetweets
			v05["feature1ExcludesRetweetsAndFastRetweets"] = origRetweets == 0 && origFastRetweets == 0
			v05["status"] = "verified"
		} else {
			v04["status"] = "not-run"
		}

		// Call 6 (+ optional 1 reserve call for a 2nd visibility-window user): halfYear / visibility window check.
		var visSummaries []map[string]any
		for idx, hy := range halfYearUsers {
			if idx > 0 {
				if gate.Usage().Categories["reserve"] >= act022APILimits["reserve"] {
					break
				}
				ck.setPhase("reserve")
			}
			hyPage, hyErr := wbClient.GetUserPosts(ctx, weibo.UserPostsRequest{
				UID:     hy.UID,
				Page:    1,
				Feature: weibo.FeatureAll,
			})
			if idx > 0 {
				ck.setPhase("user_timeline")
			}
			if hyErr != nil {
				return hyErr
			}
			for i := range hyPage.Posts {
				redactor.addPost(&hyPage.Posts[i])
			}
			visSummaries = append(visSummaries, map[string]any{
				"windowIndex":       idx,
				"items":             len(hyPage.Posts),
				"total":             hyPage.Total,
				"statusVisible":     hyPage.StatusVisible,
				"hasMore":           hyPage.HasMore,
				"hasSinceID":        hyPage.NextSinceID != "",
				"bottomTipsVisible": hyPage.BottomTipsVisible,
				"hasBottomTipsText": strings.TrimSpace(hyPage.BottomTipsText) != "",
				"uncertainEmpty":    hyPage.UncertainEmpty,
			})
		}
		if len(visSummaries) > 0 {
			v05["visibilityWindows"] = visSummaries
			v05["status"] = "verified"
		} else if len(v05) == 0 {
			v05["status"] = "not-run"
		}

		f["v04"] = v04
		f["v05"] = v05
		rc.Verifications["V-04"] = v04
		rc.Verifications["V-05"] = v05
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 5: V-06, V-07 (detail), V-08 (detail), V-09 — Single post details & video component.
	// Budget: post_detail <= 7 (+ optional reserve <= 1 if longtext + guest video both needed).
	// -------------------------------------------------------------------------
	type probedPost struct {
		target parsedAct022Post
		post   *weibo.Post
	}
	var (
		over9Post     *probedPost
		mixedPost     *probedPost
		videoPost     *weibo.Post
		videoPageRes  *weibo.VideoPageResult
		videoTargetID string // mid when host post exists, else 1034:... oid
		videoIsOID    bool
	)

	runner.run("v06_v09_post_details_and_video", func(f map[string]any) error {
		var longTextTarget, longRetweetTarget, over9Target, mixedTarget, videoTarget *parsedAct022Post
		for i := range parsedPosts {
			pp := &parsedPosts[i]
			if hasTag(pp.Item.Expect, "deletedOrigin") {
				continue // Handled in Step 6 (V-10)
			}
			if longTextTarget == nil && hasTag(pp.Item.Expect, "longText") && pp.Mid != "" {
				longTextTarget = pp
			}
			if longRetweetTarget == nil && hasTag(pp.Item.Expect, "longRetweet") && pp.Mid != "" {
				longRetweetTarget = pp
			}
			if over9Target == nil && hasTag(pp.Item.Expect, "over9") && pp.Mid != "" {
				over9Target = pp
			}
			if mixedTarget == nil && (hasTag(pp.Item.Expect, "gif") || hasTag(pp.Item.Expect, "livePhoto") || hasTag(pp.Item.Expect, "mixed")) && pp.Mid != "" {
				mixedTarget = pp
			}
			if videoTarget == nil && (hasTag(pp.Item.Expect, "video") || hasTag(pp.Item.Expect, "videoPage")) {
				videoTarget = pp
			}
		}
		if longTextTarget == nil && longRetweetTarget == nil && over9Target == nil && mixedTarget == nil && videoTarget == nil {
			for _, k := range []string{"V-06", "V-07", "V-08", "V-09"} {
				rc.Verifications[k] = map[string]any{"status": "not-run", "reason": "no post/video targets provided"}
			}
			return errSkip
		}

		ck.setPhase("post_detail")
		defer ck.setPhase("")

		remainingDetail := func() int {
			return act022APILimits["post_detail"] - gate.Usage().Categories["post_detail"]
		}

		// V-06: longText & longRetweet
		v06 := map[string]any{}
		if longTextTarget != nil {
			post, err := wbClient.GetPost(ctx, longTextTarget.MblogID, weibo.PostOptions{SkipLongTextFetch: true})
			if err != nil {
				return err
			}
			redactor.addPost(post)
			v06["longTextIsLong"] = post.IsLongText
			v06["longTextInlinedByShow"] = post.LongTextFetched
			v06["longTextRuneLen"] = len([]rune(post.Text))
			if post.IsLongText && !post.LongTextFetched && remainingDetail() > 0 {
				lt, ltErr := wbClient.GetLongText(ctx, post.MblogID)
				if ltErr != nil {
					if ck.stopReason() != "" || site.IsRoundStop(ltErr) || errors.Is(ltErr, sitecheck.ErrQuotaExceeded) {
						return ltErr
					}
					v06["longTextFallbackErrKind"] = site.KindOf(ltErr)
				} else if lt != nil {
					v06["longTextFallbackSucceeded"] = strings.TrimSpace(lt.LongTextContent) != ""
					v06["longTextFallbackRuneLen"] = len([]rune(lt.LongTextContent))
				}
			}
			v06["status"] = "verified"
		} else {
			v06["longTextStatus"] = "not-run"
		}
		if longRetweetTarget != nil && remainingDetail() > 0 {
			post, err := wbClient.GetPost(ctx, longRetweetTarget.MblogID, weibo.PostOptions{SkipLongTextFetch: true})
			if err != nil {
				return err
			}
			redactor.addPost(post)
			v06["longRetweetIsRetweet"] = post.IsRetweet
			if post.Retweeted != nil {
				v06["longRetweetOriginIsLong"] = post.Retweeted.IsLongText
				v06["longRetweetOriginInlinedByShow"] = post.Retweeted.LongTextFetched
				v06["longRetweetOriginRuneLen"] = len([]rune(post.Retweeted.Text))
			}
			v06["status"] = "verified"
		} else if longRetweetTarget == nil {
			v06["longRetweetStatus"] = "not-run"
		}
		if _, ok := v06["status"]; !ok {
			v06["status"] = "not-run"
		}
		rc.Verifications["V-06"] = v06

		// V-07 metadata: over9 post
		v07 := map[string]any{}
		if over9Target != nil && remainingDetail() > 0 {
			post, err := wbClient.GetPost(ctx, over9Target.MblogID, weibo.PostOptions{SkipLongTextFetch: true})
			if err != nil {
				return err
			}
			redactor.addPost(post)
			over9Post = &probedPost{target: *over9Target, post: post}
			imgs, _ := post.EffectiveMedia()
			v07["imageCount"] = len(imgs)
			v07["over9Confirmed"] = len(imgs) > 9
			v07["ruleType"] = post.RuleType
			if len(imgs) > 0 && imgs[0].URL != "" {
				if u, uErr := url.Parse(imgs[0].URL); uErr == nil {
					v07["firstImageHostClass"] = hostClass(u.Hostname())
				}
			}
			v07["status"] = "verified"
		} else {
			v07["status"] = "not-run"
		}
		rc.Verifications["V-07"] = v07

		// V-08 metadata: gif / livePhoto / mixed post
		v08 := map[string]any{}
		if mixedTarget != nil && remainingDetail() > 0 {
			post, err := wbClient.GetPost(ctx, mixedTarget.MblogID, weibo.PostOptions{SkipLongTextFetch: true})
			if err != nil {
				return err
			}
			redactor.addPost(post)
			mixedPost = &probedPost{target: *mixedTarget, post: post}
			imgs, _ := post.EffectiveMedia()
			kindCounts := map[string]int{}
			liveNeedsHop := 0
			gifHasVideo := 0
			motionHosts := map[string]int{}
			for _, im := range imgs {
				kindCounts[im.Kind]++
				if im.NeedsLivePhotoHop {
					liveNeedsHop++
				}
				if im.GIFVideoURL != "" {
					gifHasVideo++
				}
				if im.MotionURL != "" {
					if u, uErr := url.Parse(im.MotionURL); uErr == nil {
						motionHosts[hostClass(u.Hostname())]++
					}
				}
			}
			v08["mediaCount"] = len(imgs)
			v08["mediaKinds"] = kindCounts
			v08["ruleType"] = post.RuleType
			v08["livePhotoNeedsMediaPlayHop"] = liveNeedsHop
			v08["gifHasTranscodedMP4"] = gifHasVideo
			if len(motionHosts) > 0 {
				v08["motionHostClasses"] = motionHosts
			}
			v08["status"] = "verified"
		} else {
			v08["status"] = "not-run"
		}
		rc.Verifications["V-08"] = v08

		// V-09: video / videoPage (1034: component + logged-in show + no-cookie show comparison)
		v09 := map[string]any{}
		if videoTarget != nil && remainingDetail() > 0 {
			if videoTarget.VideoOID != "" {
				vr, err := wbClient.GetVideo(ctx, videoTarget.VideoOID, weibo.PostOptions{SkipHostPostLookup: true})
				if err != nil {
					return err
				}
				videoPageRes = vr
				redactor.addIDs(vr.ObjectID, vr.HostMid, vr.AuthorUID)
				redactor.addHandles(vr.AuthorName, vr.Title)
				v09["componentSucceeded"] = true
				v09["componentHasHostMid"] = vr.HostMid != ""
				v09["componentHasAuthorUID"] = vr.AuthorUID != ""
				if vr.Video != nil {
					v09["componentDurationSec"] = vr.Video.DurationSec
					v09["componentStreamCount"] = len(vr.Video.Streams)
					if vr.Video.Selected != nil {
						v09["componentSelectedQuality"] = vr.Video.Selected.QualityLabel
						v09["componentSelectedCodec"] = vr.Video.Selected.Codec
					}
				}
				if vr.HostMid != "" {
					videoTargetID = vr.HostMid
					videoIsOID = false
					if remainingDetail() > 0 {
						hp, hpErr := wbClient.GetPost(ctx, vr.HostMid, weibo.PostOptions{SkipLongTextFetch: true})
						if hpErr != nil {
							return hpErr
						}
						redactor.addPost(hp)
						videoPost = hp
						_, vid := hp.EffectiveMedia()
						if vid != nil {
							v09["authStreamCount"] = len(vid.Streams)
							v09["authLegacyFallback"] = vid.LegacyFallback
							v09["authDurationSec"] = vid.DurationSec
							if vid.Selected != nil {
								v09["authSelectedQuality"] = vid.Selected.QualityLabel
								v09["authSelectedCodec"] = vid.Selected.Codec
								v09["authSelectedHeight"] = vid.Selected.Height
							}
						}
					}
				} else {
					videoTargetID = vr.ObjectID
					videoIsOID = true
				}
			} else if videoTarget.Mid != "" {
				videoTargetID = videoTarget.Mid
				videoIsOID = false
				hp, err := wbClient.GetPost(ctx, videoTarget.MblogID, weibo.PostOptions{SkipLongTextFetch: true})
				if err != nil {
					return err
				}
				redactor.addPost(hp)
				videoPost = hp
				_, vid := hp.EffectiveMedia()
				if vid != nil {
					v09["authStreamCount"] = len(vid.Streams)
					v09["authLegacyFallback"] = vid.LegacyFallback
					v09["authDurationSec"] = vid.DurationSec
					if vid.Selected != nil {
						v09["authSelectedQuality"] = vid.Selected.QualityLabel
						v09["authSelectedCodec"] = vid.Selected.Codec
						v09["authSelectedHeight"] = vid.Selected.Height
					}
				}
			}

			// Optional guest vs logged-in video quality comparison on the host post (uses remaining post_detail or 1 reserve).
			if videoPost != nil && videoPost.MblogID != "" {
				usePhase := ""
				if remainingDetail() > 0 {
					usePhase = "post_detail"
				} else if gate.Usage().Categories["reserve"] < act022APILimits["reserve"] {
					usePhase = "reserve"
				}
				if usePhase != "" {
					ck.setPhase(usePhase)
					ck.guestControlActive.Store(true)
					guestPost, gErr := wbClient.GetPost(ctx, videoPost.MblogID, weibo.PostOptions{
						SkipLongTextFetch:   true,
						UnauthenticatedCall: true,
					})
					ck.guestControlActive.Store(false)
					ck.setPhase("post_detail")
					if stopped := ck.stopReason(); stopped != "" {
						return fmt.Errorf("guest video control stopped: %s", stopped)
					}
					if gErr != nil {
						v09["guestShowErrKind"] = site.KindOf(gErr)
					} else if guestPost != nil {
						redactor.addPost(guestPost)
						_, gVid := guestPost.EffectiveMedia()
						if gVid != nil {
							v09["guestStreamCount"] = len(gVid.Streams)
							if gVid.Selected != nil {
								v09["guestSelectedQuality"] = gVid.Selected.QualityLabel
								v09["guestSelectedHeight"] = gVid.Selected.Height
							}
						}
					}
				}
			}
			v09["status"] = "verified"
		} else {
			v09["status"] = "not-run"
		}
		rc.Verifications["V-09"] = v09

		f["v06"] = v06
		f["v07"] = v07
		f["v08"] = v08
		f["v09"] = v09
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 6: V-10 — Nonexistent UID/MID error codes + deleted-origin retweet structure.
	// Budget: error_probe <= 3 API calls.
	// -------------------------------------------------------------------------
	runner.run("v10_error_codes_and_deleted_origin", func(f map[string]any) error {
		ck.setPhase("error_probe")
		defer ck.setPhase("")

		v10 := map[string]any{"status": "verified"}
		ep := wbClient.Endpoints()

		// Call 1: nonexistent synthetic UID on profile/info
		ck.expectedBusinessErrorActive.Store(true)
		uidRes, uidErr := wbClient.ProbeRaw(ctx, http.MethodGet, ep.ProfileInfo, url.Values{"uid": {act022NonexistentSynthUID}}, "", false, "")
		ck.expectedBusinessErrorActive.Store(false)
		uidTel := ck.snapshotLastTelemetry()
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("v10 uid probe stopped: %s", stopped)
		}
		if uidErr != nil {
			return uidErr
		}
		uidSe := weibo.ClassifyHTTPAndEnvelopeError(weibo.EnvelopeErrorContext{
			Endpoint:   "/ajax/profile/info",
			StatusCode: uidRes.StatusCode,
			Header:     uidRes.Header,
			Body:       uidRes.Body,
			HasCookie:  true,
		})
		v10["nonexistentUser"] = map[string]any{
			"httpStatus":     uidTel.HTTPStatus,
			"hasOK":          uidTel.HasOK,
			"ok":             uidTel.OK,
			"errno":          uidTel.Errno,
			"errorCode":      uidTel.ErrorCode,
			"classifiedKind": classifiedKindOf(uidSe),
			"hasDataUser":    jsonDataHasNonEmptyKey(uidRes.Body, "user"),
		}

		// Call 2: nonexistent synthetic MID on statuses/show
		ck.expectedBusinessErrorActive.Store(true)
		midRes, midErr := wbClient.ProbeRaw(ctx, http.MethodGet, ep.StatusesShow, url.Values{"id": {act022NonexistentSynthMid}, "locale": {"zh-CN"}}, "", false, "")
		ck.expectedBusinessErrorActive.Store(false)
		midTel := ck.snapshotLastTelemetry()
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("v10 mid probe stopped: %s", stopped)
		}
		if midErr != nil {
			return midErr
		}
		midSe := weibo.ClassifyHTTPAndEnvelopeError(weibo.EnvelopeErrorContext{
			Endpoint:   "/ajax/statuses/show",
			StatusCode: midRes.StatusCode,
			Header:     midRes.Header,
			Body:       midRes.Body,
			HasCookie:  true,
		})
		v10["nonexistentPost"] = map[string]any{
			"httpStatus":     midTel.HTTPStatus,
			"hasOK":          midTel.HasOK,
			"ok":             midTel.OK,
			"errno":          midTel.Errno,
			"errorCode":      midTel.ErrorCode,
			"classifiedKind": classifiedKindOf(midSe),
			"hasMidField":    jsonTopHasNonEmptyKey(midRes.Body, "mid") || jsonTopHasNonEmptyKey(midRes.Body, "idstr"),
		}

		// Call 3: deleted-origin retweet post (if provided in targets-weibo.json)
		var delOriginTarget *parsedAct022Post
		for i := range parsedPosts {
			if hasTag(parsedPosts[i].Item.Expect, "deletedOrigin") && parsedPosts[i].Mid != "" {
				delOriginTarget = &parsedPosts[i]
				break
			}
		}
		if delOriginTarget != nil {
			delPost, delErr := wbClient.GetPost(ctx, delOriginTarget.MblogID, weibo.PostOptions{SkipLongTextFetch: true})
			if delErr != nil {
				if ck.stopReason() != "" || site.IsRoundStop(delErr) || errors.Is(delErr, sitecheck.ErrQuotaExceeded) {
					return delErr
				}
				v10["deletedOriginErrKind"] = site.KindOf(delErr)
			} else if delPost != nil {
				redactor.addPost(delPost)
				delInfo := map[string]any{
					"isRetweet":       delPost.IsRetweet,
					"visibleType":     delPost.Visible.Type,
					"hasVisibleLabel": strings.TrimSpace(delPost.Visible.Label) != "",
				}
				if delPost.Retweeted != nil {
					delInfo["retweetedUnavailable"] = delPost.Retweeted.Unavailable
					delInfo["retweetedHasReason"] = strings.TrimSpace(delPost.Retweeted.UnavailableReason) != ""
				} else {
					delInfo["retweetedMissing"] = true
				}
				v10["deletedOrigin"] = delInfo
			}
		} else {
			v10["deletedOriginStatus"] = "not-run"
		}

		for k, v := range v10 {
			f[k] = v
		}
		rc.Verifications["V-10"] = v10
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 7: V-07 (watermark probe), V-11 (image CDN), V-12 (video Range + LivePhoto + mp4ff).
	// Budget: probeCDN <= 10 requests, <= 60 MiB, 0 API calls, strictly NO Cookie!
	// -------------------------------------------------------------------------
	runner.run("v07_v11_v12_media_cdn_probes", func(f map[string]any) error {
		var sampleImgURL, sampleGIFURL, sampleLivePhotoURL string
		var sampleVideoURL string
		var sampleVideoHintSize int64
		var sampleVideoDur float64

		if over9Post != nil && over9Post.post != nil {
			imgs, _ := over9Post.post.EffectiveMedia()
			for _, im := range imgs {
				if sampleImgURL == "" && im.Kind == weibo.MediaKindImage && im.URL != "" {
					sampleImgURL = im.URL
					break
				}
			}
		}
		if mixedPost != nil && mixedPost.post != nil {
			imgs, _ := mixedPost.post.EffectiveMedia()
			for _, im := range imgs {
				if sampleImgURL == "" && im.Kind == weibo.MediaKindImage && im.URL != "" {
					sampleImgURL = im.URL
				}
				if sampleGIFURL == "" && im.Kind == weibo.MediaKindGIF && im.URL != "" {
					sampleGIFURL = im.URL
				}
				if sampleLivePhotoURL == "" && im.Kind == weibo.MediaKindLivePhoto && im.MotionURL != "" {
					sampleLivePhotoURL = im.MotionURL
				}
			}
		}
		if videoPost != nil {
			_, vid := videoPost.EffectiveMedia()
			if vid != nil && vid.Selected != nil && vid.Selected.URL != "" {
				sampleVideoURL = vid.Selected.URL
				sampleVideoHintSize = vid.Selected.Size
				sampleVideoDur = float64(vid.DurationSec)
			}
		} else if videoPageRes != nil && videoPageRes.Video != nil && videoPageRes.Video.Selected != nil && videoPageRes.Video.Selected.URL != "" {
			sampleVideoURL = videoPageRes.Video.Selected.URL
			sampleVideoHintSize = videoPageRes.Video.Selected.Size
			sampleVideoDur = float64(videoPageRes.Video.DurationSec)
		}

		if sampleImgURL == "" && sampleGIFURL == "" && sampleVideoURL == "" && sampleLivePhotoURL == "" {
			rc.Verifications["V-11"] = map[string]any{"status": "not-run", "reason": "no media URLs collected in Step 5"}
			rc.Verifications["V-12"] = map[string]any{"status": "not-run", "reason": "no video/livephoto URLs collected in Step 5"}
			return errSkip
		}

		ck.setPhase("probe_cdn")
		defer ck.setPhase("")

		mediaSpec := wbClient.MediaTransport()
		liveSpec := wbClient.LivePhotoMediaTransport()

		v11 := map[string]any{}
		if sampleImgURL != "" && ck.canAffordProbeCDN(2<<20, 1) {
			imgFact, imgErr := probeWeiboImageAsset(ctx, probeMediaHTTP, mediaSpec, sampleImgURL, 12<<20, false)
			if imgErr != nil {
				return imgErr
			}
			v11["primaryImage"] = imgFact
			v11["status"] = "verified"

			// V-07 watermark-free rewrite probes (/oslarge/, /osj1080/) — expected 404/403 does not stop ACT-022.
			wmProbes := map[string]any{}
			for _, variant := range []string{"oslarge", "osj1080"} {
				rewritten, ok := rewriteWeiboImageVariant(sampleImgURL, variant)
				if !ok || !ck.canAffordProbeCDN(2<<20, 1) {
					continue
				}
				ck.expectedCDNErrorActive.Store(true)
				wFact, wErr := probeWeiboImageAsset(ctx, probeMediaHTTP, mediaSpec, rewritten, 12<<20, false)
				ck.expectedCDNErrorActive.Store(false)
				if stopped := ck.stopReason(); stopped != "" {
					return fmt.Errorf("watermark probe stopped: %s", stopped)
				}
				if wErr != nil {
					if wFact == nil {
						wFact = map[string]any{}
					}
					wFact["available"] = false
					wFact["errKind"] = site.KindOf(wErr)
				} else {
					if wFact == nil {
						wFact = map[string]any{}
					}
					wFact["available"] = true
					if origW, ok1 := imgFact["width"].(int); ok1 {
						if newW, ok2 := wFact["width"].(int); ok2 && origW > 0 {
							wFact["sameOrLargerWidth"] = newW >= origW
						}
					}
				}
				wmProbes[variant] = wFact
			}
			if len(wmProbes) > 0 {
				if v07Map, ok := rc.Verifications["V-07"].(map[string]any); ok {
					v07Map["watermarkFreeProbes"] = wmProbes
				}
				f["v07WatermarkProbes"] = wmProbes
			}
		} else {
			v11["status"] = "not-run"
		}

		if sampleGIFURL != "" && ck.canAffordProbeCDN(2<<20, 1) {
			gifFact, gifErr := probeWeiboImageAsset(ctx, probeMediaHTTP, mediaSpec, sampleGIFURL, 12<<20, true)
			if gifErr != nil {
				if ck.stopReason() != "" || errors.Is(gifErr, sitecheck.ErrQuotaExceeded) {
					return gifErr
				}
				v11["gifProbeErrKind"] = site.KindOf(gifErr)
			} else {
				v11["gifProbe"] = gifFact
				if v08Map, ok := rc.Verifications["V-08"].(map[string]any); ok {
					v08Map["gifMagicFormat"] = gifFact["magicFormat"]
					v08Map["gifAnimated"] = gifFact["gifAnimated"]
					v08Map["gifFrameCount"] = gifFact["gifFrameCount"]
				}
			}
		}
		rc.Verifications["V-11"] = v11

		v12 := map[string]any{}
		var videoProbeErr error
		if sampleVideoURL != "" && ck.canAffordProbeCDN(9<<20, 2) {
			if remSec, ok := computeExpiresRemainingSec(sampleVideoURL); ok {
				v12["videoExpiresRemainingSec"] = remSec
			}
			vFact, vErr := probeOrDownloadWeiboMP4(ctx, probeMediaHTTP, mediaSpec, workDir, "v12-video.mp4",
				sampleVideoURL, sampleVideoHintSize, sampleVideoDur, true, ck)
			if vFact != nil {
				v12["video"] = vFact
			}
			if vErr != nil {
				v12["videoStatus"] = "failed"
				v12["videoErrKind"] = site.KindOf(vErr)
				if ck.stopReason() != "" || errors.Is(vErr, sitecheck.ErrQuotaExceeded) {
					v12["status"] = "failed"
					rc.Verifications["V-12"] = v12
					f["v11"] = v11
					f["v12"] = v12
					return vErr
				}
				videoProbeErr = vErr
			} else {
				v12["videoStatus"] = "verified"
				v12["status"] = "verified"
			}
		} else {
			v12["videoStatus"] = "not-run"
		}

		if sampleLivePhotoURL != "" && ck.canAffordProbeCDN(4<<20, 2) {
			lpFact, lpErr := probeWeiboLivePhotoMotion(ctx, probeMediaHTTP, liveSpec, workDir, sampleLivePhotoURL, ck)
			if lpErr != nil {
				if ck.stopReason() != "" || errors.Is(lpErr, sitecheck.ErrQuotaExceeded) {
					v12["livePhotoStatus"] = "failed"
					v12["livePhotoErrKind"] = site.KindOf(lpErr)
					v12["status"] = "failed"
					rc.Verifications["V-12"] = v12
					f["v11"] = v11
					f["v12"] = v12
					return lpErr
				}
				v12["livePhotoStatus"] = "failed"
				v12["livePhotoErrKind"] = site.KindOf(lpErr)
			} else {
				v12["livePhoto"] = lpFact
				v12["livePhotoStatus"] = "verified"
				if videoProbeErr == nil {
					v12["status"] = "verified"
				}
			}
		} else {
			v12["livePhotoStatus"] = "not-run"
		}
		if videoProbeErr != nil {
			v12["status"] = "failed"
		} else if _, ok := v12["status"]; !ok {
			v12["status"] = "not-run"
		}
		rc.Verifications["V-12"] = v12

		f["v11"] = v11
		f["v12"] = v12
		if videoProbeErr != nil {
			return videoProbeErr
		}
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 8: V-13 (following, groups, favorites) & V-14 (likes single probe).
	// Budget: mine_follow <= 4, mine_fav <= 2, mine_likes <= 1 (total <= 7 API).
	// -------------------------------------------------------------------------
	runner.run("v13_v14_mine_lists", func(f map[string]any) error {
		if selfUID == "" {
			rc.Verifications["V-13"] = map[string]any{"status": "not-run", "reason": "self UID unavailable"}
			rc.Verifications["V-14"] = map[string]any{"status": "not-run", "reason": "self UID unavailable"}
			return errSkip
		}

		v13 := map[string]any{"status": "verified"}
		v14 := map[string]any{}

		// 8a. Following (core M6 path; uses <= 2 of mine_follow <= 4)
		ck.setPhase("mine_follow")
		seenFollow := make(map[string]bool)
		fp1, err := wbClient.ListFollowing(ctx, selfUID, 1, seenFollow)
		if err != nil {
			ck.setPhase("")
			return err
		}
		for _, u := range fp1.Users {
			redactor.addIDs(u.UID)
			redactor.addHandles(u.ScreenName, u.Remark)
		}
		v13["followingPage1Items"] = len(fp1.Users)
		v13["followingTotal"] = fp1.Total
		v13["followingPage1HasMore"] = fp1.HasMore
		v13["followingPage1HasNextCursor"] = fp1.NextCursor != ""

		if fp1.HasMore && fp1.NextPage > 1 {
			fp2, err2 := wbClient.ListFollowing(ctx, selfUID, fp1.NextPage, seenFollow)
			if err2 != nil {
				ck.setPhase("")
				return err2
			}
			for _, u := range fp2.Users {
				redactor.addIDs(u.UID)
				redactor.addHandles(u.ScreenName, u.Remark)
			}
			v13["followingPage2Items"] = len(fp2.Users)
			v13["followingPage2HasMore"] = fp2.HasMore
		}

		// 8b. Favorites (core M6 path; run BEFORE unverified follow-group endpoints so core V-13 evidence cannot be aborted).
		ck.setPhase("mine_fav")
		seenFav := make(map[string]bool)
		fav1, favErr := wbClient.ListFavorites(ctx, selfUID, 1, seenFav)
		if favErr != nil {
			ck.setPhase("")
			return favErr
		}
		for i := range fav1.Posts {
			redactor.addPost(&fav1.Posts[i])
		}
		v13["favoritesPage1Items"] = len(fav1.Posts)
		v13["favoritesTotal"] = fav1.Total
		v13["favoritesPage1HasMore"] = fav1.HasMore
		v13["favoritesDataStatusKeyOK"] = true

		if fav1.HasMore && fav1.NextPage > 1 {
			fav2, favErr2 := wbClient.ListFavorites(ctx, selfUID, fav1.NextPage, seenFav)
			if favErr2 != nil {
				ck.setPhase("")
				return favErr2
			}
			for i := range fav2.Posts {
				redactor.addPost(&fav2.Posts[i])
			}
			v13["favoritesPage2Items"] = len(fav2.Posts)
			v13["favoritesPage2HasMore"] = fav2.HasMore
		}

		// 8c. Follow groups & group members (unverified optional endpoints behind default-off FollowGroupsEnabled; uses remaining <= 2 of mine_follow <= 4).
		ck.setPhase("mine_follow")
		ck.expectedBusinessErrorActive.Store(true)
		groups, gErr := wbClient.ListFollowGroups(ctx)
		ck.expectedBusinessErrorActive.Store(false)
		if gErr != nil {
			if ck.stopReason() != "" || site.IsRoundStop(gErr) || errors.Is(gErr, sitecheck.ErrQuotaExceeded) {
				ck.setPhase("")
				return gErr
			}
			v13["followGroupsAvailable"] = false
			v13["followGroupsErrKind"] = site.KindOf(gErr)
		} else {
			for _, g := range groups {
				redactor.addIDs(g.ID)
				redactor.addHandles(g.Title)
			}
			v13["followGroupsAvailable"] = true
			v13["followGroupCount"] = len(groups)
			if len(groups) > 0 {
				seenGroup := make(map[string]bool)
				ck.expectedBusinessErrorActive.Store(true)
				gm, gmErr := wbClient.ListFollowGroupMembers(ctx, selfUID, groups[0].ID, 1, seenGroup)
				ck.expectedBusinessErrorActive.Store(false)
				if gmErr != nil {
					if ck.stopReason() != "" || site.IsRoundStop(gmErr) || errors.Is(gmErr, sitecheck.ErrQuotaExceeded) {
						ck.setPhase("")
						return gmErr
					}
					v13["groupMembersAvailable"] = false
					v13["groupMembersErrKind"] = site.KindOf(gmErr)
				} else {
					for _, u := range gm.Users {
						redactor.addIDs(u.UID)
						redactor.addHandles(u.ScreenName, u.Remark)
					}
					v13["groupMembersAvailable"] = true
					v13["groupMembersPage1Items"] = len(gm.Users)
				}
			}
		}

		// 8d. Likes single probe (mine_likes <= 1; expected 404/403/0-byte/empty degrades without failing ACT-022).
		ck.setPhase("mine_likes")
		ck.expectedBusinessErrorActive.Store(true)
		seenLikes := make(map[string]bool)
		likesPage, likesErr := wbClient.ListLikes(ctx, selfUID, 1, seenLikes)
		ck.expectedBusinessErrorActive.Store(false)
		ck.setPhase("")
		likesTel := ck.snapshotLastTelemetry()
		if stopped := ck.stopReason(); stopped != "" {
			return fmt.Errorf("v14 likes probe stopped: %s", stopped)
		}
		v14["httpStatus"] = likesTel.HTTPStatus
		if likesTel.HasOK {
			v14["ok"] = likesTel.OK
		}
		if likesErr != nil {
			if errors.Is(likesErr, sitecheck.ErrQuotaExceeded) || site.IsRoundStop(likesErr) {
				return likesErr
			}
			v14["status"] = "unavailable"
			v14["available"] = false
			v14["errKind"] = site.KindOf(likesErr)
		} else {
			for i := range likesPage.Posts {
				redactor.addPost(&likesPage.Posts[i])
			}
			v14["status"] = "verified"
			v14["available"] = true
			v14["items"] = len(likesPage.Posts)
			v14["total"] = likesPage.Total
		}

		rc.Verifications["V-13"] = v13
		rc.Verifications["V-14"] = v14
		f["v13"] = v13
		f["v14"] = v14
		return nil
	})

	// -------------------------------------------------------------------------
	// Step 9: Formal cache flow via production Backend + DownloadManager in temp dir.
	// Budget: formal_cache <= 4 API calls, cacheCDN <= 20 requests / <= 100 MiB.
	// -------------------------------------------------------------------------
	runner.run("step9_formal_cache_flow", func(f map[string]any) error {
		var imageCacheMid string
		var imageCacheLabel string
		estimateCDNReqs := func(p *weibo.Post) int {
			if p == nil {
				return 999
			}
			imgs, vid := p.EffectiveMedia()
			reqs := 1 // cover / metadata headroom
			for _, im := range imgs {
				reqs++ // image/still
				if im.Kind == weibo.MediaKindLivePhoto && im.MotionURL != "" {
					reqs += 2 // possible /media/play redirect + motion clip
				}
				if im.Kind == weibo.MediaKindVideo {
					reqs += 2
				}
			}
			if vid != nil {
				reqs += 3
			}
			return reqs
		}

		if videoTargetID == "" && over9Post == nil && mixedPost == nil {
			rc.Verifications["Step9"] = map[string]any{"status": "not-run", "reason": "no video or multi-image post available"}
			return errSkip
		}

		ck.setPhase("formal_cache")
		defer ck.setPhase("")

		if err := backend.DownloadManager().Start(ctx); err != nil {
			return err
		}

		waitTask := func(taskID string) (download.Task, error) {
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
				case <-time.After(150 * time.Millisecond):
				}
			}
			return download.Task{}, errors.New("formal cache task timed out after 3m")
		}

		step9 := map[string]any{}

		// 9a. Cache 1 video post (or standalone 1034: video) through production CacheService.
		if videoTargetID != "" {
			var (
				task download.Task
				tErr error
			)
			if videoIsOID {
				task, tErr = backend.CacheWeiboVideo(ctx, videoTargetID)
			} else {
				task, tErr = backend.CacheWeiboPost(ctx, videoTargetID, nil)
			}
			if tErr != nil {
				return tErr
			}
			doneTask, wErr := waitTask(task.TaskID)
			if wErr != nil {
				return wErr
			}
			vRec := map[string]any{
				"taskStatus": doneTask.Status,
				"errorKind":  doneTask.ErrorKind,
			}
			if item, ok, gErr := backend.GetLibraryItem(doneTask.ItemKey); gErr == nil && ok {
				vRec["indexed"] = true
				vRec["itemStatus"] = item.Status
				vRec["kind"] = item.Kind
				vRec["mediaCount"] = len(item.Media)
				vRec["missingCount"] = len(item.Missing)
				for _, m := range item.Media {
					if m.Role == "video" {
						if p, pErr := backend.MediaPath(doneTask.ItemKey, m.Path); pErr == nil {
							if sum, iErr := download.InspectMP4FileContext(ctx, p); iErr == nil {
								vRec["mp4Bytes"] = sum.FileBytes
								vRec["codecs"] = sum.Codecs
								vRec["width"] = sum.Width
								vRec["height"] = sum.Height
								vRec["moovBeforeMdat"] = sum.MoovBeforeMdat
								vRec["durationSec"] = float64(int(sum.DurationSec*1000+0.5)) / 1000
							}
						}
					}
				}
			}
			step9["videoPost"] = vRec
			if doneTask.Status != "completed" {
				return fmt.Errorf("formal video cache ended as %s (%s)", doneTask.Status, doneTask.ErrorKind)
			}
		} else {
			step9["videoPostStatus"] = "not-run"
		}

		// 9b. Cache 1 multi-image post fitting within remaining cacheCDN budget.
		remCDN := ck.remainingCacheCDNRequests()
		over9Reqs := 999
		if over9Post != nil && over9Post.post != nil {
			over9Reqs = estimateCDNReqs(over9Post.post)
		}
		mixedReqs := 999
		if mixedPost != nil && mixedPost.post != nil {
			mixedReqs = estimateCDNReqs(mixedPost.post)
		}
		switch {
		case over9Reqs <= remCDN:
			imageCacheMid = over9Post.post.Mid
			imageCacheLabel = "over9"
		case mixedReqs <= remCDN:
			imageCacheMid = mixedPost.post.Mid
			imageCacheLabel = "mixed"
		case mixedReqs < over9Reqs && mixedPost != nil && mixedPost.post != nil:
			imageCacheMid = mixedPost.post.Mid
			imageCacheLabel = "mixed"
		case over9Post != nil && over9Post.post != nil:
			imageCacheMid = over9Post.post.Mid
			imageCacheLabel = "over9"
		case mixedPost != nil && mixedPost.post != nil:
			imageCacheMid = mixedPost.post.Mid
			imageCacheLabel = "mixed"
		}

		if imageCacheMid != "" {
			task, tErr := backend.CacheWeiboPost(ctx, imageCacheMid, nil)
			if tErr != nil {
				return tErr
			}
			doneTask, wErr := waitTask(task.TaskID)
			if wErr != nil {
				return wErr
			}
			iRec := map[string]any{
				"sourceSample": imageCacheLabel,
				"taskStatus":   doneTask.Status,
				"errorKind":    doneTask.ErrorKind,
			}
			if item, ok, gErr := backend.GetLibraryItem(doneTask.ItemKey); gErr == nil && ok {
				iRec["indexed"] = true
				iRec["itemStatus"] = item.Status
				iRec["kind"] = item.Kind
				iRec["missingCount"] = len(item.Missing)
				roles := map[string]int{}
				motionVerified := 0
				for _, m := range item.Media {
					roles[m.Role]++
					if m.Role == "motion" {
						if p, pErr := backend.MediaPath(doneTask.ItemKey, m.Path); pErr == nil {
							if _, iErr := download.InspectMP4FileContext(ctx, p); iErr == nil {
								motionVerified++
							}
						}
					}
				}
				iRec["mediaRoles"] = roles
				if roles["motion"] > 0 {
					iRec["motionClipsVerified"] = motionVerified
				}
			}
			step9["multiImagePost"] = iRec
			if doneTask.Status != "completed" && doneTask.Status != "partial" {
				return fmt.Errorf("formal multi-image cache ended as %s (%s)", doneTask.Status, doneTask.ErrorKind)
			}
		} else {
			step9["multiImagePostStatus"] = "not-run"
		}

		// Verify zero leaked cookies or signed URLs in temp cache manifests, databases, and logs before cleanup.
		tempSecretClean := true
		_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, wErr error) error {
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
			if raw, rErr := os.ReadFile(path); rErr == nil {
				if len(sitecheck.ScanTextForSecrets(string(raw))) > 0 || redactor.containsCookie(raw) {
					tempSecretClean = false
				}
			}
			return nil
		})
		step9["tempCacheSecretScanClean"] = tempSecretClean
		step9["status"] = "verified"

		for k, v := range step9 {
			f[k] = v
		}
		rc.Verifications["Step9"] = step9
		if !tempSecretClean {
			return errors.New("secret or signed URL detected in temporary cache metadata or logs")
		}
		return nil
	})

	closeBackend()
	rc.TempDataDeleted = cleanupWorkDir()
	rc.APIUsage = gate.Usage()
	rc.ShortLinkHops = mediaUsage{int(ck.shortLink.requests.Load()), ck.shortLink.bytes.Load()}
	rc.ProbeCDN = mediaUsage{int(ck.probeCDN.requests.Load()), ck.probeCDN.bytes.Load()}
	rc.CacheCDN = mediaUsage{int(ck.cacheCDN.requests.Load()), ck.cacheCDN.bytes.Load()}
	rc.StoppedBy = ck.stopReason()
	rc.Redirects = ck.redirectShapes()

	rc.Passed = rc.StoppedBy == "" && rc.TempDataDeleted && !ck.cookieOnCDNHit
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
			return rc, fmt.Errorf("%s stopped by %s", act022ID, rc.StoppedBy)
		}
		return rc, fmt.Errorf("%s completed with step failures", act022ID)
	}
	return rc, nil
}
