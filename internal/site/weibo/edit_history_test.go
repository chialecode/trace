package weibo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func newTestEditHistoryClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB; SUBP=SYNTHETIC_SUBP; XSRF-TOKEN=synthetic-xsrf")
	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			StatusesEditHistory: srv.URL + "/ajax/statuses/editHistory",
		},
		MinInterval:    -1,
		JitterMax:      -1,
		AllowLocalhost: true,
	})
	return client, srv
}

func TestGetEditHistory_SinglePageAndMediaAndLinks(t *testing.T) {
	var seenQueries []string
	var srvURL string
	client, srv := newTestEditHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		seenQueries = append(seenQueries, r.URL.RawQuery)
		if r.URL.Query().Get("mid") != "5001002003004005" {
			t.Errorf("expected mid=5001002003004005, got %q", r.URL.RawQuery)
		}
		if r.Header.Get("X-XSRF-TOKEN") != "synthetic-xsrf" {
			t.Errorf("missing X-XSRF-TOKEN header")
		}
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`{"ok":1,"total":3,"statuses":[]}`))
			return
		}
		payload := fmt.Sprintf(`{
			"ok": 1,
			"total": 3,
			"statuses": [
				{
					"idstr": "5001002003004005",
					"mid": "5001002003004005",
					"mblogid": "",
					"created_at": "Thu Nov 28 12:30:00 +0800 2026",
					"text_raw": "第三次修订正文 https://t.cn/A6test01",
					"text": "第三次修订正文 <a href=\"https://example.org/doc\">项目说明</a>",
					"url_struct": [
						{
							"short_url": "https://t.cn/A6test01",
							"long_url": "https://example.org/doc",
							"url_title": "项目说明"
						}
					],
					"pic_ids": ["picCur01"],
					"pic_infos": {
						"picCur01": {
							"pic_id": "picCur01",
							"type": "pic",
							"largest": {"url": "https://wx1.sinaimg.cn/large/picCur01.jpg", "width": 1600, "height": 1200}
						}
					}
				},
				{
					"idstr": "5001002003004005",
					"mid": "5001002003004005",
					"mblogid": "",
					"created_at": "Thu Nov 28 12:15:00 +0800 2026",
					"text_raw": "第二次修订：修正错别字",
					"text": "第二次修订：修正错别字",
					"pic_ids": ["picCur01", "picOld02"],
					"pic_infos": {
						"picCur01": {
							"pic_id": "picCur01",
							"type": "pic",
							"largest": {"url": "https://wx1.sinaimg.cn/large/picCur01.jpg", "width": 1600, "height": 1200}
						},
						"picOld02": {
							"pic_id": "picOld02",
							"type": "livephoto",
							"video": "https://video.weibo.com/signed.mp4?ssig=SECRET&Expires=999999&KID=unistore,video",
							"largest": {"url": "https://wx2.sinaimg.cn/large/picOld02.jpg", "width": 1200, "height": 900},
							"original": {"url": "https://wx2.sinaimg.cn/orj1080/picOld02.jpg?ssig=BAD&Expires=1", "width": 1080, "height": 810}
						}
					}
				},
				{
					"idstr": "5001002003004005",
					"mid": "5001002003004005",
					"mblogid": "",
					"created_at": "Thu Nov 28 12:00:00 +0800 2026",
					"text_raw": "原始首发版本",
					"text": "原始首发版本",
					"pic_ids": ["picOrig03"],
					"pic_infos": {
						"picOrig03": {
							"pic_id": "picOrig03",
							"type": "pic",
							"largest": {"url": "%s/img/picOrig03.jpg", "width": 800, "height": 600}
						}
					}
				}
			]
		}`, srvURL)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	srvURL = srv.URL

	// Also verify mblogid input converts cleanly to numeric mid.
	mblogID, err := MidToMblogID("5001002003004005")
	if err != nil {
		t.Fatalf("MidToMblogID: %v", err)
	}

	revs, err := client.GetEditHistory(context.Background(), mblogID)
	if err != nil {
		t.Fatalf("GetEditHistory: %v", err)
	}
	if len(seenQueries) != 1 {
		t.Fatalf("expected exactly 1 page request when total reached, got %d (%v)", len(seenQueries), seenQueries)
	}
	if len(revs) != 3 {
		t.Fatalf("expected 3 revisions, got %d", len(revs))
	}

	// Verify newest-first ordering and 1-based version numbering (3 -> 2 -> 1).
	if revs[0].Version != 3 || revs[1].Version != 2 || revs[2].Version != 1 {
		t.Fatalf("unexpected versions: %d, %d, %d", revs[0].Version, revs[1].Version, revs[2].Version)
	}
	if !(revs[0].EditTs > revs[1].EditTs && revs[1].EditTs > revs[2].EditTs) {
		t.Fatalf("expected descending timestamps: %d, %d, %d", revs[0].EditTs, revs[1].EditTs, revs[2].EditTs)
	}
	if !strings.Contains(revs[0].Text, "第三次修订正文") || len(revs[0].Links) == 0 || revs[0].Links[0].URL != "https://example.org/doc" {
		t.Fatalf("unexpected rev[0] text/links: text=%q links=%+v", revs[0].Text, revs[0].Links)
	}

	// Verify signed video / motion / signed fallback URLs are strictly stripped.
	if len(revs[1].Images) != 2 {
		t.Fatalf("expected 2 images on rev[1], got %d", len(revs[1].Images))
	}
	picOld := revs[1].Images[1]
	if picOld.PicID != "picOld02" || picOld.PublicPreviewURL != "https://wx2.sinaimg.cn/large/picOld02.jpg" {
		t.Fatalf("unexpected picOld: %+v", picOld)
	}
	if picOld.MotionURL != "" || len(picOld.FallbackURLs) != 0 {
		t.Fatalf("expected signed motion and signed fallback stripped, got motion=%q fallbacks=%v", picOld.MotionURL, picOld.FallbackURLs)
	}
	if len(revs[2].Images) != 1 || !strings.HasPrefix(revs[2].Images[0].PublicPreviewURL, srvURL) {
		t.Fatalf("expected synthetic loopback image parsed on rev[2], got %+v", revs[2].Images)
	}

	rawJSON, _ := json.Marshal(revs)
	if strings.Contains(string(rawJSON), "ssig=") || strings.Contains(string(rawJSON), "Expires=") {
		t.Fatalf("serialized EditRevision leaked signed params: %s", string(rawJSON))
	}
}

