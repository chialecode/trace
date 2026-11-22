package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	synthWeiboSUB      = "SYNTH_WB_SUB_9a8b7c6d5e4f"
	synthWeiboSUBP     = "SYNTH_WB_SUBP_112233445566"
	synthWeiboXSRF     = "SYNTH_WB_XSRF_778899aabbcc"
	synthWeiboWBPSESS  = "SYNTH_WB_SESS_ddeeff001122"
	synthWeiboSelfUID  = "5100000001"
	synthWeiboSelfName = "合成微博本人"
	synthWeiboUID      = "5100000002"
	synthWeiboName     = "合成微博创作者"
	synthWeiboDomain   = "synthcreator"
	synthWeiboMidPlain = "5090000000000001"
	synthWeiboMidLong  = "5090000000000002"
	synthWeiboMidRT    = "5090000000000003"
	synthWeiboMidFast  = "5090000000000004"
	synthWeiboMidHost  = "5090000000000005"
	synthWeiboMidDelRT = "5090000000000006"
	synthWeiboOIDHost  = "1034:5090000000000011"
	synthWeiboOIDAlone = "1034:5090000000000022"
)

func synthWeiboCookie() string {
	return "SUB=" + synthWeiboSUB + "; SUBP=" + synthWeiboSUBP + "; XSRF-TOKEN=" + synthWeiboXSRF + "; WBPSESS=" + synthWeiboWBPSESS
}

