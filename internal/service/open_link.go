package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	maxPreviewMediaBytes     = site.DefaultMaxImageBytes // 128 MiB safety cap for uncached note BGM / clip / GIF preview
	maxPreviewDirBytes       = 512 * 1024 * 1024         // 512 MiB total cache budget for preview directory
	maxPreviewDirFiles       = 32                        // max staged preview files kept simultaneously
	maxHistoryThumbSourcePix = 100_000_000               // 100 MP source decode ceiling (covers Weibo long images before downscaling to <=640px PNG)
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

	switch siteID {
	case site.Douyin:
		return b.openDouyinLink(ctx, normalizedInput)
	case site.Weibo:
		return b.openWeiboLink(ctx, normalizedInput)
	case site.X:
		return b.openXLink(ctx, normalizedInput)
	case site.Bilibili:
		// Continue below with Bilibili handler.
	default:
		return nil, &site.Error{
			Site:    siteID,
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点标识: " + string(siteID),
		}
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
		if vd.Author.Mid > 0 && vd.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(vd.Author.Mid, 10), vd.Author.Face)
		}
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
		if space.Mid > 0 && space.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(space.Mid, 10), space.Face)
		}
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
		if dyn.Author.Mid > 0 && dyn.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(dyn.Author.Mid, 10), dyn.Author.Face)
		}
		if dyn.Origin != nil && dyn.Origin.Author.Mid > 0 && dyn.Origin.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(dyn.Origin.Author.Mid, 10), dyn.Origin.Author.Face)
		}
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
		if space.Mid > 0 && space.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(space.Mid, 10), space.Face)
		}
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
	known := &douyin.UserProfile{
		SecUID:     uv.SecUID,
		AwemeCount: uv.AwemeCount,
		IsPrivate:  uv.IsPrivate,
	}
	wp, err := b.douyinUserWorks(ctx, uv.SecUID, "", known)
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

func (b *Backend) openWeiboLink(ctx context.Context, normalizedInput string) (*OpenLinkResult, error) {
	target, err := b.weibo.ResolveLink(ctx, normalizedInput)
	if err != nil {
		b.logError("OpenLink.WeiboResolve", err)
		return nil, err
	}
	out := &OpenLinkResult{
		Site:       string(site.Weibo),
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
	case "dynamic":
		hintUID := firstNonEmpty(target.HintAuthorUID, target.AuthorUID)
		pv, err := b.WeiboPost(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		if hintUID != "" {
			pv.ForwarderHintUID = hintUID
			pv.ForwarderHintName = b.resolveWeiboForwarderHintName(hintUID, pv.Author.UID, pv.Author.ScreenName)
			out.SiteTarget.HintAuthorUID = hintUID
		}
		out.WeiboPost = pv
		out.CachedItem = pv.CachedItem
		out.Target.ID = pv.Mid
		out.SiteTarget.ID = pv.Mid
		out.SiteTarget.AuthorUID = pv.Author.UID
		out.SiteTarget.MblogID = pv.MblogID
		out.SiteTarget.CanonicalURL = pv.CanonicalURL
		title := weiboPostSummaryTitle(pv)
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         string(site.Weibo),
			Kind:         "dynamic",
			RawID:        pv.Mid,
			AuthorUID:    pv.Author.UID,
			AuthorName:   pv.Author.ScreenName,
			AuthorFace:   weibo.SafePublicImageURL(pv.Author.AvatarURL),
			Title:        title,
			Summary:      pv.Text,
			CoverURL:     weiboPostPublicCoverURL(pv),
			CanonicalURL: pv.CanonicalURL,
			PubTs:        pv.CreatedAt,
		}); err != nil {
			return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      string(site.Weibo),
			Kind:      "dynamic",
			ID:        pv.Mid,
			AvatarURL: pv.Author.AvatarURL,
			CoverURL:  weiboPostThumbCoverURL(pv),
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         string(site.Weibo),
			Kind:         "dynamic",
			ID:           pv.Mid,
			Title:        title,
			Subtitle:     pv.Author.ScreenName,
			CanonicalURL: pv.CanonicalURL,
			DurationText: formatHistoryDuration(weiboPostDuration(pv)),
		}); err != nil {
			return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "video":
		vv, err := b.WeiboVideo(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		out.WeiboVideo = vv
		if vv.HostPost != nil {
			pv := vv.HostPost
			out.WeiboPost = pv
			out.CachedItem = pv.CachedItem
			out.Target.Kind = "dynamic"
			out.Target.ID = pv.Mid
			out.SiteTarget.Kind = "dynamic"
			out.SiteTarget.ID = pv.Mid
			out.SiteTarget.AuthorUID = pv.Author.UID
			out.SiteTarget.MblogID = pv.MblogID
			out.SiteTarget.CanonicalURL = pv.CanonicalURL
			title := weiboPostSummaryTitle(pv)
			if err := b.lib.UpsertFeedEntry(library.FeedEntry{
				Site:         string(site.Weibo),
				Kind:         "dynamic",
				RawID:        pv.Mid,
				AuthorUID:    pv.Author.UID,
				AuthorName:   pv.Author.ScreenName,
				AuthorFace:   weibo.SafePublicImageURL(pv.Author.AvatarURL),
				Title:        title,
				Summary:      pv.Text,
				CoverURL:     weiboPostPublicCoverURL(pv),
				CanonicalURL: pv.CanonicalURL,
				PubTs:        pv.CreatedAt,
			}); err != nil {
				return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "解析记录写入失败", Err: err}
			}
			out.HistoryThumbs = &HistoryThumbJob{
				Site:      string(site.Weibo),
				Kind:      "dynamic",
				ID:        pv.Mid,
				AvatarURL: pv.Author.AvatarURL,
				CoverURL:  weiboPostThumbCoverURL(pv),
			}
			if err := b.history.Record(storage.HistoryEntry{
				Site:         string(site.Weibo),
				Kind:         "dynamic",
				ID:           pv.Mid,
				Title:        title,
				Subtitle:     pv.Author.ScreenName,
				CanonicalURL: pv.CanonicalURL,
				DurationText: formatHistoryDuration(weiboPostDuration(pv)),
			}); err != nil {
				return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "历史记录写入失败", Err: err}
			}
		} else {
			out.CachedItem = vv.CachedItem
			rawID := strings.ReplaceAll(vv.ObjectID, ":", "_")
			title := firstNonEmpty(vv.Title, "微博视频 "+vv.ObjectID)
			if err := b.lib.UpsertFeedEntry(library.FeedEntry{
				Site:         string(site.Weibo),
				Kind:         "video",
				RawID:        rawID,
				AuthorUID:    vv.AuthorUID,
				AuthorName:   vv.AuthorName,
				Title:        title,
				CoverURL:     weiboVideoPublicCoverURL(vv.Video),
				CanonicalURL: vv.CanonicalURL,
			}); err != nil {
				return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "解析记录写入失败", Err: err}
			}
			out.HistoryThumbs = &HistoryThumbJob{
				Site:     string(site.Weibo),
				Kind:     "video",
				ID:       rawID,
				CoverURL: weiboVideoThumbCoverURL(vv.Video),
			}
			if err := b.history.Record(storage.HistoryEntry{
				Site:         string(site.Weibo),
				Kind:         "video",
				ID:           rawID,
				Title:        title,
				Subtitle:     vv.AuthorName,
				CanonicalURL: vv.CanonicalURL,
				DurationText: formatHistoryDuration(weiboVideoDuration(vv.Video)),
			}); err != nil {
				return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "历史记录写入失败", Err: err}
			}
		}

	case "user":
		uid := firstNonEmpty(target.AuthorUID, target.ID)
		uv, err := b.WeiboUser(ctx, uid)
		if err != nil {
			return nil, err
		}
		out.WeiboUser = uv
		out.Target.ID = uv.UID
		out.SiteTarget.ID = uv.UID
		out.SiteTarget.AuthorUID = uv.UID
		out.SiteTarget.CanonicalURL = uv.CanonicalURL
		known := &weibo.UserProfile{
			UID:           uv.UID,
			ScreenName:    uv.ScreenName,
			StatusesCount: uv.StatusesCount,
			BlockText:     uv.BlockText,
		}
		if postsPage, _, pErr := b.weiboUserPosts(ctx, uv.UID, "", weibo.FeatureAll, known); pErr == nil {
			out.WeiboUserPosts = postsPage
		} else {
			b.logError("OpenLink.WeiboUserPosts", pErr)
			out.WeiboUserPostsError = pErr.Error()
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      string(site.Weibo),
			Kind:      "user",
			ID:        uv.UID,
			AvatarURL: uv.AvatarURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         string(site.Weibo),
			Kind:         "user",
			ID:           uv.UID,
			Title:        uv.ScreenName,
			Subtitle:     uv.Description,
			CanonicalURL: uv.CanonicalURL,
		}); err != nil {
			return nil, &site.Error{Site: site.Weibo, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "live":
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "微博直播暂不支持录制，可以在浏览器中打开",
		}

	default:
		return nil, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     firstNonEmpty(target.Reason, "暂不支持这种微博链接"),
		}
	}

	return out, nil
}

