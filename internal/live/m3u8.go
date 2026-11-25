package live

import (
	"math"
	"net/url"
	"strconv"
	"strings"
)

type m3u8Segment struct {
	uri           string
	seq           int64
	discontinuity bool
	mapURI        string
	duration      float64
}

type m3u8Playlist struct {
	mapURI           string
	targetDuration   float64
	mediaSequence    int64
	hasMediaSequence bool
	hasDiscontinuity bool
	segments         []m3u8Segment
	endList          bool
	valid            bool
	invalidReason    string
	encrypted        bool
	// variants is set for a multivariant (master) playlist instead of segments.
	variants []m3u8Variant
}

type m3u8Variant struct {
	uri       string
	bandwidth int64
}

func (p m3u8Playlist) bestVariantInfo() (m3u8Variant, bool) {
	best := -1
	for i, v := range p.variants {
		if v.uri != "" && (best < 0 || v.bandwidth > p.variants[best].bandwidth) {
			best = i
		}
	}
	if best < 0 {
		return m3u8Variant{}, false
	}
	return p.variants[best], true
}

// parseM3U8Attributes splits a comma-separated HLS attribute list while respecting quoted strings.
func parseM3U8Attributes(raw string) [][2]string {
	var attrs [][2]string
	for i := 0; i < len(raw); {
		for i < len(raw) && (raw[i] == ',' || raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) {
			break
		}
		eq := strings.IndexByte(raw[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(raw[i : i+eq])
		i += eq + 1
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		var val string
		if i < len(raw) && (raw[i] == '"' || raw[i] == '\'') {
			quote := raw[i]
			i++
			start := i
			for i < len(raw) && raw[i] != quote {
				i++
			}
			val = raw[start:i]
			if i < len(raw) && raw[i] == quote {
				i++
			}
			for i < len(raw) && raw[i] != ',' {
				i++
			}
		} else {
			start := i
			for i < len(raw) && raw[i] != ',' {
				i++
			}
			val = strings.TrimSpace(raw[start:i])
		}
		if key != "" {
			attrs = append(attrs, [2]string{key, val})
		}
	}
	return attrs
}

func parseM3U8PlaylistDetails(content string, baseURL string) m3u8Playlist {
	content = strings.TrimPrefix(content, "\uFEFF")
	baseParsed, _ := url.Parse(baseURL)
	resolveRef := func(ref string) string {
		ref = strings.Trim(strings.TrimSpace(ref), `"'`)
		if ref == "" {
			return ""
		}
		if baseParsed != nil {
			if u, err := baseParsed.Parse(ref); err == nil {
				return u.String()
			}
		}
		return ref
	}

	out := m3u8Playlist{
		targetDuration: 1.0,
		mediaSequence:  0,
		valid:          strings.HasPrefix(strings.TrimSpace(content), "#EXTM3U"),
	}
	if !out.valid {
		out.invalidReason = "缺少 #EXTM3U 头"
	}
	invalidate := func(reason string) {
		if out.valid {
			out.valid, out.invalidReason = false, reason
		}
	}
	var curSeq int64
	var duration float64
	nextDisc := false
	pendingVariant := int64(-1)

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\uFEFF"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if td, err := strconv.ParseFloat(val, 64); err == nil && td > 0 && !math.IsNaN(td) && !math.IsInf(td, 0) {
				out.targetDuration = td
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			if seq, err := strconv.ParseInt(val, 10, 64); err == nil && seq >= 0 {
				out.mediaSequence = seq
				out.hasMediaSequence = true
				curSeq = seq
			}
			continue
		}
		if line == "#EXT-X-DISCONTINUITY" {
			out.hasDiscontinuity = true
			nextDisc = true
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			method := ""
			for _, kv := range parseM3U8Attributes(strings.TrimPrefix(line, "#EXT-X-KEY:")) {
				if strings.EqualFold(kv[0], "METHOD") {
					method = strings.TrimSpace(kv[1])
				}
			}
			// Sticky encryption flag: once any key specifies a non-NONE method,
			// a later METHOD=NONE does not clear the playlist's encrypted state.
			if !strings.EqualFold(method, "NONE") {
				out.encrypted = true
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-BYTERANGE:") {
			invalidate("不支持按字节范围切分的分片")
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pendingVariant = 0
			for _, kv := range parseM3U8Attributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")) {
				if strings.EqualFold(kv[0], "BANDWIDTH") {
					if bw, err := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64); err == nil && bw > 0 {
						pendingVariant = bw
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			value := strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0]
			duration, _ = strconv.ParseFloat(strings.TrimSpace(value), 64)
			if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				invalidate("分片时长无效")
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			for _, kv := range parseM3U8Attributes(strings.TrimPrefix(line, "#EXT-X-MAP:")) {
				switch {
				case strings.EqualFold(kv[0], "URI"):
					out.mapURI = resolveRef(kv[1])
				case strings.EqualFold(kv[0], "BYTERANGE") && strings.TrimSpace(kv[1]) != "":
					invalidate("不支持按字节范围切分的初始化分片")
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-ENDLIST") {
			out.endList = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pendingVariant >= 0 {
			out.variants = append(out.variants, m3u8Variant{uri: resolveRef(line), bandwidth: pendingVariant})
			pendingVariant = -1
			continue
		}
		out.segments = append(out.segments, m3u8Segment{
			uri:           resolveRef(line),
			seq:           curSeq,
			discontinuity: nextDisc,
			mapURI:        out.mapURI,
			duration:      duration,
		})
		if out.mapURI == "" {
			invalidate("分片缺少 fMP4 初始化分片（EXT-X-MAP）")
		}
		curSeq++
		nextDisc = false
		duration = 0
	}
	return out
}
