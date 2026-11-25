package appdirs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeWindowFile is an ephemeral marker inside DataDir recording whether
// the running TRACE process currently has a user-visible window open.
// tools/package -install reads it when cross-desktop Win32 EnumWindows cannot
// observe the interactive desktop (for example, from an isolated sandbox desktop).
const RuntimeWindowFile = "runtime-window.json"

// RuntimeWindowState is the JSON payload stored in RuntimeWindowFile.
type RuntimeWindowState struct {
	PID        int    `json:"pid"`
	WindowOpen bool   `json:"windowOpen"`
	UpdatedAt  string `json:"updatedAt"`
}

// WriteRuntimeWindowState atomically writes the running instance's window
// visibility marker inside dataDir. Empty dataDir or non-positive pid is a no-op.
func WriteRuntimeWindowState(dataDir string, pid int, windowOpen bool) error {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" || pid <= 0 {
		return nil
	}
	payload, err := json.Marshal(RuntimeWindowState{
		PID:        pid,
		WindowOpen: windowOpen,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	tmp, err := os.CreateTemp(dataDir, ".trace-runtime-window-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Chmod(0o600)
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := filepath.Join(dataDir, RuntimeWindowFile)
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

// ReadRuntimeWindowState reads and parses RuntimeWindowFile from dataDir.
// It returns ok=false when the file is absent, malformed, or lacks a valid PID.
func ReadRuntimeWindowState(dataDir string) (RuntimeWindowState, bool) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return RuntimeWindowState{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, RuntimeWindowFile))
	if err != nil {
		return RuntimeWindowState{}, false
	}
	var st RuntimeWindowState
	if err := json.Unmarshal(raw, &st); err != nil || st.PID <= 0 {
		return RuntimeWindowState{}, false
	}
	return st, true
}

// RemoveRuntimeWindowState removes RuntimeWindowFile if it belongs to pid.
func RemoveRuntimeWindowState(dataDir string, pid int) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" || pid <= 0 {
		return
	}
	st, ok := ReadRuntimeWindowState(dataDir)
	if !ok || st.PID != pid {
		return
	}
	_ = os.Remove(filepath.Join(dataDir, RuntimeWindowFile))
}
