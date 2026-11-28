package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"path/filepath"
	"time"

	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/ui"
	mygoui "github.com/egoist/mygo/ui"
)

type SceneAssertion struct {
	Scene                   string       `json:"scene"`
	Width                   int          `json:"width"`
	Height                  int          `json:"height"`
	Screenshot              string       `json:"screenshot"`
	LeftRect                mygoui.Rect  `json:"leftRect"`
	CenterRect              mygoui.Rect  `json:"centerRect"`
	PanelRect               *mygoui.Rect `json:"panelRect,omitempty"`
	NoHorizontalOver        bool         `json:"noHorizontalOverflow"`
	FixedSidebarsOnScr      bool         `json:"fixedSidebarsOnScroll"`
	RequiredControlsVisible bool         `json:"requiredControlsVisible"`
	ForbiddenControlsAbsent bool         `json:"forbiddenControlsAbsent"`
	RequiredChecked         []string     `json:"requiredChecked,omitempty"`
	MissingLabels           []string     `json:"missingLabels,omitempty"`
	MissingTexts            []string     `json:"missingTexts,omitempty"`
	ForbiddenChecked        []string     `json:"forbiddenChecked,omitempty"`
	ForbiddenFound          []string     `json:"forbiddenFound,omitempty"`
	ForbiddenTextsFound     []string     `json:"forbiddenTextsFound,omitempty"`
	Passed                  bool         `json:"passed"`
}

type UIShotsReport struct {
	GeneratedAt                 string           `json:"generatedAt"`
	HeadlessTester              bool             `json:"headlessTester"`
	AudioMutedNullSink          bool             `json:"audioMutedNullSink"`
	VirtualFeedCount            int              `json:"virtualFeedCount"`
	ComposeGuardPassed          bool             `json:"composeGuardPassed"`
	SiteFiltersVerified         bool             `json:"siteFiltersVerified"`
	SiteBadgesInSixPlaces       bool             `json:"siteBadgesInSixPlaces"`
	SubscriptionGridNoSiteBadge bool             `json:"subscriptionGridNoSiteBadge"`
	XBadgesInSixPlaces          bool             `json:"xBadgesInSixPlaces"`
	FiveSegmentFilterNoOverflow bool             `json:"fiveSegmentFilterNoOverflow"`
	XAccountRowNoLiveNoRecord   bool             `json:"xAccountRowNoLiveNoRecord"`
	LoginXRiskDisclosureShown   bool             `json:"loginXRiskDisclosureShown"`
	Assertions                  []SceneAssertion `json:"assertions"`
	Passed                      bool             `json:"passed"`
}

// verifyFiveSegmentSiteFilter ensures all 5 site filter segments
// ("全部 / B 站 / 抖音 / 微博 / X") fit cleanly on one row with zero horizontal
// overflow, clipping, or overlap (including at 720px narrow width).
func verifyFiveSegmentSiteFilter(tt *mygoui.Tester, prefix string, width int) bool {
	segments := []string{"全部", "B 站", "抖音", "微博", "X"}
	prevRight := float32(-1)
	var firstY, firstW float32
	for i, seg := range segments {
		r, ok := tt.Find(prefix + seg)
		if !ok || r.W <= 12 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(width)+0.5 {
			return false
		}
		if i == 0 {
			firstY = r.Y
			firstW = r.W
		} else {
			dy := r.Y - firstY
			if dy < -1.0 || dy > 1.0 {
				return false
			}
			if r.X < prevRight-0.5 {
				return false
			}
			// All 5 segments use equal Grow(1).Basis(0) sizing; a sharply
			// narrower trailing segment indicates right-edge viewport clipping.
			if r.W < firstW*0.75 {
				return false
			}
		}
		prevRight = r.X + r.W
	}
	return true
}

