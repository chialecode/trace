package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

// This file is what trace-server's API calls (docs/design/remote.md §3–§5,
// §7). Status is assembled from what the backend already knows; it never
// sends a site request of its own.

// ServerStatus returns the remote's status without the upload part (the
// server adds it) and without the disk part when free is 0.
func (b *Backend) ServerStatus() remote.Status {
	build := appdirs.BuildInfo()
	node := b.lib.Node()
	now := time.Now().UTC()
	st := remote.Status{
		Version:   build.Version,
		Protocol:  remote.ProtocolVersion,
		Node:      remote.Node{ID: node.Node, Name: node.Name},
		StartedAt: b.startedAt.UTC().Format(time.RFC3339),
		Now:       now.Format(time.RFC3339),
	}
	sync := b.loadSyncState()
	st.Subscriptions = b.ServerSubscriptions()

	backoff := map[string]string{}
	if b.subs != nil {
		for siteName, s := range b.subs.ListSiteSchedulerStates() {
			if until, err := time.Parse(time.RFC3339, s.BackoffUntil); err == nil && until.After(now) {
				backoff[siteName] = until.UTC().Format(time.RFC3339)
			}
		}
	}
	for _, sid := range site.All() {
		cs := b.credMgr.Status(string(sid))
		row := remote.SiteStatus{Site: string(sid), Uname: cs.Uname, CookieSyncedAt: sync.CookieSyncedAt[string(sid)]}
		switch {
		case cs.State == "logged_out" || cs.State == "decrypt_failed" || cs.State == "":
			row.State = remote.SiteNone
		case cs.State == "expired":
			row.State = remote.SiteResync
			row.Message = "登录已失效，请在本地重新同步 Cookie"
		case backoff[string(sid)] != "":
			row.State = remote.SiteBackoff
			row.BackoffUntil = backoff[string(sid)]
		case cs.State == "unknown":
			row.State = remote.SiteUnknown
			row.Message = "暂时无法确认登录状态"
		default:
			row.State = remote.SiteValid
		}
		st.Sites = append(st.Sites, row)
	}

	for _, p := range b.checkPlanLocal() {
		st.Checks = append(st.Checks, remote.CheckRow{
			Site: p.Site, UID: p.UID, Name: p.Name, Subscription: p.SubName,
			LastCheckedAt: p.LastCheckedAt, LastResult: firstNonEmpty(p.Note, p.LastKind), NextAt: p.NextAt,
			Baseline: p.LastKind == "baseline", Scheduled: p.Plan == subscription.PlanScheduled,
		})
	}

	if b.dl != nil {
		if tasks, err := b.dl.ListTasks(); err == nil {
			var done []remote.TaskRow
			for _, t := range tasks {
				switch t.Status {
				case "running":
					st.Cache.Running++
				case "queued":
					st.Cache.Queued++
				case "completed", "partial", "failed":
					msg := ""
					if t.Status == "failed" {
						msg = firstNonEmpty(t.ErrorMessage, t.LastError)
					}
					done = append(done, remote.TaskRow{Site: t.Site, Title: t.Title, Status: t.Status, At: t.UpdatedAt, Error: b.sanitizeLogLine(msg)})
				}
			}
			sort.SliceStable(done, func(i, j int) bool { return done[i].At > done[j].At })
			if len(done) > 20 {
				done = done[:20]
			}
			st.Cache.Recent = done
		}
	}

	if b.live != nil {
		for _, r := range b.live.ListRooms() {
			if !r.Recording {
				continue
			}
			row := remote.RecordingRow{Site: firstNonEmpty(r.Site, string(site.Bilibili)), Name: r.Uname, Title: r.Title, DurationSec: int64(r.RecordDurationSec), Bytes: r.RecordBytes, Segments: r.RecordSegments, Incidents: len(r.Incidents)}
			if r.RecordDurationSec > 0 {
				row.StartedAt = now.Add(-time.Duration(r.RecordDurationSec * float64(time.Second))).Format(time.RFC3339)
			}
			st.Recordings = append(st.Recordings, row)
		}
	}

	for _, e := range b.RecentErrors() {
		st.Errors = append(st.Errors, remote.ErrorRow{At: e.At, Category: errorCategory(e.Message), Message: e.Message})
	}
	for _, s := range b.mustListSubscriptions() {
		if s.Rules.FullCache {
			st.Notes = append(st.Notes, "规则里的“全部缓存”在远程不执行，远程只缓存建立起点之后的新内容")
			break
		}
	}
	return st
}

func (b *Backend) mustListSubscriptions() []subscription.Subscription {
	subs, _ := b.subs.List()
	return subs
}

func errorCategory(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "upload"):
		return "上传"
	case strings.Contains(m, "live") || strings.Contains(m, "record"):
		return "录制"
	case strings.Contains(m, "subscription") || strings.Contains(m, "check"):
		return "检查"
	case strings.Contains(m, "download") || strings.Contains(m, "task"):
		return "缓存"
	case strings.Contains(m, "cookie") || strings.Contains(m, "login") || strings.Contains(m, "auth"):
		return "登录"
	default:
		return "其他"
	}
}

