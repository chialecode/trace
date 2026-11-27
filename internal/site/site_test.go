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

func TestTypedNilErrorHelpersReturnZeroValues(t *testing.T) {
	var se *Error
	inputs := []struct {
		name string
		err  error
	}{
		{name: "direct typed-nil *site.Error", err: se},
		{name: "wrapped typed-nil *site.Error", err: fmt.Errorf("wrapped: %w", se)},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			if got := KindOf(tc.err); got != "" {
				t.Fatalf("KindOf() = %q, want empty", got)
			}
			if IsKind(tc.err, "") || IsKind(tc.err, ErrKindNetwork) || IsKind(tc.err, ErrKindRiskControl) {
				t.Fatal("IsKind() unexpectedly returned true for typed-nil *Error")
			}
			if got := SiteOf(tc.err); got != "" {
				t.Fatalf("SiteOf() = %q, want empty", got)
			}
			if IsUnsupported(tc.err) {
				t.Fatal("IsUnsupported() unexpectedly returned true for typed-nil *Error")
			}
			if IsRoundStop(tc.err) {
				t.Fatal("IsRoundStop() unexpectedly returned true for typed-nil *Error")
			}
			if IsCircuitBreak(tc.err) {
				t.Fatal("IsCircuitBreak() unexpectedly returned true for typed-nil *Error")
			}
		})
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

	biliLive := BilibiliLiveTransport(false)
	if err := biliLive.ValidateURL("https://cn-jssz-cm-02-08.bilivideo.com/live-bvc/index.m3u8"); err != nil {
		t.Fatalf("Bilibili live official host rejected: %v", err)
	}
	if err := biliLive.ValidateURL("https://edge-1.h2.smtcdns.net/live-bvc/index.m3u8"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("Bilibili live initial URL off allow-list must be blocked, got %v", err)
	}
	if err := biliLive.ValidateRedirectURL("https://edge-1.h2.smtcdns.net/live-bvc/index.m3u8?e=1"); err != nil {
		t.Fatalf("Bilibili live scheduled vendor CDN host blocked: %v", err)
	}
	for _, u := range blocked {
		if err := biliLive.ValidateRedirectURL(u); err == nil {
			t.Errorf("Bilibili live scheduled/redirect URL %q must be blocked", u)
		}
	}
	req, err := http.NewRequest(http.MethodGet, "https://edge-1.h2.smtcdns.net/live-bvc/index.m3u8", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "SESSDATA=must_never_reach_cdn")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Csrf-Token", "csrf_secret")
	biliLive.ApplyHeaders(req)
	if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("X-Csrf-Token") != "" {
		t.Fatalf("BilibiliLiveTransport.ApplyHeaders leaked credentials: %+v", req.Header)
	}
	if req.Header.Get("Referer") != "https://live.bilibili.com/" || req.Header.Get("Origin") != "https://live.bilibili.com" {
		t.Fatalf("BilibiliLiveTransport headers = Referer:%q Origin:%q", req.Header.Get("Referer"), req.Header.Get("Origin"))
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
		{
			name: "expanding unicode before url does not panic",
			in:   "İİİİİİİİİİİİİİİİ https://www.bilibili.com/video/BV1xx411c7mD",
			want: "https://www.bilibili.com/video/BV1xx411c7mD",
		},
		{
			name: "bare acg.tv and m.douyin.com links",
			in:   "查看 acg.tv/av170001",
			want: "https://acg.tv/av170001",
		},
		{
			name: "bare m.douyin.com link",
			in:   "打开 m.douyin.com/share/video/7300000000000000001",
			want: "https://m.douyin.com/share/video/7300000000000000001",
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

func TestWeiboSiteIDAndAccountKey(t *testing.T) {
	all := All()
	if len(all) != 4 || all[0] != Bilibili || all[1] != Douyin || all[2] != Weibo || all[3] != X {
		t.Fatalf("All() = %v, want [bilibili douyin weibo x]", all)
	}
	if !Weibo.Valid() {
		t.Fatal("Weibo.Valid() = false, want true")
	}
	if got := Weibo.DisplayName(); got != "微博" {
		t.Fatalf("Weibo.DisplayName() = %q, want %q", got, "微博")
	}
	if got := DisplayName("weibo"); got != "微博" {
		t.Fatalf("DisplayName(weibo) = %q, want %q", got, "微博")
	}
	if id, ok := ParseID(" WEIBO "); !ok || id != Weibo {
		t.Fatalf("ParseID(WEIBO) = (%q, %v), want (weibo, true)", id, ok)
	}
	if ID("unknown").Valid() {
		t.Fatal("unknown ID unexpectedly valid")
	}
	if _, err := NormalizeUID(ID(""), "1000000001"); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("NormalizeUID(empty) err = %v, want ErrUnknownSite", err)
	}
	if _, err := NormalizeUID(ID("unknown"), "1000000001"); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("NormalizeUID(unknown) err = %v, want ErrUnknownSite", err)
	}
	if _, err := FormatAccountKey(ID("unknown"), "1000000001"); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("FormatAccountKey(unknown) err = %v, want ErrUnknownSite", err)
	}

	validUIDs := map[string]string{
		"12345":               "12345",
		"0012345":             "12345",
		"001000000001":        "1000000001",
		"1000000001":          "1000000001",
		"9999999999999999999": "9999999999999999999",
	}
	for raw, want := range validUIDs {
		got, err := NormalizeUID(Weibo, raw)
		if err != nil || got != want {
			t.Fatalf("NormalizeUID(Weibo, %q) = (%q, %v), want (%q, nil)", raw, got, err, want)
		}
	}

	invalidUIDs := []string{
		"",
		"0",
		"00000",
		"1234",
		"001234",
		"10000000000000000000", // 20 digits
		"-1000000001",
		"+1000000001",
		"10000a0001",
		"10000 0001",
	}
	for _, bad := range invalidUIDs {
		if got, err := NormalizeUID(Weibo, bad); !errors.Is(err, ErrInvalidUID) {
			t.Fatalf("NormalizeUID(Weibo, %q) = (%q, %v), want ErrInvalidUID", bad, got, err)
		}
	}

	wbKey, err := FormatAccountKey(Weibo, "001000000001")
	if err != nil || wbKey != "weibo:1000000001" {
		t.Fatalf("FormatAccountKey(Weibo) = (%q, %v), want (weibo:1000000001, nil)", wbKey, err)
	}
	if got := AccountKey(Weibo, "001000000001"); got != "weibo:1000000001" {
		t.Fatalf("AccountKey(Weibo) = %q, want weibo:1000000001", got)
	}
	parsedSite, parsedUID, err := ParseAccountKey(wbKey)
	if err != nil || parsedSite != Weibo || parsedUID != "1000000001" {
		t.Fatalf("ParseAccountKey(%q) = (%q, %q, %v), want (weibo, 1000000001, nil)", wbKey, parsedSite, parsedUID, err)
	}
	for _, bad := range []string{"WEIBO:1000000001", "weibo:1234", "weibo:", "unknown:1000000001"} {
		if _, _, err := ParseAccountKey(bad); err == nil {
			t.Fatalf("ParseAccountKey(%q) unexpectedly succeeded", bad)
		}
	}

	wbErr := &Error{Site: Weibo, Kind: ErrKindRiskControl, HTTPStatus: 432, Message: "风控拦截"}
	if got := wbErr.Error(); got != "weibo [risk_control http=432]: 风控拦截" {
		t.Fatalf("Weibo Error() = %q", got)
	}
	if SiteOf(wbErr) != Weibo {
		t.Fatalf("SiteOf(wbErr) = %q, want weibo", SiteOf(wbErr))
	}
}

