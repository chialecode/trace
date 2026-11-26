package companionclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

// ErrorKind classifies connection, pairing, and runtime disconnect reasons (design §8).
type ErrorKind string

const (
	KindConnRefused      ErrorKind = "tcp_refused"
	KindUnreachable      ErrorKind = "unreachable"
	KindPhoneOffline     ErrorKind = "phone_offline"
	KindCertChanged      ErrorKind = "cert_changed"
	KindUnauthorized     ErrorKind = "unauthorized"
	KindForbiddenSource  ErrorKind = "forbidden_source"
	KindProtocolMismatch ErrorKind = "protocol_mismatch"
	KindRateLimited      ErrorKind = "rate_limited"
	KindDesktopLoggedOut ErrorKind = "desktop_logged_out"
	KindChannelMismatch  ErrorKind = "channel_mismatch"
	KindDeviceLimit      ErrorKind = "device_limit"
	KindByeExit          ErrorKind = "bye_exit"
	KindByeUpdate        ErrorKind = "bye_update"
	KindByeDisabled      ErrorKind = "bye_disabled"
	KindByeRevoked       ErrorKind = "bye_revoked"
	KindBadPairingCode   ErrorKind = "bad_pairing_code"
	KindPairRejected     ErrorKind = "pair_rejected"
	KindPairExpired      ErrorKind = "pair_expired"
	KindCanceled         ErrorKind = "canceled"
	KindServerError      ErrorKind = "server_error"
)

// ErrorAction describes an action button displayed on the connection error screen.
type ErrorAction string

const (
	ActionRetry        ErrorAction = "retry"
	ActionRescan       ErrorAction = "rescan"
	ActionOpenDownload ErrorAction = "open_download"
)

// Error is a classified companion client error.
type Error struct {
	Kind           ErrorKind
	Code           string
	Message        string
	ServerVersion  string
	ServerProtocol string
	ClientVersion  string
	ClientProtocol string
	UpdateSide     string // "mobile", "desktop", or ""
	Status         int
	Cause          error
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.UserMessage()
}

// UserMessage returns the exact user-facing Chinese reason for design §8.
func (e *Error) UserMessage() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case KindConnRefused:
		return "电脑上的 TRACE 没有运行，或没有打开‘允许手机连接’"
	case KindUnreachable:
		return "找不到电脑：电脑可能关机、睡眠，或手机与电脑不在同一网络"
	case KindPhoneOffline:
		return "手机没有连接网络"
	case KindCertChanged:
		return "电脑的身份变了，请重新扫码"
	case KindUnauthorized:
		return "这台电脑已移除本机，请重新扫码"
	case KindForbiddenSource:
		return "电脑只接受同一局域网或 VPN 内的连接"
	case KindProtocolMismatch:
		sv := firstNonEmpty(e.ServerVersion, "未知")
		cv := firstNonEmpty(e.ClientVersion, "未知")
		sp := firstNonEmpty(e.ServerProtocol, "未知")
		cp := firstNonEmpty(e.ClientProtocol, companionapi.ProtocolVersion)
		base := fmt.Sprintf("版本不兼容：电脑端 %s（协议 %s），手机端 %s（协议 %s）。", sv, sp, cv, cp)
		if e.UpdateSide == "desktop" {
			return base + "请更新电脑上的 TRACE"
		}
		return base + "请更新手机上的 TRACE"
	case KindRateLimited:
		return "尝试次数过多，请 10 分钟后再试"
	case KindDesktopLoggedOut:
		return "请先在电脑上登录一个站点"
	case KindByeExit:
		return "电脑上的 TRACE 已退出"
	case KindByeUpdate:
		return "电脑上的 TRACE 正在更新"
	case KindByeDisabled:
		return "电脑上的 TRACE 已关闭手机连接"
	case KindByeRevoked:
		return "电脑上的 TRACE 已移除本机"
	case KindChannelMismatch:
		if e.Message != "" {
			return e.Message
		}
		return "手机端与电脑端通道不一致（正式版只能配对正式版，开发版只能配对开发版）"
	case KindDeviceLimit:
		return "电脑端已配对设备已达上限（5 台），请先在电脑上移除一台"
	case KindBadPairingCode:
		if e.Message != "" {
			return e.Message
		}
		return "配对码无效或已过期，请刷新电脑上的二维码或配对码"
	case KindPairRejected:
		return "电脑端已拒绝这次配对请求"
	case KindPairExpired:
		return "配对确认已超时，请重新配对"
	case KindCanceled:
		return "已取消连接"
	default:
		if e.Message != "" {
			return e.Message
		}
		if e.Status > 0 {
			return fmt.Sprintf("连接出错（HTTP %d）", e.Status)
		}
		return "连接失败，请重试"
	}
}

// Actions returns the allowed recovery actions for this error (design §8).
func (e *Error) Actions() []ErrorAction {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case KindPhoneOffline, KindRateLimited:
		return nil
	case KindCertChanged, KindUnauthorized:
		return []ErrorAction{ActionRescan}
	case KindForbiddenSource, KindDesktopLoggedOut:
		return []ErrorAction{ActionRetry}
	case KindProtocolMismatch:
		if e.UpdateSide == "mobile" || e.UpdateSide == "" {
			return []ErrorAction{ActionOpenDownload}
		}
		return []ErrorAction{ActionRetry}
	case KindConnRefused, KindUnreachable, KindByeExit, KindByeUpdate, KindByeDisabled, KindByeRevoked:
		return []ErrorAction{ActionRetry, ActionRescan}
	default:
		return []ErrorAction{ActionRetry, ActionRescan}
	}
}

