package library

import "strings"

// CountCachedWorks returns, for each account, how many of its works are in the
// library (DEC-061): on Bilibili its videos, including collaboration videos
// cached for it under its folder; on Douyin its videos and notes; on Weibo its
// posts and standalone videos (including items cached via this creator); on X
// its posts (including forwarded/quoted posts cached via this creator). Keys
// are "site:uid". It reads only the local index in a single grouped query.
func (s *Store) CountCachedWorks(refs []AuthorRef) (map[string]int, error) {
	out := make(map[string]int, len(refs))
	wanted := make(map[string]bool, len(refs))
	for _, r := range refs {
		siteID := strings.ToLower(strings.TrimSpace(r.Site))
		uid := strings.TrimSpace(r.UID)
		if siteID == "" || uid == "" {
			continue
		}
		switch siteID {
		case "bilibili", "douyin", "weibo", "x":
		default:
			continue
		}
		key := siteID + ":" + uid
		if !wanted[key] {
			wanted[key] = true
			out[key] = 0
		}
	}
	if len(wanted) == 0 {
		return out, nil
	}

	const qualifyingSiteKindSQL = `(
		(site = 'bilibili' AND kind = 'video')
		OR (site = 'douyin' AND kind IN ('video', 'dynamic'))
		OR (site = 'weibo' AND kind IN ('video', 'dynamic'))
		OR (site = 'x' AND kind = 'dynamic')
	)`

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT site, owner_uid, COUNT(*)
		FROM (
			SELECT site, author_uid AS owner_uid
			FROM library_items
			WHERE ` + qualifyingSiteKindSQL + `
				AND ` + notArchivedSnapshotSQL + `
				AND author_uid != ''
				AND (site = 'douyin' OR NOT ` + viaForwardSQL + `)
			UNION ALL
			SELECT site, ` + viaUIDSQL + ` AS owner_uid
			FROM library_items
			WHERE ` + qualifyingSiteKindSQL + `
				AND ` + notArchivedSnapshotSQL + `
				AND (
					(site IN ('bilibili', 'douyin') AND ` + collabSQL + ` AND ` + viaUIDSQL + ` != author_uid)
					OR (site IN ('weibo', 'x') AND (` + viaForwardSQL + ` OR (` + collabSQL + ` AND ` + viaUIDSQL + ` != author_uid)))
				)
		)
		GROUP BY site, owner_uid
	`)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var siteID, ownerUID string
		var n int
		if err := rows.Scan(&siteID, &ownerUID, &n); err != nil {
			return out, err
		}
		key := siteID + ":" + ownerUID
		if wanted[key] {
			out[key] = n
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}
