package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
)

// ACT-015: cache one user-named Douyin video end to end through the production
// backend and download manager (plan, Range download, MP4 finalize, library index)
// after the paid-content false positive fix. Media requests reach the ledger via
// download.Manager.SetMediaTransportWrap.
const (
	act015ID                   = "act015"
	act015Label                = "ACT-015"
	act015MaxAPICalls          = 4
	act015MaxVODRequests       = 40
	act015MaxVODBytes    int64 = 200 << 20
	act015Timeout              = 8 * time.Minute
)

var act015APILimits = map[string]int{"profile_self": 1, "aweme_detail": 3}

// act015Targets lives in the ignored .local/site-check/targets-act015.json.
type act015Targets struct {
	AwemeID  string `json:"awemeId"`
	URL      string `json:"url"`
	MinBytes int64  `json:"minBytes"`
}

func loadAct015Targets(root string) (act015Targets, error) {
	var tg act015Targets
	raw, err := os.ReadFile(filepath.Join(root, ".local", "site-check", "targets-act015.json"))
	if err != nil {
		return tg, errors.New("targets: .local/site-check/targets-act015.json is unreadable")
	}
	if err := json.Unmarshal(raw, &tg); err != nil {
		return tg, errors.New("targets: invalid JSON in targets-act015.json")
	}
	if !reAct010WorkID.MatchString(strings.TrimSpace(tg.AwemeID)) {
		return tg, errors.New("targets: awemeId is invalid")
	}
	return tg, nil
}

// act015Run names one authorization that runs this flow; each has its own ledger.
// ACT-016 repeats it once after the local-proxy dial fix (ACT-015 stopped there).
type act015Run struct{ id, label string }

var (
	act015Run1 = act015Run{id: act015ID, label: act015Label}
	act016Run  = act015Run{id: "act016", label: "ACT-016"}
	// ACT-017: the same flow after the planner tries the untranscoded source first.
	act017Run = act015Run{id: "act017", label: "ACT-017"}
	// ACT-018: ACT-017 after the service planner passes the original URLs on.
	act018Run = act015Run{id: "act018", label: "ACT-018"}
)

func runAct015Main(root, out string, run act015Run) {
	rc, err := runAct015(context.Background(), run, root, out)
	if err != nil && rc.StartedAt == "" {
		fatal(err.Error())
	}
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", run.id+".json")
	}
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d vodMedia=%d/%dB receipt=%s\n",
		run.label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, rc.VODMedia.Requests, rc.VODMedia.Bytes, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-22s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", run.label, err)
	}
	if err != nil || !rc.Passed {
		os.Exit(1)
	}
}

