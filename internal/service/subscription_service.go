package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/subscription"
)

// AccountCheckRow is a row of Tasks › 检查计划: an account's update checks
// and, when its subscription records broadcasts, its live checks.
type AccountCheckRow struct {
	subscription.AccountCheckPlan
	// LiveCheckedAt is the last live status check (RFC 3339), "" if none
	// in this run; LiveEverySec how often one comes, about.
	LiveCheckedAt string `json:"liveCheckedAt,omitempty"`
	LiveEverySec  int    `json:"liveEverySec,omitempty"`
}

// ListSubscriptions returns all creator subscriptions.
func (b *Backend) ListSubscriptions() ([]subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.subs.List()
}

// UpsertSubscription creates or updates a creator subscription.
func (b *Backend) UpsertSubscription(sub subscription.Subscription) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Upsert(sub)
	if err == nil {
		// A user page subscribes with only Mid and Face; the stored accounts
		// then carry that face. Raw input accounts win when given.
		if len(sub.Accounts) > 0 {
			b.rememberAccountAvatars(sub.Accounts)
		} else {
			b.rememberAccountAvatars(res.Accounts)
		}
		b.events.Publish(EventSubscriptionChecked, "upserted")
	}
	return res, err
}

// CreateSubscription creates a new multi-account subscription.
func (b *Backend) CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Create(name, note, accounts, rules)
	if err == nil {
		// The input still has the raw avatar URL; a stored Douyin one is dropped.
		b.rememberAccountAvatars(accounts)
		b.events.Publish(EventSubscriptionChecked, "created")
	}
	return res, err
}

// LinkSubscriptionAccount links an account to an existing subscription.
func (b *Backend) LinkSubscriptionAccount(subID, siteID, uid, name, face string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Link(subID, siteID, uid, name, face)
	if err == nil {
		b.rememberAccountAvatar(siteID, uid, face)
		b.events.Publish(EventSubscriptionChecked, "linked")
	}
	return res, err
}

// LinkSubscriptionAccountRef links an AccountRef (preserving Douyin UniqueID and WebRID) to a subscription.
func (b *Backend) LinkSubscriptionAccountRef(subID string, acct subscription.AccountRef) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.LinkAccount(subID, acct)
	if err == nil {
		b.rememberAccountAvatar(acct.Site, acct.UID, acct.Face)
		b.events.Publish(EventSubscriptionChecked, "linked")
	}
	return res, err
}

// UnlinkSubscriptionAccount removes an account from a subscription.
func (b *Backend) UnlinkSubscriptionAccount(subID, siteID, uid string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Unlink(subID, siteID, uid)
	if err == nil {
		b.pruneAccountAvatars()
		b.events.Publish(EventSubscriptionChecked, "unlinked")
	}
	return res, err
}

// MoveSubscriptionAccount atomically moves an account from its current subscription to toSubID.
func (b *Backend) MoveSubscriptionAccount(siteID, uid, toSubID string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Move(siteID, uid, toSubID)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "moved")
	}
	return res, err
}

// MoveSubscriptionAccountToNew atomically moves an account from its current subscription
// into a newly created subscription under a single lock and single file write.
func (b *Backend) MoveSubscriptionAccountToNew(siteID, uid, name, note string, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.MoveToNew(siteID, uid, name, note, rules)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "moved")
	}
	return res, err
}

