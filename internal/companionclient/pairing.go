package companionclient

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

// PairingRecord is persisted in phone preferences (no device token; token lives in mygo.Secrets).
type PairingRecord struct {
	Node     string    `json:"node"`
	Name     string    `json:"name"`
	Channel  string    `json:"channel"`
	FP       string    `json:"fp"`
	Addrs    []string  `json:"addrs"`
	LastAddr string    `json:"lastAddr,omitempty"`
	PairedAt time.Time `json:"pairedAt"`
}

// ParseQRCode decodes and validates a "trace-pair:<base64url JSON>" string using companionapi.
func ParseQRCode(raw string) (companionapi.PairPayload, error) {
	p, err := companionapi.DecodePairPayload(raw)
	if err != nil {
		return companionapi.PairPayload{}, err
	}
	addrs := make([]string, 0, len(p.Addrs))
	seen := make(map[string]bool, len(p.Addrs))
	for _, rawAddr := range p.Addrs {
		norm, err := NormalizeEndpoint(rawAddr, p.Port)
		if err != nil {
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			addrs = append(addrs, norm)
		}
	}
	if len(addrs) == 0 {
		return companionapi.PairPayload{}, companionapi.ErrInvalidPairPayload
	}
	p.Addrs = addrs
	return p, nil
}

// EncodeQRCode encodes a PairPayload as "trace-pair:<base64url JSON>".
func EncodeQRCode(p companionapi.PairPayload) (string, error) {
	return companionapi.EncodePairPayload(p)
}

// NormalizeChannel normalizes channel names ("stable", "dev") or returns "".
func NormalizeChannel(ch string) string {
	norm, ok := companionapi.NormalizeChannel(ch)
	if !ok {
		return ""
	}
	return norm
}

// DefaultPortForChannel returns 47382 for dev and 47381 for stable.
func DefaultPortForChannel(channel string) int {
	return companionapi.DefaultPortForChannel(channel)
}

// NormalizeEndpoint parses "ip", "ip:port", "[ipv6]:port", or "https://ip[:port]"
// into canonical "host:port", filling defaultPort when omitted.
func NormalizeEndpoint(raw string, defaultPort int) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("请填写电脑地址")
	}
	if !strings.Contains(s, "://") {
		if ip := net.ParseIP(s); ip != nil && ip.To4() == nil {
			s = "[" + s + "]"
		}
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("电脑地址格式不正确")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", errors.New("电脑地址格式不正确")
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return "", errors.New("电脑地址格式不正确")
	}
	if port == "" {
		if defaultPort <= 0 || defaultPort > 65535 {
			defaultPort = companionapi.DefaultStablePort
		}
		port = strconv.Itoa(defaultPort)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", errors.New("端口号必须在 1～65535 之间")
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

// NormalizeShortCode validates and normalizes an 8-character manual pairing code
// into formatted "XXXX-XXXX" (using companionapi's unambiguous alphabet).
func NormalizeShortCode(raw string) (string, error) {
	norm, ok := companionapi.NormalizeShortCode(raw)
	if !ok {
		return "", errors.New("请输入正确的 8 位配对码")
	}
	return companionapi.FormatShortCode(norm), nil
}

// OrderedAddrs returns candidate addresses with lastAddr prioritized first.
func (r PairingRecord) OrderedAddrs() []string {
	out := make([]string, 0, len(r.Addrs)+1)
	seen := make(map[string]bool, len(r.Addrs)+1)
	if r.LastAddr != "" {
		out = append(out, r.LastAddr)
		seen[r.LastAddr] = true
	}
	for _, a := range r.Addrs {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// SameIdentity reports whether a scanned QR code belongs to the same paired
// computer node and pinned certificate fingerprint (design §4.7).
func (r PairingRecord) SameIdentity(qr companionapi.PairPayload) bool {
	if r.Node == "" || r.FP == "" {
		return false
	}
	fp, ok := companionapi.NormalizeFingerprint(qr.Fingerprint)
	if !ok {
		return false
	}
	return r.Node == qr.Node && strings.EqualFold(r.FP, fp)
}