func (b *Backend) openXLink(ctx context.Context, normalizedInput string) (*OpenLinkResult, error) {
	target, err := b.x.ResolveLink(ctx, normalizedInput)
	if err != nil {
		b.logError("OpenLink.XResolve", err)
		return nil, err
	}
	out := &OpenLinkResult{
		Site:       string(site.X),
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
	case "dynamic":
		pv, err := b.XPost(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		out.XPost = pv
		out.CachedItem = pv.CachedItem
		out.Target.ID = pv.ID
		out.SiteTarget.ID = pv.ID
		out.SiteTarget.AuthorUID = pv.Author.RestID
		out.SiteTarget.CanonicalURL = pv.CanonicalURL
		title := xPostSummaryTitle(pv)
		authorName := xPostAuthorDisplayName(pv.Author)
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         string(site.X),
			Kind:         "dynamic",
			RawID:        pv.ID,
			AuthorUID:    pv.Author.RestID,
			AuthorName:   authorName,
			AuthorFace:   x.SafePublicImageURL(pv.Author.AvatarURL),
			Title:        title,
			Summary:      pv.Text,
			CoverURL:     xPostPublicCoverURL(pv),
			CanonicalURL: pv.CanonicalURL,
			PubTs:        pv.CreatedAt,
		}); err != nil {
			return nil, &site.Error{Site: site.X, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      string(site.X),
			Kind:      "dynamic",
			ID:        pv.ID,
			AvatarURL: pv.Author.AvatarURL,
			CoverURL:  xPostThumbCoverURL(pv),
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         string(site.X),
			Kind:         "dynamic",
			ID:           pv.ID,
			Title:        title,
			Subtitle:     authorName,
			CanonicalURL: pv.CanonicalURL,
			DurationText: formatHistoryDuration(xPostDuration(pv)),
		}); err != nil {
			return nil, &site.Error{Site: site.X, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "user":
		uid := firstNonEmpty(target.AuthorUID, target.ID)
		uv, err := b.XUser(ctx, uid)
		if err != nil {
			return nil, err
		}
		out.XUser = uv
		out.Target.ID = uv.RestID
		out.SiteTarget.ID = uv.RestID
		out.SiteTarget.AuthorUID = uv.RestID
		out.SiteTarget.CanonicalURL = uv.CanonicalURL
		known := &x.UserProfile{
			RestID:        uv.RestID,
			ScreenName:    uv.ScreenName,
			Name:          uv.Name,
			StatusesCount: uv.StatusesCount,
			Protected:     uv.Protected,
		}
		if postsPage, _, pErr := b.xUserPosts(ctx, uv.RestID, "", known); pErr == nil {
			out.XUserPosts = postsPage
		} else {
			b.logError("OpenLink.XUserPosts", pErr)
			out.XUserPostsError = pErr.Error()
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      string(site.X),
			Kind:      "user",
			ID:        uv.RestID,
			AvatarURL: uv.AvatarURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         string(site.X),
			Kind:         "user",
			ID:           uv.RestID,
			Title:        formatXDisplayName(uv.Name, uv.ScreenName),
			Subtitle:     uv.Description,
			CanonicalURL: uv.CanonicalURL,
		}); err != nil {
			return nil, &site.Error{Site: site.X, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "live":
		return nil, &site.Error{
			Site:        site.X,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     x.LiveUnsupportedText,
		}

	default:
		return nil, &site.Error{
			Site:        site.X,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     firstNonEmpty(target.Reason, "暂不支持这种 X 链接"),
		}
	}

	return out, nil
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
	sourceLimit := int64(storage.MaxHistoryThumbBytes)
	if role == "cover" {
		sourceLimit = site.DefaultMaxImageBytes
	}
	var raw []byte
	var err error
	switch siteID {
	case site.Douyin:
		if b.douyin == nil {
			return storage.HistoryThumbnail{}
		}
		raw, _, err = b.douyin.FetchImageLimited(ctx, rawURL, sourceLimit)
	case site.Weibo:
		if b.weibo == nil {
			return storage.HistoryThumbnail{}
		}
		raw, _, err = b.weibo.FetchImageLimited(ctx, rawURL, sourceLimit)
	case site.X:
		if b.x == nil {
			return storage.HistoryThumbnail{}
		}
		raw, _, err = b.x.FetchImageLimited(ctx, rawURL, sourceLimit)
	case site.Bilibili:
		if b.bili == nil {
			return storage.HistoryThumbnail{}
		}
		raw, err = b.bili.FetchImageLimited(ctx, rawURL, sourceLimit)
	default:
		return storage.HistoryThumbnail{}
	}
	if err != nil || len(raw) == 0 || int64(len(raw)) > sourceLimit {
		return storage.HistoryThumbnail{}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxHistoryThumbSourcePix {
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
		h, w = max(1, int(int64(h)*int64(maxSide)/int64(w))), maxSide
	}
	if h > maxSide {
		w, h = max(1, int(int64(w)*int64(maxSide)/int64(h))), maxSide
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

// FetchImage fetches a whitelisted Bilibili, Douyin, Weibo, or X cover/avatar/post image without sending Cookie.
func (b *Backend) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	imgSite := b.detectImageSite(rawURL)
	if err := b.requireSiteLogin(imgSite); err != nil {
		return nil, err
	}
	switch imgSite {
	case site.Douyin:
		data, _, err := b.douyin.FetchImage(ctx, rawURL)
		return data, err
	case site.Weibo:
		data, _, err := b.weibo.FetchImage(ctx, rawURL)
		return data, err
	case site.X:
		data, _, err := b.x.FetchImage(ctx, rawURL)
		return data, err
	case site.Bilibili:
		return b.bili.FetchImage(ctx, rawURL)
	default:
		return nil, &site.Error{Site: imgSite, Kind: site.ErrKindBadRequest, Message: "不支持的图片站点"}
	}
}

func (b *Backend) detectImageSite(rawURL string) site.ID {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return site.Bilibili
	}
	host := strings.ToLower(u.Hostname())
	if site.MatchDomainSuffix(host, site.DefaultXMediaHosts()) {
		return site.X
	}
	if site.MatchDomainSuffix(host, site.DefaultWeiboMediaHosts()) || site.IsWeiboLivePhotoInitialURL(rawURL) {
		return site.Weibo
	}
	if site.MatchDomainSuffix(host, site.DefaultDouyinMediaHosts()) {
		return site.Douyin
	}
	if id, ok := site.DetectHostSite(host); ok {
		return id
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		if b.x != nil {
			ep := b.x.Endpoints()
			for _, candidate := range []string{ep.HomeURL, ep.AccountSettings, ep.GraphQLBase} {
				if cu, cErr := url.Parse(candidate); cErr == nil && strings.EqualFold(cu.Host, u.Host) {
					if strings.HasPrefix(u.Path, "/bfs/") {
						return site.Bilibili
					}
					return site.X
				}
			}
		}
		if b.weibo != nil {
			ep := b.weibo.Endpoints()
			for _, candidate := range []string{ep.PCBasicInfo, ep.ProfileInfo, ep.StatusesShow} {
				if cu, cErr := url.Parse(candidate); cErr == nil && strings.EqualFold(cu.Host, u.Host) {
					if strings.HasPrefix(u.Path, "/bfs/") {
						return site.Bilibili
					}
					return site.Weibo
				}
			}
		}
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
		if strings.HasPrefix(u.Path, "/xcdn/") || strings.HasPrefix(u.Path, "/twimg/") ||
			strings.HasPrefix(u.Path, "/profile_images/") || strings.HasPrefix(u.Path, "/ext_tw_video_thumb/") ||
			strings.HasPrefix(u.Path, "/amplify_video_thumb/") || strings.HasPrefix(u.Path, "/tweet_video_thumb/") {
			return site.X
		}
		if strings.HasPrefix(u.Path, "/weibo") || strings.HasPrefix(u.Path, "/sinaimg/") || strings.HasPrefix(u.Path, "/weibocdn/") || strings.HasPrefix(u.Path, "/livephoto/") {
			return site.Weibo
		}
		if strings.HasPrefix(u.Path, "/douyin") || strings.HasPrefix(u.Path, "/obj/") || strings.HasPrefix(u.Path, "/tos-") {
			return site.Douyin
		}
		biliActive := b.cred != nil && b.cred.HasActiveCredential()
		dyActive := b.douyinCred != nil && b.douyinCred.HasActiveCredential()
		wbActive := b.weiboCred != nil && b.weiboCred.HasActiveCredential()
		xActive := b.xCred != nil && b.xCred.HasActiveCredential()
		if xActive && !biliActive && !dyActive && !wbActive {
			return site.X
		}
		if wbActive && !biliActive && !dyActive {
			return site.Weibo
		}
		if dyActive && !biliActive {
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
	if detail.Author.SecUID != "" && detail.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.Douyin), detail.Author.SecUID, detail.Author.AvatarURL)
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
	if prof.SecUID != "" && prof.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.Douyin), prof.SecUID, prof.AvatarURL)
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
	return b.douyinUserWorks(ctx, secUID, cursor, nil)
}

func (b *Backend) douyinUserWorks(ctx context.Context, secUID, cursor string, known *douyin.UserProfile) (*DouyinUserWorksPage, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return nil, err
	}
	cleanCursor := strings.TrimSpace(cursor)
	page, err := b.douyin.ListUserPosts(ctx, douyin.UserPostsRequest{
		SecUID:            strings.TrimSpace(secUID),
		MaxCursor:         cleanCursor,
		KnownProfile:      known,
		CrossCheckProfile: known == nil && (cleanCursor == "" || cleanCursor == "0"),
		StreamOptions:     douyin.StreamSelectOptions{PreferredCodec: b.preferredDouyinCodec()},
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

// WeiboPost fetches and maps a single Weibo post by mid or mblogid.
func (b *Backend) WeiboPost(ctx context.Context, mid string) (*WeiboPostView, error) {
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return nil, err
	}
	codec := b.preferredWeiboCodec()
	maxRes := b.currentWeiboMaxResolution()
	post, err := b.weibo.GetPost(ctx, strings.TrimSpace(mid), weibo.PostOptions{
		StreamOptions: weibo.StreamSelectOptions{
			PreferredCodec: codec,
			MaxResolution:  maxRes,
		},
	})
	if err != nil {
		b.logError("WeiboPost", err)
		return nil, err
	}
	b.rememberWeiboPostAvatars(post)
	b.weiboPostCache.put(post, codec, maxRes)
	cacheKey := weibo.PostItemKey(post.Mid)
	var cached *library.ItemMetadata
	if item, ok, _ := b.lib.GetItem(cacheKey); ok {
		cached = &item
	}
	st := b.States([]string{cacheKey})[cacheKey]
	return mapWeiboPostView(post, cached, st), nil
}

// WeiboVideo fetches a standalone 1034:<id> Weibo video page.
// If the video resolves to a host Weibo post, HostPost is populated and CacheKey normalizes to weibo:dynamic:<mid>;
// otherwise CacheKey is weibo:video:<oid_underscored>.
func (b *Backend) WeiboVideo(ctx context.Context, oid string) (*WeiboVideoView, error) {
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return nil, err
	}
	codec := b.preferredWeiboCodec()
	maxRes := b.currentWeiboMaxResolution()
	vr, err := b.weibo.GetVideo(ctx, strings.TrimSpace(oid), weibo.PostOptions{
		StreamOptions: weibo.StreamSelectOptions{
			PreferredCodec: codec,
			MaxResolution:  maxRes,
		},
	})
	if err != nil {
		b.logError("WeiboVideo", err)
		return nil, err
	}
	if vr.HostPost != nil {
		b.rememberWeiboPostAvatars(vr.HostPost)
		b.weiboPostCache.put(vr.HostPost, codec, maxRes)
		hostKey := weibo.PostItemKey(vr.HostPost.Mid)
		var cached *library.ItemMetadata
		if item, ok, _ := b.lib.GetItem(hostKey); ok {
			cached = &item
		}
		st := b.States([]string{hostKey})[hostKey]
		hostView := mapWeiboPostView(vr.HostPost, cached, st)
		return &WeiboVideoView{
			ObjectID:     vr.ObjectID,
			HostMid:      vr.HostPost.Mid,
			HostPost:     hostView,
			Title:        firstNonEmpty(vr.Title, weiboPostSummaryTitle(hostView)),
			AuthorUID:    firstNonEmpty(vr.AuthorUID, hostView.Author.UID),
			AuthorName:   firstNonEmpty(vr.AuthorName, hostView.Author.ScreenName),
			Video:        mapWeiboVideoDTO(vr.Video),
			CanonicalURL: firstNonEmpty(hostView.CanonicalURL, vr.CanonicalURL),
			CacheKey:     hostView.CacheKey,
			CacheState:   hostView.CacheState,
			CachedItem:   hostView.CachedItem,
		}, nil
	}
	videoKey := weibo.VideoItemKey(vr.ObjectID)
	var cached *library.ItemMetadata
	if item, ok, _ := b.lib.GetItem(videoKey); ok {
		cached = &item
	}
	st := b.States([]string{videoKey})[videoKey]
	return &WeiboVideoView{
		ObjectID:     vr.ObjectID,
		HostMid:      vr.HostMid,
		Title:        vr.Title,
		AuthorUID:    vr.AuthorUID,
		AuthorName:   vr.AuthorName,
		Video:        mapWeiboVideoDTO(vr.Video),
		CanonicalURL: vr.CanonicalURL,
		CacheKey:     videoKey,
		CacheState:   st,
		CachedItem:   cached,
	}, nil
}

// WeiboUser fetches a Weibo creator's profile, saves statuses_count into account_works,
// and refreshes the local avatar cache for subscribed accounts.
func (b *Backend) WeiboUser(ctx context.Context, uid string) (*WeiboUserView, error) {
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return nil, err
	}
	targetUID := strings.TrimSpace(uid)
	if targetUID == "" || strings.EqualFold(targetUID, "self") {
		targetUID = b.weiboCred.Status().UID
		if targetUID == "" {
			selfProf, sErr := b.weibo.GetSelfProfile(ctx)
			if sErr != nil {
				return nil, sErr
			}
			targetUID = selfProf.UID
		}
	}
	prof, err := b.weibo.GetUserProfile(ctx, targetUID)
	if err != nil {
		b.logError("WeiboUser", err)
		return nil, err
	}
	if b.subs != nil {
		_ = b.subs.UpdateAccountSnapshot(string(site.Weibo), prof.UID, prof.ScreenName, prof.AvatarURL, "")
	}
	if prof.UID != "" && prof.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.Weibo), prof.UID, prof.AvatarURL)
	}
	b.recordAccountWorks(site.Weibo, prof.UID, prof.StatusesCount)
	return mapWeiboUserView(prof), nil
}

