package site

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ID identifies a supported content platform in TRACE.
type ID string

const (
	Bilibili ID = "bilibili"
	Douyin   ID = "douyin"
)

// Errors returned by site and account-key validation helpers.
var (
	ErrUnknownSite = errors.New("site: unsupported site id")
	ErrInvalidUID  = errors.New("site: invalid account uid")
	ErrInvalidKey  = errors.New("site: invalid account key")
)

// All returns all supported site identifiers in canonical UI order.
func All() []ID {
	return []ID{Bilibili, Douyin}
}

// Valid reports whether id is a known site identifier.
func (id ID) Valid() bool {
	return id == Bilibili || id == Douyin
}

// ParseID normalizes a raw site string into a known ID.
func ParseID(raw string) (ID, bool) {
	switch ID(strings.ToLower(strings.TrimSpace(raw))) {
	case Bilibili:
		return Bilibili, true
	case Douyin:
		return Douyin, true
	default:
		return "", false
	}
}

// DisplayName returns the user-facing Chinese label for the site.
func (id ID) DisplayName() string {
	switch id {
	case Bilibili:
		return "B 站"
	case Douyin:
		return "抖音"
	default:
		return strings.TrimSpace(string(id))
	}
}

// DisplayName returns the user-facing Chinese label for a raw site string.
func DisplayName(raw string) string {
	if id, ok := ParseID(raw); ok {
		return id.DisplayName()
	}
	return strings.TrimSpace(raw)
}

// NormalizeUID validates and normalizes a per-site account UID:
//   - bilibili: positive decimal integer mid (leading zeros stripped)
//   - douyin: case-sensitive sec_uid preserved verbatim (including consecutive underscores)
func NormalizeUID(siteID ID, uid string) (string, error) {
	if !siteID.Valid() {
		return "", ErrUnknownSite
	}
	trimmed := strings.TrimSpace(uid)
	if trimmed == "" {
		return "", ErrInvalidUID
	}

	switch siteID {
	case Bilibili:
		for _, r := range trimmed {
			if r < '0' || r > '9' {
				return "", ErrInvalidUID
			}
		}
		mid, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || mid <= 0 {
			return "", ErrInvalidUID
		}
		return strconv.FormatInt(mid, 10), nil

	case Douyin:
		if strings.Contains(trimmed, "..") {
			return "", ErrInvalidUID
		}
		for _, r := range trimmed {
			if r <= 0x20 || r == 0x7f || unicode.IsSpace(r) {
				return "", ErrInvalidUID
			}
			switch r {
			case ':', '/', '\\', '"', '\'', '<', '>', '|', '?', '*':
				return "", ErrInvalidUID
			}
		}
		return trimmed, nil

	default:
		return "", ErrUnknownSite
	}
}

// FormatAccountKey validates siteID and uid and formats "<site>:<uid>".
func FormatAccountKey(siteID ID, uid string) (string, error) {
	normUID, err := NormalizeUID(siteID, uid)
	if err != nil {
		return "", err
	}
	return string(siteID) + ":" + normUID, nil
}

// AccountKey returns "<site>:<uid>", normalizing valid inputs and falling back to trimmed concatenation.
func AccountKey(siteID ID, uid string) string {
	if key, err := FormatAccountKey(siteID, uid); err == nil {
		return key
	}
	return strings.TrimSpace(string(siteID)) + ":" + strings.TrimSpace(uid)
}

// ParseAccountKey splits and validates a canonical "<site>:<uid>" key.
func ParseAccountKey(key string) (ID, string, error) {
	if key != strings.TrimSpace(key) || key == "" {
		return "", "", ErrInvalidKey
	}
	rawSite, rawUID, ok := strings.Cut(key, ":")
	if !ok || rawSite == "" || rawUID == "" {
		return "", "", ErrInvalidKey
	}
	if rawUID != strings.TrimSpace(rawUID) {
		return "", "", ErrInvalidKey
	}
	siteID, valid := ParseID(rawSite)
	if !valid || string(siteID) != rawSite {
		return "", "", fmt.Errorf("%w: %s", ErrUnknownSite, rawSite)
	}
	normUID, err := NormalizeUID(siteID, rawUID)
	if err != nil {
		return "", "", err
	}
	return siteID, normUID, nil
}

// DouyinAuthorDir returns the 12-character lowercase hex sha256 prefix used as
// the filesystem-safe author directory name for a Douyin sec_uid.
func DouyinAuthorDir(secUID string) string {
	sum := sha256.Sum256([]byte(secUID))
	return hex.EncodeToString(sum[:6])
}
