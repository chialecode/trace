package site

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func buildSyntheticGrayPlaceholderGIF480x360(t *testing.T, frames int) []byte {
	t.Helper()
	palette := color.Palette{
		color.RGBA{R: 240, G: 240, B: 240, A: 255},
		color.RGBA{R: 180, G: 180, B: 180, A: 255},
		color.RGBA{R: 120, G: 120, B: 120, A: 255},
	}
	if frames <= 1 {
		img := image.NewPaletted(image.Rect(0, 0, 480, 360), palette)
		for y := 150; y < 210; y++ {
			for x := 180; x < 300; x++ {
				img.SetColorIndex(x, y, 2)
			}
		}
		var buf bytes.Buffer
		if err := gif.Encode(&buf, img, nil); err != nil {
			t.Fatalf("encode single-frame placeholder gif: %v", err)
		}
		return buf.Bytes()
	}

	anim := &gif.GIF{
		Image: make([]*image.Paletted, frames),
		Delay: make([]int, frames),
	}
	for i := 0; i < frames; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, 480, 360), palette)
		frame.SetColorIndex(10+i, 10+i, 2)
		anim.Image[i] = frame
		anim.Delay[i] = 8
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("encode multi-frame gif: %v", err)
	}
	return buf.Bytes()
}

func buildSyntheticColorPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 32, G: 128, B: 220, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestPublicImageCDNFallbackEligibilityAndBuilders(t *testing.T) {
	allowed := []struct {
		name      string
		rawURL    string
		wantClean string
		wantPhot  string
	}{
		{
			name:      "Weibo unsigned static large jpg",
			rawURL:    "https://wx1.sinaimg.cn/large/001SynthPic01.jpg",
			wantClean: "https://wx1.sinaimg.cn/large/001SynthPic01.jpg",
			wantPhot:  "https://i0.wp.com/wx1.sinaimg.cn/large/001SynthPic01.jpg?ssl=1",
		},
		{
			name:      "Weibo unsigned static gif",
			rawURL:    "https://wx3.sinaimg.cn/large/001SynthAnim02.gif",
			wantClean: "https://wx3.sinaimg.cn/large/001SynthAnim02.gif",
			wantPhot:  "https://i0.wp.com/wx3.sinaimg.cn/large/001SynthAnim02.gif?ssl=1",
		},
		{
			name:      "X unsigned pbs.twimg.com with format and name=orig",
			rawURL:    "https://pbs.twimg.com/media/SynthImg01?name=orig&format=JPG",
			wantClean: "https://pbs.twimg.com/media/SynthImg01?format=jpg&name=orig",
			wantPhot:  "", // Photon skipped when origin carries query parameters
		},
		{
			name:      "X queryless legacy :orig path",
			rawURL:    "https://pbs.twimg.com/media/SynthImg02.png:orig",
			wantClean: "https://pbs.twimg.com/media/SynthImg02.png:orig",
			wantPhot:  "https://i0.wp.com/pbs.twimg.com/media/SynthImg02.png:orig?ssl=1",
		},
		{
			name:      "Bilibili public album image strips @ size suffix",
			rawURL:    "https://i0.hdslb.com/bfs/album/synth_cover_01.png@640w_400h.webp",
			wantClean: "https://i0.hdslb.com/bfs/album/synth_cover_01.png",
			wantPhot:  "https://i0.wp.com/i0.hdslb.com/bfs/album/synth_cover_01.png?ssl=1",
		},
	}

	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			clean, parsed, ok := IsEligibleForPublicImageCDNFallback(tc.rawURL, false)
			if !ok {
				t.Fatalf("IsEligibleForPublicImageCDNFallback(%q) = false, want true", tc.rawURL)
			}
			if clean != tc.wantClean {
				t.Fatalf("clean URL = %q, want %q", clean, tc.wantClean)
			}
			phot, photOK := BuildPhotonCacheURL(parsed, clean)
			if tc.wantPhot == "" {
				if photOK {
					t.Fatalf("BuildPhotonCacheURL(%q) = %q, want skipped", clean, phot)
				}
			} else if !photOK || phot != tc.wantPhot {
				t.Fatalf("BuildPhotonCacheURL(%q) = (%q, %v), want (%q, true)", clean, phot, photOK, tc.wantPhot)
			}
			wsrv, wsrvOK := BuildWsrvCacheURL(parsed, clean)
			if !wsrvOK || wsrv != "https://wsrv.nl/?url="+url.QueryEscape(clean)+"&n=-1" {
				t.Fatalf("BuildWsrvCacheURL(%q) = (%q, %v)", clean, wsrv, wsrvOK)
			}
			wb, wbOK := BuildWaybackRawEarliestURL(parsed, clean)
			if !wbOK || wb != "https://web.archive.org/web/0id_/"+clean {
				t.Fatalf("BuildWaybackRawEarliestURL(%q) = (%q, %v)", clean, wb, wbOK)
			}
		})
	}

	rejected := []string{
		"",
		"http://wx1.sinaimg.cn/large/001SynthPic.jpg",
		"https://wx1.sinaimg.cn:8443/large/001SynthPic.jpg",
		"https://user:pass@wx1.sinaimg.cn/large/001SynthPic.jpg",
		"https://wx1.sinaimg.cn/large/001SynthPic.jpg?ssig=SynthSig&Expires=1763700000&KID=unistore",
		"https://wx1.sinaimg.cn/large/001SynthPic.jpg?SUB=SynthSubCookie",
		"https://wx1.sinaimg.cn/large/001SynthPic.jpg?extra=1",
		"https://livephoto.us.sinaimg.cn/001SynthLive.jpg",
		"https://f.video.weibocdn.com/o0/001SynthVid.mp4?ssig=SynthSig",
		"https://p3-pc-sign.douyinpic.com/tos-cn-i-0813/cover.jpeg?x-expires=1763099999&x-signature=SynthSig",
		"https://p3-pc.douyinpic.com/img/tos-cn-i-0813/note.jpeg?a_bogus=SynthBogus",
		"https://ton.x.com/i/ton/data/dm/1001/private.jpg",
		"https://ton.twitter.com/i/ton/data/dm/1001/private.jpg",
		"https://video.twimg.com/ext_tw_video/1/pu/vid/clip.mp4",
		"https://pbs.twimg.com/media/SynthImg01?format=jpg&token=SynthSecret",
		"https://pbs.twimg.com/media/SynthImg01?format=jpg&auth_token=SynthAuth",
		"https://pbs.twimg.com/media/SynthImg01?format=jpg&ct0=SynthCsrf",
		"https://pbs.twimg.com/media/SynthImg01?format=jpg;token=SynthSemi",
		"https://i0.hdslb.com/bfs/subtitle/synth.json",
		"https://i0.hdslb.com/bfs/album/synth.jpg?auth_key=1763700000-0-0-synth",
		"https://evil.example.com/large/001SynthPic.jpg",
	}
	for _, raw := range rejected {
		if clean, _, ok := IsEligibleForPublicImageCDNFallback(raw, false); ok {
			t.Fatalf("IsEligibleForPublicImageCDNFallback(%q) unexpectedly allowed (%q)", raw, clean)
		}
	}
}

