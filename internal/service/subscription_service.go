package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
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

// PreviewAccount resolves a Bilibili or Douyin account input and checks whether it is already owned by a subscription.
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

	if siteID == site.Douyin {
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
	}

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
	out := make(map[string]SiteSchedulerStatus, 2)
	var states map[string]subscription.SchedulerState
	if b.subs != nil {
		states = b.subs.ListSiteSchedulerStates()
	}
	for _, id := range site.All() {
		key := string(id)
		label := "哔哩哔哩"
		if id == site.Douyin {
			label = "抖音"
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
				st.BackoffSummary = fmt.Sprintf("%s：因风控放慢到 %s，%s 恢复", label, lvlText, until.Local().Format("1 月 2 日 15:04"))
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