func TestWeiboRegistryDispatchAndShareTextPriority(t *testing.T) {
	weiboHosts := []string{
		"weibo.com",
		"www.weibo.com",
		"m.weibo.cn",
		"weibo.cn",
		"video.weibo.com",
		"t.cn",
		"share.api.weibo.cn",
		"weibointl.api.weibo.com",
		"live.weibo.com",
		"s.weibo.com",
		"card.weibo.com",
	}
	for _, host := range weiboHosts {
		if got, ok := DetectHostSite(host); !ok || got != Weibo {
			t.Fatalf("DetectHostSite(%q) = (%q, %v), want (weibo, true)", host, got, ok)
		}
	}
	for _, host := range []string{"test.cn", "evilweibo.com", "weibo.com.evil.net", ""} {
		if got, ok := DetectHostSite(host); ok {
			t.Fatalf("DetectHostSite(%q) = (%q, true), want false", host, got)
		}
	}

	extractCases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "weibo share text with explicit m.weibo.cn",
			in:   "【合成标题】分享@合成示例用户 的微博 https://m.weibo.cn/status/5000000000000001，快来看看",
			want: "https://m.weibo.cn/status/5000000000000001",
		},
		{
			name: "prefer explicit in-site weibo URL over earlier t.cn",
			in:   "正文外链 http://t.cn/A6SYNTHETIC 分享@合成示例用户 的微博 https://m.weibo.cn/status/5000000000000001。",
			want: "https://m.weibo.cn/status/5000000000000001",
		},
		{
			name: "prefer bare in-site weibo URL over earlier http t.cn",
			in:   "先看 http://t.cn/A6SYNTHETIC 再看 weibo.com/1000000001/O00000001！",
			want: "https://weibo.com/1000000001/O00000001",
		},
		{
			name: "prefer glued bare weibo URL over glued bare t.cn",
			in:   "看微博t.cn/A6SYNTHETIC，原微博weibo.com/1000000001/O00000001！",
			want: "https://weibo.com/1000000001/O00000001",
		},
		{
			name: "keep explicit weibo URL when t.cn follows",
			in:   "原博 https://www.weibo.com/1000000001/O00000001 附 http://t.cn/A6SYNTHETIC",
			want: "https://www.weibo.com/1000000001/O00000001",
		},
		{
			name: "fallback to t.cn when no explicit in-site weibo URL exists",
			in:   "分享短链 http://t.cn/A6SYNTHETIC。",
			want: "http://t.cn/A6SYNTHETIC",
		},
		{
			name: "bare video.weibo.com link",
			in:   "查看 video.weibo.com/show?fid=1034:5000000000000001",
			want: "https://video.weibo.com/show?fid=1034:5000000000000001",
		},
		{
			name: "bare share.api.weibo.cn link",
			in:   "share.api.weibo.cn/share/100000001,5000000000000001.html",
			want: "https://share.api.weibo.cn/share/100000001,5000000000000001.html",
		},
		{
			name: "bare weibointl.api.weibo.com link",
			in:   "weibointl.api.weibo.com/share/100000001.html?weibo_id=5000000000000001",
			want: "https://weibointl.api.weibo.com/share/100000001.html?weibo_id=5000000000000001",
		},
		{
			name: "bare live.weibo.com link",
			in:   "live.weibo.com/show?id=1022:2321325000000000000001",
			want: "https://live.weibo.com/show?id=1022:2321325000000000000001",
		},
		{
			name: "inner weibo url in external query stays external",
			in:   "https://evil.example/jump?u=https://weibo.com/1000000001/O00000001",
			want: "https://evil.example/jump?u=https://weibo.com/1000000001/O00000001",
		},
	}
	for _, tc := range extractCases {
		got, ok := ExtractShareURL(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("%s: ExtractShareURL(%q) = (%q, %v), want (%q, true)", tc.name, tc.in, got, ok, tc.want)
		}
	}
	if got, ok := ExtractShareURL("打开 test.cn/A6SYNTHETIC 查看"); ok {
		t.Fatalf("test.cn must not match bare t.cn domain, got %q", got)
	}

	biliStub := &stubResolver{id: Bilibili}
	dyStub := &stubResolver{id: Douyin}
	wbStub := &stubResolver{id: Weibo}
	reg := NewRegistry(biliStub, dyStub, wbStub)

	res, err := reg.Resolve(context.Background(), "外链 http://t.cn/A6SYNTHETIC 分享 https://weibo.com/1000000001/O00000001")
	if err != nil || res.Site != Weibo || res.ID != "https://weibo.com/1000000001/O00000001" {
		t.Fatalf("Registry.Resolve weibo share = (%+v, %v)", res, err)
	}

	res, err = reg.Resolve(context.Background(), "http://t.cn/A6SYNTHETIC")
	if err != nil || res.Site != Weibo || res.ID != "http://t.cn/A6SYNTHETIC" {
		t.Fatalf("Registry.Resolve t.cn = (%+v, %v)", res, err)
	}

	// Pure <19 digit numbers must still route to Bilibili (never Weibo).
	beforeWb := wbStub.calls
	for _, num := range []string{"1000000001", "5000000000000001"} {
		res, err := reg.Resolve(context.Background(), num)
		if err != nil || res.Site != Bilibili || res.ID != num {
			t.Fatalf("Registry.Resolve(%q) = (%+v, %v), want Bilibili", num, res, err)
		}
	}
	if wbStub.calls != beforeWb {
		t.Fatalf("pure numeric inputs routed to Weibo: calls %d -> %d", beforeWb, wbStub.calls)
	}

	if _, err := reg.Resolve(context.Background(), "https://evil.example/jump?u=https://weibo.com/1000000001/O00000001"); !IsUnsupported(err) {
		t.Fatalf("external host with embedded weibo query must be rejected as unsupported, got %v", err)
	}
	if wbStub.calls != beforeWb {
		t.Fatalf("rejected external host must make zero Weibo resolver calls")
	}
}

