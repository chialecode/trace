package x

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"unicode/utf16"
)

// Normalized X Articles Draft.js block types (DEC-093).
const (
	ArticleBlockParagraph     = "unstyled"
	ArticleBlockHeaderOne     = "header-one"
	ArticleBlockHeaderTwo     = "header-two"
	ArticleBlockBlockquote    = "blockquote"
	ArticleBlockOrderedList   = "ordered-list-item"
	ArticleBlockUnorderedList = "unordered-list-item"
	ArticleBlockAtomic        = "atomic"
)

// articleFieldToggles requests full Draft.js content_state and plain_text on
// TweetDetail, UserArticlesTweets, and ListLatestTweetsTimeline (DEC-093).
func articleFieldToggles() map[string]bool {
	return map[string]bool{
		"withArticleRichContentState": true,
		"withArticlePlainText":        true,
	}
}

// ArticleLink holds a validated safe external HTTP(S) link embedded in an X Article block.
type ArticleLink struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// ArticleImage represents the cover or an inline image of an X Article.
// Raw CDN download URLs are tagged json:"-" so they are never persisted into manifests.
type ArticleImage struct {
	MediaID          string `json:"mediaId,omitempty"`
	MediaKey         string `json:"mediaKey,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	AltText          string `json:"altText,omitempty"`
	PublicPreviewURL string `json:"publicPreviewUrl,omitempty"`
	ImageURL         string `json:"-"`
	FallbackURL      string `json:"-"`
}

// ArticleBlock represents one ordered rich-text, embedded-image, or embedded-tweet
// block parsed from an X Article's Draft.js content_state.
type ArticleBlock struct {
	Key     string        `json:"key,omitempty"`
	Type    string        `json:"type"`
	Text    string        `json:"text,omitempty"`
	Links   []ArticleLink `json:"links,omitempty"`
	Image   *ArticleImage `json:"image,omitempty"`
	TweetID string        `json:"tweetId,omitempty"`
}

// Article holds the parsed rich content of an X Article attached to a tweet
// (tweet.article.article_results.result).
type Article struct {
	ID           string         `json:"id,omitempty"`
	RestID       string         `json:"restId,omitempty"`
	Title        string         `json:"title,omitempty"`
	PreviewText  string         `json:"previewText,omitempty"`
	PlainText    string         `json:"plainText,omitempty"`
	Cover        *ArticleImage  `json:"cover,omitempty"`
	Blocks       []ArticleBlock `json:"blocks,omitempty"`
	Images       []ArticleImage `json:"images,omitempty"`
	CanonicalURL string         `json:"canonicalUrl,omitempty"`
}

// HasFullContent reports whether the article contains parsed rich blocks or plain text
// beyond a degraded title/preview card stub.
func (a *Article) HasFullContent() bool {
	if a == nil {
		return false
	}
	return len(a.Blocks) > 0 || strings.TrimSpace(a.PlainText) != ""
}

// FullPlainText returns the best plain-text representation of the article (for search,
// summary fallback, and backward-compatible item.json text fields).
func (a *Article) FullPlainText() string {
	if a == nil {
		return ""
	}
	if s := strings.TrimSpace(a.PlainText); s != "" {
		return s
	}
	var parts []string
	if t := strings.TrimSpace(a.Title); t != "" {
		parts = append(parts, t)
	}
	ord := 0
	for _, blk := range a.Blocks {
		txt := strings.TrimSpace(blk.Text)
		if txt == "" {
			if blk.Type != ArticleBlockOrderedList {
				ord = 0
			}
			continue
		}
		switch blk.Type {
		case ArticleBlockOrderedList:
			ord++
			parts = append(parts, fmtOrderedPrefix(ord)+txt)
		case ArticleBlockUnorderedList:
			ord = 0
			parts = append(parts, "• "+txt)
		case ArticleBlockBlockquote:
			ord = 0
			parts = append(parts, "> "+txt)
		default:
			ord = 0
			parts = append(parts, txt)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return strings.TrimSpace(a.PreviewText)
}

func fmtOrderedPrefix(n int) string {
	if n <= 0 {
		return "1. "
	}
	return strconvItoa(n) + ". "
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ---------- Raw X Articles GraphQL JSON Structures ----------

type rawArticleMediaInfo struct {
	Typename          string          `json:"__typename"`
	OriginalImgURL    string          `json:"original_img_url"`
	OriginalImgWidth  json.RawMessage `json:"original_img_width"`
	OriginalImgHeight json.RawMessage `json:"original_img_height"`
}

type rawArticleMediaEntity struct {
	ID        string               `json:"id"`
	MediaID   json.RawMessage      `json:"media_id"`
	MediaKey  string               `json:"media_key"`
	MediaInfo *rawArticleMediaInfo `json:"media_info"`
}

type rawDraftEntityRange struct {
	Key    json.RawMessage `json:"key"`
	Offset int             `json:"offset"`
	Length int             `json:"length"`
}

type rawDraftBlock struct {
	Key          string                `json:"key"`
	Text         string                `json:"text"`
	Type         string                `json:"type"`
	EntityRanges []rawDraftEntityRange `json:"entityRanges"`
}

type rawDraftContentState struct {
	Blocks    []rawDraftBlock `json:"blocks"`
	EntityMap json.RawMessage `json:"entityMap"`
}

type rawDraftEntity struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type rawArticleResult struct {
	ID               string                  `json:"id"`
	RestID           string                  `json:"rest_id"`
	Title            string                  `json:"title"`
	PreviewText      string                  `json:"preview_text"`
	PlainText        string                  `json:"plain_text"`
	CoverMedia       *rawArticleMediaEntity  `json:"cover_media"`
	CoverMediaImgURL string                  `json:"cover_media_img_url"`
	MediaEntities    []rawArticleMediaEntity `json:"media_entities"`
	ContentState     *rawDraftContentState   `json:"content_state"`
}

type rawArticleWrap struct {
	ArticleResults *struct {
		Result *rawArticleResult `json:"result"`
	} `json:"article_results"`
}

// parseArticle extracts rich structured content from tweet.article.article_results.result.
// Returns nil when the payload only carries a stub title/preview without full rich
// content_state blocks or plain_text (so legacy degraded CardKindArticle fallback applies).
func parseArticle(wrap *rawArticleWrap, canonicalPostURL string, allowLocalhost bool) *Article {
	if wrap == nil || wrap.ArticleResults == nil || wrap.ArticleResults.Result == nil {
		return nil
	}
	ar := wrap.ArticleResults.Result
	title := strings.TrimSpace(html.UnescapeString(ar.Title))
	preview := strings.TrimSpace(html.UnescapeString(ar.PreviewText))
	plainText := normalizeArticlePlainText(ar.PlainText)

	// Index media_entities by media_id and media_key for atomic block resolution.
	mediaByID := make(map[string]ArticleImage, len(ar.MediaEntities))
	mediaByKey := make(map[string]ArticleImage, len(ar.MediaEntities))
	orderedMediaEntities := make([]ArticleImage, 0, len(ar.MediaEntities))
	for _, me := range ar.MediaEntities {
		img := buildArticleImage(&me, allowLocalhost)
		if img == nil {
			continue
		}
		orderedMediaEntities = append(orderedMediaEntities, *img)
		if img.MediaID != "" {
			mediaByID[img.MediaID] = *img
		}
		if img.MediaKey != "" {
			mediaByKey[img.MediaKey] = *img
		}
	}

	var cover *ArticleImage
	if ar.CoverMedia != nil {
		cover = buildArticleImage(ar.CoverMedia, allowLocalhost)
	}
	if cover == nil && strings.TrimSpace(ar.CoverMediaImgURL) != "" {
		cover = buildArticleImageFromURL(ar.CoverMediaImgURL, "", "", 0, 0, allowLocalhost)
	}

	var blocks []ArticleBlock
	var inlineImages []ArticleImage
	usedInlineMediaID := make(map[string]bool)

	if ar.ContentState != nil && len(ar.ContentState.Blocks) > 0 {
		entityByKey := parseDraftEntityMap(ar.ContentState.EntityMap)

		for _, rb := range ar.ContentState.Blocks {
			bType := normalizeDraftBlockType(rb.Type)
			if bType == "" {
				continue
			}

			if bType == ArticleBlockAtomic {
				emitted := parseAtomicDraftBlock(rb, entityByKey, mediaByID, mediaByKey, &inlineImages, usedInlineMediaID)
				blocks = append(blocks, emitted...)
				continue
			}

			rawText := rb.Text
			cleanText := strings.TrimSpace(strings.ReplaceAll(rawText, "\r\n", "\n"))
			links, tweetID := extractDraftTextEntities(rawText, rb.EntityRanges, entityByKey)
			if cleanText == "" && len(links) == 0 && tweetID == "" {
				continue
			}
			blocks = append(blocks, ArticleBlock{
				Key:     strings.TrimSpace(rb.Key),
				Type:    bType,
				Text:    cleanText,
				Links:   links,
				TweetID: tweetID,
			})
		}
	}

	// If content_state had no blocks but plain_text is present, split plain_text into
	// clean paragraph blocks and append any media_entities in order.
	if len(blocks) == 0 && plainText != "" {
		for _, para := range splitArticleParagraphs(plainText) {
			blocks = append(blocks, ArticleBlock{
				Type: ArticleBlockParagraph,
				Text: para,
			})
		}
		for _, img := range orderedMediaEntities {
			cp := img
			inlineImages = append(inlineImages, cp)
			blocks = append(blocks, ArticleBlock{
				Type:  ArticleBlockAtomic,
				Image: &cp,
			})
		}
	}

	if len(blocks) == 0 && plainText == "" {
		return nil
	}

	artURL := SafePublicWebURL(canonicalPostURL)
	restID := strings.TrimSpace(ar.RestID)
	if artURL == "" && restID != "" && reArticleID.MatchString(restID) {
		artURL = "https://x.com/i/article/" + restID
	}

	art := &Article{
		ID:           strings.TrimSpace(ar.ID),
		RestID:       restID,
		Title:        title,
		PreviewText:  preview,
		PlainText:    plainText,
		Cover:        cover,
		Blocks:       blocks,
		Images:       inlineImages,
		CanonicalURL: artURL,
	}
	if art.PlainText == "" {
		art.PlainText = art.FullPlainText()
	}
	return art
}

func buildArticleImage(me *rawArticleMediaEntity, allowLocalhost bool) *ArticleImage {
	if me == nil || me.MediaInfo == nil {
		return nil
	}
	rawURL := strings.TrimSpace(me.MediaInfo.OriginalImgURL)
	if rawURL == "" {
		return nil
	}
	mediaID := parseJSONScalarString(me.MediaID)
	if mediaID == "" {
		mediaID = strings.TrimSpace(me.ID)
	}
	w := int(parseJSONInt64(me.MediaInfo.OriginalImgWidth))
	h := int(parseJSONInt64(me.MediaInfo.OriginalImgHeight))
	return buildArticleImageFromURL(rawURL, mediaID, strings.TrimSpace(me.MediaKey), w, h, allowLocalhost)
}

func buildArticleImageFromURL(rawURL, mediaID, mediaKey string, width, height int, allowLocalhost bool) *ArticleImage {
	normRaw := normalizeXMediaURL(rawURL, allowLocalhost)
	if normRaw == "" {
		return nil
	}
	origURL, _ := buildPhotoURLs(normRaw, allowLocalhost)
	if origURL == "" {
		origURL = normRaw
	}
	fallback := ""
	if normRaw != origURL {
		fallback = normRaw
	}
	return &ArticleImage{
		MediaID:          strings.TrimSpace(mediaID),
		MediaKey:         strings.TrimSpace(mediaKey),
		Width:            width,
		Height:           height,
		PublicPreviewURL: SafePublicImageURL(origURL),
		ImageURL:         origURL,
		FallbackURL:      fallback,
	}
}

// parseDraftEntityMap parses Draft.js entityMap in BOTH observed formats (DEC-093):
//   - JSON array:  [{"key":"0","value":{"type":"MEDIA","data":{...}}}]
//   - JSON object: {"0":{"type":"MEDIA","data":{...}}} (or {"0":{"value":{...}}})
func parseDraftEntityMap(raw json.RawMessage) map[string]rawDraftEntity {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	out := make(map[string]rawDraftEntity)
	switch trimmed[0] {
	case '[':
		var entries []struct {
			Key   json.RawMessage `json:"key"`
			Value *rawDraftEntity `json:"value"`
			Type  string          `json:"type"`
			Data  json.RawMessage `json:"data"`
		}
		if err := decodeJSONUseNumber(trimmed, &entries); err != nil {
			return nil
		}
		for i, ent := range entries {
			k := parseJSONScalarString(ent.Key)
			if k == "" {
				k = strconvItoa(i)
			}
			if ent.Value != nil {
				out[k] = *ent.Value
			} else if strings.TrimSpace(ent.Type) != "" || hasNonEmptyRawJSON(ent.Data) {
				out[k] = rawDraftEntity{Type: ent.Type, Data: ent.Data}
			}
		}

	case '{':
		var obj map[string]struct {
			Value *rawDraftEntity `json:"value"`
			Type  string          `json:"type"`
			Data  json.RawMessage `json:"data"`
		}
		if err := decodeJSONUseNumber(trimmed, &obj); err != nil {
			return nil
		}
		for k, ent := range obj {
			if ent.Value != nil {
				out[strings.TrimSpace(k)] = *ent.Value
			} else {
				out[strings.TrimSpace(k)] = rawDraftEntity{Type: ent.Type, Data: ent.Data}
			}
		}
	}
	return out
}

func normalizeDraftBlockType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "unstyled", "paragraph":
		return ArticleBlockParagraph
	case "header-one":
		return ArticleBlockHeaderOne
	case "header-two", "header-three", "header-four", "header-five", "header-six":
		return ArticleBlockHeaderTwo
	case "blockquote", "code-block":
		return ArticleBlockBlockquote
	case "ordered-list-item":
		return ArticleBlockOrderedList
	case "unordered-list-item":
		return ArticleBlockUnorderedList
	case "atomic":
		return ArticleBlockAtomic
	default:
		return ArticleBlockParagraph
	}
}

func parseAtomicDraftBlock(
	rb rawDraftBlock,
	entityByKey map[string]rawDraftEntity,
	mediaByID map[string]ArticleImage,
	mediaByKey map[string]ArticleImage,
	inlineImages *[]ArticleImage,
	usedInlineMediaID map[string]bool,
) []ArticleBlock {
	var out []ArticleBlock
	seenEntityKey := make(map[string]bool, len(rb.EntityRanges))

	for _, er := range rb.EntityRanges {
		k := parseJSONScalarString(er.Key)
		if k == "" || seenEntityKey[k] {
			continue
		}
		seenEntityKey[k] = true
		ent, ok := entityByKey[k]
		if !ok {
			continue
		}
		entType := strings.ToUpper(strings.TrimSpace(ent.Type))

		var data struct {
			MediaItems []struct {
				MediaID      json.RawMessage `json:"mediaId"`
				MediaIDSnake json.RawMessage `json:"media_id"`
				LocalMediaID json.RawMessage `json:"localMediaId"`
				MediaKey     string          `json:"mediaKey"`
				MediaKeySnk  string          `json:"media_key"`
			} `json:"mediaItems"`
			MediaID  json.RawMessage `json:"mediaId"`
			MediaKey string          `json:"mediaKey"`
			Caption  string          `json:"caption"`
			TweetID  json.RawMessage `json:"tweetId"`
			TweetSnk json.RawMessage `json:"tweet_id"`
			ID       json.RawMessage `json:"id"`
			URL      string          `json:"url"`
			Href     string          `json:"href"`
			Title    string          `json:"title"`
			Markdown string          `json:"markdown"`
		}
		if hasNonEmptyRawJSON(ent.Data) {
			_ = decodeJSONUseNumber(ent.Data, &data)
		}

		// 1. Embedded media items (MEDIA / IMAGE / PHOTO / generic mediaItems).
		if entType == "MEDIA" || entType == "IMAGE" || entType == "PHOTO" || len(data.MediaItems) > 0 {
			caption := strings.TrimSpace(data.Caption)
			for _, mi := range data.MediaItems {
				mid := firstNonEmpty(
					parseJSONScalarString(mi.MediaID),
					parseJSONScalarString(mi.MediaIDSnake),
					parseJSONScalarString(mi.LocalMediaID),
				)
				mkey := firstNonEmpty(strings.TrimSpace(mi.MediaKey), strings.TrimSpace(mi.MediaKeySnk))
				if img, found := lookupArticleMedia(mid, mkey, mediaByID, mediaByKey); found {
					cp := img
					if caption != "" && cp.AltText == "" {
						cp.AltText = caption
					}
					*inlineImages = append(*inlineImages, cp)
					if cp.MediaID != "" {
						usedInlineMediaID[cp.MediaID] = true
					}
					out = append(out, ArticleBlock{
						Key:   strings.TrimSpace(rb.Key),
						Type:  ArticleBlockAtomic,
						Text:  caption,
						Image: &cp,
					})
				}
			}
			if len(data.MediaItems) == 0 {
				mid := parseJSONScalarString(data.MediaID)
				mkey := strings.TrimSpace(data.MediaKey)
				if img, found := lookupArticleMedia(mid, mkey, mediaByID, mediaByKey); found {
					cp := img
					if caption != "" && cp.AltText == "" {
						cp.AltText = caption
					}
					*inlineImages = append(*inlineImages, cp)
					if cp.MediaID != "" {
						usedInlineMediaID[cp.MediaID] = true
					}
					out = append(out, ArticleBlock{
						Key:   strings.TrimSpace(rb.Key),
						Type:  ArticleBlockAtomic,
						Text:  caption,
						Image: &cp,
					})
				}
			}
			continue
		}

		// 2. Embedded tweet reference (TWEET entity).
		if entType == "TWEET" {
			rawTID := firstNonEmpty(
				parseJSONScalarString(data.TweetID),
				parseJSONScalarString(data.TweetSnk),
				parseJSONScalarString(data.ID),
			)
			if tid := normalizeArticleTweetID(rawTID); tid != "" {
				out = append(out, ArticleBlock{
					Key:     strings.TrimSpace(rb.Key),
					Type:    ArticleBlockAtomic,
					TweetID: tid,
				})
			}
			continue
		}

		// 3. Markdown / Code block atomic entity.
		if md := strings.TrimSpace(data.Markdown); md != "" {
			out = append(out, ArticleBlock{
				Key:  strings.TrimSpace(rb.Key),
				Type: ArticleBlockBlockquote,
				Text: md,
			})
			continue
		}

		// 4. Embedded safe external link.
		if safeU := SafePublicWebURL(firstNonEmpty(data.URL, data.Href)); safeU != "" {
			lbl := strings.TrimSpace(data.Title)
			if lbl == "" {
				lbl = safeU
			}
			out = append(out, ArticleBlock{
				Key:   strings.TrimSpace(rb.Key),
				Type:  ArticleBlockParagraph,
				Text:  lbl,
				Links: []ArticleLink{{Label: lbl, URL: safeU}},
			})
		}
	}

	return out
}

func lookupArticleMedia(
	mediaID, mediaKey string,
	mediaByID map[string]ArticleImage,
	mediaByKey map[string]ArticleImage,
) (ArticleImage, bool) {
	if mediaID != "" {
		if img, ok := mediaByID[mediaID]; ok {
			return img, true
		}
	}
	if mediaKey != "" {
		if img, ok := mediaByKey[mediaKey]; ok {
			return img, true
		}
	}
	return ArticleImage{}, false
}

func extractDraftTextEntities(
	rawText string,
	ranges []rawDraftEntityRange,
	entityByKey map[string]rawDraftEntity,
) ([]ArticleLink, string) {
	if len(ranges) == 0 || len(entityByKey) == 0 {
		return nil, ""
	}
	var links []ArticleLink
	seenURL := make(map[string]bool)
	var tweetID string

	for _, er := range ranges {
		k := parseJSONScalarString(er.Key)
		if k == "" {
			continue
		}
		ent, ok := entityByKey[k]
		if !ok {
			continue
		}
		entType := strings.ToUpper(strings.TrimSpace(ent.Type))
		var data struct {
			URL      string          `json:"url"`
			Href     string          `json:"href"`
			Title    string          `json:"title"`
			TweetID  json.RawMessage `json:"tweetId"`
			TweetSnk json.RawMessage `json:"tweet_id"`
		}
		if hasNonEmptyRawJSON(ent.Data) {
			_ = decodeJSONUseNumber(ent.Data, &data)
		}

		switch entType {
		case "LINK":
			safeU := SafePublicWebURL(firstNonEmpty(data.URL, data.Href))
			if safeU == "" || seenURL[safeU] {
				continue
			}
			seenURL[safeU] = true
			label := strings.TrimSpace(sliceUTF16Range(rawText, er.Offset, er.Length))
			if label == "" {
				label = strings.TrimSpace(data.Title)
			}
			if label == "" {
				label = safeU
			}
			links = append(links, ArticleLink{Label: label, URL: safeU})

		case "TWEET":
			if tweetID == "" {
				rawTID := firstNonEmpty(parseJSONScalarString(data.TweetID), parseJSONScalarString(data.TweetSnk))
				tweetID = normalizeArticleTweetID(rawTID)
			}
		}
	}
	return links, tweetID
}

func sliceUTF16Range(s string, offset, length int) string {
	if s == "" || offset < 0 || length <= 0 {
		return ""
	}
	encoded := utf16.Encode([]rune(s))
	if offset >= len(encoded) {
		return ""
	}
	end := offset + length
	if end > len(encoded) {
		end = len(encoded)
	}
	return string(utf16.Decode(encoded[offset:end]))
}

func parseJSONScalarString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return strings.TrimSpace(s)
		}
	}
	var num json.Number
	if err := decodeJSONUseNumber(trimmed, &num); err == nil {
		return strings.TrimSpace(num.String())
	}
	return ""
}

func normalizeArticleTweetID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if sf, err := ParseSnowflakeID(s); err == nil {
		return sf.ID
	}
	if len(s) >= 1 && len(s) <= 24 && isAllASCII(s) {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return ""
			}
		}
		return s
	}
	return ""
}

func isAllASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func normalizeArticlePlainText(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(html.UnescapeString(s))
}

func splitArticleParagraphs(plain string) []string {
	rawParas := strings.Split(plain, "\n\n")
	out := make([]string, 0, len(rawParas))
	for _, p := range rawParas {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
