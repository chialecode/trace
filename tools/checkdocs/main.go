package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var (
	anchorTagRe        = regexp.MustCompile(`<a\s+(?:id|name)=["']([^"']+)["']`)
	headingRe          = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
	mdLinkRe           = regexp.MustCompile(`!?\[[^\]]*\]\(([^)]+)\)`)
	refHeadingRe       = regexp.MustCompile(`(?m)^###\s+([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)\s*$`)
	winAbsPathRe       = regexp.MustCompile(`(?i)\b[a-zA-Z]:\\[A-Za-z0-9_.-]+`)
	unixHomeRe         = regexp.MustCompile(`(?:/Users/[A-Za-z0-9_.-]+|/home/[a-zA-Z0-9._-]+/)`)
	cookieLeakRe       = regexp.MustCompile(`(?i)(?:\b(?:SESSDATA|bili_jct|DedeUserID__ckMd5|ac_time_value|sessionid|sessionid_ss|sid_guard|msToken|UIFID|UIFID_TEMP|ttwid|odin_tt|sid_tt|uid_tt|uid_tt_ss|__ac_signature)=[A-Za-z0-9%_][A-Za-z0-9%|._-]{5,}|\btrace-remote:(?:[0-9]+:)?[A-Za-z0-9_=-]{8,}|(?-i:\b(?:SUB|SUBP|XSRF-TOKEN|WBPSESS|SCF|ALF|SSOLoginState|SUHB|PC_TOKEN|_T_WM)=[A-Za-z0-9%_-][A-Za-z0-9%|._+/=-]{7,}))`)
	signedURLLeakRe    = regexp.MustCompile(`(?i)https?://[^\s"'<>` + "`" + `]*(?:x-(?:secsdk-web-)?signature/[A-Za-z0-9%+/=._-]{4,}|\?(?:[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%3f|%26))?(?:a_bogus=[A-Za-z0-9%+/=._-]{4,}|x-(?:secsdk-web-)?signature=[A-Za-z0-9%+/=._-]{4,}|ssig(?:=|%3d)[A-Za-z0-9%+/=._-]{4,}|expires(?:=|%3d)[0-9]{9,}[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%26)kid(?:=|%3d)[A-Za-z0-9%,._-]{3,}|kid(?:=|%3d)[A-Za-z0-9%,._-]{3,}[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%26)expires(?:=|%3d)[0-9]{9,}))`)
	signedQueryLeakRe  = regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `?&]|\\u0026|&amp;|%3f|%26)(?:ssig(?:=|%3d)[A-Za-z0-9%+/=._-]{4,}|expires(?:=|%3d)[0-9]{9,}[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%26)kid(?:=|%3d)[A-Za-z0-9%,._-]{3,}|kid(?:=|%3d)[A-Za-z0-9%,._-]{3,}[^\s"'<>` + "`" + `]*(?:&|\\u0026|&amp;|%26)expires(?:=|%3d)[0-9]{9,})`)
	signedURLPathSigRe = regexp.MustCompile(`(?i)(x-(?:secsdk-web-)?signature/)[^/\s?"'<>` + "`" + `]+`)

	// X Cookie, CSRF header, Bearer token, and signed/private media URL patterns (docs/research/x.md §15.1 #20).
	xSecretCookieRawRe  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)(auth_token|ct0|kdt)=(?:\\?["'])?([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})`)
	xSecretCookieEncRe  = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)(auth_token|ct0|kdt)%3d(?:%22|%27)?([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){7,})`)
	xSecretCookieJSONRe = regexp.MustCompile(`(?i)\\?"(auth_token|ct0|kdt)\\?"[ \t]*:[ \t]*\\?"([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})\\?"`)
	xCSRFHeaderRe       = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%20)x-csrf-token(?:[ \t]*:[ \t]*(?:\\?["'])?|:\[)([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFHeaderEncRe    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)x-csrf-token(?:%3a(?:%20|\+)*|:(?:%20)+)(?:%22|%27)?([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFKVRe           = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)x-csrf-token=(?:\\?["'])?([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFKVEncRe        = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)x-csrf-token%3d(?:%22|%27)?([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){7,})`)
	xCSRFJSONRe         = regexp.MustCompile(`(?i)\\?"x-csrf-token\\?"[ \t]*:[ \t]*\\?"([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){7,})\\?"`)
	xAuthBearerHeaderRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%20)authorization(?:[ \t]*:[ \t]*(?:\\?["'])?|:\[)bearer[ \t]+([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){11,})`)
	xAuthBearerJSONRe   = regexp.MustCompile(`(?i)\\?"authorization\\?"[ \t]*:[ \t]*\\?"bearer[ \t]+([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){11,})\\?"`)
	xAuthBearerEncRe    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt]|%3f|%26|%3b|%20|%3d|%3a|%22|%27|%2c|%0a|%0d|%09)authorization(?:%3a(?:%20|\+)*|:(?:%20)+)(?:%22|%27)?bearer(?:%20|\+)+([A-Za-z0-9](?:[A-Za-z0-9._-]|%2b|%2f|%3d|%7c){11,})`)
	xBareBearerRe       = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt])bearer[ \t]+([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){15,})`)
	xBearerJSONRe       = regexp.MustCompile(`(?i)\\?"bearer(?:_?token)?\\?"[ \t]*:[ \t]*\\?"([A-Za-z0-9](?:[A-Za-z0-9._+/=-]|%2b|%2f|%3d|%7c){15,})\\?"`)
	xRawWebBearerRe     = regexp.MustCompile(`(?:^|[^A-Za-z0-9_]|\\[nrt]|(?i:%20|%3a|%3d|%22|%27))(A{18,}(?:[A-Za-z0-9._+/=-]|(?i:%2b|%2f|%3d|%7c)){16,})`)
	xSignedMediaURLRe   = regexp.MustCompile(`(?i)https?(?:://|%3a%2f%2f)(?:[A-Za-z0-9-]+\.)*(?:twimg\.com|pscp\.tv|periscope\.tv)(?:/|%2f)[^\s"'<>` + "`" + `]*?(?:\?|&|\\u0026|&amp;|%3f|%26)(x-amz-signature|x-amz-security-token|x-amz-credential|signature|sig|hmac|hdnts|policy|key-pair-id|token|auth)(?:=|%3d)([A-Za-z0-9][A-Za-z0-9%+/=._~-]{5,})`)
	xPrivateTonURLRe    = regexp.MustCompile(`(?i)https?(?:://|%3a%2f%2f)(ton\.(?:x|twitter)\.com)(?:/|%2f)([^\s"'` + "`" + `]+)`)
)