func synthWeiboMP4Bytes(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	vBytes, err := download.BuildSyntheticVideoM4S(60, 30) // 2.0s @ 30fps
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(94) // ~2.0s @ 48kHz
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	vPath := filepath.Join(dir, "v.m4s")
	aPath := filepath.Join(dir, "a.m4s")
	outPath := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(vPath, vBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := download.MuxDASHFiles(vPath, aPath, outPath); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type weiboThreeSiteHarness struct {
	backend *Backend
	loc     appdirs.Locations
	desktop *MockDesktop

	biliSrv *httptest.Server
	dySrv   *httptest.Server
	wbSrv   *httptest.Server

	biliReqs atomic.Int64
	dyReqs   atomic.Int64
	wbReqs   atomic.Int64

	wbShowReqs   atomic.Int64
	wbMyMblogReq atomic.Int64
	wbCDNReqs    atomic.Int64
	wbGroupReqs  atomic.Int64
	wbFavReqs    atomic.Int64
	wbLikeReqs   atomic.Int64

	wbMode          atomic.Value // "ok", "expired", "risk418", "mismatch"
	wbBottomTips    atomic.Bool
	wbLastFeature   atomic.Value // string
	dyZeroAwemeNull atomic.Bool

	mu           sync.Mutex
	wbMyMblogLog []string
}

func newWeiboThreeSiteHarness(t *testing.T, extraOpts ...func(*Options)) *weiboThreeSiteHarness {
	t.Helper()

	h := &weiboThreeSiteHarness{
		loc:     testLocations(t),
		desktop: NewMockDesktop(),
	}
	h.wbMode.Store("ok")
	h.wbLastFeature.Store("")

	thumbPNG := historyThumbnailPNG(t, 48, 48)
	mp4Bytes := synthWeiboMP4Bytes(t)
	gifBytes := append([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"), []byte{}...)

	h.biliSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.biliReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			if !strings.Contains(r.Header.Get("Cookie"), "SESSDATA=") {
				_, _ = fmt.Fprint(w, `{"code":-101,"message":"账号未登录","data":{"isLogin":false}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"合成B站用户","face":"","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
		case "/x/web-interface/view", "/x/web-interface/wbi/view":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"bvid":"BV1xx411c7mD","aid":1001,"title":"合成B站视频","pic":"","pubdate":1710000000,"duration":30,"owner":{"mid":12345,"name":"合成UP","face":""},"pages":[{"cid":2001,"page":1,"part":"P1","duration":30}]}}`)
		case "/x/relation/tags":
			_, _ = fmt.Fprint(w, `{"code":0,"data":[{"tagid":0,"name":"默认分组","count":1}]}`)
		case "/x/relation/followings":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"list":[{"mid":12345,"uname":"合成UP","face":"","sign":"简介"}],"total":1}}`)
		case "/room/v1/Room/get_status_info_by_uids":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"12345":{"uid":12345,"room_id":223344,"uname":"合成UP","title":"合成直播","live_status":0}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.biliSrv.Close)

	const (
		dySelfSec    = "MS4wLjABAAAA_synth_dy_self_3site"
		dyZeroSec    = "MS4wLjABAAAA_synth_dy_zero_posts"
		dyNonZeroSec = "MS4wLjABAAAA_synth_dy_non_zero_empty"
	)
	h.dySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.dyReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/aweme/v1/web/user/profile/self/":
			if !strings.Contains(r.Header.Get("Cookie"), "sessionid=") {
				_, _ = fmt.Fprint(w, `{"status_code":8,"status_msg":"请先登录"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88001122","sec_uid":%q,"unique_id":"synth_dy","nickname":"合成抖音用户"}}`, dySelfSec)
		case "/aweme/v1/web/user/profile/other/":
			sec := r.URL.Query().Get("sec_user_id")
			switch sec {
			case dyZeroSec:
				_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88009901","sec_uid":%q,"unique_id":"zero_creator","nickname":"零作品作者","aweme_count":0}}`, dyZeroSec)
			case dyNonZeroSec:
				_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88009902","sec_uid":%q,"unique_id":"shell_creator","nickname":"有作品空壳作者","aweme_count":5}}`, dyNonZeroSec)
			default:
				_, _ = fmt.Fprintf(w, `{"status_code":0,"user":{"uid":"88009999","sec_uid":%q,"unique_id":"other_creator","nickname":"其他作者","aweme_count":0}}`, sec)
			}
		case "/aweme/v1/web/aweme/post/":
			sec := r.URL.Query().Get("sec_user_id")
			if sec == dyZeroSec && h.dyZeroAwemeNull.Load() {
				_, _ = fmt.Fprint(w, `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.dySrv.Close)

	h.wbSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cdn/") {
			h.wbCDNReqs.Add(1)
			if r.Header.Get("Cookie") != "" {
				t.Errorf("weibo CDN request leaked Cookie header on %s", r.URL.Path)
			}
			switch {
			case strings.HasSuffix(r.URL.Path, ".png"), strings.HasSuffix(r.URL.Path, ".jpg"):
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(thumbPNG)
			case strings.HasSuffix(r.URL.Path, ".gif"):
				w.Header().Set("Content-Type", "image/gif")
				_, _ = w.Write(gifBytes)
			case strings.HasSuffix(r.URL.Path, ".mp4"):
				w.Header().Set("Content-Type", "video/mp4")
				http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(mp4Bytes))
			default:
				http.NotFound(w, r)
			}
			return
		}

		h.wbReqs.Add(1)
		mode, _ := h.wbMode.Load().(string)
		ck := r.Header.Get("Cookie")
		if mode == "risk418" || strings.Contains(ck, "SUB=trigger_418") {
			w.WriteHeader(418)
			_, _ = fmt.Fprint(w, `{"ok":-100,"message":"blocked"}`)
			return
		}
		if mode == "expired" || !strings.Contains(ck, "SUB=") || strings.Contains(ck, "SUB=expired") || strings.Contains(ck, "SUB=guest") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		cdn := h.wbSrv.URL + "/cdn"
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"synthself","description":"合成简介","gender":"m","province":"11","city":"1"}}`, synthWeiboSelfName)

		case "/ajax/profile/info":
			q := r.URL.Query()
			switch {
			case q.Get("screen_name") == synthWeiboSelfName:
				if mode == "mismatch" {
					_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"id":5100000001,"idstr":%q,"screen_name":"不匹配的昵称","avatar_hd":%q,"statuses_count":10}}}`, synthWeiboSelfUID, cdn+"/avatar_self.png")
					return
				}
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"id":5100000001,"idstr":%q,"screen_name":%q,"domain":"synthself","avatar_hd":%q,"description":"本人简介","statuses_count":10,"followers_count":12,"friends_count":3}}}`,
					synthWeiboSelfUID, synthWeiboSelfName, cdn+"/avatar_self.png")
			case q.Get("uid") == synthWeiboUID || q.Get("screen_name") == synthWeiboName || q.Get("custom") == synthWeiboDomain:
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"id":5100000002,"idstr":%q,"screen_name":%q,"domain":%q,"avatar_hd":%q,"description":"创作者简介","statuses_count":42,"followers_count":1234,"friends_count":56,"verified":true,"verified_reason":"合成认证"}}}`,
					synthWeiboUID, synthWeiboName, synthWeiboDomain, cdn+"/avatar_creator.png")
			default:
				uid := q.Get("uid")
				if uid == "" {
					uid = "5100000099"
				}
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":"其他微博用户","avatar_hd":%q,"statuses_count":1}}}`, uid, cdn+"/avatar_creator.png")
			}

		case "/ajax/statuses/show":
			h.wbShowReqs.Add(1)
			id := r.URL.Query().Get("id")
			if decoded, err := weibo.MblogIDToMid(id); err == nil {
				id = decoded
			}
			_, _ = fmt.Fprint(w, h.weiboPostJSON(id, cdn))

		case "/ajax/statuses/longtext":
			id := r.URL.Query().Get("id")
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"longTextContent":"完整展开的合成长微博正文 %s 含多段文字。"}}`, id)

		case "/ajax/statuses/mymblog":
			h.wbMyMblogReq.Add(1)
			page := r.URL.Query().Get("page")
			sinceID := r.URL.Query().Get("since_id")
			feat := r.URL.Query().Get("feature")
			h.wbLastFeature.Store(feat)
			h.mu.Lock()
			h.wbMyMblogLog = append(h.wbMyMblogLog, fmt.Sprintf("page=%s&feature=%s&since_id=%s", page, feat, sinceID))
			h.mu.Unlock()
			if page == "1" || page == "" {
				listItems := []string{
					h.weiboPostJSON(synthWeiboMidPlain, cdn),
					h.weiboPostJSON(synthWeiboMidRT, cdn),
					h.weiboPostJSON(synthWeiboMidFast, cdn),
					h.weiboPostJSON(synthWeiboMidLong, cdn),
				}
				if feat == "1" {
					// Simulate server-side original-only filter returning only originals.
					listItems = []string{
						h.weiboPostJSON(synthWeiboMidPlain, cdn),
						h.weiboPostJSON(synthWeiboMidLong, cdn),
					}
				}
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"since_id":"since_page_2","total":42,"bottom_tips_visible":false,"bottom_tips_text":"","list":[%s]}}`, strings.Join(listItems, ","))
				return
			}
			if h.wbBottomTips.Load() {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"since_id":"","total":42,"bottom_tips_visible":true,"bottom_tips_text":"博主设置了仅展示半年内的微博","list":[%s]}}`,
					h.weiboPostJSON(synthWeiboMidHost, cdn))
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"since_id":"","total":42,"bottom_tips_visible":false,"bottom_tips_text":"","list":[%s]}}`,
				h.weiboPostJSON(synthWeiboMidHost, cdn))

		case "/tv/api/component":
			_ = r.ParseForm()
			dataArg := r.FormValue("data")
			if strings.Contains(dataArg, synthWeiboOIDHost) {
				_, _ = fmt.Fprintf(w, `{"code":"100000","data":{"Component_Play_Playinfo":{"mid":%q,"title":"带宿主帖的合成视频","author":%q,"author_id":%q,"cover_image":%q,"duration":2,"urls":{"高清 1080P":%q}}}}`,
					synthWeiboMidHost, synthWeiboName, synthWeiboUID, cdn+"/cover_host.png", cdn+"/host_video.mp4?ssig=SIGN_HOST&Expires=1999999999&KID=unistore,video")
				return
			}
			_, _ = fmt.Fprintf(w, `{"code":"100000","data":{"Component_Play_Playinfo":{"mid":"","title":"独立合成微博视频","author":%q,"author_id":%q,"cover_image":%q,"duration":2,"urls":{"高清 1080P":%q}}}}`,
				synthWeiboName, synthWeiboUID, cdn+"/cover_alone.png", cdn+"/alone_video.mp4?ssig=SIGN_ALONE&Expires=1999999999&KID=unistore,video")

		case "/ajax/feed/allGroups":
			h.wbGroupReqs.Add(1)
			_, _ = fmt.Fprint(w, `{"ok":1,"groups":[{"title":"自定义分组","group":[{"gid":"grp_1","list_id":"grp_1","title":"SYNTH_PRIVATE_GROUP_TITLE","count":2}]}]}`)

		case "/ajax/friendships/friends":
			page := r.URL.Query().Get("page")
			if page == "1" || page == "" {
				_, _ = fmt.Fprintf(w, `{"ok":1,"total_number":3,"users":[{"idstr":%q,"screen_name":"SYNTH_FOLLOWED_ONE","description":"关注简介一","avatar_hd":%q},{"idstr":"5100000003","screen_name":"SYNTH_FOLLOWED_TWO","description":"关注简介二","avatar_hd":%q}]}`,
					synthWeiboUID, cdn+"/avatar_creator.png", cdn+"/avatar_creator.png")
				return
			}
			if page == "2" {
				// Intentionally repeat 5100000003 to assert cross-page deduplication.
				_, _ = fmt.Fprintf(w, `{"ok":1,"total_number":3,"users":[{"idstr":"5100000003","screen_name":"SYNTH_FOLLOWED_TWO","avatar_hd":%q},{"idstr":"5100000004","screen_name":"SYNTH_FOLLOWED_THREE","avatar_hd":%q}]}`,
					cdn+"/avatar_creator.png", cdn+"/avatar_creator.png")
				return
			}
			_, _ = fmt.Fprint(w, `{"ok":1,"total_number":3,"users":[]}`)

		case "/ajax/favorites/all_fav":
			h.wbFavReqs.Add(1)
			page := r.URL.Query().Get("page")
			if page == "1" || page == "" {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"total_number":2,"status":[{"idstr":"5090000000000091","mblogid":"FavOne001","created_at":"Mon Nov 18 10:00:00 +0800 2026","text_raw":"SYNTH_FAV_SECRET_TITLE_ONE","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q}}]}}`,
					synthWeiboUID, synthWeiboName, cdn+"/avatar_creator.png")
				return
			}
			if page == "2" {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"total_number":2,"status":[{"idstr":"5090000000000091","mblogid":"FavOne001","created_at":"Mon Nov 18 10:00:00 +0800 2026","text_raw":"SYNTH_FAV_SECRET_TITLE_ONE","user":{"idstr":%q,"screen_name":%q}},{"idstr":"5090000000000092","mblogid":"FavTwo002","created_at":"Mon Nov 18 11:00:00 +0800 2026","text_raw":"SYNTH_FAV_SECRET_TITLE_TWO","user":{"idstr":%q,"screen_name":%q}}]}}`,
					synthWeiboUID, synthWeiboName, synthWeiboUID, synthWeiboName)
				return
			}
			_, _ = fmt.Fprint(w, `{"ok":1,"data":{"total_number":2,"status":[]}}`)

		case "/ajax/statuses/likelist":
			h.wbLikeReqs.Add(1)
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"total_number":1,"list":[{"idstr":"5090000000000095","mblogid":"LikeOne01","created_at":"Mon Nov 18 12:00:00 +0800 2026","text_raw":"SYNTH_LIKE_SECRET_TITLE","user":{"idstr":%q,"screen_name":%q}}]}}`,
				synthWeiboUID, synthWeiboName)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.wbSrv.Close)

	opts := Options{
		Locations:        h.loc,
		Desktop:          h.desktop,
		DisableBgWorkers: true,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      h.biliSrv.URL,
				PassportBase: h.biliSrv.URL,
				WWWBase:      h.biliSrv.URL,
				SpaceBase:    h.biliSrv.URL,
				DynamicBase:  h.biliSrv.URL,
				LiveAPIBase:  h.biliSrv.URL,
				LiveWebBase:  h.biliSrv.URL,
			},
			PacingInterval: -1,
			AllowLocalhost: true,
		},
		DouyinOpts: douyin.ClientOptions{
			Endpoints: douyin.Endpoints{
				ProfileSelf:  h.dySrv.URL + "/aweme/v1/web/user/profile/self/",
				ProfileOther: h.dySrv.URL + "/aweme/v1/web/user/profile/other/",
				AwemePost:    h.dySrv.URL + "/aweme/v1/web/aweme/post/",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
		WeiboOpts: weibo.ClientOptions{
			Endpoints: weibo.Endpoints{
				PCBasicInfo:     h.wbSrv.URL + "/ajax/setting/getBasicInfo",
				ProfileInfo:     h.wbSrv.URL + "/ajax/profile/info",
				StatusesShow:    h.wbSrv.URL + "/ajax/statuses/show",
				StatusesLong:    h.wbSrv.URL + "/ajax/statuses/longtext",
				StatusesMyMblog: h.wbSrv.URL + "/ajax/statuses/mymblog",
				TVComponent:     h.wbSrv.URL + "/tv/api/component",
				Friends:         h.wbSrv.URL + "/ajax/friendships/friends",
				AllGroups:       h.wbSrv.URL + "/ajax/feed/allGroups",
				AllFavorites:    h.wbSrv.URL + "/ajax/favorites/all_fav",
				LikeList:        h.wbSrv.URL + "/ajax/statuses/likelist",
			},
			MinInterval:    -1,
			JitterMax:      -1,
			AllowLocalhost: true,
		},
	}
	for _, fn := range extraOpts {
		if fn != nil {
			fn(&opts)
		}
	}
	b, err := NewBackend(opts)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	h.backend = b
	return h
}

