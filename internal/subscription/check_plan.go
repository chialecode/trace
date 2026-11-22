package subscription

import (
	"sort"
	"time"

	"github.com/chialecode/trace/internal/site"
)

// Check plans of an account (AccountCheckPlan.Plan).
const (
	PlanScheduled = "scheduled" // a scheduled round checks it at NextAt
	PlanManual    = "manual"    // its rule checks only by hand
	PlanOff       = "off"       // scheduled checking is off in Settings
	PlanUnauth    = "unauth"    // its site is not signed in
)

// WeiboLiveUnsupportedMessage explains that Weibo live checking and recording are unsupported.
const WeiboLiveUnsupportedMessage = "微博直播暂不支持录制"

// AccountCheckPlan tells when an account was last checked for updates and
// when a scheduled round should check it next (Tasks › 检查计划).
type AccountCheckPlan struct {
	SubID         string `json:"subId"`
	SubName       string `json:"subName"`
	Site          string `json:"site"`
	UID           string `json:"uid"`
	Name          string `json:"name"`
	AutoRecord    bool   `json:"autoRecord,omitempty"`
	LiveNote      string `json:"liveNote,omitempty"`
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	LastKind      string `json:"lastKind,omitempty"`
	Note          string `json:"note,omitempty"`
	Plan          string `json:"plan"`
	// NextAt is when the next scheduled round should check the account
	// (RFC 3339), set for PlanScheduled.
	NextAt string `json:"nextAt,omitempty"`
	// AfterBackoff marks a next check pushed past the site's backoff.
	AfterBackoff bool `json:"afterBackoff,omitempty"`
}

// CheckPlan lists every subscribed account with its last check and the
// scheduled round expected to check it next. A round checks at most
// SiteScheduledRoundCap accounts of a site, those checked longest ago first,
// so an account further down waits for a later round; a site in backoff
// skips the rounds before its backoff ends. A round deferred for want of
// quota is not foreseen: the plan then slips by a round.
func (m *Manager) CheckPlan() []AccountCheckPlan {
	m.mu.Lock()
	cfg := m.subSettings
	now := m.nowFunc()
	next := m.nextRunAt
	if !cfg.Enabled {
		next = time.Time{}
	}
	subs, err := m.listSubsOnlyLocked()
	accStates, _ := m.listAccountStatesLocked()
	type entry struct {
		plan AccountCheckPlan
		last string
	}
	var entries []entry
	if err == nil {
		for _, sub := range subs {
			ruleAutoRecord := sub.Rules.AutoRecord || sub.LiveAutoRecord
			for _, acc := range sub.Accounts {
				siteName := normalizeLegacyAccountSite(acc.Site)
				st := accStates[siteName+":"+acc.UID]
				autoRecord := ruleAutoRecord
				liveNote := ""
				if siteName == string(site.Weibo) {
					autoRecord = false
					if ruleAutoRecord {
						liveNote = WeiboLiveUnsupportedMessage
					}
				}
				p := AccountCheckPlan{
					SubID: sub.ID, SubName: sub.Name, Site: siteName, UID: acc.UID, Name: acc.Name,
					AutoRecord:    autoRecord,
					LiveNote:      liveNote,
					LastCheckedAt: st.LastCheckedAt, LastKind: st.LastKind, Note: st.Note,
					Plan: PlanScheduled,
				}
				if !sub.Rules.ScheduledCheck {
					p.Plan = PlanManual
				}
				entries = append(entries, entry{p, st.LastCheckedAt})
			}
		}
	}
	m.mu.Unlock()

	if !cfg.Enabled {
		next = time.Time{}
	} else if !next.After(now) {
		var ok bool
		if next, ok = NextRunTime(cfg, now); !ok {
			next = time.Time{}
		}
	}
	// runAt is the start of the k-th scheduled round from now.
	runs := []time.Time{next}
	runAt := func(k int) time.Time {
		for len(runs) <= k {
			last := runs[len(runs)-1]
			t, ok := NextRunTime(cfg, last)
			if !ok || !t.After(last) {
				return time.Time{}
			}
			runs = append(runs, t)
		}
		return runs[k]
	}

	bySite := make(map[string][]int)
	for i := range entries {
		if entries[i].plan.Plan == PlanScheduled {
			bySite[entries[i].plan.Site] = append(bySite[entries[i].plan.Site], i)
		}
	}
	for siteName, idx := range bySite {
		if !m.isSiteAuthenticated(site.ID(siteName)) {
			for _, i := range idx {
				entries[i].plan.Plan = PlanUnauth
			}
			continue
		}
		if next.IsZero() {
			for _, i := range idx {
				entries[i].plan.Plan = PlanOff
			}
			continue
		}
		// The round's order: subscription order, then checked longest ago.
		sort.SliceStable(idx, func(a, b int) bool { return entries[idx[a]].last < entries[idx[b]].last })
		first := 0
		inBackoff, until := m.IsSiteInBackoff(siteName)
		if inBackoff {
			const maxBackoffAdvance = 10000
			for steps := 0; steps < maxBackoffAdvance; steps++ {
				t := runAt(first)
				if t.IsZero() || !t.Before(until) {
					break
				}
				first++
			}
		}
		roundCap := SiteScheduledRoundCap(siteName)
		if roundCap <= 0 {
			for _, i := range idx {
				entries[i].plan.Plan = PlanOff
			}
			continue
		}
		for pos, i := range idx {
			if at := runAt(first + pos/roundCap); !at.IsZero() {
				entries[i].plan.NextAt = at.UTC().Format(time.RFC3339)
				entries[i].plan.AfterBackoff = inBackoff
			}
		}
	}

	out := make([]AccountCheckPlan, len(entries))
	for i, e := range entries {
		out[i] = e.plan
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.NextAt == "") != (b.NextAt == "") {
			return a.NextAt != ""
		}
		return a.NextAt < b.NextAt
	})
	return out
}
