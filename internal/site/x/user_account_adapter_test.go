package x

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func makeTimelineTweet(id, uid, handle, replyTo, text string) string {
	return `{"__typename":"Tweet","rest_id":"` + id + `","core":{"user_results":{"result":` +
		makeUserCoreJSON(uid, handle, "Creator "+handle, "https://pbs.twimg.com/profile_images/1/u_normal.jpg") +
		`}},"legacy":{"id_str":"` + id + `","user_id_str":"` + uid + `","created_at":"Fri May 17 00:10:00 +0000 2024","in_reply_to_status_id_str":"` + replyTo + `","full_text":"` + text + `"}}`
}

func TestUserProfileCoreAndLegacyPathsAndUnavailable(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_USER_01; ct0=SynthCT0User0001")

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.RawQuery
		switch {
		case strings.Contains(q, "1000000000000000001"):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":` + makeUserCoreJSON("1000000000000000001", "core_user", "Core Name", "https://pbs.twimg.com/profile_images/1/c_normal.jpg") + `}}}`))
		case strings.Contains(q, "1000000000000000002"):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":` + makeUserLegacyJSON("1000000000000000002", "legacy_user", "Legacy Name", "https://pbs.twimg.com/profile_images/2/l_normal.jpg") + `}}}`))
		case strings.Contains(q, "1000000000000000003"):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"UserUnavailable","reason":"Suspended"}}}}`))
		case strings.Contains(q, "1000000000000000004"):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"UserUnavailable","reason":"Blocked"}}}}`))
		case strings.Contains(q, "1000000000000000005"):
			_, _ = w.Write([]byte(`{"data":{"user":{}}}`))
		default:
			http.NotFound(w, r)
		}
	}, store, nil)

	pCore, err := client.GetUserByRestId(context.Background(), "1000000000000000001")
	if err != nil || pCore.ScreenName != "core_user" || pCore.Name != "Core Name" || !pCore.IsBlueVerified || pCore.Description != "New core bio" {
		t.Fatalf("core profile = (%+v, %v)", pCore, err)
	}
	pLeg, err := client.GetUserByRestId(context.Background(), "1000000000000000002")
	if err != nil || pLeg.ScreenName != "legacy_user" || pLeg.Name != "Legacy Name" || !pLeg.Verified || pLeg.Description != "Legacy bio" {
		t.Fatalf("legacy profile = (%+v, %v)", pLeg, err)
	}
	if _, err := client.GetUserByRestId(context.Background(), "1000000000000000003"); !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("Suspended err = %v, want unavailable", err)
	}
	if _, err := client.GetUserByRestId(context.Background(), "1000000000000000004"); !site.IsKind(err, site.ErrKindForbidden) {
		t.Fatalf("Blocked err = %v, want forbidden", err)
	}
	if _, err := client.GetUserByRestId(context.Background(), "1000000000000000005"); !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("empty shell err = %v, want rate_limited", err)
	}
}

