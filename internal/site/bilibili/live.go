package bilibili

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// MaxLiveBatchUIDs is the maximum number of UIDs queried per get_status_info_by_uids call.
const MaxLiveBatchUIDs = 50

// LiveRoomInit holds canonical room information from /room/v1/Room/room_init.
type LiveRoomInit struct {
	RoomID     int64 `json:"roomId"`
	ShortID    int64 `json:"shortId"`
	UID        int64 `json:"uid"`
	LiveStatus int   `json:"liveStatus"` // 0=offline, 1=live, 2=carousel
	Encrypted  bool  `json:"encrypted"`
	Locked     bool  `json:"locked"`
	LiveTime   int64 `json:"liveTime"`
}

// LiveUserStatus holds live broadcast status for a single UID.
type LiveUserStatus struct {
	UID        int64  `json:"uid"`
	RoomID     int64  `json:"roomId"`
	ShortID    int64  `json:"shortId"`
	Title      string `json:"title"`
	Uname      string `json:"uname"`
	Face       string `json:"face"`
	Cover      string `json:"cover"`
	AreaName   string `json:"areaName"`
	LiveStatus int    `json:"liveStatus"` // 0=offline, 1=live, 2=carousel
	LiveTime   int64  `json:"liveTime"`
	Online     int    `json:"online"`
}

// LivePlaySelection holds the chosen live stream protocol, format, codec, quality, and ordered URLs.
type LivePlaySelection struct {
	RoomID      int64    `json:"roomId"`
	UID         int64    `json:"uid"`
	LiveStatus  int      `json:"liveStatus"`
	Protocol    string   `json:"protocol"` // "http_hls" or "http_stream"
	Format      string   `json:"format"`   // "fmp4", "ts", "flv"
	Codec       string   `json:"codec"`    // "avc", "hevc", "av1"
	Quality     int      `json:"quality"`
	QualityName string   `json:"qualityName"`
	URLs        []string `json:"-"`
}

// LiveQualityFallbackName returns the canonical Chinese live quality label when g_qn_desc is absent.
func LiveQualityFallbackName(qn int) string {
	switch qn {
	case 10000:
		return "原画"
	case 400:
		return "蓝光"
	case 250:
		return "超清"
	case 150:
		return "高清"
	case 80:
		return "流畅"
	default:
		if qn > 0 {
			return strconv.Itoa(qn)
		}
		return ""
	}
}

// FormatLiveQualitySpec formats quality name (or qn fallback) and codec into "原画 · AVC".
func FormatLiveQualitySpec(qualityName string, quality int, codec string) string {
	qName := strings.TrimSpace(qualityName)
	if qName == "" && quality > 0 {
		qName = LiveQualityFallbackName(quality)
	}
	cName := strings.ToUpper(strings.TrimSpace(codec))
	if qName != "" && cName != "" {
		return qName + " · " + cName
	}
	if qName != "" {
		return qName
	}
	return cName
}

// RoomInit resolves a room_id or short_id via /room/v1/Room/room_init.
func (c *Client) RoomInit(ctx context.Context, roomOrShortID int64) (LiveRoomInit, error) {
	if err := c.RequireAuth("/room/v1/Room/room_init"); err != nil {
		return LiveRoomInit{}, err
	}
	if roomOrShortID <= 0 {
		return LiveRoomInit{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "无效的直播间房间号",
		}
	}

	q := url.Values{}
	q.Set("id", strconv.FormatInt(roomOrShortID, 10))
	fullURL := strings.TrimRight(c.endpoints.LiveAPIBase, "/") + "/room/v1/Room/room_init?" + q.Encode()

	var raw struct {
		RoomID      int64 `json:"room_id"`
		ShortID     int64 `json:"short_id"`
		UID         int64 `json:"uid"`
		LiveStatus  int   `json:"live_status"`
		Encrypted   bool  `json:"encrypted"`
		PwdVerified bool  `json:"pwd_verified"`
		IsLocked    bool  `json:"is_locked"`
		LiveTime    int64 `json:"live_time"`
	}
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return LiveRoomInit{}, err
	}
	if raw.Encrypted || raw.IsLocked {
		return LiveRoomInit{}, &SiteError{
			Kind:     ErrKindBlocked,
			Code:     203,
			Endpoint: fullURL,
			Message:  "该直播间已加密或封禁，不支持打开或录制",
		}
	}
	if raw.RoomID <= 0 {
		return LiveRoomInit{}, &SiteError{
			Kind:     ErrKindUnavailable,
			Endpoint: fullURL,
			Message:  "未找到对应的直播间",
		}
	}
	return LiveRoomInit{
		RoomID:     raw.RoomID,
		ShortID:    raw.ShortID,
		UID:        raw.UID,
		LiveStatus: raw.LiveStatus,
		Encrypted:  raw.Encrypted,
		Locked:     raw.IsLocked,
		LiveTime:   raw.LiveTime,
	}, nil
}

