package library

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// forwardContentSQL matches library_items rows that are forwarded posts or
// content cached because of one (media library "转发", DEC-056).
// COALESCE ensures the expression always yields a boolean (never NULL under NOT).
const forwardContentSQL = "(COALESCE(json_extract(json_data, '$.via.reason'), '') = 'forward' OR (kind = 'dynamic' AND COALESCE(json_extract(json_data, '$.dynamicType'), '') IN ('DYNAMIC_TYPE_FORWARD', 'forward')))"

// viaUIDSQL is the indexed expression for the creator an item was cached for
// (idx_library_items_via).
const viaUIDSQL = "json_extract(json_data, '$.via.uid')"

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLikePattern(kw string) string {
	return "%" + likeEscaper.Replace(kw) + "%"
}

// appendLibrarySiteAndAuthorClauses is appendSiteAndAuthorClauses for
// library_items: an author filter also matches items cached for that creator
// under their folder (collaboration videos and forwarded content, DEC-056).
func appendLibrarySiteAndAuthorClauses(clauses []string, args []any, siteFilter string, mid int64, authorUID string, mids []int64, refs []AuthorRef) ([]string, []any) {
	return appendSiteAndAuthorClausesVia(clauses, args, "", siteFilter, mid, authorUID, mids, refs, true)
}

// appendSiteAndAuthorClauses is the author filter for feed_items (colPrefix
// "f."): an author's feed includes collaboration works and forwarded posts
// cached for them under their folder (DEC-056, DEC-061, DEC-063), and excludes
// items cached solely because another creator forwarded them (e.g. Weibo
// fast-retweets whose author_uid is the original author while via.uid is the
// forwarder).
func appendSiteAndAuthorClauses(clauses []string, args []any, colPrefix, siteFilter string, mid int64, authorUID string, mids []int64, refs []AuthorRef) ([]string, []any) {
	return appendSiteAndAuthorClausesVia(clauses, args, colPrefix, siteFilter, mid, authorUID, mids, refs, false)
}

// viaOwnerItemKeysSQL selects the item keys of collaboration works and
// forwarded posts cached for one of a site's creators; the site and creator
// placeholders follow.
func viaOwnerItemKeysSQL(n int) string {
	ph := strings.TrimSuffix(strings.Repeat("?,", n), ",")
	return "(SELECT item_key FROM library_items WHERE site = ? AND " + viaUIDSQL + " IN (" + ph + ") AND json_extract(json_data, '$.via.reason') IN ('" + ViaCollab + "', '" + ViaForward + "'))"
}

// feedExcludedForwardSQL excludes feed_items rows whose library item was cached
// under another creator's forwards/ folder (via.reason = 'forward' and
// via.uid != f.author_uid), so fast-retweets never leak into the original
// author's feed.
func feedExcludedForwardSQL(colPrefix string) string {
	return " AND NOT (" + colPrefix + "item_key != '' AND EXISTS (SELECT 1 FROM library_items li WHERE li.item_key = " + colPrefix + "item_key AND COALESCE(json_extract(li.json_data, '$.via.reason'), '') = '" + ViaForward + "' AND COALESCE(json_extract(li.json_data, '$.via.uid'), '') != '' AND json_extract(li.json_data, '$.via.uid') != " + colPrefix + "author_uid))"
}

