package main

import (
	"strings"
	"time"

	"github.com/chialecode/trace/internal/companion"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/ui"
)

// Settings › 移动端 screenshot fixtures: synthetic companion service states answered
// entirely from memory without opening any sockets.

type mobileShotCompanion struct {
	snap companion.StatusSnapshot
}

func (m *mobileShotCompanion) Status() companion.StatusSnapshot { return m.snap }
func (m *mobileShotCompanion) ApplySettings(bool, int) error    { return nil }
func (m *mobileShotCompanion) StartPairing() (companion.PairingSessionInfo, error) {
	return companion.PairingSessionInfo{}, nil
}
func (m *mobileShotCompanion) CancelPairing() {}
func (m *mobileShotCompanion) ApprovePairing(string) (companion.DeviceSummary, error) {
	return companion.DeviceSummary{}, nil
}
func (m *mobileShotCompanion) RejectPairing(string) error        { return nil }
func (m *mobileShotCompanion) RenameDevice(string, string) error { return nil }
func (m *mobileShotCompanion) RevokeDevice(string) error         { return nil }
func (m *mobileShotCompanion) ResetIdentity() error              { return nil }

func mobileShotBaseSnapshot(enabled bool) companion.StatusSnapshot {
	snap := companion.StatusSnapshot{
		Supported:    true,
		Enabled:      enabled,
		Running:      enabled,
		Port:         companionapi.DefaultStablePort,
		DefaultPort:  companionapi.DefaultStablePort,
		Channel:      companionapi.ChannelStable,
		NodeID:       "node-shot-mobile",
		NodeName:     "我的台式机",
		Fingerprint:  strings.Repeat("a1", 32),
		IdentityCode: "A1B2-C3D4-E5F6-0718",
		CloseAction:  "tray",
	}
	if enabled {
		snap.ListenAddrs = []string{"192.168.1.24", "100.64.0.12"}
	}
	return snap
}

func mobileShotDevices() []companion.DeviceSummary {
	now := time.Now().UTC()
	return []companion.DeviceSummary{
		{
			ID:         "dev-shot-1",
			Name:       "主力 Android 手机",
			Model:      "Pixel 9",
			Platform:   "android",
			Channel:    companionapi.ChannelStable,
			PairedAt:   now.Add(-48 * time.Hour).Format(time.RFC3339),
			LastSeenAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
			LastAddr:   "192.168.1.50",
			Online:     true,
		},
		{
			ID:         "dev-shot-2",
			Name:       "平板（客厅）",
			Model:      "Xiaomi Pad",
			Platform:   "android",
			Channel:    companionapi.ChannelStable,
			PairedAt:   now.Add(-7 * 24 * time.Hour).Format(time.RFC3339),
			LastSeenAt: now.Add(-14 * time.Hour).Format(time.RFC3339),
			LastAddr:   "192.168.1.63",
			Online:     false,
		},
	}
}

func mobileShotPairingSession() *companion.PairingSessionInfo {
	exp := time.Now().UTC().Add(4*time.Minute + 30*time.Second)
	payload, _ := companionapi.EncodePairPayload(companionapi.PairPayload{
		Version:     companionapi.PairPayloadVersion,
		Node:        "node-shot-mobile",
		Name:        "我的台式机",
		Channel:     companionapi.ChannelStable,
		Fingerprint: strings.Repeat("a1", 32),
		Addrs:       []string{"192.168.1.24", "100.64.0.12"},
		Port:        companionapi.DefaultStablePort,
		Code:        "0123456789abcdef0123456789abcdef",
	})
	return &companion.PairingSessionInfo{
		Payload:      payload,
		ShortCode:    "K7M3-P9Q2",
		IdentityCode: "A1B2-C3D4-E5F6-0718",
		Addrs:        []string{"192.168.1.24", "100.64.0.12"},
		Port:         companionapi.DefaultStablePort,
		ExpiresAt:    exp.Format(time.RFC3339),
	}
}

func mobileShotScenes(app *ui.App) []sceneSpec {
	openMobile := func(snap companion.StatusSnapshot) {
		app.PlayerOverlay.Open, app.ImageViewer.Open = false, false
		app.ConflictDiff.Open = false
		app.Mobile = ui.MobileUIState{}
		app.BindCompanion(&mobileShotCompanion{snap: snap})
		app.SettingsCategory = "mobile"
		app.Navigate(ui.RouteSettings)
	}
	return []sceneSpec{
		{
			Name: "settings-mobile-off",
			Setup: func() {
				openMobile(mobileShotBaseSnapshot(false))
			},
			RequiredLabels: []string{
				"设置移动端面板",
				"允许手机连接开关",
				"移动端监听端口",
				"应用移动端端口",
				"添加手机",
				"重置移动端身份",
			},
			RequiredTexts: []string{
				"允许手机连接",
				"监听端口",
			},
		},
		{
			Name: "settings-mobile-active",
			Setup: func() {
				snap := mobileShotBaseSnapshot(true)
				snap.Devices = mobileShotDevices()
				openMobile(snap)
			},
			RequiredLabels: []string{
				"设置移动端面板",
				"移动端监听状态",
				"添加手机",
				"已配对手机：主力 Android 手机",
				"已配对手机：平板（客厅）",
				"改名手机：主力 Android 手机",
				"移除手机：主力 Android 手机",
			},
			RequiredTexts: []string{
				"正在监听 192.168.1.24:47381 · 100.64.0.12:47381",
				"在线 · 192.168.1.50",
				"电脑身份码：A1B2-C3D4-E5F6-0718",
			},
		},
		{
			Name: "settings-mobile-pair-qr",
			Setup: func() {
				snap := mobileShotBaseSnapshot(true)
				snap.Devices = mobileShotDevices()[:1]
				snap.ActivePairing = mobileShotPairingSession()
				openMobile(snap)
				app.Mobile.PairDialogOpen = true
			},
			RequiredLabels: []string{
				"添加手机对话框",
				"移动端配对二维码",
				"刷新配对二维码",
				"关闭添加手机对话框",
			},
			RequiredTexts: []string{
				"K7M3-P9Q2",
				"A1B2-C3D4-E5F6-0718",
				"仅可使用一次",
			},
		},
		{
			Name: "settings-mobile-pair-confirm",
			Setup: func() {
				snap := mobileShotBaseSnapshot(true)
				snap.PendingPairs = []companion.PendingPairInfo{{
					PairID:     "pair-shot-1",
					DeviceName: "我的 Android 手机",
					Model:      "Pixel 9",
					Platform:   "android",
					RemoteAddr: "192.168.1.50",
					ExpiresAt:  time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339),
				}}
				openMobile(snap)
				app.Mobile.PairDialogOpen = true
			},
			RequiredLabels: []string{
				"添加手机对话框",
				"拒绝手机连接",
				"允许手机连接",
			},
			RequiredTexts: []string{
				"允许『我的 Android 手机』连接这台电脑？",
				"Pixel 9 · android · 192.168.1.50",
			},
		},
	}
}
