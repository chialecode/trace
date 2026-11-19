package remote

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrorKind classifies a failed remote request for the desktop's Settings ›
// 远程 (remote.md §7).
type ErrorKind string

const (
	KindUnreachable  ErrorKind = "unreachable"  // 无法连接
	KindCertChanged  ErrorKind = "cert_changed" // 证书变了
	KindUnauthorized ErrorKind = "unauthorized" // 令牌错
	KindRateLimited  ErrorKind = "rate_limited" // 暂时拒绝
	KindVersion      ErrorKind = "version"      // 版本不同
	KindRejected     ErrorKind = "rejected"     // 远程拒绝 Cookie
	KindRemote       ErrorKind = "remote"       // 其他远程错误
)

// Error is a failed remote request.
type Error struct {
	Kind           ErrorKind
	Message        string
	ServerVersion  string
	ServerProtocol string
	LocalVersion   string
	Status         int
}

func (e *Error) Error() string {
	switch e.Kind {
	case KindUnreachable:
		return "无法连接远程：" + e.Message
	case KindCertChanged:
		return "远程证书变了：与连接码里的指纹不一致，已拒绝连接。若远程重新 init 过，请粘贴新的连接码"
	case KindUnauthorized:
		return "远程拒绝了访问令牌：令牌错误或已更换，请粘贴新的连接码"
	case KindRateLimited:
		return "远程暂时拒绝连接：失败次数过多，请 10 分钟后再试"
	case KindVersion:
		return fmt.Sprintf("两边版本不同：远程 %s（协议 %s），本机 %s（协议 %s）。请把较旧的一边更新到同一版本", firstNonEmpty(e.ServerVersion, "未知"), firstNonEmpty(e.ServerProtocol, "未知"), firstNonEmpty(e.LocalVersion, "未知"), ProtocolVersion)
	case KindRejected:
		return "远程拒绝了 Cookie：" + e.Message
	default:
		if e.Message != "" {
			return "远程出错：" + e.Message
		}
		return fmt.Sprintf("远程出错（HTTP %d）", e.Status)
	}
}

// KindOf returns the kind of a remote error, or "".
func KindOf(err error) ErrorKind {
	var re *Error
	if errors.As(err, &re) {
		return re.Kind
	}
	return ""
}

// errFingerprint is returned by the TLS check when the certificate differs.
var errFingerprint = errors.New("remote: certificate fingerprint mismatch")

// Client talks to one remote over HTTPS, trusting only the certificate
// whose SHA-256 is the connection's fingerprint.
type Client struct {
	conn    Connection
	version string
	http    *http.Client
}

// NewClient builds a client for conn; version is this build's version,
// sent with every request.
func NewClient(conn Connection, version string) (*Client, error) {
	conn, err := conn.Normalize()
	if err != nil {
		return nil, err
	}
	want := conn.Fingerprint
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The system's certificate store is not used: the only certificate
		// trusted is the one pinned by the connection code. Verification is
		// done in VerifyConnection, before any request byte is sent.
		InsecureSkipVerify: true, //nolint:gosec // pinned below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || Fingerprint(cs.PeerCertificates[0].Raw) != want {
				return errFingerprint
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsCfg,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          2,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	httpClient := &http.Client{
		Transport: tr,
		Timeout:   60 * time.Second,
		// Never follow redirects: prevents downgrade from pinned HTTPS to an
		// unpinned host or plaintext HTTP.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Client{conn: conn, version: version, http: httpClient}, nil
}

// Connection returns the client's connection.
func (c *Client) Connection() Connection { return c.conn }

// Close drops idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Status reads GET status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.do(ctx, http.MethodGet, "status", nil, &st)
	return st, err
}

// Subscriptions reads the remote's synced subscription summary.
func (c *Client) Subscriptions(ctx context.Context) (SubscriptionsInfo, error) {
	var info SubscriptionsInfo
	err := c.do(ctx, http.MethodGet, "subscriptions", nil, &info)
	return info, err
}

// PutSubscriptions replaces the remote's subscriptions with snapshot (the
// trace-subscriptions JSON).
func (c *Client) PutSubscriptions(ctx context.Context, snapshot []byte) (SubscriptionsInfo, error) {
	var info SubscriptionsInfo
	err := c.do(ctx, http.MethodPut, "subscriptions", snapshot, &info)
	return info, err
}

// PushCredential sends one site's Cookie; the remote checks it before it
// keeps it.
func (c *Client) PushCredential(ctx context.Context, siteID string, push CredentialPush) (SiteStatus, error) {
	body, _ := json.Marshal(push)
	var st SiteStatus
	err := c.do(ctx, http.MethodPut, "credentials/"+url.PathEscape(siteID), body, &st)
	return st, err
}

// DeleteCredential clears the remote's login of one site.
func (c *Client) DeleteCredential(ctx context.Context, siteID string) error {
	return c.do(ctx, http.MethodDelete, "credentials/"+url.PathEscape(siteID), nil, nil)
}

// RetryUpload makes failed uploads retry at once.
func (c *Client) RetryUpload(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "upload/retry", nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.conn.URL+APIPrefix+path, rd)
	if err != nil {
		return &Error{Kind: KindUnreachable, Message: "地址不正确"}
	}
	req.Header.Set("Authorization", "Bearer "+c.conn.Token)
	req.Header.Set(HeaderProtocol, ProtocolVersion)
	req.Header.Set(HeaderVersion, c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return classifyTransport(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &Error{Kind: KindUnreachable, Message: "读取响应中断"}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return &Error{Kind: KindRemote, Message: "响应格式不正确", Status: resp.StatusCode}
			}
		}
		return nil
	}
	var eb ErrorBody
	_ = json.Unmarshal(raw, &eb)
	e := &Error{LocalVersion: c.version, Status: resp.StatusCode, Message: eb.Error.Message, ServerVersion: firstNonEmpty(eb.Error.ServerVersion, resp.Header.Get(HeaderVersion)), ServerProtocol: firstNonEmpty(eb.Error.ServerProtocol, resp.Header.Get(HeaderProtocol))}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		e.Kind = KindUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case resp.StatusCode == http.StatusConflict && eb.Error.Code == CodeProtocol:
		e.Kind = KindVersion
	case eb.Error.Code == CodeCredentialRejected:
		e.Kind = KindRejected
	default:
		e.Kind = KindRemote
	}
	return e
}

func classifyTransport(err error) error {
	if errors.Is(err, errFingerprint) || strings.Contains(err.Error(), errFingerprint.Error()) {
		return &Error{Kind: KindCertChanged}
	}
	msg := "网络不通或远程没有运行"
	var nerr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &nerr) && nerr.Timeout():
		msg = "连接超时"
	case errors.Is(err, context.Canceled):
		msg = "已取消"
	case strings.Contains(strings.ToLower(err.Error()), "refused"):
		msg = "远程拒绝连接（程序没有运行或端口没有放行）"
	case strings.Contains(strings.ToLower(err.Error()), "no such host"):
		msg = "找不到这个地址"
	case strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(strings.ToLower(err.Error()), "certificate"):
		msg = "安全连接失败"
	}
	return &Error{Kind: KindUnreachable, Message: msg}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
