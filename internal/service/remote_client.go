package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
)

// RemoteService is the desktop's side of the remote (Settings › 远程,
// docs/design/remote.md §3, §5, §7). Only one remote is kept for now.
type RemoteService interface {
	RemoteConnection() (RemoteConnectionInfo, bool)
	ConnectRemote(ctx context.Context, in RemoteConnectInput) (RemoteView, error)
	RemoveRemote() error
	RemoteStatus(ctx context.Context) (RemoteView, error)
	SyncSubscriptionsToRemote(ctx context.Context) (remote.SubscriptionsInfo, error)
	PushCredentialToRemote(ctx context.Context, siteID string) (remote.SiteStatus, error)
	ClearRemoteCredential(ctx context.Context, siteID string) error
	RetryRemoteUpload(ctx context.Context) error
	LocalSnapshotHash() string
}

var _ RemoteService = (*Backend)(nil)

// RemoteConnectInput is a pasted connection code, or the three parts typed
// separately.
type RemoteConnectInput struct {
	Code        string `json:"code,omitempty"`
	URL         string `json:"url,omitempty"`
	Token       string `json:"-"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// RemoteConnectionInfo is the saved connection (never the token).
type RemoteConnectionInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name,omitempty"`
	URL            string   `json:"url"`
	Fingerprint    string   `json:"fingerprint"`
	PendingCookies []string `json:"pendingCookies,omitempty"`
}

// RemoteView is one read of the remote for Settings › 远程.
type RemoteView struct {
	Connection RemoteConnectionInfo `json:"connection"`
	Status     remote.Status        `json:"status"`
	LocalHash  string               `json:"localHash"`
	InSync     bool                 `json:"inSync"`
	FetchedAt  string               `json:"fetchedAt"`
}

// ErrNoRemote is returned when no remote is connected.
var ErrNoRemote = errors.New("还没有连接远程")

type remoteLink struct {
	mu      sync.Mutex
	keyHash string
	token   string
	client  *remote.Client
}

func (r *remoteLink) activeToken() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token
}

func (r *remoteLink) closeClient() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
	r.keyHash = ""
	r.token = ""
	r.mu.Unlock()
}

// remoteClientFactory builds the client; tests replace it.
var remoteClientFactory = func(conn remote.Connection, version string) (*remote.Client, error) {
	return remote.NewClient(conn, version)
}

func (b *Backend) remoteTokenPath(id string) string {
	return filepath.Join(b.loc.CredentialsDir, "remote-"+id+".bin")
}

// RemoteConnection returns the saved connection.
func (b *Backend) RemoteConnection() (RemoteConnectionInfo, bool) {
	rs := b.settings.Get().Remote
	if len(rs.Connections) == 0 {
		return RemoteConnectionInfo{}, false
	}
	c := rs.Connections[0]
	info := RemoteConnectionInfo{ID: c.ID, Name: c.Name, URL: c.URL, Fingerprint: c.Fingerprint}
	for _, p := range rs.PendingCookies {
		info.PendingCookies = append(info.PendingCookies, p.Site)
	}
	return info, true
}

func (b *Backend) remoteClient() (*remote.Client, RemoteConnectionInfo, error) {
	info, ok := b.RemoteConnection()
	if !ok {
		return nil, RemoteConnectionInfo{}, ErrNoRemote
	}
	token, ok, err := credential.LoadSecret(b.remoteTokenPath(info.ID), credential.RemoteTokenEntropy)
	if err != nil {
		return nil, info, err
	}
	if !ok {
		return nil, info, errors.New("远程的访问令牌读不出来了，请重新粘贴连接码")
	}
	if b.remote == nil {
		b.remote = &remoteLink{}
	}
	b.remote.mu.Lock()
	defer b.remote.mu.Unlock()
	sum := sha256.Sum256([]byte(info.ID + "|" + info.URL + "|" + info.Fingerprint + "|" + string(token)))
	keyHash := hex.EncodeToString(sum[:])
	b.remote.token = string(token)
	if b.remote.client != nil && b.remote.keyHash == keyHash {
		return b.remote.client, info, nil
	}
	if b.remote.client != nil {
		b.remote.client.Close()
		b.remote.client = nil
	}
	c, err := remoteClientFactory(remote.Connection{URL: info.URL, Token: string(token), Fingerprint: info.Fingerprint, Name: info.Name}, appdirs.BuildInfo().Version)
	if err != nil {
		return nil, info, err
	}
	b.remote.client, b.remote.keyHash = c, keyHash
	return c, info, nil
}

