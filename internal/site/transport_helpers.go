package site

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WrapIdleReadCloser wraps rc so any single Read that blocks longer than
// timeout closes rc and returns onTimeout() (or context.DeadlineExceeded when
// onTimeout is nil). It spawns zero per-Read goroutines and never races on
// the caller's Read buffer because rc.Read executes on the caller's goroutine.
func WrapIdleReadCloser(rc io.ReadCloser, timeout time.Duration, onTimeout func() error) io.ReadCloser {
	if rc == nil || timeout <= 0 {
		return rc
	}
	return &idleReadCloser{
		rc:        rc,
		timeout:   timeout,
		onTimeout: onTimeout,
	}
}

type idleReadCloser struct {
	rc        io.ReadCloser
	timeout   time.Duration
	onTimeout func() error
	timedOut  atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (r *idleReadCloser) timeoutErr() error {
	if r.onTimeout != nil {
		if err := r.onTimeout(); err != nil {
			return err
		}
	}
	return context.DeadlineExceeded
}

func (r *idleReadCloser) closeInner() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.rc.Close()
	})
	return r.closeErr
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	if r.timedOut.Load() {
		return 0, r.timeoutErr()
	}
	var done atomic.Bool
	timer := time.AfterFunc(r.timeout, func() {
		if done.CompareAndSwap(false, true) {
			r.timedOut.Store(true)
			_ = r.closeInner()
		}
	})
	n, err := r.rc.Read(p)
	if done.CompareAndSwap(false, true) {
		timer.Stop()
		return n, err
	}
	timer.Stop()
	if n > 0 && err == nil {
		return n, nil
	}
	return n, r.timeoutErr()
}

func (r *idleReadCloser) Close() error {
	return r.closeInner()
}

// SanitizeEndpoint returns a safe endpoint label containing only scheme/host/path
// with all query parameters, fragments, and userinfo removed.
func SanitizeEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil {
		if u.Scheme != "" && u.Host != "" {
			return strings.ToLower(u.Scheme) + "://" + u.Host + u.Path
		}
		if u.Host != "" {
			return u.Host + u.Path
		}
		if u.Path != "" {
			return u.Path
		}
	}
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	if at := strings.Index(trimmed, "@"); at >= 0 {
		if slash := strings.Index(trimmed, "/"); slash < 0 || at < slash {
			trimmed = trimmed[at+1:]
		}
	}
	return trimmed
}

// UnwrapURLError strips *url.Error wrappers so request URLs carrying query
// signatures or credentials are never retained in wrapped error chains.
func UnwrapURLError(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	for errors.As(err, &ue) && ue != nil && ue.Err != nil {
		err = ue.Err
		ue = nil
	}
	return err
}

// LoopbackTransport wraps inner and rejects any outbound request whose host is
// not a loopback address. Used by fixture/synthetic clients (AllowLocalhost).
func LoopbackTransport(inner http.RoundTripper) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return loopbackRoundTripper{base: inner}
}

type loopbackRoundTripper struct {
	base http.RoundTripper
}

func (t loopbackRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil || !IsLoopbackHost(r.URL.Hostname()) {
		return nil, errors.New("fixture transport: non-loopback request rejected")
	}
	return t.base.RoundTrip(r)
}

// SplitCleanPath splits a URL path into non-empty path segments.
func SplitCleanPath(p string) []string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
