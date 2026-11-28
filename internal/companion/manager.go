package companion

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/service"
)

// StatusSnapshot is the desktop UI's view of the companion service ("设置 › 移动端").
type StatusSnapshot struct {
	Supported      bool                `json:"supported"`
	Enabled        bool                `json:"enabled"`
	Running        bool                `json:"running"`
	PreventSleep   bool                `json:"preventSleep"`
	SleepInhibited bool                `json:"sleepInhibited"`
	MDNSActive     bool                `json:"mdnsActive"`
	Port           int                 `json:"port"`
	DefaultPort    int                 `json:"defaultPort"`
	Channel        string              `json:"channel"`
	NodeID         string              `json:"nodeId"`
	NodeName       string              `json:"nodeName"`
	Fingerprint    string              `json:"fingerprint,omitempty"`
	IdentityCode   string              `json:"identityCode,omitempty"`
	ListenAddrs    []string            `json:"listenAddrs,omitempty"`
	LastError      string              `json:"lastError,omitempty"`
	Devices        []DeviceSummary     `json:"devices"`
	ActivePairing  *PairingSessionInfo `json:"activePairing,omitempty"`
	PendingPairs   []PendingPairInfo   `json:"pendingPairs,omitempty"`
	CloseAction    string              `json:"closeAction,omitempty"`
}

// ManagerOptions configures a desktop companion Manager.
type ManagerOptions struct {
	DataDir        string
	Channel        string
	NodeName       string
	Version        string
	Backend        service.BackendAPI
	Now            func() time.Time
	Logf           func(format string, args ...any)
	EnumIfaces     func() ([]NetworkInterface, error)
	RebindEvery    time.Duration
	PollMaxWait    time.Duration
	OnChange       func()
	SleepInhibitor SleepInhibitor
	MDNSFactory    MDNSFactory
	// ListenOverrides bypasses LAN interface enumeration for loopback integration tests
	// and tools/companiondev (e.g. ["127.0.0.1"] or ["0.0.0.0"]).
	ListenOverrides []string
	// AdvertiseOverrides replaces the addresses written into QR payloads (e.g. ["10.0.2.2"]).
	AdvertiseOverrides []string
}

type activeListener struct {
	addr    string
	virtual bool
	ln      net.Listener
	srv     *http.Server
}

// Manager owns the companion service lifecycle: TLS identity, companion.json,
// LAN/VPN listeners, interface-change rebinding, EventHub bridge, pairing, and shutdown.
// When disabled, zero listeners and zero background goroutines are active (NFR-MOB-RES-001).
type Manager struct {
	dataDir            string
	channel            string
	supported          bool
	nodeName           string
	version            string
	backend            service.BackendAPI
	now                func() time.Time
	logf               func(format string, args ...any)
	enumIfaces         func() ([]NetworkInterface, error)
	rebindEvery        time.Duration
	pollMaxWait        time.Duration
	onChange           func()
	listenOverrides    []string
	advertiseOverrides []string
	sleepInhibitor     SleepInhibitor
	mdnsFactory        MDNSFactory

	store   *Store
	pairing *pairingManager
	hub     *eventHub
	thumbs  *thumbCache
	server  *Server

	sleepMu     sync.Mutex
	sleepActive bool

	mu            sync.Mutex
	enabled       bool
	running       bool
	preventSleep  bool
	port          int
	identity      TLSIdentity
	listeners     []*activeListener
	listenAddrs   []string
	lastError     string
	unsubBackend  func()
	monitorCancel context.CancelFunc
	monitorWg     sync.WaitGroup
	mdns          *mdnsAdvertiser
}

