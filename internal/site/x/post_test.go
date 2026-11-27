package x

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func wrapTweetDetailEntries(entriesJSON string) string {
	return `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"type":"TimelineAddEntries","entries":[` + entriesJSON + `]}]}}}`
}

func makeTweetEntry(entryID, tweetResultJSON string) string {
	return `{"entryId":"` + entryID + `","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweetDisplayType":"Tweet","tweet_results":{"result":` + tweetResultJSON + `}}}}`
}

func makeUserCoreJSON(restID, screenName, name, avatar string) string {
	return `{"__typename":"User","rest_id":"` + restID + `","is_blue_verified":true,"core":{"name":"` + name + `","screen_name":"` + screenName + `"},"avatar":{"image_url":"` + avatar + `"},"profile_bio":{"description":"New core bio"},"legacy":{"statuses_count":10,"followers_count":500,"friends_count":50}}`
}

func makeUserLegacyJSON(restID, screenName, name, avatar string) string {
	return `{"__typename":"User","rest_id":"` + restID + `","legacy":{"id_str":"` + restID + `","name":"` + name + `","screen_name":"` + screenName + `","profile_image_url_https":"` + avatar + `","description":"Legacy bio","verified":true,"statuses_count":20,"followers_count":800,"friends_count":60}}`
}