func TestWeiboMediaTransportAndLivePhotoInitialHop(t *testing.T) {
	hosts := DefaultWeiboMediaHosts()
	if len(hosts) != 2 || hosts[0] != "sinaimg.cn" || hosts[1] != "weibocdn.com" {
		t.Fatalf("DefaultWeiboMediaHosts() = %v, want [sinaimg.cn weibocdn.com]", hosts)
	}

	spec := WeiboMediaTransport("", false)
	if spec.Site != Weibo || spec.Referer != "https://weibo.com/" || spec.Origin != "" || spec.PublicRedirects {
		t.Fatalf("unexpected WeiboMediaTransport spec: %+v", spec)
	}
	for _, okURL := range []string{
		"https://wx1.sinaimg.cn/large/001SynThPic01ly1synthetic00001.jpg",
		"https://tvax2.sinaimg.cn/crop.0.0.100.100/synthetic.jpg",
		"https://f.video.weibocdn.com/o0/synthetic.mp4?Expires=1&ssig=syn&KID=unistore,video",
		"https://livephoto.us.sinaimg.cn/synthetic.mov",
	} {
		if err := spec.ValidateURL(okURL); err != nil {
			t.Fatalf("WeiboMediaTransport.ValidateURL(%q) failed: %v", okURL, err)
		}
		if err := spec.ValidateRedirectURL(okURL); err != nil {
			t.Fatalf("WeiboMediaTransport.ValidateRedirectURL(%q) failed: %v", okURL, err)
		}
	}
	for _, badURL := range []string{
		"http://wx1.sinaimg.cn/large/synthetic.jpg",
		"https://wx1.sinaimg.cn:8443/large/synthetic.jpg",
		"https://weibo.com/ajax/statuses/show?id=O00000001",
		"https://video.weibo.com/media/play?livephoto=synthetic",
		"https://edge.example.net/synthetic.mp4",
	} {
		if err := spec.ValidateURL(badURL); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("WeiboMediaTransport.ValidateURL(%q) = %v, want blocked", badURL, err)
		}
		if err := spec.ValidateRedirectURL(badURL); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("WeiboMediaTransport.ValidateRedirectURL(%q) = %v, want blocked", badURL, err)
		}
	}

	req, err := http.NewRequest(http.MethodGet, "https://wx1.sinaimg.cn/large/synthetic.jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "SUB=must_never_reach_cdn; XSRF-TOKEN=secret")
	spec.ApplyHeaders(req)
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("WeiboMediaTransport.ApplyHeaders did not strip Cookie: %q", got)
	}
	if got := req.Header.Get("Referer"); got != "https://weibo.com/" {
		t.Fatalf("Referer = %q, want https://weibo.com/", got)
	}
	if got := req.Header.Get("Origin"); got != "" {
		t.Fatalf("Origin = %q, want empty", got)
	}

	lp := WeiboLivePhotoMediaTransport("", false)
	for _, okInitial := range []string{
		"https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2Fsyn.mov",
		"https://VIDEO.WEIBO.COM:443/media/play?livephoto=syn",
		"https://livephoto.us.sinaimg.cn/synthetic.mov",
		"https://f.video.weibocdn.com/synthetic.mp4",
	} {
		if err := lp.ValidateURL(okInitial); err != nil {
			t.Fatalf("WeiboLivePhotoMediaTransport.ValidateURL(%q) failed: %v", okInitial, err)
		}
	}
	for _, badInitial := range []string{
		"http://video.weibo.com/media/play?livephoto=syn",
		"https://video.weibo.com:8443/media/play?livephoto=syn",
		"https://video.weibo.com/show?fid=1034:5000000000000001",
		"https://video.weibo.com/media/play/extra",
		"https://video.weibo.com/media/play/../show",
		"https://video.weibo.com/media/%70lay?livephoto=syn",
		"https://sub.video.weibo.com/media/play?livephoto=syn",
		"https://weibo.com/media/play?livephoto=syn",
		"https://user:pass@video.weibo.com/media/play?livephoto=syn",
	} {
		if err := lp.ValidateURL(badInitial); err == nil {
			t.Fatalf("WeiboLivePhotoMediaTransport.ValidateURL(%q) must fail", badInitial)
		}
	}
	if err := lp.ValidateRedirectURL("https://livephoto.us.sinaimg.cn/synthetic.mov"); err != nil {
		t.Fatalf("LivePhoto redirect to sinaimg.cn failed: %v", err)
	}
	if err := lp.ValidateRedirectURL("https://f.video.weibocdn.com/synthetic.mp4"); err != nil {
		t.Fatalf("LivePhoto redirect to weibocdn.com failed: %v", err)
	}
	for _, badHop := range []string{
		"https://video.weibo.com/media/play?livephoto=loop",
		"https://weibo.com/newlogin",
		"https://edge.example.net/clip.mov",
	} {
		if err := lp.ValidateRedirectURL(badHop); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("LivePhoto redirect to %q = %v, want blocked", badHop, err)
		}
	}

	if !IsWeiboLivePhotoInitialURL("https://video.weibo.com/media/play?livephoto=syn") {
		t.Fatal("IsWeiboLivePhotoInitialURL returned false for valid URL")
	}
	if IsWeiboLivePhotoInitialURL("https://video.weibo.com/show?fid=1034:5000000000000001") {
		t.Fatal("IsWeiboLivePhotoInitialURL returned true for /show")
	}
	if IsWeiboLivePhotoInitialURL("https://livephoto.us.sinaimg.cn/synthetic.mov") {
		t.Fatal("IsWeiboLivePhotoInitialURL returned true for sinaimg.cn")
	}
}

