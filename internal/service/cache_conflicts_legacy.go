package service

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/weibo"
)

var (
	reConflictEditCountCN     = regexp.MustCompile(`第\s*(\d{1,5})\s*次编辑`)
	reLegacySuperTopicInClean = regexp.MustCompile(`#([^#\r\n\[\]\s]+)\[超话\]#`)
)

// reconcileLegacyInlineURLConflicts inspects pending text_edited conflicts and
// automatically reconciles pre-DEC-073 false positives whose only local-vs-remote
// difference is legacy inline parenthesized URLs (e.g. "名称超话 (sinaweibo://...)").
// Offline reconciliation requires an exact SHA-256 match against rec.RemoteFingerprint
// so genuine text edits are never dismissed.
func (b *Backend) reconcileLegacyInlineURLConflicts() {
	if b == nil || b.profile == ProfileRemote || b.conflicts == nil || b.lib == nil {
		return
	}
	candidates := b.conflicts.pendingTextConflictCandidates()
	if len(candidates) == 0 {
		return
	}

	var resolvedSites []string
	var resolvedKeys []string
	for _, rec := range candidates {
		localMeta, ok, err := b.lib.GetItem(rec.ItemKey)
		if err != nil || !ok {
			b.conflicts.markNonLegacyTextConflict(rec.ID, rec.RemoteFingerprint)
			continue
		}
		cleanedMeta, hadLegacy := cleanLegacyInlineLinksInItem(localMeta, nil)
		if !hadLegacy {
			b.conflicts.markNonLegacyTextConflict(rec.ID, rec.RemoteFingerprint)
			continue
		}
		if !matchesLegacyInlineOnlyConflictFingerprint(rec, localMeta, cleanedMeta) {
			b.conflicts.markNonLegacyTextConflict(rec.ID, rec.RemoteFingerprint)
			continue
		}
		if strings.TrimSpace(localMeta.ItemDir) == "" {
			continue
		}
		if _, saveErr := b.lib.SaveItemMetadataOnly(localMeta.ItemDir, cleanedMeta); saveErr != nil {
			b.logError("conflicts.reconcile_legacy_save", saveErr)
			continue
		}
		if b.conflicts.removePendingIfMatching(rec.ID, rec.RemoteFingerprint) {
			resolvedSites = append(resolvedSites, rec.Site)
			resolvedKeys = append(resolvedKeys, rec.ItemKey)
		}
	}

	if len(resolvedKeys) > 0 && b.events != nil {
		for i, key := range resolvedKeys {
			b.events.PublishSite(EventCacheConflictsChanged, resolvedSites[i], key)
			b.events.PublishSite(EventLibraryChanged, resolvedSites[i], key)
		}
	}
}

// reconcileLiveLegacyTextConflict reconciles an existing pending text_edited conflict
// when a live comparison (fullCache inspection or ConflictDiff review) confirms that
// media is unchanged and normalized local/remote text are identical under
// NormalizeConflictText, differing only by pre-DEC-073 inline URL formatting.
func (b *Backend) reconcileLiveLegacyTextConflict(itemKey string, remoteLinks []TextLinkDTO) (library.ItemMetadata, bool) {
	if b == nil || b.profile == ProfileRemote || b.conflicts == nil || b.lib == nil {
		return library.ItemMetadata{}, false
	}
	rec, ok := b.conflicts.pendingTextConflictForItem(itemKey)
	if !ok {
		return library.ItemMetadata{}, false
	}
	localMeta, exists, err := b.lib.GetItem(rec.ItemKey)
	if err != nil || !exists || strings.TrimSpace(localMeta.ItemDir) == "" {
		return library.ItemMetadata{}, false
	}
	cleanedMeta, hadLegacy := cleanLegacyInlineLinksInItem(localMeta, remoteLinks)
	if !hadLegacy {
		return library.ItemMetadata{}, false
	}
	saved, saveErr := b.lib.SaveItemMetadataOnly(localMeta.ItemDir, cleanedMeta)
	if saveErr != nil {
		b.logError("conflicts.reconcile_live_legacy_save", saveErr)
		return library.ItemMetadata{}, false
	}
	if !b.conflicts.removePendingIfMatching(rec.ID, rec.RemoteFingerprint) {
		return saved, false
	}
	if b.events != nil {
		b.events.PublishSite(EventCacheConflictsChanged, rec.Site, rec.ItemKey)
		b.events.PublishSite(EventLibraryChanged, rec.Site, rec.ItemKey)
	}
	return saved, true
}

