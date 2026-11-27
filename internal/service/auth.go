package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// HasAnyLogin reports whether at least one supported site has an active credential.
func (b *Backend) HasAnyLogin() bool {
	return (b.cred != nil && b.cred.HasActiveCredential()) ||
		(b.douyinCred != nil && b.douyinCred.HasActiveCredential()) ||
		(b.weiboCred != nil && b.weiboCred.HasActiveCredential()) ||
		(b.xCred != nil && b.xCred.HasActiveCredential())
}

func (b *Backend) requireAnyLogin() error {
	if b.HasAnyLogin() {
		return nil
	}
	return &site.Error{
		Kind:    site.ErrKindAuthRequired,
		Message: "请先登录哔哩哔哩、抖音、微博或 X 账号",
	}
}

func (b *Backend) requireSiteLogin(siteID site.ID) error {
	switch siteID {
	case site.Bilibili:
		if b.cred != nil && b.cred.HasActiveCredential() {
			return nil
		}
		return &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录哔哩哔哩账号",
		}
	case site.Douyin:
		if b.douyinCred != nil && b.douyinCred.HasActiveCredential() {
			return nil
		}
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先在设置中粘贴 Cookie 登录抖音",
		}
	case site.Weibo:
		if b.weiboCred != nil && b.weiboCred.HasActiveCredential() {
			return nil
		}
		return &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录微博",
		}
	case site.X:
		if b.xCred != nil && b.xCred.HasActiveCredential() {
			return nil
		}
		return &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录 X",
		}
	default:
		return &site.Error{
			Site:    siteID,
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点标识: " + strings.TrimSpace(string(siteID)),
		}
	}
}

func normalizeSiteID(raw string) (site.ID, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(site.Bilibili):
		return site.Bilibili, nil
	case string(site.Douyin):
		return site.Douyin, nil
	case string(site.Weibo):
		return site.Weibo, nil
	case string(site.X):
		return site.X, nil
	default:
		return "", &site.Error{
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点标识: " + strings.TrimSpace(raw),
		}
	}
}

func normalizeExplicitSiteID(raw string) (site.ID, error) {
	id, ok := site.ParseID(raw)
	if !ok {
		return "", &site.Error{
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点标识: " + strings.TrimSpace(raw),
		}
	}
	return id, nil
}

// AccountStatus returns the desensitized Bilibili account status (never exposing raw cookies or tokens).
func (b *Backend) AccountStatus() credential.Status {
	return b.cred.Status()
}

// Statuses returns desensitized credential status snapshots for all supported sites.
func (b *Backend) Statuses() map[string]credential.Status {
	if b.credMgr == nil {
		return map[string]credential.Status{}
	}
	return b.credMgr.Statuses()
}

// StatusOf returns the desensitized credential status for a specific site ("bilibili", "douyin", or "weibo").
func (b *Backend) StatusOf(siteID string) credential.Status {
	if b.credMgr == nil {
		return credential.Status{}
	}
	return b.credMgr.Status(siteID)
}

// Renewal summarizes automatic session renewal availability and last-renewed state for siteID.
func (b *Backend) Renewal(siteID string) RenewalStatus {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return RenewalStatus{Site: strings.TrimSpace(siteID), Available: false, Reason: err.Error()}
	}
	if b.profile == ProfileRemote {
		return RenewalStatus{Site: string(id), Available: false, Reason: "由本地续期"}
	}
	switch id {
	case site.Douyin:
		return RenewalStatus{
			Site:      string(site.Douyin),
			Available: false,
			Reason:    "不可用，粘贴的 Cookie 与浏览器共用会话；过期后请重新粘贴",
		}
	case site.Weibo:
		return RenewalStatus{
			Site:      string(site.Weibo),
			Available: false,
			Reason:    "不可用，粘贴的 Cookie 与浏览器共用会话；过期后请重新粘贴",
		}
	case site.X:
		return RenewalStatus{
			Site:      string(site.X),
			Available: false,
			Reason:    credential.XManualRenewalUnavailableReason,
		}
	case site.Bilibili:
		st := b.cred.Status()
		if !st.LoggedIn {
			return RenewalStatus{
				Site:      string(site.Bilibili),
				Available: false,
				Reason:    "未登录",
			}
		}
		if st.Source == "qr" && st.HasRefreshToken {
			var lastErr string
			if st.State == "refresh_failed" {
				lastErr = st.Message
			}
			return RenewalStatus{
				Site:          string(site.Bilibili),
				Available:     true,
				LastRenewedAt: st.LastRenewedAt,
				LastError:     lastErr,
			}
		}
		return RenewalStatus{
			Site:          string(site.Bilibili),
			Available:     false,
			Reason:        "不可用：粘贴的 Cookie 与浏览器共用会话，续期会让浏览器那边掉线。过期后请重新粘贴，或改用扫码登录",
			LastRenewedAt: st.LastRenewedAt,
		}
	default:
		return RenewalStatus{Site: string(id), Available: false, Reason: "不支持的站点"}
	}
}