// verifyLoginXRiskDisclosure confirms the X login segment displays the neutral
// factual risk disclosure visibly and strictly above the submit button.
func verifyLoginXRiskDisclosure(tt *mygoui.Tester, width, height int) bool {
	riskRect, okR := tt.Find("X登录风险提示")
	submitRect, okS := tt.Find("验证并登录X")
	if !okR || !okS {
		return false
	}
	if riskRect.W <= 0 || riskRect.H <= 0 || riskRect.X < -0.5 || riskRect.Y < -0.5 ||
		riskRect.X+riskRect.W > float32(width)+0.5 || riskRect.Y+riskRect.H > float32(height)+0.5 {
		return false
	}
	if submitRect.W <= 0 || submitRect.H <= 0 || submitRect.Y+submitRect.H > float32(height)+0.5 {
		return false
	}
	if riskRect.Y+riskRect.H > submitRect.Y+1.0 {
		return false
	}
	requiredPhrases := []string{
		"使用前请注意",
		"不是官方开放 API",
		"服务条款",
		"限流",
		"临时锁号",
		"封号",
		"专门账号",
		"只读不写",
		"不保存账号密码",
		"自动续期",
	}
	for _, phrase := range requiredPhrases {
		if !tt.HasText(phrase) {
			return false
		}
	}
	return true
}

// verifyXAccountRowNoLiveAndNoRecord checks that an X subscription account row
// displays the X site badge, has no record button, and (for X-only views) shows
// no live broadcast status text.
func verifyXAccountRowNoLiveAndNoRecord(tt *mygoui.Tester, xUID string, xOnly bool) bool {
	if _, ok := tt.Find("订阅详情账号站点：x:" + xUID); !ok {
		return false
	}
	if _, ok := tt.Find("开始录制 x:" + xUID); ok {
		return false
	}
	if _, ok := tt.Find("停止录制 x:" + xUID); ok {
		return false
	}
	if xOnly {
		for _, forbidden := range []string{"未开播", "直播中", "录制中", "未开通直播间"} {
			if tt.HasText(forbidden) {
				return false
			}
		}
	}
	return true
}

// verifyComposeGuard ensures IME composition does not prematurely submit parse
// input when Enter is pressed.
func verifyComposeGuard(app *ui.App) bool {
	app.Navigate(ui.RouteParse)
	composeTester := mygoui.NewTester(app.View, 1280, 860)
	_ = composeTester.Click("链接解析输入框")
	composeTester.Compose("BV1xx411c7mD", 12)
	composeTester.Key(0, mygoui.KeyEnter)
	return app.CurrentRoute == ui.RouteParse
}

func evaluateScene(tt *mygoui.Tester, sc sceneSpec, width, height int, fileName string) (SceneAssertion, image.Image) {
	return evaluateSceneWithCheck(tt, sc, width, height, fileName, nil)
}

