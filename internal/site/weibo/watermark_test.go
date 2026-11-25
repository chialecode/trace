package weibo

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func makeSyntheticValidJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

func makeSyntheticPlaceholderGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 480, 360), color.Palette{
		color.RGBA{R: 235, G: 235, B: 235, A: 255},
		color.RGBA{R: 160, G: 160, B: 160, A: 255},
	})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("gif.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestRewriteWatermarkFreeImageURLSafetyAndGIFExclusion(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{
			name:   "mw2000_static_jpg",
			in:     "https://wx1.sinaimg.cn/mw2000/001SynPic01.jpg",
			want:   "https://wx1.sinaimg.cn/oslarge/001SynPic01.jpg",
			wantOK: true,
		},
		{
			name:   "large_static_png",
			in:     "https://wx4.sinaimg.cn/large/001SynPic02.png",
			want:   "https://wx4.sinaimg.cn/oslarge/001SynPic02.png",
			wantOK: true,
		},
		{
			name:   "orj960_and_orj1080",
			in:     "https://wx2.sinaimg.cn/orj1080/001SynPic03.jpg",
			want:   "https://wx2.sinaimg.cn/oslarge/001SynPic03.jpg",
			wantOK: true,
		},
		{
			name:   "gif_never_rewrites",
			in:     "https://wx1.sinaimg.cn/large/001SynAnim.gif",
			wantOK: false,
		},
		{
			name:   "gif_uppercase_with_query_never_rewrites",
			in:     "https://wx1.sinaimg.cn/mw2000/001SynAnim.GIF?foo=1",
			wantOK: false,
		},
		{
			name:   "already_oslarge_noop",
			in:     "https://wx1.sinaimg.cn/oslarge/001SynPic04.jpg",
			wantOK: false,
		},
		{
			name:   "non_sinaimg_never_rewrites",
			in:     "https://example.invalid/large/001SynPic05.jpg",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		got, ok := RewriteWatermarkFreeImageURL(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: RewriteWatermarkFreeImageURL(%q) = (%q, %v), want (%q, %v)", tc.name, tc.in, got, ok, tc.want, tc.wantOK)
		}
		if strings.Contains(got, "/osj1080/") {
			t.Errorf("%s: rewritten URL must never use /osj1080/: %q", tc.name, got)
		}
	}
}