// WeiboUserPosts fetches one paginated timeline page for a Weibo creator.
// Cursor encodes (page, since_id) via weibo.FormatCursor / weibo.ParseCursor.
func (b *Backend) WeiboUserPosts(ctx context.Context, uid, cursor string) (*WeiboUserPostsPage, error) {
	page, _, err := b.weiboUserPosts(ctx, uid, cursor, weibo.FeatureAll, nil)
	return page, err
}

func (b *Backend) weiboUserPosts(
	ctx context.Context,
	uid, cursor string,
	feature int,
	known *weibo.UserProfile,
) (*WeiboUserPostsPage, []weibo.Post, error) {
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return nil, nil, err
	}
	pageNum, sinceID := weibo.ParseCursor(cursor)
	codec := b.preferredWeiboCodec()
	maxRes := b.currentWeiboMaxResolution()
	page, err := b.weibo.GetUserPosts(ctx, weibo.UserPostsRequest{
		UID:               strings.TrimSpace(uid),
		Page:              pageNum,
		SinceID:           sinceID,
		Feature:           feature,
		KnownProfile:      known,
		CrossCheckProfile: known == nil && pageNum == 1,
		StreamOptions: weibo.StreamSelectOptions{
			PreferredCodec: codec,
			MaxResolution:  maxRes,
		},
	})
	if err != nil {
		b.logError("WeiboUserPosts", err)
		return nil, nil, err
	}
	keys := make([]string, 0, len(page.Posts))
	for i := range page.Posts {
		if page.Posts[i].Mid != "" {
			keys = append(keys, weibo.PostItemKey(page.Posts[i].Mid))
		}
	}
	var states map[string]library.DotState
	if len(keys) > 0 {
		states = b.States(keys)
	}
	ownerUID := strings.TrimSpace(uid)
	if normUID, uErr := site.NormalizeUID(site.Weibo, ownerUID); uErr == nil {
		ownerUID = normUID
	}
	ownerName := ""
	if known != nil {
		ownerName = strings.TrimSpace(known.ScreenName)
	}
	if ownerName == "" && page.Author != nil {
		ownerName = strings.TrimSpace(page.Author.ScreenName)
	}
	if ownerName == "" && ownerUID != "" {
		ownerName = b.resolveWeiboForwarderHintName(ownerUID, "", "")
	}

	views := make([]WeiboPostView, 0, len(page.Posts))
	for i := range page.Posts {
		p := &page.Posts[i]
		b.weiboPostCache.put(p, codec, maxRes)
		k := weibo.PostItemKey(p.Mid)
		v := *mapWeiboPostView(p, nil, states[k])
		if ownerUID != "" && (p.IsRetweet || p.IsFastRetweet || p.Author.UID != ownerUID) {
			v.ForwarderHintUID = ownerUID
			v.ForwarderHintName = ownerName
		}
		views = append(views, v)
	}
	nextCursor := ""
	if page.HasMore {
		nextCursor = weibo.FormatCursor(page.NextPage, page.NextSinceID)
	}
	totalWorks := page.Total
	if totalWorks == 0 && known != nil && known.StatusesCount > 0 {
		totalWorks = known.StatusesCount
	}
	if totalWorks == 0 && page.Author != nil && page.Author.StatusesCount > 0 {
		totalWorks = page.Author.StatusesCount
	}
	if pageNum == 1 && totalWorks > 0 {
		if normUID, uErr := site.NormalizeUID(site.Weibo, uid); uErr == nil {
			b.recordAccountWorks(site.Weibo, normUID, totalWorks)
		}
	}
	return &WeiboUserPostsPage{
		Posts:             views,
		Page:              page.Page,
		NextCursor:        nextCursor,
		HasMore:           page.HasMore,
		BottomTipsVisible: page.BottomTipsVisible,
		BottomTipsText:    page.BottomTipsText,
		UncertainEmpty:    page.UncertainEmpty,
		UncertainReason:   page.UncertainReason,
		TotalWorks:        totalWorks,
	}, page.Posts, nil
}

