package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/sitecheck"
)

const (
	synthX024AuthToken      = "0123456789abcdef0123456789abcdef01234567"
	synthX024CT0            = "abcdef0123456789abcdef0123456789"
	synthX024CT0Rotated     = "fedcba9876543210fedcba9876543210"
	synthX024KDT            = "SynthXKdt024TokenValue987654"
	synthX024GuestID        = "v1%3A1795327200999"
	synthX024SelfRestID     = "1000000000000000101"
	synthX024SelfHandle     = "synth_x_self024"
	synthX024SelfName       = "合成测试账号甲"
	synthX024CreatorRestID  = "1000000000000000202"
	synthX024CreatorHandle  = "synth_x_auth024"
	synthX024CreatorName    = "合成创作者乙"
	synthX024ProtRestID     = "1000000000000000303"
	synthX024ProtHandle     = "synth_x_prot024"
	synthX024ProtName       = "合成受保护账号丙"
	synthX024Follow1RestID  = "1000000000000000401"
	synthX024Follow1Handle  = "synth_x_follow1"
	synthX024Follow2RestID  = "1000000000000000402"
	synthX024Follow2Handle  = "synth_x_follow2"
	synthX024MissingUserID  = "9000000000000000001"
	synthX024MissingPostID  = "9000000000000000002"
	synthX024PostVideoID    = "1800000000000000101"
	synthX024PostDelOrigID  = "1800000000000000102"
	synthX024PostRTQuoteID  = "1800000000000000103"
	synthX024PostQuoteID    = "1800000000000000104"
	synthX024PostTcoID      = "1800000000000000105"
	synthX024PostPinnedID   = "1800000000000000010"
	synthX024PostTL1ID      = "1800000000000000205"
	synthX024PostTL2ID      = "1800000000000000204"
	synthX024PostTL3ID      = "1800000000000000203"
	synthX024PostBookmarkID = "1800000000000000301"
	synthX024PostLikeID     = "1800000000000000302"
	synthX024TcoCode        = "SynthTco024"
	synthX024SignedQS       = "Expires=1999999999&Signature=SynthXMediaSig024&Key-Pair-Id=APKASYNTH024"
	synthX024VideoPostText  = "合成短视频正文唯一哨兵文本ALPHA024"
	synthX024DelOrigText    = "合成引用已删除原帖正文唯一哨兵文本BETA024"
	synthX024TcoPostText    = "合成外链帖子正文唯一哨兵文本GAMMA024"
	synthX024CookieRaw      = "auth_token=" + synthX024AuthToken + "; ct0=" + synthX024CT0 + "; twid=u%3D" + synthX024SelfRestID + "; guest_id=" + synthX024GuestID + "; kdt=" + synthX024KDT + "; __cf_bm=DROP_CF_BM_024; personalization_id=DROP_ADS_024"
)

type act024AbsRewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func newAct024AbsRewriteTransport(t *testing.T, srv *httptest.Server) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse httptest server url: %v", err)
	}
	return &act024AbsRewriteTransport{target: u, inner: srv.Client().Transport}
}

func (rt *act024AbsRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return rt.inner.RoundTrip(clone)
}

func readXTestdataFile(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("..", "..", "internal", "site", "x", "testdata", name)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return raw
}

type act024MockOpts struct {
	preUsedLogin      int
	useVite           bool
	includeFolderOp   bool
	hlsOnlyVideo      bool
	omitTcoInPost     bool
	targetMode        string // "full", "real_shape_no_missing", "empty"
	externalTcoTarget bool
}

type act024MockEnv struct {
	rootDir           string
	srv               *httptest.Server
	endpoints         x.Endpoints
	assetRT           http.RoundTripper
	frontendHits      atomic.Int64
	apiHits           atomic.Int64
	shortHits         atomic.Int64
	mediaHits         atomic.Int64
	hlsHits           atomic.Int64
	assetCookieLeaked atomic.Bool
	shortLinkCookied  atomic.Bool
	cdnCookieLeaked   atomic.Bool
	cdnRefererLeaked  atomic.Bool
	fetchedDetailIDs  []string
	detailMu          sync.Mutex
	hook              func(w http.ResponseWriter, r *http.Request) bool
}

func (e *act024MockEnv) recordFetchedDetailID(id string) {
	e.detailMu.Lock()
	defer e.detailMu.Unlock()
	e.fetchedDetailIDs = append(e.fetchedDetailIDs, id)
}

func (e *act024MockEnv) getFetchedDetailIDs() []string {
	e.detailMu.Lock()
	defer e.detailMu.Unlock()
	return append([]string(nil), e.fetchedDetailIDs...)
}

func synthX024UserObj(restID, screenName, name, avatar string, statuses int, protected bool, pinned []string) map[string]any {
	legacy := map[string]any{
		"statuses_count":   statuses,
		"followers_count":  250,
		"friends_count":    42,
		"protected":        protected,
		"pinned_tweet_ids": pinned,
	}
	if len(pinned) > 0 {
		legacy["pinned_tweet_ids_str"] = pinned
	}
	return map[string]any{
		"__typename":       "User",
		"rest_id":          restID,
		"is_blue_verified": true,
		"core": map[string]any{
			"name":        name,
			"screen_name": screenName,
		},
		"avatar":      map[string]any{"image_url": avatar},
		"profile_bio": map[string]any{"description": "Synthetic bio"},
		"legacy":      legacy,
	}
}

func synthX024TweetObj(id, authorID, authorHandle, authorName, avatar, text string, extraLegacy map[string]any, extraTop map[string]any) map[string]any {
	legacy := map[string]any{
		"id_str":          id,
		"conversation_id": id,
		"created_at":      "Sat Nov 21 12:00:00 +0000 2026",
		"full_text":       text,
		"reply_count":     1,
		"retweet_count":   2,
		"favorite_count":  3,
		"quote_count":     1,
		"bookmark_count":  1,
	}
	for k, v := range extraLegacy {
		legacy[k] = v
	}
	obj := map[string]any{
		"__typename": "Tweet",
		"rest_id":    id,
		"core": map[string]any{
			"user_results": map[string]any{
				"result": synthX024UserObj(authorID, authorHandle, authorName, avatar, 50, false, nil),
			},
		},
		"legacy": legacy,
	}
	for k, v := range extraTop {
		obj[k] = v
	}
	return obj
}

func synthX024TweetEntry(entryID string, tweet map[string]any) map[string]any {
	return map[string]any{
		"entryId": entryID,
		"content": map[string]any{
			"entryType": "TimelineTimelineItem",
			"itemContent": map[string]any{
				"itemType":         "TimelineTweet",
				"tweetDisplayType": "Tweet",
				"tweet_results": map[string]any{
					"result": tweet,
				},
			},
		},
	}
}

func synthX024CursorEntry(entryID, cursorType, value string) map[string]any {
	return map[string]any{
		"entryId": entryID,
		"content": map[string]any{
			"entryType":  "TimelineTimelineCursor",
			"cursorType": cursorType,
			"value":      value,
		},
	}
}

func synthX024UserEntry(entryID string, user map[string]any) map[string]any {
	return map[string]any{
		"entryId": entryID,
		"content": map[string]any{
			"entryType": "TimelineTimelineItem",
			"itemContent": map[string]any{
				"itemType": "TimelineUser",
				"user_results": map[string]any{
					"result": user,
				},
			},
		},
	}
}

func writeJSONWithRateLimit(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("x-rate-limit-limit", "150")
	w.Header().Set("x-rate-limit-remaining", "120")
	w.Header().Set("x-rate-limit-reset", "1795328100")
	raw, _ := json.Marshal(payload)
	_, _ = w.Write(raw)
}

