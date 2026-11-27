package companionclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

// Client talks to one paired desktop TRACE over pinned-fingerprint TLS 1.3.
type Client struct {
	mu         sync.RWMutex
	record     PairingRecord
	token      string
	appVersion string
	channel    string
	activeAddr string
	httpClient *http.Client
	pollClient *http.Client
}

// NewClient creates a pinned-TLS companion client for an existing pairing record and token.
func NewClient(rec PairingRecord, token, appVersion, channel string) (*Client, error) {
	httpCli, err := PinnedHTTPClient(rec.FP, 15*time.Second)
	if err != nil {
		return nil, err
	}
	pollCli, err := PinnedHTTPClient(rec.FP, 70*time.Second)
	if err != nil {
		return nil, err
	}
	rec.Channel = NormalizeChannel(rec.Channel)
	return &Client{
		record:     rec,
		token:      token,
		appVersion: appVersion,
		channel:    NormalizeChannel(channel),
		activeAddr: rec.LastAddr,
		httpClient: httpCli,
		pollClient: pollCli,
	}, nil
}

// ActiveAddr returns the currently working "host:port" address.
func (c *Client) ActiveAddr() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.activeAddr != "" {
		return c.activeAddr
	}
	addrs := c.record.OrderedAddrs()
	if len(addrs) > 0 {
		return addrs[0]
	}
	return ""
}

// ActiveBaseURL returns "https://<ActiveAddr>".
func (c *Client) ActiveBaseURL() string {
	addr := c.ActiveAddr()
	if addr == "" {
		return ""
	}
	return "https://" + addr
}

// Token returns the device token (never log this).
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// AppVersion returns the phone app version.
func (c *Client) AppVersion() string { return c.appVersion }

// Channel returns the normalized phone channel ("stable" or "dev").
func (c *Client) Channel() string { return c.channel }

// HTTPClient returns the pinned HTTP client.
func (c *Client) HTTPClient() *http.Client { return c.httpClient }

// Close closes idle connections.
func (c *Client) Close() {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	if c.pollClient != nil {
		c.pollClient.CloseIdleConnections()
	}
}

// ConnectStatus tries every address in the pairing record in order (LastAddr first)
// and fetches GET /api/v1/status. If status.LoggedIn is false, it returns
// *Error{Kind: KindDesktopLoggedOut} alongside the fetched status (design §8).
func (c *Client) ConnectStatus(ctx context.Context) (companionapi.StatusResponse, string, error) {
	c.mu.RLock()
	addrs := c.record.OrderedAddrs()
	c.mu.RUnlock()
	if len(addrs) == 0 {
		return companionapi.StatusResponse{}, "", &Error{Kind: KindUnreachable}
	}
	var lastErr error
	for _, addr := range addrs {
		var st companionapi.StatusResponse
		err := c.doOneAddr(ctx, c.httpClient, addr, http.MethodGet, companionapi.PathStatus, nil, nil, &st, true)
		if err == nil {
			c.mu.Lock()
			c.activeAddr = addr
			c.record.LastAddr = addr
			c.mu.Unlock()
			if !st.LoggedIn {
				return st, addr, &Error{Kind: KindDesktopLoggedOut}
			}
			return st, addr, nil
		}
		lastErr = err
		switch KindOf(err) {
		case KindCertChanged, KindUnauthorized, KindProtocolMismatch, KindRateLimited, KindForbiddenSource, KindCanceled:
			return companionapi.StatusResponse{}, "", err
		}
	}
	if lastErr == nil {
		lastErr = &Error{Kind: KindUnreachable}
	}
	return companionapi.StatusResponse{}, "", lastErr
}

// Status fetches GET /api/v1/status from the active address (or tries all addresses).
func (c *Client) Status(ctx context.Context) (companionapi.StatusResponse, error) {
	st, _, err := c.ConnectStatus(ctx)
	return st, err
}

// Unpair calls DELETE /api/v1/devices/self to remove this device on the desktop.
func (c *Client) Unpair(ctx context.Context) error {
	addr := c.ActiveAddr()
	if addr == "" {
		return nil
	}
	return c.doOneAddr(ctx, c.httpClient, addr, http.MethodDelete, companionapi.PathDevicesSelf, nil, nil, nil, true)
}

