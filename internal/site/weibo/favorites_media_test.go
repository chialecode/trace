package weibo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListFavoritesMediaFallbacksAndRetweetVariants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/ajax/favorites/all_fav" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"ok": 1,
			"data": {
				"total_number": 7,
				"status": [
					{
						"id": 990001,
						"favorited_time": "Sat Nov 21 20:00:00 +0800 2026",
						"status": {
							"idstr": "5000000000001001",
							"mblogid": "O00001001",
							"text_raw": "包裹收藏一：完整 pic_infos 含原图与动图",
							"user": {"idstr": "1000000011", "screen_name": "收藏博主甲"},
							"pic_ids": ["synfav01", "synfav02"],
							"pic_infos": {
								"synfav01": {
									"pic_id": "synfav01",
									"type": "pic",
									"bmiddle": {"url": "https://wx1.sinaimg.cn/bmiddle/synfav01.jpg", "width": 440, "height": 330},
									"largest": {"url": "https://wx1.sinaimg.cn/mw2000/synfav01.jpg", "width": 2000, "height": 1500}
								},
								"synfav02": {
									"pic_id": "synfav02",
									"type": "gif",
									"largest": {"url": "https://wx1.sinaimg.cn/large/synfav02.gif", "width": 600, "height": 400}
								}
							}
						}
					},
					{
						"idstr": "5000000000001002",
						"mblogid": "O00001002",
						"text_raw": "简略收藏二：仅有 pic_ids 与 pic_urls，无 pic_infos",
						"user": {"idstr": "1000000012", "screen_name": "收藏博主乙"},
						"pic_ids": ["synbare01", "synbare02", "synbare03"],
						"bmiddle_pic": "http://wx3.sinaimg.cn/bmiddle/synbare01.jpg",
						"pic_urls": [
							{"thumbnail_pic": "http://wx3.sinaimg.cn/thumbnail/synbare01.jpg"},
							{"thumbnail_pic": "http://wx3.sinaimg.cn/orj360/synbare02.gif"}
						]
					},
					{
						"idstr": "5000000000001003",
						"mblogid": "O00001003",
						"text_raw": "移动端结构收藏三：pics 数字键字典 + 实况图",
						"user": {"idstr": "1000000013", "screen_name": "收藏博主丙"},
						"live_photo": ["", "https://video.weibo.com/media/play?livephoto=synlp02"],
						"pics": {
							"1": {
								"pid": "synmob02",
								"url": "https://wx2.sinaimg.cn/orj360/synmob02.jpg",
								"type": "livephotos",
								"geo": {"width": "1080", "height": "1440"}
							},
							"0": {
								"pid": "synmob01",
								"url": "http://wx2.sinaimg.cn/bmiddle/synmob01.jpg",
								"geo": false,
								"large": {
									"url": "http://wx2.sinaimg.cn/large/synmob01.jpg",
									"geo": {"width": 1600, "height": 1200}
								}
							}
						}
					},
					{
						"idstr": "5000000000001004",
						"mblogid": "O00001004",
						"text_raw": "混排收藏四：图文与简略混排视频封面",
						"user": {"idstr": "1000000014", "screen_name": "收藏博主丁"},
						"mix_media_info": {
							"items": [
								{
									"id": "synmix01",
									"type": "pic",
									"data": {"pic_id": "synmix01", "type": "pic", "largest": {"url": "https://wx4.sinaimg.cn/large/synmix01.jpg", "width": 1200, "height": 900}}
								},
								{
									"id": "1034:5000000000001004",
									"type": "video",
									"data": {
										"object_id": "1034:5000000000001004",
										"page_pic": "http://wx4.sinaimg.cn/orj360/synmixvid.jpg",
										"media_info": {"duration": 19}
									}
								}
							]
						}
					},
					{
						"idstr": "5000000000001005",
						"mblogid": "O00001005",
						"text_raw": "视频收藏五：page_info.urls 字典选流",
						"user": {"idstr": "1000000015", "screen_name": "收藏博主戊"},
						"page_info": {
							"object_type": "video",
							"object_id": "1034:5000000000001005",
							"page_title": "收藏视频标题五",
							"pic_info": {"pic_big": {"url": "http://wx1.sinaimg.cn/orj480/synvid05.jpg"}},
							"urls": {
								"mp4_1080p_mp4": "https://f.video.weibocdn.com/synvid05_1080.mp4",
								"mp4_hd_mp4": "https://f.video.weibocdn.com/synvid05_720.mp4"
							},
							"media_info": {"duration": 36}
						}
					},
					{
						"idstr": "5000000000001006",
						"mblogid": "O00001006",
						"text_raw": "转发配图收藏六：外层附图 + 原博仅有裸 pic_ids",
						"user": {"idstr": "1000000016", "screen_name": "转发收藏者"},
						"pic_ids": ["synouter01"],
						"retweeted_status": {
							"idstr": "5000000000002006",
							"mblogid": "O00002006",
							"text_raw": "被转发原博：仅返回裸 pic_ids 无 pic_infos",
							"user": {"idstr": "1000000026", "screen_name": "原图作者"},
							"pic_num": 2,
							"pic_ids": ["synrtpic01", "synrtpic02"]
						}
					},
					{
						"idstr": "5000000000001007",
						"mblogid": "O00001007",
						"text_raw": "转发视频收藏七：外层上提简略视频卡片",
						"user": {"idstr": "1000000017", "screen_name": "转发视频者"},
						"page_info": {
							"object_type": "video",
							"object_id": "1034:5000000000002007",
							"page_title": "被转发原博简略视频",
							"page_pic": "http://wx2.sinaimg.cn/bmiddle/synrtvid07.jpg",
							"media_info": {"duration": 64}
						},
						"retweeted_status": {
							"idstr": "5000000000002007",
							"mblogid": "O00002007",
							"text_raw": "被转发视频原博正文",
							"user": {"idstr": "1000000027", "screen_name": "原视频作者"}
						}
					}
				]
			}
		}`))
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_FAVMEDIA"))
	page, err := client.ListFavorites(context.Background(), "1000000001", 1, nil)
	if err != nil {
		t.Fatalf("ListFavorites failed: %v", err)
	}
	if len(page.Posts) != 7 {
		t.Fatalf("len(Posts) = %d, want 7", len(page.Posts))
	}

	// 1. Wrapped {"id": 990001, "status": {...}} unwraps inner status mid (never 990001) + pic_infos.
	p1 := page.Posts[0]
	if p1.Mid != "5000000000001001" || p1.MediaIncomplete || len(p1.Images) != 2 {
		t.Fatalf("p1 unwrap/images = mid=%q incomplete=%v images=%+v", p1.Mid, p1.MediaIncomplete, p1.Images)
	}
	if p1.Images[0].URL != "https://wx1.sinaimg.cn/mw2000/synfav01.jpg" || p1.Images[1].Kind != MediaKindGIF {
		t.Fatalf("p1 image tiers = %+v", p1.Images)
	}

	// 2. Abbreviated pic_ids + pic_urls + bmiddle_pic without pic_infos -> promoted HTTPS /large/ + MediaIncomplete=true.
	p2 := page.Posts[1]
	if !p2.MediaIncomplete || len(p2.Images) != 3 {
		t.Fatalf("p2 fallback = incomplete=%v images=%+v", p2.MediaIncomplete, p2.Images)
	}
	if p2.Images[0].URL != "https://wx3.sinaimg.cn/large/synbare01.jpg" || p2.Images[0].PublicPreviewURL != "https://wx3.sinaimg.cn/large/synbare01.jpg" {
		t.Fatalf("p2[0] = %+v", p2.Images[0])
	}
	if p2.Images[1].URL != "https://wx3.sinaimg.cn/large/synbare02.gif" || p2.Images[1].Kind != MediaKindGIF {
		t.Fatalf("p2[1] gif hint = %+v", p2.Images[1])
	}
	if p2.Images[2].URL != "https://wx3.sinaimg.cn/large/synbare03.jpg" {
		t.Fatalf("p2[2] host inheritance = %+v", p2.Images[2])
	}

	// 3. Mobile pics numeric-keyed map sorted 0, 1 + geo false/string + live_photo pairing.
	p3 := page.Posts[2]
	if p3.MediaIncomplete || len(p3.Images) != 2 {
		t.Fatalf("p3 mobile pics = incomplete=%v images=%+v", p3.MediaIncomplete, p3.Images)
	}
	if p3.Images[0].PicID != "synmob01" || p3.Images[0].URL != "https://wx2.sinaimg.cn/large/synmob01.jpg" || p3.Images[0].Width != 1600 {
		t.Fatalf("p3[0] = %+v", p3.Images[0])
	}
	if p3.Images[1].PicID != "synmob02" || p3.Images[1].Kind != MediaKindLivePhoto || !p3.Images[1].NeedsLivePhotoHop || p3.Images[1].URL != "https://wx2.sinaimg.cn/large/synmob02.jpg" {
		t.Fatalf("p3[1] livephoto = %+v", p3.Images[1])
	}

	// 4. mix_media_info with abbreviated video item preserves video cover + duration and marks MediaIncomplete=true.
	p4 := page.Posts[3]
	if !p4.MediaIncomplete || len(p4.Images) != 2 || p4.Images[1].Kind != MediaKindVideo || p4.Images[1].URL != "https://wx4.sinaimg.cn/large/synmixvid.jpg" || p4.Images[1].DurationSec != 19 {
		t.Fatalf("p4 mixed abbreviated video = incomplete=%v images=%+v", p4.MediaIncomplete, p4.Images)
	}

	// 5. page_info.urls + pic_info.pic_big selects 1080P stream and promotes cover to HTTPS /large/.
	p5 := page.Posts[4]
	if p5.MediaIncomplete || p5.Video == nil || p5.Video.Selected == nil || p5.Video.Selected.ShortEdge != 1080 || p5.Video.PublicCoverURL != "https://wx1.sinaimg.cn/large/synvid05.jpg" {
		t.Fatalf("p5 page_info.urls video = incomplete=%v video=%+v", p5.MediaIncomplete, p5.Video)
	}

	// 6. Retweet with bare pic_ids on BOTH outer and retweeted_status populates both image slices and marks incomplete.
	p6 := page.Posts[5]
	if !p6.MediaIncomplete || !p6.OriginIncomplete || len(p6.Images) != 1 || p6.Retweeted == nil || len(p6.Retweeted.Images) != 2 {
		t.Fatalf("p6 retweet bare pic_ids = mediaInc=%v origInc=%v outer=%+v rt=%+v", p6.MediaIncomplete, p6.OriginIncomplete, p6.Images, p6.Retweeted)
	}
	if p6.Images[0].PublicPreviewURL != "https://wx1.sinaimg.cn/large/synouter01.jpg" ||
		p6.Retweeted.Images[0].PublicPreviewURL != "https://wx1.sinaimg.cn/large/synrtpic01.jpg" ||
		p6.Retweeted.Images[1].PublicPreviewURL != "https://wx1.sinaimg.cn/large/synrtpic02.jpg" {
		t.Fatalf("p6 synthesized URLs = outer=%+v rt=%+v", p6.Images, p6.Retweeted.Images)
	}

	// 7. Outer-hoisted abbreviated video card moves onto Retweeted.Video (never misclassified as webpage RichCard) and marks OriginIncomplete=true.
	p7 := page.Posts[6]
	if p7.Video != nil || p7.Card != nil || !p7.OriginIncomplete || p7.Retweeted == nil || p7.Retweeted.Video == nil {
		t.Fatalf("p7 hoisted abbreviated video = outerVid=%+v card=%+v origInc=%v rt=%+v", p7.Video, p7.Card, p7.OriginIncomplete, p7.Retweeted)
	}
	if p7.Retweeted.Video.Title != "被转发原博简略视频" || p7.Retweeted.Video.PublicCoverURL != "https://wx2.sinaimg.cn/large/synrtvid07.jpg" || p7.Retweeted.Video.DurationSec != 64 {
		t.Fatalf("p7 Retweeted.Video = %+v", p7.Retweeted.Video)
	}
}
