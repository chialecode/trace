package download

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// A work by someone else found in an account's works list (a co-creation)
// is stored under that account's folder with via collab, keeps its real
// author, is listed under the account in the library and keeps the via on
// the persisted task; the author's own works and works already cached stay
// where they are (DEC-063).
func TestDouyinCoCreationIsStoredUnderTheAccountItWasCachedFor(t *testing.T) {
	const (
		author  = "MS4wLjABAAAA_cocreation_author_000001"
		member  = "MS4wLjABAAAA_cocreation_member_000002"
		collab  = "7300000000000000031"
		ownWork = "7300000000000000032"
	)
	videoMP4 := buildProgressiveWithoutElst(t, true, false, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/v.mp4", func(w http.ResponseWriter, r *http.Request) {
		serveRangeBytes(w, r, videoMP4, "video/mp4", 0, nil)
	})
	mgr, libStore, srv, _, _ := setupDouyinDownloadTestEnv(t, mux, 1)
	planner := newFakeDouyinWorkPlanner()
	mgr.RegisterWorkPlanner(planner)
	plan := func(id string) func(int) (WorkDownloadPlan, error) {
		return func(int) (WorkDownloadPlan, error) {
			return WorkDownloadPlan{
				Site: site.Douyin, Kind: "video", TargetID: id, Title: "共创", DurationSec: 2,
				AuthorUID: author, AuthorName: "发布者",
				VideoURLs: []string{srv.URL + "/cdn/v.mp4"}, VideoGear: "adapt_lowest_4_1", QualityName: "720P", Codec: "avc", Width: 640, Height: 360,
				Transport: site.DouyinVODTransport("Mozilla/5.0 Test", true),
			}, nil
		}
	}
	planner.setPlan(collab, plan(collab))
	planner.setPlan(ownWork, plan(ownWork))
	root := libStore.CacheDir()
	folder := func(uid, id string) string {
		return filepath.Join(root, "douyin", site.DouyinAuthorDir(uid), "works", id)
	}
	cache := func(id, forUID string) library.ItemMetadata {
		t.Helper()
		task, err := mgr.EnqueueDouyinWork(context.Background(), DouyinWorkEnqueueRequest{AwemeID: id, Source: "queue", ForUID: forUID, ForName: "共创成员"})
		if err != nil {
			t.Fatalf("EnqueueDouyinWork(%s): %v", id, err)
		}
		waitForTaskStatus(t, mgr, task.TaskID, "completed", 8*time.Second)
		item, ok, err := libStore.GetItem("douyin:video:" + id)
		if err != nil || !ok {
			t.Fatalf("%s not indexed: %v %v", id, ok, err)
		}
		return item
	}

	item := cache(collab, member)
	if !strings.EqualFold(filepath.Clean(item.ItemDir), filepath.Clean(folder(member, collab))) {
		t.Fatalf("co-creation stored in %s, want the member's folder %s", item.ItemDir, folder(member, collab))
	}
	if item.Via == nil || item.Via.Reason != library.ViaCollab || item.Via.Site != "douyin" || item.Via.UID != member || item.Via.Name != "共创成员" {
		t.Fatalf("via = %+v", item.Via)
	}
	if item.Author.UID != author {
		t.Fatalf("author = %+v, want the real author kept", item.Author)
	}
	listed, err := libStore.ListItems(library.FeedFilter{Site: "douyin", AuthorUID: member})
	if err != nil || len(listed) != 1 || listed[0].ItemKey != item.ItemKey {
		t.Fatalf("library filtered by the member = %d items %v", len(listed), err)
	}
	counts, err := libStore.CountCachedWorks([]library.AuthorRef{{Site: "douyin", UID: member}})
	if err != nil || counts["douyin:"+member] != 1 {
		t.Fatalf("cached works of the member = %v %v, want the co-creation counted", counts, err)
	}
	tasks, err := mgr.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	var persisted *library.ItemVia
	for _, tk := range tasks {
		if tk.TargetID == collab {
			persisted = tk.Via
		}
	}
	if persisted == nil || persisted.UID != member {
		t.Fatalf("persisted task via = %+v", persisted)
	}

	// The author's own work, cached for the author, has no via.
	own := cache(ownWork, author)
	if own.Via != nil || !strings.EqualFold(filepath.Clean(own.ItemDir), filepath.Clean(folder(author, ownWork))) {
		t.Fatalf("own work: dir %s via %+v", own.ItemDir, own.Via)
	}
	// An item already cached keeps its folder when cached again for another account.
	again := cache(ownWork, member)
	if again.Via != nil || !strings.EqualFold(filepath.Clean(again.ItemDir), filepath.Clean(folder(author, ownWork))) {
		t.Fatalf("re-cached work moved: dir %s via %+v", again.ItemDir, again.Via)
	}
}
