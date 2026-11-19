package library

import "strconv"

type Statistics struct {
	Items   int          `json:"items"`
	Bytes   int64        `json:"bytes"`
	Authors []ItemAuthor `json:"authors"`
}

func (s *Store) Statistics() (Statistics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var stats Statistics
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(total_bytes), 0) FROM library_items`).Scan(&stats.Items, &stats.Bytes); err != nil {
		return stats, err
	}
	// A collaboration video cached for a member is listed under that member,
	// whose folder holds it, not under the uploader (DEC-061).
	rows, err := s.db.Query(`
		SELECT site, owner_uid, MAX(owner_mid), MAX(owner_name), MAX(owner_face), COALESCE(MAX(pub_ts), 0)
		FROM (
			SELECT site, pub_ts,
				CASE WHEN ` + collabSQL + ` THEN ` + viaUIDSQL + ` ELSE author_uid END AS owner_uid,
				CASE WHEN ` + collabSQL + ` THEN 0 ELSE mid END AS owner_mid,
				CASE WHEN ` + collabSQL + ` THEN COALESCE(json_extract(json_data, '$.via.name'), '') ELSE author_name END AS owner_name,
				CASE WHEN ` + collabSQL + ` THEN '' ELSE author_face END AS owner_face
			FROM library_items
		)
		WHERE owner_uid != ''
		GROUP BY site, owner_uid
		ORDER BY MAX(owner_name), site, owner_uid
	`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var author ItemAuthor
		if err := rows.Scan(&author.Site, &author.UID, &author.Mid, &author.Name, &author.Face, &author.LatestPubTs); err != nil {
			return stats, err
		}
		if author.Site == "bilibili" && author.Mid <= 0 {
			if n, convErr := strconv.ParseInt(author.UID, 10, 64); convErr == nil && n > 0 {
				author.Mid = n
			}
		}
		stats.Authors = append(stats.Authors, author)
	}
	return stats, rows.Err()
}

// collabSQL matches library_items rows that are collaboration videos cached
// for a member other than the uploader (DEC-056).
const collabSQL = "(json_extract(json_data, '$.via.reason') = '" + ViaCollab + "' AND COALESCE(" + viaUIDSQL + ", '') != '')"

// LatestPubByAuthor returns the newest cached pub_ts for each (site, author_uid)
// across library_items and cached feed_items, supporting multi-site HomeSeen baselines.
func (s *Store) LatestPubByAuthor() ([]ItemAuthor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT site, author_uid, MAX(mid), MAX(author_name), MAX(author_face), COALESCE(MAX(pub_ts), 0)
		FROM (
			SELECT site, author_uid, mid, author_name, author_face, pub_ts
			FROM library_items
			WHERE author_uid != '' AND pub_ts > 0
			UNION ALL
			SELECT site, ` + viaUIDSQL + `, 0, COALESCE(json_extract(json_data, '$.via.name'), ''), '', pub_ts
			FROM library_items
			WHERE ` + collabSQL + ` AND pub_ts > 0
			UNION ALL
			SELECT site, author_uid, mid, author_name, author_face, pub_ts
			FROM feed_items
			WHERE cached = 1 AND author_uid != '' AND pub_ts > 0
		)
		GROUP BY site, author_uid
		ORDER BY 6 DESC, site, author_uid
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemAuthor
	for rows.Next() {
		var au ItemAuthor
		if err := rows.Scan(&au.Site, &au.UID, &au.Mid, &au.Name, &au.Face, &au.LatestPubTs); err != nil {
			return nil, err
		}
		if au.Site == "bilibili" && au.Mid <= 0 {
			if n, convErr := strconv.ParseInt(au.UID, 10, 64); convErr == nil && n > 0 {
				au.Mid = n
			}
		}
		out = append(out, au)
	}
	return out, rows.Err()
}
