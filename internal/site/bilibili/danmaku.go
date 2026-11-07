package bilibili

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// DanmakuElem represents a single Bilibili danmaku item decoded from DmSegMobileReply.
type DanmakuElem struct {
	ID       int64  `json:"id"`
	Progress int32  `json:"progress"` // milliseconds
	Mode     int32  `json:"mode"`
	Fontsize int32  `json:"fontsize"`
	Color    uint32 `json:"color"`
	MidHash  string `json:"midHash"`
	Content  string `json:"content"`
	Ctime    int64  `json:"ctime"`
	Weight   int32  `json:"weight"`
	Action   string `json:"action,omitempty"`
	Pool     int32  `json:"pool"`
	IDStr    string `json:"idStr"`
	Attr     int32  `json:"attr,omitempty"`
}

// DanmakuXML fetches all 6-minute protobuf danmaku segments for cid, deduplicates by idStr,
// sorts by progress ascending, and returns standard Bilibili XML bytes (C13).
func (c *Client) DanmakuXML(ctx context.Context, cid int64, durationSec int) ([]byte, error) {
	if err := c.RequireAuth("/x/v2/dm/web/seg.so"); err != nil {
		return nil, err
	}
	if cid <= 0 {
		return nil, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "获取弹幕需要有效的 cid",
		}
	}

	segCount := 1
	if durationSec > 0 {
		segCount = (durationSec + 359) / 360
	}
	if segCount < 1 {
		segCount = 1
	}

	var all []DanmakuElem
	for segIdx := 1; segIdx <= segCount; segIdx++ {
		q := url.Values{}
		q.Set("type", "1")
		q.Set("oid", strconv.FormatInt(cid, 10))
		q.Set("segment_index", strconv.Itoa(segIdx))
		fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/v2/dm/web/seg.so?" + q.Encode()

		resp, raw, err := c.doAPIRequest(ctx, http.MethodGet, fullURL, nil, "", true)
		if err != nil {
			if segIdx > 1 && resp != nil && resp.StatusCode == http.StatusNotFound {
				break
			}
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		// Check if response is a JSON error envelope rather than protobuf.
		if raw[0] == '{' && bytes.Contains(raw, []byte(`"code"`)) {
			if segIdx > 1 {
				break
			}
			return nil, &SiteError{
				Kind:     ErrKindUnavailable,
				Endpoint: fullURL,
				Message:  "弹幕接口返回错误响应",
			}
		}

		elems, err := DecodeDmSegMobileReply(raw)
		if err != nil {
			return nil, &SiteError{
				Kind:     ErrKindSchemaChanged,
				Endpoint: fullURL,
				Message:  "解析弹幕 protobuf 分片失败",
				Err:      err,
			}
		}
		all = append(all, elems...)
	}

	return FormatDanmakuXML(cid, all), nil
}

// DecodeDmSegMobileReply decodes a Bilibili DmSegMobileReply protobuf payload using protowire.
func DecodeDmSegMobileReply(b []byte) ([]DanmakuElem, error) {
	var elems []DanmakuElem
	for len(b) > 0 {
		num, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]

		if num == 1 && wtype == protowire.BytesType {
			msgBytes, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			b = b[m:]
			elem, err := decodeDanmakuElem(msgBytes)
			if err != nil {
				return nil, err
			}
			elems = append(elems, elem)
		} else {
			m := protowire.ConsumeFieldValue(num, wtype, b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			b = b[m:]
		}
	}
	return elems, nil
}

func decodeDanmakuElem(b []byte) (DanmakuElem, error) {
	var e DanmakuElem
	e.Mode = 1
	e.Fontsize = 25
	e.Color = 0xFFFFFF
	for len(b) > 0 {
		num, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return e, protowire.ParseError(n)
		}
		b = b[n:]

		switch {
		case num == 1 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.ID = int64(v)
			b = b[m:]
		case num == 2 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Progress = int32(v)
			b = b[m:]
		case num == 3 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Mode = int32(v)
			b = b[m:]
		case num == 4 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Fontsize = int32(v)
			b = b[m:]
		case num == 5 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Color = uint32(v)
			b = b[m:]
		case num == 6 && wtype == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.MidHash = v
			b = b[m:]
		case num == 7 && wtype == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Content = v
			b = b[m:]
		case num == 8 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Ctime = int64(v)
			b = b[m:]
		case num == 9 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Weight = int32(v)
			b = b[m:]
		case num == 10 && wtype == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Action = v
			b = b[m:]
		case num == 11 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Pool = int32(v)
			b = b[m:]
		case num == 12 && wtype == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.IDStr = v
			b = b[m:]
		case num == 13 && wtype == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			e.Attr = int32(v)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, wtype, b)
			if m < 0 {
				return e, protowire.ParseError(m)
			}
			b = b[m:]
		}
	}
	if e.IDStr == "" && e.ID != 0 {
		e.IDStr = strconv.FormatInt(e.ID, 10)
	}
	return e, nil
}

