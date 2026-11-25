package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/sitecheck"
)

const (
	synthWeiboSUB          = "_2A25SynthWeiboSubToken022abcdef"
	synthWeiboSUBP         = "0033WrSXqPxfM725SynthSubp022"
	synthWeiboXSRF         = "SynthXsrfToken022_AbCdEf"
	synthWeiboWBPSESS      = "Dt2hbAUaXSynthWbpsess022=="
	synthWeiboCookieRaw    = "SUB=" + synthWeiboSUB + "; SUBP=" + synthWeiboSUBP + "; XSRF-TOKEN=" + synthWeiboXSRF + "; WBPSESS=" + synthWeiboWBPSESS + "; SINAGLOBAL=ignored_stat_001"
	synthWeiboSelfUID      = "5100000001"
	synthWeiboSelfNick     = "合成测试账号甲"
	synthWeiboActiveUID    = "5100000002"
	synthWeiboActiveNick   = "合成活跃博主乙"
	synthWeiboHalfYrUID    = "5100000003"
	synthWeiboThreeYrUID   = "5100000004"
	synthWeiboCustomUID    = "5100000005"
	synthWeiboCustomDomain = "synthcustom022"
	synthWeiboCustomNick   = "合成个性域名丙"
	synthWeiboGroupTitle   = "合成特别分组丁"

	synthWeiboMidLong       = "5000000000000001" // O00000001
	synthWeiboMidLongRT     = "5000000000000002" // O00000002
	synthWeiboMidOver9      = "5000000000000003" // O00000003
	synthWeiboMidMixed      = "5000000000000004" // O00000004
	synthWeiboMidVideoHost  = "5000000000000005" // O00000005
	synthWeiboMidDelOrigin  = "5000000000000006" // O00000006
	synthWeiboMidPinned     = "5000000000000011"
	synthWeiboMidFastRT     = "5000000000000012"
	synthWeiboMidPage1Reg   = "5000000000000013"
	synthWeiboMidPage2Reg   = "5000000000000014"
	synthWeiboVideoOID      = "1034:5000000000000099"
	synthWeiboSignedVideoQS = "Expires=1999999999&ssig=SynthSigAct022&KID=unistore,video"
)

func buildSyntheticPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func buildSyntheticAnimatedGIFBytes(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White}
	f1 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	f2 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	f2.SetColorIndex(1, 1, 1)
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{f1, f2},
		Delay: []int{10, 10},
	}); err != nil {
		t.Fatalf("gif.EncodeAll: %v", err)
	}
	return buf.Bytes()
}

func buildSyntheticQuickTimeMOV(t *testing.T, baseMP4 []byte) []byte {
	t.Helper()
	out := append([]byte(nil), baseMP4...)
	if len(out) >= 12 && string(out[4:8]) == "ftyp" {
		copy(out[8:12], []byte("qt  "))
	}
	return out
}

type act022MockEnv struct {
	rootDir          string
	srv              *httptest.Server
	endpoints        weibo.Endpoints
	apiHits          atomic.Int64
	cdnHits          atomic.Int64
	shortHits        atomic.Int64
	cdnCookieLeaked  atomic.Bool
	shortLinkCookied atomic.Bool
	hook             func(w http.ResponseWriter, r *http.Request) bool
}

