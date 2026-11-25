package subscription

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Atomic cache capability tags (DEC-070): one tag equals one cache capability,
// mapping 1-to-1 with SubRules. New subscriptions start with empty tags ([]).
const (
	TagScheduledCheck = "scheduled_check"
	TagCacheVideo     = "cache_video"
	TagCacheDraw      = "cache_draw"
	TagCacheWord      = "cache_word"
	TagCacheForward   = "cache_forward"
	TagFullCache      = "full_cache"
	TagAutoRecord     = "auto_record"
)

// Batch tag update modes for BatchUpdateSubscriptionsTags.
const (
	TagBatchAdd     = "add"
	TagBatchRemove  = "remove"
	TagBatchReplace = "replace"
)

// CapabilityTag describes one atomic cache capability tag.
type CapabilityTag struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ErrTagNotFound reports an unknown capability tag ID.
var ErrTagNotFound = errors.New("未知的缓存标签")

// ErrInvalidBatchMode reports an unknown batch tag operation mode.
var ErrInvalidBatchMode = errors.New("无效的批量标签操作方式")

var capabilityTagCatalog = []CapabilityTag{
	{
		ID:          TagScheduledCheck,
		Name:        "定时检查",
		Description: "按设置的间隔自动检查关联账号的新内容与开播状态",
	},
	{
		ID:          TagCacheVideo,
		Name:        "自动缓存视频",
		Description: "检查更新发现新发布的视频动态、作品、视频微博或单视频帖时自动缓存（只缓存开启标签后发布的内容）",
	},
	{
		ID:          TagCacheDraw,
		Name:        "自动缓存图文",
		Description: "检查更新发现新发布的图文、多图、GIF、实况或混排动态时自动缓存（只缓存开启标签后发布的内容）",
	},
	{
		ID:          TagCacheWord,
		Name:        "自动缓存文字",
		Description: "检查更新发现新发布的纯文字或卡片动态时自动缓存（只缓存开启标签后发布的内容）",
	},
	{
		ID:          TagCacheForward,
		Name:        "自动缓存转发",
		Description: "检查更新发现新发布的转发、快转、转帖或引用帖时自动缓存（只缓存开启标签后发布的内容）",
	},
	{
		ID:          TagFullCache,
		Name:        "全部缓存",
		Description: "每 7 天检查一次每个账号的全部作品（哔哩哔哩为投稿视频，微博与 X 为原创内容），没缓存的放进缓存队列慢慢缓存",
	},
	{
		ID:          TagAutoRecord,
		Name:        "开播录制",
		Description: "发现哔哩哔哩或抖音账号正在直播时自动开始录制（微博与 X 暂不支持直播录制）",
	},
}

// AllCapabilityTags returns all supported capability tags in canonical display and storage order.
func AllCapabilityTags() []CapabilityTag {
	return slices.Clone(capabilityTagCatalog)
}

// CapabilityTagByID looks up a capability tag definition by ID.
func CapabilityTagByID(id string) (CapabilityTag, bool) {
	trimmed := strings.TrimSpace(id)
	for _, t := range capabilityTagCatalog {
		if t.ID == trimmed {
			return t, true
		}
	}
	return CapabilityTag{}, false
}

// IsValidTag reports whether id names a supported capability tag.
func IsValidTag(id string) bool {
	_, ok := CapabilityTagByID(id)
	return ok
}

// NormalizeTags deduplicates and orders tags according to AllCapabilityTags,
// dropping unknown or empty IDs and returning a non-nil empty slice ([]string{})
// when none remain so JSON marshaling always emits "[]".
func NormalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, def := range capabilityTagCatalog {
		for _, raw := range tags {
			if strings.TrimSpace(raw) == def.ID {
				out = append(out, def.ID)
				break
			}
		}
	}
	return out
}

// ValidateTags verifies every tag ID is known and returns the normalized slice.
func ValidateTags(tags []string) ([]string, error) {
	for _, raw := range tags {
		id := strings.TrimSpace(raw)
		if !IsValidTag(id) {
			return nil, fmt.Errorf("%w: %q", ErrTagNotFound, raw)
		}
	}
	return NormalizeTags(tags), nil
}