// SetSubscriptionRules updates per-subscription rules and manages AutoCache.Since.
func (b *Backend) SetSubscriptionRules(subID string, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.SetRules(subID, rules)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// ListCacheRules returns the shared cache rules, the built-in one first.
func (b *Backend) ListCacheRules() []subscription.CacheRule {
	return b.subs.ListRules()
}

// SaveCacheRule creates or edits a shared rule and applies it to every subscription that uses it.
func (b *Backend) SaveCacheRule(rule subscription.CacheRule) (subscription.CacheRule, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.CacheRule{}, err
	}
	res, err := b.subs.SaveRule(rule)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// DeleteCacheRule removes a shared rule; its subscriptions use the default.
func (b *Backend) DeleteCacheRule(id string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	err := b.subs.DeleteRule(id)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return err
}

// SetSubscriptionRule makes a subscription use a shared rule.
func (b *Backend) SetSubscriptionRule(subID, ruleID string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.SetSubscriptionRule(subID, ruleID)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// SetSubscriptionsRule assigns a shared rule to the selected subscriptions in one batch;
// unselected subscriptions that previously used a non-default rule fall back to DefaultRuleID.
func (b *Backend) SetSubscriptionsRule(ruleID string, subIDs []string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	err := b.subs.SetSubscriptionsRule(ruleID, subIDs)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return err
}

// PreviewAccount resolves a Bilibili, Douyin, or Weibo account input and checks whether it is already owned by a subscription.
func (b *Backend) PreviewAccount(ctx context.Context, input string) (subscription.AccountRef, *subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.AccountRef{}, nil, err
	}
	siteID, normalized, err := b.registry.DetectSite(input)
	if err != nil {
		return subscription.AccountRef{}, nil, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return subscription.AccountRef{}, nil, err
	}

	switch siteID {
	case site.Douyin:
		target, rErr := b.douyin.ResolveLink(ctx, normalized)
		if rErr != nil {
			return subscription.AccountRef{}, nil, rErr
		}
		var secUID string
		switch target.Kind {
		case "user":
			secUID = firstNonEmpty(target.SecUID, target.ID)
		case "live":
			lv, lErr := b.DouyinLive(ctx, firstNonEmpty(target.WebRID, target.ID))
			if lErr != nil {
				return subscription.AccountRef{}, nil, lErr
			}
			secUID = lv.OwnerSecUID
		default:
			return subscription.AccountRef{}, nil, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindBadRequest,
				Message: "请粘贴抖音用户主页链接、分享文本或 sec_uid",
			}
		}
		uv, uErr := b.DouyinUser(ctx, secUID)
		if uErr != nil {
			return subscription.AccountRef{}, nil, uErr
		}
		acc := subscription.AccountRef{
			Site:     string(site.Douyin),
			UID:      uv.SecUID,
			UniqueID: uv.UniqueID,
			WebRID:   uv.WebRID,
			Name:     uv.Nickname,
			Face:     uv.AvatarURL,
			AddedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		return b.subs.PreviewResolvedAccount(acc)

	case site.Weibo:
		target, rErr := b.weibo.ResolveLink(ctx, normalized)
		if rErr != nil {
			return subscription.AccountRef{}, nil, rErr
		}
		if target.Kind != "user" {
			return subscription.AccountRef{}, nil, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindBadRequest,
				Message: "请粘贴微博用户主页链接或分享文本",
			}
		}
		uv, uErr := b.WeiboUser(ctx, firstNonEmpty(target.AuthorUID, target.ID))
		if uErr != nil {
			return subscription.AccountRef{}, nil, uErr
		}
		acc := subscription.AccountRef{
			Site:    string(site.Weibo),
			UID:     uv.UID,
			Name:    uv.ScreenName,
			Face:    uv.AvatarURL,
			AddedAt: time.Now().UTC().Format(time.RFC3339),
		}
		return b.subs.PreviewResolvedAccount(acc)

	case site.X:
		target, rErr := b.x.ResolveLink(ctx, normalized)
		if rErr != nil {
			return subscription.AccountRef{}, nil, rErr
		}
		if target.Kind != "user" {
			return subscription.AccountRef{}, nil, &site.Error{
				Site:    site.X,
				Kind:    site.ErrKindBadRequest,
				Message: "请粘贴 X 用户主页链接或分享文本",
			}
		}
		uv, uErr := b.XUser(ctx, firstNonEmpty(target.AuthorUID, target.ID))
		if uErr != nil {
			return subscription.AccountRef{}, nil, uErr
		}
		acc := subscription.AccountRef{
			Site:     string(site.X),
			UID:      uv.RestID,
			UniqueID: uv.ScreenName,
			Name:     firstNonEmpty(uv.Name, uv.ScreenName, uv.RestID),
			Face:     uv.AvatarURL,
			AddedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		return b.subs.PreviewResolvedAccount(acc)

	case site.Bilibili:
		target, err := b.bili.ResolveLink(ctx, normalized)
		if err != nil {
			return subscription.AccountRef{}, nil, err
		}
		if target.Kind != "user" {
			return subscription.AccountRef{}, nil, fmt.Errorf("请粘贴用户主页链接或 UID")
		}
		card, err := b.bili.UserCard(ctx, target.Mid)
		if err != nil {
			return subscription.AccountRef{}, nil, err
		}
		uidStr := fmt.Sprintf("%d", card.Mid)
		acc := subscription.AccountRef{
			Site:    "bilibili",
			UID:     uidStr,
			Name:    card.Name,
			Face:    card.Face,
			AddedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if owner, owned := b.subs.OwnerOf("bilibili", uidStr); owned {
			return acc, &owner, nil
		}
		return acc, nil, nil

	default:
		return subscription.AccountRef{}, nil, &site.Error{
			Site:    siteID,
			Kind:    site.ErrKindBadRequest,
			Message: "不支持的站点标识: " + string(siteID),
		}
	}
}

// OwnerOfAccount returns the subscription owning (site, uid), if any.
func (b *Backend) OwnerOfAccount(siteID, uid string) (subscription.Subscription, bool) {
	if b.requireAnyLogin() != nil {
		return subscription.Subscription{}, false
	}
	return b.subs.OwnerOf(siteID, uid)
}

// ListSubscriptionUpdates returns discovered update items for a subscription.
func (b *Backend) ListSubscriptionUpdates(subID string) ([]subscription.UpdateItem, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.subs.ListUpdates(subID)
}

// ListSubscriptionUpdatesPage returns a paginated slice of discovered update items for a subscription.
func (b *Backend) ListSubscriptionUpdatesPage(subID string, limit, offset int) ([]subscription.UpdateItem, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.subs.ListUpdatesPage(subID, limit, offset)
}

// DeleteSubscription removes a creator subscription.
func (b *Backend) DeleteSubscription(subIDOrMid string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	err := b.subs.Delete(subIDOrMid)
	if err == nil {
		b.pruneAccountAvatars()
		b.events.Publish(EventSubscriptionChecked, "deleted")
	}
	return err
}

// CheckSubscriptionsNow triggers an immediate incremental check for targetMid (or 0 for all enabled).
func (b *Backend) CheckSubscriptionsNow(ctx context.Context, targetMid int64) (subscription.CheckSummary, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.CheckSummary{}, err
	}
	if targetMid > 0 {
		if err := b.requireSiteLogin(site.Bilibili); err != nil {
			return subscription.CheckSummary{}, err
		}
	}
	return b.subs.CheckNow(ctx, targetMid)
}

// CheckSubscription triggers an immediate check for all accounts of subID.
func (b *Backend) CheckSubscription(ctx context.Context, subID string) (subscription.CheckSummary, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.CheckSummary{}, err
	}
	return b.subs.CheckSubscription(ctx, subID)
}

