package download

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// DEC-056 ①: a forward is stored in the forwarder's forwards/ folder, and the
// video it carries goes there too (not under the uploader), recorded as
// forwarded content for the media library's "转发" filter.
func TestForwardIsStoredInForwarderFolderApartFromOwnPosts(t *testing.T) {
	vBytes, _ := BuildSyntheticVideoM4S(10, 30)
	aBytes, _ := BuildSyntheticAudioM4S(15)
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": "900100",
					"type":   "DYNAMIC_TYPE_FORWARD",
					"modules": map[string]any{
						"module_author":  map[string]any{"mid": 555, "name": "Forwarder", "pub_ts": 1700000600},
						"module_dynamic": map[string]any{"desc": map[string]any{"text": "转发一下"}},
					},
					"orig": map[string]any{
						"id_str": "900001",
						"type":   "DYNAMIC_TYPE_AV",
						"modules": map[string]any{
							"module_author": map[string]any{"mid": 888, "name": "Uploader", "pub_ts": 1700000500},
							"module_dynamic": map[string]any{
								"major": map[string]any{
									"archive": map[string]any{"bvid": "BV1fw411w7KC", "title": "Forwarded Video"},
								},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV1fw411w7KC", "aid": 170002, "title": "Forwarded Video", "duration": 1,
				"owner": map[string]any{"mid": 888, "name": "Uploader"},
				"pages": []map[string]any{{"cid": 5002, "page": 1, "part": "P1", "duration": 1}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality": 80, "accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 1,
					"video":    []map[string]any{{"id": 80, "baseUrl": srvURL + "/media/video.m4s", "codecid": 7, "codecs": "avc1"}},
					"audio":    []map[string]any{{"id": 30280, "baseUrl": srvURL + "/media/audio.m4s", "codecs": "mp4a.40.2"}},
				},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bvid": "BV1fw411w7KC", "cid": 5002}})
	})
	mux.HandleFunc("/media/video.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(vBytes)))
		_, _ = w.Write(vBytes)
	})
	mux.HandleFunc("/media/audio.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(aBytes)))
		_, _ = w.Write(aBytes)
	})
	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = mgr.Start(ctx)
	defer mgr.Stop()

	task, err := mgr.EnqueueDynamic(ctx, DynamicEnqueueRequest{DynamicID: "900100", CacheLinkedVideos: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, task.TaskID, "completed", 5*time.Second)

	root := lib.CacheDir()
	dyn, ok, _ := lib.GetItem("bilibili:dynamic:900100")
	if !ok || !samePath(dyn.ItemDir, filepath.Join(root, "bilibili", "555", "forwards", "900100")) {
		t.Fatalf("forward stored at %q, want the forwarder's forwards/ folder", dyn.ItemDir)
	}
	vid, ok, _ := lib.GetItem("bilibili:video:BV1fw411w7KC")
	if !ok || !samePath(vid.ItemDir, filepath.Join(root, "bilibili", "555", "forwards", "BV1fw411w7KC")) {
		t.Fatalf("forwarded video stored at %q, want the forwarder's forwards/ folder", vid.ItemDir)
	}
	if _, err := os.Stat(filepath.Join(root, "bilibili", "888")); !os.IsNotExist(err) {
		t.Fatalf("the uploader's folder was created for a forward: %v", err)
	}
	if vid.Author.Mid != 888 || vid.Via == nil || vid.Via.Reason != library.ViaForward || vid.Via.UID != "555" || vid.Via.DynamicID != "900100" {
		t.Fatalf("forwarded video author/via = %+v / %+v", vid.Author, vid.Via)
	}
	fwd, err := lib.ListItems(library.FeedFilter{Kind: "forward"})
	if err != nil || len(fwd) != 2 {
		t.Fatalf("转发 lists %d items (%v), want the forward and its video", len(fwd), err)
	}
	own, err := lib.ListItems(library.FeedFilter{Kind: "video", ExcludeForward: true})
	if err != nil || len(own) != 0 {
		t.Fatalf("视频 without forwards lists %d items (%v), want 0", len(own), err)
	}
	mine, err := lib.ListItems(library.FeedFilter{Mid: 555})
	if err != nil || len(mine) != 2 {
		t.Fatalf("the forwarder's filter lists %d items (%v), want the forward and the video cached for them", len(mine), err)
	}
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// DEC-056 ②: a collaboration video cached for a member who is not the
// uploader goes under that member's folder; anyone else's request (or no
// creator) keeps the uploader's folder.
func TestCollaborationVideoIsStoredUnderTheMemberItWasCachedFor(t *testing.T) {
	mgr, lib, _ := setupDownloadEnv(t, http.NewServeMux(), 1)
	detail := &bilibili.VideoDetail{
		BVID:   "BV1co411w7KC",
		Title:  "Collab",
		Author: bilibili.AuthorInfo{Mid: 888, Name: "Uploader"},
		Staff:  []bilibili.AuthorInfo{{Mid: 888, Name: "Uploader"}, {Mid: 777, Name: "Member"}},
		Pages:  []bilibili.VideoPage{{CID: 1, Page: 1, Title: "P1", Duration: 10}},
	}
	ctx := context.Background()
	tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: detail.BVID, Detail: detail, ForMid: 777})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("EnqueueVideo: %v", err)
	}
	want := filepath.Join(lib.CacheDir(), "bilibili", "777", "videos", detail.BVID)
	if !samePath(tasks[0].ItemDir, want) {
		t.Fatalf("collab task folder %q, want %q", tasks[0].ItemDir, want)
	}
	via := videoVia(lib.CacheDir(), want, *detail, &tasks[0])
	if via == nil || via.Reason != library.ViaCollab || via.UID != "777" || via.Name != "Member" {
		t.Fatalf("collab via = %+v", via)
	}

	other := *detail
	other.BVID, other.Pages = "BV1ot411w7KC", []bilibili.VideoPage{{CID: 2, Page: 1}}
	for _, forMid := range []int64{0, 888, 999} {
		tasks, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: other.BVID, Detail: &other, ForMid: forMid})
		if err != nil || len(tasks) != 1 || tasks[0].ItemDir != "" {
			t.Fatalf("forMid=%d: task folder %q (%v), want the uploader's default", forMid, tasks[0].ItemDir, err)
		}
		_, _ = mgr.Cancel(tasks[0].TaskID)
	}
	if v := videoVia(lib.CacheDir(), filepath.Join(lib.CacheDir(), "bilibili", "999", "videos", other.BVID), other, &Task{}); v != nil {
		t.Fatalf("a non-member's folder recorded via %+v", v)
	}
	if v := videoVia(lib.CacheDir(), filepath.Join(lib.CacheDir(), "bilibili", "888", "videos", other.BVID), other, &Task{}); v != nil {
		t.Fatalf("the uploader's own folder recorded via %+v", v)
	}
}

func TestDedupeDynamicMediaKeepsOrder(t *testing.T) {
	imgs := dedupeDynamicImages([]bilibili.DynamicMedia{{URL: "a"}, {URL: "b"}, {URL: "a"}})
	if len(imgs) != 2 || imgs[0].URL != "a" || imgs[1].URL != "b" {
		t.Fatalf("images = %+v", imgs)
	}
	vids := dedupeDynamicVideos([]bilibili.DynamicVideoRef{{BVID: "x"}, {BVID: "x"}, {BVID: "y"}})
	if len(vids) != 2 || vids[1].BVID != "y" {
		t.Fatalf("videos = %+v", vids)
	}
}
