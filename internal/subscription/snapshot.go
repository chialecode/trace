package subscription

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// SortSnapshotAccountsBySiteOrder sorts snapshot accounts in-place by canonical
// site order (bilibili -> douyin -> weibo -> x), keeping stable relative order
// for multiple accounts on the same site (DEC-071).
func SortSnapshotAccountsBySiteOrder(accounts []SnapshotAccount) {
	if len(accounts) < 2 {
		return
	}
	slices.SortStableFunc(accounts, func(a, b SnapshotAccount) int {
		return cmp.Compare(AccountSiteOrderRank(a.Site), AccountSiteOrderRank(b.Site))
	})
}

// The subscription snapshot is both the file 导出订阅 writes
// (trace-subscriptions.json) and what the desktop sends to the remote
// (docs/design/remote.md §4, §6.4). It holds no Cookie and no per-machine
// state (check times, quotas, the cache folder).
const (
	SnapshotFormat   = "trace-subscriptions"
	SnapshotVersion  = 1
	SnapshotFileName = "trace-subscriptions.json"
)

// Snapshot is a set of subscriptions with their capability tags and part of
// the settings.
type Snapshot struct {
	Format        string                 `json:"format"`
	Version       int                    `json:"version"`
	ExportedAt    string                 `json:"exportedAt,omitempty"`
	Node          *SnapshotNode          `json:"node,omitempty"`
	Subscriptions []SnapshotSubscription `json:"subscriptions"`
	Settings      *SnapshotSettings      `json:"settings,omitempty"`
}

// SnapshotNode names the installation that made the snapshot.
type SnapshotNode struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// SnapshotSubscription is one subscription of a snapshot.
type SnapshotSubscription struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Note           string            `json:"note,omitempty"`
	Avatar         AvatarRef         `json:"avatar"`
	CreatedAt      string            `json:"createdAt,omitempty"`
	Tags           []string          `json:"tags"`
	AutoCacheSince string            `json:"autoCacheSince,omitempty"`
	Accounts       []SnapshotAccount `json:"accounts"`
	Links          []CustomLink      `json:"links,omitempty"`
}

// SnapshotAccount is an account of a subscription.
type SnapshotAccount struct {
	Site     string `json:"site"`
	UID      string `json:"uid"`
	UniqueID string `json:"uniqueId,omitempty"`
	WebRID   string `json:"webRid,omitempty"`
	Name     string `json:"name,omitempty"`
	Face     string `json:"face,omitempty"`
}

// SnapshotSettings is the part of the settings that follows the
// subscriptions to the remote. Proxy, save location, window
// and player settings are never included.
type SnapshotSettings struct {
	VideoQuality         string   `json:"videoQuality,omitempty"`
	CodecOrder           []string `json:"codecOrder,omitempty"`
	AudioQuality         string   `json:"audioQuality,omitempty"`
	MultiPart            string   `json:"multiPart,omitempty"`
	Concurrency          int      `json:"concurrency,omitempty"`
	Danmaku              bool     `json:"danmaku"`
	Subtitles            bool     `json:"subtitles"`
	AISubtitles          bool     `json:"aiSubtitles"`
	CheckIntervalMin     int      `json:"checkIntervalMin,omitempty"`
	LiveCheckIntervalMin int      `json:"liveCheckIntervalMin,omitempty"`
}

// ErrBadSnapshot reports a file that is not a subscription snapshot of this
// version.
var ErrBadSnapshot = errors.New("不是 TRACE 导出的订阅文件，或版本不同")

