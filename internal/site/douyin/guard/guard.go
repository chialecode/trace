// Package guard implements a pure Go standard-library Douyin Web bd-ticket-guard (v2)
// ECDSA P-256 keypair, handshake header, server-data parser, and request-signing stack.
//
// It is pure computation with zero net/http, internal/credential, or upper-layer dependencies
// (mirroring internal/site/douyin/sign). Protocol specification and synthetic vectors are
// documented in docs/research/douyin.md.
package guard

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Outbound and inbound bd-ticket-guard HTTP header names and protocol defaults.
const (
	HeaderVersion          = "bd-ticket-guard-version"
	HeaderIterationVersion = "bd-ticket-guard-iteration-version"
	HeaderWebVersion       = "bd-ticket-guard-web-version"
	HeaderWebSignType      = "bd-ticket-guard-web-sign-type"
	HeaderREEPublicKey     = "bd-ticket-guard-ree-public-key"
	HeaderClientData       = "bd-ticket-guard-client-data"

	HeaderResult       = "bd-ticket-guard-result"
	HeaderServerDataBD = "bd-ticket-guard-server-data"
	HeaderServerDataTT = "tt-ticket-guard-server-data"
	HeaderClientCertBD = "bd-ticket-guard-client-cert"
	HeaderClientCertTT = "tt-ticket-guard-client-cert"
	HeaderTicketBD     = "bd-ticket-guard-ticket"
	HeaderTicketTT     = "tt-ticket-guard-ticket"

	DefaultVersion          = "2"
	DefaultIterationVersion = "1"
	DefaultWebVersion       = "2"
	DefaultWebSignType      = "1"
	DefaultReqContent       = "ticket,path,timestamp"

	sec1UncompressedP256Len = 65
)

var (
	// ErrInvalidPrivateKey indicates the serialized ECDSA P-256 private key is missing or corrupt.
	ErrInvalidPrivateKey = errors.New("douyin/guard: invalid ECDSA P-256 private key")
	// ErrInvalidPublicKey indicates the serialized SEC1 P-256 public key is missing or invalid.
	ErrInvalidPublicKey = errors.New("douyin/guard: invalid SEC1 P-256 public key")
	// ErrInvalidPath indicates the request path for ticket-guard signing is empty.
	ErrInvalidPath = errors.New("douyin/guard: request path is required")
)

// guardedPaths are the Douyin Web endpoints confirmed to emit bd-ticket-guard-result
// and participate in ticket-guard handshake / verification.
var guardedPaths = map[string]struct{}{
	"/aweme/v1/web/user/profile/self/": {},
	"/aweme/v1/web/aweme/favorite/":    {},
}

// State holds the client-owned ECDSA P-256 keypair and any server-issued guard
// material bound to that public key for the current Douyin login session.
type State struct {
	PrivateKeyPKCS8 string
	PublicKeySEC1   string
	Ticket          string
	TsSign          string
	ClientCert      string
	UpdatedAt       string
}

// HasKeyPair reports whether State carries a non-empty serialized keypair.
func (s State) HasKeyPair() bool {
	return strings.TrimSpace(s.PrivateKeyPKCS8) != "" && strings.TrimSpace(s.PublicKeySEC1) != ""
}

// ClientDataPayload is the JSON structure Base64-encoded into bd-ticket-guard-client-data.
type ClientDataPayload struct {
	TsSign     string `json:"ts_sign"`
	ReqContent string `json:"req_content"`
	ReqSign    string `json:"req_sign"`
	Timestamp  int64  `json:"timestamp"`
}

// ServerData captures parsed guard fields from a server HTTP response.
type ServerData struct {
	HasResultHeader bool
	HasResultCode   bool
	ResultCode      int
	HasServerData   bool
	Ticket          string
	TsSign          string
	ClientCert      string
}

// BuildHeadersInput configures ticket-guard header generation for one request.
type BuildHeadersInput struct {
	State          State
	Path           string
	FallbackTicket string
	Now            func() time.Time
	Entropy        io.Reader
}

// BuildHeadersResult contains the outbound bd-ticket-guard-* headers and whether
// full request signing (bd-ticket-guard-client-data + req_sign) was applied.
type BuildHeadersResult struct {
	Headers map[string]string
	Signed  bool
}

// IsGuardedPath reports whether endpoint participates in bd-ticket-guard headers.
func IsGuardedPath(endpoint string) bool {
	_, ok := guardedPaths[NormalizePath(endpoint)]
	return ok
}

