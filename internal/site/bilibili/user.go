package bilibili

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

var reSpaceRenderData = regexp.MustCompile(`<script\s+id=["']__RENDER_DATA__["'][^>]*>([^<]+)</script>`)

// UserSpace holds creator profile metadata fetched from /x/web-interface/card.
type UserSpace struct {
	Mid          int64  `json:"mid"`
	Name         string `json:"name"`
	Sign         string `json:"sign"`
	Face         string `json:"face"`
	TopPhoto     string `json:"topPhoto,omitempty"`
	Level        int    `json:"level"`
	Fans         int    `json:"fans"`
	Friend       int    `json:"friend"`
	ArchiveCount int    `json:"archiveCount"`
	Following    bool   `json:"following"`
	CanonicalURL string `json:"canonicalUrl"`
}

// UserVideoItem represents an item in a creator's uploaded videos list.
type UserVideoItem struct {
	BVID         string `json:"bvid"`
	AID          int64  `json:"aid"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Cover        string `json:"cover"`
	Length       string `json:"length"`
	DurationSec  int    `json:"durationSec"`
	PubTs        int64  `json:"pubTs"`
	PlayCount    int    `json:"playCount"`
	CommentCount int    `json:"commentCount"`
	Mid          int64  `json:"mid"`
	Author       string `json:"author"`
}

// UserVideoZone describes one partition bucket in /x/space/wbi/arc/search data.list.tlist.
type UserVideoZone struct {
	TID   int    `json:"tid"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// UserVideosPage holds a paginated slice of creator videos.
type UserVideosPage struct {
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	Total    int             `json:"total"`
	Items    []UserVideoItem `json:"items"`
	Zones    []UserVideoZone `json:"zones,omitempty"`
}

// UserCard fetches creator profile info from /x/web-interface/card.
func (c *Client) UserCard(ctx context.Context, mid int64) (UserSpace, error) {
	if err := c.RequireAuth("/x/web-interface/card"); err != nil {
		return UserSpace{}, err
	}
	if mid <= 0 {
		return UserSpace{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "无效的用户 UID",
		}
	}

	q := url.Values{}
	q.Set("mid", strconv.FormatInt(mid, 10))
	q.Set("photo", "true")
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/web-interface/card?" + q.Encode()

	var raw struct {
		Card struct {
			Mid       json.Number `json:"mid"`
			Name      string      `json:"name"`
			Sign      string      `json:"sign"`
			Face      string      `json:"face"`
			Fans      int         `json:"fans"`
			Friend    int         `json:"friend"`
			Attention int         `json:"attention"`
			LevelInfo struct {
				CurrentLevel int `json:"current_level"`
			} `json:"level_info"`
		} `json:"card"`
		Space struct {
			SImg string `json:"s_img"`
			LImg string `json:"l_img"`
		} `json:"space"`
		Follower     int  `json:"follower"`
		ArchiveCount int  `json:"archive_count"`
		Following    bool `json:"following"`
	}

	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return UserSpace{}, err
	}

	parsedMid, _ := raw.Card.Mid.Int64()
	if parsedMid <= 0 {
		parsedMid = mid
	}
	if parsedMid != mid {
		return UserSpace{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: "/x/web-interface/card",
			Message:  "返回的用户 mid 与请求不一致",
		}
	}
	topPhoto := raw.Space.LImg
	if topPhoto == "" {
		topPhoto = raw.Space.SImg
	}
	fans := raw.Card.Fans
	if fans == 0 && raw.Follower > 0 {
		fans = raw.Follower
	}
	friend := raw.Card.Friend
	if friend == 0 && raw.Card.Attention > 0 {
		friend = raw.Card.Attention
	}

	return UserSpace{
		Mid:          parsedMid,
		Name:         raw.Card.Name,
		Sign:         raw.Card.Sign,
		Face:         normalizeHTTPSURL(raw.Card.Face),
		TopPhoto:     normalizeHTTPSURL(topPhoto),
		Level:        raw.Card.LevelInfo.CurrentLevel,
		Fans:         fans,
		Friend:       friend,
		ArchiveCount: raw.ArchiveCount,
		Following:    raw.Following,
		CanonicalURL: fmt.Sprintf("https://space.bilibili.com/%d", parsedMid),
	}, nil
}

// SetSpaceWebID sets a cached w_webid for a creator mid (for tests or pre-populated state).
func (c *Client) SetSpaceWebID(mid int64, webID string) {
	c.webIDMu.Lock()
	defer c.webIDMu.Unlock()
	if c.webIDCache == nil {
		c.webIDCache = make(map[int64]cachedWebID)
	}
	c.webIDCache[mid] = cachedWebID{
		Value:     webID,
		FetchedAt: c.nowFunc(),
	}
}

// ExtractSpaceAccessID extracts and validates access_id from space page HTML (__RENDER_DATA__).
func ExtractSpaceAccessID(html string) string {
	m := reSpaceRenderData.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	decoded, err := url.QueryUnescape(strings.TrimSpace(m[1]))
	if err != nil {
		return ""
	}
	var payload struct {
		AccessID string `json:"access_id"`
	}
	if err := json.Unmarshal([]byte(decoded), &payload); err != nil {
		return ""
	}
	id := strings.TrimSpace(payload.AccessID)
	if !isValidSpaceAccessID(id) {
		return ""
	}
	return id
}