func weiboPostSummaryTitle(pv *WeiboPostView) string {
	if pv == nil {
		return ""
	}
	raw := firstNonEmpty(pv.Text, pv.TextRaw)
	if raw == "" && pv.Video != nil {
		raw = pv.Video.Title
	}
	if raw == "" && pv.Card != nil {
		raw = pv.Card.Title
	}
	if raw == "" && pv.Retweeted != nil {
		raw = firstNonEmpty(pv.Retweeted.Text, pv.Retweeted.TextRaw)
	}
	return trimQueueSummaryTitle(firstNonEmpty(raw, "微博 "+pv.Mid))
}

func weiboPostPublicCoverURL(pv *WeiboPostView) string {
	if pv == nil {
		return ""
	}
	for _, img := range pv.Images {
		if u := weibo.SafePublicImageURL(firstNonEmpty(img.PublicPreviewURL, img.ImageURL)); u != "" {
			return u
		}
	}
	if pv.Video != nil {
		if u := weibo.SafePublicImageURL(firstNonEmpty(pv.Video.PublicCoverURL, pv.Video.CoverURL)); u != "" {
			return u
		}
	}
	if pv.Card != nil {
		if u := weibo.SafePublicImageURL(pv.Card.CoverURL); u != "" {
			return u
		}
	}
	if pv.Retweeted != nil {
		return weiboPostPublicCoverURL(pv.Retweeted)
	}
	return ""
}

