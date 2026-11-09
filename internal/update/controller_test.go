package update

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEngine struct {
	mu         sync.Mutex
	enabled    bool
	release    *Release
	checkErr   error
	installErr error
	checks     int
	installs   int
}

func (f *fakeEngine) Enabled() bool { return f.enabled }
func (f *fakeEngine) Check(ctx context.Context) (*Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	if f.release == nil {
		return nil, nil
	}
	r := *f.release
	return &r, nil
}
func (f *fakeEngine) Install(ctx context.Context, progress func(int64, int64)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs++
	if progress != nil {
		progress(5, 10)
		progress(10, 10)
	}
	return f.installErr
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestController(eng *fakeEngine, prefs *Prefs, clk *clock) (*Controller, *[]string) {
	var saved []string
	c := NewController(Options{
		Engine:         eng,
		Channel:        "stable",
		CurrentVersion: "0.1.1",
		Prefs:          func() Prefs { return *prefs },
		Now:            clk.now,
		SaveReady:      func(v, n string) { saved = append(saved, v+"|"+n) },
		ActiveWork:     func() (int, int) { return 2, 1 },
	})
	return c, &saved
}

func TestUpdateScheduleFirstMinuteThenDaily(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)}
	eng := &fakeEngine{enabled: true}
	prefs := &Prefs{AutoCheck: true, AutoDownload: true}
	c, _ := newTestController(eng, prefs, clk)
	ctx := context.Background()

	due, ok := c.DueAt()
	if !ok || !due.Equal(clk.t.Add(time.Minute)) {
		t.Fatalf("first check due %v %v, want one minute after launch", due, ok)
	}
	clk.t = clk.t.Add(59 * time.Second)
	c.Tick(ctx)
	if eng.checks != 0 {
		t.Fatal("checked before the first minute")
	}
	clk.t = clk.t.Add(time.Second)
	c.Tick(ctx)
	if eng.checks != 1 {
		t.Fatalf("checks = %d after one minute", eng.checks)
	}
	clk.t = clk.t.Add(23 * time.Hour)
	c.Tick(ctx)
	if eng.checks != 1 {
		t.Fatal("checked again within 24 hours")
	}
	clk.t = clk.t.Add(time.Hour)
	c.Tick(ctx)
	if eng.checks != 2 {
		t.Fatalf("checks = %d after 24 hours", eng.checks)
	}
	prefs.AutoCheck = false
	if _, ok := c.DueAt(); ok {
		t.Fatal("automatic check scheduled while it is off")
	}
	clk.t = clk.t.Add(48 * time.Hour)
	c.Tick(ctx)
	if eng.checks != 2 {
		t.Fatal("checked while automatic checks are off")
	}
}

func TestUpdateOfflineAutomaticCheckIsSilent(t *testing.T) {
	clk := &clock{t: time.Now()}
	offline := &net.OpError{Op: "dial", Err: errors.New("no route")}
	eng := &fakeEngine{enabled: true, checkErr: offline}
	prefs := &Prefs{AutoCheck: true, AutoDownload: true}
	c, _ := newTestController(eng, prefs, clk)
	clk.t = clk.t.Add(time.Minute)
	c.Tick(context.Background())
	st := c.State()
	if st.Message != "" || st.Phase != PhaseIdle {
		t.Fatalf("automatic check failure must stay silent: %+v", st)
	}
	c.CheckNow(context.Background())
	st = c.State()
	if !st.MessageError || !strings.Contains(st.Message, "无法连接") {
		t.Fatalf("manual check must say why: %+v", st)
	}
}

func TestUpdateManualCheckReportsResult(t *testing.T) {
	clk := &clock{t: time.Now()}
	eng := &fakeEngine{enabled: true}
	prefs := &Prefs{AutoCheck: true, AutoDownload: true}
	c, _ := newTestController(eng, prefs, clk)
	c.CheckNow(context.Background())
	if st := c.State(); st.Message != MsgUpToDate || st.MessageError || st.LastCheck.IsZero() {
		t.Fatalf("up to date: %+v", st)
	}
	eng.checkErr = rejectf("bad signature")
	c.CheckNow(context.Background())
	if st := c.State(); st.Message != MsgRejected || !st.MessageError {
		t.Fatalf("rejected update: %+v", st)
	}
}