// ServerSubscriptions is the remote's subscription summary and the hash of
// the snapshot it last applied.
func (b *Backend) ServerSubscriptions() remote.SubscriptionsInfo {
	sync := b.loadSyncState()
	info := remote.SubscriptionsInfo{SyncedAt: sync.SyncedAt, Hash: sync.Hash}
	for _, s := range b.mustListSubscriptions() {
		info.Subscriptions++
		info.Accounts += len(s.Accounts)
	}
	return info
}

// ApplyRemoteSnapshot replaces the remote's subscriptions, rules and synced
// settings with the snapshot's (remote.md §4) and records its hash.
func (b *Backend) ApplyRemoteSnapshot(raw []byte) (remote.SubscriptionsInfo, error) {
	if b.profile != ProfileRemote {
		return remote.SubscriptionsInfo{}, errors.New("only the remote applies a sync snapshot")
	}
	snap, err := subscription.ParseSnapshot(raw)
	if err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	if err := b.subs.ReplaceFromSnapshot(snap); err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	if snap.Settings != nil {
		if _, err := b.UpdateSettings(applySnapshotSettings(b.settings.Get(), *snap.Settings)); err != nil {
			b.logError("ApplyRemoteSnapshot.settings", err)
			return remote.SubscriptionsInfo{}, err
		}
	}
	subsCount, accsCount := snap.Counts()
	st, err := b.mutateSyncState(func(s *syncState) {
		s.Hash = snap.Hash()
		s.SyncedAt = time.Now().UTC().Format(time.RFC3339)
		s.Subscriptions, s.Accounts = subsCount, accsCount
	})
	if err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	b.logInfo(fmt.Sprintf("subscriptions synced: subscriptions=%d accounts=%d", st.Subscriptions, st.Accounts))
	b.subs.WakeScheduler()
	return b.ServerSubscriptions(), nil
}

// AcceptPushedCredential checks a Cookie the desktop pushed (nav for
// 哔哩哔哩, self profile for 抖音, PC getBasicInfo+profile/info for 微博)
// and keeps it only when the check passes; otherwise the stored credential is
// unchanged and the reason is returned (remote.md §5). The kept credential is
// marked remote_push: no refresh token, never renewed.
func (b *Backend) AcceptPushedCredential(ctx context.Context, siteID, cookie string) (remote.SiteStatus, error) {
	id, err := normalizeExplicitSiteID(siteID)
	if err != nil {
		return remote.SiteStatus{}, err
	}
	if strings.TrimSpace(cookie) == "" {
		return remote.SiteStatus{}, &site.Error{Site: id, Kind: site.ErrKindInvalidInput, Message: "没有收到 Cookie"}
	}
	var st credential.Status
	switch id {
	case site.Bilibili:
		st, err = b.ImportCookie(ctx, cookie, "")
	case site.Douyin:
		st, err = b.LoginWithCookie(ctx, string(site.Douyin), cookie, "")
	case site.Weibo:
		st, err = b.LoginWithCookie(ctx, string(site.Weibo), cookie, "")
	default:
		return remote.SiteStatus{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
	if err != nil {
		return remote.SiteStatus{}, err
	}
	if !st.LoggedIn {
		return remote.SiteStatus{}, &site.Error{Site: id, Kind: site.ErrKindAuthRequired, Message: "站点确认这个 Cookie 未登录"}
	}
	if _, err := b.credMgr.Store(string(id)).MarkPushed(CredentialSourceRemotePush); err != nil {
		return remote.SiteStatus{}, err
	}
	if _, err := b.mutateSyncState(func(sync *syncState) {
		if sync.CookieSyncedAt == nil {
			sync.CookieSyncedAt = map[string]string{}
		}
		sync.CookieSyncedAt[string(id)] = time.Now().UTC().Format(time.RFC3339)
	}); err != nil {
		b.logError("AcceptPushedCredential.syncState", err)
	}
	b.logInfo("credential pushed by the desktop: site=" + string(id))
	b.subs.WakeScheduler()
	for _, row := range b.ServerStatus().Sites {
		if row.Site == string(id) {
			return row, nil
		}
	}
	return remote.SiteStatus{Site: string(id), State: remote.SiteValid}, nil
}

// ClearPushedCredential clears the remote's login of one site.
func (b *Backend) ClearPushedCredential(siteID string) error {
	id, err := normalizeExplicitSiteID(siteID)
	if err != nil {
		return err
	}
	switch id {
	case site.Bilibili, site.Douyin, site.Weibo:
		_, err = b.LogoutSite(string(id))
	default:
		return &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
	if err == nil {
		_, _ = b.mutateSyncState(func(sync *syncState) {
			delete(sync.CookieSyncedAt, string(id))
		})
	}
	return err
}

// ExitGracefully is SIGTERM on the remote: the same path as 退出 in the
// tray (tasks paused, recordings finalized), bounded by timeout.
func (b *Backend) ExitGracefully(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		b.PauseForExit()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