// CheckAccount checks one account of a subscription for updates at once.
func (b *Backend) CheckAccount(ctx context.Context, subID, siteName, uid string) (subscription.CheckSummary, error) {
	id, err := normalizeSiteID(siteName)
	if err != nil {
		return subscription.CheckSummary{}, err
	}
	if err := b.requireSiteLogin(id); err != nil {
		return subscription.CheckSummary{}, err
	}
	return b.subs.CheckAccount(ctx, subID, string(id), uid)
}

// CancelSubscriptionCheck cancels an in-progress subscription update check.
func (b *Backend) CancelSubscriptionCheck() { b.subs.CancelCheck() }

func formatBackoffLevelText(level int) string {
	switch {
	case level <= 1:
		return "15 分钟"
	case level == 2:
		return "30 分钟"
	case level == 3:
		return "1 小时"
	case level == 4:
		return "2 小时"
	default:
		return "6 小时"
	}
}

// SchedulerStatuses returns per-site scheduled check timing and backoff state for Settings -> 订阅与直播.
func (b *Backend) SchedulerStatuses() map[string]SiteSchedulerStatus {
	allSites := site.All()
	out := make(map[string]SiteSchedulerStatus, len(allSites))
	var states map[string]subscription.SchedulerState
	if b.subs != nil {
		states = b.subs.ListSiteSchedulerStates()
	}
	for _, id := range allSites {
		key := string(id)
		var label string
		switch id {
		case site.Bilibili:
			label = "哔哩哔哩"
		case site.Douyin:
			label = "抖音"
		case site.Weibo:
			label = "微博"
		case site.X:
			label = "X"
		default:
			label = id.DisplayName()
		}
		st := SiteSchedulerStatus{
			Site:        key,
			DisplayName: label,
		}
		if b.subs != nil {
			raw := states[key]
			st.LastError = raw.LastStopReason
			if inBackoff, until := b.subs.IsSiteInBackoff(key); inBackoff {
				lvlText := formatBackoffLevelText(raw.BackoffLevel)
				st.BackingOff = true
				st.CurrentInterval = lvlText
				st.BackoffUntil = until.UTC().Format("2006-01-02T15:04:05Z07:00")
				if id == site.X && raw.LastStopReason == site.ErrKindRateLimited {
					st.BackoffSummary = fmt.Sprintf("%s：触发限流暂停，%s 恢复", label, until.Local().Format("1 月 2 日 15:04"))
				} else {
					st.BackoffSummary = fmt.Sprintf("%s：因风控放慢到 %s，%s 恢复", label, lvlText, until.Local().Format("1 月 2 日 15:04"))
				}
			}
		}
		out[key] = st
	}
	return out
}

