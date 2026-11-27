package service

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/x"
)

func synthXMediaTweetJSON(id, uid, handle, replyTo, text, mediaURL string) string {
	return fmt.Sprintf(
		`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":%s}},"legacy":{"id_str":%q,"user_id_str":%q,"created_at":"Fri May 17 00:15:00 +0000 2024","in_reply_to_status_id_str":%q,"full_text":%q,"extended_entities":{"media":[{"id_str":"m_%s","type":"photo","media_url_https":%q,"original_info":{"width":640,"height":480}}]}}}`,
		id,
		synthXUserCoreJSON(uid, handle, synthXCreatorName, "https://pbs.twimg.com/profile_images/1/u_normal.jpg", "bio", 128),
		id, uid, replyTo, text, id, mediaURL,
	)
}

func synthXTimelineTabEnvelope(entriesJSON string) string {
	return `{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` + entriesJSON + `]}]}}}}}}`
}

func TestOpenLinkAndXUserTabPostsAcrossTabsBackoffAndRateBrake(t *testing.T) {
	fixedNow := time.Unix(1795327200, 0).UTC()
	h := newXServiceTestHarness(t, func(o *Options) {
		o.XOpts.Now = func() time.Time { return fixedNow }
	})

	if _, err := h.backend.LoginWithCookie(context.Background(), string(site.X), synthXValidCookie, ""); err != nil {
		t.Fatalf("login X failed: %v", err)
	}

	var mu sync.Mutex
	opCounts := map[string]int{}
	hlBrakeReset := fixedNow.Add(10 * time.Minute)
	var triggerHLBrake atomic.Bool

	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		op := parts[len(parts)-1]
		switch op {
		case x.OpUserTweetsAndReplies:
			mu.Lock()
			opCounts[op]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Rate-Limit-Limit", "500")
			w.Header().Set("X-Rate-Limit-Remaining", "495")
			replyTweet := synthXMediaTweetJSON("1900000000000001001", synthXCreatorRestID, synthXCreatorHandle, "1900000000000000001", "Synthetic reply with photo", h.xSrv.URL+"/xcdn/reply1.png")
			foreignTweet := synthXMediaTweetJSON("1900000000000001099", "1000000000000000888", "foreign_user", "", "Foreign parent must not leak", h.xSrv.URL+"/xcdn/foreign.png")
			convEntry := `{"entryId":"profile-conversation-1","content":{"entryType":"TimelineTimelineModule","displayType":"VerticalConversation","items":[` +
				`{"entryId":"profile-conversation-1-tweet-1900000000000001099","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + foreignTweet + `}}}},` +
				`{"entryId":"profile-conversation-1-tweet-1900000000000001001","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + replyTweet + `}}}}` +
				`]}}`
			_, _ = w.Write([]byte(synthXTimelineTabEnvelope(convEntry + "," + synthXCursorEntry("cursor-bottom-1", "Bottom", "REP_NEXT_2"))))
			return true
		case x.OpUserMedia:
			mu.Lock()
			opCounts[op]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Rate-Limit-Limit", "500")
			w.Header().Set("X-Rate-Limit-Remaining", "496")
			mediaTweet := synthXMediaTweetJSON("1900000000000002001", synthXCreatorRestID, synthXCreatorHandle, "", "Synthetic media grid item", h.xSrv.URL+"/xcdn/media1.png")
			gridEntry := `{"entryId":"profile-grid-0","content":{"entryType":"TimelineTimelineModule","displayType":"VerticalGrid","items":[` +
				`{"entryId":"profile-grid-0-tweet-1900000000000002001","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + mediaTweet + `}}}}` +
				`]}}`
			_, _ = w.Write([]byte(synthXTimelineTabEnvelope(gridEntry + "," + synthXCursorEntry("cursor-bottom-1", "Bottom", "MED_NEXT_2"))))
			return true
		case x.OpUserHighlightsTweets:
			mu.Lock()
			opCounts[op]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Rate-Limit-Limit", "50")
			if triggerHLBrake.Load() {
				w.Header().Set("X-Rate-Limit-Remaining", "1")
				w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(hlBrakeReset.Unix(), 10))
			} else {
				w.Header().Set("X-Rate-Limit-Remaining", "49")
			}
			hlTweet := synthXMediaTweetJSON("1900000000000003001", synthXCreatorRestID, synthXCreatorHandle, "", "Synthetic highlight item", h.xSrv.URL+"/xcdn/hl1.png")
			_, _ = w.Write([]byte(synthXTimelineTabEnvelope(synthXTweetEntry("tweet-1900000000000003001", hlTweet) + "," + synthXCursorEntry("cursor-bottom-1", "Bottom", "HL_NEXT_2"))))
			return true
		}
		return false
	}
	h.mu.Unlock()

	// 1. OpenLink on /with_replies, /media, /highlights lands on the matching tab & calls matching op.
	openCases := []struct {
		url      string
		wantTab  string
		wantPost string
	}{
		{"https://x.com/" + synthXCreatorHandle + "/with_replies", "replies", "1900000000000001001"},
		{"https://x.com/" + synthXCreatorHandle + "/media", "media", "1900000000000002001"},
		{"https://x.com/" + synthXCreatorHandle + "/highlights", "highlights", "1900000000000003001"},
	}
	for _, tc := range openCases {
		res, err := h.backend.OpenLink(context.Background(), tc.url)
		if err != nil {
			t.Fatalf("OpenLink(%s) failed: %v", tc.url, err)
		}
		if res.XUserTab != tc.wantTab || res.SiteTarget == nil || res.SiteTarget.Tab != tc.wantTab {
			t.Fatalf("OpenLink(%s) tab = (XUserTab=%q targetTab=%q), want %q", tc.url, res.XUserTab, res.SiteTarget.Tab, tc.wantTab)
		}
		if res.XUserPosts == nil || len(res.XUserPosts.Posts) != 1 || res.XUserPosts.Posts[0].ID != tc.wantPost {
			t.Fatalf("OpenLink(%s) posts = %+v, want [%s]", tc.url, res.XUserPosts, tc.wantPost)
		}
	}

	// 2. XUserTabPosts supports @handle lookup and rejects invalid tab with 0 requests.
	reqBefore := h.xReqs.Load()
	if _, err := h.backend.XUserTabPosts(context.Background(), synthXCreatorRestID, "invalid_tab", ""); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("invalid tab err = %v, want bad_request", err)
	}
	if h.xReqs.Load() != reqBefore {
		t.Fatalf("invalid tab must not issue any HTTP request")
	}

	pgByHandle, err := h.backend.XUserTabPosts(context.Background(), "@"+synthXCreatorHandle, "media", "")
	if err != nil || pgByHandle.Tab != "media" || len(pgByHandle.Posts) != 1 {
		t.Fatalf("XUserTabPosts(@handle, media) = (%+v, %v)", pgByHandle, err)
	}

	// 3. Proactive rate-limit brake on UserHighlightsTweets (50/15min tight bucket) works when NOT in site backoff.
	triggerHLBrake.Store(true)
	if _, err := h.backend.XUserTabPosts(context.Background(), synthXCreatorRestID, "highlights", ""); err != nil {
		t.Fatalf("highlights brake-priming call failed: %v", err)
	}
	mu.Lock()
	hlBeforeBrake := opCounts[x.OpUserHighlightsTweets]
	mu.Unlock()
	if _, err := h.backend.XUserTabPosts(context.Background(), synthXCreatorRestID, "highlights", "HL_NEXT_2"); !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("braked highlights call err = %v, want rate_limited", err)
	}
	mu.Lock()
	hlAfterBrake := opCounts[x.OpUserHighlightsTweets]
	mu.Unlock()
	if hlAfterBrake != hlBeforeBrake {
		t.Fatalf("proactive brake must not send another HTTP request: before=%d after=%d", hlBeforeBrake, hlAfterBrake)
	}

	// 4. DEC-089 manual probe recovery: when X is in site backoff, XUserTabPosts probes once and clears backoff on success.
	h.backend.subs.RecordSiteAutomaticStop(string(site.X), site.ErrKindRateLimited)
	if active, _ := h.backend.subs.IsSiteInBackoff(string(site.X)); !active {
		t.Fatal("expected X to be in site backoff")
	}
	if _, err := h.backend.XUserTabPosts(context.Background(), synthXCreatorRestID, "media", ""); err != nil {
		t.Fatalf("manual probe during site backoff failed: %v", err)
	}
	if active, _ := h.backend.subs.IsSiteInBackoff(string(site.X)); active {
		t.Fatal("successful manual XUserTabPosts probe should clear X site backoff (DEC-089)")
	}
}

func TestCacheXPostsPageSkipsCachedUnsupportedEnforcesCapAndStopsOnSiteError(t *testing.T) {
	h := newXServiceTestHarness(t, func(o *Options) {
		o.DisableBgWorkers = true
	})
	if _, err := h.backend.LoginWithCookie(context.Background(), string(site.X), synthXValidCookie, ""); err != nil {
		t.Fatalf("login X failed: %v", err)
	}

	var detailCalls atomic.Int32
	h.mu.Lock()
	h.xHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail) {
			detailCalls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
			return true
		}
		return false
	}
	h.mu.Unlock()

	// Pre-populate in-memory xPostCache for 23 synthetic posts so CacheXPost reuses them with 0 TweetDetail calls.
	posts := make([]XPostDTO, 0, 26)
	// 1. Unsupported pure HLS-only video post (must be skipped).
	posts = append(posts, XPostDTO{
		ID:              "1900000000000009001",
		CacheKey:        x.PostItemKey("1900000000000009001"),
		Author:          XAuthorDTO{RestID: synthXCreatorRestID, ScreenName: synthXCreatorHandle, Name: synthXCreatorName},
		Text:            "HLS only stream",
		HasHLSOnlyVideo: true,
		Media:           []XMediaItemDTO{{Index: 1, Kind: x.MediaKindVideo, HLSOnly: true}},
	})
	// 2. Unsupported degraded live/article card with no downloadable media (must be skipped).
	posts = append(posts, XPostDTO{
		ID:       "1900000000000009002",
		CacheKey: x.PostItemKey("1900000000000009002"),
		Author:   XAuthorDTO{RestID: synthXCreatorRestID, ScreenName: synthXCreatorHandle, Name: synthXCreatorName},
		Text:     "Live broadcast card only",
		Card:     &XRichCardDTO{Kind: "live", Title: "Live Stream", Unsupported: true},
	})
	// 3. Unavailable post (must be skipped).
	posts = append(posts, XPostDTO{
		ID:          "1900000000000009003",
		CacheKey:    x.PostItemKey("1900000000000009003"),
		Unavailable: true,
	})
	// 4..26: 23 cacheable posts (1 pre-active, 20 enqueued, remaining 2 capped).
	for i := 0; i < 23; i++ {
		id := fmt.Sprintf("1900000000000008%03d", i+1)
		imgURL := h.xSrv.URL + "/xcdn/p" + strconv.Itoa(i+1) + ".png"
		p := &x.Post{
			ID:           id,
			Author:       x.AuthorSummary{RestID: synthXCreatorRestID, ScreenName: synthXCreatorHandle, Name: synthXCreatorName},
			CreatedAt:    1710004000 + int64(i),
			Text:         fmt.Sprintf("Synthetic cacheable media post %d", i+1),
			RuleType:     x.RuleTypeDraw,
			CanonicalURL: "https://x.com/" + synthXCreatorHandle + "/status/" + id,
			Media: []x.MediaItem{{
				Index:    1,
				Kind:     x.MediaKindPhoto,
				ImageURL: imgURL,
				Width:    640,
				Height:   480,
			}},
		}
		h.backend.xPostCache.put(p, false)
		posts = append(posts, XPostDTO{
			ID:           p.ID,
			CacheKey:     x.PostItemKey(p.ID),
			Author:       XAuthorDTO{RestID: synthXCreatorRestID, ScreenName: synthXCreatorHandle, Name: synthXCreatorName},
			CreatedAt:    p.CreatedAt,
			Text:         p.Text,
			RuleType:     p.RuleType,
			CanonicalURL: p.CanonicalURL,
			Media:        []XMediaItemDTO{{Index: 1, Kind: x.MediaKindPhoto, ImageURL: imgURL, Width: 640, Height: 480}},
		})
	}

	// Pre-enqueue post #1 so it is already active ("ring") and counted as SkippedCached.
	if _, err := h.backend.CacheXPost(context.Background(), "1900000000000008001", nil); err != nil {
		t.Fatalf("seed active task failed: %v", err)
	}

	res, err := h.backend.CacheXPostsPage(context.Background(), XBatchCachePageRequest{
		UserUID:  synthXCreatorRestID,
		UserName: synthXCreatorName,
		Tab:      "media",
		Posts:    posts,
	})
	if err != nil {
		t.Fatalf("CacheXPostsPage failed: %v", err)
	}
	if res.SkippedUnsupported != 3 {
		t.Fatalf("SkippedUnsupported = %d, want 3", res.SkippedUnsupported)
	}
	if res.SkippedCached != 1 {
		t.Fatalf("SkippedCached = %d, want 1", res.SkippedCached)
	}
	if res.Enqueued != MaxXCurrentPageBatchCache {
		t.Fatalf("Enqueued = %d, want %d", res.Enqueued, MaxXCurrentPageBatchCache)
	}
	if res.SkippedCapped != 2 {
		t.Fatalf("SkippedCapped = %d, want 2 (23 cacheable - 1 active - 20 enqueued)", res.SkippedCapped)
	}
	if detailCalls.Load() != 0 {
		t.Fatalf("CacheXPostsPage should reuse in-memory xPostCache with 0 TweetDetail calls, got %d", detailCalls.Load())
	}
	// Verify cache-queue.json was NOT touched (zero schema upgrade / stays v3).
	if _, err := os.Stat(filepath.Join(h.loc.DataDir, "cache-queue.json")); !os.IsNotExist(err) {
		t.Fatalf("CacheXPostsPage must not create/modify cache-queue.json, stat err = %v", err)
	}

	// Site-level error (rate_limited from TweetDetail cache miss) stops batch immediately.
	uncachedMisses := []XPostDTO{
		{ID: "1900000000000009901", CacheKey: x.PostItemKey("1900000000000009901"), Text: "Miss 1"},
		{ID: "1900000000000009902", CacheKey: x.PostItemKey("1900000000000009902"), Text: "Miss 2"},
	}
	errRes, err := h.backend.CacheXPostsPage(context.Background(), XBatchCachePageRequest{
		UserUID: synthXCreatorRestID,
		Tab:     "replies",
		Posts:   uncachedMisses,
	})
	if !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("site-error batch err = %v, want rate_limited", err)
	}
	if errRes.Failed != 1 || detailCalls.Load() != 1 {
		t.Fatalf("site-level error must stop batch immediately after 1st failure: failed=%d detailCalls=%d", errRes.Failed, detailCalls.Load())
	}
}
