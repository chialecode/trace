package site

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

type stubResolver struct {
	id    ID
	calls int
}

func (s *stubResolver) SiteID() ID { return s.id }

func (s *stubResolver) Resolve(_ context.Context, input string) (ResolveTarget, error) {
	s.calls++
	return ResolveTarget{
		Site:         s.id,
		Kind:         "resolved",
		ID:           input,
		CanonicalURL: input,
	}, nil
}

func TestSiteIDAndAccountKey(t *testing.T) {
	if got := Bilibili.DisplayName(); got != "B 站" {
		t.Fatalf("Bilibili.DisplayName() = %q, want %q", got, "B 站")
	}
	if got := Douyin.DisplayName(); got != "抖音" {
		t.Fatalf("Douyin.DisplayName() = %q, want %q", got, "抖音")
	}
	if id, ok := ParseID(" DOUYIN "); !ok || id != Douyin {
		t.Fatalf("ParseID(DOUYIN) = (%q, %v), want (douyin, true)", id, ok)
	}
	if _, ok := ParseID("tiktok"); ok {
		t.Fatal("ParseID(tiktok) unexpectedly succeeded")
	}

	biliKey, err := FormatAccountKey(Bilibili, "00123456")
	if err != nil || biliKey != "bilibili:123456" {
		t.Fatalf("FormatAccountKey bilibili = (%q, %v), want (bilibili:123456, nil)", biliKey, err)
	}
	if _, err := FormatAccountKey(Bilibili, "0"); err == nil {
		t.Fatal("expected zero bilibili uid to fail")
	}
	if _, err := FormatAccountKey(Bilibili, "MS4wLjABAAAA"); err == nil {
		t.Fatal("expected non-numeric bilibili uid to fail")
	}

	// Preserve case and consecutive double underscores verbatim for Douyin sec_uid.
	secUID := "MS4wLjABAAAA__Synthetic_CaseSensitive__01"
	dyKey, err := FormatAccountKey(Douyin, secUID)
	if err != nil {
		t.Fatalf("FormatAccountKey douyin failed: %v", err)
	}
	parsedSite, parsedUID, err := ParseAccountKey(dyKey)
	if err != nil {
		t.Fatalf("ParseAccountKey(%q) failed: %v", dyKey, err)
	}
	if parsedSite != Douyin || parsedUID != secUID {
		t.Fatalf("ParseAccountKey roundtrip = (%q, %q), want (%q, %q)", parsedSite, parsedUID, Douyin, secUID)
	}

	for _, bad := range []string{
		"",
		"bilibili:",
		":123",
		"douyin:MS4w:extra",
		"douyin:../escape",
		"douyin:has space",
		"DOUYIN:MS4wLjABAAAA_1",
	} {
		if _, _, err := ParseAccountKey(bad); err == nil {
			t.Fatalf("ParseAccountKey(%q) unexpectedly succeeded", bad)
		}
	}

	dir := DouyinAuthorDir(secUID)
	if len(dir) != 12 {
		t.Fatalf("DouyinAuthorDir length = %d, want 12 (%q)", len(dir), dir)
	}
}

func TestErrorClassificationAndAutomationContext(t *testing.T) {
	biliCompat := &Error{
		Kind:    ErrKindRiskControl,
		Code:    -352,
		Message: "risk triggered",
	}
	if got := biliCompat.Error(); got != "bilibili [risk_control code=-352]: risk triggered" {
		t.Fatalf("compat Error() = %q", got)
	}

	dyErr := fmt.Errorf("wrapped: %w", &Error{
		Site:        Douyin,
		Kind:        ErrKindBadRequest,
		Unsupported: true,
		Message:     "暂不支持这种抖音链接",
	})
	if !IsKind(dyErr, ErrKindBadRequest) || KindOf(dyErr) != ErrKindBadRequest {
		t.Fatalf("KindOf = %q, want %q", KindOf(dyErr), ErrKindBadRequest)
	}
	if SiteOf(dyErr) != Douyin {
		t.Fatalf("SiteOf = %q, want %q", SiteOf(dyErr), Douyin)
	}
	if !IsUnsupported(dyErr) {
		t.Fatal("expected IsUnsupported(dyErr) = true")
	}
	if !IsRoundStop(biliCompat) || !IsCircuitBreak(biliCompat) {
		t.Fatal("expected risk_control to stop round and trip circuit")
	}
	if KindOf(errors.New("plain io error")) != ErrKindNetwork {
		t.Fatal("expected plain error to classify as network")
	}

	ctx := WithAutomaticRequests(context.Background(), "live")
	if got := AutomaticRequestSource(ctx); got != "live" {
		t.Fatalf("AutomaticRequestSource = %q, want live", got)
	}
}

