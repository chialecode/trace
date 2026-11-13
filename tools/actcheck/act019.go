package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/sitecheck"
)

// ACT-019: cache one user-named Bilibili live-photo post (实况) through the
// production backend and download manager, and answer where the motion clip
// gets lost: parsing (live_url), caching (motion file), the clip's container
// (moov first, not fragmented) or the viewer. Only counts, hosts' classes,
// box names and codecs reach the receipt.
const (
	act019ID                = "act019"
	act019Label             = "ACT-019"
	act019MaxAPI            = 4
	act019MaxCDN            = 10
	act019MaxCDNBytes int64 = 50 << 20
)

var act019APILimits = map[string]int{"nav": 2, "dynamic_detail": 2}

var reOpusID = regexp.MustCompile(`^[0-9]{6,24}$`)

type act019Targets struct {
	OpusID string `json:"opusId"`
	URL    string `json:"url"`
}

// detailSniff keeps what the raw detail response said about live photos.
type detailSniff struct {
	mu        sync.Mutex
	responses int
	majorType []string
	pics      int
	livePics  int
	picKeys   map[string]int
	liveExts  map[string]int
	liveHosts map[string]int
}

func (d *detailSniff) note(raw []byte) {
	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.responses++
	if d.responses > 1 {
		return // the worker's re-fetch says the same; count the first only
	}
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch t := v.(type) {
		case map[string]any:
			if mt, ok := t["type"].(string); ok && strings.HasPrefix(mt, "MAJOR_TYPE_") {
				d.majorType = append(d.majorType, mt)
			}
			for k, child := range t {
				walk(child, k)
			}
		case []any:
			if key == "pics" || key == "items" {
				for _, el := range t {
					m, ok := el.(map[string]any)
					if !ok {
						continue
					}
					if _, hasURL := m["url"]; !hasURL {
						if _, hasSrc := m["src"]; !hasSrc {
							continue
						}
					}
					d.pics++
					for k := range m {
						d.picKeys[k]++
					}
					if lu, _ := m["live_url"].(string); lu != "" {
						d.livePics++
						if u, err := url.Parse(lu); err == nil {
							d.liveExts[strings.ToLower(path.Ext(u.Path))]++
							d.liveHosts[hostClass(u.Hostname())]++
						}
					}
				}
			}
			for _, el := range t {
				walk(el, "")
			}
		}
	}
	walk(doc, "")
}

// hostClass names a CDN host without its node: "*.hdslb.com", "*.bilivideo.com".
func hostClass(host string) string {
	parts := strings.Split(strings.ToLower(host), ".")
	if len(parts) <= 2 {
		return host
	}
	return "*." + strings.Join(parts[len(parts)-2:], ".")
}

