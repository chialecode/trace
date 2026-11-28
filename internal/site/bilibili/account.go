package bilibili

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Read-only lists of the signed-in account (bilibili.md §21). None of these
// calls write to the site; the caller decides when to fetch the next page.

const (
	accountPageSize    = 20
	historyPageSize    = 20
	favTypeVideo       = 2
	favTypeAudio       = 12
	favTypeOGV         = 24
	favAttrInvalidSelf = 9
	favAttrInvalid     = 1
)

// flexID accepts a JSON number or a JSON string and keeps it as decimal text,
// so 64-bit ids never pass through float64.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		*f = ""
		return nil
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return err
		}
		*f = flexID(strings.TrimSpace(s))
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(t, &n); err != nil {
		return err
	}
	*f = flexID(n.String())
	return nil
}

func (f flexID) Int64() int64 {
	v, _ := strconv.ParseInt(string(f), 10, 64)
	return v
}

// nullableData lets an endpoint treat `data: null` as an empty result instead of a schema change.
type nullableData struct{ out any }

// FavFolder is one favourites folder created by the signed-in account.
type FavFolder struct {
	ID        string `json:"id"` // media_id, used to list the folder
	Title     string `json:"title"`
	Count     int    `json:"count"`
	Private   bool   `json:"private"`
	IsDefault bool   `json:"isDefault"`
}

// FavFolders lists the folders created by mid (the signed-in account). The
// default folder comes first; the rest keep the site's order.
func (c *Client) FavFolders(ctx context.Context, mid int64) ([]FavFolder, error) {
	const ep = "/x/v3/fav/folder/created/list-all"
	if err := c.RequireAuth(ep); err != nil {
		return nil, err
	}
	if mid <= 0 && c.credStore != nil {
		mid = c.credStore.Status().Mid
	}
	if mid <= 0 {
		return nil, &SiteError{Kind: ErrKindAuthRequired, Endpoint: ep, Message: "缺少登录账号 UID，请重新登录"}
	}
	q := url.Values{}
	q.Set("up_mid", strconv.FormatInt(mid, 10))
	var raw struct {
		Count int `json:"count"`
		List  []struct {
			ID         flexID `json:"id"`
			Attr       int    `json:"attr"`
			Title      string `json:"title"`
			MediaCount int    `json:"media_count"`
		} `json:"list"`
	}
	if _, err := c.doBiliJSONWithOptions(ctx, http.MethodGet, c.apiURL(ep, q), nil, "", true, nullableData{&raw}, true, apiRequestOptions{referer: c.spaceReferer(mid)}); err != nil {
		return nil, err
	}
	out := make([]FavFolder, 0, len(raw.List))
	for _, f := range raw.List {
		if f.ID == "" {
			return nil, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "收藏夹缺少编号"}
		}
		out = append(out, FavFolder{
			ID:        string(f.ID),
			Title:     f.Title,
			Count:     f.MediaCount,
			Private:   f.Attr&1 == 1,
			IsDefault: f.Attr&2 == 0,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].IsDefault && !out[j].IsDefault })
	return out, nil
}

// Favourite item kinds, normalised from the site's numeric type.
const (
	FavKindVideo = "video"
	FavKindAudio = "audio"
	FavKindOGV   = "ogv"
	FavKindOther = "other"
)

// FavItem is one entry of a favourites folder.
type FavItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TypeLabel string `json:"typeLabel,omitempty"`
	BVID      string `json:"bvid,omitempty"`
	Title     string `json:"title"`
	Cover     string `json:"cover"`
	Duration  int    `json:"duration"`
	UpperMid  int64  `json:"upperMid"`
	UpperName string `json:"upperName"`
	Invalid   bool   `json:"invalid"`
	FavAt     int64  `json:"favAt"`
	SeasonID  string `json:"seasonId,omitempty"`
}

// FavPage is one page of a favourites folder.
type FavPage struct {
	Items   []FavItem `json:"items"`
	HasMore bool      `json:"hasMore"`
	Total   int       `json:"total"`
}

