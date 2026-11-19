// Package fakerclone is a stand-in for rclone in tests and smoke runs. A
// test binary (or tools/smoke) re-runs itself as "rclone" with
// TRACE_FAKE_RCLONE=1; nothing touches the network. Remote paths
// "<name>:<path>" map to TRACE_FAKE_RCLONE_ROOT/<path>.
//
// TRACE_FAKE_RCLONE_MODE: "" or "ok" copies; "fail" exits 1; "quota" exits 7
// with a quota message; "config" exits 1 with a config message; "slow"
// sleeps 2 s before copying. TRACE_FAKE_RCLONE_LOG, when set, gets one line
// per call: the command, the destination and the files copied.
package fakerclone

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Enabled reports whether this process should act as rclone.
func Enabled() bool { return os.Getenv("TRACE_FAKE_RCLONE") == "1" }

// Main acts as rclone with args and returns the exit code.
func Main(args []string) int {
	mode := os.Getenv("TRACE_FAKE_RCLONE_MODE")
	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "2026/11/19 10:00:00 ERROR : Attempt 1/3 failed with 1 errors and: something broke")
		return 1
	case "quota":
		fmt.Fprintln(os.Stderr, "2026/11/19 10:00:00 ERROR : video.mp4: Failed to copy: googleapi: Error 403: User rate limit exceeded., userRateLimitExceeded")
		return 7
	case "config":
		fmt.Fprintln(os.Stderr, `2026/11/19 10:00:00 Failed to create file system for "gdrive:trace": didn't find section in config file`)
		return 1
	case "slow":
		time.Sleep(2 * time.Second)
	}
	if len(args) == 0 {
		return 2
	}
	root := os.Getenv("TRACE_FAKE_RCLONE_ROOT")
	switch args[0] {
	case "copy":
		if len(args) < 3 {
			return 2
		}
		src, dst := args[1], mapRemote(root, args[2])
		var list string
		for i := 3; i < len(args); i++ {
			if (args[i] == "--files-from" || args[i] == "--files-from-raw") && i+1 < len(args) {
				list = args[i+1]
			}
		}
		var names []string
		if list != "" {
			f, err := os.Open(list)
			if err != nil {
				return 1
			}
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if n := strings.TrimSpace(sc.Text()); n != "" {
					names = append(names, n)
				}
			}
			_ = f.Close()
		} else {
			_ = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(src, p)
					names = append(names, filepath.ToSlash(rel))
				}
				return nil
			})
		}
		for _, n := range names {
			if err := copyFile(filepath.Join(src, filepath.FromSlash(n)), filepath.Join(dst, filepath.FromSlash(n))); err != nil {
				fmt.Fprintln(os.Stderr, "ERROR : "+n+": can't copy - source file is being updated")
				return 1
			}
		}
		logCall("copy", args[2], names)
		return 0
	case "deletefile":
		if len(args) < 2 {
			return 2
		}
		_ = os.Remove(mapRemote(root, args[1]))
		logCall("deletefile", args[1], nil)
		return 0
	case "version":
		fmt.Println("rclone v0.0.0-fake")
		return 0
	}
	return 2
}

func mapRemote(root, remote string) string {
	_, p, ok := strings.Cut(remote, ":")
	if !ok {
		p = remote
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "/")))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func logCall(cmd, dst string, names []string) {
	path := os.Getenv("TRACE_FAKE_RCLONE_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s %s\n", cmd, dst, strings.Join(names, ","))
}
