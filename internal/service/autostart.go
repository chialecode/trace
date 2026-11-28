package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site"
)

// AutostartBackgroundFlag is the launch argument passed to the Windows Run
// entry so TRACE starts minimized in the system tray (FR-RUN-002).
const AutostartBackgroundFlag = "--background"

// AutostartRegistry abstracts per-user startup registration
// (HKCU\Software\Microsoft\Windows\CurrentVersion\Run) so automated tests and
// quiet runs never read or modify the real Windows registry.
type AutostartRegistry interface {
	Get(name string) (command string, exists bool, err error)
	Set(name, command string) error
	Delete(name string) error
}

// AutostartStatus reports whether per-user Windows login autostart is
// supported for the current process and its effective state (FR-RUN-002).
type AutostartStatus struct {
	Supported   bool   `json:"supported"`
	Enabled     bool   `json:"enabled"`
	Registered  bool   `json:"registered"`
	Channel     string `json:"channel"`
	DisplayName string `json:"displayName"`
	ValueName   string `json:"valueName,omitempty"`
	Command     string `json:"command,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// MemoryAutostartRegistry is a thread-safe in-memory AutostartRegistry used in
// tests and quiet runs so the host's real Windows registry is never touched.
type MemoryAutostartRegistry struct {
	mu      sync.Mutex
	entries map[string]string
	GetErr  error
	SetErr  error
	DelErr  error
}

// NewMemoryAutostartRegistry creates an empty in-memory AutostartRegistry.
func NewMemoryAutostartRegistry() *MemoryAutostartRegistry {
	return &MemoryAutostartRegistry{
		entries: make(map[string]string),
	}
}

func (m *MemoryAutostartRegistry) Get(name string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetErr != nil {
		return "", false, m.GetErr
	}
	v, ok := m.entries[name]
	return v, ok, nil
}

func (m *MemoryAutostartRegistry) Set(name, command string) error {
	if err := validateAutostartRegInput(name, command); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SetErr != nil {
		return m.SetErr
	}
	if m.entries == nil {
		m.entries = make(map[string]string)
	}
	m.entries[name] = command
	return nil
}

func (m *MemoryAutostartRegistry) Delete(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("autostart: empty registry value name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DelErr != nil {
		return m.DelErr
	}
	delete(m.entries, name)
	return nil
}

// Snapshot returns a copy of all recorded entries for test assertions.
func (m *MemoryAutostartRegistry) Snapshot() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.entries))
	for k, v := range m.entries {
		out[k] = v
	}
	return out
}

func validateAutostartRegInput(name, command string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("autostart: invalid registry value name")
	}
	if strings.TrimSpace(command) == "" || strings.ContainsAny(command, "\x00\r\n") {
		return errors.New("autostart: invalid startup command")
	}
	return nil
}

func isWindowsDriveAbs(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	alpha := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	return alpha && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// BuildAutostartCommand formats a safe Windows Run command line for exePath:
// `"<cleanExePath>" --background`.
func BuildAutostartCommand(exePath, targetGOOS string) (string, error) {
	raw := strings.TrimSpace(exePath)
	if raw == "" {
		return "", errors.New("无法定位当前程序路径")
	}
	if strings.ContainsAny(raw, "\"\x00\r\n") {
		return "", errors.New("程序路径包含不支持的字符")
	}
	clean := raw
	if targetGOOS == "windows" {
		if runtime.GOOS == "windows" {
			clean = filepath.Clean(raw)
		} else {
			clean = strings.ReplaceAll(raw, "/", `\`)
		}
		if !isWindowsDriveAbs(clean) {
			return "", errors.New("开机启动项需要本地绝对程序路径")
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".test.exe") {
			return "", errors.New("测试二进制不支持写入 Windows 开机启动项")
		}
	} else {
		clean = filepath.Clean(raw)
		if !filepath.IsAbs(clean) {
			return "", errors.New("开机启动项需要本地绝对程序路径")
		}
	}
	return fmt.Sprintf(`"%s" %s`, clean, AutostartBackgroundFlag), nil
}

type autostartEval struct {
	Supported   bool
	Channel     string
	DisplayName string
	ValueName   string
	Command     string
	Reason      string
}

func (b *Backend) evaluateAutostartConfig() autostartEval {
	ch := appdirs.EffectiveChannel(b.loc.Channel)
	displayName := firstNonEmpty(b.loc.DisplayName, appdirs.DisplayNameForChannel(ch))
	valName := appdirs.AutostartValueNameForChannel(ch)
	targetGOOS := strings.TrimSpace(b.autostartGOOS)
	if targetGOOS == "" {
		targetGOOS = runtime.GOOS
	}
	out := autostartEval{
		Channel:     ch,
		DisplayName: displayName,
		ValueName:   valName,
	}
	if b.profile == ProfileRemote {
		out.Reason = "远程无头服务不支持桌面开机自动启动"
		return out
	}
	if targetGOOS != "windows" {
		out.Reason = "开机自动启动仅支持 Windows 桌面版"
		return out
	}
	if !strings.EqualFold(strings.TrimSpace(b.loc.Mode), "installed") {
		out.Reason = "仅支持在 Windows 安装版中启用开机自动启动（当前为便携/非安装运行模式）"
		return out
	}
	ver := strings.TrimSpace(b.autostartVersion)
	if ver == "" {
		ver = appdirs.BuildInfo().Version
	}
	if ver == "" || ver == appdirs.UnversionedBuild {
		out.Reason = "源码运行（go run / mygo dev）不支持写入 Windows 开机启动项，请使用已安装的 " + displayName
		return out
	}
	exePath := strings.TrimSpace(b.autostartExePath)
	if exePath == "" {
		if p, err := os.Executable(); err == nil {
			exePath = p
		}
	}
	cmd, err := BuildAutostartCommand(exePath, targetGOOS)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	if b.autostartReg == nil {
		out.Reason = "当前运行模式未连接 Windows 启动项接口"
		return out
	}
	out.Supported = true
	out.Command = cmd
	return out
}

// AutostartStatus returns the current Windows login autostart capability and
// registration status for this channel without writing to the registry.
func (b *Backend) AutostartStatus() AutostartStatus {
	eval := b.evaluateAutostartConfig()
	enabled := false
	if b.settings != nil {
		enabled = b.settings.Get().Window.AutoStart
	}
	st := AutostartStatus{
		Supported:   eval.Supported,
		Enabled:     enabled,
		Channel:     eval.Channel,
		DisplayName: eval.DisplayName,
		ValueName:   eval.ValueName,
		Command:     eval.Command,
		Reason:      eval.Reason,
	}
	if eval.Supported && b.autostartReg != nil {
		if curCmd, exists, err := b.autostartReg.Get(eval.ValueName); err == nil && exists && strings.TrimSpace(curCmd) != "" {
			st.Registered = true
		}
	}
	return st
}

// applyAutostartToggle applies an AutoStart preference change to this channel's
// Windows Run entry and returns an optional rollback callback if persisting
// settings.json subsequently fails. Must be called with b.settingsMu held.
func (b *Backend) applyAutostartToggle(enable bool) (func(), error) {
	eval := b.evaluateAutostartConfig()
	if enable {
		if !eval.Supported {
			return nil, &site.Error{
				Kind:    site.ErrKindBadRequest,
				Message: firstNonEmpty(eval.Reason, "当前环境不支持开启开机自动启动"),
			}
		}
		prevCmd, prevExists, _ := b.autostartReg.Get(eval.ValueName)
		if err := b.autostartReg.Set(eval.ValueName, eval.Command); err != nil {
			b.logError("autostart.set", err)
			return nil, &site.Error{
				Kind:    site.ErrKindStorage,
				Message: "写入 Windows 开机启动项失败：" + err.Error(),
			}
		}
		b.logInfo(fmt.Sprintf("autostart enabled: channel=%s value=%s", eval.Channel, eval.ValueName))
		return func() {
			if prevExists {
				_ = b.autostartReg.Set(eval.ValueName, prevCmd)
			} else {
				_ = b.autostartReg.Delete(eval.ValueName)
			}
		}, nil
	}

	// Disabling: only touch the registry when this process is a supported
	// installed Windows build for its channel. In unsupported environments
	// (non-Windows, portable, source 0.0.0-dev, remote), still allow turning
	// the saved preference off so settings never get stuck on true, without
	// ever touching an installed channel's Windows Run entry.
	if !eval.Supported || b.autostartReg == nil || eval.ValueName == "" {
		return nil, nil
	}
	prevCmd, prevExists, getErr := b.autostartReg.Get(eval.ValueName)
	if getErr == nil && !prevExists {
		return nil, nil
	}
	if err := b.autostartReg.Delete(eval.ValueName); err != nil {
		b.logError("autostart.delete", err)
		return nil, &site.Error{
			Kind:    site.ErrKindStorage,
			Message: "移除 Windows 开机启动项失败：" + err.Error(),
		}
	}
	b.logInfo(fmt.Sprintf("autostart disabled: channel=%s value=%s", eval.Channel, eval.ValueName))
	if getErr != nil || !prevExists {
		return nil, nil
	}
	return func() {
		_ = b.autostartReg.Set(eval.ValueName, prevCmd)
	}, nil
}

// ReconcileAutostart ensures that when settings.Window.AutoStart is enabled on
// a supported installed Windows build, this channel's Run registry entry points
// to the current executable with --background (e.g. after relocating or
// reinstalling the app). Never touches other channels and never writes when
// AutoStart is disabled.
func (b *Backend) ReconcileAutostart() (bool, error) {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	if b.settings == nil || !b.settings.Get().Window.AutoStart {
		return false, nil
	}
	eval := b.evaluateAutostartConfig()
	if !eval.Supported {
		return false, nil
	}
	existing, exists, err := b.autostartReg.Get(eval.ValueName)
	if err != nil {
		return false, err
	}
	if exists && strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(eval.Command)) {
		return false, nil
	}
	if err := b.autostartReg.Set(eval.ValueName, eval.Command); err != nil {
		return false, err
	}
	b.logInfo(fmt.Sprintf("autostart synced: channel=%s value=%s", eval.Channel, eval.ValueName))
	return true, nil
}
