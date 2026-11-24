package weibo

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

type rawMobilePic struct {
	PID      string          `json:"pid"`
	PicID    string          `json:"pic_id"`
	URL      string          `json:"url"`
	Type     string          `json:"type"`
	VideoSrc string          `json:"videoSrc"`
	Video    string          `json:"video"`
	Geo      json.RawMessage `json:"geo"`
	Large    *struct {
		URL string          `json:"url"`
		Geo json.RawMessage `json:"geo"`
	} `json:"large"`
	BMiddle *struct {
		URL string          `json:"url"`
		Geo json.RawMessage `json:"geo"`
	} `json:"bmiddle"`
}

func decodeRawMobilePics(raw json.RawMessage) []rawMobilePic {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) {
		return nil
	}
	switch trimmed[0] {
	case '[':
		var arr []rawMobilePic
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&arr); err != nil {
			return nil
		}
		return arr
	case '{':
		var obj map[string]rawMobilePic
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&obj); err != nil || len(obj) == 0 {
			return nil
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			ni, errI := strconv.Atoi(strings.TrimSpace(keys[i]))
			nj, errJ := strconv.Atoi(strings.TrimSpace(keys[j]))
			if errI == nil && errJ == nil {
				return ni < nj
			}
			if errI == nil {
				return true
			}
			if errJ == nil {
				return false
			}
			return keys[i] < keys[j]
		})
		out := make([]rawMobilePic, 0, len(keys))
		for _, k := range keys {
			mp := obj[k]
			if strings.TrimSpace(mp.PID) == "" && strings.TrimSpace(mp.PicID) == "" {
				if _, err := strconv.Atoi(strings.TrimSpace(k)); err != nil {
					mp.PID = strings.TrimSpace(k)
				}
			}
			out = append(out, mp)
		}
		return out
	default:
		return nil
	}
}