// ConnectRemote checks the connection by reading the remote's status and
// saves it only when that works: the address, name and fingerprint in
// settings.json, the token in the credential store.
func (b *Backend) ConnectRemote(ctx context.Context, in RemoteConnectInput) (RemoteView, error) {
	var conn remote.Connection
	var err error
	if strings.TrimSpace(in.Code) != "" {
		conn, err = remote.DecodeCode(in.Code)
	} else {
		conn, err = remote.Connection{URL: in.URL, Token: in.Token, Fingerprint: in.Fingerprint}.Normalize()
	}
	if err != nil {
		return RemoteView{}, err
	}
	client, err := remoteClientFactory(conn, appdirs.BuildInfo().Version)
	if err != nil {
		return RemoteView{}, err
	}
	st, err := client.Status(ctx)
	if err != nil {
		client.Close()
		return RemoteView{}, err
	}
	client.Close()
	id := newRemoteID()
	prev, hadPrev := b.RemoteConnection()
	if err := credential.SaveSecret(b.remoteTokenPath(id), credential.RemoteTokenEntropy, []byte(conn.Token)); err != nil {
		return RemoteView{}, err
	}
	name := firstNonEmpty(conn.Name, st.Node.Name)
	if _, err := b.mutateRemoteSettings(func(rs *settings.RemoteSettings) {
		rs.Connections = []settings.RemoteConnection{{ID: id, Name: name, URL: conn.URL, Fingerprint: conn.Fingerprint, AddedAt: time.Now().UTC().Format(time.RFC3339)}}
		rs.PendingCookies = nil
	}); err != nil {
		_ = credential.DeleteSecret(b.remoteTokenPath(id))
		return RemoteView{}, err
	}
	// Only remove the previous token and reset the cached client after the new
	// settings and token have been durably saved.
	if b.remote != nil {
		b.remote.closeClient()
		b.remote.mu.Lock()
		b.remote.token = conn.Token
		b.remote.mu.Unlock()
	}
	if hadPrev && prev.ID != "" && prev.ID != id {
		_ = credential.DeleteSecret(b.remoteTokenPath(prev.ID))
	}
	b.logInfo("remote connected: version=" + st.Version)
	return b.remoteView(st), nil
}

// RemoveRemote forgets the connection and deletes its token. The remote
// itself is not changed (its Cookies stay until cleared there).
func (b *Backend) RemoveRemote() error {
	info, ok := b.RemoteConnection()
	if !ok {
		return nil
	}
	if b.remote != nil {
		b.remote.closeClient()
	}
	if _, err := b.mutateRemoteSettings(func(rs *settings.RemoteSettings) {
		rs.Connections = nil
		rs.PendingCookies = nil
	}); err != nil {
		return err
	}
	b.logInfo("remote removed")
	return credential.DeleteSecret(b.remoteTokenPath(info.ID))
}

func (b *Backend) remoteView(st remote.Status) RemoteView {
	info, _ := b.RemoteConnection()
	local := b.LocalSnapshotHash()
	return RemoteView{Connection: info, Status: st, LocalHash: local, InSync: local != "" && local == st.Subscriptions.Hash, FetchedAt: time.Now().UTC().Format(time.RFC3339)}
}

// RemoteStatus reads the remote's status; pending Cookie pushes are retried
// after a successful read.
func (b *Backend) RemoteStatus(ctx context.Context) (RemoteView, error) {
	client, _, err := b.remoteClient()
	if err != nil {
		return RemoteView{}, err
	}
	st, err := client.Status(ctx)
	if err != nil {
		return RemoteView{}, err
	}
	if b.retryPendingRemoteCookies(ctx) {
		if again, err := client.Status(ctx); err == nil {
			st = again
		}
	}
	return b.remoteView(st), nil
}

// SyncSubscriptionsToRemote replaces the remote's subscriptions with the
// local ones (the desktop is the only source, Q-022 item 4).
func (b *Backend) SyncSubscriptionsToRemote(ctx context.Context) (remote.SubscriptionsInfo, error) {
	client, _, err := b.remoteClient()
	if err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	snap, err := b.SubscriptionSnapshot()
	if err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	raw, err := marshalSnapshot(snap)
	if err != nil {
		return remote.SubscriptionsInfo{}, err
	}
	info, err := client.PutSubscriptions(ctx, raw)
	if err != nil {
		b.logError("SyncSubscriptionsToRemote", err)
		return info, err
	}
	b.logInfo(fmt.Sprintf("subscriptions synced to the remote: subscriptions=%d accounts=%d", info.Subscriptions, info.Accounts))
	return info, nil
}

