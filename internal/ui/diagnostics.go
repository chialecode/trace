package ui

import (
	"context"
	"time"

	"github.com/chialecode/trace/internal/service"
)

type runtimeLogResult struct {
	diagnostics service.Diagnostics
	lines       []string
	readError   string
}

func (a *App) refreshRuntimeLogs(now time.Time) {
	if !a.runtimeLogsNextRead.IsZero() && now.Before(a.runtimeLogsNextRead) {
		return
	}
	a.runtimeLogsNextRead = now.Add(time.Second)
	backend := a.backend
	requestGuarded(a, "runtime-logs", func() bool {
		return a.CurrentRoute == RouteSettings && a.SettingsCategory == "logs"
	}, func(context.Context) (runtimeLogResult, error) {
		result := runtimeLogResult{diagnostics: backend.DiagnosticsSnapshot()}
		lines, err := backend.RecentLogs(500)
		if err != nil {
			result.readError = "无法读取运行日志：" + err.Error()
		} else {
			result.lines = lines
		}
		return result, nil
	}, func(result runtimeLogResult) {
		a.runtimeDiagnostics = result.diagnostics
		a.runtimeLogError = result.readError
		if result.readError == "" {
			a.runtimeLogLines = result.lines
		}
	}, nil)
}
