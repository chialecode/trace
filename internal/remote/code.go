package remote

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// CodePrefix starts every connection code.
const CodePrefix = "trace-remote:1:"

// Connection is what the desktop needs to reach one remote: its address,
// access token and certificate fingerprint, and a name to show.
type Connection struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name,omitempty"`
}

// EncodeCode turns a connection into the code trace-server init prints.
func EncodeCode(c Connection) string {
	raw, _ := json.Marshal(c)
	return CodePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// ErrBadCode is returned for a code that is not a TRACE connection code.
var ErrBadCode = errors.New("连接码不正确，请从远程的 trace-server init 或 token 输出里完整复制")

// DecodeCode parses a connection code and checks its three parts.
func DecodeCode(code string) (Connection, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, CodePrefix) {
		return Connection{}, ErrBadCode
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(code, CodePrefix), "="))
	if err != nil {
		return Connection{}, ErrBadCode
	}
	var c Connection
	if err := json.Unmarshal(raw, &c); err != nil {
		return Connection{}, ErrBadCode
	}
	return c.Normalize()
}

// Normalize checks a connection's address, token and fingerprint and
// returns them in canonical form.
func (c Connection) Normalize() (Connection, error) {
	u, err := NormalizeURL(c.URL)
	if err != nil {
		return Connection{}, err
	}
	c.URL = u
	c.Token = strings.TrimSpace(c.Token)
	if len(c.Token) < 32 || strings.ContainsAny(c.Token, " \t\r\n") {
		return Connection{}, fmt.Errorf("访问令牌不正确")
	}
	fp, ok := NormalizeFingerprint(c.Fingerprint)
	if !ok {
		return Connection{}, fmt.Errorf("证书指纹不正确，应是 64 位十六进制")
	}
	c.Fingerprint = fp
	c.Name = strings.TrimSpace(c.Name)
	return c, nil
}

// NormalizeURL accepts "host", "host:port" or "https://host[:port]" and
// returns "https://host:port" (default port 47380).
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("请填写远程地址")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("远程地址不正确")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("远程地址必须是 https")
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return "", fmt.Errorf("远程地址不正确")
	}
	if port == "" {
		port = strconv.Itoa(DefaultPort)
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("远程端口不正确")
	}
	return "https://" + net.JoinHostPort(host, port), nil
}
