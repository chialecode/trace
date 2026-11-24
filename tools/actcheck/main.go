// Command actcheck runs an authorized, quota-gated real-site check
// (authorizations ACT-006, ACT-007, ACT-010 to ACT-012, ACT-014 to ACT-020, ACT-022, ACT-024 to ACT-026). Every request passes a persistent
// quota ledger before it is sent; risk control, rate limits, auth expiry or a
// challenge stop the run. The receipt holds categories, counts, sizes and
// quality only: no Cookie, signed URL or account data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/sitecheck"
)

// actQuota mirrors one real-site authorization. Each act has its own
// persistent ledger, so a used-up act can never be rerun under a new name
// without a new entry here and a new authorization (docs/dev-rules/workflow.md#site-requests).
type actQuota struct {
	label   string
	maxRuns int
	maxAPI  int
	api     map[string]int
}

var acts = map[string]actQuota{
	"act006": {label: "ACT-006", maxRuns: 2, maxAPI: 20, api: map[string]int{
		"nav": 2, "dynamic_feed": 1, "space_page": 1, "arc_search": 1, "video_view": 2,
		"playurl": 3, "danmaku": 2, "subtitle_list": 1, "cookie_info": 1,
		"live_status": 1, "room_init": 1, "live_playinfo": 2,
	}},
	"act007": {label: "ACT-007", maxRuns: 1, maxAPI: 20, api: map[string]int{
		"nav": 2, "dynamic_feed": 1, "space_page": 1, "arc_search": 1, "video_view": 2,
		"playurl": 3, "danmaku": 2, "subtitle_list": 1, "cookie_info": 1,
		"live_status": 1, "room_init": 2, "live_playinfo": 2,
	}},
}

// targets live in the ignored .local/site-check/targets-<act>.json; the
// repository never names the authorized accounts.
type targets struct {
	LiveRoom   int64 `json:"liveRoom"`
	DynamicMid int64 `json:"dynamicMid"`
	VideoMid   int64 `json:"videoMid"`
}

type checker struct {
	gate       *sitecheck.Gate
	apiLimits  map[string]int
	vod, live  *mediaBudget
	liveMode   atomic.Bool
	mu         sync.Mutex
	statuses   map[string]int
	stopped    string
	playlists  []map[string]any
	mediaKinds []string
}

func (c *checker) stopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *checker) tripStop(reason string) {
	if reason == "" {
		return
	}
	c.mu.Lock()
	if c.stopped == "" {
		c.stopped = reason
	}
	c.mu.Unlock()
}

// notePlaylist keeps only the playlist's structure: tag names and counts,
// whether entries are relative, and file extensions. No URI or token text.
func (c *checker) notePlaylist(status int, contentType string, raw []byte) {
	summary := parseHLSPlaylistSummary(status, contentType, raw, true)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.playlists) < 3 {
		c.playlists = append(c.playlists, summary)
	}
}

func (c *checker) playlistSummaries() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"firstPlaylists": c.playlists, "mediaSequence": c.mediaKinds}
}

func classify(r *http.Request) (category string, media bool) {
	host, path := strings.ToLower(r.URL.Hostname()), r.URL.Path
	switch {
	case strings.HasSuffix(path, "/x/web-interface/nav"):
		return "nav", false
	case strings.Contains(path, "/x/polymer/web-dynamic/") && strings.HasSuffix(path, "/feed/space"):
		return "dynamic_feed", false
	case strings.HasSuffix(path, "/x/polymer/web-dynamic/v1/detail"):
		return "dynamic_detail", false
	case host == "space.bilibili.com":
		return "space_page", false
	case strings.HasSuffix(path, "/x/space/wbi/arc/search"):
		return "arc_search", false
	case strings.Contains(path, "/x/web-interface/") && strings.HasSuffix(path, "/view"):
		return "video_view", false
	case strings.HasSuffix(path, "/x/player/wbi/playurl"):
		return "playurl", false
	case strings.Contains(path, "/x/v2/dm/"):
		return "danmaku", false
	case strings.HasSuffix(path, "/x/player/wbi/v2"):
		return "subtitle_list", false
	case strings.HasSuffix(path, "/cookie/info"):
		return "cookie_info", false
	case strings.HasSuffix(path, "/get_status_info_by_uids"):
		return "live_status", false
	case strings.HasSuffix(path, "/room_init"):
		return "room_init", false
	case strings.Contains(path, "getRoomPlayInfo"):
		return "live_playinfo", false
	case strings.HasSuffix(host, "bilibili.com"):
		return "other", false
	}
	return "media", true
}