// KindOf returns the ErrorKind if err contains *Error, or "".
func KindOf(err error) ErrorKind {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Kind
	}
	return ""
}

// ByeError converts a WebSocket bye reason ("exit", "update", "disabled", "revoked")
// into the corresponding *Error.
func ByeError(reason string) *Error {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case companionapi.ByeReasonExit:
		return &Error{Kind: KindByeExit, Code: "bye_exit"}
	case companionapi.ByeReasonUpdate:
		return &Error{Kind: KindByeUpdate, Code: "bye_update"}
	case companionapi.ByeReasonDisabled:
		return &Error{Kind: KindByeDisabled, Code: "bye_disabled"}
	case companionapi.ByeReasonRevoked:
		return &Error{Kind: KindByeRevoked, Code: "bye_revoked"}
	default:
		return &Error{Kind: KindByeExit, Code: "bye_" + strings.TrimSpace(reason)}
	}
}

var errFingerprintMismatch = errors.New("companionclient: certificate fingerprint mismatch")

// Fingerprint delegates to companionapi.Fingerprint.
func Fingerprint(der []byte) string { return companionapi.Fingerprint(der) }

// IdentityCode formats the first 64 bits of fp as XXXX-XXXX-XXXX-XXXX.
func IdentityCode(fp string) string {
	id, err := companionapi.IdentityCode(fp)
	if err != nil {
		return ""
	}
	return id
}

// RandomHex128 returns a 128-bit cryptographically random lowercase hex string.
func RandomHex128() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// RandomBase64URL128 returns a 128-bit cryptographically random unpadded base64url string.
func RandomBase64URL128() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// PinnedTLSConfig returns a TLS 1.3 config that trusts ONLY the certificate whose
// leaf DER SHA-256 matches fp. System root CAs are never consulted.
func PinnedTLSConfig(fp string) (*tls.Config, error) {
	want, ok := companionapi.NormalizeFingerprint(fp)
	if !ok {
		return nil, errors.New("companionclient: invalid certificate fingerprint")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // verified against pinned leaf DER SHA-256 below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errFingerprintMismatch
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != want {
				return errFingerprintMismatch
			}
			return nil
		},
	}, nil
}

// PinnedTransport returns an HTTP transport pinned to fp with Proxy disabled (LAN only).
func PinnedTransport(fp string) (*http.Transport, error) {
	tlsCfg, err := PinnedTLSConfig(fp)
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsCfg,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     false,
	}, nil
}

// PinnedHTTPClient builds an HTTP client trusting only fp and never following redirects.
func PinnedHTTPClient(fp string, timeout time.Duration) (*http.Client, error) {
	tr, err := PinnedTransport(fp)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if timeout > tr.ResponseHeaderTimeout {
		// Long-poll requests (e.g. GET /api/v1/pair/{id} up to 60s) must not
		// be aborted prematurely by the default 30s ResponseHeaderTimeout.
		tr.ResponseHeaderTimeout = timeout
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// ProbeIdentity connects to addr over TLS 1.3 without sending any HTTP request
// or pairing code, reads the server's leaf certificate, and returns its SHA-256
// fingerprint and 64-bit identity code (design §4.2).
func ProbeIdentity(ctx context.Context, addr string) (fp string, identityCode string, err error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tlsDialer := &tls.Dialer{
		NetDialer: dialer,
		Config: &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, //nolint:gosec // manual pairing inspects leaf cert before user confirms
		},
	}
	conn, err := tlsDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", "", ClassifyTransportError(err)
	}
	defer conn.Close()

	tc, ok := conn.(*tls.Conn)
	if !ok {
		return "", "", &Error{Kind: KindUnreachable, Message: "安全连接失败"}
	}
	cs := tc.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return "", "", &Error{Kind: KindUnreachable, Message: "电脑端未提供证书"}
	}
	fp = companionapi.Fingerprint(cs.PeerCertificates[0].Raw)
	id, _ := companionapi.IdentityCode(fp)
	return fp, id, nil
}

// ClassifyTransportError maps dial/TLS/context errors to design §8 ErrorKind.
func ClassifyTransportError(err error) *Error {
	if err == nil {
		return nil
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce
	}
	if errors.Is(err, errFingerprintMismatch) || strings.Contains(err.Error(), errFingerprintMismatch.Error()) {
		return &Error{Kind: KindCertChanged, Cause: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: KindCanceled, Cause: err}
	}
	var nerr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()) {
		return &Error{Kind: KindUnreachable, Cause: err}
	}
	lower := strings.ToLower(err.Error())
	switch {
	case isConnRefused(err, lower):
		return &Error{Kind: KindConnRefused, Cause: err}
	case strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "host is unreachable") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "i/o timeout"):
		return &Error{Kind: KindUnreachable, Cause: err}
	default:
		return &Error{Kind: KindUnreachable, Cause: err}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
