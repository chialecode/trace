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
	ForbiddenChecked        []string     `json:"forbiddenChecked,omitempty"`
	ForbiddenFound          []string     `json:"forbiddenFound,omitempty"`
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
	Assertions                  []SceneAssertion `json:"assertions"`
	Passed                      bool             `json:"passed"`
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

	forbiddenAbsent := true
	var forbiddenFound []string
	for _, label := range sc.ForbiddenLabels {
		if r, ok := tt.Find(label); ok {
			forbiddenAbsent = false
			forbiddenFound = append(forbiddenFound, fmt.Sprintf("%s(rect=%.1f,%.1f,%.1f,%.1f)", label, r.X, r.Y, r.W, r.H))
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

	scenePassed := noOverflow && fixedSidebars && controlsVisible && forbiddenAbsent
	return SceneAssertion{
		Scene:                   sc.Name,
		Width:                   width,
		Height:                  height,
		Screenshot:              fileName,
		LeftRect:                leftRect,
		CenterRect:              centerRect,
		PanelRect:               panelPtr,
		NoHorizontalOver:        noOverflow,
		FixedSidebarsOnScr:      fixedSidebars,
		RequiredControlsVisible: controlsVisible,
		ForbiddenControlsAbsent: forbiddenAbsent,
		RequiredChecked:         checkedLabels,
		MissingLabels:           missingLabels,
		ForbiddenChecked:        sc.ForbiddenLabels,
		ForbiddenFound:          forbiddenFound,
		Passed:                  scenePassed,
	}, img
}

func runScenesAndWriteReport(outDir string, app *ui.App, scenes []sceneSpec, composeGuardPassed bool) error {
	widths := []int{1280, 1000, 720}
	const shotHeight = 860

	var assertions []SceneAssertion
	allPassed := composeGuardPassed
	siteFiltersOK := true
	siteBadgesOK := true
	subGridNoBadgeOK := true

	for _, sc := range scenes {
		for _, w := range widths {
			sc.Setup()
			tt := mygoui.NewTester(app.View, w, shotHeight)
			fileName := fmt.Sprintf("%s-%dx%d.png", sc.Name, w, shotHeight)
			assertion, img := evaluateScene(tt, sc, w, shotHeight, fileName)

			if sc.Name == "home" || sc.Name == "library" {
				if !assertion.RequiredControlsVisible {
					siteFiltersOK = false
				}
			}
			switch sc.Name {
			case "home", "subscription-detail", "subscription-accounts", "parse", "user", "douyin-user", "weibo-user", "library", "tasks", "tasks-queue", "tasks-live", "tasks-incidents":
				if !assertion.RequiredControlsVisible {
					siteBadgesOK = false
				}
			case "subscriptions":
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

	if !siteFiltersOK || !siteBadgesOK || !subGridNoBadgeOK {
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
