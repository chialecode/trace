package subscription

import (
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

// Live work does not take the hourly quota (it keeps its own pace), so an
// exhausted quota neither stops live checks nor is drained by them; it still
// stops during backoff. Other automatic work is charged as before.
func TestLiveRequestsSkipHourlyQuota(t *testing.T) {
	mgr := regressionSubscriptions(t)
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	for i := 0; i < 500; i++ {
		if err := mgr.SiteAutomaticRequestAllowedFor("douyin", "live"); err != nil {
			t.Fatalf("live request %d refused: %v", i, err)
		}
	}
	if !mgr.ConsumeSiteQuotaTokens("douyin", DefaultDouyinHourlyQuota) {
		t.Fatal("live requests took the hourly quota")
	}
	if err := mgr.SiteAutomaticRequestAllowedFor("douyin", "subscription"); !site.IsKind(err, site.ErrKindQuota) {
		t.Fatalf("an update check request past the quota: %v", err)
	}
	if err := mgr.SiteAutomaticRequestAllowedFor("douyin", "live"); err != nil {
		t.Fatalf("a live request refused for the quota: %v", err)
	}
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindRiskControl)
	if err := mgr.SiteAutomaticRequestAllowedFor("douyin", "live"); !site.IsKind(err, site.ErrKindBackoff) {
		t.Fatalf("a live request in backoff: %v", err)
	}
}