func TestBuildImageAssetAndFallbackBuildersSkipGIFAndPreserveOriginalFallback(t *testing.T) {
	client := NewClient(ClientOptions{
		ImageURLRewriter: func(_ string, rawURL string) (string, bool) {
			return RewriteWatermarkFreeImageURL(rawURL)
		},
	})

	// 1. Standard pic_infos: primary URL ALWAYS stays the official original (largest/mw2000/large);
	// static pic & LivePhoto still populate WatermarkFreeURL with /oslarge/;
	// GIF (even if URL omits .gif suffix) NEVER populates WatermarkFreeURL.
	rawJSON := `{
		"ok": 1,
		"idstr": "5000000000000801",
		"mblogid": "O000000cV",
		"text_raw": "去水印安全边界测试",
		"user": {"idstr": "1000000001", "screen_name": "合成博主"},
		"pic_ids": ["p_static", "p_gif_typed", "p_gif_ext", "p_live"],
		"pic_infos": {
			"p_static": {
				"pic_id": "p_static",
				"type": "pic",
				"largest": {"url": "https://wx1.sinaimg.cn/mw2000/p_static.jpg", "width": 2000, "height": 1500}
			},
			"p_gif_typed": {
				"pic_id": "p_gif_typed",
				"type": "gif",
				"largest": {"url": "https://wx1.sinaimg.cn/large/p_gif_typed.jpg", "width": 480, "height": 360}
			},
			"p_gif_ext": {
				"pic_id": "p_gif_ext",
				"type": "pic",
				"largest": {"url": "https://wx1.sinaimg.cn/large/p_gif_ext.gif", "width": 480, "height": 360}
			},
			"p_live": {
				"pic_id": "p_live",
				"type": "livephoto",
				"largest": {"url": "https://wx2.sinaimg.cn/large/p_live.jpg", "width": 1200, "height": 1600},
				"video": "https://video.weibo.com/media/play?livephoto=syn1"
			}
		},
		"retweeted_status": {
			"idstr": "5000000000000800",
			"mblogid": "O000000cU",
			"text_raw": "转发原博带图",
			"user": {"idstr": "1000000002", "screen_name": "原作者"},
			"pic_ids": ["p_orig"],
			"pic_infos": {
				"p_orig": {
					"pic_id": "p_orig",
					"type": "pic",
					"largest": {"url": "https://wx3.sinaimg.cn/mw2000/p_orig.jpg", "width": 1800, "height": 1200}
				}
			}
		}
	}`
	post, err := client.ParseRawPostJSON([]byte(rawJSON))
	if err != nil {
		t.Fatalf("ParseRawPostJSON: %v", err)
	}
	if len(post.Images) != 4 {
		t.Fatalf("len(Images) = %d, want 4", len(post.Images))
	}
	if post.Images[0].URL != "https://wx1.sinaimg.cn/mw2000/p_static.jpg" ||
		post.Images[0].WatermarkFreeURL != "https://wx1.sinaimg.cn/oslarge/p_static.jpg" {
		t.Fatalf("static pic = (url=%q, wmFree=%q)", post.Images[0].URL, post.Images[0].WatermarkFreeURL)
	}
	if post.Images[1].Kind != MediaKindGIF ||
		post.Images[1].URL != "https://wx1.sinaimg.cn/large/p_gif_typed.jpg" ||
		post.Images[1].WatermarkFreeURL != "" {
		t.Fatalf("typed GIF must not set WatermarkFreeURL: kind=%q url=%q wmFree=%q", post.Images[1].Kind, post.Images[1].URL, post.Images[1].WatermarkFreeURL)
	}
	if post.Images[2].Kind != MediaKindGIF ||
		post.Images[2].URL != "https://wx1.sinaimg.cn/large/p_gif_ext.gif" ||
		post.Images[2].WatermarkFreeURL != "" {
		t.Fatalf("ext GIF must not set WatermarkFreeURL: kind=%q url=%q wmFree=%q", post.Images[2].Kind, post.Images[2].URL, post.Images[2].WatermarkFreeURL)
	}
	if post.Images[3].Kind != MediaKindLivePhoto ||
		post.Images[3].URL != "https://wx2.sinaimg.cn/large/p_live.jpg" ||
		post.Images[3].WatermarkFreeURL != "https://wx2.sinaimg.cn/oslarge/p_live.jpg" {
		t.Fatalf("livephoto still = (url=%q, wmFree=%q)", post.Images[3].URL, post.Images[3].WatermarkFreeURL)
	}
	if post.Retweeted == nil || len(post.Retweeted.Images) != 1 ||
		post.Retweeted.Images[0].URL != "https://wx3.sinaimg.cn/mw2000/p_orig.jpg" ||
		post.Retweeted.Images[0].WatermarkFreeURL != "https://wx3.sinaimg.cn/oslarge/p_orig.jpg" {
		t.Fatalf("retweeted origin pic = %+v", post.Retweeted)
	}

	// Verify Adapter.PlanDownloadFromPost propagates WatermarkFreeURL and buildPostSnapshot strips it.
	adapter := NewAdapter(client)
	dlPlan, err := adapter.PlanDownloadFromPost(post)
	if err != nil {
		t.Fatalf("PlanDownloadFromPost: %v", err)
	}
	if len(dlPlan.Images) != 5 {
		t.Fatalf("len(dlPlan.Images) = %d, want 5", len(dlPlan.Images))
	}
	if dlPlan.Images[0].ImageURLs[0] != "https://wx1.sinaimg.cn/mw2000/p_static.jpg" ||
		dlPlan.Images[0].WatermarkFreeURL != "https://wx1.sinaimg.cn/oslarge/p_static.jpg" {
		t.Fatalf("dlPlan.Images[0] = %+v", dlPlan.Images[0])
	}
	if dlPlan.Images[1].WatermarkFreeURL != "" || dlPlan.Images[2].WatermarkFreeURL != "" {
		t.Fatalf("dlPlan GIFs unexpectedly carry WatermarkFreeURL: %+v %+v", dlPlan.Images[1], dlPlan.Images[2])
	}
	if dlPlan.Images[3].WatermarkFreeURL != "https://wx2.sinaimg.cn/oslarge/p_live.jpg" ||
		dlPlan.Images[4].WatermarkFreeURL != "https://wx3.sinaimg.cn/oslarge/p_orig.jpg" {
		t.Fatalf("dlPlan live/origin = %+v %+v", dlPlan.Images[3], dlPlan.Images[4])
	}
	snap := buildPostSnapshot(post)
	if snap == nil || len(snap.Images) != 4 || snap.Images[0].WatermarkFreeURL != "" || snap.Images[0].URL != "" {
		t.Fatalf("buildPostSnapshot did not scrub runtime URLs: %+v", snap)
	}

	// 2. Mobile pics[] fallback builder also keeps primary at /large/ and populates WatermarkFreeURL only for non-GIF.
	mobileJSON := `{
		"ok": 1,
		"idstr": "5000000000000802",
		"mblogid": "O000000cW",
		"text_raw": "移动端 pics 夹具",
		"user": {"idstr": "1000000001", "screen_name": "合成博主"},
		"pics": [
			{"pid": "mob_pic", "type": "pic", "large": {"url": "https://wx1.sinaimg.cn/large/mob_pic.jpg"}},
			{"pid": "mob_gif", "type": "gif", "large": {"url": "https://wx1.sinaimg.cn/large/mob_gif.jpg"}}
		]
	}`
	mPost, err := client.ParseRawPostJSON([]byte(mobileJSON))
	if err != nil || len(mPost.Images) != 2 {
		t.Fatalf("mobile ParseRawPostJSON = (%+v, %v)", mPost, err)
	}
	if mPost.Images[0].URL != "https://wx1.sinaimg.cn/large/mob_pic.jpg" ||
		mPost.Images[0].WatermarkFreeURL != "https://wx1.sinaimg.cn/oslarge/mob_pic.jpg" {
		t.Fatalf("mobile static pic = (url=%q, wmFree=%q)", mPost.Images[0].URL, mPost.Images[0].WatermarkFreeURL)
	}
	if mPost.Images[1].Kind != MediaKindGIF ||
		mPost.Images[1].URL != "https://wx1.sinaimg.cn/large/mob_gif.jpg" ||
		mPost.Images[1].WatermarkFreeURL != "" {
		t.Fatalf("mobile gif pic = (kind=%q, url=%q, wmFree=%q)", mPost.Images[1].Kind, mPost.Images[1].URL, mPost.Images[1].WatermarkFreeURL)
	}

	// 3. pic_ids fallback builder keeps primary at /large/ and populates WatermarkFreeURL.
	picIDJSON := `{
		"ok": 1,
		"idstr": "5000000000000803",
		"mblogid": "O000000cX",
		"text_raw": "pic_ids 回退夹具",
		"user": {"idstr": "1000000001", "screen_name": "合成博主"},
		"pic_ids": ["pid_fallback_01"]
	}`
	pPost, err := client.ParseRawPostJSON([]byte(picIDJSON))
	if err != nil || len(pPost.Images) != 1 {
		t.Fatalf("pic_ids ParseRawPostJSON = (%+v, %v)", pPost, err)
	}
	if pPost.Images[0].URL != "https://wx1.sinaimg.cn/large/pid_fallback_01.jpg" ||
		pPost.Images[0].WatermarkFreeURL != "https://wx1.sinaimg.cn/oslarge/pid_fallback_01.jpg" {
		t.Fatalf("pic_ids fallback = (url=%q, wmFree=%q)", pPost.Images[0].URL, pPost.Images[0].WatermarkFreeURL)
	}
}

