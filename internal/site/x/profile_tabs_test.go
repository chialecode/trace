package x

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func TestProfileTabOperationsDiscoveredFromWebpackAndViteMainJS(t *testing.T) {
	cases := []struct {
		name         string
		homeFile     string
		mainPath     string
		mainFile     string
		ondemandPath string
		ondemandFile string
		wantQIDs     map[string]string
	}{
		{
			name:         "webpack main.js direct",
			homeFile:     "webpack_home.html",
			mainPath:     "/responsive-web/client-web/main.synth01a.js",
			mainFile:     "webpack_main.js",
			ondemandPath: "/responsive-web/client-web/ondemand.s.synth02a.js",
			ondemandFile: "webpack_ondemand.js",
			wantQIDs: map[string]string{
				OpUserTweetsAndReplies: "SynthQidUserTweetsAndReplies08",
				OpUserMedia:            "SynthQidUserMedia09",
				OpUserHighlightsTweets: "SynthQidUserHighlightsTweets10",
			},
		},
		{
			name:         "vite main.js direct",
			homeFile:     "vite_home.html",
			mainPath:     "/responsive-web/x-web/assets/main-vite01.js",
			mainFile:     "vite_main.js",
			ondemandPath: "/responsive-web/x-web/assets/ondemand.s-vite02.js",
			ondemandFile: "vite_ondemand.js",
			wantQIDs: map[string]string{
				OpUserTweetsAndReplies: "SynthViteQidUserTweetsAndReplies08",
				OpUserMedia:            "SynthViteQidUserMedia09",
				OpUserHighlightsTweets: "SynthViteQidUserHighlightsTweets10",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeBytes := mustReadTestdata(t, tc.homeFile)
			mainBytes := mustReadTestdata(t, tc.mainFile)
			ondemandBytes := mustReadTestdata(t, tc.ondemandFile)

			var lazyHits atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/home":
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					_, _ = w.Write(homeBytes)
				case tc.mainPath:
					w.Header().Set("Content-Type", "application/javascript")
					_, _ = w.Write(mainBytes)
				case tc.ondemandPath:
					w.Header().Set("Content-Type", "application/javascript")
					_, _ = w.Write(ondemandBytes)
				default:
					lazyHits.Add(1)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()

			fixedNow := time.Unix(1795327200, 0).UTC()
			disc := NewFrontendDiscovery(FrontendDiscoveryConfig{
				FetchHome: func(ctx context.Context, homeURL string) ([]byte, error) {
					return homeBytes, nil
				},
				Now:            func() time.Time { return fixedNow },
				AssetTransport: newAbsRewriteTransport(t, ts),
				TxIDOptions: []TxIDOption{
					WithTxIDClock(func() time.Time { return fixedNow }),
					WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
				},
			})
			meta, err := disc.Ensure(context.Background())
			if err != nil {
				t.Fatalf("Ensure failed: %v", err)
			}
			for op, wantQID := range tc.wantQIDs {
				got, ok := meta.Operations[op]
				if !ok {
					t.Fatalf("missing operation %s from main.js", op)
				}
				if got.QueryID != wantQID {
					t.Fatalf("op %s queryId = %q, want %q", op, got.QueryID, wantQID)
				}
				if len(got.Features) == 0 {
					t.Fatalf("op %s expected non-empty features", op)
				}
				if len(got.FieldToggles) == 0 {
					t.Fatalf("op %s expected non-empty fieldToggles", op)
				}
			}
			if lazyHits.Load() != 0 {
				t.Fatalf("expected 0 lazy chunk fetches for profile tab ops, got %d", lazyHits.Load())
			}
		})
	}
}

