package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
)

// AccountService serves 我的 (FR-ACCT-001～005, DEC-047): read-only lists of the
// signed-in accounts. Results live only in the caller's memory: nothing here
// writes files, the feed or recent parses, and the log only records the list,
// page number, item count and error class.
type AccountService interface {
	AccountSections(siteID string) []AccountSection
	AccountFolders(ctx context.Context, siteID, kind string) ([]AccountFolder, error)
	AccountPage(ctx context.Context, siteID, kind, folderID, cursor string) (AccountPage, error)
}

// Account list kinds.
const (
	AccountFollowing    = "following"
	AccountFavorites    = "favorites"     // 哔哩哔哩收藏夹
	AccountToView       = "toview"        // 哔哩哔哩稍后再看
	AccountHistory      = "history"       // 哔哩哔哩历史记录
	AccountLikes        = "likes"         // 抖音/微博/X 喜欢
	AccountCollection   = "collection"    // 抖音收藏
	AccountWatchHistory = "watch_history" // 抖音观看历史
	AccountBookmarks    = "bookmarks"     // X 书签
	AccountLists        = "lists"         // X 列表（条件，默认关闭）
	AccountLogin        = "login"         // 未登录或失效时的唯一条目

	// Folder ids that are not site ids.
	AccountFolderAll        = "all"
	AccountFolderUnfinished = "unfinished"
)

// Account item kinds.
const (
	AccountItemVideo       = "video"
	AccountItemWork        = "work"
	AccountItemPost        = "post"
	AccountItemUser        = "user"
	AccountItemUnsupported = "unsupported"
)