func matchesLegacyInlineOnlyConflictFingerprint(rec cacheConflictRecord, localMeta, cleanedMeta library.ItemMetadata) bool {
	targetFP := strings.TrimSpace(rec.RemoteFingerprint)
	if targetFP == "" {
		return false
	}
	for _, cand := range computeLocalCandidateFingerprints(rec, localMeta, cleanedMeta) {
		if cand != "" && cand == targetFP {
			return true
		}
	}
	return false
}

func computeLocalCandidateFingerprints(rec cacheConflictRecord, localMeta, cleanedMeta library.ItemMetadata) []string {
	siteID := strings.ToLower(strings.TrimSpace(firstNonEmpty(rec.Site, localMeta.Site)))
	kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(rec.Kind, localMeta.Kind)))
	targetID := strings.TrimSpace(firstNonEmpty(rec.TargetID, localMeta.RawID))
	if siteID == "" || targetID == "" {
		return nil
	}

	switch siteID {
	case "weibo", "douyin", "x":
		localSingleVid := localMeta.DouyinVideo != nil && len(localMeta.NotePages) == 0
		fpKind := "dynamic"
		fpPages := len(localMeta.NotePages)
		fpVidDur := 0
		if localSingleVid {
			fpKind = "video"
			fpPages = 0
			fpVidDur = localMeta.Duration
		}
		var assets []string
		if len(localMeta.AssetIDs) > 0 {
			assets = localMeta.AssetIDs
		} else if localSingleVid {
			if mid := metaVideoMediaID(localMeta); mid != "" {
				assets = []string{mid}
			}
		} else if len(localMeta.NotePages) > 0 {
			for _, pg := range localMeta.NotePages {
				if tok := library.SanitizeMediaToken(pg.MediaID); tok != "" {
					assets = append(assets, tok)
				}
			}
		}

		focalText := firstNonEmpty(cleanedMeta.Text, cleanedMeta.Desc)
		if siteID == "douyin" {
			focalText = firstNonEmpty(cleanedMeta.Desc, cleanedMeta.Text)
		}

		editCounts := []int{max(0, localMeta.EditCount)}
		if localMeta.EditCount == 0 {
			if m := reConflictEditCountCN.FindStringSubmatch(rec.RemoteSummary); len(m) == 2 {
				if parsed, err := strconv.Atoi(m[1]); err == nil && parsed > 0 {
					editCounts = append(editCounts, parsed)
				}
			}
		}

		fps := make([]string, 0, len(editCounts)*2)
		for _, ec := range editCounts {
			fps = append(fps, download.WorkConflictFingerprint(
				siteID, fpKind, targetID, assets, ec, fpPages, fpVidDur, focalText,
			))
			if siteID == "weibo" && kind == "video" && fpKind != "video" {
				fps = append(fps, download.WorkConflictFingerprint(
					siteID, "video", targetID, assets, ec, 0, localMeta.Duration, focalText,
				))
			}
		}
		return fps

	case "bilibili":
		if kind == "video" {
			pages := make([]bilibili.VideoPage, 0, len(localMeta.Parts))
			for _, p := range localMeta.Parts {
				pages = append(pages, bilibili.VideoPage{
					Page:     p.Page,
					CID:      p.CID,
					Duration: p.Duration,
				})
			}
			return []string{
				download.BilibiliVideoConflictFingerprint(targetID, pages, cleanedMeta.Title, cleanedMeta.Desc),
			}
		}
		var imgURLs []string
		for _, mf := range localMeta.Media {
			if mf.Role == "image" && strings.TrimSpace(mf.Source) != "" {
				imgURLs = append(imgURLs, mf.Source)
			}
		}
		return []string{
			download.BilibiliDynamicConflictFingerprint(targetID, imgURLs, localMeta.Videos, cleanedMeta.Title, cleanedMeta.Text),
		}
	}
	return nil
}

