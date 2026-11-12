package douyin

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// Compile-time assertions that *Adapter (and *Client via Adapter) implements all cross-site contracts.
var (
	_ site.Resolver            = (*Adapter)(nil)
	_ site.URLMatcher          = (*Adapter)(nil)
	_ site.DownloadPlanner     = (*Adapter)(nil)
	_ site.LiveProvider        = (*Adapter)(nil)
	_ site.SubscriptionFetcher = (*Adapter)(nil)
)

// Adapter wraps *Client to implement internal/site's cross-site Resolver, URLMatcher,
// DownloadPlanner, LiveProvider, and SubscriptionFetcher interfaces.
type Adapter struct {
	client *Client
}

// NewAdapter constructs a cross-site Adapter backed by client.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{client: client}
}

// Client returns the underlying Douyin protocol Client.
func (a *Adapter) Client() *Client {
	return a.client
}

// SiteID returns site.Douyin ("douyin").
func (a *Adapter) SiteID() site.ID {
	return site.Douyin
}

// MatchURL claims loopback synthetic URLs in httptest when they match Douyin routes or configured endpoints.
func (a *Adapter) MatchURL(u *url.URL) bool {
	if a == nil || a.client == nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if id, ok := site.DetectHostSite(host); ok {
		return id == site.Douyin
	}
	if !a.client.allowLocalhost || !isLoopbackHost(host) {
		return false
	}
	segs := splitPathSegments(u.Path)
	if len(segs) >= 1 {
		switch strings.ToLower(segs[0]) {
		case "short", "aweme", "webcast", "douyin":
			return true
		case "video", "note", "slides", "user", "live":
			if len(segs) >= 2 && (reWorkIDDigits.MatchString(segs[1]) || reValidSecUID.MatchString(segs[1]) || strings.EqualFold(segs[1], "self")) {
				return true
			}
		}
	}
	return false
}

// Resolve implements site.Resolver.
func (a *Adapter) Resolve(ctx context.Context, input string) (site.ResolveTarget, error) {
	return a.client.ResolveLink(ctx, input)
}

// PlanDownload implements site.DownloadPlanner for both single progressive MP4 videos
// and mixed image/live-photo/video notes with background music.
func (a *Adapter) PlanDownload(ctx context.Context, targetID string, _ int) (site.DownloadPlan, error) {
	pref := "avc"
	if a.client.prefCodecFn != nil {
		if v := strings.TrimSpace(a.client.prefCodecFn()); v != "" {
			pref = v
		}
	}

	work, err := a.client.GetWorkDetail(ctx, targetID, StreamSelectOptions{PreferredCodec: pref})
	if err != nil {
		return site.DownloadPlan{}, err
	}
	if work.Uncacheable {
		reason := work.UncacheableReason
		if reason == "" {
			reason = "该作品为付费或加密内容，不支持缓存"
		}
		return site.DownloadPlan{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     reason,
		}
	}

	mediaSpec := a.client.VODTransport()

	switch work.Kind {
	case WorkKindNote:
		images := make([]site.MediaImageAsset, 0, len(work.Pages))
		for _, page := range work.Pages {
			asset := site.MediaImageAsset{
				Index:     page.Index,
				Width:     page.Width,
				Height:    page.Height,
				ImageURLs: append([]string(nil), page.ImageURLs...),
			}
			if page.VideoStream != nil {
				asset.VideoURLs = append([]string(nil), page.VideoStream.URLs...)
			}
			images = append(images, asset)
		}
		var musicURLs []string
		if work.Music != nil && len(work.Music.PlayURLs) > 0 {
			musicURLs = append([]string(nil), work.Music.PlayURLs...)
		}
		return site.DownloadPlan{
			Site:       site.Douyin,
			ItemKey:    "douyin:dynamic:" + work.ID,
			TargetID:   work.ID,
			Kind:       "image_note",
			AuthorUID:  work.Author.SecUID,
			AuthorName: work.Author.Nickname,
			Title:      work.Title,
			Desc:       work.Desc,
			PubTs:      work.CreateTime,
			CoverURLs:  append([]string(nil), work.CoverURLs...),
			MusicURLs:  musicURLs,
			Images:     images,
			Media:      mediaSpec,
		}, nil

	case WorkKindVideo:
		if work.Video == nil || len(work.Video.URLs) == 0 {
			return site.DownloadPlan{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindUnavailable,
				Message: "未找到可缓存的无水印视频直链",
			}
		}
		return site.DownloadPlan{
			Site:         site.Douyin,
			ItemKey:      "douyin:video:" + work.ID,
			TargetID:     work.ID,
			Kind:         "single_mp4",
			AuthorUID:    work.Author.SecUID,
			AuthorName:   work.Author.Nickname,
			Title:        work.Title,
			Desc:         work.Desc,
			PubTs:        work.CreateTime,
			DurationSec:  work.DurationSec,
			VideoCodec:   work.Video.Codec,
			QualityLabel: work.Video.QualityLabel,
			Width:        work.Video.Width,
			Height:       work.Video.Height,
			ExpectedSize: work.Video.DataSize,
			VideoURLs:    append([]string(nil), work.Video.URLs...),
			OriginalURLs: append([]string(nil), work.OriginalURLs...),
			CoverURLs:    append([]string(nil), work.CoverURLs...),
			Media:        mediaSpec,
		}, nil

	default:
		return site.DownloadPlan{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindBadRequest,
			Unsupported: true,
			Message:     "暂不支持缓存该抖音作品类型",
		}
	}
}

