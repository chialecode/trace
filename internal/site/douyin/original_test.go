package douyin

import (
	"net/url"
	"testing"
)

func TestOriginalSourceURLs(t *testing.T) {
	v := &rawVideoNode{PlayAddr: &rawURLContainer{URI: "v0200fg10000abcdefghij"}}
	got := originalSourceURLs(DefaultEndpoints().PlayOriginal, v)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	u, err := url.Parse(got[0])
	if err != nil || u.Host != "aweme.snssdk.com" || u.Path != "/aweme/v1/play/" {
		t.Fatalf("unexpected URL %q", got[0])
	}
	q := u.Query()
	if q.Get("video_id") != "v0200fg10000abcdefghij" || q.Get("ratio") != "default" || q.Get("watermark") != "0" {
		t.Fatalf("unexpected query %v", q)
	}
	for _, bad := range []*rawVideoNode{nil, {}, {PlayAddr: &rawURLContainer{URI: "../x?y"}}, {PlayAddr: &rawURLContainer{URI: "short"}}} {
		if got := originalSourceURLs(DefaultEndpoints().PlayOriginal, bad); got != nil {
			t.Fatalf("unusable uri produced %v", got)
		}
	}
	if got := originalSourceURLs("", v); got != nil {
		t.Fatalf("disabled redirector produced %v", got)
	}
	// A fixture API endpoint disables the real redirector.
	if e := (Endpoints{AwemeDetail: "http://127.0.0.1:1/detail"}).withDefaults(); e.PlayOriginal != "" {
		t.Fatalf("fixture endpoints kept PlayOriginal %q", e.PlayOriginal)
	}
}
