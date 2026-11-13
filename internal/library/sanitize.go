package library

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const MaxFileNameRunes = 80

var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeFileName sanitizes a single file or directory name segment for Windows/cross-platform safety.
// It replaces illegal characters (<>:"/\|?* and control chars), trims trailing dots/spaces,
// prefixes Windows reserved device names, and truncates to MaxFileNameRunes runes.
func SanitizeFileName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7F || unicode.IsControl(r) {
			sb.WriteByte('_')
			continue
		}
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			sb.WriteByte('_')
		default:
			sb.WriteRune(r)
		}
	}

	cleaned := strings.TrimSpace(sb.String())
	cleaned = strings.TrimRight(cleaned, ". ")
	cleaned = strings.TrimLeft(cleaned, " ")
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		cleaned = "untitled"
	}

	stem := cleaned
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	if windowsReservedNames[strings.ToUpper(strings.TrimSpace(stem))] {
		cleaned = "_" + cleaned
	}

	runes := []rune(cleaned)
	if len(runes) > MaxFileNameRunes {
		cleaned = strings.TrimRight(string(runes[:MaxFileNameRunes]), ". ")
		if cleaned == "" {
			cleaned = "untitled"
		}
	}
	return cleaned
}

// CreatorDirName returns "<mid>_<sanitized_uname>" for a creator directory.
func CreatorDirName(mid int64, uname string) string {
	safeName := SanitizeFileName(uname)
	if mid <= 0 {
		return "0_" + safeName
	}
	return fmt.Sprintf("%d_%s", mid, safeName)
}

// ItemDirName returns "<kind>_<sanitized_id>" for an item directory.
func ItemDirName(kind, id string) string {
	cleanID := NormalizeRawID(kind, id)
	return SanitizeFileName(strings.ToLower(strings.TrimSpace(kind)) + "_" + cleanID)
}

// NormalizeRawID strips optional "<site>:<kind>:" or "<kind>:" prefixes from an item ID.
func NormalizeRawID(kind, id string) string {
	id = strings.TrimSpace(id)
	lower := strings.ToLower(id)
	cleanKind := strings.ToLower(strings.TrimSpace(kind))
	for _, site := range []string{"bilibili:", "douyin:"} {
		if strings.HasPrefix(lower, site) {
			id = id[len(site):]
			lower = lower[len(site):]
			break
		}
	}
	if cleanKind != "" && strings.HasPrefix(lower, cleanKind+":") {
		return id[len(cleanKind)+1:]
	}
	for _, p := range []string{"video:", "dynamic:", "note:", "live:"} {
		if strings.HasPrefix(lower, p) {
			return id[len(p):]
		}
	}
	return id
}

// CanonicalItemKey returns "<site>:<kind>:<raw_id>" (empty site defaults to "bilibili").
func CanonicalItemKey(site, kind, id string) string {
	site = strings.ToLower(strings.TrimSpace(site))
	if site == "" {
		site = "bilibili"
	}
	normKind := strings.ToLower(strings.TrimSpace(kind))
	if normKind == "note" && site == "douyin" {
		normKind = "dynamic"
	}
	rawID := NormalizeRawID(normKind, id)
	return site + ":" + normKind + ":" + rawID
}

// ValidateDouyinSecUID verifies a Douyin sec_uid preserves exact case/underscores
// and contains no whitespace, path separators, traversal segments, or colons.
func ValidateDouyinSecUID(secUID string) error {
	if secUID == "" {
		return fmt.Errorf("library: douyin sec_uid cannot be empty")
	}
	if secUID == "." || secUID == ".." || strings.Contains(secUID, "..") {
		return fmt.Errorf("library: invalid douyin sec_uid")
	}
	for _, r := range secUID {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' || r == 0 {
			return fmt.Errorf("library: forbidden character in douyin sec_uid")
		}
	}
	return nil
}

// ValidateSafeIDSegment validates an item directory ID segment (such as aweme_id or room_id).
func ValidateSafeIDSegment(id string) error {
	if id == "" || id == "." || id == ".." || strings.Contains(id, "..") {
		return fmt.Errorf("library: invalid item ID segment")
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' || r == 0 ||
			r == '<' || r == '>' || r == '"' || r == '|' || r == '?' || r == '*' {
			return fmt.Errorf("library: forbidden character in item ID segment")
		}
	}
	return nil
}

// DouyinCreatorDirHash returns the first 12 lowercase hex characters of sha256(sec_uid) (DEC-043).
func DouyinCreatorDirHash(secUID string) (string, error) {
	if err := ValidateDouyinSecUID(secUID); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(secUID))
	return hex.EncodeToString(sum[:])[:12], nil
}

// DouyinCreatorDir resolves <cacheRoot>/douyin/<sha256(sec_uid)[:12]>.
func DouyinCreatorDir(cacheRoot, secUID string) (string, error) {
	if strings.TrimSpace(cacheRoot) == "" {
		return "", fmt.Errorf("library: cacheRoot cannot be empty")
	}
	dirHash, err := DouyinCreatorDirHash(secUID)
	if err != nil {
		return "", err
	}
	return EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "douyin", dirHash))
}

// DouyinWorkDir resolves <cacheRoot>/douyin/<sha256(sec_uid)[:12]>/works/<aweme_id>.
func DouyinWorkDir(cacheRoot, secUID, awemeID string) (string, error) {
	creatorDir, err := DouyinCreatorDir(cacheRoot, secUID)
	if err != nil {
		return "", err
	}
	cleanID := NormalizeRawID("video", awemeID)
	if err := ValidateSafeIDSegment(cleanID); err != nil {
		return "", err
	}
	return EnsureWithinRoot(cacheRoot, filepath.Join(creatorDir, "works", cleanID))
}

// DouyinLiveDir resolves <cacheRoot>/douyin/<sha256(sec_uid)[:12]>/lives/<room_id>.
func DouyinLiveDir(cacheRoot, secUID, roomID string) (string, error) {
	creatorDir, err := DouyinCreatorDir(cacheRoot, secUID)
	if err != nil {
		return "", err
	}
	cleanID := NormalizeRawID("live", roomID)
	if err := ValidateSafeIDSegment(cleanID); err != nil {
		return "", err
	}
	return EnsureWithinRoot(cacheRoot, filepath.Join(creatorDir, "lives", cleanID))
}

// EnsureWithinRoot verifies that target is strictly inside root (or equal to root)
// and does not escape via relative segments ("..").
func EnsureWithinRoot(root, target string) (string, error) {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("library: resolve root path: %w", err)
	}
	absTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", fmt.Errorf("library: resolve target path: %w", err)
	}

	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return "", fmt.Errorf("library: rel path check failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("library: path %q escapes root %q", absTarget, absRoot)
	}
	return absTarget, nil
}