var ignoredDirs = map[string]bool{
	".git":      true,
	".local":    true,
	".mygo":     true,
	"artifacts": true,
	"build":     true,
	"dist":      true,
	"tmp":       true,
	"vendor":    true,
}

func main() {
	rootFlag := flag.String("root", ".", "repository root directory")
	refDirFlag := flag.String("ref-dir", "", "optional reference directory override")
	flag.Parse()

	findings, msg, err := Run(*rootFlag, *refDirFlag)
	if msg != "" {
		fmt.Println(msg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkdocs error: %v\n", err)
		os.Exit(1)
	}
	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Fprintf(os.Stderr, "checkdocs: %s\n", f)
		}
		os.Exit(1)
	}
	fmt.Println("checkdocs: OK")
}

// Run checks Markdown links/anchors, local absolute paths, leaked Cookie/token strings,
// and reference project names across markdown documents in rootDir.
func Run(rootDir string, refDirOverride string) ([]string, string, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, "", err
	}

	refTerms, infoMsg := loadReferenceTerms(absRoot, refDirOverride)
	anchorCache := make(map[string]map[string]bool)
	var findings []string

	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != absRoot && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".md" {
			return nil
		}
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)

		// 1. Check local absolute paths
		if m := winAbsPathRe.FindString(text); m != "" {
			findings = append(findings, fmt.Sprintf("%s: contains local Windows absolute path %q", relPath, m))
		}
		if m := unixHomeRe.FindString(text); m != "" {
			findings = append(findings, fmt.Sprintf("%s: contains local Unix home path %q", relPath, m))
		}

		// 2. Check leaked Cookie/token values and signed URLs
		if m := cookieLeakRe.FindString(text); m != "" {
			findings = append(findings, fmt.Sprintf("%s: contains Cookie/token value %q", relPath, redactSecretMatch(m)))
		}
		for _, redacted := range findXSecretLeaks(text) {
			findings = append(findings, fmt.Sprintf("%s: contains Cookie/token value %q", relPath, redacted))
		}
		if m := signedURLLeakRe.FindString(text); m != "" {
			findings = append(findings, fmt.Sprintf("%s: contains signed URL %q", relPath, redactSignedURL(m)))
		} else if m := signedQueryLeakRe.FindString(text); m != "" {
			findings = append(findings, fmt.Sprintf("%s: contains signed URL %q", relPath, redactSignedQuery(m)))
		}
		for _, redacted := range findXSignedURLLeaks(text) {
			findings = append(findings, fmt.Sprintf("%s: contains signed URL %q", relPath, redacted))
		}

		// 3. Check reference project names (if reference index was found)
		lowerText := strings.ToLower(text)
		for _, term := range refTerms {
			if strings.Contains(lowerText, strings.ToLower(term)) {
				findings = append(findings, fmt.Sprintf("%s: contains reference project name %q", relPath, term))
			}
		}

		// 4. Check Markdown relative links and #anchors (outside fenced code blocks and inline code)
		stripped := stripCodeBlocksAndSpans(text)
		matches := mdLinkRe.FindAllStringSubmatch(stripped, -1)
		for _, m := range matches {
			target := strings.TrimSpace(m[1])
			if idx := strings.IndexAny(target, " \t"); idx > 0 {
				target = target[:idx]
			}
			target = strings.Trim(target, "<>")
			if target == "" || isExternalLink(target) {
				continue
			}

			linkPath, anchor, hasAnchor := strings.Cut(target, "#")
			var targetFile string
			if linkPath == "" {
				targetFile = path
			} else {
				targetFile = filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(linkPath)))
			}

			// Verify targetFile stays inside repository root
			relTarget, relErr := filepath.Rel(absRoot, targetFile)
			if relErr != nil || strings.HasPrefix(relTarget, "..") {
				findings = append(findings, fmt.Sprintf("%s: link %q escapes repository root", relPath, target))
				continue
			}

			st, statErr := os.Stat(targetFile)
			if statErr != nil {
				findings = append(findings, fmt.Sprintf("%s: broken link target %q", relPath, target))
				continue
			}

			if hasAnchor && anchor != "" && !st.IsDir() && strings.EqualFold(filepath.Ext(targetFile), ".md") {
				anchors, ok := anchorCache[targetFile]
				if !ok {
					anchors, err = collectMarkdownAnchors(targetFile)
					if err != nil {
						return err
					}
					anchorCache[targetFile] = anchors
				}
				if !anchors[strings.ToLower(anchor)] {
					findings = append(findings, fmt.Sprintf("%s: missing anchor #%s in %q", relPath, anchor, target))
				}
			}
		}
		return nil
	})
	return findings, infoMsg, err
}