func TestProfileTabOperationsOptionalWhenMissingFromBundle(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_OPT_01; ct0=SynthOptCT0")
	homeBytes := mustReadTestdata(t, "webpack_home.html")
	ondemandBytes := mustReadTestdata(t, "webpack_ondemand.js")
	// Strip the 3 optional profile tab definitions from main.js.
	rawMain := string(mustReadTestdata(t, "webpack_main.js"))
	for _, op := range ProfileTabOperations() {
		rawMain = strings.ReplaceAll(rawMain, op, "RemovedOp_"+op)
	}

	uid := "1000000000000000001"
	userTweetsBody := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
		makeTweetEntry("tweet-1791257321273778100", makeTimelineTweet("1791257321273778100", uid, "example_user", "", "Core posts ok")) +
		`]}]}}}}}}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
		case "/responsive-web/client-web/main.synth01a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(rawMain))
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
		default:
			if strings.HasSuffix(r.URL.Path, "/"+OpUserTweets) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(userTweetsBody))
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	fixedNow := time.Unix(1795327200, 0).UTC()
	var breakerCalls atomic.Int32
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
	client.SetAutomaticRequestHandlers(nil, func(source string, err error) {
		breakerCalls.Add(1)
	})

	// Core UserTweets MUST succeed even though optional profile-tab ops are absent.
	page, err := client.GetUserTweets(context.Background(), UserTweetsRequest{RestID: uid})
	if err != nil {
		t.Fatalf("core UserTweets should not be blocked by missing optional profile tab ops: %v", err)
	}
	if len(page.Posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(page.Posts))
	}

	// Missing optional profile tab returns schema_changed WITHOUT tripping site-wide circuit breaker.
	autoCtx := site.WithAutomaticRequests(context.Background(), "test_optional_tab")
	for _, tab := range []string{ProfileTabReplies, ProfileTabMedia, ProfileTabHighlights} {
		_, err := client.GetUserTabTweets(autoCtx, UserTweetsRequest{RestID: uid, Tab: tab})
		if !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("tab %s err = %v, want schema_changed", tab, err)
		}
		if breakerCalls.Load() != 0 {
			t.Fatalf("missing optional tab %s must not trip site-wide circuit breaker, got %d calls", tab, breakerCalls.Load())
		}
	}
}

func makeQuotedReplyTweet(id, uid, handle, replyTo, text, quoteID, quoteUID, quoteHandle, quoteText string) string {
	return `{"__typename":"Tweet","rest_id":"` + id + `","core":{"user_results":{"result":` +
		makeUserCoreJSON(uid, handle, "Creator "+handle, "https://pbs.twimg.com/profile_images/1/u_normal.jpg") +
		`}},"quoted_status_result":{"result":` +
		makeTimelineTweet(quoteID, quoteUID, quoteHandle, "", quoteText) +
		`},"legacy":{"id_str":"` + id + `","user_id_str":"` + uid + `","created_at":"Fri May 17 00:12:00 +0000 2024","in_reply_to_status_id_str":"` + replyTo + `","is_quote_status":true,"quoted_status_id_str":"` + quoteID + `","full_text":"` + text + `"}}`
}

func makeMediaTimelineTweet(id, uid, handle, replyTo, text, mediaKey string) string {
	return `{"__typename":"Tweet","rest_id":"` + id + `","core":{"user_results":{"result":` +
		makeUserCoreJSON(uid, handle, "Creator "+handle, "https://pbs.twimg.com/profile_images/1/u_normal.jpg") +
		`}},"legacy":{"id_str":"` + id + `","user_id_str":"` + uid + `","created_at":"Fri May 17 00:15:00 +0000 2024","in_reply_to_status_id_str":"` + replyTo + `","full_text":"` + text + `","extended_entities":{"media":[{"id_str":"` + mediaKey + `","type":"photo","media_url_https":"https://pbs.twimg.com/media/` + mediaKey + `.jpg","original_info":{"width":1200,"height":800}}]}}}`
}

func makeRetweetTimelineTweet(outerID, uid, handle, origID, origUID, origHandle, origText string) string {
	return `{"__typename":"Tweet","rest_id":"` + outerID + `","core":{"user_results":{"result":` +
		makeUserCoreJSON(uid, handle, "Creator "+handle, "https://pbs.twimg.com/profile_images/1/u_normal.jpg") +
		`}},"legacy":{"id_str":"` + outerID + `","user_id_str":"` + uid + `","created_at":"Fri May 17 00:20:00 +0000 2024","full_text":"RT @` + origHandle + `: ` + origText + `","retweeted_status_result":{"result":` +
		makeTimelineTweet(origID, origUID, origHandle, "", origText) + `}}}`
}

func TestUserTweetsAndRepliesConversationFilteringWhoToAndPagination(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_REP_01; ct0=SynthRepCT0")
	uid := "1000000000000000001"
	otherUID := "1000000000000000088"
	quotedUID := "1000000000000000099"

	foreignParentID := "1791257321273778100"
	replyID := "1791257321273778101"
	replyWithQuoteID := "1791257321273778102"
	quotedID := "1791257321273778050"
	retweetID := "1791257321273778103"
	origID := "1791257321273778040"
	page2ReplyID := "1791257321273778099"

	page1JSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[` +
		`{"type":"TimelineAddEntries","entries":[` +
		// VerticalConversation module: foreign parent + target user's reply + target user's reply-with-quote.
		`{"entryId":"profile-conversation-1","content":{"entryType":"TimelineTimelineModule","displayType":"VerticalConversation","items":[` +
		`{"entryId":"profile-conversation-1-tweet-` + foreignParentID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeTimelineTweet(foreignParentID, otherUID, "other_user", "", "Foreign parent tweet MUST NOT leak") + `}}}},` +
		`{"entryId":"profile-conversation-1-tweet-` + replyID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeTimelineTweet(replyID, uid, "target_user", foreignParentID, "Target user reply") + `}}}},` +
		`{"entryId":"profile-conversation-1-tweet-` + replyWithQuoteID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeQuotedReplyTweet(replyWithQuoteID, uid, "target_user", foreignParentID, "Target reply quoting another post", quotedID, quotedUID, "quoted_author", "Quoted content") + `}}}}` +
		`]}},` +
		// Retweet by target user (outer author matches targetUID, inner author is otherUID) MUST be kept!
		makeTweetEntry("tweet-"+retweetID, makeRetweetTimelineTweet(retweetID, uid, "target_user", origID, otherUID, "other_user", "Retweeted original")) + `,` +
		// DEC-077 verified who-to-* recommendation card (displayType: "Vertical") MUST be filtered out!
		`{"entryId":"who-to-connect-1","content":{"entryType":"TimelineTimelineModule","displayType":"Vertical","items":[` +
		`{"entryId":"who-to-connect-1-tweet-1791257321273778999","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeTimelineTweet("1791257321273778999", uid, "target_user", "", "Noise inside who-to module") + `}}}}` +
		`]}},` +
		// Promoted entry MUST be filtered out!
		`{"entryId":"promoted-tweet-1","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","promotedMetadata":{"impressionId":"ad1"},"tweet_results":{"result":` + makeTimelineTweet("1791257321273778998", uid, "target_user", "", "Promoted noise") + `}}}},` +
		`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"REP_CURSOR_2"}}` +
		`]}]}}}}}}`

	page2JSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[` +
		`{"type":"TimelineAddEntries","entries":[` +
		// Overlap with page 1 MUST be deduped.
		makeTweetEntry("tweet-"+replyID, makeTimelineTweet(replyID, uid, "target_user", foreignParentID, "Target user reply")) + `,` +
		makeTweetEntry("tweet-"+page2ReplyID, makeTimelineTweet(page2ReplyID, uid, "target_user", foreignParentID, "Page 2 target reply")) + `,` +
		`{"entryId":"cursor-bottom-2","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"REP_CURSOR_2"}}` +
		`]}]}}}}}}`

	var sawOp string
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+OpUserTweetsAndReplies) {
			sawOp = OpUserTweetsAndReplies
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rate-Limit-Limit", "500")
		w.Header().Set("X-Rate-Limit-Remaining", "499")
		if strings.Contains(r.URL.RawQuery, "REP_CURSOR_2") {
			_, _ = w.Write([]byte(page2JSON))
			return
		}
		_, _ = w.Write([]byte(page1JSON))
	}, store, nil)

	seen := make(map[string]bool)
	p1, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID:  uid,
		Tab:     ProfileTabReplies,
		SeenIDs: seen,
	})
	if err != nil {
		t.Fatalf("page 1 replies failed: %v", err)
	}
	if sawOp != OpUserTweetsAndReplies {
		t.Fatalf("called op = %q, want %q", sawOp, OpUserTweetsAndReplies)
	}
	if p1.Tab != ProfileTabReplies {
		t.Fatalf("page 1 tab = %q, want %q", p1.Tab, ProfileTabReplies)
	}
	if len(p1.Posts) != 3 {
		t.Fatalf("page 1 len = %d, want 3 (reply, reply-with-quote, retweet): %+v", len(p1.Posts), p1.Posts)
	}
	if p1.Posts[0].ID != replyID || p1.Posts[0].InReplyToStatusID != foreignParentID {
		t.Fatalf("post[0] = (id=%q replyTo=%q), want target reply", p1.Posts[0].ID, p1.Posts[0].InReplyToStatusID)
	}
	if p1.Posts[1].ID != replyWithQuoteID || p1.Posts[1].InReplyToStatusID != foreignParentID || !p1.Posts[1].IsQuote || p1.Posts[1].Quoted == nil || p1.Posts[1].Quoted.ID != quotedID {
		t.Fatalf("post[1] should keep reply-with-quote intact: %+v", p1.Posts[1])
	}
	if p1.Posts[2].ID != retweetID || !p1.Posts[2].IsRetweet {
		t.Fatalf("post[2] should keep target user retweet: %+v", p1.Posts[2])
	}
	for _, p := range p1.Posts {
		if p.ID == foreignParentID || p.Author.RestID != uid {
			t.Fatalf("foreign tweet leaked into replies tab: %+v", p)
		}
	}
	if !p1.HasMore || p1.NextCursor != "REP_CURSOR_2" {
		t.Fatalf("page 1 cursor = (%v, %q)", p1.HasMore, p1.NextCursor)
	}

	p2, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID:  uid,
		Tab:     ProfileTabReplies,
		Cursor:  p1.NextCursor,
		SeenIDs: seen,
	})
	if err != nil {
		t.Fatalf("page 2 replies failed: %v", err)
	}
	if len(p2.Posts) != 1 || p2.Posts[0].ID != page2ReplyID {
		t.Fatalf("page 2 posts = %+v, want [%s]", p2.Posts, page2ReplyID)
	}
	if p2.HasMore || !p2.CursorStagnated {
		t.Fatalf("page 2 expected cursor stagnation stop: hasMore=%v stagnated=%v", p2.HasMore, p2.CursorStagnated)
	}
}