// NormalizePath extracts and normalizes the URL path component (leading slash,
// trailing slash when non-root, stripped of scheme/host/query/fragment).
func NormalizePath(endpoint string) string {
	p := strings.TrimSpace(endpoint)
	if p == "" {
		return ""
	}
	if idx := strings.Index(p, "://"); idx >= 0 {
		rest := p[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			p = rest[slash:]
		} else {
			return "/"
		}
	}
	if q := strings.IndexByte(p, '?'); q >= 0 {
		p = p[:q]
	}
	if h := strings.IndexByte(p, '#'); h >= 0 {
		p = p[:h]
	}
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// GenerateState creates a fresh ECDSA P-256 keypair encoded for encrypted session storage
// and bd-ticket-guard-ree-public-key transport.
func GenerateState(entropy io.Reader, now func() time.Time) (State, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	if now == nil {
		now = time.Now
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), entropy)
	if err != nil {
		return State{}, fmt.Errorf("douyin/guard: generate P-256 key: %w", err)
	}
	return StateFromPrivateKey(priv, now)
}

// StateFromPrivateKey serializes an existing ECDSA P-256 private key into State.
func StateFromPrivateKey(priv *ecdsa.PrivateKey, now func() time.Time) (State, error) {
	if priv == nil || priv.Curve != elliptic.P256() {
		return State{}, ErrInvalidPrivateKey
	}
	if now == nil {
		now = time.Now
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrInvalidPrivateKey, err)
	}
	pubB64, err := EncodePublicKeySEC1(&priv.PublicKey)
	if err != nil {
		return State{}, err
	}
	return State{
		PrivateKeyPKCS8: base64.StdEncoding.EncodeToString(pkcs8),
		PublicKeySEC1:   pubB64,
		UpdatedAt:       now().UTC().Format(time.RFC3339),
	}, nil
}

// ParsePrivateKey decodes a Base64-encoded PKCS#8 DER ECDSA P-256 private key.
func ParsePrivateKey(pkcs8Base64 string) (*ecdsa.PrivateKey, error) {
	trimmed := strings.TrimSpace(pkcs8Base64)
	if trimmed == "" {
		return nil, ErrInvalidPrivateKey
	}
	der, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, ErrInvalidPrivateKey
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, ErrInvalidPrivateKey
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, ErrInvalidPrivateKey
	}
	// Validate via crypto/ecdh to guarantee scalar/point integrity.
	if _, err := priv.ECDH(); err != nil {
		return nil, ErrInvalidPrivateKey
	}
	return priv, nil
}

