package companionapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Pairing payload format constants (docs/design/mobile.md §4).
const (
	PairPayloadPrefix  = "trace-pair:"
	PairPayloadVersion = 1

	// ShortCodeAlphabet omits visually ambiguous characters (0, O, 1, I).
	ShortCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	ShortCodeLength   = 8
)

// Pairing status values returned by POST /api/v1/pair and GET /api/v1/pair/{pairId}.
const (
	PairStatusPending  = "pending"
	PairStatusApproved = "approved"
	PairStatusRejected = "rejected"
	PairStatusExpired  = "expired"
	PairStatusUpdated  = "updated"
)

// ErrInvalidPairPayload reports malformed or unsupported QR pairing payloads.
var ErrInvalidPairPayload = errors.New("配对二维码格式不正确")

// PairPayload is the JSON encoded inside the QR code (`trace-pair:<base64url>`).
// Field names follow docs/design/mobile.md §4.1 verbatim.
type PairPayload struct {
	Version     int      `json:"v"`
	Node        string   `json:"node"`
	Name        string   `json:"name"`
	Channel     string   `json:"channel"`
	Port        int      `json:"port"`
	Addrs       []string `json:"addrs"`
	Fingerprint string   `json:"fp"`
	Code        string   `json:"code"`
}

// Normalize validates and canonicalizes a PairPayload.
func (p PairPayload) Normalize() (PairPayload, error) {
	if p.Version == 0 {
		p.Version = PairPayloadVersion
	}
	if p.Version != PairPayloadVersion {
		return PairPayload{}, fmt.Errorf("%w：不支持的配对格式版本 %d", ErrInvalidPairPayload, p.Version)
	}
	p.Node = strings.TrimSpace(p.Node)
	if p.Node == "" {
		return PairPayload{}, fmt.Errorf("%w：缺少节点标识", ErrInvalidPairPayload)
	}
	p.Name = strings.TrimSpace(p.Name)
	ch, ok := NormalizeChannel(p.Channel)
	if !ok {
		return PairPayload{}, fmt.Errorf("%w：不支持的通道 %q", ErrInvalidPairPayload, p.Channel)
	}
	p.Channel = ch
	if p.Port < 1 || p.Port > 65535 {
		return PairPayload{}, fmt.Errorf("%w：端口无效 %d", ErrInvalidPairPayload, p.Port)
	}
	cleanAddrs := make([]string, 0, len(p.Addrs))
	seen := make(map[string]bool, len(p.Addrs))
	for _, raw := range p.Addrs {
		addr := strings.TrimSpace(raw)
		if addr == "" {
			continue
		}
		if ip := net.ParseIP(strings.Trim(addr, "[]")); ip != nil {
			addr = ip.String()
		}
		if !seen[addr] {
			seen[addr] = true
			cleanAddrs = append(cleanAddrs, addr)
		}
	}
	if len(cleanAddrs) == 0 {
		return PairPayload{}, fmt.Errorf("%w：缺少监听地址", ErrInvalidPairPayload)
	}
	p.Addrs = cleanAddrs

	fp, ok := NormalizeFingerprint(p.Fingerprint)
	if !ok {
		return PairPayload{}, fmt.Errorf("%w：证书指纹无效", ErrInvalidPairPayload)
	}
	p.Fingerprint = fp

	p.Code = strings.TrimSpace(p.Code)
	if p.Code == "" || strings.ContainsAny(p.Code, " \t\r\n") {
		return PairPayload{}, fmt.Errorf("%w：缺少配对码", ErrInvalidPairPayload)
	}
	return p, nil
}

// EncodePairPayload validates and encodes p into `trace-pair:<base64url(json)>`.
func EncodePairPayload(p PairPayload) (string, error) {
	norm, err := p.Normalize()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(norm)
	if err != nil {
		return "", err
	}
	return PairPayloadPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodePairPayload decodes and validates a `trace-pair:<base64url(json)>` QR string.
func DecodePairPayload(raw string) (PairPayload, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, PairPayloadPrefix) {
		return PairPayload{}, ErrInvalidPairPayload
	}
	encoded := strings.TrimRight(strings.TrimPrefix(trimmed, PairPayloadPrefix), "=")
	if encoded == "" {
		return PairPayload{}, ErrInvalidPairPayload
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return PairPayload{}, ErrInvalidPairPayload
	}
	var p PairPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return PairPayload{}, ErrInvalidPairPayload
	}
	return p.Normalize()
}