// PushCredentialToRemote sends one site's Cookie (never a refresh token).
// A push that fails for a reason other than the remote rejecting the
// Cookie is remembered and retried later.
func (b *Backend) PushCredentialToRemote(ctx context.Context, siteID string) (remote.SiteStatus, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return remote.SiteStatus{}, err
	}
	store := b.credMgr.Store(string(id))
	if store == nil || !store.HasActiveCredential() {
		return remote.SiteStatus{}, fmt.Errorf("本地还没有登录%s", id.DisplayName())
	}
	client, _, err := b.remoteClient()
	if err != nil {
		return remote.SiteStatus{}, err
	}
	st, err := client.PushCredential(ctx, string(id), remote.CredentialPush{Cookie: store.CookieHeader()})
	if err != nil {
		// X cookies require explicit user confirmation per push and never enter background auto-retry.
		if id != site.X && remote.KindOf(err) != remote.KindRejected {
			b.markRemoteCookiePending(string(id), true)
		}
		b.logError("PushCredentialToRemote", err)
		return st, err
	}
	b.markRemoteCookiePending(string(id), false)
	b.logInfo("cookie pushed to the remote: site=" + string(id))
	return st, nil
}

// ClearRemoteCredential clears the remote's login of one site (远程的
// “清除远程登录”); logging out locally does not do this.
func (b *Backend) ClearRemoteCredential(ctx context.Context, siteID string) error {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return err
	}
	client, _, err := b.remoteClient()
	if err != nil {
		return err
	}
	if err := client.DeleteCredential(ctx, string(id)); err != nil {
		return err
	}
	b.markRemoteCookiePending(string(id), false)
	return nil
}

// RetryRemoteUpload makes the remote retry failed uploads now.
func (b *Backend) RetryRemoteUpload(ctx context.Context) error {
	client, _, err := b.remoteClient()
	if err != nil {
		return err
	}
	return client.RetryUpload(ctx)
}

func (b *Backend) mutateRemoteSettings(fn func(*settings.RemoteSettings)) (settings.Settings, error) {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	cur := b.settings.Get()
	fn(&cur.Remote)
	return b.settings.Update(cur)
}

func (b *Backend) markRemoteCookiePending(siteID string, pending bool) {
	if _, ok := b.RemoteConnection(); !ok {
		return
	}
	cur := b.settings.Get().Remote.PendingCookies
	has := slices.ContainsFunc(cur, func(p settings.PendingCookie) bool { return p.Site == siteID })
	if has == pending {
		return
	}
	if _, err := b.mutateRemoteSettings(func(rs *settings.RemoteSettings) {
		rs.PendingCookies = slices.DeleteFunc(rs.PendingCookies, func(p settings.PendingCookie) bool { return p.Site == siteID })
		if pending {
			rs.PendingCookies = append(rs.PendingCookies, settings.PendingCookie{Site: siteID, Since: time.Now().UTC().Format(time.RFC3339)})
		}
	}); err != nil {
		b.logError("markRemoteCookiePending", err)
	}
	b.events.Publish(EventSettingsChanged, "remote")
}

// retryPendingRemoteCookies pushes the sites marked "Cookie 待同步"; it
// reports whether anything was pushed.
func (b *Backend) retryPendingRemoteCookies(ctx context.Context) bool {
	info, ok := b.RemoteConnection()
	if !ok || len(info.PendingCookies) == 0 {
		return false
	}
	pushed := false
	for _, s := range info.PendingCookies {
		// Never silently auto-push X cookies in background retry (x.md §15.1 #18).
		if strings.EqualFold(strings.TrimSpace(s), string(site.X)) {
			b.markRemoteCookiePending(s, false)
			continue
		}
		if _, err := b.PushCredentialToRemote(ctx, s); err == nil {
			pushed = true
		} else if remote.KindOf(err) == remote.KindRejected {
			// The remote checked and refused it: retrying the same Cookie
			// would not help; the next local login pushes again.
			b.markRemoteCookiePending(s, false)
		}
	}
	return pushed
}

// RetryPendingRemoteCookies is the background retry of "Cookie 待同步"
// (every 10 minutes); exported for tests.
func (b *Backend) RetryPendingRemoteCookies(ctx context.Context) bool {
	return b.retryPendingRemoteCookies(ctx)
}

// runRemoteCookieRetry retries pending Cookie pushes every 10 minutes. It
// sends nothing while nothing is pending (Settings › 远程 closed means no
// other remote request, remote.md §7).
func (b *Backend) runRemoteCookieRetry(ctx context.Context) {
	defer b.bgWg.Done()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.retryPendingRemoteCookies(ctx)
		}
	}
}

// afterLocalLogin pushes a site's new Cookie to the remote after a local
// login or renewal (remote.md §5), in the background. Nothing happens when
// no remote is connected, on the remote itself, or for site.X (X cookies
// must NEVER auto-push due to datacenter IP risk; x.md §15.1 #18).
func (b *Backend) afterLocalLogin(id site.ID) {
	if b.profile == ProfileRemote || id == site.X {
		return
	}
	if _, ok := b.RemoteConnection(); !ok {
		return
	}
	b.goBg(func(bgCtx context.Context) {
		ctx, cancel := context.WithTimeout(bgCtx, 2*time.Minute)
		defer cancel()
		_, _ = b.PushCredentialToRemote(ctx, string(id))
	})
}

func newRemoteID() string {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	return hex.EncodeToString(rnd[:])
}
