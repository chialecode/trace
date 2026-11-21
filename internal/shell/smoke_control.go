package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/storage"
	"github.com/egoist/mygo"
)

// smokeDirForChannel returns the smoke-test control directory only on non-release channels.
func smokeDirForChannel(channel string) string {
	if isReleaseChannel(channel) {
		return ""
	}
	return strings.TrimSpace(os.Getenv("TRACE_SMOKE_DIR"))
}

func (s *Shell) runSmokeControlLoop(smokeDir string) {
	_ = os.MkdirAll(smokeDir, 0o700)
	writeSnap := func(name string) {
		snap := s.Snapshot()
		if raw, err := json.MarshalIndent(snap, "", "  "); err == nil {
			_ = storage.WriteFileAtomic(filepath.Join(smokeDir, name), raw, 0o600)
		}
	}

	writeSnap("ready.json")
	cmdFile := filepath.Join(smokeDir, "command.txt")
	lastCmd := ""

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		quitting := s.quitting
		s.mu.Unlock()
		if quitting {
			return
		}
		raw, err := os.ReadFile(cmdFile)
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(string(raw))
		if cmd == "" || cmd == lastCmd {
			continue
		}
		lastCmd = cmd

		switch cmd {
		case "close_to_tray":
			done := make(chan struct{})
			mygo.RunOnMain(func() {
				s.CloseToTray()
				close(done)
			})
			<-done
			// CloseToTray already collects and trims; measure after it settles.
			time.Sleep(2 * time.Second)
			writeSnap("tray_state.json")

		case "restore_window":
			done := make(chan struct{})
			mygo.RunOnMain(func() {
				s.RestoreFromTray()
				close(done)
			})
			<-done
			time.Sleep(50 * time.Millisecond)
			writeSnap("restored_state.json")

		case "snapshot":
			writeSnap("snapshot.json")

		case "quit":
			writeSnap("final_state.json")
			mygo.RunOnMain(func() {
				s.RequestQuit(true)
			})
			return
		}
	}
}
