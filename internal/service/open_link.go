package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/storage"
)

const (
	maxPreviewMediaBytes = 64 * 1024 * 1024  // 64 MiB safety cap for uncached note BGM / clip preview
	maxPreviewDirBytes   = 256 * 1024 * 1024 // 256 MiB total cache budget for preview directory
	maxPreviewDirFiles   = 32                // max staged preview files kept simultaneously
)

// Resolve dispatches user input via site.Registry after enforcing per-site login gates with zero pre-auth network requests.
func (b *Backend) Resolve(ctx context.Context, rawInput string) (site.ResolveTarget, error) {
	if err := b.requireAnyLogin(); err != nil {
		return site.ResolveTarget{}, err
	}
	siteID, normalized, err := b.registry.DetectSite(rawInput)
	if err != nil {
		return site.ResolveTarget{}, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return site.ResolveTarget{}, err
	}
	res, ok := b.registry.Resolver(siteID)
	if !ok {
		return site.ResolveTarget{}, &site.Error{
			Site:        siteID,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     fmt.Sprintf("当前尚未启用 %s 站点解析器", siteID.DisplayName()),
		}
	}
	target, err := res.Resolve(ctx, normalized)
	if err != nil {
		return target, err
	}
	if target.Site == "" {
		target.Site = siteID
	}
	return target, nil
}

// OpenLink resolves user input across supported sites after enforcing per-site login gates,
// fetches detail metadata, records sanitized navigation history, and returns OpenLinkResult.
func (b *Backend) OpenLink(ctx context.Context, rawInput string) (*OpenLinkResult, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	siteID, normalizedInput, err := b.registry.DetectSite(rawInput)
	if err != nil {
		return nil, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return nil, err
	}

	if siteID == site.Douyin {
		return b.openDouyinLink(ctx, normalizedInput)
	}

	target, err := b.bili.ResolveLink(ctx, normalizedInput)
	if err != nil {
		b.logError("OpenLink.Resolve", err)
		return nil, err
	}

	var roomStr string
	if target.RoomID > 0 {
		roomStr = strconv.FormatInt(target.RoomID, 10)
	}
	siteTarget := site.ResolveTarget{
		Site:          site.Bilibili,
		Kind:          target.Kind,
		ID:            target.ID,
		RoomID:        roomStr,
		NumericAID:    target.AID,
		Page:          target.Page,
		NumericRoomID: target.RoomID,
		NumericMid:    target.Mid,
		CanonicalURL:  target.CanonicalURL,
		Unsupported:   target.Unsupported,
		Reason:        target.Reason,
	}
	out := &OpenLinkResult{
		Site:       string(site.Bilibili),
		SiteTarget: &siteTarget,
		Target:     target,
	}

	switch target.Kind {
	case "video":
		vd, err := b.bili.VideoDetail(ctx, target.ID)
		if err != nil {
			b.logError("OpenLink.VideoDetail", err)
			return nil, err
		}
		out.Video = &vd
		if item, ok, _ := b.lib.GetItem(vd.BVID); ok {
			out.CachedItem = &item
		}
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         "bilibili",
			Kind:         "video",
			RawID:        vd.BVID,
			Mid:          vd.Author.Mid,
			AuthorName:   vd.Author.Name,
			AuthorFace:   vd.Author.Face,
			Title:        vd.Title,
			Summary:      vd.Desc,
			CoverURL:     vd.Cover,
			CanonicalURL: vd.CanonicalURL,
			PubTs:        vd.PubTs,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "video", ID: vd.BVID, AvatarURL: vd.Author.Face, CoverURL: vd.Cover}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "video",
			ID:           vd.BVID,
			Title:        vd.Title,
			Subtitle:     vd.Author.Name,
			CanonicalURL: vd.CanonicalURL,
			DurationText: formatHistoryDuration(vd.Duration),
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "user":
		space, err := b.bili.UserCard(ctx, target.Mid)
		if err != nil {
			b.logError("OpenLink.UserCard", err)
			return nil, err
		}
		out.User = &space
		b.recordAccountWorks(site.Bilibili, strconv.FormatInt(space.Mid, 10), space.ArchiveCount)
		if dyns, dErr := b.bili.UserDynamics(ctx, target.Mid, ""); dErr == nil {
			out.UserDynamics = &dyns
		} else {
			b.logError("OpenLink.UserDynamics", dErr)
			out.UserDynamicsError = dErr.Error()
		}
		userID := fmt.Sprintf("%d", space.Mid)
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "user", ID: userID, AvatarURL: space.Face}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "user",
			ID:           userID,
			Title:        space.Name,
			Subtitle:     space.Sign,
			CanonicalURL: space.CanonicalURL,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "dynamic":
		dyn, err := b.bili.DynamicDetail(ctx, target.ID)
		if err != nil {
			b.logError("OpenLink.DynamicDetail", err)
			return nil, err
		}
		out.Dynamic = &dyn
		if item, ok, _ := b.lib.GetItem(library.CanonicalItemKey("bilibili", "dynamic", dyn.ID)); ok {
			out.CachedItem = &item
		}
		var coverURL string
		if len(dyn.Images) > 0 {
			coverURL = dyn.Images[0].URL
		} else if len(dyn.Videos) > 0 {
			coverURL = dyn.Videos[0].Cover
		}
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         "bilibili",
			Kind:         "dynamic",
			RawID:        dyn.ID,
			Mid:          dyn.Author.Mid,
			AuthorName:   dyn.Author.Name,
			AuthorFace:   dyn.Author.Face,
			Title:        dyn.Title,
			Summary:      dyn.Text,
			CoverURL:     coverURL,
			CanonicalURL: dyn.CanonicalURL,
			PubTs:        dyn.PubTs,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "dynamic", ID: dyn.ID, AvatarURL: dyn.Author.Face, CoverURL: coverURL}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "dynamic",
			ID:           dyn.ID,
			Title:        dyn.Title,
			Subtitle:     dyn.Author.Name,
			CanonicalURL: dyn.CanonicalURL,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "live":
		room, err := b.live.OpenRoom(ctx, target.RoomID)
		if err != nil {
			b.logError("OpenLink.LiveRoom", err)
			return nil, err
		}
		out.LiveRoom = &room
		space, err := b.bili.UserCard(ctx, room.UID)
		if err != nil {
			b.logError("OpenLink.LiveUserCard", err)
			return nil, err
		}
		out.User = &space
		b.recordAccountWorks(site.Bilibili, strconv.FormatInt(space.Mid, 10), space.ArchiveCount)
		if dyns, dErr := b.bili.UserDynamics(ctx, room.UID, ""); dErr == nil {
			out.UserDynamics = &dyns
		} else {
			b.logError("OpenLink.LiveUserDynamics", dErr)
			out.UserDynamicsError = dErr.Error()
		}
		liveID := fmt.Sprintf("live:%d", room.RoomID)
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "live", ID: liveID, AvatarURL: space.Face, CoverURL: room.Cover}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "live",
			ID:           liveID,
			Title:        room.Title,
			Subtitle:     room.Uname,
			CanonicalURL: fmt.Sprintf("https://live.bilibili.com/%d", room.RoomID),
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}
	}

	return out, nil
}