func (c *checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if s := c.stopReason(); s != "" {
			return nil, errors.New("site check stopped: " + s)
		}
		cat, media := classify(r)
		budget := c.vod
		if media {
			if c.liveMode.Load() {
				budget = c.live
			}
			if err := budget.acquire(); err != nil {
				return nil, err
			}
		} else if err := checkAPICategoryAllowed(c.apiLimits, cat); err != nil {
			return nil, err
		} else if err := c.gate.AcquireAPI(cat); err != nil {
			return nil, err
		}
		resp, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.statuses[fmt.Sprintf("%s:%d", map[bool]string{true: "media", false: cat}[media], resp.StatusCode)]++
		c.mu.Unlock()
		if resp.StatusCode == http.StatusPreconditionFailed {
			c.tripStop("http_412")
		}
		if media {
			resp.Body = countingBody{ReadCloser: resp.Body, budget: budget}
			if strings.HasSuffix(strings.ToLower(r.URL.Path), ".m3u8") {
				raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
				c.notePlaylist(resp.StatusCode, resp.Header.Get("Content-Type"), raw)
				resp.Body = io.NopCloser(strings.NewReader(string(raw)))
				if err != nil {
					return nil, err
				}
			} else {
				c.mu.Lock()
				c.mediaKinds = append(c.mediaKinds, fmt.Sprintf("%s:%d", strings.ToLower(filepath.Ext(r.URL.Path)), resp.StatusCode))
				c.mu.Unlock()
			}
		}
		return resp, nil
	})
}

func newBilibiliStepRunner(steps *[]step, ck *checker, scrub func(string) string) *stepRunner {
	return &stepRunner{
		steps:       steps,
		stopReason:  ck.stopReason,
		tripStop:    ck.tripStop,
		classifyErr: bilibili.KindOf,
		scrubErr:    scrub,
		stopOnKinds: map[string]bool{
			bilibili.ErrKindRiskControl:  true,
			bilibili.ErrKindRateLimited:  true,
			bilibili.ErrKindAuthRequired: true,
		},
	}
}

func firstCachedPartFile(item library.ItemMetadata) (string, error) {
	if len(item.Parts) == 0 {
		return "", errors.New("cached item has no parts")
	}
	if strings.TrimSpace(item.Parts[0].File) == "" {
		return "", errors.New("cached item first part has empty file path")
	}
	return item.Parts[0].File, nil
}

