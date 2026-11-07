package bilibili

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

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
	BVID         string      `json:"bvid"`
	AID          int64       `json:"aid"`
	Title        string      `json:"title"`
	Desc         string      `json:"desc"`
	Cover        string      `json:"cover"`
	PubTs        int64       `json:"pubTs"`
	Duration     int         `json:"duration"`
	TName        string      `json:"tname,omitempty"`
	Stat         VideoStat   `json:"stat"`
	Author       AuthorInfo  `json:"author"`
	Pages        []VideoPage `json:"pages"`
	CanonicalURL string      `json:"canonicalUrl"`
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
	}

	if err := c.doWBIGet(ctx, "/x/web-interface/wbi/view", params, &raw); err != nil {
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
		Pages:        pages,
		CanonicalURL: fmt.Sprintf("https://www.bilibili.com/video/%s", raw.BVID),
	}, nil
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