func parseMobileGeo(raw json.RawMessage) (int, int) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 0, 0
	}
	var geo struct {
		Width  json.RawMessage `json:"width"`
		Height json.RawMessage `json:"height"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&geo); err != nil {
		return 0, 0
	}
	return parseJSONInt(geo.Width), parseJSONInt(geo.Height)
}

func promoteSinaimgPreviewToLarge(rawURL string, allowLocalhost bool) string {
	norm := normalizeWeiboMediaURL(rawURL, allowLocalhost)
	if norm == "" {
		return ""
	}
	u, err := url.Parse(norm)
	if err != nil || u.Host == "" {
		return norm
	}
	host := strings.ToLower(u.Hostname())
	isSinaimg := site.MatchDomainSuffix(host, []string{"sinaimg.cn"})
	isLoopback := allowLocalhost && site.IsLoopbackHost(host)
	if !isSinaimg && !isLoopback {
		return norm
	}
	segs := site.SplitCleanPath(u.Path)
	if len(segs) < 2 {
		return norm
	}
	seg0 := strings.ToLower(segs[0])
	if isCompressedSinaimgSegment(seg0) {
		segs[0] = "large"
		cloned := *u
		cloned.Path = "/" + strings.Join(segs, "/")
		return cloned.String()
	}
	return norm
}

func isCompressedSinaimgSegment(seg string) bool {
	if strings.HasPrefix(seg, "crop.") {
		return true
	}
	switch seg {
	case "thumbnail", "thumb150", "thumb180", "thumb300",
		"wap180", "wap360", "wap720",
		"bmiddle", "orj360", "orj480",
		"mw600", "mw690", "mw1024",
		"small", "square":
		return true
	default:
		return false
	}
}

func extractPicIDAndExtFromURL(rawURL string) (picID, ext string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", ""
	}
	base := strings.TrimSpace(path.Base(u.Path))
	if base == "" || base == "." || base == "/" {
		return "", ""
	}
	rawExt := strings.ToLower(path.Ext(base))
	switch rawExt {
	case ".jpg", ".jpeg", ".gif", ".png", ".webp":
		return strings.TrimSuffix(base, path.Ext(base)), rawExt
	default:
		return base, ""
	}
}

func (c *Client) buildMobilePicAssets(rp *rawPost, rewriter func(picID, rawURL string) (string, bool)) []ImageAsset {
	if rp == nil {
		return nil
	}
	items := decodeRawMobilePics(rp.Pics)
	if len(items) == 0 {
		return nil
	}
	out := make([]ImageAsset, 0, len(items))
	for i, mp := range items {
		if asset, ok := c.buildSingleMobilePicAsset(len(out)+1, i, mp, rp.LivePhoto, rewriter); ok {
			out = append(out, asset)
		}
	}
	return out
}

func (c *Client) buildSingleMobilePicAsset(
	index int,
	rawIdx int,
	mp rawMobilePic,
	livePhotos []string,
	rewriter func(picID, rawURL string) (string, bool),
) (ImageAsset, bool) {
	picID := strings.TrimSpace(mp.PID)
	if picID == "" {
		picID = strings.TrimSpace(mp.PicID)
	}
	var baseURL string
	var w, h int
	if mp.Large != nil && strings.TrimSpace(mp.Large.URL) != "" {
		baseURL = promoteSinaimgPreviewToLarge(mp.Large.URL, c.allowLocalhost)
		w, h = parseMobileGeo(mp.Large.Geo)
	}
	if baseURL == "" && strings.TrimSpace(mp.URL) != "" {
		baseURL = promoteSinaimgPreviewToLarge(mp.URL, c.allowLocalhost)
	}
	if w == 0 || h == 0 {
		gw, gh := parseMobileGeo(mp.Geo)
		if w == 0 {
			w = gw
		}
		if h == 0 {
			h = gh
		}
	}
	if baseURL == "" {
		return ImageAsset{}, false
	}
	urlPicID, ext := extractPicIDAndExtFromURL(baseURL)
	if picID == "" {
		picID = urlPicID
	}

	primaryURL := baseURL
	var fallbacks []string
	if rewriter != nil {
		if rewritten, ok := rewriter(picID, primaryURL); ok && rewritten != "" && rewritten != primaryURL {
			fallbacks = []string{primaryURL}
			primaryURL = rewritten
		}
	}

	rawVideo := strings.TrimSpace(mp.VideoSrc)
	if rawVideo == "" {
		rawVideo = strings.TrimSpace(mp.Video)
	}
	if rawVideo == "" && rawIdx >= 0 && rawIdx < len(livePhotos) {
		rawVideo = strings.TrimSpace(livePhotos[rawIdx])
	}
	normVideo := normalizeWeiboMediaURL(rawVideo, c.allowLocalhost)

	picType := strings.ToLower(strings.TrimSpace(mp.Type))
	kind := MediaKindImage
	var motionURL, gifVideoURL string
	var needsLivePhotoHop bool
	switch {
	case picType == "gif" || ext == ".gif":
		kind = MediaKindGIF
		gifVideoURL = normVideo
	case picType == "livephoto" || picType == "livephotos" || normVideo != "":
		kind = MediaKindLivePhoto
		motionURL = normVideo
		if motionURL != "" && site.IsWeiboLivePhotoInitialURL(motionURL) {
			needsLivePhotoHop = true
		}
	}

	return ImageAsset{
		Index:             index,
		PicID:             picID,
		Kind:              kind,
		Width:             w,
		Height:            h,
		PublicPreviewURL:  SafePublicImageURL(baseURL),
		URL:               primaryURL,
		FallbackURLs:      fallbacks,
		MotionURL:         motionURL,
		GIFVideoURL:       gifVideoURL,
		NeedsLivePhotoHop: needsLivePhotoHop,
	}, true
}

func decodePicURLHints(rp *rawPost) []string {
	if rp == nil {
		return nil
	}
	var hints []string
	for _, s := range []string{rp.OriginalPic, rp.BMiddlePic, rp.ThumbnailPic} {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			hints = append(hints, trimmed)
		}
	}
	trimmed := bytes.TrimSpace(rp.PicURLs)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var rawItems []json.RawMessage
		if json.Unmarshal(trimmed, &rawItems) == nil {
			for _, item := range rawItems {
				it := bytes.TrimSpace(item)
				if len(it) == 0 {
					continue
				}
				if it[0] == '"' {
					if s := parseJSONString(it); s != "" {
						hints = append(hints, s)
					}
					continue
				}
				if it[0] == '{' {
					var obj struct {
						OriginalPic  string `json:"original_pic"`
						BMiddlePic   string `json:"bmiddle_pic"`
						ThumbnailPic string `json:"thumbnail_pic"`
						URL          string `json:"url"`
					}
					if json.Unmarshal(it, &obj) == nil {
						for _, cand := range []string{obj.OriginalPic, obj.BMiddlePic, obj.ThumbnailPic, obj.URL} {
							if strings.TrimSpace(cand) != "" {
								hints = append(hints, strings.TrimSpace(cand))
								break
							}
						}
					}
				}
			}
		}
	}
	return hints
}

func (c *Client) buildPicIDFallbackAssets(rp *rawPost, rewriter func(picID, rawURL string) (string, bool)) []ImageAsset {
	if rp == nil {
		return nil
	}
	rawHints := decodePicURLHints(rp)
	byPicID := make(map[string]string, len(rawHints))
	var orderedURLs []string
	seenURL := make(map[string]bool, len(rawHints))
	defaultBase := ""

	for _, rawHint := range rawHints {
		promoted := promoteSinaimgPreviewToLarge(rawHint, c.allowLocalhost)
		if promoted == "" {
			continue
		}
		if defaultBase == "" {
			if u, err := url.Parse(promoted); err == nil && u.Host != "" {
				host := strings.ToLower(u.Hostname())
				if site.MatchDomainSuffix(host, []string{"sinaimg.cn"}) || (c.allowLocalhost && site.IsLoopbackHost(host)) {
					defaultBase = u.Scheme + "://" + u.Host
				}
			}
		}
		pid, _ := extractPicIDAndExtFromURL(promoted)
		if pid != "" {
			if _, exists := byPicID[pid]; !exists {
				byPicID[pid] = promoted
			}
		}
		if !seenURL[promoted] {
			seenURL[promoted] = true
			orderedURLs = append(orderedURLs, promoted)
		}
	}
	if defaultBase == "" {
		defaultBase = "https://wx1.sinaimg.cn"
	}

	buildOne := func(index int, picID, baseURL string) (ImageAsset, bool) {
		if baseURL == "" {
			return ImageAsset{}, false
		}
		urlPicID, ext := extractPicIDAndExtFromURL(baseURL)
		if picID == "" {
			picID = urlPicID
		}
		primaryURL := baseURL
		var fallbacks []string
		if rewriter != nil {
			if rewritten, ok := rewriter(picID, primaryURL); ok && rewritten != "" && rewritten != primaryURL {
				fallbacks = []string{primaryURL}
				primaryURL = rewritten
			}
		}
		kind := MediaKindImage
		if ext == ".gif" {
			kind = MediaKindGIF
		}
		return ImageAsset{
			Index:            index,
			PicID:            picID,
			Kind:             kind,
			PublicPreviewURL: SafePublicImageURL(baseURL),
			URL:              primaryURL,
			FallbackURLs:     fallbacks,
		}, true
	}

	if len(rp.PicIDs) > 0 {
		out := make([]ImageAsset, 0, len(rp.PicIDs))
		seenPic := make(map[string]bool, len(rp.PicIDs))
		for _, rawPID := range rp.PicIDs {
			pid := strings.TrimSpace(rawPID)
			if pid == "" || seenPic[pid] {
				continue
			}
			seenPic[pid] = true
			cleanPID, pidExt := extractPicIDAndExtFromURL("https://wx1.sinaimg.cn/large/" + pid)
			baseURL := byPicID[pid]
			if baseURL == "" && cleanPID != "" {
				baseURL = byPicID[cleanPID]
			}
			if baseURL == "" {
				fileName := pid
				if pidExt == "" {
					fileName = pid + ".jpg"
				} else {
					pid = cleanPID
				}
				baseURL = defaultBase + "/large/" + fileName
			}
			if asset, ok := buildOne(len(out)+1, pid, baseURL); ok {
				out = append(out, asset)
			}
		}
		return out
	}

	if len(orderedURLs) > 0 {
		out := make([]ImageAsset, 0, len(orderedURLs))
		for _, u := range orderedURLs {
			if asset, ok := buildOne(len(out)+1, "", u); ok {
				out = append(out, asset)
			}
		}
		return out
	}

	return nil
}

func (c *Client) selectVideoStreamsWithPageURLs(mi *rawMediaInfo, pageURLs map[string]string, opts StreamSelectOptions) ([]VideoStream, bool) {
	if streams, legacy := c.selectVideoStreams(mi, opts); len(streams) > 0 {
		return streams, legacy
	}
	if len(pageURLs) == 0 {
		return nil, false
	}
	durSec := 0
	if mi != nil {
		durSec = int(parseJSONFloat64(mi.Duration) + 0.5)
	}
	streams := parseComponentVideoStreams(pageURLs, "", "", durSec, opts, c.allowLocalhost)
	if len(streams) > 0 {
		return streams, true
	}
	return nil, false
}

func extractPageInfoCoverURL(pi *rawPageInfo, allowLocalhost bool) string {
	if pi == nil {
		return ""
	}
	if cover := extractPagePicURL(pi.PagePic, pi.MediaInfo, allowLocalhost); cover != "" {
		return promoteSinaimgPreviewToLarge(cover, allowLocalhost)
	}
	if pi.PicInfo != nil {
		for _, v := range []*rawPicVariant{pi.PicInfo.PicBig, pi.PicInfo.PicMiddle, pi.PicInfo.PicSmall} {
			if v != nil && strings.TrimSpace(v.URL) != "" {
				if cover := promoteSinaimgPreviewToLarge(v.URL, allowLocalhost); cover != "" {
					return cover
				}
			}
		}
	}
	return ""
}