// EncodePublicKeySEC1 encodes a P-256 public key as standard Base64 of the 65-byte
// uncompressed SEC1 representation (0x04 || X(32B) || Y(32B)).
func EncodePublicKeySEC1(pub *ecdsa.PublicKey) (string, error) {
	if pub == nil || pub.Curve != elliptic.P256() {
		return "", ErrInvalidPublicKey
	}
	ecdhPub, err := pub.ECDH()
	if err != nil {
		return "", ErrInvalidPublicKey
	}
	raw := ecdhPub.Bytes()
	if len(raw) != sec1UncompressedP256Len || raw[0] != 0x04 {
		return "", ErrInvalidPublicKey
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodePublicKeySEC1 decodes and validates a Base64-encoded 65-byte uncompressed
// SEC1 ECDSA P-256 public key.
func DecodePublicKeySEC1(pubBase64 string) (*ecdsa.PublicKey, error) {
	trimmed := strings.TrimSpace(pubBase64)
	if trimmed == "" {
		return nil, ErrInvalidPublicKey
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil || len(raw) != sec1UncompressedP256Len || raw[0] != 0x04 {
		return nil, ErrInvalidPublicKey
	}
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return nil, ErrInvalidPublicKey
	}
	x := new(big.Int).SetBytes(raw[1:33])
	y := new(big.Int).SetBytes(raw[33:65])
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     x,
		Y:     y,
	}, nil
}

// BuildPreimage formats the canonical UTF-8 preimage for req_content="ticket,path,timestamp".
func BuildPreimage(ticket, path string, timestamp int64) string {
	return "ticket=" + strings.TrimSpace(ticket) + "&path=" + NormalizePath(path) + "&timestamp=" + strconv.FormatInt(timestamp, 10)
}

// SignPreimageASN1 signs preimage using SHA-256 + ECDSA P-256 ASN.1 DER and returns standard Base64.
func SignPreimageASN1(priv *ecdsa.PrivateKey, preimage string, entropy io.Reader) (string, error) {
	if priv == nil || priv.Curve != elliptic.P256() {
		return "", ErrInvalidPrivateKey
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	digest := sha256.Sum256([]byte(preimage))
	sig, err := ecdsa.SignASN1(entropy, priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("douyin/guard: sign preimage: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyPreimageASN1 verifies a Base64-encoded ASN.1 DER ECDSA-SHA256 signature over preimage.
func VerifyPreimageASN1(pub *ecdsa.PublicKey, preimage, sigBase64 string) bool {
	if pub == nil || pub.Curve != elliptic.P256() || strings.TrimSpace(sigBase64) == "" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigBase64))
	if err != nil || len(sig) == 0 {
		return false
	}
	digest := sha256.Sum256([]byte(preimage))
	return ecdsa.VerifyASN1(pub, digest[:], sig)
}

// BuildHeaders constructs outbound bd-ticket-guard-* headers for a request.
//
// Always sets the 4 version/public-key handshake headers. When both TsSign and a
// non-empty ticket (State.Ticket or FallbackTicket) are available, also signs the
// request path and Unix timestamp and sets bd-ticket-guard-web-sign-type and
// bd-ticket-guard-client-data.
func BuildHeaders(in BuildHeadersInput) (BuildHeadersResult, error) {
	priv, err := ParsePrivateKey(in.State.PrivateKeyPKCS8)
	if err != nil {
		return BuildHeadersResult{}, err
	}
	pubB64, err := EncodePublicKeySEC1(&priv.PublicKey)
	if err != nil {
		return BuildHeadersResult{}, err
	}
	if storedPub := strings.TrimSpace(in.State.PublicKeySEC1); storedPub != "" && storedPub != pubB64 {
		return BuildHeadersResult{}, ErrInvalidPublicKey
	}

	headers := map[string]string{
		HeaderVersion:          DefaultVersion,
		HeaderIterationVersion: DefaultIterationVersion,
		HeaderWebVersion:       DefaultWebVersion,
		HeaderREEPublicKey:     pubB64,
	}

	tsSign := strings.TrimSpace(in.State.TsSign)
	ticket := strings.TrimSpace(in.State.Ticket)
	if ticket == "" {
		ticket = strings.TrimSpace(in.FallbackTicket)
	}
	normPath := NormalizePath(in.Path)
	if tsSign == "" || ticket == "" {
		return BuildHeadersResult{Headers: headers, Signed: false}, nil
	}
	if normPath == "" {
		return BuildHeadersResult{}, ErrInvalidPath
	}

	nowFn := in.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	ts := nowFn().UTC().Unix()
	preimage := BuildPreimage(ticket, normPath, ts)
	reqSign, err := SignPreimageASN1(priv, preimage, in.Entropy)
	if err != nil {
		return BuildHeadersResult{}, err
	}
	payload := ClientDataPayload{
		TsSign:     tsSign,
		ReqContent: DefaultReqContent,
		ReqSign:    reqSign,
		Timestamp:  ts,
	}
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return BuildHeadersResult{}, fmt.Errorf("douyin/guard: marshal client-data: %w", err)
	}
	headers[HeaderWebSignType] = DefaultWebSignType
	headers[HeaderClientData] = base64.StdEncoding.EncodeToString(rawJSON)
	return BuildHeadersResult{Headers: headers, Signed: true}, nil
}

// ParseServerResponse extracts server-issued guard status and ticket material from
// case-insensitive response headers and optional Set-Cookie key-value pairs.
func ParseServerResponse(headers map[string]string, setCookies map[string]string) ServerData {
	var out ServerData
	normHeaders := make(map[string]string, len(headers))
	for k, v := range headers {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk != "" && strings.TrimSpace(v) != "" {
			normHeaders[lk] = strings.TrimSpace(v)
		}
	}

	if resRaw, ok := normHeaders[HeaderResult]; ok {
		out.HasResultHeader = true
		if code, okCode := parseGuardResultCode(resRaw); okCode {
			out.HasResultCode = true
			out.ResultCode = code
		}
	}

	for _, key := range []string{HeaderServerDataBD, HeaderServerDataTT} {
		if raw := normHeaders[key]; raw != "" {
			out.HasServerData = true
			extractGuardJSONFields(raw, &out)
		}
	}
	for _, key := range []string{HeaderTicketBD, HeaderTicketTT} {
		if raw := normHeaders[key]; raw != "" && out.Ticket == "" {
			out.Ticket = raw
		}
	}
	for _, key := range []string{HeaderClientCertBD, HeaderClientCertTT} {
		if raw := normHeaders[key]; raw != "" && out.ClientCert == "" {
			out.ClientCert = raw
		}
	}

	// Also inspect any server-returned Set-Cookie payloads carrying guard JSON.
	for k, v := range setCookies {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "bd_ticket_guard_server_data" || lk == "tt_ticket_guard_server_data" || lk == "bd_ticket_guard_client_data" {
			if strings.TrimSpace(v) != "" {
				extractGuardJSONFields(v, &out)
			}
		}
	}

	return out
}

// MergeServerData applies newly received server guard fields onto cur without
// clearing previously obtained values when a response omits them.
func MergeServerData(cur State, srv ServerData, now func() time.Time) (State, bool) {
	next := cur
	changed := false
	if v := strings.TrimSpace(srv.Ticket); v != "" && next.Ticket != v {
		next.Ticket = v
		changed = true
	}
	if v := strings.TrimSpace(srv.TsSign); v != "" && next.TsSign != v {
		next.TsSign = v
		changed = true
	}
	if v := strings.TrimSpace(srv.ClientCert); v != "" && next.ClientCert != v {
		next.ClientCert = v
		changed = true
	}
	if changed {
		if now == nil {
			now = time.Now
		}
		next.UpdatedAt = now().UTC().Format(time.RFC3339)
	}
	return next, changed
}

func parseGuardResultCode(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	if v, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return clampInt32(v), true
	}
	// Some gateways emit JSON objects like {"code":0} or {"result":0}.
	if obj, ok := decodeJSONObject(trimmed); ok {
		for _, k := range []string{"result", "code", "status", "status_code"} {
			if rawVal, exists := obj[k]; exists {
				if v, okNum := parseJSONInt64(rawVal); okNum {
					return clampInt32(v), true
				}
			}
		}
	}
	return 0, false
}

func clampInt32(v int64) int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int(v)
}

