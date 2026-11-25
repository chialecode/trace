package ui

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type richSpanKind uint8

const (
	richSpanPlain richSpanKind = iota
	richSpanLink
	richSpanHashtag
)

type richSpan struct {
	Kind  richSpanKind
	Text  string
	URL   string
	Label string
}

type richParagraph struct {
	Spans []richSpan
}

type richTextOptions struct {
	Label             string
	ClampKey          string
	MaxLines          int
	FontSize          float32
	LineHeight        float32
	TextColor         ui.Color
	HighlightHashtags bool
}

// ValidateSafeJumpURL validates an external jump URL strictly at the UI boundary:
// only clean http:// and https:// URLs with a non-empty host and no userinfo,
// whitespace, backslashes, or control characters are permitted (DEC-073).
func ValidateSafeJumpURL(rawURL string) (string, bool) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) || r == '\\' {
			return "", false
		}
	}
	u, err := url.ParseRequestURI(s)
	if err != nil || u.Host == "" || strings.TrimSpace(u.Hostname()) == "" || u.User != nil {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String(), true
}

// openExternalURL opens a validated HTTP(S) hyperlink via the backend Desktop
// implementation (recording in MockDesktop during headless UI tests without
// launching a real browser). Unsafe schemes are rejected before reaching Desktop.
func (a *App) openExternalURL(rawURL string) {
	cleanURL, ok := ValidateSafeJumpURL(rawURL)
	if !ok {
		a.showError(errors.New("仅支持打开 http 或 https 网页链接"))
		return
	}
	if err := a.backend.Desktop().OpenExternalURL(cleanURL); err != nil {
		a.showError(err)
	}
}

