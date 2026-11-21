package sitecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// ErrQuotaExceeded is returned BEFORE a network request is sent when an ACT quota is exhausted.
var ErrQuotaExceeded = errors.New("sitecheck: quota exceeded for authorization")

// Usage tracks cumulative site-check consumption for a specific ACT authorization ID.
type Usage struct {
	ActID          string         `json:"actId"`
	UpdatedAt      string         `json:"updatedAt"`
	Runs           int            `json:"runs"`
	APICalls       int            `json:"apiCalls"`
	NavCalls       int            `json:"navCalls"`
	DynamicFeeds   int            `json:"dynamicFeeds"`
	VideoViews     int            `json:"videoViews"`
	PlayURLCalls   int            `json:"playUrlCalls"`
	DynamicDetails int            `json:"dynamicDetails"`
	SpaceReads     int            `json:"spaceReads"`
	QRCalls        int            `json:"qrCalls"`
	MediaFiles     int            `json:"mediaFiles"`
	MediaBytes     int64          `json:"mediaBytes"`
	Categories     map[string]int `json:"categories,omitempty"`
}

// Limits defines the maximum allowed operations under an ACT authorization in USER-ACTIONS.md.
type Limits struct {
	ActID         string         `json:"actId"`
	MaxRuns       int            `json:"maxRuns"`
	MaxAPICalls   int            `json:"maxApiCalls"`
	MaxMediaFiles int            `json:"maxMediaFiles"`
	MaxMediaBytes int64          `json:"maxMediaBytes"`
	MaxByCategory map[string]int `json:"maxByCategory,omitempty"`
}

// Gate enforces ACT quota limits backed by a persistent JSON ledger (`usage-<ACT>.json`).
type Gate struct {
	mu         sync.Mutex
	ledgerPath string
	limits     Limits
	usage      Usage
}

// OpenGate loads or initializes the persistent usage ledger at `<ledgerDir>/usage-<actId>.json`.
func OpenGate(ledgerDir string, limits Limits) (*Gate, error) {
	actID := strings.TrimSpace(limits.ActID)
	if actID == "" {
		return nil, fmt.Errorf("sitecheck: actId is required")
	}
	if err := os.MkdirAll(ledgerDir, 0o700); err != nil {
		return nil, err
	}
	ledgerPath := filepath.Join(ledgerDir, fmt.Sprintf("usage-%s.json", actID))
	u := Usage{
		ActID:      actID,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Categories: make(map[string]int),
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sitecheck: read %s: %w", ledgerPath, err)
	}
	if err == nil && len(raw) > 0 {
		if uErr := json.Unmarshal(raw, &u); uErr != nil {
			return nil, fmt.Errorf("sitecheck: parse %s: %w", ledgerPath, uErr)
		}
		if u.Categories == nil {
			u.Categories = make(map[string]int)
		}
	}
	return &Gate{
		ledgerPath: ledgerPath,
		limits:     limits,
		usage:      u,
	}, nil
}

func cloneUsage(in Usage) Usage {
	cp := in
	cp.Categories = make(map[string]int, len(in.Categories))
	for k, v := range in.Categories {
		cp.Categories[k] = v
	}
	return cp
}

// Usage returns a snapshot of the current usage counters.
func (g *Gate) Usage() Usage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneUsage(g.usage)
}

// BeginRun checks and increments the run counter before starting a site-check run.
func (g *Gate) BeginRun() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limits.MaxRuns > 0 && g.usage.Runs >= g.limits.MaxRuns {
		return fmt.Errorf("%w: runs %d/%d", ErrQuotaExceeded, g.usage.Runs, g.limits.MaxRuns)
	}
	next := cloneUsage(g.usage)
	next.Runs++
	return g.commitUsageLocked(next)
}

// AcquireAPI checks and reserves 1 API call of the given category BEFORE the HTTP request is sent.
func (g *Gate) AcquireAPI(category string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.limits.MaxAPICalls > 0 && g.usage.APICalls >= g.limits.MaxAPICalls {
		return fmt.Errorf("%w: apiCalls %d/%d", ErrQuotaExceeded, g.usage.APICalls, g.limits.MaxAPICalls)
	}
	if len(g.limits.MaxByCategory) > 0 {
		maxCat, ok := g.limits.MaxByCategory[category]
		if !ok || maxCat <= 0 {
			return fmt.Errorf("%w: category %s not allowed", ErrQuotaExceeded, category)
		}
		cur := categoryCount(g.usage, category)
		if cur >= maxCat {
			return fmt.Errorf("%w: category %s %d/%d", ErrQuotaExceeded, category, cur, maxCat)
		}
	}

	next := cloneUsage(g.usage)
	next.APICalls++
	incrementCategory(&next, category)
	return g.commitUsageLocked(next)
}

