package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

// Subscribed accounts' avatars, kept as small local PNGs under
// <DataDir>/avatars/. Douyin avatar URLs carry query strings (some signed and
// expiring), so subscriptions.json never stores them; both sites keep a local
// copy preferred by the UI. The file name is a hash of site and account id; no
// URL is written. Only files matching avatarFileRe are ever removed.

const avatarRefreshAge = 7 * 24 * time.Hour

var avatarFileRe = regexp.MustCompile(`^(bilibili|douyin|weibo|x)-[0-9a-f]{20}\.png$`)

func (b *Backend) avatarDir() string {
	if b.loc.DataDir == "" {
		return ""
	}
	return filepath.Join(b.loc.DataDir, "avatars")
}

func avatarFileName(siteID, uid string) string {
	siteID = strings.ToLower(strings.TrimSpace(siteID))
	uid = strings.TrimSpace(uid)
	if !site.ID(siteID).Valid() || uid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(siteID + ":" + uid))
	return siteID + "-" + hex.EncodeToString(sum[:10]) + ".png"
}

// AccountAvatarPath returns the local avatar of a subscribed account, or "".
func (b *Backend) AccountAvatarPath(siteID, uid string) string {
	name, dir := avatarFileName(siteID, uid), b.avatarDir()
	if name == "" || dir == "" {
		return ""
	}
	path := filepath.Join(dir, name)
	if !isValidLocalAvatarFile(path) {
		return ""
	}
	return path
}

func isValidLocalAvatarFile(path string) bool {
	ext, sz, _, ok := site.ValidateExistingImageFile(path, "", 0, 0, false)
	return ok && sz > 0 && ext == ".png"
}

// rememberAvatar fetches and atomically saves a local PNG avatar in the background
// when it is missing or older than avatarRefreshAge, enforcing desktop-only profile
// and INV-07 per-site login before issuing any CDN request.
func (b *Backend) rememberAvatar(siteID, uid, rawFace string, defaultBilibili bool, onSaved func(normSite string)) {
	if b.profile == ProfileRemote {
		return
	}
	normSite := strings.ToLower(strings.TrimSpace(siteID))
	if normSite == "" && defaultBilibili {
		normSite = string(site.Bilibili)
	}
	if !site.ID(normSite).Valid() || strings.TrimSpace(rawFace) == "" {
		return
	}
	sid := site.ID(normSite)
	if b.requireSiteLogin(sid) != nil {
		return
	}
	name, dir := avatarFileName(normSite, uid), b.avatarDir()
	if name == "" || dir == "" {
		return
	}
	if (sid == site.Douyin && b.douyin == nil) || (sid == site.Bilibili && b.bili == nil) || (sid == site.Weibo && b.weibo == nil) || (sid == site.X && b.x == nil) {
		return
	}
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < avatarRefreshAge && isValidLocalAvatarFile(path) {
		return
	}
	if _, busy := b.avatarInflight.LoadOrStore(name, struct{}{}); busy {
		return
	}
	started := b.goBg(func(ctx context.Context) {
		defer b.avatarInflight.Delete(name)
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		thumb := b.fetchHistoryThumbForSite(fetchCtx, sid, "avatar", rawFace)
		if len(thumb.Data) == 0 || fetchCtx.Err() != nil {
			return
		}
		if ext, ok := site.SniffImageMagicBytes(thumb.Data); !ok || ext != ".png" || site.LooksLikeExpiredPlaceholderImageBytes(thumb.Data, rawFace, 0, 0, false) {
			return
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		if err := storage.WriteFileAtomic(path, thumb.Data, 0o600); err != nil {
			return
		}
		if onSaved != nil {
			onSaved(normSite)
		}
	})
	if !started {
		b.avatarInflight.Delete(name)
	}
}

// rememberAccountAvatar keeps a local copy of a subscribed account's avatar
// in the background when it is missing or older than a week.
func (b *Backend) rememberAccountAvatar(siteID, uid, rawFace string) {
	b.rememberAvatar(siteID, uid, rawFace, true, func(string) {
		if b.events != nil {
			b.events.Publish(EventSubscriptionChecked, "avatar")
		}
	})
}

func (b *Backend) rememberAccountAvatars(accounts []subscription.AccountRef) {
	for _, acc := range accounts {
		b.rememberAccountAvatar(acc.Site, acc.UID, acc.Face)
	}
}

func (b *Backend) rememberWeiboPostAvatars(post *weibo.Post) {
	if post == nil {
		return
	}
	if post.Author.UID != "" && post.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.Weibo), post.Author.UID, post.Author.AvatarURL)
	}
	if post.Retweeted != nil && post.Retweeted.Author.UID != "" && post.Retweeted.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.Weibo), post.Retweeted.Author.UID, post.Retweeted.Author.AvatarURL)
	}
}

func (b *Backend) rememberXPostAvatars(post *x.Post) {
	if post == nil {
		return
	}
	if post.Author.RestID != "" && post.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.X), post.Author.RestID, post.Author.AvatarURL)
	}
	if post.Retweeted != nil && post.Retweeted.Author.RestID != "" && post.Retweeted.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.X), post.Retweeted.Author.RestID, post.Retweeted.Author.AvatarURL)
	}
	if post.Quoted != nil && post.Quoted.Author.RestID != "" && post.Quoted.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.X), post.Quoted.Author.RestID, post.Quoted.Author.AvatarURL)
	}
}

// rememberQueueAvatar caches a local PNG copy of a queue account's avatar (for Bilibili or Douyin)
// so cache-queue.json never needs to store remote avatar URLs or query strings.
func (b *Backend) rememberQueueAvatar(siteID, uid, rawFace string) {
	b.rememberAvatar(siteID, uid, rawFace, false, func(normSite string) {
		if b.events != nil {
			b.events.PublishSite(EventCacheQueueUpdated, normSite, "avatar")
		}
	})
}

// pruneAccountAvatars removes local avatars of accounts no subscription, cache queue task, or library item uses.
func (b *Backend) pruneAccountAvatars() {
	dir := b.avatarDir()
	if dir == "" || b.subs == nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	subs, err := b.subs.List()
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, sub := range subs {
		for _, acc := range sub.Accounts {
			if name := avatarFileName(acc.Site, acc.UID); name != "" {
				keep[name] = true
			}
		}
	}
	if b.queue != nil {
		for _, k := range b.queue.avatarKeys() {
			keep[k] = true
		}
	}
	if b.lib != nil {
		authors, err := b.lib.DistinctAuthorRefs()
		if err != nil {
			return
		}
		for _, au := range authors {
			if name := avatarFileName(au.Site, au.UID); name != "" {
				keep[name] = true
			}
		}
	}
	for _, e := range entries {
		if e.Type().IsRegular() && avatarFileRe.MatchString(e.Name()) && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
