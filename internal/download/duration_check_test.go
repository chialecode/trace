package download

import (
	"strings"
	"testing"
)

// A Bilibili page listed at 129 s whose DASH streams both run 126.5 s (dev
// 2026-11-18, BV1tw411t7Pu P1) is a complete download: the merged MP4 is
// kept. A file far shorter than stated is still refused.
func TestValidateVideoOutputToleratesShortTranscode(t *testing.T) {
	ok := MuxTrackSummary{Handlers: []string{"vide", "soun"}, MoovBeforeMdat: true, HasElst: true, DurationSec: 126.5}
	if err := validateVideoOutput(ok, 129, true); err != nil {
		t.Fatalf("126.5 s of a 129 s page refused: %v", err)
	}
	short := ok
	short.DurationSec = 60
	err := validateVideoOutput(short, 129, true)
	if err == nil || !strings.Contains(err.Error(), "相差超过") {
		t.Fatalf("60 s of a 129 s page: %v", err)
	}
	noAudio := ok
	noAudio.Handlers = []string{"vide"}
	if err := validateVideoOutput(noAudio, 129, true); err == nil || !strings.Contains(err.Error(), "音频轨") {
		t.Fatalf("missing audio track: %v", err)
	}
	// Short videos keep the 2-second floor.
	if durationMismatch(12.5, 10) == "" || durationMismatch(11.9, 10) != "" {
		t.Fatal("2-second floor")
	}
}