// CheckPlan lists every subscribed account with its last update check and
// the scheduled round expected to check it next, plus the live checks of
// accounts whose subscriptions record broadcasts.
func (b *Backend) CheckPlan() []AccountCheckRow {
	if b.requireAnyLogin() != nil {
		return nil
	}
	return b.checkPlanLocal()
}

// checkPlanLocal builds the check plan strictly from local subscription and
// live state without requiring an active site login (used by remote status).
func (b *Backend) checkPlanLocal() []AccountCheckRow {
	if b.subs == nil {
		return nil
	}
	plans := b.subs.CheckPlan()
	var liveAt map[string]time.Time
	if b.live != nil {
		liveAt = b.live.LiveCheckTimes()
	}
	autoRecord := map[string]int{}
	for _, p := range plans {
		if p.AutoRecord {
			autoRecord[p.Site]++
		}
	}
	out := make([]AccountCheckRow, 0, len(plans))
	for _, p := range plans {
		row := AccountCheckRow{AccountCheckPlan: p}
		if p.AutoRecord && b.live != nil {
			if at, ok := liveAt[p.Site+":"+p.UID]; ok {
				row.LiveCheckedAt = at.UTC().Format(time.RFC3339)
			}
			row.LiveEverySec = int(b.live.LiveCheckEvery(site.ID(p.Site), autoRecord[p.Site]).Seconds())
		}
		out = append(out, row)
	}
	return out
}

// RelationTags returns the logged-in user's Bilibili following groups.
func (b *Backend) RelationTags(ctx context.Context) ([]bilibili.RelationTag, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	return b.bili.RelationTags(ctx)
}

// RelationFollowings returns a page of Bilibili followings (either for a specific tagID > 0 or all followings when tagID == 0).
func (b *Backend) RelationFollowings(ctx context.Context, tagID int64, pn, ps int) ([]bilibili.FollowingUser, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	if tagID != 0 {
		mid := b.cred.Status().Mid
		return b.bili.RelationTagUsers(ctx, mid, tagID, pn, ps)
	}
	page, err := b.bili.RelationFollowings(ctx, 0, pn, ps)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ImportCreators imports selected Bilibili creators into subscriptions idempotently.
func (b *Backend) ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return 0, err
	}
	n, err := b.subs.ImportCreators(users, scope, autoCache, liveNotify, liveAutoRecord)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "imported")
	}
	return n, err
}

