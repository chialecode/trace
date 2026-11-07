package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DynamicMedia describes an image attached to a dynamic post.
type DynamicMedia struct {
	URL    string  `json:"url"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	SizeKB float64 `json:"sizeKb,omitempty"`
	// LiveURL is the motion clip of a live photo (实况); the image is its
	// still frame.
	LiveURL string `json:"liveUrl,omitempty"`
}

// DynamicVideoRef describes a video referenced inside a dynamic post.
type DynamicVideoRef struct {
	BVID         string `json:"bvid"`
	AID          int64  `json:"aid,omitempty"`
	Title        string `json:"title"`
	Cover        string `json:"cover"`
	Desc         string `json:"desc,omitempty"`
	DurationText string `json:"durationText,omitempty"`
	DurationSec  int    `json:"durationSec,omitempty"`
}

// DynamicItem represents a parsed Bilibili dynamic post.
type DynamicItem struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	TypeLabel    string            `json:"typeLabel"`
	Author       AuthorInfo        `json:"author"`
	PubTs        int64             `json:"pubTs"`
	Title        string            `json:"title"`
	Text         string            `json:"text"`
	Images       []DynamicMedia    `json:"images,omitempty"`
	Videos       []DynamicVideoRef `json:"videos,omitempty"`
	Origin       *DynamicItem      `json:"origin,omitempty"`
	Pinned       bool              `json:"pinned,omitempty"`
	Degraded     bool              `json:"degraded,omitempty"`
	CanonicalURL string            `json:"canonicalUrl"`
}

// UserDynamicsPage represents a page of user space dynamics.
type UserDynamicsPage struct {
	HasMore bool          `json:"hasMore"`
	Offset  string        `json:"offset"`
	Items   []DynamicItem `json:"items"`
}

// UserDynamics fetches a page of user dynamics from /x/polymer/web-dynamic/v1/feed/space.
func (c *Client) UserDynamics(ctx context.Context, hostMid int64, offset string) (UserDynamicsPage, error) {
	if err := c.RequireAuth("/x/polymer/web-dynamic/v1/feed/space"); err != nil {
		return UserDynamicsPage{}, err
	}
	if hostMid <= 0 {
		return UserDynamicsPage{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "无效的用户 UID",
		}
	}

	params := url.Values{}
	params.Set("host_mid", strconv.FormatInt(hostMid, 10))
	params.Set("offset", offset)
	params.Set("timezone_offset", "-480")
	params.Set("features", "itemOpusStyle,listOnlyfans,opusBigCover,onlyfansVote")

	var raw struct {
		HasMore bool              `json:"has_more"`
		Offset  string            `json:"offset"`
		Items   []rawDynamicEntry `json:"items"`
	}
	if err := c.doWBIGet(ctx, "/x/polymer/web-dynamic/v1/feed/space", params, &raw); err != nil {
		return UserDynamicsPage{}, err
	}

	items := make([]DynamicItem, 0, len(raw.Items))
	for _, entry := range raw.Items {
		parsed := parseDynamicEntry(entry)
		if parsed.ID != "" {
			items = append(items, parsed)
		}
	}
	return UserDynamicsPage{
		HasMore: raw.HasMore,
		Offset:  raw.Offset,
		Items:   items,
	}, nil
}

// DynamicDetail fetches a single dynamic post from /x/polymer/web-dynamic/v1/detail.
func (c *Client) DynamicDetail(ctx context.Context, dynID string) (DynamicItem, error) {
	if err := c.RequireAuth("/x/polymer/web-dynamic/v1/detail"); err != nil {
		return DynamicItem{}, err
	}
	dynID = strings.TrimSpace(dynID)
	if dynID == "" {
		return DynamicItem{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "动态 ID 不能为空",
		}
	}

	q := url.Values{}
	q.Set("id", dynID)
	q.Set("timezone_offset", "-480")
	q.Set("features", "itemOpusStyle,opusBigCover")
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/polymer/web-dynamic/v1/detail?" + q.Encode()

	var raw struct {
		Item rawDynamicEntry `json:"item"`
	}
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return DynamicItem{}, err
	}
	item := parseDynamicEntry(raw.Item)
	if item.ID == "" {
		return DynamicItem{}, &SiteError{
			Kind:     ErrKindSchemaChanged,
			Endpoint: fullURL,
			Message:  "动态详情响应缺少 id_str",
		}
	}
	return item, nil
}

type rawDynamicEntry struct {
	IDStr   string          `json:"id_str"`
	Type    string          `json:"type"`
	Origin  json.RawMessage `json:"orig"`
	Modules struct {
		ModuleTag *struct {
			Text string `json:"text"`
		} `json:"module_tag"`
		ModuleAuthor struct {
			Mid   int64       `json:"mid"`
			Name  string      `json:"name"`
			Face  string      `json:"face"`
			PubTs json.Number `json:"pub_ts"`
		} `json:"module_author"`
		ModuleDynamic struct {
			Desc *struct {
				Text string `json:"text"`
			} `json:"desc"`
			Major *struct {
				Type string `json:"type"`
				Opus *struct {
					Title   string `json:"title"`
					Summary struct {
						Text string `json:"text"`
					} `json:"summary"`
					Pics []struct {
						URL     string  `json:"url"`
						Width   int     `json:"width"`
						Height  int     `json:"height"`
						Size    float64 `json:"size"`
						LiveURL string  `json:"live_url"`
					} `json:"pics"`
				} `json:"opus"`
				Draw *struct {
					Items []struct {
						Src     string  `json:"src"`
						Width   int     `json:"width"`
						Height  int     `json:"height"`
						Size    float64 `json:"size"`
						LiveURL string  `json:"live_url"`
					} `json:"items"`
				} `json:"draw"`
				Archive *struct {
					AID          json.Number `json:"aid"`
					BVID         string      `json:"bvid"`
					Title        string      `json:"title"`
					Cover        string      `json:"cover"`
					Desc         string      `json:"desc"`
					DurationText string      `json:"duration_text"`
				} `json:"archive"`
				Article *struct {
					ID     int64    `json:"id"`
					Title  string   `json:"title"`
					Desc   string   `json:"desc"`
					Covers []string `json:"covers"`
				} `json:"article"`
			} `json:"major"`
		} `json:"module_dynamic"`
	} `json:"modules"`
}

func parseDynamicEntry(raw rawDynamicEntry) DynamicItem {
	pubTs, _ := raw.Modules.ModuleAuthor.PubTs.Int64()
	item := DynamicItem{
		ID:   raw.IDStr,
		Type: raw.Type,
		Author: AuthorInfo{
			Mid:  raw.Modules.ModuleAuthor.Mid,
			Name: raw.Modules.ModuleAuthor.Name,
			Face: normalizeHTTPSURL(raw.Modules.ModuleAuthor.Face),
		},
		PubTs:        pubTs,
		Pinned:       raw.Modules.ModuleTag != nil && strings.Contains(raw.Modules.ModuleTag.Text, "置顶"),
		CanonicalURL: fmt.Sprintf("https://t.bilibili.com/%s", raw.IDStr),
	}

	if raw.Modules.ModuleDynamic.Desc != nil {
		item.Text = strings.TrimSpace(raw.Modules.ModuleDynamic.Desc.Text)
	}

	major := raw.Modules.ModuleDynamic.Major
	if major != nil {
		if major.Opus != nil {
			if major.Opus.Title != "" {
				item.Title = strings.TrimSpace(major.Opus.Title)
			}
			if item.Text == "" && major.Opus.Summary.Text != "" {
				item.Text = strings.TrimSpace(major.Opus.Summary.Text)
			}
			for _, p := range major.Opus.Pics {
				if p.URL != "" {
					item.Images = append(item.Images, DynamicMedia{
						URL:     normalizeHTTPSURL(p.URL),
						Width:   p.Width,
						Height:  p.Height,
						SizeKB:  p.Size,
						LiveURL: normalizeHTTPSURL(p.LiveURL),
					})
				}
			}
		}
		if major.Draw != nil && len(item.Images) == 0 {
			for _, d := range major.Draw.Items {
				if d.Src != "" {
					item.Images = append(item.Images, DynamicMedia{
						URL:     normalizeHTTPSURL(d.Src),
						Width:   d.Width,
						Height:  d.Height,
						SizeKB:  d.Size,
						LiveURL: normalizeHTTPSURL(d.LiveURL),
					})
				}
			}
		}
		if major.Archive != nil && major.Archive.BVID != "" {
			aid, _ := major.Archive.AID.Int64()
			if item.Title == "" {
				item.Title = major.Archive.Title
			}
			item.Videos = append(item.Videos, DynamicVideoRef{
				BVID:         major.Archive.BVID,
				AID:          aid,
				Title:        major.Archive.Title,
				Cover:        normalizeHTTPSURL(major.Archive.Cover),
				Desc:         major.Archive.Desc,
				DurationText: major.Archive.DurationText,
				DurationSec:  parseDurationText(major.Archive.DurationText),
			})
		}
		if major.Article != nil {
			if item.Title == "" {
				item.Title = major.Article.Title
			}
			if item.Text == "" {
				item.Text = major.Article.Desc
			}
			for _, cov := range major.Article.Covers {
				if cov != "" {
					item.Images = append(item.Images, DynamicMedia{
						URL: normalizeHTTPSURL(cov),
					})
				}
			}
		}
	}

	switch raw.Type {
	case "DYNAMIC_TYPE_AV":
		item.TypeLabel = "视频动态"
	case "DYNAMIC_TYPE_DRAW":
		item.TypeLabel = "图文动态"
	case "DYNAMIC_TYPE_WORD":
		item.TypeLabel = "文字动态"
	case "DYNAMIC_TYPE_ARTICLE":
		item.TypeLabel = "专栏动态"
	case "DYNAMIC_TYPE_FORWARD":
		item.TypeLabel = "转发动态"
		if len(raw.Origin) > 0 && string(raw.Origin) != "null" {
			var origRaw rawDynamicEntry
			if err := json.Unmarshal(raw.Origin, &origRaw); err == nil && origRaw.IDStr != "" {
				origItem := parseDynamicEntry(origRaw)
				item.Origin = &origItem
				if len(item.Videos) == 0 && len(origItem.Videos) > 0 {
					item.Videos = append(item.Videos, origItem.Videos...)
				}
				if len(item.Images) == 0 && len(origItem.Images) > 0 {
					item.Images = append(item.Images, origItem.Images...)
				}
			}
		}
	default:
		// Graceful degradation for unsupported/future dynamic types (C12).
		item.TypeLabel = "其他动态"
		item.Degraded = true
		if item.Text == "" && item.Title == "" {
			item.Text = fmt.Sprintf("暂不支持完整展示的动态类型 (%s)", raw.Type)
		}
	}

	if item.Title == "" {
		item.Title = summarizeDynamicTitle(item)
	}
	return item
}

func summarizeDynamicTitle(item DynamicItem) string {
	if len(item.Videos) > 0 && item.Videos[0].Title != "" {
		return item.Videos[0].Title
	}
	txt := strings.TrimSpace(item.Text)
	if txt != "" {
		firstLine := strings.Split(txt, "\n")[0]
		runes := []rune(firstLine)
		if len(runes) > 36 {
			return string(runes[:36]) + "…"
		}
		return firstLine
	}
	if item.Author.Name != "" {
		return item.Author.Name + " 的" + item.TypeLabel
	}
	return item.TypeLabel
}