func TestUserTweetsThreePathsPinnedPaginationStagnationFilteringAndVisibleLimit(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_TL_01; ct0=SynthCT0TL0001")
	uid := "1000000000000000001"
	otherUID := "1000000000000000088"

	// Test all 3 timeline paths (timeline_v2.timeline, timeline.timeline, timeline.timeline_v2).
	t.Run("all 3 timeline response paths", func(t *testing.T) {
		tweet := makeTimelineTweet("1791257321273778100", uid, "example_user", "", "Hello path")
		instrs := `[{"type":"TimelineAddEntries","entries":[` + makeTweetEntry("tweet-1791257321273778100", tweet) + `]}]`
		paths := []struct {
			name string
			body string
		}{
			{"result.timeline_v2.timeline", `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline_v2":{"timeline":{"instructions":` + instrs + `}}}}}}`},
			{"result.timeline.timeline", `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline":{"instructions":` + instrs + `}}}}}}`},
			{"result.timeline.timeline_v2", `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline":{"timeline_v2":{"instructions":` + instrs + `}}}}}}`},
		}
		for _, tc := range paths {
			t.Run(tc.name, func(t *testing.T) {
				_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
				}, store, nil)

				page, err := client.GetUserTweets(context.Background(), UserTweetsRequest{RestID: uid})
				if err != nil {
					t.Fatalf("GetUserTweets(%s) failed: %v", tc.name, err)
				}
				if len(page.Posts) != 1 || page.Posts[0].ID != "1791257321273778100" {
					t.Fatalf("posts = %+v", page.Posts)
				}
			})
		}
	})

	// Test pinned first, promoted/recommendation/foreign/reply filtering, page 1 -> page 2 dedup,
	// cursor stagnation stop, and visible limit flag!
	t.Run("pinned first, filtering, pagination dedup, cursor stagnation and visible limit", func(t *testing.T) {
		pinID := "1791257321273778001"
		p1ID := "1791257321273778102"
		p2ID := "1791257321273778101"
		replyID := "1791257321273778103"
		promoID := "1791257321273778104"
		foreignID := "1791257321273778105"

		page1JSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline_v2":{"timeline":{"instructions":[` +
			`{"type":"TimelinePinEntry","entry":` + makeTweetEntry("tweet-"+pinID, makeTimelineTweet(pinID, uid, "example_user", "", "Pinned post")) + `},` +
			`{"type":"TimelineAddEntries","entries":[` +
			// Duplicate of pinned in TimelineAddEntries must be deduped.
			makeTweetEntry("tweet-"+pinID, makeTimelineTweet(pinID, uid, "example_user", "", "Pinned post")) + `,` +
			makeTweetEntry("tweet-"+p1ID, makeTimelineTweet(p1ID, uid, "example_user", "", "Regular post 1")) + `,` +
			// Reply to someone else MUST be filtered out!
			makeTweetEntry("tweet-"+replyID, makeTimelineTweet(replyID, uid, "example_user", "1791257321273770001", "Reply to other")) + `,` +
			// Promoted entry by entryId prefix AND promotedMetadata MUST be filtered out!
			`{"entryId":"promoted-tweet-1","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","promotedMetadata":{"impressionId":"ad1"},"tweet_results":{"result":` + makeTimelineTweet(promoID, uid, "example_user", "", "Ad") + `}}}},` +
			// Who-to-follow recommendation module MUST be filtered out!
			`{"entryId":"who-to-follow-1","content":{"entryType":"TimelineTimelineModule","displayType":"VerticalWithContextLine","items":[{"entryId":"who-to-follow-1-user-1","item":{"itemContent":{"itemType":"TimelineUser","user_results":{"result":` + makeUserCoreJSON(otherUID, "rec_user", "Rec", "") + `}}}}]}},` +
			// Conversation module containing foreign author entry (filtered) and target author root entry.
			`{"entryId":"profile-conversation-1","content":{"entryType":"TimelineTimelineModule","items":[` +
			`{"entryId":"profile-conversation-1-tweet-` + foreignID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeTimelineTweet(foreignID, otherUID, "other_user", "", "Foreign post") + `}}}}` +
			`]}},` +
			`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"CURSOR_PAGE_2"}}` +
			`]}]}}}}}}`

		page2JSON := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline_v2":{"timeline":{"instructions":[` +
			`{"type":"TimelineAddEntries","entries":[` +
			// Overlap p1ID from page 1 must be deduped via SeenIDs.
			makeTweetEntry("tweet-"+p1ID, makeTimelineTweet(p1ID, uid, "example_user", "", "Regular post 1")) + `,` +
			makeTweetEntry("tweet-"+p2ID, makeTimelineTweet(p2ID, uid, "example_user", "", "Regular post 2")) + `,` +
			// Stagnated cursor (same CURSOR_PAGE_2 repeated) -> MUST stop immediately!
			`{"entryId":"cursor-bottom-2","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"CURSOR_PAGE_2"}}` +
			`]}]}}}}}}`

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.RawQuery, "CURSOR_PAGE_2") {
				_, _ = w.Write([]byte(page2JSON))
				return
			}
			_, _ = w.Write([]byte(page1JSON))
		}, store, nil)

		seen := make(map[string]bool)
		knownProf := &UserProfile{RestID: uid, StatusesCount: 5000}
		p1, err := client.GetUserTweets(context.Background(), UserTweetsRequest{
			RestID:       uid,
			SeenIDs:      seen,
			KnownProfile: knownProf,
		})
		if err != nil {
			t.Fatalf("page 1 failed: %v", err)
		}
		if len(p1.Posts) != 2 || p1.Posts[0].ID != pinID || !p1.Posts[0].Pinned || p1.Posts[1].ID != p1ID || p1.Posts[1].Pinned {
			t.Fatalf("page 1 posts = %+v, want [pinned=%s, regular=%s]", p1.Posts, pinID, p1ID)
		}
		if !p1.HasMore || p1.NextCursor != "CURSOR_PAGE_2" {
			t.Fatalf("page 1 pagination = (hasMore=%v cursor=%q)", p1.HasMore, p1.NextCursor)
		}

		p2, err := client.GetUserTweets(context.Background(), UserTweetsRequest{
			RestID:       uid,
			Cursor:       p1.NextCursor,
			SeenIDs:      seen,
			KnownProfile: knownProf,
			TotalFetched: len(p1.Posts),
		})
		if err != nil {
			t.Fatalf("page 2 failed: %v", err)
		}
		if len(p2.Posts) != 1 || p2.Posts[0].ID != p2ID {
			t.Fatalf("page 2 deduped posts = %+v, want [%s]", p2.Posts, p2ID)
		}
		if p2.HasMore || !p2.CursorStagnated || !p2.ReachedVisibleLimit || !p2.BottomTipsVisible || p2.BottomTipsText != VisibleTimelineLimitText {
			t.Fatalf("page 2 stagnation/visible limit flags = (hasMore=%v stag=%v visLim=%v tip=%q)",
				p2.HasMore, p2.CursorStagnated, p2.ReachedVisibleLimit, p2.BottomTipsText)
		}
	})
}

