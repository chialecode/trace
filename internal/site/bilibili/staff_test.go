package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// DEC-056 ②: the members of a collaboration video (联合投稿) come from
// data.staff; a single-author video has none.
func TestVideoDetailParsesCollaborationStaff(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"bvid": r.URL.Query().Get("bvid"), "aid": 1, "title": "v", "cid": 9,
			"owner": map[string]any{"mid": 888, "name": "Uploader"}}
		if r.URL.Query().Get("bvid") == "BV1co411w7KC" {
			data["staff"] = []map[string]any{
				{"mid": 888, "title": "UP主", "name": "Uploader", "face": "//i0.hdslb.com/bfs/face/a.jpg"},
				{"mid": 777, "title": "参演", "name": "Member", "face": "http://i0.hdslb.com/bfs/face/b.jpg"},
				{"mid": 0, "name": "broken"},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	})
	client, _, _ := newTestLoggedClient(t, mux, -1)
	vd, err := client.VideoDetail(context.Background(), "BV1co411w7KC")
	if err != nil {
		t.Fatal(err)
	}
	if len(vd.Staff) != 2 || vd.Staff[1].Mid != 777 || vd.Staff[1].Name != "Member" || vd.Staff[0].Face != "https://i0.hdslb.com/bfs/face/a.jpg" {
		t.Fatalf("staff = %+v", vd.Staff)
	}
	if !vd.HasStaffMember(777) || !vd.HasStaffMember(888) || vd.HasStaffMember(999) {
		t.Fatal("HasStaffMember is wrong")
	}
	single, err := client.VideoDetail(context.Background(), "BV17x411w7KC")
	if err != nil || len(single.Staff) != 0 || single.HasStaffMember(777) {
		t.Fatalf("single-author staff = %+v (%v)", single.Staff, err)
	}
}