func TestPostUnwrapOrderPreviewDisplayTombstoneUnavailableAndVisibilityResults(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_POST_01; ct0=SynthCT0Post0001")

	t.Run("TweetPreviewDisplay parsed BEFORE TweetWithVisibilityResults without legacy", func(t *testing.T) {
		statusID := "1791257321273778509"
		tweetJSON := `{
			"__typename": "TweetPreviewDisplay",
			"tweet": {
				"rest_id": "` + statusID + `",
				"text": "Subscriber-only preview &amp; teaser",
				"created_at": "Fri May 17 00:00:00 +0000 2024",
				"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000001", "example_user", "Example Name", "https://pbs.twimg.com/profile_images/1/a_normal.jpg") + `}}
			}
		}`
		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+statusID, tweetJSON))))
		}, store, nil)

		post, err := client.GetPost(context.Background(), statusID)
		if err != nil {
			t.Fatalf("GetPost preview display failed: %v", err)
		}
		if !post.IsPreviewDisplay || post.ID != statusID || post.Text != "Subscriber-only preview & teaser" {
			t.Fatalf("unexpected preview post: %+v", post)
		}
		if post.Author.ScreenName != "example_user" || post.Author.Name != "Example Name" {
			t.Fatalf("author = %+v", post.Author)
		}
		if post.RuleType != RuleTypeWord {
			t.Fatalf("RuleType = %q, want word", post.RuleType)
		}
	})

	t.Run("TweetWithVisibilityResults unwraps inner Tweet cleanly", func(t *testing.T) {
		statusID := "1791257321273778510"
		tweetJSON := `{
			"__typename": "TweetWithVisibilityResults",
			"limitedActionResults": {"limited_actions": [{"action": "Reply"}]},
			"tweet": {
				"__typename": "Tweet",
				"rest_id": "` + statusID + `",
				"core": {"user_results": {"result": ` + makeUserLegacyJSON("1000000000000000002", "legacy_user", "Legacy Display", "https://pbs.twimg.com/profile_images/2/b_normal.png") + `}},
				"legacy": {
					"id_str": "` + statusID + `",
					"user_id_str": "1000000000000000002",
					"created_at": "Fri May 17 00:01:00 +0000 2024",
					"full_text": "Visible with limited replies"
				}
			}
		}`
		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+statusID, tweetJSON))))
		}, store, nil)

		post, err := client.GetPost(context.Background(), statusID)
		if err != nil {
			t.Fatalf("GetPost visibility results failed: %v", err)
		}
		if post.ID != statusID || post.VisibilityLabel == "" || post.Author.ScreenName != "legacy_user" || post.Author.Name != "Legacy Display" {
			t.Fatalf("unexpected unwrapped post: %+v", post)
		}
		if post.Author.AvatarURL != "https://pbs.twimg.com/profile_images/2/b_400x400.png" {
			t.Fatalf("legacy avatar not upgraded: %q", post.Author.AvatarURL)
		}
	})

	t.Run("TweetUnavailable every documented reason maps accurately without circuit break", func(t *testing.T) {
		statusID := "1791257321273778511"
		reasons := []struct {
			reason   string
			wantKind string
		}{
			{"Protected", site.ErrKindForbidden},
			{"Blocked", site.ErrKindForbidden},
			{"CountryWithheld", site.ErrKindUnavailable},
			{"BounceDeleted", site.ErrKindUnavailable},
			{"Suspended", site.ErrKindUnavailable},
			{"NsfwLoggedOut", site.ErrKindForbidden},
			{"NsfwViewerHasNoStatedAge", site.ErrKindForbidden},
			{"Offboarded", site.ErrKindUnavailable},
		}
		for _, tc := range reasons {
			t.Run(tc.reason, func(t *testing.T) {
				unavailJSON := `{"__typename":"TweetUnavailable","reason":"` + tc.reason + `"}`
				_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+statusID, unavailJSON))))
				}, store, nil)

				_, err := client.GetPost(context.Background(), statusID)
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("reason %q kind = %q, want %q (%v)", tc.reason, site.KindOf(err), tc.wantKind, err)
				}
				if site.IsCircuitBreak(err) || site.IsKind(err, site.ErrKindAuthRequired) {
					t.Fatalf("TweetUnavailable(%s) must never trip circuit break or auth_required", tc.reason)
				}
			})
		}
	})

	t.Run("TweetTombstone returns unavailable", func(t *testing.T) {
		statusID := "1791257321273778512"
		tombJSON := `{"__typename":"TweetTombstone","tombstone":{"text":{"text":"This Post was deleted by the Post author."}}}`
		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+statusID, tombJSON))))
		}, store, nil)

		_, err := client.GetPost(context.Background(), statusID)
		if !site.IsKind(err, site.ErrKindUnavailable) {
			t.Fatalf("tombstone err = %v, want unavailable", err)
		}
	})

	t.Run("Focal rest_id mismatch returns schema_changed", func(t *testing.T) {
		reqID := "1791257321273778509"
		wrongID := "1791257321273778999"
		tweetJSON := `{
			"__typename": "Tweet",
			"rest_id": "` + wrongID + `",
			"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},
			"legacy": {"id_str": "` + wrongID + `", "full_text": "Wrong tweet id"}
		}`
		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+reqID, tweetJSON))))
		}, store, nil)

		if _, err := client.GetPost(context.Background(), reqID); !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("rest_id mismatch err = %v, want schema_changed", err)
		}
	})
}

func TestPostRecursiveNoteTweetTCoRestoreRetweetQuoteAndDegradedOrigins(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_POST_02; ct0=SynthCT0Post0002")

	t.Run("recursive note_tweet on main, retweeted, and quoted plus t.co entity restore", func(t *testing.T) {
		outerID := "1791257321273778600"
		rtID := "1791257321273778550"
		qtID := "1791257321273778500"

		quotedJSON := `{
			"__typename": "Tweet",
			"rest_id": "` + qtID + `",
			"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000003", "quoted_author", "Quoted Author", "") + `}},
			"note_tweet": {"is_expandable": true, "note_tweet_results": {"result": {"text": "Full quoted longform text with link https://t.co/SynthQt01", "entity_set": {"urls": [{"url": "https://t.co/SynthQt01", "expanded_url": "https://example.org/quoted"}]}}}},
			"legacy": {"id_str": "` + qtID + `", "full_text": "Truncated quoted..."}
		}`

		retweetedJSON := `{
			"__typename": "Tweet",
			"rest_id": "` + rtID + `",
			"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000002", "rt_author", "RT Author", "") + `}},
			"note_tweet": {"is_expandable": true, "note_tweet_results": {"result": {"text": "Full retweeted longform text https://t.co/SynthRt01 https://t.co/SynthSelfRt https://t.co/SynthMed01", "entity_set": {"urls": [{"url": "https://t.co/SynthRt01", "expanded_url": "https://example.org/rt-article"}, {"url": "https://t.co/SynthSelfRt", "expanded_url": "https://x.com/rt_author/status/` + rtID + `"}]}}}},
			"quoted_status_result": {"result": ` + quotedJSON + `},
			"legacy": {
				"id_str": "` + rtID + `",
				"is_quote_status": true,
				"full_text": "Truncated rt...",
				"extended_entities": {"media": [{"id_str": "901", "type": "photo", "url": "https://t.co/SynthMed01", "media_url_https": "https://pbs.twimg.com/media/SynthRtPic.jpg"}]}
			}
		}`

		outerJSON := `{
			"__typename": "Tweet",
			"rest_id": "` + outerID + `",
			"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},
			"note_tweet": {"is_expandable": true, "note_tweet_results": {"result": {"text": "Main note tweet text https://t.co/SynthMain01", "entity_set": {"urls": [{"url": "https://t.co/SynthMain01", "expanded_url": "https://example.org/main"}]}}}},
			"legacy": {
				"id_str": "` + outerID + `",
				"created_at": "Fri May 17 00:05:00 +0000 2024",
				"full_text": "RT @rt_author: Truncated...",
				"retweeted_status_result": {"result": ` + retweetedJSON + `}
			}
		}`

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+outerID, outerJSON))))
		}, store, nil)

		post, err := client.GetPost(context.Background(), outerID)
		if err != nil {
			t.Fatalf("GetPost failed: %v", err)
		}
		if !post.IsLongText || post.Text != "Main note tweet text https://example.org/main" {
			t.Fatalf("main note_tweet text = %q (isLong=%v)", post.Text, post.IsLongText)
		}
		if !post.IsRetweet || !post.IsForward() || post.RuleType != RuleTypeForward {
			t.Fatalf("retweet flags = (isRetweet=%v ruleType=%q)", post.IsRetweet, post.RuleType)
		}
		if post.Retweeted == nil || !post.Retweeted.IsLongText {
			t.Fatalf("retweeted note_tweet not hydrated: %+v", post.Retweeted)
		}
		if post.Retweeted.Text != "Full retweeted longform text https://example.org/rt-article" {
			t.Fatalf("retweeted hydrated text = %q (self-link & media t.co should be stripped)", post.Retweeted.Text)
		}
		if post.Retweeted.Quoted == nil || !post.Retweeted.Quoted.IsLongText {
			t.Fatalf("quoted note_tweet not hydrated: %+v", post.Retweeted.Quoted)
		}
		if post.Retweeted.Quoted.Text != "Full quoted longform text with link https://example.org/quoted" {
			t.Fatalf("quoted hydrated text = %q", post.Retweeted.Quoted.Text)
		}
	})

	t.Run("degraded deleted protected region-withheld origins succeed without schema error", func(t *testing.T) {
		cases := []struct {
			name       string
			quotedRes  string
			wantReason string
		}{
			{"deleted tombstone", `{"result":{"__typename":"TweetTombstone","tombstone":{"text":{"text":"This Post was deleted by the Post author."}}}}`, "deleted"},
			{"protected origin", `{"result":{"__typename":"TweetUnavailable","reason":"Protected"}}`, "受保护"},
			{"country withheld origin", `{"result":{"__typename":"TweetUnavailable","reason":"CountryWithheld"}}`, "地区限制"},
			{"empty origin object", `{"result":{}}`, "不可见"},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				id := fmt.Sprintf("179125732127377870%d", i)
				outerJSON := `{
					"__typename": "Tweet",
					"rest_id": "` + id + `",
					"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},
					"quoted_status_result": ` + tc.quotedRes + `,
					"legacy": {"id_str": "` + id + `", "is_quote_status": true, "full_text": "Commenting on unavailable post"}
				}`
				_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+id, outerJSON))))
				}, store, nil)

				post, err := client.GetPost(context.Background(), id)
				if err != nil {
					t.Fatalf("degraded quote failed: %v", err)
				}
				if !post.IsQuote || post.RuleType != RuleTypeWord || !post.OriginUnavailable {
					t.Fatalf("expected OriginUnavailable=true text quote (RuleTypeWord), got %+v", post)
				}
				if !strings.Contains(post.OriginUnavailableReason, tc.wantReason) {
					t.Fatalf("OriginUnavailableReason = %q, want substring %q", post.OriginUnavailableReason, tc.wantReason)
				}
			})
		}
	})
}

func TestPostAuthorSelfReplyThreadCycleGuardAndDepthCap(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_POST_03; ct0=SynthCT0Post0003")
	authorID := "1000000000000000001"
	otherID := "1000000000000000099"

	t.Run("stitches same-author thread and filters other users", func(t *testing.T) {
		rootID := "1791257321273778801"
		r2ID := "1791257321273778802"
		otherReplyID := "1791257321273778803"
		r3ID := "1791257321273778804"

		makeT := func(id, uid, handle, replyTo, text string) string {
			return `{"__typename":"Tweet","rest_id":"` + id + `","core":{"user_results":{"result":` +
				makeUserCoreJSON(uid, handle, handle, "") + `}},"legacy":{"id_str":"` + id +
				`","user_id_str":"` + uid + `","in_reply_to_status_id_str":"` + replyTo + `","full_text":"` + text + `"}}`
		}

		entries := strings.Join([]string{
			makeTweetEntry("tweet-"+rootID, makeT(rootID, authorID, "example_user", "", "Thread part 1")),
			`{"entryId":"conversationthread-1","content":{"entryType":"TimelineTimelineModule","items":[` +
				`{"entryId":"conversationthread-1-tweet-` + r2ID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeT(r2ID, authorID, "example_user", rootID, "Thread part 2") + `}}}},` +
				`{"entryId":"conversationthread-1-tweet-` + otherReplyID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeT(otherReplyID, otherID, "stranger_user", rootID, "Other user reply MUST be excluded") + `}}}},` +
				`{"entryId":"conversationthread-1-tweet-` + r3ID + `","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":` + makeT(r3ID, authorID, "example_user", r2ID, "Thread part 3") + `}}}}` +
				`]}}`,
		}, ",")

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(entries)))
		}, store, nil)

		post, err := client.GetPost(context.Background(), rootID)
		if err != nil {
			t.Fatalf("GetPost thread failed: %v", err)
		}
		if len(post.Thread) != 2 || post.Thread[0].ID != r2ID || post.Thread[1].ID != r3ID {
			t.Fatalf("stitched Thread = %+v, want [%s, %s]", post.Thread, r2ID, r3ID)
		}
		full := post.FullThreadText()
		if !strings.Contains(full, "Thread part 1") || !strings.Contains(full, "Thread part 2") || !strings.Contains(full, "Thread part 3") {
			t.Fatalf("FullThreadText missing parts: %q", full)
		}
		if strings.Contains(full, "Other user reply") {
			t.Fatalf("FullThreadText leaked other user's reply: %q", full)
		}
	})

	t.Run("cycle guard terminates safely on loops", func(t *testing.T) {
		a := &Post{ID: "1791257321273779901", Author: AuthorSummary{RestID: authorID}, InReplyToStatusID: "1791257321273779902", Text: "Node A"}
		b := &Post{ID: "1791257321273779902", Author: AuthorSummary{RestID: authorID}, InReplyToStatusID: "1791257321273779901", Text: "Node B"}
		byID := map[string]*Post{a.ID: a, b.ID: b}
		stitchAuthorSelfReplyThread(a, []*Post{a, b}, byID)
		if len(a.ThreadAncestors) != 1 || len(a.Thread) != 0 {
			t.Fatalf("2-node cycle should visit B once and stop: anc=%d thread=%d", len(a.ThreadAncestors), len(a.Thread))
		}

		self := &Post{ID: "1791257321273779903", Author: AuthorSummary{RestID: authorID}, InReplyToStatusID: "1791257321273779903", Text: "Self loop"}
		stitchAuthorSelfReplyThread(self, []*Post{self}, map[string]*Post{self.ID: self})
		if len(self.ThreadAncestors) != 0 || len(self.Thread) != 0 {
			t.Fatalf("self-loop must not duplicate focal: anc=%d thread=%d", len(self.ThreadAncestors), len(self.Thread))
		}
	})

	t.Run("depth cap stops cleanly at MaxThreadDepth (50)", func(t *testing.T) {
		focal := &Post{ID: "1791257321273779000", Author: AuthorSummary{RestID: authorID}, Text: "Root"}
		byID := map[string]*Post{focal.ID: focal}
		ordered := []*Post{focal}
		prevID := focal.ID
		for i := 1; i <= 65; i++ {
			id := fmt.Sprintf("1791257321273779%03d", i)
			p := &Post{
				ID:                id,
				Author:            AuthorSummary{RestID: authorID},
				InReplyToStatusID: prevID,
				Text:              fmt.Sprintf("Part %d", i),
			}
			byID[id] = p
			ordered = append(ordered, p)
			prevID = id
		}
		stitchAuthorSelfReplyThread(focal, ordered, byID)
		if len(focal.Thread) != MaxThreadDepth {
			t.Fatalf("thread len = %d, want %d", len(focal.Thread), MaxThreadDepth)
		}
	})
}

