package bilibili

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

var (
	_ site.Resolver            = (*Adapter)(nil)
	_ site.URLMatcher          = (*Adapter)(nil)
	_ site.LiveProvider        = (*Adapter)(nil)
	_ site.SubscriptionFetcher = (*Adapter)(nil)
)

// Adapter wraps a Bilibili Client to satisfy cross-site interfaces in package site
// without altering any existing Client method signatures.
type Adapter struct {
	client *Client
}

// NewAdapter constructs a cross-site Adapter backed by client.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{client: client}
}

// Client returns the underlying Bilibili Client.
func (a *Adapter) Client() *Client {
	if a == nil {
		return nil
	}
	return a.client
}

// SiteID returns site.Bilibili.
func (a *Adapter) SiteID() site.ID {
	return site.Bilibili
}

// MediaTransport returns the Bilibili media CDN transport policy.
func (a *Adapter) MediaTransport() site.MediaTransportSpec {
	allowLocal := a != nil && a.client != nil && a.client.allowLocalhost
	return site.BilibiliMediaTransport(allowLocal)
}

// MatchURL claims Bilibili domains and local b23 short-link fixture URLs when AllowLocalhost is active.
func (a *Adapter) MatchURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if a != nil && a.client != nil && a.client.allowLocalhost &&
		(host == "127.0.0.1" || host == "localhost") && strings.HasPrefix(u.Path, "/b23/") {
		return true
	}
	id, ok := site.DetectHostSite(host)
	return ok && id == site.Bilibili
}

// Resolve converts user input into a cross-site site.ResolveTarget.
func (a *Adapter) Resolve(ctx context.Context, input string) (site.ResolveTarget, error) {
	if a == nil || a.client == nil {
		return site.ResolveTarget{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindUnavailable,
			Message: "哔哩哔哩客户端未初始化",
		}
	}
	t, err := a.client.ResolveLink(ctx, input)
	out := toSiteResolveTarget(t)
	if err != nil {
		return out, err
	}
	return out, nil
}

func toSiteResolveTarget(t ResolveTarget) site.ResolveTarget {
	var roomStr string
	if t.RoomID > 0 {
		roomStr = strconv.FormatInt(t.RoomID, 10)
	}
	return site.ResolveTarget{
		Site:          site.Bilibili,
		Kind:          t.Kind,
		ID:            t.ID,
		RoomID:        roomStr,
		NumericAID:    t.AID,
		Page:          t.Page,
		NumericRoomID: t.RoomID,
		NumericMid:    t.Mid,
		CanonicalURL:  t.CanonicalURL,
		Unsupported:   t.Unsupported,
		Reason:        t.Reason,
	}
}

// CheckLiveStatus queries live broadcast status for a single Bilibili UID.
func (a *Adapter) CheckLiveStatus(ctx context.Context, uid string, cachedWebRID string) (site.LiveStatus, error) {
	if a == nil || a.client == nil {
		return site.LiveStatus{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindUnavailable,
			Message: "哔哩哔哩客户端未初始化",
		}
	}
	normUID, err := site.NormalizeUID(site.Bilibili, uid)
	if err != nil {
		return site.LiveStatus{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindBadRequest,
			Message: "无效的哔哩哔哩 UID",
		}
	}
	mid, _ := strconv.ParseInt(normUID, 10, 64)
	statuses, err := a.client.LiveStatusByUIDs(ctx, []int64{mid})
	if err != nil {
		return site.LiveStatus{}, err
	}
	st, ok := statuses[mid]
	if !ok {
		roomFallback := strings.TrimSpace(cachedWebRID)
		return site.LiveStatus{
			Site:       site.Bilibili,
			AccountUID: normUID,
			RoomID:     roomFallback,
			Status:     site.LiveStateNone,
		}, nil
	}
	var roomStr string
	if st.RoomID > 0 {
		roomStr = strconv.FormatInt(st.RoomID, 10)
	}
	return site.LiveStatus{
		Site:       site.Bilibili,
		AccountUID: normUID,
		RoomID:     roomStr,
		Status:     mapBiliLiveStatus(st.LiveStatus),
		Title:      st.Title,
		Uname:      st.Uname,
		Face:       st.Face,
		Cover:      st.Cover,
		AreaName:   st.AreaName,
		LiveSince:  st.LiveTime,
	}, nil
}

