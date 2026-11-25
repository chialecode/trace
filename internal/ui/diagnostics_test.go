package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type runtimeLogsBackend struct {
	service.BackendAPI
	reads int
	fail  bool
}

func (b *runtimeLogsBackend) RecentLogs(int) ([]string, error) {
	b.reads++
	if b.fail {
		return nil, errors.New("synthetic log read failure")
	}
	return []string{"retained runtime line"}, nil
}

func TestRuntimeLogsRefreshOncePerSecondAndShowReadFailures(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	backend := &runtimeLogsBackend{BackendAPI: app.backend}
	app.backend = backend
	app.Navigate(RouteSettings)
	tester := ui.NewTester(app.View, 720, 560)
	if err := tester.Click("设置分类：日志"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		tester.Frame()
	}
	if backend.reads != 1 {
		t.Fatalf("runtime logs were read on every repaint: %d reads", backend.reads)
	}
	backend.fail = true
	tester.Advance(time.Second)
	if backend.reads != 2 || !strings.Contains(app.runtimeLogError, "synthetic log read failure") {
		t.Fatalf("runtime log read failure disappeared: reads=%d error=%q", backend.reads, app.runtimeLogError)
	}
	if _, ok := tester.Find("运行日志读取错误"); !ok || len(app.runtimeLogLines) != 1 {
		t.Fatal("log read failure was hidden or destroyed the last readable log lines")
	}
	app.SettingsCategory = "account"
	tester.Advance(2 * time.Second)
	if backend.reads != 2 {
		t.Fatal("runtime log polling continued outside the log category")
	}
}

func TestMonospaceFontStackAndDiagnosticsReadouts(t *testing.T) {
	if MonoFont != "JetBrains Mono, Cascadia Mono, Consolas" {
		t.Fatalf("MonoFont = %q, want %q", MonoFont, "JetBrains Mono, Cascadia Mono, Consolas")
	}
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.runtimeLogLines = []string{
		"2026-11-24T12:00:00Z [info] 启动完成 version=0.1.2-dev commit=abcdef1",
		"2026-11-24T12:00:01Z [info] 媒体库索引正常 items=42",
	}
	app.runtimeLogsNextRead = time.Now().Add(time.Hour)
	app.Navigate(RouteSettings)

	for _, cat := range []string{"storage", "logs", "update", "about"} {
		app.SettingsCategory = cat
		for _, w := range []int{1280, 1000, 720} {
			tt := ui.NewTester(app.View, w, 860)
			tt.Frame()
			center, ok := tt.Find("中栏内容")
			if !ok || center.X+center.W > float32(w)+0.5 {
				t.Fatalf("category=%s width=%d overflowed: ok=%v center=%+v", cat, w, ok, center)
			}
		}
	}
}
