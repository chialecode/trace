package library

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site/bilibili"
)

// UpgradeCandidates returns up to 50 cached Bilibili video items ordered by
// oldest UpgradeCheckedAt first (FR-CACHE-006), querying all indexed videos
// directly without scanning disk for non-returned items.
func (s *Store) UpgradeCandidates(limit int) ([]ItemMetadata, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	s.mu.RLock()
	allowMoved := s.allowMovedFiles
	rows, err := s.db.Query(`
		SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
		FROM library_items
		WHERE site = 'bilibili' AND kind = 'video'
			AND ` + notArchivedSnapshotSQL + `
	`)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	var items []ItemMetadata
	for rows.Next() {
		var jsonStr, itemDir, status, upHint string
		var totalBytes int64
		var upAvail int
		if err := rows.Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint); err != nil {
			_ = rows.Close()
			s.mu.RUnlock()
			return nil, err
		}
		meta, err := ParseItemMetadata([]byte(jsonStr))
		if err != nil {
			continue
		}
		meta.ItemDir = itemDir
		meta.Status = status
		meta.TotalBytes = totalBytes
		meta.UpgradeAvailable = upAvail == 1
		meta.UpgradeHint = upHint
		items = append(items, meta)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	s.mu.RUnlock()
	if rowErr != nil {
		return nil, rowErr
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, items[i].UpgradeCheckedAt)
		tj, _ := time.Parse(time.RFC3339, items[j].UpgradeCheckedAt)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return items[i].PubTs < items[j].PubTs
	})
	if len(items) > limit {
		items = items[:limit]
	}
	if !allowMoved {
		for i := range items {
			refreshItemFileState(&items[i])
		}
	}
	return items, nil
}

// CheckUpgradeKeys probes the given video keys (or all candidates if keys is empty) for quality upgrades.
// When force is false, items checked within the last 24 hours are skipped without network requests (FR-CACHE-006).
func (s *Store) CheckUpgradeKeys(ctx context.Context, client *bilibili.Client, keys []string, force bool, opts bilibili.PlaySelectionOptions) ([]ItemMetadata, error) {
	if client == nil {
		return nil, fmt.Errorf("library: bilibili client is nil")
	}

	var candidates []ItemMetadata
	if len(keys) == 0 {
		var err error
		candidates, err = s.UpgradeCandidates(50)
		if err != nil {
			return nil, err
		}
	} else {
		for _, k := range keys {
			m, ok, err := s.GetItem(k)
			if err != nil {
				return nil, &bilibili.SiteError{Kind: "storage", Message: "读取缓存记录失败", Err: err}
			}
			if ok && m.Kind == "video" && !m.IsArchivedSnapshot() {
				candidates = append(candidates, m)
			}
		}
	}

	var upgradable []ItemMetadata
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	for _, meta := range candidates {
		if err := ctx.Err(); err != nil {
			return upgradable, err
		}
		if len(meta.Parts) == 0 {
			continue
		}
		if !force && meta.UpgradeCheckedAt != "" {
			if lastT, err := time.Parse(time.RFC3339, meta.UpgradeCheckedAt); err == nil && now.Sub(lastT) < 24*time.Hour {
				if meta.UpgradeAvailable {
					upgradable = append(upgradable, meta)
				}
				continue
			}
		}

		// Probe P1 only (data-and-cache.md §2.7).
		part := &meta.Parts[0]
		if part.CID <= 0 {
			continue
		}
		plan, err := client.PlayURL(ctx, meta.RawID, part.CID, opts)
		if err != nil {
			return upgradable, err
		}

		part.Available = PartAvailableInfo{
			AcceptQuality: plan.Available.AcceptQuality,
			Formats:       plan.Available.Formats,
			Returned:      plan.Available.Returned,
			BestVideo:     plan.Available.BestVideo,
			BestAudio:     plan.Available.BestAudio,
			HasDolby:      plan.Available.HasDolby,
			HasFlac:       plan.Available.HasFlac,
			ProbedAt:      nowStr,
			Downgraded:    plan.Available.Downgraded,
			DowngradeHint: plan.Available.DowngradeHint,
		}

		var hints []string
		if bilibili.QualityRank(plan.Video.Quality) > bilibili.QualityRank(part.Video.Quality) {
			hints = append(hints, fmt.Sprintf("P%02d 视频可由 %s (%d) 升级至 %s (%d)",
				part.Page,
				bilibili.QualityName(part.Video.Quality), part.Video.Quality,
				plan.Video.QualityName, plan.Video.Quality,
			))
		}
		if plan.Audio != nil && audioPriority(plan.Audio.Quality) > audioPriority(part.Audio.Quality) {
			hints = append(hints, fmt.Sprintf("P%02d 音频可由 %s 升级至 %s",
				part.Page,
				part.Audio.Name,
				plan.Audio.Name,
			))
		}

		meta.UpgradeCheckedAt = nowStr
		meta.UpgradeAvailable = len(hints) > 0
		meta.UpgradeHint = strings.Join(hints, "；")
		updated, err := s.SaveItem(meta.ItemDir, meta)
		if err != nil {
			return upgradable, &bilibili.SiteError{Kind: "storage", Message: "画质探测结果保存失败", Err: err}
		}
		if updated.UpgradeAvailable {
			upgradable = append(upgradable, updated)
		}
	}

	sort.SliceStable(upgradable, func(i, j int) bool {
		return upgradable[i].PubTs > upgradable[j].PubTs
	})
	return upgradable, nil
}

// CheckQualityUpgrade probes cached video items against the Bilibili PlayURL API (with force=true for direct calls).
func (s *Store) CheckQualityUpgrade(ctx context.Context, client *bilibili.Client, opts bilibili.PlaySelectionOptions) ([]ItemMetadata, error) {
	return s.CheckUpgradeKeys(ctx, client, nil, true, opts)
}

func audioPriority(id int) int {
	switch id {
	case 30251:
		return 50
	case 30250:
		return 40
	case 30280:
		return 30
	case 30232:
		return 20
	case 30216:
		return 10
	default:
		return 0
	}
}