func runAct019Main(root, out string) {
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", act019ID+".json")
	}
	var tg act019Targets
	raw, err := os.ReadFile(filepath.Join(root, ".local", "site-check", "targets-bilibili-live-photo.json"))
	if err != nil || json.Unmarshal(raw, &tg) != nil || !reOpusID.MatchString(strings.TrimSpace(tg.OpusID)) {
		fatal("targets: .local/site-check/targets-bilibili-live-photo.json must hold opusId")
	}
	cookie, err := readCookie(root)
	if err != nil {
		fatal("cookie: " + err.Error())
	}
	actID, apiLimits = act019ID, act019APILimits
	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID: act019ID, MaxRuns: 1, MaxAPICalls: act019MaxAPI, MaxByCategory: act019APILimits,
	})
	if err != nil {
		fatal(err.Error())
	}
	if err := gate.BeginRun(); err != nil {
		fatal(err.Error())
	}
	ck := &checker{
		gate:     gate,
		vod:      &mediaBudget{name: "cdn", maxRequests: act019MaxCDN, maxBytes: act019MaxCDNBytes},
		live:     &mediaBudget{name: "live", maxRequests: 0, maxBytes: 0},
		statuses: map[string]int{},
	}
	ck.stopped.Store("")
	sniff := &detailSniff{picKeys: map[string]int{}, liveExts: map[string]int{}, liveHosts: map[string]int{}}
	var ctMu sync.Mutex
	contentTypes := map[string]int{}
	wrap := func(base http.RoundTripper) http.RoundTripper {
		inner := ck.wrap(base)
		return roundTrip(func(r *http.Request) (*http.Response, error) {
			resp, err := inner.RoundTrip(r)
			if err != nil || resp == nil {
				return resp, err
			}
			cat, media := classify(r)
			if cat == "dynamic_detail" {
				body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
				_ = resp.Body.Close()
				sniff.note(body)
				resp.Body = io.NopCloser(bytes.NewReader(body))
				if rerr != nil {
					return nil, rerr
				}
			} else if media {
				ct := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
				ctMu.Lock()
				contentTypes[fmt.Sprintf("%s %s:%d", strings.ToLower(path.Ext(r.URL.Path)), ct, resp.StatusCode)]++
				ctMu.Unlock()
			}
			return resp, nil
		})
	}

	work, err := os.MkdirTemp("", "trace-"+act019ID+"-")
	if err != nil {
		fatal(err.Error())
	}
	defer os.RemoveAll(work)
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
		BilibiliOpts:     bilibili.ClientOptions{WrapTransport: wrap},
	})
	if err != nil {
		fatal(err.Error())
	}
	defer b.Close()

	rc := receipt{Act: act019Label, StartedAt: time.Now().UTC().Format(time.RFC3339), StatusCounts: ck.statuses,
		Limits: map[string]any{"api": act019APILimits, "apiTotal": act019MaxAPI, "cdnRequests": act019MaxCDN, "cdnBytes": act019MaxCDNBytes}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	run := func(name string, fn func(f map[string]any) error) bool {
		if s, _ := ck.stopped.Load().(string); s != "" {
			rc.Steps = append(rc.Steps, step{Name: name, Skipped: true, Error: "stopped earlier: " + s})
			return false
		}
		f := map[string]any{}
		start := time.Now()
		err := fn(f)
		st := step{Name: name, OK: err == nil, Facts: f, Duration: time.Since(start).Milliseconds()}
		if errors.Is(err, errSkip) {
			st.OK, st.Skipped = true, true
		} else if err != nil {
			st.ErrKind = bilibili.KindOf(err)
			st.Error = redact(redactID(err.Error(), tg.OpusID), cookie)
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
		f["loggedIn"] = st.LoggedIn
		return err
	})

	var itemKey string
	run("cache_live_photo_post", func(f map[string]any) error {
		if err := b.DownloadManager().Start(ctx); err != nil {
			return err
		}
		task, err := b.EnqueueDynamicCache(ctx, download.DynamicEnqueueRequest{DynamicID: strings.TrimSpace(tg.OpusID)})
		if err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			list, _ := b.ListDownloadTasks()
			for _, t := range list {
				if t.TaskID != task.TaskID {
					continue
				}
				if t.Status == "completed" || t.Status == "failed" || t.Status == "partial" || strings.HasPrefix(t.Status, "paused") {
					f["status"] = t.Status
					itemKey = t.ItemKey
					detail, _, _ := b.TaskDetail(t.TaskID)
					files := map[string]int{}
					for _, file := range detail.Files {
						files[file.Role+":"+filepath.Ext(file.Path)+":"+file.Status]++
					}
					f["files"] = files
					if t.Status != "completed" {
						f["errorKind"] = t.ErrorKind
						return fmt.Errorf("task ended as %s", t.Status)
					}
					return nil
				}
			}
			time.Sleep(time.Second)
		}
		return errors.New("cache did not finish within 3 minutes")
	})

	run("parse_live_url", func(f map[string]any) error {
		sniff.mu.Lock()
		defer sniff.mu.Unlock()
		f["detailResponses"], f["majorTypes"], f["pics"], f["picsWithLiveURL"] = sniff.responses, sniff.majorType, sniff.pics, sniff.livePics
		f["picKeys"], f["liveExts"], f["liveHosts"] = sniff.picKeys, sniff.liveExts, sniff.liveHosts
		if sniff.responses == 0 {
			return errSkip
		}
		if sniff.livePics == 0 {
			return errors.New("detail response has no non-empty live_url")
		}
		return nil
	})

	run("inspect_motion_clips", func(f map[string]any) error {
		if itemKey == "" {
			return errSkip
		}
		item, ok, err := b.GetLibraryItem(itemKey)
		if err != nil || !ok {
			return fmt.Errorf("cached item not indexed: %v", err)
		}
		f["itemStatus"], f["missing"] = item.Status, len(item.Missing)
		var stills, linked int
		var clips []map[string]any
		for _, m := range item.Media {
			if m.Role == "image" {
				stills++
				if m.Motion != "" {
					linked++
				}
			}
		}
		for _, m := range item.Media {
			if m.Role != "motion" {
				continue
			}
			c := map[string]any{"ext": filepath.Ext(m.Path), "bytes": m.Bytes}
			var still *library.MediaFile
			for i := range item.Media {
				if item.Media[i].Role == "image" && item.Media[i].Motion == m.Path {
					still = &item.Media[i]
				}
			}
			c["linkedToStill"] = still != nil
			full, perr := b.MediaPath(itemKey, m.Path)
			if perr != nil {
				c["error"] = "path"
				clips = append(clips, c)
				continue
			}
			c["topBoxes"] = topLevelBoxes(full)
			if sum, ierr := download.InspectMP4File(full); ierr == nil {
				c["codecs"], c["moovBeforeMdat"], c["durationSec"], c["width"], c["height"] = sum.Codecs, sum.MoovBeforeMdat, sum.DurationSec, sum.Width, sum.Height
				if still != nil && still.Width > 0 && still.Height > 0 && sum.Width > 0 && sum.Height > 0 {
					c["stillWH"] = fmt.Sprintf("%dx%d", still.Width, still.Height)
				}
			} else {
				c["inspectError"] = redact(ierr.Error(), cookie)
			}
			clips = append(clips, c)
		}
		sort.Slice(clips, func(i, j int) bool { return fmt.Sprint(clips[i]["bytes"]) < fmt.Sprint(clips[j]["bytes"]) })
		f["stills"], f["stillsWithMotion"], f["clips"] = stills, linked, clips
		ctMu.Lock()
		f["mediaContentTypes"] = contentTypes
		ctMu.Unlock()
		if len(clips) == 0 {
			return errors.New("no motion clip cached")
		}
		return nil
	})

	rc.APIUsage = gate.Usage()
	rc.VODMedia = mediaUsage{int(ck.vod.requests.Load()), ck.vod.bytes.Load()}
	rc.StoppedBy, _ = ck.stopped.Load().(string)
	rc.Passed = rc.StoppedBy == ""
	for _, s := range rc.Steps {
		if !s.OK {
			rc.Passed = false
		}
	}
	rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	out2, _ := json.MarshalIndent(rc, "", "  ")
	text := string(out2)
	for _, secret := range []string{cookieValue(cookie, "SESSDATA"), cookieValue(cookie, "bili_jct"), strings.TrimSpace(tg.OpusID)} {
		if secret != "" && strings.Contains(text, secret) {
			fatal("receipt would contain a secret or the target id; not written")
		}
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		fatal("receipt would contain a URL; not written")
	}
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	if err := os.WriteFile(out, out2, 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("%s passed=%v stoppedBy=%q api=%d cdn=%d/%dB receipt=%s\n",
		act019Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, rc.VODMedia.Requests, rc.VODMedia.Bytes, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-22s ok=%v skipped=%v %s %s\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Error)
	}
	if !rc.Passed {
		os.Exit(1)
	}
}

func redactID(s, id string) string {
	if id = strings.TrimSpace(id); id != "" {
		s = strings.ReplaceAll(s, id, "<opus>")
	}
	return s
}

// topLevelBoxes lists an MP4's top-level box types in file order.
func topLevelBoxes(p string) []string {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	var out []string
	var off int64
	extra := 0
	hdr := make([]byte, 16)
	for off+8 <= info.Size() {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			break
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		if size == 1 {
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				break
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		} else if size == 0 {
			size = info.Size() - off
		}
		if size < 8 {
			out = append(out, "!bad")
			break
		}
		if len(out) < 8 {
			out = append(out, typ)
		} else {
			extra++
		}
		off += size
	}
	if extra > 0 {
		out = append(out, fmt.Sprintf("+%d", extra))
	}
	return out
}
