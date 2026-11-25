package update

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Schedule of the automatic checks: once a minute after launch,
// then every 24 hours while TRACE runs.
const (
	FirstCheckDelay = time.Minute
	CheckInterval   = 24 * time.Hour
)

// Release is a newer version that a check found.
type Release struct {
	Version string
	Notes   string
	Date    time.Time
}

// Engine checks for and installs updates: mygo.Updater in the app, a fake
// in tests. Install installs the release of the last Check.
type Engine interface {
	Enabled() bool
	Check(ctx context.Context) (*Release, error)
	Install(ctx context.Context, progress func(downloaded, total int64)) error
}

// Phase is where the update of the running TRACE stands.
type Phase string

const (
	PhaseDisabled    Phase = "disabled"    // a build without updates (development, trace-test without a source)
	PhaseIdle        Phase = "idle"        // up to date, or not checked yet
	PhaseChecking    Phase = "checking"    // a check is running
	PhaseAvailable   Phase = "available"   // a newer version is there, not downloaded (automatic download is off)
	PhaseDownloading Phase = "downloading" // downloading and verifying it
	PhaseReady       Phase = "ready"       // installed for the next launch
)

// User-facing messages.
const (
	MsgRejected    = "更新包校验失败，已保留当前版本"
	MsgUpToDate    = "已是最新版本"
	MsgDevChannel  = "开发版由本机打包自动更新"
	msgCheckFailed = "检查更新失败"
)

// Prefs are the user's update preferences (settings.json).
type Prefs struct {
	AutoCheck    bool
	AutoDownload bool
}

// State is what the interface shows.
type State struct {
	Enabled        bool
	Channel        string
	CurrentVersion string
	Phase          Phase
	Release        *Release // the newer version, when Phase is available, downloading or ready
	Downloaded     int64
	Total          int64
	LastCheck      time.Time
	// NextCheck is when the next automatic check is scheduled, or zero when
	// automatic checks are off, disabled, or an update is already ready.
	NextCheck time.Time
	// LastCheckResult is the sanitized outcome of the most recent check
	// (automatic or manual), shown in Settings -> 更新.
	LastCheckResult string
	// Message is the result of the last manual check or of a download the
	// user started, shown beside the button; automatic checks stay silent.
	Message      string
	MessageError bool
	// BannerDismissed hides the "ready" line for the rest of this run.
	BannerDismissed bool
	// UpdatedTo and UpdatedNotes say, once after an update, which version
	// now runs and what changed.
	UpdatedTo    string
	UpdatedNotes string
	Prefs        Prefs
}

// Options configure a Controller.
type Options struct {
	Engine         Engine
	Channel        string
	CurrentVersion string
	Prefs          func() Prefs
	Now            func() time.Time
	// LastCheck is the time of the last check of an earlier run, shown
	// until this run checks.
	LastCheck time.Time
	// LastCheckResult is the sanitized result of the last check of an
	// earlier run, shown until this run checks.
	LastCheckResult string
	// OnChange is called after every change of State.
	OnChange func()
	// Logf writes a line to TRACE's log.
	Logf func(format string, args ...any)
	// Saved records a check time and the notes of a downloaded release,
	// so that the next launch can say what changed.
	SaveLastCheck func(time.Time)
	SaveReady     func(version, notes string)
	// RecordCheck appends one completed check to update-history.json.
	RecordCheck func(CheckRecord)
	// Restart quits TRACE as the tray's Quit does and starts the new
	// version.
	Restart func()
	// ActiveWork counts the cache tasks a restart pauses and the
	// recordings it ends.
	ActiveWork func() (tasks, recordings int)
	// FirstDelay replaces FirstCheckDelay (the trace-test identity's rehearsal
	// checks a few seconds after launch).
	FirstDelay time.Duration
}

// Controller runs the update checks of one TRACE process.
type Controller struct {
	opts Options

	mu        sync.Mutex
	state     State
	started   time.Time
	lastRun   time.Time // last check of this run, automatic or manual
	busy      bool
	wake      chan struct{}
	installed bool
}