func weiboPostThumbCoverURL(pv *WeiboPostView) string {
	if pv == nil {
		return ""
	}
	for _, img := range pv.Images {
		if u := firstNonEmpty(img.ImageURL, img.PublicPreviewURL); u != "" {
			return u
		}
	}
	if pv.Video != nil {
		if u := firstNonEmpty(pv.Video.CoverURL, pv.Video.PublicCoverURL); u != "" {
			return u
		}
	}
	if pv.Card != nil && pv.Card.CoverURL != "" {
		return pv.Card.CoverURL
	}
	if pv.Retweeted != nil {
		return weiboPostThumbCoverURL(pv.Retweeted)
	}
	return ""
}

func weiboPostDuration(pv *WeiboPostView) int {
	if pv == nil {
		return 0
	}
	if pv.Video != nil && pv.Video.DurationSec > 0 {
		return pv.Video.DurationSec
	}
	for _, img := range pv.Images {
		if img.DurationSec > 0 {
			return img.DurationSec
		}
	}
	if pv.Retweeted != nil {
		return weiboPostDuration(pv.Retweeted)
	}
	return 0
}

func weiboVideoPublicCoverURL(v *WeiboVideoDTO) string {
	if v == nil {
		return ""
	}
	return weibo.SafePublicImageURL(firstNonEmpty(v.PublicCoverURL, v.CoverURL))
}

func weiboVideoThumbCoverURL(v *WeiboVideoDTO) string {
	if v == nil {
		return ""
	}
	return firstNonEmpty(v.CoverURL, v.PublicCoverURL)
}

func weiboVideoDuration(v *WeiboVideoDTO) int {
	if v == nil {
		return 0
	}
	return v.DurationSec
}