// EncodeDmSegMobileReply encodes DanmakuElem items into a DmSegMobileReply protobuf payload (used by tests/fixtures).
func EncodeDmSegMobileReply(elems []DanmakuElem) []byte {
	var out []byte
	for _, e := range elems {
		var msg []byte
		msg = protowire.AppendTag(msg, 1, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.ID))
		msg = protowire.AppendTag(msg, 2, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Progress))
		msg = protowire.AppendTag(msg, 3, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Mode))
		msg = protowire.AppendTag(msg, 4, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Fontsize))
		msg = protowire.AppendTag(msg, 5, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Color))
		msg = protowire.AppendTag(msg, 6, protowire.BytesType)
		msg = protowire.AppendString(msg, e.MidHash)
		msg = protowire.AppendTag(msg, 7, protowire.BytesType)
		msg = protowire.AppendString(msg, e.Content)
		msg = protowire.AppendTag(msg, 8, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Ctime))
		msg = protowire.AppendTag(msg, 9, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Weight))
		msg = protowire.AppendTag(msg, 11, protowire.VarintType)
		msg = protowire.AppendVarint(msg, uint64(e.Pool))
		idStr := e.IDStr
		if idStr == "" && e.ID != 0 {
			idStr = strconv.FormatInt(e.ID, 10)
		}
		msg = protowire.AppendTag(msg, 12, protowire.BytesType)
		msg = protowire.AppendString(msg, idStr)

		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, msg)
	}
	return out
}

// FormatDanmakuXML deduplicates danmaku elements by idStr, sorts by progress ascending, and formats Bilibili XML.
func FormatDanmakuXML(cid int64, elems []DanmakuElem) []byte {
	seen := make(map[string]bool, len(elems))
	dedup := make([]DanmakuElem, 0, len(elems))
	for _, e := range elems {
		key := e.IDStr
		if key == "" && e.ID != 0 {
			key = strconv.FormatInt(e.ID, 10)
		}
		if key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		e.IDStr = key
		dedup = append(dedup, e)
	}

	sort.SliceStable(dedup, func(i, j int) bool {
		if dedup[i].Progress != dedup[j].Progress {
			return dedup[i].Progress < dedup[j].Progress
		}
		return dedup[i].IDStr < dedup[j].IDStr
	})

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString("<i>\n")
	buf.WriteString("<chatserver>chat.bilibili.com</chatserver>\n")
	buf.WriteString(fmt.Sprintf("<chatid>%d</chatid>\n", cid))
	buf.WriteString("<mission>0</mission>\n")
	buf.WriteString("<maxlimit>3000</maxlimit>\n")
	buf.WriteString("<state>0</state>\n")
	buf.WriteString("<real_name>0</real_name>\n")
	buf.WriteString("<source>k-v</source>\n")

	for _, e := range dedup {
		sec := float64(e.Progress) / 1000.0
		mode := e.Mode
		if mode == 0 {
			mode = 1
		}
		fontsize := e.Fontsize
		if fontsize == 0 {
			fontsize = 25
		}
		color := e.Color
		if color == 0 {
			color = 16777215
		}
		pAttr := fmt.Sprintf("%.5f,%d,%d,%d,%d,%d,%s,%s",
			sec, mode, fontsize, color, e.Ctime, e.Pool, e.MidHash, e.IDStr)
		buf.WriteString(`<d p="`)
		_ = xml.EscapeText(&buf, []byte(pAttr))
		buf.WriteString(`">`)
		_ = xml.EscapeText(&buf, []byte(sanitizeXMLChars(e.Content)))
		buf.WriteString("</d>\n")
	}
	buf.WriteString("</i>\n")
	return buf.Bytes()
}

func sanitizeXMLChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0x09 || r == 0x0A || r == 0x0D ||
			(r >= 0x20 && r <= 0xD7FF) ||
			(r >= 0xE000 && r <= 0xFFFD) ||
			(r >= 0x10000 && r <= 0x10FFFF) {
			return r
		}
		return -1
	}, s)
}
