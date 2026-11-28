package companion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/library"
)

func TestLibraryItemDetailIncludesWeiboEditHistoryAndServesEditImages(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "node-wb-eh")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.AddDevice("Phone", "Pixel", "android", companionapi.ChannelDev, "192.168.1.55", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	backend := newTestCompanionBackend(t, true)
	defer backend.Close()

	postDir, err := library.WeiboPostDir(backend.LibraryStore().CacheDir(), "500000001", "5009001002003088", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(postDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	imgBytes := synthJPEG(320, 200)
	_ = os.WriteFile(filepath.Join(postDir, "images", "01.jpg"), imgBytes, 0o600)
	_ = os.WriteFile(filepath.Join(postDir, "images", "edit_v01_01.jpg"), imgBytes, 0o600)

	saved, err := backend.LibraryStore().SaveItem(postDir, library.ItemMetadata{
		Version:      1,
		ID:           "dynamic:5009001002003088",
		Site:         "weibo",
		Kind:         "dynamic",
		DynamicType:  "draw",
		TypeLabel:    "图文",
		Status:       "complete",
		DirectCached: true,
		Title:        "配套接口微博编辑历史测试",
		Text:         "最终版正文",
		PubTs:        1764203600,
		EditCount:    1,
		Author:       library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "合成微博作者"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.jpg", Bytes: int64(len(imgBytes)), Width: 320, Height: 200},
			{Role: "edit_image", Path: "images/edit_v01_01.jpg", Bytes: int64(len(imgBytes)), Width: 320, Height: 200},
		},
		EditHistory: []library.ItemEditRevision{
			{
				Version:    2,
				EditTs:     1764203600,
				Text:       "最终版正文",
				Links:      []library.ItemLink{{Label: "文档", URL: "https://example.org/doc"}},
				MediaPaths: []string{"images/01.jpg"},
			},
			{
				Version:    1,
				EditTs:     1764200000,
				Text:       "初版正文",
				MediaPaths: []string{"images/edit_v01_01.jpg"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	srv := NewServer(ServerOptions{
		Backend: backend,
		Store:   store,
		Pairing: newPairingManager(nil, nil),
		Hub:     newEventHub(nil, nil),
		Channel: companionapi.ChannelDev,
		Version: "0.1.0-dev",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/library/items/"+url.PathEscape(saved.ItemKey), nil)
	req.RemoteAddr = "192.168.1.55:45123"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	req.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET library item status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), postDir) {
		t.Fatalf("companion response leaked local path: %s", rec.Body.String())
	}
	var detail companionapi.LibraryItemDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail.EditCount != 1 || len(detail.EditHistory) != 2 {
		t.Fatalf("unexpected editCount/editHistory: count=%d hist=%+v", detail.EditCount, detail.EditHistory)
	}
	if detail.EditHistory[1].MediaPaths[0] != "images/edit_v01_01.jpg" {
		t.Fatalf("unexpected v1 mediaPaths: %+v", detail.EditHistory[1].MediaPaths)
	}

	// Verify GET /api/v1/media/{key}/images/edit_v01_01.jpg succeeds via MediaPath whitelist!
	mediaReq := httptest.NewRequest(http.MethodGet, "/api/v1/media/"+url.PathEscape(saved.ItemKey)+"/images/edit_v01_01.jpg", nil)
	mediaReq.RemoteAddr = "192.168.1.55:45124"
	mediaReq.Header.Set("Authorization", "Bearer "+token)
	mediaReq.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	mediaReq.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	mediaRec := httptest.NewRecorder()
	srv.ServeHTTP(mediaRec, mediaReq)
	if mediaRec.Code != http.StatusOK || mediaRec.Body.Len() == 0 {
		t.Fatalf("GET edit_image media status=%d len=%d", mediaRec.Code, mediaRec.Body.Len())
	}
}
