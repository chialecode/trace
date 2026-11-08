package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultRuleID is the built-in rule every subscription falls back to; it
// can be edited but not deleted (DEC-040).
const DefaultRuleID = "default"

// CacheRule is a named automation rule shared by subscriptions (DEC-040): a
// subscription only chooses one. The "since" time of automatic caching stays
// per subscription (subscriptions.md §4).
type CacheRule struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ScheduledCheck bool     `json:"scheduledCheck"`
	AutoCache      bool     `json:"autoCache"`
	Types          []string `json:"types,omitempty"`
	AutoRecord     bool     `json:"autoRecord"`
}

// ErrRuleNotFound reports an unknown rule ID.
var ErrRuleNotFound = errors.New("subscription: cache rule not found")

func defaultRule() CacheRule {
	return CacheRule{ID: DefaultRuleID, Name: "默认", ScheduledCheck: true, Types: []string{"video", "draw"}}
}

// ruleTypeOrder is the display and storage order of dynamic types.
var ruleTypeOrder = []string{"video", "draw", "word", "forward"}

func normalizeRuleTypes(types []string) []string {
	var out []string
	for _, t := range ruleTypeOrder {
		if slices.Contains(types, t) {
			out = append(out, t)
		}
	}
	return out
}

// ruleMatches reports whether a subscription's effective rules equal r.
func ruleMatches(r CacheRule, s SubRules) bool {
	if r.ScheduledCheck != s.ScheduledCheck || r.AutoRecord != s.AutoRecord || r.AutoCache != s.AutoCache.Enabled {
		return false
	}
	return !r.AutoCache || slices.Equal(normalizeRuleTypes(r.Types), normalizeRuleTypes(s.AutoCache.Types))
}

var ruleTypeNames = map[string]string{"video": "视频", "draw": "图文", "word": "文字", "forward": "转发"}

// RuleSummary is the one-line description of what a rule does.
func RuleSummary(r CacheRule) string {
	var parts []string
	if r.ScheduledCheck {
		parts = append(parts, "定时检查")
	} else {
		parts = append(parts, "只手动检查")
	}
	if r.AutoCache {
		var names []string
		for _, t := range normalizeRuleTypes(r.Types) {
			names = append(names, ruleTypeNames[t])
		}
		if len(names) == 0 {
			names = append(names, "无类型")
		}
		parts = append(parts, "自动缓存"+strings.Join(names, "、"))
	}
	if r.AutoRecord {
		parts = append(parts, "开播录制")
	}
	return strings.Join(parts, " · ")
}

func newRuleID() string {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	return "rule-" + hex.EncodeToString(rnd[:])
}

// ensureDefaultRuleLocked keeps the built-in rule first in the list.
func (m *Manager) ensureDefaultRuleLocked() {
	for i, r := range m.rules {
		if r.ID == DefaultRuleID {
			if i != 0 {
				m.rules = append([]CacheRule{r}, slices.Delete(slices.Clone(m.rules), i, i+1)...)
			}
			return
		}
	}
	m.rules = append([]CacheRule{defaultRule()}, m.rules...)
}

func (m *Manager) ruleLocked(id string) (CacheRule, bool) {
	for _, r := range m.rules {
		if r.ID == id {
			return r, true
		}
	}
	return CacheRule{}, false
}

// bindRuleLocked gives sub a valid rule without changing what it does: keep
// its rule when that still describes its settings, otherwise use (or create)
// a rule with exactly its settings. Existing subscriptions are migrated this
// way, so no subscription changes behaviour when rules become shared.
func (m *Manager) bindRuleLocked(sub *Subscription) bool {
	m.ensureDefaultRuleLocked()
	if r, ok := m.ruleLocked(sub.RuleID); ok && ruleMatches(r, sub.Rules) {
		return false
	}
	for _, r := range m.rules {
		if ruleMatches(r, sub.Rules) {
			sub.RuleID = r.ID
			return true
		}
	}
	r := CacheRule{
		ID:             newRuleID(),
		ScheduledCheck: sub.Rules.ScheduledCheck,
		AutoCache:      sub.Rules.AutoCache.Enabled,
		AutoRecord:     sub.Rules.AutoRecord,
	}
	if r.AutoCache {
		r.Types = normalizeRuleTypes(sub.Rules.AutoCache.Types)
	}
	r.Name = m.uniqueRuleNameLocked(RuleSummary(r), "")
	m.rules = append(m.rules, r)
	sub.RuleID = r.ID
	return true
}

func (m *Manager) uniqueRuleNameLocked(name, exceptID string) string {
	taken := func(n string) bool {
		for _, r := range m.rules {
			if r.ID != exceptID && r.Name == n {
				return true
			}
		}
		return false
	}
	out := name
	for i := 2; taken(out); i++ {
		out = fmt.Sprintf("%s（%d）", name, i)
	}
	return out
}

