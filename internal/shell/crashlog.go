package shell

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
)

const (
	crashLogName  = "crash.log"
	crashLogKeep  = 5
	crashLogGlob  = "crash-*.log"
	crashLogStamp = "20060102T150405Z"
)

// setupCrashOutput makes the Go runtime write the traceback of a fatal crash
// to <logs>/crash.log (2026-11-15 log review: three restarts on 11-14 left
// nothing in trace.log). A non-empty crash.log left by the previous run is
// kept as crash-<time>.log (newest five) and its name returned so the
// backend can note it in trace.log. The file stays open for the process.
func setupCrashOutput(logsDir string) (previous string) {
	if strings.TrimSpace(logsDir) == "" {
		return ""
	}
	path := filepath.Join(logsDir, crashLogName)
	// Release an earlier crash output of this process (tests call this twice);
	// Windows cannot rename a file that is still open.
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		name := "crash-" + info.ModTime().UTC().Format(crashLogStamp) + ".log"
		if os.Rename(path, filepath.Join(logsDir, name)) == nil {
			previous = name
		}
		pruneCrashLogs(logsDir)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return previous
	}
	if debug.SetCrashOutput(f, debug.CrashOptions{}) != nil {
		_ = f.Close()
		return previous
	}
	// SetCrashOutput duplicated the descriptor; this copy is not needed.
	_ = f.Close()
	return previous
}

// pruneCrashLogs keeps only the newest crashLogKeep crash-<time>.log files.
// The stamp sorts by time, and only files matching the pattern are removed.
func pruneCrashLogs(logsDir string) {
	matches, err := filepath.Glob(filepath.Join(logsDir, crashLogGlob))
	if err != nil || len(matches) <= crashLogKeep {
		return
	}
	slices.Sort(matches)
	for _, old := range matches[:len(matches)-crashLogKeep] {
		_ = os.Remove(old)
	}
}
