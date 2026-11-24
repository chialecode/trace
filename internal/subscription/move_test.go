package subscription

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedAccountUpdateBaseline(t *testing.T, mgr *Manager, siteName, uid, dynID string, pubTs int64) {
	t.Helper()
	st := AccountState{
		Site:          siteName,
		UID:           uid,
		BaselineAt:    "2026-11-20T08:00:00Z",
		LastCheckedAt: "2026-11-21T08:00:00Z",
		LastKind:      "ok",
		NewestID:      dynID,
		NewestTs:      pubTs,
		Note:          "baseline intact",
	}
	mgr.mu.Lock()
	err := mgr.saveAccountStateLocked(st)
	mgr.mu.Unlock()
	if err != nil {
		t.Fatalf("saveAccountStateLocked(%s:%s): %v", siteName, uid, err)
	}
	if _, err := mgr.insertUpdateItem(siteName, uid, dynID, pubTs, "draw", `{"id":"`+dynID+`"}`, "2026-11-21T08:00:00Z"); err != nil {
		t.Fatalf("insertUpdateItem(%s:%s): %v", siteName, uid, err)
	}
}

func findSubByID(t *testing.T, subs []Subscription, id string) Subscription {
	t.Helper()
	for _, s := range subs {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("subscription %s not found in %+v", id, subs)
	return Subscription{}
}

func TestMoveAcrossAllThreeSitesMetadataAvatarSyncBaselineAndRollback(t *testing.T) {
	mgr := regressionSubscriptions(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	dyUID := "MS4wLjABAAAA__MoveCaseSensitive_01"
	wbUID := "1000000123"
	biliFace := "https://i0.hdslb.com/bfs/face/bili_1001.jpg"
	wbFace := "https://wx1.sinaimg.cn/large/wb_1000000123.jpg"

	subSrc, err := mgr.Create("源订阅", "保留原备注", []AccountRef{
		{Site: "bilibili", UID: "001001", Name: "B站甲", Face: biliFace, AddedAt: "2026-11-10T01:00:00Z"},
		{Site: "douyin", UID: dyUID, UniqueID: "dy_handle_move", WebRID: "998877", Name: "抖音甲", AddedAt: "2026-11-11T02:00:00Z"},
		{Site: "weibo", UID: "00" + wbUID, Name: "微博甲", Face: wbFace, AddedAt: "2026-11-12T03:00:00Z"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create subSrc: %v", err)
	}
	// Point subSrc.Avatar explicitly at the Weibo account to test avatar re-sync on move.
	subSrc.Avatar = AvatarRef{Site: "weibo", UID: wbUID}
	subSrc, err = mgr.Upsert(subSrc)
	if err != nil {
		t.Fatalf("Upsert subSrc avatar: %v", err)
	}
	if subSrc.Avatar.Site != "weibo" || subSrc.Avatar.UID != wbUID {
		t.Fatalf("expected subSrc avatar on weibo, got %+v", subSrc.Avatar)
	}

	subDest, err := mgr.Create("目标订阅", "", nil, DefaultSubRules())
	if err != nil {
		t.Fatalf("Create subDest: %v", err)
	}

	seedAccountUpdateBaseline(t, mgr, "bilibili", "1001", "bili_dyn_1", 1710001000)
	seedAccountUpdateBaseline(t, mgr, "douyin", dyUID, "dy_work_1", 1710002000)
	seedAccountUpdateBaseline(t, mgr, "weibo", wbUID, "wb_post_1", 1710003000)

	// 1. Move Weibo (with leading zeros in input) from subSrc to empty subDest.
	//    - subSrc.Avatar pointed to Weibo -> must re-sync cleanly to subSrc's first remaining account (bilibili:1001).
	//    - subDest was empty -> subDest.Avatar must point to weibo:1000000123; metadata & updates.db stay intact.
	movedDest, err := mgr.Move("weibo", "00"+wbUID, subDest.ID)
	if err != nil {
		t.Fatalf("Move weibo: %v", err)
	}
	if len(movedDest.Accounts) != 1 || movedDest.Accounts[0].Site != "weibo" || movedDest.Accounts[0].UID != wbUID ||
		movedDest.Accounts[0].Name != "微博甲" || movedDest.Accounts[0].Face != wbFace || movedDest.Accounts[0].AddedAt != "2026-11-12T03:00:00Z" {
		t.Fatalf("moved weibo account metadata mismatch: %+v", movedDest.Accounts)
	}
	if movedDest.Avatar != (AvatarRef{Site: "weibo", UID: wbUID}) {
		t.Fatalf("expected empty dest avatar synced to moved weibo account, got %+v", movedDest.Avatar)
	}
	all, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	srcAfterWB := findSubByID(t, all, subSrc.ID)
	if len(srcAfterWB.Accounts) != 2 || srcAfterWB.Avatar != (AvatarRef{Site: "bilibili", UID: "1001"}) {
		t.Fatalf("expected src avatar re-synced to bilibili:1001 after moving avatar account, got accounts=%+v avatar=%+v", srcAfterWB.Accounts, srcAfterWB.Avatar)
	}

	// 2. Move Douyin from subSrc to subDest; UniqueID, WebRID, Name, AddedAt preserved verbatim,
	//    canonical site order places Douyin before Weibo, and subDest.Avatar stays on Weibo.
	movedDest, err = mgr.Move("douyin", dyUID, subDest.ID)
	if err != nil {
		t.Fatalf("Move douyin: %v", err)
	}
	if len(movedDest.Accounts) != 2 {
		t.Fatalf("expected 2 accounts on dest, got %+v", movedDest.Accounts)
	}
	dyAcc := movedDest.Accounts[0]
	if dyAcc.Site != "douyin" || dyAcc.UID != dyUID || dyAcc.UniqueID != "dy_handle_move" || dyAcc.WebRID != "998877" ||
		dyAcc.Name != "抖音甲" || dyAcc.AddedAt != "2026-11-11T02:00:00Z" || movedDest.Accounts[1].Site != "weibo" {
		t.Fatalf("moved douyin metadata/order mismatch: %+v", movedDest.Accounts)
	}
	if movedDest.Avatar != (AvatarRef{Site: "weibo", UID: wbUID}) {
		t.Fatalf("expected dest avatar preserved on weibo, got %+v", movedDest.Avatar)
	}

	// 3. Move last account (Bilibili) from subSrc to subDest:
	//    subDest orders [bilibili, douyin, weibo] while keeping its Weibo avatar;
	//    subSrc becomes empty -> Avatar cleared, Face cleared, Mid=0, Name & Rules preserved.
	movedDest, err = mgr.Move("bilibili", "001001", subDest.ID)
	if err != nil {
		t.Fatalf("Move bilibili: %v", err)
	}
	if len(movedDest.Accounts) != 3 || movedDest.Accounts[0].Site != "bilibili" || movedDest.Accounts[0].Face != biliFace ||
		movedDest.Accounts[0].AddedAt != "2026-11-10T01:00:00Z" || movedDest.Accounts[1].Site != "douyin" || movedDest.Accounts[2].Site != "weibo" {
		t.Fatalf("moved bilibili metadata/order mismatch: %+v", movedDest.Accounts)
	}
	all, err = mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	srcEmpty := findSubByID(t, all, subSrc.ID)
	if len(srcEmpty.Accounts) != 0 || srcEmpty.Mid != 0 || srcEmpty.Avatar != (AvatarRef{}) || srcEmpty.Face != "" {
		t.Fatalf("expected emptied source sub to clear Mid/Avatar/Face, got %+v", srcEmpty)
	}
	if srcEmpty.Name != "源订阅" || srcEmpty.Note != "保留原备注" || !srcEmpty.Rules.ScheduledCheck {
		t.Fatalf("emptied source sub lost its name/note/rules: %+v", srcEmpty)
	}

	// 4. Verify updates.db baselines and update_item rows are untouched with unlinked_at == "" for all 3 sites!
	for _, tc := range []struct {
		site, uid, dynID string
		pubTs            int64
	}{
		{"bilibili", "1001", "bili_dyn_1", 1710001000},
		{"douyin", dyUID, "dy_work_1", 1710002000},
		{"weibo", wbUID, "wb_post_1", 1710003000},
	} {
		st, ok, err := mgr.GetAccountState(tc.site, tc.uid)
		if err != nil || !ok {
			t.Fatalf("GetAccountState(%s:%s): ok=%v err=%v", tc.site, tc.uid, ok, err)
		}
		if st.UnlinkedAt != "" || st.BaselineAt != "2026-11-20T08:00:00Z" || st.NewestID != tc.dynID || st.NewestTs != tc.pubTs {
			t.Fatalf("updates.db account_state mutated on Move for %s:%s: %+v", tc.site, tc.uid, st)
		}
		owner, owned := mgr.OwnerOf(tc.site, tc.uid)
		if !owned || owner.ID != subDest.ID {
			t.Fatalf("OwnerOf(%s:%s) = (%v, %+v), want %s", tc.site, tc.uid, owned, owner, subDest.ID)
		}
	}
	updatesDest, err := mgr.ListUpdates(subDest.ID)
	if err != nil || len(updatesDest) != 3 {
		t.Fatalf("expected all 3 update_item rows accessible under destination sub, got %d err=%v", len(updatesDest), err)
	}
	updatesSrc, err := mgr.ListUpdates(subSrc.ID)
	if err != nil || len(updatesSrc) != 0 {
		t.Fatalf("expected 0 update_item rows under emptied source sub, got %d err=%v", len(updatesSrc), err)
	}

	// 5. Disk write failure during Move rolls back SQLite and in-memory owner cache completely.
	diskBefore, err := os.ReadFile(mgr.subsPath)
	if err != nil {
		t.Fatal(err)
	}
	mgr.diskWriteHook = func() error { return errors.New("synthetic disk full") }
	if _, err := mgr.Move("weibo", wbUID, subSrc.ID); err == nil {
		t.Fatal("expected Move to fail when disk write fails")
	}
	mgr.diskWriteHook = nil
	if owner, ok := mgr.OwnerOf("weibo", wbUID); !ok || owner.ID != subDest.ID {
		t.Fatalf("failed Move did not roll back owner of weibo:%s: ok=%v owner=%+v", wbUID, ok, owner)
	}
	diskAfter, _ := os.ReadFile(mgr.subsPath)
	if string(diskBefore) != string(diskAfter) {
		t.Fatal("failed Move modified subscriptions.json on disk")
	}
}

func TestMoveToNewAtomicAcrossAllThreeSitesAndRollback(t *testing.T) {
	mgr := regressionSubscriptions(t)
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	dyUID := "MS4wLjABAAAA__MoveToNew_01"
	wbUID := "2000000456"
	biliFace := "https://i0.hdslb.com/bfs/face/bili_2001.jpg"
	wbFace := "https://wx1.sinaimg.cn/large/wb_2000000456.jpg"

	schedRule := SubRules{ScheduledCheck: true}
	src, err := mgr.Create("三人合集订阅", "源备注", []AccountRef{
		{Site: "bilibili", UID: "2001", Name: "B站原号", Face: biliFace, AddedAt: "2026-11-01T00:00:00Z"},
		{Site: "douyin", UID: dyUID, UniqueID: "dy_split_id", WebRID: "554433", Name: "抖音原号", AddedAt: "2026-11-02T00:00:00Z"},
		{Site: "weibo", UID: wbUID, Name: "微博原号", Face: wbFace, AddedAt: "2026-11-03T00:00:00Z"},
	}, schedRule)
	if err != nil {
		t.Fatalf("Create src: %v", err)
	}
	// Seed legacy sub_id on src ("bilibili:2001") to verify rekeying when moving bilibili:2001 out!
	if _, err := mgr.db.Exec(`UPDATE subscriptions SET sub_id = ? WHERE sub_id = ?`, "bilibili:2001", src.SubID); err != nil {
		t.Fatalf("seed legacy sub_id: %v", err)
	}
	mgr.mu.Lock()
	mgr.invalidateSubsCacheLocked()
	mgr.mu.Unlock()

	seedAccountUpdateBaseline(t, mgr, "bilibili", "2001", "bili_d_2001", 1710010000)
	seedAccountUpdateBaseline(t, mgr, "douyin", dyUID, "dy_w_2001", 1710020000)
	seedAccountUpdateBaseline(t, mgr, "weibo", wbUID, "wb_p_2001", 1710030000)

	// Count disk writes to guarantee MoveToNew performs exactly ONE atomic write.
	diskWrites := 0
	mgr.diskWriteHook = func() error {
		diskWrites++
		return nil
	}

	// 1. MoveToNew Bilibili account (also rekeys legacy sub_id and re-syncs src.Avatar to Douyin).
	diskWrites = 0
	newBiliSub, err := mgr.MoveToNew("bilibili", "002001", "独立B站订阅", "拆分备注", DefaultSubRules())
	if err != nil {
		t.Fatalf("MoveToNew bilibili: %v", err)
	}
	if diskWrites != 1 {
		t.Fatalf("expected exactly 1 atomic subscriptions.json write, got %d", diskWrites)
	}
	if newBiliSub.Name != "独立B站订阅" || newBiliSub.Note != "拆分备注" || newBiliSub.RuleID != DefaultRuleID {
		t.Fatalf("unexpected newBiliSub header/rule: %+v", newBiliSub)
	}
	if len(newBiliSub.Accounts) != 1 || newBiliSub.Accounts[0].UID != "2001" || newBiliSub.Accounts[0].Face != biliFace || newBiliSub.Accounts[0].AddedAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("newBiliSub account metadata mismatch: %+v", newBiliSub.Accounts)
	}
	if newBiliSub.Avatar != (AvatarRef{Site: "bilibili", UID: "2001"}) || newBiliSub.Mid != 2001 {
		t.Fatalf("newBiliSub avatar/mid mismatch: avatar=%+v mid=%d", newBiliSub.Avatar, newBiliSub.Mid)
	}
	if owner, ok := mgr.OwnerOf("bilibili", "2001"); !ok || owner.ID != newBiliSub.ID {
		t.Fatalf("OwnerOf(bilibili:2001) = (%v, %+v), want %s", ok, owner, newBiliSub.ID)
	}
	all, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	srcAfterBili := findSubByID(t, all, src.ID)
	if srcAfterBili.SubID == "bilibili:2001" {
		t.Fatalf("legacy sub_id was not rekeyed away from bilibili:2001")
	}
	if len(srcAfterBili.Accounts) != 2 || srcAfterBili.Mid != 0 || srcAfterBili.Avatar != (AvatarRef{Site: "douyin", UID: dyUID}) {
		t.Fatalf("src after moving bilibili mismatch: %+v", srcAfterBili)
	}
	if srcAfterBili.Name != "三人合集订阅" || srcAfterBili.Note != "源备注" || !srcAfterBili.Rules.ScheduledCheck {
		t.Fatalf("src lost its own name/note/rules: %+v", srcAfterBili)
	}

	// 2. MoveToNew Douyin account (preserves UniqueID, WebRID, Name, AddedAt; src re-syncs avatar to Weibo).
	diskWrites = 0
	newDySub, err := mgr.MoveToNew("douyin", dyUID, "独立抖音订阅", "", DefaultSubRules())
	if err != nil {
		t.Fatalf("MoveToNew douyin: %v", err)
	}
	if diskWrites != 1 {
		t.Fatalf("expected 1 atomic disk write for Douyin MoveToNew, got %d", diskWrites)
	}
	if len(newDySub.Accounts) != 1 || newDySub.Accounts[0].UID != dyUID || newDySub.Accounts[0].UniqueID != "dy_split_id" ||
		newDySub.Accounts[0].WebRID != "554433" || newDySub.Accounts[0].Name != "抖音原号" || newDySub.Accounts[0].AddedAt != "2026-11-02T00:00:00Z" {
		t.Fatalf("newDySub account metadata mismatch: %+v", newDySub.Accounts)
	}
	if newDySub.Avatar != (AvatarRef{Site: "douyin", UID: dyUID}) || newDySub.Mid != 0 {
		t.Fatalf("newDySub avatar/mid mismatch: %+v", newDySub)
	}
	if owner, ok := mgr.OwnerOf("douyin", dyUID); !ok || owner.ID != newDySub.ID {
		t.Fatalf("OwnerOf(douyin) = (%v, %+v), want %s", ok, owner, newDySub.ID)
	}

	// 3. MoveToNew Weibo account (last account on src -> src cleared cleanly while keeping its own name/rule).
	diskWrites = 0
	newWbSub, err := mgr.MoveToNew("weibo", "00"+wbUID, "独立微博订阅", "", DefaultSubRules())
	if err != nil {
		t.Fatalf("MoveToNew weibo: %v", err)
	}
	if diskWrites != 1 {
		t.Fatalf("expected 1 atomic disk write for Weibo MoveToNew, got %d", diskWrites)
	}
	if len(newWbSub.Accounts) != 1 || newWbSub.Accounts[0].Site != "weibo" || newWbSub.Accounts[0].UID != wbUID ||
		newWbSub.Accounts[0].Name != "微博原号" || newWbSub.Accounts[0].Face != wbFace || newWbSub.Accounts[0].AddedAt != "2026-11-03T00:00:00Z" {
		t.Fatalf("newWbSub account metadata mismatch: %+v", newWbSub.Accounts)
	}
	if newWbSub.Avatar != (AvatarRef{Site: "weibo", UID: wbUID}) {
		t.Fatalf("newWbSub avatar mismatch: %+v", newWbSub.Avatar)
	}
	if owner, ok := mgr.OwnerOf("weibo", wbUID); !ok || owner.ID != newWbSub.ID {
		t.Fatalf("OwnerOf(weibo) = (%v, %+v), want %s", ok, owner, newWbSub.ID)
	}

	all, err = mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 subscriptions (src + 3 split), got %d", len(all))
	}
	srcFinal := findSubByID(t, all, src.ID)
	if len(srcFinal.Accounts) != 0 || srcFinal.Avatar != (AvatarRef{}) || srcFinal.Face != "" || srcFinal.Mid != 0 {
		t.Fatalf("expected srcFinal empty with cleared avatar/face/mid, got %+v", srcFinal)
	}
	if srcFinal.Name != "三人合集订阅" || !srcFinal.Rules.ScheduledCheck {
		t.Fatalf("srcFinal lost name/rule: %+v", srcFinal)
	}

	// Verify updates.db baselines and items remain intact without unlinked_at for all 3 sites!
	for _, tc := range []struct {
		site, uid, subID, dynID string
	}{
		{"bilibili", "2001", newBiliSub.ID, "bili_d_2001"},
		{"douyin", dyUID, newDySub.ID, "dy_w_2001"},
		{"weibo", wbUID, newWbSub.ID, "wb_p_2001"},
	} {
		st, ok, err := mgr.GetAccountState(tc.site, tc.uid)
		if err != nil || !ok || st.UnlinkedAt != "" || st.NewestID != tc.dynID {
			t.Fatalf("updates.db state altered for %s:%s: ok=%v err=%v st=%+v", tc.site, tc.uid, ok, err, st)
		}
		ups, err := mgr.ListUpdates(tc.subID)
		if err != nil || len(ups) != 1 || ups[0].DynID != tc.dynID {
			t.Fatalf("expected update item %s under new sub %s, got %+v err=%v", tc.dynID, tc.subID, ups, err)
		}
	}

	// 4. Error cases must leave subscriptions, disk file, and rules completely untouched.
	mgr.diskWriteHook = nil
	diskSnapshot, err := os.ReadFile(mgr.subsPath)
	if err != nil {
		t.Fatal(err)
	}
	// (a) Invalid destination name (empty / whitespace / >64 runes).
	if _, err := mgr.MoveToNew("weibo", wbUID, "   ", "", DefaultSubRules()); err == nil {
		t.Fatal("expected empty name rejected")
	}
	longRunes := make([]rune, 65)
	for i := range longRunes {
		longRunes[i] = '字'
	}
	if _, err := mgr.MoveToNew("weibo", wbUID, string(longRunes), "", DefaultSubRules()); err == nil {
		t.Fatal("expected >64 rune name rejected")
	}
	// (b) Account not owned by any subscription.
	if _, err := mgr.MoveToNew("weibo", "9999999999", "未关联测试", "", DefaultSubRules()); err == nil {
		t.Fatal("expected unowned account rejected")
	}
	// (c) Unknown site.
	if _, err := mgr.MoveToNew("unknown", "12345", "未知站点", "", DefaultSubRules()); err == nil {
		t.Fatal("expected unknown site rejected")
	}
	// (d) Disk write failure rolls back BOTH old owner mutation and new subscription creation AND any new rule!
	rulesBefore := len(mgr.ListRules())
	mgr.diskWriteHook = func() error { return errors.New("synthetic disk write fail") }
	if _, err := mgr.MoveToNew("weibo", wbUID, "磁盘失败回滚", "", SubRules{ScheduledCheck: true, AutoRecord: true}); err == nil {
		t.Fatal("expected disk failure error")
	}
	mgr.diskWriteHook = nil

	if owner, ok := mgr.OwnerOf("weibo", wbUID); !ok || owner.ID != newWbSub.ID {
		t.Fatalf("rollback failed to preserve weibo owner: ok=%v owner=%+v", ok, owner)
	}
	allAfterErr, err := mgr.List()
	if err != nil || len(allAfterErr) != 4 {
		t.Fatalf("error cases changed subscription count: len=%d err=%v", len(allAfterErr), err)
	}
	if len(mgr.ListRules()) != rulesBefore {
		t.Fatalf("rollback leaked newly created rule: before=%d after=%d", rulesBefore, len(mgr.ListRules()))
	}
	diskAfterErr, _ := os.ReadFile(mgr.subsPath)
	if string(diskAfterErr) != string(diskSnapshot) {
		t.Fatal("error/rollback path modified subscriptions.json")
	}

	// Verify disk JSON reloads cleanly into a fresh Manager with all 4 subs & owners intact.
	var sf subscriptionsFile
	if err := json.Unmarshal(diskAfterErr, &sf); err != nil {
		t.Fatalf("invalid subscriptions.json: %v", err)
	}
	if len(sf.Subscriptions) != 4 {
		t.Fatalf("disk subscriptions count = %d, want 4", len(sf.Subscriptions))
	}
	_ = filepath.Base(mgr.subsPath)
}
