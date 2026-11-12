package douyin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

// Work kinds normalized by the Douyin parser.
const (
	WorkKindVideo       = "video"
	WorkKindNote        = "note"
	WorkKindUnsupported = "unsupported"
)

// Mixed image-note page kinds (douyin.md §6.4 & §11).
const (
	NotePageImage     = "image"
	NotePageLivePhoto = "live_photo"
	NotePageVideo     = "video"
	NotePageUnknown   = "unknown"
)

// StreamSelectOptions controls Douyin VOD stream selection (DEC-043: resolution tier first, then codec).
type StreamSelectOptions struct {
	PreferredCodec string // "avc" (default) or "hevc"
}

// SelectedVODStream describes a normalized non-watermark progressive MP4 stream candidate.
type SelectedVODStream struct {
	Tier         int      `json:"tier"`
	QualityLabel string   `json:"qualityLabel"`
	GearName     string   `json:"gearName,omitempty"`
	Codec        string   `json:"codec"` // "avc" or "hevc"
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	FPS          int      `json:"fps,omitempty"`
	BitRate      int64    `json:"bitRate,omitempty"`
	DataSize     int64    `json:"dataSize,omitempty"`
	DurationSec  int      `json:"durationSec,omitempty"`
	URLs         []string `json:"-"`
}

// NotePage represents one ordered page of a Douyin image/mixed note post.
type NotePage struct {
	Index         int                `json:"index"` // 1-based
	Kind          string             `json:"kind"`  // NotePageImage, NotePageLivePhoto, NotePageVideo, NotePageUnknown
	Width         int                `json:"width,omitempty"`
	Height        int                `json:"height,omitempty"`
	ImageURLs     []string           `json:"-"`
	VideoStream   *SelectedVODStream `json:"videoStream,omitempty"`
	UnknownKind   bool               `json:"unknownKind,omitempty"`
	UnknownReason string             `json:"unknownReason,omitempty"`
}

// MusicInfo holds background music or original sound metadata attached to a Douyin post.
type MusicInfo struct {
	ID              string   `json:"id,omitempty"`
	Title           string   `json:"title,omitempty"`
	Author          string   `json:"author,omitempty"`
	DurationSec     int      `json:"durationSec,omitempty"`
	IsOriginalSound bool     `json:"isOriginalSound"`
	CoverURL        string   `json:"coverUrl,omitempty"`
	PlayURLs        []string `json:"-"`
}

// WorkStats holds online engagement counts for a Douyin work.
// Note: Douyin Web's statistics.play_count is always a placeholder 0 and is intentionally
// omitted (PlayCount is nil) so TRACE never displays "0 plays" (douyin.md §6.1 & §11).
type WorkStats struct {
	DiggCount    int64  `json:"diggCount"`
	CommentCount int64  `json:"commentCount"`
	CollectCount int64  `json:"collectCount"`
	ShareCount   int64  `json:"shareCount"`
	PlayCount    *int64 `json:"playCount,omitempty"`
}