func (b *Backend) openDouyinLink(ctx context.Context, normalizedInput string) (*OpenLinkResult, error) {
	target, err := b.douyin.ResolveLink(ctx, normalizedInput)
	if err != nil {
		b.logError("OpenLink.DouyinResolve", err)
		return nil, err
	}
	out := &OpenLinkResult{
		Site:       string(site.Douyin),
		SiteTarget: &target,
		Target: bilibili.ResolveTarget{
			Kind:         target.Kind,
			ID:           target.ID,
			CanonicalURL: target.CanonicalURL,
			Unsupported:  target.Unsupported,
			Reason:       target.Reason,
		},
	}

	switch target.Kind {
	case "video", "note":
		wv, err := b.DouyinWork(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		out.DouyinWork = wv
		out.CachedItem = wv.CachedItem
		feedKind := "video"
		histKind := "video"
		if wv.Kind == douyin.WorkKindNote {
			feedKind = "dynamic"
			histKind = "note"
		}
		out.Target.Kind = histKind
		out.SiteTarget.Kind = histKind
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         "douyin",
			Kind:         feedKind,
			RawID:        wv.ID,
			AuthorUID:    wv.Author.SecUID,
			AuthorName:   wv.Author.Nickname,
			AuthorFace:   wv.Author.AvatarURL,
			Title:        wv.Title,
			Summary:      wv.Desc,
			CoverURL:     wv.CoverURL,
			CanonicalURL: wv.CanonicalURL,
			PubTs:        wv.CreateTime,
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      "douyin",
			Kind:      histKind,
			ID:        wv.ID,
			AvatarURL: wv.Author.AvatarURL,
			CoverURL:  wv.CoverURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "douyin",
			Kind:         histKind,
			ID:           wv.ID,
			Title:        wv.Title,
			Subtitle:     wv.Author.Nickname,
			CanonicalURL: wv.CanonicalURL,
			DurationText: formatHistoryDuration(wv.DurationSec),
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "user":
		secUID := firstNonEmpty(target.SecUID, target.ID)
		uv, err := b.DouyinUser(ctx, secUID)
		if err != nil {
			return nil, err
		}
		out.DouyinUser = uv
		out.Target.ID = uv.SecUID
		out.SiteTarget.ID = uv.SecUID
		out.SiteTarget.SecUID = uv.SecUID
		out.DouyinUserWorks, out.DouyinUserWorksError = b.firstDouyinUserWorks(ctx, uv)
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      "douyin",
			Kind:      "user",
			ID:        uv.SecUID,
			AvatarURL: uv.AvatarURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "douyin",
			Kind:         "user",
			ID:           uv.SecUID,
			Title:        uv.Nickname,
			Subtitle:     uv.Signature,
			CanonicalURL: uv.CanonicalURL,
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "live":
		webRID := firstNonEmpty(target.WebRID, target.ID)
		lv, err := b.DouyinLive(ctx, webRID)
		if err != nil {
			return nil, err
		}
		out.DouyinLive = lv
		if lv.OwnerSecUID != "" {
			if uv, uErr := b.DouyinUser(ctx, lv.OwnerSecUID); uErr == nil {
				if lv.State != nil {
					uv.LiveRoom = lv.State
					if lv.LiveStatus == 1 {
						uv.LiveStatus = 1
					}
				}
				out.DouyinUser = uv
				out.DouyinUserWorks, out.DouyinUserWorksError = b.firstDouyinUserWorks(ctx, uv)
			}
		}
		liveID := "live:" + lv.WebRID
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      "douyin",
			Kind:      "live",
			ID:        liveID,
			AvatarURL: lv.OwnerAvatar,
			CoverURL:  lv.CoverURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "douyin",
			Kind:         "live",
			ID:           liveID,
			Title:        firstNonEmpty(lv.Title, lv.OwnerName+"的直播间"),
			Subtitle:     lv.OwnerName,
			CanonicalURL: lv.CanonicalURL,
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}
	}

	return out, nil
}

