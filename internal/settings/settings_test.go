package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsCorruptRecoveryAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	cacheDir := filepath.Join(dir, "cache")

	// 1. Corrupt JSON is backed up and reset to default.
	if err := os.WriteFile(path, []byte("{corrupt-json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	store, recovered, backup, err := Open(path, cacheDir)
	if err != nil {
		t.Fatalf("Open corrupt settings: %v", err)
	}
	if !recovered || !strings.Contains(filepath.Base(backup), "settings.json.corrupt-") {
		t.Fatalf("expected corrupt recovery, got recovered=%v backup=%q", recovered, backup)
	}
	got := store.Get()
	if got.Concurrency != 2 || got.Download.MaxConcurrentTasks != 2 || got.CacheDir != filepath.Clean(cacheDir) {
		t.Fatalf("unexpected default settings: %+v", got)
	}
	if got.ScheduledCheck || got.CheckIntervalMin != 60 || got.LiveCheckIntervalMin != 3 || got.MaxRecordings != 2 {
		t.Fatalf("unexpected Q-006 defaults: scheduledCheck=%v checkIntervalMin=%d liveCheckIntervalMin=%d maxRecordings=%d",
			got.ScheduledCheck, got.CheckIntervalMin, got.LiveCheckIntervalMin, got.MaxRecordings)
	}
	if got.VideoQuality != "best" || got.AudioQuality != "best" || got.MultiPart != "all" || !got.UpgradeCheckOnOpen || !got.AISubtitles || !got.WeiboRemoveWatermark {
		t.Fatalf("unexpected media defaults: %+v", got)
	}
	if got.Version != 1 || got.Companion.Enabled || got.Companion.PreventSleep {
		t.Fatalf("expected settings v1 with companion disabled and preventSleep=false by default: %+v", got.Companion)
	}

	// 2. Unknown fields are tolerated while valid fields are loaded.
	withUnknown := `{
  "version": 1,
  "futureField": "ignored",
  "videoQuality": "1080",
  "audioQuality": "192k",
  "multiPart": "first",
  "concurrency": 3,
  "scheduledCheck": true,
  "checkIntervalMin": 30,
  "liveCheckIntervalMin": 5,
  "maxRecordings": 3,
  "download": {
    "maxConcurrentTasks": 3,
    "preferredVideoCodec": "hevc",
    "unknownDownloadFlag": 42
  }
}`
	if err := os.WriteFile(path, []byte(withUnknown), 0o600); err != nil {
		t.Fatalf("write unknown fields json: %v", err)
	}
	store2, recovered2, _, err := Open(path, cacheDir)
	if err != nil {
		t.Fatalf("Open with unknown fields: %v", err)
	}
	if recovered2 {
		t.Fatalf("expected recovered2=false for valid JSON with unknown fields")
	}
	got2 := store2.Get()
	if got2.Concurrency != 3 || got2.Download.MaxConcurrentTasks != 3 || got2.VideoQuality != "1080" || got2.AudioQuality != "192k" || got2.MultiPart != "first" {
		t.Fatalf("unexpected loaded settings: %+v", got2)
	}
	if !got2.ScheduledCheck || got2.CheckIntervalMin != 30 || got2.LiveCheckIntervalMin != 5 || got2.MaxRecordings != 3 || !got2.WeiboRemoveWatermark {
		t.Fatalf("unexpected loaded schedule/live/watermark settings: %+v", got2)
	}

	// 3. Invalid validation values are rejected on Update.
	bad := got2
	bad.CheckIntervalMin = 5
	bad.Subscription.IntervalMinutes = 5
	if _, err := store2.Update(bad); err == nil {
		t.Fatalf("expected validation error for checkIntervalMin < 15")
	}
}

func TestSettingsCanToggleRulesAndRestoreDefaults(t *testing.T) {
	dir := t.TempDir()
	store, _, _, err := Open(filepath.Join(dir, "settings.json"), filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.ScheduledCheck, next.Danmaku, next.Subtitles, next.WeiboRemoveWatermark = true, false, false, false
	next.Companion.PreventSleep = true
	next.AudioQuality, next.CloseAction = "192k", "quit"
	next.LiveCheckIntervalMin, next.Concurrency, next.MaxRecordings = 5, 3, 3
	if _, err := store.Update(next); err != nil {
		t.Fatal(err)
	}
	reloadedOff, _, _, err := Open(filepath.Join(dir, "settings.json"), filepath.Join(dir, "cache"))
	if err != nil || reloadedOff.Get().WeiboRemoveWatermark || !reloadedOff.Get().Companion.PreventSleep || reloadedOff.Get().Version != 1 {
		t.Fatalf("settings did not persist across Open: %+v err=%v", reloadedOff, err)
	}
	next = store.Get()
	next.ScheduledCheck, next.Danmaku, next.Subtitles, next.WeiboRemoveWatermark = false, true, true, true
	next.AudioQuality, next.CloseAction = "best", "tray"
	next.LiveCheckIntervalMin, next.Concurrency, next.MaxRecordings = 3, 2, 2
	after, err := store.Update(next)
	if err != nil {
		t.Fatal(err)
	}
	if after.Subscription.Enabled || !after.Download.SaveDanmaku || !after.Download.SaveSubtitles || !after.Download.PreferLosslessAudio || !after.Download.PreferDolbyAudio || after.Window.CloseAction != "tray" || after.Live.PollIntervalSeconds != 180 || after.Download.MaxConcurrentTasks != 2 || after.Live.MaxConcurrentRecordings != 2 {
		t.Fatalf("stale nested settings prevented user changes: %+v", after)
	}
	loaded, _, _, err := Open(filepath.Join(dir, "settings.json"), filepath.Join(dir, "cache"))
	if err != nil || loaded.Get().ScheduledCheck || !loaded.Get().Danmaku {
		t.Fatalf("saved settings did not round trip: %+v %v", loaded, err)
	}
}

func TestSettingsCloneIsolatesRemoteSlicesAndPollInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	store, _, _, err := Open(path, filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.Live.PollIntervalSeconds = 45
	next.Remote.Connections = []RemoteConnection{{ID: "r1", Name: "srv", URL: "https://example.com"}}
	next.Remote.PendingCookies = []PendingCookie{{Site: "bilibili", Since: "2026-01-01T00:00:00Z"}}
	updated, err := store.Update(next)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.LiveCheckIntervalMin != 1 || updated.Live.PollIntervalSeconds != 60 {
		t.Fatalf("45s poll interval = (%d min, %d sec), want (1, 60)", updated.LiveCheckIntervalMin, updated.Live.PollIntervalSeconds)
	}
	// Mutating caller or returned slices must not mutate committed state.
	next.Remote.Connections[0].Name = "mutated-input"
	updated.Remote.Connections[0].Name = "mutated-return"
	next.Remote.PendingCookies[0].Site = "douyin"
	updated.Remote.PendingCookies[0].Site = "douyin"
	got := store.Get()
	if got.Remote.Connections[0].Name != "srv" || got.Remote.PendingCookies[0].Site != "bilibili" {
		t.Fatalf("committed Remote slices were aliased: %+v", got.Remote)
	}

	newerPath := filepath.Join(dir, "newer-settings.json")
	if err := os.WriteFile(newerPath, []byte(`{"version":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Open(newerPath, filepath.Join(dir, "cache")); err == nil {
		t.Fatal("Open(version=5) succeeded, want NewerDataError")
	}
}

func TestSettingsAutoStartOptionalAndKeepsSchemaV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	cacheDir := filepath.Join(dir, "cache")

	// Existing settings.json without window.autoStart loads cleanly with AutoStart=false and Version=1.
	if err := os.WriteFile(path, []byte(`{"version":1,"window":{"closeAction":"tray"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, recovered, _, err := Open(path, cacheDir)
	if err != nil || recovered {
		t.Fatalf("Open without autoStart: recovered=%v err=%v", recovered, err)
	}
	cur := store.Get()
	if cur.Version != 1 || cur.Window.AutoStart {
		t.Fatalf("unexpected initial autostart settings: version=%d autoStart=%v", cur.Version, cur.Window.AutoStart)
	}

	cur.Window.AutoStart = true
	updated, err := store.Update(cur)
	if err != nil {
		t.Fatalf("Update(AutoStart=true): %v", err)
	}
	if updated.Version != 1 || !updated.Window.AutoStart {
		t.Fatalf("updated settings = %+v, want version=1 autoStart=true", updated)
	}

	reopened, recovered2, _, err := Open(path, cacheDir)
	if err != nil || recovered2 {
		t.Fatalf("reopen settings: recovered=%v err=%v", recovered2, err)
	}
	if got := reopened.Get(); got.Version != 1 || !got.Window.AutoStart {
		t.Fatalf("reopened settings = %+v, want version=1 autoStart=true", got)
	}
}
