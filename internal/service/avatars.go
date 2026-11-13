package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

// Subscribed accounts' avatars, kept as small local PNGs under
// <DataDir>/avatars/. Douyin avatar URLs carry query strings (some signed and
// expiring), so subscriptions.json never stores them and the avatar was lost
// at the next start; a Bilibili subscription made from a user page had no
// avatar URL at all until its first check (2026-11-15 feedback). Both sites
// now keep a local copy, which the interface prefers. The file name is a hash
// of site and account id; no URL is written. Only files matching avatarFileRe
// are ever removed.

const avatarRefreshAge = 7 * 24 * time.Hour

var avatarFileRe = regexp.MustCompile(`^(bilibili|douyin)-[0-9a-f]{20}\.png$`)

var avatarInflight sync.Map // file name -> struct{}

func (b *Backend) avatarDir() string {
	if b.loc.DataDir == "" {
		return ""
	}
	return filepath.Join(b.loc.DataDir, "avatars")
}

func avatarFileName(siteID, uid string) string {
	siteID = strings.ToLower(strings.TrimSpace(siteID))
	uid = strings.TrimSpace(uid)
	if (siteID != string(site.Bilibili) && siteID != string(site.Douyin)) || uid == "" {
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
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}
	return path
}

// rememberAccountAvatar keeps a local copy of a subscribed account's avatar
// in the background when it is missing or older than a week.
func (b *Backend) rememberAccountAvatar(siteID, uid, rawFace string) {
	normSite := strings.ToLower(strings.TrimSpace(siteID))
	if normSite == "" {
		normSite = string(site.Bilibili)
	}
	if strings.TrimSpace(rawFace) == "" {
		return
	}
	name, dir := avatarFileName(normSite, uid), b.avatarDir()
	if name == "" || dir == "" {
		return
	}
	if (normSite == string(site.Douyin) && b.douyin == nil) || (normSite == string(site.Bilibili) && b.bili == nil) {
		return
	}
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < avatarRefreshAge {
		return
	}
	if _, busy := avatarInflight.LoadOrStore(name, struct{}{}); busy {
		return
	}
	ctx := b.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}
	b.bgWg.Add(1)
	go func() {
		defer b.bgWg.Done()
		defer avatarInflight.Delete(name)
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		thumb := b.fetchHistoryThumbForSite(fetchCtx, site.ID(normSite), "avatar", rawFace)
		if len(thumb.Data) == 0 || fetchCtx.Err() != nil {
			return
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		tmp, err := os.CreateTemp(dir, ".avatar-*.tmp")
		if err != nil {
			return
		}
		_, werr := tmp.Write(thumb.Data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
			_ = os.Remove(tmp.Name())
			return
		}
		b.events.Publish(EventSubscriptionChecked, "avatar")
	}()
}

func (b *Backend) rememberAccountAvatars(accounts []subscription.AccountRef) {
	for _, acc := range accounts {
		b.rememberAccountAvatar(acc.Site, acc.UID, acc.Face)
	}
}

// rememberQueueAvatar caches a local PNG copy of a queue account's avatar (for Bilibili or Douyin)
// so cache-queue.json never needs to store remote avatar URLs or query strings.
func (b *Backend) rememberQueueAvatar(siteID, uid, rawFace string) {
	normSite := strings.ToLower(strings.TrimSpace(siteID))
	if (normSite != string(site.Bilibili) && normSite != string(site.Douyin)) || strings.TrimSpace(rawFace) == "" {
		return
	}
	name, dir := avatarFileName(normSite, uid), b.avatarDir()
	if name == "" || dir == "" {
		return
	}
	if normSite == string(site.Douyin) && b.douyin == nil {
		return
	}
	if normSite == string(site.Bilibili) && b.bili == nil {
		return
	}
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < avatarRefreshAge {
		return
	}
	if _, busy := avatarInflight.LoadOrStore(name, struct{}{}); busy {
		return
	}
	ctx := b.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}
	b.bgWg.Add(1)
	go func() {
		defer b.bgWg.Done()
		defer avatarInflight.Delete(name)
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		thumb := b.fetchHistoryThumbForSite(fetchCtx, site.ID(normSite), "avatar", rawFace)
		if len(thumb.Data) == 0 || fetchCtx.Err() != nil {
			return
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		tmp, err := os.CreateTemp(dir, ".avatar-*.tmp")
		if err != nil {
			return
		}
		_, werr := tmp.Write(thumb.Data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
			_ = os.Remove(tmp.Name())
			return
		}
		b.events.PublishSite(EventCacheQueueUpdated, normSite, "avatar")
	}()
}

// pruneAccountAvatars removes local avatars of accounts no subscription or cache queue task uses.
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
	for _, e := range entries {
		if e.Type().IsRegular() && avatarFileRe.MatchString(e.Name()) && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
