package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site"
)

// DiagnosticsSnapshot returns a sanitized runtime summary without any secret values.
func (b *Backend) DiagnosticsSnapshot() Diagnostics {
	stats, indexErr := b.lib.Statistics()
	tasks, tasksErr := b.dl.ListTasksSummary()
	subs, subsErr := b.subs.List()
	rooms := b.live.ListRooms()

	b.logMu.Lock()
	corrupt := append([]string(nil), b.corruptFiles...)
	b.logMu.Unlock()

	indexStatus := fmt.Sprintf("正常（已索引 %d 项）", stats.Items)
	if indexErr != nil {
		indexStatus = "读取索引失败：" + b.sanitizeLogLine(indexErr.Error())
	} else if len(corrupt) > 0 {
		indexStatus = fmt.Sprintf("已保留 %d 个损坏文件，当前已索引 %d 项", len(corrupt), stats.Items)
	}
	subCheckStatus := fmt.Sprintf("空闲（共 %d 个订阅）", len(subs))
	if b.subs != nil {
		if progress, checking := b.subs.CheckProgress(); checking {
			subCheckStatus = fmt.Sprintf("正在检查更新 %d / %d", progress.CheckedCreators, progress.TotalCreators)
		} else {
			var backoffParts []string
			for _, id := range site.All() {
				if inBackoff, until := b.subs.IsSiteInBackoff(string(id)); inBackoff {
					backoffParts = append(backoffParts, fmt.Sprintf("%s至 %s", id.DisplayName(), until.Local().Format("15:04")))
				}
			}
			if len(backoffParts) > 0 {
				subCheckStatus = "风控退避中（暂停 " + strings.Join(backoffParts, " · ") + "）"
			}
		}
	}
	if subsErr != nil {
		subCheckStatus = "读取订阅失败：" + b.sanitizeLogLine(subsErr.Error())
	}
	var runningTasks, queuedTasks, autoTasks int
	for _, t := range tasks {
		switch t.Status {
		case "running":
			runningTasks++
		case "queued":
			queuedTasks++
		}
		if t.Source == "auto" && (t.Status == "running" || t.Status == "queued") {
			autoTasks++
		}
	}
	autoCacheStatus := "空闲"
	if runningTasks > 0 || queuedTasks > 0 {
		autoCacheStatus = fmt.Sprintf("进行中 %d · 排队 %d（自动缓存 %d）", runningTasks, queuedTasks, autoTasks)
	}
	if tasksErr != nil {
		autoCacheStatus = "读取任务失败：" + b.sanitizeLogLine(tasksErr.Error())
	}
	var recordingRooms, liveRooms int
	for _, r := range rooms {
		if r.LiveStatus == 1 {
			liveRooms++
		}
		if r.Recording {
			recordingRooms++
		}
	}
	liveRecordStatus := "空闲"
	if recordingRooms > 0 || liveRooms > 0 {
		liveRecordStatus = fmt.Sprintf("录制中 %d 路 · 直播中 %d 个房间", recordingRooms, liveRooms)
	}

	build := appdirs.BuildInfo()
	return Diagnostics{
		Version:                 build.Version,
		Commit:                  build.Commit,
		BuildTime:               build.BuildTime,
		AppName:                 b.loc.AppName,
		DisplayName:             b.loc.DisplayName,
		Channel:                 b.loc.Channel,
		Mode:                    b.loc.Mode,
		DataDir:                 b.loc.DataDir,
		CacheDir:                b.lib.CacheDir(),
		LogsDir:                 b.loc.LogsDir,
		Account:                 b.cred.Status(),
		Accounts:                b.Statuses(),
		LibraryCount:            stats.Items,
		TaskCount:               len(tasks),
		SubscriptionCnt:         len(subs),
		LiveRoomCount:           len(rooms),
		IndexStatus:             indexStatus,
		SubscriptionCheckStatus: subCheckStatus,
		AutoCacheStatus:         autoCacheStatus,
		LiveRecordStatus:        liveRecordStatus,
		CorruptRecovered:        corrupt,
		RecentEvents:            b.events.Recent(),
	}
}

const runtimeLogReadLimit = 1 << 20

// RecentLogs reads at most 1 MiB from the end of trace.log and returns at most
// 500 sanitized lines. An incomplete oldest line is discarded.
func (b *Backend) RecentLogs(maxLines int) ([]string, error) {
	if maxLines <= 0 || maxLines > 500 {
		maxLines = 500
	}
	if b.logPath == "" {
		return nil, nil
	}
	b.logMu.Lock()
	defer b.logMu.Unlock()
	f, err := os.Open(b.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("运行日志不是普通文件")
	}
	start := max(int64(0), info.Size()-runtimeLogReadLimit)
	raw := make([]byte, info.Size()-start)
	n, err := f.ReadAt(raw, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	raw = raw[:n]
	if start > 0 {
		if newline := bytes.IndexByte(raw, '\n'); newline >= 0 {
			raw = raw[newline+1:]
		} else {
			raw = nil
		}
	}
	rawLines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var out []string
	for _, ln := range rawLines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		out = append(out, b.sanitizeLogLine(ln))
	}
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	return out, nil
}