func setupAct024MockEnv(t *testing.T, opts act024MockOpts) *act024MockEnv {
	t.Helper()
	t.Setenv("TRACE_TEST_X_COOKIE_FILE", "")
	if opts.targetMode == "" {
		opts.targetMode = "full"
	}

	rootDir := t.TempDir()
	env := &act024MockEnv{rootDir: rootDir}

	mp4Data := buildSyntheticProgressiveMP4(t, 2, 0)
	pngData := buildSyntheticPNGBytes(t, 32, 24)

	homeFile := "webpack_home.html"
	mainFile := "webpack_main.js"
	ondemandFile := "webpack_ondemand.js"
	mainPath := "/responsive-web/client-web/main.synth01a.js"
	ondemandPath := "/responsive-web/client-web/ondemand.s.synth02a.js"
	if opts.useVite {
		homeFile = "vite_home.html"
		mainFile = "vite_main.js"
		ondemandFile = "vite_ondemand.js"
		mainPath = "/responsive-web/x-web/assets/main-vite01.js"
		ondemandPath = "/responsive-web/x-web/assets/ondemand.s-vite02.js"
	}

	homeBytes := readXTestdataFile(t, homeFile)
	mainBytes := readXTestdataFile(t, mainFile)
	ondemandBytes := readXTestdataFile(t, ondemandFile)
	if opts.includeFolderOp {
		mainBytes = append(mainBytes, []byte("\n/* optional */ var bf={queryId:\"SynthQidBookmarkFolders08\",operationName:\"BookmarkFoldersSlice\",operationType:\"query\",metadata:{featureSwitches:[\"responsive_web_graphql_exclude_directive_enabled\"]}};\n")...)
	}

	var activeCT0 atomic.Value
	activeCT0.Store(synthX024CT0)

	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if env.hook != nil && env.hook(w, r) {
			return
		}
		p := r.URL.Path
		base := env.srv.URL

		// Frontend discovery resources
		switch p {
		case "/home":
			env.frontendHits.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(homeBytes)
			return
		case mainPath:
			env.frontendHits.Add(1)
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				env.assetCookieLeaked.Store(true)
			}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(mainBytes)
			return
		case ondemandPath:
			env.frontendHits.Add(1)
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				env.assetCookieLeaked.Store(true)
			}
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(ondemandBytes)
			return
		}

		// Short-link expansion (NO Cookie allowed)
		if strings.HasPrefix(p, "/tco/") {
			env.shortHits.Add(1)
			if r.Header.Get("Cookie") != "" {
				env.shortLinkCookied.Store(true)
			}
			if opts.externalTcoTarget {
				w.Header().Set("Location", "https://example.org/external-article")
			} else {
				w.Header().Set("Location", base+"/"+synthX024CreatorHandle+"/status/"+synthX024PostTcoID)
			}
			w.WriteHeader(http.StatusFound)
			return
		}

		// HLS playlist sentinel (MUST NEVER be requested!)
		if strings.HasSuffix(strings.ToLower(p), ".m3u8") {
			env.hlsHits.Add(1)
			w.WriteHeader(http.StatusGone)
			return
		}

		// Media CDN endpoints (NO Cookie, NO Referer)
		if strings.HasPrefix(p, "/cdn/") {
			env.mediaHits.Add(1)
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
				env.cdnCookieLeaked.Store(true)
			}
			if r.Header.Get("Referer") != "" || r.Header.Get("Origin") != "" {
				env.cdnRefererLeaked.Store(true)
			}
			if strings.HasSuffix(p, ".mp4") {
				serveBytesWithRange(w, r, "video/mp4", mp4Data)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
			return
		}

		// REST & GraphQL API endpoints
		env.apiHits.Add(1)
		ck := r.Header.Get("Cookie")
		csrf := r.Header.Get("x-csrf-token")
		curCT0, _ := activeCT0.Load().(string)
		hasAuth := strings.Contains(ck, "auth_token="+synthX024AuthToken)

		switch {
		case p == "/i/api/1.1/account/settings.json":
			if !hasAuth {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			if csrf != curCT0 {
				http.SetCookie(w, &http.Cookie{Name: "ct0", Value: curCT0, Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header."}]}`))
				return
			}
			writeJSONWithRateLimit(w, map[string]any{
				"screen_name": synthX024SelfHandle,
				"protected":   false,
				"language":    "en",
			})
			return

		case strings.HasSuffix(p, "/"+x.OpUserByScreenName):
			if !hasAuth {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			vars := r.URL.Query().Get("variables")
			switch {
			case strings.Contains(vars, synthX024CreatorHandle):
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": synthX024UserObj(synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", 128, false, []string{synthX024PostPinnedID}),
						},
					},
				})
			case strings.Contains(vars, synthX024ProtHandle):
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": synthX024UserObj(synthX024ProtRestID, synthX024ProtHandle, synthX024ProtName, base+"/cdn/prot_avatar.png", 5, true, nil),
						},
					},
				})
			default:
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": synthX024UserObj(synthX024SelfRestID, synthX024SelfHandle, synthX024SelfName, base+"/cdn/self_avatar.png", 12, false, nil),
						},
					},
				})
			}
			return

		case strings.HasSuffix(p, "/"+x.OpUserByRestId):
			if !hasAuth {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			vars := r.URL.Query().Get("variables")
			switch {
			case strings.Contains(vars, synthX024MissingUserID):
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{"user": nil},
					"errors": []map[string]any{
						{"code": 50, "message": "User not found."},
					},
				})
			case strings.Contains(vars, synthX024ProtRestID):
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": synthX024UserObj(synthX024ProtRestID, synthX024ProtHandle, synthX024ProtName, base+"/cdn/prot_avatar.png", 5, true, nil),
						},
					},
				})
			default:
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": synthX024UserObj(synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", 128, false, []string{synthX024PostPinnedID}),
						},
					},
				})
			}
			return

		case strings.HasSuffix(p, "/"+x.OpTweetDetail):
			if !hasAuth {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			vars := r.URL.Query().Get("variables")
			var parsedVars struct {
				FocalTweetID string `json:"focalTweetId"`
			}
			_ = json.Unmarshal([]byte(vars), &parsedVars)
			fid := parsedVars.FocalTweetID
			env.recordFetchedDetailID(fid)

			switch fid {
			case synthX024MissingPostID:
				writeJSONWithRateLimit(w, map[string]any{
					"errors": []map[string]any{
						{"code": 144, "message": "No status found with that ID."},
					},
				})
				return

			case synthX024PostVideoID:
				var variants []map[string]any
				if opts.hlsOnlyVideo {
					variants = []map[string]any{
						{"content_type": "application/x-mpegURL", "url": base + "/cdn/stream.m3u8?" + synthX024SignedQS},
					}
				} else {
					variants = []map[string]any{
						{"content_type": "application/x-mpegURL", "url": base + "/cdn/stream.m3u8?" + synthX024SignedQS},
						{"content_type": "video/mp4", "bitrate": 2176000, "url": base + "/cdn/video_1280x720.mp4?" + synthX024SignedQS},
					}
				}
				tw := synthX024TweetObj(synthX024PostVideoID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", synthX024VideoPostText,
					map[string]any{
						"extended_entities": map[string]any{
							"media": []map[string]any{
								{
									"media_key":       "7_1800000000000000101",
									"type":            "video",
									"media_url_https": base + "/cdn/vcover.png",
									"original_info":   map[string]any{"width": 1280, "height": 720},
									"video_info": map[string]any{
										"duration_millis": 2000,
										"variants":        variants,
									},
								},
							},
						},
					}, nil)
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"threaded_conversation_with_injections_v2": map[string]any{
							"instructions": []map[string]any{
								{"type": "TimelineAddEntries", "entries": []map[string]any{synthX024TweetEntry("tweet-"+synthX024PostVideoID, tw)}},
							},
						},
					},
				})
				return

			case synthX024PostDelOrigID:
				tw := synthX024TweetObj(synthX024PostDelOrigID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", synthX024DelOrigText,
					map[string]any{"is_quote_status": true},
					map[string]any{
						"quoted_status_result": map[string]any{
							"result": map[string]any{
								"__typename": "TweetTombstone",
								"tombstone": map[string]any{
									"text": map[string]any{"text": "This Post was deleted by the Post author."},
								},
							},
						},
					})
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"threaded_conversation_with_injections_v2": map[string]any{
							"instructions": []map[string]any{
								{"type": "TimelineAddEntries", "entries": []map[string]any{synthX024TweetEntry("tweet-"+synthX024PostDelOrigID, tw)}},
							},
						},
					},
				})
				return

			default: // synthX024PostTcoID or other posts
				extraLegacy := map[string]any{}
				if !opts.omitTcoInPost {
					extraLegacy["entities"] = map[string]any{
						"urls": []map[string]any{
							{
								"url":          base + "/tco/" + synthX024TcoCode,
								"expanded_url": "https://example.org/external-article",
								"display_url":  "example.org/external-article",
							},
						},
					}
				}
				extraLegacy["extended_entities"] = map[string]any{
					"media": []map[string]any{
						{
							"media_key":       "3_1800000000000000105",
							"type":            "photo",
							"media_url_https": base + "/cdn/photo1.png?format=png&name=orig",
							"original_info":   map[string]any{"width": 32, "height": 24},
						},
					},
				}
				tw := synthX024TweetObj(fid, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", synthX024TcoPostText,
					extraLegacy,
					map[string]any{
						"note_tweet": map[string]any{
							"note_tweet_results": map[string]any{
								"result": map[string]any{
									"text": synthX024TcoPostText + " 完整长帖展开内容",
								},
							},
						},
					})
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"threaded_conversation_with_injections_v2": map[string]any{
							"instructions": []map[string]any{
								{"type": "TimelineAddEntries", "entries": []map[string]any{synthX024TweetEntry("tweet-"+fid, tw)}},
							},
						},
					},
				})
				return
			}

		case strings.HasSuffix(p, "/"+x.OpUserTweets):
			if !hasAuth {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
				return
			}
			vars := r.URL.Query().Get("variables")
			if strings.Contains(vars, "cursor_tl_p2") {
				tw3 := synthX024TweetObj(synthX024PostTL3ID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", "时间线第二页帖子文本DELTA024", nil, nil)
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": map[string]any{
								"__typename": "User",
								"timeline_v2": map[string]any{
									"timeline": map[string]any{
										"instructions": []map[string]any{
											{
												"type": "TimelineAddEntries",
												"entries": []map[string]any{
													synthX024TweetEntry("tweet-"+synthX024PostTL3ID, tw3),
													synthX024CursorEntry("cursor-bottom-2", "Bottom", "0|cursor_end"),
												},
											},
										},
									},
								},
							},
						},
					},
				})
				return
			}
			twPinned := synthX024TweetObj(synthX024PostPinnedID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", "置顶帖子文本EPSILON024", nil, nil)
			tw1 := synthX024TweetObj(synthX024PostTL1ID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", "时间线第一条带图帖子ZETA024",
				map[string]any{
					"extended_entities": map[string]any{
						"media": []map[string]any{
							{
								"media_key":       "3_1800000000000000205",
								"type":            "photo",
								"media_url_https": base + "/cdn/tl_photo.png?format=png&name=orig",
								"original_info":   map[string]any{"width": 32, "height": 24},
							},
						},
					},
				}, nil)
			tw2 := synthX024TweetObj(synthX024PostTL2ID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", "时间线第二条帖子ETA024", nil, nil)
			writeJSONWithRateLimit(w, map[string]any{
				"data": map[string]any{
					"user": map[string]any{
						"result": map[string]any{
							"__typename": "User",
							"timeline_v2": map[string]any{
								"timeline": map[string]any{
									"instructions": []map[string]any{
										{"type": "TimelinePinEntry", "entry": synthX024TweetEntry("tweet-"+synthX024PostPinnedID, twPinned)},
										{
											"type": "TimelineAddEntries",
											"entries": []map[string]any{
												synthX024TweetEntry("tweet-"+synthX024PostTL1ID, tw1),
												synthX024TweetEntry("tweet-"+synthX024PostTL2ID, tw2),
												synthX024CursorEntry("cursor-bottom-1", "Bottom", "cursor_tl_p2"),
											},
										},
									},
								},
							},
						},
					},
				},
			})
			return

		case strings.HasSuffix(p, "/"+x.OpFollowing):
			vars := r.URL.Query().Get("variables")
			if strings.Contains(vars, "cursor_f_p2") {
				u2 := synthX024UserObj(synthX024Follow2RestID, synthX024Follow2Handle, "合成关注用户二", base+"/cdn/f2.png", 9, false, nil)
				writeJSONWithRateLimit(w, map[string]any{
					"data": map[string]any{
						"user": map[string]any{
							"result": map[string]any{
								"__typename": "User",
								"timeline": map[string]any{
									"timeline": map[string]any{
										"instructions": []map[string]any{
											{
												"type": "TimelineAddEntries",
												"entries": []map[string]any{
													synthX024UserEntry("user-"+synthX024Follow2RestID, u2),
													synthX024CursorEntry("cursor-bottom-f2", "Bottom", "0|f_end"),
												},
											},
										},
									},
								},
							},
						},
					},
				})
				return
			}
			u1 := synthX024UserObj(synthX024Follow1RestID, synthX024Follow1Handle, "合成关注用户一", base+"/cdn/f1.png", 15, false, nil)
			writeJSONWithRateLimit(w, map[string]any{
				"data": map[string]any{
					"user": map[string]any{
						"result": map[string]any{
							"__typename": "User",
							"timeline": map[string]any{
								"timeline": map[string]any{
									"instructions": []map[string]any{
										{
											"type": "TimelineAddEntries",
											"entries": []map[string]any{
												synthX024UserEntry("user-"+synthX024Follow1RestID, u1),
												synthX024CursorEntry("cursor-bottom-f1", "Bottom", "cursor_f_p2"),
											},
										},
									},
								},
							},
						},
					},
				},
			})
			return

		case strings.HasSuffix(p, "/"+x.OpBookmarks):
			twBM := synthX024TweetObj(synthX024PostBookmarkID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", "书签合成帖子THETA024", nil, nil)
			writeJSONWithRateLimit(w, map[string]any{
				"data": map[string]any{
					"bookmark_timeline_v2": map[string]any{
						"timeline": map[string]any{
							"instructions": []map[string]any{
								{
									"type": "TimelineAddEntries",
									"entries": []map[string]any{
										synthX024TweetEntry("tweet-"+synthX024PostBookmarkID, twBM),
										synthX024CursorEntry("cursor-bottom-bm", "Bottom", "0|bm_end"),
									},
								},
							},
						},
					},
				},
			})
			return

		case strings.HasSuffix(p, "/"+x.OpLikes):
			twLk := synthX024TweetObj(synthX024PostLikeID, synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName, base+"/cdn/creator_avatar.png", "喜欢合成帖子IOTA024", nil, nil)
			writeJSONWithRateLimit(w, map[string]any{
				"data": map[string]any{
					"user": map[string]any{
						"result": map[string]any{
							"__typename": "User",
							"timeline_v2": map[string]any{
								"timeline": map[string]any{
									"instructions": []map[string]any{
										{
											"type": "TimelineAddEntries",
											"entries": []map[string]any{
												synthX024TweetEntry("tweet-"+synthX024PostLikeID, twLk),
												synthX024CursorEntry("cursor-bottom-lk", "Bottom", "0|lk_end"),
											},
										},
									},
								},
							},
						},
					},
				},
			})
			return

		case strings.HasSuffix(p, "/"+act024OpBookmarkFolders):
			// Simulate non-Premium empty bookmark_collections_slice gracefully.
			writeJSONWithRateLimit(w, map[string]any{
				"data": map[string]any{
					"viewer": map[string]any{
						"user_results": map[string]any{
							"result": map[string]any{
								"bookmark_collections_slice": map[string]any{
									"items": []any{},
								},
							},
						},
					},
				},
			})
			return

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(env.srv.Close)

	secDir := filepath.Join(rootDir, ".local", "site-secrets")
	checkDir := filepath.Join(rootDir, ".local", "site-check")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "x.cookie"), []byte(synthX024CookieRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env.local"), []byte("TRACE_TEST_X_COOKIE_FILE=.local/site-secrets/x.cookie\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if opts.preUsedLogin > 0 {
		preUsage := sitecheck.Usage{
			ActID:      act024ID,
			UpdatedAt:  "2026-11-22T12:00:00Z",
			Runs:       0,
			APICalls:   opts.preUsedLogin,
			Categories: map[string]int{"login": opts.preUsedLogin},
		}
		rawPre, _ := json.MarshalIndent(preUsage, "", "  ")
		if err := os.WriteFile(filepath.Join(checkDir, "usage-act024.json"), rawPre, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var tg act024Targets
	switch opts.targetMode {
	case "empty":
		tg = act024Targets{
			Version:        1,
			Act:            "ACT-024",
			ProvidedAt:     "2026-11-22",
			Note:           "empty targets",
			Users:          []act024TargetItem{},
			Posts:          []act024TargetItem{},
			ProtectedUsers: []act024TargetItem{},
			ShortLinks:     []string{},
			Missing:        nil,
		}
	case "real_shape_no_missing":
		// Matches real .local/site-check/targets-x.json: 1 user, 5 posts, empty protectedUsers/shortLinks, nil missing.
		tg = act024Targets{
			Version:    1,
			Act:        "ACT-024",
			ProvidedAt: "2026-11-22",
			Note:       "real shape without protected or missing",
			Users: []act024TargetItem{
				{URL: env.srv.URL + "/" + synthX024CreatorHandle, Expect: []string{"active", "pinned", "retweet", "images"}},
			},
			Posts: []act024TargetItem{
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostVideoID, Expect: []string{"video"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostDelOrigID, Expect: []string{"deletedOrigin"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostRTQuoteID, Expect: []string{"retweetOrQuote"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostQuoteID, Expect: []string{"quote"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostTcoID, Expect: []string{"tcoLink"}},
			},
			ProtectedUsers: []act024TargetItem{},
			ShortLinks:     []string{},
			Missing:        nil,
		}
	default: // "full"
		tg = act024Targets{
			Version:    1,
			Act:        "ACT-024",
			ProvidedAt: "2026-11-22",
			Note:       "full synthetic ACT-024 targets",
			Users: []act024TargetItem{
				{URL: env.srv.URL + "/" + synthX024CreatorHandle, Expect: []string{"active", "pinned", "retweet", "images"}},
			},
			Posts: []act024TargetItem{
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostVideoID, Expect: []string{"video"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostDelOrigID, Expect: []string{"deletedOrigin"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostRTQuoteID, Expect: []string{"retweetOrQuote"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostQuoteID, Expect: []string{"quote"}},
				{URL: env.srv.URL + "/" + synthX024CreatorHandle + "/status/" + synthX024PostTcoID, Expect: []string{"tcoLink"}},
			},
			ProtectedUsers: []act024TargetItem{
				{URL: env.srv.URL + "/" + synthX024ProtHandle, Expect: []string{"protected"}},
			},
			ShortLinks: []string{}, // Intentionally empty to exercise t.co extraction from tcoLink post!
			Missing: &act024MissingTarget{
				UserID:   synthX024MissingUserID,
				StatusID: synthX024MissingPostID,
			},
		}
	}
	rawTG, _ := json.MarshalIndent(tg, "", "  ")
	if err := os.WriteFile(filepath.Join(checkDir, "targets-x.json"), rawTG, 0o600); err != nil {
		t.Fatal(err)
	}

	env.endpoints = x.Endpoints{
		HomeURL:         env.srv.URL + "/home",
		AccountSettings: env.srv.URL + "/i/api/1.1/account/settings.json",
		GraphQLBase:     env.srv.URL + "/i/api/graphql",
	}
	env.assetRT = newAct024AbsRewriteTransport(t, env.srv)
	return env
}

func fixedAct024TxIDOpts() []x.TxIDOption {
	fixedNow := time.Unix(1795327200, 0).UTC()
	return []x.TxIDOption{
		x.WithTxIDClock(func() time.Time { return fixedNow }),
		x.WithTxIDRandByte(func() (byte, error) { return 0x42, nil }),
	}
}

func TestAct024PreExistingLedgerPreservedAndOverBudgetBlocked(t *testing.T) {
	sum := 0
	for _, v := range act024APILimits {
		sum += v
	}
	if sum != act024MaxTotalRequests {
		t.Fatalf("act024APILimits sum = %d, want %d", sum, act024MaxTotalRequests)
	}

	dir := t.TempDir()
	pre := sitecheck.Usage{
		ActID:      act024ID,
		UpdatedAt:  "2026-11-22T12:00:00Z",
		Runs:       0,
		APICalls:   1,
		Categories: map[string]int{"login": 1},
	}
	rawPre, _ := json.MarshalIndent(pre, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "usage-act024.json"), rawPre, 0o600); err != nil {
		t.Fatal(err)
	}

	gate, err := sitecheck.OpenGate(dir, sitecheck.Limits{
		ActID:         act024ID,
		MaxRuns:       act024MaxRuns,
		MaxAPICalls:   act024MaxTotalRequests,
		MaxMediaFiles: act024MaxMediaRequests,
		MaxMediaBytes: act024MaxMediaBytes,
		MaxByCategory: act024APILimits,
	})
	if err != nil {
		t.Fatalf("OpenGate: %v", err)
	}
	if u := gate.Usage(); u.Runs != 0 || u.APICalls != 1 || u.Categories["login"] != 1 {
		t.Fatalf("OpenGate did not preserve preflight counts: %+v", u)
	}

	var hits atomic.Int64
	ck := &act024Checker{
		gate:           gate,
		apiLimits:      act024APILimits,
		shortLink:      &mediaBudget{name: "shortlink", maxRequests: act024MaxShortLinkHops, maxBytes: act024MaxShortLinkBytes},
		media:          &mediaBudget{name: "media", maxRequests: act024MaxMediaRequests, maxBytes: act024MaxMediaBytes},
		endpoints:      x.DefaultEndpoints(),
		statuses:       map[string]int{},
		opCounts:       map[string]int{},
		rateLimits:     map[string]act024RateLimitSnapshot{},
		cdnHostClasses: map[string]int{},
	}

	okRT := roundTrip(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &nopCloser{bytes.NewReader([]byte(`{"ok":true}`))},
		}, nil
	})
	apiRT := ck.wrap(okRT)
	mediaRT := ck.wrapMedia(okRT)

	// 1. Disallowed write/forbidden endpoints must fail BEFORE upstream (0 hits).
	ck.setPhase("postDetail")
	forbidden := []struct{ method, rawURL string }{
		{http.MethodPost, "https://x.com/i/api/graphql/Qid1/CreateTweet"},
		{http.MethodPost, "https://x.com/i/api/graphql/Qid1/TweetDetail"},
		{http.MethodGet, "https://x.com/i/api/graphql/Qid1/SearchTimeline"},
		{http.MethodGet, "https://x.com/i/api/graphql/Qid1/BroadcastQuery"},
		{http.MethodGet, "https://x.com/i/api/graphql/Qid1/AudioSpaceById"},
		{http.MethodGet, "https://x.com/i/api/graphql/Qid1/UserMedia"},
		{http.MethodGet, "https://x.com/i/api/graphql/Qid1/UserTweetsAndReplies"},
		{http.MethodGet, "https://x.com/i/api/graphql/Qid1/TweetResultByRestId"},
		{http.MethodGet, "https://api.x.com/2/tweets/1800000000000000101"},
		{http.MethodGet, "https://x.com/i/flow/login"},
		{http.MethodGet, "https://x.com/i/api/1.1/onboarding/task.json"},
		{http.MethodGet, "https://x.com/i/api/1.1/account/settings.json"}, // wrong phase!
		{http.MethodGet, "https://pbs.twimg.com/profile_images/1/avatar.jpg"},
	}
	for _, tc := range forbidden {
		req, _ := http.NewRequest(tc.method, tc.rawURL, nil)
		if _, err := apiRT.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("%s %s err = %v, want ErrQuotaExceeded", tc.method, tc.rawURL, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("forbidden endpoints hit upstream: %d", hits.Load())
	}

	// 2. Auth headers on abs.twimg.com scripts, t.co, and media CDN (or Referer on CDN) MUST be refused BEFORE upstream.
	ck.setPhase("frontend")
	reqScriptCookie, _ := http.NewRequest(http.MethodGet, "https://abs.twimg.com/responsive-web/client-web/main.abc1.js", nil)
	reqScriptCookie.Header.Set("Cookie", "auth_token="+synthX024AuthToken)
	if _, err := apiRT.RoundTrip(reqScriptCookie); err == nil {
		t.Fatal("expected error when Cookie sent to abs.twimg.com script")
	}

	ck.setPhase("shortlink")
	reqShortCookie, _ := http.NewRequest(http.MethodGet, "https://t.co/Synth01", nil)
	reqShortCookie.Header.Set("Cookie", "auth_token="+synthX024AuthToken)
	if _, err := apiRT.RoundTrip(reqShortCookie); err == nil {
		t.Fatal("expected error when Cookie sent to t.co")
	}

	ck.setPhase("media")
	reqCDNCookie, _ := http.NewRequest(http.MethodGet, "https://video.twimg.com/ext_tw_video/1/vid.mp4", nil)
	reqCDNCookie.Header.Set("Cookie", "auth_token="+synthX024AuthToken)
	if _, err := mediaRT.RoundTrip(reqCDNCookie); err == nil {
		t.Fatal("expected error when Cookie sent to media CDN")
	}
	reqCDNRef, _ := http.NewRequest(http.MethodGet, "https://pbs.twimg.com/media/abc?format=jpg&name=orig", nil)
	reqCDNRef.Header.Set("Referer", "https://x.com/")
	if _, err := mediaRT.RoundTrip(reqCDNRef); err == nil {
		t.Fatal("expected error when Referer sent to media CDN")
	}
	if hits.Load() != 0 {
		t.Fatalf("cookied/referer script, shortlink, or CDN hit upstream: %d", hits.Load())
	}

	// 3. Pre-existing login=1 leaves ONLY 4 new login calls (total 5); 5th new login call MUST fail BEFORE upstream!
	ck.setPhase("login")
	for i := 0; i < 4; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://x.com/i/api/1.1/account/settings.json", nil)
		if _, err := apiRT.RoundTrip(req); err != nil {
			t.Fatalf("login call %d failed: %v", i+1, err)
		}
	}
	if hits.Load() != 4 {
		t.Fatalf("hits after 4 login calls = %d, want 4", hits.Load())
	}
	reqOverLogin, _ := http.NewRequest(http.MethodGet, "https://x.com/i/api/1.1/account/settings.json", nil)
	if _, err := apiRT.RoundTrip(reqOverLogin); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("5th new login call err = %v, want ErrQuotaExceeded", err)
	}
	if hits.Load() != 4 {
		t.Fatalf("over-budget login call hit upstream (%d), want 4", hits.Load())
	}

	// 4. Exhaust remaining categories to verify 32 total cap (1 preflight + 31 new) blocks 33rd BEFORE upstream!
	// Already used: login=5 (1 preflight + 4 new). Remaining = 27 across frontend(3), shortlink(3),
	// unauthControl(1), postDetail(3), userTimeline(4), errorProbe(2), mine(5), media(5), csrfReplay(1).
	batches := []struct {
		phase string
		url   string
		count int
		media bool
	}{
		{"frontend", "https://abs.twimg.com/responsive-web/client-web/main.abc1.js", 3, false},
		{"shortlink", "https://t.co/Synth01", 3, false},
		{"unauthControl", "https://x.com/i/api/graphql/Qid1/TweetDetail", 1, false},
		{"postDetail", "https://x.com/i/api/graphql/Qid1/TweetDetail", 3, false},
		{"userTimeline", "https://x.com/i/api/graphql/Qid1/UserTweets", 4, false},
		{"errorProbe", "https://x.com/i/api/graphql/Qid1/UserByRestId", 2, false},
		{"mine", "https://x.com/i/api/graphql/Qid1/Following", 5, false},
		{"media", "https://pbs.twimg.com/media/abc?format=jpg&name=orig", 5, true},
	}
	for _, b := range batches {
		ck.setPhase(b.phase)
		for i := 0; i < b.count; i++ {
			req, _ := http.NewRequest(http.MethodGet, b.url, nil)
			var rErr error
			if b.media {
				_, rErr = mediaRT.RoundTrip(req)
			} else {
				_, rErr = apiRT.RoundTrip(req)
			}
			if rErr != nil {
				t.Fatalf("batch %s #%d failed: %v", b.phase, i+1, rErr)
			}
		}
		// One extra in each category must fail with ErrQuotaExceeded!
		reqExtra, _ := http.NewRequest(http.MethodGet, b.url, nil)
		var extraErr error
		if b.media {
			_, extraErr = mediaRT.RoundTrip(reqExtra)
		} else {
			_, extraErr = apiRT.RoundTrip(reqExtra)
		}
		if !errors.Is(extraErr, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("extra call in %s err = %v, want ErrQuotaExceeded", b.phase, extraErr)
		}
	}

	// Final 1 slot: csrfReplay (brings total APICalls to 32/32 = 1 preflight + 31 upstream hits).
	ck.setPhase("postDetail")
	ck.pendingCSRFReplay.Store(true)
	reqReplay, _ := http.NewRequest(http.MethodGet, "https://x.com/i/api/graphql/Qid1/TweetDetail", nil)
	if _, err := apiRT.RoundTrip(reqReplay); err != nil {
		t.Fatalf("csrfReplay call failed: %v", err)
	}
	if u := gate.Usage(); u.APICalls != 32 || hits.Load() != 31 {
		t.Fatalf("expected APICalls=32 hits=31, got APICalls=%d hits=%d", u.APICalls, hits.Load())
	}

	// 33rd total call (even with csrfReplay flag) MUST be blocked BEFORE upstream!
	ck.pendingCSRFReplay.Store(true)
	if _, err := apiRT.RoundTrip(reqReplay); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("33rd call err = %v, want ErrQuotaExceeded", err)
	}
	if hits.Load() != 31 {
		t.Fatalf("33rd over-budget call hit upstream (%d), want 31", hits.Load())
	}

	// 5. Frontend script > 8 MiB must fail!
	dir2 := t.TempDir()
	gate2, err := sitecheck.OpenGate(dir2, sitecheck.Limits{
		ActID: act024ID, MaxRuns: 1, MaxAPICalls: 32, MaxByCategory: act024APILimits,
	})
	if err != nil {
		t.Fatal(err)
	}
	ck2 := &act024Checker{
		gate:           gate2,
		apiLimits:      act024APILimits,
		shortLink:      &mediaBudget{name: "shortlink", maxRequests: 3, maxBytes: 192 << 10},
		media:          &mediaBudget{name: "media", maxRequests: 5, maxBytes: 1024},
		endpoints:      x.DefaultEndpoints(),
		statuses:       map[string]int{},
		opCounts:       map[string]int{},
		rateLimits:     map[string]act024RateLimitSnapshot{},
		cdnHostClasses: map[string]int{},
	}
	ck2.setPhase("frontend")
	bigScriptRT := ck2.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/javascript"}},
			Body:       &nopCloser{bytes.NewReader(make([]byte, act024MaxFrontendBytes+1))},
		}, nil
	}))
	reqBigScript, _ := http.NewRequest(http.MethodGet, "https://abs.twimg.com/responsive-web/client-web/main.big01.js", nil)
	if _, err := bigScriptRT.RoundTrip(reqBigScript); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("oversized >8MiB script err = %v, want ErrQuotaExceeded", err)
	}

	// 6. Media CDN > maxBytes must fail BEFORE next upstream call!
	ck2.setPhase("media")
	ck2.media.bytes.Store(1024)
	var mediaAfterByteCap atomic.Int64
	overBytesRT := ck2.wrapMedia(roundTrip(func(r *http.Request) (*http.Response, error) {
		mediaAfterByteCap.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	reqMediaOver, _ := http.NewRequest(http.MethodGet, "https://video.twimg.com/ext_tw_video/1/vid.mp4", nil)
	if _, err := overBytesRT.RoundTrip(reqMediaOver); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("over-byte-cap media err = %v, want ErrQuotaExceeded", err)
	}
	if mediaAfterByteCap.Load() != 0 {
		t.Fatalf("over-byte-cap media hit upstream: %d", mediaAfterByteCap.Load())
	}
}

func TestAct024StopConditionsHaltImmediatelyWithZeroFurtherRequests(t *testing.T) {
	cases := []struct {
		name      string
		wantStop  string
		triggerAt string // "TweetDetail" or "UserTweets"
		respondFn func(w http.ResponseWriter, r *http.Request, count int)
	}{
		{
			name: "http_429", wantStop: "http_429", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
			},
		},
		{
			name: "x_error_88_http_200", wantStop: "x_error_88", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`))
			},
		},
		{
			name: "http_403_code_64", wantStop: "x_error_64", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":64,"message":"Your account is suspended"}]}`))
			},
		},
		{
			name: "http_403_code_226", wantStop: "x_error_226", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":226,"message":"This request looks like it might be automated"}]}`))
			},
		},
		{
			name: "http_403_code_326", wantStop: "x_error_326", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":326,"message":"To protect our users from spam, this account is temporarily locked"}]}`))
			},
		},
		{
			name: "challenge_html_arkose", wantStop: "challenge_html_page", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<html><title>ArkoseLabs FunCaptcha Challenge</title><body><div id="arkoselabs"></div></body></html>`))
			},
		},
		{
			name: "challenge_html_turnstile", wantStop: "challenge_html_page", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<html><title>Just a moment...</title><body><div class="cf-turnstile"></div></body></html>`))
			},
		},
		{
			name: "login_redirect_flow", wantStop: "login_redirect", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Location", "https://x.com/i/flow/login")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "login_redirect_root", wantStop: "login_redirect", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Location", "https://x.com/login")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "auth_401_code_32", wantStop: "auth_error_32", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":32,"message":"Could not authenticate you."}]}`))
			},
		},
		{
			name: "auth_401_code_89", wantStop: "auth_error_89", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"code":89,"message":"Invalid or expired token."}]}`))
			},
		},
		{
			name: "fake_empty_user_shell", wantStop: "fake_empty_user_shell", triggerAt: x.OpUserTweets,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{"user":{}}}`))
			},
		},
		{
			name: "csrf_353_without_new_ct0", wantStop: "csrf_353_no_new_ct0", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"csrf mismatch"}]}`))
			},
		},
		{
			name: "unexpected_second_353", wantStop: "unexpected_second_353", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, count int) {
				if count == 1 {
					http.SetCookie(w, &http.Cookie{Name: "ct0", Value: synthX024CT0Rotated, Path: "/"})
				} else {
					http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "11223344556677889900aabbccddeeff", Path: "/"})
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"csrf mismatch"}]}`))
			},
		},
		{
			name: "rate_limit_remaining_1", wantStop: "rate_limit_remaining_low", triggerAt: x.OpTweetDetail,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-rate-limit-limit", "150")
				w.Header().Set("x-rate-limit-remaining", "1")
				w.Header().Set("x-rate-limit-reset", "1795328100")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{"threaded_conversation_with_injections_v2":{"instructions":[]}}}`))
			},
		},
		{
			name: "rate_limit_remaining_0", wantStop: "rate_limit_remaining_low", triggerAt: x.OpUserTweets,
			respondFn: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-rate-limit-limit", "50")
				w.Header().Set("x-rate-limit-remaining", "0")
				w.Header().Set("x-rate-limit-reset", "1795328100")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{"user":{"result":{"__typename":"User","timeline_v2":{"timeline":{"instructions":[]}}}}}}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1})
			var stopTripped atomic.Bool
			var hitsAfterStop atomic.Int64
			var triggerCount atomic.Int32

			env.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if stopTripped.Load() {
					hitsAfterStop.Add(1)
					return false
				}
				if strings.HasSuffix(r.URL.Path, "/"+tc.triggerAt) && r.Header.Get("Cookie") != "" {
					n := int(triggerCount.Add(1))
					if tc.name == "unexpected_second_353" {
						if n >= 2 {
							stopTripped.Store(true)
						}
					} else {
						stopTripped.Store(true)
					}
					tc.respondFn(w, r, n)
					return true
				}
				return false
			}

			outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-"+tc.name+".json")
			rc, err := runAct024(context.Background(), act024Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				MinSpacing:     -1,
				JitterMax:      -1,
				Endpoints:      env.endpoints,
				AssetTransport: env.assetRT,
				AllowLocalhost: true,
				TxIDOptions:    fixedAct024TxIDOpts(),
			})
			if err == nil || rc.Passed {
				t.Fatalf("%s: expected stopped/failed run, got err=%v passed=%v", tc.name, err, rc.Passed)
			}
			if rc.StoppedBy != tc.wantStop {
				t.Fatalf("%s: stoppedBy = %q, want %q", tc.name, rc.StoppedBy, tc.wantStop)
			}
			if !stopTripped.Load() {
				t.Fatalf("%s: stop trigger was never reached", tc.name)
			}
			if got := hitsAfterStop.Load(); got != 0 {
				t.Fatalf("%s: sent %d requests AFTER stop condition tripped, want 0", tc.name, got)
			}
			if !rc.TempDataDeleted {
				t.Fatalf("%s: temp data dir was not deleted", tc.name)
			}
			if !rc.SecretScanClean {
				t.Fatalf("%s: receipt failed secret scan", tc.name)
			}
			rawBytes, rErr := os.ReadFile(outPath)
			if rErr != nil {
				t.Fatalf("%s: expected receipt file on disk: %v", tc.name, rErr)
			}
			if findings := sitecheck.ScanTextForSecrets(string(rawBytes)); len(findings) > 0 {
				t.Fatalf("%s: receipt failed ScanTextForSecrets: %v", tc.name, findings)
			}
		})
	}
}

func TestAct024EndToEndSyntheticRunFormalCacheAndSecretScan(t *testing.T) {
	for _, tc := range []struct {
		name           string
		useVite        bool
		wantBundleKind string
	}{
		{"webpack_5posts_preflight_and_formal_cache", false, "webpack"},
		{"vite_5posts_preflight_and_formal_cache", true, "vite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAct024MockEnv(t, act024MockOpts{
				preUsedLogin:    1,
				useVite:         tc.useVite,
				includeFolderOp: true,
				targetMode:      "full",
			})
			outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024.json")

			rc, err := runAct024(context.Background(), act024Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				MinSpacing:     -1,
				JitterMax:      -1,
				Endpoints:      env.endpoints,
				AssetTransport: env.assetRT,
				AllowLocalhost: true,
				TxIDOptions:    fixedAct024TxIDOpts(),
			})
			if err != nil {
				t.Fatalf("runAct024 failed: %v (stoppedBy=%q steps=%+v)", err, rc.StoppedBy, rc.Steps)
			}
			if !rc.Passed || rc.StoppedBy != "" || !rc.TempDataDeleted || !rc.SecretScanClean {
				t.Fatalf("unexpected receipt summary: passed=%v stoppedBy=%q tempDel=%v secretClean=%v",
					rc.Passed, rc.StoppedBy, rc.TempDataDeleted, rc.SecretScanClean)
			}
			if env.assetCookieLeaked.Load() || env.shortLinkCookied.Load() || env.cdnCookieLeaked.Load() || env.cdnRefererLeaked.Load() {
				t.Fatalf("auth/referer leak detected: asset=%v short=%v cdnCookie=%v cdnRef=%v",
					env.assetCookieLeaked.Load(), env.shortLinkCookied.Load(), env.cdnCookieLeaked.Load(), env.cdnRefererLeaked.Load())
			}

			// Ledger preservation & strict caps
			if rc.PreUsedRequests != 1 || rc.PreUsedLoginCalls != 1 {
				t.Fatalf("preflight accounting mismatch: preReq=%d preLogin=%d", rc.PreUsedRequests, rc.PreUsedLoginCalls)
			}
			if rc.APIUsage.Runs != 1 || rc.APIUsage.Categories["login"] != 5 || rc.APIUsage.APICalls > act024MaxTotalRequests {
				t.Fatalf("usage mismatch: %+v", rc.APIUsage)
			}
			if rc.APIUsage.Categories["frontend"] != 3 {
				t.Fatalf("frontend calls = %d, want 3", rc.APIUsage.Categories["frontend"])
			}
			if rc.APIUsage.Categories["postDetail"] != 3 {
				t.Fatalf("postDetail calls = %d, want 3", rc.APIUsage.Categories["postDetail"])
			}

			// Verify 5-post priority selector picked [video, deletedOrigin, tcoLink] for TweetDetail
			detailIDs := env.getFetchedDetailIDs()
			if len(detailIDs) < 3 {
				t.Fatalf("fetchedDetailIDs = %v, want >= 3", detailIDs)
			}
			wantFirstThree := []string{synthX024PostVideoID, synthX024PostDelOrigID, synthX024PostTcoID}
			for i, wantID := range wantFirstThree {
				if detailIDs[i] != wantID {
					t.Fatalf("detail[%d] = %s, want %s (all=%v)", i, detailIDs[i], wantID, detailIDs)
				}
			}

			// Verify V-01..V-09 and verdictHints are all populated
			for _, key := range []string{"V-01", "V-02", "V-03", "V-04", "V-05", "V-06", "V-07", "V-08", "V-09"} {
				if _, ok := rc.Verifications[key]; !ok {
					t.Fatalf("missing verification %s in receipt", key)
				}
				if rc.VerdictHints[key] != "pass" {
					t.Fatalf("verdictHint[%s] = %q, want pass (v=%+v)", key, rc.VerdictHints[key], rc.Verifications[key])
				}
			}

			v03, _ := rc.Verifications["V-03"].(map[string]any)
			if v03["shortLinkSource"] != "post_entity" || v03["shortLinkHops"] != 1 || v03["unauthRejected"] != true {
				t.Fatalf("unexpected V-03: %+v", v03)
			}
			v04, _ := rc.Verifications["V-04"].(map[string]any)
			if v04["bundleKind"] != tc.wantBundleKind || v04["frontendDiscoveryOK"] != true || v04["txidHeaderSentOnGraphQL"] != true {
				t.Fatalf("unexpected V-04: %+v", v04)
			}
			v07, _ := rc.Verifications["V-07"].(map[string]any)
			if v07["mp4ffValid"] != true || v07["range206"] != true || v07["tempCacheSecretScanClean"] != true || v07["cdnCookieless"] != true {
				t.Fatalf("unexpected V-07: %+v", v07)
			}
			v08, _ := rc.Verifications["V-08"].(map[string]any)
			if v08["followingPage2Fetched"] != true || v08["bookmarkFoldersStatus"] != "verified" || v08["bookmarkFoldersAvailable"] != true {
				t.Fatalf("unexpected V-08: %+v", v08)
			}

			// Read serialized act024.json and verify ZERO leaked secrets, URLs, IDs, handles, or post text!
			rawBytes, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read receipt: %v", err)
			}
			rawStr := string(rawBytes)
			if findings := sitecheck.ScanTextForSecrets(rawStr); len(findings) > 0 {
				t.Fatalf("receipt failed sitecheck.ScanTextForSecrets: %v", findings)
			}
			forbidden := []string{
				"http://", "https://",
				synthX024AuthToken, synthX024CT0, synthX024CT0Rotated, synthX024KDT, synthX024GuestID,
				"SYNTHETIC_WEB_BEARER_TOKEN_0001", "SYNTHETIC_VITE_BEARER_TOKEN_0002",
				"U1lOVEhFVElDX1hfVkVSSUZJQ0FUSU9OX0tFWV9GSVhUVVJFXzAwMDFfQUJDREVG",
				"U1lOVEhFVElDX1ZJVEVfVkVSSUZZX0tFWV9GSVhUVVJFXzAwMDJfMTIzNDU2Nzg5",
				"SynthQidTweetDetail04", "SynthViteQidTweetDetail04", "SynthQidBookmarkFolders08",
				"SynthXMediaSig024", "APKASYNTH024",
				synthX024SelfRestID, synthX024SelfHandle, synthX024SelfName,
				synthX024CreatorRestID, synthX024CreatorHandle, synthX024CreatorName,
				synthX024ProtRestID, synthX024ProtHandle, synthX024ProtName,
				synthX024Follow1RestID, synthX024Follow1Handle,
				synthX024Follow2RestID, synthX024Follow2Handle,
				synthX024MissingUserID, synthX024MissingPostID,
				synthX024PostVideoID, synthX024PostDelOrigID, synthX024PostTcoID,
				synthX024PostPinnedID, synthX024PostTL1ID, synthX024PostBookmarkID, synthX024PostLikeID,
				synthX024TcoCode,
				synthX024VideoPostText, synthX024DelOrigText, synthX024TcoPostText,
			}
			for _, f := range forbidden {
				if strings.Contains(rawStr, f) {
					t.Fatalf("receipt leaked forbidden string %q", f)
				}
			}

			// Second run on the same ledger MUST be rejected BEFORE any network call (MaxRuns=1).
			hitsBefore := env.apiHits.Load() + env.frontendHits.Load()
			if _, err := runAct024(context.Background(), act024Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				MinSpacing:     -1,
				JitterMax:      -1,
				Endpoints:      env.endpoints,
				AssetTransport: env.assetRT,
				AllowLocalhost: true,
				TxIDOptions:    fixedAct024TxIDOpts(),
			}); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
				t.Fatalf("second run err = %v, want ErrQuotaExceeded", err)
			}
			if got := env.apiHits.Load() + env.frontendHits.Load(); got != hitsBefore {
				t.Fatalf("second run sent %d calls after MaxRuns exhausted", got-hitsBefore)
			}
		})
	}
}

func TestAct024MissingTargetsReportedNotRun(t *testing.T) {
	t.Run("real_shape_empty_protected_shortlinks_and_nil_missing", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{
			preUsedLogin:  1,
			omitTcoInPost: true,
			targetMode:    "real_shape_no_missing",
		})
		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-realshape.json")

		rc, err := runAct024(context.Background(), act024Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		})
		if err != nil {
			t.Fatalf("runAct024 failed: %v (steps=%+v)", err, rc.Steps)
		}
		if !rc.Passed {
			t.Fatalf("expected passed=true")
		}
		if rc.APIUsage.Categories["errorProbe"] != 0 {
			t.Fatalf("expected 0 errorProbe calls when protectedUsers=[] and missing=nil, got %d", rc.APIUsage.Categories["errorProbe"])
		}
		if rc.APIUsage.Categories["shortlink"] != 0 {
			t.Fatalf("expected 0 shortlink calls when shortLinks=[] and no t.co entity, got %d", rc.APIUsage.Categories["shortlink"])
		}
		v09, _ := rc.Verifications["V-09"].(map[string]any)
		if v09["status"] != "not-run" || rc.VerdictHints["V-09"] != "not-run" {
			t.Fatalf("expected V-09 not-run, got v09=%+v hint=%q", v09, rc.VerdictHints["V-09"])
		}
		v03, _ := rc.Verifications["V-03"].(map[string]any)
		if v03["shortLinkStatus"] != "not-run" {
			t.Fatalf("expected V-03 shortLinkStatus=not-run, got %+v", v03)
		}
	})

	t.Run("all_targets_empty", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{
			preUsedLogin: 0,
			targetMode:   "empty",
		})
		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-empty.json")

		rc, err := runAct024(context.Background(), act024Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		})
		if err != nil {
			t.Fatalf("runAct024 with empty targets failed: %v (steps=%+v)", err, rc.Steps)
		}
		if !rc.Passed {
			t.Fatalf("expected passed=true when optional targets are absent")
		}
		for _, vKey := range []string{"V-03", "V-05", "V-06", "V-07", "V-09"} {
			m, _ := rc.Verifications[vKey].(map[string]any)
			if m["status"] != "not-run" || rc.VerdictHints[vKey] != "not-run" {
				t.Fatalf("verification %s = %+v (hint=%q), want not-run", vKey, m, rc.VerdictHints[vKey])
			}
		}
		for _, zeroCat := range []string{"shortlink", "unauthControl", "postDetail", "userTimeline", "errorProbe", "media"} {
			if rc.APIUsage.Categories[zeroCat] != 0 {
				t.Fatalf("expected 0 calls in %s for empty targets, got %d", zeroCat, rc.APIUsage.Categories[zeroCat])
			}
		}
	})
}

func TestAct024Single353ReplayWithFreshCT0SucceedsAndRedacts(t *testing.T) {
	env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "full"})
	var firedOnce atomic.Bool

	env.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/"+x.OpUserTweets) && !firedOnce.Swap(true) {
			http.SetCookie(w, &http.Cookie{Name: "ct0", Value: synthX024CT0Rotated, Path: "/"})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header."}]}`))
			return true
		}
		return false
	}

	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-replay353.json")
	rc, err := runAct024(context.Background(), act024Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		JitterMax:      -1,
		Endpoints:      env.endpoints,
		AssetTransport: env.assetRT,
		AllowLocalhost: true,
		TxIDOptions:    fixedAct024TxIDOpts(),
	})
	if err != nil {
		t.Fatalf("single 353 replay run failed: %v (stoppedBy=%q)", err, rc.StoppedBy)
	}
	if !rc.Passed || rc.StoppedBy != "" {
		t.Fatalf("expected clean pass after single 353 replay, got passed=%v stoppedBy=%q", rc.Passed, rc.StoppedBy)
	}
	if rc.APIUsage.Categories["csrfReplay"] != 1 {
		t.Fatalf("expected csrfReplay=1, got %d", rc.APIUsage.Categories["csrfReplay"])
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), synthX024CT0) || strings.Contains(string(raw), synthX024CT0Rotated) {
		t.Fatalf("receipt leaked old or rotated ct0")
	}
}