// FormatConciseLinkLabel builds a concise, human-readable label for a bare
// HTTP(S) URL so post bodies never sprawl raw URL addresses across the layout.
func FormatConciseLinkLabel(rawURL string) string {
	cleanURL, ok := ValidateSafeJumpURL(rawURL)
	if !ok {
		return "网页链接"
	}
	u, err := url.Parse(cleanURL)
	if err != nil || u.Host == "" {
		return "网页链接"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch host {
	case "t.cn", "t.co":
		return "网页链接"
	case "s.weibo.com":
		if q := strings.TrimSpace(u.Query().Get("q")); strings.HasPrefix(q, "#") && strings.HasSuffix(q, "#") && utf8.RuneCountInString(q) <= 32 {
			return q
		}
	case "card.weibo.com":
		if strings.HasPrefix(strings.ToLower(u.Path), "/article/") {
			return "头条文章"
		}
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

	cleanPath := strings.TrimRight(u.Path, "/")
	if decoded, dErr := url.PathUnescape(cleanPath); dErr == nil && utf8.ValidString(decoded) && !hasControlOrSpace(decoded) {
		cleanPath = decoded
	}
	if cleanPath == "" {
		return host
	}
	cand := host + cleanPath
	if utf8.RuneCountInString(cand) > 30 {
		runes := []rune(cand)
		return string(runes[:29]) + "…"
	}
	return cand
}

func hasControlOrSpace(s string) bool {
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// parseRichTextParagraphs tokenizes multi-paragraph post text into plain, hashtag,
// and clickable hyperlink spans, matching:
//  1. Structured links (e.g. Weibo <a href> / url_struct labels, X display_url entities)
//  2. Legacy cached "标题 (https://...)" / "标题（https://...）" patterns
//  3. Inline http:// and https:// URLs across all sites and already-cached items
func parseRichTextParagraphs(text string, links []service.TextLinkDTO, highlightHashtags bool) ([]richParagraph, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}

	// Sanitize incoming structured links once and build both sequential label list
	// and exact URL -> preferred display label lookup (for X expanded URLs in text).
	validLinks := make([]service.TextLinkDTO, 0, len(links))
	urlToLabel := make(map[string]string, len(links))
	for _, l := range links {
		cleanU, ok := ValidateSafeJumpURL(l.URL)
		if !ok {
			continue
		}
		lbl := strings.TrimSpace(l.Label)
		if lbl == "" {
			lbl = FormatConciseLinkLabel(cleanU)
		}
		validLinks = append(validLinks, service.TextLinkDTO{Label: lbl, URL: cleanU})
		if _, exists := urlToLabel[cleanU]; !exists {
			urlToLabel[cleanU] = lbl
		}
	}

	usedLinks := make([]bool, len(validLinks))
	rawLines := strings.Split(trimmed, "\n")
	paragraphs := make([]richParagraph, 0, len(rawLines))
	hasRich := false

	for _, rawLine := range rawLines {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			paragraphs = append(paragraphs, richParagraph{})
			continue
		}
		spans := tokenizeRichLine(line, validLinks, usedLinks, urlToLabel, highlightHashtags)
		for _, sp := range spans {
			if sp.Kind != richSpanPlain {
				hasRich = true
				break
			}
		}
		paragraphs = append(paragraphs, richParagraph{Spans: spans})
	}
	return paragraphs, hasRich
}

type lineLinkMatch struct {
	Start    int
	End      int
	Label    string
	URL      string
	LinkSlot int // index in validLinks if matched from structured label, else -1
}

func tokenizeRichLine(
	line string,
	validLinks []service.TextLinkDTO,
	usedLinks []bool,
	urlToLabel map[string]string,
	highlightHashtags bool,
) []richSpan {
	var matches []lineLinkMatch

	// 1. Scan inline http:// and https:// URLs (and legacy "Label (https://...)" wrappers).
	pos := 0
	for pos < len(line) {
		httpIdx := indexHTTPPrefix(line[pos:])
		if httpIdx < 0 {
			break
		}
		urlStart := pos + httpIdx
		rawEnd := scanRawURLEnd(line, urlStart)
		candURL, cleanEnd := trimTrailingURLPunctuation(line, urlStart, rawEnd)
		cleanURL, ok := ValidateSafeJumpURL(candURL)
		if !ok {
			pos = urlStart + 4
			continue
		}

		matchStart := urlStart
		matchEnd := cleanEnd
		label := ""

		// Recognize legacy "标题 (https://...)" or "标题（https://...）" from older cached items.
		if legStart, legEnd, legLabel, legOK := matchLegacyParenthesizedLabel(line, urlStart, cleanEnd); legOK {
			matchStart = legStart
			matchEnd = legEnd
			label = legLabel
		}

		matchedSlot := -1
		for i, vl := range validLinks {
			if !usedLinks[i] && vl.URL == cleanURL {
				matchedSlot = i
				if label == "" {
					label = vl.Label
				}
				break
			}
		}
		if label == "" {
			if mapped, exists := urlToLabel[cleanURL]; exists && mapped != "" {
				label = mapped
			} else {
				label = FormatConciseLinkLabel(cleanURL)
			}
		}
		if matchedSlot >= 0 {
			usedLinks[matchedSlot] = true
		}
		matches = append(matches, lineLinkMatch{
			Start:    matchStart,
			End:      matchEnd,
			Label:    label,
			URL:      cleanURL,
			LinkSlot: matchedSlot,
		})
		pos = matchEnd
	}

	// 2. Match remaining structured links (e.g. Weibo "网页链接", article title, "#话题#", "@昵称")
	// sequentially left-to-right on unclaimed spans of this line.
	searchFrom := 0
	for i, vl := range validLinks {
		if usedLinks[i] || vl.Label == "" {
			continue
		}
		idx := findUnclaimedLineSpan(line, vl.Label, searchFrom, matches)
		if idx < 0 && searchFrom > 0 {
			idx = findUnclaimedLineSpan(line, vl.Label, 0, matches)
		}
		if idx < 0 {
			continue
		}
		usedLinks[i] = true
		end := idx + len(vl.Label)
		matches = append(matches, lineLinkMatch{
			Start:    idx,
			End:      end,
			Label:    vl.Label,
			URL:      vl.URL,
			LinkSlot: i,
		})
		searchFrom = end
	}

	if len(matches) == 0 {
		return buildPlainOrHashtagSpans(line, highlightHashtags)
	}

	// Sort matches by byte offset.
	for i := 1; i < len(matches); i++ {
		cur := matches[i]
		j := i - 1
		for j >= 0 && matches[j].Start > cur.Start {
			matches[j+1] = matches[j]
			j--
		}
		matches[j+1] = cur
	}

	var out []richSpan
	cursor := 0
	for _, m := range matches {
		if m.Start < cursor {
			continue
		}
		if m.Start > cursor {
			out = append(out, buildPlainOrHashtagSpans(line[cursor:m.Start], highlightHashtags)...)
		}
		out = append(out, richSpan{
			Kind:  richSpanLink,
			Text:  m.Label,
			Label: m.Label,
			URL:   m.URL,
		})
		cursor = m.End
	}
	if cursor < len(line) {
		out = append(out, buildPlainOrHashtagSpans(line[cursor:], highlightHashtags)...)
	}
	return out
}

func buildPlainOrHashtagSpans(segment string, highlightHashtags bool) []richSpan {
	if segment == "" {
		return nil
	}
	if !highlightHashtags {
		return []richSpan{{Kind: richSpanPlain, Text: segment}}
	}
	rawSpans := splitHashtagSpans(segment)
	out := make([]richSpan, 0, len(rawSpans))
	for _, s := range rawSpans {
		if s.Hashtag {
			out = append(out, richSpan{Kind: richSpanHashtag, Text: s.Text})
		} else if s.Text != "" {
			out = append(out, richSpan{Kind: richSpanPlain, Text: s.Text})
		}
	}
	return out
}

func indexHTTPPrefix(s string) int {
	best := -1
	for _, pfx := range []string{"https://", "http://", "HTTPS://", "HTTP://"} {
		if idx := strings.Index(s, pfx); idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	if best >= 0 {
		return best
	}
	// Fallback case-insensitive check only if mixed-case "Http://" exists.
	lower := strings.ToLower(s)
	p1 := strings.Index(lower, "https://")
	p2 := strings.Index(lower, "http://")
	switch {
	case p1 >= 0 && p2 >= 0:
		return min(p1, p2)
	case p1 >= 0:
		return p1
	default:
		return p2
	}
}

func scanRawURLEnd(s string, start int) int {
	i := start
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) {
			break
		}
		// Stop before quotes, angle brackets, backslashes, or non-ASCII CJK text/fullwidth punctuation.
		if r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == '\\' || r > 0x7e {
			break
		}
		i += w
	}
	return i
}