// NewManager initializes companion.json and prepares the manager without starting any listener.
func NewManager(opt ManagerOptions) (*Manager, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.EnumIfaces == nil {
		opt.EnumIfaces = EnumerateNetworkInterfaces
	}
	if opt.RebindEvery <= 0 {
		opt.RebindEvery = 10 * time.Second
	}
	if opt.Version == "" {
		opt.Version = appdirs.BuildInfo().Version
	}
	if opt.NodeName == "" {
		opt.NodeName, _ = os.Hostname()
		if opt.NodeName == "" {
			opt.NodeName = "我的电脑"
		}
	}

	rawCh := opt.Channel
	if rawCh == "" {
		rawCh = appdirs.BuildInfo().Channel
	}
	ch, supported := companionapi.NormalizeChannel(rawCh)
	if !supported {
		// trace-test rehearsal identity never runs the companion service (design §3, §10).
		ch = rawCh
	}

	store, err := OpenStore(opt.DataDir, opt.NodeName)
	if err != nil {
		return nil, err
	}

	inhibitor := opt.SleepInhibitor
	if inhibitor == nil {
		inhibitor = newPlatformSleepInhibitor()
	}

	m := &Manager{
		dataDir:            opt.DataDir,
		channel:            ch,
		supported:          supported,
		nodeName:           opt.NodeName,
		version:            opt.Version,
		backend:            opt.Backend,
		now:                opt.Now,
		logf:               opt.Logf,
		enumIfaces:         opt.EnumIfaces,
		rebindEvery:        opt.RebindEvery,
		pollMaxWait:        opt.PollMaxWait,
		onChange:           opt.OnChange,
		listenOverrides:    append([]string(nil), opt.ListenOverrides...),
		advertiseOverrides: append([]string(nil), opt.AdvertiseOverrides...),
		sleepInhibitor:     inhibitor,
		mdnsFactory:        opt.MDNSFactory,
		store:              store,
		thumbs:             newThumbCache(opt.DataDir),
		port:               companionapi.DefaultPortForChannel(ch),
	}

	notify := func() {
		if m.onChange != nil {
			m.onChange()
		}
	}
	onHubDiff := func() {
		m.reconcileSleepInhibitor()
		notify()
	}
	m.pairing = newPairingManager(opt.Now, notify)
	m.hub = newEventHub(opt.Now, onHubDiff)
	m.server = NewServer(ServerOptions{
		Backend:          opt.Backend,
		Store:            store,
		Pairing:          m.pairing,
		Hub:              m.hub,
		Thumbs:           m.thumbs,
		Channel:          ch,
		NodeName:         opt.NodeName,
		Version:          opt.Version,
		StartedAt:        opt.Now(),
		Now:              opt.Now,
		AddrsFunc:        m.advertisedAddrs,
		MDNSActiveFunc:   m.mdnsActive,
		PreventSleepFunc: m.preventSleepEnabled,
		Logf:             opt.Logf,
		OnFeedSeen:       notify,
		PollMaxWait:      opt.PollMaxWait,
	})
	return m, nil
}

// CorruptBackup returns any corrupt companion.json backup path created at startup.
func (m *Manager) CorruptBackup() string {
	return m.store.CorruptBackup()
}

// Server returns the underlying HTTP handler (useful for unit tests).
func (m *Manager) Server() *Server {
	return m.server
}

// Store returns the companion device store.
func (m *Manager) Store() *Store {
	return m.store
}