// XPost fetches and maps a single X post by Snowflake status_id via GraphQL TweetDetail.
func (b *Backend) XPost(ctx context.Context, statusID string) (*XPostDTO, error) {
	if err := b.requireSiteLogin(site.X); err != nil {
		return nil, err
	}
	post, err := b.x.GetPost(ctx, strings.TrimSpace(statusID))
	if err != nil {
		b.logError("XPost", err)
		return nil, err
	}
	b.rememberXPostAvatars(post)
	b.xPostCache.put(post, true)
	cacheKey := x.PostItemKey(post.ID)
	var cached *library.ItemMetadata
	if item, ok, _ := b.lib.GetItem(cacheKey); ok {
		cached = &item
	}
	st := b.States([]string{cacheKey})[cacheKey]
	return mapXPostDTO(post, cached, st), nil
}

// XUser fetches an X creator's profile by permanent rest_id (or "self"), updates
// subscription snapshots & statuses_count, and attaches local subscription ownership.
func (b *Backend) XUser(ctx context.Context, restID string) (*XUserDTO, error) {
	if err := b.requireSiteLogin(site.X); err != nil {
		return nil, err
	}
	targetUID := strings.TrimSpace(restID)
	if targetUID == "" || strings.EqualFold(targetUID, "self") {
		targetUID = b.xCred.Status().UID
		if targetUID == "" {
			selfProf, sErr := b.x.GetSelfProfile(ctx)
			if sErr != nil {
				return nil, sErr
			}
			targetUID = selfProf.RestID
		}
	}
	prof, err := b.x.GetUserByRestId(ctx, targetUID)
	if err != nil {
		b.logError("XUser", err)
		return nil, err
	}
	return b.finalizeXUserProfile(prof), nil
}

// XUserByScreenName resolves an X creator's profile by mutable @screen_name.
func (b *Backend) XUserByScreenName(ctx context.Context, screenName string) (*XUserDTO, error) {
	if err := b.requireSiteLogin(site.X); err != nil {
		return nil, err
	}
	prof, err := b.x.GetUserByScreenName(ctx, strings.TrimSpace(screenName))
	if err != nil {
		b.logError("XUserByScreenName", err)
		return nil, err
	}
	return b.finalizeXUserProfile(prof), nil
}

func (b *Backend) finalizeXUserProfile(prof *x.UserProfile) *XUserDTO {
	if prof == nil {
		return nil
	}
	displayName := firstNonEmpty(prof.Name, prof.ScreenName, prof.RestID)
	if b.subs != nil {
		_ = b.subs.UpdateAccountSnapshot(string(site.X), prof.RestID, displayName, prof.AvatarURL, prof.ScreenName)
	}
	if prof.RestID != "" && prof.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.X), prof.RestID, prof.AvatarURL)
	}
	b.recordAccountWorks(site.X, prof.RestID, prof.StatusesCount)
	var ownerSubID, ownerSubName string
	if sub, ok := b.OwnerOfAccount(string(site.X), prof.RestID); ok {
		ownerSubID = sub.ID
		ownerSubName = sub.Name
	}
	return mapXUserDTO(prof, ownerSubID, ownerSubName)
}

// XUserPosts fetches one paginated timeline page for an X creator via GraphQL UserTweets.
func (b *Backend) XUserPosts(ctx context.Context, restID, cursor string) (*XUserPostsPageDTO, error) {
	page, _, err := b.xUserPosts(ctx, restID, cursor, nil)
	return page, err
}

func (b *Backend) xUserPosts(
	ctx context.Context,
	restID, cursor string,
	known *x.UserProfile,
) (*XUserPostsPageDTO, []x.Post, error) {
	if err := b.requireSiteLogin(site.X); err != nil {
		return nil, nil, err
	}
	cleanCursor := strings.TrimSpace(cursor)
	page, err := b.x.GetUserTweets(ctx, x.UserTweetsRequest{
		RestID:            strings.TrimSpace(restID),
		Cursor:            cleanCursor,
		KnownProfile:      known,
		CrossCheckProfile: known == nil && cleanCursor == "",
	})
	if err != nil {
		b.logError("XUserPosts", err)
		return nil, nil, err
	}
	keys := make([]string, 0, len(page.Posts))
	for i := range page.Posts {
		if page.Posts[i].ID != "" {
			keys = append(keys, x.PostItemKey(page.Posts[i].ID))
		}
	}
	var states map[string]library.DotState
	if len(keys) > 0 {
		states = b.States(keys)
	}
	ownerUID := strings.TrimSpace(restID)
	if normUID, uErr := site.NormalizeUID(site.X, ownerUID); uErr == nil {
		ownerUID = normUID
	}
	ownerName := ""
	if known != nil {
		ownerName = formatXDisplayName(known.Name, known.ScreenName)
	}
	if ownerName == "" && page.Author != nil {
		ownerName = formatXDisplayName(page.Author.Name, page.Author.ScreenName)
	}
	if ownerName == "" && ownerUID != "" {
		ownerName = b.resolveXForwarderHintName(ownerUID, "", "")
	}

	views := make([]XPostDTO, 0, len(page.Posts))
	for i := range page.Posts {
		p := &page.Posts[i]
		b.xPostCache.put(p, false)
		k := x.PostItemKey(p.ID)
		v := *mapXPostDTO(p, nil, states[k])
		if ownerUID != "" && (p.IsRetweet || p.IsQuote || p.Author.RestID != ownerUID) {
			v.ForwarderHintUID = ownerUID
			v.ForwarderHintName = ownerName
		}
		views = append(views, v)
	}
	var authorDTO *XAuthorDTO
	if page.Author != nil {
		a := mapXAuthorDTO(*page.Author)
		authorDTO = &a
	}
	totalWorks := 0
	if known != nil && known.StatusesCount > 0 {
		totalWorks = known.StatusesCount
	}
	if totalWorks == 0 && page.Author != nil && page.Author.StatusesCount > 0 {
		totalWorks = page.Author.StatusesCount
	}
	if cleanCursor == "" && totalWorks > 0 && ownerUID != "" {
		b.recordAccountWorks(site.X, ownerUID, totalWorks)
	}
	bottomTipsText := strings.TrimSpace(page.BottomTipsText)
	bottomTipsVisible := page.BottomTipsVisible
	if page.ReachedVisibleLimit && bottomTipsText == "" {
		bottomTipsText = x.VisibleTimelineLimitText
		bottomTipsVisible = true
	}
	nextCursor := ""
	if page.HasMore {
		nextCursor = page.NextCursor
	}
	return &XUserPostsPageDTO{
		Posts:               views,
		NextCursor:          nextCursor,
		HasMore:             page.HasMore,
		ReachedVisibleLimit: page.ReachedVisibleLimit,
		BottomTipsVisible:   bottomTipsVisible,
		BottomTipsText:      bottomTipsText,
		UncertainEmpty:      page.UncertainEmpty,
		UncertainReason:     page.UncertainReason,
		Author:              authorDTO,
		TotalWorks:          totalWorks,
	}, page.Posts, nil
}