func appendSiteAndAuthorClausesVia(clauses []string, args []any, colPrefix, siteFilter string, mid int64, authorUID string, mids []int64, refs []AuthorRef, withVia bool) ([]string, []any) {
	normSiteFilter := strings.ToLower(strings.TrimSpace(siteFilter))
	if normSiteFilter != "" && normSiteFilter != "all" {
		clauses = append(clauses, colPrefix+"site = ?")
		args = append(args, normSiteFilter)
	}

	var collected []AuthorRef
	seen := make(map[string]bool)
	addRef := func(site, uid string) {
		uid = strings.TrimSpace(uid)
		if uid == "" {
			return
		}
		site = strings.ToLower(strings.TrimSpace(site))
		if site == "" {
			if normSiteFilter != "" && normSiteFilter != "all" {
				site = normSiteFilter
			} else {
				site = "bilibili"
			}
		}
		k := site + "\x00" + uid
		if !seen[k] {
			seen[k] = true
			collected = append(collected, AuthorRef{Site: site, UID: uid})
		}
	}
	if mid > 0 {
		addRef("bilibili", strconv.FormatInt(mid, 10))
	}
	if strings.TrimSpace(authorUID) != "" {
		addRef("", authorUID)
	}
	for _, m := range mids {
		if m > 0 {
			addRef("bilibili", strconv.FormatInt(m, 10))
		}
	}
	for _, r := range refs {
		addRef(r.Site, r.UID)
	}
	if len(collected) == 0 {
		return clauses, args
	}

	bySite := make(map[string][]string)
	var siteOrder []string
	for _, r := range collected {
		if _, ok := bySite[r.Site]; !ok {
			siteOrder = append(siteOrder, r.Site)
		}
		bySite[r.Site] = append(bySite[r.Site], r.UID)
	}
	var orParts []string
	for _, st := range siteOrder {
		uids := bySite[st]
		type colClause struct {
			expr  string
			extra string
		}
		cols := []colClause{{expr: colPrefix + "author_uid"}}
		if withVia {
			cols[0].extra = " AND NOT " + viaForwardSQL
			cols = append(cols, colClause{expr: viaUIDSQL})
		} else if colPrefix != "" {
			cols[0].extra = feedExcludedForwardSQL(colPrefix)
		}
		for _, col := range cols {
			if len(uids) == 1 {
				orParts = append(orParts, "("+colPrefix+"site = ? AND "+col.expr+" = ?"+col.extra+")")
				args = append(args, st, uids[0])
			} else {
				ph := make([]string, len(uids))
				args = append(args, st)
				for i, u := range uids {
					ph[i] = "?"
					args = append(args, u)
				}
				orParts = append(orParts, "("+colPrefix+"site = ? AND "+col.expr+" IN ("+strings.Join(ph, ",")+")"+col.extra+")")
			}
		}
		if !withVia && colPrefix != "" {
			orParts = append(orParts, "("+colPrefix+"item_key IN "+viaOwnerItemKeysSQL(len(uids))+")")
			args = append(args, st)
			for _, u := range uids {
				args = append(args, u)
			}
		}
	}
	if len(orParts) == 1 {
		clauses = append(clauses, orParts[0])
	} else {
		clauses = append(clauses, "("+strings.Join(orParts, " OR ")+")")
	}
	return clauses, args
}

// ListItems returns cached library items matching filter.
func (s *Store) ListItems(filter FeedFilter) ([]ItemMetadata, error) {
	var clauses []string
	var args []any
	switch filter.Kind {
	case "image":
		clauses = append(clauses, "(EXISTS (SELECT 1 FROM json_each(json_data, '$.media') WHERE json_extract(value, '$.role') = 'image' OR COALESCE(json_extract(value, '$.gif'), 0) = 1) OR EXISTS (SELECT 1 FROM json_each(json_data, '$.notePages') WHERE json_extract(value, '$.kind') IN ('image', 'motion') OR COALESCE(json_extract(value, '$.gif'), 0) = 1))")
		if filter.ExcludeForward {
			clauses = append(clauses, "NOT "+forwardContentSQL)
		}
	case "video":
		clauses = append(clauses, "(kind = 'video' OR EXISTS (SELECT 1 FROM json_each(json_data, '$.media') WHERE json_extract(value, '$.role') = 'video' AND COALESCE(json_extract(value, '$.gif'), 0) = 0) OR EXISTS (SELECT 1 FROM json_each(json_data, '$.notePages') WHERE json_extract(value, '$.kind') = 'video' AND COALESCE(json_extract(value, '$.gif'), 0) = 0))")
		if filter.ExcludeForward {
			clauses = append(clauses, "NOT "+forwardContentSQL)
		}
	case "forward":
		clauses = append(clauses, forwardContentSQL)
	case "", "all":
	default:
		clauses = append(clauses, "kind = ?")
		args = append(args, filter.Kind)
		if filter.ExcludeForward {
			clauses = append(clauses, "NOT "+forwardContentSQL)
		}
	}
	clauses, args = appendLibrarySiteAndAuthorClauses(clauses, args, filter.Site, filter.Mid, filter.AuthorUID, filter.Authors, filter.AuthorRefs)
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		clauses = append(clauses, `(title LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\' OR author_name LIKE ? ESCAPE '\' OR raw_id LIKE ? ESCAPE '\')`)
		pat := escapeLikePattern(kw)
		args = append(args, pat, pat, pat, pat)
	}

	query := `SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint FROM library_items`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	if strings.EqualFold(filter.Sort, "cached_at") {
		query += " ORDER BY cached_at DESC, item_key DESC"
	} else if strings.EqualFold(filter.Sort, "oldest") {
		query += " ORDER BY pub_ts ASC, item_key ASC"
	} else {
		query += " ORDER BY pub_ts DESC, item_key DESC"
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(0, filter.Offset))

	s.mu.RLock()
	allowMoved := s.allowMovedFiles
	rows, err := s.db.Query(query, args...)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	var out []ItemMetadata
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
		out = append(out, meta)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	s.mu.RUnlock()
	if rowErr != nil {
		return nil, rowErr
	}
	if !allowMoved {
		for i := range out {
			refreshItemFileState(&out[i])
		}
	}
	return out, nil
}