func isExternalLink(target string) bool {
	lower := strings.ToLower(target)
	for _, prefix := range []string{"http://", "https://", "mailto:", "file://", "conversation://"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func redactSecretMatch(m string) string {
	if strings.HasPrefix(strings.ToLower(m), "trace-remote:") {
		return "trace-remote:<redacted>"
	}
	if k, _, ok := strings.Cut(m, "="); ok {
		return k + "=<redacted>"
	}
	return "<redacted>"
}

func redactSignedURL(m string) string {
	if base, _, ok := strings.Cut(m, "?"); ok {
		return signedURLPathSigRe.ReplaceAllString(base, "${1}<redacted>") + "?<redacted>"
	}
	if red := signedURLPathSigRe.ReplaceAllString(m, "${1}<redacted>"); red != m {
		return red
	}
	return "<redacted>"
}

func redactSignedQuery(m string) string {
	trimmed := strings.TrimLeft(m, " \t\r\n\"'`?&")
	for {
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, `\u0026`):
			trimmed = trimmed[len(`\u0026`):]
		case strings.HasPrefix(lower, "&amp;"):
			trimmed = trimmed[len("&amp;"):]
		case strings.HasPrefix(lower, "%3f"):
			trimmed = trimmed[len("%3f"):]
		case strings.HasPrefix(lower, "%26"):
			trimmed = trimmed[len("%26"):]
		default:
			goto done
		}
	}
done:
	if k, _, ok := strings.Cut(trimmed, "="); ok && k != "" {
		if idx := strings.Index(strings.ToLower(k), "%3d"); idx > 0 {
			return k[:idx] + "=<redacted>"
		}
		return k + "=<redacted>"
	}
	if idx := strings.Index(strings.ToLower(trimmed), "%3d"); idx > 0 {
		return trimmed[:idx] + "=<redacted>"
	}
	return "<redacted>"
}

