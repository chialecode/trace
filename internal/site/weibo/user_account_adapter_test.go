package weibo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestUserPostsPaginationDedupPinnedFastRetweetAdFilterAndCursorStall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("page")
		since := r.URL.Query().Get("since_id")
		switch {
		case page == "1" && since == "":
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"since_id": "cursor_page_2",
					"total": 42,
					"list": [
						{"idstr":"5000000000000001","mblogid":"O00000001","isTop":1,"text_raw":"置顶微博","created_at":"Sat Nov 21 10:00:00 +0800 2026","user":{"idstr":"1000000001","screen_name":"测试博主"}},
						{"idstr":"5000000000000002","mblogid":"O00000002","mblogtype":1,"text_raw":"推广广告","user":{"idstr":"1000000001","screen_name":"测试博主"}},
						{"idstr":"5000000000000003","mblogid":"O00000003","is_paid":true,"text_raw":"付费推广","user":{"idstr":"1000000001","screen_name":"测试博主"}},
						{"idstr":"5000000000000004","mblogid":"O00000004","text_raw":"外人动态卡","user":{"idstr":"1000009999","screen_name":"陌生人"}},
						{"idstr":"5000000000000005","mblogid":"O00000005","ori_mid":"4900000000000001","text_raw":"快转的旧微博","created_at":"Mon Jan 01 10:00:00 +0800 2024","user":{"idstr":"1000008888","screen_name":"原博主"}},
						{"idstr":"5000000000000006","mblogid":"O00000006","text_raw":"常规原创微博","created_at":"Sat Nov 21 14:00:00 +0800 2026","user":{"idstr":"1000000001","screen_name":"测试博主"}}
					]
				}
			}`))
		case page == "2" && since == "cursor_page_2":
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"since_id": "cursor_page_3",
					"total": 42,
					"list": [
						{"idstr":"5000000000000006","mblogid":"O00000006","text_raw":"跨页重复微博","user":{"idstr":"1000000001","screen_name":"测试博主"}},
						{"idstr":"5000000000000007","mblogid":"O00000007","text_raw":"第二页新微博","created_at":"Sat Nov 21 12:00:00 +0800 2026","user":{"idstr":"1000000001","screen_name":"测试博主"}}
					]
				}
			}`))
		case page == "3" && since == "cursor_page_3":
			// Cursor stalled: returns same since_id!
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"since_id": "cursor_page_3",
					"list": [
						{"idstr":"5000000000000008","mblogid":"O00000008","text_raw":"停滞页","user":{"idstr":"1000000001","screen_name":"测试博主"}}
					]
				}
			}`))
		}
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_USER"))
	seen := make(map[string]bool)

	p1, err := client.GetUserPosts(context.Background(), UserPostsRequest{
		UID:     "1000000001",
		Page:    1,
		SeenIDs: seen,
	})
	if err != nil {
		t.Fatalf("page 1 failed: %v", err)
	}
	// Expect 3 kept items: pinned (0001), fast retweet (0005), regular (0006); ads/paid/foreign filtered!
	if len(p1.Posts) != 3 {
		t.Fatalf("page 1 posts = %d, want 3: %+v", len(p1.Posts), p1.Posts)
	}
	if !p1.Posts[0].IsTop || p1.Posts[0].Mid != "5000000000000001" {
		t.Fatalf("expected pinned first, got %+v", p1.Posts[0])
	}
	if !p1.Posts[1].IsFastRetweet || p1.Posts[1].OriMid != "4900000000000001" || p1.Posts[1].RuleType != RuleTypeForward {
		t.Fatalf("expected fast retweet preserved, got %+v", p1.Posts[1])
	}
	if !p1.HasMore || p1.NextSinceID != "cursor_page_2" || p1.NextPage != 2 {
		t.Fatalf("page 1 pagination = (hasMore=%v, nextPage=%d, nextSince=%q)", p1.HasMore, p1.NextPage, p1.NextSinceID)
	}

	p2, err := client.GetUserPosts(context.Background(), UserPostsRequest{
		UID:     "1000000001",
		Page:    p1.NextPage,
		SinceID: p1.NextSinceID,
		SeenIDs: seen,
	})
	if err != nil {
		t.Fatalf("page 2 failed: %v", err)
	}
	if len(p2.Posts) != 1 || p2.Posts[0].Mid != "5000000000000007" {
		t.Fatalf("page 2 cross-page dedup failed: %+v", p2.Posts)
	}

	// Page 3 cursor stall -> schema_changed!
	if _, err := client.GetUserPosts(context.Background(), UserPostsRequest{
		UID:     "1000000001",
		Page:    p2.NextPage,
		SinceID: p2.NextSinceID,
		SeenIDs: seen,
	}); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("cursor stall err = %v, want schema_changed", err)
	}
}

func TestUserPostsEmptyTruthTableEveryRow(t *testing.T) {
	t.Run("missing_data_or_null_list_is_risk_control", func(t *testing.T) {
		for _, body := range []string{`{"ok":1}`, `{"ok":1,"data":null}`, `{"ok":1,"data":{}}`, `{"ok":1,"data":{"list":null}}`} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_TT"))
			_, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 1})
			srv.Close()
			if !site.IsKind(err, site.ErrKindRiskControl) {
				t.Fatalf("body %s: err = %v, want risk_control", body, err)
			}
		}
	})

	t.Run("bottom_tips_visible_normal_end", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":"","bottom_tips_visible":true,"bottom_tips_text":"博主设置仅展示半年内的微博"}}`))
		}))
		defer srv.Close()

		client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_TT"))
		page, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 1})
		if err != nil {
			t.Fatalf("bottom_tips err: %v", err)
		}
		if page.HasMore || page.UncertainEmpty || !page.BottomTipsVisible || page.BottomTipsText != "博主设置仅展示半年内的微博" {
			t.Fatalf("unexpected bottom_tips page: %+v", page)
		}
	})

	t.Run("page_gt_1_empty_no_tips_genuine_end", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":""}}`))
		}))
		defer srv.Close()

		client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_TT"))
		page, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 2, SinceID: "cursor_2"})
		if err != nil || page.HasMore || page.UncertainEmpty {
			t.Fatalf("page>1 empty = (%+v, %v), want clean end", page, err)
		}
	})

	t.Run("page_1_empty_crosscheck_blockText_zero_and_nonzero_statuses", func(t *testing.T) {
		var profileMode atomic.Int32 // 0=blockText, 1=statuses_count==0, 2=statuses_count>0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/ajax/statuses/mymblog" {
				_, _ = w.Write([]byte(`{"ok":1,"data":{"list":[],"since_id":""}}`))
				return
			}
			switch profileMode.Load() {
			case 0:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"封禁账号","statuses_count":5},"blockText":"该账号因违反社区公约已被封禁"}}`))
			case 1:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"零博账号","statuses_count":0},"blockText":""}}`))
			case 2:
				_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"限流账号","statuses_count":120},"blockText":""}}`))
			}
		}))
		defer srv.Close()

		client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_TT"))

		// blockText != "" -> unavailable (or forbidden if blacklist/permission text).
		profileMode.Store(0)
		if _, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 1, CrossCheckProfile: true}); !site.IsKind(err, site.ErrKindUnavailable) {
			t.Fatalf("blockText err = %v, want unavailable", err)
		}

		// statuses_count == 0 -> genuine 0 posts (UncertainEmpty == false).
		profileMode.Store(1)
		pZero, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 1, CrossCheckProfile: true})
		if err != nil || pZero.UncertainEmpty || pZero.HasMore {
			t.Fatalf("statuses_count==0 = (%+v, %v), want genuine empty", pZero, err)
		}

		// statuses_count > 0 -> UncertainEmpty == true!
		profileMode.Store(2)
		pUncertain, err := client.GetUserPosts(context.Background(), UserPostsRequest{UID: "1000000001", Page: 1, CrossCheckProfile: true})
		if err != nil || !pUncertain.UncertainEmpty || pUncertain.HasMore {
			t.Fatalf("statuses_count>0 = (%+v, %v), want UncertainEmpty=true", pUncertain, err)
		}
	})
}

func TestAccountFollowingFavoritesAndFeatureFlaggedLikesGroups(t *testing.T) {
	if LikesEnabled {
		t.Fatalf("LikesEnabled must default to false until ACT-022 V-14 verifies it")
	}
	if FollowGroupsEnabled {
		t.Fatalf("FollowGroupsEnabled must default to false until ACT-022 V-13 verifies it")
	}

	var likesStatus atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/friendships/friends":
			page := r.URL.Query().Get("page")
			if page == "1" {
				_, _ = w.Write([]byte(`{
					"ok": 1,
					"next_cursor": 20,
					"total_number": 3,
					"users": [
						{"idstr":"1000000010","screen_name":"关注博主一","avatar_large":"https://tvax1.sinaimg.cn/u10.jpg","special_follow":true},
						{"idstr":"1000000011","screen_name":"关注博主二","avatar_large":"https://tvax1.sinaimg.cn/u11.jpg"}
					]
				}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"next_cursor": 0,
				"total_number": 3,
				"users": [
					{"idstr":"1000000011","screen_name":"关注博主二重复"},
					{"idstr":"1000000012","screen_name":"关注博主三"}
				]
			}`))

		case "/ajax/profile/followContent":
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"total_number": 1,
					"follows": {
						"next_cursor": 0,
						"total_number": 1,
						"users": [{"idstr":"1000000010","screen_name":"关注博主一"}]
					}
				}
			}`))

		case "/ajax/favorites/all_fav":
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write([]byte(`{
					"ok": 1,
					"data": {
						"total_number": 2,
						"status": [
							{"idstr":"5000000000000001","mblogid":"O00000001","text_raw":"收藏微博一","user":{"idstr":"1000000010","screen_name":"关注博主一"}},
							{"status":{"idstr":"5000000000000002","mblogid":"O00000002","text_raw":"收藏微博二","user":{"idstr":"1000000011","screen_name":"关注博主二"}}}
						]
					}
				}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":1,"data":{"total_number":2,"status":[]}}`))

		case "/ajax/feed/allGroups":
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"groups": [
					{"title":"我的分组","group":[{"gid":"4000000000000001","title":"特别关注","type":"special"},{"list_id":"4000000000000002","title":"摄影","type":"custom"}]}
				]
			}`))

		case "/ajax/statuses/likelist":
			if r.URL.Query().Get("uid") == "" || r.URL.Query().Get("id") != "" {
				t.Errorf("likelist must query uid=<self> and NEVER id=<mid>, got %s", r.URL.RawQuery)
			}
			st := likesStatus.Load()
			if st == 404 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if st == 200 {
				w.WriteHeader(http.StatusOK) // 0-byte body!
				return
			}
			_, _ = w.Write([]byte(`{"ok":1,"data":{"total":1,"list":[{"idstr":"5000000000000001","mblogid":"O00000001","text_raw":"赞过的微博","user":{"idstr":"1000000010","screen_name":"博主"}}]}}`))
		}
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_ACCT"))
	var breakerCalled atomic.Int32
	client.SetAutomaticRequestHandlers(nil, func(_ string, _ error) {
		breakerCalled.Add(1)
	})

	// Following pagination + cross-page dedup.
	seenUsers := make(map[string]bool)
	f1, err := client.ListFollowing(context.Background(), "1000000001", 1, seenUsers)
	if err != nil || len(f1.Users) != 2 || !f1.HasMore || !f1.Users[0].SpecialFollow {
		t.Fatalf("ListFollowing page 1 = (%+v, %v)", f1, err)
	}
	f2, err := client.ListFollowing(context.Background(), "1000000001", f1.NextPage, seenUsers)
	if err != nil || len(f2.Users) != 1 || f2.Users[0].UID != "1000000012" || f2.HasMore {
		t.Fatalf("ListFollowing page 2 = (%+v, %v)", f2, err)
	}

	// Following without next_cursor key and seen == nil must compute hasMore from len(users) < total.
	var noCursorTotal atomic.Int32
	srvNoCursor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if noCursorTotal.Load() == 5 {
			_, _ = w.Write([]byte(`{"ok":1,"total_number":5,"users":[{"idstr":"1000000010","screen_name":"甲"},{"idstr":"1000000011","screen_name":"乙"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":1,"total_number":2,"users":[{"idstr":"1000000010","screen_name":"甲"},{"idstr":"1000000011","screen_name":"乙"}]}`))
	}))
	defer srvNoCursor.Close()
	ncClient := newFastTestClient(t, srvNoCursor.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_NC"))
	noCursorTotal.Store(5)
	ncMore, err := ncClient.ListFollowing(context.Background(), "1000000001", 1, nil)
	if err != nil || !ncMore.HasMore {
		t.Fatalf("ListFollowing(no next_cursor, seen=nil, 2<5) = (%+v, %v), want HasMore=true", ncMore, err)
	}
	noCursorTotal.Store(2)
	ncEnd, err := ncClient.ListFollowing(context.Background(), "1000000001", 1, nil)
	if err != nil || ncEnd.HasMore {
		t.Fatalf("ListFollowing(no next_cursor, seen=nil, 2==2) = (%+v, %v), want HasMore=false", ncEnd, err)
	}

	// Optional followContent parser.
	fc, err := client.ListFollowingContent(context.Background(), 1, "", nil)
	if err != nil || len(fc.Users) != 1 || fc.HasMore {
		t.Fatalf("ListFollowingContent = (%+v, %v)", fc, err)
	}

	// Favorites strictly reads data.status (both direct & wrapped entries) and ends cleanly on [].
	seenFav := make(map[string]bool)
	fav1, err := client.ListFavorites(context.Background(), "1000000001", 1, seenFav)
	if err != nil || len(fav1.Posts) != 2 || fav1.Total != 2 {
		t.Fatalf("ListFavorites page 1 = (%+v, %v)", fav1, err)
	}
	fav2, err := client.ListFavorites(context.Background(), "1000000001", 2, seenFav)
	if err != nil || len(fav2.Posts) != 0 || fav2.HasMore {
		t.Fatalf("ListFavorites page 2 = (%+v, %v)", fav2, err)
	}

	// Follow groups + group members.
	groups, err := client.ListFollowGroups(context.Background())
	if err != nil || len(groups) != 2 || groups[0].ID != "4000000000000001" {
		t.Fatalf("ListFollowGroups = (%+v, %v)", groups, err)
	}
	gm, err := client.ListFollowGroupMembers(context.Background(), "1000000001", "4000000000000002", 1, nil)
	if err != nil || len(gm.Users) != 2 {
		t.Fatalf("ListFollowGroupMembers = (%+v, %v)", gm, err)
	}

	// Likes: 404 and 0-byte degrade to unavailable WITHOUT tripping circuit breaker!
	likesStatus.Store(404)
	if _, err := client.ListLikes(site.WithAutomaticRequests(context.Background(), "probe"), "1000000001", 1, nil); !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("ListLikes(404) err = %v, want unavailable", err)
	}
	likesStatus.Store(200)
	if _, err := client.ListLikes(site.WithAutomaticRequests(context.Background(), "probe"), "1000000001", 1, nil); !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("ListLikes(0-byte) err = %v, want unavailable", err)
	}
	if breakerCalled.Load() != 0 {
		t.Fatalf("ListLikes degradation tripped circuit breaker %d times, want 0", breakerCalled.Load())
	}
	likesStatus.Store(1)
	lp, err := client.ListLikes(context.Background(), "1000000001", 1, nil)
	if err != nil || len(lp.Posts) != 1 {
		t.Fatalf("ListLikes(ok) = (%+v, %v)", lp, err)
	}
}