// cleanLegacyInlineLinksInItem converts pre-DEC-073 inline parenthesized URLs
// (e.g. "名称超话 (sinaweibo://pageinfo?containerid=...)" or "标题 (https://...)")
// in item text fields into clean readable text plus safe ItemLink records without
// modifying CachedAt, media files, or any other manifest fields.
func cleanLegacyInlineLinksInItem(meta library.ItemMetadata, extraRemoteLinks []TextLinkDTO) (library.ItemMetadata, bool) {
	out := meta
	if len(meta.Links) > 0 {
		out.Links = append([]library.ItemLink(nil), meta.Links...)
	}
	if meta.Origin != nil {
		origCopy := *meta.Origin
		if len(meta.Origin.Links) > 0 {
			origCopy.Links = append([]library.ItemLink(nil), meta.Origin.Links...)
		}
		out.Origin = &origCopy
	}

	siteID := strings.ToLower(strings.TrimSpace(meta.Site))
	var extracted []library.ItemLink
	changed := false

	if out.Text != "" {
		cleanText, links, textChanged := extractLegacyInlineLinksFromText(out.Text, siteID)
		if textChanged {
			out.Text = cleanText
			extracted = append(extracted, links...)
			changed = true
		}
	}
	if out.Desc != "" {
		cleanDesc, links, descChanged := extractLegacyInlineLinksFromText(out.Desc, siteID)
		if descChanged {
			out.Desc = cleanDesc
			extracted = append(extracted, links...)
			changed = true
		}
	}
	if out.Title != "" {
		cleanTitle, links, titleChanged := extractLegacyInlineLinksFromText(out.Title, siteID)
		if titleChanged {
			out.Title = cleanTitle
			extracted = append(extracted, links...)
			changed = true
		}
	}
	if out.Origin != nil && out.Origin.Text != "" {
		cleanOrig, origLinks, origChanged := extractLegacyInlineLinksFromText(out.Origin.Text, firstNonEmpty(out.Origin.Site, siteID))
		if origChanged {
			out.Origin.Text = cleanOrig
			out.Origin.Links = mergeSanitizedItemLinks(out.Origin.Links, origLinks)
			changed = true
		}
	}

	if !changed {
		return meta, false
	}

	// Merge any matching safe remote links (e.g. supertopic links whose local text had "#名称[超话]#").
	bodyForMatch := firstNonEmpty(out.Text, out.Desc)
	for _, rl := range extraRemoteLinks {
		lbl := strings.TrimSpace(rl.Label)
		u := strings.TrimSpace(rl.URL)
		if lbl != "" && u != "" && strings.Contains(bodyForMatch, lbl) {
			extracted = append(extracted, library.ItemLink{Label: lbl, URL: u})
		}
	}
	out.Links = mergeSanitizedItemLinks(out.Links, extracted)
	return out, true
}

func mergeSanitizedItemLinks(existing, extra []library.ItemLink) []library.ItemLink {
	combined := make([]library.ItemLink, 0, len(existing)+len(extra))
	combined = append(combined, existing...)
	combined = append(combined, extra...)
	sanitized := library.SanitizeItemLinks(combined)
	if len(sanitized) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(sanitized))
	out := make([]library.ItemLink, 0, len(sanitized))
	for _, l := range sanitized {
		key := l.Label + "\x00" + l.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	return out
}

// extractLegacyInlineLinksFromText strips legacy pre-DEC-073 parenthesized URL
// suffixes ("标题 (sinaweibo://...)", "标题 (https://...)", "标题 (http://...)",
// and fullwidth equivalents) and returns cleaned text plus extracted safe links.
func extractLegacyInlineLinksFromText(raw, siteID string) (cleaned string, links []library.ItemLink, changed bool) {
	if raw == "" {
		return "", nil, false
	}
	var sb strings.Builder
	sb.Grow(len(raw))

	for i := 0; i < len(raw); {
		openASCII := raw[i] == '('
		openFull := !openASCII && strings.HasPrefix(raw[i:], "（")
		if !openASCII && !openFull {
			_, w := utf8.DecodeRuneInString(raw[i:])
			sb.WriteString(raw[i : i+w])
			i += w
			continue
		}

		openLen := 1
		if openFull {
			openLen = len("（")
		}
		urlStart := i + openLen
		schemeKind := detectLegacyURLScheme(raw[urlStart:])
		if schemeKind == "" {
			sb.WriteString(raw[i:urlStart])
			i = urlStart
			continue
		}

		curOut := sb.String()
		trimmedOut := strings.TrimRight(curOut, " \t")
		if trimmedOut == "" {
			sb.WriteString(raw[i:urlStart])
			i = urlStart
			continue
		}
		lastRune, _ := utf8.DecodeLastRuneInString(trimmedOut)
		if lastRune == '\n' || lastRune == '\r' || unicode.IsSpace(lastRune) {
			sb.WriteString(raw[i:urlStart])
			i = urlStart
			continue
		}

		urlEnd, closeEnd, ok := scanParenthesizedInlineURL(raw, urlStart, openASCII, schemeKind == "sinaweibo")
		if !ok {
			sb.WriteString(raw[i:urlStart])
			i = urlStart
			continue
		}

		rawURL := strings.TrimSpace(raw[urlStart:urlEnd])
		label := extractTrailingLegacyLabel(trimmedOut)
		safeURL := normalizeLegacyExtractedURL(rawURL, siteID)
		if safeURL != "" && label != "" {
			links = append(links, library.ItemLink{
				Label: label,
				URL:   safeURL,
			})
		}

		if len(trimmedOut) < len(curOut) {
			sb.Reset()
			sb.WriteString(trimmedOut)
		}
		changed = true
		i = closeEnd
	}

	outText := sb.String()
	if strings.EqualFold(siteID, "weibo") && strings.Contains(outText, "[超话]#") {
		replaced := reLegacySuperTopicInClean.ReplaceAllString(outText, "${1}超话")
		if replaced != outText {
			outText = replaced
			changed = true
		}
	}
	if !changed {
		return raw, nil, false
	}
	return outText, library.SanitizeItemLinks(links), true
}