// BootstrapAuth verifies existing credentials per-site on startup without cross-site interference:
// - Logged-out sites are skipped with zero network requests.
// - Expired credentials pause only that site's active tasks and recordings.
// - Transient network or risk-control errors mark status "unknown" while keeping credentials usable.
func (b *Backend) BootstrapAuth(ctx context.Context) (credential.Status, error) {
	biliOut, biliErr := b.verifyBootstrapSite(
		ctx,
		site.Bilibili,
		b.cred,
		b.bili.VerifyCredential,
		func(err error) bool { return bilibili.IsKind(err, bilibili.ErrKindAuthRequired) },
		"哔哩哔哩登录已失效，任务已暂停",
		"无法确认登录状态，请重试",
	)
	_, dyErr := b.verifyBootstrapSite(
		ctx,
		site.Douyin,
		b.douyinCred,
		b.douyin.VerifyCredential,
		func(err error) bool { return site.IsKind(err, site.ErrKindAuthRequired) },
		"抖音登录已失效，任务已暂停",
		"无法确认抖音登录状态，请稍后重试",
	)
	_, wbErr := b.verifyBootstrapSite(
		ctx,
		site.Weibo,
		b.weiboCred,
		b.weibo.VerifyCredential,
		func(err error) bool { return site.IsKind(err, site.ErrKindAuthRequired) },
		"微博登录已失效，任务已暂停",
		"无法确认微博登录状态，请稍后重试",
	)
	_, xErr := b.verifyBootstrapSite(
		ctx,
		site.X,
		b.xCred,
		b.x.VerifyCredential,
		func(err error) bool { return site.IsKind(err, site.ErrKindAuthRequired) },
		"X 登录已失效，任务已暂停",
		"无法确认 X 登录状态，请稍后重试",
	)
	return biliOut, errors.Join(biliErr, dyErr, wbErr, xErr)
}

func (b *Backend) verifyBootstrapSite(
	ctx context.Context,
	siteID site.ID,
	store *credential.Store,
	verify func(context.Context) (credential.Status, error),
	isAuthErr func(error) bool,
	expiredMsg, unknownMsg string,
) (credential.Status, error) {
	st := store.Status()
	if st.State == "logged_out" || !st.LoggedIn {
		return st, nil
	}
	verified, err := verify(ctx)
	if err != nil && isAuthErr(err) {
		switch siteID {
		case site.Douyin:
			b.clearDouyinMemoryCaches()
		case site.Weibo:
			b.clearWeiboMemoryCaches()
		case site.X:
			b.clearXMemoryCaches()
		}
		b.pauseSiteActiveWork(siteID, "auth_required", expiredMsg)
	} else if err != nil {
		var stateErr error
		verified, stateErr = store.MarkState("unknown", unknownMsg)
		err = errors.Join(err, stateErr)
		if stateErr != nil {
			verified = store.Status()
		}
	} else if !verified.LoggedIn {
		switch siteID {
		case site.Douyin:
			b.clearDouyinMemoryCaches()
		case site.Weibo:
			b.clearWeiboMemoryCaches()
		case site.X:
			b.clearXMemoryCaches()
		}
		b.pauseSiteActiveWork(siteID, "auth_required", expiredMsg)
	}
	b.events.PublishSite(EventAccountChanged, string(siteID), verified.State)
	return verified, err
}

// VerifyAccount queries Bilibili /x/web-interface/nav to refresh account profile and login state.
func (b *Backend) VerifyAccount(ctx context.Context) (credential.Status, error) {
	st, err := b.bili.VerifyCredential(ctx)
	if err != nil && bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
		b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录已失效，任务已暂停")
	}
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, err
}