// AcquireMedia checks and reserves media file/byte quota BEFORE downloading media bytes.
func (g *Gate) AcquireMedia(expectedBytes int64) error {
	if expectedBytes < 0 {
		return fmt.Errorf("sitecheck: invalid negative expectedBytes %d", expectedBytes)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limits.MaxMediaFiles > 0 && g.usage.MediaFiles >= g.limits.MaxMediaFiles {
		return fmt.Errorf("%w: mediaFiles %d/%d", ErrQuotaExceeded, g.usage.MediaFiles, g.limits.MaxMediaFiles)
	}
	if g.limits.MaxMediaBytes > 0 && g.usage.MediaBytes+expectedBytes > g.limits.MaxMediaBytes {
		return fmt.Errorf("%w: mediaBytes %d+%d/%d", ErrQuotaExceeded, g.usage.MediaBytes, expectedBytes, g.limits.MaxMediaBytes)
	}
	next := cloneUsage(g.usage)
	next.MediaFiles++
	next.MediaBytes += expectedBytes
	return g.commitUsageLocked(next)
}

// WrapTransport wraps base RoundTripper so every outgoing request is gated by AcquireAPI before transmission.
func (g *Gate) WrapTransport(base http.RoundTripper, classify func(*http.Request) string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cat := "api"
		if classify != nil {
			if c := classify(req); c != "" {
				cat = c
			}
		}
		if err := g.AcquireAPI(cat); err != nil {
			return nil, err
		}
		return base.RoundTrip(req)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func categoryCount(u Usage, category string) int {
	switch category {
	case "nav":
		return u.NavCalls
	case "dynamic_feed":
		return u.DynamicFeeds
	case "video_view":
		return u.VideoViews
	case "playurl":
		return u.PlayURLCalls
	case "dynamic_detail":
		return u.DynamicDetails
	case "space_read":
		return u.SpaceReads
	case "qr":
		return u.QRCalls
	default:
		return u.Categories[category]
	}
}

func incrementCategory(u *Usage, category string) {
	switch category {
	case "nav":
		u.NavCalls++
	case "dynamic_feed":
		u.DynamicFeeds++
	case "video_view":
		u.VideoViews++
	case "playurl":
		u.PlayURLCalls++
	case "dynamic_detail":
		u.DynamicDetails++
	case "space_read":
		u.SpaceReads++
	case "qr":
		u.QRCalls++
	}
	if u.Categories == nil {
		u.Categories = make(map[string]int)
	}
	u.Categories[category]++
}

func (g *Gate) commitUsageLocked(next Usage) error {
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := storage.WriteFileAtomic(g.ledgerPath, raw, 0o600); err != nil {
		return err
	}
	g.usage = next
	return nil
}

var secretReceiptMarkers = []string{
	// Bilibili Cookie & refresh markers
	"SESSDATA=",
	"bili_jct=",
	"DedeUserID__ckMd5=",
	"ac_time_value=",
	"refresh_token=",
	"access_key=",
	// Douyin Cookie & session markers
	"sessionid=",
	"sessionid_ss=",
	"sid_guard=",
	"msToken=",
}

var signedReceiptURLRe = regexp.MustCompile(`(?i)https?://[^\s"'<>` + "`" + `]*?(a_bogus|x-(?:secsdk-web-)?signature)(?:=[A-Za-z0-9%+/=._-]{4,}|/[A-Za-z0-9%+/=._-]{4,})`)

// ScanTextForSecrets checks an arbitrary text or JSON payload for Bilibili/Douyin Cookie markers
// and signed URLs containing a_bogus= or x-signature.
func ScanTextForSecrets(text string) []string {
	var findings []string
	for _, marker := range secretReceiptMarkers {
		if strings.Contains(text, marker) {
			findings = append(findings, fmt.Sprintf("contains secret marker %q", marker))
		}
	}
	if m := signedReceiptURLRe.FindStringSubmatch(text); len(m) >= 2 {
		findings = append(findings, fmt.Sprintf("contains signed URL marker %q (redacted)", strings.ToLower(m[1])))
	}
	return findings
}

// ScanReceiptsForSecrets scans JSON receipts in dir for any leaked Cookie/token markers or signed URLs.
// Returns nil without error if dir does not exist (such as clean CI environments).
func ScanReceiptsForSecrets(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var findings []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, err
		}
		for _, item := range ScanTextForSecrets(string(raw)) {
			findings = append(findings, fmt.Sprintf("%s %s", entry.Name(), item))
		}
	}
	return findings, nil
}