func (h *weiboThreeSiteHarness) weiboPostJSON(id, cdn string) string {
	switch id {
	case synthWeiboMidLong:
		return fmt.Sprintf(`{"ok":1,"idstr":%q,"mid":%q,"mblogid":"LongPost02","created_at":"Mon Nov 18 09:30:00 +0800 2026","text_raw":"长微博截断预览...","isLongText":true,"user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"reposts_count":3,"comments_count":4,"attitudes_count":15,"pic_ids":["piclong1"],"pic_infos":{"piclong1":{"pic_id":"piclong1","type":"pic","largest":{"url":%q,"width":48,"height":48}}}}`,
			synthWeiboMidLong, synthWeiboMidLong, synthWeiboUID, synthWeiboName, cdn+"/avatar_creator.png", cdn+"/pic_long1.png")
	case synthWeiboMidRT:
		return fmt.Sprintf(`{"ok":1,"idstr":%q,"mid":%q,"mblogid":"RtPost0003","created_at":"Mon Nov 18 09:20:00 +0800 2026","text_raw":"转发一条原创","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"retweeted_status":{"idstr":"5090000000000088","mid":"5090000000000088","mblogid":"OrigPost88","created_at":"Mon Nov 18 08:00:00 +0800 2026","text_raw":"原作者正文","user":{"idstr":"5100000088","screen_name":"原作者","avatar_hd":%q},"pic_ids":["orig1"],"pic_infos":{"orig1":{"pic_id":"orig1","type":"pic","largest":{"url":%q,"width":48,"height":48}}}}}`,
			synthWeiboMidRT, synthWeiboMidRT, synthWeiboUID, synthWeiboName, cdn+"/avatar_creator.png", cdn+"/avatar_creator.png", cdn+"/orig1.png")
	case synthWeiboMidFast:
		return fmt.Sprintf(`{"ok":1,"idstr":%q,"mid":%q,"mblogid":"FastRt0004","created_at":"Mon Nov 18 09:10:00 +0800 2026","text_raw":"快转原帖正文","ori_mid":"5090000000000089","title":{"text":"快转微博"},"user":{"idstr":"5100000089","screen_name":"快转原作者","avatar_hd":%q}}`,
			synthWeiboMidFast, synthWeiboMidFast, cdn+"/avatar_creator.png")
	case synthWeiboMidDelRT:
		return fmt.Sprintf(`{"ok":1,"idstr":%q,"mid":%q,"mblogid":"DelRt00006","created_at":"Mon Nov 18 09:05:00 +0800 2026","text_raw":"转发但原帖已删除","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"retweeted_status":{"idstr":"5090000000000077","mid":"5090000000000077","deleted":"1","text":"抱歉，此微博已被作者删除。"}}`,
			synthWeiboMidDelRT, synthWeiboMidDelRT, synthWeiboUID, synthWeiboName, cdn+"/avatar_creator.png")
	case synthWeiboMidHost:
		return fmt.Sprintf(`{"ok":1,"idstr":%q,"mid":%q,"mblogid":"HostVid005","created_at":"Mon Nov 18 09:00:00 +0800 2026","text_raw":"带视频的原创微博","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"reposts_count":7,"comments_count":8,"attitudes_count":99,"page_info":{"type":"11","object_type":"video","object_id":%q,"page_title":"带宿主帖的合成视频","page_pic":%q,"media_info":{"duration":2,"playback_list":[{"meta":{"label":"mp4_1080p","quality_index":1080,"quality_label":"1080P","quality_desc":"高清"},"play_info":{"url":%q,"mime":"video/mp4","width":1920,"height":1080,"fps":30,"bitrate":2000000,"size":40,"duration":2,"video_codecs":"avc1"}}]}}}`,
			synthWeiboMidHost, synthWeiboMidHost, synthWeiboUID, synthWeiboName, cdn+"/avatar_creator.png", synthWeiboOIDHost, cdn+"/cover_host.png", cdn+"/host_video.mp4?ssig=SIGN_HOST&Expires=1999999999&KID=unistore,video")
	default:
		// synthWeiboMidPlain: multi-media (image + gif) + degraded article card + visible label + online stats.
		return fmt.Sprintf(`{"ok":1,"idstr":%q,"mid":%q,"mblogid":"PlainPost1","created_at":"Mon Nov 18 10:00:00 +0800 2026","text_raw":"合成普通原创图文微博","visible":{"type":10},"reposts_count":11,"comments_count":22,"attitudes_count":333,"user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"pic_ids":["pic1","gif1"],"pic_infos":{"pic1":{"pic_id":"pic1","type":"pic","largest":{"url":%q,"width":48,"height":48}},"gif1":{"pic_id":"gif1","type":"gif","largest":{"url":%q,"width":48,"height":48}}},"page_info":{"type":"article","object_type":"article","page_title":"合成头条文章","page_desc":"文章摘要","page_url":"https://weibo.com/ttarticle/p/show?id=2309400000001"}}`,
			synthWeiboMidPlain, synthWeiboMidPlain, synthWeiboUID, synthWeiboName, cdn+"/avatar_creator.png", cdn+"/pic1.png", cdn+"/anim1.gif")
	}
}