// AccountSection is one function under a site in 我的.
type AccountSection struct {
	Site      string `json:"site"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// AccountFolder is a selectable sub-list: a following group, a favourites folder, a 收藏夹 or a filter.
type AccountFolder struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Count     int    `json:"count"`
	Private   bool   `json:"private,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// AccountItem is one row or card of an account list, independent of the site.
type AccountItem struct {
	Kind       string `json:"kind"`
	Site       string `json:"site"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Cover      string `json:"cover,omitempty"`
	Author     string `json:"author,omitempty"`
	AuthorID   string `json:"authorId,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Progress   int    `json:"progress,omitempty"`
	Finished   bool   `json:"finished,omitempty"`
	ViewedAt   int64  `json:"viewedAt,omitempty"`
	FavAt      int64  `json:"favAt,omitempty"`
	Invalid    bool   `json:"invalid,omitempty"`
	TypeLabel  string `json:"typeLabel,omitempty"`
	Live       bool   `json:"live,omitempty"`
	OpenURL    string `json:"openUrl,omitempty"`
	BrowserURL string `json:"browserUrl,omitempty"`
	CacheKey   string `json:"cacheKey,omitempty"`
	// Users only.
	Signature  string `json:"signature,omitempty"`
	UniqueID   string `json:"uniqueId,omitempty"`
	AccountKey string `json:"accountKey,omitempty"`
	OwnerID    string `json:"ownerId,omitempty"`
	OwnerName  string `json:"ownerName,omitempty"`
	// Douyin works only: the same card the user page renders.
	Work *DouyinWorkCardDTO `json:"work,omitempty"`
	// Weibo posts only: the normalized post card for 我的 › 微博.
	Post *WeiboPostView `json:"post,omitempty"`
	// X posts only: the normalized post card for 我的 › X.
	XPost *XPostDTO `json:"xPost,omitempty"`
}

// AccountPage is one page of an account list. Cursor is opaque; "" asks for the first page.
type AccountPage struct {
	Items   []AccountItem `json:"items"`
	Cursor  string        `json:"cursor,omitempty"`
	HasMore bool          `json:"hasMore"`
	Total   int           `json:"total,omitempty"`
}

var _ AccountService = (*Backend)(nil)

// Feature flags mirroring site package defaults so tests can flip them without mutating constants.
var (
	accountWatchHistoryEnabled      = douyin.WatchHistoryEnabled
	accountWeiboLikesEnabled        = weibo.LikesEnabled
	accountWeiboFollowGroupsEnabled = weibo.FollowGroupsEnabled
	accountXBookmarkFoldersEnabled  = x.BookmarkFoldersEnabled
	accountXListsEnabled            = x.ListsEnabled
)

// AccountSections lists the functions of siteID; a site that is not signed in
// gets a single unavailable "login" entry and nothing else.
func (b *Backend) AccountSections(siteID string) []AccountSection {
	sid := site.ID(strings.ToLower(strings.TrimSpace(siteID)))
	var st credential.Status
	var labels [][2]string
	switch sid {
	case site.Bilibili:
		if b.cred != nil {
			st = b.cred.Status()
		}
		labels = [][2]string{{AccountFollowing, "关注"}, {AccountFavorites, "收藏夹"}, {AccountToView, "稍后再看"}, {AccountHistory, "历史记录"}}
	case site.Douyin:
		if b.douyinCred != nil {
			st = b.douyinCred.Status()
		}
		labels = [][2]string{{AccountFollowing, "关注"}, {AccountLikes, "喜欢"}, {AccountCollection, "收藏"}}
		if accountWatchHistoryEnabled {
			labels = append(labels, [2]string{AccountWatchHistory, "观看历史"})
		}
	case site.Weibo:
		if b.weiboCred != nil {
			st = b.weiboCred.Status()
		}
		labels = [][2]string{{AccountFollowing, "关注"}, {AccountFavorites, "收藏"}}
		if accountWeiboLikesEnabled {
			labels = append(labels, [2]string{AccountLikes, "赞过"})
		}
	case site.X:
		if b.xCred != nil {
			st = b.xCred.Status()
		}
		labels = [][2]string{{AccountFollowing, "关注"}, {AccountBookmarks, "书签"}, {AccountLikes, "喜欢"}}
		if accountXListsEnabled {
			labels = append(labels, [2]string{AccountLists, "列表"})
		}
	default:
		return nil
	}
	if b.requireSiteLogin(sid) != nil {
		reason := "未登录"
		switch st.State {
		case "expired", "refresh_failed", "decrypt_failed":
			reason = "登录已失效"
		}
		return []AccountSection{{Site: string(sid), Kind: AccountLogin, Label: reason, Reason: reason}}
	}
	out := make([]AccountSection, 0, len(labels))
	for _, l := range labels {
		out = append(out, AccountSection{Site: string(sid), Kind: l[0], Label: l[1], Available: true})
	}
	return out
}

// AccountFolders lists the sub-lists of a function that has them.
func (b *Backend) AccountFolders(ctx context.Context, siteID, kind string) ([]AccountFolder, error) {
	sid, err := b.accountGate(siteID, kind)
	if err != nil {
		return nil, err
	}
	var out []AccountFolder
	switch {
	case sid == site.Bilibili && kind == AccountFollowing:
		tags, err := b.bili.RelationTags(ctx)
		if err != nil {
			return nil, b.accountFail(sid, kind, err)
		}
		out = append(out, AccountFolder{ID: AccountFolderAll, Title: "全部关注", IsDefault: true})
		for _, t := range tags {
			out = append(out, AccountFolder{ID: strconv.FormatInt(t.TagID, 10), Title: t.Name, Count: t.Count})
		}
	case sid == site.Bilibili && kind == AccountFavorites:
		folders, err := b.bili.FavFolders(ctx, b.cred.Status().Mid)
		if err != nil {
			return nil, b.accountFail(sid, kind, err)
		}
		for _, f := range folders {
			out = append(out, AccountFolder{ID: f.ID, Title: f.Title, Count: f.Count, Private: f.Private, IsDefault: f.IsDefault})
		}
	case sid == site.Bilibili && kind == AccountToView:
		return []AccountFolder{{ID: AccountFolderAll, Title: "全部", IsDefault: true}, {ID: AccountFolderUnfinished, Title: "未看完"}}, nil
	case sid == site.Douyin && kind == AccountCollection:
		out = append(out, AccountFolder{ID: AccountFolderAll, Title: "全部收藏", IsDefault: true})
		cursor := ""
		// The folder list pages 10 at a time; read at most 5 pages for the dropdown.
		for i := 0; i < 5; i++ {
			page, err := b.douyin.CollectFolders(ctx, cursor)
			if err != nil {
				return nil, b.accountFail(sid, kind, err)
			}
			for _, f := range page.Folders {
				out = append(out, AccountFolder{ID: f.ID, Title: f.Name, Count: f.Count})
			}
			if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
				break
			}
			cursor = page.NextCursor
		}
	case sid == site.Weibo && kind == AccountFollowing:
		if !accountWeiboFollowGroupsEnabled {
			return nil, nil
		}
		groups, err := b.weibo.ListFollowGroups(ctx)
		if err != nil {
			return nil, b.accountFail(sid, kind, err)
		}
		out = append(out, AccountFolder{ID: AccountFolderAll, Title: "全部关注", IsDefault: true})
		for _, g := range groups {
			out = append(out, AccountFolder{ID: g.ID, Title: g.Title, Count: g.Count})
		}
	case sid == site.X && kind == AccountBookmarks:
		if !accountXBookmarkFoldersEnabled {
			return nil, nil
		}
		out = append(out, AccountFolder{ID: AccountFolderAll, Title: "全部书签", IsDefault: true})
	default:
		return nil, nil
	}
	b.Logf("account.%s.%s folders=%d", sid, kind, len(out))
	return out, nil
}