// FavResources lists page pn (from 1) of folder mediaID, newest favourite first.
func (c *Client) FavResources(ctx context.Context, mediaID string, pn int) (FavPage, error) {
	const ep = "/x/v3/fav/resource/list"
	if err := c.RequireAuth(ep); err != nil {
		return FavPage{}, err
	}
	mediaID = strings.TrimSpace(mediaID)
	if id, err := strconv.ParseInt(mediaID, 10, 64); err != nil || id <= 0 {
		return FavPage{}, &SiteError{Kind: ErrKindBadRequest, Endpoint: ep, Message: "收藏夹编号无效"}
	}
	if pn <= 0 {
		pn = 1
	}
	q := url.Values{}
	q.Set("media_id", mediaID)
	q.Set("pn", strconv.Itoa(pn))
	q.Set("ps", strconv.Itoa(accountPageSize))
	q.Set("keyword", "")
	q.Set("order", "mtime")
	q.Set("type", "0")
	q.Set("tid", "0")
	q.Set("platform", "web")
	var raw struct {
		Info struct {
			MediaCount int `json:"media_count"`
		} `json:"info"`
		Medias []struct {
			ID       flexID `json:"id"`
			Type     int    `json:"type"`
			Title    string `json:"title"`
			Cover    string `json:"cover"`
			Duration int    `json:"duration"`
			Attr     int    `json:"attr"`
			BVID     string `json:"bvid"`
			BVIDAlt  string `json:"bv_id"`
			FavTime  int64  `json:"fav_time"`
			Upper    struct {
				Mid  flexID `json:"mid"`
				Name string `json:"name"`
			} `json:"upper"`
			OGV *struct {
				TypeName string `json:"type_name"`
				SeasonID flexID `json:"season_id"`
			} `json:"ogv"`
		} `json:"medias"`
		HasMore bool `json:"has_more"`
	}
	var selfMid int64
	if c.credStore != nil {
		selfMid = c.credStore.Status().Mid
	}
	if _, err := c.doBiliJSONWithOptions(ctx, http.MethodGet, c.apiURL(ep, q), nil, "", true, &raw, true, apiRequestOptions{referer: c.spaceReferer(selfMid)}); err != nil {
		return FavPage{}, err
	}
	if raw.HasMore && len(raw.Medias) == 0 {
		return FavPage{}, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "收藏夹返回空页但仍有下一页，已停止翻页"}
	}
	page := FavPage{HasMore: raw.HasMore, Total: raw.Info.MediaCount, Items: make([]FavItem, 0, len(raw.Medias))}
	for _, m := range raw.Medias {
		bvid := m.BVID
		if bvid == "" {
			bvid = m.BVIDAlt
		}
		it := FavItem{
			ID:        string(m.ID),
			BVID:      bvid,
			Title:     m.Title,
			Cover:     normalizeHTTPSURL(m.Cover),
			Duration:  m.Duration,
			UpperMid:  m.Upper.Mid.Int64(),
			UpperName: m.Upper.Name,
			FavAt:     m.FavTime,
			Invalid:   m.Attr == favAttrInvalid || m.Attr == favAttrInvalidSelf,
		}
		switch m.Type {
		case favTypeVideo:
			it.Kind = FavKindVideo
		case favTypeAudio:
			it.Kind, it.TypeLabel = FavKindAudio, "音频"
		case favTypeOGV:
			it.Kind, it.TypeLabel = FavKindOGV, "番剧"
			if m.OGV != nil {
				if m.OGV.TypeName != "" {
					it.TypeLabel = m.OGV.TypeName
				}
				it.SeasonID = string(m.OGV.SeasonID)
			}
		default:
			it.Kind, it.TypeLabel = FavKindOther, "其他"
		}
		if it.Kind == FavKindVideo && it.BVID == "" && !it.Invalid {
			return FavPage{}, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "收藏的视频缺少 BV 号"}
		}
		page.Items = append(page.Items, it)
	}
	return page, nil
}

// ToViewItem is one entry of 稍后再看.
type ToViewItem struct {
	AID         int64  `json:"aid"`
	BVID        string `json:"bvid"`
	Title       string `json:"title"`
	Cover       string `json:"cover"`
	Duration    int    `json:"duration"`
	OwnerMid    int64  `json:"ownerMid"`
	OwnerName   string `json:"ownerName"`
	Progress    int    `json:"progress"` // seconds; meaningful when !Finished
	Finished    bool   `json:"finished"`
	Unsupported bool   `json:"unsupported"` // 番剧或课程
	AddAt       int64  `json:"addAt"`
}