func TestUserMediaVerticalGridAddToModuleAndCleanEmpty(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_MED_01; ct0=SynthMedCT0")
	uid := "1000000000000000001"
	otherUID := "1000000000000000088"

	gridID1 := "1791257321273778201"
	gridReplyMediaID := "1791257321273778202"
	regularMediaID := "1791257321273778203"
	foreignMediaID := "1791257321273778299"
	page2MediaID := "1791257321273778198"

	page1JSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[` +
		// DEC-077 observed Top-only TimelineTerminateTimeline alongside valid Bottom cursor.
		`{"type":"TimelineTerminateTimeline","direction":"Top"},` +
		`{"type":"TimelineAddEntries","entries":[` +
		`{"entryId":"profile-grid-0","content":{"entryType":"TimelineTimelineModule","displayType":"VerticalGrid","items":[` +
		`{"entryId":"profile-grid-0-tweet-` + gridID1 + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeMediaTimelineTweet(gridID1, uid, "target_user", "", "Grid photo 1", "SynthGrid01") + `}}}},` +
		`{"entryId":"profile-grid-0-tweet-` + gridReplyMediaID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeMediaTimelineTweet(gridReplyMediaID, uid, "target_user", "1791257321273778001", "Reply with media", "SynthGrid02") + `}}}},` +
		`{"entryId":"profile-grid-0-tweet-` + foreignMediaID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeMediaTimelineTweet(foreignMediaID, otherUID, "other_user", "", "Foreign media MUST drop", "SynthGrid99") + `}}}}` +
		`]}},` +
		makeTweetEntry("tweet-"+regularMediaID, makeMediaTimelineTweet(regularMediaID, uid, "target_user", "", "Standalone media tweet", "SynthGrid03")) + `,` +
		`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"MEDIA_CURSOR_2"}}` +
		`]}]}}}}}}`

	page2JSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[` +
		`{"type":"TimelineAddToModule","moduleEntryId":"profile-grid-0","moduleItems":[` +
		`{"entryId":"profile-grid-0-tweet-` + gridID1 + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeMediaTimelineTweet(gridID1, uid, "target_user", "", "Overlap photo 1", "SynthGrid01") + `}}}},` +
		`{"entryId":"profile-grid-0-tweet-` + page2MediaID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeMediaTimelineTweet(page2MediaID, uid, "target_user", "", "Grid page 2 photo", "SynthGrid04") + `}}}}` +
		`]},` +
		`{"type":"TimelineAddEntries","entries":[` +
		`{"entryId":"cursor-bottom-2","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"MEDIA_CURSOR_3"}}` +
		`]}]}}}}}}`

	emptyJSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[]}]}}}}}}`

	var mode atomic.Int32 // 0 = normal, 1 = clean empty, 2 = fake empty shell
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+OpUserMedia) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rate-Limit-Limit", "500")
		w.Header().Set("X-Rate-Limit-Remaining", "498")
		switch mode.Load() {
		case 1:
			_, _ = w.Write([]byte(emptyJSON))
		case 2:
			_, _ = w.Write([]byte(`{"data":{"user":{}}}`))
		default:
			if strings.Contains(r.URL.RawQuery, "MEDIA_CURSOR_2") {
				_, _ = w.Write([]byte(page2JSON))
				return
			}
			_, _ = w.Write([]byte(page1JSON))
		}
	}, store, nil)

	seen := make(map[string]bool)
	p1, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID:  uid,
		Tab:     ProfileTabMedia,
		SeenIDs: seen,
	})
	if err != nil {
		t.Fatalf("UserMedia page 1 failed: %v", err)
	}
	if len(p1.Posts) != 3 {
		t.Fatalf("UserMedia page 1 len = %d, want 3 (2 target grid items + 1 regular tweet, foreign dropped): %+v", len(p1.Posts), p1.Posts)
	}
	if p1.Posts[0].ID != gridID1 || p1.Posts[1].ID != gridReplyMediaID || p1.Posts[1].InReplyToStatusID == "" || p1.Posts[2].ID != regularMediaID {
		t.Fatalf("UserMedia page 1 posts = %+v", p1.Posts)
	}
	if !p1.HasMore || p1.NextCursor != "MEDIA_CURSOR_2" {
		t.Fatalf("UserMedia page 1 pagination must survive Top-only TimelineTerminateTimeline: hasMore=%v cursor=%q", p1.HasMore, p1.NextCursor)
	}

	p2, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID:  uid,
		Tab:     ProfileTabMedia,
		Cursor:  p1.NextCursor,
		SeenIDs: seen,
	})
	if err != nil {
		t.Fatalf("UserMedia page 2 (TimelineAddToModule) failed: %v", err)
	}
	if len(p2.Posts) != 1 || p2.Posts[0].ID != page2MediaID {
		t.Fatalf("UserMedia page 2 deduped posts = %+v, want [%s]", p2.Posts, page2MediaID)
	}
	if !p2.HasMore || p2.NextCursor != "MEDIA_CURSOR_3" {
		t.Fatalf("UserMedia page 2 pagination = (%v, %q)", p2.HasMore, p2.NextCursor)
	}

	// Clean empty page 1 on UserMedia must NOT falsely become UncertainEmpty even when StatusesCount > 0.
	mode.Store(1)
	emptyPage, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID:       uid,
		Tab:          ProfileTabMedia,
		KnownProfile: &UserProfile{RestID: uid, StatusesCount: 500},
	})
	if err != nil {
		t.Fatalf("UserMedia clean empty failed: %v", err)
	}
	if len(emptyPage.Posts) != 0 || emptyPage.UncertainEmpty || emptyPage.HasMore {
		t.Fatalf("UserMedia clean empty = %+v, want clean empty without UncertainEmpty", emptyPage)
	}

	// Fake empty shell MUST still fail with rate_limited.
	mode.Store(2)
	if _, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{RestID: uid, Tab: ProfileTabMedia}); !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("UserMedia fake shell err = %v, want rate_limited", err)
	}
}

func TestUserHighlightsTweetsTight50Per15MinBucketAndCleanEmpty(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_HL_01; ct0=SynthHLCT0")
	uid := "1000000000000000001"
	hlID1 := "1791257321273778301"
	fixedNow := time.Unix(1795327200, 0).UTC()
	resetAt := fixedNow.Add(12 * time.Minute)

	var hlHits atomic.Int32
	var mediaHits atomic.Int32
	var emptyHighlights atomic.Bool

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+OpUserHighlightsTweets):
			hlHits.Add(1)
			w.Header().Set("X-Rate-Limit-Limit", "50")
			if emptyHighlights.Load() {
				w.Header().Set("X-Rate-Limit-Remaining", "49")
				_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[]}]}}}}}}`))
				return
			}
			// Remaining == 1 on 50/15min tight bucket triggers proactive brake until resetAt.
			w.Header().Set("X-Rate-Limit-Remaining", "1")
			w.Header().Set("X-Rate-Limit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				makeTweetEntry("tweet-"+hlID1, makeTimelineTweet(hlID1, uid, "target_user", "", "Highlighted post 1")) + `,` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"HL_CURSOR_2"}}` +
				`]}]}}}}}}`))
		case strings.HasSuffix(r.URL.Path, "/"+OpUserMedia):
			mediaHits.Add(1)
			w.Header().Set("X-Rate-Limit-Limit", "500")
			w.Header().Set("X-Rate-Limit-Remaining", "499")
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[]}]}}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}, store, func(o *ClientOptions) {
		o.Now = func() time.Time { return fixedNow }
	})

	// Clean empty first (doesn't exhaust bucket).
	emptyHighlights.Store(true)
	emptyPg, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID:       uid,
		Tab:          ProfileTabHighlights,
		KnownProfile: &UserProfile{RestID: uid, StatusesCount: 1200},
	})
	if err != nil || len(emptyPg.Posts) != 0 || emptyPg.UncertainEmpty {
		t.Fatalf("clean empty highlights = (%+v, %v)", emptyPg, err)
	}

	// Next call returns 1 highlight and remaining=1 on the 50/15min tight bucket.
	emptyHighlights.Store(false)
	p1, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{RestID: uid, Tab: ProfileTabHighlights})
	if err != nil {
		t.Fatalf("highlights page 1 failed: %v", err)
	}
	if len(p1.Posts) != 1 || p1.Posts[0].ID != hlID1 || !p1.HasMore || p1.NextCursor != "HL_CURSOR_2" {
		t.Fatalf("highlights page 1 = %+v", p1)
	}
	hitsBeforeBrake := hlHits.Load()

	// Subsequent UserHighlightsTweets call MUST be braked locally with 0 additional HTTP requests!
	_, err = client.GetUserTabTweets(context.Background(), UserTweetsRequest{
		RestID: uid,
		Tab:    ProfileTabHighlights,
		Cursor: p1.NextCursor,
	})
	if !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("braked highlights err = %v, want rate_limited", err)
	}
	if hlHits.Load() != hitsBeforeBrake {
		t.Fatalf("proactive brake leaked HTTP request: before=%d after=%d", hitsBeforeBrake, hlHits.Load())
	}

	// UserMedia (separate 500/15min bucket) remains unaffected by UserHighlightsTweets brake!
	if _, err := client.GetUserTabTweets(context.Background(), UserTweetsRequest{RestID: uid, Tab: ProfileTabMedia}); err != nil {
		t.Fatalf("UserMedia should remain unblocked by UserHighlightsTweets brake: %v", err)
	}
	if mediaHits.Load() != 1 {
		t.Fatalf("mediaHits = %d, want 1", mediaHits.Load())
	}
}

