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
	if got.VideoQuality != "best" || got.AudioQuality != "best" || got.MultiPart != "all" || !got.UpgradeCheckOnOpen || !got.AISubtitles {
		t.Fatalf("unexpected media defaults: %+v", got)
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
	if !got2.ScheduledCheck || got2.CheckIntervalMin != 30 || got2.LiveCheckIntervalMin != 5 || got2.MaxRecordings != 3 {
		t.Fatalf("unexpected loaded schedule/live settings: %+v", got2)
	}

	// 3. Invalid validation values are rejected on Update.
	bad := got2
	bad.CheckIntervalMin = 5
	bad.Subscription.IntervalMinutes = 5
	if _, err := store2.Update(bad); err == nil {
		t.Fatalf("expected validation error for checkIntervalMin < 15")
	}
}

func TestSettingsSanitizeAndCorruptRecovery(t *testing.T) {
	TestSettingsCorruptRecoveryAndUnknownFields(t)
}

func TestSettingsCanToggleRulesAndRestoreDefaults(t *testing.T) {
	dir := t.TempDir()
	store, _, _, err := Open(filepath.Join(dir, "settings.json"), filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.ScheduledCheck, next.Danmaku, next.Subtitles = true, false, false
	next.AudioQuality, next.CloseAction = "192k", "quit"
	next.LiveCheckIntervalMin, next.Concurrency, next.MaxRecordings = 5, 3, 3
	if _, err := store.Update(next); err != nil {
		t.Fatal(err)
	}
	next = store.Get()
	next.ScheduledCheck, next.Danmaku, next.Subtitles = false, true, true
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
