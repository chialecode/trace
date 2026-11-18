package library

import "strings"

// CountCachedWorks returns, for each account, how many of its works are in the
// library (DEC-061): on Bilibili its videos, including collaboration videos
// cached for it under its folder; on Douyin its videos and notes. Keys are
// "site:uid". It reads only the local index.
func (s *Store) CountCachedWorks(refs []AuthorRef) (map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int, len(refs))
	for _, r := range refs {
		siteID := strings.ToLower(strings.TrimSpace(r.Site))
		uid := strings.TrimSpace(r.UID)
		if siteID == "" || uid == "" {
			continue
		}
		key := siteID + ":" + uid
		if _, done := out[key]; done {
			continue
		}
		var (
			n   int
			err error
		)
		switch siteID {
		case "bilibili":
			err = s.db.QueryRow(`SELECT COUNT(*) FROM library_items WHERE site = 'bilibili' AND kind = 'video'
				AND (author_uid = ? OR (`+collabSQL+` AND `+viaUIDSQL+` = ?))`, uid, uid).Scan(&n)
		case "douyin":
			err = s.db.QueryRow(`SELECT COUNT(*) FROM library_items WHERE site = 'douyin' AND kind IN ('video', 'dynamic') AND author_uid = ?`, uid).Scan(&n)
		default:
			continue
		}
		if err != nil {
			return out, err
		}
		out[key] = n
	}
	return out, nil
}
