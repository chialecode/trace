package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

func TestMoveSubscriptionAccountAndMoveToNewAllSitesZeroNetwork(t *testing.T) {
	var httpCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/web-interface/nav" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"测试账号"}}`))
			return
		}
		httpCalls.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	root := t.TempDir()
	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(root, "data"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "data", "credentials"),
		LogsDir:         filepath.Join(root, "data", "logs"),
		LockFile:        filepath.Join(root, "data", "app.lock"),
	}
	b, err := NewBackend(Options{
		Locations: loc,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			AllowLocalhost: true,
			PacingInterval: time.Millisecond,
		},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()

	// Must require at least one site login before mutating subscriptions.
	if _, err := b.MoveSubscriptionAccountToNew("bilibili", "1001", "未登录测试", "", subscription.DefaultSubRules()); err == nil {
		t.Fatal("expected auth_required when logged out")
	}

	if _, err := b.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	var mu sync.Mutex
	var events []Event
	unsub := b.SubscribeEvents(func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	defer unsub()

	dyUID := "MS4wLjABAAAA__ServiceMove_01"
	wbUID := "3000000789"
	subA, err := b.CreateSubscription("源订阅A", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "B站号"},
		{Site: "douyin", UID: dyUID, UniqueID: "dy_svc_id", WebRID: "667788", Name: "抖音号"},
		{Site: "weibo", UID: wbUID, Name: "微博号"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription A: %v", err)
	}
	subB, err := b.CreateSubscription("目标订阅B", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "2002", Name: "已有号"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription B: %v", err)
	}

	httpCalls.Store(0)
	mu.Lock()
	events = nil
	mu.Unlock()

	// Move Douyin to subB via Backend.MoveSubscriptionAccount.
	movedB, err := b.MoveSubscriptionAccount("douyin", dyUID, subB.ID)
	if err != nil {
		t.Fatalf("MoveSubscriptionAccount douyin: %v", err)
	}
	if movedB.ID != subB.ID || len(movedB.Accounts) != 2 || movedB.Accounts[1].UniqueID != "dy_svc_id" || movedB.Accounts[1].WebRID != "667788" {
		t.Fatalf("unexpected movedB: %+v", movedB)
	}
	if owner, ok := b.OwnerOfAccount("douyin", dyUID); !ok || owner.ID != subB.ID {
		t.Fatalf("OwnerOfAccount(douyin) = (%v, %+v), want %s", ok, owner, subB.ID)
	}

	// Move Weibo and Bilibili each into brand-new subscriptions via MoveSubscriptionAccountToNew.
	newWb, err := b.MoveSubscriptionAccountToNew("weibo", wbUID, "微博独立订阅", "来自A", subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("MoveSubscriptionAccountToNew weibo: %v", err)
	}
	if newWb.Name != "微博独立订阅" || newWb.Note != "来自A" || len(newWb.Accounts) != 1 || newWb.Accounts[0].UID != wbUID {
		t.Fatalf("unexpected newWb: %+v", newWb)
	}
	if owner, ok := b.OwnerOfAccount("weibo", wbUID); !ok || owner.ID != newWb.ID {
		t.Fatalf("OwnerOfAccount(weibo) = (%v, %+v), want %s", ok, owner, newWb.ID)
	}

	newBili, err := b.MoveSubscriptionAccountToNew("bilibili", "1001", "B站独立订阅", "", subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("MoveSubscriptionAccountToNew bilibili: %v", err)
	}
	if newBili.Name != "B站独立订阅" || len(newBili.Accounts) != 1 || newBili.Accounts[0].UID != "1001" || newBili.Mid != 1001 {
		t.Fatalf("unexpected newBili: %+v", newBili)
	}

	// Verify subA now has 0 accounts and cleared avatar, and total subscriptions = 4.
	subs, err := b.ListSubscriptions()
	if err != nil || len(subs) != 4 {
		t.Fatalf("ListSubscriptions = %d err=%v, want 4", len(subs), err)
	}
	for _, s := range subs {
		if s.ID == subA.ID {
			if len(s.Accounts) != 0 || s.Avatar != (subscription.AvatarRef{}) || s.Mid != 0 {
				t.Fatalf("expected subA empty after moving all accounts, got %+v", s)
			}
		}
	}

	// Strictly zero network requests and 3 "moved" subscription events published.
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("expected 0 HTTP requests during account moves, got %d", got)
	}
	mu.Lock()
	movedEvents := 0
	for _, ev := range events {
		if ev.Type == EventSubscriptionChecked && ev.Message == "moved" {
			movedEvents++
		}
	}
	mu.Unlock()
	if movedEvents != 3 {
		t.Fatalf("expected 3 moved events, got %d", movedEvents)
	}
}
