package site

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWrapIdleReadCloserNormalAndTimeout(t *testing.T) {
	// Normal read completes without closing early.
	rc := io.NopCloser(strings.NewReader("hello-media"))
	wrapped := WrapIdleReadCloser(rc, time.Second, nil)
	got, err := io.ReadAll(wrapped)
	if err != nil || string(got) != "hello-media" {
		t.Fatalf("normal ReadAll = (%q, %v)", got, err)
	}
	_ = wrapped.Close()

	// Stalled pipe times out, closes inner reader, and returns custom error.
	pr, pw := io.Pipe()
	defer pw.Close()
	sentinel := &Error{Site: Bilibili, Kind: ErrKindNetwork, Message: "idle timeout"}
	timed := WrapIdleReadCloser(pr, 30*time.Millisecond, func() error { return sentinel })
	buf := make([]byte, 16)
	_, err = timed.Read(buf)
	if !errors.Is(err, sentinel) {
		t.Fatalf("stalled Read err = %v, want %v", err, sentinel)
	}
	// Subsequent read after timeout immediately returns timeout error.
	if _, err2 := timed.Read(buf); !errors.Is(err2, sentinel) {
		t.Fatalf("post-timeout Read err = %v, want %v", err2, sentinel)
	}
}

func TestSanitizeEndpointAndUnwrapURLError(t *testing.T) {
	cases := map[string]string{
		"https://user:pass@api.bilibili.com/x/web-interface/nav?csrf=secret&w_rid=123#frag": "https://api.bilibili.com/x/web-interface/nav",
		"http://127.0.0.1:8080/x/foo?a_bogus=abc":                                           "http://127.0.0.1:8080/x/foo",
		"/x/passport-login/web/cookie/info?csrf=secret_jct":                                 "/x/passport-login/web/cookie/info",
		"": "",
	}
	for in, want := range cases {
		if got := SanitizeEndpoint(in); got != want {
			t.Errorf("SanitizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}

	rawErr := &url.Error{
		Op:  "Get",
		URL: "https://api.bilibili.com/x/foo?csrf=SECRET_JCT&w_rid=SECRET_WRID",
		Err: context.DeadlineExceeded,
	}
	unwrapped := UnwrapURLError(rawErr)
	if !errors.Is(unwrapped, context.DeadlineExceeded) {
		t.Fatalf("UnwrapURLError lost underlying cause: %v", unwrapped)
	}
	if strings.Contains(unwrapped.Error(), "SECRET_JCT") || strings.Contains(unwrapped.Error(), "SECRET_WRID") {
		t.Fatalf("UnwrapURLError leaked URL secrets: %v", unwrapped)
	}
}

func TestLoopbackTransportRejectsRemoteHosts(t *testing.T) {
	var called bool
	rt := LoopbackTransport(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	remoteReq, _ := http.NewRequest(http.MethodGet, "https://api.bilibili.com/x/nav", nil)
	if _, err := rt.RoundTrip(remoteReq); err == nil || called {
		t.Fatalf("expected remote host to be rejected, err=%v called=%v", err, called)
	}
	localReq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:9999/x/nav", nil)
	if resp, err := rt.RoundTrip(localReq); err != nil || !called || resp == nil {
		t.Fatalf("expected loopback host to pass, err=%v called=%v", err, called)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