func trimTrailingURLPunctuation(s string, start, end int) (string, int) {
	openParens := 0
	openBrackets := 0
	for i := start; i < end; i++ {
		switch s[i] {
		case '(':
			openParens++
		case ')':
			openParens--
		case '[':
			openBrackets++
		case ']':
			openBrackets--
		}
	}
	cur := end
	for cur > start {
		last := s[cur-1]
		switch last {
		case '.', ',', ';', ':', '!', '?':
			cur--
		case ')':
			if openParens < 0 {
				openParens++
				cur--
			} else {
				return s[start:cur], cur
			}
		case ']':
			if openBrackets < 0 {
				openBrackets++
				cur--
			} else {
				return s[start:cur], cur
			}
		case '}':
			cur--
		default:
			return s[start:cur], cur
		}
	}
	return s[start:cur], cur
}

// matchLegacyParenthesizedLabel detects "标题 (https://...)" or "标题（https://...）"
// so already-cached Weibo items display clickable "标题" without raw parenthesized URLs.
func matchLegacyParenthesizedLabel(line string, urlStart, urlEnd int) (start, end int, label string, ok bool) {
	var closeParen string
	openPos := -1
	switch {
	case urlStart >= 1 && line[urlStart-1] == '(' && urlEnd < len(line) && line[urlEnd] == ')':
		openPos = urlStart - 1
		closeParen = ")"
	case urlStart >= len("（") && strings.HasSuffix(line[:urlStart], "（") && strings.HasPrefix(line[urlEnd:], "）"):
		openPos = urlStart - len("（")
		closeParen = "）"
	default:
		return 0, 0, "", false
	}

	// Optional single space before '(' (as produced by legacy "Title (https://...)").
	labelEnd := openPos
	hasSpaceBeforeParen := false
	if labelEnd > 0 && line[labelEnd-1] == ' ' {
		hasSpaceBeforeParen = true
		labelEnd--
	}
	if !hasSpaceBeforeParen && closeParen == ")" {
		// Require space before ASCII '(' so normal prose "说明(https://...)" doesn't swallow whole clauses.
		return 0, 0, "", false
	}
	if labelEnd <= 0 {
		return 0, 0, "", false
	}

	// Scan backwards to find the token boundary (whitespace or sentence punctuation).
	labelStart := labelEnd
	runeCount := 0
	for labelStart > 0 {
		r, w := utf8.DecodeLastRuneInString(line[:labelStart])
		if unicode.IsSpace(r) || isLegacyLabelBoundaryRune(r) {
			break
		}
		labelStart -= w
		runeCount++
		if runeCount > 32 {
			return 0, 0, "", false
		}
	}
	candLabel := strings.TrimSpace(line[labelStart:labelEnd])
	if candLabel == "" || strings.Contains(candLabel, "://") {
		return 0, 0, "", false
	}
	return labelStart, urlEnd + len(closeParen), candLabel, true
}

