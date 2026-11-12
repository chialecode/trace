package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

const maxPreviewMediaBytes = 64 * 1024 * 1024 // 64 MiB safety cap for uncached note BGM / clip preview

// HasAnyLogin reports whether at least one supported site has an active credential.
func (b *Backend) HasAnyLogin() bool {
	return (b.cred != nil && b.cred.HasActiveCredential()) ||
		(b.douyinCred != nil && b.douyinCred.HasActiveCredential())
}

func (b *Backend) requireAnyLogin() error {
	if b.HasAnyLogin() {
		return nil
	}
	return &site.Error{
		Kind:    site.ErrKindAuthRequired,
		Message: "请先登录哔哩哔哩或抖音账号",
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
	default:
		return &site.Error{
			Site:    siteID,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录对应站点账号",
		}
	}
}

// requireLogin preserves compatibility for local-only operations gated on >= 1 active site login.
func (b *Backend) requireLogin() error {
	return b.requireAnyLogin()
}

func normalizeSiteID(raw string) (site.ID, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(site.Bilibili):
		return site.Bilibili, nil
	case string(site.Douyin):
		return site.Douyin, nil
	default:
		return "", &site.Error{
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点标识: " + strings.TrimSpace(raw),
		}
	}
}

// Statuses returns desensitized credential status snapshots for all supported sites.
func (b *Backend) Statuses() map[string]credential.Status {
	if b.credMgr == nil {
		return map[string]credential.Status{}
	}
	return b.credMgr.Statuses()
}

// StatusOf returns the desensitized credential status for a specific site ("bilibili" or "douyin").
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
	if id == site.Douyin {
		return RenewalStatus{
			Site:      string(site.Douyin),
			Available: false,
			Reason:    "不可用，粘贴的 Cookie 与浏览器共用会话；过期后请重新粘贴",
		}
	}
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
}

// VerifySiteAccount verifies the stored credential for the specified site ("bilibili" or "douyin").
func (b *Backend) VerifySiteAccount(ctx context.Context, siteID string) (credential.Status, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return credential.Status{}, err
	}
	if id == site.Bilibili {
		return b.VerifyAccount(ctx)
	}
	st, vErr := b.douyin.VerifyCredential(ctx)
	if vErr != nil && site.IsKind(vErr, site.ErrKindAuthRequired) {
		b.pauseSiteActiveWork(site.Douyin, "auth_required", "抖音登录已失效，任务已暂停")
	}
	b.events.PublishSite(EventAccountChanged, string(site.Douyin), st.State)
	return st, vErr
}

// LoginWithCookie validates and imports a pasted Cookie for the target site ("bilibili" or "douyin").
func (b *Backend) LoginWithCookie(ctx context.Context, siteID, rawCookie, rawRefreshToken string) (credential.Status, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return credential.Status{}, err
	}
	if id == site.Bilibili {
		return b.ImportCookie(ctx, rawCookie, rawRefreshToken)
	}
	prev := b.douyinCred.Status()
	st, lErr := b.douyin.LoginWithCookie(ctx, rawCookie)
	if lErr != nil {
		b.logError("LoginWithCookie.Douyin", lErr)
		return b.douyinCred.Status(), lErr
	}
	if prev.LoggedIn && prev.SecUID != "" && prev.SecUID != st.SecUID {
		b.pauseSiteActiveWork(site.Douyin, "auth_required", "抖音登录账号已变更，任务已暂停")
	}
	b.logInfo(fmt.Sprintf("douyin cookie imported: state=%s uid=%s", st.State, firstNonEmpty(st.UniqueID, st.UID)))
	b.events.PublishSite(EventAccountChanged, string(site.Douyin), st.State)
	return st, nil
}

// LogoutSite pauses active work belonging ONLY to siteID and clears that site's stored credential.
func (b *Backend) LogoutSite(siteID string) (credential.Status, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return credential.Status{}, err
	}
	if id == site.Bilibili {
		return b.Logout()
	}
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
}