// AccountPage fetches one page. folderID selects a group, folder or filter
// where the function has them ("" picks the default).
func (b *Backend) AccountPage(ctx context.Context, siteID, kind, folderID, cursor string) (AccountPage, error) {
	sid, err := b.accountGate(siteID, kind)
	if err != nil {
		return AccountPage{}, err
	}
	pageNo, raw := splitAccountCursor(cursor)
	var page AccountPage
	switch sid {
	case site.Bilibili:
		page, err = b.biliAccountPage(ctx, kind, folderID, pageNo, raw)
	case site.Douyin:
		page, err = b.douyinAccountPage(ctx, kind, folderID, raw)
	case site.Weibo:
		page, err = b.weiboAccountPage(ctx, kind, folderID, pageNo)
	case site.X:
		page, err = b.xAccountPage(ctx, kind, folderID, pageNo, raw)
	default:
		err = &site.Error{Site: sid, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
	if err != nil {
		return AccountPage{}, b.accountFail(sid, kind, err)
	}
	if page.HasMore {
		page.Cursor = joinAccountCursor(pageNo+1, page.Cursor)
	} else {
		page.Cursor = ""
	}
	if page.Items == nil {
		page.Items = []AccountItem{}
	}
	b.Logf("account.%s.%s page=%d items=%d more=%v", sid, kind, pageNo, len(page.Items), page.HasMore)
	return page, nil
}

func (b *Backend) biliAccountPage(ctx context.Context, kind, folderID string, pn int, raw string) (AccountPage, error) {
	const ps = 20
	switch kind {
	case AccountFollowing:
		var users []bilibili.FollowingUser
		var total int
		if folderID == "" || folderID == AccountFolderAll {
			p, err := b.bili.RelationFollowings(ctx, b.cred.Status().Mid, pn, ps)
			if err != nil {
				return AccountPage{}, err
			}
			users, total = p.Items, p.Total
		} else {
			tag, err := strconv.ParseInt(folderID, 10, 64)
			if err != nil {
				return AccountPage{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "关注分组无效"}
			}
			users, err = b.bili.RelationTagUsers(ctx, b.cred.Status().Mid, tag, pn, ps)
			if err != nil {
				return AccountPage{}, err
			}
		}
		out := AccountPage{Total: total, Items: make([]AccountItem, 0, len(users))}
		for _, u := range users {
			out.Items = append(out.Items, b.accountUser(site.Bilibili, strconv.FormatInt(u.Mid, 10), u.Uname, u.Face, u.Sign, ""))
		}
		if total > 0 {
			out.HasMore = len(users) > 0 && (pn-1)*ps+len(users) < total
		} else {
			out.HasMore = len(users) == ps
		}
		return out, nil

	case AccountFavorites:
		mediaID := folderID
		if mediaID == "" || mediaID == AccountFolderAll {
			folders, err := b.bili.FavFolders(ctx, b.cred.Status().Mid)
			if err != nil {
				return AccountPage{}, err
			}
			if len(folders) == 0 {
				return AccountPage{}, nil
			}
			mediaID = folders[0].ID
		}
		p, err := b.bili.FavResources(ctx, mediaID, pn)
		if err != nil {
			return AccountPage{}, err
		}
		out := AccountPage{HasMore: p.HasMore, Total: p.Total, Items: make([]AccountItem, 0, len(p.Items))}
		for _, f := range p.Items {
			it := AccountItem{Site: string(site.Bilibili), ID: f.ID, Title: f.Title, Cover: f.Cover, Author: f.UpperName, AuthorID: midString(f.UpperMid), Duration: f.Duration, FavAt: f.FavAt, TypeLabel: f.TypeLabel}
			switch {
			case f.Invalid:
				it.Kind, it.Invalid, it.Title, it.Cover, it.TypeLabel = AccountItemUnsupported, true, "已失效", "", ""
			case f.Kind == bilibili.FavKindVideo:
				biliVideoItem(&it, f.BVID)
			case f.Kind == bilibili.FavKindAudio:
				it.Kind, it.BrowserURL = AccountItemUnsupported, safeBrowserURL("https://www.bilibili.com/audio/au"+f.ID)
			case f.Kind == bilibili.FavKindOGV && f.SeasonID != "":
				it.Kind, it.BrowserURL = AccountItemUnsupported, safeBrowserURL("https://www.bilibili.com/bangumi/play/ss"+f.SeasonID)
			default:
				it.Kind = AccountItemUnsupported
			}
			out.Items = append(out.Items, it)
		}
		return out, nil

	case AccountToView:
		p, err := b.bili.ToView(ctx, pn, folderID == AccountFolderUnfinished)
		if err != nil {
			return AccountPage{}, err
		}
		out := AccountPage{HasMore: p.HasMore, Total: p.Total, Items: make([]AccountItem, 0, len(p.Items))}
		for _, v := range p.Items {
			it := AccountItem{Site: string(site.Bilibili), ID: v.BVID, Title: v.Title, Cover: v.Cover, Author: v.OwnerName, AuthorID: midString(v.OwnerMid), Duration: v.Duration, Progress: v.Progress, Finished: v.Finished}
			if v.Unsupported {
				it.Kind, it.TypeLabel, it.ID = AccountItemUnsupported, "番剧或课程", strconv.FormatInt(v.AID, 10)
				if v.BVID != "" {
					it.BrowserURL = safeBrowserURL("https://www.bilibili.com/video/" + v.BVID)
				}
			} else {
				biliVideoItem(&it, v.BVID)
			}
			out.Items = append(out.Items, it)
		}
		return out, nil

	case AccountHistory:
		cur, err := parseHistoryCursor(raw)
		if err != nil {
			return AccountPage{}, err
		}
		p, err := b.bili.WatchHistory(ctx, cur)
		if err != nil {
			return AccountPage{}, err
		}
		out := AccountPage{HasMore: p.HasMore, Items: make([]AccountItem, 0, len(p.Items))}
		if p.HasMore {
			out.Cursor = fmt.Sprintf("%d:%d:%s", p.Next.Max, p.Next.ViewAt, p.Next.Business)
		}
		for _, h := range p.Items {
			it := AccountItem{Site: string(site.Bilibili), Title: h.Title, Cover: h.Cover, Author: h.AuthorName, AuthorID: midString(h.AuthorMid), Duration: h.Duration, Progress: h.Progress, Finished: h.Finished, ViewedAt: h.ViewAt, TypeLabel: h.TypeLabel, Live: h.Live}
			switch h.Business {
			case bilibili.HistoryArchive:
				it.ID = h.BVID
				biliVideoItem(&it, h.BVID)
			case bilibili.HistoryLive:
				it.Kind, it.ID = AccountItemUser, strconv.FormatInt(h.OID, 10)
				if h.AuthorMid > 0 {
					it.OpenURL = "https://space.bilibili.com/" + midString(h.AuthorMid)
				} else {
					it.Kind = AccountItemUnsupported
				}
				it.BrowserURL = safeBrowserURL("https://live.bilibili.com/" + strconv.FormatInt(h.OID, 10))
			default:
				it.Kind, it.ID, it.BrowserURL = AccountItemUnsupported, strconv.FormatInt(h.OID, 10), safeBrowserURL(h.URI)
			}
			out.Items = append(out.Items, it)
		}
		return out, nil
	}
	return AccountPage{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
}

func (b *Backend) douyinAccountPage(ctx context.Context, kind, folderID, raw string) (AccountPage, error) {
	if kind == AccountFollowing {
		p, err := b.douyin.Followings(ctx, b.douyinCred.Status().SecUID, raw)
		if err != nil {
			return AccountPage{}, err
		}
		out := AccountPage{HasMore: p.HasMore, Total: p.Total, Cursor: p.NextMaxTime, Items: make([]AccountItem, 0, len(p.Users))}
		for _, u := range p.Users {
			out.Items = append(out.Items, b.accountUser(site.Douyin, u.SecUID, u.Nickname, u.AvatarURL, u.Signature, u.UniqueID))
		}
		return out, nil
	}
	var p *douyin.AccountWorksPage
	var err error
	switch kind {
	case AccountLikes:
		p, err = b.douyin.Favorites(ctx, b.douyinCred.Status().SecUID, raw)
	case AccountCollection:
		if folderID == "" || folderID == AccountFolderAll {
			p, err = b.douyin.Collection(ctx, raw)
		} else {
			p, err = b.douyin.CollectFolderWorks(ctx, folderID, raw)
		}
	case AccountWatchHistory:
		p, err = b.douyin.WatchHistory(ctx, raw)
	default:
		return AccountPage{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
	}
	if err != nil {
		return AccountPage{}, err
	}
	out := AccountPage{HasMore: p.HasMore, Cursor: p.NextCursor, Items: make([]AccountItem, 0, len(p.Works))}
	for _, w := range p.Works {
		card := mapDouyinWorkCard(w)
		it := AccountItem{Site: string(site.Douyin), ID: w.ID, Title: card.Title, Cover: card.CoverURL, Author: w.Author.Nickname, AuthorID: w.Author.SecUID, Duration: w.DurationSec, Work: &card}
		if w.Kind == douyin.WorkKindUnsupported {
			it.Kind, it.TypeLabel = AccountItemUnsupported, "暂不支持的作品"
			it.BrowserURL = safeBrowserURL("https://www.douyin.com/video/" + w.ID)
		} else {
			it.Kind, it.CacheKey = AccountItemWork, card.CacheKey
			it.OpenURL = firstNonEmpty(safeBrowserURL(card.CanonicalURL), "https://www.douyin.com/video/"+w.ID)
			it.BrowserURL = it.OpenURL
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

func (b *Backend) snapshotWeiboAccountSeen(bucket string, reset bool) (map[string]bool, uint64) {
	return b.snapshotWeiboSeenMap(&b.accountWeiboSeen, bucket, reset)
}

func (b *Backend) mergeWeiboAccountSeen(bucket string, gen uint64, seen map[string]bool) {
	b.mergeWeiboSeenMap(&b.accountWeiboSeen, bucket, gen, seen)
}

func (b *Backend) weiboAccountPage(ctx context.Context, kind, folderID string, pn int) (AccountPage, error) {
	selfUID := ""
	if b.weiboCred != nil {
		selfUID = strings.TrimSpace(b.weiboCred.Status().UID)
	}
	if selfUID == "" {
		return AccountPage{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindAuthRequired, Message: "缺少登录账号的微博 UID，请重新登录微博"}
	}
	if pn <= 0 {
		pn = 1
	}

	switch kind {
	case AccountFollowing:
		bucket := kind + ":" + strings.TrimSpace(folderID)
		seen, gen := b.snapshotWeiboAccountSeen(bucket, pn <= 1)
		var p *weibo.FollowingPage
		var err error
		if folderID == "" || folderID == AccountFolderAll {
			p, err = b.weibo.ListFollowing(ctx, selfUID, pn, seen)
		} else {
			if !accountWeiboFollowGroupsEnabled {
				return AccountPage{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
			}
			p, err = b.weibo.ListFollowGroupMembers(ctx, selfUID, folderID, pn, seen)
		}
		if err != nil {
			return AccountPage{}, err
		}
		b.mergeWeiboAccountSeen(bucket, gen, seen)
		out := AccountPage{HasMore: p.HasMore, Total: p.Total, Cursor: p.NextCursor, Items: make([]AccountItem, 0, len(p.Users))}
		for _, u := range p.Users {
			out.Items = append(out.Items, b.accountUser(site.Weibo, u.UID, u.ScreenName, u.AvatarURL, u.Description, ""))
		}
		return out, nil

	case AccountFavorites:
		seen, gen := b.snapshotWeiboAccountSeen(kind, pn <= 1)
		p, err := b.weibo.ListFavorites(ctx, selfUID, pn, seen)
		if err != nil {
			return AccountPage{}, err
		}
		b.mergeWeiboAccountSeen(kind, gen, seen)
		return b.mapWeiboAccountPostsPage(p), nil

	case AccountLikes:
		if !accountWeiboLikesEnabled {
			return AccountPage{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
		}
		seen, gen := b.snapshotWeiboAccountSeen(kind, pn <= 1)
		p, err := b.weibo.ListLikes(ctx, selfUID, pn, seen)
		if err != nil {
			return AccountPage{}, err
		}
		b.mergeWeiboAccountSeen(kind, gen, seen)
		return b.mapWeiboAccountPostsPage(p), nil

	default:
		return AccountPage{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
	}
}

func (b *Backend) mapWeiboAccountPostsPage(p *weibo.AccountPostsPage) AccountPage {
	if p == nil {
		return AccountPage{}
	}
	keys := make([]string, 0, len(p.Posts))
	for i := range p.Posts {
		if p.Posts[i].Mid != "" {
			keys = append(keys, weibo.PostItemKey(p.Posts[i].Mid))
		}
	}
	var states map[string]library.DotState
	if len(keys) > 0 {
		states = b.States(keys)
	}
	out := AccountPage{
		HasMore: p.HasMore,
		Total:   p.Total,
		Items:   make([]AccountItem, 0, len(p.Posts)),
	}
	for i := range p.Posts {
		post := &p.Posts[i]
		key := weibo.PostItemKey(post.Mid)
		pv := mapWeiboPostView(post, nil, states[key])
		openURL := firstNonEmpty(safeBrowserURL(post.CanonicalURL), "https://weibo.com/detail/"+post.Mid)
		it := AccountItem{
			Kind:       AccountItemPost,
			Site:       string(site.Weibo),
			ID:         post.Mid,
			Title:      weiboPostSummaryTitle(pv),
			Cover:      weiboPostThumbCoverURL(pv),
			Author:     post.Author.ScreenName,
			AuthorID:   post.Author.UID,
			Duration:   weiboPostDuration(pv),
			OpenURL:    openURL,
			BrowserURL: safeBrowserURL(openURL),
			CacheKey:   pv.CacheKey,
			Post:       pv,
		}
		out.Items = append(out.Items, it)
	}
	return out
}

func (b *Backend) snapshotXAccountSeen(bucket string, reset bool) (map[string]bool, uint64) {
	return b.snapshotXSeenMap(&b.accountXSeen, bucket, reset)
}

func (b *Backend) mergeXAccountSeen(bucket string, gen uint64, seen map[string]bool) {
	b.mergeXSeenMap(&b.accountXSeen, bucket, gen, seen)
}

func (b *Backend) xAccountPage(ctx context.Context, kind, folderID string, pn int, raw string) (AccountPage, error) {
	selfUID := ""
	if b.xCred != nil {
		selfUID = strings.TrimSpace(b.xCred.Status().UID)
	}
	if selfUID == "" {
		return AccountPage{}, &site.Error{Site: site.X, Kind: site.ErrKindAuthRequired, Message: "缺少登录账号的 X rest_id，请重新登录 X"}
	}
	reset := pn <= 1 && strings.TrimSpace(raw) == ""
	switch kind {
	case AccountFollowing:
		seen, gen := b.snapshotXAccountSeen(kind, reset)
		p, err := b.x.ListFollowing(ctx, selfUID, raw, seen)
		if err != nil {
			return AccountPage{}, err
		}
		b.mergeXAccountSeen(kind, gen, seen)
		out := AccountPage{
			HasMore: p.HasMore && strings.TrimSpace(p.NextCursor) != "",
			Cursor:  p.NextCursor,
			Items:   make([]AccountItem, 0, len(p.Users)),
		}
		for _, u := range p.Users {
			out.Items = append(out.Items, b.accountUser(site.X, u.RestID, firstNonEmpty(u.Name, u.ScreenName, u.RestID), u.AvatarURL, u.Description, u.ScreenName))
		}
		return out, nil

	case AccountBookmarks:
		if folderID != "" && folderID != AccountFolderAll && !accountXBookmarkFoldersEnabled {
			return AccountPage{}, &site.Error{Site: site.X, Kind: site.ErrKindBadRequest, Message: "X 书签文件夹暂未开放"}
		}
		seen, gen := b.snapshotXAccountSeen(kind, reset)
		p, err := b.x.ListBookmarks(ctx, raw, seen)
		if err != nil {
			return AccountPage{}, err
		}
		b.mergeXAccountSeen(kind, gen, seen)
		return b.mapXAccountPostsPage(p), nil

	case AccountLikes:
		seen, gen := b.snapshotXAccountSeen(kind, reset)
		p, err := b.x.ListLikes(ctx, selfUID, raw, seen)
		if err != nil {
			return AccountPage{}, err
		}
		b.mergeXAccountSeen(kind, gen, seen)
		return b.mapXAccountPostsPage(p), nil

	case AccountLists:
		if !accountXListsEnabled {
			return AccountPage{}, &site.Error{Site: site.X, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
		}
		return AccountPage{}, &site.Error{Site: site.X, Kind: site.ErrKindUnavailable, Unsupported: true, Message: "X 列表浏览暂未开放"}

	default:
		return AccountPage{}, &site.Error{Site: site.X, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
	}
}

func (b *Backend) mapXAccountPostsPage(p *x.AccountPostsPage) AccountPage {
	if p == nil {
		return AccountPage{}
	}
	keys := make([]string, 0, len(p.Posts))
	for i := range p.Posts {
		if p.Posts[i].ID != "" {
			keys = append(keys, x.PostItemKey(p.Posts[i].ID))
		}
	}
	var states map[string]library.DotState
	if len(keys) > 0 {
		states = b.States(keys)
	}
	out := AccountPage{
		HasMore: p.HasMore && strings.TrimSpace(p.NextCursor) != "",
		Cursor:  p.NextCursor,
		Items:   make([]AccountItem, 0, len(p.Posts)),
	}
	for i := range p.Posts {
		post := &p.Posts[i]
		key := x.PostItemKey(post.ID)
		pv := mapXPostDTO(post, nil, states[key])
		openURL := firstNonEmpty(safeBrowserURL(post.CanonicalURL), "https://x.com/i/web/status/"+post.ID)
		it := AccountItem{
			Kind:       AccountItemPost,
			Site:       string(site.X),
			ID:         post.ID,
			Title:      xPostSummaryTitle(pv),
			Cover:      xPostThumbCoverURL(pv),
			Author:     xPostAuthorDisplayName(pv.Author),
			AuthorID:   post.Author.RestID,
			Duration:   xPostDuration(pv),
			OpenURL:    openURL,
			BrowserURL: safeBrowserURL(openURL),
			CacheKey:   pv.CacheKey,
			XPost:      pv,
		}
		out.Items = append(out.Items, it)
	}
	return out
}

func (b *Backend) accountUser(sid site.ID, uid, name, face, sign, uniqueID string) AccountItem {
	it := AccountItem{Kind: AccountItemUser, Site: string(sid), ID: uid, Title: name, Cover: face, Signature: firstLine(sign), UniqueID: uniqueID, AccountKey: formatAccountKey(sid, uid)}
	switch sid {
	case site.Douyin:
		it.OpenURL = "https://www.douyin.com/user/" + uid
	case site.Weibo:
		it.OpenURL = "https://weibo.com/u/" + uid
	case site.X:
		it.OpenURL = "https://x.com/i/user/" + uid
	case site.Bilibili:
		it.OpenURL = "https://space.bilibili.com/" + uid
	}
	it.BrowserURL = safeBrowserURL(it.OpenURL)
	if b.subs != nil {
		if owner, ok := b.subs.OwnerOf(string(sid), uid); ok {
			it.OwnerID, it.OwnerName = owner.ID, owner.Name
		}
	}
	return it
}

func biliVideoItem(it *AccountItem, bvid string) {
	it.Kind, it.ID = AccountItemVideo, bvid
	it.OpenURL = "https://www.bilibili.com/video/" + bvid
	it.BrowserURL = it.OpenURL
	it.CacheKey = library.CanonicalItemKey("bilibili", "video", bvid)
}

// accountGate checks the site, login and breaker before any request is sent.
func (b *Backend) accountGate(siteID, kind string) (site.ID, error) {
	sid := site.ID(strings.ToLower(strings.TrimSpace(siteID)))
	known := false
	for _, s := range b.AccountSections(string(sid)) {
		if s.Kind == kind && s.Available {
			known = true
		}
	}
	if err := b.requireSiteLogin(sid); err != nil {
		if !sid.Valid() {
			return "", &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
		}
		return "", err
	}
	if !known {
		return "", &site.Error{Site: sid, Kind: site.ErrKindBadRequest, Message: "不支持的功能"}
	}
	if b.subs != nil {
		if active, until := b.subs.IsSiteInBackoff(string(sid)); active {
			return "", &site.Error{Site: sid, Kind: site.ErrKindBackoff, Message: "站点暂停访问中（风控或限流后的退避），" + until.Local().Format("15:04") + " 后再试"}
		}
	}
	return sid, nil
}

// accountFail logs only the class of err and, on risk control or rate
// limiting, opens this site's breaker so every request of the site stops.
func (b *Backend) accountFail(sid site.ID, kind string, err error) error {
	k := site.KindOf(err)
	b.Logf("account.%s.%s error=%s", sid, kind, k)
	if (k == site.ErrKindRiskControl || k == site.ErrKindRateLimited) && b.subs != nil {
		if sid == site.X {
			b.recordXAutomaticStop(err)
		} else {
			b.subs.RecordSiteAutomaticStop(string(sid), k)
		}
	}
	return err
}

func splitAccountCursor(c string) (int, string) {
	c = strings.TrimSpace(c)
	if c == "" {
		return 1, ""
	}
	n, rest, ok := strings.Cut(c, "|")
	pn, err := strconv.Atoi(n)
	if !ok || err != nil || pn < 1 {
		return 1, ""
	}
	return pn, rest
}

func joinAccountCursor(pn int, raw string) string { return strconv.Itoa(pn) + "|" + raw }

func parseHistoryCursor(raw string) (bilibili.HistoryCursor, error) {
	if raw == "" {
		return bilibili.HistoryCursor{}, nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return bilibili.HistoryCursor{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "历史记录翻页位置无效"}
	}
	maxV, err1 := strconv.ParseInt(parts[0], 10, 64)
	viewAt, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return bilibili.HistoryCursor{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "历史记录翻页位置无效"}
	}
	return bilibili.HistoryCursor{Max: maxV, ViewAt: viewAt, Business: parts[2]}, nil
}

// accountBrowserHosts are the only hosts 我的 offers to open in the browser.
var accountBrowserHosts = []string{"bilibili.com", "b23.tv", "douyin.com", "weibo.com", "weibo.cn", "x.com", "twitter.com"}

// safeBrowserURL returns raw only when it is an https URL on a site host.
func safeBrowserURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Host == "" || (u.Port() != "" && u.Port() != "443") {
		return ""
	}
	if !site.MatchDomainSuffix(strings.ToLower(u.Hostname()), accountBrowserHosts) {
		return ""
	}
	return u.String()
}

func midString(mid int64) string {
	if mid <= 0 {
		return ""
	}
	return strconv.FormatInt(mid, 10)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
