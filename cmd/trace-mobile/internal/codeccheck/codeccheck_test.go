package codeccheck

import "testing"

func TestParseCodecHints(t *testing.T) {
	cases := []struct {
		hints  []string
		vMIME  string
		vLabel string
		aMIME  string
		aLabel string
	}{
		{[]string{"1080P 高码率 · AV1 · 杜比全景声"}, "video/av01", "AV1", "audio/eac3", "杜比音轨"},
		{[]string{"av01.0.08M.08"}, "video/av01", "AV1", "", ""},
		{[]string{"4K · HEVC", "FLAC"}, "video/hevc", "HEVC", "audio/flac", "FLAC"},
		{[]string{"hvc1.1.6.L120.90"}, "video/hevc", "HEVC", "", ""},
		{[]string{"原画 · AVC"}, "video/avc", "AVC", "", ""},
		{[]string{"vp09.00.41.08"}, "video/x-vnd.on2.vp9", "VP9", "", ""},
		{[]string{"Hi-Res 无损"}, "", "", "audio/flac", "FLAC"},
	}
	for _, tc := range cases {
		vm, vl, am, al := ParseCodecHints(tc.hints...)
		if vm != tc.vMIME || vl != tc.vLabel || am != tc.aMIME || al != tc.aLabel {
			t.Fatalf("ParseCodecHints(%v) = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
				tc.hints, vm, vl, am, al, tc.vMIME, tc.vLabel, tc.aMIME, tc.aLabel)
		}
	}
}

func TestCheckTrackPrecheckAndFallback(t *testing.T) {
	// Default on desktop/non-Android: graceful fallback with Known=false, Supported=true.
	SetProbeForTest(nil)
	res := CheckTrack("1080P · AV1", "", 1920, 1080)
	if !res.Supported || res.Known {
		t.Fatalf("desktop fallback should be Supported=true Known=false, got %+v", res)
	}

	// Mock Android phone without AV1 decoder.
	SetProbeForTest(func(mime string, w, h int) (bool, bool) {
		if mime == "video/av01" {
			return false, true
		}
		if mime == "audio/flac" {
			return false, true
		}
		return true, true
	})
	defer SetProbeForTest(nil)

	av1 := CheckTrack("1080P · AV1", "", 1920, 1080)
	if av1.Supported || !av1.Known || av1.Message != "这台手机不能播放 AV1 视频" {
		t.Fatalf("expected AV1 unsupported message, got %+v", av1)
	}

	flac := CheckTrack("1080P · AVC", "FLAC", 1920, 1080)
	if flac.Supported || !flac.Known || flac.Message != "这台手机不能播放 FLAC 音轨" {
		t.Fatalf("expected FLAC unsupported message, got %+v", flac)
	}

	avc := CheckTrack("1080P · AVC", "AAC", 1920, 1080)
	if !avc.Supported || !avc.Known {
		t.Fatalf("expected AVC supported, got %+v", avc)
	}
}