func waitDownloadTerminal(t *testing.T, b *Backend, taskID string) download.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tasks, _ := b.ListDownloadTasks()
		for _, task := range tasks {
			if task.TaskID == taskID && (task.Status == "completed" || task.Status == "partial" || task.Status == "failed" || task.Status == "canceled" || task.Status == "paused") {
				return task
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("download task %s did not reach terminal state; tasks=%+v", taskID, tasks)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func assertNoSentinelsInTree(t *testing.T, root string, sentinels []string) {
	t.Helper()
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, rErr := os.ReadFile(p)
		if rErr != nil {
			return nil
		}
		for _, s := range sentinels {
			if s != "" && bytes.Contains(raw, []byte(s)) {
				t.Errorf("sentinel %q leaked into file %s", s, p)
			}
		}
		return nil
	})
}

func TestThreeSiteAuthIsolationAndWeiboCookieVerification(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()

	if h.backend.HasAnyLogin() {
		t.Fatal("fresh backend reported HasAnyLogin=true")
	}

	// Unlogged Weibo calls must fail with auth_required and 0 HTTP requests.
	if _, err := h.backend.WeiboUser(ctx, synthWeiboUID); err == nil || !strings.Contains(err.Error(), "请先登录微博") {
		t.Fatalf("unlogged WeiboUser err = %v, want 请先登录微博", err)
	}
	if _, err := h.backend.WeiboPost(ctx, synthWeiboMidPlain); err == nil || !strings.Contains(err.Error(), "请先登录微博") {
		t.Fatalf("unlogged WeiboPost err = %v, want 请先登录微博", err)
	}
	if _, err := h.backend.OpenLink(ctx, "https://weibo.com/u/"+synthWeiboUID); err == nil || site.KindOf(err) != site.ErrKindAuthRequired {
		t.Fatalf("unlogged OpenLink weibo err = %v, want auth_required", err)
	}
	if h.wbReqs.Load() != 0 {
		t.Fatalf("unlogged Weibo operations sent %d HTTP requests, want 0", h.wbReqs.Load())
	}

	// Missing SUB -> rejected with 0 requests.
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", "SUBP=only_subp; XSRF-TOKEN=abc", ""); err == nil {
		t.Fatal("LoginWithCookie without SUB unexpectedly succeeded")
	}
	if h.wbReqs.Load() != 0 {
		t.Fatalf("missing SUB sent %d HTTP requests, want 0", h.wbReqs.Load())
	}

	// Unknown site must be explicitly rejected (no silent fallback to Douyin/Bilibili).
	if _, err := h.backend.LoginWithCookie(ctx, "unknown_site", synthWeiboCookie(), ""); err == nil {
		t.Fatal("LoginWithCookie accepted unknown site")
	}
	if _, err := h.backend.VerifySiteAccount(ctx, "unknown_site"); err == nil {
		t.Fatal("VerifySiteAccount accepted unknown site")
	}
	if _, err := h.backend.LogoutSite("unknown_site"); err == nil {
		t.Fatal("LogoutSite accepted unknown site")
	}
	if h.backend.Renewal("unknown_site").Available {
		t.Fatal("Renewal(unknown_site) should not be available")
	}

	// Unauthenticated / mismatched / 418 cookies must fail without saving and without echoing secrets.
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", "SUB=expired_secret_999", ""); err == nil || strings.Contains(err.Error(), "expired_secret_999") {
		t.Fatalf("expired Weibo cookie err = %v", err)
	}
	h.wbMode.Store("mismatch")
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err == nil {
		t.Fatal("screen_name mismatch unexpectedly succeeded")
	}
	h.wbMode.Store("risk418")
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err == nil {
		t.Fatal("risk 418 unexpectedly succeeded")
	}
	if h.backend.HasAnyLogin() {
		t.Fatal("failed Weibo logins must leave store unlogged")
	}

	// Valid PC 2-step Weibo login succeeds and allows entering app alone.
	h.wbMode.Store("ok")
	st, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), "")
	if err != nil {
		t.Fatalf("valid LoginWithCookie(weibo): %v", err)
	}
	if !st.LoggedIn || st.UID != synthWeiboSelfUID || st.Uname != synthWeiboSelfName {
		t.Fatalf("Weibo login status = %+v", st)
	}
	if !h.backend.HasAnyLogin() {
		t.Fatal("Weibo-only login should satisfy HasAnyLogin")
	}
	if h.backend.Renewal("weibo").Available {
		t.Fatal("Weibo auto-renewal should not be available")
	}

	// Statuses lists all supported sites; Bilibili & Douyin remain unlogged and send 0 requests.
	statuses := h.backend.Statuses()
	if len(statuses) != len(site.All()) {
		t.Fatalf("Statuses count = %d, want %d (%+v)", len(statuses), len(site.All()), statuses)
	}
	if _, err := h.backend.OpenLink(ctx, "https://www.bilibili.com/video/BV1xx411c7mD"); err == nil {
		t.Fatal("unlogged Bilibili OpenLink unexpectedly succeeded")
	}
	if _, err := h.backend.DouyinUser(ctx, "MS4wLjABAAAA_synth_dy_zero_posts"); err == nil {
		t.Fatal("unlogged DouyinUser unexpectedly succeeded")
	}
	if h.biliReqs.Load() != 0 || h.dyReqs.Load() != 0 {
		t.Fatalf("unlogged Bilibili/Douyin sent requests: bili=%d dy=%d", h.biliReqs.Load(), h.dyReqs.Load())
	}

	// VerifySiteAccount & BootstrapAuth work for Weibo while leaving unlogged Bilibili/Douyin at 0 requests.
	if vst, err := h.backend.VerifySiteAccount(ctx, "weibo"); err != nil || !vst.LoggedIn {
		t.Fatalf("VerifySiteAccount(weibo) = %+v, err=%v", vst, err)
	}
	if _, err := h.backend.BootstrapAuth(ctx); err != nil {
		t.Fatalf("BootstrapAuth with Weibo only err=%v", err)
	}
	if !h.backend.StatusOf("weibo").LoggedIn || h.biliReqs.Load() != 0 || h.dyReqs.Load() != 0 {
		t.Fatalf("after BootstrapAuth: weibo=%+v biliReqs=%d dyReqs=%d", h.backend.StatusOf("weibo"), h.biliReqs.Load(), h.dyReqs.Load())
	}

	// Log in Bilibili and Douyin too; verify LogoutSite("weibo") only clears Weibo memory caches and leaves other sites logged in.
	if _, err := h.backend.LoginWithCookie(ctx, "bilibili", "SESSDATA=synth_bili_sess; bili_jct=synth_csrf; DedeUserID=778899", ""); err != nil {
		t.Fatalf("LoginWithCookie(bilibili): %v", err)
	}
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_sess; ttwid=synth_ttwid", ""); err != nil {
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}
	// Populate Weibo memory caches before logging out.
	if _, err := h.backend.WeiboPost(ctx, synthWeiboMidPlain); err != nil {
		t.Fatalf("WeiboPost: %v", err)
	}
	if _, err := h.backend.AccountPage(ctx, "weibo", AccountFollowing, "", ""); err != nil {
		t.Fatalf("AccountPage(weibo, following): %v", err)
	}
	if _, err := h.backend.LogoutSite("weibo"); err != nil {
		t.Fatalf("LogoutSite(weibo): %v", err)
	}
	if h.backend.StatusOf("weibo").LoggedIn {
		t.Fatal("Weibo still logged in after LogoutSite")
	}
	if !h.backend.StatusOf("bilibili").LoggedIn || !h.backend.StatusOf("douyin").LoggedIn {
		t.Fatalf("LogoutSite(weibo) affected other sites: bili=%+v dy=%+v", h.backend.StatusOf("bilibili"), h.backend.StatusOf("douyin"))
	}
	if _, ok := h.backend.weiboPostCache.get(synthWeiboMidPlain, "", 0); ok {
		t.Fatal("LogoutSite(weibo) did not clear in-memory weiboPostCache")
	}
	// With Bilibili & Douyin logged in but Weibo logged out, OpenLink on a Weibo URL must say "请先登录微博" with 0 requests.
	wbReqsAfterLogout := h.wbReqs.Load()
	if _, err := h.backend.OpenLink(ctx, "https://weibo.com/u/"+synthWeiboUID); err == nil || !strings.Contains(err.Error(), "请先登录微博") {
		t.Fatalf("OpenLink(weibo) while only other sites logged in err = %v, want 请先登录微博", err)
	}
	if h.wbReqs.Load() != wbReqsAfterLogout {
		t.Fatalf("unlogged Weibo OpenLink sent %d requests, want 0", h.wbReqs.Load()-wbReqsAfterLogout)
	}

	// Re-login Weibo, enqueue a Weibo queue task + Bilibili queue task, trigger Weibo auth expiry -> pauses ONLY Weibo.
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("re-login Weibo: %v", err)
	}
	wbTask, err := h.backend.enqueueCacheQueueWithPager(site.Weibo, "posts", synthWeiboUID, CacheQueueRequest{
		Site: "weibo", User: synthWeiboUID, UserName: synthWeiboName, List: "posts",
	}, nil, func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
		<-ctx.Done()
		return nil, "", false, ctx.Err()
	})
	if err != nil {
		t.Fatalf("enqueue Weibo queue: %v", err)
	}
	biliTask, err := h.backend.enqueueCacheQueueWithPager(site.Bilibili, "videos", "12345", CacheQueueRequest{
		Site: "bilibili", User: "12345", UserName: "合成UP", List: "videos",
	}, nil, func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
		<-ctx.Done()
		return nil, "", false, ctx.Err()
	})
	if err != nil {
		t.Fatalf("enqueue Bilibili queue: %v", err)
	}

	h.wbMode.Store("expired")
	_, _ = h.backend.VerifySiteAccount(ctx, "weibo")

	tasks := h.backend.ListCacheQueue()
	var wbState, biliState string
	for _, tk := range tasks {
		if tk.ID == wbTask.ID {
			wbState = tk.Status
		}
		if tk.ID == biliTask.ID {
			biliState = tk.Status
		}
	}
	if wbState != CacheQueuePaused {
		t.Fatalf("Weibo queue status after Weibo auth expiry = %q, want paused", wbState)
	}
	if biliState == CacheQueuePaused {
		t.Fatalf("Bilibili queue was paused by Weibo auth expiry (state=%q)", biliState)
	}

	// Sentinel check across entire data/logs/credentials directory tree: zero Weibo Cookie values on disk!
	assertNoSentinelsInTree(t, h.loc.DataDir, []string{synthWeiboSUB, synthWeiboSUBP, synthWeiboXSRF, synthWeiboWBPSESS})
	assertNoSentinelsInTree(t, h.loc.CredentialsDir, []string{synthWeiboSUB, synthWeiboSUBP, synthWeiboXSRF, synthWeiboWBPSESS})
	assertNoSentinelsInTree(t, h.loc.LogsDir, []string{synthWeiboSUB, synthWeiboSUBP, synthWeiboXSRF, synthWeiboWBPSESS})
}