// Resolve dispatches user input via site.Registry after enforcing per-site login gates with zero pre-auth network requests.
func (b *Backend) Resolve(ctx context.Context, rawInput string) (site.ResolveTarget, error) {
	if err := b.requireAnyLogin(); err != nil {
		return site.ResolveTarget{}, err
	}
	siteID, normalized, err := b.registry.DetectSite(rawInput)
	if err != nil {
		return site.ResolveTarget{}, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return site.ResolveTarget{}, err
	}
	res, ok := b.registry.Resolver(siteID)
	if !ok {
		return site.ResolveTarget{}, &site.Error{
			Site:        siteID,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     fmt.Sprintf("当前尚未启用 %s 站点解析器", siteID.DisplayName()),
		}
	}
	target, err := res.Resolve(ctx, normalized)
	if err != nil {
		return target, err
	}
	if target.Site == "" {
		target.Site = siteID
	}
	return target, nil
}

func (b *Backend) preferredDouyinCodec() string {
	dl := b.settings.Get().Download
	for _, c := range dl.CodecOrder {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "hevc", "h265", "bytevc1":
			return "hevc"
		case "avc", "h264":
			return "avc"
		}
	}
	if strings.EqualFold(dl.PreferredVideoCodec, "hevc") {
		return "hevc"
	}
	return "avc"
}

// DouyinWork fetches and maps a single Douyin video or image/mixed note post.
func (b *Backend) DouyinWork(ctx context.Context, awemeID string) (*DouyinWorkView, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	detail, err := b.douyin.GetWorkDetail(ctx, awemeID, douyin.StreamSelectOptions{
		PreferredCodec: b.preferredDouyinCodec(),
	})
	if err != nil {
		b.logError("DouyinWork", err)
		return nil, err
	}
	var cached *library.ItemMetadata
	cacheKey := douyinCacheKeyForKind(detail.Kind, detail.ID)
	if cacheKey != "" {
		if item, ok, _ := b.lib.GetItem(cacheKey); ok {
			cached = &item
		}
	}
	if detail.Author.SecUID != "" && detail.Author.WebRID != "" {
		b.syncDouyinAccountWebRID(detail.Author.SecUID, detail.Author.WebRID)
	}
	return mapDouyinWorkView(detail, cached), nil
}

// DouyinUser fetches a Douyin creator's profile and maps optional live banner state.
func (b *Backend) DouyinUser(ctx context.Context, secUID string) (*DouyinUserView, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	targetSecUID := strings.TrimSpace(secUID)
	if targetSecUID == "" || strings.EqualFold(targetSecUID, "self") || targetSecUID == "/user/self" {
		targetSecUID = b.douyinCred.Status().SecUID
		if targetSecUID == "" {
			selfProf, sErr := b.douyin.GetSelfProfile(ctx)
			if sErr != nil {
				return nil, sErr
			}
			targetSecUID = selfProf.SecUID
		}
	}
	prof, err := b.douyin.GetUserProfile(ctx, targetSecUID)
	if err != nil {
		b.logError("DouyinUser", err)
		return nil, err
	}
	if b.subs != nil {
		_ = b.subs.UpdateAccountSnapshot(string(site.Douyin), prof.SecUID, prof.Nickname, prof.AvatarURL, prof.UniqueID)
	}
	if prof.WebRID != "" {
		b.syncDouyinAccountWebRID(prof.SecUID, prof.WebRID)
	}
	if b.live != nil && (prof.WebRID != "" || prof.RoomID != "") {
		b.live.ObserveAccountLiveHint(site.Douyin, prof.SecUID, prof.WebRID, prof.RoomID)
	}
	var liveState *LiveAccountState
	if prof.LiveStatus == 1 {
		st := LiveAccountState{
			Site:       string(site.Douyin),
			AccountKey: formatAccountKey(site.Douyin, prof.SecUID),
			UID:        prof.SecUID,
			RoomID:     prof.RoomID,
			WebRID:     prof.WebRID,
			Uname:      prof.Nickname,
			Face:       prof.AvatarURL,
			LiveStatus: 1,
			StatusText: "直播中",
			Transport:  string(site.LiveTransportHTTPFLV),
		}
		if prof.RoomID != "" {
			st.RecordingKey = "douyin:live:" + prof.RoomID
		}
		if prof.WebRID != "" && b.live != nil {
			if rs, lErr := b.live.CheckAccountLiveStatus(ctx, site.Douyin, prof.SecUID, prof.WebRID, false); lErr == nil {
				mapped := mapRoomToAccountState(rs)
				if mapped.Uname == "" {
					mapped.Uname = prof.Nickname
				}
				if mapped.Face == "" {
					mapped.Face = prof.AvatarURL
				}
				st = mapped
			}
		}
		liveState = &st
	}
	return mapDouyinUserView(prof, liveState), nil
}

