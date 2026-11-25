package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ImportPreview is what 导入订阅 would change, shown before anything is
// written (docs/design/remote.md §6.4).
type ImportPreview struct {
	// New lists the imported subscriptions that become new subscriptions
	// (with the accounts that are not subscribed yet).
	New []ImportSub `json:"new"`
	// Existing lists imported accounts that already belong to a local
	// subscription: skipped by default, or merged into that subscription.
	Existing []ImportExistingAccount `json:"existing"`
	// Rules lists rules with an ID that exists locally with other settings:
	// keep the local one (default) or use the imported one.
	Rules []ImportRuleConflict `json:"rules"`
	// NewRules counts imported rules added as they are.
	NewRules int `json:"newRules"`
	// Unchanged counts imported subscriptions whose every account is
	// already subscribed.
	Unchanged int `json:"unchanged"`
}

// ImportSub is an imported subscription that would be created.
type ImportSub struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Accounts []string `json:"accounts"`
	RuleName string   `json:"ruleName"`
}

// ImportExistingAccount is an imported account a local subscription
// already has.
type ImportExistingAccount struct {
	Site          string `json:"site"`
	UID           string `json:"uid"`
	Name          string `json:"name"`
	ImportedSubID string `json:"importedSubId"`
	ImportedSub   string `json:"importedSub"`
	LocalSubID    string `json:"localSubId"`
	LocalSub      string `json:"localSub"`
}

// ImportRuleConflict is a rule whose ID exists locally with other settings.
type ImportRuleConflict struct {
	ID              string `json:"id"`
	LocalName       string `json:"localName"`
	LocalSummary    string `json:"localSummary"`
	ImportedName    string `json:"importedName"`
	ImportedSummary string `json:"importedSummary"`
}

// ImportChoices are the user's answers to a preview.
type ImportChoices struct {
	// MergeExisting adds an imported subscription's new accounts to the
	// local subscription that already has one of its accounts, instead of
	// creating a new subscription.
	MergeExisting bool `json:"mergeExisting"`
	// UseImportedRules lists conflicting rule IDs whose imported settings
	// replace the local ones.
	UseImportedRules []string `json:"useImportedRules,omitempty"`
}

type importPlan struct {
	preview ImportPreview
	// per imported subscription: the accounts not subscribed yet, and the
	// local subscription owning one of its accounts ("" when none).
	fresh map[string][]SnapshotAccount
	owner map[string]string
}

func (m *Manager) planImportLocked(snap Snapshot) (importPlan, error) {
	plan := importPlan{fresh: map[string][]SnapshotAccount{}, owner: map[string]string{}}
	local := map[string]CacheRule{}
	for _, r := range m.rules {
		local[r.ID] = r
	}
	ruleName := map[string]string{}
	for _, r := range snap.Rules {
		ruleName[r.ID] = r.Name
		if r.ID == DefaultRuleID {
			continue
		}
		if lr, ok := local[r.ID]; ok {
			if lr.Name != r.Name || !sameRuleSettings(lr, r) {
				plan.preview.Rules = append(plan.preview.Rules, ImportRuleConflict{ID: r.ID, LocalName: lr.Name, LocalSummary: RuleSummary(lr), ImportedName: r.Name, ImportedSummary: RuleSummary(r)})
			}
			continue
		}
		plan.preview.NewRules++
	}
	for id, r := range local {
		if _, ok := ruleName[id]; !ok {
			ruleName[id] = r.Name
		}
	}
	for _, x := range snap.Subscriptions {
		var fresh []SnapshotAccount
		for _, a := range x.Accounts {
			if owner, ok := m.ownerOfLocked(a.Site, a.UID); ok {
				plan.preview.Existing = append(plan.preview.Existing, ImportExistingAccount{Site: a.Site, UID: a.UID, Name: a.Name, ImportedSubID: x.ID, ImportedSub: x.Name, LocalSubID: owner.ID, LocalSub: owner.Name})
				if plan.owner[x.ID] == "" {
					plan.owner[x.ID] = owner.ID
				}
				continue
			}
			fresh = append(fresh, a)
		}
		plan.fresh[x.ID] = fresh
		if len(fresh) == 0 {
			plan.preview.Unchanged++
			continue
		}
		names := make([]string, 0, len(fresh))
		for _, a := range fresh {
			names = append(names, firstNonEmptyStr(a.Name, a.UID))
		}
		plan.preview.New = append(plan.preview.New, ImportSub{ID: x.ID, Name: x.Name, Accounts: names, RuleName: firstNonEmptyStr(ruleName[x.RuleID], "默认")})
	}
	return plan, nil
}

func sameRuleSettings(a, b CacheRule) bool {
	return a.ScheduledCheck == b.ScheduledCheck && a.AutoCache == b.AutoCache && a.AutoRecord == b.AutoRecord && a.FullCache == b.FullCache &&
		slices.Equal(normalizeRuleTypes(a.Types), normalizeRuleTypes(b.Types))
}

