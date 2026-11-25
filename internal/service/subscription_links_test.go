package service

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

func TestBackendSubscriptionCustomLinksAndSafeOpenExternalLink(t *testing.T) {
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
	desk := NewMockDesktop()
	b, err := NewBackend(Options{
		Locations:        loc,
		BilibiliOpts:     bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: time.Millisecond},
		Desktop:          desk,
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()

	// Require login before mutating subscription links.
	if _, err := b.SetSubscriptionLinks("missing", []subscription.CustomLink{{URL: "https://link3.cc/a"}}); err == nil {
		t.Fatal("expected SetSubscriptionLinks to require login")
	}

	if _, err := b.cred.SaveManual("SESSDATA=synth_bili_cookie; DedeUserID=99001", ""); err != nil {
		t.Fatalf("SaveManual: %v", err)
	}

	var mu sync.Mutex
	var events []string
	unsub := b.SubscribeEvents(func(ev Event) {
		if ev.Type == EventSubscriptionChecked {
			mu.Lock()
			events = append(events, ev.Message)
			mu.Unlock()
		}
	})
	defer unsub()

	sub, err := b.CreateSubscription("测试订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "UP"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	want := []subscription.CustomLink{
		{URL: "https://link3.cc/test_creator"},
		{Label: "个人主页", URL: "https://linktr.ee/test_creator"},
	}
	updated, err := b.SetSubscriptionLinks(sub.ID, want)
	if err != nil {
		t.Fatalf("SetSubscriptionLinks: %v", err)
	}
	if !reflect.DeepEqual(updated.Links, want) {
		t.Fatalf("updated.Links = %+v, want %+v", updated.Links, want)
	}

	// Rule changes preserve custom links.
	rule, err := b.SaveCacheRule(subscription.CacheRule{Name: "定时规则", ScheduledCheck: true})
	if err != nil {
		t.Fatalf("SaveCacheRule: %v", err)
	}
	afterRule, err := b.SetSubscriptionRule(sub.ID, rule.ID)
	if err != nil {
		t.Fatalf("SetSubscriptionRule: %v", err)
	}
	if !reflect.DeepEqual(afterRule.Links, want) {
		t.Fatalf("SetSubscriptionRule lost Links: %+v", afterRule.Links)
	}

	mu.Lock()
	hasLinksUpdated := false
	for _, msg := range events {
		if msg == "links_updated" {
			hasLinksUpdated = true
		}
	}
	mu.Unlock()
	if !hasLinksUpdated {
		t.Fatalf("expected links_updated event, got %v", events)
	}

	// OpenExternalLink records clean HTTP/HTTPS URLs and rejects unsafe schemes with 0 shell calls.
	for _, bad := range []string{
		"javascript:alert(1)",
		"file:///C:/Windows/win.ini",
		"data:text/plain,hello",
		"link3.cc/no-scheme",
		"https://user:pass@link3.cc/a",
	} {
		if err := b.OpenExternalLink(bad); err == nil {
			t.Fatalf("OpenExternalLink(%q) succeeded, want rejection", bad)
		}
	}
	if len(desk.Actions()) != 0 {
		t.Fatalf("unsafe URLs recorded desktop actions: %+v", desk.Actions())
	}

	if err := b.OpenExternalLink("https://Link3.cc/test_creator"); err != nil {
		t.Fatalf("OpenExternalLink valid URL: %v", err)
	}
	acts := desk.Actions()
	if len(acts) != 1 || acts[0].Kind != "url" || acts[0].Target != "https://link3.cc/test_creator" {
		t.Fatalf("unexpected desktop actions: %+v", acts)
	}
	_ = context.Background()
}
