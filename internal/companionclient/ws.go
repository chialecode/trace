package companionclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/companionapi"
)

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xa

	wsCloseNormal        = 1000
	wsCloseGoingAway     = 1001
	wsCloseProtocolError = 1002
	wsCloseInvalidData   = 1007
	wsCloseTooBig        = 1009

	// DefaultHeartbeatTimeout matches design §5.4 / FR-MOB-006 (15s without any frame => disconnected).
	DefaultHeartbeatTimeout = companionapi.WSPongTimeout
	wsMaxMessageBytes       = 4 << 20
)

// EventFrame aliases companionapi.EventFrame.
type EventFrame = companionapi.EventFrame

// EventConn is a live WebSocket connection to /api/v1/events.
type EventConn struct {
	rwc              io.ReadWriteCloser
	br               *bufio.Reader
	heartbeatTimeout time.Duration

	wmu       sync.Mutex
	closeSent bool
	once      sync.Once
}

// DialEvents opens a pinned-TLS WebSocket connection to wss://<addr>/api/v1/events.
func DialEvents(ctx context.Context, httpClient *http.Client, baseURL, token, version, channel string, heartbeatTimeout time.Duration) (*EventConn, error) {
	if httpClient == nil {
		return nil, errors.New("companionclient: nil httpClient")
	}
	if heartbeatTimeout <= 0 {
		heartbeatTimeout = DefaultHeartbeatTimeout
	}
	u := strings.TrimRight(baseURL, "/") + companionapi.PathEvents
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, ClassifyTransportError(err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	wsKey := base64.StdEncoding.EncodeToString(nonce[:])
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	if version != "" {
		req.Header.Set(companionapi.HeaderVersion, version)
	}
	if channel != "" {
		req.Header.Set(companionapi.HeaderChannel, channel)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", wsKey)
	req.Header.Set("Sec-WebSocket-Version", "13")

	c := *httpClient
	c.Timeout = 0
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return nil, ClassifyTransportError(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, classifyHTTPStatus(resp.StatusCode, resp.Header, raw, version)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !wsHasToken(resp.Header.Get("Connection"), "upgrade") {
		resp.Body.Close()
		return nil, &Error{Kind: KindServerError, Message: "电脑端未升级为 WebSocket 连接"}
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != wsAcceptKey(wsKey) {
		resp.Body.Close()
		return nil, &Error{Kind: KindServerError, Message: "WebSocket 握手校验失败"}
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, &Error{Kind: KindServerError, Message: "HTTP 客户端不支持 WebSocket 升级"}
	}
	return &EventConn{
		rwc:              rwc,
		br:               bufio.NewReader(rwc),
		heartbeatTimeout: heartbeatTimeout,
	}, nil
}

// ReadEvent blocks until the next JSON EventFrame arrives, the server sends close,
// the 15-second heartbeat deadline expires with zero frames, or ctx is canceled.
func (ec *EventConn) ReadEvent(ctx context.Context) (EventFrame, error) {
	stopWatch := context.AfterFunc(ctx, func() {
		_ = ec.Close()
	})
	defer stopWatch()

	for {
		op, data, err := ec.readMessageWithDeadline()
		if err != nil {
			if ctx.Err() != nil {
				return EventFrame{}, &Error{Kind: KindCanceled, Cause: ctx.Err()}
			}
			return EventFrame{}, ClassifyTransportError(err)
		}
		if op == wsOpClose {
			return EventFrame{}, io.EOF
		}
		if op != wsOpText && op != wsOpBinary {
			continue
		}
		var ev EventFrame
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		return ev, nil
	}
}

// Close sends a close frame best-effort and closes the underlying connection.
func (ec *EventConn) Close() error {
	var err error
	ec.once.Do(func() {
		_ = ec.writeClose(wsCloseNormal)
		err = ec.rwc.Close()
	})
	return err
}

func (ec *EventConn) readMessageWithDeadline() (byte, []byte, error) {
	var msgOp byte
	var data []byte
	for {
		fin, op, p, err := ec.readFrameWithDeadline()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case wsOpPing:
			// Any incoming frame (including ping) proves liveness and resets heartbeat timer.
			_ = ec.writeFrame(wsOpPong, p)
			continue
		case wsOpPong:
			continue
		case wsOpClose:
			_ = ec.writeClose(wsCloseNormal)
			return wsOpClose, p, nil
		case wsOpContinuation:
			if msgOp == 0 {
				return 0, nil, errors.New("websocket: unexpected continuation frame")
			}
		case wsOpText, wsOpBinary:
			if msgOp != 0 {
				return 0, nil, errors.New("websocket: interleaved message")
			}
			msgOp = op
		default:
			return 0, nil, fmt.Errorf("websocket: unknown opcode %d", op)
		}
		if int64(len(data)+len(p)) > wsMaxMessageBytes {
			return 0, nil, fmt.Errorf("websocket: message exceeds %d bytes", wsMaxMessageBytes)
		}
		data = append(data, p...)
		if fin {
			if msgOp == wsOpText && !utf8.Valid(data) {
				return 0, nil, errors.New("websocket: invalid utf-8 text frame")
			}
			if data == nil {
				data = []byte{}
			}
			return msgOp, data, nil
		}
	}
}

func (ec *EventConn) readFrameWithDeadline() (fin bool, op byte, payload []byte, err error) {
	if c, ok := ec.rwc.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = c.SetReadDeadline(time.Now().Add(ec.heartbeatTimeout))
	}
	timer := time.AfterFunc(ec.heartbeatTimeout, func() {
		_ = ec.rwc.Close()
	})
	defer timer.Stop()

	var h [8]byte
	if _, err := io.ReadFull(ec.br, h[:2]); err != nil {
		return false, 0, nil, err
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	if h[0]&0x70 != 0 {
		return false, 0, nil, errors.New("websocket: reserved bits set")
	}
	if h[1]&0x80 != 0 {
		// Server-to-client frames must not be masked.
		return false, 0, nil, errors.New("websocket: server frame masked")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		if _, err := io.ReadFull(ec.br, h[:2]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(h[:2]))
	case 127:
		if _, err := io.ReadFull(ec.br, h[:8]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(h[:8])
		if n>>63 != 0 {
			return false, 0, nil, errors.New("websocket: invalid frame length")
		}
	}
	if op >= wsOpClose && (!fin || n > 125) {
		return false, 0, nil, errors.New("websocket: invalid control frame")
	}
	if n > wsMaxMessageBytes {
		return false, 0, nil, errors.New("websocket: frame too large")
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(ec.br, payload); err != nil {
		return false, 0, nil, err
	}
	return fin, op, payload, nil
}

func (ec *EventConn) writeFrame(op byte, p []byte) error {
	ec.wmu.Lock()
	defer ec.wmu.Unlock()
	if ec.closeSent {
		return net.ErrClosed
	}
	if op == wsOpClose {
		ec.closeSent = true
	}
	b := make([]byte, 0, 14+len(p))
	b = append(b, 0x80|op)
	switch n := len(p); {
	case n <= 125:
		b = append(b, 0x80|byte(n))
	case n <= 0xffff:
		b = append(b, 0x80|126)
		b = binary.BigEndian.AppendUint16(b, uint16(n))
	default:
		b = append(b, 0x80|127)
		b = binary.BigEndian.AppendUint64(b, uint64(n))
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	b = append(b, mask[:]...)
	for i, x := range p {
		b = append(b, x^mask[i%4])
	}
	_, err := ec.rwc.Write(b)
	return err
}

func (ec *EventConn) writeClose(code int) error {
	p := binary.BigEndian.AppendUint16(nil, uint16(code))
	return ec.writeFrame(wsOpClose, p)
}

func wsAcceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

func wsHasToken(header, token string) bool {
	for t := range strings.SplitSeq(header, ",") {
		if strings.EqualFold(strings.TrimSpace(t), token) {
			return true
		}
	}
	return false
}