// SelectLiveStream selects an HLS fMP4 or HTTP-FLV fallback live recording stream for a Bilibili room.
func (a *Adapter) SelectLiveStream(ctx context.Context, uid string, roomOrWebRID string, codecOrder []string) (site.LiveStreamPlan, error) {
	if a == nil || a.client == nil {
		return site.LiveStreamPlan{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindUnavailable,
			Message: "哔哩哔哩客户端未初始化",
		}
	}
	roomID, err := strconv.ParseInt(strings.TrimSpace(roomOrWebRID), 10, 64)
	if err != nil || roomID <= 0 {
		return site.LiveStreamPlan{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindBadRequest,
			Message: "无效的直播间房间号",
		}
	}
	sel, err := a.client.LiveRoomPlayInfo(ctx, roomID, codecOrder)
	if err != nil {
		return site.LiveStreamPlan{}, err
	}
	accountUID := strings.TrimSpace(uid)
	if accountUID == "" && sel.UID > 0 {
		accountUID = strconv.FormatInt(sel.UID, 10)
	}
	transport := site.LiveTransportHLSFMP4
	if sel.Format == "flv" || sel.Protocol == "http_stream" {
		transport = site.LiveTransportHTTPFLV
	}
	media := a.MediaTransport()
	media.Referer = "https://live.bilibili.com/"
	media.Origin = "https://live.bilibili.com"
	return site.LiveStreamPlan{
		Site:        site.Bilibili,
		AccountUID:  accountUID,
		RoomID:      strconv.FormatInt(sel.RoomID, 10),
		Status:      mapBiliLiveStatus(sel.LiveStatus),
		Transport:   transport,
		Protocol:    sel.Protocol,
		Format:      sel.Format,
		Codec:       sel.Codec,
		Quality:     sel.Quality,
		QualityName: sel.QualityName,
		URLs:        sel.URLs,
		Media:       media,
	}, nil
}

// FetchCreatorPosts fetches one page of creator space dynamics for subscription checks.
func (a *Adapter) FetchCreatorPosts(ctx context.Context, uid string, cursor string) (site.CreatorPostsPage, error) {
	if a == nil || a.client == nil {
		return site.CreatorPostsPage{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindUnavailable,
			Message: "哔哩哔哩客户端未初始化",
		}
	}
	normUID, err := site.NormalizeUID(site.Bilibili, uid)
	if err != nil {
		return site.CreatorPostsPage{}, &SiteError{
			Site:    site.Bilibili,
			Kind:    ErrKindBadRequest,
			Message: "无效的哔哩哔哩 UID",
		}
	}
	mid, _ := strconv.ParseInt(normUID, 10, 64)
	page, err := a.client.UserDynamics(ctx, mid, cursor)
	if err != nil {
		return site.CreatorPostsPage{}, err
	}
	posts := make([]site.CreatorPost, 0, len(page.Items))
	var authorName, authorFace string
	for _, item := range page.Items {
		if authorName == "" && item.Author.Name != "" {
			authorName = item.Author.Name
		}
		if authorFace == "" && item.Author.Face != "" {
			authorFace = item.Author.Face
		}
		snap, _ := json.Marshal(item)
		cover := ""
		if len(item.Videos) > 0 {
			cover = item.Videos[0].Cover
		} else if len(item.Images) > 0 {
			cover = item.Images[0].URL
		}
		posts = append(posts, site.CreatorPost{
			ID:       item.ID,
			PubTs:    item.PubTs,
			Type:     item.Type,
			IsTop:    item.Pinned,
			Title:    item.Title,
			CoverURL: cover,
			Snapshot: string(snap),
		})
	}
	return site.CreatorPostsPage{
		Posts:      posts,
		NextCursor: page.Offset,
		HasMore:    page.HasMore,
		AuthorName: authorName,
		AuthorFace: authorFace,
	}, nil
}

func mapBiliLiveStatus(code int) string {
	switch code {
	case 1:
		return site.LiveStateLive
	case 2:
		return site.LiveStateRound
	default:
		return site.LiveStateOffline
	}
}