// Fingerprint computes the lowercase 64-hex SHA-256 of a leaf certificate DER.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint accepts a hex SHA-256 fingerprint with optional colons,
// dashes, or spaces in any case, returning the 64-character lowercase hex form.
func NormalizeFingerprint(raw string) (string, bool) {
	r := strings.NewReplacer(":", "", " ", "", "-", "")
	fp := strings.ToLower(r.Replace(strings.TrimSpace(raw)))
	if len(fp) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(fp); err != nil {
		return "", false
	}
	return fp, true
}

// IdentityCode formats the first 64 bits (16 hex characters) of a certificate
// SHA-256 fingerprint as four uppercase 4-character groups: `XXXX-XXXX-XXXX-XXXX`
// (docs/design/mobile.md §4.2).
func IdentityCode(fp string) (string, error) {
	norm, ok := NormalizeFingerprint(fp)
	if !ok {
		return "", fmt.Errorf("证书指纹不正确，应是 64 位十六进制")
	}
	head := strings.ToUpper(norm[:16])
	return head[0:4] + "-" + head[4:8] + "-" + head[8:12] + "-" + head[12:16], nil
}

// NormalizeShortCode validates an 8-character manual pairing short code (with
// optional dashes or spaces, case-insensitive) and returns the canonical
// 8-character uppercase form without separators.
func NormalizeShortCode(raw string) (string, bool) {
	r := strings.NewReplacer("-", "", " ", "", "\t", "")
	s := strings.ToUpper(r.Replace(strings.TrimSpace(raw)))
	if len(s) != ShortCodeLength {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(ShortCodeAlphabet, s[i]) < 0 {
			return "", false
		}
	}
	return s, true
}

// FormatShortCode formats an 8-character short code as `XXXX-XXXX`.
// If raw is not a valid short code, it returns the trimmed input unchanged.
func FormatShortCode(raw string) string {
	norm, ok := NormalizeShortCode(raw)
	if !ok {
		return strings.TrimSpace(raw)
	}
	return norm[:4] + "-" + norm[4:]
}

// PairRequest is POST /api/v1/pair (docs/design/mobile.md §4.3).
// When an already-paired device re-scans a QR code for the same node and
// certificate fingerprint (design §4.7), it sends the same request with its
// existing `Authorization: Bearer <token>` header; the server verifies the
// token, consumes the one-time pairing code, and responds with status="updated"
// and fresh addresses without prompting desktop confirmation.
type PairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
	Model      string `json:"model,omitempty"`
	Platform   string `json:"platform"`
	Channel    string `json:"channel"`
	AppVersion string `json:"appVersion,omitempty"`
	PairSecret string `json:"pairSecret"`
}

// PairStartResponse is returned by POST /api/v1/pair.
type PairStartResponse struct {
	PairID    string   `json:"pairId,omitempty"`
	Status    string   `json:"status"` // "pending" or "updated"
	ExpiresAt string   `json:"expiresAt,omitempty"`
	Node      string   `json:"node"`
	Name      string   `json:"name"`
	Channel   string   `json:"channel"`
	Addrs     []string `json:"addrs,omitempty"`
}

// PairPollResponse is returned by GET /api/v1/pair/{pairId} (docs/design/mobile.md §4.4).
// Token is populated once when Status == "approved" and deleted from memory immediately after.
type PairPollResponse struct {
	Status        string   `json:"status"` // "pending", "approved", "rejected", "expired"
	DeviceID      string   `json:"deviceId,omitempty"`
	Token         string   `json:"token,omitempty"`
	Node          string   `json:"node,omitempty"`
	Name          string   `json:"name,omitempty"`
	Channel       string   `json:"channel,omitempty"`
	Addrs         []string `json:"addrs,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	Protocol      string   `json:"protocol,omitempty"`
}
