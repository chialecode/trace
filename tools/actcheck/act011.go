package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
)

const (
	act011MaxHLSBytes = 2 << 20
)

// liveFollowup describes one bounded Douyin live follow-up authorization. Each has
// its own persistent ledger (.local/site-check/usage-<id>.json); none touches
// usage-act010.json or another follow-up's ledger.
type liveFollowup struct {
	id           string
	label        string
	maxAPICalls  int
	apiLimits    map[string]int
	maxHLSReqs   int
	maxLiveReqs  int  // FLV requests, each redirect hop included
	offlineProbe bool // V-06 reflow/info with the room_id=2 placeholder
}

// ACT-011: the two sub-items ACT-010 did not reach (V-06 offline room_id=2 and
// V-07 HLS playlist + HTTP-FLV <= 25 s). Executed once on 2026-11-13.
var act011Followup = liveFollowup{
	id:           "act011",
	label:        "ACT-011",
	maxAPICalls:  4,
	apiLimits:    map[string]int{"profile_self": 1, "reflow_info": 1, "web_enter": 2},
	maxHLSReqs:   1,
	maxLiveReqs:  act010MaxLiveConns,
	offlineProbe: true,
}

// ACT-012: the V-07 HTTP-FLV <= 25 s recording that ACT-011 stopped before, after
// the checker learned that a CDN 302 with a small HTML body is a redirect, not a
// verification page. Two FLV connections, each allowed one redirect hop.
var act012Followup = liveFollowup{
	id:          "act012",
	label:       "ACT-012",
	maxAPICalls: 3,
	apiLimits:   map[string]int{"profile_self": 1, "web_enter": 2},
	maxHLSReqs:  0,
	maxLiveReqs: 2 * act010MaxLiveConns,
}

// ACT-014: ACT-012 reached the CDN and recorded that both FLV 302 hops go to an
// HTTPS 443 domain outside the live allow-list, which the recorder refused (F-004).
// After the redirect-hop policy fix this repeats the same bounded recording once.
var act014Followup = liveFollowup{
	id:          "act014",
	label:       "ACT-014",
	maxAPICalls: act012Followup.maxAPICalls,
	apiLimits:   map[string]int{"profile_self": 1, "web_enter": 2},
	maxHLSReqs:  0,
	maxLiveReqs: act012Followup.maxLiveReqs,
}

// Kept for the ACT-011 tests.
var (
	act011MaxAPICalls = act011Followup.maxAPICalls
	act011APILimits   = act011Followup.apiLimits
)

func runAct011Main(root, out string, recordFor time.Duration) {
	runLiveFollowupMain(act011Followup, root, out, recordFor)
}

func runAct012Main(root, out string, recordFor time.Duration) {
	runLiveFollowupMain(act012Followup, root, out, recordFor)
}

func runAct014Main(root, out string, recordFor time.Duration) {
	runLiveFollowupMain(act014Followup, root, out, recordFor)
}

func runLiveFollowupMain(fu liveFollowup, root, out string, recordFor time.Duration) {
	rc, err := runLiveFollowup(context.Background(), fu, act010Config{
		RootDir:        root,
		OutPath:        out,
		RecordDuration: recordFor,
	})
	if err != nil && rc.StartedAt == "" {
		fatal(err.Error())
	}
	outDisp := out
	if outDisp == "" {
		outDisp = filepath.Join(root, "artifacts", "site-check", fu.id+".json")
	}
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d liveMedia=%d/%dB receipt=%s\n",
		fu.label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls,
		rc.LiveMedia.Requests, rc.LiveMedia.Bytes, outDisp)
	for _, s := range rc.Steps {
		fmt.Printf("  %-26s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", fu.label, err)
	}
	if err != nil || !rc.Passed {
		os.Exit(1)
	}
}

// runAct011 executes the ACT-011 follow-up (V-06 offline room_id=2 and V-07 live).
func runAct011(parentCtx context.Context, cfg act010Config) (act010Receipt, error) {
	return runLiveFollowup(parentCtx, act011Followup, cfg)
}