func isLegacyLabelBoundaryRune(r rune) bool {
	switch r {
	case '，', '。', '：', '；', '！', '？', '、', '（', '）', '(', ')', '【', '】', '《', '》', '"', '\'', ',', ';', ':':
		return true
	default:
		return false
	}
}

func findUnclaimedLineSpan(line, label string, from int, claimed []lineLinkMatch) int {
	if label == "" || from < 0 || from >= len(line) || len(label) > len(line) {
		return -1
	}
	cur := from
	for cur <= len(line)-len(label) {
		rel := strings.Index(line[cur:], label)
		if rel < 0 {
			return -1
		}
		idx := cur + rel
		end := idx + len(label)
		overlaps := false
		for _, m := range claimed {
			if idx < m.End && end > m.Start {
				overlaps = true
				break
			}
		}
		if !overlaps {
			return idx
		}
		cur = idx + 1
	}
	return -1
}

// clampRichParagraphs truncates tokenized paragraphs by visible line count and
// visible rune count without ever splitting a hyperlink URL in half.
func clampRichParagraphs(paras []richParagraph, maxParas, maxRunes int) ([]richParagraph, bool) {
	totalNonEmpty := 0
	totalRunes := 0
	for _, p := range paras {
		if len(p.Spans) == 0 {
			continue
		}
		totalNonEmpty++
		for _, sp := range p.Spans {
			totalRunes += utf8.RuneCountInString(sp.Text)
		}
	}
	needsClamp := (maxParas > 0 && totalNonEmpty > maxParas) || (maxRunes > 0 && totalRunes > maxRunes)
	if !needsClamp {
		return paras, false
	}

	var out []richParagraph
	seenParas := 0
	remRunes := maxRunes
	if remRunes <= 0 {
		remRunes = 1 << 30
	}

	for _, p := range paras {
		if len(p.Spans) == 0 {
			if len(out) > 0 && seenParas < maxParas {
				out = append(out, p)
			}
			continue
		}
		if maxParas > 0 && seenParas >= maxParas {
			appendEllipsisToParagraphs(&out)
			return out, true
		}
		seenParas++

		var clampedSpans []richSpan
		for _, sp := range p.Spans {
			n := utf8.RuneCountInString(sp.Text)
			if n <= remRunes {
				clampedSpans = append(clampedSpans, sp)
				remRunes -= n
				continue
			}
			if sp.Kind == richSpanLink {
				// Keep an in-progress link intact if there is room for at least half its label,
				// otherwise stop cleanly before it rather than corrupting its label/URL.
				if remRunes >= 4 {
					clampedSpans = append(clampedSpans, sp)
				}
			} else if remRunes > 0 {
				r := []rune(sp.Text)
				sp.Text = string(r[:remRunes]) + "…"
				clampedSpans = append(clampedSpans, sp)
			}
			remRunes = 0
			break
		}
		if len(clampedSpans) > 0 {
			out = append(out, richParagraph{Spans: clampedSpans})
		}
		if remRunes <= 0 {
			appendEllipsisToParagraphs(&out)
			return out, true
		}
	}
	return out, true
}