// ToViewPage is one page of 稍后再看.
type ToViewPage struct {
	Items   []ToViewItem `json:"items"`
	Total   int          `json:"total"`
	HasMore bool         `json:"hasMore"`
}

// ToView lists page pn (from 1) of 稍后再看; unfinishedOnly selects 未看完 (viewed=2).
func (c *Client) ToView(ctx context.Context, pn int, unfinishedOnly bool) (ToViewPage, error) {
	const ep = "/x/v2/history/toview/web"
	if err := c.RequireAuth(ep); err != nil {
		return ToViewPage{}, err
	}
	if pn <= 0 {
		pn = 1
	}
	q := url.Values{}
	q.Set("pn", strconv.Itoa(pn))
	q.Set("ps", strconv.Itoa(accountPageSize))
	q.Set("viewed", "0")
	if unfinishedOnly {
		q.Set("viewed", "2")
	}
	q.Set("key", "")
	q.Set("asc", "false")
	q.Set("need_split", "true")
	q.Set("web_location", "333.881")
	var raw struct {
		Count int `json:"count"`
		List  []struct {
			AID      flexID `json:"aid"`
			BVID     string `json:"bvid"`
			Title    string `json:"title"`
			Pic      string `json:"pic"`
			Duration int    `json:"duration"`
			Progress int    `json:"progress"`
			IsPGC    bool   `json:"is_pgc"`
			IsPUGV   bool   `json:"is_pugv"`
			AddAt    int64  `json:"add_at"`
			Owner    struct {
				Mid  flexID `json:"mid"`
				Name string `json:"name"`
			} `json:"owner"`
		} `json:"list"`
	}
	if err := c.doWBIGet(ctx, ep, q, nullableData{&raw}); err != nil {
		return ToViewPage{}, err
	}
	fetched := (pn-1)*accountPageSize + len(raw.List)
	page := ToViewPage{Total: raw.Count, HasMore: len(raw.List) > 0 && fetched < raw.Count, Items: make([]ToViewItem, 0, len(raw.List))}
	if len(raw.List) == 0 && (pn-1)*accountPageSize < raw.Count {
		return ToViewPage{}, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "稍后再看返回空页但总数显示还有，已停止翻页"}
	}
	for _, v := range raw.List {
		it := ToViewItem{
			AID:         v.AID.Int64(),
			BVID:        v.BVID,
			Title:       v.Title,
			Cover:       normalizeHTTPSURL(v.Pic),
			Duration:    v.Duration,
			OwnerMid:    v.Owner.Mid.Int64(),
			OwnerName:   v.Owner.Name,
			Progress:    v.Progress,
			Finished:    v.Progress == -1 || (v.Duration > 0 && v.Progress >= v.Duration),
			Unsupported: v.IsPGC || v.IsPUGV,
			AddAt:       v.AddAt,
		}
		if it.Progress < 0 {
			it.Progress = 0
		}
		if !it.Unsupported && it.BVID == "" {
			return ToViewPage{}, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "稍后再看的视频缺少 BV 号"}
		}
		page.Items = append(page.Items, it)
	}
	return page, nil
}

// HistoryCursor is the three-part cursor of history/cursor. The zero value is the first page.
type HistoryCursor struct {
	Max      int64  `json:"max"`
	ViewAt   int64  `json:"viewAt"`
	Business string `json:"business"`
}

// IsZero reports whether the cursor is the first page.
func (h HistoryCursor) IsZero() bool { return h.Max == 0 && h.ViewAt == 0 && h.Business == "" }

// History item businesses TRACE distinguishes.
const (
	HistoryArchive = "archive"
	HistoryLive    = "live"
	HistoryPGC     = "pgc"
	HistoryArticle = "article"
	HistoryCheese  = "cheese"
)

// HistoryItem is one entry of 历史记录.
type HistoryItem struct {
	Business   string `json:"business"`
	TypeLabel  string `json:"typeLabel,omitempty"`
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	BVID       string `json:"bvid,omitempty"`
	Page       int    `json:"page,omitempty"`
	OID        int64  `json:"oid"`
	AuthorMid  int64  `json:"authorMid"`
	AuthorName string `json:"authorName"`
	ViewAt     int64  `json:"viewAt"`
	Progress   int    `json:"progress"`
	Duration   int    `json:"duration"`
	Finished   bool   `json:"finished"`
	Live       bool   `json:"live"`
	URI        string `json:"uri,omitempty"`
}