func TestAct024HLSOnlyFormalCachePanicRecoveryAndEnvCookie(t *testing.T) {
	t.Run("hls_only_video_records_missing_with_zero_m3u8_requests", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{
			preUsedLogin:      1,
			hlsOnlyVideo:      true,
			externalTcoTarget: true,
			targetMode:        "full",
		})
		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-hls.json")
		rc, err := runAct024(context.Background(), act024Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		})
		if err != nil {
			t.Fatalf("HLS-only run failed: %v (steps=%+v)", err, rc.Steps)
		}
		if env.hlsHits.Load() != 0 {
			t.Fatalf("HLS-only video must make 0 .m3u8 fetches, got %d", env.hlsHits.Load())
		}
		v07, _ := rc.Verifications["V-07"].(map[string]any)
		if v07["hasHLSOnly"] != true || v07["status"] != "verified" {
			t.Fatalf("expected V-07 hasHLSOnly=true verified, got %+v", v07)
		}
		v03, _ := rc.Verifications["V-03"].(map[string]any)
		if v03["blockedExternal"] != true || v03["shortLinkStatus"] != "verified" {
			t.Fatalf("expected V-03 external t.co blockedExternal=true, got %+v", v03)
		}
	})

	t.Run("step_panic_still_cleans_temp_dir_and_writes_sanitized_receipt", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "empty"})
		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-panic.json")
		rc, err := runAct024(context.Background(), act024Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
			StepHook: func(runner *stepRunner) {
				runner.run("synthetic_panic_step", func(f map[string]any) error {
					panic("synthetic panic containing " + synthX024AuthToken + " and " + synthX024SelfHandle)
				})
			},
		})
		if err == nil || rc.Passed {
			t.Fatalf("expected failed run on step panic, got err=%v passed=%v", err, rc.Passed)
		}
		if !rc.TempDataDeleted || !rc.SecretScanClean {
			t.Fatalf("expected TempDataDeleted=true SecretScanClean=true after panic, got temp=%v clean=%v", rc.TempDataDeleted, rc.SecretScanClean)
		}
		raw, rErr := os.ReadFile(outPath)
		if rErr != nil {
			t.Fatalf("receipt was not written after panic: %v", rErr)
		}
		if strings.Contains(string(raw), synthX024AuthToken) || strings.Contains(string(raw), synthX024SelfHandle) {
			t.Fatalf("panic error leaked secret or handle into receipt")
		}
	})

	t.Run("cookie_path_from_process_env_var", func(t *testing.T) {
		dir := t.TempDir()
		ckPath := filepath.Join(dir, "custom-x.cookie")
		if err := os.WriteFile(ckPath, []byte(synthX024CookieRaw), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TRACE_TEST_X_COOKIE_FILE", ckPath)
		raw, cleaned, err := readAndValidateXCookie(dir)
		if err != nil {
			t.Fatalf("readAndValidateXCookie via env failed: %v", err)
		}
		if raw == "" || cleaned["auth_token"] != synthX024AuthToken || cleaned["ct0"] != synthX024CT0 {
			t.Fatalf("unexpected cleaned cookies: %+v", cleaned)
		}
	})
}