func TestUserTweetsTruthTableEveryRow(t *testing.T) {
	uid := "1000000000000000001"
	emptyInstrs := `[{"type":"TimelineAddEntries","entries":[]}]`

	cases := []struct {
		name             string
		status           int
		body             string
		knownProf        *UserProfile
		wantErrKind      string
		wantUncertain    bool
		wantCircuitBreak bool
	}{
		{
			name:          "row 1a genuine 0 posts statuses_count == 0",
			status:        200,
			body:          `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","legacy":{"statuses_count":0,"protected":false},"timeline_v2":{"timeline":{"instructions":` + emptyInstrs + `}}}}}}`,
			knownProf:     &UserProfile{RestID: uid, StatusesCount: 0},
			wantErrKind:   "",
			wantUncertain: false,
		},
		{
			name:          "row 1b empty page 1 with statuses_count > 0 -> UncertainEmpty (baseline must not move)",
			status:        200,
			body:          `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","legacy":{"statuses_count":150,"protected":false},"timeline_v2":{"timeline":{"instructions":` + emptyInstrs + `}}}}}}`,
			knownProf:     &UserProfile{RestID: uid, StatusesCount: 150},
			wantErrKind:   "",
			wantUncertain: true,
		},
		{
			name:             "row 2a fake empty shell {data:{user:{}}} -> rate_limited NOT 0 posts",
			status:           200,
			body:             `{"data":{"user":{}}}`,
			wantErrKind:      site.ErrKindRateLimited,
			wantCircuitBreak: true,
		},
		{
			name:             "row 2b missing timeline object on non-protected user -> rate_limited",
			status:           200,
			body:             `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","legacy":{"protected":false}}}}}`,
			wantErrKind:      site.ErrKindRateLimited,
			wantCircuitBreak: true,
		},
		{
			name:             "row 3a data.user == null -> unavailable single target",
			status:           200,
			body:             `{"data":{"user":null}}`,
			wantErrKind:      site.ErrKindUnavailable,
			wantCircuitBreak: false,
		},
		{
			name:             "row 3b user.result == null -> unavailable single target",
			status:           200,
			body:             `{"data":{"user":{"result":null}}}`,
			wantErrKind:      site.ErrKindUnavailable,
			wantCircuitBreak: false,
		},
		{
			name:             "row 4a UserUnavailable Suspended -> unavailable no circuit break",
			status:           200,
			body:             `{"data":{"user":{"result":{"__typename":"UserUnavailable","reason":"Suspended"}}}}`,
			wantErrKind:      site.ErrKindUnavailable,
			wantCircuitBreak: false,
		},
		{
			name:             "row 4b UserUnavailable Blocked -> forbidden no circuit break",
			status:           200,
			body:             `{"data":{"user":{"result":{"__typename":"UserUnavailable","reason":"Blocked"}}}}`,
			wantErrKind:      site.ErrKindForbidden,
			wantCircuitBreak: false,
		},
		{
			name:             "row 5 protected true without accessible timeline -> forbidden no circuit break",
			status:           200,
			body:             `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","legacy":{"protected":true,"statuses_count":50}}}}}`,
			wantErrKind:      site.ErrKindForbidden,
			wantCircuitBreak: false,
		},
		{
			name:             "row 6 top-level errors[] non-empty -> mapped via §10.1",
			status:           200,
			body:             `{"data":{"user":{"result":null}},"errors":[{"code":88,"message":"Rate limit exceeded"}]}`,
			wantErrKind:      site.ErrKindRiskControl,
			wantCircuitBreak: true,
		},
		{
			name:             "row 7 HTTP 403 code 326 risk control stops cleanly without clearing cred",
			status:           403,
			body:             `{"errors":[{"code":326,"message":"Account temporarily locked"}]}`,
			wantErrKind:      site.ErrKindRiskControl,
			wantCircuitBreak: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestXStore(t, "auth_token=SYNTH_TT; ct0=SynthCT0TT0001")
			_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}, store, nil)

			page, err := client.GetUserTweets(context.Background(), UserTweetsRequest{
				RestID:       uid,
				KnownProfile: tc.knownProf,
			})
			if tc.wantErrKind != "" {
				if !site.IsKind(err, tc.wantErrKind) {
					t.Fatalf("err = %v, want kind %q", err, tc.wantErrKind)
				}
				if site.IsCircuitBreak(err) != tc.wantCircuitBreak {
					t.Fatalf("IsCircuitBreak = %v, want %v", site.IsCircuitBreak(err), tc.wantCircuitBreak)
				}
				if !store.HasActiveCredential() {
					t.Fatalf("credential must stay intact for %s", tc.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(page.Posts) != 0 || page.HasMore || page.UncertainEmpty != tc.wantUncertain {
				t.Fatalf("page = (posts=%d hasMore=%v uncertain=%v), want uncertain=%v",
					len(page.Posts), page.HasMore, page.UncertainEmpty, tc.wantUncertain)
			}
		})
	}
}