func TestWeiboOpenLinkDetailUserVideoAndCaching(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	runCtx, cancelRun := context.WithCancel(context.Background())
	if err := h.backend.dl.Start(runCtx); err != nil {
		cancelRun()
		t.Fatalf("dl.Start: %v", err)
	}
	t.Cleanup(cancelRun)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	// 1. Weibo live link returns unsupported message with 0 Weibo API requests.
	beforeLive := h.wbReqs.Load()
	if _, err := h.backend.OpenLink(ctx, "https://weibo.com/l/wblive/p/show/1022:2321325090000001"); err == nil || !strings.Contains(err.Error(), "微博直播暂不支持录制") {
		t.Fatalf("OpenLink weibo live err = %v, want unsupported message", err)
	}
	if h.wbReqs.Load() != beforeLive {
		t.Fatalf("Weibo live OpenLink sent %d requests, want 0", h.wbReqs.Load()-beforeLive)
	}

	// 2. OpenLink on Weibo post: rich fields, degraded card, visible label, online stats, history + local thumb.
	res, err := h.backend.OpenLink(ctx, "https://weibo.com/"+synthWeiboUID+"/"+synthWeiboMidPlain)
	if err != nil {
		t.Fatalf("OpenLink weibo post: %v", err)
	}
	if res.WeiboPost == nil || res.WeiboPost.Mid != synthWeiboMidPlain {
		t.Fatalf("OpenLink post result = %+v", res)
	}
	if res.WeiboPost.VisibleLabel == "" || res.WeiboPost.Card == nil || !res.WeiboPost.Card.Unsupported {
		t.Fatalf("expected visible label & degraded card, got %+v", res.WeiboPost)
	}
	if res.WeiboPost.Stats.AttitudesCount != 333 || len(res.WeiboPost.Images) != 2 {
		t.Fatalf("unexpected stats/media: %+v", res.WeiboPost)
	}
	if res.WeiboPost.CacheKey != "weibo:dynamic:"+synthWeiboMidPlain {
		t.Fatalf("cacheKey = %q", res.WeiboPost.CacheKey)
	}
	if res.HistoryThumbs == nil || res.HistoryThumbs.Site != "weibo" {
		t.Fatalf("expected HistoryThumbs job for weibo, got %+v", res.HistoryThumbs)
	}
	if err := h.backend.FetchHistoryThumbs(ctx, *res.HistoryThumbs); err != nil {
		t.Fatalf("FetchHistoryThumbs(weibo): %v", err)
	}
	hist := h.backend.ListHistory()
	if len(hist) == 0 || hist[0].Site != "weibo" || hist[0].CoverThumb == "" {
		t.Fatalf("history entry missing site=weibo or local cover thumb: %+v", hist)
	}
	thumbPath, err := h.backend.HistoryThumbPath(hist[0].CoverThumb)
	if err != nil {
		t.Fatalf("HistoryThumbPath: %v", err)
	}
	if _, statErr := os.Stat(thumbPath); statErr != nil {
		t.Fatalf("expected local history thumb file at %s: %v", thumbPath, statErr)
	}

	// 3. CacheWeiboPost reuses in-memory cached post plan (0 extra statuses/show calls!).
	showBefore := h.wbShowReqs.Load()
	task, err := h.backend.CacheWeiboPost(ctx, synthWeiboMidPlain, nil)
	if err != nil {
		t.Fatalf("CacheWeiboPost: %v", err)
	}
	doneTask := waitDownloadTerminal(t, h.backend, task.TaskID)
	if doneTask.Status != "completed" {
		t.Fatalf("CacheWeiboPost task status = %s err=%s", doneTask.Status, doneTask.ErrorMessage)
	}
	if h.wbShowReqs.Load() != showBefore {
		t.Fatalf("CacheWeiboPost made %d extra statuses/show calls despite in-memory post cache", h.wbShowReqs.Load()-showBefore)
	}
	// Verify post now reports green DotState.
	postAfter, err := h.backend.WeiboPost(ctx, synthWeiboMidPlain)
	if err != nil || postAfter.CacheState.Dot != "green" {
		t.Fatalf("post after cache = %+v err=%v", postAfter, err)
	}

	// 4. CacheWeiboPost with forward via places item into forwards/<mid>/ with via metadata.
	fwdVia := &library.ItemVia{Reason: library.ViaForward, UID: synthWeiboUID, Name: synthWeiboName}
	fwdTask, err := h.backend.CacheWeiboPost(ctx, synthWeiboMidLong, fwdVia)
	if err != nil {
		t.Fatalf("CacheWeiboPost(long + via): %v", err)
	}
	doneFwd := waitDownloadTerminal(t, h.backend, fwdTask.TaskID)
	if doneFwd.Status != "completed" {
		t.Fatalf("forward CacheWeiboPost status = %s err=%s", doneFwd.Status, doneFwd.ErrorMessage)
	}
	cachedFwd, ok, err := h.backend.LibraryStore().GetItem("weibo:dynamic:" + synthWeiboMidLong)
	if err != nil || !ok || cachedFwd.Via == nil || cachedFwd.Via.Reason != library.ViaForward || !strings.Contains(filepath.ToSlash(cachedFwd.ItemDir), "/forwards/") {
		t.Fatalf("cached forward item = %+v ok=%v err=%v", cachedFwd, ok, err)
	}

	// 5. Retweet with deleted origin preserves unavailable state.
	delPost, err := h.backend.WeiboPost(ctx, synthWeiboMidDelRT)
	if err != nil || delPost.Retweeted == nil || !delPost.Retweeted.Unavailable {
		t.Fatalf("deleted origin retweet = %+v err=%v", delPost, err)
	}

	// 6. 1034: video with host mid normalizes to host WeiboPost; standalone 1034: caches as weibo:video:1034_<id>.
	hostRes, err := h.backend.OpenLink(ctx, "https://weibo.com/tv/show/"+synthWeiboOIDHost)
	if err != nil || hostRes.WeiboPost == nil || hostRes.WeiboPost.Mid != synthWeiboMidHost {
		t.Fatalf("host video OpenLink = %+v err=%v", hostRes, err)
	}
	aloneRes, err := h.backend.OpenLink(ctx, "https://weibo.com/tv/show/"+synthWeiboOIDAlone)
	if err != nil || aloneRes.WeiboVideo == nil || aloneRes.WeiboVideo.CacheKey != "weibo:video:1034_5090000000000022" {
		t.Fatalf("standalone video OpenLink = %+v err=%v", aloneRes, err)
	}
	vidTask, err := h.backend.CacheWeiboVideo(ctx, synthWeiboOIDAlone)
	if err != nil {
		t.Fatalf("CacheWeiboVideo: %v", err)
	}
	doneVid := waitDownloadTerminal(t, h.backend, vidTask.TaskID)
	if doneVid.Status != "completed" {
		t.Fatalf("CacheWeiboVideo status = %s err=%s", doneVid.Status, doneVid.ErrorMessage)
	}
	if _, ok, _ := h.backend.LibraryStore().GetItem("weibo:video:1034_5090000000000022"); !ok {
		t.Fatal("standalone weibo video not indexed in library")
	}

	// 7. User page (/n/ and custom domain -> stable UID), saves statuses_count, refreshes avatar for subscribed account, paginates posts.
	sub, err := h.backend.CreateSubscription(
		"微博分组",
		"",
		[]subscription.AccountRef{{Site: "weibo", UID: synthWeiboUID, Name: synthWeiboName}},
		subscription.SubRules{},
	)
	if err != nil || sub.ID == "" {
		t.Fatalf("CreateSubscription(weibo): %v", err)
	}

	uRes, err := h.backend.OpenLink(ctx, "https://weibo.com/"+synthWeiboDomain)
	if err != nil || uRes.WeiboUser == nil || uRes.WeiboUser.UID != synthWeiboUID {
		t.Fatalf("OpenLink custom domain = %+v err=%v", uRes, err)
	}
	if uRes.WeiboUserPosts == nil || len(uRes.WeiboUserPosts.Posts) == 0 || uRes.WeiboUserPosts.TotalWorks != 42 {
		t.Fatalf("WeiboUserPosts page 1 = %+v", uRes.WeiboUserPosts)
	}
	// Verify statuses_count was recorded in account_works.
	if aw := h.backend.SubscriptionAccountWorks()["weibo:"+synthWeiboUID]; aw.Total != 42 {
		t.Fatalf("SubscriptionAccountWorks[weibo:%s] = %+v, want Total=42", synthWeiboUID, aw)
	}
	// Verify page 2 pagination and bottom tips.
	h.wbBottomTips.Store(true)
	page2, err := h.backend.WeiboUserPosts(ctx, synthWeiboUID, uRes.WeiboUserPosts.NextCursor)
	if err != nil {
		t.Fatalf("WeiboUserPosts page 2: %v", err)
	}
	if !page2.BottomTipsVisible || !strings.Contains(page2.BottomTipsText, "半年") {
		t.Fatalf("expected bottom tips on page 2, got %+v", page2)
	}

	// Verify /n/<screen_name> resolves cleanly too.
	nickRes, err := h.backend.OpenLink(ctx, "https://weibo.com/n/"+url.PathEscape(synthWeiboName))
	if err != nil || nickRes.WeiboUser == nil || nickRes.WeiboUser.UID != synthWeiboUID {
		t.Fatalf("OpenLink /n/ = %+v err=%v", nickRes, err)
	}
}

