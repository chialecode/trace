package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/subscription"
)

func synthXArticleTweetServiceJSON(id, uid, handle, coverURL, inlineURL string) string {
	return `{
		"__typename": "Tweet",
		"rest_id": "` + id + `",
		"core": {"user_results": {"result": ` + synthXUserCoreJSON(uid, handle, synthXCreatorName, "https://pbs.twimg.com/profile_images/1/u_normal.jpg", "bio", 128) + `}},
		"article": {
			"article_results": {
				"result": {
					"id": "QXJ0aWNsZTo5MDAy",
					"rest_id": "2000000000000000102",
					"title": "服务层 X 长文章完整缓存测试",
					"preview_text": "文章摘要",
					"plain_text": "完整长文章纯文本正文内容，支持全文检索与离线渲染。",
					"cover_media": {
						"media_id": "9002000",
						"media_info": {"__typename": "ApiImage", "original_img_url": ` + mustJSONQuote(coverURL) + `, "original_img_width": 1200, "original_img_height": 675}
					},
					"media_entities": [
						{"media_id": "9002001", "media_info": {"__typename": "ApiImage", "original_img_url": ` + mustJSONQuote(inlineURL) + `, "original_img_width": 800, "original_img_height": 600}}
					],
					"content_state": {
						"blocks": [
							{"key": "h1", "type": "header-one", "text": "章节一"},
							{"key": "p1", "type": "unstyled", "text": "长文章段落正文。"},
							{"key": "a1", "type": "atomic", "text": " ", "entityRanges": [{"offset": 0, "length": 1, "key": 0}]}
						],
						"entityMap": [
							{"key": "0", "value": {"type": "MEDIA", "data": {"mediaItems": [{"mediaId": "9002001"}]}}}
						]
					}
				}
			}
		},
		"legacy": {
			"id_str": "` + id + `",
			"user_id_str": "` + uid + `",
			"created_at": "Fri May 17 02:00:00 +0000 2024",
			"full_text": "https://t.co/SynthArtSvc"
		}
	}`
}

