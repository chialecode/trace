package subscriptions

import (
	"slices"
	"strings"
)

// Pure mobile presentation catalog mirroring desktop DEC-070 / DEC-080
// capability tags and tracking presets without importing internal/subscription
// (enforced by tools/check mobile dependency boundary rule #10).
const (
	tagScheduledCheck = "scheduled_check"
	tagCacheVideo     = "cache_video"
	tagCacheDraw      = "cache_draw"
	tagCacheWord      = "cache_word"
	tagCacheForward   = "cache_forward"
	tagFullCache      = "full_cache"
	tagAutoRecord     = "auto_record"

	presetFullArchive = "full_archive"
	presetFollowCache = "follow_cache"
	presetCheckOnly   = "check_only"
	presetManual      = "manual"
	presetCustom      = "custom"
)

type capabilityTagDef struct {
	ID          string
	Name        string
	Description string
}

type trackingPresetDef struct {
	ID          string
	Name        string
	Description string
	Tags        []string
}

var mobileCapabilityTags = []capabilityTagDef{
	{ID: tagScheduledCheck, Name: "定时检查", Description: "按设置间隔自动检查新内容与开播状态"},
	{ID: tagCacheVideo, Name: "自动缓存视频", Description: "检查更新发现新视频时自动缓存"},
	{ID: tagCacheDraw, Name: "自动缓存图文", Description: "检查更新发现新图文/实况/混排时自动缓存"},
	{ID: tagCacheWord, Name: "自动缓存文字", Description: "检查更新发现新纯文字或卡片时自动缓存"},
	{ID: tagCacheForward, Name: "自动缓存转发", Description: "检查更新发现新转发或引用时自动缓存"},
	{ID: tagFullCache, Name: "全部缓存", Description: "每 7 天检查全部作品并补齐未缓存内容"},
	{ID: tagAutoRecord, Name: "开播录制", Description: "发现 B 站或抖音账号开播时自动录制"},
}

var mobileTrackingPresets = []trackingPresetDef{
	{
		ID:          presetFullArchive,
		Name:        "完整存档",
		Description: "定时检查新内容，自动缓存全部类型，并定期补齐历史作品",
		Tags:        []string{tagScheduledCheck, tagCacheVideo, tagCacheDraw, tagCacheWord, tagCacheForward, tagFullCache},
	},
	{
		ID:          presetFollowCache,
		Name:        "追更并保存",
		Description: "定时检查新内容，自动缓存新发布的视频与图文",
		Tags:        []string{tagScheduledCheck, tagCacheVideo, tagCacheDraw},
	},
	{
		ID:          presetCheckOnly,
		Name:        "只看新动态",
		Description: "仅定时检查新动态与开播状态，不自动缓存",
		Tags:        []string{tagScheduledCheck},
	},
	{
		ID:          presetManual,
		Name:        "仅手动",
		Description: "关闭定时检查与自动缓存，需要时手动检查和缓存",
		Tags:        []string{},
	},
}

func normalizeMobileTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, def := range mobileCapabilityTags {
		for _, raw := range tags {
			if strings.TrimSpace(raw) == def.ID {
				out = append(out, def.ID)
				break
			}
		}
	}
	return out
}

func hasMobileTag(tags []string, id string) bool {
	for _, t := range tags {
		if strings.TrimSpace(t) == id {
			return true
		}
	}
	return false
}

func toggleMobileTag(tags []string, id string) []string {
	if hasMobileTag(tags, id) {
		out := make([]string, 0, len(tags))
		for _, t := range tags {
			if strings.TrimSpace(t) != id {
				out = append(out, t)
			}
		}
		return normalizeMobileTags(out)
	}
	return normalizeMobileTags(append(slices.Clone(tags), id))
}

func applyMobilePreset(presetID string, autoRecord bool) []string {
	for _, p := range mobileTrackingPresets {
		if p.ID == presetID {
			tags := slices.Clone(p.Tags)
			if autoRecord {
				tags = append(tags, tagAutoRecord)
			}
			return normalizeMobileTags(tags)
		}
	}
	return normalizeMobileTags(nil)
}

func matchMobileTrackingPreset(tags []string) (presetID string, autoRecord bool) {
	norm := normalizeMobileTags(tags)
	base := make([]string, 0, len(norm))
	for _, id := range norm {
		if id == tagAutoRecord {
			autoRecord = true
			continue
		}
		base = append(base, id)
	}
	for _, p := range mobileTrackingPresets {
		if slices.Equal(base, p.Tags) {
			return p.ID, autoRecord
		}
	}
	return presetCustom, autoRecord
}

func hasAutoCacheWithoutScheduledCheck(tags []string) bool {
	hasAuto := hasMobileTag(tags, tagCacheVideo) ||
		hasMobileTag(tags, tagCacheDraw) ||
		hasMobileTag(tags, tagCacheWord) ||
		hasMobileTag(tags, tagCacheForward)
	return hasAuto && !hasMobileTag(tags, tagScheduledCheck)
}
