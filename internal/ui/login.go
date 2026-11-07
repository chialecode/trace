package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

func (a *App) cancelQRLogin() {
	a.qrRevision++
	if a.qrCancel != nil {
		a.qrCancel()
		a.qrCancel = nil
	}
	if a.QRSession.FlowID != "" {
		a.backend.CancelQRLogin(a.QRSession.FlowID)
	}
	a.QRSession, a.QRBitmap = bilibili.QRSession{}, nil
}

func (a *App) startQRLogin() {
	a.cancelQRLogin()
	revision := a.qrRevision
	a.LoginError, a.QRStatusText = "", "正在获取二维码…"
	requestGuarded(a, fmt.Sprintf("qr-start-%d", revision), func() bool { return a.qrRevision == revision }, a.backend.StartQRLogin, func(session bilibili.QRSession) {
		a.QRSession = session
		a.QRBitmap = ui.NewBitmap(bilibili.RenderQRRGBA(session.URL, 6))
		a.QRStatusText = "未扫码，请使用哔哩哔哩手机客户端扫码"
		// Testers without an event loop remain deterministic and use manual polls.
		if a.dispatch == nil {
			return
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.qrCancel = cancel
		dispatch := a.dispatch
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					dispatch(func() {
						if !a.closed && a.qrRevision == revision {
							a.pollQRLogin()
						}
					})
				}
			}
		}()
	}, func(err error) { a.LoginError = err.Error(); a.QRStatusText = "获取失败，请重试" })
}

func (a *App) pollQRLogin() {
	flow, revision := a.QRSession.FlowID, a.qrRevision
	if flow == "" {
		return
	}
	requestGuarded(a, "qr-poll-"+flow, func() bool { return a.qrRevision == revision && a.QRSession.FlowID == flow }, func(ctx context.Context) (bilibili.QRPollResult, error) {
		return a.backend.PollQRLogin(ctx, flow)
	}, func(result bilibili.QRPollResult) {
		a.QRStatusText = result.Message
		switch result.State {
		case "confirmed":
			a.cancelQRLogin()
			a.CompleteLogin()
		case "expired", "canceled":
			a.cancelQRLogin()
			a.QRStatusText = "二维码已失效，请重新获取"
		}
	}, func(err error) { a.LoginError = err.Error() })
}

func (a *App) importManualCookie() {
	rawCookie, rawRefresh := a.ManualCookieInput, a.ManualRefreshInput
	a.ManualCookieInput, a.ManualRefreshInput, a.LoginError = "", "", ""
	requestGuarded(a, "manual-login", nil, func(ctx context.Context) (credential.Status, error) {
		status, err := a.backend.ImportCookie(ctx, rawCookie, rawRefresh)
		rawCookie, rawRefresh = "", ""
		return status, err
	}, func(status credential.Status) {
		if status.LoggedIn {
			a.cancelQRLogin()
			a.CompleteLogin()
		}
	}, func(err error) { a.LoginError = err.Error() })
}