func TestDouyinZeroPostProfileOpenLinkAndDirectPage(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "douyin", "sessionid=synth_dy_zero_sess; ttwid=synth", ""); err != nil {
		t.Fatalf("LoginWithCookie(douyin): %v", err)
	}

	const (
		dyZeroSec    = "MS4wLjABAAAA_synth_dy_zero_posts"
		dyNonZeroSec = "MS4wLjABAAAA_synth_dy_non_zero_empty"
	)

	// OpenLink on 0-post Douyin profile with aweme_list: [] -> clean empty, UncertainEmpty=false, still logged in.
	res, err := h.backend.OpenLink(ctx, "https://www.douyin.com/user/"+dyZeroSec)
	if err != nil {
		t.Fatalf("OpenLink 0-post Douyin user: %v", err)
	}
	if res.DouyinUserWorksError != "" || res.DouyinUserWorks == nil || len(res.DouyinUserWorks.Works) != 0 || res.DouyinUserWorks.UncertainEmpty {
		t.Fatalf("0-post OpenLink ([]): works=%+v err=%q", res.DouyinUserWorks, res.DouyinUserWorksError)
	}
	if !h.backend.StatusOf("douyin").LoggedIn {
		t.Fatal("0-post Douyin user wrongly expired Douyin login")
	}

	// Same with aweme_list: null -> clean empty via KnownProfile + CrossCheckProfile.
	h.dyZeroAwemeNull.Store(true)
	resNull, err := h.backend.OpenLink(ctx, "https://www.douyin.com/user/"+dyZeroSec)
	if err != nil {
		t.Fatalf("OpenLink 0-post Douyin user (null): %v", err)
	}
	if resNull.DouyinUserWorksError != "" || resNull.DouyinUserWorks == nil || resNull.DouyinUserWorks.UncertainEmpty {
		t.Fatalf("0-post OpenLink (null): works=%+v err=%q", resNull.DouyinUserWorks, resNull.DouyinUserWorksError)
	}

	// Direct DouyinUserWorks on 0-post user (without OpenLink) cross-checks profile and succeeds cleanly.
	directZero, err := h.backend.DouyinUserWorks(ctx, dyZeroSec, "")
	if err != nil || directZero == nil || directZero.UncertainEmpty || len(directZero.Works) != 0 {
		t.Fatalf("direct DouyinUserWorks(0-post) = %+v err=%v", directZero, err)
	}

	// Direct DouyinUserWorks on aweme_count > 0 empty shell still flags UncertainEmpty (no false negative).
	h.dyZeroAwemeNull.Store(false)
	directShell, err := h.backend.DouyinUserWorks(ctx, dyNonZeroSec, "")
	if err != nil {
		t.Fatalf("direct DouyinUserWorks(non-zero empty): %v", err)
	}
	if !directShell.UncertainEmpty {
		t.Fatalf("expected UncertainEmpty=true for aweme_count=5 empty shell, got %+v", directShell)
	}
	if !h.backend.StatusOf("douyin").LoggedIn {
		t.Fatal("Douyin login state should remain logged in")
	}
}