// NewController returns a controller; Start begins the automatic checks.
func NewController(opts Options) *Controller {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Prefs == nil {
		opts.Prefs = func() Prefs { return Prefs{AutoCheck: true, AutoDownload: true} }
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	enabled := opts.Engine != nil && opts.Engine.Enabled()
	phase := PhaseIdle
	if !enabled {
		phase = PhaseDisabled
	}
	c := &Controller{
		opts:    opts,
		started: opts.Now(),
		wake:    make(chan struct{}, 1),
		state: State{
			Enabled:         enabled,
			Channel:         opts.Channel,
			CurrentVersion:  opts.CurrentVersion,
			Phase:           phase,
			LastCheck:       opts.LastCheck,
			LastCheckResult: SanitizeHistoryText(opts.LastCheckResult, nil),
			Prefs:           opts.Prefs(),
		},
	}
	return c
}

// SetUpdatedNotice makes the interface say once that TRACE was updated.
func (c *Controller) SetUpdatedNotice(version, notes string) {
	c.mu.Lock()
	c.state.UpdatedTo, c.state.UpdatedNotes = version, notes
	c.mu.Unlock()
	c.changed()
}

// DismissUpdatedNotice hides the "updated to" line.
func (c *Controller) DismissUpdatedNotice() {
	c.mu.Lock()
	c.state.UpdatedTo, c.state.UpdatedNotes = "", ""
	c.mu.Unlock()
	c.changed()
}

// State returns a copy of the current state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state
	st.Prefs = c.opts.Prefs()
	if next, ok := c.dueAtLocked(); ok {
		st.NextCheck = next
	}
	if st.Release != nil {
		r := *st.Release
		st.Release = &r
	}
	return st
}

// Start runs the automatic checks until ctx ends.
func (c *Controller) Start(ctx context.Context) {
	if !c.State().Enabled {
		return
	}
	go func() {
		for {
			wait := c.untilNextCheck(c.opts.Now())
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-c.wake:
				t.Stop()
			case <-t.C:
			}
			c.Tick(ctx)
		}
	}()
}

// PrefsChanged re-evaluates the schedule after the user changed a setting.
func (c *Controller) PrefsChanged() {
	c.poke()
	c.changed()
}

func (c *Controller) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// DueAt returns when the next automatic check is due, and false when none
// is (automatic checks off, or the update is already downloaded).
func (c *Controller) DueAt() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dueAtLocked()
}

func (c *Controller) dueAtLocked() (time.Time, bool) {
	if !c.state.Enabled || !c.opts.Prefs().AutoCheck || c.installed {
		return time.Time{}, false
	}
	if c.lastRun.IsZero() {
		if c.opts.FirstDelay > 0 {
			return c.started.Add(c.opts.FirstDelay), true
		}
		return c.started.Add(FirstCheckDelay), true
	}
	return c.lastRun.Add(CheckInterval), true
}

func (c *Controller) untilNextCheck(now time.Time) time.Duration {
	c.mu.Lock()
	busy := c.busy
	due, ok := c.dueAtLocked()
	c.mu.Unlock()
	if !ok {
		return time.Hour
	}
	if d := due.Sub(now); d > 0 {
		return d
	}
	if busy {
		// Wait for the in-flight manual check or download to finish and poke
		// c.wake, rather than spinning on a zero-duration timer.
		return 5 * time.Second
	}
	return 0
}

// Tick runs the automatic check when it is due.
func (c *Controller) Tick(ctx context.Context) {
	due, ok := c.DueAt()
	if !ok || c.opts.Now().Before(due) {
		return
	}
	c.check(ctx, false)
}

// CheckNow checks at once, for the "检查更新" button; the result is shown.
func (c *Controller) CheckNow(ctx context.Context) {
	if !c.State().Enabled {
		c.mu.Lock()
		c.state.Message, c.state.MessageError = MsgDevChannel, false
		c.mu.Unlock()
		c.changed()
		return
	}
	c.check(ctx, true)
}

