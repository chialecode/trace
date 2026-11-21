package douyin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

// TestEmptyUserProfileAndShellTruthTable verifies that genuine 0-post Douyin user profiles
// (aweme_count == 0 with empty/null aweme_list and explicit has_more == 0) return a clean empty
// page without auth_required or authExpired callbacks, while expired/guest/risk-control shells
// and aweme_count > 0 mismatches remain strictly caught.
func TestEmptyUserProfileAndShellTruthTable(t *testing.T) {
	ctx := context.Background()
	const (
		emptySecUID   = "MS4wLjABAAAA_SYNTHETIC_EMPTY_001"
		otherSecUID   = "MS4wLjABAAAA_SYNTHETIC_OTHER_002"
		privateSecUID = "MS4wLjABAAAA_SYNTHETIC_PRIVATE_003"
	)

	t.Run("genuine_zero_post_user_with_known_profile_empty_null_and_omitted_list", func(t *testing.T) {
		bodies := []struct {
			name string
			body string
		}{
			{name: "explicit_empty_array", body: `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0,"min_cursor":0}`},
			{name: "null_list_with_has_more_zero", body: `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0,"min_cursor":0}`},
			{name: "omitted_list_with_has_more_zero", body: `{"status_code":0,"has_more":0,"max_cursor":0,"min_cursor":0}`},
		}

		for _, tc := range bodies {
			t.Run(tc.name, func(t *testing.T) {
				var profileHits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/aweme/v1/web/aweme/post/":
						_, _ = w.Write([]byte(tc.body))
					case "/aweme/v1/web/user/profile/other/":
						profileHits.Add(1)
						_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"30001","sec_uid":"` + emptySecUID + `","nickname":"空主页合成号","secret":0,"aweme_count":0}}`))
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()

				store := openTestDouyinCredStore(t, true)
				var authExpiredCount atomic.Int32
				var breakerCount atomic.Int32

				client := NewClient(ClientOptions{
					CredStore:      store,
					AllowLocalhost: true,
					MinInterval:    -1,
					JitterMax:      -1,
					Entropy:        newDeterministicEntropy(0x51),
					Endpoints: Endpoints{
						ProfileOther: srv.URL + "/aweme/v1/web/user/profile/other/",
						AwemePost:    srv.URL + "/aweme/v1/web/aweme/post/",
					},
				})
				client.SetAuthExpiredHandler(func(string) {
					authExpiredCount.Add(1)
				})
				client.SetAutomaticRequestHandlers(nil, func(string, error) {
					breakerCount.Add(1)
				})

				autoCtx := site.WithAutomaticRequests(ctx, "subscription")
				page, err := client.ListUserPosts(autoCtx, UserPostsRequest{
					SecUID:    emptySecUID,
					MaxCursor: "0",
					KnownProfile: &UserProfile{
						SecUID:     emptySecUID,
						Nickname:   "空主页合成号",
						AwemeCount: 0,
						IsPrivate:  false,
					},
				})
				if err != nil {
					t.Fatalf("ListUserPosts(%s) unexpected error: %v", tc.name, err)
				}
				if len(page.Works) != 0 || page.HasMore || page.UncertainEmpty {
					t.Fatalf("ListUserPosts(%s) = %+v, want empty non-uncertain page with HasMore=false", tc.name, page)
				}
				if profileHits.Load() != 0 {
					t.Fatalf("expected 0 extra profile/other requests when KnownProfile is supplied, got %d", profileHits.Load())
				}
				if authExpiredCount.Load() != 0 {
					t.Fatalf("authExpired handler must NOT fire on genuine 0-post user, got %d", authExpiredCount.Load())
				}
				if breakerCount.Load() != 0 {
					t.Fatalf("automatic breaker must NOT fire on genuine 0-post user, got %d", breakerCount.Load())
				}
				if st := store.Status(); !st.LoggedIn || st.State != "logged_in" {
					t.Fatalf("credential store state mutated on genuine 0-post user: %+v", st)
				}
			})
		}
	})

	t.Run("genuine_zero_post_user_cross_check_via_client_and_adapter", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
		}{
			{name: "empty_array", body: `{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`},
			{name: "null_list", body: `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var postHits, profileHits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/aweme/v1/web/aweme/post/":
						postHits.Add(1)
						_, _ = w.Write([]byte(tc.body))
					case "/aweme/v1/web/user/profile/other/":
						profileHits.Add(1)
						_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"30002","sec_uid":"` + emptySecUID + `","unique_id":"synth_empty_001","nickname":"零作品主播","secret":0,"aweme_count":0}}`))
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()

				store := openTestDouyinCredStore(t, true)
				var authExpiredCount atomic.Int32
				client := NewClient(ClientOptions{
					CredStore:      store,
					AllowLocalhost: true,
					MinInterval:    -1,
					JitterMax:      -1,
					Entropy:        newDeterministicEntropy(0x52),
					Endpoints: Endpoints{
						ProfileOther: srv.URL + "/aweme/v1/web/user/profile/other/",
						AwemePost:    srv.URL + "/aweme/v1/web/aweme/post/",
					},
				})
				client.SetAuthExpiredHandler(func(string) {
					authExpiredCount.Add(1)
				})

				// Direct ListUserPosts with CrossCheckProfile=true.
				page, err := client.ListUserPosts(ctx, UserPostsRequest{
					SecUID:            emptySecUID,
					MaxCursor:         "0",
					CrossCheckProfile: true,
				})
				if err != nil {
					t.Fatalf("ListUserPosts(CrossCheckProfile) error: %v", err)
				}
				if len(page.Works) != 0 || page.HasMore || page.UncertainEmpty || page.Author == nil || page.Author.Nickname != "零作品主播" {
					t.Fatalf("unexpected cross-checked page: %+v", page)
				}
				if postHits.Load() != 1 || profileHits.Load() != 1 {
					t.Fatalf("expected 1 post + 1 profile hit, got post=%d profile=%d", postHits.Load(), profileHits.Load())
				}

				// Adapter.FetchCreatorPosts handles both [] and null cleanly with 1 post + 1 profile call.
				adapter := NewAdapter(client)
				cp, err := adapter.FetchCreatorPosts(ctx, emptySecUID, "0")
				if err != nil {
					t.Fatalf("FetchCreatorPosts(%s) error: %v", tc.name, err)
				}
				if len(cp.Posts) != 0 || cp.HasMore || cp.UncertainEmpty || cp.AuthorName != "零作品主播" || cp.UniqueID != "synth_empty_001" {
					t.Fatalf("unexpected FetchCreatorPosts result: %+v", cp)
				}
				if postHits.Load() != 2 || profileHits.Load() != 2 {
					t.Fatalf("expected 2 total post + 2 total profile hits, got post=%d profile=%d", postHits.Load(), profileHits.Load())
				}
				if authExpiredCount.Load() != 0 || store.Status().State != "logged_in" {
					t.Fatalf("authExpired must NOT trigger on genuine 0-post cross-check (count=%d state=%q)", authExpiredCount.Load(), store.Status().State)
				}
			})
		}
	})

	t.Run("private_account_dec057_visible_posts_and_hidden_zero_post_never_expire_auth", func(t *testing.T) {
		var mode atomic.Int32 // 0 = visible 1 post, 1 = hidden empty [], 2 = hidden null
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/aweme/v1/web/aweme/post/":
				switch mode.Load() {
				case 0:
					_, _ = w.Write([]byte(`{"status_code":0,"has_more":0,"max_cursor":0,"aweme_list":[{"aweme_id":"7300000000000000501","aweme_type":0,"create_time":1763000100,"desc":"私密好友可见作品","author":{"uid":"30003","sec_uid":"` + privateSecUID + `","nickname":"私密合成号"},"video":{"width":1080,"height":1920,"play_addr":{"url_list":["https://v26-web.douyinvod.com/synthetic/priv.mp4"]}}}]}`))
				case 1:
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`))
				case 2:
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`))
				}
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		store := openTestDouyinCredStore(t, true)
		var authExpiredCount atomic.Int32
		client := NewClient(ClientOptions{
			CredStore:      store,
			AllowLocalhost: true,
			MinInterval:    -1,
			JitterMax:      -1,
			Entropy:        newDeterministicEntropy(0x53),
			Endpoints: Endpoints{
				AwemePost: srv.URL + "/aweme/v1/web/aweme/post/",
			},
		})
		client.SetAuthExpiredHandler(func(string) {
			authExpiredCount.Add(1)
		})

		privProfile := &UserProfile{SecUID: privateSecUID, Nickname: "私密合成号", IsPrivate: true, AwemeCount: 0}

		// DEC-057 #7: private account with visible works must still return those works.
		mode.Store(0)
		pVisible, err := client.ListUserPosts(ctx, UserPostsRequest{
			SecUID:       privateSecUID,
			MaxCursor:    "0",
			KnownProfile: privProfile,
		})
		if err != nil || len(pVisible.Works) != 1 || pVisible.Works[0].ID != "7300000000000000501" {
			t.Fatalf("DEC-057 visible private account failed: page=%+v err=%v", pVisible, err)
		}

		// Hidden private account (aweme_count == 0, both [] and null) -> ErrKindForbidden, never auth_required/authExpired.
		for _, m := range []int32{1, 2} {
			mode.Store(m)
			_, err := client.ListUserPosts(ctx, UserPostsRequest{
				SecUID:       privateSecUID,
				MaxCursor:    "0",
				KnownProfile: privProfile,
			})
			if !site.IsKind(err, site.ErrKindForbidden) {
				t.Fatalf("private hidden mode=%d err=%v, want forbidden", m, err)
			}
		}
		if authExpiredCount.Load() != 0 || store.Status().State != "logged_in" {
			t.Fatalf("private account must NOT expire auth (count=%d state=%q)", authExpiredCount.Load(), store.Status().State)
		}
	})

	t.Run("true_shells_errors_and_nonzero_aweme_count_still_strictly_caught", func(t *testing.T) {
		zeroKnown := &UserProfile{SecUID: emptySecUID, Nickname: "空主页合成号", AwemeCount: 0, IsPrivate: false}
		nonZeroKnown := &UserProfile{SecUID: emptySecUID, Nickname: "有作品合成号", AwemeCount: 8, IsPrivate: false}
		mismatchedKnown := &UserProfile{SecUID: otherSecUID, Nickname: "另一个用户", AwemeCount: 0, IsPrivate: false}
		unknownTotalKnown := &UserProfile{SecUID: emptySecUID, Nickname: "未知数合成号", AwemeCount: -1, IsPrivate: false}

		errorCases := []struct {
			name            string
			cursor          string
			httpStatus      int
			contentType     string
			body            string
			known           *UserProfile
			crossCheck      bool
			profileAwemeCnt int
			wantKind        string
			wantExpired     bool
			wantProfileHits int32
		}{
			{
				name:       "zero_byte_body_even_with_known_zero_profile",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       "",
				known:      zeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:        "html_verify_page_even_with_known_zero_profile",
				cursor:      "0",
				httpStatus:  http.StatusOK,
				contentType: "text/html; charset=utf-8",
				body:        `<!DOCTYPE html><html><head><title>验证码中间页</title></head><body>verify</body></html>`,
				known:       zeroKnown,
				wantKind:    site.ErrKindRiskControl,
			},
			{
				name:        "http_401_unauthorized_marks_expired",
				cursor:      "0",
				httpStatus:  http.StatusUnauthorized,
				body:        `{"status_code":401}`,
				known:       zeroKnown,
				wantKind:    site.ErrKindAuthRequired,
				wantExpired: true,
			},
			{
				name:       "http_403_argus_block",
				cursor:     "0",
				httpStatus: http.StatusForbidden,
				body:       `Blocked by ArgusSecurityPlugin`,
				known:      zeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:       "http_429_rate_limited",
				cursor:     "0",
				httpStatus: http.StatusTooManyRequests,
				body:       `{}`,
				known:      zeroKnown,
				wantKind:   site.ErrKindRateLimited,
			},
			{
				name:        "status_code_2483_even_with_known_zero_profile",
				cursor:      "0",
				httpStatus:  http.StatusOK,
				body:        `{"status_code":2483,"status_msg":"未登录"}`,
				known:       zeroKnown,
				wantKind:    site.ErrKindAuthRequired,
				wantExpired: true,
			},
			{
				name:       "bare_status_zero_missing_has_more_and_aweme_list_even_with_known_zero",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0}`,
				known:      zeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:       "null_list_missing_has_more_even_with_known_zero",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0,"aweme_list":null}`,
				known:      zeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:       "empty_array_missing_has_more_even_with_known_zero",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0,"aweme_list":[]}`,
				known:      zeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:            "missing_aweme_list_with_has_more_zero_without_known_profile",
				cursor:          "0",
				httpStatus:      http.StatusOK,
				body:            `{"status_code":0,"has_more":0,"max_cursor":0}`,
				known:           nil,
				crossCheck:      true, // missing key must NOT blindly cross-check
				wantKind:        site.ErrKindRiskControl,
				wantProfileHits: 0,
			},
			{
				name:       "missing_aweme_list_with_has_more_zero_nonzero_profile",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0,"has_more":0,"max_cursor":0}`,
				known:      nonZeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:       "missing_aweme_list_with_has_more_zero_mismatched_secuid",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0,"has_more":0,"max_cursor":0}`,
				known:      mismatchedKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:       "null_list_with_has_more_zero_no_profile_no_crosscheck",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`,
				known:      nil,
				crossCheck: false,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:            "null_list_with_has_more_zero_crosscheck_nonzero_profile",
				cursor:          "0",
				httpStatus:      http.StatusOK,
				body:            `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`,
				known:           nil,
				crossCheck:      true,
				profileAwemeCnt: 12,
				wantKind:        site.ErrKindRiskControl,
				wantProfileHits: 1,
			},
			{
				name:       "null_list_with_has_more_zero_known_nonzero_profile",
				cursor:     "0",
				httpStatus: http.StatusOK,
				body:       `{"status_code":0,"aweme_list":null,"has_more":0,"max_cursor":0}`,
				known:      nonZeroKnown,
				wantKind:   site.ErrKindRiskControl,
			},
			{
				name:        "subsequent_page_bare_guest_shell_still_auth_required",
				cursor:      "1763000500000",
				httpStatus:  http.StatusOK,
				body:        `{"status_code":0}`,
				known:       zeroKnown,
				wantKind:    site.ErrKindAuthRequired,
				wantExpired: true,
			},
			{
				name:        "subsequent_page_missing_aweme_list_still_auth_required",
				cursor:      "1763000500000",
				httpStatus:  http.StatusOK,
				body:        `{"status_code":0,"has_more":0}`,
				known:       zeroKnown,
				wantKind:    site.ErrKindAuthRequired,
				wantExpired: true,
			},
		}

		for _, tc := range errorCases {
			t.Run(tc.name, func(t *testing.T) {
				var profileHits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/aweme/v1/web/user/profile/other/" {
						profileHits.Add(1)
						w.Header().Set("Content-Type", "application/json")
						b, _ := json.Marshal(map[string]any{
							"status_code": 0,
							"user": map[string]any{
								"uid":         "30009",
								"sec_uid":     emptySecUID,
								"nickname":    "核对合成号",
								"secret":      0,
								"aweme_count": tc.profileAwemeCnt,
							},
						})
						_, _ = w.Write(b)
						return
					}
					ct := tc.contentType
					if ct == "" {
						ct = "application/json"
					}
					w.Header().Set("Content-Type", ct)
					w.WriteHeader(tc.httpStatus)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer srv.Close()

				store := openTestDouyinCredStore(t, true)
				var authExpiredCount atomic.Int32
				client := NewClient(ClientOptions{
					CredStore:      store,
					AllowLocalhost: true,
					MinInterval:    -1,
					JitterMax:      -1,
					Entropy:        newDeterministicEntropy(0x54),
					Endpoints: Endpoints{
						ProfileOther: srv.URL + "/aweme/v1/web/user/profile/other/",
						AwemePost:    srv.URL + "/aweme/v1/web/aweme/post/",
					},
				})
				client.SetAuthExpiredHandler(func(string) {
					authExpiredCount.Add(1)
				})

				_, err := client.ListUserPosts(ctx, UserPostsRequest{
					SecUID:            emptySecUID,
					MaxCursor:         tc.cursor,
					KnownProfile:      tc.known,
					CrossCheckProfile: tc.crossCheck,
				})
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("ListUserPosts(%s) kind = %q (err=%v), want %q", tc.name, site.KindOf(err), err, tc.wantKind)
				}
				if gotHits := profileHits.Load(); gotHits != tc.wantProfileHits {
					t.Fatalf("ListUserPosts(%s) profileHits = %d, want %d", tc.name, gotHits, tc.wantProfileHits)
				}
				if tc.wantExpired {
					if authExpiredCount.Load() != 1 || store.Status().State != "expired" {
						t.Fatalf("ListUserPosts(%s) expected authExpired=1 & state=expired, got count=%d state=%q",
							tc.name, authExpiredCount.Load(), store.Status().State)
					}
				} else {
					if authExpiredCount.Load() != 0 || store.Status().State != "logged_in" {
						t.Fatalf("ListUserPosts(%s) must NOT expire auth, got count=%d state=%q",
							tc.name, authExpiredCount.Load(), store.Status().State)
					}
				}
			})
		}

		// Page-1 explicit [] + has_more: 0 with aweme_count > 0, mismatched SecUID, or unknown (-1) total
		// -> UncertainEmpty=true, NEVER genuine empty, NEVER firing authExpired.
		for _, tc := range []struct {
			name  string
			known *UserProfile
		}{
			{name: "nonzero_high_aweme_count", known: nonZeroKnown},
			{name: "nonzero_small_aweme_count", known: &UserProfile{SecUID: emptySecUID, AwemeCount: 3}},
			{name: "mismatched_secuid_profile", known: mismatchedKnown},
			{name: "negative_unknown_total", known: unknownTotalKnown},
			{name: "nil_profile_no_crosscheck", known: nil},
		} {
			t.Run("uncertain_empty_"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`))
				}))
				defer srv.Close()

				store := openTestDouyinCredStore(t, true)
				var authExpiredCount atomic.Int32
				client := NewClient(ClientOptions{
					CredStore:      store,
					AllowLocalhost: true,
					MinInterval:    -1,
					JitterMax:      -1,
					Entropy:        newDeterministicEntropy(0x55),
					Endpoints: Endpoints{
						AwemePost: srv.URL + "/aweme/v1/web/aweme/post/",
					},
				})
				client.SetAuthExpiredHandler(func(string) {
					authExpiredCount.Add(1)
				})

				page, err := client.ListUserPosts(ctx, UserPostsRequest{
					SecUID:       emptySecUID,
					MaxCursor:    "0",
					KnownProfile: tc.known,
				})
				if err != nil {
					t.Fatalf("ListUserPosts(%s) unexpected error: %v", tc.name, err)
				}
				if !page.UncertainEmpty || page.HasMore || len(page.Works) != 0 {
					t.Fatalf("ListUserPosts(%s) = %+v, want UncertainEmpty=true & HasMore=false", tc.name, page)
				}
				if authExpiredCount.Load() != 0 || store.Status().State != "logged_in" {
					t.Fatalf("ListUserPosts(%s) must NOT expire auth (count=%d state=%q)", tc.name, authExpiredCount.Load(), store.Status().State)
				}
			})
		}
	})
}