// VerifySiteAccount verifies the stored credential for the specified site ("bilibili", "douyin", or "weibo").
func (b *Backend) VerifySiteAccount(ctx context.Context, siteID string) (credential.Status, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return credential.Status{}, err
	}
	switch id {
	case site.Bilibili:
		return b.VerifyAccount(ctx)
	case site.Douyin:
		st, vErr := b.douyin.VerifyCredential(ctx)
		if vErr != nil && site.IsKind(vErr, site.ErrKindAuthRequired) {
			b.clearDouyinMemoryCaches()
			b.pauseSiteActiveWork(site.Douyin, "auth_required", "抖音登录已失效，任务已暂停")
		}
		b.events.PublishSite(EventAccountChanged, string(site.Douyin), st.State)
		return st, vErr
	case site.Weibo:
		st, vErr := b.weibo.VerifyCredential(ctx)
		if vErr != nil && site.IsKind(vErr, site.ErrKindAuthRequired) {
			b.clearWeiboMemoryCaches()
			b.pauseSiteActiveWork(site.Weibo, "auth_required", "微博登录已失效，任务已暂停")
		}
		b.events.PublishSite(EventAccountChanged, string(site.Weibo), st.State)
		return st, vErr
	case site.X:
		st, vErr := b.x.VerifyCredential(ctx)
		if vErr != nil && site.IsKind(vErr, site.ErrKindAuthRequired) {
			b.clearXMemoryCaches()
			b.pauseSiteActiveWork(site.X, "auth_required", "X 登录已失效，任务已暂停")
		}
		b.events.PublishSite(EventAccountChanged, string(site.X), st.State)
		return st, vErr
	default:
		return credential.Status{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
}

// StartQRLogin starts a new QR code login session and invalidates any previous QR session.
func (b *Backend) StartQRLogin(ctx context.Context) (bilibili.QRSession, error) {
	sess, err := b.bili.StartQR(ctx)
	if err != nil {
		b.logError("StartQRLogin", err)
		return bilibili.QRSession{}, err
	}
	b.logInfo("QR login started: flow=" + sess.FlowID)
	return sess, nil
}

// PollQRLogin polls the status of the active QR login flow.
func (b *Backend) PollQRLogin(ctx context.Context, flowID string) (bilibili.QRPollResult, error) {
	prevAuthPaused := b.snapshotAuthPausedQueueIDs(site.Bilibili)
	prev := b.cred.Status()
	res, err := b.bili.PollQR(ctx, flowID)
	if err != nil {
		b.logError("PollQRLogin", err)
		return bilibili.QRPollResult{}, err
	}
	if res.State == "confirmed" {
		b.bili.ClearCaches()
		select {
		case b.renewalWake <- struct{}{}:
		default:
		}
		switched := prev.LoggedIn && prev.Mid != 0 && prev.Mid != res.Account.Mid
		b.logInfo(fmt.Sprintf("QR login confirmed: mid=%d", res.Account.Mid))
		b.finishSiteLogin(site.Bilibili, true, "logged_in", switched, prevAuthPaused)
	}
	return res, nil
}

// CancelQRLogin cancels the QR login flow so late responses never write credentials.
func (b *Backend) CancelQRLogin(flowID string) {
	b.bili.CancelQR(flowID)
	b.logInfo("QR login canceled: flow=" + flowID)
}

// ImportCookie validates a manual Bilibili Cookie string against /x/web-interface/nav first,
// and only persists the credential if verification succeeds.
func (b *Backend) ImportCookie(ctx context.Context, rawCookie, rawRefreshToken string) (credential.Status, error) {
	prevAuthPaused := b.snapshotAuthPausedQueueIDs(site.Bilibili)
	prev := b.cred.Status()
	st, err := b.bili.LoginWithCookie(ctx, rawCookie, rawRefreshToken)
	if err != nil {
		b.logError("ImportCookie", err)
		return b.cred.Status(), err
	}
	// Note: bilibili.Client.LoginWithCookie already invokes SetAccountReplacingHandler
	// when replacing a different logged-in account, so we do not pause a second time here.
	switched := prev.LoggedIn && prev.Mid != 0 && prev.Mid != st.Mid
	b.logInfo(fmt.Sprintf("manual cookie imported: state=%s mid=%d", st.State, st.Mid))
	b.finishSiteLogin(site.Bilibili, st.LoggedIn, st.State, switched, prevAuthPaused)
	return st, nil
}

// LoginWithCookie validates and imports a pasted Cookie for the target site ("bilibili", "douyin", or "weibo").
func (b *Backend) LoginWithCookie(ctx context.Context, siteID, rawCookie, rawRefreshToken string) (credential.Status, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return credential.Status{}, err
	}
	switch id {
	case site.Bilibili:
		return b.ImportCookie(ctx, rawCookie, rawRefreshToken)
	case site.Douyin:
		prevAuthPaused := b.snapshotAuthPausedQueueIDs(site.Douyin)
		prev := b.douyinCred.Status()
		st, lErr := b.douyin.LoginWithCookie(ctx, rawCookie)
		if lErr != nil {
			b.logError("LoginWithCookie.Douyin", lErr)
			return b.douyinCred.Status(), lErr
		}
		switched := prev.LoggedIn && prev.SecUID != "" && prev.SecUID != st.SecUID
		if switched {
			b.clearDouyinMemoryCaches()
			b.pauseSiteActiveWork(site.Douyin, "auth_required", "抖音登录账号已变更，任务已暂停")
		}
		b.logInfo(fmt.Sprintf("douyin cookie imported: state=%s uid=%s", st.State, firstNonEmpty(st.UniqueID, st.UID)))
		b.finishSiteLogin(site.Douyin, st.LoggedIn, st.State, switched, prevAuthPaused)
		return st, nil
	case site.Weibo:
		prevAuthPaused := b.snapshotAuthPausedQueueIDs(site.Weibo)
		prev := b.weiboCred.Status()
		st, lErr := b.weibo.LoginWithCookie(ctx, rawCookie)
		if lErr != nil {
			b.logError("LoginWithCookie.Weibo", lErr)
			return b.weiboCred.Status(), lErr
		}
		switched := prev.LoggedIn && prev.UID != "" && prev.UID != st.UID
		if switched {
			b.clearWeiboMemoryCaches()
			b.pauseSiteActiveWork(site.Weibo, "auth_required", "微博登录账号已变更，任务已暂停")
		}
		b.logInfo(fmt.Sprintf("weibo cookie imported: state=%s uid=%s", st.State, st.UID))
		b.finishSiteLogin(site.Weibo, st.LoggedIn, st.State, switched, prevAuthPaused)
		return st, nil
	case site.X:
		prevAuthPaused := b.snapshotAuthPausedQueueIDs(site.X)
		prev := b.xCred.Status()
		st, lErr := b.x.LoginWithCookie(ctx, rawCookie)
		if lErr != nil {
			b.logError("LoginWithCookie.X", lErr)
			return b.xCred.Status(), lErr
		}
		switched := prev.LoggedIn && prev.UID != "" && prev.UID != st.UID
		if switched {
			b.clearXMemoryCaches()
			b.pauseSiteActiveWork(site.X, "auth_required", "X 登录账号已变更，任务已暂停")
		}
		b.logInfo(fmt.Sprintf("x cookie imported: state=%s uid=%s", st.State, st.UID))
		b.finishSiteLogin(site.X, st.LoggedIn, st.State, switched, prevAuthPaused)
		return st, nil
	default:
		return credential.Status{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
}

func (b *Backend) finishSiteLogin(siteID site.ID, loggedIn bool, state string, switched bool, prevAuthPaused []string) {
	if loggedIn {
		if switched {
			b.resumeAuthPausedQueueIDs(siteID, prevAuthPaused)
		} else if b.queue != nil {
			b.queue.resumeAuthPausedForSite(siteID)
		}
	}
	b.events.PublishSite(EventAccountChanged, string(siteID), state)
	if loggedIn {
		b.afterLocalLogin(siteID)
	}
}

// RefreshCookie runs the 5-step Bilibili Cookie renewal flow (or resumes confirmation if pending).
func (b *Backend) RefreshCookie(ctx context.Context) (credential.Status, error) {
	if err := b.requireDesktop(); err != nil {
		return b.cred.Status(), err
	}
	before := b.cred.CookieHeader()
	st, err := b.bili.CheckAndRefreshCookie(ctx)
	if err != nil {
		b.logError("RefreshCookie", err)
	} else if st.LoggedIn && b.queue != nil {
		b.queue.resumeAuthPausedForSite(site.Bilibili)
	}
	if err == nil && st.LoggedIn && b.cred.CookieHeader() != before {
		// Renewal replaced the Cookie; the old one stops working, so the
		// remote gets the new one (DEC-062 ③).
		b.afterLocalLogin(site.Bilibili)
	}
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, err
}

// Logout pauses Bilibili active tasks and live recordings, cancels any active QR login flow, and clears Bilibili credentials.
func (b *Backend) Logout() (credential.Status, error) {
	b.pauseSiteActiveWork(site.Bilibili, "auth_required", "已退出哔哩哔哩登录，任务已暂停")
	err := b.bili.LogoutCredential()
	st := b.cred.Status()
	if err != nil {
		b.logError("Logout", err)
	} else {
		b.logInfo("bilibili account logged out and credentials cleared")
	}
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, err
}

// LogoutSite pauses active work belonging ONLY to siteID and clears that site's stored credential.
func (b *Backend) LogoutSite(siteID string) (credential.Status, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return credential.Status{}, err
	}
	switch id {
	case site.Bilibili:
		return b.Logout()
	case site.Douyin:
		b.clearDouyinMemoryCaches()
		b.pauseSiteActiveWork(site.Douyin, "auth_required", "已退出抖音登录，任务已暂停")
		lErr := b.douyin.LogoutCredential()
		st := b.douyinCred.Status()
		if lErr != nil {
			b.logError("LogoutSite.Douyin", lErr)
		} else {
			b.logInfo("douyin account logged out and credentials cleared")
		}
		b.events.PublishSite(EventAccountChanged, string(site.Douyin), st.State)
		return st, lErr
	case site.Weibo:
		b.clearWeiboMemoryCaches()
		b.pauseSiteActiveWork(site.Weibo, "auth_required", "已退出微博登录，任务已暂停")
		lErr := b.weibo.LogoutCredential()
		st := b.weiboCred.Status()
		if lErr != nil {
			b.logError("LogoutSite.Weibo", lErr)
		} else {
			b.logInfo("weibo account logged out and credentials cleared")
		}
		b.events.PublishSite(EventAccountChanged, string(site.Weibo), st.State)
		return st, lErr
	case site.X:
		b.clearXMemoryCaches()
		b.pauseSiteActiveWork(site.X, "auth_required", "已退出 X 登录，任务已暂停")
		lErr := b.x.LogoutCredential()
		st := b.xCred.Status()
		if lErr != nil {
			b.logError("LogoutSite.X", lErr)
		} else {
			b.logInfo("x account logged out and credentials cleared")
		}
		b.events.PublishSite(EventAccountChanged, string(site.X), st.State)
		return st, lErr
	default:
		return credential.Status{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
}

func (b *Backend) runRenewalLoop(ctx context.Context) {
	defer b.bgWg.Done()
	// Wait 1 minute after startup before the first automatic renewal check.
	initialTimer := time.NewTimer(time.Minute)
	select {
	case <-ctx.Done():
		initialTimer.Stop()
		return
	case <-b.renewalWake:
		initialTimer.Stop()
		// Wait 1 minute after QR login confirmation
		t2 := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			t2.Stop()
			return
		case <-t2.C:
		}
	case <-initialTimer.C:
	}

	for {
		if ctx.Err() != nil {
			return
		}
		b.tryScheduledRenewal(ctx)

		timer := time.NewTimer(12 * time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.renewalWake:
			timer.Stop()
			t2 := time.NewTimer(time.Minute)
			select {
			case <-ctx.Done():
				t2.Stop()
				return
			case <-t2.C:
			}
		case <-timer.C:
		}
	}
}

func (b *Backend) tryScheduledRenewal(ctx context.Context) {
	st := b.cred.Status()
	if !st.LoggedIn {
		return
	}
	if st.Source != "qr" {
		return
	}
	if b.subs != nil {
		if inBackoff, _ := b.subs.IsSiteInBackoff(string(site.Bilibili)); inBackoff {
			return
		}
		if !b.subs.CanUseSiteQuotaTokens(string(site.Bilibili), 8) {
			return
		}
	}
	_, _ = b.RefreshCookie(bilibili.WithAutomaticRequests(ctx, "renewal"))
}