// ListItemsMetaOnlyBySite returns all cached items for a single site directly
// from SQLite with zero disk syscalls and no row cap (used for offline backfill
// statistics and historical cache standard scans, DEC-079).
func (s *Store) ListItemsMetaOnlyBySite(site string) ([]ItemMetadata, error) {
	normSite := strings.ToLower(strings.TrimSpace(site))
	if normSite == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(
		`SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
		 FROM library_items
		 WHERE site = ?
		 ORDER BY pub_ts DESC, item_key DESC`,
		normSite,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ItemMetadata
	for rows.Next() {
		var jsonStr, itemDir, status, upHint string
		var totalBytes int64
		var upAvail int
		if err := rows.Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint); err != nil {
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
		out = append(out, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FeedPage implements bidirectional keyset pagination over (pub_ts, feed_key) for the home feed (FR-FEED-001).
// It excludes videos that are referenced by a cached dynamic item so videos referenced by dynamics do not appear twice.
func (s *Store) FeedPage(req FeedPageRequest) (FeedPageResult, error) {
	if req.Direction == "around" && req.Cursor != nil {
		pivot, err := s.feedAnchorCursor(req)
		if err != nil || pivot == nil {
			return FeedPageResult{}, err
		}
		halfLimit := req.Limit
		if halfLimit <= 0 {
			halfLimit = 30
		}
		beforeReq := req
		beforeReq.Cursor = pivot
		beforeReq.Order = "newest"
		beforeReq.Direction = "newer"
		beforeReq.Limit = halfLimit
		before, err := s.FeedPage(beforeReq)
		if err != nil {
			return FeedPageResult{}, err
		}
		afterReq := beforeReq
		afterReq.Direction = "older"
		cursor := *pivot
		cursor.Key += "\x00"
		afterReq.Cursor = &cursor
		after, err := s.FeedPage(afterReq)
		if err != nil {
			return FeedPageResult{}, err
		}
		result := after
		result.Items = append(before.Items, after.Items...)
		if req.Order == "oldest" {
			for i, j := 0, len(result.Items)-1; i < j; i, j = i+1, j-1 {
				result.Items[i], result.Items[j] = result.Items[j], result.Items[i]
			}
			result.HasMore = before.HasMore
		}
		if len(result.Items) > 0 {
			first, last := result.Items[0], result.Items[len(result.Items)-1]
			result.PrevCursor = &FeedCursor{PubTs: first.PubTs, Key: first.FeedKey}
			result.NextCursor = &FeedCursor{PubTs: last.PubTs, Key: last.FeedKey}
		}
		return result, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	order := strings.ToLower(strings.TrimSpace(req.Order))
	if order != "oldest" {
		order = "newest"
	}
	dir := strings.ToLower(strings.TrimSpace(req.Direction))
	if dir != "newer" {
		dir = "older"
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 30
	}

	clauses, args := feedPageClauses(req)

	// Determine SQL comparison and sort order for keyset cursor (pub_ts, feed_key).
	wantDesc := (order == "newest" && dir == "older") || (order == "oldest" && dir == "newer")
	reverseAfter := dir == "newer"

	if req.Cursor != nil && (req.Cursor.Key != "" || req.Cursor.PubTs > 0) {
		if wantDesc {
			clauses = append(clauses, "(f.pub_ts < ? OR (f.pub_ts = ? AND f.feed_key < ?))")
		} else {
			clauses = append(clauses, "(f.pub_ts > ? OR (f.pub_ts = ? AND f.feed_key > ?))")
		}
		args = append(args, req.Cursor.PubTs, req.Cursor.PubTs, req.Cursor.Key)
	}

	query := `
		SELECT
			f.feed_key, f.site, f.kind, f.raw_id, f.mid, f.author_uid, f.author_name, f.author_face,
			f.title, f.summary, f.cover_url, f.canonical_url, f.pub_ts, f.discovered_at,
			f.cached, f.cache_status, f.item_key,
			COALESCE(l.item_dir, ''), COALESCE(l.upgrade_available, 0), COALESCE(l.upgrade_hint, '')
		FROM feed_items f
		LEFT JOIN library_items l ON f.item_key = l.item_key
		WHERE ` + strings.Join(clauses, " AND ")

	if wantDesc {
		query += " ORDER BY f.pub_ts DESC, f.feed_key DESC"
	} else {
		query += " ORDER BY f.pub_ts ASC, f.feed_key ASC"
	}
	query += fmt.Sprintf(" LIMIT %d", limit+1)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return FeedPageResult{}, err
	}
	defer rows.Close()

	var items []FeedEntry
	for rows.Next() {
		var e FeedEntry
		var cachedInt, upAvail int
		if err := rows.Scan(
			&e.FeedKey, &e.Site, &e.Kind, &e.RawID, &e.Mid, &e.AuthorUID, &e.AuthorName, &e.AuthorFace,
			&e.Title, &e.Summary, &e.CoverURL, &e.CanonicalURL, &e.PubTs, &e.DiscoveredAt,
			&cachedInt, &e.CacheStatus, &e.ItemKey,
			&e.ItemDir, &upAvail, &e.UpgradeHint,
		); err != nil {
			return FeedPageResult{}, err
		}
		if e.AuthorUID == "" && e.Mid > 0 && e.Site == "bilibili" {
			e.AuthorUID = strconv.FormatInt(e.Mid, 10)
		}
		e.Cached = cachedInt == 1
		e.UpgradeAvailable = upAvail == 1
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return FeedPageResult{}, err
	}

	hasMore := false
	if len(items) > limit {
		hasMore = true
		items = items[:limit]
	}
	if reverseAfter && len(items) > 1 {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}

	var res FeedPageResult
	res.Items = items
	res.HasMore = hasMore
	if len(items) > 0 {
		first := items[0]
		last := items[len(items)-1]
		res.PrevCursor = &FeedCursor{PubTs: first.PubTs, Key: first.FeedKey}
		res.NextCursor = &FeedCursor{PubTs: last.PubTs, Key: last.FeedKey}
	}
	return res, nil
}

func feedPageClauses(req FeedPageRequest) ([]string, []any) {
	clauses := []string{"NOT (f.kind = 'video' AND EXISTS (SELECT 1 FROM item_refs r WHERE r.to_key = f.feed_key))"}
	var args []any
	if req.CachedOnly {
		clauses = append(clauses, "f.cached = 1")
	}
	if req.Kind != "" && req.Kind != "all" {
		clauses = append(clauses, "f.kind = ?")
		args = append(args, req.Kind)
	}
	clauses, args = appendSiteAndAuthorClauses(clauses, args, "f.", req.Site, 0, "", req.Authors, req.AuthorRefs)
	return clauses, args
}

// Resolve a surviving key first; otherwise choose the newest item at or before
// the saved timestamp (the earliest item if everything is newer). Resolving the
// pivot before paging also handles more than one page of equal timestamps.
func (s *Store) feedAnchorCursor(req FeedPageRequest) (*FeedCursor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clauses, args := feedPageClauses(req)
	base := `SELECT f.feed_key, f.pub_ts FROM feed_items f WHERE ` + strings.Join(clauses, " AND ")
	var pivot FeedCursor
	if req.Cursor.Key != "" {
		keyArgs := append(append([]any(nil), args...), req.Cursor.Key)
		err := s.db.QueryRow(base+` AND f.feed_key = ?`, keyArgs...).Scan(&pivot.Key, &pivot.PubTs)
		if err == nil {
			return &pivot, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if req.Cursor.PubTs > 0 {
		tsArgs := append(append([]any(nil), args...), req.Cursor.PubTs)
		err := s.db.QueryRow(base+` AND f.pub_ts <= ? ORDER BY f.pub_ts DESC, f.feed_key DESC LIMIT 1`, tsArgs...).Scan(&pivot.Key, &pivot.PubTs)
		if err == nil {
			return &pivot, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	order := "DESC"
	if req.Cursor.PubTs > 0 {
		order = "ASC"
	}
	err := s.db.QueryRow(base+" ORDER BY f.pub_ts "+order+", f.feed_key "+order+" LIMIT 1", args...).Scan(&pivot.Key, &pivot.PubTs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &pivot, err
}

// NewerCount returns the number of feed items published more recently than the given anchor (pubTs, key).
func (s *Store) NewerCount(anchorPubTs int64, anchorKey string) (int, error) {
	return s.newerCount(anchorPubTs, anchorKey, false)
}

func (s *Store) NewerCachedCount(anchorPubTs int64, anchorKey string) (int, error) {
	return s.newerCount(anchorPubTs, anchorKey, true)
}

func (s *Store) newerCount(anchorPubTs int64, anchorKey string, cachedOnly bool) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if anchorPubTs <= 0 && anchorKey == "" {
		return 0, nil
	}
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(1) FROM feed_items f
		WHERE (? = 0 OR f.cached = 1) AND NOT (f.kind = 'video' AND EXISTS (SELECT 1 FROM item_refs r WHERE r.to_key = f.feed_key))
		  AND (f.pub_ts > ? OR (f.pub_ts = ? AND f.feed_key > ?))
	`, cachedOnly, anchorPubTs, anchorPubTs, anchorKey).Scan(&count)
	return count, err
}

// QueryFeed queries the unified feed (both online-discovered and locally cached items) ordered by pub_ts DESC.
func (s *Store) QueryFeed(filter FeedFilter) ([]FeedEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var clauses []string
	var args []any
	if filter.Kind != "" && filter.Kind != "all" {
		clauses = append(clauses, "f.kind = ?")
		args = append(args, filter.Kind)
	}
	clauses, args = appendSiteAndAuthorClauses(clauses, args, "f.", filter.Site, filter.Mid, filter.AuthorUID, filter.Authors, filter.AuthorRefs)
	if filter.CachedOnly {
		clauses = append(clauses, "f.cached = 1")
	}
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		clauses = append(clauses, `(f.title LIKE ? ESCAPE '\' OR f.summary LIKE ? ESCAPE '\' OR f.author_name LIKE ? ESCAPE '\' OR f.raw_id LIKE ? ESCAPE '\')`)
		pat := escapeLikePattern(kw)
		args = append(args, pat, pat, pat, pat)
	}

	query := `
		SELECT
			f.feed_key, f.site, f.kind, f.raw_id, f.mid, f.author_uid, f.author_name, f.author_face,
			f.title, f.summary, f.cover_url, f.canonical_url, f.pub_ts, f.discovered_at,
			f.cached, f.cache_status, f.item_key,
			COALESCE(l.item_dir, ''), COALESCE(l.upgrade_available, 0), COALESCE(l.upgrade_hint, '')
		FROM feed_items f
		LEFT JOIN library_items l ON f.item_key = l.item_key
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	if strings.EqualFold(filter.Sort, "oldest") {
		query += " ORDER BY f.pub_ts ASC, f.feed_key ASC"
	} else {
		query += " ORDER BY f.pub_ts DESC, f.feed_key DESC"
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(0, filter.Offset))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedEntry
	for rows.Next() {
		var e FeedEntry
		var cachedInt, upAvail int
		if err := rows.Scan(
			&e.FeedKey, &e.Site, &e.Kind, &e.RawID, &e.Mid, &e.AuthorUID, &e.AuthorName, &e.AuthorFace,
			&e.Title, &e.Summary, &e.CoverURL, &e.CanonicalURL, &e.PubTs, &e.DiscoveredAt,
			&cachedInt, &e.CacheStatus, &e.ItemKey,
			&e.ItemDir, &upAvail, &e.UpgradeHint,
		); err != nil {
			return nil, err
		}
		if e.AuthorUID == "" && e.Mid > 0 && e.Site == "bilibili" {
			e.AuthorUID = strconv.FormatInt(e.Mid, 10)
		}
		e.Cached = cachedInt == 1
		e.UpgradeAvailable = upAvail == 1
		out = append(out, e)
	}
	return out, rows.Err()
}