func main() {
	root := flag.String("root", ".", "repository root")
	act := flag.String("act", "act007", "authorization id (ledger and limits)")
	out := flag.String("out", "", "receipt path (default artifacts/site-check/<act>.json)")
	recordFor := flag.Duration("record", 20*time.Second, "live recording length (<= 30s; <= 25s for act010)")
	flag.Parse()
	switch *act {
	case "act010":
		runAct010Main(*root, *out, *recordFor)
		return
	case "act011":
		runAct011Main(*root, *out, *recordFor)
		return
	case "act012":
		runAct012Main(*root, *out, *recordFor)
		return
	case "act014":
		runAct014Main(*root, *out, *recordFor)
		return
	case "act015":
		runAct015Main(*root, *out, act015Run1)
		return
	case "act016":
		runAct015Main(*root, *out, act016Run)
		return
	case "act017":
		runAct015Main(*root, *out, act017Run)
		return
	case "act018":
		runAct015Main(*root, *out, act018Run)
		return
	case "act019":
		runAct019Main(*root, *out)
		return
	case "act020":
		runAct020Main(*root, *out)
		return
	case "act022":
		runAct022Main(*root, *out)
		return
	case "act024":
		runAct024Main(*root, *out)
		return
	case "act025":
		runAct025Main(*root, *out)
		return
	case "act026":
		runAct026Main(*root, *out)
		return
	}
	if *recordFor > 30*time.Second {
		fatal("recording longer than the authorized 30s")
	}
	quota, ok := acts[*act]
	if !ok {
		fatal("unknown authorization " + *act)
	}
	actID := *act
	if *out == "" {
		*out = filepath.Join("artifacts", "site-check", actID+".json")
	}
	var tg targets
	if raw, err := os.ReadFile(filepath.Join(*root, ".local", "site-check", "targets-"+actID+".json")); err != nil || json.Unmarshal(raw, &tg) != nil || tg.LiveRoom <= 0 || tg.DynamicMid <= 0 || tg.VideoMid <= 0 {
		fatal("targets: .local/site-check/targets-" + actID + ".json must hold liveRoom, dynamicMid and videoMid")
	}
	liveRoom, dynMid, videoMid := &tg.LiveRoom, &tg.DynamicMid, &tg.VideoMid

	cookie, err := readCookie(*root)
	if err != nil {
		fatal("cookie: " + err.Error())
	}
	gate, err := sitecheck.OpenGate(filepath.Join(*root, ".local", "site-check"), sitecheck.Limits{
		ActID: actID, MaxRuns: quota.maxRuns, MaxAPICalls: quota.maxAPI, MaxByCategory: quota.api,
	})
	if err != nil {
		fatal(err.Error())
	}
	if err := gate.BeginRun(); err != nil {
		fatal(err.Error())
	}
	ck := &checker{
		gate:      gate,
		apiLimits: quota.api,
		vod:       &mediaBudget{name: "vod", maxRequests: 30, maxBytes: 200 << 20},
		live:      &mediaBudget{name: "live", maxRequests: 60, maxBytes: 60 << 20},
		statuses:  map[string]int{},
	}

	work, err := os.MkdirTemp("", "trace-"+actID+"-")
	if err != nil {
		fatal(err.Error())
	}
	defer os.RemoveAll(work) // temporary media is deleted after verification
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel: "dev", ForceMode: "installed",
		LocalAppData: filepath.Join(work, "localappdata"), DocumentsDir: filepath.Join(work, "documents"),
	})
	if err != nil {
		fatal(err.Error())
	}
	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		BilibiliOpts:     bilibili.ClientOptions{WrapTransport: ck.wrap},
	})
	if err != nil {
		fatal(err.Error())
	}
	defer b.Close()

	rc := receipt{
		Act:          quota.label,
		Commit:       resolveReceiptCommit(*root),
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
		StatusCounts: ck.statuses,
		Limits: map[string]any{
			"api": quota.api, "apiTotal": quota.maxAPI, "vodMediaRequests": 30, "vodMediaBytes": 200 << 20,
			"liveMediaRequests": 60, "liveMediaBytes": 60 << 20, "recordSeconds": recordFor.Seconds(),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	runner := newBilibiliStepRunner(&rc.Steps, ck, func(s string) string { return redact(s, cookie) })

	runner.run("login_with_cookie", func(f map[string]any) error {
		st, err := b.ImportCookie(ctx, cookie, "")
		f["loggedIn"], f["source"] = st.LoggedIn, st.Source
		return err
	})

	var candidates []candidate
	runner.run("user_dynamics", func(f map[string]any) error {
		page, err := b.UserDynamicsPage(ctx, *dynMid, "")
		f["items"], f["hasMore"] = len(page.Items), page.HasMore
		types := map[string]int{}
		for _, it := range page.Items {
			types[it.Type]++
			for _, v := range it.Videos {
				if v.BVID != "" && v.DurationSec > 0 {
					candidates = append(candidates, candidate{v.BVID, v.DurationSec, "dynamic"})
				}
			}
		}
		f["types"] = types
		return err
	})
	runner.run("user_videos", func(f map[string]any) error {
		page, err := b.UserVideosPage(ctx, *videoMid, 1)
		f["items"], f["total"] = len(page.Items), page.Total
		for _, it := range page.Items {
			if it.BVID != "" && it.DurationSec > 0 {
				candidates = append(candidates, candidate{it.BVID, it.DurationSec, "videos"})
			}
		}
		return err
	})

	var cachedKey string
	runner.run("cache_shortest_video", func(f map[string]any) error {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].dur < candidates[j].dur })
		if len(candidates) == 0 || candidates[0].dur > 180 {
			return fmt.Errorf("no candidate video of 180s or less")
		}
		c := candidates[0]
		f["durationSec"], f["from"] = c.dur, c.from
		// Start first: startup recovery pauses tasks that are already queued.
		if err := b.DownloadManager().Start(ctx); err != nil {
			return err
		}
		tasks, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: c.bvid, Pages: []int{1}})
		if err != nil {
			return err
		}
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			list, _ := b.ListDownloadTasks()
			for _, t := range list {
				if len(tasks) > 0 && t.TaskID == tasks[0].TaskID {
					f["status"], f["qualityHint"] = t.Status, t.QualityHint
					if t.Status == "completed" || t.Status == "failed" || t.Status == "partial" || strings.HasPrefix(t.Status, "paused") {
						cachedKey = t.ItemKey
						detail, _, _ := b.TaskDetail(t.TaskID)
						files := map[string]any{}
						for _, file := range detail.Files {
							files[file.Role+":"+filepath.Ext(file.Path)] = map[string]any{"status": file.Status, "bytes": file.TotalBytes}
						}
						f["files"], f["attempts"] = files, len(detail.Attempts)
						if t.Status != "completed" {
							return fmt.Errorf("task ended as %s (%s)", t.Status, t.ErrorKind)
						}
						return nil
					}
				}
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("download did not finish within 4 minutes")
	})
	runner.run("verify_cached_item", func(f map[string]any) error {
		if cachedKey == "" {
			return errSkip
		}
		item, ok, err := b.GetLibraryItem(cachedKey)
		if err != nil || !ok {
			return fmt.Errorf("cached item not indexed: %v", err)
		}
		f["status"], f["parts"], f["missing"] = item.Status, len(item.Parts), len(item.Missing)
		for _, p := range item.Parts {
			f["videoQuality"], f["videoCodec"], f["videoWidth"], f["videoHeight"] = p.Video.QualityName, p.Video.Codecs, p.Video.Width, p.Video.Height
			f["audioQuality"], f["audioSavedAs"] = p.Audio.Quality, p.Audio.SavedAs != ""
			f["danmaku"], f["danmakuState"], f["subtitles"], f["subtitleState"] = p.Danmaku != "", p.DanmakuState, len(p.Subtitles), p.SubtitleState
		}
		partFile, err := firstCachedPartFile(item)
		if err != nil {
			return err
		}
		path, err := b.MediaPath(cachedKey, partFile)
		if err != nil {
			return err
		}
		if !library.VerifyMediaFile(item.ItemDir, library.MediaFile{Path: partFile}, false) {
			return fmt.Errorf("cached MP4 failed verification")
		}
		info, err := os.Stat(path)
		if err == nil {
			f["mp4Bytes"] = info.Size()
		}
		return err
	})

	ck.liveMode.Store(true)
	runner.run("live_record", func(f map[string]any) error {
		st, err := b.StartLiveRecording(ctx, *liveRoom)
		f["liveStatus"] = st.LiveStatus
		if err != nil {
			if st.LiveStatus != 1 && st.RoomID != 0 {
				f["reason"] = "not live"
				return errSkip
			}
			return err
		}
		time.Sleep(*recordFor)
		stopped, err := b.StopLiveRecording(st.RoomID)
		f["recordedSec"], f["recordedBytes"], f["segments"] = stopped.RecordDurationSec, stopped.RecordBytes, stopped.RecordSegments
		for _, r := range b.ListLiveRooms() {
			if r.RoomID == st.RoomID {
				f["stopReason"], f["lastError"] = r.StopReason, redact(r.LastError, cookie)
			}
		}
		f["playlists"] = ck.playlistSummaries()
		if err != nil {
			return err
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			items, _ := b.ListLibraryItems(library.FeedFilter{})
			for _, it := range items {
				if it.Kind == "live" && it.Status != "recording" {
					f["itemStatus"] = it.Status
					if it.Live != nil {
						f["codec"], f["quality"] = it.Live.Codec, it.Live.Quality
					}
					var total int64
					for _, m := range it.Media {
						total += m.Bytes
					}
					f["mp4Bytes"], f["mediaFiles"] = total, len(it.Media)
					if it.Status != "complete" {
						return fmt.Errorf("live item ended as %s", it.Status)
					}
					return nil
				}
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("live recording was not finalized within 45s")
	})

	rc.APIUsage = gate.Usage()
	rc.VODMedia = mediaUsage{int(ck.vod.requests.Load()), ck.vod.bytes.Load()}
	rc.LiveMedia = mediaUsage{int(ck.live.requests.Load()), ck.live.bytes.Load()}
	rc.StoppedBy = ck.stopReason()
	rc.Passed = rc.StoppedBy == ""
	for _, s := range rc.Steps {
		if !s.OK {
			rc.Passed = false
		}
	}
	rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeCleanReceipt(*out, rc, func(raw string) error {
		return verifyCookieValuesAbsent(raw, cookie, "SESSDATA", "bili_jct")
	}); err != nil {
		fatal(err.Error())
	}
	fmt.Printf(quota.label+" passed=%v stoppedBy=%q api=%d vodMedia=%d/%dB liveMedia=%d/%dB receipt=%s\n",
		rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, rc.VODMedia.Requests, rc.VODMedia.Bytes, rc.LiveMedia.Requests, rc.LiveMedia.Bytes, *out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-22s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if !rc.Passed {
		os.Exit(1)
	}
}

type candidate struct {
	bvid string
	dur  int
	from string
}