func TestPostMediaMixed4ItemsGIFVideoBitrateHLSOnlyAndDegradedCards(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_POST_04; ct0=SynthCT0Post0004")

	t.Run("4-item mixed media preserves exact order photo+gif+video+photo", func(t *testing.T) {
		id := "1791257321273778901"
		tweetJSON := `{
			"__typename": "Tweet",
			"rest_id": "` + id + `",
			"core": {"user_results": {"result": ` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},
			"legacy": {
				"id_str": "` + id + `",
				"full_text": "Mixed 4 items https://t.co/SynthM4",
				"extended_entities": {
					"media": [
						{"media_key":"3_1","type":"photo","url":"https://t.co/SynthM4","media_url_https":"https://pbs.twimg.com/media/SynthImg1.jpeg","original_info":{"width":1920,"height":1080}},
						{"media_key":"16_2","type":"animated_gif","url":"https://t.co/SynthM4","media_url_https":"https://pbs.twimg.com/tweet_video_thumb/SynthGif2.jpg","video_info":{"variants":[{"bitrate":0,"content_type":"video/mp4","url":"https://video.twimg.com/tweet_video/SynthGif2.mp4"}]}},
						{"media_key":"7_3","type":"video","url":"https://t.co/SynthM4","media_url_https":"https://pbs.twimg.com/ext_tw_video_thumb/3/img/thumb.png","video_info":{"duration_millis":12400,"variants":[{"content_type":"application/x-mpegURL","url":"https://video.twimg.com/ext_tw_video/3/pl/master.m3u8"},{"bitrate":832000,"content_type":"video/mp4","url":"https://video.twimg.com/ext_tw_video/3/pu/vid/640x360/low.mp4"},{"bitrate":2176000,"content_type":"video/mp4","url":"https://video.twimg.com/ext_tw_video/3/pu/vid/avc1/1280x720/high.mp4"}]}},
						{"media_key":"3_4","type":"photo","url":"https://t.co/SynthM4","media_url_https":"https://pbs.twimg.com/media/SynthImg4?format=webp&name=medium","original_info":{"width":800,"height":600}}
					]
				}
			}
		}`

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+id, tweetJSON))))
		}, store, nil)

		post, err := client.GetPost(context.Background(), id)
		if err != nil {
			t.Fatalf("GetPost mixed failed: %v", err)
		}
		if post.Text != "Mixed 4 items" {
			t.Fatalf("media t.co not stripped cleanly: %q", post.Text)
		}
		if post.RuleType != RuleTypeDraw {
			t.Fatalf("mixed RuleType = %q, want draw", post.RuleType)
		}
		if len(post.Media) != 4 {
			t.Fatalf("media count = %d, want 4", len(post.Media))
		}
		// Item 1: photo jpeg -> ?format=jpg&name=orig only (no recompressed fallback).
		m0 := post.Media[0]
		if m0.Index != 1 || m0.Kind != MediaKindPhoto || m0.ImageURL != "https://pbs.twimg.com/media/SynthImg1?format=jpg&name=orig" {
			t.Fatalf("m0 = %+v (url=%q)", m0, m0.ImageURL)
		}
		// Originals only (2026-11-24): name=large and the bare media URL are recompressed copies.
		if len(m0.FallbackImageURLs) != 0 {
			t.Fatalf("m0 must have no compressed fallbacks, got %v", m0.FallbackImageURLs)
		}
		// Item 2: animated_gif -> GIF: true, MP4 variant selected.
		m1 := post.Media[1]
		if m1.Index != 2 || m1.Kind != MediaKindGIF || !m1.GIF || m1.VideoURL != "https://video.twimg.com/tweet_video/SynthGif2.mp4" {
			t.Fatalf("m1 = %+v (video=%q)", m1, m1.VideoURL)
		}
		// Item 3: video -> highest bitrate MP4 selected, all 3 variants recorded, dimensions parsed.
		m2 := post.Media[2]
		if m2.Index != 3 || m2.Kind != MediaKindVideo || m2.VideoBitrate != 2176000 || m2.Width != 1280 || m2.Height != 720 || m2.DurationSec != 12 {
			t.Fatalf("m2 = %+v", m2)
		}
		if m2.VideoURL != "https://video.twimg.com/ext_tw_video/3/pu/vid/avc1/1280x720/high.mp4" {
			t.Fatalf("m2 VideoURL = %q", m2.VideoURL)
		}
		if len(m2.Variants) != 3 {
			t.Fatalf("m2 variants = %d, want 3", len(m2.Variants))
		}
		// Item 4: photo with existing query format=webp -> name=orig.
		m3 := post.Media[3]
		if m3.Index != 4 || m3.Kind != MediaKindPhoto || m3.ImageURL != "https://pbs.twimg.com/media/SynthImg4?format=webp&name=orig" {
			t.Fatalf("m3 = %+v (url=%q)", m3, m3.ImageURL)
		}
	})

	t.Run("single video rule type vs single GIF rule type vs HLS-only video", func(t *testing.T) {
		// Single progressive video -> RuleTypeVideo.
		vidID := "1791257321273778902"
		vidTweet := `{"__typename":"Tweet","rest_id":"` + vidID + `","core":{"user_results":{"result":` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},"legacy":{"id_str":"` + vidID + `","full_text":"Video post","extended_entities":{"media":[{"type":"video","media_url_https":"https://pbs.twimg.com/ext_tw_video_thumb/1/t.jpg","video_info":{"duration_millis":5000,"variants":[{"bitrate":2176000,"content_type":"video/mp4","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/v.mp4"}]}}]}}}`

		// Single GIF -> RuleTypeDraw (NOT video per x.md §15.1 #10!).
		gifID := "1791257321273778903"
		gifTweet := `{"__typename":"Tweet","rest_id":"` + gifID + `","core":{"user_results":{"result":` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},"legacy":{"id_str":"` + gifID + `","full_text":"GIF post","extended_entities":{"media":[{"type":"animated_gif","media_url_https":"https://pbs.twimg.com/tweet_video_thumb/2.jpg","video_info":{"variants":[{"bitrate":0,"content_type":"video/mp4","url":"https://video.twimg.com/tweet_video/2.mp4"}]}}]}}}`

		// HLS-only video (only application/x-mpegURL) -> HLSOnly: true, VideoURL empty!
		hlsID := "1791257321273778904"
		hlsTweet := `{"__typename":"Tweet","rest_id":"` + hlsID + `","core":{"user_results":{"result":` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},"legacy":{"id_str":"` + hlsID + `","full_text":"HLS only post","extended_entities":{"media":[{"type":"video","media_url_https":"https://pbs.twimg.com/ext_tw_video_thumb/3/t.jpg","video_info":{"duration_millis":180000,"variants":[{"content_type":"application/x-mpegURL","url":"https://video.twimg.com/ext_tw_video/3/pl/master.m3u8?tag=21"}]}}]}}}`

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.Contains(r.URL.RawQuery, vidID):
				_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+vidID, vidTweet))))
			case strings.Contains(r.URL.RawQuery, gifID):
				_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+gifID, gifTweet))))
			case strings.Contains(r.URL.RawQuery, hlsID):
				_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+hlsID, hlsTweet))))
			default:
				http.NotFound(w, r)
			}
		}, store, nil)

		pVid, err := client.GetPost(context.Background(), vidID)
		if err != nil || pVid.RuleType != RuleTypeVideo || pVid.HasHLSOnlyVideo {
			t.Fatalf("single video = (%+v, %v)", pVid, err)
		}
		pGif, err := client.GetPost(context.Background(), gifID)
		if err != nil || pGif.RuleType != RuleTypeDraw || !pGif.Media[0].GIF {
			t.Fatalf("single GIF must map to RuleTypeDraw, got (%+v, %v)", pGif, err)
		}
		pHLS, err := client.GetPost(context.Background(), hlsID)
		if err != nil || !pHLS.HasHLSOnlyVideo || !pHLS.Media[0].HLSOnly || pHLS.Media[0].VideoURL != "" || pHLS.Media[0].HLSOnlyNotice != HLSOnlyNoticeText {
			t.Fatalf("HLS-only post = (%+v, %v)", pHLS, err)
		}
	})

	t.Run("safe degradation for poll, URL card, X Article, Broadcast and Spaces cards", func(t *testing.T) {
		cards := []struct {
			name     string
			id       string
			extra    string
			wantKind string
			wantDesc string
		}{
			{
				name: "poll card",
				id:   "1791257321273778911",
				extra: `"card":{"legacy":{"name":"poll2choice_text_only","binding_values":[` +
					`{"key":"choice1_label","value":{"type":"STRING","string_value":"Go"}},` +
					`{"key":"choice1_count","value":{"type":"STRING","string_value":"120"}},` +
					`{"key":"choice2_label","value":{"type":"STRING","string_value":"Rust"}},` +
					`{"key":"choice2_count","value":{"type":"STRING","string_value":"80"}}]}}`,
				wantKind: CardKindVote,
				wantDesc: "Go",
			},
			{
				name: "URL summary card",
				id:   "1791257321273778912",
				extra: `"card":{"legacy":{"name":"summary_large_image","url":"https://example.org/post","binding_values":[` +
					`{"key":"title","value":{"type":"STRING","string_value":"Example Article"}},` +
					`{"key":"description","value":{"type":"STRING","string_value":"Short summary"}},` +
					`{"key":"card_url","value":{"type":"STRING","string_value":"https://example.org/post"}}]}}`,
				wantKind: CardKindWebpage,
				wantDesc: "Short summary",
			},
			{
				name:     "X Article card",
				id:       "1791257321273778913",
				extra:    `"article":{"article_results":{"result":{"rest_id":"1000000000000000099","title":"Deep Dive X Article","preview_text":"Article preview paragraph","cover_media_img_url":"https://pbs.twimg.com/media/SynthArtCover.jpg"}}}`,
				wantKind: CardKindArticle,
				wantDesc: "Article preview paragraph",
			},
			{
				name: "Broadcast card",
				id:   "1791257321273778914",
				extra: `"card":{"legacy":{"name":"745291183405076480:broadcast","binding_values":[` +
					`{"key":"broadcast_id","value":{"type":"STRING","string_value":"1SynthBcast01"}},` +
					`{"key":"broadcast_title","value":{"type":"STRING","string_value":"Live Stream"}}]}}`,
				wantKind: CardKindLive,
				wantDesc: LiveUnsupportedText,
			},
			{
				name: "Spaces card",
				id:   "1791257321273778915",
				extra: `"card":{"legacy":{"name":"3691233323:audiospace","binding_values":[` +
					`{"key":"id","value":{"type":"STRING","string_value":"1SynthSpace01"}},` +
					`{"key":"title","value":{"type":"STRING","string_value":"Audio Space"}}]}}`,
				wantKind: CardKindLive,
				wantDesc: LiveUnsupportedText,
			},
		}

		for _, tc := range cards {
			t.Run(tc.name, func(t *testing.T) {
				tweetJSON := `{"__typename":"Tweet","rest_id":"` + tc.id + `",` + tc.extra + `,"core":{"user_results":{"result":` + makeUserCoreJSON("1000000000000000001", "example_user", "Example", "") + `}},"legacy":{"id_str":"` + tc.id + `","full_text":"Post with card"}}`
				_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(wrapTweetDetailEntries(makeTweetEntry("tweet-"+tc.id, tweetJSON))))
				}, store, nil)

				post, err := client.GetPost(context.Background(), tc.id)
				if err != nil {
					t.Fatalf("GetPost(%s) failed: %v", tc.name, err)
				}
				if post.RuleType != RuleTypeWord || len(post.Media) != 0 {
					t.Fatalf("degraded card must be RuleTypeWord with 0 media entries, got rule=%q media=%d", post.RuleType, len(post.Media))
				}
				if post.Card == nil || post.Card.Kind != tc.wantKind || !post.Card.Unsupported || !strings.Contains(post.Card.Desc, tc.wantDesc) {
					t.Fatalf("unexpected Card: %+v", post.Card)
				}
				if tc.wantKind == CardKindVote && (post.Poll == nil || len(post.Poll.Options) != 2 || post.Poll.TotalVotes != 200) {
					t.Fatalf("unexpected Poll summary: %+v", post.Poll)
				}
				// Verify PlanDownload creates NO video cache entry for degraded cards!
				plan, pErr := NewAdapter(client).PlanDownloadFromPost(post)
				if pErr != nil {
					t.Fatalf("PlanDownloadFromPost(%s) failed: %v", tc.name, pErr)
				}
				if plan.Kind != "image_note" || len(plan.VideoURLs) != 0 || len(plan.Images) != 0 || plan.Card == nil {
					raw, _ := json.Marshal(plan)
					t.Fatalf("degraded card produced unexpected DownloadPlan: %s", string(raw))
				}
			})
		}
	})
}