// DouyinUserWorks fetches one paginated grid page of a Douyin creator's works.
func (b *Backend) DouyinUserWorks(ctx context.Context, secUID, cursor string) (*DouyinUserWorksPage, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	page, err := b.douyin.ListUserPosts(ctx, douyin.UserPostsRequest{
		SecUID:        strings.TrimSpace(secUID),
		MaxCursor:     strings.TrimSpace(cursor),
		StreamOptions: douyin.StreamSelectOptions{PreferredCodec: b.preferredDouyinCodec()},
	})
	if err != nil {
		b.logError("DouyinUserWorks", err)
		return nil, err
	}
	cards := make([]DouyinWorkCardDTO, 0, len(page.Works))
	for _, w := range page.Works {
		cards = append(cards, mapDouyinWorkCard(w))
	}
	return &DouyinUserWorksPage{
		Works:           cards,
		NextCursor:      page.NextCursor,
		HasMore:         page.HasMore,
		UncertainEmpty:  page.UncertainEmpty,
		UncertainReason: page.UncertainReason,
	}, nil
}

// DouyinLive queries a Douyin live room by web_rid and returns normalized room and anchor state.
func (b *Backend) DouyinLive(ctx context.Context, webRID string) (*DouyinLiveView, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	detail, err := b.douyin.EnterLiveRoom(ctx, strings.TrimSpace(webRID), "")
	if err != nil {
		b.logError("DouyinLive", err)
		return nil, err
	}
	prefWebRID := detail.PreferredWebRID()
	if detail.OwnerSecUID != "" && prefWebRID != "" {
		b.syncDouyinAccountWebRID(detail.OwnerSecUID, prefWebRID)
	}
	if b.live != nil && detail.OwnerSecUID != "" {
		b.live.ObserveAccountLiveHint(site.Douyin, detail.OwnerSecUID, prefWebRID, detail.RoomID)
	}
	liveStatus := 0
	statusText := "未开播"
	if detail.RoomStatus == 2 {
		liveStatus = 1
		if detail.AudioOnly {
			statusText = "纯语音直播"
		} else {
			statusText = "直播中"
		}
	}
	acctState := &LiveAccountState{
		Site:         string(site.Douyin),
		AccountKey:   formatAccountKey(site.Douyin, detail.OwnerSecUID),
		RecordingKey: detail.LiveDedupeKey(),
		UID:          detail.OwnerSecUID,
		RoomID:       detail.RoomID,
		WebRID:       prefWebRID,
		Uname:        detail.OwnerNickname,
		Face:         detail.OwnerAvatar,
		Title:        detail.Title,
		Cover:        detail.CoverURL,
		AreaName:     detail.AreaName,
		LiveStatus:   liveStatus,
		LiveSince:    detail.LiveSince,
		StatusText:   statusText,
		Transport:    string(site.LiveTransportHTTPFLV),
		AudioOnly:    detail.AudioOnly,
	}
	if b.live != nil && detail.OwnerSecUID != "" {
		if cached, ok := b.live.CachedAccountState(site.Douyin, detail.OwnerSecUID); ok && cached.Recording {
			acctState.Recording = true
			acctState.RecordSession = cached.RecordSession
			acctState.RecordDurationSec = cached.RecordDurationSec
			acctState.RecordBytes = cached.RecordBytes
			acctState.RecordSegments = cached.RecordSegments
			acctState.RecordItemDir = cached.RecordItemDir
		}
	}
	canonicalURL := "https://live.douyin.com/" + prefWebRID
	return &DouyinLiveView{
		OwnerSecUID:  detail.OwnerSecUID,
		OwnerUID:     detail.OwnerUID,
		OwnerName:    detail.OwnerNickname,
		OwnerAvatar:  detail.OwnerAvatar,
		WebRID:       prefWebRID,
		RoomID:       detail.RoomID,
		LiveStatus:   liveStatus,
		StatusText:   statusText,
		Title:        detail.Title,
		CoverURL:     detail.CoverURL,
		AreaName:     detail.AreaName,
		LiveSince:    detail.LiveSince,
		AudioOnly:    detail.AudioOnly,
		CanonicalURL: canonicalURL,
		State:        acctState,
	}, nil
}

