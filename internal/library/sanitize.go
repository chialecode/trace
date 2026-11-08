package library

import (
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

// SanitizeFileName sanitizes a single file or directory name segment for Windows/cross-platform safety (C16).
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

// NormalizeRawID strips optional "<kind>:" prefix from an item ID (e.g. "video:BV1xx" -> "BV1xx").
func NormalizeRawID(kind, id string) string {
	id = strings.TrimSpace(id)
	prefix := strings.ToLower(strings.TrimSpace(kind)) + ":"
	if strings.HasPrefix(strings.ToLower(id), prefix) {
		return id[len(prefix):]
	}
	if strings.HasPrefix(strings.ToLower(id), "live:") {
		return id[len("live:"):]
	}
	return id
}

// CanonicalItemKey returns "bilibili:<kind>:<raw_id>".
func CanonicalItemKey(site, kind, id string) string {
	if site == "" {
		site = "bilibili"
	}
	rawID := NormalizeRawID(kind, id)
	return strings.ToLower(strings.TrimSpace(site)) + ":" +
		strings.ToLower(strings.TrimSpace(kind)) + ":" +
		rawID
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