// CheckLiveStatus implements site.LiveProvider.
func (a *Adapter) CheckLiveStatus(ctx context.Context, uid string, cachedWebRID string) (site.LiveStatus, error) {
	return a.client.CheckLiveStatus(ctx, uid, cachedWebRID)
}

// SelectLiveStream implements site.LiveProvider.
func (a *Adapter) SelectLiveStream(ctx context.Context, uid string, roomOrWebRID string, codecOrder []string) (site.LiveStreamPlan, error) {
	return a.client.SelectLiveStream(ctx, uid, roomOrWebRID, codecOrder)
}

// FetchCreatorPosts implements site.SubscriptionFetcher for Douyin incremental update checking.
// On page-1 empty arrays, it cross-checks GetUserProfile per douyin.md §9.2 row 6 so private accounts
// or silent throttling are never misclassified as genuine 0 works; on empty filtered pages with advancing
// max_cursor, it allows at most 1 follow-up per §9.2 row 4.
func (a *Adapter) FetchCreatorPosts(ctx context.Context, uid string, cursor string) (site.CreatorPostsPage, error) {
	pref := "avc"
	if a.client.prefCodecFn != nil {
		if v := strings.TrimSpace(a.client.prefCodecFn()); v != "" {
			pref = v
		}
	}

	reqCursor := strings.TrimSpace(cursor)
	if reqCursor == "" {
		reqCursor = "0"
	}

	seen := make(map[string]bool)
	page, err := a.client.ListUserPosts(ctx, UserPostsRequest{
		SecUID:        uid,
		MaxCursor:     reqCursor,
		SeenIDs:       seen,
		StreamOptions: StreamSelectOptions{PreferredCodec: pref},
	})
	if err != nil {
		return site.CreatorPostsPage{}, err
	}

	// Row 4: allow at most 1 follow-up when aweme_list: [] && has_more == 1 with advancing cursor.
	if len(page.Works) == 0 && page.EmptyFollowupSuggested && page.NextCursor != "" && page.NextCursor != reqCursor {
		page2, err2 := a.client.ListUserPosts(ctx, UserPostsRequest{
			SecUID:        uid,
			MaxCursor:     page.NextCursor,
			SeenIDs:       seen,
			StreamOptions: StreamSelectOptions{PreferredCodec: pref},
		})
		if err2 != nil {
			return site.CreatorPostsPage{}, err2
		}
		if len(page2.Works) == 0 {
			// Two consecutive empty pages -> abort abnormally as uncertain!
			return site.CreatorPostsPage{
				Posts:           []site.CreatorPost{},
				NextCursor:      page2.NextCursor,
				HasMore:         false,
				UncertainEmpty:  true,
				UncertainReason: "连续两页返回空作品列表，已中止翻页并保留原基线",
			}, nil
		}
		page = page2
	}

	// Row 6: page-1 empty list with has_more == 0 -> cross-check profile/other when reachable!
	if reqCursor == "0" && len(page.Works) == 0 && !page.HasMore && a.client.canCallEndpoint(a.client.endpoints.ProfileOther) {
		prof, pErr := a.client.GetUserProfile(ctx, uid)
		if pErr != nil {
			return site.CreatorPostsPage{}, pErr
		}
		if prof.IsPrivate {
			return site.CreatorPostsPage{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindForbidden,
				Message: "该抖音账号已设为私密，无法查看作品列表",
			}
		}
		if prof.AwemeCount == 0 {
			return site.CreatorPostsPage{
				Posts:          []site.CreatorPost{},
				HasMore:        false,
				UncertainEmpty: false,
				AuthorName:     prof.Nickname,
				AuthorFace:     prof.AvatarURL,
				UniqueID:       prof.UniqueID,
				LiveWebRID:     prof.WebRID,
				LiveRoomID:     prof.RoomID,
			}, nil
		}
		return site.CreatorPostsPage{
			Posts:           []site.CreatorPost{},
			HasMore:         false,
			UncertainEmpty:  true,
			UncertainReason: "账号公开作品数大于 0 但首屏返回空列表，本轮结果记为不完整",
			AuthorName:      prof.Nickname,
			AuthorFace:      prof.AvatarURL,
			UniqueID:        prof.UniqueID,
			LiveWebRID:      prof.WebRID,
			LiveRoomID:      prof.RoomID,
		}, nil
	}

	posts := make([]site.CreatorPost, 0, len(page.Works))
	for _, w := range page.Works {
		postType := "video"
		if w.Kind == WorkKindNote {
			postType = "draw"
		}
		var cover string
		if len(w.CoverURLs) > 0 {
			cover = w.CoverURLs[0]
		}
		snapBytes, _ := json.Marshal(w)
		posts = append(posts, site.CreatorPost{
			ID:         w.ID,
			PubTs:      w.CreateTime,
			Type:       postType,
			IsTop:      w.IsTop,
			Title:      w.Title,
			CoverURL:   cover,
			Snapshot:   string(snapBytes),
			LiveWebRID: w.Author.WebRID,
			LiveRoomID: w.Author.RoomID,
		})
	}

	out := site.CreatorPostsPage{
		Posts:           posts,
		NextCursor:      page.NextCursor,
		HasMore:         page.HasMore,
		UncertainEmpty:  page.UncertainEmpty,
		UncertainReason: page.UncertainReason,
	}
	if page.Author != nil {
		out.AuthorName = page.Author.Nickname
		out.AuthorFace = page.Author.AvatarURL
		out.UniqueID = page.Author.UniqueID
		out.LiveWebRID = page.Author.WebRID
		out.LiveRoomID = page.Author.RoomID
	}
	return out, nil
}