func appendEllipsisToParagraphs(paras *[]richParagraph) {
	if len(*paras) == 0 {
		return
	}
	last := &(*paras)[len(*paras)-1]
	if len(last.Spans) == 0 {
		return
	}
	tail := &last.Spans[len(last.Spans)-1]
	if tail.Kind == richSpanPlain && !strings.HasSuffix(tail.Text, "…") {
		tail.Text += "…"
	} else if tail.Kind != richSpanPlain {
		last.Spans = append(last.Spans, richSpan{Kind: richSpanPlain, Text: "…"})
	}
}

func splitPlainIntoClauses(s string) []string {
	if utf8.RuneCountInString(s) <= 18 {
		return []string{s}
	}
	var out []string
	var buf strings.Builder
	runeLen := 0
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
			runeLen = 0
		}
	}
	for _, r := range s {
		buf.WriteRune(r)
		runeLen++
		switch r {
		case '，', '。', '、', '；', '：', '！', '？', '）':
			flush()
		case ',', ';', '!', '?':
			if runeLen >= 8 {
				flush()
			}
		default:
			if r == ' ' && runeLen >= 16 {
				flush()
			}
		}
	}
	flush()
	return out
}

func shouldShowInlineLinkIcon(label string) bool {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "#") {
		return false
	}
	return true
}