func evaluateSceneWithCheck(tt *mygoui.Tester, sc sceneSpec, width, height int, fileName string, preScrollCheck func(tt *mygoui.Tester, a *SceneAssertion)) (SceneAssertion, image.Image) {
	if sc.TitleBar.Height > 0 {
		tt.SetTitleBar(sc.TitleBar)
	}
	tt.Frame()
	for i := 0; i < sc.Frames; i++ {
		tt.Frame()
	}

	leftRect, okL := tt.Find("左栏导航")
	centerRect, okC := tt.Find("中栏内容")
	var panelPtr *mygoui.Rect
	noOverflow := okL && okC && leftRect.X >= -0.5 && (centerRect.X+centerRect.W) <= float32(width)+0.5

	controlsVisible := true
	var missingLabels []string
	var missingTexts []string
	checkedLabels := append([]string(nil), sc.RequiredLabels...)
	if width >= 1000 && len(sc.PanelRequiredLabels) > 0 {
		checkedLabels = append(checkedLabels, sc.PanelRequiredLabels...)
	}
	if width < 1000 && len(sc.NarrowRequiredLabels) > 0 {
		checkedLabels = append(checkedLabels, sc.NarrowRequiredLabels...)
	}
	for _, label := range checkedLabels {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(width)+0.5 || r.Y+r.H > float32(height)+0.5 {
			controlsVisible = false
			missingLabels = append(missingLabels, fmt.Sprintf("%s(ok=%v,rect=%.1f,%.1f,%.1f,%.1f)", label, ok, r.X, r.Y, r.W, r.H))
		}
	}
	for _, txt := range sc.RequiredTexts {
		if !tt.HasText(txt) {
			controlsVisible = false
			missingTexts = append(missingTexts, txt)
		}
	}

	forbiddenAbsent := true
	var forbiddenFound []string
	var forbiddenTextsFound []string
	for _, label := range sc.ForbiddenLabels {
		if r, ok := tt.Find(label); ok {
			forbiddenAbsent = false
			forbiddenFound = append(forbiddenFound, fmt.Sprintf("%s(rect=%.1f,%.1f,%.1f,%.1f)", label, r.X, r.Y, r.W, r.H))
		}
	}
	for _, txt := range sc.ForbiddenTexts {
		if tt.HasText(txt) {
			forbiddenAbsent = false
			forbiddenTextsFound = append(forbiddenTextsFound, txt)
		}
	}

	// The context panel, where the page has one, sits between
	// the rail and the page (DEC-039).
	if pRect, okP := tt.Find("上下文侧栏"); okP {
		pc := pRect
		panelPtr = &pc
		if pRect.X < leftRect.X+leftRect.W-0.5 || pRect.X+pRect.W > centerRect.X+0.5 {
			noOverflow = false
		}
	}

	img := tt.Image()
	assertion := SceneAssertion{
		Scene:                   sc.Name,
		Width:                   width,
		Height:                  height,
		Screenshot:              fileName,
		LeftRect:                leftRect,
		CenterRect:              centerRect,
		PanelRect:               panelPtr,
		NoHorizontalOver:        noOverflow,
		RequiredControlsVisible: controlsVisible,
		ForbiddenControlsAbsent: forbiddenAbsent,
		RequiredChecked:         checkedLabels,
		MissingLabels:           missingLabels,
		MissingTexts:            missingTexts,
		ForbiddenChecked:        sc.ForbiddenLabels,
		ForbiddenFound:          forbiddenFound,
		ForbiddenTextsFound:     forbiddenTextsFound,
		Passed:                  noOverflow && controlsVisible && forbiddenAbsent,
	}
	if preScrollCheck != nil {
		preScrollCheck(tt, &assertion)
	}

	// Verify sidebars remain fixed when scrolling Center column (NFR-UI-001).
	tt.Scroll(centerRect.X+centerRect.W/2, centerRect.Y+300, 0, -320)
	leftAfter, _ := tt.Find("左栏导航")
	fixedSidebars := leftAfter == leftRect
	if panelPtr != nil {
		panelAfter, _ := tt.Find("上下文侧栏")
		if panelAfter != *panelPtr {
			fixedSidebars = false
		}
	}
	assertion.FixedSidebarsOnScr = fixedSidebars
	assertion.Passed = assertion.Passed && fixedSidebars
	return assertion, img
}

