package bilibili

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// UpowerLockedMessage explains why an unpurchased charging-exclusive video cannot be cached.
const UpowerLockedMessage = "充电专属：当前登录账号未开通该 UP 主充电权益，暂不支持缓存"

// NewUpowerLockedError constructs a structured ErrKindForbidden error before playurl is requested.
func NewUpowerLockedError(endpoint string) *SiteError {
	return &SiteError{
		Site:     site.Bilibili,
		Kind:     ErrKindForbidden,
		Endpoint: site.SanitizeEndpoint(endpoint),
		Message:  UpowerLockedMessage,
	}
}

type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch string(trimmed) {
	case "true", "1", `"true"`, `"1"`:
		*b = true
	case "false", "0", "null", `"false"`, `"0"`, `""`:
		*b = false
	default:
		*b = false
	}
	return nil
}

// AuthorInfo holds basic creator metadata.
type AuthorInfo struct {
	Mid  int64  `json:"mid"`
	Name string `json:"name"`
	Face string `json:"face"`
}

// VideoPage describes a single P (part) of a Bilibili video.
type VideoPage struct {
	CID      int64  `json:"cid"`
	Page     int    `json:"page"`
	Title    string `json:"title"`
	Duration int    `json:"duration"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// VideoStat holds interaction counters for a Bilibili video.
type VideoStat struct {
	View     int64 `json:"view"`
	Danmaku  int64 `json:"danmaku"`
	Reply    int64 `json:"reply"`
	Favorite int64 `json:"favorite"`
	Coin     int64 `json:"coin"`
	Share    int64 `json:"share"`
	Like     int64 `json:"like"`
}

// VideoDetail holds metadata and part list for a Bilibili video.
type VideoDetail struct {
	BVID              string       `json:"bvid"`
	AID               int64        `json:"aid"`
	Title             string       `json:"title"`
	Desc              string       `json:"desc"`
	Cover             string       `json:"cover"`
	PubTs             int64        `json:"pubTs"`
	Duration          int          `json:"duration"`
	TName             string       `json:"tname,omitempty"`
	Stat              VideoStat    `json:"stat"`
	Author            AuthorInfo   `json:"author"`
	Staff             []AuthorInfo `json:"staff,omitempty"` // collaboration members (联合投稿), uploader included; empty for single-author videos
	Pages             []VideoPage  `json:"pages"`
	IsUpowerExclusive bool         `json:"isUpowerExclusive,omitempty"`
	IsUpowerPlay      bool         `json:"isUpowerPlay,omitempty"`
	IsUpowerPreview   bool         `json:"isUpowerPreview,omitempty"`
	CanonicalURL      string       `json:"canonicalUrl"`
}

// IsUpowerLocked reports whether this video is charging-exclusive and the signed-in account lacks full playback access.
func (d VideoDetail) IsUpowerLocked() bool {
	return d.IsUpowerExclusive && !d.IsUpowerPlay
}

// IsUpowerUnlocked reports whether this video is charging-exclusive and unlocked for the signed-in account.
func (d VideoDetail) IsUpowerUnlocked() bool {
	return d.IsUpowerExclusive && d.IsUpowerPlay
}

// VideoDetail fetches video details from /x/web-interface/wbi/view.
func (c *Client) VideoDetail(ctx context.Context, bvidOrAid string) (VideoDetail, error) {
	if err := c.RequireAuth("/x/web-interface/wbi/view"); err != nil {
		return VideoDetail{}, err
	}

	params := url.Values{}
	trimmed := strings.TrimSpace(bvidOrAid)
	if strings.HasPrefix(strings.ToLower(trimmed), "bv1") {
		params.Set("bvid", normalizeBVID(trimmed))
	} else if strings.HasPrefix(strings.ToLower(trimmed), "av") {
		params.Set("aid", strings.TrimPrefix(strings.ToLower(trimmed), "av"))
	} else if rePureNum.MatchString(trimmed) {
		params.Set("aid", trimmed)
	} else {
		params.Set("bvid", trimmed)
	}

	var raw struct {
		BVID     string `json:"bvid"`
		AID      int64  `json:"aid"`
		Title    string `json:"title"`
		Desc     string `json:"desc"`
		Pic      string `json:"pic"`
		PubDate  int64  `json:"pubdate"`
		Duration int    `json:"duration"`
		TName    string `json:"tname"`
		CID      int64  `json:"cid"`
		Stat     struct {
			View     int64 `json:"view"`
			Danmaku  int64 `json:"danmaku"`
			Reply    int64 `json:"reply"`
			Favorite int64 `json:"favorite"`
			Coin     int64 `json:"coin"`
			Share    int64 `json:"share"`
			Like     int64 `json:"like"`
		} `json:"stat"`
		Dimension struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"dimension"`
		Owner struct {
			Mid  int64  `json:"mid"`
			Name string `json:"name"`
			Face string `json:"face"`
		} `json:"owner"`
		Staff []rawVideoStaff `json:"staff"`
		Pages []struct {
			CID       int64  `json:"cid"`
			Page      int    `json:"page"`
			Part      string `json:"part"`
			Duration  int    `json:"duration"`
			Dimension struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"dimension"`
		} `json:"pages"`
		IsUpowerExclusive flexBool `json:"is_upower_exclusive"`
		IsUpowerPlay      flexBool `json:"is_upower_play"`
		IsUpowerPreview   flexBool `json:"is_upower_preview"`
	}

	if err := c.doWBIGetWithReferer(ctx, "/x/web-interface/wbi/view", params, &raw, c.videoReferer(params.Get("bvid"))); err != nil {
		return VideoDetail{}, err
	}
	if raw.BVID == "" && raw.AID > 0 {
		raw.BVID = AvToBv(raw.AID)
	}
	if raw.BVID == "" {
		return VideoDetail{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: "/x/web-interface/wbi/view",
			Message:  "视频详情响应缺少 bvid",
		}
	}

	pages := make([]VideoPage, 0, max(1, len(raw.Pages)))
	for _, p := range raw.Pages {
		partTitle := strings.TrimSpace(p.Part)
		if partTitle == "" {
			partTitle = raw.Title
		}
		pages = append(pages, VideoPage{
			CID:      p.CID,
			Page:     p.Page,
			Title:    partTitle,
			Duration: p.Duration,
			Width:    p.Dimension.Width,
			Height:   p.Dimension.Height,
		})
	}
	if len(pages) == 0 && raw.CID > 0 {
		pages = append(pages, VideoPage{
			CID:      raw.CID,
			Page:     1,
			Title:    raw.Title,
			Duration: raw.Duration,
			Width:    raw.Dimension.Width,
			Height:   raw.Dimension.Height,
		})
	}

	return VideoDetail{
		BVID:     raw.BVID,
		AID:      raw.AID,
		Title:    raw.Title,
		Desc:     raw.Desc,
		Cover:    normalizeHTTPSURL(raw.Pic),
		PubTs:    raw.PubDate,
		Duration: raw.Duration,
		TName:    raw.TName,
		Stat: VideoStat{
			View:     raw.Stat.View,
			Danmaku:  raw.Stat.Danmaku,
			Reply:    raw.Stat.Reply,
			Favorite: raw.Stat.Favorite,
			Coin:     raw.Stat.Coin,
			Share:    raw.Stat.Share,
			Like:     raw.Stat.Like,
		},
		Author: AuthorInfo{
			Mid:  raw.Owner.Mid,
			Name: raw.Owner.Name,
			Face: normalizeHTTPSURL(raw.Owner.Face),
		},
		Staff:             staffInfos(raw.Staff),
		Pages:             pages,
		IsUpowerExclusive: bool(raw.IsUpowerExclusive),
		IsUpowerPlay:      bool(raw.IsUpowerPlay),
		IsUpowerPreview:   bool(raw.IsUpowerPreview),
		CanonicalURL:      fmt.Sprintf("https://www.bilibili.com/video/%s", raw.BVID),
	}, nil
}

type rawVideoStaff struct {
	Mid  int64  `json:"mid"`
	Name string `json:"name"`
	Face string `json:"face"`
}

func staffInfos(raw []rawVideoStaff) []AuthorInfo {
	var out []AuthorInfo
	for _, m := range raw {
		if m.Mid <= 0 {
			continue
		}
		out = append(out, AuthorInfo{Mid: m.Mid, Name: m.Name, Face: normalizeHTTPSURL(m.Face)})
	}
	return out
}

// HasStaffMember reports whether mid is the uploader or a collaboration member.
func (d VideoDetail) HasStaffMember(mid int64) bool {
	if mid <= 0 {
		return false
	}
	if d.Author.Mid == mid {
		return true
	}
	for _, m := range d.Staff {
		if m.Mid == mid {
			return true
		}
	}
	return false
}

// StaffMember returns the collaboration member with mid, if any.
func (d VideoDetail) StaffMember(mid int64) (AuthorInfo, bool) {
	for _, m := range d.Staff {
		if m.Mid == mid {
			return m, true
		}
	}
	return AuthorInfo{}, false
}

func normalizeHTTPSURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(raw, "http://") && !strings.Contains(raw, "127.0.0.1") && !strings.Contains(raw, "localhost") {
		return "https://" + strings.TrimPrefix(raw, "http://")
	}
	return raw
}

func parseDurationText(length string) int {
	parts := strings.Split(strings.TrimSpace(length), ":")
	total := 0
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return total
}
