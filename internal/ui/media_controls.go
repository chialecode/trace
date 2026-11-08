package ui

// BindFullscreenHandler connects the player control to the native shell window.
// Memory-only UI tests inject a recorder instead of creating a window.
func (a *App) BindFullscreenHandler(set func(bool)) {
	a.fullscreen = set
}

func (a *App) setPlayerFullscreen(fullscreen bool) {
	if a.PlayerOverlay.Fullscreen == fullscreen {
		return
	}
	a.PlayerOverlay.Fullscreen = fullscreen
	if a.fullscreen != nil {
		a.fullscreen(fullscreen)
	}
}

func (a *App) closePlayerOverlay() {
	a.setPlayerFullscreen(false)
	a.PlayerOverlay.Open = false
	a.PlayerOverlay.MoreMenuOpen = false
	a.PlayerOverlay.VolumeMenuOpen = false
	_ = a.player.Close()
}

func (a *App) closeImageViewer() {
	a.imageViewerRevision++
	if a.imageViewerCancel != nil {
		a.imageViewerCancel()
		a.imageViewerCancel = nil
	}
	a.ImageViewer.Open = false
	a.ImageViewer.Bitmaps = nil
}

func playerAreaName(area float64) string {
	if area <= 0.25 {
		return "quarter"
	}
	if area >= 1 {
		return "full"
	}
	return "half"
}

// playerAreaLabels are the visible choices for the stored area values
// "quarter", "half" and "full"; machine values never appear in the menu.
var playerAreaLabels = []string{"上方 1/4", "上半屏", "全屏"}

func playerAreaLabel(area string) string {
	switch area {
	case "quarter":
		return playerAreaLabels[0]
	case "full":
		return playerAreaLabels[2]
	default:
		return playerAreaLabels[1]
	}
}

func playerAreaValue(label string) string {
	switch label {
	case playerAreaLabels[0]:
		return "quarter"
	case playerAreaLabels[2]:
		return "full"
	default:
		return "half"
	}
}

func playerDensityLabel(density string) string {
	switch density {
	case "low":
		return "低"
	case "high":
		return "高"
	case "unlimited":
		return "不限"
	default:
		return "中"
	}
}

func playerDensityValue(label string) string {
	switch label {
	case "低":
		return "low"
	case "高":
		return "high"
	case "不限":
		return "unlimited"
	default:
		return "medium"
	}
}

func playerSubtitleLabel(lang string) string {
	if lang == "" {
		return "关闭"
	}
	return lang
}

func (a *App) persistPlayerVolume() {
	a.player.SetVolume(a.PlayerOverlay.Volume)
	st := a.backend.GetUIState()
	st.Player.Volume = a.PlayerOverlay.Volume
	st.Player.Muted = a.player.Snapshot(1, 1).Muted
	if _, err := a.backend.UpdateUIState(st); err != nil {
		a.showError(err)
	}
}