func formatXDisplayName(name, screenName string) string {
	name = strings.TrimSpace(name)
	handle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(screenName), "@"))
	switch {
	case name != "" && handle != "":
		if strings.EqualFold(name, handle) || strings.EqualFold(name, "@"+handle) {
			return "@" + handle
		}
		return name + " (@" + handle + ")"
	case name != "":
		return name
	case handle != "":
		return "@" + handle
	default:
		return ""
	}
}

func xPostAuthorDisplayName(a XAuthorDTO) string {
	return firstNonEmpty(formatXDisplayName(a.Name, a.ScreenName), a.RestID)
}

func xPostSummaryTitle(pv *XPostDTO) string {
	if pv == nil {
		return ""
	}
	raw := strings.TrimSpace(pv.Text)
	if raw == "" && pv.Card != nil {
		raw = strings.TrimSpace(pv.Card.Title)
	}
	if raw == "" && pv.Retweeted != nil {
		raw = strings.TrimSpace(pv.Retweeted.Text)
	}
	if raw == "" && pv.Quoted != nil {
		raw = strings.TrimSpace(pv.Quoted.Text)
	}
	return trimQueueSummaryTitle(firstNonEmpty(raw, "X 帖子 "+pv.ID))
}

func xPostPublicCoverURL(pv *XPostDTO) string {
	if pv == nil {
		return ""
	}
	for _, m := range pv.Media {
		if u := x.SafePublicImageURL(firstNonEmpty(m.PublicPreviewURL, m.ImageURL)); u != "" {
			return u
		}
	}
	if pv.Card != nil {
		if u := x.SafePublicImageURL(pv.Card.CoverURL); u != "" {
			return u
		}
	}
	if pv.Retweeted != nil {
		if u := xPostPublicCoverURL(pv.Retweeted); u != "" {
			return u
		}
	}
	if pv.Quoted != nil {
		if u := xPostPublicCoverURL(pv.Quoted); u != "" {
			return u
		}
	}
	return ""
}

func xPostThumbCoverURL(pv *XPostDTO) string {
	if pv == nil {
		return ""
	}
	for _, m := range pv.Media {
		if u := firstNonEmpty(m.PublicPreviewURL, m.ImageURL); u != "" {
			return u
		}
	}
	if pv.Card != nil && pv.Card.CoverURL != "" {
		return pv.Card.CoverURL
	}
	if pv.Retweeted != nil {
		if u := xPostThumbCoverURL(pv.Retweeted); u != "" {
			return u
		}
	}
	if pv.Quoted != nil {
		if u := xPostThumbCoverURL(pv.Quoted); u != "" {
			return u
		}
	}
	return ""
}

func xPostDuration(pv *XPostDTO) int {
	if pv == nil {
		return 0
	}
	for _, m := range pv.Media {
		if m.DurationSec > 0 {
			return m.DurationSec
		}
	}
	if pv.Retweeted != nil {
		if d := xPostDuration(pv.Retweeted); d > 0 {
			return d
		}
	}
	if pv.Quoted != nil {
		if d := xPostDuration(pv.Quoted); d > 0 {
			return d
		}
	}
	return 0
}

func (b *Backend) resolveXForwarderHintName(hintUID, postAuthorUID, postAuthorName string) string {
	hintUID = strings.TrimSpace(hintUID)
	if hintUID == "" {
		return ""
	}
	if strings.TrimSpace(postAuthorUID) == hintUID && strings.TrimSpace(postAuthorName) != "" {
		return strings.TrimSpace(postAuthorName)
	}
	if b.subs != nil {
		if sub, acc, ok := b.subs.GetAccount(string(site.X), hintUID); ok {
			return firstNonEmpty(formatXDisplayName(acc.Name, acc.UniqueID), acc.Name, sub.Name)
		}
	}
	return ""
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

// StagePreviewMedia downloads a whitelisted CDN audio/video clip (e.g. uncached Douyin note BGM,
// note video page, or Weibo LivePhoto clip) without sending Cookie into <DataDir>/cache/preview/
// and returns its local file path for playback via internal/player.
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
	isWeiboLivePhoto := false
	switch id {
	case site.Douyin:
		var fetchErr error
		data, _, fetchErr = b.douyin.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	case site.Weibo:
		if b.weibo == nil {
			return "", &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "微博客户端未初始化"}
		}
		liveSpec := b.weibo.LivePhotoMediaTransport()
		normalizedURL := normalizeWeiboPreviewURL(rawURL, liveSpec.AllowLocalhost)
		isWeiboLivePhoto = site.IsWeiboLivePhotoInitialURL(normalizedURL)
		spec := b.weibo.MediaTransport()
		if isWeiboLivePhoto {
			spec = liveSpec
		}
		var fetchErr error
		data, fetchErr = b.fetchWeiboPreviewMedia(ctx, spec, normalizedURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	case site.Bilibili:
		var fetchErr error
		data, fetchErr = b.bili.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	case site.X:
		if b.x == nil {
			return "", &site.Error{Site: site.X, Kind: site.ErrKindUnavailable, Message: "X 客户端未初始化"}
		}
		var fetchErr error
		data, _, fetchErr = b.x.FetchImageLimited(ctx, rawURL, maxPreviewMediaBytes)
		if fetchErr != nil {
			return "", fetchErr
		}
	default:
		return "", &site.Error{Site: id, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
	safeExt := ".mp4"
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".mov", "mov":
		safeExt = ".mov"
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
	case ".jpg", "jpg", ".jpeg", "jpeg":
		safeExt = ".jpg"
	case ".png", "png":
		safeExt = ".png"
	case ".gif", "gif":
		safeExt = ".gif"
	case ".webp", "webp":
		safeExt = ".webp"
	}
	needsLivePhotoMP4 := id == site.Weibo && (isWeiboLivePhoto || safeExt == ".mov" || isQuickTimeContainerBytes(data))
	if needsLivePhotoMP4 {
		safeExt = ".mp4"
	}
	sum := sha256.Sum256([]byte(string(id) + "\x00" + rawURL))
	fileName := fmt.Sprintf("preview_%s_%x%s", string(id), sum[:16], safeExt)
	fullPath := filepath.Join(b.previewDir, fileName)
	if needsLivePhotoMP4 {
		if err := os.MkdirAll(b.previewDir, 0o700); err != nil {
			return "", err
		}
		stagePath := storage.TempSiblingPath(fullPath)
		cleanupStage := true
		defer func() {
			if cleanupStage {
				_ = os.Remove(stagePath)
			}
		}()
		if err := os.WriteFile(stagePath, data, 0o600); err != nil {
			return "", err
		}
		if _, _, err := download.EnsureLivePhotoMP4Context(ctx, stagePath, 0); err != nil {
			return "", err
		}
		if err := os.Rename(stagePath, fullPath); err != nil {
			return "", err
		}
		cleanupStage = false
	} else {
		if err := storage.WriteFileAtomic(fullPath, data, 0o600); err != nil {
			return "", err
		}
	}
	prunePreviewDir(b.previewDir, fullPath, maxPreviewDirBytes, maxPreviewDirFiles)
	return fullPath, nil
}

func normalizeWeiboPreviewURL(raw string, allowLocalhost bool) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "//") {
		return "https:" + s
	}
	if strings.HasPrefix(strings.ToLower(s), "http://") {
		if allowLocalhost {
			if u, err := url.Parse(s); err == nil && site.IsLoopbackHost(u.Hostname()) {
				return s
			}
		}
		return "https://" + s[len("http://"):]
	}
	return s
}