// ApplySettings reconciles the running state with settings.Companion {enabled, port}.
// When enabled==false, stops listening immediately after sending bye{reason: "disabled"}.
// When port is busy, keeps the service stopped and returns a user-facing error (never auto-switches port).
func (m *Manager) ApplySettings(enabled bool, port int) error {
	if !m.supported {
		if enabled {
			return errors.New("测试演练通道不提供移动端服务")
		}
		return nil
	}
	if port <= 0 {
		port = companionapi.DefaultPortForChannel(m.channel)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口 %d 不正确，请输入 1～65535 之间的数字", port)
	}

	if !enabled {
		m.stop(companionapi.ByeReasonDisabled)
		m.mu.Lock()
		m.enabled = false
		m.port = port
		m.lastError = ""
		m.mu.Unlock()
		m.notifyChange()
		return nil
	}

	m.mu.Lock()
	alreadySame := m.running && m.port == port
	m.mu.Unlock()
	if alreadySame {
		return nil
	}

	// Stop previous listener if changing ports.
	m.stop(companionapi.ByeReasonDisabled)

	id, err := EnsureTLSIdentity(m.dataDir)
	if err != nil {
		m.setError(port, "无法准备 TLS 证书："+err.Error())
		return err
	}

	candidates, err := m.computeCandidates()
	if err != nil {
		m.setError(port, "无法读取本机局域网地址："+err.Error())
		return err
	}
	if len(candidates) == 0 {
		err := errors.New("未找到可用的局域网或 VPN 地址，请检查网络连接")
		m.setError(port, err.Error())
		return err
	}

	listeners, boundPort, boundAddrs, err := m.bindCandidates(candidates, port, id.TLSConfig)
	if err != nil {
		msg := formatBindError(port, err)
		m.setError(port, msg)
		return errors.New(msg)
	}

	var unsub func()
	if m.backend != nil {
		unsub = m.backend.SubscribeEvents(m.handleBackendEvent)
	}

	monCtx, monCancel := context.WithCancel(context.Background())

	var adv *mdnsAdvertiser
	if len(m.listenOverrides) == 0 || m.mdnsFactory != nil {
		cfg := newMDNSRecordConfig(m.store.NodeID(), m.nodeName, m.channel, m.version, boundPort)
		adv = startMDNSAdvertiser(cfg, candidates, m.mdnsFactory, m.logf)
	}

	m.mu.Lock()
	m.enabled = true
	m.running = true
	m.port = boundPort
	m.identity = id
	m.listeners = listeners
	m.listenAddrs = boundAddrs
	m.lastError = ""
	m.unsubBackend = unsub
	m.monitorCancel = monCancel
	m.mdns = adv
	m.mu.Unlock()

	if len(m.listenOverrides) == 0 && m.rebindEvery > 0 {
		m.monitorWg.Add(1)
		go m.watchInterfaces(monCtx)
	}

	m.reconcileSleepInhibitor()
	m.logf("companion listening on %s (port %d)", strings.Join(boundAddrs, ", "), boundPort)
	m.notifyChange()
	return nil
}

// SetPreventSleep toggles whether the system stays awake while at least one
// mobile client is connected. Takes effect immediately without restarting listeners.
func (m *Manager) SetPreventSleep(enabled bool) {
	m.mu.Lock()
	m.preventSleep = enabled
	m.mu.Unlock()
	m.reconcileSleepInhibitor()
	m.notifyChange()
}

func (m *Manager) preventSleepEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.preventSleep
}

func (m *Manager) mdnsActive() bool {
	m.mu.Lock()
	adv := m.mdns
	m.mu.Unlock()
	return adv.Active()
}

func (m *Manager) reconcileSleepInhibitor() {
	if m.sleepInhibitor == nil {
		return
	}
	m.sleepMu.Lock()
	defer m.sleepMu.Unlock()

	m.mu.Lock()
	want := m.running && m.enabled && m.preventSleep
	m.mu.Unlock()
	if want {
		want = m.hub != nil && m.hub.ClientCount() > 0
	}
	if want == m.sleepActive {
		return
	}
	if err := m.sleepInhibitor.SetActive(want); err != nil {
		m.logf("companion prevent-sleep(%v) failed: %v", want, err)
		return
	}
	m.sleepActive = want
}

// Shutdown broadcasts bye{reason} ("exit" or "update") with a <= 1s cap, stops mDNS
// with a goodbye packet, clears sleep inhibition, and stops all listeners.
func (m *Manager) Shutdown(reason string) {
	if reason == "" {
		reason = companionapi.ByeReasonExit
	}
	m.stop(reason)
	if m.sleepInhibitor != nil {
		m.sleepMu.Lock()
		if m.sleepActive {
			_ = m.sleepInhibitor.SetActive(false)
			m.sleepActive = false
		}
		_ = m.sleepInhibitor.Close()
		m.sleepMu.Unlock()
	}
}