func TestFetchImageLimitedOSLargeFallbackOn404HTMLAndPlaceholder(t *testing.T) {
	validJPG := makeSyntheticValidJPEG(t, 16, 16, color.RGBA{R: 40, G: 120, B: 200, A: 255})
	placeholderGIF := makeSyntheticPlaceholderGIF(t)

	scenarios := []struct {
		name        string
		oslargeResp func() *http.Response
	}{
		{
			name: "404_not_found",
			oslargeResp: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     http.Header{"Content-Type": []string{"text/plain"}},
					Body:       io.NopCloser(strings.NewReader("not found")),
				}
			},
		},
		{
			name: "html_disguised_200",
			oslargeResp: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader("<!doctype html><html><body>error</body></html>")),
				}
			},
		},
		{
			name: "placeholder_gif_200",
			oslargeResp: func() *http.Response {
				return &http.Response{
					StatusCode:    http.StatusOK,
					ContentLength: int64(len(placeholderGIF)),
					Header:        http.Header{"Content-Type": []string{"image/gif"}},
					Body:          io.NopCloser(bytes.NewReader(placeholderGIF)),
				}
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var mu sync.Mutex
			var seenPaths []string
			var sawCookie bool
			client := NewClient(ClientOptions{
				CredStore:   newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_OSLARGE"),
				MinInterval: -1,
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					mu.Lock()
					seenPaths = append(seenPaths, req.URL.Path)
					if req.Header.Get("Cookie") != "" {
						sawCookie = true
					}
					mu.Unlock()
					if strings.HasPrefix(req.URL.Path, "/oslarge/") {
						return sc.oslargeResp(), nil
					}
					if strings.HasPrefix(req.URL.Path, "/large/") {
						return &http.Response{
							StatusCode:    http.StatusOK,
							ContentLength: int64(len(validJPG)),
							Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
							Body:          io.NopCloser(bytes.NewReader(validJPG)),
						}, nil
					}
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Header:     http.Header{},
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				}),
			})

			data, ct, err := client.FetchImage(context.Background(), "https://wx1.sinaimg.cn/oslarge/001SynOldPic.jpg")
			if err != nil {
				t.Fatalf("FetchImage fallback failed: %v", err)
			}
			if !bytes.Equal(data, validJPG) || !strings.HasPrefix(ct, "image/jpeg") {
				t.Fatalf("FetchImage returned len=%d ct=%q, want len=%d image/jpeg", len(data), ct, len(validJPG))
			}
			if sawCookie {
				t.Fatal("sinaimg.cn request unexpectedly carried Cookie header")
			}
			if len(seenPaths) != 2 || seenPaths[0] != "/oslarge/001SynOldPic.jpg" || seenPaths[1] != "/large/001SynOldPic.jpg" {
				t.Fatalf("seenPaths = %v, want [/oslarge/001SynOldPic.jpg, /large/001SynOldPic.jpg]", seenPaths)
			}
		})
	}
}