func isLikelyTokenValue(v string, minLen int, plainAlphaMin int) bool {
	if len(v) < minLen {
		return false
	}
	allHex := true
	hasLower := false
	hasInternalUpper := false
	hasPunct := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= '0' && c <= '9', c == '_', c == '%', c == '+', c == '/', c == '=':
			return true
		case c >= 'a' && c <= 'f':
			hasLower = true
		case c >= 'A' && c <= 'F':
			if i > 0 {
				hasInternalUpper = true
			}
		case c >= 'g' && c <= 'z':
			allHex = false
			hasLower = true
		case c >= 'A' && c <= 'Z':
			allHex = false
			if i > 0 {
				hasInternalUpper = true
			}
		case c == '-' || c == '.':
			allHex = false
			hasPunct = true
		default:
			allHex = false
		}
	}
	if allHex || (hasLower && hasInternalUpper) {
		return true
	}
	return !hasPunct && len(v) >= plainAlphaMin
}

func isPlaceholderURLPath(path string) bool {
	if len(path) < 4 {
		return true
	}
	if strings.HasPrefix(path, "[") || strings.HasPrefix(path, ".") {
		return true
	}
	if strings.ContainsAny(path, "<>") {
		return true
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, `\u003c`) || strings.Contains(lower, "%3c")
}

func findXSecretLeaks(text string) []string {
	var leaks []string
	seenKeys := make(map[string]bool)
	for _, re := range []*regexp.Regexp{xSecretCookieRawRe, xSecretCookieEncRe, xSecretCookieJSONRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) < 3 || !isLikelyTokenValue(m[2], 8, 24) {
				continue
			}
			key := strings.ToLower(m[1])
			if !seenKeys[key] {
				seenKeys[key] = true
				leaks = append(leaks, key+"=<redacted>")
			}
		}
	}
	csrfFound := false
	for _, re := range []*regexp.Regexp{xCSRFHeaderRe, xCSRFHeaderEncRe, xCSRFKVRe, xCSRFKVEncRe, xCSRFJSONRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) >= 2 && isLikelyTokenValue(m[1], 8, 24) {
				csrfFound = true
				break
			}
		}
		if csrfFound {
			break
		}
	}
	if csrfFound {
		leaks = append(leaks, "x-csrf-token: <redacted>")
	}
	bearerFound := false
	for _, re := range []*regexp.Regexp{xAuthBearerHeaderRe, xAuthBearerJSONRe, xAuthBearerEncRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) >= 2 && isLikelyTokenValue(m[1], 12, 32) {
				bearerFound = true
				break
			}
		}
		if bearerFound {
			break
		}
	}
	if !bearerFound {
		for _, re := range []*regexp.Regexp{xBareBearerRe, xBearerJSONRe} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if len(m) >= 2 && isLikelyTokenValue(m[1], 16, 32) {
					bearerFound = true
					break
				}
			}
			if bearerFound {
				break
			}
		}
	}
	if !bearerFound {
		for _, m := range xRawWebBearerRe.FindAllStringSubmatch(text, -1) {
			if len(m) >= 2 && strings.Trim(m[1], "A") != "" {
				bearerFound = true
				break
			}
		}
	}
	if bearerFound {
		leaks = append(leaks, "Authorization: Bearer <redacted>")
	}
	return leaks
}

func findXSignedURLLeaks(text string) []string {
	var leaks []string
	if m := xSignedMediaURLRe.FindString(text); m != "" {
		leaks = append(leaks, redactXSignedURL(m))
	}
	for _, m := range xPrivateTonURLRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 3 && !isPlaceholderURLPath(m[2]) {
			leaks = append(leaks, "https://"+strings.ToLower(m[1])+"/<redacted>")
			break
		}
	}
	return leaks
}

func redactXSignedURL(m string) string {
	lower := strings.ToLower(m)
	start := 0
	if idx := strings.Index(lower, "://"); idx >= 0 {
		start = idx + 3
	} else if idx := strings.Index(lower, "%3a%2f%2f"); idx >= 0 {
		start = idx + len("%3a%2f%2f")
	}
	delims := []string{"?", "&", `\u0026`, "&amp;", "%3f", "%26"}
	bestIdx := -1
	bestDelim := ""
	for _, d := range delims {
		if rel := strings.Index(lower[start:], d); rel >= 0 {
			pos := start + rel
			if bestIdx < 0 || pos < bestIdx {
				bestIdx = pos
				bestDelim = m[pos : pos+len(d)]
			}
		}
	}
	if bestIdx > start {
		return m[:bestIdx] + bestDelim + "<redacted>"
	}
	return "<redacted>"
}

