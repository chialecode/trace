package douyin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func mustReadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// TestMixedNoteAndVODWorkDetail verifies M3-K08:
//   - images[] checked BEFORE top-level video.play_addr BGM trap
//   - mixed note page order & 4 subtypes (image with .heic last & watermark excluded, live_photo, video page, unknownKind fallback)
//   - live_photo_type == 1 missing motion clip fails with schema_changed
//   - music play_url upgraded to https & is_original_sound independent of musician is_original
//   - DEC-043 stream selection (resolution tier first, then codec, excluding playwm & is_bytevc1==2, direct CDN before cold & /aweme/v1/play/)
//   - Stage-2 x-secsdk-web-signature + uifid + header-only x-secsdk-web-expire on aweme/detail
//   - statistics.play_count never exposed as 0
//   - paid / encrypted marked uncacheable & rejected by PlanDownload; filter_detail vs empty aweme_detail classified accurately
func TestMixedNoteAndVODWorkDetail(t *testing.T) {
	ctx := context.Background()
	mixedNoteJSON := mustReadFixture(t, "aweme_detail_mixed_note.json")
	videoJSON := mustReadFixture(t, "aweme_detail_video.json")

	t.Run("mixed_note_beats_bgm_trap_and_preserves_four_page_kinds", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var capturedHeaders http.Header
		var capturedRawQuery string

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Now: func() time.Time {
				return time.UnixMilli(1763000000000)
			},
			Entropy: newDeterministicEntropy(0x21),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				capturedHeaders = req.Header.Clone()
				capturedRawQuery = req.URL.RawQuery
				return jsonHTTPResponse(http.StatusOK, mixedNoteJSON, nil), nil
			}),
		})

		work, err := client.GetWorkDetail(ctx, "7300000000000000002", StreamSelectOptions{PreferredCodec: "avc"})
		if err != nil {
			t.Fatalf("GetWorkDetail(mixed note) failed: %v", err)
		}

		// Verify Stage-2 protected-path headers on /aweme/v1/web/aweme/detail/.
		sig := capturedHeaders.Get("x-secsdk-web-signature")
		if len(sig) != 32 || !strings.Contains(capturedRawQuery, "x-secsdk-web-signature="+sig) {
			t.Fatalf("missing or mismatched 32-hex x-secsdk-web-signature: header=%q query=%q", sig, capturedRawQuery)
		}
		if gotUIFID := capturedHeaders.Get("uifid"); gotUIFID != "SYNTH_ACTIVE_UIFID_SEED" {
			t.Fatalf("uifid header = %q, want SYNTH_ACTIVE_UIFID_SEED", gotUIFID)
		}
		if exp := capturedHeaders.Get("x-secsdk-web-expire"); exp == "" {
			t.Fatalf("missing x-secsdk-web-expire header")
		}
		if strings.Contains(capturedRawQuery, "x-secsdk-web-expire") {
			t.Fatalf("x-secsdk-web-expire must NEVER appear in URL query: %s", capturedRawQuery)
		}

		// Must be classified as Note, NOT tricked by top-level video.play_addr BGM MP3!
		if work.Kind != WorkKindNote || work.Video != nil {
			t.Fatalf("work.Kind = %q, Video = %+v; want WorkKindNote with nil top-level Video", work.Kind, work.Video)
		}
		if work.Stats.PlayCount != nil {
			t.Fatalf("Stats.PlayCount = %v, want nil (never expose placeholder 0)", *work.Stats.PlayCount)
		}
		if rawSnap, _ := json.Marshal(work); strings.Contains(string(rawSnap), "playCount") {
			t.Fatalf("serialized WorkDetail leaked playCount: %s", string(rawSnap))
		}

		if work.Music == nil || !work.Music.IsOriginalSound || len(work.Music.PlayURLs) != 1 ||
			work.Music.PlayURLs[0] != "https://sf3-cdn-tos.douyinstatic.com/obj/ies-music/synthetic_bgm.mp3" {
			t.Fatalf("unexpected Music parsing: %+v", work.Music)
		}

		if len(work.Pages) != 4 {
			t.Fatalf("len(work.Pages) = %d, want 4", len(work.Pages))
		}

		// Page 1: static image -> .webp and .jpeg before .heic; watermarked download_url_list excluded.
		p1 := work.Pages[0]
		if p1.Index != 1 || p1.Kind != NotePageImage || p1.UnknownKind || len(p1.ImageURLs) != 3 {
			t.Fatalf("unexpected Page 1: %+v", p1)
		}
		if !strings.HasSuffix(p1.ImageURLs[2], ".heic") {
			t.Fatalf("expected .heic URL to be ordered last on Page 1, got %v", p1.ImageURLs)
		}
		for _, u := range p1.ImageURLs {
			if strings.Contains(u, "water") {
				t.Fatalf("watermarked URL leaked into Page 1 ImageURLs: %s", u)
			}
		}

		// Page 2: Live Photo -> static image + motion MP4.
		p2 := work.Pages[1]
		if p2.Index != 2 || p2.Kind != NotePageLivePhoto || p2.VideoStream == nil ||
			len(p2.VideoStream.URLs) != 1 || !strings.HasSuffix(p2.VideoStream.URLs[0], "/livephoto_page_02.mp4") {
			t.Fatalf("unexpected Page 2 (live_photo): %+v", p2)
		}

		// Page 3: Mixed video page -> cover + 1080P60 stream selected over 720P30.
		p3 := work.Pages[2]
		if p3.Index != 3 || p3.Kind != NotePageVideo || p3.VideoStream == nil ||
			p3.VideoStream.Tier != 1080 || p3.VideoStream.FPS != 60 ||
			!strings.HasSuffix(p3.VideoStream.URLs[0], "/note_video_1080p60.mp4") {
			t.Fatalf("unexpected Page 3 (video page): %+v (stream=%+v)", p3, p3.VideoStream)
		}

		// Page 4: Unknown clip_type=99 -> preserves image & marks UnknownKind=true.
		p4 := work.Pages[3]
		if p4.Index != 4 || p4.Kind != NotePageUnknown || !p4.UnknownKind ||
			len(p4.ImageURLs) != 1 || !strings.HasSuffix(p4.ImageURLs[0], "/synth_page_04.jpeg") {
			t.Fatalf("unexpected Page 4 (unknown fallback): %+v", p4)
		}

		// Adapter.PlanDownload maps mixed note cleanly to image_note DownloadPlan.
		adapter := NewAdapter(client)
		plan, err := adapter.PlanDownload(ctx, "7300000000000000002", 1)
		if err != nil {
			t.Fatalf("PlanDownload(mixed note) failed: %v", err)
		}
		if plan.Kind != "image_note" || plan.ItemKey != "douyin:dynamic:7300000000000000002" || len(plan.Images) != 4 || len(plan.MusicURLs) != 1 {
			t.Fatalf("unexpected DownloadPlan for mixed note: %+v", plan)
		}
		probeReq, _ := http.NewRequest(http.MethodGet, "https://v26-web.douyinvod.com/synthetic/livephoto_page_02.mp4", nil)
		probeReq.Header.Set("Cookie", "sessionid=MUST_STRIP_ON_CDN")
		plan.Media.ApplyHeaders(probeReq)
		if gotCookie := probeReq.Header.Get("Cookie"); gotCookie != "" {
			t.Fatalf("Douyin VOD media transport leaked Cookie header: %q", gotCookie)
		}
	})

	t.Run("live_photo_missing_motion_clip_fails_explicitly", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		brokenLivePhoto := `{
			"status_code": 0,
			"aweme_detail": {
				"aweme_id": "7300000000000000003",
				"aweme_type": 68,
				"desc": "损坏实况样本",
				"author": {"uid": "1001", "sec_uid": "MS4wLjABAAAA_SYNTHETIC_AUTHOR_01", "nickname": "作者"},
				"images": [{
					"width": 1080,
					"height": 1440,
					"live_photo_type": 1,
					"url_list": ["https://p3-pc-sign.douyinpic.com/tos-cn-i-0813/only_still.jpeg"]
				}]
			}
		}`
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x22),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return jsonHTTPResponse(http.StatusOK, brokenLivePhoto, nil), nil
			}),
		})
		_, err := client.GetWorkDetail(ctx, "7300000000000000003", StreamSelectOptions{})
		if !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("broken live photo kind = %q, want schema_changed (err=%v)", site.KindOf(err), err)
		}
	})

	t.Run("vod_stream_selection_dec043_avc_vs_hevc_and_url_ranking", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		pref := "avc"
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			PreferredCodec: func() string {
				return pref
			},
			Entropy: newDeterministicEntropy(0x23),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return jsonHTTPResponse(http.StatusOK, videoJSON, nil), nil
			}),
		})
		adapter := NewAdapter(client)

		// 1. Default PreferredCodec = "avc" -> picks 1080P AVC (excludes bytevc2 is_bytevc1==2 & playwm).
		planAVC, err := adapter.PlanDownload(ctx, "7300000000000000001", 1)
		if err != nil {
			t.Fatalf("PlanDownload(avc) failed: %v", err)
		}
		if planAVC.Kind != "single_mp4" || planAVC.VideoCodec != "avc" || planAVC.QualityLabel != "1080P" {
			t.Fatalf("unexpected AVC plan: %+v", planAVC)
		}
		for _, u := range planAVC.VideoURLs {
			if strings.Contains(u, "playwm") || strings.Contains(u, "bytevc2") {
				t.Fatalf("excluded watermark/ByteVC2 URL leaked into VideoURLs: %s", u)
			}
		}
		if len(planAVC.VideoURLs) != 3 ||
			!strings.Contains(planAVC.VideoURLs[0], "1080p30_avc_direct.mp4") ||
			!strings.Contains(planAVC.VideoURLs[1], "-mc-cold.douyinvod.com") ||
			!strings.Contains(planAVC.VideoURLs[2], "/aweme/v1/play/") {
			t.Fatalf("unexpected CDN URL ranking: %v", planAVC.VideoURLs)
		}

		// 2. PreferredCodec = "hevc" -> picks 1080P60 HEVC (still excluding ByteVC2!).
		pref = "hevc"
		planHEVC, err := adapter.PlanDownload(ctx, "7300000000000000001", 1)
		if err != nil {
			t.Fatalf("PlanDownload(hevc) failed: %v", err)
		}
		if planHEVC.VideoCodec != "hevc" || planHEVC.QualityLabel != "1080P" ||
			!strings.Contains(planHEVC.VideoURLs[0], "1080p60_hevc_direct.mp4") {
			t.Fatalf("unexpected HEVC plan: %+v", planHEVC)
		}
	})

	t.Run("paid_encrypted_filtered_and_empty_aweme_detail", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var payload string
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x24),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return jsonHTTPResponse(http.StatusOK, payload, nil), nil
			}),
		})
		adapter := NewAdapter(client)

		// Paid unpaid work -> Uncacheable=true, PlanDownload Unsupported=true.
		payload = `{
			"status_code": 0,
			"aweme_detail": {
				"aweme_id": "7300000000000000010",
				"desc": "付费未购测试",
				"author": {"uid": "1001", "sec_uid": "MS4wLjABAAAA_SYNTHETIC_AUTHOR_01", "nickname": "作者"},
				"charge_info": {"is_charge_content": true, "has_paid": false},
				"video": {"width": 1080, "height": 1920, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/paid_preview.mp4"]}}
			}
		}`
		wPaid, err := client.GetWorkDetail(ctx, "7300000000000000010", StreamSelectOptions{})
		if err != nil || !wPaid.Uncacheable {
			t.Fatalf("expected Uncacheable=true for unpaid charge content, got work=%+v err=%v", wPaid, err)
		}
		if _, err := adapter.PlanDownload(ctx, "7300000000000000010", 1); !site.IsUnsupported(err) {
			t.Fatalf("PlanDownload(paid) err = %v, want Unsupported=true", err)
		}

		// Ordinary work: the site attaches entertainment_product_info with
		// limit_free.in_free=false to normal videos too; it is not a paid signal.
		payload = `{
			"status_code": 0,
			"aweme_detail": {
				"aweme_id": "7300000000000000014",
				"desc": "普通作品",
				"author": {"uid": "1001", "sec_uid": "MS4wLjABAAAA_SYNTHETIC_AUTHOR_01", "nickname": "作者"},
				"charge_info": {"is_charge_content": false, "has_paid": false},
				"entertainment_product_info": {"sub_title": null, "market_info": {"limit_free": {"in_free": false}, "marketing_tag": null}},
				"video": {"width": 1080, "height": 1920, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/normal.mp4"], "width": 1080, "height": 1920}}
			}
		}`
		wNormal, err := client.GetWorkDetail(ctx, "7300000000000000014", StreamSelectOptions{})
		if err != nil || wNormal.Uncacheable {
			t.Fatalf("ordinary work with in_free=false must stay cacheable, got work=%+v err=%v", wNormal, err)
		}

		// Encrypted video -> Uncacheable=true, PlanDownload Unsupported=true.
		payload = `{
			"status_code": 0,
			"aweme_detail": {
				"aweme_id": "7300000000000000011",
				"desc": "加密流测试",
				"author": {"uid": "1001", "sec_uid": "MS4wLjABAAAA_SYNTHETIC_AUTHOR_01", "nickname": "作者"},
				"video": {"width": 1080, "height": 1920, "encrypt_type": 1, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/enc.mp4"]}}
			}
		}`
		if _, err := adapter.PlanDownload(ctx, "7300000000000000011", 1); !site.IsUnsupported(err) {
			t.Fatalf("PlanDownload(encrypted) err = %v, want Unsupported=true", err)
		}

		// Business filter_detail -> ErrKindUnavailable.
		payload = `{"status_code":0,"aweme_detail":null,"filter_detail":{"aweme_id":"7300000000000000012","detail_msg":"作者已设为私密或删除"}}`
		if _, err := client.GetWorkDetail(ctx, "7300000000000000012", StreamSelectOptions{}); !site.IsKind(err, site.ErrKindUnavailable) {
			t.Fatalf("filter_detail kind = %q, want unavailable (err=%v)", site.KindOf(err), err)
		}

		// Silent empty aweme_detail without filter_detail -> ErrKindRiskControl.
		payload = `{"status_code":0,"aweme_detail":null}`
		if _, err := client.GetWorkDetail(ctx, "7300000000000000013", StreamSelectOptions{}); !site.IsKind(err, site.ErrKindRiskControl) {
			t.Fatalf("silent null aweme_detail kind = %q, want risk_control (err=%v)", site.KindOf(err), err)
		}
	})
}

