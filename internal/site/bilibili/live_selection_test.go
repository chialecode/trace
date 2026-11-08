package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestLiveSelectionChoosesHighestQualityBeforeCodec(t *testing.T) {
	for _, tc := range []struct {
		name     string
		avcQN    int
		wantCode string
	}{
		{name: "higher quality overrides codec preference", avcQN: 400, wantCode: "hevc"},
		{name: "codec preference breaks equal quality tie", avcQN: 10000, wantCode: "avc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := func(name string, qn int) map[string]any {
				return map[string]any{
					"codec_name": name, "current_qn": qn, "base_url": "/fixture/index.m3u8",
					"url_info": []map[string]string{{"host": "https://fixture.bilivideo.com", "extra": "?token=synthetic"}},
				}
			}
			client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/xlive/web-room/v2/index/getRoomPlayInfo" || r.URL.Query().Get("qn") != "10000" {
					t.Errorf("unexpected live selection request: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"room_id": 9001, "uid": 10001, "live_status": 1,
						"playurl_info": map[string]any{"playurl": map[string]any{
							"g_qn_desc": []map[string]any{{"qn": 10000, "desc": "测试原画"}},
							"stream": []map[string]any{{"protocol_name": "http_hls", "format": []map[string]any{{
								"format_name": "fmp4", "codec": []map[string]any{codec("avc", tc.avcQN), codec("hevc", 10000)},
							}}}},
						}},
					},
				})
			}), -1)
			selected, err := client.LiveRoomPlayInfo(context.Background(), 9001, []string{"avc", "hevc"})
			if err != nil {
				t.Fatal(err)
			}
			if selected.Quality != 10000 || selected.Codec != tc.wantCode || selected.QualityName != "测试原画" {
				t.Fatalf("highest available quality and matching description were not selected: %+v", selected)
			}
		})
	}
}