// Sanity check that JSON-serialized WeiboPostView omits raw cookies and never fails marshaling.
func TestWeiboPostViewJSONContract(t *testing.T) {
	h := newWeiboThreeSiteHarness(t)
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie: %v", err)
	}
	pv, err := h.backend.WeiboPost(ctx, synthWeiboMidPlain)
	if err != nil {
		t.Fatalf("WeiboPost: %v", err)
	}
	raw, err := json.Marshal(pv)
	if err != nil {
		t.Fatalf("marshal WeiboPostView: %v", err)
	}
	if !strings.Contains(string(raw), `"cacheKey":"weibo:dynamic:`) {
		t.Fatalf("unexpected JSON: %s", raw)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("unexpected canceled error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func synthWeiboQuickTimeMovBytes(t *testing.T) []byte {
	t.Helper()
	baseMP4 := synthWeiboMP4Bytes(t)
	parsed, err := mp4.DecodeFile(bytes.NewReader(baseMP4))
	if err != nil || parsed == nil || parsed.Moov == nil || parsed.Mdat == nil {
		t.Fatalf("DecodeFile base mp4: %v", err)
	}
	qtFtyp := mp4.NewFtyp("qt  ", 0x20050300, []string{"qt  "})
	// Put mdat before moov and adjust stco/co64 chunk offsets so it exercises full QuickTime repackaging.
	oldMdatStart := uint64(0)
	for _, ch := range parsed.Children {
		if ch == parsed.Mdat {
			oldMdatStart += parsed.Mdat.HeaderSize()
			break
		}
		oldMdatStart += ch.Size()
	}
	newMdatStart := qtFtyp.Size() + parsed.Mdat.HeaderSize()
	delta := int64(newMdatStart) - int64(oldMdatStart)
	for _, trak := range parsed.Moov.Traks {
		if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		if stbl.Stco != nil {
			for i := range stbl.Stco.ChunkOffset {
				stbl.Stco.ChunkOffset[i] = uint32(int64(stbl.Stco.ChunkOffset[i]) + delta)
			}
		}
		if stbl.Co64 != nil {
			for i := range stbl.Co64.ChunkOffset {
				stbl.Co64.ChunkOffset[i] = uint64(int64(stbl.Co64.ChunkOffset[i]) + delta)
			}
		}
	}
	var out bytes.Buffer
	for _, box := range []mp4.Box{qtFtyp, parsed.Mdat, parsed.Moov} {
		if err := box.Encode(&out); err != nil {
			t.Fatalf("encode qt mov box %s: %v", box.Type(), err)
		}
	}
	raw := out.Bytes()
	if !isQuickTimeContainerBytes(raw) {
		t.Fatalf("expected qt  ftyp header, got %q", raw[:min(12, len(raw))])
	}
	return raw
}

func TestStagePreviewMediaWeiboLivePhotoRedirectAndQuickTimeRepackage(t *testing.T) {
	qtMov := synthWeiboQuickTimeMovBytes(t)

	var evilHits atomic.Int32
	var loopSecondHits atomic.Int32
	var mu sync.Mutex
	var seenReqHosts []string

	// Intercept synthetic HTTPS LivePhoto & CDN hosts while forwarding localhost API calls to httptest servers.
	baseTransport := http.DefaultTransport
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		host := strings.ToLower(r.URL.Hostname())
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			return baseTransport.RoundTrip(r)
		}
		mu.Lock()
		seenReqHosts = append(seenReqHosts, host+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Cookie") != "" {
			t.Errorf("LivePhoto preview request to %s leaked Cookie header", r.URL.String())
		}
		if r.Header.Get("Referer") != weibo.DefaultReferer {
			t.Errorf("LivePhoto preview request to %s missing Referer %q (got %q)", r.URL.String(), weibo.DefaultReferer, r.Header.Get("Referer"))
		}

		makeResp := func(status int, ct string, loc string, body []byte) *http.Response {
			hdr := make(http.Header)
			if ct != "" {
				hdr.Set("Content-Type", ct)
			}
			if loc != "" {
				hdr.Set("Location", loc)
			}
			return &http.Response{
				StatusCode: status,
				Header:     hdr,
				Body:       ioNopCloser(bytes.NewReader(body)),
				Request:    r,
			}
		}

		switch {
		case host == "video.weibo.com" && r.URL.Path == "/media/play":
			switch r.URL.Query().Get("case") {
			case "ok":
				return makeResp(http.StatusFound, "", "https://livephoto.us.sinaimg.cn/synth_clip.mov", nil), nil
			case "evil":
				return makeResp(http.StatusFound, "", "https://evil.example.com/clip.mov", nil), nil
			case "loop":
				if r.URL.Query().Get("second") == "1" {
					loopSecondHits.Add(1)
				}
				return makeResp(http.StatusFound, "", "https://video.weibo.com/media/play?case=loop&second=1", nil), nil
			case "html":
				return makeResp(http.StatusFound, "", "https://livephoto.us.sinaimg.cn/login.html", nil), nil
			default:
				return makeResp(http.StatusNotFound, "text/plain", "", []byte("not found")), nil
			}
		case host == "livephoto.us.sinaimg.cn" && r.URL.Path == "/synth_clip.mov":
			return makeResp(http.StatusOK, "video/quicktime", "", qtMov), nil
		case host == "livephoto.us.sinaimg.cn" && r.URL.Path == "/direct.mov":
			return makeResp(http.StatusOK, "video/quicktime", "", qtMov), nil
		case host == "livephoto.us.sinaimg.cn" && r.URL.Path == "/login.html":
			return makeResp(http.StatusOK, "text/html; charset=utf-8", "", []byte("<html><body>login</body></html>")), nil
		case host == "evil.example.com":
			evilHits.Add(1)
			return makeResp(http.StatusOK, "video/mp4", "", qtMov), nil
		default:
			return makeResp(http.StatusNotFound, "", "", nil), nil
		}
	})

	h := newWeiboThreeSiteHarness(t, func(o *Options) {
		o.WeiboOpts.Transport = rt
	})
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie(weibo): %v", err)
	}

	// 1. LivePhoto initial hop (https://video.weibo.com/media/play) -> allowed CDN redirect -> QuickTime .mov repackaged to .mp4.
	stagedPath, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://video.weibo.com/media/play?case=ok", ".mov")
	if err != nil {
		t.Fatalf("StagePreviewMedia(LivePhoto ok): %v", err)
	}
	if !strings.HasSuffix(stagedPath, ".mp4") {
		t.Fatalf("expected repackaged .mp4 extension, got %s", stagedPath)
	}
	stagedBytes, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("read staged preview: %v", err)
	}
	if isQuickTimeContainerBytes(stagedBytes) {
		t.Fatal("staged LivePhoto preview still has QuickTime ftyp=qt  header instead of ISO BMFF mp4")
	}
	summary, err := download.InspectMP4FileContext(ctx, stagedPath)
	if err != nil || !summary.MoovBeforeMdat {
		t.Fatalf("InspectMP4FileContext(staged LivePhoto) = %+v err=%v, want MoovBeforeMdat=true", summary, err)
	}

	// 2. Direct CDN QuickTime .mov also repackages to .mp4.
	directPath, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://livephoto.us.sinaimg.cn/direct.mov", ".mov")
	if err != nil {
		t.Fatalf("StagePreviewMedia(direct .mov): %v", err)
	}
	if !strings.HasSuffix(directPath, ".mp4") {
		t.Fatalf("expected .mp4 for direct .mov, got %s", directPath)
	}
	if s2, err := download.InspectMP4FileContext(ctx, directPath); err != nil || !s2.MoovBeforeMdat {
		t.Fatalf("InspectMP4FileContext(direct .mov) = %+v err=%v", s2, err)
	}

	// 3. Non-allowlist redirect target MUST be rejected with ErrKindBlocked before contacting evil host.
	if _, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://video.weibo.com/media/play?case=evil", ".mov"); err == nil || site.KindOf(err) != site.ErrKindBlocked {
		t.Fatalf("evil redirect err = %v, want ErrKindBlocked", err)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("blocked evil redirect host received %d requests, want 0", evilHits.Load())
	}

	// 4. Redirect loop back to video.weibo.com/media/play MUST be rejected (initial rule does not apply to redirects).
	if _, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://video.weibo.com/media/play?case=loop", ".mov"); err == nil || site.KindOf(err) != site.ErrKindBlocked {
		t.Fatalf("loop redirect err = %v, want ErrKindBlocked", err)
	}
	if loopSecondHits.Load() != 0 {
		t.Fatalf("blocked redirect back to video.weibo.com received %d requests, want 0", loopSecondHits.Load())
	}

	// 5. HTML disguise on allowed CDN host MUST be rejected with ErrKindBlocked.
	if _, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://video.weibo.com/media/play?case=html", ".mov"); err == nil || site.KindOf(err) != site.ErrKindBlocked {
		t.Fatalf("HTML disguised LivePhoto err = %v, want ErrKindBlocked", err)
	}
}