func isQuickTimeContainerBytes(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "qt  "
}

func looksLikePreviewHTMLOrJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return true
	}
	if trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '<' {
		lower := strings.ToLower(string(trimmed[:min(len(trimmed), 256)]))
		if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") || strings.HasPrefix(lower, "<head") || strings.HasPrefix(lower, "<script") || strings.HasPrefix(lower, "<body") {
			return true
		}
		if trimmed[0] == '{' || trimmed[0] == '[' {
			return true
		}
	}
	return false
}

func (b *Backend) resolveWeiboForwarderHintName(hintUID, postAuthorUID, postAuthorName string) string {
	hintUID = strings.TrimSpace(hintUID)
	if hintUID == "" {
		return ""
	}
	if strings.TrimSpace(postAuthorUID) == hintUID && strings.TrimSpace(postAuthorName) != "" {
		return strings.TrimSpace(postAuthorName)
	}
	if b.subs != nil {
		if sub, acc, ok := b.subs.GetAccount(string(site.Weibo), hintUID); ok {
			return firstNonEmpty(acc.Name, sub.Name)
		}
	}
	return ""
}

func (b *Backend) fetchWeiboPreviewMedia(ctx context.Context, spec site.MediaTransportSpec, rawURL string, maxBytes int64) ([]byte, error) {
	if err := spec.ValidateURL(rawURL); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = maxPreviewMediaBytes
	}

	var rt http.RoundTripper
	var ownedTransport *http.Transport
	if b.weiboPreviewRT != nil {
		rt = b.weiboPreviewRT
	} else {
		ownedTransport = &http.Transport{
			Proxy: site.Proxy,
			DialContext: site.NewGuardedMediaDialer(
				site.Weibo,
				spec.AllowLocalhost,
				&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
				"媒体地址解析到受限的本地或内网地址",
			).DialContext,
		}
		defer ownedTransport.CloseIdleConnections()
		rt = ownedTransport
		if spec.AllowLocalhost {
			rt = site.LoopbackTransport(ownedTransport)
		}
	}
	if b.weiboPreviewWrap != nil {
		rt = b.weiboPreviewWrap(rt)
	}

	const maxHops = 5
	client := &http.Client{
		Transport: rt,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxHops {
				return &site.Error{Site: site.Weibo, Kind: site.ErrKindBlocked, Message: "微博预览媒体重定向次数过多"}
			}
			if err := spec.ValidateRedirectURL(req.URL.String()); err != nil {
				return err
			}
			req.Header.Del("Cookie")
			spec.ApplyHeaders(req)
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindBadRequest, Message: "无效的微博预览媒体地址", Err: site.UnwrapURLError(err)}
	}
	req.Header.Del("Cookie")
	spec.ApplyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return nil, se
		}
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindNetwork, Message: "拉取微博预览媒体失败", Err: site.UnwrapURLError(err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		kind := site.ErrKindUnavailable
		switch resp.StatusCode {
		case 414, 418, 432:
			kind = site.ErrKindRiskControl
		case http.StatusTooManyRequests:
			kind = site.ErrKindRateLimited
		}
		return nil, &site.Error{
			Site:       site.Weibo,
			Kind:       kind,
			HTTPStatus: resp.StatusCode,
			Message:    fmt.Sprintf("微博预览媒体返回 HTTP %d", resp.StatusCode),
		}
	}

	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain") {
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindBlocked, Message: "微博预览媒体响应类型异常: " + ct}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindNetwork, Message: "读取微博预览媒体失败", Err: err}
	}
	if int64(len(data)) > maxBytes {
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindBlocked, Message: "微博预览媒体体积超出安全上限"}
	}
	if looksLikePreviewHTMLOrJSON(data) {
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindBlocked, Message: "微博预览媒体响应内容疑似伪装页面"}
	}
	return data, nil
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

// OpenExternalLink validates rawURL strictly as an HTTP/HTTPS URL without
// credentials or control characters and opens it via the Desktop shell adapter
// (recorded in memory without launching external processes under MockDesktop / TRACE_QUIET).
func (b *Backend) OpenExternalLink(rawURL string) error {
	clean, err := subscription.ValidateCustomLinkURL(rawURL)
	if err != nil {
		return err
	}
	if b.desktop == nil {
		return fmt.Errorf("desktop handler unavailable")
	}
	return b.desktop.OpenExternalURL(clean)
}
