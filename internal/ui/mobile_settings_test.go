package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/companion"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/uistate"
	mygoui "github.com/egoist/mygo/ui"
)

type fakeCompanionAPI struct {
	snap       companion.StatusSnapshot
	applyErr   error
	applyCalls []struct {
		enabled bool
		port    int
	}
	startCalls  int
	cancelCalls int
	approvedIDs []string
	rejectedIDs []string
	renamed     map[string]string
	revokedIDs  []string
	resetCalls  int
}

func newFakeCompanionAPI() *fakeCompanionAPI {
	return &fakeCompanionAPI{
		snap: companion.StatusSnapshot{
			Supported:    true,
			Enabled:      false,
			Running:      false,
			Port:         companionapi.DefaultStablePort,
			DefaultPort:  companionapi.DefaultStablePort,
			Channel:      companionapi.ChannelStable,
			NodeID:       "node-test-01",
			NodeName:     "DeskPC",
			IdentityCode: "A1B2-C3D4-E5F6-0718",
			CloseAction:  "tray",
		},
		renamed: map[string]string{},
	}
}

func (f *fakeCompanionAPI) Status() companion.StatusSnapshot { return f.snap }

func (f *fakeCompanionAPI) ApplySettings(enabled bool, port int) error {
	f.applyCalls = append(f.applyCalls, struct {
		enabled bool
		port    int
	}{enabled: enabled, port: port})
	if f.applyErr != nil {
		f.snap.Enabled = false
		f.snap.Running = false
		f.snap.ListenAddrs = nil
		return f.applyErr
	}
	f.snap.Enabled = enabled
	f.snap.Running = enabled
	f.snap.Port = port
	if enabled {
		f.snap.ListenAddrs = []string{"192.168.1.24"}
	} else {
		f.snap.ListenAddrs = nil
		f.snap.ActivePairing = nil
		f.snap.PendingPairs = nil
	}
	return nil
}

func (f *fakeCompanionAPI) StartPairing() (companion.PairingSessionInfo, error) {
	f.startCalls++
	payload, _ := companionapi.EncodePairPayload(companionapi.PairPayload{
		Version:     companionapi.PairPayloadVersion,
		Node:        f.snap.NodeID,
		Name:        f.snap.NodeName,
		Channel:     f.snap.Channel,
		Fingerprint: strings.Repeat("a", 64),
		Addrs:       []string{"192.168.1.24"},
		Port:        f.snap.Port,
		Code:        "0123456789abcdef0123456789abcdef",
	})
	sess := companion.PairingSessionInfo{
		Payload:      payload,
		ShortCode:    "ABCD-2345",
		IdentityCode: f.snap.IdentityCode,
		Addrs:        []string{"192.168.1.24"},
		Port:         f.snap.Port,
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	}
	f.snap.ActivePairing = &sess
	return sess, nil
}

func (f *fakeCompanionAPI) CancelPairing() {
	f.cancelCalls++
	f.snap.ActivePairing = nil
}

func (f *fakeCompanionAPI) ApprovePairing(pairID string) (companion.DeviceSummary, error) {
	f.approvedIDs = append(f.approvedIDs, pairID)
	dev := companion.DeviceSummary{
		ID:       "dev-new-1",
		Name:     "我的 Android 手机",
		Model:    "Pixel 9",
		Platform: "android",
		Online:   true,
		LastAddr: "192.168.1.50",
	}
	f.snap.PendingPairs = nil
	f.snap.ActivePairing = nil
	f.snap.Devices = append(f.snap.Devices, dev)
	return dev, nil
}

func (f *fakeCompanionAPI) RejectPairing(pairID string) error {
	f.rejectedIDs = append(f.rejectedIDs, pairID)
	f.snap.PendingPairs = nil
	return nil
}

func (f *fakeCompanionAPI) RenameDevice(deviceID, newName string) error {
	f.renamed[deviceID] = newName
	for i := range f.snap.Devices {
		if f.snap.Devices[i].ID == deviceID {
			f.snap.Devices[i].Name = newName
		}
	}
	return nil
}

func (f *fakeCompanionAPI) RevokeDevice(deviceID string) error {
	f.revokedIDs = append(f.revokedIDs, deviceID)
	out := f.snap.Devices[:0]
	for _, d := range f.snap.Devices {
		if d.ID != deviceID {
			out = append(out, d)
		}
	}
	f.snap.Devices = out
	return nil
}

func (f *fakeCompanionAPI) ResetIdentity() error {
	f.resetCalls++
	f.snap.Devices = nil
	f.snap.IdentityCode = "1111-2222-3333-4444"
	return nil
}