func (c *Controller) check(ctx context.Context, manual bool) {
	c.mu.Lock()
	if c.busy || c.installed {
		notify := false
		if manual && c.installed && c.state.Release != nil {
			c.state.Message, c.state.MessageError = fmt.Sprintf("新版本 %s 已就绪，重启以更新", c.state.Release.Version), false
			notify = true
		}
		c.mu.Unlock()
		if notify {
			c.changed()
		}
		return
	}
	c.busy = true
	prev := c.state.Phase
	c.state.Phase = PhaseChecking
	if manual {
		c.state.Message, c.state.MessageError = "", false
	}
	c.mu.Unlock()
	c.changed()

	rel, err := c.opts.Engine.Check(ctx)
	now := c.opts.Now()
	checkResult := FormatCheckResult(rel, err)

	c.mu.Lock()
	c.lastRun = now
	c.state.LastCheck = now
	c.state.LastCheckResult = checkResult
	autoDownload := c.opts.Prefs().AutoDownload
	startDownload := false
	var downloadRel Release
	switch {
	case err != nil:
		c.state.Phase = prev
		if c.state.Phase == PhaseChecking || c.state.Phase == PhaseDownloading {
			c.state.Phase = PhaseIdle
		}
		c.opts.Logf("update check failed: %v", Describe(err))
		if manual {
			c.state.Message, c.state.MessageError = Describe(err), true
		}
	case rel == nil:
		c.state.Phase = PhaseIdle
		c.state.Release = nil
		c.opts.Logf("update check: up to date (%s)", c.opts.CurrentVersion)
		if manual {
			c.state.Message, c.state.MessageError = MsgUpToDate, false
		}
	default:
		r := *rel
		c.state.Release = &r
		c.opts.Logf("update check: %s is available", r.Version)
		if autoDownload {
			startDownload = true
			downloadRel = r
			c.state.Phase = PhaseDownloading
		} else {
			c.state.Phase = PhaseAvailable
			if manual {
				c.state.Message, c.state.MessageError = fmt.Sprintf("有新版本 %s", r.Version), false
			}
		}
	}
	if !startDownload {
		c.busy = false
		c.poke()
	}
	c.mu.Unlock()
	if c.opts.SaveLastCheck != nil {
		c.opts.SaveLastCheck(now)
	}
	if c.opts.RecordCheck != nil {
		trigger := CheckTriggerAuto
		if manual {
			trigger = CheckTriggerManual
		}
		c.opts.RecordCheck(CheckRecord{
			At:      now.UTC().Format(time.RFC3339),
			Trigger: trigger,
			Result:  checkResult,
		})
	}
	c.changed()
	if startDownload {
		c.install(ctx, downloadRel, manual)
	}
}

// Download downloads and installs the available version, for the
// "下载并安装" button when automatic download is off.
func (c *Controller) Download(ctx context.Context) {
	c.mu.Lock()
	if c.busy || c.state.Phase != PhaseAvailable || c.state.Release == nil {
		c.mu.Unlock()
		return
	}
	c.busy = true
	rel := *c.state.Release
	c.state.Phase = PhaseDownloading
	c.state.Message, c.state.MessageError = "", false
	c.mu.Unlock()
	c.changed()
	c.install(ctx, rel, true)
}