func TestShouldAttemptImageCDNFallbackRespectsRiskAndRateLimits(t *testing.T) {
	if !ShouldAttemptImageCDNFallback(nil, true) {
		t.Fatal("expected sawPlaceholder=true to allow fallback")
	}
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		err := &Error{Site: Weibo, Kind: ErrKindUnavailable, HTTPStatus: status}
		if !ShouldAttemptImageCDNFallback(err, false) {
			t.Fatalf("expected HTTP %d to allow fallback", status)
		}
	}
	disallowed := []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		&Error{Site: Weibo, Kind: ErrKindRateLimited, HTTPStatus: http.StatusTooManyRequests},
		&Error{Site: Weibo, Kind: ErrKindRiskControl, HTTPStatus: 432},
		&Error{Site: Bilibili, Kind: ErrKindRiskControl, HTTPStatus: http.StatusPreconditionFailed},
		&Error{Site: X, Kind: ErrKindAuthRequired, HTTPStatus: http.StatusUnauthorized},
		&Error{Site: X, Kind: ErrKindBlocked, Message: "图片体积超出安全上限"},
		&Error{Site: Weibo, Kind: ErrKindNetwork, HTTPStatus: http.StatusBadGateway},
	}
	for _, err := range disallowed {
		if ShouldAttemptImageCDNFallback(err, false) {
			t.Fatalf("ShouldAttemptImageCDNFallback(%v) = true, want false", err)
		}
	}
}