func TestXSiteIDAndAccountKey(t *testing.T) {
	all := All()
	if len(all) != 4 || all[0] != Bilibili || all[1] != Douyin || all[2] != Weibo || all[3] != X {
		t.Fatalf("All() = %v, want [bilibili douyin weibo x]", all)
	}
	if !X.Valid() {
		t.Fatal("X.Valid() = false, want true")
	}
	if got := X.DisplayName(); got != "X" {
		t.Fatalf("X.DisplayName() = %q, want %q", got, "X")
	}
	if got := DisplayName("x"); got != "X" {
		t.Fatalf("DisplayName(x) = %q, want %q", got, "X")
	}
	if id, ok := ParseID(" X "); !ok || id != X {
		t.Fatalf("ParseID(X) = (%q, %v), want (x, true)", id, ok)
	}

	validUIDs := map[string]string{
		"1":                     "1",
		"12":                    "12",
		"00012":                 "12",
		"1000000000000000001":   "1000000000000000001",
		"001000000000000000001": "1000000000000000001",
		"9223372036854775807":   "9223372036854775807",
		"18446744073709551615":  "18446744073709551615", // math.MaxUint64
	}
	for raw, want := range validUIDs {
		got, err := NormalizeUID(X, raw)
		if err != nil || got != want {
			t.Fatalf("NormalizeUID(X, %q) = (%q, %v), want (%q, nil)", raw, got, err, want)
		}
	}

	invalidUIDs := []string{
		"",
		"   ",
		"0",
		"00000",
		"18446744073709551616",  // math.MaxUint64 + 1
		"99999999999999999999",  // 20 digits > uint64
		"100000000000000000000", // 21 digits
		"-1000000000000000001",
		"+1000000000000000001",
		"100000a000001",
		"100000 000001",
		"@example_user",
		"example_user",
	}
	for _, bad := range invalidUIDs {
		if got, err := NormalizeUID(X, bad); !errors.Is(err, ErrInvalidUID) {
			t.Fatalf("NormalizeUID(X, %q) = (%q, %v), want ErrInvalidUID", bad, got, err)
		}
	}

	xKey, err := FormatAccountKey(X, "001000000000000000001")
	if err != nil || xKey != "x:1000000000000000001" {
		t.Fatalf("FormatAccountKey(X) = (%q, %v), want (x:1000000000000000001, nil)", xKey, err)
	}
	if got := AccountKey(X, "001000000000000000001"); got != "x:1000000000000000001" {
		t.Fatalf("AccountKey(X) = %q, want x:1000000000000000001", got)
	}
	parsedSite, parsedUID, err := ParseAccountKey(xKey)
	if err != nil || parsedSite != X || parsedUID != "1000000000000000001" {
		t.Fatalf("ParseAccountKey(%q) = (%q, %q, %v), want (x, 1000000000000000001, nil)", xKey, parsedSite, parsedUID, err)
	}
	for _, bad := range []string{
		"X:1000000000000000001",
		"x:0",
		"x:",
		"x:18446744073709551616",
		"x:example_user",
	} {
		if _, _, err := ParseAccountKey(bad); err == nil {
			t.Fatalf("ParseAccountKey(%q) unexpectedly succeeded", bad)
		}
	}

	xErr := &Error{Site: X, Kind: ErrKindRateLimited, HTTPStatus: 429, Message: "X 限流"}
	if got := xErr.Error(); got != "x [rate_limited http=429]: X 限流" {
		t.Fatalf("X Error() = %q", got)
	}
	if SiteOf(xErr) != X {
		t.Fatalf("SiteOf(xErr) = %q, want x", SiteOf(xErr))
	}
}