// install runs Engine.Install for rel. Callers must already hold c.busy = true.
func (c *Controller) install(ctx context.Context, rel Release, report bool) {
	c.mu.Lock()
	c.state.Downloaded, c.state.Total = 0, 0
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.busy {
			c.busy = false
			c.poke()
		}
		c.mu.Unlock()
	}()
	last := time.Time{}
	err := c.opts.Engine.Install(ctx, func(done, total int64) {
		c.mu.Lock()
		c.state.Downloaded, c.state.Total = done, total
		c.mu.Unlock()
		if now := time.Now(); now.Sub(last) > 200*time.Millisecond || done == total {
			last = now
			c.changed()
		}
	})
	c.mu.Lock()
	c.busy = false
	if err != nil {
		// The current version stays; the next check tries again.
		c.state.Phase = PhaseAvailable
		c.opts.Logf("update %s not installed: %s", rel.Version, Describe(err))
		if report || IsRejection(err) {
			c.state.Message, c.state.MessageError = Describe(err), true
		}
	} else {
		c.installed = true
		c.state.Phase = PhaseReady
		c.state.BannerDismissed = false
		c.opts.Logf("update %s is ready for the next launch", rel.Version)
		if report {
			c.state.Message, c.state.MessageError = fmt.Sprintf("新版本 %s 已就绪", rel.Version), false
		}
	}
	c.poke()
	c.mu.Unlock()
	if err == nil && c.opts.SaveReady != nil {
		c.opts.SaveReady(rel.Version, rel.Notes)
	}
	c.changed()
}

// DismissBanner hides the "ready" line for the rest of this run.
func (c *Controller) DismissBanner() {
	c.mu.Lock()
	c.state.BannerDismissed = true
	c.mu.Unlock()
	c.changed()
}

// RestartPrompt returns what restarting would pause, "" when nothing.
func (c *Controller) RestartPrompt() string {
	if c.opts.ActiveWork == nil {
		return ""
	}
	return RestartPrompt(c.opts.ActiveWork())
}

// RestartPrompt describes what a restart pauses and ends.
func RestartPrompt(tasks, recordings int) string {
	var parts []string
	if tasks > 0 {
		parts = append(parts, fmt.Sprintf("暂停 %d 个缓存任务", tasks))
	}
	if recordings > 0 {
		parts = append(parts, fmt.Sprintf("结束 %d 场录制", recordings))
	}
	if len(parts) == 0 {
		return ""
	}
	msg := "重启会" + strings.Join(parts, "并")
	if tasks > 0 {
		msg += "，重启后可以继续缓存"
	}
	return msg + "。"
}

// Restart restarts TRACE into the downloaded version.
func (c *Controller) Restart() {
	c.mu.Lock()
	ready := c.installed
	c.mu.Unlock()
	if !ready || c.opts.Restart == nil {
		return
	}
	c.opts.Restart()
}

func (c *Controller) changed() {
	if c.opts.OnChange != nil {
		c.opts.OnChange()
	}
}

// classifyUpdateError returns whether err is a verification rejection and a
// sanitized user-facing reason shared by Describe and FormatCheckResult.
func classifyUpdateError(err error) (rejected bool, detail string) {
	if IsRejection(err) {
		return true, MsgRejected
	}
	var st *HTTPStatusError
	if errors.As(err, &st) {
		if st.Status == 404 {
			return false, "还没有发布的版本"
		}
		return false, fmt.Sprintf("更新服务器返回 HTTP %d", st.Status)
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return false, "无法连接 GitHub，请检查网络或代理"
	}
	reason := SanitizeHistoryText(err.Error(), nil)
	reason = strings.TrimPrefix(reason, msgCheckFailed+"：")
	reason = strings.TrimPrefix(reason, msgHistoryFailedPrefix+"：")
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false, "请求异常"
	}
	return false, shorten(reason, 80)
}

// Describe turns an update error into what the user reads.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	rejected, detail := classifyUpdateError(err)
	if rejected {
		return detail
	}
	return msgCheckFailed + "：" + detail
}

// IsRejection reports an update that failed verification, by TRACE's
// checks or by the updater's own.
func IsRejection(err error) bool {
	if errors.Is(err, ErrRejected) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"not signed with the app's key", "updates need https", "incomplete manifest", "invalid size", "downloaded ", "the delta updates", "damaged delta", "not a delta update", "checking for updates: json", "jsontext", "invalid character"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