func TestResolveLinkPreservesProfileTabsAcrossDomainsAndMirrors(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_RES_TAB; ct0=SynthResTabCT0")
	uid := "1000000000000000001"
	var screenCalls atomic.Int32

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName) {
			screenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload(uid, "synth_creator", "Synth Creator", "")))
			return
		}
		http.NotFound(w, r)
	}, store, nil)

	cases := []struct {
		input    string
		wantKind string
		wantID   string
		wantTab  string
		zeroAPI  bool
	}{
		{"https://x.com/synth_creator", "user", uid, "", false},
		{"https://x.com/synth_creator/with_replies", "user", uid, ProfileTabReplies, false},
		{"https://x.com/synth_creator/media", "user", uid, ProfileTabMedia, false},
		{"https://x.com/synth_creator/highlights", "user", uid, ProfileTabHighlights, false},
		{"https://twitter.com/synth_creator/MEDIA?s=20", "user", uid, ProfileTabMedia, false},
		{"https://fxtwitter.com/synth_creator/with_replies", "user", uid, ProfileTabReplies, false},
		{"https://xcancel.com/synth_creator/media", "user", uid, ProfileTabMedia, false},
		{"https://nitter.net/synth_creator/highlights", "user", uid, ProfileTabHighlights, false},
		{"https://x.com/i/user/" + uid, "user", uid, "", true},
		{"https://x.com/i/user/" + uid + "/with_replies", "user", uid, ProfileTabReplies, true},
		{"https://x.com/i/user/" + uid + "/media", "user", uid, ProfileTabMedia, true},
		{"https://x.com/i/user/" + uid + "/highlights", "user", uid, ProfileTabHighlights, true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			before := screenCalls.Load()
			got, err := client.ResolveLink(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("ResolveLink(%q) error: %v", tc.input, err)
			}
			if got.Kind != tc.wantKind || got.ID != tc.wantID || got.Tab != tc.wantTab {
				t.Fatalf("ResolveLink(%q) = (kind=%q id=%q tab=%q), want (%q, %q, %q)",
					tc.input, got.Kind, got.ID, got.Tab, tc.wantKind, tc.wantID, tc.wantTab)
			}
			if tc.zeroAPI && screenCalls.Load() != before {
				t.Fatalf("expected 0 API calls for %q", tc.input)
			}
		})
	}
}