// renderRichText renders post body text with clickable accent hyperlinks (and optional
// Douyin #hashtag highlights), returning true if a hyperlink was clicked in this frame.
func (a *App) renderRichText(c *ui.Context, text string, links []service.TextLinkDTO, opts richTextOptions) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	fontSize := opts.FontSize
	if fontSize <= 0 {
		fontSize = 15
	}
	lineHeight := opts.LineHeight
	if lineHeight <= 0 {
		lineHeight = LineHeightBody
	}
	textColor := opts.TextColor
	if textColor == (ui.Color{}) {
		textColor = ColorTextMain
	}

	paragraphs, hasRich := parseRichTextParagraphs(text, links, opts.HighlightHashtags)
	if !hasRich {
		return a.renderPlainFastPath(c, text, fontSize, lineHeight, textColor, opts)
	}

	if a.ExpandedPosts == nil && opts.ClampKey != "" {
		a.ExpandedPosts = make(map[string]bool)
	}
	expanded := opts.ClampKey != "" && a.ExpandedPosts[opts.ClampKey]

	shownParas := paragraphs
	needsClamp := false
	if opts.ClampKey != "" {
		var clamped []richParagraph
		clamped, needsClamp = clampRichParagraphs(paragraphs, 6, 280)
		if needsClamp && !expanded {
			shownParas = clamped
		}
	} else if opts.MaxLines > 0 {
		shownParas, _ = clampRichParagraphs(paragraphs, opts.MaxLines, opts.MaxLines*42)
	}

	linkClicked := false
	labelCounts := make(map[string]int)

	col := ui.Column(c).FillWidth().Gap(4).ClipX()
	if opts.Label != "" {
		col.Label(opts.Label)
	}
	col.Children(func() {
		for _, para := range shownParas {
			if len(para.Spans) == 0 {
				continue
			}
			paraHasRich := false
			for _, sp := range para.Spans {
				if sp.Kind != richSpanPlain {
					paraHasRich = true
					break
				}
			}
			if !paraHasRich && len(para.Spans) == 1 {
				ui.Text(c, strings.TrimSpace(para.Spans[0].Text)).
					FontSize(fontSize).
					LineHeight(lineHeight).
					TextColor(textColor)
				continue
			}

			ui.Row(c).FillWidth().Wrap().Gap(0).AlignItems(ui.Center).ClipX().Children(func() {
				for _, sp := range para.Spans {
					span := sp
					switch span.Kind {
					case richSpanHashtag:
						ui.Text(c, span.Text).
							FontSize(fontSize).
							LineHeight(lineHeight).
							FontWeight(600).
							TextColor(ColorAccent)
					case richSpanLink:
						labelCounts[span.Label]++
						accLabel := "打开链接：" + span.Label
						if labelCounts[span.Label] > 1 {
							accLabel = fmt.Sprintf("打开链接：%s (%d)", span.Label, labelCounts[span.Label])
						}
						if a.renderInlineHyperlink(c, accLabel, span.Label, span.URL, fontSize) {
							linkClicked = true
						}
					default:
						for _, clause := range splitPlainIntoClauses(span.Text) {
							ui.Text(c, clause).
								FontSize(fontSize).
								LineHeight(lineHeight).
								TextColor(textColor)
						}
					}
				}
			})
		}

		if needsClamp && opts.ClampKey != "" {
			toggleText := "展开"
			if expanded {
				toggleText = "收起"
			}
			btn := ui.ButtonBase(c).Label("正文展开收起："+opts.ClampKey).Padding(2, 0).Children(func() {
				ui.Text(c, toggleText).FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
			if btn.Clicked() {
				a.ExpandedPosts[opts.ClampKey] = !expanded
				linkClicked = true
			}
		}
	})
	return linkClicked
}

func (a *App) renderPlainFastPath(
	c *ui.Context,
	text string,
	fontSize, lineHeight float32,
	textColor ui.Color,
	opts richTextOptions,
) bool {
	if opts.ClampKey == "" {
		renderPlain := func() {
			t := ui.Text(c, text).FontSize(fontSize).LineHeight(lineHeight).TextColor(textColor)
			if opts.MaxLines > 0 {
				t.MaxLines(opts.MaxLines)
			}
		}
		if opts.Label != "" {
			ui.Box(c).Label(opts.Label).FillWidth().Children(renderPlain)
		} else {
			renderPlain()
		}
		return false
	}

	if a.ExpandedPosts == nil {
		a.ExpandedPosts = make(map[string]bool)
	}
	lines := strings.Count(text, "\n") + 1
	needsClamp := lines > 6 || utf8.RuneCountInString(text) > 280
	expanded := a.ExpandedPosts[opts.ClampKey]

	col := ui.Column(c).FillWidth().Gap(4)
	if opts.Label != "" {
		col.Label(opts.Label)
	}
	toggled := false
	col.Children(func() {
		t := ui.Text(c, text).FontSize(fontSize).LineHeight(lineHeight).TextColor(textColor)
		if needsClamp && !expanded {
			t.MaxLines(6)
		}
		if needsClamp {
			toggleText := "展开"
			if expanded {
				toggleText = "收起"
			}
			btn := ui.ButtonBase(c).Label("正文展开收起："+opts.ClampKey).Padding(2, 0).Children(func() {
				ui.Text(c, toggleText).FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
			if btn.Clicked() {
				a.ExpandedPosts[opts.ClampKey] = !expanded
				toggled = true
			}
		}
	})
	return toggled
}

func (a *App) renderInlineHyperlink(c *ui.Context, accessLabel, displayText, targetURL string, fontSize float32) bool {
	showIcon := shouldShowInlineLinkIcon(displayText)
	btn := ui.ButtonBase(c).
		Label(accessLabel).
		Tooltip(targetURL).
		Padding(1, 3).
		Radius(5).
		AlignItems(ui.Center).
		Shrink(1)

	hovered := btn.Hovered()
	col := ColorAccent
	if hovered {
		col = ColorAccentHover
		btn.Background(ColorAccentBg)
	}

	btn.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if hovered && r.W > 6 {
			p.Fill(ui.Rect{
				X: r.X + 3,
				Y: r.Y + r.H - 2,
				W: r.W - 6,
				H: 1.2,
			}, col, 0.6)
		}
	})

	btn.Children(func() {
		if showIcon {
			ui.Row(c).Gap(3).AlignItems(ui.Center).Shrink(1).ClipX().Children(func() {
				DrawIcon(c, "link", max(12, fontSize*0.84), col, 1.8)
				ui.Text(c, displayText).
					FontSize(fontSize).
					FontWeight(500).
					TextColor(col).
					SingleLine().
					Shrink(1)
			})
		} else {
			ui.Text(c, displayText).
				FontSize(fontSize).
				FontWeight(500).
				TextColor(col).
				SingleLine().
				Shrink(1)
		}
	})

	if btn.Clicked() {
		a.openExternalURL(targetURL)
		return true
	}
	return false
}