func TestAct024RedactorRejectsLeaksAndAllowsSafeSchemaEnums(t *testing.T) {
	r := newAct024Redactor()
	r.addCookieHeader(synthX024CookieRaw)
	r.addSecrets("SYNTHETIC_WEB_BEARER_TOKEN_0001", "SynthQidTweetDetail04")
	r.addIDs(synthX024CreatorRestID, synthX024PostVideoID, synthX024TcoCode)
	r.addHandles(synthX024CreatorHandle, synthX024CreatorName, "Video", "Active")
	r.addURLs("https://x.com/" + synthX024CreatorHandle)
	r.addTexts(synthX024VideoPostText)

	// Safe enum values ("video", "active") MUST NOT false-positive even if a followed user is named "Video"/"Active".
	clean := `{"act":"ACT-024","passed":true,"verifications":{"V-05":{"status":"verified","ruleType":"video","expect":["active"]}}}`
	if err := r.verifyJSONClean(clean); err != nil {
		t.Fatalf("clean receipt falsely rejected: %v", err)
	}

	if !r.containsSecretBytes([]byte("sqlite-bin\x00" + synthX024AuthToken + "\x00end")) {
		t.Fatal("expected containsSecretBytes to catch auth_token in binary bytes")
	}

	leaks := []string{
		`{"note":"` + synthX024AuthToken + `"}`,
		`{"note":"` + synthX024CT0 + `"}`,
		`{"note":"` + synthX024KDT + `"}`,
		`{"note":"SYNTHETIC_WEB_BEARER_TOKEN_0001"}`,
		`{"note":"SynthQidTweetDetail04"}`,
		`{"note":"` + synthX024CreatorRestID + `"}`,
		`{"note":"` + synthX024PostVideoID + `"}`,
		`{"note":"` + synthX024TcoCode + `"}`,
		`{"note":"` + synthX024CreatorHandle + `"}`,
		`{"note":"` + synthX024CreatorName + `"}`,
		`{"note":"` + synthX024VideoPostText + `"}`,
		`{"note":"https://video.twimg.com/ext_tw_video/1/vid.mp4?` + synthX024SignedQS + `"}`,
		`{"note":"https://x.com/i/web/status/12345"}`,
	}
	for i, payload := range leaks {
		if err := r.verifyJSONClean(payload); err == nil {
			t.Fatalf("leak case %d was NOT rejected: %s", i, payload)
		}
	}
}