func TestMobileSettingsTogglePortPairingAndDevices(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	fake := newFakeCompanionAPI()
	app.BindCompanion(fake)
	app.SettingsCategory = "mobile"
	app.Navigate(RouteSettings)

	tt := mygoui.NewTester(app.View, 1280, 860)
	settle(tt, 2)
	if !tt.HasText("允许手机连接") {
		t.Fatalf("expected master switch text on Settings › 移动端")
	}
	if _, ok := tt.Find("添加手机"); !ok {
		t.Fatalf("expected 添加手机 button")
	}

	// Toggle master switch on.
	if err := tt.Click("允许手机连接开关"); err != nil {
		t.Fatalf("click master switch: %v", err)
	}
	settle(tt, 2)
	if !fake.snap.Enabled || !app.DraftSettings.Companion.Enabled {
		t.Fatalf("expected companion enabled in manager and draft settings")
	}
	if !tt.HasText("正在监听 192.168.1.24:47381") {
		t.Fatalf("expected listening status text after enabling; texts=%v", tt.Texts())
	}

	// Port busy failure keeps companion disabled.
	fake.applyErr = errors.New("端口 47389 已被其他程序占用")
	app.Mobile.PortInput = "47389"
	if err := tt.Click("应用移动端端口"); err != nil {
		t.Fatalf("click apply port: %v", err)
	}
	settle(tt, 2)
	if app.Mobile.Err == "" {
		t.Fatalf("expected error banner on port busy")
	}
	if app.DraftSettings.Companion.Enabled {
		t.Fatalf("expected companion disabled in settings after bind error")
	}

	// Recover with valid port and re-enable.
	fake.applyErr = nil
	app.Mobile.PortInput = "47381"
	if err := tt.Click("应用移动端端口"); err != nil {
		t.Fatalf("click apply port recover: %v", err)
	}
	settle(tt, 2)
	if err := tt.Click("允许手机连接开关"); err != nil {
		t.Fatalf("re-enable companion: %v", err)
	}
	settle(tt, 2)

	// Open pairing QR dialog.
	if err := tt.Click("添加手机"); err != nil {
		t.Fatalf("click 添加手机: %v", err)
	}
	settle(tt, 2)
	if !app.Mobile.PairDialogOpen || app.Mobile.PairQRBitmap == nil {
		t.Fatalf("expected pairing QR dialog open with rendered QR bitmap")
	}
	_, okQR := tt.Find("移动端配对二维码")
	hasID := tt.HasText("A1B2-C3D4-E5F6-0718")
	hasCode := tt.HasText("ABCD-2345")
	if !okQR || !hasID || !hasCode {
		t.Fatalf("expected QR image, identity code, and short code in dialog (okQR=%v hasID=%v hasCode=%v texts=%v)", okQR, hasID, hasCode, tt.Texts())
	}

	// Simulate incoming phone pairing request -> desktop confirmation dialog.
	fake.snap.PendingPairs = []companion.PendingPairInfo{{
		PairID:     "pair-1",
		DeviceName: "我的 Android 手机",
		Model:      "Pixel 9",
		Platform:   "android",
		RemoteAddr: "192.168.1.50",
		ExpiresAt:  time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	}}
	settle(tt, 2)
	if !tt.HasText("允许『我的 Android 手机』连接这台电脑？") {
		t.Fatalf("expected desktop confirmation prompt")
	}
	if err := tt.Click("允许手机连接"); err != nil {
		t.Fatalf("click approve pairing: %v", err)
	}
	settle(tt, 2)
	if len(fake.approvedIDs) != 1 || fake.approvedIDs[0] != "pair-1" {
		t.Fatalf("approvedIDs = %v, want [pair-1]", fake.approvedIDs)
	}
	if _, ok := tt.Find("已配对手机：我的 Android 手机"); !ok {
		t.Fatalf("expected newly paired device row")
	}

	// Rename device.
	if err := tt.Click("改名手机：我的 Android 手机"); err != nil {
		t.Fatalf("click rename: %v", err)
	}
	settle(tt, 2)
	app.Mobile.RenameInput = "主力机"
	if err := tt.Click("保存手机名称"); err != nil {
		t.Fatalf("click save rename: %v", err)
	}
	settle(tt, 2)
	if fake.renamed["dev-new-1"] != "主力机" {
		t.Fatalf("renamed = %v, want 主力机", fake.renamed)
	}

	// Revoke device via inline confirmation.
	if err := tt.Click("移除手机：主力机"); err != nil {
		t.Fatalf("click revoke: %v", err)
	}
	settle(tt, 2)
	if err := tt.Click("确认移除手机"); err != nil {
		t.Fatalf("click confirm revoke: %v", err)
	}
	settle(tt, 2)
	if len(fake.revokedIDs) != 1 || fake.revokedIDs[0] != "dev-new-1" {
		t.Fatalf("revokedIDs = %v, want [dev-new-1]", fake.revokedIDs)
	}

	// Reset identity via inline confirmation.
	if err := tt.Click("重置移动端身份"); err != nil {
		t.Fatalf("click reset identity: %v", err)
	}
	settle(tt, 2)
	if err := tt.Click("确认重置移动端身份"); err != nil {
		t.Fatalf("click confirm reset: %v", err)
	}
	settle(tt, 2)
	if fake.resetCalls != 1 {
		t.Fatalf("resetCalls = %d, want 1", fake.resetCalls)
	}
}

func TestMobileFeedSeenEventSyncsDesktopHomeBaseline(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	if _, err := app.backend.MutateUIState(func(st *uistate.State) {
		st.HomeSeen = map[string]int64{"sub-1": 100}
	}); err != nil {
		t.Fatalf("MutateUIState: %v", err)
	}
	app.RestartHomeSession()
	app.Navigate(RouteHome)
	if app.homeSeenLocal["sub-1"] != 100 {
		t.Fatalf("initial homeSeenLocal = %v, want 100", app.homeSeenLocal["sub-1"])
	}

	// Simulate phone calling POST /api/v1/feed/seen for sub-1.
	if _, err := app.backend.MutateUIState(func(st *uistate.State) {
		st.HomeSeen["sub-1"] = 999
	}); err != nil {
		t.Fatalf("MutateUIState phone seen: %v", err)
	}
	app.handleBackendEvent(service.Event{
		Type:    service.EventLibraryChanged,
		Message: "feed_seen:sub-1",
	})
	if app.homeSeenLocal["sub-1"] != 999 {
		t.Fatalf("synced homeSeenLocal = %d, want 999", app.homeSeenLocal["sub-1"])
	}
}