// secretKeyNamesPattern is the single source of truth for secret parameter and
// cookie field names redacted across runtime logs and persisted queue text.
const secretKeyNamesPattern = `cookie|authorization|sessdata|bili_jct|dedeuserid|dedeuserid__ckmd5|sid|buvid3|buvid4|b_nut|ac_time_value|refresh_token|pending_old_refresh_token|access_key|auth_key|csrf|qrcode_key|token|sign|x-expires|w_rid|upsig|wssecret|wstime|deadline|bwsv|sessionid|sessionid_ss|sid_tt|uid_tt|uid_tt_ss|sid_guard|odin_tt|passport_csrf_token|passport_csrf_token_default|ttwid|uifid|uifid_temp|mstoken|s_v_web_id|__ac_nonce|__ac_signature|a_bogus|x-bogus|x-signature|x-secsdk-web-signature|x-secsdk-csrf-token|sub|subp|xsrf-token|x-xsrf-token|wbpsess|scf|alf|alc|ssologinstate|suhb|pc_token|_t_wm|ssig|kid|expires|auth_token|ct0|twid|guest_id|kdt|x-csrf-token|x-client-transaction-id|bearer`

var (
	runtimeLogSecretField   = regexp.MustCompile(`(?i)\b(?:` + secretKeyNamesPattern + `)["'\\]*\s*[=:]|[?&]e=|\btrace-remote:\S+|\bbearer\s+\S+`)
	runtimeLogURL           = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)
	runtimeLogPercentEscape = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)
)

func (b *Backend) redactStoredSecrets(s string) string {
	if b.credMgr != nil {
		s = b.credMgr.RedactString(s)
	} else if b.cred != nil {
		s = b.cred.RedactString(s)
	}
	if b.remote != nil {
		if tok := b.remote.activeToken(); tok != "" {
			s = strings.ReplaceAll(s, tok, "[REDACTED]")
		}
	}
	return s
}

func (b *Backend) sanitizeLogLine(msg string) string {
	sanitized := b.redactStoredSecrets(msg)
	decoded := sanitized
	for i := 0; i < 3; i++ {
		if runtimeLogSecretField.MatchString(decoded) {
			return "[REDACTED_LOG_LINE]"
		}
		for _, rawURL := range runtimeLogURL.FindAllString(decoded, -1) {
			if strings.Contains(rawURL, "?") {
				return "[REDACTED_LOG_LINE]"
			}
		}
		if b.redactStoredSecrets(decoded) != decoded {
			return "[REDACTED_LOG_LINE]"
		}
		next, err := url.QueryUnescape(decoded)
		if err != nil {
			// A progress value such as "50%" must not prevent redaction of an
			// encoded credential elsewhere on the same line.
			next = runtimeLogPercentEscape.ReplaceAllStringFunc(decoded, func(escape string) string {
				value, _ := url.QueryUnescape(escape)
				return value
			})
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	return sanitized
}

// Logf writes an informational line to trace.log.
func (b *Backend) Logf(format string, args ...any) {
	b.logInfo(fmt.Sprintf(format, args...))
}

func (b *Backend) logInfo(msg string) {
	b.writeLogLine("INFO", msg)
}

func (b *Backend) logError(op string, err error) {
	if err == nil {
		return
	}
	b.writeLogLine("ERROR", op+": "+err.Error()+siteErrorDetail(err))
}

// LogUserError logs an error the UI displayed. The same line within a minute
// is written once, so a page that keeps failing does not flood the log.
func (b *Backend) LogUserError(err error) {
	if err == nil {
		return
	}
	line := "ui: " + err.Error() + siteErrorDetail(err)
	now := time.Now()
	b.logMu.Lock()
	repeated := line == b.lastUserError && now.Sub(b.lastUserErrorAt) < time.Minute
	if !repeated {
		b.lastUserError, b.lastUserErrorAt = line, now
	}
	b.logMu.Unlock()
	if !repeated {
		b.writeLogLine("ERROR", line)
	}
}

// siteErrorDetail adds what the user-facing message leaves out: the endpoint
// path (never its query, which can carry signatures) and, for a response that
// did not match the expected fields, the decoder's reason (field and type).
func siteErrorDetail(err error) string {
	var se *site.Error
	if !errors.As(err, &se) {
		return ""
	}
	var detail string
	if se.Endpoint != "" {
		if u, perr := url.Parse(se.Endpoint); perr == nil && u.Path != "" {
			detail += " endpoint=" + u.Path
		}
	}
	if se.Kind == site.ErrKindSchemaChanged && se.Err != nil {
		cause := []rune(se.Err.Error())
		if len(cause) > 240 { // Go prints a whole anonymous struct type here
			cause = append(cause[:240], '…')
		}
		detail += " cause=" + string(cause)
	}
	return detail
}

func (b *Backend) writeLogLine(level, msg string) {
	if b.logPath == "" {
		return
	}
	sanitized := b.sanitizeLogLine(msg)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339), level, sanitized)

	b.logMu.Lock()
	defer b.logMu.Unlock()
	if level == "ERROR" {
		b.recentErrors = append(b.recentErrors, RecentError{At: time.Now().UTC().Format(time.RFC3339), Message: sanitized})
		if len(b.recentErrors) > maxRecentErrors {
			b.recentErrors = b.recentErrors[len(b.recentErrors)-maxRecentErrors:]
		}
	}
	if b.logMirror != nil {
		_, _ = io.WriteString(b.logMirror, line)
	}
	f, err := os.OpenFile(b.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// taskStatusLog remembers the last logged status of each download task.
// Progress updates arrive about once a second while a task runs; the log
// records only status changes so it stays readable.
type taskStatusLog struct {
	mu   sync.Mutex
	last map[string]string
}

func newTaskStatusLog() *taskStatusLog {
	return &taskStatusLog{last: map[string]string{}}
}

// changed reports whether status differs from the last one logged for id.
func (l *taskStatusLog) changed(id, status string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last[id] == status {
		return false
	}
	if status == "completed" || status == "canceled" {
		delete(l.last, id)
	} else {
		l.last[id] = status
	}
	return true
}