func TestAct024RedactorExemptsFixedMetadataKeysFromHandleSubstringCheck(t *testing.T) {
	r := newAct024Redactor()
	r.addCookieHeader(synthX024CookieRaw)
	r.addSecrets("SYNTHETIC_WEB_BEARER_TOKEN_0001", "SynthQidTweetDetail04")
	r.addIDs(synthX024CreatorRestID, synthX024PostVideoID)
	r.addURLs("https://x.com/" + synthX024CreatorHandle)
	r.addTexts(synthX024VideoPostText)
	// Real-world X account display names / handles that overlap fixed receipt schema tokens.
	r.addHandles(
		"Twitter", "Image", "Profile", "Timeline",
		"Detail", "Short", "Lists", "Control", "Cache", "Error", "Probe",
		"Media", "Formal", "Missing", "Video", "Targets",
	)

	// Realistic clean receipt containing step names, MIME types, hostClasses, codecs,
	// reasons, commit SHA, timestamps, stoppedBy, and errorKind MUST pass cleanly.
	cleanReceipt := `{
		"act": "ACT-024",
		"commit": "7040df8",
		"startedAt": "2026-11-22T18:00:00Z",
		"finishedAt": "2026-11-22T18:05:00Z",
		"passed": true,
		"stoppedBy": "",
		"apiUsage": {"actId": "act024", "updatedAt": "2026-11-22T18:05:00Z", "runs": 1, "apiCalls": 10},
		"steps": [
			{"name": "v04_v01_v02_frontend_login_and_csrf", "ok": true, "durationMs": 100},
			{"name": "v05_post_details", "ok": true, "durationMs": 100},
			{"name": "v03_shortlink_and_unauth_control", "ok": true, "durationMs": 100},
			{"name": "v06_user_profile_and_timeline", "ok": true, "durationMs": 100},
			{"name": "v09_error_probes", "ok": true, "skipped": true, "durationMs": 0},
			{"name": "v08_mine_lists", "ok": true, "durationMs": 100},
			{"name": "v07_media_and_formal_cache", "ok": true, "durationMs": 100, "errorKind": "unavailable"}
		],
		"verifications": {
			"V-03": {"status": "verified", "landedHostClass": "*.twitter.com", "shortLinkReason": "no shortLinks provided and no t.co URL found in fetched posts"},
			"V-05": {"status": "verified", "posts": [{"index": 1, "expect": ["deletedOrigin"], "errKind": "unavailable", "cardKind": "summary_large_image"}]},
			"V-07": {
				"status": "verified",
				"formalCache": {"codecs": ["avc1.64001f", "mp4a.40.2"], "errorKind": ""},
				"imageProbe": {"contentType": "image/jpeg", "hostClass": "*.twimg.com"}
			},
			"V-09": {"status": "not-run", "reason": "no protectedUsers or missing targets provided"}
		},
		"redirects": [{"category": "shortlink", "targetScheme": "https", "targetHostClass": "*.twitter.com"}]
	}`
	if err := r.verifyJSONClean(cleanReceipt); err != nil {
		t.Fatalf("clean receipt with common-word handles was falsely rejected: %v", err)
	}

	// Free-text and unknown fields MUST still reject those handles.
	handleLeaks := []string{
		`{"notes": ["saw Twitter user"]}`,
		`{"steps": [{"name": "v05_post_details", "error": "failed on Timeline"}]}`,
		`{"unexpected": "Image"}`,
	}
	for i, payload := range handleLeaks {
		if err := r.verifyJSONClean(payload); err == nil {
			t.Fatalf("handle leak case %d was NOT rejected: %s", i, payload)
		}
	}

	// Secrets, raw URLs, IDs, and post text MUST still be rejected even inside exempt metadata keys!
	stillEnforced := []string{
		`{"reason": "` + synthX024AuthToken + `"}`,
		`{"name": "https://x.com/i/status/1"}`,
		`{"reason": "` + synthX024PostVideoID + `"}`,
		`{"reason": "` + synthX024VideoPostText + `"}`,
		`{"contentType": "SYNTHETIC_WEB_BEARER_TOKEN_0001"}`,
	}
	for i, payload := range stillEnforced {
		if err := r.verifyJSONClean(payload); err == nil {
			t.Fatalf("global secret/URL/ID/text check %d failed to reject inside exempt key: %s", i, payload)
		}
	}
}