// LiveStatusByUIDs queries live broadcast status for a list of UIDs in batches of at most 50 UIDs.
func (c *Client) LiveStatusByUIDs(ctx context.Context, uids []int64) (map[int64]LiveUserStatus, error) {
	if err := c.RequireAuth("/room/v1/Room/get_status_info_by_uids"); err != nil {
		return nil, err
	}
	out := make(map[int64]LiveUserStatus, len(uids))
	if len(uids) == 0 {
		return out, nil
	}

	// Deduplicate UIDs while preserving order.
	seen := make(map[int64]bool, len(uids))
	unique := make([]int64, 0, len(uids))
	for _, u := range uids {
		if u > 0 && !seen[u] {
			seen[u] = true
			unique = append(unique, u)
		}
	}

	for start := 0; start < len(unique); start += MaxLiveBatchUIDs {
		end := start + MaxLiveBatchUIDs
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]

		q := url.Values{}
		for _, id := range batch {
			q.Add("uids[]", strconv.FormatInt(id, 10))
		}
		fullURL := strings.TrimRight(c.endpoints.LiveAPIBase, "/") + "/room/v1/Room/get_status_info_by_uids?" + q.Encode()

		var rawMap map[string]struct {
			UID        int64  `json:"uid"`
			RoomID     int64  `json:"room_id"`
			ShortID    int64  `json:"short_id"`
			Title      string `json:"title"`
			Uname      string `json:"uname"`
			Face       string `json:"face"`
			Cover      string `json:"cover_from_user"`
			Keyframe   string `json:"keyframe"`
			AreaName   string `json:"area_v2_name"`
			LiveStatus int    `json:"live_status"`
			LiveTime   int64  `json:"live_time"`
			Online     int    `json:"online"`
		}

		if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &rawMap); err != nil {
			return nil, err
		}
		for k, v := range rawMap {
			uid := v.UID
			if uid <= 0 {
				uid, _ = strconv.ParseInt(k, 10, 64)
			}
			if uid <= 0 {
				continue
			}
			cover := v.Cover
			if cover == "" {
				cover = v.Keyframe
			}
			out[uid] = LiveUserStatus{
				UID:        uid,
				RoomID:     v.RoomID,
				ShortID:    v.ShortID,
				Title:      v.Title,
				Uname:      v.Uname,
				Face:       normalizeHTTPSURL(v.Face),
				Cover:      normalizeHTTPSURL(cover),
				AreaName:   v.AreaName,
				LiveStatus: v.LiveStatus,
				LiveTime:   v.LiveTime,
				Online:     v.Online,
			}
		}
	}
	return out, nil
}