// firstDouyinUserWorks loads the first page of a user's works for the user
// page. A private account still shows its works to the followers it approved,
// which the signed-in account may be, so the works are always asked for: when
// they come back the page shows them (IsPrivate is cleared, PrivateVisible set);
// otherwise it says the account is private, without an error.
func (b *Backend) firstDouyinUserWorks(ctx context.Context, uv *DouyinUserView) (*DouyinUserWorksPage, string) {
	wp, err := b.DouyinUserWorks(ctx, uv.SecUID, "")
	if uv.IsPrivate {
		if err == nil && wp != nil && len(wp.Works) > 0 {
			uv.IsPrivate = false
			uv.PrivateVisible = true
			return wp, ""
		}
		// Only an empty list or a refusal means the works are not visible;
		// risk control, rate limits, an expired login or the network are
		// reported as they are, with a retry.
		if err == nil || site.IsKind(err, site.ErrKindForbidden) || site.IsKind(err, site.ErrKindUnavailable) {
			return nil, ""
		}
		return nil, err.Error()
	}
	if err != nil {
		return nil, err.Error()
	}
	return wp, ""
}

func formatHistoryDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// FetchHistoryThumbs downloads and stores the thumbnails of a recent-parse
// entry after OpenLink returned. Image failures leave the placeholder; an
// entry removed meanwhile gets nothing.
func (b *Backend) FetchHistoryThumbs(ctx context.Context, job HistoryThumbJob) error {
	jobSite, err := normalizeSiteID(job.Site)
	if err != nil {
		return err
	}
	if err := b.requireSiteLogin(jobSite); err != nil {
		return err
	}
	if b.history == nil {
		return nil
	}
	var thumbs []storage.HistoryThumbnail
	for _, want := range []struct{ role, url string }{{"avatar", job.AvatarURL}, {"cover", job.CoverURL}} {
		if t := b.fetchHistoryThumbForSite(ctx, jobSite, want.role, want.url); len(t.Data) > 0 {
			thumbs = append(thumbs, t)
		}
	}
	if job.Kind == "user" && b.subs != nil {
		if _, ok := b.subs.OwnerOf(string(jobSite), job.ID); ok {
			b.rememberAccountAvatar(string(jobSite), job.ID, job.AvatarURL)
		}
	}
	if len(thumbs) == 0 {
		return nil
	}
	return b.history.AttachThumbs(string(jobSite), job.Kind, job.ID, thumbs...)
}