func TestAct024Step1cCSRFProbeCT0ComparisonSyncAndNon353Handling(t *testing.T) {
	t.Run("unchanged_ct0_echo_on_home_and_step1c_reports_not_changed", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "real_shape_no_missing"})
		homeEchoed := false
		env.hook = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/home" && !homeEchoed {
				homeEchoed = true
				// Echo unchanged ct0 on x.com/home; passiveCT0RotatedOnNormalCalls must stay false.
				http.SetCookie(w, &http.Cookie{Name: "ct0", Value: synthX024CT0, Path: "/"})
			}
			return false
		}

		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-ct0-echo.json")
		rc, err := runAct024(context.Background(), act024Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		})
		if err != nil || !rc.Passed {
			t.Fatalf("runAct024 failed: err=%v passed=%v stoppedBy=%q", err, rc.Passed, rc.StoppedBy)
		}
		v02, _ := rc.Verifications["V-02"].(map[string]any)
		if v02["passiveCT0RotatedOnNormalCalls"] != false {
			t.Fatalf("passiveCT0RotatedOnNormalCalls = %v, want false on unchanged home_html ct0 echo", v02["passiveCT0RotatedOnNormalCalls"])
		}
		if v02["mismatchSetCookieCT0Present"] != true || v02["mismatchSetCookieCT0Valid"] != true {
			t.Fatalf("expected mismatchSetCookieCT0Present=true valid=true, got %+v", v02)
		}
		if v02["mismatchSetCookieCT0Changed"] != false {
			t.Fatalf("mismatchSetCookieCT0Changed = %v, want false when Step 1c echoes unchanged ct0", v02["mismatchSetCookieCT0Changed"])
		}
	})

	t.Run("genuine_ct0_rotation_on_step1c_syncs_for_step2_and_redacts", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "real_shape_no_missing"})
		var rotatedOn1c atomic.Bool
		var step2SawRotatedCT0 atomic.Bool
		var step2SawStaleCT0 atomic.Bool

		env.hook = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/i/api/1.1/account/settings.json" &&
				r.Header.Get("Cookie") != "" &&
				r.Header.Get("x-csrf-token") != "" &&
				r.Header.Get("x-csrf-token") != synthX024CT0 {
				// Step 1c intentional mismatch: genuinely rotate ct0!
				rotatedOn1c.Store(true)
				http.SetCookie(w, &http.Cookie{Name: "ct0", Value: synthX024CT0Rotated, Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header."}]}`))
				return true
			}
			if rotatedOn1c.Load() && strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail) && r.Header.Get("Cookie") != "" {
				if r.Header.Get("x-csrf-token") == synthX024CT0Rotated && strings.Contains(r.Header.Get("Cookie"), "ct0="+synthX024CT0Rotated) {
					step2SawRotatedCT0.Store(true)
				} else {
					step2SawStaleCT0.Store(true)
				}
			}
			return false
		}

		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-ct0-rot.json")
		rc, err := runAct024(context.Background(), act024Config{
			RootDir:        env.rootDir,
			OutPath:        outPath,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		})
		if err != nil || !rc.Passed {
			t.Fatalf("runAct024 with Step 1c ct0 rotation failed: err=%v passed=%v stoppedBy=%q", err, rc.Passed, rc.StoppedBy)
		}
		if !rotatedOn1c.Load() {
			t.Fatal("Step 1c rotation hook never fired")
		}
		if !step2SawRotatedCT0.Load() || step2SawStaleCT0.Load() {
			t.Fatalf("Step 2 ct0 sync mismatch: sawRotated=%v sawStale=%v", step2SawRotatedCT0.Load(), step2SawStaleCT0.Load())
		}
		if rc.APIUsage.Categories["csrfReplay"] != 0 {
			t.Fatalf("expected 0 csrfReplay calls when Step 1c synced rotated ct0, got %d", rc.APIUsage.Categories["csrfReplay"])
		}
		v02, _ := rc.Verifications["V-02"].(map[string]any)
		if v02["mismatchSetCookieCT0Changed"] != true {
			t.Fatalf("mismatchSetCookieCT0Changed = %v, want true on genuine ct0 rotation", v02["mismatchSetCookieCT0Changed"])
		}
		raw, rErr := os.ReadFile(outPath)
		if rErr != nil {
			t.Fatal(rErr)
		}
		if strings.Contains(string(raw), synthX024CT0Rotated) {
			t.Fatal("rotated ct0 from Step 1c leaked into act024.json")
		}
	})

	t.Run("non_353_403_and_non_json_403_on_step1c_do_not_abort_remaining_steps", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			contentType string
			body        string
		}{
			{"json_non_353_403", "application/json", `{"errors":[{"code":99,"message":"Unable to verify your credentials"}]}`},
			{"plain_text_403", "text/plain; charset=utf-8", "Forbidden: csrf mismatch"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "real_shape_no_missing"})
				env.hook = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path == "/i/api/1.1/account/settings.json" &&
						r.Header.Get("Cookie") != "" &&
						r.Header.Get("x-csrf-token") != "" &&
						r.Header.Get("x-csrf-token") != synthX024CT0 {
						w.Header().Set("Content-Type", tc.contentType)
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(tc.body))
						return true
					}
					return false
				}

				outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-"+tc.name+".json")
				rc, err := runAct024(context.Background(), act024Config{
					RootDir:        env.rootDir,
					OutPath:        outPath,
					MinSpacing:     -1,
					JitterMax:      -1,
					Endpoints:      env.endpoints,
					AssetTransport: env.assetRT,
					AllowLocalhost: true,
					TxIDOptions:    fixedAct024TxIDOpts(),
				})
				if err != nil || !rc.Passed || rc.StoppedBy != "" {
					t.Fatalf("%s: expected clean pass without stop, got err=%v passed=%v stoppedBy=%q", tc.name, err, rc.Passed, rc.StoppedBy)
				}
				v02, _ := rc.Verifications["V-02"].(map[string]any)
				if v02["status"] != "verified" || v02["mismatchHTTPStatus"] != 403 || v02["mismatchGot353"] != false {
					t.Fatalf("%s: unexpected V-02: %+v", tc.name, v02)
				}
				if rc.VerdictHints["V-05"] != "pass" || rc.VerdictHints["V-07"] != "pass" {
					t.Fatalf("%s: later steps did not complete: hints=%+v", tc.name, rc.VerdictHints)
				}
			})
		}
	})

	t.Run("fatal_326_or_challenge_html_on_step1c_still_halts_immediately", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			contentType string
			body        string
			wantStop    string
		}{
			{"code_326", "application/json", `{"errors":[{"code":326,"message":"Account locked"}]}`, "x_error_326"},
			{"challenge_html", "text/html", `<html><title>Just a moment...</title><div class="cf-turnstile"></div></html>`, "challenge_html_page"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "real_shape_no_missing"})
				env.hook = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path == "/i/api/1.1/account/settings.json" &&
						r.Header.Get("Cookie") != "" &&
						r.Header.Get("x-csrf-token") != "" &&
						r.Header.Get("x-csrf-token") != synthX024CT0 {
						w.Header().Set("Content-Type", tc.contentType)
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(tc.body))
						return true
					}
					return false
				}

				outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-fatal1c-"+tc.name+".json")
				rc, err := runAct024(context.Background(), act024Config{
					RootDir:        env.rootDir,
					OutPath:        outPath,
					MinSpacing:     -1,
					JitterMax:      -1,
					Endpoints:      env.endpoints,
					AssetTransport: env.assetRT,
					AllowLocalhost: true,
					TxIDOptions:    fixedAct024TxIDOpts(),
				})
				if err == nil || rc.Passed || rc.StoppedBy != tc.wantStop {
					t.Fatalf("%s: got err=%v passed=%v stoppedBy=%q, want stop=%q", tc.name, err, rc.Passed, rc.StoppedBy, tc.wantStop)
				}
			})
		}
	})
}