// runLiveFollowup executes one bounded Douyin live follow-up. FLV redirects follow
// the production recorder's policy (HTTPS, port 443, Douyin live CDN allow-list)
// and their shape is recorded without the signed target.
func runLiveFollowup(parentCtx context.Context, fu liveFollowup, cfg act010Config) (act010Receipt, error) {
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
		return rc, fmt.Errorf("recording duration %s exceeds authorized %s limit for %s", recordFor, act010MaxRecord, fu.label)
	}
	outPath := cfg.OutPath
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", fu.id+".json")
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
		ActID:         fu.id,
		MaxRuns:       1,
		MaxAPICalls:   fu.maxAPICalls,
		MaxByCategory: fu.apiLimits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}

	ck := &act010Checker{
		gate:           gate,
		vod:            &mediaBudget{name: "vod", maxRequests: 0, maxBytes: 0},
		live:           &mediaBudget{name: "live", maxRequests: fu.maxLiveReqs, maxBytes: act010MaxLiveBytes},
		hls:            &mediaBudget{name: "hls_playlist", maxRequests: fu.maxHLSReqs, maxBytes: act011MaxHLSBytes},
		minSpacing:     spacing,
		allowLocalhost: cfg.AllowLocalhost,
		statuses:       make(map[string]int),
		apiStatusCodes: make(map[string]map[int]int),
	}

	workDir, err := os.MkdirTemp("", "trace-"+fu.id+"-")
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
		MinInterval:    -1,
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
	liveSpec := dyClient.LiveTransport()
	var redirectBlocked string
	liveHTTP := &http.Client{
		Transport: wrappedTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Cookie")
			if len(via) >= 3 {
				return errors.New("live redirect limit exceeded")
			}
			// Same policy as the production recorder: a hop it would refuse is
			// refused here too and reported as the finding.
			if err := liveSpec.ValidateRedirectURL(req.URL.String()); err != nil {
				redirectBlocked = site.KindOf(err)
				return fmt.Errorf("production redirect policy refused the FLV hop (%s)", site.KindOf(err))
			}
			liveSpec.ApplyHeaders(req)
			return nil
		},
	}

	rc = act010Receipt{
		Act:            fu.label,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		Verifications:  make(map[string]any),
		APIStatusCodes: ck.apiStatusCodes,
		StatusCounts:   ck.statuses,
		Limits: map[string]any{
			"api":               fu.apiLimits,
			"apiTotal":          fu.maxAPICalls,
			"hlsPlaylistMax":    fu.maxHLSReqs,
			"shortLinkHops":     0,
			"vodMediaRequests":  0,
			"vodMediaBytes":     0,
			"liveMediaRequests": fu.maxLiveReqs,
			"liveMediaBytes":    act010MaxLiveBytes,
			"recordSeconds":     recordFor.Seconds(),
			"minSpacingMs":      spacing.Milliseconds(),
		},
	}
	// The check runs the working tree through `go run`, so the receipt names the
	// source it ran (HEAD plus -dirty), not the last packaged build.
	rc.Commit = sourceCommit(root)

	ctx, cancel := context.WithTimeout(parentCtx, 3*time.Minute)
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

	// Step 1: Verify Cookie session via profile/self (<= 1 call).
	runStep("v01_auth_session", func(f map[string]any) error {
		st, loginErr := dyClient.LoginWithCookie(ctx, rawCookie)
		ck.mu.Lock()
		authCode := ck.lastAuthCode
		ck.mu.Unlock()
		f["authStatusCode"] = authCode
		if loginErr != nil {
			return loginErr
		}
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID)
		redactor.addCookieValuesFromStore(credStore)
		f["loggedIn"] = st.LoggedIn
		return nil
	})

	// Step 2: V-06 offline reflow/info probe with room_id=2 (<= 1 call; ACT-011 only).
	if fu.offlineProbe {
		runStep("v06_offline_reflow_room_id_2", func(f map[string]any) error {
			targetSec := tg.Users[0].SecUID
			offDetail, offErr := dyClient.ReflowRoomInfo(ctx, "2", targetSec)
			v06 := map[string]any{}
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
				v06["roomStatus"] = offDetail.RoomStatus
			}
			for k, v := range v06 {
				f[k] = v
			}
			rc.Verifications["V-06"] = v06
			return nil
		})
	}

	// Step 3: V-07 live room enter (<= 2 calls: initial + 1 reconnect spare), HLS .m3u8 (<= 1), and FLV <=25s remux.
	runStep("v07_live_enter_and_record", func(f map[string]any) error {
		primaryWebRID := tg.LiveRooms[0].WebRID
		activeRoom, err := dyClient.EnterLiveRoom(ctx, primaryWebRID, "")
		if err != nil {
			return err
		}
		redactor.addIDs(activeRoom.OwnerSecUID, activeRoom.OwnerUID, activeRoom.OwnerShortID, activeRoom.QueriedWebRID, activeRoom.ResponseWebRID, activeRoom.RoomID)
		f["roomStatus"] = activeRoom.RoomStatus
		f["audioOnly"] = activeRoom.AudioOnly

		if activeRoom.RoomStatus != 2 {
			f["status"] = "not-run"
			f["reason"] = "live room offline"
			rc.Verifications["V-07"] = map[string]any{
				"status": "not-run",
				"reason": "live room offline",
			}
			return errSkip
		}
		if activeRoom.AudioOnly {
			f["status"] = "not-run"
			f["reason"] = "live room in audio-only mode"
			rc.Verifications["V-07"] = map[string]any{
				"status": "not-run",
				"reason": "live room in audio-only mode",
			}
			return errSkip
		}

		streamCaps := inspectRoomStreamCapabilities(activeRoom, cfg.AllowLocalhost)
		f["streamCapabilities"] = streamCaps

		var hlsSummary map[string]any
		if hlsURL, _ := streamCaps["sampleHlsUrl"].(string); hlsURL != "" && fu.maxHLSReqs > 0 {
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

		recSummary, recErr := recordShortFLV(ctx, liveHTTP, plan, workDir, recordFor, ck)
		if recErr != nil && ck.stopReason() == "" && !errors.Is(recErr, sitecheck.ErrQuotaExceeded) &&
			int(ck.live.requests.Load()) < act010MaxLiveConns && gate.Usage().Categories["web_enter"] < fu.apiLimits["web_enter"] {
			// Use the single authorized web_enter reconnect spare to refresh FLV stream URLs once.
			ck.liveStreamActive.Store(false)
			refreshed, reErr := dyClient.EnterLiveRoom(ctx, primaryWebRID, activeRoom.OwnerSecUID)
			ck.liveStreamActive.Store(true)
			if reErr == nil && refreshed.RoomStatus == 2 && !refreshed.AudioOnly {
				redactor.addIDs(refreshed.OwnerSecUID, refreshed.OwnerUID, refreshed.OwnerShortID, refreshed.ResponseWebRID, refreshed.RoomID)
				if refreshedPlan, pErr := douyin.SelectFLVStreamFromRoom(refreshed, nil, liveSpec, cfg.AllowLocalhost); pErr == nil {
					recSummary, recErr = recordShortFLV(ctx, liveHTTP, refreshedPlan, workDir, recordFor, ck)
				}
			}
		}
		if redirectBlocked != "" {
			f["productionRedirectBlocked"] = redirectBlocked
		}
		if recErr != nil {
			return recErr
		}

		if hlsSummary != nil {
			recSummary["hlsPlaylist"] = hlsSummary
		}
		recSummary["hlsPlaylistRequests"] = int(ck.hls.requests.Load())
		recSummary["originHasFLV"] = streamCaps["originHasFLV"]
		recSummary["originHasHLS"] = streamCaps["originHasHLS"]
		recSummary["status"] = "completed"
		for k, v := range recSummary {
			f[k] = v
		}
		rc.Verifications["V-07"] = recSummary
		return nil
	})

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
			return rc, fmt.Errorf("%s stopped by %s", fu.id, rc.StoppedBy)
		}
		return rc, fmt.Errorf("%s completed with step failures", fu.id)
	}
	return rc, nil
}

// sourceCommit returns the short HEAD commit of root, with -dirty when tracked
// files differ from it; empty when git is unavailable.
func sourceCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return ""
	}
	commit := strings.TrimSpace(string(out))
	if status, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=no").Output(); err == nil && len(strings.TrimSpace(string(status))) > 0 {
		commit += "-dirty"
	}
	return commit
}