// ParseSnapshot reads a snapshot and refuses other formats and versions
// (both sides are always the same version, DEC-062 ②). Pre-DEC-070 v1 snapshot
// files carrying legacy "rules" + "ruleId" without "tags" are transparently
// normalized into capability tags.
func ParseSnapshot(raw []byte) (Snapshot, error) {
	type rawSnapshotSub struct {
		SnapshotSubscription
		RawTags json.RawMessage `json:"tags"`
		RuleID  string          `json:"ruleId"`
	}
	var wire struct {
		Format        string            `json:"format"`
		Version       int               `json:"version"`
		ExportedAt    string            `json:"exportedAt,omitempty"`
		Node          *SnapshotNode     `json:"node,omitempty"`
		Rules         []legacyCacheRule `json:"rules,omitempty"`
		Subscriptions []rawSnapshotSub  `json:"subscriptions"`
		Settings      *SnapshotSettings `json:"settings,omitempty"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Snapshot{}, ErrBadSnapshot
	}
	if wire.Format != SnapshotFormat || wire.Version != SnapshotVersion {
		return Snapshot{}, ErrBadSnapshot
	}
	legacyRules := make(map[string]legacyCacheRule, len(wire.Rules))
	for _, r := range wire.Rules {
		if r.ID != "" {
			legacyRules[r.ID] = r
		}
	}

	snap := Snapshot{
		Format:        wire.Format,
		Version:       wire.Version,
		ExportedAt:    wire.ExportedAt,
		Node:          wire.Node,
		Settings:      wire.Settings,
		Subscriptions: make([]SnapshotSubscription, 0, len(wire.Subscriptions)),
	}
	seen := map[string]bool{}
	seenSubIDs := map[string]bool{}
	for _, rawSub := range wire.Subscriptions {
		sub := rawSub.SnapshotSubscription
		sub.ID = strings.TrimSpace(sub.ID)
		if sub.ID == "" {
			return Snapshot{}, fmt.Errorf("订阅文件里有缺少 ID 的订阅")
		}
		if seenSubIDs[sub.ID] {
			return Snapshot{}, fmt.Errorf("订阅文件里有重复的订阅 ID：%s", sub.ID)
		}
		seenSubIDs[sub.ID] = true

		rawTagsTrim := strings.TrimSpace(string(rawSub.RawTags))
		if len(rawTagsTrim) > 0 && rawTagsTrim != "null" {
			var parsedTags []string
			if err := json.Unmarshal(rawSub.RawTags, &parsedTags); err != nil {
				return Snapshot{}, ErrBadSnapshot
			}
			sub.Tags = NormalizeTags(parsedTags)
		} else if rawSub.RuleID != "" {
			if lr, ok := legacyRules[rawSub.RuleID]; ok {
				sub.Tags = legacyCacheRuleToTags(lr)
			} else {
				sub.Tags = []string{}
			}
		} else {
			sub.Tags = NormalizeTags(sub.Tags)
		}

		var accs []SnapshotAccount
		for _, a := range sub.Accounts {
			ref, ok := normalizeSubscriptionAccountForLoad(AccountRef{Site: a.Site, UID: a.UID, UniqueID: a.UniqueID, WebRID: a.WebRID, Name: a.Name, Face: a.Face})
			if !ok {
				continue
			}
			key := ref.Site + ":" + ref.UID
			if seen[key] {
				continue
			}
			seen[key] = true
			accs = append(accs, SnapshotAccount{Site: ref.Site, UID: ref.UID, UniqueID: ref.UniqueID, WebRID: ref.WebRID, Name: ref.Name, Face: ref.Face})
		}
		SortSnapshotAccountsBySiteOrder(accs)
		sub.Accounts = accs
		sub.Links = normalizeCustomLinksForLoad(sub.Links)
		snap.Subscriptions = append(snap.Subscriptions, sub)
	}
	return snap, nil
}

// Hash is the snapshot's content hash: subscriptions (ID, name, note, accounts,
// links, capability tags, auto-cache start) and settings. Display data that the
// checks refresh (account names and avatars) and the export time are not part
// of it, so "有改动未同步" only follows real changes.
func (s Snapshot) Hash() string {
	type acc struct{ Site, UID string }
	type sub struct {
		ID, Name, Note, Since string
		Tags                  []string
		Accounts              []acc
		Links                 []CustomLink `json:",omitempty"`
	}
	content := struct {
		Format   string
		Version  int
		Subs     []sub
		Settings *SnapshotSettings
	}{Format: s.Format, Version: s.Version, Settings: s.Settings}
	for _, x := range s.Subscriptions {
		item := sub{
			ID:    x.ID,
			Name:  x.Name,
			Note:  x.Note,
			Tags:  NormalizeTags(x.Tags),
			Since: x.AutoCacheSince,
			Links: normalizeCustomLinksForLoad(x.Links),
		}
		ordered := append([]SnapshotAccount(nil), x.Accounts...)
		SortSnapshotAccountsBySiteOrder(ordered)
		for _, a := range ordered {
			item.Accounts = append(item.Accounts, acc{normalizeLegacyAccountSite(a.Site), a.UID})
		}
		content.Subs = append(content.Subs, item)
	}
	raw, _ := json.Marshal(content)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Counts returns the number of subscriptions and accounts.
func (s Snapshot) Counts() (subs, accounts int) {
	for _, x := range s.Subscriptions {
		accounts += len(x.Accounts)
	}
	return len(s.Subscriptions), accounts
}

// Snapshot returns the current subscriptions and their capability tags; the
// caller adds the settings part and the node.
func (m *Manager) Snapshot() (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	subs, err := m.listLocked()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Format: SnapshotFormat, Version: SnapshotVersion}
	slices.SortStableFunc(subs, func(a, b Subscription) int { return strings.Compare(a.ID, b.ID) })
	for _, s := range subs {
		tags := SubRulesToTags(s.Rules)
		out := SnapshotSubscription{
			ID:        s.ID,
			Name:      s.Name,
			Note:      s.Note,
			Avatar:    s.Avatar,
			CreatedAt: s.CreatedAt,
			Tags:      tags,
			Links:     cloneCustomLinks(s.Links),
		}
		if s.Rules.AutoCache.Enabled {
			out.AutoCacheSince = s.Rules.AutoCache.Since
		}
		for _, a := range s.Accounts {
			out.Accounts = append(out.Accounts, SnapshotAccount{Site: a.Site, UID: a.UID, UniqueID: a.UniqueID, WebRID: a.WebRID, Name: a.Name, Face: a.Face})
		}
		SortSnapshotAccountsBySiteOrder(out.Accounts)
		snap.Subscriptions = append(snap.Subscriptions, out)
	}
	return snap, nil
}

// snapshotToSubscriptionLocked builds the subscription a snapshot describes
// with its capability tags.
func (m *Manager) snapshotToSubscriptionLocked(x SnapshotSubscription) Subscription {
	now := m.nowFunc().UTC().Format(time.RFC3339)
	tags := NormalizeTags(x.Tags)
	sub := Subscription{
		ID:        x.ID,
		Name:      x.Name,
		Note:      x.Note,
		Avatar:    x.Avatar,
		CreatedAt: x.CreatedAt,
		Tags:      tags,
		Links:     normalizeCustomLinksForLoad(x.Links),
	}
	for _, a := range x.Accounts {
		sub.Accounts = append(sub.Accounts, AccountRef{Site: a.Site, UID: a.UID, UniqueID: a.UniqueID, WebRID: a.WebRID, Name: a.Name, Face: a.Face, AddedAt: now})
	}
	rules := TagsToSubRules(tags)
	if rules.AutoCache.Enabled {
		rules.AutoCache.Since = x.AutoCacheSince
		if rules.AutoCache.Since == "" {
			rules.AutoCache.Since = now
		}
	}
	m.applyRulesLocked(&sub, rules)
	return sub
}

// ReplaceFromSnapshot makes the subscriptions and their capability tags exactly
// the snapshot's (the remote's 同步订阅, remote.md §4) in one write. Each
// account's check state (start point, last check, discovered updates) is
// kept by site and UID; accounts left out are no longer checked, and no file
// is deleted. On any failure the previous subscriptions stay.
func (m *Manager) ReplaceFromSnapshot(snap Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prevSubs, _ := m.listSubsOnlyLocked()
	prevAccs := map[string]AccountRef{}
	for _, s := range prevSubs {
		for _, a := range s.Accounts {
			prevAccs[normalizeLegacyAccountSite(a.Site)+":"+a.UID] = a
		}
	}
	var subs []Subscription
	nextAccs := map[string]AccountRef{}
	for _, x := range snap.Subscriptions {
		sub := m.snapshotToSubscriptionLocked(x)
		for _, a := range sub.Accounts {
			nextAccs[normalizeLegacyAccountSite(a.Site)+":"+a.UID] = a
		}
		subs = append(subs, sub)
	}
	if err := m.replaceAllLocked(subs); err != nil {
		return err
	}
	if m.updatesDB != nil {
		nowStr := m.nowFunc().UTC().Format(time.RFC3339)
		for k, a := range prevAccs {
			if _, stillPresent := nextAccs[k]; !stillPresent {
				_, _ = m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = ? WHERE site = ? AND uid = ?`, nowStr, normalizeLegacyAccountSite(a.Site), a.UID)
			}
		}
		for _, a := range nextAccs {
			_, _ = m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = '' WHERE site = ? AND uid = ?`, normalizeLegacyAccountSite(a.Site), a.UID)
		}
	}
	return nil
}

// replaceAllLocked writes subs as subscriptions.json and reloads them; the
// previous file is restored when anything fails.
func (m *Manager) replaceAllLocked(subs []Subscription) error {
	if m.subsPath == "" {
		return fmt.Errorf("subscription: no subscriptions.json path configured")
	}
	if subs == nil {
		subs = []Subscription{}
	}
	for i := range subs {
		subs[i].Tags = SubRulesToTags(subs[i].Rules)
	}
	prevRaw, prevErr := os.ReadFile(m.subsPath)
	sf := subscriptionsFile{Version: SubscriptionsSchemaVersion, Subscriptions: subs}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if m.diskWriteHook != nil {
		if err := m.diskWriteHook(); err != nil {
			return err
		}
	}
	if prevErr == nil && len(prevRaw) > 0 {
		if err := storage.WriteFileAtomic(m.subsPath+".bak", prevRaw, 0o600); err != nil {
			return err
		}
	}
	if err := storage.WriteFileAtomic(m.subsPath, data, 0o600); err != nil {
		return err
	}
	if err := m.loadSubscriptionsFromDiskLocked(); err != nil {
		restoreErr := error(nil)
		if prevErr == nil {
			restoreErr = storage.WriteFileAtomic(m.subsPath, prevRaw, 0o600)
		} else {
			restoreErr = os.Remove(m.subsPath)
		}
		return errors.Join(err, restoreErr, m.loadSubscriptionsFromDiskLocked())
	}
	return nil
}