func TestAct024Step2UnavailableOrForbiddenPostContinuesToTcoPost(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        int
		message     string
		wantErrKind string
	}{
		{"unavailable_post2_code_144", 144, "No status found with that ID.", "unavailable"},
		{"forbidden_post2_code_179", 179, "Sorry, you are not authorized to see this status.", "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "real_shape_no_missing"})
			env.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.HasSuffix(r.URL.Path, "/"+x.OpTweetDetail) && r.Header.Get("Cookie") != "" {
					if strings.Contains(r.URL.Query().Get("variables"), synthX024PostDelOrigID) {
						env.recordFetchedDetailID(synthX024PostDelOrigID)
						writeJSONWithRateLimit(w, map[string]any{
							"errors": []map[string]any{
								{"code": tc.code, "message": tc.message},
							},
						})
						return true
					}
				}
				return false
			}

			outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-"+tc.name+".json")
			rc, err := runAct024(context.Background(), act024Config{
				RootDir:        env.rootDir,
				OutPath:        outPath,
				MinSpacing:     -1,
				JitterMax:      -1,
				Endpoints:      env.endpoints,
				AssetTransport: env.assetRT,
				AllowLocalhost: true,
				TxIDOptions:    fixedAct024TxIDOpts(),
			})
			if err != nil || !rc.Passed || rc.StoppedBy != "" {
				t.Fatalf("%s: expected clean pass, got err=%v passed=%v stoppedBy=%q steps=%+v", tc.name, err, rc.Passed, rc.StoppedBy, rc.Steps)
			}

			// Verify all 3 selected posts were attempted (post #1 video, post #2 unavailable/forbidden, post #3 tcoLink).
			detailIDs := env.getFetchedDetailIDs()
			if len(detailIDs) < 3 ||
				detailIDs[0] != synthX024PostVideoID ||
				detailIDs[1] != synthX024PostDelOrigID ||
				detailIDs[2] != synthX024PostTcoID {
				t.Fatalf("%s: fetchedDetailIDs = %v, want [%s, %s, %s, ...]",
					tc.name, detailIDs, synthX024PostVideoID, synthX024PostDelOrigID, synthX024PostTcoID)
			}

			v05, _ := rc.Verifications["V-05"].(map[string]any)
			if v05["status"] != "verified" || v05["fetchedCount"] != 3 || v05["succeededCount"] != 2 || v05["hasTcoEntity"] != true {
				t.Fatalf("%s: unexpected V-05: %+v", tc.name, v05)
			}
			posts, _ := v05["posts"].([]map[string]any)
			if len(posts) != 3 {
				t.Fatalf("%s: len(V-05.posts) = %d, want 3", tc.name, len(posts))
			}
			if posts[1]["index"] != 1 || posts[1]["errKind"] != tc.wantErrKind {
				t.Fatalf("%s: V-05.posts[1] = %+v, want index=1 errKind=%q", tc.name, posts[1], tc.wantErrKind)
			}

			// Post #3 (tcoLink) still supplied extractedTcoURL for Step 3, and Post #1 (video) still drove Step 7!
			v03, _ := rc.Verifications["V-03"].(map[string]any)
			if v03["shortLinkStatus"] != "verified" || v03["shortLinkSource"] != "post_entity" {
				t.Fatalf("%s: V-03 did not receive extractedTcoURL from post #3: %+v", tc.name, v03)
			}
			v07, _ := rc.Verifications["V-07"].(map[string]any)
			if v07["status"] != "verified" || v07["mp4ffValid"] != true {
				t.Fatalf("%s: V-07 did not formally cache post #1 video: %+v", tc.name, v07)
			}
		})
	}
}