func parseFenceOpen(trimmed string) (byte, int) {
	if len(trimmed) < 3 {
		return 0, 0
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	if ch == '`' && strings.ContainsRune(trimmed[n:], '`') {
		return 0, 0
	}
	return ch, n
}

func isFenceClose(trimmed string, ch byte, minLen int) bool {
	if len(trimmed) < minLen || trimmed[0] != ch {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != ch {
			return false
		}
	}
	return true
}

func stripFencedCodeBlocks(md string) string {
	lines := strings.Split(md, "\n")
	var out strings.Builder
	var fenceChar byte
	fenceLen := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fenceLen == 0 {
			if ch, n := parseFenceOpen(trimmed); n > 0 {
				fenceChar, fenceLen = ch, n
				out.WriteByte('\n')
				continue
			}
			out.WriteString(line)
			out.WriteByte('\n')
		} else {
			if isFenceClose(trimmed, fenceChar, fenceLen) {
				fenceChar, fenceLen = 0, 0
			}
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func stripCodeBlocksAndSpans(md string) string {
	lines := strings.Split(stripFencedCodeBlocks(md), "\n")
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(stripInlineCode(line))
		out.WriteByte('\n')
	}
	return out.String()
}

func stripInlineCode(line string) string {
	var b strings.Builder
	inCode := false
	for i := 0; i < len(line); i++ {
		if line[i] == '`' {
			inCode = !inCode
			b.WriteByte(' ')
			continue
		}
		if !inCode {
			b.WriteByte(line[i])
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func collectMarkdownAnchors(mdPath string) (map[string]bool, error) {
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, err
	}
	text := stripFencedCodeBlocks(string(raw))
	anchors := make(map[string]bool)

	for _, m := range anchorTagRe.FindAllStringSubmatch(text, -1) {
		anchors[strings.ToLower(strings.TrimSpace(m[1]))] = true
	}
	for _, m := range headingRe.FindAllStringSubmatch(text, -1) {
		slug := githubHeadingSlug(m[1])
		if slug != "" {
			anchors[slug] = true
		}
	}
	return anchors, nil
}

func githubHeadingSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func loadReferenceTerms(absRoot string, overrideDir string) ([]string, string) {
	refFile := findReferenceIndexFile(absRoot, overrideDir)
	if refFile == "" {
		return nil, "checkdocs: reference index not found, skipping reference-name check"
	}
	f, err := os.Open(refFile)
	if err != nil {
		return nil, "checkdocs: reference index unreadable, skipping reference-name check"
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, "checkdocs: reference index read failed, skipping reference-name check"
	}

	seen := make(map[string]bool)
	var terms []string
	for _, m := range refHeadingRe.FindAllStringSubmatch(string(raw), -1) {
		full := strings.TrimSpace(m[1])
		if full != "" && !seen[strings.ToLower(full)] {
			seen[strings.ToLower(full)] = true
			terms = append(terms, full)
		}
		if _, repo, ok := strings.Cut(full, "/"); ok && len(repo) >= 6 {
			if !seen[strings.ToLower(repo)] {
				seen[strings.ToLower(repo)] = true
				terms = append(terms, repo)
			}
		}
	}
	return terms, fmt.Sprintf("checkdocs: loaded %d reference terms from local index", len(terms))
}

func findReferenceIndexFile(absRoot string, overrideDir string) string {
	candidates := []string{"README.md", "INDEX.md", "REFERENCE.md"}
	checkDir := func(dir string) string {
		if dir == "" {
			return ""
		}
		for _, name := range candidates {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
		return ""
	}

	if overrideDir != "" {
		return checkDir(overrideDir)
	}
	if envDir := strings.TrimSpace(os.Getenv("TRACE_REFERENCE_DIR")); envDir != "" {
		return checkDir(envDir)
	}

	repoName := filepath.Base(absRoot)
	cur := filepath.Dir(absRoot)
	for i := 0; i < 6; i++ {
		if found := checkDir(filepath.Join(cur, "reference", repoName)); found != "" {
			return found
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return ""
}
