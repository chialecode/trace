package main

import (
	"time"
)

// rehearsalInput is what the ptest rehearsal needs from the release.
type rehearsalInput struct {
	Root    string
	Base    []byte
	Version string
	Commit  string
	Build   func(root, out, ldflags string, env []string) (string, error)
	Logf    func(format string, args ...any)
}

// rehearsalStep is one checked step of the rehearsal.
type rehearsalStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Ms     int64  `json:"ms"`
}

// rehearsalResult is artifacts/release/rehearsal.json.
type rehearsalResult struct {
	Status      string          `json:"status"` // passed, failed, not-run
	Reason      string          `json:"reason,omitempty"`
	Identity    string          `json:"identity"`
	OldVersion  string          `json:"oldVersion"`
	NewVersion  string          `json:"newVersion"`
	Tampered    string          `json:"tamperedVersion"`
	Commit      string          `json:"commit"`
	Steps       []rehearsalStep `json:"steps"`
	Requests    []string        `json:"requests"`
	OthersSame  bool            `json:"releaseAndDevUntouched"`
	At          string          `json:"at"`
	ElapsedMs   int64           `json:"elapsedMs"`
	InstallDir  string          `json:"installDir,omitempty"`
	DataKept    bool            `json:"dataKept"`
	InstallGone bool            `json:"installDirRemoved"`
}

func newRehearsalResult(in rehearsalInput) rehearsalResult {
	return rehearsalResult{
		Identity:   "io.github.chialecode.trace-ptest",
		OldVersion: in.Version + "-rehearsal.1",
		NewVersion: in.Version + "-rehearsal.2",
		Tampered:   in.Version + "-rehearsal.3",
		Commit:     in.Commit,
		At:         time.Now().UTC().Format(time.RFC3339),
	}
}