// HistoryPage is one page of 历史记录.
type HistoryPage struct {
	Items   []HistoryItem `json:"items"`
	Next    HistoryCursor `json:"next"`
	HasMore bool          `json:"hasMore"`
}

// WatchHistory lists one page of 历史记录 (type=all) after cursor.
func (c *Client) WatchHistory(ctx context.Context, cursor HistoryCursor) (HistoryPage, error) {
	const ep = "/x/web-interface/history/cursor"
	if err := c.RequireAuth(ep); err != nil {
		return HistoryPage{}, err
	}
	q := url.Values{}
	q.Set("type", "all")
	q.Set("ps", strconv.Itoa(historyPageSize))
	q.Set("max", strconv.FormatInt(cursor.Max, 10))
	q.Set("view_at", strconv.FormatInt(cursor.ViewAt, 10))
	if cursor.Business != "" {
		q.Set("business", cursor.Business)
	}
	var raw struct {
		Cursor struct {
			Max      int64  `json:"max"`
			ViewAt   int64  `json:"view_at"`
			Business string `json:"business"`
		} `json:"cursor"`
		List []struct {
			Title      string   `json:"title"`
			Cover      string   `json:"cover"`
			Covers     []string `json:"covers"`
			AuthorName string   `json:"author_name"`
			AuthorMid  flexID   `json:"author_mid"`
			ViewAt     int64    `json:"view_at"`
			Progress   int      `json:"progress"`
			Duration   int      `json:"duration"`
			Badge      string   `json:"badge"`
			LiveStatus int      `json:"live_status"`
			URI        string   `json:"uri"`
			History    struct {
				OID      flexID `json:"oid"`
				BVID     string `json:"bvid"`
				Page     int    `json:"page"`
				Business string `json:"business"`
			} `json:"history"`
		} `json:"list"`
	}
	if _, err := c.doBiliJSON(ctx, http.MethodGet, c.apiURL(ep, q), nil, "", true, &raw); err != nil {
		return HistoryPage{}, err
	}
	next := HistoryCursor{Max: raw.Cursor.Max, ViewAt: raw.Cursor.ViewAt, Business: raw.Cursor.Business}
	page := HistoryPage{Next: next, Items: make([]HistoryItem, 0, len(raw.List))}
	if len(raw.List) == 0 || next.Max == 0 {
		page.HasMore = false
	} else {
		page.HasMore = true
		if !cursor.IsZero() && next == cursor {
			return HistoryPage{}, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "历史记录的翻页位置没有前进，已停止翻页"}
		}
	}
	for _, v := range raw.List {
		cover := v.Cover
		if cover == "" && len(v.Covers) > 0 {
			cover = v.Covers[0]
		}
		it := HistoryItem{
			Business:   v.History.Business,
			Title:      v.Title,
			Cover:      normalizeHTTPSURL(cover),
			BVID:       v.History.BVID,
			Page:       v.History.Page,
			OID:        v.History.OID.Int64(),
			AuthorMid:  v.AuthorMid.Int64(),
			AuthorName: v.AuthorName,
			ViewAt:     v.ViewAt,
			Progress:   v.Progress,
			Duration:   v.Duration,
			Finished:   v.Progress == -1,
			Live:       v.LiveStatus == 1,
			URI:        v.URI,
		}
		if it.Progress < 0 {
			it.Progress = 0
		}
		switch it.Business {
		case HistoryArchive:
			if it.BVID == "" {
				return HistoryPage{}, &SiteError{Kind: ErrKindSchemaChanged, Endpoint: ep, Message: "历史记录的视频缺少 BV 号"}
			}
		case HistoryLive:
			it.TypeLabel = "直播"
		case HistoryPGC:
			it.TypeLabel = firstNonEmpty(v.Badge, "番剧")
		case HistoryArticle, "article-list":
			it.TypeLabel = "专栏"
		case HistoryCheese:
			it.TypeLabel = "课程"
		default:
			it.TypeLabel = firstNonEmpty(v.Badge, "其他")
		}
		page.Items = append(page.Items, it)
	}
	return page, nil
}

func (c *Client) apiURL(path string, q url.Values) string {
	return strings.TrimRight(c.endpoints.APIBase, "/") + path + "?" + q.Encode()
}
