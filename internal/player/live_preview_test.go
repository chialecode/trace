package player

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

// buildSyntheticGrowingFMP4Parts creates an fMP4 init segment (video track 1 + audio track 2)
// and N 1-second media chunks matching either:
//   - Path A (multiTrackMoof=false): separate video and audio moof+mdat fragments with arbitrary baseTFDT and audio sub-frame overlap
//   - Path B (multiTrackMoof=true): FLV-remux style single moof+mdat per GOP containing both video and audio trafs
func buildSyntheticGrowingFMP4Parts(t *testing.T, multiTrackMoof bool, baseTFDTSec uint64, audioOverlapTicks uint64, numChunks int) (initBytes []byte, chunks [][]byte) {
	t.Helper()
	vRaw, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aRaw, err := download.BuildSyntheticAudioM4S(47)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}

	vParsed, err := mp4.DecodeFile(bytes.NewReader(vRaw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || vParsed.Init == nil || len(vParsed.Segments) == 0 {
		t.Fatalf("DecodeFile video: %v", err)
	}
	aParsed, err := mp4.DecodeFile(bytes.NewReader(aRaw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || aParsed.Init == nil || len(aParsed.Segments) == 0 {
		t.Fatalf("DecodeFile audio: %v", err)
	}

	vSampleList, err := vParsed.Segments[0].Fragments[0].GetFullSamples(vParsed.Init.Moov.Mvex.Trexs[0])
	if err != nil || len(vSampleList) == 0 {
		t.Fatalf("GetFullSamples video: %v", err)
	}
	aSampleList, err := aParsed.Segments[0].Fragments[0].GetFullSamples(aParsed.Init.Moov.Mvex.Trexs[0])
	if err != nil || len(aSampleList) == 0 {
		t.Fatalf("GetFullSamples audio: %v", err)
	}

	vScale := vParsed.Init.Moov.Traks[0].Mdia.Mdhd.Timescale
	aScale := aParsed.Init.Moov.Traks[0].Mdia.Mdhd.Timescale

	// Build combined init segment: Track 1 = Video (AVC), Track 2 = Audio (AAC).
	initSeg := mp4.CreateEmptyInit()
	vTrak := vParsed.Init.Moov.Traks[0]
	vTrak.Tkhd.TrackID = 1
	initSeg.Moov.AddChild(vTrak)
	initSeg.Moov.Mvex.AddChild(mp4.CreateTrex(1))

	aTrak := aParsed.Init.Moov.Traks[0]
	aTrak.Tkhd.TrackID = 2
	initSeg.Moov.AddChild(aTrak)
	initSeg.Moov.Mvex.AddChild(mp4.CreateTrex(2))

	var initBuf bytes.Buffer
	if err := initSeg.Encode(&initBuf); err != nil {
		t.Fatalf("initSeg.Encode: %v", err)
	}
	initBytes = initBuf.Bytes()

	var vDurPerChunk uint64
	for _, s := range vSampleList {
		vDurPerChunk += uint64(s.Dur)
	}
	var aDurPerChunk uint64
	for _, s := range aSampleList {
		aDurPerChunk += uint64(s.Dur)
	}

	vBase := baseTFDTSec * uint64(vScale)
	aBase := baseTFDTSec * uint64(aScale)

	for idx := 0; idx < numChunks; idx++ {
		vStart := vBase + uint64(idx)*vDurPerChunk
		aStart := aBase + uint64(idx)*aDurPerChunk
		if idx > 0 && audioOverlapTicks > 0 && aStart > audioOverlapTicks {
			aStart -= uint64(idx) * audioOverlapTicks
		}

		var chunkBuf bytes.Buffer
		if multiTrackMoof {
			frag, err := mp4.CreateMultiTrackFragment(uint32(idx+1), []uint32{1, 2})
			if err != nil {
				t.Fatalf("CreateMultiTrackFragment: %v", err)
			}
			vCur := vStart
			for _, s := range vSampleList {
				cp := s
				cp.DecodeTime = vCur
				vCur += uint64(cp.Dur)
				frag.AddFullSampleToTrack(cp, 1)
			}
			aCur := aStart
			for _, s := range aSampleList {
				cp := s
				cp.DecodeTime = aCur
				aCur += uint64(cp.Dur)
				frag.AddFullSampleToTrack(cp, 2)
			}
			if err := frag.Encode(&chunkBuf); err != nil {
				t.Fatalf("multi-track frag.Encode: %v", err)
			}
		} else {
			vFrag, err := mp4.CreateFragment(uint32(idx*2+1), 1)
			if err != nil {
				t.Fatalf("CreateFragment video: %v", err)
			}
			vCur := vStart
			for _, s := range vSampleList {
				cp := s
				cp.DecodeTime = vCur
				vCur += uint64(cp.Dur)
				vFrag.AddFullSample(cp)
			}
			if err := vFrag.Encode(&chunkBuf); err != nil {
				t.Fatalf("vFrag.Encode: %v", err)
			}

			aFrag, err := mp4.CreateFragment(uint32(idx*2+2), 2)
			if err != nil {
				t.Fatalf("CreateFragment audio: %v", err)
			}
			aCur := aStart
			for _, s := range aSampleList {
				cp := s
				cp.DecodeTime = aCur
				aCur += uint64(cp.Dur)
				aFrag.AddFullSample(cp)
			}
			if err := aFrag.Encode(&chunkBuf); err != nil {
				t.Fatalf("aFrag.Encode: %v", err)
			}
		}
		chunks = append(chunks, chunkBuf.Bytes())
	}
	return initBytes, chunks
}

func TestOpenLivePreviewGrowingHLSAndFLVFMP4AndDanmakuSuppressed(t *testing.T) {
	cases := []struct {
		name              string
		multiTrackMoof    bool
		baseTFDTSec       uint64
		audioOverlapTicks uint64
	}{
		{
			name:              "hls_fmp4_nonzero_tfdt_and_subframe_overlap",
			multiTrackMoof:    false,
			baseTFDTSec:       14400, // 4h broadcast uptime in tfdt
			audioOverlapTicks: 128,   // sub-frame AAC overlap across segments
		},
		{
			name:              "flv_remux_multitrack_gop_fmp4",
			multiTrackMoof:    true,
			baseTFDTSec:       0,
			audioOverlapTicks: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initBytes, chunks := buildSyntheticGrowingFMP4Parts(t, tc.multiTrackMoof, tc.baseTFDTSec, tc.audioOverlapTicks, 4)
			tmp := t.TempDir()
			partPath := filepath.Join(tmp, "part-001.mp4.part")
			finalPath := filepath.Join(tmp, "part-001.mp4")

			writer, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				t.Fatalf("OpenFile writer: %v", err)
			}
			defer func() {
				if writer != nil {
					_ = writer.Close()
				}
			}()

			var committed atomic.Int64
			var active atomic.Bool
			active.Store(true)

			appendBytes := func(b []byte) {
				t.Helper()
				if _, err := writer.Write(b); err != nil {
					t.Fatalf("writer.Write: %v", err)
				}
				if err := writer.Sync(); err != nil {
					t.Fatalf("writer.Sync: %v", err)
				}
				committed.Add(int64(len(b)))
			}

			// Start with ONLY init + chunk 0 (~1.0s) on disk while active=true!
			appendBytes(initBytes)
			appendBytes(chunks[0])

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			dec, err := OpenLivePreview(ctx, partPath, LivePreviewOptions{
				IsActive:       active.Load,
				CommittedBytes: committed.Load,
				PollInterval:   20 * time.Millisecond,
				InitialWait:    2 * time.Second,
				TailWindow:     -1,
			})
			if err != nil {
				t.Fatalf("OpenLivePreview: %v", err)
			}

			ld, ok := dec.(LiveDecoder)
			if !ok || !ld.IsLive() {
				_ = dec.Close()
				t.Fatalf("expected LiveDecoder with IsLive()=true, got %T", dec)
			}
			if ld.AvailableDuration() < 800*time.Millisecond {
				_ = dec.Close()
				t.Fatalf("unexpected initial AvailableDuration %v", ld.AvailableDuration())
			}

			sink := NewNullAudioSink()
			now := time.Now()
			sink.NowFunc = func() time.Time { return now }

			ctrl := NewController(Options{
				AudioSink:      sink,
				DanmakuVisible: true,
				SubtitleLang:   "zh-CN",
			})
			if err := ctrl.LoadLiveDecoder(partPath, dec); err != nil {
				t.Fatalf("LoadLiveDecoder: %v", err)
			}
			defer ctrl.Close()

			if !ctrl.IsLive() {
				t.Fatal("expected ctrl.IsLive() == true")
			}

			// Verify danmaku and subtitles are completely suppressed in live preview mode.
			_ = ctrl.LoadDanmakuXML([]byte(`<i><d p="0.2,1,25,16777215,0,0,0,1">live-dm</d></i>`))
			ctrl.SetSubtitles([]SubtitleTrack{{
				Lang: "zh-CN",
				Cues: []SubtitleCue{{StartSec: 0, EndSec: 10, Text: "live-sub"}},
			}})

			snap0 := ctrl.Snapshot(1280, 720)
			if !snap0.Open || !snap0.Live || snap0.LiveEnded || snap0.DurationSec != 0 {
				t.Fatalf("unexpected initial live snapshot: %+v", snap0)
			}
			if snap0.DanmakuVisible || snap0.DanmakuCount != 0 || len(snap0.VisibleDanmaku) != 0 || len(snap0.SubtitleOptions) != 0 || snap0.ActiveSubtitle != "" {
				t.Fatalf("expected danmaku & subtitles suppressed in live mode, got %+v", snap0)
			}
			if ctrl.Bitmap() == nil {
				t.Fatal("expected initial non-nil Bitmap")
			}

			// Start playback and advance into chunk 0.
			ctrl.Play()
			for step := 0; step < 6; step++ {
				now = now.Add(100 * time.Millisecond)
				ctrl.Advance(0)
				time.Sleep(15 * time.Millisecond)
			}
			if pos1 := ctrl.Snapshot(1280, 720).PositionSec; pos1 < 0.45 {
				t.Fatalf("expected playback clock > 0.45s on chunk 0, got %.3fs", pos1)
			}

			// Append chunks 1, 2, 3 dynamically (~1s -> ~4s) while player is open and playing!
			appendBytes(chunks[1])
			appendBytes(chunks[2])
			appendBytes(chunks[3])

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if ld.AvailableDuration() >= 3500*time.Millisecond {
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
			if avail := ld.AvailableDuration(); avail < 3500*time.Millisecond {
				t.Fatalf("expected AvailableDuration >= 3.5s after appending 4 chunks, got %v", avail)
			}

			// Advance playback past 2.6s (well beyond chunk 0's 1.0s boundary!).
			for step := 0; step < 22; step++ {
				now = now.Add(100 * time.Millisecond)
				ctrl.Advance(0)
				time.Sleep(15 * time.Millisecond)
			}
			snapMid := ctrl.Snapshot(1280, 720)
			if !snapMid.Playing || snapMid.PositionSec < 2.4 {
				t.Fatalf("expected playback to advance past 2.4s across newly appended fragments, got %+v", snapMid)
			}

			// Also verify audio samples decode cleanly across appended fragments.
			pcm := make([]float32, 4096)
			totalPCM := 0
			for i := 0; i < 12; i++ {
				n, rErr := dec.ReadAudioSamples(pcm)
				totalPCM += n
				if rErr != nil || totalPCM >= 48000*2*2 { // >= 2s stereo 48kHz
					break
				}
			}
			if dec.SampleRate() > 0 && totalPCM < 48000 {
				t.Fatalf("expected decoded audio samples across fragments, got %d", totalPCM)
			}

			// Mark recording ended and advance to end of buffered stream.
			active.Store(false)
			deadline = time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline) && !ld.LiveEnded() {
				time.Sleep(15 * time.Millisecond)
			}
			now = now.Add(3 * time.Second)
			ctrl.Advance(0)
			snapEnd := ctrl.Snapshot(1280, 720)
			if !snapEnd.LiveEnded || snapEnd.Playing {
				t.Fatalf("expected LiveEnded=true and Playing=false at tail EOF, got %+v", snapEnd)
			}

			// Close player and writer, then IMMEDIATELY rename & remove .part with zero delay (Windows handle check).
			if err := ctrl.Close(); err != nil {
				t.Fatalf("ctrl.Close: %v", err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("writer.Close: %v", err)
			}
			writer = nil

			if err := os.Rename(partPath, finalPath); err != nil {
				t.Fatalf("immediate os.Rename after ctrl.Close failed (handle leak): %v", err)
			}
			if err := os.Remove(finalPath); err != nil {
				t.Fatalf("immediate os.Remove after rename failed: %v", err)
			}
		})
	}
}

func TestLivePreviewBlockedTailImmediateUnblockAndWindowsRename(t *testing.T) {
	initBytes, chunks := buildSyntheticGrowingFMP4Parts(t, true, 0, 0, 1)
	tmp := t.TempDir()
	partPath := filepath.Join(tmp, "blocked-tail.mp4.part")
	finalPath := filepath.Join(tmp, "blocked-tail.mp4")

	if err := os.WriteFile(partPath, append(append([]byte(nil), initBytes...), chunks[0]...), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var active atomic.Bool
	active.Store(true)

	dec, err := OpenLivePreview(context.Background(), partPath, LivePreviewOptions{
		IsActive:     active.Load,
		PollInterval: 20 * time.Millisecond,
		TailWindow:   -1,
	})
	if err != nil {
		t.Fatalf("OpenLivePreview: %v", err)
	}

	ctrl := NewController(Options{AudioSink: NewNullAudioSink()})
	if err := ctrl.LoadLiveDecoder(partPath, dec); err != nil {
		t.Fatalf("LoadLiveDecoder: %v", err)
	}
	ctrl.Play()
	ctrl.Advance(1500 * time.Millisecond)

	// Drain audio until ReadAudioSamples blocks waiting for fragment #2.
	audioDone := make(chan error, 1)
	if dec.SampleRate() > 0 {
		go func() {
			buf := make([]float32, 8192)
			for {
				_, rErr := dec.ReadAudioSamples(buf)
				if rErr != nil {
					audioDone <- rErr
					return
				}
			}
		}()
	} else {
		audioDone <- io.EOF
	}

	// Give video worker and audio reader 80ms to park inside blocked tail read.
	time.Sleep(80 * time.Millisecond)

	tClose := time.Now()
	if err := ctrl.Close(); err != nil {
		t.Fatalf("ctrl.Close: %v", err)
	}
	closeElapsed := time.Since(tClose)
	if closeElapsed > 500*time.Millisecond {
		t.Fatalf("ctrl.Close blocked too long on active live tail: %v", closeElapsed)
	}

	select {
	case <-audioDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("blocked ReadAudioSamples goroutine did not exit on ctrl.Close")
	}

	// Immediately rename & delete on Windows with 0ms sleep to prove all OS/COM handles are released.
	if err := os.Rename(partPath, finalPath); err != nil {
		t.Fatalf("os.Rename immediately after Close failed: %v", err)
	}
	if err := os.Remove(finalPath); err != nil {
		t.Fatalf("os.Remove failed: %v", err)
	}
}

func TestLivePreviewTailWindowJoinsNearLiveEdge(t *testing.T) {
	initBytes, chunks := buildSyntheticGrowingFMP4Parts(t, true, 0, 0, 12)
	tmp := t.TempDir()
	partPath := filepath.Join(tmp, "long-session.mp4.part")

	var full bytes.Buffer
	full.Write(initBytes)
	for _, c := range chunks {
		full.Write(c)
	}
	if err := os.WriteFile(partPath, full.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tStart := time.Now()
	dec, err := OpenLivePreview(context.Background(), partPath, LivePreviewOptions{
		IsActive:     func() bool { return true },
		PollInterval: 30 * time.Millisecond,
		TailWindow:   3 * time.Second,
	})
	if err != nil {
		t.Fatalf("OpenLivePreview: %v", err)
	}
	defer dec.Close()

	if elapsed := time.Since(tStart); elapsed >= 5*time.Second {
		t.Fatalf("first open took %v >= 5s", elapsed)
	}
	ld := dec.(LiveDecoder)
	avail := ld.AvailableDuration()
	if avail < 2500*time.Millisecond || avail > 5500*time.Millisecond {
		t.Fatalf("expected TailWindow=3s to join near 12s live edge (avail in [2.5s, 5.5s]), got %v", avail)
	}
	rgba, _, err := dec.DecodeVideoFrameAt(0, nil)
	if err != nil || rgba == nil {
		t.Fatalf("DecodeVideoFrameAt(0) after TailWindow join: rgba=%v err=%v", rgba != nil, err)
	}
}

func TestOpenLivePreviewRejectsUnsupportedContainersAndReleasesHandle(t *testing.T) {
	tmp := t.TempDir()

	// 1. MPEG-TS sync byte 0x47 payload.
	tsPath := filepath.Join(tmp, "unsupported-ts.mp4.part")
	tsBytes := bytes.Repeat([]byte{0x47, 0x40, 0x00, 0x10}, 64)
	if err := os.WriteFile(tsPath, tsBytes, 0o644); err != nil {
		t.Fatalf("WriteFile TS: %v", err)
	}
	_, err := OpenLivePreview(context.Background(), tsPath, LivePreviewOptions{
		IsActive:    func() bool { return false },
		InitialWait: 200 * time.Millisecond,
	})
	if !errors.Is(err, ErrLivePreviewUnsupported) {
		t.Fatalf("expected ErrLivePreviewUnsupported for MPEG-TS, got %v", err)
	}
	if err := os.Remove(tsPath); err != nil {
		t.Fatalf("expected TS file handle closed on error: %v", err)
	}

	// 2. Non-fragmented MP4 (moov without mvex).
	vPath := filepath.Join(tmp, "v.m4s")
	aPath := filepath.Join(tmp, "a.m4s")
	muxedPath := filepath.Join(tmp, "unfragmented.mp4")
	vBytes, _ := download.BuildSyntheticVideoM4S(15, 30)
	aBytes, _ := download.BuildSyntheticAudioM4S(22)
	_ = os.WriteFile(vPath, vBytes, 0o644)
	_ = os.WriteFile(aPath, aBytes, 0o644)
	if _, err := download.MuxDASHFiles(vPath, aPath, muxedPath); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}
	_, err = OpenLivePreview(context.Background(), muxedPath, LivePreviewOptions{
		IsActive:    func() bool { return false },
		InitialWait: 200 * time.Millisecond,
	})
	if !errors.Is(err, ErrLivePreviewUnsupported) {
		t.Fatalf("expected ErrLivePreviewUnsupported for non-fragmented MP4, got %v", err)
	}
	if err := os.Remove(muxedPath); err != nil {
		t.Fatalf("expected non-fragmented MP4 file handle closed on error: %v", err)
	}
}

func TestEndToEndLiveRecorderAndPlayerPreviewFinalizeWithoutHandleConflict(t *testing.T) {
	initBytes, chunks := buildSyntheticGrowingFMP4Parts(t, false, 3600, 128, 3)

	var segSeq atomic.Int32
	var httpHits atomic.Int64
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"3001": map[string]any{
					"uid": 3001, "room_id": 9301, "title": "E2E Live Preview", "uname": "Host3001", "live_status": 1, "live_time": 1794024000,
				},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 9301, "uid": 3001, "live_status": 1, "live_time": 1794024000},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id": 9301, "uid": 3001, "live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{{
							"protocol_name": "http_hls",
							"format": []map[string]any{{
								"format_name": "fmp4",
								"codec": []map[string]any{{
									"codec_name": "avc",
									"current_qn": 10000,
									"base_url":   "/hls/live.m3u8",
									"url_info":   []map[string]any{{"host": srvURL, "extra": "?tok=1"}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	mux.HandleFunc("/hls/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		n := segSeq.Add(1)
		switch {
		case n == 1:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/s0.m4s\n"))
		case n == 2:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:2\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/s1.m4s\n"))
		default:
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:3\n#EXT-X-MAP:URI=\"/hls/init.mp4\"\n#EXTINF:1.0,\n/hls/s2.m4s\n"))
		}
	})
	mux.HandleFunc("/hls/init.mp4", func(w http.ResponseWriter, _ *http.Request) { httpHits.Add(1); _, _ = w.Write(initBytes) })
	mux.HandleFunc("/hls/s0.m4s", func(w http.ResponseWriter, _ *http.Request) { httpHits.Add(1); _, _ = w.Write(chunks[0]) })
	mux.HandleFunc("/hls/s1.m4s", func(w http.ResponseWriter, _ *http.Request) { httpHits.Add(1); _, _ = w.Write(chunks[1]) })
	mux.HandleFunc("/hls/s2.m4s", func(w http.ResponseWriter, _ *http.Request) { httpHits.Add(1); _, _ = w.Write(chunks[2]) })

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "lib.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll cacheDir: %v", err)
	}
	lib, err := library.Open(db, cacheDir)
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}
	cred, _, err := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	if err != nil {
		t.Fatalf("credential.Open: %v", err)
	}
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c; DedeUserID=3001", "rt")
	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	subMgr, _ := subscription.NewManager(db, lib, nil, client, settings.Default(cacheDir).Subscription, nil)
	_, _ = subMgr.Upsert(subscription.Subscription{Mid: 3001, Uname: "Host3001", Enabled: true, LiveNotify: true})

	liveMgr := live.NewManager(lib, subMgr, client, settings.Default(cacheDir).Live, nil, nil)
	defer liveMgr.Stop()

	ctx := context.Background()
	if _, err := liveMgr.PollNow(ctx); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if _, err := liveMgr.StartRecording(ctx, 9301); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	var pv live.RecordingPreview
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		cur, ok := liveMgr.RecordingPreview("9301")
		if ok && cur.PartReady && cur.PreviewSupported && cur.PartPath != "" {
			pv = cur
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !pv.PartReady {
		t.Fatalf("timed out waiting for RecordingPreview PartReady, got %+v", pv)
	}

	// Open real growing .part file in player.Controller while live.Manager continues recording!
	dec, err := OpenLivePreview(ctx, pv.PartPath, LivePreviewOptions{
		IsActive: func() bool {
			cur, ok := liveMgr.RecordingPreview("9301")
			return ok && cur.Recording && cur.PartPath == pv.PartPath
		},
		CommittedBytes: func() int64 {
			cur, _ := liveMgr.RecordingPreview("9301")
			return cur.PartBytes
		},
		PollInterval: 25 * time.Millisecond,
		TailWindow:   -1,
	})
	if err != nil {
		t.Fatalf("OpenLivePreview on active live.Manager .part: %v", err)
	}

	sink := NewNullAudioSink()
	now := time.Now()
	sink.NowFunc = func() time.Time { return now }
	ctrl := NewController(Options{AudioSink: sink})
	if err := ctrl.LoadLiveDecoder(pv.PartPath, dec); err != nil {
		t.Fatalf("LoadLiveDecoder: %v", err)
	}
	ctrl.Play()

	// Wait for recorder to append s1 & s2 while preview plays!
	ld := dec.(LiveDecoder)
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		now = now.Add(80 * time.Millisecond)
		ctrl.Advance(0)
		if ld.AvailableDuration() >= 2800*time.Millisecond && ctrl.Snapshot(1280, 720).PositionSec >= 2.2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if gotPos := ctrl.Snapshot(1280, 720).PositionSec; gotPos < 2.2 {
		t.Fatalf("expected live preview clock >= 2.2s while recorder appends HLS segments, got %.3fs", gotPos)
	}

	// Close preview first, then stop recording, and verify Defragment + Rename succeeds cleanly!
	if err := ctrl.Close(); err != nil {
		t.Fatalf("ctrl.Close: %v", err)
	}
	if _, err := liveMgr.StopRecording(9301); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}

	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := liveMgr.RecordingPreview("9301")
		if !cur.Recording {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	finalMP4 := filepath.Join(pv.ItemDir, "part-001.mp4")
	summary, err := download.InspectMP4File(finalMP4)
	if err != nil || !summary.MoovBeforeMdat || summary.DurationSec < 2.5 {
		t.Fatalf("expected finalized moov-before-mdat %s (dur>=2.5s): summary=%+v err=%v", finalMP4, summary, err)
	}
}

func TestSynthLiveDecoderGrowthAndClose(t *testing.T) {
	var active atomic.Bool
	active.Store(true)

	dec := NewSynthLiveDecoder(640, 360, 30, active.Load)
	dec.SetAvailableDuration(time.Second)

	ctrl := NewController(Options{AudioSink: NewNullAudioSink()})
	if err := ctrl.LoadDecoder("synth://live", dec); err != nil {
		t.Fatalf("LoadDecoder(SynthLiveDecoder): %v", err)
	}
	if !ctrl.IsLive() {
		t.Fatal("expected Controller to auto-detect SynthLiveDecoder as live")
	}
	ctrl.Play()
	ctrl.Advance(800 * time.Millisecond)
	if got := ctrl.Snapshot(640, 360).PositionSec; got < 0.7 {
		t.Fatalf("expected PositionSec >= 0.7, got %.3f", got)
	}

	dec.SetAvailableDuration(3 * time.Second)
	ctrl.Advance(1500 * time.Millisecond)
	if got := ctrl.Snapshot(640, 360).PositionSec; got < 2.2 {
		t.Fatalf("expected PositionSec >= 2.2 after growth, got %.3f", got)
	}

	active.Store(false)
	ctrl.Advance(1 * time.Second)
	snap := ctrl.Snapshot(640, 360)
	if !snap.LiveEnded || snap.Playing {
		t.Fatalf("expected LiveEnded=true and Playing=false, got %+v", snap)
	}
	if err := ctrl.Close(); err != nil {
		t.Fatalf("ctrl.Close: %v", err)
	}
}