// FollowingGroups returns the selectable following groups for siteID in the subscription import dialog.
// For Weibo, groups are returned ONLY when weibo.FollowGroupsEnabled (accountWeiboFollowGroupsEnabled) is true.
func (b *Backend) FollowingGroups(ctx context.Context, siteID string) ([]FollowingGroupDTO, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return nil, err
	}
	switch id {
	case site.Bilibili:
		if err := b.requireSiteLogin(site.Bilibili); err != nil {
			return nil, err
		}
		tags, err := b.bili.RelationTags(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]FollowingGroupDTO, 0, len(tags))
		for _, t := range tags {
			out = append(out, FollowingGroupDTO{
				ID:    strconv.FormatInt(t.TagID, 10),
				Name:  t.Name,
				Count: t.Count,
			})
		}
		return out, nil

	case site.Weibo:
		if err := b.requireSiteLogin(site.Weibo); err != nil {
			return nil, err
		}
		if !accountWeiboFollowGroupsEnabled {
			return nil, nil
		}
		groups, err := b.weibo.ListFollowGroups(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]FollowingGroupDTO, 0, len(groups))
		for _, g := range groups {
			out = append(out, FollowingGroupDTO{
				ID:    g.ID,
				Name:  g.Title,
				Count: g.Count,
			})
		}
		return out, nil

	case site.X:
		if err := b.requireSiteLogin(site.X); err != nil {
			return nil, err
		}
		return nil, nil

	case site.Douyin:
		return nil, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     "抖音暂不支持从关注列表导入订阅",
		}

	default:
		return nil, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
}

func (b *Backend) snapshotWeiboFollowingSeen(groupKey string, reset bool) (map[string]bool, uint64) {
	return b.snapshotWeiboSeenMap(&b.weiboFollowingSeen, groupKey, reset)
}

func (b *Backend) mergeWeiboFollowingSeen(groupKey string, gen uint64, seen map[string]bool) {
	b.mergeWeiboSeenMap(&b.weiboFollowingSeen, groupKey, gen, seen)
}

