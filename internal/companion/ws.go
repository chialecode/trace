package companion

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	wsOpCont   = 0x0
	wsOpText   = 0x1
	wsOpBinary = 0x2
	wsOpClose  = 0x8
	wsOpPing   = 0x9
	wsOpPong   = 0xA

	wsMaxReadFrame = 64 << 10
)

// wsConn is a minimal RFC 6455 WebSocket connection over an upgraded net.Conn.
// Supports text frames, Ping/Pong, Close, and concurrent-safe writes with zero external dependencies.
type wsConn struct {
	conn     net.Conn
	br       *bufio.Reader
	isServer bool
	writeMu  sync.Mutex
	closed   chan struct{}
	closeOne sync.Once
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("websocket requires GET")
	}
	if !headerContainsToken(r.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") ||
		strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return nil, errors.New("invalid websocket upgrade headers")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	accept := computeWSAccept(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n" +
		"Cache-Control: no-store\r\n\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := rw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return &wsConn{
		conn:     conn,
		br:       rw.Reader,
		isServer: true,
		closed:   make(chan struct{}),
	}, nil
}

func computeWSAccept(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func headerContainsToken(header, want string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}

func (c *wsConn) Close() error {
	var err error
	c.closeOne.Do(func() {
		close(c.closed)
		err = c.conn.Close()
	})
	return err
}

func (c *wsConn) WriteFrame(opcode byte, payload []byte, timeout time.Duration) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if timeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	} else {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}
	var hdr [14]byte
	hdr[0] = 0x80 | (opcode & 0x0f) // FIN=1
	n := len(payload)
	idx := 2
	var maskBit byte
	if !c.isServer {
		maskBit = 0x80
	}
	switch {
	case n < 126:
		hdr[1] = maskBit | byte(n)
	case n <= 0xffff:
		hdr[1] = maskBit | 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		idx = 4
	default:
		hdr[1] = maskBit | 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		idx = 10
	}
	if !c.isServer {
		var maskKey [4]byte
		if _, err := rand.Read(maskKey[:]); err != nil {
			return err
		}
		copy(hdr[idx:idx+4], maskKey[:])
		idx += 4
		masked := make([]byte, n)
		for i := 0; i < n; i++ {
			masked[i] = payload[i] ^ maskKey[i%4]
		}
		if _, err := c.conn.Write(hdr[:idx]); err != nil {
			return err
		}
		if n > 0 {
			if _, err := c.conn.Write(masked); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := c.conn.Write(hdr[:idx]); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *wsConn) WriteText(payload []byte, timeout time.Duration) error {
	return c.WriteFrame(wsOpText, payload, timeout)
}

func (c *wsConn) WritePing(timeout time.Duration) error {
	return c.WriteFrame(wsOpPing, nil, timeout)
}

func (c *wsConn) WritePong(payload []byte, timeout time.Duration) error {
	return c.WriteFrame(wsOpPong, payload, timeout)
}

func (c *wsConn) WriteClose(code uint16, reason string, timeout time.Duration) error {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	buf := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(buf[:2], code)
	copy(buf[2:], reason)
	return c.WriteFrame(wsOpClose, buf, timeout)
}

// ReadFrame reads a single RFC 6455 frame.
func (c *wsConn) ReadFrame(deadline time.Time) (opcode byte, payload []byte, err error) {
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return 0, nil, err
	}
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode = hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	if c.isServer && !masked {
		return 0, nil, errors.New("websocket client frame must be masked")
	}
	length := uint64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > wsMaxReadFrame {
		return 0, nil, fmt.Errorf("websocket frame too large: %d", length)
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
	}
	return opcode, payload, nil
}