func TestAct024Step1SchemaChangedImmediatelySkipsRemainingSteps(t *testing.T) {
	env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 1, targetMode: "full"})
	brokenMain := strings.ReplaceAll(
		string(readXTestdataFile(t, "webpack_main.js")),
		`operationName:"TweetDetail"`,
		`operationName:"BrokenOp"`,
	)
	env.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/responsive-web/client-web/main.synth01a.js" {
			env.frontendHits.Add(1)
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(brokenMain))
			return true
		}
		return false
	}

	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act024-step1-schema.json")
	rc, err := runAct024(context.Background(), act024Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		MinSpacing:     -1,
		JitterMax:      -1,
		Endpoints:      env.endpoints,
		AssetTransport: env.assetRT,
		AllowLocalhost: true,
		TxIDOptions:    fixedAct024TxIDOpts(),
	})
	if err == nil || rc.Passed {
		t.Fatalf("expected failed run on Step 1 schema_changed, got err=%v passed=%v", err, rc.Passed)
	}
	if rc.StoppedBy != "schema_changed" {
		t.Fatalf("stoppedBy = %q, want %q (must NOT cascade into Step 2 auth_required)", rc.StoppedBy, "schema_changed")
	}
	if len(rc.Steps) != 7 {
		t.Fatalf("len(Steps) = %d, want 7", len(rc.Steps))
	}
	if rc.Steps[0].OK || rc.Steps[0].ErrKind != "schema_changed" {
		t.Fatalf("Step 1 = %+v, want ok=false errorKind=schema_changed", rc.Steps[0])
	}
	for i := 1; i < len(rc.Steps); i++ {
		st := rc.Steps[i]
		if !st.Skipped || st.ErrKind != "" || st.Error != "stopped earlier: schema_changed" {
			t.Fatalf("Step %d (%s) = %+v, want skipped with 'stopped earlier: schema_changed'", i+1, st.Name, st)
		}
	}
	if env.apiHits.Load() != 0 || env.shortHits.Load() != 0 || env.mediaHits.Load() != 0 {
		t.Fatalf("expected 0 API/shortlink/media hits after Step 1 schema_changed, got api=%d short=%d media=%d",
			env.apiHits.Load(), env.shortHits.Load(), env.mediaHits.Load())
	}
	if !rc.TempDataDeleted || !rc.SecretScanClean {
		t.Fatalf("expected TempDataDeleted=true SecretScanClean=true, got temp=%v clean=%v", rc.TempDataDeleted, rc.SecretScanClean)
	}
	v04, _ := rc.Verifications["V-04"].(map[string]any)
	if v04["status"] != "failed" || v04["frontendDiscoveryOK"] != false {
		t.Fatalf("unexpected V-04: %+v", v04)
	}
}

func stripOptionalOpsFromTestMain(mainJS string) string {
	lines := strings.Split(mainJS, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, `operationName:"Bookmarks"`) || strings.Contains(line, `operationName:"Likes"`) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func TestAct025SplitChunkWebpackFixtureEndToEndAndBudget(t *testing.T) {
	// Verify act025APILimits sums exactly to 32 with frontend == 5 (home + main + ondemand + <=2 lazy chunks).
	sum := 0
	for _, v := range act025APILimits {
		sum += v
	}
	if sum != act024MaxTotalRequests || act025APILimits["frontend"] != 5 {
		t.Fatalf("act025APILimits sum=%d frontend=%d, want sum=%d frontend=5", sum, act025APILimits["frontend"], act024MaxTotalRequests)
	}

	baseHome := string(readXTestdataFile(t, "webpack_home.html"))
	baseMain := string(readXTestdataFile(t, "webpack_main.js"))
	coreOnlyMain := stripOptionalOpsFromTestMain(baseMain)

	bookmarksLazyChunk := `(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[203],{` +
		`2006:function(e){e.exports={queryId:"SynthLazyBookmarksQid025",operationName:"Bookmarks",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","longform_notetweets_consumption_enabled"]}}},` +
		`2008:function(e){e.exports={queryId:"SynthLazyFolderQid025",operationName:"BookmarkFoldersSlice",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled"]}}}` +
		`}]);`
	likesLazyChunk := `(self.webpackChunk_synthetic_web=self.webpackChunk_synthetic_web||[]).push([[315],{` +
		`2007:function(e){e.exports={queryId:"SynthLazyLikesQid025",operationName:"Likes",operationType:"query",metadata:{featureSwitches:["responsive_web_graphql_exclude_directive_enabled","verified_phone_label_enabled"]}}}` +
		`}]);`

	t.Run("two_separate_lazy_chunks_bookmarks_and_likes_all_7_steps_pass", func(t *testing.T) {
		env := setupAct024MockEnv(t, act024MockOpts{preUsedLogin: 0, targetMode: "full"})
		splitHome := strings.Replace(
			baseHome,
			`"ondemand.s": "synth02"`,
			`412:"ondemand.s",203:"bundle.Bookmarks",315:"bundle.Likes"}; window.__SYNTH_HASHES__ = {412:"synth02",203:"bmlazy1",315:"lklazy2"`,
			1,
		)
		var bmChunkHits, lkChunkHits atomic.Int32
		env.hook = func(w http.ResponseWriter, r *http.Request) bool {
			switch r.URL.Path {
			case "/home":
				env.frontendHits.Add(1)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(splitHome))
				return true
			case "/responsive-web/client-web/main.synth01a.js":
				env.frontendHits.Add(1)
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
					env.assetCookieLeaked.Store(true)
				}
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write([]byte(coreOnlyMain))
				return true
			case "/responsive-web/client-web/bundle.Bookmarks.bmlazy1a.js":
				env.frontendHits.Add(1)
				bmChunkHits.Add(1)
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
					env.assetCookieLeaked.Store(true)
				}
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write([]byte(bookmarksLazyChunk))
				return true
			case "/responsive-web/client-web/bundle.Likes.lklazy2a.js":
				env.frontendHits.Add(1)
				lkChunkHits.Add(1)
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Csrf-Token") != "" {
					env.assetCookieLeaked.Store(true)
				}
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write([]byte(likesLazyChunk))
				return true
			}
			return false
		}

		// Omit OutPath to verify default artifacts/site-check/act025.json receipt path!
		rc, err := runAct025(context.Background(), act024Config{
			RootDir:        env.rootDir,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		})
		if err != nil {
			t.Fatalf("runAct025 split-chunk failed: %v (stoppedBy=%q steps=%+v)", err, rc.StoppedBy, rc.Steps)
		}
		if !rc.Passed || rc.StoppedBy != "" || rc.Act != act025Label {
			t.Fatalf("unexpected ACT-025 summary: act=%q passed=%v stoppedBy=%q", rc.Act, rc.Passed, rc.StoppedBy)
		}
		if rc.APIUsage.ActID != act025ID || rc.APIUsage.Runs != 1 || rc.APIUsage.APICalls > act024MaxTotalRequests {
			t.Fatalf("unexpected ACT-025 usage: %+v", rc.APIUsage)
		}
		if rc.APIUsage.Categories["frontend"] != 5 {
			t.Fatalf("ACT-025 frontend calls = %d, want 5 (home + main + ondemand + 2 lazy chunks)", rc.APIUsage.Categories["frontend"])
		}
		if bmChunkHits.Load() != 1 || lkChunkHits.Load() != 1 || rc.OperationCounts["static_js"] != 2 {
			t.Fatalf("lazy chunk counts mismatch: bm=%d lk=%d opCounts=%+v", bmChunkHits.Load(), lkChunkHits.Load(), rc.OperationCounts)
		}
		if env.assetCookieLeaked.Load() || !rc.SecretScanClean || !rc.TempDataDeleted {
			t.Fatalf("hygiene violation: assetCookie=%v secretClean=%v tempDel=%v", env.assetCookieLeaked.Load(), rc.SecretScanClean, rc.TempDataDeleted)
		}

		// Independent ledger .local/site-check/usage-act025.json exists; usage-act024.json untouched/absent!
		if _, err := os.Stat(filepath.Join(env.rootDir, ".local", "site-check", "usage-act025.json")); err != nil {
			t.Fatalf("expected usage-act025.json: %v", err)
		}
		if _, err := os.Stat(filepath.Join(env.rootDir, ".local", "site-check", "usage-act024.json")); !os.IsNotExist(err) {
			t.Fatalf("runAct025 must not touch usage-act024.json: err=%v", err)
		}

		// All V-01..V-09 pass!
		for _, key := range []string{"V-01", "V-02", "V-03", "V-04", "V-05", "V-06", "V-07", "V-08", "V-09"} {
			if rc.VerdictHints[key] != "pass" {
				t.Fatalf("verdictHint[%s] = %q, want pass (v=%+v)", key, rc.VerdictHints[key], rc.Verifications[key])
			}
		}
		v04, _ := rc.Verifications["V-04"].(map[string]any)
		if v04["coreOpsCount"] != 5 || v04["requiredOpsCount"] != 7 || v04["lazyChunksFetched"] != 2 || v04["bookmarkFoldersOpInBundle"] != true {
			t.Fatalf("unexpected V-04 after lazy chunk merge: %+v", v04)
		}
		v08, _ := rc.Verifications["V-08"].(map[string]any)
		if v08["bookmarksItems"] != 1 || v08["likesItems"] != 1 || v08["bookmarkFoldersStatus"] != "verified" {
			t.Fatalf("unexpected V-08: %+v", v08)
		}

		// Receipt artifacts/site-check/act025.json exists and leaks ZERO lazy queryIds or URLs!
		outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act025.json")
		rawBytes, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read default act025.json: %v", err)
		}
		rawStr := string(rawBytes)
		for _, secret := range []string{
			"http://", "https://",
			"SynthLazyBookmarksQid025", "SynthLazyLikesQid025", "SynthLazyFolderQid025",
			"bmlazy1", "lklazy2",
			synthX024AuthToken, synthX024CT0,
		} {
			if strings.Contains(rawStr, secret) {
				t.Fatalf("act025.json leaked forbidden string %q", secret)
			}
		}

		// Second run on usage-act025.json MUST fail BEFORE network (MaxRuns=1).
		hitsBefore := env.apiHits.Load() + env.frontendHits.Load()
		if _, err := runAct025(context.Background(), act024Config{
			RootDir:        env.rootDir,
			MinSpacing:     -1,
			JitterMax:      -1,
			Endpoints:      env.endpoints,
			AssetTransport: env.assetRT,
			AllowLocalhost: true,
			TxIDOptions:    fixedAct024TxIDOpts(),
		}); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("second runAct025 err = %v, want ErrQuotaExceeded", err)
		}
		if got := env.apiHits.Load() + env.frontendHits.Load(); got != hitsBefore {
			t.Fatalf("second runAct025 sent %d requests after MaxRuns exhausted", got-hitsBefore)
		}
	})

	t.Run("cookie_on_lazy_chunk_during_mine_phase_is_refused_before_upstream", func(t *testing.T) {
		dir := t.TempDir()
		gate, err := sitecheck.OpenGate(dir, sitecheck.Limits{
			ActID: act025ID, MaxRuns: 1, MaxAPICalls: 32, MaxByCategory: act025APILimits,
		})
		if err != nil {
			t.Fatal(err)
		}
		var hits atomic.Int32
		ck := &act024Checker{
			actID:          act025ID,
			gate:           gate,
			apiLimits:      act025APILimits,
			shortLink:      &mediaBudget{name: "shortlink", maxRequests: 3, maxBytes: 192 << 10},
			media:          &mediaBudget{name: "media", maxRequests: 5, maxBytes: 25 << 20},
			endpoints:      x.DefaultEndpoints(),
			statuses:       map[string]int{},
			opCounts:       map[string]int{},
			rateLimits:     map[string]act024RateLimitSnapshot{},
			cdnHostClasses: map[string]int{},
		}
		apiRT := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
		}))
		ck.setPhase("mine")

		reqCookied, _ := http.NewRequest(http.MethodGet, "https://abs.twimg.com/responsive-web/client-web/bundle.Bookmarks.bmlazy1a.js", nil)
		reqCookied.Header.Set("Cookie", "auth_token="+synthX024AuthToken)
		if _, err := apiRT.RoundTrip(reqCookied); err == nil {
			t.Fatal("expected error when Cookie sent to lazy chunk during mine phase")
		}
		reqNonJS, _ := http.NewRequest(http.MethodGet, "https://abs.twimg.com/responsive-web/client-web/icon.png", nil)
		if _, err := apiRT.RoundTrip(reqNonJS); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("non-.js static asset err = %v, want ErrQuotaExceeded", err)
		}
		if hits.Load() != 0 {
			t.Fatalf("blocked lazy/non-js requests hit upstream: %d", hits.Load())
		}
	})
}
