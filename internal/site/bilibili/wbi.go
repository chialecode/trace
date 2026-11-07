package bilibili

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

var mixinKeyEncTab = [64]int{
	46, 47, 18, 2, 53, 8, 23, 32,
	15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19,
	29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61,
	26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63,
	57, 62, 11, 36, 20, 34, 44, 52,
}

// ExtractWBIKey extracts the filename stem from a wbi_img URL.
func ExtractWBIKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		rawURL = u.Path
	}
	base := path.Base(rawURL)
	if dot := strings.IndexByte(base, '.'); dot > 0 {
		return base[:dot]
	}
	return base
}

// MixinKey derives the 32-character WBI mixin_key from img_key and sub_key.
func MixinKey(imgKey, subKey string) string {
	raw := imgKey + subKey
	if len(raw) < 64 {
		return ""
	}
	var out [32]byte
	for i := 0; i < 32; i++ {
		out[i] = raw[mixinKeyEncTab[i]]
	}
	return string(out[:])
}

// SignWBIParams signs query parameters with mixinKey and wts, returning a new url.Values containing wts and w_rid.
func SignWBIParams(params url.Values, mixinKey string, wts int64) url.Values {
	out := make(url.Values, len(params)+2)
	keys := make([]string, 0, len(params)+1)
	for k, vals := range params {
		if k == "w_rid" || k == "wts" {
			continue
		}
		v := ""
		if len(vals) > 0 {
			v = vals[0]
		}
		cleaned := stripWBIChars(v)
		out.Set(k, cleaned)
		keys = append(keys, k)
	}
	wtsStr := strconv.FormatInt(wts, 10)
	out.Set("wts", wtsStr)
	keys = append(keys, "wts")
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(encodeURIComponent(k))
		sb.WriteByte('=')
		sb.WriteString(encodeURIComponent(out.Get(k)))
	}
	query := sb.String()
	sum := md5.Sum([]byte(query + mixinKey))
	out.Set("w_rid", hex.EncodeToString(sum[:]))
	return out
}

func stripWBIChars(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '!', '\'', '(', ')', '*':
			return -1
		default:
			return r
		}
	}, s)
}

func encodeURIComponent(s string) string {
	escaped := url.QueryEscape(s)
	return strings.ReplaceAll(escaped, "+", "%20")
}