func TestXRegistryDispatchAndShareTextPriority(t *testing.T) {
	xHosts := []string{
		"x.com",
		"www.x.com",
		"mobile.x.com",
		"m.x.com",
		"twitter.com",
		"www.twitter.com",
		"mobile.twitter.com",
		"m.twitter.com",
		"t.co",
		"fxtwitter.com",
		"www.fxtwitter.com",
		"d.fxtwitter.com",
		"i.fxtwitter.com",
		"g.fxtwitter.com",
		"m.fxtwitter.com",
		"fixupx.com",
		"d.fixupx.com",
		"vxtwitter.com",
		"i.vxtwitter.com",
		"fixvx.com",
		"g.fixvx.com",
		"twittpr.com",
		"m.twittpr.com",
		"nitter.net",
		"www.nitter.net",
		"xcancel.com",
		"www.xcancel.com",
		"periscope.tv",
		"pscp.tv",
	}
	for _, host := range xHosts {
		if got, ok := DetectHostSite(host); !ok || got != X {
			t.Fatalf("DetectHostSite(%q) = (%q, %v), want (x, true)", host, got, ok)
		}
	}
	for _, host := range []string{
		"box.com",
		"fix.com",
		"myx.com",
		"not-t.co",
		"t.com",
		"x.com.evil.net",
		"twitter.com.evil.net",
		"eviltwitter.com",
		"evilfxtwitter.com",
		"pbs.twimg.com",
		"video.twimg.com",
		"abs.twimg.com",
	} {
		if got, ok := DetectHostSite(host); ok {
			t.Fatalf("DetectHostSite(%q) = (%q, true), want false", host, got)
		}
	}

	extractCases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "x share text with explicit status link",
			in:   "看看 @example_user 的帖子 https://x.com/example_user/status/1000000000000000009?s=20，很有意思",
			want: "https://x.com/example_user/status/1000000000000000009?s=20",
		},
		{
			name: "prefer explicit x.com link over earlier t.co",
			in:   "正文外链 https://t.co/Synthetic01 原帖 https://x.com/example_user/status/1000000000000000009。",
			want: "https://x.com/example_user/status/1000000000000000009",
		},
		{
			name: "prefer explicit twitter.com link over earlier http t.co",
			in:   "短链 http://t.co/Synthetic01 见 https://mobile.twitter.com/example_user/status/1000000000000000009!",
			want: "https://mobile.twitter.com/example_user/status/1000000000000000009",
		},
		{
			name: "prefer mirror link over earlier t.co",
			in:   "外链 https://t.co/Synthetic01 镜像 https://d.fxtwitter.com/example_user/status/1000000000000000009",
			want: "https://d.fxtwitter.com/example_user/status/1000000000000000009",
		},
		{
			name: "prefer bare x.com over earlier t.co",
			in:   "先看 https://t.co/Synthetic01 再看 x.com/example_user/status/1000000000000000009！",
			want: "https://x.com/example_user/status/1000000000000000009",
		},
		{
			name: "prefer glued bare x.com over glued bare t.co",
			in:   "看帖t.co/Synthetic01，原帖x.com/example_user/status/1000000000000000009！",
			want: "https://x.com/example_user/status/1000000000000000009",
		},
		{
			name: "prefer bare mirror over bare t.co",
			in:   "先看 t.co/Synthetic01 再看 xcancel.com/example_user/status/1000000000000000009",
			want: "https://xcancel.com/example_user/status/1000000000000000009",
		},
		{
			name: "keep explicit x.com link when t.co follows",
			in:   "原帖 https://x.com/example_user/status/1000000000000000009 附 https://t.co/Synthetic01",
			want: "https://x.com/example_user/status/1000000000000000009",
		},
		{
			name: "keep mirror link when t.co follows",
			in:   "镜像 https://fixupx.com/example_user/status/1000000000000000009 附 https://t.co/Synthetic01",
			want: "https://fixupx.com/example_user/status/1000000000000000009",
		},
		{
			name: "fallback to t.co when no explicit x or mirror link exists",
			in:   "分享短链 https://t.co/Synthetic01。",
			want: "https://t.co/Synthetic01",
		},
		{
			name: "bare t.co link",
			in:   "打开 t.co/Synthetic01 查看",
			want: "https://t.co/Synthetic01",
		},
		{
			name: "bare www.x.com link",
			in:   "查看 www.x.com/example_user/status/1000000000000000009",
			want: "https://www.x.com/example_user/status/1000000000000000009",
		},
		{
			name: "bare mobile.x.com link",
			in:   "查看 mobile.x.com/example_user",
			want: "https://mobile.x.com/example_user",
		},
		{
			name: "bare m.twitter.com link",
			in:   "查看 m.twitter.com/example_user/status/1000000000000000009",
			want: "https://m.twitter.com/example_user/status/1000000000000000009",
		},
		{
			name: "bare vxtwitter mirror with subdomain",
			in:   "查看 i.vxtwitter.com/example_user/status/1000000000000000009",
			want: "https://i.vxtwitter.com/example_user/status/1000000000000000009",
		},
		{
			name: "bare fixvx mirror",
			in:   "查看 fixvx.com/example_user/status/1000000000000000009",
			want: "https://fixvx.com/example_user/status/1000000000000000009",
		},
		{
			name: "bare twittpr mirror with subdomain",
			in:   "查看 m.twittpr.com/example_user/status/1000000000000000009",
			want: "https://m.twittpr.com/example_user/status/1000000000000000009",
		},
		{
			name: "bare nitter.net mirror",
			in:   "查看 nitter.net/example_user/status/1000000000000000009#m",
			want: "https://nitter.net/example_user/status/1000000000000000009#m",
		},
		{
			name: "bare periscope.tv broadcast",
			in:   "直播 periscope.tv/w/1SyntheticBcastId",
			want: "https://periscope.tv/w/1SyntheticBcastId",
		},
		{
			name: "bare pscp.tv broadcast",
			in:   "直播 pscp.tv/w/1SyntheticBcastId",
			want: "https://pscp.tv/w/1SyntheticBcastId",
		},
		{
			name: "inner x url in external query stays external",
			in:   "https://evil.example/jump?u=https://x.com/example_user/status/1000000000000000009",
			want: "https://evil.example/jump?u=https://x.com/example_user/status/1000000000000000009",
		},
	}
	for _, tc := range extractCases {
		got, ok := ExtractShareURL(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("%s: ExtractShareURL(%q) = (%q, %v), want (%q, true)", tc.name, tc.in, got, ok, tc.want)
		}
	}

	// Host boundary checks: lookalike bare domains must NEVER match x.com or t.co or mirrors.
	for _, nonMatch := range []string{
		"打开 box.com/example_user/status/1000000000000000009 查看",
		"打开 fix.com/example_user/status/1000000000000000009 查看",
		"打开 myx.com/example_user/status/1000000000000000009 查看",
		"打开 not-t.co/Synthetic01 查看",
		"打开 a_x.com/example_user 查看",
		"打开 evil.x.com/example_user 查看",
		"打开 example.org/x.com/example_user 查看",
		"联系 user@x.com/example_user 查看",
		"打开 x.com.evil.net/example_user 查看",
		"打开 t.com/Synthetic01 查看",
		"打开 ad.fxtwitter.com/example_user/status/1000000000000000009 查看",
	} {
		if got, ok := ExtractShareURL(nonMatch); ok {
			t.Fatalf("ExtractShareURL(%q) unexpectedly matched %q", nonMatch, got)
		}
	}

	biliStub := &stubResolver{id: Bilibili}
	dyStub := &stubResolver{id: Douyin}
	wbStub := &stubResolver{id: Weibo}
	xStub := &stubResolver{id: X}
	reg := NewRegistry(biliStub, dyStub, wbStub, xStub)

	// Share text with t.co + x.com dispatches x.com to X.
	res, err := reg.Resolve(context.Background(), "外链 https://t.co/Synthetic01 原帖 https://x.com/example_user/status/1000000000000000009")
	if err != nil || res.Site != X || res.ID != "https://x.com/example_user/status/1000000000000000009" {
		t.Fatalf("Registry.Resolve x share = (%+v, %v)", res, err)
	}

	// Mirror and t.co and Periscope dispatch to X.
	for _, in := range []string{
		"https://fxtwitter.com/example_user/status/1000000000000000009",
		"https://t.co/Synthetic01",
		"https://periscope.tv/w/1SyntheticBcastId",
		"https://pscp.tv/w/1SyntheticBcastId",
	} {
		res, err := reg.Resolve(context.Background(), in)
		if err != nil || res.Site != X || res.ID != in {
			t.Fatalf("Registry.Resolve(%q) = (%+v, %v), want X", in, res, err)
		}
	}

	// Pure <19 digits still route to Bilibili (never X).
	beforeX := xStub.calls
	for _, num := range []string{"12", "1000000001", "5000000000000001"} {
		res, err := reg.Resolve(context.Background(), num)
		if err != nil || res.Site != Bilibili || res.ID != num {
			t.Fatalf("Registry.Resolve(%q) = (%+v, %v), want Bilibili", num, res, err)
		}
	}

	// Pure 19 digits stay ambiguous; bare @handle and lookalike domains must NOT route to X.
	beforeBili, beforeDy, beforeWb := biliStub.calls, dyStub.calls, wbStub.calls
	for _, rejected := range []string{
		"1000000000000000009",
		"10000000000000000001",
		"@example_user",
		"box.com/example_user/status/1000000000000000009",
		"fix.com/example_user/status/1000000000000000009",
		"myx.com/example_user/status/1000000000000000009",
		"not-t.co/Synthetic01",
		"https://box.com/example_user/status/1000000000000000009",
		"https://not-t.co/Synthetic01",
		"https://evil.example/jump?u=https://x.com/example_user/status/1000000000000000009",
	} {
		if _, err := reg.Resolve(context.Background(), rejected); !IsKind(err, ErrKindBadRequest) {
			t.Fatalf("Registry.Resolve(%q) err = %v, want bad_request", rejected, err)
		}
	}
	if xStub.calls != beforeX || biliStub.calls != beforeBili || dyStub.calls != beforeDy || wbStub.calls != beforeWb {
		t.Fatalf("rejected inputs invoked resolvers: x=%d->%d bili=%d->%d dy=%d->%d wb=%d->%d",
			beforeX, xStub.calls, beforeBili, biliStub.calls, beforeDy, dyStub.calls, beforeWb, wbStub.calls)
	}
}