func setupAct022MockEnv(t *testing.T, withPreUsedLogin int, emptyTargets bool) *act022MockEnv {
	t.Helper()
	rootDir := t.TempDir()
	env := &act022MockEnv{rootDir: rootDir}

	mp4Data := buildSyntheticProgressiveMP4(t, 2, 0)
	movData := buildSyntheticQuickTimeMOV(t, mp4Data)
	pngData := buildSyntheticPNGBytes(t, 32, 24)
	gifData := buildSyntheticAnimatedGIFBytes(t)

	mblogLong, _ := weibo.MidToMblogID(synthWeiboMidLong)
	mblogLongRT, _ := weibo.MidToMblogID(synthWeiboMidLongRT)
	mblogOver9, _ := weibo.MidToMblogID(synthWeiboMidOver9)
	mblogMixed, _ := weibo.MidToMblogID(synthWeiboMidMixed)
	mblogVideoHost, _ := weibo.MidToMblogID(synthWeiboMidVideoHost)
	mblogDelOrigin, _ := weibo.MidToMblogID(synthWeiboMidDelOrigin)

	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if env.hook != nil && env.hook(w, r) {
			return
		}
		p := r.URL.Path

		// Short link endpoint (NO Cookie allowed)
		if strings.HasPrefix(p, "/tcn/") {
			env.shortHits.Add(1)
			if r.Header.Get("Cookie") != "" {
				env.shortLinkCookied.Store(true)
			}
			w.Header().Set("Location", env.srv.URL+"/"+synthWeiboActiveUID+"/"+mblogLong)
			w.WriteHeader(http.StatusFound)
			return
		}

		// LivePhoto redirector hop (NO Cookie allowed)
		if strings.HasPrefix(p, "/media/play") {
			env.cdnHits.Add(1)
			if r.Header.Get("Cookie") != "" {
				env.cdnCookieLeaked.Store(true)
			}
			w.Header().Set("Location", env.srv.URL+"/cdn/live.mov?"+synthWeiboSignedVideoQS)
			w.WriteHeader(http.StatusFound)
			return
		}

		// Media CDN endpoints (NO Cookie allowed)
		if strings.HasPrefix(p, "/cdn/") {
			env.cdnHits.Add(1)
			if r.Header.Get("Cookie") != "" {
				env.cdnCookieLeaked.Store(true)
			}
			switch {
			case strings.Contains(p, "/oslarge/"):
				http.NotFound(w, r)
			case strings.Contains(p, "/osj1080/"):
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngData)
			case strings.HasSuffix(p, ".gif"):
				w.Header().Set("Content-Type", "image/gif")
				_, _ = w.Write(gifData)
			case strings.HasSuffix(p, ".mov"):
				serveBytesWithRange(w, r, "video/quicktime", movData)
			case strings.HasSuffix(p, ".mp4"):
				serveBytesWithRange(w, r, "video/mp4", mp4Data)
			default:
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngData)
			}
			return
		}

		env.apiHits.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		ck := r.Header.Get("Cookie")
		hasAuth := strings.Contains(ck, "SUB="+synthWeiboSUB)
		base := env.srv.URL

		switch p {
		case "/ajax/setting/getBasicInfo":
			if !hasAuth {
				_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
				return
			}
			// Exact 18-field shape without uid/avatar per weibo.md §13.1 #2.
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"selfdom022","description":"synth","gender":"m","province":"11","city":"1","birthday":"","single":0,"auth_status":0}}`, synthWeiboSelfNick)

		case "/ajax/profile/info":
			q := r.URL.Query()
			if !hasAuth {
				_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
				return
			}
			switch {
			case q.Get("screen_name") == synthWeiboSelfNick:
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q,"statuses_count":12,"followers_count":100,"friends_count":30}}}`,
					synthWeiboSelfUID, synthWeiboSelfNick, base+"/cdn/large/self.png")
			case q.Get("custom") == synthWeiboCustomDomain:
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"domain":%q,"avatar_hd":%q,"statuses_count":88}}}`,
					synthWeiboCustomUID, synthWeiboCustomNick, synthWeiboCustomDomain, base+"/cdn/large/custom.png")
			case q.Get("uid") == synthWeiboActiveUID:
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q,"statuses_count":42,"followers_count":500,"friends_count":20}}}`,
					synthWeiboActiveUID, synthWeiboActiveNick, base+"/cdn/large/active.png")
			case q.Get("uid") == act022NonexistentSynthUID:
				_, _ = fmt.Fprint(w, `{"ok":0,"error_code":20003,"msg":"用户不存在"}`)
			default:
				_, _ = fmt.Fprint(w, `{"ok":0,"error_code":20003,"msg":"用户不存在"}`)
			}

		case "/ajax/statuses/mymblog":
			q := r.URL.Query()
			if !hasAuth {
				_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
				return
			}
			uid := q.Get("uid")
			page := q.Get("page")
			feature := q.Get("feature")
			switch uid {
			case synthWeiboHalfYrUID:
				_, _ = fmt.Fprint(w, `{"ok":1,"data":{"list":[],"since_id":"","total":15,"status_visible":2,"bottom_tips_visible":true,"bottom_tips_text":"博主设置仅展示半年内的微博"}}`)
			case synthWeiboThreeYrUID:
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"list":[{"idstr":%q,"mid":%q,"mblogid":"O0000001Y","created_at":"Sat Nov 21 10:00:00 +0800 2026","text_raw":"三年可见窗口样本","user":{"idstr":%q,"screen_name":"合成三年可见"}}],"since_id":"","total":5,"status_visible":1,"bottom_tips_visible":true,"bottom_tips_text":"已展示近三年全部可见微博"}}`,
					"5000000000000091", "5000000000000091", synthWeiboThreeYrUID)
			case synthWeiboActiveUID:
				if feature == "1" {
					_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"list":[{"idstr":%q,"mid":%q,"mblogid":"O00000013","created_at":"Sat Nov 21 12:00:00 +0800 2026","text_raw":"原创微博样本","user":{"idstr":%q,"screen_name":%q}}],"since_id":"cursor_orig_2","total":42}}`,
						synthWeiboMidPage1Reg, synthWeiboMidPage1Reg, synthWeiboActiveUID, synthWeiboActiveNick)
					return
				}
				if page == "2" {
					_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"list":[{"idstr":%q,"mid":%q,"mblogid":"O00000014","created_at":"Sat Nov 20 12:00:00 +0800 2026","text_raw":"第二页微博","user":{"idstr":%q,"screen_name":%q}}],"since_id":"cursor_p3","total":42}}`,
						synthWeiboMidPage2Reg, synthWeiboMidPage2Reg, synthWeiboActiveUID, synthWeiboActiveNick)
					return
				}
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"list":[`+
					`{"idstr":%q,"mid":%q,"mblogid":"O00000011","isTop":1,"created_at":"Mon Nov 01 12:00:00 +0800 2026","text_raw":"置顶微博","user":{"idstr":%q,"screen_name":%q}},`+
					`{"idstr":%q,"mid":%q,"mblogid":"O00000012","ori_mid":"4900000000000001","created_at":"Sat Nov 21 11:00:00 +0800 2026","text_raw":"快转微博","user":{"idstr":%q,"screen_name":%q}},`+
					`{"idstr":%q,"mid":%q,"mblogid":"O00000013","created_at":"Sat Nov 21 12:00:00 +0800 2026","text_raw":"常规微博","user":{"idstr":%q,"screen_name":%q}}`+
					`],"since_id":"cursor_p2","total":42}}`,
					synthWeiboMidPinned, synthWeiboMidPinned, synthWeiboActiveUID, synthWeiboActiveNick,
					synthWeiboMidFastRT, synthWeiboMidFastRT, synthWeiboActiveUID, synthWeiboActiveNick,
					synthWeiboMidPage1Reg, synthWeiboMidPage1Reg, synthWeiboActiveUID, synthWeiboActiveNick)
			default:
				_, _ = fmt.Fprint(w, `{"ok":1,"data":{"list":[],"since_id":"","total":0}}`)
			}

		case "/ajax/statuses/show":
			id := r.URL.Query().Get("id")
			switch id {
			case act022NonexistentSynthMid:
				_, _ = fmt.Fprint(w, `{"ok":0,"error_code":20101,"msg":"该微博不存在或已被作者删除"}`)
			case mblogLong, synthWeiboMidLong:
				_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Sat Nov 21 12:00:00 +0800 2026","text_raw":"长微博截断...","isLongText":true,"longText":{"longTextContent":"这是一段完整内联的长微博全文内容"},"user":{"idstr":%q,"screen_name":%q}}`,
					synthWeiboMidLong, synthWeiboMidLong, mblogLong, synthWeiboActiveUID, synthWeiboActiveNick)
			case mblogLongRT, synthWeiboMidLongRT:
				_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Sat Nov 21 12:05:00 +0800 2026","text_raw":"转发长文评语","user":{"idstr":%q,"screen_name":%q},"retweeted_status":{"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Sat Nov 21 11:00:00 +0800 2026","text_raw":"原长文...","isLongText":true,"longText":{"longTextContent":"原微博的完整长文内容"},"user":{"idstr":%q,"screen_name":%q}}}`,
					synthWeiboMidLongRT, synthWeiboMidLongRT, mblogLongRT, synthWeiboActiveUID, synthWeiboActiveNick,
					synthWeiboMidLong, synthWeiboMidLong, mblogLong, synthWeiboCustomUID, synthWeiboCustomNick)
			case mblogOver9, synthWeiboMidOver9:
				var ids []string
				infos := make(map[string]any, 10)
				for i := 1; i <= 10; i++ {
					pid := fmt.Sprintf("pic%02d", i)
					ids = append(ids, pid)
					infos[pid] = map[string]any{
						"pic_id": pid,
						"type":   "pic",
						"largest": map[string]any{
							"url":    fmt.Sprintf("%s/cdn/large/%s.png", base, pid),
							"width":  32,
							"height": 24,
						},
					}
				}
				payload, _ := json.Marshal(map[string]any{
					"ok":         1,
					"idstr":      synthWeiboMidOver9,
					"mid":        synthWeiboMidOver9,
					"mblogid":    mblogOver9,
					"created_at": "Sat Nov 21 12:10:00 +0800 2026",
					"text_raw":   "超过九图测试微博",
					"user":       map[string]any{"idstr": synthWeiboActiveUID, "screen_name": synthWeiboActiveNick},
					"pic_ids":    ids,
					"pic_infos":  infos,
				})
				_, _ = w.Write(payload)
			case mblogMixed, synthWeiboMidMixed:
				payload, _ := json.Marshal(map[string]any{
					"ok":         1,
					"idstr":      synthWeiboMidMixed,
					"mid":        synthWeiboMidMixed,
					"mblogid":    mblogMixed,
					"created_at": "Sat Nov 21 12:15:00 +0800 2026",
					"text_raw":   "GIF 与实况混排测试",
					"user":       map[string]any{"idstr": synthWeiboActiveUID, "screen_name": synthWeiboActiveNick},
					"pic_ids":    []string{"g1", "lp1"},
					"pic_infos": map[string]any{
						"g1": map[string]any{
							"pic_id": "g1",
							"type":   "gif",
							"largest": map[string]any{
								"url":    base + "/cdn/large/anim.gif",
								"width":  4,
								"height": 4,
							},
						},
						"lp1": map[string]any{
							"pic_id": "lp1",
							"type":   "livephoto",
							"video":  base + "/media/play?livephoto=" + url.QueryEscape(base+"/cdn/live.mov?"+synthWeiboSignedVideoQS),
							"largest": map[string]any{
								"url":    base + "/cdn/large/still.png",
								"width":  32,
								"height": 24,
							},
						},
					},
				})
				_, _ = w.Write(payload)
			case mblogVideoHost, synthWeiboMidVideoHost:
				height := 1080
				qLabel := "1080P"
				if !hasAuth {
					height = 720
					qLabel = "720P"
				}
				payload, _ := json.Marshal(map[string]any{
					"ok":         1,
					"idstr":      synthWeiboMidVideoHost,
					"mid":        synthWeiboMidVideoHost,
					"mblogid":    mblogVideoHost,
					"created_at": "Sat Nov 21 12:20:00 +0800 2026",
					"text_raw":   "短视频微博测试",
					"user":       map[string]any{"idstr": synthWeiboActiveUID, "screen_name": synthWeiboActiveNick},
					"page_info": map[string]any{
						"object_type": "video",
						"object_id":   synthWeiboVideoOID,
						"page_pic":    base + "/cdn/large/vcover.png",
						"media_info": map[string]any{
							"duration": 2,
							"playback_list": []map[string]any{
								{
									"meta": map[string]any{
										"label":         "mp4_" + strings.ToLower(qLabel),
										"quality_index": height,
										"quality_label": qLabel,
										"is_hidden":     0,
									},
									"play_info": map[string]any{
										"url":          base + "/cdn/video.mp4?" + synthWeiboSignedVideoQS,
										"mime":         "video/mp4",
										"width":        1920,
										"height":       height,
										"fps":          30,
										"bitrate":      2500000,
										"size":         len(mp4Data),
										"duration":     2,
										"video_codecs": "avc1.640028",
										"audio_codecs": "mp4a.40.2",
									},
								},
							},
						},
					},
				})
				_, _ = w.Write(payload)
			case mblogDelOrigin, synthWeiboMidDelOrigin:
				_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Sat Nov 21 12:25:00 +0800 2026","text_raw":"转发已删除原博","visible":{"type":0,"list_id":0},"user":{"idstr":%q,"screen_name":%q},"retweeted_status":{"deleted":"1","text":"抱歉，此微博已被作者删除。"}}`,
					synthWeiboMidDelOrigin, synthWeiboMidDelOrigin, mblogDelOrigin, synthWeiboActiveUID, synthWeiboActiveNick)
			default:
				_, _ = fmt.Fprint(w, `{"ok":0,"error_code":20101,"msg":"该微博不存在"}`)
			}

		case "/tv/api/component":
			payload, _ := json.Marshal(map[string]any{
				"code": "100000",
				"data": map[string]any{
					"Component_Play_Playinfo": map[string]any{
						"mid":         synthWeiboMidVideoHost,
						"title":       "独立视频页测试",
						"author":      synthWeiboActiveNick,
						"author_id":   synthWeiboActiveUID,
						"cover_image": base + "/cdn/large/vcover.png",
						"duration":    2,
						"urls": map[string]string{
							"高清 1080P": base + "/cdn/video.mp4?" + synthWeiboSignedVideoQS,
						},
					},
				},
			})
			_, _ = w.Write(payload)

		case "/ajax/friendships/friends":
			page := r.URL.Query().Get("page")
			if page == "2" {
				_, _ = fmt.Fprintf(w, `{"ok":1,"users":[{"idstr":"5100000021","screen_name":"合成关注对象二","avatar_hd":%q}],"next_cursor":0,"total_number":2}`,
					base+"/cdn/large/f2.png")
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"users":[{"idstr":"5100000020","screen_name":"合成关注对象一","avatar_hd":%q}],"next_cursor":2,"total_number":2}`,
				base+"/cdn/large/f1.png")

		case "/ajax/feed/allGroups":
			_, _ = fmt.Fprintf(w, `{"ok":1,"groups":[{"title":"分组","group":[{"gid":"4000000000000001","list_id":"4000000000000001","title":%q,"type":"1","count":1}]}]}`,
				synthWeiboGroupTitle)

		case "/ajax/favorites/all_fav":
			page := r.URL.Query().Get("page")
			if page == "2" {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"status":[{"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Sat Nov 20 12:00:00 +0800 2026","text_raw":"收藏第二页","user":{"idstr":%q,"screen_name":%q}}],"total_number":2}}`,
					synthWeiboMidPage2Reg, synthWeiboMidPage2Reg, "O00000014", synthWeiboActiveUID, synthWeiboActiveNick)
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"status":[{"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Sat Nov 21 12:00:00 +0800 2026","text_raw":"收藏第一页","user":{"idstr":%q,"screen_name":%q}}],"total_number":2}}`,
				synthWeiboMidPage1Reg, synthWeiboMidPage1Reg, "O00000013", synthWeiboActiveUID, synthWeiboActiveNick)

		case "/ajax/statuses/likelist":
			// Simulate expected 404 on uncertain likelist endpoint (V-14).
			http.NotFound(w, r)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(env.srv.Close)

	// Prepare .env.local, cookie file, targets-weibo.json, and optional pre-used ledger.
	secDir := filepath.Join(rootDir, ".local", "site-secrets")
	checkDir := filepath.Join(rootDir, ".local", "site-check")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "weibo.cookie"), []byte(synthWeiboCookieRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env.local"), []byte("TRACE_TEST_WEIBO_COOKIE_FILE=.local/site-secrets/weibo.cookie\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if withPreUsedLogin > 0 {
		preLedger := fmt.Sprintf(`{"act":"act022","runs":0,"step1LoginApi":{"limit":5,"used":%d}}`, withPreUsedLogin)
		if err := os.WriteFile(filepath.Join(checkDir, "usage-act022.json"), []byte(preLedger), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var tg act022Targets
	if emptyTargets {
		tg = act022Targets{Version: 1, Act: "ACT-022", ProvidedAt: "2026-11-21"}
	} else {
		tg = act022Targets{
			Version:    1,
			Act:        "ACT-022",
			ProvidedAt: "2026-11-21",
			Users: []act022UserItem{
				{URL: env.srv.URL + "/u/" + synthWeiboActiveUID, Expect: []string{"active", "pinned", "fastRetweet"}},
				{URL: env.srv.URL + "/u/" + synthWeiboHalfYrUID, Expect: []string{"halfYear"}, Note: "empty in last 6 months"},
				{URL: env.srv.URL + "/u/" + synthWeiboThreeYrUID, Expect: []string{"halfYear"}, Note: "3-year window"},
				{URL: env.srv.URL + "/" + synthWeiboCustomDomain, Expect: []string{"customDomain"}},
			},
			Posts: []act022PostItem{
				{URL: env.srv.URL + "/" + synthWeiboActiveUID + "/" + mblogLong, Expect: []string{"longText"}},
				{URL: env.srv.URL + "/" + synthWeiboActiveUID + "/" + mblogLongRT, Expect: []string{"longRetweet"}},
				{URL: env.srv.URL + "/" + synthWeiboActiveUID + "/" + mblogOver9, Expect: []string{"over9"}},
				{URL: env.srv.URL + "/" + synthWeiboActiveUID + "/" + mblogMixed, Expect: []string{"gif", "livePhoto", "mixed"}},
				{URL: env.srv.URL + "/tv/show/" + synthWeiboVideoOID, Expect: []string{"video", "videoPage"}},
				{URL: env.srv.URL + "/" + synthWeiboActiveUID + "/" + mblogDelOrigin, Expect: []string{"deletedOrigin"}},
			},
			ShortLinks: []string{env.srv.URL + "/tcn/A6SYNTH01"},
			ShareTexts: []string{"分享一条微博 https://weibo.com/" + synthWeiboActiveUID + "/" + mblogLong},
		}
	}
	rawTG, _ := json.MarshalIndent(tg, "", "  ")
	if err := os.WriteFile(filepath.Join(checkDir, "targets-weibo.json"), rawTG, 0o600); err != nil {
		t.Fatal(err)
	}

	u := env.srv.URL
	env.endpoints = weibo.Endpoints{
		PCBasicInfo:     u + "/ajax/setting/getBasicInfo",
		ProfileInfo:     u + "/ajax/profile/info",
		StatusesShow:    u + "/ajax/statuses/show",
		StatusesLong:    u + "/ajax/statuses/longtext",
		StatusesMyMblog: u + "/ajax/statuses/mymblog",
		TVComponent:     u + "/tv/api/component",
		Friends:         u + "/ajax/friendships/friends",
		FollowContent:   u + "/ajax/profile/followContent",
		AllGroups:       u + "/ajax/feed/allGroups",
		AllFavorites:    u + "/ajax/favorites/all_fav",
		LikeList:        u + "/ajax/statuses/likelist",
	}
	return env
}

func TestAct022LimitsAndPreUsedLedgerMigration(t *testing.T) {
	sum := 0
	for _, v := range act022APILimits {
		sum += v
	}
	if sum != act022MaxAPICalls {
		t.Fatalf("act022APILimits sum = %d, want %d", sum, act022MaxAPICalls)
	}

	dir := t.TempDir()
	preLedger := `{"act":"act022","note":"login confirmation only","runs":0,"step1LoginApi":{"limit":5,"used":2}}`
	if err := os.WriteFile(filepath.Join(dir, "usage-act022.json"), []byte(preLedger), 0o600); err != nil {
		t.Fatal(err)
	}

	preUsed, err := migrateAct022PreUsedLedger(dir)
	if err != nil {
		t.Fatalf("migrateAct022PreUsedLedger: %v", err)
	}
	if preUsed != 2 {
		t.Fatalf("preUsed = %d, want 2", preUsed)
	}

	gate, err := sitecheck.OpenGate(dir, sitecheck.Limits{
		ActID:         act022ID,
		MaxRuns:       act022MaxRuns,
		MaxAPICalls:   act022MaxAPICalls,
		MaxByCategory: act022APILimits,
	})
	if err != nil {
		t.Fatalf("OpenGate: %v", err)
	}
	if u := gate.Usage(); u.APICalls != 2 || u.Categories["login"] != 2 || u.Runs != 0 {
		t.Fatalf("migrated usage mismatch: %+v", u)
	}

	// Only 3 additional login calls are allowed (2+3=5); the 4th MUST fail BEFORE HTTP send.
	var upstreamHits atomic.Int64
	ck := &act022Checker{
		gate:         gate,
		apiLimits:    act022APILimits,
		shortLink:    &mediaBudget{name: "short_link", maxRequests: act022MaxShortLinkHops, maxBytes: act022MaxShortLinkBytes},
		probeCDN:     &mediaBudget{name: "probe_cdn", maxRequests: act022MaxProbeCDNReqs, maxBytes: act022MaxProbeCDNBytes},
		cacheCDN:     &mediaBudget{name: "cache_cdn", maxRequests: act022MaxCacheCDNReqs, maxBytes: act022MaxCacheCDNBytes},
		statuses:     map[string]int{},
		pathCounts:   map[string]int{},
		apiEnvelopes: map[string][]act022APITelemetry{},
	}
	ck.setPhase("login")
	rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		upstreamHits.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &nopCloser{bytes.NewReader([]byte(`{"ok":1}`))},
		}, nil
	}))

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://weibo.com/ajax/setting/getBasicInfo", nil)
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatalf("login call %d failed: %v", i+1, err)
		}
	}
	if upstreamHits.Load() != 3 {
		t.Fatalf("upstreamHits = %d, want 3", upstreamHits.Load())
	}
	reqOver, _ := http.NewRequest(http.MethodGet, "https://weibo.com/ajax/setting/getBasicInfo", nil)
	if _, err := rt.RoundTrip(reqOver); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("4th login call err = %v, want ErrQuotaExceeded", err)
	}
	if upstreamHits.Load() != 3 {
		t.Fatalf("4th over-quota login call hit upstream (%d), want 3", upstreamHits.Load())
	}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func TestAct022QuotaGateRejectsBeforeHTTPSend(t *testing.T) {
	gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
		ActID:         act022ID,
		MaxRuns:       1,
		MaxAPICalls:   act022MaxAPICalls,
		MaxByCategory: act022APILimits,
	})
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	ck := &act022Checker{
		gate:         gate,
		apiLimits:    act022APILimits,
		shortLink:    &mediaBudget{name: "short_link", maxRequests: 1, maxBytes: 1024},
		probeCDN:     &mediaBudget{name: "probe_cdn", maxRequests: 1, maxBytes: 1024},
		cacheCDN:     &mediaBudget{name: "cache_cdn", maxRequests: 1, maxBytes: 1024},
		statuses:     map[string]int{},
		pathCounts:   map[string]int{},
		apiEnvelopes: map[string][]act022APITelemetry{},
	}
	rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &nopCloser{bytes.NewReader([]byte(`{"ok":1}`))},
		}, nil
	}))

	// 1. Disallowed write/unlisted endpoints must fail with ErrQuotaExceeded and 0 hits.
	ck.setPhase("post_detail")
	for _, tc := range []struct{ method, rawURL string }{
		{http.MethodPost, "https://weibo.com/ajax/statuses/create"},
		{http.MethodPost, "https://weibo.com/ajax/statuses/show?id=O00000001"},
		{http.MethodGet, "https://weibo.com/tv/api/component"},
		{http.MethodGet, "https://m.weibo.cn/api/config"},
		{http.MethodGet, "https://weibo.com/ajax/setting/getBasicInfo"}, // wrong phase!
	} {
		req, _ := http.NewRequest(tc.method, tc.rawURL, nil)
		if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("%s %s: err = %v, want ErrQuotaExceeded", tc.method, tc.rawURL, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("disallowed requests hit upstream: %d", hits.Load())
	}

	// 2. Cookie on short_link or CDN must be refused BEFORE HTTP send.
	ck.setPhase("short_link")
	slCookied, _ := http.NewRequest(http.MethodGet, "https://t.cn/A6SYNTH01", nil)
	slCookied.Header.Set("Cookie", "SUB="+synthWeiboSUB)
	if _, err := rt.RoundTrip(slCookied); err == nil {
		t.Fatal("expected error when Cookie is sent on t.cn")
	}

	ck.setPhase("probe_cdn")
	cdnCookied, _ := http.NewRequest(http.MethodGet, "https://wx1.sinaimg.cn/large/pic.jpg", nil)
	cdnCookied.Header.Set("Cookie", "SUB="+synthWeiboSUB)
	if _, err := rt.RoundTrip(cdnCookied); err == nil {
		t.Fatal("expected error when Cookie is sent on probe CDN")
	}
	if hits.Load() != 0 {
		t.Fatalf("cookied short_link/CDN hit upstream: %d", hits.Load())
	}

	// 3. Exceeding shortLink & probeCDN request caps must reject BEFORE HTTP send.
	ck.setPhase("short_link")
	slOK, _ := http.NewRequest(http.MethodGet, "https://t.cn/A6SYNTH01", nil)
	if _, err := rt.RoundTrip(slOK); err != nil {
		t.Fatalf("1st shortLink call failed: %v", err)
	}
	if _, err := rt.RoundTrip(slOK); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("2nd shortLink call err = %v, want ErrQuotaExceeded", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("over-quota shortLink hit upstream (%d)", hits.Load())
	}

	ck.setPhase("probe_cdn")
	cdnOK, _ := http.NewRequest(http.MethodGet, "https://wx1.sinaimg.cn/large/pic.jpg", nil)
	if _, err := rt.RoundTrip(cdnOK); err != nil {
		t.Fatalf("1st probeCDN call failed: %v", err)
	}
	if _, err := rt.RoundTrip(cdnOK); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("2nd probeCDN call err = %v, want ErrQuotaExceeded", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("over-quota probeCDN hit upstream (%d)", hits.Load())
	}
}

func TestAct022StopConditionsHaltAllRemainingRequests(t *testing.T) {
	cases := []struct {
		name      string
		wantStop  string
		respondFn func(w http.ResponseWriter)
	}{
		{"http_414", "http_414", func(w http.ResponseWriter) { w.WriteHeader(http.StatusRequestURITooLong) }},
		{"http_418", "http_418", func(w http.ResponseWriter) { w.WriteHeader(http.StatusTeapot) }},
		{"http_429", "http_429", func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) }},
		{"http_432", "http_432", func(w http.ResponseWriter) { w.WriteHeader(432) }},
		{"http_403_unexpected", "http_403", func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }},
		{"login_visitor_redirect", "login_or_visitor_redirect", func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://passport.weibo.com/visitor/visitor?entry=miniblog")
			w.WriteHeader(http.StatusFound)
		}},
		{"ok_minus_100", "ok_minus_100", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
		}},
		{"html_verify_page", "html_verify_page", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><title>Geetest 滑块安全验证</title></html>`)
		}},
		{"html_visitor_page", "html_visitor_or_login_page", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<html><title>Sina Visitor System</title></html>`)
		}},
		{"empty_api_body", "empty_api_body", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAct022MockEnv(t, 2, false)
			var hitsAfterTrigger atomic.Int64
			var triggered atomic.Bool

			env.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if triggered.Load() {
					hitsAfterTrigger.Add(1)
					return false
				}
				// Trigger stop condition on Step 3 (/ajax/profile/info?custom=...).
				if r.URL.Path == "/ajax/profile/info" && r.URL.Query().Get("custom") != "" {
					triggered.Store(true)
					tc.respondFn(w)
					return true
				}
				return false
			}

			outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act022.json")
			rc, err := runAct022(context.Background(), act022Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				MinSpacing:     -1,
				Endpoints:      env.endpoints,
				AllowLocalhost: true,
			})
			if err == nil || rc.Passed {
				t.Fatalf("%s: expected failed/stopped run, got err=%v passed=%v", tc.name, err, rc.Passed)
			}
			if rc.StoppedBy != tc.wantStop {
				t.Fatalf("%s: stoppedBy = %q, want %q", tc.name, rc.StoppedBy, tc.wantStop)
			}
			if !triggered.Load() {
				t.Fatalf("%s: stop trigger was never reached", tc.name)
			}
			if got := hitsAfterTrigger.Load(); got != 0 {
				t.Fatalf("%s: sent %d requests AFTER stop condition tripped, want 0", tc.name, got)
			}
			if !rc.TempDataDeleted {
				t.Fatalf("%s: temp data directory was not deleted", tc.name)
			}
			if !rc.SecretScanClean {
				t.Fatalf("%s: receipt failed secret scan", tc.name)
			}
		})
	}
}

func TestAct022EndToEndSyntheticRunAndRedaction(t *testing.T) {
	env := setupAct022MockEnv(t, 2, false)
	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act022.json")

	rc, err := runAct022(context.Background(), act022Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runAct022 failed: %v (stoppedBy=%q steps=%+v)", err, rc.StoppedBy, rc.Steps)
	}
	if !rc.Passed || rc.StoppedBy != "" || !rc.TempDataDeleted || !rc.SecretScanClean {
		t.Fatalf("unexpected receipt summary: passed=%v stoppedBy=%q tempDel=%v secretClean=%v",
			rc.Passed, rc.StoppedBy, rc.TempDataDeleted, rc.SecretScanClean)
	}
	if env.cdnCookieLeaked.Load() || env.shortLinkCookied.Load() {
		t.Fatalf("Cookie leaked to CDN (%v) or t.cn (%v)", env.cdnCookieLeaked.Load(), env.shortLinkCookied.Load())
	}
	if rc.PreUsedLoginCalls != 2 || rc.APIUsage.Categories["login"] != 5 {
		t.Fatalf("login quota accounting mismatch: preUsed=%d loginCat=%d", rc.PreUsedLoginCalls, rc.APIUsage.Categories["login"])
	}
	if rc.APIUsage.APICalls > act022MaxAPICalls {
		t.Fatalf("apiCalls=%d exceeded %d", rc.APIUsage.APICalls, act022MaxAPICalls)
	}
	if rc.ShortLinkHops.Requests != 1 {
		t.Fatalf("shortLinkHops=%d, want 1", rc.ShortLinkHops.Requests)
	}
	if rc.ProbeCDN.Requests == 0 || rc.ProbeCDN.Requests > act022MaxProbeCDNReqs {
		t.Fatalf("probeCDN requests=%d out of range 1..%d", rc.ProbeCDN.Requests, act022MaxProbeCDNReqs)
	}
	if rc.CacheCDN.Requests == 0 || rc.CacheCDN.Requests > act022MaxCacheCDNReqs {
		t.Fatalf("cacheCDN requests=%d out of range 1..%d", rc.CacheCDN.Requests, act022MaxCacheCDNReqs)
	}

	// Verify V-01..V-14 & Step9 are all populated.
	for _, key := range []string{"V-01", "V-02", "V-03", "V-04", "V-05", "V-06", "V-07", "V-08", "V-09", "V-10", "V-11", "V-12", "V-13", "V-14", "Step9"} {
		if _, ok := rc.Verifications[key]; !ok {
			t.Fatalf("missing verification %s in receipt", key)
		}
	}
	v14, _ := rc.Verifications["V-14"].(map[string]any)
	if v14["available"] != false || v14["status"] != "unavailable" {
		t.Fatalf("expected V-14 expected 404 to degrade to unavailable, got %+v", v14)
	}

	// Verify written receipt JSON contains ZERO cookies, signed URLs, raw URLs, or target identifiers.
	rawBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	rawStr := string(rawBytes)
	if findings := sitecheck.ScanTextForSecrets(rawStr); len(findings) > 0 {
		t.Fatalf("receipt failed sitecheck.ScanTextForSecrets: %v", findings)
	}
	mblogLong, _ := weibo.MidToMblogID(synthWeiboMidLong)
	forbidden := []string{
		"http://", "https://",
		synthWeiboSUB, synthWeiboSUBP, synthWeiboXSRF, synthWeiboWBPSESS,
		"SynthSigAct022", "unistore,video",
		synthWeiboSelfUID, synthWeiboSelfNick,
		synthWeiboActiveUID, synthWeiboActiveNick,
		synthWeiboHalfYrUID, synthWeiboThreeYrUID,
		synthWeiboCustomUID, synthWeiboCustomDomain, synthWeiboCustomNick,
		synthWeiboGroupTitle,
		synthWeiboMidLong, mblogLong, synthWeiboMidOver9, synthWeiboMidMixed,
		synthWeiboMidVideoHost, synthWeiboVideoOID, "A6SYNTH01",
	}
	for _, f := range forbidden {
		if strings.Contains(rawStr, f) {
			t.Fatalf("receipt leaked forbidden string %q", f)
		}
	}

	// Second run on the same ledger MUST be rejected BEFORE any network call (MaxRuns=1).
	hitsBefore := env.apiHits.Load()
	if _, err := runAct022(context.Background(), act022Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	}); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("second run err = %v, want ErrQuotaExceeded", err)
	}
	if env.apiHits.Load() != hitsBefore {
		t.Fatalf("second run sent %d API calls after MaxRuns exhausted", env.apiHits.Load()-hitsBefore)
	}
}

func TestAct022MissingTargetsMarkedNotRun(t *testing.T) {
	env := setupAct022MockEnv(t, 0, true)
	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act022-empty.json")

	rc, err := runAct022(context.Background(), act022Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runAct022 with missing targets failed: %v (steps=%+v)", err, rc.Steps)
	}
	if !rc.Passed {
		t.Fatalf("expected passed=true when missing optional targets are marked not-run")
	}

	skippedSteps := map[string]bool{}
	for _, s := range rc.Steps {
		if s.Skipped {
			skippedSteps[s.Name] = true
		}
	}
	for _, wantSkipped := range []string{
		"v02_short_link_and_share",
		"v03_resolve_nick_and_custom",
		"v04_v05_user_profile_and_mymblog",
		"v06_v09_post_details_and_video",
		"v07_v11_v12_media_cdn_probes",
		"step9_formal_cache_flow",
	} {
		if !skippedSteps[wantSkipped] {
			t.Fatalf("expected step %s to be marked skipped/not-run when targets missing", wantSkipped)
		}
	}
	for _, vKey := range []string{"V-02", "V-03", "V-04", "V-05", "V-06", "V-07", "V-08", "V-09", "V-11", "V-12", "Step9"} {
		m, _ := rc.Verifications[vKey].(map[string]any)
		if m["status"] != "not-run" {
			t.Fatalf("verification %s status = %v, want not-run", vKey, m["status"])
		}
	}
}

func TestAct022RedactorRejectsLeaks(t *testing.T) {
	r := newAct022Redactor()
	r.addCookieHeader(synthWeiboCookieRaw)
	r.addIDs(synthWeiboActiveUID, synthWeiboMidLong, "O00000001", synthWeiboVideoOID)
	r.addHandles(synthWeiboActiveNick, synthWeiboCustomDomain, "星野", "ok")
	r.addURLs("https://weibo.com/u/" + synthWeiboActiveUID)

	if err := r.verifyJSONClean(`{"act":"ACT-022","passed":true,"ok":1,"apiCalls":10}`); err != nil {
		t.Fatalf("clean receipt rejected: %v", err)
	}
	if !r.containsCookie([]byte("binary-db-header\x00" + synthWeiboSUB + "\x00trailer")) {
		t.Fatalf("expected containsCookie to catch raw cookie inside binary DB bytes")
	}
	if got := r.scrubError("error for 星野 failed"); strings.Contains(got, "星野") {
		t.Fatalf("2-char non-ASCII handle was not scrubbed from error: %q", got)
	}
	leaks := []string{
		`{"note":"` + synthWeiboSUB + `"}`,
		`{"note":"SUB=` + synthWeiboSUB + `"}`,
		`{"note":"https://f.video.weibocdn.com/o0/a.mp4?` + synthWeiboSignedVideoQS + `"}`,
		`{"note":"` + synthWeiboActiveUID + `"}`,
		`{"note":"` + synthWeiboMidLong + `"}`,
		`{"note":"O00000001"}`,
		`{"note":"` + synthWeiboActiveNick + `"}`,
		`{"note":"` + synthWeiboCustomDomain + `"}`,
		`{"note":"星野"}`,
		`{"note":"https://weibo.com/u/123456"}`,
		`{"note":"Expires=` + strconv.FormatInt(1999999999, 10) + `&ssig=AbCdEf12&KID=unistore,video"}`,
	}
	for i, payload := range leaks {
		if err := r.verifyJSONClean(payload); err == nil {
			t.Fatalf("leak case %d was NOT rejected: %s", i, payload)
		}
	}
}

func TestAct022PreflightResilience(t *testing.T) {
	t.Run("shortlink_out_of_allowlist_v10_400_404_json_and_optional_groups_404_html", func(t *testing.T) {
		env := setupAct022MockEnv(t, 2, false)
		var favCalledBeforeGroups atomic.Bool
		var favCalled atomic.Bool

		env.hook = func(w http.ResponseWriter, r *http.Request) bool {
			switch {
			case strings.HasPrefix(r.URL.Path, "/tcn/"):
				// Redirect to an out-of-allowlist Weibo subdomain (e.g. photo.weibo.com).
				w.Header().Set("Location", "https://photo.weibo.com/12345/talbum/detail/photo_id/99999")
				w.WriteHeader(http.StatusFound)
				return true
			case r.URL.Path == "/ajax/profile/info" && r.URL.Query().Get("uid") == act022NonexistentSynthUID:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"ok":0,"error_code":20003,"errno":20003,"msg":"user does not exist"}`)
				return true
			case r.URL.Path == "/ajax/statuses/show" && r.URL.Query().Get("id") == act022NonexistentSynthMid:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"ok":0,"error_code":20101,"errno":20101,"msg":"status does not exist"}`)
				return true
			case r.URL.Path == "/ajax/favorites/all_fav":
				favCalled.Store(true)
				return false
			case r.URL.Path == "/ajax/feed/allGroups":
				if favCalled.Load() {
					favCalledBeforeGroups.Store(true)
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `<html><body>404 Not Found</body></html>`)
				return true
			}
			return false
		}

		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act022-resilience.json")
		rc, err := runAct022(context.Background(), act022Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      env.endpoints,
			AllowLocalhost: true,
		})
		if err != nil {
			t.Fatalf("runAct022 failed: %v (stoppedBy=%q steps=%+v)", err, rc.StoppedBy, rc.Steps)
		}
		if !rc.Passed || rc.StoppedBy != "" {
			t.Fatalf("expected passed run, got passed=%v stoppedBy=%q", rc.Passed, rc.StoppedBy)
		}
		if !favCalledBeforeGroups.Load() {
			t.Fatalf("expected ListFavorites to run BEFORE unverified ListFollowGroups")
		}

		v02, _ := rc.Verifications["V-02"].(map[string]any)
		if v02["blockedExternal"] != true || v02["resolved"] != false {
			t.Fatalf("V-02 = %+v, want blockedExternal=true resolved=false", v02)
		}
		foundShortRedirect := false
		for _, rd := range rc.Redirects {
			if rd["category"] == "short_link" {
				foundShortRedirect = true
				if rd["targetScheme"] != "https" || rd["targetHostClass"] != "*.weibo.com" || rd["withinWeiboAllowlist"] != false {
					t.Fatalf("unexpected short_link redirect shape: %+v", rd)
				}
			}
		}
		if !foundShortRedirect {
			t.Fatalf("missing short_link redirect shape in rc.Redirects: %+v", rc.Redirects)
		}

		v10, _ := rc.Verifications["V-10"].(map[string]any)
		nonUID, _ := v10["nonexistentUser"].(map[string]any)
		if nonUID["httpStatus"] != 400 || nonUID["errorCode"] != 20003 {
			t.Fatalf("V-10 nonexistentUser lost 400 JSON errorCode: %+v", nonUID)
		}
		nonMid, _ := v10["nonexistentPost"].(map[string]any)
		if nonMid["httpStatus"] != 404 || nonMid["errorCode"] != 20101 {
			t.Fatalf("V-10 nonexistentPost lost 404 JSON errorCode: %+v", nonMid)
		}

		v13, _ := rc.Verifications["V-13"].(map[string]any)
		if v13["favoritesDataStatusKeyOK"] != true || v13["followGroupsAvailable"] != false {
			t.Fatalf("V-13 = %+v, want favorites OK and followGroupsAvailable=false", v13)
		}
		step9, _ := rc.Verifications["Step9"].(map[string]any)
		if step9["status"] != "verified" || step9["tempCacheSecretScanClean"] != true {
			t.Fatalf("Step9 did not complete cleanly: %+v", step9)
		}
	})

	t.Run("v12_video_error_still_probes_livephoto_and_watermark_transport_error_no_panic", func(t *testing.T) {
		env := setupAct022MockEnv(t, 2, false)
		var livePhotoHit atomic.Bool
		var videoProbeFailed atomic.Bool

		env.hook = func(w http.ResponseWriter, r *http.Request) bool {
			switch {
			case strings.Contains(r.URL.Path, "/oslarge/"):
				// Force a transport-level failure so probeWeiboImageAsset returns (nil, err).
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return true
					}
				}
				return false
			case r.URL.Path == "/cdn/video.mp4" && !videoProbeFailed.Swap(true):
				// Fail only the first V-12 probe with HTTP 500 (non-stop error); Step 9 still runs cleanly.
				w.WriteHeader(http.StatusInternalServerError)
				return true
			case r.URL.Path == "/media/play":
				livePhotoHit.Store(true)
				return false
			}
			return false
		}

		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act022-v12-partial.json")
		rc, err := runAct022(context.Background(), act022Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      env.endpoints,
			AllowLocalhost: true,
		})
		if err == nil || !strings.Contains(err.Error(), "step failures") {
			t.Fatalf("expected clean step-failure error, got %v", err)
		}
		if _, statErr := os.Stat(outPath); statErr != nil {
			t.Fatalf("expected redacted receipt to still be written on step failure: %v", statErr)
		}
		if rc.Passed {
			t.Fatalf("expected rc.Passed=false when V-12 video probe failed")
		}
		if rc.StoppedBy != "" {
			t.Fatalf("non-stop V-12 error tripped stop: %q", rc.StoppedBy)
		}
		if !livePhotoHit.Load() {
			t.Fatalf("LivePhoto probe was skipped after non-stop V-12 video failure")
		}
		v07, _ := rc.Verifications["V-07"].(map[string]any)
		wmProbes, _ := v07["watermarkFreeProbes"].(map[string]any)
		oslarge, _ := wmProbes["oslarge"].(map[string]any)
		if oslarge["available"] != false {
			t.Fatalf("expected oslarge transport failure to record available=false without panicking, got %+v", oslarge)
		}
		v12, _ := rc.Verifications["V-12"].(map[string]any)
		if v12["videoStatus"] != "failed" || v12["livePhotoStatus"] != "verified" {
			t.Fatalf("V-12 = %+v, want videoStatus=failed and livePhotoStatus=verified", v12)
		}
		lp, _ := v12["livePhoto"].(map[string]any)
		if lp["ensureLivePhotoMP4OK"] != true || lp["quickTimeRepackaged"] != true {
			t.Fatalf("expected V-12 livePhoto ensureLivePhotoMP4OK=true quickTimeRepackaged=true, got %+v", lp)
		}
	})

	t.Run("v10_ok1_empty_data_nonexistent_user_no_typed_nil_panic", func(t *testing.T) {
		env := setupAct022MockEnv(t, 2, false)
		env.hook = func(w http.ResponseWriter, r *http.Request) bool {
			switch {
			case r.URL.Path == "/ajax/profile/info" && r.URL.Query().Get("uid") == act022NonexistentSynthUID:
				// Real Weibo can return HTTP 200 ok=1 with empty data object for nonexistent UID -> ClassifyHTTPAndEnvelopeError returns (*site.Error)(nil).
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ok":1,"data":{}}`)
				return true
			case r.URL.Path == "/ajax/statuses/show" && r.URL.Query().Get("id") == act022NonexistentSynthMid:
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ok":1}`)
				return true
			}
			return false
		}
		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act022-v10-ok1.json")
		rc, err := runAct022(context.Background(), act022Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      env.endpoints,
			AllowLocalhost: true,
		})
		if err != nil {
			t.Fatalf("runAct022 failed on ok=1 empty V-10 probe: %v", err)
		}
		v10, _ := rc.Verifications["V-10"].(map[string]any)
		nonUID, _ := v10["nonexistentUser"].(map[string]any)
		if nonUID["httpStatus"] != 200 || nonUID["ok"] != 1 || nonUID["classifiedKind"] != "" || nonUID["hasDataUser"] != false {
			t.Fatalf("unexpected V-10 nonexistentUser = %+v", nonUID)
		}
		nonMid, _ := v10["nonexistentPost"].(map[string]any)
		if nonMid["httpStatus"] != 200 || nonMid["ok"] != 1 || nonMid["classifiedKind"] != "" || nonMid["hasMidField"] != false {
			t.Fatalf("unexpected V-10 nonexistentPost = %+v", nonMid)
		}
	})

	t.Run("classify_livephoto_media_play_path_exact", func(t *testing.T) {
		ck := &act022Checker{}
		ck.setPhase("probe_cdn")

		reqExact, _ := http.NewRequest(http.MethodGet, "https://video.weibo.com/media/play?livephoto=test", nil)
		if kind, _, _ := ck.classify(reqExact); kind != act022KindProbeCDN {
			t.Fatalf("exact /media/play kind = %v, want act022KindProbeCDN", kind)
		}
		for _, bad := range []string{
			"https://video.weibo.com/media/play/extra?livephoto=test",
			"https://video.weibo.com/media/playback?livephoto=test",
		} {
			reqBad, _ := http.NewRequest(http.MethodGet, bad, nil)
			if kind, _, _ := ck.classify(reqBad); kind != act022KindUnlisted {
				t.Fatalf("%s kind = %v, want act022KindUnlisted", bad, kind)
			}
		}
	})
}