// DoJSON sends an authenticated JSON request to path (e.g. "/api/v1/feed") on the active address.
func (c *Client) DoJSON(ctx context.Context, method, path string, reqBody any, respOut any) error {
	addr := c.ActiveAddr()
	if addr == "" {
		return &Error{Kind: KindUnreachable}
	}
	var raw []byte
	if reqBody != nil {
		var err error
		raw, err = json.Marshal(reqBody)
		if err != nil {
			return err
		}
	}
	return c.doOneAddr(ctx, c.httpClient, addr, method, path, nil, raw, respOut, true)
}

// FetchBytes downloads raw bytes (e.g. thumbnail or image) over pinned TLS with Bearer auth.
func (c *Client) FetchBytes(ctx context.Context, path string) ([]byte, error) {
	addr := c.ActiveAddr()
	if addr == "" {
		return nil, &Error{Kind: KindUnreachable}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+path, nil)
	if err != nil {
		return nil, ClassifyTransportError(err)
	}
	c.setCommonHeaders(req, true)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ClassifyTransportError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, ClassifyTransportError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyHTTPStatus(resp.StatusCode, resp.Header, raw, c.appVersion)
	}
	return raw, nil
}

// DialEvents opens the WebSocket event stream on the currently active address.
func (c *Client) DialEvents(ctx context.Context, heartbeatTimeout time.Duration) (*EventConn, error) {
	base := c.ActiveBaseURL()
	if base == "" {
		return nil, &Error{Kind: KindUnreachable}
	}
	return DialEvents(ctx, c.httpClient, base, c.Token(), c.appVersion, c.channel, heartbeatTimeout)
}