func TestSubscriptionAdapterRedLineNeverPollsProfileTabOps(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_SUB_01; ct0=SynthSubCT0")
	uid := "1000000000000000001"

	var mu sync.Mutex
	calledOps := map[string]int{}
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		op := parts[len(parts)-1]
		mu.Lock()
		calledOps[op]++
		mu.Unlock()
		if op != OpUserTweets {
			t.Errorf("subscription adapter must NEVER call %s", op)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
			makeTweetEntry("tweet-1791257321273778401", makeTimelineTweet("1791257321273778401", uid, "target_user", "", "Sub check post")) +
			`]}]}}}}}}`))
	}, store, nil)

	adapter := NewAdapter(client)
	pg, err := adapter.FetchCreatorPosts(context.Background(), uid, "")
	if err != nil {
		t.Fatalf("FetchCreatorPosts failed: %v", err)
	}
	if len(pg.Posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(pg.Posts))
	}
	mu.Lock()
	defer mu.Unlock()
	if calledOps[OpUserTweets] != 1 {
		t.Fatalf("UserTweets calls = %d, want 1", calledOps[OpUserTweets])
	}
	for _, forbidden := range ProfileTabOperations() {
		if calledOps[forbidden] != 0 {
			t.Fatalf("subscription FetchCreatorPosts must not call %s, got %d", forbidden, calledOps[forbidden])
		}
	}
}
