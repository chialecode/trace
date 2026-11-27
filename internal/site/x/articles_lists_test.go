package x

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func buildSynthArticleTweetJSON(statusID, uid, handle string, entityMapArray bool) string {
	entityMapJSON := `{
		"0": {"type": "LINK", "mutability": "MUTABLE", "data": {"url": "https://example.com/trace-spec"}},
		"1": {"type": "LINK", "mutability": "MUTABLE", "data": {"url": "javascript:alert(1)"}},
		"2": {"type": "MEDIA", "mutability": "IMMUTABLE", "data": {"mediaItems": [{"mediaId": "9001001"}, {"mediaId": "9001002"}]}},
		"3": {"type": "TWEET", "mutability": "IMMUTABLE", "data": {"tweetId": "1791257321273778888"}}
	}`
	if entityMapArray {
		// Real X GraphQL shape verified in DEC-077: entityMap is a JSON array [{key, value}].
		entityMapJSON = `[
			{"key": "0", "value": {"type": "LINK", "mutability": "MUTABLE", "data": {"url": "https://example.com/trace-spec"}}},
			{"key": "1", "value": {"type": "LINK", "mutability": "MUTABLE", "data": {"url": "javascript:alert(1)"}}},
			{"key": "2", "value": {"type": "MEDIA", "mutability": "IMMUTABLE", "data": {"mediaItems": [{"mediaId": "9001001"}, {"mediaId": "9001002"}]}}},
			{"key": "3", "value": {"type": "TWEET", "mutability": "IMMUTABLE", "data": {"tweetId": "1791257321273778888"}}}
		]`
	}
	return `{
		"__typename": "Tweet",
		"rest_id": "` + statusID + `",
		"core": {"user_results": {"result": ` + makeUserCoreJSON(uid, handle, "Synth Author", "https://pbs.twimg.com/profile_images/1/u_normal.jpg") + `}},
		"article": {
			"article_results": {
				"result": {
					"id": "QXJ0aWNsZTo5MDAx",
					"rest_id": "2000000000000000099",
					"title": "X Articles 富文本离线归档架构说明",
					"preview_text": "文章摘要：支持 Draft.js 富文本块、内嵌图与推文引用。",
					"plain_text": "文章纯文本回退正文：覆盖全部段落与引用。",
					"cover_media": {
						"media_id": "9001000",
						"media_key": "3_9001000",
						"media_info": {
							"__typename": "ApiImage",
							"original_img_url": "https://pbs.twimg.com/media/SynthArtCover00.jpg",
							"original_img_width": 1600,
							"original_img_height": 900
						}
					},
					"media_entities": [
						{
							"media_id": "9001001",
							"media_key": "3_9001001",
							"media_info": {
								"__typename": "ApiImage",
								"original_img_url": "https://pbs.twimg.com/media/SynthArtInline01.png",
								"original_img_width": 1200,
								"original_img_height": 800
							}
						},
						{
							"media_id": 9001002,
							"media_key": "3_9001002",
							"media_info": {
								"__typename": "ApiImage",
								"original_img_url": "https://pbs.twimg.com/media/SynthArtInline02.jpg",
								"original_img_width": 1080,
								"original_img_height": 720
							}
						}
					],
					"content_state": {
						"blocks": [
							{"key": "b1", "type": "header-one", "text": "第一章 协议设计"},
							{"key": "b2", "type": "unstyled", "text": "阅读规格文档与不安全链接测试。", "entityRanges": [{"offset": 2, "length": 4, "key": 0}, {"offset": 7, "length": 5, "key": 1}]},
							{"key": "b3", "type": "header-two", "text": "1.1 核心约束"},
							{"key": "b4", "type": "blockquote", "text": "所有正文与图片均离线校验后登记进清单。"},
							{"key": "b5", "type": "ordered-list-item", "text": "零 schema 版本变更"},
							{"key": "b6", "type": "unordered-list-item", "text": "严禁落盘远程 CDN URL"},
							{"key": "b7", "type": "atomic", "text": " ", "entityRanges": [{"offset": 0, "length": 1, "key": 2}]},
							{"key": "b8", "type": "atomic", "text": " ", "entityRanges": [{"offset": 0, "length": 1, "key": 3}]}
						],
						"entityMap": ` + entityMapJSON + `
					}
				}
			}
		},
		"legacy": {
			"id_str": "` + statusID + `",
			"user_id_str": "` + uid + `",
			"created_at": "Fri May 17 01:00:00 +0000 2024",
			"full_text": "https://t.co/SynthArt01"
		}
	}`
}