func (m *Manager) stop(byeReason string) {
	m.mu.Lock()
	wasRunning := m.running
	listeners := m.listeners
	unsub := m.unsubBackend
	monCancel := m.monitorCancel
	adv := m.mdns
	m.running = false
	m.listeners = nil
	m.listenAddrs = nil
	m.unsubBackend = nil
	m.monitorCancel = nil
	m.mdns = nil
	m.mu.Unlock()

	if monCancel != nil {
		monCancel()
		m.monitorWg.Wait()
	}
	if adv != nil {
		adv.Stop()
	}
	if unsub != nil {
		unsub()
	}
	m.pairing.ClearAll()
	if wasRunning {
		// Broadcast bye{reason} with max 1s wait (design §3).
		m.hub.BroadcastBye(byeReason, time.Second)
	}
	m.reconcileSleepInhibitor()
	for _, al := range listeners {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_ = al.srv.Shutdown(ctx)
		cancel()
		_ = al.ln.Close()
	}
}

func (m *Manager) setError(port int, msg string) {
	m.mu.Lock()
	m.enabled = false
	m.running = false
	m.port = port
	m.lastError = msg
	m.mu.Unlock()
	m.notifyChange()
}

func (m *Manager) computeCandidates() ([]ListenCandidate, error) {
	if len(m.listenOverrides) > 0 {
		out := make([]ListenCandidate, 0, len(m.listenOverrides))
		for _, raw := range m.listenOverrides {
			addr, err := netip.ParseAddr(strings.TrimSpace(raw))
			if err != nil {
				return nil, fmt.Errorf("无效的监听地址 %q", raw)
			}
			out = append(out, ListenCandidate{
				Addr:    addr.Unmap(),
				Virtual: true, // allow CGNAT/loopback in dev/test overrides
				Iface:   "override",
			})
		}
		return out, nil
	}
	ifaces, err := m.enumIfaces()
	if err != nil {
		return nil, err
	}
	return SelectListenCandidates(ifaces), nil
}

func (m *Manager) bindCandidates(candidates []ListenCandidate, port int, tlsCfg *tls.Config) ([]*activeListener, int, []string, error) {
	var opened []*activeListener
	var addrs []string
	effectivePort := port

	cleanup := func() {
		for _, al := range opened {
			_ = al.ln.Close()
		}
	}

	for _, c := range candidates {
		host := c.Addr.String()
		bindAddr := net.JoinHostPort(host, strconv.Itoa(effectivePort))
		rawLn, err := net.Listen("tcp", bindAddr)
		if err != nil {
			// If port is explicitly busy (EADDRINUSE), fail immediately rather than silently skipping (design §3).
			cleanup()
			return nil, 0, nil, err
		}
		if tcpAddr, ok := rawLn.Addr().(*net.TCPAddr); ok && effectivePort == 0 {
			effectivePort = tcpAddr.Port
		}
		isVirtual := c.Virtual
		hs := &http.Server{
			Handler:           m.server,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
			ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
				return withVirtualInterface(ctx, isVirtual)
			},
			ErrorLog: log.New(companionLogWriter{m.logf}, "", 0),
		}
		tlsLn := tls.NewListener(rawLn, tlsCfg)
		go func(srv *http.Server, l net.Listener) {
			_ = srv.Serve(l)
		}(hs, tlsLn)

		opened = append(opened, &activeListener{
			addr:    host,
			virtual: isVirtual,
			ln:      tlsLn,
			srv:     hs,
		})
		addrs = append(addrs, host)
	}
	return opened, effectivePort, addrs, nil
}

func (m *Manager) watchInterfaces(ctx context.Context) {
	defer m.monitorWg.Done()
	ticker := time.NewTicker(m.rebindEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcileInterfaces()
		}
	}
}

