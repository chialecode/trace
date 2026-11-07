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
