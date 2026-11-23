package download

import "testing"

func TestTrackLengthsDisagreeScalesWithLength(t *testing.T) {
	cases := []struct {
		name     string
		duration float64
		diff     float64
		want     bool
	}{
		// Real case (2026-11-23): 564.0 s video, 569.1 s audio.
		{"long video, 5.1 s drift (0.9 %)", 569.1, 5.1, false},
		{"long video, 20 s drift (3.5 %)", 569.1, 20, true},
		{"short video keeps the 2 s limit", 60, 2.5, true},
		{"short video within 2 s", 60, 1.9, false},
		{"unknown duration keeps the 2 s limit", 0, 2.5, true},
	}
	for _, tc := range cases {
		got := trackLengthsDisagree(MuxTrackSummary{DurationSec: tc.duration, MaxTrackDiffSec: tc.diff})
		if got != tc.want {
			t.Errorf("%s: trackLengthsDisagree = %v, want %v", tc.name, got, tc.want)
		}
	}
}