func (b *Backend) fetchHistoryThumb(ctx context.Context, role, rawURL string) storage.HistoryThumbnail {
	return b.fetchHistoryThumbForSite(ctx, site.Bilibili, role, rawURL)
}

func (b *Backend) fetchHistoryThumbForSite(ctx context.Context, siteID site.ID, role, rawURL string) storage.HistoryThumbnail {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || b.history == nil || b.requireSiteLogin(siteID) != nil {
		return storage.HistoryThumbnail{}
	}
	if strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "http://127.0.0.1") && !strings.HasPrefix(rawURL, "http://localhost") {
		rawURL = "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	var raw []byte
	var err error
	switch siteID {
	case site.Douyin:
		if b.douyin == nil {
			return storage.HistoryThumbnail{}
		}
		raw, _, err = b.douyin.FetchImageLimited(ctx, rawURL, storage.MaxHistoryThumbBytes)
	default:
		if b.bili == nil {
			return storage.HistoryThumbnail{}
		}
		raw, err = b.bili.FetchImageLimited(ctx, rawURL, storage.MaxHistoryThumbBytes)
	}
	if err != nil || len(raw) == 0 || len(raw) > storage.MaxHistoryThumbBytes {
		return storage.HistoryThumbnail{}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 24_000_000 {
		return storage.HistoryThumbnail{}
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return storage.HistoryThumbnail{}
	}
	w, h := cfg.Width, cfg.Height
	maxSide := 640
	if role == "avatar" {
		maxSide = 128
	}
	if w > maxSide {
		h, w = max(1, h*maxSide/w), maxSide
	}
	if h > maxSide {
		w, h = max(1, w*maxSide/h), maxSide
	}
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, small); err != nil {
		return storage.HistoryThumbnail{}
	}
	return storage.HistoryThumbnail{Role: role, Ext: ".png", Data: encoded.Bytes()}
}

// HistoryThumbPath resolves a local cached parse-history thumbnail inside <DataDir>/thumbs/.
func (b *Backend) HistoryThumbPath(name string) (string, error) {
	if err := b.requireAnyLogin(); err != nil {
		return "", err
	}
	if b.history == nil {
		return "", fmt.Errorf("service: history store unavailable")
	}
	return b.history.ThumbPath(name)
}

// ListHistory returns the recent sanitized navigation history entries.
func (b *Backend) ListHistory() []storage.HistoryEntry {
	if b.requireAnyLogin() != nil {
		return nil
	}
	return b.history.List()
}

// ClearHistory clears the navigation history.
func (b *Backend) ClearHistory() error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	return b.history.Clear()
}

// RemoveHistory removes a single history entry by kind and id.
func (b *Backend) RemoveHistory(kind, id string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	return b.history.Remove(kind, id)
}

// RemoveHistoryForSite deletes a single recent-parse history item scoped to (siteID, kind, id).
func (b *Backend) RemoveHistoryForSite(siteID, kind, id string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	return b.history.RemoveBySite(siteID, kind, id)
}

// FetchImage fetches a whitelisted Bilibili or Douyin cover/avatar/note image without sending Cookie.
func (b *Backend) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	imgSite := b.detectImageSite(rawURL)
	if err := b.requireSiteLogin(imgSite); err != nil {
		return nil, err
	}
	if imgSite == site.Douyin {
		data, _, err := b.douyin.FetchImage(ctx, rawURL)
		return data, err
	}
	return b.bili.FetchImage(ctx, rawURL)
}

