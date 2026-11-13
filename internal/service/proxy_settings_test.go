package service

import (
	"testing"

	"github.com/chialecode/trace/internal/site"
)

// DEC-046 ⑤: Settings → 网络 switches every site request at once, without a
// restart; a bad custom address is refused and leaves the proxy alone; the
// choice survives a restart.
func TestProxySettingAppliesAtOnceAndPersists(t *testing.T) {
	t.Cleanup(func() { _ = site.SetProxy(site.ProxySystem, "") })
	loc := testLocations(t)
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.GetSettings().Network.ProxyMode; got != "system" || site.ProxyMode() != site.ProxySystem {
		t.Fatalf("default proxy mode %q / %q, want system", got, site.ProxyMode())
	}
	next := b.GetSettings()
	next.Network.ProxyMode, next.Network.ProxyURL = "custom", "ftp://nope:1"
	if _, err := b.UpdateSettings(next); err == nil {
		t.Fatal("a bad custom proxy was saved")
	}
	if site.ProxyMode() != site.ProxySystem {
		t.Fatal("a refused address changed the proxy in use")
	}
	next.Network.ProxyURL = "socks5://127.0.0.1:1080"
	if _, err := b.UpdateSettings(next); err != nil {
		t.Fatal(err)
	}
	if site.ProxyMode() != site.ProxyCustom {
		t.Fatalf("custom proxy not applied at once: %q", site.ProxyMode())
	}
	next.Network.ProxyMode = "none"
	if _, err := b.UpdateSettings(next); err != nil || site.ProxyMode() != site.ProxyNone {
		t.Fatalf("no-proxy mode not applied: %v %q", err, site.ProxyMode())
	}
	b.Close()

	_ = site.SetProxy(site.ProxySystem, "")
	b2, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if site.ProxyMode() != site.ProxyNone {
		t.Fatalf("restart lost the proxy choice: %q", site.ProxyMode())
	}
}