func TestWeiboRetweetPlanCacheRefusalForwarderHintAndVideoPreview(t *testing.T) {
	// 1. canReuseWeiboPostPlan refuses incomplete retweets, fast-retweets, and retweets missing Retweeted.
	if canReuseWeiboPostPlan(&weibo.Post{Mid: "5001", Author: weibo.AuthorSummary{UID: "1001"}, IsRetweet: true, OriginIncomplete: true, Retweeted: &weibo.Post{Mid: "4001"}}) {
		t.Fatal("canReuseWeiboPostPlan accepted OriginIncomplete retweet")
	}
	if canReuseWeiboPostPlan(&weibo.Post{Mid: "5002", Author: weibo.AuthorSummary{UID: "1001"}, IsRetweet: true, IsFastRetweet: true, OriMid: "4002"}) {
		t.Fatal("canReuseWeiboPostPlan accepted fast-retweet")
	}
	if canReuseWeiboPostPlan(&weibo.Post{Mid: "5003", Author: weibo.AuthorSummary{UID: "1001"}, IsRetweet: true, Retweeted: nil}) {
		t.Fatal("canReuseWeiboPostPlan accepted retweet with nil Retweeted")
	}
	if !canReuseWeiboPostPlan(&weibo.Post{Mid: "5004", Author: weibo.AuthorSummary{UID: "1001"}, IsRetweet: true, Retweeted: &weibo.Post{Mid: "4004", Text: "完整原博"}}) {
		t.Fatal("canReuseWeiboPostPlan rejected complete retweet")
	}

	// 2. Direct link OpenLink preserves ForwarderHintUID & resolves subscribed ForwarderHintName;
	// StagePreviewMedia supports allowlisted weibocdn.com video redirect without leaking Cookie.
	const (
		subUID       = "1000000088"
		videoMP4Stub = "synthetic-weibo-mp4-preview-bytes"
	)
	var evilHits atomic.Int32
	baseTransport := http.DefaultTransport
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		host := strings.ToLower(r.URL.Hostname())
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			return baseTransport.RoundTrip(r)
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("video preview request leaked Cookie to %s", r.URL.String())
		}
		mkResp := func(status int, ct, loc string, body []byte) *http.Response {
			hdr := make(http.Header)
			if ct != "" {
				hdr.Set("Content-Type", ct)
			}
			if loc != "" {
				hdr.Set("Location", loc)
			}
			return &http.Response{StatusCode: status, Header: hdr, Body: ioNopCloser(bytes.NewReader(body)), Request: r}
		}
		switch {
		case host == "f.video.weibocdn.com" && r.URL.Path == "/rt_redirect.mp4":
			return mkResp(http.StatusFound, "", "https://edge1.weibocdn.com/rt_final.mp4", nil), nil
		case host == "edge1.weibocdn.com" && r.URL.Path == "/rt_final.mp4":
			return mkResp(http.StatusOK, "video/mp4", "", []byte(videoMP4Stub)), nil
		case host == "f.video.weibocdn.com" && r.URL.Path == "/rt_evil.mp4":
			return mkResp(http.StatusFound, "", "https://evil.example.com/bad.mp4", nil), nil
		case host == "evil.example.com":
			evilHits.Add(1)
			return mkResp(http.StatusOK, "video/mp4", "", []byte("evil")), nil
		default:
			return mkResp(http.StatusNotFound, "", "", nil), nil
		}
	})

	h := newWeiboThreeSiteHarness(t, func(o *Options) {
		o.WeiboOpts.Transport = rt
	})
	ctx := context.Background()
	if _, err := h.backend.LoginWithCookie(ctx, "weibo", synthWeiboCookie(), ""); err != nil {
		t.Fatalf("LoginWithCookie: %v", err)
	}
	if _, err := h.backend.CreateSubscription("合成转发订阅", "", []subscription.AccountRef{
		{Site: "weibo", UID: subUID, Name: "订阅的转发博主"},
	}, subscription.SubRules{}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// OpenLink on /<subUID>/<mid> preserves ForwarderHintUID & resolves subscribed name even when statuses/show author differs.
	res, err := h.backend.OpenLink(ctx, "https://weibo.com/"+subUID+"/"+synthWeiboMidPlain)
	if err != nil {
		t.Fatalf("OpenLink: %v", err)
	}
	if res.WeiboPost == nil || res.WeiboPost.ForwarderHintUID != subUID || res.WeiboPost.ForwarderHintName != "订阅的转发博主" {
		t.Fatalf("ForwarderHint = (%q, %q), want (%q, %q)", res.WeiboPost.ForwarderHintUID, res.WeiboPost.ForwarderHintName, subUID, "订阅的转发博主")
	}

	// StagePreviewMedia follows allowlisted weibocdn.com redirect for uncached retweet video preview.
	vidPath, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://f.video.weibocdn.com/rt_redirect.mp4", ".mp4")
	if err != nil {
		t.Fatalf("StagePreviewMedia(weibocdn redirect): %v", err)
	}
	gotBytes, err := os.ReadFile(vidPath)
	if err != nil || string(gotBytes) != videoMP4Stub {
		t.Fatalf("staged video preview bytes = %q err=%v", string(gotBytes), err)
	}
	// Non-allowlist redirect target MUST still be blocked.
	if _, err := h.backend.StagePreviewMedia(ctx, "weibo", "https://f.video.weibocdn.com/rt_evil.mp4", ".mp4"); err == nil || site.KindOf(err) != site.ErrKindBlocked {
		t.Fatalf("evil video redirect err = %v, want ErrKindBlocked", err)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("blocked evil host received %d hits", evilHits.Load())
	}
}

func ioNopCloser(r *bytes.Reader) readCloser { return readCloser{r} }

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }
