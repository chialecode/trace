package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUIShotsScenesAndStructuredAssertions(t *testing.T) {
	_ = os.Setenv("TRACE_QUIET", "1")
	outDir := t.TempDir()

	if err := runUIShots(outDir); err != nil {
		// If an assertion failed, inspect assertions.json to report exact scene/width.
		if raw, rErr := os.ReadFile(filepath.Join(outDir, "assertions.json")); rErr == nil {
			var rep UIShotsReport
			if json.Unmarshal(raw, &rep) == nil {
				for _, a := range rep.Assertions {
					if !a.Passed {
						t.Logf("FAILED scene=%s width=%d noOverflow=%v fixed=%v reqVisible=%v forbAbsent=%v missing=%v",
							a.Scene, a.Width, a.NoHorizontalOver, a.FixedSidebarsOnScr, a.RequiredControlsVisible, a.ForbiddenControlsAbsent, a.MissingLabels)
					}
				}
			}
		}
		t.Fatalf("runUIShots failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "assertions.json"))
	if err != nil {
		t.Fatalf("read assertions.json: %v", err)
	}
	var report UIShotsReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal assertions.json: %v", err)
	}
	if !report.Passed || !report.ComposeGuardPassed || !report.SiteFiltersVerified || !report.SiteBadgesInSixPlaces || !report.SubscriptionGridNoSiteBadge {
		t.Fatalf("unexpected report summary flags: passed=%v compose=%v filters=%v badges=%v subNoBadge=%v",
			report.Passed, report.ComposeGuardPassed, report.SiteFiltersVerified, report.SiteBadgesInSixPlaces, report.SubscriptionGridNoSiteBadge)
	}

	requiredScenes := []string{
		"home",
		"subscriptions",
		"subscription-accounts",
		"settings-account",
		"settings-subscription",
		"parse",
		"user",
		"douyin-user",
		"douyin-video",
		"cached-douyin-video",
		"douyin-note",
		"library",
		"tasks",
		"login",
		"login-douyin",
	}
	widths := []int{1280, 1000, 720}
	bySceneWidth := make(map[string]map[int]SceneAssertion)
	for _, a := range report.Assertions {
		if !a.Passed {
			t.Fatalf("scene %s (%d) did not pass: %+v", a.Scene, a.Width, a)
		}
		info, err := os.Stat(filepath.Join(outDir, a.Screenshot))
		if err != nil || info.Size() == 0 {
			t.Fatalf("missing or empty screenshot %s: err=%v", a.Screenshot, err)
		}
		if bySceneWidth[a.Scene] == nil {
			bySceneWidth[a.Scene] = make(map[int]SceneAssertion)
		}
		bySceneWidth[a.Scene][a.Width] = a
	}

	for _, sc := range requiredScenes {
		for _, w := range widths {
			if _, ok := bySceneWidth[sc][w]; !ok {
				t.Fatalf("missing assertion for scene=%s width=%d", sc, w)
			}
		}
	}
}
