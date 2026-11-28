package bilibili

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// SubtitleBodyItem represents a single cue in a Bilibili JSON subtitle file.
type SubtitleBodyItem struct {
	From    float64 `json:"from"`
	To      float64 `json:"to"`
	Content string  `json:"content"`
}

// SubtitleTrack holds a downloaded and converted CC subtitle track.
type SubtitleTrack struct {
	Lan         string `json:"lan"`
	LanDoc      string `json:"lanDoc"`
	SubtitleURL string `json:"-"`
	AI          bool   `json:"ai,omitempty"`
	ErrorKind   string `json:"errorKind,omitempty"`
	VTT         []byte `json:"-"`
	SRT         []byte `json:"-"`
}

// VideoChapter holds one chapter marker (看点) parsed from /x/player/wbi/v2 data.view_points.
type VideoChapter struct {
	FromSec int    `json:"fromSec"`
	ToSec   int    `json:"toSec"`
	Title   string `json:"title"`
}

// PlayerPartExtras holds subtitles and chapter markers returned by a single /x/player/wbi/v2 request.
type PlayerPartExtras struct {
	Subtitles []SubtitleTrack `json:"subtitles,omitempty"`
	Chapters  []VideoChapter  `json:"chapters,omitempty"`
}

// Subtitles fetches available CC subtitle tracks from /x/player/wbi/v2 and converts each track to WebVTT and SRT.
func (c *Client) Subtitles(ctx context.Context, bvid string, cid int64, includeAI ...bool) ([]SubtitleTrack, error) {
	allowAI := len(includeAI) == 0 || includeAI[0]
	return c.MissingSubtitles(ctx, bvid, cid, allowAI, nil)
}

// MissingSubtitles skips complete language pairs before issuing body requests.
func (c *Client) MissingSubtitles(ctx context.Context, bvid string, cid int64, includeAI bool, savedLanguages []string) ([]SubtitleTrack, error) {
	extras, err := c.MissingSubtitlesAndChapters(ctx, bvid, cid, includeAI, savedLanguages)
	return extras.Subtitles, err
}

// MissingSubtitlesAndChapters fetches subtitles and zero-extra-request chapter markers (view_points)
// from a single /x/player/wbi/v2 call, skipping subtitle body downloads for already saved languages.
func (c *Client) MissingSubtitlesAndChapters(ctx context.Context, bvid string, cid int64, includeAI bool, savedLanguages []string) (PlayerPartExtras, error) {
	if err := c.RequireAuth("/x/player/wbi/v2"); err != nil {
		return PlayerPartExtras{}, err
	}
	if bvid == "" || cid <= 0 {
		return PlayerPartExtras{}, &SiteError{
			Kind:    ErrKindBadRequest,
			Message: "获取字幕需要有效的 bvid 与 cid",
		}
	}

	params := url.Values{}
	params.Set("bvid", bvid)
	params.Set("cid", strconv.FormatInt(cid, 10))

	var raw struct {
		BVID     string `json:"bvid"`
		CID      int64  `json:"cid"`
		Subtitle struct {
			Subtitles []struct {
				ID          int64  `json:"id"`
				Lan         string `json:"lan"`
				LanDoc      string `json:"lan_doc"`
				SubtitleURL string `json:"subtitle_url"`
			} `json:"subtitles"`
		} `json:"subtitle"`
		ViewPoints []struct {
			Type    any    `json:"type"`
			From    any    `json:"from"`
			To      any    `json:"to"`
			Content string `json:"content"`
		} `json:"view_points"`
	}
	if err := c.doWBIGetWithReferer(ctx, "/x/player/wbi/v2", params, &raw, c.videoReferer(bvid)); err != nil {
		return PlayerPartExtras{}, err
	}
	if raw.BVID != bvid || raw.CID != cid {
		return PlayerPartExtras{}, &SiteError{Kind: ErrKindSchemaChanged, Message: "字幕列表与请求的分 P 不一致"}
	}

	chapters := make([]VideoChapter, 0, len(raw.ViewPoints))
	type chapterKey struct {
		from  int
		to    int
		title string
	}
	seenChapters := make(map[chapterKey]bool, len(raw.ViewPoints))
	for _, vp := range raw.ViewPoints {
		title := strings.TrimSpace(vp.Content)
		fromSec, fromOK := parseJSONInt(vp.From)
		toSec, toOK := parseJSONInt(vp.To)
		if title == "" || !fromOK || !toOK || fromSec < 0 || toSec <= fromSec {
			continue
		}
		key := chapterKey{from: fromSec, to: toSec, title: title}
		if seenChapters[key] {
			continue
		}
		seenChapters[key] = true
		chapters = append(chapters, VideoChapter{
			FromSec: fromSec,
			ToSec:   toSec,
			Title:   title,
		})
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].FromSec != chapters[j].FromSec {
			return chapters[i].FromSec < chapters[j].FromSec
		}
		return chapters[i].ToSec < chapters[j].ToSec
	})

	tracks := make([]SubtitleTrack, 0, len(raw.Subtitle.Subtitles))
	for _, sub := range raw.Subtitle.Subtitles {
		ai := strings.HasPrefix(strings.ToLower(sub.Lan), "ai-")
		if ai && !includeAI {
			continue
		}
		alreadySaved := false
		for _, language := range savedLanguages {
			if language == sub.Lan {
				alreadySaved = true
			}
		}
		if alreadySaved {
			continue
		}
		subURL := normalizeHTTPSURL(sub.SubtitleURL)
		if subURL == "" {
			continue
		}
		bodyBytes, err := c.subtitleBytes(ctx, subURL)
		if err != nil {
			if IsKind(err, ErrKindRiskControl) || IsKind(err, ErrKindRateLimited) || IsKind(err, ErrKindAuthRequired) || ctx.Err() != nil {
				return PlayerPartExtras{Subtitles: tracks, Chapters: chapters}, err
			}
			tracks = append(tracks, SubtitleTrack{Lan: sub.Lan, LanDoc: sub.LanDoc, AI: ai, ErrorKind: KindOf(err)})
			continue
		}
		var doc struct {
			Body []SubtitleBodyItem `json:"body"`
		}
		if err := json.Unmarshal(bodyBytes, &doc); err != nil {
			tracks = append(tracks, SubtitleTrack{Lan: sub.Lan, LanDoc: sub.LanDoc, AI: ai, ErrorKind: ErrKindSchemaChanged})
			continue
		}

		vtt, srt := ConvertSubtitleToVTTAndSRT(doc.Body)
		tracks = append(tracks, SubtitleTrack{
			Lan:         strings.TrimSpace(sub.Lan),
			LanDoc:      strings.TrimSpace(sub.LanDoc),
			SubtitleURL: subURL,
			AI:          ai,
			VTT:         vtt,
			SRT:         srt,
		})
	}
	return PlayerPartExtras{Subtitles: tracks, Chapters: chapters}, nil
}