func (m *Manager) reconcileInterfaces() {
	m.mu.Lock()
	if !m.running || !m.enabled {
		m.mu.Unlock()
		return
	}
	port := m.port
	tlsCfg := m.identity.TLSConfig
	current := make(map[string]*activeListener, len(m.listeners))
	for _, al := range m.listeners {
		current[al.addr] = al
	}
	m.mu.Unlock()

	candidates, err := m.computeCandidates()
	if err != nil {
		return
	}
	want := make(map[string]ListenCandidate, len(candidates))
	for _, c := range candidates {
		want[c.Addr.String()] = c
	}

	changed := false
	// Close removed interfaces.
	var kept []*activeListener
	for _, al := range current {
		if _, still := want[al.addr]; !still {
			_ = al.ln.Close()
			changed = true
		} else {
			kept = append(kept, al)
		}
	}
	// Open newly appeared interfaces.
	var newlyOpened []*activeListener
	for _, c := range candidates {
		host := c.Addr.String()
		if _, exists := current[host]; exists {
			continue
		}
		rawLn, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		isVirtual := c.Virtual
		hs := &http.Server{
			Handler:           m.server,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
			ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
				return withVirtualInterface(ctx, isVirtual)
			},
			ErrorLog: log.New(companionLogWriter{m.logf}, "", 0),
		}
		tlsLn := tls.NewListener(rawLn, tlsCfg)
		go func(srv *http.Server, l net.Listener) {
			_ = srv.Serve(l)
		}(hs, tlsLn)
		al := &activeListener{addr: host, virtual: isVirtual, ln: tlsLn, srv: hs}
		kept = append(kept, al)
		newlyOpened = append(newlyOpened, al)
		changed = true
	}

	if changed {
		addrs := make([]string, 0, len(kept))
		for _, al := range kept {
			addrs = append(addrs, al.addr)
		}
		m.mu.Lock()
		stillRunning := m.running
		var oldAdv *mdnsAdvertiser
		if stillRunning {
			m.listeners = kept
			m.listenAddrs = addrs
			oldAdv = m.mdns
			m.mdns = nil
		}
		m.mu.Unlock()
		if !stillRunning {
			// Manager stopped concurrently while reconcileInterfaces opened new listeners:
			// close them immediately so no sockets or goroutines leak (NFR-MOB-RES-001).
			for _, al := range newlyOpened {
				_ = al.ln.Close()
				_ = al.srv.Close()
			}
			return
		}
		if oldAdv != nil {
			oldAdv.Stop()
		}
		if len(m.listenOverrides) == 0 || m.mdnsFactory != nil {
			cfg := newMDNSRecordConfig(m.store.NodeID(), m.nodeName, m.channel, m.version, port)
			newAdv := startMDNSAdvertiser(cfg, candidates, m.mdnsFactory, m.logf)
			m.mu.Lock()
			if m.running {
				m.mdns = newAdv
			} else if newAdv != nil {
				m.mu.Unlock()
				newAdv.Stop()
				return
			}
			m.mu.Unlock()
		}
		m.logf("companion interfaces updated: %s", strings.Join(addrs, ", "))
		m.notifyChange()
	}
}

func (m *Manager) advertisedAddrs() []string {
	if len(m.advertiseOverrides) > 0 {
		return append([]string(nil), m.advertiseOverrides...)
	}
	m.mu.Lock()
	raw := append([]string(nil), m.listenAddrs...)
	m.mu.Unlock()
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		if ip, err := netip.ParseAddr(a); err == nil && prefixLL6.Contains(ip.Unmap()) {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return raw
	}
	return out
}

