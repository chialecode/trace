package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
	mygoui "github.com/egoist/mygo/ui"
)

func createDirLink(t *testing.T, linkPath, targetDir string) {
	t.Helper()
	if err := os.Symlink(targetDir, linkPath); err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", linkPath, targetDir)
		appdirs.SetSubprocessQuiet(cmd)
		if out, err := cmd.CombinedOutput(); err == nil {
			return
		} else {
			t.Fatalf("create directory junction %q -> %q failed: %v (%s)", linkPath, targetDir, err, string(out))
		}
	}
	t.Fatalf("os.Symlink(%q, %q) failed", targetDir, linkPath)
}

func TestValidateCLIOutDir(t *testing.T) {
	repoRoot := t.TempDir()
	artifactsDir := filepath.Join(repoRoot, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()

	// Valid subdirectories inside <repo>/artifacts/
	for _, rel := range []string{"artifacts/uishots", "artifacts/uishots/nested"} {
		got, err := validateCLIOutDir(repoRoot, rel)
		if err != nil {
			t.Fatalf("validateCLIOutDir(%q) unexpected error: %v", rel, err)
		}
		want := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if !sameCleanPath(got, want) {
			t.Fatalf("validateCLIOutDir(%q) = %q, want %q", rel, got, want)
		}
	}

	// Reject empty, repo root (.), parent (..), artifacts root itself, traversal, and outside paths.
	rejected := []string{
		"",
		"   ",
		".",
		"..",
		"artifacts",
		"artifacts/.",
		"artifacts/../outside",
		filepath.Join(repoRoot, "tools", "uishots"),
		outsideDir,
		filepath.Join(outsideDir, "uishots"),
	}
	for _, bad := range rejected {
		if got, err := validateCLIOutDir(repoRoot, bad); err == nil {
			t.Fatalf("validateCLIOutDir(%q) unexpectedly succeeded with %q", bad, got)
		}
	}

	// Reject regular file at target path.
	fileTarget := filepath.Join(artifactsDir, "not-a-dir")
	if err := os.WriteFile(fileTarget, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateCLIOutDir(repoRoot, "artifacts/not-a-dir"); err == nil {
		t.Fatal("validateCLIOutDir accepted regular file path")
	}

	// Reject symlink/junction at -out directly AND as an intermediate path component.
	linkPath := filepath.Join(artifactsDir, "link-uishots")
	createDirLink(t, linkPath, outsideDir)
	if _, err := validateCLIOutDir(repoRoot, "artifacts/link-uishots"); err == nil {
		t.Fatal("validateCLIOutDir accepted symlink/junction output directory")
	}
	if _, err := validateCLIOutDir(repoRoot, "artifacts/link-uishots/child"); err == nil {
		t.Fatal("validateCLIOutDir accepted path traversing symlink/junction")
	}
}

func TestPrepareCleanOutDirPreservesNonGeneratedAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	// Seed generated files (should be removed) and non-generated files/dirs (must be preserved).
	for name, content := range map[string]string{
		"old-home-1280x860.png": "old-png",
		"UPPER.PNG":             "upper-png",
		"assertions.json":       `{"old":true}`,
		"keep.txt":              "important notes",
		"custom.json":           `{"keep":true}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	subDir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedPNG := filepath.Join(subDir, "nested.png")
	if err := os.WriteFile(nestedPNG, []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := prepareCleanOutDir(dir); err != nil {
		t.Fatalf("prepareCleanOutDir failed: %v", err)
	}

	for _, deleted := range []string{"old-home-1280x860.png", "UPPER.PNG", "assertions.json"} {
		if _, err := os.Stat(filepath.Join(dir, deleted)); !os.IsNotExist(err) {
			t.Fatalf("expected generated file %s to be deleted, stat err=%v", deleted, err)
		}
	}
	for _, kept := range []string{"keep.txt", "custom.json"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Fatalf("expected non-generated file %s to be preserved: %v", kept, err)
		}
	}
	if _, err := os.Stat(nestedPNG); err != nil {
		t.Fatalf("expected nested file in subdirectory to be preserved: %v", err)
	}

	// Reject symlink/junction in both prepareCleanOutDir and runUIShots without deleting target contents.
	targetDir := t.TempDir()
	targetPNG := filepath.Join(targetDir, "victim.png")
	targetJSON := filepath.Join(targetDir, "assertions.json")
	if err := os.WriteFile(targetPNG, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetJSON, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(t.TempDir(), "out-link")
	createDirLink(t, linkPath, targetDir)

	if err := prepareCleanOutDir(linkPath); err == nil {
		t.Fatal("prepareCleanOutDir accepted symlink/junction")
	}
	if err := runUIShots(linkPath); err == nil {
		t.Fatal("runUIShots accepted symlink/junction")
	}
	if _, err := os.Stat(targetPNG); err != nil {
		t.Fatalf("target PNG behind rejected symlink was deleted: %v", err)
	}
	if _, err := os.Stat(targetJSON); err != nil {
		t.Fatalf("target assertions.json behind rejected symlink was deleted: %v", err)
	}
}

func TestEvaluateSceneRecordsForbiddenFound(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")
	tt := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Row(c).Size(1280, 860).Children(func() {
			mygoui.Column(c).Label("左栏导航").Size(64, 860)
			mygoui.Column(c).Label("中栏内容").Size(1216, 860).Children(func() {
				mygoui.Box(c).Label("必须存在的控件").Size(100, 32)
				mygoui.Box(c).Label("禁止出现的控件").Size(100, 32)
			})
		})
	}, 1280, 860)

	sc := sceneSpec{
		Name:            "forbidden-check",
		RequiredLabels:  []string{"必须存在的控件"},
		ForbiddenLabels: []string{"禁止出现的控件", "确实不存在的控件"},
	}
	a, _ := evaluateScene(tt, sc, 1280, 860, "forbidden-check-1280x860.png")
	if a.ForbiddenControlsAbsent {
		t.Fatal("expected ForbiddenControlsAbsent=false when forbidden control is rendered")
	}
	if a.Passed {
		t.Fatal("expected Passed=false when forbidden control is rendered")
	}
	if len(a.ForbiddenFound) != 1 || !strings.Contains(a.ForbiddenFound[0], "禁止出现的控件") {
		t.Fatalf("expected ForbiddenFound to record matched forbidden label, got %v", a.ForbiddenFound)
	}
}

func TestM7K19AssertionGuardsDetectFailures(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")

	// 1. RequiredTexts / ForbiddenTexts failure reporting.
	ttText := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Row(c).Size(1280, 860).Children(func() {
			mygoui.Column(c).Label("左栏导航").Size(52, 860)
			mygoui.Column(c).Label("中栏内容").Size(1228, 860).Children(func() {
				mygoui.Text(c, "直播中")
			})
		})
	}, 1280, 860)
	a, _ := evaluateScene(ttText, sceneSpec{
		Name:           "text-guard",
		RequiredTexts:  []string{"不存在的文案"},
		ForbiddenTexts: []string{"直播中"},
	}, 1280, 860, "text-guard.png")
	if a.Passed || a.RequiredControlsVisible || a.ForbiddenControlsAbsent {
		t.Fatalf("expected text-guard failure, got %+v", a)
	}
	if len(a.MissingTexts) != 1 || len(a.ForbiddenTextsFound) != 1 {
		t.Fatalf("expected missing/forbidden texts recorded, got missing=%v forbidden=%v", a.MissingTexts, a.ForbiddenTextsFound)
	}

	// 2. 5-segment filter overflow or overlap at 720px must fail verifyFiveSegmentSiteFilter.
	ttOverflow := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Box(c).Size(720, 40).Children(func() {
			for i, seg := range []string{"全部", "B 站", "抖音", "微博", "X"} {
				mygoui.Box(c).Label("主页站点筛选："+seg).Absolute().Left(float32(i)*160).Top(4).Size(160, 32)
			}
		})
	}, 720, 860)
	ttOverflow.Frame()
	if verifyFiveSegmentSiteFilter(ttOverflow, "主页站点筛选：", 720) {
		t.Fatal("expected verifyFiveSegmentSiteFilter to reject overflowing 5-segment bar at 720px")
	}

	ttOverlap := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Box(c).Size(720, 40).Children(func() {
			for i, seg := range []string{"全部", "B 站", "抖音", "微博", "X"} {
				mygoui.Box(c).Label("主页站点筛选："+seg).Absolute().Left(float32(i)*80).Top(4).Size(100, 32)
			}
		})
	}, 720, 860)
	ttOverlap.Frame()
	if verifyFiveSegmentSiteFilter(ttOverlap, "主页站点筛选：", 720) {
		t.Fatal("expected verifyFiveSegmentSiteFilter to reject overlapping 5-segment bar at 720px")
	}

	// 3. Login X risk disclosure placed BELOW submit button must fail verifyLoginXRiskDisclosure.
	ttBadRisk := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Column(c).Size(720, 860).Children(func() {
			mygoui.Box(c).Label("验证并登录X").Size(120, 36)
			mygoui.Column(c).Label("X登录风险提示").Size(500, 120).Children(func() {
				mygoui.Text(c, "使用前请注意 不是官方开放 API 服务条款 限流 临时锁号 封号 专门账号 只读不写 不保存账号密码 自动续期")
			})
		})
	}, 720, 860)
	ttBadRisk.Frame()
	if verifyLoginXRiskDisclosure(ttBadRisk, 720, 860) {
		t.Fatal("expected verifyLoginXRiskDisclosure to reject risk box positioned below submit button")
	}

	// 4. X subscription row showing live status or record button must fail verifyXAccountRowNoLiveAndNoRecord.
	ttBadXRow := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Column(c).Size(720, 860).Children(func() {
			mygoui.Text(c, "X").Label("订阅详情账号站点：x:1000000000000000006")
			mygoui.Text(c, "未开播")
		})
	}, 720, 860)
	ttBadXRow.Frame()
	if verifyXAccountRowNoLiveAndNoRecord(ttBadXRow, "1000000000000000006", true) {
		t.Fatal("expected verifyXAccountRowNoLiveAndNoRecord to reject live status text on X-only subscription row")
	}

	// 5. Squeezed subscription name in tag batch checklist row must fail verifyTagBatchSubNamesVisible.
	ttSqueezedSub := mygoui.NewTester(func(c *mygoui.Context) {
		mygoui.Row(c).Label("勾选规则订阅：技术频道_Alpha").Size(490, 44).Children(func() {
			mygoui.Text(c, "技术频道_Alpha").Label("规则订阅名称：技术频道_Alpha").Size(16, 20)
		})
	}, 720, 860)
	ttSqueezedSub.Frame()
	if verifyTagBatchSubNamesVisible(ttSqueezedSub, "勾选规则订阅：", "规则订阅名称：", []string{"技术频道_Alpha"}, 720) {
		t.Fatal("expected verifyTagBatchSubNamesVisible to reject squeezed subscription name width")
	}
}

func TestUIShotsScenesAndStructuredAssertions(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")
	outDir := t.TempDir()

	// Seed a non-generated file to verify runUIShots preserves it while cleaning stale PNGs.
	keepFile := filepath.Join(outDir, "README.txt")
	if err := os.WriteFile(keepFile, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	stalePNG := filepath.Join(outDir, "stale-removed-1280x860.png")
	if err := os.WriteFile(stalePNG, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runUIShots(outDir); err != nil {
		// If an assertion failed, inspect assertions.json to report exact scene/width.
		if raw, rErr := os.ReadFile(filepath.Join(outDir, "assertions.json")); rErr == nil {
			var rep UIShotsReport
			if json.Unmarshal(raw, &rep) == nil {
				for _, a := range rep.Assertions {
					if !a.Passed {
						t.Logf("FAILED scene=%s width=%d noOverflow=%v fixed=%v reqVisible=%v forbAbsent=%v missing=%v missingTexts=%v forbiddenFound=%v forbiddenTexts=%v",
							a.Scene, a.Width, a.NoHorizontalOver, a.FixedSidebarsOnScr, a.RequiredControlsVisible, a.ForbiddenControlsAbsent, a.MissingLabels, a.MissingTexts, a.ForbiddenFound, a.ForbiddenTextsFound)
					}
				}
				t.Logf("REPORT flags: passed=%v compose=%v filters=%v badges=%v subNoBadge=%v xBadges6=%v fiveSeg=%v xNoLive=%v loginXRisk=%v",
					rep.Passed, rep.ComposeGuardPassed, rep.SiteFiltersVerified, rep.SiteBadgesInSixPlaces, rep.SubscriptionGridNoSiteBadge,
					rep.XBadgesInSixPlaces, rep.FiveSegmentFilterNoOverflow, rep.XAccountRowNoLiveNoRecord, rep.LoginXRiskDisclosureShown)
			}
		}
		t.Fatalf("runUIShots failed: %v", err)
	}

	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("runUIShots deleted non-generated file %s: %v", keepFile, err)
	}
	if _, err := os.Stat(stalePNG); !os.IsNotExist(err) {
		t.Fatalf("runUIShots did not clean stale PNG %s (err=%v)", stalePNG, err)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "assertions.json"))
	if err != nil {
		t.Fatalf("read assertions.json: %v", err)
	}
	var report UIShotsReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal assertions.json: %v", err)
	}
	if !report.Passed || !report.ComposeGuardPassed || !report.SiteFiltersVerified ||
		!report.SiteBadgesInSixPlaces || !report.SubscriptionGridNoSiteBadge ||
		!report.XBadgesInSixPlaces || !report.FiveSegmentFilterNoOverflow ||
		!report.XAccountRowNoLiveNoRecord || !report.LoginXRiskDisclosureShown {
		t.Fatalf("unexpected report summary flags: passed=%v compose=%v filters=%v badges=%v subNoBadge=%v xBadges6=%v fiveSeg=%v xNoLive=%v loginXRisk=%v",
			report.Passed, report.ComposeGuardPassed, report.SiteFiltersVerified, report.SiteBadgesInSixPlaces, report.SubscriptionGridNoSiteBadge,
			report.XBadgesInSixPlaces, report.FiveSegmentFilterNoOverflow, report.XAccountRowNoLiveNoRecord, report.LoginXRiskDisclosureShown)
	}

	requiredScenes := []string{
		"home",
		"subscriptions",
		"subscriptions-sort-recent",
		"subscribe-picker",
		"move-account-picker",
		"subscriptions-all-accounts",
		"subscription-detail",
		"subscription-content-empty",
		"subscription-accounts",
		"subscription-accounts-x",
		"settings-rules",
		"settings-rules-batch",
		"settings-tags-multi-batch",
		"settings-account",
		"settings-account-expired",
		"settings-account-paste",
		"settings-download",
		"settings-subscription",
		"parse",
		"library-item",
		"cached-video",
		"user",
		"douyin-user",
		"douyin-video",
		"cached-douyin-video",
		"douyin-note",
		"weibo-user",
		"weibo-user-empty-shell",
		"weibo-user-muted",
		"weibo-post",
		"weibo-post-video",
		"weibo-post-cards",
		"weibo-post-retweet",
		"weibo-post-retweet-offline",
		"weibo-post-edit-history",
		"weibo-post-edit-history-offline",
		"home-weibo-retweet",
		"parse-weibo-live",
		"import-followings-weibo",
		"mine-weibo-following",
		"mine-weibo-favorites",
		"login-weibo",
		"login-weibo-qr-expired",
		"login-weibo-cookie",
		"x-user",
		"x-user-protected",
		"x-user-empty-shell",
		"x-post",
		"x-post-thread",
		"x-post-retweet-deleted",
		"x-post-quote",
		"x-post-video",
		"x-post-cards",
		"x-post-live-card",
		"x-post-offline",
		"home-x",
		"parse-x-spaces",
		"import-followings-x",
		"mine-x-following",
		"mine-x-bookmarks",
		"mine-x-likes",
		"login-x",
		"settings-remote-connected",
		"settings-remote-push-x-confirm",
		"subscriptions-checks",
		"library",
		"tasks",
		"tasks-queue",
		"tasks-live",
		"tasks-attention",
		"tasks-conflicts",
		"settings-update",
		"settings-update-dev",
		"settings-about",
		"login",
		"login-douyin",
		"login-douyin-precheck",
		"mine-folded",
		"mine-bili-favorites",
		"mine-bili-toview",
		"mine-bili-history",
		"mine-bili-following",
		"mine-douyin-likes",
		"mine-douyin-collection",
		"mine-douyin-watch-history",
		"mine-logged-out",
		"mine-risk-error",
		"mine-narrow-chooser",
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
	t.Logf("verified %d scenes (%d screenshots)", len(bySceneWidth), len(report.Assertions))
}