func TestUpdateAutoDownloadOnReadyBanner(t *testing.T) {
	clk := &clock{t: time.Now()}
	eng := &fakeEngine{enabled: true, release: &Release{Version: "0.1.2", Notes: "## 0.1.2\n- 修复"}}
	prefs := &Prefs{AutoCheck: true, AutoDownload: true}
	c, saved := newTestController(eng, prefs, clk)
	clk.t = clk.t.Add(time.Minute)
	c.Tick(context.Background())
	st := c.State()
	if st.Phase != PhaseReady || st.Release == nil || st.Release.Version != "0.1.2" || st.BannerDismissed {
		t.Fatalf("auto download should make the update ready: %+v", st)
	}
	if eng.installs != 1 || len(*saved) != 1 || !strings.HasPrefix((*saved)[0], "0.1.2|") {
		t.Fatalf("installs=%d saved=%v", eng.installs, *saved)
	}
	if st.Message != "" {
		t.Fatalf("automatic download says nothing beside the button: %q", st.Message)
	}
	c.DismissBanner()
	if !c.State().BannerDismissed {
		t.Fatal("banner not dismissed")
	}
	// No further checks once an update waits for the restart.
	if _, ok := c.DueAt(); ok {
		t.Fatal("check scheduled after the update is ready")
	}
}

func TestUpdateAutoDownloadOffOnlyOffers(t *testing.T) {
	clk := &clock{t: time.Now()}
	eng := &fakeEngine{enabled: true, release: &Release{Version: "0.1.2"}}
	prefs := &Prefs{AutoCheck: true, AutoDownload: false}
	c, _ := newTestController(eng, prefs, clk)
	c.CheckNow(context.Background())
	st := c.State()
	if st.Phase != PhaseAvailable || eng.installs != 0 || !strings.Contains(st.Message, "0.1.2") {
		t.Fatalf("with automatic download off only offer the update: %+v installs=%d", st, eng.installs)
	}
	c.Download(context.Background())
	if st := c.State(); st.Phase != PhaseReady || eng.installs != 1 {
		t.Fatalf("download on request: %+v", st)
	}
}

func TestUpdateFailedInstallKeepsCurrentVersion(t *testing.T) {
	clk := &clock{t: time.Now()}
	eng := &fakeEngine{enabled: true, release: &Release{Version: "0.1.2"}, installErr: errors.New("mygo: installing the update: the update is not signed with the app's key")}
	prefs := &Prefs{AutoCheck: true, AutoDownload: true}
	c, saved := newTestController(eng, prefs, clk)
	clk.t = clk.t.Add(time.Minute)
	c.Tick(context.Background())
	st := c.State()
	if st.Phase == PhaseReady || len(*saved) != 0 {
		t.Fatalf("a rejected update must not become ready: %+v", st)
	}
	if st.Message != MsgRejected {
		t.Fatalf("a rejected download is reported even when automatic: %q", st.Message)
	}
	restarted := false
	c.opts.Restart = func() { restarted = true }
	c.Restart()
	if restarted {
		t.Fatal("restart offered without a ready update")
	}
}

func TestUpdateRestartPromptAndDisabledChannel(t *testing.T) {
	if got := RestartPrompt(2, 1); got != "重启会暂停 2 个缓存任务并结束 1 场录制，重启后可以继续缓存。" {
		t.Fatalf("prompt %q", got)
	}
	if got := RestartPrompt(0, 1); got != "重启会结束 1 场录制。" {
		t.Fatalf("prompt %q", got)
	}
	if RestartPrompt(0, 0) != "" {
		t.Fatal("no prompt without work")
	}
	eng := &fakeEngine{enabled: false}
	prefs := &Prefs{AutoCheck: true, AutoDownload: true}
	c, _ := newTestController(eng, prefs, &clock{t: time.Now()})
	if st := c.State(); st.Enabled || st.Phase != PhaseDisabled {
		t.Fatalf("development build: %+v", st)
	}
	if _, ok := c.DueAt(); ok {
		t.Fatal("development build schedules checks")
	}
	c.CheckNow(context.Background())
	if eng.checks != 0 || c.State().Message != MsgDevChannel {
		t.Fatalf("development build checked: %d %q", eng.checks, c.State().Message)
	}
}

func TestUpdateNoticeAfterUpdate(t *testing.T) {
	c, _ := newTestController(&fakeEngine{enabled: true}, &Prefs{AutoCheck: true, AutoDownload: true}, &clock{t: time.Now()})
	c.SetUpdatedNotice("0.1.2", "## 0.1.2")
	if st := c.State(); st.UpdatedTo != "0.1.2" || st.UpdatedNotes == "" {
		t.Fatalf("notice %+v", st)
	}
	c.DismissUpdatedNotice()
	if c.State().UpdatedTo != "" {
		t.Fatal("notice not dismissed")
	}
}