func runScenesAndWriteReport(outDir string, env *fixtureEnv, app *ui.App, scenes []sceneSpec, composeGuardPassed bool) error {
	widths := []int{1280, 1000, 720}
	const shotHeight = 860

	var assertions []SceneAssertion
	allPassed := composeGuardPassed
	siteFiltersOK := true
	siteBadgesOK := true
	subGridNoBadgeOK := true
	fiveSegFilterOK := true
	fiveSegChecked := 0
	xAccountNoLiveOK := true
	xAccountNoLiveChecked := 0
	loginXRiskOK := true
	loginXRiskChecked := 0
	xBadgePlaces := make(map[string]int, 6)

	for _, sc := range scenes {
		for _, w := range widths {
			sc.Setup()
			tt := mygoui.NewTester(app.View, w, shotHeight)
			fileName := fmt.Sprintf("%s-%dx%d.png", sc.Name, w, shotHeight)
			preScroll := func(tt *mygoui.Tester, a *SceneAssertion) {
				switch sc.Name {
				case "home":
					if !verifyFiveSegmentSiteFilter(tt, "主页站点筛选：", w) {
						fiveSegFilterOK = false
						siteFiltersOK = false
						a.Passed = false
					} else {
						fiveSegChecked++
					}
					if _, ok := tt.Find("主页作者站点：" + env.savedXPost.ItemKey); ok && a.RequiredControlsVisible {
						xBadgePlaces["home_post"]++
					}
				case "library":
					if !verifyFiveSegmentSiteFilter(tt, "媒体库站点筛选：", w) {
						fiveSegFilterOK = false
						siteFiltersOK = false
						a.Passed = false
					} else {
						fiveSegChecked++
					}
					if _, ok := tt.Find("媒体库站点：" + env.savedXPost.ItemKey); ok && a.RequiredControlsVisible {
						xBadgePlaces["library_card"]++
					}
				case "subscriptions":
					bar, okB := tt.Find("订阅搜索与排序栏")
					sel, okS := tt.Find("订阅排序方式")
					inp, okI := tt.Find("订阅搜索过滤框")
					if !okB || !okS || !okI ||
						bar.X < -0.5 || bar.X+bar.W > float32(w)+0.5 ||
						sel.X < -0.5 || sel.X+sel.W > float32(w)+0.5 ||
						inp.X+inp.W > sel.X+1.0 {
						a.NoHorizontalOver = false
						a.Passed = false
					}
				case "subscriptions-sort-recent":
					bar, okB := tt.Find("订阅搜索与排序栏")
					sel, okS := tt.Find("订阅排序方式")
					inp, okI := tt.Find("订阅搜索过滤框")
					if !okB || !okS || !okI ||
						bar.X < -0.5 || bar.X+bar.W > float32(w)+0.5 ||
						sel.X < -0.5 || sel.X+sel.W > float32(w)+0.5 ||
						inp.X+inp.W > sel.X+1.0 {
						a.NoHorizontalOver = false
						a.Passed = false
					}
					firstCell, ok1 := tt.Find("选择订阅 " + env.xOnlySub.ID)
					secondCell, ok2 := tt.Find("选择订阅 " + env.primarySub.ID)
					lastCell, ok3 := tt.Find("选择订阅 " + env.auroraSub.ID)
					inOrder := func(p, q mygoui.Rect) bool {
						sameRow := q.Y >= p.Y-4 && q.Y <= p.Y+4
						return q.Y > p.Y+4 || (sameRow && q.X > p.X)
					}
					if !ok1 || !ok2 || !ok3 || !inOrder(firstCell, secondCell) || !inOrder(secondCell, lastCell) {
						a.Passed = false
					}
				case "subscriptions-all-accounts":
					if !verifyFiveSegmentSiteFilter(tt, "订阅账号站点筛选：", w) {
						fiveSegFilterOK = false
						siteFiltersOK = false
						a.Passed = false
					} else {
						fiveSegChecked++
					}
				case "x-user":
					if _, ok := tt.Find("用户页站点标记"); ok && tt.HasText("X") && a.RequiredControlsVisible {
						xBadgePlaces["user_header"]++
					}
				case "subscription-accounts":
					if !verifyXAccountRowNoLiveAndNoRecord(tt, env.xUID, false) {
						xAccountNoLiveOK = false
						a.Passed = false
					} else {
						xAccountNoLiveChecked++
						xBadgePlaces["sub_accounts"]++
					}
				case "subscription-accounts-x":
					if !verifyXAccountRowNoLiveAndNoRecord(tt, env.xOnlyUID, true) {
						xAccountNoLiveOK = false
						a.Passed = false
					} else {
						xAccountNoLiveChecked++
					}
				case "tasks":
					if _, ok := tt.Find("任务站点：task_demo_x"); ok && a.RequiredControlsVisible {
						xBadgePlaces["task_row"]++
					}
				case "parse":
					if _, ok := tt.Find("历史站点：x:1000000000000000009"); ok && a.RequiredControlsVisible {
						xBadgePlaces["recent_parse"]++
					}
				case "login-x":
					if !verifyLoginXRiskDisclosure(tt, w, shotHeight) {
						loginXRiskOK = false
						a.Passed = false
					} else {
						loginXRiskChecked++
					}
				}
			}
			assertion, img := evaluateSceneWithCheck(tt, sc, w, shotHeight, fileName, preScroll)

			if sc.Name == "home" || sc.Name == "library" {
				if !assertion.RequiredControlsVisible {
					siteFiltersOK = false
				}
			}
			switch sc.Name {
			case "home", "subscription-detail", "subscription-accounts", "subscription-accounts-x", "parse", "user", "douyin-user", "weibo-user", "x-user", "library", "tasks", "tasks-queue", "tasks-live", "tasks-attention":
				if !assertion.RequiredControlsVisible {
					siteBadgesOK = false
				}
			case "subscriptions", "subscriptions-sort-recent":
				if !assertion.ForbiddenControlsAbsent {
					subGridNoBadgeOK = false
				}
			}

			var pngBuf bytes.Buffer
			if err := png.Encode(&pngBuf, img); err != nil {
				return err
			}
			if err := storage.WriteFileAtomic(filepath.Join(outDir, fileName), pngBuf.Bytes(), 0o644); err != nil {
				return err
			}

			if !assertion.Passed {
				allPassed = false
			}
			assertions = append(assertions, assertion)
		}
	}

	xBadgesInSixPlaces := len(xBadgePlaces) == 6
	for _, cnt := range xBadgePlaces {
		if cnt != len(widths) {
			xBadgesInSixPlaces = false
		}
	}
	fiveSegFilterOK = fiveSegFilterOK && fiveSegChecked == 3*len(widths)
	xAccountNoLiveOK = xAccountNoLiveOK && xAccountNoLiveChecked == 2*len(widths)
	loginXRiskOK = loginXRiskOK && loginXRiskChecked == len(widths)

	if !siteFiltersOK || !siteBadgesOK || !subGridNoBadgeOK ||
		!xBadgesInSixPlaces || !fiveSegFilterOK || !xAccountNoLiveOK || !loginXRiskOK {
		allPassed = false
	}

	report := UIShotsReport{
		GeneratedAt:                 time.Now().UTC().Format(time.RFC3339),
		HeadlessTester:              true,
		AudioMutedNullSink:          true,
		VirtualFeedCount:            5000,
		ComposeGuardPassed:          composeGuardPassed,
		SiteFiltersVerified:         siteFiltersOK,
		SiteBadgesInSixPlaces:       siteBadgesOK,
		SubscriptionGridNoSiteBadge: subGridNoBadgeOK,
		XBadgesInSixPlaces:          xBadgesInSixPlaces,
		FiveSegmentFilterNoOverflow: fiveSegFilterOK,
		XAccountRowNoLiveNoRecord:   xAccountNoLiveOK,
		LoginXRiskDisclosureShown:   loginXRiskOK,
		Assertions:                  assertions,
		Passed:                      allPassed,
	}

	rawJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := storage.WriteFileAtomic(filepath.Join(outDir, "assertions.json"), rawJSON, 0o644); err != nil {
		return err
	}
	if !allPassed {
		return fmt.Errorf("one or more UI assertions failed")
	}
	fmt.Printf("uishots: generated %d screenshots in %s (all passed)\n", len(assertions), outDir)
	return nil
}
