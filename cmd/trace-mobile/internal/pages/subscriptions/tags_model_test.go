package subscriptions

import (
	"slices"
	"testing"
)

func TestMobileTrackingPresetsAndAutoCacheDependencyWarning(t *testing.T) {
	full := applyMobilePreset(presetFullArchive, true)
	presetID, autoRec := matchMobileTrackingPreset(full)
	if presetID != presetFullArchive || !autoRec {
		t.Fatalf("matchMobileTrackingPreset(full+live) = (%q, %v)", presetID, autoRec)
	}
	if hasAutoCacheWithoutScheduledCheck(full) {
		t.Fatal("full_archive has scheduled_check, must not warn")
	}

	// Removing scheduled_check while auto-cache tags remain triggers gentle dependency warning.
	noSched := toggleMobileTag(full, tagScheduledCheck)
	if !hasAutoCacheWithoutScheduledCheck(noSched) {
		t.Fatal("expected gentle dependency warning when auto-cache is on without scheduled_check")
	}
	presetID, autoRec = matchMobileTrackingPreset(noSched)
	if presetID != presetCustom || !autoRec {
		t.Fatalf("expected custom + autoRecord=true, got (%q, %v)", presetID, autoRec)
	}

	// Switching to 仅手动 preserves autoRecord overlay when requested and clears warning.
	manual := applyMobilePreset(presetManual, false)
	if len(manual) != 0 || hasAutoCacheWithoutScheduledCheck(manual) {
		t.Fatalf("expected empty tags for manual without autoRecord, got %v", manual)
	}
	follow := applyMobilePreset(presetFollowCache, false)
	wantFollow := []string{tagScheduledCheck, tagCacheVideo, tagCacheDraw}
	if !slices.Equal(follow, wantFollow) {
		t.Fatalf("follow_cache tags = %v, want %v", follow, wantFollow)
	}
}