func TestAccountFollowingBookmarksLikesAndFeatureFlags(t *testing.T) {
	// Verify default-off feature flags per x.md §15.1 #16.
	if BookmarkFoldersEnabled || ListsEnabled {
		t.Fatalf("BookmarkFoldersEnabled (%v) and ListsEnabled (%v) must default to false", BookmarkFoldersEnabled, ListsEnabled)
	}

	store := newTestXStore(t, "auth_token=SYNTH_ACC; ct0=SynthCT0Acc0001")
	selfID := "1000000000000000001"

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+OpFollowing):
			if strings.Contains(r.URL.RawQuery, "FOL_CUR_2") {
				_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
					`{"entryId":"user-1000000000000000010","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineUser","user_results":{"result":` + makeUserCoreJSON("1000000000000000010", "followed_one", "Followed One", "") + `}}}},` +
					`{"entryId":"user-1000000000000000011","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineUser","user_results":{"result":` + makeUserLegacyJSON("1000000000000000011", "followed_two", "Followed Two", "") + `}}}},` +
					`{"entryId":"cursor-bottom-2","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"0|99999"}}` +
					`]}]}}}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				`{"entryId":"user-1000000000000000010","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineUser","user_results":{"result":` + makeUserCoreJSON("1000000000000000010", "followed_one", "Followed One", "") + `}}}},` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"FOL_CUR_2"}}` +
				`]}]}}}}}}`))

		case strings.HasSuffix(r.URL.Path, "/"+OpBookmarks):
			_, _ = w.Write([]byte(`{"data":{"bookmark_timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				makeTweetEntry("tweet-1791257321273778201", makeTimelineTweet("1791257321273778201", "1000000000000000010", "followed_one", "", "Bookmarked post")) + `,` +
				`{"entryId":"promoted-tweet-bm","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","promotedMetadata":{"id":"ad"},"tweet_results":{"result":` + makeTimelineTweet("1791257321273778299", "1000000000000000010", "followed_one", "", "Promo") + `}}}},` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"BM_CUR_2"}}` +
				`]}]}}}}`))

		case strings.HasSuffix(r.URL.Path, "/"+OpLikes):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				makeTweetEntry("tweet-1791257321273778301", makeTimelineTweet("1791257321273778301", "1000000000000000011", "followed_two", "", "Liked post")) + `,` +
				`{"entryId":"cursor-bottom-1","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"0|end"}}` +
				`]}]}}}}}}`))

		default:
			http.NotFound(w, r)
		}
	}, store, nil)

	// 1. Following pagination + cross-page dedup + 0|... end cursor.
	seenUsers := make(map[string]bool)
	f1, err := client.ListFollowing(context.Background(), selfID, "", seenUsers)
	if err != nil || len(f1.Users) != 1 || !f1.HasMore || f1.NextCursor != "FOL_CUR_2" {
		t.Fatalf("Following page 1 = (%+v, %v)", f1, err)
	}
	f2, err := client.ListFollowing(context.Background(), selfID, f1.NextCursor, seenUsers)
	if err != nil || len(f2.Users) != 1 || f2.Users[0].RestID != "1000000000000000011" || f2.HasMore {
		t.Fatalf("Following page 2 = (%+v, %v)", f2, err)
	}

	// 2. Bookmarks (promoted stripped, cursor kept).
	bm, err := client.ListBookmarks(context.Background(), "", nil)
	if err != nil || len(bm.Posts) != 1 || bm.Posts[0].ID != "1791257321273778201" || !bm.HasMore || bm.NextCursor != "BM_CUR_2" {
		t.Fatalf("Bookmarks = (%+v, %v)", bm, err)
	}

	// 3. Likes (0|end terminates cleanly).
	lk, err := client.ListLikes(context.Background(), selfID, "", nil)
	if err != nil || len(lk.Posts) != 1 || lk.Posts[0].ID != "1791257321273778301" || lk.HasMore {
		t.Fatalf("Likes = (%+v, %v)", lk, err)
	}
}

