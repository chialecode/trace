package library

import (
	"strconv"
	"strings"
)

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
	// A collaboration or forwarded video cached under another creator's folder is
	// listed under that creator in the media library, not under the uploader (DEC-056, DEC-061).
	rows, err := s.db.Query(`
		SELECT site, owner_uid, MAX(owner_mid), MAX(owner_name), MAX(owner_face), COALESCE(MAX(pub_ts), 0), COUNT(*), COALESCE(SUM(total_bytes), 0)
		FROM (
			SELECT site, pub_ts, total_bytes,
				CASE WHEN ` + viaOwnerSQL + ` THEN ` + viaUIDSQL + ` ELSE author_uid END AS owner_uid,
				CASE WHEN ` + viaOwnerSQL + ` THEN 0 ELSE mid END AS owner_mid,
				CASE WHEN ` + viaOwnerSQL + ` THEN COALESCE(json_extract(json_data, '$.via.name'), '') ELSE author_name END AS owner_name,
				CASE WHEN ` + viaOwnerSQL + ` THEN '' ELSE author_face END AS owner_face
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
		if err := rows.Scan(&author.Site, &author.UID, &author.Mid, &author.Name, &author.Face, &author.LatestPubTs, &author.ItemCount, &author.TotalBytes); err != nil {
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
const collabSQL = "(COALESCE(json_extract(json_data, '$.via.reason'), '') = '" + ViaCollab + "' AND COALESCE(" + viaUIDSQL + ", '') != '')"

// viaForwardSQL matches library_items rows cached solely because another
// creator forwarded them (stored in the forwarder's forwards/ folder, DEC-056).
const viaForwardSQL = "(COALESCE(json_extract(json_data, '$.via.reason'), '') = '" + ViaForward + "' AND COALESCE(" + viaUIDSQL + ", '') != '')"

// viaOwnerSQL matches library_items rows stored under via.uid's folder
// (collaboration videos and forwarded content, DEC-056, DEC-061, DEC-063).
const viaOwnerSQL = "(COALESCE(json_extract(json_data, '$.via.reason'), '') IN ('" + ViaCollab + "', '" + ViaForward + "') AND COALESCE(" + viaUIDSQL + ", '') != '')"

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
			WHERE author_uid != '' AND pub_ts > 0 AND NOT ` + viaForwardSQL + `
			UNION ALL
			SELECT site, ` + viaUIDSQL + `, 0, COALESCE(json_extract(json_data, '$.via.name'), ''), '', pub_ts
			FROM library_items
			WHERE ` + viaOwnerSQL + ` AND pub_ts > 0
			UNION ALL
			SELECT site, author_uid, mid, author_name, author_face, pub_ts
			FROM feed_items
			WHERE cached = 1 AND author_uid != '' AND pub_ts > 0
			  AND NOT (kind = 'video' AND EXISTS (SELECT 1 FROM item_refs r WHERE r.to_key = feed_items.feed_key))
			  AND NOT (item_key != '' AND EXISTS (SELECT 1 FROM library_items li WHERE li.item_key = feed_items.item_key AND COALESCE(json_extract(li.json_data, '$.via.reason'), '') = '` + ViaForward + `' AND COALESCE(json_extract(li.json_data, '$.via.uid'), '') != '' AND json_extract(li.json_data, '$.via.uid') != feed_items.author_uid))
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

// DistinctAuthorRefs returns every distinct (site, uid) referenced as either
// the publisher (author) or the via target of any cached item in library_items.
func (s *Store) DistinctAuthorRefs() ([]AuthorRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT DISTINCT
			site,
			author_uid,
			mid,
			COALESCE(json_extract(json_data, '$.via.site'), ''),
			COALESCE(json_extract(json_data, '$.via.uid'), '')
		FROM library_items
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]bool)
	var out []AuthorRef
	add := func(siteID, uid string, mid int64) {
		normSite, normUID := NormalizeAuthorUID(siteID, uid, mid)
		if normSite == "" || normUID == "" {
			return
		}
		k := normSite + ":" + normUID
		if !seen[k] {
			seen[k] = true
			out = append(out, AuthorRef{Site: normSite, UID: normUID})
		}
	}
	for rows.Next() {
		var siteID, authorUID, viaSite, viaUID string
		var mid int64
		if err := rows.Scan(&siteID, &authorUID, &mid, &viaSite, &viaUID); err != nil {
			return nil, err
		}
		add(siteID, authorUID, mid)
		if strings.TrimSpace(viaUID) != "" {
			if strings.TrimSpace(viaSite) == "" {
				viaSite = siteID
			}
			add(viaSite, viaUID, 0)
		}
	}
	return out, rows.Err()
}
