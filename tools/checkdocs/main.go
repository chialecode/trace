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
	anchorTagRe  = regexp.MustCompile(`<a\s+(?:id|name)=["']([^"']+)["']`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
	mdLinkRe     = regexp.MustCompile(`!?\[[^\]]*\]\(([^)]+)\)`)
	refHeadingRe = regexp.MustCompile(`(?m)^###\s+([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)\s*$`)
	winAbsPathRe = regexp.MustCompile(`(?i)\b[cd]:\\[A-Za-z0-9_.-]+`)
	unixHomeRe   = regexp.MustCompile(`(?:/Users/|/home/runner/)[A-Za-z0-9_.-]+`)
	cookieLeakRe = regexp.MustCompile(`(?i)\b(?:SESSDATA|bili_jct|DedeUserID__ckMd5|ac_time_value)=[A-Za-z0-9%._-]{6,}`)
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

		// 2. Check leaked Cookie/token values
		if m := cookieLeakRe.FindString(text); m != "" {
			findings = append(findings, fmt.Sprintf("%s: contains Cookie/token value %q", relPath, m))
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

func stripCodeBlocksAndSpans(md string) string {
	lines := strings.Split(md, "\n")
	var out strings.Builder
	inFence := false
	fenceMarker := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if strings.HasPrefix(trimmed, "```") {
				inFence = true
				fenceMarker = "```"
				out.WriteByte('\n')
				continue
			}
			if strings.HasPrefix(trimmed, "~~~") {
				inFence = true
				fenceMarker = "~~~"
				out.WriteByte('\n')
				continue
			}
			out.WriteString(stripInlineCode(line))
			out.WriteByte('\n')
		} else {
			if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
			}
			out.WriteByte('\n')
		}
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
	text := string(raw)
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
