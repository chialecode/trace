package live

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

const (
	defaultFLVConnectTimeout = 10 * time.Second
	defaultFLVIdleTimeout    = 15 * time.Second
	flvReadBufferSize        = 64 << 10 // 64 KiB buffered reader over FLV stream
)

type activeFLVConn struct {
	body    io.ReadCloser
	remuxer *FLVRemuxer
}

func (c *activeFLVConn) Close() error {
	if c == nil || c.body == nil {
		return nil
	}
	err := c.body.Close()
	c.body = nil
	return err
}

type transportCloseReadCloser struct {
	io.ReadCloser
	tr        *http.Transport
	closeOnce sync.Once
	closeErr  error
}

func (t *transportCloseReadCloser) Close() error {
	t.closeOnce.Do(func() {
		if t.ReadCloser != nil {
			t.closeErr = t.ReadCloser.Close()
		}
		if t.tr != nil {
			t.tr.CloseIdleConnections()
		}
	})
	return t.closeErr
}

func (m *Manager) recordBilibiliFLVPart(
	ctx context.Context,
	sess *recordSession,
	urls []string,
	connPtr **activeFLVConn,
	partPath string,
) (int64, error) {
	allowLocal := m.client != nil && m.client.AllowLocalhost()
	spec := site.BilibiliMediaTransport(allowLocal)
	spec.Referer = "https://live.bilibili.com/"
	spec.Origin = "https://live.bilibili.com"
	return m.recordFLVPartWithTransport(ctx, sess, spec, urls, connPtr, partPath)
}

func (m *Manager) recordFLVPartWithTransport(
	ctx context.Context,
	sess *recordSession,
	transport site.MediaTransportSpec,
	urls []string,
	connPtr **activeFLVConn,
	partPath string,
) (written int64, result error) {
	partBase := filepath.Base(partPath)
	if *connPtr == nil {
		m.mu.Lock()
		idleDur := m.flvIdleTimeout
		m.mu.Unlock()
		body, err := OpenHTTPFLVStream(ctx, transport, urls, idleDur)
		if err != nil {
			return 0, err
		}
		m.markSessionReconnected(sess, m.now(), true, partBase)
		remuxer := NewFLVRemuxer(bufio.NewReaderSize(body, flvReadBufferSize), FLVRemuxOptions{})
		*connPtr = &activeFLVConn{
			body:    body,
			remuxer: remuxer,
		}
	}

	if err := os.MkdirAll(filepath.Dir(partPath), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		if sErr := f.Sync(); sErr != nil && result == nil {
			result = sErr
		}
		if cErr := f.Close(); cErr != nil && result == nil {
			result = cErr
		}
	}()

	var prevBytes int64
	var prevDur float64
	prevCheckAt := m.now()
	cp, wErr := (*connPtr).remuxer.WritePart(ctx, f, func(cp FLVCheckpoint) error {
		if err := m.checkDiskSpace(sess.itemDir, transport.Site); err != nil {
			return err
		}
		checkNow := m.now()
		delta := cp.BytesWritten - prevBytes
		deltaDur := cp.DurationSec - prevDur
		if delta > 0 {
			m.observeTransferSpeed(sess, prevCheckAt, checkNow, delta, deltaDur, partBase)
			prevBytes = cp.BytesWritten
			written = cp.BytesWritten
			m.addSessionBytes(sess, delta)
		}
		prevCheckAt = checkNow
		if cp.DurationSec > prevDur {
			prevDur = cp.DurationSec
		}
		sess.segmentDuration = cp.DurationSec
		m.mu.Lock()
		sess.wroteMedia = true
		// Keep HTTP-FLV GOP chunk numbering isolated from HLS MEDIA-SEQUENCE tracking.
		sess.lastSequence = 0
		sess.hasSequence = false
		sess.lastSequenceAt = time.Time{}
		if cp.Codec != "" {
			sess.codec = cp.Codec
		}
		m.mu.Unlock()

		m.commitSessionPart(sess, cp.BytesWritten, cp.DurationSec)
		return m.saveCheckpoint(sess, func(meta *library.ItemMetadata) {
			if cp.Codec != "" {
				meta.Live.Codec = cp.Codec
			}
			meta.Live.LastSequence = 0
			meta.Live.HasSequence = false
			meta.Live.PendingFile = partBase
			meta.Live.PendingBytes = cp.BytesWritten
			meta.Live.PendingDuration = cp.DurationSec
		})
	})
	if cp.BytesWritten > written {
		m.addSessionBytes(sess, cp.BytesWritten-written)
		written = cp.BytesWritten
	}
	return written, wErr
}