// FollowingCreatorsPage returns one page of followed creators for siteID, marking
// already-subscribed accounts so the import dialog can render them unselectable.
func (b *Backend) FollowingCreatorsPage(ctx context.Context, siteID, groupID string, page int) (FollowingCreatorsPageDTO, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return FollowingCreatorsPageDTO{}, err
	}
	if page <= 0 {
		page = 1
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == AccountFolderAll {
		groupID = ""
	}

	switch id {
	case site.Bilibili:
		if err := b.requireSiteLogin(site.Bilibili); err != nil {
			return FollowingCreatorsPageDTO{}, err
		}
		const ps = 50
		var users []bilibili.FollowingUser
		var total int
		if groupID != "" {
			tagID, pErr := strconv.ParseInt(groupID, 10, 64)
			if pErr != nil {
				return FollowingCreatorsPageDTO{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的关注分组"}
			}
			users, err = b.bili.RelationTagUsers(ctx, b.cred.Status().Mid, tagID, page, ps)
			if err != nil {
				return FollowingCreatorsPageDTO{}, err
			}
		} else {
			res, rErr := b.bili.RelationFollowings(ctx, 0, page, ps)
			if rErr != nil {
				return FollowingCreatorsPageDTO{}, rErr
			}
			users, total = res.Items, res.Total
		}
		items := make([]FollowingCreatorDTO, 0, len(users))
		for _, u := range users {
			uid := strconv.FormatInt(u.Mid, 10)
			dto := FollowingCreatorDTO{
				Site: string(site.Bilibili),
				UID:  uid,
				Name: u.Uname,
				Face: u.Face,
				Sign: u.Sign,
			}
			if b.subs != nil {
				if owner, owned := b.subs.OwnerOf(string(site.Bilibili), uid); owned {
					dto.Subscribed = true
					dto.OwnerSubID = owner.ID
					dto.OwnerSubName = owner.Name
				}
			}
			items = append(items, dto)
		}
		hasMore := len(users) == ps
		if total > 0 {
			hasMore = len(users) > 0 && (page-1)*ps+len(users) < total
		}
		nextPage := 0
		if hasMore {
			nextPage = page + 1
		}
		return FollowingCreatorsPageDTO{
			Site:          string(site.Bilibili),
			Items:         items,
			Page:          page,
			NextPage:      nextPage,
			Total:         total,
			HasMore:       hasMore,
			GroupsEnabled: true,
		}, nil

	case site.Weibo:
		if err := b.requireSiteLogin(site.Weibo); err != nil {
			return FollowingCreatorsPageDTO{}, err
		}
		selfUID := ""
		if b.weiboCred != nil {
			selfUID = strings.TrimSpace(b.weiboCred.Status().UID)
		}
		if selfUID == "" {
			return FollowingCreatorsPageDTO{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindAuthRequired, Message: "缺少登录账号的微博 UID，请重新登录微博"}
		}
		seen, gen := b.snapshotWeiboFollowingSeen(groupID, page <= 1)
		var res *weibo.FollowingPage
		if groupID != "" {
			if !accountWeiboFollowGroupsEnabled {
				return FollowingCreatorsPageDTO{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindBadRequest, Message: "微博关注分组筛选暂未开放"}
			}
			res, err = b.weibo.ListFollowGroupMembers(ctx, selfUID, groupID, page, seen)
		} else {
			res, err = b.weibo.ListFollowing(ctx, selfUID, page, seen)
		}
		if err != nil {
			return FollowingCreatorsPageDTO{}, err
		}
		b.mergeWeiboFollowingSeen(groupID, gen, seen)
		items := make([]FollowingCreatorDTO, 0, len(res.Users))
		for _, u := range res.Users {
			dto := FollowingCreatorDTO{
				Site: string(site.Weibo),
				UID:  u.UID,
				Name: u.ScreenName,
				Face: u.AvatarURL,
				Sign: firstLine(u.Description),
			}
			if b.subs != nil {
				if owner, owned := b.subs.OwnerOf(string(site.Weibo), u.UID); owned {
					dto.Subscribed = true
					dto.OwnerSubID = owner.ID
					dto.OwnerSubName = owner.Name
				}
			}
			items = append(items, dto)
		}
		return FollowingCreatorsPageDTO{
			Site:          string(site.Weibo),
			Items:         items,
			Page:          res.Page,
			NextPage:      res.NextPage,
			NextCursor:    res.NextCursor,
			Total:         res.Total,
			HasMore:       res.HasMore,
			GroupsEnabled: accountWeiboFollowGroupsEnabled,
		}, nil

	case site.X:
		if err := b.requireSiteLogin(site.X); err != nil {
			return FollowingCreatorsPageDTO{}, err
		}
		cursor := ""
		if page > 1 {
			cursor = b.getXFollowingCursor(page)
			if cursor == "" {
				return FollowingCreatorsPageDTO{
					Site:          string(site.X),
					Page:          page,
					HasMore:       false,
					GroupsEnabled: false,
				}, nil
			}
		}
		return b.xFollowingCreatorsPage(ctx, cursor, page, page <= 1)

	case site.Douyin:
		return FollowingCreatorsPageDTO{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     "抖音暂不支持从关注列表导入订阅",
		}

	default:
		return FollowingCreatorsPageDTO{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点标识: " + strings.TrimSpace(siteID)}
	}
}

// FollowingCreatorsPageWithCursor returns one page of followed creators using an opaque string cursor
// (used directly by X GraphQL Following pagination; Bilibili/Weibo parse an optional decimal page number).
func (b *Backend) FollowingCreatorsPageWithCursor(ctx context.Context, siteID, groupID, cursor string) (FollowingCreatorsPageDTO, error) {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return FollowingCreatorsPageDTO{}, err
	}
	cleanCursor := strings.TrimSpace(cursor)
	if id == site.X {
		if err := b.requireSiteLogin(site.X); err != nil {
			return FollowingCreatorsPageDTO{}, err
		}
		pageNum := 1
		if cleanCursor != "" {
			pageNum = 0
		}
		return b.xFollowingCreatorsPage(ctx, cleanCursor, pageNum, cleanCursor == "")
	}
	page := 1
	if cleanCursor != "" {
		if p, pErr := strconv.Atoi(cleanCursor); pErr == nil && p > 0 {
			page = p
		}
	}
	return b.FollowingCreatorsPage(ctx, siteID, groupID, page)
}

func (b *Backend) xFollowingCreatorsPage(ctx context.Context, cursor string, page int, reset bool) (FollowingCreatorsPageDTO, error) {
	selfUID := ""
	if b.xCred != nil {
		selfUID = strings.TrimSpace(b.xCred.Status().UID)
	}
	if selfUID == "" {
		return FollowingCreatorsPageDTO{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindAuthRequired,
			Message: "缺少登录账号的 X rest_id，请重新登录 X",
		}
	}
	seen, gen := b.snapshotXSeenMap(&b.xFollowingSeen, "all", reset)
	res, err := b.x.ListFollowing(ctx, selfUID, cursor, seen)
	if err != nil {
		return FollowingCreatorsPageDTO{}, err
	}
	b.mergeXSeenMap(&b.xFollowingSeen, "all", gen, seen)
	items := make([]FollowingCreatorDTO, 0, len(res.Users))
	for _, u := range res.Users {
		dto := b.mapXFollowingCreatorDTO(u)
		items = append(items, dto)
	}
	nextPage := 0
	if res.HasMore && res.NextCursor != "" && page > 0 {
		nextPage = page + 1
		b.setXFollowingCursor(nextPage, res.NextCursor, gen, reset)
	} else if reset {
		b.setXFollowingCursor(0, "", gen, true)
	}
	return FollowingCreatorsPageDTO{
		Site:          string(site.X),
		Items:         items,
		Page:          page,
		NextPage:      nextPage,
		NextCursor:    res.NextCursor,
		HasMore:       res.HasMore && strings.TrimSpace(res.NextCursor) != "",
		GroupsEnabled: false,
	}, nil
}

func (b *Backend) mapXFollowingCreatorDTO(u x.FollowingUser) FollowingCreatorDTO {
	dto := FollowingCreatorDTO{
		Site:     string(site.X),
		UID:      u.RestID,
		UniqueID: u.ScreenName,
		Name:     firstNonEmpty(u.Name, u.ScreenName, u.RestID),
		Face:     u.AvatarURL,
		Sign:     firstLine(u.Description),
	}
	if b.subs != nil {
		if owner, owned := b.subs.OwnerOf(string(site.X), u.RestID); owned {
			dto.Subscribed = true
			dto.OwnerSubID = owner.ID
			dto.OwnerSubName = owner.Name
		}
	}
	return dto
}

// ImportFollowingCreators atomically imports selected creators for siteID into subscriptions
// using subscription.Manager.ImportAccountRefs with zero extra site API calls.
func (b *Backend) ImportFollowingCreators(siteID string, creators []FollowingCreatorDTO, rules subscription.SubRules) (subscription.ImportAccountRefsResult, error) {
	id, err := normalizeExplicitSiteID(siteID)
	if err != nil {
		return subscription.ImportAccountRefsResult{}, err
	}
	if err := b.requireSiteLogin(id); err != nil {
		return subscription.ImportAccountRefsResult{}, err
	}
	if len(creators) == 0 {
		return subscription.ImportAccountRefsResult{}, nil
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	refs := make([]subscription.AccountRef, 0, len(creators))
	for _, c := range creators {
		uid := strings.TrimSpace(c.UID)
		if uid == "" {
			continue
		}
		refs = append(refs, subscription.AccountRef{
			Site:     string(id),
			UID:      uid,
			UniqueID: strings.TrimSpace(c.UniqueID),
			Name:     strings.TrimSpace(c.Name),
			Face:     strings.TrimSpace(c.Face),
			AddedAt:  nowStr,
		})
	}
	res, err := b.subs.ImportAccountRefs(refs, rules)
	if err == nil && res.Created > 0 {
		b.events.Publish(EventSubscriptionChecked, "imported")
	}
	return res, err
}

// subscribedAccountName is the name an account has in the subscriptions
// (empty when it is not subscribed or has none).
func (b *Backend) subscribedAccountName(siteID site.ID, uid string) string {
	if b.subs == nil {
		return ""
	}
	subs, err := b.subs.List()
	if err != nil {
		return ""
	}
	for _, sub := range subs {
		for _, acc := range sub.Accounts {
			if strings.EqualFold(acc.Site, string(siteID)) && acc.UID == uid && strings.TrimSpace(acc.Name) != "" {
				return strings.TrimSpace(acc.Name)
			}
		}
	}
	return ""
}