// AuthorSummary holds normalized creator metadata embedded in works and user profiles.
type AuthorSummary struct {
	SecUID    string `json:"secUid"`
	UID       string `json:"uid,omitempty"`
	UniqueID  string `json:"uniqueId,omitempty"`
	ShortID   string `json:"shortId,omitempty"`
	Nickname  string `json:"nickname"`
	Signature string `json:"signature,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	RoomID    string `json:"roomId,omitempty"`
	WebRID    string `json:"webRid,omitempty"`
}

// WorkDetail represents a normalized Douyin video or image/mixed note post.
type WorkDetail struct {
	ID                string              `json:"id"`
	Kind              string              `json:"kind"` // WorkKindVideo, WorkKindNote, WorkKindUnsupported
	AwemeType         int                 `json:"awemeType"`
	Title             string              `json:"title"`
	Desc              string              `json:"desc"`
	CreateTime        int64               `json:"createTime"`
	DurationSec       int                 `json:"durationSec,omitempty"`
	IsTop             bool                `json:"isTop,omitempty"`
	PrivateStatus     int                 `json:"privateStatus,omitempty"`
	Author            AuthorSummary       `json:"author"`
	Stats             WorkStats           `json:"stats"`
	CoverURLs         []string            `json:"-"`
	Video             *SelectedVODStream  `json:"video,omitempty"`
	AvailableStreams  []SelectedVODStream `json:"availableStreams,omitempty"`
	OriginalURLs      []string            `json:"-"` // untranscoded source (原画) via the play redirector
	Pages             []NotePage          `json:"pages,omitempty"`
	Music             *MusicInfo          `json:"music,omitempty"`
	Hashtags          []string            `json:"hashtags,omitempty"`
	Uncacheable       bool                `json:"uncacheable,omitempty"`
	UncacheableReason string              `json:"uncacheableReason,omitempty"`
	CanonicalURL      string              `json:"canonicalUrl"`
}

type rawURLContainer struct {
	URI      string      `json:"uri"`
	URLList  []string    `json:"url_list"`
	Width    int         `json:"width"`
	Height   int         `json:"height"`
	DataSize json.Number `json:"data_size"`
}

type rawDouyinAuthor struct {
	UID            json.RawMessage  `json:"uid"`
	SecUID         string           `json:"sec_uid"`
	ShortID        string           `json:"short_id"`
	UniqueID       string           `json:"unique_id"`
	Nickname       string           `json:"nickname"`
	Signature      string           `json:"signature"`
	AvatarLarger   *rawURLContainer `json:"avatar_larger"`
	AvatarMedium   *rawURLContainer `json:"avatar_medium"`
	AvatarThumb    *rawURLContainer `json:"avatar_thumb"`
	AwemeCount     int              `json:"aweme_count"`
	FollowerCount  int64            `json:"follower_count"`
	FollowingCount int64            `json:"following_count"`
	TotalFavorited json.RawMessage  `json:"total_favorited"`
	Secret         int              `json:"secret"`
	LiveStatus     int              `json:"live_status"`
	RoomID         json.RawMessage  `json:"room_id"`
	RoomIDStr      string           `json:"room_id_str"`
	RoomData       string           `json:"room_data"`
}

func (a *rawDouyinAuthor) uidString() string {
	if a == nil {
		return ""
	}
	return rawNumberOrString(a.UID)
}

func (a *rawDouyinAuthor) roomIDString() string {
	if a == nil {
		return ""
	}
	if s := strings.TrimSpace(a.RoomIDStr); s != "" && s != "0" {
		return s
	}
	if s := rawNumberOrString(a.RoomID); s != "" && s != "0" {
		return s
	}
	return ""
}

func (a *rawDouyinAuthor) embeddedWebRID() string {
	if a == nil || strings.TrimSpace(a.RoomData) == "" {
		return ""
	}
	var rd struct {
		Owner struct {
			WebRID string `json:"web_rid"`
		} `json:"owner"`
	}
	if err := json.Unmarshal([]byte(a.RoomData), &rd); err == nil {
		return strings.TrimSpace(rd.Owner.WebRID)
	}
	return ""
}

type rawBitRateEntry struct {
	GearName    string           `json:"gear_name"`
	QualityType int              `json:"quality_type"`
	BitRate     int64            `json:"bit_rate"`
	IsBytevc1   int              `json:"is_bytevc1"`
	IsH265      int              `json:"is_h265"`
	FPS         int              `json:"FPS"`
	FPSLower    int              `json:"fps"`
	Format      string           `json:"format"`
	PlayAddr    *rawURLContainer `json:"play_addr"`
}

type rawVideoNode struct {
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	Duration     int64             `json:"duration"`
	Format       string            `json:"format"`
	IsBytevc1    int               `json:"is_bytevc1"`
	EncryptType  int               `json:"encrypt_type"`
	PlayAddr     *rawURLContainer  `json:"play_addr"`
	PlayAddrH264 *rawURLContainer  `json:"play_addr_h264"`
	PlayAddr265  *rawURLContainer  `json:"play_addr_265"`
	BitRate      []rawBitRateEntry `json:"bit_rate"`
	OriginCover  *rawURLContainer  `json:"origin_cover"`
	Cover        *rawURLContainer  `json:"cover"`
	// Note: download_addr & download_suffix_logo_addr are intentionally excluded (watermarked).
}

type rawNoteImage struct {
	URI             string        `json:"uri"`
	Width           int           `json:"width"`
	Height          int           `json:"height"`
	URLList         []string      `json:"url_list"`
	DownloadURLList []string      `json:"download_url_list"` // Watermarked — never selected!
	LivePhotoType   int           `json:"live_photo_type"`
	ClipType        int           `json:"clip_type"`
	Video           *rawVideoNode `json:"video"`
}

type rawMusicNode struct {
	ID              json.RawMessage  `json:"id"`
	IDStr           string           `json:"id_str"`
	Title           string           `json:"title"`
	Author          string           `json:"author"`
	Duration        int              `json:"duration"`
	IsOriginalSound bool             `json:"is_original_sound"`
	IsOriginal      bool             `json:"is_original"` // Musician flag — never used as IsOriginalSound!
	CoverHD         *rawURLContainer `json:"cover_hd"`
	CoverLarge      *rawURLContainer `json:"cover_large"`
	CoverMedium     *rawURLContainer `json:"cover_medium"`
	PlayURL         *rawURLContainer `json:"play_url"`
}

type rawAwemeNode struct {
	AwemeID       string           `json:"aweme_id"`
	AwemeType     int              `json:"aweme_type"`
	MediaType     int              `json:"media_type"`
	Desc          string           `json:"desc"`
	CreateTime    int64            `json:"create_time"`
	Duration      int64            `json:"duration"`
	IsTop         int              `json:"is_top"`
	Author        *rawDouyinAuthor `json:"author"`
	Video         *rawVideoNode    `json:"video"`
	Images        []rawNoteImage   `json:"images"`
	ImagePostInfo *struct {
		Images []rawNoteImage `json:"images"`
	} `json:"image_post_info"`
	Music      *rawMusicNode `json:"music"`
	Statistics *struct {
		DiggCount    int64 `json:"digg_count"`
		CommentCount int64 `json:"comment_count"`
		CollectCount int64 `json:"collect_count"`
		ShareCount   int64 `json:"share_count"`
		PlayCount    int64 `json:"play_count"`
	} `json:"statistics"`
	Status *struct {
		IsDelete      bool `json:"is_delete"`
		PrivateStatus int  `json:"private_status"`
		IsProhibited  bool `json:"is_prohibited"`
		InReviewing   bool `json:"in_reviewing"`
	} `json:"status"`
	ChargeInfo *struct {
		IsChargeContent bool `json:"is_charge_content"`
		HasPaid         bool `json:"has_paid"`
	} `json:"charge_info"`
	TextExtra []struct {
		HashtagName string `json:"hashtag_name"`
	} `json:"text_extra"`
}

type awemeDetailEnvelope struct {
	StatusCode   int           `json:"status_code"`
	StatusMsg    string        `json:"status_msg"`
	AwemeDetail  *rawAwemeNode `json:"aweme_detail"`
	FilterDetail *struct {
		AwemeID      string `json:"aweme_id"`
		FilterReason string `json:"filter_reason"`
		Notice       string `json:"notice"`
		DetailMsg    string `json:"detail_msg"`
	} `json:"filter_detail"`
}

// GetWorkDetail fetches and parses a single Douyin video or mixed image-note post via aweme/detail.
func (c *Client) GetWorkDetail(ctx context.Context, awemeID string, opts StreamSelectOptions) (*WorkDetail, error) {
	awemeID = strings.TrimSpace(awemeID)
	if !reWorkIDDigits.MatchString(awemeID) {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音作品 ID",
		}
	}

	params := c.profile.CommonWebParams(c.webID, "", sign.Pair{Key: "aweme_id", Value: awemeID})
	rawResp, err := c.doAPIRequest(ctx, apiRequestSpec{
		endpoint:    c.endpoints.AwemeDetail,
		referer:     DefaultReferer,
		params:      params,
		requireSign: true,
	})
	if err != nil {
		return nil, err
	}

	var env awemeDetailEnvelope
	dec := json.NewDecoder(bytes.NewReader(rawResp.body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.AwemeDetail,
			Message:  "解析抖音作品详情 JSON 失败",
			Err:      err,
		}
	}

	// Distinguish business filter_detail vs silent risk-control empty aweme_detail (douyin.md §6.1 & §9.1).
	if env.AwemeDetail == nil {
		if env.FilterDetail != nil {
			reason := firstNonEmpty(env.FilterDetail.DetailMsg, env.FilterDetail.Notice, env.FilterDetail.FilterReason)
			if reason != "" {
				return nil, &site.Error{
					Site:     site.Douyin,
					Kind:     site.ErrKindUnavailable,
					Endpoint: c.endpoints.AwemeDetail,
					Message:  "该抖音作品当前不可见或已被删除（" + reason + "）",
				}
			}
		}
		se := &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindRiskControl,
			Endpoint: c.endpoints.AwemeDetail,
			Message:  "抖音作品详情载荷为空，疑似触发风控拦截",
		}
		c.ReportAutomaticError(ctx, se)
		return nil, se
	}

	if env.AwemeDetail.AwemeID != "" && env.AwemeDetail.AwemeID != awemeID {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: c.endpoints.AwemeDetail,
			Message:  "抖音返回的作品 ID 与请求不一致",
		}
	}
	if env.AwemeDetail.Status != nil && (env.AwemeDetail.Status.IsDelete || env.AwemeDetail.Status.IsProhibited) {
		return nil, &site.Error{
			Site:     site.Douyin,
			Kind:     site.ErrKindUnavailable,
			Endpoint: c.endpoints.AwemeDetail,
			Message:  "该抖音作品已被删除或下架",
		}
	}

	detail, pErr := parseAwemeWork(env.AwemeDetail, opts, c.allowLocalhost, true)
	if pErr != nil {
		return nil, pErr
	}
	if detail.Kind == WorkKindVideo && detail.Video != nil && !detail.Uncacheable {
		detail.OriginalURLs = originalSourceURLs(c.endpoints.PlayOriginal, env.AwemeDetail.Video)
	}
	return detail, nil
}

func parseAwemeWork(node *rawAwemeNode, opts StreamSelectOptions, allowLocalhost bool, strictUnsupported bool) (*WorkDetail, error) {
	if node == nil {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindSchemaChanged,
			Message: "作品节点为空",
		}
	}
	id := strings.TrimSpace(node.AwemeID)
	desc := strings.TrimSpace(node.Desc)
	title := deriveWorkTitle(desc, id)

	author := toAuthorSummary(node.Author, allowLocalhost)
	var stats WorkStats
	if node.Statistics != nil {
		stats = WorkStats{
			DiggCount:    node.Statistics.DiggCount,
			CommentCount: node.Statistics.CommentCount,
			CollectCount: node.Statistics.CollectCount,
			ShareCount:   node.Statistics.ShareCount,
			PlayCount:    nil, // Never expose placeholder 0 play_count!
		}
	}

	var privStatus int
	if node.Status != nil {
		privStatus = node.Status.PrivateStatus
	}

	var hashtags []string
	seenTags := make(map[string]bool)
	for _, te := range node.TextExtra {
		tag := strings.TrimSpace(te.HashtagName)
		if tag != "" && !seenTags[tag] {
			seenTags[tag] = true
			hashtags = append(hashtags, tag)
		}
	}

	uncacheable, uncacheableReason := detectPaidOrEncrypted(node)
	music := parseMusicNode(node.Music, allowLocalhost)

	work := &WorkDetail{
		ID:                id,
		AwemeType:         node.AwemeType,
		Title:             title,
		Desc:              desc,
		CreateTime:        node.CreateTime,
		IsTop:             node.IsTop == 1,
		PrivateStatus:     privStatus,
		Author:            author,
		Stats:             stats,
		Music:             music,
		Hashtags:          hashtags,
		Uncacheable:       uncacheable,
		UncacheableReason: uncacheableReason,
	}

	// CRITICAL RULE (douyin.md §6.1, §6.4, §11):
	// Check images[] (or image_post_info.images[]) FIRST before checking video.play_addr!
	// On note posts, top-level video.play_addr carries background music audio, NOT video!
	rawImages := node.Images
	if len(rawImages) == 0 && node.ImagePostInfo != nil {
		rawImages = node.ImagePostInfo.Images
	}

	if len(rawImages) > 0 {
		work.Kind = WorkKindNote
		work.CanonicalURL = "https://www.douyin.com/note/" + id
		pages := make([]NotePage, 0, len(rawImages))
		for idx, rawImg := range rawImages {
			page, err := parseNotePage(idx+1, rawImg, opts, allowLocalhost)
			if err != nil {
				return nil, err
			}
			pages = append(pages, page)
		}
		work.Pages = pages
		if len(pages) > 0 && len(pages[0].ImageURLs) > 0 {
			work.CoverURLs = append([]string(nil), pages[0].ImageURLs...)
		} else if node.Video != nil {
			work.CoverURLs = extractCoverURLs(node.Video, allowLocalhost)
		}
		return work, nil
	}

	// Non-note work: check embedded live-room card (aweme_type == 101) before video.
	if node.AwemeType == 101 {
		work.Kind = WorkKindUnsupported
		work.CanonicalURL = "https://www.douyin.com/video/" + id
		if strictUnsupported {
			return nil, &site.Error{
				Site:        site.Douyin,
				Kind:        site.ErrKindBadRequest,
				Unsupported: true,
				Message:     "暂不支持的作品类型（直播卡片），请在浏览器中打开",
			}
		}
		return work, nil
	}

	if node.Video != nil {
		work.CoverURLs = extractCoverURLs(node.Video, allowLocalhost)
		work.DurationSec = normalizeDurationSec(node.Video.Duration, node.Duration)
		candidates := collectVODStreams(node.Video, opts, allowLocalhost)
		if len(candidates) > 0 {
			work.Kind = WorkKindVideo
			work.CanonicalURL = "https://www.douyin.com/video/" + id
			best := candidates[0]
			if best.DurationSec == 0 {
				best.DurationSec = work.DurationSec
			}
			work.Video = &best
			work.AvailableStreams = candidates
			return work, nil
		}
		if uncacheable {
			work.Kind = WorkKindVideo
			work.CanonicalURL = "https://www.douyin.com/video/" + id
			return work, nil
		}
	}

	work.Kind = WorkKindUnsupported
	work.CanonicalURL = "https://www.douyin.com/video/" + id
	if strictUnsupported {
		return nil, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBadRequest,
			Unsupported: true,
			Message:     "暂不支持的作品类型，请在浏览器中打开",
		}
	}
	return work, nil
}

func parseNotePage(pageIndex int, img rawNoteImage, opts StreamSelectOptions, allowLocalhost bool) (NotePage, error) {
	imgURLs := filterNoteImageURLs(img.URLList, allowLocalhost)

	// 1. Live Photo page: live_photo_type == 1 -> MUST carry valid motion clip in img.Video!
	if img.LivePhotoType == 1 {
		if img.Video == nil {
			return NotePage{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindSchemaChanged,
				Message: fmt.Sprintf("实况照片第 %d 页缺少动态视频节点", pageIndex),
			}
		}
		streams := collectVODStreams(img.Video, opts, allowLocalhost)
		if len(streams) == 0 {
			return NotePage{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindSchemaChanged,
				Message: fmt.Sprintf("实况照片第 %d 页缺少无水印动态视频流", pageIndex),
			}
		}
		best := streams[0]
		if len(imgURLs) == 0 {
			imgURLs = extractCoverURLs(img.Video, allowLocalhost)
		}
		return NotePage{
			Index:       pageIndex,
			Kind:        NotePageLivePhoto,
			Width:       firstPositive(img.Width, best.Width),
			Height:      firstPositive(img.Height, best.Height),
			ImageURLs:   imgURLs,
			VideoStream: &best,
		}, nil
	}

	// 2. Mixed note video page: live_photo_type != 1 with non-empty img.Video playable stream.
	if img.Video != nil {
		streams := collectVODStreams(img.Video, opts, allowLocalhost)
		if len(streams) > 0 {
			best := streams[0]
			if len(imgURLs) == 0 {
				imgURLs = extractCoverURLs(img.Video, allowLocalhost)
			}
			return NotePage{
				Index:       pageIndex,
				Kind:        NotePageVideo,
				Width:       firstPositive(best.Width, img.Width),
				Height:      firstPositive(best.Height, img.Height),
				ImageURLs:   imgURLs,
				VideoStream: &best,
			}, nil
		}
	}

	if len(imgURLs) == 0 {
		return NotePage{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindSchemaChanged,
			Message: fmt.Sprintf("图文作品第 %d 页缺少无水印原图地址", pageIndex),
		}
	}

	// 3. Standard static image vs 4. Unrecognized page subtype fallback (save image + mark unknownKind).
	isKnownStatic := img.LivePhotoType == 0 && (img.ClipType == 0 || img.ClipType == 1 || img.ClipType == 2)
	if !isKnownStatic {
		return NotePage{
			Index:         pageIndex,
			Kind:          NotePageUnknown,
			Width:         img.Width,
			Height:        img.Height,
			ImageURLs:     imgURLs,
			UnknownKind:   true,
			UnknownReason: fmt.Sprintf("未识别图文页子类型(live_photo_type=%d,clip_type=%d)，已保留图片", img.LivePhotoType, img.ClipType),
		}, nil
	}

	return NotePage{
		Index:     pageIndex,
		Kind:      NotePageImage,
		Width:     img.Width,
		Height:    img.Height,
		ImageURLs: imgURLs,
	}, nil
}

// SelectStreamFromVideoNode selects the best non-watermark progressive MP4 stream per DEC-043.
func SelectStreamFromVideoNode(v *rawVideoNode, opts StreamSelectOptions, allowLocalhost bool) (*SelectedVODStream, error) {
	streams := collectVODStreams(v, opts, allowLocalhost)
	if len(streams) == 0 {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "未找到可用的无水印视频流",
		}
	}
	best := streams[0]
	return &best, nil
}

func collectVODStreams(v *rawVideoNode, opts StreamSelectOptions, allowLocalhost bool) []SelectedVODStream {
	if v == nil || v.EncryptType != 0 {
		return nil
	}

	prefCodec := strings.ToLower(strings.TrimSpace(opts.PreferredCodec))
	if prefCodec != "hevc" && prefCodec != "h265" {
		prefCodec = "avc"
	} else {
		prefCodec = "hevc"
	}

	durSec := normalizeDurationSec(v.Duration, 0)
	var out []SelectedVODStream

	for _, br := range v.BitRate {
		// Exclude proprietary ByteVC2 (is_bytevc1 == 2) per DEC-043 & douyin.md §6.5.
		if br.IsBytevc1 == 2 {
			continue
		}
		if br.PlayAddr == nil {
			continue
		}
		urls := filterAndOrderVODURLs(br.PlayAddr.URLList, allowLocalhost)
		if len(urls) == 0 {
			continue
		}

		w := firstPositive(br.PlayAddr.Width, v.Width)
		h := firstPositive(br.PlayAddr.Height, v.Height)
		tier, label := classifyResolutionTier(w, h, br.GearName, br.QualityType)
		codec := classifyVODCodec(br.IsBytevc1, br.IsH265, br.GearName)
		fps := firstPositive(br.FPS, br.FPSLower)
		dataSize, _ := br.PlayAddr.DataSize.Int64()

		out = append(out, SelectedVODStream{
			Tier:         tier,
			QualityLabel: label,
			GearName:     br.GearName,
			Codec:        codec,
			Width:        w,
			Height:       h,
			FPS:          fps,
			BitRate:      br.BitRate,
			DataSize:     dataSize,
			DurationSec:  durSec,
			URLs:         urls,
		})
	}

	// Fallback when bit_rate[] is absent (e.g. Live Photo clip or older work).
	if len(out) == 0 {
		appendFallback := func(addr *rawURLContainer, codec string, bytevc1 int) {
			if addr == nil || bytevc1 == 2 {
				return
			}
			urls := filterAndOrderVODURLs(addr.URLList, allowLocalhost)
			if len(urls) == 0 {
				return
			}
			w := firstPositive(addr.Width, v.Width)
			h := firstPositive(addr.Height, v.Height)
			tier, label := classifyResolutionTier(w, h, "", 0)
			sz, _ := addr.DataSize.Int64()
			out = append(out, SelectedVODStream{
				Tier:         tier,
				QualityLabel: label,
				Codec:        codec,
				Width:        w,
				Height:       h,
				DataSize:     sz,
				DurationSec:  durSec,
				URLs:         urls,
			})
		}
		appendFallback(v.PlayAddrH264, "avc", 0)
		appendFallback(v.PlayAddr265, "hevc", 1)
		if len(out) == 0 {
			codec := classifyVODCodec(v.IsBytevc1, 0, "")
			appendFallback(v.PlayAddr, codec, v.IsBytevc1)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Tier != b.Tier {
			return a.Tier > b.Tier
		}
		aPref := a.Codec == prefCodec
		bPref := b.Codec == prefCodec
		if aPref != bPref {
			return aPref
		}
		if a.FPS != b.FPS {
			return a.FPS > b.FPS
		}
		if a.BitRate != b.BitRate {
			return a.BitRate > b.BitRate
		}
		return a.DataSize > b.DataSize
	})

	return out
}

func classifyVODCodec(isBytevc1, isH265 int, gearName string) string {
	g := strings.ToLower(gearName)
	if isBytevc1 == 1 || isH265 == 1 || strings.Contains(g, "bytevc1") || strings.Contains(g, "bvc1") || strings.Contains(g, "265") || strings.Contains(g, "hevc") {
		return "hevc"
	}
	return "avc"
}

func classifyResolutionTier(width, height int, gearName string, qualityType int) (int, string) {
	shortEdge := width
	if height > 0 && (shortEdge == 0 || height < shortEdge) {
		shortEdge = height
	}
	g := strings.ToLower(gearName)

	switch {
	case shortEdge >= 2000 || strings.Contains(g, "2160") || strings.Contains(g, "4k"):
		return 2160, "4K"
	case shortEdge >= 1350 || strings.Contains(g, "1440") || strings.Contains(g, "2k"):
		return 1440, "2K"
	case shortEdge >= 960 || strings.Contains(g, "1080") || qualityType == 2 || qualityType == 10:
		return 1080, "1080P"
	case shortEdge >= 680 || strings.Contains(g, "720") || qualityType == 1 || qualityType == 20:
		return 720, "720P"
	case shortEdge >= 500 || strings.Contains(g, "540"):
		return 540, "540P"
	case shortEdge >= 400 || strings.Contains(g, "480"):
		return 480, "480P"
	default:
		if shortEdge > 0 {
			return 360, "360P"
		}
		return 720, "720P"
	}
}

func filterAndOrderVODURLs(raw []string, allowLocalhost bool) []string {
	type scoredURL struct {
		u     string
		score int
	}
	var items []scoredURL
	seen := make(map[string]bool)

	for _, item := range raw {
		uStr := upgradeDouyinHTTPS(item, allowLocalhost)
		if uStr == "" || seen[uStr] {
			continue
		}
		lower := strings.ToLower(uStr)
		// Exclude watermarked playwm or watermark=1 URLs (douyin.md §6.5).
		if strings.Contains(lower, "/playwm/") || strings.Contains(lower, "watermark=1") || strings.Contains(lower, "tplv-dy-water") {
			continue
		}
		parsed, err := url.Parse(uStr)
		if err != nil || parsed.Host == "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if isEncryptedQuery(parsed) {
			continue
		}

		// Rank direct CDN (douyinvod.com / zjcdn.com non-cold) > cold CDN > other CDN > /aweme/v1/play/ redirector.
		score := 2
		switch {
		case strings.Contains(strings.ToLower(parsed.Path), "/aweme/v1/play"):
			score = 4
		case strings.Contains(host, "-mc-cold"):
			score = 3
		case site.MatchDomainSuffix(host, []string{"douyinvod.com", "zjcdn.com"}) || (allowLocalhost && isLoopbackHost(host)):
			score = 1
		}
		seen[uStr] = true
		items = append(items, scoredURL{u: uStr, score: score})
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].score < items[j].score
	})

	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.u
	}
	return out
}

func filterNoteImageURLs(raw []string, allowLocalhost bool) []string {
	var nonHEIC []string
	var heicOnly []string
	seen := make(map[string]bool)

	for _, item := range raw {
		uStr := upgradeDouyinHTTPS(item, allowLocalhost)
		if uStr == "" || seen[uStr] {
			continue
		}
		lower := strings.ToLower(uStr)
		if strings.Contains(lower, "tplv-dy-water") || strings.Contains(lower, "watermark") {
			continue
		}
		parsed, err := url.Parse(uStr)
		if err != nil || parsed.Host == "" {
			continue
		}
		seen[uStr] = true
		pathLower := strings.ToLower(parsed.Path)
		if strings.HasSuffix(pathLower, ".heic") || strings.Contains(pathLower, ".heic~") {
			heicOnly = append(heicOnly, uStr)
		} else {
			nonHEIC = append(nonHEIC, uStr)
		}
	}
	return append(nonHEIC, heicOnly...)
}

func extractCoverURLs(v *rawVideoNode, allowLocalhost bool) []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, box := range []*rawURLContainer{v.OriginCover, v.Cover} {
		if box == nil {
			continue
		}
		out = append(out, filterNoteImageURLs(box.URLList, allowLocalhost)...)
	}
	return dedupStrings(out)
}

func parseMusicNode(m *rawMusicNode, allowLocalhost bool) *MusicInfo {
	if m == nil {
		return nil
	}
	id := strings.TrimSpace(m.IDStr)
	if id == "" {
		id = rawNumberOrString(m.ID)
	}
	var playURLs []string
	if m.PlayURL != nil {
		for _, u := range m.PlayURL.URLList {
			norm := upgradeDouyinHTTPS(u, allowLocalhost)
			if norm != "" {
				playURLs = append(playURLs, norm)
			}
		}
		playURLs = dedupStrings(playURLs)
	}
	var cover string
	for _, c := range []*rawURLContainer{m.CoverHD, m.CoverLarge, m.CoverMedium} {
		if c != nil {
			list := filterNoteImageURLs(c.URLList, allowLocalhost)
			if len(list) > 0 {
				cover = list[0]
				break
			}
		}
	}
	title := strings.TrimSpace(m.Title)
	author := strings.TrimSpace(m.Author)
	if id == "" && title == "" && len(playURLs) == 0 {
		return nil
	}
	return &MusicInfo{
		ID:              id,
		Title:           title,
		Author:          author,
		DurationSec:     m.Duration,
		IsOriginalSound: m.IsOriginalSound,
		CoverURL:        cover,
		PlayURLs:        playURLs,
	}
}

var reVideoURI = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// originalSourceURLs builds the play redirector URL for the untranscoded source
// (原画): ratio=default with watermark=0. It is the same public, watermark-free
// play endpoint the web player falls back to; it carries no Cookie and no
// signature. Empty when the redirector is disabled or play_addr.uri is unusable.
func originalSourceURLs(playOriginal string, v *rawVideoNode) []string {
	playOriginal = strings.TrimSpace(playOriginal)
	if playOriginal == "" || v == nil || v.PlayAddr == nil {
		return nil
	}
	uri := strings.TrimSpace(v.PlayAddr.URI)
	if !reVideoURI.MatchString(uri) {
		return nil
	}
	q := url.Values{}
	q.Set("video_id", uri)
	q.Set("ratio", "default")
	q.Set("improve_bitrate", "1")
	q.Set("watermark", "0")
	q.Set("line", "0")
	return []string{playOriginal + "?" + q.Encode()}
}

func detectPaidOrEncrypted(node *rawAwemeNode) (bool, string) {
	if node == nil {
		return false, ""
	}
	if node.ChargeInfo != nil && node.ChargeInfo.IsChargeContent && !node.ChargeInfo.HasPaid {
		return true, "该作品为付费内容且当前账号未购买，不支持缓存"
	}
	if node.Video != nil {
		if node.Video.EncryptType != 0 {
			return true, "该作品视频流已加密，不支持缓存"
		}
		if node.Video.PlayAddr != nil {
			for _, u := range node.Video.PlayAddr.URLList {
				if parsed, err := url.Parse(u); err == nil && isEncryptedQuery(parsed) {
					return true, "该作品视频流已加密，不支持缓存"
				}
			}
		}
	}
	return false, ""
}

func isEncryptedQuery(u *url.URL) bool {
	if u == nil {
		return false
	}
	q := u.Query()
	return q.Get("drm_type") != "" || q.Get("encryption") == "1" || strings.Contains(strings.ToLower(u.RawQuery), "cenc")
}

func toAuthorSummary(a *rawDouyinAuthor, allowLocalhost bool) AuthorSummary {
	if a == nil {
		return AuthorSummary{}
	}
	uniqueID := strings.TrimSpace(a.UniqueID)
	shortID := strings.TrimSpace(a.ShortID)
	if uniqueID == "" && shortID != "" && shortID != "0" {
		uniqueID = shortID
	}
	return AuthorSummary{
		SecUID:    strings.TrimSpace(a.SecUID),
		UID:       strings.TrimSpace(a.uidString()),
		UniqueID:  uniqueID,
		ShortID:   shortID,
		Nickname:  strings.TrimSpace(a.Nickname),
		Signature: strings.TrimSpace(a.Signature),
		AvatarURL: pickAuthorAvatar(a, allowLocalhost),
		RoomID:    a.roomIDString(),
		WebRID:    a.embeddedWebRID(),
	}
}

func pickAuthorAvatar(a *rawDouyinAuthor, allowLocalhost bool) string {
	if a == nil {
		return ""
	}
	for _, box := range []*rawURLContainer{a.AvatarLarger, a.AvatarMedium, a.AvatarThumb} {
		if box == nil {
			continue
		}
		urls := filterNoteImageURLs(box.URLList, allowLocalhost)
		if len(urls) > 0 {
			return urls[0]
		}
	}
	return ""
}

func deriveWorkTitle(desc, fallbackID string) string {
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if utf8.RuneCountInString(trimmed) > 80 {
				runes := []rune(trimmed)
				return string(runes[:80]) + "…"
			}
			return trimmed
		}
	}
	if fallbackID != "" {
		return "抖音作品 " + fallbackID
	}
	return "抖音作品"
}

func normalizeDurationSec(videoMs, outerMs int64) int {
	v := videoMs
	if v <= 0 {
		v = outerMs
	}
	if v <= 0 {
		return 0
	}
	if v >= 1000 {
		return int((v + 500) / 1000)
	}
	return int(v)
}

func rawNumberOrString(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	if dec.Decode(&n) == nil {
		return strings.TrimSpace(n.String())
	}
	return ""
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func dedupStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func strconvFormatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
