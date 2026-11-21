package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chialecode/trace/internal/sitecheck"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/tools/internal/build"
)

var errSkip = errors.New("skipped")

var reRedactURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>` + "`" + `]+`)

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

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// checkAPICategoryAllowed enforces a fail-closed per-ACT category allowlist before
// gate.AcquireAPI is called, because sitecheck.Gate treats missing or <=0 entries
// in MaxByCategory as uncapped.
func checkAPICategoryAllowed(limits map[string]int, cat string) error {
	if limits[cat] <= 0 {
		return fmt.Errorf("%w: category %s is not authorized", sitecheck.ErrQuotaExceeded, cat)
	}
	return nil
}

// stepRunner executes sequential check steps with unified stop-checking, error
// classification, error redaction, and step recording.
type stepRunner struct {
	steps       *[]step
	stopReason  func() string
	tripStop    func(reason string)
	classifyErr func(error) string
	scrubErr    func(string) string
	stopOnKinds map[string]bool
}

func (r *stepRunner) run(name string, fn func(facts map[string]any) error) bool {
	if stopped := r.stopReason(); stopped != "" {
		*r.steps = append(*r.steps, step{
			Name:    name,
			Skipped: true,
			Error:   "stopped earlier: " + stopped,
		})
		return false
	}
	facts := make(map[string]any)
	start := time.Now()
	err := func() (outErr error) {
		defer func() {
			if rec := recover(); rec != nil {
				outErr = fmt.Errorf("step panicked: %v", rec)
			}
		}()
		return fn(facts)
	}()
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
		st.ErrKind = r.classifyErr(err)
		if errors.Is(err, sitecheck.ErrQuotaExceeded) {
			st.ErrKind = "quota"
		}
		st.Error = r.scrubErr(err.Error())
		if r.stopOnKinds[st.ErrKind] {
			r.tripStop(st.ErrKind)
		}
	}
	if len(st.Facts) == 0 {
		st.Facts = nil
	}
	*r.steps = append(*r.steps, st)
	return st.OK
}

// writeCleanReceipt marshals a receipt, runs sitecheck.ScanTextForSecrets and all
// extra safety checks before touching disk, and writes atomically via temp+rename.
func writeCleanReceipt(outPath string, rc any, extraChecks ...func(rawJSON string) error) error {
	rawJSON, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return err
	}
	text := string(rawJSON)
	if findings := sitecheck.ScanTextForSecrets(text); len(findings) > 0 {
		return fmt.Errorf("receipt failed secret scan: %s", findings[0])
	}
	for _, check := range extraChecks {
		if check == nil {
			continue
		}
		if err := check(text); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return storage.WriteFileAtomic(outPath, rawJSON, 0o644)
}

// verifyCookieValuesAbsent ensures non-empty values for named cookie keys never
// appear in serialized receipt text.
func verifyCookieValuesAbsent(rawJSON, cookie string, keys ...string) error {
	for _, key := range keys {
		val := cookieValue(cookie, key)
		if val != "" && strings.Contains(rawJSON, val) {
			return fmt.Errorf("receipt would contain secret cookie %s; not written", key)
		}
	}
	return nil
}

// parseHLSPlaylistSummary extracts only structural metadata from an HLS playlist
// (tag names/counts, relative entry count, entry extensions). No URI or token text.
func parseHLSPlaylistSummary(status int, contentType string, raw []byte, includeFirstLine bool) map[string]any {
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
		if includeFirstLine && i == 0 {
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
		if !strings.HasPrefix(strings.ToLower(line), "http") {
			relative++
		}
		pathPart, _, _ := strings.Cut(line, "?")
		exts[strings.ToLower(filepath.Ext(pathPart))]++
	}
	summary := map[string]any{
		"status":              status,
		"contentType":         contentType,
		"bytes":               len(raw),
		"tags":                tags,
		"entries":             entries,
		"relativeEntries":     relative,
		"entryExt":            exts,
		"mapBeforeFirstEntry": mapBeforeFirstEntry,
	}
	if includeFirstLine {
		summary["startsWith"] = firstLine
	}
	return summary
}

// resolveReceiptCommit returns the current source commit (with -dirty when the
// working tree has modifications) via tools/internal/build.HeadInfo, falling back
// to dist/trace-dev/VERSION.txt when git metadata is unavailable.
func resolveReceiptCommit(root string) string {
	if short, dirty, err := build.HeadInfo(root); err == nil && short != "" {
		if dirty {
			return short + "-dirty"
		}
		return short
	}
	raw, err := os.ReadFile(filepath.Join(root, "dist", "trace-dev", "VERSION.txt"))
	if err != nil {
		return ""
	}
	return build.ParseVersionFile(string(raw))["commit"]
}

func readEnvCookieFile(root, envKey string) (string, error) {
	f, err := os.Open(filepath.Join(root, ".env.local"))
	if err != nil {
		return "", errors.New(".env.local unreadable")
	}
	defer f.Close()

	var path string
	prefix := envKey + "="
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), prefix); ok {
			path = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	if path == "" {
		return "", fmt.Errorf("%s is not set", envKey)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s unreadable", envKey)
	}
	return strings.TrimSpace(string(raw)), nil
}

func readCookie(root string) (string, error) {
	return readEnvCookieFile(root, "TRACE_TEST_BILIBILI_COOKIE_FILE")
}

func cookieValue(cookie, key string) string {
	for _, part := range strings.Split(cookie, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k == key && len(v) >= 4 {
			return v
		}
	}
	return ""
}

// redactURLs replaces scheme://... URL tokens with "[URL]" without truncating
// non-URL text such as "http=200" or trailing error context.
func redactURLs(s string) string {
	return reRedactURL.ReplaceAllString(s, "[URL]")
}

func redact(s, cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		if _, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	return redactURLs(s)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "actcheck:", msg)
	os.Exit(2)
}