// applyRulesLocked sets a subscription's effective rules, keeping the time
// automatic caching started while it stays on (subscriptions.md §4).
func (m *Manager) applyRulesLocked(sub *Subscription, rules SubRules) {
	wasEnabled := sub.Rules.AutoCache.Enabled
	if rules.AutoCache.Enabled {
		if rules.AutoCache.Since == "" {
			if sub.Rules.AutoCache.Since != "" && wasEnabled {
				rules.AutoCache.Since = sub.Rules.AutoCache.Since
			} else {
				rules.AutoCache.Since = m.nowFunc().UTC().Format(time.RFC3339)
			}
		}
	} else {
		rules.AutoCache.Since = ""
	}
	sub.Rules = rules
	if !rules.AutoCache.Enabled {
		sub.AutoCache = "none"
	}
	sub.Enabled = rules.ScheduledCheck
	sub.LiveAutoRecord = rules.AutoRecord
	syncSubFields(sub, m.nowFunc())
}

func ruleToSubRules(r CacheRule) SubRules {
	types := normalizeRuleTypes(r.Types)
	if len(types) == 0 {
		types = []string{"video", "draw"}
	}
	return SubRules{ScheduledCheck: r.ScheduledCheck, AutoRecord: r.AutoRecord, AutoCache: AutoCacheRule{Enabled: r.AutoCache, Types: types}}
}

// ListRules returns the shared rules, the built-in one first.
func (m *Manager) ListRules() []CacheRule {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	return slices.Clone(m.rules)
}

// SaveRule creates (empty ID) or updates a shared rule and applies it to
// every subscription that uses it.
func (m *Manager) SaveRule(in CacheRule) (result CacheRule, resultErr error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return CacheRule{}, errors.New("请填写规则名称")
	}
	if utf8.RuneCountInString(in.Name) > 32 {
		return CacheRule{}, errors.New("规则名称最多 32 个字")
	}
	in.Types = normalizeRuleTypes(in.Types)
	if in.AutoCache && len(in.Types) == 0 {
		return CacheRule{}, errors.New("自动缓存至少选择一种动态类型")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	beforeRules := slices.Clone(m.rules)
	before, err := m.listLocked()
	if err != nil {
		return CacheRule{}, err
	}
	defer func() {
		if resultErr != nil {
			m.rules = beforeRules
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()
	for _, r := range m.rules {
		if r.Name == in.Name && r.ID != in.ID {
			return CacheRule{}, fmt.Errorf("已有名为「%s」的规则", in.Name)
		}
	}
	if in.ID == "" {
		in.ID = newRuleID()
		m.rules = append(m.rules, in)
	} else {
		i := slices.IndexFunc(m.rules, func(r CacheRule) bool { return r.ID == in.ID })
		if i < 0 {
			return CacheRule{}, ErrRuleNotFound
		}
		m.rules[i] = in
	}
	for _, sub := range before {
		if sub.RuleID != in.ID {
			continue
		}
		m.applyRulesLocked(&sub, ruleToSubRules(in))
		if err := m.upsertRowLocked(sub); err != nil {
			return CacheRule{}, err
		}
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return CacheRule{}, err
	}
	return in, nil
}

// DeleteRule removes a rule; its subscriptions switch to the built-in one.
func (m *Manager) DeleteRule(id string) (resultErr error) {
	if id == DefaultRuleID {
		return errors.New("默认规则不能删除")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	i := slices.IndexFunc(m.rules, func(r CacheRule) bool { return r.ID == id })
	if i < 0 {
		return ErrRuleNotFound
	}
	beforeRules := slices.Clone(m.rules)
	before, err := m.listLocked()
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			m.rules = beforeRules
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()
	m.rules = slices.Delete(m.rules, i, i+1)
	fallback, _ := m.ruleLocked(DefaultRuleID)
	for _, sub := range before {
		if sub.RuleID != id {
			continue
		}
		sub.RuleID = DefaultRuleID
		m.applyRulesLocked(&sub, ruleToSubRules(fallback))
		if err := m.upsertRowLocked(sub); err != nil {
			return err
		}
	}
	return m.writeSubscriptionsToDiskLocked()
}

// SetSubscriptionRule makes a subscription use a shared rule.
func (m *Manager) SetSubscriptionRule(subID, ruleID string) (result Subscription, resultErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefaultRuleLocked()
	rule, ok := m.ruleLocked(ruleID)
	if !ok {
		return Subscription{}, ErrRuleNotFound
	}
	before, err := m.listLocked()
	if err != nil {
		return Subscription{}, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()
	sub, ok, err := m.findByIDOrSubIDLocked(subID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: %s not found", subID)
	}
	sub.RuleID = rule.ID
	m.applyRulesLocked(&sub, ruleToSubRules(rule))
	if err := m.upsertRowLocked(sub); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}