// StagePreviewMedia downloads a whitelisted CDN audio/video clip (e.g. uncached Douyin note BGM
// or note video page) without sending Cookie into <DataDir>/cache/preview/ and returns its local file path
// for playback via internal/player.
func (b *Backend) StagePreviewMedia(ctx context.Context, siteID, rawURL, ext string) (string, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return "", err
	}
	if err := b.requireSiteLogin(id); err != nil {
		return "", err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "媒体预览地址为空"}
	}
	var data []byte
	switch id {
	case site.Douyin:
		var fetchErr error
		data, _, fetchErr = b.douyin.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	case site.Bilibili:
		var fetchErr error
		data, fetchErr = b.bili.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	}
	safeExt := ".mp4"
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".mp3", "mp3":
		safeExt = ".mp3"
	case ".m4a", "m4a":
		safeExt = ".m4a"
	case ".aac", "aac":
		safeExt = ".aac"
	case ".ogg", "ogg":
		safeExt = ".ogg"
	case ".wav", "wav":
		safeExt = ".wav"
	}
	sum := sha256.Sum256([]byte(string(id) + "\x00" + rawURL))
	fileName := fmt.Sprintf("preview_%s_%x%s", string(id), sum[:16], safeExt)
	fullPath := filepath.Join(b.previewDir, fileName)
	if err := storage.WriteFileAtomic(fullPath, data, 0o600); err != nil {
		return "", err
	}
	return fullPath, nil
}

// CacheDouyinWork enqueues a Douyin video or mixed note work for local caching.
func (b *Backend) CacheDouyinWork(ctx context.Context, awemeID string) (download.Task, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return download.Task{}, err
	}
	return b.enqueueDouyinWorkWithSource(ctx, awemeID, "manual")
}

func (b *Backend) enqueueDouyinWorkWithSource(ctx context.Context, awemeID, source string) (download.Task, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return download.Task{}, err
	}
	t, err := b.dl.EnqueueDouyinWork(ctx, download.DouyinWorkEnqueueRequest{
		AwemeID: strings.TrimSpace(awemeID),
		Source:  source,
	})
	if err != nil {
		b.logError("CacheDouyinWork", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Douyin), "enqueued:"+t.TargetID)
	return t, nil
}

// RemoveHistoryForSite deletes a single recent-parse history item scoped to (siteID, kind, id).
func (b *Backend) RemoveHistoryForSite(siteID, kind, id string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	return b.history.RemoveBySite(siteID, kind, id)
}

// LinkSubscriptionAccountRef links an AccountRef (preserving Douyin UniqueID and WebRID) to a subscription.
func (b *Backend) LinkSubscriptionAccountRef(subID string, acct subscription.AccountRef) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.LinkAccount(subID, acct)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "linked")
	}
	return res, err
}

func formatBackoffLevelText(level int) string {
	switch {
	case level <= 1:
		return "15 分钟"
	case level == 2:
		return "30 分钟"
	case level == 3:
		return "1 小时"
	case level == 4:
		return "2 小时"
	default:
		return "6 小时"
	}
}

