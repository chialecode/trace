package library

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
	rows, err := s.db.Query(`SELECT mid, MAX(author_name), MAX(author_face), COALESCE(MAX(pub_ts), 0) FROM library_items WHERE mid > 0 GROUP BY mid ORDER BY MAX(author_name), mid`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var author ItemAuthor
		if err := rows.Scan(&author.Mid, &author.Name, &author.Face, &author.LatestPubTs); err != nil {
			return stats, err
		}
		stats.Authors = append(stats.Authors, author)
	}
	return stats, rows.Err()
}