// TestUserProfileAndAwemePostTruthTable verifies M3-K07:
//   - profile/other sec_uid consistency check, secret==1 private account, room_id & room_data web_rid extraction
//   - aweme/post is_top==1 old pinned post preservation, cross-page SeenIDs dedup, cursor-stall guard
//   - all 6 rows of douyin.md §9.2 empty-shell truth table
func TestUserProfileAndAwemePostTruthTable(t *testing.T) {
	ctx := context.Background()
	const targetSecUID = "MS4wLjABAAAA_SYNTH_CREATOR_POSTS_01"

	t.Run("profile_other_and_paginated_posts_with_is_top_dedup_and_stall", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		mismatchMode := false

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x31),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/aweme/v1/web/user/profile/other/":
					if mismatchMode {
						return jsonHTTPResponse(http.StatusOK, `{
							"status_code": 0,
							"user": {"uid": "2001", "sec_uid": "MS4wLjABAAAA_WRONG_RETURNED_USER", "nickname": "错位用户"}
						}`, nil), nil
					}
					return jsonHTTPResponse(http.StatusOK, `{
						"status_code": 0,
						"user": {
							"uid": "200000000001",
							"sec_uid": "`+targetSecUID+`",
							"short_id": "55667788",
							"unique_id": "",
							"nickname": "合成订阅作者",
							"secret": 0,
							"aweme_count": 24,
							"room_id": 7400000000000000088,
							"room_data": "{\"owner\":{\"web_rid\":\"55667788\"}}"
						}
					}`, nil), nil

				case "/aweme/v1/web/aweme/post/":
					cur := req.URL.Query().Get("max_cursor")
					if cur == "0" {
						return jsonHTTPResponse(http.StatusOK, `{
							"status_code": 0,
							"has_more": 1,
							"max_cursor": 1763000500000,
							"aweme_list": [
								{
									"aweme_id": "7300000000000000101",
									"aweme_type": 0,
									"is_top": 1,
									"create_time": 1700000000,
									"desc": "置顶旧作品",
									"author": {"uid": "200000000001", "sec_uid": "`+targetSecUID+`", "nickname": "合成订阅作者"},
									"video": {"width": 1080, "height": 1920, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/top.mp4"]}}
								},
								{
									"aweme_id": "7300000000000000102",
									"aweme_type": 0,
									"is_top": 0,
									"create_time": 1763000900,
									"desc": "最新普通作品",
									"author": {"uid": "200000000001", "sec_uid": "`+targetSecUID+`", "nickname": "合成订阅作者"},
									"video": {"width": 1080, "height": 1920, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/new.mp4"]}}
								}
							]
						}`, nil), nil
					}
					// Page 2: echoes overlapping 102 + new 103, and stalls cursor at 1763000500000 with has_more=1!
					return jsonHTTPResponse(http.StatusOK, `{
						"status_code": 0,
						"has_more": 1,
						"max_cursor": 1763000500000,
						"aweme_list": [
							{
								"aweme_id": "7300000000000000102",
								"aweme_type": 0,
								"create_time": 1763000900,
								"desc": "重复作品应去重",
								"author": {"uid": "200000000001", "sec_uid": "`+targetSecUID+`", "nickname": "合成订阅作者"},
								"video": {"width": 1080, "height": 1920, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/new.mp4"]}}
							},
							{
								"aweme_id": "7300000000000000103",
								"aweme_type": 0,
								"create_time": 1763000400,
								"desc": "第二页作品",
								"author": {"uid": "200000000001", "sec_uid": "`+targetSecUID+`", "nickname": "合成订阅作者"},
								"video": {"width": 1080, "height": 1920, "play_addr": {"url_list": ["https://v26-web.douyinvod.com/synthetic/p2.mp4"]}}
							}
						]
					}`, nil), nil
				default:
					return jsonHTTPResponse(http.StatusNotFound, `{}`, nil), nil
				}
			}),
		})

		prof, err := client.GetUserProfile(ctx, targetSecUID)
		if err != nil {
			t.Fatalf("GetUserProfile failed: %v", err)
		}
		if prof.UniqueID != "55667788" || prof.RoomID != "7400000000000000088" || prof.WebRID != "55667788" {
			t.Fatalf("unexpected UserProfile: %+v", prof)
		}

		mismatchMode = true
		if _, err := client.GetUserProfile(ctx, targetSecUID); !site.IsKind(err, site.ErrKindSchemaChanged) {
			t.Fatalf("mismatched sec_uid kind = %q, want schema_changed", site.KindOf(err))
		}
		mismatchMode = false

		seen := make(map[string]bool)
		p1, err := client.ListUserPosts(ctx, UserPostsRequest{
			SecUID:    targetSecUID,
			MaxCursor: "0",
			SeenIDs:   seen,
		})
		if err != nil {
			t.Fatalf("ListUserPosts page 1 failed: %v", err)
		}
		if len(p1.Works) != 2 || !p1.Works[0].IsTop || p1.Works[1].IsTop || !p1.HasMore || p1.NextCursor != "1763000500000" {
			t.Fatalf("unexpected Page 1: %+v", p1)
		}

		p2, err := client.ListUserPosts(ctx, UserPostsRequest{
			SecUID:    targetSecUID,
			MaxCursor: p1.NextCursor,
			SeenIDs:   seen,
		})
		if err != nil {
			t.Fatalf("ListUserPosts page 2 failed: %v", err)
		}
		if len(p2.Works) != 1 || p2.Works[0].ID != "7300000000000000103" {
			t.Fatalf("cross-page dedup failed on Page 2: %+v", p2.Works)
		}
		if p2.HasMore || !p2.CursorStalled {
			t.Fatalf("expected cursor-stall protection (HasMore=false, CursorStalled=true), got %+v", p2)
		}
	})

	t.Run("section_9_2_empty_shell_truth_table_all_six_rows", func(t *testing.T) {
		// Row 1: HTTP 200 with 0-byte body -> risk_control + automatic breaker notified.
		{
			store := openTestDouyinCredStore(t, true)
			breakerFired := false
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x32),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return jsonHTTPResponse(http.StatusOK, "", nil), nil
				}),
			})
			client.SetAutomaticRequestHandlers(nil, func(_ string, err error) {
				if site.IsKind(err, site.ErrKindRiskControl) {
					breakerFired = true
				}
			})
			autoCtx := site.WithAutomaticRequests(ctx, "subscription")
			_, err := client.ListUserPosts(autoCtx, UserPostsRequest{SecUID: targetSecUID, MaxCursor: "0"})
			if !site.IsKind(err, site.ErrKindRiskControl) || !breakerFired {
				t.Fatalf("Row 1 (0-byte body) err=%v breakerFired=%v, want risk_control & true", err, breakerFired)
			}
		}

		// Row 2: max_cursor > 0, bare {"status_code":0} -> auth_required + store marked expired.
		{
			store := openTestDouyinCredStore(t, true)
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x33),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return jsonHTTPResponse(http.StatusOK, `{"status_code":0}`, nil), nil
				}),
			})
			_, err := client.ListUserPosts(ctx, UserPostsRequest{SecUID: targetSecUID, MaxCursor: "1763000500000"})
			if !site.IsKind(err, site.ErrKindAuthRequired) {
				t.Fatalf("Row 2 (subsequent page bare shell) kind = %q, want auth_required", site.KindOf(err))
			}
			if store.Status().State != "expired" {
				t.Fatalf("Row 2 expected store state=expired, got %q", store.Status().State)
			}
		}

		// Row 3: max_cursor == 0, bare {"status_code":0} -> risk_control (store remains logged_in!).
		{
			store := openTestDouyinCredStore(t, true)
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x34),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return jsonHTTPResponse(http.StatusOK, `{"status_code":0}`, nil), nil
				}),
			})
			_, err := client.ListUserPosts(ctx, UserPostsRequest{SecUID: targetSecUID, MaxCursor: "0"})
			if !site.IsKind(err, site.ErrKindRiskControl) {
				t.Fatalf("Row 3 (page-1 bare shell) kind = %q, want risk_control", site.KindOf(err))
			}
			if store.Status().State != "logged_in" {
				t.Fatalf("Row 3 must NOT expire credential on risk_control, got %q", store.Status().State)
			}
		}

		// Row 4: aweme_list: [] && has_more == 1 -> Adapter.FetchCreatorPosts allows AT MOST 1 follow-up,
		// then stops with UncertainEmpty=true if page 2 is also empty!
		{
			store := openTestDouyinCredStore(t, true)
			var postReqs atomic.Int32
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x35),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					n := postReqs.Add(1)
					if n == 1 {
						return jsonHTTPResponse(http.StatusOK, `{"status_code":0,"aweme_list":[],"has_more":1,"max_cursor":1763000400000}`, nil), nil
					}
					return jsonHTTPResponse(http.StatusOK, `{"status_code":0,"aweme_list":[],"has_more":1,"max_cursor":1763000300000}`, nil), nil
				}),
			})
			adapter := NewAdapter(client)
			res, err := adapter.FetchCreatorPosts(ctx, targetSecUID, "0")
			if err != nil {
				t.Fatalf("Row 4 two-empty-pages returned unexpected error: %v", err)
			}
			if postReqs.Load() != 2 || !res.UncertainEmpty || res.HasMore {
				t.Fatalf("Row 4 expected exact 2 requests & UncertainEmpty=true, got reqs=%d res=%+v", postReqs.Load(), res)
			}
		}

		// Row 5: max_cursor > 0, explicit aweme_list: [] && has_more: 0 -> genuine end of list!
		{
			store := openTestDouyinCredStore(t, true)
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x36),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return jsonHTTPResponse(http.StatusOK, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":1763000100000}`, nil), nil
				}),
			})
			page, err := client.ListUserPosts(ctx, UserPostsRequest{SecUID: targetSecUID, MaxCursor: "1763000500000"})
			if err != nil || page.HasMore || page.UncertainEmpty || len(page.Works) != 0 {
				t.Fatalf("Row 5 expected genuine end, got page=%+v err=%v", page, err)
			}
		}

		// Row 6: max_cursor == 0, aweme_list: [] && has_more: 0 -> cross-checks profile/other!
		{
			store := openTestDouyinCredStore(t, true)
			var secret int
			var awemeCount int
			client := NewClient(ClientOptions{
				CredStore:   store,
				MinInterval: -1,
				Entropy:     newDeterministicEntropy(0x37),
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/aweme/v1/web/aweme/post/" {
						return jsonHTTPResponse(http.StatusOK, `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`, nil), nil
					}
					body, _ := json.Marshal(map[string]any{
						"status_code": 0,
						"user": map[string]any{
							"uid":         "2009",
							"sec_uid":     targetSecUID,
							"nickname":    "交叉核对账号",
							"secret":      secret,
							"aweme_count": awemeCount,
						},
					})
					return jsonHTTPResponse(http.StatusOK, string(body), nil), nil
				}),
			})
			adapter := NewAdapter(client)

			// 6a: secret == 1 -> ErrKindForbidden.
			secret, awemeCount = 1, 10
			if _, err := adapter.FetchCreatorPosts(ctx, targetSecUID, "0"); !site.IsKind(err, site.ErrKindForbidden) {
				t.Fatalf("Row 6a (private account) kind = %q, want forbidden", site.KindOf(err))
			}

			// 6b: secret == 0 && aweme_count == 0 -> genuine 0 works (UncertainEmpty == false).
			secret, awemeCount = 0, 0
			resZero, err := adapter.FetchCreatorPosts(ctx, targetSecUID, "0")
			if err != nil || resZero.UncertainEmpty || len(resZero.Posts) != 0 {
				t.Fatalf("Row 6b (genuine 0 works) res=%+v err=%v", resZero, err)
			}

			// 6c: secret == 0 && aweme_count == 12 -> silent throttle / friends-only (UncertainEmpty == true!).
			secret, awemeCount = 0, 12
			resUncertain, err := adapter.FetchCreatorPosts(ctx, targetSecUID, "0")
			if err != nil || !resUncertain.UncertainEmpty {
				t.Fatalf("Row 6c (aweme_count=12 but empty list) expected UncertainEmpty=true, got res=%+v err=%v", resUncertain, err)
			}
		}
	})
}