func isValidSpaceAccessID(id string) bool {
	if id == "" || len(id) > 600 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '-' || ch == '_' || ch == '=' {
			continue
		}
		return false
	}
	return true
}

func (c *Client) ensureSpaceWebID(ctx context.Context, mid int64) (string, error) {
	now := c.nowFunc()
	c.webIDMu.Lock()
	if c.webIDCache == nil {
		c.webIDCache = make(map[int64]cachedWebID)
	}
	if entry, ok := c.webIDCache[mid]; ok && now.Sub(entry.FetchedAt) < 30*time.Minute {
		c.webIDMu.Unlock()
		return entry.Value, nil
	}
	c.webIDMu.Unlock()

	spaceBase := strings.TrimRight(c.endpoints.SpaceBase, "/")
	var webID string
	if spaceBase != "" && (strings.Contains(spaceBase, "127.0.0.1") || strings.Contains(spaceBase, "localhost") || !c.allowLocalhost) {
		spaceURL := fmt.Sprintf("%s/%d", spaceBase, mid)
		_, raw, err := c.doAPIRequest(ctx, http.MethodGet, spaceURL, nil, "", true)
		if err != nil {
			if ctx.Err() != nil || site.IsRoundStop(err) || site.IsKind(err, ErrKindQuota) || site.IsKind(err, ErrKindBackoff) {
				c.ReportAutomaticError(ctx, err)
				return "", err
			}
			// Non-fatal space HTML miss (e.g. 404): degrade without poisoning cache.
			return "", nil
		}
		if len(raw) > 0 {
			if len(raw) > 2*1024*1024 {
				raw = raw[:2*1024*1024]
			}
			webID = ExtractSpaceAccessID(string(raw))
		}
	}

	c.webIDMu.Lock()
	if len(c.webIDCache) >= 16 {
		var oldestMid int64
		var oldestTime time.Time
		for k, v := range c.webIDCache {
			if oldestMid == 0 || v.FetchedAt.Before(oldestTime) {
				oldestMid = k
				oldestTime = v.FetchedAt
			}
		}
		if oldestMid != 0 {
			delete(c.webIDCache, oldestMid)
		}
	}
	c.webIDCache[mid] = cachedWebID{
		Value:     webID,
		FetchedAt: now,
	}
	c.webIDMu.Unlock()
	return webID, nil
}

// NormalizeUserVideosOrder normalizes a Bilibili space archive sort key to one of
// "pubdate" (latest), "click" (most played), or "stow" (most favorited).
func NormalizeUserVideosOrder(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "click":
		return "click"
	case "stow":
		return "stow"
	default:
		return "pubdate"
	}
}

func clampInt64ToInt(v int64) int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int(v)
}

func parseJSONInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case nil:
		return 0, false
	case int:
		return clampInt64ToInt(int64(v)), true
	case int64:
		return clampInt64ToInt(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		if v > float64(math.MaxInt32) {
			return math.MaxInt32, true
		}
		if v < float64(math.MinInt32) {
			return math.MinInt32, true
		}
		return int(v), true
	case json.Number:
		s := strings.TrimSpace(v.String())
		if s == "" {
			return 0, false
		}
		if iv, err := strconv.ParseInt(s, 10, 64); err == nil {
			return clampInt64ToInt(iv), true
		}
		if fv, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(fv) && !math.IsInf(fv, 0) {
			if fv > float64(math.MaxInt32) {
				return math.MaxInt32, true
			}
			if fv < float64(math.MinInt32) {
				return math.MinInt32, true
			}
			return int(fv), true
		}
		return 0, false
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		if iv, err := strconv.ParseInt(s, 10, 64); err == nil {
			return clampInt64ToInt(iv), true
		}
		if fv, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(fv) && !math.IsInf(fv, 0) {
			if fv > float64(math.MaxInt32) {
				return math.MaxInt32, true
			}
			if fv < float64(math.MinInt32) {
				return math.MinInt32, true
			}
			return int(fv), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func parseUserVideoZones(raw json.RawMessage) []UserVideoZone {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '{' {
		return nil
	}
	var bucket map[string]struct {
		TID   any    `json:"tid"`
		Name  string `json:"name"`
		Count any    `json:"count"`
	}
	if err := json.Unmarshal(trimmed, &bucket); err != nil || len(bucket) == 0 {
		return nil
	}
	zones := make([]UserVideoZone, 0, len(bucket))
	seenTID := make(map[int]bool, len(bucket))
	for key, entry := range bucket {
		tid, ok := parseJSONInt(entry.TID)
		if !ok || tid <= 0 {
			tid, ok = parseJSONInt(key)
		}
		name := strings.TrimSpace(entry.Name)
		if !ok || tid <= 0 || name == "" || seenTID[tid] {
			continue
		}
		count, _ := parseJSONInt(entry.Count)
		if count < 0 {
			count = 0
		}
		seenTID[tid] = true
		zones = append(zones, UserVideoZone{
			TID:   tid,
			Name:  name,
			Count: count,
		})
	}
	sort.SliceStable(zones, func(i, j int) bool {
		if zones[i].Count != zones[j].Count {
			return zones[i].Count > zones[j].Count
		}
		return zones[i].TID < zones[j].TID
	})
	return zones
}

// UserVideos fetches a page of creator videos from /x/space/wbi/arc/search with required WBI, dm_img, and w_webid parameters.
// An optional tid (> 0) filters results to a specific partition; 0 (or omitted) returns all partitions.
func (c *Client) UserVideos(ctx context.Context, mid int64, pn, ps int, order, keyword string, tid ...int) (UserVideosPage, error) {
	if err := c.RequireAuth("/x/space/wbi/arc/search"); err != nil {
		return UserVideosPage{}, err
	}
	if mid <= 0 {
		return UserVideosPage{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "无效的用户 UID",
		}
	}
	if pn <= 0 {
		pn = 1
	}
	if ps <= 0 || ps > 50 {
		ps = 30
	}
	order = NormalizeUserVideosOrder(order)
	keyword = strings.TrimSpace(keyword)
	zoneTID := 0
	if len(tid) > 0 && tid[0] > 0 {
		zoneTID = tid[0]
	}

	params := url.Values{}
	params.Set("mid", strconv.FormatInt(mid, 10))
	params.Set("pn", strconv.Itoa(pn))
	params.Set("ps", strconv.Itoa(ps))
	params.Set("tid", strconv.Itoa(zoneTID))
	params.Set("order", order)
	params.Set("keyword", keyword)
	params.Set("platform", "web")
	params.Set("web_location", "1550101")
	params.Set("order_avoided", "true")
	params.Set("dm_img_list", "[]")
	params.Set("dm_img_str", "bm8gd2ViZ2")
	params.Set("dm_cover_img_str", "bm8gd2ViZ2")
	params.Set("dm_img_inter", `{"ds":[],"wh":[0,0,0],"of":[0,0,0]}`)
	webID, err := c.ensureSpaceWebID(ctx, mid)
	if err != nil {
		return UserVideosPage{}, err
	}
	if webID != "" {
		params.Set("w_webid", webID)
	}

	var raw struct {
		List struct {
			TList json.RawMessage `json:"tlist"`
			VList []struct {
				AID         int64  `json:"aid"`
				BVID        string `json:"bvid"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Pic         string `json:"pic"`
				Length      string `json:"length"`
				Created     int64  `json:"created"`
				Play        any    `json:"play"`
				Comment     any    `json:"comment"`
				Mid         int64  `json:"mid"`
				Author      string `json:"author"`
			} `json:"vlist"`
		} `json:"list"`
		Page struct {
			PN    any `json:"pn"`
			PS    any `json:"ps"`
			Count any `json:"count"`
		} `json:"page"`
	}

	if err := c.doWBIGet(ctx, "/x/space/wbi/arc/search", params, &raw); err != nil {
		return UserVideosPage{}, err
	}

	seenBVID := make(map[string]bool, len(raw.List.VList))
	items := make([]UserVideoItem, 0, len(raw.List.VList))
	for _, v := range raw.List.VList {
		bvid := v.BVID
		if bvid == "" && v.AID > 0 {
			bvid = AvToBv(v.AID)
		}
		if bvid != "" {
			if seenBVID[bvid] {
				continue
			}
			seenBVID[bvid] = true
		}
		playCount, _ := parseJSONInt(v.Play)
		if playCount < 0 {
			playCount = 0
		}
		commentCount, _ := parseJSONInt(v.Comment)
		if commentCount < 0 {
			commentCount = 0
		}
		items = append(items, UserVideoItem{
			BVID:         bvid,
			AID:          v.AID,
			Title:        stripHTMLTags(v.Title),
			Description:  v.Description,
			Cover:        normalizeHTTPSURL(v.Pic),
			Length:       v.Length,
			DurationSec:  parseDurationText(v.Length),
			PubTs:        v.Created,
			PlayCount:    playCount,
			CommentCount: commentCount,
			Mid:          v.Mid,
			Author:       v.Author,
		})
	}

	pageNum, _ := parseJSONInt(raw.Page.PN)
	pageSize, _ := parseJSONInt(raw.Page.PS)
	totalCount, _ := parseJSONInt(raw.Page.Count)
	if totalCount < 0 {
		totalCount = 0
	}

	return UserVideosPage{
		Page:     pageNum,
		PageSize: pageSize,
		Total:    totalCount,
		Items:    items,
		Zones:    parseUserVideoZones(raw.List.TList),
	}, nil
}

func stripHTMLTags(s string) string {
	for {
		start := strings.IndexByte(s, '<')
		if start < 0 {
			return s
		}
		end := strings.IndexByte(s[start:], '>')
		if end < 0 {
			return s
		}
		s = s[:start] + s[start+end+1:]
	}
}