func TestXArticleParsingEntityMapArrayAndObjectAndMediaEntities(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_ART_01; ct0=SynthArtCT0")
	uid := "1000000000000000001"
	statusID := "1791257321273778601"

	for _, tc := range []struct {
		name           string
		entityMapArray bool
	}{
		{name: "entityMap JSON array (DEC-077 real X format)", entityMapArray: true},
		{name: "entityMap JSON object (classic Draft.js)", entityMapArray: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotToggles map[string]bool
			_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
				if rawToggles := r.URL.Query().Get("fieldToggles"); rawToggles != "" {
					_ = json.Unmarshal([]byte(rawToggles), &gotToggles)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(wrapTweetDetailEntries(
					makeTweetEntry("tweet-"+statusID, buildSynthArticleTweetJSON(statusID, uid, "synth_writer", tc.entityMapArray)),
				)))
			}, store, nil)

			post, err := client.GetPost(context.Background(), statusID)
			if err != nil {
				t.Fatalf("GetPost article failed: %v", err)
			}
			if !gotToggles["withArticleRichContentState"] || !gotToggles["withArticlePlainText"] {
				t.Fatalf("TweetDetail fieldToggles missing article flags: %+v", gotToggles)
			}
			if post.Article == nil || !post.Article.HasFullContent() {
				t.Fatalf("expected full parsed Article, got %+v", post.Article)
			}
			art := post.Article
			if art.Title != "X Articles 富文本离线归档架构说明" {
				t.Fatalf("Article.Title = %q", art.Title)
			}
			if art.Cover == nil || !strings.Contains(art.Cover.ImageURL, "pbs.twimg.com/media/SynthArtCover00") || !strings.Contains(art.Cover.ImageURL, "name=orig") {
				t.Fatalf("Article.Cover = %+v", art.Cover)
			}
			if len(art.Images) != 2 {
				t.Fatalf("Article.Images len = %d, want 2: %+v", len(art.Images), art.Images)
			}
			if !strings.Contains(art.Images[0].ImageURL, "SynthArtInline01") || !strings.Contains(art.Images[1].ImageURL, "SynthArtInline02") {
				t.Fatalf("inline images out of order: %+v", art.Images)
			}

			// Verify all block types, safe link filtering, inline image expansion, and TWEET ref.
			var gotTypes []string
			var foundSafeLink, foundUnsafeLink, foundTweetRef bool
			inlineImgBlocks := 0
			for _, b := range art.Blocks {
				gotTypes = append(gotTypes, b.Type)
				for _, ln := range b.Links {
					if ln.URL == "https://example.com/trace-spec" && ln.Label == "规格文档" {
						foundSafeLink = true
					}
					if strings.HasPrefix(strings.ToLower(ln.URL), "javascript:") {
						foundUnsafeLink = true
					}
				}
				if b.Type == ArticleBlockAtomic && b.Image != nil {
					inlineImgBlocks++
				}
				if b.Type == ArticleBlockAtomic && b.TweetID == "1791257321273778888" {
					foundTweetRef = true
				}
			}
			wantTypes := []string{
				ArticleBlockHeaderOne,
				ArticleBlockParagraph,
				ArticleBlockHeaderTwo,
				ArticleBlockBlockquote,
				ArticleBlockOrderedList,
				ArticleBlockUnorderedList,
				ArticleBlockAtomic,
				ArticleBlockAtomic,
				ArticleBlockAtomic,
			}
			if strings.Join(gotTypes, ",") != strings.Join(wantTypes, ",") {
				t.Fatalf("block types = %v, want %v", gotTypes, wantTypes)
			}
			if !foundSafeLink || foundUnsafeLink {
				t.Fatalf("safe link=%v unsafe link=%v (blocks=%+v)", foundSafeLink, foundUnsafeLink, art.Blocks)
			}
			if inlineImgBlocks != 2 {
				t.Fatalf("inlineImgBlocks = %d, want 2", inlineImgBlocks)
			}
			if !foundTweetRef {
				t.Fatalf("expected embedded TWEET entity tweetId=1791257321273778888")
			}
			if post.Card != nil && post.Card.Kind == CardKindArticle {
				t.Fatalf("full parsed Article should not duplicate degraded CardKindArticle: %+v", post.Card)
			}
			if post.RuleType != RuleTypeDraw {
				t.Fatalf("RuleType for article with cover/inline images = %q, want %q", post.RuleType, RuleTypeDraw)
			}
		})
	}
}