// TestLiveResolutionFallbackCheckAliveAndFLVSelection verifies M3-K16:
//   - both live.douyin.com/<short_id> and live.douyin.com/<transient_web_rid> resolve anchor sec_uid & room_id
//   - confirmed permanent numeric short_id is retained in webRid cache over transient web_rid
//   - invalidated (4001038) or recycled (wrong sec_uid) cached webRid clears cache and falls back to profile/other
//     with AT MOST 1 extra HTTP request while preserving AccountUID = sec_uid
//   - CheckRoomsAlive preserves 64-bit 19-digit JSON integer room_ids via UseNumber()
//   - SelectLiveStream picks origin > uhd > hd > sd > ld, excludes only_audio=1, and returns clear Chinese errors
//     for audio-only and HLS-only rooms
func TestLiveResolutionFallbackCheckAliveAndFLVSelection(t *testing.T) {
	ctx := context.Background()
	liveEnterJSON := mustReadFixture(t, "live_web_enter.json")
	const anchorSecUID = "MS4wLjABAAAA_SYNTHETIC_ANCHOR_01"

	t.Run("both_live_url_forms_resolve_sec_uid_prefer_short_id_and_select_origin_flv", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x41),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/webcast/room/web/enter/" {
					return jsonHTTPResponse(http.StatusOK, liveEnterJSON, nil), nil
				}
				return jsonHTTPResponse(http.StatusNotFound, `{}`, nil), nil
			}),
		})

		// 1. Resolve via permanent numeric short_id (10008888).
		t1, err := client.ResolveLink(ctx, "https://live.douyin.com/10008888")
		if err != nil {
			t.Fatalf("ResolveLink(short_id) failed: %v", err)
		}
		if t1.Kind != "live" || t1.SecUID != anchorSecUID || t1.RoomID != "7400000000000000009" || t1.WebRID != "10008888" {
			t.Fatalf("unexpected target for short_id: %+v", t1)
		}
		if got := client.CachedWebRID(anchorSecUID); got != "10008888" {
			t.Fatalf("CachedWebRID after short_id = %q, want 10008888", got)
		}

		// 2. Resolve via transient web_rid (990011223344) -> resolves same anchor & room_id, AND keeps confirmed 10008888!
		t2, err := client.ResolveLink(ctx, "https://live.douyin.com/990011223344")
		if err != nil {
			t.Fatalf("ResolveLink(transient web_rid) failed: %v", err)
		}
		if t2.SecUID != anchorSecUID || t2.RoomID != "7400000000000000009" || t2.WebRID != "10008888" {
			t.Fatalf("transient web_rid should preserve confirmed short_id 10008888, got %+v", t2)
		}
		if got := client.CachedWebRID(anchorSecUID); got != "10008888" {
			t.Fatalf("CachedWebRID overwritten by transient web_rid: %q", got)
		}

		room, err := client.EnterLiveRoom(ctx, "10008888", anchorSecUID)
		if err != nil {
			t.Fatalf("EnterLiveRoom failed: %v", err)
		}
		if room.LiveDedupeKey() != "douyin:live:7400000000000000009" {
			t.Fatalf("LiveDedupeKey = %q, want douyin:live:7400000000000000009", room.LiveDedupeKey())
		}

		// 3. SelectLiveStream picks origin (excludes ao/only_audio=1, upgrades http -> https, includes backup).
		plan, err := client.SelectLiveStream(ctx, anchorSecUID, "10008888", []string{"avc", "hevc"})
		if err != nil {
			t.Fatalf("SelectLiveStream failed: %v", err)
		}
		if plan.Transport != site.LiveTransportHTTPFLV || plan.Format != "flv" ||
			plan.Quality != 10000 || plan.QualityName != "原画" || plan.Codec != "hevc" {
			t.Fatalf("unexpected LiveStreamPlan: %+v", plan)
		}
		if len(plan.URLs) != 2 ||
			plan.URLs[0] != "https://pull-flv-f1.douyincdn.com/stage/stream_origin.flv" ||
			plan.URLs[1] != "https://pull-flv-f2.douyincdn.com/stage/stream_origin_bak.flv" {
			t.Fatalf("unexpected origin FLV URLs: %v", plan.URLs)
		}
		for _, u := range plan.URLs {
			if strings.Contains(u, "only_audio=1") {
				t.Fatalf("audio-only URL leaked into FLV plan: %s", u)
			}
		}
		liveReq, _ := http.NewRequest(http.MethodGet, plan.URLs[0], nil)
		liveReq.Header.Set("Cookie", "sessionid=MUST_STRIP_ON_LIVE_CDN")
		plan.Media.ApplyHeaders(liveReq)
		if gotCookie := liveReq.Header.Get("Cookie"); gotCookie != "" {
			t.Fatalf("Douyin live CDN transport leaked Cookie header: %q", gotCookie)
		}
	})

	t.Run("invalidated_and_recycled_webrid_fallback_at_most_one_extra_request", func(t *testing.T) {
		for _, mode := range []string{"4001038_invalidated", "recycled_wrong_sec_uid"} {
			t.Run(mode, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				var totalReqs atomic.Int32

				client := NewClient(ClientOptions{
					CredStore:   store,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x42),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						totalReqs.Add(1)
						switch req.URL.Path {
						case "/webcast/room/web/enter/":
							if mode == "4001038_invalidated" {
								return jsonHTTPResponse(http.StatusOK, `{"status_code":4001038,"data":{"prompts":"房间不存在"}}`, nil), nil
							}
							// Recycled web_rid now belongs to a completely different anchor!
							return jsonHTTPResponse(http.StatusOK, `{
								"status_code": 0,
								"data": {
									"data": [{"id_str": "7499999999999999999", "status": 2, "title": "他人直播间"}],
									"user": {"id_str": "9999", "sec_uid": "MS4wLjABAAAA_SOME_OTHER_ANCHOR_99", "nickname": "他人"}
								}
							}`, nil), nil

						case "/aweme/v1/web/user/profile/other/":
							return jsonHTTPResponse(http.StatusOK, `{
								"status_code": 0,
								"user": {
									"uid": "100000000001",
									"sec_uid": "`+anchorSecUID+`",
									"short_id": "0",
									"nickname": "合成主播一号",
									"room_id": 7400000000000000055,
									"room_data": "{\"id_str\":\"7400000000000000055\",\"status\":2,\"title\":\"回退恢复的直播间\",\"start_time\":1763000800,\"owner\":{\"web_rid\":\"88990011\"},\"stream_url\":{\"flv_pull_url\":{\"HD1\":\"https://pull-flv-f1.douyincdn.com/stage/recovered_hd.flv\"}}}"
								}
							}`, nil), nil

						default:
							t.Fatalf("unexpected extra endpoint called during webRid fallback: %s", req.URL.Path)
							return nil, nil
						}
					}),
				})

				client.rememberWebRID(anchorSecUID, "44556677", false)
				st, err := client.CheckLiveStatus(ctx, anchorSecUID, "44556677")
				if err != nil {
					t.Fatalf("CheckLiveStatus fallback failed: %v", err)
				}
				// Must execute AT MOST 1 extra HTTP request after web/enter (total == 2)!
				if got := totalReqs.Load(); got != 2 {
					t.Fatalf("total HTTP requests = %d, want exact 2 (1 web/enter + 1 profile/other fallback)", got)
				}
				if st.AccountUID != anchorSecUID {
					t.Fatalf("AccountUID mutated to %q, must strictly remain %q", st.AccountUID, anchorSecUID)
				}
				if !st.InvalidatedWebRID || st.Status != site.LiveStateLive ||
					st.RoomID != "7400000000000000055" || st.WebRID != "88990011" {
					t.Fatalf("unexpected fallback LiveStatus: %+v", st)
				}
				if got := client.CachedWebRID(anchorSecUID); got != "88990011" {
					t.Fatalf("CachedWebRID after fallback recovery = %q, want 88990011", got)
				}
			})
		}
	})

	t.Run("check_rooms_alive_preserves_64bit_room_id_precision", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		// 7400000000000000009 and 7400000000000000010 would collide if decoded into float64!
		idA := "7400000000000000009"
		idB := "7400000000000000010"
		idOmitted := "7400000000000000099"

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x43),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				// Return out-of-order integer room_ids and omit idOmitted.
				body := `{"status_code":0,"data":[{"room_id":7400000000000000010,"alive":false},{"room_id":7400000000000000009,"alive":true}]}`
				return jsonHTTPResponse(http.StatusOK, body, nil), nil
			}),
		})

		aliveMap, err := client.CheckRoomsAlive(ctx, []string{idA, idB, idOmitted})
		if err != nil {
			t.Fatalf("CheckRoomsAlive failed: %v", err)
		}
		if !aliveMap[idA] || aliveMap[idB] || aliveMap[idOmitted] {
			t.Fatalf("64-bit room_id precision or mapping failed: %+v", aliveMap)
		}
	})

	t.Run("audio_only_and_hls_only_clear_errors", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var enterBody string

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x44),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return jsonHTTPResponse(http.StatusOK, enterBody, nil), nil
			}),
		})

		// 1. Audio-only room -> LiveStateAudioOnly on CheckLiveStatus & clear Chinese Unsupported error on SelectLiveStream.
		enterBody = `{
			"status_code": 0,
			"data": {
				"enter_mode": 1,
				"room_status": 1,
				"data": [{
					"id_str": "7400000000000000201",
					"status": 2,
					"live_room_mode": 2,
					"title": "语音电台测试",
					"stream_url": {"flv_pull_url": {"AO": "https://pull-flv-f1.douyincdn.com/stage/ao.flv?only_audio=1"}}
				}],
				"user": {"id_str": "1001", "sec_uid": "` + anchorSecUID + `", "nickname": "合成主播"}
			}
		}`
		st, err := client.CheckLiveStatus(ctx, anchorSecUID, "22334455")
		if err != nil || st.Status != site.LiveStateAudioOnly {
			t.Fatalf("CheckLiveStatus(audio-only) = %+v, err=%v; want LiveStateAudioOnly", st, err)
		}
		_, err = client.SelectLiveStream(ctx, anchorSecUID, "22334455", nil)
		if !site.IsUnsupported(err) || !strings.Contains(err.Error(), "语音直播") {
			t.Fatalf("SelectLiveStream(audio-only) err = %v, want Unsupported with 语音直播 message", err)
		}

		// 2. HLS-only room -> clear Chinese Unsupported error stating HTTP-FLV only.
		enterBody = `{
			"status_code": 0,
			"data": {
				"enter_mode": 0,
				"room_status": 0,
				"data": [{
					"id_str": "7400000000000000202",
					"status": 2,
					"title": "仅HLS直播测试",
					"stream_url": {
						"hls_pull_url_map": {"FULL_HD1": "https://pull-hls-f1.douyincdn.com/stage/only_hls.m3u8"}
					}
				}],
				"user": {"id_str": "1001", "sec_uid": "` + anchorSecUID + `", "nickname": "合成主播"}
			}
		}`
		_, err = client.SelectLiveStream(ctx, anchorSecUID, "22334456", nil)
		if !site.IsUnsupported(err) || !strings.Contains(err.Error(), "仅提供 HLS 流") {
			t.Fatalf("SelectLiveStream(HLS-only) err = %v, want Unsupported with 仅提供 HLS 流 message", err)
		}
	})

	t.Run("enter_mode_1_video_room_outer_room_status_and_reflow_room_id_2", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var enterBody string
		var reflowBody string
		var gotReflowRoomID string

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x45),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/webcast/room/web/enter/":
					return jsonHTTPResponse(http.StatusOK, enterBody, nil), nil
				case "/webcast/room/reflow/info/":
					gotReflowRoomID = req.URL.Query().Get("room_id")
					return jsonHTTPResponse(http.StatusOK, reflowBody, nil), nil
				default:
					return jsonHTTPResponse(http.StatusNotFound, `{}`, nil), nil
				}
			}),
		})

		// 1. Video room with enter_mode=1 + room_status=0 + data[0].status=2 (even with live_room_mode=2)
		// MUST NOT false-positive as AudioOnly and MUST select the HTTP-FLV video stream.
		enterBody = `{
			"status_code": 0,
			"data": {
				"enter_mode": 1,
				"room_status": 0,
				"data": [{
					"id_str": "7400000000000000301",
					"status": 2,
					"live_room_mode": 2,
					"title": "正常视频直播但带enter_mode_1",
					"stream_url": {
						"flv_pull_url": {
							"FULL_HD1": "https://pull-flv-f1.douyincdn.com/stage/video_uhd.flv",
							"AO": "https://pull-flv-f1.douyincdn.com/stage/video_ao.flv?only_audio=1"
						}
					}
				}],
				"user": {"id_str": "1001", "sec_uid": "` + anchorSecUID + `", "nickname": "合成主播"}
			}
		}`
		room, err := client.EnterLiveRoom(ctx, "33445566", anchorSecUID)
		if err != nil {
			t.Fatalf("EnterLiveRoom(enter_mode=1, room_status=0) failed: %v", err)
		}
		if room.AudioOnly || room.RoomStatus != 2 {
			t.Fatalf("expected AudioOnly=false & RoomStatus=2, got AudioOnly=%v RoomStatus=%d", room.AudioOnly, room.RoomStatus)
		}
		plan, err := client.SelectLiveStream(ctx, anchorSecUID, "33445566", nil)
		if err != nil || plan.Quality != 400 || len(plan.URLs) != 1 {
			t.Fatalf("SelectLiveStream(enter_mode=1, room_status=0) plan=%+v, err=%v", plan, err)
		}

		// 2. Outer room_status=1 or inner live_type_audio=true -> AudioOnly=true even if non-AO stream_url keys exist.
		enterBody = `{
			"status_code": 0,
			"data": {
				"enter_mode": 1,
				"room_status": 1,
				"data": [{
					"id_str": "7400000000000000302",
					"status": 2,
					"title": "电台直播模式",
					"stream_url": {"flv_pull_url": {"HD1": "https://pull-flv-f1.douyincdn.com/stage/radio_bg.flv"}}
				}],
				"user": {"id_str": "1001", "sec_uid": "` + anchorSecUID + `", "nickname": "合成主播"}
			}
		}`
		radioRoom, err := client.EnterLiveRoom(ctx, "33445567", anchorSecUID)
		if err != nil || !radioRoom.AudioOnly {
			t.Fatalf("expected AudioOnly=true when outer room_status=1, got room=%+v err=%v", radioRoom, err)
		}

		// 3. Inner data[0].status=0 + outer room_status=2 (offline in douyin.md §8.2) -> RoomStatus=4 (offline), NOT 2!
		enterBody = `{
			"status_code": 0,
			"data": {
				"enter_mode": 0,
				"room_status": 2,
				"data": [{
					"id_str": "",
					"status": 0,
					"title": "已下播直播间"
				}],
				"user": {"id_str": "1001", "sec_uid": "` + anchorSecUID + `", "nickname": "合成主播"}
			}
		}`
		offRoom, err := client.EnterLiveRoom(ctx, "33445568", anchorSecUID)
		if err != nil || offRoom.RoomStatus != 4 {
			t.Fatalf("expected RoomStatus=4 when data[0].status=0 & outer room_status=2, got room=%+v err=%v", offRoom, err)
		}

		// 4. ReflowRoomInfo("2", secUID) accepts 1-digit room_id=2 and handles live_type_audio=true.
		reflowBody = `{
			"status_code": 0,
			"data": {
				"room": {
					"id_str": "7400000000000000399",
					"status": 4,
					"live_type_audio": true,
					"title": "历史场次",
					"owner": {
						"id_str": "1001",
						"sec_uid": "` + anchorSecUID + `",
						"short_id": 10008888,
						"web_rid": "10008888",
						"nickname": "合成主播"
					}
				}
			}
		}`
		reflowDetail, err := client.ReflowRoomInfo(ctx, "2", anchorSecUID)
		if err != nil {
			t.Fatalf("ReflowRoomInfo(room_id=2) failed: %v", err)
		}
		if gotReflowRoomID != "2" || reflowDetail.ResponseWebRID != "10008888" || !reflowDetail.AudioOnly {
			t.Fatalf("unexpected ReflowRoomInfo(room_id=2) result: gotRoomID=%q detail=%+v", gotReflowRoomID, reflowDetail)
		}
	})
}