func TestQuoteRuleTypeDecouplingReplyContextAndNestedOriginPlan(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_POST_05; ct0=SynthCT0Post0005")
	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}, store, nil)
	adapter := NewAdapter(client)

	// 1. computeRuleType decouples quote posts from pure retweets (DEC-091).
	rtPost := &Post{IsRetweet: true}
	if got := computeRuleType(rtPost); got != RuleTypeForward {
		t.Fatalf("pure retweet RuleType = %q, want forward", got)
	}
	textQuote := &Post{IsQuote: true, Text: "文字引用"}
	if got := computeRuleType(textQuote); got != RuleTypeWord {
		t.Fatalf("text quote RuleType = %q, want word", got)
	}
	photoQuote := &Post{IsQuote: true, Media: []MediaItem{{Kind: MediaKindPhoto}}}
	if got := computeRuleType(photoQuote); got != RuleTypeDraw {
		t.Fatalf("photo quote RuleType = %q, want draw", got)
	}
	vidQuote := &Post{IsQuote: true, Media: []MediaItem{{Kind: MediaKindVideo}}}
	if got := computeRuleType(vidQuote); got != RuleTypeVideo {
		t.Fatalf("video quote RuleType = %q, want video", got)
	}

	// 2. PlanDownloadFromPost maps IsQuote, InReplyTo, OriginInReplyTo, and depth-1 OriginOrigin.
	innerQuote := &Post{
		ID:           "1791257321273790001",
		Author:       AuthorSummary{RestID: "1000000000000000011", ScreenName: "inner_author", Name: "内层作者"},
		Text:         "最内层被引用正文",
		CanonicalURL: "https://x.com/inner_author/status/1791257321273790001",
		IsQuote:      true,
		Quoted:       &Post{ID: "1791257321273790000", Text: "第三层不应进入两层嵌套"},
	}
	midQuote := &Post{
		ID:                  "1791257321273790002",
		Author:              AuthorSummary{RestID: "1000000000000000012", ScreenName: "mid_author", Name: "中层作者"},
		Text:                "中层引用帖正文",
		CanonicalURL:        "https://x.com/mid_author/status/1791257321273790002",
		InReplyToStatusID:   "1791257321273789999",
		InReplyToScreenName: "mid_parent",
		IsQuote:             true,
		Quoted:              innerQuote,
	}
	outerQuote := &Post{
		ID:                  "1791257321273790003",
		Author:              AuthorSummary{RestID: "1000000000000000013", ScreenName: "outer_author", Name: "外层作者"},
		Text:                "外层回复并引用",
		CanonicalURL:        "https://x.com/outer_author/status/1791257321273790003",
		InReplyToStatusID:   "1791257321273789998",
		InReplyToUserID:     "1000000000000000099",
		InReplyToScreenName: "target_handle",
		IsQuote:             true,
		Quoted:              midQuote,
	}
	plan, err := adapter.PlanDownloadFromPost(outerQuote)
	if err != nil {
		t.Fatalf("PlanDownloadFromPost(outerQuote): %v", err)
	}
	if !plan.IsForward || !plan.IsQuote {
		t.Fatalf("expected IsForward=true IsQuote=true, got %+v", plan)
	}
	if plan.InReplyTo == nil || plan.InReplyTo.ScreenName != "target_handle" || plan.InReplyTo.StatusID != "1791257321273789998" {
		t.Fatalf("unexpected plan.InReplyTo: %+v", plan.InReplyTo)
	}
	if plan.OriginInReplyTo == nil || plan.OriginInReplyTo.ScreenName != "mid_parent" {
		t.Fatalf("unexpected plan.OriginInReplyTo: %+v", plan.OriginInReplyTo)
	}
	if plan.OriginOrigin == nil || plan.OriginOrigin.ID != innerQuote.ID || plan.OriginOrigin.AuthorUniqueID != "inner_author" {
		t.Fatalf("unexpected plan.OriginOrigin: %+v", plan.OriginOrigin)
	}

	// Pure RT of a quote post -> IsForward=true, IsQuote=false, OriginOrigin populated!
	rtOfQuote := &Post{
		ID:        "1791257321273790004",
		Author:    AuthorSummary{RestID: "1000000000000000014", ScreenName: "rt_user", Name: "转帖人"},
		Text:      "RT @mid_author: 中层引用帖正文",
		IsRetweet: true,
		IsQuote:   true, // even if X legacy flag sets is_quote_status on an RT wrapper
		Retweeted: midQuote,
	}
	rtPlan, err := adapter.PlanDownloadFromPost(rtOfQuote)
	if err != nil {
		t.Fatalf("PlanDownloadFromPost(rtOfQuote): %v", err)
	}
	if !rtPlan.IsForward || rtPlan.IsQuote {
		t.Fatalf("RT of quote must have IsForward=true IsQuote=false, got %+v", rtPlan)
	}
	if rtPlan.OriginOrigin == nil || rtPlan.OriginOrigin.ID != innerQuote.ID {
		t.Fatalf("RT of quote missing depth-1 OriginOrigin: %+v", rtPlan.OriginOrigin)
	}
}