func TestUserArticlesTweetsPaginationAndFieldToggles(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_ART_TAB_01; ct0=SynthArtTabCT0")
	uid := "1000000000000000001"
	id1 := "1791257321273778701"
	id2 := "1791257321273778702"

	var calls atomic.Int32
	var seenToggles []map[string]bool
	var mu sync.Mutex

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+OpUserArticlesTweets) {
			http.NotFound(w, r)
			return
		}
		var toggles map[string]bool
		_ = json.Unmarshal([]byte(r.URL.Query().Get("fieldToggles")), &toggles)
		mu.Lock()
		seenToggles = append(seenToggles, toggles)
		mu.Unlock()

		var vars map[string]any
		_ = json.Unmarshal([]byte(r.URL.Query().Get("variables")), &vars)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rate-Limit-Limit", "500")
		w.Header().Set("X-Rate-Limit-Remaining", "498")

		n := calls.Add(1)
		if n == 1 {
			body := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				makeTweetEntry("tweet-"+id1, buildSynthArticleTweetJSON(id1, uid, "synth_writer", true)) + `,` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"ART_CURSOR_2"}}` +
				`]}]}}}}}}`
			_, _ = w.Write([]byte(body))
			return
		}
		if vars["cursor"] != "ART_CURSOR_2" {
			t.Errorf("page 2 cursor = %v, want ART_CURSOR_2", vars["cursor"])
		}
		body := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
			makeTweetEntry("tweet-"+id2, buildSynthArticleTweetJSON(id2, uid, "synth_writer", false)) +
			`]}]}}}}}}`
		_, _ = w.Write([]byte(body))
	}, store, nil)

	p1, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{RestID: uid, Tab: ProfileTabArticles})
	if err != nil {
		t.Fatalf("page 1 GetUserTabTweets(articles) failed: %v", err)
	}
	if len(p1.Posts) != 1 || p1.Posts[0].ID != id1 || p1.Posts[0].Article == nil || !p1.Posts[0].Article.HasFullContent() || !p1.HasMore || p1.NextCursor != "ART_CURSOR_2" {
		t.Fatalf("unexpected page 1: %+v", p1)
	}

	p2, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{RestID: uid, Tab: ProfileTabArticles, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatalf("page 2 GetUserTabTweets(articles) failed: %v", err)
	}
	if len(p2.Posts) != 1 || p2.Posts[0].ID != id2 || p2.HasMore {
		t.Fatalf("unexpected page 2: %+v", p2)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenToggles) != 2 {
		t.Fatalf("seenToggles len = %d, want 2", len(seenToggles))
	}
	for i, tg := range seenToggles {
		if !tg["withArticleRichContentState"] || !tg["withArticlePlainText"] {
			t.Fatalf("call %d missing article fieldToggles: %+v", i, tg)
		}
	}
}

func TestListsTwoHopLazyDiscoveryFeatureSplitPaginationAndRateBrake(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_LIST_01; ct0=SynthListCT0")
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	baseMain := string(mustReadTestdata(t, "webpack_main.js"))

	// Augment webpack_main.js with chunk name/hash tables pointing to bundle.UserLists (Hop 1)
	// and shared~lists (Hop 2) matching X's DEC-077 2-hop topology.
	mainWithLists := baseMain + `
;var __listsNames = {9101:"bundle.UserLists",9102:"loader.ListHandler",9201:"shared~lists.common"};
var __listsHashes = {9101:"hop1hasha",9102:"hop1hashb",9201:"hop2hasha"};
`
	hop1BundleJS := `self.webpackChunk = self.webpackChunk || [];
self.webpackChunk.push([[9101], {
  50001: function(e, t, n) {
    return Promise.all([n.e(9201)]).then(function() { return n(60001); });
  }
}]);`

	hop2SharedJS := `self.webpackChunk = self.webpackChunk || [];