// LiveRoomPlayInfo fetches stream play info from /xlive/web-room/v2/index/getRoomPlayInfo
// and selects the optimal stream (preferring http_hls + fmp4, falling back to http_stream + flv,
// then highest qn and codecOrder, with official CDN prioritized and *.mcdn.* excluded).
func (c *Client) LiveRoomPlayInfo(ctx context.Context, roomID int64, codecOrder []string) (LivePlaySelection, error) {
	if err := c.RequireAuth("/xlive/web-room/v2/index/getRoomPlayInfo"); err != nil {
		return LivePlaySelection{}, err
	}
	if roomID <= 0 {
		return LivePlaySelection{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "无效的直播间房间号",
		}
	}

	q := url.Values{}
	q.Set("room_id", strconv.FormatInt(roomID, 10))
	q.Set("protocol", "0,1")
	q.Set("format", "0,1,2")
	q.Set("codec", "0,1,2")
	q.Set("qn", "10000")
	q.Set("platform", "web")
	q.Set("ptype", "8")
	q.Set("dolby", "5")
	q.Set("panorama", "1")
	fullURL := strings.TrimRight(c.endpoints.LiveAPIBase, "/") + "/xlive/web-room/v2/index/getRoomPlayInfo?" + q.Encode()

	var raw struct {
		RoomID          int64 `json:"room_id"`
		ShortID         int64 `json:"short_id"`
		UID             int64 `json:"uid"`
		LiveStatus      int   `json:"live_status"`
		Encrypted       bool  `json:"encrypted"`
		PwdVerified     bool  `json:"pwd_verified"`
		AllSpecialTypes []int `json:"all_special_types"`
		PlayurlInfo     *struct {
			Playurl *struct {
				GQnDesc []struct {
					Qn   int    `json:"qn"`
					Desc string `json:"desc"`
				} `json:"g_qn_desc"`
				Stream []struct {
					ProtocolName string `json:"protocol_name"`
					Format       []struct {
						FormatName string `json:"format_name"`
						Codec      []struct {
							CodecName string `json:"codec_name"`
							CurrentQn int    `json:"current_qn"`
							AcceptQn  []int  `json:"accept_qn"`
							BaseURL   string `json:"base_url"`
							URLInfo   []struct {
								Host  string `json:"host"`
								Extra string `json:"extra"`
							} `json:"url_info"`
						} `json:"codec"`
					} `json:"format"`
				} `json:"stream"`
			} `json:"playurl"`
		} `json:"playurl_info"`
	}

	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return LivePlaySelection{}, err
	}

	blocked := raw.Encrypted
	for _, special := range raw.AllSpecialTypes {
		blocked = blocked || special == 203
	}
	if blocked {
		return LivePlaySelection{}, &SiteError{
			Kind:     ErrKindBlocked,
			Code:     203,
			Endpoint: fullURL,
			Message:  "该直播间已加密 (203)，不支持打开或录制",
		}
	}

	canonicalRoomID := raw.RoomID
	if canonicalRoomID <= 0 {
		canonicalRoomID = roomID
	}

	if raw.LiveStatus != 1 || raw.PlayurlInfo == nil || raw.PlayurlInfo.Playurl == nil {
		return LivePlaySelection{
			RoomID:     canonicalRoomID,
			UID:        raw.UID,
			LiveStatus: raw.LiveStatus,
		}, nil
	}

	type candidate struct {
		protocol  string
		format    string
		codec     string
		qn        int
		protoPri  int
		formatPri int
		codecPri  int
		urls      []string
	}

	if len(codecOrder) == 0 {
		codecOrder = []string{"avc", "av1", "hevc"}
	}
	codecRank := make(map[string]int, len(codecOrder))
	for i, co := range codecOrder {
		codecRank[strings.ToLower(strings.TrimSpace(co))] = len(codecOrder) - i + 10
	}

	var candidates []candidate
	for _, st := range raw.PlayurlInfo.Playurl.Stream {
		pName := strings.ToLower(strings.TrimSpace(st.ProtocolName))
		pPri := 0
		switch pName {
		case "http_hls":
			pPri = 20
		case "http_stream":
			pPri = 10
		default:
			continue
		}
		for _, fm := range st.Format {
			fName := strings.ToLower(strings.TrimSpace(fm.FormatName))
			fPri := 0
			switch {
			case pName == "http_hls" && fName == "fmp4":
				fPri = 30
			case pName == "http_stream" && fName == "flv":
				fPri = 10
			default:
				continue
			}
			for _, cd := range fm.Codec {
				cName := strings.ToLower(strings.TrimSpace(cd.CodecName))
				if fName == "flv" && cName != "avc" && cName != "hevc" {
					continue
				}
				cPri := codecRank[cName]
				var rawURLs []string
				for _, ui := range cd.URLInfo {
					host := strings.TrimRight(strings.TrimSpace(ui.Host), "/")
					if host == "" {
						continue
					}
					u := host + cd.BaseURL + ui.Extra
					rawURLs = append(rawURLs, u)
				}
				var official []string
				for _, rawURL := range prioritizeCDNURLs(rawURLs) {
					if !isMCDNHost(rawURL) {
						official = append(official, rawURL)
					}
				}
				ordered := official
				if len(ordered) == 0 {
					continue
				}
				candidates = append(candidates, candidate{
					protocol:  pName,
					format:    fName,
					codec:     cName,
					qn:        cd.CurrentQn,
					protoPri:  pPri,
					formatPri: fPri,
					codecPri:  cPri,
					urls:      ordered,
				})
			}
		}
	}

	if len(candidates) == 0 {
		return LivePlaySelection{}, &SiteError{
			Kind:     ErrKindUnavailable,
			Endpoint: fullURL,
			Message:  "这个直播间现在没有提供可录制的流格式",
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.protoPri != b.protoPri {
			return a.protoPri > b.protoPri
		}
		if a.formatPri != b.formatPri {
			return a.formatPri > b.formatPri
		}
		if a.qn != b.qn {
			return a.qn > b.qn
		}
		return a.codecPri > b.codecPri
	})

	best := candidates[0]
	qDesc := ""
	for _, d := range raw.PlayurlInfo.Playurl.GQnDesc {
		if d.Qn == best.qn && strings.TrimSpace(d.Desc) != "" {
			qDesc = strings.TrimSpace(d.Desc)
			break
		}
	}
	if qDesc == "" {
		qDesc = LiveQualityFallbackName(best.qn)
	}
	return LivePlaySelection{
		RoomID:      canonicalRoomID,
		UID:         raw.UID,
		LiveStatus:  raw.LiveStatus,
		Protocol:    best.protocol,
		Format:      best.format,
		Codec:       best.codec,
		Quality:     best.qn,
		QualityName: qDesc,
		URLs:        best.urls,
	}, nil
}