func (b *Backend) detectImageSite(rawURL string) site.ID {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return site.Bilibili
	}
	host := strings.ToLower(u.Hostname())
	if site.MatchDomainSuffix(host, site.DefaultDouyinMediaHosts()) {
		return site.Douyin
	}
	if id, ok := site.DetectHostSite(host); ok {
		return id
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		if b.douyin != nil {
			ep := b.douyin.Endpoints()
			for _, candidate := range []string{ep.ProfileSelf, ep.AwemeDetail, ep.LiveWebEnter} {
				if cu, cErr := url.Parse(candidate); cErr == nil && strings.EqualFold(cu.Host, u.Host) {
					if strings.HasPrefix(u.Path, "/bfs/") {
						return site.Bilibili
					}
					return site.Douyin
				}
			}
		}
		if strings.HasPrefix(u.Path, "/douyin") || strings.HasPrefix(u.Path, "/obj/") || strings.HasPrefix(u.Path, "/tos-") {
			return site.Douyin
		}
		if b.douyinCred != nil && b.douyinCred.HasActiveCredential() && (b.cred == nil || !b.cred.HasActiveCredential()) {
			return site.Douyin
		}
	}
	return site.Bilibili
}

func (b *Backend) UserDynamicsPage(ctx context.Context, mid int64, offset string) (bilibili.UserDynamicsPage, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return bilibili.UserDynamicsPage{}, err
	}
	return b.bili.UserDynamics(ctx, mid, offset)
}

func (b *Backend) UserVideosPage(ctx context.Context, mid int64, page int) (bilibili.UserVideosPage, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return bilibili.UserVideosPage{}, err
	}
	res, err := b.bili.UserVideos(ctx, mid, page, 30, "pubdate", "")
	if err == nil && page <= 1 {
		b.recordAccountWorks(site.Bilibili, strconv.FormatInt(mid, 10), res.Total)
	}
	return res, err
}

func (b *Backend) preferredDouyinCodec() string {
	dl := b.settings.Get().Download
	for _, c := range dl.CodecOrder {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "hevc", "h265", "bytevc1":
			return "hevc"
		case "avc", "h264":
			return "avc"
		}
	}
	if strings.EqualFold(dl.PreferredVideoCodec, "hevc") {
		return "hevc"
	}
	return "avc"
}

// DouyinWork fetches and maps a single Douyin video or image/mixed note post.
func (b *Backend) DouyinWork(ctx context.Context, awemeID string) (*DouyinWorkView, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	detail, err := b.douyin.GetWorkDetail(ctx, awemeID, douyin.StreamSelectOptions{
		PreferredCodec: b.preferredDouyinCodec(),
	})
	if err != nil {
		b.logError("DouyinWork", err)
		return nil, err
	}
	var cached *library.ItemMetadata
	cacheKey := douyinCacheKeyForKind(detail.Kind, detail.ID)
	if cacheKey != "" {
		if item, ok, _ := b.lib.GetItem(cacheKey); ok {
			cached = &item
		}
	}
	if detail.Author.SecUID != "" && detail.Author.WebRID != "" {
		b.syncDouyinAccountWebRID(detail.Author.SecUID, detail.Author.WebRID)
	}
	return mapDouyinWorkView(detail, cached), nil
}

// DouyinUser fetches a Douyin creator's profile and maps optional live banner state.
func (b *Backend) DouyinUser(ctx context.Context, secUID string) (*DouyinUserView, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	targetSecUID := strings.TrimSpace(secUID)
	if targetSecUID == "" || strings.EqualFold(targetSecUID, "self") || targetSecUID == "/user/self" {
		targetSecUID = b.douyinCred.Status().SecUID
		if targetSecUID == "" {
			selfProf, sErr := b.douyin.GetSelfProfile(ctx)
			if sErr != nil {
				return nil, sErr
			}
			targetSecUID = selfProf.SecUID
		}
	}
	prof, err := b.douyin.GetUserProfile(ctx, targetSecUID)
	if err != nil {
		b.logError("DouyinUser", err)
		return nil, err
	}
	if b.subs != nil {
		_ = b.subs.UpdateAccountSnapshot(string(site.Douyin), prof.SecUID, prof.Nickname, prof.AvatarURL, prof.UniqueID)
	}
	b.recordAccountWorks(site.Douyin, prof.SecUID, prof.AwemeCount)
	if prof.WebRID != "" {
		b.syncDouyinAccountWebRID(prof.SecUID, prof.WebRID)
	}
	if b.live != nil && (prof.WebRID != "" || prof.RoomID != "") {
		b.live.ObserveAccountLiveHint(site.Douyin, prof.SecUID, prof.WebRID, prof.RoomID)
	}
	var liveState *LiveAccountState
	if prof.LiveStatus == 1 {
		st := LiveAccountState{
			Site:       string(site.Douyin),
			AccountKey: formatAccountKey(site.Douyin, prof.SecUID),
			UID:        prof.SecUID,
			RoomID:     prof.RoomID,
			WebRID:     prof.WebRID,
			Uname:      prof.Nickname,
			Face:       prof.AvatarURL,
			LiveStatus: 1,
			StatusText: "直播中",
			Transport:  string(site.LiveTransportHTTPFLV),
		}
		if prof.RoomID != "" {
			st.RecordingKey = "douyin:live:" + prof.RoomID
		}
		if prof.WebRID != "" && b.live != nil {
			if rs, lErr := b.live.CheckAccountLiveStatus(ctx, site.Douyin, prof.SecUID, prof.WebRID, false); lErr == nil {
				mapped := mapRoomToAccountState(rs)
				if mapped.Uname == "" {
					mapped.Uname = prof.Nickname
				}
				if mapped.Face == "" {
					mapped.Face = prof.AvatarURL
				}
				st = mapped
			}
		}
		liveState = &st
	}
	return mapDouyinUserView(prof, liveState), nil
}

