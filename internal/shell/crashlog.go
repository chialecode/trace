package shell

import (
	"fmt"
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
// to <logs>/crash.log. A non-empty crash.log left by the previous run is
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
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		base := "crash-" + info.ModTime().UTC().Format(crashLogStamp)
		renamed := false
		for i := 0; i < 10; i++ {
			name := base + ".log"
			if i > 0 {
				name = fmt.Sprintf("%s-%d.log", base, i)
			}
			dst := filepath.Join(logsDir, name)
			if _, statErr := os.Stat(dst); statErr == nil {
				continue
			}
			if os.Rename(path, dst) == nil {
				previous = name
				renamed = true
				break
			}
		}
		if !renamed {
			// Avoid O_TRUNC if rotation failed so the previous crash traceback is preserved.
			flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		pruneCrashLogs(logsDir)
	}
	f, err := os.OpenFile(path, flags, 0o600)
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