// HasTag reports whether tagID is present in tags.
func HasTag(tags []string, tagID string) bool {
	want := strings.TrimSpace(tagID)
	if want == "" {
		return false
	}
	for _, t := range tags {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

// TagsSummary returns the concise human-readable summary of a tag slice.
func TagsSummary(tags []string) string {
	norm := NormalizeTags(tags)
	if len(norm) == 0 {
		return "无缓存标签"
	}
	names := make([]string, 0, len(norm))
	for _, id := range norm {
		if def, ok := CapabilityTagByID(id); ok {
			names = append(names, def.Name)
		}
	}
	if len(names) == 0 {
		return "无缓存标签"
	}
	return strings.Join(names, " · ")
}

// DefaultSubRules returns empty automation rules (all capabilities off) for new subscriptions (DEC-070).
func DefaultSubRules() SubRules {
	return SubRules{}
}

// ruleTypeOrder is the canonical storage order of auto-cache dynamic types.
var ruleTypeOrder = []string{"video", "draw", "word", "forward"}

func normalizeRuleTypes(types []string) []string {
	var out []string
	for _, t := range ruleTypeOrder {
		for _, raw := range types {
			if strings.ToLower(strings.TrimSpace(raw)) == t {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// TagsToSubRules maps capability tags 1-to-1 onto SubRules (excluding AutoCache.Since).
func TagsToSubRules(tags []string) SubRules {
	norm := NormalizeTags(tags)
	var types []string
	var r SubRules
	for _, id := range norm {
		switch id {
		case TagScheduledCheck:
			r.ScheduledCheck = true
		case TagCacheVideo:
			types = append(types, "video")
		case TagCacheDraw:
			types = append(types, "draw")
		case TagCacheWord:
			types = append(types, "word")
		case TagCacheForward:
			types = append(types, "forward")
		case TagFullCache:
			r.FullCache = true
		case TagAutoRecord:
			r.AutoRecord = true
		}
	}
	if len(types) > 0 {
		r.AutoCache = AutoCacheRule{
			Enabled: true,
			Types:   types,
		}
	}
	return r
}

// SubRulesToTags maps SubRules 1-to-1 onto canonical capability tags.
func SubRulesToTags(r SubRules) []string {
	tags := make([]string, 0, len(capabilityTagCatalog))
	if r.ScheduledCheck {
		tags = append(tags, TagScheduledCheck)
	}
	if r.AutoCache.Enabled {
		types := normalizeRuleTypes(r.AutoCache.Types)
		if len(types) == 0 {
			types = []string{"video", "draw"}
		}
		for _, typ := range types {
			switch typ {
			case "video":
				tags = append(tags, TagCacheVideo)
			case "draw":
				tags = append(tags, TagCacheDraw)
			case "word":
				tags = append(tags, TagCacheWord)
			case "forward":
				tags = append(tags, TagCacheForward)
			}
		}
	}
	if r.FullCache {
		tags = append(tags, TagFullCache)
	}
	if r.AutoRecord {
		tags = append(tags, TagAutoRecord)
	}
	return NormalizeTags(tags)
}

// legacyCacheRule reads pre-v5 named CacheRule entries during migration or legacy snapshot import.
type legacyCacheRule struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ScheduledCheck bool     `json:"scheduledCheck"`
	AutoCache      bool     `json:"autoCache"`
	Types          []string `json:"types,omitempty"`
	AutoRecord     bool     `json:"autoRecord"`
	FullCache      bool     `json:"fullCache,omitempty"`
}

func legacyCacheRuleToTags(r legacyCacheRule) []string {
	types := normalizeRuleTypes(r.Types)
	if r.AutoCache && len(types) == 0 {
		types = []string{"video", "draw"}
	}
	return SubRulesToTags(SubRules{
		ScheduledCheck: r.ScheduledCheck,
		AutoCache: AutoCacheRule{
			Enabled: r.AutoCache,
			Types:   types,
		},
		FullCache:  r.FullCache,
		AutoRecord: r.AutoRecord,
	})
}

// applyRulesLocked sets a subscription's effective rules and synchronized tags,
// keeping AutoCache.Since while automatic caching stays enabled and setting it
// to now when newly enabled (subscriptions.md §4).
func (m *Manager) applyRulesLocked(sub *Subscription, rules SubRules) {
	wasEnabled := sub.Rules.AutoCache.Enabled
	if rules.AutoCache.Enabled {
		rules.AutoCache.Types = normalizeRuleTypes(rules.AutoCache.Types)
		if len(rules.AutoCache.Types) == 0 {
			rules.AutoCache.Types = []string{"video", "draw"}
		}
		if rules.AutoCache.Since == "" {
			if sub.Rules.AutoCache.Since != "" && wasEnabled {
				rules.AutoCache.Since = sub.Rules.AutoCache.Since
			} else {
				rules.AutoCache.Since = m.nowFunc().UTC().Format(time.RFC3339)
			}
		}
	} else {
		rules.AutoCache.Types = nil
		rules.AutoCache.Since = ""
	}
	sub.Rules = rules
	sub.Tags = SubRulesToTags(rules)
	if rules.AutoCache.Enabled {
		sub.AutoCache = "dynamic"
	} else {
		sub.AutoCache = "none"
	}
	sub.Enabled = rules.ScheduledCheck
	sub.LiveAutoRecord = rules.AutoRecord
	syncSubFields(sub, m.nowFunc())
}

// applyTagsLocked updates sub's capability tags and synchronizes SubRules + AutoCache.Since.
func (m *Manager) applyTagsLocked(sub *Subscription, tags []string) {
	norm := NormalizeTags(tags)
	nextRules := TagsToSubRules(norm)
	if nextRules.AutoCache.Enabled && sub.Rules.AutoCache.Enabled && sub.Rules.AutoCache.Since != "" {
		nextRules.AutoCache.Since = sub.Rules.AutoCache.Since
	}
	m.applyRulesLocked(sub, nextRules)
	sub.Tags = norm
}

func matchSubIndexLocked(subs []Subscription, rawID string) int {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return -1
	}
	for i, s := range subs {
		if s.ID == id || s.SubID == id || (s.Mid > 0 && strconv.FormatInt(s.Mid, 10) == strings.TrimPrefix(id, "bilibili:")) {
			return i
		}
	}
	return -1
}

// SetSubscriptionTags replaces the capability tags on a single subscription,
// preserving AutoCache.Since when automatic caching remains active and setting
// it to now when newly enabled.
func (m *Manager) SetSubscriptionTags(subID string, tags []string) (result Subscription, resultErr error) {
	normTags, err := ValidateTags(tags)
	if err != nil {
		return Subscription{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, err := m.listSubsOnlyLocked()
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
	m.applyTagsLocked(&sub, normTags)
	sub.UpdatedAt = m.nowFunc().UTC().Format(time.RFC3339)
	if err := m.upsertRowLocked(sub); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// SetTagSubscriptions assigns a single capability tag across all subscriptions
// in one atomic batch: subscriptions in subIDs gain tagID (keeping their other
// tags), while subscriptions not in subIDs lose tagID (keeping their other tags).
func (m *Manager) SetTagSubscriptions(tagID string, subIDs []string) (resultErr error) {
	tagID = strings.TrimSpace(tagID)
	if !IsValidTag(tagID) {
		return fmt.Errorf("%w: %q", ErrTagNotFound, tagID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	before, err := m.listSubsOnlyLocked()
	if err != nil {
		return err
	}
	selected := make(map[int]bool, len(subIDs))
	for _, rawID := range subIDs {
		matched := matchSubIndexLocked(before, rawID)
		if matched < 0 {
			return fmt.Errorf("subscription: %s not found", rawID)
		}
		selected[matched] = true
	}

	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	for i, sub := range before {
		had := HasTag(sub.Tags, tagID)
		want := selected[i]
		if had == want {
			continue
		}
		var nextTags []string
		if want {
			nextTags = append(slices.Clone(sub.Tags), tagID)
		} else {
			for _, existing := range sub.Tags {
				if existing != tagID {
					nextTags = append(nextTags, existing)
				}
			}
		}
		m.applyTagsLocked(&sub, nextTags)
		sub.UpdatedAt = nowStr
		if err := m.upsertRowLocked(sub); err != nil {
			return err
		}
	}
	return m.writeSubscriptionsToDiskLocked()
}

// BatchUpdateSubscriptionsTags applies a multi-tag batch operation ("add",
// "remove", or "replace") across the selected subscriptions in one atomic write.
// Unselected subscriptions remain untouched.
func (m *Manager) BatchUpdateSubscriptionsTags(subIDs []string, mode string, tags []string) (resultErr error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case TagBatchAdd, TagBatchRemove, TagBatchReplace:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidBatchMode, mode)
	}
	normTags, err := ValidateTags(tags)
	if err != nil {
		return err
	}
	if len(subIDs) == 0 {
		return errors.New("请至少选择一个订阅")
	}
	if (mode == TagBatchAdd || mode == TagBatchRemove) && len(normTags) == 0 {
		return errors.New("请至少选择一个缓存标签")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	before, err := m.listSubsOnlyLocked()
	if err != nil {
		return err
	}
	selectedOrder := make([]int, 0, len(subIDs))
	selectedSet := make(map[int]bool, len(subIDs))
	for _, rawID := range subIDs {
		matched := matchSubIndexLocked(before, rawID)
		if matched < 0 {
			return fmt.Errorf("subscription: %s not found", rawID)
		}
		if !selectedSet[matched] {
			selectedSet[matched] = true
			selectedOrder = append(selectedOrder, matched)
		}
	}

	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	removeSet := make(map[string]bool, len(normTags))
	for _, t := range normTags {
		removeSet[t] = true
	}

	for _, idx := range selectedOrder {
		sub := before[idx]
		var nextTags []string
		switch mode {
		case TagBatchAdd:
			nextTags = append(slices.Clone(sub.Tags), normTags...)
		case TagBatchRemove:
			for _, existing := range sub.Tags {
				if !removeSet[existing] {
					nextTags = append(nextTags, existing)
				}
			}
		case TagBatchReplace:
			nextTags = normTags
		}
		nextTags = NormalizeTags(nextTags)
		m.applyTagsLocked(&sub, nextTags)
		sub.UpdatedAt = nowStr
		if err := m.upsertRowLocked(sub); err != nil {
			return err
		}
	}
	return m.writeSubscriptionsToDiskLocked()
}