// RescanUpdateAddrs calls POST /api/v1/pair WITH the device's existing Bearer
// token when an already-paired phone scans a QR code for the same node and
// certificate fingerprint (design §4.7).
func RescanUpdateAddrs(
	ctx context.Context,
	addrs []string,
	fp, existingToken string,
	req companionapi.PairRequest,
) (freshAddrs []string, winningAddr string, err error) {
	if len(addrs) == 0 || existingToken == "" {
		return nil, "", &Error{Kind: KindUnreachable}
	}
	httpCli, err := PinnedHTTPClient(fp, 15*time.Second)
	if err != nil {
		return nil, "", err
	}
	defer httpCli.CloseIdleConnections()

	if req.PairSecret == "" {
		sec, err := RandomHex128()
		if err != nil {
			return nil, "", err
		}
		req.PairSecret = sec
	}
	req.Channel = NormalizeChannel(req.Channel)

	tmp := &Client{
		token:      existingToken,
		appVersion: req.AppVersion,
		channel:    req.Channel,
		httpClient: httpCli,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	var lastErr error
	for _, addr := range addrs {
		var resp companionapi.PairStartResponse
		err := tmp.doOneAddr(ctx, httpCli, addr, http.MethodPost, companionapi.PathPair, nil, body, &resp, true)
		if err == nil {
			port := DefaultPortForChannel(req.Channel)
			if _, portStr, splitErr := net.SplitHostPort(addr); splitErr == nil {
				if p, parseErr := strconv.Atoi(portStr); parseErr == nil && p > 0 && p <= 65535 {
					port = p
				}
			}
			out := normalizeEndpointList(resp.Addrs, port)
			if len(out) == 0 {
				out = normalizeEndpointList(addrs, port)
			}
			return out, addr, nil
		}
		lastErr = err
		switch KindOf(err) {
		case KindCertChanged, KindUnauthorized, KindChannelMismatch, KindBadPairingCode, KindProtocolMismatch, KindRateLimited, KindForbiddenSource, KindCanceled:
			return nil, "", err
		}
	}
	if lastErr == nil {
		lastErr = &Error{Kind: KindUnreachable}
	}
	return nil, "", lastErr
}

// PairWithAddrs executes the full pairing handshake (POST /api/v1/pair + long-poll
// GET /api/v1/pair/{pairId}) against candidate addrs with a pinned fingerprint fp.
func PairWithAddrs(
	ctx context.Context,
	addrs []string,
	fp string,
	req companionapi.PairRequest,
	onWaiting func(addr string),
) (token string, winningAddr string, pollMeta companionapi.PairPollResponse, err error) {
	if len(addrs) == 0 {
		return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindUnreachable}
	}
	httpCli, err := PinnedHTTPClient(fp, 15*time.Second)
	if err != nil {
		return "", "", companionapi.PairPollResponse{}, err
	}
	defer httpCli.CloseIdleConnections()
	pollCli, err := PinnedHTTPClient(fp, 70*time.Second)
	if err != nil {
		return "", "", companionapi.PairPollResponse{}, err
	}
	defer pollCli.CloseIdleConnections()

	if req.PairSecret == "" {
		sec, err := RandomHex128()
		if err != nil {
			return "", "", companionapi.PairPollResponse{}, err
		}
		req.PairSecret = sec
	}
	req.Channel = NormalizeChannel(req.Channel)

	tmp := &Client{
		appVersion: req.AppVersion,
		channel:    req.Channel,
		httpClient: httpCli,
		pollClient: pollCli,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", "", companionapi.PairPollResponse{}, err
	}

	var startResp companionapi.PairStartResponse
	var chosenAddr string
	var lastErr error
	for _, addr := range addrs {
		err := tmp.doOneAddr(ctx, httpCli, addr, http.MethodPost, companionapi.PathPair, nil, body, &startResp, false)
		if err == nil && startResp.PairID != "" {
			chosenAddr = addr
			break
		}
		if err == nil {
			err = &Error{Kind: KindServerError, Message: "电脑端未返回配对编号"}
		}
		lastErr = err
		switch KindOf(err) {
		case KindCertChanged, KindChannelMismatch, KindDeviceLimit, KindBadPairingCode, KindProtocolMismatch, KindRateLimited, KindForbiddenSource, KindCanceled:
			return "", "", companionapi.PairPollResponse{}, err
		}
	}
	if chosenAddr == "" {
		if lastErr == nil {
			lastErr = &Error{Kind: KindUnreachable}
		}
		return "", "", companionapi.PairPollResponse{}, lastErr
	}
	if onWaiting != nil {
		onWaiting(chosenAddr)
	}

	pollPath := companionapi.PairPollPath(startResp.PairID)
	extraHdrs := map[string]string{companionapi.HeaderPairSecret: req.PairSecret}
	deadline := time.Now().Add(130 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindCanceled, Cause: ctx.Err()}
		}
		var pr companionapi.PairPollResponse
		err := tmp.doOneAddr(ctx, pollCli, chosenAddr, http.MethodGet, pollPath, extraHdrs, nil, &pr, false)
		if err != nil {
			if ce, ok := err.(*Error); ok && ce.Status == http.StatusNotFound {
				return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindPairExpired}
			}
			return "", "", companionapi.PairPollResponse{}, err
		}
		switch strings.ToLower(strings.TrimSpace(pr.Status)) {
		case companionapi.PairStatusApproved:
			if pr.Token == "" {
				return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindServerError, Message: "电脑端未发放设备令牌"}
			}
			return pr.Token, chosenAddr, pr, nil
		case companionapi.PairStatusRejected:
			return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindPairRejected}
		case companionapi.PairStatusExpired:
			return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindPairExpired}
		case companionapi.PairStatusPending, "":
			continue
		default:
			return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindServerError, Message: fmt.Sprintf("未知配对状态：%s", pr.Status)}
		}
	}
	return "", "", companionapi.PairPollResponse{}, &Error{Kind: KindPairExpired}
}

func (c *Client) setCommonHeaders(req *http.Request, includeAuth bool) {
	if includeAuth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	if c.appVersion != "" {
		req.Header.Set(companionapi.HeaderVersion, c.appVersion)
	}
	if c.channel != "" {
		req.Header.Set(companionapi.HeaderChannel, c.channel)
	}
}

func (c *Client) doOneAddr(
	ctx context.Context,
	httpCli *http.Client,
	addr, method, path string,
	extraHeaders map[string]string,
	reqBody []byte,
	out any,
	includeAuth bool,
) error {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var rd io.Reader
	if reqBody != nil {
		rd = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, rd)
	if err != nil {
		return ClassifyTransportError(err)
	}
	c.setCommonHeaders(req, includeAuth)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := httpCli.Do(req)
	if err != nil {
		return ClassifyTransportError(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ClassifyTransportError(err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return &Error{Kind: KindServerError, Status: resp.StatusCode, Message: "电脑端响应格式不正确"}
			}
		}
		return nil
	}
	return classifyHTTPStatus(resp.StatusCode, resp.Header, raw, c.appVersion)
}