func TestXArticlesAndPublicListsServiceEndToEnd(t *testing.T) {
	fixedNow := time.Unix(1795327200, 0).UTC()
	baseMain := string(readXSiteTestdata(t, "webpack_main.js")) + `
;var __listsNames = {9101:"bundle.UserLists",9201:"shared~lists.common"};
var __listsHashes = {9101:"svc1hash",9201:"svc2hash"};
`
	ondemandBytes := readXSiteTestdata(t, "webpack_ondemand.js")
	hop1JS := `self.webpackChunk = self.webpackChunk || [];
self.webpackChunk.push([[9101], { 50001: function(e,t,n){ return Promise.all([n.e(9201)]); } }]);`
	hop2JS := `self.webpackChunk = self.webpackChunk || [];
self.webpackChunk.push([[9201], {
  60001: function(e){ e.exports = {queryId:"SvcListByRestId01",operationName:"ListByRestId",operationType:"query",metadata:{featureSwitches:["f1","f2","f3","f4","f5"],fieldToggles:["t1","t2","t3"]}}; },
  60002: function(e){ e.exports = {queryId:"SvcListTweets02",operationName:"ListLatestTweetsTimeline",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled"],fieldToggles:["withArticleRichContentState","withArticlePlainText"]}}; },
  60003: function(e){ e.exports = {queryId:"SvcListMembers03",operationName:"ListMembers",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled"],fieldToggles:["t1"]}}; },
  60004: function(e){ e.exports = {queryId:"SvcCombinedLists04",operationName:"CombinedLists",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled"],fieldToggles:["t1"]}}; }
}]);`

	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		switch r.URL.Path {
		case "/responsive-web/client-web/main.synth01a.js":
			_, _ = w.Write([]byte(baseMain))
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			_, _ = w.Write(ondemandBytes)
		case "/responsive-web/client-web/bundle.UserLists.svc1hasha.js":
			_, _ = w.Write([]byte(hop1JS))
		case "/responsive-web/client-web/shared~lists.common.svc2hasha.js":
			_, _ = w.Write([]byte(hop2JS))
		default:
			http.NotFound(w, r)
		}
	}))
	defer assetSrv.Close()

	h := newXServiceTestHarness(t, func(o *Options) {
		o.XOpts.Now = func() time.Time { return fixedNow }
		o.XOpts.AssetTransport = newXAbsRewriteTransport(t, assetSrv)
	})

	if _, err := h.backend.LoginWithCookie(context.Background(), string(site.X), synthXValidCookie, ""); err != nil {
		t.Fatalf("login X failed: %v", err)
	}

	// Pre-subscribe one list member so XListMembers annotates Subscribed=true & OwnerSubID.
	sub, err := h.backend.CreateSubscription("已订阅列表成员", "", []subscription.AccountRef{
		{Site: string(site.X), UID: "1000000000000000051", UniqueID: "member_subbed", Name: "已订阅成员"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	const (
		listID        = "18000000001"
		articlePostID = "1900000000000005001"
		listPostID    = "1900000000000005002"
	)

	var mu sync.Mutex
	opCalls := map[string]int{}

	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		op := parts[len(parts)-1]
		mu.Lock()
		opCalls[op]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rate-Limit-Limit", "500")
		w.Header().Set("X-Rate-Limit-Remaining", "490")

		switch op {
		case x.OpUserArticlesTweets:
			artTweet := synthXArticleTweetServiceJSON(articlePostID, synthXCreatorRestID, synthXCreatorHandle, h.xSrv.URL+"/xcdn/art_cover.png", h.xSrv.URL+"/xcdn/art_inline.png")
			_, _ = w.Write([]byte(synthXTimelineTabEnvelope(synthXTweetEntry("tweet-"+articlePostID, artTweet))))
			return true
		case x.OpListByRestId:
			body := `{"data":{"list":{"id_str":"` + listID + `","name":"Go 开源架构观察","description":"合成公开列表简介","member_count":12,"subscriber_count":30,"mode":"Public","user_results":{"result":` +
				synthXUserCoreJSON(synthXCreatorRestID, synthXCreatorHandle, synthXCreatorName, h.xSrv.URL+"/xcdn/creator_avatar.png", "bio", 128) + `}}}}`
			_, _ = w.Write([]byte(body))
			return true
		case x.OpListLatestTweetsTimeline:
			tw := synthXMediaTweetJSON(listPostID, "1000000000000000051", "member_subbed", "", "列表内最新合成推文", h.xSrv.URL+"/xcdn/list_post.png")
			body := `{"data":{"list":{"tweets_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				synthXTweetEntry("tweet-"+listPostID, tw) + `,` + synthXCursorEntry("cursor-bottom-1", "Bottom", "LIST_P2") +
				`]}]}}}}}`
			_, _ = w.Write([]byte(body))
			return true
		case x.OpListMembers:
			m1 := synthXUserCoreJSON("1000000000000000051", "member_subbed", "已订阅成员", h.xSrv.URL+"/xcdn/m1.png", "成员一简介", 50)
			m2 := synthXUserCoreJSON("1000000000000000052", "member_new", "待订阅成员", h.xSrv.URL+"/xcdn/m2.png", "成员二简介", 80)
			body := `{"data":{"list":{"members_timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				synthXUserEntry("user-1", m1) + `,` + synthXUserEntry("user-2", m2) +
				`]}]}}}}}`
			_, _ = w.Write([]byte(body))
			return true
		case x.OpCombinedLists:
			body := `{"data":{"user":{"result":{"timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				`{"entryId":"list-` + listID + `","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTwitterList","list":{"id_str":"` + listID + `","name":"Go 开源架构观察","member_count":12,"mode":"Public"}}}}` +
				`]}]}}}}}}`
			_, _ = w.Write([]byte(body))
			return true
		}
		return false
	}
	h.mu.Unlock()

	// 1. OpenLink on x.com/<handle>/articles lands on XUserTab="articles" with full parsed Article DTO.
	artRes, err := h.backend.OpenLink(context.Background(), "https://x.com/"+synthXCreatorHandle+"/articles")
	if err != nil {
		t.Fatalf("OpenLink(/articles) failed: %v", err)
	}
	if artRes.XUserTab != "articles" || artRes.XUserPosts == nil || len(artRes.XUserPosts.Posts) != 1 {
		t.Fatalf("OpenLink(/articles) unexpected result: %+v", artRes)
	}
	artPost := artRes.XUserPosts.Posts[0]
	if artPost.Article == nil || !artPost.Article.HasFullContent() {
		t.Fatalf("expected full Article DTO on articles tab, got %+v", artPost.Article)
	}
	// Full X Article is cacheable, while degraded CardKindArticle without full content stays uncacheable.
	if IsXPostDTOUncacheable(&artPost) {
		t.Fatal("full X Article DTO must be cacheable")
	}
	degradedArticle := XPostDTO{
		ID:   "1900000000000005999",
		Card: &XRichCardDTO{Kind: "article", Title: "Legacy Stub", Unsupported: true},
	}
	if !IsXPostDTOUncacheable(&degradedArticle) {
		t.Fatal("legacy degraded CardKindArticle without full Article must stay uncacheable")
	}

	// 2. OpenLink on x.com/i/lists/<id> resolves XList + first page of tweets and records x_list in history.json.
	listRes, err := h.backend.OpenLink(context.Background(), "https://x.com/i/lists/"+listID)
	if err != nil {
		t.Fatalf("OpenLink(/i/lists/%s) failed: %v", listID, err)
	}
	if listRes.XList == nil || listRes.XList.ID != listID || listRes.XList.Name != "Go 开源架构观察" {
		t.Fatalf("unexpected OpenLink XList: %+v", listRes.XList)
	}
	if listRes.XListTweets == nil || len(listRes.XListTweets.Posts) != 1 || listRes.XListTweets.Posts[0].ID != listPostID {
		t.Fatalf("unexpected OpenLink XListTweets: %+v", listRes.XListTweets)
	}
	histEntries := h.backend.ListHistory()
	foundListHist := false
	for _, he := range histEntries {
		if he.Site == string(site.X) && he.Kind == "x_list" && he.ID == listID && he.CanonicalURL == "https://x.com/i/lists/"+listID {
			foundListHist = true
			break
		}
	}
	if !foundListHist {
		t.Fatalf("x_list entry not recorded in history: %+v", histEntries)
	}

	// 3. XListMembers annotates subscribed vs unsubscribed members cleanly.
	memPage, err := h.backend.XListMembers(context.Background(), listID, "")
	if err != nil {
		t.Fatalf("XListMembers failed: %v", err)
	}
	if len(memPage.Members) != 2 {
		t.Fatalf("XListMembers len = %d, want 2", len(memPage.Members))
	}
	if !memPage.Members[0].Subscribed || memPage.Members[0].OwnerSubID != sub.ID {
		t.Fatalf("expected member 0 subscribed to %s, got %+v", sub.ID, memPage.Members[0])
	}
	if memPage.Members[1].Subscribed {
		t.Fatalf("expected member 1 unsubscribed, got %+v", memPage.Members[1])
	}

	// 4. OpenLink on x.com/<handle>/lists returns XUserLists cleanly.
	ulRes, err := h.backend.OpenLink(context.Background(), "https://x.com/"+synthXCreatorHandle+"/lists")
	if err != nil {
		t.Fatalf("OpenLink(/lists) failed: %v", err)
	}
	if ulRes.SiteTarget == nil || ulRes.SiteTarget.Tab != "lists" || ulRes.XUserLists == nil || len(ulRes.XUserLists.Lists) != 1 || ulRes.XUserLists.Lists[0].ID != listID {
		t.Fatalf("unexpected OpenLink(/lists) result: %+v", ulRes)
	}

	// 5. CacheXPostsPage accepts Tab="articles" and Tab="list" (reusing in-memory plans with 0 extra TweetDetail).
	batchArt, err := h.backend.CacheXPostsPage(context.Background(), XBatchCachePageRequest{
		Tab:   "articles",
		Posts: []XPostDTO{artPost},
	})
	if err != nil || batchArt.Enqueued != 1 {
		t.Fatalf("CacheXPostsPage(articles) = (%+v, %v), want Enqueued=1", batchArt, err)
	}
	batchList, err := h.backend.CacheXPostsPage(context.Background(), XBatchCachePageRequest{
		Tab:   "list",
		Posts: listRes.XListTweets.Posts,
	})
	if err != nil || batchList.Enqueued != 1 {
		t.Fatalf("CacheXPostsPage(list) = (%+v, %v), want Enqueued=1", batchList, err)
	}

	// 6. DEC-089 manual probe recovery: OpenXList probes once during site backoff and clears backoff on success.
	h.backend.subs.RecordSiteAutomaticStop(string(site.X), site.ErrKindRateLimited)
	if active, _ := h.backend.subs.IsSiteInBackoff(string(site.X)); !active {
		t.Fatal("expected X in backoff")
	}
	if _, err := h.backend.OpenXList(context.Background(), listID); err != nil {
		t.Fatalf("OpenXList manual probe failed: %v", err)
	}
	if active, _ := h.backend.subs.IsSiteInBackoff(string(site.X)); active {
		t.Fatal("expected OpenXList manual probe success to clear X backoff")
	}
}
