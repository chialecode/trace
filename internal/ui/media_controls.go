package ui

import "github.com/chialecode/trace/internal/uistate"

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

func (a *App) cancelPlayerPreview() {
	if a.playerPreviewCancel != nil {
		a.playerPreviewCancel()
		a.playerPreviewCancel = nil
	}
}

func (a *App) closePlayerOverlay() {
	a.cancelPlayerPreview()
	a.playerOpenSeq++
	a.setPlayerFullscreen(false)
	a.PlayerOverlay.Open = false
	a.PlayerOverlay.MoreMenuOpen = false
	a.PlayerOverlay.VolumeMenuOpen = false
	a.PlayerOverlay.Loading = false
	a.PlayerOverlay.LoadError = ""
	a.PlayerOverlay.DecoderMissing = false
	a.PlayerOverlay.MissingReason = ""
	a.PlayerOverlay.LoopMuted = false
	a.PlayerOverlay.LoopPaused = false
	a.PlayerOverlay.PreviewMode = false
	a.PlayerOverlay.PreviewSite = ""
	a.PlayerOverlay.PreviewItemKey = ""
	a.PlayerOverlay.PreviewPendingKey = ""
	a.PlayerOverlay.PreviewCacheQueued = false
	a.PlayerOverlay.PreviewCacheError = ""
	a.PlayerOverlay.PreviewCacheToast = ""
	a.PlayerOverlay.PreviewOnCache = nil
	_ = a.player.Close()
}

func (a *App) closeImageViewer() {
	a.stopViewerInlinePlayback()
	a.imageViewerRevision++
	if a.imageViewerCancel != nil {
		a.imageViewerCancel()
		a.imageViewerCancel = nil
	}
	a.ImageViewer.Open = false
	a.ImageViewer.Bitmaps = nil
	a.ImageViewer.NoWatermarkPaths = nil
	a.ImageViewer.NoWatermarkBitmaps = nil
	a.ImageViewer.NoWatermarkErrors = nil
	a.ImageViewer.ShowNoWatermark = false
	a.ImageViewer.InlineLoopMuted = nil
	a.ImageViewer.InlineLoopPaused = false
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

// applyPlayerVolume updates volume and mute on all active player controllers
// without touching disk (used while dragging the volume slider).
func (a *App) applyPlayerVolume(volume float64, muted bool) {
	a.player.SetVolume(volume)
	a.player.SetMuted(muted)
	if a.bgmPlayer != nil {
		a.bgmPlayer.SetVolume(volume)
		a.bgmPlayer.SetMuted(muted)
	}
	if a.viewerPlayer != nil {
		a.viewerPlayer.SetVolume(volume)
		a.viewerPlayer.SetMuted(muted)
	}
	if a.livePreviewPlayer != nil {
		a.livePreviewPlayer.SetVolume(volume)
		a.livePreviewPlayer.SetMuted(muted)
	}
}

func (a *App) persistPlayerVolume() {
	muted := a.player.Snapshot(1, 1).Muted
	a.applyPlayerVolume(a.PlayerOverlay.Volume, muted)
	volume := a.PlayerOverlay.Volume
	if _, err := a.backend.MutateUIState(func(st *uistate.State) {
		st.Player.Volume, st.Player.Muted = volume, muted
	}); err != nil {
		a.showError(err)
	}
}
