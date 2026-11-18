package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

// Refreshing an account's live status by hand (the account page) starts
// recording a broadcast it finds live when the account's subscription
// records broadcasts, without waiting for the next poll round (dev
// 2026-11-18: the refresh showed 直播中 and nothing recorded). An account
// whose subscription does not record is only refreshed.
func TestManualLiveRefreshStartsAutoRecording(t *testing.T) {
	const (
		secAuto   = "MS4wLjABAAAA_manual_refresh_auto"
		secPlain  = "MS4wLjABAAAA_manual_refresh_plain"
		biliUID   = int64(30301)
		biliRoom  = int64(9801)
		douyinRID = "7400000000000009301"
	)
	initBytes, fragBytes := splitSyntheticHLSInitAndFrag(t)
	flv := buildSyntheticFLVBytes(t, "avc", 45)
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/live/hold.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			strconv.FormatInt(biliUID, 10): map[string]any{"uid": biliUID, "room_id": biliRoom, "live_status": 1, "live_time": 1794024000, "uname": "B站主播"},
		}})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"room_id": biliRoom, "uid": biliUID, "live_status": 1, "live_time": 1794024000}})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"room_id": biliRoom, "uid": biliUID, "live_status": 1,
			"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{"format_name": "fmp4", "codec": []map[string]any{{
					"codec_name": "avc", "current_qn": 10000, "base_url": "/hls/index.m3u8",
					"url_info": []map[string]any{{"host": srvURL, "extra": ""}},
				}}}},
			}}}},
		}})
	})
	mux.HandleFunc("/hls/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			_, _ = w.Write(initBytes)
		case strings.HasSuffix(r.URL.Path, ".m4s"):
			_, _ = w.Write(fragBytes)
		default:
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/seg1.m4s\n")
		}
	})
	mgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	provider := newFakeDouyinLiveProvider()
	mgr.RegisterSiteProvider(provider)
	provider.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{Site: site.Douyin, AccountUID: uid, WebRID: "880011", RoomID: douyinRID + uid[len(uid)-1:], Status: site.LiveStateLive, Title: "手动刷新", LiveSince: 1794024000}, nil
	}
	provider.streamFn = func(uid, roomOrWebRID string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: uid, WebRID: "880011", RoomID: douyinRID + uid[len(uid)-1:],
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/hold.flv"}, Media: site.DouyinLiveTransport("Mozilla/5.0 Test", true),
		}, nil
	}

	rules := subscription.SubRules{AutoRecord: true}
	if _, err := subMgr.Create("录直播", "", []subscription.AccountRef{
		{Site: "douyin", UID: secAuto, Name: "抖音主播"},
		{Site: "bilibili", UID: strconv.FormatInt(biliUID, 10), Name: "B站主播"},
	}, rules); err != nil {
		t.Fatal(err)
	}
	if _, err := subMgr.Create("只看更新", "", []subscription.AccountRef{{Site: "douyin", UID: secPlain, Name: "普通主播"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := mgr.StatusByAccountKeys(ctx, []string{"douyin:" + secAuto, "douyin:" + secPlain, "bilibili:" + strconv.FormatInt(biliUID, 10)}); err != nil {
		t.Fatal(err)
	}
	recording := func(siteID site.ID, uid string) bool {
		st, ok := mgr.CachedAccountState(siteID, uid)
		return ok && st.Recording
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(recording(site.Douyin, secAuto) && recording(site.Bilibili, strconv.FormatInt(biliUID, 10))) {
		time.Sleep(10 * time.Millisecond)
	}
	if !recording(site.Douyin, secAuto) {
		t.Error("a Douyin account of an auto-record subscription found live by hand is not recording")
	}
	if !recording(site.Bilibili, strconv.FormatInt(biliUID, 10)) {
		t.Error("a Bilibili account of an auto-record subscription found live by hand is not recording")
	}
	provider.mu.Lock()
	plainStreams := provider.streamCalls[secPlain]
	provider.mu.Unlock()
	if recording(site.Douyin, secPlain) || plainStreams != 0 {
		t.Error("an account whose subscription does not record started recording")
	}
	if _, ok := mgr.LiveCheckTimes()["douyin:"+secAuto]; !ok {
		t.Error("the manual check is not counted as the account's live check")
	}
}

// An update check that sees a Douyin account live (author.room_id in its
// works) starts recording it when its subscription records broadcasts,
// without waiting for the account's turn for a live check; a subscription
// that does not record only shows the account live.
func TestUpdateCheckLiveHintStartsAutoRecording(t *testing.T) {
	const (
		secAuto  = "MS4wLjABAAAA_hint_auto"
		secPlain = "MS4wLjABAAAA_hint_plain"
	)
	flv := buildSyntheticFLVBytes(t, "avc", 45)
	mux := http.NewServeMux()
	mux.HandleFunc("/live/hold.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(flv)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	mgr, _, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	provider := newFakeDouyinLiveProvider()
	mgr.RegisterSiteProvider(provider)
	provider.streamFn = func(uid, roomOrWebRID string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: uid, WebRID: "880022", RoomID: "7400000000000009401",
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV, Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/hold.flv"}, Media: site.DouyinLiveTransport("Mozilla/5.0 Test", true),
		}, nil
	}
	if _, err := subMgr.Create("录直播", "", []subscription.AccountRef{{Site: "douyin", UID: secAuto, Name: "抖音主播"}}, subscription.SubRules{AutoRecord: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := subMgr.Create("只看更新", "", []subscription.AccountRef{{Site: "douyin", UID: secPlain, Name: "普通主播"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}

	mgr.ObserveAccountLiveHint(site.Douyin, secAuto, "880022", "7400000000000009401")
	mgr.ObserveAccountLiveHint(site.Douyin, secPlain, "880033", "7400000000000009402")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := mgr.CachedAccountState(site.Douyin, secAuto); ok && st.Recording {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, ok := mgr.CachedAccountState(site.Douyin, secAuto); !ok || !st.Recording {
		t.Fatalf("an auto-record account seen live by an update check is not recording: %+v", st)
	}
	provider.mu.Lock()
	plain := provider.streamCalls[secPlain]
	provider.mu.Unlock()
	if plain != 0 {
		t.Error("an account whose subscription does not record started recording")
	}
}