// SchedulerStatuses returns per-site scheduled check timing and backoff state for Settings -> 订阅与直播.
func (b *Backend) SchedulerStatuses() map[string]SiteSchedulerStatus {
	out := make(map[string]SiteSchedulerStatus, 2)
	var states map[string]subscription.SchedulerState
	if b.subs != nil {
		states = b.subs.ListSiteSchedulerStates()
	}
	for _, id := range site.All() {
		key := string(id)
		label := "哔哩哔哩"
		if id == site.Douyin {
			label = "抖音"
		}
		st := SiteSchedulerStatus{
			Site:        key,
			DisplayName: label,
		}
		if b.subs != nil {
			raw := states[key]
			st.LastError = raw.LastStopReason
			if inBackoff, until := b.subs.IsSiteInBackoff(key); inBackoff {
				lvlText := formatBackoffLevelText(raw.BackoffLevel)
				st.BackingOff = true
				st.CurrentInterval = lvlText
				st.BackoffUntil = until.UTC().Format("2006-01-02T15:04:05Z07:00")
				st.BackoffSummary = fmt.Sprintf("%s：因风控放慢到 %s，%s 恢复", label, lvlText, until.Local().Format("1 月 2 日 15:04"))
			}
		}
		out[key] = st
	}
	return out
}

// ListLiveAccounts returns unified live & recording states across all tracked rooms/accounts.
func (b *Backend) ListLiveAccounts() []LiveAccountState {
	if b.requireAnyLogin() != nil {
		return nil
	}
	rooms := b.live.ListRooms()
	out := make([]LiveAccountState, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, mapBilibiliRoomToAccountState(r))
	}
	return out
}

// LiveStatuses refreshes live status for a mixed slice of "<site>:<uid>" account keys.
func (b *Backend) LiveStatuses(ctx context.Context, accountKeys []string) ([]LiveAccountState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	var biliUIDs []int64
	var douyinUIDs []string
	for _, rawKey := range accountKeys {
		siteID, uid, err := site.ParseAccountKey(rawKey)
		if err != nil {
			continue
		}
		switch siteID {
		case site.Bilibili:
			if mid, mErr := strconv.ParseInt(uid, 10, 64); mErr == nil && mid > 0 {
				biliUIDs = append(biliUIDs, mid)
			}
		case site.Douyin:
			douyinUIDs = append(douyinUIDs, uid)
		}
	}

	var out []LiveAccountState
	var firstErr error

	if len(biliUIDs) > 0 {
		if err := b.requireSiteLogin(site.Bilibili); err != nil {
			firstErr = err
		} else if rooms, err := b.live.RefreshForAccounts(ctx, biliUIDs); err != nil {
			firstErr = err
		} else {
			for _, r := range rooms {
				out = append(out, mapBilibiliRoomToAccountState(r))
			}
		}
	}

	if len(douyinUIDs) > 0 {
		if err := b.requireSiteLogin(site.Douyin); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, secUID := range douyinUIDs {
				cachedWebRID := b.lookupAccountWebRID(secUID)
				rs, err := b.live.CheckAccountLiveStatus(ctx, site.Douyin, secUID, cachedWebRID, false)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				out = append(out, mapRoomToAccountState(rs))
			}
		}
	}

	return out, firstErr
}

// StartLiveRecordingByAccount starts live recording for a "<site>:<uid>" creator account.
func (b *Backend) StartLiveRecordingByAccount(ctx context.Context, accountKey string) (LiveAccountState, error) {
	siteID, uid, err := site.ParseAccountKey(accountKey)
	if err != nil {
		return LiveAccountState{}, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return LiveAccountState{}, err
	}
	if siteID == site.Bilibili {
		mid, _ := strconv.ParseInt(uid, 10, 64)
		rooms, rErr := b.live.RefreshForAccounts(ctx, []int64{mid})
		if rErr != nil {
			return LiveAccountState{}, rErr
		}
		if len(rooms) == 0 || rooms[0].RoomID <= 0 {
			return LiveAccountState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindUnavailable, Message: "该账号未开通直播间"}
		}
		rs, sErr := b.live.StartRecording(ctx, rooms[0].RoomID)
		return mapRoomToAccountState(rs), sErr
	}
	return b.startDouyinLiveRecording(ctx, uid, b.lookupAccountWebRID(uid))
}