func TestAct023LimitsAndIndependentLedger(t *testing.T) {
	sum := 0
	for _, v := range act023APILimits {
		sum += v
	}
	if sum != act023MaxAPICalls || act023MaxAPICalls != 35 {
		t.Fatalf("act023APILimits sum = %d, max = %d, want 35", sum, act023MaxAPICalls)
	}

	env := setupAct022MockEnv(t, 0, false)
	// Seed an already-exhausted usage-act022.json; act023 MUST NOT read, migrate, or modify it.
	act022LedgerPath := filepath.Join(env.rootDir, ".local", "site-check", "usage-act022.json")
	origAct022Ledger := `{"actId":"act022","runs":1,"apiCalls":22,"categories":{"login":5}}`
	if err := os.WriteFile(act022LedgerPath, []byte(origAct022Ledger), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act023.json")
	rc, err := runAct023(context.Background(), act022Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runAct023 failed: %v (stoppedBy=%q steps=%+v)", err, rc.StoppedBy, rc.Steps)
	}
	if rc.Act != act023Label || !rc.Passed || rc.StoppedBy != "" || !rc.TempDataDeleted || !rc.SecretScanClean {
		t.Fatalf("unexpected ACT-023 receipt: act=%q passed=%v stoppedBy=%q tempDel=%v secretClean=%v",
			rc.Act, rc.Passed, rc.StoppedBy, rc.TempDataDeleted, rc.SecretScanClean)
	}
	if rc.PreUsedLoginCalls != 0 {
		t.Fatalf("PreUsedLoginCalls = %d, want 0", rc.PreUsedLoginCalls)
	}
	if rc.APIUsage.ActID != act023ID || rc.APIUsage.Runs != 1 || rc.APIUsage.APICalls > act023MaxAPICalls {
		t.Fatalf("ACT-023 usage mismatch: %+v", rc.APIUsage)
	}
	if rc.APIUsage.Categories["login"] != 3 {
		t.Fatalf("ACT-023 login calls = %d, want 3 (1 guest + 2 login)", rc.APIUsage.Categories["login"])
	}
	if env.cdnCookieLeaked.Load() || env.shortLinkCookied.Load() {
		t.Fatalf("Cookie leaked to CDN (%v) or t.cn (%v)", env.cdnCookieLeaked.Load(), env.shortLinkCookied.Load())
	}

	// Verify usage-act022.json remained untouched and usage-act023.json was created independently.
	gotAct022Ledger, err := os.ReadFile(act022LedgerPath)
	if err != nil || string(gotAct022Ledger) != origAct022Ledger {
		t.Fatalf("usage-act022.json was unexpectedly modified: err=%v content=%q", err, string(gotAct022Ledger))
	}
	act023LedgerPath := filepath.Join(env.rootDir, ".local", "site-check", "usage-act023.json")
	if _, err := os.Stat(act023LedgerPath); err != nil {
		t.Fatalf("usage-act023.json missing: %v", err)
	}

	// Second run on usage-act023.json MUST be rejected before any HTTP call.
	hitsBefore := env.apiHits.Load()
	if _, err := runAct023(context.Background(), act022Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	}); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("second runAct023 err = %v, want ErrQuotaExceeded", err)
	}
	if env.apiHits.Load() != hitsBefore {
		t.Fatalf("second runAct023 sent %d API calls after MaxRuns exhausted", env.apiHits.Load()-hitsBefore)
	}
}