func detectLegacyURLScheme(rest string) string {
	if len(rest) >= len("sinaweibo://") && strings.EqualFold(rest[:len("sinaweibo://")], "sinaweibo://") {
		return "sinaweibo"
	}
	if len(rest) >= len("https://") && strings.EqualFold(rest[:len("https://")], "https://") {
		return "https"
	}
	if len(rest) >= len("http://") && strings.EqualFold(rest[:len("http://")], "http://") {
		return "http"
	}
	return ""
}

func scanParenthesizedInlineURL(s string, urlStart int, openASCII, isSinaWeibo bool) (urlEnd, closeEnd int, ok bool) {
	sep := strings.Index(s[urlStart:], "://")
	if sep < 0 {
		return 0, 0, false
	}
	bodyStart := urlStart + sep + 3
	i := urlStart
	parenDepth := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r <= 0x1f || r == 0x7f || r == '\r' || r == '\n' || r == '<' || r == '>' || r == '"' || r == '\'' || r == '\\' {
			return 0, 0, false
		}
		if !isSinaWeibo && unicode.IsSpace(r) {
			return 0, 0, false
		}
		if openASCII {
			if r == '(' {
				parenDepth++
			} else if r == ')' {
				if parenDepth == 0 {
					if i <= bodyStart {
						return 0, 0, false
					}
					return i, i + w, true
				}
				parenDepth--
			}
		} else if r == '）' {
			if i <= bodyStart {
				return 0, 0, false
			}
			return i, i + w, true
		}
		i += w
	}
	return 0, 0, false
}

func extractTrailingLegacyLabel(prefix string) string {
	end := len(prefix)
	start := end
	runes := 0
	for start > 0 {
		r, w := utf8.DecodeLastRuneInString(prefix[:start])
		if unicode.IsSpace(r) || isLegacyDiffLabelBoundary(r) {
			break
		}
		start -= w
		runes++
		if runes >= 64 {
			break
		}
	}
	cand := strings.TrimSpace(prefix[start:end])
	if cand == "" || strings.Contains(cand, "://") {
		return ""
	}
	return cand
}

func isLegacyDiffLabelBoundary(r rune) bool {
	switch r {
	case '，', '。', '：', '；', '！', '？', '、', '（', '）', '(', ')', '【', '】', '《', '》', '"', '\'', ',', ';', ':':
		return true
	default:
		return false
	}
}

func normalizeLegacyExtractedURL(rawURL, siteID string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "sinaweibo://") {
		return weibo.RewriteSafeSinaWeiboAppURL(trimmed)
	}
	if strings.EqualFold(siteID, "weibo") {
		return weibo.NormalizeWeiboJumpURL(trimmed, false)
	}
	sanitized := library.SanitizeItemLinks([]library.ItemLink{{Label: "link", URL: trimmed}})
	if len(sanitized) == 0 {
		return ""
	}
	return sanitized[0].URL
}

