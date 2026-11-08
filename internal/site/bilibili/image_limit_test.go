package bilibili

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type countedImageBody struct {
	io.Reader
	bytesRead int
	closed    bool
}

func (b *countedImageBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytesRead += n
	return n, err
}

func (b *countedImageBody) Close() error { b.closed = true; return nil }

type imageLimitTransport struct {
	t      *testing.T
	body   *countedImageBody
	length int64
	calls  int
}

func (tr *imageLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.calls++
	if req.Header.Get("Cookie") != "" || req.Header.Get("Referer") != "" {
		tr.t.Error("thumbnail request leaked private headers")
	}
	if req.URL.Path != "/image.png" {
		tr.t.Errorf("image size suffix was retained: %s", req.URL.Path)
	}
	return &http.Response{StatusCode: http.StatusOK, ContentLength: tr.length, Body: tr.body, Header: make(http.Header), Request: req}, nil
}

func TestImageByteLimitRejectsBodiesBeforeOrDuringRead(t *testing.T) {
	for _, test := range []struct {
		name      string
		length    int64
		data      string
		wantBytes int
		wantError bool
	}{
		{"declared oversize", 100, strings.Repeat("x", 100), 0, true},
		{"unknown size", -1, strings.Repeat("x", 100), 17, true},
		{"at limit", 16, strings.Repeat("x", 16), 16, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _, srv := newTestLoggedClient(t, http.NotFoundHandler(), -1)
			body := &countedImageBody{Reader: strings.NewReader(test.data)}
			tr := &imageLimitTransport{t: t, body: body, length: test.length}
			client.mediaClient.Transport = tr
			raw, err := client.FetchImageLimited(context.Background(), srv.URL+"/image.png@200w", 16)
			if (err != nil) != test.wantError || (err != nil && KindOf(err) != ErrKindBadRequest) {
				t.Fatalf("unexpected byte-limit result: err=%v", err)
			}
			if body.bytesRead != test.wantBytes || !body.closed || tr.calls != 1 {
				t.Fatalf("image body was not bounded and closed: read=%d closed=%t calls=%d", body.bytesRead, body.closed, tr.calls)
			}
			if !test.wantError && string(raw) != test.data {
				t.Fatal("valid image bytes changed")
			}
		})
	}
}

func TestInvalidImageLimitDoesNotStartRequest(t *testing.T) {
	client, _, srv := newTestLoggedClient(t, http.NotFoundHandler(), -1)
	tr := &imageLimitTransport{t: t}
	client.mediaClient.Transport = tr
	for _, limit := range []int64{0, -1, 50*1024*1024 + 1} {
		if _, err := client.FetchImageLimited(context.Background(), srv.URL+"/image.png", limit); KindOf(err) != ErrKindBadRequest {
			t.Fatalf("invalid limit %d was accepted: %v", limit, err)
		}
	}
	if tr.calls != 0 {
		t.Fatal("invalid image limit started a network request")
	}
}