self.webpackChunk.push([[9201], {
  60001: function(e) {
    e.exports = {
      queryId: "SynthQidListByRestId01",
      operationName: "ListByRestId",
      operationType: "query",
      metadata: {
        featureSwitches: ["feat_list_1","feat_list_2","feat_list_3","feat_list_4","feat_list_5"],
        fieldToggles: ["toggle_list_1","toggle_list_2","toggle_list_3"]
      }
    };
  },
  60002: function(e) {
    e.exports = {
      queryId: "SynthQidListLatestTweets02",
      operationName: "ListLatestTweetsTimeline",
      operationType: "query",
      metadata: {
        featureSwitches: ["responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled","creator_subscriptions_tweet_preview_api_enabled","feat_4","feat_5","feat_6","feat_7"],
        fieldToggles: ["withArticleRichContentState","withArticlePlainText","withGrokAnalyze","toggle_4"]
      }
    };
  },
  60003: function(e) {
    e.exports = {
      queryId: "SynthQidListMembers03",
      operationName: "ListMembers",
      operationType: "query",
      metadata: {
        featureSwitches: ["responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled","feat_3","feat_4","feat_5","feat_6","feat_7"],
        fieldToggles: ["toggle_1","toggle_2","toggle_3","toggle_4"]
      }
    };
  },
  60004: function(e) {
    e.exports = {
      queryId: "SynthQidCombinedLists04",
      operationName: "CombinedLists",
      operationType: "query",
      metadata: {
        featureSwitches: ["responsive_web_graphql_exclude_directive_enabled","feat_2","feat_3","feat_4","feat_5","feat_6","feat_7"],
        fieldToggles: ["toggle_1","toggle_2","toggle_3","toggle_4"]
      }
    };
  },
  60005: function(e) {
    e.exports = {
      queryId: "SynthQidListOwnerships05",
      operationName: "ListOwnerships",
      operationType: "query",
      metadata: {
        featureSwitches: ["responsive_web_graphql_exclude_directive_enabled","feat_2","feat_3","feat_4","feat_5","feat_6","feat_7"],
        fieldToggles: ["toggle_1","toggle_2","toggle_3","toggle_4"]
      }
    };
  }
}]);`

	const (
		listID      = "18000000001"
		creatorUID  = "1000000000000000001"
		member1UID  = "1000000000000000011"
		member2UID  = "1000000000000000012"
		listTweetID = "1791257321273778901"
	)

	var (
		mu              sync.Mutex
		lazyJSPaths     []string
		gqlFeaturesLen  = map[string]int{}
		gqlTogglesMap   = map[string]map[string]bool{}
		brakeListTweets atomic.Bool
		listTweetsHits  atomic.Int32
		failCombined    atomic.Bool
	)
	fixedNow := time.Unix(1795327200, 0).UTC()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
			return
		case "/responsive-web/client-web/main.synth01a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(mainWithLists))
			return
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
			return
		case "/responsive-web/client-web/bundle.UserLists.hop1hashaa.js":
			mu.Lock()
			lazyJSPaths = append(lazyJSPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(hop1BundleJS))
			return
		case "/responsive-web/client-web/shared~lists.common.hop2hashaa.js":
			mu.Lock()
			lazyJSPaths = append(lazyJSPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(hop2SharedJS))
			return
		}

		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		op := parts[len(parts)-1]
		var feats map[string]bool
		var toggles map[string]bool
		if raw := r.URL.Query().Get("features"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &feats)
		}
		if raw := r.URL.Query().Get("fieldToggles"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &toggles)
		}
		mu.Lock()
		gqlFeaturesLen[op] = len(feats)
		gqlTogglesMap[op] = toggles
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rate-Limit-Limit", "500")
		w.Header().Set("X-Rate-Limit-Remaining", "490")

		switch op {
		case OpListByRestId:
			body := `{"data":{"list":{"id_str":"` + listID + `","name":"Go 开源架构观察","description":"合成公开技术列表","member_count":42,"subscriber_count":120,"mode":"Public","user_results":{"result":` +
				makeUserCoreJSON(creatorUID, "synth_curator", "列表维护者", "https://pbs.twimg.com/profile_images/1/u_normal.jpg") + `}}}}`
			_, _ = w.Write([]byte(body))
		case OpListLatestTweetsTimeline:
			listTweetsHits.Add(1)
			if brakeListTweets.Load() {
				w.Header().Set("X-Rate-Limit-Remaining", "1")
				w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(fixedNow.Add(10*time.Minute).Unix(), 10))
			}
			body := `{"data":{"list":{"tweets_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				makeTweetEntry("tweet-"+listTweetID, buildSynthArticleTweetJSON(listTweetID, member1UID, "synth_m1", true)) + `,` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"LIST_TW_CUR_2"}}` +
				`]}]}}}}}`
			_, _ = w.Write([]byte(body))
		case OpListMembers:
			body := `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				`{"entryId":"user-` + member1UID + `","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineUser","user_results":{"result":` + makeUserCoreJSON(member1UID, "synth_m1", "成员甲", "https://pbs.twimg.com/profile_images/1/m1_normal.jpg") + `}}}},` +
				`{"entryId":"user-` + member2UID + `","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineUser","user_results":{"result":` + makeUserCoreJSON(member2UID, "synth_m2", "成员乙", "https://pbs.twimg.com/profile_images/1/m2_normal.jpg") + `}}}},` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"LIST_MEM_CUR_2"}}` +
				`]}]}}}}}`
			_, _ = w.Write([]byte(body))
		case OpCombinedLists:
			if failCombined.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body := `{"data":{"user":{"result":{"timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				`{"entryId":"list-` + listID + `","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTwitterList","list":{"id_str":"` + listID + `","name":"Go 开源架构观察","member_count":42,"mode":"Public"}}}}` +
				`]}]}}}}}}`
			_, _ = w.Write([]byte(body))
		case OpListOwnerships:
			body := `{"data":{"user":{"result":{"timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				`{"entryId":"list-18000000002","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTwitterList","list":{"id_str":"18000000002","name":"备用列表来源","member_count":18,"mode":"Public"}}}}` +
				`]}]}}}}}}`
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			HomeURL:         ts.URL + "/home",
			AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
			GraphQLBase:     ts.URL + "/i/api/graphql",
		},
		MinInterval:        -1,
		RateLimitJitterMax: -1,
		AllowLocalhost:     true,
		AssetTransport:     newAbsRewriteTransport(t, ts),
		Now:                func() time.Time { return fixedNow },
		Sleep:              func(context.Context, time.Duration) error { return nil },
		TxIDOptions: []TxIDOption{
			WithTxIDClock(func() time.Time { return fixedNow }),
			WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
		},
	})

	// 1. First Lists call triggers 2-hop discovery (bundle.UserLists -> shared~lists.common) in <= 2 static JS fetches.
	ls, err := client.GetListByRestId(context.Background(), listID)
	if err != nil {
		t.Fatalf("GetListByRestId failed: %v", err)
	}
	if ls.ID != listID || ls.Name != "Go 开源架构观察" || ls.MemberCount != 42 || ls.Creator.ScreenName != "synth_curator" {
		t.Fatalf("unexpected list summary: %+v", ls)
	}
	mu.Lock()
	afterFirstLazy := len(lazyJSPaths)
	mu.Unlock()
	if afterFirstLazy == 0 || afterFirstLazy > 2 {
		t.Fatalf("expected <= 2 lazy JS fetches for 2-hop discovery, got %d: %v", afterFirstLazy, lazyJSPaths)
	}

	// 2. Subsequent Lists calls (tweets, members, user lists) must reuse cached ops with 0 extra lazy JS fetches!
	twPage, err := client.GetListTweets(context.Background(), listID, "", nil)
	if err != nil {
		t.Fatalf("GetListTweets failed: %v", err)
	}
	if len(twPage.Posts) != 1 || twPage.Posts[0].ID != listTweetID || twPage.Posts[0].Article == nil || !twPage.HasMore || twPage.NextCursor != "LIST_TW_CUR_2" {
		t.Fatalf("unexpected list tweets page: %+v", twPage)
	}

	memPage, err := client.GetListMembers(context.Background(), listID, "", nil)
	if err != nil {
		t.Fatalf("GetListMembers failed: %v", err)
	}
	if len(memPage.Members) != 2 || memPage.Members[0].RestID != member1UID || memPage.Members[1].ScreenName != "synth_m2" || !memPage.HasMore {
		t.Fatalf("unexpected list members page: %+v", memPage)
	}

	userLists, err := client.GetUserLists(context.Background(), creatorUID, "", nil)
	if err != nil || len(userLists.Lists) != 1 || userLists.Lists[0].ID != listID {
		t.Fatalf("GetUserLists(CombinedLists) = (%+v, %v)", userLists, err)
	}

	mu.Lock()
	afterAllLazy := len(lazyJSPaths)
	listByFeatCnt := gqlFeaturesLen[OpListByRestId]
	listByTogglesCnt := len(gqlTogglesMap[OpListByRestId])
	tweetsFeatCnt := gqlFeaturesLen[OpListLatestTweetsTimeline]
	tweetsToggles := gqlTogglesMap[OpListLatestTweetsTimeline]
	membersFeatCnt := gqlFeaturesLen[OpListMembers]
	mu.Unlock()

	if afterAllLazy != afterFirstLazy {
		t.Fatalf("expected 0 extra lazy JS fetches after initial 2-hop discovery: first=%d all=%d", afterFirstLazy, afterAllLazy)
	}
	// Verify ListByRestId uses its compact 5-feature / 3-toggle schema while timeline/members use the wider schema.
	if listByFeatCnt != 5 || listByTogglesCnt != 3 {
		t.Fatalf("ListByRestId features/toggles = %d/%d, want 5/3", listByFeatCnt, listByTogglesCnt)
	}
	if tweetsFeatCnt <= listByFeatCnt || membersFeatCnt <= listByFeatCnt {
		t.Fatalf("expected ListLatestTweetsTimeline (%d) & ListMembers (%d) to carry wider feature switches than ListByRestId (%d)", tweetsFeatCnt, membersFeatCnt, listByFeatCnt)
	}
	if !tweetsToggles["withArticleRichContentState"] || !tweetsToggles["withArticlePlainText"] {
		t.Fatalf("ListLatestTweetsTimeline must request article fieldToggles, got %+v", tweetsToggles)
	}

	// 3. Proactive rate-limit brake (x-rate-limit-remaining <= 1) halts subsequent calls with 0 upstream requests.
	brakeListTweets.Store(true)
	if _, err := client.GetListTweets(context.Background(), listID, "LIST_TW_CUR_2", nil); err != nil {
		t.Fatalf("priming brake call failed: %v", err)
	}
	beforeBrake := listTweetsHits.Load()
	if _, err := client.GetListTweets(context.Background(), listID, "LIST_TW_CUR_3", nil); !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("expected rate_limited when remaining <= 1, got %v", err)
	}
	if listTweetsHits.Load() != beforeBrake {
		t.Fatalf("proactive brake must not send HTTP request: before=%d after=%d", beforeBrake, listTweetsHits.Load())
	}

	// 4. Fallback from CombinedLists to ListOwnerships when CombinedLists fails.
	failCombined.Store(true)
	fallbackLists, err := client.GetUserLists(context.Background(), creatorUID, "", nil)
	if err != nil || len(fallbackLists.Lists) != 1 || fallbackLists.Lists[0].ID != "18000000002" {
		t.Fatalf("GetUserLists fallback to ListOwnerships = (%+v, %v)", fallbackLists, err)
	}
}