func TestAdapterDownloadPlansEntryKeysAndSubscriptionSnapshotSanitization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/statuses/show":
			id := r.URL.Query().Get("id")
			if id == "O00000001" {
				_, _ = w.Write([]byte(`{
					"ok": 1,
					"idstr": "5000000000000001",
					"mblogid": "O00000001",
					"created_at": "Sat Nov 21 15:00:00 +0800 2026",
					"text_raw": "单视频微博",
					"user": {"idstr": "1000000001", "screen_name": "测试博主"},
					"page_info": {
						"object_type": "video",
						"object_id": "1034:5000000000000001",
						"page_pic": "https://wx1.sinaimg.cn/large/cover1.jpg",
						"media_info": {
							"duration": 20,
							"playback_list": [
								{"meta": {"label": "mp4_1080p", "quality_index": 1080, "quality_label": "1080p"}, "play_info": {"url": "https://f.video.weibocdn.com/v1.mp4?Expires=1800000000&ssig=syn_sig_secret&KID=unistore,video", "mime": "video/mp4", "width": 1920, "height": 1080, "fps": 30, "bitrate": 3000000, "size": 7500000, "video_codecs": "avc1.640028"}}
							]
						}
					}
				}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"idstr": "5000000000000002",
				"mblogid": "O00000002",
				"created_at": "Sat Nov 21 15:05:00 +0800 2026",
				"text_raw": "实况图文微博",
				"user": {"idstr": "1000000001", "screen_name": "测试博主"},
				"pic_ids": ["p1"],
				"pic_infos": {
					"p1": {
						"pic_id": "p1",
						"type": "livephoto",
						"largest": {"url": "https://wx1.sinaimg.cn/large/p1.jpg", "width": 1080, "height": 1440},
						"video": "https://video.weibo.com/media/play?livephoto=syn_lp_1"
					}
				}
			}`))

		case "/tv/api/component":
			_, _ = w.Write([]byte(`{
				"code": "100000",
				"data": {
					"Component_Play_Playinfo": {
						"mid": "",
						"title": "无宿主独立视频",
						"author": "测试博主",
						"author_id": "1000000001",
						"cover_image": "https://wx1.sinaimg.cn/large/tv_cover.jpg",
						"duration": 15,
						"urls": {"高清 1080P": "//f.video.weibocdn.com/tv_1080.mp4?Expires=1800000000&ssig=syn_sig_secret&KID=unistore,video"}
					}
				}
			}`))

		case "/ajax/statuses/mymblog":
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"since_id": "next_cur_2",
					"total": 5,
					"list": [
						{
							"idstr": "5000000000000001",
							"mblogid": "O00000001",
							"created_at": "Sat Nov 21 15:00:00 +0800 2026",
							"text_raw": "订阅视频微博 http://t.cn/A6SIGNED",
							"reposts_count": 88,
							"comments_count": 99,
							"attitudes_count": 1234,
							"url_struct": [
								{
									"short_url": "http://t.cn/A6SIGNED",
									"long_url": "https://f.video.weibocdn.com/u.mp4?Expires=1800000000&ssig=syn_urlstruct_secret&KID=unistore,video",
									"url_title": "合成视频"
								}
							],
							"user": {"idstr": "1000000001", "screen_name": "测试博主", "avatar_hd": "https://tvax1.sinaimg.cn/av.jpg?Expires=1800000000&ssig=syn_avatar_secret&KID=unistore,img"},
							"retweeted_status": {
								"idstr": "5000000000000000",
								"mblogid": "O00000000",
								"text_raw": "原博含签名链接 https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2Fa.mov%3FExpires%3D1800000000%26ssig%3Dsyn_rt_enc%26KID%3Dunistore",
								"reposts_count": 55,
								"comments_count": 66,
								"attitudes_count": 77,
								"user": {"idstr": "1000000002", "screen_name": "原作者", "avatar_hd": "https://tvax1.sinaimg.cn/rt_av.jpg?ssig=syn_rt_av_secret"},
								"page_info": {
									"object_type": "article",
									"page_title": "合成文章",
									"page_url": "https://card.weibo.com/article/1?Expires=1800000000&ssig=syn_card_secret&KID=unistore",
									"page_pic": "http://wx1.sinaimg.cn/large/http_only_art.jpg"
								}
							},
							"page_info": {
								"object_type": "video",
								"object_id": "1034:5000000000000001",
								"page_pic": "https://wx1.sinaimg.cn/large/cover1.jpg",
								"media_info": {
									"duration": 20,
									"playback_list": [
										{"meta": {"label": "mp4_1080p", "quality_index": 1080}, "play_info": {"url": "https://f.video.weibocdn.com/v1.mp4?Expires=1800000000&ssig=syn_sig_secret&KID=unistore,video", "mime": "video/mp4", "width": 1920, "height": 1080}}
									]
								}
							}
						}
					]
				}
			}`))
		}
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_ADAPTER"))
	adapter := NewAdapter(client)

	// 1. Registry dispatches Weibo URLs cleanly through Adapter.
	reg := site.NewRegistry(adapter)
	resolved, err := reg.Resolve(context.Background(), "https://weibo.com/1000000001/O00000001")
	if err != nil || resolved.Site != site.Weibo || resolved.ID != "5000000000000001" {
		t.Fatalf("Registry.Resolve = (%+v, %v)", resolved, err)
	}

	// 2. Video post uses entry key "weibo:dynamic:<mid>" (NOT weibo:video) per weibo.md §13.1 #3!
	planVid, err := adapter.PlanDownload(context.Background(), "5000000000000001", 1)
	if err != nil {
		t.Fatalf("PlanDownload(video post) failed: %v", err)
	}
	if planVid.ItemKey != "weibo:dynamic:5000000000000001" || planVid.Kind != "single_mp4" || len(planVid.VideoURLs) != 1 {
		t.Fatalf("unexpected video post plan: %+v", planVid)
	}

	// 3. LivePhoto post with video.weibo.com/media/play uses LivePhotoMediaTransport!
	planLP, err := adapter.PlanDownload(context.Background(), "5000000000000002", 1)
	if err != nil {
		t.Fatalf("PlanDownload(livephoto) failed: %v", err)
	}
	if planLP.ItemKey != "weibo:dynamic:5000000000000002" || planLP.Kind != "image_note" || len(planLP.Images) != 1 || planLP.Images[0].MediaRole != MediaKindLivePhoto {
		t.Fatalf("unexpected livephoto plan: %+v", planLP)
	}
	if err := planLP.Media.ValidateURL("https://video.weibo.com/media/play?livephoto=syn_lp_1"); err != nil {
		t.Fatalf("LivePhotoMediaTransport should allow video.weibo.com/media/play initial hop, got %v", err)
	}

	// 4. Standalone 1034: video with no host mid uses entry key "weibo:video:<oid with ':' replaced by '_'>" (§13.1 #3).
	planTV, err := adapter.PlanDownload(context.Background(), "1034:5000000000000099", 1)
	if err != nil {
		t.Fatalf("PlanDownload(standalone video) failed: %v", err)
	}
	if planTV.ItemKey != "weibo:video:1034_5000000000000099" || planTV.Kind != "single_mp4" {
		t.Fatalf("unexpected standalone video plan: %+v", planTV)
	}

	// 5. FetchCreatorPosts snapshot must NEVER contain DEC-040 online-only Stats or signed URL markers!
	feedPage, err := adapter.FetchCreatorPosts(context.Background(), "1000000001", "")
	if err != nil || len(feedPage.Posts) != 1 || feedPage.NextCursor != "2:next_cur_2" {
		t.Fatalf("FetchCreatorPosts = (%+v, %v)", feedPage, err)
	}
	snap := feedPage.Posts[0].Snapshot
	for _, forbidden := range []string{
		"ssig=", "Expires=", "KID=", "ssig%3D", "Expires%3D", "KID%3D",
		"syn_sig_secret", "syn_urlstruct_secret", "syn_avatar_secret", "syn_rt_enc", "syn_rt_av_secret", "syn_card_secret",
		`"stats"`, `"repostsCount"`, `"commentsCount"`, `"attitudesCount"`,
	} {
		if strings.Contains(snap, forbidden) {
			t.Fatalf("Subscription Snapshot leaked forbidden token %q: %s", forbidden, snap)
		}
	}
	if !strings.Contains(snap, "wx1.sinaimg.cn/large/cover1.jpg") {
		t.Fatalf("expected public sinaimg.cn cover preserved in Snapshot: %s", snap)
	}
	// Verify SafePublicImageURL enforces HTTPS for non-loopback hosts.
	if got := SafePublicImageURL("http://wx1.sinaimg.cn/large/insecure.jpg"); got != "" {
		t.Fatalf("SafePublicImageURL accepted non-HTTPS sinaimg URL: %q", got)
	}
	if got := SafePublicImageURL("https://wx1.sinaimg.cn/large/public.jpg"); got == "" {
		t.Fatalf("SafePublicImageURL rejected valid HTTPS public sinaimg URL")
	}

	// 6. PlanDownloadFromPost builds a DownloadPlan purely in memory with 0 network calls.
	var inMemHits atomic.Int32
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		inMemHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer deadSrv.Close()
	offlineAdapter := NewAdapter(newFastTestClient(t, deadSrv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_OFFLINE")))
	parsedPost, err := offlineAdapter.Client().ParseRawPostJSON([]byte(`{
		"ok": 1,
		"idstr": "5000000000000055",
		"mblogid": "O0000000T",
		"text_raw": "纯内存列表复用计划",
		"user": {"idstr": "1000000001", "screen_name": "测试博主"},
		"pic_ids": ["p1"],
		"pic_infos": {"p1": {"pic_id": "p1", "type": "pic", "largest": {"url": "https://wx1.sinaimg.cn/large/p1.jpg", "width": 800, "height": 600}}}
	}`))
	if err != nil {
		t.Fatalf("ParseRawPostJSON failed: %v", err)
	}
	inMemPlan, err := offlineAdapter.PlanDownloadFromPost(parsedPost)
	if err != nil || inMemPlan.ItemKey != "weibo:dynamic:5000000000000055" || len(inMemPlan.Images) != 1 || inMemHits.Load() != 0 {
		t.Fatalf("PlanDownloadFromPost = (%+v, err=%v, hits=%d)", inMemPlan, err, inMemHits.Load())
	}
}

func TestUserProfileFieldsMismatchAndBlockTextClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("uid") {
		case "1000000001":
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"blockText": "",
					"user": {
						"id": 1000000001,
						"idstr": "1000000001",
						"screen_name": "合成博主",
						"avatar_hd": "https://tvax1.sinaimg.cn/hd.jpg",
						"description": "简介文本",
						"statuses_count": 321,
						"followers_count": 9999,
						"friends_count": 120,
						"verified": true,
						"verified_type": 0,
						"verified_reason": "合成认证说明",
						"domain": "synthetic_dom",
						"cover_image_phone": "https://wx1.sinaimg.cn/cover.jpg"
					}
				}
			}`))
		case "1000000002":
			// Mismatched UID -> schema_changed!
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000099","screen_name":"错人"}}}`))
		case "1000000003":
			// Blacklist blockText without user -> forbidden!
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":null,"blockText":"由于对方设置，你已被加入黑名单，无权查看"}}`))
		case "1000000004":
			// Suspended account blockText without user -> unavailable!
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":null,"blockText":"该账号因违规已被封禁"}}`))
		case "1000000005":
			// Nonexistent user returning ok:1 with empty data:{} -> unavailable (not schema_changed)!
			_, _ = w.Write([]byte(`{"ok":1,"data":{}}`))
		case "1000000006":
			// Nonexistent user returning ok:1 with data:{user:null} -> unavailable!
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":null}}`))
		case "1000000007":
			// Nonexistent user returning ok:1 with empty user shell data:{user:{}} -> unavailable!
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{}}}`))
		case "1000000008":
			// Nonexistent user returning ok:1 with PHP empty array data:[] -> unavailable!
			_, _ = w.Write([]byte(`{"ok":1,"data":[]}`))
		case "1000000009":
			// Populated user with invalid non-numeric idstr -> schema_changed!
			_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"not-a-uid","screen_name":"损坏资料"}}}`))
		case "1000000010":
			// Truly malformed data type -> schema_changed!
			_, _ = w.Write([]byte(`{"ok":1,"data":"unexpected_string"}`))
		default:
			if r.URL.Query().Get("screen_name") == "不存在的昵称" {
				_, _ = w.Write([]byte(`{"ok":1,"data":{}}`))
				return
			}
			if r.URL.Query().Get("custom") == "missing_custom_dom" {
				_, _ = w.Write([]byte(`{"ok":1,"data":{"user":null}}`))
				return
			}
		}
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_PROF"))
	prof, err := client.GetUserProfile(context.Background(), "1000000001")
	if err != nil {
		t.Fatalf("GetUserProfile failed: %v", err)
	}
	if prof.UID != "1000000001" || prof.ScreenName != "合成博主" || prof.StatusesCount != 321 || prof.FollowersCount != 9999 || !prof.Verified || prof.Domain != "synthetic_dom" {
		t.Fatalf("unexpected profile: %+v", prof)
	}

	if _, err := client.GetUserProfile(context.Background(), "1000000002"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("UID mismatch err = %v, want schema_changed", err)
	}
	if _, err := client.GetUserProfile(context.Background(), "1000000003"); !site.IsKind(err, site.ErrKindForbidden) {
		t.Fatalf("blacklist blockText err = %v, want forbidden", err)
	}
	if _, err := client.GetUserProfile(context.Background(), "1000000004"); !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("suspended blockText err = %v, want unavailable", err)
	}
	for _, tc := range []struct {
		name string
		uid  string
	}{
		{"ok1_empty_data_obj", "1000000005"},
		{"ok1_user_null", "1000000006"},
		{"ok1_empty_user_obj", "1000000007"},
		{"ok1_empty_data_array", "1000000008"},
	} {
		if _, err := client.GetUserProfile(context.Background(), tc.uid); !site.IsKind(err, site.ErrKindUnavailable) {
			t.Fatalf("%s: err = %v, want unavailable", tc.name, err)
		}
	}
	if _, err := client.GetUserProfileByScreenName(context.Background(), "不存在的昵称"); !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("GetUserProfileByScreenName(nonexistent) err = %v, want unavailable", err)
	}
	if _, err := client.GetUserProfileByCustom(context.Background(), "missing_custom_dom"); !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("GetUserProfileByCustom(nonexistent) err = %v, want unavailable", err)
	}
	if _, err := client.GetUserProfile(context.Background(), "1000000009"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("invalid idstr with screen_name err = %v, want schema_changed", err)
	}
	if _, err := client.GetUserProfile(context.Background(), "1000000010"); !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("malformed data string err = %v, want schema_changed", err)
	}
}
