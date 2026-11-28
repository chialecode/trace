package weibo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

const (
	// maxEditHistoryPages bounds pagination on GET /ajax/statuses/editHistory (DEC-094).
	maxEditHistoryPages = 5
	// maxEditHistoryRevisions caps collected revisions per post to protect memory and UI.
	maxEditHistoryRevisions = 100
)

// EditRevision represents one historical snapshot returned by
// GET /ajax/statuses/editHistory?mid=<numeric_mid>&page=<p> (DEC-094).
// GetEditHistory returns revisions ordered newest-first, while Version numbers
// count 1-based from the earliest original version (1) up to the latest version (N).
type EditRevision struct {
	Version      int             `json:"version"`
	EditTs       int64           `json:"editTs,omitempty"`
	CreatedAtRaw string          `json:"createdAtRaw,omitempty"`
	Text         string          `json:"text,omitempty"`
	TextRaw      string          `json:"textRaw,omitempty"`
	Links        []site.TextLink `json:"links,omitempty"`
	Images       []ImageAsset    `json:"images,omitempty"`
}

type rawEditRevision struct {
	rawPost
	EditAt  string          `json:"edit_at"`
	Version json.RawMessage `json:"version"`
}

type rawEditHistoryEnvelope struct {
	OK          json.RawMessage   `json:"ok"`
	Total       json.RawMessage   `json:"total"`
	TotalNumber json.RawMessage   `json:"total_number"`
	Count       json.RawMessage   `json:"count"`
	Statuses    []rawEditRevision `json:"statuses"`
	List        []rawEditRevision `json:"list"`
	Data        *struct {
		Total       json.RawMessage   `json:"total"`
		TotalNumber json.RawMessage   `json:"total_number"`
		Count       json.RawMessage   `json:"count"`
		Statuses    []rawEditRevision `json:"statuses"`
		List        []rawEditRevision `json:"list"`
	} `json:"data"`
}

// GetEditHistory fetches all available historical revisions for an edited Weibo post
// via GET /ajax/statuses/editHistory?mid=<numeric_mid>&page=<p> (DEC-094).
// Callers must skip calling GetEditHistory when post.EditCount == 0.
// Pagination stops on empty statuses, when total is reached, when no new unique
// revision is returned, or after maxEditHistoryPages (5) pages.
func (c *Client) GetEditHistory(ctx context.Context, idOrMblogID string, opts ...PostOptions) ([]EditRevision, error) {
	var opt PostOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.StreamOptions.PreferredCodec == "" {
		opt.StreamOptions.PreferredCodec = c.preferredCodec()
	}
	if opt.ImageURLRewriter == nil {
		opt.ImageURLRewriter = c.imageURLRewriter
	}

	reqMid, _, ok := parsePostIdentifier(idOrMblogID)
	if !ok || !reMidDigits.MatchString(reqMid) {
		return nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博编号或 mblogid",
		}
	}

	var collected []EditRevision
	seen := make(map[string]bool)
	reportedTotal := 0

	for page := 1; page <= maxEditHistoryPages; page++ {
		q := url.Values{}
		q.Set("mid", reqMid)
		q.Set("page", strconv.Itoa(page))

		rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
			endpoint: c.endpoints.StatusesEditHistory,
			query:    q,
			referer:  DefaultReferer,
		})
		if err != nil {
			if page > 1 && len(collected) > 0 && !site.IsRoundStop(err) {
				break
			}
			return nil, err
		}

		pageRevs, pageTotal, parseErr := c.parseEditHistoryPageJSON(rawResp.body, opt)
		if parseErr != nil {
			if page > 1 && len(collected) > 0 {
				break
			}
			return nil, parseErr
		}
		if pageTotal > reportedTotal {
			reportedTotal = pageTotal
		}
		if len(pageRevs) == 0 {
			break
		}

		added := 0
		for _, rev := range pageRevs {
			sig := editRevisionDedupKey(rev)
			if seen[sig] {
				continue
			}
			seen[sig] = true
			collected = append(collected, rev)
			added++
			if len(collected) >= maxEditHistoryRevisions {
				break
			}
		}

		if added == 0 || len(collected) >= maxEditHistoryRevisions {
			break
		}
		if reportedTotal > 0 && len(collected) >= reportedTotal {
			break
		}
	}

	return finalizeEditRevisions(collected, reportedTotal), nil
}

// ParseEditHistoryPageJSON decodes one page of /ajax/statuses/editHistory JSON into
// normalized EditRevision entries and the reported total count.
func (c *Client) ParseEditHistoryPageJSON(rawJSON []byte, opts ...PostOptions) ([]EditRevision, int, error) {
	var opt PostOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.StreamOptions.PreferredCodec == "" {
		opt.StreamOptions.PreferredCodec = c.preferredCodec()
	}
	if opt.ImageURLRewriter == nil {
		opt.ImageURLRewriter = c.imageURLRewriter
	}
	revs, total, err := c.parseEditHistoryPageJSON(rawJSON, opt)
	if err != nil {
		return nil, 0, err
	}
	return finalizeEditRevisions(revs, total), total, nil
}

func (c *Client) parseEditHistoryPageJSON(rawJSON []byte, opt PostOptions) ([]EditRevision, int, error) {
	var env rawEditHistoryEnvelope
	dec := json.NewDecoder(bytes.NewReader(rawJSON))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, 0, &site.Error{
			Site:     site.Weibo,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(c.endpoints.StatusesEditHistory),
			Message:  "解析微博编辑历史 JSON 失败",
			Err:      err,
		}
	}

	items := env.Statuses
	if len(items) == 0 && len(env.List) > 0 {
		items = env.List
	}
	if len(items) == 0 && env.Data != nil {
		if len(env.Data.Statuses) > 0 {
			items = env.Data.Statuses
		} else if len(env.Data.List) > 0 {
			items = env.Data.List
		}
	}

	total := firstPositiveInt(
		parseJSONInt(env.Total),
		parseJSONInt(env.TotalNumber),
		parseJSONInt(env.Count),
	)
	if total == 0 && env.Data != nil {
		total = firstPositiveInt(
			parseJSONInt(env.Data.Total),
			parseJSONInt(env.Data.TotalNumber),
			parseJSONInt(env.Data.Count),
		)
	}

	if len(items) == 0 {
		return nil, total, nil
	}

	out := make([]EditRevision, 0, len(items))
	for i := range items {
		rev, ok := c.parseRawEditRevision(&items[i], opt)
		if !ok {
			continue
		}
		out = append(out, rev)
	}
	return out, total, nil
}

func (c *Client) parseRawEditRevision(raw *rawEditRevision, opt PostOptions) (EditRevision, bool) {
	if raw == nil {
		return EditRevision{}, false
	}

	rawTime := strings.TrimSpace(raw.EditAt)
	editTs := parseEditTimestamp(rawTime)
	if editTs == 0 {
		if created := strings.TrimSpace(raw.CreatedAt); created != "" {
			rawTime = created
			editTs = parseEditTimestamp(created)
		}
	}

	urlMappings := parseURLStruct(raw.URLStruct, c.allowLocalhost)
	textRaw := strings.TrimSpace(raw.TextRaw)
	if raw.LongText != nil && strings.TrimSpace(raw.LongText.LongTextContent) != "" {
		textRaw = strings.TrimSpace(raw.LongText.LongTextContent)
		if len(raw.LongText.URLStruct) > 0 {
			urlMappings = mergeURLMappings(urlMappings, parseURLStruct(raw.LongText.URLStruct, c.allowLocalhost))
		}
	}
	if textRaw == "" && strings.TrimSpace(raw.Text) != "" {
		textRaw = stripHTMLText(raw.Text)
	}
	displayText, links := extractWeiboTextAndLinks(raw.Text, textRaw, urlMappings, c.allowLocalhost)

	// Extract only public, unsigned sinaimg.cn image assets (never signed video or LivePhoto motion URLs).
	parsedMedia, _ := c.parsePostMediaItemsWithMeta(&raw.rawPost, opt)
	var safeImages []ImageAsset
	for _, img := range parsedMedia {
		if img.Kind == MediaKindVideo {
			continue
		}
		safePrimary := SafePublicImageURL(img.URL)
		if safePrimary == "" {
			safePrimary = SafePublicImageURL(img.PublicPreviewURL)
		}
		if safePrimary == "" {
			continue
		}
		var safeFallbacks []string
		for _, fb := range img.FallbackURLs {
			if cleanFB := SafePublicImageURL(fb); cleanFB != "" && cleanFB != safePrimary {
				safeFallbacks = append(safeFallbacks, cleanFB)
			}
		}
		safeNoWM := SafePublicImageURL(img.WatermarkFreeURL)
		kind := img.Kind
		if kind == "" {
			kind = MediaKindImage
		}
		safeImages = append(safeImages, ImageAsset{
			Index:            len(safeImages) + 1,
			PicID:            strings.TrimSpace(img.PicID),
			Kind:             kind,
			Width:            img.Width,
			Height:           img.Height,
			PublicPreviewURL: safePrimary,
			URL:              safePrimary,
			FallbackURLs:     safeFallbacks,
			WatermarkFreeURL: safeNoWM,
		})
	}

	if displayText == "" && textRaw == "" && len(links) == 0 && len(safeImages) == 0 && editTs == 0 {
		return EditRevision{}, false
	}

	return EditRevision{
		Version:      parseJSONInt(raw.Version),
		EditTs:       editTs,
		CreatedAtRaw: rawTime,
		Text:         displayText,
		TextRaw:      textRaw,
		Links:        links,
		Images:       safeImages,
	}, true
}

func parseEditTimestamp(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if ts := parseWeiboCreatedAt(s); ts > 0 {
		return ts
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if n > 1e12 {
			return n / 1000
		}
		return n
	}
	return 0
}

func firstPositiveInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func editRevisionDedupKey(rev EditRevision) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(rev.Version))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(rev.EditTs, 10))
	b.WriteByte('|')
	b.WriteString(rev.Text)
	for _, img := range rev.Images {
		b.WriteByte('|')
		if img.PicID != "" {
			b.WriteString(img.PicID)
		} else {
			b.WriteString(img.PublicPreviewURL)
		}
	}
	return b.String()
}

// finalizeEditRevisions normalizes ordering to newest-first and assigns 1-based
// versions where 1 is the earliest original version and N is the latest version.
func finalizeEditRevisions(revs []EditRevision, reportedTotal int) []EditRevision {
	if len(revs) == 0 {
		return nil
	}
	out := make([]EditRevision, len(revs))
	copy(out, revs)

	// If timestamps are clearly ascending (oldest-first), flip to newest-first
	// so callers always get consistent newest-first presentation.
	if len(out) >= 2 && out[0].EditTs > 0 && out[len(out)-1].EditTs > 0 && out[0].EditTs < out[len(out)-1].EditTs {
		for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
			out[l], out[r] = out[r], out[l]
		}
	}

	allExplicit := true
	for _, rev := range out {
		if rev.Version <= 0 {
			allExplicit = false
			break
		}
	}
	if allExplicit {
		return out
	}

	topVersion := len(out)
	if reportedTotal > topVersion {
		topVersion = reportedTotal
	}
	for i := range out {
		if out[i].Version <= 0 {
			v := topVersion - i
			if v < 1 {
				v = 1
			}
			out[i].Version = v
		}
	}
	return out
}