// DouyinUserWorks fetches one paginated grid page of a Douyin creator's works.
func (b *Backend) DouyinUserWorks(ctx context.Context, secUID, cursor string) (*DouyinUserWorksPage, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	page, err := b.douyin.ListUserPosts(ctx, douyin.UserPostsRequest{
		SecUID:        strings.TrimSpace(secUID),
		MaxCursor:     strings.TrimSpace(cursor),
		StreamOptions: douyin.StreamSelectOptions{PreferredCodec: b.preferredDouyinCodec()},
	})
	if err != nil {
		b.logError("DouyinUserWorks", err)
		return nil, err
	}
	cards := make([]DouyinWorkCardDTO, 0, len(page.Works))
	for _, w := range page.Works {
		cards = append(cards, mapDouyinWorkCard(w))
	}
	return &DouyinUserWorksPage{
		Works:           cards,
		NextCursor:      page.NextCursor,
		HasMore:         page.HasMore,
		UncertainEmpty:  page.UncertainEmpty,
		UncertainReason: page.UncertainReason,
	}, nil
}

// DouyinLive queries a Douyin live room by web_rid and returns normalized room and anchor state.
func (b *Backend) DouyinLive(ctx context.Context, webRID string) (*DouyinLiveView, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	detail, err := b.douyin.EnterLiveRoom(ctx, strings.TrimSpace(webRID), "")
	if err != nil {
		b.logError("DouyinLive", err)
		return nil, err
	}
	prefWebRID := detail.PreferredWebRID()
	if detail.OwnerSecUID != "" && prefWebRID != "" {
		b.syncDouyinAccountWebRID(detail.OwnerSecUID, prefWebRID)
	}
	if b.live != nil && detail.OwnerSecUID != "" {
		b.live.ObserveAccountLiveHint(site.Douyin, detail.OwnerSecUID, prefWebRID, detail.RoomID)
	}
	liveStatus := 0
	statusText := "未开播"
	if detail.RoomStatus == 2 {
		liveStatus = 1
		if detail.AudioOnly {
			statusText = "纯语音直播"
		} else {
			statusText = "直播中"
		}
	}
	acctState := &LiveAccountState{
		Site:         string(site.Douyin),
		AccountKey:   formatAccountKey(site.Douyin, detail.OwnerSecUID),
		RecordingKey: detail.LiveDedupeKey(),
		UID:          detail.OwnerSecUID,
		RoomID:       detail.RoomID,
		WebRID:       prefWebRID,
		Uname:        detail.OwnerNickname,
		Face:         detail.OwnerAvatar,
		Title:        detail.Title,
		Cover:        detail.CoverURL,
		AreaName:     detail.AreaName,
		LiveStatus:   liveStatus,
		LiveSince:    detail.LiveSince,
		StatusText:   statusText,
		Transport:    string(site.LiveTransportHTTPFLV),
		AudioOnly:    detail.AudioOnly,
	}
	if b.live != nil && detail.OwnerSecUID != "" {
		if cached, ok := b.live.CachedAccountState(site.Douyin, detail.OwnerSecUID); ok {
			if len(cached.Incidents) > 0 {
				acctState.Incidents = append([]library.LiveIncident(nil), cached.Incidents...)
			}
			if cached.Recording {
				acctState.Recording = true
				if cached.Transport != "" {
					acctState.Transport = cached.Transport
				}
				acctState.AudioOnly = acctState.AudioOnly || cached.AudioOnly
				acctState.RecordSession = cached.RecordSession
				acctState.RecordDurationSec = cached.RecordDurationSec
				acctState.RecordSpeedBps = cached.RecordSpeedBps
				acctState.RecordBytes = cached.RecordBytes
				acctState.RecordSegments = cached.RecordSegments
				acctState.RecordItemDir = cached.RecordItemDir
				acctState.RecordPartPath = firstNonEmpty(cached.RecordPartPath, resolveRecordingPartPath(cached.RecordItemDir))
				acctState.RecordPartIndex = cached.RecordPartIndex
				acctState.RecordPartBytes = cached.RecordPartBytes
				acctState.RecordPartReady = cached.RecordPartReady
				acctState.PreviewSupported = cached.PreviewSupported
				acctState.PreviewReason = cached.PreviewReason
			}
		}
	}
	canonicalURL := "https://live.douyin.com/" + prefWebRID
	return &DouyinLiveView{
		OwnerSecUID:  detail.OwnerSecUID,
		OwnerUID:     detail.OwnerUID,
		OwnerName:    detail.OwnerNickname,
		OwnerAvatar:  detail.OwnerAvatar,
		WebRID:       prefWebRID,
		RoomID:       detail.RoomID,
		LiveStatus:   liveStatus,
		StatusText:   statusText,
		Title:        detail.Title,
		CoverURL:     detail.CoverURL,
		AreaName:     detail.AreaName,
		LiveSince:    detail.LiveSince,
		AudioOnly:    detail.AudioOnly,
		CanonicalURL: canonicalURL,
		State:        acctState,
	}, nil
}

