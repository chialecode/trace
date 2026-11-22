package weibo

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	base62Alphabet  = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	base62Radix     = 62
	midChunkDigits  = 7
	mblogChunkChars = 4
	midChunkLimit   = 10_000_000 // 10^7; non-leading 4-char Base62 chunks must decode strictly below this
)

var (
	// ErrInvalidMid is returned when a Weibo numeric mid string is empty, zero, or contains non-digits.
	ErrInvalidMid = errors.New("weibo: invalid numeric mid")
	// ErrInvalidMblogID is returned when a Weibo Base62 mblogid is empty, zero, out of range, or has invalid characters.
	ErrInvalidMblogID = errors.New("weibo: invalid base62 mblogid")
)

var base62DecodeTable = func() [256]int8 {
	var tbl [256]int8
	for i := range tbl {
		tbl[i] = -1
	}
	for i := 0; i < len(base62Alphabet); i++ {
		tbl[base62Alphabet[i]] = int8(i)
	}
	return tbl
}()

// MidToMblogID encodes a positive decimal Weibo mid string into its canonical Base62 mblogid
// using Weibo's right-aligned 7-digit / 4-char segmented conversion (weibo.md §3.2).
// All math uses exact integer/string operations and never passes through float64.
func MidToMblogID(mid string) (string, error) {
	if mid == "" {
		return "", ErrInvalidMid
	}
	for i := 0; i < len(mid); i++ {
		if mid[i] < '0' || mid[i] > '9' {
			return "", fmt.Errorf("%w: non-digit character", ErrInvalidMid)
		}
	}
	normalized := strings.TrimLeft(mid, "0")
	if normalized == "" {
		return "", fmt.Errorf("%w: zero value", ErrInvalidMid)
	}

	chunkCount := (len(normalized) + midChunkDigits - 1) / midChunkDigits
	chunks := make([]string, chunkCount)
	idx := chunkCount - 1

	for end := len(normalized); end > 0; {
		start := end - midChunkDigits
		if start < 0 {
			start = 0
		}
		var val uint64
		for i := start; i < end; i++ {
			val = val*10 + uint64(normalized[i]-'0')
		}
		encoded := encodeBase62Uint(val)
		if start > 0 {
			// Non-leading chunks are left-padded with '0' to exactly 4 Base62 characters.
			for len(encoded) < mblogChunkChars {
				encoded = "0" + encoded
			}
		}
		chunks[idx] = encoded
		idx--
		end = start
	}

	return strings.Join(chunks, ""), nil
}

// MblogIDToMid decodes a Weibo Base62 mblogid into its canonical positive decimal mid string
// using right-aligned 4-char / 7-digit segmented conversion (weibo.md §3.2).
// Non-leading chunks decoding to >= 10,000,000 (such as "FXsk") are rejected.
func MblogIDToMid(mblogid string) (string, error) {
	if mblogid == "" {
		return "", ErrInvalidMblogID
	}
	allZero := true
	for i := 0; i < len(mblogid); i++ {
		c := mblogid[i]
		if base62DecodeTable[c] < 0 {
			return "", fmt.Errorf("%w: invalid character", ErrInvalidMblogID)
		}
		if c != '0' {
			allZero = false
		}
	}
	if allZero {
		return "", fmt.Errorf("%w: zero value", ErrInvalidMblogID)
	}

	chunkCount := (len(mblogid) + mblogChunkChars - 1) / mblogChunkChars
	chunks := make([]string, chunkCount)
	idx := chunkCount - 1

	for end := len(mblogid); end > 0; {
		start := end - mblogChunkChars
		if start < 0 {
			start = 0
		}
		var val uint64
		for i := start; i < end; i++ {
			val = val*base62Radix + uint64(base62DecodeTable[mblogid[i]])
		}
		if start > 0 {
			if val >= midChunkLimit {
				return "", fmt.Errorf("%w: non-leading chunk overflows 7 decimal digits", ErrInvalidMblogID)
			}
			chunks[idx] = formatSevenDigits(val)
		} else {
			chunks[idx] = strconv.FormatUint(val, 10)
		}
		idx--
		end = start
	}

	joined := strings.TrimLeft(strings.Join(chunks, ""), "0")
	if joined == "" {
		return "", fmt.Errorf("%w: zero value", ErrInvalidMblogID)
	}
	return joined, nil
}

func encodeBase62Uint(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [16]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = base62Alphabet[v%base62Radix]
		v /= base62Radix
	}
	return string(buf[pos:])
}

func formatSevenDigits(v uint64) string {
	var buf [midChunkDigits]byte
	for i := midChunkDigits - 1; i >= 0; i-- {
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[:])
}
