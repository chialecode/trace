package site

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
)

// Proxy modes of Settings → 网络 (DEC-046 ⑤).
const (
	ProxySystem = "system" // the system's proxy settings and HTTP(S)_PROXY (default)
	ProxyNone   = "none"   // connect directly
	ProxyCustom = "custom" // one http:// or socks5:// proxy the user typed
)

type proxyConfig struct {
	mode   string
	custom *url.URL
}

type systemProxyHolder struct {
	fn func(*http.Request) (*url.URL, error)
}

var (
	currentProxy atomic.Pointer[proxyConfig]
	// systemProxy answers "follow the system". It reads only the HTTP(S)_PROXY
	// variables until the desktop shell installs the system reader
	// (SetSystemProxyFunc: MyGo's netproxy, which on Windows reads WinHTTP's
	// view of the user's proxy, manual, PAC or automatic detection, and lets
	// those variables decide when set).
	systemProxy atomic.Pointer[systemProxyHolder]
	proxyGen    atomic.Uint64
)

// SetSystemProxyFunc installs how "follow the system" finds the proxy; the
// shell calls it once at startup, before any request.
func SetSystemProxyFunc(fn func(*http.Request) (*url.URL, error)) {
	if fn != nil {
		systemProxy.Store(&systemProxyHolder{fn: fn})
		proxyGen.Add(1)
	}
}

func init() {
	currentProxy.Store(&proxyConfig{mode: ProxySystem})
	systemProxy.Store(&systemProxyHolder{fn: http.ProxyFromEnvironment})
}

// ParseProxyURL checks a custom proxy address: http://, https://, socks5://
// or socks5h://, with a host and port and nothing after them. An account and
// password are refused: settings.json is plain text, and credentials belong
// in the credential store only (NFR-SEC-001).
func ParseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("代理地址不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("代理地址格式不对，例如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("代理只支持 http://、https:// 与 socks5:// 地址")
	}
	if u.User != nil {
		return nil, errors.New("代理地址不能带账号密码；请改用不需要登录的本机代理地址")
	}
	if u.Hostname() == "" || u.Port() == "" {
		return nil, errors.New("代理地址要写主机和端口，例如 127.0.0.1:7890")
	}
	portNum, portErr := strconv.Atoi(u.Port())
	if portErr != nil || portNum < 1 || portNum > 65535 {
		return nil, errors.New("代理端口要在 1 到 65535 之间")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("代理地址只写协议、主机和端口")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Path = ""
	return u, nil
}

// SetProxy switches every site request (API, media, live recording) to the
// given mode at once; requests already under way keep their connections.
func SetProxy(mode, custom string) error {
	cfg := &proxyConfig{mode: ProxySystem}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ProxySystem:
	case ProxyNone:
		cfg.mode = ProxyNone
	case ProxyCustom:
		u, err := ParseProxyURL(custom)
		if err != nil {
			return err
		}
		cfg.mode, cfg.custom = ProxyCustom, u
	default:
		return errors.New("未知的代理方式：" + mode)
	}
	currentProxy.Store(cfg)
	proxyGen.Add(1)
	return nil
}

// ProxyMode reports the mode in use.
func ProxyMode() string { return currentProxy.Load().mode }

// Proxy is the http.Transport Proxy hook all site transports share. Loopback
// targets are always reached directly.
func Proxy(req *http.Request) (*url.URL, error) {
	if IsLoopbackHost(req.URL.Hostname()) {
		return nil, nil
	}
	cfg := currentProxy.Load()
	switch cfg.mode {
	case ProxyNone:
		return nil, nil
	case ProxyCustom:
		u := *cfg.custom
		return &u, nil
	}
	if h := systemProxy.Load(); h != nil && h.fn != nil {
		return h.fn(req)
	}
	return http.ProxyFromEnvironment(req)
}

// IsLoopbackHost reports whether host refers to localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