func parseJSONInt64(raw json.RawMessage) (int64, bool) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		if v, iErr := n.Int64(); iErr == nil {
			return v, true
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, iErr := strconv.ParseInt(strings.TrimSpace(s), 10, 64); iErr == nil {
			return v, true
		}
	}
	return 0, false
}

func extractGuardJSONFields(raw string, out *ServerData) {
	obj, ok := decodeJSONObject(raw)
	if !ok {
		return
	}
	readStr := func(keys ...string) string {
		for _, k := range keys {
			rawVal, exists := obj[k]
			if !exists {
				continue
			}
			var s string
			if json.Unmarshal(rawVal, &s) == nil {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if ts := readStr("ts_sign", "ts_sign_ree"); ts != "" {
		out.TsSign = ts
	}
	if tk := readStr("ticket", "bd-ticket-guard-ticket", "tt-ticket-guard-ticket"); tk != "" {
		out.Ticket = tk
	}
	if cert := readStr("client_cert", "cert", "bd-ticket-guard-client-cert", "tt-ticket-guard-client-cert"); cert != "" {
		out.ClientCert = cert
	}
}

func decodeJSONObject(raw string) (map[string]json.RawMessage, bool) {
	candidates := []string{strings.TrimSpace(raw)}
	if unesc, err := url.QueryUnescape(candidates[0]); err == nil && unesc != candidates[0] {
		candidates = append(candidates, strings.TrimSpace(unesc))
	}
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if strings.HasPrefix(cand, "{") {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(cand), &m) == nil && len(m) > 0 {
				return m, true
			}
		}
		for _, enc := range []*base64.Encoding{
			base64.StdEncoding,
			base64.RawStdEncoding,
			base64.URLEncoding,
			base64.RawURLEncoding,
		} {
			decoded, err := enc.DecodeString(cand)
			if err != nil || len(decoded) == 0 {
				continue
			}
			trimmed := strings.TrimSpace(string(decoded))
			if !strings.HasPrefix(trimmed, "{") {
				continue
			}
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(trimmed), &m) == nil && len(m) > 0 {
				return m, true
			}
		}
	}
	return nil, false
}
