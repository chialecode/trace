package service

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

// Desktop abstracts OS shell actions (opening URLs, folders, or revealing files).
// Automated tests, prototypes, uishots, and smoke runs use MockDesktop or TRACE_QUIET
// so no external browser or Explorer window is ever launched.
type Desktop interface {
	OpenExternalURL(rawURL string) error
	OpenPath(path string) error
	RevealPath(path string) error
}

// validateExternalURL ensures only safe HTTP/HTTPS URLs with a non-empty host
// and no embedded credentials or control characters are passed to the OS URL handler.
func validateExternalURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("empty url")
	}
	for _, r := range trimmed {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return "", fmt.Errorf("invalid url: contains whitespace, backslash, or control character")
		}
	}
	if strings.HasPrefix(trimmed, `\\`) {
		return "", fmt.Errorf("invalid url: UNC path rejected")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if !u.IsAbs() {
		return "", fmt.Errorf("invalid url: must be absolute")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", fmt.Errorf("unsupported url scheme: %q", u.Scheme)
	}
	if u.Host == "" || strings.TrimSpace(u.Hostname()) == "" {
		return "", fmt.Errorf("invalid url: missing host")
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid url: userinfo not allowed")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String(), nil
}

// RecordedAction represents a shell action captured by MockDesktop.
type RecordedAction struct {
	Kind   string `json:"kind"` // "url", "open_path", "reveal_path"
	Target string `json:"target"`
}

// MockDesktop records all shell actions in memory without invoking OS processes.
type MockDesktop struct {
	mu      sync.Mutex
	actions []RecordedAction
}

func NewMockDesktop() *MockDesktop {
	return &MockDesktop{}
}

func (m *MockDesktop) OpenExternalURL(rawURL string) error {
	clean, err := validateExternalURL(rawURL)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.actions = append(m.actions, RecordedAction{Kind: "url", Target: clean})
	m.mu.Unlock()
	return nil
}

func (m *MockDesktop) OpenPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path")
	}
	m.mu.Lock()
	m.actions = append(m.actions, RecordedAction{Kind: "open_path", Target: path})
	m.mu.Unlock()
	return nil
}

func (m *MockDesktop) RevealPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path")
	}
	m.mu.Lock()
	m.actions = append(m.actions, RecordedAction{Kind: "reveal_path", Target: path})
	m.mu.Unlock()
	return nil
}

func (m *MockDesktop) Actions() []RecordedAction {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecordedAction, len(m.actions))
	copy(out, m.actions)
	return out
}

// OSDesktop performs real OS shell operations when TRACE_QUIET is not set.
type OSDesktop struct {
	fallback *MockDesktop
}

func NewDefaultDesktop() Desktop {
	if os.Getenv("TRACE_QUIET") != "" {
		return NewMockDesktop()
	}
	return &OSDesktop{fallback: NewMockDesktop()}
}

func startAndRelease(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}

func (d *OSDesktop) OpenExternalURL(rawURL string) error {
	clean, err := validateExternalURL(rawURL)
	if err != nil {
		return err
	}
	if os.Getenv("TRACE_QUIET") != "" {
		return d.fallback.OpenExternalURL(clean)
	}
	if runtime.GOOS == "windows" {
		return openExternalURLWindows(clean)
	}
	return d.fallback.OpenExternalURL(clean)
}

func (d *OSDesktop) OpenPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path")
	}
	if os.Getenv("TRACE_QUIET") != "" {
		return d.fallback.OpenPath(path)
	}
	if runtime.GOOS == "windows" {
		return startAndRelease("explorer.exe", path)
	}
	return d.fallback.OpenPath(path)
}

func (d *OSDesktop) RevealPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path")
	}
	if os.Getenv("TRACE_QUIET") != "" {
		return d.fallback.RevealPath(path)
	}
	if runtime.GOOS == "windows" {
		return startAndRelease("explorer.exe", "/select,", path)
	}
	return d.fallback.RevealPath(path)
}
