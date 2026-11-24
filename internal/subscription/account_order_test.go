package subscription

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/storage"
)

func accountKeys(accounts []AccountRef) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.Site + ":" + a.UID
	}
	return out
}

func snapshotAccountKeys(accounts []SnapshotAccount) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.Site + ":" + a.UID
	}
	return out
}

// DEC-071: a subscription's linked accounts are ordered by canonical site order
// (bilibili -> douyin -> weibo -> x) across Create, LinkAccount, Unlink, disk
// reload, and snapshots, while keeping stable relative order for multiple
// accounts on the same site and coherent Avatar defaults.
func TestSubscriptionAccountsCanonicalSiteOrderAndStableSameSiteOrder(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	subsPath := filepath.Join(tmp, "subscriptions.json")
	mgr, err := NewManagerWithUpdatesDB(db, db, subsPath, nil, nil, nil, settings.Default(tmp).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}

	const (
		dy1 = "MS4wLjABAAAA__SiteOrder_DY_First"
		dy2 = "MS4wLjABAAAA__SiteOrder_DY_Second"
		wb1 = "100000101"
		wb2 = "100000202"
		x1  = "180000000000000101"
		x2  = "180000000000000202"
	)

	// 1. Create with reverse/interleaved add order across all four sites and
	//    multiple accounts per site.
	created, err := mgr.Create("跨站顺序测试", "", []AccountRef{
		{Site: "x", UID: x1, UniqueID: "x_first", Name: "X一号"},
		{Site: "weibo", UID: wb1, Name: "微博一号"},
		{Site: "douyin", UID: dy1, UniqueID: "dy_one", Name: "抖音一号"},
		{Site: "x", UID: x2, UniqueID: "x_second", Name: "X二号"},
		{Site: "bilibili", UID: "9002", Name: "B站先加号", Face: "https://i0.hdslb.com/bfs/face/bili_9002.jpg"},
		{Site: "weibo", UID: wb2, Name: "微博二号"},
		{Site: "douyin", UID: dy2, UniqueID: "dy_two", Name: "抖音二号"},
		{Site: "bilibili", UID: "1001", Name: "B站后加号", Face: "https://i0.hdslb.com/bfs/face/bili_1001.jpg"},
	}, DefaultSubRules())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wantOrder := []string{
		"bilibili:9002",
		"bilibili:1001",
		"douyin:" + dy1,
		"douyin:" + dy2,
		"weibo:" + wb1,
		"weibo:" + wb2,
		"x:" + x1,
		"x:" + x2,
	}
	if got := accountKeys(created.Accounts); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("Create Accounts order = %v, want %v", got, wantOrder)
	}
	// Default avatar picks the first account in canonical site order (bilibili:9002).
	if created.Avatar != (AvatarRef{Site: "bilibili", UID: "9002"}) || created.Mid != 9002 {
		t.Fatalf("expected default Avatar=bilibili:9002 and Mid=9002, got avatar=%+v mid=%d", created.Avatar, created.Mid)
	}

	// 2. Sequential LinkAccount in reverse site order (X -> Weibo -> Douyin -> Bilibili#1 -> Bilibili#2)
	//    sorts accounts into canonical site order while keeping the subscription's existing Avatar
	//    until that avatar account is unlinked.
	seqSub, err := mgr.Create("逐个关联测试", "", []AccountRef{
		{Site: "x", UID: "180000000000000999", UniqueID: "seq_x", Name: "逐个X", Face: "https://pbs.twimg.com/profile_images/9/a.jpg?format=jpg&name=orig"},
	}, DefaultSubRules())
	if err != nil {
		t.Fatalf("Create seqSub: %v", err)
	}
	if seqSub.Avatar != (AvatarRef{Site: "x", UID: "180000000000000999"}) {
		t.Fatalf("initial seqSub Avatar = %+v", seqSub.Avatar)
	}
	for _, acc := range []AccountRef{
		{Site: "weibo", UID: "200000999", Name: "逐个微博"},
		{Site: "douyin", UID: "MS4wLjABAAAA__Seq_DY", Name: "逐个抖音"},
		{Site: "bilibili", UID: "7701", Name: "逐个B站一", Face: "https://i0.hdslb.com/bfs/face/7701.jpg"},
		{Site: "bilibili", UID: "3301", Name: "逐个B站二", Face: "https://i0.hdslb.com/bfs/face/3301.jpg"},
	} {
		seqSub, err = mgr.LinkAccount(seqSub.ID, acc)
		if err != nil {
			t.Fatalf("LinkAccount(%s:%s): %v", acc.Site, acc.UID, err)
		}
	}
	wantSeq := []string{
		"bilibili:7701",
		"bilibili:3301",
		"douyin:MS4wLjABAAAA__Seq_DY",
		"weibo:200000999",
		"x:180000000000000999",
	}
	if got := accountKeys(seqSub.Accounts); !reflect.DeepEqual(got, wantSeq) {
		t.Fatalf("LinkAccount sequence order = %v, want %v", got, wantSeq)
	}
	// Existing Avatar stays on X while X remains linked; primary Mid reflects bilibili:7701.
	if seqSub.Avatar != (AvatarRef{Site: "x", UID: "180000000000000999"}) || seqSub.Mid != 7701 {
		t.Fatalf("expected Avatar preserved on x and Mid=7701, got avatar=%+v mid=%d", seqSub.Avatar, seqSub.Mid)
	}
	if seqSub.Face != "https://pbs.twimg.com/profile_images/9/a.jpg?format=jpg&name=orig" {
		t.Fatalf("expected Face synced to chosen Avatar account, got %q", seqSub.Face)
	}

	// Unlinking X re-syncs Avatar to the first remaining account in canonical site order (bilibili:7701).
	seqSub, err = mgr.Unlink(seqSub.ID, "x", "180000000000000999")
	if err != nil {
		t.Fatalf("Unlink x: %v", err)
	}
	if seqSub.Avatar != (AvatarRef{Site: "bilibili", UID: "7701"}) || seqSub.Face != "https://i0.hdslb.com/bfs/face/7701.jpg" {
		t.Fatalf("expected Avatar/Face re-synced to bilibili:7701 after unlinking x, got avatar=%+v face=%q", seqSub.Avatar, seqSub.Face)
	}

	// 3. Loading an existing subscriptions.json whose accounts were stored in raw add order
	//    normalizes them into canonical site order on read without losing the stored Avatar.
	diskPath := filepath.Join(tmp, "legacy_order_subscriptions.json")
	rawDoc := map[string]any{
		"version": SubscriptionsSchemaVersion,
		"subscriptions": []map[string]any{
			{
				"id":        "sub_disk_unsorted_01",
				"name":      "旧顺序订阅",
				"avatar":    map[string]any{"site": "weibo", "uid": "300000001"},
				"createdAt": "2026-11-20T00:00:00Z",
				"ruleId":    DefaultRuleID,
				"accounts": []map[string]any{
					{"site": "weibo", "uid": "300000001", "name": "微博旧一", "face": "https://wx1.sinaimg.cn/large/wb301.jpg"},
					{"site": "x", "uid": "180000000000000301", "uniqueId": "disk_x", "name": "X旧号"},
					{"site": "douyin", "uid": "MS4wLjABAAAA__Disk_DY", "name": "抖音旧号"},
					{"site": "bilibili", "uid": "5501", "name": "B站旧号"},
				},
			},
		},
	}
	encoded, err := json.MarshalIndent(rawDoc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetSubscriptionsPath(diskPath); err != nil {
		t.Fatalf("SetSubscriptionsPath: %v", err)
	}
	loaded, err := mgr.List()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("List loaded = %+v err=%v", loaded, err)
	}
	wantDiskOrder := []string{
		"bilibili:5501",
		"douyin:MS4wLjABAAAA__Disk_DY",
		"weibo:300000001",
		"x:180000000000000301",
	}
	if got := accountKeys(loaded[0].Accounts); !reflect.DeepEqual(got, wantDiskOrder) {
		t.Fatalf("disk reload Accounts order = %v, want %v", got, wantDiskOrder)
	}
	if loaded[0].Avatar != (AvatarRef{Site: "weibo", UID: "300000001"}) || loaded[0].Face != "https://wx1.sinaimg.cn/large/wb301.jpg" {
		t.Fatalf("disk reload did not preserve stored Weibo Avatar/Face: avatar=%+v face=%q", loaded[0].Avatar, loaded[0].Face)
	}

	// 4. Snapshot and ParseSnapshot normalize account order and produce identical hashes.
	snap, err := mgr.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshotAccountKeys(snap.Subscriptions[0].Accounts); !reflect.DeepEqual(got, wantDiskOrder) {
		t.Fatalf("Snapshot Accounts order = %v, want %v", got, wantDiskOrder)
	}
	unsortedSnapJSON := `{
		"format": "trace-subscriptions",
		"version": 1,
		"rules": [{"id": "default", "name": "默认"}],
		"subscriptions": [{
			"id": "sub_disk_unsorted_01",
			"name": "旧顺序订阅",
			"ruleId": "default",
			"avatar": {"site": "weibo", "uid": "300000001"},
			"accounts": [
				{"site": "x", "uid": "180000000000000301"},
				{"site": "weibo", "uid": "300000001"},
				{"site": "douyin", "uid": "MS4wLjABAAAA__Disk_DY"},
				{"site": "bilibili", "uid": "5501"}
			]
		}]
	}`
	parsedSnap, err := ParseSnapshot([]byte(unsortedSnapJSON))
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	if got := snapshotAccountKeys(parsedSnap.Subscriptions[0].Accounts); !reflect.DeepEqual(got, wantDiskOrder) {
		t.Fatalf("ParseSnapshot Accounts order = %v, want %v", got, wantDiskOrder)
	}
	if parsedSnap.Hash() != snap.Hash() {
		t.Fatalf("Snapshot.Hash mismatch between canonical and unsorted input: %s vs %s", parsedSnap.Hash(), snap.Hash())
	}
}