// OpenHTTPFLVStream opens an HTTP-FLV live stream over HTTPS (or localhost in tests),
// enforcing a 10s connect/header timeout, per-read idle timeout, <=5 redirect hops (ValidateRedirectURL),
// Referer/Origin headers with Cookie stripped, and rejecting HTML/JSON disguised responses.
func OpenHTTPFLVStream(ctx context.Context, transport site.MediaTransportSpec, urls []string, idleTimeout time.Duration) (io.ReadCloser, error) {
	if idleTimeout <= 0 {
		idleTimeout = defaultFLVIdleTimeout
	}
	var cleanURLs []string
	var lastValidateErr error
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if err := transport.ValidateURL(u); err != nil {
			lastValidateErr = err
			continue
		}
		cleanURLs = append(cleanURLs, u)
	}
	if len(cleanURLs) == 0 {
		if lastValidateErr != nil {
			return nil, lastValidateErr
		}
		return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindUnavailable, Message: "直播拉流地址列表为空"}
	}

	httpClient, tr := newLiveTransportClientWithTransport(transport, defaultFLVConnectTimeout)
	var lastErr error
	for _, targetURL := range cleanURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			lastErr = &site.Error{
				Site:    transport.Site,
				Kind:    site.ErrKindNetwork,
				Message: "创建直播流请求失败",
				Err:     site.UnwrapURLError(err),
			}
			continue
		}
		transport.ApplyHeaders(req)

		resp, err := httpClient.Do(req)
		if err != nil {
			var se *site.Error
			if errors.As(err, &se) {
				lastErr = se
				if site.IsRoundStop(se) || se.Kind == site.ErrKindBlocked {
					tr.CloseIdleConnections()
					return nil, se
				}
				continue
			}
			lastErr = &site.Error{
				Site:    transport.Site,
				Kind:    site.ErrKindNetwork,
				Message: "连接直播流节点失败",
				Err:     site.UnwrapURLError(err),
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			se := mapLiveHTTPStatus(transport.Site, resp.StatusCode)
			lastErr = se
			if site.IsRoundStop(se) {
				tr.CloseIdleConnections()
				return nil, se
			}
			continue
		}

		ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if badErr := checkLiveFLVContentType(transport.Site, ct); badErr != nil {
			_ = resp.Body.Close()
			tr.CloseIdleConnections()
			return nil, badErr
		}

		timeoutSec := max(1, int(idleTimeout.Seconds()))
		idleBody := site.WrapIdleReadCloser(resp.Body, idleTimeout, func() error {
			return &site.Error{
				Site:    transport.Site,
				Kind:    site.ErrKindNetwork,
				Message: fmt.Sprintf("直播流超过 %d 秒无数据返回", timeoutSec),
			}
		})
		return &transportCloseReadCloser{ReadCloser: idleBody, tr: tr}, nil
	}
	tr.CloseIdleConnections()
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "无法连接任何直播流节点"}
}

func newLiveTransportClient(spec site.MediaTransportSpec, connectTimeout time.Duration) *http.Client {
	c, _ := newLiveTransportClientWithTransport(spec, connectTimeout)
	return c
}

func newLiveTransportClientWithTransport(spec site.MediaTransportSpec, connectTimeout time.Duration) (*http.Client, *http.Transport) {
	if connectTimeout <= 0 {
		connectTimeout = defaultFLVConnectTimeout
	}
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	guard := site.NewGuardedMediaDialer(spec.Site, spec.AllowLocalhost, dialer, "已拦截内网或回环直播流节点地址")
	tr := &http.Transport{
		Proxy:                 guard.Proxy,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: connectTimeout,
		DialContext:           guard.DialContext,
	}
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return &site.Error{
					Site:    spec.Site,
					Kind:    site.ErrKindBlocked,
					Message: "直播流重定向超过 5 跳上限",
				}
			}
			if err := spec.ValidateRedirectURL(req.URL.String()); err != nil {
				return err
			}
			spec.ApplyHeaders(req)
			return nil
		},
	}
	return client, tr
}

func mapLiveHTTPStatus(siteID site.ID, status int) *site.Error {
	switch status {
	case http.StatusUnauthorized:
		return &site.Error{Site: siteID, Kind: site.ErrKindAuthRequired, HTTPStatus: status, Message: "直播拉流需要有效登录凭证 (401)"}
	case http.StatusPreconditionFailed:
		return &site.Error{Site: siteID, Kind: site.ErrKindRiskControl, HTTPStatus: status, Message: "直播请求触发风控 (412)，已停止录制"}
	case http.StatusTooManyRequests:
		return &site.Error{Site: siteID, Kind: site.ErrKindRateLimited, HTTPStatus: status, Message: "直播请求触发频率限制 (429)，已停止录制"}
	case http.StatusForbidden:
		return &site.Error{Site: siteID, Kind: site.ErrKindForbidden, HTTPStatus: status, Message: "直播流地址已过期或被拒绝 (403)"}
	case http.StatusNotFound, http.StatusGone:
		return &site.Error{Site: siteID, Kind: site.ErrKindUnavailable, HTTPStatus: status, Message: fmt.Sprintf("直播流节点不可用 (%d)", status)}
	default:
		return &site.Error{Site: siteID, Kind: site.ErrKindNetwork, HTTPStatus: status, Message: fmt.Sprintf("直播流 HTTP 状态异常 (%d)", status)}
	}
}

func checkLiveFLVContentType(siteID site.ID, rawCT string) error {
	if rawCT == "" {
		return nil
	}
	mt, _, err := mime.ParseMediaType(rawCT)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(rawCT, ";", 2)[0]))
	}
	mt = strings.ToLower(strings.TrimSpace(mt))
	switch mt {
	case "text/html", "application/xhtml+xml", "application/json", "text/json", "text/plain", "application/vnd.apple.mpegurl", "application/x-mpegurl":
		return &site.Error{
			Site:    siteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: fmt.Sprintf("直播流返回了非 FLV 响应类型 (%s)", mt),
		}
	}
	return nil
}
