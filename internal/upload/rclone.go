package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/storage"
)

// rcloneError is a failed rclone run, classified.
type rcloneError struct {
	kind string
	msg  string
}

func (e *rcloneError) Error() string { return e.msg }

func (u *Uploader) tempDir() string {
	dir := filepath.Join(u.opt.DataDir, "upload")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// remoteJoin joins a normalized rclone remote ("gdrive:" or "gdrive:trace")
// with a relative slash path without producing "gdrive:/path".
func remoteJoin(base, rel string) string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return base
	}
	if strings.HasSuffix(base, ":") {
		return base + rel
	}
	return strings.TrimRight(base, "/") + "/" + rel
}

// writeFilesFrom writes names (excluding unfinished temp files) to a temporary
// --files-from-raw list file and returns its path and count of entries written.
func writeFilesFrom(dir string, names []string) (string, int, error) {
	list, err := os.CreateTemp(dir, "files-*.txt")
	if err != nil {
		return "", 0, err
	}
	listPath := list.Name()
	written := 0
	for _, n := range names {
		if storage.IsTempName(path.Base(n)) {
			continue // never an unfinished file
		}
		if _, err := io.WriteString(list, n+"\n"); err != nil {
			_ = list.Close()
			_ = os.Remove(listPath)
			return "", 0, err
		}
		written++
	}
	if err := list.Close(); err != nil {
		_ = os.Remove(listPath)
		return "", 0, err
	}
	return listPath, written, nil
}

// copy runs `rclone copy <src> <remote>/<relDir> --files-from-raw <list>`.
func (u *Uploader) copy(ctx context.Context, src, relDir string, names []string) error {
	listPath, written, err := writeFilesFrom(u.tempDir(), names)
	if err != nil {
		return &rcloneError{kind: remote.UploadErrOther, msg: err.Error()}
	}
	defer os.Remove(listPath)
	if written == 0 {
		return nil
	}
	dst := remoteJoin(u.cfg.Remote, relDir)
	args := []string{"copy", src, dst, "--files-from-raw", listPath}
	args = append(args, u.commonArgs()...)
	return u.run(ctx, args)
}

func (u *Uploader) commonArgs() []string {
	var args []string
	if strings.TrimSpace(u.cfg.Config) != "" {
		args = append(args, "--config", u.cfg.Config)
	}
	return append(args, u.cfg.ExtraArgs...)
}

func (u *Uploader) program() (string, error) {
	name := strings.TrimSpace(u.cfg.Rclone)
	if name == "" {
		name = "rclone"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", &rcloneError{kind: remote.UploadErrNotFound, msg: "找不到 rclone：请安装 rclone，或在 server.json 的 upload.rclone 写明路径"}
	}
	return p, nil
}

func (u *Uploader) run(ctx context.Context, args []string) error {
	prog, err := u.program()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, prog, args...)
	if u.opt.Env != nil {
		cmd.Env = u.opt.Env
	}
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: 64 << 10}
	err = cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return classify(stderr.String(), err)
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

var (
	secretLike  = regexp.MustCompile(`[A-Za-z0-9_\-./+=]{24,}`)
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@"']+?@`)
	urlQuery    = regexp.MustCompile(`(https?://[^\s?"']+)\?[^\s"']*`)
)

// Sanitize keeps the first line of rclone's error output without URL userinfo,
// query strings or token-like strings (remote.md §2: logs never hold rclone's
// raw output).
func Sanitize(stderr string) string {
	line := ""
	for _, l := range strings.Split(strings.ReplaceAll(stderr, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.Contains(strings.ToUpper(l), "ERROR") || line == "" {
			line = l
			if strings.Contains(strings.ToUpper(l), "ERROR") {
				break
			}
		}
	}
	line = urlUserinfo.ReplaceAllString(line, "$1…@")
	line = urlQuery.ReplaceAllString(line, "$1?…")
	line = secretLike.ReplaceAllString(line, "…")
	if r := []rune(line); len(r) > 200 {
		line = string(r[:200]) + "…"
	}
	return line
}

func classify(stderr string, runErr error) error {
	low := strings.ToLower(stderr)
	code := -1
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		code = ee.ExitCode()
	}
	first := Sanitize(stderr)
	switch {
	case strings.Contains(low, "quota") || strings.Contains(low, "ratelimit") || strings.Contains(low, "rate limit") || strings.Contains(low, "upload limit") || strings.Contains(low, "userratelimitexceeded") || strings.Contains(low, "too many requests"):
		return &rcloneError{kind: remote.UploadErrQuota, msg: "网盘配额或限流（如 Google Drive 每天 750 GiB 上传上限），稍后自动重试：" + first}
	case strings.Contains(low, "didn't find section in config") || strings.Contains(low, "config file") || strings.Contains(low, "not found in config") || strings.Contains(low, "couldn't find") || strings.Contains(low, "invalid_grant") || strings.Contains(low, "unauthorized") || strings.Contains(low, "token"):
		return &rcloneError{kind: remote.UploadErrConfig, msg: "rclone 配置或网盘授权有问题，请在远程运行 rclone config 检查：" + first}
	case strings.Contains(low, "no such host") || strings.Contains(low, "connection") || strings.Contains(low, "timeout") || strings.Contains(low, "network") || strings.Contains(low, "i/o timeout") || code == 5:
		return &rcloneError{kind: remote.UploadErrNetwork, msg: "网络问题，稍后自动重试：" + first}
	default:
		return &rcloneError{kind: remote.UploadErrOther, msg: fmt.Sprintf("rclone 退出码 %d：%s", code, first)}
	}
}