func TestXMediaTransportPolicy(t *testing.T) {
	hosts := DefaultXMediaHosts()
	if len(hosts) != 2 || hosts[0] != "pbs.twimg.com" || hosts[1] != "video.twimg.com" {
		t.Fatalf("DefaultXMediaHosts() = %v, want [pbs.twimg.com video.twimg.com]", hosts)
	}

	spec := XMediaTransport("", false)
	if spec.Site != X || spec.UserAgent != DefaultUserAgent || spec.Referer != "" || spec.Origin != "" || spec.PublicRedirects {
		t.Fatalf("unexpected XMediaTransport spec: %+v", spec)
	}

	for _, okURL := range []string{
		"https://pbs.twimg.com/media/SyntheticMedia01?format=jpg&name=orig",
		"https://pbs.twimg.com/profile_images/1000000000000000001/synth_400x400.jpg",
		"https://video.twimg.com/ext_tw_video/1000000000000000009/pu/vid/avc1/1280x720/synth.mp4?tag=12",
		"https://video.twimg.com/tweet_video/SyntheticGif01.mp4",
	} {
		if err := spec.ValidateURL(okURL); err != nil {
			t.Fatalf("XMediaTransport.ValidateURL(%q) failed: %v", okURL, err)
		}
		if err := spec.ValidateRedirectURL(okURL); err != nil {
			t.Fatalf("XMediaTransport.ValidateRedirectURL(%q) failed: %v", okURL, err)
		}
	}

	for _, badURL := range []string{
		"http://pbs.twimg.com/media/SyntheticMedia01?format=jpg&name=orig",
		"https://video.twimg.com:8443/tweet_video/SyntheticGif01.mp4",
		"https://abs.twimg.com/responsive-web/client-web/main.synth.js",
		"https://ton.x.com/i/ton/data/dm/synth.jpg",
		"https://cdn.syndication.twimg.com/tweet-result?id=1000000000000000009",
		"https://twimg.com/media/synth.jpg",
		"https://x.com/example_user/status/1000000000000000009",
		"https://evil-pbs.twimg.com/media/synth.jpg",
		"https://edge.example.net/synth.mp4",
		"https://127.0.0.1/media/synth.jpg",
	} {
		if err := spec.ValidateURL(badURL); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("XMediaTransport.ValidateURL(%q) = %v, want blocked", badURL, err)
		}
		if err := spec.ValidateRedirectURL(badURL); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("XMediaTransport.ValidateRedirectURL(%q) = %v, want blocked", badURL, err)
		}
	}

	req, err := http.NewRequest(http.MethodGet, "https://video.twimg.com/tweet_video/SyntheticGif01.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "auth_token=must_never_reach_cdn; ct0=must_never_reach_cdn")
	req.Header.Set("Authorization", "Bearer must_never_reach_cdn")
	req.Header.Set("X-Csrf-Token", "must_never_reach_cdn")
	req.Header.Set("Referer", "https://x.com/example_user/status/1000000000000000009")
	req.Header.Set("Origin", "https://x.com")
	spec.ApplyHeaders(req)
	for _, hdr := range []string{"Cookie", "Authorization", "X-Csrf-Token", "Referer", "Origin"} {
		if got := req.Header.Get(hdr); got != "" {
			t.Fatalf("XMediaTransport.ApplyHeaders did not strip %s: %q", hdr, got)
		}
	}
	if got := req.Header.Get("User-Agent"); got != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, DefaultUserAgent)
	}
}