func TestAdapterRegistryPlanDownloadAndSubscriptionSnapshotSanitization(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_ADAPT; ct0=SynthCT0Adapt0001")
	uid := "1000000000000000001"
	signedVideoSentinel := "SYNTH_VIDEO_SIG_999"

	var hits atomic.Int32
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+OpTweetDetail):
			id := "1791257321273778509"
			tweetJSON := `{"__typename":"Tweet","rest_id":"` + id + `","core":{"user_results":{"result":` +
				makeUserCoreJSON(uid, "example_user", "Example Creator", "https://pbs.twimg.com/profile_images/1/a_normal.jpg") +
				`}},"legacy":{"id_str":"` + id + `","user_id_str":"` + uid + `","created_at":"Fri May 17 00:00:00 +0000 2024","full_text":"Single MP4 video post","favorite_count":999,"retweet_count":88,"extended_entities":{"media":[{"type":"video","media_url_https":"https://pbs.twimg.com/ext_tw_video_thumb/1/cover.jpg","video_info":{"duration_millis":15000,"variants":[{"bitrate":2176000,"content_type":"video/mp4","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/v.mp4?sig=` + signedVideoSentinel + `"}]}}]}}}`
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+id, tweetJSON))))

		case strings.HasSuffix(r.URL.Path, "/"+OpUserTweets):
			id := "1791257321273778509"
			tweetJSON := `{"__typename":"Tweet","rest_id":"` + id + `","core":{"user_results":{"result":` +
				makeUserCoreJSON(uid, "example_user", "Example Creator", "https://pbs.twimg.com/profile_images/1/a_normal.jpg") +
				`}},"legacy":{"id_str":"` + id + `","user_id_str":"` + uid + `","created_at":"Fri May 17 00:00:00 +0000 2024","full_text":"Single MP4 video post","favorite_count":999,"retweet_count":88,"extended_entities":{"media":[{"type":"video","media_url_https":"https://pbs.twimg.com/ext_tw_video_thumb/1/cover.jpg","video_info":{"duration_millis":15000,"variants":[{"bitrate":2176000,"content_type":"video/mp4","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/v.mp4?sig=` + signedVideoSentinel + `"}]}}]}}}`
			body := `{"data":{"user":{"result":{"__typename":"User","rest_id":"` + uid + `","timeline_v2":{"timeline":{"instructions":[{"type":"TimelineAddEntries","entries":[` +
				makeTweetEntry("tweet-"+id, tweetJSON) + `]}]}}}}}}`
			_, _ = w.Write([]byte(body))

		default:
			http.NotFound(w, r)
		}
	}, store, nil)

	adapter := NewAdapter(client)

	// 1. Registry dispatches X URL cleanly through Adapter.
	reg := site.NewRegistry(adapter)
	target, err := reg.Resolve(context.Background(), "https://fxtwitter.com/example_user/status/1791257321273778509")
	if err != nil || target.Site != site.X || target.Kind != "dynamic" || target.ID != "1791257321273778509" {
		t.Fatalf("Registry.Resolve = (%+v, %v)", target, err)
	}

	// 2. PlanDownload builds single_mp4 plan with canonical x:dynamic:<id> ItemKey and cookieless MediaTransport.
	plan, err := adapter.PlanDownload(context.Background(), "1791257321273778509", 1)
	if err != nil {
		t.Fatalf("PlanDownload failed: %v", err)
	}
	if plan.Site != site.X || plan.ItemKey != "x:dynamic:1791257321273778509" || plan.Kind != "single_mp4" {
		t.Fatalf("plan identity = (site=%q key=%q kind=%q)", plan.Site, plan.ItemKey, plan.Kind)
	}
	if len(plan.VideoURLs) != 1 || !strings.Contains(plan.VideoURLs[0], "video.twimg.com") {
		t.Fatalf("plan VideoURLs = %v", plan.VideoURLs)
	}
	if plan.Media.Referer != "" || plan.Media.Origin != "" || plan.Media.PublicRedirects {
		t.Fatalf("X MediaTransport must have empty Referer/Origin and PublicRedirects=false: %+v", plan.Media)
	}
	// Signed video URL MUST NOT appear when marshaling DownloadPlan!
	planJSON, _ := json.Marshal(plan)
	if strings.Contains(string(planJSON), signedVideoSentinel) || strings.Contains(string(planJSON), "video.twimg.com") {
		t.Fatalf("DownloadPlan JSON leaked video CDN URL: %s", string(planJSON))
	}

	// 3. FetchCreatorPosts returns sanitized snapshot (NO stats, NO video.twimg.com, NO signed query tokens).
	subPage, err := adapter.FetchCreatorPosts(context.Background(), uid, "")
	if err != nil {
		t.Fatalf("FetchCreatorPosts failed: %v", err)
	}
	if len(subPage.Posts) != 1 || subPage.Posts[0].ID != "1791257321273778509" || subPage.Posts[0].Type != RuleTypeVideo {
		t.Fatalf("subPage posts = %+v", subPage.Posts)
	}
	snap := subPage.Posts[0].Snapshot
	for _, forbidden := range []string{signedVideoSentinel, "video.twimg.com", `"likeCount"`, `"retweetCount"`, `"stats"`} {
		if strings.Contains(snap, forbidden) {
			t.Fatalf("CreatorPost.Snapshot leaked forbidden field/URL %q: %s", forbidden, snap)
		}
	}
	if !strings.Contains(subPage.Posts[0].CoverURL, "pbs.twimg.com") {
		t.Fatalf("CoverURL should keep safe public pbs.twimg.com preview, got %q", subPage.Posts[0].CoverURL)
	}

	// 4. PlanDownloadFromPost on HLS-only video, mixed GIF+photo, and retweet with degraded origin.
	hlsPlan, err := adapter.PlanDownloadFromPost(&Post{
		ID:              "1791257321273778991",
		Author:          AuthorSummary{RestID: uid, ScreenName: "example_user", Name: "Example"},
		Text:            "HLS only post",
		RuleType:        RuleTypeVideo,
		HasHLSOnlyVideo: true,
		Media: []MediaItem{{
			Index:         1,
			Kind:          MediaKindVideo,
			ImageURL:      "https://pbs.twimg.com/ext_tw_video_thumb/1/cover?format=jpg&name=orig",
			HLSOnly:       true,
			HLSOnlyNotice: HLSOnlyNoticeText,
			HLSURL:        "https://video.twimg.com/ext_tw_video/1/pl/master.m3u8",
		}},
		CanonicalURL: "https://x.com/example_user/status/1791257321273778991",
	})
	if err != nil {
		t.Fatalf("PlanDownloadFromPost(HLS-only) failed: %v", err)
	}
	if hlsPlan.Kind != "image_note" || len(hlsPlan.VideoURLs) != 0 || len(hlsPlan.Images) != 1 || len(hlsPlan.Images[0].VideoURLs) != 0 {
		t.Fatalf("HLS-only plan must not expose m3u8 in VideoURLs: %+v", hlsPlan)
	}
	if hlsPlan.Images[0].MediaRole != "hls_only" || hlsPlan.Images[0].VideoCodec != "hls" {
		t.Fatalf("HLS-only asset must set MediaRole=hls_only and VideoCodec=hls, got role=%q codec=%q",
			hlsPlan.Images[0].MediaRole, hlsPlan.Images[0].VideoCodec)
	}

	// Mixed post with image + progressive MP4 + HLS-only video + GIF preserves exact role/codec per item.
	mixedPlan, err := adapter.PlanDownloadFromPost(&Post{
		ID:              "1791257321273778992",
		Author:          AuthorSummary{RestID: uid, ScreenName: "example_user", Name: "Example"},
		Text:            "Mixed media with HLS-only video",
		RuleType:        RuleTypeVideo,
		HasHLSOnlyVideo: true,
		Media: []MediaItem{
			{
				Index:    1,
				Kind:     MediaKindPhoto,
				ImageURL: "https://pbs.twimg.com/media/SynthPhoto01?format=jpg&name=orig",
			},
			{
				Index:        2,
				Kind:         MediaKindVideo,
				ImageURL:     "https://pbs.twimg.com/ext_tw_video_thumb/2/cover.jpg",
				VideoURL:     "https://video.twimg.com/ext_tw_video/2/pu/vid/1280x720/v.mp4",
				VideoBitrate: 2176000,
			},
			{
				Index:         3,
				Kind:          MediaKindVideo,
				ImageURL:      "https://pbs.twimg.com/ext_tw_video_thumb/3/cover.jpg",
				HLSOnly:       true,
				HLSOnlyNotice: HLSOnlyNoticeText,
				HLSURL:        "https://video.twimg.com/ext_tw_video/3/pl/master.m3u8",
			},
			{
				Index:    4,
				Kind:     MediaKindGIF,
				GIF:      true,
				ImageURL: "https://pbs.twimg.com/tweet_video_thumb/4/cover.jpg",
				VideoURL: "https://video.twimg.com/tweet_video/4/g.mp4",
			},
		},
		CanonicalURL: "https://x.com/example_user/status/1791257321273778992",
	})
	if err != nil {
		t.Fatalf("PlanDownloadFromPost(mixed HLS-only) failed: %v", err)
	}
	if mixedPlan.Kind != "image_note" || len(mixedPlan.Images) != 4 {
		t.Fatalf("mixed plan = (kind=%q len=%d), want image_note with 4 assets", mixedPlan.Kind, len(mixedPlan.Images))
	}
	wantRoles := []string{"image", "video", "hls_only", "gif"}
	wantCodecs := []string{"", "avc", "hls", "avc"}
	for i := range wantRoles {
		got := mixedPlan.Images[i]
		if got.MediaRole != wantRoles[i] || got.VideoCodec != wantCodecs[i] {
			t.Fatalf("mixedPlan.Images[%d] = (role=%q codec=%q), want (role=%q codec=%q)",
				i, got.MediaRole, got.VideoCodec, wantRoles[i], wantCodecs[i])
		}
	}
	if len(mixedPlan.Images[1].VideoURLs) != 1 || len(mixedPlan.Images[2].VideoURLs) != 0 || len(mixedPlan.Images[3].VideoURLs) != 1 {
		t.Fatalf("unexpected VideoURLs counts: mp4=%d hls=%d gif=%d",
			len(mixedPlan.Images[1].VideoURLs), len(mixedPlan.Images[2].VideoURLs), len(mixedPlan.Images[3].VideoURLs))
	}

	// 5. FetchImage strips Cookie/Referer/Origin and rejects HTML fake-200 responses.
	var sawImgLeak atomic.Bool
	imgSrv := NewClient(ClientOptions{
		CredStore:      store,
		MinInterval:    -1,
		AllowLocalhost: true,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Authorization") != "" {
				sawImgLeak.Store(true)
			}
			w := &http.Response{Header: make(http.Header)}
			if strings.Contains(r.URL.Path, "fakehtml") {
				w.StatusCode = http.StatusOK
				w.Header.Set("Content-Type", "image/jpeg")
				w.Body = ioNopCloserString("<!doctype html><html>blocked</html>")
				return w, nil
			}
			w.StatusCode = http.StatusOK
			w.Header.Set("Content-Type", "image/png")
			w.Body = ioNopCloserString("\x89PNG\r\n\x1a\nSYNTH")
			return w, nil
		}),
	})
	imgBytes, _, err := imgSrv.FetchImage(context.Background(), "https://pbs.twimg.com/media/SynthImg01?format=png&name=orig")
	if err != nil || len(imgBytes) == 0 || sawImgLeak.Load() {
		t.Fatalf("FetchImage failed or leaked headers: err=%v len=%d leak=%v", err, len(imgBytes), sawImgLeak.Load())
	}
	if _, _, err := imgSrv.FetchImage(context.Background(), "https://pbs.twimg.com/media/fakehtml.jpg"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("FetchImage fake HTML err = %v, want blocked", err)
	}
}

type stringReadCloser struct{ *strings.Reader }

func (stringReadCloser) Close() error { return nil }

func ioNopCloserString(s string) stringReadCloser { return stringReadCloser{strings.NewReader(s)} }
