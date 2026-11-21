package subscription

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func (m *Manager) evaluateAutoCacheRulesForSites(ctx context.Context, abortedSites map[string]string) (createdTasks int, totalBacklog int, result error) {
	m.mu.Lock()
	subs, err := m.listSubsOnlyLocked()
	hasEnqueuers := len(m.autoCacheEnqueuers) > 0
	dl := m.dl
	globalAutoCacheScope := m.subSettings.AutoCacheScope
	gate := m.autoCacheGate
	m.mu.Unlock()
	if err != nil {
		return 0, 0, err
	}
	if dl == nil && !hasEnqueuers {
		return 0, 0, nil
	}

	type candidate struct {
		subID    string
		site     string
		uid      string
		dynID    string
		pubTs    int64
		typ      string
		snapshot string
	}
	var allCandidates []candidate

	for _, s := range subs {
		sinceUnix := int64(0)
		typeAllowed := make(map[string]bool, len(s.Rules.AutoCache.Types))
		legacy := !s.Rules.AutoCache.Enabled
		if legacy {
			effective := s.AutoCache
			if effective == "inherit" || effective == "" {
				effective = globalAutoCacheScope
			}
			if effective == "none" || effective == "" {
				continue
			}
			if effective == "video" || effective == "both" {
				typeAllowed["video"] = true
			}
			if effective == "dynamic" || effective == "both" {
				for _, typ := range []string{"video", "draw", "word", "forward"} {
					typeAllowed[typ] = true
				}
			}
		} else {
			sinceTime, err := time.Parse(time.RFC3339, s.Rules.AutoCache.Since)
			if err != nil {
				continue
			}
			sinceUnix = sinceTime.Unix()
			for _, typ := range s.Rules.AutoCache.Types {
				typeAllowed[strings.ToLower(strings.TrimSpace(typ))] = true
			}
		}

		for _, acc := range s.Accounts {
			accSite := normalizeSchedulerSite(acc.Site)
			if abortedSites != nil && abortedSites[accSite] != "" {
				continue
			}
			m.mu.Lock()
			rows, qErr := m.updatesDB.Query(`
				SELECT dyn_id, pub_ts, type, snapshot
				FROM update_item
				WHERE site = ? AND uid = ? AND pub_ts >= ? AND auto_task_id = '' AND baseline_snapshot = 0
				ORDER BY pub_ts ASC, dyn_id ASC
			`, accSite, acc.UID, sinceUnix)
			if qErr == nil {
				for rows.Next() {
					var dynID, typ, snap string
					var pubTs int64
					if err := rows.Scan(&dynID, &pubTs, &typ, &snap); err == nil {
						if typeAllowed[typ] {
							allCandidates = append(allCandidates, candidate{
								subID:    s.SubID,
								site:     accSite,
								uid:      acc.UID,
								dynID:    dynID,
								pubTs:    pubTs,
								typ:      typ,
								snapshot: snap,
							})
						}
					}
				}
				if rErr := rows.Err(); rErr != nil {
					qErr = rErr
				}
				_ = rows.Close()
			}
			m.mu.Unlock()
			if qErr != nil {
				return createdTasks, totalBacklog, qErr
			}
		}
	}

	sort.SliceStable(allCandidates, func(i, j int) bool {
		if allCandidates[i].pubTs != allCandidates[j].pubTs {
			return allCandidates[i].pubTs < allCandidates[j].pubTs
		}
		return allCandidates[i].dynID < allCandidates[j].dynID
	})

	backlogBySub := make(map[string]int)
	haltedSites := make(map[string]string, len(abortedSites))
	for k, v := range abortedSites {
		if v != "" {
			haltedSites[k] = v
		}
	}
	gateClosed := gate != nil && gate() != nil

	handleEnqueueErr := func(c candidate, enqErr error) error {
		if enqErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kind := site.KindOf(enqErr)
		switch kind {
		case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindAuthRequired, site.ErrKindQuota, site.ErrKindBackoff:
			// INV-10: halt automatic caching for this site immediately. Only
			// circuit-breaker / auth stops persist automatic stop state (quota
			// exhaustion and existing backoff must not advance exponential backoff).
			if haltedSites[c.site] == "" {
				haltedSites[c.site] = kind
				if abortedSites != nil && abortedSites[c.site] == "" {
					abortedSites[c.site] = kind
				}
				if kind == site.ErrKindRiskControl || kind == site.ErrKindRateLimited || kind == site.ErrKindAuthRequired {
					m.RecordSiteAutomaticStop(c.site, kind)
				}
			}
			backlogBySub[c.subID]++
			totalBacklog++
		case site.ErrKindUnavailable:
			// Permanently unavailable item: mark so it is not retried forever.
			return m.setUpdateItemTaskID(c.site, c.uid, c.dynID, "unavailable")
		default:
			backlogBySub[c.subID]++
			totalBacklog++
		}
		return nil
	}

	for _, c := range allCandidates {
		if err := ctx.Err(); err != nil {
			return createdTasks, totalBacklog, err
		}
		if cachedKey := m.findCachedLibraryKey(c.site, c.dynID); cachedKey != "" {
			if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, "cached:"+cachedKey); err != nil {
				return createdTasks, totalBacklog, err
			}
			continue
		}

		// A closed gate (remote export folder nearly full), per-round 20-task
		// cap, or a halted site defers remaining items to backlog.
		if createdTasks >= 20 || gateClosed || haltedSites[c.site] != "" {
			backlogBySub[c.subID]++
			totalBacklog++
			continue
		}

		m.mu.Lock()
		enq := m.autoCacheEnqueuers[site.ID(c.site)]
		m.mu.Unlock()

		if enq != nil {
			taskID, err := enq(ctx, UpdateItem{
				Site:     c.site,
				UID:      c.uid,
				DynID:    c.dynID,
				PubTs:    c.pubTs,
				Type:     c.typ,
				Snapshot: c.snapshot,
			})
			if err != nil {
				if hErr := handleEnqueueErr(c, err); hErr != nil {
					return createdTasks, totalBacklog, hErr
				}
				continue
			}
			if taskID != "" {
				createdTasks++
				if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, taskID); err != nil {
					return createdTasks, totalBacklog, err
				}
			}
			continue
		}

		if c.site == string(site.Bilibili) && dl != nil {
			if strings.HasPrefix(strings.ToUpper(c.dynID), "BV1") {
				tasks, err := dl.EnqueueVideo(ctx, download.VideoEnqueueRequest{BVID: c.dynID, Source: "auto", ForMid: parseBiliMid(c.uid)})
				if err != nil {
					if hErr := handleEnqueueErr(c, err); hErr != nil {
						return createdTasks, totalBacklog, hErr
					}
					continue
				}
				if len(tasks) > 0 {
					createdTasks++
					if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, tasks[0].TaskID); err != nil {
						return createdTasks, totalBacklog, err
					}
				}
			} else {
				var dynItem bilibili.DynamicItem
				var itemPtr *bilibili.DynamicItem
				if c.snapshot != "" && json.Unmarshal([]byte(c.snapshot), &dynItem) == nil && dynItem.ID != "" {
					itemPtr = &dynItem
				}
				task, err := dl.EnqueueDynamic(ctx, download.DynamicEnqueueRequest{
					DynamicID:         c.dynID,
					CacheLinkedVideos: true,
					Source:            "auto",
					Item:              itemPtr,
				})
				if err != nil {
					if hErr := handleEnqueueErr(c, err); hErr != nil {
						return createdTasks, totalBacklog, hErr
					}
					continue
				}
				createdTasks++
				if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, task.TaskID); err != nil {
					return createdTasks, totalBacklog, err
				}
			}
		}
	}

	m.mu.Lock()
	current, listErr := m.listSubsOnlyLocked()
	if listErr != nil {
		m.mu.Unlock()
		return createdTasks, totalBacklog, listErr
	}
	backlogChanged := false
	for i := range current {
		want := backlogBySub[current[i].SubID]
		if current[i].AutoCacheBacklog != want {
			current[i].AutoCacheBacklog = want
			backlogChanged = true
			if err := m.upsertRowLocked(current[i]); err != nil {
				m.mu.Unlock()
				return createdTasks, totalBacklog, err
			}
		}
	}
	var writeErr error
	if backlogChanged {
		writeErr = m.writeSubscriptionsToDiskLocked()
	}
	m.mu.Unlock()

	return createdTasks, totalBacklog, writeErr
}

func (m *Manager) findCachedLibraryKey(siteName, dynID string) string {
	if m.lib == nil {
		return ""
	}
	var keys []string
	switch normalizeSchedulerSite(siteName) {
	case string(site.Douyin):
		keys = []string{"douyin:video:" + dynID, "douyin:dynamic:" + dynID}
	default:
		keys = []string{"bilibili:dynamic:" + dynID, "bilibili:video:" + dynID, dynID}
	}
	for _, k := range keys {
		if _, ok, _ := m.lib.GetItem(k); ok {
			return k
		}
	}
	return ""
}

// parseBiliMid is the subscription account a video is cached for (DEC-056).
func parseBiliMid(uid string) int64 {
	mid, err := strconv.ParseInt(strings.TrimSpace(uid), 10, 64)
	if err != nil || mid <= 0 {
		return 0
	}
	return mid
}