func TestPlaceholderDetectionAndExistingImageValidation(t *testing.T) {
	placeholder480 := buildSyntheticGrayPlaceholderGIF480x360(t, 1)
	anim480 := buildSyntheticGrayPlaceholderGIF480x360(t, 2)
	realPNG := buildSyntheticColorPNG(t, 64, 64)

	// Single-frame 480x360 grayscale GIF is caught even on a GIF slot or without expected dimensions.
	if !LooksLikeExpiredPlaceholderImageBytes(placeholder480, "https://wx1.sinaimg.cn/large/001.gif", 0, 0, true) {
		t.Fatal("expected 1-frame 480x360 grayscale GIF to be flagged as expired placeholder even on GIF slot")
	}
	// Multi-frame 480x360 grayscale animated GIF is NEVER misclassified as placeholder.
	if LooksLikeExpiredPlaceholderImageBytes(anim480, "https://wx1.sinaimg.cn/large/001.gif", 480, 360, true) {
		t.Fatal("genuine multi-frame animated GIF must never be flagged as placeholder")
	}

	tmpDir := t.TempDir()
	validPath := filepath.Join(tmpDir, "valid.png")
	if err := os.WriteFile(validPath, realPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	if ext, sz, shaHex, ok := ValidateExistingImageFile(validPath, "", 64, 64, false); !ok || shaHex == "" || ext != ".png" || sz != int64(len(realPNG)) {
		t.Fatalf("ValidateExistingImageFile(valid.png) = (%q, %d, %q, %v)", ext, sz, shaHex, ok)
	}

	phPath := filepath.Join(tmpDir, "placeholder.gif")
	if err := os.WriteFile(phPath, placeholder480, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := ValidateExistingImageFile(phPath, "https://wx1.sinaimg.cn/large/001.jpg", 1080, 1440, false); ok {
		t.Fatal("ValidateExistingImageFile(placeholder.gif) = ok, want false")
	}

	htmlPath := filepath.Join(tmpDir, "fake.jpg")
	if err := os.WriteFile(htmlPath, []byte("<!DOCTYPE html><html>404</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := ValidateExistingImageFile(htmlPath, "", 0, 0, false); ok {
		t.Fatal("ValidateExistingImageFile accepted disguised HTML")
	}
}

func TestFetchPublicImageCDNFallbackRecoversViaWaybackAndStripsHeaders(t *testing.T) {
	placeholderGIF := buildSyntheticGrayPlaceholderGIF480x360(t, 1)
	genuinePNG := buildSyntheticColorPNG(t, 120, 90)

	var (
		p1Hits       atomic.Int32
		p2Hits       atomic.Int32
		p3InitHits   atomic.Int32
		p3SnapHits   atomic.Int32
		headerLeaked atomic.Bool
	)

	checkCleanHeaders := func(r *http.Request) {
		for _, h := range []string{"Cookie", "Authorization", "X-Csrf-Token", "X-XSRF-TOKEN", "Referer", "Origin"} {
			if r.Header.Get(h) != "" {
				headerLeaked.Store(true)
			}
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkCleanHeaders(r)
		switch {
		case r.URL.Path == "/wsrv":
			p1Hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"error","code":404,"message":"The requested URL returned error: 404"}`))
		case r.URL.Path == "/photon":
			p2Hits.Add(1)
			// Provider 2 cached Sina's 480x360 deleted placeholder GIF -> must be rejected!
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write(placeholderGIF)
		case strings.HasPrefix(r.URL.Path, "/web/0id_/"):
			p3InitHits.Add(1)
			http.Redirect(w, r, "/web/20250101000000id_/https://wx1.sinaimg.cn/large/001SynthRecovered.png", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/web/20250101000000id_/"):
			p3SnapHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(genuinePNG)
		case r.URL.Path == "/escape":
			http.Redirect(w, r, "https://evil.example.com/hijack.png", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Without explicit Providers in AllowLocalhost mode, zero requests must be issued.
	if _, _, _, ok := FetchPublicImageCDNFallback(
		context.Background(),
		PublicImageCDNFallbackConfig{AllowLocalhost: true},
		"https://wx1.sinaimg.cn/large/001SynthRecovered.png",
		DefaultMaxImageBytes,
		0, 0, false,
	); ok || p1Hits.Load() != 0 {
		t.Fatalf("AllowLocalhost with empty Providers must make 0 calls, got ok=%v hits=%d", ok, p1Hits.Load())
	}

	providers := []PublicImageCDNProvider{
		{
			Name:           "wsrv-local",
			AllowedDomains: []string{"127.0.0.1"},
			MaxRedirects:   1,
			BuildURL: func(_ *url.URL, clean string) (string, bool) {
				return srv.URL + "/wsrv?url=" + url.QueryEscape(clean) + "&n=-1", true
			},
		},
		{
			Name:           "photon-local",
			AllowedDomains: []string{"127.0.0.1"},
			MaxRedirects:   1,
			BuildURL: func(_ *url.URL, _ string) (string, bool) {
				return srv.URL + "/photon", true
			},
		},
		{
			Name:           "escape-local",
			AllowedDomains: []string{"127.0.0.1"},
			MaxRedirects:   2,
			BuildURL: func(_ *url.URL, _ string) (string, bool) {
				return srv.URL + "/escape", true
			},
		},
		{
			Name:           "wayback-local",
			AllowedDomains: []string{"127.0.0.1"},
			MaxRedirects:   3,
			BuildURL: func(_ *url.URL, clean string) (string, bool) {
				return srv.URL + "/web/0id_/" + clean, true
			},
		},
	}

	got, ct, provName, ok := FetchPublicImageCDNFallback(
		context.Background(),
		PublicImageCDNFallbackConfig{
			Providers:      providers,
			AllowLocalhost: true,
			UserAgent:      "TRACE-Test-Fallback-UA",
		},
		"https://wx1.sinaimg.cn/large/001SynthRecovered.png",
		DefaultMaxImageBytes,
		120, 90, false,
	)
	if !ok {
		t.Fatal("expected FetchPublicImageCDNFallback to recover image from wayback-local")
	}
	if provName != "wayback-local" || ct != "image/png" || !bytes.Equal(got, genuinePNG) {
		t.Fatalf("recovered (%q, %q, %d bytes), want (wayback-local, image/png, %d bytes)", provName, ct, len(got), len(genuinePNG))
	}
	if headerLeaked.Load() {
		t.Fatal("sensitive header leaked to public CDN fallback provider")
	}
	if p1Hits.Load() != 1 || p2Hits.Load() != 1 || p3InitHits.Load() != 1 || p3SnapHits.Load() != 1 {
		t.Fatalf("unexpected provider hit counts: p1=%d p2=%d p3Init=%d p3Snap=%d",
			p1Hits.Load(), p2Hits.Load(), p3InitHits.Load(), p3SnapHits.Load())
	}
}