// ConvertSubtitleToVTTAndSRT converts Bilibili JSON subtitle cues into WebVTT (.vtt) and SubRip (.srt).
func ConvertSubtitleToVTTAndSRT(items []SubtitleBodyItem) (vtt []byte, srt []byte) {
	var vttBuf bytes.Buffer
	var srtBuf bytes.Buffer

	vttBuf.WriteString("WEBVTT\n\n")
	idx := 1
	for _, it := range items {
		text := strings.TrimSpace(it.Content)
		if text == "" || math.IsNaN(it.From) || math.IsNaN(it.To) || math.IsInf(it.From, 0) || math.IsInf(it.To, 0) || it.To <= it.From || it.From < 0 {
			continue
		}
		// A blank line ends a cue. Preserve deliberate line breaks without letting
		// subtitle text manufacture a new timestamp block.
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
		for strings.Contains(text, "\n\n") {
			text = strings.ReplaceAll(text, "\n\n", "\n")
		}
		vttText := html.EscapeString(text)
		vttBuf.WriteString(fmt.Sprintf("%s --> %s\n%s\n\n",
			formatTimestamp(it.From, '.'),
			formatTimestamp(it.To, '.'),
			vttText,
		))
		srtBuf.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n",
			idx,
			formatTimestamp(it.From, ','),
			formatTimestamp(it.To, ','),
			text,
		))
		idx++
	}
	return vttBuf.Bytes(), srtBuf.Bytes()
}

func formatTimestamp(sec float64, millisSep byte) string {
	if sec < 0 {
		sec = 0
	}
	totalMillis := int64(sec*1000.0 + 0.5)
	ms := totalMillis % 1000
	totalSec := totalMillis / 1000
	s := totalSec % 60
	totalMin := totalSec / 60
	m := totalMin % 60
	h := totalMin / 60
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, millisSep, ms)
}

func (c *Client) subtitleBytes(ctx context.Context, rawURL string) ([]byte, error) {
	if err := c.ValidateMediaURL(rawURL); err != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "字幕地址不受支持"}
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "字幕地址不受支持"}
	}
	host := strings.ToLower(u.Hostname())
	if !(c.allowLocalhost && site.IsLoopbackHost(host)) && host != "hdslb.com" && !strings.HasSuffix(host, ".hdslb.com") {
		return nil, &SiteError{Kind: ErrKindBadRequest, Message: "字幕地址不受支持"}
	}
	endAPI, err := c.beginAPI(ctx)
	if err != nil {
		return nil, err
	}
	defer endAPI()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.profile.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	c.profile.ApplyClientHints(req.Header)
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return nil, &SiteError{Kind: ErrKindNetwork, Message: "字幕下载失败", Err: site.UnwrapURLError(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		statusErr := classifyHTTPStatus(resp.StatusCode, "subtitle")
		c.ReportAutomaticError(ctx, statusErr)
		return nil, statusErr
	}
	const limit = 5 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, &SiteError{Kind: ErrKindSchemaChanged, Message: "字幕超过5 MiB"}
	}
	return body, nil
}