func TestMediaTransportSpecStripsCookieAndValidatesHosts(t *testing.T) {
	spec := DouyinLiveTransport("", false)
	if err := spec.ValidateURL("https://pull-flv-f11.douyincdn.com/stage/stream-1.flv"); err != nil {
		t.Fatalf("ValidateURL official CDN failed: %v", err)
	}
	if err := spec.ValidateURL("http://pull-flv-f11.douyincdn.com/stage/stream-1.flv"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected non-HTTPS media URL to be blocked, got %v", err)
	}
	if err := spec.ValidateURL("https://pull-flv-f11.douyincdn.com:8443/stage/stream-1.flv"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected non-443 port media URL to be blocked, got %v", err)
	}
	if err := spec.ValidateURL("https://evil.example.com/stream.flv"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected non-allowlisted domain to be blocked, got %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "https://pull-flv-f11.douyincdn.com/stage/stream-1.flv", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "sessionid=must_never_reach_cdn")
	spec.ApplyHeaders(req)
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie header was not stripped: %q", got)
	}
	if got := req.Header.Get("Referer"); got != "https://live.douyin.com/" {
		t.Fatalf("Referer = %q, want https://live.douyin.com/", got)
	}
	if got := req.Header.Get("Origin"); got != "https://live.douyin.com" {
		t.Fatalf("Origin = %q, want https://live.douyin.com", got)
	}
}

func TestDouyinLiveRedirectHopPolicy(t *testing.T) {
	live := DouyinLiveTransport("", false)
	vod := DouyinVODTransport("", false)
	// The first URL must stay on the allow-list even with public redirects enabled.
	if err := live.ValidateURL("https://edge-7.cdnvendor.example.net/stage/stream-1.flv"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("first URL off the allow-list must be blocked, got %v", err)
	}
	allowed := []string{
		"https://pull-flv-f11.douyincdn.com/stage/stream-1.flv",
		"https://edge-7.cdnvendor.example.net/stage/stream-1.flv?sign=synthetic",
		"https://EDGE-7.CdnVendor.Example.NET:443/stage/stream-1.flv",
		"https://edge-7.cdnvendor.example.net./stage/stream-1.flv",
	}
	for _, u := range allowed {
		if err := live.ValidateRedirectURL(u); err != nil {
			t.Errorf("live redirect hop %q blocked: %v", u, err)
		}
	}
	blocked := []string{
		"http://edge-7.cdnvendor.example.net/stage/stream-1.flv",
		"https://edge-7.cdnvendor.example.net:8443/stage/stream-1.flv",
		"https://203.0.113.7/stage/stream-1.flv",
		"https://[2001:db8::7]/stage/stream-1.flv",
		"https://localhost/stage/stream-1.flv",
		"https://router/stage/stream-1.flv",
		"https://nas.local/stage/stream-1.flv",
		"https://printer.home.arpa/stage/stream-1.flv",
		"https://svc.internal/stage/stream-1.flv",
		"https://user:pass@edge-7.cdnvendor.example.net/stage/stream-1.flv",
		"https://bad_host.example.net/stage/stream-1.flv",
		"https://1.2.3.4.5/stage/stream-1.flv",
	}
	for _, u := range blocked {
		if err := live.ValidateRedirectURL(u); err == nil {
			t.Errorf("live redirect hop %q must be blocked", u)
		}
	}
	// VOD keeps the strict allow-list for every hop.
	if err := vod.ValidateRedirectURL("https://edge-7.cdnvendor.example.net/v.mp4"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("VOD redirect off the allow-list must be blocked, got %v", err)
	}
	if err := BilibiliMediaTransport(false).ValidateRedirectURL("https://edge-7.cdnvendor.example.net/v.m4s"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("Bilibili redirect off the allow-list must be blocked, got %v", err)
	}
}