func TestAct023DefaultWatermarkRewriteAndFallback(t *testing.T) {
	pngData := buildSyntheticPNGBytes(t, 32, 24)
	mblogOver9, _ := weibo.MidToMblogID(synthWeiboMidOver9)
	mblogMixed, _ := weibo.MidToMblogID(synthWeiboMidMixed)

	for _, tc := range []struct {
		name             string
		oslargeAvailable bool
	}{
		{name: "oslarge_404_falls_back_cleanly", oslargeAvailable: false},
		{name: "oslarge_200_lossless_succeeds", oslargeAvailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAct022MockEnv(t, 0, false)
			var oslargeHits, largeHits atomic.Int64

			// Return real https://wx1.sinaimg.cn/large/... image URLs so PR #95's default
			// weiboRemoveWatermark=true rewrites static images to /oslarge/ with /large/ fallback.
			env.hook = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case strings.HasPrefix(r.URL.Path, "/cdn/oslarge/"):
					env.cdnHits.Add(1)
					oslargeHits.Add(1)
					if r.Header.Get("Cookie") != "" {
						env.cdnCookieLeaked.Store(true)
					}
					if !tc.oslargeAvailable {
						http.NotFound(w, r)
						return true
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(pngData)
					return true
				case strings.HasPrefix(r.URL.Path, "/cdn/large/"):
					largeHits.Add(1)
					return false
				case r.URL.Path == "/ajax/statuses/show":
					id := r.URL.Query().Get("id")
					if id == mblogOver9 || id == synthWeiboMidOver9 {
						env.apiHits.Add(1)
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
						var ids []string
						infos := make(map[string]any, 10)
						for i := 1; i <= 10; i++ {
							pid := fmt.Sprintf("pic%02d", i)
							ids = append(ids, pid)
							infos[pid] = map[string]any{
								"pic_id": pid,
								"type":   "pic",
								"largest": map[string]any{
									"url":    fmt.Sprintf("https://wx1.sinaimg.cn/large/%s.png", pid),
									"width":  32,
									"height": 24,
								},
							}
						}
						payload, _ := json.Marshal(map[string]any{
							"ok":         1,
							"idstr":      synthWeiboMidOver9,
							"mid":        synthWeiboMidOver9,
							"mblogid":    mblogOver9,
							"created_at": "Sat Nov 21 12:10:00 +0800 2026",
							"text_raw":   "超过九图去水印测试微博",
							"user":       map[string]any{"idstr": synthWeiboActiveUID, "screen_name": synthWeiboActiveNick},
							"pic_ids":    ids,
							"pic_infos":  infos,
						})
						_, _ = w.Write(payload)
						return true
					}
					if id == mblogMixed || id == synthWeiboMidMixed {
						env.apiHits.Add(1)
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
						payload, _ := json.Marshal(map[string]any{
							"ok":         1,
							"idstr":      synthWeiboMidMixed,
							"mid":        synthWeiboMidMixed,
							"mblogid":    mblogMixed,
							"created_at": "Sat Nov 21 12:15:00 +0800 2026",
							"text_raw":   "GIF 与实况去水印混排测试",
							"user":       map[string]any{"idstr": synthWeiboActiveUID, "screen_name": synthWeiboActiveNick},
							"pic_ids":    []string{"g1", "lp1"},
							"pic_infos": map[string]any{
								"g1": map[string]any{
									"pic_id": "g1",
									"type":   "gif",
									"largest": map[string]any{
										"url":    "https://wx1.sinaimg.cn/large/anim.gif",
										"width":  4,
										"height": 4,
									},
								},
								"lp1": map[string]any{
									"pic_id": "lp1",
									"type":   "livephoto",
									"video":  env.srv.URL + "/media/play?livephoto=" + url.QueryEscape(env.srv.URL+"/cdn/live.mov?"+synthWeiboSignedVideoQS),
									"largest": map[string]any{
										"url":    "https://wx1.sinaimg.cn/large/still.png",
										"width":  32,
										"height": 24,
									},
								},
							},
						})
						_, _ = w.Write(payload)
						return true
					}
				}
				return false
			}

			srvURL, err := url.Parse(env.srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			// Route https://wx1.sinaimg.cn/<path> to the local httptest.Server /cdn/<path>.
			rewritingRT := roundTrip(func(req *http.Request) (*http.Response, error) {
				if strings.EqualFold(req.URL.Hostname(), "wx1.sinaimg.cn") {
					cloned := req.Clone(req.Context())
					cloned.URL.Scheme = srvURL.Scheme
					cloned.URL.Host = srvURL.Host
					cloned.Host = srvURL.Host
					cloned.URL.Path = "/cdn" + req.URL.Path
					return env.srv.Client().Transport.RoundTrip(cloned)
				}
				return env.srv.Client().Transport.RoundTrip(req)
			})

			outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act023-"+tc.name+".json")
			rc, err := runAct023(context.Background(), act022Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				MinSpacing:     -1,
				Endpoints:      env.endpoints,
				Transport:      rewritingRT,
				AllowLocalhost: true,
			})
			if err != nil {
				t.Fatalf("runAct023 (%s) failed: %v (stoppedBy=%q steps=%+v)", tc.name, err, rc.StoppedBy, rc.Steps)
			}
			if !rc.Passed || rc.StoppedBy != "" || !rc.SecretScanClean || !rc.TempDataDeleted {
				t.Fatalf("unexpected receipt (%s): passed=%v stoppedBy=%q secretClean=%v tempDel=%v",
					tc.name, rc.Passed, rc.StoppedBy, rc.SecretScanClean, rc.TempDataDeleted)
			}
			if env.cdnCookieLeaked.Load() || env.shortLinkCookied.Load() {
				t.Fatalf("Cookie leaked to CDN (%v) or t.cn (%v)", env.cdnCookieLeaked.Load(), env.shortLinkCookied.Load())
			}
			if rc.APIUsage.APICalls > act023MaxAPICalls {
				t.Fatalf("apiCalls=%d exceeded %d", rc.APIUsage.APICalls, act023MaxAPICalls)
			}

			v07, _ := rc.Verifications["V-07"].(map[string]any)
			if v07["defaultWatermarkRewriteApplied"] != true {
				t.Fatalf("expected V-07 defaultWatermarkRewriteApplied=true, got %+v", v07)
			}
			wmProbes, _ := v07["watermarkFreeProbes"].(map[string]any)
			osProbe, _ := wmProbes["oslarge"].(map[string]any)
			if osProbe["available"] != tc.oslargeAvailable {
				t.Fatalf("V-07 oslarge available = %v, want %v (probe=%+v)", osProbe["available"], tc.oslargeAvailable, osProbe)
			}
			osjProbe, _ := wmProbes["osj1080"].(map[string]any)
			if osjProbe["available"] != true || osjProbe["sameResolution"] != true {
				t.Fatalf("V-07 osj1080 probe = %+v, want available=true sameResolution=true", osjProbe)
			}

			v11, _ := rc.Verifications["V-11"].(map[string]any)
			prim, _ := v11["primaryImage"].(map[string]any)
			if prim["status"] != http.StatusOK {
				t.Fatalf("V-11 primaryImage status = %v, want 200", prim["status"])
			}

			step9, _ := rc.Verifications["Step9"].(map[string]any)
			if step9["weiboRemoveWatermarkDefault"] != true || step9["status"] != "verified" {
				t.Fatalf("Step9 = %+v, want weiboRemoveWatermarkDefault=true status=verified", step9)
			}
			multi, _ := step9["multiImagePost"].(map[string]any)
			if multi["taskStatus"] != "completed" || multi["missingCount"] != 0 {
				t.Fatalf("Step9 multiImagePost = %+v, want completed with 0 missing", multi)
			}
			if oslargeHits.Load() < 2 {
				t.Fatalf("expected /oslarge/ hits in both Step 7 and Step 9, got %d", oslargeHits.Load())
			}
			if !tc.oslargeAvailable && largeHits.Load() < 2 {
				t.Fatalf("expected /large/ fallback hits when /oslarge/ returns 404, got %d", largeHits.Load())
			}
		})
	}
}
