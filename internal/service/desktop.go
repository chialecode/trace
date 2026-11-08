package service

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// Desktop abstracts OS shell actions (opening URLs, folders, or revealing files).
// Automated tests, prototypes, uishots, and smoke runs use MockDesktop or TRACE_QUIET
// so no external browser or Explorer window is ever launched (§6.4).
type Desktop interface {
	OpenExternalURL(rawURL string) error
	OpenPath(path string) error
	RevealPath(path string) error
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
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("empty url")
	}
	m.mu.Lock()
	m.actions = append(m.actions, RecordedAction{Kind: "url", Target: rawURL})
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

func (d *OSDesktop) OpenExternalURL(rawURL string) error {
	if os.Getenv("TRACE_QUIET") != "" {
		return d.fallback.OpenExternalURL(rawURL)
	}
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", rawURL).Start()
	}
	return d.fallback.OpenExternalURL(rawURL)
}

func (d *OSDesktop) OpenPath(path string) error {
	if os.Getenv("TRACE_QUIET") != "" {
		return d.fallback.OpenPath(path)
	}
	if runtime.GOOS == "windows" {
		return exec.Command("explorer.exe", path).Start()
	}
	return d.fallback.OpenPath(path)
}

func (d *OSDesktop) RevealPath(path string) error {
	if os.Getenv("TRACE_QUIET") != "" {
		return d.fallback.RevealPath(path)
	}
	if runtime.GOOS == "windows" {
		return exec.Command("explorer.exe", "/select,", path).Start()
	}
	return d.fallback.RevealPath(path)
}