func TestExtractShareURLAndRegistryDispatch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "douyin share text with oral code and punctuation",
			in:   "7.46 复制打开抖音，看看【合成作者的作品】https://v.douyin.com/iRNBho6/ 08/23 DhO:/ e@o.Pk",
			want: "https://v.douyin.com/iRNBho6/",
		},
		{
			name: "glued oral code suffix on v.douyin.com",
			in:   "https://v.douyin.com/iRNBho6/7.46",
			want: "https://v.douyin.com/iRNBho6/",
		},
		{
			name: "glued chinese and emoji without space",
			in:   "快来看https://www.douyin.com/video/7300000000000000001复制此链接😀！",
			want: "https://www.douyin.com/video/7300000000000000001",
		},
		{
			name: "bare v.douyin.com link",
			in:   "打开 v.douyin.com/AbCdEf9/ 查看视频。",
			want: "https://v.douyin.com/AbCdEf9/",
		},
	}
	for _, tc := range cases {
		got, ok := ExtractShareURL(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("%s: ExtractShareURL(%q) = (%q, %v), want (%q, true)", tc.name, tc.in, got, ok, tc.want)
		}
	}

	biliStub := &stubResolver{id: Bilibili}
	dyStub := &stubResolver{id: Douyin}
	reg := NewRegistry(biliStub, dyStub)

	res, err := reg.Resolve(context.Background(), "7.46 复制打开抖音 https://v.douyin.com/iRNBho6/7.46 快看")
	if err != nil || res.Site != Douyin || res.ID != "https://v.douyin.com/iRNBho6/" {
		t.Fatalf("Registry.Resolve douyin share = (%+v, %v)", res, err)
	}

	res, err = reg.Resolve(context.Background(), "MS4wLjABAAAA__Synthetic_SecUID")
	if err != nil || res.Site != Douyin {
		t.Fatalf("Registry.Resolve sec_uid = (%+v, %v)", res, err)
	}

	res, err = reg.Resolve(context.Background(), "/user/self")
	if err != nil || res.Site != Douyin {
		t.Fatalf("Registry.Resolve /user/self = (%+v, %v)", res, err)
	}

	res, err = reg.Resolve(context.Background(), "【测试】https://www.bilibili.com/video/BV1xx411c7mD。")
	if err != nil || res.Site != Bilibili || res.ID != "https://www.bilibili.com/video/BV1xx411c7mD" {
		t.Fatalf("Registry.Resolve bilibili = (%+v, %v)", res, err)
	}

	beforeBili, beforeDy := biliStub.calls, dyStub.calls
	if _, err := reg.Resolve(context.Background(), "7300000000000000001"); !IsUnsupported(err) || !IsKind(err, ErrKindBadRequest) {
		t.Fatalf("expected bare 19-digit number to be rejected as unsupported bad_request, got %v", err)
	}
	if _, err := reg.Resolve(context.Background(), "https://www.tiktok.com/@synthetic/video/7300000000000000001"); !IsUnsupported(err) {
		t.Fatalf("expected external domain to be rejected as unsupported, got %v", err)
	}
	if biliStub.calls != beforeBili || dyStub.calls != beforeDy {
		t.Fatalf("rejected inputs must make zero resolver calls: bili=%d->%d dy=%d->%d", beforeBili, biliStub.calls, beforeDy, dyStub.calls)
	}
}