// StartPairing starts a fresh 5-minute one-time QR + short-code pairing session.
func (m *Manager) StartPairing() (PairingSessionInfo, error) {
	m.mu.Lock()
	running := m.running
	port := m.port
	id := m.identity
	m.mu.Unlock()
	if !running {
		return PairingSessionInfo{}, errors.New("请先打开“允许手机连接”")
	}
	// Allow QR generation even when MaxDevices (5) are already paired so paired
	// phones can rescan to update LAN addresses (FR-MOB-007, design §4.7) and a
	// 6th new device receives 409 device_limit on POST /api/v1/pair (AC-91).
	addrs := m.advertisedAddrs()
	if len(addrs) == 0 {
		return PairingSessionInfo{}, errors.New("当前没有可连接的局域网地址")
	}
	return m.pairing.Start(m.store.NodeID(), m.nodeName, m.channel, id.Fingerprint, id.IdentityCode, port, addrs)
}

// CancelPairing cancels the active QR/short-code session.
func (m *Manager) CancelPairing() {
	m.pairing.CancelSession()
}

// ApprovePairing confirms a pending phone pairing request on the desktop.
func (m *Manager) ApprovePairing(pairID string) (DeviceSummary, error) {
	dev, err := m.pairing.Approve(pairID, m.store)
	if err != nil {
		return DeviceSummary{}, err
	}
	m.notifyChange()
	return DeviceSummary{
		ID:         dev.ID,
		Name:       dev.Name,
		Model:      dev.Model,
		Platform:   dev.Platform,
		Channel:    dev.Channel,
		PairedAt:   dev.PairedAt,
		LastSeenAt: dev.LastSeenAt,
		LastAddr:   dev.LastAddr,
	}, nil
}

// RejectPairing denies a pending phone pairing request.
func (m *Manager) RejectPairing(pairID string) error {
	return m.pairing.Reject(pairID)
}

// RenameDevice changes a paired device's display name.
func (m *Manager) RenameDevice(deviceID, newName string) error {
	if _, err := m.store.RenameDevice(deviceID, newName); err != nil {
		return err
	}
	m.notifyChange()
	return nil
}

// RevokeDevice removes a paired device, sends bye{reason: "revoked"}, and closes its connections immediately.
func (m *Manager) RevokeDevice(deviceID string) error {
	if _, err := m.store.RevokeDevice(deviceID); err != nil {
		return err
	}
	m.hub.RevokeDevice(deviceID, time.Second)
	m.logf("companion device revoked: device=%s", deviceID)
	m.notifyChange()
	return nil
}

