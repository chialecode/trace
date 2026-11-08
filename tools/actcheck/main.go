// Command actcheck runs an authorized, quota-gated real-site check of the M1
// implementation (USER-ACTIONS ACT-006, rerun as ACT-007). Every request passes a persistent
// quota ledger before it is sent; risk control, rate limits, -101 or a
// challenge stop the run. The receipt holds categories, counts, sizes and
// quality only: no Cookie, signed URL or account data.
package main

import (
	"bufio"
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

// actQuota mirrors one USER-ACTIONS authorization. Each act has its own
// persistent ledger, so a used-up act can never be rerun under a new name
// without a new entry here and in USER-ACTIONS.
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

var (
	actID     string
	apiLimits map[string]int
)

type step struct {
	Name     string         `json:"name"`
	OK       bool           `json:"ok"`
	Skipped  bool           `json:"skipped,omitempty"`
	ErrKind  string         `json:"errorKind,omitempty"`
	Error    string         `json:"error,omitempty"`
	Facts    map[string]any `json:"facts,omitempty"`
	Duration int64          `json:"durationMs"`
}

type receipt struct {
	Act          string          `json:"act"`
	Commit       string          `json:"commit,omitempty"`
	StartedAt    string          `json:"startedAt"`
	FinishedAt   string          `json:"finishedAt"`
	Passed       bool            `json:"passed"`
	StoppedBy    string          `json:"stoppedBy,omitempty"`
	Steps        []step          `json:"steps"`
	APIUsage     sitecheck.Usage `json:"apiUsage"`
	VODMedia     mediaUsage      `json:"vodMedia"`
	LiveMedia    mediaUsage      `json:"liveMedia"`
	StatusCounts map[string]int  `json:"httpStatusCounts"`
	Limits       map[string]any  `json:"limits"`
	Notes        []string        `json:"notes,omitempty"`
}

type mediaUsage struct {
	Requests int   `json:"requests"`
	Bytes    int64 `json:"bytes"`
}

type mediaBudget struct {
	name        string
	maxRequests int
	maxBytes    int64
	requests    atomic.Int64
	bytes       atomic.Int64
}

func (b *mediaBudget) acquire() error {
	if n := b.requests.Add(1); int(n) > b.maxRequests {
		b.requests.Add(-1)
		return fmt.Errorf("%w: %s requests %d", sitecheck.ErrQuotaExceeded, b.name, b.maxRequests)
	}
	return nil
}

type countingBody struct {
	io.ReadCloser
	budget *mediaBudget
}

func (c countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if total := c.budget.bytes.Add(int64(n)); total > c.budget.maxBytes {
		return n, fmt.Errorf("%w: %s bytes over %d", sitecheck.ErrQuotaExceeded, c.budget.name, c.budget.maxBytes)
	}
	return n, err
}

type checker struct {
	gate       *sitecheck.Gate
	vod, live  *mediaBudget
	liveMode   atomic.Bool
	mu         sync.Mutex
	statuses   map[string]int
	stopped    atomic.Value // string
	playlists  []map[string]any
	mediaKinds []string
}

// notePlaylist keeps only the playlist's structure: tag names and counts,
// whether entries are relative, and file extensions. No URI or token text.
func (c *checker) notePlaylist(status int, contentType string, raw []byte) {
	tags := map[string]int{}
	entries, relative := 0, 0
	exts := map[string]int{}
	mapBeforeFirstEntry := false
	firstLine := ""
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i == 0 {
			firstLine = line
			if len(firstLine) > 7 {
				firstLine = firstLine[:7]
			}
		}
		if strings.HasPrefix(line, "#") {
			name, _, _ := strings.Cut(line, ":")
			tags[name]++
			if name == "#EXT-X-MAP" && entries == 0 {
				mapBeforeFirstEntry = true
			}
			continue
		}
		entries++
		if !strings.HasPrefix(line, "http") {
			relative++
		}
		path, _, _ := strings.Cut(line, "?")
		exts[strings.ToLower(filepath.Ext(path))]++
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.playlists) < 3 {
		c.playlists = append(c.playlists, map[string]any{"status": status, "contentType": contentType, "bytes": len(raw),
			"startsWith": firstLine, "tags": tags, "entries": entries, "relativeEntries": relative, "entryExt": exts,
			"mapBeforeFirstEntry": mapBeforeFirstEntry})
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
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if s, _ := c.stopped.Load().(string); s != "" {
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
		} else if cat == "other" {
			// The gate treats a zero limit as unlimited; refuse unlisted endpoints here.
			return nil, fmt.Errorf("%w: endpoint outside the authorized list", sitecheck.ErrQuotaExceeded)
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
			c.stopped.Store("http_412")
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

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func main() {
	root := flag.String("root", ".", "repository root")
	act := flag.String("act", "act007", "authorization id (ledger and limits)")
	out := flag.String("out", "", "receipt path (default artifacts/site-check/<act>.json)")
	recordFor := flag.Duration("record", 20*time.Second, "live recording length (<= 30s)")
	flag.Parse()
	if *recordFor > 30*time.Second {
		fatal("recording longer than the authorized 30s")
	}
	quota, ok := acts[*act]
	if !ok {
		fatal("unknown authorization " + *act)
	}
	actID, apiLimits = *act, quota.api
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
		ActID: actID, MaxRuns: quota.maxRuns, MaxAPICalls: quota.maxAPI, MaxByCategory: apiLimits,
	})
	if err != nil {
		fatal(err.Error())
	}
	if err := gate.BeginRun(); err != nil {
		fatal(err.Error())
	}
	ck := &checker{
		gate:     gate,
		vod:      &mediaBudget{name: "vod", maxRequests: 30, maxBytes: 200 << 20},
		live:     &mediaBudget{name: "live", maxRequests: 60, maxBytes: 60 << 20},
		statuses: map[string]int{},
	}
	ck.stopped.Store("")

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

	rc := receipt{Act: quota.label, StartedAt: time.Now().UTC().Format(time.RFC3339), StatusCounts: ck.statuses,
		Limits: map[string]any{"api": apiLimits, "apiTotal": 20, "vodMediaRequests": 30, "vodMediaBytes": 200 << 20,
			"liveMediaRequests": 60, "liveMediaBytes": 60 << 20, "recordSeconds": recordFor.Seconds()}}
	if raw, err := os.ReadFile(filepath.Join(*root, "dist", "trace-dev", "VERSION.txt")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "commit="); ok {
				rc.Commit = v
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	run := func(name string, fn func(facts map[string]any) error) bool {
		if s, _ := ck.stopped.Load().(string); s != "" {
			rc.Steps = append(rc.Steps, step{Name: name, Skipped: true, Error: "stopped earlier: " + s})
			return false
		}
		facts := map[string]any{}
		start := time.Now()
		err := fn(facts)
		st := step{Name: name, OK: err == nil, Facts: facts, Duration: time.Since(start).Milliseconds()}
		if errors.Is(err, errSkip) {
			st.OK, st.Skipped = true, true
		} else if err != nil {
			st.ErrKind = bilibili.KindOf(err)
			st.Error = redact(err.Error(), cookie)
			switch st.ErrKind {
			case bilibili.ErrKindRiskControl, bilibili.ErrKindRateLimited, bilibili.ErrKindAuthRequired:
				ck.stopped.Store(st.ErrKind)
			}
			if errors.Is(err, sitecheck.ErrQuotaExceeded) {
				st.ErrKind = "quota"
			}
		}
		if len(st.Facts) == 0 {
			st.Facts = nil
		}
		rc.Steps = append(rc.Steps, st)
		return err == nil
	}

	run("login_with_cookie", func(f map[string]any) error {
		st, err := b.ImportCookie(ctx, cookie, "")
		f["loggedIn"], f["source"] = st.LoggedIn, st.Source
		return err
	})

	var candidates []candidate
	run("user_dynamics", func(f map[string]any) error {
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
	run("user_videos", func(f map[string]any) error {
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
	run("cache_shortest_video", func(f map[string]any) error {
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
	run("verify_cached_item", func(f map[string]any) error {
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
		path, err := b.MediaPath(cachedKey, item.Parts[0].File)
		if err != nil {
			return err
		}
		if !library.VerifyMediaFile(item.ItemDir, library.MediaFile{Path: item.Parts[0].File}, false) {
			return fmt.Errorf("cached MP4 failed verification")
		}
		info, err := os.Stat(path)
		if err == nil {
			f["mp4Bytes"] = info.Size()
		}
		return err
	})

	ck.liveMode.Store(true)
	run("live_record", func(f map[string]any) error {
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
	rc.StoppedBy, _ = ck.stopped.Load().(string)
	rc.Passed = rc.StoppedBy == ""
	for _, s := range rc.Steps {
		if !s.OK {
			rc.Passed = false
		}
	}
	rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.MarshalIndent(rc, "", "  ")
	if strings.Contains(string(raw), cookieValue(cookie, "SESSDATA")) || strings.Contains(string(raw), cookieValue(cookie, "bili_jct")) {
		fatal("receipt would contain a secret; not written")
	}
	_ = os.MkdirAll(filepath.Dir(*out), 0o755)
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
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

var errSkip = errors.New("skipped")

func readCookie(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, ".env.local"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	var path string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "TRACE_TEST_BILIBILI_COOKIE_FILE="); ok {
			path = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	if path == "" {
		return "", errors.New("TRACE_TEST_BILIBILI_COOKIE_FILE is not set")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cookie file unreadable")
	}
	return strings.TrimSpace(string(raw)), nil
}

func cookieValue(cookie, key string) string {
	for _, part := range strings.Split(cookie, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k == key && len(v) >= 4 {
			return v
		}
	}
	return "\x00never\x00"
}

func redact(s, cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		if _, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	if i := strings.Index(s, "http"); i >= 0 {
		s = s[:i] + "[URL]"
	}
	return s
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "actcheck:", msg)
	os.Exit(2)
}