func runAct015(parentCtx context.Context, run act015Run, root, outPath string) (act010Receipt, error) {
	var rc act010Receipt
	if outPath == "" {
		outPath = filepath.Join(root, "artifacts", "site-check", run.id+".json")
	}
	tg, err := loadAct015Targets(root)
	if err != nil {
		return rc, err
	}
	rawCookie, cleaned, err := readAndValidateDouyinCookie(root)
	if err != nil {
		return rc, err
	}
	redactor := newAct010Redactor()
	redactor.addCookieHeader(rawCookie)
	redactor.addCookieMap(cleaned)
	redactor.addIDs(tg.AwemeID)
	redactor.addURLs(tg.URL)

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID: run.id, MaxRuns: 1, MaxAPICalls: act015MaxAPICalls, MaxByCategory: act015APILimits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}
	ck := &act010Checker{
		gate:           gate,
		vod:            &mediaBudget{name: "vod", maxRequests: act015MaxVODRequests, maxBytes: act015MaxVODBytes},
		live:           &mediaBudget{name: "live"},
		minSpacing:     act010DefaultSpacing,
		statuses:       make(map[string]int),
		apiStatusCodes: make(map[string]map[int]int),
	}

	workDir, err := os.MkdirTemp("", "trace-"+run.id+"-")
	if err != nil {
		return rc, err
	}
	cleanup := func() bool {
		_ = os.RemoveAll(workDir)
		_, statErr := os.Stat(workDir)
		return os.IsNotExist(statErr)
	}
	defer cleanup()

	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel: "dev", ForceMode: "installed",
		LocalAppData: filepath.Join(workDir, "localappdata"), DocumentsDir: filepath.Join(workDir, "documents"),
	})
	if err != nil {
		return rc, err
	}
	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		DouyinOpts:       douyin.ClientOptions{WrapTransport: ck.wrap},
	})
	if err != nil {
		return rc, err
	}
	backendClosed := false
	closeBackend := func() {
		if !backendClosed {
			backendClosed = true
			_ = b.Close()
		}
	}
	defer closeBackend()
	b.DownloadManager().SetMediaTransportWrap(ck.wrap)

	rc = act010Receipt{
		Act:            run.label,
		Commit:         sourceCommit(root),
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		Verifications:  make(map[string]any),
		APIStatusCodes: ck.apiStatusCodes,
		StatusCounts:   ck.statuses,
		Limits: map[string]any{
			"api": act015APILimits, "apiTotal": act015MaxAPICalls,
			"vodMediaRequests": act015MaxVODRequests, "vodMediaBytes": act015MaxVODBytes,
			"liveMediaRequests": 0, "minSpacingMs": act010DefaultSpacing.Milliseconds(),
			"minExpectedBytes": tg.MinBytes,
		},
	}
	ctx, cancel := context.WithTimeout(parentCtx, act015Timeout)
	defer cancel()

	runStep := func(name string, fn func(f map[string]any) error) bool {
		if stopped := ck.stopReason(); stopped != "" {
			rc.Steps = append(rc.Steps, step{Name: name, Skipped: true, Error: "stopped earlier: " + stopped})
			return false
		}
		f := map[string]any{}
		start := time.Now()
		err := fn(f)
		st := step{Name: name, OK: err == nil, Facts: f, Duration: time.Since(start).Milliseconds()}
		if err != nil {
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

	runStep("login_with_cookie", func(f map[string]any) error {
		st, err := b.LoginWithCookie(ctx, "douyin", rawCookie, "")
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID)
		f["loggedIn"] = st.LoggedIn
		return err
	})

	var itemKey string
	runStep("cache_video", func(f map[string]any) error {
		if err := b.DownloadManager().Start(ctx); err != nil {
			return err
		}
		ck.vodMediaActive.Store(true)
		defer ck.vodMediaActive.Store(false)
		task, err := b.CacheDouyinWork(ctx, tg.AwemeID)
		if err != nil {
			return err
		}
		for {
			list, _ := b.ListDownloadTasks()
			for _, t := range list {
				if t.TaskID != task.TaskID {
					continue
				}
				f["status"], f["qualityHint"] = t.Status, t.QualityHint
				if t.Status == "completed" || t.Status == "failed" || t.Status == "partial" || strings.HasPrefix(t.Status, "paused") {
					itemKey = t.ItemKey
					if detail, ok, _ := b.TaskDetail(t.TaskID); ok {
						f["attempts"] = len(detail.Attempts)
					}
					if t.Status != "completed" {
						if t.ErrorMessage != "" {
							f["taskError"] = redactor.scrubError(t.ErrorMessage)
						}
						return fmt.Errorf("task ended as %s (%s)", t.Status, t.ErrorKind)
					}
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("download did not finish within %s", act015Timeout)
			case <-time.After(time.Second):
			}
		}
	})

	runStep("verify_cached_item", func(f map[string]any) error {
		if itemKey == "" {
			return errors.New("no cached item")
		}
		item, ok, err := b.GetLibraryItem(itemKey)
		if err != nil || !ok {
			return fmt.Errorf("cached item not indexed: %v", err)
		}
		f["status"], f["missing"], f["durationSec"] = item.Status, len(item.Missing), item.Duration
		if dv := item.DouyinVideo; dv != nil {
			f["gear"], f["qualityName"], f["codec"] = dv.Gear, dv.QualityName, dv.Codec
			f["width"], f["height"], f["fps"], f["bitrate"] = dv.Width, dv.Height, dv.FPS, dv.Bitrate
			f["availableGears"] = dv.AvailableGears
		}
		var video *library.MediaFile
		for i := range item.Media {
			if item.Media[i].Role == "video" {
				video = &item.Media[i]
			}
		}
		if video == nil {
			return errors.New("no video file in cached item")
		}
		path, err := b.MediaPath(itemKey, video.Path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		f["mp4Bytes"] = info.Size()
		// Read-only inspection: the check must not repair what production wrote.
		summary, err := download.InspectMP4FileContext(ctx, path)
		if err != nil {
			return fmt.Errorf("cached MP4 failed inspection: %w", err)
		}
		summary.SHA256 = ""
		f["mp4Summary"] = summary
		if !summary.MoovBeforeMdat {
			return errors.New("cached MP4 has moov after mdat")
		}
		if !library.VerifyMediaFile(item.ItemDir, *video, false) {
			return errors.New("cached MP4 failed library verification")
		}
		if item.Status != "complete" {
			return fmt.Errorf("item status %s", item.Status)
		}
		if tg.MinBytes > 0 && info.Size() < tg.MinBytes {
			return fmt.Errorf("video is %d bytes, expected at least %d", info.Size(), tg.MinBytes)
		}
		rc.Verifications["cacheVideo"] = f
		return nil
	})

	closeBackend()
	rc.TempMediaDeleted = cleanup()
	rc.APIUsage = gate.Usage()
	rc.VODMedia = mediaUsage{Requests: int(ck.vod.requests.Load()), Bytes: ck.vod.bytes.Load()}
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
		rc.SecretScanClean, rc.Passed = false, false
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
			return rc, fmt.Errorf("%s stopped by %s", run.id, rc.StoppedBy)
		}
		return rc, fmt.Errorf("%s completed with step failures", run.id)
	}
	return rc, nil
}
