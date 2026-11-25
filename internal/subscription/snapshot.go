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

// Snapshot is a set of subscriptions with their shared rules and part of
// the settings.
type Snapshot struct {
	Format        string                 `json:"format"`
	Version       int                    `json:"version"`
	ExportedAt    string                 `json:"exportedAt,omitempty"`
	Node          *SnapshotNode          `json:"node,omitempty"`
	Rules         []CacheRule            `json:"rules"`
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
	RuleID         string            `json:"ruleId"`
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
// (both sides are always the same version, DEC-062 ②).
func ParseSnapshot(raw []byte) (Snapshot, error) {
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, ErrBadSnapshot
	}
	if snap.Format != SnapshotFormat || snap.Version != SnapshotVersion {
		return Snapshot{}, ErrBadSnapshot
	}
	seen := map[string]bool{}
	seenSubIDs := map[string]bool{}
	for i := range snap.Subscriptions {
		sub := &snap.Subscriptions[i]
		sub.ID = strings.TrimSpace(sub.ID)
		if sub.ID == "" {
			return Snapshot{}, fmt.Errorf("订阅文件里有缺少 ID 的订阅")
		}
		if seenSubIDs[sub.ID] {
			return Snapshot{}, fmt.Errorf("订阅文件里有重复的订阅 ID：%s", sub.ID)
		}
		seenSubIDs[sub.ID] = true
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
	}
	return snap, nil
}

// Hash is the snapshot's content hash: subscriptions (ID, name, accounts,
// links, rule, auto-cache start), rules and settings. Display data that the
// checks refresh (account names and avatars) and the export time are not part
// of it, so "有改动未同步" only follows real changes.
func (s Snapshot) Hash() string {
	type acc struct{ Site, UID string }
	type sub struct {
		ID, Name, Note, RuleID, Since string
		Accounts                      []acc
		Links                         []CustomLink `json:",omitempty"`
	}
	content := struct {
		Format   string
		Version  int
		Rules    []CacheRule
		Subs     []sub
		Settings *SnapshotSettings
	}{Format: s.Format, Version: s.Version, Rules: s.Rules, Settings: s.Settings}
	for _, x := range s.Subscriptions {
		item := sub{
			ID:     x.ID,
			Name:   x.Name,
			Note:   x.Note,
			RuleID: x.RuleID,
			Since:  x.AutoCacheSince,
			Links:  normalizeCustomLinksForLoad(x.Links),
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

// Snapshot returns the current subscriptions and rules; the caller adds the
// settings part and the node.
func (m *Manager) Snapshot() (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	subs, err := m.listLocked()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Format: SnapshotFormat, Version: SnapshotVersion, Rules: slices.Clone(m.rules)}
	slices.SortStableFunc(subs, func(a, b Subscription) int { return strings.Compare(a.ID, b.ID) })
	for _, s := range subs {
		out := SnapshotSubscription{
			ID:        s.ID,
			Name:      s.Name,
			Note:      s.Note,
			Avatar:    s.Avatar,
			CreatedAt: s.CreatedAt,
			RuleID:    s.RuleID,
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

// snapshotToSubscription builds the subscription a snapshot describes, on
// rule r.
func (m *Manager) snapshotToSubscriptionLocked(x SnapshotSubscription, r CacheRule) Subscription {
	now := m.nowFunc().UTC().Format(time.RFC3339)
	sub := Subscription{
		ID:        x.ID,
		Name:      x.Name,
		Note:      x.Note,
		Avatar:    x.Avatar,
		CreatedAt: x.CreatedAt,
		RuleID:    r.ID,
		Links:     normalizeCustomLinksForLoad(x.Links),
	}
	for _, a := range x.Accounts {
		sub.Accounts = append(sub.Accounts, AccountRef{Site: a.Site, UID: a.UID, UniqueID: a.UniqueID, WebRID: a.WebRID, Name: a.Name, Face: a.Face, AddedAt: now})
	}
	rules := ruleToSubRules(r)
	if rules.AutoCache.Enabled {
		rules.AutoCache.Since = x.AutoCacheSince
		if rules.AutoCache.Since == "" {
			rules.AutoCache.Since = now
		}
	}
	m.applyRulesLocked(&sub, rules)
	return sub
}

// ReplaceFromSnapshot makes the subscriptions and rules exactly the
// snapshot's (the remote's 同步订阅, remote.md §4) in one write. Each
// account's check state (start point, last check, discovered updates) is
// kept by site and UID; accounts left out are no longer checked, and no file
// is deleted. On any failure the previous subscriptions stay.
func (m *Manager) ReplaceFromSnapshot(snap Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rules := []CacheRule{defaultRule()}
	byID := map[string]CacheRule{DefaultRuleID: defaultRule()}
	for _, r := range snap.Rules {
		if r.ID == "" || r.ID == DefaultRuleID {
			continue
		}
		r.Types = normalizeRuleTypes(r.Types)
		if _, dup := byID[r.ID]; dup {
			continue
		}
		byID[r.ID] = r
		rules = append(rules, r)
	}
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
		r, ok := byID[x.RuleID]
		if !ok {
			r = defaultRule()
		}
		sub := m.snapshotToSubscriptionLocked(x, r)
		for _, a := range sub.Accounts {
			nextAccs[normalizeLegacyAccountSite(a.Site)+":"+a.UID] = a
		}
		subs = append(subs, sub)
	}
	if err := m.replaceAllLocked(subs, rules); err != nil {
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

// replaceAllLocked writes subs and rules as subscriptions.json and reloads
// them; the previous file is restored when anything fails.
func (m *Manager) replaceAllLocked(subs []Subscription, rules []CacheRule) error {
	if m.subsPath == "" {
		return fmt.Errorf("subscription: no subscriptions.json path configured")
	}
	if subs == nil {
		subs = []Subscription{}
	}
	prevRaw, prevErr := os.ReadFile(m.subsPath)
	prevRules := slices.Clone(m.rules)
	sf := subscriptionsFile{Version: SubscriptionsSchemaVersion, Subscriptions: subs, CacheRules: rules}
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
		m.rules = prevRules
		return errors.Join(err, restoreErr, m.loadSubscriptionsFromDiskLocked())
	}
	return nil
}