// PreviewImport reports what importing snap would do; it writes nothing.
func (m *Manager) PreviewImport(snap Snapshot) (ImportPreview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	plan, err := m.planImportLocked(snap)
	return plan.preview, err
}

// ApplyImport imports snap with the user's choices in one write. Accounts
// already subscribed keep their subscription and their check state; nothing
// is written when anything fails.
func (m *Manager) ApplyImport(snap Snapshot, choices ImportChoices) (ImportPreview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	plan, err := m.planImportLocked(snap)
	if err != nil {
		return ImportPreview{}, err
	}
	current, err := m.listSubsOnlyLocked()
	if err != nil {
		return ImportPreview{}, err
	}
	rules := slices.Clone(m.rules)
	idx := map[string]int{}
	for i, r := range rules {
		idx[r.ID] = i
	}
	uniqueRuleName := func(name, exceptID string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			name = "导入规则"
		}
		taken := func(n string) bool {
			for _, existing := range rules {
				if existing.ID != exceptID && existing.Name == n {
					return true
				}
			}
			return false
		}
		out := name
		for n := 2; taken(out); n++ {
			out = fmt.Sprintf("%s（%d）", name, n)
		}
		return out
	}
	for _, r := range snap.Rules {
		if r.ID == "" || r.ID == DefaultRuleID {
			continue
		}
		r.Types = normalizeRuleTypes(r.Types)
		if i, ok := idx[r.ID]; ok {
			if slices.Contains(choices.UseImportedRules, r.ID) {
				r.Name = uniqueRuleName(r.Name, r.ID)
				rules[i] = r
			}
			continue
		}
		r.Name = uniqueRuleName(r.Name, r.ID)
		idx[r.ID] = len(rules)
		rules = append(rules, r)
	}
	ruleByID := map[string]CacheRule{}
	for _, r := range rules {
		ruleByID[r.ID] = r
	}
	ruleOf := func(id string) CacheRule {
		if r, ok := ruleByID[id]; ok {
			return r
		}
		return defaultRule()
	}
	subIndex := map[string]int{}
	for i, s := range current {
		subIndex[s.ID] = i
	}
	// Subscriptions on a rule whose imported settings were chosen follow it.
	for i := range current {
		if slices.Contains(choices.UseImportedRules, current[i].RuleID) {
			r := ruleOf(current[i].RuleID)
			m.applyRulesLocked(&current[i], ruleToSubRulesKeeping(r, current[i].Rules))
		}
	}
	for _, x := range snap.Subscriptions {
		fresh := plan.fresh[x.ID]
		if len(fresh) == 0 {
			if owner := plan.owner[x.ID]; owner != "" && choices.MergeExisting && len(x.Links) > 0 {
				if i, ok := subIndex[owner]; ok {
					current[i].Links = MergeCustomLinks(current[i].Links, x.Links)
				}
			}
			continue
		}
		if owner := plan.owner[x.ID]; owner != "" && choices.MergeExisting {
			if i, ok := subIndex[owner]; ok {
				if len(x.Links) > 0 {
					current[i].Links = MergeCustomLinks(current[i].Links, x.Links)
				}
				var overflow []SnapshotAccount
				for _, a := range fresh {
					if len(current[i].Accounts) >= 20 {
						overflow = append(overflow, a)
						continue
					}
					current[i].Accounts = append(current[i].Accounts, AccountRef{Site: a.Site, UID: a.UID, UniqueID: a.UniqueID, WebRID: a.WebRID, Name: a.Name, Face: a.Face, AddedAt: m.nowFunc().UTC().Format(time.RFC3339)})
				}
				if len(overflow) == 0 {
					continue
				}
				// Do not silently drop accounts beyond the 20-account cap:
				// place overflow accounts into a new subscription instead.
				fresh = overflow
			}
		}
		for len(fresh) > 0 {
			chunk := fresh
			if len(chunk) > 20 {
				chunk = fresh[:20]
			}
			fresh = fresh[len(chunk):]
			y := x
			y.Accounts = chunk
			if _, taken := subIndex[y.ID]; taken || strings.TrimSpace(y.ID) == "" {
				y.ID = newImportedSubID()
			}
			sub := m.snapshotToSubscriptionLocked(y, ruleOf(y.RuleID))
			subIndex[sub.ID] = len(current)
			current = append(current, sub)
		}
	}
	if err := m.replaceAllLocked(current, rules); err != nil {
		return ImportPreview{}, err
	}
	return plan.preview, nil
}

// ruleToSubRulesKeeping applies r's settings, keeping the time automatic
// caching started when it stays on.
func ruleToSubRulesKeeping(r CacheRule, prev SubRules) SubRules {
	next := ruleToSubRules(r)
	if next.AutoCache.Enabled && prev.AutoCache.Enabled {
		next.AutoCache.Since = prev.AutoCache.Since
	}
	return next
}

func newImportedSubID() string {
	var rnd [16]byte
	_, _ = rand.Read(rnd[:])
	return hex.EncodeToString(rnd[:])
}