// ResetIdentity regenerates the TLS certificate and invalidates all paired phones (design §3, ui.md §3.12).
func (m *Manager) ResetIdentity() error {
	m.mu.Lock()
	wasEnabled := m.enabled
	port := m.port
	m.mu.Unlock()

	m.stop(companionapi.ByeReasonRevoked)
	if err := m.store.ClearDevices(); err != nil {
		return err
	}
	id, err := RegenerateTLSIdentity(m.dataDir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.identity = id
	m.mu.Unlock()
	m.logf("companion identity reset; all devices cleared")
	if wasEnabled {
		return m.ApplySettings(true, port)
	}
	m.notifyChange()
	return nil
}

// InvalidateItemThumbs drops cached mobile thumbnails for a deleted library item (design §3).
func (m *Manager) InvalidateItemThumbs(itemKey string) {
	if m.thumbs != nil {
		m.thumbs.InvalidateItem(itemKey)
	}
}

// Status returns a complete, sanitized snapshot for the desktop "设置 › 移动端" UI.
func (m *Manager) Status() StatusSnapshot {
	m.sleepMu.Lock()
	sleepInhibited := m.sleepActive
	m.sleepMu.Unlock()

	m.mu.Lock()
	adv := m.mdns
	snap := StatusSnapshot{
		Supported:      m.supported,
		Enabled:        m.enabled,
		Running:        m.running,
		PreventSleep:   m.preventSleep,
		SleepInhibited: sleepInhibited,
		Port:           m.port,
		DefaultPort:    companionapi.DefaultPortForChannel(m.channel),
		Channel:        m.channel,
		NodeID:         m.store.NodeID(),
		NodeName:       m.nodeName,
		Fingerprint:    m.identity.Fingerprint,
		IdentityCode:   m.identity.IdentityCode,
		ListenAddrs:    append([]string(nil), m.listenAddrs...),
		LastError:      m.lastError,
	}
	m.mu.Unlock()
	snap.MDNSActive = adv.Active()

	if m.backend != nil {
		snap.CloseAction = m.backend.GetSettings().Window.CloseAction
	}

	online := m.hub.OnlineDeviceIDs()
	devs := m.store.Devices()
	snap.Devices = make([]DeviceSummary, 0, len(devs))
	for _, d := range devs {
		snap.Devices = append(snap.Devices, DeviceSummary{
			ID:         d.ID,
			Name:       d.Name,
			Model:      d.Model,
			Platform:   d.Platform,
			Channel:    d.Channel,
			PairedAt:   d.PairedAt,
			LastSeenAt: d.LastSeenAt,
			LastAddr:   d.LastAddr,
			Online:     online[d.ID],
		})
	}
	snap.ActivePairing = m.pairing.ActiveSession()
	snap.PendingPairs = m.pairing.PendingConfirmations()
	return snap
}

func (m *Manager) handleBackendEvent(ev service.Event) {
	var frame companionapi.EventFrame
	frame.Site = ev.Site
	frame.Timestamp = ev.Timestamp.UTC().Format(time.RFC3339)

	switch ev.Type {
	case service.EventTaskUpdated:
		frame.Type = companionapi.EventTaskChanged
	case service.EventCacheQueueUpdated:
		frame.Type = companionapi.EventCacheQueueUpdated
	case service.EventLibraryChanged:
		frame.Type = companionapi.EventFeedChanged
		// If a deleted item key was carried in Message (e.g. "deleted:<key>"), invalidate its thumbnails.
		if key, ok := strings.CutPrefix(ev.Message, "deleted:"); ok {
			m.InvalidateItemThumbs(strings.TrimSpace(key))
		} else if strings.HasPrefix(ev.Message, "deleted_batch:") && m.thumbs != nil {
			m.thumbs.ClearAll()
		}
	case service.EventSubscriptionChecked:
		if strings.HasPrefix(ev.Message, "progress:") || strings.Contains(ev.Message, "check") {
			frame.Type = companionapi.EventSubsCheck
			frame.Message = sanitizeEventMessage(ev.Message)
		} else {
			frame.Type = companionapi.EventSubsChanged
		}
	case service.EventLiveUpdated:
		frame.Type = companionapi.EventLiveChanged
	case service.EventAccountChanged:
		frame.Type = companionapi.EventAuthChanged
		if ev.Site != "" && m.backend != nil {
			frame.State = m.backend.StatusOf(ev.Site).State
		}
	case service.EventCacheConflictsChanged:
		frame.Type = companionapi.EventConflictsChanged
	case service.EventTransferUpdated:
		frame.Type = companionapi.EventTransfersChanged
	default:
		return
	}
	m.hub.Publish(frame)
}

func (m *Manager) notifyChange() {
	if m.onChange != nil {
		m.onChange()
	}
}

func sanitizeEventMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)
	// Strip anything resembling a URL, query string, or filesystem path (NFR-MOB-SEC-003).
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") ||
		strings.Contains(msg, ":\\") || strings.Contains(msg, "?") {
		return ""
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func formatBindError(port int, err error) string {
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "address already in use") || strings.Contains(lower, "only one usage of each socket address") {
		return fmt.Sprintf("端口 %d 已被其他程序占用，请更换端口后重试", port)
	}
	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "access is denied") {
		return fmt.Sprintf("端口 %d 无法监听（权限不足或被防火墙阻止），请更换端口或允许 Windows 防火墙的“专用网络”访问", port)
	}
	return fmt.Sprintf("监听端口 %d 失败：%v", port, err)
}

type companionLogWriter struct {
	logf func(format string, args ...any)
}

func (w companionLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		w.logf("companion https: %s", msg)
	}
	return len(p), nil
}
