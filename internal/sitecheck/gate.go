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
	if raw, err := os.ReadFile(ledgerPath); err == nil && len(raw) > 0 {
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

// Usage returns a snapshot of the current usage counters.
func (g *Gate) Usage() Usage {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := g.usage
	cp.Categories = make(map[string]int, len(g.usage.Categories))
	for k, v := range g.usage.Categories {
		cp.Categories[k] = v
	}
	return cp
}

// BeginRun checks and increments the run counter before starting a site-check run.
func (g *Gate) BeginRun() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limits.MaxRuns > 0 && g.usage.Runs >= g.limits.MaxRuns {
		return fmt.Errorf("%w: runs %d/%d", ErrQuotaExceeded, g.usage.Runs, g.limits.MaxRuns)
	}
	g.usage.Runs++
	return g.saveLocked()
}

// AcquireAPI checks and reserves 1 API call of the given category BEFORE the HTTP request is sent.
func (g *Gate) AcquireAPI(category string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.limits.MaxAPICalls > 0 && g.usage.APICalls >= g.limits.MaxAPICalls {
		return fmt.Errorf("%w: apiCalls %d/%d", ErrQuotaExceeded, g.usage.APICalls, g.limits.MaxAPICalls)
	}
	if maxCat, ok := g.limits.MaxByCategory[category]; ok && maxCat > 0 {
		cur := g.categoryCountLocked(category)
		if cur >= maxCat {
			return fmt.Errorf("%w: category %s %d/%d", ErrQuotaExceeded, category, cur, maxCat)
		}
	}

	g.usage.APICalls++
	g.incrementCategoryLocked(category)
	return g.saveLocked()
}

// AcquireMedia checks and reserves media file/byte quota BEFORE downloading media bytes.
func (g *Gate) AcquireMedia(expectedBytes int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limits.MaxMediaFiles > 0 && g.usage.MediaFiles >= g.limits.MaxMediaFiles {
		return fmt.Errorf("%w: mediaFiles %d/%d", ErrQuotaExceeded, g.usage.MediaFiles, g.limits.MaxMediaFiles)
	}
	if g.limits.MaxMediaBytes > 0 && g.usage.MediaBytes+expectedBytes > g.limits.MaxMediaBytes {
		return fmt.Errorf("%w: mediaBytes %d+%d/%d", ErrQuotaExceeded, g.usage.MediaBytes, expectedBytes, g.limits.MaxMediaBytes)
	}
	g.usage.MediaFiles++
	g.usage.MediaBytes += expectedBytes
	return g.saveLocked()
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

func (g *Gate) categoryCountLocked(category string) int {
	switch category {
	case "nav":
		return g.usage.NavCalls
	case "dynamic_feed":
		return g.usage.DynamicFeeds
	case "video_view":
		return g.usage.VideoViews
	case "playurl":
		return g.usage.PlayURLCalls
	case "dynamic_detail":
		return g.usage.DynamicDetails
	case "space_read":
		return g.usage.SpaceReads
	case "qr":
		return g.usage.QRCalls
	default:
		return g.usage.Categories[category]
	}
}

func (g *Gate) incrementCategoryLocked(category string) {
	switch category {
	case "nav":
		g.usage.NavCalls++
	case "dynamic_feed":
		g.usage.DynamicFeeds++
	case "video_view":
		g.usage.VideoViews++
	case "playurl":
		g.usage.PlayURLCalls++
	case "dynamic_detail":
		g.usage.DynamicDetails++
	case "space_read":
		g.usage.SpaceReads++
	case "qr":
		g.usage.QRCalls++
	}
	if g.usage.Categories == nil {
		g.usage.Categories = make(map[string]int)
	}
	g.usage.Categories[category]++
}

func (g *Gate) saveLocked() error {
	g.usage.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(g.usage, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(g.ledgerPath, raw, 0o600)
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

var signedReceiptURLRe = regexp.MustCompile(`(?i)https?://[^\s"'<>` + "`" + `]*(?:a_bogus=[A-Za-z0-9%+/=._-]{4,}|x-(?:secsdk-web-)?signature(?:=[A-Za-z0-9%+/=._-]{4,}|/[A-Za-z0-9%+/=._-]{4,}))`)

// ScanTextForSecrets checks an arbitrary text or JSON payload for Bilibili/Douyin Cookie markers
// and signed URLs containing a_bogus= or x-signature.
func ScanTextForSecrets(text string) []string {
	var findings []string
	for _, marker := range secretReceiptMarkers {
		if strings.Contains(text, marker) {
			findings = append(findings, fmt.Sprintf("contains secret marker %q", marker))
		}
	}
	if m := signedReceiptURLRe.FindString(text); m != "" {
		findings = append(findings, fmt.Sprintf("contains signed URL %q", m))
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
