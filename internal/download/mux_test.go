package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func TestInspectMP4StructureContextMatchesFullInspectAndRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	vBytes, err := BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := BuildSyntheticAudioM4S(47)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	vPath := filepath.Join(dir, "v.m4s")
	aPath := filepath.Join(dir, "a.m4s")
	outPath := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(vPath, vBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MuxDASHFiles(vPath, aPath, outPath); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}

	full, err := InspectMP4FileContext(context.Background(), outPath)
	if err != nil {
		t.Fatalf("InspectMP4FileContext: %v", err)
	}
	structure, err := InspectMP4StructureContext(context.Background(), outPath)
	if err != nil {
		t.Fatalf("InspectMP4StructureContext: %v", err)
	}
	if full.SHA256 == "" {
		t.Fatal("InspectMP4FileContext returned empty SHA256")
	}
	if structure.SHA256 != "" {
		t.Fatalf("InspectMP4StructureContext returned non-empty SHA256 %q", structure.SHA256)
	}
	fullWithoutHash := full
	fullWithoutHash.SHA256 = ""
	if !reflect.DeepEqual(structure, fullWithoutHash) {
		t.Fatalf("InspectMP4StructureContext mismatch:\ngot:  %+v\nwant: %+v", structure, fullWithoutHash)
	}
	if !structure.MoovBeforeMdat || structure.TrackCount != 2 || structure.FileBytes <= 0 || structure.DurationSec <= 0 {
		t.Fatalf("unexpected structure summary: %+v", structure)
	}

	// Truncated MP4 must be rejected.
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	truncPath := filepath.Join(dir, "truncated.mp4")
	if err := os.WriteFile(truncPath, raw[:len(raw)-16], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectMP4StructureContext(context.Background(), truncPath); err == nil {
		t.Fatal("InspectMP4StructureContext accepted truncated MP4")
	}

	// Corrupt non-MP4 payload must be rejected.
	badPath := filepath.Join(dir, "corrupt.mp4")
	if err := os.WriteFile(badPath, []byte("not-an-mp4-container"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectMP4StructureContext(context.Background(), badPath); err == nil {
		t.Fatal("InspectMP4StructureContext accepted corrupt payload")
	}

	// Cancelled context must fail promptly.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectMP4StructureContext(canceledCtx, outPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context err = %v, want context.Canceled", err)
	}
}
