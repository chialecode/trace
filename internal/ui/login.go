package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
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
	a.setLoginError("")
	a.QRStatusText = "正在获取二维码…"
	requestGuarded(a, fmt.Sprintf("qr-start-%d", revision), func() bool {
		return a.qrRevision == revision && a.CurrentRoute == RouteLogin
	}, a.backend.StartQRLogin, func(session bilibili.QRSession) {
		a.QRSession = session
		a.QRBitmap = ui.NewBitmap(bilibili.RenderQRRGBA(session.URL, 6))
		a.QRStatusText = "未扫码，请使用哔哩哔哩手机客户端扫码"
		// Testers without an event loop remain deterministic and use manual polls.
		a.mu.Lock()
		dispatch := a.dispatch
		a.mu.Unlock()
		if dispatch == nil {
			return
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.qrCancel = cancel
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if a.isClosed() {
						return
					}
					dispatch(func() {
						if !a.isClosed() && a.CurrentRoute == RouteLogin && a.qrRevision == revision {
							a.pollQRLogin()
						}
					})
				}
			}
		}()
	}, func(err error) { a.setLoginError(userErrorText(err)); a.QRStatusText = "获取失败，请重试" })
}

func (a *App) pollQRLogin() {
	flow, revision := a.QRSession.FlowID, a.qrRevision
	if flow == "" {
		return
	}
	requestGuarded(a, "qr-poll-"+flow, func() bool {
		return a.qrRevision == revision && a.QRSession.FlowID == flow && a.CurrentRoute == RouteLogin
	}, func(ctx context.Context) (bilibili.QRPollResult, error) {
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
	}, func(err error) { a.setLoginError(userErrorText(err)) })
}

func (a *App) clearUnsentLoginInputs() {
	a.ManualCookieInput = ""
	a.ManualRefreshInput = ""
	a.DouyinCookieInput = ""
	a.WeiboCookieInput = ""
	a.XCookieInput = ""
	a.clearSettingsPasteState()
}

func (a *App) importManualCookie() {
	rawCookie, rawRefresh := a.ManualCookieInput, a.ManualRefreshInput
	a.ManualCookieInput, a.ManualRefreshInput = "", ""
	a.setLoginError("")
	if a.isPending("manual-login") {
		rawCookie, rawRefresh = "", ""
		return
	}
	requestGuarded(a, "manual-login", nil, func(ctx context.Context) (credential.Status, error) {
		defer func() { rawCookie, rawRefresh = "", "" }()
		return a.backend.ImportCookie(ctx, rawCookie, rawRefresh)
	}, func(status credential.Status) {
		if status.LoggedIn {
			a.cancelQRLogin()
			a.CompleteLogin()
		}
	}, func(err error) { a.setLoginError(userErrorText(err)) })
}

func (a *App) importDouyinCookie() {
	rawCookie := a.DouyinCookieInput
	// Clear input immediately upon submit before async network validation finishes.
	a.DouyinCookieInput = ""
	a.setLoginError("")
	if strings.TrimSpace(rawCookie) == "" {
		a.setLoginError("缺少 sessionid")
		return
	}
	if a.isPending("douyin-cookie-login") {
		rawCookie = ""
		return
	}
	requestGuarded(a, "douyin-cookie-login", nil, func(ctx context.Context) (credential.Status, error) {
		defer func() { rawCookie = "" }()
		return a.backend.LoginWithCookie(ctx, string(site.Douyin), rawCookie, "")
	}, func(status credential.Status) {
		if status.LoggedIn {
			a.cancelQRLogin()
			handle := strings.TrimSpace(status.UniqueID)
			if handle == "" {
				handle = strings.TrimSpace(status.UID)
			}
			if handle != "" {
				a.notifyInfo(fmt.Sprintf("已登录抖音：%s · 抖音号：%s", status.Uname, handle))
			} else if status.Uname != "" {
				a.notifyInfo(fmt.Sprintf("已登录抖音：%s", status.Uname))
			}
			a.CompleteLogin()
		}
	}, func(err error) {
		a.setLoginError(douyinLoginErrorMessage(err))
	})
}

func douyinLoginErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch site.KindOf(err) {
	case site.ErrKindInvalidInput:
		msg := userErrorText(err)
		if strings.Contains(msg, "KiB") || strings.Contains(msg, "上限") {
			return msg
		}
		return "缺少 sessionid"
	case site.ErrKindAuthRequired:
		return "抖音认为这个 Cookie 没有登录或已过期"
	case site.ErrKindRiskControl, site.ErrKindRateLimited:
		return "抖音要求验证，请先在浏览器里正常打开一次抖音再复制"
	default:
		return userErrorText(err)
	}
}

