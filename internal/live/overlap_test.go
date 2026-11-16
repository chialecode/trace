package live

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
)

// overlappingLiveFMP4 is two HLS-like fMP4 segments of video (track 1, 30 fps)
// and AAC (track 2, 48 kHz) where the second audio fragment starts 32 ticks
// inside the last frame of the first one, as Bilibili's HLS fMP4 did.
func overlappingLiveFMP4(t *testing.T) []byte {
	t.Helper()
	sps, _ := hex.DecodeString("6764001eacd940a02ff9610000030001000003003c8f162d96")
	pps, _ := hex.DecodeString("68ebecb22c")
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	v := init.AddEmptyTrack(30000, "video", "und")
	if err := v.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
		t.Fatal(err)
	}
	v.Tkhd.Width, v.Tkhd.Height = mp4.Fixed32(640<<16), mp4.Fixed32(360<<16)
	a := init.AddEmptyTrack(48000, "audio", "und")
	if err := a.SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	videoSample := append([]byte{0, 0, 0, byte(len(sps))}, sps...)
	audioSample := []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}
	sync := mp4.SampleFlags{SampleDependsOn: 2}.Encode()
	seq := uint32(1)
	for segment := 0; segment < 2; segment++ {
		vf, _ := mp4.CreateFragment(seq, 1)
		seq++
		for i := 0; i < 30; i++ {
			vf.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Flags: sync, Dur: 1000, Size: uint32(len(videoSample))}, DecodeTime: uint64(segment*30+i) * 1000, Data: videoSample})
		}
		if err := vf.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		af, _ := mp4.CreateFragment(seq, 2)
		seq++
		start := uint64(segment) * 47 * 1024
		if segment == 1 {
			start -= 32 // inside the last frame of the first segment
		}
		for i := 0; i < 47; i++ {
			af.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Flags: sync, Dur: 1024, Size: uint32(len(audioSample))}, DecodeTime: start + uint64(i)*1024, Data: audioSample})
		}
		if err := af.Encode(&buf); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestFinalizeLivePartTrimsSubFrameFragmentOverlap(t *testing.T) {
	raw := overlappingLiveFMP4(t)
	// mp4ff alone rejects this overlap, which left real recordings as ".part".
	parsed, err := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		t.Fatal(err)
	}
	if err := mp4.Defragment(parsed, bytes.NewReader(raw), &bytes.Buffer{}); err == nil {
		t.Fatal("fixture does not reproduce the overlap Defragment rejects")
	}
	dir := t.TempDir()
	part := filepath.Join(dir, "part-001.mp4.part")
	if err := os.WriteFile(part, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := finalizeLivePart(context.Background(), part, filepath.Join(dir, "part-001.mp4"))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !summary.MoovBeforeMdat || summary.DurationSec < 1.9 || summary.DurationSec > 2.1 || len(summary.Handlers) != 2 {
		t.Fatalf("converted file = %+v", summary)
	}
}

// A segment whose final name holds raw fragments (a ".part" renamed by hand,
// then accepted by an older recovery with no duration) is converted at start.
func TestRecoveryConvertsUnconvertedSegmentUnderFinalName(t *testing.T) {
	mgr, lib, _ := regressionRecorder(t, http.NotFoundHandler())
	session := regressionLiveSession(t, lib)
	raw := overlappingLiveFMP4(t)
	final := filepath.Join(session.itemDir, "part-001.mp4")
	if err := os.WriteFile(final, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	meta := session.itemMeta
	meta.PendingParts, meta.Status = nil, "complete"
	meta.Media = []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Bytes: int64(len(raw))}}
	if _, err := lib.SaveItem(session.itemDir, meta); err != nil {
		t.Fatal(err)
	}
	mgr.RecoverOrphanParts()
	got, _, err := lib.GetItem(meta.ItemKey)
	if err != nil || got.Status != "complete" || len(got.PendingParts) != 0 {
		t.Fatalf("recovered item = %+v %v", got, err)
	}
	summary, err := download.InspectMP4FileContext(context.Background(), final)
	if err != nil || !summary.MoovBeforeMdat || summary.DurationSec <= 0 {
		t.Fatalf("segment still unconverted: %+v %v", summary, err)
	}
	if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
		t.Fatalf("raw fragments left behind: %v", err)
	}
	if len(got.Media) != 1 || got.Media[0].Duration <= 0 || !library.VerifyMediaFile(got.ItemDir, got.Media[0], true) {
		t.Fatalf("media row = %+v", got.Media)
	}
	// A second start leaves the converted file alone.
	mgr.RecoverOrphanParts()
	again, _ := download.InspectMP4FileContext(context.Background(), final)
	if again.SHA256 != summary.SHA256 {
		t.Fatal("a converted segment was converted again")
	}
}