// sanitizeConflictDiffLinks filters and deduplicates hyperlinks for the in-memory
// CacheConflictDiffDTO payload so unsafe schemes, credentials, and signed media
// CDN URLs never surface in the diff modal.
func sanitizeConflictDiffLinks(links []TextLinkDTO) []TextLinkDTO {
	if len(links) == 0 {
		return nil
	}
	itemLinks := make([]library.ItemLink, 0, len(links))
	for _, l := range links {
		rawURL := strings.TrimSpace(l.URL)
		if strings.HasPrefix(strings.ToLower(rawURL), "sinaweibo://") {
			rawURL = weibo.RewriteSafeSinaWeiboAppURL(rawURL)
		}
		if rawURL == "" {
			continue
		}
		itemLinks = append(itemLinks, library.ItemLink{
			Label: strings.TrimSpace(l.Label),
			URL:   rawURL,
		})
	}
	clean := library.SanitizeItemLinks(itemLinks)
	if len(clean) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(clean))
	out := make([]TextLinkDTO, 0, len(clean))
	for _, l := range clean {
		key := l.Label + "\x00" + l.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, TextLinkDTO{Label: l.Label, URL: l.URL})
	}
	return out
}

// extractSafeLinksFromDiffText extracts safe external hyperlinks from free text
// (handling legacy "标题 (sinaweibo://...)" / "标题 (http(s)://...)" as well as
// bare http(s):// URLs) for populating ConflictDiff LocalLinks/RemoteLinks when
// structured links are absent or incomplete.
func extractSafeLinksFromDiffText(text, siteID string) []TextLinkDTO {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []TextLinkDTO
	seenURL := make(map[string]bool)
	addLink := func(label, targetURL string) {
		if targetURL == "" || seenURL[targetURL] {
			return
		}
		seenURL[targetURL] = true
		if strings.TrimSpace(label) == "" {
			label = conciseDiffLinkLabel(targetURL)
		}
		out = append(out, TextLinkDTO{Label: label, URL: targetURL})
	}

	// 1. Legacy parenthesized "标题 (sinaweibo://...)" / "标题 (http(s)://...)"
	cleaned, legacyLinks, _ := extractLegacyInlineLinksFromText(text, siteID)
	for _, ll := range legacyLinks {
		addLink(ll.Label, ll.URL)
	}

	// 2. Remaining bare http:// / https:// / allowlisted sinaweibo:// tokens
	pos := 0
	for pos < len(cleaned) {
		relIdx, isSina := indexDiffInlineURLScheme(cleaned[pos:])
		if relIdx < 0 {
			break
		}
		start := pos + relIdx
		end := scanBareDiffURLEnd(cleaned, start)
		cand := strings.TrimRight(cleaned[start:end], ".,;:!?)]}\"'")
		safeURL := normalizeLegacyExtractedURL(cand, siteID)
		if safeURL != "" {
			addLink(conciseDiffLinkLabel(safeURL), safeURL)
		}
		if end <= start {
			if isSina {
				pos = start + len("sinaweibo://")
			} else {
				pos = start + 4
			}
		} else {
			pos = end
		}
	}
	return sanitizeConflictDiffLinks(out)
}

func indexDiffInlineURLScheme(s string) (idx int, isSina bool) {
	lower := strings.ToLower(s)
	best := -1
	bestSina := false
	check := func(p int, sina bool) {
		if p >= 0 && (best < 0 || p < best) {
			best = p
			bestSina = sina
		}
	}
	check(strings.Index(lower, "https://"), false)
	check(strings.Index(lower, "http://"), false)
	check(strings.Index(lower, "sinaweibo://"), true)
	return best, bestSina
}

func scanBareDiffURLEnd(s string, start int) int {
	i := start
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) || r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' || r > 0x7e {
			break
		}
		i += w
	}
	return i
}

func conciseDiffLinkLabel(cleanURL string) string {
	u, err := url.Parse(cleanURL)
	if err != nil || u.Host == "" {
		return "网页链接"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch host {
	case "t.cn", "t.co":
		return "网页链接"
	case "weibo.com", "m.weibo.cn":
		lp := strings.ToLower(u.Path)
		switch {
		case strings.HasPrefix(lp, "/ttarticle/"):
			return "头条文章"
		case strings.HasPrefix(lp, "/tv/show/"):
			return "微博视频"
		case strings.HasPrefix(lp, "/p/100808"):
			return "微博超话"
		}
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return host
	}
	cand := host + p
	if utf8.RuneCountInString(cand) > 30 {
		runes := []rune(cand)
		return string(runes[:29]) + "…"
	}
	return cand
}