func TestGetEditHistory_PaginationCapAtFivePages(t *testing.T) {
	pagesRequested := 0
	client, _ := newTestEditHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		pagesRequested++
		p := r.URL.Query().Get("page")
		resp := fmt.Sprintf(`{
			"ok": 1,
			"total": 12,
			"statuses": [
				{
					"idstr": "5001002003004099",
					"created_at": "Thu Nov 28 12:0%s:00 +0800 2026",
					"text_raw": "第 %s 页修订内容"
				}
			]
		}`, p, p)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	})

	revs, err := client.GetEditHistory(context.Background(), "5001002003004099")
	if err != nil {
		t.Fatalf("GetEditHistory: %v", err)
	}
	if pagesRequested != maxEditHistoryPages {
		t.Fatalf("expected pagination cap %d, got %d requests", maxEditHistoryPages, pagesRequested)
	}
	if len(revs) != maxEditHistoryPages {
		t.Fatalf("expected %d revisions, got %d", maxEditHistoryPages, len(revs))
	}
}

func TestGetEditHistory_StopsOnDuplicateOrEmptyPage(t *testing.T) {
	pagesRequested := 0
	client, _ := newTestEditHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		pagesRequested++
		resp := `{
			"ok": 1,
			"total": 10,
			"statuses": [
				{
					"idstr": "5001002003004088",
					"created_at": "Thu Nov 28 12:00:00 +0800 2026",
					"text_raw": "重复页修订内容"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	})

	revs, err := client.GetEditHistory(context.Background(), "5001002003004088")
	if err != nil {
		t.Fatalf("GetEditHistory: %v", err)
	}
	if pagesRequested != 2 {
		t.Fatalf("expected stop after 2nd duplicate page, got %d", pagesRequested)
	}
	if len(revs) != 1 {
		t.Fatalf("expected 1 unique revision, got %d", len(revs))
	}
}

func TestGetEditHistory_ClassifiesRiskAndAuthErrors(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		location string
		body     string
		wantKind string
	}{
		{
			name:     "401 auth",
			status:   http.StatusUnauthorized,
			body:     `{"ok":0,"msg":"login required"}`,
			wantKind: site.ErrKindAuthRequired,
		},
		{
			name:     "418 risk control",
			status:   418,
			body:     `blocked`,
			wantKind: site.ErrKindRiskControl,
		},
		{
			name:     "429 rate limited",
			status:   http.StatusTooManyRequests,
			body:     `{"ok":0,"msg":"too many requests"}`,
			wantKind: site.ErrKindRateLimited,
		},
		{
			name:     "visitor redirect",
			status:   http.StatusFound,
			location: "https://passport.weibo.com/visitor/visitor?entry=miniblog",
			wantKind: site.ErrKindRiskControl,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestEditHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := client.GetEditHistory(context.Background(), "5001002003004077")
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			var se *site.Error
			if !errors.As(err, &se) {
				t.Fatalf("expected *site.Error, got %T: %v", err, err)
			}
			if se.Kind != tc.wantKind {
				t.Fatalf("got kind %s, want %s (err=%v)", se.Kind, tc.wantKind, err)
			}
		})
	}
}

func TestGetEditHistory_InvalidID(t *testing.T) {
	client, _ := newTestEditHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not send HTTP request on invalid mid")
	})
	_, err := client.GetEditHistory(context.Background(), "   ")
	if err == nil || site.KindOf(err) != site.ErrKindBadRequest {
		t.Fatalf("expected ErrKindBadRequest, got %v", err)
	}
}

func TestGetEditHistory_DecodesMblogIDToNumericMid(t *testing.T) {
	const mblogID = "Q1a2b3c4d"
	wantMid, err := MblogIDToMid(mblogID)
	if err != nil || wantMid == "" {
		t.Fatalf("MblogIDToMid(%q): mid=%q err=%v", mblogID, wantMid, err)
	}
	var gotMid string
	client, _ := newTestEditHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMid = r.URL.Query().Get("mid")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":1,"total":1,"statuses":[{"idstr":"` + wantMid + `","created_at":"Thu Nov 28 12:00:00 +0800 2026","text_raw":"解码 mblogid 正常返回"}]}`))
	})
	revs, err := client.GetEditHistory(context.Background(), mblogID)
	if err != nil {
		t.Fatalf("GetEditHistory(%q): %v", mblogID, err)
	}
	if gotMid != wantMid {
		t.Fatalf("query mid = %q, want %q", gotMid, wantMid)
	}
	if len(revs) != 1 || revs[0].Version != 1 {
		t.Fatalf("unexpected revs: %+v", revs)
	}
}
