package subscription

import (
	"strings"
	"time"
)

// FullCacheInterval is how often a full-cache rule sweeps an account's works
// (DEC-061).
const FullCacheInterval = 7 * 24 * time.Hour

// AccountWorks is what TRACE last learned about an account's works
// (updates.db account_works, DEC-061). Total is -1 until a parse of the
// account reported it.
type AccountWorks struct {
	Site        string `json:"site"`
	UID         string `json:"uid"`
	Total       int    `json:"total"`
	TotalAt     string `json:"totalAt,omitempty"`
	FullCacheAt string `json:"fullCacheAt,omitempty"`
}

// RecordAccountWorks stores an account's works total as a parse of the
// account reported it (its page, its upload list, or a full-cache sweep).
// Totals below zero are ignored.
func (m *Manager) RecordAccountWorks(siteID, uid string, total int) error {
	siteID, uid = strings.ToLower(strings.TrimSpace(siteID)), strings.TrimSpace(uid)
	if m == nil || m.updatesDB == nil || siteID == "" || uid == "" || total < 0 {
		return nil
	}
	now := m.nowFunc().UTC().Format(time.RFC3339)
	_, err := m.updatesDB.Exec(`
		INSERT INTO account_works (site, uid, works_total, works_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(site, uid) DO UPDATE SET works_total = excluded.works_total, works_at = excluded.works_at
	`, siteID, uid, total, now)
	return err
}

// MarkFullCacheSweep records when a full-cache sweep of the account started.
func (m *Manager) MarkFullCacheSweep(siteID, uid string, at time.Time) error {
	siteID, uid = strings.ToLower(strings.TrimSpace(siteID)), strings.TrimSpace(uid)
	if m == nil || m.updatesDB == nil || siteID == "" || uid == "" {
		return nil
	}
	_, err := m.updatesDB.Exec(`
		INSERT INTO account_works (site, uid, full_cache_at) VALUES (?, ?, ?)
		ON CONFLICT(site, uid) DO UPDATE SET full_cache_at = excluded.full_cache_at
	`, siteID, uid, at.UTC().Format(time.RFC3339))
	return err
}

// ListAccountWorks returns every stored row, keyed "site:uid".
func (m *Manager) ListAccountWorks() (map[string]AccountWorks, error) {
	out := map[string]AccountWorks{}
	if m == nil || m.updatesDB == nil {
		return out, nil
	}
	rows, err := m.updatesDB.Query(`SELECT site, uid, works_total, works_at, full_cache_at FROM account_works`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var w AccountWorks
		if err := rows.Scan(&w.Site, &w.UID, &w.Total, &w.TotalAt, &w.FullCacheAt); err != nil {
			return out, err
		}
		out[w.Site+":"+w.UID] = w
	}
	return out, rows.Err()
}
