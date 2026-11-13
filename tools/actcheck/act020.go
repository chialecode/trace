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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
)

// ACT-020: one read-only pass over the signed-in accounts' own lists (我的)
// through the production AccountService, to settle bilibili.md §21 B-V1～4
// and douyin.md §12 D-V1～5. The receipt holds only counts, statuses, error
// classes, cursor progress and field presence: no titles, names, ids,
// cookies or signed URLs. No image or media request is allowed.
const (
	act020ID      = "act020"
	act020Label   = "ACT-020"
	act020Timeout = 4 * time.Minute
)

var act020Limits = map[string]int{
	// 哔哩哔哩 ≤ 12: nav 1 + 2 for WBI refresh.
	"bili_nav": 3, "bili_relation_tags": 1, "bili_relation_followings": 1, "bili_fav_folders": 1,
	"bili_fav_resources": 2, "bili_toview": 2, "bili_history": 2,
	// 抖音 ≤ 10: one spare.
	"dy_profile_self": 1, "dy_following": 2, "dy_favorite": 1, "dy_listcollection": 2,
	"dy_collects_list": 1, "dy_collects_videos": 1, "dy_history_read": 1, "dy_spare": 1,
}

const act020MaxAPI = 22

func act020Classify(r *http.Request) (siteName, cat string) {
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/x/web-interface/nav"):
		return "bilibili", "bili_nav"
	case strings.HasSuffix(p, "/x/relation/tags"):
		return "bilibili", "bili_relation_tags"
	case strings.HasSuffix(p, "/x/relation/followings"):
		return "bilibili", "bili_relation_followings"
	case strings.HasSuffix(p, "/x/v3/fav/folder/created/list-all"):
		return "bilibili", "bili_fav_folders"
	case strings.HasSuffix(p, "/x/v3/fav/resource/list"):
		return "bilibili", "bili_fav_resources"
	case strings.HasSuffix(p, "/x/v2/history/toview/web"):
		return "bilibili", "bili_toview"
	case strings.HasSuffix(p, "/x/web-interface/history/cursor"):
		return "bilibili", "bili_history"
	case strings.HasSuffix(p, "/aweme/v1/web/user/profile/self/"):
		return "douyin", "dy_profile_self"
	case strings.HasSuffix(p, "/aweme/v1/web/user/following/list/"):
		return "douyin", "dy_following"
	case strings.HasSuffix(p, "/aweme/v1/web/aweme/favorite/"):
		return "douyin", "dy_favorite"
	case strings.HasSuffix(p, "/aweme/v1/web/aweme/listcollection/"):
		return "douyin", "dy_listcollection"
	case strings.HasSuffix(p, "/aweme/v1/web/collects/list/"):
		return "douyin", "dy_collects_list"
	case strings.HasSuffix(p, "/aweme/v1/web/collects/video/list/"):
		return "douyin", "dy_collects_videos"
	case strings.HasSuffix(p, "/aweme/v1/web/history/read/"):
		return "douyin", "dy_history_read"
	}
	return "", ""
}

type act020Checker struct {
	gate     *sitecheck.Gate
	mu       sync.Mutex
	stopped  map[string]string // site -> reason
	statuses map[string]int    // cat:status
	// shapes keeps only the top-level key names of the 观看历史 probe.
	historyKeys []string
	historyList string
}

func (c *act020Checker) stop(siteName, reason string) {
	c.mu.Lock()
	if c.stopped[siteName] == "" {
		c.stopped[siteName] = reason
	}
	c.mu.Unlock()
}

func (c *act020Checker) stopReason(siteName string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped[siteName]
}

func (c *act020Checker) wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		siteName, cat := act020Classify(r)
		if cat == "" {
			return nil, fmt.Errorf("%w: endpoint outside the authorized list", sitecheck.ErrQuotaExceeded)
		}
		if reason := c.stopReason(siteName); reason != "" {
			return nil, fmt.Errorf("act020 %s halted (%s)", siteName, reason)
		}
		if err := c.gate.AcquireAPI(cat); err != nil {
			return nil, err
		}
		resp, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.statuses[fmt.Sprintf("%s:%d", cat, resp.StatusCode)]++
		c.mu.Unlock()
		switch {
		case siteName == "bilibili" && resp.StatusCode == http.StatusPreconditionFailed:
			c.stop(siteName, "http_412")
		case siteName == "douyin" && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 444):
			c.stop(siteName, fmt.Sprintf("http_%d", resp.StatusCode))
		}
		if cat == "dy_history_read" {
			raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if rerr == nil {
				c.noteHistoryShape(raw)
			}
		}
		return resp, nil
	})
}