func (a *App) importWeiboCookie() {
	rawCookie := a.WeiboCookieInput
	// Clear input immediately upon submit before async network validation finishes.
	a.WeiboCookieInput = ""
	a.setLoginError("")
	if strings.TrimSpace(rawCookie) == "" {
		a.setLoginError("缺少 SUB")
		return
	}
	if a.isPending("weibo-cookie-login") {
		rawCookie = ""
		return
	}
	requestGuarded(a, "weibo-cookie-login", nil, func(ctx context.Context) (credential.Status, error) {
		defer func() { rawCookie = "" }()
		return a.backend.LoginWithCookie(ctx, string(site.Weibo), rawCookie, "")
	}, func(status credential.Status) {
		if status.LoggedIn {
			a.cancelQRLogin()
			uid := strings.TrimSpace(status.UID)
			if status.Uname != "" && uid != "" {
				a.notifyInfo(fmt.Sprintf("已登录微博：%s · UID %s", status.Uname, uid))
			} else if status.Uname != "" {
				a.notifyInfo(fmt.Sprintf("已登录微博：%s", status.Uname))
			} else if uid != "" {
				a.notifyInfo(fmt.Sprintf("已登录微博：UID %s", uid))
			}
			a.CompleteLogin()
		}
	}, func(err error) {
		a.setLoginError(weiboLoginErrorMessage(err))
	})
}

func weiboLoginErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch site.KindOf(err) {
	case site.ErrKindInvalidInput:
		msg := userErrorText(err)
		if strings.Contains(msg, "KiB") || strings.Contains(msg, "上限") || strings.Contains(msg, "控制字符") {
			return msg
		}
		return "缺少 SUB（请复制包含登录态 SUB 的完整微博 Cookie）"
	case site.ErrKindAuthRequired:
		return "微博认为这个 Cookie 没有登录、只是访客态或已过期"
	case site.ErrKindRiskControl, site.ErrKindRateLimited:
		return "微博要求验证或触发风控，请先在浏览器里正常打开一次微博再复制 Cookie"
	default:
		return userErrorText(err)
	}
}

func (a *App) importXCookie() {
	rawCookie := a.XCookieInput
	// Clear input immediately upon submit before async network validation finishes.
	a.XCookieInput = ""
	a.setLoginError("")
	if strings.TrimSpace(rawCookie) == "" {
		a.setLoginError("缺少 auth_token 或 ct0（请复制包含 auth_token 与 ct0 的完整 X Cookie）")
		return
	}
	if a.isPending("x-cookie-login") {
		rawCookie = ""
		return
	}
	requestGuarded(a, "x-cookie-login", nil, func(ctx context.Context) (credential.Status, error) {
		defer func() { rawCookie = "" }()
		return a.backend.LoginWithCookie(ctx, string(site.X), rawCookie, "")
	}, func(status credential.Status) {
		if status.LoggedIn {
			a.cancelQRLogin()
			handle := strings.TrimPrefix(strings.TrimSpace(status.UniqueID), "@")
			uid := strings.TrimSpace(status.UID)
			if status.Uname != "" && handle != "" {
				a.notifyInfo(fmt.Sprintf("已登录 X：%s (@%s)", status.Uname, handle))
			} else if handle != "" {
				a.notifyInfo(fmt.Sprintf("已登录 X：@%s", handle))
			} else if status.Uname != "" {
				a.notifyInfo(fmt.Sprintf("已登录 X：%s", status.Uname))
			} else if uid != "" {
				a.notifyInfo(fmt.Sprintf("已登录 X：ID %s", uid))
			}
			a.CompleteLogin()
		}
	}, func(err error) {
		a.setLoginError(xLoginErrorMessage(err))
	})
}

func xLoginErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch site.KindOf(err) {
	case site.ErrKindInvalidInput:
		msg := userErrorText(err)
		if strings.Contains(msg, "KiB") || strings.Contains(msg, "上限") || strings.Contains(msg, "控制字符") || strings.Contains(msg, "auth_token") || strings.Contains(msg, "ct0") {
			return msg
		}
		return "缺少 auth_token 或 ct0（请复制包含 auth_token 与 ct0 的完整 X Cookie）"
	case site.ErrKindAuthRequired:
		return "X 认为这个 Cookie 没有登录或已过期，请重新粘贴"
	case site.ErrKindRiskControl:
		return "X 要求验证或账号被临时锁定，TRACE 已停止 X 的全部自动请求，请先在浏览器里处理"
	case site.ErrKindRateLimited:
		return userErrorText(err)
	default:
		return userErrorText(err)
	}
}