func TestResolveLinksArticlesAndPublicLists(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_RES_01; ct0=SynthResCT0")
	var reqCount atomic.Int32
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		http.NotFound(w, r)
	}, store, nil)

	// 1. Public list links resolve with 0 network requests to Kind="x_list".
	for _, raw := range []string{
		"https://x.com/i/lists/18000000001",
		"https://twitter.com/i/lists/0018000000001?s=20",
		"https://mobile.x.com/i/lists/18000000001/members",
	} {
		tgt, err := client.ResolveLink(context.Background(), raw)
		if err != nil {
			t.Fatalf("ResolveLink(%q) failed: %v", raw, err)
		}
		if tgt.Kind != "x_list" || tgt.ID != "18000000001" || tgt.CanonicalURL != "https://x.com/i/lists/18000000001" {
			t.Fatalf("ResolveLink(%q) = %+v", raw, tgt)
		}
	}

	// 2. /<handle>/articles & /<handle>/lists classify locally to user_handle with matching Tab.
	uArt, _ := url.Parse("https://x.com/synth_writer/articles")
	resArt := client.classifyLocalURL(uArt)
	if resArt.kind != routeUserHandle || resArt.tab != ProfileTabArticles {
		t.Fatalf("classifyLocalURL(/articles) = %+v", resArt)
	}
	uLists, _ := url.Parse("https://x.com/synth_writer/lists")
	resLists := client.classifyLocalURL(uLists)
	if resLists.kind != routeUserHandle || resLists.tab != "lists" {
		t.Fatalf("classifyLocalURL(/lists) = %+v", resLists)
	}

	// 3. Standalone /i/article/<id> and legacy /<handle>/lists/<slug> rejected locally with 0 requests.
	for _, raw := range []string{
		"https://x.com/i/article/2000000000000000099",
		"https://x.com/synth_writer/article/2000000000000000099",
		"https://x.com/synth_writer/lists/legacy-slug",
	} {
		if _, err := client.ResolveLink(context.Background(), raw); !site.IsKind(err, site.ErrKindUnsupportedLink) {
			t.Fatalf("ResolveLink(%q) err = %v, want unsupported_link", raw, err)
		}
	}
	if reqCount.Load() != 0 {
		t.Fatalf("expected 0 network requests for local link resolution & rejections, got %d", reqCount.Load())
	}
}