func (c *act020Checker) noteHistoryShape(raw []byte) {
	var top map[string]json.RawMessage
	shape := "non_json"
	if len(bytes.TrimSpace(raw)) == 0 {
		shape = "empty_body"
	} else if json.Unmarshal(raw, &top) == nil {
		shape = "no_aweme_list"
		if v, ok := top["aweme_list"]; ok {
			var arr []json.RawMessage
			switch {
			case bytes.Equal(bytes.TrimSpace(v), []byte("null")):
				shape = "aweme_list_null"
			case json.Unmarshal(v, &arr) == nil:
				shape = fmt.Sprintf("aweme_list_len_%d", len(arr))
			}
		}
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		if len(k) <= 40 && !strings.ContainsAny(k, "/:=") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	c.mu.Lock()
	c.historyKeys, c.historyList = keys, shape
	c.mu.Unlock()
}

type act020Receipt struct {
	Act             string                    `json:"act"`
	Commit          string                    `json:"commit,omitempty"`
	StartedAt       string                    `json:"startedAt"`
	FinishedAt      string                    `json:"finishedAt"`
	Passed          bool                      `json:"passed"`
	StoppedBy       map[string]string         `json:"stoppedBy,omitempty"`
	Limits          map[string]int            `json:"limits"`
	APIUsage        sitecheck.Usage           `json:"apiUsage"`
	StatusCounts    map[string]int            `json:"statusCounts"`
	Steps           []step                    `json:"steps"`
	Verdicts        map[string]string         `json:"verdicts"`
	WatchHistory    map[string]any            `json:"douyinWatchHistory,omitempty"`
	TempDeleted     bool                      `json:"tempDataDeleted"`
	SecretScanClean bool                      `json:"secretScanClean"`
	Notes           []string                  `json:"notes,omitempty"`
	pages           map[string]*act020PageLog `json:"-"`
}

type act020PageLog struct{ cursors []string }

func runAct020Main(root, out string) {
	if out == "" {
		out = filepath.Join(root, "artifacts", "site-check", act020ID+".json")
	}
	rc, err := runAct020(context.Background(), root, out)
	fmt.Printf("%s passed=%v stoppedBy=%v api=%d receipt=%s\n", act020Label, rc.Passed, rc.StoppedBy, rc.APIUsage.APICalls, out)
	for _, s := range rc.Steps {
		fmt.Printf("  %-28s ok=%v skipped=%v %s %v\n", s.Name, s.OK, s.Skipped, s.ErrKind, s.Facts)
	}
	for k, v := range rc.Verdicts {
		fmt.Printf("  %s: %s\n", k, v)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", act020Label, err)
		os.Exit(1)
	}
}

func runAct020(parent context.Context, root, outPath string) (act020Receipt, error) {
	rc := act020Receipt{Act: act020Label, Commit: sourceCommit(root), Limits: act020Limits, Verdicts: map[string]string{}, pages: map[string]*act020PageLog{}}
	biliCookie, err := readCookie(root)
	if err != nil {
		return rc, errors.New("bilibili cookie: " + err.Error())
	}
	dyCookie, dyClean, err := readAndValidateDouyinCookie(root)
	if err != nil {
		return rc, err
	}
	redactor := newAct010Redactor()
	redactor.addCookieHeader(biliCookie)
	redactor.addCookieHeader(dyCookie)
	redactor.addCookieMap(dyClean)

	gate, err := sitecheck.OpenGate(filepath.Join(root, ".local", "site-check"), sitecheck.Limits{
		ActID: act020ID, MaxRuns: 1, MaxAPICalls: act020MaxAPI, MaxByCategory: act020Limits,
	})
	if err != nil {
		return rc, err
	}
	if err := gate.BeginRun(); err != nil {
		return rc, err
	}
	ck := &act020Checker{gate: gate, stopped: map[string]string{}, statuses: map[string]int{}}

	work, err := os.MkdirTemp("", "trace-"+act020ID+"-")
	if err != nil {
		return rc, err
	}
	cleanup := func() bool {
		_ = os.RemoveAll(work)
		_, statErr := os.Stat(work)
		return os.IsNotExist(statErr)
	}
	defer cleanup()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel: "dev", ForceMode: "installed",
		LocalAppData: filepath.Join(work, "localappdata"), DocumentsDir: filepath.Join(work, "documents"),
	})
	if err != nil {
		return rc, err
	}
	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
		BilibiliOpts:     bilibili.ClientOptions{WrapTransport: ck.wrap},
		DouyinOpts:       douyin.ClientOptions{WrapTransport: ck.wrap},
	})
	if err != nil {
		return rc, err
	}
	closed := false
	closeBackend := func() {
		if !closed {
			closed = true
			_ = b.Close()
		}
	}
	defer closeBackend()

	rc.StartedAt = time.Now().UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(parent, act020Timeout)
	defer cancel()

	runStep := func(siteName, name string, fn func(f map[string]any) error) bool {
		if reason := ck.stopReason(siteName); reason != "" {
			rc.Steps = append(rc.Steps, step{Name: name, Skipped: true, Error: "stopped earlier: " + reason})
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
			st.Error = act020Scrub(redactor.scrubError(err.Error()))
			switch st.ErrKind {
			case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindAuthRequired, site.ErrKindBackoff:
				ck.stop(siteName, st.ErrKind)
			}
		}
		if len(st.Facts) == 0 {
			st.Facts = nil
		}
		rc.Steps = append(rc.Steps, st)
		return st.OK
	}
	// page records what a page says about the list without its content.
	page := func(f map[string]any, key string, p service.AccountPage, check func(service.AccountItem) bool) {
		f["items"], f["hasMore"], f["total"] = len(p.Items), p.HasMore, p.Total
		complete := 0
		kinds := map[string]int{}
		for _, it := range p.Items {
			redactor.addIDs(it.ID, it.AuthorID)
			redactor.addHandles(it.Title, it.Author, it.UniqueID, it.Signature)
			kinds[it.Kind]++
			if check(it) {
				complete++
			}
		}
		f["kinds"], f["fieldsComplete"] = kinds, complete
		log := rc.pages[key]
		if log == nil {
			log = &act020PageLog{}
			rc.pages[key] = log
		}
		if len(log.cursors) > 0 {
			f["cursorAdvanced"] = p.Cursor == "" || p.Cursor != log.cursors[len(log.cursors)-1]
		}
		log.cursors = append(log.cursors, p.Cursor)
	}
	videoOK := func(it service.AccountItem) bool {
		if it.Invalid || it.Kind == service.AccountItemUnsupported {
			return it.Title != ""
		}
		return it.ID != "" && it.Title != "" && it.Cover != "" && it.OpenURL != ""
	}
	userOK := func(it service.AccountItem) bool { return it.ID != "" && it.Title != "" && it.OpenURL != "" }
	seenIDs := map[string]map[string]bool{}
	dupes := func(key string, items []service.AccountItem) int {
		m := seenIDs[key]
		if m == nil {
			m = map[string]bool{}
			seenIDs[key] = m
		}
		n := 0
		for _, it := range items {
			id := it.Kind + ":" + it.ID
			if it.ID == "" {
				continue
			}
			if m[id] {
				n++
			}
			m[id] = true
		}
		return n
	}

	// ---------- 哔哩哔哩 ----------
	runStep("bilibili", "bili_login", func(f map[string]any) error {
		st, err := b.LoginWithCookie(ctx, "bilibili", biliCookie, "")
		if st.Mid > 0 {
			redactor.addIDs(fmt.Sprint(st.Mid))
		}
		redactor.addHandles(st.Uname)
		f["loggedIn"] = st.LoggedIn
		return err
	})
	runStep("bilibili", "B-V0_following", func(f map[string]any) error {
		groups, err := b.AccountFolders(ctx, "bilibili", service.AccountFollowing)
		if err != nil {
			return err
		}
		f["groups"] = len(groups)
		for _, g := range groups {
			redactor.addHandles(g.Title)
		}
		p, err := b.AccountPage(ctx, "bilibili", service.AccountFollowing, service.AccountFolderAll, "")
		if err != nil {
			return err
		}
		page(f, "bili_following", p, userOK)
		return nil
	})
	var defaultFolder string
	runStep("bilibili", "B-V1_fav_folders", func(f map[string]any) error {
		folders, err := b.AccountFolders(ctx, "bilibili", service.AccountFavorites)
		if err != nil {
			return err
		}
		f["folders"] = len(folders)
		defaults, private := 0, 0
		for i, fo := range folders {
			redactor.addIDs(fo.ID)
			redactor.addHandles(fo.Title)
			if fo.IsDefault {
				defaults++
				if i != 0 {
					f["defaultNotFirst"] = true
				}
			}
			if fo.Private {
				private++
			}
		}
		f["defaultFolders"], f["privateFolders"] = defaults, private
		if len(folders) > 0 {
			defaultFolder = folders[0].ID
			f["firstFolderCount"] = folders[0].Count
		}
		if defaults != 1 {
			rc.Notes = append(rc.Notes, "B-V1: expected exactly one default folder")
		}
		return nil
	})
	runStep("bilibili", "B-V2_fav_resources", func(f map[string]any) error {
		if defaultFolder == "" {
			f["noFolder"] = true
			return nil
		}
		p1, err := b.AccountPage(ctx, "bilibili", service.AccountFavorites, defaultFolder, "")
		if err != nil {
			return err
		}
		page(f, "bili_fav", p1, videoOK)
		f["dupesPage1"] = dupes("bili_fav", p1.Items)
		invalid := 0
		for _, it := range p1.Items {
			if it.Invalid {
				invalid++
			}
		}
		f["invalidItems"] = invalid
		if p1.HasMore {
			p2, err := b.AccountPage(ctx, "bilibili", service.AccountFavorites, defaultFolder, p1.Cursor)
			if err != nil {
				return err
			}
			f2 := map[string]any{}
			page(f2, "bili_fav", p2, videoOK)
			f2["dupesAcrossPages"] = dupes("bili_fav", p2.Items)
			f["page2"] = f2
		}
		return nil
	})
	runStep("bilibili", "B-V3_toview", func(f map[string]any) error {
		p, err := b.AccountPage(ctx, "bilibili", service.AccountToView, service.AccountFolderAll, "")
		if err != nil {
			return err
		}
		page(f, "bili_toview", p, videoOK)
		finished := 0
		for _, it := range p.Items {
			if it.Finished {
				finished++
			}
		}
		f["finished"] = finished
		un, err := b.AccountPage(ctx, "bilibili", service.AccountToView, service.AccountFolderUnfinished, "")
		if err != nil {
			return err
		}
		fu := map[string]any{}
		page(fu, "bili_toview_unfinished", un, videoOK)
		f["unfinished"] = fu
		return nil
	})
	runStep("bilibili", "B-V4_history", func(f map[string]any) error {
		p1, err := b.AccountPage(ctx, "bilibili", service.AccountHistory, "", "")
		if err != nil {
			return err
		}
		page(f, "bili_history", p1, func(it service.AccountItem) bool { return it.Title != "" && it.ViewedAt > 0 })
		f["dupesPage1"] = dupes("bili_history", p1.Items)
		labels := map[string]int{}
		for _, it := range p1.Items {
			labels[it.Kind+"/"+it.TypeLabel]++
		}
		f["businesses"] = labels
		if p1.HasMore {
			p2, err := b.AccountPage(ctx, "bilibili", service.AccountHistory, "", p1.Cursor)
			if err != nil {
				return err
			}
			f2 := map[string]any{}
			page(f2, "bili_history", p2, func(it service.AccountItem) bool { return it.Title != "" && it.ViewedAt > 0 })
			f2["dupesAcrossPages"] = dupes("bili_history", p2.Items)
			f["page2"] = f2
		}
		return nil
	})

	// ---------- 抖音 ----------
	runStep("douyin", "dy_login", func(f map[string]any) error {
		st, err := b.LoginWithCookie(ctx, "douyin", dyCookie, "")
		redactor.addIDs(st.UID, st.SecUID)
		redactor.addHandles(st.UniqueID, st.Uname)
		f["loggedIn"] = st.LoggedIn
		return err
	})
	runStep("douyin", "D-V1_following", func(f map[string]any) error {
		p1, err := b.AccountPage(ctx, "douyin", service.AccountFollowing, "", "")
		if err != nil {
			return err
		}
		page(f, "dy_following", p1, userOK)
		f["dupesPage1"] = dupes("dy_following", p1.Items)
		if p1.HasMore {
			p2, err := b.AccountPage(ctx, "douyin", service.AccountFollowing, "", p1.Cursor)
			if err != nil {
				return err
			}
			f2 := map[string]any{}
			page(f2, "dy_following", p2, userOK)
			f2["dupesAcrossPages"] = dupes("dy_following", p2.Items)
			f["page2"] = f2
		}
		return nil
	})
	runStep("douyin", "D-V2_likes", func(f map[string]any) error {
		p, err := b.AccountPage(ctx, "douyin", service.AccountLikes, "", "")
		if err != nil {
			return err
		}
		page(f, "dy_likes", p, videoOK)
		return nil
	})
	runStep("douyin", "D-V3_collection_post", func(f map[string]any) error {
		p1, err := b.AccountPage(ctx, "douyin", service.AccountCollection, service.AccountFolderAll, "")
		if err != nil {
			return err
		}
		page(f, "dy_collection", p1, videoOK)
		f["dupesPage1"] = dupes("dy_collection", p1.Items)
		if p1.HasMore {
			p2, err := b.AccountPage(ctx, "douyin", service.AccountCollection, service.AccountFolderAll, p1.Cursor)
			if err != nil {
				return err
			}
			f2 := map[string]any{}
			page(f2, "dy_collection", p2, videoOK)
			f2["dupesAcrossPages"] = dupes("dy_collection", p2.Items)
			f["page2"] = f2
		}
		return nil
	})
	runStep("douyin", "D-V4_collect_folders", func(f map[string]any) error {
		folders, err := b.DouyinClient().CollectFolders(ctx, "")
		if err != nil {
			return err
		}
		f["folders"], f["hasMore"] = len(folders.Folders), folders.HasMore
		idStr := 0
		for _, fo := range folders.Folders {
			redactor.addIDs(fo.ID)
			redactor.addHandles(fo.Name)
			if fo.ID != "" {
				idStr++
			}
		}
		f["foldersWithID"] = idStr
		if len(folders.Folders) == 0 {
			return nil
		}
		p, err := b.AccountPage(ctx, "douyin", service.AccountCollection, folders.Folders[0].ID, "")
		if err != nil {
			return err
		}
		fp := map[string]any{}
		page(fp, "dy_folder", p, videoOK)
		f["firstFolder"] = fp
		return nil
	})
	runStep("douyin", "D-V5_watch_history", func(f map[string]any) error {
		p, err := b.DouyinClient().WatchHistory(ctx, "")
		ck.mu.Lock()
		f["topLevelKeys"], f["listShape"] = append([]string(nil), ck.historyKeys...), ck.historyList
		ck.mu.Unlock()
		if err != nil {
			return err
		}
		f["items"], f["hasMore"] = len(p.Works), p.HasMore
		for _, w := range p.Works {
			redactor.addIDs(w.ID, w.Author.SecUID)
			redactor.addHandles(w.Title, w.Author.Nickname)
		}
		return nil
	})

	closeBackend()
	rc.TempDeleted = cleanup()
	rc.APIUsage = gate.Usage()
	rc.StatusCounts = ck.statuses
	ck.mu.Lock()
	if len(ck.stopped) > 0 {
		rc.StoppedBy = ck.stopped
	}
	ck.mu.Unlock()
	for _, s := range rc.Steps {
		v := "pass"
		switch {
		case s.Skipped:
			v = "skipped"
		case !s.OK:
			v = "fail:" + s.ErrKind
		}
		rc.Verdicts[s.Name] = v
	}
	rc.Passed = rc.TempDeleted && len(rc.StoppedBy) == 0
	for _, s := range rc.Steps {
		if !s.OK && s.Name != "D-V5_watch_history" {
			rc.Passed = false
		}
	}
	rc.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	rc.SecretScanClean = true
	raw, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return rc, err
	}
	if err := redactor.verifyJSONClean(string(raw), nil); err != nil {
		rc.SecretScanClean, rc.Passed = false, false
		return rc, err
	}
	if err := act020NoHandles(redactor, string(raw)); err != nil {
		rc.SecretScanClean, rc.Passed = false, false
		return rc, err
	}
	if hits := sitecheck.ScanTextForSecrets(string(raw)); len(hits) > 0 {
		rc.SecretScanClean, rc.Passed = false, false
		return rc, fmt.Errorf("receipt secret scan hit %d pattern(s)", len(hits))
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return rc, err
	}
	if err := storage.WriteFileAtomic(outPath, raw, 0o644); err != nil {
		return rc, err
	}
	if !rc.Passed {
		return rc, errors.New("act020 completed with failures or stops")
	}
	return rc, nil
}

var reAct020URL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://\S+`)

// act020Scrub replaces URLs (scheme://…) and caps the length. It keeps the
// rest of the message: the ACT-020 run of 2026-11-14 cut at the "http" of
// "http=200" and lost the reason of D-V2.
func act020Scrub(s string) string {
	s = reAct020URL.ReplaceAllString(s, "[URL]")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160])
	}
	return s
}

// act020NoHandles fails when any collected title or name (4+ characters) appears in the receipt.
func act020NoHandles(r *act010Redactor, raw string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h := range r.handles {
		if utf8.RuneCountInString(h) >= 4 && strings.Contains(raw, h) {
			return errors.New("receipt contains a list title or name")
		}
	}
	return nil
}