// StagePreviewMedia downloads a whitelisted CDN audio/video clip (e.g. uncached Douyin note BGM
// or note video page) without sending Cookie into <DataDir>/cache/preview/ and returns its local file path
// for playback via internal/player.
func (b *Backend) StagePreviewMedia(ctx context.Context, siteID, rawURL, ext string) (string, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return "", err
	}
	if err := b.requireSiteLogin(id); err != nil {
		return "", err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "媒体预览地址为空"}
	}
	var data []byte
	switch id {
	case site.Douyin:
		var fetchErr error
		data, _, fetchErr = b.douyin.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	case site.Bilibili:
		var fetchErr error
		data, fetchErr = b.bili.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	}
	safeExt := ".mp4"
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".mp3", "mp3":
		safeExt = ".mp3"
	case ".m4a", "m4a":
		safeExt = ".m4a"
	case ".aac", "aac":
		safeExt = ".aac"
	case ".ogg", "ogg":
		safeExt = ".ogg"
	case ".wav", "wav":
		safeExt = ".wav"
	}
	sum := sha256.Sum256([]byte(string(id) + "\x00" + rawURL))
	fileName := fmt.Sprintf("preview_%s_%x%s", string(id), sum[:16], safeExt)
	fullPath := filepath.Join(b.previewDir, fileName)
	if err := storage.WriteFileAtomic(fullPath, data, 0o600); err != nil {
		return "", err
	}
	prunePreviewDir(b.previewDir, fullPath, maxPreviewDirBytes, maxPreviewDirFiles)
	return fullPath, nil
}

func prunePreviewDir(dir, keepPath string, maxBytes int64, maxFiles int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type stagedFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	var files []stagedFile
	var totalBytes int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		files = append(files, stagedFile{path: p, size: info.Size(), modTime: info.ModTime()})
		totalBytes += info.Size()
	}
	if totalBytes <= maxBytes && len(files) <= maxFiles {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].modTime.Equal(files[j].modTime) {
			return files[i].modTime.Before(files[j].modTime)
		}
		return files[i].path < files[j].path
	})
	remaining := len(files)
	for _, f := range files {
		if totalBytes <= maxBytes && remaining <= maxFiles {
			break
		}
		if samePath(f.path, keepPath) {
			continue
		}
		if os.Remove(f.path) == nil {
			totalBytes -= f.size
			remaining--
		}
	}
}