// StartLiveRecordingByRoom starts live recording given an explicit site and roomID/webRID.
func (b *Backend) StartLiveRecordingByRoom(ctx context.Context, siteID, roomOrWebRID string) (LiveAccountState, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return LiveAccountState{}, err
	}
	if err := b.requireSiteLogin(id); err != nil {
		return LiveAccountState{}, err
	}
	if id == site.Bilibili {
		roomID, pErr := strconv.ParseInt(strings.TrimSpace(roomOrWebRID), 10, 64)
		if pErr != nil || roomID <= 0 {
			return LiveAccountState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
		}
		rs, sErr := b.live.StartRecording(ctx, roomID)
		return mapRoomToAccountState(rs), sErr
	}
	return b.startDouyinLiveRecording(ctx, "", strings.TrimSpace(roomOrWebRID))
}

// StopLiveRecordingByKey stops an active live recording by canonical recording key ("<site>:live:<room_id>" or decimal roomID).
func (b *Backend) StopLiveRecordingByKey(recKey string) (LiveAccountState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return LiveAccountState{}, err
	}
	rs, err := b.live.StopRecordingByKey(strings.TrimSpace(recKey))
	return mapRoomToAccountState(rs), err
}

// OpenLiveInBrowser opens a Bilibili roomID or Douyin web_rid in the external system browser.
func (b *Backend) OpenLiveInBrowser(siteID, roomOrWebRID string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return err
	}
	clean := strings.TrimSpace(roomOrWebRID)
	for _, r := range clean {
		if r < '0' || r > '9' {
			return &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
		}
	}
	if clean == "" || clean == "0" {
		return &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "无效的直播间号"}
	}
	if id == site.Douyin {
		return b.desktop.OpenExternalURL("https://live.douyin.com/" + clean)
	}
	return b.desktop.OpenExternalURL("https://live.bilibili.com/" + clean)
}

func (b *Backend) LibraryStatistics() (library.Statistics, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.Statistics{}, err
	}
	return b.lib.Statistics()
}

func (b *Backend) ListSubscriptionUpdatesPage(subID string, limit, offset int) ([]subscription.UpdateItem, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.subs.ListUpdatesPage(subID, limit, offset)
}

func (b *Backend) UserDynamicsPage(ctx context.Context, mid int64, offset string) (bilibili.UserDynamicsPage, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return bilibili.UserDynamicsPage{}, err
	}
	return b.bili.UserDynamics(ctx, mid, offset)
}

func (b *Backend) UserVideosPage(ctx context.Context, mid int64, page int) (bilibili.UserVideosPage, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return bilibili.UserVideosPage{}, err
	}
	return b.bili.UserVideos(ctx, mid, page, 30, "pubdate", "")
}

// LiveStatus is a read-only refresh of visible Bilibili accounts, with a 60s cache.
// It never starts automatic recordings in response to a UI request.
func (b *Backend) LiveStatus(ctx context.Context, uids []int64) ([]live.RoomState, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	return b.live.RefreshForAccounts(ctx, uids)
}

func (b *Backend) detectImageSite(rawURL string) site.ID {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return site.Bilibili
	}
	host := strings.ToLower(u.Hostname())
	if site.MatchDomainSuffix(host, site.DefaultDouyinMediaHosts()) {
		return site.Douyin
	}
	if id, ok := site.DetectHostSite(host); ok {
		return id
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		if b.douyin != nil {
			ep := b.douyin.Endpoints()
			for _, candidate := range []string{ep.ProfileSelf, ep.AwemeDetail, ep.LiveWebEnter} {
				if cu, cErr := url.Parse(candidate); cErr == nil && strings.EqualFold(cu.Host, u.Host) {
					if strings.HasPrefix(u.Path, "/bfs/") {
						return site.Bilibili
					}
					return site.Douyin
				}
			}
		}
		if strings.HasPrefix(u.Path, "/douyin") || strings.HasPrefix(u.Path, "/obj/") || strings.HasPrefix(u.Path, "/tos-") {
			return site.Douyin
		}
		if b.douyinCred != nil && b.douyinCred.HasActiveCredential() && (b.cred == nil || !b.cred.HasActiveCredential()) {
			return site.Douyin
		}
	}
	return site.Bilibili
}
